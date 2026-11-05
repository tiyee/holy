package log

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/slogtest"
	"testing/synctest"
	"time"
)

// ---------- 测试工具 ----------

func newTestLogger(t *testing.T, buf *bytes.Buffer, mutate func(*Config)) *Logger {
	t.Helper()
	cfg := Config{Writer: buf, Level: "debug", Format: "json"}
	if mutate != nil {
		mutate(&cfg)
	}
	l, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return l
}

func parseLines(t *testing.T, s string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("invalid json %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

// ---------- JSON 编码 ----------

func TestJSONBasicFields(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(t, &buf, nil)
	l.Info("hello", String("who", "world"), Int("n", 3), Bool("ok", true), Float64("f", 1.5))

	lines := parseLines(t, buf.String())
	if len(lines) != 1 {
		t.Fatalf("want 1 line, got %d", len(lines))
	}
	m := lines[0]
	if m["msg"] != "hello" || m["level"] != "INFO" || m["who"] != "world" || m["n"] != float64(3) || m["ok"] != true || m["f"] != 1.5 {
		t.Fatalf("bad record: %v", m)
	}
	if _, ok := m["time"]; !ok {
		t.Fatal("missing time")
	}
}

func TestJSONValueTypes(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(t, &buf, nil)
	l.Info("types",
		Uint64("u", 1<<63),
		Int64("i", -9),
		Duration("d", 1500*time.Millisecond),
		Any("when", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
		Err(errors.New("boom")),
		Any("raw", []byte("hi")),
		Any("obj", map[string]int{"x": 1}),
		Any("nil", nil),
		Any("strs", []string{"a", "b"}),
	)
	m := parseLines(t, buf.String())[0]
	if m["u"] != float64(1<<63) || m["i"] != float64(-9) {
		t.Fatalf("numeric: %v", m)
	}
	if m["d"] != 1.5 { // Duration 以秒输出
		t.Fatalf("duration = %v, want 1.5", m["d"])
	}
	if m["error"] != "boom" {
		t.Fatalf("error = %v", m["error"])
	}
	if m["raw"] != "aGk=" { // []byte base64
		t.Fatalf("bytes = %v", m["raw"])
	}
	if m["nil"] != nil {
		t.Fatalf("nil = %v", m["nil"])
	}
	if m["strs"].([]any)[0] != "a" {
		t.Fatalf("strs = %v", m["strs"])
	}
	obj := m["obj"].(map[string]any)
	if obj["x"] != float64(1) {
		t.Fatalf("obj = %v", m["obj"])
	}
}

func TestJSONStringEscaping(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(t, &buf, nil)
	msg := "quote:\" backslash:\\ newline:\n tab:\t ctrl:\x01 unicode:✓ invalid:\xff"
	l.Info(msg)
	m := parseLines(t, buf.String())[0]
	got := m["msg"].(string)
	want := strings.ReplaceAll(msg, "\xff", "\ufffd")
	if got != want {
		t.Fatalf("round trip failed:\n got=%q\nwant=%q", got, want)
	}
}

func TestJSONGroups(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(t, &buf, nil)
	l.Info("g",
		Group("outer", String("a", "1"), Group("inner", Int("b", 2))),
		Group("empty"), // 空组不输出
		slog.Attr{Key: "", Value: slog.GroupValue(String("flat", "y"))}, // 匿名组平铺
	)
	m := parseLines(t, buf.String())[0]
	if _, ok := m["empty"]; ok {
		t.Fatal("empty group should be omitted")
	}
	if m["flat"] != "y" {
		t.Fatalf("anonymous group: %v", m)
	}
	outer := m["outer"].(map[string]any)
	if outer["a"] != "1" {
		t.Fatalf("outer.a = %v", outer["a"])
	}
	inner := outer["inner"].(map[string]any)
	if inner["b"] != float64(2) {
		t.Fatalf("outer.inner.b = %v", inner["b"])
	}
}

func TestJSONWithGroupSemantics(t *testing.T) {
	var buf bytes.Buffer
	lv := new(slog.LevelVar)
	h := newJSONHandler(&buf, lv, "", false, false, false)
	hg := h.WithGroup("g")
	r := slog.NewRecord(time.Now(), LevelInfo, "m", 0)
	r.AddAttrs(String("k", "v"))
	if err := hg.Handle(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"g":{"k":"v"}`) {
		t.Fatalf("with group output: %s", buf.String())
	}

	// 无字段的空组
	buf.Reset()
	r2 := slog.NewRecord(time.Now(), LevelInfo, "m2", 0)
	_ = hg.Handle(context.Background(), r2)
	if _, ok := parseLines(t, buf.String())[0]["g"]; ok {
		t.Fatalf("empty group should be omitted: %s", buf.String())
	}

	// WithGroup 后再 WithAttrs：字段落入组内，组对后续字段保持打开
	buf.Reset()
	hga := hg.WithAttrs([]slog.Attr{Int("a", 1)})
	r3 := slog.NewRecord(time.Now(), LevelInfo, "m3", 0)
	r3.AddAttrs(Int("b", 2))
	_ = hga.Handle(context.Background(), r3)
	if !strings.Contains(buf.String(), `"g":{"a":1,"b":2}`) {
		t.Fatalf("group+attrs output: %s", buf.String())
	}
}

func TestWithAttrsCacheAndIsolation(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(t, &buf, nil)
	base := l.With(String("svc", "api"))
	child1 := base.With(String("child", "1"))
	child2 := base.With(Int("n", 2))

	child1.Info("from child1")
	child2.Info("from child2")
	base.Info("from base")

	lines := parseLines(t, buf.String())
	if lines[0]["child"] != "1" || lines[0]["svc"] != "api" {
		t.Fatalf("child1: %v", lines[0])
	}
	if _, has := lines[1]["child"]; has {
		t.Fatalf("child2 leaked child attr: %v", lines[1])
	}
	if lines[1]["n"] != float64(2) {
		t.Fatalf("child2: %v", lines[1])
	}
	if _, has := lines[2]["child"]; has {
		t.Fatalf("base leaked child attrs: %v", lines[2])
	}
	if lines[2]["svc"] != "api" {
		t.Fatalf("base: %v", lines[2])
	}
}

func TestSkipAttrNoDanglingComma(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(t, &buf, nil)
	l.Info("skip", Skip(), String("k", "v"), Skip())
	m := parseLines(t, buf.String())[0]
	if m["k"] != "v" || len(m) != 4 { // time/level/msg/k
		t.Fatalf("skip handling: %v", m)
	}
}

func TestAddSource(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(t, &buf, func(c *Config) { c.AddSource = true })
	l.Info("where")
	m := parseLines(t, buf.String())[0]
	src := m["source"].(map[string]any)
	if !strings.HasSuffix(src["file"].(string), "log_test.go") {
		t.Fatalf("source file = %v", src["file"])
	}
	if src["line"].(float64) < 1 {
		t.Fatalf("source line = %v", src["line"])
	}
}

// ---------- 级别 ----------

func TestLevelFilterAndDynamic(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(t, &buf, nil)
	if !l.Enabled(LevelDebug) {
		t.Fatal("debug should be enabled")
	}
	l.Debug("d")
	l.SetLevel(LevelWarn)
	if l.Enabled(LevelInfo) {
		t.Fatal("info should be disabled after SetLevel")
	}
	l.Info("hidden")
	l.Warn("shown")
	lines := parseLines(t, buf.String())
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d: %s", len(lines), buf.String())
	}
	if lines[0]["msg"] != "d" || lines[1]["msg"] != "shown" {
		t.Fatalf("unexpected lines: %s", buf.String())
	}
}

func TestExtendedLevels(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(t, &buf, nil)
	l.SetLevel(LevelDPanic)
	l.DPanic("dp") // 生产模式不 panic
	lines := parseLines(t, buf.String())
	if lines[0]["level"] != "DPANIC" {
		t.Fatalf("level name = %v", lines[0]["level"])
	}
}

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"":        LevelInfo,
		"info":    LevelInfo,
		"DEBUG":   LevelDebug,
		"warn":    LevelWarn,
		"error":   LevelError,
		"dpanic":  LevelDPanic,
		"PANIC":   LevelPanic,
		"fatal":   LevelFatal,
		"8":       LevelError,
		"ERROR+2": slog.Level(10),
	}
	for in, want := range cases {
		got, err := ParseLevel(in)
		if err != nil || got != want {
			t.Fatalf("ParseLevel(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := ParseLevel("bogus"); err == nil {
		t.Fatal("bogus level should error")
	}
}

// ---------- console ----------

func TestConsoleFormat(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(t, &buf, func(c *Config) { c.Format = "console" })
	l.With(String("k", "v")).Warn("careful", String("msg2", "a b"))
	s := buf.String()
	if !strings.Contains(s, "WARN") || !strings.Contains(s, "careful") || !strings.Contains(s, "k=v") {
		t.Fatalf("console output: %q", s)
	}
	// 带空格的值应被 strconv.Quote 包裹
	if !strings.Contains(s, `msg2="a b"`) {
		t.Fatalf("quoted value missing: %q", s)
	}
	if strings.Contains(s, "\x1b[") {
		t.Fatalf("color should be off unless development: %q", s)
	}
}

// ---------- 采样 ----------

func TestSampler(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(t, &buf, func(c *Config) {
		c.Sampler = &SamplerConfig{First: 2, Thereafter: 2, Tick: time.Hour}
	})
	for range 6 {
		l.Info("sampled")
	}
	// 前 2 条必记 + 之后每 2 条记 1 条（第 4、6 条）= 4 条
	if got := len(parseLines(t, buf.String())); got != 4 {
		t.Fatalf("sampled %d lines, want 4:\n%s", got, buf.String())
	}

	// error 级别同样参与采样：First=2 + 之后每 2 条 1 条（4/6/8/10）= 6
	buf.Reset()
	for range 10 {
		l.Error("urgent")
	}
	if got := len(parseLines(t, buf.String())); got != 6 {
		t.Fatalf("error sampled %d lines, want 6", got)
	}

	// DPanic 以上不采样
	buf.Reset()
	for range 5 {
		l.log(LevelFatal, "fatal", nil)
	}
	if got := len(parseLines(t, buf.String())); got != 5 {
		t.Fatalf("fatal sampled %d lines, want 5", got)
	}
}

// ---------- 异步 ----------

func TestAsyncFlushAndSync(t *testing.T) {
	var out safeBuffer
	aw := newAsyncWriter(&out, AsyncConfig{MaxQueueBytes: 1 << 20, FlushInterval: time.Hour})
	h := newJSONHandler(aw, new(slog.LevelVar), "", false, false, false)
	lg := newLogger(h, nil, []io.Closer{aw}, false, false)
	for i := 0; i < 50; i++ {
		lg.Info("async", Int("i", i))
	}
	// 注意：消费者的首轮迭代在启动时立即执行，先于它入队的写入可能被
	// 即时消费，因此不保证 Sync 前零输出；只断言 Sync 后全部落盘。
	if err := lg.Sync(); err != nil {
		t.Fatal(err)
	}
	if got := len(parseLines(t, out.String())); got != 50 {
		t.Fatalf("after Sync got %d lines, want 50", got)
	}
	if err := aw.Close(); err != nil {
		t.Fatal(err)
	}
}

type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *safeBuffer) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Len()
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

type nopSyncWriter struct{ io.Writer }

type muWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (m *muWriter) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.w.Write(p)
}

func TestAsyncCloseNoLoss(t *testing.T) {
	var out safeBuffer
	aw := newAsyncWriter(&out, AsyncConfig{MaxQueueBytes: 1 << 20, FlushInterval: time.Hour})
	h := newJSONHandler(aw, new(slog.LevelVar), "", false, false, false)
	lg := newLogger(h, nil, nil, false, false)
	for i := 0; i < 100; i++ {
		lg.Info("close-flush", Int("i", i))
	}
	if err := aw.Close(); err != nil {
		t.Fatal(err)
	}
	if got := len(parseLines(t, out.String())); got != 100 {
		t.Fatalf("Close flushed %d lines, want 100", got)
	}
	// 关闭后写入返回错误
	if _, err := aw.Write([]byte("x")); err == nil {
		t.Fatal("write after close should fail")
	}
}

func TestAsyncDrop(t *testing.T) {
	// 内存上限语义：在途字节超限即丢弃并计数。消费间隔拉长到 1h，消费者
	// 首轮空转后休眠，写入确定性地堆积超限。
	var out safeBuffer
	aw := newAsyncWriter(&out, AsyncConfig{MaxQueueBytes: 4096, FlushInterval: time.Hour})
	h := newJSONHandler(aw, new(slog.LevelVar), "", false, false, false)
	lg := newLogger(h, nil, []io.Closer{aw}, false, false)
	for range 100 {
		lg.Info(strings.Repeat("x", 1024))
	}
	if aw.dropped.Load() == 0 {
		t.Fatal("expected dropped > 0")
	}
	// Sync 触发一轮消费，队列中的存量日志全部落盘
	if err := aw.Sync(); err != nil {
		t.Fatal(err)
	}
	if got := len(parseLines(t, out.String())); got == 0 {
		t.Fatal("expected some lines drained after Sync")
	}
	if err := aw.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAsyncWriteDropUnit(t *testing.T) {
	// 不启动消费循环，直接构造超限状态验证 Write 的丢弃/计数路径
	w := &asyncWriter{
		maxQueueBytes: 1,
		interval:      time.Hour,
		forceFlush:    make(chan struct{}),
	}
	w.cond = sync.NewCond(&w.mu)
	w.acceptedCond = sync.NewCond(&w.stateMu)
	w.q.size.Store(1)            // 直接置满
	w.reportOverflow.Store(true) // 预置限频标记：丢弃静默返回成功
	if _, err := w.Write([]byte("drop me")); err != nil {
		t.Fatal(err)
	}
	if got := w.dropped.Load(); got != 1 {
		t.Fatalf("dropped = %d", got)
	}
	if w.accepted.Load() != 0 {
		t.Fatal("静默丢弃后接受计数未归零")
	}
	w.reportOverflow.Store(false)
	if _, err := w.Write([]byte("report overflow")); err == nil {
		t.Fatal("首次溢出必须返回错误")
	}
	if w.accepted.Load() != 0 || w.dropped.Load() != 2 {
		t.Fatal("报告溢出后计数未归零或丢弃数错误")
	}
}

func TestSyncOrdering(t *testing.T) {
	// 回归：Sync 的序号协议——Sync 持 mu 记下 iter 后等待，mu 交接保证
	// 消费者的下一轮 dequeueAll 包含 Sync 之前入队的全部日志。多轮"写入
	// 后立刻 Sync"覆盖各种 interleaving。
	var out safeBuffer
	aw := newAsyncWriter(&out, AsyncConfig{MaxQueueBytes: 1 << 20, FlushInterval: time.Hour})
	h := newJSONHandler(aw, new(slog.LevelVar), "", false, false, false)
	lg := newLogger(h, nil, []io.Closer{aw}, false, false)

	const rounds, perRound = 50, 20
	for r := range rounds {
		for range perRound {
			lg.Info("barrier", Int("r", r))
		}
		if err := lg.Sync(); err != nil {
			t.Fatal(err)
		}
		if got := len(parseLines(t, out.String())); got != (r+1)*perRound {
			t.Fatalf("round %d: after Sync got %d lines, want %d", r, got, (r+1)*perRound)
		}
	}
	if err := aw.Close(); err != nil {
		t.Fatal(err)
	}
}

// chunkRecorder 记录每次底层 Write 收到的字节块，用于行原子性断言。
type chunkRecorder struct {
	mu     sync.Mutex
	chunks [][]byte
}

func (c *chunkRecorder) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.chunks = append(c.chunks, append([]byte(nil), p...))
	return len(p), nil
}

// TestAsyncLineAtomicity 回归：聚合缓冲只在行边界 flush，单次底层 Write
// 必须由完整行组成——bufio 时代余量不足会把行前缀随旧批冲出（两次 write
// 之间多进程可插入写入，行交错）。WriteBufferSize 压到 1 强制逐行 flush。
func TestAsyncLineAtomicity(t *testing.T) {
	out := &chunkRecorder{}
	aw := newAsyncWriter(out, AsyncConfig{MaxQueueBytes: 1 << 20, WriteBufferSize: 1, FlushInterval: time.Hour})
	h := newJSONHandler(aw, new(slog.LevelVar), "", false, false, false)
	lg := newLogger(h, nil, []io.Closer{aw}, false, false)
	const n = 50
	for i := range n {
		lg.Info(strings.Repeat("y", i*7), Int("i", i)) // 行长各异
	}
	if err := aw.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := aw.Close(); err != nil {
		t.Fatal(err)
	}
	out.mu.Lock()
	defer out.mu.Unlock()
	if len(out.chunks) == 0 {
		t.Fatal("no writes observed")
	}
	for _, c := range out.chunks {
		if len(c) == 0 || c[len(c)-1] != '\n' {
			t.Fatalf("chunk not line-terminated: %q", c)
		}
		if strings.Count(string(c), "\n") != 1 {
			t.Fatalf("chunk split across lines: %q", c)
		}
	}
	total := 0
	for _, c := range out.chunks {
		total += strings.Count(string(c), "\n")
	}
	if total != n {
		t.Fatalf("wrote %d lines, want %d", total, n)
	}
}

func TestAsyncStat(t *testing.T) {
	var out failWriter
	out.fail.Store(true)
	aw := newAsyncWriter(&out, AsyncConfig{FlushInterval: time.Hour})
	defer aw.Close()
	if _, err := aw.Write([]byte("x\n")); err != nil {
		t.Fatal(err)
	}
	if err := aw.Sync(); err == nil {
		t.Error("底层写入失败，Sync 应返回错误")
	}
	stats := aw.Stat()
	if stats.WriteErrors == 0 {
		t.Errorf("WriteErrors = 0, want > 0")
	}
	if stats.Dropped != 0 {
		t.Errorf("Dropped = %d, want 0", stats.Dropped)
	}
}

func TestLoggerAsyncStat(t *testing.T) {
	var out safeBuffer
	aw := newAsyncWriter(&out, AsyncConfig{MaxQueueBytes: 4096, FlushInterval: time.Hour})
	h := newJSONHandler(aw, new(slog.LevelVar), "", false, false, false)
	lg := newLogger(h, nil, []io.Closer{aw}, false, false)
	for range 100 {
		lg.Info(strings.Repeat("x", 1024))
	}
	stats, ok := lg.AsyncStat()
	if !ok {
		t.Fatal("AsyncStat not discovered on logger with async writer")
	}
	if stats.Dropped == 0 {
		t.Fatal("expected dropped > 0 via Logger.AsyncStat")
	}
	if err := aw.Close(); err != nil {
		t.Fatal(err)
	}
	// 无异步 writer 时 ok 为 false
	plain := newLogger(newJSONHandler(&out, new(slog.LevelVar), "", false, false, false), nil, nil, false, false)
	if _, ok := plain.AsyncStat(); ok {
		t.Fatal("AsyncStat should report false without async writer")
	}
}

// ---------- 滚动 ----------

func TestRotateBySize(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "app.log")
	rw, err := newRotateWriter(RotateConfig{Filename: file, MaxSize: 1, MaxBackups: 3, Compress: true, LocalTime: true})
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Repeat("a", 998) + "\n"
	total := 0
	for i := 0; i < 3000; i++ { // ~3MB > 3 次滚动
		n, err := rw.Write([]byte(line))
		if err != nil {
			t.Fatal(err)
		}
		total += n
	}
	if err := rw.Close(); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	var backups, live int
	for _, e := range entries {
		if e.Name() == "app.log" {
			live++
			continue
		}
		if strings.Contains(e.Name(), "app-") {
			backups++
			if !strings.HasSuffix(e.Name(), ".gz") {
				t.Fatalf("backup not compressed: %s", e.Name())
			}
		}
	}
	if live != 1 {
		t.Fatalf("live files = %d", live)
	}
	if backups < 1 || backups > 3 {
		t.Fatalf("backups = %d, want 1..3", backups)
	}
}

func TestRotateByPeriod(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    RotatePeriod
	}{
		{"daily", RotateDaily},
		{"hourly", RotateHourly},
		{"minutely", RotateMinutely},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			file := filepath.Join(dir, "time.log")
			rw, err := newRotateWriter(RotateConfig{Filename: file, MaxSize: 100, Period: tc.p, LocalTime: true})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := rw.Write([]byte("old\n")); err != nil {
				t.Fatal(err)
			}
			rw.mu.Lock()
			rw.periodEnd = time.Unix(0, 0) // 模拟跨周期
			rw.mu.Unlock()
			if _, err := rw.Write([]byte("new\n")); err != nil {
				t.Fatal(err)
			}
			if err := rw.Close(); err != nil {
				t.Fatal(err)
			}
			entries, _ := os.ReadDir(dir)
			var found bool
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), "time-") {
					found = true
				}
			}
			if !found {
				t.Fatalf("period backup missing: %v", entries)
			}
			bs, _ := os.ReadFile(file)
			if string(bs) != "new\n" {
				t.Fatalf("live file content = %q", bs)
			}
		})
	}
}

func TestParseBackupTime(t *testing.T) {
	if _, ok := parseBackupTime("app-2026-10-04T12-00-00.000.log", "app", ".log"); !ok {
		t.Fatal("should parse")
	}
	if _, ok := parseBackupTime("app-2026-10-04T12-00-00.000.log.gz", "app", ".log"); !ok {
		t.Fatal("should parse gz")
	}
	if _, ok := parseBackupTime("other-2026-10-04T12-00-00.000.log", "app", ".log"); ok {
		t.Fatal("foreign prefix should not parse")
	}
	if _, ok := parseBackupTime("app.log", "app", ".log"); ok {
		t.Fatal("live file should not parse")
	}
}

func TestRotatePrune(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "p.log")
	rw, err := newRotateWriter(RotateConfig{Filename: file, MaxSize: 100, MaxBackups: 2, LocalTime: true})
	if err != nil {
		t.Fatal(err)
	}
	// 手工放置 4 个备份
	for _, name := range []string{
		"p-2026-01-01T00-00-00.000.log",
		"p-2026-01-02T00-00-00.000.log",
		"p-2026-01-03T00-00-00.000.log",
		"p-2026-01-04T00-00-00.000.log",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rw.prune()
	entries, _ := os.ReadDir(dir)
	var left []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "p-") {
			left = append(left, e.Name())
		}
	}
	if len(left) != 2 {
		t.Fatalf("left backups = %v, want 2", left)
	}
	if left[0] != "p-2026-01-03T00-00-00.000.log" || left[1] != "p-2026-01-04T00-00-00.000.log" {
		t.Fatalf("kept wrong backups: %v", left)
	}
}

// ---------- trace ----------

func TestTraceExtractor(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(t, &buf, func(c *Config) { c.Trace.Enabled = true })
	defer SetTraceExtractor(nil)

	// 未注册提取器：无 trace 字段
	l.Info("no trace")
	if strings.Contains(buf.String(), "trace_id") {
		t.Fatal("trace should be absent")
	}

	SetTraceExtractor(func(ctx context.Context) (TraceInfo, bool) {
		if ctx.Value(traceKey{}) == nil {
			return TraceInfo{}, false
		}
		return TraceInfo{TraceID: "tid", SpanID: "sid"}, true
	})

	buf.Reset()
	l.Info("still no trace") // ctx 无标记
	if strings.Contains(buf.String(), "trace_id") {
		t.Fatal("trace should be absent without ctx")
	}

	buf.Reset()
	lc := l.WithContext(context.WithValue(context.Background(), traceKey{}, true))
	lc.Info("with trace")
	m := parseLines(t, buf.String())[0]
	if m["trace_id"] != "tid" || m["span_id"] != "sid" {
		t.Fatalf("trace fields: %v", m)
	}
}

type traceKey struct{}

// ---------- zap 兼容 API ----------

func TestZapStyleAPI(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(t, &buf, nil)

	l.Info("info msg", String("k", "v"))
	l.Sugar().Infow("sugar w", "uid", 42, "src", "app")
	l.Sugar().Infof("sugar f %d", 7)
	l.Named("sub").With(Int("x", 1)).Error("named error", Err(errors.New("bad")))

	lines := parseLines(t, buf.String())
	if len(lines) != 4 {
		t.Fatalf("want 4 lines: %s", buf.String())
	}
	if lines[0]["k"] != "v" {
		t.Fatalf("line0: %v", lines[0])
	}
	if lines[1]["uid"] != float64(42) || lines[1]["src"] != "app" {
		t.Fatalf("line1: %v", lines[1])
	}
	if lines[2]["msg"] != "sugar f 7" {
		t.Fatalf("line2: %v", lines[2])
	}
	if lines[3]["logger"] != "sub" || lines[3]["error"] != "bad" || lines[3]["x"] != float64(1) {
		t.Fatalf("line3: %v", lines[3])
	}
}

func TestFatalAndPanic(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(t, &buf, nil)

	code := 0
	l.SetExitFunc(func(c int) { code = c })
	l.Fatal("die", String("r", "test"))
	if code != 1 {
		t.Fatalf("exit code = %d", code)
	}
	m := parseLines(t, buf.String())[0]
	if m["level"] != "FATAL" || m["stack"] == nil {
		t.Fatalf("fatal record: %v", m)
	}

	buf.Reset()
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("Panic should panic")
			}
		}()
		l.Panic("boom")
	}()
	m = parseLines(t, buf.String())[0]
	if m["level"] != "PANIC" || m["stack"] == nil {
		t.Fatalf("panic record: %v", m)
	}
}

func TestDPanicDevelopment(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(t, &buf, func(c *Config) { c.Development = true })
	defer func() {
		if recover() == nil {
			t.Fatal("DPanic should panic in development")
		}
		m := parseLines(t, buf.String())[0]
		if m["stack"] == nil {
			t.Fatalf("DPanic in development should carry stack: %v", m)
		}
	}()
	l.DPanic("dev only")
}

func TestSlogInterop(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(t, &buf, nil)
	std := l.StdLogger()
	std.Info("via slog", "k", "v", slog.String("k2", "v2"))
	m := parseLines(t, buf.String())[0]
	if m["msg"] != "via slog" || m["k"] != "v" || m["k2"] != "v2" {
		t.Fatalf("slog interop: %v", m)
	}
}

// ---------- 全局 API ----------

func TestGlobalAPI(t *testing.T) {
	var buf bytes.Buffer
	defer SetDefault(nil)
	if err := Init(Config{Writer: &buf, Level: "warn"}); err != nil {
		t.Fatal(err)
	}
	Info("hidden", String("k", "v"))
	Warn("shown", "uid", 42)
	Errorw("shown too", "code", 7)
	Errorf("shown %d", 3)
	lines := parseLines(t, buf.String())
	if len(lines) != 3 {
		t.Fatalf("want 3 lines:\n%s", buf.String())
	}
	if lines[0]["uid"] != float64(42) {
		t.Fatalf("argsToAttrs kv: %v", lines[0])
	}
	if lines[2]["msg"] != "shown 3" {
		t.Fatalf("Errorf: %v", lines[2])
	}

	// 惰性默认
	SetDefault(nil)
	if Default() == nil {
		t.Fatal("Default should lazily init")
	}
}

func TestArgsToAttrs(t *testing.T) {
	attrs := argsToAttrs([]any{"a", 1, String("b", "2"), "odd"})
	if len(attrs) != 3 {
		t.Fatalf("attrs = %v", attrs)
	}
	if attrs[0].Key != "a" || attrs[1].Key != "b" || attrs[2].Key != "!BADKEY" {
		t.Fatalf("attrs = %v", attrs)
	}
}

// ---------- buffer 池 ----------

func TestBufferPool(t *testing.T) {
	b := getBuffer()
	b.AppendString("hello")
	putBuffer(b)
	b2 := getBuffer()
	if b2.Len() != 0 {
		t.Fatalf("reused buffer not reset: %d", b2.Len())
	}
	// 超大 buffer 不回池
	big := &buffer{bs: make([]byte, 0, maxPooledBufSize+1)}
	putBuffer(big)
	big2 := getBuffer()
	if cap(big2.bs) == maxPooledBufSize+1 {
		t.Fatal("oversized buffer should not be pooled")
	}
}

// ---------- 并发冒烟 ----------

func TestConcurrent(t *testing.T) {
	var out safeBuffer
	l, err := New(Config{Writer: &out, Level: "info", Format: "json"})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lg := l.With(Int("goroutine", g)) // Go 1.22+ 语义：循环变量每次迭代独立，可直接捕获
			for i := range 200 {
				lg.Info("concurrent", Int("i", i))
			}
		}()
	}
	wg.Wait()
	lines := parseLines(t, out.String())
	if len(lines) != 1600 {
		t.Fatalf("got %d lines, want 1600", len(lines))
	}
}

// ---------- 补充与回归 ----------

func TestDisableTime(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(t, &buf, func(c *Config) { c.DisableTime = true })
	l.Info("no time", String("k", "v"))
	m := parseLines(t, buf.String())[0]
	if _, ok := m["time"]; ok {
		t.Fatalf("time should be omitted: %v", m)
	}
	if m["msg"] != "no time" {
		t.Fatalf("record broken: %v", m)
	}

	buf.Reset()
	lc := newTestLogger(t, &buf, func(c *Config) { c.Format = "console"; c.DisableTime = true })
	lc.Info("no time")
	if strings.HasPrefix(buf.String(), "20") {
		t.Fatalf("console line should not start with time: %q", buf.String())
	}
	if !strings.HasPrefix(buf.String(), "INFO no time") {
		t.Fatalf("console line should start with level: %q", buf.String())
	}
}

func TestAttrTimeFollowsTimeFormat(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(t, &buf, func(c *Config) { c.TimeFormat = "15:04:05" })
	l.Info("t", Time("when", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)))
	m := parseLines(t, buf.String())[0]
	if m["when"] != "03:04:05" {
		t.Fatalf("attr time = %v, want 03:04:05", m["when"])
	}
}

func TestErrNilOmitted(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(t, &buf, nil)
	l.Info("e", Err(nil), NamedError("detail", nil), String("k", "v"))
	m := parseLines(t, buf.String())[0]
	if _, ok := m["error"]; ok {
		t.Fatalf("nil error should be omitted: %v", m)
	}
	if _, ok := m["detail"]; ok {
		t.Fatalf("nil named error should be omitted: %v", m)
	}
	if m["k"] != "v" {
		t.Fatalf("record broken: %v", m)
	}
}

func TestInfowTrailingKey(t *testing.T) {
	// 落单参数与 slog/argsToAttrs 对齐：记为 !BADKEY，而不是静默配 nil
	var buf bytes.Buffer
	l := newTestLogger(t, &buf, nil)
	l.Sugar().Infow("odd", "lonely")
	m := parseLines(t, buf.String())[0]
	if m["!BADKEY"] != "lonely" {
		t.Fatalf("trailing key: %v", m)
	}
}

func TestRotateWriteAfterClose(t *testing.T) {
	dir := t.TempDir()
	rw, err := newRotateWriter(RotateConfig{Filename: filepath.Join(dir, "a.log")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rw.Write([]byte("x\n")); err != nil {
		t.Fatal(err)
	}
	if err := rw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := rw.Write([]byte("y\n")); err == nil {
		t.Fatal("write after close should fail")
	}
}

func TestRotateStaleFileRotates(t *testing.T) {
	for _, tc := range []struct {
		name   string
		p      RotatePeriod
		offset time.Duration // 磁盘文件 mtime 相对现在的偏移，保证落在此前周期
	}{
		{"daily", RotateDaily, -24 * time.Hour},
		{"hourly", RotateHourly, -2 * time.Hour},
		{"minutely", RotateMinutely, -5 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			file := filepath.Join(dir, "stale.log")
			if err := os.WriteFile(file, []byte("old period\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			stale := time.Now().Add(tc.offset)
			if err := os.Chtimes(file, stale, stale); err != nil {
				t.Fatal(err)
			}
			rw, err := newRotateWriter(RotateConfig{Filename: file, MaxSize: 100, Period: tc.p, LocalTime: true})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := rw.Write([]byte("new period\n")); err != nil {
				t.Fatal(err)
			}
			if err := rw.Close(); err != nil {
				t.Fatal(err)
			}
			entries, _ := os.ReadDir(dir)
			found := false
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), "stale-") {
					found = true
				}
			}
			if !found {
				t.Fatalf("stale backup missing: %v", entries)
			}
			bs, _ := os.ReadFile(file)
			if string(bs) != "new period\n" {
				t.Fatalf("live content = %q", bs)
			}
		})
	}
}

func TestRotateInvalidPeriod(t *testing.T) {
	file := filepath.Join(t.TempDir(), "a.log")
	if _, err := newRotateWriter(RotateConfig{Filename: file, Period: RotatePeriod(9)}); err == nil {
		t.Fatal("expected error for invalid period")
	}
}

// TestLoggerCloseLIFO 验证资源按 LIFO 关闭：asyncWriter 先于 rotateWriter，
// 排空内容落入仍打开的文件；且关闭后写入报错、文件不被重开。
func TestLoggerCloseLIFO(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "lifo.log")
	rw, err := newRotateWriter(RotateConfig{Filename: f})
	if err != nil {
		t.Fatal(err)
	}
	aw := newAsyncWriter(rw, AsyncConfig{MaxQueueBytes: 1 << 20, FlushInterval: time.Hour})
	h := newJSONHandler(aw, new(slog.LevelVar), "", false, false, false)
	// buildWriter 的注册顺序：文件在前，异步在后
	lg := newLogger(h, nil, []io.Closer{rw, aw}, false, false)
	for i := range 10 {
		lg.Info("lifo", Int("i", i))
	}
	if err := lg.Close(); err != nil {
		t.Fatal(err)
	}
	bs, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(bs), `"msg":"lifo"`); got != 10 {
		t.Fatalf("flushed %d lines, want 10: %q", got, bs)
	}
	if _, err := rw.Write([]byte("x")); err == nil {
		t.Fatal("write after close should fail")
	}
}

func TestAsyncDrainStragglers(t *testing.T) {
	var out safeBuffer
	w := &asyncWriter{
		next:       &out,
		interval:   time.Hour,
		forceFlush: make(chan struct{}),
	}
	w.cond = sync.NewCond(&w.mu)
	w.acceptedCond = sync.NewCond(&w.stateMu)
	w.q.enqueue(&buffer{bs: []byte(`{"msg":"s1"}` + "\n")})
	w.q.enqueue(&buffer{bs: []byte(`{"msg":"s2"}` + "\n")})
	w.drainStragglers()
	if s := out.String(); !strings.Contains(s, "s1") || !strings.Contains(s, "s2") {
		t.Fatalf("stragglers not drained: %q", s)
	}
}

func TestInitClosesPreviousLogger(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "one.log")
	SetDefault(nil)
	if err := Init(Config{
		Outputs: []string{f1},
		Level:   "debug",
		Async:   &AsyncConfig{Enabled: true, FlushInterval: time.Hour},
	}); err != nil {
		t.Fatal(err)
	}
	Info("reinit-flush")
	// 重新 Init 应冲刷并关闭旧 Logger（FlushInterval 为 1h，唯一落盘机会来自 Close）
	if err := Init(Config{Outputs: []string{"stdout"}, Level: "info"}); err != nil {
		t.Fatal(err)
	}
	defer SetDefault(nil)
	bs, err := os.ReadFile(f1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(bs), "reinit-flush") {
		t.Fatalf("previous logger not flushed on re-Init: %q", bs)
	}
}

// 旧实例排空失败时应返回原始错误，新默认实例仍应可用。
func TestInitReturnsPreviousCloseError(t *testing.T) {
	defer SetDefault(nil)
	var failed failWriter
	failed.fail.Store(true)
	aw := newAsyncWriter(&failed, AsyncConfig{FlushInterval: time.Hour})
	old := newLogger(newJSONHandler(aw, new(slog.LevelVar), "", false, false, false),
		nil, []io.Closer{aw}, false, false)
	defer old.Close()
	SetDefault(old)
	old.Info("pending")

	var replacement bytes.Buffer
	err := Init(Config{Writer: &replacement})
	if !errors.Is(err, errInjected) {
		t.Errorf("Init 未返回旧实例关闭错误：%v", err)
	}
	if _, err := aw.Write([]byte("closed\n")); !errors.Is(err, errClosed) {
		t.Errorf("旧实例没有关闭：%v", err)
	}
	if Default() == old {
		t.Fatal("新默认实例没有安装")
	}
	Info("replacement")
	lines := parseLines(t, replacement.String())
	if len(lines) != 1 || lines[0]["msg"] != "replacement" {
		t.Fatalf("新默认实例不可用：%v", lines)
	}
}

func TestInitBuildFailureKeepsPreviousLogger(t *testing.T) {
	defer SetDefault(nil)
	var out bytes.Buffer
	old := newTestLogger(t, &out, nil)
	SetDefault(old)
	if err := Init(Config{Format: "invalid"}); err == nil {
		t.Fatal("非法配置没有返回错误")
	}
	if Default() != old {
		t.Fatal("构建失败替换了默认实例")
	}
	Info("previous")
	lines := parseLines(t, out.String())
	if len(lines) != 1 || lines[0]["msg"] != "previous" {
		t.Fatalf("旧实例不可用：%v", lines)
	}
}

func TestConsoleGroup(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(t, &buf, func(c *Config) { c.Format = "console" })
	l.WithGroup("req").With(String("id", "7")).Info("g", Int("n", 1))
	s := buf.String()
	if !strings.Contains(s, "req.id=7") || !strings.Contains(s, "req.n=1") {
		t.Fatalf("console group: %q", s)
	}
}

func TestConfigValidation(t *testing.T) {
	if _, err := New(Config{Format: "xml"}); err == nil {
		t.Fatal("bad format should error")
	}
	if _, err := New(Config{Level: "bogus"}); err == nil {
		t.Fatal("bad level should error")
	}
}

func TestPackagePanicVariants(t *testing.T) {
	var buf bytes.Buffer
	defer SetDefault(nil)
	if err := Init(Config{Writer: &buf, Level: "debug"}); err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() { recover() }()
		Panicf("boom %d", 7)
	}()
	m := parseLines(t, buf.String())[0]
	if m["msg"] != "boom 7" || m["stack"] == nil {
		t.Fatalf("Panicf record: %v", m)
	}
	buf.Reset()
	func() {
		defer func() { recover() }()
		Panicw("boom w", "k", 1)
	}()
	m = parseLines(t, buf.String())[0]
	if m["k"] != float64(1) || m["stack"] == nil {
		t.Fatalf("Panicw record: %v", m)
	}
	// 生产默认（Development=false）：DPanicf/DPanicw 只记录不 panic
	buf.Reset()
	DPanicf("dp %s", "x")
	DPanicw("dp w", "k2", 2)
	lines := parseLines(t, buf.String())
	if len(lines) != 2 {
		t.Fatalf("DPanic variants should log without panic: %s", buf.String())
	}
	if lines[0]["msg"] != "dp x" || lines[1]["k2"] != float64(2) {
		t.Fatalf("DPanic variants: %v %v", lines[0], lines[1])
	}
}

// 所有 panic 入口均须在 panic 发生前排空；Sync 失败不能覆盖原始 panic。
func TestPanicVariantsFlushBeforePanic(t *testing.T) {
	calls := []struct {
		name string
		call func(*Logger)
	}{
		{"logger", func(l *Logger) { l.Panic("panic marker") }},
		{"package", func(l *Logger) { Panic("panic marker") }},
		{"package-f", func(l *Logger) { Panicf("panic %s", "marker") }},
		{"package-w", func(l *Logger) { Panicw("panic marker", "key", "value") }},
		{"sugar-f", func(l *Logger) { l.Sugar().Panicf("panic %s", "marker") }},
		{"sugar-w", func(l *Logger) { l.Sugar().Panicw("panic marker", "key", "value") }},
		{"logger-dpanic", func(l *Logger) { l.DPanic("panic marker") }},
		{"package-dpanic", func(l *Logger) { DPanic("panic marker") }},
		{"package-dpanic-f", func(l *Logger) { DPanicf("panic %s", "marker") }},
		{"package-dpanic-w", func(l *Logger) { DPanicw("panic marker", "key", "value") }},
		{"sugar-dpanic-f", func(l *Logger) { l.Sugar().DPanicf("panic %s", "marker") }},
		{"sugar-dpanic-w", func(l *Logger) { l.Sugar().DPanicw("panic marker", "key", "value") }},
	}
	for _, call := range calls {
		for _, mode := range []string{"enabled", "filtered", "sync-error"} {
			t.Run(call.name+"/"+mode, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					defer SetDefault(nil)
					var healthy safeBuffer
					aw := newAsyncWriter(&healthy, AsyncConfig{FlushInterval: time.Hour})
					lv := new(slog.LevelVar)
					var h slog.Handler = newJSONHandler(aw, lv, "", false, false, false)
					closers := []io.Closer{aw}
					if mode == "sync-error" {
						var failed failWriter
						failed.fail.Store(true)
						other := newAsyncWriter(&failed, AsyncConfig{FlushInterval: time.Hour})
						h = Tee(newJSONHandler(other, lv, "", false, false, false), h)
						closers = []io.Closer{other, aw}
					}
					lg := newLogger(h, lv, closers, true, false)
					defer lg.Close()
					SetDefault(lg)
					// 首轮消费结束后再入队，避免后台首轮写出掩盖缺少 Sync 的问题。
					synctest.Wait()
					lg.Info("pending")
					if mode == "filtered" {
						lg.SetLevel(LevelFatal)
					}
					var recovered any
					func() {
						defer func() { recovered = recover() }()
						call.call(lg)
					}()
					if recovered != "panic marker" {
						t.Errorf("panic 值被改变：%v", recovered)
					}
					// 断言放在显式 Sync/Close 之前，检查 panic 时的落盘状态。
					lines := parseLines(t, healthy.String())
					want := 2
					if mode == "filtered" {
						want = 1
					}
					if len(lines) != want {
						t.Fatalf("panic 前写出 %d 条，want %d：%v", len(lines), want, lines)
					}
					if lines[0]["msg"] != "pending" || (want == 2 && (lines[1]["msg"] != "panic marker" || lines[1]["stack"] == nil)) {
						t.Errorf("panic 前日志内容错误：%v", lines)
					}
				})
			})
		}
	}
}

// ---------- 扩展特性（Tee/Hooks/ObjectMarshaler 等） ----------

type testUser struct {
	ID   int
	Name string
}

func (u testUser) MarshalLogObject() []Field {
	return []Field{Int("id", u.ID), String("name", u.Name)}
}

func TestGlobalSugarCache(t *testing.T) {
	var a, b safeBuffer
	defer SetDefault(nil)
	if err := Init(Config{Writer: &a, Level: "debug"}); err != nil {
		t.Fatal(err)
	}
	s1 := S()
	s1.Errorw("via sugar")
	if !strings.Contains(a.String(), "via sugar") {
		t.Fatalf("sugar write missing: %q", a.String())
	}
	if S() != s1 {
		t.Fatal("S() should return the cached sugar")
	}
	if err := Init(Config{Writer: &b, Level: "debug"}); err != nil {
		t.Fatal(err)
	}
	if S() == s1 {
		t.Fatal("S() should rebuild after Init replaces the default")
	}
	S().Errorw("new sugar")
	if strings.Contains(a.String(), "new sugar") || !strings.Contains(b.String(), "new sugar") {
		t.Fatalf("sugar not rebound to new default: a=%q b=%q", a.String(), b.String())
	}
}

func TestTimeEpochMillis(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	var buf bytes.Buffer
	l := newTestLogger(t, &buf, func(c *Config) {
		c.TimeEpochMillis = true
		c.Now = func() time.Time { return now }
	})
	l.Info("epoch")
	m := parseLines(t, buf.String())[0]
	if m["time"] != float64(now.UnixMilli()) {
		t.Fatalf("epoch time = %v, want %d", m["time"], now.UnixMilli())
	}
	// DisableTime 优先于 epoch
	buf.Reset()
	l2 := newTestLogger(t, &buf, func(c *Config) { c.TimeEpochMillis = true; c.DisableTime = true })
	l2.Info("no time")
	if _, ok := parseLines(t, buf.String())[0]["time"]; ok {
		t.Fatal("DisableTime should suppress epoch time too")
	}
}

func TestClockInjection(t *testing.T) {
	now := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	var buf bytes.Buffer
	l := newTestLogger(t, &buf, func(c *Config) { c.Now = func() time.Time { return now } })
	l.Info("clocked")
	m := parseLines(t, buf.String())[0]
	if m["time"] != "2026-03-04T05:06:07.000Z" {
		t.Fatalf("injected clock time = %v", m["time"])
	}
}

func TestTeeConfigFormats(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "console.log")
	f2 := filepath.Join(dir, "json.log")
	l, err := New(Config{Outputs: []string{"console:" + f1, "json:" + f2}, Level: "debug"})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	l.Info("tee", String("k", "v"))
	cb, err := os.ReadFile(f1)
	if err != nil {
		t.Fatal(err)
	}
	jb, err := os.ReadFile(f2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cb), "INFO tee k=v") {
		t.Fatalf("console branch: %q", cb)
	}
	var m map[string]any
	if err := json.Unmarshal(jb, &m); err != nil {
		t.Fatalf("json branch: %q", jb)
	}
	if m["msg"] != "tee" || m["k"] != "v" {
		t.Fatalf("json branch: %v", m)
	}
}

func TestTeeFunc(t *testing.T) {
	var b1, b2 bytes.Buffer
	h1 := newJSONHandler(&b1, new(slog.LevelVar), "", false, false, false)
	h2 := newJSONHandler(&b2, new(slog.LevelVar), "", false, false, false)
	th := Tee(h1, h2)
	r := slog.NewRecord(time.Now(), LevelInfo, "tee func", 0)
	if err := th.Handle(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b1.String(), "tee func") || !strings.Contains(b2.String(), "tee func") {
		t.Fatalf("tee dispatch: %q %q", b1.String(), b2.String())
	}
	if Tee() == nil {
		t.Fatal("Tee() with no args should return a discard handler")
	}
	if Tee(h1) != slog.Handler(h1) {
		t.Fatal("Tee with a single handler should return it as-is")
	}
}

func TestObjectMarshaler(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(t, &buf, nil)
	l.Info("obj", Any("user", testUser{42, "tom"}), Any("empty", objectMarshalerEmpty{}))
	m := parseLines(t, buf.String())[0]
	usr, ok := m["user"].(map[string]any)
	if !ok {
		t.Fatalf("object not encoded: %v", m)
	}
	if usr["id"] != float64(42) || usr["name"] != "tom" {
		t.Fatalf("object fields: %v", usr)
	}
	if empty, has := m["empty"].(map[string]any); !has || len(empty) != 0 {
		t.Fatalf("empty object: %v", m["empty"])
	}

	buf.Reset()
	lc := newTestLogger(t, &buf, func(c *Config) { c.Format = "console" })
	lc.Info("obj", Any("user", testUser{42, "tom"}))
	s := buf.String()
	if !strings.Contains(s, "user.id=42") || !strings.Contains(s, "user.name=tom") {
		t.Fatalf("console object: %q", s)
	}
}

type objectMarshalerEmpty struct{}

func (objectMarshalerEmpty) MarshalLogObject() []Field { return nil }

func TestHooks(t *testing.T) {
	var buf bytes.Buffer
	var msgs []string
	l := newTestLogger(t, &buf, func(c *Config) {
		c.Hooks = []Hook{func(ctx context.Context, r slog.Record) { msgs = append(msgs, r.Message) }}
	})
	l.Info("h1")
	l.Warn("h2")
	if len(msgs) != 2 || msgs[0] != "h1" || msgs[1] != "h2" {
		t.Fatalf("hooks: %v", msgs)
	}
	l.SetLevel(LevelError)
	l.Info("h3")
	if len(msgs) != 2 {
		t.Fatalf("filtered entries must not trigger hooks: %v", msgs)
	}
}

func TestFatalHook(t *testing.T) {
	var buf bytes.Buffer
	var got slog.Record
	l := newTestLogger(t, &buf, func(c *Config) {
		c.FatalHook = func(ctx context.Context, r slog.Record) { got = r }
	})
	code := 0
	l.SetExitFunc(func(c int) { code = c })
	l.Fatal("die", String("r", "x"))
	if code != 1 {
		t.Fatalf("exit code = %d", code)
	}
	if got.Message != "die" || got.Level != LevelFatal {
		t.Fatalf("fatal hook record: %+v", got)
	}
	// DPanic 开发模式同样触发
	buf.Reset()
	got = slog.Record{}
	ld := newTestLogger(t, &buf, func(c *Config) {
		c.Development = true
		c.FatalHook = func(ctx context.Context, r slog.Record) { got = r }
	})
	func() {
		defer func() { recover() }()
		ld.DPanic("dev fatal")
	}()
	if got.Message != "dev fatal" {
		t.Fatalf("DPanic fatal hook: %+v", got)
	}
}

func TestStacktraceLevel(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(t, &buf, func(c *Config) { c.StacktraceLevel = "error" })
	l.Warn("w")
	l.Error("e")
	lines := parseLines(t, buf.String())
	if lines[0]["stack"] != nil {
		t.Fatalf("warn should not carry stack: %v", lines[0])
	}
	if lines[1]["stack"] == nil {
		t.Fatalf("error should carry stack: %v", lines[1])
	}
}

func TestIncreaseLevel(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(t, &buf, nil)
	child := l.IncreaseLevel(LevelError)
	child.Info("hidden")
	child.Error("shown")
	l.Info("parent still info")
	lines := parseLines(t, buf.String())
	if len(lines) != 2 || lines[0]["msg"] != "shown" || lines[1]["msg"] != "parent still info" {
		t.Fatalf("increase level: %s", buf.String())
	}
}

func TestOutputPrefixValidation(t *testing.T) {
	if _, err := New(Config{Outputs: []string{"json:"}}); err == nil {
		t.Fatal("empty target after json prefix should error")
	}
	if _, err := New(Config{Outputs: []string{"console:"}}); err == nil {
		t.Fatal("empty target after console prefix should error")
	}
}

func TestNamedLazy(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(t, &buf, nil)
	base := l.Named("a")

	// 派生只挂名字不做字段编码：分配上限作为回归护栏（join + clone + Logger 结构）
	n := testing.AllocsPerRun(100, func() { sinkLogger = base.Named("b.c") })
	if n > 4 {
		t.Fatalf("Named allocs = %v, want <= 4", n)
	}

	base.Named("b.c").Info("named")
	if !strings.Contains(buf.String(), `"level":"INFO","logger":"a.b.c","msg":"named"`) {
		t.Fatalf("named output: %s", buf.String())
	}
	if l.Named("") != l {
		t.Fatal("empty name should return the same logger")
	}

	// console：名字位于级别之后
	buf.Reset()
	lc := newTestLogger(t, &buf, func(c *Config) { c.Format = "console" }).Named("svc.api")
	lc.Info("named")
	if !strings.Contains(buf.String(), "INFO svc.api named") {
		t.Fatalf("console named: %q", buf.String())
	}

	// 外部 handler（标准库）：回退为 WithAttrs 公共字段
	buf.Reset()
	foreign := slog.NewJSONHandler(&buf, nil)
	WrapHandler(foreign).Named("sub").Info("fallback")
	m := parseLines(t, buf.String())[0]
	if m["logger"] != "sub" {
		t.Fatalf("fallback named: %v", m)
	}
}

var sinkLogger *Logger

// ---------- P1 修复回归 ----------

// TestAsyncConcurrentSyncWakeup 回归：长轮询周期下并发 Sync 必须靠唤醒推进，
// 不能等到下一次定时消费；虚拟时间会暴露丢失唤醒导致的周期等待。
func TestAsyncConcurrentSyncWakeup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var out safeBuffer
		aw := newAsyncWriter(&out, AsyncConfig{MaxQueueBytes: 1 << 20, FlushInterval: time.Hour})
		h := newJSONHandler(aw, new(slog.LevelVar), "", false, false, false)
		lg := newLogger(h, nil, []io.Closer{aw}, false, false)
		start := time.Now()
		var wg sync.WaitGroup
		for g := range 8 {
			wg.Go(func() {
				for i := range 16 {
					lg.Info("concurrent-sync", Int("g", g), Int("i", i))
					if err := lg.Sync(); err != nil {
						t.Errorf("Sync: %v", err)
						return
					}
				}
			})
		}
		wg.Wait()
		if err := aw.Close(); err != nil {
			t.Fatal(err)
		}
		if elapsed := time.Since(start); elapsed != 0 {
			t.Errorf("Sync/Close 等待了定时消费：%v", elapsed)
		}
		lines := parseLines(t, out.String())
		seen := make(map[[2]int]bool)
		for _, line := range lines {
			key := [2]int{int(line["g"].(float64)), int(line["i"].(float64))}
			if seen[key] {
				t.Errorf("重复日志：%v", key)
			}
			seen[key] = true
		}
		if len(seen) != 8*16 {
			t.Errorf("写出 %d 条不同日志，want %d", len(seen), 8*16)
		}
	})
}

// TestAsyncSyncDuringClose 回归：拒绝新写入后，Sync 仍需等待在途写入和
// 最后一轮排空；普通定时迭代的完成不能替代关闭完成。
func TestAsyncSyncDuringClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var out safeBuffer
		aw := newAsyncWriter(&out, AsyncConfig{FlushInterval: time.Hour})
		synctest.Wait() // 消费者首轮完成并休眠
		if _, err := aw.Write([]byte("{\"msg\":\"queued\"}\n")); err != nil {
			t.Fatal(err)
		}

		arrived := make(chan struct{})
		release := make(chan struct{})
		asyncSmallBufPool = sync.Pool{New: func() any {
			close(arrived)
			<-release
			return &buffer{bs: make([]byte, 0, smallAsyncBufSize)}
		}}
		defer func() {
			asyncSmallBufPool = sync.Pool{New: func() any { return &buffer{bs: make([]byte, 0, smallAsyncBufSize)} }}
		}()
		written := make(chan error, 1)
		go func() {
			_, err := aw.Write([]byte("{\"msg\":\"in-flight\"}\n"))
			written <- err
		}()
		<-arrived
		closed := make(chan error, 1)
		go func() { closed <- aw.Close() }()
		synctest.Wait()
		if !aw.isClosed.Load() {
			t.Fatal("Close 尚未开始拒绝新写入")
		}
		if _, err := aw.Write([]byte("rejected")); !errors.Is(err, errClosed) {
			t.Errorf("关闭期间 Write: %v，want errClosed", err)
		}
		synced := make(chan error, 1)
		go func() { synced <- aw.Sync() }()
		synctest.Wait()
		select {
		case err := <-synced:
			t.Errorf("在途写入未完成，Sync 提前返回：%v", err)
		default:
		}
		// 推进一个周期，强制消费者在 accepted 尚未归零时再完成一轮。
		time.Sleep(time.Hour)
		synctest.Wait()
		select {
		case err := <-synced:
			t.Errorf("普通迭代完成后 Sync 提前返回：%v", err)
		default:
		}
		close(release)
		synctest.Wait()
		if err := <-written; err != nil {
			t.Errorf("在途 Write: %v", err)
		}
		if err := <-closed; err != nil {
			t.Errorf("Close: %v", err)
		}
		select {
		case err := <-synced:
			if err != nil {
				t.Errorf("Sync: %v", err)
			}
		default:
			if !t.Failed() {
				t.Error("关闭完成后 Sync 未返回")
			}
		}
		if got := len(parseLines(t, out.String())); got != 2 {
			t.Errorf("排空 %d 条日志，want 2", got)
		}
		if err := aw.Sync(); err != nil {
			t.Errorf("关闭完成后的 Sync: %v", err)
		}
	})
}

// syncErrorCloser 验证每个分支都被冲刷，且错误不会只保留第一个。
type syncErrorCloser struct {
	err    error
	called bool
}

func (c *syncErrorCloser) Close() error { return nil }
func (c *syncErrorCloser) Sync() error {
	c.called = true
	return c.err
}

func TestLoggerSyncAllBranches(t *testing.T) {
	firstErr := errors.New("log: first sync failure")
	lastErr := errors.New("log: last sync failure")
	first := &syncErrorCloser{err: firstErr}
	middle := &syncErrorCloser{}
	last := &syncErrorCloser{err: lastErr}
	l := newLogger(slog.DiscardHandler, nil, []io.Closer{first, middle, last}, false, false)
	err := l.Sync()
	if !first.called || !middle.called || !last.called {
		t.Error("部分输出分支未被 Sync")
	}
	if !errors.Is(err, firstErr) || !errors.Is(err, lastErr) {
		t.Errorf("Sync 未汇总全部错误：%v", err)
	}
	first.err, last.err = nil, nil
	if err := l.Sync(); err != nil {
		t.Errorf("所有分支成功时 Sync 应返回 nil：%v", err)
	}
}

// TestFatalSyncAllBranches 回归：首分支历史失败不应阻止后续分支在退出前落盘。
func TestFatalSyncAllBranches(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var failed failWriter
		failed.fail.Store(true)
		var healthy safeBuffer
		first := newAsyncWriter(&failed, AsyncConfig{FlushInterval: time.Hour})
		second := newAsyncWriter(&healthy, AsyncConfig{FlushInterval: time.Hour})
		lg := newLogger(Tee(
			newJSONHandler(first, new(slog.LevelVar), "", false, false, false),
			newJSONHandler(second, new(slog.LevelVar), "", false, false, false),
		), nil, []io.Closer{first, second}, false, false)
		defer lg.Close()
		synctest.Wait()
		lg.Info("history")
		if err := lg.Sync(); !errors.Is(err, errInjected) {
			t.Errorf("Sync 未返回首分支失败：%v", err)
		}
		exited := false
		lg.SetExitFunc(func(code int) {
			exited = true
			lines := parseLines(t, healthy.String())
			if code != 1 || len(lines) != 2 || lines[1]["msg"] != "fatal" {
				t.Errorf("退出前健康分支未完整落盘：code=%d, lines=%v", code, lines)
			}
		})
		lg.Fatal("fatal")
		if !exited {
			t.Error("Fatal 未调用退出函数")
		}
	})
}

// failWriter 注入底层写失败：fail 置位期间所有写入报错，复位后恢复。
type failWriter struct {
	safeBuffer
	fail atomic.Bool
}

var errInjected = errors.New("injected write failure")

func (w *failWriter) Write(p []byte) (int, error) {
	if w.fail.Load() {
		return 0, errInjected
	}
	return w.safeBuffer.Write(p)
}

// TestAsyncCloseWaitsInFlight 回归：通过关闭检查的在途写入必须被 Close
// 等待，而不是丢日志（成功返回却没写出）。在缓冲区申请处暂停两个已通过
// 检查的写入，待 Close 进入等待后放行。
func TestAsyncCloseWaitsInFlight(t *testing.T) {
	arrived := make(chan struct{}, 2)
	release := make(chan struct{})
	asyncSmallBufPool = sync.Pool{New: func() any {
		arrived <- struct{}{}
		<-release
		return &buffer{bs: make([]byte, 0, smallAsyncBufSize)}
	}}
	defer func() {
		asyncSmallBufPool = sync.Pool{New: func() any { return &buffer{bs: make([]byte, 0, smallAsyncBufSize)} }}
	}()

	var out safeBuffer
	aw := newAsyncWriter(&out, AsyncConfig{MaxQueueBytes: 1 << 20, FlushInterval: time.Hour})
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := aw.Write([]byte("message\n"))
			errs <- err
		}()
	}
	<-arrived
	<-arrived
	closed := make(chan struct{})
	go func() {
		_ = aw.Close()
		close(closed)
	}()
	// 握手：确认 Close 已置位关闭标记并进入等待（两个写入仍在池申请处
	// 暂停，accepted 必然 > 0），保证触发的是"等待在途写入"时序
	for !aw.isClosed.Load() {
		time.Sleep(time.Millisecond)
	}
	close(release)
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close 未能在在途写入完成后返回（可能提前放行或死锁）")
	}

	for range 2 {
		select {
		case err := <-errs:
			if err != nil {
				t.Errorf("在途写入应成功返回: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("在途写入未在 Close 完成后返回")
		}
	}
	if got := strings.Count(out.String(), "message"); got != 2 {
		t.Errorf("写出 %d 条, want 2: %q", got, out.String())
	}
}

// TestAsyncSyncReturnsFlushError 回归：底层写失败必须经 Sync 回传。
// lastErr 为粘性语义（同 asynczap）：失败不被后续成功落盘"洗白"，
// 但输出在底层恢复后继续（不得让输出永久停止）。
func TestAsyncSyncReturnsFlushError(t *testing.T) {
	var out failWriter
	out.fail.Store(true)
	aw := newAsyncWriter(&out, AsyncConfig{FlushInterval: time.Hour})
	defer aw.Close()
	if _, err := aw.Write([]byte("first\n")); err != nil {
		t.Fatal(err)
	}
	if err := aw.Sync(); err == nil {
		t.Error("底层写入失败，Sync 应返回底层错误")
	}
	out.fail.Store(false)
	if _, err := aw.Write([]byte("second\n")); err != nil {
		t.Fatal(err)
	}
	if err := aw.Sync(); err == nil {
		t.Error("lastErr 粘性：历史失败不得被成功落盘清除")
	}
	if !strings.Contains(out.String(), "second") {
		t.Errorf("底层恢复后日志未恢复输出: %q", out.String())
	}
}

// TestAsyncCloseReturnsFlushError 回归：Close 应返回最终 flush 的底层错误。
func TestAsyncCloseReturnsFlushError(t *testing.T) {
	var out failWriter
	out.fail.Store(true)
	aw := newAsyncWriter(&out, AsyncConfig{FlushInterval: time.Hour})
	if _, err := aw.Write([]byte("x\n")); err != nil {
		t.Fatal(err)
	}
	if err := aw.Close(); err == nil {
		t.Error("底层写入失败，Close 应返回最终 flush 错误")
	}
}

// TestEmitKeepsCallerFieldsReadOnly 回归：emit 不得用 append 复用调用方
// 字段切片的剩余容量——复用切片并发记录堆栈级别日志时会在同一底层数组
// 上产生 write/write 数据竞争。需配合 -race 检测。
func TestEmitKeepsCallerFieldsReadOnly(t *testing.T) {
	var out safeBuffer
	l, err := New(Config{Writer: &out, StacktraceLevel: "info"})
	if err != nil {
		t.Fatal(err)
	}
	fields := make([]Field, 1, 2)
	fields[0] = String("shared", "value")
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			for range 20 {
				l.Info("concurrent", fields...)
			}
		})
	}
	wg.Wait()
	if !fields[:cap(fields)][1].Equal(Skip()) {
		t.Error("emit 写入了调用方切片的剩余容量")
	}
}

// TestCompressFileAtomicRename 回归：压缩先写临时文件、成功后原子改名，
// 不留临时文件，产物为合法 gzip。
func TestCompressFileAtomicRename(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "app-2026-10-05T00-00-00.000.log")
	if err := os.WriteFile(src, []byte("hello gzip"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := compressFile(src); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Error("原文件未删除")
	}
	bs, err := os.ReadFile(src + ".gz")
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(bs))
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello gzip" {
		t.Errorf("gz content = %q", got)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("压缩临时文件残留: %s", e.Name())
		}
	}
}

// TestCompressNotClobberedByPrune 回归：压缩进行中触发的清理不得把未完成
// 的 .gz 当作多余备份删除（MaxBackups=1 时会导致唯一备份彻底丢失）。
// 压缩经临时文件写入，prune 扫描不到半成品。
func TestCompressNotClobberedByPrune(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "app-2026-10-05T00-00-00.000.log")
	// xorshift 生成不可压缩内容，避免高度可压缩数据让压缩窗口一闪而过
	data := make([]byte, 4<<20)
	x := uint32(1)
	for i := range data {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		data[i] = byte(x)
	}
	if err := os.WriteFile(src, data, 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := newRotateWriter(RotateConfig{Filename: filepath.Join(dir, "app.log"), MaxBackups: 1, Compress: true})
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- compressFile(src) }()
	// 以临时文件自身出现为压缩进行中的信号，而不是目录项总数
	deadline := time.Now().Add(5 * time.Second)
	for {
		entries, _ := os.ReadDir(dir)
		inFlight := false
		for _, e := range entries {
			if strings.Contains(e.Name(), ".gz.tmp") {
				inFlight = true
			}
		}
		if inFlight {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("compression did not start")
		}
		time.Sleep(time.Millisecond)
	}
	w.prune()
	if err := <-finished; err != nil {
		t.Fatalf("compress: %v", err)
	}
	if _, err := os.Stat(src + ".gz"); err != nil {
		t.Errorf("唯一备份丢失: %v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Error("原文件未删除")
	}
}

// TestAnyNilMarshaler 回归：typed-nil 的 json.Marshaler 不得使日志入口 panic。
func TestAnyNilMarshaler(t *testing.T) {
	var out bytes.Buffer
	l := newTestLogger(t, &out, nil)
	defer func() {
		if p := recover(); p != nil {
			t.Errorf("typed-nil Marshaler 导致日志入口 panic: %v", p)
		}
	}()
	var raw *json.RawMessage
	l.Info("nil", Any("raw", raw))
	if v, ok := parseLines(t, out.String())[0]["raw"]; !ok || v != nil {
		t.Errorf("typed-nil Marshaler 应编码为 null: %q", out.String())
	}
}

// TestAnyRawMessageJSONL 回归：Marshaler 返回值不可直接拼接——非法内容
// 降级为错误字符串、多行内容被压缩，保证每条日志始终是合法的一行 JSON。
func TestAnyRawMessageJSONL(t *testing.T) {
	for _, raw := range []json.RawMessage{json.RawMessage(`{"n":`), json.RawMessage("1\n")} {
		var out bytes.Buffer
		l := newTestLogger(t, &out, nil)
		l.Info("raw", Any("v", raw))
		if !json.Valid(bytes.TrimSpace(out.Bytes())) {
			t.Errorf("非法 Marshaler 输出未校验: %q", out.String())
		}
		if strings.Count(out.String(), "\n") != 1 {
			t.Errorf("多行 Marshaler 输出破坏 JSON Lines: %q", out.String())
		}
	}
}

// ---------- benchmark 已迁移至独立模块 benchmarks/（含 zap 与标准库三方对比） ----------

func TestWrapHandler(t *testing.T) {
	var buf bytes.Buffer
	h := newJSONHandler(&buf, new(slog.LevelVar), "", false, false, false)
	l := WrapHandler(h)
	l.Info("wrapped", String("k", "v"))
	l.Sugar().Errorf("wrapped %d", 2)
	m := parseLines(t, buf.String())
	if m[0]["msg"] != "wrapped" || m[0]["k"] != "v" {
		t.Fatalf("wrap handler info: %v", m[0])
	}
	if m[1]["msg"] != "wrapped 2" || m[1]["level"] != "ERROR" {
		t.Fatalf("wrap handler sugar: %v", m[1])
	}
}

// ---------- P2 修复回归 ----------

func TestNewFailureClosesResources(t *testing.T) {
	dir := t.TempDir()
	blocked := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		Outputs: []string{"json:" + filepath.Join(dir, "first.log"), "console:" + filepath.Join(dir, "second.log"), "console:" + filepath.Join(blocked, "bad.log")},
		Async:   &AsyncConfig{Enabled: true},
	}
	_, _, closers, err := buildHandler(&cfg)
	if err == nil {
		t.Fatal("应因目录创建失败返回错误")
	}
	// 当前失败分组中已创建的文件资源也必须返回，供 New 统一清理。
	for _, c := range slices.Backward(closers) {
		_ = c.Close()
	}
	if len(closers) != 3 {
		t.Fatalf("返回 %d 个资源，want 3", len(closers))
	}
	countLoops := func() int {
		bs := make([]byte, 1<<20)
		n := runtime.Stack(bs, true)
		return strings.Count(string(bs[:n]), "zlog.(*asyncWriter).loop(")
	}
	before := countLoops()
	globalCloser.mu.Lock()
	registered := len(globalCloser.closers)
	globalCloser.mu.Unlock()
	for range 3 {
		l, err := New(cfg)
		if err == nil || l != nil {
			t.Fatalf("New = %v, %v，want nil + error", l, err)
		}
	}
	time.Sleep(10 * time.Millisecond)
	if after := countLoops(); after != before {
		t.Errorf("New 失败后遗留 %d 个消费者", after-before)
	}
	globalCloser.mu.Lock()
	defer globalCloser.mu.Unlock()
	if len(globalCloser.closers) != registered {
		t.Error("失败的装配不应注册到全局资源表")
	}
}

func TestLoggerAsyncStatAllBranches(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var out failWriter
		out.fail.Store(true)
		first := newAsyncWriter(&out, AsyncConfig{MaxQueueBytes: smallAsyncBufSize, FlushInterval: time.Hour})
		second := newAsyncWriter(&out, AsyncConfig{MaxQueueBytes: smallAsyncBufSize, FlushInterval: time.Hour})
		l := newLogger(slog.DiscardHandler, nil, []io.Closer{first, second}, false, false)
		defer l.Close()
		synctest.Wait()
		for _, w := range []*asyncWriter{first, second} {
			if _, err := w.Write([]byte("queued\n")); err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write([]byte("dropped\n")); err == nil {
				t.Error("队列已满，应报告溢出")
			}
		}
		if err := l.Sync(); !errors.Is(err, errInjected) {
			t.Errorf("Sync: %v", err)
		}
		stats, ok := l.AsyncStat()
		if !ok || stats.Dropped != 2 || stats.WriteErrors != 2 {
			t.Errorf("AsyncStat = %+v, %v", stats, ok)
		}
		if childStats, childOK := l.WithContext(context.Background()).AsyncStat(); childStats != stats || !childOK {
			t.Error("派生 Logger 统计应与父级一致")
		}
	})
}

func TestJSONTimeLayoutEscaping(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("zone\"\\\n", 0))
	for _, layout := range []string{"2006\"01\\02\n15:04", "MST", strings.Repeat("x", 500) + "\t15:04\"", defaultJSONTimeFormat} {
		t.Run(layout, func(t *testing.T) {
			var out bytes.Buffer
			l := newTestLogger(t, &out, func(c *Config) { c.TimeFormat = layout; c.Now = func() time.Time { return now } })
			// 分别覆盖行首、缓存字段、强类型字段与 Any 字段的时间编码。
			l.With(Time("cached", now)).Info("time", Time("typed", now), Any("any", now))
			m := parseLines(t, out.String())[0]
			want := now.Format(layout)
			for _, key := range []string{"time", "cached", "typed", "any"} {
				if m[key] != want {
					t.Errorf("%s = %v, want %q", key, m[key], want)
				}
			}
			if strings.Count(out.String(), "\n") != 1 {
				t.Error("时间值破坏 JSON Lines")
			}
		})
	}
}

// changingGroupValuer 第二次求值返回空组，用于检验每条记录的单次求值语义。
type changingGroupValuer struct{ calls int }

func (v *changingGroupValuer) LogValue() slog.Value {
	v.calls++
	if v.calls == 1 {
		return slog.IntValue(1)
	}
	return slog.GroupValue()
}

type anonymousGroupObject struct{ field Field }

func (o anonymousGroupObject) MarshalLogObject() []Field { return []Field{o.field} }

func TestJSONAnonymousGroupResolveOnce(t *testing.T) {
	for _, mode := range []string{"record", "with", "object"} {
		t.Run(mode, func(t *testing.T) {
			var out bytes.Buffer
			l := newTestLogger(t, &out, nil)
			v := &changingGroupValuer{}
			fields := make([]Field, 1, 3)
			fields[0] = Group("", Group("", Any("x", v)), Group("", Skip()))
			switch mode {
			case "record":
				l.Info("group", fields...)
			case "with":
				l.With(fields...).Info("group")
			case "object":
				l.Info("group", Any("obj", anonymousGroupObject{fields[0]}))
			}
			m := parseLines(t, out.String())[0]
			if mode == "object" {
				m = m["obj"].(map[string]any)
			}
			if m["x"] != float64(1) || v.calls != 1 {
				t.Errorf("x=%v, LogValue 调用 %d 次", m["x"], v.calls)
			}
			if !fields[:cap(fields)][1].Equal(Skip()) {
				t.Error("编码不得修改调用方字段切片")
			}
			// 第二次求值全部省略，验证最后一个匿名组不会留下悬空逗号。
			out.Reset()
			l.Info("empty", fields...)
			if _, exists := parseLines(t, out.String())[0]["x"]; exists {
				t.Error("空组未省略")
			}
		})
	}
}

func TestRotateBackupCollisionPrune(t *testing.T) {
	dir := t.TempDir()
	w, err := newRotateWriter(RotateConfig{Filename: filepath.Join(dir, "app.log"), MaxBackups: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	stamp := time.Now().UTC().Truncate(time.Millisecond)
	first := w.backupNameLocked(stamp)
	// 已压缩备份同样占用名称，不能被新备份覆盖。
	if err := os.WriteFile(first+".gz", []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	second := w.backupNameLocked(stamp)
	if second == first {
		t.Fatal("已压缩备份被重名覆盖")
	}
	if err := os.WriteFile(second, []byte("second"), 0o644); err != nil {
		t.Fatal(err)
	}
	third := w.backupNameLocked(stamp)
	if _, ok := parseBackupTime(filepath.Base(third), "app", ".log"); !ok {
		t.Fatalf("新备份无法解析：%s", third)
	}
	if err := os.WriteFile(third, []byte("third"), 0o644); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(dir, "app-2000-01-01T00-00-00.001(+1).log.gz")
	if err := os.WriteFile(legacy, []byte("legacy"), 0o644); err != nil {
		t.Fatal(err)
	}
	w.prune()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(third) {
		t.Errorf("MaxBackups 清理结果：%v", entries)
	}
	// 只有保留期约束时，旧格式备份也应被删除。
	w.cfg.MaxBackups, w.cfg.MaxAge = 0, 1
	if err := os.WriteFile(legacy, []byte("legacy"), 0o644); err != nil {
		t.Fatal(err)
	}
	w.prune()
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("MaxAge 未清理旧格式备份：%v", err)
	}
	for _, suffix := range []string{"(+bad).log", "(+0).log", "(+1.log", "(+1)(+2).log", "(+1).log.gz.tmp1"} {
		name := "app-2000-01-01T00-00-00.001" + suffix
		if _, ok := parseBackupTime(name, "app", ".log"); ok {
			t.Errorf("应忽略非法备份：%s", name)
		}
	}
}

func TestLogContextStackAndSource(t *testing.T) {
	var out bytes.Buffer
	ctx := context.WithValue(context.Background(), traceKey{}, "requested")
	defer SetTraceExtractor(nil)
	SetTraceExtractor(func(ctx context.Context) (TraceInfo, bool) {
		id, ok := ctx.Value(traceKey{}).(string)
		return TraceInfo{TraceID: id, SpanID: "span"}, ok
	})
	var hookContext context.Context
	l := newTestLogger(t, &out, func(c *Config) {
		c.AddSource = true
		c.StacktraceLevel = "error"
		c.Trace.Enabled = true
		c.Hooks = []Hook{func(ctx context.Context, r slog.Record) { hookContext = ctx }}
	}).WithContext(context.WithValue(context.Background(), traceKey{}, "bound"))
	fields := make([]Field, 1, 2)
	fields[0] = String("k", "v")
	_, _, line, _ := runtime.Caller(0)
	l.Log(ctx, LevelError, "via Log", fields...)
	m := parseLines(t, out.String())[0]
	if m["stack"] == nil || m["trace_id"] != "requested" || hookContext != ctx {
		t.Errorf("Log 语义不一致：%v", m)
	}
	if got := m["source"].(map[string]any)["line"]; got != float64(line+1) {
		t.Errorf("source line=%v, want %d", got, line+1)
	}
	if !fields[:cap(fields)][1].Equal(Skip()) {
		t.Error("Log 写入了调用方剩余切片")
	}
	out.Reset()
	l.Log(ctx, LevelInfo, "no stack")
	if parseLines(t, out.String())[0]["stack"] != nil {
		t.Error("低于 StacktraceLevel 不应输出堆栈")
	}
}

func TestDPanicFilteredConsistency(t *testing.T) {
	calls := []struct {
		name string
		log  func(*Logger)
	}{
		{"logger", func(l *Logger) { l.DPanic("dp") }},
		{"package", func(l *Logger) { DPanic("dp") }},
		{"package-f", func(l *Logger) { DPanicf("%s", "dp") }},
		{"package-w", func(l *Logger) { DPanicw("dp", "k", 1) }},
		{"sugar-f", func(l *Logger) { l.Sugar().DPanicf("%s", "dp") }},
		{"sugar-w", func(l *Logger) { l.Sugar().DPanicw("dp", "k", 1) }},
	}
	defer SetDefault(nil)
	for _, development := range []bool{false, true} {
		for _, level := range []string{"debug", "fatal"} {
			for _, call := range calls {
				t.Run(call.name+"/"+level+"/"+strconv.FormatBool(development), func(t *testing.T) {
					var out bytes.Buffer
					hooks := 0
					l := newTestLogger(t, &out, func(c *Config) {
						c.Development = development
						c.Level = level
						c.FatalHook = func(context.Context, slog.Record) { hooks++ }
					})
					SetDefault(l)
					defer SetDefault(nil)
					panicked := false
					func() { defer func() { panicked = recover() != nil }(); call.log(l) }()
					if panicked != development {
						t.Errorf("panic=%v, development=%v", panicked, development)
					}
					want := 0
					if level == "debug" {
						want = 1
					}
					if got := len(parseLines(t, out.String())); got != want {
						t.Errorf("输出 %d 条，want %d", got, want)
					}
					if !development {
						want = 0
					}
					if hooks != want {
						t.Errorf("FatalHook 调用 %d 次，want %d", hooks, want)
					}
				})
			}
		}
	}
}

// ---------- 全项目审查回归 ----------

func TestTeeSampledBranches(t *testing.T) {
	for _, mode := range []string{"first", "last", "wrapped", "nested"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var sampled, plain bytes.Buffer
				l := newTestLogger(t, &sampled, func(c *Config) {
					c.Level = "info"
					c.Sampler = &SamplerConfig{First: 2, Thereafter: 2, Tick: time.Hour}
				})
				var branch slog.Handler = l.Handler()
				hooks := 0
				if mode == "wrapped" {
					branch = &hookHandler{next: l.IncreaseLevel(LevelInfo).Handler(), hooks: []Hook{
						func(context.Context, slog.Record) { hooks++ },
					}}
				}
				other := newJSONHandler(&plain, new(slog.LevelVar), "", false, false, false)
				var h slog.Handler = Tee(branch, other)
				if mode == "last" {
					h = Tee(other, branch)
				} else if mode == "nested" {
					h = Tee(Tee(branch, slog.DiscardHandler), other)
				}
				lg := WrapHandler(h)
				// 单纯探测级别不能提前消耗分支采样额度。
				for range 3 {
					if !lg.Enabled(LevelInfo) {
						t.Fatal("info 应通过级别探测")
					}
				}
				lg.Debug("filtered")
				for i := range 6 {
					lg.Info("sampled", Int("i", i))
				}
				lines := parseLines(t, sampled.String())
				if len(lines) != 4 {
					t.Fatalf("采样分支输出 %d 条，want 4: %s", len(lines), sampled.String())
				}
				for i, want := range []int{0, 1, 3, 5} {
					if lines[i]["i"] != float64(want) {
						t.Errorf("第 %d 条采样记录: %v, want i=%d", i, lines[i], want)
					}
				}
				if got := len(parseLines(t, plain.String())); got != 6 {
					t.Errorf("普通分支输出 %d 条，want 6", got)
				}
				if mode == "wrapped" && hooks != 4 {
					t.Errorf("采样分支钩子调用 %d 次，want 4", hooks)
				}
			})
		})
	}
}

func TestTeeSampledConcurrent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var sampled, plain safeBuffer
		l, err := New(Config{Writer: &sampled, Sampler: &SamplerConfig{First: 40, Tick: time.Hour}})
		if err != nil {
			t.Fatal(err)
		}
		other := newJSONHandler(&plain, new(slog.LevelVar), "", false, false, false)
		lg := WrapHandler(Tee(l.Handler(), other))
		// 先建立当前 tick，避免把 tick 首次初始化的近似计数误差纳入断言。
		lg.Info("prime")
		var wg sync.WaitGroup
		for g := range 8 {
			wg.Go(func() {
				for i := range 20 {
					lg.Info("concurrent", Int("g", g), Int("i", i))
				}
			})
		}
		wg.Wait()
		if got := len(parseLines(t, sampled.String())); got != 40 {
			t.Errorf("采样分支输出 %d 条，want 40", got)
		}
		if got := len(parseLines(t, plain.String())); got != 161 {
			t.Errorf("普通分支输出 %d 条，want 161", got)
		}
	})
}

func TestWrapHandlerSource(t *testing.T) {
	for _, mode := range []string{"foreign", "native-wrapped", "mixed-tee"} {
		t.Run(mode, func(t *testing.T) {
			var out bytes.Buffer
			var h slog.Handler = slog.NewJSONHandler(&out, &slog.HandlerOptions{AddSource: true})
			if mode == "native-wrapped" {
				h = newTestLogger(t, &out, func(c *Config) {
					c.AddSource = true
					c.Sampler = &SamplerConfig{First: 100, Tick: time.Hour}
					c.Hooks = []Hook{func(context.Context, slog.Record) {}}
				}).IncreaseLevel(LevelInfo).Handler()
			} else if mode == "mixed-tee" {
				h = Tee(newJSONHandler(io.Discard, new(slog.LevelVar), "", false, false, false), h)
			}
			l := WrapHandler(h).Named("source").WithGroup("request")
			_, _, line, _ := runtime.Caller(0)
			l.Info("source")
			m := parseLines(t, out.String())[0]
			s, ok := m["source"].(map[string]any)
			if !ok || !strings.HasSuffix(s["file"].(string), "log_test.go") || s["line"] != float64(line+1) {
				t.Fatalf("调用位置错误: %v, want line=%d", m, line+1)
			}
		})
	}
	// 纯本库且关闭 source 的包装链仍应跳过 PC 采集。
	l := WrapHandler(Tee(
		newJSONHandler(io.Discard, new(slog.LevelVar), "", false, false, false),
		newConsoleHandler(io.Discard, new(slog.LevelVar), "", false, false, false),
	))
	if l.addSource {
		t.Error("无 source 需求时不应采集 PC")
	}
}

func TestSourceEntryPoints(t *testing.T) {
	defer SetDefault(nil)
	calls := []struct {
		name string
		call func(*Logger) (line int)
	}{
		{"debug", func(l *Logger) int { _, _, line, _ := runtime.Caller(0); l.Debug("source"); return line }},
		{"info", func(l *Logger) int { _, _, line, _ := runtime.Caller(0); l.Info("source"); return line }},
		{"warn", func(l *Logger) int { _, _, line, _ := runtime.Caller(0); l.Warn("source"); return line }},
		{"error", func(l *Logger) int { _, _, line, _ := runtime.Caller(0); l.Error("source"); return line }},
		{"package-info", func(l *Logger) int { _, _, line, _ := runtime.Caller(0); Info("source"); return line }},
		{"package-fatal", func(l *Logger) int { _, _, line, _ := runtime.Caller(0); Fatal("source"); return line }},
		{"package-panic", func(l *Logger) (line int) {
			defer func() { recover() }()
			_, _, line, _ = runtime.Caller(0)
			Panic("source")
			return line + 1
		}},
	}
	for _, call := range calls {
		t.Run(call.name, func(t *testing.T) {
			var out bytes.Buffer
			l := newTestLogger(t, &out, func(c *Config) { c.AddSource = true })
			l.SetExitFunc(func(int) {})
			SetDefault(l)
			defer SetDefault(nil)
			line := call.call(l)
			if call.name == "package-panic" {
				line++ // panic 后由具名返回值保留 runtime.Caller 所在行。
			}
			m := parseLines(t, out.String())[0]
			if got := m["source"].(map[string]any)["line"]; got != float64(line) {
				t.Errorf("source line=%v, want %d", got, line)
			}
		})
	}
}

func TestRotateFirstWriteChecksSize(t *testing.T) {
	file := filepath.Join(t.TempDir(), "app.log")
	old := strings.Repeat("x", (1<<20)-2)
	if err := os.WriteFile(file, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := newRotateWriter(RotateConfig{Filename: file, MaxSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := w.Write([]byte("new\n")); err != nil {
		t.Fatal(err)
	}
	bs, err := os.ReadFile(file)
	if err != nil || string(bs) != "new\n" {
		t.Fatalf("首次写入未滚动: size=%d, err=%v", len(bs), err)
	}
	entries, err := os.ReadDir(filepath.Dir(file))
	if err != nil {
		t.Fatal(err)
	}
	backups := 0
	for _, entry := range entries {
		if _, ok := parseBackupTime(entry.Name(), "app", ".log"); ok {
			backups++
			bs, err := os.ReadFile(filepath.Join(filepath.Dir(file), entry.Name()))
			if err != nil || string(bs) != old {
				t.Error("备份未保留原文件内容")
			}
		}
	}
	if backups != 1 {
		t.Errorf("备份数=%d, want 1", backups)
	}
}

func TestRotateBackupStrictSuffix(t *testing.T) {
	stamp := "2000-01-01T00-00-00.000"
	for _, ext := range []string{"", ".log", ".gz"} {
		for _, suffix := range []string{ext, ext + ".gz"} {
			if _, ok := parseBackupTime("app-"+stamp+suffix, "app", ext); !ok {
				t.Errorf("有效备份被拒绝: ext=%q, suffix=%q", ext, suffix)
			}
		}
	}
	dir := t.TempDir()
	w, err := newRotateWriter(RotateConfig{Filename: filepath.Join(dir, "app.log"), MaxAge: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	for _, suffix := range []string{"", ".gz", ".txt", ".txt.gz"} {
		name := "app-" + stamp + suffix
		if _, ok := parseBackupTime(name, "app", ".log"); ok {
			t.Errorf("其他目标的备份被接受: %s", name)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte("other"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w.prune()
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 4 {
		t.Errorf("清理误删其他目标: entries=%v, err=%v", entries, err)
	}
}

func TestRotateLocalTimeAge(t *testing.T) {
	for _, offset := range []int{-8, 8} {
		t.Run(strconv.Itoa(offset), func(t *testing.T) {
			oldLocal := time.Local
			time.Local = time.FixedZone("test", offset*60*60)
			defer func() { time.Local = oldLocal }()
			dir := t.TempDir()
			w, err := newRotateWriter(RotateConfig{Filename: filepath.Join(dir, "app.log"), LocalTime: true, MaxAge: 1})
			if err != nil {
				t.Fatal(err)
			}
			defer w.Close()
			recent := w.backupNameLocked(time.Now().Add(-20 * time.Hour))
			old := w.backupNameLocked(time.Now().Add(-28 * time.Hour))
			for _, file := range []string{recent, old} {
				if err := os.WriteFile(file, []byte("backup"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			w.prune()
			if _, err := os.Stat(recent); err != nil {
				t.Errorf("20 小时备份被提前删除: %v", err)
			}
			if _, err := os.Stat(old); !os.IsNotExist(err) {
				t.Errorf("28 小时备份未被删除: %v", err)
			}
		})
	}
}

func TestLoggerCloseUnregistersResources(t *testing.T) {
	globalCloser.mu.Lock()
	before := len(globalCloser.closers)
	globalCloser.mu.Unlock()
	for range 3 {
		l, err := New(Config{Outputs: []string{filepath.Join(t.TempDir(), "app.log")}, Async: &AsyncConfig{Enabled: true}})
		if err != nil {
			t.Fatal(err)
		}
		l.Info("close")
		child := l.WithGroup("child")
		var wg sync.WaitGroup
		for _, lg := range []*Logger{l, child} {
			wg.Go(func() {
				if err := lg.Close(); err != nil {
					t.Errorf("Close: %v", err)
				}
			})
		}
		wg.Wait()
	}
	globalCloser.mu.Lock()
	defer globalCloser.mu.Unlock()
	if after := len(globalCloser.closers); after != before {
		t.Errorf("关闭后全局表保留 %d 个资源引用", after-before)
	}
}

func TestInitUnregistersPreviousResources(t *testing.T) {
	SetDefault(nil)
	defer SetDefault(nil)
	globalCloser.mu.Lock()
	before := len(globalCloser.closers)
	globalCloser.mu.Unlock()
	for i := range 3 {
		if err := Init(Config{
			Outputs: []string{filepath.Join(t.TempDir(), "app.log")},
			Async:   &AsyncConfig{Enabled: true, FlushInterval: time.Hour},
		}); err != nil {
			t.Fatal(err)
		}
		Info("reinit", "i", i)
		globalCloser.mu.Lock()
		after := len(globalCloser.closers)
		globalCloser.mu.Unlock()
		if after != before+2 {
			t.Errorf("第 %d 次 Init 后保留 %d 个额外引用，want 2", i+1, after-before)
		}
	}
	if err := Default().Close(); err != nil {
		t.Fatal(err)
	}
	globalCloser.mu.Lock()
	defer globalCloser.mu.Unlock()
	if len(globalCloser.closers) != before {
		t.Error("最终 Close 后未释放全局引用")
	}
}

func TestArgsToAttrsInvalidKeyMatchesSlog(t *testing.T) {
	defer SetDefault(nil)
	for _, args := range [][]any{
		{123, "valid", "value"},
		{123, String("field", "value"), "valid", true},
		{"valid", 1, false, "odd"},
	} {
		for _, mode := range []string{"package", "sugar"} {
			var out, std bytes.Buffer
			l := newTestLogger(t, &out, func(c *Config) { c.DisableTime = true })
			SetDefault(l)
			stdInfo := slog.New(slog.NewJSONHandler(&std, &slog.HandlerOptions{
				ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
					if a.Key == slog.TimeKey {
						return slog.Attr{}
					}
					return a
				},
			})).Info
			// 函数变量避开 go vet 对故意无效 key 的静态诊断。
			stdInfo("bad-key", args...)
			if mode == "package" {
				Info("bad-key", args...)
			} else {
				l.Sugar().Infow("bad-key", args...)
			}
			ours, theirs := parseLines(t, out.String())[0], parseLines(t, std.String())[0]
			got, _ := json.Marshal(ours)
			want, _ := json.Marshal(theirs)
			if string(got) != string(want) {
				t.Errorf("%s 变参语义不一致: got=%s, want=%s", mode, got, want)
			}
		}
	}
}

func TestNamedThroughWrappers(t *testing.T) {
	for _, format := range []string{"json", "console"} {
		for _, wrapper := range []string{"hook", "sampler", "level", "tee", "combined"} {
			t.Run(format+"/"+wrapper, func(t *testing.T) {
				var out bytes.Buffer
				hooks := 0
				l := newTestLogger(t, &out, func(c *Config) {
					c.Level = "info"
					c.DisableTime = true
					c.Format = format
					if wrapper == "hook" || wrapper == "combined" {
						c.Hooks = []Hook{func(context.Context, slog.Record) { hooks++ }}
					}
					if wrapper == "sampler" || wrapper == "combined" {
						c.Sampler = &SamplerConfig{First: 1, Tick: time.Hour}
					}
				})
				if wrapper == "level" || wrapper == "combined" {
					l = l.IncreaseLevel(LevelInfo)
				}
				if wrapper == "tee" || wrapper == "combined" {
					l = WrapHandler(Tee(l.Handler(), slog.DiscardHandler))
				}
				parent := l.Named("api").WithGroup("request")
				child := parent.Named("db")
				child.Info("named", Int("id", 1))
				parent.Info("parent")
				child.Debug("filtered")
				if format == "json" {
					m := parseLines(t, out.String())[0]
					g := m["request"].(map[string]any)
					if m["logger"] != "api.db" || g["logger"] != nil || g["id"] != float64(1) {
						t.Fatalf("包装链名字未透传: %v", m)
					}
					if wrapper != "sampler" && wrapper != "combined" && parseLines(t, out.String())[1]["logger"] != "api" {
						t.Error("派生命名修改了父级")
					}
				} else if !strings.HasPrefix(out.String(), "INFO api.db named request.id=1") || strings.Contains(out.String(), "request.logger=") {
					t.Errorf("console 名字未透传: %q", out.String())
				}
				if wrapper == "hook" && hooks != 2 || wrapper == "combined" && hooks != 1 {
					t.Errorf("命名影响了钩子或采样: calls=%d", hooks)
				}
			})
		}
	}
}

// ---------- 审查修复：slog 契约、格式化容错与异步内存 ----------

func TestJSONHandlerConformance(t *testing.T) {
	var out bytes.Buffer
	l := newTestLogger(t, &out, nil)
	if err := slogtest.TestHandler(l.Handler(), func() []map[string]any {
		return parseLines(t, out.String())
	}); err != nil {
		t.Fatal(err)
	}
}

type emptyGroupValuer struct{}

func (emptyGroupValuer) LogValue() slog.Value { return slog.GroupValue() }

func TestJSONUnusedGroups(t *testing.T) {
	for _, cached := range []bool{false, true} {
		for _, dynamic := range []bool{false, true} {
			t.Run(strconv.FormatBool(cached)+"/"+strconv.FormatBool(dynamic), func(t *testing.T) {
				var out bytes.Buffer
				base := newTestLogger(t, &out, nil).With(Int("root", 1)).WithGroup("g")
				if cached {
					base = base.With(Int("cached", 2))
				}
				const name = "escaped\"{\\"
				child := base.WithGroup(name).WithGroup("tail").With(Skip(), Group("empty", Any("v", emptyGroupValuer{})))
				fields := []Field{Group("empty", Any("v", emptyGroupValuer{}))}
				if dynamic {
					fields = append(fields, Int("value", 3))
				}
				child.Info("child", fields...)
				base.Info("parent")
				lines := parseLines(t, out.String())
				for i, line := range lines {
					if line["root"] != float64(1) {
						t.Fatalf("根字段丢失: %v", line)
					}
					g, exists := line["g"].(map[string]any)
					wantGroup := cached || (dynamic && i == 0)
					if exists != wantGroup {
						t.Fatalf("组是否输出不符: %v", line)
					}
					if !exists {
						continue
					}
					if cached && g["cached"] != float64(2) {
						t.Fatalf("父组缓存丢失: %v", g)
					}
					if dynamic && i == 0 {
						tail := g[name].(map[string]any)["tail"].(map[string]any)
						if len(tail) != 1 || tail["value"] != float64(3) {
							t.Fatalf("动态字段不符: %v", tail)
						}
					} else if _, ok := g[name]; ok {
						t.Fatalf("空组未省略: %v", g)
					}
				}
			})
		}
	}
}

func TestHandlerZeroRecordTime(t *testing.T) {
	for _, format := range []string{"json", "console"} {
		for _, epoch := range []bool{false, true} {
			t.Run(format+"/"+strconv.FormatBool(epoch), func(t *testing.T) {
				var out bytes.Buffer
				l := newTestLogger(t, &out, func(c *Config) { c.Format = format; c.TimeEpochMillis = epoch })
				r := slog.NewRecord(time.Time{}, LevelInfo, "zero", 0)
				r.AddAttrs(Time("field_time", time.Time{}))
				if err := l.Handler().Handle(context.Background(), r); err != nil {
					t.Fatal(err)
				}
				if format == "json" {
					m := parseLines(t, out.String())[0]
					if _, ok := m["time"]; ok {
						t.Fatalf("零记录时间未省略: %v", m)
					}
					if m["field_time"] == nil {
						t.Fatal("不应省略字段内的零时间")
					}
				} else if !strings.HasPrefix(out.String(), "INFO zero field_time=0001-") {
					t.Fatalf("console 零时间语义不符: %q", out.String())
				}
			})
		}
	}
}

type panicLogError struct{}

func (panicLogError) Error() string { panic("error\"\nfailed") }

type panicLogStringer struct{}

func (panicLogStringer) String() string { panic("string\"\nfailed") }

type nilLogStringer struct{ text string }

func (s *nilLogStringer) String() string { return s.text }

type friendlyNilStringer struct{}

func (*friendlyNilStringer) String() string { return "nil handled" }

type panicMapStringer map[string]any

func (m panicMapStringer) String() string { panic(map[string]any(m)) }

func TestCyclicLogFields(t *testing.T) {
	// 栈溢出会终止整个 Go 进程。隔离每种编码器，让回归能快速报告失败。
	const childFormat = "HOLY_LOG_CYCLE_TEST_FORMAT"
	format := os.Getenv(childFormat)
	if format == "" {
		for _, mode := range []string{"json", "console"} {
			t.Run(mode, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCyclicLogFields$")
				cmd.Env = append(os.Environ(), childFormat+"="+mode)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("子进程失败: %v\n%.1500s", err, out)
				}
			})
		}
		return
	}

	oldLimit := debug.SetMaxStack(8 << 20)
	defer debug.SetMaxStack(oldLimit)
	var out bytes.Buffer
	l := newTestLogger(t, &out, func(c *Config) { c.Format = format; c.DisableTime = true })
	self := map[string]any{}
	self["self"] = self
	indirect := []any{nil}
	indirect[0] = map[string]any{"back": indirect}
	shared := map[string]int{"n": 1}
	dag := []any{shared, shared}
	// JSON 遇到 chan 会提前失败，文本降级还会展开未导出的字段。
	mixed := &struct {
		Ch     chan int
		hidden [1]any
	}{hidden: [1]any{self}}
	l.With(Any("cached", self)).Info("cycles", Any("indirect", indirect), Reflect("mixed", mixed),
		Any("panic", panicMapStringer(self)), String("after", "ok"))
	l.Sugar().Infow("next", "shared", dag)
	args := []any{self, 7}
	l.Sugar().Infof("map=%+[1]v n=%[2]d", args...)
	if _, ok := args[0].(map[string]any); !ok {
		t.Fatal("格式化修改了调用方的参数")
	}
	if format == "json" {
		lines := parseLines(t, out.String())
		if len(lines) != 3 {
			t.Fatalf("应输出三条日志: %s", out.String())
		}
		for k, want := range map[string]string{"cached": "<cycle>", "indirect": "<cycle>", "mixed": "<cycle>", "panic": "!PANIC: <cycle>", "after": "ok"} {
			if lines[0][k] != want {
				t.Errorf("%s = %v, want %q", k, lines[0][k], want)
			}
		}
		node := map[string]any{"n": float64(1)}
		if lines[1]["msg"] != "next" || !reflect.DeepEqual(lines[1]["shared"], []any{node, node}) || lines[2]["msg"] != "map=<cycle> n=7" {
			t.Fatalf("后续记录编码错误: %s", out.String())
		}
	} else if want := "INFO cycles cached=<cycle> indirect=<cycle> mixed=<cycle> panic=\"!PANIC: <cycle>\" after=ok\n" +
		"INFO next shared=\"[map[n:1] map[n:1]]\"\nINFO map=<cycle> n=7\n"; out.String() != want {
		t.Fatalf("console = %q, want %q", out.String(), want)
	}
	if got, want := logSprintf("%T %[1]p", self), fmt.Sprintf("%T %[1]p", self); got != want {
		t.Fatalf("类型和地址 = %q, want %q", got, want)
	}
	if got := logSprintf("value=%v.", self); got != "value=<cycle>." {
		t.Fatalf("单个循环实参 = %q", got)
	}
	if got := logSprintf("%*.*v", 8, 7, self); got != " <cycle>" {
		t.Fatalf("带宽度和精度的循环实参 = %q", got)
	}
	if got := logSprintf("%#v", panicMapStringer(self)); got != `"<cycle>"` {
		t.Fatalf("Go 语法格式化循环 map = %q", got)
	}
	defer func() {
		if got := recover(); got != "panic <cycle>" {
			t.Fatalf("Panicf 的 panic 值 = %T, want panic <cycle>", got)
		}
	}()
	l.Sugar().Panicf("panic %v", self)
}

func TestLogMethodPanicIsolation(t *testing.T) {
	for _, format := range []string{"json", "console"} {
		t.Run(format, func(t *testing.T) {
			var out bytes.Buffer
			l := newTestLogger(t, &out, func(c *Config) { c.Format = format })
			var err *os.PathError
			var str *nilLogStringer
			var friendly *friendlyNilStringer
			fields := []Field{Err(err), Any("stringer", str), Any("friendly", friendly),
				NamedError("panic_error", panicLogError{}), Any("panic_stringer", panicLogStringer{}), String("after", "ok")}
			l.Info("record", fields...)
			l.With(fields...).Info("cached")
			if format == "json" {
				lines := parseLines(t, out.String())
				if len(lines) != 2 {
					t.Fatalf("输出条数不符: %v", lines)
				}
				for _, m := range lines {
					if m["error"] != "<nil>" || m["stringer"] != "<nil>" || m["friendly"] != "nil handled" || m["after"] != "ok" {
						t.Fatalf("容错字段不符: %v", m)
					}
					if m["panic_error"] != "!PANIC: error\"\nfailed" || m["panic_stringer"] != "!PANIC: string\"\nfailed" {
						t.Fatalf("panic 诊断不符: %v", m)
					}
				}
			} else {
				if strings.Count(out.String(), "\n") != 2 || strings.Count(out.String(), "after=ok") != 2 || !strings.Contains(out.String(), "error=<nil> stringer=<nil>") || !strings.Contains(out.String(), "!PANIC:") {
					t.Fatalf("console 容错输出不符: %q", out.String())
				}
			}
		})
	}
}

// consumingAttrsHandler 在预编码后清空取得所有权的切片，属于合法外部 Handler。
type consumingAttrsHandler struct{ slog.Handler }

func (h consumingAttrsHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	child := h.Handler.WithAttrs(attrs)
	clear(attrs)
	return child
}

func TestTeeWithAttrsOwnership(t *testing.T) {
	var first, second, third bytes.Buffer
	h := Tee(consumingAttrsHandler{slog.NewJSONHandler(&first, nil)},
		consumingAttrsHandler{slog.NewJSONHandler(&second, nil)}, slog.NewJSONHandler(&third, nil))
	slog.New(h).With("key", "value").Info("m")
	for _, out := range []*bytes.Buffer{&first, &second, &third} {
		if parseLines(t, out.String())[0]["key"] != "value" {
			t.Fatalf("分支丢失字段: %s", out.String())
		}
	}
}

func TestAsyncConsumedBuffersCollectible(t *testing.T) {
	aw := newAsyncWriter(io.Discard, AsyncConfig{MaxQueueBytes: 4 << 20, FlushInterval: time.Hour})
	defer aw.Close()
	if err := aw.Sync(); err != nil {
		t.Fatal(err)
	}
	const count = 4
	released := make(chan struct{}, count)
	// 大缓冲不回池，排除 sync.Pool 对清理时机的影响。
	payload := bytes.Repeat([]byte{'x'}, maxPooledBufSize+1)
	for range count {
		if _, err := aw.Write(payload); err != nil {
			t.Fatal(err)
		}
	}
	func() {
		for n := aw.q.head.Load(); n != nil; n = n.next {
			runtime.AddCleanup(n.b, func(done chan struct{}) { done <- struct{}{} }, released)
		}
	}()
	if err := aw.Sync(); err != nil {
		t.Fatal(err)
	}
	// 后续批次比峰值批次小，旧切片的尾部也不得继续引用已消费缓冲。
	aw.Write([]byte("small\n"))
	if err := aw.Sync(); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for i := 0; i < count; {
		runtime.GC()
		select {
		case <-released:
			i++
		case <-time.After(10 * time.Millisecond):
		case <-deadline.C:
			t.Fatalf("仅回收 %d/%d 个已消费的大缓冲", i, count)
		}
	}
	runtime.KeepAlive(aw)
}

func TestAsyncSmallBufferCapacity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		aw := newAsyncWriter(io.Discard, AsyncConfig{MaxQueueBytes: 4096, FlushInterval: time.Hour})
		defer aw.Close()
		synctest.Wait()
		const count = 4096 / smallAsyncBufSize
		p := []byte("{\"msg\":\"small\"}\n")
		for range count {
			if _, err := aw.Write(p); err != nil {
				t.Fatal(err)
			}
		}
		if aw.q.loadSize() != 4096 {
			t.Fatalf("容量计费错误: %d", aw.q.loadSize())
		}
		if _, err := aw.Write(p); err == nil || aw.Stat().Dropped != 1 {
			t.Fatal("满队列必须丢弃并报告")
		}
		if err := aw.Sync(); err != nil {
			t.Fatal(err)
		}
		if aw.q.loadSize() != 0 {
			t.Fatal("排空后容量计数未归零")
		}
		for _, size := range []int{0, smallAsyncBufSize, smallAsyncBufSize + 1, maxPooledBufSize + 1} {
			b := getAsyncBuffer(size)
			if b.Len() != 0 {
				t.Fatal("归还的缓冲未重置")
			}
			b.bs = append(b.bs, make([]byte, size)...)
			if size <= smallAsyncBufSize && cap(b.bs) != smallAsyncBufSize {
				t.Fatalf("短日志缓冲容量=%d", cap(b.bs))
			}
			putAsyncBuffer(b)
		}
	})
}

func TestRotatePeriodKey(t *testing.T) {
	oldLocal := time.Local
	defer func() { time.Local = oldLocal }()
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	time.Local = loc
	for _, local := range []bool{false, true} {
		for _, tc := range []struct {
			p     RotatePeriod
			shape string // 与周期键一致的数字格式
		}{
			{RotateDaily, "20060102"},
			{RotateHourly, "2006010215"},
			{RotateMinutely, "200601021504"},
		} {
			w := &rotateWriter{cfg: RotateConfig{LocalTime: local, Period: tc.p}}
			for _, stamp := range []string{"2026-01-01T02:00:00Z", "2026-03-08T06:59:59Z", "2026-03-08T07:00:00Z", "2026-11-01T05:59:59Z", "2026-11-01T06:00:00Z"} {
				now, err := time.Parse(time.RFC3339, stamp)
				if err != nil {
					t.Fatal(err)
				}
				zoned := now.UTC()
				if local {
					zoned = now.In(loc)
				}
				want, _ := strconv.Atoi(zoned.Format(tc.shape))
				if got := w.periodKeyOf(now); got != int64(want) {
					t.Fatalf("local=%v, period=%d, %s: got=%d, want=%d", local, tc.p, stamp, got, want)
				}
			}
		}
	}
}

func TestDefaultJSONTimeMatchesLayout(t *testing.T) {
	for _, year := range []int{-10000, -1, 0, 1, 999, 2026, 9999, 10000} {
		for _, offset := range []int{-100 * 3600, -8*3600 - 30, -30, 0, 30, 5*3600 + 45*60, 100 * 3600} {
			for _, ns := range []int{0, 1, 999999, 1000000, 120000000, 999999999} {
				tm := time.Date(year, 12, 31, 23, 59, 58, ns, time.FixedZone("zone", offset))
				// 从几乎用尽的缓冲开始，覆盖插入小数时发生扩容的情况。
				b := &buffer{bs: make([]byte, 0, 4)}
				b.AppendString("pre")
				appendJSONTime(b, tm, defaultJSONTimeFormat)
				var got string
				if err := json.Unmarshal(b.bs[3:], &got); err != nil {
					t.Fatal(err)
				}
				if want := tm.Format(defaultJSONTimeFormat); got != want {
					t.Fatalf("got=%q, want=%q", got, want)
				}
			}
		}
	}
}

// ---------- 常见切片编码 ----------

type customLogStrings []string

func (customLogStrings) MarshalJSON() ([]byte, error) { return []byte(`["custom"]`), nil }
func (customLogStrings) String() string               { return "custom text" }

func TestCommonSliceEncoding(t *testing.T) {
	cases := []struct {
		name  string
		value any
	}{
		{"strings", []string{"alpha", "a b", "", "<>&", "\"\\\n\x00", "中文\u2028\u2029", "\xff"}},
		{"ints", []int{-42, 0, 42}},
		{"int64s", []int64{math.MinInt64, 0, math.MaxInt64}},
		{"uint64s", []uint64{0, math.MaxUint64}},
		{"floats", []float64{0, math.Copysign(0, -1), 1.5, 1e-9, 1e21, math.SmallestNonzeroFloat64, math.MaxFloat64}},
		{"bools", []bool{true, false}},
		{"nil_strings", []string(nil)}, {"empty_strings", []string{}},
		{"nil_ints", []int(nil)}, {"empty_ints", []int{}},
		{"nil_int64s", []int64(nil)}, {"empty_int64s", []int64{}},
		{"nil_uint64s", []uint64(nil)}, {"empty_uint64s", []uint64{}},
		{"nil_floats", []float64(nil)}, {"empty_floats", []float64{}},
		{"nil_bools", []bool(nil)}, {"empty_bools", []bool{}},
		{"nan", []float64{1, math.NaN()}},
		{"infinity", []float64{math.Inf(1), math.Inf(-1)}},
		{"bytes", []byte("hi")},
		{"named", customLogStrings{"original"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wantJSON, err := json.Marshal(tc.value)
			if err != nil {
				wantJSON, _ = json.Marshal(fmt.Sprintf("%+v", tc.value))
			}
			var want any
			if err := json.Unmarshal(wantJSON, &want); err != nil {
				t.Fatal(err)
			}
			field := Any("value", tc.value)
			// 同时覆盖导出构造器和 Any 的类型分派。
			if values, ok := tc.value.([]string); ok {
				field = Strings("value", values)
			} else if values, ok := tc.value.([]int); ok {
				field = Ints("value", values)
			}
			for _, mode := range []string{"dynamic", "with", "group"} {
				t.Run(mode, func(t *testing.T) {
					var output bytes.Buffer
					l := newTestLogger(t, &output, nil)
					switch mode {
					case "dynamic":
						l.Info("slices", String("before", "ok"), field, String("after", "ok"))
					case "with":
						l.With(String("before", "ok"), field).Info("slices", String("after", "ok"))
					case "group":
						l.WithGroup("group").With(field).Info("slices", String("after", "ok"))
					}
					got := parseLines(t, output.String())[0]
					if mode == "group" {
						got = got["group"].(map[string]any)
					}
					if !reflect.DeepEqual(got["value"], want) || got["after"] != "ok" {
						t.Fatalf("value=%#v, want=%#v; after=%v", got["value"], want, got["after"])
					}
					if tc.name == "strings" && !strings.Contains(output.String(), `\u003c\u003e\u0026`) {
						t.Fatal("切片字符串必须保留 HTML 转义")
					}
				})
			}
			// console 原样保留 fmt 格式，包括空格分隔和整值加引号。
			var output bytes.Buffer
			l := newTestLogger(t, &output, func(c *Config) { c.Format = "console"; c.DisableTime = true })
			l.Info("slices", Any("value", tc.value), String("after", "ok"))
			text := fmt.Sprintf("%+v", tc.value)
			if _, ok := tc.value.([]byte); ok {
				text = `"aGk="`
			} else {
				text = string(appendTextString(nil, text))
			}
			if want := " value=" + text + " after=ok\n"; !strings.HasSuffix(output.String(), want) {
				t.Fatalf("console=%q, want suffix=%q", output.String(), want)
			}
		})
	}
}

// ---------- 异步关闭的原子完成计数 ----------

func TestAsyncCloseRechecksAccepted(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var output safeBuffer
		aw := newAsyncWriter(&output, AsyncConfig{FlushInterval: time.Hour})
		synctest.Wait()
		// 模拟两个已经在锁内通过接受检查、尚未完成入队的生产者。
		aw.stateMu.Lock()
		aw.accepted.Add(2)
		aw.stateMu.Unlock()
		closed := make(chan error, 1)
		go func() { closed <- aw.Close() }()
		synctest.Wait()
		if !aw.isClosed.Load() {
			t.Fatal("Close 未开始")
		}
		// 即使收到过期或多余通知，Close 也必须检查当前计数并继续等待。
		aw.stateMu.Lock()
		aw.acceptedCond.Broadcast()
		aw.stateMu.Unlock()
		synctest.Wait()
		select {
		case err := <-closed:
			t.Fatalf("计数未归零却提前关闭：%v", err)
		default:
		}
		for i := range 2 {
			buf := getAsyncBuffer(16)
			buf.bs = append(buf.bs, "{\"n\":"...)
			buf.bs = strconv.AppendInt(buf.bs, int64(i), 10)
			buf.bs = append(buf.bs, '}', '\n')
			aw.q.enqueue(buf)
			aw.acceptedDone()
			synctest.Wait()
			if i == 0 {
				select {
				case err := <-closed:
					t.Fatalf("仍有一个在途写入却提前关闭：%v", err)
				default:
				}
			}
		}
		if err := <-closed; err != nil {
			t.Fatal(err)
		}
		if got := len(parseLines(t, output.String())); got != 2 {
			t.Fatalf("写出 %d 条，want 2", got)
		}
		if aw.accepted.Load() != 0 || aw.q.head.Load() != nil {
			t.Fatal("关闭后仍有在途写入")
		}
	})
}

func TestAsyncConcurrentWriteClose(t *testing.T) {
	for range 8 {
		var output safeBuffer
		aw := newAsyncWriter(&output, AsyncConfig{MaxQueueBytes: 1 << 20, FlushInterval: time.Hour})
		var succeeded atomic.Int64
		var writers sync.WaitGroup
		ready := make(chan struct{}, 8)
		for range 8 {
			writers.Go(func() {
				for i := range 64 {
					_, err := aw.Write([]byte("{\"msg\":\"accepted\"}\n"))
					if err == nil {
						succeeded.Add(1)
					} else if !errors.Is(err, errClosed) {
						t.Errorf("Write: %v", err)
					}
					if i == 0 {
						ready <- struct{}{}
					}
				}
			})
		}
		for range 8 {
			<-ready
		}
		if err := aw.Close(); err != nil {
			t.Fatal(err)
		}
		writers.Wait()
		if got := int64(len(parseLines(t, output.String()))); got != succeeded.Load() {
			t.Fatalf("成功接受 %d 条，实际写出 %d 条", succeeded.Load(), got)
		}
		if aw.accepted.Load() != 0 || aw.Stat().Dropped != 0 {
			t.Fatal("关闭后计数异常或发生丢弃")
		}
	}
}

// ---------- 内部性能护栏：时间周期检查与异步队列 ----------

func BenchmarkRotateTimeCheck(b *testing.B) {
	w := &rotateWriter{cfg: RotateConfig{Period: RotateDaily, MaxSize: 100}, file: os.Stdout}
	w.periodEnd = w.periodEndOf(w.now())
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := w.readyLocked(100); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAsyncEnqueueBatch(b *testing.B) {
	aw := newAsyncWriter(io.Discard, AsyncConfig{MaxQueueBytes: 64 << 20, FlushInterval: time.Hour})
	defer aw.Close()
	p := []byte("{\"level\":\"INFO\",\"msg\":\"small\"}\n")
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		if _, err := aw.Write(p); err != nil {
			b.Fatal(err)
		}
		if i%1024 == 1023 {
			if err := aw.Sync(); err != nil {
				b.Fatal(err)
			}
		}
	}
	if err := aw.Sync(); err != nil {
		b.Fatal(err)
	}
	b.StopTimer()
	if stats := aw.Stat(); stats.Dropped != 0 || stats.WriteErrors != 0 {
		b.Fatalf("异步基准未完整写出: %+v", stats)
	}
}

// ---------- 异步队列严格容量预算 ----------

func TestAsyncOversizedRecordRejected(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newAsyncWriter(io.Discard, AsyncConfig{MaxQueueBytes: 1024, FlushInterval: time.Hour})
		defer w.Close()
		synctest.Wait()
		if _, err := w.Write(bytes.Repeat([]byte("x"), 1<<20)); err == nil {
			t.Fatal("超大单条必须报告丢弃")
		}
		if w.Stat().Dropped != 1 || w.q.loadSize() != 0 || w.q.head.Load() != nil || w.accepted.Load() != 0 {
			t.Fatal("超大单条占用预算或遗留接受计数")
		}
	})
}

func TestAsyncConcurrentReservationStaysWithinBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const budget = 4096
		w := newAsyncWriter(io.Discard, AsyncConfig{MaxQueueBytes: budget, FlushInterval: time.Hour})
		defer w.Close()
		synctest.Wait()
		var wg sync.WaitGroup
		for range 128 {
			wg.Go(func() { _, _ = w.Write([]byte("record\n")) })
		}
		wg.Wait()
		queued := w.q.loadSize()
		if queued > budget {
			t.Fatalf("队列超额：%d > %d", queued, budget)
		}
		accepted := uint64(0)
		for node := w.q.head.Load(); node != nil; node = node.next {
			accepted++
		}
		if accepted+w.Stat().Dropped != 128 {
			t.Fatal("接受与丢弃计数不守恒")
		}
		if err := w.Sync(); err != nil {
			t.Fatal(err)
		}
		if w.q.loadSize() != 0 {
			t.Fatal("消费后未归还容量")
		}
	})
}

func TestAsyncBufferCapacityChargedBeforeCopy(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newAsyncWriter(io.Discard, AsyncConfig{MaxQueueBytes: 4096, FlushInterval: time.Hour})
		defer w.Close()
		synctest.Wait()
		if _, err := w.Write(bytes.Repeat([]byte("a"), 257)); err != nil {
			t.Fatal(err)
		}
		node := w.q.head.Load()
		if node == nil || w.q.loadSize() != int64(cap(node.b.bs)) || w.q.loadSize() > 4096 {
			t.Fatal("未按实际容量预留预算")
		}
	})
}

func TestAsyncLargePooledBufferDoesNotRejectSmallRecord(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		bufPool = sync.Pool{New: func() any { return &buffer{bs: make([]byte, 0, 65536)} }}
		defer func() { bufPool = sync.Pool{New: func() any { return &buffer{bs: make([]byte, 0, defaultBufSize)} }} }()
		w := newAsyncWriter(io.Discard, AsyncConfig{MaxQueueBytes: 4096, FlushInterval: time.Hour})
		defer w.Close()
		synctest.Wait()
		if _, err := w.Write(bytes.Repeat([]byte("a"), 512)); err != nil {
			t.Fatal(err)
		}
		if w.q.loadSize() != 512 {
			t.Fatalf("未替换超大池缓冲：%d", w.q.loadSize())
		}
	})
}

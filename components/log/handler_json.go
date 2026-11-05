package log

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"sync/atomic"
	"time"
)

const (
	defaultJSONTimeFormat    = "2006-01-02T15:04:05.000Z07:00"
	defaultConsoleTimeFormat = "2006-01-02 15:04:05.000"
	// 底层写失败时向 stderr 兜底输出的次数上限
	writeErrorReportThreshold = 32
)

var writeErrorReports atomic.Int64

// jsonHandler 是对标 zapcore 的高性能结构化 JSON Handler：
//   - WithAttrs/With 的公共字段只编码一次，缓存为原始 JSON 片段，输出时直接追加（零重复编码）
//   - WithGroup 打开的组在缓存中保持打开（zerolog 风格），后续字段自然落入组内，
//     行尾统一闭合，保证 slog 的组语义
//   - 值编码按 slog.Kind 分派 + 常见类型断言，避免反射与 fmt 装箱；json.Marshal 仅作兜底
//   - 行 buffer 走 sync.Pool，稳态下单条日志零堆分配
type jsonHandler struct {
	w          io.Writer
	leveler    slog.Leveler
	timeLayout string // 空 = 不输出字符串时间（DisableTime 或 epoch 模式）
	name       string // Named 的 Logger 名字
	nameJSON   []byte // 预编码后的 JSON 字符串，派生后只读共享

	// pre 是预编码片段，紧跟 msg 之后输出；以 '{' 结尾表示存在未闭合的组，
	// 形如 `"app":"api","g":{"a":1`
	pre []byte
	// open 是 pre 中未闭合的 '{' 数量，行尾需补齐对应 '}'
	open int
	// pending 标记缓存尾部尚无实际字段的组；无字段写入时整段省略。
	pending     *pendingJSONGroups
	epochMillis bool // 行首时间以 Unix 毫秒整数输出（timeLayout 为空时生效）
	addSource   bool
	traceOn     bool
}

// pendingJSONGroups 派生后只读共享，记录开启空组前的缓存边界与组数。
type pendingJSONGroups struct {
	start int
	open  int
}

// newJSONHandler 构建记录器；timeLayout 为空且 epochMillis 为假表示不输出
// 行首时间字段（DisableTime）。字段值内的时间始终有输出，见 valueTimeLayout。
func newJSONHandler(w io.Writer, leveler slog.Leveler, timeLayout string, addSource, traceOn, epochMillis bool) *jsonHandler {
	return &jsonHandler{
		w:           w,
		leveler:     leveler,
		timeLayout:  timeLayout,
		epochMillis: epochMillis,
		addSource:   addSource,
		traceOn:     traceOn,
	}
}

// valueTimeLayout 返回字段值内时间的编码布局；行首时间被关闭时仍需为字段值提供布局。
func valueTimeLayout(timeLayout string) string {
	if timeLayout == "" {
		return defaultJSONTimeFormat
	}
	return timeLayout
}

// withName 实现懒编码的 Logger 名字：仅克隆并记录，Handle 期写入。
func (h *jsonHandler) withName(name string) slog.Handler {
	n := h.clone()
	n.name = name
	n.nameJSON = appendJSONString(nil, name)
	return n
}

// Enabled 是第一道关卡：级别不够直接短路，后续所有编码成本都不会发生。
func (h *jsonHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.leveler.Level()
}

// clone 拷贝 handler；pre 用三段切片强制写时复制，避免共享底层数组。
func (h *jsonHandler) clone() *jsonHandler {
	nh := *h
	nh.pre = h.pre[:len(h.pre):len(h.pre)]
	return &nh
}

func (h *jsonHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	nh := h.clone()
	buf := getBuffer()
	defer putBuffer(buf)
	for _, a := range attrs {
		appendAttrJSON(buf, a, valueTimeLayout(h.timeLayout))
	}
	if buf.Len() == 0 {
		return h // 全部字段被跳过
	}
	// 并入缓存；若缓存以 '{' 结尾则字段自然落入打开的组内
	merged := make([]byte, 0, len(nh.pre)+buf.Len()+1)
	merged = append(merged, nh.pre...)
	if len(merged) > 0 && merged[len(merged)-1] != '{' {
		merged = append(merged, ',')
	}
	merged = append(merged, buf.bs...)
	nh.pre = merged
	nh.pending = nil
	return nh
}

func (h *jsonHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	nh := h.clone()
	if nh.pending == nil {
		nh.pending = &pendingJSONGroups{start: len(h.pre), open: h.open}
	}
	merged := make([]byte, 0, len(nh.pre)+len(name)+8)
	merged = append(merged, nh.pre...)
	if len(merged) > 0 && merged[len(merged)-1] != '{' {
		merged = append(merged, ',')
	}
	merged = appendJSONKey(merged, name)
	merged = append(merged, '{')
	nh.pre = merged
	nh.open++
	return nh
}

func (h *jsonHandler) Handle(ctx context.Context, r slog.Record) error {
	buf := getBuffer()

	buf.AppendByte('{')
	switch {
	case h.timeLayout != "" && !r.Time.IsZero():
		buf.AppendString(`"time":`)
		appendJSONTime(buf, r.Time, h.timeLayout)
		buf.AppendByte(',')
	case h.epochMillis && !r.Time.IsZero():
		buf.AppendString(`"time":`)
		buf.AppendInt(r.Time.UnixMilli())
		buf.AppendByte(',')
	}
	buf.AppendString(`"level":"`)
	buf.AppendString(levelName(r.Level))
	buf.AppendByte('"')

	if h.name != "" {
		buf.AppendString(`,"logger":`)
		buf.Write(h.nameJSON)
	}

	if h.addSource && r.PC != 0 {
		appendSourceJSON(buf, r.PC)
	}

	buf.AppendString(`,"msg":`)
	buf.bs = appendJSONString(buf.bs, r.Message)

	// 链路标识属于基础设施元数据，始终置于顶层
	if h.traceOn {
		if ti, ok := extractTrace(ctx); ok {
			buf.AppendString(`,"trace_id":`)
			buf.bs = appendJSONString(buf.bs, ti.TraceID)
			buf.AppendString(`,"span_id":`)
			buf.bs = appendJSONString(buf.bs, ti.SpanID)
		}
	}

	preStart := buf.Len()
	if len(h.pre) > 0 {
		appendCommaIfNeeded(buf)
		buf.Write(h.pre)
	}

	attrsStart := buf.Len()
	if r.NumAttrs() > 0 {
		r.Attrs(func(a slog.Attr) bool {
			appendAttrJSON(buf, a, valueTimeLayout(h.timeLayout))
			return true
		})
	}

	open := h.open
	if h.pending != nil && buf.Len() == attrsStart {
		// 所有动态字段被省略时，回退到首个未使用组之前；保留已缓存的父组字段。
		end := preStart
		if h.pending.start > 0 {
			end += 1 + h.pending.start // 缓存前的逗号
		}
		buf.bs = buf.bs[:end]
		open = h.pending.open
	}
	for i := 0; i < open; i++ {
		buf.AppendByte('}') // 闭合 WithGroup 打开的组
	}
	buf.AppendString("}\n")

	_, err := h.w.Write(buf.bs)
	if err != nil {
		reportWriteError(err, buf.bs)
	}
	putBuffer(buf)
	return err
}

// appendCommaIfNeeded 在写下一个对象成员前补逗号。
// JSON 对象值永远不会以 '{' 或 ',' 结尾，因此该判断局部且可靠；
// 空字段被跳过后也不会产生悬空或重复逗号。
func appendCommaIfNeeded(buf *buffer) {
	if buf.Len() == 0 {
		return
	}
	switch buf.bs[len(buf.bs)-1] {
	case '{', ',':
		return
	}
	buf.AppendByte(',')
}

// reportWriteError 在底层写入失败时兜底输出到 stderr（限次，避免雪崩）。
func reportWriteError(err error, line []byte) {
	if writeErrorReports.Add(1) > writeErrorReportThreshold {
		return
	}
	fmt.Fprintf(os.Stderr, "log: write failed: %v, raw line: %s\n", err, line)
}

func appendSourceJSON(buf *buffer, pc uintptr) {
	fs := runtime.CallersFrames([]uintptr{pc})
	f, _ := fs.Next()
	if f.File == "" {
		return
	}
	buf.AppendString(`,"source":{"file":`)
	buf.bs = appendJSONString(buf.bs, f.File)
	buf.AppendString(`,"function":`)
	buf.bs = appendJSONString(buf.bs, f.Function)
	buf.AppendString(`,"line":`)
	buf.AppendInt(int64(f.Line))
	buf.AppendByte('}')
}

// appendAttrJSON 统一求值、跳过空字段并补分隔符，每个 LogValuer 只求值一次。
// 匿名组由实际输出的子字段补逗号，避免空组留下悬空分隔符。
func appendAttrJSON(buf *buffer, a slog.Attr, timeLayout string) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}
	if a.Value.Kind() == slog.KindGroup {
		els := a.Value.Group()
		if len(els) == 0 {
			return
		}
		if a.Key == "" {
			// 匿名组：字段平铺到当前层级
			for _, el := range els {
				appendAttrJSON(buf, el, timeLayout)
			}
			return
		}
		start := buf.Len()
		appendCommaIfNeeded(buf)
		buf.bs = appendJSONKey(buf.bs, a.Key)
		buf.AppendByte('{')
		contentStart := buf.Len()
		for _, el := range els {
			appendAttrJSON(buf, el, timeLayout)
		}
		if buf.Len() == contentStart {
			buf.bs = buf.bs[:start] // Resolve 后为空的组也必须省略
			return
		}
		buf.AppendByte('}')
		return
	}

	appendCommaIfNeeded(buf)
	buf.bs = appendJSONKey(buf.bs, a.Key)
	appendValueJSON(buf, a.Value, timeLayout)
}

// appendJSONTime 保留默认布局的直写路径，自定义布局的格式化结果统一转义。
// MST 等布局可包含时区名称，不能仅检查布局本身是否含特殊字符。
func appendJSONTime(buf *buffer, t time.Time, layout string) {
	if layout == defaultJSONTimeFormat {
		buf.AppendByte('"')
		// 复用标准库 RFC3339 专用编码，再插入固定三位毫秒，避免通用布局解析。
		// 通过 T 定位秒末尾，兼容负年份、五位年份及非常规时区偏移。
		start := buf.Len()
		buf.AppendTime(t, time.RFC3339)
		fraction := start + bytes.IndexByte(buf.bs[start:], 'T') + len("T15:04:05")
		buf.bs = append(buf.bs, 0, 0, 0, 0)
		copy(buf.bs[fraction+4:], buf.bs[fraction:len(buf.bs)-4])
		millis := t.Nanosecond() / int(time.Millisecond)
		buf.bs[fraction] = '.'
		buf.bs[fraction+1] = '0' + byte(millis/100)
		buf.bs[fraction+2] = '0' + byte(millis/10%10)
		buf.bs[fraction+3] = '0' + byte(millis%10)
		buf.AppendByte('"')
		return
	}
	// 临时缓冲走池，长布局在稳态下也无需每次重新分配。
	scratch := getBuffer()
	scratch.AppendTime(t, layout)
	buf.bs = appendJSONString(buf.bs, string(scratch.bs))
	putBuffer(scratch)
}

// appendValueJSON 按 Kind 分派编码，避开反射；KindAny 走类型断言 + json.Marshal 兜底。
func appendValueJSON(buf *buffer, v slog.Value, timeLayout string) {
	switch v.Kind() {
	case slog.KindString:
		buf.bs = appendJSONString(buf.bs, v.String())
	case slog.KindInt64:
		buf.AppendInt(v.Int64())
	case slog.KindUint64:
		buf.AppendUint(v.Uint64())
	case slog.KindFloat64:
		buf.bs = appendJSONFloat(buf.bs, v.Float64())
	case slog.KindBool:
		buf.AppendBool(v.Bool())
	case slog.KindDuration:
		// 对齐 zap：Duration 以秒的浮点数输出，便于聚合分析
		buf.bs = appendJSONFloat(buf.bs, v.Duration().Seconds())
	case slog.KindTime:
		appendJSONTime(buf, v.Time(), valueTimeLayout(timeLayout))
	case slog.KindAny:
		appendAnyJSON(buf, v.Any(), timeLayout)
	default:
		// 理论上不可达
		buf.bs = appendJSONString(buf.bs, fmt.Sprint(v.Any()))
	}
}

func appendAnyJSON(buf *buffer, x any, timeLayout string) {
	switch xv := x.(type) {
	case nil:
		buf.AppendString("null")
	case error:
		buf.bs = appendJSONString(buf.bs, safeLogString(xv))
	case time.Duration:
		buf.bs = appendJSONFloat(buf.bs, xv.Seconds())
	case time.Time:
		appendJSONTime(buf, xv, valueTimeLayout(timeLayout))
	case []string:
		appendSliceJSON(buf, xv, appendSliceStringJSON)
	case []int:
		appendSliceJSON(buf, xv, appendSliceInt[int])
	case []int64:
		appendSliceJSON(buf, xv, appendSliceInt[int64])
	case []uint64:
		appendSliceJSON(buf, xv, appendSliceUint64)
	case []float64:
		appendFloatSliceJSON(buf, xv)
	case []bool:
		appendSliceJSON(buf, xv, strconv.AppendBool)
	case []byte:
		buf.AppendByte('"')
		buf.bs = base64.StdEncoding.AppendEncode(buf.bs, xv)
		buf.AppendByte('"')
	case LogObjectMarshaler:
		// 免反射：对象展开为嵌套 JSON 对象（空字段仍按规则跳过）
		buf.AppendByte('{')
		for _, f := range xv.MarshalLogObject() {
			appendAttrJSON(buf, f, timeLayout)
		}
		buf.AppendByte('}')
	case json.Marshaler:
		// 显式实现 json.Marshaler 的类型优先走 json.Marshal：直接调用
		// MarshalJSON 拼接返回值不安全——nil 指针接收者会 panic，输出也可能
		// 非法或含真实换行破坏 JSON Lines。json.Marshal 内部处理 nil 指针、
		// compact 与合法性校验；失败时保持既有降级方式（错误信息字符串）。
		bs, err := json.Marshal(xv)
		if err != nil {
			buf.bs = appendJSONString(buf.bs, err.Error())
			return
		}
		buf.Write(bs)
	case fmt.Stringer:
		buf.bs = appendJSONString(buf.bs, safeLogString(xv))
	default:
		appendReflectedJSON(buf, xv)
	}
}

// appendReflectedJSON 为其他类型及无法编码的切片保留原有降级路径。
func appendReflectedJSON(buf *buffer, value any) {
	bs, err := json.Marshal(value)
	if err != nil {
		buf.bs = appendJSONString(buf.bs, logValueText(value, "%+v"))
		return
	}
	buf.Write(bs)
}

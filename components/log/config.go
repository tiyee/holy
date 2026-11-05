package log

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"
)

// Format 是输出格式。
type Format uint8

const (
	FormatJSON    Format = iota // 结构化 JSON（生产默认）
	FormatConsole               // 人类可读（开发推荐）
)

func (f Format) String() string {
	switch f {
	case FormatConsole:
		return "console"
	default:
		return "json"
	}
}

// ParseFormat 解析格式字符串（json/console，默认 json）。
func ParseFormat(s string) (Format, error) {
	switch s {
	case "", "json":
		return FormatJSON, nil
	case "console", "text":
		return FormatConsole, nil
	default:
		return FormatJSON, fmt.Errorf("log: 无法解析输出格式 %q", s)
	}
}

// Config 是日志库的完整配置。
type Config struct {
	// Level 级别名称（debug/info/warn/error/dpanic/panic/fatal 或数字），默认 info
	Level string
	// Format 输出格式：json | console，默认 json
	Format string
	// Outputs 输出目标列表：stdout / stderr / discard / 文件路径，默认 [stdout]。
	// 每项可带 "json:"/"console:" 前缀覆盖该项的输出格式（Tee 多分支），
	// 如 ["console:stdout", "json:/var/log/app.log"]；未带前缀的项用 Format。
	Outputs []string
	// Writer 自定义输出（优先级高于 Outputs），主要供测试与特殊场景使用
	Writer io.Writer

	Development bool   // 开发模式：DPanic 触发 panic，console 输出着色
	AddSource   bool   // 输出调用位置 file:function:line
	TimeFormat  string // 自定义时间格式
	DisableTime bool   // 不输出时间字段
	// TimeEpochMillis 行首时间以 Unix 毫秒整数输出（仅 JSON，比字符串格式化更快）。
	// 优先于 TimeFormat；DisableTime 时不输出；console 输出忽略此项。
	TimeEpochMillis bool
	// Now 覆盖时间来源（nil 为 time.Now），测试用
	Now func() time.Time
	// StacktraceLevel 记录堆栈字段的最低级别（默认 panic；development 模式下 DPanic 也记录）
	StacktraceLevel string
	// File 任一 Outputs 为文件路径时的滚动配置模板
	File *RotateConfig
	// Async 异步写出（丢弃型内存上限语义，见 AsyncConfig）
	Async *AsyncConfig
	// Sampler 日志采样
	Sampler *SamplerConfig
	// Trace 链路关联
	Trace TraceConfig

	// Hooks 每条日志写出后依次触发（zap Hooks 选项的等价物）
	Hooks []Hook
	// FatalHook 在 Fatal（及开发模式 DPanic）记录写出后、Sync/退出前触发，
	// 适合冲刷 tracing、上报崩溃等收尾工作
	FatalHook Hook

	// InitialFields 全局公共字段，只编码一次长期缓存（WithAttrs 优化）
	InitialFields []slog.Attr
}

// TraceConfig 控制从 context 自动附加链路标识。
// 需先通过 SetTraceExtractor 注册提取函数（库不绑定具体追踪实现）。
type TraceConfig struct {
	Enabled bool
}

// normalize 填充默认值并校验。
func (c *Config) normalize() error {
	if len(c.Outputs) == 0 && c.Writer == nil {
		c.Outputs = []string{"stdout"}
	}
	if _, err := ParseFormat(c.Format); err != nil {
		return err
	}
	if _, err := ParseLevel(c.Level); err != nil {
		return err
	}
	if c.StacktraceLevel != "" {
		if _, err := ParseLevel(c.StacktraceLevel); err != nil {
			return err
		}
	}
	for _, out := range c.Outputs {
		if _, _, err := parseOutputTarget(out, FormatJSON); err != nil {
			return err
		}
	}
	return nil
}

// parseOutputTarget 解析输出项："json:"/"console:" 前缀覆盖该项的输出格式，
// 否则用 def（Config.Format）。返回格式与目标（stdout/stderr/discard/文件路径）。
func parseOutputTarget(out string, def Format) (Format, string, error) {
	if s, ok := strings.CutPrefix(out, "json:"); ok {
		if s == "" {
			return 0, "", fmt.Errorf("log: 输出目标 %q 缺少目标", out)
		}
		return FormatJSON, s, nil
	}
	if s, ok := strings.CutPrefix(out, "console:"); ok {
		if s == "" {
			return 0, "", fmt.Errorf("log: 输出目标 %q 缺少目标", out)
		}
		return FormatConsole, s, nil
	}
	return def, out, nil
}

// formatOf 解析格式字符串，失败返回 FormatJSON（normalize 已校验过，此处仅兜底）。
func formatOf(s string) Format {
	f, _ := ParseFormat(s)
	return f
}

// buildHandler 按配置装配 handler 链：
//
//	每个格式分组：writer(滚动/多路) → [async] → 编码器(json/console) → 公共字段
//	分支汇聚：Tee(各格式分支) → [sampler] → [hooks]
//
// 返回可关闭资源（异步队列、滚动文件句柄）供 Sync/Close 使用。
func buildHandler(cfg *Config) (slog.Handler, *slog.LevelVar, []io.Closer, error) {
	if err := cfg.normalize(); err != nil {
		return nil, nil, nil, err
	}

	levelVar := new(slog.LevelVar)
	lvl, _ := ParseLevel(cfg.Level)
	levelVar.Set(lvl)

	var closers []io.Closer

	// 时间编码参数：epoch 优先于字符串布局，DisableTime 时都没有
	epoch := cfg.TimeEpochMillis && !cfg.DisableTime
	jsonLayoutStr := jsonLayout(cfg)
	if epoch {
		jsonLayoutStr = ""
	}
	consoleLayoutStr := consoleLayout(cfg)

	// newBranch 组装单条链：writer → 编码器 → 公共字段预编码
	newBranch := func(w io.Writer, format Format) slog.Handler {
		var h slog.Handler
		if format == FormatConsole {
			h = newConsoleHandler(w, levelVar, consoleLayoutStr, cfg.AddSource, cfg.Development, cfg.Trace.Enabled)
		} else {
			h = newJSONHandler(w, levelVar, jsonLayoutStr, cfg.AddSource, cfg.Trace.Enabled, epoch)
		}
		if len(cfg.InitialFields) > 0 {
			h = h.WithAttrs(cfg.InitialFields)
		}
		return h
	}

	var h slog.Handler
	if cfg.Writer != nil {
		// 自定义 writer：单链直连（不参与异步包装，调用方自行控制）
		h = newBranch(cfg.Writer, formatOf(cfg.Format))
	} else {
		// 多输出按格式分组，每组一条链，最后 Tee 汇聚
		var groups []formatGroup
		idx := map[Format]int{}
		for _, out := range cfg.Outputs {
			format, target, err := parseOutputTarget(out, formatOf(cfg.Format))
			if err != nil {
				return nil, nil, closers, err
			}
			if i, ok := idx[format]; ok {
				groups[i].targets = append(groups[i].targets, target)
				continue
			}
			idx[format] = len(groups)
			groups = append(groups, formatGroup{format: format, targets: []string{target}})
		}
		branches := make([]slog.Handler, 0, len(groups))
		for _, g := range groups {
			w, cs, err := buildWriters(g.targets, cfg)
			closers = append(closers, cs...)
			if err != nil {
				return nil, nil, closers, err
			}
			branches = append(branches, newBranch(w, g.format))
		}
		h = Tee(branches...)
	}

	// 采样在 Enabled 阶段发生；外层钩子透传该决策，被丢弃的日志不会编码或触发钩子。
	if cfg.Sampler != nil {
		h = newSamplerHandler(h, *cfg.Sampler)
	}
	if len(cfg.Hooks) > 0 {
		h = &hookHandler{next: h, hooks: cfg.Hooks}
	}
	return h, levelVar, closers, nil
}

// formatGroup 是同一输出格式的一组目标。
type formatGroup struct {
	format  Format
	targets []string
}

// buildWriters 组装一组输出目标：stdout/stderr 加锁防行交错，文件路径套滚动，
// 多目标 MultiWriter，按需再套异步。
func buildWriters(targets []string, cfg *Config) (io.Writer, []io.Closer, error) {
	var (
		ws      []io.Writer
		closers []io.Closer
	)
	for _, out := range targets {
		switch out {
		case "", "stdout":
			ws = append(ws, &lockedWriter{w: os.Stdout})
		case "stderr":
			ws = append(ws, &lockedWriter{w: os.Stderr})
		case "discard", "null", "/dev/null":
			ws = append(ws, io.Discard)
		default:
			rc := RotateConfig{}
			if cfg.File != nil {
				rc = *cfg.File
			}
			rc.Filename = out
			rw, err := newRotateWriter(rc)
			if err != nil {
				return nil, closers, err
			}
			ws = append(ws, rw)
			closers = append(closers, rw)
		}
	}

	var w io.Writer = ws[0]
	if len(ws) > 1 {
		w = io.MultiWriter(ws...)
	}

	if cfg.Async != nil && cfg.Async.Enabled {
		aw := newAsyncWriter(w, *cfg.Async)
		closers = append(closers, aw)
		w = aw
	}
	return w, closers, nil
}

func jsonLayout(cfg *Config) string {
	switch {
	case cfg.DisableTime:
		return ""
	case cfg.TimeFormat != "":
		return cfg.TimeFormat
	default:
		return defaultJSONTimeFormat
	}
}

func consoleLayout(cfg *Config) string {
	switch {
	case cfg.DisableTime:
		return ""
	case cfg.TimeFormat != "":
		return cfg.TimeFormat
	default:
		return defaultConsoleTimeFormat
	}
}

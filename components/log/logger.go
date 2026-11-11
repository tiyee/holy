package log

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"runtime"
	"slices"
	"sync/atomic"
	"time"
)

// Logger 提供对齐 zap.Logger 的强类型 API，底层完全基于 slog.Handler：
//
//	l, err := log.New(cfg)
//	if err != nil {
//		return err
//	}
//	l.Info("user login", log.String("uid", "42"), log.Int("source", 3))
//	s := l.Sugar()
//	s.Infof("user login uid=%s", uid)
//	s.Infow("user login", "uid", uid, "source", 3)
type Logger struct {
	handler slog.Handler
	// std 是同 handler 的标准 slog 视图，惰性创建（派生 Logger 不再为此分配）
	std  atomic.Pointer[slog.Logger]
	ctx  context.Context
	name string

	development bool
	addSource   bool
	now         func() time.Time
	stackLevel  slog.Level
	fatalHook   Hook
	exit        func(int)
	closers     []io.Closer
	levelVar    *slog.LevelVar
}

// New 按配置构建独立的 Logger。调用方负责关闭实例所拥有的资源。
func New(cfg Config) (*Logger, error) {
	h, lv, closers, err := buildHandler(&cfg)
	if err != nil {
		errs := []error{err}
		// 装配失败也要逆序关闭已创建资源，避免遗留异步消费者与文件句柄。
		for _, c := range slices.Backward(closers) {
			if closeErr := c.Close(); closeErr != nil {
				errs = append(errs, closeErr)
			}
		}
		return nil, errors.Join(errs...)
	}
	l := newLogger(h, lv, closers, cfg.Development, cfg.AddSource)
	l.now = cfg.Now
	l.fatalHook = cfg.FatalHook
	if cfg.StacktraceLevel != "" {
		if sl, err := ParseLevel(cfg.StacktraceLevel); err == nil {
			l.stackLevel = sl
		}
	}
	return l, nil
}

func newLogger(h slog.Handler, lv *slog.LevelVar, closers []io.Closer, development, addSource bool) *Logger {
	return &Logger{
		handler:     h,
		ctx:         context.Background(),
		development: development,
		addSource:   addSource,
		stackLevel:  LevelPanic,
		exit:        os.Exit,
		closers:     closers,
		levelVar:    lv,
	}
}

// SetExitFunc 替换进程退出函数（测试 Fatal 用）。
func (l *Logger) SetExitFunc(fn func(int)) { l.exit = fn }

// WrapHandler 将任意 slog.Handler（本库、标准库或第三方的）包装为 Logger，
// 使其获得 zap 风格 API、堆栈/级别扩展与 Fatal 语义。
// handler 持有的资源由创建方管理；Sync 仅冲刷由 New 为实例创建的资源。
// 本库 handler 按其 AddSource 配置决定是否采集 PC；外部 handler 的选项不可
// 查询，因此默认采集 PC，供其 source 输出使用。
func WrapHandler(h slog.Handler) *Logger {
	return newLogger(h, nil, nil, false, handlerNeedsSource(h))
}

// handlerNeedsSource 沿本库包装链检查 source 需求，未知实现保留调用位置。
func handlerNeedsSource(h slog.Handler) bool {
	switch h := h.(type) {
	case *jsonHandler:
		return h.addSource
	case *consoleHandler:
		return h.addSource
	case *samplerHandler:
		return handlerNeedsSource(h.next)
	case *hookHandler:
		return handlerNeedsSource(h.next)
	case *levelFilterHandler:
		return handlerNeedsSource(h.next)
	case *teeHandler:
		for _, branch := range h.handlers {
			if handlerNeedsSource(branch) {
				return true
			}
		}
		return false
	default:
		return h != slog.DiscardHandler
	}
}

// Handler 返回底层 slog.Handler。
func (l *Logger) Handler() slog.Handler { return l.handler }

// StdLogger 返回等价的 *slog.Logger（惰性创建并缓存），便于与标准库生态互通。
func (l *Logger) StdLogger() *slog.Logger {
	if s := l.std.Load(); s != nil {
		return s
	}
	s := slog.New(l.handler)
	if l.std.CompareAndSwap(nil, s) {
		return s
	}
	return l.std.Load()
}

// LevelVar 返回运行时可修改级别的句柄。
func (l *Logger) LevelVar() *slog.LevelVar { return l.levelVar }

// SetLevel 运行时动态调整级别。
func (l *Logger) SetLevel(lvl slog.Level) {
	if l.levelVar != nil {
		l.levelVar.Set(lvl)
	}
}

// Enabled 判断级别是否会被输出。
func (l *Logger) Enabled(lvl slog.Level) bool { return l.handler.Enabled(l.ctx, lvl) }

// With 派生携带公共字段的子 Logger（字段只编码一次，长期缓存）。
func (l *Logger) With(fields ...Field) *Logger {
	return l.withHandler(l.handler.WithAttrs(fields))
}

// WithGroup 派生携带组前缀的子 Logger：JSON 输出嵌套对象，console 输出点分前缀。
func (l *Logger) WithGroup(name string) *Logger {
	return l.withHandler(l.handler.WithGroup(name))
}

// nameSetter 是本库 handler 对 Logger 名字懒编码的可选支持：
// Named 派生时只挂名字、编码期才写 "logger" 字段（对齐 zap 的懒编码）。
// 本库包装链透传名字，外部 handler 回退为 WithAttrs 公共字段。
type nameSetter interface {
	withName(name string) slog.Handler
}

func setHandlerName(h slog.Handler, name string) slog.Handler {
	if ns, ok := h.(nameSetter); ok {
		return ns.withName(name)
	}
	return h.WithAttrs([]Field{String("logger", name)})
}

// Named 派生具名子 Logger，输出 "logger":"a.b" 字段
// （JSON 位于 level 之后，console 位于级别之后）。派生本身零字段编码。
func (l *Logger) Named(name string) *Logger {
	if name == "" {
		return l
	}
	joined := name
	if l.name != "" {
		joined = l.name + "." + name
	}
	nl := l.withHandler(setHandlerName(l.handler, joined))
	nl.name = joined
	return nl
}

// WithContext 绑定请求上下文，链路追踪从 ctx 提取。
func (l *Logger) WithContext(ctx context.Context) *Logger {
	if ctx == nil {
		return l
	}
	nl := l.withHandler(l.handler)
	nl.ctx = ctx
	return nl
}

func (l *Logger) withHandler(h slog.Handler) *Logger {
	return &Logger{
		handler:     h,
		ctx:         l.ctx,
		name:        l.name,
		development: l.development,
		addSource:   l.addSource,
		now:         l.now,
		stackLevel:  l.stackLevel,
		fatalHook:   l.fatalHook,
		exit:        l.exit,
		closers:     l.closers,
		levelVar:    l.levelVar,
	}
}

// Sync 冲刷全部缓冲（异步队列等），汇总各输出分支的错误。
func (l *Logger) Sync() error {
	var errs []error
	for _, c := range l.closers {
		if s, ok := c.(interface{ Sync() error }); ok {
			if err := s.Sync(); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// AsyncStat 返回全部异步输出分支的计数总和（丢弃数、写失败数）；
// 未启用异步时 ok 为 false。
func (l *Logger) AsyncStat() (stats AsyncStats, ok bool) {
	for _, c := range l.closers {
		if s, found := c.(interface{ Stat() AsyncStats }); found {
			branch := s.Stat()
			stats.Dropped += branch.Dropped
			stats.WriteErrors += branch.WriteErrors
			ok = true
		}
	}
	return stats, ok
}

// IncreaseLevel 返回只提高最低输出级别的派生 Logger：父级再调低也不会低于该级别。
// 与父级共享底层资源与级别变量（SetLevel 仍作用于整棵树），本方法只加不减。
func (l *Logger) IncreaseLevel(lvl slog.Level) *Logger {
	return l.withHandler(&levelFilterHandler{next: l.handler, min: lvl})
}

// levelFilterHandler 在 next 之前加一道最低级别闸门（zap IncreaseLevel 的等价物）。
type levelFilterHandler struct {
	next slog.Handler
	min  slog.Level
}

func (h *levelFilterHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return l >= h.min && h.next.Enabled(ctx, l)
}

func (h *levelFilterHandler) Handle(ctx context.Context, r slog.Record) error {
	return h.next.Handle(ctx, r)
}

func (h *levelFilterHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &levelFilterHandler{next: h.next.WithAttrs(attrs), min: h.min}
}

func (h *levelFilterHandler) WithGroup(name string) slog.Handler {
	return &levelFilterHandler{next: h.next.WithGroup(name), min: h.min}
}

func (h *levelFilterHandler) withName(name string) slog.Handler {
	return &levelFilterHandler{next: setHandlerName(h.next, name), min: h.min}
}

// Close 冲刷并关闭 Logger 自身持有的资源（异步队列、滚动文件等）。
// 派生 Logger 与父级共享同一组资源，关闭派生 Logger 会一并关闭父级；
// 资源按创建的逆序（LIFO）关闭，保证 asyncWriter 先于其下游文件关闭。
func (l *Logger) Close() error {
	var errs []error
	for _, c := range slices.Backward(l.closers) {
		if err := c.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ---------- 强类型 API（对应 zap.Logger） ----------

func (l *Logger) Debug(msg string, fields ...Field) {
	if l.handler.Enabled(l.ctx, LevelDebug) {
		l.emit(l.ctx, LevelDebug, msg, fields, 4)
	}
}

func (l *Logger) Info(msg string, fields ...Field) {
	if l.handler.Enabled(l.ctx, LevelInfo) {
		l.emit(l.ctx, LevelInfo, msg, fields, 4)
	}
}

func (l *Logger) Warn(msg string, fields ...Field) {
	if l.handler.Enabled(l.ctx, LevelWarn) {
		l.emit(l.ctx, LevelWarn, msg, fields, 4)
	}
}

func (l *Logger) Error(msg string, fields ...Field) {
	if l.handler.Enabled(l.ctx, LevelError) {
		l.emit(l.ctx, LevelError, msg, fields, 4)
	}
}

// DPanic 在开发模式（Development=true）下记录（含堆栈）、触发 FatalHook 并冲刷后 panic，
// 生产环境仅记录。
func (l *Logger) DPanic(msg string, fields ...Field) {
	if l.development {
		if r, ok := l.emitChecked(LevelDPanic, msg, fields); ok {
			l.runFatalHooks(r)
		}
		_ = l.Sync()
		panic(msg)
	}
	l.emitChecked(LevelDPanic, msg, fields)
}

// Panic 记录（含堆栈）并冲刷后 panic(msg)；级别被过滤时仍冲刷已有日志并 panic。
func (l *Logger) Panic(msg string, fields ...Field) {
	l.emitChecked(LevelPanic, msg, fields)
	_ = l.Sync()
	panic(msg)
}

// Fatal 记录（含堆栈）并触发 FatalHook，随后冲刷日志并 exit(1)；
// 级别被过滤时跳过记录但仍退出。
func (l *Logger) Fatal(msg string, fields ...Field) {
	if r, ok := l.emitChecked(LevelFatal, msg, fields); ok {
		l.runFatalHooks(r)
	}
	_ = l.Sync()
	l.exit(1)
}

// runFatalHooks 在进程退出路径上触发 FatalHook。
func (l *Logger) runFatalHooks(r slog.Record) {
	if l.fatalHook != nil {
		l.fatalHook(l.ctx, r)
	}
}

// Log 是带 ctx 与自定义级别的通用入口。
func (l *Logger) Log(ctx context.Context, lvl slog.Level, msg string, fields ...Field) {
	if !l.handler.Enabled(ctx, lvl) {
		return
	}
	l.emit(ctx, lvl, msg, fields, 4)
}

// log 直通入口（测试与内部使用）：Enabled 检查后走 emit。
func (l *Logger) log(lvl slog.Level, msg string, fields []Field) {
	if !l.handler.Enabled(l.ctx, lvl) {
		return
	}
	l.emit(l.ctx, lvl, msg, fields, 4)
}

// emitChecked = Enabled 检查 + emit（调用链：用户 → 公开方法 → emitChecked → emit，skip 5）。
func (l *Logger) emitChecked(lvl slog.Level, msg string, fields []Field) (slog.Record, bool) {
	if !l.handler.Enabled(l.ctx, lvl) {
		return slog.Record{}, false
	}
	return l.emit(l.ctx, lvl, msg, fields, 5)
}

// emit 构建并输出记录；调用方必须已通过 Enabled 检查（避免重复消耗采样计数）。
// callerSkip 同 runtime.Callers 语义：4=调用方是 emit 的直接调用者，5=再上一层。
func (l *Logger) emit(ctx context.Context, lvl slog.Level, msg string, fields []Field, callerSkip int) (slog.Record, bool) {
	r := newRecord(lvl, msg, callerSkip, fields, l.addSource, l.now)
	if l.needStack(lvl) {
		// 不能对调用方的 fields 做 append：它可能是复用的带剩余容量切片，
		// 并发记录堆栈级别日志时会写进同一个底层数组（数据竞争）。
		// 先按原字段构建 Record，再把 stack 作为附加属性挂上；
		// 仅堆栈慢路径多一次遍历，普通热路径不受影响。
		r.AddAttrs(String("stack", stackTrace()))
	}
	_ = l.handler.Handle(ctx, r)
	return r, true
}

// needStack 报告该级别是否附带堆栈：达到 StacktraceLevel，
// 或开发模式下的 DPanic 及以上。
func (l *Logger) needStack(lvl slog.Level) bool {
	return lvl >= l.stackLevel || (l.development && lvl >= LevelDPanic)
}

// newRecord 构建日志记录；needPC 为 false 时跳过调用栈回溯
// （AddSource 关闭时省去该开销，handler 也不会输出 source）。
// callerSkip 与 runtime.Callers 的 skip 语义一致
// （0=Callers 自身，1=newRecord，2=调用者……），用于记录用户调用位置。
func newRecord(lvl slog.Level, msg string, callerSkip int, fields []Field, needPC bool, now func() time.Time) slog.Record {
	var pc uintptr
	if needPC {
		var pcs [1]uintptr
		runtime.Callers(callerSkip, pcs[:])
		pc = pcs[0]
	}
	if now == nil {
		now = time.Now
	}
	r := slog.NewRecord(now(), lvl, msg, pc)
	if len(fields) > 0 {
		r.AddAttrs(fields...)
	}
	return r
}

func stackTrace() string {
	bs := make([]byte, 4096)
	for {
		n := runtime.Stack(bs, false)
		if n < len(bs) {
			return string(bs[:n])
		}
		bs = make([]byte, 2*len(bs))
	}
}

// ---------- SugaredLogger（对应 zap.SugaredLogger） ----------

// SugaredLogger 支持拼接消息、格式化消息和结构化键值对。
type SugaredLogger struct {
	l *Logger
}

func (l *Logger) Sugar() *SugaredLogger { return &SugaredLogger{l: l} }

func (s *SugaredLogger) Desugar() *Logger { return s.l }

func (s *SugaredLogger) Debug(args ...any) { s.l.logArgs(LevelDebug, args) }
func (s *SugaredLogger) Info(args ...any)  { s.l.logArgs(LevelInfo, args) }
func (s *SugaredLogger) Warn(args ...any)  { s.l.logArgs(LevelWarn, args) }
func (s *SugaredLogger) Error(args ...any) { s.l.logArgs(LevelError, args) }

func (s *SugaredLogger) DPanic(args ...any) {
	r, ok := s.l.logArgs(LevelDPanic, args)
	if s.l.development {
		if ok {
			s.l.runFatalHooks(r)
		}
		s.panicWithMessage(r, ok, args)
	}
}

func (s *SugaredLogger) Panic(args ...any) {
	r, ok := s.l.logArgs(LevelPanic, args)
	s.panicWithMessage(r, ok, args)
}

func (s *SugaredLogger) Fatal(args ...any) {
	if r, ok := s.l.logArgs(LevelFatal, args); ok {
		s.l.runFatalHooks(r)
	}
	_ = s.l.Sync()
	s.l.exit(1)
}

func (s *SugaredLogger) panicWithMessage(r slog.Record, logged bool, args []any) {
	_ = s.l.Sync()
	if logged {
		panic(r.Message)
	}
	panic(logSprint(args))
}

func (s *SugaredLogger) Debugf(format string, args ...any) { s.l.logf(LevelDebug, format, args) }
func (s *SugaredLogger) Infof(format string, args ...any)  { s.l.logf(LevelInfo, format, args) }
func (s *SugaredLogger) Warnf(format string, args ...any)  { s.l.logf(LevelWarn, format, args) }
func (s *SugaredLogger) Errorf(format string, args ...any) { s.l.logf(LevelError, format, args) }
func (s *SugaredLogger) DPanicf(format string, args ...any) {
	if s.l.development {
		if r, ok := s.l.logf(LevelDPanic, format, args); ok {
			s.l.runFatalHooks(r)
		}
		_ = s.l.Sync()
		panic(logSprintf(format, args...))
	}
	s.l.logf(LevelDPanic, format, args)
}

func (s *SugaredLogger) Panicf(format string, args ...any) {
	s.l.logf(LevelPanic, format, args)
	_ = s.l.Sync()
	panic(logSprintf(format, args...))
}

func (s *SugaredLogger) Fatalf(format string, args ...any) {
	if r, ok := s.l.logf(LevelFatal, format, args); ok {
		s.l.runFatalHooks(r)
	}
	_ = s.l.Sync()
	s.l.exit(1)
}

func (s *SugaredLogger) Debugw(msg string, keysAndValues ...any) {
	s.l.logw(LevelDebug, msg, keysAndValues)
}
func (s *SugaredLogger) Infow(msg string, keysAndValues ...any) {
	s.l.logw(LevelInfo, msg, keysAndValues)
}
func (s *SugaredLogger) Warnw(msg string, keysAndValues ...any) {
	s.l.logw(LevelWarn, msg, keysAndValues)
}
func (s *SugaredLogger) Errorw(msg string, keysAndValues ...any) {
	s.l.logw(LevelError, msg, keysAndValues)
}
func (s *SugaredLogger) DPanicw(msg string, keysAndValues ...any) {
	if s.l.development {
		if r, ok := s.l.logw(LevelDPanic, msg, keysAndValues); ok {
			s.l.runFatalHooks(r)
		}
		_ = s.l.Sync()
		panic(msg)
	}
	s.l.logw(LevelDPanic, msg, keysAndValues)
}

func (s *SugaredLogger) Panicw(msg string, keysAndValues ...any) {
	s.l.logw(LevelPanic, msg, keysAndValues)
	_ = s.l.Sync()
	panic(msg)
}

func (s *SugaredLogger) Fatalw(msg string, keysAndValues ...any) {
	if r, ok := s.l.logw(LevelFatal, msg, keysAndValues); ok {
		s.l.runFatalHooks(r)
	}
	_ = s.l.Sync()
	s.l.exit(1)
}

func (s *SugaredLogger) Sync() error { return s.l.Sync() }

// logArgs 在级别检查通过后才拼接消息。调用方是 Sugar 方法，skip 为 5。
func (l *Logger) logArgs(lvl slog.Level, args []any) (slog.Record, bool) {
	if !l.handler.Enabled(l.ctx, lvl) {
		return slog.Record{}, false
	}
	return l.emit(l.ctx, lvl, logSprint(args), nil, 5)
}

// logf：先短路再格式化，禁用级别下无 Sprintf 成本；返回写出的记录（供 Hook 使用）。
func (l *Logger) logf(lvl slog.Level, format string, args []any) (slog.Record, bool) {
	if !l.handler.Enabled(l.ctx, lvl) {
		return slog.Record{}, false
	}
	// 已过级别闸门，直接 emit（skip 5：调用方在 f/w 变体方法内），避免重复消耗采样计数
	return l.emit(l.ctx, lvl, logSprintf(format, args...), nil, 5)
}

// logw 将结构化键值对转换为字段（落单值记 !BADKEY）。
func (l *Logger) logw(lvl slog.Level, msg string, kv []any) (slog.Record, bool) {
	if !l.handler.Enabled(l.ctx, lvl) {
		return slog.Record{}, false
	}
	return l.emit(l.ctx, lvl, msg, argsToAttrs(kv), 5)
}

// argsToAttrs 遵循 slog 的变参规则：
// 参数中的 Field 原样保留，字符串 key 与下一项配对；无效 key 和落单值标记 !BADKEY。
func argsToAttrs(args []any) []Field {
	if len(args) == 0 {
		return nil
	}
	fields := make([]Field, 0, len(args)/2+1)
	for i := 0; i < len(args); {
		if a, ok := args[i].(Field); ok {
			fields = append(fields, a)
			i++
			continue
		}
		// 字符串 key 直传，避免格式化与装箱；无效 key 只消耗自身。
		if k, ok := args[i].(string); ok && i+1 < len(args) {
			fields = append(fields, slog.Any(k, args[i+1]))
			i += 2
			continue
		}
		fields = append(fields, slog.Any("!BADKEY", args[i]))
		i++
	}
	return fields
}

package log

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
)

// 全局态经 atomic.Pointer 读写：日志热路径零锁（对齐 zap.L() 的原子读取）。
// 未显式 Init 时惰性构建（stdout + json + info）。
var (
	global       atomic.Pointer[globalState]
	globalCloser = &closerRegistry{}
)

// globalState 把默认 Logger 与其缓存的 Sugar 绑定在同一原子指针上，
// 换默认时整体替换，S() 拿到的 Sugar 永远对应当前 Logger。
type globalState struct {
	logger *Logger
	sugar  *SugaredLogger
}

// closerRegistry 汇总所有通过 New 创建的可关闭资源，供全局 Close 使用。
type closerRegistry struct {
	mu      sync.Mutex
	closers []io.Closer
	closed  bool
}

func registerClosers(cs []io.Closer) {
	globalCloser.mu.Lock()
	defer globalCloser.mu.Unlock()
	if globalCloser.closed {
		// 已统一关闭后又新建：立即关闭新资源，避免泄漏句柄
		for _, c := range slices.Backward(cs) {
			_ = c.Close()
		}
		return
	}
	globalCloser.closers = append(globalCloser.closers, cs...)
}

// unregisterClosers 释放全局表对已关闭资源的引用；本库资源均为指针，可按身份比较。
// 派生 Logger 共享只读 closers，重复注销及与全局 Close 并发均安全。
func unregisterClosers(cs []io.Closer) {
	if len(cs) == 0 {
		return
	}
	globalCloser.mu.Lock()
	defer globalCloser.mu.Unlock()
	globalCloser.closers = slices.DeleteFunc(globalCloser.closers, func(c io.Closer) bool {
		return slices.Contains(cs, c)
	})
}

// Init 按配置初始化全局 Logger（可重复调用，后者覆盖前者；
// 覆盖时会冲刷并关闭上一个 Logger 持有的资源，避免泄漏后台队列与文件句柄）。
// 新实例构建失败时保留旧实例；旧实例关闭失败时返回该错误，但新实例已经安装。
func Init(cfg Config) error {
	l, err := New(cfg)
	if err != nil {
		// New 失败路径已自行 LIFO 关闭部分资源并返回 nil，此处无可关闭；
		// 成功路径的资源已移交全局表，由后续 Init/Close 负责关闭，不得在此 defer。
		return err
	}
	if old := swapDefault(l); old != nil {
		return old.Close()
	}
	return nil
}

// swapDefault 原子替换默认 Logger 并返回旧实例。
func swapDefault(l *Logger) (old *Logger) {
	if g := global.Swap(&globalState{logger: l}); g != nil {
		return g.logger
	}
	return nil
}

// MustInit 是 Init 的 panic 版本，适合在 main 中初始化。
func MustInit(cfg Config) *Logger {
	if err := Init(cfg); err != nil {
		panic(err)
	}
	return Default()
}

// SetDefault 替换全局 Logger；传 nil 则恢复惰性默认。
// 与 Init 不同，SetDefault 不关闭旧 Logger，资源由调用方自行管理。
func SetDefault(l *Logger) {
	if l == nil {
		global.Store(nil)
		return
	}
	global.Store(&globalState{logger: l})
}

// Default 返回全局 Logger（对应 zap.L()）。
// 未显式 Init 时惰性构建 stdout + json + info（无外部资源，并发双建无害）。
func Default() *Logger {
	for {
		if g := global.Load(); g != nil {
			return g.logger
		}
		// 惰性兜底：stdout + json + info（默认配置不依赖外部资源，不会失败）
		l, err := New(Config{})
		if err != nil {
			l = newLogger(slog.DiscardHandler, nil, nil, false, false)
		}
		if global.CompareAndSwap(nil, &globalState{logger: l}) {
			return l
		}
		// 并发惰性构建：以先到者为准，本次构建直接丢弃
	}
}

// L 等价 Default()，对应 zap.L()。
func L() *Logger { return Default() }

// S 返回全局 Sugar（对应 zap.S()）。Sugar 与默认 Logger 绑定缓存，换默认后重建。
func S() *SugaredLogger {
	for {
		g := global.Load()
		if g == nil {
			_ = Default() // 触发惰性构建后重试
			continue
		}
		if g.sugar != nil {
			return g.sugar
		}
		ng := &globalState{logger: g.logger, sugar: g.logger.Sugar()}
		if global.CompareAndSwap(g, ng) {
			return ng.sugar
		}
		// 并发更新：重读。若期间默认被替换，新 state 的 sugar 为 nil，会重新构建
	}
}

// Ctx 返回绑定了请求上下文的全局 Logger，用于链路关联。
func Ctx(ctx context.Context) *Logger { return Default().WithContext(ctx) }

// With 从全局 Logger 派生携带公共字段的子 Logger。
func With(fields ...Field) *Logger { return Default().With(fields...) }

// ---------- 包级别日志函数 ----------
// 变参语义与 slog 一致：交替 key/value，或直接传 Field。

func Debug(msg string, args ...any) { Default().logAny(LevelDebug, msg, args) }
func Info(msg string, args ...any)  { Default().logAny(LevelInfo, msg, args) }
func Warn(msg string, args ...any)  { Default().logAny(LevelWarn, msg, args) }
func Error(msg string, args ...any) { Default().logAny(LevelError, msg, args) }

func DPanic(msg string, args ...any) {
	d := Default()
	if d.handler.Enabled(d.ctx, LevelDPanic) {
		r, _ := d.emit(d.ctx, LevelDPanic, msg, argsToAttrs(args), 4)
		if d.development {
			d.runFatalHooks(r)
		}
	}
	if d.development {
		_ = d.Sync()
		panic(msg)
	}
}

func Panic(msg string, args ...any) {
	d := Default()
	if d.handler.Enabled(d.ctx, LevelPanic) {
		d.emit(d.ctx, LevelPanic, msg, argsToAttrs(args), 4)
	}
	_ = d.Sync()
	panic(msg)
}

func Fatal(msg string, args ...any) {
	d := Default()
	if d.handler.Enabled(d.ctx, LevelFatal) {
		r, _ := d.emit(d.ctx, LevelFatal, msg, argsToAttrs(args), 4)
		d.runFatalHooks(r)
	}
	_ = d.Sync()
	d.exit(1)
}

// f 变体（对应 zap Sugared 风格）
func Debugf(format string, args ...any) { Default().logf(LevelDebug, format, args) }
func Infof(format string, args ...any)  { Default().logf(LevelInfo, format, args) }
func Warnf(format string, args ...any)  { Default().logf(LevelWarn, format, args) }
func Errorf(format string, args ...any) { Default().logf(LevelError, format, args) }

func DPanicf(format string, args ...any) {
	d := Default()
	if d.development {
		if r, ok := d.logf(LevelDPanic, format, args); ok {
			d.runFatalHooks(r)
		}
		_ = d.Sync()
		panic(logSprintf(format, args...))
	}
	d.logf(LevelDPanic, format, args)
}

func Panicf(format string, args ...any) {
	d := Default()
	d.logf(LevelPanic, format, args)
	_ = d.Sync()
	panic(logSprintf(format, args...))
}

func Fatalf(format string, args ...any) {
	d := Default()
	if r, ok := d.logf(LevelFatal, format, args); ok {
		d.runFatalHooks(r)
	}
	_ = d.Sync()
	d.exit(1)
}

// w 变体（对应 zap Sugared 风格）
func Debugw(msg string, keysAndValues ...any) { Default().logw(LevelDebug, msg, keysAndValues) }
func Infow(msg string, keysAndValues ...any)  { Default().logw(LevelInfo, msg, keysAndValues) }
func Warnw(msg string, keysAndValues ...any)  { Default().logw(LevelWarn, msg, keysAndValues) }
func Errorw(msg string, keysAndValues ...any) { Default().logw(LevelError, msg, keysAndValues) }

func DPanicw(msg string, keysAndValues ...any) {
	d := Default()
	if d.development {
		if r, ok := d.logw(LevelDPanic, msg, keysAndValues); ok {
			d.runFatalHooks(r)
		}
		_ = d.Sync()
		panic(msg)
	}
	d.logw(LevelDPanic, msg, keysAndValues)
}

func Panicw(msg string, keysAndValues ...any) {
	d := Default()
	d.logw(LevelPanic, msg, keysAndValues)
	_ = d.Sync()
	panic(msg)
}

func Fatalw(msg string, keysAndValues ...any) {
	d := Default()
	if r, ok := d.logw(LevelFatal, msg, keysAndValues); ok {
		d.runFatalHooks(r)
	}
	_ = d.Sync()
	d.exit(1)
}

// logAny 处理 slog 变参语义：Field 直接透传，否则按 key/value 配对。
// skip 5：调用链为 用户 → 包级函数 → logAny → emit → newRecord。
func (l *Logger) logAny(lvl slog.Level, msg string, args []any) {
	if !l.handler.Enabled(l.ctx, lvl) {
		return
	}
	l.emit(l.ctx, lvl, msg, argsToAttrs(args), 5)
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

// SetLevel 动态调整全局级别。
func SetLevel(lvl slog.Level) { Default().SetLevel(lvl) }

// Sync 冲刷当前默认 Logger 的全部输出分支。
func Sync() error { return Default().Sync() }

// Close 冲刷并关闭所有由 New 创建的资源（异步队列、滚动文件等），进程退出前调用。
// 按 LIFO 逆序关闭：asyncWriter 先于其下游文件句柄关闭，保证排空内容落到仍打开的文件。
func Close() error {
	_ = Sync()
	globalCloser.mu.Lock()
	defer globalCloser.mu.Unlock()
	if globalCloser.closed {
		return nil
	}
	globalCloser.closed = true
	var errs []error
	for _, c := range slices.Backward(globalCloser.closers) {
		if err := c.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	globalCloser.closers = nil
	return errors.Join(errs...)
}

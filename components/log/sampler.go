package log

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"
)

// SamplerConfig 对标 zapcore.Sampler：
//
//	每个 Tick 周期内，每个级别前 First 条全部记录，
//	之后每 Thereafter 条记录一条（0 表示之后全部丢弃）。
//
// DPanic/Panic/Fatal 级别永不采样。
type SamplerConfig struct {
	First      uint64
	Thereafter uint64
	Tick       time.Duration // 计数重置周期，默认 1s
}

type samplerState struct {
	// 按 debug/info/warn/error 四个桶计数：idx = (level+4)/4
	// debug(-4)→0, info(0)→1, warn(4)→2, error(8)→3；DPanic(12)→4 越界不采样
	epochs  [4]atomic.Int64
	counts  [4]atomic.Uint64
	perTick int64
}

// samplerHandler 在 Enabled 阶段做采样决策：
// 被采样的日志在 Record 构建之前就被丢弃，成本最低。
type samplerHandler struct {
	next       slog.Handler
	first      uint64
	thereafter uint64
	st         *samplerState
}

// newSamplerHandler 包装下游 handler；First 与 Thereafter 均为 0 时视为不采样。
func newSamplerHandler(next slog.Handler, cfg SamplerConfig) slog.Handler {
	if cfg.First == 0 && cfg.Thereafter == 0 {
		return next
	}
	if cfg.Tick <= 0 {
		cfg.Tick = time.Second
	}
	return &samplerHandler{
		next:       next,
		first:      cfg.First,
		thereafter: cfg.Thereafter,
		st:         &samplerState{perTick: int64(cfg.Tick)},
	}
}

func (s *samplerHandler) Enabled(ctx context.Context, l slog.Level) bool {
	if !s.next.Enabled(ctx, l) {
		return false
	}
	return s.allow(l, time.Now())
}

// 注意：采样只在 Enabled 生效。slog.Logger 会先 Enabled 再 Handle，
// 因此正常链路下每条日志只消耗一次采样计数；
// 绕过 Enabled 直接调用 Handle 的日志不参与采样。
func (s *samplerHandler) Handle(ctx context.Context, r slog.Record) error {
	return s.next.Handle(ctx, r)
}

func (s *samplerHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &samplerHandler{next: s.next.WithAttrs(attrs), first: s.first, thereafter: s.thereafter, st: s.st}
}

func (s *samplerHandler) WithGroup(name string) slog.Handler {
	return &samplerHandler{next: s.next.WithGroup(name), first: s.first, thereafter: s.thereafter, st: s.st}
}

func (s *samplerHandler) withName(name string) slog.Handler {
	return &samplerHandler{next: setHandlerName(s.next, name), first: s.first, thereafter: s.thereafter, st: s.st}
}

// allow 基于 tick 边界懒重置的原子计数做限流决策，全程无锁。
func (s *samplerHandler) allow(l slog.Level, now time.Time) bool {
	idx := max((int(l)+4)/4, 0)
	if idx >= len(s.st.epochs) {
		return true // DPanic 及以上不采样
	}

	cur := now.UnixNano() / s.st.perTick
	for {
		last := s.st.epochs[idx].Load()
		if last == cur {
			break
		}
		// 跨入新 tick：抢先者负责清零计数（先清零再提交 epoch，
		// 极端并发下可能出现极小的计数误差，对采样语义无影响）
		if s.st.epochs[idx].CompareAndSwap(last, cur) {
			s.st.counts[idx].Store(0)
			break
		}
	}

	n := s.st.counts[idx].Add(1)
	switch {
	case n <= s.first:
		return true
	case s.thereafter == 0:
		return false
	default:
		return (n-s.first)%s.thereafter == 0
	}
}

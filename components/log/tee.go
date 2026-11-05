package log

import (
	"context"
	"errors"
	"log/slog"
	"slices"
)

// Tee 将多个 slog.Handler 组合为一个：一条日志写给所有通过级别过滤的分支。
// 可混合任意实现（本库 handler、标准库 slog handler 或第三方）。
// 单个 handler 原样返回，空列表返回 slog.DiscardHandler。
func Tee(handlers ...slog.Handler) slog.Handler {
	switch len(handlers) {
	case 0:
		return slog.DiscardHandler
	case 1:
		return handlers[0]
	default:
		return &teeHandler{handlers: slices.Clone(handlers)}
	}
}

// teeHandler 是 Tee 的载体。Handle 只分发给 Enabled 为真的分支：
// 本库编码器不在 Handle 内复查级别，依赖调用方先过滤。
type teeHandler struct {
	handlers []slog.Handler
}

func (t *teeHandler) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range t.handlers {
		if enabledForTee(h, ctx, l) {
			return true
		}
	}
	return false
}

// enabledForTee 只探测分支的级别，不消耗本库采样器的额度。
// 真正分发时再调用分支 Enabled，每个采样器对一次分发只决策一次；
// 探测不缓存状态，因此并发调用与嵌套 Tee 不会串用其他记录的决策。
func enabledForTee(h slog.Handler, ctx context.Context, l slog.Level) bool {
	switch h := h.(type) {
	case *samplerHandler:
		return enabledForTee(h.next, ctx, l)
	case *hookHandler:
		return enabledForTee(h.next, ctx, l)
	case *levelFilterHandler:
		return l >= h.min && enabledForTee(h.next, ctx, l)
	default:
		return h.Enabled(ctx, l)
	}
}

func (t *teeHandler) withName(name string) slog.Handler {
	hs := make([]slog.Handler, len(t.handlers))
	for i, h := range t.handlers {
		hs[i] = setHandlerName(h, name)
	}
	return &teeHandler{handlers: hs}
}

func (t *teeHandler) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, h := range t.handlers {
		if h.Enabled(ctx, r.Level) {
			if err := h.Handle(ctx, r); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func (t *teeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	hs := make([]slog.Handler, len(t.handlers))
	for i, h := range t.handlers {
		// WithAttrs 可取得切片所有权；只有最后一个分支可直接接收原切片。
		branchAttrs := attrs
		if i < len(t.handlers)-1 {
			branchAttrs = slices.Clone(attrs)
		}
		hs[i] = h.WithAttrs(branchAttrs)
	}
	return &teeHandler{handlers: hs}
}

func (t *teeHandler) WithGroup(name string) slog.Handler {
	hs := make([]slog.Handler, len(t.handlers))
	for i, h := range t.handlers {
		hs[i] = h.WithGroup(name)
	}
	return &teeHandler{handlers: hs}
}

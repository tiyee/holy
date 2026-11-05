package log

import (
	"context"
	"log/slog"
)

// Hook 在一条日志通过级别过滤并写出后触发（zap Hooks 选项的等价物）。
// r 为已写出的记录（含堆栈等附加字段）；ctx 为该 Logger 绑定的请求上下文。
type Hook func(ctx context.Context, r slog.Record)

// hookHandler 把 hooks 附加到链路上，Enabled 透传采样决策，被丢弃的日志不触发钩子。
type hookHandler struct {
	next  slog.Handler
	hooks []Hook
}

func (h *hookHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.next.Enabled(ctx, l)
}

func (h *hookHandler) Handle(ctx context.Context, r slog.Record) error {
	err := h.next.Handle(ctx, r)
	for _, hook := range h.hooks {
		hook(ctx, r)
	}
	return err
}

func (h *hookHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &hookHandler{next: h.next.WithAttrs(attrs), hooks: h.hooks}
}

func (h *hookHandler) WithGroup(name string) slog.Handler {
	return &hookHandler{next: h.next.WithGroup(name), hooks: h.hooks}
}

func (h *hookHandler) withName(name string) slog.Handler {
	return &hookHandler{next: setHandlerName(h.next, name), hooks: h.hooks}
}

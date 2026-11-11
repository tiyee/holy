package log

import "context"

// TraceInfo 是从请求上下文中提取出的链路标识。
type TraceInfo struct {
	TraceID string
	SpanID  string
	Sampled bool
}

// TraceExtractor 从 Context 中提取链路标识，由调用方通过 Config 配置。
// 同一 Logger 并发记录日志时，提取函数也可能被并发调用。
type TraceExtractor = func(ctx context.Context) (TraceInfo, bool)

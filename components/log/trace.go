package log

import (
	"context"
	"sync/atomic"
)

// TraceInfo 是从请求上下文中提取出的链路标识。
type TraceInfo struct {
	TraceID string
	SpanID  string
	Sampled bool
}

// TraceExtractor 负责从 context 中提取链路标识。
// 本库不绑定任何链路追踪实现（如 OpenTelemetry），
// 业务侧按需注入即可，例如接入 OTel：
//
//	log.SetTraceExtractor(func(ctx context.Context) (log.TraceInfo, bool) {
//		sc := oteltrace.SpanContextFromContext(ctx)
//		if !sc.IsValid() {
//			return log.TraceInfo{}, false
//		}
//		return log.TraceInfo{
//			TraceID: sc.TraceID().String(),
//			SpanID:  sc.SpanID().String(),
//			Sampled: sc.IsSampled(),
//		}, true
//	})
type TraceExtractor = func(ctx context.Context) (TraceInfo, bool)

var globalTraceExtractor atomic.Pointer[TraceExtractor]

// SetTraceExtractor 注册全局链路提取函数；传 nil 表示关闭。
func SetTraceExtractor(fn TraceExtractor) {
	if fn == nil {
		globalTraceExtractor.Store(nil)
		return
	}
	globalTraceExtractor.Store(&fn)
}

// TraceExtractor 返回当前已注册的提取函数（可能为 nil）。
func CurrentTraceExtractor() TraceExtractor {
	if p := globalTraceExtractor.Load(); p != nil {
		return *p
	}
	return nil
}

// extractTrace 在输出日志行时调用；无注册或未命中时零开销。
func extractTrace(ctx context.Context) (TraceInfo, bool) {
	fn := CurrentTraceExtractor()
	if fn == nil || ctx == nil {
		return TraceInfo{}, false
	}
	return fn(ctx)
}

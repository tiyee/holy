package engine

import (
	"net/http"
	"slices"
)

// FallbackFunc 根据未命中的原始请求选择处理器；返回 nil 表示继续尝试。
// 选择器须支持并发调用，并将响应写入和其他业务操作留给返回的处理器。
type FallbackFunc func(*http.Request) HandlerFunc

// Fallbacks 为路由器提供按注册顺序选择处理器的实现。零值可用。
// 注册在启动阶段完成，之后可并发查询。自定义 IRouter 可以嵌入此类型。
type Fallbacks struct {
	resolvers []FallbackFunc
}

// AddFallback 追加一组选择器，参数中出现 nil 时整组注册失败。
func (f *Fallbacks) AddFallback(resolvers ...FallbackFunc) {
	for _, resolve := range resolvers {
		if resolve == nil {
			panic("holy: nil fallback resolver")
		}
	}
	f.resolvers = append(f.resolvers, resolvers...)
}

// Fallback 返回第一个接管请求的处理器；全部跳过时返回 nil。
func (f *Fallbacks) Fallback(r *http.Request) HandlerFunc {
	for _, resolve := range f.resolvers {
		if handler := resolve(r); handler != nil {
			return handler
		}
	}
	return nil
}

// WithFallback 在创建 Engine 时追加选择器，可多次使用。
func WithFallback(resolvers ...FallbackFunc) IEnginOpt {
	resolvers = slices.Clone(resolvers)
	return func(e *Engine) { e.Fallback(resolvers...) }
}

// Fallback 在引擎冻结前追加选择器，按调用顺序安装到最终选定的路由器。
func (e *Engine) Fallback(resolvers ...FallbackFunc) {
	e.checkMutable()
	e.pendingFallbacks.AddFallback(resolvers...)
}

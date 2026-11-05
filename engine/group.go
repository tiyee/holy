package engine

import (
	"net/http"
	"slices"
)

// RouterGroup 将路径前缀和中间件传给后续注册的路由和子分组。
// 分组由 Engine 注册层处理，与底层 IRouter 实现无关。
type RouterGroup struct {
	engine   *Engine
	parent   *RouterGroup
	prefix   string
	handlers []HandlerFunc
}

// Group 创建路由分组，可用于默认路由器或 WithRouter 注入的任意实现。
// 注册时向底层路由器传入完整路径；参数和通配符的匹配能力由路由器决定。
func (e *Engine) Group(prefix string, handlers ...HandlerFunc) *RouterGroup {
	return e.newGroup(nil, joinPaths("", prefix), handlers)
}

// Group 创建子分组，并复制父组当前的中间件。父组 Hook 在冻结时统一匹配。
func (g *RouterGroup) Group(prefix string, handlers ...HandlerFunc) *RouterGroup {
	return g.engine.newGroup(g, joinPaths(g.prefix, prefix), handlers)
}

func (e *Engine) newGroup(parent *RouterGroup, path string, handlers []HandlerFunc) *RouterGroup {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.checkMutable()
	validateHandlers(handlers)
	validatePath(path)
	return &RouterGroup{engine: e, parent: parent, prefix: path, handlers: parent.withMiddleware(handlers)}
}

// withMiddleware 在注册锁内复制处理链，nil 分组表示直接在引擎上注册。
func (g *RouterGroup) withMiddleware(handlers []HandlerFunc) []HandlerFunc {
	if g == nil {
		return slices.Clone(handlers)
	}
	return slices.Concat(g.handlers, handlers)
}

func (g *RouterGroup) contains(group *RouterGroup) bool {
	for ; group != nil; group = group.parent {
		if group == g {
			return true
		}
	}
	return false
}

func joinPaths(prefix, path string) string {
	if path == "" {
		if prefix == "" {
			return "/"
		}
		return prefix
	}
	if path[0] != '/' {
		panic("holy: group path must start with /: " + path)
	}
	if prefix == "/" {
		prefix = ""
	}
	if len(prefix) > 0 && prefix[len(prefix)-1] == '/' {
		prefix = prefix[:len(prefix)-1]
	}
	return prefix + path
}

// Middleware 为此后注册的路由和创建的子分组添加中间件。
func (g *RouterGroup) Middleware(handlers ...HandlerFunc) {
	g.engine.mu.Lock()
	defer g.engine.mu.Unlock()
	g.engine.checkMutable()
	validateHandlers(handlers)
	g.handlers = append(g.handlers, handlers...)
}

// Use 为本组及其子组注册 Hook。匹配器接收 HTTP 方法与完整路由模板。
func (g *RouterGroup) Use(pos HookPos, matcher IMatcher, fn HandlerFunc) {
	g.engine.addHook(g, pos, matcher, fn)
}

func (g *RouterGroup) Handle(method, path string, handlers ...HandlerFunc) {
	g.engine.setRoute(g, method, joinPaths(g.prefix, path), handlers...)
}
func (g *RouterGroup) GET(path string, handlers ...HandlerFunc) {
	g.Handle(http.MethodGet, path, handlers...)
}
func (g *RouterGroup) POST(path string, handlers ...HandlerFunc) {
	g.Handle(http.MethodPost, path, handlers...)
}
func (g *RouterGroup) PUT(path string, handlers ...HandlerFunc) {
	g.Handle(http.MethodPut, path, handlers...)
}
func (g *RouterGroup) PATCH(path string, handlers ...HandlerFunc) {
	g.Handle(http.MethodPatch, path, handlers...)
}
func (g *RouterGroup) DELETE(path string, handlers ...HandlerFunc) {
	g.Handle(http.MethodDelete, path, handlers...)
}
func (g *RouterGroup) OPTIONS(path string, handlers ...HandlerFunc) {
	g.Handle(http.MethodOptions, path, handlers...)
}
func (g *RouterGroup) HEAD(path string, handlers ...HandlerFunc) {
	g.Handle(http.MethodHead, path, handlers...)
}
func (g *RouterGroup) TRACE(path string, handlers ...HandlerFunc) {
	g.Handle(http.MethodTrace, path, handlers...)
}
func (g *RouterGroup) CONNECT(path string, handlers ...HandlerFunc) {
	g.Handle(http.MethodConnect, path, handlers...)
}
func (g *RouterGroup) Union(methods []string, path string, handlers ...HandlerFunc) {
	for _, method := range methods {
		g.Handle(method, path, handlers...)
	}
}
func (g *RouterGroup) Any(path string, handlers ...HandlerFunc) {
	g.Union([]string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions, http.MethodHead, http.MethodTrace, http.MethodConnect}, path, handlers...)
}
func (g *RouterGroup) Rest(path string, rest any)    { registerRest(g, path, rest) }
func (g *RouterGroup) SetRest(path string, rest any) { g.Rest(path, rest) }

var _ IRoutes = (*Engine)(nil)
var _ IRoutes = (*RouterGroup)(nil)

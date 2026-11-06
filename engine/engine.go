// Package engine 提供 HTTP 引擎；路由存储与匹配由 IRouter 接口定义，
// 默认实现为按 HTTP method 划分的压缩前缀树。
package engine

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tiyee/holy/components/log"
)

// Engine 在首次处理请求时冻结配置，之后可供并发请求共享。
type Engine struct {
	router                     IRouter
	addr                       string
	pool                       sync.Pool
	mu                         sync.Mutex
	once                       sync.Once
	initPanic                  any
	frozen                     bool
	methods                    []string
	hooks                      []Hook
	pendingFallbacks           Fallbacks
	fallbackChain              []HandlerFunc
	guards                     []HandlerFunc
	noRoute, noMethod          []HandlerFunc
	notFound, methodNotAllowed []HandlerFunc
	// 以下选项只能在首次 ServeHTTP 或 Run 前设置。
	// RedirectTrailingSlash 默认开启，同一方法下增删一个尾斜杠即可匹配时跳转。
	RedirectTrailingSlash bool
	// RemoveExtraSlash 默认关闭。开启时，匹配路径中的连续斜杠视为一个斜杠。
	RemoveExtraSlash       bool
	HandleMethodNotAllowed bool
	// MaxBodyBytes 默认 1 MiB；小于等于零时不限制，所有请求体读取均受限。
	MaxBodyBytes              int64
	DisallowUnknownJSONFields bool
	JSONBinder                JSONBinder
	JSONRenderer              JSONRenderer
	EnvelopeBuilder           EnvelopeBuilder
}

type IEnginOpt func(e *Engine)

func New(opts ...IEnginOpt) *Engine {
	e := &Engine{router: newRadixRouter(), addr: ":3003", RedirectTrailingSlash: true, MaxBodyBytes: DefaultMaxBodyBytes}
	e.pool.New = func() any { return e.allocateContext() }
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// WithRouter 替换路由存储与匹配实现，须在首次请求前设置。
func WithRouter(router IRouter) IEnginOpt {
	return func(e *Engine) {
		if router == nil {
			panic("holy: nil router")
		}
		e.router = router
	}
}

// WithSimpleRouter 为每个 Engine 创建独立的有序 slice 路由表。
func WithSimpleRouter() IEnginOpt {
	return func(e *Engine) {
		e.router = NewSimpleRouter()
	}
}

// prepare 将初始化失败保留下来，后续请求不会使用半成品路由表。
func (e *Engine) prepare() {
	e.once.Do(func() {
		defer func() { e.initPanic = recover() }()
		e.dispatch()
	})
	if e.initPanic != nil {
		panic(e.initPanic)
	}
}

func (e *Engine) checkMutable() {
	if e.frozen {
		panic("holy: configuration is frozen after the first request")
	}
}

func (e *Engine) SetAddr(addr string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.checkMutable()
	e.addr = addr
}

func (e *Engine) allocateContext() *Context { return &Context{index: -1} }

func (e *Engine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	e.prepare()
	c := e.pool.Get().(*Context)
	if e.MaxBodyBytes > 0 && r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, e.MaxBodyBytes)
	}
	c.reset(w, r)
	c.maxBodyBytes = e.MaxBodyBytes
	c.strictJSON = e.DisallowUnknownJSONFields
	if e.JSONBinder != nil {
		c.jsonBinder = e.JSONBinder
	}
	if e.JSONRenderer != nil {
		c.jsonRenderer = e.JSONRenderer
	}
	if e.EnvelopeBuilder != nil {
		c.envelope = e.EnvelopeBuilder
	}
	defer func() { c.release(); e.pool.Put(c) }()
	defer func() {
		if err := recover(); err != nil {
			if err == http.ErrAbortHandler {
				panic(err)
			}
			log.Error("request panic", log.Any("panic", err), log.String("method", r.Method), log.String("path", r.URL.Path))
			c.Abort()
			if !c.writer.Written() {
				c.Error("internal error", http.StatusInternalServerError)
			}
		}
	}()
	matchPath := r.URL.Path
	if e.RemoveExtraSlash {
		matchPath = collapseSlashes(matchPath)
	}
	if route := e.router.Lookup(r.Method, matchPath, &c.Params); route != nil {
		c.fullPath, c.handlers = route.Path, route.chain
		c.Next()
		return
	}
	if handler := e.router.Fallback(r); handler != nil {
		c.fallbackHandler, c.handlers = handler, e.fallbackChain
		c.Next()
		return
	}
	if e.RedirectTrailingSlash && matchPath != "/" {
		path := strings.TrimSuffix(matchPath, "/")
		if path == matchPath {
			path += "/"
		}
		if e.hasRoute(r.Method, path, &c.Params) {
			// 只使用匹配到的路径和原查询串，避免请求中的 Opaque 覆盖站内目标。
			u := url.URL{Path: path, RawQuery: r.URL.RawQuery, ForceQuery: r.URL.ForceQuery}
			code := http.StatusMovedPermanently
			if r.Method != http.MethodGet {
				code = http.StatusTemporaryRedirect
			}
			target := u.RequestURI()
			// 对第二个斜杠编码，确保重定向始终留在当前站点。
			if strings.HasPrefix(target, "//") {
				target = "/%2F" + target[2:]
			}
			redirectExactPath(c.Response(), r, target, code)
			return
		}
	}
	c.Params = c.Params[:0]
	if e.HandleMethodNotAllowed {
		// 九种标准 method 使用栈上缓冲；更多自定义 method 按需扩容。
		var buffer [9]string
		allowed := buffer[:0]
		for _, method := range e.methods {
			if method != r.Method && e.hasRoute(method, matchPath, &c.Params) {
				allowed = append(allowed, method)
			}
		}
		if len(allowed) != 0 {
			c.Response().Header().Set("Allow", strings.Join(allowed, ", "))
			c.handlers = e.methodNotAllowed
			c.Next()
			return
		}
	}
	c.handlers = e.notFound
	c.Next()
}

// hasRoute 只对默认实现省略参数提取，自定义 IRouter 仍接收非 nil 参数切片。
func (e *Engine) hasRoute(method, path string, params *Params) bool {
	if router, ok := e.router.(*radixRouter); ok {
		return router.Lookup(method, path, nil) != nil
	}
	route := e.router.Lookup(method, path, params)
	*params = (*params)[:0]
	return route != nil
}

func validateHandlers(handlers []HandlerFunc) {
	for _, fn := range handlers {
		if fn == nil {
			panic("holy: nil handler")
		}
	}
}

func (e *Engine) setRoute(group *RouterGroup, method, path string, handlers ...HandlerFunc) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.checkMutable()
	if !validMethod(method) {
		panic("holy: invalid HTTP method " + method)
	}
	validatePath(path)
	if len(handlers) == 0 {
		panic("holy: route requires at least one handler")
	}
	validateHandlers(handlers)
	route := &Route{Method: method, Path: path, group: group, HandlerFuncs: group.withGuards(handlers)}
	e.router.Insert(method, path, route)
}

func validMethod(method string) bool {
	if method == "" {
		return false
	}
	for i := range len(method) {
		c := method[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", rune(c))) {
			return false
		}
	}
	return true
}

// Guard 为所有路由、Fallback 处理器以及 404/405 处理链注册全局守卫。
func (e *Engine) Guard(handlers ...HandlerFunc) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.checkMutable()
	validateHandlers(handlers)
	e.guards = append(e.guards, handlers...)
}

func (e *Engine) NoRoute(handlers ...HandlerFunc) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.checkMutable()
	validateHandlers(handlers)
	e.noRoute = append([]HandlerFunc(nil), handlers...)
}

func (e *Engine) NoMethod(handlers ...HandlerFunc) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.checkMutable()
	validateHandlers(handlers)
	e.noMethod = append([]HandlerFunc(nil), handlers...)
}

func (e *Engine) Run(ops ...func(*http.Server)) error {
	e.prepare()
	server := &http.Server{Addr: e.addr, Handler: e, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 1 << 16}
	for _, op := range ops {
		op(server)
	}
	return server.ListenAndServe()
}

func (e *Engine) Handle(method, path string, handlers ...HandlerFunc) {
	e.setRoute(nil, method, path, handlers...)
}
func (e *Engine) GET(path string, handlers ...HandlerFunc) {
	e.Handle(http.MethodGet, path, handlers...)
}
func (e *Engine) POST(path string, handlers ...HandlerFunc) {
	e.Handle(http.MethodPost, path, handlers...)
}
func (e *Engine) PUT(path string, handlers ...HandlerFunc) {
	e.Handle(http.MethodPut, path, handlers...)
}
func (e *Engine) PATCH(path string, handlers ...HandlerFunc) {
	e.Handle(http.MethodPatch, path, handlers...)
}
func (e *Engine) DELETE(path string, handlers ...HandlerFunc) {
	e.Handle(http.MethodDelete, path, handlers...)
}
func (e *Engine) OPTIONS(path string, handlers ...HandlerFunc) {
	e.Handle(http.MethodOptions, path, handlers...)
}
func (e *Engine) HEAD(path string, handlers ...HandlerFunc) {
	e.Handle(http.MethodHead, path, handlers...)
}
func (e *Engine) TRACE(path string, handlers ...HandlerFunc) {
	e.Handle(http.MethodTrace, path, handlers...)
}
func (e *Engine) CONNECT(path string, handlers ...HandlerFunc) {
	e.Handle(http.MethodConnect, path, handlers...)
}
func (e *Engine) Union(methods []string, path string, handlers ...HandlerFunc) {
	for _, method := range methods {
		e.Handle(method, path, handlers...)
	}
}
func (e *Engine) Any(path string, handlers ...HandlerFunc) {
	e.Union([]string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions, http.MethodHead, http.MethodTrace, http.MethodConnect}, path, handlers...)
}
func (e *Engine) Rest(path string, rest any)    { registerRest(e, path, rest) }
func (e *Engine) SetRest(path string, rest any) { e.Rest(path, rest) }

func makeChain(method, path string, parts ...[]HandlerFunc) []HandlerFunc {
	var chain []HandlerFunc
	for _, part := range parts {
		chain = append(chain, part...)
	}
	if len(chain) > int(abortIndex) {
		panic(fmt.Sprintf("too many handlers for %s %s: %d (maximum %d)", method, path, len(chain), abortIndex))
	}
	return chain
}

// dispatch 只装配一次处理链；ServeHTTP 和 Run 使用同一个初始化入口。
func (e *Engine) dispatch() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.frozen {
		return
	}
	e.frozen = true
	if len(e.pendingFallbacks.resolvers) > 0 {
		e.router.AddFallback(e.pendingFallbacks.resolvers...)
		e.pendingFallbacks = Fallbacks{}
	}
	// 共享链只保存分发入口，每个请求选中的处理器挂在自己的 Context 上。
	e.fallbackChain = makeChain("", "fallback", e.guards, []HandlerFunc{func(c *Context) { c.fallbackHandler(c) }})
	if e.HandleMethodNotAllowed {
		// Methods 可能返回实现内部切片，复制后再排序，避免修改调用方状态。
		e.methods = append([]string(nil), e.router.Methods()...)
		sort.Strings(e.methods)
	}
	for route := range e.router.Iter() {
		for _, hook := range e.hooks {
			if hook.group != nil && !hook.group.contains(route.group) {
				continue
			}
			if hook.matcher.Match(route.Method, route.Path) {
				if hook.Pos == PosAhead {
					route.BeforeHooks = append(route.BeforeHooks, hook.HandlerFunc)
				} else {
					route.AfterHooks = append(route.AfterHooks, hook.HandlerFunc)
				}
			}
		}
		route.chain = makeChain(route.Method, route.Path, e.guards, route.BeforeHooks, route.HandlerFuncs, route.AfterHooks)
	}
	if len(e.noRoute) == 0 {
		e.noRoute = []HandlerFunc{func(c *Context) { c.NotFound() }}
	}
	if len(e.noMethod) == 0 {
		e.noMethod = []HandlerFunc{func(c *Context) { c.Error("method not allowed", http.StatusMethodNotAllowed) }}
	}
	e.notFound = makeChain("", "404", e.guards, e.noRoute)
	e.methodNotAllowed = makeChain("", "405", e.guards, e.noMethod)
}

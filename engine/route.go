package engine

import (
	"iter"
	"net/http"
	"strings"
)

// HandlerFunc 是路由或中间件处理函数。
type HandlerFunc func(ctx *Context)
type Route struct {
	Method                                string
	Path                                  string
	BeforeHooks, AfterHooks, HandlerFuncs []HandlerFunc
	group                                 *RouterGroup
	chain                                 []HandlerFunc
}

// Param 是按路径顺序排列的路由参数。
type Param struct{ Key, Value string }
type Params []Param

func (ps Params) Get(name string) (string, bool) {
	for _, p := range ps {
		if p.Key == name {
			return p.Value, true
		}
	}
	return "", false
}
func (ps Params) ByName(name string) string { value, _ := ps.Get(name); return value }

// IRouter 负责路由的存储与匹配，实现自行决定内部结构。
// 引擎在配置阶段串行调用 Insert/AddFallback，冻结后支持并发查询，
// 实现无需自行加锁。实现须遵守：
//   - 按传入的 path 原样匹配。引擎默认传入 Request.URL.Path，仅在开启 RemoveExtraSlash 时合并连续斜杠；
//   - 匹配能力由实现决定；支持通配时静态路径优先，失败回退参数分支，末尾 catch-all；
//   - Lookup 未命中时返回 nil，并把 *params 恢复到调用前长度；
//   - AddFallback 按顺序追加选择器，Fallback 返回首个接管请求的处理器；
//     Lookup 与 Methods/Iter 仅反映注册路由，尾斜杠及 405 探测只调用 Lookup；
//   - 相同 (method, path) 重复注册或实现定义的结构冲突时 Insert panic；
//   - Iter 恰好枚举全部已注册路由，每条恰好一次，消费方退出后立即停止，
//     route.Method/route.Path 即注册时传入的 method 与 path。
type IRouter interface {
	Insert(method, path string, route *Route)
	Lookup(method, path string, params *Params) *Route
	AddFallback(resolvers ...FallbackFunc)
	Fallback(r *http.Request) HandlerFunc
	Methods() []string
	Iter() iter.Seq[*Route]
}

// IRoutes 是注册面的路由声明接口，由 Engine 和 RouterGroup 实现。
type IRoutes interface {
	Handle(method, path string, fn ...HandlerFunc)
	GET(path string, fn ...HandlerFunc)
	POST(path string, fn ...HandlerFunc)
	PUT(path string, fn ...HandlerFunc)
	PATCH(path string, fn ...HandlerFunc)
	DELETE(path string, fn ...HandlerFunc)
	OPTIONS(path string, fn ...HandlerFunc)
	HEAD(path string, fn ...HandlerFunc)
	TRACE(path string, fn ...HandlerFunc)
	CONNECT(path string, fn ...HandlerFunc)
	Rest(path string, rest any)
	Union(methods []string, path string, fn ...HandlerFunc)
}
type IHttpGET interface{ GET(*Context) }
type IHttpPOST interface{ POST(*Context) }
type IHttpPUT interface{ PUT(*Context) }
type IHttpPATCH interface{ PATCH(*Context) }
type IHttpDELETE interface{ DELETE(*Context) }
type IHttpOPTIONS interface{ OPTIONS(*Context) }
type IHttpHEAD interface{ HEAD(*Context) }
type IHttpTRACE interface{ TRACE(*Context) }
type IHttpCONNECT interface{ CONNECT(*Context) }

// collapseSlashes 只合并连续斜杠，保留末尾斜杠和点段。
func collapseSlashes(path string) string {
	first := strings.Index(path, "//")
	if first < 0 {
		return path
	}
	var b strings.Builder
	b.Grow(len(path) - 1)
	b.WriteString(path[:first+1])
	for i := first + 2; i < len(path); i++ {
		if path[i] != '/' || path[i-1] != '/' {
			b.WriteByte(path[i])
		}
	}
	return b.String()
}

func validatePath(path string) {
	if path == "" || path[0] != '/' {
		panic("holy: route path must start with /: " + path)
	}
	if strings.ContainsAny(path, "?#\\\r\n\x00") {
		panic("holy: invalid route path: " + path)
	}
	names := make(map[string]bool)
	for offset := 0; offset < len(path); {
		i := strings.IndexAny(path[offset:], ":*")
		if i < 0 {
			return
		}
		i += offset
		end := strings.IndexByte(path[i:], '/')
		if end < 0 {
			end = len(path)
		} else {
			end += i
		}
		name := path[i+1 : end]
		if name == "" || strings.ContainsAny(name, ":*") || names[name] {
			panic("holy: invalid or repeated parameter in " + path)
		}
		names[name] = true
		if path[i] == '*' && (i == 0 || path[i-1] != '/' || end != len(path)) {
			panic("holy: catch-all must follow / and end the path: " + path)
		}
		offset = end
	}
}

func registerRest(r IRoutes, path string, rest any) {
	count := 0
	if h, ok := rest.(IHttpGET); ok {
		r.GET(path, h.GET)
		count++
	}
	if h, ok := rest.(IHttpPOST); ok {
		r.POST(path, h.POST)
		count++
	}
	if h, ok := rest.(IHttpPUT); ok {
		r.PUT(path, h.PUT)
		count++
	}
	if h, ok := rest.(IHttpPATCH); ok {
		r.PATCH(path, h.PATCH)
		count++
	}
	if h, ok := rest.(IHttpDELETE); ok {
		r.DELETE(path, h.DELETE)
		count++
	}
	if h, ok := rest.(IHttpOPTIONS); ok {
		r.OPTIONS(path, h.OPTIONS)
		count++
	}
	if h, ok := rest.(IHttpHEAD); ok {
		r.HEAD(path, h.HEAD)
		count++
	}
	if h, ok := rest.(IHttpTRACE); ok {
		r.TRACE(path, h.TRACE)
		count++
	}
	if h, ok := rest.(IHttpCONNECT); ok {
		r.CONNECT(path, h.CONNECT)
		count++
	}
	if count == 0 {
		panic("holy: empty REST handler for " + path)
	}
}

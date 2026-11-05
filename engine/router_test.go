package engine

import (
	"fmt"
	"iter"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func request(e http.Handler, method, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(method, path, nil))
	return w
}

func TestRadixRouting(t *testing.T) {
	testRouterRouting(t, func() IRouter { return newRadixRouter() })
}

func testRouterRouting(t *testing.T, newRouter func() IRouter) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprint(reverse), func(t *testing.T) {
			e := New(WithRouter(newRouter()))
			paths := []string{"/", "/users/:id", "/users/new", "/users/:id/posts/:post", "/users/new/profile", "/files/*path", "/files/static/end", "/prefix/:first/no", "/prefix/static/:second/end", "/prefix/:first/:second/final", "/a", "/ab", "/abc", "/alphabet", "/版本/:名称", "/item_:id"}
			if reverse {
				for i, j := 0, len(paths)-1; i < j; i, j = i+1, j-1 {
					paths[i], paths[j] = paths[j], paths[i]
				}
			}
			for _, path := range paths {
				e.GET(path, func(c *Context) {
					values := []string{c.FullPath()}
					for _, param := range c.Params {
						values = append(values, param.Key+"="+param.Value)
					}
					c.String(200, strings.Join(values, "|"))
				})
			}
			for _, tc := range []struct{ path, want string }{
				{"/", "/"}, {"/users/new", "/users/new"}, {"/users/42", "/users/:id|id=42"},
				{"/users/new/posts/3", "/users/:id/posts/:post|id=new|post=3"},
				{"/users/new/profile", "/users/new/profile"},
				{"/files/a/b", "/files/*path|path=/a/b"}, {"/files/", "/files/*path|path=/"},
				{"/files/static/end", "/files/static/end"}, {"/files/static/missing", "/files/*path|path=/static/missing"},
				{"/prefix/static/a/final", "/prefix/:first/:second/final|first=static|second=a"},
				{"/prefix/static/a/end", "/prefix/static/:second/end|second=a"},
				{"/a", "/a"}, {"/ab", "/ab"}, {"/abc", "/abc"}, {"/alphabet", "/alphabet"},
				{"/版本/中文", "/版本/:名称|名称=中文"}, {"/item_12", "/item_:id|id=12"},
			} {
				w := request(e, "GET", tc.path)
				if w.Code != 200 || w.Body.String() != tc.want {
					t.Errorf("%s: %d %q; want %q", tc.path, w.Code, w.Body.String(), tc.want)
				}
			}
			for _, path := range []string{"/missing", "/users//posts/3", "/users/42/extra", "/alpha"} {
				if w := request(e, "GET", path); w.Code != 404 {
					t.Errorf("%s: got %d", path, w.Code)
				}
			}
		})
	}
}

func TestRemoveExtraSlash(t *testing.T) {
	for name, backend := range map[string]IEnginOpt{
		"radix": WithRouter(newRadixRouter()), "simple": WithSimpleRouter(), "custom": WithRouter(newExactRouter()),
	} {
		t.Run(name, func(t *testing.T) {
			e := New(backend)
			e.RemoveExtraSlash = true
			e.HandleMethodNotAllowed = true
			ok := func(c *Context) { c.AbortWithStatus(204) }
			e.GET("/", ok)
			e.GET("/api/items", ok)
			e.POST("/api/items/", ok)
			e.GET("/a/./../b", ok)
			e.Fallback(func(r *http.Request) HandlerFunc {
				if r.URL.Path == "/legacy//path" {
					return func(c *Context) { c.AbortWithStatus(410) }
				}
				return nil
			})
			for _, tc := range []struct {
				method, path    string
				code            int
				location, allow string
			}{
				{"GET", "////", 204, "", ""},
				{"GET", "///api////items", 204, "", ""},
				{"POST", "/api//items///", 204, "", ""},
				{"GET", "/a//.///..//b", 204, "", ""},
				{"GET", "/api//items///?x=//", 301, "/api/items?x=//", ""},
				{"POST", "//api///items?q=%2F", 307, "/api/items/?q=%2F", ""},
				{"HEAD", "/api///items", 405, "", "GET"},
				{"GET", "/legacy//path", 410, "", ""},
			} {
				w := request(e, tc.method, tc.path)
				if w.Code != tc.code || w.Header().Get("Location") != tc.location || w.Header().Get("Allow") != tc.allow {
					t.Errorf("%s %s: code=%d headers=%v; want %d location=%q allow=%q", tc.method, tc.path, w.Code, w.Header(), tc.code, tc.location, tc.allow)
				}
			}
		})
	}
}

func TestRemoveExtraSlashParams(t *testing.T) {
	e := New()
	e.RemoveExtraSlash = true
	e.RedirectTrailingSlash = false
	const target = "/teams%2F/42///files//中文//%252F///?q=//"
	e.GET("/teams/:id/files/*path", func(c *Context) {
		if c.Param("id") != "42" || c.Param("path") != "/中文/%2F/" || c.FullPath() != "/teams/:id/files/*path" {
			t.Errorf("matched %q with params %v", c.FullPath(), c.Params)
		}
		if c.Request().URL.Path != "/teams//42///files//中文//%2F///" || c.Request().RequestURI != target {
			t.Errorf("request path changed: %q, URI %q", c.Request().URL.Path, c.Request().RequestURI)
		}
		c.AbortWithStatus(204)
	})
	if w := request(e, "GET", target); w.Code != 204 {
		t.Fatal(w.Code)
	}
	if w := request(e, "GET", "/teams//42//files"); w.Code != 404 || w.Header().Get("Location") != "" {
		t.Fatalf("disabled trailing slash redirect: code=%d headers=%v", w.Code, w.Header())
	}
}

func TestMethodTreesAndRedirects(t *testing.T) {
	testRouterMethodsAndRedirects(t, func() IRouter { return newRadixRouter() })
}

func testRouterMethodsAndRedirects(t *testing.T, newRouter func() IRouter) {
	e := New(WithRouter(newRouter()), func(e *Engine) { e.HandleMethodNotAllowed = true })
	e.GET("/users/:id", func(c *Context) { c.String(200, "GET:"+c.Param("id")) })
	e.POST("/users/:name", func(c *Context) { c.String(201, "POST:"+c.Param("name")) })
	e.OPTIONS("/users/:id", func(c *Context) { c.AbortWithStatus(204) })
	e.POST("/submit/", func(c *Context) { c.AbortWithStatus(204) })
	for _, tc := range []struct {
		method, path          string
		code                  int
		body, location, allow string
	}{
		{"GET", "/users/1", 200, "GET:1", "", ""},
		{"POST", "/users/alice", 201, "POST:alice", "", ""},
		{"OPTIONS", "/users/1", 204, "", "", ""},
		{"OPTIONS", "/submit/", 405, "method not allowed\n", "", "POST"},
		{"DELETE", "/users/1", 405, "method not allowed\n", "", "GET, OPTIONS, POST"},
		{"HEAD", "/users/1", 405, "method not allowed\n", "", "GET, OPTIONS, POST"},
		{"GET", "/users/1/?q=x", 301, "", "/users/1?q=x", ""},
		{"POST", "/submit?q=x", 307, "", "/submit/?q=x", ""},
		{"POST", "/users/a%20b/?q=%2F", 307, "", "/users/a%20b?q=%2F", ""},
		{"DELETE", "/missing", 404, "not found\n", "", ""},
	} {
		w := request(e, tc.method, tc.path)
		if w.Code != tc.code || tc.location == "" && w.Body.String() != tc.body || w.Header().Get("Location") != tc.location || w.Header().Get("Allow") != tc.allow {
			t.Errorf("%s %s: code=%d body=%q location=%q allow=%q", tc.method, tc.path, w.Code, w.Body.String(), w.Header().Get("Location"), w.Header().Get("Allow"))
		}
	}
	e2 := New(WithRouter(newRouter()))
	e2.RedirectTrailingSlash = false
	e2.HandleMethodNotAllowed = true
	e2.GET("/path", func(c *Context) { c.String(200, "ok") })
	e2.POST("/submit/", func(c *Context) { c.AbortWithStatus(204) })
	e2.GET("/submit", func(c *Context) { c.String(200, "ok") })
	if w := request(e2, "GET", "/path/"); w.Code != 404 || w.Header().Get("Location") != "" {
		t.Fatalf("disabled redirect: code=%d headers=%v", w.Code, w.Header())
	}
	if w := request(e2, "POST", "/submit"); w.Code != 405 || w.Header().Get("Allow") != "GET" || w.Header().Get("Location") != "" {
		t.Fatalf("disabled redirect with other method: code=%d headers=%v", w.Code, w.Header())
	}
}

func TestInvalidRoutes(t *testing.T) {
	testRouterInvalidRoutes(t, func() IRouter { return newRadixRouter() })
}

func testRouterInvalidRoutes(t *testing.T, newRouter func() IRouter) {
	fn := func(*Context) {}
	for _, tc := range []struct {
		name  string
		setup func(*Engine)
	}{
		{"relative", func(e *Engine) { e.GET("relative", fn) }},
		{"empty path", func(e *Engine) { e.GET("", fn) }},
		{"empty param", func(e *Engine) { e.GET("/:", fn) }},
		{"empty catchall", func(e *Engine) { e.GET("/*", fn) }},
		{"catchall not terminal", func(e *Engine) { e.GET("/*rest/more", fn) }},
		{"catchall without slash", func(e *Engine) { e.GET("/prefix*rest", fn) }},
		{"two wildcards", func(e *Engine) { e.GET("/:a:b", fn) }},
		{"repeated names", func(e *Engine) { e.GET("/:a/:a", fn) }},
		{"duplicate", func(e *Engine) { e.GET("/x", fn); e.GET("/x", fn) }},
		{"param conflict", func(e *Engine) { e.GET("/:id/a", fn); e.GET("/:name/b", fn) }},
		{"catchall conflict", func(e *Engine) { e.GET("/*a", fn); e.GET("/*b", fn) }},
		{"wildcard conflict", func(e *Engine) { e.GET("/:id", fn); e.GET("/*path", fn) }},
		{"no handler", func(e *Engine) { e.GET("/x") }},
		{"nil handler", func(e *Engine) { e.GET("/x", nil) }},
		{"empty method", func(e *Engine) { e.Handle("", "/x", fn) }},
		{"bad method", func(e *Engine) { e.Handle("GET POST", "/x", fn) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected registration panic")
				}
			}()
			tc.setup(New(WithRouter(newRouter())))
		})
	}
}

func TestHooksGroupsAndAbort(t *testing.T) {
	testRouterHooksGroupsAndAbort(t, func() IRouter { return newRadixRouter() })
}

func testRouterHooksGroupsAndAbort(t *testing.T, newRouter func() IRouter) {
	e := New(WithRouter(newRouter()))
	var order []string
	middleware := func(name string) HandlerFunc {
		return func(c *Context) { order = append(order, name+"<"); c.Next(); order = append(order, name+">") }
	}
	e.Middleware(middleware("global"))
	api := e.Group("/api", middleware("api"))
	api.Middleware(middleware("group"))
	v1 := api.Group("/v1", middleware("v1"))
	v1.GET("/users/:id", func(c *Context) { order = append(order, "handler:"+c.Param("id")); c.String(200, "ok") })
	e.Use(PosAhead, Prefix("/api", nil), middleware("before"))
	e.Use(PosBehind, Identical("/api/v1/users/:id"), func(*Context) { order = append(order, "after") })
	for range 2 {
		order = nil
		w := request(e, "GET", "/api/v1/users/7")
		want := []string{"global<", "before<", "api<", "group<", "v1<", "handler:7", "after", "v1>", "group>", "api>", "before>", "global>"}
		if w.Code != 200 || !reflect.DeepEqual(order, want) {
			t.Fatalf("order=%v want %v", order, want)
		}
	}
	e2 := New(WithRouter(newRouter()))
	e2.Use(PosAhead, Contains("private"), func(c *Context) { c.AbortWithStatus(401); c.Next() })
	e2.GET("/private/:id", func(*Context) { t.Fatal("aborted handler executed") })
	if w := request(e2, "GET", "/private/1"); w.Code != 401 {
		t.Fatal(w.Code)
	}
}

func TestFallbackMiddlewareAndFrozenConfiguration(t *testing.T) {
	e := New(func(e *Engine) { e.HandleMethodNotAllowed = true })
	e.Middleware(func(c *Context) {
		if c.Param("id") != "" || c.FullPath() != "" {
			t.Error("fallback retained route parameters")
		}
		c.Response().Header().Set("X-Middleware", "yes")
		c.Next()
	})
	e.GET("/users/:id", func(*Context) {})
	e.NoRoute(func(c *Context) { c.JSON(404, map[string]string{"error": "missing"}) })
	e.NoMethod(func(c *Context) { c.String(405, "wrong method") })
	for _, tc := range []struct {
		method, path string
		code         int
	}{{"GET", "/users/7/missing", 404}, {"DELETE", "/users/7", 405}} {
		w := request(e, tc.method, tc.path)
		if w.Code != tc.code || w.Header().Get("X-Middleware") != "yes" {
			t.Fatal(w.Code, w.Header())
		}
	}
	defer func() {
		if recover() == nil {
			t.Error("registration after first request should panic")
		}
	}()
	e.GET("/late", func(*Context) {})
}

func TestPanicRecoveryAndJSONFailure(t *testing.T) {
	e := New()
	e.GET("/panic", func(*Context) { panic("broken") })
	e.GET("/written", func(c *Context) { c.String(202, "accepted"); panic("after write") })
	e.GET("/json", func(c *Context) { c.JSON(200, make(chan int)) }, func(*Context) { t.Error("encoding failure did not abort") })
	e.GET("/ok", func(c *Context) { c.JSON(200, map[string]string{"ok": "yes"}) })
	for _, tc := range []struct {
		path string
		code int
		body string
	}{{"/panic", 500, "internal error\n"}, {"/written", 202, "accepted"}, {"/json", 500, "JSON encoding error\n"}, {"/missing", 404, "not found\n"}, {"/ok", 200, `{"ok":"yes"}`}} {
		for range 2 {
			w := request(e, "GET", tc.path)
			if w.Code != tc.code || w.Body.String() != tc.body {
				t.Errorf("%s: %d %q", tc.path, w.Code, w.Body.String())
			}
		}
	}
}

func TestConcurrentFirstRequests(t *testing.T) {
	testRouterConcurrentFirstRequests(t, func() IRouter { return newRadixRouter() })
}

func testRouterConcurrentFirstRequests(t *testing.T, newRouter func() IRouter) {
	e := New(WithRouter(newRouter()))
	e.Use(PosAhead, Prefix("/", nil), func(c *Context) { c.SetUserValue("id", c.Param("id")); c.Next() })
	e.GET("/users/:id", func(c *Context) {
		if c.UserValue("id") != c.Param("id") {
			t.Error("context crossed requests")
		}
		c.String(200, c.Param("id"))
	})
	var wg sync.WaitGroup
	for i := range 64 {
		wg.Go(func() {
			for range 20 {
				id := fmt.Sprint(i)
				if w := request(e, "GET", "/users/"+id); w.Code != 200 || w.Body.String() != id {
					t.Errorf("%d %q", w.Code, w.Body.String())
				}
				if w := request(e, "GET", "/missing"); w.Code != 404 {
					t.Error(w.Code)
				}
			}
		})
	}
	wg.Wait()
}

type restExample struct{}

func (restExample) GET(c *Context)  { c.String(200, "get:"+c.Param("id")) }
func (restExample) POST(c *Context) { c.String(201, "post:"+c.Param("id")) }
func TestRestAndCustomMethods(t *testing.T) {
	e := New()
	e.Group("/v1").Rest("/users/:id", restExample{})
	e.Union([]string{"PROPFIND", "REPORT"}, "/custom", func(c *Context) { c.String(200, c.Request().Method) })
	for _, tc := range []struct{ method, path, body string }{{"GET", "/v1/users/5", "get:5"}, {"POST", "/v1/users/6", "post:6"}, {"PROPFIND", "/custom", "PROPFIND"}, {"REPORT", "/custom", "REPORT"}} {
		if w := request(e, tc.method, tc.path); w.Body.String() != tc.body {
			t.Errorf("%s %s: %q", tc.method, tc.path, w.Body.String())
		}
	}
}

func BenchmarkRadixLookup(b *testing.B) {
	for _, dynamic := range []bool{false, true} {
		b.Run(fmt.Sprint(dynamic), func(b *testing.B) {
			router := newRadixRouter()
			for i := range 1000 {
				path := fmt.Sprintf("/resources/%d/details", i)
				if dynamic {
					path += "/:id"
				}
				router.Insert("GET", path, &Route{Path: path})
			}
			path := "/resources/789/details"
			if dynamic {
				path += "/42"
			}
			params := make(Params, 0, 4)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				params = params[:0]
				if router.Lookup("GET", path, &params) == nil {
					b.Fatal("missing route")
				}
			}
		})
	}
}

// exactRouter 是仅支持精确匹配的替代实现，用于验证引擎只依赖 IRouter 契约。
type exactRouter struct {
	Fallbacks
	routes map[string]*Route
}

func newExactRouter() *exactRouter { return &exactRouter{routes: make(map[string]*Route)} }

func (r *exactRouter) Insert(method, path string, route *Route) {
	key := method + " " + path
	if _, ok := r.routes[key]; ok {
		panic("holy: duplicate route " + key)
	}
	r.routes[key] = route
}

func (r *exactRouter) Lookup(method, path string, _ *Params) *Route {
	return r.routes[method+" "+path]
}

func (r *exactRouter) Methods() []string {
	seen := make(map[string]bool)
	methods := make([]string, 0, len(r.routes))
	for key := range r.routes {
		method, _, _ := strings.Cut(key, " ")
		if !seen[method] {
			seen[method] = true
			methods = append(methods, method)
		}
	}
	return methods
}

func (r *exactRouter) Iter() iter.Seq[*Route] {
	return func(yield func(*Route) bool) {
		for _, route := range r.routes {
			if !yield(route) {
				return
			}
		}
	}
}

var _ IRouter = (*exactRouter)(nil)

func TestCustomRouter(t *testing.T) {
	e := New(WithRouter(newExactRouter()), func(e *Engine) { e.HandleMethodNotAllowed = true })
	var order []string
	e.Middleware(func(c *Context) { order = append(order, "global"); c.Next() })
	e.Use(PosAhead, Identical("/exact"), func(c *Context) { order = append(order, "hook"); c.Next() })
	e.GET("/exact", func(c *Context) { order = append(order, "handler"); c.String(200, "ok") })
	e.POST("/exact", func(c *Context) { c.String(201, "created") })
	// 匹配语义完全由实现决定：替代实现不解析参数路由。
	e.GET("/param/:id", func(*Context) { t.Error("param route should not match") })
	w := request(e, "GET", "/exact")
	if w.Code != 200 || !reflect.DeepEqual(order, []string{"global", "hook", "handler"}) {
		t.Fatalf("code=%d order=%v", w.Code, order)
	}
	if w := request(e, "GET", "/param/7"); w.Code != 404 {
		t.Fatal(w.Code)
	}
	// 405 的 Allow 头来自 Methods() 与 Lookup()。
	if w := request(e, "DELETE", "/exact"); w.Code != 405 || w.Header().Get("Allow") != "GET, POST" {
		t.Fatalf("code=%d allow=%q", w.Code, w.Header().Get("Allow"))
	}
	// 相同 (method, path) 重复注册由实现负责检测。
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected duplicate panic")
			}
		}()
		dup := New(WithRouter(newExactRouter()))
		dup.GET("/x", func(*Context) {})
		dup.GET("/x", func(*Context) {})
	}()
	// WithRouter 拒绝空实现。
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected nil router panic")
			}
		}()
		New(WithRouter(nil))
	}()
}

func TestRadixRouterIterAndMethods(t *testing.T) {
	testRouterIterAndMethods(t, func() IRouter { return newRadixRouter() })
}

func testRouterIterAndMethods(t *testing.T, newRouter func() IRouter) {
	inserted := map[string]bool{"GET /a": true, "GET /a/:id": true, "POST /b/*px": true, "PATCH /c": true}
	r := newRouter()
	for key := range inserted {
		method, path, _ := strings.Cut(key, " ")
		r.Insert(method, path, &Route{Method: method, Path: path})
	}
	for key := range inserted {
		method, path, _ := strings.Cut(key, " ")
		if r.Lookup(method, path, &Params{}) == nil {
			t.Errorf("missing %s", key)
		}
	}
	// Iter 恰好枚举全部注册项。
	seen := make(map[string]bool)
	for route := range r.Iter() {
		key := route.Method + " " + route.Path
		if !inserted[key] {
			t.Fatalf("unexpected %s", key)
		}
		seen[key] = true
	}
	if len(seen) != len(inserted) {
		t.Errorf("iter=%v want %v", seen, inserted)
	}
	// 消费方提前退出后迭代立即停止。
	count := 0
	for range r.Iter() {
		count++
		break
	}
	if count != 1 {
		t.Errorf("break after first: count=%d", count)
	}
	// Methods 返回已注册 method 的集合。
	got := map[string]bool{}
	for _, m := range r.Methods() {
		got[m] = true
	}
	if len(got) != 3 || !got["GET"] || !got["POST"] || !got["PATCH"] {
		t.Errorf("methods=%v", got)
	}
	// Lookup 未命中时把 params 恢复到调用前长度。
	params := Params{{Key: "keep", Value: "x"}}
	if r.Lookup("GET", "/a/7/extra", &params) != nil || len(params) != 1 {
		t.Errorf("params not restored: %v", params)
	}
	// 空实现的 Iter 与 Methods。
	empty := newRouter()
	for range empty.Iter() {
		t.Error("empty router yielded a route")
	}
	if len(empty.Methods()) != 0 {
		t.Error("empty router has methods")
	}
}

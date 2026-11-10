package engine

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
)

type matcherFunc func(method, path string) bool

func (f matcherFunc) Match(method, path string) bool { return f(method, path) }

// 相同分组契约同时覆盖内置实现和仅实现 IRouter 的第三方替身。
func TestGroupsAcrossRouters(t *testing.T) {
	for _, backend := range []struct {
		name      string
		newEngine func() *Engine
		pattern   bool
	}{
		{"radix", func() *Engine { return New() }, true},
		{"simple", func() *Engine { return New(WithSimpleRouter()) }, false},
		{"custom", func() *Engine { return New(WithRouter(newExactRouter())) }, false},
	} {
		t.Run(backend.name, func(t *testing.T) {
			newEngine := backend.newEngine
			t.Run("paths", func(t *testing.T) {
				for _, tc := range []struct{ parent, child, path, want string }{
					{"/api", "/v1", "/health", "/api/v1/health"},
					{"/api/", "/v1/", "/health", "/api/v1/health"},
					{"", "", "", "/"},
					{"/", "/", "/health", "/health"},
					{"/api", "", "", "/api"},
					{"/api", "", "/", "/api/"},
					{"/api//", "/v1", "/health", "/api//v1/health"},
					{"/api", "/./v1", "/../health", "/api/./v1/../health"},
				} {
					t.Run(tc.want, func(t *testing.T) {
						e := newEngine()
						e.Group(tc.parent).Group(tc.child).GET(tc.path, func(c *Context) { c.String(200, c.FullPath()) })
						e.Prepare()
						if w := request(e, "GET", tc.want); w.Code != 200 || w.Body.String() != tc.want {
							t.Fatalf("code=%d body=%q, want %q", w.Code, w.Body.String(), tc.want)
						}
					})
				}
			})
			t.Run("guard snapshots and order", func(t *testing.T) {
				e := newEngine()
				var order []string
				mark := func(name string) HandlerFunc {
					return func(c *Context) {
						order = append(order, name+"<")
						c.Next()
						order = append(order, name+">")
					}
				}
				parent := e.Group("/api", mark("parent"))
				child := parent.Group("/v1", mark("child"))
				sibling := parent.Group("/v2")
				handler := func(c *Context) { order = append(order, "handler"); c.String(200, "ok") }
				parent.GET("/old", handler)
				child.GET("/old", handler)
				parent.Guard(mark("late-parent"))
				child.Guard(mark("late-child"))
				parent.GET("/new", handler)
				child.GET("/new", handler)
				sibling.GET("/health", handler)
				parent.Group("/v3").GET("/health", handler)
				// 全局守卫与 Hook 不受路由注册先后影响。
				e.Guard(mark("global"))
				e.Use(PosAhead, Prefix("/api", nil), mark("before"))
				e.Use(PosBehind, Prefix("/api", nil), func(*Context) { order = append(order, "after") })
				e.Prepare()
				for _, tc := range []struct{ path, want string }{
					{"/api/old", "parent<,handler,after,parent>"},
					{"/api/new", "parent<,late-parent<,handler,after,late-parent>,parent>"},
					{"/api/v1/old", "parent<,child<,handler,after,child>,parent>"},
					{"/api/v1/new", "parent<,child<,late-child<,handler,after,late-child>,child>,parent>"},
					{"/api/v2/health", "parent<,handler,after,parent>"},
					{"/api/v3/health", "parent<,late-parent<,handler,after,late-parent>,parent>"},
				} {
					order = nil
					w := request(e, "GET", tc.path)
					want := "global<,before<," + tc.want + ",before>,global>"
					if w.Code != 200 || strings.Join(order, ",") != want {
						t.Fatalf("%s: code=%d order=%v, want %s", tc.path, w.Code, order, want)
					}
				}
			})
			t.Run("group hooks", func(t *testing.T) {
				e := newEngine()
				e.HandleMethodNotAllowed = true
				var order []string
				mark := func(name string) HandlerFunc {
					return func(*Context) { order = append(order, name) }
				}
				api := e.Group("/api", mark("guard"))
				v1 := api.Group("/v1")
				v1.GET("/users/:id", mark("get"))
				all := Prefix("/", nil)
				v1.Use(PosAhead, all, mark("child-before"))
				e.Use(PosAhead, all, mark("global-before"))
				api.Use(PosAhead, all, mark("parent-before"))
				api.Use(PosBehind, matcherFunc(func(method, path string) bool {
					return method == "GET" && path == "/api/v1/users/:id"
				}), mark("audit"))
				e.Use(PosBehind, all, mark("global-after"))
				v1.Use(PosBehind, all, mark("child-after"))
				v1.POST("/users/:id", mark("post"))
				e.PUT("/api/v1/users/:id", mark("put"))
				api.GET("/health", mark("health"))
				api.Group("/v2").GET("/health", mark("health"))
				e.Group("/api/v1").GET("/other", mark("other"))
				e.GET("/api/v1/root", mark("root"))
				e.Prepare()

				userPath := "/api/v1/users/:id"
				if backend.pattern {
					userPath = "/api/v1/users/42"
				}
				for _, tc := range []struct {
					method, path string
					code         int
					want         string
				}{
					{"GET", userPath, 200, "child-before,global-before,parent-before,guard,get,audit,global-after,child-after"},
					{"POST", userPath, 200, "child-before,global-before,parent-before,guard,post,global-after,child-after"},
					{"PUT", userPath, 200, "global-before,put,global-after"},
					{"GET", "/api/health", 200, "global-before,parent-before,guard,health,global-after"},
					{"GET", "/api/v2/health", 200, "global-before,parent-before,guard,health,global-after"},
					{"GET", "/api/v1/other", 200, "global-before,other,global-after"},
					{"GET", "/api/v1/root", 200, "global-before,root,global-after"},
					{"GET", "/api/v1/missing", 404, ""},
					{"DELETE", userPath, 405, ""},
				} {
					order = nil
					w := request(e, tc.method, tc.path)
					if w.Code != tc.code || strings.Join(order, ",") != tc.want {
						t.Errorf("%s %s: code=%d order=%v, want %d %s", tc.method, tc.path, w.Code, order, tc.code, tc.want)
					}
				}
			})
			t.Run("abort", func(t *testing.T) {
				e := newEngine()
				e.Group("/private", func(c *Context) { c.AbortWithStatus(401) }).Group("/v1").GET("/health", func(*Context) {
					t.Error("aborted group handler executed")
				})
				e.Prepare()
				if w := request(e, "GET", "/private/v1/health"); w.Code != 401 {
					t.Fatal(w.Code)
				}
			})
			t.Run("registration methods", func(t *testing.T) {
				e := newEngine()
				g := e.Group("/api").Group("/v1")
				g.Rest("/users", restExample{})
				handler := func(c *Context) { c.AbortWithStatus(204) }
				e.AnyMethod("/any", handler)
				g.AnyMethod("/any", handler)
				g.Union([]string{"REPORT", "PROPFIND"}, "/custom", handler)
				e.Prepare()
				for _, method := range []string{"GET", "POST"} {
					if w := request(e, method, "/api/v1/users"); w.Code != map[string]int{"GET": 200, "POST": 201}[method] {
						t.Fatalf("REST %s: %d", method, w.Code)
					}
				}
				for _, path := range []string{"/any", "/api/v1/any"} {
					for _, method := range []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "HEAD", "TRACE", "CONNECT"} {
						if w := request(e, method, path); w.Code != 204 {
							t.Errorf("%s %s: %d", method, path, w.Code)
						}
					}
					if w := request(e, "REPORT", path); w.Code != 404 {
						t.Errorf("REPORT %s: %d", path, w.Code)
					}
				}
				for _, method := range []string{"REPORT", "PROPFIND"} {
					if w := request(e, method, "/api/v1/custom"); w.Code != 204 {
						t.Errorf("%s /api/v1/custom: %d", method, w.Code)
					}
				}
			})
			t.Run("validation and freeze", func(t *testing.T) {
				noop := func(*Context) {}
				for _, tc := range []struct {
					name string
					run  func(*Engine, *RouterGroup)
				}{
					{"duplicate", func(e *Engine, g *RouterGroup) { e.GET("/api/x", noop); g.GET("/x", noop) }},
					{"invalid prefix", func(_ *Engine, g *RouterGroup) { g.Group("invalid") }},
					{"invalid path", func(_ *Engine, g *RouterGroup) { g.GET("invalid", noop) }},
					{"nil guard", func(_ *Engine, g *RouterGroup) { g.Guard(nil) }},
					{"invalid hook position", func(_ *Engine, g *RouterGroup) { g.Use(0, Contains(""), noop) }},
					{"nil hook matcher", func(_ *Engine, g *RouterGroup) { g.Use(PosAhead, nil, noop) }},
					{"nil hook handler", func(_ *Engine, g *RouterGroup) { g.Use(PosBehind, Contains(""), nil) }},
					{"nil group guard", func(_ *Engine, g *RouterGroup) { g.Group("/v1", nil) }},
					{"missing handler", func(_ *Engine, g *RouterGroup) { g.GET("/x") }},
					{"frozen root", func(e *Engine, _ *RouterGroup) { e.Prepare(); e.Group("/new") }},
					{"frozen child", func(e *Engine, g *RouterGroup) { e.Prepare(); g.Group("/new") }},
					{"frozen guard", func(e *Engine, g *RouterGroup) { e.Prepare(); g.Guard(noop) }},
					{"frozen hook", func(e *Engine, g *RouterGroup) { e.Prepare(); g.Use(PosAhead, Contains(""), noop) }},
					{"frozen route", func(e *Engine, g *RouterGroup) { e.Prepare(); g.GET("/new", noop) }},
				} {
					t.Run(tc.name, func(t *testing.T) {
						e := newEngine()
						g := e.Group("/api", noop)
						defer func() {
							if recover() == nil {
								t.Error("expected panic")
							}
						}()
						tc.run(e, g)
					})
				}
			})
			t.Run("handler limit", func(t *testing.T) {
				for _, count := range []int{63, 64} {
					t.Run(fmt.Sprint(count), func(t *testing.T) {
						e := newEngine()
						noop := func(*Context) {}
						e.Guard(noop)
						e.Use(PosAhead, Prefix("/api", nil), noop)
						handlers := make([]HandlerFunc, count-5)
						for i := range handlers {
							handlers[i] = noop
						}
						api := e.Group("/api", handlers...)
						api.Use(PosBehind, Prefix("/api", nil), noop)
						api.Group("/v1", noop).GET("/x", noop)
						defer func() {
							p := recover()
							if (p != nil) != (count == 64) {
								t.Fatalf("chain length %d: panic=%v", count, p)
							}
						}()
						e.Prepare()
					})
				}
			})
			t.Run("matching capabilities", func(t *testing.T) {
				e := newEngine()
				e.Group("/orgs/:org").GET("/files/*path", func(c *Context) {
					c.String(200, c.Param("org")+"|"+c.Param("path"))
				})
				e.Prepare()
				path, body := "/orgs/:org/files/*path", "|"
				if backend.pattern {
					path, body = "/orgs/acme/files/a/b", "acme|/a/b"
				}
				if w := request(e, "GET", path); w.Code != 200 || w.Body.String() != body {
					t.Fatalf("code=%d body=%q, want %q", w.Code, w.Body.String(), body)
				}
				if !backend.pattern {
					if w := request(e, "GET", "/orgs/acme/files/a/b"); w.Code != 404 {
						t.Fatal(w.Code)
					}
				}
			})
			t.Run("concurrent requests", func(t *testing.T) {
				e := newEngine()
				e.Group("/api", func(c *Context) { c.Response().Header().Set("X-Group", "api"); c.Next() }).Group("/v1").GET("/health", func(c *Context) {
					c.String(http.StatusOK, c.FullPath())
				})
				e.Prepare()
				var wg sync.WaitGroup
				for range 16 {
					wg.Add(1)
					go func() {
						defer wg.Done()
						for range 10 {
							w := request(e, "GET", "/api/v1/health")
							if w.Code != 200 || w.Body.String() != "/api/v1/health" || w.Header().Get("X-Group") != "api" {
								t.Errorf("code=%d body=%q headers=%v", w.Code, w.Body.String(), w.Header())
							}
						}
					}()
				}
				wg.Wait()
			})
		})
	}
}

package engine

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
)

// 通过 IRouter 包装默认树，模拟需要非 nil 参数切片的第三方实现。
type probeFallbackRouter struct {
	IRouter
	methods      []string
	methodsCalls int
}

func (r *probeFallbackRouter) Methods() []string {
	r.methodsCalls++
	return r.methods
}

func (r *probeFallbackRouter) Lookup(method, path string, params *Params) *Route {
	if params == nil {
		panic("custom router requires parameter storage")
	}
	return r.IRouter.Lookup(method, path, params)
}

func TestCustomRouterProbeFallback(t *testing.T) {
	methods := []string{"REPORT", "POST", "GET"}
	r := &probeFallbackRouter{IRouter: newRadixRouter(), methods: append([]string(nil), methods...)}
	e := New(WithRouter(r), func(e *Engine) { e.HandleMethodNotAllowed = true })
	for _, path := range []string{"/users/:id", "/files/*rest", "/slash/:id/"} {
		e.Union(methods, path, func(c *Context) { c.AbortWithStatus(204) })
	}
	fallback := func(c *Context) {
		if len(c.Params) != 0 || c.FullPath() != "" {
			t.Errorf("probe leaked route state: %v %q", c.Params, c.FullPath())
		}
	}
	e.NoRoute(fallback, func(c *Context) { c.AbortWithStatus(404) })
	e.NoMethod(fallback, func(c *Context) { c.AbortWithStatus(405) })
	for range 2 {
		for _, tc := range []struct {
			method, path    string
			code            int
			allow, location string
		}{
			{"DELETE", "/users/7", 405, "GET, POST, REPORT", ""},
			{"DELETE", "/files/a/b.txt", 405, "GET, POST, REPORT", ""},
			{"DELETE", "/missing", 404, "", ""},
			{"DELETE", "/users/7/missing", 404, "", ""},
			{"DELETE", "/slash/7", 404, "", ""},
			{"DELETE", "/slash/7/", 405, "GET, POST, REPORT", ""},
			{"GET", "/files?q=%2F", 301, "", "/files/?q=%2F"},
			{"POST", "/slash/7?q=x", 307, "", "/slash/7/?q=x"},
		} {
			w := request(e, tc.method, tc.path)
			if w.Code != tc.code || w.Header().Get("Allow") != tc.allow || w.Header().Get("Location") != tc.location {
				t.Fatalf("%s %s: code=%d allow=%q location=%q", tc.method, tc.path, w.Code, w.Header().Get("Allow"), w.Header().Get("Location"))
			}
		}
	}
	if r.methodsCalls != 1 {
		t.Errorf("Methods called %d times; want once at freeze", r.methodsCalls)
	}
	if !reflect.DeepEqual(r.methods, methods) {
		t.Errorf("borrowed Methods slice changed: %v; want %v", r.methods, methods)
	}
}

func TestMethodProbeWithManyCustomMethods(t *testing.T) {
	e := New(func(e *Engine) { e.HandleMethodNotAllowed = true })
	var methods []string
	for i := 11; i >= 0; i-- {
		method := fmt.Sprintf("CUSTOM%d", i)
		methods = append(methods, method)
		e.Handle(method, "/files/*rest", func(c *Context) { c.AbortWithStatus(204) })
	}
	sort.Strings(methods)
	if w := request(e, "GET", "/files/a"); w.Code != 405 || w.Header().Get("Allow") != strings.Join(methods, ", ") {
		t.Fatalf("code=%d allow=%q", w.Code, w.Header().Get("Allow"))
	}
}

func TestConcurrentRouteProbes(t *testing.T) {
	e := New(func(e *Engine) { e.HandleMethodNotAllowed = true })
	e.GET("/users/:id/", func(c *Context) { c.String(200, c.Param("id")) })
	e.GET("/files/*rest", func(c *Context) { c.String(200, c.Param("rest")) })
	e.POST("/files/*rest", func(c *Context) { c.AbortWithStatus(204) })
	e.NoMethod(func(c *Context) {
		if len(c.Params) != 0 || c.FullPath() != "" {
			t.Error("405 retained route state")
		}
		c.AbortWithStatus(405)
	})
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Go(func() {
			for range 10 {
				for _, tc := range []struct {
					method, path, allow, location string
					code                          int
				}{
					{"DELETE", "/files/a/b", "GET, POST", "", 405},
					{"DELETE", "/users/7/", "GET", "", 405},
					{"DELETE", "/missing", "", "", 404},
					{"GET", "/users/7", "", "/users/7/", 301},
				} {
					w := request(e, tc.method, tc.path)
					if w.Code != tc.code || w.Header().Get("Allow") != tc.allow || w.Header().Get("Location") != tc.location {
						t.Errorf("%s %s: code=%d headers=%v", tc.method, tc.path, w.Code, w.Header())
					}
				}
				id := fmt.Sprint(i)
				for path, want := range map[string]string{"/users/" + id + "/": id, "/files/" + id: "/" + id} {
					if w := request(e, "GET", path); w.Code != 200 || w.Body.String() != want {
						t.Errorf("%s: code=%d body=%q", path, w.Code, w.Body.String())
					}
				}
			}
		})
	}
	wg.Wait()
}

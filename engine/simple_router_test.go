package engine

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestWithSimpleRouter(t *testing.T) {
	opt := WithSimpleRouter()
	first, second := New(opt), New(opt)
	for _, e := range []*Engine{first, second} {
		if _, ok := e.router.(*SimpleRouter); !ok {
			t.Fatalf("router type = %T; want *SimpleRouter", e.router)
		}
	}
	first.GET("/users/42", func(c *Context) { c.String(200, "first") })
	second.GET("/users/42", func(c *Context) { c.String(200, "second") })
	first.GET("/first-only", func(c *Context) { c.String(200, "first") })
	for _, tc := range []struct {
		e    *Engine
		want string
	}{{first, "first"}, {second, "second"}} {
		if w := request(tc.e, "GET", "/users/42"); w.Code != 200 || w.Body.String() != tc.want {
			t.Fatalf("code=%d body=%q; want 200 %q", w.Code, w.Body.String(), tc.want)
		}
	}
	if w := request(second, "GET", "/first-only"); w.Code != 404 {
		t.Fatalf("router option shared routes between engines: code=%d", w.Code)
	}
}

func TestRouterOptionOrder(t *testing.T) {
	custom := newExactRouter()
	if e := New(WithSimpleRouter(), WithRouter(custom)); e.router != custom {
		t.Fatalf("last router option was not applied: %T", e.router)
	}
	e := New(WithRouter(custom), WithSimpleRouter())
	if _, ok := e.router.(*SimpleRouter); !ok {
		t.Fatalf("last router option was not applied: %T", e.router)
	}
}

func TestSimpleRouterExactLookup(t *testing.T) {
	var r SimpleRouter
	if r.Lookup("GET", "/", nil) != nil || len(r.Methods()) != 0 {
		t.Fatal("zero router is not empty")
	}
	for range r.Iter() {
		t.Fatal("zero router yielded a route")
	}
	paths := []string{"/z", "/users/:id", "/users/:name", "/files/*rest", "/files/:id", "/regex/[0-9]+", "/a/", "/a//b", "/a/../b", "/%2F", "/版本/中文", "/a", "/", "/ab"}
	for _, method := range []string{"POST", "GET"} {
		for _, path := range paths {
			r.Insert(method, path, &Route{Method: method, Path: path})
		}
	}
	for _, method := range []string{"GET", "POST"} {
		for _, path := range paths {
			params := Params{{Key: "keep", Value: "original"}}
			route := r.Lookup(method, path, &params)
			if route == nil || route.Method != method || route.Path != path || !reflect.DeepEqual(params, Params{{"keep", "original"}}) {
				t.Fatalf("%s %q: route=%v params=%v", method, path, route, params)
			}
			if r.Lookup(method, path, nil) != route {
				t.Fatalf("nil parameter probe differs for %s %q", method, path)
			}
		}
	}
	for _, tc := range []struct{ method, path string }{
		{"HEAD", "/a"}, {"get", "/a"}, {"GET", ""}, {"GET", "/missing"},
		{"GET", "/users/42"}, {"GET", "/files/a/b"}, {"GET", "/files/"},
		{"GET", "/regex/123"}, {"GET", "/a/extra"}, {"GET", "/A"},
		{"GET", "//a"}, {"GET", "/a/b"}, {"GET", "///"},
	} {
		params := Params{{Key: "keep", Value: "original"}}
		if route := r.Lookup(tc.method, tc.path, &params); route != nil || !reflect.DeepEqual(params, Params{{"keep", "original"}}) {
			t.Fatalf("%s %q should miss without changing params: %v %v", tc.method, tc.path, route, params)
		}
	}
	count := 0
	for range r.Iter() {
		count++
	}
	if count != 2*len(paths) {
		t.Fatalf("Iter yielded %d routes; want %d", count, 2*len(paths))
	}
	for range r.Iter() {
		break
	}
	if methods := r.Methods(); !reflect.DeepEqual(methods, []string{"GET", "POST"}) {
		t.Fatalf("methods=%v", methods)
	} else {
		methods[0] = "PATCH"
		if !reflect.DeepEqual(r.Methods(), []string{"GET", "POST"}) {
			t.Fatal("caller changed router methods")
		}
	}
}

func TestSimpleRouterRejectedInsert(t *testing.T) {
	r := NewSimpleRouter()
	valid := &Route{Method: "GET", Path: "/existing"}
	r.Insert(valid.Method, valid.Path, valid)
	for _, tc := range []struct {
		method, path string
		route        *Route
	}{
		{"GET", "/existing", valid},
		{"", "/invalid", valid},
		{"GET POST", "/invalid", valid},
		{"GET", "relative", valid},
		{"GET", "/bad?query", valid},
		{"GET", "/nil", nil},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected registration panic")
				}
			}()
			r.Insert(tc.method, tc.path, tc.route)
		})
	}
	var routes []*Route
	for route := range r.Iter() {
		routes = append(routes, route)
	}
	if !reflect.DeepEqual(routes, []*Route{valid}) || r.Lookup("GET", "/existing", nil) != valid {
		t.Fatalf("rejected insert changed routes: %v", routes)
	}
}

func TestSimpleRouterHTTP(t *testing.T) {
	e := New(WithSimpleRouter(), func(e *Engine) { e.HandleMethodNotAllowed = true })
	var order []string
	e.Middleware(func(c *Context) { order = append(order, "global"); c.Next() })
	e.Use(PosAhead, Identical("/api/users/42"), func(c *Context) { order = append(order, "hook"); c.Next() })
	e.Group("/api", func(c *Context) { order = append(order, "group"); c.Next() }).GET("/users/42", func(c *Context) {
		order = append(order, "handler")
		c.String(200, c.FullPath())
	})
	e.POST("/api/users/42", func(c *Context) { c.AbortWithStatus(201) })
	e.GET("/files/*rest", func(c *Context) {
		if len(c.Params) != 0 {
			t.Errorf("exact route extracted parameters: %v", c.Params)
		}
		c.String(200, c.FullPath())
	})
	if w := request(e, "GET", "/api/users/42"); w.Code != 200 || w.Body.String() != "/api/users/42" || strings.Join(order, ",") != "global,hook,group,handler" {
		t.Fatalf("code=%d body=%q order=%v", w.Code, w.Body.String(), order)
	}
	for _, tc := range []struct {
		method, path, allow, location string
		code                          int
	}{
		{"POST", "/api/users/42", "", "", 201},
		{"DELETE", "/api/users/42", "GET, POST", "", 405},
		{"HEAD", "/api/users/42", "GET, POST", "", 405},
		{"OPTIONS", "/api/users/42", "GET, POST", "", 405},
		{"GET", "/api/users/42/?q=x", "", "/api/users/42?q=x", 301},
		{"POST", "/api/users/42/", "", "/api/users/42", 307},
		{"GET", "/api/users/43", "", "", 404},
		{"GET", "/files/a/b", "", "", 404},
		{"DELETE", "/files/a/b", "", "", 404},
		{"GET", "/files/*rest", "", "", 200},
	} {
		w := request(e, tc.method, tc.path)
		if w.Code != tc.code || w.Header().Get("Allow") != tc.allow || w.Header().Get("Location") != tc.location {
			t.Fatalf("%s %s: code=%d allow=%q location=%q", tc.method, tc.path, w.Code, w.Header().Get("Allow"), w.Header().Get("Location"))
		}
	}
}

func TestSimpleRouterConcurrentRequests(t *testing.T) {
	e := New(WithSimpleRouter())
	for i := range 32 {
		path := fmt.Sprintf("/users/%d", i)
		e.GET(path, func(c *Context) { c.String(200, c.FullPath()) })
	}
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Go(func() {
			path := fmt.Sprintf("/users/%d", i)
			for range 10 {
				if w := request(e, "GET", path); w.Code != 200 || w.Body.String() != path {
					t.Errorf("%s: code=%d body=%q", path, w.Code, w.Body.String())
				}
			}
		})
	}
	wg.Wait()
}

func BenchmarkSimpleRouterLookup(b *testing.B) {
	r := NewSimpleRouter()
	for i := range 1000 {
		path := fmt.Sprintf("/resources/%d/details", i)
		r.Insert("GET", path, &Route{Method: "GET", Path: path})
	}
	for _, tc := range []struct {
		name, method, path string
		match              bool
	}{
		{"hit", "GET", "/resources/789/details", true},
		{"miss", "GET", "/resources/1000/details", false},
		{"method-miss", "POST", "/resources/789/details", false},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if (r.Lookup(tc.method, tc.path, nil) != nil) != tc.match {
					b.Fatal("unexpected lookup result")
				}
			}
		})
	}
}

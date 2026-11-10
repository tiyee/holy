package engine

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func BenchmarkRadixLookupCases(b *testing.B) {
	var deepRoute, deepPath string
	for i := range 16 {
		deepRoute += fmt.Sprintf("/segment/:p%d", i)
		deepPath += "/segment/42"
	}
	for _, tc := range []struct {
		name, path string
		match      bool
	}{
		{"static", "/users/new", true},
		{"params", "/users/42/posts/7", true},
		{"catch-all", "/files/a/b.txt", true},
		{"empty-catch-all", "/files/", true},
		{"static-fallback", "/users/new/posts/7", true},
		{"catch-all-fallback", "/files/static/7/missing", true},
		{"miss-after-params", "/users/42/posts/7/missing", false},
		{"deep", deepPath, true},
	} {
		b.Run(tc.name, func(b *testing.B) {
			router := newRadixRouter()
			for _, path := range []string{
				"/users/new", "/users/:id/posts/:post", "/files/*path",
				"/files/static/:id/end", deepRoute,
			} {
				router.Insert("GET", path, &Route{Method: "GET", Path: path})
			}
			params := make(Params, 0, 16)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				params = params[:0]
				if (router.Lookup("GET", tc.path, &params) != nil) != tc.match {
					b.Fatal("unexpected lookup result")
				}
			}
		})
	}
}

// 复用请求和响应头，使用空处理器，以测量路由调度本身的开销。
type routerBenchmarkWriter struct{ header http.Header }

func (w *routerBenchmarkWriter) Header() http.Header         { return w.header }
func (w *routerBenchmarkWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w *routerBenchmarkWriter) WriteHeader(int)             {}

func BenchmarkRouterServeHTTP(b *testing.B) {
	for _, allow405 := range []bool{false, true} {
		for _, tc := range []struct {
			name, method, path string
			removeExtraSlash   bool
		}{
			{"static", "GET", "/users/new", false},
			{"params", "GET", "/users/42/posts/7", false},
			{"catch-all", "GET", "/files/a/b.txt", false},
			{"miss", "GET", "/missing", false},
			{"wrong-method-params", "REPORT", "/users/42/posts/7", false},
			{"wrong-method-catch-all", "REPORT", "/files/a/b.txt", false},
			{"trailing-slash", "GET", "/users/42/posts/7/", false},
			{"extra-slash/clean", "GET", "/users/42/posts/7", true},
			{"extra-slash/repeated", "GET", "/users//42///posts/7", true},
		} {
			b.Run(fmt.Sprintf("405_%v/%s", allow405, tc.name), func(b *testing.B) {
				e := New()
				e.HandleMethodNotAllowed = allow405
				e.RemoveExtraSlash = tc.removeExtraSlash
				for _, path := range []string{"/users/new", "/users/:id/posts/:post", "/files/*path"} {
					e.AnyMethod(path, func(*Context) {})
				}
				e.NoRoute(func(*Context) {})
				e.NoMethod(func(*Context) {})
				e.Prepare()
				req := &http.Request{Method: tc.method, URL: &url.URL{Path: tc.path}}
				w := &routerBenchmarkWriter{header: make(http.Header)}
				e.ServeHTTP(w, req)
				b.ReportAllocs()
				b.ResetTimer()
				for b.Loop() {
					e.ServeHTTP(w, req)
				}
			})
		}
	}
}

func BenchmarkRouterGETNoBody(b *testing.B) {
	e := New()
	e.GET("/users/:id", func(*Context) {})
	e.Prepare()
	req := httptest.NewRequest(http.MethodGet, "/users/42", nil)
	w := &routerBenchmarkWriter{header: make(http.Header)}
	e.ServeHTTP(w, req)
	b.ReportAllocs()
	for b.Loop() {
		req.Body = http.NoBody
		e.ServeHTTP(w, req)
	}
}

func BenchmarkRouterParallel(b *testing.B) {
	e := New(func(e *Engine) { e.HandleMethodNotAllowed = true })
	e.AnyMethod("/files/*path", func(*Context) {})
	e.NoMethod(func(*Context) {})
	e.Prepare()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		req := &http.Request{Method: "REPORT", URL: &url.URL{Path: "/files/a/b.txt"}}
		w := &routerBenchmarkWriter{header: make(http.Header)}
		for pb.Next() {
			e.ServeHTTP(w, req)
		}
	})
}

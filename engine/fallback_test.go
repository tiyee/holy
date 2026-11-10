package engine

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"sync"
	"testing"
)

func TestFallbackDispatch(t *testing.T) {
	for name, backend := range map[string]IEnginOpt{
		"radix": WithRouter(newRadixRouter()), "simple": WithSimpleRouter(), "custom": WithRouter(newExactRouter()),
	} {
		t.Run(name, func(t *testing.T) {
			var calls []string
			resolver := func(name string) FallbackFunc {
				return func(r *http.Request) HandlerFunc {
					calls = append(calls, name)
					if r.Header.Get("X-Select") != name {
						return nil
					}
					return func(c *Context) {
						if c.FullPath() != "" || len(c.Params) != 0 || c.Request() != r {
							t.Error("fallback inherited route state or a different request")
						}
						calls = append(calls, "handler")
						c.AbortWithStatus(202)
					}
				}
			}
			e := New(WithFallback(resolver("a"), resolver("b")), backend, WithFallback(resolver("c")))
			e.Fallback(resolver("d"), resolver("e"))
			e.HandleMethodNotAllowed = true
			e.Guard(func(c *Context) {
				calls = append(calls, "before")
				c.Next()
				calls = append(calls, "after")
			})
			e.GET("/known", func(c *Context) { c.AbortWithStatus(200) })
			e.OPTIONS("/known", func(c *Context) { c.AbortWithStatus(204) })
			e.Prepare()
			for _, tc := range []struct {
				method, path, selectName string
				code                     int
				order                    []string
			}{
				{"OPTIONS", "/known", "a", 204, []string{"before", "after"}},
				{"POST", "/known", "b", 202, []string{"a", "b", "before", "handler", "after"}},
				{"GET", "/known/", "d", 202, []string{"a", "b", "c", "d", "before", "handler", "after"}},
				{"GET", "/known/", "", 301, []string{"a", "b", "c", "d", "e"}},
				{"POST", "/known", "", 405, []string{"a", "b", "c", "d", "e", "before", "after"}},
				{"GET", "/absent", "", 404, []string{"a", "b", "c", "d", "e", "before", "after"}},
			} {
				calls = nil
				req := httptest.NewRequest(tc.method, tc.path, nil)
				req.Header.Set("X-Select", tc.selectName)
				w := httptest.NewRecorder()
				e.ServeHTTP(w, req)
				if w.Code != tc.code || !slices.Equal(calls, tc.order) {
					t.Errorf("%s %s select=%q: code=%d calls=%v; want %d %v", tc.method, tc.path, tc.selectName, w.Code, calls, tc.code, tc.order)
				}
				if tc.code == 405 && w.Header().Get("Allow") != "GET, OPTIONS" {
					t.Errorf("Allow = %q", w.Header().Get("Allow"))
				}
			}
		})
	}
}

func TestFallbackRegistration(t *testing.T) {
	match := func(*http.Request) HandlerFunc {
		return func(c *Context) { c.AbortWithStatus(202) }
	}
	e := New()
	func() {
		defer func() {
			if recover() == nil {
				t.Error("nil resolver accepted")
			}
		}()
		e.Fallback(match, nil)
	}()
	e.Prepare()
	if w := request(e, "GET", "/missing"); w.Code != 404 {
		t.Fatalf("partial registration: status %d", w.Code)
	}
	defer func() {
		if recover() == nil {
			t.Error("fallback registered after freeze")
		}
	}()
	e.Fallback(match)
}

func TestFallbackConcurrentHandlers(t *testing.T) {
	e := New()
	e.Fallback(func(r *http.Request) HandlerFunc {
		path := r.URL.Path
		return func(c *Context) { c.String(200, path) }
	})
	e.Prepare()
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Go(func() {
			path := "/" + strconv.Itoa(i)
			for range 20 {
				if w := request(e, "GET", path); w.Body.String() != path {
					t.Errorf("path %s: got %q", path, w.Body.String())
				}
			}
		})
	}
	wg.Wait()
}

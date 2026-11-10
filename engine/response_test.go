package engine

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestContextResponseController(t *testing.T) {
	w := httptest.NewRecorder()
	c := NewContext(w, httptest.NewRequest("GET", "/", nil))
	if c.Written() || c.Status() != 200 {
		t.Fatal("initial response state")
	}
	c.Response().WriteHeader(202)
	c.Response().WriteHeader(500)
	if err := http.NewResponseController(c.Response()).Flush(); err != nil {
		t.Fatal(err)
	}
	if !c.Written() || c.Status() != 202 || !w.Flushed || w.Code != 202 {
		t.Fatal("response state not preserved")
	}
	if _, _, err := http.NewResponseController(c.Response()).Hijack(); !errors.Is(err, http.ErrNotSupported) {
		t.Fatal(err)
	}
	if err := c.Response().(http.Pusher).Push("/resource", nil); !errors.Is(err, http.ErrNotSupported) {
		t.Fatal(err)
	}
}

func TestReleaseClearsRequestReferences(t *testing.T) {
	c := NewContext(httptest.NewRecorder(), httptest.NewRequest("GET", "/?q=first", nil))
	c.SetUserValue("secret", &struct{}{})
	c.Params = append(c.Params, Param{Key: "id", Value: "old"})
	c.Params = c.Params[:0]
	c.fallbackHandler = func(*Context) {}
	c.Query("q")
	c.release()
	if c.fallbackHandler != nil {
		t.Fatal("fallback handler retained")
	}
	if c.r != nil || c.w != nil || c.ctx != nil || c.handlers != nil || c.writer.ResponseWriter != nil || c.queryMap != nil || len(c.userData) != 0 {
		t.Fatal("request references retained")
	}
	for _, p := range c.Params[:cap(c.Params)] {
		if p.Key != "" || p.Value != "" {
			t.Fatal("backtracked parameter retained")
		}
	}
}

func TestEnginePropagatesCancellation(t *testing.T) {
	e := New()
	e.GET("/cancel", func(c *Context) {
		if c.Ctx().Err() != context.Canceled {
			t.Error("request cancellation lost")
		}
		c.AbortWithStatus(204)
	})
	e.Prepare()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := httptest.NewRequest("GET", "/cancel", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	e.ServeHTTP(w, r)
	if w.Code != 204 {
		t.Fatal(w.Code)
	}
}

func TestConfigurationFailureRemainsVisible(t *testing.T) {
	e := New()
	handlers := make([]HandlerFunc, 64)
	for i := range handlers {
		handlers[i] = func(*Context) {}
	}
	e.GET("/limit", handlers...)
	for _, prepare := range []func(){
		func() {
			_ = e.Run(func(*http.Server) { t.Fatal("server configured before successful preparation") })
		},
		e.Prepare,
	} {
		func() {
			defer func() {
				if p := recover(); p != "too many handlers for GET /limit: 64 (maximum 63)" {
					t.Errorf("startup panic = %v; want the handler limit failure", p)
				}
			}()
			prepare()
		}()
	}
}

func TestRedirectRemainsLocal(t *testing.T) {
	e := New()
	e.GET("//outside.example/", func(c *Context) { c.String(200, "ok") })
	e.Prepare()
	w := request(e, "GET", "//outside.example?q=1")
	if got := w.Header().Get("Location"); got != "/%2Foutside.example/?q=1" {
		t.Fatal(got)
	}
	if got := request(e, "GET", w.Header().Get("Location")); got.Code != 200 {
		t.Fatal(got.Code)
	}
}

func TestTrailingSlashRedirectPreservesPath(t *testing.T) {
	for _, tc := range []struct {
		name, route, path, location string
	}{
		{"double slash add", "/a//b/", "/a//b?q=%2F", "/a//b/?q=%2F"},
		{"double slash remove", "/a//b", "/a//b/?q=%2F", "/a//b?q=%2F"},
		{"dot add", "/a/./b/", "/a/./b", "/a/./b/"},
		{"dot remove", "/a/./b", "/a/./b/", "/a/./b"},
		{"parent add", "/a/../b/", "/a/../b", "/a/../b/"},
		{"parent remove", "/a/../b", "/a/../b/", "/a/../b"},
		{"escaped", "/a b/%2F/", "/a%20b/%252F?x=%3C&y=%22", "/a%20b/%252F/?x=%3C&y=%22"},
		{"unicode", "/中文/", "/%E4%B8%AD%E6%96%87", "/%E4%B8%AD%E6%96%87/"},
		{"local", "//outside.example/", "//outside.example?q=1", "/%2Foutside.example/?q=1"},
		{"opaque request target", "/", "x:https://outside.example/?q=%2F", "/?q=%2F"},
		{"empty query", "/items/", "/items?", "/items/?"},
	} {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodHead} {
			t.Run(tc.name+"/"+method, func(t *testing.T) {
				e := New()
				e.Handle(method, tc.route, func(c *Context) {
					if c.Request().URL.Path != tc.route {
						t.Errorf("followed path = %q, want %q", c.Request().URL.Path, tc.route)
					}
					c.AbortWithStatus(http.StatusNoContent)
				})
				e.Prepare()
				w := request(e, method, tc.path)
				wantCode := http.StatusTemporaryRedirect
				if method == http.MethodGet {
					wantCode = http.StatusMovedPermanently
				}
				if w.Code != wantCode || w.Header().Get("Location") != tc.location {
					t.Fatalf("redirect = %d %q, want %d %q", w.Code, w.Header().Get("Location"), wantCode, tc.location)
				}
				if method != http.MethodGet && w.Body.Len() != 0 {
					t.Fatal("POST/HEAD redirect must not have a body")
				}
				if method == http.MethodGet && tc.name == "escaped" && !strings.Contains(w.Body.String(), "&amp;y=%22") {
					t.Fatalf("HTML link was not escaped: %s", w.Body.String())
				}
				if got := request(e, method, w.Header().Get("Location")); got.Code != http.StatusNoContent {
					t.Fatalf("following redirect returned %d", got.Code)
				}
			})
		}
	}
}

func TestMaximumChainDoesNotImplyAbort(t *testing.T) {
	e := New()
	e.Guard(func(c *Context) {
		c.Next()
		if c.IsAborted() {
			t.Error("completed chain reported aborted")
		}
	})
	handlers := make([]HandlerFunc, 62)
	for i := range handlers {
		handlers[i] = func(*Context) {}
	}
	e.GET("/limit", handlers...)
	e.Prepare()
	request(e, "GET", "/limit")
}

func TestGroupGuardSnapshot(t *testing.T) {
	e := New()
	parent := e.Group("/api/")
	child := parent.Group("/v1")
	parent.Guard(func(c *Context) { c.Response().Header().Set("X-Parent", "new") })
	child.GET("/old", func(c *Context) { c.String(200, "old") })
	parent.GET("/new", func(c *Context) { c.String(200, "new") })
	e.Prepare()
	if w := request(e, "GET", "/api/v1/old"); w.Code != 200 || w.Header().Get("X-Parent") != "" {
		t.Fatal(w.Code, w.Header())
	}
	if w := request(e, "GET", "/api/new"); w.Code != 200 || w.Header().Get("X-Parent") != "new" {
		t.Fatal(w.Code, w.Header())
	}
}

func TestRealServerStreaming(t *testing.T) {
	e := New()
	e.GET("/stream", func(c *Context) {
		c.Response().Header().Set("Content-Type", "text/event-stream")
		_, _ = c.Response().Write([]byte("data: hello\n\n"))
		if err := http.NewResponseController(c.Response()).Flush(); err != nil {
			t.Error(err)
		}
	})
	e.Prepare()
	s := httptest.NewServer(e)
	defer s.Close()
	client := &http.Client{Timeout: time.Second}
	res, err := client.Get(s.URL + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatal(res.StatusCode, res.Header)
	}
}

func TestAbortHandlerPreservesStreamingFailure(t *testing.T) {
	e := New()
	e.GET("/", func(c *Context) {
		_, _ = c.Response().Write([]byte("partial"))
		if err := http.NewResponseController(c.Response()).Flush(); err != nil {
			t.Error(err)
		}
		panic(http.ErrAbortHandler)
	})
	e.Prepare()
	server := httptest.NewServer(e)
	defer server.Close()
	client := &http.Client{Timeout: time.Second}
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if string(data) != "partial" || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("aborted response: body=%q error=%v", data, err)
	}
}

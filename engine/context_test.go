package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestContextPreservesRequestContext(t *testing.T) {
	for _, tt := range []struct {
		name string
		init func(http.ResponseWriter, *http.Request) *Context
	}{
		{name: "NewContext", init: NewContext},
		{name: "reset", init: func(w http.ResponseWriter, r *http.Request) *Context {
			c := New().allocateContext()
			c.reset(w, r)
			return c
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			type requestKey struct{}
			deadline := time.Now().Add(time.Hour)
			parent := context.WithValue(context.Background(), requestKey{}, "request-value")
			requestCtx, cancel := context.WithDeadline(parent, deadline)
			defer cancel()
			r := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(requestCtx)
			c := tt.init(httptest.NewRecorder(), r)
			if c.Ctx() == nil {
				t.Fatal("Ctx() is nil")
			}
			if got := c.Ctx().Value(requestKey{}); got != "request-value" {
				t.Errorf("request context value = %v, want request-value", got)
			}
			if got, ok := c.Ctx().Deadline(); !ok || !got.Equal(deadline) {
				t.Errorf("Deadline() = (%v, %v), want (%v, true)", got, ok, deadline)
			}
			if err := c.Ctx().Err(); err != nil {
				t.Errorf("active request context error = %v", err)
			}
			cancel()
			select {
			case <-c.Ctx().Done():
			default:
				t.Error("Done() did not signal request cancellation")
			}
			if err := c.Ctx().Err(); err != context.Canceled {
				t.Errorf("canceled request context error = %v, want context.Canceled", err)
			}

			expiredCtx, stop := context.WithDeadline(parent, time.Now().Add(-time.Second))
			defer stop()
			r = r.WithContext(expiredCtx)
			c = tt.init(httptest.NewRecorder(), r)
			if err := c.Ctx().Err(); err != context.DeadlineExceeded {
				t.Errorf("expired request context error = %v, want context.DeadlineExceeded", err)
			}
		})
	}
}

func TestNewContextUserValues(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	c := NewContext(w, r)
	if c.Request() != r || c.writer.Unwrap() != w {
		t.Fatal("NewContext did not preserve the request and response writer")
	}
	if got := c.UserValue("user"); got != nil {
		t.Errorf("unset UserValue = %v, want nil", got)
	}
	c.SetUserValue("user", "alice")
	if got := c.UserValue("user"); got != "alice" {
		t.Errorf("UserValue = %v, want alice", got)
	}
	c.SetUserValue("user", "bob")
	if got := c.UserValue("user"); got != "bob" {
		t.Errorf("updated UserValue = %v, want bob", got)
	}
}

func TestContextResetIsolatesRequests(t *testing.T) {
	// Reuse the same instance deterministically instead of relying on sync.Pool.
	c := New().allocateContext()
	firstCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w1 := httptest.NewRecorder()
	r1 := httptest.NewRequest(http.MethodGet, "https://first.example/?old=first", nil).WithContext(firstCtx)
	c.reset(w1, r1)
	c.SetDomain("first.example")
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetUserValue("user", "alice")
	if got := c.Query("old"); got != "first" {
		t.Fatalf("first request query = %q, want first", got)
	}
	c.SetCookie("session", []byte("first"), time.Hour)
	cookies := w1.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Domain != "first.example" {
		t.Fatalf("first request cookies = %v, want Domain=first.example", cookies)
	}
	if got := cookies[0].SameSite; got != http.SameSiteStrictMode {
		t.Errorf("first request cookie SameSite = %v, want Strict", got)
	}
	c.Abort()
	cancel()

	w2 := httptest.NewRecorder()
	r2 := httptest.NewRequest(http.MethodGet, "https://second.example/?new=second", nil)
	c.reset(w2, r2)
	if c.Request() != r2 || c.writer.Unwrap() != w2 {
		t.Error("reset did not replace the request and response writer")
	}
	if c.Ctx().Err() != nil {
		t.Error("reset retained the canceled context from the first request")
	}
	if c.IsAborted() {
		t.Error("reset retained the aborted state from the first request")
	}
	if got := c.UserValue("user"); got != nil {
		t.Errorf("second request UserValue = %v, want nil", got)
	}
	c.SetUserValue("user", "bob")
	if got := c.UserValue("user"); got != "bob" {
		t.Errorf("second request UserValue = %v, want bob", got)
	}
	if got := c.Query("old"); got != "" {
		t.Errorf("second request retained old query = %q", got)
	}
	if got := c.Query("new"); got != "second" {
		t.Errorf("second request query = %q, want second", got)
	}
	c.SetCookie("session", []byte("second"), time.Hour)
	cookies = w2.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("second request cookies = %v, want one cookie", cookies)
	}
	if got := cookies[0].Domain; got != "" {
		t.Errorf("second request cookie Domain = %q, want empty domain", got)
	}
	if got := cookies[0].SameSite; got != http.SameSiteNoneMode {
		t.Errorf("second request cookie SameSite = %v, want None", got)
	}
}

func TestContextSetSameSite(t *testing.T) {
	e := New()
	e.GET("/", func(c *Context) {
		c.SetCookie("first", []byte("1"), time.Hour)
		inherit := &http.Cookie{Name: "inherit", Value: "1", SameSite: http.SameSiteDefaultMode}
		c.SetCookieV2(inherit)
		c.SetSameSite(http.SameSiteLaxMode)
		c.SetCookie("second", []byte("2"), time.Hour)
		c.SetCookieV2(inherit)
		c.SetCookieV2(&http.Cookie{Name: "third", Value: "3", SameSite: http.SameSiteStrictMode})
		c.SetCookieV2(&http.Cookie{Name: "unspecified", Value: "4"})
		c.SetCookieV2(nil)
	})
	e.Prepare()
	cookies := request(e, http.MethodGet, "/").Result().Cookies()
	want := []http.SameSite{
		http.SameSiteNoneMode, http.SameSiteNoneMode,
		http.SameSiteLaxMode, http.SameSiteLaxMode,
		http.SameSiteStrictMode, 0,
	}
	if len(cookies) != len(want) {
		t.Fatalf("got %d cookies, want %d", len(cookies), len(want))
	}
	for i, mode := range want {
		if got := cookies[i].SameSite; got != mode {
			t.Errorf("cookie %q SameSite = %v, want %v", cookies[i].Name, got, mode)
		}
	}
}

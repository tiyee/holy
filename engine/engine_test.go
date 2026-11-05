package engine

import (
	"fmt"
	"strings"
	"testing"
)

func TestDispatchHandlerLimit(t *testing.T) {
	for _, tt := range []struct {
		name      string
		handlers  int
		before    int
		after     int
		unmatched int
		wantPanic bool
	}{
		{name: "63 handlers", handlers: 63},
		{name: "64 handlers", handlers: 64, wantPanic: true},
		{name: "63 including hooks", handlers: 1, before: 31, after: 31},
		{name: "64 including hooks", handlers: 1, before: 32, after: 31, wantPanic: true},
		{name: "unmatched hooks excluded", handlers: 63, unmatched: 64},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := New()
			fn := func(*Context) {}
			handlers := make([]HandlerFunc, tt.handlers)
			for i := range handlers {
				handlers[i] = fn
			}
			e.GET("/limit", handlers...)
			for i := 0; i < tt.before; i++ {
				e.Use(PosAhead, Identical("/limit"), fn)
			}
			for i := 0; i < tt.after; i++ {
				e.Use(PosBehind, Identical("/limit"), fn)
			}
			for i := 0; i < tt.unmatched; i++ {
				e.Use(PosAhead, Identical("/other"), fn)
			}
			defer func() {
				p := recover()
				if !tt.wantPanic {
					if p != nil {
						t.Fatalf("dispatch() unexpectedly panicked: %v", p)
					}
					return
				}
				if p == nil {
					t.Fatal("dispatch() did not panic for more than 63 handlers")
				}
				if message := fmt.Sprint(p); !strings.Contains(message, "GET /limit: 64 (maximum 63)") {
					t.Errorf("panic = %q, want method, path and handler counts", message)
				}
			}()
			e.dispatch()
		})
	}
}

package cache

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

type contextTestCache struct {
	*testCache
	visit func(context.Context, string, string, []byte, time.Duration) error
}

func (c *contextTestCache) GetContext(ctx context.Context, key string) ([]byte, error) {
	if err := c.visit(ctx, "get", key, nil, 0); err != nil {
		return nil, err
	}
	return []byte("value"), nil
}
func (c *contextTestCache) SetContext(ctx context.Context, key string, value []byte) error {
	return c.visit(ctx, "set", key, value, 0)
}
func (c *contextTestCache) DeleteContext(ctx context.Context, key string) error {
	return c.visit(ctx, "delete", key, nil, 0)
}
func (c *contextTestCache) SetEContext(ctx context.Context, key string, value []byte, exp time.Duration) error {
	return c.visit(ctx, "set-expiry", key, value, exp)
}

var contextOperations = []struct {
	name string
	run  func(context.Context) error
}{
	{"get", func(ctx context.Context) error { _, err := GetContext(ctx, "key"); return err }},
	{"set", func(ctx context.Context) error { return SetContext(ctx, "key", []byte("value")) }},
	{"delete", func(ctx context.Context) error { return DeleteContext(ctx, "key") }},
	{"set-expiry", func(ctx context.Context) error { return SetEContext(ctx, "key", []byte("value"), time.Minute) }},
}

func TestContextOperationsPreserveRequestAndErrors(t *testing.T) {
	t.Cleanup(func() { InitCache(nil) })
	type requestKey struct{}
	ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), requestKey{}, "request"), time.Minute)
	defer cancel()
	deadline, _ := ctx.Deadline()
	backendErr := errors.New("backend unavailable")
	for _, op := range contextOperations {
		t.Run(op.name, func(t *testing.T) {
			for _, wantErr := range []error{nil, fmt.Errorf("lookup: %w", ErrMiss), backendErr} {
				calls := 0
				InitCache(&contextTestCache{testCache: &testCache{}, visit: func(got context.Context, method, key string, value []byte, exp time.Duration) error {
					calls++
					if got != ctx || got.Value(requestKey{}) != "request" {
						t.Fatal("请求 Context 未原样传递")
					}
					if actual, ok := got.Deadline(); !ok || !actual.Equal(deadline) {
						t.Fatal("请求截止时间丢失")
					}
					if method != op.name || key != "key" {
						t.Fatalf("操作参数 = %s %q", method, key)
					}
					if method == "set" || method == "set-expiry" {
						if string(value) != "value" {
							t.Fatalf("写入值 = %q", value)
						}
					}
					if method == "set-expiry" && exp != time.Minute {
						t.Fatalf("过期时间 = %s", exp)
					}
					return wantErr
				}})
				if err := op.run(ctx); err != wantErr || calls != 1 {
					t.Fatalf("操作错误 = %v，期望 %v；调用次数 = %d", err, wantErr, calls)
				}
			}
		})
	}
}

func TestContextOperationsRejectInvalidRequestsAndProviders(t *testing.T) {
	t.Cleanup(func() { InitCache(nil) })
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	unused := &contextTestCache{testCache: &testCache{}, visit: func(context.Context, string, string, []byte, time.Duration) error {
		t.Fatal("无效 Context 不应触发提供者操作")
		return nil
	}}
	for _, scenario := range []struct {
		name     string
		ctx      context.Context
		provider ICache
		want     error
	}{
		{"nil context", nil, unused, ErrInvalidContext},
		{"canceled", canceled, unused, context.Canceled},
		{"expired", expired, unused, context.DeadlineExceeded},
		{"not initialized", context.Background(), nil, ErrNotInitialized},
		{"no context capability", context.Background(), &testCache{}, ErrUnsupported},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			InitCache(scenario.provider)
			for _, op := range contextOperations {
				if err := op.run(scenario.ctx); !errors.Is(err, scenario.want) {
					t.Errorf("%s 错误 = %v，期望 %v", op.name, err, scenario.want)
				}
			}
		})
	}
}

func TestContextOperationsDuringProviderReplacement(t *testing.T) {
	t.Cleanup(func() { InitCache(nil) })
	type requestKey struct{}
	newProvider := func() *contextTestCache {
		return &contextTestCache{testCache: &testCache{}, visit: func(ctx context.Context, _, _ string, _ []byte, _ time.Duration) error {
			if ctx.Value(requestKey{}) == nil {
				return errors.New("请求 Context 丢失")
			}
			return nil
		}}
	}
	InitCache(newProvider())
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			ctx := context.WithValue(context.Background(), requestKey{}, i)
			for range 100 {
				InitCache(newProvider())
				for _, op := range contextOperations {
					if err := op.run(ctx); err != nil {
						t.Error(err)
					}
				}
			}
		})
	}
	wg.Wait()
}

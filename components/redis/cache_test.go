package redis

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	driver "github.com/redis/go-redis/v9"
	"github.com/tiyee/holy/components/cache"
)

type commandHook struct {
	process func(context.Context, driver.Cmder) error
}

func (h commandHook) DialHook(next driver.DialHook) driver.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) { return next(ctx, network, addr) }
}
func (h commandHook) ProcessHook(_ driver.ProcessHook) driver.ProcessHook { return h.process }
func (h commandHook) ProcessPipelineHook(next driver.ProcessPipelineHook) driver.ProcessPipelineHook {
	return next
}

func TestCacheRequestContextAndSharedExpiry(t *testing.T) {
	client := driver.NewClient(&driver.Options{Addr: "unused:6379"})
	defer client.Close()
	type contextKey struct{}
	client.AddHook(commandHook{process: func(ctx context.Context, cmd driver.Cmder) error {
		if ctx.Value(contextKey{}) != "request" {
			t.Error("request context lost")
		}
		switch cmd := cmd.(type) {
		case *driver.StringCmd:
			cmd.SetVal("value")
		case *driver.StatusCmd:
			cmd.SetVal("OK")
		case *driver.IntCmd:
			cmd.SetVal(1)
		}
		return nil
	}})
	shared := NewCache(client)
	ctx := context.WithValue(context.Background(), contextKey{}, "request")
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := shared.WithContext(canceled).Get("key"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	bound := shared.WithContext(ctx)
	if got, err := bound.Get("key"); err != nil || string(got) != "value" {
		t.Fatalf("request cache: %s %v", got, err)
	}
	if got, err := shared.GetContext(ctx, "key"); err != nil || string(got) != "value" {
		t.Fatal(err)
	}
	if shared.ctx.Err() != nil {
		t.Fatal("request view changed shared context")
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 50 {
				shared.SetDefaultExp(time.Minute)
				if err := bound.Set("key", []byte("value")); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	if err := bound.Delete("key"); err != nil {
		t.Fatal(err)
	}
	if _, err := NewCache(nil).Get("key"); !errors.Is(err, ErrNoClient) {
		t.Fatal(err)
	}
	if err := NewCache(nil).SetContext(ctx, "key", nil); !errors.Is(err, ErrNoClient) {
		t.Fatal(err)
	}
}

func TestFailedOpenClosesClient(t *testing.T) {
	failure := errors.New("ping failed")
	var created *driver.Client
	factory := func(opts *driver.Options) *driver.Client {
		created = driver.NewClient(opts)
		created.AddHook(commandHook{process: func(context.Context, driver.Cmder) error { return failure }})
		return created
	}
	client, err := open(context.Background(), &driver.Options{Addr: "unused:6379"}, factory)
	if client != nil || !errors.Is(err, failure) {
		t.Fatalf("Open: %v %v", client, err)
	}
	if err := created.Close(); !errors.Is(err, driver.ErrClosed) {
		t.Fatalf("failed client was not closed: %v", err)
	}
}

func TestCacheMissAndLegacyCompatibility(t *testing.T) {
	failure := errors.New("backend unavailable")
	for _, scenario := range []struct {
		name  string
		value string
		err   error
	}{
		{"hit", "value", nil},
		{"empty hit", "", nil},
		{"miss", "", driver.Nil},
		{"failure", "", failure},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			client := driver.NewClient(&driver.Options{Addr: "unused:6379"})
			t.Cleanup(func() { client.Close() })
			client.AddHook(commandHook{process: func(_ context.Context, cmd driver.Cmder) error {
				cmd.(*driver.StringCmd).SetVal(scenario.value)
				return scenario.err
			}})
			provider := NewCache(client)
			cache.InitCache(provider)
			t.Cleanup(func() { cache.InitCache(nil) })
			for _, get := range []func(context.Context, string) ([]byte, error){provider.GetContext, cache.GetContext} {
				value, err := get(context.Background(), "key")
				if string(value) != scenario.value || !errors.Is(err, scenario.err) {
					t.Fatalf("读取返回 = %q, %v", value, err)
				}
				if got := errors.Is(err, cache.ErrMiss); got != (scenario.err == driver.Nil) {
					t.Fatalf("ErrMiss = %v，错误 = %v", got, err)
				}
			}
			// 旧入口不仅保留 errors.Is，还保留直接比较底层错误的行为。
			for _, get := range []func(string) ([]byte, error){provider.Get, provider.WithContext(context.Background()).Get, cache.Get} {
				if _, err := get("key"); err != scenario.err {
					t.Fatalf("旧入口错误 = %v，期望 %v", err, scenario.err)
				}
			}
		})
	}
}

func TestCacheFacadeCancellationDuringGet(t *testing.T) {
	client := driver.NewClient(&driver.Options{Addr: "unused:6379"})
	t.Cleanup(func() { client.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client.AddHook(commandHook{process: func(got context.Context, _ driver.Cmder) error {
		if got != ctx {
			t.Error("缓存入口丢失请求 Context")
		}
		cancel()
		<-got.Done()
		return got.Err()
	}})
	cache.InitCache(NewCache(client))
	t.Cleanup(func() { cache.InitCache(nil) })
	if _, err := cache.GetContext(ctx, "key"); !errors.Is(err, context.Canceled) || errors.Is(err, cache.ErrMiss) {
		t.Fatalf("操作中取消返回 = %v", err)
	}
}

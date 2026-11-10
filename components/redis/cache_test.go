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

func TestCacheRequestContextAndConfiguredExpiry(t *testing.T) {
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
			args := cmd.Args()
			if len(args) != 5 || args[3] != "ex" || args[4] != int64(60) {
				t.Errorf("写入参数 = %v，预期使用构造时配置的 60 秒有效期", args)
			}
			cmd.SetVal("OK")
		case *driver.IntCmd:
			cmd.SetVal(1)
		}
		return nil
	}})
	shared := NewCacheWithExpiry(client, time.Minute)
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
			value, err := provider.GetContext(context.Background(), "key")
			if string(value) != scenario.value || !errors.Is(err, scenario.err) {
				t.Fatalf("读取返回 = %q, %v", value, err)
			}
			if got := errors.Is(err, cache.ErrMiss); got != (scenario.err == driver.Nil) {
				t.Fatalf("ErrMiss = %v，错误 = %v", got, err)
			}
			// 旧入口不仅保留 errors.Is，还保留直接比较底层错误的行为。
			for _, get := range []func(string) ([]byte, error){provider.Get, provider.WithContext(context.Background()).Get} {
				if _, err := get("key"); err != scenario.err {
					t.Fatalf("旧入口错误 = %v，期望 %v", err, scenario.err)
				}
			}
		})
	}
}

func TestCacheCancellationDuringGet(t *testing.T) {
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
	if _, err := NewCache(client).GetContext(ctx, "key"); !errors.Is(err, context.Canceled) || errors.Is(err, cache.ErrMiss) {
		t.Fatalf("操作中取消返回 = %v", err)
	}
}

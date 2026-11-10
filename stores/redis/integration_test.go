package redis

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	driver "github.com/redis/go-redis/v9"
)

func TestRedisIntegrationDefaultClient(t *testing.T) {
	addr := os.Getenv("HOLY_REDIS_ADDR")
	if addr == "" {
		if os.Getenv("HOLY_REQUIRE_REDIS") == "1" {
			t.Fatal("HOLY_REDIS_ADDR is required")
		}
		t.Skip("HOLY_REDIS_ADDR is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	canceled, stop := context.WithCancel(ctx)
	stop()
	// 测试结束后恢复初始状态，支持在同一进程中重复运行测试。
	t.Cleanup(func() {
		if err := Close(); err != nil {
			t.Error(err)
		}
		defaultClient = nil
		defaultClosed.Store(false)
		defaultState.Store(initUnstarted)
	})
	opts := &driver.Options{Addr: addr}
	if state := defaultState.Load(); state != initUnstarted {
		t.Fatalf("初始状态 = %d，预期未启动", state)
	}
	if err := InitRedisContext(canceled, opts); !errors.Is(err, context.Canceled) {
		t.Fatalf("首次初始化取消后返回 %v", err)
	}
	if state := defaultState.Load(); state != initFailed || DefaultClient() != nil {
		t.Fatalf("取消后状态 = %d，预期启动失败且无默认客户端", state)
	}

	starting := make(chan struct{}, 1)
	resume := make(chan struct{})
	startingOpts := &driver.Options{Addr: addr, OnConnect: func(_ context.Context, _ *driver.Conn) error {
		select {
		case starting <- struct{}{}:
		default:
		}
		<-resume
		return nil
	}}
	results := make(chan error, 2)
	go func() {
		results <- InitRedisContext(ctx, startingOpts)
	}()
	select {
	case <-starting:
		if state := defaultState.Load(); state != initStarting || DefaultClient() != nil {
			t.Errorf("连接验证期间状态 = %d，预期启动中且无默认客户端", state)
		}
		if err := InitRedisContext(ctx, opts); !errors.Is(err, ErrInitializing) {
			t.Errorf("启动中再次初始化返回 %v，预期 ErrInitializing", err)
		}
	case err := <-results:
		close(resume)
		t.Fatalf("初始化在进入连接验证前返回 %v", err)
	}
	close(resume)
	if err := <-results; err != nil {
		t.Fatal(err)
	}
	fixed := DefaultClient()
	if state := defaultState.Load(); state != initStarted || fixed == nil {
		t.Fatalf("验证成功后状态 = %d，预期启动完成且已发布默认客户端", state)
	}
	if err := InitRedis(addr, ""); !errors.Is(err, ErrAlreadyInitialized) || DefaultClient() != fixed {
		t.Fatalf("重复初始化返回 %v，默认客户端应保持原实例", err)
	}
	if err := fixed.Ping(ctx).Err(); err != nil {
		t.Fatalf("默认客户端不可用: %v", err)
	}
	for range 2 {
		go func() {
			results <- Close()
		}()
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Errorf("并发关闭返回 %v", err)
		}
	}
	if err := fixed.Ping(ctx).Err(); !errors.Is(err, driver.ErrClosed) {
		t.Fatalf("关闭后原客户端返回 %v，预期 ErrClosed", err)
	}
	if err := Close(); err != nil || DefaultClient() != nil {
		t.Fatal("default close is not idempotent")
	}
	if state := defaultState.Load(); state != initStarted {
		t.Fatalf("关闭后状态 = %d，预期保留启动完成状态", state)
	}
	if err := InitRedisContext(ctx, opts); !errors.Is(err, ErrAlreadyInitialized) || DefaultClient() != nil {
		t.Fatalf("关闭后初始化返回 %v，默认客户端应为空", err)
	}
}

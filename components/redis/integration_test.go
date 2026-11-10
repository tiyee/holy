package redis_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	driver "github.com/redis/go-redis/v9"
	cacheapi "github.com/tiyee/holy/components/cache"
	rediscache "github.com/tiyee/holy/components/redis"
	redisstore "github.com/tiyee/holy/stores/redis"
)

func TestRedisIntegration(t *testing.T) {
	addr := os.Getenv("HOLY_REDIS_ADDR")
	if addr == "" {
		if os.Getenv("HOLY_REQUIRE_REDIS") == "1" {
			t.Fatal("HOLY_REDIS_ADDR is required")
		}
		t.Skip("HOLY_REDIS_ADDR is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := redisstore.Open(ctx, &driver.Options{Addr: addr})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	cache := rediscache.NewCache(client)
	key := fmt.Sprintf("holy:test:%d", time.Now().UnixNano())
	defer client.Del(context.Background(), key)
	if err := cache.SetEContext(ctx, key, []byte("value"), time.Minute); err != nil {
		t.Fatal(err)
	}
	if got, err := cache.GetContext(ctx, key); err != nil || string(got) != "value" {
		t.Fatalf("get: %q %v", got, err)
	}
	if ttl := client.PTTL(ctx, key).Val(); ttl <= 0 || ttl > time.Minute {
		t.Fatalf("expiration: %s", ttl)
	}
	canceled, stop := context.WithCancel(ctx)
	stop()
	if _, err := cache.GetContext(canceled, key); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := cache.WithContext(ctx).Delete(key); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.GetContext(ctx, key); !errors.Is(err, cacheapi.ErrMiss) || !errors.Is(err, driver.Nil) {
		t.Fatal(err)
	}
	if _, err := cache.Get(key); err != driver.Nil {
		t.Fatalf("legacy get miss: %v", err)
	}
}

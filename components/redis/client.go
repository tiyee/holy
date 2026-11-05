package redis

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Client 为启动期兼容入口；新代码应显式持有 Open 返回的连接并负责关闭。
var Client *redis.Client
var clientMu sync.Mutex
var ownedClient *redis.Client

// Open 在发布客户端前验证连接，失败自动清理资源。
func Open(ctx context.Context, opts *redis.Options) (*redis.Client, error) {
	return open(ctx, opts, redis.NewClient)
}

func open(ctx context.Context, opts *redis.Options, newClient func(*redis.Options) *redis.Client) (*redis.Client, error) {
	if ctx == nil || opts == nil {
		return nil, errors.New("redis: context and options are required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	copyOptions := *opts
	copyOptions.ContextTimeoutEnabled = true
	client := newClient(&copyOptions)
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, errors.Join(err, client.Close())
	}
	return client, nil
}

func InitRedis(addr, password string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return InitRedisContext(ctx, &redis.Options{Addr: addr, Password: password})
}

func InitRedisContext(ctx context.Context, opts *redis.Options) error {
	client, err := Open(ctx, opts)
	if err != nil {
		return err
	}
	clientMu.Lock()
	old := ownedClient
	ownedClient, Client = client, client
	clientMu.Unlock()
	if old != nil {
		return old.Close()
	}
	return nil
}

func DefaultClient() *redis.Client {
	clientMu.Lock()
	defer clientMu.Unlock()
	return Client
}

// Close 只关闭兼容初始化入口拥有的默认连接。
func Close() error {
	clientMu.Lock()
	old := ownedClient
	ownedClient, Client = nil, nil
	clientMu.Unlock()
	if old != nil {
		return old.Close()
	}
	return nil
}

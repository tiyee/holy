// Package redis 管理 Redis 客户端及其生命周期。
package redis

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrAlreadyInitialized 表示默认客户端已成功初始化过。
var ErrAlreadyInitialized = errors.New("redis: default client already initialized")

// ErrInitializing 表示默认客户端正在初始化。
var ErrInitializing = errors.New("redis: default client is initializing")

const (
	initUnstarted uint32 = iota
	initStarting
	initStarted
	initFailed
)

var defaultState atomic.Uint32
var defaultClosed atomic.Bool

// defaultClient 在发布 initStarted 前赋值，之后只读。
var defaultClient *redis.Client

// Open 验证连接后返回由调用方负责关闭的客户端。失败时自动清理资源。
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

// InitRedis 使用 5 秒超时初始化默认客户端。
func InitRedis(addr, password string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return InitRedisContext(ctx, &redis.Options{Addr: addr, Password: password})
}

// InitRedisContext 在应用启动时初始化默认客户端。
// 初始化期间再次调用返回 ErrInitializing。
// 验证失败可重试，首次成功后的调用返回 ErrAlreadyInitialized。
func InitRedisContext(ctx context.Context, opts *redis.Options) error {
	for {
		state := defaultState.Load()
		switch state {
		case initStarting:
			return ErrInitializing
		case initStarted:
			return ErrAlreadyInitialized
		}
		if defaultState.CompareAndSwap(state, initStarting) {
			break
		}
	}
	// Open 提前退出时将启动中标记为失败。
	defer defaultState.CompareAndSwap(initStarting, initFailed)

	client, err := Open(ctx, opts)
	if err != nil {
		return err
	}
	defaultClient = client
	defaultState.Store(initStarted)
	return nil
}

// DefaultClient 返回借用的默认客户端，初始化成功前或关闭后返回 nil。
func DefaultClient() *redis.Client {
	if defaultState.Load() == initStarted && !defaultClosed.Load() {
		return defaultClient
	}
	return nil
}

// Close 幂等地关闭默认客户端，已成功初始化的默认客户端关闭后仍不可重新初始化。
func Close() error {
	if defaultState.Load() == initStarted && defaultClosed.CompareAndSwap(false, true) {
		return defaultClient.Close()
	}
	return nil
}

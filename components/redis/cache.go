package redis

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/tiyee/holy/components/cache"
)

var ErrNoClient = errors.New("redis: client is not configured")

type Cache struct {
	client     *redis.Client
	ctx        context.Context
	defaultExp *atomic.Int64
}

var _ cache.ICacheE = (*Cache)(nil)
var _ cache.ICacheContext = (*Cache)(nil)

// NewCache 借用连接，生命周期由调用方管理；请求 Context 通过每次操作传入。
func NewCache(client *redis.Client) *Cache { return NewRedisHandle(context.Background(), client) }

// NewRedisHandle 保留绑定 Context 的兼容入口，新代码优先使用 NewCache。
func NewRedisHandle(ctx context.Context, client *redis.Client) *Cache {
	if ctx == nil {
		ctx = context.Background()
	}
	exp := new(atomic.Int64)
	exp.Store(int64(24 * time.Hour))
	return &Cache{client: client, ctx: ctx, defaultExp: exp}
}

// WithContext 返回请求专用视图，不会修改共享 Cache 的 Context。
func (r *Cache) WithContext(ctx context.Context) *Cache {
	if ctx == nil {
		ctx = context.Background()
	}
	return &Cache{client: r.client, ctx: ctx, defaultExp: r.defaultExp}
}
func (r *Cache) SetDefaultExp(exp time.Duration) { r.defaultExp.Store(int64(exp)) }
func (r *Cache) validate(ctx context.Context) error {
	if ctx == nil {
		return errors.New("redis: nil context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if r == nil || r.client == nil {
		return ErrNoClient
	}
	return nil
}
func (r *Cache) get(ctx context.Context, key string) ([]byte, error) {
	if err := r.validate(ctx); err != nil {
		return nil, err
	}
	return r.client.Get(ctx, key).Bytes()
}

// GetContext 将未命中标记为 cache.ErrMiss，并保留底层错误供 errors.Is 判断。
func (r *Cache) GetContext(ctx context.Context, key string) ([]byte, error) {
	value, err := r.get(ctx, key)
	if errors.Is(err, redis.Nil) {
		return value, errors.Join(cache.ErrMiss, err)
	}
	return value, err
}
func (r *Cache) SetContext(ctx context.Context, key string, value []byte) error {
	if err := r.validate(ctx); err != nil {
		return err
	}
	expiration := 24 * time.Hour
	if r.defaultExp != nil {
		expiration = time.Duration(r.defaultExp.Load())
	}
	return r.SetEContext(ctx, key, value, expiration)
}
func (r *Cache) DeleteContext(ctx context.Context, key string) error {
	if err := r.validate(ctx); err != nil {
		return err
	}
	return r.client.Del(ctx, key).Err()
}
func (r *Cache) SetEContext(ctx context.Context, key string, value []byte, exp time.Duration) error {
	if err := r.validate(ctx); err != nil {
		return err
	}
	return r.client.Set(ctx, key, value, exp).Err()
}

// Get 保留兼容行为，未命中仍直接返回 redis.Nil；新代码使用 GetContext。
func (r *Cache) Get(key string) ([]byte, error)     { return r.get(r.ctx, key) }
func (r *Cache) Set(key string, value []byte) error { return r.SetContext(r.ctx, key, value) }
func (r *Cache) Delete(key string) error            { return r.DeleteContext(r.ctx, key) }
func (r *Cache) SetE(key string, value []byte, exp time.Duration) error {
	return r.SetEContext(r.ctx, key, value, exp)
}

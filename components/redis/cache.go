package redis

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/tiyee/holy/components/cache"
)

var ErrNoClient = errors.New("redis: client is not configured")

type Cache struct {
	client     *redis.Client
	ctx        context.Context
	defaultExp time.Duration
}

var _ cache.ICacheE = (*Cache)(nil)
var _ cache.ICacheContext = (*Cache)(nil)

// NewCache 借用连接并使用 24 小时的默认过期时间，连接由调用方管理。
func NewCache(client *redis.Client) *Cache { return NewCacheWithExpiry(client, 24*time.Hour) }

// NewCacheWithExpiry 在构造时指定默认过期时间，0 表示不过期。
func NewCacheWithExpiry(client *redis.Client, exp time.Duration) *Cache {
	return &Cache{client: client, ctx: context.Background(), defaultExp: exp}
}

// NewRedisHandle 保留绑定 Context 的兼容入口，新代码优先使用 NewCache。
func NewRedisHandle(ctx context.Context, client *redis.Client) *Cache {
	return NewCache(client).WithContext(ctx)
}

// WithContext 返回请求专用视图，不会修改共享 Cache 的 Context。
func (r *Cache) WithContext(ctx context.Context) *Cache {
	if ctx == nil {
		ctx = context.Background()
	}
	view := *r
	view.ctx = ctx
	return &view
}

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
	return r.client.Set(ctx, key, value, r.defaultExp).Err()
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

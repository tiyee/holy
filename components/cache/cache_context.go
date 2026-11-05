package cache

import (
	"context"
	"time"
)

// currentContext 获取同一提供者快照，不向无 Context 接口降级。
func currentContext(ctx context.Context) (ICacheContext, error) {
	if ctx == nil {
		return nil, ErrInvalidContext
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c, err := current()
	if err != nil {
		return nil, err
	}
	contextual, ok := c.(ICacheContext)
	if !ok {
		return nil, ErrUnsupported
	}
	return contextual, nil
}

// GetContext 使用请求 Context 读取缓存，未命中可用 errors.Is(err, ErrMiss) 判断。
func GetContext(ctx context.Context, key string) ([]byte, error) {
	c, err := currentContext(ctx)
	if err != nil {
		return nil, err
	}
	return c.GetContext(ctx, key)
}

// SetContext 使用请求 Context 和提供者的默认过期时间写入缓存。
func SetContext(ctx context.Context, key string, value []byte) error {
	c, err := currentContext(ctx)
	if err != nil {
		return err
	}
	return c.SetContext(ctx, key, value)
}

// DeleteContext 使用请求 Context 删除缓存。
func DeleteContext(ctx context.Context, key string) error {
	c, err := currentContext(ctx)
	if err != nil {
		return err
	}
	return c.DeleteContext(ctx, key)
}

// SetEContext 使用请求 Context 和显式过期时间写入缓存。
func SetEContext(ctx context.Context, key string, value []byte, exp time.Duration) error {
	c, err := currentContext(ctx)
	if err != nil {
		return err
	}
	return c.SetEContext(ctx, key, value, exp)
}

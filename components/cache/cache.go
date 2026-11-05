package cache

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"time"
)

type ICache interface {
	Get(key string) ([]byte, error)
	Set(key string, value []byte) error
	Delete(key string) error
}

// ICacheContext 允许共享缓存实例使用各请求独立的取消、截止时间。
// GetContext 未命中时应返回可通过 errors.Is(err, ErrMiss) 判断的错误。
type ICacheContext interface {
	GetContext(context.Context, string) ([]byte, error)
	SetContext(context.Context, string, []byte) error
	DeleteContext(context.Context, string) error
	SetEContext(context.Context, string, []byte, time.Duration) error
}

var ErrNotInitialized = errors.New("cache: provider is not initialized")
var ErrUnsupported = errors.New("cache: operation is not supported by provider")

// ErrMiss 表示键不存在；空值仍视为命中，连接或服务故障不应返回此错误。
var ErrMiss = errors.New("cache: key not found")

// ErrInvalidContext 表示 Context 操作收到了 nil Context。
var ErrInvalidContext = errors.New("cache: nil context")

var providerMu sync.RWMutex
var provider ICache

// InitCache 统一安装所有能力；InitCacheE/M 同样会更新基础操作的提供者。
func InitCache(c ICache) {
	if c != nil {
		value := reflect.ValueOf(c)
		switch value.Kind() {
		case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Interface, reflect.Chan:
			if value.IsNil() {
				c = nil
			}
		}
	}
	providerMu.Lock()
	provider = c
	providerMu.Unlock()
}
func current() (ICache, error) {
	providerMu.RLock()
	c := provider
	providerMu.RUnlock()
	if c == nil {
		return nil, ErrNotInitialized
	}
	return c, nil
}
func Get(key string) ([]byte, error) {
	c, err := current()
	if err != nil {
		return nil, err
	}
	return c.Get(key)
}
func Set(key string, value []byte) error {
	c, err := current()
	if err != nil {
		return err
	}
	return c.Set(key, value)
}
func Delete(key string) error {
	c, err := current()
	if err != nil {
		return err
	}
	return c.Delete(key)
}

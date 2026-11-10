package cache

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
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

// ErrAlreadyInitialized 表示缓存提供者已注册。
var ErrAlreadyInitialized = errors.New("cache: provider already initialized")

// ErrInvalidProvider 表示注册时未传入有效的缓存提供者。
var ErrInvalidProvider = errors.New("cache: provider is nil")

// ErrMiss 表示键不存在；空值仍视为命中，连接或服务故障不应返回此错误。
var ErrMiss = errors.New("cache: key not found")

// ErrInvalidContext 表示 Context 操作收到了 nil Context。
var ErrInvalidContext = errors.New("cache: nil context")

const (
	initUnstarted uint32 = iota
	initStarting
	initStarted
	initFailed
)

var defaultState atomic.Uint32

// defaultCache 在发布 initStarted 前赋值，之后只读。
var defaultCache ICache

// InitCache 在应用启动时一次性注册缓存提供者，各项操作共享该提供者。
func InitCache(c ICache) error {
	if isNilProvider(c) {
		defaultState.CompareAndSwap(initUnstarted, initFailed)
		return ErrInvalidProvider
	}
	if !defaultState.CompareAndSwap(initUnstarted, initStarting) &&
		!defaultState.CompareAndSwap(initFailed, initStarting) {
		return ErrAlreadyInitialized
	}
	defaultCache = c
	defaultState.Store(initStarted)
	return nil
}

func isNilProvider(c ICache) bool {
	value := reflect.ValueOf(c)
	switch value.Kind() {
	case reflect.Invalid:
		return true
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return value.IsNil()
	default:
		return false
	}
}

func current() (ICache, error) {
	if defaultState.Load() != initStarted {
		return nil, ErrNotInitialized
	}
	return defaultCache, nil
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

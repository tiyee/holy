package cache

import (
	"errors"
	"testing"
	"time"
)

type testCache struct{}

func (*testCache) Get(string) ([]byte, error)                   { return []byte("value"), nil }
func (*testCache) Set(string, []byte) error                     { return nil }
func (*testCache) Delete(string) error                          { return nil }
func (*testCache) SetE(string, []byte, time.Duration) error     { return nil }
func (*testCache) MSet(map[string][]byte) error                 { return nil }
func (*testCache) MSetE(map[string][]byte, time.Duration) error { return nil }
func (*testCache) MGet(keys ...string) ([][]byte, error)        { return make([][]byte, len(keys)), nil }
func (*testCache) MDelete(...string) error                      { return nil }

type basicCache struct{ ICache }

func resetTestCache(t *testing.T) {
	t.Helper()
	previousState := defaultState.Swap(initUnstarted)
	previousCache := defaultCache
	defaultCache = nil
	t.Cleanup(func() {
		defaultCache = previousCache
		defaultState.Store(previousState)
	})
}

func TestProviderCapabilitiesShareOneInstallation(t *testing.T) {
	resetTestCache(t)
	var empty *testCache
	for _, invalid := range []ICache{nil, empty} {
		if err := InitCache(invalid); !errors.Is(err, ErrInvalidProvider) {
			t.Fatalf("注册空提供者返回 %v", err)
		}
	}
	if state := defaultState.Load(); state != initFailed {
		t.Fatalf("空提供者注册后状态 = %d，预期失败", state)
	}
	if _, err := Get("key"); !errors.Is(err, ErrNotInitialized) {
		t.Fatal(err)
	}
	if err := InitCache(&testCache{}); err != nil {
		t.Fatal(err)
	}
	if err := InitCache(basicCache{&testCache{}}); !errors.Is(err, ErrAlreadyInitialized) {
		t.Fatalf("重复注册返回 %v", err)
	}
	if err := InitCache(empty); !errors.Is(err, ErrInvalidProvider) {
		t.Fatalf("用空提供者重置返回 %v", err)
	}
	if data, err := Get("key"); err != nil || string(data) != "value" {
		t.Fatalf("读取返回 %q, %v", data, err)
	}
	if err := SetE("key", nil, time.Minute); err != nil {
		t.Fatal(err)
	}
	if data, err := MGet("a", "b"); err != nil || len(data) != 2 {
		t.Fatal(err)
	}
}

func TestProviderUnsupportedCapabilities(t *testing.T) {
	resetTestCache(t)
	if err := InitCache(basicCache{&testCache{}}); err != nil {
		t.Fatal(err)
	}
	if err := SetE("key", nil, time.Minute); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
	if err := MDelete("key"); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
}

func TestConcurrentProviderRegistration(t *testing.T) {
	resetTestCache(t)
	type result struct {
		provider ICache
		err      error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			provider := &basicCache{&testCache{}}
			results <- result{provider, InitCache(provider)}
		}()
	}
	winner, other := <-results, <-results
	if winner.err != nil {
		winner, other = other, winner
	}
	if winner.err != nil || !errors.Is(other.err, ErrAlreadyInitialized) {
		t.Fatalf("并发注册返回 %v、%v，预期仅一次成功", winner.err, other.err)
	}
	if actual, err := current(); err != nil || actual != winner.provider {
		t.Fatalf("当前提供者 = %v, %v，预期为首次成功注册的提供者", actual, err)
	}
}

package cache

import (
	"errors"
	"sync"
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

func TestProviderCapabilitiesShareOneInstallation(t *testing.T) {
	t.Cleanup(func() { InitCache(nil) })
	InitCacheM(&testCache{})
	if data, err := Get("key"); err != nil || string(data) != "value" {
		t.Fatal(err)
	}
	if err := SetE("key", nil, time.Minute); err != nil {
		t.Fatal(err)
	}
	if data, err := MGet("a", "b"); err != nil || len(data) != 2 {
		t.Fatal(err)
	}
	InitCache(basicCache{&testCache{}})
	if err := SetE("key", nil, time.Minute); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
	if err := MDelete("key"); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
	var empty *testCache
	InitCache(empty)
	if _, err := Get("key"); !errors.Is(err, ErrNotInitialized) {
		t.Fatal(err)
	}
}
func TestProviderConcurrentReplacement(t *testing.T) {
	t.Cleanup(func() { InitCache(nil) })
	InitCacheE(&testCache{})
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 100 {
				InitCacheM(&testCache{})
				if err := Set("key", nil); err != nil {
					t.Error(err)
				}
				if err := MSet(nil); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
}

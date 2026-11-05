package cache

import "time"

type ICacheE interface {
	ICache
	SetE(key string, value []byte, exp time.Duration) error
}

func InitCacheE(c ICacheE) { InitCache(c) }
func SetE(key string, value []byte, exp time.Duration) error {
	c, err := current()
	if err != nil {
		return err
	}
	extended, ok := c.(ICacheE)
	if !ok {
		return ErrUnsupported
	}
	return extended.SetE(key, value, exp)
}

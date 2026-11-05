package cache

import "time"

type ICacheM interface {
	ICacheE
	MSet(map[string][]byte) error
	MSetE(map[string][]byte, time.Duration) error
	MGet(...string) ([][]byte, error)
	MDelete(...string) error
}

func InitCacheM(c ICacheM) { InitCache(c) }
func multi() (ICacheM, error) {
	c, err := current()
	if err != nil {
		return nil, err
	}
	m, ok := c.(ICacheM)
	if !ok {
		return nil, ErrUnsupported
	}
	return m, nil
}
func MSet(values map[string][]byte) error {
	c, err := multi()
	if err != nil {
		return err
	}
	return c.MSet(values)
}
func MSetE(values map[string][]byte, exp time.Duration) error {
	c, err := multi()
	if err != nil {
		return err
	}
	return c.MSetE(values, exp)
}
func MGet(keys ...string) ([][]byte, error) {
	c, err := multi()
	if err != nil {
		return nil, err
	}
	return c.MGet(keys...)
}
func MDelete(keys ...string) error {
	c, err := multi()
	if err != nil {
		return err
	}
	return c.MDelete(keys...)
}

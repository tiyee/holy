package aes_cbc

import "sync/atomic"

var cryptor atomic.Pointer[AesCbc]

// Configure 校验成功后原子替换默认实例；失败不改变当前配置。
func Configure(key []byte) error {
	c, err := NewWithKey(key)
	if err != nil {
		return err
	}
	cryptor.Store(c)
	return nil
}

// Reset 兼容旧调用方式；iv 已由随机 nonce 替代。
func Reset(key, iv string) error { return Configure([]byte(key)) }

// New 返回默认实例的快照；未初始化时加解密返回 ErrNotInitialized。
func New() *AesCbc {
	if c := cryptor.Load(); c != nil {
		return c
	}
	return &AesCbc{err: ErrNotInitialized}
}

func Decrypt(data []byte) ([]byte, error) { return New().Decrypt(data) }
func Encrypt(data []byte) ([]byte, error) { return New().Encrypt(data) }

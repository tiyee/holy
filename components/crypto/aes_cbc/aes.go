// Package aes_cbc 保留旧包路径，新密文统一使用带版本标识的 AES-GCM。
package aes_cbc

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/des"
	"crypto/rand"
	"errors"
)

var (
	ErrNotInitialized    = errors.New("crypto: cipher is not initialized")
	ErrInvalidCiphertext = errors.New("crypto: invalid or unauthenticated ciphertext")
	ErrInvalidPadding    = errors.New("crypto: invalid padding")
)

const formatVersion = "holy:gcm:1:"

// AesCbc 为兼容旧名称保留；实例不可变，可并发使用。
type AesCbc struct {
	aead cipher.AEAD
	err  error
}

// NewWithKey 校验 16、24 或 32 字节 AES 密钥；nonce 每次加密随机生成。
func NewWithKey(key []byte) (*AesCbc, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &AesCbc{aead: aead}, nil
}

// NewAesCbc 兼容旧构造签名，iv 不再使用；非法密钥在加解密时返回错误。
// Deprecated: 使用可立即返回密钥错误的 NewWithKey。
func NewAesCbc(key, iv string) *AesCbc {
	c, err := NewWithKey([]byte(key))
	if err != nil {
		return &AesCbc{err: err}
	}
	return c
}

func (ac *AesCbc) ready() error {
	if ac == nil {
		return ErrNotInitialized
	}
	if ac.err != nil {
		return ac.err
	}
	if ac.aead == nil {
		return ErrNotInitialized
	}
	return nil
}

func (ac *AesCbc) Encrypt(plaintext []byte) ([]byte, error) {
	if err := ac.ready(); err != nil {
		return nil, err
	}
	nonce := make([]byte, ac.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(formatVersion)+len(nonce)+len(plaintext)+ac.aead.Overhead())
	out = append(out, formatVersion...)
	out = append(out, nonce...)
	return ac.aead.Seal(out, nonce, plaintext, []byte(formatVersion)), nil
}

func (ac *AesCbc) Decrypt(data []byte) ([]byte, error) {
	if err := ac.ready(); err != nil {
		return nil, err
	}
	header := len(formatVersion)
	if len(data) < header+ac.aead.NonceSize()+ac.aead.Overhead() || !bytes.HasPrefix(data, []byte(formatVersion)) {
		return nil, ErrInvalidCiphertext
	}
	nonce := data[header : header+ac.aead.NonceSize()]
	plaintext, err := ac.aead.Open(nil, nonce, data[header+len(nonce):], []byte(formatVersion))
	if err != nil {
		return nil, ErrInvalidCiphertext
	}
	return plaintext, nil
}

// DecryptLegacyDES 仅用于显式迁移历史数据；旧 DES-CBC 密文不具备真实性保证。
// 常规 Decrypt 不会自动回退到旧算法，避免认证失败后的降级。
func DecryptLegacyDES(data []byte, key, iv string) ([]byte, error) {
	block, err := des.NewCipher([]byte(key))
	if err != nil {
		return nil, err
	}
	if len(iv) != block.BlockSize() || len(data) == 0 || len(data)%block.BlockSize() != 0 {
		return nil, ErrInvalidCiphertext
	}
	out := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, []byte(iv)).CryptBlocks(out, data)
	return UnpadPKCS7(out, block.BlockSize())
}

// PKCS5Padding 兼容旧辅助函数，不会修改调用方的底层切片。
func PKCS5Padding(data []byte, blockSize int) []byte {
	if blockSize < 1 || blockSize > 255 {
		return nil
	}
	padding := blockSize - len(data)%blockSize
	out := make([]byte, len(data)+padding)
	copy(out, data)
	copy(out[len(data):], bytes.Repeat([]byte{byte(padding)}, padding))
	return out
}

// UnpadPKCS7 完整校验长度、填充数量及所有填充字节。
func UnpadPKCS7(data []byte, blockSize int) ([]byte, error) {
	if blockSize < 1 || blockSize > 255 || len(data) == 0 || len(data)%blockSize != 0 {
		return nil, ErrInvalidPadding
	}
	n := int(data[len(data)-1])
	if n < 1 || n > blockSize || n > len(data) {
		return nil, ErrInvalidPadding
	}
	if !bytes.Equal(data[len(data)-n:], bytes.Repeat([]byte{byte(n)}, n)) {
		return nil, ErrInvalidPadding
	}
	return data[:len(data)-n], nil
}

// PKCS5UnPadding 兼容旧签名，非法填充返回 nil；需要错误原因时使用 UnpadPKCS7。
func PKCS5UnPadding(data []byte) []byte {
	out, _ := UnpadPKCS7(data, des.BlockSize)
	return out
}

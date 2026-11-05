package jwt

import (
	"bytes"
	stdhmac "crypto/hmac"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/tiyee/holy/components/crypto/base64"
	"github.com/tiyee/holy/components/crypto/hmac"
)

type IPayload interface {
	Encode() []byte
	Decode([]byte) error
}
type Header struct {
	Algorithm string `json:"alg"`
	Typ       string `json:"typ"`
}
type IOpt[T IPayload] func(j *JWT[T])

var (
	ErrEmptySecret       = errors.New("jwt: secret must not be empty")
	ErrTokenExpired      = errors.New("token expired")
	ErrMissingExpiration = errors.New("token missing expiration")
)

type JWT[T IPayload] struct {
	mu               sync.RWMutex
	header           Header
	secret           []byte
	ignoreExpiration bool
}

// WithIgnoreExpiration controls whether Decode skips expiration validation.
// Signature verification is always performed.
func WithIgnoreExpiration[T IPayload](ignore bool) IOpt[T] {
	return func(j *JWT[T]) {
		j.ignoreExpiration = ignore
	}
}

// New 保留原构造签名，空密钥会以 ErrEmptySecret panic。
// 配置来自外部输入时，优先使用可返回错误的 NewWithSecret。
func New[T IPayload](secret []byte, opts ...IOpt[T]) *JWT[T] {
	j, err := NewWithSecret[T](secret, opts...)
	if err != nil {
		panic(err)
	}
	return j
}

// NewWithSecret 拒绝空密钥并复制密钥，避免配置遗漏或外部修改影响验签。
func NewWithSecret[T IPayload](secret []byte, opts ...IOpt[T]) (*JWT[T], error) {
	if len(secret) == 0 {
		return nil, ErrEmptySecret
	}
	jwt := &JWT[T]{
		header: Header{
			Algorithm: "HS256",
			Typ:       "JWT",
		},
		secret: bytes.Clone(secret),
	}
	for _, opt := range opts {
		opt(jwt)
	}
	return jwt, nil
}
func (j *JWT[T]) SetHeader(h Header) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if h.Algorithm != "HS256" || (h.Typ != "" && h.Typ != "JWT") {
		panic("jwt: only HS256 JWT headers are supported")
	}
	j.header = h
}
func (j *JWT[T]) Header() Header {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.header
}

// Encode 保留原返回值；未配置密钥的实例会以 ErrEmptySecret panic。
func (j *JWT[T]) Encode(pl T) []byte {
	if j == nil || len(j.secret) == 0 {
		panic(ErrEmptySecret)
	}
	var header, payload []byte
	if bs, err := json.Marshal(j.Header()); err == nil {
		header = base64.UrlEncode(bs)
	}

	payload = base64.UrlEncode(pl.Encode())
	body := make([]byte, 0, len(header)+1+len(payload)+1+len(j.secret))
	body = append(body, header...)
	body = append(body, '.')
	body = append(body, payload...)

	signature := hmac.Sha256Encrypt(body, j.secret)

	body = append(body, '.')
	body = append(body, signature...)
	buff := bytes.NewBufferString("Bearer ")
	buff.Write(body)
	return buff.Bytes()

}
func (j *JWT[T]) Decode(bs []byte, pl T) error {
	if j == nil || len(j.secret) == 0 {
		return ErrEmptySecret
	}
	if bytes.Index(bs, []byte("Bearer ")) != 0 {
		return errors.New("invalid token")
	}
	bs = bs[7:]
	jwt := bytes.Split(bs, []byte{'.'})
	if len(jwt) != 3 {
		return errors.New("invalid token")
	}
	var header Header
	if bs, err := base64.UrlDecode(jwt[0]); err == nil {
		if err := json.Unmarshal(bs, &header); err != nil {
			return err
		}
	} else {
		return err
	}
	if header.Algorithm != "HS256" || (header.Typ != "" && header.Typ != "JWT") {
		return errors.New("jwt: unsupported token header")
	}
	body := [][]byte{jwt[0], jwt[1]}
	if signature := hmac.Sha256Encrypt(bytes.Join(body, []byte{'.'}), j.secret); stdhmac.Equal(signature, jwt[2]) {
		if bs, err := base64.UrlDecode(jwt[1]); err == nil {
			if !j.ignoreExpiration {
				if err := validateExpiration(bs); err != nil {
					return err
				}
			}
			if err := pl.Decode(bs); err == nil {
				return nil
			} else {
				return err
			}
		} else {
			return err
		}

	} else {
		return errors.New("dismatch token")
	}
}

// validateExpiration requires an exp claim expressed as integer Unix seconds.
func validateExpiration(payload []byte) error {
	var claims struct {
		ExpiresAt *int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return err
	}
	if claims.ExpiresAt == nil {
		return ErrMissingExpiration
	}
	if *claims.ExpiresAt <= time.Now().Unix() {
		return ErrTokenExpired
	}
	return nil
}

func Encode[T IPayload](payload T, secret []byte) []byte {
	j := New[T](secret)
	return j.Encode(payload)
}
func Decode[T IPayload](bs []byte, secret []byte, payload T, opts ...IOpt[T]) error {
	j, err := NewWithSecret[T](secret, opts...)
	if err != nil {
		return err
	}
	return j.Decode(bs, payload)
}

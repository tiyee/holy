package jwt

import (
	"encoding/base64"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/tiyee/holy/components/crypto/hmac"
)

func TestEmptySecretRejected(t *testing.T) {
	// 直接构造空密钥签名，确保校验入口拒绝真实可验签的伪造 token。
	body := []byte(base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`)) + "." +
		base64.RawURLEncoding.EncodeToString([]byte(`{"u":999,"exp":9999999999}`)))
	token := append([]byte("Bearer "), body...)
	token = append(token, '.')
	token = append(token, hmac.Sha256Encrypt(body, nil)...)
	for _, tc := range []struct {
		name   string
		secret []byte
	}{{"nil", nil}, {"empty", []byte{}}} {
		t.Run(tc.name, func(t *testing.T) {
			if j, err := NewWithSecret[*User](tc.secret); j != nil || !errors.Is(err, ErrEmptySecret) {
				t.Fatalf("NewWithSecret = %v, %v", j, err)
			}
			for _, ignore := range []bool{false, true} {
				user := User{Uid: 7}
				if err := Decode(token, tc.secret, &user, WithIgnoreExpiration[*User](ignore)); !errors.Is(err, ErrEmptySecret) {
					t.Fatalf("Decode = %v", err)
				}
				if user != (User{Uid: 7}) {
					t.Fatal("rejected token changed payload")
				}
			}
			for name, fn := range map[string]func(){
				"New":    func() { New[*User](tc.secret) },
				"Encode": func() { Encode(&User{}, tc.secret) },
			} {
				t.Run(name, func(t *testing.T) {
					defer func() {
						if got := recover(); got != ErrEmptySecret {
							t.Fatalf("panic = %v, want ErrEmptySecret", got)
						}
					}()
					fn()
				})
			}
		})
	}
	for name, j := range map[string]*JWT[*User]{"zero": {}, "nil": nil} {
		t.Run(name+" instance", func(t *testing.T) {
			user := User{Uid: 7}
			if err := j.Decode(token, &user); !errors.Is(err, ErrEmptySecret) || user != (User{Uid: 7}) {
				t.Fatalf("Decode = %v, payload = %+v", err, user)
			}
			defer func() {
				if got := recover(); got != ErrEmptySecret {
					t.Fatalf("panic = %v, want ErrEmptySecret", got)
				}
			}()
			j.Encode(&User{})
		})
	}
}

func TestNewWithSecretPreservesOptions(t *testing.T) {
	j, err := NewWithSecret[*User]([]byte("secret"), WithIgnoreExpiration[*User](true))
	if err != nil {
		t.Fatal(err)
	}
	user := User{Uid: 1, Expired: time.Now().Add(-time.Hour).Unix()}
	token := j.Encode(&user)
	var decoded User
	if err := j.Decode(token, &decoded); err != nil || decoded != user {
		t.Fatalf("Decode = %v, payload = %+v", err, decoded)
	}
}

func TestDecodeDoesNotChangeSignerHeader(t *testing.T) {
	j := New[*User]([]byte("secret"))
	before := j.Header()
	for _, header := range []string{`{"alg":"none","typ":"JWT"}`, `{"alg":"HS256","typ":"attacker"}`, `{"alg":"HS512"}`, `{"typ":"JWT"}`} {
		body := []byte(base64.RawURLEncoding.EncodeToString([]byte(header)) + "." + base64.RawURLEncoding.EncodeToString([]byte(`{"exp":9999999999}`)))
		token := append([]byte("Bearer "), body...)
		token = append(token, '.')
		token = append(token, hmac.Sha256Encrypt(body, []byte("secret"))...)
		if err := j.Decode(token, &User{}); err == nil {
			t.Fatalf("unsupported header accepted: %s", header)
		}
		if j.Header() != before {
			t.Fatal("untrusted header changed signer")
		}
	}
	invalid := []byte("Bearer eyJhbGciOiJIUzI1NiJ9.e30.invalid")
	if err := j.Decode(invalid, &User{}); err == nil || j.Header() != before {
		t.Fatal("invalid signature changed signer")
	}
}

func TestSharedJWTConcurrentOperations(t *testing.T) {
	secret := []byte("original secret")
	j, err := NewWithSecret[*User](secret)
	if err != nil {
		t.Fatal(err)
	}
	token := j.Encode(&User{Uid: 1, Expired: time.Now().Add(time.Hour).Unix()})
	for i := range secret {
		secret[i] = 'x'
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 100 {
				j.SetHeader(Header{Algorithm: "HS256", Typ: "JWT"})
				if err := j.Decode(token, &User{}); err != nil {
					t.Error(err)
				}
				signed := j.Encode(&User{Uid: 2, Expired: time.Now().Add(time.Hour).Unix()})
				if err := j.Decode(signed, &User{}); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
}

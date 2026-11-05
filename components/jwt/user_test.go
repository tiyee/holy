package jwt

import (
	"errors"
	"testing"
	"time"
)

func TestUserToken(t *testing.T) {
	user := User{Uid: 1, Name: "test", Expired: time.Now().Add(time.Hour).Unix()}
	j := New[*User]([]byte("hello world"))
	bs := j.Encode(&user)
	u := User{Uid: 0, Name: ""}
	if err := j.Decode(bs, &u); err != nil {
		t.Fatal(err)
	}
	if u != user {
		t.Errorf("decoded user = %+v, want %+v", u, user)
	}
}

func TestUserTokenExpiration(t *testing.T) {
	secret := []byte("test-secret")
	now := time.Now().Unix()
	for _, tt := range []struct {
		name    string
		expires int64
		wantErr error
	}{
		{name: "valid", expires: now + 3600},
		{name: "expired", expires: now - 3600, wantErr: ErrTokenExpired},
		{name: "expires now", expires: now, wantErr: ErrTokenExpired},
		{name: "zero", expires: 0, wantErr: ErrTokenExpired},
		{name: "negative", expires: -1, wantErr: ErrTokenExpired},
	} {
		t.Run(tt.name, func(t *testing.T) {
			j := New[*User](secret)
			token := j.Encode(&User{Uid: 1, Expired: tt.expires})
			var user User
			if err := j.Decode(token, &user); !errors.Is(err, tt.wantErr) {
				t.Errorf("Decode() error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil && user != (User{}) {
				t.Errorf("rejected token populated user: %+v", user)
			}
		})
	}
}

func TestIgnoreExpiration(t *testing.T) {
	secret := []byte("test-secret")
	expired := &User{Uid: 1, Expired: time.Now().Add(-time.Hour).Unix()}
	token := Encode(expired, secret)
	var user User
	j := New[*User](secret, WithIgnoreExpiration[*User](true))
	if err := j.Decode(token, &user); err != nil {
		t.Fatal(err)
	}
	if user != *expired {
		t.Errorf("decoded user = %+v, want %+v", user, *expired)
	}
	if err := Decode(token, secret, &User{}, WithIgnoreExpiration[*User](true)); err != nil {
		t.Fatalf("package Decode with ignore option: %v", err)
	}
	if err := Decode(token, secret, &User{}, WithIgnoreExpiration[*User](false)); !errors.Is(err, ErrTokenExpired) {
		t.Errorf("ignore=false error = %v, want %v", err, ErrTokenExpired)
	}
	if err := Decode(token, secret, &User{}); !errors.Is(err, ErrTokenExpired) {
		t.Errorf("default error = %v, want %v", err, ErrTokenExpired)
	}
	if err := Decode(token, []byte("wrong-secret"), &User{}, WithIgnoreExpiration[*User](true)); err == nil {
		t.Error("ignore option bypassed signature verification")
	}
}

type rawPayload []byte

func (p *rawPayload) Encode() []byte { return *p }
func (p *rawPayload) Decode(bs []byte) error {
	*p = append((*p)[:0], bs...)
	return nil
}

func TestExpirationClaim(t *testing.T) {
	secret := []byte("test-secret")
	for _, tt := range []struct {
		name    string
		payload string
		wantErr error
	}{
		{name: "missing", payload: `{"u":1}`, wantErr: ErrMissingExpiration},
		{name: "null", payload: `{"exp":null}`, wantErr: ErrMissingExpiration},
		{name: "string", payload: `{"exp":"invalid"}`},
		{name: "fractional", payload: `{"exp":123.5}`},
		{name: "overflow", payload: `{"exp":9223372036854775808}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			payload := rawPayload(tt.payload)
			token := Encode(&payload, secret)
			var decoded rawPayload
			err := Decode(token, secret, &decoded)
			if err == nil {
				t.Fatal("expected expiration validation error")
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Errorf("error = %v, want %v", err, tt.wantErr)
			}
			if len(decoded) != 0 {
				t.Error("rejected token populated payload")
			}
			if err := Decode(token, secret, &decoded, WithIgnoreExpiration[*rawPayload](true)); err != nil {
				t.Fatalf("ignore expiration: %v", err)
			}
		})
	}
}

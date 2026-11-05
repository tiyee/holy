package aes_cbc

import (
	"bytes"
	"crypto/cipher"
	"crypto/des"
	"errors"
	"sync"
	"testing"
)

func TestGCMRoundTripAndTampering(t *testing.T) {
	for _, key := range []string{"0123456789abcdef", "0123456789abcdef01234567", "0123456789abcdef0123456789abcdef"} {
		c, err := NewWithKey([]byte(key))
		if err != nil {
			t.Fatal(err)
		}
		for _, plain := range [][]byte{nil, []byte("hello"), bytes.Repeat([]byte("a"), 1024)} {
			data, err := c.Encrypt(plain)
			if err != nil {
				t.Fatal(err)
			}
			again, _ := c.Encrypt(plain)
			if bytes.Equal(data, again) {
				t.Fatal("nonce reused")
			}
			got, err := c.Decrypt(data)
			if err != nil || !bytes.Equal(got, plain) {
				t.Fatalf("round trip: %q %v", got, err)
			}
			for i := range data {
				changed := bytes.Clone(data)
				changed[i] ^= 1
				if out, err := c.Decrypt(changed); !errors.Is(err, ErrInvalidCiphertext) || out != nil {
					t.Fatalf("tampering accepted at %d: %v", i, err)
				}
			}
			for i := 0; i < len(data); i++ {
				if _, err := c.Decrypt(data[:i]); err == nil {
					t.Fatalf("truncation accepted at %d", i)
				}
			}
		}
	}
}

func TestInvalidKeyAndUninitialized(t *testing.T) {
	cryptor.Store(nil)
	t.Cleanup(func() { cryptor.Store(nil) })
	if _, err := New().Encrypt(nil); !errors.Is(err, ErrNotInitialized) {
		t.Fatal(err)
	}
	if _, err := Decrypt(nil); !errors.Is(err, ErrNotInitialized) {
		t.Fatal(err)
	}
	if _, err := NewWithKey([]byte("12345678")); err == nil {
		t.Fatal("DES-size key accepted")
	}
	if _, err := NewAesCbc("bad", "iv").Encrypt(nil); err == nil {
		t.Fatal("invalid compatibility key accepted")
	}
	if err := Reset("0123456789abcdef", "ignored"); err != nil {
		t.Fatal(err)
	}
	before := New()
	if err := Configure([]byte("bad")); err == nil || New() != before {
		t.Fatal("failed reset changed default")
	}
	data, err := Encrypt([]byte("configured"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := New().Decrypt(data); err != nil || string(got) != "configured" {
		t.Fatalf("configured round trip: %s %v", got, err)
	}
	other, _ := NewWithKey([]byte("fedcba9876543210"))
	if _, err := other.Decrypt(data); !errors.Is(err, ErrInvalidCiphertext) {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 50 {
				if err := Configure([]byte("0123456789abcdef")); err != nil {
					t.Error(err)
				}
				if _, err := Encrypt([]byte("concurrent")); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
}

func TestLegacyMigrationAndPadding(t *testing.T) {
	block, _ := des.NewCipher([]byte("12345678"))
	plain := []byte("legacy data")
	padded := PKCS5Padding(plain, 8)
	old := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, []byte("abcdefgh")).CryptBlocks(old, padded)
	got, err := DecryptLegacyDES(old, "12345678", "abcdefgh")
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("legacy decode: %s %v", got, err)
	}
	c, _ := NewWithKey([]byte("0123456789abcdef"))
	if _, err := c.Decrypt(old); !errors.Is(err, ErrInvalidCiphertext) {
		t.Fatal("automatic downgrade")
	}
	data, _ := c.Encrypt(got)
	if migrated, err := c.Decrypt(data); err != nil || !bytes.Equal(migrated, plain) {
		t.Fatal("migration failed")
	}
	for _, input := range [][]byte{nil, {1}, make([]byte, 8), {1, 2, 3, 4, 5, 6, 2, 3}, {1, 2, 3, 4, 5, 6, 7, 9}} {
		if _, err := DecryptLegacyDES(input, "12345678", "abcdefgh"); err == nil {
			t.Fatal("invalid legacy ciphertext accepted")
		}
	}
	if _, err := DecryptLegacyDES(old, "12345678", "short"); err == nil {
		t.Fatal("invalid IV accepted")
	}
	for _, input := range [][]byte{nil, {1}, make([]byte, 8), {1, 2, 3, 4, 5, 6, 2, 3}, {1, 2, 3, 4, 5, 6, 7, 9}} {
		if _, err := UnpadPKCS7(input, 8); !errors.Is(err, ErrInvalidPadding) {
			t.Fatalf("padding: %v", err)
		}
		if PKCS5UnPadding(input) != nil {
			t.Fatal("unsafe compatibility padding")
		}
	}
	for _, size := range []int{0, -1, 256} {
		if PKCS5Padding(plain, size) != nil {
			t.Fatal("invalid block size accepted")
		}
	}
}

func FuzzDecryptNeverPanics(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte("holy:gcm:1:invalid"))
	f.Add(make([]byte, 8))
	c, _ := NewWithKey([]byte("0123456789abcdef"))
	f.Fuzz(func(t *testing.T, data []byte) {
		c.Decrypt(data)
		DecryptLegacyDES(data, "12345678", "abcdefgh")
		UnpadPKCS7(data, 8)
	})
}

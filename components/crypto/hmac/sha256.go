package hmac

import (
	"crypto/hmac"
	"crypto/sha256"
	"github.com/tiyee/holy/components/crypto/base64"
)

func Sha256Encrypt(textBytes []byte, secret []byte) []byte {
	h := hmac.New(sha256.New, secret)
	h.Write(textBytes)

	return base64.UrlEncode(h.Sum(nil))
}

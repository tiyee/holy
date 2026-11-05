package sha256

import (
	"crypto/sha256"
	"encoding/hex"
)

func Encrypt(textBytes []byte) ([]byte, error) {
	bytes2 := sha256.Sum256(textBytes) //计算哈希值，返回一个长度为32的数组
	src := bytes2[:]
	dst := make([]byte, hex.EncodedLen(len(src)))
	hex.Encode(dst, src)
	return dst, nil
}

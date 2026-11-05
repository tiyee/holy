package helps

import (
	"crypto/rand"
	"encoding/hex"
)

// NonceStr 返回由密码学安全随机源生成的 32 位小写十六进制字符串。
func NonceStr() string {
	var value [16]byte
	// rand.Read 保证填满缓冲，随机源失败时由标准库终止进程。
	_, _ = rand.Read(value[:])
	return hex.EncodeToString(value[:])
}

package log

import (
	"math"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"
)

// buffer 是基于 []byte 的行编码缓冲区，配合 sync.Pool 复用，
// 使单条日志的编码过程基本不产生堆分配（对标 zap 的 buffer）。
type buffer struct {
	bs []byte
}

const (
	defaultBufSize   = 4096
	maxPooledBufSize = 1 << 16 // 超过 64KB 的 buffer 不归还池，防止内存膨胀
)

var bufPool = sync.Pool{
	New: func() any {
		return &buffer{bs: make([]byte, 0, defaultBufSize)}
	},
}

func getBuffer() *buffer {
	return bufPool.Get().(*buffer)
}

// putBuffer 归还 buffer；超大 buffer 直接丢弃交由 GC 回收。
func putBuffer(b *buffer) {
	if b == nil {
		return
	}
	if cap(b.bs) > maxPooledBufSize {
		return
	}
	b.bs = b.bs[:0]
	bufPool.Put(b)
}

func (b *buffer) Bytes() []byte { return b.bs }
func (b *buffer) Len() int      { return len(b.bs) }
func (b *buffer) Reset()        { b.bs = b.bs[:0] }
func (b *buffer) Write(p []byte) (int, error) {
	b.bs = append(b.bs, p...)
	return len(p), nil
}

func (b *buffer) AppendString(s string) { b.bs = append(b.bs, s...) }
func (b *buffer) AppendByte(v byte)     { b.bs = append(b.bs, v) }
func (b *buffer) AppendInt(i int64)     { b.bs = strconv.AppendInt(b.bs, i, 10) }
func (b *buffer) AppendUint(i uint64)   { b.bs = strconv.AppendUint(b.bs, i, 10) }
func (b *buffer) AppendBool(v bool)     { b.bs = strconv.AppendBool(b.bs, v) }
func (b *buffer) AppendTime(t time.Time, layout string) {
	b.bs = t.AppendFormat(b.bs, layout)
}

// appendJSONFloat 追加合法 JSON 浮点数；NaN/Inf 不是合法 JSON，降级为字符串。
func appendJSONFloat(dst []byte, f float64) []byte {
	switch {
	case math.IsNaN(f):
		return append(dst, `"NaN"`...)
	case math.IsInf(f, 1):
		return append(dst, `"+Inf"`...)
	case math.IsInf(f, -1):
		return append(dst, `"-Inf"`...)
	}
	// 与 encoding/json 保持一致：常规用 'f'，极端量级用 'e'
	abs := math.Abs(f)
	format := byte('f')
	if abs != 0 && (abs < 1e-6 || abs >= 1e21) {
		format = 'e'
	}
	return strconv.AppendFloat(dst, f, format, -1, 64)
}

const hexDigits = "0123456789abcdef"

// noEscapeTable[i] 为真表示字节 i 在 JSON 字符串中无需转义，可原样拷贝。
// 仅用于 ASCII 快速路径（单次查表替代多次比较）；>=0x80 的字节仍走
// UTF-8 解码路径，处理 U+2028/U+2029 与非法序列。
var noEscapeTable = func() (t [256]bool) {
	for i := range t {
		t[i] = i >= 0x20 && i != '\\' && i != '"'
	}
	return t
}()

// appendJSONString 追加一个完整 JSON 字符串（含首尾引号）。
// 快速路径经 noEscapeTable 逐段拷贝不含特殊字符的区间；
// 仅在遇到需要转义的字符时才走慢路径。
func appendJSONString(dst []byte, s string) []byte {
	dst = append(dst, '"')
	start := 0
	for i := 0; i < len(s); {
		if b := s[i]; b < utf8.RuneSelf {
			if noEscapeTable[b] {
				i++
				continue
			}
			dst = append(dst, s[start:i]...)
			switch b {
			case '\\', '"':
				dst = append(dst, '\\', b)
			case '\n':
				dst = append(dst, '\\', 'n')
			case '\r':
				dst = append(dst, '\\', 'r')
			case '\t':
				dst = append(dst, '\\', 't')
			default:
				// 其余控制字符统一转 \u00XX
				dst = append(dst, '\\', 'u', '0', '0', hexDigits[b>>4], hexDigits[b&0xF])
			}
			i++
			start = i
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			// 非法 UTF-8 序列，替换为 U+FFFD
			dst = append(dst, s[start:i]...)
			dst = append(dst, "\ufffd"...)
			i += size
			start = i
			continue
		}
		if r == '\u2028' || r == '\u2029' {
			// U+2028/U+2029 对 JS 不安全，需转义
			dst = append(dst, s[start:i]...)
			dst = append(dst, '\\', 'u', '2', '0', '2', hexDigits[r&0xF])
			i += size
			start = i
			continue
		}
		i += size
	}
	dst = append(dst, s[start:]...)
	return append(dst, '"')
}

// appendJSONKey 追加 `"key":`。
func appendJSONKey(dst []byte, key string) []byte {
	dst = appendJSONString(dst, key)
	return append(dst, ':')
}

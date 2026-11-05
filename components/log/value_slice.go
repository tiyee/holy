package log

import (
	"bytes"
	"math"
	"strconv"
)

// appendSliceValues 直接编码元素，不创建反射编码器或中间字符串。
func appendSliceValues[T any](dst []byte, values []T, separator byte, appendValue func([]byte, T) []byte) []byte {
	dst = append(dst, '[')
	for i, value := range values {
		if i > 0 {
			dst = append(dst, separator)
		}
		dst = appendValue(dst, value)
	}
	return append(dst, ']')
}

func appendSliceJSON[T any](buf *buffer, values []T, appendValue func([]byte, T) []byte) {
	if values == nil {
		buf.AppendString("null")
		return
	}
	buf.bs = appendSliceValues(buf.bs, values, ',', appendValue)
}

// appendSliceConsole 保留 fmt 的空格分隔及整值加引号语义，临时内容复用缓冲池。
func appendSliceConsole[T any](buf *buffer, values []T, appendValue func([]byte, T) []byte) {
	scratch := getBuffer()
	scratch.bs = appendSliceValues(scratch.bs, values, ' ', appendValue)
	buf.bs = appendTextString(buf.bs, string(scratch.bs))
	putBuffer(scratch)
}

// appendSliceStringJSON 保留原 json.Marshal 路径对 HTML 字符的转义。
func appendSliceStringJSON(dst []byte, value string) []byte {
	start := len(dst)
	dst = appendJSONString(dst, value)
	if bytes.IndexAny(dst[start:], "<>&") < 0 {
		return dst
	}
	scratch := getBuffer()
	scratch.bs = append(scratch.bs, dst[start:]...)
	dst = dst[:start]
	for _, c := range scratch.bs {
		switch c {
		case '<':
			dst = append(dst, `\u003c`...)
		case '>':
			dst = append(dst, `\u003e`...)
		case '&':
			dst = append(dst, `\u0026`...)
		default:
			dst = append(dst, c)
		}
	}
	putBuffer(scratch)
	return dst
}

func appendSliceStringConsole(dst []byte, value string) []byte { return append(dst, value...) }

func appendSliceInt[T ~int | ~int64](dst []byte, value T) []byte {
	return strconv.AppendInt(dst, int64(value), 10)
}

func appendSliceUint64(dst []byte, value uint64) []byte {
	return strconv.AppendUint(dst, value, 10)
}

func appendSliceFloatConsole(dst []byte, value float64) []byte {
	return strconv.AppendFloat(dst, value, 'g', -1, 64)
}

// appendSliceFloatJSON 与 encoding/json 一样去掉负指数的前导零。
func appendSliceFloatJSON(dst []byte, value float64) []byte {
	dst = appendJSONFloat(dst, value)
	n := len(dst)
	if n >= 4 && dst[n-4] == 'e' && dst[n-3] == '-' && dst[n-2] == '0' {
		dst[n-2] = dst[n-1]
		dst = dst[:n-1]
	}
	return dst
}

// appendFloatSliceJSON 遇到非有限数时保持整个切片降级为字符串的既有行为。
func appendFloatSliceJSON(buf *buffer, values []float64) {
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			appendReflectedJSON(buf, values)
			return
		}
	}
	appendSliceJSON(buf, values, appendSliceFloatJSON)
}

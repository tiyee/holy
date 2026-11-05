package log

import (
	"log/slog"
	"time"
)

// Field 兼容 zap.Field 的概念，直接复用 slog.Attr（零拷贝、避免装箱）。
type Field = slog.Attr

// 常用强类型字段构造器：不经过 any 装箱，也不依赖反射。
func String(key, val string) Field               { return slog.String(key, val) }
func Int(key string, v int) Field                { return slog.Int(key, v) }
func Int8(key string, v int8) Field              { return slog.Int64(key, int64(v)) }
func Int16(key string, v int16) Field            { return slog.Int64(key, int64(v)) }
func Int32(key string, v int32) Field            { return slog.Int64(key, int64(v)) }
func Int64(key string, v int64) Field            { return slog.Int64(key, v) }
func Uint(key string, v uint) Field              { return slog.Uint64(key, uint64(v)) }
func Uint8(key string, v uint8) Field            { return slog.Uint64(key, uint64(v)) }
func Uint16(key string, v uint16) Field          { return slog.Uint64(key, uint64(v)) }
func Uint32(key string, v uint32) Field          { return slog.Uint64(key, uint64(v)) }
func Uint64(key string, v uint64) Field          { return slog.Uint64(key, v) }
func Bool(key string, v bool) Field              { return slog.Bool(key, v) }
func Float64(key string, v float64) Field        { return slog.Float64(key, v) }
func Float32(key string, v float32) Field        { return slog.Float64(key, float64(v)) }
func Time(key string, v time.Time) Field         { return slog.Time(key, v) }
func Duration(key string, v time.Duration) Field { return slog.Duration(key, v) }
func Any(key string, v any) Field                { return slog.Any(key, v) }
func Strings(key string, v []string) Field       { return slog.Any(key, v) }
func Ints(key string, v []int) Field             { return slog.Any(key, v) }
func Group(key string, args ...Field) Field {
	return slog.Attr{Key: key, Value: slog.GroupValue(args...)}
}

// Err 等价 zap.Error：固定 key 为 "error"，输出 err.Error()；err 为 nil 时省略字段。
// （zap.Error 在本包更名为 Err，避免与包级日志函数 log.Error 冲突）
func Err(err error) Field {
	if err == nil {
		return Skip()
	}
	return slog.Any("error", err)
}

// NamedError 是可指定 key 的 Error（对应 zap.NamedError）；err 为 nil 时省略字段。
func NamedError(key string, err error) Field {
	if err == nil {
		return Skip()
	}
	return slog.Any(key, err)
}

// Reflect 对应 zap.Reflect，语义为"原样输出"，这里直接透传给 JSON 编码兜底。
func Reflect(key string, v any) Field { return slog.Any(key, v) }

// Skip 返回空字段，用于占位（对应 zap.Skip）。
func Skip() Field { return slog.Attr{} }

// LogObjectMarshaler 让自定义类型免反射地展开为一组字段：
// Any 等字段值遇到该实现时按嵌套对象编码（JSON 内为 `{...}`，console 内为
// 点分前缀的 k=v），不再走 json.Marshal 反射兜底（zap ObjectMarshaler 的等价物）。
// 返回切片在编码期只读；内容静态时可复用缓存以实现零分配。
type LogObjectMarshaler interface {
	MarshalLogObject() []Field
}

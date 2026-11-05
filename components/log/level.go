package log

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
)

// 在 slog 标准四级之外扩展 DPanic/Panic/Fatal，对齐 zap 的级别体系。
const (
	LevelDebug  = slog.LevelDebug // -4
	LevelInfo   = slog.LevelInfo  // 0
	LevelWarn   = slog.LevelWarn  // 4
	LevelError  = slog.LevelError // 8
	LevelDPanic = slog.Level(12)  // 开发模式下触发 panic
	LevelPanic  = slog.Level(13)  // 记录后 panic
	LevelFatal  = slog.Level(14)  // 记录后 exit(1)
)

// ParseLevel 解析级别字符串：
//   - 名称：debug/info/warn/error/dpanic/panic/fatal（大小写不敏感）
//   - 数字："8" 等价于 error
//   - slog 风格："ERROR+2"
//
// 空串视为 info。
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "info":
		return LevelInfo, nil
	case "debug":
		return LevelDebug, nil
	case "warn", "warning":
		return LevelWarn, nil
	case "error":
		return LevelError, nil
	case "dpanic":
		return LevelDPanic, nil
	case "panic":
		return LevelPanic, nil
	case "fatal":
		return LevelFatal, nil
	}
	// 纯数字
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return slog.Level(n), nil
	}
	// slog 扩展格式，如 "INFO+2"
	var l slog.Level
	if err := l.UnmarshalText([]byte(strings.TrimSpace(s))); err == nil {
		return l, nil
	}
	return LevelInfo, fmt.Errorf("log: 无法解析日志级别 %q", s)
}

// levelName 返回级别在日志行中展示的名称。
func levelName(l slog.Level) string {
	switch {
	case l < LevelInfo:
		return "DEBUG"
	case l < LevelWarn:
		return "INFO"
	case l < LevelError:
		return "WARN"
	case l < LevelDPanic:
		return "ERROR"
	case l < LevelPanic:
		return "DPANIC"
	case l < LevelFatal:
		return "PANIC"
	default:
		return "FATAL"
	}
}

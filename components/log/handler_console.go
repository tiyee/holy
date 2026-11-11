package log

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"runtime"
	"strconv"
	"time"
	"unicode/utf8"
)

// ANSI 颜色，仅开发模式的 console 输出使用。
const (
	colorReset  = "\x1b[0m"
	colorGray   = "\x1b[90m"
	colorBlue   = "\x1b[34m"
	colorYellow = "\x1b[33m"
	colorRed    = "\x1b[31m"
	colorBold   = "\x1b[1;31m"
)

func levelColor(l slog.Level) string {
	switch {
	case l < LevelInfo:
		return colorGray
	case l < LevelWarn:
		return colorBlue
	case l < LevelError:
		return colorYellow
	case l < LevelDPanic:
		return colorRed
	default:
		return colorBold
	}
}

// consoleHandler 输出人类可读格式：
//
//	2026-10-04 12:00:00.000 INFO msg k1=v1 k2="v 2" g.k=3
type consoleHandler struct {
	w          io.Writer
	leveler    slog.Leveler
	timeLayout string
	name       string // Named 的 Logger 名字，Handle 期懒编码（见 withName）
	addSource  bool
	color      bool
	options    handlerOptions

	// preformatted 缓存公共字段的文本形式，形如 ` app=api env=prod`
	preformatted []byte
	// groupPrefix 是已打开组的点分前缀，形如 `g1.g2.`
	groupPrefix string
}

// newConsoleHandler 构建记录器；timeLayout 为空表示不输出行首时间（DisableTime），
// 字段值内的时间回退 defaultConsoleTimeFormat。
func newConsoleHandler(w io.Writer, leveler slog.Leveler, timeLayout string, addSource, color bool, options handlerOptions) *consoleHandler {
	return &consoleHandler{
		w:          w,
		leveler:    leveler,
		timeLayout: timeLayout,
		addSource:  addSource,
		color:      color,
		options:    options,
	}
}

// valueConsoleTimeLayout 返回字段值内时间的编码布局。
func valueConsoleTimeLayout(timeLayout string) string {
	if timeLayout == "" {
		return defaultConsoleTimeFormat
	}
	return timeLayout
}

// withName 实现懒编码的 Logger 名字：仅克隆并记录，Handle 期写入。
func (h *consoleHandler) withName(name string) slog.Handler {
	n := h.clone()
	n.name = name
	return n
}

func (h *consoleHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.leveler.Level()
}

func (h *consoleHandler) clone() *consoleHandler {
	nh := *h
	nh.preformatted = h.preformatted[:len(h.preformatted):len(h.preformatted)]
	return &nh
}

func (h *consoleHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	nh := h.clone()
	buf := getBuffer()
	defer putBuffer(buf)
	for _, a := range attrs {
		appendAttrConsole(buf, a, nh.groupPrefix, valueConsoleTimeLayout(nh.timeLayout))
	}
	nh.preformatted = append(nh.preformatted, buf.bs...)
	return nh
}

func (h *consoleHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	nh := h.clone()
	if nh.groupPrefix == "" {
		nh.groupPrefix = name + "."
	} else {
		nh.groupPrefix += name + "."
	}
	return nh
}

func (h *consoleHandler) Handle(ctx context.Context, r slog.Record) error {
	buf := getBuffer()

	if h.timeLayout != "" && !r.Time.IsZero() {
		buf.AppendTime(r.Time, h.timeLayout)
		buf.AppendByte(' ')
	}
	if h.color {
		buf.AppendString(levelColor(r.Level))
		buf.AppendString(levelName(r.Level))
		buf.AppendString(colorReset)
	} else {
		buf.AppendString(levelName(r.Level))
	}
	if h.name != "" {
		buf.AppendByte(' ')
		buf.AppendString(h.name)
	}
	buf.AppendByte(' ')

	if h.addSource && r.PC != 0 {
		fs := runtime.CallersFrames([]uintptr{r.PC})
		if f, _ := fs.Next(); f.File != "" {
			buf.AppendString(f.File)
			buf.AppendByte(':')
			buf.AppendInt(int64(f.Line))
			buf.AppendByte(' ')
		}
	}

	buf.AppendString(r.Message)

	if len(h.preformatted) > 0 {
		buf.Write(h.preformatted)
	}

	if h.options.trace != nil && ctx != nil {
		if ti, ok := h.options.trace(ctx); ok {
			buf.AppendString(" trace_id=")
			buf.AppendString(ti.TraceID)
			buf.AppendString(" span_id=")
			buf.AppendString(ti.SpanID)
		}
	}

	if r.NumAttrs() > 0 {
		r.Attrs(func(a slog.Attr) bool {
			appendAttrConsole(buf, a, h.groupPrefix, valueConsoleTimeLayout(h.timeLayout))
			return true
		})
	}

	buf.AppendByte('\n')

	_, err := h.w.Write(buf.bs)
	if err != nil {
		h.options.writeErrors.report(err, buf.bs)
	}
	putBuffer(buf)
	return err
}

// appendAttrConsole 追加单个字段（自带前置空格）。
func appendAttrConsole(buf *buffer, a slog.Attr, prefix, timeLayout string) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}

	// LogObjectMarshaler：对象免反射展开为点分前缀的子字段
	if a.Value.Kind() == slog.KindAny {
		if om, ok := a.Value.Any().(LogObjectMarshaler); ok {
			for _, f := range om.MarshalLogObject() {
				appendAttrConsole(buf, f, prefix+a.Key+".", timeLayout)
			}
			return
		}
	}

	if a.Value.Kind() == slog.KindGroup {
		els := a.Value.Group()
		if len(els) == 0 {
			return
		}
		if a.Key == "" {
			for _, el := range els {
				appendAttrConsole(buf, el, prefix, timeLayout)
			}
			return
		}
		for _, el := range els {
			appendAttrConsole(buf, el, prefix+a.Key+".", timeLayout)
		}
		return
	}

	buf.AppendByte(' ')
	buf.AppendString(prefix)
	buf.AppendString(a.Key)
	buf.AppendByte('=')
	appendValueConsole(buf, a.Value, timeLayout)
}

func appendValueConsole(buf *buffer, v slog.Value, timeLayout string) {
	switch v.Kind() {
	case slog.KindString:
		buf.bs = appendTextString(buf.bs, v.String())
	case slog.KindInt64:
		buf.AppendInt(v.Int64())
	case slog.KindUint64:
		buf.AppendUint(v.Uint64())
	case slog.KindFloat64:
		buf.bs = strconv.AppendFloat(buf.bs, v.Float64(), 'g', -1, 64)
	case slog.KindBool:
		buf.AppendBool(v.Bool())
	case slog.KindDuration:
		buf.AppendString(v.Duration().String())
	case slog.KindTime:
		buf.AppendTime(v.Time(), timeLayout)
	case slog.KindAny:
		appendAnyConsole(buf, v.Any(), timeLayout)
	default:
		buf.bs = appendTextString(buf.bs, fmt.Sprint(v.Any()))
	}
}

func appendAnyConsole(buf *buffer, x any, timeLayout string) {
	switch xv := x.(type) {
	case nil:
		buf.AppendString("<nil>")
	case error:
		buf.bs = appendTextString(buf.bs, safeLogString(xv))
	case time.Duration:
		buf.AppendString(xv.String())
	case time.Time:
		buf.AppendTime(xv, timeLayout)
	case []string:
		appendSliceConsole(buf, xv, appendSliceStringConsole)
	case []int:
		appendSliceConsole(buf, xv, appendSliceInt[int])
	case []int64:
		appendSliceConsole(buf, xv, appendSliceInt[int64])
	case []uint64:
		appendSliceConsole(buf, xv, appendSliceUint64)
	case []float64:
		appendSliceConsole(buf, xv, appendSliceFloatConsole)
	case []bool:
		appendSliceConsole(buf, xv, strconv.AppendBool)
	case []byte:
		// 与 JSON 编码保持一致：base64 字符串
		buf.AppendByte('"')
		buf.bs = base64.StdEncoding.AppendEncode(buf.bs, xv)
		buf.AppendByte('"')
	case fmt.Stringer:
		buf.bs = appendTextString(buf.bs, safeLogString(xv))
	default:
		buf.bs = appendTextString(buf.bs, logValueText(xv, "%+v"))
	}
}

// appendTextString 对包含空白/等号/引号的字符串加引号，其余原样输出。
func appendTextString(dst []byte, s string) []byte {
	if textNeedsQuoting(s) {
		return strconv.AppendQuote(dst, s)
	}
	return append(dst, s...)
}

func textNeedsQuoting(s string) bool {
	if len(s) == 0 {
		return true
	}
	for i := 0; i < len(s); {
		b := s[i]
		if b < utf8.RuneSelf {
			if b <= ' ' || b == '=' || b == '"' || b == 0x7f {
				return true
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			return true
		}
		i += size
	}
	return false
}

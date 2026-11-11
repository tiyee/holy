package log

import (
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"
)

// safeLogString 隔离用户 Error/String 方法的 panic，不影响强类型标量热路径。
// 与 slog 一致：nil 指针方法引发的 panic 输出 <nil>，其他 panic 输出诊断文本。
func safeLogString(x any) (s string) {
	defer func() {
		if p := recover(); p != nil {
			v := reflect.ValueOf(x)
			if v.Kind() == reflect.Pointer && v.IsNil() {
				s = "<nil>"
			} else {
				s = "!PANIC: " + logValueText(p, "%v")
			}
		}
	}()
	if err, ok := x.(error); ok {
		return err.Error()
	}
	return x.(fmt.Stringer).String()
}

// logValueText 用于字段的 %v 和 %+v 文本表示。fmt 会无限展开自引用的 map 和切片，
// 栈溢出无法 recover，所以必须在把值交给 fmt 之前检查。
func logValueText(x any, format string) string {
	if (textFormat{verb: 'v'}).cyclic(x) {
		x = cycleText
	}
	return fmt.Sprintf(format, x)
}

const cycleText = "<cycle>"

// cyclePlaceholder 保留 fmt.Sprint 对非字符串参数的空格规则。
type cyclePlaceholder struct{}

func (cyclePlaceholder) String() string { return cycleText }

// logSprint 按 fmt.Sprint 规则拼接消息，跳过会无限展开的循环引用。
func logSprint(args []any) string {
	if len(args) == 1 {
		if msg, ok := args[0].(string); ok {
			return msg
		}
	}
	var safe []any
	for i, arg := range args {
		if (textFormat{verb: 'v'}).cyclic(arg) {
			if safe == nil {
				safe = slices.Clone(args)
			}
			safe[i] = cyclePlaceholder{}
		}
	}
	if safe != nil {
		args = safe
	}
	return fmt.Sprint(args...)
}

// 由 fmt 自己解析变参下标、宽度和精度。探测时不调用用户方法，也不遍历只用于
// %T、%p 或完全未使用的 map。只有会递归展开的实参才需要替换。
func logSprintf(format string, args ...any) string {
	var probes, safe []any
	for i, arg := range args {
		t := reflect.TypeOf(arg)
		if t == nil {
			continue
		}
		kind := t.Kind()
		if kind == reflect.Pointer {
			t = t.Elem()
		}
		if !canContainTextCycle(t) {
			continue
		}
		// 单个 %v 或 %+v 已经确定会展开哪个值，可直接生成整条消息。
		if len(args) == 1 {
			_, tail, _ := strings.Cut(format, "%")
			if (strings.HasPrefix(tail, "v") || strings.HasPrefix(tail, "+v")) && !strings.Contains(tail, "%") {
				return logValueText(arg, format)
			}
		}
		if probes == nil {
			probes = make([]any, len(args))
		}
		probes[i] = formatProbe(func(s fmt.State, verb rune) {
			if verb == probePointer && (kind == reflect.Pointer || kind == reflect.Map || kind == reflect.Slice) {
				return
			}
			if (textFormat{verb, s.Flag('#')}).cyclic(arg) {
				if safe == nil {
					safe = slices.Clone(args)
				}
				safe[i] = cycleText
			}
		})
	}
	if probes != nil {
		// fmt 特殊处理 %p 和 %w，不会调用 Formatter。仅在探测串内改写这两个动词，
		// 以覆盖无效格式生成诊断时也会展开原值的情况。探测输出全部丢弃。
		fmt.Fprintf(io.Discard, probeVerbs.Replace(format), probes...)
		if safe != nil {
			args = safe
		}
	}
	return fmt.Sprintf(format, args...)
}

const probePointer = '\ue000'
const probeWrap = '\ue001'

var probeVerbs = strings.NewReplacer(string(probePointer), "?", string(probeWrap), "?",
	"p", string(probePointer), "w", string(probeWrap))

type formatProbe func(fmt.State, rune)

func (p formatProbe) Format(s fmt.State, verb rune) { p(s, verb) }

type textFormat struct {
	verb  rune
	sharp bool
}

func (f textFormat) cyclic(x any) bool {
	v, ok := x.(reflect.Value)
	if !ok {
		v = reflect.ValueOf(x)
	}
	return f.hasCycle(v, make(map[textVisit]bool), true)
}

func (f textFormat) callsMethod(v reflect.Value) bool {
	if !v.IsValid() || !v.CanInterface() || v.Type().NumMethod() == 0 || f.verb == probeWrap || f.verb == probePointer {
		return false
	}
	x := v.Interface()
	if _, ok := x.(fmt.Formatter); ok {
		return true
	}
	if f.sharp && f.verb == 'v' {
		_, ok := x.(fmt.GoStringer)
		return ok
	}
	switch f.verb {
	case 'v', 's', 'x', 'X', 'q':
		switch x.(type) {
		case error, fmt.Stringer:
			return true
		}
	}
	return false
}

type textVisit struct {
	typ reflect.Type
	ptr uintptr
	len int
}

// 标量容器（如 []byte 或 map[string]int）无需逐元素检查。
func canContainTextCycle(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Struct:
		return true
	case reflect.Array, reflect.Slice, reflect.Map:
		switch t.Elem().Kind() {
		case reflect.Interface, reflect.Map, reflect.Slice, reflect.Array, reflect.Struct:
			return true
		}
	}
	return false
}

func (f textFormat) hasCycle(v reflect.Value, path map[textVisit]bool, root bool) bool {
	if v.Kind() == reflect.Interface {
		v = v.Elem()
	}
	if !v.IsValid() || (v.Kind() != reflect.Pointer && !canContainTextCycle(v.Type())) || f.callsMethod(v) {
		return false
	}
	switch v.Kind() {
	case reflect.Pointer:
		// fmt 只展开顶层指向复合值的指针，内部指针直接输出地址。
		if root {
			switch v.Elem().Kind() {
			case reflect.Array, reflect.Map, reflect.Slice, reflect.Struct:
				return f.hasCycle(v.Elem(), path, false)
			}
		}
	case reflect.Map, reflect.Slice:
		if v.Len() == 0 {
			return false
		}
		visit := textVisit{v.Type(), v.Pointer(), v.Len()}
		if path[visit] {
			return true
		}
		path[visit] = true
		defer delete(path, visit) // 兄弟字段可以合法共享同一容器。
		if v.Kind() == reflect.Map {
			for it := v.MapRange(); it.Next(); {
				if f.hasCycle(it.Value(), path, false) {
					return true
				}
			}
			return false
		}
		fallthrough
	case reflect.Array:
		for i := 0; i < v.Len(); i++ {
			if f.hasCycle(v.Index(i), path, false) {
				return true
			}
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if f.hasCycle(v.Field(i), path, false) {
				return true
			}
		}
	}
	return false
}

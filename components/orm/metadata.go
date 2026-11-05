package orm

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
)

type mappedField struct {
	index     int
	lowerName string
	quoted    string
}

type metadata struct {
	typ             reflect.Type
	mode            MappingMode
	fields          []mappedField
	rawBytesColumns []int
	columnSQL       string
	insertRow       string
}

type metadataEntry struct {
	once sync.Once
	meta *metadata
	err  error
}

// MappingMode 将数据库列名与 JSON 编解码字段解耦，默认保留旧 JSON 标签行为。
type MappingMode uint8

const (
	JSONTags MappingMode = iota
	DatabaseTags
)

type metadataKey struct {
	typ  reflect.Type
	mode MappingMode
}

var metadataCache sync.Map

func loadMetadata(typ reflect.Type) (*metadata, error) { return loadMetadataFor(typ, JSONTags) }
func loadMetadataFor(typ reflect.Type, mode MappingMode) (*metadata, error) {
	key := metadataKey{typ, mode}
	cached, ok := metadataCache.Load(key)
	if !ok {
		cached, _ = metadataCache.LoadOrStore(key, &metadataEntry{})
	}
	entry := cached.(*metadataEntry)
	entry.once.Do(func() { entry.meta, entry.err = parseMetadataFor(typ, mode) })
	return entry.meta, entry.err
}

func parseMetadata(typ reflect.Type) (*metadata, error) { return parseMetadataFor(typ, JSONTags) }
func parseMetadataFor(typ reflect.Type, mode MappingMode) (*metadata, error) {
	if mode != JSONTags && mode != DatabaseTags {
		return nil, ErrInvalidMapping
	}
	if typ == nil || typ.Kind() != reflect.Pointer || typ.Elem().Kind() != reflect.Struct {
		return nil, ErrInvalidModel
	}
	meta := &metadata{typ: typ, mode: mode}
	columns := make([]string, 0, typ.Elem().NumField())
	seen := make(map[string]bool)
	for i := 0; i < typ.Elem().NumField(); i++ {
		field := typ.Elem().Field(i)
		if field.PkgPath != "" {
			continue
		}
		tag := field.Tag.Get("json")
		if mode == DatabaseTags {
			if dbTag, ok := field.Tag.Lookup("db"); ok {
				tag = dbTag
			}
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" || name == "-" {
			continue
		}
		lowerName := strings.ToLower(name)
		if strings.TrimSpace(name) == "" || seen[lowerName] {
			return nil, fmt.Errorf("%w: %q", ErrInvalidMapping, name)
		}
		seen[lowerName] = true
		quoted := quoteIdentifier(name)
		if isRawBytesField(field.Type) {
			meta.rawBytesColumns = append(meta.rawBytesColumns, len(meta.fields))
		}
		meta.fields = append(meta.fields, mappedField{index: i, lowerName: lowerName, quoted: quoted})
		columns = append(columns, quoted)
	}
	meta.columnSQL = strings.Join(columns, ",")
	meta.insertRow = "(" + strings.TrimSuffix(strings.Repeat("?,", len(meta.fields)), ",") + ")"
	return meta, nil
}

func quoteIdentifier(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

func fieldValues[T ITable](record T, fields []mappedField) []any {
	value := reflect.ValueOf(record).Elem()
	values := make([]any, len(fields))
	for i, field := range fields {
		values[i] = value.Field(field.index).Interface()
	}
	return values
}

func scanBinds[T ITable](record T, fields []mappedField) []any {
	dest := make([]any, len(fields))
	bindInto(record, fields, dest)
	return dest
}

func bindInto[T ITable](record T, fields []mappedField, dest []any) {
	value := reflect.ValueOf(record).Elem()
	for i, field := range fields {
		dest[i] = value.Field(field.index).Addr().Interface()
	}
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return v.IsNil()
	}
	return false
}

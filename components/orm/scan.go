package orm

import (
	"database/sql"
	"reflect"
)

// isRawBytesField 只在构建元数据时调用。带名字的指针类型允许递归定义。
func isRawBytesField(t reflect.Type) bool {
	seen := map[reflect.Type]bool{}
	for t.Kind() == reflect.Pointer {
		if seen[t] {
			return false
		}
		seen[t] = true
		t = t.Elem()
	}
	return t == reflect.TypeFor[sql.RawBytes]() || t == reflect.TypeFor[sql.Null[sql.RawBytes]]()
}

func bindQueryInto[T ITable](record T, meta *metadata, dest []any) {
	bindInto(record, meta.fields, dest)
	for _, i := range meta.rawBytesColumns {
		if raw, ok := dest[i].(*sql.RawBytes); ok {
			// *[]byte 让 database/sql 在驱动缓冲仍有效时复制字节。
			dest[i] = (*[]byte)(raw)
		} else {
			dest[i] = nullableRawBytes{field: reflect.ValueOf(dest[i]).Elem()}
		}
	}
}

type nullableRawBytes struct{ field reflect.Value }

func (s nullableRawBytes) Scan(src any) error {
	var data sql.Null[[]byte]
	if err := data.Scan(src); err != nil {
		return err
	}
	field := s.field
	for field.Kind() == reflect.Pointer {
		if !data.Valid {
			field.SetZero()
			return nil
		}
		field.Set(reflect.New(field.Type().Elem()))
		field = field.Elem()
	}
	if nullable, ok := field.Addr().Interface().(*sql.Null[sql.RawBytes]); ok {
		nullable.V, nullable.Valid = data.V, data.Valid
	} else {
		field.SetBytes(data.V)
	}
	return nil
}

func collectRows[T ITable](rows *sql.Rows, newRecord func() T, meta *metadata) ([]T, error) {
	defer rows.Close()
	results := make([]T, 0)
	// This buffer belongs to this result set, so concurrent queries never share
	// destinations. Replace every address before scanning the next fresh record.
	dest := make([]any, len(meta.fields))
	for rows.Next() {
		record := newRecord()
		bindQueryInto(record, meta, dest)
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		results = append(results, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

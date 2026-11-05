package orm

import (
	"database/sql"
	"database/sql/driver"
	"reflect"
	"testing"
)

func BenchmarkMetadata(b *testing.B) {
	typ := reflect.TypeFor[*legacyMethodModel]()
	b.Run("cached", func(b *testing.B) {
		if _, err := loadMetadata(typ); err != nil {
			b.Fatal(err)
		}
		b.ReportAllocs()
		for b.Loop() {
			if _, err := loadMetadata(typ); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("uncached", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := parseMetadata(typ); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkRead64Rows(b *testing.B) {
	state := &testDatabase{columns: []string{"id", "name", "score"}}
	for i := range 64 {
		state.data = append(state.data, []driver.Value{int64(i), "record", int64(1)})
	}
	db := sql.OpenDB(testConnector{state})
	defer db.Close()
	m := New(&legacyMethodModel{}).SetRDb(db)
	b.ReportAllocs()
	for b.Loop() {
		rows, err := m.Rows("", nil)
		if err != nil || len(rows) != 64 {
			b.Fatalf("Rows = %d, %v", len(rows), err)
		}
		state.calls = state.calls[:0]
	}
}

package orm

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
)

var (
	_ IDBHandle = (*sql.DB)(nil)
	_ IDBHandle = (*sql.Tx)(nil)
)

func TestNewWithoutRegisteredDatabase(t *testing.T) {
	registerTestDB(t, nil, nil)
	m := New(&legacyMethodModel{})
	for name, operation := range databaseOperations[*legacyMethodModel]() {
		t.Run(name, func(t *testing.T) {
			if err := operation(m); !errors.Is(err, ErrNoDatabase) {
				t.Fatalf("error = %v, want ErrNoDatabase", err)
			}
		})
	}
}

func TestContextOnlyDatabaseHandle(t *testing.T) {
	state := &testDatabase{columns: []string{"id", "name", "score"}, data: [][]driver.Value{{int64(1), "first", int64(2)}}}
	db := openTestDB(t, state)
	m := New(&legacyMethodModel{}).SetRDb(contextOnly{db}).SetWDb(contextOnly{db})
	if row, err := m.Row("id=?", []any{1}); err != nil || row.Name != "first" {
		t.Fatalf("Row = %+v, %v", row, err)
	}
	if rows, err := m.Rows("", nil); err != nil || len(rows) != 1 {
		t.Fatalf("Rows = %+v, %v", rows, err)
	}
	if _, err := m.UpdateByPk(map[string]any{"score": 3}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Save(); err != nil {
		t.Fatal(err)
	}
	if len(state.calls) != 4 || state.lastContext != context.Background() {
		t.Fatal("minimal handle did not execute all operations")
	}
}

func TestDatabaseRouting(t *testing.T) {
	read := &testDatabase{columns: []string{"id", "name", "score"}, data: [][]driver.Value{{int64(1), "replica", int64(2)}}}
	write := &testDatabase{}
	m := New(&legacyMethodModel{}).SetRDb(openTestDB(t, read)).SetWDb(openTestDB(t, write))
	if _, err := m.Row("", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Save(); err != nil {
		t.Fatal(err)
	}
	if len(read.calls) != 1 || len(write.calls) != 1 || m.Record.Name != "replica" {
		t.Fatal("read/write routing changed")
	}
}

func TestNilDatabaseHandles(t *testing.T) {
	var db *sql.DB
	var tx *sql.Tx
	for _, handle := range []IDBHandle{nil, db, tx} {
		m := New(&legacyMethodModel{}).SetRDb(handle).SetWDb(handle)
		if _, err := m.Count("", nil); !errors.Is(err, ErrNoDatabase) {
			t.Fatal(err)
		}
		if _, err := m.Save(); !errors.Is(err, ErrNoDatabase) {
			t.Fatal(err)
		}
		if _, err := m.UpdateByPk(map[string]any{"name": "value"}, 1); !errors.Is(err, ErrNoDatabase) {
			t.Fatal(err)
		}
	}
}

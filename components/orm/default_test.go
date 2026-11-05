package orm

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"
	"testing"
)

func registerTestDB(t *testing.T, read, write IDBHandle) {
	t.Helper()
	previousRead, previousWrite := registeredDatabases()
	t.Cleanup(func() { RegisterDB(previousRead, previousWrite) })
	RegisterDB(read, write)
}

func TestRegisteredDatabaseRoutingAndReplacement(t *testing.T) {
	readState := &testDatabase{columns: []string{"id", "name", "score"}, data: [][]driver.Value{{int64(1), "replica", int64(2)}}}
	writeState := &testDatabase{}
	read, write := openTestDB(t, readState), openTestDB(t, writeState)
	registerTestDB(t, read, write)
	original := New(&legacyMethodModel{})
	if _, err := original.Row("", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := original.Save(); err != nil {
		t.Fatal(err)
	}
	if len(readState.calls) != 1 || len(writeState.calls) != 1 || original.Record.Name != "replica" {
		t.Fatal("registered read/write routing changed")
	}

	replacementState := &testDatabase{}
	replacement := openTestDB(t, replacementState)
	RegisterDB(replacement, replacement)
	if _, err := New(&legacyMethodModel{}).Save(); err != nil {
		t.Fatal(err)
	}
	if err := AutoTransaction(func(tx IDBHandle) error {
		_, err := tx.ExecContext(context.Background(), "UPDATE records SET score=1")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if replacementState.commits != 1 || len(replacementState.calls) != 2 || writeState.commits != 0 {
		t.Fatal("new operations did not use the replacement database")
	}
	RegisterDB(nil, nil)
	if _, err := original.Save(); err != nil || len(writeState.calls) != 2 {
		t.Fatalf("existing instance lost its database snapshot: %v", err)
	}
	for _, db := range []*sql.DB{read, write, replacement} {
		if err := db.Ping(); err != nil {
			t.Fatalf("registration closed a caller-owned pool: %v", err)
		}
	}
}

func TestRegisteredDatabaseOverridesAndValidation(t *testing.T) {
	globalState, localState := &testDatabase{}, &testDatabase{}
	global, local := openTestDB(t, globalState), openTestDB(t, localState)
	registerTestDB(t, global, global)
	m := New(&legacyMethodModel{}).SetRDb(local).SetWDb(local)
	if _, err := m.Save(); err != nil {
		t.Fatal(err)
	}
	if len(localState.calls) != 1 || len(globalState.calls) != 0 {
		t.Fatal("instance database was overridden by global registration")
	}
	unconfigured := New(&legacyMethodModel{}).SetRDb(nil).SetWDb(nil)
	if _, err := unconfigured.Count("", nil); !errors.Is(err, ErrNoDatabase) {
		t.Fatal(err)
	}
	if _, err := unconfigured.Save(); !errors.Is(err, ErrNoDatabase) {
		t.Fatal(err)
	}
	var nilDB *sql.DB
	for _, write := range []IDBHandle{nil, nilDB, contextOnly{local}} {
		RegisterDB(global, write)
		want := ErrNoDatabase
		if !isNil(write) {
			want = ErrTransactionUnsupported
		} else if _, err := New(&legacyMethodModel{}).Save(); !errors.Is(err, want) {
			t.Fatal(err)
		}
		if err := AutoTransactionContext(context.Background(), func(*sql.Tx) error {
			t.Fatal("invalid database invoked callback")
			return nil
		}); !errors.Is(err, want) {
			t.Fatalf("transaction error = %v, want %v", err, want)
		}
	}
}

func TestConcurrentDatabaseRegistration(t *testing.T) {
	first, second := openTestDB(t, &testDatabase{}), openTestDB(t, &testDatabase{})
	registerTestDB(t, first, first)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				db := first
				if (i+j)%2 == 0 {
					db = second
				}
				RegisterDB(db, db)
				m := New(&legacyMethodModel{})
				if m.rdb != m.wdb || (m.rdb != first && m.rdb != second) {
					t.Error("read/write registration snapshot was torn")
					return
				}
			}
		}(i)
	}
	wg.Wait()
}

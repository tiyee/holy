package orm

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
)

func registerTestDB(t *testing.T, read, write *sql.DB) {
	t.Helper()
	resetTestDB(t)
	if err := RegisterDB(read, write); err != nil {
		t.Fatal(err)
	}
}

func resetTestDB(t *testing.T) {
	t.Helper()
	previousState := defaultState.Swap(initUnstarted)
	previousDatabases := defaultDatabases
	defaultDatabases = databasePair{}
	t.Cleanup(func() {
		defaultDatabases = previousDatabases
		defaultState.Store(previousState)
	})
}

func TestRegisteredDatabaseRoutingAndImmutability(t *testing.T) {
	readState := &testDatabase{columns: []string{"id", "name", "score"}, data: [][]driver.Value{{int64(1), "replica", int64(2)}}}
	writeState := &testDatabase{}
	read, write := openTestDB(t, readState), openTestDB(t, writeState)
	resetTestDB(t)
	if err := RegisterDB(nil, nil); !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("empty registration = %v, want ErrNoDatabase", err)
	}
	if state := defaultState.Load(); state != initFailed {
		t.Fatalf("空连接池注册后状态 = %d，预期失败", state)
	}
	if err := RegisterDB(read, write); err != nil {
		t.Fatal(err)
	}
	original := New(&testRecord{})
	if _, err := original.Row("", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := original.Save(); err != nil {
		t.Fatal(err)
	}
	if len(readState.calls) != 1 || len(writeState.calls) != 1 || original.record.Name != "replica" {
		t.Fatal("registered read/write routing changed")
	}

	replacement := openTestDB(t, &testDatabase{})
	if err := RegisterDB(replacement, replacement); !errors.Is(err, ErrDatabaseAlreadyRegistered) {
		t.Fatalf("repeat registration = %v, want ErrDatabaseAlreadyRegistered", err)
	}
	if err := RegisterDB(nil, nil); !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("empty registration = %v, want ErrNoDatabase", err)
	}
	fresh := New(&testRecord{})
	if fresh.rdb != read || fresh.wdb != write {
		t.Fatal("registration changed the global database pair")
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
	m := New(&testRecord{}).SetDB(local)
	if _, err := m.Save(); err != nil {
		t.Fatal(err)
	}
	if len(localState.calls) != 1 || len(globalState.calls) != 0 {
		t.Fatal("instance database was overridden by global registration")
	}
	if fresh := New(&testRecord{}); fresh.rdb != global || fresh.wdb != global {
		t.Fatal("instance override changed the global database pair")
	}
	unconfigured := New(&testRecord{}).SetRDb(nil).SetWDb(nil)
	if _, err := unconfigured.Count("", nil); !errors.Is(err, ErrNoDatabase) {
		t.Fatal(err)
	}
	if _, err := unconfigured.Save(); !errors.Is(err, ErrNoDatabase) {
		t.Fatal(err)
	}
	t.Run("missing read pool", func(t *testing.T) {
		registerTestDB(t, nil, global)
		if _, err := New(&testRecord{}).Count("", nil); !errors.Is(err, ErrNoDatabase) {
			t.Fatalf("Count with no read pool = %v, want ErrNoDatabase", err)
		}
	})
	t.Run("missing write pool", func(t *testing.T) {
		registerTestDB(t, global, nil)
		if _, err := New(&testRecord{}).Save(); !errors.Is(err, ErrNoDatabase) {
			t.Fatalf("Save with no write pool = %v, want ErrNoDatabase", err)
		}
	})
}

func TestConcurrentDatabaseRegistration(t *testing.T) {
	first, second := openTestDB(t, &testDatabase{}), openTestDB(t, &testDatabase{})
	resetTestDB(t)
	type result struct {
		pair databasePair
		err  error
	}
	results := make(chan result, 2)
	for _, pair := range []databasePair{{read: first, write: second}, {read: second, write: first}} {
		go func() {
			results <- result{pair: pair, err: RegisterDB(pair.read, pair.write)}
		}()
	}
	winner, other := <-results, <-results
	if winner.err != nil {
		winner, other = other, winner
	}
	if winner.err != nil || !errors.Is(other.err, ErrDatabaseAlreadyRegistered) {
		t.Fatalf("registration results = %v, %v, want one success and one duplicate", winner.err, other.err)
	}
	m := New(&testRecord{})
	if m.rdb != winner.pair.read || m.wdb != winner.pair.write {
		t.Fatal("read/write registration snapshot differs from the successful pair")
	}
}

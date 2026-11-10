package orm

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
	"testing/synctest"
)

func TestBeginTx(t *testing.T) {
	read := &testDatabase{beginErr: errors.New("transaction started on the read database")}
	write := &testDatabase{}
	registerTestDB(t, openTestDB(t, read), openTestDB(t, write))
	opts := &sql.TxOptions{Isolation: sql.LevelReadCommitted, ReadOnly: true}
	tx, err := BeginTx(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	want := driver.TxOptions{Isolation: driver.IsolationLevel(sql.LevelReadCommitted), ReadOnly: true}
	if write.txOptions != want {
		t.Fatalf("driver options = %+v, want %+v", write.txOptions, want)
	}
	if err := tx.Commit(); err != nil || write.commits != 1 {
		t.Fatalf("Commit = %v, write database commits = %d", err, write.commits)
	}
}

func TestBeginTxInvalidInput(t *testing.T) {
	registerTestDB(t, openTestDB(t, &testDatabase{}), nil)
	if tx, err := BeginTx(nil, nil); tx != nil || !errors.Is(err, ErrInvalidContext) {
		t.Fatalf("BeginTx with nil context = %v, %v", tx, err)
	}
	if tx, err := BeginTx(t.Context(), nil); tx != nil || !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("BeginTx without write database = %v, %v", tx, err)
	}
}

func TestBeginTxCancelsConnectionWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		db := openTestDB(t, &testDatabase{})
		registerTestDB(t, nil, db)
		conn, err := db.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			tx, err := BeginTx(ctx, nil)
			if tx != nil {
				_ = tx.Rollback()
			}
			done <- err
		}()
		synctest.Wait()
		cancel()
		synctest.Wait()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("BeginTx after cancellation = %v", err)
			}
		default:
			_ = conn.Close()
			<-done
			t.Fatal("BeginTx still waited for a connection after cancellation")
		}
	})
}

func TestBeginTxRollsBackOnCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		write := &testDatabase{}
		registerTestDB(t, nil, openTestDB(t, write))
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		tx, err := BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		cancel()
		synctest.Wait()
		if write.rollbacks != 1 {
			t.Fatalf("rollbacks after cancellation = %d, want 1", write.rollbacks)
		}
	})
}

func TestAutoTransaction(t *testing.T) {
	beginErr := errors.New("begin failed")
	callbackErr := errors.New("callback failed")
	commitErr := errors.New("commit failed")
	rollbackErr := errors.New("rollback failed")
	for _, tc := range []struct {
		name        string
		beginErr    error
		callbackErr error
		commitErr   error
		rollbackErr error
		commits     int
		rollbacks   int
	}{
		{name: "commit", commits: 1},
		{name: "begin error", beginErr: beginErr},
		{name: "callback error", callbackErr: callbackErr, rollbacks: 1},
		{name: "commit error", commitErr: commitErr, commits: 1},
		{name: "rollback error", callbackErr: callbackErr, rollbackErr: rollbackErr, rollbacks: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := &testDatabase{beginErr: tc.beginErr, commitErr: tc.commitErr, rollbackErr: tc.rollbackErr}
			registerTestDB(t, nil, openTestDB(t, state))
			calls := 0
			err := AutoTransaction(func(db IDBHandle) error {
				calls++
				if tx, ok := db.(*sql.Tx); !ok || tx == nil {
					t.Fatalf("callback received %T, want a non-nil *sql.Tx", db)
				}
				return tc.callbackErr
			})
			wantErr := errors.Join(tc.beginErr, tc.callbackErr, tc.commitErr, tc.rollbackErr)
			if wantErr == nil && err != nil {
				t.Fatalf("AutoTransaction = %v", err)
			}
			for _, cause := range []error{tc.beginErr, tc.callbackErr, tc.commitErr, tc.rollbackErr} {
				if cause != nil && !errors.Is(err, cause) {
					t.Fatalf("AutoTransaction = %v, missing %v", err, cause)
				}
			}
			wantCalls := 1
			if tc.beginErr != nil {
				wantCalls = 0
			}
			if calls != wantCalls || state.commits != tc.commits || state.rollbacks != tc.rollbacks {
				t.Fatalf("calls = %d, commits = %d, rollbacks = %d", calls, state.commits, state.rollbacks)
			}
		})
	}
}

func TestAutoTransactionRollsBackOnPanic(t *testing.T) {
	state := &testDatabase{}
	registerTestDB(t, nil, openTestDB(t, state))
	failure := errors.New("callback panic")
	func() {
		defer func() {
			if got := recover(); got != failure {
				t.Fatalf("panic = %v, want %v", got, failure)
			}
		}()
		_ = AutoTransaction(func(IDBHandle) error { panic(failure) })
	}()
	if state.commits != 0 || state.rollbacks != 1 {
		t.Fatalf("commits = %d, rollbacks = %d", state.commits, state.rollbacks)
	}
}

func TestAutoTransactionInvalidInput(t *testing.T) {
	registerTestDB(t, openTestDB(t, &testDatabase{}), nil)
	if err := AutoTransaction(nil); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatal(err)
	}
	if err := AutoTransaction(func(IDBHandle) error {
		t.Fatal("callback called without a write database")
		return nil
	}); !errors.Is(err, ErrNoDatabase) {
		t.Fatal(err)
	}
}

func TestORMUsesInjectedTransaction(t *testing.T) {
	read := &testDatabase{beginErr: errors.New("transaction started on the read database")}
	write := &testDatabase{columns: []string{"n"}, data: [][]driver.Value{{int64(1)}}}
	readDB, writeDB := openTestDB(t, read), openTestDB(t, write)
	registerTestDB(t, readDB, writeDB)
	original := New(&testRecord{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bound := New(&testRecord{}).WithContext(ctx)
	err := AutoTransaction(func(db IDBHandle) error {
		bound.SetDB(db)
		other := New(&templateRecord{table: "other_records"}).SetDB(db).WithContext(ctx)
		if bound.rdb != db || bound.wdb != db || other.rdb != db || other.wdb != db {
			t.Fatal("SetDB did not bind both handles to the transaction")
		}
		if _, err := bound.Count("", nil); err != nil {
			return err
		}
		if _, err := other.UpdateByPk(map[string]any{"name": "updated"}, 1); err != nil {
			return err
		}
		if n, err := bound.Saves([]*testRecord{{Name: "one"}, {Name: "two"}}); err != nil || n != 2 {
			t.Fatalf("Saves = %d, %v", n, err)
		}
		if write.lastContext != ctx || write.commits != 0 || write.rollbacks != 0 {
			t.Fatal("ORM lost the request Context or finished the caller's transaction")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(write.calls) != 3 || len(read.calls) != 0 || original.wdb != writeDB || original.rdb != readDB {
		t.Fatal("ORM did not isolate transaction routing")
	}
	checkSQLCall(t, write.calls[0], "select count(*) as n from `test_records`")
	checkSQLCall(t, write.calls[1], "update `other_records` set `name`=? where `id`=?", "updated", int64(1))
	if write.commits != 1 || write.rollbacks != 0 {
		t.Fatal("AutoTransaction did not commit the transaction")
	}
	if _, err := bound.Count("", nil); !errors.Is(err, sql.ErrTxDone) {
		t.Fatal(err)
	}
}

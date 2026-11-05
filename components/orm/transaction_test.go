package orm

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
)

func TestAutoTransactionContext(t *testing.T) {
	failure, rollbackFailure := errors.New("transaction failed"), errors.New("rollback failed")
	for _, scenario := range []string{"success", "begin", "callback", "commit", "rollback", "panic"} {
		t.Run(scenario, func(t *testing.T) {
			state := &testDatabase{columns: []string{"n"}, data: [][]driver.Value{{int64(1)}}}
			switch scenario {
			case "begin":
				state.beginErr = failure
			case "commit":
				state.commitErr = failure
			case "rollback":
				state.rollbackErr = rollbackFailure
			}
			db := openTestDB(t, state)
			registerTestDB(t, nil, db)
			ctx := context.Background()
			var panicValue any
			err := func() (err error) {
				defer func() { panicValue = recover() }()
				return AutoTransactionContext(ctx, func(tx *sql.Tx) error {
					if _, err := tx.ExecContext(ctx, "UPDATE first_table SET value=?", 1); err != nil {
						return err
					}
					if _, err := tx.ExecContext(ctx, "UPDATE second_table SET value=?", 2); err != nil {
						return err
					}
					if scenario == "panic" {
						panic("preserved")
					}
					if scenario == "callback" || scenario == "rollback" {
						return failure
					}
					return nil
				})
			}()
			switch scenario {
			case "success":
				if err != nil {
					t.Fatal(err)
				}
			case "panic":
				if panicValue != "preserved" {
					t.Fatalf("panic = %v", panicValue)
				}
			default:
				if !errors.Is(err, failure) {
					t.Fatal(err)
				}
			}
			if scenario == "rollback" && !errors.Is(err, rollbackFailure) {
				t.Fatalf("rollback error lost: %v", err)
			}
			commits, rollbacks := 0, 0
			switch scenario {
			case "success", "commit":
				commits = 1
			case "callback", "rollback", "panic":
				rollbacks = 1
			}
			if state.commits != commits || state.rollbacks != rollbacks || state.lastContext != ctx {
				t.Fatalf("transaction lifecycle: commits=%d rollbacks=%d", state.commits, state.rollbacks)
			}
		})
	}
}

func TestAutoTransactionContextValidation(t *testing.T) {
	state := &testDatabase{}
	db := openTestDB(t, state)
	registerTestDB(t, nil, db)
	ctx := context.Background()
	unexpected := func(*sql.Tx) error { t.Fatal("invalid transaction invoked callback"); return nil }
	if err := AutoTransaction(nil); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatal(err)
	}
	if err := AutoTransactionContext(ctx, nil); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatal(err)
	}
	if err := AutoTransactionContext(nil, unexpected); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := AutoTransactionContext(cancelled, unexpected); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	db = nil
	RegisterDB(nil, db)
	if err := AutoTransaction(func(IDBHandle) error { t.Fatal("missing database invoked callback"); return nil }); !errors.Is(err, ErrNoDatabase) {
		t.Fatal(err)
	}
	if err := AutoTransactionContext(ctx, unexpected); !errors.Is(err, ErrNoDatabase) {
		t.Fatal(err)
	}
	if state.commits != 0 || state.rollbacks != 0 {
		t.Fatal("invalid transaction changed lifecycle")
	}
}

func TestORMUsesInjectedTransaction(t *testing.T) {
	read := &testDatabase{queryErr: errors.New("read escaped transaction")}
	write := &testDatabase{columns: []string{"n"}, data: [][]driver.Value{{int64(1)}}}
	readDB, writeDB := openTestDB(t, read), openTestDB(t, write)
	registerTestDB(t, readDB, writeDB)
	m := New(&legacyMethodModel{})
	tx, err := writeDB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	bound := New(&legacyMethodModel{}).SetRDb(tx).SetWDb(tx)
	if _, err := bound.Count("", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := bound.UpdateByPk(map[string]any{"score": 1}, 1); err != nil {
		t.Fatal(err)
	}
	if n, err := bound.Saves([]*legacyMethodModel{{Name: "one"}, {Name: "two"}}); err != nil || n != 2 {
		t.Fatalf("Saves = %d, %v", n, err)
	}
	if write.commits != 0 || write.rollbacks != 0 || len(read.calls) != 0 || m.wdb != writeDB || m.rdb != readDB {
		t.Fatal("ORM took transaction ownership or changed routing")
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := bound.Count("", nil); !errors.Is(err, sql.ErrTxDone) {
		t.Fatal(err)
	}
}

func TestRepositoryAutoTransaction(t *testing.T) {
	for _, scenario := range []string{"commit", "rollback", "panic"} {
		t.Run(scenario, func(t *testing.T) {
			state := &testDatabase{columns: []string{"n"}, data: [][]driver.Value{{int64(1)}}}
			wrong := &testDatabase{queryErr: errors.New("escaped transaction")}
			write := openTestDB(t, state)
			read := openTestDB(t, wrong)
			registerTestDB(t, read, write)
			original := New(&legacyMethodModel{})
			ctx := context.WithValue(context.Background(), struct{}{}, "transaction")
			failure := errors.New("callback failed")
			var panicked any
			err := func() (err error) {
				defer func() { panicked = recover() }()
				return AutoTransaction(func(tx IDBHandle) error {
					// Repository 将同一事务注入不同单表 ORM，并传递请求 Context。
					bound := New(&legacyMethodModel{}).SetRDb(tx).SetWDb(tx).WithContext(ctx)
					other := New(&compatibilityModel{table: "other_records"}).SetRDb(tx).SetWDb(tx).WithContext(ctx)
					if _, err := bound.Count("", nil); err != nil {
						return err
					}
					if _, err := other.UpdateByPk(map[string]any{"name": "updated"}, 1); err != nil {
						return err
					}
					if state.lastContext != ctx || state.commits != 0 || state.rollbacks != 0 {
						t.Fatal("single-table operations lost Context or finished the transaction")
					}
					if scenario == "panic" {
						panic("preserved")
					}
					if scenario == "rollback" {
						return failure
					}
					return nil
				})
			}()
			if scenario == "commit" && (err != nil || state.commits != 1) {
				t.Fatalf("commit: %v", err)
			}
			if scenario == "rollback" && !errors.Is(err, failure) {
				t.Fatal(err)
			}
			if scenario == "panic" && panicked != "preserved" {
				t.Fatal(panicked)
			}
			if scenario != "commit" && state.rollbacks != 1 {
				t.Fatal("missing rollback")
			}
			if len(wrong.calls) != 0 || original.wdb != write || original.rdb != read || original.context() != context.Background() {
				t.Fatal("transaction routing leaked")
			}
			if len(state.calls) != 2 {
				t.Fatalf("repository calls = %d, want 2", len(state.calls))
			}
			checkLegacyCall(t, state.calls[0], "select count(*) as n from `test_records`")
			checkLegacyCall(t, state.calls[1], "update `other_records` set `name`=? where `id`=?", "updated", int64(1))
		})
	}
}
func TestTransactionOptionsAndValidation(t *testing.T) {
	state := &testDatabase{}
	db := openTestDB(t, state)
	opts := &sql.TxOptions{Isolation: sql.LevelSerializable, ReadOnly: true}
	if err := WithinTransaction(context.Background(), db, opts, func(*sql.Tx) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if state.beginOptions.Isolation != driver.IsolationLevel(sql.LevelSerializable) || !state.beginOptions.ReadOnly {
		t.Fatal("transaction options lost")
	}
	registerTestDB(t, db, contextOnly{db})
	if err := AutoTransaction(func(IDBHandle) error {
		t.Fatal("unsupported database invoked callback")
		return nil
	}); !errors.Is(err, ErrTransactionUnsupported) {
		t.Fatal(err)
	}
}

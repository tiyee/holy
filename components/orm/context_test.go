package orm

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"
	"time"
)

func TestDatabaseOperationsUseContext(t *testing.T) {
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "request")
	for name, operation := range databaseOperations[*testRecord]() {
		t.Run(name, func(t *testing.T) {
			state := &testDatabase{
				columns: []string{"id", "name", "score"},
				data:    [][]driver.Value{{int64(1), "first", int64(2)}},
			}
			if name == "Count" || name == "Sum" {
				state.columns, state.data = []string{"n"}, [][]driver.Value{{int64(1)}}
			}
			db := openTestDB(t, state)
			original := New(&testRecord{}).SetRDb(db).SetWDb(db)
			bound := original.WithContext(ctx)
			if bound == original || bound.record != original.record || bound.meta != original.meta || original.context() != context.Background() {
				t.Fatal("WithContext did not preserve the original ORM")
			}
			if err := operation(bound); err != nil {
				t.Fatal(err)
			}
			if state.lastContext != ctx {
				t.Fatal("database operation lost the request Context")
			}
			if bound.WithContext(nil).context() != context.Background() || bound.context() != ctx {
				t.Fatal("nil Context did not restore the default on a copy")
			}
		})
	}
}

func TestDatabaseOperationsRespectCancellation(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		want := context.Canceled
		if deadline {
			cancel()
			ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			want = context.DeadlineExceeded
		}
		cancel()
		for name, operation := range databaseOperations[*testRecord]() {
			t.Run(want.Error()+"/"+name, func(t *testing.T) {
				state := &testDatabase{}
				db := openTestDB(t, state)
				m := New(&testRecord{}).SetRDb(db).SetWDb(db).WithContext(ctx)
				if err := operation(m); !errors.Is(err, want) {
					t.Fatalf("error = %v, want %v", err, want)
				}
				if len(state.calls) != 0 {
					t.Fatal("cancelled operation reached the database")
				}
			})
		}
	}
}

func TestContextAndTransactionCompose(t *testing.T) {
	state := &testDatabase{columns: []string{"n"}, data: [][]driver.Value{{int64(1)}}}
	db := openTestDB(t, state)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, bound := range []*ORM[*testRecord]{
		New(&testRecord{}).SetRDb(tx).SetWDb(tx).WithContext(ctx),
		New(&testRecord{}).WithContext(ctx).SetRDb(tx).SetWDb(tx),
	} {
		if _, err := bound.Count("", nil); err != nil {
			t.Fatal(err)
		}
		if state.lastContext != ctx {
			t.Fatal("injected transaction discarded the Context")
		}
	}
	if state.commits != 0 || state.rollbacks != 0 {
		t.Fatal("ORM changed transaction ownership")
	}
}

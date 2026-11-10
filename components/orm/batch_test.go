package orm

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestSavesInBatches(t *testing.T) {
	state := &testDatabase{}
	m := New(&testRecord{}).SetWDb(openTestDB(t, state))
	rows := []*testRecord{{ID: 1}, {ID: 2}, {ID: 3}, {ID: 4}, {ID: 5}}
	n, err := m.SavesInBatches(rows, 2)
	if err != nil || n != 5 || len(state.calls) != 3 {
		t.Fatalf("SavesInBatches = %d, %v; calls = %d", n, err, len(state.calls))
	}
	checkSQLCall(t, state.calls[0], "insert into `test_records` (`id`,`name`,`score`) values (?,?,?),(?,?,?)", int64(1), "", int64(0), int64(2), "", int64(0))
	checkSQLCall(t, state.calls[1], "insert into `test_records` (`id`,`name`,`score`) values (?,?,?),(?,?,?)", int64(3), "", int64(0), int64(4), "", int64(0))
	checkSQLCall(t, state.calls[2], "insert into `test_records` (`id`,`name`,`score`) values (?,?,?)", int64(5), "", int64(0))
	if state.commits != 0 || state.rollbacks != 0 {
		t.Fatal("batch insert changed transaction ownership")
	}
	actual := int64(7)
	state.affectedRows = &actual
	if n, err := m.SavesInBatches(rows, 2); err != nil || n != 21 {
		t.Fatalf("database RowsAffected sum = %d, %v", n, err)
	}
}

func TestSavesInBatchesValidation(t *testing.T) {
	state := &testDatabase{}
	db := openTestDB(t, state)
	m := New[ITable](&testRecord{}).SetWDb(db)
	for _, rows := range [][]ITable{
		{&testRecord{}, &testRecord{}, nil},
		{&testRecord{}, &testRecord{}, (*testRecord)(nil)},
		{&testRecord{}, &testRecord{}, &cachedModel{}},
	} {
		if n, err := m.SavesInBatches(rows, 1); n != 0 || !errors.Is(err, ErrInvalidModel) {
			t.Fatalf("invalid batch = %d, %v", n, err)
		}
	}
	for _, size := range []int{0, -1} {
		if _, err := m.SavesInBatches(nil, size); !errors.Is(err, ErrInvalidBatchSize) {
			t.Fatalf("batch size %d: %v", size, err)
		}
	}
	if n, err := m.SavesInBatches(nil, 1); n != 0 || err != nil {
		t.Fatalf("empty batch = %d, %v", n, err)
	}
	empty := New(&unmappedModel{}).SetWDb(db)
	if _, err := empty.SavesInBatches([]*unmappedModel{{}}, 1); err == nil {
		t.Fatal("empty field mapping accepted")
	}
	if len(state.calls) != 0 {
		t.Fatal("validation failed after partially writing the batch")
	}
	if _, err := m.SetWDb(nil).SavesInBatches([]ITable{&testRecord{}}, 1); !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("missing database error = %v", err)
	}
}

func TestSavesInBatchesFailure(t *testing.T) {
	failure := errors.New("second batch failed")
	for _, transactional := range []bool{false, true} {
		t.Run(map[bool]string{false: "pool", true: "transaction"}[transactional], func(t *testing.T) {
			state := &testDatabase{execErr: failure, execFailAt: 2}
			db := openTestDB(t, state)
			m := New(&testRecord{}).SetWDb(db)
			var tx *sql.Tx
			if transactional {
				var err error
				tx, err = db.Begin()
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				m.SetRDb(tx).SetWDb(tx)
			}
			n, err := m.SavesInBatches([]*testRecord{{}, {}, {}, {}, {}}, 2)
			if n != 2 || !errors.Is(err, failure) || len(state.calls) != 2 {
				t.Fatalf("failure = %d, %v; calls = %d", n, err, len(state.calls))
			}
			if state.commits != 0 || state.rollbacks != 0 {
				t.Fatal("batch method committed or rolled back the caller's transaction")
			}
			if tx != nil {
				if err := tx.Rollback(); err != nil || state.rollbacks != 1 {
					t.Fatalf("caller rollback = %v", err)
				}
			}
		})
	}
}

func TestSavesInBatchesParameterLimit(t *testing.T) {
	// Three mapped columns: 21,846 rows would exceed the MySQL parameter limit
	// if the caller's requested batch size were used without adjustment.
	rows := make([]*testRecord, 21846)
	for i := range rows {
		rows[i] = &testRecord{ID: int64(i + 1)}
	}
	state := &testDatabase{}
	m := New(&testRecord{}).SetWDb(openTestDB(t, state))
	if n, err := m.SavesInBatches(rows, len(rows)); err != nil || n != int64(len(rows)) {
		t.Fatalf("large batch = %d, %v", n, err)
	}
	if len(state.calls) != 2 || len(state.calls[0].args) != 65535 || len(state.calls[1].args) != 3 {
		t.Fatal("batch exceeded the MySQL parameter limit or lost rows")
	}
}

type cancelAfterExec struct {
	IDBHandle
	cancel context.CancelFunc
}

func (db cancelAfterExec) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	result, err := db.IDBHandle.ExecContext(ctx, query, args...)
	db.cancel()
	return result, err
}

func TestSavesInBatchesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	state := &testDatabase{}
	db := cancelAfterExec{IDBHandle: openTestDB(t, state), cancel: cancel}
	m := New(&testRecord{}).SetWDb(db).WithContext(ctx)
	n, err := m.SavesInBatches([]*testRecord{{}, {}, {}}, 2)
	if n != 2 || !errors.Is(err, context.Canceled) || len(state.calls) != 1 {
		t.Fatalf("cancelled batches = %d, %v; calls = %d", n, err, len(state.calls))
	}
}

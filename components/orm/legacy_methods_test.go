package orm

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestLegacyMethodTemplateAndMetadata(t *testing.T) {
	state := &testDatabase{columns: []string{"id", "name", "score"}, data: [][]driver.Value{{int64(1), "first", int64(10)}}}
	db := openTestDB(t, state)
	record := &compatibilityModel{table: "cached_legacy", Template: "keep"}
	m := New(record).SetRDb(db).SetWDb(db)
	if m.Fields() != "`id`,`name`,`score`" {
		t.Fatal("legacy mapping used db tags")
	}
	meta := m.meta
	other := New(&compatibilityModel{table: "other_table"})
	other.Fields()
	if other.meta != meta {
		t.Fatal("legacy methods did not share cached metadata")
	}
	row, err := m.Row("id=?", []any{1})
	if err != nil || row != record || record.Name != "first" {
		t.Fatalf("Row = %+v, %v", row, err)
	}
	state.data = [][]driver.Value{{int64(2), "partial", "bad integer"}}
	if row, err := m.Row("", nil); err == nil || row != record || record.ID != 2 || record.Name != "partial" || record.Score != 10 {
		t.Fatalf("partial Row = %+v, %v", row, err)
	}
	m.Record = &compatibilityModel{table: "replacement_table", Template: "replacement"}
	state.data = [][]driver.Value{{int64(3), "third", int64(30)}, {int64(4), "fourth", int64(40)}}
	rows, err := m.Rows("", nil)
	if err != nil || len(rows) != 2 || rows[0] == rows[1] || rows[0] == m.Record || rows[0].Template != "replacement" || rows[0].table != "replacement_table" || m.Record.ID != 0 || m.meta != meta {
		t.Fatalf("Rows = %+v, %v", rows, err)
	}
	checkLegacyCall(t, state.calls[2], "select `id`,`name`,`score` from `replacement_table`")
	if binds := Binds(m.Record); binds[1] != &m.Record.Name || binds[1] == &record.Name {
		t.Fatal("Binds reused addresses from an earlier record")
	}
}

func TestLegacyMethodSavesMinimalHandle(t *testing.T) {
	failure := errors.New("batch failed")
	for _, fail := range []bool{false, true} {
		state := &testDatabase{}
		if fail {
			state.execErr = failure
		}
		db := openTestDB(t, state)
		m := New(&legacyMethodModel{}).SetWDb(contextOnly{db})
		n, err := m.Saves([]*legacyMethodModel{{ID: 1}, {ID: 2}})
		if fail && (n != 0 || !errors.Is(err, failure)) || !fail && (n != 2 || err != nil) {
			t.Fatalf("Saves = %d, %v", n, err)
		}
		if len(state.calls) != 1 || state.commits != 0 || state.rollbacks != 0 || state.stmtsClosed != 0 {
			t.Fatal("Saves did not execute exactly one statement on a minimal handle")
		}
	}
}

func TestLegacyMethodPaginationDefaults(t *testing.T) {
	state := &testDatabase{columns: []string{"id", "name", "score"}}
	m := New(&legacyMethodModel{}).SetRDb(openTestDB(t, state))
	if _, err := m.Limit("", nil, 5, 0, ""); err != nil {
		t.Fatal(err)
	}
	checkLegacyCall(t, state.calls[0], "select `id`,`name`,`score` from `test_records` limit 5 , 20")
	if _, err := m.Limit(" ", nil, -1, -1, " "); err != nil {
		t.Fatal(err)
	}
	checkLegacyCall(t, state.calls[1], "select `id`,`name`,`score` from `test_records` limit 20")
}

type legacyMethodModel struct {
	ID        int64  `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Score     int64  `json:"score"`
	Ignored   string `json:"-"`
	Transient string
	NoName    string `json:",omitempty"`
	hidden    string
}

func (*legacyMethodModel) TableName() string { return "test_records" }
func (*legacyMethodModel) Pk() string        { return "id" }

func TestLegacyMethodFieldMapping(t *testing.T) {
	model := &legacyMethodModel{}
	m := New(model)
	if got := m.Fields(); got != "`id`,`name`,`score`" {
		t.Fatalf("Fields() = %q", got)
	}
	binds := Binds(model)
	if len(binds) != 3 {
		t.Fatalf("Binds() length = %d", len(binds))
	}
	if binds[0] != &model.ID || binds[1] != &model.Name || binds[2] != &model.Score {
		t.Fatalf("Binds() does not match column order: %v", binds)
	}
}

func TestLegacyMethodMappedWrites(t *testing.T) {
	state := &testDatabase{}
	db := openTestDB(t, state)
	model := &legacyMethodModel{ID: 7, Name: "first", Score: 8, Ignored: "skip", Transient: "skip", hidden: "skip"}
	m := New(model).SetWDb(db)
	id, err := m.Save()
	if err != nil || id != 42 {
		t.Fatalf("Save() = %d, %v", id, err)
	}
	checkLegacyCall(t, state.calls[0], "insert into `test_records` (`id`,`name`,`score`) values (?,?,?)", int64(7), "first", int64(8))
	n, err := m.Update(7)
	if err != nil || n != 1 {
		t.Fatalf("Update() = %d, %v", n, err)
	}
	checkLegacyCall(t, state.calls[1], "update `test_records` set `name`=?,`score`=? where `id`=?", "first", int64(8), int64(7))
	state.prepareErr = errors.New("Saves must not prepare a statement")
	n, err = m.Saves([]*legacyMethodModel{model, {ID: 9, Name: "second", Score: 10}})
	if err != nil || n != 2 {
		t.Fatalf("Saves() = %d, %v", n, err)
	}
	if len(state.calls) != 3 || state.stmtsClosed != 0 {
		t.Fatal("Saves did not execute exactly one statement")
	}
	checkLegacyCall(t, state.calls[2], "insert into `test_records` (`id`,`name`,`score`) values (?,?,?),(?,?,?)", int64(7), "first", int64(8), int64(9), "second", int64(10))
}

func TestLegacyMethodWriteErrors(t *testing.T) {
	failure := errors.New("write failed")
	for _, operation := range []string{"Save", "Saves", "Update", "UpdateByPk", "Delete"} {
		t.Run(operation, func(t *testing.T) {
			state := &testDatabase{execErr: failure}
			m := New(&legacyMethodModel{}).SetWDb(openTestDB(t, state))
			var err error
			switch operation {
			case "Save":
				_, err = m.Save()
			case "Saves":
				_, err = m.Saves([]*legacyMethodModel{{}})
			case "Update":
				_, err = m.Update(1)
			case "UpdateByPk":
				_, err = m.UpdateByPk(map[string]interface{}{"name": "value"}, 1)
			case "Delete":
				_, err = m.Delete(1)
			}
			if !errors.Is(err, failure) {
				t.Errorf("error = %v, want %v", err, failure)
			}
			if operation == "Saves" && (state.execCount != 1 || state.stmtsClosed != 0) {
				t.Error("failed batch executed more than once or used a statement")
			}
		})
	}
}

func TestLegacyMethodBatchFailures(t *testing.T) {
	failure := errors.New("batch failed")
	for _, operation := range []string{"exec", "affected rows"} {
		t.Run(operation, func(t *testing.T) {
			state := &testDatabase{}
			if operation == "exec" {
				state.execErr = failure
			} else {
				state.affectedErr = failure
			}
			m := New(&legacyMethodModel{}).SetWDb(openTestDB(t, state))
			n, err := m.Saves([]*legacyMethodModel{{ID: 1}, {ID: 2}, {ID: 3}})
			if n != 0 || !errors.Is(err, failure) {
				t.Fatalf("Saves = %d, %v", n, err)
			}
			if state.execCount != 1 || len(state.calls) != 1 || state.stmtsClosed != 0 {
				t.Fatal("batch did not execute exactly once")
			}
		})
	}
}

func TestSavesRowsAffected(t *testing.T) {
	// A driver-reported result must be returned rather than the input length.
	for _, affected := range []int64{0, 5} {
		state := &testDatabase{affectedRows: &affected}
		m := New(&legacyMethodModel{}).SetWDb(openTestDB(t, state))
		n, err := m.Saves([]*legacyMethodModel{{ID: 1}, {ID: 2}})
		if err != nil || n != affected || state.execCount != 1 {
			t.Fatalf("Saves = %d, %v; want %d", n, err, affected)
		}
	}
}

func TestSavesInvalidRecords(t *testing.T) {
	for _, rows := range [][]ITable{
		{&legacyMethodModel{}, (*legacyMethodModel)(nil)},
		{&legacyMethodModel{}, &cachedProjection{}},
		{&legacyMethodModel{}, valueModel{}},
	} {
		state := &testDatabase{}
		m := New[ITable](&legacyMethodModel{}).SetWDb(openTestDB(t, state))
		if n, err := m.Saves(rows); n != 0 || !errors.Is(err, ErrInvalidModel) {
			t.Fatalf("Saves = %d, %v", n, err)
		}
		if len(state.calls) != 0 {
			t.Fatal("invalid batch performed database I/O")
		}
	}
}

func TestLegacyMethodReadRows(t *testing.T) {
	failure := errors.New("read failed")
	for _, limit := range []bool{false, true} {
		for _, scenario := range []string{"success", "empty", "query error", "scan error", "iteration error", "close error"} {
			name := map[bool]string{false: "Rows", true: "Limit"}[limit] + "/" + scenario
			t.Run(name, func(t *testing.T) {
				state := &testDatabase{columns: []string{"id", "name", "score"}, data: [][]driver.Value{{int64(1), "first", int64(8)}, {int64(2), "second", int64(9)}}}
				switch scenario {
				case "empty":
					state.data = nil
				case "query error":
					state.queryErr = failure
				case "scan error":
					state.data[1][0] = "not an integer"
				case "iteration error":
					state.nextErr = failure
				case "close error":
					state.closeErr = failure
				}
				m := New(&legacyMethodModel{}).SetRDb(openTestDB(t, state))
				var rows []*legacyMethodModel
				var err error
				if limit {
					rows, err = m.Limit("1=1", nil, 0, 10, "id")
				} else {
					rows, err = m.Rows("1=1", nil)
				}
				if scenario == "success" || scenario == "empty" {
					if err != nil {
						t.Fatal(err)
					}
					if rows == nil {
						t.Error("empty results must be a non-nil slice")
					}
					if scenario == "success" && (len(rows) != 2 || rows[0].ID != 1 || rows[1].ID != 2 || rows[0] == rows[1]) {
						t.Errorf("rows = %+v", rows)
					}
					if scenario == "empty" && len(rows) != 0 {
						t.Errorf("empty result has %d rows", len(rows))
					}
				} else {
					if err == nil {
						t.Fatal("read failure returned success")
					}
					if scenario != "scan error" && !errors.Is(err, failure) {
						t.Errorf("error = %v", err)
					}
					if rows != nil {
						t.Error("returned partial results on failure")
					}
				}
				if scenario != "query error" && state.rowsClosed != 1 {
					t.Errorf("rows closed %d times", state.rowsClosed)
				}
				query := "select `id`,`name`,`score` from `test_records` where 1=1"
				if limit {
					query += " order by id limit 10"
				}
				checkLegacyCall(t, state.calls[0], query)
			})
		}
	}
}

func TestLegacyMethodRowAndAggregates(t *testing.T) {
	state := &testDatabase{columns: []string{"id", "name", "score"}, data: [][]driver.Value{{int64(4), "row", int64(5)}}}
	m := New(&legacyMethodModel{}).SetRDb(openTestDB(t, state))
	row, err := m.Row("0", nil)
	if err != nil || row.ID != 4 || row.Name != "row" || row.Score != 5 {
		t.Fatalf("Row() = %+v, %v", row, err)
	}
	checkLegacyCall(t, state.calls[0], "select `id`,`name`,`score` from `test_records` where 0")
	state.columns, state.data = []string{"n"}, [][]driver.Value{{int64(3)}}
	if n, err := m.Count("1=1", nil); err != nil || n != 3 {
		t.Fatalf("Count() = %d, %v", n, err)
	}
	checkLegacyCall(t, state.calls[1], "select count(*) as n from `test_records` where 1=1")
	state.data = [][]driver.Value{{nil}}
	if n, err := m.Sum("score", "0", nil); err != nil || n != 0 {
		t.Fatalf("empty Sum() = %d, %v", n, err)
	}
	checkLegacyCall(t, state.calls[2], "select sum(`score`) as n from `test_records` where 0")
	state.data = [][]driver.Value{{int64(12)}}
	if n, err := m.Sum("score", "", nil); err != nil || n != 12 {
		t.Fatalf("Sum() = %d, %v", n, err)
	}
	state.data, state.columns = nil, []string{"id", "name", "score"}
	if _, err := m.Row("", nil); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("missing Row() error = %v", err)
	}
}

func TestLegacyMethodEmptyWrites(t *testing.T) {
	m := New(&legacyMethodModel{}) // No database: empty operations must not execute SQL.
	if n, err := m.Saves(nil); err != nil || n != 0 {
		t.Errorf("empty Saves() = %d, %v", n, err)
	}
	if _, err := m.UpdateByPk(nil, 1); err == nil {
		t.Error("empty update accepted")
	}
	noFields := New(&legacyMethodEmptyModel{})
	if _, err := noFields.Save(); err == nil {
		t.Error("insert with no mapped fields accepted")
	}
	if _, err := noFields.Update(1); err == nil {
		t.Error("update with no mapped fields accepted")
	}
}

type legacyMethodEmptyModel struct{ Value string }

func (*legacyMethodEmptyModel) TableName() string { return "empty" }
func (*legacyMethodEmptyModel) Pk() string        { return "id" }

func TestLegacyMethodUpdateByPkAndIdentifiers(t *testing.T) {
	state := &testDatabase{}
	m := New(&legacyMethodModel{}).SetWDb(openTestDB(t, state))
	if n, err := m.UpdateByPk(map[string]interface{}{"score": int64(8), "na`me": "value"}, 4); err != nil || n != 1 {
		t.Fatalf("UpdateByPk() = %d, %v", n, err)
	}
	checkLegacyCall(t, state.calls[0], "update `test_records` set `na``me`=?,`score`=? where `id`=?", "value", int64(8), int64(4))
}

func TestLegacyMethodAutoTransaction(t *testing.T) {
	failure := errors.New("transaction failed")
	rollbackFailure := errors.New("rollback failed")
	for _, scenario := range []string{"success", "begin error", "write error", "commit error", "rollback error", "panic"} {
		t.Run(scenario, func(t *testing.T) {
			state := &testDatabase{}
			switch scenario {
			case "begin error":
				state.beginErr = failure
			case "write error", "rollback error":
				state.execErr = failure
			case "commit error":
				state.commitErr = failure
			}
			if scenario == "rollback error" {
				state.rollbackErr = rollbackFailure
			}
			db := openTestDB(t, state)
			registerTestDB(t, nil, db)
			var panicValue any
			err := func() (err error) {
				defer func() { panicValue = recover() }()
				return AutoTransaction(func(tx IDBHandle) error {
					if scenario == "panic" {
						panic("callback panic")
					}
					_, err := New(&legacyMethodModel{}).SetWDb(tx).UpdateByPk(map[string]interface{}{"name": "value"}, 1)
					return err
				})
			}()
			if scenario == "panic" {
				if panicValue != "callback panic" {
					t.Errorf("panic not preserved: %v", panicValue)
				}
			} else if scenario == "success" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, failure) {
				t.Errorf("error = %v, want %v", err, failure)
			}
			if scenario == "rollback error" && !errors.Is(err, rollbackFailure) {
				t.Errorf("rollback error lost: %v", err)
			}
			wantCommits, wantRollbacks := 0, 0
			switch scenario {
			case "success", "commit error":
				wantCommits = 1
			case "write error", "rollback error", "panic":
				wantRollbacks = 1
			}
			if state.commits != wantCommits || state.rollbacks != wantRollbacks {
				t.Errorf("commits/rollbacks = %d/%d, want %d/%d", state.commits, state.rollbacks, wantCommits, wantRollbacks)
			}
		})
	}
}

type compatibilityModel struct {
	ID       int64  `json:"id" db:"different_id,auto"`
	Name     string `json:"name" db:"different_name"`
	Score    int64  `json:"score"`
	Template string `json:"-"`
	table    string
}

func (m *compatibilityModel) TableName() string { return m.table }
func (*compatibilityModel) Pk() string          { return "id" }

func checkLegacyCall(t *testing.T, call testCall, query string, args ...driver.Value) {
	t.Helper()
	if strings.Join(strings.Fields(call.query), " ") != strings.Join(strings.Fields(query), " ") {
		t.Errorf("query = %q, want %q", call.query, query)
	}
	if len(args) != len(call.args) || len(args) > 0 && !reflect.DeepEqual(call.args, args) {
		t.Errorf("args = %#v, want %#v", call.args, args)
	}
}

package orm

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

type mysqlIntegrationModel struct {
	ID     int64          `json:"id"`
	Name   string         `json:"name"`
	Amount sql.NullString `json:"amount"`
	Score  int64          `json:"score"`
	table  string
}

func (m *mysqlIntegrationModel) TableName() string { return m.table }
func (*mysqlIntegrationModel) Pk() string          { return "id" }

// Requires a disposable MySQL schema and CREATE/DROP TABLE privileges.
// The DSN is read from the environment and is never logged.
func mysqlIntegrationSetup(t *testing.T) (context.Context, *sql.DB, *ORM[*mysqlIntegrationModel]) {
	t.Helper()
	dsn := os.Getenv("HOLY_MYSQL_DSN")
	if dsn == "" {
		if os.Getenv("HOLY_REQUIRE_MYSQL") == "1" {
			t.Fatal("HOLY_MYSQL_DSN is required for this test run")
		}
		t.Skip("HOLY_MYSQL_DSN is not configured")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	registerTestDB(t, db, db)
	db.SetMaxOpenConns(8)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	table := fmt.Sprintf("orm_test_%d", time.Now().UnixNano())
	_, err = db.ExecContext(ctx, "CREATE TABLE "+quoteIdentifier(table)+" (id BIGINT PRIMARY KEY AUTO_INCREMENT, name VARCHAR(100) NOT NULL UNIQUE, amount DECIMAL(12,2) NULL, score BIGINT NOT NULL) ENGINE=InnoDB")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if _, err := db.ExecContext(cleanupCtx, "DROP TABLE "+quoteIdentifier(table)); err != nil {
			t.Error(err)
		}
	})
	m := New(&mysqlIntegrationModel{table: table})
	return ctx, db, m
}

func TestMySQLIntegration(t *testing.T) {
	ctx, _, m := mysqlIntegrationSetup(t)
	m.record.Name = "one"
	m.record.Amount = sql.NullString{String: "12.50", Valid: true}
	m.record.Score = 4
	id, err := m.Save()
	if err != nil || id <= 0 || m.record.ID != 0 {
		t.Fatalf("Save = %d, %v", id, err)
	}
	row, err := m.Row("id=?", []any{id})
	if err != nil || row != m.record || row.Amount.String != "12.50" {
		t.Fatalf("Row = %+v, %v", row, err)
	}
	if n, err := m.Saves([]*mysqlIntegrationModel{{Name: "two", Score: 1}, {Name: "three", Score: 2}}); err != nil || n != 2 {
		t.Fatalf("Saves = %d, %v", n, err)
	}
	if n, err := m.Count("", nil); err != nil || n != 3 {
		t.Fatalf("Count = %d, %v", n, err)
	}
	if n, err := m.Sum("score", "", nil); err != nil || n != 7 {
		t.Fatalf("Sum = %d, %v", n, err)
	}
	rows, err := m.Rows("id>=?", []any{id})
	if err != nil || len(rows) != 3 || rows[0] == m.record || rows[0] == rows[1] || rows[0].table != m.record.table {
		t.Fatalf("Rows = %+v, %v", rows, err)
	}
	page, err := m.Limit("", nil, 1, 1, "id asc")
	if err != nil || len(page) != 1 || page[0].Name != "two" {
		t.Fatalf("Limit = %+v, %v", page, err)
	}
	m.record.Name, m.record.Score, m.record.Amount = "updated", 0, sql.NullString{}
	if _, err := m.Update(id); err != nil {
		t.Fatal(err)
	}
	row, err = m.Row("id=?", []any{id})
	if err != nil || row.Name != "updated" || row.Score != 0 || row.Amount.Valid {
		t.Fatalf("Update = %+v, %v", row, err)
	}
	if _, err := m.UpdateByPk(map[string]any{"score": 9}, id); err != nil {
		t.Fatal(err)
	}
	tx, err := BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	bound := New(&mysqlIntegrationModel{table: m.record.table}).SetDB(tx).WithContext(ctx)
	if _, err := bound.UpdateByPk(map[string]any{"name": "rolled_back"}, id); err != nil {
		t.Fatal(err)
	}
	if row, err := bound.Row("id=?", []any{id}); err != nil || row.Name != "rolled_back" {
		t.Fatalf("transaction read did not see its write: %+v, %v", row, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	row, err = m.Row("id=?", []any{id})
	if err != nil || row.Name != "updated" || row.Score != 9 {
		t.Fatalf("rollback = %+v, %v", row, err)
	}
	if n, err := m.Delete(id); err != nil || n != 1 {
		t.Fatalf("Delete = %d, %v", n, err)
	}
	if n, err := m.Count("id=?", []any{id}); err != nil || n != 0 {
		t.Fatalf("count after delete = %d, %v", n, err)
	}
}

func TestMySQLIntegrationBatchTransactions(t *testing.T) {
	ctx, db, m := mysqlIntegrationSetup(t)
	m.record.Name = "existing"
	if _, err := m.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Saves([]*mysqlIntegrationModel{{Name: "partial"}, {Name: "existing"}}); err == nil {
		t.Fatal("duplicate batch succeeded")
	}
	if n, err := m.Count("name=?", []any{"partial"}); err != nil || n != 0 {
		t.Fatalf("failed multi-row insert = %d, %v", n, err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	bound := New(&mysqlIntegrationModel{table: m.record.table}).SetWDb(tx).WithContext(ctx)
	if n, err := bound.Saves([]*mysqlIntegrationModel{{Name: "rolled_back"}, {Name: "also_rolled_back"}}); n != 2 || err != nil {
		t.Fatalf("transaction batch = %d, %v", n, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if n, err := m.Count("name in (?,?)", []any{"rolled_back", "also_rolled_back"}); err != nil || n != 0 {
		t.Fatalf("transaction rollback = %d, %v", n, err)
	}
}

func TestMySQLIntegrationChunkedTransactions(t *testing.T) {
	ctx, _, m := mysqlIntegrationSetup(t)
	m = m.WithContext(ctx)
	m.record.Name = "existing"
	if _, err := m.Save(); err != nil {
		t.Fatal(err)
	}
	err := AutoTransaction(func(handle IDBHandle) error {
		bound := New(&mysqlIntegrationModel{table: m.record.table}).SetDB(handle).WithContext(ctx)
		n, err := bound.SavesInBatches([]*mysqlIntegrationModel{
			{Name: "first_batch"}, {Name: "second_batch"}, {Name: "existing"},
		}, 1)
		if n != 2 || err == nil {
			t.Fatalf("chunked insert = %d, %v", n, err)
		}
		return err
	})
	if err == nil {
		t.Fatal("duplicate batch succeeded")
	}
	if n, err := m.Count("name in (?,?)", []any{"first_batch", "second_batch"}); err != nil || n != 0 {
		t.Fatalf("chunked rollback = %d, %v", n, err)
	}
}

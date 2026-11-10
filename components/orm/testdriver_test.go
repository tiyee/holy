package orm

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"
)

// The driver exercises database/sql scanning and transactions without a MySQL server.
type testCall struct {
	query string
	args  []driver.Value
}

type testDatabase struct {
	lastContext                      context.Context
	txOptions                        driver.TxOptions
	idErr, affectedErr               error
	affectedRows                     *int64
	columns                          []string
	data                             [][]driver.Value
	calls                            []testCall
	queryErr, nextErr, closeErr      error
	execErr                          error
	beginErr, commitErr, rollbackErr error
	execFailAt                       int
	execCount, rowsClosed, prepares  int
	commits, rollbacks               int
}

type testConnector struct{ state *testDatabase }

func (c testConnector) Connect(context.Context) (driver.Conn, error) {
	return testConnection{c.state}, nil
}
func (c testConnector) Driver() driver.Driver { return testDriver{} }

type testDriver struct{}

func (testDriver) Open(string) (driver.Conn, error) { return nil, errors.New("use connector") }

type testConnection struct{ state *testDatabase }

func (c testConnection) Close() error { return nil }
func (c testConnection) Begin() (driver.Tx, error) {
	if c.state.beginErr != nil {
		return nil, c.state.beginErr
	}
	return testTransaction{c.state}, nil
}
func (c testConnection) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.state.txOptions = opts
	return c.Begin()
}
func (c testConnection) Prepare(string) (driver.Stmt, error) {
	c.state.prepares++
	return nil, errors.New("unexpected statement preparation")
}
func (c testConnection) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.state.lastContext = ctx
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	values := make([]driver.Value, len(args))
	for i, arg := range args {
		values[i] = arg.Value
	}
	c.state.calls = append(c.state.calls, testCall{query, values})
	if c.state.queryErr != nil {
		return nil, c.state.queryErr
	}
	return &testRows{state: c.state}, nil
}
func (c testConnection) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.state.lastContext = ctx
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	values := make([]driver.Value, len(args))
	for i, arg := range args {
		values[i] = arg.Value
	}
	s := c.state
	s.calls = append(s.calls, testCall{query, values})
	s.execCount++
	if s.execErr != nil && (s.execFailAt == 0 || s.execCount == s.execFailAt) {
		return nil, s.execErr
	}
	affected := int64(strings.Count(query, "),(") + 1)
	if s.affectedRows != nil {
		affected = *s.affectedRows
	}
	return testResult{affected: affected, idErr: s.idErr, affectedErr: s.affectedErr}, nil
}

type testResult struct {
	affected           int64
	idErr, affectedErr error
}

func (r testResult) LastInsertId() (int64, error) { return 42, r.idErr }
func (r testResult) RowsAffected() (int64, error) { return r.affected, r.affectedErr }

type testRows struct {
	state *testDatabase
	index int
}

func (r *testRows) Columns() []string { return r.state.columns }
func (r *testRows) Close() error      { r.state.rowsClosed++; return r.state.closeErr }
func (r *testRows) Next(dest []driver.Value) error {
	if r.index < len(r.state.data) {
		copy(dest, r.state.data[r.index])
		r.index++
		return nil
	}
	if r.state.nextErr != nil {
		return r.state.nextErr
	}
	return io.EOF
}

type testTransaction struct{ state *testDatabase }

func (tx testTransaction) Commit() error {
	tx.state.commits++
	return tx.state.commitErr
}

func (tx testTransaction) Rollback() error {
	tx.state.rollbacks++
	return tx.state.rollbackErr
}

func openTestDB(t *testing.T, state *testDatabase) *sql.DB {
	t.Helper()
	db := sql.OpenDB(testConnector{state})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

// contextOnly deliberately exposes only the minimal database API.
type contextOnly struct{ IDBHandle }

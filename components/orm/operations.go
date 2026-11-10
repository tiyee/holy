package orm

import (
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

func appendWhere(conditions []string, where string) []string {
	if strings.TrimSpace(where) != "" {
		conditions = append(conditions, "where", where)
	}
	return conditions
}

func (c *ORM[T]) tableName() string {
	return quoteIdentifier(c.record.TableName())
}

func (c *ORM[T]) Row(where string, values []any) (T, error) {
	if err := c.validateModel(); err != nil {
		return c.record, err
	}
	meta := c.meta
	db, err := c.database(false)
	if err != nil {
		return c.record, err
	}
	conditions := []string{"select", meta.columnSQL, "from", c.tableName()}
	conditions = appendWhere(conditions, where)
	dest := make([]any, len(meta.fields))
	bindQueryInto(c.record, meta, dest)
	err = db.QueryRowContext(c.context(), strings.Join(conditions, " "), values...).Scan(dest...)
	return c.record, err
}
func (c *ORM[T]) Count(where string, values []any) (int64, error) {
	if err := c.validateModel(); err != nil {
		return 0, err
	}
	db, err := c.database(false)
	if err != nil {
		return 0, err
	}
	conditions := []string{"select count(*) as n  from", c.tableName()}
	conditions = appendWhere(conditions, where)
	var n int64
	if err := db.QueryRowContext(c.context(), strings.Join(conditions, " "), values...).Scan(&n); err == nil {
		return n, nil
	} else {
		return 0, err
	}
}
func (c *ORM[T]) Sum(col, where string, values []any) (int64, error) {
	if err := c.validateModel(); err != nil {
		return 0, err
	}
	db, err := c.database(false)
	if err != nil {
		return 0, err
	}
	conditions := []string{"select sum(" + quoteIdentifier(col) + ") as n from", c.tableName()}
	conditions = appendWhere(conditions, where)
	var n sql.NullInt64
	if err := db.QueryRowContext(c.context(), strings.Join(conditions, " "), values...).Scan(&n); err == nil {
		return n.Int64, nil
	} else {
		return 0, err
	}
}
func (c *ORM[T]) Rows(where string, values []any) ([]T, error) {
	if err := c.validateModel(); err != nil {
		return nil, err
	}
	meta := c.meta
	conditions := appendWhere([]string{"select", meta.columnSQL, "from", c.tableName()}, where)
	return c.queryRows(meta, strings.Join(conditions, " "), values)
}

func (c *ORM[T]) queryRows(meta *metadata, query string, values []any) ([]T, error) {
	db, err := c.database(false)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(c.context(), query, values...)
	if err != nil {
		return nil, err
	}
	clone := func() T {
		dst := reflect.New(meta.typ.Elem())
		dst.Elem().Set(reflect.ValueOf(c.record).Elem())
		// Only unmapped fields retain template values. Scanner implementations
		// may mutate maps or slices in place, so mapped fields must start fresh.
		for _, field := range meta.fields {
			dst.Elem().Field(field.index).SetZero()
		}
		return dst.Interface().(T)
	}
	return collectRows(rows, clone, meta)
}

func (c *ORM[T]) insertQuery(meta *metadata) (string, error) {
	if len(meta.fields) == 0 {
		return "", errors.New("empty fields")
	}
	return "insert into " + c.tableName() + " (" + meta.columnSQL + ") values " + meta.insertRow, nil
}

func (c *ORM[T]) Save() (int64, error) {
	if err := c.validateModel(); err != nil {
		return 0, err
	}
	meta := c.meta
	fields := meta.fields
	query, err := c.insertQuery(meta)
	if err != nil {
		return 0, err
	}
	db, err := c.database(true)
	if err != nil {
		return 0, err
	}
	result, err := db.ExecContext(c.context(), query, fieldValues(c.record, fields)...)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// Saves inserts all rows in one statement and returns RowsAffected.
// It reuses a caller's transaction without committing or rolling it back.
func (c *ORM[T]) Saves(rows []T) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	if err := c.validateModel(); err != nil {
		return 0, err
	}
	meta := c.meta
	fields := meta.fields
	if err := validateBatch(rows, meta); err != nil {
		return 0, err
	}
	query, err := c.insertQuery(meta)
	if err != nil {
		return 0, err
	}
	query += strings.Repeat(","+meta.insertRow, len(rows)-1)
	values := make([]any, len(rows)*len(fields))
	for i, row := range rows {
		value := reflect.ValueOf(row).Elem()
		for j, field := range fields {
			values[i*len(fields)+j] = value.Field(field.index).Interface()
		}
	}
	db, err := c.database(true)
	if err != nil {
		return 0, err
	}
	result, err := db.ExecContext(c.context(), query, values...)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return affected, nil
}

func (c *ORM[T]) Limit(where string, values []any, offset, size int64, order string) ([]T, error) {
	if err := c.validateModel(); err != nil {
		return nil, err
	}
	meta := c.meta
	conditions := []string{
		"select",
		meta.columnSQL,
		"from", c.tableName()}
	conditions = appendWhere(conditions, where)
	if strings.TrimSpace(order) != "" {
		conditions = append(conditions, "order by", order)
	}
	if size < 1 {
		size = 20
	}
	if offset > 0 {
		conditions = append(conditions, "limit", fmt.Sprintf(" %d , %d ", offset, size))
	} else {
		conditions = append(conditions, "limit", fmt.Sprintf("  %d ", size))
	}

	return c.queryRows(meta, strings.Join(conditions, " "), values)
}

func (c *ORM[T]) UpdateByPk(data map[string]any, pk int64) (int64, error) {
	if err := c.validateModel(); err != nil {
		return 0, err
	}
	if len(data) == 0 {
		return 0, errors.New("empty conditions")
	}
	condition := make([]string, 0, len(data))
	values := make([]any, 0, len(data)+1)
	keys := make([]string, 0, len(data))
	for key := range data {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		condition = append(condition, quoteIdentifier(key)+"=?")
		values = append(values, data[key])
	}
	values = append(values, pk)
	db, err := c.database(true)
	if err != nil {
		return 0, err
	}
	sqls := []string{"update", c.tableName(), "set", strings.Join(condition, ","), "where " + quoteIdentifier(c.record.Pk()) + "=?"}
	if ret, err := db.ExecContext(c.context(), strings.Join(sqls, " "), values...); err == nil {
		return ret.RowsAffected()
	} else {
		return 0, err
	}
}
func (c *ORM[T]) Update(pk int64) (int64, error) {
	if err := c.validateModel(); err != nil {
		return 0, err
	}
	meta := c.meta
	value := reflect.ValueOf(c.record).Elem()
	values := make([]any, 0)
	conditions := make([]string, 0)
	pkName := c.record.Pk()
	lowerPK := strings.ToLower(pkName)
	for _, field := range meta.fields {
		if field.lowerName == lowerPK {
			continue
		}
		conditions = append(conditions, field.quoted+"=?")
		values = append(values, value.Field(field.index).Interface())
	}
	if len(conditions) == 0 {
		return 0, errors.New("empty conditions")
	}
	values = append(values, pk)
	db, err := c.database(true)
	if err != nil {
		return 0, err
	}
	sqls := []string{"update", c.tableName(), "set", strings.Join(conditions, ","), "where " + quoteIdentifier(pkName) + "=?"}
	if ret, err := db.ExecContext(c.context(), strings.Join(sqls, " "), values...); err == nil {
		return ret.RowsAffected()
	} else {
		return 0, err
	}
}
func (c *ORM[T]) Delete(pk int64) (int64, error) {
	if err := c.validateModel(); err != nil {
		return 0, err
	}
	db, err := c.database(true)
	if err != nil {
		return 0, err
	}
	sqls := []string{"delete", "from", c.tableName(), "where", quoteIdentifier(c.record.Pk()), "=", "?"}
	if ret, err := db.ExecContext(c.context(), strings.Join(sqls, " "), pk); err == nil {
		return ret.RowsAffected()
	} else {
		return 0, err
	}
}

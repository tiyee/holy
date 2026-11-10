// Package orm 提供单表操作，并按模型类型和标签模式缓存字段映射。
package orm

import (
	"context"
	"database/sql"
)

// ITable 由模型实现，提供表名和主键列名。
type ITable interface {
	TableName() string
	Pk() string
}

// IDBHandle 是 *sql.DB 和 *sql.Tx 共有的 SQL 操作接口。
type IDBHandle interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// ORM 负责单表操作，借用调用方注入的连接或事务，不管理其生命周期。
type ORM[T ITable] struct {
	record   T
	wdb      IDBHandle
	rdb      IDBHandle
	meta     *metadata
	modelErr error
	ctx      context.Context
}

// New 使用注册的读写池创建实例，并按 record 的类型固定字段映射。
// 调用方可通过传入的指针读写模型字段。
func New[T ITable](record T) *ORM[T] {
	databases := registeredDatabases()
	meta, err := metadataForRecord(record, JSONTags)
	return &ORM[T]{
		record: record, rdb: databases.read, wdb: databases.write,
		meta: meta, modelErr: err,
	}
}

// SetDB 将当前实例的读写绑定到同一个数据库句柄。
func (c *ORM[T]) SetDB(db IDBHandle) *ORM[T] {
	c.rdb, c.wdb = db, db
	return c
}

// SetRDb 设置当前实例的读库句柄。
func (c *ORM[T]) SetRDb(rdb IDBHandle) *ORM[T] {
	c.rdb = rdb
	return c
}

// SetWDb 设置当前实例的写库句柄。
func (c *ORM[T]) SetWDb(wdb IDBHandle) *ORM[T] {
	c.wdb = wdb
	return c
}

func (c *ORM[T]) database(primary bool) (IDBHandle, error) {
	db := c.rdb
	if primary {
		db = c.wdb
	}
	if isNil(db) {
		return nil, ErrNoDatabase
	}
	return db, nil
}

// WithContext 返回使用 ctx 执行 SQL 的副本，副本共享模型和字段映射。
// 传入 nil 时使用 context.Background()。
func (c *ORM[T]) WithContext(ctx context.Context) *ORM[T] {
	clone := *c
	clone.ctx = ctx
	return &clone
}

func (c *ORM[T]) context() context.Context {
	if c.ctx != nil {
		return c.ctx
	}
	return context.Background()
}

func (c *ORM[T]) validateModel() error {
	if c.modelErr != nil {
		return c.modelErr
	}
	if c.meta == nil {
		return ErrInvalidModel
	}
	return nil
}

// WithMapping 返回绑定指定列映射的副本，缓存按类型和映射模式隔离。
// DatabaseTags 优先使用 db 标签，缺失时使用 JSON 标签。JSONTags 只使用 JSON 标签。
func (c *ORM[T]) WithMapping(mode MappingMode) *ORM[T] {
	clone := *c
	clone.meta, clone.modelErr = metadataForRecord(c.record, mode)
	return &clone
}

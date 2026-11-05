package orm

import (
	"context"
	"reflect"
)

// OrmV2 uses the same API and metadata cache as ORM.
type OrmV2[T ITable] = ORM[T]

// ORM 负责单表操作，借用调用方注入的连接或事务，不管理其生命周期。
type ORM[T ITable] struct {
	Record  T
	wdb     IDBHandle
	rdb     IDBHandle
	meta    *metadata
	mapping MappingMode
	ctx     context.Context
}

// WithContext returns a copy that uses ctx for all database operations.
// A nil Context restores the default context.Background(). Record is shared.
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

// findMeta reuses the instance mapping, then the shared type cache. Record is
// replaceable, so an interface-typed ORM must also check its current model type.
// TableName/Pk and record values remain instance data and are never cached here.
func (c *ORM[T]) findMeta() (*metadata, error) {
	if isNil(c.Record) {
		return nil, ErrInvalidModel
	}
	typ := reflect.TypeOf(c.Record)
	if c.meta != nil && c.meta.typ == typ && c.meta.mode == c.mapping {
		return c.meta, nil
	}
	meta, err := loadMetadataFor(typ, c.mapping)
	if err != nil {
		return nil, err
	}
	c.meta = meta
	return meta, nil
}

// WithMapping 返回使用指定列映射规则的副本，缓存按类型和映射模式隔离。
// DatabaseTags 优先 db 标签，缺少时回退 JSON；JSONTags 保留旧行为。
func (c *ORM[T]) WithMapping(mode MappingMode) *ORM[T] {
	clone := *c
	clone.mapping = mode
	clone.meta = nil
	return &clone
}

package orm

import (
	"database/sql"
	"sync/atomic"
)

type databasePair struct {
	read, write *sql.DB
}

const (
	initUnstarted uint32 = iota
	initStarting
	initStarted
	initFailed
)

var defaultState atomic.Uint32

// defaultDatabases 在发布 initStarted 前赋值，之后只读。
var defaultDatabases databasePair

// RegisterDB 在应用启动时一次性注册 ORM 的全局读写连接池。
// 至少一个池必须非 nil，重复注册返回 ErrDatabaseAlreadyRegistered。
// New 使用注册的连接作为实例初始值，BeginTx 和 AutoTransaction 使用注册的写池。
// 连接池的生命周期由调用方管理。
func RegisterDB(read, write *sql.DB) error {
	if read == nil && write == nil {
		defaultState.CompareAndSwap(initUnstarted, initFailed)
		return ErrNoDatabase
	}
	if !defaultState.CompareAndSwap(initUnstarted, initStarting) &&
		!defaultState.CompareAndSwap(initFailed, initStarting) {
		return ErrDatabaseAlreadyRegistered
	}
	defaultDatabases = databasePair{read: read, write: write}
	defaultState.Store(initStarted)
	return nil
}

// registeredDatabases 返回借用的读写连接池快照。
func registeredDatabases() databasePair {
	if defaultState.Load() == initStarted {
		return defaultDatabases
	}
	return databasePair{}
}

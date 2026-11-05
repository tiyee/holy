package orm

import "sync/atomic"

type databasePair struct {
	read, write IDBHandle
}

var defaultDatabases atomic.Pointer[databasePair]

// RegisterDB 注册 ORM 的全局读写连接，通常在应用启动时调用。
// 读写连接作为同一快照发布；New 和包级事务使用注册的连接。
// 传入 nil 清除对应连接，不自动回退到另一连接；已创建的 ORM 实例不受影响。
// ORM 仅借用连接，替换或清除注册都不会关闭连接池，生命周期由调用方管理。
func RegisterDB(read, write IDBHandle) {
	defaultDatabases.Store(&databasePair{read: read, write: write})
}

func registeredDatabases() (read, write IDBHandle) {
	if pair := defaultDatabases.Load(); pair != nil {
		return pair.read, pair.write
	}
	return nil, nil
}

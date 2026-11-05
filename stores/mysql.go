package stores

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// RDb/WDb 为启动期兼容入口；新代码应注入 OpenMySQL 返回的 Store。
// 不应在请求处理期间直接修改这些变量。
var RDb *sql.DB
var WDb *sql.DB

const MaxIdle = 20
const MaxOpen = 20

// ErrInvalidMySQLPoolConfig 表示连接池配置包含负数。
var ErrInvalidMySQLPoolConfig = errors.New("mysql: pool settings must not be negative")

// MySQLPoolConfig 在建池时同时应用于读写连接；相同 DSN 仍复用单池。
// 零值不填充默认值：MaxOpenConns 为 0 表示不限，MaxIdleConns 为 0 表示不保留空闲连接，
// 两个时间字段为 0 表示不按该时间淘汰连接。所有字段必须非负。
// MaxOpenConns 大于 0 时，实际空闲连接上限不会超过它。
type MySQLPoolConfig struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

// DefaultMySQLPoolConfig 返回独立的兼容配置，调用方可修改副本后传入配置入口。
func DefaultMySQLPoolConfig() MySQLPoolConfig {
	return MySQLPoolConfig{
		MaxOpenConns:    MaxOpen,
		MaxIdleConns:    MaxIdle,
		ConnMaxLifetime: 20 * time.Second,
	}
}

func (cfg MySQLPoolConfig) validate() error {
	if cfg.MaxOpenConns < 0 || cfg.MaxIdleConns < 0 || cfg.ConnMaxLifetime < 0 || cfg.ConnMaxIdleTime < 0 {
		return ErrInvalidMySQLPoolConfig
	}
	return nil
}

// Store 统一拥有读写连接池，Close 幂等，共享的读写池只关闭一次。
// Store 不可按值复制。ORM 仅借用连接，不负责关闭 Store。
type Store struct {
	Read, Write *sql.DB
	once        sync.Once
	closeErr    error
}

func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.once.Do(func() {
		if s.Write != nil {
			s.closeErr = errors.Join(s.closeErr, s.Write.Close())
		}
		if s.Read != nil && s.Read != s.Write {
			s.closeErr = errors.Join(s.closeErr, s.Read.Close())
		}
	})
	return s.closeErr
}

// OpenMySQL 全部验证成功后返回资源；失败时不发布任何连接并清理所有已开池。
// 相同 DSN 复用一个池；读 DSN 为空时使用写库。
func OpenMySQL(ctx context.Context, readDSN, writeDSN string) (*Store, error) {
	return OpenMySQLWithConfig(ctx, readDSN, writeDSN, DefaultMySQLPoolConfig())
}

// OpenMySQLWithConfig 使用显式连接池配置，在 Ping 前应用全部参数。
// 配置无效时不创建连接；其余资源所有权及失败清理行为与 OpenMySQL 相同。
func OpenMySQLWithConfig(ctx context.Context, readDSN, writeDSN string, cfg MySQLPoolConfig) (*Store, error) {
	return openMySQL(ctx, readDSN, writeDSN, cfg, sql.Open)
}

func openMySQL(ctx context.Context, readDSN, writeDSN string, cfg MySQLPoolConfig, open func(string, string) (*sql.DB, error)) (*Store, error) {
	if ctx == nil {
		return nil, errors.New("mysql: nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if writeDSN == "" {
		return nil, errors.New("mysql: write DSN is required")
	}
	if readDSN == "" {
		readDSN = writeDSN
	}
	openPool := func(dsn string) (*sql.DB, error) {
		db, err := open("mysql", dsn)
		if err != nil {
			return nil, err
		}
		db.SetMaxOpenConns(cfg.MaxOpenConns)
		db.SetMaxIdleConns(cfg.MaxIdleConns)
		db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
		db.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)
		if err = db.PingContext(ctx); err != nil {
			return nil, errors.Join(err, db.Close())
		}
		return db, nil
	}
	read, err := openPool(readDSN)
	if err != nil {
		return nil, err
	}
	store := &Store{Read: read, Write: read}
	if writeDSN != readDSN {
		store.Write, err = openPool(writeDSN)
		if err != nil {
			return nil, errors.Join(err, store.Close())
		}
	}
	return store, nil
}

var mysqlMu sync.Mutex
var defaultStore *Store

// InitMysql 为兼容入口设置有限初始化超时。
func InitMysql(readDSN, writeDSN string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return InitMysqlContext(ctx, readDSN, writeDSN)
}

// InitMysqlContext 仅用于启动配置；新实例完全可用后才替换并关闭旧实例。
func InitMysqlContext(ctx context.Context, readDSN, writeDSN string) error {
	return InitMysqlContextWithConfig(ctx, readDSN, writeDSN, DefaultMySQLPoolConfig())
}

// InitMysqlContextWithConfig 使用显式连接池配置初始化兼容默认连接。
// 配置或连接验证失败时保留旧池；成功替换后关闭旧池。
func InitMysqlContextWithConfig(ctx context.Context, readDSN, writeDSN string, cfg MySQLPoolConfig) error {
	store, err := OpenMySQLWithConfig(ctx, readDSN, writeDSN, cfg)
	if err != nil {
		return err
	}
	mysqlMu.Lock()
	old := defaultStore
	defaultStore, RDb, WDb = store, store.Read, store.Write
	mysqlMu.Unlock()
	return old.Close()
}

// MySQLDatabases 返回兼容默认连接的同一快照。
func MySQLDatabases() (read, write *sql.DB) {
	mysqlMu.Lock()
	defer mysqlMu.Unlock()
	return RDb, WDb
}

func CloseMysql() error {
	mysqlMu.Lock()
	old := defaultStore
	defaultStore, RDb, WDb = nil, nil, nil
	mysqlMu.Unlock()
	return old.Close()
}

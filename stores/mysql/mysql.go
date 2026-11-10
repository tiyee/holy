package mysql

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

const MaxIdle = 20
const MaxOpen = 20

// ErrInvalidPoolConfig 表示连接池配置包含负数。
var ErrInvalidPoolConfig = errors.New("mysql: pool settings must not be negative")

// ErrInitializing 表示默认连接池正在初始化。
var ErrInitializing = errors.New("mysql: default pools are initializing")

// ErrAlreadyInitialized 表示默认连接池已成功初始化过。
var ErrAlreadyInitialized = errors.New("mysql: default pools already initialized")

// PoolConfig 在建池时同时应用于读写连接；相同 DSN 仍复用单池。
// 零值不填充默认值：MaxOpenConns 为 0 表示不限，MaxIdleConns 为 0 表示不保留空闲连接，
// 两个时间字段为 0 表示不按该时间淘汰连接。所有字段必须非负。
// MaxOpenConns 大于 0 时，实际空闲连接上限不会超过它。
type PoolConfig struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

// DefaultPoolConfig 返回默认配置，调用方可按需修改。
func DefaultPoolConfig() PoolConfig {
	return PoolConfig{
		MaxOpenConns:    MaxOpen,
		MaxIdleConns:    MaxIdle,
		ConnMaxLifetime: 20 * time.Second,
	}
}

func (cfg PoolConfig) validate() error {
	if cfg.MaxOpenConns < 0 || cfg.MaxIdleConns < 0 || cfg.ConnMaxLifetime < 0 || cfg.ConnMaxIdleTime < 0 {
		return ErrInvalidPoolConfig
	}
	return nil
}

// Open 验证后返回读写连接池；失败时返回两个 nil 并清理已打开的池。
// 相同 DSN 复用一个池；读 DSN 为空时使用写库。
// 调用方负责关闭每个不同的连接池。
func Open(ctx context.Context, readDSN, writeDSN string) (read, write *sql.DB, err error) {
	return OpenWithConfig(ctx, readDSN, writeDSN, DefaultPoolConfig())
}

// OpenWithConfig 使用显式连接池配置，在 Ping 前应用全部参数。
// 配置无效时不创建连接；其余资源所有权及失败清理行为与 Open 相同。
func OpenWithConfig(ctx context.Context, readDSN, writeDSN string, cfg PoolConfig) (read, write *sql.DB, err error) {
	return open(ctx, readDSN, writeDSN, cfg, sql.Open)
}

func open(ctx context.Context, readDSN, writeDSN string, cfg PoolConfig, openDB func(string, string) (*sql.DB, error)) (*sql.DB, *sql.DB, error) {
	if ctx == nil {
		return nil, nil, errors.New("mysql: nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if err := cfg.validate(); err != nil {
		return nil, nil, err
	}
	if writeDSN == "" {
		return nil, nil, errors.New("mysql: write DSN is required")
	}
	if readDSN == "" {
		readDSN = writeDSN
	}
	openPool := func(dsn string) (*sql.DB, error) {
		db, err := openDB("mysql", dsn)
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
		return nil, nil, err
	}
	if readDSN == writeDSN {
		return read, read, nil
	}
	write, err := openPool(writeDSN)
	if err != nil {
		return nil, nil, errors.Join(err, read.Close())
	}
	return read, write, nil
}

const (
	initUnstarted uint32 = iota
	initStarting
	initStarted
	initFailed
)

var defaultState atomic.Uint32
var defaultClosed atomic.Bool

// 默认读写池在发布 initStarted 前赋值，之后只读。
var defaultRead, defaultWrite *sql.DB

// Init 使用 5 秒超时初始化默认连接池。
func Init(readDSN, writeDSN string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return InitContext(ctx, readDSN, writeDSN)
}

// InitContext 使用默认配置初始化默认连接池。
func InitContext(ctx context.Context, readDSN, writeDSN string) error {
	return InitContextWithConfig(ctx, readDSN, writeDSN, DefaultPoolConfig())
}

// InitContextWithConfig 在应用启动时初始化默认连接池。
// 初始化期间再次调用返回 ErrInitializing。
// 验证失败可重试，首次成功后的调用返回 ErrAlreadyInitialized。
func InitContextWithConfig(ctx context.Context, readDSN, writeDSN string, cfg PoolConfig) error {
	return initDefault(ctx, readDSN, writeDSN, cfg, sql.Open)
}

func initDefault(ctx context.Context, readDSN, writeDSN string, cfg PoolConfig, openDB func(string, string) (*sql.DB, error)) error {
	for {
		state := defaultState.Load()
		switch state {
		case initStarting:
			return ErrInitializing
		case initStarted:
			return ErrAlreadyInitialized
		}
		if defaultState.CompareAndSwap(state, initStarting) {
			break
		}
	}
	// 打开连接池的过程提前退出时，将启动中标记为失败。
	defer defaultState.CompareAndSwap(initStarting, initFailed)

	read, write, err := open(ctx, readDSN, writeDSN, cfg, openDB)
	if err != nil {
		return err
	}
	defaultRead, defaultWrite = read, write
	defaultState.Store(initStarted)
	return nil
}

// DefaultDatabases 返回借用的默认读写连接池。初始化成功前或关闭后返回两个 nil。
func DefaultDatabases() (read, write *sql.DB) {
	if defaultState.Load() == initStarted && !defaultClosed.Load() {
		return defaultRead, defaultWrite
	}
	return nil, nil
}

// RDB 返回借用的默认读连接池。初始化成功前或关闭后返回 nil。
func RDB() *sql.DB {
	read, _ := DefaultDatabases()
	return read
}

// WDB 返回借用的默认写连接池。初始化成功前或关闭后返回 nil。
func WDB() *sql.DB {
	_, write := DefaultDatabases()
	return write
}

// Close 幂等地关闭默认连接池，关闭后仍保留启动完成状态。
func Close() error {
	if defaultState.Load() != initStarted || !defaultClosed.CompareAndSwap(false, true) {
		return nil
	}
	if defaultRead == defaultWrite {
		return defaultWrite.Close()
	}
	return errors.Join(defaultWrite.Close(), defaultRead.Close())
}

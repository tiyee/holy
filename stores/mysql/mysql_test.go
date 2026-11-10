package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

type poolState struct {
	pingErr  error
	closeErr error
	closed   atomic.Int64
}
type poolConnector struct{ state *poolState }

func (c poolConnector) Connect(context.Context) (driver.Conn, error) { return poolConn{c.state}, nil }
func (c poolConnector) Driver() driver.Driver                        { return poolDriver{} }

type poolDriver struct{}

func (poolDriver) Open(string) (driver.Conn, error) { return nil, errors.New("unused") }

type poolConn struct{ state *poolState }

func (c poolConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unused") }
func (c poolConn) Begin() (driver.Tx, error)           { return nil, errors.New("unused") }
func (c poolConn) Close() error {
	c.state.closed.Add(1)
	return c.state.closeErr
}
func (c poolConn) Ping(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.state.pingErr
}

func TestOpenFailureClosesAllPools(t *testing.T) {
	failure := errors.New("database unavailable")
	for _, scenario := range []string{"read open", "read ping", "write open", "write ping"} {
		t.Run(scenario, func(t *testing.T) {
			pools := map[string]*sql.DB{}
			states := map[string]*poolState{}
			openDB := func(_ string, dsn string) (*sql.DB, error) {
				if scenario == dsn+" open" {
					return nil, failure
				}
				state := &poolState{}
				if scenario == dsn+" ping" {
					state.pingErr = failure
				}
				db := sql.OpenDB(poolConnector{state})
				pools[dsn] = db
				states[dsn] = state
				return db, nil
			}
			if read, write, err := open(context.Background(), "read", "write", DefaultPoolConfig(), openDB); read != nil || write != nil || !errors.Is(err, failure) {
				t.Fatalf("Open: read=%v write=%v err=%v", read, write, err)
			}
			for name, db := range pools {
				if err := db.Ping(); err == nil {
					t.Errorf("%s pool left open", name)
				}
				if states[name].closed.Load() != 1 {
					t.Errorf("%s connection not closed", name)
				}
			}
		})
	}
}

func TestCloseDistinctDefaultPools(t *testing.T) {
	resetDefaultMySQL(t)
	readErr, writeErr := errors.New("close read"), errors.New("close write")
	states := map[string]*poolState{"read": {closeErr: readErr}, "write": {closeErr: writeErr}}
	pools := make(map[string]*sql.DB)
	err := initDefault(context.Background(), "read", "write", DefaultPoolConfig(), func(_, dsn string) (*sql.DB, error) {
		db := sql.OpenDB(poolConnector{states[dsn]})
		pools[dsn] = db
		return db, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if RDB() != pools["read"] || WDB() != pools["write"] {
		t.Error("RDB() 和 WDB() 返回的池与各自的 DSN 不匹配")
	}
	if read, write := DefaultDatabases(); read != pools["read"] || write != pools["write"] {
		t.Error("默认读写池与各自的 DSN 不匹配")
	}
	if err := Close(); !errors.Is(err, readErr) || !errors.Is(err, writeErr) {
		t.Fatalf("关闭默认池应保留读写池的错误，得到 %v", err)
	}
	if err := Close(); err != nil {
		t.Fatal(err)
	}
	for dsn, state := range states {
		if got := state.closed.Load(); got != 1 {
			t.Errorf("关闭 %s 池 %d 次，期望 1 次", dsn, got)
		}
	}
}

func resetDefaultMySQL(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		if err := Close(); err != nil {
			t.Error(err)
		}
		defaultRead, defaultWrite = nil, nil
		defaultClosed.Store(false)
		defaultState.Store(initUnstarted)
	})
}

func TestDefaultMySQLLifecycle(t *testing.T) {
	resetDefaultMySQL(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := InitContext(ctx, "read", "write"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if state := defaultState.Load(); state != initFailed {
		t.Fatalf("取消后状态 = %d，预期失败", state)
	}
	if RDB() != nil || WDB() != nil {
		t.Fatal("初始化失败后仍可单独获取读池或写池")
	}
	if read, write := DefaultDatabases(); read != nil || write != nil {
		t.Fatal("初始化失败后发布了连接池")
	}

	pool := &poolState{}
	starting := make(chan struct{})
	resume := make(chan struct{})
	results := make(chan error, 2)
	go func() {
		results <- initDefault(context.Background(), "", "write", DefaultPoolConfig(), func(_, _ string) (*sql.DB, error) {
			close(starting)
			<-resume
			return sql.OpenDB(poolConnector{pool}), nil
		})
	}()
	select {
	case <-starting:
		if state := defaultState.Load(); state != initStarting {
			t.Errorf("建池期间状态 = %d，预期启动中", state)
		}
		if read, write := DefaultDatabases(); read != nil || write != nil {
			t.Error("初始化完成前发布了连接池")
		}
		if err := Init("", ""); !errors.Is(err, ErrInitializing) {
			t.Errorf("启动中再次初始化返回 %v，预期 ErrInitializing", err)
		}
	case err := <-results:
		t.Fatalf("初始化在建池前返回 %v", err)
	}
	close(resume)
	if err := <-results; err != nil {
		t.Fatal(err)
	}
	read, write := DefaultDatabases()
	if state := defaultState.Load(); state != initStarted || read == nil || read != write {
		t.Fatalf("初始化后状态 = %d，预期启动完成并发布共享连接池", state)
	}
	if RDB() != read || WDB() != write {
		t.Fatal("RDB() 和 WDB() 未返回共享池")
	}
	if err := Init("", ""); !errors.Is(err, ErrAlreadyInitialized) {
		t.Fatalf("启动后再次初始化返回 %v，预期 ErrAlreadyInitialized", err)
	}
	if r, w := DefaultDatabases(); r != read || w != write {
		t.Fatal("重复初始化更换了连接池")
	}
	if err := read.Ping(); err != nil {
		t.Fatalf("重复初始化后原连接池不可用：%v", err)
	}
	snapshotChecked := make(chan struct{})
	go func() {
		defer close(snapshotChecked)
		r, w := DefaultDatabases()
		if (r != read || w != write) && (r != nil || w != nil) {
			t.Error("并发关闭时获得了不完整的连接池快照")
		}
	}()
	for range 2 {
		go func() { results <- Close() }()
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}
	<-snapshotChecked
	if got := pool.closed.Load(); got != 1 {
		t.Fatalf("并发关闭释放连接 %d 次，预期一次", got)
	}
	if read, write := DefaultDatabases(); read != nil || write != nil {
		t.Fatal("关闭后仍可获取默认连接池")
	}
	if RDB() != nil || WDB() != nil {
		t.Fatal("关闭后仍可单独获取读池或写池")
	}
	if err := Init("", ""); !errors.Is(err, ErrAlreadyInitialized) {
		t.Fatalf("关闭后再次初始化返回 %v，预期 ErrAlreadyInitialized", err)
	}
}

func TestMySQLPoolConnectionLimits(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  PoolConfig
	}{
		{"default", DefaultPoolConfig()},
		{"custom", PoolConfig{MaxOpenConns: 4, MaxIdleConns: 2}},
		{"idle capped by open", PoolConfig{MaxOpenConns: 2, MaxIdleConns: 4}},
		{"explicit zero", PoolConfig{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, readDSN := range []string{"", "write", "read"} {
				calls := 0
				read, write, err := open(context.Background(), readDSN, "write", tc.cfg, func(_, _ string) (*sql.DB, error) {
					calls++
					return sql.OpenDB(poolConnector{&poolState{}}), nil
				})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { write.Close() })
				wantCalls := 1
				pools := []*sql.DB{write}
				if read != write {
					t.Cleanup(func() { read.Close() })
				}
				if readDSN == "read" {
					wantCalls = 2
					if read == write {
						t.Fatal("不同 DSN 复用了连接池")
					}
					pools = append(pools, read)
				} else if read != write {
					t.Fatal("相同或空读 DSN 未复用连接池")
				}
				if calls != wantCalls {
					t.Fatalf("建池次数 = %d，期望 %d", calls, wantCalls)
				}
				for _, db := range pools {
					if got := db.Stats().MaxOpenConnections; got != tc.cfg.MaxOpenConns {
						t.Fatalf("最大连接数 = %d，期望 %d", got, tc.cfg.MaxOpenConns)
					}
					// 同时借出多条连接，再归还，验证实际保留的空闲数量。
					n := tc.cfg.MaxOpenConns
					if n == 0 {
						n = 3
					}
					var conns []*sql.Conn
					for range n {
						conn, err := db.Conn(context.Background())
						if err != nil {
							t.Fatal(err)
						}
						conns = append(conns, conn)
					}
					for _, conn := range conns {
						if err := conn.Close(); err != nil {
							t.Fatal(err)
						}
					}
					if got, want := db.Stats().Idle, min(n, tc.cfg.MaxIdleConns); got != want {
						t.Fatalf("空闲连接数 = %d，期望 %d", got, want)
					}
				}
			}
		})
	}
}

func TestMySQLPoolConnectionExpiry(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		cfg                    PoolConfig
		wait                   time.Duration
		wantLifetime, wantIdle int64
	}{
		{"default lifetime", DefaultPoolConfig(), 21 * time.Second, 1, 0},
		{"longer lifetime", PoolConfig{MaxIdleConns: 1, ConnMaxLifetime: time.Minute}, 21 * time.Second, 0, 0},
		{"custom lifetime expires", PoolConfig{MaxIdleConns: 1, ConnMaxLifetime: time.Minute}, 61 * time.Second, 1, 0},
		{"idle expires", PoolConfig{MaxIdleConns: 1, ConnMaxIdleTime: 5 * time.Second}, 6 * time.Second, 0, 1},
		{"zero disables expiry", PoolConfig{MaxIdleConns: 1}, time.Hour, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// 使用虚拟时钟验证连接淘汰，不依赖真实服务或墙上时钟等待。
			synctest.Test(t, func(t *testing.T) {
				read, write, err := open(context.Background(), "read", "write", tc.cfg, func(_, _ string) (*sql.DB, error) {
					return sql.OpenDB(poolConnector{&poolState{}}), nil
				})
				if err != nil {
					t.Fatal(err)
				}
				defer read.Close()
				defer write.Close()
				synctest.Wait()
				time.Sleep(tc.wait)
				synctest.Wait()
				for _, db := range []*sql.DB{read, write} {
					if err := db.Ping(); err != nil {
						t.Fatal(err)
					}
					stats := db.Stats()
					if stats.MaxLifetimeClosed != tc.wantLifetime || stats.MaxIdleTimeClosed != tc.wantIdle {
						t.Fatalf("淘汰连接数：存活时间=%d，空闲时间=%d；期望 %d/%d", stats.MaxLifetimeClosed, stats.MaxIdleTimeClosed, tc.wantLifetime, tc.wantIdle)
					}
				}
			})
		})
	}
}

func TestInvalidMySQLPoolConfig(t *testing.T) {
	resetDefaultMySQL(t)
	for _, cfg := range []PoolConfig{
		{MaxOpenConns: -1}, {MaxIdleConns: -1}, {ConnMaxLifetime: -1}, {ConnMaxIdleTime: -1},
	} {
		err := initDefault(context.Background(), "read", "write", cfg, func(_, _ string) (*sql.DB, error) {
			t.Fatal("非法配置不应创建连接池")
			return nil, nil
		})
		if !errors.Is(err, ErrInvalidPoolConfig) {
			t.Fatalf("非法配置返回 %v", err)
		}
	}
	if state := defaultState.Load(); state != initFailed {
		t.Fatalf("非法配置后的状态 = %d，预期失败", state)
	}
}

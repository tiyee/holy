package stores

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
	pingErr error
	closed  atomic.Int64
}
type poolConnector struct{ state *poolState }

func (c poolConnector) Connect(context.Context) (driver.Conn, error) { return poolConn{c.state}, nil }
func (c poolConnector) Driver() driver.Driver                        { return poolDriver{} }

type poolDriver struct{}

func (poolDriver) Open(string) (driver.Conn, error) { return nil, errors.New("unused") }

type poolConn struct{ state *poolState }

func (c poolConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unused") }
func (c poolConn) Begin() (driver.Tx, error)           { return nil, errors.New("unused") }
func (c poolConn) Close() error                        { c.state.closed.Add(1); return nil }
func (c poolConn) Ping(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.state.pingErr
}

func TestOpenMySQLFailureClosesAllPools(t *testing.T) {
	failure := errors.New("database unavailable")
	for _, scenario := range []string{"read open", "read ping", "write open", "write ping"} {
		t.Run(scenario, func(t *testing.T) {
			pools := map[string]*sql.DB{}
			states := map[string]*poolState{}
			open := func(_ string, dsn string) (*sql.DB, error) {
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
			if s, err := openMySQL(context.Background(), "read", "write", DefaultMySQLPoolConfig(), open); s != nil || !errors.Is(err, failure) {
				t.Fatalf("OpenMySQL: %v %v", s, err)
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
func TestStoreSharedPoolAndClose(t *testing.T) {
	for _, read := range []string{"", "same", "read"} {
		calls := 0
		states := []*poolState{}
		open := func(_ string, _ string) (*sql.DB, error) {
			calls++
			state := &poolState{}
			states = append(states, state)
			return sql.OpenDB(poolConnector{state}), nil
		}
		s, err := openMySQL(context.Background(), read, "same", DefaultMySQLPoolConfig(), open)
		if err != nil {
			t.Fatal(err)
		}
		want := 1
		if read == "read" {
			want = 2
		}
		if calls != want {
			t.Fatalf("pool count: %d want %d", calls, want)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		for _, state := range states {
			if state.closed.Load() != 1 {
				t.Fatal("pool closed wrong number of times")
			}
		}
	}
}
func TestFailedDefaultInitPreservesConfiguration(t *testing.T) {
	previousRead, previousWrite := RDb, WDb
	state := &poolState{}
	db := sql.OpenDB(poolConnector{state})
	defer db.Close()
	RDb, WDb = db, db
	t.Cleanup(func() { RDb, WDb = previousRead, previousWrite })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := InitMysqlContext(ctx, "read", "write"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if RDb != db || WDb != db {
		t.Fatal("failed initialization published partial state")
	}
	if err := db.Ping(); err != nil {
		t.Fatal("failed initialization closed caller-owned pool")
	}
}

func TestMySQLPoolConnectionLimits(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  MySQLPoolConfig
	}{
		{"default", DefaultMySQLPoolConfig()},
		{"custom", MySQLPoolConfig{MaxOpenConns: 4, MaxIdleConns: 2}},
		{"idle capped by open", MySQLPoolConfig{MaxOpenConns: 2, MaxIdleConns: 4}},
		{"explicit zero", MySQLPoolConfig{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, read := range []string{"", "write", "read"} {
				calls := 0
				store, err := openMySQL(context.Background(), read, "write", tc.cfg, func(_, _ string) (*sql.DB, error) {
					calls++
					return sql.OpenDB(poolConnector{&poolState{}}), nil
				})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { store.Close() })
				wantCalls := 1
				pools := []*sql.DB{store.Write}
				if read == "read" {
					wantCalls = 2
					pools = append(pools, store.Read)
				} else if store.Read != store.Write {
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
		cfg                    MySQLPoolConfig
		wait                   time.Duration
		wantLifetime, wantIdle int64
	}{
		{"default lifetime", DefaultMySQLPoolConfig(), 21 * time.Second, 1, 0},
		{"longer lifetime", MySQLPoolConfig{MaxIdleConns: 1, ConnMaxLifetime: time.Minute}, 21 * time.Second, 0, 0},
		{"custom lifetime expires", MySQLPoolConfig{MaxIdleConns: 1, ConnMaxLifetime: time.Minute}, 61 * time.Second, 1, 0},
		{"idle expires", MySQLPoolConfig{MaxIdleConns: 1, ConnMaxIdleTime: 5 * time.Second}, 6 * time.Second, 0, 1},
		{"zero disables expiry", MySQLPoolConfig{MaxIdleConns: 1}, time.Hour, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// 使用虚拟时钟验证连接淘汰，不依赖真实服务或墙上时钟等待。
			synctest.Test(t, func(t *testing.T) {
				store, err := openMySQL(context.Background(), "read", "write", tc.cfg, func(_, _ string) (*sql.DB, error) {
					return sql.OpenDB(poolConnector{&poolState{}}), nil
				})
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				synctest.Wait()
				time.Sleep(tc.wait)
				synctest.Wait()
				for _, db := range []*sql.DB{store.Read, store.Write} {
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

func TestInvalidMySQLPoolConfigPreservesDefault(t *testing.T) {
	previousStore, previousRead, previousWrite := defaultStore, RDb, WDb
	db := sql.OpenDB(poolConnector{&poolState{}})
	defer db.Close()
	old := &Store{Read: db, Write: db}
	defaultStore, RDb, WDb = old, db, db
	t.Cleanup(func() { defaultStore, RDb, WDb = previousStore, previousRead, previousWrite })
	for _, cfg := range []MySQLPoolConfig{
		{MaxOpenConns: -1}, {MaxIdleConns: -1}, {ConnMaxLifetime: -1}, {ConnMaxIdleTime: -1},
	} {
		store, err := openMySQL(context.Background(), "read", "write", cfg, func(_, _ string) (*sql.DB, error) {
			t.Fatal("非法配置不应创建连接池")
			return nil, nil
		})
		if store != nil || !errors.Is(err, ErrInvalidMySQLPoolConfig) {
			t.Fatalf("非法配置返回 %v / %v", store, err)
		}
		if err := InitMysqlContextWithConfig(context.Background(), "read", "write", cfg); !errors.Is(err, ErrInvalidMySQLPoolConfig) {
			t.Fatal(err)
		}
		if defaultStore != old || RDb != db || WDb != db {
			t.Fatal("非法配置替换了原默认连接池")
		}
		if err := db.Ping(); err != nil {
			t.Fatalf("非法配置关闭了旧池：%v", err)
		}
	}
}

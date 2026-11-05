package stores

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestMySQLIntegrationLifecycle(t *testing.T) {
	dsn := os.Getenv("HOLY_MYSQL_DSN")
	if dsn == "" {
		if os.Getenv("HOLY_REQUIRE_MYSQL") == "1" {
			t.Fatal("HOLY_MYSQL_DSN is required")
		}
		t.Skip("HOLY_MYSQL_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store, err := OpenMySQL(ctx, dsn, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if store.Read != store.Write {
		t.Fatal("same DSN did not reuse pool")
	}
	if err := store.Read.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	if got := store.Read.Stats().MaxOpenConnections; got != MaxOpen {
		t.Fatalf("默认最大连接数 = %d，期望 %d", got, MaxOpen)
	}
	pool := DefaultMySQLPoolConfig()
	pool.MaxOpenConns = 5
	pool.MaxIdleConns = 2
	pool.ConnMaxLifetime = 5 * time.Minute
	pool.ConnMaxIdleTime = time.Minute
	configured, err := OpenMySQLWithConfig(ctx, dsn, dsn, pool)
	if err != nil {
		t.Fatal(err)
	}
	defer configured.Close()
	if configured.Read != configured.Write || configured.Read.Stats().MaxOpenConnections != pool.MaxOpenConns {
		t.Fatal("显式配置未生效或相同 DSN 未复用连接池")
	}
	if err := InitMysqlContext(ctx, dsn, dsn); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { CloseMysql() })
	beforeRead, beforeWrite := MySQLDatabases()
	// 写 DSN 非法时，已经成功建立的读连接必须被清理且不能发布。
	if err := InitMysqlContext(ctx, dsn, ":invalid"); err == nil {
		t.Fatal("invalid write DSN accepted")
	}
	read, write := MySQLDatabases()
	if read != beforeRead || write != beforeWrite {
		t.Fatal("partial configuration published")
	}
	canceled, stop := context.WithCancel(ctx)
	stop()
	if err := InitMysqlContext(canceled, dsn, dsn); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := InitMysqlContextWithConfig(ctx, dsn, dsn, pool); err != nil {
		t.Fatal(err)
	}
	if err := beforeRead.PingContext(ctx); err == nil {
		t.Fatal("old pool remains open")
	}
	read, write = MySQLDatabases()
	if read != write || write.Stats().MaxOpenConnections != pool.MaxOpenConns {
		t.Fatal("默认连接替换后未应用新配置")
	}
	if err := CloseMysql(); err != nil {
		t.Fatal(err)
	}
	if err := CloseMysql(); err != nil {
		t.Fatal(err)
	}
}

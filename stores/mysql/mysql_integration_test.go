package mysql

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
	read, write, err := Open(ctx, dsn, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer write.Close()
	if read != write {
		read.Close()
		t.Fatal("same DSN did not reuse pool")
	}
	if err := read.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	if got := read.Stats().MaxOpenConnections; got != MaxOpen {
		t.Fatalf("默认最大连接数 = %d，期望 %d", got, MaxOpen)
	}
	pool := DefaultPoolConfig()
	pool.MaxOpenConns = 5
	pool.MaxIdleConns = 2
	pool.ConnMaxLifetime = 5 * time.Minute
	pool.ConnMaxIdleTime = time.Minute
	configuredRead, configuredWrite, err := OpenWithConfig(ctx, dsn, dsn, pool)
	if err != nil {
		t.Fatal(err)
	}
	defer configuredWrite.Close()
	if configuredRead != configuredWrite {
		configuredRead.Close()
		t.Fatal("相同 DSN 未复用连接池")
	}
	if configuredRead.Stats().MaxOpenConnections != pool.MaxOpenConns {
		t.Fatal("显式配置未生效")
	}
	resetDefaultMySQL(t)
	// 写 DSN 非法时，已经成功建立的读连接必须被清理且不能发布。
	if err := InitContext(ctx, dsn, ":invalid"); err == nil {
		t.Fatal("invalid write DSN accepted")
	}
	if read, write := DefaultDatabases(); read != nil || write != nil {
		t.Fatal("partial configuration published")
	}
	if err := InitContextWithConfig(ctx, dsn, dsn, pool); err != nil {
		t.Fatal(err)
	}
	defaultRead, defaultWrite := DefaultDatabases()
	if defaultRead == nil || defaultRead != defaultWrite || defaultWrite.Stats().MaxOpenConnections != pool.MaxOpenConns {
		t.Fatal("默认连接池未应用显式配置")
	}
	if err := InitContext(ctx, dsn, dsn); !errors.Is(err, ErrAlreadyInitialized) {
		t.Fatalf("重复初始化返回 %v", err)
	}
	if err := Close(); err != nil {
		t.Fatal(err)
	}
	if err := defaultRead.PingContext(ctx); err == nil {
		t.Fatal("默认连接池仍处于打开状态")
	}
	if err := Close(); err != nil {
		t.Fatal(err)
	}
}

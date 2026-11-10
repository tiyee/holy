package orm

import (
	"context"
	"database/sql"
	"errors"
)

// BeginTx 供上层在注册的写库显式开启事务，opts 为 nil 时使用默认事务选项。
// ctx 用于等待连接和事务生命周期。调用方负责提交或回滚，并可通过 SetDB 注入事务。
func BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	if ctx == nil {
		return nil, ErrInvalidContext
	}
	write := registeredDatabases().write
	if write == nil {
		return nil, ErrNoDatabase
	}
	return write.BeginTx(ctx, opts)
}

// Begin 使用默认事务选项从注册的写库开启事务。
func Begin(ctx context.Context) (*sql.Tx, error) {
	return BeginTx(ctx, nil)
}

// AutoTransaction 使用后台 Context 在注册的写库开启事务，并将事务句柄传给 trans。
// 回调可用 SetDB 绑定 ORM 实例。成功时提交，错误或 panic 时回滚，panic 继续传播。
func AutoTransaction(trans func(db IDBHandle) error) (err error) {
	if trans == nil {
		return ErrInvalidTransaction
	}
	write := registeredDatabases().write
	if write == nil {
		return ErrNoDatabase
	}
	tx, err := write.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			err = errors.Join(err, rollbackErr)
		}
	}()
	if err = trans(tx); err != nil {
		return err
	}
	return tx.Commit()
}

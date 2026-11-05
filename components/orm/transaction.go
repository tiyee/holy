package orm

import (
	"context"
	"database/sql"
	"errors"
)

func finishTransaction(tx *sql.Tx, fn func() error) (err error) {
	defer func() {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			err = errors.Join(err, rbErr)
		}
	}()
	if err = fn(); err != nil {
		return err
	}
	return tx.Commit()
}

// TransactionBeginner 允许事务入口注入独立连接池或测试实现。
type TransactionBeginner interface {
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
}

// WithinTransaction 供 Repository 在指定连接上编排事务；错误和 panic 回滚，成功提交。
// ORM 默认连接由初始化注册；回调通过 SetRDb/SetWDb 将实例读写切换到同一事务。
func WithinTransaction(ctx context.Context, db TransactionBeginner, opts *sql.TxOptions, fn func(*sql.Tx) error) error {
	if ctx == nil || fn == nil {
		return ErrInvalidTransaction
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if isNil(db) {
		return ErrNoDatabase
	}
	tx, err := db.BeginTx(ctx, opts)
	if err != nil {
		return err
	}
	return finishTransaction(tx, func() error { return fn(tx) })
}

// AutoTransaction 供 Repository 使用 RegisterDB 注册的写库编排事务。
// 使用 context.Background()，回调将事务句柄作为各单表 ORM 的读写连接注入。
func AutoTransaction(trans func(IDBHandle) error) error {
	if trans == nil {
		return ErrInvalidTransaction
	}
	return AutoTransactionContext(context.Background(), func(tx *sql.Tx) error { return trans(tx) })
}

// AutoTransactionContext 供 Repository 使用注册的写库编排事务，并向 BeginTx 传递 ctx。
// 回调中的 ORM 通过 SetRDb/SetWDb 切换到同一事务，并通过 WithContext(ctx) 传递请求 Context。
func AutoTransactionContext(ctx context.Context, trans func(*sql.Tx) error) error {
	if ctx == nil || trans == nil {
		return ErrInvalidTransaction
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	_, write := registeredDatabases()
	if isNil(write) {
		return ErrNoDatabase
	}
	db, ok := write.(TransactionBeginner)
	if !ok {
		return ErrTransactionUnsupported
	}
	return WithinTransaction(ctx, db, nil, trans)
}

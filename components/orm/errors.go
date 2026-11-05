// Package orm 基于 holydramon ORM v2，使用按类型缓存的 JSON 字段映射。
package orm

import "errors"

var (
	ErrInvalidModel           = errors.New("orm: model must be a non-nil struct pointer")
	ErrNoDatabase             = errors.New("orm: database is not configured")
	ErrInvalidTransaction     = errors.New("orm: transaction context and callback must be non-nil")
	ErrTransactionUnsupported = errors.New("orm: database handle cannot begin a transaction")
	ErrInvalidMapping         = errors.New("orm: invalid or duplicate column mapping")
	ErrInvalidBatchSize       = errors.New("orm: batch size must be positive")
)

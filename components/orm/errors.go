package orm

import "errors"

var (
	ErrInvalidModel              = errors.New("orm: model must be a non-nil struct pointer")
	ErrNoDatabase                = errors.New("orm: database is not configured")
	ErrDatabaseAlreadyRegistered = errors.New("orm: database pools are already registered")
	ErrInvalidContext            = errors.New("orm: nil context")
	ErrInvalidTransaction        = errors.New("orm: transaction callback must be non-nil")
	ErrInvalidMapping            = errors.New("orm: invalid or duplicate column mapping")
	ErrInvalidBatchSize          = errors.New("orm: batch size must be positive")
)

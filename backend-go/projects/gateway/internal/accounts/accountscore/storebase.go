package accountscore

import (
	"context"
	"database/sql"
	"time"
)

// Queryer abstracts *sql.DB / *sql.Tx so transactional paths never touch the
// store db while a transaction holds the single SQLite test connection.
type Queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// StoreBase is the narrow Store capability port the accounts subdomain
// packages consume instead of the facade Store (REFACTOR-0005 设计 v2).
// The facade implements it over its live Store fields, so composition-root
// tests that clone a Store and swap fields keep observing the clone.
type StoreBase interface {
	// DB is the dual-mode database handle.
	DB() *sql.DB
	// PG reports the PostgreSQL dialect (SQLite otherwise).
	PG() bool
	// Secret is the Node runtimeConfig.secret material behind the
	// credentials envelope.
	Secret() string
	// Now is the store clock.
	Now() time.Time
	// NewID mirrors Node newId(prefix).
	NewID(prefix string) string
	// Table qualifies the table name for the active dialect.
	Table(name string) string
	// Bind rewrites ? placeholders into $N for PostgreSQL.
	Bind(query string) string
	// BoolTrueLiteral / BoolFalseLiteral render the dialect booleans.
	BoolTrueLiteral() string
	BoolFalseLiteral() string
}

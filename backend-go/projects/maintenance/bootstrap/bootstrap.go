// Package bootstrap is the controlled maintenance export surface for the
// six-database SQLite ensure+seed flow (and the PostgreSQL equivalent).
//
// Baseline note (2026-09-04, user-approved amendment): the Go three-project
// baseline (docs/migration/Go三项目架构基线.md line 32) forbids gateway ->
// maintenance imports. The X05 fresh dual-mode acceptance requires the
// gateway composition root to run the same ensure+seed the Node db-service
// runs at startup (backend/src/storage/database.ts getBusinessDatabase ->
// applyBusinessSchema + seedDefaults), so the user approved a controlled
// exception: this package exposes ONLY the storage bootstrap entry points and
// owns no business logic of its own; everything else stays behind
// internal/schema. The exported types are bootstrap-local copies so callers
// never need the internal package.
package bootstrap

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
)

// SchemaCounts reports how many CREATE TABLE and CREATE INDEX statements were
// ensured for one schema.
type SchemaCounts struct {
	Tables  int
	Indexes int
}

// SQLiteSchemaKind selects one of the six SQLite storage schemas.
type SQLiteSchemaKind int

const (
	// SQLiteSchemaBusiness is the business database (accounts, routing, keys).
	SQLiteSchemaBusiness SQLiteSchemaKind = iota
	// SQLiteSchemaStats is the stats results database.
	SQLiteSchemaStats
	// SQLiteSchemaChat is the chat database.
	SQLiteSchemaChat
	// SQLiteSchemaCodexContext is one codex context state shard.
	SQLiteSchemaCodexContext
	// SQLiteSchemaDataset is the dataset catalog database.
	SQLiteSchemaDataset
	// SQLiteSchemaUsageCatalog is the usage record catalog database.
	SQLiteSchemaUsageCatalog
)

// SeedOptions carries the injected seed dependencies (time and the runtime
// secret used by the AES-256-GCM envelopes).
type SeedOptions struct {
	// Now is the seed clock; nil means time.Now. Pin it for deterministic
	// runs (Node reads new Date() once per seed run).
	Now func() time.Time
	// Secret is the runtime secret (JUHE_AI_SECRET); empty selects the Node
	// dev default.
	Secret string
}

// SQLiteSeedResult summarizes the business seed run.
type SQLiteSeedResult struct {
	StatementCount   int
	ModelCatalogRows int
}

// PGSeedResult summarizes the PostgreSQL seed run.
type PGSeedResult struct {
	StatementCount int
}

func toBootstrapCounts(counts schema.SchemaCounts) SchemaCounts {
	return SchemaCounts{Tables: counts.Tables, Indexes: counts.Indexes}
}

// EnsureSQLiteSchema applies one of the six SQLite schemas to db in the Node
// dependency position selected by kind. Every statement is IF NOT EXISTS, so
// repeated calls are no-ops.
func EnsureSQLiteSchema(ctx context.Context, kind SQLiteSchemaKind, db *sql.DB) (SchemaCounts, error) {
	ensure := func(counts schema.SchemaCounts, err error) (SchemaCounts, error) {
		if err != nil {
			return SchemaCounts{}, err
		}
		return toBootstrapCounts(counts), nil
	}
	switch kind {
	case SQLiteSchemaBusiness:
		return ensure(schema.EnsureSQLiteBusiness(ctx, db))
	case SQLiteSchemaStats:
		return ensure(schema.EnsureSQLiteStats(ctx, db))
	case SQLiteSchemaChat:
		return ensure(schema.EnsureSQLiteChat(ctx, db))
	case SQLiteSchemaCodexContext:
		return ensure(schema.EnsureSQLiteCodexContext(ctx, db))
	case SQLiteSchemaDataset:
		return ensure(schema.EnsureSQLiteDataset(ctx, db))
	case SQLiteSchemaUsageCatalog:
		return ensure(schema.EnsureSQLiteUsageCatalog(ctx, db))
	default:
		return SchemaCounts{}, fmt.Errorf("unknown sqlite schema kind %d", int(kind))
	}
}

// EnsureAllSQLite applies all six SQLite schemas to db in the Node dependency
// order (business, stats, chat, codex context state, dataset, usage catalog).
func EnsureAllSQLite(ctx context.Context, db *sql.DB) ([6]SchemaCounts, error) {
	result, err := schema.EnsureAllSQLite(ctx, db)
	if err != nil {
		return [6]SchemaCounts{}, err
	}
	return [6]SchemaCounts{
		toBootstrapCounts(result.Business),
		toBootstrapCounts(result.Stats),
		toBootstrapCounts(result.Chat),
		toBootstrapCounts(result.CodexContext),
		toBootstrapCounts(result.Dataset),
		toBootstrapCounts(result.UsageCatalog),
	}, nil
}

// EnsurePostgres applies the full PostgreSQL schema (the Node
// applyPostgresSchema port) and returns the applied statement count.
func EnsurePostgres(ctx context.Context, db *sql.DB) (int, error) {
	result, err := schema.EnsurePostgres(ctx, db)
	if err != nil {
		return 0, err
	}
	return result.StatementCount, nil
}

// SeedSQLiteBusiness runs the full Node seedDefaults port over one business
// database (the only seeded SQLite database, exactly like Node).
func SeedSQLiteBusiness(ctx context.Context, db *sql.DB, options SeedOptions) (SQLiteSeedResult, error) {
	result, err := schema.SeedSQLiteDefaults(ctx, db, schema.SeedOptions{Now: options.Now, Secret: options.Secret})
	if err != nil {
		return SQLiteSeedResult{}, err
	}
	return SQLiteSeedResult{StatementCount: result.StatementCount, ModelCatalogRows: result.ModelCatalogRows}, nil
}

// SeedPostgres runs the full Node seedPostgresDefaults port (the Node
// init-postgres-schema.ts flow after EnsurePostgres).
func SeedPostgres(ctx context.Context, db *sql.DB, options SeedOptions) (PGSeedResult, error) {
	result, err := schema.SeedPostgresDefaults(ctx, db, schema.SeedOptions{Now: options.Now, Secret: options.Secret})
	if err != nil {
		return PGSeedResult{}, err
	}
	return PGSeedResult{StatementCount: result.StatementCount}, nil
}

// OpenSQLiteFile opens (creating when missing, like the Node DatabaseSync
// constructor after its recursive mkdirSync of the parent directory) one
// SQLite file handle with the Node-compatible connection pragmas (foreign
// keys, busy timeout, WAL) and a single-connection pool, mirroring the Node
// per-file usage.
func OpenSQLiteFile(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create sqlite directory %s: %w", filepath.Dir(path), err)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite file %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{"PRAGMA foreign_keys=ON", "PRAGMA busy_timeout=5000", "PRAGMA journal_mode=WAL"} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("configure sqlite file %s: %w", path, err)
		}
	}
	return db, nil
}

// Gateway SQLite startup storage preflight (X05 / BUG-0167-0168).
//
// Node db-service ensures the six-database schema and seeds the business
// database on every owned open (backend/src/storage/database.ts
// getBusinessDatabase -> applyBusinessSchema + seedDefaults, plus the lazy
// per-file schema application for chat/dataset/usage-catalog/stats/
// codex-context-state). This file wires the Go composition root to the same
// behavior in SQLite mode through the maintenance bootstrap export surface
// (user-approved baseline amendment documented in go.mod and
// projects/maintenance/bootstrap/bootstrap.go).
//
// PostgreSQL mode intentionally has NO startup ensure+seed: the Node runtime
// refuses JUHE_AI_DATABASE_DRIVER=postgres outright and the PG schema/seed is
// executed by the explicit maintenance path (Node
// scripts/maintenance/init-postgres-schema.ts, Go
// juhe-ai-maintenance --ensure-schema --seed --driver postgres --dsn ...).
package main

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/bootstrap"
)

// ensureGatewaySQLiteStoragePreflight runs the six-database ensure+seed the
// Node db-service performs on startup. The business handle is already open
// (and configured) by the composition root; the remaining five databases are
// opened here with the Node-compatible pragmas, ensured schema-only, and
// closed (Node applies schema lazily at first open with no seed beyond the
// business database). The ensure order mirrors the Node dependency order
// (business, stats, chat, codex-context shards, dataset, usage-catalog).
func ensureGatewaySQLiteStoragePreflight(ctx context.Context, cfg runtimeConfig, businessDB *sql.DB) error {
	if _, err := bootstrap.EnsureSQLiteSchema(ctx, bootstrap.SQLiteSchemaBusiness, businessDB); err != nil {
		return fmt.Errorf("ensure business sqlite schema: %w", err)
	}
	if _, err := bootstrap.SeedSQLiteBusiness(ctx, businessDB, bootstrap.SeedOptions{Secret: cfg.Secret}); err != nil {
		return fmt.Errorf("seed business sqlite defaults: %w", err)
	}

	// The auxiliary five databases must be explicitly configured like every
	// other Go storage path (no CWD-relative default; the Node distinct
	// storage path proof requires explicit files as well).
	missing := make([]string, 0, 4)
	if cfg.ChatDatabasePath == "" {
		missing = append(missing, "JUHE_AI_CHAT_DATABASE_PATH")
	}
	if cfg.DatasetDatabasePath == "" {
		missing = append(missing, "JUHE_AI_DATASET_DATABASE_PATH")
	}
	if cfg.UsageCatalogDatabasePath == "" {
		missing = append(missing, "JUHE_AI_USAGE_CATALOG_DATABASE_PATH")
	}
	if cfg.CodexContextShardRoot == "" {
		missing = append(missing, "JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT")
	}
	if len(missing) > 0 {
		return fmt.Errorf("sqlite 模式启动 preflight 需要六库路径，缺少 %s", strings.Join(missing, "、"))
	}
	if cfg.StatsDatabasePath == "" {
		return fmt.Errorf("sqlite 模式缺少 JUHE_AI_STATS_DATABASE_PATH，无法打开 ip-stats stats 数据库")
	}

	ensureFile := func(name, path string, kind bootstrap.SQLiteSchemaKind) error {
		db, err := bootstrap.OpenSQLiteFile(path)
		if err != nil {
			return err
		}
		defer db.Close()
		if _, err := bootstrap.EnsureSQLiteSchema(ctx, kind, db); err != nil {
			return fmt.Errorf("ensure %s sqlite schema: %w", name, err)
		}
		return nil
	}
	if err := ensureFile("stats", cfg.StatsDatabasePath, bootstrap.SQLiteSchemaStats); err != nil {
		return err
	}
	if err := ensureFile("chat", cfg.ChatDatabasePath, bootstrap.SQLiteSchemaChat); err != nil {
		return err
	}
	for shardIndex := 0; shardIndex < cfg.CodexContextShardCount; shardIndex++ {
		shardPath := filepath.Join(cfg.CodexContextShardRoot, fmt.Sprintf("state-%03d.sqlite3", shardIndex))
		if err := ensureFile(fmt.Sprintf("codex-context[%d]", shardIndex), shardPath, bootstrap.SQLiteSchemaCodexContext); err != nil {
			return err
		}
	}
	if err := ensureFile("dataset", cfg.DatasetDatabasePath, bootstrap.SQLiteSchemaDataset); err != nil {
		return err
	}
	if err := ensureFile("usage-catalog", cfg.UsageCatalogDatabasePath, bootstrap.SQLiteSchemaUsageCatalog); err != nil {
		return err
	}
	return nil
}

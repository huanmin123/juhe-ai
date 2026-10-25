// Package bootstrap is the controlled maintenance export surface for the
// PostgreSQL storage ensure+seed flow.
//
// Baseline note (2026-09-04, user-approved amendment): the Go three-project
// baseline (docs/architecture/Go三项目架构基线.md line 32) forbids gateway ->
// maintenance imports. The X05 acceptance required the gateway composition
// root to run the same ensure+seed the Node db-service runs at startup, so
// the user approved a controlled exception: this package exposes ONLY the
// storage bootstrap entry points and owns no business logic of its own;
// everything else stays behind internal/schema. The exported types are
// bootstrap-local copies so callers never need the internal package.
//
// 2026-10-24 SQLite 与内存模式彻底退役（PLAN-20261024T051925945Z B4）：
// 六库 SQLite ensure+seed 导出面整体删除，本包收敛为 PostgreSQL 单形态。
package bootstrap

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/capabilitydomain"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
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

// PGSeedResult summarizes the PostgreSQL seed run.
type PGSeedResult struct {
	StatementCount int
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

// SeedPostgres runs the full Node seedPostgresDefaults port (the Node
// init-postgres-schema.ts flow after EnsurePostgres). After a successful seed
// it runs the fresh-install capability domain seal (AI account capability
// domain split, M2): when the stage record is absent and accounts has no rows
// it executes empty-database M1 -> M2 cutover and writes m2_finalized; an
// existing record or a non-empty accounts table always skips (explicit
// migration owns real cutovers).
func SeedPostgres(ctx context.Context, db *sql.DB, options SeedOptions) (PGSeedResult, error) {
	result, err := schema.SeedPostgresDefaults(ctx, db, schema.SeedOptions{Now: options.Now, Secret: options.Secret})
	if err != nil {
		return PGSeedResult{}, err
	}
	if _, err := capabilitydomain.SealFreshInstall(ctx, db); err != nil {
		return PGSeedResult{}, fmt.Errorf("seed postgres defaults: %w", err)
	}
	return PGSeedResult{StatementCount: result.StatementCount}, nil
}

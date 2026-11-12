package timeinventory

import (
	"context"
	"database/sql"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// TestDryRunInstantColumnMigration 是 Phase 2 迁移的 dry-run 骨架：
// 在真实 PG 临时库上建 text 时间列 → 事务内 ALTER TYPE → 用 inventory
// 复核类型/依赖/行数 → 回滚。外部依赖门由 pgtestkit 统一处理（默认 SKIP）。
func TestDryRunInstantColumnMigration(t *testing.T) {
	db := pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		statements := []string{
			`CREATE SCHEMA juhe_business`,
			`CREATE TABLE juhe_business.time_dryrun (
				id text NOT NULL,
				created_at text NOT NULL,
				updated_at text NOT NULL,
				PRIMARY KEY (id, created_at)
			)`,
			`CREATE INDEX idx_time_dryrun_created ON juhe_business.time_dryrun (created_at)`,
			`INSERT INTO juhe_business.time_dryrun (id, created_at, updated_at) VALUES
				('a', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z'),
				('b', '2026-01-01T23:59:59.999Z', '2026-01-01T23:59:59.999Z')`,
		}
		for _, statement := range statements {
			if _, err := db.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
		return nil
	})
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		`ALTER TABLE juhe_business.time_dryrun ALTER COLUMN created_at TYPE timestamptz(3) USING created_at::timestamptz`); err != nil {
		t.Fatalf("ALTER TYPE: %v", err)
	}
	report, err := Collect(ctx, tx, []string{"juhe_business"})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	var found *Column
	for index := range report.Columns {
		if report.Columns[index].Table == "time_dryrun" && report.Columns[index].Column == "created_at" {
			found = &report.Columns[index]
			break
		}
	}
	if found == nil {
		t.Fatal("清单缺少 time_dryrun.created_at")
	}
	if found.DataType != "timestamp with time zone" || found.Target.Type != "timestamptz(3)" {
		t.Fatalf("迁移后类型 = %s / %s", found.DataType, found.Target.Type)
	}
	if !found.Dependencies.PrimaryKey {
		t.Fatal("created_at 应被主键引用")
	}
	if len(found.Dependencies.Indexes) == 0 {
		t.Fatal("created_at 应被索引引用")
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM juhe_business.time_dryrun`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Fatalf("行数 = %d, want 2", count)
	}
}

// TestDryRunPartitionKeyAlterRejected 固定 Phase 4 的硬约束：分区键列不能
// 原地 ALTER TYPE，必须整族重建。
func TestDryRunPartitionKeyAlterRejected(t *testing.T) {
	db := pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		statements := []string{
			`CREATE SCHEMA juhe_usage`,
			`CREATE TABLE juhe_usage.usage_records (
				created_at text NOT NULL,
				id text NOT NULL,
				PRIMARY KEY (created_at, id)
			) PARTITION BY RANGE (created_at)`,
			`CREATE TABLE juhe_usage.usage_records_20260101 PARTITION OF juhe_usage.usage_records
				FOR VALUES FROM ('2026-01-01') TO ('2026-01-02')`,
		}
		for _, statement := range statements {
			if _, err := db.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
		return nil
	})
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		`ALTER TABLE juhe_usage.usage_records ALTER COLUMN created_at TYPE timestamptz USING created_at::timestamptz`); err == nil {
		t.Fatal("分区键列应拒绝原地 ALTER TYPE")
	}
}

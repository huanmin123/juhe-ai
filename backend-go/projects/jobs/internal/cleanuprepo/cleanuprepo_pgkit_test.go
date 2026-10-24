package cleanuprepo

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// cleanuprepo_pgkit_test.go 是真实建库测试迁 PG 的共享基建
// （PLAN-20261024T051925945Z B1 POC#2）：对「双方言单入口」的测试
// （deleteRowsBefore 家族 / execChanged 家族 / UpsertAccountUsageSnapshots 等，
// 生产代码按 DB.Postgres 内部分派），建库改走 pgtestkit 隔离 PG 测试库，
// DDL/种子/断言 SQL 与生产 PG 臂精确对齐（schema 限定 juhe_*、$n 占位符、
// bigint/double precision/text 列类型，对照 maintenance pg_schema_stats.go）。
// 被测对象为 SQLite-only 生产臂的测试（分片/镜像半区/codex 分片/SQLite 结算
// 链/SQLite 错误注入臂）不在本迁移面，其 sqlite 建库设施保持原状，随
// PLAN B3/B6 删生产 sqlite 臂批次一并退役。
//
// scripted 假驱动（pgRecorder / kitFailingPGConn / w13e 装饰句柄）不依赖真库，
// 保持原状（PLAN §3 非目标）。

// openKitPostgres 打开 pgtestkit 隔离 PG 测试库并返回 PG 方言句柄；prepare
// 回调内建该测试所需的最小 schema（生产 PG 臂按 schema 限定访问，DDL 与
// 生产 schema 名一一对应）。测试结束自动删库。
func openKitPostgres(t *testing.T, prepare func(ctx context.Context, db *sql.DB) error) *DB {
	t.Helper()
	return &DB{DB: pgtestkit.New(t, prepare), Postgres: true}
}

// kitPGSchemaStats 是 stats 家族表的生产 PG schema（deleteRowsBefore /
// schemaForStats 的 PG 方言值；与组合根 openDual 的 "juhe_stats" 一致）。
const kitPGSchemaStats = "juhe_stats"

// TestUpsertAccountUsageSnapshots：UpsertAccountUsageSnapshots 的 PG 分支
// （juhe_business.accounts 归属读取 + juhe_stats.account_usage_snapshots
// EXCLUDED 大写 upsert）在真实 PG 上的 upsert、缺省 updatedAt 与缺归属报错。
// 单库双 schema 双句柄，与组合根 openDual 的单 PG 库装配一致。
func TestUpsertAccountUsageSnapshots(t *testing.T) {
	db := openKitPostgres(t, func(ctx context.Context, raw *sql.DB) error {
		_, err := raw.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS juhe_business;
CREATE SCHEMA IF NOT EXISTS juhe_stats;
CREATE TABLE juhe_business.accounts (id TEXT PRIMARY KEY, system_account_id TEXT);
CREATE TABLE juhe_stats.account_usage_snapshots (
      system_account_id TEXT NOT NULL, account_id TEXT NOT NULL, kind TEXT NOT NULL,
      source TEXT, snapshot_json TEXT NOT NULL, refresh_status TEXT,
      last_success_at TEXT, last_error_message TEXT, updated_at TEXT, created_at TEXT,
      PRIMARY KEY (system_account_id, account_id, kind))`)
		return err
	})
	business := &DB{DB: db.DB, Postgres: true}
	stats := &DB{DB: db.DB, Postgres: true}
	store := &RecordCleanupStore{Stats: stats, Now: kitNow}
	mustExecKit(t, business, `INSERT INTO juhe_business.accounts (id, system_account_id) VALUES ('acc-1','owner-1'), ('acc-2','owner-2')`)

	if err := store.UpsertAccountUsageSnapshots(context.Background(), business, nil); err != nil {
		t.Fatalf("空输入应直接返回 nil：%v", err)
	}
	if err := store.UpsertAccountUsageSnapshots(context.Background(), business, []retention.AccountUsageSnapshotUpsertInput{
		{AccountID: "acc-1", Kind: "openai_codex", Source: "job", Snapshot: map[string]any{"requests": 1.0}, UpdatedAt: "bad-time"},
	}); err == nil {
		t.Fatalf("非法 updatedAt 应报错")
	}
	if err := store.UpsertAccountUsageSnapshots(context.Background(), business, []retention.AccountUsageSnapshotUpsertInput{
		{AccountID: "acc-missing", Kind: "openai_codex"},
	}); err == nil || !strings.Contains(err.Error(), "缺少账户归属") {
		t.Fatalf("缺归属应报错：%v", err)
	}
	if err := store.UpsertAccountUsageSnapshots(context.Background(), business, []retention.AccountUsageSnapshotUpsertInput{
		{AccountID: "acc-1", Kind: "openai_codex", Source: "job", Snapshot: map[string]any{"requests": 1.0}},
		{AccountID: "acc-2", Kind: "openai_codex", Source: "job", Snapshot: map[string]any{"requests": 2.0}, UpdatedAt: "2026-08-01T00:00:00.000Z"},
	}); err != nil {
		t.Fatalf("UpsertAccountUsageSnapshots: %v", err)
	}
	var count int64
	if err := stats.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM juhe_stats.account_usage_snapshots`).Scan(&count); err != nil {
		t.Fatalf("read snapshots: %v", err)
	}
	if count != 2 {
		t.Fatalf("快照行数 = %d, 期望 2", count)
	}
	// 同 key 二次 upsert 覆盖（refresh_status 回 fresh）。
	if err := store.UpsertAccountUsageSnapshots(context.Background(), business, []retention.AccountUsageSnapshotUpsertInput{
		{AccountID: "acc-1", Kind: "openai_codex", Source: "retry", Snapshot: map[string]any{"requests": 9.0}},
	}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	var source string
	if err := stats.QueryRowContext(context.Background(), `SELECT source FROM juhe_stats.account_usage_snapshots WHERE account_id = 'acc-1'`).Scan(&source); err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if source != "retry" {
		t.Fatalf("upsert 未覆盖 source：%q", source)
	}
}

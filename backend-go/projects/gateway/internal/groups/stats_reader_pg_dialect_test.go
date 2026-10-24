package groups

// PG 方言契约回归（生产事故：列表账户数/当前并发/状态三列恒 0/“未绑定”）：
// GroupAccountStatsDBReader 在 PostgreSQL 上必须以 juhe_stats. 限定
// group_account_stats（生产 PG 无 search_path，裸表名 42P01 relation does
// not exist → hydrate 错误被 hydrateListAccountStats 回退空投影 → 三列全 0）。
// SQLite 分支保持裸表名不变。
//
// PG 臂夹具（组A 迁移，PLAN-20261024T051925945Z）：pgtestkit 独立 PG 库 +
// 生产同款 juhe_stats.group_account_stats 限定形式；列定义照抄 maintenance
// pg_schema_stats.go 的 group_account_stats 建表。SQLite 裸表名分支（组B）
// 保持 sqlite 夹具原状，随生产 sqlite 臂删除批次同批退役。

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
	_ "modernc.org/sqlite"
)

const groupAccountStatsFixtureDDL = `CREATE TABLE juhe_stats.group_account_stats (
		system_account_id text NOT NULL,
		group_id text NOT NULL,
		total integer NOT NULL DEFAULT 0,
		available integer NOT NULL DEFAULT 0,
		active integer NOT NULL DEFAULT 0,
		disabled integer NOT NULL DEFAULT 0,
		error integer NOT NULL DEFAULT 0,
		rate_limited integer NOT NULL DEFAULT 0,
		current_concurrency integer NOT NULL DEFAULT 0,
		concurrency_limit integer NOT NULL DEFAULT 0,
		updated_at text NOT NULL,
		PRIMARY KEY (system_account_id, group_id)
	)`

func newStatsReaderPGFixture(t *testing.T, insertSQL string, args ...any) *sql.DB {
	t.Helper()
	db := pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS juhe_stats;`+groupAccountStatsFixtureDDL)
		return err
	})
	if _, err := db.Exec(insertSQL, args...); err != nil {
		t.Fatal(err)
	}
	return db
}

// TestGroupAccountStatsDBReaderPGTableQualification locks the production
// dialect contract: the PG reader resolves juhe_stats.group_account_stats (no
// bare-table 42P01) and scans the counters, while the SQLite reader keeps the
// bare table name.
func TestGroupAccountStatsDBReaderPGTableQualification(t *testing.T) {
	pgDB := newStatsReaderPGFixture(t,
		`INSERT INTO juhe_stats.group_account_stats
			(system_account_id, group_id, total, available, active, disabled, error,
			 rate_limited, current_concurrency, concurrency_limit, updated_at)
		 VALUES ('owner-1', 'grp_1', 3, 2, 2, 1, 0, 0, 5, 10, '2026-09-27T00:00:00.000Z')`)

	reader := NewGroupAccountStatsDBReader(pgDB, true)
	stats, err := reader.ReadGroupAccountStats(context.Background(), []string{"grp_1", "grp_missing"})
	if err != nil {
		t.Fatalf("pg reader must resolve juhe_stats.group_account_stats: %v", err)
	}
	row, ok := stats["grp_1"]
	if !ok {
		t.Fatalf("pg reader lost grp_1 row: %+v", stats)
	}
	if row.Total != 3 || row.Available != 2 || row.Active != 2 || row.Disabled != 1 ||
		row.RateLimited != 0 || row.CurrentConcurrency != 5 || row.ConcurrencyLimit != 10 {
		t.Fatalf("pg reader counters mismatch: %+v", row)
	}
	if _, ok := stats["grp_missing"]; ok {
		t.Fatalf("missing group must stay absent: %+v", stats)
	}

	// SQLite 分支保持裸表名（现状契约不回退）。
	sqliteDB, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "group-stats-dialect-sqlite.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqliteDB.Close() })
	sqliteDB.SetMaxOpenConns(1)
	if _, err := sqliteDB.Exec(`CREATE TABLE group_account_stats (
		system_account_id text NOT NULL, group_id text NOT NULL,
		total integer NOT NULL DEFAULT 0, available integer NOT NULL DEFAULT 0,
		active integer NOT NULL DEFAULT 0, disabled integer NOT NULL DEFAULT 0,
		error integer NOT NULL DEFAULT 0, rate_limited integer NOT NULL DEFAULT 0,
		current_concurrency integer NOT NULL DEFAULT 0, concurrency_limit integer NOT NULL DEFAULT 0,
		updated_at text NOT NULL, PRIMARY KEY (system_account_id, group_id))`); err != nil {
		t.Fatal(err)
	}
	if _, err := sqliteDB.Exec(`INSERT INTO group_account_stats
		(system_account_id, group_id, total, updated_at)
		VALUES ('owner-1', 'grp_sq', 7, '2026-09-27T00:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	sqliteReader := NewGroupAccountStatsDBReader(sqliteDB, false)
	sqliteStats, err := sqliteReader.ReadGroupAccountStats(context.Background(), []string{"grp_sq"})
	if err != nil {
		t.Fatalf("sqlite reader must keep the bare table name: %v", err)
	}
	if row, ok := sqliteStats["grp_sq"]; !ok || row.Total != 7 {
		t.Fatalf("sqlite reader counters mismatch: %+v", sqliteStats)
	}
}

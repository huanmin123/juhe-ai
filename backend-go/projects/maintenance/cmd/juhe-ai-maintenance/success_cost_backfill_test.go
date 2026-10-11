package main

// --apply-success-cost-backfill CLI 测试（成功成本历史回填治理，2026-10-12）：
// 新库 0 批秒 done、零错误行回填、混合行不动、已回填无 marker 收敛、持久化
// cursor 续跑、--dry-run 零副作用、marker 缺失显式报错、用法错误与互斥，
// 以及 PG 方言的语句形态（表名 juhe_stats 限定 + $n 占位符）。
// 全部进程内直调（测试单进程纪律），SQLite 夹具走生产 EnsureSQLiteSchema
// （stats 脚本含 backfill_progress 建表 DDL）；本包不依赖真实 PostgreSQL。

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/bootstrap"
	_ "modernc.org/sqlite"
)

// backfillFixturePaths 建立隔离 stats SQLite 文件并返回 --paths 参数值。
// stats 库跑生产 EnsureSQLiteSchema：建表集含 backfill_progress（六表
// success_cost_usd 列由建表 DDL 直接声明）。
func backfillFixturePaths(t *testing.T) (statsPath, pathsFlag string) {
	t.Helper()
	statsPath = filepath.Join(t.TempDir(), "stats.sqlite3")
	db := openRebuildSQLite(t, statsPath)
	defer db.Close()
	if _, err := bootstrap.EnsureSQLiteSchema(context.Background(), bootstrap.SQLiteSchemaStats, db); err != nil {
		t.Fatalf("EnsureSQLiteStats 失败: %v", err)
	}
	return statsPath, "stats=" + filepath.ToSlash(statsPath)
}

// backfillInsertTotal 预置一行 usage_stats_totals（主键三元组 + 成本三元组）。
func backfillInsertTotal(t *testing.T, db *sql.DB, systemAccountID, scopeType, scopeID string, errorCount int, totalCost, successCost float64) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO usage_stats_totals
		(system_account_id, scope_type, scope_id, error_count, total_cost_usd, success_cost_usd, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, '2026-01-01T00:00:00.000Z')`,
		systemAccountID, scopeType, scopeID, errorCount, totalCost, successCost); err != nil {
		t.Fatalf("预置 usage_stats_totals 行失败: %v", err)
	}
}

// backfillTotalCosts 读取一行 usage_stats_totals 的 (error, total, success)。
func backfillTotalCosts(t *testing.T, db *sql.DB, systemAccountID string) (errorCount int, totalCost, successCost float64) {
	t.Helper()
	if err := db.QueryRow(`SELECT error_count, total_cost_usd, success_cost_usd FROM usage_stats_totals
		WHERE system_account_id = ? AND scope_type = 'system_account' AND scope_id = ?`,
		systemAccountID, systemAccountID).Scan(&errorCount, &totalCost, &successCost); err != nil {
		t.Fatalf("读取 usage_stats_totals 行失败: %v", err)
	}
	return errorCount, totalCost, successCost
}

// backfillMarkerRows 返回 marker 表 (table_name, status, cursor_json) 行集。
func backfillMarkerRows(t *testing.T, db *sql.DB) map[string][2]string {
	t.Helper()
	rows, err := db.Query(`SELECT table_name, status, cursor_json FROM backfill_progress`)
	if err != nil {
		t.Fatalf("读取 backfill_progress 失败: %v", err)
	}
	defer rows.Close()
	markers := map[string][2]string{}
	for rows.Next() {
		var table, status, cursor string
		if err := rows.Scan(&table, &status, &cursor); err != nil {
			t.Fatalf("scan backfill_progress 行失败: %v", err)
		}
		markers[table] = [2]string{status, cursor}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate backfill_progress 失败: %v", err)
	}
	return markers
}

// runApplySuccessCostBackfill 进程内直调子命令并解析 stdout JSON 报告。
func runApplySuccessCostBackfill(t *testing.T, args ...string) (int, successCostBackfillReport, string) {
	t.Helper()
	code, stdout, stderr := wmRunMaintenanceCapture(t, args...)
	var report successCostBackfillReport
	if strings.TrimSpace(stdout) != "" {
		if err := json.Unmarshal([]byte(stdout), &report); err != nil {
			t.Fatalf("解析报告失败: %v, stdout=%s", err, stdout)
		}
	}
	return code, report, stderr
}

// TestApplySuccessCostBackfillFreshDatabaseConvergesWithoutBatches：新库
// 六表为空，0 批秒 done，marker 六行全 done 且 cursor 清空。
func TestApplySuccessCostBackfillFreshDatabaseConvergesWithoutBatches(t *testing.T) {
	statsPath, pathsFlag := backfillFixturePaths(t)
	code, report, stderr := runApplySuccessCostBackfill(t, "-apply-success-cost-backfill", "-driver", "sqlite", "-paths", pathsFlag)
	if code != 0 {
		t.Fatalf("新库回填退出码 = %d, want 0, stderr=%s", code, stderr)
	}
	if !report.Completed || report.DryRun || report.Driver != "sqlite" {
		t.Fatalf("报告 completed/dryRun/driver = %v/%v/%s, want true/false/sqlite", report.Completed, report.DryRun, report.Driver)
	}
	if len(report.Tables) != len(successCostBackfillTargets) {
		t.Fatalf("报告表数 = %d, want %d", len(report.Tables), len(successCostBackfillTargets))
	}
	db := openRebuildSQLite(t, statsPath)
	defer db.Close()
	markers := backfillMarkerRows(t, db)
	for _, target := range successCostBackfillTargets {
		stat := report.Tables[target.name]
		if stat.Status != "done" || stat.Batches != 0 || stat.MatchedRows != 0 {
			t.Fatalf("%s 报告 = %+v, want {done 0 0}", target.name, stat)
		}
		marker := markers[target.name]
		if marker != [2]string{"done", "{}"} {
			t.Fatalf("%s marker = %v, want [done {}]", target.name, marker)
		}
	}
}

// TestApplySuccessCostBackfillCopiesZeroErrorRows：预置 (error 0, total 10,
// success 0) → (0, 10, 10)；已等值行不动；marker 收敛 done。
func TestApplySuccessCostBackfillCopiesZeroErrorRows(t *testing.T) {
	statsPath, pathsFlag := backfillFixturePaths(t)
	db := openRebuildSQLite(t, statsPath)
	defer db.Close()
	backfillInsertTotal(t, db, "u1", "system_account", "u1", 0, 10, 0)
	backfillInsertTotal(t, db, "u2", "system_account", "u2", 0, 5, 5)
	if _, err := db.Exec(`INSERT INTO usage_stats_minute
		(system_account_id, scope_type, scope_id, stat_minute, error_count, total_cost_usd, success_cost_usd, updated_at)
		VALUES ('u1', 'system_account', 'u1', '2026-01-02T03:04', 0, 10, 0, '2026-01-01T00:00:00.000Z')`); err != nil {
		t.Fatalf("预置 usage_stats_minute 行失败: %v", err)
	}

	code, report, stderr := runApplySuccessCostBackfill(t, "-apply-success-cost-backfill", "-driver", "sqlite", "-paths", pathsFlag)
	if code != 0 {
		t.Fatalf("回填退出码 = %d, want 0, stderr=%s", code, stderr)
	}
	if got := report.Tables["usage_stats_totals"]; got.MatchedRows != 1 || got.Batches != 1 || got.Status != "done" {
		t.Fatalf("totals 报告 = %+v, want matched=1 batches=1 done", got)
	}
	if got := report.Tables["usage_stats_minute"]; got.MatchedRows != 1 || got.Status != "done" {
		t.Fatalf("minute 报告 = %+v, want matched=1 done", got)
	}
	if _, total, success := backfillTotalCosts(t, db, "u1"); total != 10 || success != 10 {
		t.Fatalf("u1 (total, success) = (%v, %v), want (10, 10)", total, success)
	}
	if _, total, success := backfillTotalCosts(t, db, "u2"); total != 5 || success != 5 {
		t.Fatalf("u2 (total, success) = (%v, %v), want (5, 5)（已等值行零改动）", total, success)
	}
	var minuteSuccess float64
	if err := db.QueryRow(`SELECT success_cost_usd FROM usage_stats_minute
		WHERE system_account_id = 'u1' AND stat_minute = '2026-01-02T03:04'`).Scan(&minuteSuccess); err != nil {
		t.Fatalf("读取 minute 行失败: %v", err)
	}
	if minuteSuccess != 10 {
		t.Fatalf("minute success_cost_usd = %v, want 10", minuteSuccess)
	}
	markers := backfillMarkerRows(t, db)
	if markers["usage_stats_totals"] != [2]string{"done", "{}"} || markers["usage_stats_minute"] != [2]string{"done", "{}"} {
		t.Fatalf("markers = %v, want totals/minute 全 done 且 cursor 清空", markers)
	}
}

// TestApplySuccessCostBackfillLeavesMixedRowsUntouched：预置混合行
// (error 1, total 10, success 0) 不被臆算。
func TestApplySuccessCostBackfillLeavesMixedRowsUntouched(t *testing.T) {
	statsPath, pathsFlag := backfillFixturePaths(t)
	db := openRebuildSQLite(t, statsPath)
	defer db.Close()
	backfillInsertTotal(t, db, "m1", "system_account", "m1", 1, 10, 0)

	code, report, stderr := runApplySuccessCostBackfill(t, "-apply-success-cost-backfill", "-driver", "sqlite", "-paths", pathsFlag)
	if code != 0 {
		t.Fatalf("回填退出码 = %d, want 0, stderr=%s", code, stderr)
	}
	if errorCount, total, success := backfillTotalCosts(t, db, "m1"); errorCount != 1 || total != 10 || success != 0 {
		t.Fatalf("混合行被改动: (error, total, success) = (%d, %v, %v), want (1, 10, 0)", errorCount, total, success)
	}
	if got := report.Tables["usage_stats_totals"]; got.MatchedRows != 0 || got.Status != "done" {
		t.Fatalf("totals 报告 = %+v, want matched=0 done（混合行不命中谓词仍收敛）", got)
	}
}

// TestApplySuccessCostBackfillBackfilledWithoutMarkerConverges：生产现状
// （数据已回填、marker 为空）分批扫过命中 0 行，自然收敛 done。
func TestApplySuccessCostBackfillBackfilledWithoutMarkerConverges(t *testing.T) {
	statsPath, pathsFlag := backfillFixturePaths(t)
	db := openRebuildSQLite(t, statsPath)
	defer db.Close()
	backfillInsertTotal(t, db, "u1", "system_account", "u1", 0, 10, 10)
	if got := len(backfillMarkerRows(t, db)); got != 0 {
		t.Fatalf("前置 marker 行数 = %d, want 0", got)
	}

	code, report, stderr := runApplySuccessCostBackfill(t, "-apply-success-cost-backfill", "-driver", "sqlite", "-paths", pathsFlag)
	if code != 0 {
		t.Fatalf("回填退出码 = %d, want 0, stderr=%s", code, stderr)
	}
	got := report.Tables["usage_stats_totals"]
	if got.Status != "done" || got.Batches != 1 || got.MatchedRows != 0 {
		t.Fatalf("totals 报告 = %+v, want done/1 批/0 命中（有界核对扫过全表）", got)
	}
	if markers := backfillMarkerRows(t, db); markers["usage_stats_totals"] != [2]string{"done", "{}"} {
		t.Fatalf("marker = %v, want [done {}]", markers["usage_stats_totals"])
	}
}

// TestApplySuccessCostBackfillResumesFromPersistedCursor：预置 running
// marker + cursor 中间态，重跑只处理 cursor 之后的行，已处理行不重改。
func TestApplySuccessCostBackfillResumesFromPersistedCursor(t *testing.T) {
	statsPath, pathsFlag := backfillFixturePaths(t)
	db := openRebuildSQLite(t, statsPath)
	defer db.Close()
	// cursor 之前的行（上一次中断前已处理）恒为等值 (0,10,10)；cursor 之后
	// 的行保持待回填 (0,10,0)。
	backfillInsertTotal(t, db, "a", "system_account", "a", 0, 10, 10)
	backfillInsertTotal(t, db, "b", "system_account", "b", 0, 10, 10)
	backfillInsertTotal(t, db, "c", "system_account", "c", 0, 10, 0)
	if _, err := db.Exec(`INSERT INTO backfill_progress (table_name, status, cursor_json, updated_at)
		VALUES ('usage_stats_totals', 'running', '["b","system_account","b"]', '2026-01-01T00:00:00.000Z')`); err != nil {
		t.Fatalf("预置 running marker 失败: %v", err)
	}

	code, report, stderr := runApplySuccessCostBackfill(t, "-apply-success-cost-backfill", "-driver", "sqlite", "-paths", pathsFlag)
	if code != 0 {
		t.Fatalf("续跑退出码 = %d, want 0, stderr=%s", code, stderr)
	}
	got := report.Tables["usage_stats_totals"]
	if got.Status != "done" || got.Batches != 1 || got.MatchedRows != 1 {
		t.Fatalf("totals 报告 = %+v, want done/1 批/1 命中（仅 cursor 之后的 c 行）", got)
	}
	if _, total, success := backfillTotalCosts(t, db, "c"); total != 10 || success != 10 {
		t.Fatalf("c (total, success) = (%v, %v), want (10, 10)", total, success)
	}
	for _, id := range []string{"a", "b"} {
		if _, total, success := backfillTotalCosts(t, db, id); total != 10 || success != 10 {
			t.Fatalf("%s (total, success) = (%v, %v), want (10, 10)（已处理行不重改）", id, total, success)
		}
	}
	if markers := backfillMarkerRows(t, db); markers["usage_stats_totals"] != [2]string{"done", "{}"} {
		t.Fatalf("marker = %v, want [done {}]", markers["usage_stats_totals"])
	}
}

// TestApplySuccessCostBackfillDryRunReadOnly：--dry-run 只统计待回填行数，
// 不改数据、不读不写 marker。
func TestApplySuccessCostBackfillDryRunReadOnly(t *testing.T) {
	statsPath, pathsFlag := backfillFixturePaths(t)
	db := openRebuildSQLite(t, statsPath)
	defer db.Close()
	backfillInsertTotal(t, db, "u1", "system_account", "u1", 0, 10, 0)

	code, report, stderr := runApplySuccessCostBackfill(t, "-apply-success-cost-backfill", "-dry-run", "-driver", "sqlite", "-paths", pathsFlag)
	if code != 0 {
		t.Fatalf("dry-run 退出码 = %d, want 0, stderr=%s", code, stderr)
	}
	if !report.DryRun || report.Completed {
		t.Fatalf("报告 dryRun/completed = %v/%v, want true/false", report.DryRun, report.Completed)
	}
	if got := report.Tables["usage_stats_totals"]; got.Status != "dry-run" || got.MatchedRows != 1 || got.Batches != 0 {
		t.Fatalf("totals 报告 = %+v, want dry-run/0 批/1 待回填", got)
	}
	if errorCount, total, success := backfillTotalCosts(t, db, "u1"); errorCount != 0 || total != 10 || success != 0 {
		t.Fatalf("dry-run 改动了数据: (error, total, success) = (%d, %v, %v), want (0, 10, 0)", errorCount, total, success)
	}
	if got := len(backfillMarkerRows(t, db)); got != 0 {
		t.Fatalf("dry-run 写了 marker: 行数 = %d, want 0", got)
	}
}

// TestApplySuccessCostBackfillMissingMarkerTableFailsExplicitly：marker 表
// 缺失时显式报错（exit 1）并指引 --ensure-schema，不静默建表。
func TestApplySuccessCostBackfillMissingMarkerTableFailsExplicitly(t *testing.T) {
	statsPath, pathsFlag := backfillFixturePaths(t)
	db := openRebuildSQLite(t, statsPath)
	defer db.Close()
	backfillInsertTotal(t, db, "u1", "system_account", "u1", 0, 10, 0)
	if _, err := db.Exec(`DROP TABLE backfill_progress`); err != nil {
		t.Fatalf("删除 marker 表失败: %v", err)
	}

	code, _, stderr := runApplySuccessCostBackfill(t, "-apply-success-cost-backfill", "-driver", "sqlite", "-paths", pathsFlag)
	if code != 1 {
		t.Fatalf("marker 缺失退出码 = %d, want 1, stderr=%s", code, stderr)
	}
	if !strings.Contains(stderr, "--ensure-schema") || !strings.Contains(stderr, "backfill_progress") {
		t.Fatalf("错误文案缺少 --ensure-schema/backfill_progress 指引: %q", stderr)
	}
	// 不静默建表：表仍缺失，数据未被回填。
	var present int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'backfill_progress'`).Scan(&present); err != nil {
		t.Fatalf("探测 marker 表失败: %v", err)
	}
	if present != 0 {
		t.Fatal("marker 缺失报错路径不得静默建表")
	}
	if _, _, success := backfillTotalCosts(t, db, "u1"); success != 0 {
		t.Fatalf("报错路径不得回填数据: success = %v, want 0", success)
	}
}

// TestApplySuccessCostBackfillUsageErrors：用法错误与互斥全部 exit 2。
func TestApplySuccessCostBackfillUsageErrors(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"postgres without dsn", []string{"-apply-success-cost-backfill", "-driver", "postgres"}, "--dsn"},
		{"sqlite without paths", []string{"-apply-success-cost-backfill", "-driver", "sqlite"}, "--paths"},
		{"sqlite paths without stats key", []string{"-apply-success-cost-backfill", "-driver", "sqlite", "-paths", "business=/tmp/b.sqlite3"}, "stats"},
		{"sqlite with dsn", []string{"-apply-success-cost-backfill", "-driver", "sqlite", "-dsn", "postgres://s@127.0.0.1:5432/db"}, "--dsn 只适用于"},
		{"postgres with paths", []string{"-apply-success-cost-backfill", "-driver", "postgres", "-paths", "stats=/tmp/s.sqlite3"}, "--paths 只适用于"},
		{"unknown paths key", []string{"-apply-success-cost-backfill", "-driver", "sqlite", "-paths", "stat=/tmp/s.sqlite3"}, "未知 key"},
		{"mutex with ensure-schema", []string{"-apply-success-cost-backfill", "-ensure-schema"}, "mutually exclusive"},
		{"mutex with rebuild", []string{"-apply-success-cost-backfill", "-rebuild-usage-stats"}, "mutually exclusive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, stderr := runApplySuccessCostBackfill(t, tc.args...)
			if code != 2 {
				t.Fatalf("退出码 = %d, want 2, stderr=%s", code, stderr)
			}
			if !strings.Contains(stderr, tc.wantErr) {
				t.Fatalf("stderr 缺少 %q: %q", tc.wantErr, stderr)
			}
		})
	}
}

// TestApplySuccessCostBackfillTimestampShape：时间戳为 UTC 毫秒精度 RFC3339。
func TestApplySuccessCostBackfillTimestampShape(t *testing.T) {
	got := successCostBackfillTimestamp(time.Date(2026, 10, 12, 8, 9, 10, 5e8, time.UTC))
	if got != "2026-10-12T08:09:10.500Z" {
		t.Fatalf("timestamp = %s, want 2026-10-12T08:09:10.500Z", got)
	}
}

// TestApplySuccessCostBackfillPostgresStatementShapes：PG 方言的语句形态
// 契约——表名以 juhe_stats 限定、占位符为 $n 序贯绑定；SQLite 为裸表名 + ?。
func TestApplySuccessCostBackfillPostgresStatementShapes(t *testing.T) {
	pg := backfillDialect{postgres: true}
	sqlite := backfillDialect{}
	target := successCostBackfillTarget{name: "usage_stats_totals", pkColumns: []string{"system_account_id", "scope_type", "scope_id"}}

	if got := backfillMarkerReadSQL(pg); got != "SELECT status, cursor_json FROM juhe_stats.backfill_progress WHERE table_name = $1" {
		t.Fatalf("pg marker read = %s", got)
	}
	if got := backfillMarkerInsertRunningSQL(pg); got != "INSERT INTO juhe_stats.backfill_progress (table_name, status, cursor_json, updated_at) VALUES ($1, 'running', '{}', $2)" {
		t.Fatalf("pg marker insert = %s", got)
	}
	if got := backfillMarkerAdvanceSQL(pg); got != "UPDATE juhe_stats.backfill_progress SET cursor_json = $1, updated_at = $2 WHERE table_name = $3 AND status = 'running' AND cursor_json = $4" {
		t.Fatalf("pg marker advance = %s", got)
	}
	if got := backfillMarkerDoneSQL(pg); got != "UPDATE juhe_stats.backfill_progress SET status = 'done', cursor_json = '{}', updated_at = $1 WHERE table_name = $2 AND status = 'running' AND cursor_json = $3" {
		t.Fatalf("pg marker done = %s", got)
	}
	selectWithCursor := backfillBatchSelectSQL(pg, target, true)
	if selectWithCursor != "SELECT system_account_id, scope_type, scope_id FROM juhe_stats.usage_stats_totals "+
		"WHERE (system_account_id, scope_type, scope_id) > ($1, $2, $3) "+
		"ORDER BY system_account_id, scope_type, scope_id LIMIT 5000" {
		t.Fatalf("pg batch select = %s", selectWithCursor)
	}
	selectWithoutCursor := backfillBatchSelectSQL(pg, target, false)
	if strings.Contains(selectWithoutCursor, "WHERE") {
		t.Fatalf("首扫无游标不得带 WHERE 段: %s", selectWithoutCursor)
	}
	updateWithCursor := backfillBatchUpdateSQL(pg, target, true)
	if updateWithCursor != "UPDATE juhe_stats.usage_stats_totals SET success_cost_usd = total_cost_usd "+
		"WHERE (system_account_id, scope_type, scope_id) > ($1, $2, $3) "+
		"AND (system_account_id, scope_type, scope_id) <= ($4, $5, $6) "+
		"AND error_count = 0 AND success_cost_usd <> total_cost_usd" {
		t.Fatalf("pg batch update = %s", updateWithCursor)
	}
	updateWithoutCursor := backfillBatchUpdateSQL(pg, target, false)
	if !strings.Contains(updateWithoutCursor, "<= ($1, $2, $3)") {
		t.Fatalf("无游标时上界占位符应从 $1 起: %s", updateWithoutCursor)
	}
	if got := backfillBatchUpdateSQL(sqlite, target, true); strings.ContainsAny(got, "$") || !strings.Contains(got, "UPDATE usage_stats_totals SET") {
		t.Fatalf("sqlite 批语句应为裸表名 + ? 占位: %s", got)
	}
}

// ensureHintFixturePaths 建立隔离六库 --paths（stats 预先跑生产 ensure 以
// 承载 marker 预置行），返回 stats 文件路径与完整 --paths 参数值。
func ensureHintFixturePaths(t *testing.T) (statsPath, pathsFlag string) {
	t.Helper()
	dir := t.TempDir()
	statsPath = filepath.Join(dir, "stats.sqlite3")
	db := openRebuildSQLite(t, statsPath)
	defer db.Close()
	if _, err := bootstrap.EnsureSQLiteSchema(context.Background(), bootstrap.SQLiteSchemaStats, db); err != nil {
		t.Fatalf("EnsureSQLiteStats 失败: %v", err)
	}
	pathsFlag = fmt.Sprintf("business=%s,chat=%s,dataset=%s,usage-catalog=%s,stats=%s,codex-context-shard-root=%s,codex-context-shard-count=1",
		filepath.ToSlash(filepath.Join(dir, "business.sqlite3")),
		filepath.ToSlash(filepath.Join(dir, "chat.sqlite3")),
		filepath.ToSlash(filepath.Join(dir, "dataset.sqlite3")),
		filepath.ToSlash(filepath.Join(dir, "usage-catalog.sqlite3")),
		filepath.ToSlash(statsPath),
		filepath.ToSlash(filepath.Join(dir, "shards")))
	return statsPath, pathsFlag
}

// backfillPresetMarker 在 stats 库预置一行 marker（测试 helper）。
func backfillPresetMarker(t *testing.T, db *sql.DB, table, status string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO backfill_progress (table_name, status, cursor_json, updated_at)
		VALUES (?, ?, '{}', '2026-01-01T00:00:00.000Z')`, table, status); err != nil {
		t.Fatalf("预置 marker 失败: %v", err)
	}
}

// parseStorageBootstrapReport 解析 ensure 的 stdout JSON 报告（测试 helper）。
func parseStorageBootstrapReport(t *testing.T, stdout string) storageBootstrapReport {
	t.Helper()
	var report storageBootstrapReport
	if strings.TrimSpace(stdout) != "" {
		if err := json.Unmarshal([]byte(stdout), &report); err != nil {
			t.Fatalf("解析 ensure 报告失败: %v, stdout=%s", err, stdout)
		}
	}
	return report
}

// TestStorageBootstrapEnsureBackfillHintAndReport：计划 §3.2——ensure 遇到
// running/缺失 marker 时不自动迁移，仅在结果摘要输出提示并随报告携带
// pendingBackfillTables；新建库（marker 刚建表为空）与全部 done 均不提示。
func TestStorageBootstrapEnsureBackfillHintAndReport(t *testing.T) {
	t.Run("fresh stats with empty marker stays silent", func(t *testing.T) {
		_, pathsFlag := ensureHintFixturePaths(t)
		code, stdout, stderr := wmRunMaintenanceCapture(t, "-ensure-schema", "-driver", "sqlite", "-paths", pathsFlag)
		if code != 0 {
			t.Fatalf("ensure 退出码 = %d, want 0, stderr=%s", code, stderr)
		}
		if strings.Contains(stderr, "成功成本历史回填提示") {
			t.Fatalf("空 marker 不应提示: %q", stderr)
		}
		if report := parseStorageBootstrapReport(t, stdout); len(report.PendingBackfillTables) != 0 {
			t.Fatalf("空 marker 报告不应携带 pendingBackfillTables: %v", report.PendingBackfillTables)
		}
	})
	t.Run("running markers are listed in summary and report", func(t *testing.T) {
		statsPath, pathsFlag := ensureHintFixturePaths(t)
		db := openRebuildSQLite(t, statsPath)
		backfillPresetMarker(t, db, "usage_stats_totals", "running")
		backfillPresetMarker(t, db, "usage_stats_minute", "running")
		db.Close()
		code, stdout, stderr := wmRunMaintenanceCapture(t, "-ensure-schema", "-driver", "sqlite", "-paths", pathsFlag)
		if code != 0 {
			t.Fatalf("ensure 退出码 = %d, want 0, stderr=%s", code, stderr)
		}
		for _, want := range []string{"成功成本历史回填提示", "存在待回填表", "--apply-success-cost-backfill", "usage_stats_totals", "usage_stats_minute"} {
			if !strings.Contains(stderr, want) {
				t.Fatalf("stderr 缺少 %q: %q", want, stderr)
			}
		}
		report := parseStorageBootstrapReport(t, stdout)
		if want := []string{"usage_stats_minute", "usage_stats_totals"}; !reflect.DeepEqual(report.PendingBackfillTables, want) {
			t.Fatalf("pendingBackfillTables = %v, want %v（升序）", report.PendingBackfillTables, want)
		}
	})
	t.Run("done markers stay silent", func(t *testing.T) {
		statsPath, pathsFlag := ensureHintFixturePaths(t)
		db := openRebuildSQLite(t, statsPath)
		backfillPresetMarker(t, db, "usage_stats_totals", "done")
		db.Close()
		code, stdout, stderr := wmRunMaintenanceCapture(t, "-ensure-schema", "-driver", "sqlite", "-paths", pathsFlag)
		if code != 0 {
			t.Fatalf("ensure 退出码 = %d, want 0, stderr=%s", code, stderr)
		}
		if strings.Contains(stderr, "成功成本历史回填提示") {
			t.Fatalf("全部 done 不应提示: %q", stderr)
		}
		if report := parseStorageBootstrapReport(t, stdout); len(report.PendingBackfillTables) != 0 {
			t.Fatalf("全部 done 报告不应携带 pendingBackfillTables: %v", report.PendingBackfillTables)
		}
	})
	t.Run("mixed markers list only non-done", func(t *testing.T) {
		statsPath, pathsFlag := ensureHintFixturePaths(t)
		db := openRebuildSQLite(t, statsPath)
		backfillPresetMarker(t, db, "usage_stats_totals", "done")
		backfillPresetMarker(t, db, "usage_stats_minute", "running")
		db.Close()
		code, stdout, stderr := wmRunMaintenanceCapture(t, "-ensure-schema", "-driver", "sqlite", "-paths", pathsFlag)
		if code != 0 {
			t.Fatalf("ensure 退出码 = %d, want 0, stderr=%s", code, stderr)
		}
		if !strings.Contains(stderr, "usage_stats_minute") || strings.Contains(stderr, "usage_stats_totals") {
			t.Fatalf("混合 marker 应只列非 done 表: %q", stderr)
		}
		report := parseStorageBootstrapReport(t, stdout)
		if want := []string{"usage_stats_minute"}; !reflect.DeepEqual(report.PendingBackfillTables, want) {
			t.Fatalf("pendingBackfillTables = %v, want %v", report.PendingBackfillTables, want)
		}
	})
}

// TestPendingBackfillTablesThreeStates：pendingBackfillTables 的三态探测
// 契约——无表静默（present=false）、空表 present 无待办、非 done 行升序列出。
func TestPendingBackfillTablesThreeStates(t *testing.T) {
	ctx := context.Background()
	db := openRebuildSQLite(t, filepath.Join(t.TempDir(), "probe.sqlite3"))
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE unrelated (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatalf("建无关表失败: %v", err)
	}
	pending, present, err := pendingBackfillTables(ctx, db, backfillDialect{})
	if err != nil || present || pending != nil {
		t.Fatalf("无 marker 表应返回 (nil, false, nil): %v/%v/%v", pending, present, err)
	}
	if _, err := db.Exec(`CREATE TABLE backfill_progress (
		table_name TEXT PRIMARY KEY,
		status TEXT NOT NULL,
		cursor_json TEXT NOT NULL DEFAULT '{}',
		updated_at TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("建 marker 表失败: %v", err)
	}
	pending, present, err = pendingBackfillTables(ctx, db, backfillDialect{})
	if err != nil || !present || len(pending) != 0 {
		t.Fatalf("空 marker 表应返回 (空, true, nil): %v/%v/%v", pending, present, err)
	}
	backfillPresetMarker(t, db, "usage_stats_totals", "running")
	pending, present, err = pendingBackfillTables(ctx, db, backfillDialect{})
	if err != nil || !present || !reflect.DeepEqual(pending, []string{"usage_stats_totals"}) {
		t.Fatalf("running 行应被列出: %v/%v/%v", pending, present, err)
	}
}

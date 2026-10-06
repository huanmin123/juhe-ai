package main

// rebuild-usage-stats CLI 测试：门禁拒绝零副作用、SQLite 端到端重建、
// 上限未完成（退出码 3）与再次执行完成、空源放弃语义、互斥链。
// 全部进程内直调（测试单进程纪律），PG 语句形态由 jobs/statsrebuild
// 包内语句断言覆盖（本包不依赖真实 PostgreSQL）。

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/bootstrap"
	_ "modernc.org/sqlite"
)

// rebuildFixturePaths 建立隔离的 stats/business SQLite 文件并返回
// --paths 参数值。stats 库先跑生产 EnsureSQLiteStats（聚合投影表权威 DDL），
// business 库跑 EnsureSQLiteBusiness 并写入 UTC 统计时区。
func rebuildFixturePaths(t *testing.T) (dir, statsPath, businessPath, pathsFlag string) {
	t.Helper()
	dir = t.TempDir()
	statsPath = filepath.Join(dir, "stats.sqlite3")
	businessPath = filepath.Join(dir, "business.sqlite3")
	statsDB := openRebuildSQLite(t, statsPath)
	defer statsDB.Close()
	if _, err := bootstrap.EnsureSQLiteSchema(context.Background(), bootstrap.SQLiteSchemaStats, statsDB); err != nil {
		t.Fatalf("EnsureSQLiteStats 失败: %v", err)
	}
	businessDB := openRebuildSQLite(t, businessPath)
	defer businessDB.Close()
	if _, err := bootstrap.EnsureSQLiteSchema(context.Background(), bootstrap.SQLiteSchemaBusiness, businessDB); err != nil {
		t.Fatalf("EnsureSQLiteBusiness 失败: %v", err)
	}
	// sys_admin 账户行满足 system_settings 外键（生产由 seed 落库）。
	if _, err := businessDB.Exec(`INSERT INTO system_accounts (id, username, display_name, password_hash, created_at, updated_at)
		VALUES ('sys_admin', 'rebuild-fixture', 'rebuild-fixture', 'fixture-not-a-real-hash', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`); err != nil {
		t.Fatalf("写入 sys_admin 账户失败: %v", err)
	}
	if _, err := businessDB.Exec(`INSERT INTO system_settings (system_account_id, key, value_json, updated_at)
		VALUES ('sys_admin', 'usageStatsTimezone', '"UTC"', '2026-01-01T00:00:00.000Z')`); err != nil {
		t.Fatalf("写入统计时区失败: %v", err)
	}
	return dir, statsPath, businessPath, "business=" + filepath.ToSlash(businessPath) + ",stats=" + filepath.ToSlash(statsPath)
}

func openRebuildSQLite(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := bootstrap.OpenSQLiteFile(path)
	if err != nil {
		t.Fatalf("打开 %s 失败: %v", path, err)
	}
	return db
}

// seedRebuildUsageMirror 在 stats 库建立 usage_records 镜像表（46 列形状，
// 对齐 statsagg 聚合 SELECT 列集；生产由 jobs statsverify EnsureSchema 创建，
// maintenance 模块不依赖 jobs internal，测试以同形 DDL 替身）并插入记录。
func seedRebuildUsageMirror(t *testing.T, statsPath string, rows []string) *sql.DB {
	t.Helper()
	db := openRebuildSQLite(t, statsPath)
	statements := []string{`CREATE TABLE IF NOT EXISTS usage_records (
		id TEXT NOT NULL,
		system_account_id TEXT NOT NULL,
		trace_id TEXT NOT NULL,
		traffic_source TEXT NOT NULL,
		client_ip TEXT,
		api_key_id TEXT,
		group_id TEXT,
		account_id TEXT,
		endpoint TEXT,
		provider_code TEXT,
		provider_protocol_profile_id TEXT,
		model TEXT,
		status_code REAL,
		success REAL NOT NULL DEFAULT 0,
		failure_attribution TEXT,
		first_token_ms REAL,
		duration_ms REAL,
		input_tokens REAL,
		output_tokens REAL,
		cache_read_tokens REAL,
		cache_read_cost_usd REAL,
		cache_write_tokens REAL,
		cache_write_1h_tokens REAL,
		cache_write_cost_usd REAL,
		thinking_tokens REAL,
		input_image_tokens REAL,
		output_image_tokens REAL,
		input_audio_tokens REAL,
		output_audio_tokens REAL,
		tts_input_chars REAL NOT NULL DEFAULT 0,
		audio_input_seconds REAL NOT NULL DEFAULT 0,
		output_video_seconds REAL NOT NULL DEFAULT 0,
		cost_usd REAL,
		error_code TEXT,
		error_message TEXT,
		account_owner_system_account_id TEXT,
		group_owner_system_account_id TEXT,
		account_access_type TEXT,
		group_access_type TEXT,
		account_authorization_id TEXT,
		account_authorization_source_type TEXT,
		account_authorization_source_team_id TEXT,
		group_authorization_id TEXT,
		group_authorization_source_type TEXT,
		group_authorization_source_team_id TEXT,
		created_at TEXT NOT NULL,
		PRIMARY KEY (id)
	)`}
	statements = append(statements, rows...)
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("建镜像/插记录失败: %v", err)
		}
	}
	return db
}

func rebuildUsageRecordRow(id, systemAccountID string, success float64, costUsd float64, createdAt string) string {
	return `INSERT INTO usage_records (id, system_account_id, trace_id, traffic_source, success, cost_usd, created_at)
		VALUES ('` + id + `', '` + systemAccountID + `', 'trace-` + id + `', 'api', ` +
		strconv.FormatFloat(success, 'f', -1, 64) + `, ` + strconv.FormatFloat(costUsd, 'f', -1, 64) + `, '` + createdAt + `')`
}

func rebuildQueryCount(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var value int
	if err := db.QueryRow(query, args...).Scan(&value); err != nil {
		t.Fatalf("查询 %s 失败: %v", query, err)
	}
	return value
}

func TestRebuildUsageStatsGateRefusalZeroSideEffect(t *testing.T) {
	_, statsPath, _, pathsFlag := rebuildFixturePaths(t)
	statsDB := seedRebuildUsageMirror(t, statsPath, []string{
		rebuildUsageRecordRow("r1", "u1", 1, 0.5, "2026-01-02T03:04:05.000Z"),
	})
	defer statsDB.Close()
	if _, err := statsDB.Exec(`INSERT INTO usage_stats_totals
		(system_account_id, scope_type, scope_id, request_count, updated_at)
		VALUES ('poison', 'system_account', 'poison', 42, '2020-01-01T00:00:00.000Z')`); err != nil {
		t.Fatalf("插入毒化行失败: %v", err)
	}

	t.Setenv(confirmUsageStatsRebuildEnv, "")
	code, stdout, stderr := wmRunMaintenanceCapture(t, "-rebuild-usage-stats", "-driver", "sqlite", "-paths", pathsFlag)
	if code != 2 {
		t.Fatalf("无门禁退出码 = %d, want 2", code)
	}
	if stdout != "" {
		t.Fatalf("拒绝路径不应输出报告: %q", stdout)
	}
	if !strings.Contains(stderr, "统计缓存离线重建被拒绝") {
		t.Fatalf("拒绝文案缺失: %q", stderr)
	}
	// 零副作用断言：毒化行仍在、源记录仍在、游标行未产生。
	if got := rebuildQueryCount(t, statsDB, `SELECT COUNT(*) FROM usage_stats_totals WHERE system_account_id = 'poison'`); got != 1 {
		t.Fatalf("拒绝后毒化行应保留，实际 %d", got)
	}
	if got := rebuildQueryCount(t, statsDB, `SELECT COUNT(*) FROM usage_records`); got != 1 {
		t.Fatalf("拒绝后 usage_records 行数 = %d, want 1", got)
	}
	if got := rebuildQueryCount(t, statsDB, `SELECT COUNT(*) FROM stats_job_state`); got != 0 {
		t.Fatalf("拒绝后 stats_job_state 行数 = %d, want 0", got)
	}
}

func TestRebuildUsageStatsSQLiteEndToEnd(t *testing.T) {
	_, statsPath, businessPath, pathsFlag := rebuildFixturePaths(t)
	statsDB := seedRebuildUsageMirror(t, statsPath, []string{
		rebuildUsageRecordRow("r1", "u1", 1, 0.5, "2026-01-02T03:04:05.000Z"),
		rebuildUsageRecordRow("r2", "u1", 0, 0.2, "2026-01-02T03:04:05.500Z"),
		rebuildUsageRecordRow("r3", "u2", 1, 1.0, "2026-01-03T03:04:05.000Z"),
	})
	defer statsDB.Close()
	businessDB := openRebuildSQLite(t, businessPath)
	defer businessDB.Close()
	businessSettingsBefore := rebuildQueryCount(t, businessDB, `SELECT COUNT(*) FROM system_settings`)

	code, stdout, stderr := wmRunMaintenanceCapture(t,
		"-rebuild-usage-stats", "-confirm-offline", "-driver", "sqlite", "-paths", pathsFlag,
		"-batch-size", "2")
	if code != 0 {
		t.Fatalf("端到端退出码 = %d, stderr=%s", code, stderr)
	}
	var report rebuildUsageStatsReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("解析报告失败: %v, stdout=%s", err, stdout)
	}
	if !report.Completed || report.ConfirmOfflineSource != "flag" || report.Rebuild.ProcessedRows != 3 {
		t.Fatalf("报告 completed/source/processed = %v/%s/%d, want true/flag/3",
			report.Completed, report.ConfirmOfflineSource, report.Rebuild.ProcessedRows)
	}
	if len(report.Rebuild.ClearedTables) == 0 || len(report.Rebuild.WindowStages) != 9 {
		t.Fatalf("报告 clearedTables/windowStages = %d/%d", len(report.Rebuild.ClearedTables), len(report.Rebuild.WindowStages))
	}

	// 统计表重建值断言（UTC 时区）。
	if got := rebuildQueryCount(t, statsDB, `SELECT COUNT(*) FROM usage_stats_totals`); got != 3 {
		t.Fatalf("usage_stats_totals 行数 = %d, want 3", got)
	}
	var totalCost float64
	if err := statsDB.QueryRow(`SELECT total_cost_usd FROM usage_stats_totals
		WHERE system_account_id = 'u1' AND scope_type = 'system_account' AND scope_id = 'u1'`).Scan(&totalCost); err != nil {
		t.Fatalf("查询 u1 汇总失败: %v", err)
	}
	if totalCost != 0.7 {
		t.Fatalf("u1 total_cost_usd = %v, want 0.7", totalCost)
	}
	if got := rebuildQueryCount(t, statsDB, `SELECT COUNT(*) FROM usage_stats_daily WHERE stat_date = '2026-01-02'`); got < 1 {
		t.Fatalf("usage_stats_daily 缺少 2026-01-02 桶: %d", got)
	}
	// 游标推进到最后一条。
	var cursorID string
	if err := statsDB.QueryRow(`SELECT cursor_id FROM stats_job_state
		WHERE scope_type = 'global' AND scope_id = '' AND job_name = 'usage_stats_aggregation'`).Scan(&cursorID); err != nil {
		t.Fatalf("查询聚合游标失败: %v", err)
	}
	if cursorID != "r3" {
		t.Fatalf("聚合游标 = %s, want r3", cursorID)
	}
	// 业务库零写入。
	if got := rebuildQueryCount(t, businessDB, `SELECT COUNT(*) FROM system_settings`); got != businessSettingsBefore {
		t.Fatalf("业务库 system_settings 行数变化: %d -> %d", businessSettingsBefore, got)
	}
}

func TestRebuildUsageStatsMaxBatchesIncompleteExit3(t *testing.T) {
	_, statsPath, _, pathsFlag := rebuildFixturePaths(t)
	statsDB := seedRebuildUsageMirror(t, statsPath, []string{
		rebuildUsageRecordRow("r1", "u1", 1, 0.5, "2026-01-02T03:04:05.000Z"),
		rebuildUsageRecordRow("r2", "u1", 1, 0.3, "2026-01-02T04:04:05.000Z"),
		rebuildUsageRecordRow("r3", "u2", 1, 1.0, "2026-01-03T03:04:05.000Z"),
	})
	defer statsDB.Close()

	code, stdout, stderr := wmRunMaintenanceCapture(t,
		"-rebuild-usage-stats", "-confirm-offline", "-driver", "sqlite", "-paths", pathsFlag,
		"-batch-size", "2", "-max-batches", "1")
	if code != 3 {
		t.Fatalf("上限轮退出码 = %d, want 3, stderr=%s", code, stderr)
	}
	var capped rebuildUsageStatsReport
	if err := json.Unmarshal([]byte(stdout), &capped); err != nil {
		t.Fatalf("解析上限轮报告失败: %v", err)
	}
	if capped.Completed || capped.Rebuild.ProcessedRows != 2 {
		t.Fatalf("上限轮 completed/processed = %v/%d, want false/2", capped.Completed, capped.Rebuild.ProcessedRows)
	}
	if !strings.Contains(stderr, "未完成") {
		t.Fatalf("上限轮应提示未完成: %q", stderr)
	}

	// 再次执行补完（每轮重新清空并从零重放，一轮限额内完成即收敛）。
	code2, stdout2, stderr2 := wmRunMaintenanceCapture(t,
		"-rebuild-usage-stats", "-confirm-offline", "-driver", "sqlite", "-paths", pathsFlag,
		"-batch-size", "2")
	if code2 != 0 {
		t.Fatalf("续跑退出码 = %d, stderr=%s", code2, stderr2)
	}
	var completed rebuildUsageStatsReport
	if err := json.Unmarshal([]byte(stdout2), &completed); err != nil {
		t.Fatalf("解析续跑报告失败: %v", err)
	}
	if !completed.Completed || completed.Rebuild.ProcessedRows != 3 {
		t.Fatalf("续跑 completed/processed = %v/%d, want true/3", completed.Completed, completed.Rebuild.ProcessedRows)
	}
	if got := rebuildQueryCount(t, statsDB, `SELECT COUNT(*) FROM usage_stats_totals`); got != 3 {
		t.Fatalf("续跑后 usage_stats_totals 行数 = %d, want 3", got)
	}
}

func TestRebuildUsageStatsEmptySource(t *testing.T) {
	_, statsPath, _, pathsFlag := rebuildFixturePaths(t)
	statsDB := seedRebuildUsageMirror(t, statsPath, nil)
	defer statsDB.Close()

	code, stdout, stderr := wmRunMaintenanceCapture(t,
		"-rebuild-usage-stats", "-confirm-offline", "-driver", "sqlite", "-paths", pathsFlag)
	if code != 0 {
		t.Fatalf("空源退出码 = %d, want 0, stderr=%s", code, stderr)
	}
	if !strings.Contains(stderr, "放弃历史统计重建") || !strings.Contains(stderr, "后续从新请求重新累计") {
		t.Fatalf("空源提示缺失: %q", stderr)
	}
	var report rebuildUsageStatsReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("解析空源报告失败: %v", err)
	}
	if !report.Rebuild.EmptySource || !report.Completed {
		t.Fatalf("空源 emptySource/completed = %v/%v, want true/true", report.Rebuild.EmptySource, report.Completed)
	}
	if got := rebuildQueryCount(t, statsDB, `SELECT COUNT(*) FROM usage_stats_totals`); got != 0 {
		t.Fatalf("空源后统计面应保持为空: %d", got)
	}
}

func TestRebuildUsageStatsEnvGateAndMutex(t *testing.T) {
	_, statsPath, _, pathsFlag := rebuildFixturePaths(t)
	// usage_records 镜像表存在但为空（生产由 jobs statsverify 启动建表；
	// 空源路径正常完成）。
	emptyMirror := seedRebuildUsageMirror(t, statsPath, nil)
	_ = emptyMirror.Close()
	t.Run("env gate runs", func(t *testing.T) {
		t.Setenv(confirmUsageStatsRebuildEnv, "1")
		code, stdout, stderr := wmRunMaintenanceCapture(t, "-rebuild-usage-stats", "-driver", "sqlite", "-paths", pathsFlag)
		if code != 0 {
			t.Fatalf("env 门禁退出码 = %d, want 0, stderr=%s", code, stderr)
		}
		var report rebuildUsageStatsReport
		if err := json.Unmarshal([]byte(stdout), &report); err != nil {
			t.Fatalf("解析报告失败: %v", err)
		}
		if report.ConfirmOfflineSource != "env" {
			t.Fatalf("confirmOfflineSource = %s, want env", report.ConfirmOfflineSource)
		}
	})
	t.Run("mutex with seed", func(t *testing.T) {
		t.Setenv(confirmUsageStatsRebuildEnv, "1")
		code, _, stderr := wmRunMaintenanceCapture(t, "-rebuild-usage-stats", "-seed")
		if code != 2 {
			t.Fatalf("互斥退出码 = %d, want 2", code)
		}
		if !strings.Contains(stderr, "mutually exclusive") {
			t.Fatalf("互斥文案缺失: %q", stderr)
		}
	})
	t.Run("missing driver", func(t *testing.T) {
		t.Setenv(confirmUsageStatsRebuildEnv, "1")
		code, _, stderr := wmRunMaintenanceCapture(t, "-rebuild-usage-stats")
		if code != 2 {
			t.Fatalf("缺 driver 退出码 = %d, want 2", code)
		}
		if !strings.Contains(stderr, "--driver") {
			t.Fatalf("driver 提示缺失: %q", stderr)
		}
	})
	t.Run("missing paths keys", func(t *testing.T) {
		t.Setenv(confirmUsageStatsRebuildEnv, "1")
		code, _, stderr := wmRunMaintenanceCapture(t, "-rebuild-usage-stats", "-driver", "sqlite", "-paths", "stats=only")
		if code != 2 {
			t.Fatalf("缺 business 键退出码 = %d, want 2", code)
		}
		if !strings.Contains(stderr, "business") {
			t.Fatalf("business 键提示缺失: %q", stderr)
		}
	})
}

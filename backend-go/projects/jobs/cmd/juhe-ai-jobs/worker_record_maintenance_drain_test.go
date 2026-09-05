package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 组合根级测试：gateway 形状写入（手工 INSERT 模拟 internal/tablemonitor
// DurableDispatch 落行）→ jobs drain 循环一轮 → retention 执行器副作用断言
// → 交接行删除。
//
// 放置契约对照：gateway SQLite 模式把 record_maintenance_jobs 落业务库文件
// （与 api_key_record_cleanup_targets 同放置），jobs drain 复用 retention
// 家族的 business 句柄按同源读取；行内 batch_size/max_batches 取 gateway
// routes.go 的 defaultCleanupBatchSize/MaxBatches 真实形状。
//
// seed 句柄即开即关：非业务清理会删除空分片文件，Windows 上测试持有的
// 打开句柄会锁住该文件（生产无外部持锁者，测试保持同一前提）。
func seedRecordMaintenanceDrain(t *testing.T, dir string) {
	t.Helper()

	dataset := openTestSQLite(t, filepath.Join(dir, "dataset.sqlite3"))
	mustExec(t, dataset,
		"CREATE TABLE IF NOT EXISTS public_api_logs (id TEXT PRIMARY KEY, created_at TEXT)",
		`INSERT INTO public_api_logs (id, created_at) VALUES ('old', '2020-01-01T00:00:00.000Z')`,
		`INSERT INTO public_api_logs (id, created_at) VALUES ('fresh', '2100-01-01T00:00:00.000Z')`,
		"CREATE TABLE IF NOT EXISTS api_key_record_cleanup_targets (api_key_id TEXT PRIMARY KEY, system_account_id TEXT, created_at TEXT, updated_at TEXT, attempt_count INTEGER DEFAULT 0, last_attempt_at TEXT, last_blocked_reason TEXT, last_error_message TEXT)",
		"CREATE TABLE IF NOT EXISTS account_record_cleanup_targets (account_id TEXT PRIMARY KEY, system_account_id TEXT, related_account_ids_json TEXT DEFAULT '[]', authorization_ids_json TEXT DEFAULT '[]', team_scope_ids_json TEXT DEFAULT '[]', created_at TEXT, updated_at TEXT, attempt_count INTEGER DEFAULT 0, last_attempt_at TEXT, last_blocked_reason TEXT, last_error_message TEXT)")
	_ = dataset.Close()

	stats := openTestSQLite(t, filepath.Join(dir, "stats.sqlite3"))
	statsCleanupTables(t, stats)
	// 使用记录清理的安全游标（global + usage_shard 两个必需 job）。
	mustExec(t, stats,
		`INSERT INTO stats_job_state (cursor_created_at, cursor_id, scope_type, job_name) VALUES ('2099-01-01T00:00:00.000Z', 'usage-x', 'global', 'usage_stats_aggregation')`,
		`INSERT INTO stats_job_state (cursor_created_at, cursor_id, scope_type, scope_id, job_name) VALUES ('2099-01-01T00:00:00.000Z', 'usage-x', 'usage_shard', '20200101:s01', 'usage_stats_aggregation')`,
		`INSERT INTO stats_job_state (cursor_created_at, cursor_id, scope_type, scope_id, job_name) VALUES ('2099-01-01T00:00:00.000Z', 'usage-x', 'usage_shard', '20200101:s01', 'client_ip_stats_aggregation')`)
	_ = stats.Close()

	catalog := openTestSQLite(t, filepath.Join(dir, "usage-catalog.sqlite3"))
	shardPath := filepath.Join(dir, "usage-shards", "shard.sqlite3")
	mustExec(t, catalog,
		`CREATE TABLE IF NOT EXISTS usage_record_shards (shard_key TEXT PRIMARY KEY, bucket_date TEXT, shard_id INTEGER, file_path TEXT, status TEXT)`,
		`CREATE TABLE IF NOT EXISTS usage_record_shard_entries (usage_id TEXT, shard_key TEXT, created_at TEXT, system_account_id TEXT, api_key_id TEXT, account_id TEXT)`,
		`CREATE TABLE IF NOT EXISTS usage_record_account_shards (account_id TEXT, shard_key TEXT, first_created_at TEXT, last_seen_at TEXT)`,
		`CREATE TABLE IF NOT EXISTS usage_record_api_key_shards (api_key_id TEXT, system_account_id TEXT, shard_key TEXT, first_created_at TEXT, last_seen_at TEXT)`,
		"INSERT INTO usage_record_shards (shard_key, bucket_date, shard_id, file_path, status) VALUES ('20200101:s01', '2020-01-01', 1, '"+shardPath+"', 'active')",
		`INSERT INTO usage_record_shard_entries (usage_id, shard_key, created_at, system_account_id)
		 VALUES ('usage-old', '20200101:s01', '2020-01-01T00:00:00.000Z', 'sys_a')`)
	_ = catalog.Close()

	if err := os.MkdirAll(filepath.Dir(shardPath), 0o755); err != nil {
		t.Fatal(err)
	}
	shard := openTestSQLite(t, shardPath)
	mustExec(t, shard, `CREATE TABLE IF NOT EXISTS usage_records (
		id TEXT PRIMARY KEY, system_account_id TEXT, trace_id TEXT DEFAULT '', traffic_source TEXT DEFAULT '',
		client_ip TEXT, api_key_id TEXT, group_id TEXT, account_id TEXT, endpoint TEXT, provider_code TEXT,
		provider_protocol_profile_id TEXT, model TEXT, status_code INTEGER, success INTEGER DEFAULT 1,
		failure_attribution TEXT, first_token_ms INTEGER, duration_ms INTEGER, input_tokens INTEGER DEFAULT 0,
		output_tokens INTEGER DEFAULT 0, cache_read_tokens INTEGER DEFAULT 0, cache_read_cost_usd REAL DEFAULT 0,
		cache_write_tokens INTEGER DEFAULT 0, cache_write_1h_tokens INTEGER DEFAULT 0, cache_write_cost_usd REAL DEFAULT 0,
		thinking_tokens INTEGER DEFAULT 0, input_image_tokens INTEGER DEFAULT 0, output_image_tokens INTEGER DEFAULT 0,
		cost_usd REAL DEFAULT 0, error_code TEXT, error_message TEXT,
		account_owner_system_account_id TEXT, group_owner_system_account_id TEXT, account_access_type TEXT,
		group_access_type TEXT, account_authorization_id TEXT, account_authorization_source_type TEXT,
		account_authorization_source_team_id TEXT, group_authorization_id TEXT, group_authorization_source_type TEXT,
		group_authorization_source_team_id TEXT, created_at TEXT)`,
		`INSERT INTO usage_records (id, system_account_id, api_key_id, account_id, success, created_at)
		 VALUES ('usage-old', 'sys_a', 'key1', 'acc-1', 1, '2020-01-01T00:00:00.000Z')`)
	_ = shard.Close()
}

func drainTestAssembly(t *testing.T, dir string) {
	t.Helper()
	assembly, err := buildWorkerAssembly(retentionTestConfig(dir), slog.Default())
	if err != nil {
		t.Fatalf("build worker assembly: %v", err)
	}
	t.Cleanup(assembly.closeStores)
}

func seedHandoffRow(t *testing.T, dir, id string) {
	t.Helper()
	business := openTestSQLite(t, filepath.Join(dir, "business.sqlite3"))
	mustExec(t, business,
		`CREATE TABLE IF NOT EXISTS record_maintenance_jobs (
			id TEXT PRIMARY KEY,
			type TEXT NOT NULL,
			cutoff_at TEXT NOT NULL,
			batch_size INTEGER NOT NULL,
			max_batches INTEGER NOT NULL,
			created_at TEXT NOT NULL)`,
		`INSERT INTO record_maintenance_jobs (id, type, cutoff_at, batch_size, max_batches, created_at)
		 VALUES ('`+id+`', 'non_business_data_cleanup', '2020-06-01T00:00:00.000Z', 5000, 100, '2026-09-04T00:00:00.000Z')`)
}

func handoffRowCount(t *testing.T, dir string) int64 {
	t.Helper()
	business := openTestSQLite(t, filepath.Join(dir, "business.sqlite3"))
	var count int64
	_ = business.QueryRow(`SELECT COUNT(*) FROM record_maintenance_jobs`).Scan(&count)
	return count
}

func TestWorkerRecordMaintenanceTableDrainRound(t *testing.T) {
	dir := t.TempDir()
	seedRecordMaintenanceDrain(t, dir)
	seedHandoffRow(t, dir, "recmaint_1757000000000_abcdef01")
	drainTestAssembly(t, dir)

	// drain 循环（100ms 节拍）消费：以交接表清空为完成信号（单句柄轮询）。
	business := openTestSQLite(t, filepath.Join(dir, "business.sqlite3"))
	deadline := time.Now().Add(15 * time.Second)
	for {
		var pending int64
		_ = business.QueryRow(`SELECT COUNT(*) FROM record_maintenance_jobs`).Scan(&pending)
		if pending == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("record_maintenance_jobs 未被 drain：%d", pending)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// 执行器副作用断言：non_business_data_cleanup 的 dataset 半区清掉过期
	// public_api_logs、使用记录半区清掉分片 usage_records（安全游标已由
	// seed 建立），未过期行保留。
	dataset := openTestSQLite(t, filepath.Join(dir, "dataset.sqlite3"))
	var oldLogs int64
	_ = dataset.QueryRow(`SELECT COUNT(*) FROM public_api_logs WHERE id = 'old'`).Scan(&oldLogs)
	if oldLogs != 0 {
		t.Fatal("过期 public_api_logs 未被非业务清理删除")
	}
	var freshLogs int64
	_ = dataset.QueryRow(`SELECT COUNT(*) FROM public_api_logs WHERE id = 'fresh'`).Scan(&freshLogs)
	if freshLogs != 1 {
		t.Fatal("未过期 public_api_logs 被误删")
	}
	shard := openTestSQLite(t, filepath.Join(dir, "usage-shards", "shard.sqlite3"))
	var shardRows int64
	_ = shard.QueryRow(`SELECT COUNT(*) FROM usage_records`).Scan(&shardRows)
	if shardRows != 0 {
		t.Fatalf("过期分片 usage_records 未被非业务清理删除：%d", shardRows)
	}
}

// drain 失败保留语义的组合级对照：执行器不可用（dataset 半区依赖表缺失）
// 时 RunOnce 报错，交接行保留等待重试（Node 失败保留语义）。
func TestWorkerRecordMaintenanceTableDrainRetainsRowOnFailure(t *testing.T) {
	dir := t.TempDir()
	seedRecordMaintenanceDrain(t, dir)
	// 移除非业务清理 dataset 半区会扫描的目标表，制造执行器故障。
	dataset := openTestSQLite(t, filepath.Join(dir, "dataset.sqlite3"))
	mustExec(t, dataset, "DROP TABLE api_key_record_cleanup_targets")
	_ = dataset.Close()

	seedHandoffRow(t, dir, "recmaint_1757000000000_deadbeef")
	drainTestAssembly(t, dir)

	// 给 drain 循环足够轮次（含 1s 失败退避）后，行必须仍在（批内已执行的
	// 前置子步骤允许有部分副作用，与 Node flush 一致）。
	time.Sleep(1500 * time.Millisecond)
	if count := handoffRowCount(t, dir); count != 1 {
		t.Fatalf("执行失败的交接行必须保留等待重试，实际 %d", count)
	}
}

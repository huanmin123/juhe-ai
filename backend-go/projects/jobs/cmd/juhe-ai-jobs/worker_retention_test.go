package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 组合根级测试：seed → 跑一轮 → 断言删了该删的/留了该留的。
// 覆盖四个 GoWired 任务：data-retention-cleanup、chat-retention-cleanup、
// expired-deleted-account-cleanup、api-key/account-record-cleanup-retry。

func retentionTestConfig(dir string) workerConfig {
	return workerConfig{
		Enabled:                        true,
		Driver:                         "sqlite",
		InstanceID:                     "retention-test",
		WorkerRole:                     "ingest-worker",
		PostgresMaxOpenConns:           2,
		PostgresMaxIdleConns:           2,
		UsageShardCount:                2,
		BusinessSQLitePath:             filepath.Join(dir, "business.sqlite3"),
		StatsSQLitePath:                filepath.Join(dir, "stats.sqlite3"),
		DatasetSQLitePath:              filepath.Join(dir, "dataset.sqlite3"),
		ChatSQLitePath:                 filepath.Join(dir, "chat.sqlite3"),
		UsageCatalogSQLitePath:         filepath.Join(dir, "usage-catalog.sqlite3"),
		UsageShardRoot:                 filepath.Join(dir, "usage-shards"),
		CodexContextStateShardRoot:     filepath.Join(dir, "codex-state"),
		CodexContextStateShardCount:    1,
		ChatAssetsRoot:                 filepath.Join(dir, "chat-assets"),
		CodexContextRoot:               filepath.Join(dir, "codex-context"),
		ChatRetentionDays:              3,
		RetentionEnabled:               true,
		StatsEnabled:                   false,
		OAuthEnabled:                   false,
		TaskRunsEnabled:                false,
		UsageWriterEnabled:             false,
		InternalAPIEnabled:             false,
		BalanceDetectEnabled:           false,
		DrainTimeout:                   time.Second,
		RecordMaintenanceQueueMaxItems: 100,
		RecordMaintenanceQueueMaxMb:    1,
	}
}

func mustExec(t *testing.T, db *sql.DB, statements ...string) {
	t.Helper()
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("exec %q: %v", strings.Join(strings.Fields(statement), " "), err)
		}
	}
}

func openTestSQLite(t *testing.T, path string) *sql.DB {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// statsCleanupTables 覆盖 CleanupUsageStatsRetention/CleanupSystemMetricsRetention
// 会 DELETE 的全部表（测试只建时间列）。
func statsCleanupTables(t *testing.T, db *sql.DB) {
	t.Helper()
	type tableColumn struct{ table, column string }
	tables := []tableColumn{
		{"account_quality_minute_stats", "stat_minute"},
		{"group_account_stats", "updated_at"},
		{"account_quality_scores", "updated_at"},
	{"account_quality_scores_acct", ""},
		{"account_quality_dirty_accounts", "updated_at"},
		{"account_usage_snapshots", "updated_at"},
		{"usage_stats_totals", "updated_at"},
		{"usage_stats_minute", "stat_minute"},
		{"usage_stats_hourly", "stat_hour"},
		{"usage_stats_daily", "stat_date"},
		{"usage_stats_weekly", "stat_week"},
		{"usage_stats_monthly", "stat_month"},
		{"authorization_team_usage_summary_daily", "stat_date"},
		{"authorization_team_usage_range_windows", "end_date"},
		{"authorization_user_usage_summary_daily", "stat_date"},
		{"authorization_user_usage_range_windows", "end_date"},
		{"usage_model_minute", "stat_minute"},
		{"usage_model_hourly", "stat_hour"},
		{"usage_model_daily", "stat_date"},
		{"usage_model_weekly", "stat_week"},
		{"usage_model_monthly", "stat_month"},
		{"usage_error_minute", "stat_minute"},
		{"usage_error_hourly", "stat_hour"},
		{"usage_error_daily", "stat_date"},
		{"usage_error_weekly", "stat_week"},
		{"usage_error_monthly", "stat_month"},
		{"usage_latency_minute", "stat_minute"},
		{"usage_latency_hourly", "stat_hour"},
		{"usage_latency_daily", "stat_date"},
		{"usage_latency_weekly", "stat_week"},
		{"usage_latency_monthly", "stat_month"},
		{"usage_rank_snapshots", "snapshot_at"},
		{"usage_overview_summary_windows", "end_date"},
		{"usage_overview_trend_windows", "end_date"},
		{"usage_model_rank_windows", "end_date"},
		{"usage_error_rank_windows", "end_date"},
		{"ai_performance_summary_windows", "end_date"},
		{"usage_quota_hourly_windows", "updated_at"},
		{"usage_scope_range_windows", "end_date"},
		{"usage_range_window_requests", "expires_at"},
		{"client_ip_registry", "last_seen_at"},
		{"client_ip_stats_daily", "stat_date"},
		{"client_ip_usage_range_windows", "end_date"},
		{"client_ip_range_window_dirty_ips", "updated_at"},
		{"client_ip_policy_hits", "stat_date"},
		{"client_ip_account_stats_daily", "stat_date"},
		{"client_ip_account_usage_range_windows", "end_date"},
		{"client_ip_account_range_window_dirty_ips", "updated_at"},
		{"usage_record_cleanup_deductions", "updated_at"},
		{"system_metrics_samples", "sampled_at"},
		{"system_metrics_hourly", "stat_hour"},
		{"system_metrics_trend_windows", "end_date"},
		{"process_event_loop_samples", "sampled_at"},
		{"process_event_loop_hourly", "stat_hour"},
		{"process_event_loop_trend_windows", "end_date"},
		{"account_health_hourly", "stat_hour"},
		{"stats_job_state", "cursor_created_at"},
	}
	statements := make([]string, 0, len(tables)+10)
	for _, item := range tables {
		statements = append(statements, fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (%s TEXT)", item.table, item.column))
	}
	// accountScopeStatsTables 需要完整的 scope 列与各自时间列（物理清理
	// 相关记录探测与 stats 保留清理按它们查询）。
	timeColumns := map[string]string{
		"usage_stats_totals": "", "usage_stats_minute": ", stat_minute TEXT", "usage_stats_hourly": ", stat_hour TEXT",
		"usage_stats_daily": ", stat_date TEXT", "usage_stats_weekly": ", stat_week TEXT", "usage_stats_monthly": ", stat_month TEXT",
		"usage_latency_minute": ", stat_minute TEXT", "usage_latency_hourly": ", stat_hour TEXT", "usage_latency_daily": ", stat_date TEXT",
		"usage_latency_weekly": ", stat_week TEXT", "usage_latency_monthly": ", stat_month TEXT", "usage_rank_snapshots": ", snapshot_at TEXT",
		"usage_quota_hourly_windows": "", "usage_scope_range_windows": "",
	}
	for _, tableName := range []string{
		"usage_stats_totals", "usage_stats_minute", "usage_stats_hourly", "usage_stats_daily",
		"usage_stats_weekly", "usage_stats_monthly", "usage_latency_minute", "usage_latency_hourly",
		"usage_latency_daily", "usage_latency_weekly", "usage_latency_monthly", "usage_rank_snapshots",
		"usage_quota_hourly_windows", "usage_scope_range_windows",
	} {
		statements = append(statements, "DROP TABLE IF EXISTS "+tableName)
		statements = append(statements, fmt.Sprintf("CREATE TABLE %s (system_account_id TEXT, scope_type TEXT, scope_id TEXT, account_id TEXT, resource_filter_type TEXT, resource_filter_id TEXT, team_filter_id TEXT, grantee_filter_system_account_id TEXT, updated_at TEXT%s)", tableName, timeColumns[tableName]))
	}
	// hasDeletedAccountStatsRowsSQLite 按 account_id 探测这些表，测试建表带
	// account_id 列（含 usage_record_cleanup_deductions 的扣减台账列）。
	statements = append(statements,
		"DROP TABLE IF EXISTS account_quality_minute_stats",
		"CREATE TABLE account_quality_minute_stats (account_id TEXT, system_account_id TEXT DEFAULT '', provider_code TEXT DEFAULT '', stat_minute TEXT, request_count INTEGER DEFAULT 0, success_count INTEGER DEFAULT 0, error_count INTEGER DEFAULT 0, first_token_ms_sum REAL DEFAULT 0, first_token_ms_count INTEGER DEFAULT 0, last_sample_at TEXT, last_success_at TEXT, last_error_at TEXT, last_error_message TEXT, updated_at TEXT, PRIMARY KEY (account_id, stat_minute))",
		"DROP TABLE IF EXISTS account_quality_scores",
		"CREATE TABLE account_quality_scores (account_id TEXT, updated_at TEXT)",
		"DROP TABLE IF EXISTS account_quality_dirty_accounts",
		"CREATE TABLE account_quality_dirty_accounts (account_id TEXT, first_dirty_at TEXT, updated_at TEXT)",
		"DROP TABLE IF EXISTS account_usage_snapshots",
		"CREATE TABLE account_usage_snapshots (account_id TEXT, updated_at TEXT)",
		"DROP TABLE IF EXISTS account_health_hourly",
		"CREATE TABLE account_health_hourly (account_id TEXT, stat_hour TEXT, last_record_id TEXT, updated_at TEXT)",
		"DROP TABLE IF EXISTS authorization_team_usage_summary_daily",
		"CREATE TABLE authorization_team_usage_summary_daily (system_account_id TEXT, stat_date TEXT, resource_filter_type TEXT, resource_filter_id TEXT, team_filter_id TEXT, grantee_filter_system_account_id TEXT, updated_at TEXT)",
		"DROP TABLE IF EXISTS authorization_team_usage_range_windows",
		"CREATE TABLE authorization_team_usage_range_windows (resource_filter_type TEXT, resource_filter_id TEXT, updated_at TEXT)",
		"DROP TABLE IF EXISTS authorization_user_usage_summary_daily",
		"CREATE TABLE authorization_user_usage_summary_daily (system_account_id TEXT, stat_date TEXT, resource_filter_type TEXT, resource_filter_id TEXT, team_filter_id TEXT, grantee_filter_system_account_id TEXT, updated_at TEXT)",
		"DROP TABLE IF EXISTS authorization_user_usage_range_windows",
		"CREATE TABLE authorization_user_usage_range_windows (resource_filter_type TEXT, resource_filter_id TEXT, updated_at TEXT)",
		"DROP TABLE IF EXISTS usage_record_cleanup_deductions",
		`CREATE TABLE usage_record_cleanup_deductions (
			usage_id TEXT, api_key_id TEXT, account_id TEXT, system_account_id TEXT, source_shard_key TEXT,
			record_json TEXT, stats_subtracted_at TEXT, shard_deleted_at TEXT, created_at TEXT, updated_at TEXT,
			PRIMARY KEY (usage_id, source_shard_key))`,
		"ALTER TABLE stats_job_state ADD COLUMN cursor_id TEXT",
		"ALTER TABLE stats_job_state ADD COLUMN job_name TEXT",
		"ALTER TABLE stats_job_state ADD COLUMN scope_type TEXT",
		"ALTER TABLE stats_job_state ADD COLUMN scope_id TEXT")
	mustExec(t, db, statements...)
}

func seedDataRetention(t *testing.T, dir string) {
	business := openTestSQLite(t, filepath.Join(dir, "business.sqlite3"))
	mustExec(t, business, "CREATE TABLE IF NOT EXISTS system_sessions (id TEXT PRIMARY KEY, expires_at TEXT)")
	mustExec(t, business,
		`INSERT INTO system_sessions (id, expires_at) VALUES ('old', '2020-01-01T00:00:00.000Z')`,
		`INSERT INTO system_sessions (id, expires_at) VALUES ('fresh', '2100-01-01T00:00:00.000Z')`)

	dataset := openTestSQLite(t, filepath.Join(dir, "dataset.sqlite3"))
	mustExec(t, dataset,
		"CREATE TABLE IF NOT EXISTS public_api_logs (id TEXT PRIMARY KEY, created_at TEXT)",
		`INSERT INTO public_api_logs (id, created_at) VALUES ('old', '2020-01-01T00:00:00.000Z')`,
		`INSERT INTO public_api_logs (id, created_at) VALUES ('fresh', '2100-01-01T00:00:00.000Z')`)

	stats := openTestSQLite(t, filepath.Join(dir, "stats.sqlite3"))
	statsCleanupTables(t, stats)
	mustExec(t, stats,
		`INSERT INTO usage_stats_minute (stat_minute) VALUES ('2020-01-01T00:00')`,
		`INSERT INTO system_metrics_samples (sampled_at) VALUES ('2020-01-01T00:00:00.000Z')`)

	// usage catalog + 一个 active 分片（目录条目早于安全游标 → 清理链路放行）。
	catalog := openTestSQLite(t, filepath.Join(dir, "usage-catalog.sqlite3"))
	mustExec(t, catalog,
		`CREATE TABLE IF NOT EXISTS usage_record_shards (shard_key TEXT PRIMARY KEY, bucket_date TEXT, shard_id INTEGER, file_path TEXT, status TEXT)`,
		`CREATE TABLE IF NOT EXISTS usage_record_shard_entries (usage_id TEXT, shard_key TEXT, created_at TEXT, system_account_id TEXT, api_key_id TEXT, account_id TEXT)`,
		`CREATE TABLE IF NOT EXISTS usage_record_account_shards (account_id TEXT, shard_key TEXT, first_created_at TEXT, last_seen_at TEXT)`,
		`CREATE TABLE IF NOT EXISTS usage_record_api_key_shards (api_key_id TEXT, system_account_id TEXT, shard_key TEXT, first_created_at TEXT, last_seen_at TEXT)`,
		"INSERT INTO usage_record_shards (shard_key, bucket_date, shard_id, file_path, status) VALUES ('20200101:s01', '2020-01-01', 1, '" + filepath.Join(dir, "usage-shards", "shard.sqlite3") + "', 'active')",
		`INSERT INTO usage_record_shard_entries (usage_id, shard_key, created_at, system_account_id)
		 VALUES ('usage-old', '20200101:s01', '2020-01-01T00:00:00.000Z', 'sys_a')`)
	// 安全游标：两个必需 job 都建立 global 游标（PG 语义）与 shard 游标（SQLite 语义）。
	mustExec(t, stats,
		`INSERT INTO stats_job_state (cursor_created_at, cursor_id, scope_type, job_name) VALUES ('2099-01-01T00:00:00.000Z', 'usage-x', 'global', 'usage_stats_aggregation')`,
		`INSERT INTO stats_job_state (cursor_created_at, cursor_id, scope_type, scope_id, job_name) VALUES ('2099-01-01T00:00:00.000Z', 'usage-x', 'usage_shard', '20200101:s01', 'usage_stats_aggregation')`,
		`INSERT INTO stats_job_state (cursor_created_at, cursor_id, scope_type, scope_id, job_name) VALUES ('2099-01-01T00:00:00.000Z', 'usage-x', 'usage_shard', '20200101:s01', 'client_ip_stats_aggregation')`)
	shardPath := filepath.Join(dir, "usage-shards", "shard.sqlite3")
	if err := os.MkdirAll(filepath.Dir(shardPath), 0o755); err != nil {
		t.Fatal(err)
	}
	shard := openTestSQLite(t, shardPath)
	mustExec(t, shard, "CREATE TABLE IF NOT EXISTS usage_records (id TEXT PRIMARY KEY, created_at TEXT)",
		`INSERT INTO usage_records (id, created_at) VALUES ('usage-old', '2020-01-01T00:00:00.000Z')`)

	codexDir := filepath.Join(dir, "codex-state")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	codex := openTestSQLite(t, filepath.Join(codexDir, "state-000.sqlite3"))
	mustExec(t, codex,
		"CREATE TABLE IF NOT EXISTS codex_context_sessions (id TEXT PRIMARY KEY, expires_at TEXT, updated_at TEXT)",
		"CREATE TABLE IF NOT EXISTS codex_context_responses (id TEXT PRIMARY KEY, session_id TEXT, expires_at TEXT, storage_key TEXT)",
		"CREATE TABLE IF NOT EXISTS codex_context_compacts (id TEXT PRIMARY KEY, session_id TEXT, expires_at TEXT, storage_key TEXT)",
		"CREATE TABLE IF NOT EXISTS codex_context_storage_cleanup_queue (storage_key TEXT PRIMARY KEY, enqueued_at TEXT, updated_at TEXT, next_attempt_at TEXT, attempt_count INTEGER DEFAULT 0, last_error TEXT)",
		`INSERT INTO codex_context_sessions (id, expires_at) VALUES ('s-old', '2020-01-01T00:00:00.000Z')`,
		`INSERT INTO codex_context_responses (id, session_id, expires_at, storage_key) VALUES ('r-old', 's-old', '2020-01-01T00:00:00.000Z', 'resp/old.bin')`)
}

func TestWorkerRetentionDataRetentionRound(t *testing.T) {
	dir := t.TempDir()
	seedDataRetention(t, dir)
	assembly, err := buildWorkerAssembly(retentionTestConfig(dir), slog.Default())
	if err != nil {
		t.Fatalf("build worker assembly: %v", err)
	}
	defer assembly.closeStores()

	if _, err := assembly.runWiredJobOnce(context.Background(), "data-retention-cleanup"); err != nil {
		t.Fatalf("data-retention-cleanup round: %v", err)
	}

	business := openTestSQLite(t, filepath.Join(dir, "business.sqlite3"))
	var expiredSessions int64
	_ = business.QueryRow(`SELECT COUNT(*) FROM system_sessions WHERE expires_at < '2025-01-01T00:00:00.000Z'`).Scan(&expiredSessions)
	if expiredSessions != 0 {
		t.Fatalf("过期 system_sessions 未清理：%d", expiredSessions)
	}
	var freshSessions int64
	_ = business.QueryRow(`SELECT COUNT(*) FROM system_sessions WHERE id = 'fresh'`).Scan(&freshSessions)
	if freshSessions != 1 {
		t.Fatal("未过期 system_sessions 被误删")
	}

	dataset := openTestSQLite(t, filepath.Join(dir, "dataset.sqlite3"))
	var oldLogs int64
	_ = dataset.QueryRow(`SELECT COUNT(*) FROM public_api_logs WHERE id = 'old'`).Scan(&oldLogs)
	if oldLogs != 0 {
		t.Fatal("过期 public_api_logs 未清理")
	}

	shard := openTestSQLite(t, filepath.Join(dir, "usage-shards", "shard.sqlite3"))
	var shardRows int64
	_ = shard.QueryRow(`SELECT COUNT(*) FROM usage_records`).Scan(&shardRows)
	if shardRows != 0 {
		t.Fatal("过期分片 usage_records 未清理")
	}
	catalog := openTestSQLite(t, filepath.Join(dir, "usage-catalog.sqlite3"))
	var entries int64
	_ = catalog.QueryRow(`SELECT COUNT(*) FROM usage_record_shard_entries`).Scan(&entries)
	if entries != 0 {
		t.Fatal("分片目录条目未清理")
	}
}

func seedChatRetention(t *testing.T, dir string) {
	chat := openTestSQLite(t, filepath.Join(dir, "chat.sqlite3"))
	mustExec(t, chat,
		`CREATE TABLE IF NOT EXISTS chat_conversations (
			id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, active_turn_id TEXT, active_started_at TEXT,
			message_revision INTEGER NOT NULL DEFAULT 0, updated_at TEXT, title TEXT, title_source_message_id TEXT,
			created_at TEXT, context_state TEXT, context_claimed_at TEXT, context_revision INTEGER NOT NULL DEFAULT 0,
			active_checkpoint_id TEXT, compacted_through_sequence INTEGER NOT NULL DEFAULT 0,
			active_context_tokens INTEGER, effective_context_limit_tokens INTEGER, context_usage_estimated INTEGER,
			context_retry_at TEXT, context_error_code TEXT, context_progress_sequence INTEGER NOT NULL DEFAULT 0,
			context_progress_earliest_expires_at TEXT, context_claim_id TEXT, context_claim_revision INTEGER,
			context_claim_through_sequence INTEGER)`,
		`CREATE TABLE IF NOT EXISTS chat_messages (
			id TEXT PRIMARY KEY, conversation_id TEXT, system_account_id TEXT, turn_id TEXT, role TEXT, status TEXT,
			sequence_no INTEGER, expires_at TEXT, created_at TEXT, content_bytes INTEGER DEFAULT 0,
			storage_reserved_bytes INTEGER DEFAULT 0, completed_at TEXT, error_code TEXT, error_message TEXT, content_text TEXT)`,
		`CREATE TABLE IF NOT EXISTS chat_message_idempotency (conversation_id TEXT, turn_id TEXT, expires_at TEXT, user_message_id TEXT, assistant_message_id TEXT, client_message_id TEXT, system_account_id TEXT)`,
		`CREATE TABLE IF NOT EXISTS chat_user_storage_windows (system_account_id TEXT, bucket_date TEXT, content_bytes INTEGER DEFAULT 0, reserved_bytes INTEGER DEFAULT 0, updated_at TEXT)`,
		`CREATE TABLE IF NOT EXISTS chat_context_checkpoints (id TEXT PRIMARY KEY, conversation_id TEXT, system_account_id TEXT, status TEXT, expires_at TEXT)`,
		`CREATE TABLE IF NOT EXISTS chat_assets (
			id TEXT PRIMARY KEY, system_account_id TEXT, conversation_id TEXT, expires_at TEXT, cleanup_status TEXT,
			cleanup_claim_id TEXT, cleanup_claimed_at TEXT, cleanup_attempt_count INTEGER DEFAULT 0, cleanup_retry_at TEXT,
			cleanup_error_code TEXT, storage_key TEXT, preview_storage_key TEXT, quota_bytes INTEGER DEFAULT 0, updated_at TEXT, created_at TEXT)`,
		`CREATE TABLE IF NOT EXISTS chat_user_asset_usage (system_account_id TEXT, asset_bytes INTEGER DEFAULT 0, asset_count INTEGER DEFAULT 0, updated_at TEXT)`,
		// 过期轮次（user+assistant）+ 幂等键 + 空会话 + 过期检查点 + 过期资产
		`INSERT INTO chat_conversations (id, system_account_id, title, created_at, updated_at, active_checkpoint_id, context_state) VALUES ('conv', 'sys_a', 't', '2020-01-01T00:00:00.000Z', '2020-01-01T00:00:00.000Z', 'cp1', 'ready')`,
		`INSERT INTO chat_messages (id, conversation_id, system_account_id, turn_id, role, status, sequence_no, expires_at, created_at, content_bytes)
		 VALUES ('m3', 'conv', 'sys_a', 'turn2', 'user', 'completed', 3, '2100-01-01T00:00:00.000Z', '2020-01-01T00:00:00.000Z', 5)`,
		`INSERT INTO chat_messages (id, conversation_id, system_account_id, turn_id, role, status, sequence_no, expires_at, created_at, content_bytes)
		 VALUES ('m4', 'conv', 'sys_a', 'turn2', 'assistant', 'completed', 4, '2100-01-01T00:00:00.000Z', '2020-01-01T00:00:00.000Z', 5)`,
		`INSERT INTO chat_messages (id, conversation_id, system_account_id, turn_id, role, status, sequence_no, expires_at, created_at, content_bytes)
		 VALUES ('m1', 'conv', 'sys_a', 'turn1', 'user', 'completed', 1, '2020-01-01T00:00:00.000Z', '2020-01-01T00:00:00.000Z', 10)`,
		`INSERT INTO chat_messages (id, conversation_id, system_account_id, turn_id, role, status, sequence_no, expires_at, created_at, content_bytes, content_text)
		 VALUES ('m2', 'conv', 'sys_a', 'turn1', 'assistant', 'completed', 2, '2020-01-01T00:00:00.000Z', '2020-01-01T00:00:00.000Z', 20, '你好')`,
		`INSERT INTO chat_message_idempotency (conversation_id, turn_id, expires_at) VALUES ('conv', 'turn1', '2020-01-01T00:00:00.000Z')`,
		`INSERT INTO chat_user_storage_windows (system_account_id, bucket_date, content_bytes, reserved_bytes) VALUES ('sys_a', '2020-01-01', 30, 0)`,
		`INSERT INTO chat_context_checkpoints (id, conversation_id, system_account_id, status, expires_at) VALUES ('cp1', 'conv', 'sys_a', 'active', '2020-01-01T00:00:00.000Z')`,
		`INSERT INTO chat_assets (id, system_account_id, conversation_id, expires_at, cleanup_status, storage_key, quota_bytes, created_at)
		 VALUES ('asset1', 'sys_a', 'conv', '2020-01-01T00:00:00.000Z', 'active', 'img/a.png', 100, '2020-01-01T00:00:00.000Z')`,
		`INSERT INTO chat_user_asset_usage (system_account_id, asset_bytes, asset_count) VALUES ('sys_a', 100, 1)`)
	if err := os.MkdirAll(filepath.Join(dir, "chat-assets", "img"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "chat-assets", "img", "a.png"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerRetentionChatRetentionRound(t *testing.T) {
	dir := t.TempDir()
	seedChatRetention(t, dir)
	assembly, err := buildWorkerAssembly(retentionTestConfig(dir), slog.Default())
	if err != nil {
		t.Fatalf("build worker assembly: %v", err)
	}
	defer assembly.closeStores()

	if _, err := assembly.runWiredJobOnce(context.Background(), "chat-retention-cleanup"); err != nil {
		t.Fatalf("chat-retention-cleanup round: %v", err)
	}

	chat := openTestSQLite(t, filepath.Join(dir, "chat.sqlite3"))
	var expiredMessages int64
	_ = chat.QueryRow(`SELECT COUNT(*) FROM chat_messages WHERE expires_at <= '2025-01-01T00:00:00.000Z'`).Scan(&expiredMessages)
	if expiredMessages != 0 {
		t.Fatalf("过期轮次消息未清理：%d", expiredMessages)
	}
	var liveMessages int64
	_ = chat.QueryRow(`SELECT COUNT(*) FROM chat_messages`).Scan(&liveMessages)
	if liveMessages != 2 {
		t.Fatalf("未过期消息数量异常：%d", liveMessages)
	}
	var idempotency int64
	_ = chat.QueryRow(`SELECT COUNT(*) FROM chat_message_idempotency`).Scan(&idempotency)
	if idempotency != 0 {
		t.Fatal("过期幂等键未清理")
	}
	var checkpoints int64
	_ = chat.QueryRow(`SELECT COUNT(*) FROM chat_context_checkpoints`).Scan(&checkpoints)
	if checkpoints != 0 {
		t.Fatal("过期检查点未清理")
	}
	var assets int64
	_ = chat.QueryRow(`SELECT COUNT(*) FROM chat_assets`).Scan(&assets)
	if assets != 0 {
		t.Fatal("过期资产未删除")
	}
	var assetUsage int64
	_ = chat.QueryRow(`SELECT COALESCE(asset_count, 0) FROM chat_user_asset_usage WHERE system_account_id = 'sys_a'`).Scan(&assetUsage)
	if assetUsage != 0 {
		t.Fatalf("资产用量未扣减：%d", assetUsage)
	}
	if _, err := os.Stat(filepath.Join(dir, "chat-assets", "img", "a.png")); !os.IsNotExist(err) {
		t.Fatal("资产文件未删除")
	}
	var conversations int64
	_ = chat.QueryRow(`SELECT COUNT(*) FROM chat_conversations WHERE id = 'conv2'`).Scan(&conversations)
	if conversations != 0 {
		t.Fatalf("空会话未清理：%d", conversations)
	}
	var liveConversations int64
	_ = chat.QueryRow(`SELECT COUNT(*) FROM chat_conversations WHERE id = 'conv'`).Scan(&liveConversations)
	if liveConversations != 1 {
		t.Fatal("仍有未过期消息的会话被误删")
	}
}

func seedDeletedAccount(t *testing.T, dir string) {
	business := openTestSQLite(t, filepath.Join(dir, "business.sqlite3"))
	mustExec(t, business,
		`CREATE TABLE IF NOT EXISTS system_sessions (id TEXT PRIMARY KEY, expires_at TEXT)`,
		`CREATE TABLE IF NOT EXISTS accounts (
			id TEXT PRIMARY KEY, system_account_id TEXT, deleted_at TEXT, deleted_by TEXT, updated_at TEXT, created_at TEXT,
			authorization_instance_authorization_id TEXT, authorization_instance_source_account_id TEXT,
			status TEXT DEFAULT 'active', schedulable INTEGER DEFAULT 1, cooldown_until TEXT,
			provider_code TEXT DEFAULT 'openai', type TEXT DEFAULT 'api_key', config_revision INTEGER DEFAULT 1, dispatch_revision INTEGER DEFAULT 1)`,
		`CREATE TABLE IF NOT EXISTS resource_authorizations (
			id TEXT PRIMARY KEY, resource_type TEXT, resource_id TEXT, resource_owner_system_account_id TEXT,
			grantee_system_account_id TEXT, status TEXT DEFAULT 'active', effective_source_type TEXT, effective_source_team_id TEXT,
			revoked_by TEXT, revoked_at TEXT, revoked_reason TEXT, last_source_changed_at TEXT)`,
		`CREATE TABLE IF NOT EXISTS resource_authorization_sources (
			id TEXT PRIMARY KEY, authorization_id TEXT, source_type TEXT, source_team_id TEXT,
			status TEXT DEFAULT 'active', ended_at TEXT, ended_reason TEXT, revoked_by TEXT, revoked_at TEXT, updated_at TEXT)`,
		`CREATE TABLE IF NOT EXISTS resource_authorization_grants (
			id TEXT PRIMARY KEY, resource_type TEXT, resource_id TEXT, resource_owner_system_account_id TEXT,
			grantee_type TEXT, grantee_system_account_id TEXT, grantee_team_id TEXT, status TEXT DEFAULT 'active',
			created_at TEXT, revoked_by TEXT, revoked_at TEXT, updated_at TEXT)`,
		`CREATE TABLE IF NOT EXISTS group_accounts (id TEXT PRIMARY KEY, account_id TEXT, account_authorization_id TEXT)`,
		"CREATE TABLE IF NOT EXISTS account_supported_models (account_id TEXT)",
		"CREATE TABLE IF NOT EXISTS account_model_mappings (account_id TEXT)",
		"CREATE TABLE IF NOT EXISTS account_tag_bindings (account_id TEXT)",
		"CREATE TABLE IF NOT EXISTS request_quota_hourly_window_scope_bindings (scope_type TEXT, scope_id TEXT, source_type TEXT, source_id TEXT)",
		"CREATE TABLE IF NOT EXISTS account_health_jobs_input_versions (account_id TEXT PRIMARY KEY, current_version INTEGER, reserved_at TEXT)",
		`CREATE TABLE IF NOT EXISTS account_health_jobs_input_outbox (
			event_id TEXT PRIMARY KEY, account_id TEXT, input_version INTEGER, event_kind TEXT, reason TEXT,
			config_revision INTEGER, dispatch_revision INTEGER, status TEXT, available_at TEXT, created_at TEXT, updated_at TEXT)`,
		// 逻辑删除超过 1 个月的账户 + 关联授权行
		`INSERT INTO accounts (id, system_account_id, deleted_at, updated_at) VALUES ('acc-del', 'sys_a', '2020-01-01T00:00:00.000Z', '2020-01-01T00:00:00.000Z')`,
		`INSERT INTO resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id) VALUES ('auth1', 'account', 'acc-del', 'sys_owner', 'sys_b')`,
		`INSERT INTO resource_authorization_sources (id, authorization_id, source_type) VALUES ('src1', 'auth1', 'manual')`,
		`INSERT INTO group_accounts (id, account_id) VALUES ('ga1', 'acc-del')`,
		// 未过期删除：保留
		`INSERT INTO accounts (id, system_account_id, deleted_at, updated_at) VALUES ('acc-keep', 'sys_a', '2100-01-01T00:00:00.000Z', '2100-01-01T00:00:00.000Z')`)

	stats := openTestSQLite(t, filepath.Join(dir, "stats.sqlite3"))
	statsCleanupTables(t, stats)

	dataset := openTestSQLite(t, filepath.Join(dir, "dataset.sqlite3"))
	mustExec(t, dataset,
		"CREATE TABLE IF NOT EXISTS public_api_logs (id TEXT PRIMARY KEY, created_at TEXT)",
		"CREATE TABLE IF NOT EXISTS api_key_record_cleanup_targets (api_key_id TEXT PRIMARY KEY, system_account_id TEXT, created_at TEXT, updated_at TEXT, attempt_count INTEGER DEFAULT 0, last_attempt_at TEXT, last_blocked_reason TEXT, last_error_message TEXT)",
		"CREATE TABLE IF NOT EXISTS account_record_cleanup_targets (account_id TEXT PRIMARY KEY, system_account_id TEXT, related_account_ids_json TEXT DEFAULT '[]', authorization_ids_json TEXT DEFAULT '[]', team_scope_ids_json TEXT DEFAULT '[]', created_at TEXT, updated_at TEXT, attempt_count INTEGER DEFAULT 0, last_attempt_at TEXT, last_blocked_reason TEXT, last_error_message TEXT)")

	catalog := openTestSQLite(t, filepath.Join(dir, "usage-catalog.sqlite3"))
	mustExec(t, catalog,
		`CREATE TABLE IF NOT EXISTS usage_record_shards (shard_key TEXT PRIMARY KEY, bucket_date TEXT, shard_id INTEGER, file_path TEXT, status TEXT)`,
		`CREATE TABLE IF NOT EXISTS usage_record_shard_entries (usage_id TEXT, shard_key TEXT, created_at TEXT, system_account_id TEXT, api_key_id TEXT, account_id TEXT)`,
		`CREATE TABLE IF NOT EXISTS usage_record_account_shards (account_id TEXT, shard_key TEXT, first_created_at TEXT, last_seen_at TEXT)`,
		`CREATE TABLE IF NOT EXISTS usage_record_api_key_shards (api_key_id TEXT, system_account_id TEXT, shard_key TEXT, first_created_at TEXT, last_seen_at TEXT)`)
}

func TestWorkerRetentionExpiredDeletedAccountRound(t *testing.T) {
	dir := t.TempDir()
	seedDeletedAccount(t, dir)
	assembly, err := buildWorkerAssembly(retentionTestConfig(dir), slog.Default())
	if err != nil {
		t.Fatalf("build worker assembly: %v", err)
	}
	defer assembly.closeStores()

	if _, err := assembly.runWiredJobOnce(context.Background(), "expired-deleted-account-cleanup"); err != nil {
		t.Fatalf("expired-deleted-account-cleanup round: %v", err)
	}

	business := openTestSQLite(t, filepath.Join(dir, "business.sqlite3"))
	var deleted int64
	_ = business.QueryRow(`SELECT COUNT(*) FROM accounts WHERE id = 'acc-del'`).Scan(&deleted)
	if deleted != 0 {
		t.Fatal("过期逻辑删除账户未物理删除")
	}
	var kept int64
	_ = business.QueryRow(`SELECT COUNT(*) FROM accounts WHERE id = 'acc-keep'`).Scan(&kept)
	if kept != 1 {
		t.Fatal("未过期删除账户被误删")
	}
	var authorizations int64
	_ = business.QueryRow(`SELECT COUNT(*) FROM resource_authorizations WHERE id = 'auth1'`).Scan(&authorizations)
	if authorizations != 0 {
		t.Fatal("关联授权未物理删除")
	}
	var groupBindings int64
	_ = business.QueryRow(`SELECT COUNT(*) FROM group_accounts WHERE account_id = 'acc-del'`).Scan(&groupBindings)
	if groupBindings != 0 {
		t.Fatal("分组绑定未物理删除")
	}
}

func TestWorkerRetentionRecordCleanupRetryRound(t *testing.T) {
	dir := t.TempDir()
	seedDeletedAccount(t, dir)

	// 已删除 API Key 目标 + 一个分片中的使用记录 + 已聚合的统计行。
	stats := openTestSQLite(t, filepath.Join(dir, "stats.sqlite3"))
	mustExec(t, stats,
		"DROP TABLE IF EXISTS usage_stats_totals",
		"CREATE TABLE usage_stats_totals (system_account_id TEXT, scope_type TEXT, scope_id TEXT, request_count REAL DEFAULT 0, success_count REAL DEFAULT 0, error_count REAL DEFAULT 0, input_tokens REAL DEFAULT 0, output_tokens REAL DEFAULT 0, cache_read_tokens REAL DEFAULT 0, cache_read_cost_usd REAL DEFAULT 0, cache_write_tokens REAL DEFAULT 0, cache_write_1h_tokens REAL DEFAULT 0, cache_write_cost_usd REAL DEFAULT 0, thinking_tokens REAL DEFAULT 0, input_image_tokens REAL DEFAULT 0, output_image_tokens REAL DEFAULT 0, total_cost_usd REAL DEFAULT 0, duration_ms_sum REAL DEFAULT 0, duration_ms_count REAL DEFAULT 0, duration_ms_max REAL DEFAULT 0, first_token_ms_sum REAL DEFAULT 0, first_token_ms_count REAL DEFAULT 0, first_token_ms_max REAL DEFAULT 0, last_used_at TEXT, last_error_at TEXT, updated_at TEXT)",
		`INSERT INTO usage_stats_totals (system_account_id, scope_type, scope_id, request_count) VALUES ('sys_a', 'api_key', 'key1', 1)`,
		`INSERT INTO account_quality_dirty_accounts (account_id, first_dirty_at, updated_at) VALUES ('acc-1', '2020-01-01T00:00:00.000Z', '2020-01-01T00:00:00.000Z')`,
		`INSERT INTO stats_job_state (cursor_created_at, cursor_id, scope_type, scope_id, job_name) VALUES ('2099-01-01T00:00:00.000Z', 'u-x', 'usage_shard', '20200101:s01', 'usage_stats_aggregation')`,
		`INSERT INTO stats_job_state (cursor_created_at, cursor_id, scope_type, scope_id, job_name) VALUES ('2099-01-01T00:00:00.000Z', 'u-x', 'usage_shard', '20200101:s01', 'client_ip_stats_aggregation')`)
	// 重建带主键的 deductions 表（前面仅建了 updated_at 列）。
	mustExec(t, stats,
		"DROP TABLE usage_record_cleanup_deductions",
		`CREATE TABLE usage_record_cleanup_deductions (
			usage_id TEXT, api_key_id TEXT, account_id TEXT, system_account_id TEXT, source_shard_key TEXT,
			record_json TEXT, stats_subtracted_at TEXT, shard_deleted_at TEXT, created_at TEXT, updated_at TEXT,
			PRIMARY KEY (usage_id, source_shard_key))`)
	// account quality minute stats 需要全部扣减列。
	mustExec(t, stats,
		"DROP TABLE account_quality_minute_stats",
		`CREATE TABLE account_quality_minute_stats (
			account_id TEXT, system_account_id TEXT DEFAULT '', provider_code TEXT DEFAULT '', stat_minute TEXT,
			request_count INTEGER DEFAULT 0, success_count INTEGER DEFAULT 0, error_count INTEGER DEFAULT 0,
			first_token_ms_sum REAL DEFAULT 0, first_token_ms_count INTEGER DEFAULT 0,
			last_sample_at TEXT, last_success_at TEXT, last_error_at TEXT, last_error_message TEXT, updated_at TEXT,
			PRIMARY KEY (account_id, stat_minute))`,
		`INSERT INTO account_quality_minute_stats (account_id, stat_minute, request_count, success_count, error_count, first_token_ms_sum, first_token_ms_count, last_sample_at, updated_at)
		 VALUES ('acc-1', '2020-01-01T00:00', 1, 1, 0, 5, 1, '2020-01-01T00:00:00.000Z', '2020-01-01T00:00:00.000Z')`)

	catalog := openTestSQLite(t, filepath.Join(dir, "usage-catalog.sqlite3"))
	shardPath := filepath.Join(dir, "usage-shards", "2020", "01", "01", "usage-20200101-s01.sqlite3")
	mustExec(t, catalog,
		fmt.Sprintf("INSERT INTO usage_record_shards (shard_key, bucket_date, shard_id, file_path, status) VALUES ('20200101:s01', '2020-01-01', 1, '%s', 'active')", shardPath), //nolint
		`INSERT INTO usage_record_shard_entries (usage_id, shard_key, created_at, system_account_id, api_key_id, account_id)
		 VALUES ('u1', '20200101:s01', '2020-01-01T00:00:00.000Z', 'sys_a', 'key1', 'acc-1')`,
		`INSERT INTO usage_record_api_key_shards (api_key_id, system_account_id, shard_key, first_created_at, last_seen_at)
		 VALUES ('key1', 'sys_a', '20200101:s01', '2020-01-01T00:00:00.000Z', '2020-01-01T00:00:00.000Z')`,
		`INSERT INTO usage_record_account_shards (account_id, shard_key, first_created_at, last_seen_at)
		 VALUES ('acc-1', '20200101:s01', '2020-01-01T00:00:00.000Z', '2020-01-01T00:00:00.000Z')`)
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
		 VALUES ('u1', 'sys_a', 'key1', 'acc-1', 1, '2020-01-01T00:00:00.000Z')`)

	dataset := openTestSQLite(t, filepath.Join(dir, "dataset.sqlite3"))
	mustExec(t, dataset,
		`INSERT INTO api_key_record_cleanup_targets (api_key_id, system_account_id, created_at, updated_at) VALUES ('key1', 'sys_a', '2020-01-01T00:00:00.000Z', '2020-01-01T00:00:00.000Z')`,
		`INSERT INTO account_record_cleanup_targets (account_id, system_account_id, created_at, updated_at) VALUES ('acc-1', 'sys_a', '2020-01-01T00:00:00.000Z', '2020-01-01T00:00:00.000Z')`)

	assembly, err := buildWorkerAssembly(retentionTestConfig(dir), slog.Default())
	if err != nil {
		t.Fatalf("build worker assembly: %v", err)
	}
	defer assembly.closeStores()

	if _, err := assembly.runWiredJobOnce(context.Background(), "api-key-record-cleanup-retry"); err != nil {
		t.Fatalf("api-key-record-cleanup-retry round: %v", err)
	}
	if _, err := assembly.runWiredJobOnce(context.Background(), "account-record-cleanup-retry"); err != nil {
		t.Fatalf("account-record-cleanup-retry round: %v", err)
	}

	datasetAgain := openTestSQLite(t, filepath.Join(dir, "dataset.sqlite3"))
	var targets int64
	_ = datasetAgain.QueryRow(`SELECT COUNT(*) FROM api_key_record_cleanup_targets`).Scan(&targets)
	if targets != 0 {
		t.Fatalf("API Key 清理目标未完成清空：%d", targets)
	}
	var accountTargets int64
	_ = datasetAgain.QueryRow(`SELECT COUNT(*) FROM account_record_cleanup_targets`).Scan(&accountTargets)
	if accountTargets != 0 {
		t.Fatalf("AI 账户清理目标未完成清空：%d", accountTargets)
	}
	shardAgain := openTestSQLite(t, shardPath)
	var shardRows int64
	_ = shardAgain.QueryRow(`SELECT COUNT(*) FROM usage_records`).Scan(&shardRows)
	if shardRows != 0 {
		t.Fatal("使用记录未删除")
	}
	statsAgain := openTestSQLite(t, filepath.Join(dir, "stats.sqlite3"))
	var totals int64
	_ = statsAgain.QueryRow(`SELECT COUNT(*) FROM usage_stats_totals`).Scan(&totals)
	if totals != 0 {
		t.Fatal("api_key scope 统计未清理")
	}
	var qualityRows int64
	_ = statsAgain.QueryRow(`SELECT COUNT(*) FROM account_quality_minute_stats WHERE request_count > 0`).Scan(&qualityRows)
	if qualityRows != 0 {
		t.Fatal("账号质量分钟统计未扣减")
	}
}

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsagg"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accountbalance"
)

// wgUnionBusinessSchema 是「跑通全部 GoWired 任务一轮」所需的业务库联合
// fixture：探针族核心表（列最全的 accounts 形状）+ OAuth/J4 家族表 +
// 余额探测列 + 系统设置。生产 DDL 归 maintenance 项目，测试只建所需列。
var wgUnionBusinessSchema = []string{
	// accounts：探针族形状为基线，叠加 OAuth 族与余额探测列。
	`CREATE TABLE IF NOT EXISTS accounts (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL DEFAULT 'sys_admin',
      name TEXT NOT NULL DEFAULT '',
      type TEXT NOT NULL DEFAULT 'api_key',
      status TEXT NOT NULL DEFAULT 'active',
      schedulable INTEGER NOT NULL DEFAULT 1,
      provider_code TEXT NOT NULL DEFAULT 'openai',
      provider_protocol_profile_id TEXT,
      protocol_code TEXT,
      protocol_version TEXT,
      client_compatibility TEXT,
      health_check_model TEXT NOT NULL DEFAULT '',
      health_check_endpoint_mode TEXT NOT NULL DEFAULT '',
      account_expires_at TEXT,
      cooldown_until TEXT,
      last_error_code TEXT,
      last_error_message TEXT,
      credentials_encrypted TEXT NOT NULL DEFAULT '{}',
      credential_fingerprint TEXT NOT NULL DEFAULT '',
      credential_mask TEXT NOT NULL DEFAULT '',
      concurrency_limit INTEGER NOT NULL DEFAULT 0,
      priority INTEGER NOT NULL DEFAULT 0,
      super_priority_enabled INTEGER NOT NULL DEFAULT 0,
      fallback_enabled INTEGER NOT NULL DEFAULT 0,
      oauth_access_token_expires_at TEXT,
      oauth_refresh_token_present INTEGER NOT NULL DEFAULT 0,
      availability_schedule_json TEXT,
      availability_schedule_next_check_at TEXT,
      authorization_instance_authorization_id TEXT,
      authorization_instance_source_account_id TEXT,
      authorization_instance_owner_system_account_id TEXT,
      config_revision INTEGER NOT NULL DEFAULT 1,
      dispatch_revision INTEGER NOT NULL DEFAULT 1,
      balance_query_enabled INTEGER NOT NULL DEFAULT 0,
      balance_query_config_json TEXT NOT NULL DEFAULT '{}',
      balance_query_next_refresh_at TEXT,
      proxy_profile_id TEXT,
      last_health_success_at TEXT,
      last_used_at TEXT,
      cooldown_retest_failure_count INTEGER NOT NULL DEFAULT 0,
      cooldown_retest_observation_started_at TEXT,
      cooldown_retest_generation TEXT,
      cooldown_retest_last_at TEXT,
      cooldown_retest_last_status_code INTEGER,
      stream_failure_count INTEGER NOT NULL DEFAULT 0,
      stream_failure_window_started_at TEXT,
      deleted_at TEXT,
      updated_at TEXT NOT NULL DEFAULT ''
    )`,
	`CREATE TABLE IF NOT EXISTS groups (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      provider_code TEXT NOT NULL,
      enabled INTEGER NOT NULL DEFAULT 1,
      group_type TEXT,
      scheduling_policy_json TEXT
    )`,
	`CREATE TABLE IF NOT EXISTS group_accounts (
      group_id TEXT NOT NULL,
      system_account_id TEXT NOT NULL,
      account_id TEXT NOT NULL,
      account_authorization_id TEXT,
      local_priority INTEGER NOT NULL DEFAULT 0,
      local_super_priority_enabled INTEGER NOT NULL DEFAULT 0,
      local_fallback_enabled INTEGER NOT NULL DEFAULT 0,
      enabled INTEGER NOT NULL DEFAULT 1,
      updated_at TEXT NOT NULL DEFAULT ''
    )`,
	`CREATE TABLE IF NOT EXISTS resource_authorizations (
      id TEXT PRIMARY KEY,
      resource_type TEXT NOT NULL,
      resource_id TEXT NOT NULL,
      grantee_system_account_id TEXT NOT NULL,
      resource_owner_system_account_id TEXT NOT NULL,
      status TEXT NOT NULL,
      expires_at TEXT,
      limits_json TEXT,
      effective_source_type TEXT,
      effective_source_team_id TEXT
    )`,
	`CREATE TABLE IF NOT EXISTS providers (code TEXT PRIMARY KEY, enabled INTEGER NOT NULL DEFAULT 1)`,
	`CREATE TABLE IF NOT EXISTS provider_protocol_profiles (
      id TEXT PRIMARY KEY,
      provider_code TEXT NOT NULL,
      enabled INTEGER NOT NULL DEFAULT 1,
      protocol_code TEXT NOT NULL,
      protocol_version TEXT NOT NULL
    )`,
	`CREATE TABLE IF NOT EXISTS api_keys (
      id TEXT PRIMARY KEY,
      status TEXT NOT NULL DEFAULT 'active',
      availability_schedule_json TEXT,
      availability_schedule_next_check_at TEXT,
      updated_at TEXT NOT NULL DEFAULT ''
    )`,
	`CREATE TABLE IF NOT EXISTS api_key_schedule_status_events (
      event_key TEXT PRIMARY KEY, api_key_id TEXT NOT NULL, status TEXT NOT NULL, executed_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS account_schedule_status_events (
      event_key TEXT PRIMARY KEY, account_id TEXT NOT NULL, status TEXT NOT NULL, executed_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS account_quality_enforcements (
      account_id TEXT NOT NULL, state TEXT NOT NULL, action TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS account_health_jobs_input_versions (
      account_id TEXT PRIMARY KEY,
      current_version INTEGER NOT NULL CHECK (current_version >= 1),
      reserved_at TEXT NOT NULL
    )`,
	`CREATE TABLE IF NOT EXISTS account_health_jobs_input_outbox (
      event_id TEXT PRIMARY KEY,
      account_id TEXT NOT NULL,
      input_version INTEGER NOT NULL CHECK (input_version >= 1),
      event_kind TEXT NOT NULL CHECK (event_kind IN ('snapshot', 'tombstone')),
      reason TEXT NOT NULL,
      config_revision INTEGER NOT NULL CHECK (config_revision >= 1),
      dispatch_revision INTEGER NOT NULL CHECK (dispatch_revision >= 1),
      status TEXT NOT NULL CHECK (status IN ('pending', 'leased', 'published', 'failed', 'superseded')),
      claim_token TEXT,
      claimed_until TEXT,
      attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
      available_at TEXT NOT NULL,
      last_error TEXT,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      UNIQUE (account_id, input_version)
    )`,
	`CREATE TABLE IF NOT EXISTS resource_authorization_grants (
      id TEXT PRIMARY KEY,
      resource_type TEXT NOT NULL,
      resource_id TEXT NOT NULL,
      owner_system_account_id TEXT NOT NULL,
      grantee_type TEXT NOT NULL,
      grantee_id TEXT NOT NULL,
      status TEXT NOT NULL,
      revoked_at TEXT,
      revoked_by TEXT,
      created_by TEXT NOT NULL DEFAULT '',
      expires_at TEXT,
      updated_at TEXT NOT NULL DEFAULT ''
    )`,
	`CREATE TABLE IF NOT EXISTS proxy_profiles (
      id TEXT PRIMARY KEY,
      type TEXT NOT NULL,
      host TEXT,
      port INTEGER,
      username TEXT,
      password_encrypted TEXT,
      enabled INTEGER NOT NULL DEFAULT 1
    )`,
	`CREATE TABLE IF NOT EXISTS request_quota_hourly_window_scope_bindings (
      system_account_id TEXT NOT NULL,
      scope_type TEXT NOT NULL,
      scope_id TEXT NOT NULL DEFAULT '',
      source_type TEXT NOT NULL,
      source_id TEXT NOT NULL,
      window_hours INTEGER NOT NULL,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      PRIMARY KEY (system_account_id, scope_type, scope_id)
    )`,
	`CREATE TABLE IF NOT EXISTS system_settings (
      system_account_id TEXT NOT NULL,
      key TEXT NOT NULL,
      value_json TEXT,
      PRIMARY KEY (system_account_id, key)
    )`,
	`INSERT OR IGNORE INTO system_settings (system_account_id, key, value_json) VALUES ('sys_admin', 'usageStatsTimezone', '"UTC"')`,
	// 逻辑删除账户物理清理的关联域（expired-deleted-account-cleanup 读取面）。
	`CREATE TABLE IF NOT EXISTS resource_authorization_sources (
      id TEXT PRIMARY KEY,
      authorization_id TEXT,
      source_type TEXT,
      source_team_id TEXT,
      status TEXT DEFAULT 'active',
      ended_at TEXT,
      ended_reason TEXT,
      revoked_by TEXT,
      revoked_at TEXT,
      updated_at TEXT
    )`,
	`CREATE TABLE IF NOT EXISTS account_supported_models (account_id TEXT)`,
	`CREATE TABLE IF NOT EXISTS account_model_mappings (account_id TEXT)`,
	`CREATE TABLE IF NOT EXISTS account_tag_bindings (account_id TEXT)`,
	// 账户电路族（control-plane maintenance 的认领 outbox 与事件账本）。
	`CREATE TABLE IF NOT EXISTS account_circuit_incidents (
      circuit_scope_key TEXT PRIMARY KEY,
      account_id TEXT NOT NULL,
      account_runtime_key TEXT NOT NULL,
      scope_kind TEXT NOT NULL,
      key_fingerprint TEXT,
      protocol_code TEXT,
      request_lane TEXT,
      model_family TEXT,
      incident_id TEXT NOT NULL,
      parent_incident_id TEXT,
      child_incident_ids_json TEXT NOT NULL DEFAULT '[]',
      state TEXT NOT NULL,
      generation INTEGER NOT NULL,
      dispatch_revision INTEGER NOT NULL,
      ledger_revision INTEGER NOT NULL,
      projected_ledger_revision INTEGER NOT NULL DEFAULT 0,
      transition_id TEXT NOT NULL,
      lease_id TEXT,
      lease_purpose TEXT,
      lease_until_ms INTEGER,
      backoff_level INTEGER NOT NULL DEFAULT 0,
      consecutive_failures INTEGER NOT NULL DEFAULT 0,
      confirmation_failures_required INTEGER NOT NULL DEFAULT 1,
      confirmation_failure_evidence_keys_json TEXT NOT NULL DEFAULT '[]',
      recovering_successes INTEGER NOT NULL DEFAULT 0,
      next_transition_at_ms INTEGER,
      open_until_ms INTEGER,
      last_failure_class TEXT,
      retained_until_ms INTEGER,
      created_at_ms INTEGER NOT NULL,
      updated_at_ms INTEGER NOT NULL
    )`,
	`CREATE TABLE IF NOT EXISTS account_circuit_outbox (
      event_id TEXT PRIMARY KEY,
      projection_key TEXT NOT NULL,
      dedupe_key TEXT NOT NULL,
      event_type TEXT NOT NULL,
      account_id TEXT NOT NULL,
      account_runtime_key TEXT NOT NULL,
      circuit_scope_key TEXT,
      incident_id TEXT,
      transition_id TEXT NOT NULL,
      dispatch_revision INTEGER NOT NULL,
      generation INTEGER,
      ledger_revision INTEGER,
      status TEXT NOT NULL DEFAULT 'pending',
      available_at_ms INTEGER NOT NULL,
      claim_token TEXT,
      claimed_by TEXT,
      claim_until_ms INTEGER,
      attempt_count INTEGER NOT NULL DEFAULT 0,
      last_error_class TEXT,
      acknowledged_at_ms INTEGER,
      created_at_ms INTEGER NOT NULL,
      updated_at_ms INTEGER NOT NULL
    )`,
}

// wgBalanceSnapshotColumns 是余额探测快照表的完整列形状（stats 库；
// statsCleanupTables 的简化版本满足不了 relay_balance upsert 的主键列，
// 在 fixture 中先 DROP 再按此形状重建）。

// wgSeedAllJobsFixture 在 dir 下构建全部任务族可跑通的联合 fixture：
// 复用 retention 种子（数据/统计/分片/codex），再叠加业务库联合 schema、
// statsagg 完整 schema 与余额快照表。
func wgSeedAllJobsFixture(t *testing.T, dir string) {
	t.Helper()
	seedDataRetention(t, dir)
	seedChatRetention(t, dir)
	business := openTestSQLite(t, filepath.Join(dir, "business.sqlite3"))
	for _, statement := range wgUnionBusinessSchema {
		if _, err := business.Exec(statement); err != nil {
			t.Fatalf("业务库联合 schema 失败: %v", err)
		}
	}
	// record cleanup 重试目标表（dataset 库；gateway cleanup POST 的来源表）。
	dataset := openTestSQLite(t, filepath.Join(dir, "dataset.sqlite3"))
	mustExec(t, dataset,
		"CREATE TABLE IF NOT EXISTS api_key_record_cleanup_targets (api_key_id TEXT PRIMARY KEY, system_account_id TEXT, created_at TEXT, updated_at TEXT, attempt_count INTEGER DEFAULT 0, last_attempt_at TEXT, last_blocked_reason TEXT, last_error_message TEXT)",
		"CREATE TABLE IF NOT EXISTS account_record_cleanup_targets (account_id TEXT PRIMARY KEY, system_account_id TEXT, related_account_ids_json TEXT DEFAULT '[]', authorization_ids_json TEXT DEFAULT '[]', team_scope_ids_json TEXT DEFAULT '[]', created_at TEXT, updated_at TEXT, attempt_count INTEGER DEFAULT 0, last_attempt_at TEXT, last_blocked_reason TEXT, last_error_message TEXT)")
	stats := openTestSQLite(t, filepath.Join(dir, "stats.sqlite3"))
	// statsCleanupTables 的简化表会挡住 statsagg/accountquality/statsverify
	// 完整 schema（IF NOT EXISTS 不改列），先 DROP 升级表再应用完整 schema
	// （quota 测试同款模式）；client_ip_* 与 account_quality_scores 由
	// statsverify/accountquality 的 EnsureSchema 以完整列集重建。
	mustExec(t, stats, append(statsaggUpgradedDropStatements(),
		"DROP TABLE IF EXISTS account_quality_scores",
		"DROP TABLE IF EXISTS client_ip_registry",
		"DROP TABLE IF EXISTS client_ip_stats_daily",
		"DROP TABLE IF EXISTS client_ip_usage_range_windows",
		"DROP TABLE IF EXISTS client_ip_range_window_dirty_ips",
		"DROP TABLE IF EXISTS client_ip_policy_hits",
		"DROP TABLE IF EXISTS client_ip_account_stats_daily",
		"DROP TABLE IF EXISTS client_ip_account_usage_range_windows",
		"DROP TABLE IF EXISTS client_ip_account_range_window_dirty_ips")...)
	for _, statement := range statsagg.SQLiteTestSchema {
		if _, err := stats.Exec(statement); err != nil {
			t.Fatalf("statsagg schema 失败: %v", err)
		}
	}
	// stats_job_state 被升级重建后需补种安全游标（usage/cleanup 链路放行依据，
	// 与 seedDataRetention 的种子一致）。
	mustExec(t, stats,
		"INSERT INTO stats_job_state (cursor_created_at, cursor_id, scope_type, job_name, updated_at) VALUES ('2099-01-01T00:00:00.000Z', 'usage-x', 'global', 'usage_stats_aggregation', '2020-01-01T00:00:00.000Z')",
		"INSERT INTO stats_job_state (cursor_created_at, cursor_id, scope_type, scope_id, job_name, updated_at) VALUES ('2099-01-01T00:00:00.000Z', 'usage-x', 'usage_shard', '20200101:s01', 'usage_stats_aggregation', '2020-01-01T00:00:00.000Z')",
		"INSERT INTO stats_job_state (cursor_created_at, cursor_id, scope_type, scope_id, job_name, updated_at) VALUES ('2099-01-01T00:00:00.000Z', 'usage-x', 'usage_shard', '20200101:s01', 'client_ip_stats_aggregation', '2020-01-01T00:00:00.000Z')")
	// usage catalog：seedDataRetention 的简化表缺 trace_id 等列，会挡住
	// usagewriter 的 EnsureCatalogSchema；DROP 后按完整形状重建并补种。
	catalog := openTestSQLite(t, filepath.Join(dir, "usage-catalog.sqlite3"))
	mustExec(t, catalog,
		"DROP TABLE IF EXISTS usage_record_shard_entries",
		"DROP TABLE IF EXISTS usage_record_account_shards",
		"DROP TABLE IF EXISTS usage_record_api_key_shards",
		"DROP TABLE IF EXISTS usage_record_shards",
		`CREATE TABLE usage_record_shards (
          shard_key TEXT PRIMARY KEY,
          bucket_date TEXT NOT NULL,
          shard_id INTEGER NOT NULL,
          file_path TEXT NOT NULL,
          schema_version INTEGER NOT NULL DEFAULT 1,
          status TEXT NOT NULL DEFAULT 'active',
          first_seen_at TEXT NOT NULL,
          last_write_at TEXT,
          last_error_message TEXT,
          created_at TEXT NOT NULL,
          updated_at TEXT NOT NULL
        )`,
		`CREATE TABLE usage_record_shard_entries (
          usage_id TEXT PRIMARY KEY,
          shard_key TEXT NOT NULL,
          system_account_id TEXT NOT NULL,
          trace_id TEXT NOT NULL,
          api_key_id TEXT,
          account_id TEXT,
          group_id TEXT,
          model TEXT,
          traffic_source TEXT NOT NULL,
          success INTEGER NOT NULL DEFAULT 0,
          status_code INTEGER,
          client_ip TEXT,
          first_token_ms INTEGER,
          duration_ms INTEGER,
          cost_usd REAL,
          created_at TEXT NOT NULL,
          indexed_at TEXT NOT NULL
        )`,
		`CREATE TABLE usage_record_account_shards (
          account_id TEXT NOT NULL,
          shard_key TEXT NOT NULL,
          first_created_at TEXT NOT NULL,
          last_seen_at TEXT NOT NULL,
          PRIMARY KEY (account_id, shard_key)
        )`,
		`CREATE TABLE usage_record_api_key_shards (
          api_key_id TEXT NOT NULL,
          system_account_id TEXT NOT NULL,
          shard_key TEXT NOT NULL,
          first_created_at TEXT NOT NULL,
          last_seen_at TEXT NOT NULL,
          PRIMARY KEY (api_key_id, system_account_id, shard_key)
        )`,
		"INSERT INTO usage_record_shards (shard_key, bucket_date, shard_id, file_path, first_seen_at, created_at, updated_at) VALUES ('20200101:s01', '2020-01-01', 1, '"+filepath.Join(dir, "usage-shards", "shard.sqlite3")+"', '2020-01-01T00:00:00.000Z', '2020-01-01T00:00:00.000Z', '2020-01-01T00:00:00.000Z')",
		`INSERT INTO usage_record_shard_entries (usage_id, shard_key, system_account_id, trace_id, traffic_source, created_at, indexed_at)
		 VALUES ('usage-old', '20200101:s01', 'sys_a', 'trace-old', 'api', '2020-01-01T00:00:00.000Z', '2020-01-01T00:00:00.000Z')`)
	for _, statement := range []string{
		"DROP TABLE IF EXISTS account_usage_snapshots",
		`CREATE TABLE account_usage_snapshots (
  system_account_id TEXT NOT NULL,
  account_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  source TEXT,
  snapshot_json TEXT,
  refresh_status TEXT,
  last_attempt_at TEXT,
  last_success_at TEXT,
  next_refresh_after TEXT,
  last_error_message TEXT,
  updated_at TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY (system_account_id, account_id, kind)
)`,
	} {
		if _, err := stats.Exec(statement); err != nil {
			t.Fatalf("余额快照表重建失败: %v", err)
		}
	}
}

// wgAllJobsAssemblyEnv 构造启用全部任务族（含 Redis 电路/速度优先）的 env。
func wgAllJobsAssemblyEnv(t *testing.T, dir string, redisURL string) map[string]string {
	t.Helper()
	env := workerSmokeTestEnv(t)
	// 存储路径全部指向联合 fixture 文件（同库双开：WAL 多连接）。
	env["JUHE_AI_DATABASE_PATH"] = filepath.Join(dir, "business.sqlite3")
	env["JUHE_AI_STATS_DATABASE_PATH"] = filepath.Join(dir, "stats.sqlite3")
	env["JUHE_AI_TASK_RUNS_DATABASE_PATH"] = filepath.Join(dir, "task-runs.sqlite3")
	env["JUHE_AI_USAGE_CATALOG_DATABASE_PATH"] = filepath.Join(dir, "usage-catalog.sqlite3")
	env["JUHE_AI_USAGE_SHARD_ROOT"] = filepath.Join(dir, "usage-shards")
	env["JUHE_AI_DATASET_DATABASE_PATH"] = filepath.Join(dir, "dataset.sqlite3")
	env["JUHE_AI_CHAT_DATABASE_PATH"] = filepath.Join(dir, "chat.sqlite3")
	env["JUHE_AI_CHAT_ASSETS_ROOT"] = filepath.Join(dir, "chat-assets")
	env["JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT"] = filepath.Join(dir, "codex-state")
	env["JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT"] = "1"
	env["JUHE_AI_INSTANCE_ID"] = "wg-all-jobs"
	env["JUHE_AI_WORKER_ROLE"] = "stats-worker"
	env["JUHE_AI_REDIS_STATE_URL"] = redisURL
	env["JUHE_AI_REDIS_NAMESPACE"] = "juhe-ai:wg-test"
	env["JUHE_AI_GATEWAY_ACCOUNT_CIRCUIT_CAPACITY"] = "50000"
	return env
}

// wgAllWiredJobNames 是本轮要逐个直跑的 GoWired 任务全集（Redis 依赖族
// 由 miniredis 承载，account-list-availability-projection-maintenance 在
// SQLite 分支按契约保持 disabled，不在清单内）。
var wgAllWiredJobNames = []string{
	"background-task-run-reconcile",
	"usage-stats-aggregation",
	"client-ip-stats-aggregation",
	"group-account-stats-refresh",
	"usage-stats-consistency-check",
	"usage-rank-snapshots-refresh",
	"system-metrics-trend-windows-refresh",
	"usage-overview-windows-refresh",
	"usage-scope-range-windows-refresh",
	"authorization-usage-range-windows-refresh",
	"usage-hot-window-refresh",
	"usage-quota-hourly-windows-refresh",
	"openai-oauth-access-token-refresh",
	"api-key-availability-schedule-status-sync",
	"account-availability-schedule-status-sync",
	"resource-authorization-expiry-sweep",
	"data-retention-cleanup",
	"chat-retention-cleanup",
	"expired-deleted-account-cleanup",
	"api-key-record-cleanup-retry",
	"account-record-cleanup-retry",
	"account-quality-refresh",
	"account-api-key-cooldown-retest",
	"normal-route-speed-first-recovery-probe",
	"account-circuit-control-plane-maintenance",
	"account-circuit-recovery",
	"account-balance-auto-detect-recovery",
}

// TestWorkerAssemblyRunsEveryWiredJobOnce：联合 fixture 上装配全部任务族，
// 逐个直跑一轮并要求全部成功；同时锁定 Redis 依赖族的接线/登记状态。
func TestWorkerAssemblyRunsEveryWiredJobOnce(t *testing.T) {
	if testing.Short() {
		t.Skip("全任务冒烟 skipped in -short mode")
	}
	redisServer := miniredis.RunT(t)
	dir := t.TempDir()
	wgSeedAllJobsFixture(t, dir)

	env := wgAllJobsAssemblyEnv(t, dir, "redis://"+redisServer.Addr())
	config, err := loadWorkerConfig(getenvFrom(env))
	if err != nil {
		t.Fatalf("loadWorkerConfig: %v", err)
	}
	assembly, err := buildWorkerAssembly(config, nil)
	if err != nil {
		t.Fatalf("buildWorkerAssembly: %v", err)
	}
	defer assembly.closeStores()

	wired := map[string]bool{}
	for _, name := range assembly.wiredJobs {
		wired[name] = true
	}
	for _, name := range wgAllWiredJobNames {
		if !wired[name] {
			t.Errorf("任务 %s 必须接线（disabled=%v）", name, assembly.disabledJobs)
		}
	}
	if t.Failed() {
		t.FailNow()
	}
	// SQLite 分支契约：列表投影族保持 disabled，不得误接线。
	for _, disabled := range assembly.disabledJobs {
		if disabled.JobName == "account-list-availability-projection-maintenance" && wired[disabled.JobName] {
			t.Fatalf("列表投影任务在 SQLite 分支必须保持 disabled: %+v", disabled)
		}
	}

	for _, name := range wgAllWiredJobNames {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, err := assembly.runWiredJobOnce(ctx, name); err != nil {
				t.Fatalf("单轮执行失败: %v", err)
			}
		})
	}
}

// TestWorkerAssemblyRetentionFamilyAdapters 直跑 retention 家族的组合根
// 适配器（queue 执行面、关联清理、重试扫描、快照批量入口、dbService）。
func TestWorkerAssemblyRetentionFamilyAdapters(t *testing.T) {
	if testing.Short() {
		t.Skip("skipped in -short mode")
	}
	redisServer := miniredis.RunT(t)
	dir := t.TempDir()
	wgSeedAllJobsFixture(t, dir)
	env := wgAllJobsAssemblyEnv(t, dir, "redis://"+redisServer.Addr())
	config, err := loadWorkerConfig(getenvFrom(env))
	if err != nil {
		t.Fatalf("loadWorkerConfig: %v", err)
	}
	assembly, err := buildWorkerAssembly(config, nil)
	if err != nil {
		t.Fatalf("buildWorkerAssembly: %v", err)
	}
	defer assembly.closeStores()
	family := assembly.retention
	if family == nil || family.runner == nil || family.queue == nil {
		t.Fatal("retention family 必须完整装配")
	}
	ctx := context.Background()

	// runMaintenanceOnce：互斥面执行一轮 api_key 关联清理（目标不存在 → 零结果）。
	if _, err := family.runMaintenanceOnce(ctx, wgMaintenanceJob("api_key_related_cleanup", "wg-nothing")); err != nil {
		t.Fatalf("runMaintenanceOnce: %v", err)
	}
	// runMaintenanceSnapshotUpserts：批量入口在 cleanup 侧按契约显式报错
	// （快照 upsert 归 J2/J3 探针域，cleanuprepo 不承担）。
	if _, err := family.runMaintenanceSnapshotUpserts(ctx, []retention.RecordMaintenanceJob{}); err == nil {
		t.Fatal("cleanup 侧快照批量入口必须显式报错")
	}

	// familyRelatedCleaner SQLite 路径。
	cleaner := &familyRelatedCleaner{family: family}
	if result, err := cleaner.CleanupApiKeyRelated(ctx, wgMaintenanceJob("api_key_related_cleanup", "wg-nothing"), &familyStatsWriter{family: family}); err != nil {
		t.Fatalf("CleanupApiKeyRelated: %v", err)
	} else if result.DeletedRows != 0 {
		t.Fatalf("不存在目标的清理结果必须为 0: %+v", result)
	}
	if _, err := cleaner.CleanupAccountRelated(ctx, wgMaintenanceJob("account_related_cleanup", "wg-nothing"), &familyStatsWriter{family: family}); err != nil {
		t.Fatalf("CleanupAccountRelated: %v", err)
	}
	// 重试扫描（无 pending 目标）。
	if summary, err := (&familyAPIKeyRetryer{family: family}).CleanupPendingTargets(ctx, 10, nil); err != nil || summary.Attempted != 0 {
		t.Fatalf("CleanupPendingTargets(api-key): %+v %v", summary, err)
	}
	if summary, err := (&familyAccountRetryer{family: family}).CleanupPendingTargets(ctx, 10, nil); err != nil || summary.Attempted != 0 {
		t.Fatalf("CleanupPendingTargets(account): %+v %v", summary, err)
	}
	// 快照 upsert 端口按契约显式报错（归 J2/J3 探针域）。
	if err := (&familyStatsWriter{family: family}).UpsertAccountUsageSnapshots(ctx, nil); err == nil {
		t.Fatal("cleanup 侧快照 upsert 必须显式报错")
	}
	// dbService 适配器（空库 → 零结果）。
	if _, err := family.dbService.CleanupChatRetention(ctx, retention.ChatRetentionInput{Now: "2026-09-10T00:00:00.000Z", InterruptedBefore: "1970-01-01T00:00:00.000Z", Limit: 10}); err != nil {
		t.Fatalf("CleanupChatRetention: %v", err)
	}
	if deleted, err := family.dbService.CleanupExpiredSystemSessions(ctx, "1970-01-01T00:00:00.000Z", 10); err != nil || deleted != 0 {
		t.Fatalf("CleanupExpiredSystemSessions: %d %v", deleted, err)
	}
	if _, err := family.dbService.CleanupExpiredCodexContextStates(ctx, "1970-01-01T00:00:00.000Z", 10); err != nil {
		t.Fatalf("CleanupExpiredCodexContextStates: %v", err)
	}
	if _, err := family.dbService.SettleCodexContextStorageCleanup(ctx, retention.CodexContextSettlement{SucceededStorageKeys: []string{"wg/none.bin"}}); err != nil {
		t.Fatalf("SettleCodexContextStorageCleanup: %v", err)
	}
	if _, err := family.dbService.CleanupExpiredDeletedAccounts(ctx); err != nil {
		t.Fatalf("CleanupExpiredDeletedAccounts: %v", err)
	}
	// codexStorageProcessor：不存在的存储键删除计数为 0。
	processor := &codexStorageProcessor{db: family.dbService, store: family.codex, logger: assembly.logger}
	if deleted, err := processor.ProcessBatch(ctx, []string{"wg/missing.bin"}); err != nil || deleted != 0 {
		t.Fatalf("ProcessBatch: %d %v", deleted, err)
	}
	// datasetCheckpointer：三库 PASSIVE checkpoint。
	checkpointer := &datasetCheckpointer{
		dataset:      openTestSQLite(t, filepath.Join(dir, "dataset.sqlite3")),
		usageCatalog: openTestSQLite(t, filepath.Join(dir, "usage-catalog.sqlite3")),
		stats:        openTestSQLite(t, filepath.Join(dir, "stats.sqlite3")),
	}
	if err := checkpointer.CheckpointAfterDelete(ctx); err != nil {
		t.Fatalf("CheckpointAfterDelete: %v", err)
	}
}

// TestWorkerAssemblyProbeFamilyAdapters 直跑探针/电路族组合根适配器
// （speedFirstCandidateSource、projectionCredentials、probeFamilyLogger）。
func TestWorkerAssemblyProbeFamilyAdapters(t *testing.T) {
	if testing.Short() {
		t.Skip("skipped in -short mode")
	}
	redisServer := miniredis.RunT(t)
	dir := t.TempDir()
	wgSeedAllJobsFixture(t, dir)
	env := wgAllJobsAssemblyEnv(t, dir, "redis://"+redisServer.Addr())
	config, err := loadWorkerConfig(getenvFrom(env))
	if err != nil {
		t.Fatalf("loadWorkerConfig: %v", err)
	}
	assembly, err := buildWorkerAssembly(config, nil)
	if err != nil {
		t.Fatalf("buildWorkerAssembly: %v", err)
	}
	defer assembly.closeStores()
	if assembly.probeRepoStore == nil || assembly.circuitProbeService == nil {
		t.Fatal("探针族句柄必须装配（电路族复用）")
	}
	ctx := context.Background()
	// speedFirstCandidateSource：账户不存在 → nil, nil。
	source := speedFirstCandidateSource{store: assembly.probeRepoStore}
	view, err := source.FindAccountForTest(ctx, "wg-missing", "sys")
	if err != nil || view != nil {
		t.Fatalf("缺失账户必须返回 nil: %v %v", view, err)
	}
	candidate, err := source.FindCandidateAccount(ctx, "wg-group", "wg-missing", "sys")
	if err != nil || candidate != nil {
		t.Fatalf("缺失候选必须返回 nil: %v %v", candidate, err)
	}
	// parseRFC3339Millis：合法与非法输入。
	if ms, err := parseRFC3339Millis(" 2026-09-10T00:00:00Z "); err != nil || ms != time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC).UnixMilli() {
		t.Fatalf("RFC3339 毫秒解析错误: %d %v", ms, err)
	}
	if _, err := parseRFC3339Millis("nope"); err == nil {
		t.Fatal("非法时间必须报错")
	}
	// projectionCredentials：V1 封套凭据解码（proberepo 只接受封套，不收明文）。
	credentials := projectionCredentials{store: assembly.probeRepoStore}
	envelope, envelopeErr := accountbalance.EncryptV1Envelope(config.Secret, []byte(`{"api_keys":["sk-x"]}`))
	if envelopeErr != nil {
		t.Fatal(envelopeErr)
	}
	if _, err := credentials.DecryptCredentials(envelope); err != nil {
		t.Fatalf("封套凭据解码: %v", err)
	}
	entries := credentials.AccountAPIKeyEntries(map[string]any{"api_keys": []any{"sk-x"}})
	if len(entries) != 1 || entries[0].Key != "sk-x" || entries[0].Fingerprint == "" {
		t.Fatalf("Key 池投影必须带指纹: %+v", entries)
	}
	// probeFamilyLogger 四个级别 + slogFields 字段展开。
	logger := probeFamilyLogger{logger: assembly.logger}
	logger.Debug("wg_event", map[string]any{"k": "v"}, "调试消息")
	logger.Info("wg_event", map[string]any{"k": "v"}, "信息消息")
	logger.Warn("wg_event", map[string]any{"k": "v"}, "警告消息")
	logger.Error("wg_event", map[string]any{"k": "v"}, "错误消息")
	fields := slogFields("wg_event", map[string]any{"a": 1, "b": 2})
	if len(fields) != 6 || fields[0] != "event" || fields[1] != "wg_event" {
		t.Fatalf("slogFields 必须前置 event 字段: %v", fields)
	}
}

// TestWorkerAssemblyBalanceDetectRuntimeDirects 直跑余额探测运行态的
// 候选读取与围栏写入路径（覆盖 ListDue/Commit/Enable/快照分支）。
func TestWorkerAssemblyBalanceDetectRuntimeDirects(t *testing.T) {
	if testing.Short() {
		t.Skip("skipped in -short mode")
	}
	redisServer := miniredis.RunT(t)
	dir := t.TempDir()
	wgSeedAllJobsFixture(t, dir)
	// fake 上游命中 user_balance 适配器。
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/user/balance" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"balance":"9.5"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	business := openTestSQLite(t, filepath.Join(dir, "business.sqlite3"))
	secret := "0123456789abcdef0123456789abcdef"
	seedBalanceDetectionAccount(t, context.Background(), business, secret, upstream.URL, -time.Minute)

	env := wgAllJobsAssemblyEnv(t, dir, "redis://"+redisServer.Addr())
	config, err := loadWorkerConfig(getenvFrom(env))
	if err != nil {
		t.Fatalf("loadWorkerConfig: %v", err)
	}
	assembly, err := buildWorkerAssembly(config, nil)
	if err != nil {
		t.Fatalf("buildWorkerAssembly: %v", err)
	}
	defer assembly.closeStores()

	// 直跑一轮恢复任务：候选 → 探测 → 开启 → 快照（端到端在既有测试锁定，
	// 这里断言运行态内部状态已被填充）。
	if _, err := assembly.runWiredJobOnce(context.Background(), "account-balance-auto-detect-recovery"); err != nil {
		t.Fatalf("恢复任务单轮失败: %v", err)
	}
	var enabled int
	if err := business.QueryRow(`SELECT balance_query_enabled FROM accounts WHERE id = 'acc-detect-1'`).Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if enabled != 1 {
		t.Fatal("探测命中后账户必须开启余额查询")
	}
}

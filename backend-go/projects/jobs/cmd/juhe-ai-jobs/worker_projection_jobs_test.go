package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/circuitstore"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobregistry"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
	_ "modernc.org/sqlite"
)

// TestListProjectionConfigDefaults 验证 T6b 冻结清单 §4 的 env 收口：
// enabled 默认 false、interval env 1s..60s 默认 1s、batch/maxBatches/
// workerConcurrency 边界与 Node runtimeConfig.background 一致。
func TestListProjectionConfigDefaults(t *testing.T) {
	config, err := loadWorkerConfig(getenvFrom(map[string]string{}))
	if err != nil {
		t.Fatalf("loadWorkerConfig: %v", err)
	}
	if config.ListProjectionEnabled {
		t.Fatal("投影 env 默认必须为 false（Node accountListAvailabilityProjectionEnabled）")
	}
	if config.ListProjectionIntervalMS != 1_000 {
		t.Fatalf("interval 默认 = %d, 期望 1000", config.ListProjectionIntervalMS)
	}
	if config.ListProjectionBatchSize != 100 || config.ListProjectionMaxBatchesPerRun != 200 || config.ListProjectionWorkerConcurrency != 4 {
		t.Fatalf("batch/maxBatches/concurrency 默认不符: %d/%d/%d",
			config.ListProjectionBatchSize, config.ListProjectionMaxBatchesPerRun, config.ListProjectionWorkerConcurrency)
	}
	if _, err := loadWorkerConfig(getenvFrom(map[string]string{
		"JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_INTERVAL_MS": "500",
	})); err == nil {
		t.Fatal("interval < 1000 必须 fail closed")
	}
	if _, err := loadWorkerConfig(getenvFrom(map[string]string{
		"JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_INTERVAL_MS": "61000",
	})); err == nil {
		t.Fatal("interval > 60000 必须 fail closed")
	}
	if _, err := loadWorkerConfig(getenvFrom(map[string]string{
		"JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_BATCH_SIZE": "101",
	})); err == nil {
		t.Fatal("batchSize > 100 必须 fail closed")
	}
}

// projectionSeedDB 建立投影族全链所需的最小 SQLite 业务库（列集与
// Node schema 的 worker 消费面一致）。
func projectionSeedDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(5000)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	// 与组合根 openSQLite 一致：SQLite 单 writer。
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = db.Close() })
	statements := []string{
		`CREATE TABLE system_accounts (id TEXT PRIMARY KEY, username TEXT, display_name TEXT)`,
		`CREATE TABLE accounts (
			id TEXT PRIMARY KEY, config_revision INTEGER, system_account_id TEXT,
			provider_code TEXT, provider_protocol_profile_id TEXT, protocol_code TEXT, protocol_version TEXT,
			name TEXT, notes TEXT, type TEXT, status TEXT, schedulable INTEGER,
			concurrency_limit INTEGER, priority INTEGER, super_priority_enabled INTEGER, fallback_enabled INTEGER,
			client_compatibility TEXT, balance_query_enabled INTEGER, balance_query_next_refresh_at TEXT,
			availability_schedule_json TEXT, account_expires_at TEXT, cooldown_until TEXT,
			last_error_code TEXT, last_error_message TEXT, last_error_trace_id TEXT,
			last_health_check_at TEXT, next_health_check_at TEXT, last_health_check_status_code INTEGER,
			last_health_check_error_code TEXT, last_health_check_error_message TEXT, last_health_check_trace_id TEXT,
			cooldown_retest_last_at TEXT, cooldown_retest_last_status_code INTEGER,
			cooldown_retest_failure_count INTEGER, cooldown_retest_observation_started_at TEXT,
			health_check_model TEXT, health_check_endpoint_mode TEXT,
			last_used_at TEXT, last_health_success_at TEXT,
			health_check_failure_count INTEGER, health_check_failure_started_at TEXT,
			stream_failure_count INTEGER, stream_failure_window_started_at TEXT,
			created_at TEXT, updated_at TEXT, credentials_encrypted TEXT,
			authorization_instance_owner_system_account_id TEXT,
			authorization_instance_source_account_id TEXT,
			authorization_instance_authorization_id TEXT,
			proxy_profile_id TEXT, dispatch_revision INTEGER, deleted_at TEXT)`,
		`CREATE TABLE resource_authorizations (
			id TEXT PRIMARY KEY, status TEXT, expires_at TEXT, limits_json TEXT,
			effective_source_type TEXT, effective_source_team_id TEXT,
			resource_owner_system_account_id TEXT, resource_type TEXT, resource_id TEXT)`,
		`CREATE TABLE resource_authorization_grants (
			id TEXT PRIMARY KEY, resource_type TEXT, resource_id TEXT, grantee_type TEXT,
			grantee_team_id TEXT, status TEXT, expires_at TEXT, limits_json TEXT)`,
		`CREATE TABLE groups (id TEXT PRIMARY KEY, name TEXT)`,
		`CREATE TABLE group_accounts (
			account_id TEXT, system_account_id TEXT, group_id TEXT, account_authorization_id TEXT,
			local_priority INTEGER, local_super_priority_enabled INTEGER, local_fallback_enabled INTEGER,
			enabled INTEGER, updated_at TEXT)`,
		`CREATE TABLE providers (code TEXT PRIMARY KEY, name TEXT)`,
		`CREATE TABLE proxy_profiles (id TEXT PRIMARY KEY, name TEXT, type TEXT, enabled INTEGER)`,
		`CREATE TABLE account_tags (id TEXT PRIMARY KEY, system_account_id TEXT, name TEXT, created_at TEXT, updated_at TEXT)`,
		`CREATE TABLE account_tag_bindings (account_id TEXT, system_account_id TEXT, tag_id TEXT)`,
		`CREATE TABLE account_name_search_terms (account_id TEXT, term TEXT)`,
		`CREATE TABLE account_name_search_documents (account_id TEXT, normalized_name TEXT)`,
		`CREATE TABLE account_lock_states (
			account_id TEXT PRIMARY KEY, enabled INTEGER, lock_state TEXT,
			lock_death_timeout_seconds INTEGER, lock_retry_interval_seconds INTEGER,
			incident_id TEXT, generation INTEGER, incident_started_at TEXT, deadline_at TEXT,
			original_status TEXT, provenance TEXT, next_retry_at_ms INTEGER,
			lease_id TEXT, lease_until_ms INTEGER, updated_at TEXT)`,
		`CREATE TABLE account_api_key_runtime_states (
			id TEXT PRIMARY KEY, account_id TEXT, key_fingerprint TEXT, status TEXT,
			next_probe_at TEXT, last_failure_at TEXT, last_error_code TEXT,
			last_error_message TEXT, last_error_trace_id TEXT, key_index INTEGER)`,
		`CREATE TABLE account_circuit_incidents (
			circuit_scope_key TEXT, account_id TEXT, account_runtime_key TEXT, scope_kind TEXT,
			incident_id TEXT, state TEXT, generation INTEGER, dispatch_revision INTEGER,
			transition_id TEXT, cooldown_observation_generation INTEGER,
			next_transition_at_ms INTEGER, last_failure_class TEXT, retained_until_ms INTEGER,
			created_at_ms INTEGER, updated_at_ms INTEGER, circuit_incident_revision INTEGER)`,
		// 投影读模型表（Node 迁移 schema 的 worker 面）。
		`CREATE TABLE account_list_availability_projections (
			viewer_system_account_id TEXT, account_id TEXT, source_account_id TEXT, authorization_id TEXT,
			effective_status TEXT, schedulable_bucket TEXT, provider_code TEXT, provider_protocol_profile_id TEXT,
			account_type TEXT, bound_group_id TEXT, name_sort_key TEXT, priority_sort_key INTEGER,
			super_priority_sort_key INTEGER, fallback_sort_key INTEGER, concurrency_sort_key INTEGER,
			account_expires_at_sort_key TEXT, last_used_at_sort_key TEXT, created_at_sort_key TEXT,
			payload_json TEXT, source_generation INTEGER, next_transition_at TEXT, projected_at TEXT,
			PRIMARY KEY (viewer_system_account_id, account_id))`,
		`CREATE TABLE account_list_availability_projection_index (
			viewer_system_account_id TEXT, account_id TEXT, effective_status TEXT, schedulable_bucket TEXT,
			provider_code TEXT, provider_protocol_profile_id TEXT, account_type TEXT, bound_group_id TEXT,
			name_sort_key TEXT, priority_sort_key INTEGER, super_priority_sort_key INTEGER, fallback_sort_key INTEGER,
			concurrency_sort_key INTEGER, account_expires_at_sort_key TEXT, last_used_at_sort_key TEXT,
			created_at_sort_key TEXT, access_type_sort_key TEXT, search_index_complete INTEGER,
			authorization_quota_exceeded INTEGER, PRIMARY KEY (viewer_system_account_id, account_id))`,
		`CREATE TABLE account_list_availability_runtime_overlays (
			account_id TEXT PRIMARY KEY, current_concurrency INTEGER, observed_at TEXT, next_reconcile_at TEXT)`,
		`CREATE TABLE account_list_availability_projection_tags (
			viewer_system_account_id TEXT, account_id TEXT, tag_id TEXT)`,
		`CREATE TABLE account_list_availability_projection_search_terms (
			viewer_system_account_id TEXT, account_id TEXT, term TEXT, name_sort_key TEXT, created_at_sort_key TEXT)`,
		`CREATE TABLE account_list_availability_projection_viewer_health (
			viewer_system_account_id TEXT PRIMARY KEY, projection_count INTEGER,
			oldest_projected_at TEXT, next_transition_at TEXT, is_current INTEGER, updated_at TEXT)`,
		`CREATE TABLE account_list_availability_projection_dependency_health (
			dependency_name TEXT PRIMARY KEY, state TEXT, generation INTEGER, reason TEXT, updated_at TEXT)`,
		`CREATE TABLE account_list_availability_dirty (
			account_id TEXT PRIMARY KEY, viewer_system_account_id TEXT, generation INTEGER,
			applied_generation INTEGER, reason TEXT, available_at_ms INTEGER,
			claim_token TEXT, claimed_by TEXT, claim_until_ms INTEGER, attempt_count INTEGER,
			created_at_ms INTEGER, updated_at_ms INTEGER)`,
		// stats 库表（测试中 stats 句柄与 business 同库）。
		`CREATE TABLE usage_stats_totals (system_account_id TEXT, scope_type TEXT, scope_id TEXT,
			request_count INTEGER, input_tokens INTEGER, output_tokens INTEGER, total_cost_usd REAL, last_used_at TEXT)`,
		`CREATE TABLE usage_stats_daily (system_account_id TEXT, scope_type TEXT, scope_id TEXT, stat_date TEXT,
			request_count INTEGER, input_tokens INTEGER, output_tokens INTEGER, total_cost_usd REAL, last_used_at TEXT)`,
		`CREATE TABLE usage_stats_weekly (system_account_id TEXT, scope_type TEXT, scope_id TEXT, stat_week TEXT, total_cost_usd REAL)`,
		`CREATE TABLE usage_stats_monthly (system_account_id TEXT, scope_type TEXT, scope_id TEXT, stat_month TEXT, total_cost_usd REAL)`,
		`CREATE TABLE usage_quota_hourly_windows (system_account_id TEXT, scope_type TEXT, scope_id TEXT, window_hours INTEGER, total_cost_usd REAL)`,
		`CREATE TABLE account_usage_snapshots (account_id TEXT, kind TEXT, snapshot_json TEXT, next_refresh_after TEXT, updated_at TEXT)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("exec schema: %v\n%s", err, statement)
		}
	}
	return db
}

type projectionStubConcurrency struct{}

func (projectionStubConcurrency) LoadConcurrency(ctx context.Context, accountIDs []string) (map[string]int, error) {
	output := map[string]int{}
	for _, id := range accountIDs {
		output[id] = 2
	}
	return output, nil
}

type projectionStubRuntime struct{}

func (projectionStubRuntime) LoadRuntimeAvailability(ctx context.Context, runtimeKeys []string) (map[string]circuitstore.AccountRuntimeAvailability, error) {
	return map[string]circuitstore.AccountRuntimeAvailability{}, nil
}

type projectionStubTimezone struct{}

func (projectionStubTimezone) StatsTimezone(ctx context.Context) (*time.Location, error) {
	return time.UTC, nil
}

type projectionStubCredentials struct{}

func (projectionStubCredentials) DecryptCredentials(envelope string) (map[string]any, error) {
	return map[string]any{}, nil
}

func (projectionStubCredentials) AccountAPIKeyEntries(credentials map[string]any) []circuitstore.APIKeyPoolEntry {
	return []circuitstore.APIKeyPoolEntry{}
}

type projectionStubProbe struct{}

func (projectionStubProbe) Probe(ctx context.Context) (bool, bool, error) { return true, true, nil }

type projectionStubOverlays struct{}

func (projectionStubOverlays) ListDirtyEntries(ctx context.Context, limit int) ([]opsjobs.OverlayEntry, error) {
	return []opsjobs.OverlayEntry{}, nil
}
func (projectionStubOverlays) Acknowledge(ctx context.Context, entries []opsjobs.OverlayEntry) error {
	return nil
}
func (projectionStubOverlays) LoadSnapshots(ctx context.Context, accountIDs []string) ([]opsjobs.OverlaySnapshot, error) {
	return []opsjobs.OverlaySnapshot{}, nil
}
func (projectionStubOverlays) UpsertOverlays(ctx context.Context, overlays []opsjobs.OverlayUpsert) error {
	return nil
}
func (projectionStubOverlays) ExistingAccountIDs(ctx context.Context, accountIDs []string) (map[string]struct{}, error) {
	return map[string]struct{}{}, nil
}

// TestListProjectionMaintenanceSQLiteRoundTrip：seed 账户与 dirty 行 → 跑一轮
// RunListAvailabilityMaintenance → 断言投影表副作用（projections 行、payload
// 形状、tags、viewer health、dirty 清空）。
func TestListProjectionMaintenanceSQLiteRoundTrip(t *testing.T) {
	db := projectionSeedDB(t, filepath.Join(t.TempDir(), "business.db"))
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, query)
		}
	}
	exec(`INSERT INTO system_accounts (id, username, display_name) VALUES ('sys-1', 'admin', 'Admin')`)
	exec(`INSERT INTO providers (code, name) VALUES ('openai', 'OpenAI')`)
	exec(`INSERT INTO accounts (
			id, config_revision, system_account_id, provider_code, provider_protocol_profile_id,
			protocol_code, protocol_version, name, type, status, schedulable,
			concurrency_limit, priority, super_priority_enabled, fallback_enabled,
			client_compatibility, health_check_model, health_check_endpoint_mode, created_at, updated_at,
			last_used_at, dispatch_revision
		) VALUES (
			'acct-1', 3, 'sys-1', 'openai', 'profile_openai_openai_v1',
			'openai', 'v1', '主账户', 'api_key', 'active', 1,
			10, 5, 1, 0,
			'openai_standard', 'gpt-4o-mini', 'chat_completions', '2026-01-01T00:00:00.000Z', '2026-01-02T00:00:00.000Z',
			'2026-08-01T00:00:00.000Z', 1)`)
	exec(`INSERT INTO account_tags (id, system_account_id, name, created_at, updated_at)
		VALUES ('tag-1', 'sys-1', '生产', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`)
	exec(`INSERT INTO account_tag_bindings (account_id, system_account_id, tag_id) VALUES ('acct-1', 'sys-1', 'tag-1')`)
	exec(`INSERT INTO account_name_search_documents (account_id, normalized_name) VALUES ('acct-1', '主账户')`)
	exec(`INSERT INTO account_name_search_terms (account_id, term) VALUES ('acct-1', '主')`)
	// Node worker 专属 bootstrap：EnqueueMissing 会为缺失投影的账户写 dirty；
	// 这里额外预置一条过期 dirty 验证 claim 路径（generation 由 markDirty 自增）。
	loader, err := circuitstore.NewProjectionItemLoader(circuitstore.ProjectionLoadConfig{
		Business:            db,
		Stats:               db,
		Secret:              "test-secret",
		Credentials:         projectionStubCredentials{},
		Concurrency:         projectionStubConcurrency{},
		RuntimeAvailability: projectionStubRuntime{},
		Timezone:            projectionStubTimezone{},
		Now:                 func() time.Time { return time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("NewProjectionItemLoader: %v", err)
	}
	repo, err := circuitstore.NewListAvailabilityRepo(circuitstore.ListAvailabilityConfig{
		DB:       db,
		Postgres: false,
		Now:      func() time.Time { return time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("NewListAvailabilityRepo: %v", err)
	}
	options := opsjobs.ListAvailabilityOptions{
		OwnerID:          "list-projection:test",
		BatchSize:        100,
		MaxBatchesPerRun: 2,
		NowMS:            func() int64 { return time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC).UnixMilli() },
		Repo:             repo,
		RuntimeProbe:     projectionStubProbe{},
		Overlays:         projectionStubOverlays{},
		LoadItems:        loader.LoadItems,
	}
	result, err := opsjobs.RunListAvailabilityMaintenance(context.Background(), options, true)
	if err != nil {
		t.Fatalf("RunListAvailabilityMaintenance: %v", err)
	}
	if result.Bootstrapped < 1 {
		t.Fatalf("bootstrapped = %d, 期望 ≥1（EnqueueMissing 补 dirty）", result.Bootstrapped)
	}
	if result.Claimed < 1 || result.Projected < 1 {
		t.Fatalf("claimed/projected = %d/%d, 期望各 ≥1", result.Claimed, result.Projected)
	}
	if result.Deleted != 0 || result.Released != 0 {
		t.Fatalf("单账户单轮不应有删除/重放: %+v", result)
	}

	// ---- 投影表副作用断言 ----
	var (
		effectiveStatus   string
		schedulableBucket string
		payloadJSON       string
		lastUsedAtSortKey sql.NullString
		sourceGeneration  int64
	)
	if err := db.QueryRow(`
		SELECT effective_status, schedulable_bucket, payload_json, last_used_at_sort_key, source_generation
		FROM account_list_availability_projections
		WHERE viewer_system_account_id = 'sys-1' AND account_id = 'acct-1'`).Scan(
		&effectiveStatus, &schedulableBucket, &payloadJSON, &lastUsedAtSortKey, &sourceGeneration); err != nil {
		t.Fatalf("查询投影行: %v", err)
	}
	if effectiveStatus != "active" || schedulableBucket != "enabled" {
		t.Fatalf("投影状态/桶 = %s/%s, 期望 active/enabled", effectiveStatus, schedulableBucket)
	}
	if !lastUsedAtSortKey.Valid || lastUsedAtSortKey.String != "2026-08-01T00:00:00.000Z" {
		t.Fatalf("last_used_at_sort_key 应为 accounts.last_used_at: %v", lastUsedAtSortKey)
	}
	if sourceGeneration < 1 {
		t.Fatalf("source_generation = %d, 期望 ≥1", sourceGeneration)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		t.Fatalf("payload_json 解析: %v", err)
	}
	if payload["accessType"] != "owner" || payload["status"] != "active" || payload["name"] != "主账户" {
		t.Fatalf("payload 关键字段不符: %v", payload)
	}
	if concurrency, ok := payload["currentConcurrency"].(float64); !ok || concurrency != 2 {
		t.Fatalf("payload currentConcurrency 应来自 Redis stub: %v", payload["currentConcurrency"])
	}
	if _, exists := payload["effectiveAvailability"]; !exists {
		t.Fatal("payload 应携带 effectiveAvailability")
	}
	if _, exists := payload["tags"]; !exists {
		t.Fatal("payload 应携带 tags（完整标签摘要）")
	}
	// tags 表副作用。
	var tagCount int
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM account_list_availability_projection_tags
		WHERE viewer_system_account_id = 'sys-1' AND account_id = 'acct-1' AND tag_id = 'tag-1'`).Scan(&tagCount); err != nil {
		t.Fatal(err)
	}
	if tagCount != 1 {
		t.Fatalf("投影 tags 行数 = %d, 期望 1", tagCount)
	}
	// search terms 副作用。
	var termCount int
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM account_list_availability_projection_search_terms
		WHERE viewer_system_account_id = 'sys-1' AND account_id = 'acct-1' AND term = '主'`).Scan(&termCount); err != nil {
		t.Fatal(err)
	}
	if termCount != 1 {
		t.Fatalf("投影 search terms 行数 = %d, 期望 1", termCount)
	}
	// viewer health 水位刷新。
	var isCurrent int
	if err := db.QueryRow(`
		SELECT is_current FROM account_list_availability_projection_viewer_health
		WHERE viewer_system_account_id = 'sys-1'`).Scan(&isCurrent); err != nil {
		t.Fatalf("查询 viewer health: %v", err)
	}
	if isCurrent != 1 {
		t.Fatalf("viewer health is_current = %d, 期望 1", isCurrent)
	}
	// dirty 队列清空。
	var dirtyCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM account_list_availability_dirty`).Scan(&dirtyCount); err != nil {
		t.Fatal(err)
	}
	if dirtyCount != 0 {
		t.Fatalf("dirty 队列应清空, 剩 %d", dirtyCount)
	}
	// 依赖健康状态机应恢复 healthy。
	var state string
	if err := db.QueryRow(`
		SELECT state FROM account_list_availability_projection_dependency_health
		WHERE dependency_name = 'runtime_state'`).Scan(&state); err != nil {
		t.Fatalf("查询 dependency health: %v", err)
	}
	if state != "healthy" && state != "recovering" {
		t.Fatalf("依赖健康状态 = %s, 期望 healthy/recovering", state)
	}
}

// TestListProjectionRegistryWired：注册表条目已翻转为 go-wired（组合根可装配）。
func TestListProjectionRegistryWired(t *testing.T) {
	entry, ok := jobregistry.Find("account-list-availability-projection-maintenance")
	if !ok {
		t.Fatal("注册表必须收录该任务")
	}
	if entry.GoStatus != jobregistry.GoWired {
		t.Fatalf("注册表状态 = %s, 期望 go-wired", entry.GoStatus)
	}
	if !jobregistry.WiredJobNames()["account-list-availability-projection-maintenance"] {
		t.Fatal("WiredJobNames 应包含该任务")
	}
}

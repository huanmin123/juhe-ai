package circuitstore

// 投影 LoadItems 单元测试：SQLite 双模全链（seed → LoadItems → ProjectionItem
// 断言）+ 状态机分类纯函数。运行态源以 Mock 注入（fail-closed 与分类分支
// 可回放）。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
	_ "modernc.org/sqlite"
)

func newLoaderTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
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
			created_at TEXT, updated_at TEXT,
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

type stubCredentials struct{}

func (stubCredentials) DecryptCredentials(envelope string) (map[string]any, error) {
	var decoded map[string]any
	if err := json.Unmarshal([]byte(envelope), &decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func (stubCredentials) AccountAPIKeyEntries(credentials map[string]any) []APIKeyPoolEntry {
	return []APIKeyPoolEntry{}
}

type stubConcurrency struct {
	values map[string]int
	err    error
}

func (s stubConcurrency) LoadConcurrency(ctx context.Context, accountIDs []string) (map[string]int, error) {
	if s.err != nil {
		return nil, s.err
	}
	output := map[string]int{}
	for _, id := range accountIDs {
		output[id] = s.values[id]
	}
	return output, nil
}

type stubRuntime struct {
	values map[string]AccountRuntimeAvailability
	err    error
}

func (s stubRuntime) LoadRuntimeAvailability(ctx context.Context, runtimeKeys []string) (map[string]AccountRuntimeAvailability, error) {
	if s.err != nil {
		return nil, s.err
	}
	output := map[string]AccountRuntimeAvailability{}
	for _, key := range runtimeKeys {
		if value, found := s.values[key]; found {
			output[key] = value
		}
	}
	return output, nil
}

type stubTimezone struct{}

func (stubTimezone) StatsTimezone(ctx context.Context) (*time.Location, error) {
	return time.UTC, nil
}

func newTestLoader(business *sql.DB, concurrency ConcurrencySource, runtime RuntimeAvailabilitySource) *ProjectionItemLoader {
	loader, err := NewProjectionItemLoader(ProjectionLoadConfig{
		Business:             business,
		Stats:                business,
		Secret:               "test-secret",
		Credentials:          stubCredentials{},
		Concurrency:          concurrency,
		RuntimeAvailability:  runtime,
		Timezone:             stubTimezone{},
		Now:                  func() time.Time { return time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		panic(err)
	}
	return loader
}

const loaderNow = "2026-09-04T10:00:00.000Z"
const loaderFuture = "2026-09-05T10:00:00.000Z"

func seedOwnerAccount(t *testing.T, db *sql.DB) {
	t.Helper()
	exec := func(query string, args ...any) {
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
			client_compatibility, health_check_model, health_check_endpoint_mode, created_at, updated_at
		) VALUES (
			'acct-1', 3, 'sys-1', 'openai', 'profile_openai_openai_v1',
			'openai', 'v1', '主账户', 'api_key', 'active', 1,
			10, 5, 1, 0,
			'openai_standard', 'gpt-4o-mini', 'chat_completions', '2026-01-01T00:00:00.000Z', '2026-01-02T00:00:00.000Z')`)
}

func TestProjectionItemLoaderOwnerAccountBaseline(t *testing.T) {
	db := newLoaderTestDB(t)
	seedOwnerAccount(t, db)
	loader := newTestLoader(db, stubConcurrency{values: map[string]int{"acct-1": 4}}, stubRuntime{})
	items, err := loader.LoadItems(context.Background(), "sys-1", []string{"acct-1"})
	if err != nil {
		t.Fatalf("LoadItems: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items 长度 = %d, 期望 1", len(items))
	}
	item := items[0]
	if item.AccountID != "acct-1" || item.EffectiveStatus != "active" || item.CurrentConcurrency != 4 {
		t.Fatalf("投影列输入不符: %+v", item)
	}
	if item.EffectiveAvailable == nil || !*item.EffectiveAvailable {
		t.Fatalf("effectiveAvailable 应为 true")
	}
	if item.ProviderCode != "openai" || item.AccountType != "api_key" || item.Name != "主账户" {
		t.Fatalf("base 列不符: %+v", item)
	}
	if item.ConcurrencyLimit != 10 || item.Priority != 5 || !item.SuperPriorityEnabled || item.FallbackEnabled {
		t.Fatalf("排序键不符: %+v", item)
	}
	// payload 关键字段（AccountListItem 形状）。
	if payloadValue[string](t, item.Payload, "accessType") != "owner" {
		t.Fatalf("payload accessType 不符: %v", item.Payload)
	}
	if payloadValue[string](t, item.Payload, "status") != "active" {
		t.Fatalf("payload status 不符")
	}
	if value, ok := item.Payload["currentConcurrency"].(int); !ok || value != 4 {
		t.Fatalf("payload currentConcurrency 不符: %v", item.Payload["currentConcurrency"])
	}
	effective, ok := item.Payload["effectiveAvailability"].(map[string]any)
	if !ok || effective["available"] != true || effective["status"] != "available" {
		t.Fatalf("payload effectiveAvailability 不符: %v", item.Payload["effectiveAvailability"])
	}
	// role='user' 脱敏：systemAccountId/systemAccountName 不进 payload。
	if _, exists := item.Payload["systemAccountId"]; exists {
		t.Fatalf("payload 不应携带 systemAccountId（role user 脱敏）")
	}
	if _, exists := item.Payload["accountExpiresAt"]; exists {
		t.Fatalf("payload 不应携带 accountExpiresAt（Node hydrate 排除）")
	}
	todayUsage, ok := item.Payload["todayUsage"].(map[string]any)
	if !ok {
		t.Fatalf("payload todayUsage 缺失")
	}
	if payloadValue[float64](t, todayUsage, "requestCount") != 0 {
		t.Fatalf("空 usage 应为 0")
	}
}

func TestProjectionItemLoaderCooldownAccountClassifiedTemporaryUnavailable(t *testing.T) {
	db := newLoaderTestDB(t)
	seedOwnerAccount(t, db)
	if _, err := db.Exec(`UPDATE accounts SET cooldown_until = ? WHERE id = 'acct-1'`, loaderFuture); err != nil {
		t.Fatalf("seed cooldown: %v", err)
	}
	loader := newTestLoader(db, stubConcurrency{}, stubRuntime{})
	items, err := loader.LoadItems(context.Background(), "sys-1", []string{"acct-1"})
	if err != nil {
		t.Fatalf("LoadItems: %v", err)
	}
	item := items[0]
	if item.EffectiveStatus != "temporary_unavailable" {
		t.Fatalf("effectiveStatus = %s, 期望 temporary_unavailable", item.EffectiveStatus)
	}
	if *item.EffectiveAvailable {
		t.Fatalf("冷却账户 effectiveAvailable 应为 false")
	}
	found := false
	for _, candidate := range item.NextTransitionCandidates {
		if candidate == loaderFuture {
			found = true
		}
	}
	if !found {
		t.Fatalf("nextTransition 候选应包含 cooldown 边界 %s: %v", loaderFuture, item.NextTransitionCandidates)
	}
}

func TestProjectionItemLoaderMissingAccountFails(t *testing.T) {
	db := newLoaderTestDB(t)
	seedOwnerAccount(t, db)
	loader := newTestLoader(db, stubConcurrency{}, stubRuntime{})
	if _, err := loader.LoadItems(context.Background(), "sys-1", []string{"acct-missing"}); err == nil {
		t.Fatalf("缺失账户应报错（可见范围缺失）")
	}
}

func TestProjectionItemLoaderFailClosedOnRuntimeSource(t *testing.T) {
	db := newLoaderTestDB(t)
	seedOwnerAccount(t, db)
	loader := newTestLoader(db, stubConcurrency{err: errors.New("redis down")}, stubRuntime{})
	if _, err := loader.LoadItems(context.Background(), "sys-1", []string{"acct-1"}); err == nil {
		t.Fatalf("运行态读失败应 fail closed")
	}
	loader2 := newTestLoader(db, stubConcurrency{}, stubRuntime{err: errors.New("redis down")})
	if _, err := loader2.LoadItems(context.Background(), "sys-1", []string{"acct-1"}); err == nil {
		t.Fatalf("runtime availability 读失败应 fail closed")
	}
}

func TestAccountFilterStatusesClassification(t *testing.T) {
	available := effectiveAvailability{available: true, status: "available"}
	if got := accountFilterStatuses("active", &available, false); len(got) != 1 || got[0] != "active" {
		t.Fatalf("active+available 应分类为 active: %v", got)
	}
	cooldown := blockedAvailability("instance_cooldown", "", "", "", "", "")
	if got := accountFilterStatuses("active", &cooldown, false); len(got) != 1 || got[0] != "temporary_unavailable" {
		t.Fatalf("instance_cooldown 应分类为 temporary_unavailable: %v", got)
	}
	// derivedStatus 缺失（无派生状态）时 quota exceeded 生效（Node 同序）。
	if got := accountFilterStatuses("active", nil, true); len(got) != 1 || got[0] != "rate_limited" {
		t.Fatalf("quota exceeded（无派生状态）应分类为 rate_limited: %v", got)
	}
	degraded := effectiveAvailability{available: true, status: "runtime_degraded"}
	if got := accountFilterStatuses("active", &degraded, false); len(got) != 1 || got[0] != "active" {
		t.Fatalf("runtime_degraded 应分类为 active: %v", got)
	}
	unschedulable := blockedAvailability("instance_unschedulable", "", "", "", "", "")
	if got := accountFilterStatuses("active", &unschedulable, false); len(got) != 1 || got[0] != "disabled" {
		t.Fatalf("instance_unschedulable 应分类为 disabled: %v", got)
	}
	// status=active 且不可用且无派生状态：空集合 → 调用方抛错。
	unknownBlocked := effectiveAvailability{available: false, status: ""}
	if got := accountFilterStatuses("active", &unknownBlocked, false); len(got) != 0 {
		t.Fatalf("active+不可用+无派生应返回空集合: %v", got)
	}
}

func TestSchedulableBucketWithEffectiveAvailable(t *testing.T) {
	if opsjobs.SchedulableBucket("disabled", false) != "disabled" {
		t.Fatalf("disabled+不可用应为 disabled 桶")
	}
	if opsjobs.SchedulableBucket("disabled", true) != "enabled" {
		t.Fatalf("disabled+可用应为 enabled 桶")
	}
	if opsjobs.SchedulableBucket("rate_limited", true) != "cooling" {
		t.Fatalf("rate_limited 恒为 cooling 桶")
	}
}

func payloadValue[T any](t *testing.T, payload map[string]any, key string) T {
	t.Helper()
	value, ok := payload[key].(T)
	if !ok {
		var zero T
		t.Fatalf("payload[%s] 类型不符: %v (期望 %T)", key, payload[key], zero)
	}
	return value
}



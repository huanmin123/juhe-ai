package circuitstore

// listavailability_sources.go 的 SQLite 读路径测试：usage 汇总、quota 直接 +
// team 继承双检查、余额快照、api key 池运行态汇总、circuit 摘要账本读。
// 复用 loader fixture（stats 与 business 同库），credentials_encrypted 列由
// 本文件按需补齐（SQLite ALTER TABLE，不影响其他测试）。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// stubPoolCredentials 解码 {"keys":[...]} envelope 并按 keys 数量返回 key 池
// （loadAPIKeyRuntimeSummaries 的隔离与汇总依赖可 Mock）。
type stubPoolCredentials struct{}

func (stubPoolCredentials) DecryptCredentials(envelope string) (map[string]any, error) {
	var decoded map[string]any
	if err := json.Unmarshal([]byte(envelope), &decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func (stubPoolCredentials) AccountAPIKeyEntries(credentials map[string]any) []APIKeyPoolEntry {
	rawKeys, _ := credentials["keys"].([]any)
	entries := make([]APIKeyPoolEntry, 0, len(rawKeys))
	for index, key := range rawKeys {
		fingerprint, _ := key.(string)
		entries = append(entries, APIKeyPoolEntry{ID: fingerprint, Fingerprint: fingerprint, Index: index})
	}
	return entries
}

// newSourcesRow 构造最小 managementRow（owner 行默认，按需覆写授权面）。
func newSourcesRow(id string) managementRow {
	return managementRow{id: id, systemAccountID: "sys-1", ownerSystemAccountID: "sys-1"}
}

func authorizedSourcesRow(id string) managementRow {
	row := newSourcesRow(id)
	row.authorizationID.Valid = true
	row.authorizationID.String = "auth-" + id
	row.authorizationStatus.Valid = true
	row.authorizationStatus.String = "active"
	return row
}

// TestLoadUsageSummariesSQLite 覆盖 today/authorization total 双查询与
// authorized 行的 scope 提升。
func TestLoadUsageSummariesSQLite(t *testing.T) {
	db := newLoaderTestDB(t)
	loader := newTestLoader(db, stubConcurrency{}, stubRuntime{})
	ctx := context.Background()
	now := loader.now()
	timezone, err := loader.timezone.StatsTimezone(ctx)
	if err != nil {
		t.Fatal(err)
	}

	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, query)
		}
	}
	// owner 行：today 走 account scope。
	seed(`INSERT INTO usage_stats_daily (system_account_id, scope_type, scope_id, stat_date, request_count, input_tokens, output_tokens, total_cost_usd, last_used_at)
		VALUES ('sys-1', 'account', 'acc-1', '2026-09-04', 5, 100, 200, 0.5, '2026-09-04T09:00:00.000Z')`)
	// authorized 行：today/total 都走 account_authorization scope。
	seed(`INSERT INTO usage_stats_daily (system_account_id, scope_type, scope_id, stat_date, request_count, input_tokens, output_tokens, total_cost_usd, last_used_at)
		VALUES ('sys-1', 'account_authorization', 'auth-acc-2', '2026-09-04', 7, 1, 2, 0.7, '2026-09-04T08:00:00.000Z')`)
	seed(`INSERT INTO usage_stats_totals (system_account_id, scope_type, scope_id, request_count, input_tokens, output_tokens, total_cost_usd, last_used_at)
		VALUES ('sys-1', 'account_authorization', 'auth-acc-2', 70, 10, 20, 7.0, '2026-09-04T08:00:00.000Z')`)

	owner := newSourcesRow("acc-1")
	authorized := authorizedSourcesRow("acc-2")
	authorized.authorizationID.String = "auth-acc-2"
	today, totals, err := loader.loadUsageSummaries(ctx, []managementRow{owner, authorized}, now, timezone)
	if err != nil {
		t.Fatal(err)
	}
	if today["acc-1"].RequestCount != 5 || today["acc-1"].TotalTokens != 300 || today["acc-1"].TotalCost != 0.5 {
		t.Fatalf("owner today usage 不符: %+v", today["acc-1"])
	}
	if today["acc-1"].LastUsedAt != "2026-09-04T09:00:00.000Z" {
		t.Fatalf("last_used_at 应随行载入: %+v", today["acc-1"])
	}
	if today["acc-2"].RequestCount != 7 {
		t.Fatalf("authorized 行 today 应走 authorization scope: %+v", today["acc-2"])
	}
	if totals["acc-2"].RequestCount != 70 || totals["acc-2"].TotalCost != 7 {
		t.Fatalf("authorized total usage 不符: %+v", totals["acc-2"])
	}
	// owner 行无 authorization total（空 scope 集合不产生键）。
	if _, exists := totals["acc-1"]; exists {
		t.Fatalf("owner 行不应有 authorization total: %+v", totals)
	}
}

// TestLoadAuthorizationQuotaStatusSQLite 覆盖 direct limits 与 team 继承
// limits 双检查及 resetAt 回填。
func TestLoadAuthorizationQuotaStatusSQLite(t *testing.T) {
	db := newLoaderTestDB(t)
	loader := newTestLoader(db, stubConcurrency{}, stubRuntime{})
	ctx := context.Background()
	now := loader.now()
	timezone, err := loader.timezone.StatsTimezone(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, query)
		}
	}
	// acc-dir：direct daily limit 100，今日成本 150 → 超限。
	direct := authorizedSourcesRow("acc-dir")
	direct.authorizationLimitsJSON.Valid = true
	direct.authorizationLimitsJSON.String = `{"daily":{"enabled":true,"limit":100}}`
	// acc-team：无 direct 限额，team 继承 daily limit 1，team scope 成本 5 → 超限。
	team := authorizedSourcesRow("acc-team")
	team.authorizationEffectiveSourceTeamID.Valid = true
	team.authorizationEffectiveSourceTeamID.String = "team-1"
	seed(`INSERT INTO resource_authorizations (id, status, expires_at, limits_json, effective_source_type, effective_source_team_id, resource_type, resource_id)
		VALUES ('auth-acc-dir', 'active', NULL, '{}', 'manual', NULL, 'account', 'acc-dir'),
			('auth-acc-team', 'active', NULL, '{}', 'manual', 'team-1', 'account', 'acc-team')`)
	seed(`INSERT INTO resource_authorization_grants (id, resource_type, resource_id, grantee_type, grantee_team_id, status, expires_at, limits_json)
		VALUES ('grant-1', 'account', 'acc-team', 'team', 'team-1', 'active', NULL, '{"daily":{"enabled":true,"limit":1}}')`)
	seed(`INSERT INTO usage_stats_daily (system_account_id, scope_type, scope_id, stat_date, total_cost_usd)
		VALUES ('sys-1', 'account_authorization', 'auth-acc-dir', '2026-09-04', 150),
			('sys-1', 'account_authorization_team', 'acc-team:team-1', '2026-09-04', 5)`)

	exceeded, resetAt, err := loader.loadAuthorizationQuotaStatus(ctx, []managementRow{direct, team}, now, timezone)
	if err != nil {
		t.Fatal(err)
	}
	if exceeded["auth-acc-dir"] != true {
		t.Fatalf("direct 限额超限必须为 true: %v", exceeded)
	}
	if exceeded["auth-acc-team"] != true {
		t.Fatalf("team 继承限额超限必须为 true: %v", exceeded)
	}
	if resetAt["auth-acc-dir"] == "" || resetAt["auth-acc-team"] == "" {
		t.Fatalf("超限授权必须携带 resetAt: %v", resetAt)
	}

	// 未超限：成本低于限额。
	under := authorizedSourcesRow("acc-under")
	under.authorizationLimitsJSON.Valid = true
	under.authorizationLimitsJSON.String = `{"daily":{"enabled":true,"limit":100}}`
	exceeded, _, err = loader.loadAuthorizationQuotaStatus(ctx, []managementRow{under}, now, timezone)
	if err != nil {
		t.Fatal(err)
	}
	if exceeded["auth-acc-under"] != false {
		t.Fatalf("低于限额必须为 false: %v", exceeded)
	}

	// 无授权行：空结果（不报错）。
	owner := newSourcesRow("acc-owner")
	exceeded, resetAt, err = loader.loadAuthorizationQuotaStatus(ctx, []managementRow{owner}, now, timezone)
	if err != nil || len(exceeded) != 0 || len(resetAt) != 0 {
		t.Fatalf("无授权行应返回空结果: %v %v %v", exceeded, resetAt, err)
	}
}

// TestLoadQuotaCostsHourlyWindow 覆盖 hourly 自定义窗口成本查询。
func TestLoadQuotaCostsHourlyWindow(t *testing.T) {
	db := newLoaderTestDB(t)
	loader := newTestLoader(db, stubConcurrency{}, stubRuntime{})
	ctx := context.Background()
	now := loader.now()
	timezone, err := loader.timezone.StatsTimezone(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO usage_quota_hourly_windows (system_account_id, scope_type, scope_id, window_hours, total_cost_usd)
		VALUES ('sys-1', 'account_authorization', 'auth-acc-1', 2, 3.5)`); err != nil {
		t.Fatal(err)
	}
	hours := 2
	checks := []quotaCostCheck{{
		authorizationID: "auth-acc-1",
		systemAccountID: "sys-1",
		scopeType:       "account_authorization",
		scopeID:         "auth-acc-1",
		hourlyHours:     &hours,
	}}
	costs, err := loader.loadQuotaCosts(ctx, checks, now, timezone)
	if err != nil {
		t.Fatal(err)
	}
	value := costs[quotaCostKey("sys-1", "account_authorization", "auth-acc-1", &hours)]
	if value.Hourly != 3.5 {
		t.Fatalf("hourly 窗口成本不符: %+v", value)
	}
	// 无记录窗口保持 0，不报错（ErrNoRows 短路）。
	if value.Daily != 0 || value.Weekly != 0 || value.Monthly != 0 || value.Total != 0 {
		t.Fatalf("未种子化窗口应为 0: %+v", value)
	}
}

// TestLoadBalanceSnapshotRecordsSQLite 覆盖余额快照读取与非法 JSON 报错。
func TestLoadBalanceSnapshotRecordsSQLite(t *testing.T) {
	db := newLoaderTestDB(t)
	loader := newTestLoader(db, stubConcurrency{}, stubRuntime{})
	ctx := context.Background()
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, query)
		}
	}
	seed(`INSERT INTO account_usage_snapshots (account_id, kind, snapshot_json, next_refresh_after, updated_at)
		VALUES ('acc-1', 'relay_balance', '{"configRevision":4,"totalBalance":9}', '2026-09-04T10:00:00.000Z', '2026-09-04T09:00:00.000Z'),
			('acc-2', 'other_kind', '{"configRevision":1}', NULL, NULL)`)
	records, err := loader.loadBalanceSnapshotRecords(ctx, []string{"acc-1", "acc-2", "acc-missing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("只应读取 relay_balance 快照: %v", records)
	}
	record := records["acc-1"]
	if record == nil || record.snapshotRevision != 4 {
		t.Fatalf("configRevision 应从快照提取: %+v", record)
	}
	if record.nextRefreshAfter != "2026-09-04T10:00:00.000Z" || record.updatedAt != "2026-09-04T09:00:00.000Z" {
		t.Fatalf("快照时间列不符: %+v", record)
	}
	// 非法 snapshot_json → 报错（fail closed）。
	seed(`INSERT INTO account_usage_snapshots (account_id, kind, snapshot_json, next_refresh_after, updated_at)
		VALUES ('acc-bad', 'relay_balance', '{broken', NULL, NULL)`)
	if _, err := loader.loadBalanceSnapshotRecords(ctx, []string{"acc-bad"}); err == nil {
		t.Fatal("非法 snapshot_json 必须报错")
	}
	// 空列表 → 空 map（不发查询）。
	empty, err := loader.loadBalanceSnapshotRecords(ctx, nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("空输入应返回空: %v %v", empty, err)
	}
}

// TestLoadAPIKeyRuntimeSummariesSQLite 覆盖 key 池运行态汇总的主路径与跳过分支。
func TestLoadAPIKeyRuntimeSummariesSQLite(t *testing.T) {
	db := newLoaderTestDB(t)
	// loader fixture 的 accounts 表缺 credentials_encrypted 列，本测试补齐。
	if _, err := db.Exec(`ALTER TABLE accounts ADD COLUMN credentials_encrypted TEXT`); err != nil {
		t.Fatal(err)
	}
	loader := newTestLoader(db, stubConcurrency{}, stubRuntime{})
	// loader fixture 的 stubCredentials 恒返回空 key 池，汇总需要可配置池的
	// Mock，重建一个使用 stubPoolCredentials 的 loader。
	poolLoader, err := NewProjectionItemLoader(ProjectionLoadConfig{
		Business:            db,
		Stats:               db,
		Secret:              "test-secret",
		Credentials:         stubPoolCredentials{},
		Concurrency:         stubConcurrency{},
		RuntimeAvailability: stubRuntime{},
		Timezone:            stubTimezone{},
		Now:                 func() time.Time { return loader.now() },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, query)
		}
	}
	seed(`INSERT INTO accounts (id, config_revision, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version, name, type, status, credentials_encrypted, created_at, updated_at)
		VALUES ('acct-1', 1, 'sys-1', 'openai', 'p', 'openai', 'v1', '双key账户', 'api_key', 'active', '{"keys":["fp-1","fp-2"]}', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`)
	seed(`INSERT INTO account_api_key_runtime_states (id, account_id, key_fingerprint, status, next_probe_at, last_failure_at, last_error_code, last_error_message, last_error_trace_id, key_index)
		VALUES ('st-1', 'acct-1', 'fp-1', 'active', NULL, NULL, NULL, NULL, NULL, 0),
			('st-2', 'acct-1', 'fp-2', 'rate_limited', '2030-01-01T00:00:00.000Z', '2026-09-01T00:00:00.000Z', 'http_429', '限流', 'trace-9', 1)`)

	summaries, err := poolLoader.loadAPIKeyRuntimeSummaries(ctx, []string{"acct-1", "acct-missing"})
	if err != nil {
		t.Fatal(err)
	}
	summary := summaries["acct-1"]
	if summary == nil {
		t.Fatal("双 key 账户必须产出汇总")
	}
	if summary.Total != 2 || summary.Active != 1 || summary.Unavailable != 1 || summary.RateLimited != 1 {
		t.Fatalf("计数不符: %+v", summary)
	}
	if summary.AllUnavailable {
		t.Fatalf("仍有 active key 时不得标记全不可用: %+v", summary)
	}
	if summary.NextProbeAt != "2030-01-01T00:00:00.000Z" {
		t.Fatalf("rate_limited key 的 next_probe_at 应成为汇总 nextProbeAt: %+v", summary)
	}
	if summary.LastFailureAt != "2026-09-01T00:00:00.000Z" || summary.LastErrorCode != "http_429" || summary.LastTraceID != "trace-9" {
		t.Fatalf("最近失败字段不符: %+v", summary)
	}

	// 单 key 账户：不启用池隔离 → 无汇总。
	seed(`INSERT INTO accounts (id, config_revision, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version, name, type, status, credentials_encrypted, created_at, updated_at)
		VALUES ('acct-single', 1, 'sys-1', 'openai', 'p', 'openai', 'v1', '单key', 'api_key', 'active', '{"keys":["only-1"]}', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`)
	summaries, err = poolLoader.loadAPIKeyRuntimeSummaries(ctx, []string{"acct-single"})
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := summaries["acct-single"]; exists {
		t.Fatalf("单 key 池不应产出汇总: %v", summaries)
	}

	// 非法凭据 envelope：解密失败 continue（无汇总、不报错）。
	seed(`INSERT INTO accounts (id, config_revision, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version, name, type, status, credentials_encrypted, created_at, updated_at)
		VALUES ('acct-bad', 1, 'sys-1', 'openai', 'p', 'openai', 'v1', '坏凭据', 'api_key', 'active', '{broken', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`)
	summaries, err = poolLoader.loadAPIKeyRuntimeSummaries(ctx, []string{"acct-bad"})
	if err != nil {
		t.Fatalf("解密失败必须 continue 而非报错: %v", err)
	}
	if _, exists := summaries["acct-bad"]; exists {
		t.Fatalf("解密失败账户不得产出汇总: %v", summaries)
	}
}

// TestLoadCircuitSummariesSQLite 覆盖 circuit 账本读取、revision 围栏与
// >100 键上限。
func TestLoadCircuitSummariesSQLite(t *testing.T) {
	db := newLoaderTestDB(t)
	loader := newTestLoader(db, stubConcurrency{}, stubRuntime{})
	ctx := context.Background()
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, query)
		}
	}
	seed(`INSERT INTO accounts (id, dispatch_revision) VALUES ('src-dispatch', 5)`)
	seed(`INSERT INTO account_circuit_incidents (
			circuit_scope_key, account_id, account_runtime_key, scope_kind, incident_id, state,
			generation, dispatch_revision, transition_id, next_transition_at_ms, last_failure_class,
			created_at_ms, updated_at_ms)
		VALUES ('sk-open', 'src-dispatch', 'acc-open', 'account', 'in-1', 'OPEN', 1, 5, 'tr-1', 5000, 'http_502', 1, 200),
			('sk-half', 'src-dispatch', 'acc-open', 'account', 'in-2', 'HALF_OPEN', 1, 5, 'tr-2', 3000, NULL, 1, 100),
			('sk-closed', 'src-dispatch', 'acc-open', 'account', 'in-3', 'CLOSED', 1, 5, 'tr-3', NULL, NULL, 1, 50),
			('sk-stale', 'src-dispatch', 'acc-stale', 'account', 'in-4', 'OPEN', 1, 4, 'tr-4', NULL, NULL, 1, 60)`)

	summaries, err := loader.loadCircuitSummaries(ctx, []string{"acc-open", "acc-stale", "acc-quiet"}, loader.now())
	if err != nil {
		t.Fatal(err)
	}
	open := summaries["acc-open"]
	if open.Status != "avoided" || open.Reason != "http_502" {
		t.Fatalf("OPEN 优先级最高且 reason 取其 last_failure_class: %+v", open)
	}
	if open.Since == "" {
		t.Fatalf("since 应来自选中 incident 的 updatedAt: %+v", open)
	}
	if open.NextCheckAt == "" {
		t.Fatalf("nextCheckAt 取全部 incident 最早 next_transition_at_ms: %+v", open)
	}
	if summaries["acc-stale"].Status != "normal" {
		t.Fatalf("旧 revision incident 不得回放: %+v", summaries["acc-stale"])
	}
	if summaries["acc-quiet"].Status != "normal" {
		t.Fatalf("无 incident 键应为 normal: %+v", summaries["acc-quiet"])
	}

	// 超过 100 键必须报错。
	many := make([]string, 101)
	for i := range many {
		many[i] = "key-" + strings.Repeat("x", 1) + string(rune('a'+i%26)) + "-" + itoa(i)
	}
	if _, err := loader.loadCircuitSummaries(ctx, many, loader.now()); err == nil {
		t.Fatal("超过 100 键必须报错")
	}
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

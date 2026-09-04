package gatewayruntimecache

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// newSQLTestDB 建立隔离的 SQLite 内存库（MaxOpenConns(1)）并建业务表。
func newSQLTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:gatewayruntimecache-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	statements := []string{
		`CREATE TABLE system_settings (key TEXT PRIMARY KEY, value_json TEXT NOT NULL, system_account_id TEXT NOT NULL DEFAULT 'sys_admin')`,
		`CREATE TABLE system_accounts (id TEXT PRIMARY KEY, status TEXT NOT NULL, image_generation_enabled INTEGER NOT NULL DEFAULT 0, request_limits_json TEXT)`,
		`CREATE TABLE api_keys (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, route_strategy_id TEXT NOT NULL, key_hash TEXT NOT NULL, status TEXT NOT NULL, expires_at TEXT, quota_limits_json TEXT)`,
		`CREATE TABLE route_strategies (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, mode TEXT NOT NULL, config_json TEXT, status TEXT NOT NULL)`,
		`CREATE TABLE route_strategy_groups (id TEXT PRIMARY KEY, route_strategy_id TEXT NOT NULL, system_account_id TEXT NOT NULL, group_id TEXT NOT NULL, priority INTEGER NOT NULL, weight INTEGER, status TEXT NOT NULL, created_at TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE groups (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, provider_code TEXT NOT NULL, enabled INTEGER NOT NULL, group_type TEXT, scheduling_policy_json TEXT)`,
		`CREATE TABLE resource_authorizations (id TEXT PRIMARY KEY, resource_type TEXT NOT NULL, resource_id TEXT NOT NULL, grantee_system_account_id TEXT NOT NULL, status TEXT NOT NULL, effective_source_type TEXT, effective_source_team_id TEXT, expires_at TEXT, limits_json TEXT)`,
		`CREATE TABLE group_authorization_settings (authorization_id TEXT NOT NULL, system_account_id TEXT NOT NULL, group_id TEXT NOT NULL, enabled INTEGER NOT NULL, group_type TEXT, scheduling_policy_json TEXT)`,
		`CREATE TABLE response_inspection_policies (id TEXT PRIMARY KEY, name TEXT NOT NULL, enabled INTEGER NOT NULL, priority INTEGER NOT NULL, scope_type TEXT NOT NULL, protocol_code TEXT NOT NULL, provider_code TEXT, match_json TEXT NOT NULL, action TEXT NOT NULL, notes TEXT, created_at TEXT, updated_at TEXT)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	return db
}

// seedGatewaySettingsKeys 种子 GatewaySettings 投影所需的全部设置键。
// Node 契约：系统设置表必须携带全部键值（安装期种子），缺失值在投影层报错。
func seedGatewaySettingsKeys(t *testing.T, db *sql.DB) {
	t.Helper()
	keys := map[string]string{
		"gatewayTextRawBodyLimitMegabytes":          "8",
		"accountCircuitConfirmationFailuresRequired": "3",
		"defaultTemporaryUnschedulableMinutes":       "10",
		"temporaryUnschedulableRetryIntervalSeconds": "5",
		"temporaryUnschedulableRetryAttempts":        "3",
		"textFirstResponseTimeoutSeconds":            "60",
		"textStreamIdleTimeoutSeconds":               "30",
		"textUncommittedAttemptMaxLifetimeSeconds":   "300",
		"imageFirstResponseTimeoutSeconds":           "60",
		"imageStreamIdleTimeoutSeconds":              "30",
		"imageUncommittedAttemptMaxLifetimeSeconds":  "300",
		"imageRequestWallTimeoutSeconds":             "600",
		"noAvailableAccountWaitTimeoutSeconds":       "30",
		"streamFailureThresholdCount":                "3",
		"streamFailureThresholdWindowMinutes":        "5",
		"gatewayUserRequestLimitPerMinute":           "0",
		"gatewayUserRequestLimitPerDay":              "0",
		"gatewayUserRequestLimitPerWeek":             "0",
		"gatewayUserRequestLimitPerMonth":            "0",
		"usageStatsTimezone":                         `"UTC"`,
	}
	for key, value := range keys {
		setSetting(t, db, key, value)
	}
}

func setSetting(t *testing.T, db *sql.DB, key string, valueJSON string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO system_settings (key, value_json) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value_json = excluded.value_json`, key, valueJSON); err != nil {
		t.Fatalf("set setting %s: %v", key, err)
	}
}

func TestSQLReadGatewaySettingsProjection(t *testing.T) {
	db := newSQLTestDB(t)
	seedGatewaySettingsKeys(t, db)
	setSetting(t, db, "gatewayTextRawBodyLimitMegabytes", "16")
	setSetting(t, db, "usageStatsTimezone", `"Asia/Shanghai"`)
	setSetting(t, db, "gatewayUserRequestLimitPerMinute", "120")
	models, err := NewSQLReadModels(db, false, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := models.ReadGatewaySettings(context.Background())
	if err != nil {
		t.Fatalf("ReadGatewaySettings: %v", err)
	}
	if settings.GatewayTextRawBodyLimitMegabytes != 16 {
		t.Fatalf("megabytes = %d", settings.GatewayTextRawBodyLimitMegabytes)
	}
	if settings.UsageStatsTimezone != "Asia/Shanghai" {
		t.Fatalf("timezone = %s", settings.UsageStatsTimezone)
	}
	if settings.GatewayUserRequestLimitPerMinute == nil || *settings.GatewayUserRequestLimitPerMinute != 120 {
		t.Fatalf("perMinute = %+v", settings.GatewayUserRequestLimitPerMinute)
	}
	if !settings.StreamCircuitBreakerEnabled {
		t.Fatal("streamCircuitBreakerEnabled must be pinned true")
	}
}

func seedGatewayKeyFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	statements := []string{
		`INSERT INTO system_accounts (id, status) VALUES ('sys_owner', 'active')`,
		`INSERT INTO groups (id, system_account_id, provider_code, enabled) VALUES ('g1', 'sys_owner', 'gpt', 1)`,
		`INSERT INTO groups (id, system_account_id, provider_code, enabled) VALUES ('g2', 'sys_owner', 'gpt', 1)`,
		`INSERT INTO route_strategies (id, system_account_id, mode, status) VALUES ('rs1', 'sys_owner', 'normal', 'active')`,
		`INSERT INTO api_keys (id, system_account_id, route_strategy_id, key_hash, status) VALUES ('key1', 'sys_owner', 'rs1', '` + HashSecret("sk-fixture") + `', 'active')`,
		`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at) VALUES ('b2', 'rs1', 'sys_owner', 'g2', 2, 2, 'active', '2026-01-01T00:00:00.000Z')`,
		`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at) VALUES ('b1', 'rs1', 'sys_owner', 'g1', 1, NULL, 'active', '2026-01-01T00:00:00.000Z')`,
		`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at) VALUES ('b3', 'rs1', 'sys_owner', 'g_disabled', 3, 1, 'disabled', '2026-01-01T00:00:00.000Z')`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed fixture: %v\n%s", err, statement)
		}
	}
}

func TestSQLGatewayAPIKeyRowAndBindings(t *testing.T) {
	db := newSQLTestDB(t)
	seedGatewaySettingsKeys(t, db)
	seedGatewayKeyFixture(t, db)
	models, err := NewSQLReadModels(db, false, nil, seamAccountsSelector{results: map[string]OpenAIAccountsForGroupResult{}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	runtime, err := models.ReadGatewayRuntime(ctx, "sk-fixture")
	if err != nil {
		t.Fatalf("ReadGatewayRuntime: %v", err)
	}
	if runtime.APIKey == nil {
		t.Fatal("fixture key must validate")
	}
	if runtime.APIKey.ID != "key1" || runtime.APIKey.SelectedGroupID != "g1" {
		t.Fatalf("key row mismatch: %+v", runtime.APIKey)
	}
	// 绑定过滤：disabled 剔除；NULL 权重归一化为 1，有效权重保留。
	bindings := runtime.APIKey.GroupBindings
	if len(bindings) != 2 {
		t.Fatalf("bindings must drop disabled rows, got %d", len(bindings))
	}
	if bindings[0].Weight != 1 || bindings[1].Weight != 2 {
		t.Fatalf("weights must follow Node normalization, got %d/%d", bindings[0].Weight, bindings[1].Weight)
	}
	// config_json 缺失：normal_routing_config 为 undefined（Node parse → {}）。
	if runtime.APIKey.NormalRoutingConfig != nil {
		t.Fatalf("absent config must keep normal routing config undefined, got %+v", runtime.APIKey.NormalRoutingConfig)
	}

	// speed_first 配置保留完整对象；cost_first 归一为裸偏好。
	if _, err := db.Exec(`UPDATE route_strategies SET config_json = '{"normalRoutingConfig":{"schedulingPreference":"speed_first","firstByteDeadlineMs":20000,"speedFirstConfig":{"slowTriggerCount":3}}}' WHERE id = 'rs1'`); err != nil {
		t.Fatal(err)
	}
	updated, err := models.ReadGatewayRuntime(ctx, "sk-fixture")
	if err != nil {
		t.Fatalf("speed_first read: %v", err)
	}
	normalConfig := updated.APIKey.NormalRoutingConfig
	if normalConfig == nil || normalConfig.SchedulingPreference != "speed_first" {
		t.Fatalf("speed_first config mismatch: %+v", normalConfig)
	}
	if string(normalConfig.Raw) == "" || !strings.Contains(string(normalConfig.Raw), "firstByteDeadlineMs") {
		t.Fatalf("speed_first raw payload must be preserved, got %s", string(normalConfig.Raw))
	}
	clone := normalConfig.Clone()
	if string(clone.Raw) != string(normalConfig.Raw) {
		t.Fatal("speed_first clone must keep the raw payload")
	}
	if _, err := db.Exec(`UPDATE route_strategies SET config_json = '{"normalRoutingConfig":{"schedulingPreference":"cost_first"}}' WHERE id = 'rs1'`); err != nil {
		t.Fatal(err)
	}
	costFirst, err := models.ReadGatewayRuntime(ctx, "sk-fixture")
	if err != nil {
		t.Fatalf("cost_first read: %v", err)
	}
	if costFirst.APIKey.NormalRoutingConfig == nil || costFirst.APIKey.NormalRoutingConfig.SchedulingPreference != "cost_first" {
		t.Fatalf("cost_first config mismatch: %+v", costFirst.APIKey.NormalRoutingConfig)
	}
	if costFirst.APIKey.NormalRoutingConfig.Clone().SchedulingPreference != "cost_first" {
		t.Fatal("cost_first clone must collapse to the bare preference")
	}

	// 未知 key → 空运行时。
	empty, err := models.ReadGatewayRuntime(ctx, "sk-unknown")
	if err != nil {
		t.Fatalf("unknown key: %v", err)
	}
	if empty.APIKey != nil || len(empty.Accounts) != 0 {
		t.Fatalf("unknown key must yield the empty runtime, got %+v", empty.APIKey)
	}
	// 非 sk- 前缀 → nil。
	if _, err := models.ReadGatewayRuntime(ctx, "not-a-key"); err != nil {
		t.Fatalf("non-sk key: %v", err)
	}

	// 过期 key → nil。
	if _, err := db.Exec(`UPDATE api_keys SET expires_at = '2020-01-01T00:00:00.000Z' WHERE id = 'key1'`); err != nil {
		t.Fatal(err)
	}
	expired, err := models.ReadGatewayRuntime(ctx, "sk-fixture")
	if err != nil {
		t.Fatalf("expired key: %v", err)
	}
	if expired.APIKey != nil {
		t.Fatal("expired key must not validate")
	}
}

func TestSQLGroupUsageAccessOwnerAuthorizedAndDisabled(t *testing.T) {
	db := newSQLTestDB(t)
	models, err := NewSQLReadModels(db, false, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	statements := []string{
		`INSERT INTO groups (id, system_account_id, provider_code, enabled, group_type) VALUES ('g1', 'sys_owner', 'gpt', 1, 'personal')`,
		`INSERT INTO groups (id, system_account_id, provider_code, enabled, group_type) VALUES ('g_off', 'sys_owner', 'gpt', 0, 'personal')`,
		`INSERT INTO resource_authorizations (id, resource_type, resource_id, grantee_system_account_id, status, effective_source_type, effective_source_team_id, expires_at, limits_json)
			VALUES ('auth1', 'group', 'g1', 'sys_peer', 'active', 'team', 'team1', '2099-01-01T00:00:00.000Z', '{"daily":{"enabled":true,"limit":5}}')`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}

	// owner 短路。
	owner, err := models.ResolveGroupUsageAccessMetadata(ctx, "g1", "sys_owner")
	if err != nil {
		t.Fatalf("owner read: %v", err)
	}
	if owner == nil || owner.GroupAccessType != GroupAccessTypeOwner || owner.ProviderCode != "gpt" {
		t.Fatalf("owner access mismatch: %+v", owner)
	}
	// 授权访问。
	peer, err := models.ResolveGroupUsageAccessMetadata(ctx, "g1", "sys_peer")
	if err != nil {
		t.Fatalf("peer read: %v", err)
	}
	if peer == nil || peer.GroupAccessType != GroupAccessTypeAuthorized {
		t.Fatalf("peer access mismatch: %+v", peer)
	}
	if peer.GroupAuthorizationID == nil || *peer.GroupAuthorizationID != "auth1" {
		t.Fatalf("authorization id mismatch: %+v", peer.GroupAuthorizationID)
	}
	if peer.GroupAuthorizationQuotaLimited == nil || !*peer.GroupAuthorizationQuotaLimited {
		t.Fatalf("quota limited flag must follow the enabled daily limit: %+v", peer.GroupAuthorizationQuotaLimited)
	}
	if peer.GroupAuthorizationSourceType == nil || *peer.GroupAuthorizationSourceType != "team" {
		t.Fatalf("source type mismatch: %+v", peer.GroupAuthorizationSourceType)
	}
	// 本地授权设置禁用 → undefined。
	if _, err := db.Exec(`INSERT INTO group_authorization_settings (authorization_id, system_account_id, group_id, enabled) VALUES ('auth1', 'sys_peer', 'g1', 0)`); err != nil {
		t.Fatal(err)
	}
	disabled, err := models.ResolveGroupUsageAccessMetadata(ctx, "g1", "sys_peer")
	if err != nil {
		t.Fatalf("disabled read: %v", err)
	}
	if disabled != nil {
		t.Fatalf("disabled local settings must read as undefined, got %+v", disabled)
	}
	// 禁用组 → undefined。
	off, err := models.ResolveGroupUsageAccessMetadata(ctx, "g_off", "sys_owner")
	if err != nil {
		t.Fatalf("disabled group read: %v", err)
	}
	if off != nil {
		t.Fatalf("disabled group must read as undefined, got %+v", off)
	}
}

func TestSQLInspectionPoliciesForGateway(t *testing.T) {
	db := newSQLTestDB(t)
	models, err := NewSQLReadModels(db, false, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	seed := []string{
		`INSERT INTO response_inspection_policies (id, name, enabled, priority, scope_type, protocol_code, provider_code, match_json, action, updated_at)
		 VALUES ('stored_provider', 'stored provider', 1, 1, 'provider', 'openai', 'gpt', '{"errorCodes":["cyber_policy"]}', 'observe', '2026-01-02T00:00:00.000Z')`,
		`INSERT INTO response_inspection_policies (id, name, enabled, priority, scope_type, protocol_code, provider_code, match_json, action, updated_at)
		 VALUES ('stored_protocol', 'stored protocol', 1, 1, 'protocol', 'openai', NULL, '{"jsonPathsExists":["error"]}', 'observe', '2026-01-01T00:00:00.000Z')`,
		`INSERT INTO response_inspection_policies (id, name, enabled, priority, scope_type, protocol_code, provider_code, match_json, action, updated_at)
		 VALUES ('stored_disabled', 'disabled', 0, 0, 'protocol', 'openai', NULL, '{}', 'observe', '2026-01-03T00:00:00.000Z')`,
	}
	for _, statement := range seed {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed policies: %v\n%s", err, statement)
		}
	}

	// 协议层读取：默认规则（enabled 且协议匹配）在前，存储行按 priority/updated_at。
	protocol, err := models.ListActiveResponseInspectionPolicies(ctx, "openai", "")
	if err != nil {
		t.Fatalf("protocol read: %v", err)
	}
	if len(protocol) == 0 || protocol[0].ID != "default_openai_transient_precommit_error" {
		t.Fatalf("default rules must lead, got %+v", protocol)
	}
	foundStored := false
	afterDefaults := false
	for _, policy := range protocol {
		if policy.DefaultRule {
			continue
		}
		afterDefaults = true
		if policy.ID == "stored_protocol" && afterDefaults {
			foundStored = true
		}
		if policy.ID == "stored_disabled" {
			t.Fatal("disabled row must not appear")
		}
		if policy.ID == "stored_provider" {
			t.Fatal("provider-scoped row must not appear in protocol read")
		}
	}
	if !foundStored {
		t.Fatal("stored protocol row missing")
	}

	// 供应商层读取：Node 语义下协议层默认规则（providerCode === undefined）与
	// 协议层存储行都继续命中，供应商默认/存储行额外加入，禁用行不出现。
	provider, err := models.ListActiveResponseInspectionPolicies(ctx, "openai", "gpt")
	if err != nil {
		t.Fatalf("provider read: %v", err)
	}
	ids := map[string]bool{}
	for _, policy := range provider {
		ids[policy.ID] = true
		if policy.ID == "stored_disabled" {
			t.Fatal("disabled row must not appear")
		}
	}
	if !ids["default_gpt_cyber_policy"] || !ids["stored_provider"] || !ids["stored_protocol"] || !ids["default_openai_transient_precommit_error"] {
		t.Fatalf("provider scope read mismatch: %v", ids)
	}
	// 未知协议 → 空集。
	unknown, err := models.ListActiveResponseInspectionPolicies(ctx, "unknown", "")
	if err != nil {
		t.Fatalf("unknown protocol read: %v", err)
	}
	if len(unknown) != 0 {
		t.Fatalf("unknown protocol must yield empty, got %d", len(unknown))
	}
}

// seamAccountsSelector 是 AccountsSelector 的最小替身，用于 runtime 组合验证。
type seamAccountsSelector struct {
	results map[string]OpenAIAccountsForGroupResult
}

func (s seamAccountsSelector) ListOpenAIAccountsForGroupResult(ctx context.Context, groupID, systemAccountID string, opts OpenAIAccountsForGroupOptions) (OpenAIAccountsForGroupResult, error) {
	result, ok := s.results[groupID]
	if !ok {
		return OpenAIAccountsForGroupResult{Accounts: []OpenAIAccountSecret{}}, nil
	}
	return result, nil
}

func TestSQLReadGatewayRuntimeComposition(t *testing.T) {
	db := newSQLTestDB(t)
	seedGatewaySettingsKeys(t, db)
	models, err := NewSQLReadModels(db, false, nil, seamAccountsSelector{results: map[string]OpenAIAccountsForGroupResult{
		"g2": {Accounts: []OpenAIAccountSecret{testAccount("a2", "sys_owner")}},
	}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	seed := []string{
		`INSERT INTO system_accounts (id, status) VALUES ('sys_owner', 'active')`,
		`INSERT INTO groups (id, system_account_id, provider_code, enabled, group_type) VALUES ('g1', 'sys_owner', 'gpt', 1, 'personal')`,
		`INSERT INTO groups (id, system_account_id, provider_code, enabled, group_type) VALUES ('g2', 'sys_owner', 'gpt', 1, 'personal')`,
		`INSERT INTO route_strategies (id, system_account_id, mode, status) VALUES ('rs1', 'sys_owner', 'normal', 'active')`,
		`INSERT INTO api_keys (id, system_account_id, route_strategy_id, key_hash, status) VALUES ('key1', 'sys_owner', 'rs1', '` + HashSecret("sk-comp") + `', 'active')`,
		`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at) VALUES ('b1', 'rs1', 'sys_owner', 'g1', 1, 1, 'active', '2026-01-01T00:00:00.000Z')`,
		`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at) VALUES ('b2', 'rs1', 'sys_owner', 'g2', 2, 1, 'active', '2026-01-01T00:00:00.000Z')`,
	}
	for _, statement := range seed {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	ctx := context.Background()

	// g1 无可用账户 → 回退 g2。
	runtime, err := models.ReadGatewayRuntime(ctx, "sk-comp")
	if err != nil {
		t.Fatalf("composition read: %v", err)
	}
	if runtime.APIKey == nil || runtime.APIKey.SelectedGroupID != "g2" {
		t.Fatalf("composition must fall through to g2, got %+v", runtime.APIKey)
	}
	if len(runtime.Accounts) != 1 || runtime.Accounts[0].ID != "a2" {
		t.Fatalf("composition accounts mismatch: %+v", runtime.Accounts)
	}
	if runtime.GroupAccess == nil || runtime.GroupAccess.GroupOwnerSystemAccountID != "sys_owner" {
		t.Fatalf("composition group access mismatch: %+v", runtime.GroupAccess)
	}

	// 未接线账户选择器 → 明确报错（不静默返回空）。
	bare, err := NewSQLReadModels(db, false, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = bare.ReadGatewayRuntime(ctx, "sk-comp")
	if err == nil || !strings.Contains(err.Error(), "AccountsSelector") {
		t.Fatalf("unwired selector must fail fast, got %v", err)
	}

	// 动态模式返回静态形态（re-route 由 Service 的 Orderer 完成）。
	if _, err := db.Exec(`UPDATE route_strategies SET mode = 'round_robin' WHERE id = 'rs1'`); err != nil {
		t.Fatal(err)
	}
	dynamic, err := models.ReadGatewayRuntime(ctx, "sk-comp")
	if err != nil {
		t.Fatalf("dynamic read: %v", err)
	}
	if dynamic.GroupAccess != nil || len(dynamic.Accounts) != 0 {
		t.Fatalf("dynamic mode must return the static shape, got %+v", dynamic)
	}
	if dynamic.APIKey == nil || !IsDynamicRouteStrategyMode(dynamic.APIKey.RouteStrategyMode) {
		t.Fatalf("dynamic key row mismatch: %+v", dynamic.APIKey)
	}
}

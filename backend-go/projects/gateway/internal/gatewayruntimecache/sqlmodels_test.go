package gatewayruntimecache

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/apikeys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/settings"
	_ "modernc.org/sqlite"
)

// newSQLTestDB 建立隔离的 SQLite 内存库（MaxOpenConns(1)）并建业务表：
// ATTACH ':memory:' AS juhe_business 承载生产同款限定形式（juhe_business.*
// 表名 + $n 绑定），单连接保证 ATTACH 对所有查询可见。
func newSQLTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:gatewayruntimecache-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	statements := []string{
		`ATTACH ':memory:' AS juhe_business`,
		`CREATE TABLE juhe_business.system_settings (key TEXT PRIMARY KEY, value_json TEXT NOT NULL, system_account_id TEXT NOT NULL DEFAULT 'sys_admin')`,
		`CREATE TABLE juhe_business.system_accounts (id TEXT PRIMARY KEY, status TEXT NOT NULL, image_generation_enabled INTEGER NOT NULL DEFAULT 0, request_limits_json TEXT)`,
		`CREATE TABLE juhe_business.api_keys (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, route_strategy_id TEXT NOT NULL, key_hash TEXT NOT NULL, status TEXT NOT NULL, expires_at TEXT, quota_limits_json TEXT, purpose TEXT, scheduling_preference_id TEXT)`,
		// 调度偏好独立实体 P1（loadGatewayAPIKeyRow 六态解析的引用目标）。
		`CREATE TABLE juhe_business.scheduling_preferences (id TEXT PRIMARY KEY, system_account_id TEXT, name TEXT NOT NULL, chat_mode TEXT NOT NULL, chat_speed_first_config_json TEXT, built_in INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE juhe_business.route_strategies (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, mode TEXT NOT NULL, config_json TEXT, status TEXT NOT NULL)`,
		`CREATE TABLE juhe_business.route_strategy_groups (id TEXT PRIMARY KEY, route_strategy_id TEXT NOT NULL, system_account_id TEXT NOT NULL, group_id TEXT NOT NULL, priority INTEGER NOT NULL, weight INTEGER, status TEXT NOT NULL, created_at TEXT NOT NULL DEFAULT '')`,
		// groups 目标形态（供应商与上游协议面重构设计 §8，迁移窗口 shim 已按
		// A 路线一次性收敛撤除）：无 provider_code 列；owner 内名称唯一见
		// idx_groups_owner_name_unique（与权威 DDL 同名同列）。
		`CREATE TABLE juhe_business.groups (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, enabled INTEGER NOT NULL, group_type TEXT, group_kind TEXT NOT NULL DEFAULT 'standard', scheduling_policy_json TEXT)`,
		`CREATE TABLE juhe_business.super_group_members (id TEXT PRIMARY KEY, super_group_id TEXT NOT NULL, member_group_id TEXT NOT NULL, priority INTEGER NOT NULL, enabled INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE juhe_business.resource_authorizations (id TEXT PRIMARY KEY, resource_type TEXT NOT NULL, resource_id TEXT NOT NULL, grantee_system_account_id TEXT NOT NULL, status TEXT NOT NULL, effective_source_type TEXT, effective_source_team_id TEXT, expires_at TEXT, limits_json TEXT)`,
		`CREATE TABLE juhe_business.group_authorization_settings (authorization_id TEXT NOT NULL, system_account_id TEXT NOT NULL, group_id TEXT NOT NULL, enabled INTEGER NOT NULL, group_type TEXT, scheduling_policy_json TEXT)`,
		`CREATE TABLE juhe_business.response_inspection_policies (id TEXT PRIMARY KEY, name TEXT NOT NULL, enabled INTEGER NOT NULL, priority INTEGER NOT NULL, scope_type TEXT NOT NULL, protocol_code TEXT NOT NULL, provider_code TEXT, match_json TEXT NOT NULL, action TEXT NOT NULL, notes TEXT, created_at TEXT, updated_at TEXT)`,
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
	defaults := map[string]string{
		"gatewayTextRawBodyLimitMegabytes":           "8",
		"accountCircuitConfirmationFailuresRequired": "3",
		"gatewayUserRequestLimitPerMinute":           "0",
		"gatewayUserRequestLimitPerDay":              "0",
		"gatewayUserRequestLimitPerWeek":             "0",
		"gatewayUserRequestLimitPerMonth":            "0",
		"userAiAccountLimit":                         "100",
		"systemApiRateLimitIpReadPerMinute":          "600",
		"systemApiRateLimitIpReadBurstPer10Seconds":  "120",
		"systemApiRateLimitIpWritePerMinute":         "180",
		"systemApiRateLimitIpWriteBurstPer10Seconds": "40",
		"systemApiRateLimitUserReadPerMinute":        "300",
		"systemApiRateLimitUserWritePerMinute":       "120",
		"defaultTemporaryUnschedulableMinutes":       "10",
		"temporaryUnschedulableRetryIntervalSeconds": "5",
		"temporaryUnschedulableRetryAttempts":        "3",
		// 调度慢失败直切与墙钟收尾修复设计 §5（与 settings 种子默认同值）。
		"sameAccountRetryFastFailMaxMs":             "10000",
		"gatewayAttemptStartMinWindowMs":            "30000",
		"textFirstResponseTimeoutSeconds":           "60",
		"textNonStreamFirstResponseTimeoutSeconds":  "600",
		"textStreamIdleTimeoutSeconds":              "30",
		"textUncommittedAttemptMaxLifetimeSeconds":  "300",
		"imageFirstResponseTimeoutSeconds":          "60",
		"imageStreamIdleTimeoutSeconds":             "30",
		"imageUncommittedAttemptMaxLifetimeSeconds": "300",
		"imageRequestWallTimeoutSeconds":            "600",
		"audioFirstResponseTimeoutSeconds":          "120",
		"videoCreateTimeoutSeconds":                 "60",
		"noAvailableAccountWaitTimeoutSeconds":      "30",
		"streamFailureThresholdCount":               "3",
		"streamFailureThresholdWindowMinutes":       "5",
		// M5b realtime 会话生命周期键（Realtime 设计 §3；与 settings 种子默认同值）。
		"realtimeIdleTimeoutSeconds":                 "120",
		"realtimeMaxSessionSeconds":                  "1800",
		"realtimeMaxConnectionsPerApiKey":            "5",
		"operationLogRetentionDays":                  "365",
		"operationLogMaxChangesPerRecord":            "100",
		"statsAggregationIntervalSeconds":            "60",
		"statsAggregationBatchSize":                  "2000",
		"statsAggregationMaxBatchesPerRun":           "5",
		"usageHotWindowRefreshIntervalSeconds":       "600",
		"groupAccountStatsRefreshIntervalSeconds":    "60",
		"accountQualityRefreshIntervalSeconds":       "600",
		"accountQualityWindowMinutes":                "10",
		"accountHealthCheckIntervalHours":            "1",
		"accountHealthCheckJitterMinutes":            "10",
		"accountHealthCheckFailureThreshold":         "3",
		"cooldownAccountRetestIntervalSeconds":       "3",
		"cooldownAccountRetestMaxBackoffHours":       "12",
		"oauthAccessTokenRefreshIntervalSeconds":     "60",
		"oauthAccessTokenRefreshLeadSeconds":         "300",
		"oauthAccessTokenRefreshBatchSize":           "20",
		"oauthAccessTokenRefreshRetryBackoffSeconds": "300",
		"modelCheckRetentionDays":                    "30",
		"runtimeLogIndexRetentionDays":               "14",
		"publicApiLogRetentionDays":                  "30",
		"usageRecordRetentionDays":                   "30",
		"usageStatsTimezone":                         `"UTC"`,
		"usageStatsMinuteRetentionHours":             "48",
		"usageStatsHourlyRetentionDays":              "60",
		"usageStatsDailyRetentionDays":               "400",
		"usageStatsWeeklyRetentionWeeks":             "104",
		"usageStatsMonthlyRetentionMonths":           "24",
		"usageRankSnapshotRetentionDays":             "30",
		// 上游客户端版本覆盖（613f2c618 新增键）：空对象是校验器认可的合法默认。
		// 自动跟版键（2026-10-08）同形态默认。
		"upstreamClientVersionOverrides":     "{}",
		"upstreamClientVersionAutoOverrides": "{}",
	}
	for _, key := range settings.SystemSettingKeys {
		value, ok := defaults[key]
		if !ok {
			t.Fatalf("missing test default for system setting %s", key)
		}
		setSetting(t, db, key, value)
	}
}

func setSetting(t *testing.T, db *sql.DB, key string, valueJSON string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO juhe_business.system_settings (key, value_json) VALUES (?, ?)
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
	setSetting(t, db, "sameAccountRetryFastFailMaxMs", "5000")
	setSetting(t, db, "gatewayAttemptStartMinWindowMs", "20000")
	models, err := NewSQLReadModels(db, nil, nil, nil, nil)
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
	// 调度慢失败直切与墙钟收尾修复设计 §5：两键经 SQL 投影按行值读取。
	if settings.SameAccountRetryFastFailMaxMs != 5000 {
		t.Fatalf("sameAccountRetryFastFailMaxMs = %d", settings.SameAccountRetryFastFailMaxMs)
	}
	if settings.GatewayAttemptStartMinWindowMs != 20000 {
		t.Fatalf("gatewayAttemptStartMinWindowMs = %d", settings.GatewayAttemptStartMinWindowMs)
	}
}

func seedGatewayKeyFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	// P2 迁移完成世界：内置偏好行存在，普通 Key 一律绑定（NULL 属未迁移错误，
	// 读取链会显式失败），见 spc1 六态测试。
	statements := []string{
		`INSERT INTO juhe_business.system_accounts (id, status) VALUES ('sys_owner', 'active')`,
		`INSERT INTO juhe_business.groups (id, system_account_id, enabled) VALUES ('g1', 'sys_owner', 1)`,
		`INSERT INTO juhe_business.groups (id, system_account_id, enabled) VALUES ('g2', 'sys_owner', 1)`,
		`INSERT INTO juhe_business.route_strategies (id, system_account_id, mode, status) VALUES ('rs1', 'sys_owner', 'normal', 'active')`,
		`INSERT INTO juhe_business.scheduling_preferences (id, system_account_id, name, chat_mode, chat_speed_first_config_json, built_in, created_at, updated_at)
			VALUES ('` + apikeys.BuiltinSchedulingPreferenceID + `', NULL, '` + apikeys.BuiltinSchedulingPreferenceName + `', 'cost_first', NULL, 1, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`,
		`INSERT INTO juhe_business.api_keys (id, system_account_id, route_strategy_id, key_hash, status, scheduling_preference_id) VALUES ('key1', 'sys_owner', 'rs1', '` + HashSecret("sk-fixture") + `', 'active', '` + apikeys.BuiltinSchedulingPreferenceID + `')`,
		`INSERT INTO juhe_business.route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at) VALUES ('b2', 'rs1', 'sys_owner', 'g2', 2, 2, 'active', '2026-01-01T00:00:00.000Z')`,
		`INSERT INTO juhe_business.route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at) VALUES ('b1', 'rs1', 'sys_owner', 'g1', 1, NULL, 'active', '2026-01-01T00:00:00.000Z')`,
		`INSERT INTO juhe_business.route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at) VALUES ('b3', 'rs1', 'sys_owner', 'g_disabled', 3, 1, 'disabled', '2026-01-01T00:00:00.000Z')`,
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
	// 策略 config 解码的消费路径（P2）只剩 purpose=chat（会话路径保持现状）；
	// 普通 Key 的派发配置一律来自偏好合成，补一条 chat 键承载原断言。
	if _, err := db.Exec(`INSERT INTO juhe_business.api_keys (id, system_account_id, route_strategy_id, key_hash, status, purpose) VALUES ('key-chat', 'sys_owner', 'rs1', '` + HashSecret("sk-chat-row") + `', 'active', 'chat')`); err != nil {
		t.Fatal(err)
	}
	models, err := NewSQLReadModels(db, nil, seamAccountsSelector{results: map[string]OpenAIAccountsForGroupResult{}}, nil, nil)
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
	// P2 迁移完成世界：普通 Key 绑定内置偏好，派发配置为合成的裸 cost_first
	// 形（Raw nil），不再是策略 config（策略无 config 时也不为 undefined）。
	if runtime.APIKey.NormalRoutingConfig == nil || runtime.APIKey.NormalRoutingConfig.SchedulingPreference != "cost_first" || runtime.APIKey.NormalRoutingConfig.Raw != nil {
		t.Fatalf("migrated general key must carry synthesized bare cost_first, got %+v", runtime.APIKey.NormalRoutingConfig)
	}
	if runtime.APIKey.SchedulingPreferenceID != apikeys.BuiltinSchedulingPreferenceID {
		t.Fatalf("fixture key must be bound to the builtin preference: %+v", runtime.APIKey)
	}

	// 普通 Key：策略 config 变更不影响派发配置（P2 起策略调度字段不再是普通
	// Key 的配置来源；绑定合成始终覆盖）。
	if _, err := db.Exec(`UPDATE juhe_business.route_strategies SET config_json = '{"normalRoutingConfig":{"schedulingPreference":"speed_first","firstByteDeadlineMs":20000,"speedFirstConfig":{"slowTriggerCount":3}}}' WHERE id = 'rs1'`); err != nil {
		t.Fatal(err)
	}
	general, err := models.ReadGatewayRuntime(ctx, "sk-fixture")
	if err != nil {
		t.Fatalf("general read after strategy speed_first: %v", err)
	}
	if general.APIKey.NormalRoutingConfig == nil || general.APIKey.NormalRoutingConfig.SchedulingPreference != "cost_first" {
		t.Fatalf("strategy scheduling config must not leak into migrated general keys, got %+v", general.APIKey.NormalRoutingConfig)
	}

	// chat 键沿用策略 config：speed_first 配置保留完整对象；cost_first 归一为
	// 裸偏好（clone 语义不变）。
	chat, err := models.ReadGatewayRuntime(ctx, "sk-chat-row")
	if err != nil {
		t.Fatalf("chat speed_first read: %v", err)
	}
	normalConfig := chat.APIKey.NormalRoutingConfig
	if normalConfig == nil || normalConfig.SchedulingPreference != "speed_first" {
		t.Fatalf("chat speed_first config mismatch: %+v", normalConfig)
	}
	if string(normalConfig.Raw) == "" || !strings.Contains(string(normalConfig.Raw), "firstByteDeadlineMs") {
		t.Fatalf("speed_first raw payload must be preserved, got %s", string(normalConfig.Raw))
	}
	clone := normalConfig.Clone()
	if string(clone.Raw) != string(normalConfig.Raw) {
		t.Fatal("speed_first clone must keep the raw payload")
	}
	if _, err := db.Exec(`UPDATE juhe_business.route_strategies SET config_json = '{"normalRoutingConfig":{"schedulingPreference":"cost_first"}}' WHERE id = 'rs1'`); err != nil {
		t.Fatal(err)
	}
	chatCostFirst, err := models.ReadGatewayRuntime(ctx, "sk-chat-row")
	if err != nil {
		t.Fatalf("chat cost_first read: %v", err)
	}
	if chatCostFirst.APIKey.NormalRoutingConfig == nil || chatCostFirst.APIKey.NormalRoutingConfig.SchedulingPreference != "cost_first" {
		t.Fatalf("chat cost_first config mismatch: %+v", chatCostFirst.APIKey.NormalRoutingConfig)
	}
	if chatCostFirst.APIKey.NormalRoutingConfig.Clone().SchedulingPreference != "cost_first" {
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
	if _, err := db.Exec(`UPDATE juhe_business.api_keys SET expires_at = '2020-01-01T00:00:00.000Z' WHERE id = 'key1'`); err != nil {
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
	models, err := NewSQLReadModels(db, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	statements := []string{
		`INSERT INTO juhe_business.groups (id, system_account_id, enabled, group_type) VALUES ('g1', 'sys_owner', 1, 'personal')`,
		`INSERT INTO juhe_business.groups (id, system_account_id, enabled, group_type) VALUES ('g_off', 'sys_owner', 0, 'personal')`,
		`INSERT INTO juhe_business.resource_authorizations (id, resource_type, resource_id, grantee_system_account_id, status, effective_source_type, effective_source_team_id, expires_at, limits_json)
			VALUES ('auth1', 'group', 'g1', 'sys_peer', 'active', 'team', 'team1', '2099-01-01T00:00:00.000Z', '{"daily":{"enabled":true,"limit":5}}')`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}

	// owner 短路。groups.provider_code 列已随供应商解耦退役（设计 §8），
	// ProviderCode 过渡字段已随 B4 W2 删除。
	owner, err := models.ResolveGroupUsageAccessMetadata(ctx, "g1", "sys_owner")
	if err != nil {
		t.Fatalf("owner read: %v", err)
	}
	if owner == nil || owner.GroupAccessType != GroupAccessTypeOwner {
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
	if _, err := db.Exec(`INSERT INTO juhe_business.group_authorization_settings (authorization_id, system_account_id, group_id, enabled) VALUES ('auth1', 'sys_peer', 'g1', 0)`); err != nil {
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
	models, err := NewSQLReadModels(db, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	seed := []string{
		`INSERT INTO juhe_business.response_inspection_policies (id, name, enabled, priority, scope_type, protocol_code, provider_code, match_json, action, updated_at)
		 VALUES ('stored_provider', 'stored provider', 1, 1, 'provider', 'openai', 'gpt', '{"errorCodes":["cyber_policy"]}', 'observe', '2026-01-02T00:00:00.000Z')`,
		`INSERT INTO juhe_business.response_inspection_policies (id, name, enabled, priority, scope_type, protocol_code, provider_code, match_json, action, updated_at)
		 VALUES ('stored_protocol', 'stored protocol', 1, 1, 'protocol', 'openai', NULL, '{"jsonPathsExists":["error"]}', 'observe', '2026-01-01T00:00:00.000Z')`,
		`INSERT INTO juhe_business.response_inspection_policies (id, name, enabled, priority, scope_type, protocol_code, provider_code, match_json, action, updated_at)
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

// TestSQLVisibleModelsDirectoryEntries 验证 models 目录可见性查询的数据面
// （设计 §2.1/§6.1）：scope IN ('builtin','global','personal') 且
// status='active' 行按 (public_model, scope, system_account_id) 三元组返回；
// 非 active 行与非目录 scope 不返回。
func TestSQLVisibleModelsDirectoryEntries(t *testing.T) {
	db := newSQLTestDB(t)
	if _, err := db.Exec(`CREATE TABLE juhe_business.models (id TEXT PRIMARY KEY, public_model TEXT NOT NULL, scope TEXT NOT NULL, system_account_id TEXT, status TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	seed := []string{
		`INSERT INTO juhe_business.models (id, public_model, scope, system_account_id, status) VALUES ('m1', 'gpt-x', 'builtin', NULL, 'active')`,
		`INSERT INTO juhe_business.models (id, public_model, scope, system_account_id, status) VALUES ('m2', 'glm-air', 'global', NULL, 'active')`,
		`INSERT INTO juhe_business.models (id, public_model, scope, system_account_id, status) VALUES ('m3', 'my-model', 'personal', 'sys_owner', 'active')`,
		`INSERT INTO juhe_business.models (id, public_model, scope, system_account_id, status) VALUES ('m4', 'my-model', 'personal', 'sys_peer', 'active')`,
		`INSERT INTO juhe_business.models (id, public_model, scope, system_account_id, status) VALUES ('m5', 'my-model', 'personal', 'sys_owner', 'inactive')`,
	}
	for _, statement := range seed {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed models: %v\n%s", err, statement)
		}
	}
	models, err := NewSQLReadModels(db, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := models.ListVisibleModelsDirectoryEntries(context.Background())
	if err != nil {
		t.Fatalf("ListVisibleModelsDirectoryEntries: %v", err)
	}
	type key struct{ model, scope, owner string }
	want := map[key]bool{
		{"gpt-x", "builtin", ""}:              true,
		{"glm-air", "global", ""}:             true,
		{"my-model", "personal", "sys_owner"}: true,
		{"my-model", "personal", "sys_peer"}:  true,
	}
	if len(entries) != len(want) {
		t.Fatalf("entries = %d 条, want %d: %+v", len(entries), len(want), entries)
	}
	for _, entry := range entries {
		if !want[key{entry.PublicModel, entry.Scope, entry.SystemAccountID}] {
			t.Fatalf("意外目录行: %+v", entry)
		}
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
	models, err := NewSQLReadModels(db, nil, seamAccountsSelector{results: map[string]OpenAIAccountsForGroupResult{
		"g2": {Accounts: []OpenAIAccountSecret{testAccount("a2", "sys_owner")}},
	}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	seed := []string{
		`INSERT INTO juhe_business.system_accounts (id, status) VALUES ('sys_owner', 'active')`,
		`INSERT INTO juhe_business.groups (id, system_account_id, enabled, group_type) VALUES ('g1', 'sys_owner', 1, 'personal')`,
		`INSERT INTO juhe_business.groups (id, system_account_id, enabled, group_type) VALUES ('g2', 'sys_owner', 1, 'personal')`,
		`INSERT INTO juhe_business.route_strategies (id, system_account_id, mode, status) VALUES ('rs1', 'sys_owner', 'normal', 'active')`,
		`INSERT INTO juhe_business.scheduling_preferences (id, system_account_id, name, chat_mode, chat_speed_first_config_json, built_in, created_at, updated_at)
			VALUES ('` + apikeys.BuiltinSchedulingPreferenceID + `', NULL, '` + apikeys.BuiltinSchedulingPreferenceName + `', 'cost_first', NULL, 1, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`,
		`INSERT INTO juhe_business.api_keys (id, system_account_id, route_strategy_id, key_hash, status, scheduling_preference_id) VALUES ('key1', 'sys_owner', 'rs1', '` + HashSecret("sk-comp") + `', 'active', '` + apikeys.BuiltinSchedulingPreferenceID + `')`,
		`INSERT INTO juhe_business.route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at) VALUES ('b1', 'rs1', 'sys_owner', 'g1', 1, 1, 'active', '2026-01-01T00:00:00.000Z')`,
		`INSERT INTO juhe_business.route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at) VALUES ('b2', 'rs1', 'sys_owner', 'g2', 2, 1, 'active', '2026-01-01T00:00:00.000Z')`,
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
	bare, err := NewSQLReadModels(db, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = bare.ReadGatewayRuntime(ctx, "sk-comp")
	if err == nil || !strings.Contains(err.Error(), "AccountsSelector") {
		t.Fatalf("unwired selector must fail fast, got %v", err)
	}

	// 动态模式返回静态形态（re-route 由 Service 的 Orderer 完成）。
	if _, err := db.Exec(`UPDATE juhe_business.route_strategies SET mode = 'round_robin' WHERE id = 'rs1'`); err != nil {
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

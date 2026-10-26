package gatewayruntimecache

// w11d 覆盖补齐（三）：SQL 读模型（settings 投影错误臂、分组访问解析全分支、
// 运行态装载/绑定/限额/路由配置解码、检查策略行扫描、预热枚举）。
// 夹具经 ATTACH juhe_business 承载生产同款限定形式（juhe_business.* + $n）。

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/apikeys"
)

// newW11DModels 构造读模型（夹具表已按生产 juhe_business 限定形态建好），
// 覆盖 bind()/table() 契约与各查询在空库/缺行下的语义。
func newW11DModels(t *testing.T) (*SQLReadModels, *sql.DB) {
	t.Helper()
	db := newSQLTestDB(t)
	models, err := NewSQLReadModels(db, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return models, db
}

func TestW11DSQLBindAndTable(t *testing.T) {
	models, db := newW11DModels(t)
	ctx := context.Background()
	if got := models.table("api_keys"); got != "juhe_business.api_keys" {
		t.Fatalf("PG 表名 = %q", got)
	}
	if got := models.bind("a = ? AND b = ?"); got != "a = $1 AND b = $2" {
		t.Fatalf("PG 绑定 = %q", got)
	}
	// Node 契约：系统设置带全量安装期种子键（缺失值在投影层报错），runtime
	// 读链对 sk- 键恒经设置投影；这里的“空库”指业务表为空的合法生产形态。
	seedGatewaySettingsKeys(t, db)
	// 空业务表缺行不是错误：runtime 匿名、分组访问 nil、检查策略/预热枚举空集。
	runtime, err := models.ReadGatewayRuntime(ctx, "sk-pg")
	if err != nil || runtime.APIKey != nil {
		t.Fatalf("空库 runtime = %+v err=%v", runtime.APIKey, err)
	}
	if row, err := models.ReadGatewayRuntimeByKeyHash(ctx, "sk-pg"); err != nil || row.APIKey != nil {
		t.Fatalf("空库按 hash runtime = %+v err=%v", row.APIKey, err)
	}
	if meta, err := models.ResolveGroupUsageAccessMetadata(ctx, "g1", "sys"); err != nil || meta != nil {
		t.Fatalf("空库分组访问 = %v err=%v", meta, err)
	}
	// 空存储表下检查策略仍含内置默认规则（默认规则是代码静态集，见
	// TestSQLInspectionPoliciesForGateway 的 default rules must lead）。
	policies, err := models.ListActiveResponseInspectionPolicies(ctx, "openai", "gpt")
	if err != nil || len(policies) == 0 {
		t.Fatalf("空库检查策略 = %v err=%v", policies, err)
	}
	for _, policy := range policies {
		if !policy.DefaultRule {
			t.Fatalf("空库检查策略只允许内置默认规则，混入存储行: %+v", policy)
		}
	}
	if hashes, err := models.ListActiveGatewayAPIKeyHashes(ctx); err != nil || len(hashes) != 0 {
		t.Fatalf("空库预热枚举 = %v err=%v", hashes, err)
	}
	// 无 sk- 前缀匿名读取已在上面种子后的设置上覆盖；删除系统设置表后（绕开
	// 60s 快照缓存的）新读模型报错。
	plain, err := NewSQLReadModels(db, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	anonymous, err := plain.ReadGatewayRuntime(nil, "nope")
	if err != nil || anonymous.APIKey != nil {
		t.Fatalf("nil ctx 匿名读取 = %+v err=%v", anonymous.APIKey, err)
	}
	if _, err := db.Exec(`DROP TABLE juhe_business.system_settings`); err != nil {
		t.Fatal(err)
	}
	fresh, err := NewSQLReadModels(db, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.ReadGatewaySettings(ctx); err == nil {
		t.Fatal("设置表缺失必须报错")
	}
	if _, err := fresh.ReadGatewayRuntime(ctx, "nope"); err == nil {
		t.Fatal("设置投影失败必须传播到 runtime 匿名读取")
	}
}

func TestW11DProjectSettingsErrorArms(t *testing.T) {
	// 每个数值设置键的坏值与越界值都必须报各自的错误。
	cases := []struct {
		key  string
		bad  string
		over string
	}{
		{"gatewayTextRawBodyLimitMegabytes", `"x"`, "9999"},
		{"accountCircuitConfirmationFailuresRequired", "1.5", "99"},
		{"gatewayUserRequestLimitPerMinute", "[]", "2000000000"},
		{"gatewayUserRequestLimitPerDay", "1.5", "2000000000"},
		{"gatewayUserRequestLimitPerWeek", "{}", "2000000000"},
		{"gatewayUserRequestLimitPerMonth", "null", "2000000000"},
		{"defaultTemporaryUnschedulableMinutes", "0.5", "99999"},
		{"temporaryUnschedulableRetryIntervalSeconds", "x", "99999"},
		{"temporaryUnschedulableRetryAttempts", "x", "99"},
		{"textFirstResponseTimeoutSeconds", "x", "99999"},
		{"textStreamIdleTimeoutSeconds", "x", "99999"},
		{"textUncommittedAttemptMaxLifetimeSeconds", "x", "99999"},
		{"imageFirstResponseTimeoutSeconds", "x", "99999"},
		{"imageStreamIdleTimeoutSeconds", "x", "99999"},
		{"imageUncommittedAttemptMaxLifetimeSeconds", "x", "99999"},
		{"imageRequestWallTimeoutSeconds", "x", "99999"},
		{"noAvailableAccountWaitTimeoutSeconds", "x", "99999"},
		{"streamFailureThresholdCount", "x", "999"},
		{"streamFailureThresholdWindowMinutes", "x", "99999"},
	}
	db := newSQLTestDB(t)
	seedGatewaySettingsKeys(t, db)
	for _, tc := range cases {
		// 记录种子默认值，用例后恢复。
		var original string
		if err := db.QueryRow(`SELECT value_json FROM juhe_business.system_settings WHERE key = ?`, tc.key).Scan(&original); err != nil {
			t.Fatal(err)
		}
		// settings.Store 有 60s 快照缓存：每个用例重建读模型以绕开缓存。
		setSetting(t, db, tc.key, tc.bad)
		badModels, err := NewSQLReadModels(db, nil, nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := badModels.ReadGatewaySettings(context.Background()); err == nil {
			t.Fatalf("%s 坏值必须报错", tc.key)
		}
		setSetting(t, db, tc.key, tc.over)
		overModels, err := NewSQLReadModels(db, nil, nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := overModels.ReadGatewaySettings(context.Background()); err == nil {
			t.Fatalf("%s 越界必须报错", tc.key)
		}
		setSetting(t, db, tc.key, original)
	}
	// 时区键缺失：settings.Store 会先报缺少字段，投影层 else-UTC 分支用
	// 直接调用覆盖（raw 来自合法种子的副本）。
	tzModels, err := NewSQLReadModels(db, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := tzModels.settings.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	delete(raw, "usageStatsTimezone")
	projection, err := projectGatewaySettings(raw)
	if err != nil || projection.UsageStatsTimezone != "UTC" {
		t.Fatalf("时区缺失回退 = %q err=%v", projection.UsageStatsTimezone, err)
	}
}

func TestW11DResolveGroupUsageAccessArms(t *testing.T) {
	db := newSQLTestDB(t)
	seedGatewaySettingsKeys(t, db)
	models, err := NewSQLReadModels(db, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	seed := []string{
		`INSERT INTO juhe_business.system_accounts (id, status) VALUES ('sys_owner', 'active')`,
		`INSERT INTO juhe_business.system_accounts (id, status) VALUES ('sys_other', 'active')`,
		`INSERT INTO juhe_business.groups (id, system_account_id, enabled) VALUES ('g_missing_owner', '', 1)`,
		`INSERT INTO juhe_business.groups (id, system_account_id, enabled) VALUES ('g_disabled', 'sys_owner', 0)`,
		`INSERT INTO juhe_business.groups (id, system_account_id, enabled, group_type) VALUES ('g_badtype', 'sys_owner', 1, 'weird')`,
		`INSERT INTO juhe_business.groups (id, system_account_id, enabled, group_type) VALUES ('g_high_nopolicy', 'sys_owner', 1, 'high_concurrency')`,
		`INSERT INTO juhe_business.groups (id, system_account_id, enabled, group_type, scheduling_policy_json) VALUES ('g_high_badpolicy', 'sys_owner', 1, 'high_concurrency', 'not-json')`,
		`INSERT INTO juhe_business.groups (id, system_account_id, enabled, group_type, scheduling_policy_json) VALUES ('g_high', 'sys_owner', 1, 'high_concurrency', '{"maxConcurrency":5}')`,
		`INSERT INTO juhe_business.groups (id, system_account_id, enabled, group_type, scheduling_policy_json) VALUES ('g_authorized', 'sys_owner', 1, 'high_concurrency', '{"maxConcurrency":5}')`,
		`INSERT INTO juhe_business.groups (id, system_account_id, enabled, group_type, scheduling_policy_json) VALUES ('g_auth_disabled_local', 'sys_owner', 1, 'high_concurrency', '{"maxConcurrency":5}')`,
		`INSERT INTO juhe_business.groups (id, system_account_id, enabled, group_type, scheduling_policy_json) VALUES ('g_auth_overridden', 'sys_owner', 1, 'personal', NULL)`,
		`INSERT INTO juhe_business.resource_authorizations (id, resource_type, resource_id, grantee_system_account_id, status, limits_json) VALUES ('auth1', 'group', 'g_authorized', 'sys_other', 'active', NULL)`,
		`INSERT INTO juhe_business.resource_authorizations (id, resource_type, resource_id, grantee_system_account_id, status, limits_json) VALUES ('auth2', 'group', 'g_auth_disabled_local', 'sys_other', 'active', NULL)`,
		`INSERT INTO juhe_business.resource_authorizations (id, resource_type, resource_id, grantee_system_account_id, status, limits_json) VALUES ('auth3', 'group', 'g_auth_overridden', 'sys_other', 'active', '{"daily":{"enabled":true,"limit":10.5}}')`,
		`INSERT INTO juhe_business.group_authorization_settings (authorization_id, system_account_id, group_id, enabled) VALUES ('auth2', 'sys_other', 'g_auth_disabled_local', 0)`,
		`INSERT INTO juhe_business.group_authorization_settings (authorization_id, system_account_id, group_id, enabled, group_type, scheduling_policy_json) VALUES ('auth3', 'sys_other', 'g_auth_overridden', 1, 'high_concurrency', '{"maxConcurrency":9}')`,
	}
	for _, statement := range seed {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}

	// 未命中 → nil。
	missing, err := models.ResolveGroupUsageAccessMetadata(ctx, "g_none", "sys_owner")
	if err != nil || missing != nil {
		t.Fatalf("未命中 = %v err=%v", missing, err)
	}
	// 空 owner / 禁用分组 → nil。
	disabled, err := models.ResolveGroupUsageAccessMetadata(ctx, "g_disabled", "sys_owner")
	if err != nil || disabled != nil {
		t.Fatalf("禁用分组 = %v err=%v", disabled, err)
	}
	noOwner, err := models.ResolveGroupUsageAccessMetadata(ctx, "g_missing_owner", "sys_owner")
	if err != nil || noOwner != nil {
		t.Fatalf("空 owner = %v err=%v", noOwner, err)
	}
	// 坏 group_type → 错误。
	if _, err := models.ResolveGroupUsageAccessMetadata(ctx, "g_badtype", "sys_owner"); err == nil {
		t.Fatal("坏 group_type 必须报错")
	}
	// 高并发缺策略 → 错误。
	if _, err := models.ResolveGroupUsageAccessMetadata(ctx, "g_high_nopolicy", "sys_owner"); err == nil {
		t.Fatal("高并发缺策略必须报错")
	}
	// 高并发坏策略 JSON → 错误。
	if _, err := models.ResolveGroupUsageAccessMetadata(ctx, "g_high_badpolicy", "sys_owner"); err == nil {
		t.Fatal("高并发坏策略必须报错")
	}
	// owner 高并发：调度策略随行。
	owner, err := models.ResolveGroupUsageAccessMetadata(ctx, "g_high", "sys_owner")
	if err != nil || owner == nil || owner.GroupAccessType != GroupAccessTypeOwner || owner.SchedulingPolicy == nil {
		t.Fatalf("owner 高并发 = %+v err=%v", owner, err)
	}
	// 无授权的非 owner → nil。
	unauthorized, err := models.ResolveGroupUsageAccessMetadata(ctx, "g_authorized", "sys_stranger")
	if err != nil || unauthorized != nil {
		t.Fatalf("无授权 = %v err=%v", unauthorized, err)
	}
	// 授权命中：限额非启用（limits NULL）→ quotaLimited=false。
	authorized, err := models.ResolveGroupUsageAccessMetadata(ctx, "g_authorized", "sys_other")
	if err != nil || authorized == nil || authorized.GroupAccessType != GroupAccessTypeAuthorized || authorized.GroupAuthorizationID == nil {
		t.Fatalf("授权访问 = %+v err=%v", authorized, err)
	}
	if authorized.GroupAuthorizationQuotaLimited == nil || *authorized.GroupAuthorizationQuotaLimited {
		t.Fatal("无限额授权不得标记 quotaLimited")
	}
	// 本地禁用 → nil。
	locallyDisabled, err := models.ResolveGroupUsageAccessMetadata(ctx, "g_auth_disabled_local", "sys_other")
	if err != nil || locallyDisabled != nil {
		t.Fatalf("本地禁用 = %v err=%v", locallyDisabled, err)
	}
	// 本地覆盖：group_type 与调度策略覆盖 + 限额启用。
	overridden, err := models.ResolveGroupUsageAccessMetadata(ctx, "g_auth_overridden", "sys_other")
	if err != nil || overridden == nil {
		t.Fatalf("本地覆盖 = %+v err=%v", overridden, err)
	}
	if overridden.GroupType == nil || *overridden.GroupType != "high_concurrency" || overridden.SchedulingPolicy == nil {
		t.Fatalf("覆盖类型/策略 = %+v", overridden)
	}
	if overridden.GroupAuthorizationQuotaLimited == nil || !*overridden.GroupAuthorizationQuotaLimited {
		t.Fatal("启用限额必须标记 quotaLimited")
	}
	// 坏限额 JSON：按无限额处理。
	if _, err := db.Exec(`UPDATE juhe_business.resource_authorizations SET limits_json = 'not-json' WHERE id = 'auth1'`); err != nil {
		t.Fatal(err)
	}
	badLimits, err := models.ResolveGroupUsageAccessMetadata(ctx, "g_authorized", "sys_other")
	if err != nil || badLimits == nil || *badLimits.GroupAuthorizationQuotaLimited {
		t.Fatalf("坏限额 = %+v err=%v", badLimits, err)
	}
	// 纯助手：normalizeGroupTypeValue / parseGroupSchedulingPolicyJSON / strPtrIfSet。
	if value, err := normalizeGroupTypeValue(sql.NullString{}); err != nil || value != "personal" {
		t.Fatalf("空类型 = %q %v", value, err)
	}
	if value, err := normalizeGroupTypeValue(sql.NullString{String: "high_concurrency", Valid: true}); err != nil || value != "high_concurrency" {
		t.Fatalf("高并发类型 = %q %v", value, err)
	}
	if _, err := normalizeGroupTypeValue(sql.NullString{String: "odd", Valid: true}); err == nil {
		t.Fatal("未知类型必须报错")
	}
	if policy, err := parseGroupSchedulingPolicyJSON(sql.NullString{}, "personal"); err != nil || policy != nil {
		t.Fatalf("非高并发策略 = %v %v", policy, err)
	}
	if strPtrIfSet("") != nil || strPtrIfSet("x") == nil {
		t.Fatal("strPtrIfSet 臂错误")
	}
}

func TestW11DLoadGatewayAPIKeyByHashArms(t *testing.T) {
	db := newSQLTestDB(t)
	seedGatewaySettingsKeys(t, db)
	models, err := NewSQLReadModels(db, nil, seamAccountsSelector{results: map[string]OpenAIAccountsForGroupResult{}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	seed := []string{
		`INSERT INTO juhe_business.system_accounts (id, status) VALUES ('sys_owner', 'active')`,
		`INSERT INTO juhe_business.groups (id, system_account_id, enabled, group_type) VALUES ('g1', 'sys_owner', 1, 'personal')`,
		`INSERT INTO juhe_business.groups (id, system_account_id, enabled, group_type) VALUES ('g2', 'sys_owner', 1, 'personal')`,
		`INSERT INTO juhe_business.route_strategies (id, system_account_id, mode, status) VALUES ('rs1', 'sys_owner', 'normal', 'active')`,
		`INSERT INTO juhe_business.scheduling_preferences (id, system_account_id, name, chat_mode, chat_speed_first_config_json, built_in, created_at, updated_at)
			VALUES ('` + apikeys.BuiltinSchedulingPreferenceID + `', NULL, '` + apikeys.BuiltinSchedulingPreferenceName + `', 'cost_first', NULL, 1, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`,
		`INSERT INTO juhe_business.api_keys (id, system_account_id, route_strategy_id, key_hash, status, expires_at, scheduling_preference_id) VALUES ('key_badexp', 'sys_owner', 'rs1', '` + HashSecret("sk-w11d-badexp") + `', 'active', 'not-a-time', '` + apikeys.BuiltinSchedulingPreferenceID + `')`,
		`INSERT INTO juhe_business.api_keys (id, system_account_id, route_strategy_id, key_hash, status, expires_at, scheduling_preference_id) VALUES ('key_ok', 'sys_owner', 'rs1', '` + HashSecret("sk-w11d-ok") + `', 'active', NULL, '` + apikeys.BuiltinSchedulingPreferenceID + `')`,
		`INSERT INTO juhe_business.route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at) VALUES ('bw0', 'rs1', 'sys_owner', 'g1', 1, 0, 'active', '2026-01-01T00:00:00.000Z')`,
	}
	for _, statement := range seed {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}

	// 坏 expires_at → 错误。
	if _, err := models.ReadGatewayRuntimeByKeyHash(ctx, HashSecret("sk-w11d-badexp")); err == nil {
		t.Fatal("坏 expires_at 必须报错")
	}
	// 越界权重 → 错误。
	if _, err := models.ReadGatewayRuntimeByKeyHash(ctx, HashSecret("sk-w11d-ok")); err == nil || !strings.Contains(err.Error(), "权重") {
		t.Fatalf("越界权重必须报错: %v", err)
	}
	// loadGatewayAPIKeyByKeyHash：非 sk- 前缀 → nil。
	if row, err := models.loadGatewayAPIKeyByKeyHash(ctx, "nope"); err != nil || row != nil {
		t.Fatalf("非 sk- 键 = %v %v", row, err)
	}
	// 修正权重后正常装载 + NULL group_id 扫描错误。
	if _, err := db.Exec(`UPDATE juhe_business.route_strategy_groups SET weight = 3 WHERE id = 'bw0'`); err != nil {
		t.Fatal(err)
	}
	runtime, err := models.ReadGatewayRuntimeByKeyHash(ctx, HashSecret("sk-w11d-ok"))
	if err != nil || runtime.APIKey == nil || runtime.APIKey.GroupBindings[0].Weight != 3 {
		t.Fatalf("正常装载 = %+v err=%v", runtime.APIKey, err)
	}
	// 生产 PG 主键 NOT NULL，sqlite 时代的 NULL 绑定主键扫描臂不可达，已删。

	// 纯助手：权重归一、限额解析臂、路由配置解码臂。
	if _, err := normalizeAPIKeyGroupBindingWeight(0); err == nil {
		t.Fatal("权重 0 必须报错")
	}
	if weight, err := normalizeAPIKeyGroupBindingWeight(50); err != nil || weight != 50 {
		t.Fatalf("合法权重 = %d %v", weight, err)
	}
	limitsCases := []struct {
		raw  string
		want bool // 是否应解析出结构
	}{
		{`{"perMinute": 5, "expiresOn": "2030-01-01"}`, true},
		{`{"perMinute": 5.5}`, false},
		{`{"perMinute": -1}`, false},
		{`{"perMinute": 2000000001}`, false},
		{`{"perDay": 5}`, true},
		{`{"perWeek": 5}`, true},
		{`{"perMonth": 5}`, true},
		{`{"expiresOn": "2030-01-01"}`, false},
		{`{"other": 1}`, false},
	}
	for _, tc := range limitsCases {
		parsed := parseUserRequestLimitsJSON(sql.NullString{String: tc.raw, Valid: true})
		if tc.want && parsed == nil {
			t.Fatalf("限额 %s 必须解析", tc.raw)
		}
		if !tc.want && parsed != nil {
			t.Fatalf("限额 %s 必须拒绝", tc.raw)
		}
	}
	withExpiry := parseUserRequestLimitsJSON(sql.NullString{String: `{"perMinute": 5, "expiresOn": "2030-01-01"}`, Valid: true})
	if withExpiry == nil || withExpiry.ExpiresOn == nil || *withExpiry.ExpiresOn != "2030-01-01" {
		t.Fatalf("expiresOn = %+v", withExpiry)
	}
	// normal 配置：非对象 / 无键 / 缺偏好。
	if got := decodeNormalRoutingConfig(`{"normalRoutingConfig": "not-an-object"}`); got == nil || got.SchedulingPreference != "cost_first" {
		t.Fatalf("非对象配置 = %+v", got)
	}
	if got := decodeNormalRoutingConfig(`{}`); got == nil || got.SchedulingPreference != "cost_first" {
		t.Fatalf("缺键配置 = %+v", got)
	}
	if got := decodeNormalRoutingConfig(`{"normalRoutingConfig": {}}`); got == nil || got.SchedulingPreference != "cost_first" {
		t.Fatalf("缺偏好配置 = %+v", got)
	}
	speedy := decodeNormalRoutingConfig(`{"normalRoutingConfig": {"schedulingPreference": "speed_first", "extra": 1}}`)
	if speedy == nil || speedy.SchedulingPreference != "speed_first" || speedy.Raw == nil {
		t.Fatalf("speed_first 配置 = %+v", speedy)
	}
	// 混合配置 marshal 失败臂不可达（值来自已解码 JSON）；缺键臂已覆盖于 wh。
}

func TestW11DInspectionScanArms(t *testing.T) {
	// scanInspectionPolicyRow 各错误臂：直接注入 scan 函数返回值。
	if _, err := scanInspectionPolicyRow(func(dest ...any) error {
		return sql.ErrNoRows
	}); err == nil {
		t.Fatal("扫描失败必须报错")
	}
	// 造一个通过 scan 的基线参数序列，再逐字段破坏。
	// scan 列序：id, name, enabled, priority, scopeType, protocolCode,
	// providerCode(NullString), matchJSON, action, notes(NullString),
	// createdAt(NullString), updatedAt(NullString)。
	baseScan := func(scopeType, action, matchJSON string) func(...any) error {
		return func(dest ...any) error {
			strings_ := map[int]string{0: "p1", 1: "name", 4: scopeType, 5: "openai", 7: matchJSON, 8: action}
			ints := map[int]int{2: 1, 3: 1}
			for i := range dest {
				if s, ok := dest[i].(*string); ok {
					*s = strings_[i]
				} else if p, ok := dest[i].(*int); ok {
					*p = ints[i]
				} else if n, ok := dest[i].(*sql.NullString); ok {
					*n = sql.NullString{Valid: false}
				} else {
					return sql.ErrNoRows
				}
			}
			return nil
		}
	}
	if _, err := scanInspectionPolicyRow(baseScan("protocol", "observe", `{}`)); err != nil {
		t.Fatalf("合法行 = %v", err)
	}
	if _, err := scanInspectionPolicyRow(baseScan("weird", "observe", `{}`)); err == nil || !strings.Contains(err.Error(), "作用层级") {
		t.Fatalf("坏作用层级 = %v", err)
	}
	if _, err := scanInspectionPolicyRow(baseScan("protocol", "explode", `{}`)); err == nil || !strings.Contains(err.Error(), "动作") {
		t.Fatalf("坏动作 = %v", err)
	}
	if _, err := scanInspectionPolicyRow(baseScan("protocol", "observe", "not-json")); err == nil {
		t.Fatal("坏 match JSON 必须报错")
	}
	// decodeInspectionMatch：未知键报错、已知键列表收敛、非字符串项剔除。
	if _, err := decodeInspectionMatch(`{"unknownKey": []}`); err == nil {
		t.Fatal("未知键必须报错")
	}
	match, err := decodeInspectionMatch(`{"clientProfiles": ["a", 1, "b"], "errorCodes": []}`)
	if err != nil || len(match.ClientProfiles) != 2 {
		t.Fatalf("列表收敛 = %+v err=%v", match, err)
	}
	empty, err := decodeInspectionMatch("  ")
	if err != nil || empty.ClientProfiles != nil {
		t.Fatalf("空 match = %+v err=%v", empty, err)
	}
	// 协议归一：长度与白名单。
	if normalizeGatewayPolicyProtocolCode("") != "" || normalizeGatewayPolicyProtocolCode(strings.Repeat("x", 81)) != "" {
		t.Fatal("协议归一长度臂错误")
	}
	if normalizeGatewayPolicyProtocolCode("openai") != "openai" || normalizeGatewayPolicyProtocolCode("codex") != "" {
		t.Fatal("协议白名单错误")
	}
	// 预热：空 hash 跳过。
	db := newSQLTestDB(t)
	seedGatewaySettingsKeys(t, db)
	models, err := NewSQLReadModels(db, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO juhe_business.api_keys (id, system_account_id, route_strategy_id, key_hash, status) VALUES ('k_empty', 's', 'r', '', 'active')`); err != nil {
		t.Fatal(err)
	}
	hashes, err := models.ListActiveGatewayAPIKeyHashes(context.Background())
	if err != nil || len(hashes) != 0 {
		t.Fatalf("空 hash 跳过 = %v err=%v", hashes, err)
	}
	// 空协议策略：不查库直接返回默认空集。
	policies, err := models.ListActiveResponseInspectionPolicies(context.Background(), "  ", "")
	if err != nil || len(policies) != 0 {
		t.Fatalf("空协议 = %v err=%v", policies, err)
	}
}

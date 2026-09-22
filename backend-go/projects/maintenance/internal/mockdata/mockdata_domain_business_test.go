package mockdata

// business 域造数的验收测试：在隔离的临时数据根上应用 business schema + seed，
// 跑一次 seedBusiness，然后按可机器判定的口径核对结果。
//
// 三个口径：
//  1. coverage.go 里归属 business 域的关键状态断言必须全部满足（域已接线时它们
//     是硬门槛，不是提示）；
//  2. docs/functions/Mockdata造数设计.md「数据边界」要求的表必须都有行，且状态
//     样本清单（账户状态、授权状态、Key 级运行态、题库三态……）齐全；
//  3. 幂等与边界：重复执行不报错、不叠加；没有 schema/seed 的数据根上跳过而不是
//     失败；不得生成自授权样本。
//
// 所有写入都落在 t.TempDir() 下：测试绝不触碰 .local 或仓库内既有数据目录。

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
)

// businessTestNow 是测试固定的时间基准，让时间计划、过期样本与状态事件可复现。
var businessTestNow = time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC)

// businessTestRoot 建一个只含 business 库（schema + seed）的临时数据根。
func businessTestRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, fixedBusinessDatabase)
	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	if _, err := schema.EnsureSQLiteBusiness(ctx, db); err != nil {
		t.Fatalf("应用 business schema: %v", err)
	}
	if _, err := schema.SeedSQLiteDefaults(ctx, db, schema.SeedOptions{Now: func() time.Time { return businessTestNow }}); err != nil {
		t.Fatalf("seed business defaults: %v", err)
	}
	return dir
}

// businessTestEnv 在给定数据根上构建造数 env 并执行 seedBusiness（不跑清理与
// 其他域），把域结果记账进 env 后返回：覆盖断言因此按「已接线」评估。
func businessTestEnv(t *testing.T, dir string) (*env, DomainResult, *sql.DB) {
	t.Helper()
	paths, err := ResolvePaths(dir, "", envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(Options{Paths: paths, Days: 7, DailyRequests: 20, Now: businessTestNow}, nil)
	t.Cleanup(func() {
		if err := e.Close(); err != nil {
			t.Errorf("关闭 mockdata 存储: %v", err)
		}
	})
	result, err := seedBusiness(context.Background(), e)
	if err != nil {
		t.Fatalf("seedBusiness: %v", err)
	}
	e.recordDomainResult(result)
	db, err := e.open(StoreBusiness)
	if err != nil {
		t.Fatal(err)
	}
	return e, result, db
}

// businessCount 查询单表行数（失败即 Fatal，避免把查询错误当成 0 行）。
func businessCount(t *testing.T, db *sql.DB, query string) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(context.Background(), query).Scan(&count); err != nil {
		t.Fatalf("查询 %q: %v", query, err)
	}
	return count
}

// TestSeedBusinessSatisfiesCoverageAssertions 逐条核对 coverage.go 中归属
// business 域的关键状态断言：域已接线时它们就是硬门槛。
func TestSeedBusinessSatisfiesCoverageAssertions(t *testing.T) {
	e, result, db := businessTestEnv(t, businessTestRoot(t))
	if len(result.Counts) == 0 {
		t.Fatal("business 域必须返回非空 Counts，否则覆盖报告会把它当成未接线")
	}
	assertions := 0
	for _, assertion := range coverageAssertions() {
		if assertion.Domain != DomainBusiness {
			continue
		}
		assertions++
		if assertion.Store != StoreBusiness {
			t.Fatalf("business 断言应指向 %s：%s", StoreBusiness, assertion.Name)
		}
		if got := businessCount(t, db, assertion.Query); got < assertion.Min {
			t.Errorf("%s: 命中 %d 行，要求至少 %d 行", assertion.Name, got, assertion.Min)
		}
	}
	if assertions == 0 {
		t.Fatal("coverage.go 里必须存在 business 域断言（口径变更时本测试要同步）")
	}
	// 同一个 env 上跑覆盖校验：域已接线，business 断言必须进入评估而不是 notCovered。
	report, err := VerifyCoverage(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range report.NotCovered {
		if strings.Contains(item, "域 "+DomainBusiness) {
			t.Errorf("business 已接线但断言仍被标记未覆盖: %s", item)
		}
	}
}

// TestSeedBusinessRequiredTables 核对设计要求写入的表都有行、行数满足下限。
func TestSeedBusinessRequiredTables(t *testing.T) {
	_, _, db := businessTestEnv(t, businessTestRoot(t))
	cases := []struct {
		name  string
		query string
		min   int
	}{
		{name: "系统账户", query: "SELECT COUNT(*) FROM system_accounts", min: 8},
		{name: "会话", query: "SELECT COUNT(*) FROM system_sessions", min: 1},
		{name: "分组", query: "SELECT COUNT(*) FROM groups", min: 15},
		{name: "空分组样本", query: "SELECT COUNT(*) FROM groups WHERE enabled = 0", min: 1},
		{name: "高并发分组", query: "SELECT COUNT(*) FROM groups WHERE group_type = 'high_concurrency' AND scheduling_policy_json IS NOT NULL", min: 3},
		{name: "分组账户绑定", query: "SELECT COUNT(*) FROM group_accounts", min: 24},
		{name: "AI 账户", query: "SELECT COUNT(*) FROM accounts", min: 24},
		{name: "账户支持模型", query: "SELECT COUNT(*) FROM account_supported_models", min: 24},
		{name: "账户模型映射", query: "SELECT COUNT(*) FROM account_model_mappings", min: 2},
		{name: "模型映射启用样本", query: "SELECT COUNT(*) FROM account_model_mappings WHERE enabled = 1", min: 1},
		{name: "模型映射停用样本", query: "SELECT COUNT(*) FROM account_model_mappings WHERE enabled = 0", min: 1},
		{name: "账户标签", query: "SELECT COUNT(*) FROM account_tags", min: 5},
		{name: "标签绑定", query: "SELECT COUNT(*) FROM account_tag_bindings", min: 5},
		{name: "Key 级运行态", query: "SELECT COUNT(*) FROM account_api_key_runtime_states", min: 3},
		{name: "Key 级运行态状态种类", query: "SELECT COUNT(DISTINCT status) FROM account_api_key_runtime_states", min: 3},
		{name: "账户锁状态", query: "SELECT COUNT(*) FROM account_lock_states", min: 2},
		{name: "熔断事件", query: "SELECT COUNT(*) FROM account_circuit_incidents", min: 3},
		{name: "熔断 outbox", query: "SELECT COUNT(*) FROM account_circuit_outbox", min: 2},
		{name: "模型质量策略", query: "SELECT COUNT(*) FROM model_quality_policies", min: 2},
		{name: "模型质量计划", query: "SELECT COUNT(*) FROM model_quality_schedules", min: 2},
		{name: "质量隔离", query: "SELECT COUNT(*) FROM account_quality_enforcements", min: 1},
		{name: "账户级体检模型", query: "SELECT COUNT(*) FROM provider_default_health_check_models", min: 1},
		{name: "系统级体检模型", query: "SELECT COUNT(*) FROM provider_system_default_health_check_models", min: 1},
		{name: "账户测试任务", query: "SELECT COUNT(*) FROM account_test_tasks", min: 3},
		{name: "账户测试会话", query: "SELECT COUNT(*) FROM account_test_sessions", min: 2},
		{name: "账户测试会话任务", query: "SELECT COUNT(*) FROM account_test_session_tasks", min: 3},
		{name: "账户时间计划事件", query: "SELECT COUNT(*) FROM account_schedule_status_events", min: 3},
		{name: "账户名检索文档", query: "SELECT COUNT(*) FROM account_name_search_documents", min: 24},
		{name: "账户名检索词", query: "SELECT COUNT(*) FROM account_name_search_terms", min: 100},
		{name: "自定义模型", query: "SELECT COUNT(*) FROM custom_provider_models", min: 5},
		{name: "自定义模型状态种类", query: "SELECT COUNT(DISTINCT status) FROM custom_provider_models", min: 3},
		{name: "自定义模型 scope 种类", query: "SELECT COUNT(DISTINCT scope) FROM custom_provider_models", min: 2},
		{name: "路由策略", query: "SELECT COUNT(*) FROM route_strategies", min: 16},
		{name: "路由策略模式种类", query: "SELECT COUNT(DISTINCT mode) FROM route_strategies", min: 5},
		{name: "策略分组绑定", query: "SELECT COUNT(*) FROM route_strategy_groups", min: 16},
		{name: "停用策略绑定", query: "SELECT COUNT(*) FROM route_strategy_groups WHERE status = 'disabled'", min: 1},
		{name: "API Key", query: "SELECT COUNT(*) FROM api_keys", min: 16},
		{name: "API Key 时间计划", query: "SELECT COUNT(*) FROM api_keys WHERE availability_schedule_json IS NOT NULL", min: 2},
		{name: "API Key 过期样本", query: "SELECT COUNT(*) FROM api_keys WHERE expires_at IS NOT NULL", min: 1},
		{name: "API Key 停用样本", query: "SELECT COUNT(*) FROM api_keys WHERE status = 'disabled'", min: 1},
		{name: "API Key 已使用时间", query: "SELECT COUNT(*) FROM api_keys WHERE last_used_at IS NOT NULL", min: 10},
		{name: "额度窗口绑定", query: "SELECT COUNT(*) FROM request_quota_hourly_window_scope_bindings", min: 10},
		{name: "Key 时间计划事件", query: "SELECT COUNT(*) FROM api_key_schedule_status_events", min: 16},
		{name: "团队", query: "SELECT COUNT(*) FROM system_teams", min: 3},
		{name: "停用团队", query: "SELECT COUNT(*) FROM system_teams WHERE status = 'disabled'", min: 1},
		{name: "团队成员", query: "SELECT COUNT(*) FROM system_team_members", min: 5},
		{name: "历史成员", query: "SELECT COUNT(*) FROM system_team_members WHERE status = 'removed'", min: 1},
		{name: "授权运行时", query: "SELECT COUNT(*) FROM resource_authorizations", min: 20},
		{name: "授权来源", query: "SELECT COUNT(*) FROM resource_authorization_sources", min: 20},
		{name: "授权业务授予", query: "SELECT COUNT(*) FROM resource_authorization_grants", min: 20},
		{name: "团队授予", query: "SELECT COUNT(*) FROM resource_authorization_grants WHERE grantee_type = 'team'", min: 3},
		{name: "授权分组设置", query: "SELECT COUNT(*) FROM group_authorization_settings", min: 1},
		{name: "代理", query: "SELECT COUNT(*) FROM proxy_profiles", min: 3},
		{name: "带密码代理", query: "SELECT COUNT(*) FROM proxy_profiles WHERE password_encrypted IS NOT NULL", min: 1},
		{name: "停用代理", query: "SELECT COUNT(*) FROM proxy_profiles WHERE enabled = 0", min: 1},
		{name: "公告", query: "SELECT COUNT(*) FROM announcements", min: 5},
		{name: "公告已读", query: "SELECT COUNT(*) FROM announcement_reads", min: 3},
		{name: "响应检查策略", query: "SELECT COUNT(*) FROM response_inspection_policies", min: 3},
		{name: "响应检查停用样本", query: "SELECT COUNT(*) FROM response_inspection_policies WHERE enabled = 0", min: 1},
		{name: "外部来源系统", query: "SELECT COUNT(*) FROM external_integration_sources", min: 2},
		{name: "外部来源 Token", query: "SELECT COUNT(*) FROM external_integration_source_tokens", min: 3},
		{name: "外部来源停用 Token", query: "SELECT COUNT(*) FROM external_integration_source_tokens WHERE status = 'disabled'", min: 1},
		{name: "兼容文件", query: "SELECT COUNT(*) FROM openai_compatible_files", min: 1},
		{name: "向量库", query: "SELECT COUNT(*) FROM openai_compatible_vector_stores", min: 1},
		{name: "向量库文件", query: "SELECT COUNT(*) FROM openai_compatible_vector_store_files", min: 1},
		{name: "向量库分块", query: "SELECT COUNT(*) FROM openai_compatible_vector_store_chunks", min: 1},
		{name: "题库", query: "SELECT COUNT(*) FROM model_check_question_bank", min: 6},
		{name: "题库三态", query: "SELECT COUNT(DISTINCT status) FROM model_check_question_bank", min: 3},
		{name: "题库已通过", query: "SELECT COUNT(*) FROM model_check_question_bank WHERE status = 'approved' AND reviewed_by IS NOT NULL AND reviewed_at IS NOT NULL AND reference_answer <> '' AND key_points_json IS NOT NULL", min: 3},
		{name: "题库已驳回", query: "SELECT COUNT(*) FROM model_check_question_bank WHERE status = 'rejected' AND reject_reason IS NOT NULL", min: 1},
		{name: "OAuth 客户端", query: "SELECT COUNT(*) FROM oauth_clients", min: 2},
		{name: "OAuth 授权", query: "SELECT COUNT(*) FROM oauth_grants", min: 1},
		{name: "OAuth 授权码", query: "SELECT COUNT(*) FROM oauth_authorization_codes", min: 1},
		{name: "OAuth 访问令牌", query: "SELECT COUNT(*) FROM oauth_access_tokens", min: 1},
		{name: "OAuth 事务", query: "SELECT COUNT(*) FROM oauth_authorization_transactions", min: 1},
		{name: "OAuth 设备授权", query: "SELECT COUNT(*) FROM oauth_device_authorizations", min: 1},
		{name: "OAuth 签名密钥", query: "SELECT COUNT(*) FROM oauth_signing_keys", min: 1},
		{name: "OAuth 授权码 OIDC 上下文", query: "SELECT COUNT(*) FROM oauth_authorization_code_oidc_contexts", min: 1},
		{name: "授权账户实例", query: "SELECT COUNT(*) FROM accounts WHERE authorization_instance_source_account_id IS NOT NULL", min: 1},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			if got := businessCount(t, db, testCase.query); got < testCase.min {
				t.Fatalf("%s = %d，要求至少 %d", testCase.query, got, testCase.min)
			}
		})
	}
}

// TestSeedBusinessStatusSamples 核对状态样本清单：账户状态、授权状态、Key 级运行
// 态与时间计划启用/停用都必须同时存在。
func TestSeedBusinessStatusSamples(t *testing.T) {
	_, _, db := businessTestEnv(t, businessTestRoot(t))
	cases := []struct {
		name  string
		query string
	}{
		{name: "active", query: "SELECT COUNT(*) FROM accounts WHERE status = 'active'"},
		{name: "pending_test", query: "SELECT COUNT(*) FROM accounts WHERE status = 'pending_test'"},
		{name: "disabled", query: "SELECT COUNT(*) FROM accounts WHERE status = 'disabled'"},
		{name: "error", query: "SELECT COUNT(*) FROM accounts WHERE status = 'error'"},
		{name: "rate_limited", query: "SELECT COUNT(*) FROM accounts WHERE status = 'rate_limited'"},
		{name: "temporary_unavailable", query: "SELECT COUNT(*) FROM accounts WHERE status = 'temporary_unavailable'"},
		{name: "expired", query: "SELECT COUNT(*) FROM accounts WHERE status = 'expired'"},
		{name: "unschedulable", query: "SELECT COUNT(*) FROM accounts WHERE schedulable = 0 AND status = 'active'"},
		{name: "oauth 类型", query: "SELECT COUNT(*) FROM accounts WHERE type = 'oauth'"},
		{name: "api_key 类型", query: "SELECT COUNT(*) FROM accounts WHERE type = 'api_key'"},
		{name: "多 Key 池凭据", query: "SELECT COUNT(*) FROM accounts WHERE id LIKE 'mockdata_acc_multikey'"},
		{name: "图像生成模型", query: "SELECT COUNT(*) FROM account_supported_models WHERE model = 'mockdata-global-image'"},
		{name: "时间计划停用账户", query: "SELECT COUNT(*) FROM accounts WHERE availability_schedule_json LIKE '%dateRange%'"},
		{name: "账户时间计划启用样本", query: "SELECT COUNT(*) FROM accounts WHERE availability_schedule_json IS NOT NULL AND availability_schedule_json NOT LIKE '%dateRange%'"},
		{name: "授权 active", query: "SELECT COUNT(*) FROM resource_authorizations WHERE status = 'active'"},
		{name: "授权 paused", query: "SELECT COUNT(*) FROM resource_authorizations WHERE status = 'paused'"},
		{name: "授权 expired", query: "SELECT COUNT(*) FROM resource_authorizations WHERE status = 'expired'"},
		{name: "授权 revoked", query: "SELECT COUNT(*) FROM resource_authorizations WHERE status = 'revoked'"},
		{name: "授权 returned", query: "SELECT COUNT(*) FROM resource_authorizations WHERE status = 'returned'"},
		{name: "团队来源授权", query: "SELECT COUNT(*) FROM resource_authorizations WHERE effective_source_type = 'team'"},
		{name: "手动来源授权", query: "SELECT COUNT(*) FROM resource_authorizations WHERE effective_source_type = 'manual'"},
		{name: "账户资源授权", query: "SELECT COUNT(*) FROM resource_authorizations WHERE resource_type = 'account'"},
		{name: "分组资源授权", query: "SELECT COUNT(*) FROM resource_authorizations WHERE resource_type = 'group'"},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			if got := businessCount(t, db, testCase.query); got < 1 {
				t.Fatalf("%s 命中 0 行", testCase.query)
			}
		})
	}
}

// TestSeedBusinessNoSelfAuthorization 造数不得生成资源归属人 = 被授权人的授权。
func TestSeedBusinessNoSelfAuthorization(t *testing.T) {
	_, _, db := businessTestEnv(t, businessTestRoot(t))
	cases := []struct {
		name  string
		query string
	}{
		{name: "运行时授权", query: "SELECT COUNT(*) FROM resource_authorizations WHERE resource_owner_system_account_id = grantee_system_account_id"},
		{name: "业务授予（系统账户）", query: "SELECT COUNT(*) FROM resource_authorization_grants WHERE grantee_type = 'system_account' AND resource_owner_system_account_id = grantee_system_account_id"},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			if got := businessCount(t, db, testCase.query); got != 0 {
				t.Fatalf("%s = %d，不得生成自授权样本", testCase.query, got)
			}
		})
	}
}

// TestSeedBusinessGroupSchedulingPolicyStrictShape 高并发分组的调度策略必须是
// 完整 16 键文档：gateway 的读路径对 high_concurrency 分组做严格校验。
func TestSeedBusinessGroupSchedulingPolicyStrictShape(t *testing.T) {
	_, _, db := businessTestEnv(t, businessTestRoot(t))
	rows, err := db.QueryContext(context.Background(),
		"SELECT name, scheduling_policy_json FROM groups WHERE group_type = 'high_concurrency'")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var name, raw string
		if err := rows.Scan(&name, &raw); err != nil {
			t.Fatal(err)
		}
		seen++
		var policy map[string]any
		if err := json.Unmarshal([]byte(raw), &policy); err != nil {
			t.Fatalf("%s 的调度策略不是合法 JSON: %v", name, err)
		}
		for _, key := range []string{
			"mode", "defaultSoftConcurrency", "fastFirstEnabled", "fallbackOnQueueEnabled",
			"breakAffinityOnSoftLimit", "breakAffinityOnQueueWaitMs", "slowRequestThresholdMs",
			"firstOutputSlowThresholdMs", "recentTimeoutWindowSeconds", "recentTimeoutPenaltyThreshold",
			"maxQueueWaitMs", "maxQueueSize", "perApiKeyQueueLimit", "clientIpConcurrencyLimit",
			"clientIpConcurrencyOverflowMode", "imageLaneMaxConcurrency",
		} {
			if _, ok := policy[key]; !ok {
				t.Errorf("%s 的调度策略缺少字段 %s", name, key)
			}
		}
		if policy["mode"] != "balanced_fast" {
			t.Errorf("%s 的调度策略 mode = %v，要求 balanced_fast", name, policy["mode"])
		}
		if policy["clientIpConcurrencyOverflowMode"] != "queue" && policy["clientIpConcurrencyOverflowMode"] != "reject" {
			t.Errorf("%s 的 overflowMode = %v", name, policy["clientIpConcurrencyOverflowMode"])
		}
	}
	if seen == 0 {
		t.Fatal("至少要有一个高并发分组样本")
	}
}

// TestSeedBusinessJSONColumnsValid 造数写入的 JSON 列必须可解析：读路径对这些列
// 做严格解析，写坏一行就会让对应页面整体报错。
func TestSeedBusinessJSONColumnsValid(t *testing.T) {
	_, _, db := businessTestEnv(t, businessTestRoot(t))
	cases := []struct {
		name  string
		query string
	}{
		{name: "账户时间计划", query: "SELECT availability_schedule_json FROM accounts WHERE availability_schedule_json IS NOT NULL"},
		{name: "Key 时间计划", query: "SELECT availability_schedule_json FROM api_keys WHERE availability_schedule_json IS NOT NULL"},
		{name: "Key 额度窗口", query: "SELECT quota_limits_json FROM api_keys WHERE quota_limits_json IS NOT NULL"},
		{name: "授权额度", query: "SELECT limits_json FROM resource_authorizations WHERE limits_json IS NOT NULL"},
		{name: "响应检查匹配", query: "SELECT match_json FROM response_inspection_policies"},
		{name: "来源 scopes", query: "SELECT scopes_json FROM external_integration_sources"},
		{name: "来源限频", query: "SELECT rate_limits_json FROM external_integration_sources"},
		{name: "题库要点", query: "SELECT key_points_json FROM model_check_question_bank WHERE key_points_json IS NOT NULL"},
		{name: "签名密钥 JWK", query: "SELECT public_jwk_json FROM oauth_signing_keys"},
		{name: "向量库元数据", query: "SELECT metadata_json FROM openai_compatible_vector_stores"},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			rows, err := db.QueryContext(context.Background(), testCase.query)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			count := 0
			for rows.Next() {
				var raw string
				if err := rows.Scan(&raw); err != nil {
					t.Fatal(err)
				}
				count++
				if !json.Valid([]byte(raw)) {
					t.Fatalf("%s 不是合法 JSON: %s", testCase.query, raw)
				}
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if count == 0 {
				t.Fatalf("%s 没有可校验的行", testCase.query)
			}
		})
	}
	// 凭据信封必须是 accountcrypto 的 v1 格式，否则 gateway 解不开。
	var sealed string
	if err := db.QueryRowContext(context.Background(),
		"SELECT credentials_encrypted FROM accounts LIMIT 1").Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sealed, "v1:") {
		t.Fatalf("账户凭据必须是 v1 信封: %s", sealed)
	}
}

// TestSeedBusinessIdempotent 重复执行不报错且不叠加：确定性主键 + INSERT OR
// REPLACE 让第二次执行覆盖同一批行。
func TestSeedBusinessIdempotent(t *testing.T) {
	dir := businessTestRoot(t)
	_, first, db := businessTestEnv(t, dir)
	tables := []string{
		"system_accounts", "groups", "group_accounts", "accounts", "account_tags",
		"account_tag_bindings", "account_supported_models", "account_model_mappings",
		"route_strategies", "route_strategy_groups", "api_keys",
		"resource_authorizations", "resource_authorization_sources",
		"resource_authorization_grants", "system_teams", "system_team_members",
		"announcements", "announcement_reads", "custom_provider_models",
		"model_check_question_bank", "oauth_clients", "account_name_search_documents",
	}
	before := map[string]int{}
	for _, table := range tables {
		before[table] = businessCount(t, db, "SELECT COUNT(*) FROM "+table)
	}
	paths, err := ResolvePaths(dir, "", envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	second := newEnv(Options{Paths: paths, Days: 7, DailyRequests: 20, Now: businessTestNow}, nil)
	defer second.Close()
	again, err := seedBusiness(context.Background(), second)
	if err != nil {
		t.Fatalf("第二次 seedBusiness: %v", err)
	}
	if len(again.Counts) != len(first.Counts) {
		t.Fatalf("两次执行的计数键不一致: %d vs %d", len(first.Counts), len(again.Counts))
	}
	for _, table := range tables {
		if got := businessCount(t, db, "SELECT COUNT(*) FROM "+table); got != before[table] {
			t.Errorf("重复执行后 %s 行数 %d，期望 %d（不得叠加）", table, got, before[table])
		}
	}
}

// TestSeedBusinessSkipsWithoutSchema 没有 schema / seed 的数据根必须安全跳过：
// 返回空计数（覆盖报告据此记为未接线）而不是让整个造数失败。
func TestSeedBusinessSkipsWithoutSchema(t *testing.T) {
	dir := t.TempDir()
	paths, err := ResolvePaths(dir, "", envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(Options{Paths: paths, Days: 1, DailyRequests: 1, Now: businessTestNow}, nil)
	defer e.Close()
	result, err := seedBusiness(context.Background(), e)
	if err != nil {
		t.Fatalf("缺少 schema 时不得失败: %v", err)
	}
	if len(result.Counts) != 0 {
		t.Fatalf("缺少 schema 时必须返回空计数，实际 %v", result.Counts)
	}
	if result.Name != DomainBusiness {
		t.Fatalf("域名为 %q，期望 %q", result.Name, DomainBusiness)
	}
}

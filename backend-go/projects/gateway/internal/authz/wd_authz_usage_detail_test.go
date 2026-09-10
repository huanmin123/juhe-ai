package authz

// authz 单授权用量明细补充测试（wd_ 前缀，独占新增）：usage_detail.go 的
// 直授/团队两条读链（窗口行水合、成员分页、团队窗口摘要、分组/实例回退），
// 加上访问范围矩阵与纯函数契约。构造沿用既有 usageFixture（stats 句柄 +
// UTC 时区源）。

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

func wdInsertScopeWindow(t *testing.T, f *fixture, systemAccountID, scopeType, scopeID, start, end string, requestCount, inputTokens, outputTokens, totalCost float64, lastUsedAt any) {
	t.Helper()
	_, err := f.db.Exec(`INSERT INTO usage_scope_range_windows
		(system_account_id, scope_type, scope_id, start_date, end_date,
		 request_count, input_tokens, output_tokens, total_cost_usd, last_used_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '2026-09-06T00:00:00.000Z')`,
		systemAccountID, scopeType, scopeID, start, end, requestCount, inputTokens, outputTokens, totalCost, lastUsedAt)
	if err != nil {
		t.Fatalf("seed scope window: %v", err)
	}
}

func wdInsertTeamFilterWindow(t *testing.T, f *fixture, systemAccountID, teamID, resourceType, resourceID string, requestCount, totalCost float64, lastUsedAt any) {
	t.Helper()
	_, err := f.db.Exec(`INSERT INTO authorization_team_usage_range_windows
		(system_account_id, start_date, end_date, team_filter_id, resource_filter_type, resource_filter_id,
		 request_count, total_cost_usd, last_used_at, updated_at)
		VALUES (?, '2026-08-08', '2026-09-06', ?, ?, ?, ?, ?, ?, '2026-09-06T00:00:00.000Z')`,
		systemAccountID, teamID, resourceType, resourceID, requestCount, totalCost, lastUsedAt)
	if err != nil {
		t.Fatalf("seed team window: %v", err)
	}
}

var wdDetailRange = UsageStatsRange{StartDate: "2026-08-08", EndDate: "2026-09-06", Days: 31, MaxDays: 31}

func TestWdUsageDetailDirectGrant(t *testing.T) {
	f := newUsageFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedAccount(t, "member", "active")
	f.seedGroup(t, "grp_d", "owner")
	created, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_d",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	runtimeID := wdRuntimeID(t, f, "group", "grp_d", "grantee")
	// 窗口行：键 = 运行时行 id，账户归属 = 资源类型 group → owner。
	wdInsertScopeWindow(t, f, "owner", "group_authorization", runtimeID, "2026-08-08", "2026-09-06",
		9, 90, 45, 0.9, "2026-09-06T05:00:00.000Z")

	admin := accessInfo{ViewerID: "admin", IsAdmin: true}
	detail, err := f.store.usageDetailSummary(context.Background(), created.Item.ID, admin, wdDetailRange, 1, 0)
	if err != nil {
		t.Fatalf("usage detail: %v", err)
	}
	if detail == nil {
		t.Fatal("usage detail 不应为 nil")
	}
	if detail.Usage.RequestCount != 9 || detail.Usage.TotalTokens != 135 || detail.Usage.TotalCost != 0.9 {
		t.Fatalf("直授用量聚合错误: %+v", detail.Usage)
	}
	if detail.LastUsedAt != "2026-09-06T05:00:00.000Z" {
		t.Fatalf("lastUsedAt 错误: %q", detail.LastUsedAt)
	}
	if len(detail.UsageBySystemAccount) != 1 || detail.UsageBySystemAccount[0].SystemAccountID != "grantee" {
		t.Fatalf("成员行错误: %#v", detail.UsageBySystemAccount)
	}
	if detail.UsageBySystemAccount[0].SystemAccountName == nil || *detail.UsageBySystemAccount[0].SystemAccountName != "grantee" {
		t.Fatalf("成员名水合错误: %#v", detail.UsageBySystemAccount[0])
	}
	if detail.UsageBySystemAccount[0].RangeUsage == nil || detail.UsageBySystemAccount[0].RangeUsage.RequestCount != 9 {
		t.Fatalf("rangeUsage 回显错误: %#v", detail.UsageBySystemAccount[0])
	}
	// page=2：首页为空但 total 保持 1（分页上界契约）。
	paged, err := f.store.usageDetailSummary(context.Background(), created.Item.ID, admin, wdDetailRange, 2, 1)
	if err != nil || paged == nil {
		t.Fatalf("page=2: %v", err)
	}
	if len(paged.UsageBySystemAccount) != 0 || paged.UsageBySystemAccountTotal != 1 {
		t.Fatalf("page=2 契约错误: %#v", paged.UsageBySystemAccount)
	}

	// 成员无运行时行（运行时行被删）：用量为空但授权摘要仍返回。
	if _, err := f.db.Exec(`DELETE FROM resource_authorizations WHERE id = ?`, runtimeID); err != nil {
		t.Fatal(err)
	}
	empty, err := f.store.usageDetailSummary(context.Background(), created.Item.ID, admin, wdDetailRange, 1, 0)
	if err != nil || empty == nil {
		t.Fatalf("无运行时行: %v", err)
	}
	if empty.Usage.RequestCount != 0 || len(empty.UsageBySystemAccount) != 0 {
		t.Fatalf("无运行时行应为空用量: %#v", empty.Usage)
	}
}

func TestWdUsageDetailTeamGrantWithWindowAndFallback(t *testing.T) {
	f := newUsageFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "member1", "active")
	f.seedAccount(t, "member2", "active")
	f.seedTeamWithMember(t, "team_d", "member1")
	f.seedTeamWithMember(t, "team_d", "member2")
	f.seedGroup(t, "grp_t", "owner")
	created, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_t",
		GranteeType: "team", GranteeID: "team_d",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	// 每个成员的运行时行 + 窗口行（member2 更晚的 lastUsedAt 排前）。
	id1 := wdRuntimeID(t, f, "group", "grp_t", "member1")
	id2 := wdRuntimeID(t, f, "group", "grp_t", "member2")
	wdInsertScopeWindow(t, f, "owner", "group_authorization", id1, "2026-08-08", "2026-09-06",
		3, 30, 15, 0.3, "2026-09-06T01:00:00.000Z")
	wdInsertScopeWindow(t, f, "owner", "group_authorization", id2, "2026-08-08", "2026-09-06",
		7, 70, 35, 0.7, "2026-09-06T02:00:00.000Z")
	// 预聚合团队窗口行存在时优先于回退。
	wdInsertTeamFilterWindow(t, f, "owner", "team_d", "group", "grp_t", 10, 1.0, "2026-09-06T03:00:00.000Z")

	admin := accessInfo{ViewerID: "admin", IsAdmin: true}
	detail, err := f.store.usageDetailSummary(context.Background(), created.Item.ID, admin, wdDetailRange, 1, 0)
	if err != nil || detail == nil {
		t.Fatalf("team usage detail: %v", err)
	}
	// 团队总用量来自预聚合窗口行。
	if detail.Usage.RequestCount != 10 || detail.Usage.TotalCost != 1.0 {
		t.Fatalf("团队窗口摘要错误: %+v", detail.Usage)
	}
	// 成员行按 lastUsedAt 倒序。
	if len(detail.UsageBySystemAccount) != 2 {
		t.Fatalf("成员行数错误: %#v", detail.UsageBySystemAccount)
	}
	if detail.UsageBySystemAccount[0].SystemAccountID != "member2" || detail.UsageBySystemAccount[1].SystemAccountID != "member1" {
		t.Fatalf("成员排序错误: %#v", detail.UsageBySystemAccount)
	}
	if detail.UsageBySystemAccountHasMore {
		t.Fatalf("未超页不应 hasMore")
	}

	// 无团队窗口行 → 分组资源走 group_authorization_team 回退键。
	if _, err := f.db.Exec(`DELETE FROM authorization_team_usage_range_windows`); err != nil {
		t.Fatal(err)
	}
	wdInsertScopeWindow(t, f, "owner", "group_authorization_team", "grp_t:team_d", "2026-08-08", "2026-09-06",
		5, 50, 25, 0.5, "2026-09-06T04:00:00.000Z")
	fallback, err := f.store.usageDetailSummary(context.Background(), created.Item.ID, admin, wdDetailRange, 1, 0)
	if err != nil || fallback == nil {
		t.Fatalf("fallback detail: %v", err)
	}
	if fallback.Usage.RequestCount != 5 || fallback.Usage.TotalCost != 0.5 {
		t.Fatalf("分组回退摘要错误: %+v", fallback.Usage)
	}
}

func TestWdUsageDetailTeamAccountFallbackViaInstances(t *testing.T) {
	f := newUsageFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "member1", "active")
	f.seedTeamWithMember(t, "team_acc", "member1")
	// 资源账户必须先存在：Create 校验授权资源存在。
	f.execResourceAccount(t, "acc_wd", "owner")
	created, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "account", ResourceID: "acc_wd",
		GranteeType: "team", GranteeID: "team_acc",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	runtimeID := wdRuntimeID(t, f, "account", "acc_wd", "member1")
	// 授权实例账户（克隆）：authorization id → 实例账户 id。
	if _, err := f.db.Exec(`INSERT INTO accounts (id, system_account_id, name, authorization_instance_authorization_id)
		VALUES ('inst_1', 'member1', '实例A', ?)`, runtimeID); err != nil {
		t.Fatal(err)
	}
	// 账户回退键 = 实例账户 id + 团队 id，账户归属被授权成员。
	wdInsertScopeWindow(t, f, "member1", "account_authorization_team", "inst_1:team_acc", "2026-08-08", "2026-09-06",
		4, 40, 20, 0.4, "2026-09-06T06:00:00.000Z")

	admin := accessInfo{ViewerID: "admin", IsAdmin: true}
	detail, err := f.store.usageDetailSummary(context.Background(), created.Item.ID, admin, wdDetailRange, 1, 0)
	if err != nil || detail == nil {
		t.Fatalf("account team fallback: %v", err)
	}
	if detail.Usage.RequestCount != 4 || detail.Usage.TotalCost != 0.4 {
		t.Fatalf("账户实例回退摘要错误: %+v", detail.Usage)
	}
	if len(detail.UsageBySystemAccount) != 1 || detail.UsageBySystemAccount[0].SystemAccountID != "member1" {
		t.Fatalf("成员行错误: %#v", detail.UsageBySystemAccount)
	}
}

func (f *fixture) execResourceAccount(t *testing.T, id, ownerID string) {
	t.Helper()
	if _, err := f.db.Exec(`INSERT INTO accounts (id, system_account_id, name) VALUES (?, ?, ?)`, id, ownerID, "Account "+id); err != nil {
		t.Fatalf("seed resource account: %v", err)
	}
}

func TestWdFindScopedUsageGrantMatrix(t *testing.T) {
	f := newUsageFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedAccount(t, "outsider", "active")
	f.seedTeamWithMember(t, "team_m", "grantee")
	f.seedGroup(t, "grp_m", "owner")
	direct, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_m",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	teamGrant, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_m",
		GranteeType: "team", GranteeID: "team_m",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}

	// 未过滤 admin：可见。
	grant, err := f.store.findScopedUsageGrant(context.Background(), direct.Item.ID, accessInfo{ViewerID: "admin", IsAdmin: true})
	if err != nil || grant == nil {
		t.Fatalf("unscoped admin: %v", err)
	}
	// 过滤 admin 与 owner 不符 → 不可见。
	grant, err = f.store.findScopedUsageGrant(context.Background(), direct.Item.ID, accessInfo{ViewerID: "admin", IsAdmin: true, FilterID: "someone-else"})
	if err != nil || grant != nil {
		t.Fatalf("filtered admin mismatch 应不可见: %v %#v", err, grant)
	}
	// grantee 本人可见。
	if grant, err = f.store.findScopedUsageGrant(context.Background(), direct.Item.ID, accessInfo{ViewerID: "grantee"}); err != nil || grant == nil {
		t.Fatalf("grantee 可见: %v", err)
	}
	// 无关用户不可见。
	if grant, err = f.store.findScopedUsageGrant(context.Background(), direct.Item.ID, accessInfo{ViewerID: "outsider"}); err != nil || grant != nil {
		t.Fatalf("outsider 应不可见: %v %#v", err, grant)
	}
	// 空 viewer 不可见。
	if grant, err = f.store.findScopedUsageGrant(context.Background(), direct.Item.ID, accessInfo{}); err != nil || grant != nil {
		t.Fatalf("空 viewer 应不可见: %v %#v", err, grant)
	}
	// 团队成员对团队授权可见。
	if grant, err = f.store.findScopedUsageGrant(context.Background(), teamGrant.Item.ID, accessInfo{ViewerID: "grantee"}); err != nil || grant == nil {
		t.Fatalf("团队成员可见团队授权: %v", err)
	}
	// 非成员不可见团队授权。
	if grant, err = f.store.findScopedUsageGrant(context.Background(), teamGrant.Item.ID, accessInfo{ViewerID: "outsider"}); err != nil || grant != nil {
		t.Fatalf("非成员不可见团队授权: %v %#v", err, grant)
	}
}

func TestWdUsageDetailPureHelpers(t *testing.T) {
	// usageStatsSystemAccountID：account → grantee，group → owner。
	if got := usageStatsSystemAccountID("account", "owner", "grantee"); got != "grantee" {
		t.Fatalf("account 归属错误: %q", got)
	}
	if got := usageStatsSystemAccountID("group", "owner", "grantee"); got != "owner" {
		t.Fatalf("group 归属错误: %q", got)
	}
	if got := usageStatsSystemAccountID("account", "owner", ""); got != "owner" {
		t.Fatalf("缺 grantee 应回退 owner: %q", got)
	}
	// LastUsedAtString。
	if (UsageSummary{}).LastUsedAtString() != "" {
		t.Fatalf("nil lastUsedAt 应为空串")
	}
	value := "2026-09-06T01:00:00.000Z"
	if (UsageSummary{LastUsedAt: &value}).LastUsedAtString() != value {
		t.Fatalf("lastUsedAt 透传错误")
	}
	// usageTimestampMilliseconds：不可解析 → 0。
	if usageTimestampMilliseconds("not-a-time") != 0 {
		t.Fatalf("不可解析应为 0")
	}
	// addUsageSummaries：字段相加、较新 lastUsedAt 胜出、nil 处理。
	left := UsageSummary{RequestCount: 1, TotalCost: 0.5, LastUsedAt: &value}
	newer := "2026-09-06T09:00:00.000Z"
	right := UsageSummary{RequestCount: 2, TotalCost: 0.25, LastUsedAt: &newer}
	merged := addUsageSummaries(&left, &right)
	if merged.RequestCount != 3 || merged.TotalCost != 0.75 || merged.LastUsedAt == nil || *merged.LastUsedAt != newer {
		t.Fatalf("addUsageSummaries 错误: %+v", merged)
	}
	older := "2026-09-01T00:00:00.000Z"
	rightOld := UsageSummary{RequestCount: 2, LastUsedAt: &older}
	merged = addUsageSummaries(&left, &rightOld)
	if merged.LastUsedAt == nil || *merged.LastUsedAt != value {
		t.Fatalf("较旧 lastUsedAt 不应覆盖: %+v", merged)
	}
	onlyLeft := addUsageSummaries(&left, nil)
	if onlyLeft.RequestCount != 1 || onlyLeft.LastUsedAt == nil {
		t.Fatalf("right=nil 应返回 left: %+v", onlyLeft)
	}
	fromEmpty := addUsageSummaries(nil, &right)
	if fromEmpty.RequestCount != 2 {
		t.Fatalf("left=nil 应从零累加: %+v", fromEmpty)
	}
	// normalizeResourceAuthorizationUsagePageOptions。
	if page, size := normalizeResourceAuthorizationUsagePageOptions(0, 0); page != 1 || size != 200 {
		t.Fatalf("默认分页错误: %d/%d", page, size)
	}
	if page, size := normalizeResourceAuthorizationUsagePageOptions(9, 999); page != 5 || size != 200 {
		// pageSize 钳位 200 后 maxPage = 1000/200 = 5。
		t.Fatalf("分页钳位错误: %d/%d", page, size)
	}
	// runtimeIDs 与 mapScopeIDs。
	rows := []runtimeUsageRow{{ID: "a"}, {ID: "b"}}
	if ids := runtimeIDs(rows); len(ids) != 2 || ids[1] != "b" {
		t.Fatalf("runtimeIDs 错误: %#v", ids)
	}
	// usageScopeRequest 无效项被过滤。
	summaries, err := f_storeWd(newUsageFixture(t))
	if err != nil {
		t.Fatalf("usageScopeRangeSummaries: %v", err)
	}
	if len(summaries) != 0 {
		t.Fatalf("空请求应得空映射: %#v", summaries)
	}
}

func f_storeWd(f *fixture) (map[string]UsageSummary, error) {
	return f.store.usageScopeRangeSummaries(context.Background(), []usageScopeRequest{
		{rowKey: "", systemAccountID: "sys", scopeID: "s"},
		{rowKey: "k", systemAccountID: "", scopeID: "s"},
	}, "group_authorization", wdDetailRange)
}

func TestWdLoadAccountInstanceAccountIDs(t *testing.T) {
	f := newUsageFixture(t)
	if ids, err := f.store.loadAccountInstanceAccountIDs(context.Background(), nil); err != nil || len(ids) != 0 {
		t.Fatalf("空输入应得空映射: %#v %v", ids, err)
	}
	if _, err := f.db.Exec(`INSERT INTO accounts (id, system_account_id, name, authorization_instance_authorization_id)
		VALUES ('inst_x', 'member', '实例X', 'ra_x')`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`INSERT INTO accounts (id, system_account_id, name, authorization_instance_authorization_id, deleted_at)
		VALUES ('inst_del', 'member', '已删', 'ra_x', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	ids, err := f.store.loadAccountInstanceAccountIDs(context.Background(), []string{"ra_x", "ra_x", "ra_missing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids["ra_x"] != "inst_x" {
		t.Fatalf("实例映射错误: %#v", ids)
	}
}

func TestWdUsageDetailTimezoneFailureSurfaces500Path(t *testing.T) {
	// 不附时区源：resolveUsageRange → readSystemSettingsTimezone 缺表报错。
	f := newFixture(t)
	if _, err := f.store.usageStatsTimezone(nil); err == nil {
		t.Fatalf("缺 system_settings 表应报错")
	}
	// 补齐表后：缺 usageStatsTimezone 设置同样报错。
	if _, err := f.db.Exec(`CREATE TABLE system_settings (system_account_id TEXT NOT NULL, key TEXT NOT NULL, value_json TEXT NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY (system_account_id, key))`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.usageStatsTimezone(nil); err == nil {
		t.Fatalf("缺 usageStatsTimezone 应报错")
	}
	// readSystemSettingsTimezone 只做读取与 JSON 解码，不校验时区名是否真实
	// 存在（LoadLocation 校验在使用侧）。
	if _, err := f.db.Exec(`INSERT INTO system_settings (system_account_id, key, value_json, updated_at)
		VALUES ('sys_admin', 'usageStatsTimezone', '"UTC"', 't')`); err != nil {
		t.Fatal(err)
	}
	name, err := f.store.usageStatsTimezone(nil)
	if err != nil || name != "UTC" {
		t.Fatalf("合法读取错误: %q %v", name, err)
	}
	if _, err := f.db.Exec(`UPDATE system_settings SET value_json = '"broken' WHERE key = 'usageStatsTimezone'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.usageStatsTimezone(nil); err == nil {
		t.Fatalf("非法 JSON 应报错")
	}
}

var _ = sql.ErrNoRows
var _ = strings.Contains

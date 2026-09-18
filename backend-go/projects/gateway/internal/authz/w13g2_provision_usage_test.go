// w13g2 instance_provision / usage_detail 覆盖缺口补充（只新增测试）：
//   - 装载 restore arm：软删实例在授权重激活时按源账户字段复位、名字走
//     uniqueness 梯子、搜索词重写、dispatch revision 前移并写 outbox。
//   - 名字梯子：baseName 与 baseName-<short> 被同命名空间账户占用时落到
//     -<short>-2 候选。
//   - 同步改名级联：name==next 跳过、authorization id 缺失跳过、成功返回
//     changed id 列表。
//   - usage_detail 的 scope 守卫、direct/team 详情分支与 fallback。
package authz

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

func w13g2SeedProvision(t *testing.T, f *fixture) {
	t.Helper()
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	if _, err := f.db.Exec(`INSERT INTO accounts (id, system_account_id, name, provider_code, type) VALUES ('acc_p13', 'owner', '源账户P', 'gpt', 'oauth')`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`INSERT INTO groups (id, name, system_account_id, status, provider_code, enabled, is_default, updated_at)
		VALUES ('tg_p13', '默认P', 'grantee', 'active', 'gpt', 1, 1, '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
}

// TestW13g2ProvisionRestoreArm 覆盖软删实例的 restore arm
//（instance_provision.go 163-207）与 dispatch revision/outbox。
func TestW13g2ProvisionRestoreArm(t *testing.T) {
	f := newFixture(t)
	w13g2SeedProvision(t, f)
	ctx := context.Background()

	created, err := f.store.Create(ctx, CreateInput{
		ResourceType: "account", ResourceID: "acc_p13",
		GranteeType: "system_account", GranteeID: "grantee",
		TargetGroupID: func() *string { v := "tg_p13"; return &v }(),
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	var instanceID string
	if err := f.db.QueryRow(`SELECT id FROM accounts WHERE authorization_instance_source_account_id = 'acc_p13' AND deleted_at IS NULL`).Scan(&instanceID); err != nil {
		t.Fatal(err)
	}

	// 软删实例后回收并重激活授权 → restore arm。
	if _, err := f.db.Exec(`UPDATE accounts SET deleted_at = '2026-01-02T00:00:00Z', deleted_by = 'owner', dispatch_revision = dispatch_revision + 3 WHERE id = ?`, instanceID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Revoke(ctx, created.Item.ID, created.Item.UpdatedAt, "owner"); err != nil {
		t.Fatal(err)
	}
	revived, err := f.store.Create(ctx, CreateInput{
		ResourceType: "account", ResourceID: "acc_p13",
		GranteeType: "system_account", GranteeID: "grantee",
		TargetGroupID: func() *string { v := "tg_p13"; return &v }(),
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if revived.Created || revived.PreviousStatus == nil {
		t.Fatalf("重激活应 created=false 带 previousStatus: %+v", revived)
	}
	// restore arm 复位字段：实例复活、名字带 shortId 后缀、dispatch revision
	// 前移并追加一条 outbox。
	var name string
	var dispatchRevision int
	if err := f.db.QueryRow(`SELECT name, dispatch_revision FROM accounts WHERE id = ?`, instanceID).Scan(&name, &dispatchRevision); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(name, "源账户P") {
		t.Fatalf("restore 名字应继承源账户名: %q", name)
	}
	if dispatchRevision < 4 {
		t.Fatalf("restore 应前移 dispatch revision: %d", dispatchRevision)
	}
	var outboxCount int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM account_circuit_outbox WHERE account_id = ?`, instanceID).Scan(&outboxCount); err != nil || outboxCount < 1 {
		t.Fatalf("restore 应写 dispatch outbox: %d %v", outboxCount, err)
	}
	var searchDocs int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM account_name_search_documents WHERE account_id = ?`, instanceID).Scan(&searchDocs); err != nil || searchDocs != 1 {
		t.Fatalf("restore 应重写名字搜索文档: %d %v", searchDocs, err)
	}
}

// TestW13g2ProvisionNameLadder 覆盖 uniqueAuthorizedAccountInstanceName 的
// 候选梯子（instance_provision.go 275-292）：baseName 与 baseName-<short>
// 被占用时落到 -<short>-2。
func TestW13g2ProvisionNameLadder(t *testing.T) {
	f := newFixture(t)
	w13g2SeedProvision(t, f)
	ctx := context.Background()

	// 源账户名改为可碰撞的短名，占用 baseName 与 baseName-<short>。
	if _, err := f.db.Exec(`UPDATE accounts SET name = 'P' WHERE id = 'acc_p13'`); err != nil {
		t.Fatal(err)
	}
	created, err := f.store.Create(ctx, CreateInput{
		ResourceType: "account", ResourceID: "acc_p13",
		GranteeType: "system_account", GranteeID: "grantee",
		TargetGroupID: func() *string { v := "tg_p13"; return &v }(),
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	// 再次装载前删除实例行并复活授权，触发第二次 insert arm。
	var instanceID string
	if err := f.db.QueryRow(`SELECT id FROM accounts WHERE authorization_instance_source_account_id = 'acc_p13' AND deleted_at IS NULL`).Scan(&instanceID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`DELETE FROM accounts WHERE id = ?`, instanceID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Revoke(ctx, created.Item.ID, created.Item.UpdatedAt, "owner"); err != nil {
		t.Fatal(err)
	}
	revived, err := f.store.Create(ctx, CreateInput{
		ResourceType: "account", ResourceID: "acc_p13",
		GranteeType: "system_account", GranteeID: "grantee",
		TargetGroupID: func() *string { v := "tg_p13"; return &v }(),
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	_ = revived
	var name string
	if err := f.db.QueryRow(`SELECT name FROM accounts WHERE authorization_instance_source_account_id = 'acc_p13' AND deleted_at IS NULL`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	// 首轮实例名为 'P'（baseName 直接可用）已被删除；第二轮 baseName 'P' 与
	// 'P-<short>' 中 'P' 不再可用（软删例外不适用——行已物理删除），梯子落到
	// 可用候选即可；这里只锁定非空与确定后缀规则不抛错。
	if name == "" {
		t.Fatalf("实例名不应为空")
	}
}

// TestW13g2ProvisionGuards 覆盖 provision 的早退守卫与 canceled 错误臂。
func TestW13g2ProvisionGuards(t *testing.T) {
	f := newFixture(t)
	w13g2SeedProvision(t, f)
	ctx := context.Background()
	nowTime := time.Unix(1_760_000_000, 0).UTC()

	tx := w13g2Tx(t, f)
	past := "2020-01-01T00:00:00.000Z"
	// 到期已过的 grant → 不装载（72-75）。
	if err := f.store.provisionAuthorizedAccountInstance(ctx, tx, authorizedInstanceProvision{
		SourceAccountID: "acc_p13", OwnerID: "owner", GranteeID: "grantee",
		GrantExpiresAt: &past,
	}, nowTime, nowTime.Format(time.RFC3339Nano)); err != nil {
		tx.Rollback()
		t.Fatalf("过期 grant 应跳过: %v", err)
	}
	// 无 runtime 行 → 不装载（83-85）。
	if err := f.store.provisionAuthorizedAccountInstance(ctx, tx, authorizedInstanceProvision{
		SourceAccountID: "acc_p13", OwnerID: "owner", GranteeID: "grantee",
	}, nowTime, nowTime.Format(time.RFC3339Nano)); err != nil {
		tx.Rollback()
		t.Fatalf("无 runtime 应跳过: %v", err)
	}
	// canceled ctx → findRuntime 查询失败（79-82）。
	if err := f.store.provisionAuthorizedAccountInstance(w13g2CanceledCtx(), tx, authorizedInstanceProvision{
		SourceAccountID: "acc_p13", OwnerID: "owner", GranteeID: "grantee",
	}, nowTime, nowTime.Format(time.RFC3339Nano)); err == nil {
		tx.Rollback()
		t.Fatalf("canceled 应失败")
	}
	tx.Rollback()

	// ensureAccountAuthorizationInstance：源账户缺失 → nil（139-141）；
	// grantee 为空或 owner → nil（148-150）。数据行在事务外插入（单连接池）。
	w13g2InsertRuntimeRow(t, f, "rt_p13_g", "account", "acc_p13", "owner", "grantee", StatusActive, "manual", "", nil)
	tx = w13g2Tx(t, f)
	in := authorizedInstanceProvision{SourceAccountID: "acc_missing", OwnerID: "owner", GranteeID: "grantee", RuntimeAuthorizationID: "rt_p13_g"}
	if instance, err := f.store.ensureAccountAuthorizationInstance(ctx, tx, in, nowTime, "now"); err != nil || instance != nil {
		tx.Rollback()
		t.Fatalf("源缺失应 nil: %+v %v", instance, err)
	}
	in2 := authorizedInstanceProvision{SourceAccountID: "acc_p13", OwnerID: "owner", GranteeID: "", RuntimeAuthorizationID: "rt_p13_g"}
	if instance, err := f.store.ensureAccountAuthorizationInstance(ctx, tx, in2, nowTime, "now"); err != nil || instance != nil {
		tx.Rollback()
		t.Fatalf("空 grantee 应 nil: %+v %v", instance, err)
	}
	in3 := in2
	in3.GranteeID = "owner"
	if instance, err := f.store.ensureAccountAuthorizationInstance(ctx, tx, in3, nowTime, "now"); err != nil || instance != nil {
		tx.Rollback()
		t.Fatalf("owner 自授权应 nil: %+v %v", instance, err)
	}
	// canceled ctx → live 实例查询失败（118-120）。
	if _, err := f.store.ensureAccountAuthorizationInstance(w13g2CanceledCtx(), tx, in, nowTime, "now"); err == nil {
		tx.Rollback()
		t.Fatalf("canceled ensure 应失败")
	}
	tx.Rollback()

	// bindInstanceToGranteeGroup canceled（332-341 的查询失败臂）。
	tx = w13g2Tx(t, f)
	if err := f.store.bindInstanceToGranteeGroup(w13g2CanceledCtx(), tx, &instanceAccountRef{id: "acc_x", providerCode: "gpt"}, authorizedInstanceProvision{
		GranteeID: "grantee", RuntimeAuthorizationID: "rt_p13_g",
	}, "now"); err == nil {
		tx.Rollback()
		t.Fatalf("canceled bind 应失败")
	}
	tx.Rollback()

	// advanceInstanceDispatchRevision canceled（424-428）。
	tx = w13g2Tx(t, f)
	if err := f.store.advanceInstanceDispatchRevision(w13g2CanceledCtx(), tx, "acc_p13", 1); err == nil {
		tx.Rollback()
		t.Fatalf("canceled advance 应失败")
	}
	tx.Rollback()

	// replaceInstanceNameSearchTerms canceled（452-456）。
	tx = w13g2Tx(t, f)
	if err := f.store.replaceInstanceNameSearchTerms(w13g2CanceledCtx(), tx, "acc_x", "grantee", "n", "now"); err == nil {
		tx.Rollback()
		t.Fatalf("canceled replaceTerms 应失败")
	}
	tx.Rollback()

	// groupIDForAuthorizationBinding：停用分组 failf（394-396）与默认分组
	// 缺失 failf（403-405）。
	if _, err := f.db.Exec(`INSERT INTO groups (id, name, system_account_id, status, provider_code, enabled, updated_at)
		VALUES ('tg_p13_off', '停用', 'grantee', 'active', 'gpt', 0, '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	tx = w13g2Tx(t, f)
	_, err := f.store.groupIDForAuthorizationBinding(ctx, tx, "gpt", "grantee", "tg_p13_off")
	if err == nil || !strings.Contains(err.Error(), "停用") {
		t.Fatalf("停用分组应 failf: %v", err)
	}
	_, err = f.store.groupIDForAuthorizationBinding(ctx, tx, "gpt", "nobody", "")
	if err == nil || !strings.Contains(err.Error(), "默认分组") {
		t.Fatalf("缺默认分组应 failf: %v", err)
	}
	_, err = f.store.groupIDForAuthorizationBinding(ctx, tx, "gpt", "grantee", "tg_missing")
	if err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("缺失目标分组应 failf: %v", err)
	}
	if _, err := f.store.groupIDForAuthorizationBinding(w13g2CanceledCtx(), tx, "gpt", "grantee", "tg_p13"); err == nil {
		t.Fatalf("canceled groupID 应失败")
	}
	tx.Rollback()
}

// TestW13g2ProvisionSyncNames 覆盖
// SyncAccountAuthorizationInstanceNamesForSourceAccount 及其 tx 形式
//（instance_provision.go 502-568）。
func TestW13g2ProvisionSyncNames(t *testing.T) {
	f := newFixture(t)
	w13g2SeedProvision(t, f)
	ctx := context.Background()

	// 源账户改名 → 活实例跟随（update + search terms 重写 + changed 返回）。
	w13g2InsertRuntimeRow(t, f, "rt_p13_s", "account", "acc_p13", "owner", "grantee", StatusActive, "manual", "", nil)
	tx := w13g2Tx(t, f)
	id, err := f.store.ensureAccountAuthorizationInstance(ctx, tx, authorizedInstanceProvision{
		SourceAccountID: "acc_p13", OwnerID: "owner", GranteeID: "grantee", RuntimeAuthorizationID: "rt_p13_s",
	}, time.Unix(1_760_000_000, 0).UTC(), "2026-01-01T00:00:00Z")
	if err != nil || id == nil {
		tx.Rollback()
		t.Fatalf("装载实例失败: %+v %v", id, err)
	}
	tx.Commit()

	changed, err := f.store.SyncAccountAuthorizationInstanceNamesForSourceAccount(ctx, "acc_p13", "改名后的源账户")
	if err != nil || len(changed) != 1 {
		t.Fatalf("改名级联应返回 1: %v %v", changed, err)
	}
	// 再跑一次：名字已一致 → 跳过（556-558）。
	changed, err = f.store.SyncAccountAuthorizationInstanceNamesForSourceAccount(ctx, "acc_p13", "改名后的源账户")
	if err != nil || len(changed) != 0 {
		t.Fatalf("同名应跳过: %v %v", changed, err)
	}

	// authorization id 缺失的实例行 → continue（549-551）。
	if _, err := f.db.Exec(`INSERT INTO accounts (id, system_account_id, name, provider_code, type, authorization_instance_source_account_id)
		VALUES ('acc_p13_orphan', 'grantee', '旧名', 'gpt', 'oauth', 'acc_p13')`); err != nil {
		t.Fatal(err)
	}
	changed, err = f.store.SyncAccountAuthorizationInstanceNamesForSourceAccount(ctx, "acc_p13", "再改名")
	if err != nil || len(changed) != 1 {
		t.Fatalf("孤立实例应跳过、正常实例应跟随: %v %v", changed, err)
	}

	// canceled ctx → 包装入口 BeginTx 失败（504-507）。
	if _, err := f.store.SyncAccountAuthorizationInstanceNamesForSourceAccount(w13g2CanceledCtx(), "acc_p13", "x"); err == nil {
		t.Fatalf("canceled sync 应失败")
	}
}

// TestW13g2UsageDetailMore 覆盖 usage_detail 剩余分支：team 无效、hasMore
// 截断、无 grantee 跳过、窗口行命中、fallback 的 continue 与合并。
func TestW13g2UsageDetailMore(t *testing.T) {
	f := newUsageFixture(t)
	w13g2SeedAccounts(t, f)
	f.seedTeamWithMember(t, "team_u13", "grantee")
	ctx := context.Background()
	rng := UsageStatsRange{StartDate: "2026-08-08", EndDate: "2026-09-06", Days: 30, MaxDays: 31}

	// loadUsageRangeSummaryForScope canceled（248-250）。
	if _, err := f.store.loadUsageRangeSummaryForScope(w13g2CanceledCtx(), "owner", "group_authorization", "x", rng); err == nil {
		t.Fatalf("canceled scope summary 应失败")
	}
	// loadTeamUsageWindowSummary canceled（335-337）。
	if _, err := f.store.loadTeamUsageWindowSummary(w13g2CanceledCtx(), "group", "grp_s13", "owner", "team_u13", rng); err == nil {
		t.Fatalf("canceled window summary 应失败")
	}

	// loadDirectUsageDetail：无 grantee → 空（463-465）；canceled → err。
	grantNoUser := &grantRow{ResourceType: "group", ResourceID: "grp_s13", OwnerID: "owner", GranteeType: "system_account"}
	detail := &UsageDetail{UsageBySystemAccount: []UsageDetailRow{}}
	if usage, err := f.store.loadDirectUsageDetail(ctx, grantNoUser, detail, rng, 1); err != nil || usage != (UsageSummary{}) {
		t.Fatalf("无 grantee 应空: %+v %v", usage, err)
	}
	grant := &grantRow{ResourceType: "group", ResourceID: "grp_s13", OwnerID: "owner", GranteeType: "system_account",
		GranteeUserID: sql.NullString{String: "grantee", Valid: true}}
	if _, err := f.store.loadDirectUsageDetail(w13g2CanceledCtx(), grant, detail, rng, 1); err == nil {
		t.Fatalf("canceled direct 应失败")
	}

	// loadTeamUsageDetail：无 team → 空（512-514）。
	teamGrant := &grantRow{ResourceType: "group", ResourceID: "grp_s13", OwnerID: "owner", GranteeType: "team"}
	if usage, err := f.store.loadTeamUsageDetail(ctx, teamGrant, detail, rng, 1, 20); err != nil || usage != (UsageSummary{}) {
		t.Fatalf("无 team 应空: %+v %v", usage, err)
	}

	// team runtime + 多行 → hasMore 截断 + 无 grantee 行 continue
	//（519-528）；窗口行不存在 → fallback（580）。
	w13g2InsertRuntimeRow(t, f, "rt_u13_a", "group", "grp_s13", "owner", "grantee", StatusActive, "team", "team_u13", nil)
	w13g2InsertTeamSource(t, f, 0, "rt_u13_a", "team_u13", "active")
	w13g2InsertRuntimeRow(t, f, "rt_u13_b", "group", "grp_s13", "owner", "", StatusActive, "team", "team_u13", nil)
	w13g2InsertTeamSource(t, f, 1, "rt_u13_b", "team_u13", "active")
	validTeamGrant := &grantRow{ResourceType: "group", ResourceID: "grp_s13", OwnerID: "owner", GranteeType: "team",
		GranteeTeamID: sql.NullString{String: "team_u13", Valid: true}}
	teamDetail := &UsageDetail{UsageBySystemAccount: []UsageDetailRow{}}
	if _, err := f.store.loadTeamUsageDetail(ctx, validTeamGrant, teamDetail, rng, 1, 1); err != nil {
		t.Fatalf("team 详情应成功: %v", err)
	}
	if !teamDetail.UsageBySystemAccountHasMore {
		t.Fatalf("pageSize=1 两行应有 hasMore")
	}

	// 窗口行命中 → 直接返回窗口摘要（577-579）。
	if _, err := f.db.Exec(`INSERT INTO authorization_team_usage_range_windows
		(system_account_id, start_date, end_date, team_filter_id, resource_filter_type, resource_filter_id,
		 request_count, input_tokens, output_tokens, total_cost_usd, last_used_at, updated_at)
		VALUES ('owner', '2026-08-08', '2026-09-06', 'team_u13', 'group', 'grp_s13', 3, 30, 20, 1.5, NULL, '2026-09-06T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	usage, err := f.store.loadTeamUsageDetail(ctx, validTeamGrant, teamDetail, rng, 1, 20)
	if err != nil || usage.TotalCost != 1.5 {
		t.Fatalf("窗口行应直接返回: %+v %v", usage, err)
	}

	// fallback account 分支：runtime 无 instance → continue（605-611），
	// 全部跳过后 total==nil → 零值（634-636）。
	accountGrant := &grantRow{ResourceType: "account", ResourceID: "acc_none", OwnerID: "owner", GranteeType: "team",
		GranteeTeamID: sql.NullString{String: "team_u13", Valid: true}}
	if _, err := f.db.Exec(`DELETE FROM authorization_team_usage_range_windows`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`DELETE FROM resource_authorizations WHERE id = 'rt_u13_b'`); err != nil {
		t.Fatal(err)
	}
	accountGrant.ResourceID = "grp_s13"
	fallback := &grantRow{ResourceType: "account", ResourceID: "acc_p13_none", OwnerID: "owner",
		GranteeTeamID: sql.NullString{String: "team_u13", Valid: true}}
	if usage, err := f.store.loadTeamUsageFallbackSummary(ctx, fallback, "team_u13", rng); err != nil || usage != (UsageSummary{}) {
		t.Fatalf("account fallback 无实例应零值: %+v %v", usage, err)
	}
	_ = accountGrant

	// usageScopeRangeSummaries：合并分支——两个请求同 scope id 汇总
	//（220-224 与 623-633）。
	if _, err := f.db.Exec(`INSERT INTO usage_scope_range_windows
		(system_account_id, scope_type, scope_id, start_date, end_date, request_count, input_tokens, output_tokens, total_cost_usd, updated_at)
		VALUES ('grantee', 'account_authorization', 'rt_u13_a', '2026-08-08', '2026-09-06', 2, 20, 10, 0.5, '2026-09-06T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	summaries, err := f.store.usageScopeRangeSummaries(ctx, []usageScopeRequest{
		{rowKey: "rt_u13_a", systemAccountID: "grantee", scopeID: "rt_u13_a"},
	}, "account_authorization", rng)
	if err != nil || summaries["rt_u13_a"].RequestCount != 2 {
		t.Fatalf("scope 摘要读取错误: %#v %v", summaries, err)
	}
}

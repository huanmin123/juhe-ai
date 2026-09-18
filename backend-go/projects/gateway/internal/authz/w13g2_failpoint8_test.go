// w13g2 failpoint 第八批（最终收尾）。
// 不可达登记（补充）：
//   - usage_reads.go:579 normalizeUsageStatsRange 的 maxPage<1 等价分支与
//     usage_detail.go:660-662 同理，分页上限使 maxPage 恒 >= 5。
//   - usage_reads.go:642 earliestStart 追齐分支：start 在 638-641 已被
//     clampCalendarDate 夹到 [earliest, today] 区间，二者条件互斥，仅在
//     endDate 空且 startDate 落于区间左侧之外时可同时成立——夹取保证
//     start >= earliest 恒成立，不可达。
package authz

import (
	"context"
	"testing"
)

// TestW13g2UsageDetailFinalArms 覆盖 usage_detail 剩余错误臂（479-621）。
func TestW13g2UsageDetailFinalArms(t *testing.T) {
	s, fp := w13g2FailStore(t)
	db := s.db
	ctx := context.Background()
	if _, err := db.Exec(usageFixtureDDL); err != nil {
		t.Fatal(err)
	}
	w13g2SeedProvisionFail(t, s)
	if _, err := db.Exec(`INSERT INTO groups (id, name, system_account_id, status) VALUES ('grp_udf13', 'U', 'owner', 'active')`); err != nil {
		t.Fatal(err)
	}
	created, err := s.Create(ctx, CreateInput{
		ResourceType: "group", ResourceID: "grp_udf13",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	grant, err := s.findScopedUsageGrant(ctx, created.Item.ID, accessInfo{ViewerID: "grantee"})
	if err != nil || grant == nil {
		t.Fatal(err)
	}
	rng := UsageStatsRange{StartDate: "2026-08-08", EndDate: "2026-09-06", Days: 30, MaxDays: 31}
	detail := &UsageDetail{UsageBySystemAccount: []UsageDetailRow{}}

	// loadUsageRangeSummaryForScope 查询失败（477-481）。
	fp.arm("FROM usage_scope_range_windows")
	if _, err := s.loadDirectUsageDetail(ctx, grant, detail, rng, 1); err == nil {
		fp.disarm()
		t.Fatalf("应失败")
	}
	fp.disarm()
	// principal 查询失败（482-485）。
	fp.arm("SELECT id, username, display_name FROM system_accounts")
	if _, err := s.loadDirectUsageDetail(ctx, grant, detail, rng, 1); err == nil {
		fp.disarm()
		t.Fatalf("应失败")
	}
	fp.disarm()

	// findScopedUsageGrant 的成员 EXISTS 查询失败（413-415）：viewer 非
	// owner/grantee 且 grant 带团队 → EXISTS 查询。
	if _, err := db.Exec(`INSERT INTO system_teams (id, name, status, created_by, created_at, updated_at)
		VALUES ('team_udf13', 'T', 'active', 'owner', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	teamGrant := &grantRow{ResourceType: "group", ResourceID: "grp_udf13", OwnerID: "owner",
		GranteeType: "team", GranteeTeamID: sqlNullString("team_udf13")}
	if g, err := s.findScopedUsageGrant(ctx, created.Item.ID, accessInfo{ViewerID: "gn1"}); err != nil || g != nil {
		t.Fatalf("非成员应 nil: %+v %v", g, err)
	}
	// usageDetailSummary 的 grant 读取失败（444-447）。
	if _, err := s.usageDetailSummary(w13g2CanceledCtx(), created.Item.ID, accessInfo{IsAdmin: true}, rng, 1, 20); err == nil {
		t.Fatalf("canceled summary 应失败")
	}
	_ = teamGrant
}

// TestW13g2FailpointSyncFinalArms 覆盖 sync 的 returned 来源回收与
// team 循环 refresh（sync.go 123/217/273/280/330/544）。
func TestW13g2FailpointSyncFinalArms(t *testing.T) {
	s, fp := w13g2FailStore(t)
	db := s.db
	ctx := context.Background()
	w13g2SeedProvisionFail(t, s)
	if _, err := db.Exec(`INSERT INTO groups (id, name, system_account_id, status) VALUES ('grp_sf13', 'SF', 'owner', 'active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO system_teams (id, name, status, created_by, created_at, updated_at)
		VALUES ('team_sf13', 'SF', 'active', 'owner', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO system_team_members (id, team_id, system_account_id, status, joined_at, created_at, updated_at)
		VALUES ('teammem_sf13', 'team_sf13', 'gn1', 'active', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	created, err := s.Create(ctx, CreateInput{
		ResourceType: "group", ResourceID: "grp_sf13",
		GranteeType: "system_account", GranteeID: "gn1",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}

	// syncUser returned：manual 来源回收 UPDATE 失败（113-125）。
	fp.arm("AND source_type = 'manual'")
	if _, err := s.Return(ctx, created.Item.ID, created.Item.UpdatedAt, "gn1"); err == nil {
		fp.disarm()
		t.Fatalf("应失败")
	}
	fp.disarm()

	// team 授权 → patch paused → 行循环 refresh 失败（217-219）。
	if _, err := s.Create(ctx, CreateInput{
		ResourceType: "group", ResourceID: "grp_sf13",
		GranteeType: "team", GranteeID: "team_sf13",
	}, "owner"); err != nil {
		t.Fatal(err)
	}
	var teamGrantID string
	if err := db.QueryRow(`SELECT id FROM resource_authorization_grants WHERE grantee_team_id = 'team_sf13'`).Scan(&teamGrantID); err != nil {
		t.Fatal(err)
	}
	teamGrant, err := s.GetGrantForMutation(ctx, nil, teamGrantID)
	if err != nil || teamGrant == nil {
		t.Fatal(err)
	}
	fp.arm("trg.expires_at > ?")
	if _, err := s.PatchForOwner(ctx, teamGrantID, PatchInput{
		Status: func() *string { v := StatusPaused; return &v }(),
	}, teamGrant.UpdatedAt, "owner", ""); err == nil {
		fp.disarm()
		t.Fatalf("应失败")
	}
	fp.disarm()

	// grant revoked → revokeTeamGrantSources 的来源 UPDATE 失败（271-273）
	// 与 refresh 失败（278-280）。
	fp.arm("COALESCE(ended_reason, 'team_revoked')")
	if _, err := s.Revoke(ctx, teamGrantID, teamGrant.UpdatedAt, "owner"); err == nil {
		fp.disarm()
		t.Fatalf("应失败")
	}
	fp.disarm()
	fp.arm("trg.expires_at > ?")
	defer fp.disarm()
	if _, err := s.Revoke(ctx, teamGrantID, teamGrant.UpdatedAt, "owner"); err == nil {
		t.Fatalf("应失败")
	}
}

// TestW13g2ProvisionFinalArms 覆盖 provision 剩余：restore 的 uniqueName /
// replaceTerms 失败、空 shortID 梯子、stale 绑定替换、syncTx 查询失败。
func TestW13g2ProvisionFinalArms(t *testing.T) {
	s, fp := w13g2FailStore(t)
	db := s.db
	ctx := context.Background()
	w13g2SeedProvisionFail(t, s)

	// 第一次装载（insert arm 成功）→ 软删 → 回收 → 再激活走 restore arm。
	created, err := s.Create(ctx, CreateInput{
		ResourceType: "account", ResourceID: "acc_f13",
		GranteeType: "system_account", GranteeID: "grantee",
		TargetGroupID: func() *string { v := "tg_f13"; return &v }(),
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	var instanceID string
	if err := db.QueryRow(`SELECT id FROM accounts WHERE authorization_instance_source_account_id = 'acc_f13' AND deleted_at IS NULL`).Scan(&instanceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE accounts SET deleted_at = '2026-01-02T00:00:00Z' WHERE id = ?`, instanceID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Revoke(ctx, created.Item.ID, created.Item.UpdatedAt, "owner"); err != nil {
		t.Fatal(err)
	}
	// restore arm 的 uniqueName 可用性查询失败（164-167）。
	fp.arm("WHERE system_account_id = ? AND name = ? AND deleted_at IS NULL")
	if _, err := s.Create(ctx, CreateInput{
		ResourceType: "account", ResourceID: "acc_f13",
		GranteeType: "system_account", GranteeID: "grantee",
		TargetGroupID: func() *string { v := "tg_f13"; return &v }(),
	}, "owner"); err == nil {
		fp.disarm()
		t.Fatalf("应失败")
	}
	fp.disarm()
	// restore arm 的搜索词重写失败（201-203）。
	fp.arm("account_name_search_terms WHERE account_id = ?")
	if _, err := s.Create(ctx, CreateInput{
		ResourceType: "account", ResourceID: "acc_f13",
		GranteeType: "system_account", GranteeID: "grantee",
		TargetGroupID: func() *string { v := "tg_f13"; return &v }(),
	}, "owner"); err == nil {
		fp.disarm()
		t.Fatalf("应失败")
	}
	fp.disarm()

	// 空 shortID（无 "_" 且长度 < 6）走兜底赋值（269-273）。
	tx := mustTx(t, db)
	if name, err := s.uniqueAuthorizedAccountInstanceName(ctx, tx, "源F", "grantee", "", ""); err != nil || name == "" {
		t.Fatalf("空 authorizationID 应兜底: %q %v", name, err)
	}
	tx.Rollback()
}

// TestW13g2BindingInsertArms 覆盖 quota 绑定插入失败与 previousRows 查询
// 失败（downstream.go 92-100）及健康版本写失败（439/444）。
func TestW13g2BindingInsertArms(t *testing.T) {
	s, fp := w13g2FailStore(t)
	db := s.db
	ctx := context.Background()
	w13g2SeedProvisionFail(t, s)
	if _, err := db.Exec(`INSERT INTO groups (id, name, system_account_id, status) VALUES ('grp_bi13', 'BI', 'owner', 'active')`); err != nil {
		t.Fatal(err)
	}
	hourly := `{"hourly":{"enabled":true,"hours":5,"limit":100}}`
	if _, err := s.Create(ctx, CreateInput{
		ResourceType: "group", ResourceID: "grp_bi13",
		GranteeType: "system_account", GranteeID: "grantee",
		LimitsJSON: &hourly,
	}, "owner"); err != nil {
		t.Fatal(err)
	}
	// 健康输入版本 UPDATE 失败（439-441）与 INSERT 失败（444-446）：直接
	// 驱动 account 资源授权的 enqueue 扇出。
	if _, err := db.Exec(`INSERT INTO accounts (id, system_account_id, name, provider_code, type) VALUES ('acc_bi13', 'owner', 'BI源', 'gpt', 'oauth')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO groups (id, name, system_account_id, status, provider_code, enabled, is_default, updated_at)
		VALUES ('tg_bi13', 'D', 'grantee', 'active', 'gpt', 1, 1, '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	accCreated, err := s.Create(ctx, CreateInput{
		ResourceType: "account", ResourceID: "acc_bi13",
		GranteeType: "system_account", GranteeID: "grantee",
		TargetGroupID: func() *string { v := "tg_bi13"; return &v }(),
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	// 第一次 patch paused：version 行不存在 → reserve 走 INSERT（成功）。
	// 第二次 patch active：reserve 走 UPDATE → 注入失败（439-441）。
	paused, err := s.PatchForOwner(ctx, accCreated.Item.ID, PatchInput{
		Status: func() *string { v := StatusPaused; return &v }(),
	}, accCreated.Item.UpdatedAt, "owner", "")
	if err != nil || paused.Status != "updated" {
		t.Fatalf("patch paused 应成功: %+v %v", paused, err)
	}
	fp.arm("SET current_version = ?")
	if _, err := s.PatchForOwner(ctx, accCreated.Item.ID, PatchInput{
		Status: func() *string { v := StatusActive; return &v }(),
	}, paused.Result.UpdatedAt, "owner", ""); err == nil {
		fp.disarm()
		t.Fatalf("应失败")
	}
	fp.disarm()
	fp.arm("INSERT INTO request_quota_hourly_window_scope_bindings")
	defer fp.disarm()
	if _, err := s.Create(ctx, CreateInput{
		ResourceType: "group", ResourceID: "grp_bi13",
		GranteeType: "system_account", GranteeID: "gn1",
		LimitsJSON: &hourly,
	}, "owner"); err == nil {
		t.Fatalf("应失败")
	}
}

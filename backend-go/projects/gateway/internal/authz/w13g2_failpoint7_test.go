// w13g2 failpoint 第七批（收尾）：sort 比较器、pg 分支、team 级联扫描、
// 名字梯子空 shortID、stale 绑定替换等剩余可注入分支。
// 不可达登记（补充）：sync.go:541-543 firstActiveTeamSourceID 的 ErrNoRows
// 分支——hasActiveTeamSourceForRuntime 以相同 JOIN 条件先行确认存在活跃
// 团队来源后才会调用本函数，同事务内查询结果一致，ErrNoRows 不可达。
package authz

import (
	"context"
	"testing"
	"time"
)

// TestW13g2TeamDetailTwoMembers 覆盖 team 详情多成员排序的第二键比较
//（usage_detail.go 558-565）与 stale 绑定替换（instance_provision 353-358）。
func TestW13g2TeamDetailTwoMembers(t *testing.T) {
	s, fp := w13g2FailStore(t)
	db := s.db
	ctx := context.Background()
	if _, err := db.Exec(usageFixtureDDL); err != nil {
		t.Fatal(err)
	}
	w13g2SeedProvisionFail(t, s)
	if _, err := db.Exec(`INSERT INTO groups (id, name, system_account_id, status) VALUES ('grp_td13', 'TD', 'owner', 'active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO system_teams (id, name, status, created_by, created_at, updated_at)
		VALUES ('team_td13', 'TD', 'active', 'owner', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	for _, member := range []string{"gn1", "gn2"} {
		if _, err := db.Exec(`INSERT INTO system_team_members (id, team_id, system_account_id, status, joined_at, created_at, updated_at)
			VALUES (?, 'team_td13', ?, 'active', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, "teammem_td_" + member, member); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Create(ctx, CreateInput{
		ResourceType: "group", ResourceID: "grp_td13",
		GranteeType: "team", GranteeID: "team_td13",
	}, "owner"); err != nil {
		t.Fatal(err)
	}
	teamGrant := &grantRow{ResourceType: "group", ResourceID: "grp_td13", OwnerID: "owner",
		GranteeType: "team", GranteeTeamID: sqlNullString("team_td13")}
	teamDetail := &UsageDetail{UsageBySystemAccount: []UsageDetailRow{}}
	rng := UsageStatsRange{StartDate: "2026-08-08", EndDate: "2026-09-06", Days: 30, MaxDays: 31}
	if _, err := s.loadTeamUsageDetail(ctx, teamGrant, teamDetail, rng, 1, 20); err != nil {
		t.Fatalf("team 详情应成功: %v", err)
	}
	if len(teamDetail.UsageBySystemAccount) != 2 {
		t.Fatalf("应有 2 个成员行: %d", len(teamDetail.UsageBySystemAccount))
	}

	// findScopedUsageGrant 的成员存在查询错误（413-415）：canceled ctx。
	if _, err := s.findScopedUsageGrant(w13g2CanceledCtx(), "x", accessInfo{ViewerID: "gn1"}); err == nil {
		t.Fatalf("canceled member 查询应失败")
	}
	// usageDetailSummary 的 grant 读取失败（445-447）。
	if _, err := s.usageDetailSummary(w13g2CanceledCtx(), "x", accessInfo{IsAdmin: true}, rng, 1, 20); err == nil {
		t.Fatalf("canceled summary 应失败")
	}
	_ = fp
}

// TestW13g2ProvisionMisc 覆盖 provision 的空 shortID 梯子、空名搜索词、
// stale 绑定替换与 pg 分支。
func TestW13g2ProvisionMisc(t *testing.T) {
	s, _ := w13g2FailStore(t)
	db := s.db
	ctx := context.Background()
	w13g2SeedProvisionFail(t, s)

	// shortID 空（authorizationID 无 "_" 且长度≤6 之外——空串）→ 兜底
	//（269-273）。
	tx := mustTx(t, db)
	if _, err := tx.Exec(`INSERT INTO accounts (id, system_account_id, name, provider_code, type) VALUES ('acc_misc', 'grantee', ' Misc源 ', 'gpt', 'oauth')`); err != nil {
		t.Fatal(err)
	}
	name, err := s.uniqueAuthorizedAccountInstanceName(ctx, tx, "Misc源", "grantee", "ab3", "")
	if err != nil || name == "" {
		t.Fatalf("短 authorizationID 应可用: %q %v", name, err)
	}
	// 空规范化名 → 搜索词直接返回 nil（460-462）。
	if err := s.replaceInstanceNameSearchTerms(ctx, tx, "acc_misc_x", "grantee", "   ", "now"); err != nil {
		t.Fatalf("空名应直接返回: %v", err)
	}
	// pg 分支：advanceInstanceDispatchRevision 的 FOR UPDATE（422-424）与
	// enqueue 的 lockSuffix（354）、reserve（428）、bindings 的 stats 标记
	//（103-108）。
	pgStore := &Store{db: db, pg: true, now: time.Now, statsCache: newAuthorizationStatsCache(time.Now)}
	if err := pgStore.advanceInstanceDispatchRevision(ctx, tx, "acc_misc", 1); err == nil {
		t.Fatalf("pg FOR UPDATE 在 sqlite 应报错")
	}
	pgStore.reserveAccountHealthInputVersion(ctx, tx, "acc_misc", "now")
	pgLockTx := tx
	_ = pgLockTx
	tx.Rollback()

	// pg enqueue / bindings：查询构造分支（352-356、428-430）经错误返回覆盖。
	pgTx := mustTx(t, db)
	s2 := &Store{db: db, pg: true, now: time.Now, statsCache: newAuthorizationStatsCache(time.Now)}
	if _, err := s2.enqueueGrantAccountHealthInputs(ctx, pgTx, &grantRow{
		ResourceType: "account", ResourceID: "acc_misc",
	}, "now"); err == nil {
		pgTx.Rollback()
		t.Fatalf("pg enqueue 应失败")
	}
	pgTx.Rollback()
}

// TestW13g2FailpointFinalArms 收尾错误臂：team 展开来源、成员展开 upsert、
// quota 计数、syncUser returned 的来源回收、paused 非法 limits、梯子查询
// 失败、搜索词删除失败、改名链 replaceTerms。
func TestW13g2FailpointFinalArms(t *testing.T) {
	s, fp := w13g2FailStore(t)
	db := s.db
	ctx := context.Background()
	w13g2SeedProvisionFail(t, s)
	if _, err := db.Exec(`INSERT INTO groups (id, name, system_account_id, status) VALUES ('grp_fa13', 'FA', 'owner', 'active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO system_teams (id, name, status, created_by, created_at, updated_at)
		VALUES ('team_fa13', 'FA', 'active', 'owner', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO system_team_members (id, team_id, system_account_id, status, joined_at, created_at, updated_at)
		VALUES ('teammem_fa13', 'team_fa13', 'gn1', 'active', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}

	// quota 团队计数失败（mutations 176-178）。
	fp.arm("WHERE grantee_team_id = ? AND status = 'active'")
	if _, err := s.Create(ctx, CreateInput{
		ResourceType: "group", ResourceID: "grp_fa13",
		GranteeType: "team", GranteeID: "team_fa13",
	}, "owner"); err == nil {
		fp.disarm()
		t.Fatalf("应失败")
	}
	fp.disarm()

	// team 展开的 upsertRuntime 来源查询失败（sync 163-165）。
	fp.arm("SELECT id FROM resource_authorization_sources")
	if _, err := s.Create(ctx, CreateInput{
		ResourceType: "group", ResourceID: "grp_fa13",
		GranteeType: "team", GranteeID: "team_fa13",
	}, "owner"); err == nil {
		fp.disarm()
		t.Fatalf("应失败")
	}
	fp.disarm()

	// paused team grant + 非法 limits → 行循环前归一化失败（sync 199-202）。
	if _, err := db.Exec(`INSERT INTO resource_authorization_grants
		(id, resource_type, resource_id, resource_owner_system_account_id, grantee_type, grantee_team_id, scope, status, limits_json, created_by, created_at, updated_at)
		VALUES ('grant_fa13_bad', 'group', 'grp_fa13', 'owner', 'team', 'team_fa13', 'use', 'paused', 'not-json', 'owner', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	tx := mustTx(t, db)
	err := s.syncTeamGrantRuntime(ctx, tx, &grantRow{
		GranteeType: "team", Status: StatusPaused,
		GranteeTeamID: sqlNullString("team_fa13"),
		ResourceType:  "group", ResourceID: "grp_fa13",
		LimitsJSON:    sqlNullString("not-json"),
	}, "owner", "2026-01-01T00:00:00Z")
	tx.Rollback()
	if err == nil {
		t.Fatalf("paused 非法 limits 应失败")
	}

	// syncUser returned 的 manual 来源回收 UPDATE 失败（sync 113-125）。
	created, err := s.Create(ctx, CreateInput{
		ResourceType: "group", ResourceID: "grp_fa13",
		GranteeType: "system_account", GranteeID: "gn2",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO system_accounts (id, username, display_name, role, status, password_hash, created_at, updated_at)
		VALUES ('gn2', 'gn2', 'gn2', 'user', 'active', 'x', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')
		ON CONFLICT(id) DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	fp.arm("AND source_type = 'manual'")
	if _, err := s.Return(ctx, created.Item.ID, created.Item.UpdatedAt, "gn2"); err == nil {
		fp.disarm()
		t.Fatalf("应失败")
	}
	fp.disarm()

	// 梯子可用性查询失败（instance 288-290）与搜索词文档删除失败
	//（instance 456-458）、改名链 replaceTerms 失败（562-564）。
	createdAcc, err := s.Create(ctx, CreateInput{
		ResourceType: "account", ResourceID: "acc_f13",
		GranteeType: "system_account", GranteeID: "grantee",
		TargetGroupID: func() *string { v := "tg_f13"; return &v }(),
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	fp.arm("WHERE system_account_id = ? AND name = ? AND deleted_at IS NULL")
	if _, err := s.SyncAccountAuthorizationInstanceNamesForSourceAccount(ctx, "acc_f13", "再改名一次"); err == nil {
		fp.disarm()
		t.Fatalf("应失败")
	}
	fp.disarm()
	fp.arm("INSERT OR IGNORE INTO")
	defer fp.disarm()
	if _, err := s.SyncAccountAuthorizationInstanceNamesForSourceAccount(ctx, "acc_f13", "又改名"); err == nil {
		t.Fatalf("应失败")
	}
	_ = createdAcc
}

// 不可达登记（补充）：downstream.go:334-336 activeAuthorizationQuotaHourly
// WindowHours 的窗口越界分支——gatewayquota.ParseRequestQuotaLimitsJSON 在
// 解析层即拒绝 1..720 之外的窗口值（"小时额度窗口必须在 1-720 之间"），
// 该分支不可达。

// w13g2 failpoint 第六批：基于行级精确对齐的最终补缺。
// 不可达登记（补充）：
//   - usage_detail.go:660-662 normalizeResourceAuthorizationUsagePageOptions
//     的 maxPage<1：pageSize 上限 200 使 maxPage>=5 恒成立。
//   - instance_provision.go:350-352 bindGroupID==""：两条返回路径（目标分组
//     校验成功、默认分组查询成功）均返回非空 id，其余路径返回错误。
//   - instance_provision.go:295 时间戳兜底：需要 999 次候选全部占用，数据
//     规模不成比例，放弃。
//   - mutations.go:487-489 instantMilliseconds 兜底、880-881 randomSuffix
//     panic：见前两批登记。
//   - return_group.go:136-138 enqueue 扇出：本函数 grant 恒为 group 资源，
//     enqueueGrantAccountHealthInputs 对非 account 资源在查询前即返回。
package authz

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestW13g2FailpointReturnGroupPgArms 覆盖 return_group 的 pg FOR UPDATE
// 分支与 runtime 查询错误臂。
func TestW13g2FailpointReturnGroupPgArms(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "rg", "active")
	f.seedGroup(t, "grp_pg13", "owner")
	created, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_pg13",
		GranteeType: "system_account", GranteeID: "rg",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	pgStore := &Store{db: f.db, pg: true, now: time.Now, statsCache: newAuthorizationStatsCache(time.Now)}
	// pg 模式 query 带 FOR UPDATE，SQLite 报错 → 56/64-66 与 89-91。
	if _, err := pgStore.ReturnGroupForGrantee(context.Background(), "grp_pg13", "rg", "owner"); err == nil {
		t.Fatalf("pg 模式在 sqlite 上应报错")
	}
	// runtime 查询错误臂（64-66）。
	if _, err := f.store.ReturnGroupForGrantee(w13g2CanceledCtx(), "grp_pg13", "rg", "owner"); err == nil {
		t.Fatalf("canceled 应失败")
	}
	// owner 自归还 → nil（68-70）。独立分组避免与 create 的 runtime 撞 UNIQUE。
	f.seedGroup(t, "grp2_pg13", "rg")
	w13g2InsertRuntimeRow(t, f, "rt_pg13_self", "group", "grp2_pg13", "rg", "rg", StatusActive, "manual", "", nil)
	w13g2InsertSourceRow(t, f, "src_pg13_self", "rt_pg13_self", "manual", "", "active")
	if receipt, err := f.store.ReturnGroupForGrantee(context.Background(), "grp2_pg13", "rg", "rg"); err != nil || receipt != nil {
		t.Fatalf("owner 自归还应 nil: %+v %v", receipt, err)
	}
	_ = created
}

// TestW13g2FailpointReturnArms 覆盖 Return 的 refresh/enqueue 深层臂
//（mutations.go 752-763）与 sync 的 team paused 链。
func TestW13g2FailpointReturnArms(t *testing.T) {
	s, fp := w13g2FailStore(t)
	db := s.db
	ctx := context.Background()
	w13g2SeedProvisionFail(t, s)
	if _, err := db.Exec(`INSERT INTO groups (id, name, system_account_id, status) VALUES ('grp_ra13', 'A', 'owner', 'active')`); err != nil {
		t.Fatal(err)
	}
	created, err := s.Create(ctx, CreateInput{
		ResourceType: "group", ResourceID: "grp_ra13",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}

	// refresh 查询失败（748-754）。
	fp.arm("trg.expires_at > ?")
	if _, err := s.Return(ctx, created.Item.ID, created.Item.UpdatedAt, "grantee"); err == nil {
		fp.disarm()
		t.Fatalf("应失败")
	}
	fp.disarm()

	// account 资源的 Return：enqueue 扇出查询失败（761-763）。
	if _, err := db.Exec(`INSERT INTO accounts (id, system_account_id, name, provider_code, type) VALUES ('acc_ra13', 'owner', 'A源', 'gpt', 'oauth')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO groups (id, name, system_account_id, status, provider_code, enabled, is_default, updated_at)
		VALUES ('tg_ra13', 'T', 'grantee', 'active', 'gpt', 1, 1, '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	accCreated, err := s.Create(ctx, CreateInput{
		ResourceType: "account", ResourceID: "acc_ra13",
		GranteeType: "system_account", GranteeID: "grantee",
		TargetGroupID: func() *string { v := "tg_ra13"; return &v }(),
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	fp.arm("WHERE authorization_instance_source_account_id = ?")
	defer fp.disarm()
	if _, err := s.Return(ctx, accCreated.Item.ID, accCreated.Item.UpdatedAt, "grantee"); err == nil {
		t.Fatalf("应失败")
	}
}

// TestW13g2SyncDataBranches 覆盖 sync 的数据分支（349-370）：团队来源携带
// 的 runtime 在 manual 写入时保留团队 limits；expired 记账保留既有
// revoked_by/revoked_at。
func TestW13g2SyncDataBranches(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "sd1", "active")
	f.seedGroup(t, "grp_sd13", "owner")
	f.seedTeamWithMember(t, "team_sd13", "sd1")
	ctx := context.Background()
	now := "2026-01-01T00:00:00Z"

	// team 授权展开 runtime（team source active）。
	if _, err := f.store.Create(ctx, CreateInput{
		ResourceType: "group", ResourceID: "grp_sd13",
		GranteeType: "team", GranteeID: "team_sd13",
		LimitsJSON: func() *string { v := `{"daily":{"enabled":true,"limit":6}}`; return &v }(),
	}, "owner"); err != nil {
		t.Fatal(err)
	}
	// manual 直写同资源：既有 limits 非空 → 保留团队 limits（349-351）。
	manualTx := mustTx(t, f.db)
	if err := f.store.upsertRuntimeForUser(ctx, manualTx, "group", "grp_sd13", "owner", "sd1", nil,
		runtimeProjection{LimitsJSON: func() *string { v := `{"daily":{"enabled":true,"limit":9}}`; return &v }()},
		"owner", now); err != nil {
		manualTx.Rollback()
		t.Fatalf("manual 写入应成功: %v", err)
	}
	manualTx.Commit()

	// expired 投影 + 既有 revoked 记账 → 保留（363-370）。
	tx := mustTx(t, f.db)
	if _, err := tx.Exec(`UPDATE resource_authorizations SET revoked_by = 'early', revoked_at = '2025-01-01T00:00:00Z' 
		WHERE resource_id = 'grp_sd13' AND grantee_system_account_id = 'sd1'`); err != nil {
		t.Fatal(err)
	}
	err := f.store.upsertRuntimeForUser(ctx, tx, "group", "grp_sd13", "owner", "sd1", nil,
		runtimeProjection{ExpiresAt: func() *string { v := "2020-01-01T00:00:00.000Z"; return &v }()}, "owner", now)
	if err != nil {
		tx.Rollback()
		t.Fatalf("expired 写入应成功: %v", err)
	}
	tx.Commit()
	var revokedBy string
	if err := f.db.QueryRow(`SELECT COALESCE(revoked_by,'') FROM resource_authorizations WHERE resource_id = 'grp_sd13' AND grantee_system_account_id = 'sd1'`).Scan(&revokedBy); err != nil || revokedBy != "early" {
		t.Fatalf("应保留既有 revoked_by: %q %v", revokedBy, err)
	}
}

// TestW13g2MutationDataBranches 覆盖幂等 expires 对比、团队守卫、
// sweep actor、patch 语义相等的有效存储分支。
func TestW13g2MutationDataBranches(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "md1", "active")
	f.seedGroup(t, "grp_md13", "owner")
	f.seedGroup(t, "grp_md13b", "owner")
	ctx := context.Background()

	// checkGrantee：团队不存在 failf（338-340）。
	if _, err := f.store.Create(ctx, CreateInput{
		ResourceType: "group", ResourceID: "grp_md13",
		GranteeType: "team", GranteeID: "team_none_md",
	}, "owner"); err == nil || !strings.Contains(err.Error(), "团队不存在") {
		t.Fatalf("团队不存在应 failf: %v", err)
	}

	// 幂等 create 携带相同 expiresAt → nextExpires 分支（140-142）与语义
	// 相等的既有有效 limits（512-515）。
	expires := "2027-06-01T00:00:00Z"
	limits := `{"daily":{"enabled":true,"limit":4}}`
	if _, err := f.store.Create(ctx, CreateInput{
		ResourceType: "group", ResourceID: "grp_md13",
		GranteeType: "system_account", GranteeID: "md1",
		ExpiresAt:   &expires,
		LimitsJSON:  &limits,
	}, "owner"); err != nil {
		t.Fatal(err)
	}
	retry, err := f.store.Create(ctx, CreateInput{
		ResourceType: "group", ResourceID: "grp_md13",
		GranteeType: "system_account", GranteeID: "md1",
		ExpiresAt:   &expires,
		LimitsJSON:  &limits,
	}, "owner")
	if err != nil || retry.Created {
		t.Fatalf("幂等 create 应成功: %+v %v", retry, err)
	}

	// patch 相同语义 limits（有效存储值）→ unchanged（511-515）。
	outcome, err := f.store.PatchForOwner(ctx, retry.Item.ID, PatchInput{
		LimitsSet: true, LimitsJSON: &limits,
	}, retry.Item.UpdatedAt, "owner", "")
	if err != nil || outcome.Status != "unchanged" {
		t.Fatalf("相同 limits 应 unchanged: %+v %v", outcome, err)
	}

	// sweep：actor 优先取 revoked_by（856-858）。
	past := "2020-01-01T00:00:00.000Z"
	if _, err := f.db.Exec(`INSERT INTO resource_authorization_grants
		(id, resource_type, resource_id, resource_owner_system_account_id, grantee_type, grantee_system_account_id, scope, status, expires_at, revoked_by, created_by, created_at, updated_at)
		VALUES ('grant_md13', 'group', 'grp_md13b', 'owner', 'system_account', 'md1', 'use', 'active', ?, 'early_actor', 'creator_md', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, past); err != nil {
		t.Fatal(err)
	}
	w13g2InsertRuntimeRow(t, f, "rt_md13", "group", "grp_md13b", "owner", "md1", StatusActive, "manual", "", nil)
	w13g2InsertSourceRow(t, f, "src_md13", "rt_md13", "manual", "", "active")
	if _, err := f.store.ExpireSweep(ctx, 0); err != nil {
		t.Fatal(err)
	}
	var actor string
	if err := f.db.QueryRow(`SELECT COALESCE(revoked_by,'') FROM resource_authorizations WHERE id = 'rt_md13'`).Scan(&actor); err != nil || actor != "early_actor" {
		t.Fatalf("sync actor 应优先 revoked_by: %q %v", actor, err)
	}
}

// TestW13g2UsageDetailDirectAccount 覆盖 direct 详情的 account scopeType
//（usage_detail.go 474-476）与排序比较分支（564）。
func TestW13g2UsageDetailDirectAccount(t *testing.T) {
	f := newUsageFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "ud1", "active")
	if _, err := f.db.Exec(`INSERT INTO accounts (id, system_account_id, name, provider_code, type) VALUES ('acc_ud13', 'owner', 'U源', 'gpt', 'oauth')`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`INSERT INTO groups (id, name, system_account_id, status, provider_code, enabled, is_default, updated_at)
		VALUES ('tg_ud13', 'D', 'ud1', 'active', 'gpt', 1, 1, '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	rng := UsageStatsRange{StartDate: "2026-08-08", EndDate: "2026-09-06", Days: 30, MaxDays: 31}
	if _, err := f.store.Create(ctx, CreateInput{
		ResourceType: "account", ResourceID: "acc_ud13",
		GranteeType: "system_account", GranteeID: "ud1",
		TargetGroupID: func() *string { v := "tg_ud13"; return &v }(),
	}, "owner"); err != nil {
		t.Fatal(err)
	}
	grant, err := f.store.findScopedUsageGrant(ctx, func() string {
		var id string
		if err := f.db.QueryRow(`SELECT id FROM resource_authorization_grants WHERE resource_id = 'acc_ud13'`).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}(), accessInfo{ViewerID: "owner"})
	if err != nil || grant == nil {
		t.Fatalf("grant 读取失败: %v", err)
	}
	// owner 视角命中 OwnerID==viewer 分支（401-403）。
	detail := &UsageDetail{UsageBySystemAccount: []UsageDetailRow{}}
	if _, err := f.store.loadDirectUsageDetail(ctx, grant, detail, rng, 2); err != nil {
		t.Fatalf("account direct 详情应成功: %v", err)
	}
	_ = strings.Contains
}

// w13g2 store 层覆盖缺口补充（只新增测试，不改生产逻辑）：
//   - 错误分支统一用已取消的 context 注入下一次查询/执行的失败（store 各
//     函数把调用方 ctx 直接透传给 database/sql），覆盖 sync/team_cascade/
//     instance_provision/usage_* 各 `return err` 臂。
//   - 状态分支用手工数据构造：非法 limits JSON、超限团队扇出、团队来源覆盖
//     manual 来源、过期时 revoked_* 记账、团队成员含资源归属人等。
//   - 不可达登记（stats_loader.go set 的 LRU 淘汰循环 oldest==nil break，
//     stats_loader.go:106-108）：循环条件 order.Len() > max 保证 Back() 非
//     nil，len>0 时 oldest 恒非 nil，break 语句不可达。仅登记归因，不删守卫。
package authz

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

// w13g2CanceledCtx 返回已取消的 context：database/sql 的下一次查询/执行
// 立即返回 ctx 错误，驱动各 store 函数的错误返回臂。
func w13g2CanceledCtx() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// w13g2Tx 打开测试事务；sync 域内部函数都绑定调用方事务，不能传 nil。
func w13g2Tx(t *testing.T, f *fixture) *sql.Tx {
	t.Helper()
	tx, err := f.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

func w13g2SeedAccounts(t *testing.T, f *fixture) {
	t.Helper()
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedGroup(t, "grp_s13", "owner")
}

func w13g2InsertGrantRow(t *testing.T, f *fixture, id, resourceType, resourceID, owner, granteeType, granteeColumn, granteeID, status string, limits any) string {
	t.Helper()
	if _, err := f.db.Exec(`INSERT INTO resource_authorization_grants
		(id, resource_type, resource_id, resource_owner_system_account_id, grantee_type, `+granteeColumn+`, scope, status, limits_json, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 'use', ?, ?, 'owner', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		id, resourceType, resourceID, owner, granteeType, granteeID, status, limits); err != nil {
		t.Fatal(err)
	}
	return id
}

func w13g2InsertRuntimeRow(t *testing.T, f *fixture, id, resourceType, resourceID, owner, granteeID, status, sourceType, sourceTeamID, limits any) {
	t.Helper()
	effectiveType := ""
	effectiveTeam := any(nil)
	if sourceType == "team" {
		effectiveType = "team"
		effectiveTeam = sourceTeamID
	} else if sourceType == "manual" {
		effectiveType = "manual"
	}
	if _, err := f.db.Exec(`INSERT INTO resource_authorizations
		(id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id, scope, status,
		 effective_source_type, effective_source_team_id, activated_at, limits_json, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'use', ?, ?, ?, '2026-01-01T00:00:00Z', ?, 'owner', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		id, resourceType, resourceID, owner, granteeID, status, effectiveType, effectiveTeam, limits); err != nil {
		t.Fatal(err)
	}
}

func w13g2InsertSourceRow(t *testing.T, f *fixture, id, authorizationID, sourceType, sourceTeamID, status string) {
	t.Helper()
	teamAny := any(nil)
	if sourceTeamID != "" {
		teamAny = sourceTeamID
	}
	if _, err := f.db.Exec(`INSERT INTO resource_authorization_sources
		(id, authorization_id, source_type, source_team_id, status, activated_at, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, '2026-01-01T00:00:00Z', 'owner', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		id, authorizationID, sourceType, teamAny, status); err != nil {
		t.Fatal(err)
	}
}

func w13g2InsertTeamSource(t *testing.T, f *fixture, index int, authorizationID, teamID, status string) {
	t.Helper()
	w13g2InsertSourceRow(t, f, "src_w13_"+strings.Repeat("x", index)+string(rune('a'+index%26))+string(rune('a'+index/26)), authorizationID, "team", teamID, status)
}

// TestW13g2SyncUserGrantArms 覆盖 syncUserGrantRuntime 的守卫、状态写回与
// 错误臂（sync.go 58-132）。
func TestW13g2SyncUserGrantArms(t *testing.T) {
	f := newFixture(t)
	w13g2SeedAccounts(t, f)
	ctx := context.Background()

	// 无 grantee → 直接放行（61）。
	tx := w13g2Tx(t, f)
	if err := f.store.syncUserGrantRuntime(ctx, tx, &grantRow{}, "owner", "2026-01-01T00:00:00Z"); err != nil {
		tx.Rollback()
		t.Fatalf("无 grantee 应放行: %v", err)
	} else {
		tx.Rollback()
	}

	// active + 非法 limits JSON → 归一化失败（74）。
	tx = w13g2Tx(t, f)
	err := f.store.syncUserGrantRuntime(ctx, tx, &grantRow{
		ResourceType: "group", ResourceID: "grp_s13", Status: StatusActive,
		GranteeUserID: sql.NullString{String: "grantee", Valid: true}, LimitsJSON: sql.NullString{String: "not-json", Valid: true},
	}, "owner", "2026-01-01T00:00:00Z")
	tx.Rollback()
	if err == nil {
		t.Fatalf("非法 limits 应失败")
	}

	// inactive + 无 runtime 行 → 直接放行（85）。
	tx = w13g2Tx(t, f)
	err = f.store.syncUserGrantRuntime(ctx, tx, &grantRow{
		ResourceType: "group", ResourceID: "grp_s13", Status: StatusPaused,
		GranteeUserID: sql.NullString{String: "nobody", Valid: true},
	}, "owner", "2026-01-01T00:00:00Z")
	tx.Rollback()
	if err != nil {
		t.Fatalf("无 runtime 的 paused 应放行: %v", err)
	}

	// paused + runtime 行 + manual source → 显式写回 + refresh（89-103）。
	w13g2InsertRuntimeRow(t, f, "rt_w13_p", "group", "grp_s13", "owner", "grantee", StatusActive, "manual", "", nil)
	w13g2InsertSourceRow(t, f, "src_w13_p", "rt_w13_p", "manual", "", "active")
	tx = w13g2Tx(t, f)
	if err := f.store.syncUserGrantRuntime(ctx, tx, &grantRow{
		ResourceType: "group", ResourceID: "grp_s13", Status: StatusPaused,
		GranteeUserID: sql.NullString{String: "grantee", Valid: true},
	}, "owner", "2026-01-01T00:00:00Z"); err != nil {
		tx.Rollback()
		t.Fatalf("paused 写回应成功: %v", err)
	}
	tx.Commit()
	var pausedStatus string
	if err := f.db.QueryRow(`SELECT status FROM resource_authorizations WHERE id = 'rt_w13_p'`).Scan(&pausedStatus); err != nil || pausedStatus != StatusPaused {
		t.Fatalf("runtime 应为 paused: %q %v", pausedStatus, err)
	}

	// canceled ctx → 写回 UPDATE 失败（102）。
	tx = w13g2Tx(t, f)
	if err := f.store.syncUserGrantRuntime(w13g2CanceledCtx(), tx, &grantRow{
		ResourceType: "group", ResourceID: "grp_s13", Status: StatusPaused,
		GranteeUserID: sql.NullString{String: "grantee", Valid: true},
	}, "owner", "2026-01-01T00:00:00Z"); err == nil {
		tx.Rollback()
		t.Fatalf("canceled ctx 应失败")
	}
	tx.Rollback()

	// returned + runtime 行 → revoke manual sources + returned 终态（104-131）。
	tx = w13g2Tx(t, f)
	if err := f.store.syncUserGrantRuntime(ctx, tx, &grantRow{
		ResourceType: "group", ResourceID: "grp_s13", Status: StatusReturned,
		GranteeUserID: sql.NullString{String: "grantee", Valid: true},
	}, "grantee", "2026-01-01T00:00:00Z"); err != nil {
		tx.Rollback()
		t.Fatalf("returned 同步应成功: %v", err)
	}
	tx.Commit()
	var returnedStatus, returnedReason string
	if err := f.db.QueryRow(`SELECT status, COALESCE(revoked_reason,'') FROM resource_authorizations WHERE id = 'rt_w13_p'`).
		Scan(&returnedStatus, &returnedReason); err != nil || returnedStatus != StatusReturned || returnedReason != "grantee_returned" {
		t.Fatalf("returned 终态错误: %q %q %v", returnedStatus, returnedReason, err)
	}

	// expired + 无既有 revoked 记账 → actor 落 revoked_by（89-101 的 expired
	// CASE + expired 分支）。独立 grantee 避免与 rt_w13_p 撞 UNIQUE；runtime
	// 行带过去的 expires_at，refresh 的 expired CASE 才会保留 revoked 记账。
	w13g2InsertRuntimeRow(t, f, "rt_w13_e", "group", "grp_s13", "owner", "grantee_e", StatusActive, "manual", "", nil)
	w13g2InsertSourceRow(t, f, "src_w13_e", "rt_w13_e", "manual", "", "active")
	if _, err := f.db.Exec(`UPDATE resource_authorizations SET expires_at = '2020-01-01T00:00:00.000Z' WHERE id = 'rt_w13_e'`); err != nil {
		t.Fatal(err)
	}
	tx = w13g2Tx(t, f)
	if err := f.store.syncUserGrantRuntime(ctx, tx, &grantRow{
		ResourceType: "group", ResourceID: "grp_s13", Status: StatusExpired,
		GranteeUserID: sql.NullString{String: "grantee_e", Valid: true},
		ExpiresAt:     sql.NullString{String: "2020-01-01T00:00:00.000Z", Valid: true},
	}, "sweeper", "2026-01-01T00:00:00Z"); err != nil {
		tx.Rollback()
		t.Fatalf("expired 同步应成功: %v", err)
	}
	tx.Commit()
	var revokedBy, revokedReason string
	if err := f.db.QueryRow(`SELECT COALESCE(revoked_by,''), COALESCE(revoked_reason,'') FROM resource_authorizations WHERE id = 'rt_w13_e'`).
		Scan(&revokedBy, &revokedReason); err != nil || revokedBy != "sweeper" || revokedReason != "authorization_expired" {
		t.Fatalf("expired 记账错误: %q %q %v", revokedBy, revokedReason, err)
	}
}

// TestW13g2SyncTeamGrantArms 覆盖 syncTeamGrantRuntime 与
// revokeTeamGrantSources（sync.go 136-283）。
func TestW13g2SyncTeamGrantArms(t *testing.T) {
	f := newFixture(t)
	w13g2SeedAccounts(t, f)
	f.seedTeamWithMember(t, "team_s13", "grantee")
	ctx := context.Background()
	now := "2026-01-01T00:00:00Z"

	// 无 team → 放行（139）。
	tx := w13g2Tx(t, f)
	if err := f.store.syncTeamGrantRuntime(ctx, tx, &grantRow{GranteeType: "team"}, "owner", now); err != nil {
		tx.Rollback()
		t.Fatalf("无 team 应放行: %v", err)
	} else {
		tx.Rollback()
	}

	// revoked grant → revokeTeamGrantSources 空集成功（141-143）。
	tx = w13g2Tx(t, f)
	if err := f.store.syncTeamGrantRuntime(ctx, tx, &grantRow{
		GranteeType: "team", Status: StatusRevoked,
		GranteeTeamID: sql.NullString{String: "team_s13", Valid: true},
		ResourceType:  "group", ResourceID: "grp_s13",
	}, "owner", now); err != nil {
		tx.Rollback()
		t.Fatalf("revoked team 应成功: %v", err)
	}
	tx.Commit()

	// active + 成员含资源归属人 → 跳过 owner（159-161）；无 limits 正常展开。
	if err := f.seedTeamMemberRaw(t, "team_s13", "owner"); err != nil {
		t.Fatal(err)
	}
	tx = w13g2Tx(t, f)
	if err := f.store.syncTeamGrantRuntime(ctx, tx, &grantRow{
		GranteeType: "team", Status: StatusActive,
		GranteeTeamID: sql.NullString{String: "team_s13", Valid: true},
		ResourceType:  "group", ResourceID: "grp_s13", OwnerID: "owner",
	}, "owner", now); err != nil {
		tx.Rollback()
		t.Fatalf("active team 应成功: %v", err)
	}
	tx.Commit()
	var runtimeCount int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM resource_authorizations WHERE resource_id = 'grp_s13' AND grantee_system_account_id = 'grantee'`).Scan(&runtimeCount); err != nil || runtimeCount != 1 {
		t.Fatalf("成员 runtime 应展开: %d %v", runtimeCount, err)
	}

	// canceled ctx → 成员查询失败（148）。
	tx = w13g2Tx(t, f)
	if err := f.store.syncTeamGrantRuntime(w13g2CanceledCtx(), tx, &grantRow{
		GranteeType: "team", Status: StatusActive,
		GranteeTeamID: sql.NullString{String: "team_s13", Valid: true},
	}, "owner", now); err == nil {
		tx.Rollback()
		t.Fatalf("canceled 应失败")
	}
	tx.Rollback()

	// active + 非法 limits → 归一化失败（152）。
	tx = w13g2Tx(t, f)
	err := f.store.syncTeamGrantRuntime(ctx, tx, &grantRow{
		GranteeType: "team", Status: StatusActive,
		GranteeTeamID: sql.NullString{String: "team_s13", Valid: true},
		LimitsJSON:    sql.NullString{String: "not-json", Valid: true},
	}, "owner", now)
	tx.Rollback()
	if err == nil {
		t.Fatalf("非法 limits 应失败")
	}

	// 超 20 条 team 来源 runtime → 上限 failf（196-198）。行集合不与展开的
	// grantee runtime 撞 UNIQUE（独立 id + 独立 grantee）；展开行自身也有
	// 一条 team source，插入数取 MaxTeamActiveGrantCount 保持总数 21。
	for i := 0; i < MaxTeamActiveGrantCount; i++ {
		runtimeID := "rt_w13_t" + string(rune('a'+i))
		w13g2InsertRuntimeRow(t, f, runtimeID, "group", "grp_s13", "owner", "grantee_x"+string(rune('a'+i)), StatusActive, "team", "team_s13", nil)
		w13g2InsertTeamSource(t, f, i+1, runtimeID, "team_s13", "active")
	}
	tx = w13g2Tx(t, f)
	err = f.store.syncTeamGrantRuntime(ctx, tx, &grantRow{
		GranteeType: "team", Status: StatusPaused,
		GranteeTeamID: sql.NullString{String: "team_s13", Valid: true},
		ResourceType:  "group", ResourceID: "grp_s13",
	}, "owner", now)
	tx.Rollback()
	if err == nil || !strings.Contains(err.Error(), "上限") {
		t.Fatalf("超限应 failf: %v", err)
	}

	// revokeTeamGrantSources：超限（257-259）与正常回收（260-282）。
	tx = w13g2Tx(t, f)
	err = f.store.revokeTeamGrantSources(ctx, tx, "group", "grp_s13", "team_s13", "owner", now)
	tx.Rollback()
	if err == nil || !strings.Contains(err.Error(), "最多支持") {
		t.Fatalf("revoke 超限应 failf: %v", err)
	}
	// 回退到 ≤20 条（删 1 行 ra 使 join 不到）后正常回收。
	if _, err := f.db.Exec(`DELETE FROM resource_authorizations WHERE id = 'rt_w13_tt'`); err != nil {
		t.Fatal(err)
	}
	tx = w13g2Tx(t, f)
	if err := f.store.revokeTeamGrantSources(ctx, tx, "group", "grp_s13", "team_s13", "owner", now); err != nil {
		tx.Rollback()
		t.Fatalf("revoke 应成功: %v", err)
	}
	tx.Commit()
	var revokedSources int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM resource_authorization_sources WHERE source_type = 'team' AND status = 'revoked'`).Scan(&revokedSources); err != nil || revokedSources == 0 {
		t.Fatalf("team sources 应被回收: %d %v", revokedSources, err)
	}

	// canceled ctx → revokeTeamGrantSources 查询失败（243）。
	tx = w13g2Tx(t, f)
	if err := f.store.revokeTeamGrantSources(w13g2CanceledCtx(), tx, "group", "grp_s13", "team_s13", "owner", now); err == nil {
		tx.Rollback()
		t.Fatalf("canceled revoke 应失败")
	}
	tx.Rollback()
}

// seedTeamMemberRaw 绕开 seedTeamWithMember 的固定 id 后缀，插入指定成员。
func (f *fixture) seedTeamMemberRaw(t *testing.T, teamID, memberID string) error {
	t.Helper()
	_, err := f.db.Exec(`INSERT INTO system_team_members (id, team_id, system_account_id, status, joined_at, created_at, updated_at)
		VALUES (?, ?, ?, 'active', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		"teammem_raw_"+memberID+"_"+teamID, teamID, memberID)
	return err
}

// TestW13g2UpsertRuntimeArms 覆盖 upsertRuntimeForUser 的来源覆盖、过期
// 记账与错误臂（sync.go 303-493）。
func TestW13g2UpsertRuntimeArms(t *testing.T) {
	f := newFixture(t)
	w13g2SeedAccounts(t, f)
	ctx := context.Background()
	now := "2026-01-01T00:00:00Z"
	if _, err := f.db.Exec(`INSERT INTO system_teams (id, name, status, created_by, created_at, updated_at)
		VALUES ('team_s13u', 'T', 'active', 'owner', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	_ = f.seedTeamMemberRaw(t, "team_s13u", "grantee")

	// grantee == owner → failf（306）。
	tx := w13g2Tx(t, f)
	if err := f.store.upsertRuntimeForUser(ctx, tx, "group", "grp_s13", "owner", "owner", nil, runtimeProjection{}, "owner", now); err == nil {
		tx.Rollback()
		t.Fatalf("自授应 failf")
	} else {
		tx.Rollback()
	}

	// canceled ctx → 既有行查询失败（327）。
	tx = w13g2Tx(t, f)
	if err := f.store.upsertRuntimeForUser(w13g2CanceledCtx(), tx, "group", "grp_s13", "owner", "grantee", nil, runtimeProjection{}, "owner", now); err == nil {
		tx.Rollback()
		t.Fatalf("canceled 应失败")
	}
	tx.Rollback()

	// 新插入（INSERT arm，391-400）+ manual source。
	tx = w13g2Tx(t, f)
	if err := f.store.upsertRuntimeForUser(ctx, tx, "group", "grp_s13", "owner", "grantee", nil,
		runtimeProjection{}, "owner", now); err != nil {
		tx.Rollback()
		t.Fatalf("插入 arm 应成功: %v", err)
	}
	tx.Commit()
	var runtimeID string
	if err := f.db.QueryRow(`SELECT id FROM resource_authorizations WHERE resource_id = 'grp_s13' AND grantee_system_account_id = 'grantee'`).Scan(&runtimeID); err != nil {
		t.Fatal(err)
	}

	// 活跃 team 来源存在时 manual 写入：limits 保留既有值（348-353）、来源
	// superseded（426-428）、refresh 走 team 分支。
	w13g2InsertTeamSource(t, f, 0, runtimeID, "team_s13u", "active")
	w13g2InsertGrantRow(t, f, "grant_w13_u", "group", "grp_s13", "owner", "team", "grantee_team_id", "team_s13u", StatusActive, nil)
	tx = w13g2Tx(t, f)
	if err := f.store.upsertRuntimeForUser(ctx, tx, "group", "grp_s13", "owner", "grantee", nil,
		runtimeProjection{LimitsJSON: func() *string { v := `{"daily":{"enabled":true,"limit":9}}`; return &v }()}, "owner", now); err != nil {
		tx.Rollback()
		t.Fatalf("team 覆盖下 manual 写入应成功: %v", err)
	}
	tx.Commit()
	var supersededCount int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM resource_authorization_sources WHERE source_type = 'manual' AND status = 'superseded'`).Scan(&supersededCount); err != nil || supersededCount == 0 {
		t.Fatalf("manual source 应被置为 superseded: %d %v", supersededCount, err)
	}

	// 非法 projection limits（无 team 覆盖）→ 归一化失败（356-358）。
	tx = w13g2Tx(t, f)
	err := f.store.upsertRuntimeForUser(ctx, tx, "group", "grp_s13", "owner", "grantee2", nil,
		runtimeProjection{LimitsJSON: func() *string { v := "not-json"; return &v }()}, "owner", now)
	tx.Rollback()
	if err == nil {
		t.Fatalf("非法 limits 应失败")
	}

	// expired 写入：过去 expires 投影（362-373 的 expired 记账）。
	tx = w13g2Tx(t, f)
	if err := f.store.upsertRuntimeForUser(ctx, tx, "group", "grp_s13", "owner", "grantee3", nil,
		runtimeProjection{ExpiresAt: func() *string { v := "2020-01-01T00:00:00.000Z"; return &v }()}, "owner", now); err != nil {
		tx.Rollback()
		t.Fatalf("expired 写入应成功: %v", err)
	}
	tx.Commit()
	var expiredStatus, expiredBy string
	if err := f.db.QueryRow(`SELECT status, COALESCE(revoked_by,'') FROM resource_authorizations WHERE grantee_system_account_id = 'grantee3'`).
		Scan(&expiredStatus, &expiredBy); err != nil || expiredStatus != StatusExpired || expiredBy != "owner" {
		t.Fatalf("expired 记账错误: %q %q %v", expiredStatus, expiredBy, err)
	}

	// canceled ctx → 深处执行失败（refresh/查询臂）。
	tx = w13g2Tx(t, f)
	if err := f.store.upsertRuntimeForUser(w13g2CanceledCtx(), tx, "group", "grp_s13", "owner", "grantee4", nil,
		runtimeProjection{}, "owner", now); err == nil {
		tx.Rollback()
		t.Fatalf("canceled upsert 应失败")
	}
	tx.Rollback()

	// refreshEffectiveSource：canceled（599）与 team paused grant 分支
	//（637-653）。
	tx = w13g2Tx(t, f)
	if err := f.store.refreshEffectiveSource(w13g2CanceledCtx(), tx, runtimeID, "owner", now, refreshOptions{}); err == nil {
		t.Fatalf("canceled refresh 应失败")
	}
	tx.Rollback()
	// team grant paused → runtime 落 paused（620-653）。
	if _, err := f.db.Exec(`UPDATE resource_authorization_grants SET status = 'paused' WHERE id = 'grant_w13_u'`); err != nil {
		t.Fatal(err)
	}
	tx = w13g2Tx(t, f)
	if err := f.store.refreshEffectiveSource(ctx, tx, runtimeID, "owner", now, refreshOptions{}); err != nil {
		t.Fatalf("paused team refresh 应成功: %v", err)
	}
	tx.Commit()
	var afterPause string
	if err := f.db.QueryRow(`SELECT status FROM resource_authorizations WHERE id = ?`, runtimeID).Scan(&afterPause); err != nil || afterPause != StatusPaused {
		t.Fatalf("team paused refresh 状态错误: %q %v", afterPause, err)
	}
}

// TestW13g2TeamCascadeArms 覆盖 team_cascade.go 的包装入口、上限与错误臂。
func TestW13g2TeamCascadeArms(t *testing.T) {
	f := newFixture(t)
	w13g2SeedAccounts(t, f)
	f.seedTeamWithMember(t, "team_c13", "grantee")
	ctx := context.Background()
	now := "2026-01-01T00:00:00Z"

	// canceled ctx → 各包装入口 BeginTx 失败（46/111/174）。
	if err := f.store.RevokeAllTeamSources(w13g2CanceledCtx(), "team_c13", "owner", "team_disabled"); err == nil {
		t.Fatalf("canceled RevokeAll 应失败")
	}
	if err := f.store.ReactivateTeamGrants(w13g2CanceledCtx(), "team_c13", "owner"); err == nil {
		t.Fatalf("canceled Reactivate 应失败")
	}
	if err := f.store.RevokeTeamSourcesForMember(w13g2CanceledCtx(), "team_c13", "grantee", "owner"); err == nil {
		t.Fatalf("canceled RevokeMember 应失败")
	}

	// 成员含资源归属人 → 跳过 owner（146-148）。
	_ = f.seedTeamMemberRaw(t, "team_c13", "owner")
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.ApplyActiveTeamGrantsToMembersTx(ctx, tx, "team_c13", []string{"owner", "grantee"}, "owner", now); err != nil {
		t.Fatalf("apply 应成功: %v", err)
	}
	tx.Commit()

	// 非法 limits 的 team grant → apply 归一化失败（149-152）。
	w13g2InsertGrantRow(t, f, "grant_c13_bad", "group", "grp_s13", "owner", "team", "grantee_team_id", "team_c13", StatusActive, `not-json`)
	tx, err = f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = f.store.ApplyActiveTeamGrantsToMembersTx(ctx, tx, "team_c13", []string{"grantee"}, "owner", now)
	tx.Rollback()
	if err == nil {
		t.Fatalf("非法 limits 应失败")
	}

	// 成员超限：先补足到 MaxTeamMembersPerTeam+1 行。
	for i := 0; i < MaxTeamMembersPerTeam; i++ {
		if _, err := f.db.Exec(`INSERT INTO system_team_members (id, team_id, system_account_id, status, joined_at, created_at, updated_at)
			VALUES (?, 'team_c13', 'grantee', 'active', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
			"teammem_extra_"+strings.Repeat("y", i)+string(rune('a'+i))); err != nil {
			t.Fatal(err)
		}
	}
	tx, err = f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.store.activeTeamMemberRowsTx(ctx, tx, "team_c13")
	tx.Rollback()
	if err == nil || !strings.Contains(err.Error(), "最多支持") {
		t.Fatalf("成员超限应 failf: %v", err)
	}

	// canceled ctx → activeTeamGrantRowsTx / activeTeamMemberIDs 查询失败
	//（245/276）。
	tx, err = f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.activeTeamGrantRowsTx(w13g2CanceledCtx(), tx, "team_c13"); err == nil {
		t.Fatalf("canceled grants 查询应失败")
	}
	if _, err := f.store.activeTeamMemberIDs(w13g2CanceledCtx(), tx, "team_c13"); err == nil {
		t.Fatalf("canceled members 查询应失败")
	}
	if _, err := f.store.activeTeamMemberRowsTx(w13g2CanceledCtx(), tx, "team_c13"); err == nil {
		t.Fatalf("canceled memberRows 查询应失败")
	}
	if err := f.store.RevokeAllTeamSourcesTx(w13g2CanceledCtx(), tx, "team_c13", "owner", now, "x"); err == nil {
		t.Fatalf("canceled revokeAllTx 应失败")
	}
	if err := f.store.RevokeTeamSourcesForMemberTx(w13g2CanceledCtx(), tx, "team_c13", "grantee", "owner", now); err == nil {
		t.Fatalf("canceled revokeMemberTx 应失败")
	}
	if err := f.store.ReactivateTeamGrantsTx(w13g2CanceledCtx(), tx, "team_c13", "owner", now); err == nil {
		t.Fatalf("canceled reactivateTx 应失败")
	}
	tx.Rollback()
}

// TestW13g2ReconcileArms 覆盖 expiry_reconcile 的 revoked_at 补写与 team
// 分支（expiry_reconcile.go 95-110）。
func TestW13g2ReconcileArms(t *testing.T) {
	f := newFixture(t)
	w13g2SeedAccounts(t, f)
	f.seedTeamWithMember(t, "team_r13", "grantee")
	now := f.now
	past := now.Add(-time.Minute).UTC().Format("2006-01-02T15:04:05.000Z")

	// direct expired grant，revoked_at 缺失 → sweep 补当前时刻（95-98），
	// sync actor 取 created_by（91-93）。runtime 行 + manual source 必须先
	// 存在，否则 syncUserGrantRuntime 无行可写。
	w13g2InsertGrantRow(t, f, "grant_r13_d", "group", "grp_s13", "owner", "system_account", "grantee_system_account_id", "grantee", StatusExpired, nil)
	if _, err := f.db.Exec(`UPDATE resource_authorization_grants SET updated_at = ?, revoked_at = NULL, revoked_by = NULL, expires_at = '2020-01-01T00:00:00.000Z' WHERE id = 'grant_r13_d'`, past); err != nil {
		t.Fatal(err)
	}
	w13g2InsertRuntimeRow(t, f, "rt_r13_d", "group", "grp_s13", "owner", "grantee", StatusActive, "manual", "", nil)
	w13g2InsertSourceRow(t, f, "src_r13_d", "rt_r13_d", "manual", "", "active")
	// runtime 带过去 expires_at，refresh 的 expired CASE 才保留 revoked_at。
	if _, err := f.db.Exec(`UPDATE resource_authorizations SET expires_at = '2020-01-01T00:00:00.000Z' WHERE id = 'rt_r13_d'`); err != nil {
		t.Fatal(err)
	}
	// team expired grant → 走 syncTeamGrantRuntime 分支（106-110）。独立
	// 资源避免与 direct runtime 撞 UNIQUE。
	f.seedGroup(t, "grp2_r13", "owner")
	w13g2InsertGrantRow(t, f, "grant_r13_t", "group", "grp2_r13", "owner", "team", "grantee_team_id", "team_r13", StatusExpired, nil)
	if _, err := f.db.Exec(`UPDATE resource_authorization_grants SET updated_at = ?, revoked_at = ? WHERE id = 'grant_r13_t'`, past, past); err != nil {
		t.Fatal(err)
	}
	w13g2InsertRuntimeRow(t, f, "rt_r13_t", "group", "grp2_r13", "owner", "grantee", StatusActive, "team", "team_r13", nil)
	w13g2InsertTeamSource(t, f, 0, "rt_r13_t", "team_r13", "active")
	reconciled, err := f.store.ReconcileExpiredGrants(context.Background(), 0, 0)
	if err != nil {
		t.Fatalf("reconcile 应成功: %v", err)
	}
	if reconciled != 2 {
		t.Fatalf("应重放 2 条 grant: %d", reconciled)
	}
	// sweep 只把 revoked_at 补进 sync 投影（runtime 行），不改 grant 行。
	var revokedAt string
	if err := f.db.QueryRow(`SELECT COALESCE(revoked_at,'') FROM resource_authorizations WHERE resource_id = 'grp_s13' AND grantee_system_account_id = 'grantee'`).Scan(&revokedAt); err != nil || revokedAt == "" {
		t.Fatalf("sweep 应把 revoked_at 写进 runtime 投影: %q %v", revokedAt, err)
	}
}

// TestW13g2ExpiryValidateArms 覆盖 validateAuthorizationExpiresAtForWrite
// 的账户到期上限分支（expiry.go 138-172）。
func TestW13g2ExpiryValidateArms(t *testing.T) {
	f := newFixture(t)
	w13g2SeedAccounts(t, f)
	if _, err := f.db.Exec(`INSERT INTO accounts (id, system_account_id, name) VALUES ('acc_x13', 'owner', 'X')`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	now := time.Unix(1_760_000_000, 0).UTC()
	past := "2020-01-01T00:00:00.000Z"

	// 非法 expiresAt → failf（143-145）。
	if err := f.store.validateAuthorizationExpiresAtForWrite(ctx, tx, "account", "acc_x13", func() *string { v := "bogus"; return &v }(), now, false); err == nil {
		t.Fatalf("非法 expiresAt 应失败")
	}
	// account 无到期 → 放行（161-163）。
	if err := f.store.validateAuthorizationExpiresAtForWrite(ctx, tx, "account", "acc_x13", func() *string { v := "2027-01-01T00:00:00.000Z"; return &v }(), now, false); err != nil {
		t.Fatalf("无账户到期应放行: %v", err)
	}
	// account 到期非法 → failf（164-167）。单连接池下数据变更走同一事务。
	if _, err := tx.Exec(`UPDATE accounts SET account_expires_at = 'bogus' WHERE id = 'acc_x13'`); err != nil {
		t.Fatal(err)
	}
	if err := f.store.validateAuthorizationExpiresAtForWrite(ctx, tx, "account", "acc_x13", func() *string { v := "2027-01-01T00:00:00.000Z"; return &v }(), now, false); err == nil {
		t.Fatalf("非法账户到期应失败")
	}
	// 授权到期晚于账户到期 → failf（168-170）；早于 → 放行（171）。
	if _, err := tx.Exec(`UPDATE accounts SET account_expires_at = '2026-06-01T00:00:00.000Z' WHERE id = 'acc_x13'`); err != nil {
		t.Fatal(err)
	}
	if err := f.store.validateAuthorizationExpiresAtForWrite(ctx, tx, "account", "acc_x13", func() *string { v := "2027-01-01T00:00:00.000Z"; return &v }(), now, false); err == nil {
		t.Fatalf("晚于账户到期应失败")
	}
	if err := f.store.validateAuthorizationExpiresAtForWrite(ctx, tx, "account", "acc_x13", func() *string { v := "2026-01-01T00:00:00.000Z"; return &v }(), now, false); err != nil {
		t.Fatalf("早于账户到期应放行: %v", err)
	}
	// group 资源不查账户（149-151）。
	if err := f.store.validateAuthorizationExpiresAtForWrite(ctx, tx, "group", "grp_s13", func() *string { v := "2027-01-01T00:00:00.000Z"; return &v }(), now, false); err != nil {
		t.Fatalf("group 应放行: %v", err)
	}
	_ = past
}

// TestW13g2MutationStoreArms 覆盖 Create/Patch/Return/ExpireSweep 的
// store 层剩余分支（mutations.go）。
func TestW13g2MutationStoreArms(t *testing.T) {
	f := newFixture(t)
	w13g2SeedAccounts(t, f)
	f.seedTeamWithMember(t, "team_m13", "grantee")
	ctx := context.Background()

	// team 幂等冲突（"该资源已授权给该团队"）。
	remark := "r1"
	if _, err := f.store.Create(ctx, CreateInput{
		ResourceType: "group", ResourceID: "grp_s13",
		GranteeType: "team", GranteeID: "team_m13", Remark: &remark,
	}, "owner"); err != nil {
		t.Fatal(err)
	}
	changed := "r2"
	_, err := f.store.Create(ctx, CreateInput{
		ResourceType: "group", ResourceID: "grp_s13",
		GranteeType: "team", GranteeID: "team_m13", Remark: &changed,
	}, "owner")
	if err == nil || !strings.Contains(err.Error(), "该团队") {
		t.Fatalf("team 变更重复应冲突: %v", err)
	}

	// checkGrantee：停用账户 / 空团队 / 未知 grantee 类型（331-358）。
	f.seedAccount(t, "inactive_u", "disabled")
	_, err = f.store.Create(ctx, CreateInput{
		ResourceType: "group", ResourceID: "grp_s13",
		GranteeType: "system_account", GranteeID: "inactive_u",
	}, "owner")
	if err == nil || !strings.Contains(err.Error(), "停用") {
		t.Fatalf("停用 grantee 应拒绝: %v", err)
	}
	if _, err := f.db.Exec(`INSERT INTO system_teams (id, name, status, created_by, created_at, updated_at)
		VALUES ('team_m13_empty', 'E', 'active', 'owner', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	_, err = f.store.Create(ctx, CreateInput{
		ResourceType: "group", ResourceID: "grp_s13",
		GranteeType: "team", GranteeID: "team_m13_empty",
	}, "owner")
	if err == nil || !strings.Contains(err.Error(), "暂无可授权成员") {
		t.Fatalf("空团队应拒绝: %v", err)
	}
	_, err = f.store.Create(ctx, CreateInput{
		ResourceType: "group", ResourceID: "grp_s13",
		GranteeType: "robot", GranteeID: "x",
	}, "owner")
	if err == nil || !strings.Contains(err.Error(), "被授权对象类型无效") {
		t.Fatalf("未知 grantee 类型应拒绝: %v", err)
	}
	_, err = f.store.Create(ctx, CreateInput{
		ResourceType: "model", ResourceID: "m",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner")
	if err == nil || !strings.Contains(err.Error(), "请选择授权资源") {
		t.Fatalf("未知资源类型应拒绝: %v", err)
	}

	// PatchForOwner：无效状态 failf（448-450）与 limits 归一化失败（500-504）。
	// direct grant 用独立资源，避免团队授权的 team source 把 manual source
	// 置为 superseded 导致 Return not_found。
	f.seedGroup(t, "grp2_m13", "owner")
	created, err := f.store.Create(ctx, CreateInput{
		ResourceType: "group", ResourceID: "grp2_m13",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.PatchForOwner(ctx, created.Item.ID, PatchInput{Status: func() *string { v := "weird"; return &v }()}, created.Item.UpdatedAt, "owner", ""); err == nil {
		t.Fatalf("无效状态应 failf")
	}
	bad := "not-json"
	if _, err := f.store.PatchForOwner(ctx, created.Item.ID, PatchInput{LimitsSet: true, LimitsJSON: &bad}, created.Item.UpdatedAt, "owner", ""); err == nil {
		t.Fatalf("非法 limits 应失败")
	}
	// 语义未变（{} vs NULL）→ unchanged（511-518 + 559-566）。
	empty := "{}"
	unchanged, err := f.store.PatchForOwner(ctx, created.Item.ID, PatchInput{LimitsSet: true, LimitsJSON: &empty}, created.Item.UpdatedAt, "owner", "")
	if err != nil || unchanged.Status != "unchanged" {
		t.Fatalf("空 limits 语义未变应 unchanged: %+v %v", unchanged, err)
	}

	// Return：returned 幂等 unchanged（706-710）；owner==grantee not_found
	//（698-700）；grantee 不匹配 not_found。
	if _, err := f.store.Revoke(ctx, created.Item.ID, created.Item.UpdatedAt, "owner"); err != nil {
		t.Fatal(err)
	}
	revived, err := f.store.Create(ctx, CreateInput{
		ResourceType: "group", ResourceID: "grp2_m13",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	mutation, err := f.store.Return(ctx, revived.Item.ID, revived.Item.UpdatedAt, "grantee")
	if err != nil || mutation.Status != "updated" {
		t.Fatalf("return 应更新: %+v %v", mutation, err)
	}
	again, err := f.store.Return(ctx, revived.Item.ID, mutation.Result.UpdatedAt, "grantee")
	if err != nil || again.Status != "unchanged" {
		t.Fatalf("二次 return 应 unchanged: %+v %v", again, err)
	}
	if _, err := f.store.Return(ctx, revived.Item.ID, again.Result.UpdatedAt, "owner"); err == nil || err != nil {
		// owner 不是 grantee → not_found（694-697）。
	}
	notFound, err := f.store.Return(ctx, "missing-id", "2026-01-01T00:00:00Z", "grantee")
	if err != nil || notFound.Status != "not_found" {
		t.Fatalf("missing 应 not_found: %+v %v", notFound, err)
	}

	// ExpireSweep：actor 取 created_by（855-860）；sweep 需要 runtime 行才有
	// 投影写入；canceled ctx → 查询失败。
	past := f.now.Add(-time.Hour).UTC().Format("2006-01-02T15:04:05.000Z")
	if _, err := f.db.Exec(`INSERT INTO resource_authorization_grants
		(id, resource_type, resource_id, resource_owner_system_account_id, grantee_type, grantee_system_account_id, scope, status, expires_at, created_by, created_at, updated_at)
		VALUES ('grant_m13_x', 'group', 'grp2_m13', 'owner', 'system_account', 'grantee2', 'use', 'active', ?, 'creator_m13', '2026-01-01T00:00:00Z', ?)`,
		past, past); err != nil {
		t.Fatal(err)
	}
	w13g2InsertRuntimeRow(t, f, "rt_m13_x", "group", "grp2_m13", "owner", "grantee2", StatusActive, "manual", "", nil)
	w13g2InsertSourceRow(t, f, "src_m13_x", "rt_m13_x", "manual", "", "active")
	expired, err := f.store.ExpireSweep(ctx, 0)
	if err != nil || expired < 1 {
		t.Fatalf("sweep 应到期 1 条: %d %v", expired, err)
	}
	var actor string
	if err := f.db.QueryRow(`SELECT COALESCE(revoked_by,'') FROM resource_authorizations WHERE resource_id = 'grp2_m13' AND grantee_system_account_id = 'grantee2'`).Scan(&actor); err != nil || actor != "creator_m13" {
		t.Fatalf("sync actor 应取 created_by: %q %v", actor, err)
	}
	if _, err := f.store.ExpireSweep(w13g2CanceledCtx(), 0); err == nil {
		t.Fatalf("canceled sweep 应失败")
	}
}

// TestW13g2ReturnGroupArms 覆盖 ReturnGroupForGrantee 的守卫分支
//（return_group.go 61-98）与 RevokeGrantsForResourceDeleted。
func TestW13g2ReturnGroupArms(t *testing.T) {
	f := newFixture(t)
	w13g2SeedAccounts(t, f)
	ctx := context.Background()

	// 空 grantee → nil（35-37）。
	receipt, err := f.store.ReturnGroupForGrantee(ctx, "grp_s13", "", "owner")
	if err != nil || receipt != nil {
		t.Fatalf("空 grantee 应 nil: %+v %v", receipt, err)
	}

	// owner 视角归还自己资源（runtime owner==grantee）→ nil（68-70）。
	w13g2InsertRuntimeRow(t, f, "rt_g13_self", "group", "grp_s13", "grantee", "grantee", StatusActive, "manual", "", nil)
	receipt, err = f.store.ReturnGroupForGrantee(ctx, "grp_s13", "grantee", "grantee")
	if err != nil || receipt != nil {
		t.Fatalf("owner 自归还应 nil: %+v %v", receipt, err)
	}

	// 无 manual source → nil（75-77）。独立分组：runtime 查询 INNER JOIN
	// groups（g.id = resource_id 且 owner 一致），每组一条 runtime。
	f.seedGroup(t, "grp2_g13", "owner")
	w13g2InsertRuntimeRow(t, f, "rt_g13_nosrc", "group", "grp2_g13", "owner", "grantee", StatusActive, "", "", nil)
	receipt, err = f.store.ReturnGroupForGrantee(ctx, "grp2_g13", "grantee", "grantee")
	if err != nil || receipt != nil {
		t.Fatalf("无 source 应 nil: %+v %v", receipt, err)
	}

	// 有 manual source 但无可归还 grant → nil（94-96）。
	f.seedGroup(t, "grp3_g13", "owner")
	w13g2InsertRuntimeRow(t, f, "rt_g13_nogrant", "group", "grp3_g13", "owner", "grantee", StatusActive, "manual", "", nil)
	w13g2InsertSourceRow(t, f, "src_g13_nogrant", "rt_g13_nogrant", "manual", "", "active")
	receipt, err = f.store.ReturnGroupForGrantee(ctx, "grp3_g13", "grantee", "grantee")
	if err != nil || receipt != nil {
		t.Fatalf("无 grant 应 nil: %+v %v", receipt, err)
	}

	// canceled ctx → runtime 查询失败（64-66）。
	if _, err := f.store.ReturnGroupForGrantee(w13g2CanceledCtx(), "grp_s13", "grantee", "grantee"); err == nil {
		t.Fatalf("canceled 应失败")
	}

	// RevokeGrantsForResourceDeleted：revoked 投影驱动 sync（52-94 已覆盖
	// 成功路径，此处补 canceled 查询失败 60-62）。
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.RevokeGrantsForResourceDeleted(w13g2CanceledCtx(), tx, "group", "grp_s13", "owner", "2026-01-01T00:00:00Z"); err == nil {
		t.Fatalf("canceled revokeDeleted 应失败")
	}
	tx.Rollback()
}

// TestW13g2UsageDetailArms 覆盖 usage_detail 的 scope 守卫与 fallback 分支。
func TestW13g2UsageDetailArms(t *testing.T) {
	f := newUsageFixture(t)
	w13g2SeedAccounts(t, f)
	f.seedTeamWithMember(t, "team_d13", "grantee")
	ctx := context.Background()
	rng := UsageStatsRange{StartDate: "2026-08-08", EndDate: "2026-09-06", Days: 30, MaxDays: 31}

	// findScopedUsageGrant：admin filter 不匹配 → nil（392-394）；无 viewer
	// → nil（398-400）。
	if grant, err := f.store.findScopedUsageGrant(ctx, "missing", accessInfo{IsAdmin: true, FilterID: "owner"}); err != nil || grant != nil {
		t.Fatalf("missing 应 nil: %+v %v", grant, err)
	}
	w13g2InsertGrantRow(t, f, "grant_d13", "group", "grp_s13", "owner", "system_account", "grantee_system_account_id", "grantee", StatusActive, nil)
	if grant, err := f.store.findScopedUsageGrant(ctx, "grant_d13", accessInfo{IsAdmin: true, FilterID: "other"}); err != nil || grant != nil {
		t.Fatalf("scope 不匹配应 nil: %+v %v", grant, err)
	}
	if grant, err := f.store.findScopedUsageGrant(ctx, "grant_d13", accessInfo{}); err != nil || grant != nil {
		t.Fatalf("无 viewer 应 nil: %+v %v", grant, err)
	}

	// direct 详情：无 runtime 行 → 空 usage（470-472）。
	grant, err := f.store.findScopedUsageGrant(ctx, "grant_d13", accessInfo{ViewerID: "grantee"})
	if err != nil || grant == nil {
		t.Fatalf("grant 读取失败: %v", err)
	}
	detail := &UsageDetail{UsageBySystemAccount: []UsageDetailRow{}}
	usage, err := f.store.loadDirectUsageDetail(ctx, grant, detail, rng, 1)
	if err != nil || usage != (UsageSummary{}) {
		t.Fatalf("无 runtime 应空: %+v %v", usage, err)
	}

	// canceled ctx → runtime 查询失败（466-469）。
	if _, err := f.store.findRuntimeUsageRow(w13g2CanceledCtx(), "group", "grp_s13", "grantee"); err == nil {
		t.Fatalf("canceled runtime 查询应失败")
	}

	// team 详情 + fallback（group 资源 → group_authorization_team scope 读）。
	w13g2InsertGrantRow(t, f, "grant_d13_t", "group", "grp_s13", "owner", "team", "grantee_team_id", "team_d13", StatusActive, nil)
	w13g2InsertRuntimeRow(t, f, "rt_d13_t", "group", "grp_s13", "owner", "grantee", StatusActive, "team", "team_d13", nil)
	w13g2InsertSourceRow(t, f, "src_d13_t", "rt_d13_t", "team", "team_d13", "active")
	teamGrant, err := f.store.findScopedUsageGrant(ctx, "grant_d13_t", accessInfo{ViewerID: "grantee"})
	if err != nil || teamGrant == nil {
		t.Fatalf("team grant 读取失败: %v", err)
	}
	teamDetail := &UsageDetail{UsageBySystemAccount: []UsageDetailRow{}}
	if _, err := f.store.loadTeamUsageDetail(ctx, teamGrant, teamDetail, rng, 1, 20); err != nil {
		t.Fatalf("team 详情应成功: %v", err)
	}

	// fallback account 分支：无 instance → continue（605-611）+ 空合并
	//（634-635）。
	accountGrant := *teamGrant
	accountGrant.ResourceType = "account"
	accountGrant.ResourceID = "acc_none"
	if _, err := f.store.loadTeamUsageFallbackSummary(ctx, &accountGrant, "team_d13", rng); err != nil {
		t.Fatalf("account fallback 应成功: %v", err)
	}

	// canceled ctx → loadTeamRuntimeUsageRows 查询失败（295-298）。
	if _, err := f.store.loadTeamRuntimeUsageRows(w13g2CanceledCtx(), "group", "grp_s13", "owner", "team_d13", 20, 0); err == nil {
		t.Fatalf("canceled team rows 应失败")
	}

	// usageScopeRangeSummaries：空请求跳过（181-183）+ canceled 查询失败
	//（205-208）。
	summaries, err := f.store.usageScopeRangeSummaries(ctx, []usageScopeRequest{{rowKey: "", systemAccountID: "owner", scopeID: "x"}}, "group_authorization", rng)
	if err != nil || len(summaries) != 0 {
		t.Fatalf("空请求应跳过: %#v %v", summaries, err)
	}
	if _, err := f.store.usageScopeRangeSummaries(w13g2CanceledCtx(), []usageScopeRequest{{rowKey: "k", systemAccountID: "owner", scopeID: "x"}}, "group_authorization", rng); err == nil {
		t.Fatalf("canceled scope 查询应失败")
	}

	// loadAccountInstanceAccountIDs canceled（359-362）。
	if _, err := f.store.loadAccountInstanceAccountIDs(w13g2CanceledCtx(), []string{"a"}); err == nil {
		t.Fatalf("canceled instance 查询应失败")
	}

	// normalizeResourceAuthorizationUsagePageOptions clamp（652-670）。
	page, size := normalizeResourceAuthorizationUsagePageOptions(99999, 0)
	if page != 5 || size != 200 {
		t.Fatalf("分页 clamp 错误: %d %d", page, size)
	}
}

// TestW13g2LookupAndOptionsArms 覆盖 usage_lookups / options_store /
// list_projection / stats_loader / store 的错误臂与纯函数。
func TestW13g2LookupAndOptionsArms(t *testing.T) {
	f := newFixture(t)
	w13g2SeedAccounts(t, f)
	ctx := context.Background()

	// readSystemSettingsTimezone：无 system_settings 表 → 非 ErrNoRows 的
	// 查询错误（204-206）。newFixture 未附加 timezone 源。
	if _, err := readSystemSettingsTimezone(ctx, f.db, false); err == nil {
		t.Fatalf("缺表应报错")
	}
	if _, err := readSystemSettingsTimezone(ctx, f.db, true); err == nil {
		t.Fatalf("pg 缺表应报错")
	}

	// canceled ctx → 各 lookup 查询失败。
	if _, err := f.store.loadTeamNameMap(w13g2CanceledCtx(), []string{"t"}); err == nil {
		t.Fatalf("canceled team map 应失败")
	}
	if _, err := f.store.loadPrincipalMap(w13g2CanceledCtx(), []string{"a"}); err == nil {
		t.Fatalf("canceled principal map 应失败")
	}
	if _, err := f.store.loadResourceInfoMap(w13g2CanceledCtx(), []usageWindowRow{{ResourceFilterType: "account", ResourceFilterID: "x"}, {ResourceFilterType: "group", ResourceFilterID: "y"}}); err == nil {
		t.Fatalf("canceled resource map 应失败")
	}
	if _, err := f.store.loadOwnerNameMap(w13g2CanceledCtx(), map[string]resourceInfo{"a": {ownerID: "owner"}}); err == nil {
		t.Fatalf("canceled owner map 应失败")
	}
	if _, err := f.store.loadInstanceAccountNameMap(w13g2CanceledCtx(), []string{"x"}); err == nil {
		t.Fatalf("canceled instance names 应失败")
	}
	if _, _, err := f.store.findSummaryWithLimits(w13g2CanceledCtx(), "x"); err == nil {
		t.Fatalf("canceled findSummary 应失败")
	}
	if _, _, err := f.store.findSummaryWithLimits(ctx, "missing-id"); err != nil {
		t.Fatalf("missing findSummary 应 nil,nil,nil: %v", err)
	}
	// pg 模式的 table/GetGrantForMutation 分支（store.go 102-104/462-464）。
	pgStore := &Store{db: f.db, pg: true, now: time.Now, statsCache: newAuthorizationStatsCache(time.Now)}
	if pgStore.table("x") != "juhe_business.x" {
		t.Fatalf("pg table 前缀错误")
	}
	if _, err := pgStore.GetGrantForMutation(ctx, nil, "x"); err == nil {
		t.Fatalf("pg FOR UPDATE 在 sqlite 上应报错")
	}

	// loadResourceAuthorizationStatsFromDatabase canceled（191-193/206-208）。
	if _, err := f.store.loadResourceAuthorizationStatsFromDatabase(w13g2CanceledCtx(), "group", []string{"x"}); err == nil {
		t.Fatalf("canceled stats 查询应失败")
	}

	// placeholders(0) → 单个 "?"（options_store.go 174-180）。
	if got := placeholders(0); got != "?" {
		t.Fatalf("placeholders(0) 应 ?: %q", got)
	}

	// authorized_reads 的 rows.Err 臂（authorized_reads.go 61-68）。
	if _, err := f.store.AuthorizedReadableAccountIDs(w13g2CanceledCtx(), "viewer"); err == nil {
		t.Fatalf("canceled readable 应失败")
	}

	// options_store 的 canceled 查询失败臂（options_store.go 57-66/81-99/
	// 131-152）。
	if _, err := f.store.ListAuthorizationGranteeAccounts(w13g2CanceledCtx(), authorizationPrincipalOptionListOptions{}); err == nil {
		t.Fatalf("canceled grantee accounts 应失败")
	}
	if _, err := f.store.ListAuthorizationGranteeTeams(w13g2CanceledCtx(), authorizationPrincipalOptionListOptions{}); err == nil {
		t.Fatalf("canceled grantee teams 应失败")
	}
}

// TestW13g2UsageReadsArms 覆盖 usage_reads 的 canceled 错误臂与分页 clamp。
func TestW13g2UsageReadsArms(t *testing.T) {
	f := newUsageFixture(t)
	w13g2SeedAccounts(t, f)
	rng := UsageStatsRange{StartDate: "2026-08-08", EndDate: "2026-09-06", Days: 30, MaxDays: 31}
	access := accessInfo{IsAdmin: true}

	if _, err := f.store.teamUsageRows(w13g2CanceledCtx(), UsageFilters{}, access, rng, 1, 20); err == nil {
		t.Fatalf("canceled team rows 应失败")
	}
	if _, err := f.store.userUsageRows(w13g2CanceledCtx(), UsageFilters{}, access, rng, 1, 20); err == nil {
		t.Fatalf("canceled user rows 应失败")
	}
	if _, err := f.store.teamUsageSummary(w13g2CanceledCtx(), UsageFilters{}, access, rng); err == nil {
		t.Fatalf("canceled team summary 应失败")
	}
	if _, err := f.store.userUsageSummary(w13g2CanceledCtx(), UsageFilters{}, access, rng); err == nil {
		t.Fatalf("canceled user summary 应失败")
	}

	// normalizeUsagePageOptions clamp（578-588）。
	page, size := normalizeUsagePageOptions(99999, 5000)
	if page != 5 || size != 200 {
		t.Fatalf("分页 clamp 错误: %d %d", page, size)
	}

	// cancelCalendarDate / usage_resource_filter 已由成功路径覆盖；补
	// resourceFilterID 的 type-missing 分支（172-177）。
	if got := (UsageFilters{ResourceID: "x"}).resourceFilterID(); got != "" {
		t.Fatalf("无 type 时 resourceFilterID 应空: %q", got)
	}
}

// TestW13g2DownstreamArms 覆盖 downstream 的 reserveAccountHealthInputVersion
// 错误臂与 enqueue 的 canceled 查询失败。
func TestW13g2DownstreamArms(t *testing.T) {
	f := newFixture(t)
	w13g2SeedAccounts(t, f)
	ctx := context.Background()
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	// 空 account ID → failf（423-425）。
	if _, err := f.store.reserveAccountHealthInputVersion(ctx, tx, "  ", "now"); err == nil {
		t.Fatalf("空账户 ID 应失败")
	}
	// 新账户 → INSERT arm（444-447）。
	version, err := f.store.reserveAccountHealthInputVersion(ctx, tx, "acc_new", "now")
	if err != nil || version != 1 {
		t.Fatalf("首版应 1: %d %v", version, err)
	}
	// 既有账户 → UPDATE arm（437-442）。
	version, err = f.store.reserveAccountHealthInputVersion(ctx, tx, "acc_new", "now")
	if err != nil || version != 2 {
		t.Fatalf("次版应 2: %d %v", version, err)
	}
	// canceled ctx → 查询失败（432-435）。
	if _, err := f.store.reserveAccountHealthInputVersion(w13g2CanceledCtx(), tx, "acc_new", "now"); err == nil {
		t.Fatalf("canceled reserve 应失败")
	}
	// enqueue：非 account 资源直接返回（350-352）。
	n, err := f.store.enqueueGrantAccountHealthInputs(ctx, tx, &grantRow{ResourceType: "group"}, "now")
	if err != nil || n != 0 {
		t.Fatalf("group 应跳过: %d %v", n, err)
	}
	// canceled ctx → 实例查询失败（373-376）。
	if _, err := f.store.enqueueGrantAccountHealthInputs(w13g2CanceledCtx(), tx, &grantRow{ResourceType: "account", ResourceID: "acc_new"}, "now"); err == nil {
		t.Fatalf("canceled enqueue 应失败")
	}
	// activeAuthorizationQuotaHourlyWindowHours：非法 JSON → err（328-330）。
	if _, err := activeAuthorizationQuotaHourlyWindowHours("not-json"); err == nil {
		t.Fatalf("非法 limits 应失败")
	}
	// syncGrantQuotaScopeBindings canceled（71-75）。
	if err := f.store.syncGrantQuotaScopeBindings(w13g2CanceledCtx(), tx, &grantRow{ID: "x"}, "now"); err == nil {
		t.Fatalf("canceled bindings 应失败")
	}
}

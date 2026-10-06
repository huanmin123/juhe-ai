// 团队 AI 账户授权的成员实例克隆覆盖（BUG-0175 D-64/D-74 收尾 + 设计 :257）：
// 团队账户授权展开到成员时逐成员克隆授权实例并绑定该成员自己的同供应商默认
// 分组；成员缺默认分组属于数据异常，整体失败且指明成员；级联（新增成员、团队
// 重新启用）补齐实例且幂等；分组类型团队授权不产生实例。
package authz

import (
	"context"
	"testing"
	"time"
)

// teamInstanceFixture 建 owner + 两个成员的团队与 gpt 供应商源账户。
func teamInstanceFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "member1", "active")
	f.seedAccount(t, "member2", "active")
	f.seedProvisionSource(t, "acc-src", "owner", "生产账户", "gpt")
	return f
}

// TestTeamAccountGrantProvisionsEveryActiveMemberInstance 团队账户授权为每个
// 活跃成员创建实例并绑定成员自己的默认分组；归属人成员不获得实例。
func TestTeamAccountGrantProvisionsEveryActiveMemberInstance(t *testing.T) {
	f := teamInstanceFixture(t)
	f.seedTeamWithMember(t, "team_1", "owner")
	f.seedTeamWithMember(t, "team_1", "member1")
	f.seedTeamWithMember(t, "team_1", "member2")
	f.seedGranteeGroup(t, "grp-m1", "member1", "gpt", 1, 1)
	f.seedGranteeGroup(t, "grp-m2", "member2", "gpt", 1, 1)

	result, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "account", ResourceID: "acc-src",
		GranteeType: "team", GranteeID: "team_1",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created {
		t.Fatalf("team grant must create: %+v", result)
	}

	wantGroups := map[string]string{"member1": "grp-m1", "member2": "grp-m2"}
	for _, member := range []string{"member1", "member2"} {
		row := f.provisionInstanceByAuthorization(t, "acc-src", member)
		if row.systemAccountID != member || row.ownerSystemAccountID != "owner" || row.sourceAccountID != "acc-src" {
			t.Fatalf("member %s instance correlation: %+v", member, row)
		}
		if row.status != "active" || row.schedulable != 1 {
			t.Fatalf("member %s instance runtime state: %+v", member, row)
		}
		groupID, enabled := f.provisionBinding(t, row.id, "acc-src", member)
		if groupID != wantGroups[member] || enabled != 1 {
			t.Fatalf("member %s binding = %q enabled=%d, want %q", member, groupID, enabled, wantGroups[member])
		}
	}

	// 归属人成员不获得实例（fanout 排除，保持一致）。
	var ownerInstances int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM accounts
		WHERE authorization_instance_source_account_id = 'acc-src' AND system_account_id = 'owner'`).Scan(&ownerInstances); err != nil {
		t.Fatal(err)
	}
	if ownerInstances != 0 {
		t.Fatalf("owner member must not receive an instance: %d", ownerInstances)
	}
	var totalInstances int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM accounts
		WHERE authorization_instance_source_account_id = 'acc-src'`).Scan(&totalInstances); err != nil {
		t.Fatal(err)
	}
	if totalInstances != 2 {
		t.Fatalf("instance count = %d, want one per non-owner member", totalInstances)
	}
}

// TestTeamMemberCascadeProvisionsLaterMemberInstance 新加入成员经级联获得实例；
// 缺默认分组成员的级联失败并回滚整个团队操作事务。
func TestTeamMemberCascadeProvisionsLaterMemberInstance(t *testing.T) {
	f := teamInstanceFixture(t)
	f.seedTeamWithMember(t, "team_1", "member1")
	f.seedGranteeGroup(t, "grp-m1", "member1", "gpt", 1, 1)
	if _, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "account", ResourceID: "acc-src",
		GranteeType: "team", GranteeID: "team_1",
	}, "owner"); err != nil {
		t.Fatal(err)
	}
	f.provisionInstanceByAuthorization(t, "acc-src", "member1") // member1 实例已就位

	// 后加入成员 member2（fixture 已建账号，此处只补默认分组）：级联补运行时
	// 行 + 实例克隆。
	f.seedGranteeGroup(t, "grp-m2", "member2", "gpt", 1, 1)
	ctx := context.Background()
	now := f.now.UTC().Format(time.RFC3339Nano)
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.ApplyActiveTeamGrantsToMembersTx(ctx, tx, "team_1", []string{"member2"}, "owner", now); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	row := f.provisionInstanceByAuthorization(t, "acc-src", "member2")
	if row.systemAccountID != "member2" {
		t.Fatalf("later member instance: %+v", row)
	}
	if groupID, enabled := f.provisionBinding(t, row.id, "acc-src", "member2"); groupID != "grp-m2" || enabled != 1 {
		t.Fatalf("later member binding = %q %d", groupID, enabled)
	}

	// 缺默认分组的成员：级联失败并指明成员，事务回滚后无任何残留。
	f.seedAccount(t, "member3", "active")
	f.seedTeamWithMember(t, "team_1", "member3")
	tx, err = f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	cascadeErr := f.store.ApplyActiveTeamGrantsToMembersTx(ctx, tx, "team_1", []string{"member3"}, "owner", now)
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	fail, ok := cascadeErr.(*Fail)
	if !ok || fail.Message != "团队成员 member3 缺少该供应商的默认分组，无法完成团队授权" {
		t.Fatalf("cascade without default group = %v, want member-identifying Fail", cascadeErr)
	}
	var runtimeCount, instanceCount int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM resource_authorizations
		WHERE resource_id = 'acc-src' AND grantee_system_account_id = 'member3'`).Scan(&runtimeCount); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM accounts
		WHERE authorization_instance_source_account_id = 'acc-src' AND system_account_id = 'member3'`).Scan(&instanceCount); err != nil {
		t.Fatal(err)
	}
	if runtimeCount != 0 || instanceCount != 0 {
		t.Fatalf("failed cascade must roll back: runtimes=%d instances=%d", runtimeCount, instanceCount)
	}
}

// TestTeamReactivateRestoresInstanceCoverageIdempotently 团队停用（回收来源）
// 后重新启用：级联恢复成员运行时授权与实例覆盖；实例已存在时不重复创建。
func TestTeamReactivateRestoresInstanceCoverageIdempotently(t *testing.T) {
	f := teamInstanceFixture(t)
	f.seedTeamWithMember(t, "team_1", "member1")
	f.seedTeamWithMember(t, "team_1", "member2")
	f.seedGranteeGroup(t, "grp-m1", "member1", "gpt", 1, 1)
	f.seedGranteeGroup(t, "grp-m2", "member2", "gpt", 1, 1)
	if _, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "account", ResourceID: "acc-src",
		GranteeType: "team", GranteeID: "team_1",
	}, "owner"); err != nil {
		t.Fatal(err)
	}
	first1 := f.provisionInstanceByAuthorization(t, "acc-src", "member1")
	first2 := f.provisionInstanceByAuthorization(t, "acc-src", "member2")

	// 团队停用级联：成员运行时授权转回收，实例行保持原状（生产停用只回收来源）。
	if err := f.store.RevokeAllTeamSources(context.Background(), "team_1", "owner", "team_disabled"); err != nil {
		t.Fatal(err)
	}
	var runtimeStatus string
	if err := f.db.QueryRow(`SELECT status FROM resource_authorizations
		WHERE resource_id = 'acc-src' AND grantee_system_account_id = 'member1'`).Scan(&runtimeStatus); err != nil {
		t.Fatal(err)
	}
	if runtimeStatus != StatusRevoked {
		t.Fatalf("disabled team runtime = %s", runtimeStatus)
	}

	// 团队重新启用：ReactivateTeamGrantsTx 恢复成员授权并补齐实例；实例已存在
	// → 幂等复用，不新建行。
	ctx := context.Background()
	now := f.now.UTC().Format(time.RFC3339Nano)
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.ReactivateTeamGrantsTx(ctx, tx, "team_1", "owner", now); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, member := range []string{"member1", "member2"} {
		var runtimeStatus string
		if err := f.db.QueryRow(`SELECT status FROM resource_authorizations
			WHERE resource_id = 'acc-src' AND grantee_system_account_id = ?`, member).Scan(&runtimeStatus); err != nil {
			t.Fatal(err)
		}
		if runtimeStatus != StatusActive {
			t.Fatalf("member %s runtime after reactivate = %s", member, runtimeStatus)
		}
	}
	restored1 := f.provisionInstanceByAuthorization(t, "acc-src", "member1")
	restored2 := f.provisionInstanceByAuthorization(t, "acc-src", "member2")
	if restored1.id != first1.id || restored2.id != first2.id {
		t.Fatalf("reactivate must reuse live instances: %q→%q %q→%q", first1.id, restored1.id, first2.id, restored2.id)
	}
	var totalInstances int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM accounts
		WHERE authorization_instance_source_account_id = 'acc-src'`).Scan(&totalInstances); err != nil {
		t.Fatal(err)
	}
	if totalInstances != 2 {
		t.Fatalf("reactivate duplicated instances: %d", totalInstances)
	}
	for _, member := range []string{"member1", "member2"} {
		row := f.provisionInstanceByAuthorization(t, "acc-src", member)
		wantGroup := "grp-m1"
		if member == "member2" {
			wantGroup = "grp-m2"
		}
		if groupID, enabled := f.provisionBinding(t, row.id, "acc-src", member); groupID != wantGroup || enabled != 1 {
			t.Fatalf("member %s binding after reactivate = %q %d", member, groupID, enabled)
		}
	}
}

// TestGroupTeamGrantDoesNotProvisionInstances 分组类型团队授权不受实例克隆影响。
func TestGroupTeamGrantDoesNotProvisionInstances(t *testing.T) {
	f := teamInstanceFixture(t)
	f.seedTeamWithMember(t, "team_1", "member1")
	f.seedTeamWithMember(t, "team_1", "member2")
	f.seedGranteeGroup(t, "grp-m1", "member1", "gpt", 1, 1)
	f.seedGranteeGroup(t, "grp-m2", "member2", "gpt", 1, 1)
	f.seedGroup(t, "grp-owned", "owner")

	if _, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp-owned",
		GranteeType: "team", GranteeID: "team_1",
	}, "owner"); err != nil {
		t.Fatal(err)
	}
	var instanceCount int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM accounts WHERE authorization_instance_authorization_id IS NOT NULL`).Scan(&instanceCount); err != nil {
		t.Fatal(err)
	}
	if instanceCount != 0 {
		t.Fatalf("group team grant provisioned %d instances", instanceCount)
	}
	// 成员运行时授权仍然按团队来源展开。
	for _, member := range []string{"member1", "member2"} {
		var status, effective string
		if err := f.db.QueryRow(`SELECT status, COALESCE(effective_source_type,'') FROM resource_authorizations
			WHERE resource_id = 'grp-owned' AND grantee_system_account_id = ?`, member).Scan(&status, &effective); err != nil {
			t.Fatal(err)
		}
		if status != StatusActive || effective != "team" {
			t.Fatalf("member %s group runtime = %s/%s", member, status, effective)
		}
	}
}

// TestTeamGrantPatchReviveProvisionsMemberJoinedDuringExpiry PATCH 复活收尾：
// 过期期间加入的成员在 expired→active 复活后经 syncTeamGrantRuntime 补齐
// 实例克隆与默认分组绑定；已有实例的老成员幂等复用，不重复创建。
func TestTeamGrantPatchReviveProvisionsMemberJoinedDuringExpiry(t *testing.T) {
	f := teamInstanceFixture(t)
	f.seedTeamWithMember(t, "team_1", "member1")
	f.seedGranteeGroup(t, "grp-m1", "member1", "gpt", 1, 1)
	created, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "account", ResourceID: "acc-src",
		GranteeType: "team", GranteeID: "team_1",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	first1 := f.provisionInstanceByAuthorization(t, "acc-src", "member1")

	// 授权置为 expired（置态用 SQL 即可；复活链路走真实 PATCH）。过期期间
	// member2 加入团队。
	if _, err := f.db.Exec(`UPDATE resource_authorization_grants SET status = 'expired' WHERE id = ?`,
		created.Item.ID); err != nil {
		t.Fatal(err)
	}
	f.seedTeamWithMember(t, "team_1", "member2")
	f.seedGranteeGroup(t, "grp-m2", "member2", "gpt", 1, 1)

	// PATCH 复活：active + 新的未来过期时间（到期授权恢复必须同时调整
	// expiresAt 的契约）。
	active := StatusActive
	future := "2027-01-01T00:00:00.000Z"
	outcome, err := f.store.Patch(context.Background(), created.Item.ID, PatchInput{
		Status:       &active,
		ExpiresAtSet: true,
		ExpiresAt:    &future,
	}, created.Item.UpdatedAt, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Status != "updated" {
		t.Fatalf("patch revive outcome = %s", outcome.Status)
	}

	// 过期期间加入的 member2：runtime 激活 + 实例克隆 + 自己的默认分组绑定。
	var runtimeStatus, effective string
	if err := f.db.QueryRow(`SELECT status, COALESCE(effective_source_type,'') FROM resource_authorizations
		WHERE resource_id = 'acc-src' AND grantee_system_account_id = 'member2'`).Scan(&runtimeStatus, &effective); err != nil {
		t.Fatal(err)
	}
	if runtimeStatus != StatusActive || effective != "team" {
		t.Fatalf("revived member2 runtime = %s/%s", runtimeStatus, effective)
	}
	row2 := f.provisionInstanceByAuthorization(t, "acc-src", "member2")
	if row2.systemAccountID != "member2" || row2.status != "active" || row2.schedulable != 1 {
		t.Fatalf("revived member2 instance: %+v", row2)
	}
	if groupID, enabled := f.provisionBinding(t, row2.id, "acc-src", "member2"); groupID != "grp-m2" || enabled != 1 {
		t.Fatalf("revived member2 binding = %q %d", groupID, enabled)
	}
	// 已有实例的 member1 幂等复用，实例总数不变。
	restored1 := f.provisionInstanceByAuthorization(t, "acc-src", "member1")
	if restored1.id != first1.id {
		t.Fatalf("member1 instance must be reused: %q → %q", first1.id, restored1.id)
	}
	var totalInstances int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM accounts
		WHERE authorization_instance_source_account_id = 'acc-src'`).Scan(&totalInstances); err != nil {
		t.Fatal(err)
	}
	if totalInstances != 2 {
		t.Fatalf("instance count after revive = %d, want 2", totalInstances)
	}
}

// TestTeamMemberJoinCascadeSyncsQuotaHourlyBindings 成员加入级联收尾：带小时
// 额度的团队账户授权在 ApplyActiveTeamGrantsToMembersTx 后，新成员的
// account_authorization_team 绑定行（scope = 实例账户 ID:团队 ID，source =
// 授权业务主记录）立即可见。
func TestTeamMemberJoinCascadeSyncsQuotaHourlyBindings(t *testing.T) {
	f := teamInstanceFixture(t)
	f.seedTeamWithMember(t, "team_1", "member1")
	f.seedGranteeGroup(t, "grp-m1", "member1", "gpt", 1, 1)
	hourly := `{"hourly":{"enabled":true,"hours":5,"limit":100}}`
	created, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "account", ResourceID: "acc-src",
		GranteeType: "team", GranteeID: "team_1",
		LimitsJSON: &hourly,
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	row1 := f.provisionInstanceByAuthorization(t, "acc-src", "member1")
	countTeamBinding := func(instanceID string) int {
		t.Helper()
		var count int
		if err := f.db.QueryRow(`SELECT COUNT(*) FROM request_quota_hourly_window_scope_bindings
			WHERE scope_type = 'account_authorization_team' AND scope_id = ? AND source_id = ?`,
			instanceID+":team_1", created.Item.ID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	if countTeamBinding(row1.id) != 1 {
		t.Fatalf("创建后 member1 团队额度绑定应存在")
	}

	// 新成员加入：级联在同一事务内补 runtime + 实例 + quota 绑定。
	f.seedTeamWithMember(t, "team_1", "member2")
	f.seedGranteeGroup(t, "grp-m2", "member2", "gpt", 1, 1)
	ctx := context.Background()
	now := f.now.UTC().Format(time.RFC3339Nano)
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.ApplyActiveTeamGrantsToMembersTx(ctx, tx, "team_1", []string{"member2"}, "owner", now); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	row2 := f.provisionInstanceByAuthorization(t, "acc-src", "member2")
	if countTeamBinding(row2.id) != 1 {
		t.Fatalf("member2 加入后团队额度绑定应立即可见")
	}
}

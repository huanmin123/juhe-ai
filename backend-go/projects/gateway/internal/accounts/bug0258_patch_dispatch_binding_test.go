package accounts

// BUG-0258 回归：单账户 PATCH 调度字段（priority / superPriorityEnabled /
// fallbackEnabled）终值变化时，同一事务内同步 group_accounts 启用绑定行的
// local_* 快照（Node account-management-patch.repository.ts:835-845 的
// else-if 臂，Go 迁移丢失后调度排序永远按创建时旧绑定值）。换组路径走既有
// replaceGroupBinding 重建语义，绑定列跟随 next 终值（归档 :830-834）。

import (
	"context"
	"testing"
)

func bug0258Bool(value bool) *bool { return &value }

func bug0258Int(value int) *int { return &value }

// bug0258Seed 建 owner 账户 + 启用分组 grp-b258 + 一行 enabled=1 绑定，
// local_* 用偏离账户级的旧快照值模拟生产残留（创建时落库后从未同步）。
func bug0258Seed(t *testing.T, env *testEnv, accountID string, accountSuper int, localPriority, localSuper, localFallback int) {
	t.Helper()
	env.seedAccount(t, accountID, "owner-1", "调度绑定-"+accountID, "active")
	env.seedProviderAndDefaultGroup(t, "owner-1")
	if accountSuper != 0 {
		env.exec(t, `UPDATE accounts SET super_priority_enabled = ? WHERE id = ?`, accountSuper, accountID)
	}
	env.exec(t, `INSERT INTO groups (id, system_account_id, name, provider_code, enabled, group_type, created_at, updated_at)
		VALUES ('grp-b258', 'owner-1', 'BUG0258分组', 'gpt', 1, 'personal', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`)
	env.exec(t, `INSERT INTO group_accounts (system_account_id, group_id, account_id,
		local_priority, local_super_priority_enabled, local_fallback_enabled, enabled, created_at, updated_at)
		VALUES ('owner-1', 'grp-b258', ?, ?, ?, ?, 1, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`,
		accountID, localPriority, localSuper, localFallback)
}

// bug0258Binding 读回绑定行 local_* 三列与 updated_at（group 维度过滤后单行）。
func bug0258Binding(t *testing.T, env *testEnv, accountID, groupID string) (priority, super, fallback, updatedAt string) {
	t.Helper()
	where := ` FROM group_accounts WHERE account_id = ? AND group_id = ?`
	priority = env.queryCell(t, `SELECT local_priority`+where, accountID, groupID)
	super = env.queryCell(t, `SELECT local_super_priority_enabled`+where, accountID, groupID)
	fallback = env.queryCell(t, `SELECT local_fallback_enabled`+where, accountID, groupID)
	updatedAt = env.queryCell(t, `SELECT updated_at`+where, accountID, groupID)
	return
}

// TestBug0258PatchSuperPrioritySyncsBinding 红→绿主用例：开启超级优先后，
// 账户级与绑定级同步开启，绑定里残留的旧 local_priority 一并被账户级终值
// 覆盖（Node UPDATE 三值全量写），并落分组统计脏标记。
func TestBug0258PatchSuperPrioritySyncsBinding(t *testing.T) {
	env := newTestEnv(t)
	bug0258Seed(t, env, "acc-b258-1", 0, 3, 0, 0)
	ctx := context.Background()
	scope := AccessScope{ViewerID: "owner-1"}

	result, err := env.store.Patch(ctx, "acc-b258-1", PatchInput{
		ExpectedConfigRevision: 1,
		SuperPriorityEnabled:   bug0258Bool(true),
	}, scope)
	if err != nil {
		t.Fatal(err)
	}
	if got := env.queryCell(t, `SELECT super_priority_enabled FROM accounts WHERE id = 'acc-b258-1'`); got != "1" {
		t.Fatalf("账户级 super_priority_enabled = %s，应为 1", got)
	}
	priority, super, fallback, _ := bug0258Binding(t, env, "acc-b258-1", "grp-b258")
	if super != "1" {
		t.Fatalf("绑定 local_super_priority_enabled = %s，应为 1", super)
	}
	if priority != "0" || fallback != "0" {
		t.Fatalf("绑定 local_priority/local_fallback = %s/%s，应为 0/0（账户级终值全量覆盖旧快照）", priority, fallback)
	}
	if !result.DispatchBindingChanged {
		t.Fatal("DispatchBindingChanged 应为 true")
	}
	if result.GroupChanged {
		t.Fatal("GroupChanged 应为 false")
	}
	// groupStatsAffected 联动（Node :1036/:1731-1737）：提交后按账户 id 落
	// 分组统计脏标记，reason=account_management_patch。
	if got := env.queryCell(t, `SELECT reason FROM group_account_stats_dirty WHERE group_id = 'grp-b258'`); got != "account_management_patch" {
		t.Fatalf("分组统计脏标记 reason = %q，应为 account_management_patch", got)
	}
}

// TestBug0258PatchNotesOnlyKeepsBinding 未触及调度字段（只改 notes）时绑定行
// 原样保留（值与 updated_at 均不动），DispatchBindingChanged 为 false。
func TestBug0258PatchNotesOnlyKeepsBinding(t *testing.T) {
	env := newTestEnv(t)
	bug0258Seed(t, env, "acc-b258-2", 0, 3, 0, 1)
	ctx := context.Background()
	scope := AccessScope{ViewerID: "owner-1"}

	result, err := env.store.Patch(ctx, "acc-b258-2", PatchInput{
		ExpectedConfigRevision: 1,
		Notes:                  stringPtr("仅备注"),
	}, scope)
	if err != nil {
		t.Fatal(err)
	}
	priority, super, fallback, updatedAt := bug0258Binding(t, env, "acc-b258-2", "grp-b258")
	if priority != "3" || super != "0" || fallback != "1" || updatedAt != "2026-01-01T00:00:00.000Z" {
		t.Fatalf("仅改 notes 后绑定行被改动：priority=%s super=%s fallback=%s updated_at=%s", priority, super, fallback, updatedAt)
	}
	if result.DispatchBindingChanged {
		t.Fatal("未触及调度字段时 DispatchBindingChanged 应为 false")
	}
	if env.count(t, `SELECT COUNT(*) FROM group_account_stats_dirty`) != 0 {
		t.Fatal("未触及调度字段不应落分组统计脏标记")
	}
}

// TestBug0258PatchGroupChangeWithSuperRebuildsBinding 换组 + 同时开超级优先：
// 走 replaceGroupBinding 删除重建语义，旧分组绑定消失，新分组绑定三列跟随
// next 终值（归档 :830-834 传 next 值），GroupChanged/DispatchBindingChanged
// 同时为 true。
func TestBug0258PatchGroupChangeWithSuperRebuildsBinding(t *testing.T) {
	env := newTestEnv(t)
	bug0258Seed(t, env, "acc-b258-3", 0, 3, 0, 0)
	ctx := context.Background()
	scope := AccessScope{ViewerID: "owner-1"}

	result, err := env.store.Patch(ctx, "acc-b258-3", PatchInput{
		ExpectedConfigRevision: 1,
		GroupID:                stringPtr("grp-default-owner-1"),
		GroupIDPresent:         true,
		SuperPriorityEnabled:   bug0258Bool(true),
	}, scope)
	if err != nil {
		t.Fatal(err)
	}
	if env.count(t, `SELECT COUNT(*) FROM group_accounts WHERE account_id = 'acc-b258-3' AND group_id = 'grp-b258'`) != 0 {
		t.Fatal("旧分组绑定应被删除")
	}
	priority, super, fallback, _ := bug0258Binding(t, env, "acc-b258-3", "grp-default-owner-1")
	if super != "1" || priority != "0" || fallback != "0" {
		t.Fatalf("新分组绑定应跟随 next 终值：priority=%s super=%s fallback=%s，应为 0/1/0", priority, super, fallback)
	}
	if !result.GroupChanged || !result.DispatchBindingChanged {
		t.Fatalf("GroupChanged=%v DispatchBindingChanged=%v，应同时为 true", result.GroupChanged, result.DispatchBindingChanged)
	}
}

// TestBug0258PatchSuperOffSyncsBindingBack 关闭超级优先：账户级与绑定级同步
// 回 0；随后同值再提交（已关闭再关）不触发绑定同步（false 臂）。
func TestBug0258PatchSuperOffSyncsBindingBack(t *testing.T) {
	env := newTestEnv(t)
	bug0258Seed(t, env, "acc-b258-4", 1, 5, 1, 0)
	ctx := context.Background()
	scope := AccessScope{ViewerID: "owner-1"}

	result, err := env.store.Patch(ctx, "acc-b258-4", PatchInput{
		ExpectedConfigRevision: 1,
		SuperPriorityEnabled:   bug0258Bool(false),
	}, scope)
	if err != nil {
		t.Fatal(err)
	}
	if got := env.queryCell(t, `SELECT super_priority_enabled FROM accounts WHERE id = 'acc-b258-4'`); got != "0" {
		t.Fatalf("账户级 super_priority_enabled = %s，应为 0", got)
	}
	priority, super, fallback, updatedAt := bug0258Binding(t, env, "acc-b258-4", "grp-b258")
	if super != "0" {
		t.Fatalf("绑定 local_super_priority_enabled = %s，应为 0", super)
	}
	if priority != "0" || fallback != "0" {
		t.Fatalf("绑定 local_priority/local_fallback = %s/%s，应为 0/0（账户级终值全量覆盖旧快照）", priority, fallback)
	}
	if !result.DispatchBindingChanged {
		t.Fatal("关闭超级优先时 DispatchBindingChanged 应为 true")
	}

	// 同值再提交（已关闭再显式关）：无字段变化，绑定行不动。
	result, err = env.store.Patch(ctx, "acc-b258-4", PatchInput{
		ExpectedConfigRevision: 2,
		SuperPriorityEnabled:   bug0258Bool(false),
	}, scope)
	if err != nil {
		t.Fatal(err)
	}
	if result.DispatchBindingChanged {
		t.Fatal("同值提交 DispatchBindingChanged 应为 false")
	}
	_, super, _, updatedAtAfter := bug0258Binding(t, env, "acc-b258-4", "grp-b258")
	if super != "0" || updatedAtAfter != updatedAt {
		t.Fatalf("同值提交后绑定行被改动：super=%s updated_at=%s（前值 %s）", super, updatedAtAfter, updatedAt)
	}
}

// TestBug0258PatchPriorityAndFallbackSyncBinding priority 与 fallback 终值变化
// 同样走绑定同步（覆盖调度三字段的另外两列）。
func TestBug0258PatchPriorityAndFallbackSyncBinding(t *testing.T) {
	env := newTestEnv(t)
	bug0258Seed(t, env, "acc-b258-5", 0, 3, 0, 0)
	ctx := context.Background()
	scope := AccessScope{ViewerID: "owner-1"}

	result, err := env.store.Patch(ctx, "acc-b258-5", PatchInput{
		ExpectedConfigRevision: 1,
		Priority:               bug0258Int(7),
		FallbackEnabled:        bug0258Bool(true),
	}, scope)
	if err != nil {
		t.Fatal(err)
	}
	priority, super, fallback, _ := bug0258Binding(t, env, "acc-b258-5", "grp-b258")
	if priority != "7" || super != "0" || fallback != "1" {
		t.Fatalf("绑定行应同步为 priority=7 super=0 fallback=1，实际 %s/%s/%s", priority, super, fallback)
	}
	if !result.DispatchBindingChanged {
		t.Fatal("priority+fallback 变化时 DispatchBindingChanged 应为 true")
	}
}

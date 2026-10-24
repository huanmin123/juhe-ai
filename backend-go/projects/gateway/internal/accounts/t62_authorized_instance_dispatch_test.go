package accounts

// T62 深度排查（2026-10-24）回归：授权实例调度字段的三处形态缺口。
// ① 通用 PATCH 对实例行不再接受调度字段（核心功能设计「授权实例」节：
// 实例的调度编辑只写分组绑定 local_*，accounts.* 调度标记恒为初始 0）。
// ② 实例换绑分组时保留既有绑定行的 local_* 调度编辑（bind-group 载荷不
// 携带调度字段，旧逻辑用实例 accounts 行的 0 值重建会丢配置）。
// ③ 账户列表排序键与展示同源（绑定级 local_* 优先），实例行不再按恒 0 的
// accounts.* 排序。

import (
	"context"
	"errors"
	"testing"
)

// t62InstanceSeed 建授权实例账户（authorization_instance_authorization_id
// 指向 authz-1）+ owner 分组；withBinding 时再建 grp-a 启用绑定行（携带
// account_authorization_id 与 local_priority/local_super 编辑值）。
func t62InstanceSeed(t *testing.T, env *testEnv, withBinding bool) (accountID, groupA, groupB string) {
	t.Helper()
	env.seedAccount(t, "acc-t62-inst", "user-2", "T62授权实例", "active")
	env.exec(t, `UPDATE accounts SET authorization_instance_authorization_id = 'authz-1' WHERE id = 'acc-t62-inst'`)
	env.exec(t, `INSERT INTO groups (id, system_account_id, name, enabled, group_type, created_at, updated_at)
		VALUES ('grp-t62-a', 'user-2', 'T62组A', 1, 'personal', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`)
	env.exec(t, `INSERT INTO groups (id, system_account_id, name, enabled, group_type, created_at, updated_at)
		VALUES ('grp-t62-b', 'user-2', 'T62组B', 1, 'personal', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`)
	if withBinding {
		env.exec(t, `INSERT INTO group_accounts (system_account_id, group_id, account_id, account_authorization_id,
			local_priority, local_super_priority_enabled, local_fallback_enabled, enabled, created_at, updated_at)
			VALUES ('user-2', 'grp-t62-a', 'acc-t62-inst', 'authz-1', 5, 1, 0, 1,
			'2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`)
	}
	return "acc-t62-inst", "grp-t62-a", "grp-t62-b"
}

// TestT62GenericPatchRejectsInstanceDispatchFields ①：通用 PATCH 对授权实例
// 携带调度字段（并发上限/优先级/超级优先/降级备用）一律拒绝——实例调度编辑
// 只允许 authorized-dispatch 专用接口（写 local_*，不写 accounts.*）。
func TestT62GenericPatchRejectsInstanceDispatchFields(t *testing.T) {
	env := newTestEnv(t)
	accountID, _, _ := t62InstanceSeed(t, env, false)
	ctx := context.Background()
	scope := AccessScope{ViewerID: "user-2"}

	cases := map[string]PatchInput{
		"priority":         {Priority: bug0258Int(2)},
		"concurrencyLimit": {ConcurrencyLimit: bug0258Int(4)},
		"superPriority":    {SuperPriorityEnabled: bug0258Bool(true)},
		"fallback":         {FallbackEnabled: bug0258Bool(true)},
	}
	for name, input := range cases {
		input.ExpectedConfigRevision = 1
		_, err := env.store.Patch(ctx, accountID, input, scope)
		var valErr *ValidationError
		if !errors.As(err, &valErr) {
			t.Fatalf("%s: 通用 PATCH 应拒绝授权实例调度字段，got %v", name, err)
		}
		if valErr.Code != "authorized_instance_dispatch_field_rejected" {
			t.Fatalf("%s: code = %q", name, valErr.Code)
		}
	}
	// 拒绝路径不得改写实例 accounts.*（契约：实例调度标记恒为初始 0）。
	if got := env.queryCell(t, `SELECT super_priority_enabled FROM accounts WHERE id = 'acc-t62-inst'`); got != "0" {
		t.Fatalf("实例 accounts.super_priority_enabled = %s，应保持 0", got)
	}
}

// TestT62InstanceRebindPreservesLocalDispatchSnapshot ②：实例换绑分组时，
// authorized-dispatch 写入的 local_* 调度编辑随绑定保留（旧逻辑用实例
// accounts 行的 0 值重建绑定，换绑即丢配置）。
func TestT62InstanceRebindPreservesLocalDispatchSnapshot(t *testing.T) {
	env := newTestEnv(t)
	accountID, _, groupB := t62InstanceSeed(t, env, true)
	ctx := context.Background()
	scope := AccessScope{ViewerID: "user-2"}

	result, err := env.store.Patch(ctx, accountID, PatchInput{
		ExpectedConfigRevision: 1,
		GroupIDPresent:         true,
		GroupID:                &groupB,
	}, scope)
	if err != nil {
		t.Fatal(err)
	}
	if !result.GroupChanged {
		t.Fatal("GroupChanged 应为 true")
	}
	if got := env.queryCell(t, `SELECT local_priority FROM group_accounts WHERE account_id = 'acc-t62-inst' AND group_id = 'grp-t62-b'`); got != "5" {
		t.Fatalf("新绑定 local_priority = %s，应保留 5", got)
	}
	if got := env.queryCell(t, `SELECT local_super_priority_enabled FROM group_accounts WHERE account_id = 'acc-t62-inst' AND group_id = 'grp-t62-b'`); got != "1" {
		t.Fatalf("新绑定 local_super_priority_enabled = %s，应保留 1", got)
	}
	if got := env.queryCell(t, `SELECT count(*) FROM group_accounts WHERE account_id = 'acc-t62-inst' AND group_id = 'grp-t62-a'`); got != "0" {
		t.Fatalf("旧绑定未清理，count = %s", got)
	}
}

// TestT62ListSortMatchesDisplayPerAccessForm ③：列表按优先级排序取派发生效
// 值——有绑定行的账户按 local_priority（链面派发实际排序输入：自有账户由
// 写侧镜像同步、实例为 authorized-dispatch 编辑值），无绑定行按 accounts.*。
// 恒用 accounts.* 会让实例行按恒 0 排序、与派发/展示不一致。
func TestT62ListSortMatchesDisplayPerAccessForm(t *testing.T) {
	env := newTestEnv(t)
	// 自有账户：accounts.priority=1，绑定 local 镜像 1（写侧同步形态）。
	env.seedAccount(t, "acc-t62-own", "owner-1", "自有p1", "active")
	env.exec(t, `UPDATE accounts SET priority = 1 WHERE id = 'acc-t62-own'`)
	// 授权实例：accounts.priority=0（初始值），调度编辑在绑定 local_priority=3
	// （authorized-dispatch 形态，展示与派发均为 3）。
	env.seedAccount(t, "acc-t62-inst", "user-2", "实例p3", "active")
	env.exec(t, `UPDATE accounts SET authorization_instance_authorization_id = 'authz-1' WHERE id = 'acc-t62-inst'`)
	// 列表状态守卫要求授权行存在且 active，否则实例行被过滤。
	env.exec(t, `INSERT INTO resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id, status, created_by, created_at, updated_at)
		VALUES ('authz-1', 'account', 'acc-t62-src', 'owner-1', 'user-2', 'active', 'owner-1', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`)
	env.exec(t, `INSERT INTO groups (id, system_account_id, name, enabled, group_type, created_at, updated_at)
		VALUES ('grp-t62-s', 'user-2', 'T62排序组', 1, 'personal', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`)
	env.exec(t, `INSERT INTO group_accounts (system_account_id, group_id, account_id, account_authorization_id,
		local_priority, local_super_priority_enabled, local_fallback_enabled, enabled, created_at, updated_at)
		VALUES ('user-2', 'grp-t62-s', 'acc-t62-inst', 'authz-1', 3, 0, 0, 1, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`)
	// 自有绑定镜像（写侧同步形态）。
	env.exec(t, `INSERT INTO group_accounts (system_account_id, group_id, account_id,
		local_priority, local_super_priority_enabled, local_fallback_enabled, enabled, created_at, updated_at)
		VALUES ('owner-1', 'grp-t62-s', 'acc-t62-own', 1, 0, 0, 1, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`)

	scope := AccessScope{IsAdmin: true}
	page, err := env.store.ListPage(context.Background(), scope, ListOptions{
		Page:     1,
		PageSize: 10,
		Sorts:    []ListSort{{Field: "priority", Order: "asc"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	var priorities []int
	for _, item := range page.Items {
		got = append(got, item.ID)
		priorities = append(priorities, item.Priority)
	}
	// 派发生效值：自有=1（镜像）、实例=3（local 编辑值）；升序 → 自有在前。
	// 旧排序（恒 accounts.*）会把实例（accounts 0）排最前。
	wantIDs := []string{"acc-t62-own", "acc-t62-inst"}
	if len(got) != len(wantIDs) {
		t.Fatalf("items = %v（priorities %v），want %v", got, priorities, wantIDs)
	}
	for index := range wantIDs {
		if got[index] != wantIDs[index] {
			t.Fatalf("排序 = %v（display priorities %v），want %v——排序键应取派发生效值", got, priorities, wantIDs)
		}
	}
	// 展示值随观看者形态变化（授权人视角实例展示 local 3、管理员视角展示
	// accounts 0），不在此断言；排序固定取派发生效值（local/镜像）。
}

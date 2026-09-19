package routestrategies

// w9e 覆盖率战役第二波：store 级 Create/Patch/Delete 校验与冲突分支，
// 覆盖 HTTP 层打不到的内部臂。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func w9eStore(t *testing.T, env *testEnv) *Store {
	t.Helper()
	store, err := NewStore(env.db, false, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestW9EStoreCreateValidationMatrix(t *testing.T) {
	env := newTestEnv(t)
	admin := env.login(t, "w9e-sc", "root-pass", "super_admin")
	group := env.createGroup(t, admin, "w9e-sc-group", true)
	store := w9eStore(t, env)
	access := AccessScope{ViewerID: admin}
	ctx := context.Background()

	mk := func(bindings ...BindingInput) MutationInput {
		return MutationInput{Name: ptrString("矩阵"), HasBindings: true, Bindings: bindings}
	}
	// 缺绑定 / 空绑定。
	if _, err := store.Create(ctx, MutationInput{Name: ptrString("无绑定")}, access); err == nil {
		t.Fatal("缺绑定必须失败")
	}
	if _, err := store.Create(ctx, mk(), access); err == nil {
		t.Fatal("空绑定列表必须失败")
	}
	// 空名。
	if _, err := store.Create(ctx, MutationInput{HasBindings: true, Bindings: []BindingInput{{GroupID: group, Priority: intPtr(1), Status: "active"}}}, access); err == nil {
		t.Fatal("空名必须失败")
	}
	// priority=0 在 store 层归一化为默认序号（HTTP 层负责拒绝），此处不报错。
	// status 非法。
	if _, err := store.Create(ctx, mk(BindingInput{GroupID: group, Priority: intPtr(1), Status: "bogus"}), access); err == nil {
		t.Fatal("非法 status 必须失败")
	}
	// weight=0/101 的拒绝在 HTTP 层（bodies 解析），store 层按提供值直接落库。
	// 空 groupID。
	if _, err := store.Create(ctx, mk(BindingInput{GroupID: " ", Priority: intPtr(1), Status: "active"}), access); err == nil {
		t.Fatal("空 groupID 必须失败")
	}
	// 不存在的分组。
	if _, err := store.Create(ctx, mk(BindingInput{GroupID: "grp-ghost", Priority: intPtr(1), Status: "active"}), access); err == nil {
		t.Fatal("未知分组必须失败")
	}
	// 禁用分组绑定：disabled 组 + active 绑定 → 不允许。
	disabled := env.createGroup(t, admin, "w9e-sc-disabled", false)
	if _, err := store.Create(ctx, mk(BindingInput{GroupID: disabled, Priority: intPtr(1), Status: "active"}), access); err == nil {
		t.Fatal("禁用分组的 active 绑定必须失败")
	}
	// 超 max 绑定（> 上限）。
	many := make([]BindingInput, 0, maxRouteStrategyGroupBindings+1)
	for i := 0; i <= maxRouteStrategyGroupBindings; i++ {
		g := env.createGroup(t, admin, "w9e-many-"+strings.Repeat("g", i+1), true)
		many = append(many, BindingInput{GroupID: g, Priority: intPtr(i + 1), Status: "active"})
	}
	if _, err := store.Create(ctx, MutationInput{Name: ptrString("超量"), HasBindings: true, Bindings: many}, access); err == nil {
		t.Fatal("超量绑定必须失败")
	}
	// 重复优先级。
	g2 := env.createGroup(t, admin, "w9e-sc-group2", true)
	if _, err := store.Create(ctx, mk(BindingInput{GroupID: group, Priority: intPtr(1), Status: "active"}, BindingInput{GroupID: g2, Priority: intPtr(1), Status: "active"}), access); err == nil {
		t.Fatal("重复优先级必须失败")
	}
	// 全 disabled。
	if _, err := store.Create(ctx, mk(BindingInput{GroupID: group, Priority: intPtr(1), Status: "disabled"}), access); err == nil {
		t.Fatal("全 disabled 必须失败")
	}
}

func TestW9EStorePatchAndDelete(t *testing.T) {
	env := newTestEnv(t)
	admin := env.login(t, "w9e-pd", "root-pass", "super_admin")
	group := env.createGroup(t, admin, "w9e-pd-group", true)
	store := w9eStore(t, env)
	access := AccessScope{ViewerID: admin}
	ctx := context.Background()

	created, err := store.Create(ctx, MutationInput{Name: ptrString("补丁策略"), HasBindings: true,
		Bindings: []BindingInput{{GroupID: group, Priority: intPtr(1), Status: "active"}}}, access)
	if err != nil {
		t.Fatal(err)
	}
	updatedAt := env.strategyUpdatedAt(t, created.ID)

	// 空 patch 由 HTTP 层拒绝；store 层视为合法 no-op。
	// 版本冲突。
	if _, err := store.Patch(ctx, created.ID, MutationInput{HasDescription: true, Description: ptrString("x")}, "2000-01-01T00:00:00Z", access); err == nil {
		t.Fatal("版本冲突必须失败")
	}
	// 仅状态更新（disabled → active 保持可操作性）。
	if _, err := store.Patch(ctx, created.ID, MutationInput{
		Status: ptrString("disabled"),
	}, updatedAt, access); err != nil {
		t.Fatalf("status patch = %v", err)
	}
	updatedAt = env.strategyUpdatedAt(t, created.ID)
	// 不存在的策略 → 幂等 no-op（HTTP 层渲染 404）。
	if _, err := store.Patch(ctx, "route_strategy_ghost", MutationInput{HasDescription: true, Description: ptrString("x")}, updatedAt, access); err != nil {
		t.Fatalf("ghost patch 应幂等: %v", err)
	}
	// Delete：先造一个被 api_keys 引用的策略。
	env.exec(t, `INSERT INTO api_keys (id, system_account_id, route_strategy_id, name, status, created_at, updated_at)
		VALUES ('key-ref', ?, ?, '引用键', 'active', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, admin, created.ID)
	if _, err := store.Delete(ctx, created.ID, access); err == nil {
		t.Fatal("被引用策略必须禁止删除")
	}
	env.exec(t, `DELETE FROM api_keys WHERE id='key-ref'`)
	// 默认策略删除保护。
	env.exec(t, `UPDATE route_strategies SET is_default=1 WHERE id=?`, created.ID)
	if _, err := store.Delete(ctx, created.ID, access); err == nil {
		t.Fatal("默认策略必须禁止删除")
	}
	env.exec(t, `UPDATE route_strategies SET is_default=0 WHERE id=?`, created.ID)
	// 正常删除 + 幂等删除。
	result, err := store.Delete(ctx, created.ID, access)
	if err != nil || result == nil || !result.Deleted {
		t.Fatalf("delete = %+v err=%v", result, err)
	}
	if _, err := store.Delete(ctx, created.ID, access); err != nil {
		t.Fatalf("重复 delete 应幂等: %v", err)
	}
	_ = time.Now
	_ = errors.Is
}

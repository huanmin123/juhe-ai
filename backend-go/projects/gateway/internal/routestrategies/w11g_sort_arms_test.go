package routestrategies

// w11g 覆盖补充（第七批）：绑定展示排序分支（同优先级不同状态的
// groupID/ID 比较）、禁用分组绑定授权路径与 mutation 提交后钩子。

import (
	"context"
	"testing"
)

func TestW11GBindingSortComparatorArms(t *testing.T) {
	env := newTestEnv(t)
	admin := env.login(t, "w11g-so", "root-pass", "super_admin")
	// 命名保证 groupID 字典序可预测。
	groupA := env.createGroup(t, admin, "aaa", true)
	groupB := env.createGroup(t, admin, "bbb", true)
	store := w9eStore(t, env)
	ctx := context.Background()
	viewer := AccessScope{ViewerID: admin}
	// 同优先级 active+disabled（active 优先级唯一性只约束 active）。
	created, err := store.Create(ctx, MutationInput{
		Name: ptrString("w11g 排序"), Mode: ptrString(ModeHybridSmart),
		HybridConfigRaw: map[string]any{
			"scoringModel": "m", "scoringContextMode": "full_request",
			"levelRoutes": []any{
				map[string]any{"minLevel": 1, "maxLevel": 5, "targetModel": "t-low"},
				map[string]any{"minLevel": 6, "maxLevel": 10, "targetModel": "t-high"},
			},
		},
		HasBindings: true,
		Bindings: []BindingInput{
			{GroupID: groupB, Priority: intPtr(1), Status: "disabled"},
			{GroupID: groupA, Priority: intPtr(1), Status: "active"},
		},
	}, viewer)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := store.FindDetail(ctx, created.ID, viewer)
	if err != nil || detail == nil || len(detail.GroupBindings) != 2 {
		t.Fatalf("detail=%+v err=%v", detail, err)
	}
	if detail.GroupBindings[0].GroupID != groupA || detail.GroupBindings[0].Status != "active" {
		t.Fatalf("order=%+v", detail.GroupBindings)
	}
	// patch 禁用分组启用守卫（绑定已禁用分组再启用）。
	env.exec(t, `UPDATE groups SET enabled = 0 WHERE id = '`+groupA+`'`)
	if _, err := store.Patch(ctx, created.ID, MutationInput{
		HasBindings: true,
		Bindings: []BindingInput{
			{GroupID: groupB, Priority: intPtr(1), Status: "disabled"},
			{GroupID: groupA, Priority: intPtr(1), Status: "active"},
		},
	}, strategyUpdatedAt(env, t, created.ID), viewer); err == nil {
		t.Fatal("activating disabled group must fail")
	}
}

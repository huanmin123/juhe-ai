package routestrategies

// w11g 覆盖补充（第十批）：分组读取失败臂、failover 模式绑定校验分支与
// 同优先级禁用绑定的展示排序。

import (
	"context"
	"testing"
)

func TestW11GBindableGroupLoadErrorArm(t *testing.T) {
	env := newTestEnv(t)
	admin := env.login(t, "w11g-bg", "root-pass", "super_admin")
	env.createGroup(t, admin, "w11g-bg-group", true)
	store := w9eStore(t, env)
	viewer := AccessScope{ViewerID: admin}
	// 分组底表读取失败。
	if _, err := env.db.Exec(`DROP TABLE groups`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(context.Background(), MutationInput{
		Name: ptrString("w11g 组读失败"), HasBindings: true,
		Bindings: []BindingInput{{GroupID: "grp-w11g-x", Priority: intPtr(1), Status: "active"}},
	}, viewer); err == nil {
		t.Fatal("bindable groups load error must propagate")
	}
}

func TestW11GFailoverModeValidationArms(t *testing.T) {
	env := newTestEnv(t)
	admin := env.login(t, "w11g-fo", "root-pass", "super_admin")
	groupA := env.createGroup(t, admin, "w11g-fo-a", true)
	groupB := env.createGroup(t, admin, "w11g-fo-b", true)
	store := w9eStore(t, env)
	viewer := AccessScope{ViewerID: admin}
	ctx := context.Background()
	// failover：主用分组必须启用（首绑定 disabled）。
	if _, err := store.Create(ctx, MutationInput{
		Name: ptrString("w11g 回退"), Mode: ptrString(ModeFailover), HasBindings: true,
		Bindings: []BindingInput{
			{GroupID: groupA, Priority: intPtr(1), Status: "disabled"},
			{GroupID: groupB, Priority: intPtr(2), Status: "active"},
		},
	}, viewer); err == nil {
		t.Fatal("failover inactive primary must fail")
	}
	// failover：少于两个分组。
	if _, err := store.Create(ctx, MutationInput{
		Name: ptrString("w11g 回退单"), Mode: ptrString(ModeFailover), HasBindings: true,
		Bindings: []BindingInput{{GroupID: groupA, Priority: intPtr(1), Status: "active"}},
	}, viewer); err == nil {
		t.Fatal("failover single binding must fail")
	}
	// failover：备用分组全部禁用。
	if _, err := store.Create(ctx, MutationInput{
		Name: ptrString("w11g 回退备禁"), Mode: ptrString(ModeFailover), HasBindings: true,
		Bindings: []BindingInput{
			{GroupID: groupA, Priority: intPtr(1), Status: "active"},
			{GroupID: groupB, Priority: intPtr(2), Status: "disabled"},
		},
	}, viewer); err == nil {
		t.Fatal("failover without active backup must fail")
	}
}

func TestW11GWeightedSamePriorityDisabledSort(t *testing.T) {
	env := newTestEnv(t)
	admin := env.login(t, "w11g-hs", "root-pass", "super_admin")
	groupA := env.createGroup(t, admin, "aaa", true)
	groupB := env.createGroup(t, admin, "bbb", true)
	groupC := env.createGroup(t, admin, "ccc", true)
	store := w9eStore(t, env)
	viewer := AccessScope{ViewerID: admin}
	created, err := store.Create(context.Background(), MutationInput{
		Name: ptrString("w11g 同序"), Mode: ptrString(ModeWeighted),
		HasBindings: true,
		Bindings: []BindingInput{
			{GroupID: groupA, Priority: intPtr(1), Status: "active"},
			{GroupID: groupC, Priority: intPtr(2), Status: "disabled"},
			{GroupID: groupB, Priority: intPtr(2), Status: "disabled"},
		},
	}, viewer)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := store.FindDetail(context.Background(), created.ID, viewer)
	if err != nil || detail == nil || len(detail.GroupBindings) != 3 {
		t.Fatalf("detail=%+v err=%v", detail, err)
	}
	// 同优先级同状态 disabled 两行：按行 ID 兜底排序；active 恒首位。
	if detail.GroupBindings[0].GroupID != groupA {
		t.Fatalf("active first: %+v", detail.GroupBindings)
	}
	second, third := detail.GroupBindings[1], detail.GroupBindings[2]
	if second.Priority != third.Priority || second.ID >= third.ID {
		t.Fatalf("tie order by id: %+v", detail.GroupBindings)
	}
}

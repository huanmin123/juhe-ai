package routestrategies

// w11g 覆盖补充（第六批）：replace/reconcile 绑定写链的 delete/insert/update
// 阶段错误臂（PG plpgsql EXCEPTION 触发器注入，sqlite RAISE 臂按 D9 同型
// 重写）与展示排序。

import (
	"context"
	"testing"
)

func TestW11GBindingWriteStageErrors(t *testing.T) {
	setup := func(t *testing.T) (*Store, *testEnv, string, string, AccessScope, string) {
		t.Helper()
		env := newTestEnv(t)
		admin := env.login(t, "w11g-bw", "root-pass", "super_admin")
		group := env.createGroup(t, admin, "w11g-bw-group", true)
		store := w9eStore(t, env)
		viewer := AccessScope{ViewerID: admin}
		created, err := store.Create(context.Background(), MutationInput{
			Name: ptrString("w11g 写阶段"), HasBindings: true,
			Bindings: []BindingInput{{GroupID: group, Priority: intPtr(1), Status: "active"}},
		}, viewer)
		if err != nil {
			t.Fatal(err)
		}
		return store, env, created.ID, group, viewer, admin
	}
	patch := func(store *Store, id, group, at string, viewer AccessScope) error {
		_, err := store.Patch(context.Background(), id, MutationInput{
			HasBindings: true,
			Bindings:    []BindingInput{{GroupID: group, Priority: intPtr(2), Status: "active"}},
		}, at, viewer)
		return err
	}
	ctx := context.Background()
	// Create 的 replace 阶段：INSERT 失败。触发器注入按 D9 重写为 PG 同型
	// 机制（plpgsql EXCEPTION），三段断言逐字节保持。
	store, env, id, group, viewer, admin := setup(t)
	env.exec(t, `CREATE FUNCTION w11g_abort() RETURNS trigger AS $w11g$ BEGIN RAISE EXCEPTION 'w11f abort'; END $w11g$ LANGUAGE plpgsql`)
	env.exec(t, `CREATE TRIGGER w11g_fail_insert BEFORE INSERT ON juhe_business.route_strategy_groups FOR EACH STATEMENT EXECUTE FUNCTION w11g_abort()`)
	if _, err := store.Create(ctx, MutationInput{
		Name: ptrString("w11g 触发器"), HasBindings: true,
		Bindings: []BindingInput{{GroupID: group, Priority: intPtr(1), Status: "active"}},
	}, viewer); err == nil {
		t.Fatal("replace insert error must propagate")
	}
	// Patch 的 reconcile insert 失败（新分组）。
	group2 := env.createGroup(t, admin, "w11g-bw-group2", true)
	if err := patch(store, id, group2, strategyUpdatedAt(env, t, id), viewer); err == nil {
		t.Fatal("reconcile insert error must propagate")
	}
	env.exec(t, `DROP TRIGGER w11g_fail_insert ON juhe_business.route_strategy_groups`)

	// reconcile update 失败（同分组改优先级）。
	env.exec(t, `CREATE TRIGGER w11g_fail_update BEFORE UPDATE ON juhe_business.route_strategy_groups FOR EACH STATEMENT EXECUTE FUNCTION w11g_abort()`)
	if err := patch(store, id, group, strategyUpdatedAt(env, t, id), viewer); err == nil {
		t.Fatal("reconcile update error must propagate")
	}
	env.exec(t, `DROP TRIGGER w11g_fail_update ON juhe_business.route_strategy_groups`)

	// reconcile remove 失败（换绑另一分组触发删除）。
	env.exec(t, `CREATE TRIGGER w11g_fail_delete BEFORE DELETE ON juhe_business.route_strategy_groups FOR EACH STATEMENT EXECUTE FUNCTION w11g_abort()`)
	if err := patch(store, id, group2, strategyUpdatedAt(env, t, id), viewer); err == nil {
		t.Fatal("reconcile delete error must propagate")
	}
	env.exec(t, `DROP TRIGGER w11g_fail_delete ON juhe_business.route_strategy_groups`)
	env.exec(t, `DROP FUNCTION w11g_abort()`)

	_ = id
	_ = group
	_ = viewer
	_ = store
	_ = admin
}

// strategyUpdatedAt 读取当前 updated_at 版本。
func strategyUpdatedAt(env *testEnv, t *testing.T, id string) string {
	t.Helper()
	var at string
	if err := env.db.QueryRow(fixtureBind(`SELECT updated_at FROM route_strategies WHERE id = ?`), id).Scan(&at); err != nil {
		t.Fatal(err)
	}
	return at
}

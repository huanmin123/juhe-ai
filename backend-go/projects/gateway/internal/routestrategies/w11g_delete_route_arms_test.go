package routestrategies

// w11g 覆盖补充（第八批）：Delete 各阶段错误臂（drop 表/触发器）、
// speed-first 运行时路由与 remove 的错误渲染。

import (
	"context"
	"net/http"
	"testing"
)

func TestW11GDeleteStageErrorArms(t *testing.T) {
	setup := func(t *testing.T) (*Store, *testEnv, string, AccessScope, string) {
		t.Helper()
		env := newTestEnv(t)
		admin := env.login(t, "w11g-ds", "root-pass", "super_admin")
		group := env.createGroup(t, admin, "w11g-ds-group", true)
		store := w9eStore(t, env)
		viewer := AccessScope{ViewerID: admin}
		created, err := store.Create(context.Background(), MutationInput{
			Name: ptrString("w11g 删除阶段"), HasBindings: true,
			Bindings: []BindingInput{{GroupID: group, Priority: intPtr(1), Status: "active"}},
		}, viewer)
		if err != nil {
			t.Fatal(err)
		}
		return store, env, created.ID, viewer, admin
	}
	ctx := context.Background()
	// api_keys 计数查询失败。
	store, env, id, viewer, _ := setup(t)
	if _, err := env.db.Exec(`DROP TABLE api_keys`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Delete(ctx, id, viewer); err == nil {
		t.Fatal("api key count error must propagate")
	}
}

func TestW11GSpeedFirstAndRemoveRouteArms(t *testing.T) {
	// speed-first 运行时路由（facade 挂载分支）。
	env := newTestEnvFull(t, &wlRuntimeFacade{available: true})
	admin := env.login(t, "w11g-sf", "root-pass", "super_admin")
	group := env.createGroup(t, admin, "w11g-sf-group", true)
	viewer := AccessScope{ViewerID: admin}
	store := w9eStore(t, env)
	created, err := store.Create(context.Background(), MutationInput{
		Name: ptrString("w11g 运行时"), Mode: ptrString(ModeNormal), HasBindings: true,
		Bindings: []BindingInput{{GroupID: group, Priority: intPtr(1), Status: "active"}},
	}, viewer)
	if err != nil {
		t.Fatal(err)
	}
	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/route-strategies/"+created.ID+"/speed-first-runtime", "")
	if code != http.StatusOK {
		t.Fatalf("speed-first runtime = %d %v", code, payload)
	}
	// 404 分支。
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/route-strategies/rs-w11g-ghost/speed-first-runtime", "")
	if code != http.StatusNotFound {
		t.Fatalf("speed-first 404 = %d", code)
	}
	// remove 错误渲染：drop 主表 → 400。
	if _, err := env.db.Exec(`DROP TABLE route_strategies`); err != nil {
		t.Fatal(err)
	}
	code, _ = env.do(t, http.MethodDelete, "/__aisys__/api/route-strategies/rs-w11g-x", "")
	if code != http.StatusBadRequest {
		t.Fatalf("remove error = %d", code)
	}
	// 读链 500：list/options/detail/edit。
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/route-strategies", "")
	if code != http.StatusInternalServerError {
		t.Fatalf("list error = %d", code)
	}
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/route-strategies/options", "")
	if code != http.StatusInternalServerError {
		t.Fatalf("options error = %d", code)
	}
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/route-strategies/rs-w11g-x", "")
	if code != http.StatusInternalServerError {
		t.Fatalf("detail error = %d", code)
	}
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/route-strategies/rs-w11g-x/edit-basic", "")
	if code != http.StatusInternalServerError {
		t.Fatalf("edit error = %d", code)
	}
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/route-strategies/rs-w11g-x/speed-first-runtime", "")
	if code != http.StatusInternalServerError {
		t.Fatalf("runtime error = %d", code)
	}
}

package routestrategies

// w11g 覆盖补充（第二批）：ListPage 读链错误臂与坏存量数据、scope 查询
// 门、options 查询解析与 mutation 路由分支。

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestW11GListPageErrorArms(t *testing.T) {
	env := newTestEnv(t)
	admin := env.login(t, "w11g-lp", "root-pass", "super_admin")
	group := env.createGroup(t, admin, "w11g-lp-group", true)
	store := w9eStore(t, env)
	ctx := context.Background()
	viewer := AccessScope{ViewerID: admin}
	created, err := store.Create(ctx, MutationInput{
		Name: ptrString("w11g 列表"), HasBindings: true,
		Bindings: []BindingInput{{GroupID: group, Priority: intPtr(1), Status: "active"}},
	}, viewer)
	if err != nil {
		t.Fatal(err)
	}
	options := ListOptions{Page: 1, PageSize: 10}
	// 主查询失败。
	if _, err := env.db.Exec(`DROP TABLE route_strategies`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListPage(ctx, viewer, options); err == nil {
		t.Fatal("main query error must propagate")
	}
	// 恢复表后注入坏存量：mode 非法 → newListItem 失败。
	if _, err := env.db.Exec(`CREATE TABLE route_strategies (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL, description TEXT, mode TEXT NOT NULL DEFAULT 'normal', status TEXT NOT NULL DEFAULT 'active', is_default INTEGER NOT NULL DEFAULT 0, config_json TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	env.exec(t, `INSERT INTO route_strategies (id, system_account_id, name, mode, status, is_default, config_json, created_at, updated_at)
		VALUES ('rs-w11g-bad', '`+admin+`', 'w11g 坏行', 'bogus', 'active', 0, '{"schedulingPreference":"latency_first"}', '2026-09-01T00:00:00.000Z', '2026-09-01T00:00:00.000Z')`)
	if _, err := store.ListPage(ctx, viewer, options); err == nil {
		t.Fatal("bad stored mode must fail list")
	}
	// 坏 config_json。
	env.exec(t, `UPDATE route_strategies SET mode='normal', config_json='not-json' WHERE id='rs-w11g-bad'`)
	if _, err := store.ListPage(ctx, viewer, options); err == nil {
		t.Fatal("bad stored config must fail list")
	}
	// 坏 status。
	env.exec(t, `UPDATE route_strategies SET config_json='{}', status='bogus' WHERE id='rs-w11g-bad'`)
	if _, err := store.ListPage(ctx, viewer, options); err == nil {
		t.Fatal("bad stored status must fail list")
	}
	// 恢复合法行；名称水合失败：drop system_accounts。
	env.exec(t, `UPDATE route_strategies SET status='active' WHERE id='rs-w11g-bad'`)
	if _, err := env.db.Exec(`DROP TABLE system_accounts`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListPage(ctx, viewer, options); err == nil {
		t.Fatal("system account names error must propagate")
	}
	_ = created
}

func TestW11GBindingSnapshotErrorArms(t *testing.T) {
	env := newTestEnv(t)
	admin := env.login(t, "w11g-bs", "root-pass", "super_admin")
	group := env.createGroup(t, admin, "w11g-bs-group", true)
	store := w9eStore(t, env)
	ctx := context.Background()
	viewer := AccessScope{ViewerID: admin}
	if _, err := store.Create(ctx, MutationInput{
		Name: ptrString("w11g 快照"), HasBindings: true,
		Bindings: []BindingInput{{GroupID: group, Priority: intPtr(1), Status: "active"}},
	}, viewer); err != nil {
		t.Fatal(err)
	}
	// 绑定快照读链失败：drop 绑定表。
	if _, err := env.db.Exec(`DROP TABLE route_strategy_groups`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListPage(ctx, viewer, ListOptions{Page: 1, PageSize: 10}); err == nil {
		t.Fatal("binding snapshot error must propagate")
	}
	// 恢复绑定表后 drop api_keys（计数读链）。
	if _, err := env.db.Exec(`CREATE TABLE route_strategy_groups (id TEXT PRIMARY KEY, route_strategy_id TEXT NOT NULL, system_account_id TEXT NOT NULL, group_id TEXT NOT NULL, priority INTEGER NOT NULL DEFAULT 1, weight INTEGER NOT NULL DEFAULT 1, status TEXT NOT NULL DEFAULT 'active', created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := env.db.Exec(`DROP TABLE api_keys`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListPage(ctx, viewer, ListOptions{Page: 1, PageSize: 10}); err == nil {
		t.Fatal("api key count error must propagate")
	}
}

func TestW11GScopeQueryAndRouteArms(t *testing.T) {
	env := newTestEnv(t)
	admin := env.login(t, "w11g-sq", "root-pass", "super_admin")
	// 空白 systemAccountId：详情/编辑/变更路由全部 400。
	for _, target := range []string{
		"/__aisys__/api/route-strategies/rs-w11g?systemAccountId=%20",
		"/__aisys__/api/route-strategies/rs-w11g/edit-basic?systemAccountId=",
	} {
		code, payload := env.do(t, http.MethodGet, target, "")
		if code != http.StatusBadRequest {
			t.Fatalf("blank scope %s = %d %v", target, code, payload)
		}
	}
	code, _ := env.do(t, http.MethodPatch, "/__aisys__/api/route-strategies/rs-w11g?systemAccountId= ", `{}`)
	if code != http.StatusBadRequest {
		t.Fatalf("patch blank scope = %d", code)
	}
	code, _ = env.do(t, http.MethodDelete, "/__aisys__/api/route-strategies/rs-w11g?systemAccountId= ", "")
	if code != http.StatusBadRequest {
		t.Fatalf("delete blank scope = %d", code)
	}
	// 存在性：404 分支。
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/route-strategies/rs-w11g-ghost", "")
	if code != http.StatusNotFound {
		t.Fatalf("detail 404 = %d", code)
	}
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/route-strategies/rs-w11g-ghost/edit-basic", "")
	if code != http.StatusNotFound {
		t.Fatalf("edit 404 = %d", code)
	}
	code, _ = env.do(t, http.MethodDelete, "/__aisys__/api/route-strategies/rs-w11g-ghost", "")
	if code != http.StatusNotFound {
		t.Fatalf("delete 404 = %d", code)
	}
	// patch：坏 JSON → 400；不存在 → 404。
	code, _ = env.do(t, http.MethodPatch, "/__aisys__/api/route-strategies/rs-w11g", `not-json`)
	if code != http.StatusBadRequest {
		t.Fatalf("patch bad json = %d", code)
	}
	code, _ = env.do(t, http.MethodPatch, "/__aisys__/api/route-strategies/rs-w11g-ghost", `{"name":"x","expectedUpdatedAt":"2026-09-01T00:00:00.000Z"}`)
	if code != http.StatusNotFound {
		t.Fatalf("patch 404 = %d", code)
	}
	// create：坏 JSON 与空绑定。
	code, _ = env.do(t, http.MethodPost, "/__aisys__/api/route-strategies", `not-json`)
	if code != http.StatusBadRequest {
		t.Fatalf("create bad json = %d", code)
	}
	code, _ = env.do(t, http.MethodPost, "/__aisys__/api/route-strategies", `{"name":"w11g 无绑定"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("create no bindings = %d", code)
	}
	_ = admin
}

func TestW11GOptionsQueryParsing(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "w11g-oq", "root-pass", "super_admin")
	// limit/activeOnly/ids 解析分支（含超量 id 截断）。
	ids := make([]string, 55)
	for i := range ids {
		ids[i] = "grp-w11g"
	}
	target := "/__aisys__/api/route-strategies/options?limit=7&activeOnly=false&ids=" + strings.Join(ids, ",")
	code, payload := env.do(t, http.MethodGet, target, "")
	if code != http.StatusOK {
		t.Fatalf("options = %d %v", code, payload)
	}
	// keyword 空白不参与过滤。
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/route-strategies/options?keyword=%20%20&activeOnly=0&limit=abc", "")
	if code != http.StatusOK {
		t.Fatalf("options blank keyword = %d %v", code, payload)
	}
	// my 面同路由。
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/my-route-strategies/options?activeOnly=yes", "")
	if code != http.StatusOK {
		t.Fatalf("my options = %d %v", code, payload)
	}
	// list：mode/status 过滤参数。
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/route-strategies?mode=normal&status=active&page=2&pageSize=5&keyword=w11g", "")
	if code != http.StatusOK {
		t.Fatalf("list filters = %d", code)
	}
	// my 面列表。
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/my-route-strategies", "")
	if code != http.StatusOK {
		t.Fatalf("my list = %d", code)
	}
}

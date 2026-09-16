package routestrategies

// w11g 覆盖补充（第四批）：FindDetail/FindEditBasic/ListOptionsPage 的
// 错误臂与行规范化、绑定快照计数循环、名称检索兜底。

import (
	"context"
	"testing"
)

func TestW11GFindDetailAndEditBasicArms(t *testing.T) {
	env := newTestEnv(t)
	admin := env.login(t, "w11g-fd", "root-pass", "super_admin")
	group := env.createGroup(t, admin, "w11g-fd-group", true)
	store := w9eStore(t, env)
	ctx := context.Background()
	viewer := AccessScope{ViewerID: admin}
	created, err := store.Create(ctx, MutationInput{
		Name: ptrString("w11g 详情"), HasBindings: true,
		Bindings: []BindingInput{{GroupID: group, Priority: intPtr(1), Status: "active"}},
	}, viewer)
	if err != nil {
		t.Fatal(err)
	}
	// 正常 detail（admin 视角带 owner 名）。
	adminScopeVar := AccessScope{ViewerID: admin, IsAdmin: true}
	detail, err := store.FindDetail(ctx, created.ID, adminScopeVar)
	if err != nil || detail == nil || detail.APIKeyCount != 0 || len(detail.GroupBindings) != 1 {
		t.Fatalf("detail=%+v err=%v", detail, err)
	}
	// 无绑定 → 空列表（另建无绑定策略不可行，走 edit-basic 的空绑定行）。
	edit, err := store.FindEditBasic(ctx, created.ID, adminScopeVar)
	if err != nil || edit == nil || len(edit.GroupBindings) != 1 {
		t.Fatalf("edit=%+v err=%v", edit, err)
	}
	// 坏存量 mode：detail 与 edit-basic 均失败。
	env.exec(t, `UPDATE route_strategies SET mode='bogus' WHERE id='`+created.ID+`'`)
	if _, err := store.FindDetail(ctx, created.ID, viewer); err == nil {
		t.Fatal("bad mode detail must fail")
	}
	if _, err := store.FindEditBasic(ctx, created.ID, viewer); err == nil {
		t.Fatal("bad mode edit must fail")
	}
	// 坏存量 status。
	env.exec(t, `UPDATE route_strategies SET mode='normal', status='bogus' WHERE id='`+created.ID+`'`)
	if _, err := store.FindDetail(ctx, created.ID, viewer); err == nil {
		t.Fatal("bad status detail must fail")
	}
	if _, err := store.FindEditBasic(ctx, created.ID, viewer); err == nil {
		t.Fatal("bad status edit must fail")
	}
	// 坏存量 config。
	env.exec(t, `UPDATE route_strategies SET status='active', config_json='not-json' WHERE id='`+created.ID+`'`)
	if _, err := store.FindDetail(ctx, created.ID, viewer); err == nil {
		t.Fatal("bad config detail must fail")
	}
	if _, err := store.FindEditBasic(ctx, created.ID, viewer); err == nil {
		t.Fatal("bad config edit must fail")
	}
	// 绑定读链失败。
	env.exec(t, `UPDATE route_strategies SET config_json='{}' WHERE id='`+created.ID+`'`)
	if _, err := env.db.Exec(`DROP TABLE route_strategy_groups`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FindDetail(ctx, created.ID, viewer); err == nil {
		t.Fatal("bindings load error must propagate")
	}
	if _, err := store.FindEditBasic(ctx, created.ID, viewer); err == nil {
		t.Fatal("bindings load error must propagate (edit)")
	}
	// 主查询失败。
	if _, err := env.db.Exec(`DROP TABLE route_strategies`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FindDetail(ctx, created.ID, viewer); err == nil {
		t.Fatal("detail query error must propagate")
	}
	if _, err := store.FindEditBasic(ctx, created.ID, viewer); err == nil {
		t.Fatal("edit query error must propagate")
	}
}

func TestW11GListOptionsArms(t *testing.T) {
	env := newTestEnv(t)
	admin := env.login(t, "w11g-lo", "root-pass", "super_admin")
	group := env.createGroup(t, admin, "w11g-lo-group", true)
	store := w9eStore(t, env)
	ctx := context.Background()
	viewer := AccessScope{ViewerID: admin}
	adminScopeVar := AccessScope{ViewerID: admin, IsAdmin: true}
	created, err := store.Create(ctx, MutationInput{
		Name: ptrString("w11g 选项"), HasBindings: true,
		Bindings: []BindingInput{{GroupID: group, Priority: intPtr(1), Status: "active"}},
	}, viewer)
	if err != nil {
		t.Fatal(err)
	}
	// limit 钳制两端 + ids/keyword 过滤。
	options, err := store.ListOptionsPage(ctx, adminScopeVar, OptionsQuery{Limit: 0, IDs: []string{created.ID}})
	if err != nil || len(options) != 1 {
		t.Fatalf("limit clamp low options=%+v err=%v", options, err)
	}
	options, err = store.ListOptionsPage(ctx, adminScopeVar, OptionsQuery{Limit: 500, Keyword: "w11g"})
	if err != nil || len(options) != 1 {
		t.Fatalf("limit clamp high options=%+v err=%v", options, err)
	}
	// 坏存量 mode/status：整个读失败。
	env.exec(t, `UPDATE route_strategies SET mode='bogus' WHERE id='`+created.ID+`'`)
	if _, err := store.ListOptionsPage(ctx, viewer, OptionsQuery{Limit: 10}); err == nil {
		t.Fatal("bad mode options must fail")
	}
	env.exec(t, `UPDATE route_strategies SET mode='normal', status='bogus' WHERE id='`+created.ID+`'`)
	if _, err := store.ListOptionsPage(ctx, viewer, OptionsQuery{Limit: 10}); err == nil {
		t.Fatal("bad status options must fail")
	}
	// 名称水合失败（admin 面）。
	env.exec(t, `UPDATE route_strategies SET status='active' WHERE id='`+created.ID+`'`)
	if _, err := env.db.Exec(`DROP TABLE system_accounts`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListOptionsPage(ctx, adminScopeVar, OptionsQuery{Limit: 10}); err == nil {
		t.Fatal("names error must propagate")
	}
	// 主查询失败。
	if _, err := env.db.Exec(`DROP TABLE route_strategies`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListOptionsPage(ctx, viewer, OptionsQuery{Limit: 10}); err == nil {
		t.Fatal("options query error must propagate")
	}
}

func TestW11GBindingSnapshotCountsAndPreviews(t *testing.T) {
	env := newTestEnv(t)
	admin := env.login(t, "w11g-bc", "root-pass", "super_admin")
	group1 := env.createGroup(t, admin, "w11g-bc-g1", true)
	group2 := env.createGroup(t, admin, "w11g-bc-g2", true)
	group3 := env.createGroup(t, admin, "w11g-bc-g3", true)
	group4 := env.createGroup(t, admin, "w11g-bc-g4", true)
	store := w9eStore(t, env)
	ctx := context.Background()
	viewer := AccessScope{ViewerID: admin}
	created, err := store.Create(ctx, MutationInput{
		Name: ptrString("w11g 计数"), Mode: ptrString(ModeHybridSmart), HasBindings: true,
		HybridConfigRaw: map[string]any{
			"scoringModel":      "score-model-w11g",
			"scoringContextMode": "full_request",
			"levelRoutes": []any{
				map[string]any{"minLevel": 1, "maxLevel": 5, "targetModel": "model-low"},
				map[string]any{"minLevel": 6, "maxLevel": 10, "targetModel": "model-high"},
			},
		},
		Bindings: []BindingInput{
			{GroupID: group1, Priority: intPtr(1), Status: "active"},
			{GroupID: group2, Priority: intPtr(2), Status: "active"},
			{GroupID: group3, Priority: intPtr(3), Status: "active"},
			{GroupID: group4, Priority: intPtr(4), Status: "active"},
		},
	}, viewer)
	if err != nil {
		t.Fatal(err)
	}
	// api key 计数循环：插入引用行。
	env.exec(t, `INSERT INTO api_keys (id, system_account_id, route_strategy_id, name, created_at, updated_at)
		VALUES ('w11g-key-1', '`+admin+`', '`+created.ID+`', 'w11g key', '2026-09-01T00:00:00.000Z', '2026-09-01T00:00:00.000Z')`)
	result, err := store.ListPage(ctx, viewer, ListOptions{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].BindingCount != 4 || result.Items[0].APIKeyCount != 1 {
		t.Fatalf("items=%+v", result.Items)
	}
	// 预览截断为 3。
	if len(result.Items[0].GroupBindingPreview) != 3 {
		t.Fatalf("preview=%+v", result.Items[0].GroupBindingPreview)
	}
	// previewsFromBounds 直接断言截断。
	if got := len(previewsFromBindings(make([]GroupBinding, 5), 2)); got != 2 {
		t.Fatalf("previews cut = %d", got)
	}
}

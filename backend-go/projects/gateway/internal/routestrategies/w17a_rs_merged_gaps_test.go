package routestrategies

// W17a 覆盖率补缺（routestrategies ≥95% 硬门）。重点补 hybrid_smart 模式
// 移除后遗留的无效存量数据臂、双方言（pg/SQLite）分支、scope 空子句臂与
// 纯函数守卫臂；全部测试串行（无 t.Parallel）。

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"testing"
)

func TestW17aRsPureHelperArms(t *testing.T) {
	// SpeedFirstRuntimeScope：跨包适配器的 scope 构造。
	scope := SpeedFirstRuntimeScope("rs-w17a", "grp-w17a")
	if scope.RouteStrategyID != "rs-w17a" || scope.GroupID != "grp-w17a" {
		t.Fatalf("scope = %#v", scope)
	}
	// normalConfigForMode：不支持调度偏好的模式返回 nil（hybrid_smart 遗留臂）。
	if normalConfigForMode("hybrid_smart", nil) != nil {
		t.Fatal("不支持调度偏好的模式应返回 nil")
	}
	normal := normalConfigForMode(ModeNormal, nil)
	if normal == nil || normal.SchedulingPreference != defaultNormalSchedulingPreference {
		t.Fatalf("normal = %#v", normal)
	}
	// changeText：nil 与序列化失败臂。
	if changeText(nil) != "" {
		t.Fatal("nil 应渲染空串")
	}
	if changeText(make(chan int)) != "" {
		t.Fatal("不可序列化值应渲染空串")
	}
	// uniqueGroupIDs：去重、去空白、跳过空值。
	ids := uniqueGroupIDs([]BindingInput{
		{GroupID: "g1"}, {GroupID: ""}, {GroupID: "g1"}, {GroupID: "g2"},
	})
	if len(ids) != 2 || ids[0] != "g1" || ids[1] != "g2" {
		t.Fatalf("ids = %#v", ids)
	}
	// scanBindingRow：扫描失败臂。
	if _, err := scanBindingRow(func(...any) error { return errors.New("扫描失败") }); err == nil {
		t.Fatal("扫描错误应透传")
	}
	// scanBindingRow：关联分组状态非 0/1 臂。
	_, err := scanBindingRow(func(targets ...any) error {
		*(targets[0].(*string)) = "rsg-w17a"
		*(targets[1].(*string)) = "rs-w17a"
		*(targets[2].(*string)) = "grp-w17a"
		*(targets[3].(*int)) = 1
		*(targets[4].(*sql.NullString)) = sql.NullString{String: "1", Valid: true}
		*(targets[5].(*string)) = "active"
		*(targets[6].(*sql.NullString)) = sql.NullString{String: "组", Valid: true}
		*(targets[7].(*sql.NullString)) = sql.NullString{String: "openai", Valid: true}
		*(targets[8].(*int)) = 2
		return nil
	})
	var invalidGroup *ValidationError
	if !errors.As(err, &invalidGroup) {
		t.Fatalf("关联分组状态无效应报验证错误, got %v", err)
	}
}

func TestW17aRsBindingStoreEmptyArms(t *testing.T) {
	env := newTestEnv(t)
	store := env.store
	ctx := context.Background()
	// 空策略集合直接返回。
	bindings, err := store.loadBindings(ctx, env.db, nil)
	if err != nil || len(bindings) != 0 {
		t.Fatalf("bindings = %#v, err = %v", bindings, err)
	}
	groups, err := store.loadBindableGroups(ctx, env.db, nil, "owner-w17a", false)
	if err != nil || len(groups) != 0 {
		t.Fatalf("groups = %#v, err = %v", groups, err)
	}
	// 空绑定输入 → 验证错误。
	if _, err := store.normalizeBindings(ctx, env.db, nil, "owner-w17a", false); err == nil {
		t.Fatal("空绑定应报验证错误")
	}
	// 模式与绑定数不匹配 → replace/reconcile 的验证臂。
	writes := []bindingWrite{
		{groupID: "g1", providerCode: "openai", priority: 1, weight: 1, status: "active", groupEnabled: true},
		{groupID: "g2", providerCode: "openai", priority: 2, weight: 1, status: "active", groupEnabled: true},
	}
	if _, err := store.replaceBindings(ctx, env.db, "rs-w17a", "owner-w17a", ModeNormal, writes, "2026-01-01T00:00:00.000Z"); err == nil {
		t.Fatal("普通路由双绑定应报验证错误")
	}
	if _, err := store.reconcileBindings(ctx, env.db, "rs-w17a", "owner-w17a", ModeNormal, nil, writes, "2026-01-01T00:00:00.000Z"); err == nil {
		t.Fatal("普通路由双绑定应报验证错误")
	}
}

func TestW17aRsPatchConflictAndBadStoredConfig(t *testing.T) {
	env := newTestEnv(t)
	admin := env.login(t, "w17a-rs", "root-pass", "super_admin")
	group := env.createGroup(t, admin, "w17a-group", true)
	store := env.store
	ctx := context.Background()
	viewer := AccessScope{ViewerID: admin}
	created, err := store.Create(ctx, MutationInput{
		Name: ptrString("w17a 策略甲"), HasBindings: true,
		Bindings: []BindingInput{{GroupID: group, Priority: intPtr(1), Status: "active"}},
	}, viewer)
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.Create(ctx, MutationInput{
		Name: ptrString("w17a 策略乙"), HasBindings: true,
		Bindings: []BindingInput{{GroupID: group, Priority: intPtr(1), Status: "active"}},
	}, viewer)
	if err != nil {
		t.Fatal(err)
	}
	// 不存在的 id + 版本号：isNoRows → conflict 臂（无行、无冲突版本 → nil, nil）。
	result, err := store.Patch(ctx, "rs-w17a-missing", MutationInput{Name: ptrString("x")}, "2026-01-01T00:00:00.000Z", AccessScope{IsAdmin: true})
	if result != nil || err != nil {
		t.Fatalf("missing patch = %#v, %v", result, err)
	}
	// 坏存量 config_json：parseStoredConfig 失败臂。
	env.exec(t, `INSERT INTO route_strategies (id, system_account_id, name, mode, status, is_default, config_json, created_at, updated_at)
		VALUES ('rs-w17a-bad', ?, 'w17a 坏配置', 'normal', 'active', 0, '{oops', '2026-01-01T00:00:00.000Z', '2026-01-02T00:00:00.000Z')`, admin)
	if _, err := store.Patch(ctx, "rs-w17a-bad", MutationInput{Mode: ptrString(ModeNormal)}, "2026-01-02T00:00:00.000Z", AccessScope{IsAdmin: true}); err == nil {
		t.Fatal("坏存量配置应失败")
	}
	// 重命名为同 owner 下已有名称：UPDATE 唯一约束 → 冲突臂。
	updatedAt := env.strategyUpdatedAt(t, created.ID)
	_, err = store.Patch(ctx, created.ID, MutationInput{Name: ptrString(other.Name)}, updatedAt, AccessScope{IsAdmin: true})
	if err == nil {
		t.Fatal("重名更新应冲突")
	}
	var conflictErr *ConflictError
	if !errorsAs(err, &conflictErr) {
		t.Fatalf("期望名称冲突错误, got %v", err)
	}
}

func TestW17aRsPgStoreDialectArms(t *testing.T) {
	env := newTestEnv(t)
	admin := env.login(t, "w17a-pg", "root-pass", "super_admin")
	group := env.createGroup(t, admin, "w17a-pg-group", true)
	store, err := NewStore(env.db, true, nil, nil, &recordingInvalidator{})
	if err != nil {
		t.Fatal(err)
	}
	// 先用 SQLite 方言准备一行存量数据。
	sqliteStore := env.store
	created, err := sqliteStore.Create(context.Background(), MutationInput{
		Name: ptrString("w17a pg"), HasBindings: true,
		Bindings: []BindingInput{{GroupID: group, Priority: intPtr(1), Status: "active"}},
	}, AccessScope{ViewerID: admin})
	if err != nil {
		t.Fatal(err)
	}
	// pg 方言：Patch 追加 FOR UPDATE，SQLite 语法错误 → 错误臂。
	if _, err := store.Patch(context.Background(), created.ID, MutationInput{Name: ptrString("x")}, env.strategyUpdatedAt(t, created.ID), AccessScope{IsAdmin: true}); err == nil {
		t.Fatal("pg 方言 Patch 应因 FOR UPDATE 失败")
	}
	// pg 方言：Delete 行级锁失败臂。
	if _, err := store.Delete(context.Background(), created.ID, AccessScope{IsAdmin: true}); err == nil {
		t.Fatal("pg 方言 Delete 应因 FOR UPDATE 失败")
	}
}

func TestW17aRsListScopeArms(t *testing.T) {
	env := newTestEnv(t)
	admin := env.login(t, "w17a-list", "root-pass", "super_admin")
	group := env.createGroup(t, admin, "w17a-list-group", true)
	store := env.store
	ctx := context.Background()
	created, err := store.Create(ctx, MutationInput{
		Name: ptrString("w17a 列表"), HasBindings: true,
		Bindings: []BindingInput{{GroupID: group, Priority: intPtr(1), Status: "active"}},
	}, AccessScope{ViewerID: admin})
	if err != nil {
		t.Fatal(err)
	}
	// ListPage：scope 子句非空臂。
	page, err := store.ListPage(ctx, AccessScope{ViewerID: admin}, ListOptions{Page: 1, PageSize: 10})
	if err != nil || page == nil || len(page.Items) != 1 {
		t.Fatalf("page = %#v, err = %v", page, err)
	}
	// 空 scope（无 viewer、非 admin）→ ownerClause 失败臂。
	if detail, err := store.FindDetail(ctx, created.ID, AccessScope{}); detail != nil || err != nil {
		t.Fatalf("detail = %#v, err = %v", detail, err)
	}
	options, err := store.ListOptionsPage(ctx, AccessScope{}, OptionsQuery{})
	if err != nil || len(options) != 0 {
		t.Fatalf("options = %#v, err = %v", options, err)
	}
	if basic, err := store.FindEditBasic(ctx, created.ID, AccessScope{}); basic != nil || err != nil {
		t.Fatalf("basic = %#v, err = %v", basic, err)
	}
	// admin scope：详情附加系统账号信息。
	basic, err := store.FindEditBasic(ctx, created.ID, AccessScope{IsAdmin: true})
	if err != nil || basic == nil || basic.SystemAccountID == nil || *basic.SystemAccountID != admin {
		t.Fatalf("basic = %#v, err = %v", basic, err)
	}
	// hybrid_smart 遗留存量：options 读取整体失败臂（limit 放大到全量，
	// 确保扫到遗留行）。
	env.exec(t, `INSERT INTO route_strategies (id, system_account_id, name, mode, status, is_default, created_at, updated_at)
		VALUES ('rs-w17a-legacy', ?, 'w17a 遗留', 'hybrid_smart', 'active', 0, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`, admin)
	if _, err := store.ListOptionsPage(ctx, AccessScope{IsAdmin: true}, OptionsQuery{Limit: 100}); err == nil {
		t.Fatal("遗留模式存量应导致 options 读取失败")
	}
}

func TestW17aRsRouteScopeGuardAndSpeedFirst(t *testing.T) {
	env := newTestEnvFull(t, &wlRuntimeFacade{available: true})
	admin := env.login(t, "w17a-route", "root-pass", "super_admin")
	group := env.createGroup(t, admin, "w17a-route-group", true)
	store := env.store
	created, err := store.Create(context.Background(), MutationInput{
		Name: ptrString("w17a 路由"), HasBindings: true,
		Bindings: []BindingInput{{GroupID: group, Priority: intPtr(1), Status: "active"}},
	}, AccessScope{ViewerID: admin})
	if err != nil {
		t.Fatal(err)
	}
	id := created.ID
	// 管理端速度优先运行时读（facade 已挂载的注册分支）。
	if code, _ := env.do(t, http.MethodGet, "/__aisys__/api/route-strategies/"+id+"/speed-first-runtime", ""); code != http.StatusOK {
		t.Fatalf("speed-first-runtime = %d", code)
	}
	// 空白 systemAccountId 查询：详情/编辑/修改/删除四个入口的 400 门。
	if code, _ := env.do(t, http.MethodGet, "/__aisys__/api/route-strategies/"+id+"/edit-basic?systemAccountId=", ""); code != http.StatusBadRequest {
		t.Fatalf("edit-basic blank scope = %d", code)
	}
	if code, _ := env.do(t, http.MethodPatch, "/__aisys__/api/route-strategies/"+id+"?systemAccountId=", `{"name":"改名"}`); code != http.StatusBadRequest {
		t.Fatalf("patch blank scope = %d", code)
	}
	if code, _ := env.do(t, http.MethodDelete, "/__aisys__/api/route-strategies/"+id+"?systemAccountId=", ""); code != http.StatusBadRequest {
		t.Fatalf("delete blank scope = %d", code)
	}
	// 创建入口：非法 JSON body。
	if code, _ := env.do(t, http.MethodPost, "/__aisys__/api/route-strategies", "{oops"); code != http.StatusBadRequest {
		t.Fatalf("invalid json create = %d", code)
	}
}

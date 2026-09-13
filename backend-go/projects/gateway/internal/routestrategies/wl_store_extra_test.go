package routestrategies

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

// ---- 指针与空值辅助 ----

func TestWlPointerHelpers(t *testing.T) {
	if ptrString("") != nil {
		t.Fatal("空串必须返回 nil")
	}
	if got := ptrString("v"); got == nil || *got != "v" {
		t.Fatalf("got=%v", got)
	}
	group := bindableGroup{name: "n"}
	if got := group.namePtr(); got == nil || *got != "n" {
		t.Fatalf("namePtr=%v", got)
	}
	if (bindableGroup{}).namePtr() != nil {
		t.Fatal("空名必须返回 nil")
	}
	if got := derefOrEmpty(nil); got != "" {
		t.Fatalf("derefOrEmpty(nil)=%q", got)
	}
	value := "x"
	if got := derefOrEmpty(&value); got != "x" {
		t.Fatalf("got=%q", got)
	}
	if got := nullPtrString(sql.NullString{String: "s", Valid: true}); got == nil || *got != "s" {
		t.Fatalf("got=%v", got)
	}
	if nullPtrString(sql.NullString{}) != nil || nullPtrString(sql.NullString{String: "", Valid: true}) != nil {
		t.Fatal("无效或空必须返回 nil")
	}
	if nullString(sql.NullString{}) != nil {
		t.Fatal("无效 NullString 渲染为 nil")
	}
	if nullString(sql.NullString{String: "v", Valid: true}) != "v" {
		t.Fatal("有效 NullString 渲染为字符串")
	}
	valid := sql.NullString{String: "same", Valid: true}
	if !sameNullableText(valid, &value) {
		_ = value
	}
	empty := ""
	if !sameNullableText(sql.NullString{String: "", Valid: true}, &empty) || !sameNullableText(sql.NullString{Valid: false}, nil) {
		t.Fatal("空值比较语义错误")
	}
	if sameNullableText(valid, nil) {
		t.Fatal("有值 vs nil 必须不等")
	}
	other := "other"
	if sameNullableText(valid, &other) {
		t.Fatal("不同值必须不等")
	}
	if !sameNullString(valid, sql.NullString{String: "same", Valid: true}) {
		t.Fatal("相同值必须相等")
	}
	if sameNullString(valid, sql.NullString{}) {
		t.Fatal("不同值必须不等")
	}
}

// ---- 绑定写入比较 ----

func TestWlBindingWritesEqual(t *testing.T) {
	base := []bindingWrite{{groupID: "g1", priority: 1, weight: 1, status: "active"}}
	if !bindingWritesEqual(base, base) {
		t.Fatal("相同切片必须相等")
	}
	if bindingWritesEqual(base, nil) {
		t.Fatal("长度不同必须不等")
	}
	if bindingWritesEqual(nil, nil) != true {
		t.Fatal("双空相等")
	}
	if bindingWritesEqual(base, []bindingWrite{{groupID: "g2", priority: 1, weight: 1, status: "active"}}) {
		t.Fatal("groupID 不同必须不等")
	}
	if bindingWritesEqual(base, []bindingWrite{{groupID: "g1", priority: 2, weight: 1, status: "active"}}) {
		t.Fatal("priority 不同必须不等")
	}
	if bindingWritesEqual(base, []bindingWrite{{groupID: "g1", priority: 1, weight: 2, status: "active"}}) {
		t.Fatal("weight 不同必须不等")
	}
	if bindingWritesEqual(base, []bindingWrite{{groupID: "g1", priority: 1, weight: 1, status: "disabled"}}) {
		t.Fatal("status 不同必须不等")
	}
}

// ---- 请求体形态校验 ----

func TestWlStrictObjectAndHybridShape(t *testing.T) {
	if _, ok := strictObject("x", map[string]bool{}); ok {
		t.Fatal("非对象必须失败")
	}
	if _, ok := strictObject(map[string]any{"a": 1}, map[string]bool{}); ok {
		t.Fatal("未知键必须失败")
	}
	record, ok := strictObject(map[string]any{"a": 1}, map[string]bool{"a": true})
	if !ok || record["a"] != 1 {
		t.Fatalf("record=%v ok=%v", record, ok)
	}
	if validHybridConfigShape("x") {
		t.Fatal("非对象必须无效")
	}
	if validHybridConfigShape(map[string]any{"levelRoutes": "x"}) {
		t.Fatal("levelRoutes 非数组必须无效")
	}
	if validHybridConfigShape(map[string]any{"levelRoutes": []any{"x"}}) {
		t.Fatal("等级项非对象必须无效")
	}
	if validHybridConfigShape(map[string]any{"levelRoutes": []any{map[string]any{"bogus": 1}}}) {
		t.Fatal("等级项未知键必须无效")
	}
	if !validHybridConfigShape(map[string]any{"levelRoutes": []any{map[string]any{"minLevel": 1}}}) {
		t.Fatal("合法等级项必须有效")
	}
}

// ---- 错误类型与杂项 ----

func TestWlErrorTypesAndMisc(t *testing.T) {
	if (&ConflictError{Message: "c"}).Error() != "c" {
		t.Fatal("ConflictError.Error 透传")
	}
	if (&VersionConflictError{Message: "v"}).Error() != "v" {
		t.Fatal("VersionConflictError.Error 透传")
	}
	if (&ValidationError{Message: "e"}).Error() != "e" {
		t.Fatal("ValidationError.Error 透传")
	}
	if (&ValidationCacheInvalidationError{Message: "i"}).Error() != "i" {
		t.Fatal("ValidationCacheInvalidationError.Error 透传")
	}
	if ensureCtx(nil) == nil {
		t.Fatal("nil ctx 必须回退 Background")
	}
	if actorResolver(httptest.NewRequest(http.MethodGet, "/", nil)) != "anonymous" {
		t.Fatal("无会话必须返回 anonymous")
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request = request.WithContext(authsys.WithAuthContext(request.Context(), &authsys.AuthContext{SystemAccountID: "acc-1"}))
	if actorResolver(request) != "acc-1" {
		t.Fatal("有会话必须返回账户 ID")
	}
	labels := map[string]string{
		"name": "名称", "description": "说明", "mode": "路由模式", "status": "状态",
		"groupBindings": "绑定分组", "normalRoutingConfig": "普通路由调度配置", "hybridRoutingConfig": "混合智能路由配置",
	}
	for field, want := range labels {
		if got := patchFieldLabel(field); got != want {
			t.Fatalf("patchFieldLabel(%q)=%q want=%q", field, got, want)
		}
	}
	if got := patchFieldLabel("unknown"); got != "unknown" {
		t.Fatalf("未知字段必须原样返回: %q", got)
	}
}

func TestWlDuplicateNameError(t *testing.T) {
	if duplicateNameError(nil, "n") != nil {
		t.Fatal("nil 错误必须返回 nil")
	}
	if duplicateNameError(errors.New("unrelated"), "n") != nil {
		t.Fatal("无关错误必须返回 nil")
	}
	matched := duplicateNameError(errors.New("UNIQUE constraint failed: idx_route_strategies_owner_name_unique"), "n1")
	if matched == nil || !contains(matched.Error(), "n1") {
		t.Fatalf("matched=%v", matched)
	}
	if duplicateNameError(errors.New("UNIQUE constraint failed: route_strategies.system_account_id, route_strategies.name"), "n2") == nil {
		t.Fatal("SQLite 唯一约束必须识别")
	}
	if duplicateNameError(errors.New("UNIQUE constraint failed: juhe_business.route_strategies.system_account_id, juhe_business.route_strategies.name"), "n3") == nil {
		t.Fatal("PG 唯一约束必须识别")
	}
}

// ---- 配置输入选择 ----

func TestWlMutationInputSelection(t *testing.T) {
	currentNormal := &NormalRoutingConfig{SchedulingPreference: "cost_first"}
	currentHybrid := &HybridRoutingConfig{ScoringModel: "m"}
	input := MutationInput{}
	// 非 normal 模式且无新值：normalInput 必须为 nil。
	if got := input.normalInput(ModeHybridSmart, currentNormal); got != nil {
		t.Fatalf("got=%v", got)
	}
	// normal 模式且无新值：透传当前配置的 raw 形态。
	raw := input.normalInput(ModeNormal, currentNormal)
	if raw == nil {
		t.Fatal("必须透传当前配置")
	}
	// 带 HasNormalConfig 时透传 raw。
	withRaw := MutationInput{HasNormalConfig: true, NormalConfigRaw: map[string]any{}}
	if withRaw.normalInput(ModeNormal, nil) == nil {
		t.Fatal("显式 raw 必须透传")
	}
	// hybrid 对称分支。
	if got := input.hybridInput(ModeNormal, currentHybrid); got != nil {
		t.Fatalf("got=%v", got)
	}
	if input.hybridInput(ModeHybridSmart, currentHybrid) == nil {
		t.Fatal("必须透传当前混合配置")
	}
	withHybrid := MutationInput{HasHybridConfig: true, HybridConfigRaw: map[string]any{}}
	if withHybrid.hybridInput(ModeHybridSmart, nil) == nil {
		t.Fatal("显式 raw 必须透传")
	}
	if rawForMode(ModeNormal, ModeHybridSmart, "x") != nil {
		t.Fatal("模式不匹配必须为 nil")
	}
	if rawForMode(ModeNormal, ModeNormal, "x") == nil {
		t.Fatal("模式匹配必须透传")
	}
	if typedToRaw(nil) != nil {
		t.Fatal("nil 输入必须为 nil")
	}
	decoded := typedToRaw(currentNormal)
	if _, ok := decoded.(map[string]any); !ok {
		t.Fatalf("typedToRaw=%T", decoded)
	}
	if gatewayRuntimeChanged([]string{"name"}) {
		t.Fatal("name 不触发运行时失效")
	}
	if !gatewayRuntimeChanged([]string{"status"}) || !gatewayRuntimeChanged([]string{"mode"}) ||
		!gatewayRuntimeChanged([]string{"groupBindings"}) || !gatewayRuntimeChanged([]string{"normalRoutingConfig"}) ||
		!gatewayRuntimeChanged([]string{"hybridRoutingConfig"}) {
		t.Fatal("运行时相关字段必须触发失效")
	}
}

func TestWlRouteStrategyConfigJSONFromRaw(t *testing.T) {
	normalRaw := map[string]any{"schedulingPreference": "cost_first"}
	if stored, err := routeStrategyConfigJSONFromRaw(normalRaw, nil); err != nil || stored.Valid {
		t.Fatalf("cost_first 必须存 NULL: %v %v", stored, err)
	}
	speedRaw := map[string]any{"schedulingPreference": "speed_first"}
	if stored, err := routeStrategyConfigJSONFromRaw(speedRaw, nil); err != nil || !stored.Valid {
		t.Fatalf("speed_first 必须落盘: %v %v", stored, err)
	}
	if _, err := routeStrategyConfigJSONFromRaw("bad", nil); err == nil {
		t.Fatal("普通配置损坏必须报错")
	}
	if _, err := routeStrategyConfigJSONFromRaw(nil, "bad"); err == nil {
		t.Fatal("混合配置损坏必须报错")
	}
	if _, err := routeStrategyConfigJSONFromRaw(nil, nil); err != nil {
		t.Fatalf("err=%v", err)
	}
}

// ---- Store 层 Delete / ListPage 空页 ----

func TestWlStoreDeleteAndEmptyPage(t *testing.T) {
	env := newTestEnv(t)
	ownerID := env.login(t, "wldelete", "pass-1234", "super_admin")
	ctx := context.Background()
	t.Run("删除不存在的策略返回未删除", func(t *testing.T) {
		result, err := env.store.Delete(ctx, "missing-id", AccessScope{ViewerID: ownerID, IsAdmin: true})
		if err != nil || result.Deleted {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
	t.Run("空列表走空页", func(t *testing.T) {
		page, err := env.store.ListPage(ctx, AccessScope{ViewerID: ownerID, IsAdmin: true}, ListOptions{Page: 2, PageSize: 5})
		if err != nil || page == nil || len(page.Items) != 0 {
			t.Fatalf("page=%+v err=%v", page, err)
		}
		if page.GeneratedAt == "" {
			t.Fatal("空页必须携带生成时间")
		}
	})
	t.Run("bindingSnapshot 空集合", func(t *testing.T) {
		bindings, apiKeys, previews, err := env.store.bindingSnapshot(ctx, nil)
		if err != nil || len(bindings) != 0 || len(apiKeys) != 0 || len(previews) != 0 {
			t.Fatalf("bindings=%v apiKeys=%v previews=%v err=%v", bindings, apiKeys, previews, err)
		}
	})
	t.Run("options 空结果", func(t *testing.T) {
		options, err := env.store.ListOptionsPage(ctx, AccessScope{ViewerID: ownerID, IsAdmin: true}, OptionsQuery{IDs: []string{"missing"}})
		if err != nil || len(options) != 0 {
			t.Fatalf("options=%+v err=%v", options, err)
		}
	})
}

// ---- speed-first 运行时富化 ----

// wlRuntimeFacade 是 SpeedFirstRuntimeFacade 的确定性假实现。
type wlRuntimeFacade struct {
	items     []SpeedFirstRuntimeItem
	available bool
	err       error
}

func (f *wlRuntimeFacade) ListDegradedRuntime(context.Context, *string, []string) ([]SpeedFirstRuntimeItem, bool, error) {
	return f.items, f.available, f.err
}

func (f *wlRuntimeFacade) ClearDegradedRuntime(context.Context, string) (int, error) {
	return 0, nil
}

func speedFirstItem(id string, count int) ListItem {
	deadline := 30000
	list := ListItem{ID: id, Mode: ModeNormal, NormalRoutingConfig: &NormalRoutingConfig{
		SchedulingPreference: "speed_first", FirstByteDeadlineMs: &deadline,
	}}
	_ = count
	return list
}

func TestWlEnrichSpeedFirstSummaries(t *testing.T) {
	ctx := context.Background()
	t.Run("无 facade 直接返回", func(t *testing.T) {
		env := newTestEnv(t)
		env.routerDeps().enrichSpeedFirstSummaries(ctx, []ListItem{speedFirstItem("s1", 0)})
	})
	t.Run("空列表直接返回", func(t *testing.T) {
		env := newTestEnvFull(t, &wlRuntimeFacade{available: true})
		env.routerDeps().enrichSpeedFirstSummaries(ctx, nil)
	})
	t.Run("非速度优先策略不查询", func(t *testing.T) {
		env := newTestEnvFull(t, &wlRuntimeFacade{available: true})
		list := []ListItem{{ID: "s1", Mode: ModeWeighted}}
		env.routerDeps().enrichSpeedFirstSummaries(ctx, list)
		if list[0].SpeedFirstLatency != nil {
			t.Fatal("非速度优先不得附加运行时摘要")
		}
	})
	t.Run("运行时不可用渲染降级摘要", func(t *testing.T) {
		env := newTestEnvFull(t, &wlRuntimeFacade{available: false})
		list := []ListItem{speedFirstItem("s1", 0)}
		env.routerDeps().enrichSpeedFirstSummaries(ctx, list)
		if list[0].SpeedFirstLatency == nil || list[0].SpeedFirstLatency.RuntimeAvailable {
			t.Fatalf("summary=%+v", list[0].SpeedFirstLatency)
		}
	})
	t.Run("查询失败渲染降级摘要", func(t *testing.T) {
		env := newTestEnvFull(t, &wlRuntimeFacade{err: errors.New("boom")})
		list := []ListItem{speedFirstItem("s1", 0)}
		env.routerDeps().enrichSpeedFirstSummaries(ctx, list)
		if list[0].SpeedFirstLatency == nil || list[0].SpeedFirstLatency.RuntimeAvailable {
			t.Fatalf("summary=%+v", list[0].SpeedFirstLatency)
		}
	})
	t.Run("可用时附加降级计数", func(t *testing.T) {
		env := newTestEnvFull(t, &wlRuntimeFacade{available: true, items: []SpeedFirstRuntimeItem{
			{AccountID: "a1", DegradedUntil: "t100", Scope: runtimeScope{RouteStrategyID: "s1"}},
			{AccountID: "a2", DegradedUntil: "t200", Scope: runtimeScope{RouteStrategyID: "s1"}},
		}})
		list := []ListItem{speedFirstItem("s1", 0)}
		env.routerDeps().enrichSpeedFirstSummaries(ctx, list)
		if list[0].SpeedFirstLatency == nil || !list[0].SpeedFirstLatency.RuntimeAvailable || list[0].SpeedFirstLatency.DegradedCount != 2 {
			t.Fatalf("summary=%+v", list[0].SpeedFirstLatency)
		}
	})
	t.Run("超过 50 个策略短路为不可用", func(t *testing.T) {
		env := newTestEnvFull(t, &wlRuntimeFacade{available: true})
		list := make([]ListItem, 0, 51)
		for index := 0; index < 51; index++ {
			list = append(list, speedFirstItem("s"+string(rune('a'+index%26)), 0))
		}
		env.routerDeps().enrichSpeedFirstSummaries(ctx, list)
		if list[0].SpeedFirstLatency == nil || list[0].SpeedFirstLatency.RuntimeAvailable {
			t.Fatal("超量必须短路为不可用")
		}
	})
}

func TestWlLookupNameAndEmptyPage(t *testing.T) {
	env := newTestEnv(t)
	ownerID := env.login(t, "wllookup", "pass-1234", "super_admin")
	ctx := context.Background()
	t.Run("lookupName 命中与缺失", func(t *testing.T) {
		if got := env.store.lookupName(ctx, ownerID); got == nil || *got != "wllookup_name" {
			t.Fatalf("got=%v", got)
		}
		if got := env.store.lookupName(ctx, "missing"); got != nil {
			t.Fatalf("缺失账户=%v", got)
		}
	})
	t.Run("非管理员无过滤范围得到空页", func(t *testing.T) {
		page, err := env.store.ListPage(ctx, AccessScope{ViewerID: "u", IsAdmin: false}, ListOptions{Page: 1, PageSize: 10})
		if err != nil || page == nil || len(page.Items) != 0 {
			t.Fatalf("page=%+v err=%v", page, err)
		}
	})
}

// routerDeps 返回挂在 kernel 上的路由 Deps（复用 env.store）。
func (e *testEnv) routerDeps() *Deps {
	return &Deps{Store: e.store, Auth: e.deps, Sink: e.sink}
}

func TestWlStoreDeleteProtections(t *testing.T) {
	env := newTestEnv(t)
	ownerID := env.login(t, "wlprotect", "pass-1234", "super_admin")
	ctx := context.Background()
	createStrategy := func(id, name string, isDefault int) string {
		now := "2026-01-01T00:00:00Z"
		env.exec(t, `INSERT INTO route_strategies (id, system_account_id, name, mode, status, is_default, created_at, updated_at)
			VALUES (?, ?, ?, 'normal', 'active', ?, ?, ?)`, id, ownerID, name, isDefault, now, now)
		return id
	}
	t.Run("默认策略不允许删除", func(t *testing.T) {
		id := createStrategy("rs-default", "默认路由", 1)
		_, err := env.store.Delete(ctx, id, AccessScope{ViewerID: ownerID, IsAdmin: true})
		if err == nil || !contains(err.Error(), "默认策略路由不允许删除") {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("被 API Key 引用不允许删除", func(t *testing.T) {
		id := createStrategy("rs-referenced", "被引用路由", 0)
		env.exec(t, `INSERT INTO api_keys (id, system_account_id, route_strategy_id, name, created_at, updated_at)
			VALUES ('key-1', ?, ?, 'k', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, ownerID, id)
		_, err := env.store.Delete(ctx, id, AccessScope{ViewerID: ownerID, IsAdmin: true})
		if err == nil || !contains(err.Error(), "API Key 使用") {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("成功删除并级联清理绑定", func(t *testing.T) {
		id := createStrategy("rs-deletable", "可删除路由", 0)
		env.exec(t, `INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, created_at, updated_at)
			VALUES ('rsg-1', ?, ?, 'grp-x', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, id, ownerID)
		result, err := env.store.Delete(ctx, id, AccessScope{ViewerID: ownerID, IsAdmin: true})
		if err != nil || !result.Deleted {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		if got := env.count(t, `SELECT COUNT(1) FROM route_strategy_groups WHERE route_strategy_id = ?`, id); got != 0 {
			t.Fatalf("绑定残留=%d", got)
		}
	})
	t.Run("无权限访问者得到未删除", func(t *testing.T) {
		id := createStrategy("rs-other", "他人路由", 0)
		result, err := env.store.Delete(ctx, id, AccessScope{ViewerID: "someone-else", IsAdmin: false})
		if err != nil || result.Deleted {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
	t.Run("无上下文访问者得到空页", func(t *testing.T) {
		page, err := env.store.ListPage(ctx, AccessScope{}, ListOptions{Page: 1, PageSize: 10})
		if err != nil || page == nil || len(page.Items) != 0 || page.PageSize != 10 {
			t.Fatalf("page=%+v err=%v", page, err)
		}
	})
}

func TestWlStoreClosedDBErrorPaths(t *testing.T) {
	env := newTestEnv(t)
	ownerID := env.login(t, "wlclosed", "pass-1234", "super_admin")
	if err := env.db.Close(); err != nil {
		t.Fatal(err)
	}
	access := AccessScope{ViewerID: ownerID, IsAdmin: true}
	t.Run("options 查询失败返回 500", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/options", nil)
		request = request.WithContext(authsys.WithAuthContext(request.Context(), &authsys.AuthContext{SystemAccountID: ownerID, Role: "super_admin"}))
		env.routerDeps().options(recorder, request, access)
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d", recorder.Code)
		}
	})
	t.Run("editBasic 查询失败返回 500", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/id", nil)
		env.routerDeps().editBasic(recorder, request, access)
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d", recorder.Code)
		}
	})
	t.Run("bindingSnapshot 查询失败上抛", func(t *testing.T) {
		if _, _, _, err := env.store.bindingSnapshot(context.Background(), []string{"s1"}); err == nil {
			t.Fatal("关闭数据库后必须上抛错误")
		}
	})
	t.Run("列表查询失败上抛", func(t *testing.T) {
		if _, err := env.store.ListPage(context.Background(), access, ListOptions{Page: 1, PageSize: 10}); err == nil {
			t.Fatal("关闭数据库后必须上抛错误")
		}
	})
}

func TestWlSmallPureHelpers(t *testing.T) {
	t.Run("parseBindingWeight", func(t *testing.T) {
		if _, message := parseBindingWeight("x"); message != "分组权重必须是数字" {
			t.Fatalf("message=%q", message)
		}
		if _, message := parseBindingWeight(1.5); message != "分组权重必须是整数" {
			t.Fatalf("message=%q", message)
		}
		if _, message := parseBindingWeight(float64(101)); message != "分组权重必须在 1-100 之间" {
			t.Fatalf("message=%q", message)
		}
		if weight, message := parseBindingWeight(float64(50)); weight != 50 || message != "" {
			t.Fatalf("weight=%d message=%q", weight, message)
		}
	})
	t.Run("nullableDescription", func(t *testing.T) {
		if got, err := nullableDescription(MutationInput{}); err != nil || got != nil {
			t.Fatalf("无描述字段必须为 nil: %v %v", got, err)
		}
		description := "  内容  "
		got, err := nullableDescription(MutationInput{HasDescription: true, Description: &description})
		if err != nil || got == nil || *got != "内容" {
			t.Fatalf("got=%v err=%v", got, err)
		}
		blank := "   "
		if got, _ := nullableDescription(MutationInput{HasDescription: true, Description: &blank}); got != nil {
			t.Fatal("空白必须折叠为 NULL")
		}
		tooLong := make([]rune, 201)
		for index := range tooLong {
			tooLong[index] = '字'
		}
		long := string(tooLong)
		if _, err := nullableDescription(MutationInput{HasDescription: true, Description: &long}); err == nil {
			t.Fatal("超长必须报错")
		}
	})
}

package authz

// authz 第三波补充（wd_ 前缀，独占新增）：列表过滤器矩阵与 ListPage 归一、
// 路由层读错误 → 500 与乐观锁 409 契约、选项查询子句（ids/provider/keyword/
// preferDefault）、团队明细对缺失名称投影的空值容错。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWdListFilterMatrixAndListPage(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedTeamWithMember(t, "team_f", "grantee")
	f.seedGroup(t, "grp_f1", "owner")
	f.seedGroup(t, "grp_f2", "owner")
	direct, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_f1",
		GranteeType: "system_account", GranteeID: "grantee", Remark: ptrString("备注关键字"),
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	teamGrant, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_f2",
		GranteeType: "team", GranteeID: "team_f",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	viewer := accessInfo{ViewerID: "admin", IsAdmin: true}

	cases := []struct {
		name    string
		filters Filters
		wantIDs []string
	}{
		{"资源类型", Filters{ResourceType: "group", Status: "all"}, []string{direct.Item.ID, teamGrant.Item.ID}},
		{"资源 ID", Filters{ResourceID: "grp_f2", Status: "all"}, []string{teamGrant.Item.ID}},
		{"owner 过滤", Filters{ResourceOwnerSystemAccountID: "owner", Status: "all"}, []string{direct.Item.ID, teamGrant.Item.ID}},
		{"被授权人过滤", Filters{GranteeSystemAccountID: "grantee", Status: "all"}, []string{direct.Item.ID}},
		{"团队过滤", Filters{TeamID: "team_f", Status: "all"}, []string{teamGrant.Item.ID}},
		{"状态过滤", Filters{Status: StatusActive}, []string{direct.Item.ID, teamGrant.Item.ID}},
		{"来源 manual", Filters{SourceType: "manual", Status: "all"}, []string{direct.Item.ID}},
		{"来源 team", Filters{SourceType: "team", Status: "all"}, []string{teamGrant.Item.ID}},
		{"关键字命中备注", Filters{Keyword: "备注关键", Status: "all"}, []string{direct.Item.ID}},
		{"关键字命中资源 ID", Filters{Keyword: "grp_f2", Status: "all"}, []string{teamGrant.Item.ID}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			items, _, _, err := f.store.ListItemsPage(context.Background(), tc.filters, 1, 50, viewer)
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]bool{}
			for _, item := range items {
				got[item.ID] = true
			}
			for _, want := range tc.wantIDs {
				if !got[want] {
					t.Fatalf("结果缺 %s: %#v", want, items)
				}
			}
			if len(items) != len(tc.wantIDs) {
				t.Fatalf("结果数错误: %#v", items)
			}
		})
	}

	// ListPage 归一：page<1 → 1、pageSize<1 → 50、pageSize>500 → 500。
	items, total, hasMore, err := f.store.ListPage(context.Background(), Filters{Status: "all"}, 0, 0)
	if err != nil || len(items) != 2 || total != 2 || hasMore {
		t.Fatalf("ListPage 默认归一错误: %d %d %v %v", len(items), total, hasMore, err)
	}
	items, _, _, err = f.store.ListPage(context.Background(), Filters{Status: "all"}, 1, 99999)
	if err != nil || len(items) != 2 {
		t.Fatalf("ListPage pageSize 上限错误: %d %v", len(items), err)
	}
	// pageSize=1 → hasMore。
	_, _, hasMore, err = f.store.ListPage(context.Background(), Filters{Status: "all"}, 1, 1)
	if err != nil || !hasMore {
		t.Fatalf("ListPage hasMore 错误: %v %v", hasMore, err)
	}
}

func TestWdRouteReadErrorsSurfaceAs400Or500(t *testing.T) {
	f, deps, sink := wdNewRouteFixture(t)
	created, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_r",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	_ = created

	// RequireSelf 无认证上下文 → 401。
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	recorder := httptest.NewRecorder()
	deps.RequireSelf(next).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("RequireSelf 无认证应 401: %d", recorder.Code)
	}

	// 关闭底层库：读/写错误各自落 500/400 兜底。
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	recorder = httptest.NewRecorder()
	deps.list(recorder, wdGetReq(t, http.MethodGet, "/", wdAuthCtx("admin", "admin")), false)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("list 读错误应 500: %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	findRequest := wdGetReq(t, http.MethodGet, "/", wdAuthCtx("admin", "admin"))
	findRequest.SetPathValue("id", "any")
	deps.find(recorder, findRequest, false)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("find 读错误应 500: %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	revokeRequest := wdJSONRequest(t, http.MethodDelete, "/", `{"expectedUpdatedAt":"2026-01-01T00:00:00Z"}`, wdAuthCtx("owner", "user"))
	revokeRequest.SetPathValue("id", "any")
	deps.revokeScoped(recorder, revokeRequest, true)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("revoke 存储错误应 400: %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	patchRequest := wdJSONRequest(t, http.MethodPatch, "/", `{"expectedUpdatedAt":"2026-01-01T00:00:00Z","status":"active"}`, wdAuthCtx("owner", "user"))
	patchRequest.SetPathValue("id", "any")
	deps.patchScoped(recorder, patchRequest, false, true)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("patch 存储错误应 400: %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	returnRequest := wdJSONRequest(t, http.MethodDelete, "/", `{"expectedUpdatedAt":"2026-01-01T00:00:00Z"}`, wdAuthCtx("grantee", "user"))
	returnRequest.SetPathValue("id", "any")
	deps.returnValue(recorder, returnRequest, true)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("return 存储错误应 400: %d", recorder.Code)
	}
	if len(sink.entries) != 0 {
		t.Fatalf("失败路径不应写操作日志: %#v", sink.entries)
	}
}

func TestWdCreateRouteLimitsAndConflict(t *testing.T) {
	f, deps, _ := wdNewRouteFixture(t)
	// limits 合法 JSON：创建成功并在结果里回显。
	recorder := httptest.NewRecorder()
	deps.create(recorder, wdJSONRequest(t, http.MethodPost, "/__aisys__/api/authorizations?systemAccountId=owner",
		`{"resourceType":"group","resourceId":"grp_r","granteeType":"system_account","granteeId":"grantee","limits":{"daily":{"enabled":true,"limit":3}}}`,
		wdAuthCtx("admin", "admin")))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("带 limits 创建应 201: %d %s", recorder.Code, recorder.Body.String())
	}
	payload := wdDecodeMap(t, recorder)
	if payload["item"] == nil {
		t.Fatalf("创建结果缺 item: %#v", payload)
	}

	// 同资源换内容重复授权 → 领域 Fail 原文 400（Create 只在并发复活竞争
	// 中返回 Conflict，路由的 errorsAsConflict 分支无法确定性驱动）。
	changed := `{"resourceType":"group","resourceId":"grp_r","granteeType":"system_account","granteeId":"grantee","remark":"changed"}`
	recorder = httptest.NewRecorder()
	deps.create(recorder, wdJSONRequest(t, http.MethodPost, "/__aisys__/api/authorizations?systemAccountId=owner",
		changed, wdAuthCtx("admin", "admin")))
	if recorder.Code != http.StatusBadRequest || decodeWd(recorder) != "该资源已授权给该用户，请勿重复授权" {
		t.Fatalf("内容变化的重复授权应 400 原文: %d %s", recorder.Code, recorder.Body.String())
	}

	// 非 JSON body → 400 请求体无效。
	recorder = httptest.NewRecorder()
	deps.create(recorder, wdJSONRequest(t, http.MethodPost, "/__aisys__/api/my-authorizations",
		`{not-json`, wdAuthCtx("owner", "user")))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("坏 JSON 应 400: %d %s", recorder.Code, recorder.Body.String())
	}

	// 关闭库 → 创建失败兜底 400 创建授权失败。
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	recorder = httptest.NewRecorder()
	deps.create(recorder, wdJSONRequest(t, http.MethodPost, "/__aisys__/api/my-authorizations",
		`{"resourceType":"group","resourceId":"grp_x","granteeType":"team","granteeId":"team_r"}`, wdAuthCtx("owner", "user")))
	if recorder.Code != http.StatusBadRequest || decodeWd(recorder) != "创建授权失败" {
		t.Fatalf("存储错误应 400 兜底: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestWdGranteeGroupOptionClauses(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "grantee", "active")
	f.seedGroup(t, "grp_c1", "grantee")
	f.seedGroup(t, "grp_c2", "grantee")
	if _, err := f.db.Exec(`UPDATE groups SET enabled = 1, provider_code = 'openai', is_default = 1, updated_at = '2026-01-02T00:00:00Z' WHERE id = 'grp_c1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE groups SET enabled = 1, provider_code = 'anthropic', is_default = 0, updated_at = '2026-01-03T00:00:00Z' WHERE id = 'grp_c2'`); err != nil {
		t.Fatal(err)
	}
	base := authorizationGranteeGroupOptionListOptions{
		authorizationPrincipalOptionListOptions: authorizationPrincipalOptionListOptions{Limit: 50},
		GranteeSystemAccountID:                  "grantee",
	}
	// ids 子句。
	withIDs := base
	withIDs.IDs = []string{"grp_c2", "grp_c2", "grp_c1"}
	rows, err := f.store.ListAuthorizationGranteeGroups(context.Background(), withIDs)
	if err != nil || len(rows) != 2 {
		t.Fatalf("ids 子句错误: %#v %v", rows, err)
	}
	// providerCode 子句。
	withProvider := base
	withProvider.ProviderCode = "anthropic"
	rows, err = f.store.ListAuthorizationGranteeGroups(context.Background(), withProvider)
	if err != nil || len(rows) != 1 || rows[0].ID != "grp_c2" {
		t.Fatalf("provider 子句错误: %#v %v", rows, err)
	}
	// keyword 子句（组名前缀）。
	withKeyword := base
	withKeyword.Keyword = "Group grp_c1"
	rows, err = f.store.ListAuthorizationGranteeGroups(context.Background(), withKeyword)
	if err != nil || len(rows) != 1 || rows[0].ID != "grp_c1" {
		t.Fatalf("keyword 子句错误: %#v %v", rows, err)
	}
	// preferDefault=false：去掉 is_default 优先，按 updated_at DESC → grp_c2 在前。
	noPrefer := base
	preferFalse := false
	noPrefer.PreferDefault = &preferFalse
	noPrefer.HasPreferDefault = true
	rows, err = f.store.ListAuthorizationGranteeGroups(context.Background(), noPrefer)
	if err != nil || len(rows) != 2 || rows[0].ID != "grp_c2" {
		t.Fatalf("preferDefault=false 排序错误: %#v %v", rows, err)
	}
	// 默认（未显式 false）：is_default DESC 优先 → grp_c1 在前。
	rows, err = f.store.ListAuthorizationGranteeGroups(context.Background(), base)
	if err != nil || len(rows) != 2 || rows[0].ID != "grp_c1" {
		t.Fatalf("默认排序错误: %#v %v", rows, err)
	}

	// Store 读失败 → granteeGroups 500（options_routes 68-71）。
	broken := newFixture(t)
	deps := &Deps{Store: broken.store}
	if err := broken.db.Close(); err != nil {
		t.Fatal(err)
	}
	recorder := wdAuthzGet(t, deps.granteeGroups, "/?granteeSystemAccountId=grantee", nil)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("分组选项读失败应 500: %d", recorder.Code)
	}
}

func TestWdUsageRoutesReadErrorsSurfaceAs500(t *testing.T) {
	// 未附时区源且无 system_settings 表：resolveUsageRange 失败 → 500。
	f := newFixture(t)
	deps := &Deps{Store: f.store}
	auth := wdAuthCtx("admin", "admin")
	recorder := httptest.NewRecorder()
	deps.usageRows(recorder, wdGetReq(t, http.MethodGet, "/?resourceType=account", auth), "team", false)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("usage rows 时区失败应 500: %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	deps.usageSummary(recorder, wdGetReq(t, http.MethodGet, "/?resourceType=account", auth), "user", false)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("usage summary 时区失败应 500: %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	detailRequest := wdGetReq(t, http.MethodGet, "/", auth)
	detailRequest.SetPathValue("id", "any")
	deps.usageDetail(recorder, detailRequest, false)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("usage detail 时区失败应 500: %d", recorder.Code)
	}

	// 附上时区但关闭库：查询失败 → rows/summary/detail 全 500。
	f2 := newUsageFixture(t)
	deps2 := &Deps{Store: f2.store}
	if err := f2.db.Close(); err != nil {
		t.Fatal(err)
	}
	recorder = httptest.NewRecorder()
	deps2.usageRows(recorder, wdGetReq(t, http.MethodGet, "/?resourceType=account", auth), "team", false)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("usage rows 读失败应 500: %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	deps2.usageSummary(recorder, wdGetReq(t, http.MethodGet, "/?resourceType=account", auth), "team", false)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("usage summary 读失败应 500: %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	detailRequest2 := wdGetReq(t, http.MethodGet, "/", auth)
	detailRequest2.SetPathValue("id", "any")
	deps2.usageDetail(recorder, detailRequest2, false)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("usage detail 读失败应 500: %d", recorder.Code)
	}
}

func TestWdTeamUsageRowsMissingNameProjection(t *testing.T) {
	f := newUsageFixture(t)
	// 窗口行引用未注册的团队/资源：名称投影为 nil 而非报错。
	insertTeamWindowRow(t, f, "owner1", "team_ghost", "account", "acc_ghost", 2, 20, 10, 0.2, nil)
	result, err := f.store.teamUsageRows(context.Background(), UsageFilters{}, accessInfo{ViewerID: "owner1"}, wdDetailRange, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("行数错误: %#v", result.Rows)
	}
	if result.Rows[0].TeamName != "" || result.Rows[0].ResourceName != "" {
		t.Fatalf("缺失名称应为空串: %#v", result.Rows[0])
	}
}

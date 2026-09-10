package authz

// authz 授权路由族补充测试（wd_ 前缀，独占新增）：直接注入 AuthContext 驱动
// handler（create/patch/revoke/return/list/find/usage），锁定错误契约顺序
//（scope query → body schema → 领域校验）、409/404/400 分支与操作日志 sink
// 的写入时机。Mount 注册契约经 kernel 全链路断言（未认证 → 门禁 401 而非 404）。

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

type wdSink struct {
	entries []authsys.OperationLogEntry
}

func (s *wdSink) Record(entry authsys.OperationLogEntry, r *http.Request) {
	s.entries = append(s.entries, entry)
}

func wdAuthCtx(systemAccountID, role string) *authsys.AuthContext {
	return &authsys.AuthContext{SystemAccountID: systemAccountID, Username: systemAccountID, DisplayName: systemAccountID, Role: role, SessionID: "s"}
}

func wdJSONRequest(t *testing.T, method, target, body string, auth *authsys.AuthContext) *http.Request {
	t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Content-Length", strconv.Itoa(len(body)))
	}
	if auth != nil {
		request = request.WithContext(authsys.WithAuthContext(request.Context(), auth))
	}
	return request
}

func wdCreateBody(resourceType, resourceID, granteeType, granteeID, targetGroupID string) string {
	payload := map[string]any{
		"resourceType": resourceType, "resourceId": resourceID,
		"granteeType": granteeType, "granteeId": granteeID,
	}
	if targetGroupID != "" {
		payload["targetGroupId"] = targetGroupID
	}
	encoded, _ := json.Marshal(payload)
	return string(encoded)
}

// wdNewRouteFixture 建好 owner/grantee/团队/分组并提供 admin/user 上下文。
func wdNewRouteFixture(t *testing.T) (*fixture, *Deps, *wdSink) {
	t.Helper()
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedAccount(t, "other", "active")
	f.seedGroup(t, "grp_r", "owner")
	f.seedTeamWithMember(t, "team_r", "grantee")
	sink := &wdSink{}
	deps := &Deps{Store: f.store, Sink: sink, Auth: &authsys.Deps{}}
	return f, deps, sink
}

func TestWdCreateRouteContracts(t *testing.T) {
	_, deps, sink := wdNewRouteFixture(t)

	// 无认证上下文 → 401。
	recorder := httptest.NewRecorder()
	deps.create(recorder, wdJSONRequest(t, http.MethodPost, "/__aisys__/api/authorizations",
		wdCreateBody("group", "grp_r", "system_account", "grantee", ""), nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("无认证应 401: %d", recorder.Code)
	}

	// 管理面未指定授权人 → 400。
	recorder = httptest.NewRecorder()
	deps.create(recorder, wdJSONRequest(t, http.MethodPost, "/__aisys__/api/authorizations",
		wdCreateBody("group", "grp_r", "system_account", "grantee", ""), wdAuthCtx("admin", "admin")))
	if recorder.Code != http.StatusBadRequest || decodeWd(recorder) != "管理员新增授权时必须指定授权人" {
		t.Fatalf("admin 未指定 scope 应 400: %d %s", recorder.Code, recorder.Body.String())
	}

	// 管理面指定授权人 → 201 Created。
	recorder = httptest.NewRecorder()
	deps.create(recorder, wdJSONRequest(t, http.MethodPost, "/__aisys__/api/authorizations?systemAccountId=owner",
		wdCreateBody("group", "grp_r", "system_account", "grantee", ""), wdAuthCtx("admin", "admin")))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("admin create 应 201: %d %s", recorder.Code, recorder.Body.String())
	}
	if len(sink.entries) != 1 || sink.entries[0].Action != "create" || sink.entries[0].Mode != "admin" {
		t.Fatalf("create 操作日志错误: %#v", sink.entries)
	}

	// 重复幂等创建 → 200 + previousStatus 缺失；重新激活 → 200 + previousStatus。
	recorder = httptest.NewRecorder()
	deps.create(recorder, wdJSONRequest(t, http.MethodPost, "/__aisys__/api/authorizations?systemAccountId=owner",
		wdCreateBody("group", "grp_r", "system_account", "grantee", ""), wdAuthCtx("admin", "admin")))
	if recorder.Code != http.StatusOK {
		t.Fatalf("幂等创建应 200: %d %s", recorder.Code, recorder.Body.String())
	}
	if _, hasPrevious := wdDecodeMap(t, recorder)["previousStatus"]; hasPrevious {
		t.Fatalf("幂等创建不应带 previousStatus")
	}

	// 参数校验矩阵。
	badBodies := []struct {
		name    string
		body    string
		scope   string
		message string
	}{
		{"缺字段", `{"resourceType":"group"}`, "owner", "授权参数不合法"},
		{"资源类型非法", wdCreateBody("model", "m1", "system_account", "grantee", ""), "owner", "授权参数不合法"},
		{"被授权类型非法", wdCreateBody("group", "grp_r", "robot", "grantee", ""), "owner", "授权参数不合法"},
		{"账户给个人缺目标分组", wdCreateBody("account", "acc1", "system_account", "grantee", ""), "owner", "授权 AI 账户给个人时必须选择目标分组"},
		{"分组授权带目标分组", wdCreateBody("group", "grp_r", "system_account", "grantee", "tg1"), "owner", "只有授权 AI 账户给个人时可以指定目标分组"},
		{"limits 为显式 null", `{"resourceType":"group","resourceId":"grp_r","granteeType":"system_account","granteeId":"grantee","limits":null}`, "owner", "授权参数不合法"},
	}
	for _, tc := range badBodies {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			deps.create(recorder, wdJSONRequest(t, http.MethodPost, "/__aisys__/api/authorizations?systemAccountId="+tc.scope,
				tc.body, wdAuthCtx("admin", "admin")))
			if recorder.Code != http.StatusBadRequest || decodeWd(recorder) != tc.message {
				t.Fatalf("%s 应 400/%s: %d %s", tc.name, tc.message, recorder.Code, recorder.Body.String())
			}
		})
	}

	// 资源所有者自授 → 领域 Fail 原样透出（errorsAsFail 分支）。
	recorder = httptest.NewRecorder()
	deps.create(recorder, wdJSONRequest(t, http.MethodPost, "/__aisys__/api/authorizations?systemAccountId=owner",
		wdCreateBody("group", "grp_r", "system_account", "owner", ""), wdAuthCtx("admin", "admin")))
	if recorder.Code != http.StatusBadRequest || decodeWd(recorder) != "不能授权给资源所有者自己" {
		t.Fatalf("自授应 400 原文: %d %s", recorder.Code, recorder.Body.String())
	}

	// my-* 面：scope 钉到调用者，日志 mode=self；目标分组放行账户授权。
	recorder = httptest.NewRecorder()
	deps.create(recorder, wdJSONRequest(t, http.MethodPost, "/__aisys__/api/my-authorizations",
		wdCreateBody("group", "grp_r", "team", "team_r", ""), wdAuthCtx("owner", "user")))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("my create 应 201: %d %s", recorder.Code, recorder.Body.String())
	}
	if len(sink.entries) < 2 || sink.entries[len(sink.entries)-1].Mode != "self" {
		t.Fatalf("my create 日志 mode 应为 self: %#v", sink.entries)
	}

	// 未知 body 键 → 严格对象拒绝（decodeStrictJSON 契约）。
	recorder = httptest.NewRecorder()
	deps.create(recorder, wdJSONRequest(t, http.MethodPost, "/__aisys__/api/my-authorizations",
		`{"resourceType":"group","resourceId":"grp_r","granteeType":"team","granteeId":"team_r","extra":1}`, wdAuthCtx("owner", "user")))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("未知键应 400: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestWdRevokeAndReturnContracts(t *testing.T) {
	f, deps, sink := wdNewRouteFixture(t)
	created, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_r",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	version := created.Item.UpdatedAt
	request := func(method, target, body string, auth *authsys.AuthContext) *http.Request {
		request := wdJSONRequest(t, method, target, body, auth)
		request.SetPathValue("id", created.Item.ID)
		return request
	}

	// revoke：无认证 / 版本非法 / 成功。
	recorder := httptest.NewRecorder()
	deps.revokeScoped(recorder, request(http.MethodDelete, "/__aisys__/api/authorizations/"+created.Item.ID, `{"expectedUpdatedAt":"`+version+`"}`, nil), false)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("revoke 无认证应 401: %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	deps.revokeScoped(recorder, request(http.MethodDelete, "/", `{"expectedUpdatedAt":"bad"}`, wdAuthCtx("owner", "user")), false)
	if recorder.Code != http.StatusBadRequest || decodeWd(recorder) != "授权配置版本格式不正确" {
		t.Fatalf("版本非法应 400: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	deps.revokeScoped(recorder, request(http.MethodDelete, "/", `{"expectedUpdatedAt":"`+version+`"}`, wdAuthCtx("owner", "user")), true)
	if recorder.Code != http.StatusOK {
		t.Fatalf("self revoke 应 200: %d %s", recorder.Code, recorder.Body.String())
	}
	if len(sink.entries) == 0 || sink.entries[len(sink.entries)-1].Action != "revoke" {
		t.Fatalf("revoke 日志缺失: %#v", sink.entries)
	}

	// grantee 发起回收 → 410 语义不存在（scope 边界），路由层是 400 兜底或 404。
	revived, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_r",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	recorder = httptest.NewRecorder()
	deps.revokeScoped(recorder, request(http.MethodDelete, "/", `{"expectedUpdatedAt":"`+revived.Item.UpdatedAt+`"}`, wdAuthCtx("grantee", "user")), true)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("grantee 回收应 404（owner scope 钉到 viewer）: %d %s", recorder.Code, recorder.Body.String())
	}

	// return：非法版本 / 成功 204 / 幂等 not_found。
	recorder = httptest.NewRecorder()
	deps.returnValue(recorder, request(http.MethodDelete, "/", `{"expectedUpdatedAt":"x"}`, wdAuthCtx("grantee", "user")), true)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("return 版本非法应 400: %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	deps.returnValue(recorder, request(http.MethodDelete, "/", `{"expectedUpdatedAt":"`+revived.Item.UpdatedAt+`"}`, wdAuthCtx("grantee", "user")), true)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("return 成功应 204: %d %s", recorder.Code, recorder.Body.String())
	}
	if len(sink.entries) == 0 || sink.entries[len(sink.entries)-1].Action != "return" {
		t.Fatalf("return 日志缺失: %#v", sink.entries)
	}
	recorder = httptest.NewRecorder()
	deps.returnValue(recorder, request(http.MethodDelete, "/", `{"expectedUpdatedAt":"`+revived.Item.UpdatedAt+`"}`, wdAuthCtx("grantee", "user")), true)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("同版本二次 return 应乐观锁 409: %d %s", recorder.Code, recorder.Body.String())
	}

	// 空视图身份（admin 无 filter + 空 viewer）→ 404 兜底。
	recorder = httptest.NewRecorder()
	deps.returnValue(recorder, request(http.MethodDelete, "/", `{"expectedUpdatedAt":"`+version+`"}`, wdAuthCtx("", "admin")), false)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("空 grantee 应 404: %d", recorder.Code)
	}
}

func TestWdPatchContracts(t *testing.T) {
	f, deps, sink := wdNewRouteFixture(t)
	created, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_r",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	request := func(body string, auth *authsys.AuthContext) *http.Request {
		request := wdJSONRequest(t, http.MethodPatch, "/__aisys__/api/my-authorizations/"+created.Item.ID, body, auth)
		request.SetPathValue("id", created.Item.ID)
		return request
	}

	// 无认证 / 内容缺失。
	recorder := httptest.NewRecorder()
	deps.patchScoped(recorder, request(`{"expectedUpdatedAt":"`+created.Item.UpdatedAt+`"}`, nil), false, true)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("patch 无认证应 401: %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	deps.patchScoped(recorder, request(`{"expectedUpdatedAt":"`+created.Item.UpdatedAt+`"}`, wdAuthCtx("owner", "user")), false, true)
	if recorder.Code != http.StatusBadRequest || decodeWd(recorder) != "请提供要修改的授权内容" {
		t.Fatalf("无内容应 400: %d %s", recorder.Code, recorder.Body.String())
	}
	// 非法 status。
	recorder = httptest.NewRecorder()
	deps.patchScoped(recorder, request(`{"expectedUpdatedAt":"`+created.Item.UpdatedAt+`","status":"weird"}`, wdAuthCtx("owner", "user")), false, true)
	if recorder.Code != http.StatusBadRequest || decodeWd(recorder) != "修改授权参数不合法" {
		t.Fatalf("非法 status 应 400: %d %s", recorder.Code, recorder.Body.String())
	}
	// 非法 expiresAt。
	recorder = httptest.NewRecorder()
	deps.patchScoped(recorder, request(`{"expectedUpdatedAt":"`+created.Item.UpdatedAt+`","expiresAt":"nope"}`, wdAuthCtx("owner", "user")), false, true)
	if recorder.Code != http.StatusBadRequest || decodeWd(recorder) != "过期时间格式不正确" {
		t.Fatalf("非法 expiresAt 应 400: %d %s", recorder.Code, recorder.Body.String())
	}
	// 成功路径：status + limits。
	recorder = httptest.NewRecorder()
	deps.patchScoped(recorder, request(`{"expectedUpdatedAt":"`+created.Item.UpdatedAt+`","status":"paused","limits":{"daily":{"enabled":true,"limit":2}}}`,
		wdAuthCtx("owner", "user")), false, true)
	if recorder.Code != http.StatusOK {
		t.Fatalf("patch 应 200: %d %s", recorder.Code, recorder.Body.String())
	}
	payload := wdDecodeMap(t, recorder)
	if payload["status"] != "paused" || payload["limits"] == nil {
		t.Fatalf("patch 结果错误: %#v", payload)
	}
	if len(sink.entries) == 0 || sink.entries[len(sink.entries)-1].Action != "update" {
		t.Fatalf("patch 日志缺失: %#v", sink.entries)
	}

	// expire 面：expireOnly 拒绝 status 键（严格 schema）。
	recorder = httptest.NewRecorder()
	deps.patchScoped(recorder, request(`{"expectedUpdatedAt":"`+created.Item.UpdatedAt+`","status":"active"}`, wdAuthCtx("owner", "user")), true, true)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expire 面带 status 应 400: %d %s", recorder.Code, recorder.Body.String())
	}
	// expire 成功（mode=update_expire）。
	current, err := f.store.GetGrantForMutation(context.Background(), nil, created.Item.ID)
	if err != nil || current == nil {
		t.Fatalf("read grant: %v", err)
	}
	recorder = httptest.NewRecorder()
	deps.patchScoped(recorder, request(`{"expectedUpdatedAt":"`+current.UpdatedAt+`","expiresAt":"2027-01-01T00:00:00Z"}`, wdAuthCtx("owner", "user")), true, true)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expire 应 200: %d %s", recorder.Code, recorder.Body.String())
	}
	if len(sink.entries) == 0 || sink.entries[len(sink.entries)-1].Action != "update_expire" {
		t.Fatalf("expire 日志缺失: %#v", sink.entries)
	}

	// not_found 与乐观锁 conflict。
	recorder = httptest.NewRecorder()
	deps.patchScoped(recorder, wdJSONRequestWdID(t, "missing-id", `{"expectedUpdatedAt":"2026-01-01T00:00:00Z","status":"active"}`, wdAuthCtx("owner", "user")), false, true)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("缺失授权应 404: %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	deps.patchScoped(recorder, wdJSONRequestWdID(t, created.Item.ID, `{"expectedUpdatedAt":"2020-01-01T00:00:00Z","status":"active"}`, wdAuthCtx("owner", "user")), false, true)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("版本冲突应 409: %d %s", recorder.Code, recorder.Body.String())
	}
}

func wdJSONRequestWdID(t *testing.T, id, body string, auth *authsys.AuthContext) *http.Request {
	t.Helper()
	request := wdJSONRequest(t, http.MethodPatch, "/__aisys__/api/authorizations/"+id+"/expire", body, auth)
	request.SetPathValue("id", id)
	return request
}

func TestWdListFindVisibleToContracts(t *testing.T) {
	f, deps, _ := wdNewRouteFixture(t)
	created, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_r",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}

	// list：非法分页 400 / 超长 keyword 400 / admin 与 self 成功。
	recorder := httptest.NewRecorder()
	deps.list(recorder, wdGetReq(t, http.MethodGet, "/?page=0", nil), false)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("page=0 应 400: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	deps.list(recorder, wdGetReq(t, http.MethodGet, "/?keyword="+strings.Repeat("长", 121), nil), false)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("超长 keyword 应 400: %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	deps.list(recorder, wdGetReq(t, http.MethodGet, "/?status=all&direction=inbound", wdAuthCtx("grantee", "user")), true)
	if recorder.Code != http.StatusOK {
		t.Fatalf("self list 应 200: %d %s", recorder.Code, recorder.Body.String())
	}
	if items := wdDecodeMap(t, recorder)["items"].([]any); len(items) != 1 {
		t.Fatalf("self list（入站方向）应看到自己被授权的行: %#v", items)
	}
	recorder = httptest.NewRecorder()
	deps.list(recorder, wdGetReq(t, http.MethodGet, "/?resourceType=account", wdAuthCtx("admin", "admin")), false)
	if recorder.Code != http.StatusOK {
		t.Fatalf("admin list 应 200: %d %s", recorder.Code, recorder.Body.String())
	}

	// find：未找到 / 无关用户 404 / owner 可见。
	recorder = httptest.NewRecorder()
	request := wdGetReq(t, http.MethodGet, "/", nil)
	request.SetPathValue("id", "missing")
	deps.find(recorder, request, true)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("missing find 应 404: %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	request = wdGetReq(t, http.MethodGet, "/", nil)
	request.SetPathValue("id", created.Item.ID)
	deps.find(recorder, request, true)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("无关用户 find 应 404: %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	request = wdGetReq(t, http.MethodGet, "/", wdAuthCtx("grantee", "user"))
	request.SetPathValue("id", created.Item.ID)
	deps.find(recorder, request, true)
	if recorder.Code != http.StatusOK {
		t.Fatalf("grantee find 应 200: %d %s", recorder.Code, recorder.Body.String())
	}

	// visibleTo 矩阵。
	summary, err := f.store.Find(context.Background(), created.Item.ID)
	if err != nil || summary == nil {
		t.Fatalf("find: %v", err)
	}
	if !deps.visibleTo(summary, "owner") || !deps.visibleTo(summary, "grantee") || deps.visibleTo(summary, "other") {
		t.Fatalf("visibleTo 矩阵错误")
	}
}

func TestWdMountRegistersAuthorizationRoutes(t *testing.T) {
	f := newFixture(t)
	deps := &Deps{Store: f.store, Auth: &authsys.Deps{}}
	gateway := kernel.New(kernel.Options{CompressionDisabled: true})
	deps.Mount(gateway)
	handler := gateway.Handler()
	targets := []string{
		"/__aisys__/api/my-authorizations",
		"/__aisys__/api/authorizations",
		"/__aisys__/api/authorizations/usage/team-details",
		"/__aisys__/api/my-authorizations/usage/user-summary",
		"/__aisys__/api/authorizations/x/usage",
	}
	for _, target := range targets {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s 未认证应 401（未注册会是 404）: %d %s", target, recorder.Code, recorder.Body.String())
		}
	}
	// MountAuthz 是 wiring 入口别名。
	MountAuthz(kernel.New(kernel.Options{CompressionDisabled: true}), deps)
}

func TestWdUsageRouteContracts(t *testing.T) {
	f := newUsageFixture(t)
	sink := &wdSink{}
	deps := &Deps{Store: f.store, Sink: sink}
	auth := wdAuthCtx("admin", "admin")

	// 严格查询键：未知键拒绝（zod 原文）。
	recorder := httptest.NewRecorder()
	deps.usageRows(recorder, wdGetReq(t, http.MethodGet, "/?extra=1", auth), "team", false)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("未知键应 400: %d %s", recorder.Code, recorder.Body.String())
	}
	// resourceId 无 resourceType。
	recorder = httptest.NewRecorder()
	deps.usageRows(recorder, wdGetReq(t, http.MethodGet, "/?resourceId=x", auth), "team", false)
	if recorder.Code != http.StatusBadRequest || decodeWd(recorder) != "按资源筛选时必须指定资源类型" {
		t.Fatalf("resource 过滤不完整应 400: %d %s", recorder.Code, recorder.Body.String())
	}
	// user 维度：grantee 参数 present-but-blank 才拒绝（absent 合法，等同不过滤）。
	recorder = httptest.NewRecorder()
	deps.usageRows(recorder, wdGetReq(t, http.MethodGet, "/?resourceType=account&granteeSystemAccountId=", auth), "user", false)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("user 维度空白 grantee 应 400: %d %s", recorder.Code, recorder.Body.String())
	}
	// 非法分页。
	recorder = httptest.NewRecorder()
	deps.usageRows(recorder, wdGetReq(t, http.MethodGet, "/?resourceType=account&page=-1", auth), "team", false)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("非法页码应 400: %d", recorder.Code)
	}
	// 合法读（空数据 → 200 空行）。
	recorder = httptest.NewRecorder()
	deps.usageRows(recorder, wdGetReq(t, http.MethodGet, "/?resourceType=account&page=1&pageSize=20", auth), "team", false)
	if recorder.Code != http.StatusOK {
		t.Fatalf("usage team-details 应 200: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	deps.usageRows(recorder, wdGetReq(t, http.MethodGet, "/?resourceType=account&granteeSystemAccountId=grantee", auth), "user", false)
	if recorder.Code != http.StatusOK {
		t.Fatalf("usage user-details 应 200: %d", recorder.Code)
	}
	// summary 家族：严格键 + 成功。
	recorder = httptest.NewRecorder()
	deps.usageSummary(recorder, wdGetReq(t, http.MethodGet, "/?resourceType=account&page=1", auth), "team", false)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("summary 严格键应拒绝分页参数: %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	deps.usageSummary(recorder, wdGetReq(t, http.MethodGet, "/?resourceType=account", auth), "team", false)
	if recorder.Code != http.StatusOK {
		t.Fatalf("usage team-summary 应 200: %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	deps.usageSummary(recorder, wdGetReq(t, http.MethodGet, "/?resourceType=account&granteeSystemAccountId=grantee", auth), "user", false)
	if recorder.Code != http.StatusOK {
		t.Fatalf("usage user-summary 应 200: %d", recorder.Code)
	}

	// usageDetail：非法分页 400 / 缺 id 400 / 未找到 404。
	recorder = httptest.NewRecorder()
	deps.usageDetail(recorder, wdGetReq(t, http.MethodGet, "/?page=x", auth), false)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("detail 非法分页应 400: %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	deps.usageDetail(recorder, wdGetReq(t, http.MethodGet, "/", auth), false)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("detail 缺 id 应 400: %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	missing := wdGetReq(t, http.MethodGet, "/", auth)
	missing.SetPathValue("id", "missing")
	deps.usageDetail(recorder, missing, false)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("detail 未找到应 404: %d", recorder.Code)
	}
}

// decodeWd 读取 {"message": "..."} 载荷的 message 字段。
func decodeWd(recorder *httptest.ResponseRecorder) string {
	var payload struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		return "<undecodable>"
	}
	return payload.Message
}

func wdDecodeMap(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var payload struct {
		Data map[string]any `json:"data"`
	}
	if err := json.NewDecoder(bytes.NewReader(recorder.Body.Bytes())).Decode(&payload); err != nil {
		t.Fatalf("decode: %v (%s)", err, recorder.Body.String())
	}
	return payload.Data
}

// wdGetReq 构造无 body 的 GET 请求。
func wdGetReq(t *testing.T, method, target string, auth *authsys.AuthContext) *http.Request {
	t.Helper()
	return wdJSONRequest(t, method, target, "", auth)
}

// TestWdUsageRouteUnknownKeyVerbatim 走真实 kernel + authsys 中间件（开发自动
// 登录）：本地化写包装保住 zod 英文原文，锁定 unknown query key 的逐字契约。
func TestWdUsageRouteUnknownKeyVerbatim(t *testing.T) {
	env := newUsageRouteEnv(t, "admin")
	status, body := env.do(t, http.MethodGet, "/__aisys__/api/authorizations/usage/team-details?extra=1", "")
	if status != http.StatusBadRequest || !strings.Contains(body, "Unrecognized key(s) in object: 'extra'") {
		t.Fatalf("未知键应 400 zod 原文: %d %s", status, body)
	}
}

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
		"granteeType": granteeType,
	}
	// Recipient-consent model: normal users address the receiver by exact
	// username / team name; granteeId and targetGroupId are retired.
	if granteeType == "team" {
		payload["teamName"] = "Team " + granteeID
	} else {
		payload["granteeUsername"] = granteeID
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
	f.seedGroup(t, "grp_self", "owner")
	f.seedGroup(t, "grp_team", "owner")
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

	// 管理面代授权方发起 → 进入待授权方审批，接收方尚不可见。
	recorder = httptest.NewRecorder()
	deps.create(recorder, wdJSONRequest(t, http.MethodPost, "/__aisys__/api/authorizations?systemAccountId=owner",
		wdCreateBody("group", "grp_r", "system_account", "grantee", ""), wdAuthCtx("admin", "admin")))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("admin create 应 201: %d %s", recorder.Code, recorder.Body.String())
	}
	created := wdDecodeMap(t, recorder)
	if created["status"] != StatusPendingOwnerApproval || created["ownerApprovalRequired"] != true || created["created"] != true {
		t.Fatalf("admin create 结果错误: %#v", created)
	}
	if len(sink.entries) != 1 || sink.entries[0].Action != "create_offer" || sink.entries[0].Mode != "admin" {
		t.Fatalf("create 操作日志错误: %#v", sink.entries)
	}

	// 重复发起同一 offer → 200 + created=false，不重复推进状态。
	recorder = httptest.NewRecorder()
	deps.create(recorder, wdJSONRequest(t, http.MethodPost, "/__aisys__/api/authorizations?systemAccountId=owner",
		wdCreateBody("group", "grp_r", "system_account", "grantee", ""), wdAuthCtx("admin", "admin")))
	if recorder.Code != http.StatusOK {
		t.Fatalf("幂等创建应 200: %d %s", recorder.Code, recorder.Body.String())
	}
	if duplicate := wdDecodeMap(t, recorder); duplicate["created"] != false {
		t.Fatalf("幂等创建不应重复创建: %#v", duplicate)
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
		{"目标分组已退役", wdCreateBody("account", "acc1", "system_account", "grantee", "tg1"), "owner", "授权不再支持 targetGroupId，请由接收方接受后自行绑定"},
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

	// 资源所有者自授 → 领域错误原样透出。
	recorder = httptest.NewRecorder()
	deps.create(recorder, wdJSONRequest(t, http.MethodPost, "/__aisys__/api/authorizations?systemAccountId=owner",
		wdCreateBody("group", "grp_r", "system_account", "owner", ""), wdAuthCtx("admin", "admin")))
	if recorder.Code != http.StatusBadRequest || decodeWd(recorder) != "不能授权给资源所有者自己" {
		t.Fatalf("自授应 400 原文: %d %s", recorder.Code, recorder.Body.String())
	}

	// 管理面 scope 指向非资源所有者 → 404 语义的领域错误。
	recorder = httptest.NewRecorder()
	deps.create(recorder, wdJSONRequest(t, http.MethodPost, "/__aisys__/api/authorizations?systemAccountId=grantee",
		wdCreateBody("group", "grp_r", "system_account", "other", ""), wdAuthCtx("admin", "admin")))
	if recorder.Code != http.StatusBadRequest || decodeWd(recorder) != "授权资源不存在" {
		t.Fatalf("scope 不匹配应 400/授权资源不存在: %d %s", recorder.Code, recorder.Body.String())
	}

	// my-* 面：scope 钉到调用者，普通用户直接发给个人进入 pending。
	recorder = httptest.NewRecorder()
	deps.create(recorder, wdJSONRequest(t, http.MethodPost, "/__aisys__/api/my-authorizations",
		wdCreateBody("group", "grp_self", "system_account", "grantee", ""), wdAuthCtx("owner", "user")))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("my create 应 201: %d %s", recorder.Code, recorder.Body.String())
	}
	if mine := wdDecodeMap(t, recorder); mine["status"] != StatusPending {
		t.Fatalf("个人 offer 应为 pending: %#v", mine)
	}
	if len(sink.entries) < 2 || sink.entries[len(sink.entries)-1].Mode != "self" {
		t.Fatalf("my create 日志 mode 应为 self: %#v", sink.entries)
	}

	// my-* 团队 offer → pending_team_approval，由团队审批后逐成员发送。
	recorder = httptest.NewRecorder()
	deps.create(recorder, wdJSONRequest(t, http.MethodPost, "/__aisys__/api/my-authorizations",
		wdCreateBody("group", "grp_team", "team", "team_r", ""), wdAuthCtx("owner", "user")))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("my team create 应 201: %d %s", recorder.Code, recorder.Body.String())
	}
	if teamOffer := wdDecodeMap(t, recorder); teamOffer["status"] != StatusPendingTeamApproval {
		t.Fatalf("团队 offer 应为 pending_team_approval: %#v", teamOffer)
	}

	// 普通用户不允许直接指定 ID。
	recorder = httptest.NewRecorder()
	deps.create(recorder, wdJSONRequest(t, http.MethodPost, "/__aisys__/api/my-authorizations",
		`{"resourceType":"group","resourceId":"grp_r","granteeType":"system_account","granteeSystemAccountId":"grantee"}`, wdAuthCtx("owner", "user")))
	if recorder.Code != http.StatusBadRequest || decodeWd(recorder) != "普通用户请使用精确用户名发起授权" {
		t.Fatalf("普通用户带 ID 应 400: %d %s", recorder.Code, recorder.Body.String())
	}

	// 未知 body 键 → 严格对象拒绝（decodeStrictJSON 契约）。
	recorder = httptest.NewRecorder()
	deps.create(recorder, wdJSONRequest(t, http.MethodPost, "/__aisys__/api/my-authorizations",
		`{"resourceType":"group","resourceId":"grp_r","granteeType":"team","teamName":"Team team_r","extra":1}`, wdAuthCtx("owner", "user")))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("未知键应 400: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestWdRevokeAndReturnContracts(t *testing.T) {
	f, deps, sink := wdNewRouteFixture(t)
	created, err := f.store.CreateOffer(context.Background(), OfferInput{
		ResourceType: "group", ResourceID: "grp_r",
		GranteeType: "system_account", GranteeUsername: "grantee",
	}, "owner", false, "owner")
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, target, body string, auth *authsys.AuthContext) *http.Request {
		request := wdJSONRequest(t, method, target, body, auth)
		request.SetPathValue("id", created.GrantID)
		return request
	}

	// revoke：无认证 / 非所有者 / 所有者成功 / 幂等重放。
	recorder := httptest.NewRecorder()
	deps.revokeScoped(recorder, request(http.MethodDelete, "/", "", nil), false)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("revoke 无认证应 401: %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	deps.revokeScoped(recorder, request(http.MethodDelete, "/", "", wdAuthCtx("grantee", "user")), true)
	if recorder.Code != http.StatusBadRequest || decodeWd(recorder) != "授权不存在" {
		t.Fatalf("接收方不得回收授权: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	deps.revokeScoped(recorder, request(http.MethodDelete, "/", "", wdAuthCtx("owner", "user")), true)
	if recorder.Code != http.StatusOK {
		t.Fatalf("self revoke 应 200: %d %s", recorder.Code, recorder.Body.String())
	}
	if len(sink.entries) == 0 || sink.entries[len(sink.entries)-1].Action != "revoke" {
		t.Fatalf("revoke 日志缺失: %#v", sink.entries)
	}
	recorder = httptest.NewRecorder()
	deps.revokeScoped(recorder, request(http.MethodDelete, "/", "", wdAuthCtx("owner", "user")), true)
	if recorder.Code != http.StatusOK {
		t.Fatalf("revoke 幂等重放应 200: %d %s", recorder.Code, recorder.Body.String())
	}

	// 旧的整体归还路由已退役：团队/个人都必须按来源归还。
	recorder = httptest.NewRecorder()
	deps.returnValue(recorder, request(http.MethodDelete, "/", "", wdAuthCtx("grantee", "user")), true)
	var conflictBody struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &conflictBody); err != nil {
		t.Fatalf("return decode: %v (%s)", err, recorder.Body.String())
	}
	if recorder.Code != http.StatusConflict || conflictBody.Code != "state_conflict" ||
		!strings.Contains(conflictBody.Message, "按来源归还") {
		t.Fatalf("旧归还路由应 409 state_conflict: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestWdPatchContracts(t *testing.T) {
	f, deps, sink := wdNewRouteFixture(t)
	created, err := f.store.CreateOffer(context.Background(), OfferInput{
		ResourceType: "group", ResourceID: "grp_r",
		GranteeType: "system_account", GranteeUsername: "grantee",
	}, "owner", false, "owner")
	if err != nil {
		t.Fatal(err)
	}
	// Authorizations can only be edited while active/paused; the offer flow's
	// acceptance path is covered separately, so promote the row directly here.
	if _, err := f.db.Exec(fixtureBind(`UPDATE resource_authorization_grants SET status = 'active' WHERE id = ?`), created.GrantID); err != nil {
		t.Fatal(err)
	}
	request := func(body string, auth *authsys.AuthContext) *http.Request {
		request := wdJSONRequest(t, http.MethodPatch, "/__aisys__/api/my-authorizations/"+created.GrantID, body, auth)
		request.SetPathValue("id", created.GrantID)
		return request
	}

	// 无认证 / 内容缺失。
	recorder := httptest.NewRecorder()
	deps.patchScoped(recorder, request(`{}`, nil), false, true)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("patch 无认证应 401: %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	deps.patchScoped(recorder, request(`{}`, wdAuthCtx("owner", "user")), false, true)
	if recorder.Code != http.StatusBadRequest || decodeWd(recorder) != "请提供要修改的授权内容" {
		t.Fatalf("无内容应 400: %d %s", recorder.Code, recorder.Body.String())
	}
	// status 已从更新契约移除（暂停/恢复走独立语义），严格对象拒绝。
	recorder = httptest.NewRecorder()
	deps.patchScoped(recorder, request(`{"status":"paused"}`, wdAuthCtx("owner", "user")), false, true)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status 键应被严格 schema 拒绝: %d %s", recorder.Code, recorder.Body.String())
	}
	// 非法 expiresAt。
	recorder = httptest.NewRecorder()
	deps.patchScoped(recorder, request(`{"expiresAt":"nope"}`, wdAuthCtx("owner", "user")), false, true)
	if recorder.Code != http.StatusBadRequest || decodeWd(recorder) != "过期时间格式不正确" {
		t.Fatalf("非法 expiresAt 应 400: %d %s", recorder.Code, recorder.Body.String())
	}
	// 成功路径：remark + limits（响应只回 updated）。
	recorder = httptest.NewRecorder()
	deps.patchScoped(recorder, request(`{"remark":"调整额度","limits":{"daily":{"enabled":true,"limit":2}}}`,
		wdAuthCtx("owner", "user")), false, true)
	if recorder.Code != http.StatusOK {
		t.Fatalf("patch 应 200: %d %s", recorder.Code, recorder.Body.String())
	}
	if payload := wdDecodeMap(t, recorder); payload["updated"] != true {
		t.Fatalf("patch 结果错误: %#v", payload)
	}
	if len(sink.entries) == 0 || sink.entries[len(sink.entries)-1].Action != "update" {
		t.Fatalf("patch 日志缺失: %#v", sink.entries)
	}

	// expire 面：expireOnly 拒绝 limits/remark 键（严格 schema）。
	recorder = httptest.NewRecorder()
	deps.patchScoped(recorder, request(`{"remark":"x"}`, wdAuthCtx("owner", "user")), true, true)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expire 面带 remark 应 400: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	deps.patchScoped(recorder, request(`{"expiresAt":"2027-01-01T00:00:00Z"}`, wdAuthCtx("owner", "user")), true, true)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expire 应 200: %d %s", recorder.Code, recorder.Body.String())
	}
	if len(sink.entries) == 0 || sink.entries[len(sink.entries)-1].Action != "update" {
		t.Fatalf("expire 日志缺失: %#v", sink.entries)
	}

	// 非所有者 / 缺失授权 → 领域错误 400。
	recorder = httptest.NewRecorder()
	deps.patchScoped(recorder, request(`{"remark":"x"}`, wdAuthCtx("grantee", "user")), false, true)
	if recorder.Code != http.StatusBadRequest || decodeWd(recorder) != "授权不存在" {
		t.Fatalf("非所有者 patch 应 400/授权不存在: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	deps.patchScoped(recorder, wdJSONRequestWdID(t, "missing-id", `{"remark":"x"}`, wdAuthCtx("owner", "user")), false, true)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("缺失授权应 400: %d %s", recorder.Code, recorder.Body.String())
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
	created, err := f.store.CreateOffer(context.Background(), OfferInput{
		ResourceType: "group", ResourceID: "grp_r",
		GranteeType: "system_account", GranteeUsername: "grantee",
	}, "owner", false, "owner")
	if err != nil {
		t.Fatal(err)
	}

	// list：分页参数已退役（不再解析 page/keyword），inbound 只返回接收方可见来源。
	recorder := httptest.NewRecorder()
	deps.list(recorder, wdGetReq(t, http.MethodGet, "/?page=0", wdAuthCtx("grantee", "user")), true)
	if recorder.Code != http.StatusOK {
		t.Fatalf("self list 应忽略 page: %d %s", recorder.Code, recorder.Body.String())
	}
	if items := wdDecodeMap(t, recorder)["items"].([]any); len(items) != 1 {
		t.Fatalf("self list（入站方向）应看到自己被授权的行: %#v", items)
	}
	recorder = httptest.NewRecorder()
	deps.list(recorder, wdGetReq(t, http.MethodGet, "/?direction=outbound", wdAuthCtx("owner", "user")), true)
	if recorder.Code != http.StatusOK {
		t.Fatalf("owner outbound list 应 200: %d %s", recorder.Code, recorder.Body.String())
	}
	if items := wdDecodeMap(t, recorder)["items"].([]any); len(items) != 1 {
		t.Fatalf("outbound 应看到自己发起的行: %#v", items)
	}
	recorder = httptest.NewRecorder()
	deps.list(recorder, wdGetReq(t, http.MethodGet, "/?resourceType=account", wdAuthCtx("admin", "admin")), false)
	if recorder.Code != http.StatusOK {
		t.Fatalf("admin list 应 200: %d %s", recorder.Code, recorder.Body.String())
	}

	// find：未找到 / 无关用户 404 / owner 与 grantee 可见。
	recorder = httptest.NewRecorder()
	request := wdGetReq(t, http.MethodGet, "/", nil)
	request.SetPathValue("id", "missing")
	deps.find(recorder, request, true)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("missing find 应 404: %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	request = wdGetReq(t, http.MethodGet, "/", wdAuthCtx("other", "user"))
	request.SetPathValue("id", created.GrantID)
	deps.find(recorder, request, true)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("无关用户 find 应 404: %d", recorder.Code)
	}
	for _, viewer := range []string{"grantee", "owner"} {
		recorder = httptest.NewRecorder()
		request = wdGetReq(t, http.MethodGet, "/", wdAuthCtx(viewer, "user"))
		request.SetPathValue("id", created.GrantID)
		deps.find(recorder, request, true)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s find 应 200: %d %s", viewer, recorder.Code, recorder.Body.String())
		}
	}

	// visibleTo 矩阵。
	summary, err := f.store.Find(context.Background(), created.GrantID)
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

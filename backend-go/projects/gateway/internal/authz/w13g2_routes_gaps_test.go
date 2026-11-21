// w13g2 覆盖缺口补充测试（只新增测试，不改生产逻辑）：
//   - routes.go Mount 各注册闭包与 usage 家族闭包此前仅经 401 未认证探测，
//     闭包体未执行；本文件经 newUsageRouteEnv 的真实 kernel + authsys 会话
//     链路（开发自动登录）驱动两面前端到端的 handler 分支。
//   - readJSONBody / decodeStrictJSON 的传输分支（Content-Type 门、413、
//     读失败 400、空白体、严格 schema 目标反序列化失败）用手工请求驱动。
//   - 不可达登记：routes.go RequireSelf 内 auth == nil 的 401 守卫
//     （routes.go:240-243）不可达——authsys sessionMiddleware 在全部放行
//     路径（令牌认证与开发自动登录）都通过 WithAuthContext 注入身份，
//     next 到达时 AuthContextFrom 恒非 nil；本任务只登记归因，不删守卫。
package authz

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

func w13g2DataMap(t *testing.T, body string) map[string]any {
	t.Helper()
	var payload struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return payload.Data
}

func w13g2UpdateAt(t *testing.T, body string) string {
	t.Helper()
	value, _ := w13g2DataMap(t, body)["updatedAt"].(string)
	if value == "" {
		if item, ok := w13g2DataMap(t, body)["item"].(map[string]any); ok {
			value, _ = item["updatedAt"].(string)
		}
	}
	if value == "" {
		t.Fatalf("missing updatedAt in %q", body)
	}
	return value
}

// TestW13g2MountSelfSurfaceChain drives the my-* surface through the full
// recipient-consent chain: offer → team approval → member acceptance →
// receiver bind for a group, plus the personal patch/expire/extend/return/
// revoke lifecycle and the account-accept instance projection.
func TestW13g2MountSelfSurfaceChain(t *testing.T) {
	env := newUsageRouteEnv(t, "owner1")
	f := env.f
	f.seedAccount(t, "owner", "active")
	f.seedGroup(t, "grp_w13s", "owner1")
	f.seedGroup(t, "grp2_w13s", "owner1")
	f.seedGroup(t, "grp3_w13s", "owner1")
	f.seedTeamWithMember(t, "team_w13s", "grantee")
	seedResourceAccount(t, f, "acc_w13s", "owner1", "源账户")
	// grantee 的默认分组：provider_code 与源账户一致（bind 的供应商校验），
	// is_default=1。
	if _, err := f.db.Exec(`INSERT INTO groups (id, name, system_account_id, status, enabled, is_default, updated_at)
		VALUES ('tg_w13s', '默认分组', 'grantee', 'active', 1, 1, '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE accounts SET vendor_code = 'gpt', connection_driver_code = 'gpt' WHERE id = 'acc_w13s'`); err != nil {
		t.Fatal(err)
	}

	const selfPrefix = "/__aisys__/api/my-authorizations"
	// POST my-authorizations: the owner creates a team offer; it awaits team
	// approval and creates no source yet.
	status, body := env.do(t, http.MethodPost, selfPrefix,
		`{"resourceType":"group","resourceId":"grp_w13s","granteeType":"team","teamName":"Team team_w13s"}`)
	if status != http.StatusCreated {
		t.Fatalf("my create team 应 201: %d %s", status, body)
	}
	teamGrantID := w13g2DataMap(t, body)["grantId"].(string)
	if got := w13g2DataMap(t, body)["status"]; got != StatusPendingTeamApproval {
		t.Fatalf("team create status = %v, want pending_team_approval", got)
	}

	// The team creator (owner, not the resource owner) approves; this expands
	// eligible members into pending sources without instances/bindings.
	teamCreatorEnv := newUsageRouteEnv(t, "owner", f)
	status, body = teamCreatorEnv.do(t, http.MethodPost,
		"/__aisys__/api/my-teams/team_w13s/authorization-offers/"+teamGrantID+"/approve", `{}`)
	if status != http.StatusOK {
		t.Fatalf("team approve 应 200: %d %s", status, body)
	}
	var teamSourceID string
	if err := f.db.QueryRow(fixtureBind(`SELECT id FROM resource_authorization_sources
		WHERE grant_id = ? AND status = 'pending'`), teamGrantID).Scan(&teamSourceID); err != nil {
		t.Fatal(err)
	}

	// The receiver accepts the member source; the group snapshot is built with
	// settings.enabled=0 and no group binding.
	granteeEnv := newUsageRouteEnv(t, "grantee", f)
	var observed string
	var version int
	if err := f.db.QueryRow(fixtureBind(`SELECT observed_source_revision, revision_version
		FROM resource_authorization_sources WHERE id = ?`), teamSourceID).Scan(&observed, &version); err != nil {
		t.Fatal(err)
	}
	status, body = granteeEnv.do(t, http.MethodPost,
		"/__aisys__/api/my-authorization-sources/"+teamSourceID+"/accept",
		`{"expectedRevision":"`+observed+`","expectedRevisionVersion":`+strconv.Itoa(version)+`}`)
	if status != http.StatusOK || !strings.Contains(body, `"status":"active"`) {
		t.Fatalf("member accept 应 active: %d %s", status, body)
	}
	var enabled int
	if err := f.db.QueryRow(fixtureBind(`SELECT enabled FROM group_authorization_settings
		WHERE authorization_id = (SELECT authorization_id FROM resource_authorization_sources WHERE id = ?)`),
		teamSourceID).Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if enabled != 0 {
		t.Fatalf("首次接受后 settings.enabled = %d, want 0", enabled)
	}
	status, body = granteeEnv.do(t, http.MethodPost,
		"/__aisys__/api/my-authorization-sources/"+teamSourceID+"/bind", `{}`)
	if status != http.StatusOK {
		t.Fatalf("source bind 应 200: %d %s", status, body)
	}
	if err := f.db.QueryRow(fixtureBind(`SELECT enabled FROM group_authorization_settings
		WHERE authorization_id = (SELECT authorization_id FROM resource_authorization_sources WHERE id = ?)`),
		teamSourceID).Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if enabled != 1 {
		t.Fatalf("bind 后 settings.enabled = %d, want 1", enabled)
	}

	// direct grant: return 只支持个人授权，后续 patch/expire/extend/return/
	// revoke 用这条 direct grant 走完整生命周期。
	status, body = env.do(t, http.MethodPost, selfPrefix,
		`{"resourceType":"group","resourceId":"grp2_w13s","granteeType":"system_account","granteeUsername":"grantee"}`)
	if status != http.StatusCreated {
		t.Fatalf("my create direct 应 201: %d %s", status, body)
	}
	directID := w13g2DataMap(t, body)["grantId"].(string)
	directSourceID := w13g2DataMap(t, body)["sourceId"].(string)

	// GET 列表与详情闭包。
	if status, body = env.do(t, http.MethodGet, selfPrefix, ""); status != http.StatusOK {
		t.Fatalf("my list 应 200: %d %s", status, body)
	}
	if status, body = env.do(t, http.MethodGet, selfPrefix+"/"+directID, ""); status != http.StatusOK {
		t.Fatalf("my find 应 200: %d %s", status, body)
	}
	_ = body

	// The receiver accepts the direct source first; owner PATCH only applies
	// to active/paused offers.
	if err := f.db.QueryRow(fixtureBind(`SELECT observed_source_revision, revision_version
		FROM resource_authorization_sources WHERE id = ?`), directSourceID).Scan(&observed, &version); err != nil {
		t.Fatal(err)
	}
	status, body = granteeEnv.do(t, http.MethodPost,
		"/__aisys__/api/my-authorization-sources/"+directSourceID+"/accept",
		`{"expectedRevision":"`+observed+`","expectedRevisionVersion":`+strconv.Itoa(version)+`}`)
	if status != http.StatusOK {
		t.Fatalf("direct accept 应 200: %d %s", status, body)
	}

	// PATCH（非 expire）闭包：只允许备注/额度。
	status, body = env.do(t, http.MethodPatch, selfPrefix+"/"+directID, `{"remark":"updated"}`)
	if status != http.StatusOK {
		t.Fatalf("my patch 应 200: %d %s", status, body)
	}

	// PATCH expire 闭包：先设置一个到期时间。
	status, body = env.do(t, http.MethodPatch, selfPrefix+"/"+directID+"/expire",
		`{"expiresAt":"2027-01-01T00:00:00Z"}`)
	if status != http.StatusOK {
		t.Fatalf("my expire 应 200: %d %s", status, body)
	}
	// 延长必须走 /extend。
	status, body = env.do(t, http.MethodPost, selfPrefix+"/"+directID+"/extend",
		`{"expiresAt":"2028-01-01T00:00:00Z"}`)
	if status != http.StatusOK {
		t.Fatalf("my extend 应 200: %d %s", status, body)
	}

	// Per-source return: the receiver returns its own source. Terminal grants
	// are not revocable, so the revoke closure runs on a fresh offer.
	status, body = granteeEnv.do(t, http.MethodPost,
		"/__aisys__/api/my-authorization-sources/"+directSourceID+"/return", `{}`)
	if status != http.StatusOK || !strings.Contains(body, `"status":"returned"`) {
		t.Fatalf("my source return 应 200 returned: %d %s", status, body)
	}
	status, body = env.do(t, http.MethodPost, selfPrefix,
		`{"resourceType":"group","resourceId":"grp3_w13s","granteeType":"system_account","granteeUsername":"grantee"}`)
	if status != http.StatusCreated {
		t.Fatalf("my create revoke-target 应 201: %d %s", status, body)
	}
	revokeID := w13g2DataMap(t, body)["grantId"].(string)
	status, body = env.do(t, http.MethodDelete, selfPrefix+"/"+revokeID, `{}`)
	if status != http.StatusOK || !strings.Contains(body, `"status":"revoked"`) {
		t.Fatalf("my revoke 应 200: %d %s", status, body)
	}

	// account 资源 create：不再接受 targetGroupId；接收方接受后创建实例，但
	// 不自动写入 group_accounts（绑定由接收方自行完成）。
	status, body = env.do(t, http.MethodPost, selfPrefix,
		`{"resourceType":"account","resourceId":"acc_w13s","granteeType":"system_account","granteeUsername":"grantee"}`)
	if status != http.StatusCreated {
		t.Fatalf("my account create 应 201: %d %s", status, body)
	}
	accountSourceID := w13g2DataMap(t, body)["sourceId"].(string)
	if err := f.db.QueryRow(fixtureBind(`SELECT observed_source_revision, revision_version
		FROM resource_authorization_sources WHERE id = ?`), accountSourceID).Scan(&observed, &version); err != nil {
		t.Fatal(err)
	}
	status, body = granteeEnv.do(t, http.MethodPost,
		"/__aisys__/api/my-authorization-sources/"+accountSourceID+"/accept",
		`{"expectedRevision":"`+observed+`","expectedRevisionVersion":`+strconv.Itoa(version)+`}`)
	if status != http.StatusOK {
		t.Fatalf("account accept 应 200: %d %s", status, body)
	}
	var instanceCount int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM accounts WHERE authorization_instance_source_account_id = 'acc_w13s' AND deleted_at IS NULL`).Scan(&instanceCount); err != nil {
		t.Fatal(err)
	}
	if instanceCount != 1 {
		t.Fatalf("account accept 应装填 1 个实例账户: %d", instanceCount)
	}
	var boundGroups int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM group_accounts WHERE group_id = 'tg_w13s'`).Scan(&boundGroups); err != nil {
		t.Fatal(err)
	}
	if boundGroups != 0 {
		t.Fatalf("接受不得自动绑定接收方默认分组: %d", boundGroups)
	}
}

// TestW13g2MountAdminSurfaceChain drives the admin surface through a full
// admin-created offer: owner approval gate, receiver acceptance, then the
// admin patch/expire/extend/pause/resume/revoke governance actions.
func TestW13g2MountAdminSurfaceChain(t *testing.T) {
	env := newUsageRouteEnv(t, "admin")
	f := env.f
	f.seedGroup(t, "grp_w13a", "owner1")

	const adminPrefix = "/__aisys__/api/authorizations"
	status, body := env.do(t, http.MethodPost, adminPrefix+"?systemAccountId=owner1",
		`{"resourceType":"group","resourceId":"grp_w13a","granteeType":"system_account","granteeUsername":"grantee"}`)
	if status != http.StatusCreated {
		t.Fatalf("admin create 应 201: %d %s", status, body)
	}
	createdID := w13g2DataMap(t, body)["grantId"].(string)
	if got := w13g2DataMap(t, body)["status"]; got != StatusPendingOwnerApproval {
		t.Fatalf("admin create status = %v, want pending_owner_approval", got)
	}

	if status, _ = env.do(t, http.MethodGet, adminPrefix, ""); status != http.StatusOK {
		t.Fatalf("admin list 应 200: %d", status)
	}
	if status, body = env.do(t, http.MethodGet, adminPrefix+"/"+createdID, ""); status != http.StatusOK {
		t.Fatalf("admin find 应 200: %d", status)
	}

	// The resource owner confirms the admin-created offer; the receiver then
	// accepts its source.
	ownerEnv := newUsageRouteEnv(t, "owner1", f)
	status, body = ownerEnv.do(t, http.MethodPost,
		"/__aisys__/api/my-authorizations/"+createdID+"/owner-approve", `{}`)
	if status != http.StatusOK {
		t.Fatalf("owner approve 应 200: %d %s", status, body)
	}
	var sourceID, observed string
	var version int
	if err := f.db.QueryRow(fixtureBind(`SELECT id, observed_source_revision, revision_version
		FROM resource_authorization_sources WHERE grant_id = ? AND status = 'pending'`), createdID).
		Scan(&sourceID, &observed, &version); err != nil {
		t.Fatal(err)
	}
	granteeEnv := newUsageRouteEnv(t, "grantee", f)
	status, body = granteeEnv.do(t, http.MethodPost,
		"/__aisys__/api/my-authorization-sources/"+sourceID+"/accept",
		`{"expectedRevision":"`+observed+`","expectedRevisionVersion":`+strconv.Itoa(version)+`}`)
	if status != http.StatusOK || !strings.Contains(body, `"status":"active"`) {
		t.Fatalf("grantee accept 应 active: %d %s", status, body)
	}

	// Remark/expiry PATCH stays owner-only (the admin governance surface only
	// grants pause/resume/extend/revoke).
	status, body = env.do(t, http.MethodPatch, adminPrefix+"/"+createdID, `{"remark":"admin-updated"}`)
	if status != http.StatusBadRequest || !strings.Contains(body, "授权不存在") {
		t.Fatalf("admin patch 应被 owner scope 拒绝: %d %s", status, body)
	}
	status, body = ownerEnv.do(t, http.MethodPatch,
		"/__aisys__/api/my-authorizations/"+createdID, `{"remark":"owner-updated"}`)
	if status != http.StatusOK {
		t.Fatalf("owner patch 应 200: %d %s", status, body)
	}
	status, body = ownerEnv.do(t, http.MethodPatch,
		"/__aisys__/api/my-authorizations/"+createdID+"/expire", `{"expiresAt":"2027-02-01T00:00:00Z"}`)
	if status != http.StatusOK {
		t.Fatalf("owner expire 应 200: %d %s", status, body)
	}
	status, body = env.do(t, http.MethodPost, adminPrefix+"/"+createdID+"/extend",
		`{"expiresAt":"2028-02-01T00:00:00Z"}`)
	if status != http.StatusOK {
		t.Fatalf("admin extend 应 200: %d %s", status, body)
	}
	status, body = env.do(t, http.MethodPost, adminPrefix+"/"+createdID+"/pause", `{}`)
	if status != http.StatusOK || !strings.Contains(body, `"status":"paused"`) {
		t.Fatalf("admin pause 应 200: %d %s", status, body)
	}
	status, body = env.do(t, http.MethodPost, adminPrefix+"/"+createdID+"/resume", `{}`)
	if status != http.StatusOK || !strings.Contains(body, `"status":"active"`) {
		t.Fatalf("admin resume 应 200: %d %s", status, body)
	}
	status, body = env.do(t, http.MethodDelete, adminPrefix+"/"+createdID, `{}`)
	if status != http.StatusOK || !strings.Contains(body, `"status":"revoked"`) {
		t.Fatalf("admin revoke 应 200: %d %s", status, body)
	}
}

// TestW13g2RouteBranchGaps 直接驱动 handler 的剩余分支：accessFor 无身份、
// list 两侧 scope filter、find 读失败 500、create 重新激活日志与回显、
// revoke 乐观锁 409、return 的 401/空白 scope、patch 的领域 Fail 透传。
func TestW13g2RouteBranchGaps(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedGroup(t, "grp_g13", "owner")
	sink := &wdSink{}
	deps := &Deps{Store: f.store, Sink: sink, Auth: &authsys.Deps{}}

	// accessFor：无认证上下文 → 零值（routes.go 142-144）。
	req := httptest.NewRequest(http.MethodGet, "/?systemAccountId=owner", nil)
	if access := deps.accessFor(req, false); access.IsAdmin || access.ViewerID != "" {
		t.Fatalf("无认证 accessFor 应为零值: %#v", access)
	}

	grantID, _ := f.createAcceptedDirectOffer(t, "group", "grp_g13", "grantee")

	// list：admin 面全量视图（忽略 legacy ?systemAccountId）与 self 面
	// filter。
	recorder := httptest.NewRecorder()
	deps.list(recorder, wdGetReq(t, http.MethodGet, "/?systemAccountId=owner", wdAuthCtx("admin", "admin")), false)
	if recorder.Code != http.StatusOK {
		t.Fatalf("admin filter list 应 200: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	deps.list(recorder, wdGetReq(t, http.MethodGet, "/?systemAccountId=owner", wdAuthCtx("admin", "admin")), true)
	if recorder.Code != http.StatusOK {
		t.Fatalf("self filter list 应 200: %d %s", recorder.Code, recorder.Body.String())
	}

	// find：存储读失败 → 500（327-329）。
	recorder = httptest.NewRecorder()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	deps.find(recorder, wdGetReq(t, http.MethodGet, "/", nil).WithContext(canceled), false)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("find 存储失败应 500: %d", recorder.Code)
	}

	// revoke：recipient-consent model drops the optimistic version from the
	// body; the owner revokes the accepted grant directly.
	recorder = httptest.NewRecorder()
	revokeRequest := wdJSONRequest(t, http.MethodDelete, "/", `{"expectedUpdatedAt":"2020-01-01T00:00:00Z"}`, wdAuthCtx("owner", "user"))
	revokeRequest.SetPathValue("id", grantID)
	deps.revokeScoped(recorder, revokeRequest, false)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"status":"revoked"`) {
		t.Fatalf("revoke 应 200 revoked: %d %s", recorder.Code, recorder.Body.String())
	}

	// create after revoke opens a fresh pending offer (no previousStatus
	// resurrection), logged as offer creation.
	recorder = httptest.NewRecorder()
	deps.create(recorder, wdJSONRequest(t, http.MethodPost, "/__aisys__/api/authorizations?systemAccountId=owner",
		wdCreateBody("group", "grp_g13", "system_account", "grantee", ""), wdAuthCtx("admin", "admin")))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("revoke 后二次 create 应 201 新 offer: %d %s", recorder.Code, recorder.Body.String())
	}
	secondGrantID := w13g2DataMap(t, recorder.Body.String())["grantId"].(string)
	secondSourceID := w13g2DataMap(t, recorder.Body.String())["sourceId"].(string)
	last := sink.entries[len(sink.entries)-1]
	if last.Summary == "" || !strings.Contains(last.Summary, "发起资源授权") {
		t.Fatalf("create 日志摘要错误: %#v", last)
	}

	// returnValue：整单归还面已退役，无论认证/scope 均返回 409。
	for _, request := range []*http.Request{
		wdJSONRequest(t, http.MethodDelete, "/", `{}`, nil),
		wdJSONRequest(t, http.MethodDelete, "/?systemAccountId=", `{}`, wdAuthCtx("owner", "user")),
	} {
		recorder = httptest.NewRecorder()
		deps.returnValue(recorder, request, true)
		if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "按来源归还") {
			t.Fatalf("retired return 应 409: %d %s", recorder.Code, recorder.Body.String())
		}
	}

	// patchScoped：先让接收方接受新 offer，再验证 domain Fail 原文透传
	//（延长必须走 /extend，缩短/备注走 PATCH）。
	var observed string
	var version int
	// The admin-created offer needs the resource owner's confirmation before
	// the receiver can see/accept it.
	if err := f.store.OwnerApprove(context.Background(), "owner", secondGrantID, f.now.UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(fixtureBind(`SELECT observed_source_revision, revision_version
		FROM resource_authorization_sources WHERE id = ?`), secondSourceID).Scan(&observed, &version); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.AcceptSource(context.Background(), "grantee", secondSourceID, AcceptInput{
		ExpectedRevision: observed, ExpectedRevisionVersion: version,
	}, f.now.UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	expireRec := httptest.NewRecorder()
	deps.patchScoped(expireRec, wdJSONRequestWdID(t, secondGrantID,
		`{"expiresAt":"2030-01-01T00:00:00Z"}`, wdAuthCtx("owner", "user")), true, false)
	if expireRec.Code != http.StatusOK {
		t.Fatalf("patch 到期应 200: %d %s", expireRec.Code, expireRec.Body.String())
	}
	patchRec := httptest.NewRecorder()
	deps.patchScoped(patchRec, wdJSONRequestWdID(t, secondGrantID,
		`{"expiresAt":"2031-01-01T00:00:00Z"}`, wdAuthCtx("owner", "user")), false, false)
	if patchRec.Code != http.StatusBadRequest || !strings.Contains(patchRec.Body.String(), "延长到期时间请使用延长接口") {
		t.Fatalf("patch 延长应 400 原文: %d %s", patchRec.Code, patchRec.Body.String())
	}

	// errorsAsConflict 命中分支（818-821）。
	var conflict *Conflict
	if !errorsAsConflict(&Conflict{CurrentUpdatedAt: "x"}, &conflict) || conflict == nil {
		t.Fatalf("errorsAsConflict 应命中")
	}
}

// TestW13g2ReadJSONBodyTransport 锁 readJSONBody / decodeStrictJSON 的
// 传输分支（validation.go 57-122）。
func TestW13g2ReadJSONBodyTransport(t *testing.T) {
	build := func(body string, contentType string, contentLength string) (*httptest.ResponseRecorder, *http.Request) {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		if contentType != "" {
			request.Header.Set("Content-Type", contentType)
		}
		if contentLength != "" {
			request.Header.Set("Content-Length", contentLength)
		}
		return recorder, request
	}

	// 非 JSON Content-Type → (nil, true)。
	recorder, request := build("x", "text/plain", "1")
	if data, ok := readJSONBody(recorder, request); !ok || data != nil {
		t.Fatalf("非 JSON 应放行: %v %v", data, ok)
	}
	// JSON 但无 Content-Length → (nil, true)。
	recorder, request = build("x", "application/json", "")
	if data, ok := readJSONBody(recorder, request); !ok || data != nil {
		t.Fatalf("无 CL 应放行: %v %v", data, ok)
	}
	// JSON + CL + 空白体 → (nil, true)（74 行）。
	recorder, request = build("   ", "application/json", "3")
	if data, ok := readJSONBody(recorder, request); !ok || data != nil {
		t.Fatalf("空白体应放行: %v %v", data, ok)
	}
	// MaxBytesReader 超限 → 413（65-67）。
	recorder, request = build(`{"a":1}`, "application/json", "7")
	request.Body = http.MaxBytesReader(recorder, io.NopCloser(strings.NewReader(`{"a":1}`)), 3)
	if data, ok := readJSONBody(recorder, request); ok || data != nil || recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("超限应 413: %v %v %d", data, ok, recorder.Code)
	}
	// 读失败（非超限）→ 400（68-70）。
	recorder, request = build(`{"a":1}`, "application/json", "7")
	request.Body = io.NopCloser(&w13g2ErrReader{})
	if data, ok := readJSONBody(recorder, request); ok || data != nil || recorder.Code != http.StatusBadRequest {
		t.Fatalf("读失败应 400: %v %v %d", data, ok, recorder.Code)
	}

	// decodeStrictJSON：空体放行（88-90），字段缺失交由调用方校验。
	deps := &Deps{}
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Content-Length", "0")
	var body struct {
		ResourceType *string `json:"resourceType"`
	}
	if !decodeStrictJSON(recorder, request, &body, map[string]bool{"resourceType": true}) || body.ResourceType != nil {
		t.Fatalf("空体应放行: %d %s", recorder.Code, recorder.Body.String())
	}
	// 未知键拒绝后的目标反序列化失败：合法键但类型不匹配 → 请求体无效
	//（117-121）。
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"resourceType":123}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Content-Length", "20")
	if decodeStrictJSON(recorder, request, &body, map[string]bool{"resourceType": true}) {
		t.Fatalf("类型不匹配应拒绝")
	}
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("目标反序列化失败应 400: %d", recorder.Code)
	}
	_ = deps

	// 路由级确认：create 空体 → 放行后落到"授权参数不合法"；类型不匹配
	// →"请求体无效"。
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedGroup(t, "grp_v13", "owner")
	routeDeps := &Deps{Store: f.store, Auth: &authsys.Deps{}}
	recorder = httptest.NewRecorder()
	emptyRequest := httptest.NewRequest(http.MethodPost, "/__aisys__/api/my-authorizations", strings.NewReader(""))
	emptyRequest.Header.Set("Content-Type", "application/json")
	emptyRequest.Header.Set("Content-Length", "0")
	routeDeps.create(recorder, emptyRequest.WithContext(authsys.WithAuthContext(emptyRequest.Context(), wdAuthCtx("owner", "user"))))
	if recorder.Code != http.StatusBadRequest || decodeWd(recorder) != "授权参数不合法" {
		t.Fatalf("create 空体应 400 授权参数不合法: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	badType := httptest.NewRequest(http.MethodPost, "/__aisys__/api/my-authorizations", strings.NewReader(`{"expiresAt":123}`))
	badType.Header.Set("Content-Type", "application/json")
	badType.Header.Set("Content-Length", "17")
	routeDeps.create(recorder, badType.WithContext(authsys.WithAuthContext(badType.Context(), wdAuthCtx("owner", "user"))))
	if recorder.Code != http.StatusBadRequest || decodeWd(recorder) != "请求体无效" {
		t.Fatalf("create 类型不匹配应 400 请求体无效: %d %s", recorder.Code, recorder.Body.String())
	}
}

type w13g2ErrReader struct{}

func (*w13g2ErrReader) Read([]byte) (int, error) { return 0, context.DeadlineExceeded }

// TestW13g2PureHelperGaps 直接驱动纯函数缺口分支。
func TestW13g2PureHelperGaps(t *testing.T) {
	now := time.Unix(1_760_000_000, 0).UTC()

	// NextVersion：当前版本不可解析 → 以 now 兜底（store.go 147-149）。
	if NextVersion("not-a-time", now) != now.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("NextVersion 兜底错误")
	}
	// summaryAggregate nil → 零值（usage_reads.go 209-211）。
	if summaryAggregate(nil) != (UsageAggregateSummary{}) {
		t.Fatalf("summaryAggregate(nil) 应为零值")
	}
	// addCalendarDays 非法输入原样返回（usage_reads.go 676-678）。
	if addCalendarDays("bad", 1) != "bad" {
		t.Fatalf("addCalendarDays 非法应原样返回")
	}
	// calendarDaysBetweenInclusive：解析失败 → 1；倒挂 → 1。
	if calendarDaysBetweenInclusive("bad", "2026-01-01") != 1 || calendarDaysBetweenInclusive("2026-01-02", "2026-01-01") != 1 {
		t.Fatalf("calendarDaysBetweenInclusive 兜底错误")
	}
	// dateKeyAt：非法时区 → UTC 兜底（usage_reads.go 665-668）。
	if dateKeyAt(now, "Bogus/Zone") != now.UTC().Format("2006-01-02") {
		t.Fatalf("dateKeyAt 非法时区应回退 UTC")
	}
	// parseAuthorizationRFC3339Instant：模式通过但 time.Parse 拒绝
	//（expiry.go 45-48，月份 13）。
	if _, valid := parseAuthorizationRFC3339Instant("2026-13-01T00:00:00Z"); valid {
		t.Fatalf("月份 13 应拒绝")
	}
	// usageTimestampMilliseconds：不可解析 → 0（usage_detail.go 103-108）。
	if usageTimestampMilliseconds("nope") != 0 {
		t.Fatalf("usageTimestampMilliseconds 非法应为 0")
	}
	// usageScopeKey：无身份非管理员 → 无 key（usage_reads.go 164-167）。
	if key, ok := usageScopeKey(accessInfo{}); ok || key != "" {
		t.Fatalf("空 access 应无 scope key: %q %v", key, ok)
	}

	// 统计缓存：set 更新既有键（stats_loader.go 91-96）+ get 过期淘汰
	//（stats_loader.go 76-80）。
	tick := now
	cache := newAuthorizationStatsCache(func() time.Time { return tick })
	cache.set("a:b", ResourceAuthorizationStats{AuthorizationCount: 1})
	cache.set("a:b", ResourceAuthorizationStats{AuthorizationCount: 2})
	if stats, ok := cache.get("a:b"); !ok || stats.AuthorizationCount != 2 {
		t.Fatalf("set 更新分支错误: %#v %v", stats, ok)
	}
	tick = tick.Add(2 * authorizationStatsCacheTTL)
	if _, ok := cache.get("a:b"); ok {
		t.Fatalf("过期条目应被淘汰")
	}

	// chunkValues 边界（size 0 → 1）。
	if chunks := chunkValues([]string{"a", "b"}, 0); len(chunks) != 2 {
		t.Fatalf("chunkValues size 0 应逐项分块: %#v", chunks)
	}
}

// TestW13g2UsageStoreEmptyScopes 驱动 usage 读族的无 scope 空结果分支
// （usage_reads.go 336-338 / 368-371 / 473-476）。
func TestW13g2UsageStoreEmptyScopes(t *testing.T) {
	f := newUsageFixture(t)
	rng := UsageStatsRange{StartDate: "2026-08-08", EndDate: "2026-09-06", Days: 30, MaxDays: 31}
	empty := accessInfo{}

	teamRows, err := f.store.teamUsageRows(context.Background(), UsageFilters{}, empty, rng, 1, 20)
	if err != nil || len(teamRows.Rows) != 0 || teamRows.Total != 0 {
		t.Fatalf("teamUsageRows 空 scope 应为空: %#v %v", teamRows, err)
	}
	teamSummary, err := f.store.teamUsageSummary(context.Background(), UsageFilters{}, empty, rng)
	if err != nil || teamSummary.Summary != (UsageAggregateSummary{}) {
		t.Fatalf("teamUsageSummary 空 scope 应为零摘要: %#v %v", teamSummary, err)
	}
	userRows, err := f.store.userUsageRows(context.Background(), UsageFilters{}, empty, rng, 1, 20)
	if err != nil || len(userRows.Rows) != 0 {
		t.Fatalf("userUsageRows 空 scope 应为空: %#v %v", userRows, err)
	}
	userSummary, err := f.store.userUsageSummary(context.Background(), UsageFilters{}, empty, rng)
	if err != nil || userSummary.Summary != (UsageAggregateSummary{}) {
		t.Fatalf("userUsageSummary 空 scope 应为零摘要: %#v %v", userSummary, err)
	}

	// statsQueryDB：注入 stats 句柄时优先返回注入句柄（usage_reads.go
	// 145-148）。newUsageFixture 已 AttachStatsDatabase(f.db)。
	if f.store.statsQueryDB() != f.db {
		t.Fatalf("statsQueryDB 应返回注入句柄")
	}
	_ = kernel.WriteOK
}

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

// TestW13g2MountSelfSurfaceChain 驱动 my-* 面全部注册闭包（routes.go
// 153-189）+ create 的 MutationGuard Scope/Fingerprint/create 闭包
//（177-181）+ account 资源 create 的 target-group 装载分支（428-430）。
func TestW13g2MountSelfSurfaceChain(t *testing.T) {
	env := newUsageRouteEnv(t, "owner1")
	f := env.f
	f.seedGroup(t, "grp_w13s", "owner1")
	f.seedGroup(t, "grp2_w13s", "owner1")
	f.seedTeamWithMember(t, "team_w13s", "grantee")
	seedResourceAccount(t, f, "acc_w13s", "owner1", "源账户")
	// grantee 的默认分组：provider_code 与源账户一致（bind 的供应商校验），
	// is_default=1。
	if _, err := f.db.Exec(`INSERT INTO groups (id, name, system_account_id, status, provider_code, enabled, is_default, updated_at)
		VALUES ('tg_w13s', '默认分组', 'grantee', 'active', 'gpt', 1, 1, '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE accounts SET provider_code = 'gpt' WHERE id = 'acc_w13s'`); err != nil {
		t.Fatal(err)
	}

	const selfPrefix = "/__aisys__/api/my-authorizations"
	// POST my-authorizations：MutationGuard Scope/Fingerprint/create 闭包
	//（team 授权）。团队来源会把同资源 runtime 的 manual source 置为
	// superseded，因此 direct 生命周期用独立资源 grp2_w13s 隔离。
	status, body := env.do(t, http.MethodPost, selfPrefix,
		`{"resourceType":"group","resourceId":"grp_w13s","granteeType":"team","granteeId":"team_w13s"}`)
	if status != http.StatusCreated {
		t.Fatalf("my create team 应 201: %d %s", status, body)
	}
	createdID := w13g2DataMap(t, body)["item"].(map[string]any)["id"].(string)

	// direct grant：return 只支持个人授权，后续 patch/expire/return/revoke
	// 用这条 direct grant 走完整生命周期。
	status, body = env.do(t, http.MethodPost, selfPrefix,
		`{"resourceType":"group","resourceId":"grp2_w13s","granteeType":"system_account","granteeId":"grantee"}`)
	if status != http.StatusCreated {
		t.Fatalf("my create direct 应 201: %d %s", status, body)
	}
	directID := w13g2DataMap(t, body)["item"].(map[string]any)["id"].(string)
	directUpdatedAt := w13g2UpdateAt(t, body)

	// GET 列表与详情闭包。
	if status, body = env.do(t, http.MethodGet, selfPrefix, ""); status != http.StatusOK {
		t.Fatalf("my list 应 200: %d %s", status, body)
	}
	if status, body = env.do(t, http.MethodGet, selfPrefix+"/"+createdID, ""); status != http.StatusOK {
		t.Fatalf("my find 应 200: %d %s", status, body)
	}
	_ = body

	// PATCH（非 expire）闭包。
	status, body = env.do(t, http.MethodPatch, selfPrefix+"/"+directID,
		`{"expectedUpdatedAt":"`+directUpdatedAt+`","status":"paused"}`)
	if status != http.StatusOK {
		t.Fatalf("my patch 应 200: %d %s", status, body)
	}
	pausedAt := w13g2UpdateAt(t, body)

	// PATCH expire 闭包。
	status, body = env.do(t, http.MethodPatch, selfPrefix+"/"+directID+"/expire",
		`{"expectedUpdatedAt":"`+pausedAt+`","expiresAt":"2027-01-01T00:00:00Z"}`)
	if status != http.StatusOK {
		t.Fatalf("my expire 应 200: %d %s", status, body)
	}

	// DELETE return 闭包：my-* 面 viewer 钉到调用者，归还必须由被授权人
	//（grantee）发起——owner 视角归还他人授权会 404。
	granteeEnv := newUsageRouteEnv(t, "grantee")
	status, _ = granteeEnv.do(t, http.MethodDelete, selfPrefix+"/"+directID+"/return",
		`{"expectedUpdatedAt":"`+w13g2UpdateAt(t, body)+`"}`)
	if status != http.StatusNoContent {
		t.Fatalf("my return 应 204: %d", status)
	}

	// DELETE revoke 闭包（returned → revoked）。
	grant, err := f.store.GetGrantForMutation(context.Background(), nil, directID)
	if err != nil || grant == nil {
		t.Fatalf("read grant: %v", err)
	}
	status, _ = env.do(t, http.MethodDelete, selfPrefix+"/"+directID,
		`{"expectedUpdatedAt":"`+grant.UpdatedAt+`"}`)
	if status != http.StatusOK {
		t.Fatalf("my revoke 应 200: %d", status)
	}

	// account 资源 create：走 targetGroupID 输入（routes.go 428-430）与
	// provision insert arm（instance + search terms + 绑定默认分组）。
	status, body = env.do(t, http.MethodPost, selfPrefix,
		`{"resourceType":"account","resourceId":"acc_w13s","granteeType":"system_account","granteeId":"grantee","targetGroupId":"tg_w13s"}`)
	if status != http.StatusCreated && status != http.StatusOK {
		t.Fatalf("my account create 应成功: %d %s", status, body)
	}
	var instanceCount int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM accounts WHERE authorization_instance_source_account_id = 'acc_w13s' AND deleted_at IS NULL`).Scan(&instanceCount); err != nil {
		t.Fatal(err)
	}
	if instanceCount != 1 {
		t.Fatalf("account create 应装填 1 个实例账户: %d", instanceCount)
	}
	var boundGroups int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM group_accounts WHERE group_id = 'tg_w13s'`).Scan(&boundGroups); err != nil {
		t.Fatal(err)
	}
	if boundGroups != 1 {
		t.Fatalf("实例账户应绑定 grantee 默认分组: %d", boundGroups)
	}
}

// TestW13g2MountAdminSurfaceChain 驱动管理面注册闭包（routes.go
// 191-223）：GET 列表/详情、POST、DELETE、PATCH、PATCH expire。
func TestW13g2MountAdminSurfaceChain(t *testing.T) {
	env := newUsageRouteEnv(t, "admin")
	f := env.f
	f.seedGroup(t, "grp_w13a", "owner1")
	f.seedTeamWithMember(t, "team_w13a", "grantee")

	const adminPrefix = "/__aisys__/api/authorizations"
	status, body := env.do(t, http.MethodPost, adminPrefix+"?systemAccountId=owner1",
		`{"resourceType":"group","resourceId":"grp_w13a","granteeType":"team","granteeId":"team_w13a"}`)
	if status != http.StatusCreated {
		t.Fatalf("admin create 应 201: %d %s", status, body)
	}
	createdID := w13g2DataMap(t, body)["item"].(map[string]any)["id"].(string)

	if status, _ = env.do(t, http.MethodGet, adminPrefix, ""); status != http.StatusOK {
		t.Fatalf("admin list 应 200: %d", status)
	}
	if status, body = env.do(t, http.MethodGet, adminPrefix+"/"+createdID, ""); status != http.StatusOK {
		t.Fatalf("admin find 应 200: %d", status)
	}
	updatedAt := w13g2UpdateAt(t, body)

	// DELETE / PATCH / PATCH expire 闭包（每步版本取自上一响应）。
	status, _ = env.do(t, http.MethodDelete, adminPrefix+"/"+createdID, `{"expectedUpdatedAt":"`+updatedAt+`"}`)
	if status != http.StatusOK {
		t.Fatalf("admin revoke 应 200: %d", status)
	}
	grant, err := f.store.GetGrantForMutation(context.Background(), nil, createdID)
	if err != nil || grant == nil {
		t.Fatalf("read grant: %v", err)
	}
	status, body = env.do(t, http.MethodPatch, adminPrefix+"/"+createdID,
		`{"expectedUpdatedAt":"`+grant.UpdatedAt+`","status":"paused"}`)
	if status != http.StatusOK {
		t.Fatalf("admin patch 应 200: %d %s", status, body)
	}
	grant, err = f.store.GetGrantForMutation(context.Background(), nil, createdID)
	if err != nil {
		t.Fatal(err)
	}
	status, _ = env.do(t, http.MethodPatch, adminPrefix+"/"+createdID+"/expire",
		`{"expectedUpdatedAt":"`+grant.UpdatedAt+`","expiresAt":"2027-02-01T00:00:00Z"}`)
	if status != http.StatusOK {
		t.Fatalf("admin expire 应 200: %d", status)
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

	created, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_g13",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}

	// list：admin 面 ?systemAccountId 过滤（304-307）与 self 面 filter
	//（301-303）。
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

	// create 重新激活：revoke 后再次 create → 200 + previousStatus 回显
	//（457-459、492-494）+ 日志摘要"重新激活资源授权："。
	if _, err := f.store.Revoke(context.Background(), created.Item.ID, created.Item.UpdatedAt, "owner"); err != nil {
		t.Fatal(err)
	}
	recorder = httptest.NewRecorder()
	deps.create(recorder, wdJSONRequest(t, http.MethodPost, "/__aisys__/api/authorizations?systemAccountId=owner",
		wdCreateBody("group", "grp_g13", "system_account", "grantee", ""), wdAuthCtx("admin", "admin")))
	if recorder.Code != http.StatusOK {
		t.Fatalf("revive create 应 200: %d %s", recorder.Code, recorder.Body.String())
	}
	if previous, has := w13g2DataMap(t, recorder.Body.String())["previousStatus"]; !has || previous != StatusRevoked {
		t.Fatalf("revive create 应回显 previousStatus=revoked: %s", recorder.Body.String())
	}
	reviveUpdatedAt := w13g2UpdateAt(t, recorder.Body.String())
	last := sink.entries[len(sink.entries)-1]
	if last.Summary == "" || !strings.Contains(last.Summary, "重新激活资源授权") {
		t.Fatalf("revive 日志摘要错误: %#v", last)
	}

	// revoke：旧版本 → 乐观锁 409（554-558）。
	recorder = httptest.NewRecorder()
	revokeRequest := wdJSONRequest(t, http.MethodDelete, "/", `{"expectedUpdatedAt":"2020-01-01T00:00:00Z"}`, wdAuthCtx("owner", "user"))
	revokeRequest.SetPathValue("id", created.Item.ID)
	deps.revokeScoped(recorder, revokeRequest, false)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("旧版本 revoke 应 409: %d %s", recorder.Code, recorder.Body.String())
	}

	// returnValue：无认证 401（590-593）与空白 scope 400（596-598）。
	recorder = httptest.NewRecorder()
	deps.returnValue(recorder, wdJSONRequest(t, http.MethodDelete, "/", `{}`, nil), true)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("return 无认证应 401: %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	deps.returnValue(recorder, wdJSONRequest(t, http.MethodDelete, "/?systemAccountId=", `{}`, wdAuthCtx("owner", "user")), true)
	if recorder.Code != http.StatusBadRequest || decodeWd(recorder) != "系统账号 ID 不能为空" {
		t.Fatalf("return 空白 scope 应 400: %d %s", recorder.Code, recorder.Body.String())
	}

	// patchScoped：领域 Fail 原文透传（749-752）——expired 授权恢复 active
	// 必须同时调整过期时间。先把授权 patch 成 expired（过去时间），再仅恢复
	// active。
	expireRec := httptest.NewRecorder()
	deps.patchScoped(expireRec, wdJSONRequestWdID(t, created.Item.ID,
		`{"expectedUpdatedAt":"`+reviveUpdatedAt+`","expiresAt":"2020-01-01T00:00:00Z"}`, wdAuthCtx("owner", "user")), true, false)
	if expireRec.Code != http.StatusOK {
		t.Fatalf("patch 到期应 200: %d %s", expireRec.Code, expireRec.Body.String())
	}
	expiredAt := w13g2UpdateAt(t, expireRec.Body.String())
	patchRec := httptest.NewRecorder()
	deps.patchScoped(patchRec, wdJSONRequestWdID(t, created.Item.ID,
		`{"expectedUpdatedAt":"`+expiredAt+`","status":"active"}`, wdAuthCtx("owner", "user")), false, false)
	if patchRec.Code != http.StatusBadRequest || decodeWd(patchRec) != "到期授权恢复时请同时调整过期时间" {
		t.Fatalf("expired 恢复应 400 Fail 原文: %d %s", patchRec.Code, patchRec.Body.String())
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
//（usage_reads.go 336-338 / 368-371 / 473-476）。
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

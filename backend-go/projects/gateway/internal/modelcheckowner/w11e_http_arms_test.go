package modelcheckowner

// w11e http.go 错误臂：接线缺失、scope 解析、JSON 解码、质量管理路由、
// run 详情形状、SSE 降级与前端进度事件适配的剩余分支。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
)

func TestW11EAuthorizeAdaptersRejectNilDependencies(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/run/active", nil)
	if _, err := NewAdminAuthorize(nil)(context.Background(), request); err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("nil authenticator 必须拒绝: %v", err)
	}
	if _, err := NewSelfAuthorize(nil)(context.Background(), request); err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("nil self authenticator 必须拒绝: %v", err)
	}
	var nilAuth *modelcheckauth.Authenticator
	if _, err := NewAdminAuthorize(nilAuth)(context.Background(), request); err == nil {
		t.Fatal("typed-nil authenticator 必须拒绝")
	}
}

func TestW11EHandlerRejectsIncompleteWiring(t *testing.T) {
	handler := newTestHTTPHandler()
	handler.Service = nil
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/run/active", nil))
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "owner 接线") {
		t.Fatalf("接线缺失必须 503: %d %s", response.Code, response.Body.String())
	}
}

func TestW11EResolveManagementScopeArms(t *testing.T) {
	request := func(query string) *http.Request {
		return httptest.NewRequest(http.MethodGet, "/runs?"+query, nil)
	}
	if _, err := resolveManagementScope(request(""), "", false, false); err == nil || !strings.Contains(err.Error(), "缺少认证") {
		t.Fatalf("空 actor 必须拒绝: %v", err)
	}
	if _, err := resolveManagementScope(request("systemAccountId=a&systemAccountId=b"), "sys-1", false, false); err == nil || !strings.Contains(err.Error(), "作用域参数无效") {
		t.Fatalf("重复参数必须拒绝: %v", err)
	}
	if scope, err := resolveManagementScope(request("systemAccountId=%20"), "sys-1", false, false); err != nil || scope.SelectedSystemAccountID != "sys-1" {
		t.Fatalf("空白参数必须回落自身: %+v err=%v", scope, err)
	}
	scope, err := resolveManagementScope(request("systemAccountId=all"), "sys-1", false, false)
	if err != nil || scope.SelectedSystemAccountID != "sys-1" || scope.AllSystemAccounts {
		t.Fatalf("非管理员 all 必须回落自身: %+v err=%v", scope, err)
	}
	if _, err := resolveManagementScope(request("systemAccountId=sys-2"), "sys-1", false, false); err == nil || !strings.Contains(err.Error(), "跨 systemAccountId") {
		t.Fatalf("跨租户必须拒绝: %v", err)
	}
	scope, err = resolveManagementScope(request("systemAccountId=sys-2"), "sys-1", true, false)
	if err != nil || scope.SelectedSystemAccountID != "sys-2" {
		t.Fatalf("管理员选择租户必须接受: %+v err=%v", scope, err)
	}
	scope, err = resolveManagementScope(request("systemAccountId=all"), "sys-1", true, false)
	if err != nil || !scope.AllSystemAccounts {
		t.Fatalf("管理员 all 必须接受: %+v err=%v", scope, err)
	}
}

func TestW11EAccountOptionsRouteAndQueryArms(t *testing.T) {
	handler := newTestHTTPHandler()
	handler.AccountOptions = nil
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/account-options?purpose=run", nil))
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "账户选项 owner") {
		t.Fatalf("账户选项 owner 缺失必须 503: %d", response.Code)
	}
	options := handler.AccountOptions
	handler.AccountOptions = w11eAccountOptionsStub{}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/account-options?purpose=run", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("合法请求必须 200: %d %s", response.Code, response.Body.String())
	}
	_ = options
	for _, query := range []string{
		"purpose=unknown",
		"purpose=run&keyword=" + strings.Repeat("k", 101),
		"purpose=run&accountId=a,b",
		"purpose=run&accountId=" + strings.Repeat("a", 121),
		"purpose=run&limit=0",
		"purpose=run&limit=51",
		"purpose=run&limit=x",
		"purpose=run&selectedIds=a&selectedIds[]=b",
		"purpose=run&selectedIds=%20",
		"purpose=run&selectedIds=" + strings.Repeat("a", 121),
		"purpose=run&selectedIds=a,b",
		"purpose=run&accountId=a&keyword=k",
		"purpose=run&accountId=a&selectedIds=b",
		"purpose=run&accountId=a&limit=2",
	} {
		if _, err := parseAccountOptionsQuery(httptest.NewRequest(http.MethodGet, "/account-options?"+query, nil)); err == nil {
			t.Fatalf("查询 %q 必须被拒绝", query)
		}
	}
	query, err := parseAccountOptionsQuery(httptest.NewRequest(http.MethodGet, "/account-options?purpose=run&selectedIds=a&selectedIds=b&keyword=k&limit=3", nil))
	if err != nil || query.Purpose != "run" || len(query.SelectedID) != 2 || query.Keyword != "k" || query.Limit != 3 {
		t.Fatalf("合法查询解析=%+v err=%v", query, err)
	}
}

type w11eAccountOptionsStub struct{}

func (w11eAccountOptionsStub) ListAccountOptions(context.Context, AccountOptionsQuery) ([]AccountOption, error) {
	return nil, nil
}
func (w11eAccountOptionsStub) ModelCheckOptions() ModelCheckOptions { return ModelCheckOptions{} }

func TestW11EScopedQualityRoutesRequireSpecificSystemAccount(t *testing.T) {
	handler := newTestHTTPHandler()
	handler.AllowCrossAccount = true
	// 管理员全局 scope：无 systemAccountId 参数。
	for _, tc := range []struct {
		method, path string
	}{
		{http.MethodGet, "/quality-policy"},
		{http.MethodPatch, "/quality-policy"},
		{http.MethodGet, "/quality-schedules"},
		{http.MethodPost, "/quality-schedules"},
		{http.MethodPatch, "/quality-schedules/sch-1"},
		{http.MethodDelete, "/quality-schedules/sch-1"},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{}`)))
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "请先选择具体系统账户") {
			t.Fatalf("%s %s 必须要求具体系统账户: %d %s", tc.method, tc.path, response.Code, response.Body.String())
		}
	}
	// 空 id 的 PATCH/DELETE 返回 404。
	scoped := newTestHTTPHandler()
	scoped.Authorize = func(context.Context, *http.Request) (string, error) { return "sys-1", nil }
	response := httptest.NewRecorder()
	scoped.ServeHTTP(response, httptest.NewRequest(http.MethodPatch, "/quality-schedules/%20", strings.NewReader(`{}`)))
	if response.Code != http.StatusNotFound {
		t.Fatalf("空 schedule id 必须 404: %d", response.Code)
	}
	response = httptest.NewRecorder()
	scoped.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/quality-schedules/", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("空 schedule id DELETE 必须 404: %d", response.Code)
	}
}

func TestW11EDecodeOwnerJSONArms(t *testing.T) {
	request := &http.Request{Method: http.MethodPost}
	if err := decodeOwnerJSON(request, 1024, &struct{}{}); err == nil || !strings.Contains(err.Error(), "请求体不能为空") {
		t.Fatalf("nil body 必须拒绝: %v", err)
	}
	request = httptest.NewRequest(http.MethodPost, "/quality-policy", strings.NewReader(`{} {}`))
	if err := decodeOwnerJSON(request, 1024, &struct{}{}); err == nil || !strings.Contains(err.Error(), "单个 JSON 对象") {
		t.Fatalf("尾随对象必须拒绝: %v", err)
	}
	request = httptest.NewRequest(http.MethodPost, "/quality-policy", strings.NewReader(`{invalid`))
	if err := decodeOwnerJSON(request, 1024, &struct{}{}); err == nil || !strings.Contains(err.Error(), "请求体无效") {
		t.Fatalf("非法 JSON 必须拒绝: %v", err)
	}
}

func TestW11EWriteQualityErrorArms(t *testing.T) {
	cases := []struct {
		err    error
		status int
	}{
		{errors.New("策略已被其他操作修改"), http.StatusConflict},
		{errors.New("计划已变化"), http.StatusConflict},
		{errors.New("计划不存在"), http.StatusNotFound},
		{errors.New("参数无效"), http.StatusBadRequest},
		{nil, http.StatusBadRequest},
	}
	for _, tc := range cases {
		response := httptest.NewRecorder()
		writeQualityError(response, tc.err)
		if response.Code != tc.status {
			t.Fatalf("err=%v status=%d want=%d", tc.err, response.Code, tc.status)
		}
	}
}

func TestW11EActivateBaselineDefaultErrorArm(t *testing.T) {
	handler := newTestHTTPHandler()
	handler.Baseline = &fakeBaselineActivator{err: errors.New("w11e-unexpected")}
	handler.Authorize = func(context.Context, *http.Request) (string, error) { return "sys-1", nil }
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/token-intercept-baselines/activate", strings.NewReader(`{"cohortKeyHmac":"hmac-sha256-v1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","requestedModel":"gpt-5.6","tokenizerVersion":"o200k_base@1","probeSetVersion":"probe-v1","baselineVersion":2,"strongThresholdIntercept":128,"calibrationNote":"calibrated"}`)))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("未知激活错误必须 503: %d %s", response.Code, response.Body.String())
	}
	// self 挂载禁止激活。
	self := newTestHTTPHandler()
	self.ForceActorScope = true
	response = httptest.NewRecorder()
	self.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/token-intercept-baselines/activate", strings.NewReader(`{"cohortKeyHmac":"hmac-sha256-v1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","requestedModel":"gpt-5.6","tokenizerVersion":"o200k_base@1","probeSetVersion":"probe-v1","baselineVersion":2,"strongThresholdIntercept":128,"calibrationNote":"calibrated"}`)))
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "仅限管理员") {
		t.Fatalf("self 挂载必须 403: %d", response.Code)
	}
}

func TestW11EDecodeRunCommandArms(t *testing.T) {
	if _, err := decodeRunCommand(&http.Request{Method: http.MethodPost}, 1024); err == nil || !strings.Contains(err.Error(), "请求体不能为空") {
		t.Fatalf("nil body 必须拒绝: %v", err)
	}
	body := func(payload string) *http.Request {
		return httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(payload))
	}
	for name, payload := range map[string]string{
		"missing fields":  `{}`,
		"bad profile":     `{"targetType":"account","targetId":"a","model":"m","profile":"medium"}`,
		"comparison id":   `{"targetType":"account","targetId":"a","model":"m","trustedComparisonAccountId":"b"}`,
		"comparison flag": `{"targetType":"account","targetId":"a","model":"m","trustedComparison":true}`,
		"invalid json":    `{invalid`,
		"trailing":        `{} {}`,
	} {
		if _, err := decodeRunCommand(body(payload), 1024); err == nil {
			t.Fatalf("%s 必须拒绝", name)
		}
	}
}

func TestW11EValidateBuiltRequestArms(t *testing.T) {
	scope := ManagementScope{ActorSystemAccountID: "sys-1", SelectedSystemAccountID: "sys-1"}
	command := RunCommand{TargetType: "account", TargetID: "acct-1", Model: "m"}
	base := RunRequest{SystemAccountID: "sys-1", ActorSystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-1", Model: "m", Profile: "quick"}
	if err := validateBuiltRequest(scope, command, base); err != nil {
		t.Fatalf("基线必须通过: %v", err)
	}
	for name, request := range map[string]RunRequest{
		"scope invalid":      {SystemAccountID: "", ActorSystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-1", Model: "m", Profile: "quick"},
		"system mismatch":    {SystemAccountID: "sys-2", ActorSystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-1", Model: "m", Profile: "quick"},
		"actor mismatch":     {SystemAccountID: "sys-1", ActorSystemAccountID: "sys-2", TargetType: "account", TargetID: "acct-1", Model: "m", Profile: "quick"},
		"target mismatch":    {SystemAccountID: "sys-1", ActorSystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-2", Model: "m", Profile: "quick"},
		"profile invalid":    {SystemAccountID: "sys-1", ActorSystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-1", Model: "m", Profile: "medium"},
	} {
		if err := validateBuiltRequest(scope, command, request); err == nil {
			t.Fatalf("%s 必须拒绝", name)
		}
	}
	globalScope := ManagementScope{ActorSystemAccountID: "sys-1", AllSystemAccounts: true}
	if err := validateBuiltRequest(globalScope, command, base); err != nil {
		t.Fatalf("全局 scope 必须通过: %v", err)
	}
}

func TestW11ERunDetailShapeAndErrorArms(t *testing.T) {
	if hasCompleteRunDetailShape(make(chan int)) {
		t.Fatal("不可序列化详情必须判为不完整")
	}
	if hasCompleteRunDetailShape(map[string]any{"id": "run-1"}) {
		t.Fatal("缺关键字段的详情必须判为不完整")
	}
	handler := newTestHTTPHandler()
	handler.Service = &contractRunService{runResult: RunResult{}}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(`{"targetType":"account","targetId":"acct-1","model":"gpt-5.6"}`)))
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), "缺少运行 ID") {
		t.Fatalf("缺 RunID 必须 500: %d %s", response.Code, response.Body.String())
	}
	handler.Service = &contractRunService{runResult: RunResult{RunID: "run-x"}, detailErr: errors.New("w11e-read-failed")}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(`{"targetType":"account","targetId":"acct-1","model":"gpt-5.6"}`)))
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), "w11e-read-failed") {
		t.Fatalf("详情读取失败必须 500: %d %s", response.Code, response.Body.String())
	}
	// /runs/{id} 查询错误与跨租户 404。
	handler.Service = &contractRunService{detail: RunView{ID: "run-1", SystemAccountID: "sys-2"}, found: true}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/runs/run-1", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("跨租户详情必须 404: %d", response.Code)
	}
	handler.Service = &contractRunService{detail: map[string]any{}, found: true}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/runs/run-1", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("无租户字段的详情必须 404: %d", response.Code)
	}
	handler.Service = &contractRunService{detailErr: errors.New("w11e-list-failed")}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/runs/run-1", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("详情错误必须 500: %d", response.Code)
	}
	// 列表脱敏失败（不可序列化 list + self scope）。
	selfList := newTestHTTPHandler()
	selfList.ForceActorScope = true
	selfList.Service = &scopedRunService{list: map[string]any{"bad": make(chan int)}}
	response = httptest.NewRecorder()
	selfList.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/runs", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("列表脱敏失败必须 500: %d %s", response.Code, response.Body.String())
	}
}

func TestW11EHandlerDefaultsAndBuildScopeArms(t *testing.T) {
	handler := &HTTPHandler{}
	if handler.maxBody() != 512<<10 {
		t.Fatalf("默认 maxBody=%d", handler.maxBody())
	}
	if handler.heartbeat() != 10*time.Second {
		t.Fatalf("默认 heartbeat=%v", handler.heartbeat())
	}
	admin := newTestHTTPHandler()
	admin.AllowCrossAccount = true
	admin.BuildScoped = nil
	response := httptest.NewRecorder()
	admin.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(`{"targetType":"account","targetId":"acct-1","model":"gpt-5.6"}`)))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "全局 scope 尚未接入") {
		t.Fatalf("全局 scope 无构建器必须 400: %d %s", response.Code, response.Body.String())
	}
	// 构建器返回普通错误必须 400；RequestError 保持状态码。
	plain := newTestHTTPHandler()
	plain.Build = func(context.Context, string, RunCommand) (RunRequest, error) { return RunRequest{}, errors.New("w11e-build-failed") }
	response = httptest.NewRecorder()
	plain.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(`{"targetType":"account","targetId":"acct-1","model":"gpt-5.6"}`)))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "w11e-build-failed") {
		t.Fatalf("普通构建错误必须 400: %d %s", response.Code, response.Body.String())
	}
	invalid := newTestHTTPHandler()
	invalid.Build = func(context.Context, string, RunCommand) (RunRequest, error) {
		return RunRequest{SystemAccountID: "sys-2", ActorSystemAccountID: "sys-1", TargetType: "account", TargetID: "a", Model: "m", Profile: "quick"}, nil
	}
	response = httptest.NewRecorder()
	invalid.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(`{"targetType":"account","targetId":"acct-1","model":"gpt-5.6"}`)))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "scope 与选中账号不一致") {
		t.Fatalf("scope 漂移必须 400: %d %s", response.Code, response.Body.String())
	}
}

// w11ePlainRecorder 只实现基础 ResponseWriter 接口（无 Flusher）。
type w11ePlainRecorder struct {
	header http.Header
	body   strings.Builder
	code   int
}

func (r *w11ePlainRecorder) Header() http.Header  { return r.header }
func (r *w11ePlainRecorder) WriteHeader(code int) { r.code = code }
func (r *w11ePlainRecorder) Write(data []byte) (int, error) {
	return r.body.Write(data)
}

func TestW11ESSEFlusherAndMarshalArms(t *testing.T) {
	handler := newTestHTTPHandler()
	handler.Service = &contractRunService{streamResult: RunResult{RunID: "run-1"}, detail: map[string]any{}, found: false}
	recorder := &w11ePlainRecorder{header: http.Header{}}
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/run/stream", strings.NewReader(`{"targetType":"account","targetId":"acct-1","model":"gpt-5.6"}`)))
	// 非 Flusher 写入必须被拒绝（没有 SSE 头）。
	if recorder.header.Get("Content-Type") == "text/event-stream; charset=utf-8" {
		t.Fatal("不支持 Flusher 的连接不能进入 SSE 流")
	}
	// run_completed 事件携带不可序列化数据 → 进度帧写失败，流静默终止。
	bad := newTestHTTPHandler()
	bad.Service = &contractRunService{
		streamEvents: []ProgressEvent{{Kind: "run_completed", Data: map[string]any{"runId": "run-1", "bad": make(chan int)}}},
		streamResult: RunResult{RunID: "run-1"},
		detail:       map[string]any{"id": "run-1", "requestSummary": map[string]any{}, "resultSummary": map[string]any{}, "checks": []any{}, "systemAccountId": "sys-1"},
		found:        true,
	}
	streamRecorder := httptest.NewRecorder()
	bad.ServeHTTP(streamRecorder, httptest.NewRequest(http.MethodPost, "/run/stream", strings.NewReader(`{"targetType":"account","targetId":"acct-1","model":"gpt-5.6"}`)))
	if strings.Contains(streamRecorder.Body.String(), "event: complete") {
		t.Fatal("进度帧写失败后不应继续输出 complete 帧")
	}
}

func TestW11EAdaptFrontendProgressEventArms(t *testing.T) {
	request := RunRequest{TargetID: "acct-1", Model: "m", Profile: "quick", TrustedComparison: true, TrustedComparisonAccountID: "acct-2"}
	payload, ok := adaptFrontendProgressEvent(ProgressEvent{Kind: "run_started"}, request)
	if !ok || payload["type"] != "run_started" || payload["trustedComparisonAccountId"] != "acct-2" || payload["trustedComparison"] != true {
		t.Fatalf("run_started 载荷=%v", payload)
	}
	if _, ok := adaptFrontendProgressEvent(ProgressEvent{Kind: "run_completed", Data: map[string]any{"status": "completed"}}, request); ok {
		t.Fatal("缺 runId 的 run_completed 必须被丢弃")
	}
	payload, ok = adaptFrontendProgressEvent(ProgressEvent{Kind: "run_completed", Data: map[string]any{"runId": "r1"}}, request)
	if !ok || payload["score"] != 0 || payload["maxScore"] != 100 || payload["level"] != "unavailable" {
		t.Fatalf("run_completed 默认值载荷=%v", payload)
	}
	payload, ok = adaptFrontendProgressEvent(ProgressEvent{Kind: "quality_health_sync"}, request)
	if !ok || payload["result"] != "pending_retry" || payload["statHour"] != "" {
		t.Fatalf("quality_health_sync 默认载荷=%v", payload)
	}
	payload, ok = adaptFrontendProgressEvent(ProgressEvent{Kind: "health_sync_failed"}, request)
	if !ok || payload["result"] != "failed" {
		t.Fatalf("health_sync_failed 默认载荷=%v", payload)
	}
	payload, ok = adaptFrontendProgressEvent(ProgressEvent{Kind: "quality_health_sync", Data: map[string]any{"result": "ok", "statHour": "2026-09-16T10:00Z"}}, request)
	if !ok || payload["result"] != "ok" || payload["statHour"] != "2026-09-16T10:00Z" {
		t.Fatalf("quality_health_sync 显式载荷=%v", payload)
	}
	if _, ok := adaptFrontendProgressEvent(ProgressEvent{Kind: "w11e-unknown"}, request); ok {
		t.Fatal("未知事件类型必须被丢弃")
	}
}

func TestW11EStreamErrorPayloadHelpers(t *testing.T) {
	if payload := ownerStreamError(nil); payload["message"] != "模型检测失败" {
		t.Fatalf("nil 错误载荷=%v", payload)
	}
	if _, present := ownerStreamError(errors.New("x"))["statusCode"]; present {
		t.Fatal("普通错误不得编造状态码")
	}
	payload := ownerStreamError(&RequestError{StatusCode: http.StatusNotFound, Message: "目标不存在"})
	if payload["statusCode"] != http.StatusNotFound || payload["message"] != "目标不存在" {
		t.Fatalf("RequestError 载荷=%v", payload)
	}
	response := httptest.NewRecorder()
	writeRunError(response, nil)
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), "模型检测失败") {
		t.Fatalf("nil run 错误必须使用默认消息: %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	writeRunError(response, errors.New("   "))
	if !strings.Contains(response.Body.String(), "模型检测失败") {
		t.Fatalf("空白错误消息必须回退默认: %s", response.Body.String())
	}
	response = httptest.NewRecorder()
	writeRunError(response, &RequestError{StatusCode: 702, Message: "越界状态码"})
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("越界状态码必须回退 500: %d", response.Code)
	}
}

func TestW11EServeDetailJSONArms(t *testing.T) {
	// GetRun 返回 JSON 原始形态且 systemAccountId 为空字符串 → 404。
	handler := newTestHTTPHandler()
	handler.Service = &contractRunService{detail: map[string]any{"systemAccountId": "  "}, found: true}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/runs/run-1", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("空白 systemAccountId 必须 404: %d", response.Code)
	}
	// 详情为 JSON 字符串（非对象）。
	handler.Service = &contractRunService{detail: json.RawMessage(`"not-an-object"`), found: true}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/runs/run-1", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("非对象详情必须 404: %d", response.Code)
	}
	// 分页参数错误。
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/runs?page=x", nil))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "page") {
		t.Fatalf("非法分页必须 400: %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/runs?pageSize=0", nil))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "pageSize") {
		t.Fatalf("非法 pageSize 必须 400: %d %s", response.Code, response.Body.String())
	}
}

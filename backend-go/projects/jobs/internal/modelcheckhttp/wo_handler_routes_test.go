package modelcheckhttp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckactive"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckauth"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckexecutor"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprofile"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckruntime"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckstore"
)

// woRunReader 是 RunReader 的可编程假实现。
type woRunReader struct {
	listResult modelcheckstore.RunListResult
	listErr    error
	listOpts   *modelcheckstore.RunListOptions
	detail     modelcheckstore.RunDetail
	found      bool
	getErr     error
}

func (r *woRunReader) ListRuns(_ context.Context, options modelcheckstore.RunListOptions) (modelcheckstore.RunListResult, error) {
	r.listOpts = &options
	return r.listResult, r.listErr
}

func (r *woRunReader) GetRun(_ context.Context, runID, systemAccountID string) (modelcheckstore.RunDetail, bool, error) {
	return r.detail, r.found, r.getErr
}

func TestServeHTTPGuards(t *testing.T) {
	var nilHandler *Handler
	recorder := httptest.NewRecorder()
	nilHandler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/runs", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil handler 应返回 503: %d", recorder.Code)
	}
	empty := &Handler{}
	recorder = httptest.NewRecorder()
	empty.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/runs", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("未初始化 handler 应返回 503: %d", recorder.Code)
	}
	handler := newTestHandler(woService(t))
	handler.Authorize = func(context.Context, *http.Request) (Scope, error) {
		return Scope{}, ErrUnauthorized
	}
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/runs", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("鉴权失败应返回 401: %d", recorder.Code)
	}
	forbidden := &HTTPError{Status: http.StatusForbidden, Message: "需要管理员权限", Code: "forbidden"}
	handler.Authorize = func(context.Context, *http.Request) (Scope, error) {
		return Scope{}, forbidden
	}
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/runs", nil))
	if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), `"code":"forbidden"`) {
		t.Fatalf("HTTPError 应透传状态与 code: %d %s", recorder.Code, recorder.Body.String())
	}
	handler.Authorize = func(context.Context, *http.Request) (Scope, error) {
		return Scope{ActorSystemAccountID: "actor"}, nil
	}
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/runs", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("空系统作用域应返回 401: %d", recorder.Code)
	}
	handler.Authorize = func(context.Context, *http.Request) (Scope, error) {
		return Scope{SystemAccountID: "system-account"}, nil
	}
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/unknown", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("未知路由应 404: %d", recorder.Code)
	}
}

func TestServeRunListValidationAndFilters(t *testing.T) {
	service := woService(t)
	handler := newTestHandler(service)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/runs", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("Reader 缺失应返回 503: %d", recorder.Code)
	}
	reader := &woRunReader{listResult: modelcheckstore.RunListResult{Total: 1}}
	handler.Reader = reader
	// 分页参数非法分支。
	for _, query := range []string{"?page=0", "?page=abc", "?pageSize=0", "?pageSize=101", "?pageSize=xyz"} {
		recorder = httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/runs"+query, nil))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("分页参数 %s 应返回 400: %d", query, recorder.Code)
		}
	}
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet,
		"/runs?page=3&pageSize=10&targetId=target-1&model=gpt-5.6-sol&level=healthy&status=completed&triggerKind=manual&startAt=2026-01-01&endAt=2026-01-02", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("合法查询应 200: %d %s", recorder.Code, recorder.Body.String())
	}
	if reader.listOpts == nil || reader.listOpts.Page != 3 || reader.listOpts.PageSize != 10 ||
		reader.listOpts.TargetType != "account" || reader.listOpts.TargetID != "target-1" ||
		reader.listOpts.Model != "gpt-5.6-sol" || reader.listOpts.Level != "healthy" ||
		reader.listOpts.Status != "completed" || reader.listOpts.TriggerKind != "manual" ||
		reader.listOpts.StartAt != "2026-01-01" || reader.listOpts.EndAt != "2026-01-02" {
		t.Fatalf("查询参数应完整传递且目标类型固定为 account: %#v", reader.listOpts)
	}
	reader.listErr = errors.New("reader down")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/runs", nil))
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("Reader 错误应返回 502: %d", recorder.Code)
	}
}

func TestServeRunDetailValidation(t *testing.T) {
	service := woService(t)
	handler := newTestHandler(service)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/runs/run-1", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("Reader 缺失应返回 503: %d", recorder.Code)
	}
	reader := &woRunReader{found: true, detail: modelcheckstore.RunDetail{}}
	handler.Reader = reader
	for _, path := range []string{"/runs/a/b"} {
		recorder = httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("非法 run id %s 应返回 404: %d", path, recorder.Code)
		}
	}
	// 无法反转义的路径段同样按 404 拒绝。
	badEscape := httptest.NewRequest(http.MethodGet, "/runs/run-1", nil)
	badEscape.URL = &url.URL{Path: "/runs/%zz"}
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, badEscape)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("无法反转义的 run id 应返回 404: %d", recorder.Code)
	}
	reader.getErr = errors.New("reader down")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/runs/run-1", nil))
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("Reader 错误应返回 502: %d", recorder.Code)
	}
	reader.getErr = nil
	reader.found = false
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/runs/run-1", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("未找到应返回 404: %d", recorder.Code)
	}
	reader.found = true
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/runs/run%2D1", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"data"`) {
		t.Fatalf("命中记录应 200: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestJSONRunBuildAndRunErrorMapping(t *testing.T) {
	service := woService(t)
	handler := newTestHandler(service)
	body := `{"targetType":"account","targetId":"target-account","model":"gpt-5.6-sol"}`
	// resolveScope 失败 → 400。
	handler.ResolveScope = func(context.Context, Scope, Command) (Scope, error) {
		return Scope{}, errors.New("scope resolve failed")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(body)))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("作用域解析失败应 400: %d", recorder.Code)
	}
	// resolveScope 返回 HTTPError → 状态透传。
	handler.ResolveScope = func(context.Context, Scope, Command) (Scope, error) {
		return Scope{}, &HTTPError{Status: http.StatusForbidden, Message: "需要管理员权限"}
	}
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(body)))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("HTTPError 状态应透传: %d", recorder.Code)
	}
	handler.ResolveScope = nil
	// BuildRequest 失败 → 400。
	handler.BuildRequest = func(context.Context, Scope, Command) (modelcheckruntime.RunRequest, error) {
		return modelcheckruntime.RunRequest{}, errors.New("build failed")
	}
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(body)))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("构建失败应 400: %d", recorder.Code)
	}
	// 恢复 BuildRequest，运行期错误映射。
	handler.BuildRequest = func(_ context.Context, scope Scope, command Command) (modelcheckruntime.RunRequest, error) {
		request, err := newTestHandler(service).BuildRequest(context.Background(), scope, command)
		return request, err
	}
	failing := woService(t)
	failing.Resolver = func(context.Context, modelcheckexecutor.ResolutionRequest) (modelcheckexecutor.ResolvedTarget, error) {
		return modelcheckexecutor.ResolvedTarget{}, modelcheckruntime.ErrInvalidRequest
	}
	invalidHandler := newTestHandler(failing)
	recorder = httptest.NewRecorder()
	invalidHandler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(body)))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("无效请求应 400: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestStreamRunBuildFailureWritesJSON(t *testing.T) {
	service := woService(t)
	handler := newTestHandler(service)
	handler.BuildRequest = func(context.Context, Scope, Command) (modelcheckruntime.RunRequest, error) {
		return modelcheckruntime.RunRequest{}, errors.New("build failed")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/run/stream", strings.NewReader(`{"targetType":"account","targetId":"target-account","model":"gpt-5.6-sol"}`)))
	if recorder.Code != http.StatusBadRequest || strings.Contains(recorder.Body.String(), "event:") {
		t.Fatalf("流式构建失败应在 SSE 之前返回 JSON 错误: %d %s", recorder.Code, recorder.Body.String())
	}
	// 请求体非法同样在 SSE 开始前拒绝。
	recorder = httptest.NewRecorder()
	handler.BuildRequest = nil
	handler.BuildRequest = func(_ context.Context, scope Scope, command Command) (modelcheckruntime.RunRequest, error) {
		return newTestHandler(service).BuildRequest(context.Background(), scope, command)
	}
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/run/stream", strings.NewReader(`{"targetType":"model"}`)))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("非法命令应 400: %d", recorder.Code)
	}
	// 超小请求体上限触发 MaxBytesReader 失败。
	limited := newTestHandler(service)
	limited.MaxBodyBytes = 8
	recorder = httptest.NewRecorder()
	limited.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(`{"targetType":"account","targetId":"target-account","model":"gpt-5.6-sol"}`)))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("超限请求体应 400: %d", recorder.Code)
	}
}

func TestStreamRunEmitsErrorEventOnRunFailure(t *testing.T) {
	failing := woService(t)
	failing.Resolver = func(context.Context, modelcheckexecutor.ResolutionRequest) (modelcheckexecutor.ResolvedTarget, error) {
		return modelcheckexecutor.ResolvedTarget{}, errors.New("resolver boom")
	}
	handler := newTestHandler(failing)
	handler.Heartbeat = time.Hour
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/run/stream", strings.NewReader(`{"targetType":"account","targetId":"target-account","model":"gpt-5.6-sol"}`)))
	body := recorder.Body.String()
	if !strings.Contains(body, "event: error") || !strings.Contains(body, "resolver boom") {
		t.Fatalf("运行失败应产生 error 事件: %s", body)
	}
}

func TestServeActiveAndStopWithoutRegistry(t *testing.T) {
	service := woService(t)
	service.Active = nil
	handler := newTestHandler(service)
	handler.Active = nil
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/run/active", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"data":null`) {
		t.Fatalf("无注册表时 active 应返回 null: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/run/stop", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"stopped":false`) {
		t.Fatalf("无注册表时 stop 应返回 false: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestActiveKeyPrefersActor(t *testing.T) {
	if got := activeKey(Scope{SystemAccountID: "system", ActorSystemAccountID: "actor"}); got != "system-account:actor" {
		t.Fatalf("有 actor 时应使用 actor: %s", got)
	}
	if got := activeKey(Scope{SystemAccountID: "system"}); got != "system-account:system" {
		t.Fatalf("无 actor 时应回退 system: %s", got)
	}
}

func TestSSEErrorPayloadMarksInvalidRequest(t *testing.T) {
	payload := sseErrorPayload(modelcheckruntime.ErrInvalidRequest)
	if payload["statusCode"] != http.StatusBadRequest {
		t.Fatalf("无效请求错误应带 400 状态: %#v", payload)
	}
	payload = sseErrorPayload(errors.New("boom"))
	if _, exists := payload["statusCode"]; exists {
		t.Fatalf("普通错误不应带状态码: %#v", payload)
	}
}

// woService 构造仅返回服务的测试辅助（丢弃数据集路径与固定时间）。
func woService(t *testing.T) *modelcheckruntime.Service {
	t.Helper()
	service, _, _ := newHTTPTestService(t, "http://unused.invalid")
	return service
}

func TestManagementAuthErrorMapping(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"无效令牌", modelcheckauth.ErrInvalidToken, http.StatusUnauthorized, ""},
		{"需要登录", modelcheckauth.ErrLoginRequired, http.StatusUnauthorized, ""},
		{"会话过期", modelcheckauth.ErrSessionExpired, http.StatusUnauthorized, ""},
		{"需改密", modelcheckauth.ErrMustChange, http.StatusForbidden, "must_change_password"},
		{"权限不足", modelcheckauth.ErrForbidden, http.StatusForbidden, ""},
		{"未知错误", errors.New("db down"), http.StatusServiceUnavailable, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			httpErr, ok := managementAuthError(tc.err).(*HTTPError)
			if !ok || httpErr.Status != tc.wantStatus || httpErr.Code != tc.wantCode {
				t.Fatalf("映射不符: %#v", httpErr)
			}
		})
	}
}

func TestStreamRunUsesDefaultHeartbeatAndConflictRetryAfter(t *testing.T) {
	service := woService(t)
	registry := modelcheckactive.NewRegistry()
	service.Active = registry
	_, acquired, _ := registry.TryStart(context.Background(), "system-account:system-account", modelcheckactive.Summary{RunID: "existing"})
	if !acquired {
		t.Fatal("failed to seed active run")
	}
	handler := newTestHandler(service)
	// RetryAfterSec>0 时冲突响应应使用自定义值（retryAfter 正分支）。
	handler.RetryAfterSec = 7
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(`{"targetType":"account","targetId":"target-account","model":"gpt-5.6-sol"}`)))
	if recorder.Code != http.StatusConflict || recorder.Header().Get("Retry-After") != "7" {
		t.Fatalf("自定义 Retry-After 应生效: %d %q", recorder.Code, recorder.Header().Get("Retry-After"))
	}
	// Heartbeat 未配置时使用默认值并完成流式输出（heartbeat 默认分支）。
	handler.RetryAfterSec = 0
	slow := woService(t)
	slowServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(20 * time.Millisecond)
		_, _ = w.Write([]byte(`{"model":"gpt-5.6-sol","output_text":"OK-MODEL-CHECK","usage":{"input_tokens":1}}`))
	}))
	defer slowServer.Close()
	slow.Resolver = func(context.Context, modelcheckexecutor.ResolutionRequest) (modelcheckexecutor.ResolvedTarget, error) {
		return modelcheckexecutor.ResolvedTarget{ConfigRevision: "config-revision-1", ProtocolProfileID: "profile-openai-responses", ProtocolProfileRevision: "profile-revision-1", Endpoint: slowServer.URL, Protocol: modelcheckprofile.ProtocolOpenAIResponses, Model: "gpt-5.6-sol", Prompt: "hello", MaxOutputTokens: 32}, nil
	}
	streamHandler := newTestHandler(slow)
	streamRecorder := httptest.NewRecorder()
	streamHandler.ServeHTTP(streamRecorder, httptest.NewRequest(http.MethodPost, "/run/stream", strings.NewReader(`{"targetType":"account","targetId":"target-account","model":"gpt-5.6-sol"}`)))
	if !strings.Contains(streamRecorder.Body.String(), "event: complete") {
		t.Fatalf("默认心跳下流式应完成: %s", streamRecorder.Body.String())
	}
	// 空 actor 作用域下 active 仍可访问（serveActive 空作用域分支）。
	emptyScope := newTestHandler(service)
	emptyScope.Authorize = func(context.Context, *http.Request) (Scope, error) {
		return Scope{SystemAccountID: "system-account", ActorSystemAccountID: ""}, nil
	}
	recorder = httptest.NewRecorder()
	emptyScope.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/run/active", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("active 查询应 200: %d", recorder.Code)
	}
}

// woPlainWriter 只实现 http.ResponseWriter，用于验证非 Flusher 服务器的降级路径。
type woPlainWriter struct {
	header http.Header
	body   strings.Builder
	code   int
}

func (w *woPlainWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}
func (w *woPlainWriter) Write(data []byte) (int, error) { return w.body.Write(data) }
func (w *woPlainWriter) WriteHeader(status int)         { w.code = status }

func TestStreamRunRejectsServerWithoutFlusher(t *testing.T) {
	service := woService(t)
	handler := newTestHandler(service)
	writer := &woPlainWriter{}
	request := httptest.NewRequest(http.MethodPost, "/run/stream", strings.NewReader(`{"targetType":"account","targetId":"target-account","model":"gpt-5.6-sol"}`))
	handler.ServeHTTP(writer, request)
	if writer.code != http.StatusInternalServerError || !strings.Contains(writer.body.String(), "SSE") {
		t.Fatalf("非 Flusher 服务器应返回 500: %d %s", writer.code, writer.body.String())
	}
}

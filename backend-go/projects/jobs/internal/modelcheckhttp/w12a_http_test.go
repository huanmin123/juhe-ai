package modelcheckhttp

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckactive"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckauth"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckinput"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckruntime"
)

// w12a_http_test.go 覆盖管理鉴权适配器的 nil/多值头/过滤器分支、
// 目标作用域解析器分支与 SSE 错误负载分类。

func TestW12aAdminAuthorizeNilAuth(t *testing.T) {
	authorize := NewAdminAuthorizeFunc(nil)
	request := httptest.NewRequest(http.MethodGet, "/runs", nil)
	_, err := authorize(context.Background(), request)
	httpErr, ok := err.(*HTTPError)
	if !ok || httpErr.Status != http.StatusServiceUnavailable {
		t.Fatalf("nil 认证器应返回 503: %#v", err)
	}
}

func TestW12aAdminAuthorizeDuplicateAuthorizationHeader(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/runs", nil)
	request.Header["Authorization"] = []string{"a", "b"}
	authorize := NewAdminAuthorizeFunc(&modelcheckauth.Authenticator{})
	_, err := authorize(context.Background(), request)
	httpErr, ok := err.(*HTTPError)
	if !ok || httpErr.Status != http.StatusUnauthorized {
		t.Fatalf("重复 Authorization 头应 401: %#v", err)
	}
}

type w12aScopeReader struct{ scope string }

func (r w12aScopeReader) ResolveManagementSystemAccount(context.Context, string) (string, error) {
	return r.scope, nil
}

func TestW12aAdminTargetScopeResolverBranches(t *testing.T) {
	resolver := NewAdminTargetScopeResolver(nil)
	_, err := resolver(context.Background(), Scope{}, Command{})
	httpErr, ok := err.(*HTTPError)
	if !ok || httpErr.Status != http.StatusServiceUnavailable {
		t.Fatalf("nil reader 应返回 503: %#v", err)
	}
	// 已带过滤器 → 直接返回原 scope。
	scoped := Scope{SystemAccountID: "sys", SystemAccountFilterID: "filter"}
	resolved, err := NewAdminTargetScopeResolver(w12aScopeReader{scope: "ignored"})(context.Background(), scoped, Command{})
	if err != nil || resolved.SystemAccountID != "sys" {
		t.Fatalf("已有过滤器不应改写: %#v %v", resolved, err)
	}
	// 无过滤器 → 查询账户归属。
	resolved, err = NewAdminTargetScopeResolver(w12aScopeReader{scope: "w12a-owner"})(context.Background(), Scope{SystemAccountID: "actor"}, Command{TargetID: "acct"})
	if err != nil || resolved.SystemAccountID != "w12a-owner" {
		t.Fatalf("应改写为账户归属: %#v %v", resolved, err)
	}
	// 查询失败 → 400。
	_, err = NewAdminTargetScopeResolver(w12aFailReader{})(context.Background(), Scope{}, Command{TargetID: "acct"})
	httpErr, ok = err.(*HTTPError)
	if !ok || httpErr.Status != http.StatusBadRequest {
		t.Fatalf("查询失败应 400: %#v", err)
	}
}

type w12aFailReader struct{}

func (w12aFailReader) ResolveManagementSystemAccount(context.Context, string) (string, error) {
	return "", errors.New("w12a boom")
}

func TestW12aRequestedSystemAccountFilterVariants(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/runs?systemAccountId=a&systemAccountId=b", nil)
	if _, err := requestedSystemAccountFilter(request); err == nil {
		t.Fatalf("重复过滤器应报错")
	}
	request = httptest.NewRequest(http.MethodGet, "/runs?systemAccountId=", nil)
	if _, err := requestedSystemAccountFilter(request); err == nil {
		t.Fatalf("空白过滤器应报错")
	}
	request = httptest.NewRequest(http.MethodGet, "/runs?systemAccountId=all", nil)
	value, err := requestedSystemAccountFilter(request)
	if err != nil || value != "" {
		t.Fatalf("all 应清空过滤器: %q %v", value, err)
	}
	query := url.Values{"systemAccountId": []string{" w12a-sys "}}
	request.URL.RawQuery = query.Encode()
	value, err = requestedSystemAccountFilter(request)
	if err != nil || value != "w12a-sys" {
		t.Fatalf("过滤器应裁剪空白: %q %v", value, err)
	}
}

func TestW12aSSEErrorPayloadMarksInvalidRequest(t *testing.T) {
	payload := sseErrorPayload(modelcheckruntime.ErrInvalidRequest)
	if payload["statusCode"] != http.StatusBadRequest {
		t.Fatalf("无效请求应标记 400: %#v", payload)
	}
	payload = sseErrorPayload(errors.New("w12a other"))
	if _, has := payload["statusCode"]; has {
		t.Fatalf("其他错误不应带状态码: %#v", payload)
	}
	if !strings.Contains(payload["message"].(string), "w12a other") {
		t.Fatalf("消息应保留原始错误")
	}
}

// w12aNoFlushWriter 不实现 http.Flusher，用于触发 SSE 不支持分支。
type w12aNoFlushWriter struct {
	header http.Header
}

func (w *w12aNoFlushWriter) Header() http.Header         { return w.header }
func (w *w12aNoFlushWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w *w12aNoFlushWriter) WriteHeader(int)             {}

func TestW12aStreamRunRejectsNonFlusherWriter(t *testing.T) {
	handler := newTestHandler(woService(t))
	handler.Authorize = func(context.Context, *http.Request) (Scope, error) {
		return Scope{SystemAccountID: "system-account"}, nil
	}
	writer := &w12aNoFlushWriter{header: http.Header{}}
	request := httptest.NewRequest(http.MethodPost, "/run/stream", strings.NewReader(`{"targetType":"account","targetId":"acct","model":"gpt-5.6-sol"}`))
	handler.serveStreamRun(writer, request, Scope{SystemAccountID: "system-account", ActorSystemAccountID: "system-account"})
	if !strings.Contains(writer.header.Get("Content-Type"), "application/json") || writer.header.Get("Content-Type") == "text/event-stream; charset=utf-8" {
		t.Fatalf("非 Flusher 应返回 JSON 错误而非 SSE: %q", writer.header.Get("Content-Type"))
	}
}

func TestW12aStreamRunHeartbeatAndCompletion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"model":"gpt-5.6-sol","output_text":"OK-MODEL-CHECK"}`))
	}))
	defer server.Close()
	service, _, _ := newHTTPTestService(t, server.URL)
	handler := newTestHandler(service)
	handler.Heartbeat = time.Millisecond
	handler.Authorize = func(context.Context, *http.Request) (Scope, error) {
		return Scope{SystemAccountID: "system-account"}, nil
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/run/stream", strings.NewReader(`{"targetType":"account","targetId":"acct","model":"gpt-5.6-sol"}`))
	handler.ServeHTTP(recorder, request)
	body := recorder.Body.String()
	if !strings.Contains(body, ": connected") || !strings.Contains(body, "event: complete") {
		t.Fatalf("SSE 应含连接与完成事件: %s", body)
	}
}

func TestW12aRequireAdminErrorMapsToHTTPStatus(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	auth, err := modelcheckauth.New(db, modelcheckauth.SQLite, nil)
	if err != nil {
		t.Fatal(err)
	}
	authorize := NewAdminAuthorizeFunc(auth)
	request := httptest.NewRequest(http.MethodGet, "/runs", nil)
	request.Header.Set("Authorization", "Bearer w12a-token")
	_, err = authorize(context.Background(), request)
	httpErr, ok := err.(*HTTPError)
	if !ok {
		t.Fatalf("应返回 HTTPError: %#v", err)
	}
	if httpErr.Status != http.StatusUnauthorized && httpErr.Status != http.StatusServiceUnavailable {
		t.Fatalf("状态不符: %d", httpErr.Status)
	}
	// 带过滤器的授权请求（会话缺失仍失败，但过滤器分支被解析）。
	request2 := httptest.NewRequest(http.MethodGet, "/runs?systemAccountId=w12a-sys", nil)
	request2.Header.Set("Authorization", "Bearer w12a-token")
	if _, err := authorize(context.Background(), request2); err == nil {
		t.Fatalf("缺失会话应失败")
	}
}

func TestW12aServeActiveWithoutScope(t *testing.T) {
	handler := newTestHandler(woService(t))
	handler.Authorize = func(context.Context, *http.Request) (Scope, error) {
		return Scope{SystemAccountID: "system-account"}, nil
	}
	handler.Reader = &woRunReader{}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/run/active", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"data":null`) {
		t.Fatalf("无活动运行应返回 null: %d %s", recorder.Code, recorder.Body.String())
	}
}

// w12aFailingWriter 写入即失败，用于触发 SSE 的网络写错误分支。
type w12aFailingWriter struct {
	header http.Header
}

func (w *w12aFailingWriter) Header() http.Header { return w.header }
func (w *w12aFailingWriter) Write(p []byte) (int, error) {
	return 0, errors.New("w12a write failure")
}
func (w *w12aFailingWriter) WriteHeader(int) {}
func (w *w12aFailingWriter) Flush()          {}

func TestW12aWriteSSEWriteFailureBranches(t *testing.T) {
	writer := &w12aFailingWriter{header: http.Header{}}
	handler := &Handler{}
	if handler.writeSSE(writer, writer, "heartbeat", nil) {
		t.Fatalf("heartbeat 写失败应返回 false")
	}
	if handler.writeSSE(writer, writer, "progress", nil) {
		t.Fatalf("无负载事件写失败应返回 false")
	}
	if handler.writeSSE(writer, writer, "progress", map[string]any{"k": "v"}) {
		t.Fatalf("带负载事件写失败应返回 false")
	}
}

func TestW12aStreamRunConnectedWriteFailure(t *testing.T) {
	service, _, _ := newHTTPTestService(t, "http://unused.invalid")
	handler := newTestHandler(service)
	writer := &w12aFailingWriter{header: http.Header{}}
	request := httptest.NewRequest(http.MethodPost, "/run/stream", strings.NewReader(`{"targetType":"account","targetId":"acct","model":"gpt-5.6-sol"}`))
	handler.serveStreamRun(writer, request, Scope{SystemAccountID: "system-account", ActorSystemAccountID: "system-account"})
}

func TestW12aReserveActiveConflict(t *testing.T) {
	handler := newTestHandler(woService(t))
	scope := Scope{SystemAccountID: "system-account", ActorSystemAccountID: "system-account"}
	request := modelcheckruntime.RunRequest{RunID: "w12a-run", Target: modelcheckinput.AccountSnapshot{ID: "acct"}}
	first, release, err := handler.reserveActive(context.Background(), scope, request)
	if err != nil {
		t.Fatalf("首次占用不应失败: %v", err)
	}
	if first.ActiveLease == nil {
		t.Fatalf("首次占用应携带租约")
	}
	if _, _, err := handler.reserveActive(context.Background(), scope, request); err == nil {
		t.Fatalf("重复占用应报活动冲突")
	}
	release()
	if _, _, err := handler.reserveActive(context.Background(), scope, request); err != nil {
		t.Fatalf("释放后应可重新占用: %v", err)
	}
}

func TestW12aServeActiveAndStopHitRegistry(t *testing.T) {
	handler := newTestHandler(woService(t))
	handler.Authorize = func(context.Context, *http.Request) (Scope, error) {
		return Scope{SystemAccountID: "system-account", ActorSystemAccountID: "system-account"}, nil
	}
	// 借真实注册表启动一条活动记录后查询。
	handle, started, _ := handler.activeRegistry().TryStart(context.Background(), activeKey(Scope{SystemAccountID: "system-account"}), modelcheckactive.Summary{RunID: "w12a-run"})
	if !started {
		t.Fatalf("应能启动活动记录")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/run/active", nil))
	if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), `"data":null`) {
		t.Fatalf("活动记录应返回摘要: %d %s", recorder.Code, recorder.Body.String())
	}
	// Stop 命中并写入 StopRequest。
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/run/stop", strings.NewReader(`{"runId":"w12a-run"}`)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("Stop 应成功: %d %s", recorder.Code, recorder.Body.String())
	}
	handle.Finish()
}

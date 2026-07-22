package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/config"
	publicapilogjob "juhe-ai/backend-go/internal/jobs/publicapilog"
	"juhe-ai/backend-go/internal/jobs/queue"
	"juhe-ai/backend-go/internal/modules/publicapi"
	publicapiauth "juhe-ai/backend-go/internal/modules/publicapi/auth"
	publicapiratelimit "juhe-ai/backend-go/internal/modules/publicapi/ratelimit"
	"juhe-ai/backend-go/internal/store/port"
)

func TestPublicAPIShellSuccessCapturesAndEnqueuesLog(t *testing.T) {
	now := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	authenticator := &publicAPIShellAuthStub{ctx: publicAPIShellAuthContext()}
	limiter := &publicAPIShellLimiterStub{decision: publicapiratelimit.Decision{Allowed: true}}
	logQueue := &publicAPIShellLogQueueStub{}
	router := newTestPublicAPIShell(authenticator, limiter, logQueue, map[string]http.Handler{
		"group-list": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			endpoint, ok := PublicAPIEndpointFromRequest(r)
			if !ok || endpoint.ID != "group-list" {
				t.Fatalf("endpoint = %+v, ok=%v", endpoint, ok)
			}
			authContext, ok := PublicAPIAuthContextFromRequest(r)
			if !ok || authContext.SourceRefID != "source_1" {
				t.Fatalf("auth context = %+v, ok=%v", authContext, ok)
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"items": []any{}}})
		}),
	}, now)

	const querySecret = "sk-0123456789abcdef0123456789abcdef"
	const queryBearer = "Bearer abcdefghijklmnop"
	const rawQuery = "targetUsername=admin&keyword=" + querySecret + "&authorization=Bearer%20abcdefghijklmnop&filter%5Bstatus%5D=active"
	req := httptest.NewRequest(http.MethodGet, "/__aipublic__/group/list?"+rawQuery, nil)
	req.Header.Set("Authorization", "Bearer juis_plain")
	req.Header.Set("Cookie", "session=secret")
	req.Header.Set("User-Agent", "shell-test")
	req.Header.Set("X-Request-Id", "trace_public_shell")
	req.Header.Set("X-Trace-Id", "trace_public_shell")
	req.Header.Set("X-Forwarded-For", "198.51.100.9")
	req.RemoteAddr = "10.0.0.1:12345"
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Request-Id"); got != "trace_public_shell" {
		t.Fatalf("X-Request-Id = %q", got)
	}
	if authenticator.header != "Bearer juis_plain" || authenticator.scope != publicapi.ScopeGroupListRead {
		t.Fatalf("auth call = header %q scope %q", authenticator.header, authenticator.scope)
	}
	if limiter.calls != 1 || limiter.ctx.SourceRefID != "source_1" {
		t.Fatalf("limiter = calls %d ctx %+v", limiter.calls, limiter.ctx)
	}

	log := singlePublicAPILog(t, logQueue)
	if log.ID != "publog_test_1" || log.TraceID != "trace_public_shell" {
		t.Fatalf("log id/trace = %s/%s", log.ID, log.TraceID)
	}
	if log.Method != http.MethodGet || log.Path != "/__aipublic__/group/list" || log.QueryString != rawQuery {
		t.Fatalf("log request = %s %s ? %s", log.Method, log.Path, log.QueryString)
	}
	if log.ClientIP != "198.51.100.9" || log.UserAgent != "shell-test" {
		t.Fatalf("client info = %s %s", log.ClientIP, log.UserAgent)
	}
	if log.StatusCode == nil || *log.StatusCode != http.StatusOK || !log.Success {
		t.Fatalf("status/success = %v/%v", log.StatusCode, log.Success)
	}
	if log.SourceRefID != "source_1" || log.TokenID != "token_1" || !log.IsTestToken {
		t.Fatalf("source fields = %+v", log)
	}
	headers, ok := log.RequestData["headers"].(map[string]any)
	if !ok {
		t.Fatalf("request headers = %#v", log.RequestData["headers"])
	}
	for key := range headers {
		if strings.EqualFold(key, "authorization") || strings.EqualFold(key, "cookie") {
			t.Fatalf("request snapshot must not include credential header %q: %#v", key, headers)
		}
	}
	query, ok := log.RequestData["query"].(map[string]any)
	if !ok || query["keyword"] != querySecret || query["authorization"] != queryBearer {
		t.Fatalf("request query = %#v, want original captured values", log.RequestData["query"])
	}
	filter, ok := query["filter"].(map[string]any)
	if !ok || filter["status"] != "active" {
		t.Fatalf("request query filter = %#v, want extended bracket object", query["filter"])
	}
	responseBody, ok := log.ResponseData["body"].(map[string]any)
	if !ok || responseBody["data"] == nil {
		t.Fatalf("response body = %#v", log.ResponseData["body"])
	}
}

func TestPublicAPIShellPreservesJSONNumberShapeInSnapshots(t *testing.T) {
	authenticator := &publicAPIShellAuthStub{ctx: publicAPIShellAuthContext()}
	limiter := &publicAPIShellLimiterStub{decision: publicapiratelimit.Decision{Allowed: true}}
	logQueue := &publicAPIShellLogQueueStub{}
	router := newTestPublicAPIShell(authenticator, limiter, logQueue, map[string]http.Handler{
		"group-add": http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusCreated, map[string]any{
				"data": map[string]any{"weight": 20, "ratio": 2.5},
			})
		}),
	}, time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC))

	req := httptest.NewRequest(http.MethodPost, "/__aipublic__/group/add", strings.NewReader(`{"weight":10,"ratio":1.25}`))
	req.Header.Set("Authorization", "Bearer juis_plain")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	log := singlePublicAPILog(t, logQueue)
	requestBody, ok := log.RequestData["body"].(map[string]any)
	requestWeight, weightOK := requestBody["weight"].(float64)
	requestRatio, ratioOK := requestBody["ratio"].(float64)
	if !ok || !weightOK || !ratioOK || requestWeight != 10 || requestRatio != 1.25 {
		t.Fatalf("request body numbers = %#v, want JSON number values", log.RequestData["body"])
	}
	responseBody, ok := log.ResponseData["body"].(map[string]any)
	if !ok {
		t.Fatalf("response body = %#v, want object", log.ResponseData["body"])
	}
	responseData, ok := responseBody["data"].(map[string]any)
	responseWeight, weightOK := responseData["weight"].(float64)
	responseRatio, ratioOK := responseData["ratio"].(float64)
	if !ok || !weightOK || !ratioOK || responseWeight != 20 || responseRatio != 2.5 {
		t.Fatalf("response data numbers = %#v, want JSON number values", responseBody["data"])
	}
}

func TestPublicAPIShellUsesOuterTraceIDForPublicAPILog(t *testing.T) {
	now := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	authenticator := &publicAPIShellAuthStub{ctx: publicAPIShellAuthContext()}
	limiter := &publicAPIShellLimiterStub{decision: publicapiratelimit.Decision{Allowed: true}}
	logQueue := &publicAPIShellLogQueueStub{}
	shell := NewPublicAPIShell(PublicAPIShellOptions{
		Config:        config.Config{Host: "127.0.0.1", Port: 3000},
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		Authenticator: authenticator,
		RateLimiter:   limiter,
		LogClient:     logQueue,
		EndpointHandlers: map[string]http.Handler{"group-list": http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{}})
		})},
		Now:                     func() time.Time { return now },
		NewLogID:                func() string { return "publog_test_1" },
		SkipRequestIDMiddleware: true,
	})

	req := httptest.NewRequest(http.MethodGet, "/__aipublic__/group/list", nil)
	req.Header.Set("Authorization", "Bearer juis_plain")
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "outer_request_id"))
	req = req.WithContext(context.WithValue(req.Context(), traceIDKey, "outer_trace_id"))
	rec := httptest.NewRecorder()

	shell.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	log := singlePublicAPILog(t, logQueue)
	if log.TraceID != "outer_trace_id" {
		t.Fatalf("trace id = %q, want outer trace id", log.TraceID)
	}
}

func TestPublicAPIShellRoutesAllCatalogEndpointsThroughAuthAndHandler(t *testing.T) {
	for _, endpoint := range publicapi.Endpoints() {
		t.Run(endpoint.ID, func(t *testing.T) {
			authenticator := &publicAPIShellAuthStub{ctx: publicAPIShellAuthContext()}
			limiter := &publicAPIShellLimiterStub{decision: publicapiratelimit.Decision{Allowed: true}}
			logQueue := &publicAPIShellLogQueueStub{}
			handlerCalled := false
			router := newTestPublicAPIShell(authenticator, limiter, logQueue, map[string]http.Handler{
				endpoint.ID: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					handlerCalled = true
					gotEndpoint, ok := PublicAPIEndpointFromRequest(r)
					if !ok || gotEndpoint != endpoint {
						t.Fatalf("endpoint = %+v, ok=%v; want %+v", gotEndpoint, ok, endpoint)
					}
					writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"endpointId": endpoint.ID}})
				}),
			}, time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC))

			req := httptest.NewRequest(endpoint.Method, endpoint.Path, nil)
			req.Header.Set("Authorization", "Bearer juis_plain")
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if !handlerCalled {
				t.Fatal("handler was not called")
			}
			if authenticator.scope != endpoint.Scope {
				t.Fatalf("auth scope = %q, want %q", authenticator.scope, endpoint.Scope)
			}
			if limiter.calls != 1 {
				t.Fatalf("limiter calls = %d, want 1", limiter.calls)
			}
			log := singlePublicAPILog(t, logQueue)
			if log.Path != endpoint.Path {
				t.Fatalf("log path = %q, want %q", log.Path, endpoint.Path)
			}
		})
	}
}

func TestPublicAPIShellAuthErrorsReturnCodeAndSkipLimiter(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{
			name:       "missing bearer",
			err:        &publicapiauth.AuthError{StatusCode: http.StatusUnauthorized, Code: publicapiauth.ErrorCodeTokenMissing, Message: "缺少来源系统 token"},
			wantStatus: http.StatusUnauthorized,
			wantCode:   publicapiauth.ErrorCodeTokenMissing,
		},
		{
			name:       "scope forbidden keeps source context for log",
			err:        &publicapiauth.AuthError{StatusCode: http.StatusForbidden, Code: publicapiauth.ErrorCodeScopeForbidden, Message: "来源系统没有调用该接口的权限", Context: ptrPublicAPIAuthContext(publicAPIShellAuthContext())},
			wantStatus: http.StatusForbidden,
			wantCode:   publicapiauth.ErrorCodeScopeForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			authenticator := &publicAPIShellAuthStub{err: tt.err}
			limiter := &publicAPIShellLimiterStub{decision: publicapiratelimit.Decision{Allowed: true}}
			logQueue := &publicAPIShellLogQueueStub{}
			router := newTestPublicAPIShell(authenticator, limiter, logQueue, nil, time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC))

			req := httptest.NewRequest(http.MethodGet, "/__aipublic__/group/list", nil)
			req.Header.Set("Authorization", "Bearer juis_plain")
			req.Header.Set("X-Request-Id", "trace_auth_error")
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			var body map[string]any
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if body["code"] != tt.wantCode {
				t.Fatalf("code = %v, want %s", body["code"], tt.wantCode)
			}
			if limiter.calls != 0 {
				t.Fatalf("limiter calls = %d, want 0", limiter.calls)
			}
			log := singlePublicAPILog(t, logQueue)
			if log.StatusCode == nil || *log.StatusCode != tt.wantStatus || log.Success {
				t.Fatalf("log status/success = %v/%v", log.StatusCode, log.Success)
			}
			if log.ErrorCode != tt.wantCode {
				t.Fatalf("log error code = %q, want %q", log.ErrorCode, tt.wantCode)
			}
		})
	}
}

func TestPublicAPIShellRateLimitReturnsRetryAfterAndLogsSource(t *testing.T) {
	authenticator := &publicAPIShellAuthStub{ctx: publicAPIShellAuthContext()}
	limiter := &publicAPIShellLimiterStub{decision: publicapiratelimit.Decision{
		Allowed:           false,
		Rule:              port.PublicAPIRateLimitRule{WindowSeconds: 60, MaxRequests: 10},
		RetryAfterSeconds: 7,
	}}
	logQueue := &publicAPIShellLogQueueStub{}
	router := newTestPublicAPIShell(authenticator, limiter, logQueue, nil, time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC))

	req := httptest.NewRequest(http.MethodGet, "/__aipublic__/group/list", nil)
	req.Header.Set("Authorization", "Bearer juis_plain")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "7" {
		t.Fatalf("Retry-After = %q, want 7", got)
	}
	var body struct {
		Code    string `json:"code"`
		Details struct {
			WindowSeconds     int `json:"windowSeconds"`
			MaxRequests       int `json:"maxRequests"`
			RetryAfterSeconds int `json:"retryAfterSeconds"`
		} `json:"details"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Code != "external_source_rate_limited" || body.Details.RetryAfterSeconds != 7 {
		t.Fatalf("body = %+v", body)
	}
	log := singlePublicAPILog(t, logQueue)
	if log.SourceRefID != "source_1" || log.ErrorCode != "external_source_rate_limited" {
		t.Fatalf("log source/error = %+v", log)
	}
}

func TestPublicAPIShellBodyParseErrorsAreCapturedBeforeAuth(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantReason string
	}{
		{name: "invalid json", body: "{bad", wantStatus: http.StatusBadRequest, wantReason: "request_body_parse_failed"},
		{name: "too large", body: strings.Repeat("a", publicapi.JSONBodyLimitBytes+1), wantStatus: http.StatusRequestEntityTooLarge, wantReason: "request_body_too_large"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			authenticator := &publicAPIShellAuthStub{ctx: publicAPIShellAuthContext()}
			limiter := &publicAPIShellLimiterStub{decision: publicapiratelimit.Decision{Allowed: true}}
			logQueue := &publicAPIShellLogQueueStub{}
			router := newTestPublicAPIShell(authenticator, limiter, logQueue, nil, time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC))

			req := httptest.NewRequest(http.MethodPost, "/__aipublic__/group/add", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer juis_plain")
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if authenticator.calls != 0 || limiter.calls != 0 {
				t.Fatalf("auth/limiter calls = %d/%d, want 0/0", authenticator.calls, limiter.calls)
			}
			log := singlePublicAPILog(t, logQueue)
			if log.RequestCaptureStatus != port.PublicAPILogCaptureDropped {
				t.Fatalf("request capture status = %q", log.RequestCaptureStatus)
			}
			body, ok := log.RequestData["body"].(map[string]any)
			if !ok || body["reason"] != tt.wantReason || body["dropped"] != true {
				t.Fatalf("request body snapshot = %#v", log.RequestData["body"])
			}
		})
	}
}

func TestPublicAPIShellOldPublicPath404DoesNotAuthButLogs(t *testing.T) {
	paths := []string{
		"/__aipublic__/demo/source-auth",
		"/__aipublic__/ip/usage?range=today",
		"/__aipublic__/account/usage?range=today",
		"/__aipublic__/consumption/ranking?range=today",
		"/__aipublic__/access/info",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			authenticator := &publicAPIShellAuthStub{ctx: publicAPIShellAuthContext()}
			limiter := &publicAPIShellLimiterStub{decision: publicapiratelimit.Decision{Allowed: true}}
			logQueue := &publicAPIShellLogQueueStub{}
			router := newTestPublicAPIShell(authenticator, limiter, logQueue, nil, time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC))

			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("Authorization", "Bearer juis_plain")
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d", rec.Code)
			}
			if authenticator.calls != 0 || limiter.calls != 0 {
				t.Fatalf("auth/limiter calls = %d/%d, want 0/0", authenticator.calls, limiter.calls)
			}
			log := singlePublicAPILog(t, logQueue)
			if log.ErrorMessage != "资源不存在" {
				t.Fatalf("log = %+v", log)
			}
		})
	}
}

func TestPublicAPIShellPOSTBodyIsAvailableToHandler(t *testing.T) {
	authenticator := &publicAPIShellAuthStub{ctx: publicAPIShellAuthContext()}
	limiter := &publicAPIShellLimiterStub{decision: publicapiratelimit.Decision{Allowed: true}}
	logQueue := &publicAPIShellLogQueueStub{}
	router := newTestPublicAPIShell(authenticator, limiter, logQueue, map[string]http.Handler{
		"group-add": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, ok := PublicAPIRequestBodyFromRequest(r)
			if !ok {
				t.Fatal("request body missing from context")
			}
			record, ok := body.(map[string]any)
			if !ok || record["targetUsername"] != "admin" {
				t.Fatalf("body = %#v", body)
			}
			writeJSON(w, http.StatusCreated, map[string]any{"data": map[string]any{"action": "created"}})
		}),
	}, time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC))

	req := httptest.NewRequest(http.MethodPost, "/__aipublic__/group/add", strings.NewReader(`{"targetUsername":"admin"}`))
	req.Header.Set("Authorization", "Bearer juis_plain")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d", rec.Code)
	}
	log := singlePublicAPILog(t, logQueue)
	if log.StatusCode == nil || *log.StatusCode != http.StatusCreated || !log.Success {
		t.Fatalf("log status/success = %v/%v", log.StatusCode, log.Success)
	}
	body, ok := log.RequestData["body"].(map[string]any)
	if !ok || body["targetUsername"] != "admin" {
		t.Fatalf("logged body = %#v", log.RequestData["body"])
	}
}

func TestPublicAPIShellLogQueueErrorDoesNotChangeResponse(t *testing.T) {
	authenticator := &publicAPIShellAuthStub{ctx: publicAPIShellAuthContext()}
	limiter := &publicAPIShellLimiterStub{decision: publicapiratelimit.Decision{Allowed: true}}
	logQueue := &publicAPIShellLogQueueStub{err: errors.New("redis down")}
	router := newTestPublicAPIShell(authenticator, limiter, logQueue, map[string]http.Handler{
		"group-list": http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"items": []any{}}})
		}),
	}, time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC))

	req := httptest.NewRequest(http.MethodGet, "/__aipublic__/group/list", nil)
	req.Header.Set("Authorization", "Bearer juis_plain")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(logQueue.logs) != 0 {
		t.Fatalf("logs = %d, want 0 because fake queue returned error", len(logQueue.logs))
	}
}

func TestPublicAPIShellNonJSONBodyDoesNotParseBeforeAuth(t *testing.T) {
	authenticator := &publicAPIShellAuthStub{ctx: publicAPIShellAuthContext()}
	limiter := &publicAPIShellLimiterStub{decision: publicapiratelimit.Decision{Allowed: true}}
	logQueue := &publicAPIShellLogQueueStub{}
	handlerCalled := false
	router := newTestPublicAPIShell(authenticator, limiter, logQueue, map[string]http.Handler{
		"group-add": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handlerCalled = true
			if body, ok := PublicAPIRequestBodyFromRequest(r); ok || body != nil {
				t.Fatalf("body = %#v, ok=%v; non-json body should not be parsed", body, ok)
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"ok": true}})
		}),
	}, time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC))

	req := httptest.NewRequest(http.MethodPost, "/__aipublic__/group/add", strings.NewReader("targetUsername=admin"))
	req.Header.Set("Authorization", "Bearer juis_plain")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !handlerCalled || authenticator.calls != 1 || limiter.calls != 1 {
		t.Fatalf("handler/auth/limiter = %v/%d/%d", handlerCalled, authenticator.calls, limiter.calls)
	}
	log := singlePublicAPILog(t, logQueue)
	if log.RequestCaptureStatus != port.PublicAPILogCaptureEmpty {
		t.Fatalf("request capture status = %q, want empty", log.RequestCaptureStatus)
	}
	if log.RequestSizeBytes != int64(len("targetUsername=admin")) {
		t.Fatalf("request size = %d", log.RequestSizeBytes)
	}
}

func TestPublicAPIShellCanceledRequestLogs499(t *testing.T) {
	authenticator := &publicAPIShellAuthStub{ctx: publicAPIShellAuthContext()}
	limiter := &publicAPIShellLimiterStub{decision: publicapiratelimit.Decision{Allowed: true}}
	logQueue := &publicAPIShellLogQueueStub{}
	ctx, cancel := context.WithCancel(context.Background())
	router := newTestPublicAPIShell(authenticator, limiter, logQueue, map[string]http.Handler{
		"group-list": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cancel()
			<-r.Context().Done()
		}),
	}, time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC))

	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/__aipublic__/group/list", nil)
	req.Header.Set("Authorization", "Bearer juis_plain")
	req.Header.Set("X-Request-Id", "trace_client_closed")
	req.Header.Set("X-Trace-Id", "trace_client_closed")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	log := singlePublicAPILog(t, logQueue)
	assertPublicAPIClientClosedLog(t, log)
	if log.TraceID != "trace_client_closed" || log.SourceRefID != "source_1" || log.TokenID != "token_1" {
		t.Fatalf("trace/source = %+v", log)
	}
	if status := publicAPILogResponseStatusCode(t, log); status != 499 {
		t.Fatalf("response snapshot status = %d, want 499", status)
	}
}

func TestPublicAPIShellWriteClosedErrorLogs499(t *testing.T) {
	authenticator := &publicAPIShellAuthStub{ctx: publicAPIShellAuthContext()}
	limiter := &publicAPIShellLimiterStub{decision: publicapiratelimit.Decision{Allowed: true}}
	logQueue := &publicAPIShellLogQueueStub{}
	router := newTestPublicAPIShell(authenticator, limiter, logQueue, map[string]http.Handler{
		"group-list": http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"items": []any{}}})
		}),
	}, time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC))

	req := httptest.NewRequest(http.MethodGet, "/__aipublic__/group/list", nil)
	req.Header.Set("Authorization", "Bearer juis_plain")
	writer := newPublicAPIShellClosedWriter()

	router.ServeHTTP(writer, req)

	log := singlePublicAPILog(t, logQueue)
	assertPublicAPIClientClosedLog(t, log)
	if writer.statusCode != http.StatusOK {
		t.Fatalf("underlying status = %d, want 200", writer.statusCode)
	}
}

func TestPublicAPIShellResponseWriterPreservesOptionalInterfaces(t *testing.T) {
	authenticator := &publicAPIShellAuthStub{ctx: publicAPIShellAuthContext()}
	limiter := &publicAPIShellLimiterStub{decision: publicapiratelimit.Decision{Allowed: true}}
	logQueue := &publicAPIShellLogQueueStub{}
	body := "streamed-response"
	router := newTestPublicAPIShell(authenticator, limiter, logQueue, map[string]http.Handler{
		"group-list": http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if _, ok := w.(http.Flusher); !ok {
				t.Fatal("response writer does not preserve http.Flusher")
			}
			if _, ok := w.(io.ReaderFrom); !ok {
				t.Fatal("response writer does not preserve io.ReaderFrom")
			}
			unwrapper, ok := w.(interface{ Unwrap() http.ResponseWriter })
			if !ok || unwrapper.Unwrap() == nil {
				t.Fatalf("response writer unwrap support = %v", ok)
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			if err := http.NewResponseController(w).Flush(); err != nil {
				t.Fatalf("response controller flush: %v", err)
			}
			w.(http.Flusher).Flush()
			if _, err := io.Copy(w, io.LimitReader(strings.NewReader(body), int64(len(body)))); err != nil {
				t.Fatalf("copy response: %v", err)
			}
		}),
	}, time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC))

	req := httptest.NewRequest(http.MethodGet, "/__aipublic__/group/list", nil)
	req.Header.Set("Authorization", "Bearer juis_plain")
	writer := newPublicAPIShellOptionalWriter()

	router.ServeHTTP(writer, req)

	if !writer.flushed || writer.readFromCalls != 1 {
		t.Fatalf("optional writer calls = flushed %v readFrom %d", writer.flushed, writer.readFromCalls)
	}
	log := singlePublicAPILog(t, logQueue)
	if log.StatusCode == nil || *log.StatusCode != http.StatusOK || !log.Success {
		t.Fatalf("log status/success = %v/%v", log.StatusCode, log.Success)
	}
	responseBody, ok := log.ResponseData["body"].(string)
	if !ok || responseBody != body {
		t.Fatalf("captured response body = %#v, want %q", log.ResponseData["body"], body)
	}
}

func TestPublicAPIShellFlushOnlyMarksResponseStarted(t *testing.T) {
	authenticator := &publicAPIShellAuthStub{ctx: publicAPIShellAuthContext()}
	limiter := &publicAPIShellLimiterStub{decision: publicapiratelimit.Decision{Allowed: true}}
	logQueue := &publicAPIShellLogQueueStub{}
	router := newTestPublicAPIShell(authenticator, limiter, logQueue, map[string]http.Handler{
		"group-list": http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if err := http.NewResponseController(w).Flush(); err != nil {
				t.Fatalf("response controller flush: %v", err)
			}
		}),
	}, time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC))

	req := httptest.NewRequest(http.MethodGet, "/__aipublic__/group/list", nil)
	req.Header.Set("Authorization", "Bearer juis_plain")
	writer := newPublicAPIShellOptionalWriter()

	router.ServeHTTP(writer, req)

	if !writer.flushed || writer.Code != http.StatusOK {
		t.Fatalf("flush writer = flushed %v code %d", writer.flushed, writer.Code)
	}
	log := singlePublicAPILog(t, logQueue)
	if log.StatusCode == nil || *log.StatusCode != http.StatusOK || !log.Success {
		t.Fatalf("log status/success = %v/%v", log.StatusCode, log.Success)
	}
	if log.ResponseSizeBytes != 0 || log.ResponseCaptureStatus != port.PublicAPILogCaptureEmpty {
		t.Fatalf("response capture = size %d status %q", log.ResponseSizeBytes, log.ResponseCaptureStatus)
	}
}

func newTestPublicAPIShell(
	authenticator PublicAPIAuthenticator,
	limiter PublicAPIRateLimiter,
	logQueue publicapilogjob.EnqueueClient,
	handlers map[string]http.Handler,
	now time.Time,
) http.Handler {
	ticks := 0
	return NewPublicAPIShell(PublicAPIShellOptions{
		Config:           config.Config{Host: "127.0.0.1", Port: 3000, TrustProxy: "true"},
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		Authenticator:    authenticator,
		RateLimiter:      limiter,
		LogClient:        logQueue,
		EndpointHandlers: handlers,
		Now: func() time.Time {
			ticks++
			return now.Add(time.Duration(ticks-1) * time.Millisecond)
		},
		NewLogID: func() string { return "publog_test_1" },
	})
}

func singlePublicAPILog(t *testing.T, queue *publicAPIShellLogQueueStub) port.PublicAPILogInput {
	t.Helper()

	if len(queue.logs) != 1 {
		t.Fatalf("queued logs = %d, want 1", len(queue.logs))
	}
	return queue.logs[0]
}

func assertPublicAPIClientClosedLog(t *testing.T, log port.PublicAPILogInput) {
	t.Helper()

	if log.StatusCode == nil || *log.StatusCode != 499 {
		t.Fatalf("status = %v, want 499", log.StatusCode)
	}
	if log.Success {
		t.Fatal("client closed log should not be success")
	}
	if log.ErrorCode != "public_api_client_closed" || log.ErrorMessage != "客户端连接提前关闭" {
		t.Fatalf("error = %s / %s", log.ErrorCode, log.ErrorMessage)
	}
}

func publicAPILogResponseStatusCode(t *testing.T, log port.PublicAPILogInput) int {
	t.Helper()

	switch value := log.ResponseData["statusCode"].(type) {
	case int:
		return value
	case float64:
		return int(value)
	default:
		t.Fatalf("response status code = %#v", log.ResponseData["statusCode"])
		return 0
	}
}

func publicAPIShellAuthContext() publicapiauth.AuthContext {
	return publicapiauth.AuthContext{
		SourceRefID: "source_1",
		SourceName:  "外部来源",
		TokenID:     "token_1",
		TokenName:   "来源 token",
		TokenPrefix: "juis_test",
		IsTestToken: true,
		RateLimits:  []port.PublicAPIRateLimitRule{{WindowSeconds: 60, MaxRequests: 10}},
	}
}

type publicAPIShellClosedWriter struct {
	header     http.Header
	statusCode int
}

func newPublicAPIShellClosedWriter() *publicAPIShellClosedWriter {
	return &publicAPIShellClosedWriter{header: http.Header{}}
}

func (w *publicAPIShellClosedWriter) Header() http.Header {
	return w.header
}

func (w *publicAPIShellClosedWriter) WriteHeader(statusCode int) {
	if w.statusCode == 0 {
		w.statusCode = statusCode
	}
}

func (w *publicAPIShellClosedWriter) Write(_ []byte) (int, error) {
	if w.statusCode == 0 {
		w.statusCode = http.StatusOK
	}
	return 0, net.ErrClosed
}

type publicAPIShellOptionalWriter struct {
	*httptest.ResponseRecorder
	flushed       bool
	readFromCalls int
}

func newPublicAPIShellOptionalWriter() *publicAPIShellOptionalWriter {
	return &publicAPIShellOptionalWriter{ResponseRecorder: httptest.NewRecorder()}
}

func (w *publicAPIShellOptionalWriter) Flush() {
	w.flushed = true
	w.ResponseRecorder.Flush()
}

func (w *publicAPIShellOptionalWriter) ReadFrom(src io.Reader) (int64, error) {
	w.readFromCalls++
	return io.Copy(w.ResponseRecorder, src)
}

func ptrPublicAPIAuthContext(value publicapiauth.AuthContext) *publicapiauth.AuthContext {
	return &value
}

type publicAPIShellAuthStub struct {
	ctx    publicapiauth.AuthContext
	err    error
	header string
	scope  string
	calls  int
}

func (s *publicAPIShellAuthStub) Authenticate(_ context.Context, header string, scope string) (publicapiauth.AuthContext, error) {
	s.calls++
	s.header = header
	s.scope = scope
	if s.err != nil {
		return publicapiauth.AuthContext{}, s.err
	}
	return s.ctx, nil
}

type publicAPIShellLimiterStub struct {
	decision publicapiratelimit.Decision
	err      error
	ctx      publicapiauth.AuthContext
	calls    int
}

func (s *publicAPIShellLimiterStub) Allow(_ context.Context, ctx publicapiauth.AuthContext) (publicapiratelimit.Decision, error) {
	s.calls++
	s.ctx = ctx
	if s.err != nil {
		return publicapiratelimit.Decision{}, s.err
	}
	return s.decision, nil
}

type publicAPIShellLogQueueStub struct {
	logs []port.PublicAPILogInput
	err  error
}

func (s *publicAPIShellLogQueueStub) Enqueue(_ context.Context, taskType string, payload []byte, opts queue.EnqueueOptions) (queue.TaskInfo, error) {
	if s.err != nil {
		return queue.TaskInfo{}, s.err
	}
	if taskType != publicapilogjob.TaskTypeWrite || opts.Queue != publicapilogjob.QueueName {
		return queue.TaskInfo{}, errors.New("unexpected public api log task")
	}
	log, err := publicapilogjob.DecodeWriteTaskPayload(payload)
	if err != nil {
		return queue.TaskInfo{}, err
	}
	s.logs = append(s.logs, log)
	return queue.TaskInfo{ID: "task_1", Queue: opts.Queue, Type: taskType}, nil
}

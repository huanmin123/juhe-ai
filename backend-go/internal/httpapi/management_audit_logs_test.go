package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/managementauditlogs"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/store/port"
)

func TestManagementAuditLogsHandlerParsesListAndRequiresAdmin(t *testing.T) {
	service := &managementAuditLogServiceStub{result: managementauditlogs.ListResult{Items: []managementauditlogs.Summary{}, Page: 1, PageSize: 100}}
	handler := newManagementAuditLogsHandler(service)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/audit-logs?path=POST%20%2Fv1%2Fresponses%3Fx%3D1&model=gpt-5&statusCode=503&outcome=upstream_failed&trafficSource=gateway", nil)
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || service.input.Path != "POST /v1/responses?x=1" || service.input.Model != "gpt-5" || service.input.StatusCode != 503 || !service.deadlineSeen {
		t.Fatalf("status=%d input=%+v body=%s", rec.Code, service.input, rec.Body.String())
	}

	service.called = false
	req = httptest.NewRequest(http.MethodGet, "/__aisys__/api/audit-logs", nil)
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "sys_user", Role: "user"}))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || service.called {
		t.Fatalf("status=%d called=%v", rec.Code, service.called)
	}
}

func TestManagementAuditLogsHandlerReturnsGenericDependencyError(t *testing.T) {
	service := &managementAuditLogServiceStub{err: errors.New("postgres password leaked")}
	handler := newManagementAuditLogsHandler(service)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/audit-logs", nil)
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "postgres") || !strings.Contains(rec.Body.String(), "服务器内部错误") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestManagementAuditLogDetailHandlerReturnsGenericDependencyError(t *testing.T) {
	service := &managementAuditLogServiceStub{err: errors.New("postgres password leaked")}
	handler := newManagementAuditLogsHandler(service)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/audit-logs/audit_1", nil)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("id", "audit_1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext))
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "postgres") || !strings.Contains(rec.Body.String(), "服务器内部错误") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestManagementAuditLogsHandlerKeepsFalseModelMappingApplied(t *testing.T) {
	service := &managementAuditLogServiceStub{result: managementauditlogs.ListResult{
		Items: []managementauditlogs.Summary{{
			ID: "audit_1", TraceID: "trace_1", TrafficSource: "gateway", Method: http.MethodPost,
			Path: "/v1/responses", AuditOutcome: "success", SampleReason: "sampled",
			CaptureStatus: "complete", StartedAt: "2026-07-21T00:00:00.000Z",
			EndedAt: "2026-07-21T00:00:01.000Z", CreatedAt: "2026-07-21T00:00:01.000Z",
		}},
		Page: 1, PageSize: 100,
	}}
	handler := newManagementAuditLogsHandler(service)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/audit-logs", nil)
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"modelMappingApplied":false`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestManagementAuditLogDetailHandlerReturnsDetailAndNotFound(t *testing.T) {
	service := &managementAuditLogServiceStub{
		detailResult: managementauditlogs.Detail{Summary: managementauditlogs.Summary{
			ID: "audit_1", TraceID: "trace_1", TrafficSource: "gateway", Method: "POST", Path: "/v1/responses",
			AuditOutcome: "success", SampleReason: "sampled", CaptureStatus: "complete",
			StartedAt: "2026-07-21T00:00:00.000Z", EndedAt: "2026-07-21T00:00:01.000Z", CreatedAt: "2026-07-21T00:00:01.000Z",
		}},
		detailFound: true,
	}
	handler := newManagementAuditLogsHandler(service)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/audit-logs/audit_1", nil)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("id", "audit_1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext))
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !service.detailCalled || service.detailID != "audit_1" {
		t.Fatalf("status=%d detailCalled=%v id=%q body=%s", rec.Code, service.detailCalled, service.detailID, rec.Body.String())
	}

	service.detailFound = false
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "审计日志不存在") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestManagementAuditErrorGroupsHandlerParsesListAndRequiresAdmin(t *testing.T) {
	service := &managementAuditLogServiceStub{errorGroupResult: managementauditlogs.ErrorGroupListResult{
		Items: []managementauditlogs.ErrorGroup{}, Page: 2, PageSize: 50,
	}}
	handler := newManagementAuditErrorGroupsHandler(service)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/audit-logs/error-groups?path=%EF%BB%BF%2Fv1%2Fresponses%EF%BB%BF&model=%EF%BB%BFgpt-5%EF%BB%BF&statusCode=503&systemAccountId=sys_1&apiKeyId=key_1&groupId=group_1&accountId=account_1&page=2&pageSize=50&traceId=ignored", nil)
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	want := managementauditlogs.ErrorGroupListInput{
		Path: "/v1/responses", Model: "gpt-5", StatusCode: 503,
		SystemAccountID: "sys_1", APIKeyID: "key_1", GroupID: "group_1", AccountID: "account_1",
		Page: 2, PageSize: 50, PageSizeProvided: true,
	}
	if rec.Code != http.StatusOK || service.errorGroupInput != want {
		t.Fatalf("status=%d input=%+v want=%+v body=%s", rec.Code, service.errorGroupInput, want, rec.Body.String())
	}
	assertManagementAuditReadResponse(t, rec, service.deadline)

	service.errorGroupCalled = false
	req = httptest.NewRequest(http.MethodGet, "/__aisys__/api/audit-logs/error-groups", nil)
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "sys_user", Role: "user"}))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || service.errorGroupCalled {
		t.Fatalf("status=%d called=%v body=%s", rec.Code, service.errorGroupCalled, rec.Body.String())
	}
}

func TestManagementAuditErrorGroupsHandlerReturnsGenericDependencyError(t *testing.T) {
	service := &managementAuditLogServiceStub{err: errors.New("postgres password leaked")}
	handler := newManagementAuditErrorGroupsHandler(service)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/audit-logs/error-groups", nil)
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "postgres") || !strings.Contains(rec.Body.String(), "服务器内部错误") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestManagementAuditErrorGroupEventsHandlerPassesRouteIDAndListQuery(t *testing.T) {
	service := &managementAuditLogServiceStub{eventResult: managementauditlogs.ListResult{
		Items: []managementauditlogs.Summary{}, Page: 3, PageSize: 25,
	}}
	handler := newManagementAuditErrorGroupEventsHandler(service)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/audit-logs/error-groups/route_group/events?traceId=trace_1&errorGroupId=query_group&outcome=upstream_failed&statusCode=502&path=POST%20%2Fv1%2Fresponses%3Fx%3D1&model=gpt-5&systemAccountId=sys_1&apiKeyId=key_1&groupId=group_1&accountId=account_1&clientIp=203.0.113.1&startAt=2026-07-21T00%3A00%3A00Z&endAt=2026-07-22T00%3A00%3A00Z&trafficSource=gateway&page=3&pageSize=25", nil)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("errorGroupId", "route_group")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext))
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "sys_admin", Role: "super_admin"}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	want := managementauditlogs.ListInput{
		TraceID: "trace_1", ErrorGroupID: "query_group", Outcome: "upstream_failed", StatusCode: 502,
		Path: "POST /v1/responses?x=1", Model: "gpt-5", SystemAccountID: "sys_1", APIKeyID: "key_1",
		GroupID: "group_1", AccountID: "account_1", ClientIP: "203.0.113.1",
		StartAt: "2026-07-21T00:00:00Z", EndAt: "2026-07-22T00:00:00Z", TrafficSource: "gateway",
		Page: 3, PageSize: 25, PageSizeProvided: true,
	}
	if rec.Code != http.StatusOK || service.eventErrorGroupID != "route_group" || service.eventInput != want {
		t.Fatalf("status=%d routeID=%q input=%+v want=%+v body=%s", rec.Code, service.eventErrorGroupID, service.eventInput, want, rec.Body.String())
	}
	assertManagementAuditReadResponse(t, rec, service.deadline)
}

func TestManagementAuditErrorGroupEventsHandlerReturnsGenericDependencyError(t *testing.T) {
	service := &managementAuditLogServiceStub{err: errors.New("postgres password leaked")}
	handler := newManagementAuditErrorGroupEventsHandler(service)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/audit-logs/error-groups/err_1/events", nil)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("errorGroupId", "err_1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext))
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "postgres") || !strings.Contains(rec.Body.String(), "服务器内部错误") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRouterRegistersManagementAuditLogAndErrorGroupReads(t *testing.T) {
	service := &managementAuditLogServiceStub{
		result:           managementauditlogs.ListResult{Items: []managementauditlogs.Summary{}, Page: 1, PageSize: 100},
		errorGroupResult: managementauditlogs.ErrorGroupListResult{Items: []managementauditlogs.ErrorGroup{}, Page: 1, PageSize: 100},
		eventResult:      managementauditlogs.ListResult{Items: []managementauditlogs.Summary{}, Page: 1, PageSize: 100},
		detailResult:     managementauditlogs.Detail{Summary: managementauditlogs.Summary{ID: "audit_1"}},
		detailFound:      true,
	}
	router := NewRouter(RouterOptions{
		Config:                                 config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                                 slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementAuditLogsHandler:             newManagementAuditLogsHandler(service),
		ManagementAuditErrorGroupsHandler:      newManagementAuditErrorGroupsHandler(service),
		ManagementAuditErrorGroupEventsHandler: newManagementAuditErrorGroupEventsHandler(service),
		ManagementAPIAuthMiddleware:            NewManagementAPIAuthMiddleware(managementPublicAPILogAuthenticatorStub{authContext: managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"}}),
	})
	for path, want := range map[string]int{
		"/__aisys__/api/audit-logs":                            http.StatusOK,
		"/__aisys__/api/audit-logs/runtime":                    http.StatusNotFound,
		"/__aisys__/api/audit-logs/search-hot":                 http.StatusNotFound,
		"/__aisys__/api/audit-logs/audit_1":                    http.StatusOK,
		"/__aisys__/api/audit-logs/error-groups":               http.StatusOK,
		"/__aisys__/api/audit-logs/audit_1/payloads/payload_1": http.StatusNotFound,
		"/__aisys__/api/audit-logs/error-groups/err_1/events":  http.StatusOK,
		"/__aisys__/api/audit-logs/error-groups/err_1":         http.StatusNotFound,
		"/__aisys__/api/audit-logs/not-migrated/child":         http.StatusNotFound,
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Cookie", "juhe_ai_session=session-token")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("%s status=%d want=%d body=%s", path, rec.Code, want, rec.Body.String())
		}
	}
	if service.detailCalls != 1 || service.errorGroupCalls != 1 || service.eventCalls != 1 {
		t.Fatalf("detail=%d errorGroups=%d events=%d", service.detailCalls, service.errorGroupCalls, service.eventCalls)
	}
}

func TestRouterRegistersManagementAuditErrorGroupReadsWithAuthenticationAndRateLimit(t *testing.T) {
	service := &managementAuditLogServiceStub{
		errorGroupResult: managementauditlogs.ErrorGroupListResult{Items: []managementauditlogs.ErrorGroup{}, Page: 1, PageSize: 100},
		eventResult:      managementauditlogs.ListResult{Items: []managementauditlogs.Summary{}, Page: 1, PageSize: 100},
	}
	userLimiter := &systemAPIAuthenticatedRateLimiterStub{decision: SystemAPIRateLimitDecision{Allowed: true}}
	adminAuthenticator := &managementAPIAuthenticatorStub{context: managementauth.Context{
		SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin",
	}}
	router := NewRouter(RouterOptions{
		Config:                                 config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                                 slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		SystemAPIRateLimitReader:               systemAPIRateLimitReaderStub{settings: port.SystemAPIRateLimitSettings{IPReadPerMinute: 600, IPReadBurstPer10Seconds: 120, UserReadPerMinute: 300}},
		SystemAPIIPRateLimiter:                 &publicSettingsRateLimiterStub{decision: SystemAPIRateLimitDecision{Allowed: true}},
		SystemAPIAuthenticatedRateLimiter:      userLimiter,
		ManagementAPIAuthMiddleware:            NewManagementAPIAuthMiddleware(adminAuthenticator),
		ManagementAuditErrorGroupsHandler:      newManagementAuditErrorGroupsHandler(service),
		ManagementAuditErrorGroupEventsHandler: newManagementAuditErrorGroupEventsHandler(service),
	})
	for _, path := range []string{
		"/__aisys__/api/audit-logs/error-groups",
		"/__aisys__/api/audit-logs/error-groups/err_1/events",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Cookie", "juhe_ai_session=session-token")
		rec := httptest.NewRecorder()
		beforeLimiterCalls := userLimiter.calls
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, rec.Code, rec.Body.String())
		}
		if userLimiter.calls != beforeLimiterCalls+1 {
			t.Fatalf("%s limiter calls=%d want %d", path, userLimiter.calls, beforeLimiterCalls+1)
		}
	}
	if userLimiter.calls != 2 || userLimiter.limit != 300 || service.errorGroupCalls != 1 || service.eventCalls != 1 {
		t.Fatalf("limiter calls=%d limit=%d errorGroups=%d events=%d", userLimiter.calls, userLimiter.limit, service.errorGroupCalls, service.eventCalls)
	}

	userService := &managementAuditLogServiceStub{}
	userLimiter = &systemAPIAuthenticatedRateLimiterStub{decision: SystemAPIRateLimitDecision{Allowed: true}}
	router = NewRouter(RouterOptions{
		Config:                                 config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                                 slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		SystemAPIRateLimitReader:               systemAPIRateLimitReaderStub{settings: port.SystemAPIRateLimitSettings{UserReadPerMinute: 300}},
		SystemAPIIPRateLimiter:                 &publicSettingsRateLimiterStub{decision: SystemAPIRateLimitDecision{Allowed: true}},
		SystemAPIAuthenticatedRateLimiter:      userLimiter,
		ManagementAPIAuthMiddleware:            NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{context: managementauth.Context{SystemAccountID: "sys_user", Role: "user", SessionID: "sess_user"}}),
		ManagementAuditErrorGroupsHandler:      newManagementAuditErrorGroupsHandler(userService),
		ManagementAuditErrorGroupEventsHandler: newManagementAuditErrorGroupEventsHandler(userService),
	})
	for _, path := range []string{
		"/__aisys__/api/audit-logs/error-groups",
		"/__aisys__/api/audit-logs/error-groups/err_1/events",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Cookie", "juhe_ai_session=session-token")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s user status=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}
	if userLimiter.calls != 2 || userService.errorGroupCalls != 0 || userService.eventCalls != 0 {
		t.Fatalf("user limiter=%d errorGroups=%d events=%d", userLimiter.calls, userService.errorGroupCalls, userService.eventCalls)
	}

	anonymousService := &managementAuditLogServiceStub{}
	router = NewRouter(RouterOptions{
		Config:                                 config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                                 slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementAPIAuthMiddleware:            NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{err: &managementauth.AuthError{StatusCode: http.StatusUnauthorized, Message: "请先登录"}}),
		ManagementAuditErrorGroupsHandler:      newManagementAuditErrorGroupsHandler(anonymousService),
		ManagementAuditErrorGroupEventsHandler: newManagementAuditErrorGroupEventsHandler(anonymousService),
	})
	for _, path := range []string{
		"/__aisys__/api/audit-logs/error-groups",
		"/__aisys__/api/audit-logs/error-groups/err_1/events",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s anonymous status=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}
	if anonymousService.errorGroupCalls != 0 || anonymousService.eventCalls != 0 {
		t.Fatalf("anonymous errorGroups=%d events=%d", anonymousService.errorGroupCalls, anonymousService.eventCalls)
	}
}

type managementAuditLogServiceStub struct {
	called            bool
	input             managementauditlogs.ListInput
	result            managementauditlogs.ListResult
	detailCalled      bool
	detailCalls       int
	detailID          string
	detailResult      managementauditlogs.Detail
	detailFound       bool
	errorGroupCalled  bool
	errorGroupCalls   int
	errorGroupInput   managementauditlogs.ErrorGroupListInput
	errorGroupResult  managementauditlogs.ErrorGroupListResult
	eventCalled       bool
	eventCalls        int
	eventErrorGroupID string
	eventInput        managementauditlogs.ListInput
	eventResult       managementauditlogs.ListResult
	deadlineSeen      bool
	deadline          time.Time
	err               error
}

func (s *managementAuditLogServiceStub) Detail(r *http.Request, id string) (managementauditlogs.Detail, bool, error) {
	s.detailCalled = true
	s.detailCalls++
	s.detailID = id
	s.deadline, s.deadlineSeen = r.Context().Deadline()
	return s.detailResult, s.detailFound, s.err
}

func (s *managementAuditLogServiceStub) List(r *http.Request, input managementauditlogs.ListInput) (managementauditlogs.ListResult, error) {
	s.called = true
	s.input = input
	s.deadline, s.deadlineSeen = r.Context().Deadline()
	return s.result, s.err
}

func (s *managementAuditLogServiceStub) ListErrorGroups(r *http.Request, input managementauditlogs.ErrorGroupListInput) (managementauditlogs.ErrorGroupListResult, error) {
	s.errorGroupCalled = true
	s.errorGroupCalls++
	s.errorGroupInput = input
	s.deadline, s.deadlineSeen = r.Context().Deadline()
	return s.errorGroupResult, s.err
}

func (s *managementAuditLogServiceStub) ListErrorGroupEvents(r *http.Request, errorGroupID string, input managementauditlogs.ListInput) (managementauditlogs.ListResult, error) {
	s.eventCalled = true
	s.eventCalls++
	s.eventErrorGroupID = errorGroupID
	s.eventInput = input
	s.deadline, s.deadlineSeen = r.Context().Deadline()
	return s.eventResult, s.err
}

func assertManagementAuditReadResponse(t *testing.T, rec *httptest.ResponseRecorder, deadline time.Time) {
	t.Helper()
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control=%q want no-store", got)
	}
	if got := rec.Header().Get("Pragma"); got != "no-cache" {
		t.Fatalf("Pragma=%q want no-cache", got)
	}
	remaining := time.Until(deadline)
	if deadline.IsZero() || remaining < 118*time.Second || remaining > managementAuditLogRequestTimeout {
		t.Fatalf("deadline=%v remaining=%v want about 120s", deadline, remaining)
	}
}

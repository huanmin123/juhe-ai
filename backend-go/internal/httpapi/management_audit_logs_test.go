package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/managementauditlogs"
	"juhe-ai/backend-go/internal/modules/managementauth"
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

func TestManagementAuditLogsHandlerSearchHotParsesRepeatedKeywords(t *testing.T) {
	service := &managementAuditLogServiceStub{hotResult: managementauditlogs.HotSearchResult{
		Items: []managementauditlogs.Summary{}, Page: 1, PageSize: 25, Available: true,
		Keywords: []string{"needle", "other"}, Limit: 25,
	}}
	handler := newManagementAuditLogsHandler(service)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/audit-logs/search-hot?keywords=needle&keywords=other&limit=25.8&startAt=2026-07-22T09%3A00%3A00Z", nil)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("id", "search-hot")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext))
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !service.hotCalled || len(service.hotInput.Keywords) != 2 || service.hotInput.Limit != 25 || service.hotInput.StartAt == "" {
		t.Fatalf("status=%d input=%+v body=%s", rec.Code, service.hotInput, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" || !strings.Contains(rec.Body.String(), `"available":true`) {
		t.Fatalf("headers=%v body=%s", rec.Header(), rec.Body.String())
	}
}

func TestManagementAuditHotSearchLimitMatchesNodeFiniteNumberRules(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		query    string
		want     int
		provided bool
	}{
		{name: "missing", want: 0, provided: false},
		{name: "explicit zero", query: "limit=0", want: 0, provided: true},
		{name: "negative decimal", query: "limit=-1.8", want: -1, provided: true},
		{name: "hex", query: "limit=0x10", want: 16, provided: true},
		{name: "invalid", query: "limit=oops", want: 0, provided: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			values, err := url.ParseQuery(testCase.query)
			if err != nil {
				t.Fatal(err)
			}
			got, provided := managementAuditHotSearchLimit(values)
			if got != testCase.want || provided != testCase.provided {
				t.Fatalf("managementAuditHotSearchLimit(%q) = (%d, %v), want (%d, %v)", testCase.query, got, provided, testCase.want, testCase.provided)
			}
		})
	}
}

func TestRouterRegistersManagementAuditLogListAndDetailOnly(t *testing.T) {
	service := &managementAuditLogServiceStub{
		result:       managementauditlogs.ListResult{Items: []managementauditlogs.Summary{}, Page: 1, PageSize: 100},
		detailResult: managementauditlogs.Detail{Summary: managementauditlogs.Summary{ID: "audit_1"}},
		detailFound:  true,
	}
	router := NewRouter(RouterOptions{
		Config:                      config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                      slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementAuditLogsHandler:  newManagementAuditLogsHandler(service),
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(managementPublicAPILogAuthenticatorStub{authContext: managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"}}),
	})
	for path, want := range map[string]int{
		"/__aisys__/api/audit-logs":                            http.StatusOK,
		"/__aisys__/api/audit-logs/runtime":                    http.StatusNotFound,
		"/__aisys__/api/audit-logs/search-hot":                 http.StatusOK,
		"/__aisys__/api/audit-logs/audit_1":                    http.StatusOK,
		"/__aisys__/api/audit-logs/error-groups":               http.StatusNotFound,
		"/__aisys__/api/audit-logs/audit_1/payloads/payload_1": http.StatusNotFound,
		"/__aisys__/api/audit-logs/error-groups/err_1/events":  http.StatusNotFound,
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Cookie", "juhe_ai_session=session-token")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("%s status=%d want=%d body=%s", path, rec.Code, want, rec.Body.String())
		}
	}
}

type managementAuditLogServiceStub struct {
	called       bool
	input        managementauditlogs.ListInput
	result       managementauditlogs.ListResult
	detailCalled bool
	detailID     string
	detailResult managementauditlogs.Detail
	detailFound  bool
	deadlineSeen bool
	err          error
	hotCalled    bool
	hotInput     managementauditlogs.HotSearchInput
	hotResult    managementauditlogs.HotSearchResult
}

func (s *managementAuditLogServiceStub) HotSearch(r *http.Request, input managementauditlogs.HotSearchInput) (managementauditlogs.HotSearchResult, error) {
	s.hotCalled = true
	s.hotInput = input
	_, s.deadlineSeen = r.Context().Deadline()
	return s.hotResult, s.err
}

func (s *managementAuditLogServiceStub) Detail(r *http.Request, id string) (managementauditlogs.Detail, bool, error) {
	s.detailCalled = true
	s.detailID = id
	_, s.deadlineSeen = r.Context().Deadline()
	return s.detailResult, s.detailFound, s.err
}

func (s *managementAuditLogServiceStub) List(r *http.Request, input managementauditlogs.ListInput) (managementauditlogs.ListResult, error) {
	s.called = true
	s.input = input
	_, s.deadlineSeen = r.Context().Deadline()
	return s.result, s.err
}

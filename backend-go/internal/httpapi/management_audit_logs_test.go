package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	if rec.Code != http.StatusOK || service.input.Path != "POST /v1/responses?x=1" || service.input.Model != "gpt-5" || service.input.StatusCode != 503 {
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

func TestRouterRegistersOnlyManagementAuditLogListRoute(t *testing.T) {
	service := &managementAuditLogServiceStub{result: managementauditlogs.ListResult{Items: []managementauditlogs.Summary{}, Page: 1, PageSize: 100}}
	router := NewRouter(RouterOptions{
		Config:                      config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                      slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementAuditLogsHandler:  newManagementAuditLogsHandler(service),
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(managementPublicAPILogAuthenticatorStub{authContext: managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"}}),
	})
	for path, want := range map[string]int{
		"/__aisys__/api/audit-logs":            http.StatusOK,
		"/__aisys__/api/audit-logs/runtime":    http.StatusNotFound,
		"/__aisys__/api/audit-logs/search-hot": http.StatusNotFound,
		"/__aisys__/api/audit-logs/audit_1":    http.StatusNotFound,
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
	called bool
	input  managementauditlogs.ListInput
	result managementauditlogs.ListResult
	err    error
}

func (s *managementAuditLogServiceStub) List(_ *http.Request, input managementauditlogs.ListInput) (managementauditlogs.ListResult, error) {
	s.called = true
	s.input = input
	return s.result, s.err
}

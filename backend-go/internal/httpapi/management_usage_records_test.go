package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementusagerecords"
)

func TestManagementMyUsageRecordsHandlerForcesSelfScope(t *testing.T) {
	service := &managementUsageRecordServiceStub{listResult: managementusagerecords.ListResult{Items: []managementusagerecords.Summary{}, Page: 1, PageSize: 50}}
	handler := newManagementUsageRecordsHandler(service, managementUsageRecordScopeSelf)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-usage-records?systemAccountId=sys_other&page=2&pageSize=20", nil)
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "sys_user", Role: "user"}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", rec.Code, rec.Body.String())
	}
	if service.listInput.ScopeSystemAccountID != "sys_user" || service.listInput.IncludeSystemAccount || service.listInput.Page != 2 || service.listInput.PageSize != 20 || !service.listInput.PageSizeProvided {
		t.Fatalf("input = %+v", service.listInput)
	}
}

func TestManagementUsageRecordsHandlerRequiresAccountBeforeFilteringAllAccounts(t *testing.T) {
	service := &managementUsageRecordServiceStub{}
	handler := newManagementUsageRecordsHandler(service, managementUsageRecordScopeAdmin)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/usage-records?model=gpt-5.5", nil)
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "请先选择系统账户后筛选") || service.listCalled {
		t.Fatalf("status = %d called = %v body = %s", rec.Code, service.listCalled, rec.Body.String())
	}
}

func TestRouterRegistersManagementAndSelfUsageRecordRoutes(t *testing.T) {
	service := &managementUsageRecordServiceStub{listResult: managementusagerecords.ListResult{Items: []managementusagerecords.Summary{}, Page: 1, PageSize: 50}}
	router := NewRouter(RouterOptions{
		Config:                          config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                          slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementUsageRecordsHandler:   newManagementUsageRecordsHandler(service, managementUsageRecordScopeAdmin),
		ManagementMyUsageRecordsHandler: newManagementUsageRecordsHandler(service, managementUsageRecordScopeSelf),
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(managementPublicAPILogAuthenticatorStub{
			authContext: managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"},
		}),
	})
	for _, path := range []string{"/__aisys__/api/usage-records", "/__aisys__/api/my-usage-records"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Cookie", "juhe_ai_session=session-token")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d; body = %s", path, rec.Code, rec.Body.String())
		}
	}
}

type managementUsageRecordServiceStub struct {
	listCalled  bool
	listInput   managementusagerecords.ListInput
	listResult  managementusagerecords.ListResult
	detailInput managementusagerecords.DetailInput
}

func (s *managementUsageRecordServiceStub) List(_ *http.Request, input managementusagerecords.ListInput) (managementusagerecords.ListResult, error) {
	s.listCalled = true
	s.listInput = input
	return s.listResult, nil
}

func (s *managementUsageRecordServiceStub) Detail(_ *http.Request, input managementusagerecords.DetailInput) (managementusagerecords.Summary, bool, error) {
	s.detailInput = input
	return managementusagerecords.Summary{}, false, nil
}

package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"juhe-ai/backend-go/internal/modules/managementaccountlist"
	"juhe-ai/backend-go/internal/modules/managementauth"
)

func TestManagementAccountListHandlerScopesAndParsesQuery(t *testing.T) {
	service := &accountListServiceStub{result: managementaccountlist.Result{Items: []managementaccountlist.Item{}}}
	handler := newManagementAccountListHandler(service, managementAccountListScopeSelf)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-accounts?systemAccountId=sys_other&page=2&pageSize=25&status=active,bad&sorts=priority:desc,credentials:asc", nil)
	req = requestWithManagementAuthContext(req, managementauth.Context{SystemAccountID: "sys_user", Role: "user"})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if service.input.SystemAccountID != "sys_user" || !service.input.SelfOnly || service.input.Page != 2 || service.input.PageSize != 25 {
		t.Fatalf("input = %+v", service.input)
	}
	if len(service.input.Statuses) != 1 || service.input.Statuses[0] != "active" || len(service.input.Sorts) != 1 {
		t.Fatalf("filters/sorts = %+v / %+v", service.input.Statuses, service.input.Sorts)
	}
}

func TestManagementAccountListHandlerRejectsNonAdmin(t *testing.T) {
	service := &accountListServiceStub{}
	handler := newManagementAccountListHandler(service, managementAccountListScopeAdmin)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/accounts", nil)
	req = requestWithManagementAuthContext(req, managementauth.Context{SystemAccountID: "sys_user", Role: "user"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || service.calls != 0 {
		t.Fatalf("status=%d calls=%d", rec.Code, service.calls)
	}
}

type accountListServiceStub struct {
	input  managementaccountlist.Input
	result managementaccountlist.Result
	err    error
	calls  int
}

func (s *accountListServiceStub) List(_ *http.Request, input managementaccountlist.Input) (managementaccountlist.Result, error) {
	s.calls++
	s.input = input
	return s.result, s.err
}

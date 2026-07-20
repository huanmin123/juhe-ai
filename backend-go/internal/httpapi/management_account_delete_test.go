package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/config"
	operationlogjob "juhe-ai/backend-go/internal/jobs/operationlog"
	"juhe-ai/backend-go/internal/modules/managementaccountdelete"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/store/port"
)

func TestManagementAccountDeleteHandlerBuildsAdminScopeAndOperationLog(t *testing.T) {
	now := time.Date(2026, time.July, 20, 13, 0, 0, 0, time.UTC)
	queue := &operationLogQueueStub{}
	service := &managementAccountDeleteServiceStub{result: managementaccountdelete.DeleteResult{
		Before:            port.ManagementAccountDeleteSummary{ID: "acc_1", SystemAccountID: "sys_owner", Name: "生产账户"},
		DeletedAccountIDs: []string{"acc_1", "acc_instance"},
	}}
	handler := newManagementAccountDeleteHandler(service, managementAccountScopeAdmin, newManagementOperationLogOptions(ManagementOperationLogOptions{
		Config: config.Config{TrustProxy: "false"}, Client: queue, Now: func() time.Time { return now }, NewLogID: func() string { return "oplog_account_delete" },
	}))
	req := httptest.NewRequest(http.MethodDelete, "/__aisys__/api/accounts/acc_1?systemAccountId=sys_owner", nil)
	req = requestWithManagementAccountDeleteID(req, "acc_1")
	req = requestWithManagementAuthContext(req, managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin"})
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "req_account_delete"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if service.calls != 1 || service.input.SystemAccountID != "sys_owner" || service.input.SelfOnly || service.input.AccountID != "acc_1" {
		t.Fatalf("service input=%+v calls=%d", service.input, service.calls)
	}
	logInput, err := operationlogjob.DecodeWriteTaskPayload(queue.payload)
	if err != nil {
		t.Fatalf("decode operation log: %v", err)
	}
	if logInput.ID != "oplog_account_delete" || logInput.TraceID != "req_account_delete" || logInput.OperationScopeSystemAccountID != "sys_owner" || logInput.Mode != "admin" || logInput.Module != "accounts" || logInput.Action != "delete" || logInput.OperationKey != "accounts.delete" || logInput.ResourceType != "account" || logInput.ResourceID != "acc_1" || logInput.ResourceName != "生产账户" || logInput.Summary != "删除 AI 账户：生产账户" || len(logInput.Changes) != 1 || logInput.Changes[0].Field != "deleted" || logInput.Changes[0].Before != false || logInput.Changes[0].After != true || len(logInput.Viewers) != 1 || logInput.Viewers[0].SystemAccountID != "sys_owner" || !logInput.CreatedAt.Equal(now) {
		t.Fatalf("operation log=%+v", logInput)
	}
}

func TestManagementMyAccountDeleteHandlerForcesSelfScope(t *testing.T) {
	service := &managementAccountDeleteServiceStub{result: managementaccountdelete.DeleteResult{Before: port.ManagementAccountDeleteSummary{ID: "acc_self", SystemAccountID: "sys_self", Name: "个人账户"}}}
	handler := newManagementAccountDeleteHandler(service, managementAccountScopeSelf, managementOperationLogOptions{})
	req := httptest.NewRequest(http.MethodDelete, "/__aisys__/api/my-accounts/acc_self?systemAccountId=sys_other", nil)
	req = requestWithManagementAccountDeleteID(req, "acc_self")
	req = requestWithManagementAuthContext(req, managementauth.Context{SystemAccountID: "sys_self", Role: "user"})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent || !service.input.SelfOnly || service.input.SystemAccountID != "" || service.input.ActorSystemAccountID != "sys_self" {
		t.Fatalf("status=%d input=%+v body=%s", rec.Code, service.input, rec.Body.String())
	}
}

func TestManagementAccountDeleteHandlerMapsErrors(t *testing.T) {
	tests := []struct {
		name       string
		scope      managementAccountOptionScope
		auth       managementauth.Context
		query      string
		serviceErr error
		wantStatus int
		wantText   string
	}{
		{name: "admin forbidden", scope: managementAccountScopeAdmin, auth: managementauth.Context{SystemAccountID: "sys_user", Role: "user"}, wantStatus: http.StatusForbidden, wantText: "需要管理员权限"},
		{name: "repeated owner query", scope: managementAccountScopeAdmin, auth: managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"}, query: "?systemAccountId=a&systemAccountId=b", wantStatus: http.StatusBadRequest, wantText: "Expected string, received array"},
		{name: "not found", scope: managementAccountScopeSelf, auth: managementauth.Context{SystemAccountID: "sys_user", Role: "user"}, serviceErr: managementaccountdelete.ErrAccountNotFound, wantStatus: http.StatusNotFound, wantText: "账户不存在"},
		{name: "authorization instance", scope: managementAccountScopeSelf, auth: managementauth.Context{SystemAccountID: "sys_user", Role: "user"}, serviceErr: managementaccountdelete.ErrAuthorizationInstance, wantStatus: http.StatusBadRequest, wantText: "授权账户请使用归还操作"},
		{name: "internal", scope: managementAccountScopeSelf, auth: managementauth.Context{SystemAccountID: "sys_user", Role: "user"}, serviceErr: errors.New("db failure"), wantStatus: http.StatusInternalServerError, wantText: "服务器内部错误"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementAccountDeleteServiceStub{err: tt.serviceErr}
			handler := newManagementAccountDeleteHandler(service, tt.scope, managementOperationLogOptions{})
			req := httptest.NewRequest(http.MethodDelete, "/__aisys__/api/accounts/acc_1"+tt.query, nil)
			req = requestWithManagementAccountDeleteID(req, "acc_1")
			req = requestWithManagementAuthContext(req, tt.auth)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus || !strings.Contains(rec.Body.String(), tt.wantText) {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

type managementAccountDeleteServiceStub struct {
	input  managementaccountdelete.DeleteInput
	result managementaccountdelete.DeleteResult
	err    error
	calls  int
}

func (s *managementAccountDeleteServiceStub) Delete(_ *http.Request, input managementaccountdelete.DeleteInput) (managementaccountdelete.DeleteResult, error) {
	s.calls++
	s.input = input
	return s.result, s.err
}

func requestWithManagementAccountDeleteID(req *http.Request, id string) *http.Request {
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("id", id)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext))
}

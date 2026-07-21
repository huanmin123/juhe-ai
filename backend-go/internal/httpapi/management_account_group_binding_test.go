package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	operationlogjob "juhe-ai/backend-go/internal/jobs/operationlog"
	"juhe-ai/backend-go/internal/modules/managementaccountgroupbinding"
	"juhe-ai/backend-go/internal/modules/managementauth"
)

func TestManagementAccountGroupBindingHandlerAdminScopeAndResponse(t *testing.T) {
	service := &managementAccountGroupBindingServiceStub{result: bindingHTTPResult()}
	handler := newManagementAccountGroupBindingHandler(service, managementAccountGroupBindingScopeAdmin)
	req := managementAccountGroupBindingRequest("/__aisys__/api/accounts/acct_main/group?systemAccountId=sys_owner", "acct_main", `{"groupId":" group_new "}`)
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if service.input.ActorSystemAccountID != "sys_admin" || service.input.SystemAccountID != "sys_owner" || service.input.SelfOnly ||
		service.input.AccountID != "acct_main" || service.input.GroupID != " group_new " {
		t.Fatalf("service input = %+v", service.input)
	}
	var body struct {
		Data managementaccountgroupbinding.Account `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Data.ID != "acct_main" || body.Data.BoundGroupID != "group_new" {
		t.Fatalf("response = %+v", body.Data)
	}
}

func TestManagementMyAccountGroupBindingHandlerForcesSelfScope(t *testing.T) {
	service := &managementAccountGroupBindingServiceStub{result: bindingHTTPResult()}
	handler := newManagementAccountGroupBindingHandler(service, managementAccountGroupBindingScopeSelf)
	req := managementAccountGroupBindingRequest("/__aisys__/api/my-accounts/acct_main/group?systemAccountId=sys_other", "acct_main", `{"groupId":"group_new"}`)
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "sys_self", Role: "user"}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !service.input.SelfOnly || service.input.SystemAccountID != "" {
		t.Fatalf("status=%d input=%+v body=%s", rec.Code, service.input, rec.Body.String())
	}
}

func TestManagementAccountGroupBindingHandlerValidation(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		body        string
		role        string
		err         error
		wantStatus  int
		wantMessage string
	}{
		{name: "admin required", path: "/__aisys__/api/accounts/acct_main/group", body: `{"groupId":"group_new"}`, role: "user", wantStatus: 403, wantMessage: "需要管理员权限"},
		{name: "blank scope", path: "/__aisys__/api/accounts/acct_main/group?systemAccountId=", body: `{"groupId":"group_new"}`, role: "admin", wantStatus: 400, wantMessage: "查询参数不合法"},
		{name: "missing group", path: "/__aisys__/api/accounts/acct_main/group", body: `{}`, role: "admin", wantStatus: 400, wantMessage: "绑定分组参数无效"},
		{name: "unknown field", path: "/__aisys__/api/accounts/acct_main/group", body: `{"groupId":"group_new","extra":true}`, role: "admin", wantStatus: 400, wantMessage: "绑定分组参数无效"},
		{name: "trailing token", path: "/__aisys__/api/accounts/acct_main/group", body: `{"groupId":"group_new"} true`, role: "admin", wantStatus: 400, wantMessage: "绑定分组参数无效"},
		{name: "binding rejection", path: "/__aisys__/api/accounts/acct_main/group", body: `{"groupId":"group_new"}`, role: "admin", err: managementaccountgroupbinding.ErrBindingRejected, wantStatus: 400, wantMessage: "账户不存在、授权已失效或分组不可用"},
		{name: "store error kept", path: "/__aisys__/api/accounts/acct_main/group", body: `{"groupId":"group_new"}`, role: "admin", err: errors.New("postgres connection failed"), wantStatus: 400, wantMessage: "postgres connection failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementAccountGroupBindingServiceStub{result: bindingHTTPResult(), err: tt.err}
			handler := newManagementAccountGroupBindingHandler(service, managementAccountGroupBindingScopeAdmin)
			req := managementAccountGroupBindingRequest(tt.path, "acct_main", tt.body)
			req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "sys_actor", Role: tt.role}))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			var body map[string]string
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("decode error response: %v", err)
			}
			if body["message"] != tt.wantMessage {
				t.Fatalf("message = %q, want %q", body["message"], tt.wantMessage)
			}
		})
	}
}

func TestManagementAccountGroupBindingHandlerWritesOperationLog(t *testing.T) {
	createdAt := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	queueStub := &operationLogQueueStub{}
	service := &managementAccountGroupBindingServiceStub{result: bindingHTTPResult()}
	handler := newManagementAccountGroupBindingHandler(service, managementAccountGroupBindingScopeAdmin, newManagementOperationLogOptions(ManagementOperationLogOptions{
		Client: queueStub, Now: func() time.Time { return createdAt }, NewLogID: func() string { return "oplog_binding" },
	}))
	req := managementAccountGroupBindingRequest("/__aisys__/api/accounts/acct_main/group?systemAccountId=sys_owner", "acct_main", `{"groupId":"group_new"}`)
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
		SystemAccountID: "sys_admin", Username: "admin", DisplayName: "管理员", Role: "admin",
	}))
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "req_binding"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || queueStub.calls != 1 || queueStub.taskType != operationlogjob.TaskTypeWrite {
		t.Fatalf("status=%d queue=%+v body=%s", rec.Code, queueStub, rec.Body.String())
	}
	logInput, err := operationlogjob.DecodeWriteTaskPayload(queueStub.payload)
	if err != nil {
		t.Fatalf("DecodeWriteTaskPayload() error = %v", err)
	}
	if logInput.ID != "oplog_binding" || logInput.OperationKey != "accounts.bind_group" || logInput.Action != "bind_group" ||
		logInput.OperationScopeSystemAccountID != "sys_owner" || logInput.ResourceID != "acct_main" ||
		len(logInput.Changes) != 1 || logInput.Changes[0].Before != "group_old" || logInput.Changes[0].After != "group_new" ||
		!logInput.CreatedAt.Equal(createdAt) {
		t.Fatalf("operation log = %+v", logInput)
	}
}

type managementAccountGroupBindingServiceStub struct {
	input  managementaccountgroupbinding.BindInput
	result managementaccountgroupbinding.BindResult
	err    error
}

func (s *managementAccountGroupBindingServiceStub) Bind(_ *http.Request, input managementaccountgroupbinding.BindInput) (managementaccountgroupbinding.BindResult, error) {
	s.input = input
	return s.result, s.err
}

func bindingHTTPResult() managementaccountgroupbinding.BindResult {
	return managementaccountgroupbinding.BindResult{
		Account:         managementaccountgroupbinding.Account{ID: "acct_main", SystemAccountID: "sys_owner", Name: "主账号", ProviderCode: "openai", Type: "api_key", Status: "active", BoundGroupID: "group_new", BoundGroupName: "主分组"},
		PreviousGroupID: "group_old",
	}
}

func managementAccountGroupBindingRequest(path, accountID, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("id", accountID)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext))
}

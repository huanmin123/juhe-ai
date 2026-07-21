package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	operationlogjob "juhe-ai/backend-go/internal/jobs/operationlog"
	"juhe-ai/backend-go/internal/modules/managementaccountforceactivate"
	"juhe-ai/backend-go/internal/modules/managementauth"
)

func TestManagementAccountForceActivateHandlerAdminAndSelfScopes(t *testing.T) {
	tests := []struct {
		name, path, role, actor string
		scope                   managementAccountForceActivateScope
		wantSystemID            string
	}{
		{"admin", "/accounts/acct/force-activate?systemAccountId=owner", "admin", "actor", managementAccountForceActivateScopeAdmin, "owner"},
		{"self", "/my-accounts/acct/force-activate?systemAccountId=other", "user", "self", managementAccountForceActivateScopeSelf, "self"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementAccountForceActivateServiceStub{result: forceActivateHTTPResult()}
			handler := newManagementAccountForceActivateHandler(service, tt.scope)
			req := forceActivateRequest(tt.path, `{"acknowledgedAccountAvailable":true}`)
			req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: tt.actor, Role: tt.role}))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK || service.input.SystemAccountID != tt.wantSystemID || !service.input.Acknowledged {
				t.Fatalf("status=%d input=%+v body=%s", rec.Code, service.input, rec.Body.String())
			}
			var body struct {
				Data map[string]any `json:"data"`
			}
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil || body.Data["id"] != "acct" {
				t.Fatalf("body=%s err=%v", rec.Body.String(), err)
			}
		})
	}
}

func TestManagementAccountForceActivateHandlerValidationAndErrors(t *testing.T) {
	tests := []struct {
		name, path, body, role string
		err                    error
		want                   int
	}{
		{"admin required", "/accounts/acct/force-activate", `{"acknowledgedAccountAvailable":true}`, "user", nil, 403},
		{"blank scope", "/accounts/acct/force-activate?systemAccountId=", `{"acknowledgedAccountAvailable":true}`, "admin", nil, 400},
		{"confirmation", "/accounts/acct/force-activate", `{}`, "admin", managementaccountforceactivate.ErrConfirmation, 400},
		{"unknown field", "/accounts/acct/force-activate", `{"acknowledgedAccountAvailable":true,"extra":1}`, "admin", nil, 400},
		{"not found", "/accounts/acct/force-activate", `{"acknowledgedAccountAvailable":true}`, "admin", managementaccountforceactivate.ErrNotFound, 404},
		{"authorized", "/accounts/acct/force-activate", `{"acknowledgedAccountAvailable":true}`, "admin", managementaccountforceactivate.ErrAuthorized, 400},
		{"wrong status", "/accounts/acct/force-activate", `{"acknowledgedAccountAvailable":true}`, "admin", managementaccountforceactivate.ErrInvalidStatus, 409},
		{"changed", "/accounts/acct/force-activate", `{"acknowledgedAccountAvailable":true}`, "admin", managementaccountforceactivate.ErrStateChanged, 409},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementAccountForceActivateServiceStub{result: forceActivateHTTPResult(), err: tt.err}
			handler := newManagementAccountForceActivateHandler(service, managementAccountForceActivateScopeAdmin)
			req := forceActivateRequest(tt.path, tt.body)
			req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "actor", Role: tt.role}))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tt.want, rec.Body.String())
			}
		})
	}
}

func TestManagementAccountForceActivateHandlerWritesOperationLog(t *testing.T) {
	queue := &operationLogQueueStub{}
	createdAt := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	service := &managementAccountForceActivateServiceStub{result: forceActivateHTTPResult()}
	handler := newManagementAccountForceActivateHandler(service, managementAccountForceActivateScopeAdmin, newManagementOperationLogOptions(ManagementOperationLogOptions{
		Client: queue, Now: func() time.Time { return createdAt }, NewLogID: func() string { return "oplog_force" },
	}))
	req := forceActivateRequest("/accounts/acct/force-activate", `{"acknowledgedAccountAvailable":true}`)
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "admin", Username: "admin", Role: "admin"}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || queue.calls != 1 || queue.taskType != operationlogjob.TaskTypeWrite {
		t.Fatalf("status=%d queue=%+v body=%s", rec.Code, queue, rec.Body.String())
	}
	input, err := operationlogjob.DecodeWriteTaskPayload(queue.payload)
	if err != nil {
		t.Fatal(err)
	}
	if input.ID != "oplog_force" || input.OperationKey != "accounts.force_activate_pending" || input.ResourceID != "acct" || input.OperationScopeSystemAccountID != "owner" || len(input.Changes) != 2 || !input.CreatedAt.Equal(createdAt) {
		t.Fatalf("operation log=%+v", input)
	}
}

type managementAccountForceActivateServiceStub struct {
	input  managementaccountforceactivate.Input
	result managementaccountforceactivate.Result
	err    error
}

func (s *managementAccountForceActivateServiceStub) ForceActivate(_ *http.Request, input managementaccountforceactivate.Input) (managementaccountforceactivate.Result, error) {
	s.input = input
	return s.result, s.err
}

func forceActivateHTTPResult() managementaccountforceactivate.Result {
	return managementaccountforceactivate.Result{BeforeStatus: "pending_test", AfterStatus: "active", OwnerSystemID: "owner", After: map[string]any{"id": "acct", "name": "主账户", "status": "active"}}
}

func forceActivateRequest(path, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("id", "acct")
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext))
}

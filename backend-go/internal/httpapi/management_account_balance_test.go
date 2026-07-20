package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/modules/managementaccountbalance"
	"juhe-ai/backend-go/internal/modules/managementauth"
)

func TestManagementAccountBalanceHandlerUsesAdminAndSelfScopes(t *testing.T) {
	tests := []struct {
		name         string
		path         string
		role         string
		actor        string
		scope        managementAccountBalanceScope
		wantSystemID string
	}{
		{name: "admin", path: "/accounts/acct/balance?systemAccountId=owner", role: "admin", actor: "admin", scope: managementAccountBalanceScopeAdmin, wantSystemID: "owner"},
		{name: "self", path: "/my-accounts/acct/balance?systemAccountId=other", role: "user", actor: "self", scope: managementAccountBalanceScopeSelf, wantSystemID: "self"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementAccountBalanceServiceStub{snapshot: managementaccountbalance.Snapshot{Status: "fresh", Balance: "12.50"}, found: true}
			handler := newManagementAccountBalanceHandler(service, tt.scope)
			req := managementAccountBalanceRequest(http.MethodGet, tt.path)
			req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: tt.actor, Role: tt.role}))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK || service.getInput.AccountID != "acct" || service.getInput.SystemAccountID != tt.wantSystemID {
				t.Fatalf("status=%d input=%+v body=%s", rec.Code, service.getInput, rec.Body.String())
			}
			var body struct {
				Data managementaccountbalance.Snapshot `json:"data"`
			}
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil || body.Data.Balance != "12.50" {
				t.Fatalf("body=%s err=%v", rec.Body.String(), err)
			}
		})
	}
}

func TestManagementAccountBalanceRefreshHandlerReturnsRecognizableMissingAdapterError(t *testing.T) {
	service := &managementAccountBalanceServiceStub{refreshErr: managementaccountbalance.ErrBalanceQueryMissing}
	handler := newManagementAccountBalanceRefreshHandler(service, managementAccountBalanceScopeSelf)
	req := managementAccountBalanceRequest(http.MethodPost, "/my-accounts/acct/balance/refresh")
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "self", Role: "user"}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil || body["message"] != "账户余额查询适配器未配置" {
		t.Fatalf("body=%s err=%v", rec.Body.String(), err)
	}
}

func TestManagementAccountBalanceHandlersValidateAccessAndNotFound(t *testing.T) {
	tests := []struct {
		name    string
		handler http.Handler
		role    string
		want    int
	}{
		{name: "admin required", handler: newManagementAccountBalanceHandler(&managementAccountBalanceServiceStub{}, managementAccountBalanceScopeAdmin), role: "user", want: http.StatusForbidden},
		{name: "blank admin scope", handler: newManagementAccountBalanceHandler(&managementAccountBalanceServiceStub{}, managementAccountBalanceScopeAdmin), role: "admin", want: http.StatusBadRequest},
		{name: "snapshot not found", handler: newManagementAccountBalanceHandler(&managementAccountBalanceServiceStub{}, managementAccountBalanceScopeSelf), role: "user", want: http.StatusNotFound},
		{name: "refresh candidate not found", handler: newManagementAccountBalanceRefreshHandler(&managementAccountBalanceServiceStub{}, managementAccountBalanceScopeSelf), role: "user", want: http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := "/accounts/acct/balance"
			if tt.name == "blank admin scope" {
				path += "?systemAccountId="
			}
			req := managementAccountBalanceRequest(http.MethodGet, path)
			req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "actor", Role: tt.role}))
			rec := httptest.NewRecorder()
			tt.handler.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tt.want, rec.Body.String())
			}
		})
	}
}

func TestManagementAccountBalanceRefreshHandlerReturnsSnapshot(t *testing.T) {
	service := &managementAccountBalanceServiceStub{snapshot: managementaccountbalance.Snapshot{Status: "fresh", Balance: "9.99"}, found: true}
	handler := newManagementAccountBalanceRefreshHandler(service, managementAccountBalanceScopeAdmin)
	req := managementAccountBalanceRequest(http.MethodPost, "/accounts/acct/balance/refresh?systemAccountId=all")
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "admin", Role: "admin"}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || service.refreshInput.AccountID != "acct" || service.refreshInput.SystemAccountID != "" {
		t.Fatalf("status=%d input=%+v body=%s", rec.Code, service.refreshInput, rec.Body.String())
	}
}

type managementAccountBalanceServiceStub struct {
	getInput     managementaccountbalance.Input
	refreshInput managementaccountbalance.Input
	snapshot     managementaccountbalance.Snapshot
	found        bool
	getErr       error
	refreshErr   error
}

func (s *managementAccountBalanceServiceStub) Get(_ *http.Request, input managementaccountbalance.Input) (managementaccountbalance.Snapshot, bool, error) {
	s.getInput = input
	return s.snapshot, s.found, s.getErr
}

func (s *managementAccountBalanceServiceStub) Refresh(_ *http.Request, input managementaccountbalance.Input) (managementaccountbalance.Snapshot, bool, error) {
	s.refreshInput = input
	return s.snapshot, s.found, s.refreshErr
}

func managementAccountBalanceRequest(method, path string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("id", "acct")
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext))
}

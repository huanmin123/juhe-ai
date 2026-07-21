package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/modules/managementaccountauthorizeddispatch"
	"juhe-ai/backend-go/internal/modules/managementauth"
)

func TestManagementAccountAuthorizedDispatchHandlersScopeAndPayload(t *testing.T) {
	service := &authorizedDispatchHTTPStub{result: managementaccountauthorizeddispatch.Result{Account: managementaccountauthorizeddispatch.Account{ID: "acct_auth", AccessType: "authorized"}}}
	handler := newManagementAccountAuthorizedDispatchHandler(service, managementAccountAuthorizedDispatchScopeAdmin)
	req := authorizedDispatchRequest("/__aisys__/api/accounts/acct_auth/authorized-dispatch?systemAccountId=sys_target", `{"status":"active","priority":7,"clearFailureState":true}`)
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if service.input.SystemAccountID != "sys_target" || service.input.SelfOnly || service.input.Status == nil || *service.input.Priority != 7 {
		t.Fatalf("input=%+v", service.input)
	}
	var body struct {
		Data managementaccountauthorizeddispatch.Account `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil || body.Data.ID != "acct_auth" {
		t.Fatalf("body=%s err=%v", rec.Body.String(), err)
	}

	self := newManagementAccountAuthorizedDispatchHandler(service, managementAccountAuthorizedDispatchScopeSelf)
	req = authorizedDispatchRequest("/__aisys__/api/my-accounts/acct_auth/authorized-dispatch?systemAccountId=sys_other", `{}`)
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "sys_self", Role: "user"}))
	rec = httptest.NewRecorder()
	self.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !service.input.SelfOnly || service.input.SystemAccountID != "" {
		t.Fatalf("status=%d input=%+v", rec.Code, service.input)
	}
}

func TestManagementAccountAuthorizedDispatchHandlerRejectsInvalidInput(t *testing.T) {
	service := &authorizedDispatchHTTPStub{}
	handler := newManagementAccountAuthorizedDispatchHandler(service, managementAccountAuthorizedDispatchScopeAdmin)
	for _, body := range []string{`{"status":"paused"}`, `{"priority":1.5}`, `{"extra":true}`, `{"status":null}`} {
		req := authorizedDispatchRequest("/__aisys__/api/accounts/acct_auth/authorized-dispatch", body)
		req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"}))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body=%s status=%d response=%s", body, rec.Code, rec.Body.String())
		}
	}
}

type authorizedDispatchHTTPStub struct {
	input  managementaccountauthorizeddispatch.Input
	result managementaccountauthorizeddispatch.Result
	err    error
}

func (s *authorizedDispatchHTTPStub) Update(_ *http.Request, input managementaccountauthorizeddispatch.Input) (managementaccountauthorizeddispatch.Result, error) {
	s.input = input
	return s.result, s.err
}
func authorizedDispatchRequest(path, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPatch, path, strings.NewReader(body))
	route := chi.NewRouteContext()
	route.URLParams.Add("id", "acct_auth")
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
}

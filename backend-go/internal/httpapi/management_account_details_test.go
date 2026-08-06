package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/modules/managementaccountdetails"
	"juhe-ai/backend-go/internal/modules/managementauth"
)

func TestManagementAccountOAuthReauthorizationContextHandlerReturnsNoStoreEnvelopeAndScope(t *testing.T) {
	service := &managementAccountDetailContextServiceStub{result: managementaccountdetails.OAuthReauthorizationContext{
		ID: "acct_1", ConfigRevision: 2, OAuthType: "ai_studio", ClientID: "client", ClientSecret: "secret",
	}}
	handler := newManagementAccountOAuthReauthorizationContextHandler(service, managementAccountDetailScopeAdmin)
	req := requestWithManagementOAuthReauthorizationID(httptest.NewRequest(http.MethodGet, "/__aisys__/api/accounts/acct_1?systemAccountId=sys_owner", nil), "acct_1")
	req = requestWithManagementAuthContext(req, managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d cache-control=%q body=%s", recorder.Code, recorder.Header().Get("Cache-Control"), recorder.Body.String())
	}
	var body struct {
		Data managementaccountdetails.OAuthReauthorizationContext `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Data != service.result || service.input.SystemAccountID != "sys_owner" || service.input.AccountID != "acct_1" {
		t.Fatalf("data=%#v input=%#v", body.Data, service.input)
	}

	selfService := &managementAccountDetailContextServiceStub{result: service.result}
	selfHandler := newManagementAccountOAuthReauthorizationContextHandler(selfService, managementAccountDetailScopeSelf)
	selfReq := requestWithManagementOAuthReauthorizationID(httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-accounts/acct_1?systemAccountId=other", nil), "acct_1")
	selfReq = requestWithManagementAuthContext(selfReq, managementauth.Context{SystemAccountID: "sys_self", Role: "user"})
	selfRecorder := httptest.NewRecorder()
	selfHandler.ServeHTTP(selfRecorder, selfReq)
	if selfRecorder.Code != http.StatusOK || selfService.input.SystemAccountID != "sys_self" {
		t.Fatalf("self status=%d input=%#v", selfRecorder.Code, selfService.input)
	}
}

func TestManagementAccountOAuthReauthorizationContextHandlerMapsErrors(t *testing.T) {
	tests := []struct {
		name       string
		serviceErr error
		found      bool
		wantStatus int
		wantText   string
	}{
		{name: "not found", wantStatus: http.StatusNotFound, wantText: "账户不存在"},
		{name: "authorization instance", serviceErr: managementaccountdetails.ErrOAuthReauthorizationForbidden, wantStatus: http.StatusForbidden, wantText: "授权实例不能重新授权"},
		{name: "internal", serviceErr: errors.New("secret database failure"), wantStatus: http.StatusInternalServerError, wantText: "服务器内部错误"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementAccountDetailContextServiceStub{found: tt.found, err: tt.serviceErr}
			handler := newManagementAccountOAuthReauthorizationContextHandler(service, managementAccountDetailScopeSelf)
			req := requestWithManagementOAuthReauthorizationID(httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-accounts/acct_1", nil), "acct_1")
			req = requestWithManagementAuthContext(req, managementauth.Context{SystemAccountID: "sys_self", Role: "user"})
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)
			if recorder.Code != tt.wantStatus || !strings.Contains(recorder.Body.String(), tt.wantText) {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if strings.Contains(recorder.Body.String(), "secret database failure") {
				t.Fatal("internal error leaked original error")
			}
		})
	}
}

func requestWithManagementOAuthReauthorizationID(req *http.Request, id string) *http.Request {
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("id", id)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext))
}

type managementAccountDetailContextServiceStub struct {
	input  managementaccountdetails.Input
	result managementaccountdetails.OAuthReauthorizationContext
	found  bool
	err    error
}

func (s *managementAccountDetailContextServiceStub) Get(*http.Request, managementaccountdetails.Input, managementaccountdetails.Level) (map[string]any, bool, error) {
	return nil, false, nil
}

func (s *managementAccountDetailContextServiceStub) APIKeyRuntime(*http.Request, managementaccountdetails.Input) (managementaccountdetails.APIKeyRuntimeResponse, bool, error) {
	return managementaccountdetails.APIKeyRuntimeResponse{}, false, nil
}

func (s *managementAccountDetailContextServiceStub) OAuthReauthorizationContext(_ *http.Request, input managementaccountdetails.Input) (managementaccountdetails.OAuthReauthorizationContext, bool, error) {
	s.input = input
	return s.result, s.found || s.err == nil && s.result.ID != "", s.err
}

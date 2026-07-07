package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementauthorizationoptions"
)

func TestManagementAuthorizationGranteeAccountsHandlerRequiresAdminAndParsesQuery(t *testing.T) {
	service := &managementAuthorizationOptionServiceStub{
		granteeAccounts: []managementauthorizationoptions.GranteeAccountOption{{
			ID:          "sys_user",
			Username:    "user",
			DisplayName: "用户",
			Status:      "active",
		}},
	}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
	})(newManagementAuthorizationGranteeAccountsHandler(service, managementAuthorizationOptionScopeAdmin))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/authorization-options/grantee-accounts?ids=sys_user,sys_disabled&ids=sys_user&limit=500&keyword=%20%E7%94%A8%E6%88%B7%20", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if service.input.Keyword != "用户" || service.input.Limit != 500 {
		t.Fatalf("service input = %+v", service.input)
	}
	if len(service.input.IDs) != 2 || service.input.IDs[0] != "sys_user" || service.input.IDs[1] != "sys_disabled" {
		t.Fatalf("ids = %#v", service.input.IDs)
	}
	var body struct {
		Data []managementauthorizationoptions.GranteeAccountOption `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data) != 1 || body.Data[0].ID != "sys_user" || body.Data[0].DisplayName != "用户" {
		t.Fatalf("body = %+v", body)
	}
}

func TestManagementAuthorizationGranteeAccountsHandlerRejectsOrdinaryUserOnAdminRoute(t *testing.T) {
	service := &managementAuthorizationOptionServiceStub{}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_user", Username: "user", Role: "user", SessionID: "sess_user"},
	})(newManagementAuthorizationGranteeAccountsHandler(service, managementAuthorizationOptionScopeAdmin))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/authorization-options/grantee-accounts", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if service.called {
		t.Fatal("service should not be called for ordinary user on admin route")
	}
}

func TestManagementMyAuthorizationGranteeAccountsHandlerAllowsOrdinaryUser(t *testing.T) {
	service := &managementAuthorizationOptionServiceStub{
		granteeAccounts: []managementauthorizationoptions.GranteeAccountOption{{ID: "sys_admin", Username: "admin", DisplayName: "管理员", Status: "active"}},
	}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_user", Username: "user", Role: "user", SessionID: "sess_user"},
	})(newManagementAuthorizationGranteeAccountsHandler(service, managementAuthorizationOptionScopeSelf))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-authorization-options/grantee-accounts?limit=50", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !service.called {
		t.Fatal("service should be called for ordinary user on my route")
	}
}

func TestManagementAuthorizationGranteeAccountsHandlerRedactsStoreErrors(t *testing.T) {
	handler := newManagementAuthorizationGranteeAccountsHandler(&managementAuthorizationOptionServiceStub{err: errors.New("postgres password leaked")}, managementAuthorizationOptionScopeSelf)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-authorization-options/grantee-accounts", nil)
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "sys_user", Role: "user"}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["message"] != "服务器内部错误" {
		t.Fatalf("body = %+v", body)
	}
}

func TestRouterRegistersW2ManagementAuthorizationGranteeAccounts(t *testing.T) {
	service := &managementAuthorizationOptionServiceStub{
		granteeAccounts: []managementauthorizationoptions.GranteeAccountOption{{ID: "sys_user", Username: "user", DisplayName: "用户", Status: "active"}},
	}
	router := NewRouter(RouterOptions{
		Config: config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger: slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementAuthorizationGranteeAccountsHandler:   newManagementAuthorizationGranteeAccountsHandler(service, managementAuthorizationOptionScopeAdmin),
		ManagementMyAuthorizationGranteeAccountsHandler: newManagementAuthorizationGranteeAccountsHandler(service, managementAuthorizationOptionScopeSelf),
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
			context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
		}),
	})

	for _, path := range []string{
		"/__aisys__/api/authorization-options/grantee-accounts?limit=50",
		"/__aisys__/api/my-authorization-options/grantee-accounts?limit=50",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Cookie", "juhe_ai_session=session-token")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", path, rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("%s Cache-Control = %q, want no-store", path, got)
		}
	}
}

func TestRouterDoesNotRegisterW2ManagementAuthorizationGranteeAccountsWhenDisabled(t *testing.T) {
	router := NewRouter(RouterOptions{
		Config: config.Config{Host: "127.0.0.1", Port: 3000},
		Logger: slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementAuthorizationGranteeAccountsHandler:   newManagementAuthorizationGranteeAccountsHandler(&managementAuthorizationOptionServiceStub{}, managementAuthorizationOptionScopeAdmin),
		ManagementMyAuthorizationGranteeAccountsHandler: newManagementAuthorizationGranteeAccountsHandler(&managementAuthorizationOptionServiceStub{}, managementAuthorizationOptionScopeSelf),
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
			context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
		}),
	})

	for _, path := range []string{
		"/__aisys__/api/authorization-options/grantee-accounts",
		"/__aisys__/api/my-authorization-options/grantee-accounts",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Cookie", "juhe_ai_session=session-token")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404 while JUHE_AI_MANAGEMENT_API_ENABLED=false", path, rec.Code)
		}
	}
}

type managementAuthorizationOptionServiceStub struct {
	called          bool
	input           managementauthorizationoptions.PrincipalOptionListInput
	granteeAccounts []managementauthorizationoptions.GranteeAccountOption
	err             error
}

func (s *managementAuthorizationOptionServiceStub) GranteeAccounts(_ *http.Request, input managementauthorizationoptions.PrincipalOptionListInput) ([]managementauthorizationoptions.GranteeAccountOption, error) {
	s.called = true
	s.input = input
	return s.granteeAccounts, s.err
}

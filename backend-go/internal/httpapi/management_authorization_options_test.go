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

func TestManagementAuthorizationGranteeTeamsHandlerRequiresAdminAndParsesQuery(t *testing.T) {
	service := &managementAuthorizationOptionServiceStub{
		granteeTeams: []managementauthorizationoptions.GranteeTeamOption{{
			ID:     "team_ops",
			Name:   "运维团队",
			Status: "active",
		}},
	}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
	})(newManagementAuthorizationGranteeTeamsHandler(service, managementAuthorizationOptionScopeAdmin))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/authorization-options/grantee-teams?ids=team_ops,team_disabled&ids=team_ops&limit=500&keyword=%20%E8%BF%90%E7%BB%B4%20", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if service.teamInput.Keyword != "运维" || service.teamInput.Limit != 500 {
		t.Fatalf("service team input = %+v", service.teamInput)
	}
	if len(service.teamInput.IDs) != 2 || service.teamInput.IDs[0] != "team_ops" || service.teamInput.IDs[1] != "team_disabled" {
		t.Fatalf("team ids = %#v", service.teamInput.IDs)
	}
	var body struct {
		Data []managementauthorizationoptions.GranteeTeamOption `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data) != 1 || body.Data[0].ID != "team_ops" || body.Data[0].Name != "运维团队" || body.Data[0].Status != "active" {
		t.Fatalf("body = %+v", body)
	}
}

func TestManagementAuthorizationGranteeTeamsHandlerRejectsOrdinaryUserOnAdminRoute(t *testing.T) {
	service := &managementAuthorizationOptionServiceStub{}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_user", Username: "user", Role: "user", SessionID: "sess_user"},
	})(newManagementAuthorizationGranteeTeamsHandler(service, managementAuthorizationOptionScopeAdmin))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/authorization-options/grantee-teams", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if service.teamCalled {
		t.Fatal("service should not be called for ordinary user on admin route")
	}
}

func TestManagementMyAuthorizationGranteeTeamsHandlerAllowsOrdinaryUser(t *testing.T) {
	service := &managementAuthorizationOptionServiceStub{
		granteeTeams: []managementauthorizationoptions.GranteeTeamOption{{ID: "team_ops", Name: "运维团队", Status: "active"}},
	}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_user", Username: "user", Role: "user", SessionID: "sess_user"},
	})(newManagementAuthorizationGranteeTeamsHandler(service, managementAuthorizationOptionScopeSelf))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-authorization-options/grantee-teams?limit=50", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !service.teamCalled {
		t.Fatal("service should be called for ordinary user on my route")
	}
}

func TestManagementAuthorizationGranteeTeamsHandlerRedactsStoreErrors(t *testing.T) {
	handler := newManagementAuthorizationGranteeTeamsHandler(&managementAuthorizationOptionServiceStub{err: errors.New("postgres password leaked")}, managementAuthorizationOptionScopeSelf)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-authorization-options/grantee-teams", nil)
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

func TestManagementAuthorizationGranteeGroupsHandlerRequiresAdminAndParsesQuery(t *testing.T) {
	service := &managementAuthorizationOptionServiceStub{
		granteeGroups: []managementauthorizationoptions.GranteeGroupOption{{
			ID: "grp_default", Name: "默认分组",
		}},
	}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
	})(newManagementAuthorizationGranteeGroupsHandler(service, managementAuthorizationOptionScopeAdmin))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/authorization-options/grantee-groups?granteeSystemAccountId=%20sys_user%20&ids=grp_default,grp_backup&ids=grp_default&limit=500&keyword=%20%E9%BB%98%E8%AE%A4%20&providerCode=%20openai%20&preferDefault=false", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if service.groupInput.GranteeSystemAccountID != "sys_user" ||
		service.groupInput.Keyword != "默认" ||
		service.groupInput.ProviderCode != "openai" ||
		service.groupInput.Limit != 500 ||
		service.groupInput.PreferDefault {
		t.Fatalf("service group input = %+v", service.groupInput)
	}
	if len(service.groupInput.IDs) != 2 || service.groupInput.IDs[0] != "grp_default" || service.groupInput.IDs[1] != "grp_backup" {
		t.Fatalf("group ids = %#v", service.groupInput.IDs)
	}
	var body struct {
		Data []managementauthorizationoptions.GranteeGroupOption `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data) != 1 || body.Data[0].ID != "grp_default" || body.Data[0].Name != "默认分组" {
		t.Fatalf("body = %+v", body)
	}
}

func TestManagementAuthorizationGranteeGroupsHandlerRequiresGranteeSystemAccount(t *testing.T) {
	service := &managementAuthorizationOptionServiceStub{}
	handler := newManagementAuthorizationGranteeGroupsHandler(service, managementAuthorizationOptionScopeSelf)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-authorization-options/grantee-groups?providerCode=openai", nil)
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "sys_user", Role: "user"}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if service.groupCalled {
		t.Fatal("service should not be called without granteeSystemAccountId")
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["message"] != "被授权用户不能为空" {
		t.Fatalf("body = %+v", body)
	}
}

func TestManagementAuthorizationGranteeGroupsHandlerRejectsOrdinaryUserOnAdminRoute(t *testing.T) {
	service := &managementAuthorizationOptionServiceStub{}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_user", Username: "user", Role: "user", SessionID: "sess_user"},
	})(newManagementAuthorizationGranteeGroupsHandler(service, managementAuthorizationOptionScopeAdmin))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/authorization-options/grantee-groups", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if service.groupCalled {
		t.Fatal("service should not be called for ordinary user on admin route")
	}
}

func TestManagementMyAuthorizationGranteeGroupsHandlerAllowsOrdinaryUser(t *testing.T) {
	service := &managementAuthorizationOptionServiceStub{
		granteeGroups: []managementauthorizationoptions.GranteeGroupOption{{
			ID: "grp_default", Name: "默认分组",
		}},
	}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_user", Username: "user", Role: "user", SessionID: "sess_user"},
	})(newManagementAuthorizationGranteeGroupsHandler(service, managementAuthorizationOptionScopeSelf))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-authorization-options/grantee-groups?granteeSystemAccountId=sys_target&providerCode=openai&preferDefault=true&limit=50", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !service.groupCalled {
		t.Fatal("service should be called for ordinary user on my route")
	}
	if service.groupInput.GranteeSystemAccountID != "sys_target" {
		t.Fatalf("service group input = %+v", service.groupInput)
	}
	var raw map[string][]map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(raw["data"]) != 1 {
		t.Fatalf("body = %+v", raw)
	}
	if len(raw["data"][0]) != 2 || raw["data"][0]["name"] != "默认分组" {
		t.Fatalf("my route body = %+v", raw["data"][0])
	}
}

func TestManagementAuthorizationGranteeGroupsHandlerRedactsStoreErrors(t *testing.T) {
	handler := newManagementAuthorizationGranteeGroupsHandler(&managementAuthorizationOptionServiceStub{err: errors.New("postgres password leaked")}, managementAuthorizationOptionScopeSelf)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-authorization-options/grantee-groups?granteeSystemAccountId=sys_user", nil)
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
		granteeTeams:    []managementauthorizationoptions.GranteeTeamOption{{ID: "team_ops", Name: "运维团队", Status: "active"}},
		granteeGroups:   []managementauthorizationoptions.GranteeGroupOption{{ID: "grp_default", Name: "默认分组"}},
	}
	router := NewRouter(RouterOptions{
		Config: config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger: slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementAuthorizationGranteeAccountsHandler:   newManagementAuthorizationGranteeAccountsHandler(service, managementAuthorizationOptionScopeAdmin),
		ManagementMyAuthorizationGranteeAccountsHandler: newManagementAuthorizationGranteeAccountsHandler(service, managementAuthorizationOptionScopeSelf),
		ManagementAuthorizationGranteeTeamsHandler:      newManagementAuthorizationGranteeTeamsHandler(service, managementAuthorizationOptionScopeAdmin),
		ManagementMyAuthorizationGranteeTeamsHandler:    newManagementAuthorizationGranteeTeamsHandler(service, managementAuthorizationOptionScopeSelf),
		ManagementAuthorizationGranteeGroupsHandler:     newManagementAuthorizationGranteeGroupsHandler(service, managementAuthorizationOptionScopeAdmin),
		ManagementMyAuthorizationGranteeGroupsHandler:   newManagementAuthorizationGranteeGroupsHandler(service, managementAuthorizationOptionScopeSelf),
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
			context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
		}),
	})

	for _, path := range []string{
		"/__aisys__/api/authorization-options/grantee-accounts?limit=50",
		"/__aisys__/api/my-authorization-options/grantee-accounts?limit=50",
		"/__aisys__/api/authorization-options/grantee-teams?limit=50",
		"/__aisys__/api/my-authorization-options/grantee-teams?limit=50",
		"/__aisys__/api/authorization-options/grantee-groups?granteeSystemAccountId=sys_user&limit=50",
		"/__aisys__/api/my-authorization-options/grantee-groups?granteeSystemAccountId=sys_user&limit=50",
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
		ManagementAuthorizationGranteeTeamsHandler:      newManagementAuthorizationGranteeTeamsHandler(&managementAuthorizationOptionServiceStub{}, managementAuthorizationOptionScopeAdmin),
		ManagementMyAuthorizationGranteeTeamsHandler:    newManagementAuthorizationGranteeTeamsHandler(&managementAuthorizationOptionServiceStub{}, managementAuthorizationOptionScopeSelf),
		ManagementAuthorizationGranteeGroupsHandler:     newManagementAuthorizationGranteeGroupsHandler(&managementAuthorizationOptionServiceStub{}, managementAuthorizationOptionScopeAdmin),
		ManagementMyAuthorizationGranteeGroupsHandler:   newManagementAuthorizationGranteeGroupsHandler(&managementAuthorizationOptionServiceStub{}, managementAuthorizationOptionScopeSelf),
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
			context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
		}),
	})

	for _, path := range []string{
		"/__aisys__/api/authorization-options/grantee-accounts",
		"/__aisys__/api/my-authorization-options/grantee-accounts",
		"/__aisys__/api/authorization-options/grantee-teams",
		"/__aisys__/api/my-authorization-options/grantee-teams",
		"/__aisys__/api/authorization-options/grantee-groups?granteeSystemAccountId=sys_user",
		"/__aisys__/api/my-authorization-options/grantee-groups?granteeSystemAccountId=sys_user",
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
	teamCalled      bool
	groupCalled     bool
	input           managementauthorizationoptions.PrincipalOptionListInput
	teamInput       managementauthorizationoptions.PrincipalOptionListInput
	groupInput      managementauthorizationoptions.GranteeGroupOptionListInput
	granteeAccounts []managementauthorizationoptions.GranteeAccountOption
	granteeTeams    []managementauthorizationoptions.GranteeTeamOption
	granteeGroups   []managementauthorizationoptions.GranteeGroupOption
	err             error
}

func (s *managementAuthorizationOptionServiceStub) GranteeAccounts(_ *http.Request, input managementauthorizationoptions.PrincipalOptionListInput) ([]managementauthorizationoptions.GranteeAccountOption, error) {
	s.called = true
	s.input = input
	return s.granteeAccounts, s.err
}

func (s *managementAuthorizationOptionServiceStub) GranteeTeams(_ *http.Request, input managementauthorizationoptions.PrincipalOptionListInput) ([]managementauthorizationoptions.GranteeTeamOption, error) {
	s.teamCalled = true
	s.teamInput = input
	return s.granteeTeams, s.err
}

func (s *managementAuthorizationOptionServiceStub) GranteeGroups(_ *http.Request, input managementauthorizationoptions.GranteeGroupOptionListInput) ([]managementauthorizationoptions.GranteeGroupOption, error) {
	s.groupCalled = true
	s.groupInput = input
	return s.granteeGroups, s.err
}

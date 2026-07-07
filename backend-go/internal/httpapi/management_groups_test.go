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
	"juhe-ai/backend-go/internal/modules/managementgroups"
)

func TestManagementGroupOptionsHandlerRequiresAdminAndParsesQuery(t *testing.T) {
	service := &managementGroupOptionServiceStub{
		options: []managementgroups.Option{
			{
				ID:                     "group_default",
				SystemAccountID:        "sys_user",
				SystemAccountName:      "用户",
				OwnerSystemAccountID:   "sys_user",
				OwnerSystemAccountName: "用户",
				Name:                   "默认分组",
				ProviderCode:           "openai",
				Enabled:                true,
				IsDefault:              true,
				GroupType:              "personal",
				AccessType:             "owner",
			},
		},
	}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
	})(newManagementGroupOptionsHandler(service, managementGroupScopeAdmin))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/groups/options?systemAccountId=sys_user&ids=group_a,group_b&ids=group_a&keyword=%20%E9%BB%98%E8%AE%A4%20&providerCode=%20openai%20&limit=500&manageableOnly=yes&preferDefault=1", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if service.input.SystemAccountID != "sys_user" ||
		!service.input.IncludeSystemAccountFields ||
		service.input.Keyword != "默认" ||
		service.input.ProviderCode != "openai" ||
		service.input.Limit != 500 ||
		!service.input.ManageableOnly ||
		!service.input.PreferDefault {
		t.Fatalf("service input = %+v", service.input)
	}
	if len(service.input.IDs) != 2 || service.input.IDs[0] != "group_a" || service.input.IDs[1] != "group_b" {
		t.Fatalf("ids = %#v", service.input.IDs)
	}
	var body struct {
		Data []managementgroups.Option `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data) != 1 || body.Data[0].SystemAccountName != "用户" || body.Data[0].AccessType != "owner" {
		t.Fatalf("body = %+v", body)
	}
}

func TestManagementGroupOptionsHandlerRejectsOrdinaryUser(t *testing.T) {
	service := &managementGroupOptionServiceStub{}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_user", Username: "user", Role: "user", SessionID: "sess_user"},
	})(newManagementGroupOptionsHandler(service, managementGroupScopeAdmin))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/groups/options?systemAccountId=sys_admin", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if service.called {
		t.Fatal("service should not be called for ordinary user on management route")
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["message"] != "需要管理员权限" {
		t.Fatalf("body = %+v", body)
	}
}

func TestManagementMyGroupOptionsHandlerForcesSelfScope(t *testing.T) {
	service := &managementGroupOptionServiceStub{}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_user", Username: "user", Role: "user", SessionID: "sess_user"},
	})(newManagementGroupOptionsHandler(service, managementGroupScopeSelf))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-groups/options?systemAccountId=sys_admin&manageableOnly=bad&preferDefault=false", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if service.input.SystemAccountID != "sys_user" ||
		service.input.IncludeSystemAccountFields ||
		service.input.ManageableOnly ||
		service.input.PreferDefault {
		t.Fatalf("service input = %+v", service.input)
	}
}

func TestManagementGroupOptionsHandlerRedactsStoreErrors(t *testing.T) {
	handler := newManagementGroupOptionsHandler(&managementGroupOptionServiceStub{err: errors.New("postgres password leaked")}, managementGroupScopeSelf)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-groups/options", nil)
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

func TestManagementGroupAccountOptionsHandlerRequiresAdminAndParsesQuery(t *testing.T) {
	service := &managementGroupOptionServiceStub{
		accountOptions: []managementgroups.AccountOption{
			{
				Option: managementgroups.Option{
					ID:                     "group_default",
					SystemAccountID:        "sys_user",
					SystemAccountName:      "用户",
					OwnerSystemAccountID:   "sys_user",
					OwnerSystemAccountName: "用户",
					Name:                   "默认分组",
					ProviderCode:           "openai",
					Enabled:                true,
					IsDefault:              true,
					GroupType:              "personal",
					AccessType:             "owner",
				},
				AccountIDs: []string{"acct_a", "acct_b"},
			},
		},
	}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
	})(newManagementGroupAccountOptionsHandler(service, managementGroupScopeAdmin))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/groups/account-options?systemAccountId=sys_user&ids=group_a,group_b&ids=group_a&keyword=%20%E9%BB%98%E8%AE%A4%20&providerCode=%20openai%20&limit=500&manageableOnly=yes&preferDefault=1", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if service.accountInput.SystemAccountID != "sys_user" ||
		!service.accountInput.IncludeSystemAccountFields ||
		service.accountInput.Keyword != "默认" ||
		service.accountInput.ProviderCode != "openai" ||
		service.accountInput.Limit != 500 ||
		!service.accountInput.ManageableOnly ||
		!service.accountInput.PreferDefault {
		t.Fatalf("service account input = %+v", service.accountInput)
	}
	if len(service.accountInput.IDs) != 2 || service.accountInput.IDs[0] != "group_a" || service.accountInput.IDs[1] != "group_b" {
		t.Fatalf("ids = %#v", service.accountInput.IDs)
	}
	var body struct {
		Data []managementgroups.AccountOption `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data) != 1 ||
		body.Data[0].SystemAccountName != "用户" ||
		body.Data[0].AccessType != "owner" ||
		len(body.Data[0].AccountIDs) != 2 {
		t.Fatalf("body = %+v", body)
	}
}

func TestManagementGroupAccountOptionsHandlerRejectsOrdinaryUser(t *testing.T) {
	service := &managementGroupOptionServiceStub{}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_user", Username: "user", Role: "user", SessionID: "sess_user"},
	})(newManagementGroupAccountOptionsHandler(service, managementGroupScopeAdmin))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/groups/account-options?systemAccountId=sys_admin", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if service.accountCalled {
		t.Fatal("service should not be called for ordinary user on management route")
	}
}

func TestManagementMyGroupAccountOptionsHandlerForcesSelfScope(t *testing.T) {
	service := &managementGroupOptionServiceStub{}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_user", Username: "user", Role: "user", SessionID: "sess_user"},
	})(newManagementGroupAccountOptionsHandler(service, managementGroupScopeSelf))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-groups/account-options?systemAccountId=sys_admin&manageableOnly=bad&preferDefault=false", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if service.accountInput.SystemAccountID != "sys_user" ||
		service.accountInput.IncludeSystemAccountFields ||
		service.accountInput.ManageableOnly ||
		service.accountInput.PreferDefault {
		t.Fatalf("service account input = %+v", service.accountInput)
	}
}

func TestManagementGroupAccountOptionsHandlerRedactsStoreErrors(t *testing.T) {
	handler := newManagementGroupAccountOptionsHandler(&managementGroupOptionServiceStub{err: errors.New("postgres password leaked")}, managementGroupScopeSelf)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-groups/account-options", nil)
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

func TestRouterRegistersW2ManagementGroupOptions(t *testing.T) {
	service := &managementGroupOptionServiceStub{
		options:        []managementgroups.Option{{ID: "group_default", OwnerSystemAccountID: "sys_admin", Name: "默认分组", ProviderCode: "openai", Enabled: true, IsDefault: true, GroupType: "personal", AccessType: "owner"}},
		accountOptions: []managementgroups.AccountOption{{Option: managementgroups.Option{ID: "group_default", OwnerSystemAccountID: "sys_admin", Name: "默认分组", ProviderCode: "openai", Enabled: true, IsDefault: true, GroupType: "personal", AccessType: "owner"}, AccountIDs: []string{"acct_main"}}},
	}
	router := NewRouter(RouterOptions{
		Config:                                 config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                                 slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementGroupOptionsHandler:          newManagementGroupOptionsHandler(service, managementGroupScopeAdmin),
		ManagementMyGroupOptionsHandler:        newManagementGroupOptionsHandler(service, managementGroupScopeSelf),
		ManagementGroupAccountOptionsHandler:   newManagementGroupAccountOptionsHandler(service, managementGroupScopeAdmin),
		ManagementMyGroupAccountOptionsHandler: newManagementGroupAccountOptionsHandler(service, managementGroupScopeSelf),
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
			context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
		}),
	})

	for _, path := range []string{"/__aisys__/api/groups/options", "/__aisys__/api/my-groups/options", "/__aisys__/api/groups/account-options", "/__aisys__/api/my-groups/account-options"} {
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

func TestRouterDoesNotRegisterW2ManagementGroupOptionsWhenDisabled(t *testing.T) {
	router := NewRouter(RouterOptions{
		Config:                               config.Config{Host: "127.0.0.1", Port: 3000},
		Logger:                               slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementGroupOptionsHandler:        newManagementGroupOptionsHandler(&managementGroupOptionServiceStub{}, managementGroupScopeAdmin),
		ManagementGroupAccountOptionsHandler: newManagementGroupAccountOptionsHandler(&managementGroupOptionServiceStub{}, managementGroupScopeAdmin),
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
			context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
		}),
	})

	for _, path := range []string{"/__aisys__/api/groups/options", "/__aisys__/api/groups/account-options"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Cookie", "juhe_ai_session=session-token")
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404 while JUHE_AI_MANAGEMENT_API_ENABLED=false", path, rec.Code)
		}
	}
}

type managementGroupOptionServiceStub struct {
	called         bool
	accountCalled  bool
	input          managementgroups.OptionListInput
	accountInput   managementgroups.OptionListInput
	options        []managementgroups.Option
	accountOptions []managementgroups.AccountOption
	err            error
}

func (s *managementGroupOptionServiceStub) Options(_ *http.Request, input managementgroups.OptionListInput) ([]managementgroups.Option, error) {
	s.called = true
	s.input = input
	return s.options, s.err
}

func (s *managementGroupOptionServiceStub) AccountOptions(_ *http.Request, input managementgroups.OptionListInput) ([]managementgroups.AccountOption, error) {
	s.accountCalled = true
	s.accountInput = input
	return s.accountOptions, s.err
}

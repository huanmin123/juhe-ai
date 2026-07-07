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
	"juhe-ai/backend-go/internal/modules/managementaccounts"
	"juhe-ai/backend-go/internal/modules/managementauth"
)

func TestManagementAccountOptionsHandlerRequiresAdminAndParsesQuery(t *testing.T) {
	service := &managementAccountOptionServiceStub{
		options: []managementaccounts.Option{
			{
				ID:                     "acct_main",
				SystemAccountID:        "sys_user",
				SystemAccountName:      "用户",
				OwnerSystemAccountID:   "sys_user",
				OwnerSystemAccountName: "用户",
				ProviderCode:           "openai",
				Name:                   "主账号",
				Type:                   "api_key",
				Status:                 "active",
				AccessType:             "owner",
			},
		},
	}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
	})(newManagementAccountOptionsHandler(service, managementAccountScopeAdmin))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/accounts/options?systemAccountId=sys_user&ids=acct_a,acct_b&ids=acct_a&page=3&limit=500&keyword=%20%E4%B8%BB%20&providerCode=%20openai%20&groupId=group_default&tagIds=tag_a,tag_b&type=api_key&status=active,disabled&schedulable=enabled&sorts=qualityScore:desc", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if service.input.SystemAccountID != "sys_user" ||
		!service.input.IncludeSystemAccountFields ||
		service.input.Page != 3 ||
		service.input.Limit != 500 ||
		service.input.Keyword != "主" ||
		service.input.ProviderCode != "openai" ||
		service.input.GroupID != "group_default" ||
		service.input.Type != "api_key" ||
		service.input.Status != "active,disabled" ||
		service.input.Schedulable != "enabled" {
		t.Fatalf("service input = %+v", service.input)
	}
	if len(service.input.IDs) != 2 || service.input.IDs[0] != "acct_a" || service.input.IDs[1] != "acct_b" {
		t.Fatalf("ids = %#v", service.input.IDs)
	}
	if len(service.input.TagIDs) != 2 || service.input.TagIDs[0] != "tag_a" || service.input.TagIDs[1] != "tag_b" {
		t.Fatalf("tagIds = %#v", service.input.TagIDs)
	}
	var body struct {
		Data []managementaccounts.Option `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data) != 1 || body.Data[0].SystemAccountName != "用户" || body.Data[0].AccessType != "owner" {
		t.Fatalf("body = %+v", body)
	}
}

func TestManagementAccountOptionsHandlerRejectsOrdinaryUser(t *testing.T) {
	service := &managementAccountOptionServiceStub{}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_user", Username: "user", Role: "user", SessionID: "sess_user"},
	})(newManagementAccountOptionsHandler(service, managementAccountScopeAdmin))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/accounts/options?systemAccountId=sys_admin", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if service.called {
		t.Fatal("service should not be called for ordinary user on management route")
	}
}

func TestManagementMyAccountOptionsHandlerForcesSelfScope(t *testing.T) {
	service := &managementAccountOptionServiceStub{}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_user", Username: "user", Role: "user", SessionID: "sess_user"},
	})(newManagementAccountOptionsHandler(service, managementAccountScopeSelf))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-accounts/options?systemAccountId=sys_admin&limit=20", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if service.input.SystemAccountID != "sys_user" || service.input.IncludeSystemAccountFields {
		t.Fatalf("service input = %+v", service.input)
	}
}

func TestManagementAccountOptionsHandlerRedactsStoreErrors(t *testing.T) {
	handler := newManagementAccountOptionsHandler(&managementAccountOptionServiceStub{err: errors.New("postgres password leaked")}, managementAccountScopeSelf)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-accounts/options", nil)
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

func TestManagementAccountTagsHandlerRequiresAdminAndParsesScope(t *testing.T) {
	service := &managementAccountOptionServiceStub{
		tags: []managementaccounts.Tag{{ID: "tag_main", Name: "主力", AccountCount: 2}},
	}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
	})(newManagementAccountTagsHandler(service, managementAccountScopeAdmin))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/accounts/tags?systemAccountId=sys_user", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !service.tagCalled || service.tagInput.SystemAccountID != "sys_user" {
		t.Fatalf("tag input = %+v, called = %v", service.tagInput, service.tagCalled)
	}
	var body struct {
		Data []managementaccounts.Tag `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data) != 1 || body.Data[0].ID != "tag_main" || body.Data[0].AccountCount != 2 {
		t.Fatalf("body = %+v", body)
	}
}

func TestManagementAccountTagsHandlerUsesAdminSelfScopeForAll(t *testing.T) {
	service := &managementAccountOptionServiceStub{}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
	})(newManagementAccountTagsHandler(service, managementAccountScopeAdmin))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/accounts/tags?systemAccountId=all", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if service.tagInput.SystemAccountID != "sys_admin" {
		t.Fatalf("tag input = %+v, want admin current scope", service.tagInput)
	}
}

func TestManagementAccountTagsHandlerRejectsOrdinaryUser(t *testing.T) {
	service := &managementAccountOptionServiceStub{}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_user", Username: "user", Role: "user", SessionID: "sess_user"},
	})(newManagementAccountTagsHandler(service, managementAccountScopeAdmin))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/accounts/tags?systemAccountId=sys_admin", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if service.tagCalled {
		t.Fatal("service should not be called for ordinary user on management tag route")
	}
}

func TestManagementMyAccountTagsHandlerForcesSelfScope(t *testing.T) {
	service := &managementAccountOptionServiceStub{}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_user", Username: "user", Role: "user", SessionID: "sess_user"},
	})(newManagementAccountTagsHandler(service, managementAccountScopeSelf))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-accounts/tags?systemAccountId=sys_admin", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if service.tagInput.SystemAccountID != "sys_user" {
		t.Fatalf("tag input = %+v", service.tagInput)
	}
}

func TestManagementAccountTagsHandlerRedactsStoreErrors(t *testing.T) {
	handler := newManagementAccountTagsHandler(&managementAccountOptionServiceStub{tagErr: errors.New("postgres password leaked")}, managementAccountScopeSelf)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-accounts/tags", nil)
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

func TestRouterRegistersW2ManagementAccountOptions(t *testing.T) {
	service := &managementAccountOptionServiceStub{
		options: []managementaccounts.Option{{ID: "acct_main", OwnerSystemAccountID: "sys_admin", Name: "主账号", ProviderCode: "openai", Type: "api_key", Status: "active", AccessType: "owner"}},
		tags:    []managementaccounts.Tag{{ID: "tag_main", Name: "主力"}},
	}
	router := NewRouter(RouterOptions{
		Config:                            config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                            slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementAccountOptionsHandler:   newManagementAccountOptionsHandler(service, managementAccountScopeAdmin),
		ManagementMyAccountOptionsHandler: newManagementAccountOptionsHandler(service, managementAccountScopeSelf),
		ManagementAccountTagsHandler:      newManagementAccountTagsHandler(service, managementAccountScopeAdmin),
		ManagementMyAccountTagsHandler:    newManagementAccountTagsHandler(service, managementAccountScopeSelf),
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
			context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
		}),
	})

	for _, path := range []string{"/__aisys__/api/accounts/options", "/__aisys__/api/my-accounts/options", "/__aisys__/api/accounts/tags", "/__aisys__/api/my-accounts/tags"} {
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

func TestRouterDoesNotRegisterW2ManagementAccountOptionsWhenDisabled(t *testing.T) {
	router := NewRouter(RouterOptions{
		Config:                          config.Config{Host: "127.0.0.1", Port: 3000},
		Logger:                          slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementAccountOptionsHandler: newManagementAccountOptionsHandler(&managementAccountOptionServiceStub{}, managementAccountScopeAdmin),
		ManagementAccountTagsHandler:    newManagementAccountTagsHandler(&managementAccountOptionServiceStub{}, managementAccountScopeAdmin),
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
			context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
		}),
	})

	for _, path := range []string{"/__aisys__/api/accounts/options", "/__aisys__/api/accounts/tags"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Cookie", "juhe_ai_session=session-token")
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404 while JUHE_AI_MANAGEMENT_API_ENABLED=false", path, rec.Code)
		}
	}
}

type managementAccountOptionServiceStub struct {
	called    bool
	input     managementaccounts.OptionListInput
	options   []managementaccounts.Option
	err       error
	tagCalled bool
	tagInput  managementaccounts.TagListInput
	tags      []managementaccounts.Tag
	tagErr    error
}

func (s *managementAccountOptionServiceStub) Options(_ *http.Request, input managementaccounts.OptionListInput) ([]managementaccounts.Option, error) {
	s.called = true
	s.input = input
	return s.options, s.err
}

func (s *managementAccountOptionServiceStub) Tags(_ *http.Request, input managementaccounts.TagListInput) ([]managementaccounts.Tag, error) {
	s.tagCalled = true
	s.tagInput = input
	return s.tags, s.tagErr
}

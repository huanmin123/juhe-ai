package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementproviders"
)

func TestManagementProviderOptionsHandler(t *testing.T) {
	service := &managementProviderOptionServiceStub{
		options: []managementproviders.Option{
			{ID: "provider_gpt", Code: "gpt", Name: "GPT", Enabled: true},
		},
	}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
	})(newManagementProviderOptionsHandler(service))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/providers/options?systemAccountId=sys_user", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if service.input.SystemAccountID != "sys_user" {
		t.Fatalf("service input = %+v", service.input)
	}
	var body struct {
		Data []managementproviders.Option `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data) != 1 || body.Data[0].Code != "gpt" {
		t.Fatalf("body = %+v", body)
	}
}

func TestManagementProviderOptionsHandlerUsesSelfScopeForOrdinaryUser(t *testing.T) {
	service := &managementProviderOptionServiceStub{}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_user", Username: "user", Role: "user", SessionID: "sess_user"},
	})(newManagementProviderOptionsHandler(service))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/providers/options?systemAccountId=sys_admin", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if service.input.SystemAccountID != "sys_user" {
		t.Fatalf("service input = %+v", service.input)
	}
}

func TestManagementProviderOptionsHandlerRedactsStoreErrors(t *testing.T) {
	handler := newManagementProviderOptionsHandler(&managementProviderOptionServiceStub{err: errors.New("postgres password leaked")})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/providers/options", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := body["message"]; got != "服务器内部错误" {
		t.Fatalf("message = %q", got)
	}
}

func TestRouterRegistersW2ManagementProviderOptionsOnlyWithAuthMiddleware(t *testing.T) {
	service := &managementProviderOptionServiceStub{
		options: []managementproviders.Option{
			{ID: "provider_gpt", Code: "gpt", Name: "GPT", Enabled: true},
		},
	}
	router := NewRouter(RouterOptions{
		Config:                           config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                           slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementProviderOptionsHandler: newManagementProviderOptionsHandler(service),
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
			context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
		}),
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/providers/options", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}

func TestRouterDoesNotRegisterW2ManagementProviderOptionsWhenDisabled(t *testing.T) {
	router := NewRouter(RouterOptions{
		Config:                           config.Config{Host: "127.0.0.1", Port: 3000},
		Logger:                           slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementProviderOptionsHandler: newManagementProviderOptionsHandler(&managementProviderOptionServiceStub{}),
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
			context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
		}),
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/providers/options", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 while JUHE_AI_MANAGEMENT_API_ENABLED=false", rec.Code)
	}
}

type managementProviderOptionServiceStub struct {
	input   managementproviders.OptionListInput
	options []managementproviders.Option
	err     error
}

func (s *managementProviderOptionServiceStub) Options(_ *http.Request, input managementproviders.OptionListInput) ([]managementproviders.Option, error) {
	s.input = input
	return s.options, s.err
}

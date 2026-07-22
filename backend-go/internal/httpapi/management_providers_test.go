package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementproviders"
)

func TestManagementProviderOptionsHandler(t *testing.T) {
	service := &managementProviderOptionServiceStub{
		selectOptions: []managementproviders.SelectOption{
			{ID: "gpt", Code: "gpt", Name: "GPT", Enabled: true},
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
	var body struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data) != 1 || body.Data[0]["code"] != "gpt" {
		t.Fatalf("body = %+v", body)
	}
	if body.Data[0]["id"] != "gpt" {
		t.Fatalf("provider option id = %v, want provider code", body.Data[0]["id"])
	}
	if got := len(body.Data[0]); got != 4 {
		t.Fatalf("provider option keys = %+v, want id/code/name/enabled only", body.Data[0])
	}
}

func TestManagementProviderDefinitionsHandlerUsesScopedEnabledDefinitions(t *testing.T) {
	service := &managementProviderOptionServiceStub{
		options: []managementproviders.Option{{
			ID:           "provider_gpt",
			Code:         "gpt",
			Name:         "GPT",
			Enabled:      true,
			BaseURL:      "https://example.test/v1",
			AccountTypes: []string{"api_key"},
			ProtocolProfiles: []managementproviders.ProtocolProfile{{
				ID: "profile_gpt_openai_v1", ProviderCode: "gpt", Enabled: true,
			}},
		}},
	}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_user", Username: "user", Role: "user", SessionID: "sess_user"},
	})(newManagementProviderDefinitionsHandler(service))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/providers/definitions?systemAccountId=sys_admin", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if service.input.SystemAccountID != "sys_user" {
		t.Fatalf("service input = %+v, want ordinary-user self scope", service.input)
	}
	var body struct {
		Data []managementproviders.Option `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data) != 1 || body.Data[0].Code != "gpt" || !body.Data[0].Enabled || len(body.Data[0].ProtocolProfiles) != 1 {
		t.Fatalf("body = %+v", body)
	}
}

func TestManagementProvidersHandlerRequiresAdminAndIncludesDisabledProviders(t *testing.T) {
	service := &managementProviderOptionServiceStub{
		providers: []managementproviders.Option{
			{ID: "provider_disabled", Code: "disabled", Name: "Disabled", Enabled: false},
			{
				ID:                            "provider_gpt",
				Code:                          "gpt",
				Name:                          "GPT",
				Enabled:                       true,
				DefaultHealthCheckModel:       "gpt-5-user",
				SystemDefaultHealthCheckModel: "gpt-5-system",
				ProtocolProfiles: []managementproviders.ProtocolProfile{
					{ID: "profile_gpt_openai_v1", DefaultHealthCheckModel: "gpt-5-system"},
				},
			},
		},
	}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
	})(newManagementProvidersHandler(service))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/providers?systemAccountId=sys_user", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if service.listInput.SystemAccountID != "" {
		t.Fatalf("service list input = %+v, want global scope", service.listInput)
	}
	responseJSON := rec.Body.String()
	if !strings.Contains(responseJSON, `"defaultHealthCheckModel":"gpt-5-user"`) ||
		!strings.Contains(responseJSON, `"systemDefaultHealthCheckModel":"gpt-5-system"`) {
		t.Fatalf("response missing provider health check model contract: %s", responseJSON)
	}
	var rawBody struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(responseJSON), &rawBody); err != nil {
		t.Fatalf("decode raw provider response: %v", err)
	}
	if len(rawBody.Data) != 2 {
		t.Fatalf("raw provider response = %+v", rawBody.Data)
	}
	if _, exists := rawBody.Data[0]["systemDefaultHealthCheckModel"]; exists {
		t.Fatalf("unset system default health check model should be omitted: %s", responseJSON)
	}
	if strings.Contains(responseJSON, "defaultTestModel") || strings.Contains(responseJSON, "systemDefaultTestModel") {
		t.Fatalf("response exposes legacy provider model fields: %s", responseJSON)
	}
	var body struct {
		Data []managementproviders.Option `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data) != 2 || body.Data[0].Code != "disabled" || body.Data[0].Enabled {
		t.Fatalf("body = %+v, want disabled provider preserved", body)
	}
	if body.Data[1].DefaultHealthCheckModel != "gpt-5-user" ||
		body.Data[1].SystemDefaultHealthCheckModel != "gpt-5-system" ||
		body.Data[1].ProtocolProfiles[0].DefaultHealthCheckModel != "gpt-5-system" {
		t.Fatalf("provider contract = %+v", body.Data[1])
	}
}

func TestManagementProvidersHandlerRejectsOrdinaryUser(t *testing.T) {
	service := &managementProviderOptionServiceStub{}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_user", Username: "user", Role: "user", SessionID: "sess_user"},
	})(newManagementProvidersHandler(service))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/providers?systemAccountId=sys_admin", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if service.listCalled {
		t.Fatal("service should not be called for ordinary user")
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["message"] != "需要管理员权限" {
		t.Fatalf("body = %+v", body)
	}
}

func TestManagementProviderDefinitionsHandlerUsesSelfScopeForOrdinaryUser(t *testing.T) {
	service := &managementProviderOptionServiceStub{}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_user", Username: "user", Role: "user", SessionID: "sess_user"},
	})(newManagementProviderDefinitionsHandler(service))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/providers/definitions?systemAccountId=sys_admin", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if service.input.SystemAccountID != "sys_user" {
		t.Fatalf("service input = %+v", service.input)
	}
}

func TestManagementProviderDefinitionsHandlerUsesSelfScopeForAdminWithoutTarget(t *testing.T) {
	service := &managementProviderOptionServiceStub{}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
	})(newManagementProviderDefinitionsHandler(service))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/providers/definitions", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if service.input.SystemAccountID != "sys_admin" {
		t.Fatalf("service input = %+v, want admin self scope", service.input)
	}
}

func TestManagementProvidersHandlerRedactsStoreErrors(t *testing.T) {
	service := &managementProviderOptionServiceStub{listErr: errors.New("postgres password leaked")}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
	})(newManagementProvidersHandler(service))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/providers", nil)
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

func TestRouterRegistersW2ManagementProviderHandlersOnlyWithAuthMiddleware(t *testing.T) {
	service := &managementProviderOptionServiceStub{
		providers: []managementproviders.Option{
			{ID: "provider_disabled", Code: "disabled", Name: "Disabled", Enabled: false},
			{ID: "provider_gpt", Code: "gpt", Name: "GPT", Enabled: true},
		},
		options: []managementproviders.Option{
			{ID: "provider_gpt", Code: "gpt", Name: "GPT", Enabled: true},
		},
		selectOptions: []managementproviders.SelectOption{
			{ID: "provider_gpt", Code: "gpt", Name: "GPT", Enabled: true},
		},
	}
	router := NewRouter(RouterOptions{
		Config:                               config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                               slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementProvidersHandler:           newManagementProvidersHandler(service),
		ManagementProviderOptionsHandler:     newManagementProviderOptionsHandler(service),
		ManagementProviderDefinitionsHandler: newManagementProviderDefinitionsHandler(service),
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
			context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
		}),
	})

	for _, path := range []string{"/__aisys__/api/providers", "/__aisys__/api/providers/options", "/__aisys__/api/providers/definitions"} {
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

func TestRouterDoesNotRegisterW2ManagementProviderOptionsWhenDisabled(t *testing.T) {
	router := NewRouter(RouterOptions{
		Config:                               config.Config{Host: "127.0.0.1", Port: 3000},
		Logger:                               slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementProvidersHandler:           newManagementProvidersHandler(&managementProviderOptionServiceStub{}),
		ManagementProviderOptionsHandler:     newManagementProviderOptionsHandler(&managementProviderOptionServiceStub{}),
		ManagementProviderDefinitionsHandler: newManagementProviderDefinitionsHandler(&managementProviderOptionServiceStub{}),
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
			context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
		}),
	})

	for _, path := range []string{"/__aisys__/api/providers", "/__aisys__/api/providers/options", "/__aisys__/api/providers/definitions"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Cookie", "juhe_ai_session=session-token")
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404 while JUHE_AI_MANAGEMENT_API_ENABLED=false", path, rec.Code)
		}
	}
}

type managementProviderOptionServiceStub struct {
	listCalled    bool
	listInput     managementproviders.ListInput
	input         managementproviders.OptionListInput
	providers     []managementproviders.Option
	options       []managementproviders.Option
	selectOptions []managementproviders.SelectOption
	listErr       error
	err           error
}

func (s *managementProviderOptionServiceStub) List(_ *http.Request, input managementproviders.ListInput) ([]managementproviders.Option, error) {
	s.listCalled = true
	s.listInput = input
	return s.providers, s.listErr
}

func (s *managementProviderOptionServiceStub) Options(_ *http.Request, input managementproviders.OptionListInput) ([]managementproviders.Option, error) {
	s.input = input
	return s.options, s.err
}

func (s *managementProviderOptionServiceStub) SelectOptions(*http.Request) ([]managementproviders.SelectOption, error) {
	return s.selectOptions, s.err
}

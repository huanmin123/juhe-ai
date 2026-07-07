package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementprovidermodels"
)

func TestManagementProviderModelOptionsHandlerParsesScopeAndProtocol(t *testing.T) {
	service := &managementProviderModelServiceStub{
		modelOptions: []managementprovidermodels.ModelOption{{ProviderCode: "gpt", Model: "gpt-5.5"}},
	}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
	})(newManagementProviderModelOptionsHandler(service))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/providers/models/options?systemAccountId=sys_user&protocol=openai", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if service.modelOptionsInput.SystemAccountID != "sys_user" || service.modelOptionsInput.Protocol != "openai" {
		t.Fatalf("input = %+v", service.modelOptionsInput)
	}
	var body struct {
		Data []managementprovidermodels.ModelOption `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data) != 1 || body.Data[0].Model != "gpt-5.5" {
		t.Fatalf("body = %+v", body)
	}
}

func TestManagementProviderModelOptionsHandlerUsesSelfScopeForOrdinaryUser(t *testing.T) {
	service := &managementProviderModelServiceStub{}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_user", Username: "user", Role: "user", SessionID: "sess_user"},
	})(newManagementProviderModelOptionsHandler(service))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/providers/models/options?systemAccountId=sys_admin", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if service.modelOptionsInput.SystemAccountID != "sys_user" {
		t.Fatalf("input = %+v", service.modelOptionsInput)
	}
}

func TestManagementProviderModelsHandlerParsesQueryAndProvider(t *testing.T) {
	service := &managementProviderModelServiceStub{
		models: []managementprovidermodels.ModelCatalogItem{{ProviderCode: "gpt", Model: "gpt-5.5", Scope: "built_in", Status: "active"}},
	}
	handler := chi.NewRouter()
	handler.With(NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
	})).Get("/__aisys__/api/providers/{code}/models", newManagementProviderModelsHandler(service).ServeHTTP)

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/providers/gpt/models?systemAccountId=sys_user&includeInactive=true&includeUnpriced=yes", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if service.modelsInput.ProviderCode != "gpt" ||
		service.modelsInput.SystemAccountID != "sys_user" ||
		!service.modelsInput.IncludeInactive ||
		!service.modelsInput.IncludeUnpriced {
		t.Fatalf("input = %+v", service.modelsInput)
	}
}

func TestManagementProviderModelsHandlerNotFound(t *testing.T) {
	service := &managementProviderModelServiceStub{err: managementprovidermodels.ErrProviderNotFound}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
	})(newManagementProviderModelsHandler(service))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/providers/missing/models", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["message"] != "供应商不存在" {
		t.Fatalf("body = %+v", body)
	}
}

func TestManagementProviderModelsHandlerRedactsStoreErrors(t *testing.T) {
	service := &managementProviderModelServiceStub{err: errors.New("postgres password leaked")}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
	})(newManagementProviderModelsHandler(service))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/providers/gpt/models", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if got := rec.Body.String(); got != "{\"message\":\"服务器内部错误\"}\n" {
		t.Fatalf("body = %s", got)
	}
}

func TestRouterRegistersW2ManagementProviderModelHandlers(t *testing.T) {
	service := &managementProviderModelServiceStub{
		modelOptions: []managementprovidermodels.ModelOption{{ProviderCode: "gpt", Model: "gpt-5.5"}},
		models:       []managementprovidermodels.ModelCatalogItem{{ProviderCode: "gpt", Model: "gpt-5.5", Scope: "built_in", Status: "active"}},
	}
	router := NewRouter(RouterOptions{
		Config:                                config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                                slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementProviderModelOptionsHandler: newManagementProviderModelOptionsHandler(service),
		ManagementProviderModelsHandler:       newManagementProviderModelsHandler(service),
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
			context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
		}),
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/providers/models/options", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("model options status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/__aisys__/api/providers/gpt/models", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("models status = %d, want 200", rec.Code)
	}
}

func TestRouterDoesNotRegisterW2ManagementProviderModelHandlersWhenDisabled(t *testing.T) {
	router := NewRouter(RouterOptions{
		Config:                                config.Config{Host: "127.0.0.1", Port: 3000},
		Logger:                                slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementProviderModelOptionsHandler: newManagementProviderModelOptionsHandler(&managementProviderModelServiceStub{}),
		ManagementProviderModelsHandler:       newManagementProviderModelsHandler(&managementProviderModelServiceStub{}),
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
			context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
		}),
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/providers/models/options", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 while JUHE_AI_MANAGEMENT_API_ENABLED=false", rec.Code)
	}
}

type managementProviderModelServiceStub struct {
	modelOptionsInput managementprovidermodels.ModelOptionListInput
	modelsInput       managementprovidermodels.ModelListInput
	modelOptions      []managementprovidermodels.ModelOption
	models            []managementprovidermodels.ModelCatalogItem
	err               error
}

func (s *managementProviderModelServiceStub) ModelOptions(_ *http.Request, input managementprovidermodels.ModelOptionListInput) ([]managementprovidermodels.ModelOption, error) {
	s.modelOptionsInput = input
	return s.modelOptions, s.err
}

func (s *managementProviderModelServiceStub) Models(_ *http.Request, input managementprovidermodels.ModelListInput) ([]managementprovidermodels.ModelCatalogItem, error) {
	s.modelsInput = input
	return s.models, s.err
}

package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestManagementProviderDefaultTestModelHandlerParsesAdminTargetScope(t *testing.T) {
	service := &managementProviderModelServiceStub{
		defaultTestModelResult: managementprovidermodels.DefaultTestModelResult{ProviderCode: "gpt", DefaultTestModel: "gpt-5.5"},
	}
	handler := chi.NewRouter()
	handler.With(NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
	})).Put("/__aisys__/api/providers/{code}/default-test-model", newManagementProviderDefaultTestModelHandler(service).ServeHTTP)

	req := httptest.NewRequest(http.MethodPut, "/__aisys__/api/providers/gpt/default-test-model?systemAccountId=sys_user", strings.NewReader(`{"model":" gpt-5.5 "}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if service.defaultTestModelInput.ProviderCode != "gpt" ||
		service.defaultTestModelInput.SystemAccountID != "sys_user" ||
		service.defaultTestModelInput.Model != " gpt-5.5 " {
		t.Fatalf("input = %+v", service.defaultTestModelInput)
	}
	var body struct {
		Data managementprovidermodels.DefaultTestModelResult `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.DefaultTestModel != "gpt-5.5" {
		t.Fatalf("body = %+v", body)
	}
}

func TestManagementProviderDefaultTestModelHandlerUsesSelfScopeForOrdinaryUser(t *testing.T) {
	service := &managementProviderModelServiceStub{
		defaultTestModelResult: managementprovidermodels.DefaultTestModelResult{ProviderCode: "gpt", DefaultTestModel: "gpt-5.5"},
	}
	handler := chi.NewRouter()
	handler.With(NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_user", Username: "user", Role: "user", SessionID: "sess_user"},
	})).Put("/__aisys__/api/providers/{code}/default-test-model", newManagementProviderDefaultTestModelHandler(service).ServeHTTP)

	req := httptest.NewRequest(http.MethodPut, "/__aisys__/api/providers/gpt/default-test-model?systemAccountId=sys_admin", strings.NewReader(`{"model":"gpt-5.5"}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if service.defaultTestModelInput.SystemAccountID != "sys_user" {
		t.Fatalf("input = %+v", service.defaultTestModelInput)
	}
}

func TestManagementProviderDefaultTestModelHandlerErrors(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		err        error
		wantStatus int
		wantMsg    string
	}{
		{name: "invalid body", body: `{"model":""}`, wantStatus: http.StatusBadRequest, wantMsg: "默认测试模型参数无效"},
		{name: "provider not found", body: `{"model":"gpt-5.5"}`, err: managementprovidermodels.ErrProviderNotFound, wantStatus: http.StatusNotFound, wantMsg: "供应商不存在"},
		{name: "validation", body: `{"model":"missing"}`, err: &managementprovidermodels.DefaultTestModelValidationError{Message: "模型不在当前用户可见目录中：missing"}, wantStatus: http.StatusBadRequest, wantMsg: "模型不在当前用户可见目录中：missing"},
		{name: "store error", body: `{"model":"gpt-5.5"}`, err: errors.New("postgres password leaked"), wantStatus: http.StatusInternalServerError, wantMsg: "服务器内部错误"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementProviderModelServiceStub{err: tt.err}
			handler := chi.NewRouter()
			handler.With(NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
				context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
			})).Put("/__aisys__/api/providers/{code}/default-test-model", newManagementProviderDefaultTestModelHandler(service).ServeHTTP)

			req := httptest.NewRequest(http.MethodPut, "/__aisys__/api/providers/gpt/default-test-model", strings.NewReader(tt.body))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			var body map[string]string
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if body["message"] != tt.wantMsg {
				t.Fatalf("message = %q, want %q", body["message"], tt.wantMsg)
			}
		})
	}
}

func TestRouterRegistersW2ManagementProviderModelHandlers(t *testing.T) {
	service := &managementProviderModelServiceStub{
		modelOptions:           []managementprovidermodels.ModelOption{{ProviderCode: "gpt", Model: "gpt-5.5"}},
		models:                 []managementprovidermodels.ModelCatalogItem{{ProviderCode: "gpt", Model: "gpt-5.5", Scope: "built_in", Status: "active"}},
		defaultTestModelResult: managementprovidermodels.DefaultTestModelResult{ProviderCode: "gpt", DefaultTestModel: "gpt-5.5"},
	}
	router := NewRouter(RouterOptions{
		Config:                                config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                                slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementProviderModelOptionsHandler: newManagementProviderModelOptionsHandler(service),
		ManagementProviderModelsHandler:       newManagementProviderModelsHandler(service),
		ManagementProviderDefaultTestModelHandler: newManagementProviderDefaultTestModelHandler(service),
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

	req = httptest.NewRequest(http.MethodPut, "/__aisys__/api/providers/gpt/default-test-model", strings.NewReader(`{"model":"gpt-5.5"}`))
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("default test model status = %d, want 200", rec.Code)
	}
}

func TestRouterDoesNotRegisterW2ManagementProviderModelHandlersWhenDisabled(t *testing.T) {
	router := NewRouter(RouterOptions{
		Config:                                config.Config{Host: "127.0.0.1", Port: 3000},
		Logger:                                slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementProviderModelOptionsHandler: newManagementProviderModelOptionsHandler(&managementProviderModelServiceStub{}),
		ManagementProviderModelsHandler:       newManagementProviderModelsHandler(&managementProviderModelServiceStub{}),
		ManagementProviderDefaultTestModelHandler: newManagementProviderDefaultTestModelHandler(&managementProviderModelServiceStub{}),
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

	req = httptest.NewRequest(http.MethodPut, "/__aisys__/api/providers/gpt/default-test-model", strings.NewReader(`{"model":"gpt-5.5"}`))
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("put status = %d, want 404 while JUHE_AI_MANAGEMENT_API_ENABLED=false", rec.Code)
	}
}

type managementProviderModelServiceStub struct {
	modelOptionsInput      managementprovidermodels.ModelOptionListInput
	modelsInput            managementprovidermodels.ModelListInput
	defaultTestModelInput  managementprovidermodels.DefaultTestModelInput
	modelOptions           []managementprovidermodels.ModelOption
	models                 []managementprovidermodels.ModelCatalogItem
	defaultTestModelResult managementprovidermodels.DefaultTestModelResult
	err                    error
}

func (s *managementProviderModelServiceStub) ModelOptions(_ *http.Request, input managementprovidermodels.ModelOptionListInput) ([]managementprovidermodels.ModelOption, error) {
	s.modelOptionsInput = input
	return s.modelOptions, s.err
}

func (s *managementProviderModelServiceStub) Models(_ *http.Request, input managementprovidermodels.ModelListInput) ([]managementprovidermodels.ModelCatalogItem, error) {
	s.modelsInput = input
	return s.models, s.err
}

func (s *managementProviderModelServiceStub) SetDefaultTestModel(_ *http.Request, input managementprovidermodels.DefaultTestModelInput) (managementprovidermodels.DefaultTestModelResult, error) {
	s.defaultTestModelInput = input
	return s.defaultTestModelResult, s.err
}

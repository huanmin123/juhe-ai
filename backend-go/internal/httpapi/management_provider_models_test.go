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

func TestManagementProviderDefaultHealthCheckModelHandlerParsesAdminTargetScope(t *testing.T) {
	service := &managementProviderModelServiceStub{
		defaultHealthCheckModelResult: managementprovidermodels.DefaultHealthCheckModelResult{ProviderCode: "gpt", DefaultHealthCheckModel: "gpt-5.5"},
	}
	handler := chi.NewRouter()
	handler.With(NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
	})).Put("/__aisys__/api/providers/{code}/default-health-check-model", newManagementProviderDefaultHealthCheckModelHandler(service).ServeHTTP)

	req := httptest.NewRequest(http.MethodPut, "/__aisys__/api/providers/gpt/default-health-check-model?systemAccountId=sys_user", strings.NewReader(`{"model":" gpt-5.5 "}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if service.defaultHealthCheckModelInput.ProviderCode != "gpt" ||
		service.defaultHealthCheckModelInput.SystemAccountID != "sys_user" ||
		service.defaultHealthCheckModelInput.Model != " gpt-5.5 " {
		t.Fatalf("input = %+v", service.defaultHealthCheckModelInput)
	}
	responseJSON := rec.Body.String()
	if !strings.Contains(responseJSON, `"defaultHealthCheckModel":"gpt-5.5"`) {
		t.Fatalf("response missing default health check model: %s", responseJSON)
	}
	if strings.Contains(responseJSON, "defaultTestModel") {
		t.Fatalf("response exposes legacy default test model field: %s", responseJSON)
	}
	var body struct {
		Data managementprovidermodels.DefaultHealthCheckModelResult `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.DefaultHealthCheckModel != "gpt-5.5" {
		t.Fatalf("body = %+v", body)
	}
}

func TestManagementProviderDefaultHealthCheckModelHandlerUsesSelfScopeForOrdinaryUser(t *testing.T) {
	service := &managementProviderModelServiceStub{
		defaultHealthCheckModelResult: managementprovidermodels.DefaultHealthCheckModelResult{ProviderCode: "gpt", DefaultHealthCheckModel: "gpt-5.5"},
	}
	handler := chi.NewRouter()
	handler.With(NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_user", Username: "user", Role: "user", SessionID: "sess_user"},
	})).Put("/__aisys__/api/providers/{code}/default-health-check-model", newManagementProviderDefaultHealthCheckModelHandler(service).ServeHTTP)

	req := httptest.NewRequest(http.MethodPut, "/__aisys__/api/providers/gpt/default-health-check-model?systemAccountId=sys_admin", strings.NewReader(`{"model":"gpt-5.5"}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if service.defaultHealthCheckModelInput.SystemAccountID != "sys_user" {
		t.Fatalf("input = %+v", service.defaultHealthCheckModelInput)
	}
}

func TestManagementProviderDefaultHealthCheckModelHandlerErrors(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		err        error
		wantStatus int
		wantMsg    string
	}{
		{name: "invalid body", body: `{"model":""}`, wantStatus: http.StatusBadRequest, wantMsg: "默认检查模型参数无效"},
		{name: "trailing json", body: `{"model":"gpt-5.5"}{"model":"gpt-5.6-sol"}`, wantStatus: http.StatusBadRequest, wantMsg: "默认检查模型参数无效"},
		{name: "provider not found", body: `{"model":"gpt-5.5"}`, err: managementprovidermodels.ErrProviderNotFound, wantStatus: http.StatusNotFound, wantMsg: "供应商不存在"},
		{name: "validation", body: `{"model":"missing"}`, err: &managementprovidermodels.DefaultHealthCheckModelValidationError{Message: "模型不在当前用户可见目录中：missing"}, wantStatus: http.StatusBadRequest, wantMsg: "模型不在当前用户可见目录中：missing"},
		{name: "store error", body: `{"model":"gpt-5.5"}`, err: errors.New("postgres password leaked"), wantStatus: http.StatusInternalServerError, wantMsg: "服务器内部错误"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementProviderModelServiceStub{err: tt.err}
			handler := chi.NewRouter()
			handler.With(NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
				context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
			})).Put("/__aisys__/api/providers/{code}/default-health-check-model", newManagementProviderDefaultHealthCheckModelHandler(service).ServeHTTP)

			req := httptest.NewRequest(http.MethodPut, "/__aisys__/api/providers/gpt/default-health-check-model", strings.NewReader(tt.body))
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

func TestManagementProviderCustomModelCreateHandlerParsesBodyAndTargetScope(t *testing.T) {
	service := &managementProviderModelServiceStub{
		customModelResult: managementprovidermodels.ModelCatalogItem{ID: "custom_model_1", ProviderCode: "gpt", Model: "custom-chat", Scope: "personal", Status: "active"},
	}
	handler := chi.NewRouter()
	handler.With(NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
	})).Post("/__aisys__/api/providers/{code}/models", newManagementProviderCustomModelCreateHandler(service).ServeHTTP)

	req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/providers/gpt/models?systemAccountId=sys_user", strings.NewReader(`{
		"model":" custom-chat ",
		"supportedApiProtocols":["responses"],
		"inputUsdPer1M":1.25,
		"pricingNotes":" 说明 "
	}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if service.createInput.ProviderCode != "gpt" ||
		service.createInput.ActorSystemAccountID != "sys_admin" ||
		service.createInput.TargetSystemAccountID != "sys_user" ||
		!service.createInput.Fields.Model.Set ||
		service.createInput.Fields.Model.Value != " custom-chat " ||
		service.createInput.Fields.InputUSDPer1M.Value == nil ||
		*service.createInput.Fields.InputUSDPer1M.Value != 1.25 {
		t.Fatalf("create input = %+v", service.createInput)
	}
	var body struct {
		Data managementprovidermodels.ModelCatalogItem `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.ID != "custom_model_1" {
		t.Fatalf("body = %+v", body)
	}
}

func TestManagementProviderCustomModelHandlersMapErrors(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		target     string
		body       string
		err        error
		wantStatus int
		wantMsg    string
	}{
		{
			name:       "create invalid body",
			method:     http.MethodPost,
			target:     "/__aisys__/api/providers/gpt/models",
			body:       `{"model":null}`,
			wantStatus: http.StatusBadRequest,
			wantMsg:    "自定义模型参数无效",
		},
		{
			name:       "create provider not found before invalid body",
			method:     http.MethodPost,
			target:     "/__aisys__/api/providers/missing/models",
			body:       `{"unknown":true}`,
			err:        managementprovidermodels.ErrProviderNotFound,
			wantStatus: http.StatusNotFound,
			wantMsg:    "供应商不存在",
		},
		{
			name:       "create provider not found before non object body",
			method:     http.MethodPost,
			target:     "/__aisys__/api/providers/missing/models",
			body:       `[]`,
			err:        managementprovidermodels.ErrProviderNotFound,
			wantStatus: http.StatusNotFound,
			wantMsg:    "供应商不存在",
		},
		{
			name:       "create int overflow",
			method:     http.MethodPost,
			target:     "/__aisys__/api/providers/gpt/models",
			body:       `{"model":"custom-chat","maxOutputTokens":2147483648}`,
			wantStatus: http.StatusBadRequest,
			wantMsg:    "自定义模型参数无效",
		},
		{
			name:       "update not found",
			method:     http.MethodPatch,
			target:     "/__aisys__/api/providers/gpt/models/custom_model_1",
			body:       `{"status":"disabled"}`,
			err:        managementprovidermodels.ErrCustomProviderModelNotFound,
			wantStatus: http.StatusNotFound,
			wantMsg:    "自定义模型不存在",
		},
		{
			name:       "update not found before empty patch",
			method:     http.MethodPatch,
			target:     "/__aisys__/api/providers/gpt/models/missing",
			body:       `{}`,
			err:        managementprovidermodels.ErrCustomProviderModelNotFound,
			wantStatus: http.StatusNotFound,
			wantMsg:    "自定义模型不存在",
		},
		{
			name:       "update forbidden",
			method:     http.MethodPatch,
			target:     "/__aisys__/api/providers/gpt/models/custom_model_1",
			body:       `{"status":"disabled"}`,
			err:        &managementprovidermodels.CustomModelForbiddenError{Message: "无权修改该自定义模型"},
			wantStatus: http.StatusForbidden,
			wantMsg:    "无权修改该自定义模型",
		},
		{
			name:       "update forbidden before empty patch",
			method:     http.MethodPatch,
			target:     "/__aisys__/api/providers/gpt/models/custom_model_1",
			body:       `{}`,
			err:        &managementprovidermodels.CustomModelForbiddenError{Message: "无权修改该自定义模型"},
			wantStatus: http.StatusForbidden,
			wantMsg:    "无权修改该自定义模型",
		},
		{
			name:       "update forbidden before null body",
			method:     http.MethodPatch,
			target:     "/__aisys__/api/providers/gpt/models/custom_model_1",
			body:       `null`,
			err:        &managementprovidermodels.CustomModelForbiddenError{Message: "无权修改该自定义模型"},
			wantStatus: http.StatusForbidden,
			wantMsg:    "无权修改该自定义模型",
		},
		{
			name:       "delete bound",
			method:     http.MethodDelete,
			target:     "/__aisys__/api/providers/gpt/models/custom_model_1",
			err:        &managementprovidermodels.CustomModelBoundError{Message: "模型已绑定 AI 账户，不能删除；请先从1 个账户支持模型中移除后再删除"},
			wantStatus: http.StatusConflict,
			wantMsg:    "模型已绑定 AI 账户，不能删除；请先从1 个账户支持模型中移除后再删除",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementProviderModelServiceStub{err: tt.err}
			router := chi.NewRouter()
			router.With(NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
				context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
			})).Post("/__aisys__/api/providers/{code}/models", newManagementProviderCustomModelCreateHandler(service).ServeHTTP)
			router.With(NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
				context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
			})).Patch("/__aisys__/api/providers/{code}/models/{id}", newManagementProviderCustomModelUpdateHandler(service).ServeHTTP)
			router.With(NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
				context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
			})).Delete("/__aisys__/api/providers/{code}/models/{id}", newManagementProviderCustomModelDeleteHandler(service).ServeHTTP)

			req := httptest.NewRequest(tt.method, tt.target, strings.NewReader(tt.body))
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

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
		modelOptions:                  []managementprovidermodels.ModelOption{{ProviderCode: "gpt", Model: "gpt-5.5"}},
		models:                        []managementprovidermodels.ModelCatalogItem{{ProviderCode: "gpt", Model: "gpt-5.5", Scope: "built_in", Status: "active"}},
		defaultHealthCheckModelResult: managementprovidermodels.DefaultHealthCheckModelResult{ProviderCode: "gpt", DefaultHealthCheckModel: "gpt-5.5"},
		customModelResult:             managementprovidermodels.ModelCatalogItem{ID: "custom_model_1", ProviderCode: "gpt", Model: "custom-chat", Scope: "personal", Status: "active"},
		deleteResult:                  managementprovidermodels.CustomModelDeleteResult{Deleted: true},
	}
	readAuthenticator := &managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_read"},
	}
	touchAuthenticator := &managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_touch"},
	}
	router := NewRouter(RouterOptions{
		Config:                                config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                                slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementProviderModelOptionsHandler: newManagementProviderModelOptionsHandler(service),
		ManagementProviderModelsHandler:       newManagementProviderModelsHandler(service),
		ManagementProviderDefaultHealthCheckModelHandler: newManagementProviderDefaultHealthCheckModelHandler(service),
		ManagementProviderCustomModelCreateHandler:       newManagementProviderCustomModelCreateHandler(service),
		ManagementProviderCustomModelUpdateHandler:       newManagementProviderCustomModelUpdateHandler(service),
		ManagementProviderCustomModelDeleteHandler:       newManagementProviderCustomModelDeleteHandler(service),
		ManagementAPIAuthMiddleware:                      NewManagementAPIAuthMiddleware(readAuthenticator),
		ManagementAPIAuthTouchMiddleware:                 NewManagementAPIAuthTouchMiddleware(touchAuthenticator),
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

	req = httptest.NewRequest(http.MethodPut, "/__aisys__/api/providers/gpt/default-health-check-model", strings.NewReader(`{"model":"gpt-5.5"}`))
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("default health check model status = %d, want 200", rec.Code)
	}
	if readAuthenticator.cookieHeader == "" || touchAuthenticator.touchCookieHeader == "" {
		t.Fatalf("auth headers read=%q touch=%q", readAuthenticator.cookieHeader, touchAuthenticator.touchCookieHeader)
	}

	req = httptest.NewRequest(http.MethodPut, "/__aisys__/api/providers/gpt/default-test-model", strings.NewReader(`{"model":"gpt-5.5"}`))
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("legacy default test model route status = %d, want 404", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/__aisys__/api/providers/gpt/models", strings.NewReader(`{"model":"custom-chat","inputUsdPer1M":1}`))
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("custom create status = %d, body = %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPatch, "/__aisys__/api/providers/gpt/models/custom_model_1", strings.NewReader(`{"status":"disabled"}`))
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("custom update status = %d, body = %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodDelete, "/__aisys__/api/providers/gpt/models/custom_model_1", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("custom delete status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestRouterDoesNotRegisterW2ManagementProviderModelHandlersWhenDisabled(t *testing.T) {
	router := NewRouter(RouterOptions{
		Config:                                config.Config{Host: "127.0.0.1", Port: 3000},
		Logger:                                slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementProviderModelOptionsHandler: newManagementProviderModelOptionsHandler(&managementProviderModelServiceStub{}),
		ManagementProviderModelsHandler:       newManagementProviderModelsHandler(&managementProviderModelServiceStub{}),
		ManagementProviderDefaultHealthCheckModelHandler: newManagementProviderDefaultHealthCheckModelHandler(&managementProviderModelServiceStub{}),
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

	req = httptest.NewRequest(http.MethodPut, "/__aisys__/api/providers/gpt/default-health-check-model", strings.NewReader(`{"model":"gpt-5.5"}`))
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("put status = %d, want 404 while JUHE_AI_MANAGEMENT_API_ENABLED=false", rec.Code)
	}
}

type managementProviderModelServiceStub struct {
	modelOptionsInput             managementprovidermodels.ModelOptionListInput
	modelsInput                   managementprovidermodels.ModelListInput
	defaultHealthCheckModelInput  managementprovidermodels.DefaultHealthCheckModelInput
	createInput                   managementprovidermodels.CustomModelCreateInput
	updateInput                   managementprovidermodels.CustomModelUpdateInput
	deleteInput                   managementprovidermodels.CustomModelDeleteInput
	modelOptions                  []managementprovidermodels.ModelOption
	models                        []managementprovidermodels.ModelCatalogItem
	defaultHealthCheckModelResult managementprovidermodels.DefaultHealthCheckModelResult
	customModelResult             managementprovidermodels.ModelCatalogItem
	deleteResult                  managementprovidermodels.CustomModelDeleteResult
	err                           error
}

func (s *managementProviderModelServiceStub) ModelOptions(_ *http.Request, input managementprovidermodels.ModelOptionListInput) ([]managementprovidermodels.ModelOption, error) {
	s.modelOptionsInput = input
	return s.modelOptions, s.err
}

func (s *managementProviderModelServiceStub) Models(_ *http.Request, input managementprovidermodels.ModelListInput) ([]managementprovidermodels.ModelCatalogItem, error) {
	s.modelsInput = input
	return s.models, s.err
}

func (s *managementProviderModelServiceStub) SetDefaultHealthCheckModel(_ *http.Request, input managementprovidermodels.DefaultHealthCheckModelInput) (managementprovidermodels.DefaultHealthCheckModelResult, error) {
	s.defaultHealthCheckModelInput = input
	return s.defaultHealthCheckModelResult, s.err
}

func (s *managementProviderModelServiceStub) CreateCustomModel(_ *http.Request, input managementprovidermodels.CustomModelCreateInput) (managementprovidermodels.ModelCatalogItem, error) {
	s.createInput = input
	if s.err != nil {
		return s.customModelResult, s.err
	}
	if input.Fields.Invalid {
		return managementprovidermodels.ModelCatalogItem{}, &managementprovidermodels.CustomModelValidationError{Message: "自定义模型参数无效"}
	}
	return s.customModelResult, s.err
}

func (s *managementProviderModelServiceStub) UpdateCustomModel(_ *http.Request, input managementprovidermodels.CustomModelUpdateInput) (managementprovidermodels.ModelCatalogItem, error) {
	s.updateInput = input
	if s.err != nil {
		return s.customModelResult, s.err
	}
	if input.Fields.Invalid {
		return managementprovidermodels.ModelCatalogItem{}, &managementprovidermodels.CustomModelValidationError{Message: "自定义模型参数无效"}
	}
	return s.customModelResult, s.err
}

func (s *managementProviderModelServiceStub) DeleteCustomModel(_ *http.Request, input managementprovidermodels.CustomModelDeleteInput) (managementprovidermodels.CustomModelDeleteResult, error) {
	s.deleteInput = input
	return s.deleteResult, s.err
}

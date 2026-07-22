package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/managementaccounttestoptions"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/store/port"
)

func TestManagementAccountTestOptionsHandlerAdminPermissionAndScope(t *testing.T) {
	tests := []struct {
		name                string
		role                string
		query               string
		wantStatus          int
		wantSystemAccountID string
		wantCalls           int
	}{
		{name: "admin global", role: "admin", wantStatus: http.StatusOK, wantCalls: 1},
		{name: "super admin empty is global", role: "super_admin", query: "?systemAccountId=", wantStatus: http.StatusOK, wantCalls: 1},
		{name: "admin blank is global", role: "admin", query: "?systemAccountId=%20%20", wantStatus: http.StatusOK, wantCalls: 1},
		{name: "admin all is global", role: "admin", query: "?systemAccountId=%20all%20", wantStatus: http.StatusOK, wantCalls: 1},
		{name: "admin narrows scope", role: "admin", query: "?systemAccountId=%20sys_target%20", wantStatus: http.StatusOK, wantSystemAccountID: "sys_target", wantCalls: 1},
		{name: "user forbidden", role: "user", query: "?systemAccountId=sys_target", wantStatus: http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementAccountTestOptionsServiceStub{
				result: managementaccounttestoptions.Result{AccountID: "acct_1"},
				found:  true,
			}
			handler := newManagementAccountTestOptionsHandler(service, managementAccountTestOptionsScopeAdmin)
			authContext := managementauth.Context{SystemAccountID: "sys_actor", Role: tt.role}
			req := managementAccountTestOptionsRequest(
				http.MethodGet,
				"/__aisys__/api/accounts/acct_1/test-options"+tt.query,
				"acct_1",
				&authContext,
			)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if service.calls != tt.wantCalls {
				t.Fatalf("service calls = %d, want %d", service.calls, tt.wantCalls)
			}
			if tt.wantCalls == 1 && (service.input.AccountID != "acct_1" || service.input.SystemAccountID != tt.wantSystemAccountID) {
				t.Fatalf("service input = %+v", service.input)
			}
		})
	}
}

func TestManagementMyAccountTestOptionsHandlerForcesCurrentScope(t *testing.T) {
	service := &managementAccountTestOptionsServiceStub{
		result: managementaccounttestoptions.Result{AccountID: "acct_1"},
		found:  true,
	}
	handler := newManagementAccountTestOptionsHandler(service, managementAccountTestOptionsScopeSelf)
	authContext := managementauth.Context{SystemAccountID: "sys_current", Role: "user"}
	req := managementAccountTestOptionsRequest(
		http.MethodGet,
		"/__aisys__/api/my-accounts/acct_1/test-options?systemAccountId=sys_forged&systemAccountId=all",
		"acct_1",
		&authContext,
	)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if service.calls != 1 || service.input.AccountID != "acct_1" || service.input.SystemAccountID != "sys_current" {
		t.Fatalf("service calls = %d, input = %+v", service.calls, service.input)
	}
}

func TestManagementAccountTestOptionsHandlerMapsErrorsWithoutLeakingInternals(t *testing.T) {
	tests := []struct {
		name        string
		found       bool
		err         error
		wantStatus  int
		wantMessage string
	}{
		{name: "not found", wantStatus: http.StatusNotFound, wantMessage: "账户不存在"},
		{
			name:        "validation",
			err:         &managementaccounttestoptions.ValidationError{Message: "账户检查模型不可用"},
			wantStatus:  http.StatusBadRequest,
			wantMessage: "账户检查模型不可用",
		},
		{
			name:        "internal",
			found:       true,
			err:         errors.New("postgres password=secret connection failed"),
			wantStatus:  http.StatusInternalServerError,
			wantMessage: "服务器内部错误",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementAccountTestOptionsServiceStub{found: tt.found, err: tt.err}
			handler := newManagementAccountTestOptionsHandler(service, managementAccountTestOptionsScopeSelf)
			authContext := managementauth.Context{SystemAccountID: "sys_current", Role: "user"}
			req := managementAccountTestOptionsRequest(
				http.MethodGet,
				"/__aisys__/api/my-accounts/acct_1/test-options",
				"acct_1",
				&authContext,
			)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body["message"] != tt.wantMessage {
				t.Fatalf("message = %q, want %q", body["message"], tt.wantMessage)
			}
			if strings.Contains(rec.Body.String(), "postgres") || strings.Contains(rec.Body.String(), "password") || strings.Contains(rec.Body.String(), "secret") {
				t.Fatalf("response leaked internal error: %s", rec.Body.String())
			}
		})
	}
}

func TestManagementAccountTestOptionsHandlerReturnsExactDTO(t *testing.T) {
	service := &managementAccountTestOptionsServiceStub{
		result: managementaccounttestoptions.Result{
			AccountID:    "acct_1",
			DefaultModel: "gpt-5.2",
			Models: []managementaccounttestoptions.ModelOption{
				{
					Model:                 "gpt-5.2",
					SupportedAPIProtocols: []string{"responses", "chat_completions"},
					TestEndpointModes:     []string{"responses_sse", "chat_sse"},
				},
			},
			TestEndpointModes:       []string{"responses_sse", "chat_sse"},
			DefaultTestEndpointMode: "responses_sse",
		},
		found: true,
	}
	handler := newManagementAccountTestOptionsHandler(service, managementAccountTestOptionsScopeSelf)
	authContext := managementauth.Context{SystemAccountID: "sys_current", Role: "user"}
	req := managementAccountTestOptionsRequest(
		http.MethodGet,
		"/__aisys__/api/my-accounts/acct_1/test-options?keyword=%EF%BB%BFgpt%EF%BB%BF&limit=7&selectedIds=gpt-5.2&selectedIds=gpt-5.3%2Cgpt-5.4",
		"acct_1",
		&authContext,
	)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	var got any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	var want any
	if err := json.Unmarshal([]byte(`{
		"data": [{"id": "gpt-5.2", "name": "gpt-5.2"}]
	}`), &want); err != nil {
		t.Fatalf("decode expected response: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("response = %s", rec.Body.String())
	}
	if !reflect.DeepEqual(service.optionsInput, managementaccounttestoptions.OptionsInput{
		AccountID: "acct_1", SystemAccountID: "sys_current", Keyword: "gpt", Limit: 7,
		SelectedIDs: []string{"gpt-5.2", "gpt-5.3,gpt-5.4"},
	}) {
		t.Fatalf("options input = %#v", service.optionsInput)
	}
}

func TestManagementAccountTestOptionsHandlerRejectsInvalidLimit(t *testing.T) {
	service := &managementAccountTestOptionsServiceStub{found: true}
	handler := newManagementAccountTestOptionsHandler(service, managementAccountTestOptionsScopeSelf)
	authContext := managementauth.Context{SystemAccountID: "sys_current", Role: "user"}
	req := managementAccountTestOptionsRequest(
		http.MethodGet,
		"/__aisys__/api/my-accounts/acct_1/test-options?limit=51",
		"acct_1",
		&authContext,
	)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
	if service.calls != 0 {
		t.Fatalf("service calls = %d, want 0", service.calls)
	}
}

func TestManagementAccountTestModelCapabilitiesHandlerReturnsExactDTO(t *testing.T) {
	service := &managementAccountTestOptionsServiceStub{
		result: managementaccounttestoptions.Result{
			Models: []managementaccounttestoptions.ModelOption{
				{Model: "vendor/model", TestEndpointModes: []string{"responses_json", "responses_sse"}},
			},
		},
		found: true,
	}
	handler := newManagementAccountTestOptionsHandler(service, managementAccountTestOptionsScopeSelf)
	authContext := managementauth.Context{SystemAccountID: "sys_current", Role: "user"}
	req := managementAccountTestOptionsRequest(
		http.MethodGet,
		"/__aisys__/api/my-accounts/acct_1/test-options/models/vendor%2Fmodel",
		"acct_1",
		&authContext,
	)
	routeContext := chi.RouteContext(req.Context())
	routeContext.URLParams.Add("modelId", "vendor%2Fmodel")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	var got any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	var want any
	if err := json.Unmarshal([]byte(`{
		"data": {"id": "vendor/model", "name": "vendor/model", "testEndpointModes": ["responses_json", "responses_sse"]}
	}`), &want); err != nil {
		t.Fatalf("decode expected response: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("response = %s", rec.Body.String())
	}
	if !reflect.DeepEqual(service.capabilitiesInput, managementaccounttestoptions.ModelCapabilitiesInput{
		AccountID: "acct_1", SystemAccountID: "sys_current", Model: "vendor/model",
	}) {
		t.Fatalf("capabilities input = %#v", service.capabilitiesInput)
	}
}

func TestRouterRegistersAccountTestOptionsAsNoStoreLimitedReadRoutes(t *testing.T) {
	authenticator := &managementAccountTestOptionsAuthenticator{
		authContext: managementauth.Context{
			SystemAccountID: "sys_admin",
			Role:            "admin",
			SessionID:       "sess_admin",
		},
	}
	ipLimiter := &managementAccountTestOptionsIPLimiter{decision: SystemAPIRateLimitDecision{Allowed: true}}
	userLimiter := &managementAccountTestOptionsUserLimiter{decision: SystemAPIRateLimitDecision{Allowed: true}}
	handlerCalls := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalls++
		if chi.URLParam(r, "id") != "acct_1" {
			t.Fatalf("account id = %q", chi.URLParam(r, "id"))
		}
		if strings.Contains(r.URL.RawPath, "/models/") || strings.Contains(r.URL.Path, "/models/") {
			if chi.URLParam(r, "modelId") != "vendor%2Fmodel" {
				t.Fatalf("model id = %q", chi.URLParam(r, "modelId"))
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})
	opts := RouterOptions{
		Config: config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		SystemAPIRateLimitReader: managementAccountTestOptionsRateLimitReader{settings: port.SystemAPIRateLimitSettings{
			IPReadPerMinute:         600,
			IPReadBurstPer10Seconds: 120,
			UserReadPerMinute:       300,
		}},
		SystemAPIIPRateLimiter:                ipLimiter,
		SystemAPIAuthenticatedRateLimiter:     userLimiter,
		ManagementAPIAuthMiddleware:           NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware:      NewManagementAPIAuthTouchMiddleware(authenticator),
		ManagementAccountTestOptionsHandler:   handler,
		ManagementMyAccountTestOptionsHandler: handler,
	}
	if !managementBusinessRoutesConfigured(opts) {
		t.Fatal("account test-options routes were not classified as management business routes")
	}
	if managementWriteRoutesConfigured(opts) {
		t.Fatal("account test-options routes were classified as management write routes")
	}
	router := NewRouter(opts)

	for _, path := range []string{
		"/__aisys__/api/accounts/acct_1/test-options",
		"/__aisys__/api/my-accounts/acct_1/test-options?systemAccountId=sys_forged",
		"/__aisys__/api/accounts/acct_1/test-options/models/vendor%2Fmodel",
		"/__aisys__/api/my-accounts/acct_1/test-options/models/vendor%2Fmodel?systemAccountId=sys_forged",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Cookie", "juhe_ai_session=session-token")
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusNoContent {
			t.Fatalf("%s status = %d, want 204; body = %s", path, rec.Code, rec.Body.String())
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s Cache-Control = %q", path, rec.Header().Get("Cache-Control"))
		}
	}
	if handlerCalls != 4 || authenticator.readCalls != 4 || authenticator.touchCalls != 0 {
		t.Fatalf("handler calls = %d, read auth calls = %d, touch auth calls = %d", handlerCalls, authenticator.readCalls, authenticator.touchCalls)
	}
	if ipLimiter.calls != 4 || ipLimiter.settings != (SystemAPIIPRateLimitSettings{PerMinute: 600, BurstPer10Seconds: 120}) {
		t.Fatalf("IP limiter calls = %d, settings = %+v", ipLimiter.calls, ipLimiter.settings)
	}
	if userLimiter.calls != 4 || userLimiter.limit != 300 {
		t.Fatalf("user limiter calls = %d, limit = %d", userLimiter.calls, userLimiter.limit)
	}
}

func TestRouterDoesNotRegisterAccountTestOptionsWhenManagementAPIDisabled(t *testing.T) {
	handlerCalls := 0
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { handlerCalls++ })
	router := NewRouter(RouterOptions{
		Config:                                config.Config{Host: "127.0.0.1", Port: 3000},
		ManagementAccountTestOptionsHandler:   handler,
		ManagementMyAccountTestOptionsHandler: handler,
	})

	for _, path := range []string{
		"/__aisys__/api/accounts/acct_1/test-options",
		"/__aisys__/api/my-accounts/acct_1/test-options",
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404; body = %s", path, rec.Code, rec.Body.String())
		}
	}
	if handlerCalls != 0 {
		t.Fatalf("handler calls = %d, want 0", handlerCalls)
	}
}

type managementAccountTestOptionsServiceStub struct {
	calls             int
	input             managementaccounttestoptions.Input
	optionsInput      managementaccounttestoptions.OptionsInput
	capabilitiesInput managementaccounttestoptions.ModelCapabilitiesInput
	result            managementaccounttestoptions.Result
	found             bool
	err               error
}

func (s *managementAccountTestOptionsServiceStub) Options(
	_ *http.Request,
	input managementaccounttestoptions.OptionsInput,
) ([]managementaccounttestoptions.SelectionOption, bool, error) {
	s.calls++
	s.input = managementaccounttestoptions.Input{AccountID: input.AccountID, SystemAccountID: input.SystemAccountID}
	s.optionsInput = input
	result := make([]managementaccounttestoptions.SelectionOption, 0, len(s.result.Models))
	for _, model := range s.result.Models {
		result = append(result, managementaccounttestoptions.SelectionOption{ID: model.Model, Name: model.Model})
	}
	return result, s.found, s.err
}

func (s *managementAccountTestOptionsServiceStub) ModelCapabilities(
	_ *http.Request,
	input managementaccounttestoptions.ModelCapabilitiesInput,
) (managementaccounttestoptions.ModelCapabilities, bool, error) {
	s.calls++
	s.input = managementaccounttestoptions.Input{AccountID: input.AccountID, SystemAccountID: input.SystemAccountID}
	s.capabilitiesInput = input
	for _, model := range s.result.Models {
		if model.Model == input.Model {
			return managementaccounttestoptions.ModelCapabilities{
				ID: model.Model, Name: model.Model, TestEndpointModes: model.TestEndpointModes,
			}, s.found, s.err
		}
	}
	return managementaccounttestoptions.ModelCapabilities{}, s.found, s.err
}

func managementAccountTestOptionsRequest(
	method string,
	target string,
	accountID string,
	authContext *managementauth.Context,
) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("id", accountID)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeContext)
	if authContext != nil {
		ctx = context.WithValue(ctx, managementAuthContextKey, *authContext)
	}
	return req.WithContext(ctx)
}

type managementAccountTestOptionsAuthenticator struct {
	authContext managementauth.Context
	readCalls   int
	touchCalls  int
}

func (s *managementAccountTestOptionsAuthenticator) AuthenticateCookie(
	_ context.Context,
	_ string,
) (managementauth.Context, error) {
	s.readCalls++
	return s.authContext, nil
}

func (s *managementAccountTestOptionsAuthenticator) AuthenticateCookieAndTouch(
	_ context.Context,
	_ string,
) (managementauth.Context, error) {
	s.touchCalls++
	return s.authContext, nil
}

type managementAccountTestOptionsRateLimitReader struct {
	settings port.SystemAPIRateLimitSettings
}

func (s managementAccountTestOptionsRateLimitReader) SystemAPIRateLimitSettings(context.Context) (port.SystemAPIRateLimitSettings, error) {
	return s.settings, nil
}

type managementAccountTestOptionsIPLimiter struct {
	decision SystemAPIRateLimitDecision
	settings SystemAPIIPRateLimitSettings
	calls    int
}

func (s *managementAccountTestOptionsIPLimiter) AllowSystemAPIIP(
	_ context.Context,
	_ string,
	settings SystemAPIIPRateLimitSettings,
) (SystemAPIRateLimitDecision, error) {
	s.calls++
	s.settings = settings
	return s.decision, nil
}

type managementAccountTestOptionsUserLimiter struct {
	decision SystemAPIRateLimitDecision
	limit    int
	calls    int
}

func (s *managementAccountTestOptionsUserLimiter) AllowSystemAPIAuthenticated(
	_ context.Context,
	_ string,
	limit int,
) (SystemAPIRateLimitDecision, error) {
	s.calls++
	s.limit = limit
	return s.decision, nil
}

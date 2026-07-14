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
		"/__aisys__/api/my-accounts/acct_1/test-options",
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
		"data": {
			"accountId": "acct_1",
			"defaultModel": "gpt-5.2",
			"models": [{"model": "gpt-5.2", "supportedApiProtocols": ["responses", "chat_completions"], "testEndpointModes": ["responses_sse", "chat_sse"]}],
			"testEndpointModes": ["responses_sse", "chat_sse"],
			"defaultTestEndpointMode": "responses_sse"
		}
	}`), &want); err != nil {
		t.Fatalf("decode expected response: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("response = %s", rec.Body.String())
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
	if handlerCalls != 2 || authenticator.readCalls != 2 || authenticator.touchCalls != 0 {
		t.Fatalf("handler calls = %d, read auth calls = %d, touch auth calls = %d", handlerCalls, authenticator.readCalls, authenticator.touchCalls)
	}
	if ipLimiter.calls != 2 || ipLimiter.settings != (SystemAPIIPRateLimitSettings{PerMinute: 600, BurstPer10Seconds: 120}) {
		t.Fatalf("IP limiter calls = %d, settings = %+v", ipLimiter.calls, ipLimiter.settings)
	}
	if userLimiter.calls != 2 || userLimiter.limit != 300 {
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
	calls  int
	input  managementaccounttestoptions.Input
	result managementaccounttestoptions.Result
	found  bool
	err    error
}

func (s *managementAccountTestOptionsServiceStub) Get(
	_ *http.Request,
	input managementaccounttestoptions.Input,
) (managementaccounttestoptions.Result, bool, error) {
	s.calls++
	s.input = input
	return s.result, s.found, s.err
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

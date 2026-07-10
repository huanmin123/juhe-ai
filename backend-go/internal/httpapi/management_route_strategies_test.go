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
	"juhe-ai/backend-go/internal/modules/managementroutestrategies"
)

func TestManagementRouteStrategyOptionsHandlerRequiresAdminAndParsesQuery(t *testing.T) {
	service := &managementRouteStrategyOptionServiceStub{
		options: []managementroutestrategies.Option{
			{ID: "route_default", SystemAccountID: "sys_user", SystemAccountName: "用户", Name: "默认路由", Mode: "normal", Status: "active", IsDefault: true},
		},
	}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
	})(newManagementRouteStrategyOptionsHandler(service, managementRouteStrategyScopeAdmin))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/route-strategies/options?systemAccountId=sys_user&ids=route_a,route_b&ids=route_a&keyword=%20%E9%BB%98%E8%AE%A4%20&limit=500&activeOnly=false", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if service.input.SystemAccountID != "sys_user" ||
		!service.input.IncludeSystemAccountFields ||
		service.input.Keyword != "默认" ||
		service.input.Limit != 500 ||
		service.input.ActiveOnly {
		t.Fatalf("service input = %+v", service.input)
	}
	if len(service.input.IDs) != 2 || service.input.IDs[0] != "route_a" || service.input.IDs[1] != "route_b" {
		t.Fatalf("ids = %#v", service.input.IDs)
	}
	var body struct {
		Data []managementroutestrategies.Option `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data) != 1 || body.Data[0].SystemAccountName != "用户" {
		t.Fatalf("body = %+v", body)
	}
}

func TestManagementRouteStrategyOptionsHandlerRejectsOrdinaryUser(t *testing.T) {
	service := &managementRouteStrategyOptionServiceStub{}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_user", Username: "user", Role: "user", SessionID: "sess_user"},
	})(newManagementRouteStrategyOptionsHandler(service, managementRouteStrategyScopeAdmin))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/route-strategies/options?systemAccountId=sys_admin", nil)
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

func TestManagementMyRouteStrategyOptionsHandlerForcesSelfScope(t *testing.T) {
	service := &managementRouteStrategyOptionServiceStub{}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_user", Username: "user", Role: "user", SessionID: "sess_user"},
	})(newManagementRouteStrategyOptionsHandler(service, managementRouteStrategyScopeSelf))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-route-strategies/options?systemAccountId=sys_admin&activeOnly=bad", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if service.input.SystemAccountID != "sys_user" || service.input.IncludeSystemAccountFields || !service.input.ActiveOnly {
		t.Fatalf("service input = %+v", service.input)
	}
}

func TestManagementRouteStrategyOptionsHandlerRedactsStoreErrors(t *testing.T) {
	handler := newManagementRouteStrategyOptionsHandler(&managementRouteStrategyOptionServiceStub{err: errors.New("postgres password leaked")}, managementRouteStrategyScopeSelf)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-route-strategies/options", nil)
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

func TestRouterRegistersW2ManagementRouteStrategyOptions(t *testing.T) {
	service := &managementRouteStrategyOptionServiceStub{
		options: []managementroutestrategies.Option{{ID: "route_default", Name: "默认路由", Mode: "normal", Status: "active", IsDefault: true}},
	}
	router := NewRouter(RouterOptions{
		Config:                                  config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                                  slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementRouteStrategyOptionsHandler:   newManagementRouteStrategyOptionsHandler(service, managementRouteStrategyScopeAdmin),
		ManagementMyRouteStrategyOptionsHandler: newManagementRouteStrategyOptionsHandler(service, managementRouteStrategyScopeSelf),
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
			context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
		}),
	})

	for _, path := range []string{"/__aisys__/api/route-strategies/options", "/__aisys__/api/my-route-strategies/options"} {
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

func TestRouterDoesNotRegisterW2ManagementRouteStrategyOptionsWhenDisabled(t *testing.T) {
	router := NewRouter(RouterOptions{
		Config:                                config.Config{Host: "127.0.0.1", Port: 3000},
		Logger:                                slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementRouteStrategyOptionsHandler: newManagementRouteStrategyOptionsHandler(&managementRouteStrategyOptionServiceStub{}, managementRouteStrategyScopeAdmin),
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
			context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
		}),
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/route-strategies/options", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 while JUHE_AI_MANAGEMENT_API_ENABLED=false", rec.Code)
	}
}

type managementRouteStrategyOptionServiceStub struct {
	called  bool
	input   managementroutestrategies.OptionListInput
	options []managementroutestrategies.Option
	err     error
}

func (s *managementRouteStrategyOptionServiceStub) Options(_ *http.Request, input managementroutestrategies.OptionListInput) ([]managementroutestrategies.Option, error) {
	s.called = true
	s.input = input
	return s.options, s.err
}

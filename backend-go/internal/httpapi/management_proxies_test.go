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
	"juhe-ai/backend-go/internal/modules/managementproxies"
)

func TestManagementProxyOptionsHandler(t *testing.T) {
	service := &managementProxyOptionServiceStub{
		options: []managementproxies.Option{
			{ID: "proxy_a", Name: "代理 A", Type: "http", Enabled: true},
		},
	}
	handler := newManagementProxyOptionsHandler(service)

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/proxies/options?keyword=%20%E4%BB%A3%E7%90%86%20&limit=500", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if service.input.Keyword != "代理" || service.input.Limit != 500 {
		t.Fatalf("service input = %+v", service.input)
	}
	var body struct {
		Data []managementproxies.Option `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data) != 1 || body.Data[0].ID != "proxy_a" || body.Data[0].Name != "代理 A" {
		t.Fatalf("body = %+v", body)
	}
}

func TestManagementProxyOptionsHandlerRedactsStoreErrors(t *testing.T) {
	handler := newManagementProxyOptionsHandler(&managementProxyOptionServiceStub{err: errors.New("postgres password leaked")})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/proxies/options", nil)
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

func TestRouterDoesNotRegisterW2ManagementProxyOptionsBeforeTakeover(t *testing.T) {
	router := NewRouter(RouterOptions{
		Config: config.Config{Host: "127.0.0.1", Port: 3000},
		Logger: slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/proxies/options", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := body["error"]; got != "接口不存在" {
		t.Fatalf("error = %v, want current generic not-found before W2 takeover", got)
	}
}

func TestRouterRegistersW2ManagementProxyOptionsOnlyWithAuthMiddleware(t *testing.T) {
	service := &managementProxyOptionServiceStub{
		options: []managementproxies.Option{
			{ID: "proxy_a", Name: "代理 A", Type: "http", Enabled: true},
		},
	}
	router := NewRouter(RouterOptions{
		Config:                        config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                        slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementProxyOptionsHandler: newManagementProxyOptionsHandler(service),
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
			context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
		}),
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/proxies/options?limit=50", nil)
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

func TestRouterDoesNotRegisterW2ManagementProxyOptionsWhenDisabled(t *testing.T) {
	router := NewRouter(RouterOptions{
		Config:                        config.Config{Host: "127.0.0.1", Port: 3000},
		Logger:                        slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementProxyOptionsHandler: newManagementProxyOptionsHandler(&managementProxyOptionServiceStub{}),
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
			context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
		}),
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/proxies/options", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 while JUHE_AI_MANAGEMENT_API_ENABLED=false", rec.Code)
	}
}

func TestRouterW2ManagementProxyOptionsRequiresValidSession(t *testing.T) {
	authenticator := &managementAPIAuthenticatorStub{
		err: &managementauth.AuthError{StatusCode: http.StatusUnauthorized, Message: "请先登录"},
	}
	router := NewRouter(RouterOptions{
		Config:                        config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                        slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementProxyOptionsHandler: newManagementProxyOptionsHandler(&managementProxyOptionServiceStub{}),
		ManagementAPIAuthMiddleware:   NewManagementAPIAuthMiddleware(authenticator),
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/proxies/options", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["message"] != "请先登录" {
		t.Fatalf("body = %+v", body)
	}
	if authenticator.cookieHeader != "" {
		t.Fatalf("cookie header = %q, want empty", authenticator.cookieHeader)
	}
}

func TestRouterRequiresW2ManagementAuthMiddleware(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("NewRouter() did not panic without management auth middleware")
		}
	}()

	_ = NewRouter(RouterOptions{
		Config:                        config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                        slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementProxyOptionsHandler: newManagementProxyOptionsHandler(&managementProxyOptionServiceStub{}),
	})
}

func TestRouterRequiresW2ManagementProxyOptionsHandlerWhenEnabled(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("NewRouter() did not panic without management proxy options handler")
		}
	}()

	_ = NewRouter(RouterOptions{
		Config:                      config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                      slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{}),
	})
}

type managementProxyOptionServiceStub struct {
	input   managementproxies.OptionListInput
	options []managementproxies.Option
	err     error
}

func (s *managementProxyOptionServiceStub) Options(_ *http.Request, input managementproxies.OptionListInput) ([]managementproxies.Option, error) {
	s.input = input
	return s.options, s.err
}

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestManagementProxiesHandlerRequiresAdminAndParsesQuery(t *testing.T) {
	description := "说明"
	service := &managementProxyOptionServiceStub{
		listResult: managementproxies.ListResult{
			Items: []managementproxies.Summary{
				{
					ID:          "proxy_a",
					Name:        "代理 A",
					Description: &description,
					Type:        "http",
					Host:        "proxy.example.com",
					Port:        8080,
					Enabled:     false,
					TestStatus:  "warning",
				},
			},
			Page:     2,
			PageSize: 25,
			Total:    51,
			HasMore:  true,
		},
	}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
	})(newManagementProxiesHandler(service))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/proxies?page=2&pageSize=25&keyword=%20%E4%BB%A3%E7%90%86%20", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if service.listInput.Keyword != "代理" || service.listInput.Page != 2 || service.listInput.PageSize != 25 {
		t.Fatalf("service list input = %+v", service.listInput)
	}
	bodyText := rec.Body.String()
	for _, forbidden := range []string{
		"password",
		"password_encrypted",
		"proxyUrl",
		"system_account_id",
		"systemAccountId",
		"created_at",
		"createdAt",
		"updated_at",
		"updatedAt",
	} {
		if strings.Contains(bodyText, forbidden) {
			t.Fatalf("proxy list leaked sensitive field %q in body: %s", forbidden, bodyText)
		}
	}
	var body struct {
		Data managementproxies.ListResult `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data.Items) != 1 ||
		body.Data.Items[0].ID != "proxy_a" ||
		body.Data.Items[0].Description == nil ||
		*body.Data.Items[0].Description != "说明" ||
		body.Data.Total != 51 ||
		!body.Data.HasMore {
		t.Fatalf("body = %+v", body)
	}
}

func TestManagementProxiesHandlerTreatsInvalidQueryAsMissingAndUsesFirstValue(t *testing.T) {
	service := &managementProxyOptionServiceStub{}
	handler := newManagementProxiesHandler(service)

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/proxies?page=bad&page=2&pageSize=bad&pageSize=25&keyword=%20first%20&keyword=second", nil)
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if service.listInput.Keyword != "first" || service.listInput.Page != 0 || service.listInput.PageSize != 0 {
		t.Fatalf("service list input = %+v, want first keyword and missing invalid integers", service.listInput)
	}
}

func TestManagementProxiesHandlerRejectsOrdinaryUser(t *testing.T) {
	service := &managementProxyOptionServiceStub{}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_user", Username: "user", Role: "user", SessionID: "sess_user"},
	})(newManagementProxiesHandler(service))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/proxies", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if service.listCalled {
		t.Fatal("service should not be called for ordinary user")
	}
}

func TestManagementProxiesHandlerRedactsStoreErrors(t *testing.T) {
	handler := newManagementProxiesHandler(&managementProxyOptionServiceStub{listErr: errors.New("postgres password leaked")})
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/proxies", nil)
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"}))
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

	req = httptest.NewRequest(http.MethodGet, "/__aisys__/api/proxies", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("proxies status = %d, want 404", rec.Code)
	}
}

func TestRouterRegistersW2ManagementProxyHandlersOnlyWithAuthMiddleware(t *testing.T) {
	service := &managementProxyOptionServiceStub{
		listResult: managementproxies.ListResult{
			Items: []managementproxies.Summary{
				{ID: "proxy_a", Name: "代理 A", Type: "http", Host: "proxy.example.com", Port: 8080, Enabled: true, TestStatus: "unknown"},
			},
		},
		options: []managementproxies.Option{
			{ID: "proxy_a", Name: "代理 A", Type: "http", Enabled: true},
		},
	}
	router := NewRouter(RouterOptions{
		Config:                        config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                        slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementProxiesHandler:      newManagementProxiesHandler(service),
		ManagementProxyOptionsHandler: newManagementProxyOptionsHandler(service),
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
			context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin"},
		}),
	})

	for _, path := range []string{"/__aisys__/api/proxies?page=1&pageSize=20", "/__aisys__/api/proxies/options?limit=50"} {
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

func TestRouterDoesNotRegisterW2ManagementProxyOptionsWhenDisabled(t *testing.T) {
	router := NewRouter(RouterOptions{
		Config:                        config.Config{Host: "127.0.0.1", Port: 3000},
		Logger:                        slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementProxiesHandler:      newManagementProxiesHandler(&managementProxyOptionServiceStub{}),
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

	req = httptest.NewRequest(http.MethodGet, "/__aisys__/api/proxies", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("proxies status = %d, want 404 while JUHE_AI_MANAGEMENT_API_ENABLED=false", rec.Code)
	}
}

func TestRouterW2ManagementProxyOptionsRequiresValidSession(t *testing.T) {
	authenticator := &managementAPIAuthenticatorStub{
		err: &managementauth.AuthError{StatusCode: http.StatusUnauthorized, Message: "请先登录"},
	}
	router := NewRouter(RouterOptions{
		Config:                        config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                        slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementProxiesHandler:      newManagementProxiesHandler(&managementProxyOptionServiceStub{}),
		ManagementProxyOptionsHandler: newManagementProxyOptionsHandler(&managementProxyOptionServiceStub{}),
		ManagementAPIAuthMiddleware:   NewManagementAPIAuthMiddleware(authenticator),
	})

	for _, path := range []string{"/__aisys__/api/proxies", "/__aisys__/api/proxies/options"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s status = %d, want 401", path, rec.Code)
		}
		var body map[string]string
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if body["message"] != "请先登录" {
			t.Fatalf("body = %+v", body)
		}
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
		ManagementProxiesHandler:      newManagementProxiesHandler(&managementProxyOptionServiceStub{}),
		ManagementProxyOptionsHandler: newManagementProxyOptionsHandler(&managementProxyOptionServiceStub{}),
	})
}

func TestRouterRequiresAtLeastOneW2ManagementHandlerWhenEnabled(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("NewRouter() did not panic without management handlers")
		}
	}()

	_ = NewRouter(RouterOptions{
		Config:                      config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                      slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{}),
	})
}

type managementProxyOptionServiceStub struct {
	listCalled bool
	listInput  managementproxies.ListInput
	input      managementproxies.OptionListInput
	listResult managementproxies.ListResult
	options    []managementproxies.Option
	listErr    error
	err        error
}

func (s *managementProxyOptionServiceStub) List(_ *http.Request, input managementproxies.ListInput) (managementproxies.ListResult, error) {
	s.listCalled = true
	s.listInput = input
	return s.listResult, s.listErr
}

func (s *managementProxyOptionServiceStub) Options(_ *http.Request, input managementproxies.OptionListInput) ([]managementproxies.Option, error) {
	s.input = input
	return s.options, s.err
}

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
	"time"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/config"
	operationlogjob "juhe-ai/backend-go/internal/jobs/operationlog"
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

func TestManagementProxyCreateHandlerCreatesAndWritesSafeOperationLog(t *testing.T) {
	createdAt := time.Date(2026, 7, 10, 9, 30, 0, 0, time.UTC)
	queueStub := &operationLogQueueStub{}
	service := &managementProxyOptionServiceStub{
		createResult: managementproxies.CreateResult{
			Proxy: managementproxies.Summary{
				ID:         "proxy_a",
				Name:       "代理 A",
				Type:       "socks5h",
				Host:       "proxy.example.com",
				Port:       1080,
				Username:   stringPtr("proxy-user"),
				Enabled:    true,
				TestStatus: "unknown",
			},
			PasswordSet: true,
		},
	}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{
			SystemAccountID: "sys_admin",
			Username:        "admin",
			DisplayName:     "管理员",
			Role:            "admin",
			SessionID:       "sess_admin",
		},
	})(newManagementProxyCreateHandler(
		service,
		newManagementOperationLogOptions(ManagementOperationLogOptions{
			Config:   config.Config{TrustProxy: "false"},
			Client:   queueStub,
			Now:      func() time.Time { return createdAt },
			NewLogID: func() string { return "oplog_proxy_create" },
		}),
	))

	req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/proxies", strings.NewReader(`{
		"name":"代理 A",
		"type":"socks5h",
		"host":"proxy.example.com",
		"port":1080,
		"username":"proxy-user",
		"password":"proxy-secret",
		"enabled":true
	}`))
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "req_proxy_create"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", rec.Code, rec.Body.String())
	}
	if !service.createCalled ||
		service.createInput.SystemAccountID != "sys_admin" ||
		service.createInput.Name != "代理 A" ||
		service.createInput.Password == nil ||
		*service.createInput.Password != "proxy-secret" {
		t.Fatalf("create input = %+v called=%v", service.createInput, service.createCalled)
	}
	bodyText := rec.Body.String()
	for _, forbidden := range []string{"password", "proxy-secret", "password_encrypted"} {
		if strings.Contains(bodyText, forbidden) {
			t.Fatalf("create response leaked %q: %s", forbidden, bodyText)
		}
	}
	if strings.Contains(string(queueStub.payload), "proxy-secret") {
		t.Fatalf("operation log payload leaked proxy password: %s", string(queueStub.payload))
	}
	logInput, err := operationlogjob.DecodeWriteTaskPayload(queueStub.payload)
	if err != nil {
		t.Fatalf("decode operation log: %v", err)
	}
	if logInput.OperationKey != "proxies.create" ||
		logInput.ResourceType != "proxy" ||
		logInput.ResourceID != "proxy_a" ||
		logInput.VisibilityScope != "admin_only" ||
		logInput.StatusCode == nil ||
		*logInput.StatusCode != http.StatusCreated {
		t.Fatalf("operation log = %+v", logInput)
	}
	foundPasswordChange := false
	for _, change := range logInput.Changes {
		if change.Field == "password" {
			foundPasswordChange = change.Sensitive && change.After == "已设置"
		}
	}
	if !foundPasswordChange {
		t.Fatalf("operation log changes = %+v, want sensitive password marker", logInput.Changes)
	}
}

func TestManagementProxyUpdateHandlerRejectsInvalidBodyBeforeService(t *testing.T) {
	service := &managementProxyOptionServiceStub{}
	handler := newManagementProxyUpdateHandler(service, managementOperationLogOptions{})
	req := httptest.NewRequest(http.MethodPatch, "/__aisys__/api/proxies/proxy_a", strings.NewReader(`{"username":null}`))
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"}))
	req = managementProxyRequestWithID(req, "proxy_a")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
	if service.updateCalled {
		t.Fatal("service should not be called for invalid proxy update body")
	}
}

func TestManagementProxyUpdateHandlerMapsNotFoundAndDuplicateName(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantText   string
	}{
		{name: "not found", err: managementproxies.ErrProxyNotFound, wantStatus: http.StatusNotFound, wantText: "代理不存在"},
		{name: "duplicate", err: &managementproxies.NameExistsError{Name: "代理 A"}, wantStatus: http.StatusConflict, wantText: "代理名称已存在：代理 A"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementProxyOptionServiceStub{updateErr: tt.err}
			handler := newManagementProxyUpdateHandler(service, managementOperationLogOptions{})
			req := httptest.NewRequest(http.MethodPatch, "/__aisys__/api/proxies/proxy_a", strings.NewReader(`{"name":"代理 A"}`))
			req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"}))
			req = managementProxyRequestWithID(req, "proxy_a")
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tt.wantText) {
				t.Fatalf("body = %s, want %q", rec.Body.String(), tt.wantText)
			}
		})
	}
}

func TestManagementProxyDeleteHandlerReturnsConflictWhenInUse(t *testing.T) {
	service := &managementProxyOptionServiceStub{
		deleteErr: &managementproxies.InUseError{
			AccountCount: 2,
			AccountNames: []string{"账户 A", "账户 B"},
		},
	}
	handler := newManagementProxyDeleteHandler(service, managementOperationLogOptions{})
	req := httptest.NewRequest(http.MethodDelete, "/__aisys__/api/proxies/proxy_a", nil)
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"}))
	req = managementProxyRequestWithID(req, "proxy_a")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "这个代理仍被 2 个账户使用") {
		t.Fatalf("body = %s", rec.Body.String())
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

func TestRouterRegistersManagementProxyWriteHandlersWithTouchMiddleware(t *testing.T) {
	readAuthenticator := &managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_read"},
	}
	touchAuthenticator := &managementAPIAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_touch"},
	}
	service := &managementProxyOptionServiceStub{
		createResult: managementproxies.CreateResult{
			Proxy: managementproxies.Summary{
				ID:         "proxy_a",
				Name:       "代理 A",
				Type:       "http",
				Host:       "proxy.example.com",
				Port:       8080,
				Enabled:    true,
				TestStatus: "unknown",
			},
		},
	}
	router := NewRouter(RouterOptions{
		Config:                           config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                           slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementProxyCreateHandler:     newManagementProxyCreateHandler(service, managementOperationLogOptions{}),
		ManagementAPIAuthMiddleware:      NewManagementAPIAuthMiddleware(readAuthenticator),
		ManagementAPIAuthTouchMiddleware: NewManagementAPIAuthTouchMiddleware(touchAuthenticator),
	})

	req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/proxies", strings.NewReader(`{
		"name":"代理 A",
		"type":"http",
		"host":"proxy.example.com",
		"port":8080
	}`))
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", rec.Code, rec.Body.String())
	}
	if readAuthenticator.cookieHeader != "" || touchAuthenticator.touchCookieHeader == "" {
		t.Fatalf("auth headers read=%q touch=%q", readAuthenticator.cookieHeader, touchAuthenticator.touchCookieHeader)
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
	listCalled   bool
	listInput    managementproxies.ListInput
	input        managementproxies.OptionListInput
	listResult   managementproxies.ListResult
	options      []managementproxies.Option
	listErr      error
	err          error
	createCalled bool
	createInput  managementproxies.CreateInput
	createResult managementproxies.CreateResult
	createErr    error
	updateCalled bool
	updateInput  managementproxies.UpdateInput
	updateResult managementproxies.UpdateResult
	updateErr    error
	deleteCalled bool
	deleteInput  managementproxies.DeleteInput
	deleteResult managementproxies.DeleteResult
	deleteErr    error
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

func (s *managementProxyOptionServiceStub) Create(_ *http.Request, input managementproxies.CreateInput) (managementproxies.CreateResult, error) {
	s.createCalled = true
	s.createInput = input
	return s.createResult, s.createErr
}

func (s *managementProxyOptionServiceStub) Update(_ *http.Request, input managementproxies.UpdateInput) (managementproxies.UpdateResult, error) {
	s.updateCalled = true
	s.updateInput = input
	return s.updateResult, s.updateErr
}

func (s *managementProxyOptionServiceStub) Delete(_ *http.Request, input managementproxies.DeleteInput) (managementproxies.DeleteResult, error) {
	s.deleteCalled = true
	s.deleteInput = input
	return s.deleteResult, s.deleteErr
}

func managementProxyRequestWithID(req *http.Request, proxyID string) *http.Request {
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("id", proxyID)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext))
}

func stringPtr(value string) *string {
	return &value
}

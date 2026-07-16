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

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementroutestrategies"
	"juhe-ai/backend-go/internal/store/port"
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

func TestManagementRouteStrategyListHandlerParsesFirstQueryValues(t *testing.T) {
	service := &managementRouteStrategyListServiceStub{
		result: managementroutestrategies.ListResult{Items: []managementroutestrategies.ListItem{}},
	}
	handler := newManagementRouteStrategyListHandler(service, managementRouteStrategyScopeAdmin)
	req := httptest.NewRequest(
		http.MethodGet,
		"/__aisys__/api/route-strategies?page=2&page=9&pageSize=25&pageSize=100"+
			"&keyword=%20first%20&keyword=second&mode=invalid&mode=normal"+
			"&status=bogus&status=active&systemAccountId=%20sys_target%20&systemAccountId=sys_other",
		nil,
	)
	req = requestWithManagementAuthContext(req, managementauth.Context{
		SystemAccountID: "sys_admin",
		Role:            "admin",
	})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if service.calls != 1 ||
		service.input.ActorSystemAccountID != "sys_admin" ||
		service.input.ActorRole != "admin" ||
		service.input.SystemAccountID != "sys_target" ||
		service.input.SelfOnly ||
		service.input.Page != 2 ||
		service.input.PageSize != 25 ||
		!service.input.PageSizeProvided ||
		service.input.Keyword != "first" ||
		service.input.Mode != "invalid" ||
		service.input.Status != "bogus" {
		t.Fatalf("service input = %+v calls=%d", service.input, service.calls)
	}
}

func TestManagementRouteStrategyListHandlerBuildsAdminAndSelfScopes(t *testing.T) {
	t.Run("admin all first is global", func(t *testing.T) {
		service := &managementRouteStrategyListServiceStub{
			result: managementroutestrategies.ListResult{Items: []managementroutestrategies.ListItem{}},
		}
		handler := newManagementRouteStrategyListHandler(service, managementRouteStrategyScopeAdmin)
		req := httptest.NewRequest(
			http.MethodGet,
			"/__aisys__/api/route-strategies?systemAccountId=all&systemAccountId=sys_spoof",
			nil,
		)
		req = requestWithManagementAuthContext(req, managementauth.Context{
			SystemAccountID: "sys_admin",
			Role:            "super_admin",
		})
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK || service.calls != 1 ||
			service.input.SystemAccountID != "" || service.input.SelfOnly {
			t.Fatalf("status=%d input=%+v calls=%d body=%s", rec.Code, service.input, service.calls, rec.Body.String())
		}
	})

	t.Run("admin non ECMAScript whitespace owner remains narrowed", func(t *testing.T) {
		service := &managementRouteStrategyListServiceStub{
			result: managementroutestrategies.ListResult{Items: []managementroutestrategies.ListItem{}},
		}
		handler := newManagementRouteStrategyListHandler(service, managementRouteStrategyScopeAdmin)
		req := httptest.NewRequest(
			http.MethodGet,
			"/__aisys__/api/route-strategies?systemAccountId=%C2%85",
			nil,
		)
		req = requestWithManagementAuthContext(req, managementauth.Context{
			SystemAccountID: "sys_admin",
			Role:            "admin",
		})
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK ||
			service.calls != 1 ||
			service.input.SystemAccountID != "\u0085" ||
			service.input.SelfOnly {
			t.Fatalf("status=%d input=%+v calls=%d body=%s", rec.Code, service.input, service.calls, rec.Body.String())
		}
	})

	t.Run("self forces current account and ignores spoof owner", func(t *testing.T) {
		service := &managementRouteStrategyListServiceStub{
			result: managementroutestrategies.ListResult{Items: []managementroutestrategies.ListItem{}},
		}
		handler := newManagementRouteStrategyListHandler(service, managementRouteStrategyScopeSelf)
		req := httptest.NewRequest(
			http.MethodGet,
			"/__aisys__/api/my-route-strategies?systemAccountId=sys_spoof&systemAccountId=&page=1e2&pageSize=0",
			nil,
		)
		req = requestWithManagementAuthContext(req, managementauth.Context{
			SystemAccountID: "sys_current",
			Role:            "user",
		})
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK ||
			service.calls != 1 ||
			service.input.ActorSystemAccountID != "sys_current" ||
			service.input.SystemAccountID != "sys_current" ||
			!service.input.SelfOnly ||
			service.input.Page != 100 ||
			service.input.PageSize != 0 ||
			!service.input.PageSizeProvided {
			t.Fatalf("status=%d input=%+v calls=%d body=%s", rec.Code, service.input, service.calls, rec.Body.String())
		}
	})
}

func TestManagementRouteStrategyReadHandlersRejectOrdinaryUserOnAdminRoutes(t *testing.T) {
	listService := &managementRouteStrategyListServiceStub{}
	listHandler := newManagementRouteStrategyListHandler(listService, managementRouteStrategyScopeAdmin)
	listReq := httptest.NewRequest(http.MethodGet, "/__aisys__/api/route-strategies", nil)
	listReq = requestWithManagementAuthContext(listReq, managementauth.Context{
		SystemAccountID: "sys_user",
		Role:            "user",
	})
	listRec := httptest.NewRecorder()
	listHandler.ServeHTTP(listRec, listReq)

	detailService := &managementRouteStrategyDetailServiceStub{}
	detailHandler := newManagementRouteStrategyDetailHandler(detailService, managementRouteStrategyScopeAdmin)
	detailReq := httptest.NewRequest(http.MethodGet, "/__aisys__/api/route-strategies/route_1", nil)
	detailReq = requestWithManagementRouteStrategyID(detailReq, "route_1")
	detailReq = requestWithManagementAuthContext(detailReq, managementauth.Context{
		SystemAccountID: "sys_user",
		Role:            "user",
	})
	detailRec := httptest.NewRecorder()
	detailHandler.ServeHTTP(detailRec, detailReq)

	if listRec.Code != http.StatusForbidden || listService.calls != 0 {
		t.Fatalf("list status=%d calls=%d body=%s", listRec.Code, listService.calls, listRec.Body.String())
	}
	if detailRec.Code != http.StatusForbidden || detailService.calls != 0 {
		t.Fatalf("detail status=%d calls=%d body=%s", detailRec.Code, detailService.calls, detailRec.Body.String())
	}
}

func TestManagementRouteStrategyDetailHandlerValidatesAdminSystemAccountID(t *testing.T) {
	tests := []struct {
		name                string
		query               string
		wantStatus          int
		wantCalls           int
		wantSystemAccountID string
	}{
		{name: "missing is global", wantStatus: http.StatusOK, wantCalls: 1},
		{name: "all is global", query: "?systemAccountId=%20all%20", wantStatus: http.StatusOK, wantCalls: 1},
		{name: "target", query: "?systemAccountId=%20sys_target%20", wantStatus: http.StatusOK, wantCalls: 1, wantSystemAccountID: "sys_target"},
		{name: "empty", query: "?systemAccountId=", wantStatus: http.StatusBadRequest},
		{name: "blank", query: "?systemAccountId=%EF%BB%BF%E2%80%83", wantStatus: http.StatusBadRequest},
		{name: "duplicate", query: "?systemAccountId=sys_a&systemAccountId=sys_b", wantStatus: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementRouteStrategyDetailServiceStub{
				result: managementroutestrategies.DetailResult{
					ID:            "route_1",
					GroupBindings: []managementroutestrategies.GroupBindingSummary{},
				},
			}
			handler := newManagementRouteStrategyDetailHandler(service, managementRouteStrategyScopeAdmin)
			req := httptest.NewRequest(
				http.MethodGet,
				"/__aisys__/api/route-strategies/route_1"+tt.query,
				nil,
			)
			req = requestWithManagementRouteStrategyID(req, " route_1 ")
			req = requestWithManagementAuthContext(req, managementauth.Context{
				SystemAccountID: "sys_admin",
				Role:            "admin",
			})
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus || service.calls != tt.wantCalls {
				t.Fatalf("status=%d calls=%d input=%+v body=%s", rec.Code, service.calls, service.input, rec.Body.String())
			}
			if service.calls == 1 &&
				(service.input.ActorSystemAccountID != "sys_admin" ||
					service.input.SystemAccountID != tt.wantSystemAccountID ||
					service.input.SelfOnly ||
					service.input.RouteStrategyID != " route_1 ") {
				t.Fatalf("service input = %+v", service.input)
			}
		})
	}
}

func TestManagementMyRouteStrategyDetailHandlerIgnoresOwnerQuery(t *testing.T) {
	service := &managementRouteStrategyDetailServiceStub{
		result: managementroutestrategies.DetailResult{
			ID:            "route_1",
			GroupBindings: []managementroutestrategies.GroupBindingSummary{},
		},
	}
	handler := newManagementRouteStrategyDetailHandler(service, managementRouteStrategyScopeSelf)
	req := httptest.NewRequest(
		http.MethodGet,
		"/__aisys__/api/my-route-strategies/route_1?systemAccountId=&systemAccountId=sys_spoof",
		nil,
	)
	req = requestWithManagementRouteStrategyID(req, "route_1")
	req = requestWithManagementAuthContext(req, managementauth.Context{
		SystemAccountID: "sys_current",
		Role:            "user",
	})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK ||
		service.calls != 1 ||
		service.input.ActorSystemAccountID != "sys_current" ||
		service.input.SystemAccountID != "" ||
		!service.input.SelfOnly {
		t.Fatalf("status=%d input=%+v calls=%d body=%s", rec.Code, service.input, service.calls, rec.Body.String())
	}
}

func TestManagementRouteStrategyReadHandlersMapErrors(t *testing.T) {
	t.Run("list redacts internal error", func(t *testing.T) {
		handler := newManagementRouteStrategyListHandler(
			&managementRouteStrategyListServiceStub{err: errors.New("postgres password leaked")},
			managementRouteStrategyScopeSelf,
		)
		req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-route-strategies", nil)
		req = requestWithManagementAuthContext(req, managementauth.Context{
			SystemAccountID: "sys_user",
			Role:            "user",
		})
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusInternalServerError ||
			!strings.Contains(rec.Body.String(), "服务器内部错误") ||
			strings.Contains(rec.Body.String(), "postgres") ||
			strings.Contains(rec.Body.String(), "password") {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})

	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantText   string
	}{
		{name: "detail not found", err: managementroutestrategies.ErrRouteStrategyNotFound, wantStatus: http.StatusNotFound, wantText: "策略路由不存在"},
		{name: "detail internal", err: errors.New("postgres password leaked"), wantStatus: http.StatusInternalServerError, wantText: "服务器内部错误"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := newManagementRouteStrategyDetailHandler(
				&managementRouteStrategyDetailServiceStub{err: tt.err},
				managementRouteStrategyScopeSelf,
			)
			req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-route-strategies/route_1", nil)
			req = requestWithManagementRouteStrategyID(req, "route_1")
			req = requestWithManagementAuthContext(req, managementauth.Context{
				SystemAccountID: "sys_user",
				Role:            "user",
			})
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus || !strings.Contains(rec.Body.String(), tt.wantText) {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "postgres") || strings.Contains(rec.Body.String(), "password") {
				t.Fatalf("response leaked internal error: %s", rec.Body.String())
			}
		})
	}
}

func TestRouterRegistersManagementRouteStrategyReadsWithoutRegressingOptions(t *testing.T) {
	readAuthenticator := &managementAPIAuthenticatorStub{
		context: managementauth.Context{
			SystemAccountID: "sys_admin",
			Role:            "admin",
			SessionID:       "sess_read",
		},
	}
	touchAuthenticator := &managementAPIAuthenticatorStub{
		context: managementauth.Context{
			SystemAccountID: "sys_admin",
			Role:            "admin",
			SessionID:       "sess_touch",
		},
	}
	ipLimiter := &publicSettingsRateLimiterStub{
		decision: SystemAPIRateLimitDecision{Allowed: true},
	}
	userLimiter := &systemAPIAuthenticatedRateLimiterStub{
		decision: SystemAPIRateLimitDecision{Allowed: true},
	}
	listCalls := 0
	detailCalls := 0
	optionsCalls := 0
	listHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		listCalls++
		writeData(w, http.StatusOK, managementroutestrategies.ListResult{Items: []managementroutestrategies.ListItem{}})
	})
	detailHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		detailCalls++
		writeData(w, http.StatusOK, map[string]string{"id": chi.URLParam(r, "id")})
	})
	optionsHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		optionsCalls++
		writeData(w, http.StatusOK, []managementroutestrategies.Option{})
	})
	router := NewRouter(RouterOptions{
		Config:                                  config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                                  slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		SystemAPIRateLimitReader:                systemAPIRateLimitReaderStub{settings: port.SystemAPIRateLimitSettings{IPReadPerMinute: 600, IPReadBurstPer10Seconds: 120, UserReadPerMinute: 300}},
		SystemAPIIPRateLimiter:                  ipLimiter,
		SystemAPIAuthenticatedRateLimiter:       userLimiter,
		ManagementAPIAuthMiddleware:             NewManagementAPIAuthMiddleware(readAuthenticator),
		ManagementAPIAuthTouchMiddleware:        NewManagementAPIAuthTouchMiddleware(touchAuthenticator),
		ManagementRouteStrategyListHandler:      listHandler,
		ManagementMyRouteStrategyListHandler:    listHandler,
		ManagementRouteStrategyDetailHandler:    detailHandler,
		ManagementMyRouteStrategyDetailHandler:  detailHandler,
		ManagementRouteStrategyOptionsHandler:   optionsHandler,
		ManagementMyRouteStrategyOptionsHandler: optionsHandler,
	})

	paths := []string{
		"/__aisys__/api/route-strategies",
		"/__aisys__/api/my-route-strategies",
		"/__aisys__/api/route-strategies/route_1",
		"/__aisys__/api/my-route-strategies/route_1",
		"/__aisys__/api/route-strategies/options",
		"/__aisys__/api/my-route-strategies/options",
	}
	for _, path := range paths {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Cookie", "juhe_ai_session=session-token")
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s status=%d cache=%q body=%s", path, rec.Code, rec.Header().Get("Cache-Control"), rec.Body.String())
		}
	}
	if listCalls != 2 || detailCalls != 2 || optionsCalls != 2 {
		t.Fatalf("list calls=%d detail calls=%d options calls=%d", listCalls, detailCalls, optionsCalls)
	}
	if ipLimiter.calls != len(paths) || userLimiter.calls != len(paths) {
		t.Fatalf("ip calls=%d user calls=%d", ipLimiter.calls, userLimiter.calls)
	}
	if touchAuthenticator.touchCookieHeader != "" {
		t.Fatalf("touch auth cookie = %q, want empty for reads", touchAuthenticator.touchCookieHeader)
	}
}

func TestRouterDoesNotRegisterManagementRouteStrategyReadsWhenDisabled(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeData(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	router := NewRouter(RouterOptions{
		Config:                                 config.Config{Host: "127.0.0.1", Port: 3000},
		Logger:                                 slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementRouteStrategyListHandler:     handler,
		ManagementMyRouteStrategyListHandler:   handler,
		ManagementRouteStrategyDetailHandler:   handler,
		ManagementMyRouteStrategyDetailHandler: handler,
	})

	for _, path := range []string{
		"/__aisys__/api/route-strategies",
		"/__aisys__/api/my-route-strategies",
		"/__aisys__/api/route-strategies/route_1",
		"/__aisys__/api/my-route-strategies/route_1",
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404 while disabled", path, rec.Code)
		}
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

type managementRouteStrategyListServiceStub struct {
	calls  int
	input  managementroutestrategies.ListInput
	result managementroutestrategies.ListResult
	err    error
}

func (s *managementRouteStrategyListServiceStub) List(
	_ *http.Request,
	input managementroutestrategies.ListInput,
) (managementroutestrategies.ListResult, error) {
	s.calls++
	s.input = input
	return s.result, s.err
}

type managementRouteStrategyDetailServiceStub struct {
	calls  int
	input  managementroutestrategies.DetailInput
	result managementroutestrategies.DetailResult
	err    error
}

func (s *managementRouteStrategyDetailServiceStub) Detail(
	_ *http.Request,
	input managementroutestrategies.DetailInput,
) (managementroutestrategies.DetailResult, error) {
	s.calls++
	s.input = input
	return s.result, s.err
}

func requestWithManagementRouteStrategyID(req *http.Request, routeStrategyID string) *http.Request {
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("id", routeStrategyID)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext))
}

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementgroups"
	"juhe-ai/backend-go/internal/store/port"
)

func TestManagementGroupListHandlerBuildsAdminScopes(t *testing.T) {
	tests := []struct {
		name                string
		query               string
		wantSystemAccountID string
	}{
		{name: "missing is global"},
		{name: "empty is global", query: "?systemAccountId="},
		{name: "blank is global", query: "?systemAccountId=%20%20"},
		{name: "all is global", query: "?systemAccountId=%20all%20"},
		{name: "target is forwarded", query: "?systemAccountId=%20sys_target%20", wantSystemAccountID: "sys_target"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementGroupListServiceStub{
				result: managementgroups.ListResult{Items: []managementgroups.ListItem{}},
			}
			handler := newManagementGroupListHandler(service, managementGroupScopeAdmin)
			req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/groups"+tt.query, nil)
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
				service.input.SystemAccountID != tt.wantSystemAccountID ||
				service.input.SelfOnly ||
				service.input.PageSizeProvided {
				t.Fatalf("service input = %+v calls=%d", service.input, service.calls)
			}
		})
	}
}

func TestManagementMyGroupListHandlerForcesCurrentAccountAndIgnoresUnknownQuery(t *testing.T) {
	service := &managementGroupListServiceStub{
		result: managementgroups.ListResult{Items: []managementgroups.ListItem{}},
	}
	handler := newManagementGroupListHandler(service, managementGroupScopeSelf)
	req := httptest.NewRequest(
		http.MethodGet,
		"/__aisys__/api/my-groups?systemAccountId=sys_other&page=1e2&pageSize=25&keyword=ignored&providerCode=openai",
		nil,
	)
	req = requestWithManagementAuthContext(req, managementauth.Context{
		SystemAccountID: "sys_current",
		Role:            "admin",
	})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if service.calls != 1 ||
		service.input.ActorSystemAccountID != "sys_current" ||
		service.input.ActorRole != "admin" ||
		service.input.SystemAccountID != "sys_current" ||
		!service.input.SelfOnly ||
		service.input.Page != 100 ||
		service.input.PageSize != 25 ||
		!service.input.PageSizeProvided {
		t.Fatalf("service input = %+v calls=%d", service.input, service.calls)
	}
}

func TestManagementGroupListIntegerQueryValueMatchesNodeIntegerParsing(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want int
		ok   bool
	}{
		{name: "zero", raw: "0", ok: true},
		{name: "decimal", raw: "25", want: 25, ok: true},
		{name: "exponent", raw: "1e2", want: 100, ok: true},
		{name: "hex", raw: "0x10", want: 16, ok: true},
		{name: "binary", raw: "0b10", want: 2, ok: true},
		{name: "octal", raw: "0o10", want: 8, ok: true},
		{name: "negative", raw: "-2", want: -2, ok: true},
		{name: "rounded integer", raw: "5.0000000000000001", want: 5, ok: true},
		{name: "fraction", raw: "1.5"},
		{name: "infinity", raw: "Infinity"},
		{name: "overflow", raw: "1e309"},
		{name: "malformed exponent", raw: "1e"},
		{name: "numeric separator", raw: "1_000"},
		{name: "blank", raw: " "},
		{name: "ecmascript whitespace", raw: "\uFEFF\u20032\u2029", want: 2, ok: true},
		{name: "next line is not ecmascript whitespace", raw: "\u00852\u0085"},
		{name: "information separator is not ecmascript whitespace", raw: "\u001C2\u001C"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			values := url.Values{"page": []string{tt.raw}}
			got, ok := managementGroupListIntegerQueryValue(values, "page")
			if got != tt.want || ok != tt.ok {
				t.Fatalf("value = %d provided=%v, want %d provided=%v", got, ok, tt.want, tt.ok)
			}
		})
	}

	values := url.Values{"page": []string{"bad", "2"}}
	if got, ok := managementGroupListIntegerQueryValue(values, "page"); got != 0 || ok {
		t.Fatalf("duplicate first invalid value = %d provided=%v, want missing value", got, ok)
	}
}

func TestManagementGroupListInputTracksExplicitPageSize(t *testing.T) {
	tests := []struct {
		name         string
		query        string
		wantPageSize int
		wantProvided bool
	}{
		{name: "missing"},
		{name: "blank", query: "pageSize="},
		{name: "invalid", query: "pageSize=bad"},
		{name: "fraction", query: "pageSize=1.5"},
		{name: "zero", query: "pageSize=0", wantProvided: true},
		{name: "negative", query: "pageSize=-2", wantPageSize: -2, wantProvided: true},
		{name: "exponent", query: "pageSize=1e2", wantPageSize: 100, wantProvided: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			values, err := url.ParseQuery(tt.query)
			if err != nil {
				t.Fatalf("parse query: %v", err)
			}
			input := managementGroupListInput(
				managementauth.Context{SystemAccountID: "sys_user", Role: "user"},
				values,
				managementGroupScopeSelf,
			)
			if input.PageSize != tt.wantPageSize || input.PageSizeProvided != tt.wantProvided {
				t.Fatalf("input = %+v, want pageSize=%d provided=%v", input, tt.wantPageSize, tt.wantProvided)
			}
		})
	}
}

func TestManagementGroupListHandlerRejectsOrdinaryUserOnAdminRoute(t *testing.T) {
	service := &managementGroupListServiceStub{}
	handler := newManagementGroupListHandler(service, managementGroupScopeAdmin)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/groups", nil)
	req = requestWithManagementAuthContext(req, managementauth.Context{
		SystemAccountID: "sys_user",
		Role:            "user",
	})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", rec.Code, rec.Body.String())
	}
	if service.calls != 0 {
		t.Fatalf("service calls = %d, want 0", service.calls)
	}
}

func TestManagementGroupListHandlerRequiresAuthContextAndService(t *testing.T) {
	t.Run("missing auth context", func(t *testing.T) {
		handler := newManagementGroupListHandler(&managementGroupListServiceStub{}, managementGroupScopeSelf)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-groups", nil))

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500; body = %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("nil service", func(t *testing.T) {
		handler := NewManagementMyGroupListHandler(nil)
		req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-groups", nil)
		req = requestWithManagementAuthContext(req, managementauth.Context{
			SystemAccountID: "sys_user",
			Role:            "user",
		})
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500; body = %s", rec.Code, rec.Body.String())
		}
	})
}

func TestManagementGroupListHandlerRedactsServiceErrors(t *testing.T) {
	handler := newManagementGroupListHandler(&managementGroupListServiceStub{
		err: errors.New("postgres password leaked"),
	}, managementGroupScopeSelf)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-groups", nil)
	req = requestWithManagementAuthContext(req, managementauth.Context{
		SystemAccountID: "sys_user",
		Role:            "user",
	})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body = %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "postgres") || strings.Contains(rec.Body.String(), "password") {
		t.Fatalf("response leaked service error: %s", rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["message"] != "服务器内部错误" {
		t.Fatalf("body = %+v", body)
	}
}

func TestManagementGroupListHandlerReturnsProgressiveJSONWithoutDetailFields(t *testing.T) {
	service := &managementGroupListServiceStub{
		result: managementgroups.ListResult{
			Items: []managementgroups.ListItem{{
				ID:                   "grp_authorized",
				OwnerSystemAccountID: "sys_owner",
				Name:                 "授权分组",
				ProviderCode:         "openai",
				Enabled:              true,
				GroupType:            "personal",
				AccessType:           "authorized",
				GroupAuthorizationID: "rauthgrant_group",
				AuthorizationStatus:  "active",
				AccountCount:         0,
				AuthorizationSourceSummary: &managementgroups.AuthorizationSourceSummary{
					ActiveSourceCount: 1,
					HasManual:         true,
					TeamNames:         []string{},
				},
			}},
			Total:    1,
			Page:     1,
			PageSize: 50,
			RuntimeSnapshot: managementgroups.RuntimeSnapshot{
				AccountConcurrencyAvailable: true,
			},
		},
	}
	handler := newManagementGroupListHandler(service, managementGroupScopeSelf)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-groups", nil)
	req = requestWithManagementAuthContext(req, managementauth.Context{
		SystemAccountID: "sys_user",
		Role:            "user",
	})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	raw := rec.Body.String()
	for _, forbidden := range []string{"accountIds", "authorizationSources"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, raw)
		}
	}
	if !strings.Contains(raw, "authorizationSourceSummary") {
		t.Fatalf("response omitted authorization source summary: %s", raw)
	}
}

func TestRouterRegistersManagementGroupListAsNoStoreLimitedReadRoutes(t *testing.T) {
	service := &managementGroupListServiceStub{
		result: managementgroups.ListResult{Items: []managementgroups.ListItem{}, Page: 1, PageSize: 50},
	}
	readAuthenticator := &managementAPIAuthenticatorStub{
		context: managementauth.Context{
			SystemAccountID: "sys_admin",
			Username:        "admin",
			Role:            "admin",
			SessionID:       "sess_read",
		},
	}
	touchAuthenticator := &managementAPIAuthenticatorStub{
		context: managementauth.Context{
			SystemAccountID: "sys_admin",
			Username:        "admin",
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
	router := NewRouter(RouterOptions{
		Config:                            config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                            slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		SystemAPIRateLimitReader:          systemAPIRateLimitReaderStub{settings: port.SystemAPIRateLimitSettings{IPReadPerMinute: 600, IPReadBurstPer10Seconds: 120, UserReadPerMinute: 300}},
		SystemAPIIPRateLimiter:            ipLimiter,
		SystemAPIAuthenticatedRateLimiter: userLimiter,
		ManagementAPIAuthMiddleware:       NewManagementAPIAuthMiddleware(readAuthenticator),
		ManagementAPIAuthTouchMiddleware:  NewManagementAPIAuthTouchMiddleware(touchAuthenticator),
		ManagementGroupListHandler:        newManagementGroupListHandler(service, managementGroupScopeAdmin),
		ManagementMyGroupListHandler:      newManagementGroupListHandler(service, managementGroupScopeSelf),
	})

	for _, path := range []string{"/__aisys__/api/groups", "/__aisys__/api/my-groups"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Cookie", "juhe_ai_session=session-token")
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200; body = %s", path, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("%s Cache-Control = %q, want no-store", path, got)
		}
	}
	if readAuthenticator.cookieHeader != "juhe_ai_session=session-token" {
		t.Fatalf("read auth cookie = %q", readAuthenticator.cookieHeader)
	}
	if touchAuthenticator.touchCookieHeader != "" {
		t.Fatalf("touch auth cookie = %q, want empty for list reads", touchAuthenticator.touchCookieHeader)
	}
	if ipLimiter.calls != 2 ||
		ipLimiter.settings.PerMinute != 600 ||
		ipLimiter.settings.BurstPer10Seconds != 120 {
		t.Fatalf("IP limiter calls=%d settings=%+v", ipLimiter.calls, ipLimiter.settings)
	}
	if userLimiter.calls != 2 || userLimiter.limit != 300 {
		t.Fatalf("user limiter calls=%d limit=%d", userLimiter.calls, userLimiter.limit)
	}
	if service.calls != 2 {
		t.Fatalf("service calls = %d, want 2", service.calls)
	}
}

func TestRouterManagementGroupListDoesNotRegressPostCreateOnSamePaths(t *testing.T) {
	authenticator := &managementAPIAuthenticatorStub{
		context: managementauth.Context{
			SystemAccountID: "sys_admin",
			Username:        "admin",
			Role:            "admin",
			SessionID:       "sess_admin",
		},
	}
	listCalls := 0
	createCalls := 0
	router := NewRouter(RouterOptions{
		Config: config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger: slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementGroupListHandler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			listCalls++
			writeData(w, http.StatusOK, managementgroups.ListResult{Items: []managementgroups.ListItem{}})
		}),
		ManagementGroupCreateHandler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			createCalls++
			writeData(w, http.StatusCreated, map[string]string{"id": "grp_created"})
		}),
		ManagementAPIAuthMiddleware:      NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware: NewManagementAPIAuthTouchMiddleware(authenticator),
	})

	getReq := httptest.NewRequest(http.MethodGet, "/__aisys__/api/groups", nil)
	getReq.Header.Set("Cookie", "juhe_ai_session=session-token")
	getRec := httptest.NewRecorder()
	router.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200; body = %s", getRec.Code, getRec.Body.String())
	}

	postReq := httptest.NewRequest(
		http.MethodPost,
		"/__aisys__/api/groups",
		strings.NewReader(`{"name":"分组","providerCode":"openai"}`),
	)
	postReq.Header.Set("Cookie", "juhe_ai_session=session-token")
	postReq.Header.Set("Content-Type", "application/json")
	postRec := httptest.NewRecorder()
	router.ServeHTTP(postRec, postReq)
	if postRec.Code != http.StatusCreated {
		t.Fatalf("POST status = %d, want 201; body = %s", postRec.Code, postRec.Body.String())
	}
	if listCalls != 1 || createCalls != 1 {
		t.Fatalf("list calls=%d create calls=%d", listCalls, createCalls)
	}
	if authenticator.cookieHeader == "" || authenticator.touchCookieHeader == "" {
		t.Fatalf("auth headers read=%q touch=%q", authenticator.cookieHeader, authenticator.touchCookieHeader)
	}
}

func TestRouterDoesNotRegisterManagementGroupListWhenDisabled(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeData(w, http.StatusOK, managementgroups.ListResult{Items: []managementgroups.ListItem{}})
	})
	router := NewRouter(RouterOptions{
		Config:                       config.Config{Host: "127.0.0.1", Port: 3000},
		Logger:                       slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementGroupListHandler:   handler,
		ManagementMyGroupListHandler: handler,
	})

	for _, path := range []string{"/__aisys__/api/groups", "/__aisys__/api/my-groups"} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404 while disabled", path, rec.Code)
		}
	}
}

func requestWithManagementAuthContext(
	req *http.Request,
	authContext managementauth.Context,
) *http.Request {
	return req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, authContext))
}

type managementGroupListServiceStub struct {
	calls  int
	input  managementgroups.ListInput
	result managementgroups.ListResult
	err    error
}

func (s *managementGroupListServiceStub) List(
	_ *http.Request,
	input managementgroups.ListInput,
) (managementgroups.ListResult, error) {
	s.calls++
	s.input = input
	return s.result, s.err
}

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
	"juhe-ai/backend-go/internal/modules/managementstatsoverview"
	"juhe-ai/backend-go/internal/store/port"
)

func TestManagementStatsUsageOverviewHandlersApplyAdminAndSelfScopes(t *testing.T) {
	tests := []struct {
		name          string
		handler       func(managementStatsUsageOverviewService) http.Handler
		auth          managementauth.Context
		target        string
		wantStatus    int
		wantScope     string
		wantStartDate string
	}{
		{name: "admin global", handler: newManagementStatsUsageOverviewHandler, auth: managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"}, target: "/__aisys__/api/stats/usage-overview?startDate=2026-07-01", wantStatus: 200, wantScope: "global", wantStartDate: "2026-07-01"},
		{name: "admin selected user", handler: newManagementStatsUsageOverviewHandler, auth: managementauth.Context{SystemAccountID: "sys_admin", Role: "super_admin"}, target: "/__aisys__/api/stats/usage-overview?systemAccountId=%20sys_user%20", wantStatus: 200, wantScope: "sys_user"},
		{name: "admin all", handler: newManagementStatsUsageOverviewHandler, auth: managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"}, target: "/__aisys__/api/stats/usage-overview?systemAccountId=all", wantStatus: 200, wantScope: "global"},
		{name: "admin rejects user", handler: newManagementStatsUsageOverviewHandler, auth: managementauth.Context{SystemAccountID: "sys_user", Role: "user"}, target: "/__aisys__/api/stats/usage-overview", wantStatus: 403},
		{name: "self ignores forged scope", handler: newManagementMyStatsUsageOverviewHandler, auth: managementauth.Context{SystemAccountID: "sys_user", Role: "user"}, target: "/__aisys__/api/my-stats/usage-overview?systemAccountId=sys_other", wantStatus: 200, wantScope: "sys_user"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &managementStatsUsageOverviewServiceStub{result: managementstatsoverview.Overview{HourlyTrend: []managementstatsoverview.TrendPoint{}, ModelDistribution: []managementstatsoverview.ModelPoint{}, Errors: []managementstatsoverview.ErrorPoint{}}}
			req := httptest.NewRequest(http.MethodGet, test.target, nil)
			req = requestWithManagementAuthContext(req, test.auth)
			rec := httptest.NewRecorder()

			test.handler(service).ServeHTTP(rec, req)

			if rec.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, test.wantStatus, rec.Body.String())
			}
			if test.wantStatus != http.StatusOK {
				if service.calls != 0 {
					t.Fatalf("service calls = %d", service.calls)
				}
				return
			}
			if service.scope != test.wantScope || service.input.StartDate != test.wantStartDate {
				t.Fatalf("scope/input = %q / %+v", service.scope, service.input)
			}
			var envelope DataResponse
			if err := json.NewDecoder(rec.Body).Decode(&envelope); err != nil {
				t.Fatalf("decode: %v", err)
			}
		})
	}
}

func TestManagementStatsUsageOverviewHandlersValidateDatesAndRedactErrors(t *testing.T) {
	for _, target := range []string{
		"/__aisys__/api/stats/usage-overview?startDate=2026/07/01",
		"/__aisys__/api/stats/usage-overview?endDate=",
		"/__aisys__/api/stats/usage-overview?startDate=2026-07-01&startDate=2026-07-02",
	} {
		service := &managementStatsUsageOverviewServiceStub{}
		req := requestWithManagementAuthContext(httptest.NewRequest(http.MethodGet, target, nil), managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"})
		rec := httptest.NewRecorder()

		newManagementStatsUsageOverviewHandler(service).ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest || service.calls != 0 {
			t.Fatalf("%s status = %d, calls = %d, body = %s", target, rec.Code, service.calls, rec.Body.String())
		}
	}

	service := &managementStatsUsageOverviewServiceStub{err: errors.New("postgres password leaked")}
	req := requestWithManagementAuthContext(httptest.NewRequest(http.MethodGet, "/__aisys__/api/stats/usage-overview", nil), managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"})
	rec := httptest.NewRecorder()
	newManagementStatsUsageOverviewHandler(service).ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError || rec.Body.String() != "{\"message\":\"服务器内部错误\"}\n" {
		t.Fatalf("redacted response = %d %s", rec.Code, rec.Body.String())
	}
}

func TestRouterRegistersStatsUsageOverviewAsLimitedNoTouchReadRoutes(t *testing.T) {
	authenticator := &managementAPIAuthenticatorStub{context: managementauth.Context{SystemAccountID: "sys_admin", Role: "admin", SessionID: "sess_read"}}
	userLimiter := &systemAPIAuthenticatedRateLimiterStub{decision: SystemAPIRateLimitDecision{Allowed: true}}
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeData(w, http.StatusOK, map[string]string{"ok": "yes"})
	})
	opts := RouterOptions{
		Config:                                config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                                slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		SystemAPIRateLimitReader:              systemAPIRateLimitReaderStub{settings: port.SystemAPIRateLimitSettings{IPReadPerMinute: 600, IPReadBurstPer10Seconds: 120, UserReadPerMinute: 300}},
		SystemAPIIPRateLimiter:                NewInMemorySystemAPIIPRateLimiter(),
		SystemAPIAuthenticatedRateLimiter:     userLimiter,
		ManagementAPIAuthMiddleware:           NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware:      NewManagementAPIAuthTouchMiddleware(authenticator),
		ManagementStatsUsageOverviewHandler:   handler,
		ManagementMyStatsUsageOverviewHandler: handler,
	}
	if !managementBusinessRoutesConfigured(opts) || managementWriteRoutesConfigured(opts) {
		t.Fatalf("stats overview route classification is wrong")
	}
	router := NewRouter(opts)
	for _, path := range []string{"/__aisys__/api/stats/usage-overview", "/__aisys__/api/my-stats/usage-overview"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Cookie", "juhe_ai_session=session-token")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s response = %d, cache = %q", path, rec.Code, rec.Header().Get("Cache-Control"))
		}
	}
	if authenticator.cookieHeader != "juhe_ai_session=session-token" || authenticator.touchCookieHeader != "" {
		t.Fatalf("read/touch auth = %q / %q", authenticator.cookieHeader, authenticator.touchCookieHeader)
	}
	if userLimiter.calls != 2 || userLimiter.limit != 300 {
		t.Fatalf("user limiter calls/limit = %d/%d", userLimiter.calls, userLimiter.limit)
	}
}

func TestRouterDoesNotRegisterStatsUsageOverviewWithoutOptIn(t *testing.T) {
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	router := NewRouter(RouterOptions{Config: config.Config{Host: "127.0.0.1", Port: 3000}, ManagementStatsUsageOverviewHandler: handler, ManagementMyStatsUsageOverviewHandler: handler})
	for _, path := range []string{"/__aisys__/api/stats/usage-overview", "/__aisys__/api/my-stats/usage-overview"} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d", path, rec.Code)
		}
	}
}

type managementStatsUsageOverviewServiceStub struct {
	calls  int
	scope  string
	input  managementstatsoverview.Input
	result managementstatsoverview.Overview
	err    error
}

func (s *managementStatsUsageOverviewServiceStub) Overview(_ context.Context, scope string, input managementstatsoverview.Input) (managementstatsoverview.Overview, error) {
	s.calls++
	s.scope = scope
	s.input = input
	return s.result, s.err
}

var _ managementStatsUsageOverviewService = (*managementStatsUsageOverviewServiceStub)(nil)

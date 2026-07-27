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
	"juhe-ai/backend-go/internal/modules/managementstats"
	"juhe-ai/backend-go/internal/store/port"
)

func TestManagementSystemMetricsHandlerRequiresAdminParsesRangeAndReturnsEnvelope(t *testing.T) {
	service := &managementSystemMetricsServiceStub{
		result: managementstats.SystemMetricsOverview{
			HourlyTrend:                  []managementstats.SystemMetricsHourly{{StatHour: "2026-07-20T00", SampleCount: 2}},
			ProcessEventLoopLatestStatus: []managementstats.SystemMetricsProcessStatus{},
			ProcessEventLoopPeakStatus:   []managementstats.SystemMetricsProcessStatus{},
			ProcessEventLoopTrend:        []managementstats.SystemMetricsProcessTrend{},
		},
	}
	handler := newManagementSystemMetricsHandler(service)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/stats/system-metrics?startDate=%EF%BB%BF2026-07-20%EF%BB%BF&endDate=2026-07-22&ignored=value", nil)
	req = withManagementSystemMetricsAuth(req, "admin")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if service.input != (managementstats.SystemMetricsQuery{StartDate: "2026-07-20", EndDate: "2026-07-22"}) {
		t.Fatalf("query = %+v", service.input)
	}
	if !service.deadlineSet {
		t.Fatal("system metrics request must have a bounded deadline")
	}
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Pragma") != "no-cache" {
		t.Fatalf("cache headers = %+v", rec.Header())
	}
	var body struct {
		Data managementstats.SystemMetricsOverview `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data.HourlyTrend) != 1 || body.Data.HourlyTrend[0].StatHour != "2026-07-20T00" {
		t.Fatalf("data = %+v", body.Data)
	}
}

func TestManagementSystemMetricsHandlerRejectsOrdinaryUserAndInvalidDates(t *testing.T) {
	service := &managementSystemMetricsServiceStub{}
	tests := []struct {
		name    string
		role    string
		query   string
		status  int
		message string
	}{
		{name: "ordinary user", role: "user", status: 403, message: "需要管理员权限"},
		{name: "invalid start format", role: "admin", query: "?startDate=2026-7-1", status: 400, message: "开始日期格式应为 YYYY-MM-DD"},
		{name: "invalid end format", role: "admin", query: "?endDate=", status: 400, message: "结束日期格式应为 YYYY-MM-DD"},
		{name: "duplicate boundary", role: "admin", query: "?startDate=2026-07-01&startDate=2026-07-02", status: 400, message: "监控日期范围不合法"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service.called = false
			req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/stats/system-metrics"+test.query, nil)
			req = withManagementSystemMetricsAuth(req, test.role)
			rec := httptest.NewRecorder()

			newManagementSystemMetricsHandler(service).ServeHTTP(rec, req)

			if rec.Code != test.status {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, test.status, rec.Body.String())
			}
			if service.called {
				t.Fatal("service should not be called")
			}
			var body map[string]string
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if body["message"] != test.message {
				t.Fatalf("body = %+v", body)
			}
		})
	}
}

func TestManagementSystemMetricsHandlerRedactsServiceErrors(t *testing.T) {
	service := &managementSystemMetricsServiceStub{err: errors.New("postgres password leaked")}
	req := withManagementSystemMetricsAuth(httptest.NewRequest(http.MethodGet, "/__aisys__/api/stats/system-metrics", nil), "admin")
	rec := httptest.NewRecorder()

	newManagementSystemMetricsHandler(service).ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError || rec.Body.String() != "{\"message\":\"服务器内部错误\"}\n" {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRouterRegistersManagementSystemMetricsTrendAsLimitedNoTouchReadAndLeavesRuntime404(t *testing.T) {
	readAuthenticator := &managementAPIAuthenticatorStub{context: managementauth.Context{
		SystemAccountID: "sys_admin", Role: "admin", SessionID: "sess_read",
	}}
	touchAuthenticator := &managementAPIAuthenticatorStub{context: managementauth.Context{
		SystemAccountID: "sys_admin", Role: "admin", SessionID: "sess_touch",
	}}
	ipLimiter := &publicSettingsRateLimiterStub{decision: SystemAPIRateLimitDecision{Allowed: true}}
	userLimiter := &systemAPIAuthenticatedRateLimiterStub{decision: SystemAPIRateLimitDecision{Allowed: true}}
	handlerCalls := 0
	router := NewRouter(RouterOptions{
		Config: config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger: slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		SystemAPIRateLimitReader: systemAPIRateLimitReaderStub{settings: port.SystemAPIRateLimitSettings{
			IPReadPerMinute: 600, IPReadBurstPer10Seconds: 120, UserReadPerMinute: 300,
		}},
		SystemAPIIPRateLimiter:            ipLimiter,
		SystemAPIAuthenticatedRateLimiter: userLimiter,
		ManagementAPIAuthMiddleware:       NewManagementAPIAuthMiddleware(readAuthenticator),
		ManagementAPIAuthTouchMiddleware:  NewManagementAPIAuthTouchMiddleware(touchAuthenticator),
		ManagementSystemMetricsHandler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			handlerCalls++
			writeData(w, http.StatusOK, map[string]any{"hourlyTrend": []any{}})
		}),
		ManagementSystemMetricsTrendHandler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			handlerCalls++
			writeData(w, http.StatusOK, map[string]any{
				"hourlyTrend": []any{}, "processEventLoopLatestStatus": []any{},
				"processEventLoopPeakStatus": []any{}, "processEventLoopTrend": []any{},
			})
		}),
	})

	for _, path := range []string{"/__aisys__/api/stats/system-metrics", "/__aisys__/api/stats/system-metrics/trend"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Cookie", "juhe_ai_session=session-token")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s status=%d cache=%q handlerCalls=%d body=%s", path, rec.Code, rec.Header().Get("Cache-Control"), handlerCalls, rec.Body.String())
		}
	}
	if handlerCalls != 2 || readAuthenticator.cookieHeader != "juhe_ai_session=session-token" || touchAuthenticator.touchCookieHeader != "" {
		t.Fatalf("handlerCalls=%d readCookie=%q touchCookie=%q", handlerCalls, readAuthenticator.cookieHeader, touchAuthenticator.touchCookieHeader)
	}
	if ipLimiter.calls != 2 || userLimiter.calls != 2 || userLimiter.limit != 300 {
		t.Fatalf("limiter calls ip=%d user=%d userLimit=%d", ipLimiter.calls, userLimiter.calls, userLimiter.limit)
	}

	runtimeReq := httptest.NewRequest(http.MethodGet, "/__aisys__/api/stats/system-metrics/runtime", nil)
	runtimeReq.Header.Set("Cookie", "juhe_ai_session=session-token")
	runtimeRec := httptest.NewRecorder()
	router.ServeHTTP(runtimeRec, runtimeReq)
	if runtimeRec.Code != http.StatusNotFound || handlerCalls != 2 {
		t.Fatalf("runtime status=%d handlerCalls=%d body=%s", runtimeRec.Code, handlerCalls, runtimeRec.Body.String())
	}
}

type managementSystemMetricsServiceStub struct {
	called      bool
	deadlineSet bool
	input       managementstats.SystemMetricsQuery
	result      managementstats.SystemMetricsOverview
	err         error
}

func (s *managementSystemMetricsServiceStub) SystemMetrics(ctx context.Context, input managementstats.SystemMetricsQuery) (managementstats.SystemMetricsOverview, error) {
	s.called = true
	_, s.deadlineSet = ctx.Deadline()
	s.input = input
	return s.result, s.err
}

func withManagementSystemMetricsAuth(request *http.Request, role string) *http.Request {
	return request.WithContext(context.WithValue(request.Context(), managementAuthContextKey, managementauth.Context{
		SystemAccountID: "sys_actor",
		Role:            role,
		SessionID:       "sess_actor",
	}))
}

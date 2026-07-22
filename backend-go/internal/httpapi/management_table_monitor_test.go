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
	"juhe-ai/backend-go/internal/modules/managementtablemonitor"
	"juhe-ai/backend-go/internal/store/port"
)

func TestManagementTableMonitorHandlerParsesNodeCompatibleReadRoutes(t *testing.T) {
	service := &managementTableMonitorServiceStub{
		overview:        managementtablemonitor.Overview{Databases: []managementtablemonitor.DatabaseStorageSnapshot{}, Tables: []managementtablemonitor.TableStorageOverviewSnapshot{}},
		tableHistory:    []managementtablemonitor.TableStorageSnapshot{},
		databaseHistory: []managementtablemonitor.DatabaseStorageSnapshot{},
	}
	handler := newManagementTableMonitorHandler(service)

	tests := []struct {
		path  string
		check func(t *testing.T)
	}{
		{
			path: "/__aisys__/api/table-monitor/overview?limit=25",
			check: func(t *testing.T) {
				if service.overviewInput.Limit != 25 {
					t.Fatalf("overview input = %+v", service.overviewInput)
				}
			},
		},
		{
			path: "/__aisys__/api/table-monitor/history?databaseRole=usage-catalog&tableName=%20usage_records%20&startAt=2026-07-22T12:00:00Z&endAt=2026-07-20T12:00:00Z&limit=720",
			check: func(t *testing.T) {
				input := service.tableHistoryInput
				if input.DatabaseRole != port.MonitoredDatabaseRoleUsageCatalog || input.TableName != "usage_records" || input.Limit != 720 || input.StartAt.IsZero() || input.EndAt.IsZero() {
					t.Fatalf("table history input = %+v", input)
				}
			},
		},
		{
			path: "/__aisys__/api/table-monitor/database-history?startAt=2026-07-01&endAt=2026-07-22&limit=100",
			check: func(t *testing.T) {
				if service.databaseHistoryInput.Limit != 100 || service.databaseHistoryInput.StartAt.IsZero() || service.databaseHistoryInput.EndAt.IsZero() {
					t.Fatalf("database history input = %+v", service.databaseHistoryInput)
				}
			},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.path, func(t *testing.T) {
			req := withManagementTableMonitorAuth(httptest.NewRequest(http.MethodGet, testCase.path, nil), "admin")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
			}
			if !service.deadlineSet {
				t.Fatal("table monitor read must have a bounded deadline")
			}
			var envelope struct {
				Data json.RawMessage `json:"data"`
			}
			if err := json.NewDecoder(rec.Body).Decode(&envelope); err != nil || len(envelope.Data) == 0 {
				t.Fatalf("decode envelope: %v body=%s", err, rec.Body.String())
			}
			testCase.check(t)
		})
	}
}

func TestManagementTableMonitorHandlerRejectsInvalidQueriesAndNonAdmin(t *testing.T) {
	service := &managementTableMonitorServiceStub{}
	handler := newManagementTableMonitorHandler(service)
	tests := []struct {
		name       string
		role       string
		path       string
		wantStatus int
	}{
		{name: "non admin", role: "user", path: "/__aisys__/api/table-monitor/overview", wantStatus: http.StatusForbidden},
		{name: "overview limit", role: "admin", path: "/__aisys__/api/table-monitor/overview?limit=1001", wantStatus: http.StatusBadRequest},
		{name: "history role", role: "admin", path: "/__aisys__/api/table-monitor/history?databaseRole=archive&tableName=x", wantStatus: http.StatusBadRequest},
		{name: "history table", role: "admin", path: "/__aisys__/api/table-monitor/history?databaseRole=business&tableName=%20", wantStatus: http.StatusBadRequest},
		{name: "database history duplicate limit", role: "admin", path: "/__aisys__/api/table-monitor/database-history?limit=1&limit=2", wantStatus: http.StatusBadRequest},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			req := withManagementTableMonitorAuth(httptest.NewRequest(http.MethodGet, testCase.path, nil), testCase.role)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != testCase.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, testCase.wantStatus, rec.Body.String())
			}
		})
	}
	if service.overviewCalled || service.tableHistoryCalled || service.databaseHistoryCalled {
		t.Fatal("invalid or unauthorized requests must not reach service")
	}
}

func TestManagementTableMonitorHandlerUsesGenericDependencyErrors(t *testing.T) {
	service := &managementTableMonitorServiceStub{overviewErr: errors.New("postgres password leaked")}
	handler := newManagementTableMonitorHandler(service)
	req := withManagementTableMonitorAuth(httptest.NewRequest(http.MethodGet, "/__aisys__/api/table-monitor/overview", nil), "admin")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "服务器内部错误") || strings.Contains(rec.Body.String(), "password") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRouterRegistersOnlyTableMonitorPostgresReadsWithoutSessionTouch(t *testing.T) {
	service := &managementTableMonitorServiceStub{
		overview:        managementtablemonitor.Overview{Databases: []managementtablemonitor.DatabaseStorageSnapshot{}, Tables: []managementtablemonitor.TableStorageOverviewSnapshot{}},
		tableHistory:    []managementtablemonitor.TableStorageSnapshot{},
		databaseHistory: []managementtablemonitor.DatabaseStorageSnapshot{},
	}
	events := []string{}
	authenticator := &managementClientIPStatsRouterAuthenticator{
		events: &events,
		authContext: managementauth.Context{
			SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_admin",
		},
	}
	ipLimiter := &managementClientIPStatsRouterIPLimiter{events: &events, decision: SystemAPIRateLimitDecision{Allowed: true}}
	userLimiter := &managementClientIPStatsRouterUserLimiter{events: &events, decision: SystemAPIRateLimitDecision{Allowed: true}}
	router := NewRouter(RouterOptions{
		Config: config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger: slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		SystemAPIRateLimitReader: systemAPIRateLimitReaderStub{settings: port.SystemAPIRateLimitSettings{
			IPReadPerMinute: 600, IPReadBurstPer10Seconds: 120, UserReadPerMinute: 300,
		}},
		SystemAPIIPRateLimiter:            ipLimiter,
		SystemAPIAuthenticatedRateLimiter: userLimiter,
		ManagementTableMonitorHandler:     newManagementTableMonitorHandler(service),
		ManagementAPIAuthMiddleware:       NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware:  NewManagementAPIAuthTouchMiddleware(authenticator),
	})
	for _, path := range []string{
		"/__aisys__/api/table-monitor/overview",
		"/__aisys__/api/table-monitor/history?databaseRole=business&tableName=system_accounts",
		"/__aisys__/api/table-monitor/database-history",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Cookie", "juhe_ai_session=session-token")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s status=%d cache=%q body=%s", path, rec.Code, rec.Header().Get("Cache-Control"), rec.Body.String())
		}
	}
	if authenticator.readCalls != 3 || authenticator.touchCalls != 0 {
		t.Fatalf("read auth calls=%d touch calls=%d", authenticator.readCalls, authenticator.touchCalls)
	}
	if ipLimiter.calls != 3 || userLimiter.calls != 3 || userLimiter.limit != 300 {
		t.Fatalf("IP limiter calls=%d user limiter calls=%d limit=%d", ipLimiter.calls, userLimiter.calls, userLimiter.limit)
	}
	cleanupReq := httptest.NewRequest(http.MethodPost, "/__aisys__/api/table-monitor/non-business-data/cleanup", strings.NewReader(`{"cutoffAt":"2026-07-01T00:00:00.000Z"}`))
	cleanupReq.Header.Set("Cookie", "juhe_ai_session=session-token")
	cleanupRec := httptest.NewRecorder()
	router.ServeHTTP(cleanupRec, cleanupReq)
	if cleanupRec.Code != http.StatusNotFound {
		t.Fatalf("cleanup status=%d, want 404; body=%s", cleanupRec.Code, cleanupRec.Body.String())
	}
	if authenticator.touchCalls != 0 {
		t.Fatalf("read-only table monitor routes touched session %d times", authenticator.touchCalls)
	}
}

func withManagementTableMonitorAuth(req *http.Request, role string) *http.Request {
	return req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{
		SystemAccountID: "sys_admin",
		Role:            role,
	}))
}

type managementTableMonitorServiceStub struct {
	deadlineSet           bool
	overviewCalled        bool
	overviewInput         managementtablemonitor.OverviewInput
	overview              managementtablemonitor.Overview
	overviewErr           error
	tableHistoryCalled    bool
	tableHistoryInput     managementtablemonitor.TableHistoryInput
	tableHistory          []managementtablemonitor.TableStorageSnapshot
	tableHistoryErr       error
	databaseHistoryCalled bool
	databaseHistoryInput  managementtablemonitor.DatabaseHistoryInput
	databaseHistory       []managementtablemonitor.DatabaseStorageSnapshot
	databaseHistoryErr    error
}

func (s *managementTableMonitorServiceStub) Overview(r *http.Request, input managementtablemonitor.OverviewInput) (managementtablemonitor.Overview, error) {
	s.overviewCalled = true
	s.overviewInput = input
	_, s.deadlineSet = r.Context().Deadline()
	return s.overview, s.overviewErr
}

func (s *managementTableMonitorServiceStub) TableHistory(r *http.Request, input managementtablemonitor.TableHistoryInput) ([]managementtablemonitor.TableStorageSnapshot, error) {
	s.tableHistoryCalled = true
	s.tableHistoryInput = input
	_, s.deadlineSet = r.Context().Deadline()
	return s.tableHistory, s.tableHistoryErr
}

func (s *managementTableMonitorServiceStub) DatabaseHistory(r *http.Request, input managementtablemonitor.DatabaseHistoryInput) ([]managementtablemonitor.DatabaseStorageSnapshot, error) {
	s.databaseHistoryCalled = true
	s.databaseHistoryInput = input
	_, s.deadlineSet = r.Context().Deadline()
	return s.databaseHistory, s.databaseHistoryErr
}

package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementstats"
	"juhe-ai/backend-go/internal/store/port"
)

func TestRouterRegistersManagementStatsReadRoutesWithNoTouchAuth(t *testing.T) {
	readAuthenticator := &managementAPIAuthenticatorStub{context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_read"}}
	touchAuthenticator := &managementAPIAuthenticatorStub{context: managementauth.Context{SystemAccountID: "sys_admin", Username: "admin", Role: "admin", SessionID: "sess_touch"}}
	reader := &managementStatsHTTPReaderStub{}
	service := managementStatsHTTPService(reader)
	router := NewRouter(RouterOptions{
		Config:                               config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                               slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementStatsAccountUsageHandler:   NewManagementStatsAccountUsageHandler(service),
		ManagementMyStatsAccountUsageHandler: NewManagementMyStatsAccountUsageHandler(service),
		ManagementStatsAccountUsageSummaryHandler:     NewManagementStatsAccountUsageSummaryHandler(service),
		ManagementMyStatsAccountUsageSummaryHandler:   NewManagementMyStatsAccountUsageSummaryHandler(service),
		ManagementStatsAccountUsageTrendHandler:       NewManagementStatsAccountUsageTrendHandler(service),
		ManagementMyStatsAccountUsageTrendHandler:     NewManagementMyStatsAccountUsageTrendHandler(service),
		ManagementStatsAIPerformanceHandler:           NewManagementStatsAIPerformanceHandler(service),
		ManagementMyStatsAIPerformanceHandler:         NewManagementMyStatsAIPerformanceHandler(service),
		ManagementStatsAIPerformanceSeriesHandler:     NewManagementStatsAIPerformanceSeriesHandler(service),
		ManagementMyStatsAIPerformanceSeriesHandler:   NewManagementMyStatsAIPerformanceSeriesHandler(service),
		ManagementStatsAIPerformanceAccountsHandler:   NewManagementStatsAIPerformanceAccountsHandler(service),
		ManagementMyStatsAIPerformanceAccountsHandler: NewManagementMyStatsAIPerformanceAccountsHandler(service),
		ManagementAPIAuthMiddleware:                   NewManagementAPIAuthMiddleware(readAuthenticator),
		ManagementAPIAuthTouchMiddleware:              NewManagementAPIAuthTouchMiddleware(touchAuthenticator),
	})

	paths := []string{
		"/__aisys__/api/stats/account-usage?systemAccountId=sys_target",
		"/__aisys__/api/stats/account-usage/summary?systemAccountId=sys_target",
		"/__aisys__/api/stats/account-usage/trend?accountIds=acc_1,acc_2",
		"/__aisys__/api/my-stats/account-usage",
		"/__aisys__/api/my-stats/account-usage/summary",
		"/__aisys__/api/my-stats/account-usage/trend?accountIds=acc_1",
		"/__aisys__/api/stats/ai-performance?startDate=2026-07-22&endDate=2026-07-22",
		"/__aisys__/api/stats/ai-performance/series?startDate=2026-07-22&endDate=2026-07-22&accountIds=acc_1",
		"/__aisys__/api/stats/ai-performance/accounts?limit=50",
		"/__aisys__/api/my-stats/ai-performance",
		"/__aisys__/api/my-stats/ai-performance/series?accountIds=acc_1",
		"/__aisys__/api/my-stats/ai-performance/accounts",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			readAuthenticator.cookieHeader = ""
			touchAuthenticator.touchCookieHeader = ""
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("Cookie", "juhe_ai_session=session-token")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
			}
			if readAuthenticator.cookieHeader != "juhe_ai_session=session-token" || touchAuthenticator.touchCookieHeader != "" {
				t.Fatalf("auth headers read=%q touch=%q", readAuthenticator.cookieHeader, touchAuthenticator.touchCookieHeader)
			}
		})
	}
	if len(reader.accountUsageInputs) == 0 || reader.accountUsageInputs[0].Scope.SystemAccountID != "sys_target" || reader.accountUsageInputs[0].Scope.ScopeType != "caller_account" {
		t.Fatalf("admin filtered account scope = %+v", reader.accountUsageInputs)
	}
}

func TestManagementStatsAdminRoutesRequireAdminAndSelfIgnoresSystemAccountFilter(t *testing.T) {
	reader := &managementStatsHTTPReaderStub{}
	service := managementStatsHTTPService(reader)
	authenticator := &managementAPIAuthenticatorStub{context: managementauth.Context{SystemAccountID: "sys_user", Username: "user", Role: "user", SessionID: "sess_read"}}
	router := NewRouter(RouterOptions{
		Config:                               config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                               slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementStatsAccountUsageHandler:   NewManagementStatsAccountUsageHandler(service),
		ManagementMyStatsAccountUsageHandler: NewManagementMyStatsAccountUsageHandler(service),
		ManagementAPIAuthMiddleware:          NewManagementAPIAuthMiddleware(authenticator),
	})

	adminReq := httptest.NewRequest(http.MethodGet, "/__aisys__/api/stats/account-usage", nil)
	adminRec := httptest.NewRecorder()
	router.ServeHTTP(adminRec, adminReq)
	if adminRec.Code != http.StatusForbidden {
		t.Fatalf("admin status = %d, body = %s", adminRec.Code, adminRec.Body.String())
	}

	selfReq := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-stats/account-usage?systemAccountId=sys_other", nil)
	selfRec := httptest.NewRecorder()
	router.ServeHTTP(selfRec, selfReq)
	if selfRec.Code != http.StatusOK {
		t.Fatalf("self status = %d, body = %s", selfRec.Code, selfRec.Body.String())
	}
	got := reader.accountUsageInputs[len(reader.accountUsageInputs)-1].Scope
	if got.SystemAccountID != "sys_user" || got.ScopeType != "caller_account" {
		t.Fatalf("self scope = %+v", got)
	}
}

func TestManagementStatsAIRejectsInvalidDateAndAccountOptionsLimit(t *testing.T) {
	service := managementStatsHTTPService(&managementStatsHTTPReaderStub{})
	for _, test := range []struct {
		name    string
		handler http.Handler
		path    string
	}{
		{name: "date", handler: NewManagementStatsAIPerformanceHandler(service), path: "/?startDate=2026-7-2"},
		{name: "base account ids", handler: NewManagementStatsAIPerformanceHandler(service), path: "/?accountIds=acc_1"},
		{name: "base bracket ids", handler: NewManagementStatsAIPerformanceHandler(service), path: "/?accountIds[]=acc_1"},
		{name: "series missing ids", handler: NewManagementStatsAIPerformanceSeriesHandler(service), path: "/"},
		{name: "series empty ids", handler: NewManagementStatsAIPerformanceSeriesHandler(service), path: "/?accountIds="},
		{name: "series csv", handler: NewManagementStatsAIPerformanceSeriesHandler(service), path: "/?accountIds=acc_1,acc_2"},
		{name: "series indexed bracket", handler: NewManagementStatsAIPerformanceSeriesHandler(service), path: "/?accountIds[0]=acc_1"},
		{name: "limit", handler: NewManagementStatsAIPerformanceAccountsHandler(service), path: "/?limit=51"},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, test.path, nil)
			req = requestWithManagementAuthContext(req, managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"})
			rec := httptest.NewRecorder()
			test.handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestManagementStatsAccountUsageListRejectsIncludeSummary(t *testing.T) {
	service := managementStatsHTTPService(&managementStatsHTTPReaderStub{})
	req := httptest.NewRequest(http.MethodGet, "/?includeSummary=false", nil)
	req = requestWithManagementAuthContext(req, managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"})
	rec := httptest.NewRecorder()
	NewManagementStatsAccountUsageHandler(service).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestManagementStatsProgressiveResponseShapes(t *testing.T) {
	service := managementStatsHTTPService(&managementStatsHTTPReaderStub{})
	for _, test := range []struct {
		name    string
		handler http.Handler
		path    string
		keys    []string
	}{
		{name: "account list", handler: NewManagementStatsAccountUsageHandler(service), path: "/", keys: []string{"defaultTrendAccountIds", "hasMore", "page", "pageSize", "range", "rows", "total"}},
		{name: "account summary", handler: NewManagementStatsAccountUsageSummaryHandler(service), path: "/", keys: []string{"range", "summary"}},
		{name: "AI base", handler: NewManagementStatsAIPerformanceHandler(service), path: "/", keys: []string{"accounts", "hourlySeries", "range", "summary"}},
		{name: "AI series", handler: NewManagementStatsAIPerformanceSeriesHandler(service), path: "/?accountIds=acc_1", keys: []string{"accounts", "hourlySeries", "range"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, test.path, nil)
			req = requestWithManagementAuthContext(req, managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"})
			rec := httptest.NewRecorder()
			test.handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			var payload struct {
				Data map[string]json.RawMessage `json:"data"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if len(payload.Data) != len(test.keys) {
				t.Fatalf("data keys = %v, want %v", mapKeys(payload.Data), test.keys)
			}
			for _, key := range test.keys {
				if _, ok := payload.Data[key]; !ok {
					t.Fatalf("data keys = %v, missing %q", mapKeys(payload.Data), key)
				}
			}
		})
	}
}

func TestManagementStatsSeriesAccountIDsMatchesNodeBoundary(t *testing.T) {
	values := make([]string, 21)
	for index := range values {
		values[index] = "acc"
	}
	for _, test := range []struct {
		name  string
		query string
		valid bool
	}{
		{name: "bare", query: "accountIds=acc_1&accountIds=acc_2", valid: true},
		{name: "bracket", query: "accountIds[]=acc_1", valid: true},
		{name: "missing", query: "", valid: false},
		{name: "empty", query: "accountIds=", valid: false},
		{name: "csv", query: "accountIds=acc_1,acc_2", valid: false},
		{name: "indexed bracket", query: "accountIds[0]=acc_1", valid: false},
		{name: "too many", query: "accountIds=" + joinStatsQueryValues(values, "&accountIds="), valid: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/?"+test.query, nil)
			_, _, valid := managementStatsSeriesAccountIDs(request.URL.Query())
			if valid != test.valid {
				t.Fatalf("valid = %v, want %v", valid, test.valid)
			}
		})
	}
}

func mapKeys(values map[string]json.RawMessage) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	return result
}

func joinStatsQueryValues(values []string, separator string) string {
	if len(values) == 0 {
		return ""
	}
	result := values[0]
	for _, value := range values[1:] {
		result += separator + value
	}
	return result
}

func managementStatsHTTPService(reader port.ManagementStatsReader) *managementstats.Service {
	return managementstats.NewServiceWithOptions(managementstats.ServiceOptions{
		Store:       managementStatsHTTPTimezoneStub{},
		StatsReader: reader,
		Now:         func() time.Time { return time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC) },
	})
}

type managementStatsHTTPTimezoneStub struct{}

func (managementStatsHTTPTimezoneStub) GetManagementUsageStatsTimezone(context.Context) (string, bool, error) {
	return "UTC", true, nil
}

type managementStatsHTTPReaderStub struct {
	accountUsageInputs []port.ManagementAccountUsageReadInput
}

func (s *managementStatsHTTPReaderStub) ReadManagementAccountUsage(_ context.Context, input port.ManagementAccountUsageReadInput) (port.ManagementAccountUsageReadResult, error) {
	s.accountUsageInputs = append(s.accountUsageInputs, input)
	return port.ManagementAccountUsageReadResult{}, nil
}
func (s *managementStatsHTTPReaderStub) ReadManagementAccountUsageSummary(context.Context, port.ManagementAccountUsageSummaryReadInput) (port.ManagementUsageAggregate, error) {
	return port.ManagementUsageAggregate{}, nil
}
func (s *managementStatsHTTPReaderStub) ReadManagementAccountUsageTrend(context.Context, port.ManagementAccountUsageTrendReadInput) (port.ManagementAccountUsageTrendReadResult, error) {
	return port.ManagementAccountUsageTrendReadResult{}, nil
}
func (s *managementStatsHTTPReaderStub) ReadManagementAIPerformance(context.Context, port.ManagementAIPerformanceReadInput) (port.ManagementAIPerformanceReadResult, error) {
	return port.ManagementAIPerformanceReadResult{}, nil
}
func (s *managementStatsHTTPReaderStub) ReadManagementAIPerformanceSeries(context.Context, port.ManagementAIPerformanceSeriesReadInput) (port.ManagementAIPerformanceSeriesReadResult, error) {
	return port.ManagementAIPerformanceSeriesReadResult{}, nil
}
func (s *managementStatsHTTPReaderStub) ReadManagementAIPerformanceAccounts(context.Context, port.ManagementAIPerformanceAccountsReadInput) ([]port.ManagementStatsAccount, error) {
	return []port.ManagementStatsAccount{}, nil
}

var _ port.ManagementStatsReader = (*managementStatsHTTPReaderStub)(nil)

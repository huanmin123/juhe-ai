package httpapi

import (
	"context"
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
		Config:                                        config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                                        slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementStatsAccountUsageHandler:            NewManagementStatsAccountUsageHandler(service),
		ManagementMyStatsAccountUsageHandler:          NewManagementMyStatsAccountUsageHandler(service),
		ManagementStatsAccountUsageTrendHandler:       NewManagementStatsAccountUsageTrendHandler(service),
		ManagementMyStatsAccountUsageTrendHandler:     NewManagementMyStatsAccountUsageTrendHandler(service),
		ManagementStatsAIPerformanceHandler:           NewManagementStatsAIPerformanceHandler(service),
		ManagementMyStatsAIPerformanceHandler:         NewManagementMyStatsAIPerformanceHandler(service),
		ManagementStatsAIPerformanceAccountsHandler:   NewManagementStatsAIPerformanceAccountsHandler(service),
		ManagementMyStatsAIPerformanceAccountsHandler: NewManagementMyStatsAIPerformanceAccountsHandler(service),
		ManagementAPIAuthMiddleware:                   NewManagementAPIAuthMiddleware(readAuthenticator),
		ManagementAPIAuthTouchMiddleware:              NewManagementAPIAuthTouchMiddleware(touchAuthenticator),
	})

	paths := []string{
		"/__aisys__/api/stats/account-usage?systemAccountId=sys_target",
		"/__aisys__/api/stats/account-usage/trend?accountIds=acc_1,acc_2",
		"/__aisys__/api/my-stats/account-usage",
		"/__aisys__/api/my-stats/account-usage/trend?accountIds=acc_1",
		"/__aisys__/api/stats/ai-performance?startDate=2026-07-22&endDate=2026-07-22",
		"/__aisys__/api/stats/ai-performance/accounts?limit=50",
		"/__aisys__/api/my-stats/ai-performance",
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
func (s *managementStatsHTTPReaderStub) ReadManagementAccountUsageTrend(context.Context, port.ManagementAccountUsageTrendReadInput) (port.ManagementAccountUsageTrendReadResult, error) {
	return port.ManagementAccountUsageTrendReadResult{}, nil
}
func (s *managementStatsHTTPReaderStub) ReadManagementAIPerformance(context.Context, port.ManagementAIPerformanceReadInput) (port.ManagementAIPerformanceReadResult, error) {
	return port.ManagementAIPerformanceReadResult{}, nil
}
func (s *managementStatsHTTPReaderStub) ReadManagementAIPerformanceAccounts(context.Context, port.ManagementAIPerformanceAccountsReadInput) ([]port.ManagementStatsAccount, error) {
	return []port.ManagementStatsAccount{}, nil
}

var _ port.ManagementStatsReader = (*managementStatsHTTPReaderStub)(nil)

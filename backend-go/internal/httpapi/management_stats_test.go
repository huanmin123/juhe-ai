package httpapi

import (
	"context"
	"encoding/json"
	"errors"
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

func TestManagementStatsUsageWindowHandlerRequiresAdminAndReturnsDataEnvelope(t *testing.T) {
	store := &managementStatsTimezoneStoreStub{
		timezone: "Asia/Shanghai",
		found:    true,
	}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{
			SystemAccountID: "sys_admin",
			Username:        "admin",
			Role:            "admin",
			SessionID:       "sess_admin",
		},
	})(NewManagementStatsUsageWindowHandler(newManagementStatsUsageWindowTestService(store)))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/stats/usage-window", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if !store.called {
		t.Fatal("usage stats timezone store was not called")
	}
	var body struct {
		Data struct {
			Timezone  string `json:"timezone"`
			StartDate string `json:"startDate"`
			EndDate   string `json:"endDate"`
			Days      int    `json:"days"`
			MaxDays   int    `json:"maxDays"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.Timezone != "Asia/Shanghai" ||
		body.Data.StartDate != "2026-06-09" ||
		body.Data.EndDate != "2026-07-09" ||
		body.Data.Days != 31 ||
		body.Data.MaxDays != 31 {
		t.Fatalf("body = %+v", body)
	}
}

func TestManagementStatsUsageWindowHandlerRejectsOrdinaryUser(t *testing.T) {
	store := &managementStatsTimezoneStoreStub{
		timezone: "Asia/Shanghai",
		found:    true,
	}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{
			SystemAccountID: "sys_user",
			Username:        "user",
			Role:            "user",
			SessionID:       "sess_user",
		},
	})(NewManagementStatsUsageWindowHandler(newManagementStatsUsageWindowTestService(store)))

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/stats/usage-window", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", rec.Code, rec.Body.String())
	}
	if store.called {
		t.Fatal("service should not be called for ordinary user on admin usage window route")
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["message"] != "需要管理员权限" {
		t.Fatalf("body = %+v", body)
	}
}

func TestManagementMyStatsUsageWindowHandlerAllowsAuthenticatedSystemAccounts(t *testing.T) {
	for _, role := range []string{"user", "admin", "super_admin"} {
		t.Run(role, func(t *testing.T) {
			store := &managementStatsTimezoneStoreStub{
				timezone: "Asia/Shanghai",
				found:    true,
			}
			handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
				context: managementauth.Context{
					SystemAccountID: "sys_" + role,
					Username:        role,
					Role:            role,
					SessionID:       "sess_" + role,
				},
			})(NewManagementMyStatsUsageWindowHandler(newManagementStatsUsageWindowTestService(store)))

			req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-stats/usage-window?systemAccountId=sys_other", nil)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
			}
			if !store.called {
				t.Fatal("usage stats timezone store was not called")
			}
			var body struct {
				Data struct {
					Timezone string `json:"timezone"`
				} `json:"data"`
			}
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if body.Data.Timezone != "Asia/Shanghai" {
				t.Fatalf("body = %+v", body)
			}
		})
	}
}

func TestManagementStatsUsageWindowHandlersRedactServiceErrors(t *testing.T) {
	tests := []struct {
		name    string
		context managementauth.Context
		handler func(*managementstats.Service) http.Handler
		path    string
	}{
		{
			name:    "admin",
			context: managementauth.Context{SystemAccountID: "sys_admin", Role: "admin", SessionID: "sess_admin"},
			handler: NewManagementStatsUsageWindowHandler,
			path:    "/__aisys__/api/stats/usage-window",
		},
		{
			name:    "self",
			context: managementauth.Context{SystemAccountID: "sys_user", Role: "user", SessionID: "sess_user"},
			handler: NewManagementMyStatsUsageWindowHandler,
			path:    "/__aisys__/api/my-stats/usage-window",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := newManagementStatsUsageWindowTestService(&managementStatsTimezoneStoreStub{
				err: errors.New("postgres password leaked"),
			})
			handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
				context: test.context,
			})(test.handler(service))
			req := httptest.NewRequest(http.MethodGet, test.path, nil)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500; body = %s", rec.Code, rec.Body.String())
			}
			var body map[string]string
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if body["message"] != "服务器内部错误" {
				t.Fatalf("body = %+v", body)
			}
		})
	}
}

func TestRouterRegistersW6ManagementStatsUsageWindowWithReadAuth(t *testing.T) {
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
	service := newManagementStatsUsageWindowTestService(&managementStatsTimezoneStoreStub{
		timezone: "Asia/Shanghai",
		found:    true,
	})
	router := NewRouter(RouterOptions{
		Config:                              config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                              slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementStatsUsageWindowHandler:   NewManagementStatsUsageWindowHandler(service),
		ManagementMyStatsUsageWindowHandler: NewManagementMyStatsUsageWindowHandler(service),
		ManagementAPIAuthMiddleware:         NewManagementAPIAuthMiddleware(readAuthenticator),
		ManagementAPIAuthTouchMiddleware:    NewManagementAPIAuthTouchMiddleware(touchAuthenticator),
	})

	for _, path := range []string{
		"/__aisys__/api/stats/usage-window",
		"/__aisys__/api/my-stats/usage-window",
	} {
		t.Run(path, func(t *testing.T) {
			readAuthenticator.cookieHeader = ""
			touchAuthenticator.touchCookieHeader = ""
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("Cookie", "juhe_ai_session=session-token")
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Fatalf("Cache-Control = %q, want no-store", got)
			}
			if readAuthenticator.cookieHeader != "juhe_ai_session=session-token" ||
				touchAuthenticator.touchCookieHeader != "" {
				t.Fatalf(
					"auth headers read=%q touch=%q",
					readAuthenticator.cookieHeader,
					touchAuthenticator.touchCookieHeader,
				)
			}
		})
	}
}

func newManagementStatsUsageWindowTestService(store port.ManagementUsageStatsTimezoneReader) *managementstats.Service {
	return managementstats.NewServiceWithOptions(managementstats.ServiceOptions{
		Store: store,
		Now: func() time.Time {
			return time.Date(2026, 7, 8, 16, 30, 0, 0, time.UTC)
		},
	})
}

type managementStatsTimezoneStoreStub struct {
	called   bool
	timezone string
	found    bool
	err      error
}

func (s *managementStatsTimezoneStoreStub) GetManagementUsageStatsTimezone(context.Context) (string, bool, error) {
	s.called = true
	if s.err != nil {
		return "", false, s.err
	}
	return s.timezone, s.found, nil
}

var _ port.ManagementUsageStatsTimezoneReader = (*managementStatsTimezoneStoreStub)(nil)

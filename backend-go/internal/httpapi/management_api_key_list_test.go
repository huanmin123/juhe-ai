package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/managementapikeys"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/store/port"
)

func TestManagementAPIKeyListHandlersBuildAdminAndSelfScopes(t *testing.T) {
	t.Run("admin global and filters", func(t *testing.T) {
		service := &managementAPIKeyListServiceStub{
			result: managementapikeys.ListResult{Items: []managementapikeys.ListItem{}},
		}
		handler := newManagementAPIKeyListHandler(service, managementAPIKeyScopeAdmin)
		req := httptest.NewRequest(
			http.MethodGet,
			"/__aisys__/api/api-keys?systemAccountId=%20all%20&page=2&pageSize=25&keyword=%20Key%25%20&status=bad&routeStrategyId=%20route_1%20",
			nil,
		)
		req = requestWithManagementAuthContext(req, managementauth.Context{
			SystemAccountID: "sys_admin",
			Role:            "admin",
		})
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		input := service.input
		if input.ActorSystemAccountID != "sys_admin" ||
			input.ActorRole != "admin" ||
			input.SystemAccountID != "" ||
			input.SelfOnly ||
			input.Page != 2 ||
			input.PageSize != 25 ||
			!input.PageSizeProvided ||
			input.Keyword != "Key%" ||
			input.Status != "" ||
			input.RouteStrategyID != "route_1" {
			t.Fatalf("input = %+v", input)
		}
	})

	t.Run("self ignores forged owner", func(t *testing.T) {
		service := &managementAPIKeyListServiceStub{
			result: managementapikeys.ListResult{Items: []managementapikeys.ListItem{}},
		}
		handler := newManagementAPIKeyListHandler(service, managementAPIKeyScopeSelf)
		req := httptest.NewRequest(
			http.MethodGet,
			"/__aisys__/api/my-api-keys?systemAccountId=sys_forged&status=disabled",
			nil,
		)
		req = requestWithManagementAuthContext(req, managementauth.Context{
			SystemAccountID: "sys_current",
			Role:            "admin",
		})
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		if service.input.SystemAccountID != "sys_current" ||
			!service.input.SelfOnly ||
			service.input.Status != "disabled" {
			t.Fatalf("input = %+v", service.input)
		}
	})
}

func TestManagementAPIKeyListHandlerRejectsNonAdminOnAdminRoute(t *testing.T) {
	service := &managementAPIKeyListServiceStub{}
	handler := newManagementAPIKeyListHandler(service, managementAPIKeyScopeAdmin)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/api-keys", nil)
	req = requestWithManagementAuthContext(req, managementauth.Context{
		SystemAccountID: "sys_user",
		Role:            "user",
	})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden || service.calls != 0 {
		t.Fatalf("status=%d calls=%d body=%s", rec.Code, service.calls, rec.Body.String())
	}
}

func TestManagementAPIKeyListHandlerReturnsExactEnvelopeWithoutInternalFields(t *testing.T) {
	service := &managementAPIKeyListServiceStub{
		result: managementapikeys.ListResult{
			Items: []managementapikeys.ListItem{{
				ID:                  "key_1",
				SystemAccountID:     "sys_owner",
				SystemAccountName:   "所有者",
				Name:                "Key",
				KeyPrefix:           "sk-prefix",
				KeySuffix:           "suffix",
				Status:              "active",
				RouteStrategyID:     "route_1",
				RouteStrategyName:   "策略",
				RouteStrategyMode:   "normal",
				RouteStrategyStatus: "active",
				QuotaLimits:         port.ManagementRequestQuotaLimits{},
				Usage:               port.ManagementAccountUsageSummary{},
			}},
			Total:    1,
			Page:     1,
			PageSize: 50,
		},
	}
	handler := newManagementAPIKeyListHandler(service, managementAPIKeyScopeAdmin)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/api-keys", nil)
	req = requestWithManagementAuthContext(req, managementauth.Context{
		SystemAccountID: "sys_admin",
		Role:            "admin",
	})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	raw := rec.Body.String()
	for _, forbidden := range []string{
		`"key":`,
		"keyHash",
		"keySecretEncrypted",
		"key_hash",
		"key_secret_encrypted",
		"createdAt",
		"updatedAt",
	} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, raw)
		}
	}
	var envelope struct {
		Data managementapikeys.ListResult `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(envelope.Data.Items) != 1 {
		t.Fatalf("envelope = %+v", envelope)
	}
}

func TestRouterRegistersManagementAPIKeyListsAsNoStoreLimitedReadRoutes(t *testing.T) {
	service := &managementAPIKeyListServiceStub{
		result: managementapikeys.ListResult{Items: []managementapikeys.ListItem{}, Page: 1, PageSize: 50},
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
		ManagementAPIKeyListHandler:       newManagementAPIKeyListHandler(service, managementAPIKeyScopeAdmin),
		ManagementMyAPIKeyListHandler:     newManagementAPIKeyListHandler(service, managementAPIKeyScopeSelf),
	})

	for _, path := range []string{"/__aisys__/api/api-keys", "/__aisys__/api/my-api-keys"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Cookie", "juhe_ai_session=session-token")
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, body = %s", path, rec.Code, rec.Body.String())
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s Cache-Control = %q", path, rec.Header().Get("Cache-Control"))
		}
	}
	if readAuthenticator.cookieHeader == "" || touchAuthenticator.touchCookieHeader != "" {
		t.Fatalf("read auth=%q touch auth=%q", readAuthenticator.cookieHeader, touchAuthenticator.touchCookieHeader)
	}
	if ipLimiter.calls != 2 || userLimiter.calls != 2 || service.calls != 2 {
		t.Fatalf("IP calls=%d user calls=%d service calls=%d", ipLimiter.calls, userLimiter.calls, service.calls)
	}
}

func TestRouterDoesNotRegisterManagementAPIKeyListsWhenDisabled(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeData(w, http.StatusOK, managementapikeys.ListResult{})
	})
	router := NewRouter(RouterOptions{
		Config:                        config.Config{Host: "127.0.0.1", Port: 3000},
		Logger:                        slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementAPIKeyListHandler:   handler,
		ManagementMyAPIKeyListHandler: handler,
	})
	for _, path := range []string{"/__aisys__/api/api-keys", "/__aisys__/api/my-api-keys"} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404", path, rec.Code)
		}
	}
}

type managementAPIKeyListServiceStub struct {
	calls  int
	input  managementapikeys.ListInput
	result managementapikeys.ListResult
	err    error
}

func (s *managementAPIKeyListServiceStub) List(
	_ *http.Request,
	input managementapikeys.ListInput,
) (managementapikeys.ListResult, error) {
	s.calls++
	s.input = input
	return s.result, s.err
}

var _ managementAPIKeyListService = (*managementAPIKeyListServiceStub)(nil)

func requestWithManagementAPIKeyAuthContext(
	req *http.Request,
	authContext managementauth.Context,
) *http.Request {
	return req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, authContext))
}

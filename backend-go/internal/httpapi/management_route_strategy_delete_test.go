package httpapi

import (
	"context"
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
	"juhe-ai/backend-go/internal/modules/managementroutestrategies"
	"juhe-ai/backend-go/internal/store/port"
)

func TestManagementRouteStrategyDeleteHandlerScopesAdminAndSelf(t *testing.T) {
	tests := []struct {
		name                string
		scope               managementRouteStrategyOptionScope
		role                string
		path                string
		wantSystemAccountID string
		wantSelfOnly        bool
	}{
		{
			name:  "admin global when owner omitted",
			scope: managementRouteStrategyScopeAdmin,
			role:  "admin",
			path:  "/__aisys__/api/route-strategies/route_1",
		},
		{
			name:  "admin global when owner is all",
			scope: managementRouteStrategyScopeAdmin,
			role:  "super_admin",
			path:  "/__aisys__/api/route-strategies/route_1?systemAccountId=%20all%20",
		},
		{
			name:                "admin narrows explicit owner",
			scope:               managementRouteStrategyScopeAdmin,
			role:                "admin",
			path:                "/__aisys__/api/route-strategies/route_1?systemAccountId=%20sys_owner%20",
			wantSystemAccountID: "sys_owner",
		},
		{
			name:         "self ignores forged owner query",
			scope:        managementRouteStrategyScopeSelf,
			role:         "admin",
			path:         "/__aisys__/api/my-route-strategies/route_1?systemAccountId=&systemAccountId=sys_forged",
			wantSelfOnly: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &managementRouteStrategyDeleteServiceStub{
				result: managementroutestrategies.DeleteResult{
					Before: managementroutestrategies.DeleteBeforeSummary{
						ID:     "route_1",
						Name:   "生产策略",
						Mode:   "normal",
						Status: "active",
					},
					OwnerSystemAccountID: "sys_owner",
					Committed:            true,
				},
			}
			handler := newManagementRouteStrategyDeleteHandler(
				service,
				test.scope,
				managementOperationLogOptions{},
			)
			req := httptest.NewRequest(
				http.MethodDelete,
				test.path,
				strings.NewReader(`{"malformed":`),
			)
			req = requestWithManagementRouteStrategyDeleteID(req, " route/raw ")
			req = requestWithManagementAuthContext(req, managementauth.Context{
				SystemAccountID: "sys_actor",
				Role:            test.role,
			})
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
				t.Fatalf("status=%d body=%q, want empty 204", rec.Code, rec.Body.String())
			}
			if service.calls != 1 ||
				service.input.ActorSystemAccountID != "sys_actor" ||
				service.input.ActorRole != test.role ||
				service.input.SystemAccountID != test.wantSystemAccountID ||
				service.input.SelfOnly != test.wantSelfOnly ||
				service.input.RouteStrategyID != " route/raw " {
				t.Fatalf("service calls=%d input=%+v", service.calls, service.input)
			}
		})
	}
}

func TestManagementRouteStrategyDeleteHandlerRejectsInvalidBoundaryInput(t *testing.T) {
	tests := []struct {
		name        string
		scope       managementRouteStrategyOptionScope
		auth        managementauth.Context
		query       string
		id          string
		serviceErr  error
		wantStatus  int
		wantMessage string
		wantCalls   int
	}{
		{
			name:        "missing actor",
			scope:       managementRouteStrategyScopeSelf,
			wantStatus:  http.StatusUnauthorized,
			wantMessage: "未登录",
		},
		{
			name:        "admin route rejects ordinary user",
			scope:       managementRouteStrategyScopeAdmin,
			auth:        managementauth.Context{SystemAccountID: "sys_user", Role: "user"},
			wantStatus:  http.StatusForbidden,
			wantMessage: "需要管理员权限",
		},
		{
			name:        "admin rejects empty owner",
			scope:       managementRouteStrategyScopeAdmin,
			auth:        managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"},
			query:       "?systemAccountId=",
			wantStatus:  http.StatusBadRequest,
			wantMessage: "系统账号 ID 不能为空",
		},
		{
			name:        "admin rejects repeated owner",
			scope:       managementRouteStrategyScopeAdmin,
			auth:        managementauth.Context{SystemAccountID: "sys_admin", Role: "super_admin"},
			query:       "?systemAccountId=sys_a&systemAccountId=sys_b",
			wantStatus:  http.StatusBadRequest,
			wantMessage: "Expected string, received array",
		},
		{
			name:        "blank path reaches typed service validation",
			scope:       managementRouteStrategyScopeSelf,
			auth:        managementauth.Context{SystemAccountID: "sys_user", Role: "user"},
			id:          "   ",
			serviceErr:  &managementroutestrategies.ValidationError{Message: "策略路由删除作用域无效"},
			wantStatus:  http.StatusBadRequest,
			wantMessage: "策略路由删除作用域无效",
			wantCalls:   1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &managementRouteStrategyDeleteServiceStub{err: test.serviceErr}
			handler := newManagementRouteStrategyDeleteHandler(
				service,
				test.scope,
				managementOperationLogOptions{},
			)
			req := httptest.NewRequest(
				http.MethodDelete,
				"/__aisys__/api/route-strategies/route_1"+test.query,
				nil,
			)
			id := test.id
			if id == "" {
				id = "route_1"
			}
			req = requestWithManagementRouteStrategyDeleteID(req, id)
			if test.auth.SystemAccountID != "" {
				req = requestWithManagementAuthContext(req, test.auth)
			}
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != test.wantStatus ||
				!strings.Contains(rec.Body.String(), test.wantMessage) ||
				service.calls != test.wantCalls {
				t.Fatalf(
					"status=%d calls=%d body=%s",
					rec.Code,
					service.calls,
					rec.Body.String(),
				)
			}
		})
	}
}

func TestManagementRouteStrategyDeleteHandlerMapsServiceErrorsWithoutLogging(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantStatus  int
		wantMessage string
	}{
		{
			name:        "not found",
			err:         &managementroutestrategies.NotFoundError{RouteStrategyID: "route_1"},
			wantStatus:  http.StatusNotFound,
			wantMessage: "策略路由不存在",
		},
		{
			name: "default conflict",
			err: &managementroutestrategies.DeleteConflictError{
				Kind: managementroutestrategies.DeleteConflictDefault,
			},
			wantStatus:  http.StatusBadRequest,
			wantMessage: "默认策略路由不允许删除",
		},
		{
			name: "api key reference conflict",
			err: &managementroutestrategies.DeleteConflictError{
				Kind:        managementroutestrategies.DeleteConflictAPIKeysInUse,
				APIKeyCount: 3,
			},
			wantStatus:  http.StatusBadRequest,
			wantMessage: "策略路由已被 3 个 API Key 使用，请先解绑",
		},
		{
			name:        "internal",
			err:         errors.New("postgres password leaked"),
			wantStatus:  http.StatusInternalServerError,
			wantMessage: "服务器内部错误",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			queueStub := &operationLogQueueStub{}
			service := &managementRouteStrategyDeleteServiceStub{err: test.err}
			handler := newManagementRouteStrategyDeleteHandler(
				service,
				managementRouteStrategyScopeSelf,
				newManagementOperationLogOptions(ManagementOperationLogOptions{
					Client: queueStub,
				}),
			)
			req := httptest.NewRequest(
				http.MethodDelete,
				"/__aisys__/api/my-route-strategies/route_1",
				nil,
			)
			req = requestWithManagementRouteStrategyDeleteID(req, "route_1")
			req = requestWithManagementAuthContext(req, managementauth.Context{
				SystemAccountID: "sys_owner",
				Role:            "user",
			})
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != test.wantStatus ||
				!strings.Contains(rec.Body.String(), test.wantMessage) ||
				queueStub.calls != 0 {
				t.Fatalf(
					"status=%d logs=%d body=%s",
					rec.Code,
					queueStub.calls,
					rec.Body.String(),
				)
			}
		})
	}
}

func TestManagementRouteStrategyDeleteHandlerWritesExactOperationLogBestEffort(t *testing.T) {
	now := time.Date(2026, time.July, 12, 9, 30, 0, 0, time.UTC)
	queueStub := &operationLogQueueStub{err: errors.New("redis down")}
	service := &managementRouteStrategyDeleteServiceStub{
		result: managementroutestrategies.DeleteResult{
			Before: managementroutestrategies.DeleteBeforeSummary{
				ID:     "route_1",
				Name:   "生产策略",
				Mode:   "normal",
				Status: "active",
			},
			OwnerSystemAccountID: "sys_owner",
			Committed:            true,
		},
	}
	handler := newManagementRouteStrategyDeleteHandler(
		service,
		managementRouteStrategyScopeAdmin,
		newManagementOperationLogOptions(ManagementOperationLogOptions{
			Config:   config.Config{TrustProxy: "false"},
			Client:   queueStub,
			Now:      func() time.Time { return now },
			NewLogID: func() string { return "oplog_route_delete" },
		}),
	)
	req := httptest.NewRequest(
		http.MethodDelete,
		"/__aisys__/api/route-strategies/route_1?systemAccountId=sys_owner",
		nil,
	)
	req = requestWithManagementRouteStrategyDeleteID(req, "route_1")
	req = requestWithManagementAuthContext(req, managementauth.Context{
		SystemAccountID: "sys_admin",
		Username:        "admin",
		DisplayName:     "管理员",
		Role:            "super_admin",
	})
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "req_route_delete"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 || queueStub.calls != 1 {
		t.Fatalf("status=%d body=%q queue calls=%d", rec.Code, rec.Body.String(), queueStub.calls)
	}
	logInput, err := operationlogjob.DecodeWriteTaskPayload(queueStub.payload)
	if err != nil {
		t.Fatalf("decode operation log: %v", err)
	}
	if logInput.ID != "oplog_route_delete" ||
		logInput.TraceID != "req_route_delete" ||
		logInput.ActorSystemAccountID != "sys_admin" ||
		logInput.ActorUsername != "admin" ||
		logInput.ActorDisplayName != "管理员" ||
		logInput.ActorRole != "super_admin" ||
		logInput.OperationScopeSystemAccountID != "sys_owner" ||
		logInput.Mode != "admin" ||
		logInput.Module != "route_strategies" ||
		logInput.Action != "delete" ||
		logInput.OperationKey != "route_strategies.delete" ||
		logInput.ResourceType != "route_strategy" ||
		logInput.ResourceID != "route_1" ||
		logInput.ResourceName != "生产策略" ||
		logInput.Summary != "删除策略路由：生产策略" ||
		logInput.DetailLevel != "full" ||
		logInput.VisibilityScope != "targeted" ||
		logInput.Method != http.MethodDelete ||
		logInput.Path != "/__aisys__/api/route-strategies/route_1" ||
		logInput.StatusCode == nil ||
		*logInput.StatusCode != http.StatusNoContent ||
		len(logInput.Viewers) != 1 ||
		logInput.Viewers[0].SystemAccountID != "sys_owner" ||
		logInput.Viewers[0].VisibilityReason != "resource_owner" ||
		logInput.Viewers[0].DetailLevel != "full" ||
		!logInput.CreatedAt.Equal(now) {
		t.Fatalf("operation log=%+v", logInput)
	}
	if len(logInput.Changes) != 1 ||
		logInput.Changes[0].Field != "deleted" ||
		logInput.Changes[0].Label != "删除状态" ||
		logInput.Changes[0].Before != false ||
		logInput.Changes[0].After != true {
		t.Fatalf("operation log changes=%+v", logInput.Changes)
	}
}

func TestManagementMyRouteStrategyDeleteOperationLogUsesSelfModeAndOwnerViewer(t *testing.T) {
	queueStub := &operationLogQueueStub{}
	service := &managementRouteStrategyDeleteServiceStub{
		result: managementroutestrategies.DeleteResult{
			Before: managementroutestrategies.DeleteBeforeSummary{
				ID:   "route_self",
				Name: "个人策略",
				Mode: "normal",
			},
			OwnerSystemAccountID: "sys_actor",
			Committed:            true,
		},
	}
	handler := newManagementRouteStrategyDeleteHandler(
		service,
		managementRouteStrategyScopeSelf,
		newManagementOperationLogOptions(ManagementOperationLogOptions{
			Client: queueStub,
		}),
	)
	req := httptest.NewRequest(
		http.MethodDelete,
		"/__aisys__/api/my-route-strategies/route_self?systemAccountId=sys_forged",
		nil,
	)
	req = requestWithManagementRouteStrategyDeleteID(req, "route_self")
	req = requestWithManagementAuthContext(req, managementauth.Context{
		SystemAccountID: "sys_actor",
		Role:            "admin",
	})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	logInput, err := operationlogjob.DecodeWriteTaskPayload(queueStub.payload)
	if err != nil {
		t.Fatalf("decode operation log: %v", err)
	}
	if logInput.Mode != "self" ||
		logInput.OperationScopeSystemAccountID != "sys_actor" ||
		len(logInput.Viewers) != 1 ||
		logInput.Viewers[0].SystemAccountID != "sys_actor" {
		t.Fatalf("operation log=%+v", logInput)
	}
}

func TestRouterRegistersManagementRouteStrategyDeleteWithWriteBoundary(t *testing.T) {
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
	handlerCalls := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerCalls++
		w.WriteHeader(http.StatusNoContent)
	})
	router := NewRouter(RouterOptions{
		Config: config.Config{
			Host:                 "127.0.0.1",
			Port:                 3000,
			ManagementAPIEnabled: true,
		},
		Logger: slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		SystemAPIRateLimitReader: systemAPIRateLimitReaderStub{
			settings: port.SystemAPIRateLimitSettings{
				IPWritePerMinute:         180,
				IPWriteBurstPer10Seconds: 40,
				UserWritePerMinute:       120,
			},
		},
		SystemAPIIPRateLimiter:                 ipLimiter,
		SystemAPIAuthenticatedRateLimiter:      userLimiter,
		ManagementAPIAuthMiddleware:            NewManagementAPIAuthMiddleware(readAuthenticator),
		ManagementAPIAuthTouchMiddleware:       NewManagementAPIAuthTouchMiddleware(touchAuthenticator),
		ManagementRouteStrategyDeleteHandler:   handler,
		ManagementMyRouteStrategyDeleteHandler: handler,
	})

	for _, path := range []string{
		"/__aisys__/api/route-strategies/route_1",
		"/__aisys__/api/my-route-strategies/route_1",
	} {
		req := httptest.NewRequest(
			http.MethodDelete,
			path,
			strings.NewReader(`{"malformed":`),
		)
		req.Header.Set("Cookie", "juhe_ai_session=session-token")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusNoContent ||
			rec.Body.Len() != 0 ||
			rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf(
				"%s status=%d cache=%q body=%q",
				path,
				rec.Code,
				rec.Header().Get("Cache-Control"),
				rec.Body.String(),
			)
		}
	}
	if handlerCalls != 2 ||
		touchAuthenticator.touchCookieHeader != "juhe_ai_session=session-token" ||
		readAuthenticator.cookieHeader != "" ||
		ipLimiter.calls != 2 ||
		ipLimiter.settings.PerMinute != 180 ||
		ipLimiter.settings.BurstPer10Seconds != 40 ||
		userLimiter.calls != 2 ||
		userLimiter.limit != 120 {
		t.Fatalf(
			"handler=%d touch=%q read=%q ip=%d/%+v user=%d/%d",
			handlerCalls,
			touchAuthenticator.touchCookieHeader,
			readAuthenticator.cookieHeader,
			ipLimiter.calls,
			ipLimiter.settings,
			userLimiter.calls,
			userLimiter.limit,
		)
	}
}

func TestRouterManagementRouteStrategyDeleteAdminCheckRunsAfterWriteLimiters(t *testing.T) {
	ipLimiter := &publicSettingsRateLimiterStub{
		decision: SystemAPIRateLimitDecision{Allowed: true},
	}
	userLimiter := &systemAPIAuthenticatedRateLimiterStub{
		decision: SystemAPIRateLimitDecision{Allowed: true},
	}
	touchAuthenticator := &managementAPIAuthenticatorStub{
		context: managementauth.Context{
			SystemAccountID: "sys_user",
			Role:            "user",
			SessionID:       "sess_user",
		},
	}
	handlerCalls := 0
	router := NewRouter(RouterOptions{
		Config: config.Config{
			Host:                 "127.0.0.1",
			Port:                 3000,
			ManagementAPIEnabled: true,
		},
		SystemAPIRateLimitReader: systemAPIRateLimitReaderStub{
			settings: port.SystemAPIRateLimitSettings{
				IPWritePerMinute:         180,
				IPWriteBurstPer10Seconds: 40,
				UserWritePerMinute:       120,
			},
		},
		SystemAPIIPRateLimiter:            ipLimiter,
		SystemAPIAuthenticatedRateLimiter: userLimiter,
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(
			&managementAPIAuthenticatorStub{},
		),
		ManagementAPIAuthTouchMiddleware: NewManagementAPIAuthTouchMiddleware(
			touchAuthenticator,
		),
		ManagementRouteStrategyDeleteHandler: http.HandlerFunc(
			func(http.ResponseWriter, *http.Request) {
				handlerCalls++
			},
		),
	})
	req := httptest.NewRequest(
		http.MethodDelete,
		"/__aisys__/api/route-strategies/route_1",
		nil,
	)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden ||
		!strings.Contains(rec.Body.String(), "需要管理员权限") ||
		ipLimiter.calls != 1 ||
		touchAuthenticator.touchCookieHeader != "juhe_ai_session=session-token" ||
		userLimiter.calls != 1 ||
		handlerCalls != 0 {
		t.Fatalf(
			"status=%d ip=%d touch=%q user=%d handler=%d body=%s",
			rec.Code,
			ipLimiter.calls,
			touchAuthenticator.touchCookieHeader,
			userLimiter.calls,
			handlerCalls,
			rec.Body.String(),
		)
	}
}

func TestManagementRouteStrategyDeleteRouteClassificationAndDisabledRegistration(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	opts := RouterOptions{ManagementRouteStrategyDeleteHandler: handler}
	if !managementBusinessRoutesConfigured(opts) || !managementWriteRoutesConfigured(opts) {
		t.Fatal("route strategy delete was not classified as management business/write route")
	}

	router := NewRouter(RouterOptions{
		Config:                                 config.Config{Host: "127.0.0.1", Port: 3000},
		ManagementRouteStrategyDeleteHandler:   handler,
		ManagementMyRouteStrategyDeleteHandler: handler,
		ManagementAPIAuthMiddleware:            nil,
		ManagementAPIAuthTouchMiddleware:       nil,
	})
	for _, path := range []string{
		"/__aisys__/api/route-strategies/route_1",
		"/__aisys__/api/my-route-strategies/route_1",
	} {
		req := httptest.NewRequest(http.MethodDelete, path, nil)
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}
}

type managementRouteStrategyDeleteServiceStub struct {
	input  managementroutestrategies.DeleteInput
	result managementroutestrategies.DeleteResult
	err    error
	calls  int
}

func (s *managementRouteStrategyDeleteServiceStub) Delete(
	_ *http.Request,
	input managementroutestrategies.DeleteInput,
) (managementroutestrategies.DeleteResult, error) {
	s.calls++
	s.input = input
	return s.result, s.err
}

func requestWithManagementRouteStrategyDeleteID(
	req *http.Request,
	id string,
) *http.Request {
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("id", id)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext))
}

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

	"juhe-ai/backend-go/internal/config"
	operationlogjob "juhe-ai/backend-go/internal/jobs/operationlog"
	"juhe-ai/backend-go/internal/modules/managementapikeys"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/store/port"
)

func TestManagementAPIKeyDeleteHandlerScopesAndReturnsEmptyNoStoreResponse(t *testing.T) {
	tests := []struct {
		name         string
		scope        managementAPIKeyScope
		role         string
		path         string
		wantTarget   string
		wantSelfOnly bool
	}{
		{
			name:       "admin global omitted",
			scope:      managementAPIKeyScopeAdmin,
			role:       "admin",
			path:       "/__aisys__/api/api-keys/key_1",
			wantTarget: "",
		},
		{
			name:       "admin global all",
			scope:      managementAPIKeyScopeAdmin,
			role:       "super_admin",
			path:       "/__aisys__/api/api-keys/key_1?systemAccountId=all",
			wantTarget: "",
		},
		{
			name:       "admin explicit owner",
			scope:      managementAPIKeyScopeAdmin,
			role:       "admin",
			path:       "/__aisys__/api/api-keys/key_1?systemAccountId=%20sys_target%20",
			wantTarget: "sys_target",
		},
		{
			name:         "self ignores forged query",
			scope:        managementAPIKeyScopeSelf,
			role:         "user",
			path:         "/__aisys__/api/my-api-keys/key_1?systemAccountId=&systemAccountId=sys_forged",
			wantTarget:   "sys_actor",
			wantSelfOnly: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &managementAPIKeyDeleteServiceStub{
				result: managementapikeys.DeleteResult{
					APIKeyID:             "key_1",
					Name:                 "生产 Key",
					OwnerSystemAccountID: "sys_owner",
					Committed:            true,
				},
			}
			handler := newManagementAPIKeyDeleteHandler(
				service,
				test.scope,
				managementOperationLogOptions{},
			)
			req := httptest.NewRequest(http.MethodDelete, test.path, strings.NewReader(`{"malformed":`))
			req = requestWithManagementAPIKeyID(req, " key/raw ")
			req = requestWithManagementAPIKeyAuthContext(req, managementauth.Context{
				SystemAccountID: "sys_actor",
				Role:            test.role,
			})
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
				t.Fatalf("status=%d body=%q, want empty 204", rec.Code, rec.Body.String())
			}
			if rec.Header().Get("Cache-Control") != "no-store" ||
				rec.Header().Get("Pragma") != "no-cache" {
				t.Fatalf("cache headers=%v", rec.Header())
			}
			if service.calls != 1 ||
				service.input.ActorSystemAccountID != "sys_actor" ||
				service.input.ActorRole != test.role ||
				service.input.SystemAccountID != test.wantTarget ||
				service.input.SelfOnly != test.wantSelfOnly ||
				service.input.APIKeyID != " key/raw " {
				t.Fatalf("service calls=%d input=%+v", service.calls, service.input)
			}
		})
	}
}

func TestManagementAPIKeyDeleteHandlerRejectsInvalidAdminScopeAndRole(t *testing.T) {
	tests := []struct {
		name        string
		role        string
		query       string
		wantStatus  int
		wantMessage string
	}{
		{
			name:        "ordinary user",
			role:        "user",
			wantStatus:  http.StatusForbidden,
			wantMessage: "需要管理员权限",
		},
		{
			name:        "empty owner",
			role:        "admin",
			query:       "?systemAccountId=",
			wantStatus:  http.StatusBadRequest,
			wantMessage: "系统账号 ID 不能为空",
		},
		{
			name:        "repeated owner",
			role:        "super_admin",
			query:       "?systemAccountId=sys_a&systemAccountId=sys_b",
			wantStatus:  http.StatusBadRequest,
			wantMessage: "Expected string, received array",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &managementAPIKeyDeleteServiceStub{}
			handler := newManagementAPIKeyDeleteHandler(
				service,
				managementAPIKeyScopeAdmin,
				managementOperationLogOptions{},
			)
			req := httptest.NewRequest(
				http.MethodDelete,
				"/__aisys__/api/api-keys/key_1"+test.query,
				nil,
			)
			req = requestWithManagementAPIKeyID(req, "key_1")
			req = requestWithManagementAPIKeyAuthContext(req, managementauth.Context{
				SystemAccountID: "sys_actor",
				Role:            test.role,
			})
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != test.wantStatus ||
				!strings.Contains(rec.Body.String(), test.wantMessage) ||
				service.calls != 0 {
				t.Fatalf("status=%d calls=%d body=%s", rec.Code, service.calls, rec.Body.String())
			}
			if rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("cache header=%q", rec.Header().Get("Cache-Control"))
			}
		})
	}
}

func TestManagementAPIKeyDeleteHandlerMapsErrorsAndLogsOnlyCommittedResults(t *testing.T) {
	tests := []struct {
		name       string
		result     managementapikeys.DeleteResult
		err        error
		wantStatus int
		wantText   string
		wantLogs   int
	}{
		{
			name:       "not found or wrong owner",
			err:        managementapikeys.ErrAPIKeyNotFound,
			wantStatus: http.StatusNotFound,
			wantText:   "API Key 不存在",
		},
		{
			name:       "default",
			err:        managementapikeys.ErrAPIKeyDefaultDelete,
			wantStatus: http.StatusConflict,
			wantText:   "默认 API Key 不允许删除",
		},
		{
			name:       "invalid",
			err:        managementapikeys.ErrAPIKeyDeleteInvalid,
			wantStatus: http.StatusBadRequest,
			wantText:   "API Key 参数无效",
		},
		{
			name:       "internal",
			err:        errors.New("postgres password leaked"),
			wantStatus: http.StatusInternalServerError,
			wantText:   "服务器内部错误",
		},
		{
			name: "committed validation failure",
			result: managementapikeys.DeleteResult{
				APIKeyID:             "key_1",
				Name:                 "生产 Key",
				OwnerSystemAccountID: "sys_owner",
				Committed:            true,
			},
			err:        managementapikeys.ErrAPIKeyDeleteValidationCacheInvalidation,
			wantStatus: http.StatusInternalServerError,
			wantText:   "服务器内部错误",
			wantLogs:   1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			queueStub := &operationLogQueueStub{}
			service := &managementAPIKeyDeleteServiceStub{result: test.result, err: test.err}
			handler := newManagementAPIKeyDeleteHandler(
				service,
				managementAPIKeyScopeSelf,
				newManagementOperationLogOptions(ManagementOperationLogOptions{
					Client:   queueStub,
					NewLogID: func() string { return "oplog_delete_error" },
				}),
			)
			req := httptest.NewRequest(http.MethodDelete, "/__aisys__/api/my-api-keys/key_1", nil)
			req = requestWithManagementAPIKeyID(req, "key_1")
			req = requestWithManagementAPIKeyAuthContext(req, managementauth.Context{
				SystemAccountID: "sys_owner",
				Role:            "user",
			})
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != test.wantStatus ||
				!strings.Contains(rec.Body.String(), test.wantText) ||
				queueStub.calls != test.wantLogs {
				t.Fatalf(
					"status=%d logs=%d body=%s",
					rec.Code,
					queueStub.calls,
					rec.Body.String(),
				)
			}
			if test.wantLogs == 1 {
				logInput, err := operationlogjob.DecodeWriteTaskPayload(queueStub.payload)
				if err != nil {
					t.Fatalf("decode operation log: %v", err)
				}
				if logInput.StatusCode == nil ||
					*logInput.StatusCode != http.StatusInternalServerError ||
					logInput.ResourceID != "key_1" ||
					logInput.ResourceName != "生产 Key" {
					t.Fatalf("operation log=%+v", logInput)
				}
			}
		})
	}
}

func TestManagementAPIKeyDeleteHandlerWritesExactOperationLogBestEffort(t *testing.T) {
	now := time.Date(2026, time.July, 12, 8, 30, 0, 0, time.UTC)
	queueStub := &operationLogQueueStub{err: errors.New("redis down")}
	service := &managementAPIKeyDeleteServiceStub{
		result: managementapikeys.DeleteResult{
			APIKeyID:             "key_1",
			Name:                 "生产 Key",
			OwnerSystemAccountID: "sys_owner",
			Committed:            true,
		},
	}
	handler := newManagementAPIKeyDeleteHandler(
		service,
		managementAPIKeyScopeAdmin,
		newManagementOperationLogOptions(ManagementOperationLogOptions{
			Config:   config.Config{TrustProxy: "false"},
			Client:   queueStub,
			Now:      func() time.Time { return now },
			NewLogID: func() string { return "oplog_delete" },
		}),
	)
	req := httptest.NewRequest(
		http.MethodDelete,
		"/__aisys__/api/api-keys/key_1?systemAccountId=sys_owner",
		nil,
	)
	req = requestWithManagementAPIKeyID(req, "key_1")
	req = requestWithManagementAPIKeyAuthContext(req, managementauth.Context{
		SystemAccountID: "sys_admin",
		Username:        "admin",
		DisplayName:     "管理员",
		Role:            "super_admin",
	})
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "req_delete"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 || queueStub.calls != 1 {
		t.Fatalf("status=%d body=%q queue calls=%d", rec.Code, rec.Body.String(), queueStub.calls)
	}
	logInput, err := operationlogjob.DecodeWriteTaskPayload(queueStub.payload)
	if err != nil {
		t.Fatalf("decode operation log: %v", err)
	}
	if logInput.ID != "oplog_delete" ||
		logInput.TraceID != "req_delete" ||
		logInput.ActorSystemAccountID != "sys_admin" ||
		logInput.ActorRole != "super_admin" ||
		logInput.OperationScopeSystemAccountID != "sys_owner" ||
		logInput.Mode != "admin" ||
		logInput.Module != "api_keys" ||
		logInput.Action != "delete" ||
		logInput.OperationKey != "api_keys.delete" ||
		logInput.ResourceType != "api_key" ||
		logInput.ResourceID != "key_1" ||
		logInput.ResourceName != "生产 Key" ||
		logInput.Summary != "删除 API Key：生产 Key" ||
		logInput.VisibilityScope != "targeted" ||
		logInput.StatusCode == nil ||
		*logInput.StatusCode != http.StatusNoContent ||
		len(logInput.Viewers) != 1 ||
		logInput.Viewers[0].SystemAccountID != "sys_owner" ||
		logInput.Viewers[0].VisibilityReason != "resource_owner" ||
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

func TestManagementAPIKeyDeleteHandlerAllowsSecondDeleteToReachNaturalNotFound(t *testing.T) {
	queueStub := &operationLogQueueStub{}
	service := &managementAPIKeyDeleteServiceStub{
		results: []managementapikeys.DeleteResult{{
			APIKeyID:             "key_1",
			Name:                 "生产 Key",
			OwnerSystemAccountID: "sys_owner",
			Committed:            true,
		}},
		errs: []error{nil, managementapikeys.ErrAPIKeyNotFound},
	}
	handler := newManagementAPIKeyDeleteHandler(
		service,
		managementAPIKeyScopeSelf,
		newManagementOperationLogOptions(ManagementOperationLogOptions{Client: queueStub}),
	)

	for attempt, wantStatus := range []int{http.StatusNoContent, http.StatusNotFound} {
		req := httptest.NewRequest(http.MethodDelete, "/__aisys__/api/my-api-keys/key_1", nil)
		req = requestWithManagementAPIKeyID(req, "key_1")
		req = requestWithManagementAPIKeyAuthContext(req, managementauth.Context{
			SystemAccountID: "sys_owner",
			Role:            "user",
		})
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != wantStatus {
			t.Fatalf("attempt %d status=%d body=%s", attempt+1, rec.Code, rec.Body.String())
		}
	}
	if service.calls != 2 || queueStub.calls != 1 {
		t.Fatalf("service calls=%d logs=%d, want natural second call and one committed log", service.calls, queueStub.calls)
	}
}

func TestRouterRegistersManagementAPIKeyDeleteWithWriteAuthAndRateLimits(t *testing.T) {
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
		SystemAPIIPRateLimiter:            ipLimiter,
		SystemAPIAuthenticatedRateLimiter: userLimiter,
		ManagementAPIAuthMiddleware:       NewManagementAPIAuthMiddleware(readAuthenticator),
		ManagementAPIAuthTouchMiddleware:  NewManagementAPIAuthTouchMiddleware(touchAuthenticator),
		ManagementAPIKeyDeleteHandler:     handler,
		ManagementMyAPIKeyDeleteHandler:   handler,
	})

	for _, path := range []string{
		"/__aisys__/api/api-keys/key_1",
		"/__aisys__/api/my-api-keys/key_1",
	} {
		for attempt := 0; attempt < 2; attempt++ {
			req := httptest.NewRequest(http.MethodDelete, path, strings.NewReader(`{"malformed":`))
			req.Header.Set("Cookie", "juhe_ai_session=session-token")
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
				t.Fatalf(
					"%s attempt %d status=%d body=%q",
					path,
					attempt+1,
					rec.Code,
					rec.Body.String(),
				)
			}
		}
	}
	if handlerCalls != 4 ||
		touchAuthenticator.touchCookieHeader != "juhe_ai_session=session-token" ||
		readAuthenticator.cookieHeader != "" ||
		ipLimiter.calls != 4 ||
		ipLimiter.settings.PerMinute != 180 ||
		ipLimiter.settings.BurstPer10Seconds != 40 ||
		userLimiter.calls != 4 ||
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

func TestRouterManagementAPIKeyDeleteAdminRouteRejectsOrdinaryUser(t *testing.T) {
	handlerCalls := 0
	router := NewRouter(RouterOptions{
		Config: config.Config{
			Host:                 "127.0.0.1",
			Port:                 3000,
			ManagementAPIEnabled: true,
		},
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{}),
		ManagementAPIAuthTouchMiddleware: NewManagementAPIAuthTouchMiddleware(&managementAPIAuthenticatorStub{
			context: managementauth.Context{
				SystemAccountID: "sys_user",
				Role:            "user",
				SessionID:       "sess_user",
			},
		}),
		ManagementAPIKeyDeleteHandler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			handlerCalls++
		}),
	})
	req := httptest.NewRequest(http.MethodDelete, "/__aisys__/api/api-keys/key_1", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden ||
		!strings.Contains(rec.Body.String(), "需要管理员权限") ||
		handlerCalls != 0 {
		t.Fatalf("status=%d calls=%d body=%s", rec.Code, handlerCalls, rec.Body.String())
	}
}

func TestManagementAPIKeyDeleteRouteClassificationAndDisabledRegistration(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	opts := RouterOptions{ManagementAPIKeyDeleteHandler: handler}
	if !managementBusinessRoutesConfigured(opts) || !managementWriteRoutesConfigured(opts) {
		t.Fatal("API Key delete was not classified as management business/write route")
	}
	router := NewRouter(RouterOptions{
		Config:                           config.Config{Host: "127.0.0.1", Port: 3000},
		ManagementAPIKeyDeleteHandler:    handler,
		ManagementMyAPIKeyDeleteHandler:  handler,
		ManagementAPIAuthMiddleware:      nil,
		ManagementAPIAuthTouchMiddleware: nil,
	})
	for _, path := range []string{
		"/__aisys__/api/api-keys/key_1",
		"/__aisys__/api/my-api-keys/key_1",
	} {
		req := httptest.NewRequest(http.MethodDelete, path, nil)
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}
}

type managementAPIKeyDeleteServiceStub struct {
	input   managementapikeys.DeleteInput
	result  managementapikeys.DeleteResult
	results []managementapikeys.DeleteResult
	err     error
	errs    []error
	calls   int
}

func (s *managementAPIKeyDeleteServiceStub) Delete(
	_ *http.Request,
	input managementapikeys.DeleteInput,
) (managementapikeys.DeleteResult, error) {
	s.input = input
	index := s.calls
	s.calls++
	result := s.result
	if index < len(s.results) {
		result = s.results[index]
	}
	err := s.err
	if index < len(s.errs) {
		err = s.errs[index]
	}
	return result, err
}

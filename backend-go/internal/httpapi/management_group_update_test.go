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
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementgroups"
	"juhe-ai/backend-go/internal/store/port"
)

func TestManagementGroupUpdateHandlerBuildsAdminScopeAndOperationLog(t *testing.T) {
	now := time.Date(2026, time.July, 11, 10, 0, 0, 0, time.UTC)
	queueStub := &operationLogQueueStub{}
	beforeDescription := "旧说明"
	afterDescription := "新说明"
	service := &managementGroupUpdateServiceStub{
		result: managementgroups.UpdateResult{
			Before: port.ManagementGroupMutationSummary{
				ID:           "grp_1",
				Name:         "旧分组",
				ProviderCode: "openai",
				Description:  &beforeDescription,
				Enabled:      true,
				GroupType:    "personal",
			},
			Group: managementgroups.DetailResult{
				ID:                   "grp_1",
				SystemAccountID:      "sys_target",
				OwnerSystemAccountID: "sys_target",
				Name:                 "新分组",
				ProviderCode:         "gpt",
				Description:          &afterDescription,
				Enabled:              false,
				GroupType:            "high_concurrency",
				SchedulingPolicy:     &managementgroups.SchedulingPolicy{Mode: "balanced_fast", DefaultSoftConcurrency: 8},
				AccountIDs:           []string{},
			},
			AccessType:               "owner",
			OwnerSystemAccountID:     "sys_target",
			EffectiveSystemAccountID: "sys_target",
		},
	}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{
			SystemAccountID: "sys_admin",
			Username:        "admin",
			DisplayName:     "管理员",
			Role:            "admin",
			SessionID:       "sess_admin",
		},
	})(newManagementGroupUpdateHandler(
		service,
		managementGroupScopeAdmin,
		newManagementOperationLogOptions(ManagementOperationLogOptions{
			Config:   config.Config{TrustProxy: "false"},
			Client:   queueStub,
			Now:      func() time.Time { return now },
			NewLogID: func() string { return "oplog_group_update" },
		}),
	))
	req := httptest.NewRequest(
		http.MethodPatch,
		"/__aisys__/api/groups/grp_1?systemAccountId=sys_target",
		strings.NewReader(`{
			"name":" 新分组 ",
			"providerCode":" gpt ",
			"description":" 新说明 ",
			"enabled":false,
			"groupType":"high_concurrency",
			"schedulingPolicy":{"defaultSoftConcurrency":8}
		}`),
	)
	req = requestWithManagementGroupDetailID(req, "grp_1")
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "req_group_update"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if service.calls != 1 ||
		service.input.SystemAccountID != "sys_target" ||
		service.input.GroupID != "grp_1" ||
		!service.input.HasName ||
		service.input.Name != "新分组" ||
		!service.input.HasProviderCode ||
		service.input.ProviderCode != "gpt" ||
		service.input.Description == nil ||
		*service.input.Description != "新说明" ||
		service.input.SchedulingPolicy == nil ||
		service.input.SchedulingPolicy.DefaultSoftConcurrency == nil ||
		*service.input.SchedulingPolicy.DefaultSoftConcurrency != 8 {
		t.Fatalf("update input = %+v calls=%d", service.input, service.calls)
	}
	logInput, err := operationlogjob.DecodeWriteTaskPayload(queueStub.payload)
	if err != nil {
		t.Fatalf("decode operation log: %v", err)
	}
	if logInput.ID != "oplog_group_update" ||
		logInput.TraceID != "req_group_update" ||
		logInput.ActorRole != "admin" ||
		logInput.OperationScopeSystemAccountID != "sys_target" ||
		logInput.Mode != "admin" ||
		logInput.OperationKey != "groups.update" ||
		logInput.ResourceID != "grp_1" ||
		logInput.Summary != "更新分组：新分组" ||
		len(logInput.Viewers) != 1 ||
		logInput.Viewers[0].VisibilityReason != "resource_owner" ||
		!logInput.CreatedAt.Equal(now) {
		t.Fatalf("operation log = %+v", logInput)
	}
}

func TestManagementMyGroupUpdateHandlerKeepsRealActorRoleAndIgnoresScopeQuery(t *testing.T) {
	queueStub := &operationLogQueueStub{}
	service := &managementGroupUpdateServiceStub{
		result: managementgroups.UpdateResult{
			Before: port.ManagementGroupMutationSummary{
				ID:        "grp_authorized",
				Name:      "授权分组",
				Enabled:   true,
				GroupType: "personal",
			},
			Group: managementgroups.DetailResult{
				ID:                   "grp_authorized",
				OwnerSystemAccountID: "sys_owner",
				Name:                 "授权分组",
				Enabled:              false,
				GroupType:            "personal",
				AccessType:           "authorized",
				AccountIDs:           []string{},
			},
			AccessType:               "authorized",
			OwnerSystemAccountID:     "sys_owner",
			EffectiveSystemAccountID: "sys_admin",
			GroupAuthorizationID:     "auth_1",
		},
	}
	handler := NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
		context: managementauth.Context{
			SystemAccountID: "sys_admin",
			Username:        "admin",
			Role:            "admin",
			SessionID:       "sess_admin",
		},
	})(newManagementGroupUpdateHandler(
		service,
		managementGroupScopeSelf,
		newManagementOperationLogOptions(ManagementOperationLogOptions{
			Config:   config.Config{TrustProxy: "false"},
			Client:   queueStub,
			NewLogID: func() string { return "oplog_my_group_update" },
		}),
	))
	req := httptest.NewRequest(
		http.MethodPatch,
		"/__aisys__/api/my-groups/grp_authorized?systemAccountId=&systemAccountId=sys_other",
		strings.NewReader(`{"enabled":false}`),
	)
	req = requestWithManagementGroupDetailID(req, "grp_authorized")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK ||
		service.calls != 1 ||
		!service.input.SelfOnly ||
		service.input.SystemAccountID != "" ||
		service.input.ActorRole != "admin" {
		t.Fatalf("status=%d input=%+v calls=%d body=%s", rec.Code, service.input, service.calls, rec.Body.String())
	}
	logInput, err := operationlogjob.DecodeWriteTaskPayload(queueStub.payload)
	if err != nil {
		t.Fatalf("decode operation log: %v", err)
	}
	if logInput.ActorRole != "admin" ||
		logInput.Mode != "self" ||
		logInput.OperationScopeSystemAccountID != "sys_admin" ||
		logInput.Summary != "更新授权分组使用配置：授权分组" ||
		logInput.Viewers[0].VisibilityReason != "authorization_grantee" {
		t.Fatalf("operation log = %+v", logInput)
	}
}

func TestManagementGroupUpdateHandlerRejectsInvalidBodies(t *testing.T) {
	tests := []string{
		``,
		`{"name":`,
		`{}`,
		`[]`,
		`"value"`,
		`{"description":null}`,
		`{"enabled":"true"}`,
		`{"groupType":" high_concurrency "}`,
		`{"schedulingPolicy":{"unknown":1}}`,
		`{"unknown":true}`,
		`{"name":"ok"} {"enabled":true}`,
	}
	for _, body := range tests {
		t.Run(body, func(t *testing.T) {
			service := &managementGroupUpdateServiceStub{}
			handler := newManagementGroupUpdateHandler(service, managementGroupScopeSelf, managementOperationLogOptions{})
			req := httptest.NewRequest(http.MethodPatch, "/__aisys__/api/my-groups/grp_1", strings.NewReader(body))
			req = requestWithManagementGroupDetailID(req, "grp_1")
			req = requestWithManagementAuthContext(req, managementauth.Context{
				SystemAccountID: "sys_user",
				Role:            "user",
			})
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest ||
				service.calls != 0 ||
				!strings.Contains(rec.Body.String(), "分组参数无效") {
				t.Fatalf("status=%d calls=%d body=%s", rec.Code, service.calls, rec.Body.String())
			}
		})
	}
}

func TestManagementGroupUpdateHandlerDoesNotLogFailedMutation(t *testing.T) {
	queueStub := &operationLogQueueStub{}
	service := &managementGroupUpdateServiceStub{err: managementgroups.ErrGroupNotFound}
	handler := newManagementGroupUpdateHandler(
		service,
		managementGroupScopeSelf,
		newManagementOperationLogOptions(ManagementOperationLogOptions{
			Client: queueStub,
		}),
	)
	req := httptest.NewRequest(
		http.MethodPatch,
		"/__aisys__/api/my-groups/grp_missing",
		strings.NewReader(`{"enabled":false}`),
	)
	req = requestWithManagementGroupDetailID(req, "grp_missing")
	req = requestWithManagementAuthContext(req, managementauth.Context{
		SystemAccountID: "sys_user",
		Role:            "user",
	})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound || queueStub.calls != 0 {
		t.Fatalf("status=%d queue calls=%d body=%s", rec.Code, queueStub.calls, rec.Body.String())
	}
}

func TestManagementGroupUpdateHandlerMapsUserFacingErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantText   string
	}{
		{name: "not found", err: managementgroups.ErrGroupNotFound, wantStatus: http.StatusNotFound, wantText: "分组不存在"},
		{name: "default", err: managementgroups.ErrGroupDefaultReadonly, wantStatus: http.StatusBadRequest, wantText: "默认分组不允许修改"},
		{name: "provider accounts", err: managementgroups.ErrGroupProviderHasAccounts, wantStatus: http.StatusBadRequest, wantText: "已有账户的分组不允许修改供应商"},
		{name: "provider missing", err: &managementgroups.ProviderNotFoundError{Code: "missing"}, wantStatus: http.StatusBadRequest, wantText: "不支持的供应商：missing"},
		{name: "duplicate", err: &managementgroups.NameExistsError{Name: "重复"}, wantStatus: http.StatusConflict, wantText: "同一供应商下分组名称已存在：重复"},
		{name: "rejected", err: &managementgroups.UpdateRejectedError{Message: "授权分组使用配置包含未知字段：name"}, wantStatus: http.StatusBadRequest, wantText: "授权分组使用配置包含未知字段：name"},
		{name: "internal", err: errors.New("database down"), wantStatus: http.StatusInternalServerError, wantText: "服务器内部错误"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementGroupUpdateServiceStub{err: tt.err}
			handler := newManagementGroupUpdateHandler(service, managementGroupScopeSelf, managementOperationLogOptions{})
			req := httptest.NewRequest(http.MethodPatch, "/__aisys__/api/my-groups/grp_1", strings.NewReader(`{"enabled":false}`))
			req = requestWithManagementGroupDetailID(req, "grp_1")
			req = requestWithManagementAuthContext(req, managementauth.Context{
				SystemAccountID: "sys_user",
				Role:            "user",
			})
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus || !strings.Contains(rec.Body.String(), tt.wantText) {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestRouterRegistersW5ManagementGroupUpdateRoutesAndTransportBoundary(t *testing.T) {
	authenticator := &managementAPIAuthenticatorStub{
		context: managementauth.Context{
			SystemAccountID: "sys_admin",
			Username:        "admin",
			Role:            "admin",
			SessionID:       "sess_admin",
		},
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeData(w, http.StatusOK, map[string]string{"id": "grp_1"})
	})
	router := NewRouter(RouterOptions{
		Config:                           config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                           slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementGroupUpdateHandler:     handler,
		ManagementMyGroupUpdateHandler:   handler,
		ManagementAPIAuthMiddleware:      NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware: NewManagementAPIAuthTouchMiddleware(authenticator),
	})

	for _, path := range []string{"/__aisys__/api/groups/grp_1", "/__aisys__/api/my-groups/grp_1"} {
		req := httptest.NewRequest(http.MethodPatch, path, strings.NewReader(`{"enabled":false}`))
		req.Header.Set("Cookie", "juhe_ai_session=session-token")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}

	req := httptest.NewRequest(
		http.MethodPatch,
		"/__aisys__/api/groups/grp_1",
		strings.NewReader(`"`+strings.Repeat("x", managementGroupCreateMaxBodyBytes)+`"`),
	)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge ||
		!strings.Contains(rec.Body.String(), "请求体过大") {
		t.Fatalf("oversized status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRouterW5ManagementGroupUpdateUsesWriteAuthAndRateLimits(t *testing.T) {
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
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeData(w, http.StatusOK, map[string]string{"id": "grp_1"})
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
		ManagementGroupUpdateHandler:      handler,
		ManagementMyGroupUpdateHandler:    handler,
	})

	for _, path := range []string{"/__aisys__/api/groups/grp_1", "/__aisys__/api/my-groups/grp_1"} {
		req := httptest.NewRequest(http.MethodPatch, path, strings.NewReader(`{"enabled":false}`))
		req.Header.Set("Cookie", "juhe_ai_session=session-token")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf(
				"%s status=%d cache=%q body=%s",
				path,
				rec.Code,
				rec.Header().Get("Cache-Control"),
				rec.Body.String(),
			)
		}
	}
	if touchAuthenticator.touchCookieHeader != "juhe_ai_session=session-token" {
		t.Fatalf("touch auth cookie = %q", touchAuthenticator.touchCookieHeader)
	}
	if readAuthenticator.cookieHeader != "" {
		t.Fatalf("read auth cookie = %q, want empty for PATCH", readAuthenticator.cookieHeader)
	}
	if ipLimiter.calls != 2 ||
		ipLimiter.settings.PerMinute != 180 ||
		ipLimiter.settings.BurstPer10Seconds != 40 {
		t.Fatalf("IP limiter calls=%d settings=%+v", ipLimiter.calls, ipLimiter.settings)
	}
	if userLimiter.calls != 2 || userLimiter.limit != 120 {
		t.Fatalf("user limiter calls=%d limit=%d", userLimiter.calls, userLimiter.limit)
	}
}

func TestRouterW5ManagementGroupUpdateParsesJSONBeforeAuthAndUserLimit(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantText   string
	}{
		{
			name:       "malformed",
			body:       `{"enabled":`,
			wantStatus: http.StatusBadRequest,
			wantText:   "请求体无效",
		},
		{
			name:       "oversized",
			body:       `{"description":"` + strings.Repeat("x", managementGroupCreateMaxBodyBytes) + `"}`,
			wantStatus: http.StatusRequestEntityTooLarge,
			wantText:   "请求体过大",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			readAuthenticator := &managementAPIAuthenticatorStub{}
			touchAuthenticator := &managementAPIAuthenticatorStub{
				err: &managementauth.AuthError{
					StatusCode: http.StatusUnauthorized,
					Message:    "请先登录",
				},
			}
			ipLimiter := &publicSettingsRateLimiterStub{
				decision: SystemAPIRateLimitDecision{Allowed: true},
			}
			userLimiter := &systemAPIAuthenticatedRateLimiterStub{
				decision: SystemAPIRateLimitDecision{Allowed: true},
			}
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
				ManagementGroupUpdateHandler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					writeData(w, http.StatusOK, map[string]string{"id": "grp_1"})
				}),
			})

			req := httptest.NewRequest(
				http.MethodPatch,
				"/__aisys__/api/groups/grp_1",
				strings.NewReader(tt.body),
			)
			req.Header.Set("Cookie", "juhe_ai_session=session-token")
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus || !strings.Contains(rec.Body.String(), tt.wantText) {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if ipLimiter.calls != 1 {
				t.Fatalf("IP limiter calls=%d, want 1 before parser", ipLimiter.calls)
			}
			if touchAuthenticator.touchCookieHeader != "" || userLimiter.calls != 0 {
				t.Fatalf(
					"touch=%q user limiter calls=%d, want parser rejection before auth/user limit",
					touchAuthenticator.touchCookieHeader,
					userLimiter.calls,
				)
			}
		})
	}
}

func TestRouterDoesNotRegisterW5ManagementGroupUpdateWhenDisabled(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeData(w, http.StatusOK, map[string]string{"id": "grp_1"})
	})
	router := NewRouter(RouterOptions{
		Config:                         config.Config{Host: "127.0.0.1", Port: 3000},
		ManagementGroupUpdateHandler:   handler,
		ManagementMyGroupUpdateHandler: handler,
	})

	for _, path := range []string{"/__aisys__/api/groups/grp_1", "/__aisys__/api/my-groups/grp_1"} {
		req := httptest.NewRequest(http.MethodPatch, path, strings.NewReader(`{"enabled":false}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}
}

type managementGroupUpdateServiceStub struct {
	input  managementgroups.UpdateInput
	result managementgroups.UpdateResult
	err    error
	calls  int
}

func (s *managementGroupUpdateServiceStub) Update(
	_ *http.Request,
	input managementgroups.UpdateInput,
) (managementgroups.UpdateResult, error) {
	s.calls++
	s.input = input
	return s.result, s.err
}

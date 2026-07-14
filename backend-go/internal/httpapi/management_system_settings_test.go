package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/config"
	operationlogjob "juhe-ai/backend-go/internal/jobs/operationlog"
	"juhe-ai/backend-go/internal/jobs/queue"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementsettings"
	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/systemsettings"
)

func TestManagementSystemSettingsHandlerAllowsAdministratorsAndReturnsCompleteContract(t *testing.T) {
	settings := managementSystemSettingsSnapshot(t, nil)
	for _, role := range []string{"admin", "super_admin"} {
		t.Run(role, func(t *testing.T) {
			service := &managementSystemSettingsServiceStub{settings: settings}
			handler := newManagementSystemSettingsHandler(service)
			req := managementSystemSettingsRequest(http.MethodGet, role, "")
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
			}
			if service.getCalls != 1 {
				t.Fatalf("Get() calls = %d, want 1", service.getCalls)
			}
			assertManagementSystemSettingsResponse(t, rec, settings)
		})
	}
}

func TestManagementSystemSettingsHandlersRequireContextAndAdmin(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		role       string
		withAuth   bool
		accountID  string
		wantStatus int
		wantMsg    string
	}{
		{
			name:       "get missing context",
			method:     http.MethodGet,
			wantStatus: http.StatusInternalServerError,
			wantMsg:    "服务器内部错误",
		},
		{
			name:       "patch missing context",
			method:     http.MethodPatch,
			wantStatus: http.StatusInternalServerError,
			wantMsg:    "服务器内部错误",
		},
		{
			name:       "get blank account id",
			method:     http.MethodGet,
			role:       "admin",
			withAuth:   true,
			accountID:  " ",
			wantStatus: http.StatusInternalServerError,
			wantMsg:    "服务器内部错误",
		},
		{
			name:       "patch blank account id",
			method:     http.MethodPatch,
			role:       "admin",
			withAuth:   true,
			accountID:  " ",
			wantStatus: http.StatusInternalServerError,
			wantMsg:    "服务器内部错误",
		},
		{
			name:       "get ordinary user",
			method:     http.MethodGet,
			role:       "user",
			withAuth:   true,
			accountID:  "sys_user",
			wantStatus: http.StatusForbidden,
			wantMsg:    "需要管理员权限",
		},
		{
			name:       "patch ordinary user",
			method:     http.MethodPatch,
			role:       "user",
			withAuth:   true,
			accountID:  "sys_user",
			wantStatus: http.StatusForbidden,
			wantMsg:    "需要管理员权限",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementSystemSettingsServiceStub{}
			var handler http.Handler
			body := ""
			if tt.method == http.MethodPatch {
				handler = newManagementSystemSettingsUpdateHandler(service)
				body = `{"accountTestTaskConcurrency":2}`
			} else {
				handler = newManagementSystemSettingsHandler(service)
			}
			req := httptest.NewRequest(tt.method, "/__aisys__/api/settings", strings.NewReader(body))
			if tt.withAuth {
				authContext := managementauth.Context{
					SystemAccountID: tt.accountID,
					Username:        tt.role,
					DisplayName:     tt.role,
					Role:            tt.role,
					SessionID:       "sess_" + tt.role,
				}
				req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, authContext))
			}
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assertManagementSystemSettingsMessage(t, rec, tt.wantStatus, tt.wantMsg)
			if service.getCalls != 0 || service.updateCalls != 0 {
				t.Fatalf("service calls = get:%d update:%d, want 0/0", service.getCalls, service.updateCalls)
			}
		})
	}
}

func TestManagementSystemSettingsHandlerRedactsNilStoreAndSnapshotErrors(t *testing.T) {
	tests := []struct {
		name    string
		handler http.Handler
	}{
		{
			name:    "nil service",
			handler: NewManagementSystemSettingsHandler(nil),
		},
		{
			name: "store error",
			handler: NewManagementSystemSettingsHandler(managementsettings.NewSystemService(
				&managementSystemSettingsStoreStub{readErr: errors.New("postgres password leaked")},
			)),
		},
		{
			name: "invalid snapshot",
			handler: NewManagementSystemSettingsHandler(managementsettings.NewSystemService(
				&managementSystemSettingsStoreStub{},
			)),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := managementSystemSettingsRequest(http.MethodGet, "admin", "")
			rec := httptest.NewRecorder()

			tt.handler.ServeHTTP(rec, req)

			assertManagementSystemSettingsMessage(t, rec, http.StatusInternalServerError, "服务器内部错误")
			if strings.Contains(rec.Body.String(), "postgres password leaked") ||
				strings.Contains(rec.Body.String(), "management system settings") {
				t.Fatalf("response leaked internal error: %s", rec.Body.String())
			}
		})
	}
}

func TestManagementSystemSettingsUpdateHandlerAllowsAdministratorsAndReturnsCompleteContract(t *testing.T) {
	before := managementSystemSettingsSnapshot(t, nil)
	after := managementSystemSettingsSnapshot(t, map[string]string{
		"accountTestTaskConcurrency":       "8",
		"gatewayTextRawBodyLimitMegabytes": "32",
	})
	for _, role := range []string{"admin", "super_admin"} {
		t.Run(role, func(t *testing.T) {
			service := &managementSystemSettingsServiceStub{
				updateResult: managementsettings.SystemUpdateResult{
					Before:   before,
					Settings: after,
				},
			}
			handler := newManagementSystemSettingsUpdateHandler(service)
			req := managementSystemSettingsRequest(
				http.MethodPatch,
				role,
				`{"gatewayTextRawBodyLimitMegabytes":32,"accountTestTaskConcurrency":8}`,
			)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
			}
			if service.updateCalls != 1 {
				t.Fatalf("Update() calls = %d, want 1", service.updateCalls)
			}
			if len(service.updateInput.Values) != 2 ||
				string(service.updateInput.Values["accountTestTaskConcurrency"]) != "8" ||
				string(service.updateInput.Values["gatewayTextRawBodyLimitMegabytes"]) != "32" {
				t.Fatalf("update input = %+v", service.updateInput.Values)
			}
			assertManagementSystemSettingsResponse(t, rec, after)
		})
	}
}

func TestManagementSystemSettingsUpdateHandlerStrictBodyValidation(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantMsg    string
	}{
		{name: "empty body", body: "", wantStatus: http.StatusBadRequest, wantMsg: "请求体无效"},
		{name: "syntax error", body: `{"accountTestTaskConcurrency":`, wantStatus: http.StatusBadRequest, wantMsg: "请求体无效"},
		{name: "trailing json", body: `{"accountTestTaskConcurrency":2} true`, wantStatus: http.StatusBadRequest, wantMsg: "请求体无效"},
		{name: "top level null", body: `null`, wantStatus: http.StatusBadRequest, wantMsg: "请求体必须是对象"},
		{name: "top level array", body: `[]`, wantStatus: http.StatusBadRequest, wantMsg: "请求体必须是对象"},
		{name: "top level string", body: `"value"`, wantStatus: http.StatusBadRequest, wantMsg: "请求体必须是对象"},
		{name: "top level number", body: `1`, wantStatus: http.StatusBadRequest, wantMsg: "请求体必须是对象"},
		{name: "top level boolean", body: `true`, wantStatus: http.StatusBadRequest, wantMsg: "请求体必须是对象"},
		{name: "empty object", body: `{}`, wantStatus: http.StatusBadRequest, wantMsg: "系统设置更新不能为空"},
		{
			name: "oversized body",
			body: `{"accountTestTaskConcurrency":` +
				strings.Repeat("1", managementGlobalSettingsMaxBodyBytes) +
				`}`,
			wantStatus: http.StatusRequestEntityTooLarge,
			wantMsg:    "请求体过大",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementSystemSettingsServiceStub{}
			handler := newManagementSystemSettingsUpdateHandler(service)
			req := managementSystemSettingsRequest(http.MethodPatch, "admin", tt.body)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assertManagementSystemSettingsMessage(t, rec, tt.wantStatus, tt.wantMsg)
			if service.updateCalls != 0 {
				t.Fatalf("Update() calls = %d, want 0", service.updateCalls)
			}
		})
	}
}

func TestManagementSystemSettingsUpdateHandlerReturnsDomainValidationErrorsAsBadRequest(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		err     error
		wantMsg string
	}{
		{
			name:    "unknown field",
			body:    `{"unknownSetting":1}`,
			wantMsg: "未知系统设置字段：unknownSetting",
		},
		{
			name:    "null",
			body:    `{"accountTestTaskConcurrency":null}`,
			wantMsg: "accountTestTaskConcurrency 必须是整数",
		},
		{
			name:    "float",
			body:    `{"accountTestTaskConcurrency":1.5}`,
			wantMsg: "accountTestTaskConcurrency 必须是整数",
		},
		{
			name:    "numeric string",
			body:    `{"accountTestTaskConcurrency":"2"}`,
			wantMsg: "accountTestTaskConcurrency 必须是整数",
		},
		{
			name:    "boolean",
			body:    `{"accountTestTaskConcurrency":true}`,
			wantMsg: "accountTestTaskConcurrency 必须是整数",
		},
		{
			name:    "out of range",
			body:    `{"accountTestTaskConcurrency":1001}`,
			wantMsg: "accountTestTaskConcurrency 必须在 1 到 1000 之间",
		},
		{
			name:    "empty patch sentinel",
			body:    `{"accountTestTaskConcurrency":2}`,
			err:     fmt.Errorf("wrapped: %w", systemsettings.ErrPatchEmpty),
			wantMsg: "系统设置更新不能为空",
		},
		{
			name: "timezone online update",
			body: `{"usageStatsTimezone":"Asia/Shanghai"}`,
			err: fmt.Errorf(
				"wrapped: %w",
				managementsettings.ErrUsageStatsTimezoneOnlineUpdateUnsupported,
			),
			wantMsg: "PostgreSQL 模式下暂不支持在线修改统计时区，请停机后通过离线迁移 / 重建流程调整",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementSystemSettingsServiceStub{
				updateFunc: func(input managementsettings.SystemUpdateInput) (managementsettings.SystemUpdateResult, error) {
					if tt.err != nil {
						return managementsettings.SystemUpdateResult{}, tt.err
					}
					_, err := systemsettings.NewPatch(input.Values)
					return managementsettings.SystemUpdateResult{}, err
				},
			}
			handler := newManagementSystemSettingsUpdateHandler(service)
			req := managementSystemSettingsRequest(http.MethodPatch, "admin", tt.body)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assertManagementSystemSettingsMessage(t, rec, http.StatusBadRequest, tt.wantMsg)
			if service.updateCalls != 1 {
				t.Fatalf("Update() calls = %d, want 1", service.updateCalls)
			}
		})
	}
}

func TestManagementSystemSettingsUpdateHandlerRedactsInternalErrors(t *testing.T) {
	tests := []struct {
		name    string
		handler http.Handler
	}{
		{
			name:    "nil service",
			handler: NewManagementSystemSettingsUpdateHandler(nil),
		},
		{
			name: "service error",
			handler: newManagementSystemSettingsUpdateHandler(&managementSystemSettingsServiceStub{
				updateErr: errors.New("postgres password leaked"),
			}),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := managementSystemSettingsRequest(
				http.MethodPatch,
				"admin",
				`{"accountTestTaskConcurrency":2}`,
			)
			rec := httptest.NewRecorder()

			tt.handler.ServeHTTP(rec, req)

			assertManagementSystemSettingsMessage(t, rec, http.StatusInternalServerError, "服务器内部错误")
			if strings.Contains(rec.Body.String(), "postgres password leaked") ||
				strings.Contains(rec.Body.String(), "management system settings") {
				t.Fatalf("response leaked internal error: %s", rec.Body.String())
			}
		})
	}
}

func TestManagementSystemSettingsUpdateHandlerEnqueuesStableOperationLogChanges(t *testing.T) {
	createdAt := time.Date(2026, 7, 10, 15, 0, 0, 0, time.UTC)
	before := managementSystemSettingsSnapshot(t, nil)
	after := managementSystemSettingsSnapshot(t, map[string]string{
		"accountTestTaskConcurrency":       "8",
		"gatewayTextRawBodyLimitMegabytes": "32",
		"systemMetricsHourlyRetentionDays": "20",
		"usageStatsDailyRetentionDays":     "30",
	})
	queueStub := &managementSystemSettingsOperationLogQueueStub{}
	service := &managementSystemSettingsServiceStub{
		updateResult: managementsettings.SystemUpdateResult{
			Before:   before,
			Settings: after,
		},
	}
	handler := newManagementSystemSettingsUpdateHandler(
		service,
		newManagementOperationLogOptions(ManagementOperationLogOptions{
			Config:   config.Config{TrustProxy: "false"},
			Client:   queueStub,
			Now:      func() time.Time { return createdAt },
			NewLogID: func() string { return "oplog_system_settings" },
		}),
	)
	req := managementSystemSettingsRequest(
		http.MethodPatch,
		"admin",
		`{"usageStatsDailyRetentionDays":30,"systemMetricsHourlyRetentionDays":20,"gatewayTextRawBodyLimitMegabytes":32,"accountTestTaskConcurrency":8}`,
	)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("User-Agent", "system-settings-test")
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "req_system_settings"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if queueStub.calls != 1 {
		t.Fatalf("queue calls = %d, want 1", queueStub.calls)
	}
	if queueStub.taskType != operationlogjob.TaskTypeWrite ||
		queueStub.options.Queue != operationlogjob.QueueName {
		t.Fatalf("queue task=%q options=%+v", queueStub.taskType, queueStub.options)
	}
	logInput, err := operationlogjob.DecodeWriteTaskPayload(queueStub.payload)
	if err != nil {
		t.Fatalf("DecodeWriteTaskPayload() error = %v", err)
	}
	if logInput.ID != "oplog_system_settings" ||
		logInput.TraceID != "req_system_settings" ||
		logInput.ActorSystemAccountID != "sys_admin" ||
		logInput.ActorUsername != "admin" ||
		logInput.ActorDisplayName != "admin" ||
		logInput.ActorRole != "admin" ||
		logInput.Mode != "admin" ||
		logInput.Module != "settings" ||
		logInput.Action != "update_settings" ||
		logInput.OperationKey != "settings.update" ||
		logInput.ResourceType != "system_settings" ||
		logInput.ResourceID != "system" ||
		logInput.ResourceName != "系统运行设置" ||
		logInput.Summary != "更新系统运行设置" ||
		logInput.DetailLevel != "summary" ||
		logInput.VisibilityScope != "all_users" ||
		logInput.Method != http.MethodPatch ||
		logInput.Path != "/__aisys__/api/settings" ||
		logInput.ClientIP != "127.0.0.1" ||
		logInput.UserAgent != "system-settings-test" ||
		!logInput.CreatedAt.Equal(createdAt) {
		t.Fatalf("operation log input = %+v", logInput)
	}
	if logInput.StatusCode == nil || *logInput.StatusCode != http.StatusOK {
		t.Fatalf("status code = %+v, want 200", logInput.StatusCode)
	}
	if len(logInput.Changes) != 4 {
		t.Fatalf("changes = %+v, want 4", logInput.Changes)
	}
	wantFields := []string{
		"accountTestTaskConcurrency",
		"gatewayTextRawBodyLimitMegabytes",
		"systemMetricsHourlyRetentionDays",
		"usageStatsDailyRetentionDays",
	}
	wantBefore := []float64{1, 1, 1, 1}
	wantAfter := []float64{8, 32, 20, 30}
	for index, change := range logInput.Changes {
		if change.Field != wantFields[index] || change.Label != wantFields[index] {
			t.Fatalf("change[%d] = %+v, want field/label %q", index, change, wantFields[index])
		}
		if got := managementSystemSettingsOperationLogNumber(t, change.Before); got != wantBefore[index] {
			t.Fatalf("change[%d].Before = %v, want %v", index, got, wantBefore[index])
		}
		if got := managementSystemSettingsOperationLogNumber(t, change.After); got != wantAfter[index] {
			t.Fatalf("change[%d].After = %v, want %v", index, got, wantAfter[index])
		}
	}
}

func TestManagementSystemSettingsUpdateHandlerEnqueuesNoOpOperationLog(t *testing.T) {
	settings := managementSystemSettingsSnapshot(t, nil)
	queueStub := &managementSystemSettingsOperationLogQueueStub{}
	service := &managementSystemSettingsServiceStub{
		updateResult: managementsettings.SystemUpdateResult{
			Before:   settings,
			Settings: settings,
		},
	}
	handler := newManagementSystemSettingsUpdateHandler(
		service,
		newManagementOperationLogOptions(ManagementOperationLogOptions{
			Client:   queueStub,
			NewLogID: func() string { return "oplog_system_settings_noop" },
		}),
	)
	req := managementSystemSettingsRequest(
		http.MethodPatch,
		"admin",
		`{"accountTestTaskConcurrency":1}`,
	)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if queueStub.calls != 1 {
		t.Fatalf("queue calls = %d, want 1", queueStub.calls)
	}
	logInput, err := operationlogjob.DecodeWriteTaskPayload(queueStub.payload)
	if err != nil {
		t.Fatalf("DecodeWriteTaskPayload() error = %v", err)
	}
	if logInput.Changes == nil || len(logInput.Changes) != 0 {
		t.Fatalf("changes = %#v, want []", logInput.Changes)
	}
}

func TestManagementSystemSettingsUpdateHandlerKeepsSuccessWhenOperationLogQueueFails(t *testing.T) {
	settings := managementSystemSettingsSnapshot(t, nil)
	queueStub := &managementSystemSettingsOperationLogQueueStub{err: errors.New("redis down")}
	service := &managementSystemSettingsServiceStub{
		updateResult: managementsettings.SystemUpdateResult{
			Before:   settings,
			Settings: settings,
		},
	}
	handler := newManagementSystemSettingsUpdateHandler(
		service,
		newManagementOperationLogOptions(ManagementOperationLogOptions{Client: queueStub}),
	)
	req := managementSystemSettingsRequest(
		http.MethodPatch,
		"admin",
		`{"accountTestTaskConcurrency":1}`,
	)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if queueStub.calls != 1 {
		t.Fatalf("queue calls = %d, want 1", queueStub.calls)
	}
	assertManagementSystemSettingsResponse(t, rec, settings)
}

func TestRouterRegistersManagementSystemSettingsAsLimitedReadRoute(t *testing.T) {
	service := &managementSystemSettingsServiceStub{
		settings: managementSystemSettingsSnapshot(t, nil),
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
		SystemAPIRateLimitReader:          systemAPIRateLimitReaderStub{settings: port.SystemAPIRateLimitSettings{IPReadPerMinute: 600, IPReadBurstPer10Seconds: 120, UserReadPerMinute: 300}},
		SystemAPIIPRateLimiter:            ipLimiter,
		SystemAPIAuthenticatedRateLimiter: userLimiter,
		ManagementAPIAuthMiddleware:       NewManagementAPIAuthMiddleware(readAuthenticator),
		ManagementAPIAuthTouchMiddleware:  NewManagementAPIAuthTouchMiddleware(touchAuthenticator),
		ManagementSystemSettingsHandler:   newManagementSystemSettingsHandler(service),
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/settings", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if readAuthenticator.cookieHeader != "juhe_ai_session=session-token" {
		t.Fatalf("read auth cookie = %q", readAuthenticator.cookieHeader)
	}
	if touchAuthenticator.touchCookieHeader != "" {
		t.Fatalf("touch auth cookie = %q, want empty for read route", touchAuthenticator.touchCookieHeader)
	}
	if ipLimiter.calls != 1 || ipLimiter.settings.PerMinute != 600 || ipLimiter.settings.BurstPer10Seconds != 120 {
		t.Fatalf("IP limiter calls=%d settings=%+v, want read limits", ipLimiter.calls, ipLimiter.settings)
	}
	if userLimiter.calls != 1 || userLimiter.limit != 300 {
		t.Fatalf("user limiter calls=%d limit=%d, want one read limit call", userLimiter.calls, userLimiter.limit)
	}
	if service.getCalls != 1 {
		t.Fatalf("Get() calls = %d, want 1", service.getCalls)
	}
}

func TestRouterRegistersManagementSystemSettingsUpdateAsLimitedWriteRoute(t *testing.T) {
	settings := managementSystemSettingsSnapshot(t, nil)
	service := &managementSystemSettingsServiceStub{
		updateResult: managementsettings.SystemUpdateResult{
			Before:   settings,
			Settings: settings,
		},
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
		Config:                                config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		SystemAPIRateLimitReader:              systemAPIRateLimitReaderStub{settings: port.SystemAPIRateLimitSettings{IPWritePerMinute: 180, IPWriteBurstPer10Seconds: 40, UserWritePerMinute: 120}},
		SystemAPIIPRateLimiter:                ipLimiter,
		SystemAPIAuthenticatedRateLimiter:     userLimiter,
		ManagementAPIAuthMiddleware:           NewManagementAPIAuthMiddleware(readAuthenticator),
		ManagementAPIAuthTouchMiddleware:      NewManagementAPIAuthTouchMiddleware(touchAuthenticator),
		ManagementSystemSettingsUpdateHandler: newManagementSystemSettingsUpdateHandler(service),
	})

	req := httptest.NewRequest(
		http.MethodPatch,
		"/__aisys__/api/settings",
		strings.NewReader(`{"accountTestTaskConcurrency":1}`),
	)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if touchAuthenticator.touchCookieHeader != "juhe_ai_session=session-token" {
		t.Fatalf("touch auth cookie = %q", touchAuthenticator.touchCookieHeader)
	}
	if readAuthenticator.cookieHeader != "" {
		t.Fatalf("read auth cookie = %q, want empty for write route", readAuthenticator.cookieHeader)
	}
	if ipLimiter.calls != 1 || ipLimiter.settings.PerMinute != 180 || ipLimiter.settings.BurstPer10Seconds != 40 {
		t.Fatalf("IP limiter calls=%d settings=%+v, want write limits", ipLimiter.calls, ipLimiter.settings)
	}
	if userLimiter.calls != 1 || userLimiter.limit != 120 {
		t.Fatalf("user limiter calls=%d limit=%d, want one write limit call", userLimiter.calls, userLimiter.limit)
	}
	if service.updateCalls != 1 {
		t.Fatalf("Update() calls = %d, want 1", service.updateCalls)
	}
}

func TestRouterDoesNotRegisterManagementSystemSettingsWhenDisabled(t *testing.T) {
	service := &managementSystemSettingsServiceStub{
		settings: managementSystemSettingsSnapshot(t, nil),
	}
	router := NewRouter(RouterOptions{
		Config:                                config.Config{Host: "127.0.0.1", Port: 3000},
		ManagementSystemSettingsHandler:       newManagementSystemSettingsHandler(service),
		ManagementSystemSettingsUpdateHandler: newManagementSystemSettingsUpdateHandler(service),
	})

	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/__aisys__/api/settings", nil),
		httptest.NewRequest(
			http.MethodPatch,
			"/__aisys__/api/settings",
			strings.NewReader(`{"accountTestTaskConcurrency":1}`),
		),
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, request)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404 while management API disabled", request.Method, rec.Code)
		}
	}
	if service.getCalls != 0 || service.updateCalls != 0 {
		t.Fatalf("service calls = get:%d update:%d, want 0/0", service.getCalls, service.updateCalls)
	}
}

func TestRouterParsesManagementSettingsJSONBeforeAuthenticationAndUserLimit(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		body       string
		wantStatus int
		wantMsg    string
	}{
		{
			name:       "system settings malformed",
			path:       "/__aisys__/api/settings",
			body:       `{"accountTestTaskConcurrency":`,
			wantStatus: http.StatusBadRequest,
			wantMsg:    "请求体无效",
		},
		{
			name:       "global settings malformed",
			path:       "/__aisys__/api/settings/global",
			body:       `{"appName":`,
			wantStatus: http.StatusBadRequest,
			wantMsg:    "请求体无效",
		},
		{
			name: "system settings oversized",
			path: "/__aisys__/api/settings",
			body: `{"accountTestTaskConcurrency":` +
				strings.Repeat("1", managementGlobalSettingsMaxBodyBytes) +
				`}`,
			wantStatus: http.StatusRequestEntityTooLarge,
			wantMsg:    "请求体过大",
		},
		{
			name: "global settings oversized",
			path: "/__aisys__/api/settings/global",
			body: `{"appName":"` +
				strings.Repeat("x", managementGlobalSettingsMaxBodyBytes) +
				`"}`,
			wantStatus: http.StatusRequestEntityTooLarge,
			wantMsg:    "请求体过大",
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
			userLimiter := &systemAPIAuthenticatedRateLimiterStub{
				decision: SystemAPIRateLimitDecision{Allowed: true},
			}
			router := NewRouter(RouterOptions{
				Config:                                config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
				SystemAPIRateLimitReader:              systemAPIRateLimitReaderStub{settings: port.SystemAPIRateLimitSettings{IPWritePerMinute: 180, IPWriteBurstPer10Seconds: 40, UserWritePerMinute: 120}},
				SystemAPIIPRateLimiter:                &publicSettingsRateLimiterStub{decision: SystemAPIRateLimitDecision{Allowed: true}},
				SystemAPIAuthenticatedRateLimiter:     userLimiter,
				ManagementAPIAuthMiddleware:           NewManagementAPIAuthMiddleware(readAuthenticator),
				ManagementAPIAuthTouchMiddleware:      NewManagementAPIAuthTouchMiddleware(touchAuthenticator),
				ManagementSystemSettingsUpdateHandler: newManagementSystemSettingsUpdateHandler(&managementSystemSettingsServiceStub{}),
				ManagementGlobalSettingsUpdateHandler: newManagementGlobalSettingsUpdateHandler(&managementGlobalSettingsUpdateServiceStub{}),
			})

			req := httptest.NewRequest(http.MethodPatch, tt.path, strings.NewReader(tt.body))
			req.Header.Set("Cookie", "juhe_ai_session=session-token")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assertManagementSystemSettingsMessage(t, rec, tt.wantStatus, tt.wantMsg)
			if touchAuthenticator.touchCookieHeader != "" {
				t.Fatalf("touch auth cookie = %q, want parser rejection before authentication", touchAuthenticator.touchCookieHeader)
			}
			if readAuthenticator.cookieHeader != "" {
				t.Fatalf("read auth cookie = %q, want empty for write route", readAuthenticator.cookieHeader)
			}
			if userLimiter.calls != 0 {
				t.Fatalf("user limiter calls = %d, want 0 before authentication", userLimiter.calls)
			}
		})
	}
}

func managementSystemSettingsRequest(method string, role string, body string) *http.Request {
	req := httptest.NewRequest(method, "/__aisys__/api/settings", strings.NewReader(body))
	authContext := managementauth.Context{
		SystemAccountID: "sys_" + role,
		Username:        role,
		DisplayName:     role,
		Role:            role,
		SessionID:       "sess_" + role,
	}
	return req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, authContext))
}

func managementSystemSettingsSnapshot(
	t *testing.T,
	overrides map[string]string,
) systemsettings.Snapshot {
	t.Helper()
	values := make(map[string]json.RawMessage, len(systemsettings.Definitions()))
	for _, definition := range systemsettings.Definitions() {
		if definition.Kind == systemsettings.ValueKindTimezone {
			values[definition.Key] = json.RawMessage(`"UTC"`)
			continue
		}
		values[definition.Key] = json.RawMessage(strconv.Itoa(definition.Minimum))
	}
	for key, value := range overrides {
		values[key] = json.RawMessage(value)
	}
	settings, err := systemsettings.NewSnapshot(values)
	if err != nil {
		t.Fatalf("NewSnapshot() error = %v", err)
	}
	if settings.Len() != 53 {
		t.Fatalf("settings length = %d, want 53", settings.Len())
	}
	return settings
}

func assertManagementSystemSettingsResponse(
	t *testing.T,
	rec *httptest.ResponseRecorder,
	want systemsettings.Snapshot,
) {
	t.Helper()
	var envelope map[string]json.RawMessage
	if err := json.NewDecoder(rec.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(envelope) != 1 {
		t.Fatalf("response keys = %v, want only data", envelope)
	}
	rawData, ok := envelope["data"]
	if !ok {
		t.Fatalf("response = %v, missing data", envelope)
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(rawData, &data); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	if len(data) != 53 {
		t.Fatalf("data field count = %d, want 53", len(data))
	}
	for key, wantValue := range want.Values() {
		gotValue, exists := data[key]
		if !exists {
			t.Fatalf("data missing field %q", key)
		}
		if string(gotValue) != string(wantValue) {
			t.Fatalf("data[%q] = %s, want %s", key, gotValue, wantValue)
		}
	}
}

func assertManagementSystemSettingsMessage(
	t *testing.T,
	rec *httptest.ResponseRecorder,
	wantStatus int,
	wantMessage string,
) {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, wantStatus, rec.Body.String())
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["message"] != wantMessage {
		t.Fatalf("message = %q, want %q", body["message"], wantMessage)
	}
}

func managementSystemSettingsOperationLogNumber(t *testing.T, value any) float64 {
	t.Helper()
	number, ok := value.(float64)
	if !ok {
		t.Fatalf("operation log value = %#v (%T), want JSON number", value, value)
	}
	return number
}

type managementSystemSettingsServiceStub struct {
	settings     systemsettings.Snapshot
	getErr       error
	getCalls     int
	updateInput  managementsettings.SystemUpdateInput
	updateResult managementsettings.SystemUpdateResult
	updateErr    error
	updateCalls  int
	updateFunc   func(managementsettings.SystemUpdateInput) (managementsettings.SystemUpdateResult, error)
}

func (s *managementSystemSettingsServiceStub) Get(context.Context) (systemsettings.Snapshot, error) {
	s.getCalls++
	return s.settings, s.getErr
}

func (s *managementSystemSettingsServiceStub) Update(
	_ context.Context,
	input managementsettings.SystemUpdateInput,
) (managementsettings.SystemUpdateResult, error) {
	s.updateCalls++
	s.updateInput = managementsettings.SystemUpdateInput{
		Values: make(map[string]json.RawMessage, len(input.Values)),
	}
	for key, value := range input.Values {
		s.updateInput.Values[key] = append(json.RawMessage(nil), value...)
	}
	if s.updateFunc != nil {
		return s.updateFunc(input)
	}
	return s.updateResult, s.updateErr
}

var (
	_ managementSystemSettingsReadService   = (*managementSystemSettingsServiceStub)(nil)
	_ managementSystemSettingsUpdateService = (*managementSystemSettingsServiceStub)(nil)
)

type managementSystemSettingsStoreStub struct {
	settings systemsettings.Snapshot
	readErr  error
}

func (s *managementSystemSettingsStoreStub) ManagementSystemSettings(context.Context) (systemsettings.Snapshot, error) {
	return s.settings, s.readErr
}

func (s *managementSystemSettingsStoreStub) UpdateManagementSystemSettings(
	context.Context,
	port.ManagementSystemSettingsUpdateInput,
) (port.ManagementSystemSettingsUpdateResult, error) {
	return port.ManagementSystemSettingsUpdateResult{}, errors.New("unexpected update")
}

var _ managementsettings.SystemStore = (*managementSystemSettingsStoreStub)(nil)

type managementSystemSettingsOperationLogQueueStub struct {
	calls    int
	taskType string
	payload  []byte
	options  queue.EnqueueOptions
	err      error
}

func (s *managementSystemSettingsOperationLogQueueStub) Enqueue(
	_ context.Context,
	taskType string,
	payload []byte,
	opts queue.EnqueueOptions,
) (queue.TaskInfo, error) {
	s.calls++
	s.taskType = taskType
	s.payload = append([]byte(nil), payload...)
	s.options = opts
	return queue.TaskInfo{ID: "task_system_settings", Queue: opts.Queue, Type: taskType}, s.err
}

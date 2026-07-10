package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/config"
	operationlogjob "juhe-ai/backend-go/internal/jobs/operationlog"
	"juhe-ai/backend-go/internal/jobs/queue"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementsettings"
	"juhe-ai/backend-go/internal/modules/publicsettings"
	"juhe-ai/backend-go/internal/store/port"
)

func TestManagementGlobalSettingsHandlerAllowsAdministratorsAndReturnsExactContract(t *testing.T) {
	for _, role := range []string{"admin", "super_admin"} {
		t.Run(role, func(t *testing.T) {
			reader := &managementGlobalSettingsReaderStub{
				settings: port.PublicGlobalSettings{
					AppName: "聚合 AI",
					AppIcon: "/__aisys__/brand-icon.svg",
				},
			}
			service := publicsettings.NewService(reader)
			handler := NewManagementGlobalSettingsHandler(&service)
			req := managementGlobalSettingsRequest(role)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
			}
			if !reader.called {
				t.Fatal("public settings service was not called")
			}
			const want = "{\"data\":{\"appName\":\"聚合 AI\",\"appIcon\":\"/__aisys__/brand-icon.svg\"}}\n"
			if got := rec.Body.String(); got != want {
				t.Fatalf("body = %q, want %q", got, want)
			}
		})
	}
}

func TestManagementGlobalSettingsHandlerRejectsOrdinaryUser(t *testing.T) {
	reader := &managementGlobalSettingsReaderStub{}
	service := publicsettings.NewService(reader)
	handler := NewManagementGlobalSettingsHandler(&service)
	req := managementGlobalSettingsRequest("user")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", rec.Code, rec.Body.String())
	}
	if reader.called {
		t.Fatal("public settings service should not be called for ordinary user")
	}
	const want = "{\"message\":\"需要管理员权限\"}\n"
	if got := rec.Body.String(); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestManagementGlobalSettingsHandlerRedactsServiceErrors(t *testing.T) {
	reader := &managementGlobalSettingsReaderStub{
		err: errors.New("postgres password leaked"),
	}
	service := publicsettings.NewService(reader)
	handler := NewManagementGlobalSettingsHandler(&service)
	req := managementGlobalSettingsRequest("admin")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body = %s", rec.Code, rec.Body.String())
	}
	if !reader.called {
		t.Fatal("public settings service was not called")
	}
	const want = "{\"message\":\"服务器内部错误\"}\n"
	if got := rec.Body.String(); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
	if strings.Contains(rec.Body.String(), "postgres password leaked") {
		t.Fatalf("body leaked service error: %s", rec.Body.String())
	}
}

func TestManagementGlobalSettingsUpdateHandlerAllowsAdministratorsAndReturnsExactContract(t *testing.T) {
	for _, role := range []string{"admin", "super_admin"} {
		t.Run(role, func(t *testing.T) {
			service := &managementGlobalSettingsUpdateServiceStub{
				result: managementsettings.UpdateResult{
					Before: managementsettings.Settings{
						AppName: "聚合 AI",
						AppIcon: "/__aisys__/brand-icon.svg",
					},
					Settings: managementsettings.Settings{
						AppName: "新名称",
						AppIcon: "/new-icon.svg",
					},
				},
			}
			handler := newManagementGlobalSettingsUpdateHandler(service)
			req := managementGlobalSettingsUpdateRequest(role, `{"appName":" 新名称 ","appIcon":" /new-icon.svg "}`)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
			}
			if !service.called {
				t.Fatal("management settings service was not called")
			}
			if service.input.AppName == nil || *service.input.AppName != "新名称" {
				t.Fatalf("appName input = %+v, want trimmed value", service.input.AppName)
			}
			if service.input.AppIcon == nil || *service.input.AppIcon != "/new-icon.svg" {
				t.Fatalf("appIcon input = %+v, want trimmed value", service.input.AppIcon)
			}
			const want = "{\"data\":{\"appName\":\"新名称\",\"appIcon\":\"/new-icon.svg\"}}\n"
			if got := rec.Body.String(); got != want {
				t.Fatalf("body = %q, want %q", got, want)
			}
		})
	}
}

func TestManagementGlobalSettingsUpdateHandlerRejectsOrdinaryUser(t *testing.T) {
	service := &managementGlobalSettingsUpdateServiceStub{}
	handler := newManagementGlobalSettingsUpdateHandler(service)
	req := managementGlobalSettingsUpdateRequest("user", `{"appName":"新名称"}`)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", rec.Code, rec.Body.String())
	}
	if service.called {
		t.Fatal("management settings service should not be called for ordinary user")
	}
	const want = "{\"message\":\"需要管理员权限\"}\n"
	if got := rec.Body.String(); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestManagementGlobalSettingsUpdateHandlerStrictBodyValidation(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantMsg string
	}{
		{name: "empty body", body: "", wantMsg: "请求体无效"},
		{name: "syntax error", body: `{"appName":`, wantMsg: "请求体无效"},
		{name: "trailing json", body: `{"appName":"新名称"} true`, wantMsg: "请求体无效"},
		{name: "top level null", body: `null`, wantMsg: "请求体必须是对象"},
		{name: "top level array", body: `[]`, wantMsg: "请求体必须是对象"},
		{name: "top level string", body: `"value"`, wantMsg: "请求体必须是对象"},
		{name: "empty object", body: `{}`, wantMsg: "全局设置更新不能为空"},
		{name: "unknown field", body: `{"theme":"dark"}`, wantMsg: "未知全局设置字段：theme"},
		{name: "app name null", body: `{"appName":null}`, wantMsg: "appName 必须是非空字符串"},
		{name: "app name non string", body: `{"appName":123}`, wantMsg: "appName 必须是非空字符串"},
		{name: "app name blank", body: `{"appName":" \t "}`, wantMsg: "appName 必须是非空字符串"},
		{name: "app icon null", body: `{"appIcon":null}`, wantMsg: "appIcon 必须是非空字符串"},
		{name: "app icon non string", body: `{"appIcon":false}`, wantMsg: "appIcon 必须是非空字符串"},
		{name: "app icon blank", body: `{"appIcon":" \n "}`, wantMsg: "appIcon 必须是非空字符串"},
		{
			name:    "oversized body",
			body:    `{"appName":"` + strings.Repeat("x", managementGlobalSettingsMaxBodyBytes) + `"}`,
			wantMsg: "请求体过大",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementGlobalSettingsUpdateServiceStub{}
			handler := newManagementGlobalSettingsUpdateHandler(service)
			req := managementGlobalSettingsUpdateRequest("admin", tt.body)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
			}
			if service.called {
				t.Fatal("management settings service should not be called for invalid body")
			}
			var body map[string]string
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body["message"] != tt.wantMsg {
				t.Fatalf("message = %q, want %q", body["message"], tt.wantMsg)
			}
		})
	}
}

func TestManagementGlobalSettingsUpdateHandlerRedactsServiceErrors(t *testing.T) {
	service := &managementGlobalSettingsUpdateServiceStub{
		err: errors.New("postgres password leaked"),
	}
	handler := newManagementGlobalSettingsUpdateHandler(service)
	req := managementGlobalSettingsUpdateRequest("admin", `{"appName":"新名称"}`)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body = %s", rec.Code, rec.Body.String())
	}
	if !service.called {
		t.Fatal("management settings service was not called")
	}
	const want = "{\"message\":\"服务器内部错误\"}\n"
	if got := rec.Body.String(); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
	if strings.Contains(rec.Body.String(), "postgres password leaked") {
		t.Fatalf("body leaked service error: %s", rec.Body.String())
	}
}

func TestManagementGlobalSettingsUpdateHandlerEnqueuesOperationLogWithActualChanges(t *testing.T) {
	createdAt := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	queueStub := &managementGlobalSettingsOperationLogQueueStub{}
	service := &managementGlobalSettingsUpdateServiceStub{
		result: managementsettings.UpdateResult{
			Before: managementsettings.Settings{
				AppName: "聚合 AI",
				AppIcon: "/same-icon.svg",
			},
			Settings: managementsettings.Settings{
				AppName: "新名称",
				AppIcon: "/same-icon.svg",
			},
		},
	}
	handler := newManagementGlobalSettingsUpdateHandler(
		service,
		newManagementOperationLogOptions(ManagementOperationLogOptions{
			Config:   config.Config{TrustProxy: "false"},
			Client:   queueStub,
			Now:      func() time.Time { return createdAt },
			NewLogID: func() string { return "oplog_settings" },
		}),
	)
	req := managementGlobalSettingsUpdateRequest("admin", `{"appName":"新名称"}`)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("User-Agent", "settings-test")
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "req_settings"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if queueStub.calls != 1 {
		t.Fatalf("queue calls = %d, want 1", queueStub.calls)
	}
	if queueStub.taskType != operationlogjob.TaskTypeWrite || queueStub.options.Queue != operationlogjob.QueueName {
		t.Fatalf("queue task=%q options=%+v", queueStub.taskType, queueStub.options)
	}
	logInput, err := operationlogjob.DecodeWriteTaskPayload(queueStub.payload)
	if err != nil {
		t.Fatalf("DecodeWriteTaskPayload() error = %v", err)
	}
	if logInput.ID != "oplog_settings" ||
		logInput.TraceID != "req_settings" ||
		logInput.ActorSystemAccountID != "sys_admin" ||
		logInput.ActorUsername != "admin" ||
		logInput.ActorDisplayName != "admin" ||
		logInput.ActorRole != "admin" ||
		logInput.OperationScopeSystemAccountID != "" ||
		logInput.Mode != "admin" ||
		logInput.Module != "settings" ||
		logInput.Action != "update_global" ||
		logInput.OperationKey != "settings.update_global" ||
		logInput.ResourceType != "global_settings" ||
		logInput.ResourceID != "global" ||
		logInput.ResourceName != "全局品牌设置" ||
		logInput.Summary != "更新全局品牌设置" ||
		logInput.VisibilityScope != "all_users" ||
		logInput.DetailLevel != "summary" ||
		logInput.Method != http.MethodPatch ||
		logInput.Path != "/__aisys__/api/settings/global" ||
		logInput.ClientIP != "127.0.0.1" ||
		logInput.UserAgent != "settings-test" ||
		!logInput.CreatedAt.Equal(createdAt) {
		t.Fatalf("operation log input = %+v", logInput)
	}
	if logInput.StatusCode == nil || *logInput.StatusCode != http.StatusOK {
		t.Fatalf("status code = %+v, want 200", logInput.StatusCode)
	}
	if len(logInput.Changes) != 1 {
		t.Fatalf("changes = %+v, want one actual change", logInput.Changes)
	}
	change := logInput.Changes[0]
	if change.Field != "appName" ||
		change.Label != "系统名称" ||
		change.Before != "聚合 AI" ||
		change.After != "新名称" {
		t.Fatalf("change = %+v", change)
	}
	if len(logInput.Targets) != 0 || len(logInput.Viewers) != 0 {
		t.Fatalf("targets=%+v viewers=%+v, want none for all_users log", logInput.Targets, logInput.Viewers)
	}
}

func TestManagementGlobalSettingsUpdateHandlerKeepsSuccessWhenOperationLogQueueFails(t *testing.T) {
	queueStub := &managementGlobalSettingsOperationLogQueueStub{err: errors.New("redis down")}
	service := &managementGlobalSettingsUpdateServiceStub{
		result: managementsettings.UpdateResult{
			Before:   managementsettings.Settings{AppName: "旧名称", AppIcon: "/icon.svg"},
			Settings: managementsettings.Settings{AppName: "新名称", AppIcon: "/icon.svg"},
		},
	}
	handler := newManagementGlobalSettingsUpdateHandler(
		service,
		newManagementOperationLogOptions(ManagementOperationLogOptions{Client: queueStub}),
	)
	req := managementGlobalSettingsUpdateRequest("admin", `{"appName":"新名称"}`)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if queueStub.calls != 1 {
		t.Fatalf("queue calls = %d, want 1", queueStub.calls)
	}
}

func TestManagementGlobalSettingsUpdateHandlerEnqueuesNoOpOperationLog(t *testing.T) {
	queueStub := &managementGlobalSettingsOperationLogQueueStub{}
	settings := managementsettings.Settings{AppName: "同名", AppIcon: "/same-icon.svg"}
	service := &managementGlobalSettingsUpdateServiceStub{
		result: managementsettings.UpdateResult{
			Before:   settings,
			Settings: settings,
		},
	}
	handler := newManagementGlobalSettingsUpdateHandler(
		service,
		newManagementOperationLogOptions(ManagementOperationLogOptions{
			Client:   queueStub,
			NewLogID: func() string { return "oplog_settings_noop" },
		}),
	)
	req := managementGlobalSettingsUpdateRequest("admin", `{"appName":"同名"}`)
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
	if len(logInput.Changes) != 0 {
		t.Fatalf("changes = %+v, want empty no-op changes", logInput.Changes)
	}
}

func TestGlobalSettingsUpdateOperationChangesOnlyIncludesChangedFields(t *testing.T) {
	result := managementsettings.UpdateResult{
		Before:   managementsettings.Settings{AppName: "同名", AppIcon: "/old-icon.svg"},
		Settings: managementsettings.Settings{AppName: "同名", AppIcon: "/new-icon.svg"},
	}
	changes := globalSettingsUpdateOperationChanges(result)
	if len(changes) != 1 {
		t.Fatalf("changes = %+v, want one appIcon change", changes)
	}
	change := changes[0]
	if change.Field != "appIcon" ||
		change.Label != "系统图标" ||
		change.Before != "/old-icon.svg" ||
		change.After != "/new-icon.svg" {
		t.Fatalf("change = %+v", change)
	}

	result.Before = result.Settings
	if changes := globalSettingsUpdateOperationChanges(result); len(changes) != 0 {
		t.Fatalf("no-op changes = %+v, want empty", changes)
	}
}

func TestRouterRegistersManagementGlobalSettingsAsLimitedReadRoute(t *testing.T) {
	reader := &managementGlobalSettingsReaderStub{
		settings: port.PublicGlobalSettings{
			AppName: "聚合 AI",
			AppIcon: "/__aisys__/brand-icon.svg",
		},
	}
	service := publicsettings.NewService(reader)
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
		ManagementGlobalSettingsHandler:   NewManagementGlobalSettingsHandler(&service),
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/settings/global", nil)
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
	if !reader.called {
		t.Fatal("global settings reader was not called")
	}
}

func TestRouterRegistersManagementGlobalSettingsUpdateAsLimitedWriteRoute(t *testing.T) {
	service := &managementGlobalSettingsUpdateServiceStub{
		result: managementsettings.UpdateResult{
			Before:   managementsettings.Settings{AppName: "聚合 AI", AppIcon: "/__aisys__/brand-icon.svg"},
			Settings: managementsettings.Settings{AppName: "新名称", AppIcon: "/__aisys__/brand-icon.svg"},
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
		SystemAPIRateLimitReader:              systemAPIRateLimitReaderStub{settings: port.SystemAPIRateLimitSettings{IPWritePerMinute: 180, IPWriteBurstPer10Seconds: 40, UserWritePerMinute: 90}},
		SystemAPIIPRateLimiter:                ipLimiter,
		SystemAPIAuthenticatedRateLimiter:     userLimiter,
		ManagementAPIAuthMiddleware:           NewManagementAPIAuthMiddleware(readAuthenticator),
		ManagementAPIAuthTouchMiddleware:      NewManagementAPIAuthTouchMiddleware(touchAuthenticator),
		ManagementGlobalSettingsUpdateHandler: newManagementGlobalSettingsUpdateHandler(service),
	})

	req := httptest.NewRequest(http.MethodPatch, "/__aisys__/api/settings/global", strings.NewReader(`{"appName":"新名称"}`))
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
	if userLimiter.calls != 1 || userLimiter.limit != 90 {
		t.Fatalf("user limiter calls=%d limit=%d, want one write limit call", userLimiter.calls, userLimiter.limit)
	}
	if !service.called {
		t.Fatal("management settings service was not called")
	}
}

func TestRouterRequiresTouchMiddlewareForManagementGlobalSettingsUpdate(t *testing.T) {
	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("NewRouter() did not panic without touch middleware")
		}
		if got := recovered.(string); got != "ManagementAPIAuthTouchMiddleware is required for Go management write routes" {
			t.Fatalf("panic = %q", got)
		}
	}()

	_ = NewRouter(RouterOptions{
		Config: config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
			context: managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"},
		}),
		ManagementGlobalSettingsUpdateHandler: newManagementGlobalSettingsUpdateHandler(&managementGlobalSettingsUpdateServiceStub{}),
	})
}

func TestRouterManagementGlobalSettingsRequiresAuthentication(t *testing.T) {
	reader := &managementGlobalSettingsReaderStub{}
	service := publicsettings.NewService(reader)
	userLimiter := &systemAPIAuthenticatedRateLimiterStub{
		decision: SystemAPIRateLimitDecision{Allowed: true},
	}
	router := NewRouter(RouterOptions{
		Config:                            config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		SystemAPIRateLimitReader:          systemAPIRateLimitReaderStub{settings: port.SystemAPIRateLimitSettings{}},
		SystemAPIIPRateLimiter:            &publicSettingsRateLimiterStub{decision: SystemAPIRateLimitDecision{Allowed: true}},
		SystemAPIAuthenticatedRateLimiter: userLimiter,
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
			err: &managementauth.AuthError{
				StatusCode: http.StatusUnauthorized,
				Message:    "请先登录",
			},
		}),
		ManagementGlobalSettingsHandler: NewManagementGlobalSettingsHandler(&service),
	})

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/settings/global", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body = %s", rec.Code, rec.Body.String())
	}
	if userLimiter.calls != 0 {
		t.Fatalf("user limiter calls = %d, want 0 before authentication", userLimiter.calls)
	}
	if reader.called {
		t.Fatal("global settings reader should not run before authentication")
	}
}

func TestRouterDoesNotRegisterManagementGlobalSettingsWhenDisabled(t *testing.T) {
	reader := &managementGlobalSettingsReaderStub{}
	service := publicsettings.NewService(reader)
	updateService := &managementGlobalSettingsUpdateServiceStub{}
	router := NewRouter(RouterOptions{
		Config:                                config.Config{Host: "127.0.0.1", Port: 3000},
		ManagementGlobalSettingsHandler:       NewManagementGlobalSettingsHandler(&service),
		ManagementGlobalSettingsUpdateHandler: newManagementGlobalSettingsUpdateHandler(updateService),
	})

	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/__aisys__/api/settings/global", nil),
		httptest.NewRequest(http.MethodPatch, "/__aisys__/api/settings/global", strings.NewReader(`{"appName":"新名称"}`)),
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, request)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404 while management API disabled", request.Method, rec.Code)
		}
	}
	if reader.called {
		t.Fatal("global settings reader should not run while management API disabled")
	}
	if updateService.called {
		t.Fatal("global settings update service should not run while management API disabled")
	}
}

func managementGlobalSettingsRequest(role string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/settings/global", nil)
	authContext := managementauth.Context{
		SystemAccountID: "sys_" + role,
		Username:        role,
		Role:            role,
		SessionID:       "sess_" + role,
	}
	return req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, authContext))
}

func managementGlobalSettingsUpdateRequest(role string, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPatch, "/__aisys__/api/settings/global", strings.NewReader(body))
	authContext := managementauth.Context{
		SystemAccountID: "sys_" + role,
		Username:        role,
		DisplayName:     role,
		Role:            role,
		SessionID:       "sess_" + role,
	}
	return req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, authContext))
}

type managementGlobalSettingsReaderStub struct {
	called   bool
	settings port.PublicGlobalSettings
	err      error
}

func (s *managementGlobalSettingsReaderStub) PublicGlobalSettings(context.Context) (port.PublicGlobalSettings, error) {
	s.called = true
	return s.settings, s.err
}

var _ port.PublicSettingsReader = (*managementGlobalSettingsReaderStub)(nil)

type managementGlobalSettingsUpdateServiceStub struct {
	called bool
	input  managementsettings.UpdateInput
	result managementsettings.UpdateResult
	err    error
}

func (s *managementGlobalSettingsUpdateServiceStub) Update(_ context.Context, input managementsettings.UpdateInput) (managementsettings.UpdateResult, error) {
	s.called = true
	s.input = input
	return s.result, s.err
}

type managementGlobalSettingsOperationLogQueueStub struct {
	calls    int
	taskType string
	payload  []byte
	options  queue.EnqueueOptions
	err      error
}

func (s *managementGlobalSettingsOperationLogQueueStub) Enqueue(_ context.Context, taskType string, payload []byte, opts queue.EnqueueOptions) (queue.TaskInfo, error) {
	s.calls++
	s.taskType = taskType
	s.payload = append([]byte(nil), payload...)
	s.options = opts
	return queue.TaskInfo{ID: "task_settings", Queue: opts.Queue, Type: taskType}, s.err
}

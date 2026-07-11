package httpapi

import (
	"encoding/json"
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

func TestManagementAPIKeyUpdateHandlerScopesAndPreservesFieldPresence(t *testing.T) {
	tests := []struct {
		name         string
		scope        managementAPIKeyScope
		role         string
		path         string
		wantTarget   string
		wantSelfOnly bool
		wantOwner    bool
	}{
		{
			name:       "admin global omitted",
			scope:      managementAPIKeyScopeAdmin,
			role:       "admin",
			path:       "/__aisys__/api/api-keys/key_1",
			wantTarget: "",
			wantOwner:  true,
		},
		{
			name:       "admin global all",
			scope:      managementAPIKeyScopeAdmin,
			role:       "super_admin",
			path:       "/__aisys__/api/api-keys/key_1?systemAccountId=all",
			wantTarget: "",
			wantOwner:  true,
		},
		{
			name:       "admin explicit owner",
			scope:      managementAPIKeyScopeAdmin,
			role:       "admin",
			path:       "/__aisys__/api/api-keys/key_1?systemAccountId=%20sys_target%20",
			wantTarget: "sys_target",
			wantOwner:  true,
		},
		{
			name:         "self ignores forged query",
			scope:        managementAPIKeyScopeSelf,
			role:         "user",
			path:         "/__aisys__/api/my-api-keys/key_1?systemAccountId=&systemAccountId=sys_forged",
			wantTarget:   "sys_actor",
			wantSelfOnly: true,
			wantOwner:    false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &managementAPIKeyUpdateServiceStub{
				result: managementapikeys.UpdateResult{
					After:                managementAPIKeyUpdateListItem("key_1", "sys_target", "更新后"),
					OwnerSystemAccountID: "sys_target",
					Committed:            true,
				},
			}
			handler := newManagementAPIKeyUpdateHandler(
				service,
				test.scope,
				managementOperationLogOptions{},
			)
			req := httptest.NewRequest(http.MethodPatch, test.path, strings.NewReader(`{
				"name":" 新名称 ",
				"description":null,
				"routeStrategyId":" route_2 ",
				"status":"disabled",
				"expiresAt":null,
				"quotaLimits":{"daily":{"enabled":true,"limit":9007199254740991}},
				"availabilitySchedule":null
			}`))
			req = requestWithManagementAPIKeyID(req, "key_1")
			req = requestWithManagementAPIKeyAuthContext(req, managementauth.Context{
				SystemAccountID: "sys_actor",
				Role:            test.role,
			})
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			input := service.input
			if service.calls != 1 ||
				input.ActorSystemAccountID != "sys_actor" ||
				input.ActorRole != test.role ||
				input.SystemAccountID != test.wantTarget ||
				input.SelfOnly != test.wantSelfOnly ||
				input.APIKeyID != "key_1" ||
				!input.HasName || input.Name != " 新名称 " ||
				!input.HasDescription || input.Description != nil ||
				!input.HasRouteStrategyID || input.RouteStrategyID != " route_2 " ||
				!input.HasStatus || input.Status != "disabled" ||
				!input.HasExpiresAt || input.ExpiresAt != nil ||
				!input.HasQuotaLimits ||
				!input.HasAvailabilitySchedule || input.AvailabilitySchedule != nil {
				t.Fatalf("service calls=%d input=%+v", service.calls, input)
			}
			quota, ok := input.QuotaLimits.(map[string]any)
			if !ok {
				t.Fatalf("quota type = %T", input.QuotaLimits)
			}
			daily, ok := quota["daily"].(map[string]any)
			if !ok || daily["limit"] != json.Number("9007199254740991") {
				t.Fatalf("quota = %#v", quota)
			}
			if rec.Header().Get("Cache-Control") != "no-store" ||
				rec.Header().Get("Pragma") != "no-cache" {
				t.Fatalf("cache headers = %#v", rec.Header())
			}
			var body struct {
				Data map[string]json.RawMessage `json:"data"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if _, exists := body.Data["key"]; exists {
				t.Fatal("response leaked key")
			}
			if strings.Contains(rec.Body.String(), `"message"`) {
				t.Fatalf("response contains message: %s", rec.Body.String())
			}
			_, ownerExists := body.Data["systemAccountId"]
			if ownerExists != test.wantOwner {
				t.Fatalf("owner exists = %v, want %v; body=%s", ownerExists, test.wantOwner, rec.Body.String())
			}
		})
	}
}

func TestManagementAPIKeyUpdateHandlerRejectsInvalidScopeAndBodies(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		body        string
		wantMessage string
	}{
		{name: "empty target", path: "/api-keys/key_1?systemAccountId=", body: `{"name":"ok"}`, wantMessage: "系统账号 ID 不能为空"},
		{name: "repeated target", path: "/api-keys/key_1?systemAccountId=a&systemAccountId=b", body: `{"name":"ok"}`, wantMessage: "Expected string, received array"},
		{name: "empty object", path: "/api-keys/key_1", body: `{}`, wantMessage: "请提供要修改的 API Key 内容"},
		{name: "unknown field", path: "/api-keys/key_1", body: `{"unknown":true}`, wantMessage: "API Key 参数无效"},
		{name: "array", path: "/api-keys/key_1", body: `[]`, wantMessage: "API Key 参数无效"},
		{name: "null", path: "/api-keys/key_1", body: `null`, wantMessage: "API Key 参数无效"},
		{name: "scalar", path: "/api-keys/key_1", body: `"value"`, wantMessage: "API Key 参数无效"},
		{name: "wrong type", path: "/api-keys/key_1", body: `{"status":true}`, wantMessage: "API Key 参数无效"},
		{name: "trailing", path: "/api-keys/key_1", body: `{"status":"active"} {}`, wantMessage: "请求体无效"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &managementAPIKeyUpdateServiceStub{}
			handler := newManagementAPIKeyUpdateHandler(
				service,
				managementAPIKeyScopeAdmin,
				managementOperationLogOptions{},
			)
			req := httptest.NewRequest(http.MethodPatch, test.path, strings.NewReader(test.body))
			req = requestWithManagementAPIKeyID(req, "key_1")
			req = requestWithManagementAPIKeyAuthContext(req, managementauth.Context{
				SystemAccountID: "sys_admin",
				Role:            "admin",
			})
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest ||
				!strings.Contains(rec.Body.String(), test.wantMessage) ||
				service.calls != 0 {
				t.Fatalf("status=%d calls=%d body=%s", rec.Code, service.calls, rec.Body.String())
			}
			if rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("cache header = %q", rec.Header().Get("Cache-Control"))
			}
		})
	}
}

func TestManagementAPIKeyUpdateHandlerMapsErrorsAndLogsOnlyCommittedResults(t *testing.T) {
	tests := []struct {
		name       string
		result     managementapikeys.UpdateResult
		err        error
		wantStatus int
		wantText   string
		wantLogs   int
		wantID     string
		wantName   string
	}{
		{name: "not found", err: managementapikeys.ErrAPIKeyNotFound, wantStatus: http.StatusNotFound, wantText: "API Key 不存在"},
		{name: "duplicate", err: managementapikeys.NewAPIKeyNameExistsError("重复"), wantStatus: http.StatusConflict, wantText: "API Key 名称已存在：重复"},
		{name: "default route", err: managementapikeys.ErrAPIKeyDefaultRouteChange, wantStatus: http.StatusBadRequest, wantText: "默认 API Key 不允许更换策略路由"},
		{name: "route missing", err: managementapikeys.ErrAPIKeyRouteStrategyMissing, wantStatus: http.StatusBadRequest, wantText: managementapikeys.ErrAPIKeyRouteStrategyMissing.Error()},
		{name: "route disabled", err: managementapikeys.ErrAPIKeyRouteStrategyOff, wantStatus: http.StatusBadRequest, wantText: managementapikeys.ErrAPIKeyRouteStrategyOff.Error()},
		{name: "validation", err: managementapikeys.ErrAPIKeyUpdateInvalid, wantStatus: http.StatusBadRequest, wantText: "API Key 参数无效"},
		{
			name: "committed cache failure",
			result: managementapikeys.UpdateResult{
				Before:               managementAPIKeyUpdateListItem("key_1", "sys_owner", "更新前"),
				After:                managementAPIKeyUpdateListItem("key_1", "sys_owner", "更新后"),
				OwnerSystemAccountID: "sys_owner",
				Committed:            true,
			},
			err:        managementapikeys.ErrAPIKeyUpdateValidationCacheInvalidation,
			wantStatus: http.StatusInternalServerError,
			wantText:   "服务器内部错误",
			wantLogs:   1,
			wantID:     "key_1",
			wantName:   "更新后",
		},
		{
			name: "committed parse failure",
			result: managementapikeys.UpdateResult{
				Before:               managementAPIKeyUpdateListItem("key_1", "sys_owner", "更新前"),
				After:                managementAPIKeyUpdateListItem("key_1", "sys_owner", "更新后"),
				OwnerSystemAccountID: "sys_owner",
				Committed:            true,
			},
			err:        errors.New("parse management API Key result"),
			wantStatus: http.StatusInternalServerError,
			wantText:   "服务器内部错误",
			wantLogs:   1,
			wantID:     "key_1",
			wantName:   "更新后",
		},
		{name: "internal", err: errors.New("database down"), wantStatus: http.StatusInternalServerError, wantText: "服务器内部错误"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			queue := &operationLogQueueStub{}
			service := &managementAPIKeyUpdateServiceStub{result: test.result, err: test.err}
			handler := newManagementAPIKeyUpdateHandler(
				service,
				managementAPIKeyScopeSelf,
				newManagementOperationLogOptions(ManagementOperationLogOptions{
					Client:   queue,
					NewLogID: func() string { return "oplog_update" },
				}),
			)
			req := httptest.NewRequest(http.MethodPatch, "/my-api-keys/key_1", strings.NewReader(`{"name":"更新后"}`))
			req = requestWithManagementAPIKeyID(req, "key_1")
			req = requestWithManagementAPIKeyAuthContext(req, managementauth.Context{
				SystemAccountID: "sys_owner",
				Role:            "user",
			})
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != test.wantStatus ||
				!strings.Contains(rec.Body.String(), test.wantText) ||
				queue.calls != test.wantLogs {
				t.Fatalf("status=%d logs=%d body=%s", rec.Code, queue.calls, rec.Body.String())
			}
			if test.wantLogs == 1 {
				logInput, err := operationlogjob.DecodeWriteTaskPayload(queue.payload)
				if err != nil {
					t.Fatalf("decode operation log: %v", err)
				}
				if logInput.StatusCode == nil || *logInput.StatusCode != test.wantStatus {
					t.Fatalf("operation log status = %v, want %d", logInput.StatusCode, test.wantStatus)
				}
				if logInput.ResourceID != test.wantID ||
					logInput.ResourceName != test.wantName {
					t.Fatalf(
						"operation log resource = %q/%q, want %q/%q",
						logInput.ResourceID,
						logInput.ResourceName,
						test.wantID,
						test.wantName,
					)
				}
			}
		})
	}
}

func TestManagementAPIKeyUpdateHandlerOperationLogContainsOnlyActualSafeChanges(t *testing.T) {
	queue := &operationLogQueueStub{}
	before := managementAPIKeyUpdateListItem("key_1", "sys_owner", "旧名称")
	after := managementAPIKeyUpdateListItem("key_1", "sys_owner", "新名称")
	before.Description = managementAPIKeyUpdateStringPtr("旧说明")
	after.Description = nil
	before.RouteStrategyID = "route_1"
	after.RouteStrategyID = "route_2"
	before.Status = "active"
	after.Status = "disabled"
	before.ExpiresAt = managementAPIKeyUpdateTimePtr(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
	after.ExpiresAt = nil
	before.QuotaLimits.Daily = &port.ManagementRequestQuotaLimit{Enabled: true, Limit: 10}
	after.QuotaLimits.Daily = &port.ManagementRequestQuotaLimit{Enabled: true, Limit: 20}
	before.AvailabilitySchedule = map[string]any{"timezone": "UTC"}
	after.AvailabilitySchedule = map[string]any{"timezone": "Asia/Shanghai"}
	service := &managementAPIKeyUpdateServiceStub{
		result: managementapikeys.UpdateResult{
			Before:               before,
			After:                after,
			OwnerSystemAccountID: "sys_owner",
			Committed:            true,
		},
	}
	handler := newManagementAPIKeyUpdateHandler(
		service,
		managementAPIKeyScopeAdmin,
		newManagementOperationLogOptions(ManagementOperationLogOptions{
			Client:   queue,
			NewLogID: func() string { return "oplog_update" },
		}),
	)
	req := httptest.NewRequest(http.MethodPatch, "/api-keys/key_1", strings.NewReader(`{"name":"新名称"}`))
	req = requestWithManagementAPIKeyID(req, "key_1")
	req = requestWithManagementAPIKeyAuthContext(req, managementauth.Context{
		SystemAccountID: "sys_admin",
		Role:            "admin",
	})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || queue.calls != 1 {
		t.Fatalf("status=%d logs=%d body=%s", rec.Code, queue.calls, rec.Body.String())
	}
	logInput, err := operationlogjob.DecodeWriteTaskPayload(queue.payload)
	if err != nil {
		t.Fatalf("decode operation log: %v", err)
	}
	if logInput.OperationKey != "api_keys.update" ||
		logInput.ResourceType != "api_key" ||
		logInput.ResourceName != "新名称" ||
		logInput.OperationScopeSystemAccountID != "sys_owner" ||
		len(logInput.Changes) != 7 {
		t.Fatalf("operation log = %+v", logInput)
	}
	allowed := map[string]bool{
		"name": true, "description": true, "routeStrategyId": true, "status": true,
		"expiresAt": true, "quotaLimits": true, "availabilitySchedule": true,
	}
	for _, change := range logInput.Changes {
		if !allowed[change.Field] || strings.Contains(strings.ToLower(change.Field), "key") {
			t.Fatalf("unsafe change = %+v", change)
		}
	}

	queue = &operationLogQueueStub{}
	service.result.Before = after
	service.result.After = after
	handler = newManagementAPIKeyUpdateHandler(
		service,
		managementAPIKeyScopeAdmin,
		newManagementOperationLogOptions(ManagementOperationLogOptions{
			Client:   queue,
			NewLogID: func() string { return "oplog_noop" },
		}),
	)
	req = httptest.NewRequest(http.MethodPatch, "/api-keys/key_1", strings.NewReader(`{"name":"新名称"}`))
	req = requestWithManagementAPIKeyID(req, "key_1")
	req = requestWithManagementAPIKeyAuthContext(req, managementauth.Context{
		SystemAccountID: "sys_admin",
		Role:            "admin",
	})
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	logInput, err = operationlogjob.DecodeWriteTaskPayload(queue.payload)
	if err != nil {
		t.Fatalf("decode no-op operation log: %v", err)
	}
	if len(logInput.Changes) != 0 {
		t.Fatalf("no-op changes = %+v", logInput.Changes)
	}
}

func TestRouterRegistersManagementAPIKeyUpdateRoutesWithoutMutationGuard(t *testing.T) {
	authenticator := &managementAPIKeyRefreshAuthStub{
		context: managementauth.Context{
			SystemAccountID: "sys_admin",
			Role:            "admin",
			SessionID:       "sess_admin",
		},
	}
	limiter := &systemAPIAuthenticatedRateLimiterStub{
		decision: SystemAPIRateLimitDecision{Allowed: true},
	}
	handlerCalls := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerCalls++
		writeData(w, http.StatusOK, map[string]string{"id": "key_1"})
	})
	router := NewRouter(RouterOptions{
		Config:                            config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                            slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		SystemAPIRateLimitReader:          systemAPIRateLimitReaderStub{settings: port.SystemAPIRateLimitSettings{IPWritePerMinute: 180, IPWriteBurstPer10Seconds: 40, UserWritePerMinute: 120}},
		SystemAPIIPRateLimiter:            NewInMemorySystemAPIIPRateLimiter(),
		SystemAPIAuthenticatedRateLimiter: limiter,
		ManagementAPIAuthMiddleware:       NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware:  NewManagementAPIAuthTouchMiddleware(authenticator),
		ManagementAPIKeyUpdateHandler:     handler,
		ManagementMyAPIKeyUpdateHandler:   handler,
	})

	for _, path := range []string{"/__aisys__/api/api-keys/key_1", "/__aisys__/api/my-api-keys/key_1"} {
		for attempt := 0; attempt < 2; attempt++ {
			req := httptest.NewRequest(http.MethodPatch, path, strings.NewReader(`{"status":"disabled"}`))
			req.Header.Set("Cookie", "juhe_ai_session=session-token")
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s attempt %d status=%d body=%s", path, attempt+1, rec.Code, rec.Body.String())
			}
		}
	}
	if handlerCalls != 4 || authenticator.touchCalls != 4 || authenticator.readCalls != 0 || limiter.calls != 4 {
		t.Fatalf(
			"handler=%d touch=%d read=%d limiter=%d",
			handlerCalls,
			authenticator.touchCalls,
			authenticator.readCalls,
			limiter.calls,
		)
	}
}

func TestRouterManagementAPIKeyUpdateTransportAndAdminBoundary(t *testing.T) {
	t.Run("transport before auth", func(t *testing.T) {
		authenticator := &managementAPIAuthenticatorStub{
			err: &managementauth.AuthError{StatusCode: http.StatusUnauthorized, Message: "未登录"},
		}
		router := NewRouter(RouterOptions{
			Config:                           config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
			Logger:                           slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
			ManagementAPIAuthMiddleware:      NewManagementAPIAuthMiddleware(authenticator),
			ManagementAPIAuthTouchMiddleware: NewManagementAPIAuthTouchMiddleware(authenticator),
			ManagementAPIKeyUpdateHandler:    http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		})
		req := httptest.NewRequest(http.MethodPatch, "/__aisys__/api/api-keys/key_1", strings.NewReader(`{"status":`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest ||
			!strings.Contains(rec.Body.String(), "请求体无效") ||
			rec.Header().Get("Cache-Control") != "no-store" ||
			authenticator.touchCookieHeader != "" {
			t.Fatalf(
				"status=%d cache=%q auth=%q body=%s",
				rec.Code,
				rec.Header().Get("Cache-Control"),
				authenticator.touchCookieHeader,
				rec.Body.String(),
			)
		}
	})

	t.Run("admin only and self logged in", func(t *testing.T) {
		authenticator := &managementAPIAuthenticatorStub{
			context: managementauth.Context{
				SystemAccountID: "sys_user",
				Role:            "user",
				SessionID:       "sess_user",
			},
		}
		handlerCalls := 0
		handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			handlerCalls++
			writeData(w, http.StatusOK, map[string]string{"id": "key_1"})
		})
		router := NewRouter(RouterOptions{
			Config:                           config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
			Logger:                           slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
			ManagementAPIAuthMiddleware:      NewManagementAPIAuthMiddleware(authenticator),
			ManagementAPIAuthTouchMiddleware: NewManagementAPIAuthTouchMiddleware(authenticator),
			ManagementAPIKeyUpdateHandler:    handler,
			ManagementMyAPIKeyUpdateHandler:  handler,
		})
		admin := serveManagementAPIKeyUpdateTestRequest(router, "/__aisys__/api/api-keys/key_1", `{"status":"disabled"}`)
		self := serveManagementAPIKeyUpdateTestRequest(router, "/__aisys__/api/my-api-keys/key_1", `{"status":"disabled"}`)
		if admin.Code != http.StatusForbidden || self.Code != http.StatusOK || handlerCalls != 1 {
			t.Fatalf("admin=%d self=%d calls=%d", admin.Code, self.Code, handlerCalls)
		}
	})
}

func TestManagementAPIKeyUpdateRouteClassificationAndDisabledRegistration(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeData(w, http.StatusOK, map[string]string{"id": "key_1"})
	})
	opts := RouterOptions{ManagementAPIKeyUpdateHandler: handler}
	if !managementBusinessRoutesConfigured(opts) || !managementWriteRoutesConfigured(opts) {
		t.Fatal("API Key update was not classified as management business/write route")
	}
	router := NewRouter(RouterOptions{
		Config:                          config.Config{Host: "127.0.0.1", Port: 3000},
		ManagementAPIKeyUpdateHandler:   handler,
		ManagementMyAPIKeyUpdateHandler: handler,
	})
	for _, path := range []string{"/__aisys__/api/api-keys/key_1", "/__aisys__/api/my-api-keys/key_1"} {
		rec := serveManagementAPIKeyUpdateTestRequest(router, path, `{"status":"disabled"}`)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}
}

type managementAPIKeyUpdateServiceStub struct {
	input  managementapikeys.UpdateInput
	result managementapikeys.UpdateResult
	err    error
	calls  int
}

func (s *managementAPIKeyUpdateServiceStub) Update(
	_ *http.Request,
	input managementapikeys.UpdateInput,
) (managementapikeys.UpdateResult, error) {
	s.calls++
	s.input = input
	return s.result, s.err
}

func managementAPIKeyUpdateListItem(id string, owner string, name string) managementapikeys.ListItem {
	return managementapikeys.ListItem{
		ID:                id,
		SystemAccountID:   owner,
		SystemAccountName: "Owner",
		Name:              name,
		KeyPrefix:         "sk-prefix",
		KeySuffix:         "suffix",
		Status:            "active",
		RouteStrategyID:   "route_1",
		RouteStrategyName: "默认策略",
		RouteStrategyMode: "normal",
		QuotaLimits:       port.ManagementRequestQuotaLimits{},
		Usage:             port.ManagementAccountUsageSummary{},
	}
}

func managementAPIKeyUpdateStringPtr(value string) *string {
	return &value
}

func managementAPIKeyUpdateTimePtr(value time.Time) *time.Time {
	return &value
}

func serveManagementAPIKeyUpdateTestRequest(
	router http.Handler,
	path string,
	body string,
) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPatch, path, strings.NewReader(body))
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

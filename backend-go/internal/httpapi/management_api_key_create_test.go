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
	"juhe-ai/backend-go/internal/modules/managementapikeys"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/store/port"
)

func TestManagementAPIKeyCreateHandlerScopesOwnerAndPreservesQuotaNumbers(t *testing.T) {
	tests := []struct {
		name            string
		scope           managementAPIKeyScope
		role            string
		path            string
		wantOwner       string
		wantSelfOnly    bool
		wantOwnerInBody bool
	}{
		{
			name:            "admin explicit owner",
			scope:           managementAPIKeyScopeAdmin,
			role:            "admin",
			path:            "/__aisys__/api/api-keys?systemAccountId=%20sys_target%20",
			wantOwner:       "sys_target",
			wantOwnerInBody: true,
		},
		{
			name:            "admin default owner",
			scope:           managementAPIKeyScopeAdmin,
			role:            "super_admin",
			path:            "/__aisys__/api/api-keys",
			wantOwner:       "sys_actor",
			wantOwnerInBody: true,
		},
		{
			name:            "admin all owner",
			scope:           managementAPIKeyScopeAdmin,
			role:            "admin",
			path:            "/__aisys__/api/api-keys?systemAccountId=all",
			wantOwner:       "sys_actor",
			wantOwnerInBody: true,
		},
		{
			name:         "self ignores forged owner",
			scope:        managementAPIKeyScopeSelf,
			role:         "user",
			path:         "/__aisys__/api/my-api-keys?systemAccountId=sys_forged",
			wantOwner:    "sys_actor",
			wantSelfOnly: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &managementAPIKeyCreateServiceStub{
				result: managementapikeys.CreateResult{
					ListItem: managementapikeys.ListItem{
						ID:                "key_created",
						SystemAccountID:   test.wantOwner,
						SystemAccountName: "Owner",
						Name:              "生产 Key",
						KeyPrefix:         "sk-creat",
						KeySuffix:         "23456789",
						Status:            "active",
						RouteStrategyID:   "route_1",
						QuotaLimits:       port.ManagementRequestQuotaLimits{},
						Usage:             port.ManagementAccountUsageSummary{},
					},
					Key:                  "sk-created-secret-0123456789",
					OwnerSystemAccountID: test.wantOwner,
				},
			}
			handler := newManagementAPIKeyCreateHandler(
				service,
				test.scope,
				managementOperationLogOptions{},
			)
			req := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(`{
				"name":" 生产 Key ",
				"description":null,
				"routeStrategyId":" route_1 ",
				"status":"active",
				"expiresAt":null,
				"quotaLimits":{"daily":{"enabled":true,"limit":9007199254740991}},
				"availabilitySchedule":null
			}`))
			req = requestWithManagementAPIKeyAuthContext(req, managementauth.Context{
				SystemAccountID: "sys_actor",
				Username:        "actor",
				Role:            test.role,
			})
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusCreated {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if service.calls != 1 ||
				service.input.ActorSystemAccountID != "sys_actor" ||
				service.input.ActorRole != test.role ||
				service.input.SystemAccountID != test.wantOwner ||
				service.input.SelfOnly != test.wantSelfOnly ||
				service.input.Name != " 生产 Key " ||
				service.input.RouteStrategyID != " route_1 " {
				t.Fatalf("service calls=%d input=%+v", service.calls, service.input)
			}
			quota, ok := service.input.QuotaLimits.(map[string]any)
			if !ok {
				t.Fatalf("quota type = %T", service.input.QuotaLimits)
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
				Data    map[string]json.RawMessage `json:"data"`
				Message string                     `json:"message"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.Message != "API Key 已创建，请立即复制完整密钥" ||
				string(body.Data["key"]) != `"sk-created-secret-0123456789"` {
				t.Fatalf("body = %s", rec.Body.String())
			}
			_, ownerExists := body.Data["systemAccountId"]
			if ownerExists != test.wantOwnerInBody {
				t.Fatalf("owner exists = %v, want %v; body = %s", ownerExists, test.wantOwnerInBody, rec.Body.String())
			}
		})
	}
}

func TestManagementAPIKeyCreateHandlerValidatesScopeAndBody(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		body        string
		wantMessage string
	}{
		{name: "empty target", path: "/__aisys__/api/api-keys?systemAccountId=", body: validManagementAPIKeyCreateBody(), wantMessage: "系统账号 ID 不能为空"},
		{name: "repeated target", path: "/__aisys__/api/api-keys?systemAccountId=a&systemAccountId=b", body: validManagementAPIKeyCreateBody(), wantMessage: "Expected string, received array"},
		{name: "unknown field", path: "/__aisys__/api/api-keys", body: `{"name":"Key","routeStrategyId":"route_1","unknown":true}`, wantMessage: "API Key 参数无效"},
		{name: "array", path: "/__aisys__/api/api-keys", body: `[]`, wantMessage: "API Key 参数无效"},
		{name: "missing name", path: "/__aisys__/api/api-keys", body: `{"routeStrategyId":"route_1"}`, wantMessage: "API Key 参数无效"},
		{name: "bad description", path: "/__aisys__/api/api-keys", body: `{"name":"Key","routeStrategyId":"route_1","description":1}`, wantMessage: "API Key 参数无效"},
		{name: "missing route", path: "/__aisys__/api/api-keys", body: `{"name":"Key"}`, wantMessage: "API Key 必须绑定策略路由"},
		{name: "empty route", path: "/__aisys__/api/api-keys", body: `{"name":"Key","routeStrategyId":" "}`, wantMessage: "API Key 必须绑定策略路由"},
		{name: "trailing JSON", path: "/__aisys__/api/api-keys", body: validManagementAPIKeyCreateBody() + `{}`, wantMessage: "请求体无效"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &managementAPIKeyCreateServiceStub{}
			handler := newManagementAPIKeyCreateHandler(
				service,
				managementAPIKeyScopeAdmin,
				managementOperationLogOptions{},
			)
			req := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
			req = requestWithManagementAPIKeyAuthContext(req, managementauth.Context{
				SystemAccountID: "sys_admin",
				Role:            "admin",
			})
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body["message"] != test.wantMessage {
				t.Fatalf("message = %q, want %q", body["message"], test.wantMessage)
			}
			if service.calls != 0 {
				t.Fatalf("service calls = %d, want 0", service.calls)
			}
		})
	}
}

func TestManagementAPIKeyCreateHandlerMapsServiceErrors(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantStatus  int
		wantMessage string
	}{
		{name: "invalid", err: managementapikeys.ErrAPIKeyCreateInvalid, wantStatus: http.StatusBadRequest, wantMessage: "API Key 参数无效"},
		{name: "route missing", err: managementapikeys.ErrAPIKeyRouteStrategyMissing, wantStatus: http.StatusBadRequest, wantMessage: managementapikeys.ErrAPIKeyRouteStrategyMissing.Error()},
		{name: "route disabled", err: managementapikeys.ErrAPIKeyRouteStrategyOff, wantStatus: http.StatusBadRequest, wantMessage: managementapikeys.ErrAPIKeyRouteStrategyOff.Error()},
		{name: "duplicate", err: managementapikeys.NewAPIKeyNameExistsError(" 生产 Key "), wantStatus: http.StatusConflict, wantMessage: "API Key 名称已存在：生产 Key"},
		{name: "internal", err: errors.New("postgres password leaked"), wantStatus: http.StatusInternalServerError, wantMessage: "服务器内部错误"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &managementAPIKeyCreateServiceStub{err: test.err}
			handler := newManagementAPIKeyCreateHandler(service, managementAPIKeyScopeSelf, managementOperationLogOptions{})
			req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/my-api-keys", strings.NewReader(validManagementAPIKeyCreateBody()))
			req = requestWithManagementAPIKeyAuthContext(req, managementauth.Context{
				SystemAccountID: "sys_user",
				Role:            "user",
			})
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, test.wantStatus, rec.Body.String())
			}
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body["message"] != test.wantMessage {
				t.Fatalf("message = %q, want %q", body["message"], test.wantMessage)
			}
		})
	}
}

func TestManagementAPIKeyCreateHandlerLogsOnlyAllowedFieldsAfterSuccess(t *testing.T) {
	const plaintext = "sk-created-secret-must-not-leak-0123456789"
	queueStub := &managementAPIKeySecretOperationLogQueueStub{}
	schedule := map[string]any{
		"enabled":  true,
		"timezone": "UTC",
		"mode":     "allow_windows",
		"windows":  []any{},
	}
	service := &managementAPIKeyCreateServiceStub{
		result: managementapikeys.CreateResult{
			ListItem: managementapikeys.ListItem{
				ID:                   "key_created",
				Name:                 "生产 Key",
				Description:          managementAPIKeyCreateStringPtr("secret description"),
				KeyPrefix:            "sk-creat",
				KeySuffix:            "23456789",
				Status:               "active",
				RouteStrategyID:      "route_1",
				ExpiresAt:            timePtr(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)),
				QuotaLimits:          port.ManagementRequestQuotaLimits{Daily: &port.ManagementRequestQuotaLimit{Enabled: true, Limit: 5}},
				AvailabilitySchedule: schedule,
			},
			Key:                  plaintext,
			OwnerSystemAccountID: "sys_owner",
		},
	}
	handler := newManagementAPIKeyCreateHandler(
		service,
		managementAPIKeyScopeAdmin,
		newManagementOperationLogOptions(ManagementOperationLogOptions{
			Client:   queueStub,
			Now:      func() time.Time { return time.Date(2026, 7, 11, 7, 0, 0, 0, time.UTC) },
			NewLogID: func() string { return "oplog_create" },
		}),
	)
	req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/api-keys?systemAccountId=sys_owner", strings.NewReader(validManagementAPIKeyCreateBody()))
	req = requestWithManagementAPIKeyAuthContext(req, managementauth.Context{
		SystemAccountID: "sys_admin",
		Username:        "admin",
		DisplayName:     "管理员",
		Role:            "admin",
	})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	input := queueStub.requireInput(t)
	if input.Module != "api_keys" ||
		input.Action != "create" ||
		input.OperationKey != "api_keys.create" ||
		input.ResourceType != "api_key" ||
		input.ResourceID != "key_created" ||
		input.ResourceName != "生产 Key" ||
		input.Summary != "创建 API Key：生产 Key" ||
		input.Mode != "admin" ||
		input.OperationScopeSystemAccountID != "sys_owner" {
		t.Fatalf("operation log = %+v", input)
	}
	wantFields := []string{"name", "status", "routeStrategyId", "availabilitySchedule", "key"}
	if len(input.Changes) != len(wantFields) {
		t.Fatalf("changes = %+v", input.Changes)
	}
	for index, field := range wantFields {
		if input.Changes[index].Field != field {
			t.Fatalf("change[%d] = %+v, want field %q", index, input.Changes[index], field)
		}
	}
	serialized, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal log: %v", err)
	}
	for _, forbidden := range []string{plaintext, "secret description", "expiresAt", "quotaLimits", "ciphertext", "hash"} {
		if strings.Contains(string(serialized), forbidden) {
			t.Fatalf("operation log leaked %q: %s", forbidden, serialized)
		}
	}
	if !strings.Contains(string(serialized), "sk-creat...23456789") {
		t.Fatalf("operation log missing marker: %s", serialized)
	}
}

func TestManagementAPIKeyCreateHandlerDoesNotLogFailuresOrOverrideSuccessOnLogFailure(t *testing.T) {
	t.Run("service failure", func(t *testing.T) {
		queueStub := &managementAPIKeySecretOperationLogQueueStub{}
		service := &managementAPIKeyCreateServiceStub{err: errors.New("create failed")}
		handler := newManagementAPIKeyCreateHandler(
			service,
			managementAPIKeyScopeSelf,
			newManagementOperationLogOptions(ManagementOperationLogOptions{Client: queueStub}),
		)
		req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/my-api-keys", strings.NewReader(validManagementAPIKeyCreateBody()))
		req = requestWithManagementAPIKeyAuthContext(req, managementauth.Context{SystemAccountID: "sys_user", Role: "user"})
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusInternalServerError || queueStub.calls != 0 {
			t.Fatalf("status=%d log calls=%d body=%s", rec.Code, queueStub.calls, rec.Body.String())
		}
	})

	t.Run("log failure", func(t *testing.T) {
		queueStub := &managementAPIKeySecretOperationLogQueueStub{err: errors.New("queue unavailable")}
		service := &managementAPIKeyCreateServiceStub{
			result: managementapikeys.CreateResult{
				ListItem: managementapikeys.ListItem{
					ID:              "key_created",
					Name:            "生产 Key",
					KeyPrefix:       "sk-creat",
					KeySuffix:       "23456789",
					Status:          "active",
					RouteStrategyID: "route_1",
				},
				Key:                  "sk-created-secret-0123456789",
				OwnerSystemAccountID: "sys_user",
			},
		}
		handler := newManagementAPIKeyCreateHandler(
			service,
			managementAPIKeyScopeSelf,
			newManagementOperationLogOptions(ManagementOperationLogOptions{Client: queueStub}),
		)
		req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/my-api-keys", strings.NewReader(validManagementAPIKeyCreateBody()))
		req = requestWithManagementAPIKeyAuthContext(req, managementauth.Context{SystemAccountID: "sys_user", Role: "user"})
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusCreated || queueStub.calls != 1 {
			t.Fatalf("status=%d log calls=%d body=%s", rec.Code, queueStub.calls, rec.Body.String())
		}
	})
}

func TestRouterRegistersManagementAPIKeyCreateRoutesWithWritePipeline(t *testing.T) {
	authenticator := &managementAPIKeyRefreshAuthStub{
		context: managementauth.Context{
			SystemAccountID: "sys_admin",
			Username:        "admin",
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
		writeData(w, http.StatusCreated, map[string]string{"id": "key_created"})
	})
	router := NewRouter(RouterOptions{
		Config:                            config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                            slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		SystemAPIRateLimitReader:          systemAPIRateLimitReaderStub{settings: port.SystemAPIRateLimitSettings{IPWritePerMinute: 180, IPWriteBurstPer10Seconds: 40, UserWritePerMinute: 120}},
		SystemAPIIPRateLimiter:            NewInMemorySystemAPIIPRateLimiter(),
		SystemAPIAuthenticatedRateLimiter: limiter,
		ManagementAPIAuthMiddleware:       NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware:  NewManagementAPIAuthTouchMiddleware(authenticator),
		ManagementAPIKeyCreateHandler:     handler,
		ManagementMyAPIKeyCreateHandler:   handler,
	})

	for index, path := range []string{"/__aisys__/api/api-keys", "/__aisys__/api/my-api-keys"} {
		body := `{"name":"生产 Key ` + string(rune('A'+index)) + `","routeStrategyId":"route_1"}`
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Cookie", "juhe_ai_session=session-token")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusCreated {
			t.Fatalf("%s status = %d, body = %s", path, rec.Code, rec.Body.String())
		}
	}
	if authenticator.touchCalls != 2 || authenticator.readCalls != 0 {
		t.Fatalf("touch=%d read=%d", authenticator.touchCalls, authenticator.readCalls)
	}
	if limiter.calls != 2 || limiter.limit != 120 {
		t.Fatalf("limiter calls=%d limit=%d", limiter.calls, limiter.limit)
	}
	if handlerCalls != 2 {
		t.Fatalf("handler calls = %d, want 2", handlerCalls)
	}
}

func TestRouterManagementAPIKeyCreateTransportRunsBeforeAuth(t *testing.T) {
	tests := []struct {
		name         string
		contentType  string
		body         string
		wantStatus   int
		wantMessage  string
		wantAuthCall bool
	}{
		{name: "malformed", contentType: "application/json", body: "{", wantStatus: http.StatusBadRequest, wantMessage: "请求体无效"},
		{name: "scalar", contentType: "application/json", body: `"key"`, wantStatus: http.StatusBadRequest, wantMessage: "请求体无效"},
		{name: "unsupported charset", contentType: "application/json; charset=gbk", body: `{}`, wantStatus: http.StatusUnsupportedMediaType, wantMessage: "请求体无效"},
		{name: "oversized", contentType: "application/json", body: `{"name":"` + strings.Repeat("x", managementAPIKeyCreateMaxBodyBytes) + `"}`, wantStatus: http.StatusRequestEntityTooLarge, wantMessage: "请求体过大"},
		{name: "array reaches schema", contentType: "application/json", body: `[]`, wantStatus: http.StatusBadRequest, wantMessage: "API Key 参数无效", wantAuthCall: true},
		{name: "empty reaches schema", contentType: "application/json", body: "", wantStatus: http.StatusBadRequest, wantMessage: "API Key 参数无效", wantAuthCall: true},
		{name: "non json reaches schema as empty object", contentType: "text/plain", body: validManagementAPIKeyCreateBody(), wantStatus: http.StatusBadRequest, wantMessage: "API Key 参数无效", wantAuthCall: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authenticator := &managementAPIKeyRefreshAuthStub{
				context: managementauth.Context{
					SystemAccountID: "sys_admin",
					Role:            "admin",
					SessionID:       "sess_admin",
				},
			}
			service := &managementAPIKeyCreateServiceStub{}
			router := NewRouter(RouterOptions{
				Config:                           config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
				Logger:                           slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
				ManagementAPIAuthMiddleware:      NewManagementAPIAuthMiddleware(authenticator),
				ManagementAPIAuthTouchMiddleware: NewManagementAPIAuthTouchMiddleware(authenticator),
				ManagementAPIKeyCreateHandler: newManagementAPIKeyCreateHandler(
					service,
					managementAPIKeyScopeAdmin,
					managementOperationLogOptions{},
				),
			})
			req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/api-keys", strings.NewReader(test.body))
			req.Header.Set("Cookie", "juhe_ai_session=session-token")
			req.Header.Set("Content-Type", test.contentType)
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if rec.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, test.wantStatus, rec.Body.String())
			}
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body["message"] != test.wantMessage {
				t.Fatalf("message = %q, want %q", body["message"], test.wantMessage)
			}
			authCalled := authenticator.touchCalls > 0
			if authCalled != test.wantAuthCall {
				t.Fatalf("auth called = %v, want %v", authCalled, test.wantAuthCall)
			}
			if service.calls != 0 {
				t.Fatalf("service calls = %d", service.calls)
			}
		})
	}
}

func TestRouterManagementAPIKeyCreateChecksAdminBeforeMutationGuard(t *testing.T) {
	authenticator := &managementAPIKeyRefreshAuthStub{
		context: managementauth.Context{
			SystemAccountID: "sys_user",
			Role:            "user",
			SessionID:       "sess_user",
		},
	}
	handlerCalls := 0
	router := NewRouter(RouterOptions{
		Config:                           config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                           slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementAPIAuthMiddleware:      NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware: NewManagementAPIAuthTouchMiddleware(authenticator),
		ManagementAPIKeyCreateHandler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			handlerCalls++
			writeData(w, http.StatusCreated, map[string]string{"id": "key_created"})
		}),
	})

	for attempt := 0; attempt < 2; attempt++ {
		req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/api-keys", strings.NewReader(validManagementAPIKeyCreateBody()))
		req.Header.Set("Cookie", "juhe_ai_session=session-token")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("attempt %d status = %d, body = %s", attempt+1, rec.Code, rec.Body.String())
		}
	}
	if handlerCalls != 0 {
		t.Fatalf("handler calls = %d", handlerCalls)
	}
}

func TestRouterManagementAPIKeyCreateDeduplicatesEffectiveOwnerAndTrimmedName(t *testing.T) {
	authenticator := &managementAPIKeyRefreshAuthStub{
		context: managementauth.Context{
			SystemAccountID: "sys_admin",
			Role:            "admin",
			SessionID:       "sess_admin",
		},
	}
	handlerCalls := 0
	router := NewRouter(RouterOptions{
		Config:                           config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                           slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementAPIAuthMiddleware:      NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware: NewManagementAPIAuthTouchMiddleware(authenticator),
		ManagementAPIKeyCreateHandler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			handlerCalls++
			writeData(w, http.StatusCreated, map[string]string{"id": "key_created"})
		}),
	})

	first := httptest.NewRequest(http.MethodPost, "/__aisys__/api/api-keys", strings.NewReader(`{"name":" 生产 Key ","routeStrategyId":"route_1"}`))
	first.Header.Set("Cookie", "juhe_ai_session=session-token")
	first.Header.Set("Content-Type", "application/json")
	firstRec := httptest.NewRecorder()
	router.ServeHTTP(firstRec, first)
	if firstRec.Code != http.StatusCreated {
		t.Fatalf("first status = %d, body = %s", firstRec.Code, firstRec.Body.String())
	}

	second := httptest.NewRequest(http.MethodPost, "/__aisys__/api/api-keys?systemAccountId=all", strings.NewReader(validManagementAPIKeyCreateBody()))
	second.Header.Set("Cookie", "juhe_ai_session=session-token")
	second.Header.Set("Content-Type", "application/json")
	secondRec := httptest.NewRecorder()
	router.ServeHTTP(secondRec, second)
	if secondRec.Code != http.StatusConflict {
		t.Fatalf("second status = %d, body = %s", secondRec.Code, secondRec.Body.String())
	}
	if handlerCalls != 1 {
		t.Fatalf("handler calls = %d, want 1", handlerCalls)
	}
}

func TestManagementAPIKeyCreateMutationGuardUsesSelfActorOwner(t *testing.T) {
	config := managementAPIKeyCreateMutationGuardConfig(managementAPIKeyScopeSelf)
	req := httptest.NewRequest(
		http.MethodPost,
		"/__aisys__/api/my-api-keys?systemAccountId=sys_forged",
		strings.NewReader(`{"name":" Key ","routeStrategyId":"route_1"}`),
	)
	req = requestWithManagementAPIKeyAuthContext(req, managementauth.Context{SystemAccountID: "sys_actor"})
	fingerprint, err := config.fingerprint(httptest.NewRecorder(), req)
	if err != nil {
		t.Fatalf("fingerprint error: %v", err)
	}
	got := fingerprint.(map[string]any)
	if got["owner"] != "sys_actor" || got["name"] != "Key" || config.operationKey != "api_keys.create" {
		t.Fatalf("config=%+v fingerprint=%+v", config, got)
	}
}

func TestRouterDoesNotRegisterManagementAPIKeyCreateWhenDisabled(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeData(w, http.StatusCreated, map[string]string{"id": "key_created"})
	})
	router := NewRouter(RouterOptions{
		Config:                          config.Config{Host: "127.0.0.1", Port: 3000},
		Logger:                          slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementAPIKeyCreateHandler:   handler,
		ManagementMyAPIKeyCreateHandler: handler,
	})
	for _, path := range []string{"/__aisys__/api/api-keys", "/__aisys__/api/my-api-keys"} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(validManagementAPIKeyCreateBody())))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404", path, rec.Code)
		}
	}
}

func TestRouterManagementAPIKeyCreateReusesManagementAuthFailures(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantStatus  int
		wantMessage string
		wantCode    string
	}{
		{
			name:        "unauthorized",
			err:         &managementauth.AuthError{StatusCode: http.StatusUnauthorized, Message: "未登录"},
			wantStatus:  http.StatusUnauthorized,
			wantMessage: "未登录",
		},
		{
			name:        "expired",
			err:         &managementauth.AuthError{StatusCode: http.StatusUnauthorized, Message: "登录已过期"},
			wantStatus:  http.StatusUnauthorized,
			wantMessage: "登录已过期",
		},
		{
			name: "must change password",
			err: &managementauth.AuthError{
				StatusCode: http.StatusForbidden,
				Code:       managementauth.ErrorCodeMustChangePassword,
				Message:    "请先修改初始密码",
			},
			wantStatus:  http.StatusForbidden,
			wantMessage: "请先修改初始密码",
			wantCode:    managementauth.ErrorCodeMustChangePassword,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authenticator := &managementAPIKeyRefreshAuthStub{}
			authenticatorError := &managementAPIAuthenticatorStub{err: test.err}
			handlerCalls := 0
			router := NewRouter(RouterOptions{
				Config:                           config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
				Logger:                           slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
				ManagementAPIAuthMiddleware:      NewManagementAPIAuthMiddleware(authenticator),
				ManagementAPIAuthTouchMiddleware: NewManagementAPIAuthTouchMiddleware(authenticatorError),
				ManagementAPIKeyCreateHandler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					handlerCalls++
					writeData(w, http.StatusCreated, map[string]string{"id": "key_created"})
				}),
			})
			req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/api-keys", strings.NewReader(validManagementAPIKeyCreateBody()))
			req.Header.Set("Cookie", "juhe_ai_session=session-token")
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if rec.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, test.wantStatus, rec.Body.String())
			}
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body["message"] != test.wantMessage || body["code"] != test.wantCode {
				t.Fatalf("body = %+v", body)
			}
			if handlerCalls != 0 {
				t.Fatalf("handler calls = %d", handlerCalls)
			}
		})
	}
}

func TestManagementAPIKeyCreateIsBusinessWriteRoute(t *testing.T) {
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	opts := RouterOptions{ManagementAPIKeyCreateHandler: handler}
	if !managementBusinessRoutesConfigured(opts) {
		t.Fatal("API Key create was not classified as management business route")
	}
	if !managementWriteRoutesConfigured(opts) {
		t.Fatal("API Key create was not classified as management write route")
	}
}

func validManagementAPIKeyCreateBody() string {
	return `{"name":"生产 Key","routeStrategyId":"route_1"}`
}

func managementAPIKeyCreateStringPtr(value string) *string {
	return &value
}

func timePtr(value time.Time) *time.Time {
	return &value
}

type managementAPIKeyCreateServiceStub struct {
	input  managementapikeys.CreateInput
	result managementapikeys.CreateResult
	err    error
	calls  int
}

func (s *managementAPIKeyCreateServiceStub) Create(
	_ *http.Request,
	input managementapikeys.CreateInput,
) (managementapikeys.CreateResult, error) {
	s.calls++
	s.input = input
	return s.result, s.err
}

var _ managementAPIKeyCreateService = (*managementAPIKeyCreateServiceStub)(nil)

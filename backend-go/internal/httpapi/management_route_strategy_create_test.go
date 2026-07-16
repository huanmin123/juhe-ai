package httpapi

import (
	"context"
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
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementroutestrategies"
	"juhe-ai/backend-go/internal/store/port"
)

func TestManagementRouteStrategyCreatePublicConstructorsMatchExpectedAPI(t *testing.T) {
	var adminConstructor func(
		*managementroutestrategies.Service,
		ManagementOperationLogOptions,
	) http.Handler = NewManagementRouteStrategyCreateHandlerWithOperationLog
	var selfConstructor func(
		*managementroutestrategies.Service,
		ManagementOperationLogOptions,
	) http.Handler = NewManagementMyRouteStrategyCreateHandlerWithOperationLog

	if adminConstructor == nil || selfConstructor == nil {
		t.Fatal("route strategy create constructors must be available")
	}
}

func TestManagementRouteStrategyCreateHandlerScopesOwnerAndReturnsCreatedData(t *testing.T) {
	tests := []struct {
		name             string
		scope            managementRouteStrategyOptionScope
		role             string
		path             string
		wantOwner        string
		wantIncludeOwner bool
		wantOwnerInBody  bool
	}{
		{
			name:             "admin missing owner uses actor",
			scope:            managementRouteStrategyScopeAdmin,
			role:             "admin",
			path:             "/__aisys__/api/route-strategies",
			wantOwner:        "sys_actor",
			wantIncludeOwner: true,
			wantOwnerInBody:  true,
		},
		{
			name:             "admin all owner uses actor",
			scope:            managementRouteStrategyScopeAdmin,
			role:             "super_admin",
			path:             "/__aisys__/api/route-strategies?systemAccountId=%20all%20",
			wantOwner:        "sys_actor",
			wantIncludeOwner: true,
			wantOwnerInBody:  true,
		},
		{
			name:             "admin explicit owner uses target",
			scope:            managementRouteStrategyScopeAdmin,
			role:             "admin",
			path:             "/__aisys__/api/route-strategies?systemAccountId=%20sys_target%20",
			wantOwner:        "sys_target",
			wantIncludeOwner: true,
			wantOwnerInBody:  true,
		},
		{
			name:            "self ignores forged owner query",
			scope:           managementRouteStrategyScopeSelf,
			role:            "user",
			path:            "/__aisys__/api/my-route-strategies?systemAccountId=sys_forged&systemAccountId=",
			wantOwner:       "sys_actor",
			wantOwnerInBody: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := routeStrategyCreateResult(tt.wantOwner)
			if !tt.wantIncludeOwner {
				result.SystemAccountID = ""
				result.SystemAccountName = ""
			}
			service := &managementRouteStrategyCreateServiceStub{
				result: result,
			}
			handler := newManagementRouteStrategyCreateHandler(
				service,
				tt.scope,
				managementOperationLogOptions{},
			)
			req := routeStrategyCreateRequest(
				tt.path,
				`{
					"name":" 新策略 ",
					"description":" 说明 ",
					"mode":"weighted",
					"status":"disabled",
					"groupBindings":[{
						"groupId":" group_1 ",
						"priority":2,
						"weight":75,
						"status":"active"
					}],
					"normalRoutingConfig":null,
					"hybridRoutingConfig":null
				}`,
				managementauth.Context{
					SystemAccountID: "sys_actor",
					Username:        "actor",
					Role:            tt.role,
				},
			)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201; body = %s", rec.Code, rec.Body.String())
			}
			if service.calls != 1 ||
				service.input.SystemAccountID != tt.wantOwner ||
				service.input.IncludeSystemAccountFields != tt.wantIncludeOwner ||
				service.input.Name != "新策略" ||
				service.input.Description == nil ||
				*service.input.Description != "说明" ||
				service.input.Mode != "weighted" ||
				!service.input.ModeSet ||
				service.input.Status != "disabled" ||
				!service.input.StatusSet ||
				len(service.input.GroupBindings) != 1 {
				t.Fatalf("service calls=%d input=%+v", service.calls, service.input)
			}
			binding := service.input.GroupBindings[0]
			if binding.GroupID != "group_1" ||
				binding.Priority != 2 ||
				!binding.PrioritySet ||
				binding.Weight != 75 ||
				!binding.WeightSet ||
				binding.Status != "active" ||
				!binding.StatusSet {
				t.Fatalf("binding = %+v", binding)
			}
			if !service.input.NormalRoutingConfig.Set() ||
				service.input.NormalRoutingConfig.Value() != nil ||
				!service.input.HybridRoutingConfig.Set() ||
				service.input.HybridRoutingConfig.Value() != nil {
				t.Fatalf(
					"config presence normal=(%v,%#v) hybrid=(%v,%#v)",
					service.input.NormalRoutingConfig.Set(),
					service.input.NormalRoutingConfig.Value(),
					service.input.HybridRoutingConfig.Set(),
					service.input.HybridRoutingConfig.Value(),
				)
			}

			var body struct {
				Data map[string]json.RawMessage `json:"data"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if string(body.Data["id"]) != `"route_created"` ||
				string(body.Data["name"]) != `"新策略"` {
				t.Fatalf("response data = %#v", body.Data)
			}
			_, ownerExists := body.Data["systemAccountId"]
			if ownerExists != tt.wantOwnerInBody {
				t.Fatalf("owner exists = %v, want %v; body = %s", ownerExists, tt.wantOwnerInBody, rec.Body.String())
			}
		})
	}
}

func TestManagementRouteStrategyCreateHandlerRejectsInvalidAdminOwnerQuery(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		wantMessage string
	}{
		{
			name:        "empty owner",
			path:        "/__aisys__/api/route-strategies?systemAccountId=",
			wantMessage: "查询参数不合法",
		},
		{
			name:        "repeated owner",
			path:        "/__aisys__/api/route-strategies?systemAccountId=sys_a&systemAccountId=sys_b",
			wantMessage: "查询参数不合法",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementRouteStrategyCreateServiceStub{}
			handler := newManagementRouteStrategyCreateHandler(
				service,
				managementRouteStrategyScopeAdmin,
				managementOperationLogOptions{},
			)
			req := routeStrategyCreateRequest(
				tt.path,
				validRouteStrategyCreateBody(),
				managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"},
			)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assertRouteStrategyCreateError(t, rec, http.StatusBadRequest, tt.wantMessage)
			if service.calls != 0 {
				t.Fatalf("service calls = %d, want 0", service.calls)
			}
		})
	}
}

func TestManagementRouteStrategyCreateHandlerPreservesDefaultsAndConfigPresence(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		wantNormalSet bool
		wantNormal    string
		wantHybridSet bool
		wantHybrid    string
	}{
		{
			name: "omitted configs remain absent",
			body: validRouteStrategyCreateBody(),
		},
		{
			name:          "explicit null configs remain present",
			body:          `{"name":"策略","groupBindings":[{"groupId":"group_1"}],"normalRoutingConfig":null,"hybridRoutingConfig":null}`,
			wantNormalSet: true,
			wantNormal:    "null",
			wantHybridSet: true,
			wantHybrid:    "null",
		},
		{
			name:          "normal config object is preserved",
			body:          `{"name":"策略","mode":"normal","groupBindings":[{"groupId":"group_1"}],"normalRoutingConfig":{"schedulingPreference":"speed_first","speedFirstConfig":{"slowTriggerCount":4}}}`,
			wantNormalSet: true,
			wantNormal:    `{"schedulingPreference":"speed_first","speedFirstConfig":{"slowTriggerCount":4}}`,
		},
		{
			name:          "hybrid config object is preserved",
			body:          `{"name":"策略","mode":"hybrid_smart","groupBindings":[{"groupId":"group_1"}],"normalRoutingConfig":null,"hybridRoutingConfig":{"scoringModel":"score-model","levelRoutes":[{"minLevel":1,"maxLevel":10,"targetModel":"gpt-5"}]}}`,
			wantNormalSet: true,
			wantNormal:    "null",
			wantHybridSet: true,
			wantHybrid:    `{"levelRoutes":[{"maxLevel":10,"minLevel":1,"targetModel":"gpt-5"}],"scoringModel":"score-model"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementRouteStrategyCreateServiceStub{
				result: routeStrategyCreateResult("sys_user"),
			}
			handler := newManagementRouteStrategyCreateHandler(
				service,
				managementRouteStrategyScopeSelf,
				managementOperationLogOptions{},
			)
			req := routeStrategyCreateRequest(
				"/__aisys__/api/my-route-strategies",
				tt.body,
				managementauth.Context{SystemAccountID: "sys_user", Role: "user"},
			)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusCreated || service.calls != 1 {
				t.Fatalf("status=%d calls=%d body=%s", rec.Code, service.calls, rec.Body.String())
			}
			if service.input.Mode == "" {
				if service.input.ModeSet ||
					service.input.Status != "" ||
					service.input.StatusSet ||
					len(service.input.GroupBindings) != 1 ||
					service.input.GroupBindings[0].Priority != 0 ||
					service.input.GroupBindings[0].PrioritySet ||
					service.input.GroupBindings[0].Weight != 0 ||
					service.input.GroupBindings[0].WeightSet ||
					service.input.GroupBindings[0].Status != "" ||
					service.input.GroupBindings[0].StatusSet {
					t.Fatalf("omitted defaults were changed before service: %+v", service.input)
				}
			}
			assertRouteStrategyCreateConfigInput(
				t,
				"normalRoutingConfig",
				service.input.NormalRoutingConfig,
				tt.wantNormalSet,
				tt.wantNormal,
			)
			assertRouteStrategyCreateConfigInput(
				t,
				"hybridRoutingConfig",
				service.input.HybridRoutingConfig,
				tt.wantHybridSet,
				tt.wantHybrid,
			)
		})
	}
}

func TestManagementRouteStrategyCreateHandlerUsesStrictNestedJSONAndIntegerStatusRules(t *testing.T) {
	validHybrid := `"hybridRoutingConfig":{"scoringModel":"score","levelRoutes":[{"minLevel":1,"maxLevel":10,"targetModel":"gpt"}]}`
	tests := []struct {
		name string
		body string
	}{
		{name: "top level array", body: `[]`},
		{name: "top level unknown field", body: `{"name":"策略","groupBindings":[{"groupId":"group_1"}],"unknown":true}`},
		{name: "missing name", body: `{"groupBindings":[{"groupId":"group_1"}]}`},
		{name: "missing group bindings", body: `{"name":"策略"}`},
		{name: "null group bindings", body: `{"name":"策略","groupBindings":null}`},
		{name: "too many group bindings", body: routeStrategyCreateBodyWithBindingCount(21)},
		{name: "description wrong type", body: `{"name":"策略","description":1,"groupBindings":[{"groupId":"group_1"}]}`},
		{name: "invalid mode", body: `{"name":"策略","mode":"random","groupBindings":[{"groupId":"group_1"}]}`},
		{name: "mode is not trimmed", body: `{"name":"策略","mode":" normal ","groupBindings":[{"groupId":"group_1"}]}`},
		{name: "invalid status", body: `{"name":"策略","status":"paused","groupBindings":[{"groupId":"group_1"}]}`},
		{name: "binding unknown field", body: `{"name":"策略","groupBindings":[{"groupId":"group_1","unknown":true}]}`},
		{name: "binding priority must be positive", body: `{"name":"策略","groupBindings":[{"groupId":"group_1","priority":0}]}`},
		{name: "binding priority must be integer", body: `{"name":"策略","groupBindings":[{"groupId":"group_1","priority":1.5}]}`},
		{name: "binding weight must be integer", body: `{"name":"策略","groupBindings":[{"groupId":"group_1","weight":1.5}]}`},
		{name: "binding weight range", body: `{"name":"策略","groupBindings":[{"groupId":"group_1","weight":101}]}`},
		{name: "binding status invalid", body: `{"name":"策略","groupBindings":[{"groupId":"group_1","status":"paused"}]}`},
		{name: "normal config unknown field", body: `{"name":"策略","groupBindings":[{"groupId":"group_1"}],"normalRoutingConfig":{"unknown":true}}`},
		{name: "speed config unknown field", body: `{"name":"策略","groupBindings":[{"groupId":"group_1"}],"normalRoutingConfig":{"speedFirstConfig":{"unknown":true}}}`},
		{name: "speed config integer", body: `{"name":"策略","groupBindings":[{"groupId":"group_1"}],"normalRoutingConfig":{"speedFirstConfig":{"slowTriggerCount":2.5}}}`},
		{name: "hybrid config unknown field", body: `{"name":"策略","mode":"hybrid_smart","groupBindings":[{"groupId":"group_1"}],` + strings.TrimSuffix(validHybrid, "}") + `,"unknown":true}}`},
		{name: "hybrid integer", body: `{"name":"策略","mode":"hybrid_smart","groupBindings":[{"groupId":"group_1"}],"hybridRoutingConfig":{"scoringModel":"score","scoringTimeoutMs":1000.5,"levelRoutes":[{"minLevel":1,"maxLevel":10,"targetModel":"gpt"}]}}`},
		{name: "level route unknown field", body: `{"name":"策略","mode":"hybrid_smart","groupBindings":[{"groupId":"group_1"}],"hybridRoutingConfig":{"scoringModel":"score","levelRoutes":[{"minLevel":1,"maxLevel":10,"targetModel":"gpt","unknown":true}]}}`},
		{name: "quality inspection unknown field", body: `{"name":"策略","mode":"hybrid_smart","groupBindings":[{"groupId":"group_1"}],"hybridRoutingConfig":{"scoringModel":"score","levelRoutes":[{"minLevel":1,"maxLevel":10,"targetModel":"gpt"}],"qualityInspection":{"enabled":true,"unknown":true}}}`},
		{name: "trailing json", body: validRouteStrategyCreateBody() + `{}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementRouteStrategyCreateServiceStub{}
			handler := newManagementRouteStrategyCreateHandler(
				service,
				managementRouteStrategyScopeSelf,
				managementOperationLogOptions{},
			)
			req := routeStrategyCreateRequest(
				"/__aisys__/api/my-route-strategies",
				tt.body,
				managementauth.Context{SystemAccountID: "sys_user", Role: "user"},
			)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
			}
			if service.calls != 0 {
				t.Fatalf("service calls = %d, want 0", service.calls)
			}
		})
	}
}

func TestManagementRouteStrategyCreateHandlerCountsDescriptionAsUTF16(t *testing.T) {
	tests := []struct {
		name        string
		description string
		wantStatus  int
		wantCalls   int
	}{
		{name: "200 ascii code units accepted", description: strings.Repeat("x", 200), wantStatus: http.StatusCreated, wantCalls: 1},
		{name: "201 ascii code units rejected", description: strings.Repeat("x", 201), wantStatus: http.StatusBadRequest},
		{name: "100 emoji are 200 UTF-16 code units", description: strings.Repeat("😀", 100), wantStatus: http.StatusCreated, wantCalls: 1},
		{name: "101 emoji exceed 200 UTF-16 code units", description: strings.Repeat("😀", 101), wantStatus: http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementRouteStrategyCreateServiceStub{
				result: routeStrategyCreateResult("sys_user"),
			}
			handler := newManagementRouteStrategyCreateHandler(
				service,
				managementRouteStrategyScopeSelf,
				managementOperationLogOptions{},
			)
			body, err := json.Marshal(map[string]any{
				"name":        "策略",
				"description": tt.description,
				"groupBindings": []map[string]any{{
					"groupId": "group_1",
				}},
			})
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
			req := routeStrategyCreateRequest(
				"/__aisys__/api/my-route-strategies",
				string(body),
				managementauth.Context{SystemAccountID: "sys_user", Role: "user"},
			)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus || service.calls != tt.wantCalls {
				t.Fatalf(
					"status=%d want=%d calls=%d wantCalls=%d body=%s",
					rec.Code,
					tt.wantStatus,
					service.calls,
					tt.wantCalls,
					rec.Body.String(),
				)
			}
		})
	}
}

func TestManagementRouteStrategyCreateHandlerEnforces256KiBBodyLimit(t *testing.T) {
	service := &managementRouteStrategyCreateServiceStub{}
	handler := newManagementRouteStrategyCreateHandler(
		service,
		managementRouteStrategyScopeSelf,
		managementOperationLogOptions{},
	)
	req := routeStrategyCreateRequest(
		"/__aisys__/api/my-route-strategies",
		`{"name":"`+strings.Repeat("x", 256<<10)+`","groupBindings":[{"groupId":"group_1"}]}`,
		managementauth.Context{SystemAccountID: "sys_user", Role: "user"},
	)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assertRouteStrategyCreateError(t, rec, http.StatusRequestEntityTooLarge, "请求体过大")
	if service.calls != 0 {
		t.Fatalf("service calls = %d, want 0", service.calls)
	}
}

func TestManagementRouteStrategyCreateHandlerMapsServiceErrors(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantStatus  int
		wantMessage string
	}{
		{
			name:        "validation",
			err:         &managementroutestrategies.ValidationError{Message: "路由策略模式无效"},
			wantStatus:  http.StatusBadRequest,
			wantMessage: "路由策略模式无效",
		},
		{
			name:        "duplicate name",
			err:         &managementroutestrategies.NameExistsError{Name: " 重复策略 "},
			wantStatus:  http.StatusConflict,
			wantMessage: "策略路由名称已存在：重复策略",
		},
		{
			name:        "internal is redacted",
			err:         errors.New("postgres password leaked"),
			wantStatus:  http.StatusInternalServerError,
			wantMessage: "服务器内部错误",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementRouteStrategyCreateServiceStub{err: tt.err}
			handler := newManagementRouteStrategyCreateHandler(
				service,
				managementRouteStrategyScopeSelf,
				managementOperationLogOptions{},
			)
			req := routeStrategyCreateRequest(
				"/__aisys__/api/my-route-strategies",
				validRouteStrategyCreateBody(),
				managementauth.Context{SystemAccountID: "sys_user", Role: "user"},
			)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assertRouteStrategyCreateError(t, rec, tt.wantStatus, tt.wantMessage)
			if strings.Contains(rec.Body.String(), "postgres") ||
				strings.Contains(rec.Body.String(), "password") {
				t.Fatalf("response leaked internal error: %s", rec.Body.String())
			}
		})
	}
}

func TestManagementRouteStrategyCreateHandlerWritesSixSafeOperationLogChanges(t *testing.T) {
	const forbiddenDescription = "description must not enter operation log"
	queueStub := &operationLogQueueStub{}
	result := routeStrategyCreateResult("sys_owner")
	result.Description = routeStrategyCreateStringPtr(forbiddenDescription)
	result.Mode = "normal"
	result.Status = "active"
	result.NormalRoutingConfig = &managementroutestrategies.NormalRoutingConfig{
		SchedulingPreference: "cost_first",
	}
	service := &managementRouteStrategyCreateServiceStub{result: result}
	handler := newManagementRouteStrategyCreateHandler(
		service,
		managementRouteStrategyScopeAdmin,
		newManagementOperationLogOptions(ManagementOperationLogOptions{
			Config:   config.Config{TrustProxy: "false"},
			Client:   queueStub,
			Now:      func() time.Time { return time.Date(2026, 7, 12, 8, 0, 0, 0, time.UTC) },
			NewLogID: func() string { return "oplog_route_create" },
		}),
	)
	req := routeStrategyCreateRequest(
		"/__aisys__/api/route-strategies?systemAccountId=sys_owner",
		validRouteStrategyCreateBody(),
		managementauth.Context{
			SystemAccountID: "sys_admin",
			Username:        "admin",
			DisplayName:     "管理员",
			Role:            "admin",
		},
	)
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "req_route_create"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", rec.Code, rec.Body.String())
	}
	if queueStub.calls != 1 {
		t.Fatalf("operation log calls = %d, want 1", queueStub.calls)
	}
	logInput, err := operationlogjob.DecodeWriteTaskPayload(queueStub.payload)
	if err != nil {
		t.Fatalf("decode operation log: %v", err)
	}
	if logInput.ID != "oplog_route_create" ||
		logInput.TraceID != "req_route_create" ||
		logInput.OperationScopeSystemAccountID != "sys_owner" ||
		logInput.Mode != "admin" ||
		logInput.Module != "route_strategies" ||
		logInput.Action != "create" ||
		logInput.OperationKey != "route_strategies.create" ||
		logInput.ResourceType != "route_strategy" ||
		logInput.ResourceID != "route_created" ||
		logInput.ResourceName != "新策略" ||
		logInput.Summary != "创建策略路由：新策略" ||
		len(logInput.Changes) != 6 {
		t.Fatalf("operation log = %+v", logInput)
	}
	wantFields := []string{
		"name",
		"mode",
		"status",
		"groupBindings",
		"normalRoutingConfig",
		"hybridRoutingConfig",
	}
	for index, field := range wantFields {
		if logInput.Changes[index].Field != field {
			t.Fatalf("change[%d] = %+v, want field %q", index, logInput.Changes[index], field)
		}
	}
	encoded, err := json.Marshal(logInput)
	if err != nil {
		t.Fatalf("marshal operation log: %v", err)
	}
	if strings.Contains(string(encoded), forbiddenDescription) ||
		strings.Contains(string(encoded), `"description"`) {
		t.Fatalf("operation log contains description: %s", encoded)
	}
}

func TestRouterRegistersManagementRouteStrategyCreateWithWriteAuthRateLimitsAndNoStore(t *testing.T) {
	readAuthenticator := &managementAPIKeyRefreshAuthStub{
		context: managementauth.Context{
			SystemAccountID: "sys_admin",
			Role:            "admin",
			SessionID:       "sess_read",
		},
	}
	touchAuthenticator := &managementAPIKeyRefreshAuthStub{
		context: managementauth.Context{
			SystemAccountID: "sys_admin",
			Role:            "admin",
			SessionID:       "sess_touch",
		},
	}
	ipLimiter := &managementAPIKeyRefreshIPLimiterStub{}
	userLimiter := &systemAPIAuthenticatedRateLimiterStub{
		decision: SystemAPIRateLimitDecision{Allowed: true},
	}
	handlerCalls := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerCalls++
		writeData(w, http.StatusCreated, map[string]string{"id": "route_created"})
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
		ManagementRouteStrategyCreateHandler:   handler,
		ManagementMyRouteStrategyCreateHandler: handler,
	})

	for index, path := range []string{
		"/__aisys__/api/route-strategies",
		"/__aisys__/api/my-route-strategies",
	} {
		body := `{"name":"新策略 ` + string(rune('A'+index)) + `","groupBindings":[{"groupId":"group_1"}]}`
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Cookie", "juhe_ai_session=session-token")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusCreated {
			t.Fatalf("%s status = %d, want 201; body = %s", path, rec.Code, rec.Body.String())
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s Cache-Control = %q, want no-store", path, rec.Header().Get("Cache-Control"))
		}
	}
	if readAuthenticator.readCalls != 0 ||
		touchAuthenticator.touchCalls != 2 ||
		ipLimiter.calls != 2 ||
		userLimiter.calls != 2 ||
		userLimiter.limit != 120 ||
		handlerCalls != 2 {
		t.Fatalf(
			"read=%d touch=%d ip=%d user=%d limit=%d handler=%d",
			readAuthenticator.readCalls,
			touchAuthenticator.touchCalls,
			ipLimiter.calls,
			userLimiter.calls,
			userLimiter.limit,
			handlerCalls,
		)
	}
}

func TestRouterManagementRouteStrategyCreateChecksAdminBeforeMutationGuard(t *testing.T) {
	authenticator := &managementAPIKeyRefreshAuthStub{
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
		Logger:                           slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementAPIAuthMiddleware:      NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware: NewManagementAPIAuthTouchMiddleware(authenticator),
		ManagementRouteStrategyCreateHandler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			handlerCalls++
			writeData(w, http.StatusCreated, map[string]string{"id": "route_created"})
		}),
	})

	for attempt := 1; attempt <= 2; attempt++ {
		rec := serveRouteStrategyCreateRouterRequest(
			router,
			"/__aisys__/api/route-strategies",
			validRouteStrategyCreateBody(),
		)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("attempt %d status = %d, want 403; body = %s", attempt, rec.Code, rec.Body.String())
		}
	}
	if handlerCalls != 0 {
		t.Fatalf("handler calls = %d, want 0", handlerCalls)
	}
}

func TestRouterManagementRouteStrategyCreateRejectsNonIdentityContentEncoding(t *testing.T) {
	authenticator := &managementAPIKeyRefreshAuthStub{
		context: managementauth.Context{
			SystemAccountID: "sys_admin",
			Role:            "admin",
			SessionID:       "sess_admin",
		},
	}
	handlerCalls := 0
	router := NewRouter(RouterOptions{
		Config: config.Config{
			Host:                 "127.0.0.1",
			Port:                 3000,
			ManagementAPIEnabled: true,
		},
		Logger:                           slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementAPIAuthMiddleware:      NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware: NewManagementAPIAuthTouchMiddleware(authenticator),
		ManagementRouteStrategyCreateHandler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			handlerCalls++
			writeData(w, http.StatusCreated, map[string]string{"id": "route_created"})
		}),
	})

	req := httptest.NewRequest(
		http.MethodPost,
		"/__aisys__/api/route-strategies",
		strings.NewReader(validRouteStrategyCreateBody()),
	)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	assertRouteStrategyCreateError(t, rec, http.StatusUnsupportedMediaType, "请求体无效")
	if handlerCalls != 0 || authenticator.touchCalls != 0 {
		t.Fatalf(
			"handler calls=%d auth touch calls=%d, want both 0",
			handlerCalls,
			authenticator.touchCalls,
		)
	}
}

func TestRouterManagementRouteStrategyCreateFingerprintsEffectiveOwnerAndTrimmedName(t *testing.T) {
	authenticator := &managementAPIKeyRefreshAuthStub{
		context: managementauth.Context{
			SystemAccountID: "sys_admin",
			Role:            "admin",
			SessionID:       "sess_admin",
		},
	}
	handlerCalls := 0
	router := NewRouter(RouterOptions{
		Config: config.Config{
			Host:                 "127.0.0.1",
			Port:                 3000,
			ManagementAPIEnabled: true,
		},
		Logger:                           slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementAPIAuthMiddleware:      NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware: NewManagementAPIAuthTouchMiddleware(authenticator),
		ManagementRouteStrategyCreateHandler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			handlerCalls++
			writeData(w, http.StatusCreated, map[string]string{"id": "route_created"})
		}),
	})

	first := serveRouteStrategyCreateRouterRequest(
		router,
		"/__aisys__/api/route-strategies",
		`{"name":" 新策略 ","groupBindings":[{"groupId":"group_1"}]}`,
	)
	if first.Code != http.StatusCreated {
		t.Fatalf("first status = %d, want 201; body = %s", first.Code, first.Body.String())
	}
	duplicate := serveRouteStrategyCreateRouterRequest(
		router,
		"/__aisys__/api/route-strategies?systemAccountId=all",
		validRouteStrategyCreateBody(),
	)
	if duplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate status = %d, want 409; body = %s", duplicate.Code, duplicate.Body.String())
	}
	differentOwner := serveRouteStrategyCreateRouterRequest(
		router,
		"/__aisys__/api/route-strategies?systemAccountId=sys_other",
		validRouteStrategyCreateBody(),
	)
	if differentOwner.Code != http.StatusCreated {
		t.Fatalf("different owner status = %d, want 201; body = %s", differentOwner.Code, differentOwner.Body.String())
	}
	if handlerCalls != 2 {
		t.Fatalf("handler calls = %d, want 2", handlerCalls)
	}
}

func TestRouterDoesNotRegisterManagementRouteStrategyCreateWhenDisabled(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeData(w, http.StatusCreated, map[string]string{"id": "route_created"})
	})
	router := NewRouter(RouterOptions{
		Config:                                 config.Config{Host: "127.0.0.1", Port: 3000},
		Logger:                                 slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementRouteStrategyCreateHandler:   handler,
		ManagementMyRouteStrategyCreateHandler: handler,
	})

	for _, path := range []string{
		"/__aisys__/api/route-strategies",
		"/__aisys__/api/my-route-strategies",
	} {
		rec := serveRouteStrategyCreateRouterRequest(router, path, validRouteStrategyCreateBody())
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404", path, rec.Code)
		}
	}
}

func TestManagementRouteStrategyCreateIsBusinessWriteRoute(t *testing.T) {
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	opts := RouterOptions{ManagementRouteStrategyCreateHandler: handler}
	if !managementBusinessRoutesConfigured(opts) {
		t.Fatal("route strategy create was not classified as management business route")
	}
	if !managementWriteRoutesConfigured(opts) {
		t.Fatal("route strategy create was not classified as management write route")
	}
}

func validRouteStrategyCreateBody() string {
	return `{"name":"新策略","groupBindings":[{"groupId":"group_1"}]}`
}

func routeStrategyCreateBodyWithBindingCount(count int) string {
	bindings := make([]map[string]any, 0, count)
	for index := 0; index < count; index++ {
		bindings = append(bindings, map[string]any{
			"groupId": "group_" + string(rune('a'+index)),
		})
	}
	body, _ := json.Marshal(map[string]any{
		"name":          "策略",
		"groupBindings": bindings,
	})
	return string(body)
}

func routeStrategyCreateRequest(
	path string,
	body string,
	authContext managementauth.Context,
) *http.Request {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	return requestWithManagementAuthContext(req, authContext)
}

func serveRouteStrategyCreateRouterRequest(
	router http.Handler,
	path string,
	body string,
) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func assertRouteStrategyCreateError(
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
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["message"] != wantMessage {
		t.Fatalf("message = %q, want %q", body["message"], wantMessage)
	}
}

func assertRouteStrategyCreateConfigInput(
	t *testing.T,
	name string,
	input managementroutestrategies.ConfigInput,
	wantSet bool,
	wantJSON string,
) {
	t.Helper()
	if input.Set() != wantSet {
		t.Fatalf("%s Set() = %v, want %v", name, input.Set(), wantSet)
	}
	if !wantSet {
		if input.Value() != nil {
			t.Fatalf("%s Value() = %#v, want nil", name, input.Value())
		}
		return
	}
	if input.Value() == nil {
		if wantJSON != "null" {
			t.Fatalf("%s Value() = nil, want %s", name, wantJSON)
		}
		return
	}
	encoded, err := json.Marshal(input.Value())
	if err != nil {
		t.Fatalf("marshal %s: %v", name, err)
	}
	if string(encoded) != wantJSON {
		t.Fatalf("%s JSON = %s, want %s", name, encoded, wantJSON)
	}
}

func routeStrategyCreateResult(ownerSystemAccountID string) managementroutestrategies.DetailResult {
	return managementroutestrategies.DetailResult{
		ID:                "route_created",
		SystemAccountID:   ownerSystemAccountID,
		SystemAccountName: "Owner",
		Name:              "新策略",
		Mode:              "weighted",
		Status:            "disabled",
		GroupBindings: []managementroutestrategies.GroupBindingSummary{{
			ID:           "binding_1",
			GroupID:      "group_1",
			GroupName:    "分组一",
			ProviderCode: "openai",
			Priority:     2,
			Weight:       75,
			Status:       "active",
			GroupEnabled: true,
		}},
		CreatedAt: "2026-07-12T08:00:00.000Z",
		UpdatedAt: "2026-07-12T08:00:00.000Z",
	}
}

func routeStrategyCreateStringPtr(value string) *string {
	return &value
}

type managementRouteStrategyCreateServiceStub struct {
	calls  int
	input  managementroutestrategies.CreateInput
	result managementroutestrategies.DetailResult
	err    error
}

func (s *managementRouteStrategyCreateServiceStub) Create(
	_ *http.Request,
	input managementroutestrategies.CreateInput,
) (managementroutestrategies.DetailResult, error) {
	s.calls++
	s.input = input
	return s.result, s.err
}

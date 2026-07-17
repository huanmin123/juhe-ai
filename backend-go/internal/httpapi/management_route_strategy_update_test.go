package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
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

func TestManagementMyRouteStrategyUpdateHandlerMapsActorIDAndPatch(t *testing.T) {
	service := &managementRouteStrategyUpdateServiceStub{
		result: managementroutestrategies.UpdateResult{
			RouteStrategy: managementroutestrategies.DetailResult{
				ID:     "route_1",
				Name:   "新策略",
				Mode:   "normal",
				Status: "active",
			},
			OwnerSystemAccountID: "sys_user",
		},
	}
	handler := newManagementRouteStrategyUpdateHandler(
		service,
		managementRouteStrategyScopeSelf,
		managementOperationLogOptions{},
	)
	req := httptest.NewRequest(
		http.MethodPatch,
		"/__aisys__/api/my-route-strategies/route_1",
		strings.NewReader(`{"name":" 新策略 "}`),
	)
	req = requestWithManagementRouteStrategyUpdateID(req, "route_1")
	req = requestWithManagementAuthContext(req, managementauth.Context{
		SystemAccountID: "sys_user",
		Role:            "user",
	})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if service.calls != 1 ||
		service.input.ActorSystemAccountID != "sys_user" ||
		service.input.ActorRole != "user" ||
		!service.input.SelfOnly ||
		service.input.RouteStrategyID != "route_1" ||
		!service.input.HasName ||
		service.input.Name != " 新策略 " {
		t.Fatalf("input = %+v; calls = %d", service.input, service.calls)
	}
	var body struct {
		Data managementroutestrategies.DetailResult `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Data.ID != "route_1" ||
		body.Data.Name != "新策略" {
		t.Fatalf("response route strategy = %+v", body.Data)
	}
}

func TestManagementRouteStrategyUpdateHandlerScopesAdminAndSelf(t *testing.T) {
	tests := []struct {
		name          string
		scope         managementRouteStrategyOptionScope
		path          string
		auth          managementauth.Context
		wantStatus    int
		wantCalls     int
		wantOwner     string
		wantSelfOnly  bool
		wantActorRole string
	}{
		{
			name:  "admin global when query missing",
			scope: managementRouteStrategyScopeAdmin,
			path:  "/__aisys__/api/route-strategies/route_1",
			auth: managementauth.Context{
				SystemAccountID: "sys_admin",
				Role:            "admin",
			},
			wantStatus:    http.StatusOK,
			wantCalls:     1,
			wantActorRole: "admin",
		},
		{
			name:  "admin global when query all",
			scope: managementRouteStrategyScopeAdmin,
			path:  "/__aisys__/api/route-strategies/route_1?systemAccountId=all",
			auth: managementauth.Context{
				SystemAccountID: "sys_admin",
				Role:            "admin",
			},
			wantStatus:    http.StatusOK,
			wantCalls:     1,
			wantActorRole: "admin",
		},
		{
			name:  "admin narrows owner",
			scope: managementRouteStrategyScopeAdmin,
			path:  "/__aisys__/api/route-strategies/route_1?systemAccountId=sys_owner",
			auth: managementauth.Context{
				SystemAccountID: "sys_admin",
				Role:            "admin",
			},
			wantStatus:    http.StatusOK,
			wantCalls:     1,
			wantOwner:     "sys_owner",
			wantActorRole: "admin",
		},
		{
			name:  "self ignores invalid owner query",
			scope: managementRouteStrategyScopeSelf,
			path:  "/__aisys__/api/my-route-strategies/route_1?systemAccountId=&systemAccountId=sys_other",
			auth: managementauth.Context{
				SystemAccountID: "sys_user",
				Role:            "user",
			},
			wantStatus:    http.StatusOK,
			wantCalls:     1,
			wantSelfOnly:  true,
			wantActorRole: "user",
		},
		{
			name:  "admin rejects empty owner",
			scope: managementRouteStrategyScopeAdmin,
			path:  "/__aisys__/api/route-strategies/route_1?systemAccountId=",
			auth: managementauth.Context{
				SystemAccountID: "sys_admin",
				Role:            "admin",
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:  "admin rejects repeated owner",
			scope: managementRouteStrategyScopeAdmin,
			path:  "/__aisys__/api/route-strategies/route_1?systemAccountId=a&systemAccountId=b",
			auth: managementauth.Context{
				SystemAccountID: "sys_admin",
				Role:            "admin",
			},
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementRouteStrategyUpdateServiceStub{
				result: routeStrategyUpdateResult(),
			}
			handler := newManagementRouteStrategyUpdateHandler(
				service,
				tt.scope,
				managementOperationLogOptions{},
			)
			req := httptest.NewRequest(http.MethodPatch, tt.path, strings.NewReader(`{"status":"disabled"}`))
			req = requestWithManagementRouteStrategyUpdateID(req, "route_1")
			req = requestWithManagementAuthContext(req, tt.auth)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus || service.calls != tt.wantCalls {
				t.Fatalf(
					"status=%d want=%d calls=%d want=%d body=%s",
					rec.Code,
					tt.wantStatus,
					service.calls,
					tt.wantCalls,
					rec.Body.String(),
				)
			}
			if service.calls == 1 &&
				(service.input.SystemAccountID != tt.wantOwner ||
					service.input.SelfOnly != tt.wantSelfOnly ||
					service.input.ActorRole != tt.wantActorRole) {
				t.Fatalf("input = %+v", service.input)
			}
		})
	}
}

func TestManagementRouteStrategyUpdateHandlerMapsAllPatchPresence(t *testing.T) {
	service := &managementRouteStrategyUpdateServiceStub{
		result: routeStrategyUpdateResult(),
	}
	handler := newManagementRouteStrategyUpdateHandler(
		service,
		managementRouteStrategyScopeSelf,
		managementOperationLogOptions{},
	)
	req := httptest.NewRequest(
		http.MethodPatch,
		"/__aisys__/api/my-route-strategies/route_1",
		strings.NewReader(`{
			"name":"新策略",
			"description":null,
			"mode":"weighted",
			"status":"disabled",
			"groupBindings":[{
				"groupId":"group_1",
				"priority":2,
				"weight":75,
				"status":"active"
			}],
			"normalRoutingConfig":null,
			"hybridRoutingConfig":{"scoringModel":"gpt-5"}
		}`),
	)
	req = requestWithManagementRouteStrategyUpdateID(req, "route_1")
	req = requestWithManagementAuthContext(req, managementauth.Context{
		SystemAccountID: "sys_user",
		Role:            "user",
	})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || service.calls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", rec.Code, service.calls, rec.Body.String())
	}
	input := service.input
	if !input.HasName || input.Name != "新策略" ||
		!input.HasDescription || input.Description != nil ||
		!input.HasMode || input.Mode != "weighted" ||
		!input.HasStatus || input.Status != "disabled" ||
		!input.HasGroupBindings || len(input.GroupBindings) != 1 {
		t.Fatalf("input = %+v", input)
	}
	binding := input.GroupBindings[0]
	if binding.GroupID != "group_1" ||
		!binding.PrioritySet || binding.Priority != 2 ||
		!binding.WeightSet || binding.Weight != 75 ||
		!binding.StatusSet || binding.Status != "active" {
		t.Fatalf("binding = %+v", binding)
	}
	assertRouteStrategyUpdateConfigInput(t, "normal", input.NormalRoutingConfig, true, "null")
	assertRouteStrategyUpdateConfigInput(
		t,
		"hybrid",
		input.HybridRoutingConfig,
		true,
		`{"scoringModel":"gpt-5"}`,
	)
}

func TestManagementRouteStrategyUpdateHandlerRejectsInvalidPartialBodies(t *testing.T) {
	tests := []string{
		``,
		`{}`,
		`null`,
		`[]`,
		`{"name":null}`,
		`{"description":1}`,
		`{"mode":false}`,
		`{"status":1}`,
		`{"groupBindings":null}`,
		`{"groupBindings":[{"groupId":1}]}`,
		`{"groupBindings":[{"groupId":"group_1","unknown":true}]}`,
		`{"normalRoutingConfig":[]}`,
		`{"hybridRoutingConfig":"bad"}`,
		`{"unknown":true}`,
		`{"name":"ok"} {"status":"active"}`,
	}

	for _, body := range tests {
		t.Run(body, func(t *testing.T) {
			service := &managementRouteStrategyUpdateServiceStub{}
			handler := newManagementRouteStrategyUpdateHandler(
				service,
				managementRouteStrategyScopeSelf,
				managementOperationLogOptions{},
			)
			req := httptest.NewRequest(
				http.MethodPatch,
				"/__aisys__/api/my-route-strategies/route_1",
				strings.NewReader(body),
			)
			req = requestWithManagementRouteStrategyUpdateID(req, "route_1")
			req = requestWithManagementAuthContext(req, managementauth.Context{
				SystemAccountID: "sys_user",
				Role:            "user",
			})
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest || service.calls != 0 {
				t.Fatalf("status=%d calls=%d body=%s", rec.Code, service.calls, rec.Body.String())
			}
		})
	}
}

func TestManagementRouteStrategyUpdateHandlerPreloadsBeforeSchema(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		prepareErr    error
		updateErr     error
		wantStatus    int
		wantMessage   string
		wantPrepare   int
		wantUpdate    int
		wantCallOrder []string
	}{
		{
			name:        "visible malformed config wins over invalid schema",
			body:        `{"mode":false}`,
			prepareErr:  &managementroutestrategies.ValidationError{Message: "现有策略路由配置无效：invalid character"},
			wantStatus:  http.StatusBadRequest,
			wantMessage: "现有策略路由配置无效：invalid character",
			wantPrepare: 1,
			wantCallOrder: []string{
				"prepare",
			},
		},
		{
			name:        "database preload error wins over invalid schema",
			body:        `{"mode":false}`,
			prepareErr:  errors.New("postgres preload password leaked"),
			wantStatus:  http.StatusInternalServerError,
			wantMessage: "服务器内部错误",
			wantPrepare: 1,
			wantCallOrder: []string{
				"prepare",
			},
		},
		{
			name:        "missing invalid schema stays bad request",
			body:        `{"mode":false}`,
			updateErr:   &managementroutestrategies.NotFoundError{RouteStrategyID: "route_missing"},
			wantStatus:  http.StatusBadRequest,
			wantMessage: "策略路由参数无效",
			wantPrepare: 1,
			wantCallOrder: []string{
				"prepare",
			},
		},
		{
			name:        "missing valid schema becomes not found",
			body:        `{"status":"disabled"}`,
			updateErr:   &managementroutestrategies.NotFoundError{RouteStrategyID: "route_missing"},
			wantStatus:  http.StatusNotFound,
			wantMessage: "策略路由不存在",
			wantPrepare: 1,
			wantUpdate:  1,
			wantCallOrder: []string{
				"prepare",
				"update",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementRouteStrategyUpdateServiceStub{
				prepareErr: tt.prepareErr,
				err:        tt.updateErr,
			}
			handler := newManagementRouteStrategyUpdateHandler(
				service,
				managementRouteStrategyScopeAdmin,
				managementOperationLogOptions{},
			)
			req := httptest.NewRequest(
				http.MethodPatch,
				"/__aisys__/api/route-strategies/route_missing?systemAccountId=sys_owner",
				strings.NewReader(tt.body),
			)
			req = requestWithManagementRouteStrategyUpdateID(req, "route_missing")
			req = requestWithManagementAuthContext(req, managementauth.Context{
				SystemAccountID: "sys_admin",
				Role:            "admin",
			})
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assertRouteStrategyCreateError(t, rec, tt.wantStatus, tt.wantMessage)
			if service.prepareCalls != tt.wantPrepare ||
				service.calls != tt.wantUpdate ||
				!reflect.DeepEqual(service.callOrder, tt.wantCallOrder) {
				t.Fatalf(
					"prepare=%d want=%d update=%d want=%d order=%v want=%v",
					service.prepareCalls,
					tt.wantPrepare,
					service.calls,
					tt.wantUpdate,
					service.callOrder,
					tt.wantCallOrder,
				)
			}
			if service.prepareCalls == 1 &&
				(service.prepareInput.ActorSystemAccountID != "sys_admin" ||
					service.prepareInput.ActorRole != "admin" ||
					service.prepareInput.SystemAccountID != "sys_owner" ||
					service.prepareInput.SelfOnly ||
					service.prepareInput.RouteStrategyID != "route_missing") {
				t.Fatalf("prepare input=%+v", service.prepareInput)
			}
			if strings.Contains(rec.Body.String(), "postgres") ||
				strings.Contains(rec.Body.String(), "password") {
				t.Fatalf("response leaked preload error: %s", rec.Body.String())
			}
		})
	}
}

func TestManagementRouteStrategyUpdateHandlerEnforcesDescriptionUTF16Limit(t *testing.T) {
	tests := []struct {
		name        string
		description string
		wantStatus  int
		wantCalls   int
	}{
		{
			name:        "200 ascii code units accepted",
			description: strings.Repeat("x", 200),
			wantStatus:  http.StatusOK,
			wantCalls:   1,
		},
		{
			name:        "201 ascii code units rejected",
			description: strings.Repeat("x", 201),
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "100 emoji are 200 UTF16 code units",
			description: strings.Repeat("😀", 100),
			wantStatus:  http.StatusOK,
			wantCalls:   1,
		},
		{
			name:        "101 emoji exceed 200 UTF16 code units",
			description: strings.Repeat("😀", 101),
			wantStatus:  http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementRouteStrategyUpdateServiceStub{
				result: routeStrategyUpdateResult(),
			}
			handler := newManagementRouteStrategyUpdateHandler(
				service,
				managementRouteStrategyScopeSelf,
				managementOperationLogOptions{},
			)
			body, err := json.Marshal(map[string]any{"description": tt.description})
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
			req := httptest.NewRequest(
				http.MethodPatch,
				"/__aisys__/api/my-route-strategies/route_1",
				strings.NewReader(string(body)),
			)
			req = requestWithManagementRouteStrategyUpdateID(req, "route_1")
			req = requestWithManagementAuthContext(req, managementauth.Context{
				SystemAccountID: "sys_user",
				Role:            "user",
			})
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus || service.calls != tt.wantCalls {
				t.Fatalf(
					"status=%d want=%d calls=%d want=%d body=%s",
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

func TestManagementRouteStrategyUpdateHandlerMapsServiceErrors(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantStatus  int
		wantMessage string
	}{
		{
			name:        "not found",
			err:         &managementroutestrategies.NotFoundError{RouteStrategyID: "route_missing"},
			wantStatus:  http.StatusNotFound,
			wantMessage: "策略路由不存在",
		},
		{
			name:        "duplicate name",
			err:         &managementroutestrategies.NameExistsError{Name: "重复策略"},
			wantStatus:  http.StatusConflict,
			wantMessage: "策略路由名称已存在：重复策略",
		},
		{
			name:        "validation",
			err:         &managementroutestrategies.ValidationError{Message: "路由策略状态无效"},
			wantStatus:  http.StatusBadRequest,
			wantMessage: "路由策略状态无效",
		},
		{
			name:        "internal redacted",
			err:         errors.New("postgres password leaked"),
			wantStatus:  http.StatusInternalServerError,
			wantMessage: "服务器内部错误",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementRouteStrategyUpdateServiceStub{err: tt.err}
			handler := newManagementRouteStrategyUpdateHandler(
				service,
				managementRouteStrategyScopeSelf,
				managementOperationLogOptions{},
			)
			req := httptest.NewRequest(
				http.MethodPatch,
				"/__aisys__/api/my-route-strategies/route_1",
				strings.NewReader(`{"status":"disabled"}`),
			)
			req = requestWithManagementRouteStrategyUpdateID(req, "route_1")
			req = requestWithManagementAuthContext(req, managementauth.Context{
				SystemAccountID: "sys_user",
				Role:            "user",
			})
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

func TestManagementRouteStrategyUpdateHandlerLogsOnlySevenActualSafeChanges(t *testing.T) {
	queue := &operationLogQueueStub{}
	beforeDescription := "旧说明"
	afterDescription := "新说明"
	before := managementroutestrategies.DetailResult{
		ID:          "route_1",
		Name:        "旧策略",
		Description: &beforeDescription,
		Mode:        "normal",
		Status:      "active",
		GroupBindings: []managementroutestrategies.GroupBindingSummary{{
			ID:       "binding_old",
			GroupID:  "group_1",
			Priority: 1,
			Weight:   1,
			Status:   "active",
		}},
		NormalRoutingConfig: &managementroutestrategies.NormalRoutingConfig{
			SchedulingPreference: "cost_first",
		},
	}
	after := managementroutestrategies.DetailResult{
		ID:          "route_1",
		Name:        "新策略",
		Description: &afterDescription,
		Mode:        "hybrid_smart",
		Status:      "disabled",
		GroupBindings: []managementroutestrategies.GroupBindingSummary{{
			ID:       "binding_new",
			GroupID:  "group_2",
			Priority: 2,
			Weight:   75,
			Status:   "active",
		}},
		HybridRoutingConfig: map[string]any{"scoringModel": "gpt-5"},
	}
	service := &managementRouteStrategyUpdateServiceStub{
		result: managementroutestrategies.UpdateResult{
			Before:               before,
			RouteStrategy:        after,
			OwnerSystemAccountID: "sys_owner",
		},
	}
	handler := newManagementRouteStrategyUpdateHandler(
		service,
		managementRouteStrategyScopeAdmin,
		newManagementOperationLogOptions(ManagementOperationLogOptions{
			Config:   config.Config{TrustProxy: "false"},
			Client:   queue,
			Now:      func() time.Time { return time.Date(2026, 7, 12, 9, 0, 0, 0, time.UTC) },
			NewLogID: func() string { return "oplog_route_update" },
		}),
	)
	req := httptest.NewRequest(
		http.MethodPatch,
		"/__aisys__/api/route-strategies/route_1?systemAccountId=sys_owner",
		strings.NewReader(`{"name":"新策略"}`),
	)
	req = requestWithManagementRouteStrategyUpdateID(req, "route_1")
	req = requestWithManagementAuthContext(req, managementauth.Context{
		SystemAccountID: "sys_admin",
		Username:        "admin",
		DisplayName:     "管理员",
		Role:            "admin",
	})
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "req_route_update"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || queue.calls != 1 {
		t.Fatalf("status=%d logs=%d body=%s", rec.Code, queue.calls, rec.Body.String())
	}
	logInput, err := operationlogjob.DecodeWriteTaskPayload(queue.payload)
	if err != nil {
		t.Fatalf("decode operation log: %v", err)
	}
	if logInput.ID != "oplog_route_update" ||
		logInput.TraceID != "req_route_update" ||
		logInput.OperationScopeSystemAccountID != "sys_owner" ||
		logInput.Mode != "admin" ||
		logInput.Module != "route_strategies" ||
		logInput.Action != "update" ||
		logInput.OperationKey != "route_strategies.update" ||
		logInput.ResourceType != "route_strategy" ||
		logInput.ResourceID != "route_1" ||
		logInput.ResourceName != "新策略" ||
		logInput.Summary != "更新策略路由：新策略" ||
		len(logInput.Changes) != 7 {
		t.Fatalf("operation log = %+v", logInput)
	}
	wantFields := []string{
		"name",
		"description",
		"mode",
		"status",
		"groupBindings",
		"normalRoutingConfig",
		"hybridRoutingConfig",
	}
	for index, field := range wantFields {
		if logInput.Changes[index].Field != field {
			t.Fatalf("change[%d] = %+v, want %q", index, logInput.Changes[index], field)
		}
	}
	if logInput.Changes[1].Before != beforeDescription ||
		logInput.Changes[1].After != afterDescription {
		t.Fatalf("description change = %+v, want plain text values", logInput.Changes[1])
	}

	queue = &operationLogQueueStub{}
	service.result.Before = after
	handler = newManagementRouteStrategyUpdateHandler(
		service,
		managementRouteStrategyScopeAdmin,
		newManagementOperationLogOptions(ManagementOperationLogOptions{
			Client:   queue,
			NewLogID: func() string { return "oplog_route_update_noop" },
		}),
	)
	req = httptest.NewRequest(
		http.MethodPatch,
		"/__aisys__/api/route-strategies/route_1",
		strings.NewReader(`{"name":"新策略"}`),
	)
	req = requestWithManagementRouteStrategyUpdateID(req, "route_1")
	req = requestWithManagementAuthContext(req, managementauth.Context{
		SystemAccountID: "sys_admin",
		Role:            "admin",
	})
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	logInput, err = operationlogjob.DecodeWriteTaskPayload(queue.payload)
	if err != nil {
		t.Fatalf("decode no-op log: %v", err)
	}
	if len(logInput.Changes) != 0 {
		t.Fatalf("no-op changes = %+v", logInput.Changes)
	}
}

func TestManagementRouteStrategyUpdateOperationLogProjectsBindingBusinessSemantics(t *testing.T) {
	binding := func(id string, weight int) managementroutestrategies.GroupBindingSummary {
		return managementroutestrategies.GroupBindingSummary{
			ID:           id,
			GroupID:      "group_1",
			GroupName:    "分组一",
			ProviderCode: "openai",
			Priority:     1,
			Weight:       weight,
			Status:       "active",
			GroupEnabled: true,
		}
	}
	base := managementroutestrategies.DetailResult{
		ID:            "route_1",
		Name:          "策略",
		Mode:          "normal",
		Status:        "active",
		GroupBindings: []managementroutestrategies.GroupBindingSummary{binding("binding_old", 1)},
	}

	t.Run("binding id rebuild is not a business change", func(t *testing.T) {
		queue := &operationLogQueueStub{}
		after := base
		after.GroupBindings = []managementroutestrategies.GroupBindingSummary{
			binding("binding_rebuilt", 1),
		}
		service := &managementRouteStrategyUpdateServiceStub{
			result: managementroutestrategies.UpdateResult{
				Before:               base,
				RouteStrategy:        after,
				OwnerSystemAccountID: "sys_owner",
			},
		}
		handler := newManagementRouteStrategyUpdateHandler(
			service,
			managementRouteStrategyScopeSelf,
			newManagementOperationLogOptions(ManagementOperationLogOptions{
				Client: queue,
			}),
		)
		req := httptest.NewRequest(
			http.MethodPatch,
			"/__aisys__/api/my-route-strategies/route_1",
			strings.NewReader(`{"name":"策略"}`),
		)
		req = requestWithManagementRouteStrategyUpdateID(req, "route_1")
		req = requestWithManagementAuthContext(req, managementauth.Context{
			SystemAccountID: "sys_owner",
			Role:            "user",
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
		if len(logInput.Changes) != 0 {
			t.Fatalf("binding ID-only changes=%+v, want none", logInput.Changes)
		}
	})

	t.Run("real binding change records stable projection without id", func(t *testing.T) {
		queue := &operationLogQueueStub{}
		after := base
		after.GroupBindings = []managementroutestrategies.GroupBindingSummary{
			binding("binding_rebuilt", 20),
		}
		service := &managementRouteStrategyUpdateServiceStub{
			result: managementroutestrategies.UpdateResult{
				Before:               base,
				RouteStrategy:        after,
				OwnerSystemAccountID: "sys_owner",
			},
		}
		handler := newManagementRouteStrategyUpdateHandler(
			service,
			managementRouteStrategyScopeSelf,
			newManagementOperationLogOptions(ManagementOperationLogOptions{
				Client: queue,
			}),
		)
		req := httptest.NewRequest(
			http.MethodPatch,
			"/__aisys__/api/my-route-strategies/route_1",
			strings.NewReader(`{"groupBindings":[{"groupId":"group_1","weight":20}]}`),
		)
		req = requestWithManagementRouteStrategyUpdateID(req, "route_1")
		req = requestWithManagementAuthContext(req, managementauth.Context{
			SystemAccountID: "sys_owner",
			Role:            "user",
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
		if len(logInput.Changes) != 1 ||
			logInput.Changes[0].Field != "groupBindings" {
			t.Fatalf("changes=%+v, want one groupBindings change", logInput.Changes)
		}
		for _, value := range []struct {
			name       string
			value      any
			wantWeight float64
		}{
			{name: "before", value: logInput.Changes[0].Before, wantWeight: 1},
			{name: "after", value: logInput.Changes[0].After, wantWeight: 20},
		} {
			var encoded []byte
			if text, ok := value.value.(string); ok {
				encoded = []byte(text)
			} else {
				encoded, err = json.Marshal(value.value)
				if err != nil {
					t.Fatalf("marshal %s projection: %v", value.name, err)
				}
			}
			if strings.Contains(string(encoded), `"id"`) {
				t.Fatalf("%s projection contains binding id: %s", value.name, encoded)
			}
			var bindings []map[string]any
			if err := json.Unmarshal(encoded, &bindings); err != nil {
				t.Fatalf("decode %s projection: %v; json=%s", value.name, err, encoded)
			}
			if len(bindings) != 1 ||
				len(bindings[0]) != 7 ||
				bindings[0]["groupId"] != "group_1" ||
				bindings[0]["groupName"] != "分组一" ||
				bindings[0]["providerCode"] != "openai" ||
				bindings[0]["priority"] != float64(1) ||
				bindings[0]["weight"] != value.wantWeight ||
				bindings[0]["status"] != "active" ||
				bindings[0]["groupEnabled"] != true {
				t.Fatalf("%s projection=%v", value.name, bindings)
			}
		}
	})
}

func TestRouterRegistersManagementRouteStrategyUpdateWithWriteBoundary(t *testing.T) {
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
		writeData(w, http.StatusOK, map[string]string{"id": "route_1"})
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
		ManagementRouteStrategyUpdateHandler:   handler,
		ManagementMyRouteStrategyUpdateHandler: handler,
	})

	for _, path := range []string{
		"/__aisys__/api/route-strategies/route_1",
		"/__aisys__/api/my-route-strategies/route_1",
	} {
		for attempt := 1; attempt <= 2; attempt++ {
			req := httptest.NewRequest(http.MethodPatch, path, strings.NewReader(`{"status":"disabled"}`))
			req.Header.Set("Cookie", "juhe_ai_session=session-token")
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK ||
				rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf(
					"%s attempt=%d status=%d cache=%q body=%s",
					path,
					attempt,
					rec.Code,
					rec.Header().Get("Cache-Control"),
					rec.Body.String(),
				)
			}
		}
	}
	if readAuthenticator.readCalls != 0 ||
		touchAuthenticator.touchCalls != 4 ||
		ipLimiter.calls != 4 ||
		userLimiter.calls != 4 ||
		userLimiter.limit != 120 ||
		handlerCalls != 4 {
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

func TestRouterManagementRouteStrategyUpdateAdminRoleAndSelfBoundary(t *testing.T) {
	authenticator := &managementAPIKeyRefreshAuthStub{
		context: managementauth.Context{
			SystemAccountID: "sys_user",
			Role:            "user",
			SessionID:       "sess_user",
		},
	}
	handlerCalls := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerCalls++
		writeData(w, http.StatusOK, map[string]string{"id": "route_1"})
	})
	router := NewRouter(RouterOptions{
		Config: config.Config{
			Host:                 "127.0.0.1",
			Port:                 3000,
			ManagementAPIEnabled: true,
		},
		Logger:                                 slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementAPIAuthMiddleware:            NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware:       NewManagementAPIAuthTouchMiddleware(authenticator),
		ManagementRouteStrategyUpdateHandler:   handler,
		ManagementMyRouteStrategyUpdateHandler: handler,
	})

	adminReq := httptest.NewRequest(
		http.MethodPatch,
		"/__aisys__/api/route-strategies/route_1",
		strings.NewReader(`{"status":"disabled"}`),
	)
	adminReq.Header.Set("Cookie", "juhe_ai_session=session-token")
	adminReq.Header.Set("Content-Type", "application/json")
	adminRec := httptest.NewRecorder()
	router.ServeHTTP(adminRec, adminReq)
	if adminRec.Code != http.StatusForbidden {
		t.Fatalf("admin route status=%d body=%s", adminRec.Code, adminRec.Body.String())
	}

	selfReq := httptest.NewRequest(
		http.MethodPatch,
		"/__aisys__/api/my-route-strategies/route_1",
		strings.NewReader(`{"status":"disabled"}`),
	)
	selfReq.Header.Set("Cookie", "juhe_ai_session=session-token")
	selfReq.Header.Set("Content-Type", "application/json")
	selfRec := httptest.NewRecorder()
	router.ServeHTTP(selfRec, selfReq)
	if selfRec.Code != http.StatusOK || handlerCalls != 1 {
		t.Fatalf("self status=%d calls=%d body=%s", selfRec.Code, handlerCalls, selfRec.Body.String())
	}
}

func TestRouterManagementRouteStrategyUpdateParsesTransportBeforeAuth(t *testing.T) {
	tests := []struct {
		name            string
		body            string
		contentEncoding string
		wantStatus      int
		wantMessage     string
	}{
		{
			name:        "malformed",
			body:        `{"status":`,
			wantStatus:  http.StatusBadRequest,
			wantMessage: "请求体无效",
		},
		{
			name:        "oversized",
			body:        `{"name":"` + strings.Repeat("x", managementGroupCreateMaxBodyBytes) + `"}`,
			wantStatus:  http.StatusRequestEntityTooLarge,
			wantMessage: "请求体过大",
		},
		{
			name:            "non identity content encoding",
			body:            `{"status":"disabled"}`,
			contentEncoding: "gzip",
			wantStatus:      http.StatusUnsupportedMediaType,
			wantMessage:     "请求体无效",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			authenticator := &managementAPIAuthenticatorStub{
				err: &managementauth.AuthError{
					StatusCode: http.StatusUnauthorized,
					Message:    "请先登录",
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
				ManagementRouteStrategyUpdateHandler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					handlerCalls++
					writeData(w, http.StatusOK, map[string]string{"id": "route_1"})
				}),
			})
			req := httptest.NewRequest(
				http.MethodPatch,
				"/__aisys__/api/route-strategies/route_1",
				strings.NewReader(tt.body),
			)
			req.Header.Set("Cookie", "juhe_ai_session=session-token")
			req.Header.Set("Content-Type", "application/json")
			if tt.contentEncoding != "" {
				req.Header.Set("Content-Encoding", tt.contentEncoding)
			}
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			assertRouteStrategyCreateError(t, rec, tt.wantStatus, tt.wantMessage)
			if authenticator.touchCookieHeader != "" || handlerCalls != 0 {
				t.Fatalf(
					"auth touch=%q handler=%d, want parser rejection first",
					authenticator.touchCookieHeader,
					handlerCalls,
				)
			}
		})
	}
}

func TestRouterDoesNotRegisterManagementRouteStrategyUpdateWhenDisabled(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeData(w, http.StatusOK, map[string]string{"id": "route_1"})
	})
	router := NewRouter(RouterOptions{
		Config:                                 config.Config{Host: "127.0.0.1", Port: 3000},
		Logger:                                 slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementRouteStrategyUpdateHandler:   handler,
		ManagementMyRouteStrategyUpdateHandler: handler,
	})

	for _, path := range []string{
		"/__aisys__/api/route-strategies/route_1",
		"/__aisys__/api/my-route-strategies/route_1",
	} {
		req := httptest.NewRequest(http.MethodPatch, path, strings.NewReader(`{"status":"disabled"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestManagementRouteStrategyUpdateIsBusinessWriteRoute(t *testing.T) {
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	opts := RouterOptions{ManagementRouteStrategyUpdateHandler: handler}
	if !managementBusinessRoutesConfigured(opts) {
		t.Fatal("route strategy update was not classified as management business route")
	}
	if !managementWriteRoutesConfigured(opts) {
		t.Fatal("route strategy update was not classified as management write route")
	}
}

func requestWithManagementRouteStrategyUpdateID(
	req *http.Request,
	id string,
) *http.Request {
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("id", id)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext))
}

func assertRouteStrategyUpdateConfigInput(
	t *testing.T,
	name string,
	input managementroutestrategies.ConfigInput,
	wantSet bool,
	wantJSON string,
) {
	t.Helper()
	if input.Set() != wantSet {
		t.Fatalf("%s Set()=%v, want %v", name, input.Set(), wantSet)
	}
	encoded, err := json.Marshal(input.Value())
	if err != nil {
		t.Fatalf("marshal %s config: %v", name, err)
	}
	if string(encoded) != wantJSON {
		t.Fatalf("%s config=%s, want %s", name, encoded, wantJSON)
	}
}

func routeStrategyUpdateResult() managementroutestrategies.UpdateResult {
	return managementroutestrategies.UpdateResult{
		Before: managementroutestrategies.DetailResult{
			ID:     "route_1",
			Name:   "旧策略",
			Mode:   "normal",
			Status: "active",
		},
		RouteStrategy: managementroutestrategies.DetailResult{
			ID:     "route_1",
			Name:   "新策略",
			Mode:   "normal",
			Status: "disabled",
		},
		OwnerSystemAccountID: "sys_user",
	}
}

type managementRouteStrategyUpdateServiceStub struct {
	prepareInput managementroutestrategies.UpdateInput
	prepareErr   error
	prepareCalls int
	input        managementroutestrategies.UpdateInput
	result       managementroutestrategies.UpdateResult
	err          error
	calls        int
	callOrder    []string
}

func (s *managementRouteStrategyUpdateServiceStub) PrepareUpdate(
	_ *http.Request,
	input managementroutestrategies.UpdateInput,
) error {
	s.prepareCalls++
	s.prepareInput = input
	s.callOrder = append(s.callOrder, "prepare")
	return s.prepareErr
}

func (s *managementRouteStrategyUpdateServiceStub) Update(
	_ *http.Request,
	input managementroutestrategies.UpdateInput,
) (managementroutestrategies.UpdateResult, error) {
	s.calls++
	s.input = input
	s.callOrder = append(s.callOrder, "update")
	return s.result, s.err
}

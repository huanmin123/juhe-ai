package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func TestManagementGroupDeleteHandlerBuildsAdminScopeAndBoundedOperationLog(t *testing.T) {
	now := time.Date(2026, time.July, 11, 11, 0, 0, 0, time.UTC)
	queueStub := &operationLogQueueStub{}
	strategies := make([]port.ManagementGroupDeletedRouteStrategy, 0, 21)
	for index := 0; index < 21; index++ {
		strategies = append(strategies, port.ManagementGroupDeletedRouteStrategy{
			ID:   fmt.Sprintf("route_%02d", index),
			Name: fmt.Sprintf("策略 %02d", index),
		})
	}
	service := &managementGroupDeleteServiceStub{
		result: managementgroups.DeleteResult{
			Before: port.ManagementGroupMutationSummary{
				ID:   "grp_delete",
				Name: "待删除分组",
			},
			OwnerSystemAccountID:    "sys_owner",
			AffectedRouteStrategies: strategies,
		},
	}
	handler := newManagementGroupDeleteHandler(
		service,
		managementGroupScopeAdmin,
		newManagementOperationLogOptions(ManagementOperationLogOptions{
			Config:   config.Config{TrustProxy: "false"},
			Client:   queueStub,
			Now:      func() time.Time { return now },
			NewLogID: func() string { return "oplog_group_delete" },
		}),
	)
	req := httptest.NewRequest(
		http.MethodDelete,
		"/__aisys__/api/groups/grp_delete?systemAccountId=%20sys_owner%20",
		nil,
	)
	req = requestWithManagementGroupDetailID(req, " grp/raw ")
	req = requestWithManagementAuthContext(req, managementauth.Context{
		SystemAccountID: "sys_admin",
		Username:        "admin",
		DisplayName:     "管理员",
		Role:            "super_admin",
	})
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "req_group_delete"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("status=%d body=%q, want empty 204", rec.Code, rec.Body.String())
	}
	if service.calls != 1 ||
		service.input.ActorSystemAccountID != "sys_admin" ||
		service.input.ActorRole != "super_admin" ||
		service.input.SystemAccountID != "sys_owner" ||
		service.input.SelfOnly ||
		service.input.GroupID != " grp/raw " {
		t.Fatalf("delete input=%+v calls=%d", service.input, service.calls)
	}

	logInput, err := operationlogjob.DecodeWriteTaskPayload(queueStub.payload)
	if err != nil {
		t.Fatalf("decode operation log: %v", err)
	}
	if logInput.ID != "oplog_group_delete" ||
		logInput.TraceID != "req_group_delete" ||
		logInput.ActorRole != "super_admin" ||
		logInput.OperationScopeSystemAccountID != "sys_owner" ||
		logInput.Mode != "admin" ||
		logInput.Module != "groups" ||
		logInput.Action != "delete" ||
		logInput.OperationKey != "groups.delete" ||
		logInput.ResourceType != "group" ||
		logInput.ResourceID != "grp_delete" ||
		logInput.ResourceName != "待删除分组" ||
		logInput.Summary != "删除分组：待删除分组" ||
		logInput.VisibilityScope != "targeted" ||
		logInput.StatusCode == nil ||
		*logInput.StatusCode != http.StatusNoContent ||
		len(logInput.Viewers) != 1 ||
		logInput.Viewers[0].SystemAccountID != "sys_owner" ||
		logInput.Viewers[0].VisibilityReason != "resource_owner" ||
		!logInput.CreatedAt.Equal(now) {
		t.Fatalf("operation log=%+v", logInput)
	}
	if len(logInput.Changes) != 2 ||
		logInput.Changes[0].Field != "deleted" ||
		logInput.Changes[0].Before != false ||
		logInput.Changes[0].After != true ||
		logInput.Changes[1].Field != "affectedRouteStrategies" ||
		logInput.Changes[1].After != "策略 00：移除分组 待删除分组；策略 01：移除分组 待删除分组；策略 02：移除分组 待删除分组；另有 18 个策略路由受影响" {
		t.Fatalf("operation log changes=%+v", logInput.Changes)
	}
	if len(logInput.Targets) != 20 {
		t.Fatalf("targets=%d, want 20", len(logInput.Targets))
	}
	for index, target := range logInput.Targets {
		if target.TargetType != "route_strategy" ||
			target.TargetID != strategies[index].ID ||
			target.TargetName != strategies[index].Name ||
			target.TargetOwnerSystemAccountID != "sys_owner" ||
			target.Relation != "affected" {
			t.Fatalf("target[%d]=%+v", index, target)
		}
	}
	if metadataCount := managementGroupDeleteMetadataCount(t, logInput.Metadata["affectedRouteStrategyCount"]); metadataCount != 21 {
		t.Fatalf("metadata count=%d, want 21", metadataCount)
	}
	metadataStrategies := decodeManagementGroupDeleteLogStrategies(
		t,
		logInput.Metadata["affectedRouteStrategies"],
	)
	if len(metadataStrategies) != 20 ||
		metadataStrategies[0].RouteStrategyID != "route_00" ||
		metadataStrategies[0].RouteStrategyName != "策略 00" ||
		metadataStrategies[0].RemovedGroupID != "grp_delete" ||
		metadataStrategies[0].RemovedGroupName != "待删除分组" ||
		metadataStrategies[19].RouteStrategyID != "route_19" ||
		metadataStrategies[19].RouteStrategyName != "策略 19" {
		t.Fatalf("metadata strategies=%+v", metadataStrategies)
	}
}

func TestManagementMyGroupDeleteHandlerForcesSelfScopeAndKeepsActorRole(t *testing.T) {
	queueStub := &operationLogQueueStub{}
	service := &managementGroupDeleteServiceStub{
		result: managementgroups.DeleteResult{
			Before: port.ManagementGroupMutationSummary{
				ID:   "grp_self",
				Name: "个人分组",
			},
			OwnerSystemAccountID: "sys_admin",
		},
	}
	handler := newManagementGroupDeleteHandler(
		service,
		managementGroupScopeSelf,
		newManagementOperationLogOptions(ManagementOperationLogOptions{
			Client:   queueStub,
			NewLogID: func() string { return "oplog_my_group_delete" },
		}),
	)
	req := httptest.NewRequest(
		http.MethodDelete,
		"/__aisys__/api/my-groups/grp_self?systemAccountId=&systemAccountId=sys_other",
		nil,
	)
	req = requestWithManagementGroupDetailID(req, "grp_self")
	req = requestWithManagementAuthContext(req, managementauth.Context{
		SystemAccountID: "sys_admin",
		Username:        "admin",
		Role:            "admin",
	})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent ||
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
		logInput.Summary != "删除分组：个人分组" ||
		len(logInput.Changes) != 1 ||
		len(logInput.Targets) != 0 ||
		logInput.Metadata != nil {
		t.Fatalf("operation log=%+v", logInput)
	}
}

func TestManagementGroupDeleteHandlerMapsStatusAndDoesNotLogFailures(t *testing.T) {
	tests := []struct {
		name       string
		scope      managementGroupOptionScope
		auth       managementauth.Context
		query      string
		serviceErr error
		wantStatus int
		wantText   string
	}{
		{
			name:       "unauthorized",
			scope:      managementGroupScopeSelf,
			wantStatus: http.StatusUnauthorized,
			wantText:   "未登录",
		},
		{
			name:       "admin forbidden",
			scope:      managementGroupScopeAdmin,
			auth:       managementauth.Context{SystemAccountID: "sys_user", Role: "user"},
			wantStatus: http.StatusForbidden,
			wantText:   "需要管理员权限",
		},
		{
			name:       "admin invalid repeated query",
			scope:      managementGroupScopeAdmin,
			auth:       managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"},
			query:      "?systemAccountId=sys_a&systemAccountId=sys_b",
			wantStatus: http.StatusBadRequest,
			wantText:   "Expected string, received array",
		},
		{
			name:       "not found",
			scope:      managementGroupScopeSelf,
			auth:       managementauth.Context{SystemAccountID: "sys_user", Role: "user"},
			serviceErr: managementgroups.ErrGroupNotFound,
			wantStatus: http.StatusNotFound,
			wantText:   "分组不存在",
		},
		{
			name:       "default group",
			scope:      managementGroupScopeSelf,
			auth:       managementauth.Context{SystemAccountID: "sys_user", Role: "user"},
			serviceErr: managementgroups.ErrGroupDefaultDelete,
			wantStatus: http.StatusBadRequest,
			wantText:   "默认分组不能删除",
		},
		{
			name:       "route guard",
			scope:      managementGroupScopeSelf,
			auth:       managementauth.Context{SystemAccountID: "sys_user", Role: "user"},
			serviceErr: &managementgroups.UpdateRejectedError{Message: "删除后策略路由将失去唯一可用分组"},
			wantStatus: http.StatusBadRequest,
			wantText:   "删除后策略路由将失去唯一可用分组",
		},
		{
			name:       "internal",
			scope:      managementGroupScopeSelf,
			auth:       managementauth.Context{SystemAccountID: "sys_user", Role: "user"},
			serviceErr: errors.New("postgres password leaked"),
			wantStatus: http.StatusInternalServerError,
			wantText:   "服务器内部错误",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			queueStub := &operationLogQueueStub{}
			service := &managementGroupDeleteServiceStub{err: tt.serviceErr}
			handler := newManagementGroupDeleteHandler(
				service,
				tt.scope,
				newManagementOperationLogOptions(ManagementOperationLogOptions{Client: queueStub}),
			)
			req := httptest.NewRequest(
				http.MethodDelete,
				"/__aisys__/api/groups/grp_1"+tt.query,
				nil,
			)
			req = requestWithManagementGroupDetailID(req, "grp_1")
			if tt.auth.SystemAccountID != "" {
				req = requestWithManagementAuthContext(req, tt.auth)
			}
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus || !strings.Contains(rec.Body.String(), tt.wantText) {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if queueStub.calls != 0 {
				t.Fatalf("operation log queue calls=%d, want 0", queueStub.calls)
			}
			if tt.serviceErr == nil && service.calls != 0 {
				t.Fatalf("service calls=%d, want 0 before service", service.calls)
			}
		})
	}
}

func TestManagementGroupDeleteHandlerKeepsSuccessWhenOperationLogFails(t *testing.T) {
	queueStub := &operationLogQueueStub{err: errors.New("redis down")}
	service := &managementGroupDeleteServiceStub{
		result: managementgroups.DeleteResult{
			Before:               port.ManagementGroupMutationSummary{ID: "grp_1", Name: "分组"},
			OwnerSystemAccountID: "sys_user",
		},
	}
	handler := newManagementGroupDeleteHandler(
		service,
		managementGroupScopeSelf,
		newManagementOperationLogOptions(ManagementOperationLogOptions{Client: queueStub}),
	)
	req := httptest.NewRequest(http.MethodDelete, "/__aisys__/api/my-groups/grp_1", nil)
	req = requestWithManagementGroupDetailID(req, "grp_1")
	req = requestWithManagementAuthContext(req, managementauth.Context{
		SystemAccountID: "sys_user",
		Role:            "user",
	})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 || queueStub.calls != 1 {
		t.Fatalf("status=%d body=%q queue calls=%d", rec.Code, rec.Body.String(), queueStub.calls)
	}
}

func TestRouterRegistersW5ManagementGroupDeleteWithWriteAuthAndRateLimits(t *testing.T) {
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
		ManagementGroupDeleteHandler:      handler,
		ManagementMyGroupDeleteHandler:    handler,
	})

	for _, path := range []string{
		"/__aisys__/api/groups/grp_1",
		"/__aisys__/api/my-groups/grp_1",
	} {
		req := httptest.NewRequest(http.MethodDelete, path, strings.NewReader(`{"malformed":`))
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
	if handlerCalls != 2 {
		t.Fatalf("handler calls=%d, want 2 without body parser or mutation guard", handlerCalls)
	}
	if touchAuthenticator.touchCookieHeader != "juhe_ai_session=session-token" {
		t.Fatalf("touch auth cookie=%q", touchAuthenticator.touchCookieHeader)
	}
	if readAuthenticator.cookieHeader != "" {
		t.Fatalf("read auth cookie=%q, want empty for DELETE", readAuthenticator.cookieHeader)
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

func TestRouterW5ManagementGroupDeleteAdminRouteRejectsOrdinaryUser(t *testing.T) {
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
		ManagementGroupDeleteHandler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			handlerCalls++
		}),
	})
	req := httptest.NewRequest(http.MethodDelete, "/__aisys__/api/groups/grp_1", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden ||
		!strings.Contains(rec.Body.String(), "需要管理员权限") ||
		handlerCalls != 0 {
		t.Fatalf("status=%d calls=%d body=%s", rec.Code, handlerCalls, rec.Body.String())
	}
}

func TestRouterDoesNotRegisterW5ManagementGroupDeleteWhenDisabled(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	router := NewRouter(RouterOptions{
		Config:                         config.Config{Host: "127.0.0.1", Port: 3000},
		ManagementGroupDeleteHandler:   handler,
		ManagementMyGroupDeleteHandler: handler,
	})

	for _, path := range []string{
		"/__aisys__/api/groups/grp_1",
		"/__aisys__/api/my-groups/grp_1",
	} {
		req := httptest.NewRequest(http.MethodDelete, path, nil)
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}
}

type managementGroupDeleteServiceStub struct {
	input  managementgroups.DeleteInput
	result managementgroups.DeleteResult
	err    error
	calls  int
}

func (s *managementGroupDeleteServiceStub) Delete(
	_ *http.Request,
	input managementgroups.DeleteInput,
) (managementgroups.DeleteResult, error) {
	s.calls++
	s.input = input
	return s.result, s.err
}

func decodeManagementGroupDeleteLogStrategies(
	t *testing.T,
	value any,
) []managementGroupDeletedRouteStrategyLogEntry {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal route strategies: %v", err)
	}
	if text, ok := value.(string); ok {
		data = []byte(text)
	}
	var strategies []managementGroupDeletedRouteStrategyLogEntry
	if err := json.Unmarshal(data, &strategies); err != nil {
		t.Fatalf("decode route strategies from %T: %v; value=%v", value, err, value)
	}
	return strategies
}

func managementGroupDeleteMetadataCount(t *testing.T, value any) int {
	t.Helper()
	switch count := value.(type) {
	case float64:
		return int(count)
	case int:
		return count
	default:
		t.Fatalf("metadata count type=%T value=%v", value, value)
		return 0
	}
}

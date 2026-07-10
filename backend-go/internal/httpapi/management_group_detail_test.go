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

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementgroups"
	"juhe-ai/backend-go/internal/store/port"
)

func TestManagementGroupDetailHandlerBuildsAdminScope(t *testing.T) {
	tests := []struct {
		name                string
		query               string
		wantSystemAccountID string
		wantStatus          int
		wantMessage         string
	}{
		{name: "missing is global", wantStatus: http.StatusOK},
		{name: "all is global", query: "?systemAccountId=%EF%BB%BFall%E2%80%83", wantStatus: http.StatusOK},
		{name: "target", query: "?systemAccountId=%EF%BB%BFsys_target%E2%80%A9", wantSystemAccountID: "sys_target", wantStatus: http.StatusOK},
		{name: "empty", query: "?systemAccountId=", wantStatus: http.StatusBadRequest, wantMessage: "系统账号 ID 不能为空"},
		{name: "blank", query: "?systemAccountId=%EF%BB%BF%E2%80%83", wantStatus: http.StatusBadRequest, wantMessage: "系统账号 ID 不能为空"},
		{name: "duplicate", query: "?systemAccountId=a&systemAccountId=b", wantStatus: http.StatusBadRequest, wantMessage: "Expected string, received array"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementGroupDetailServiceStub{
				result: managementgroups.DetailResult{ID: "grp_1", AccountIDs: []string{}},
			}
			handler := newManagementGroupDetailHandler(service, managementGroupScopeAdmin)
			req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/groups/grp_1"+tt.query, nil)
			req = requestWithManagementGroupDetailID(req, "grp_1")
			req = requestWithManagementAuthContext(req, managementauth.Context{
				SystemAccountID: "sys_admin",
				Role:            "admin",
			})
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.wantStatus != http.StatusOK {
				if service.calls != 0 || !strings.Contains(rec.Body.String(), tt.wantMessage) {
					t.Fatalf("service calls=%d body=%s", service.calls, rec.Body.String())
				}
				return
			}
			if service.calls != 1 ||
				service.input.ActorSystemAccountID != "sys_admin" ||
				service.input.ActorRole != "admin" ||
				service.input.SystemAccountID != tt.wantSystemAccountID ||
				service.input.SelfOnly ||
				service.input.GroupID != "grp_1" {
				t.Fatalf("service input = %+v calls=%d", service.input, service.calls)
			}
		})
	}
}

func TestManagementGroupDetailHandlerPreservesGroupIDAndNodeQueryErrorPriority(t *testing.T) {
	tests := []struct {
		name                string
		groupID             string
		query               string
		serviceErr          error
		wantStatus          int
		wantCalls           int
		wantGroupID         string
		wantSystemAccountID string
		wantMessage         string
	}{
		{
			name:                "path id remains exact",
			groupID:             " grp_1 ",
			query:               "?systemAccountId=sys_target",
			wantStatus:          http.StatusOK,
			wantCalls:           1,
			wantGroupID:         " grp_1 ",
			wantSystemAccountID: "sys_target",
		},
		{
			name:        "invalid query wins before missing path",
			groupID:     " missing ",
			query:       "?systemAccountId=%EF%BB%BF",
			serviceErr:  managementgroups.ErrGroupNotFound,
			wantStatus:  http.StatusBadRequest,
			wantMessage: "系统账号 ID 不能为空",
		},
		{
			name:                "non ecmascript whitespace remains a lookup",
			groupID:             " missing ",
			query:               "?systemAccountId=%C2%85",
			serviceErr:          managementgroups.ErrGroupNotFound,
			wantStatus:          http.StatusNotFound,
			wantCalls:           1,
			wantGroupID:         " missing ",
			wantSystemAccountID: "\u0085",
			wantMessage:         "分组不存在",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementGroupDetailServiceStub{
				result: managementgroups.DetailResult{ID: "grp_1", AccountIDs: []string{}},
				err:    tt.serviceErr,
			}
			handler := newManagementGroupDetailHandler(service, managementGroupScopeAdmin)
			req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/groups/value"+tt.query, nil)
			req = requestWithManagementGroupDetailID(req, tt.groupID)
			req = requestWithManagementAuthContext(req, managementauth.Context{
				SystemAccountID: "sys_admin",
				Role:            "admin",
			})
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus ||
				service.calls != tt.wantCalls ||
				(tt.wantMessage != "" && !strings.Contains(rec.Body.String(), tt.wantMessage)) {
				t.Fatalf("status=%d calls=%d input=%+v body=%s", rec.Code, service.calls, service.input, rec.Body.String())
			}
			if service.calls > 0 &&
				(service.input.GroupID != tt.wantGroupID ||
					service.input.SystemAccountID != tt.wantSystemAccountID) {
				t.Fatalf("service input = %+v", service.input)
			}
		})
	}
}

func TestManagementMyGroupDetailHandlerIgnoresSystemAccountQuery(t *testing.T) {
	service := &managementGroupDetailServiceStub{
		result: managementgroups.DetailResult{ID: "grp_1", AccountIDs: []string{}},
	}
	handler := newManagementGroupDetailHandler(service, managementGroupScopeSelf)
	req := httptest.NewRequest(
		http.MethodGet,
		"/__aisys__/api/my-groups/grp_1?systemAccountId=&systemAccountId=sys_other&unknown=value",
		nil,
	)
	req = requestWithManagementGroupDetailID(req, "grp_1")
	req = requestWithManagementAuthContext(req, managementauth.Context{
		SystemAccountID: "sys_current",
		Role:            "admin",
	})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK ||
		service.calls != 1 ||
		service.input.ActorSystemAccountID != "sys_current" ||
		service.input.SystemAccountID != "" ||
		!service.input.SelfOnly {
		t.Fatalf("status=%d input=%+v calls=%d body=%s", rec.Code, service.input, service.calls, rec.Body.String())
	}
}

func TestManagementGroupDetailHandlerChecksAdminBeforeQuery(t *testing.T) {
	service := &managementGroupDetailServiceStub{}
	handler := newManagementGroupDetailHandler(service, managementGroupScopeAdmin)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/groups/grp_1?systemAccountId=", nil)
	req = requestWithManagementGroupDetailID(req, "grp_1")
	req = requestWithManagementAuthContext(req, managementauth.Context{
		SystemAccountID: "sys_user",
		Role:            "user",
	})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden || service.calls != 0 {
		t.Fatalf("status=%d calls=%d body=%s", rec.Code, service.calls, rec.Body.String())
	}
}

func TestManagementGroupDetailHandlerMapsNotFoundAndRedactsErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantText   string
	}{
		{name: "not found", err: managementgroups.ErrGroupNotFound, wantStatus: http.StatusNotFound, wantText: "分组不存在"},
		{name: "internal", err: errors.New("postgres password leaked"), wantStatus: http.StatusInternalServerError, wantText: "服务器内部错误"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := newManagementGroupDetailHandler(
				&managementGroupDetailServiceStub{err: tt.err},
				managementGroupScopeSelf,
			)
			req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-groups/grp_1", nil)
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
			if strings.Contains(rec.Body.String(), "postgres") || strings.Contains(rec.Body.String(), "password") {
				t.Fatalf("response leaked internal error: %s", rec.Body.String())
			}
		})
	}
}

func TestManagementGroupDetailHandlerReturnsExactDetailShape(t *testing.T) {
	sources := []managementgroups.DetailAuthorizationSource{{
		ID:              "rasrc_1",
		AuthorizationID: "rauth_1",
		SourceType:      "manual",
		Status:          "active",
		CreatedBy:       "",
	}}
	service := &managementGroupDetailServiceStub{
		result: managementgroups.DetailResult{
			ID:                   "grp_1",
			OwnerSystemAccountID: "sys_owner",
			Name:                 "授权分组",
			ProviderCode:         "openai",
			GroupType:            "personal",
			AccountIDs:           []string{},
			AccessType:           "authorized",
			AuthorizationLimits:  port.ManagementRequestQuotaLimits{},
			AuthorizationSources: &sources,
		},
	}
	handler := newManagementGroupDetailHandler(service, managementGroupScopeSelf)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-groups/grp_1", nil)
	req = requestWithManagementGroupDetailID(req, "grp_1")
	req = requestWithManagementAuthContext(req, managementauth.Context{
		SystemAccountID: "sys_user",
		Role:            "user",
	})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	for _, required := range []string{"accountIds", "accountStats", "authorizationLimits", "authorizationSources", "permissions"} {
		if _, exists := body.Data[required]; !exists {
			t.Fatalf("response missing %q: %s", required, rec.Body.String())
		}
	}
	for _, forbidden := range []string{"runtimeSnapshot", "currentConcurrencyAvailable", "sourceTeamId", "revokedBy", "revokedAt"} {
		if strings.Contains(rec.Body.String(), forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, rec.Body.String())
		}
	}
}

func TestRouterRegistersManagementGroupDetailAsNoStoreLimitedReadRoutes(t *testing.T) {
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
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeData(w, http.StatusOK, map[string]string{"id": chi.URLParam(r, "id")})
	})
	router := NewRouter(RouterOptions{
		Config:                            config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                            slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		SystemAPIRateLimitReader:          systemAPIRateLimitReaderStub{settings: port.SystemAPIRateLimitSettings{IPReadPerMinute: 600, IPReadBurstPer10Seconds: 120, UserReadPerMinute: 300}},
		SystemAPIIPRateLimiter:            ipLimiter,
		SystemAPIAuthenticatedRateLimiter: userLimiter,
		ManagementAPIAuthMiddleware:       NewManagementAPIAuthMiddleware(readAuthenticator),
		ManagementAPIAuthTouchMiddleware:  NewManagementAPIAuthTouchMiddleware(touchAuthenticator),
		ManagementGroupDetailHandler:      handler,
		ManagementMyGroupDetailHandler:    handler,
	})

	for _, path := range []string{"/__aisys__/api/groups/grp_1", "/__aisys__/api/my-groups/grp_1"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Cookie", "juhe_ai_session=session-token")
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s status=%d cache=%q body=%s", path, rec.Code, rec.Header().Get("Cache-Control"), rec.Body.String())
		}
	}
	if ipLimiter.calls != 2 || userLimiter.calls != 2 || touchAuthenticator.touchCookieHeader != "" {
		t.Fatalf("ip calls=%d user calls=%d touch=%q", ipLimiter.calls, userLimiter.calls, touchAuthenticator.touchCookieHeader)
	}
}

type managementGroupDetailServiceStub struct {
	calls  int
	input  managementgroups.DetailInput
	result managementgroups.DetailResult
	err    error
}

func requestWithManagementGroupDetailID(req *http.Request, groupID string) *http.Request {
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("id", groupID)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext))
}

func (s *managementGroupDetailServiceStub) Detail(
	_ *http.Request,
	input managementgroups.DetailInput,
) (managementgroups.DetailResult, error) {
	s.calls++
	s.input = input
	return s.result, s.err
}

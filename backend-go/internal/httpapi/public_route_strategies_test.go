package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/modules/publicapi"
	publicapiratelimit "juhe-ai/backend-go/internal/modules/publicapi/ratelimit"
	"juhe-ai/backend-go/internal/modules/publicroutestrategies"
)

func TestPublicRouteStrategyHandlersAddThroughShell(t *testing.T) {
	service := &publicRouteStrategyServiceStub{
		addResponse: publicroutestrategies.RouteStrategyResponse{
			Source:      "stats",
			GeneratedAt: "2026-07-07T11:00:00Z",
			Action:      "created",
			Target:      publicroutestrategies.Target{Username: "admin", DisplayName: "Admin", SystemAccountID: "sys_admin"},
			RouteStrategy: &publicroutestrategies.RouteStrategySummary{
				ID: "rts_1", Name: "公开策略", Mode: publicroutestrategies.ModeNormal, Status: publicroutestrategies.StatusActive,
				GroupBindings: []publicroutestrategies.GroupBindingSummary{{ID: "rsg_1", GroupID: "grp_1", Priority: 1, Weight: 1, Status: publicroutestrategies.StatusActive, GroupEnabled: true}},
			},
		},
	}
	authenticator := newPublicGroupAPIAuthStub()
	limiter := &publicAPIShellLimiterStub{decision: publicapiratelimit.Decision{Allowed: true}}
	logQueue := &publicAPIShellLogQueueStub{}
	router := newTestPublicAPIShell(authenticator, limiter, logQueue, newPublicRouteStrategyHandlers(service), time.Date(2026, 7, 7, 11, 0, 0, 0, time.UTC))

	req := httptest.NewRequest(http.MethodPost, "/__aipublic__/route-strategy/add", strings.NewReader(`{"targetUsername":"admin","name":"公开策略","groupBindings":[{"groupId":"grp_1"}]}`))
	req.Header.Set("Authorization", "Bearer juis_plain")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if authenticator.scope != publicapi.ScopeRouteStrategyAddWrite || limiter.calls != 1 {
		t.Fatalf("auth scope/limiter = %q/%d", authenticator.scope, limiter.calls)
	}
	if service.addCalls != 1 || service.addInput.TargetUsername != "admin" || service.addInput.GroupBindings[0].Weight != 0 {
		t.Fatalf("add input = calls %d %+v", service.addCalls, service.addInput)
	}
	var body struct {
		Data publicroutestrategies.RouteStrategyResponse `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.Action != "created" || body.Data.RouteStrategy == nil || body.Data.RouteStrategy.ID != "rts_1" {
		t.Fatalf("body = %+v", body.Data)
	}
	log := singlePublicAPILog(t, logQueue)
	if log.StatusCode == nil || *log.StatusCode != http.StatusCreated || !log.Success {
		t.Fatalf("log status/success = %v/%v", log.StatusCode, log.Success)
	}
}

func TestPublicRouteStrategyHandlersRejectStrictAndNonCoercedFields(t *testing.T) {
	tests := []struct {
		name string
		path string
		body string
	}{
		{name: "add unknown field", path: "/__aipublic__/route-strategy/add", body: `{"targetUsername":"admin","name":"公开策略","groupBindings":[{"groupId":"grp_1"}],"extra":1}`},
		{name: "add string priority", path: "/__aipublic__/route-strategy/add", body: `{"targetUsername":"admin","name":"公开策略","groupBindings":[{"groupId":"grp_1","priority":"1"}]}`},
		{name: "add unknown binding field", path: "/__aipublic__/route-strategy/add", body: `{"targetUsername":"admin","name":"公开策略","groupBindings":[{"groupId":"grp_1","extra":1}]}`},
		{name: "update empty mutable", path: "/__aipublic__/route-strategy/update", body: `{"routeStrategyId":"rts_1"}`},
		{name: "delete unknown field", path: "/__aipublic__/route-strategy/del", body: `{"routeStrategyId":"rts_1","extra":1}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &publicRouteStrategyServiceStub{}
			router := newTestPublicAPIShell(
				newPublicGroupAPIAuthStub(),
				&publicAPIShellLimiterStub{decision: publicapiratelimit.Decision{Allowed: true}},
				&publicAPIShellLogQueueStub{},
				newPublicRouteStrategyHandlers(service),
				time.Date(2026, 7, 7, 11, 0, 0, 0, time.UTC),
			)

			req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body))
			req.Header.Set("Authorization", "Bearer juis_plain")
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if service.addCalls != 0 || service.updateCalls != 0 || service.deleteCalls != 0 {
				t.Fatalf("service calls = add %d update %d delete %d", service.addCalls, service.updateCalls, service.deleteCalls)
			}
		})
	}
}

func TestPublicRouteStrategyHandlersCoerceListPagination(t *testing.T) {
	service := &publicRouteStrategyServiceStub{listResponse: publicroutestrategies.RouteStrategyListResponse{
		Source:         "stats",
		GeneratedAt:    "2026-07-07T11:00:00Z",
		Target:         publicroutestrategies.Target{Username: "admin", DisplayName: "Admin", SystemAccountID: "sys_admin"},
		Page:           2,
		PageSize:       10,
		PageUpperBound: 11,
		Items:          []publicroutestrategies.RouteStrategySummary{},
	}}
	router := newTestPublicAPIShell(
		newPublicGroupAPIAuthStub(),
		&publicAPIShellLimiterStub{decision: publicapiratelimit.Decision{Allowed: true}},
		&publicAPIShellLogQueueStub{},
		newPublicRouteStrategyHandlers(service),
		time.Date(2026, 7, 7, 11, 0, 0, 0, time.UTC),
	)

	req := httptest.NewRequest(http.MethodGet, "/__aipublic__/route-strategy/list?targetUsername=admin&page=2&pageSize=10&mode=all&status=active", nil)
	req.Header.Set("Authorization", "Bearer juis_plain")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if service.listInput.Page != 2 || service.listInput.PageSize != 10 || service.listInput.Mode != "all" || service.listInput.Status != publicroutestrategies.StatusActive {
		t.Fatalf("list input = %+v", service.listInput)
	}
}

func TestPublicRouteStrategyHandlersMapServiceErrors(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		body       string
		configure  func(*publicRouteStrategyServiceStub)
		wantStatus int
		wantMsg    string
	}{
		{
			name: "update not found", path: "/__aipublic__/route-strategy/update", body: `{"routeStrategyId":"rts_1","name":"公开策略"}`,
			configure: func(stub *publicRouteStrategyServiceStub) {
				stub.updateErr = publicroutestrategies.ErrRouteStrategyNotFound
			},
			wantStatus: http.StatusNotFound, wantMsg: "路由策略不存在",
		},
		{
			name: "add target not found is bad request", path: "/__aipublic__/route-strategy/add", body: `{"targetUsername":"admin","name":"公开策略","groupBindings":[{"groupId":"grp_1"}]}`,
			configure: func(stub *publicRouteStrategyServiceStub) {
				stub.addErr = fmt.Errorf("%w: admin", publicroutestrategies.ErrTargetNotFound)
			},
			wantStatus: http.StatusBadRequest, wantMsg: "目标用户不存在：admin",
		},
		{
			name: "list target not found is 404", path: "/__aipublic__/route-strategy/list?targetUsername=admin", body: ``,
			configure: func(stub *publicRouteStrategyServiceStub) {
				stub.listErr = fmt.Errorf("%w: admin", publicroutestrategies.ErrTargetNotFound)
			},
			wantStatus: http.StatusNotFound, wantMsg: "目标用户不存在：admin",
		},
		{
			name: "duplicate", path: "/__aipublic__/route-strategy/update", body: `{"routeStrategyId":"rts_1","name":"公开策略"}`,
			configure: func(stub *publicRouteStrategyServiceStub) {
				stub.updateErr = fmt.Errorf("%w: 公开策略", publicroutestrategies.ErrDuplicateRouteStrategyName)
			},
			wantStatus: http.StatusConflict, wantMsg: "策略路由名称已存在：公开策略",
		},
		{
			name: "default delete", path: "/__aipublic__/route-strategy/del", body: `{"routeStrategyId":"rts_1"}`,
			configure: func(stub *publicRouteStrategyServiceStub) {
				stub.deleteErr = publicroutestrategies.ErrDefaultRouteStrategyDelete
			},
			wantStatus: http.StatusBadRequest, wantMsg: "默认策略路由不允许删除",
		},
		{
			name: "api keys in use", path: "/__aipublic__/route-strategy/del", body: `{"routeStrategyId":"rts_1"}`,
			configure: func(stub *publicRouteStrategyServiceStub) {
				stub.deleteErr = fmt.Errorf("%w: 2", publicroutestrategies.ErrRouteStrategyAPIKeysInUse)
			},
			wantStatus: http.StatusBadRequest, wantMsg: "策略路由已被 2 个 API Key 使用，请先解绑",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &publicRouteStrategyServiceStub{}
			tt.configure(service)
			router := newTestPublicAPIShell(
				newPublicGroupAPIAuthStub(),
				&publicAPIShellLimiterStub{decision: publicapiratelimit.Decision{Allowed: true}},
				&publicAPIShellLogQueueStub{},
				newPublicRouteStrategyHandlers(service),
				time.Date(2026, 7, 7, 11, 0, 0, 0, time.UTC),
			)

			method := http.MethodPost
			var reader *strings.Reader
			if tt.body == "" {
				method = http.MethodGet
				reader = strings.NewReader("")
			} else {
				reader = strings.NewReader(tt.body)
			}
			req := httptest.NewRequest(method, tt.path, reader)
			req.Header.Set("Authorization", "Bearer juis_plain")
			if tt.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			assertPublicGroupMessageError(t, rec, tt.wantStatus, tt.wantMsg)
		})
	}
}

func TestPublicRouteStrategyHandlersTestTokenSkipsService(t *testing.T) {
	authContext := publicAPIShellAuthContext()
	authContext.IsTestToken = true
	service := &publicRouteStrategyServiceStub{addErr: errors.New("should not call service")}
	router := newTestPublicAPIShell(
		&publicAPIShellAuthStub{ctx: authContext},
		&publicAPIShellLimiterStub{decision: publicapiratelimit.Decision{Allowed: true}},
		&publicAPIShellLogQueueStub{},
		newPublicRouteStrategyHandlers(service),
		time.Date(2026, 7, 7, 11, 0, 0, 0, time.UTC),
	)

	req := httptest.NewRequest(http.MethodPost, "/__aipublic__/route-strategy/add", strings.NewReader(`{"targetUsername":"admin","name":"公开策略","groupBindings":[{"groupId":"grp_1"}]}`))
	req.Header.Set("Authorization", "Bearer juis_test")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if service.addCalls != 0 {
		t.Fatalf("add calls = %d, want 0", service.addCalls)
	}
}

type publicRouteStrategyServiceStub struct {
	listInput   publicroutestrategies.ListInput
	addInput    publicroutestrategies.AddInput
	updateInput publicroutestrategies.UpdateInput
	deleteInput publicroutestrategies.DeleteInput

	listCalls   int
	addCalls    int
	updateCalls int
	deleteCalls int

	listResponse   publicroutestrategies.RouteStrategyListResponse
	addResponse    publicroutestrategies.RouteStrategyResponse
	updateResponse publicroutestrategies.RouteStrategyResponse
	deleteResponse publicroutestrategies.RouteStrategyResponse

	listErr   error
	addErr    error
	updateErr error
	deleteErr error
}

func (s *publicRouteStrategyServiceStub) List(_ *http.Request, input publicroutestrategies.ListInput) (publicroutestrategies.RouteStrategyListResponse, error) {
	s.listCalls++
	s.listInput = input
	return s.listResponse, s.listErr
}

func (s *publicRouteStrategyServiceStub) Add(_ *http.Request, input publicroutestrategies.AddInput) (publicroutestrategies.RouteStrategyResponse, error) {
	s.addCalls++
	s.addInput = input
	return s.addResponse, s.addErr
}

func (s *publicRouteStrategyServiceStub) Update(_ *http.Request, input publicroutestrategies.UpdateInput) (publicroutestrategies.RouteStrategyResponse, error) {
	s.updateCalls++
	s.updateInput = input
	return s.updateResponse, s.updateErr
}

func (s *publicRouteStrategyServiceStub) Delete(_ *http.Request, input publicroutestrategies.DeleteInput) (publicroutestrategies.RouteStrategyResponse, error) {
	s.deleteCalls++
	s.deleteInput = input
	return s.deleteResponse, s.deleteErr
}

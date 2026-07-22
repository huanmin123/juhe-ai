package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementmodelchecks"
	"juhe-ai/backend-go/internal/store/port"
)

func TestManagementModelCheckHandlersEnforceAdminAndSelfScope(t *testing.T) {
	tests := []struct {
		name                string
		scope               managementModelCheckScope
		role                string
		path                string
		wantStatus          int
		wantSystemAccountID string
		wantIncludeFields   bool
	}{
		{name: "admin global", scope: managementModelCheckScopeAdmin, role: "admin", path: "/__aisys__/api/model-checks/runs", wantStatus: 200, wantIncludeFields: true},
		{name: "super admin narrows", scope: managementModelCheckScopeAdmin, role: "super_admin", path: "/__aisys__/api/model-checks/runs?systemAccountId=%20sys_target%20", wantStatus: 200, wantSystemAccountID: "sys_target", wantIncludeFields: true},
		{name: "admin all is global", scope: managementModelCheckScopeAdmin, role: "admin", path: "/__aisys__/api/model-checks/runs?systemAccountId=%20all%20", wantStatus: 200, wantIncludeFields: true},
		{name: "admin rejects user", scope: managementModelCheckScopeAdmin, role: "user", path: "/__aisys__/api/model-checks/runs", wantStatus: 403},
		{name: "self forces current", scope: managementModelCheckScopeSelf, role: "user", path: "/__aisys__/api/my-model-checks/runs?systemAccountId=sys_forged", wantStatus: 200, wantSystemAccountID: "sys_actor"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &managementModelCheckServiceStub{listResult: managementmodelchecks.ListResult{Items: []managementmodelchecks.RunSummary{}}}
			handler := newManagementModelCheckListHandler(service, tt.scope)
			req := managementModelCheckRequest(tt.path, managementauth.Context{SystemAccountID: "sys_actor", Role: tt.role}, "")
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.wantStatus == 200 {
				if service.listCalls != 1 || service.listInput.SystemAccountID != tt.wantSystemAccountID || service.listInput.IncludeSystemAccountFields != tt.wantIncludeFields {
					t.Fatalf("list calls/input = %d %+v", service.listCalls, service.listInput)
				}
			} else if service.listCalls != 0 {
				t.Fatalf("service called on forbidden request")
			}
		})
	}
}

func TestManagementModelCheckListParsesFrontendFilters(t *testing.T) {
	service := &managementModelCheckServiceStub{listResult: managementmodelchecks.ListResult{Items: []managementmodelchecks.RunSummary{}}}
	handler := newManagementModelCheckListHandler(service, managementModelCheckScopeAdmin)
	req := managementModelCheckRequest("/__aisys__/api/model-checks/runs?page=2&pageSize=25&targetType=account&targetId=acct_1&model=gpt-5.6-sol&level=likely&status=completed&startAt=start&endAt=end", managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"}, "")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	want := managementmodelchecks.ListInput{
		IncludeSystemAccountFields: true, Page: 2, PageSize: 25, PageSizeProvided: true,
		TargetType: "account", TargetID: "acct_1", Model: "gpt-5.6-sol", Level: "likely", Status: "completed", StartAt: "start", EndAt: "end",
	}
	if rec.Code != 200 || !reflect.DeepEqual(service.listInput, want) {
		t.Fatalf("status=%d input=%+v want=%+v body=%s", rec.Code, service.listInput, want, rec.Body.String())
	}
}

func TestManagementModelCheckListUsesNodeParseIntSemantics(t *testing.T) {
	service := &managementModelCheckServiceStub{listResult: managementmodelchecks.ListResult{Items: []managementmodelchecks.RunSummary{}}}
	handler := newManagementModelCheckListHandler(service, managementModelCheckScopeAdmin)
	req := managementModelCheckRequest("/__aisys__/api/model-checks/runs?page=2rows&pageSize=25.9", managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"}, "")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != 200 || service.listInput.Page != 2 || service.listInput.PageSize != 25 || !service.listInput.PageSizeProvided {
		t.Fatalf("status=%d input=%+v body=%s", rec.Code, service.listInput, rec.Body.String())
	}
}

func TestManagementModelCheckActiveIsActorOwnedAndNullWhenAbsent(t *testing.T) {
	service := &managementModelCheckServiceStub{}
	handler := newManagementModelCheckActiveHandler(service, managementModelCheckScopeAdmin)
	req := managementModelCheckRequest("/__aisys__/api/model-checks/run/active?systemAccountId=sys_other", managementauth.Context{SystemAccountID: "sys_actor", Role: "admin"}, "")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != 200 || service.activeActorID != "sys_actor" || rec.Body.String() != "{\"data\":null}\n" {
		t.Fatalf("status=%d actor=%q body=%q", rec.Code, service.activeActorID, rec.Body.String())
	}
}

func TestManagementModelCheckStrictScopeQueryErrors(t *testing.T) {
	for _, path := range []string{
		"/__aisys__/api/model-checks/run/active?systemAccountId=",
		"/__aisys__/api/model-checks/runs/mcr_1?systemAccountId=a&systemAccountId=b",
	} {
		service := &managementModelCheckServiceStub{}
		var handler http.Handler
		if path == "/__aisys__/api/model-checks/run/active?systemAccountId=" {
			handler = newManagementModelCheckActiveHandler(service, managementModelCheckScopeAdmin)
		} else {
			handler = newManagementModelCheckDetailHandler(service, managementModelCheckScopeAdmin)
		}
		req := managementModelCheckRequest(path, managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"}, "mcr_1")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		assertModelCheckMessage(t, rec, 400, "查询参数不合法")
		if service.activeCalls != 0 || service.detailCalls != 0 {
			t.Fatalf("service called for invalid scope: active=%d detail=%d", service.activeCalls, service.detailCalls)
		}
	}
}

func TestManagementMyModelCheckDetailIgnoresForgedScopeAndMapsNotFound(t *testing.T) {
	service := &managementModelCheckServiceStub{}
	handler := newManagementModelCheckDetailHandler(service, managementModelCheckScopeSelf)
	req := managementModelCheckRequest("/__aisys__/api/my-model-checks/runs/missing?systemAccountId=", managementauth.Context{SystemAccountID: "sys_self", Role: "user"}, "missing")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assertModelCheckMessage(t, rec, 404, "模型检测记录不存在")
	if service.detailCalls != 1 || service.detailInput.ID != "missing" || service.detailInput.SystemAccountID != "sys_self" || service.detailInput.IncludeSystemAccountFields {
		t.Fatalf("detail input = %+v calls=%d", service.detailInput, service.detailCalls)
	}
}

func TestManagementModelCheckHandlersUseEnvelopeAndHideInternalErrors(t *testing.T) {
	optionsHandler := newManagementModelCheckOptionsHandler(&managementModelCheckServiceStub{}, managementModelCheckScopeSelf)
	optionsReq := managementModelCheckRequest("/__aisys__/api/my-model-checks/options", managementauth.Context{SystemAccountID: "sys_self", Role: "user"}, "")
	optionsRec := httptest.NewRecorder()
	optionsHandler.ServeHTTP(optionsRec, optionsReq)
	var envelope struct {
		Data managementmodelchecks.OptionsResult `json:"data"`
	}
	if optionsRec.Code != 200 || json.Unmarshal(optionsRec.Body.Bytes(), &envelope) != nil || envelope.Data.DefaultModel != "gpt-5.6-sol" {
		t.Fatalf("options status/body = %d %s", optionsRec.Code, optionsRec.Body.String())
	}

	listHandler := newManagementModelCheckListHandler(&managementModelCheckServiceStub{err: errors.New("sql secret")}, managementModelCheckScopeSelf)
	listReq := managementModelCheckRequest("/__aisys__/api/my-model-checks/runs", managementauth.Context{SystemAccountID: "sys_self", Role: "user"}, "")
	listRec := httptest.NewRecorder()
	listHandler.ServeHTTP(listRec, listReq)
	assertModelCheckMessage(t, listRec, 500, "服务器内部错误")
}

func TestManagementModelCheckHandlersRejectMissingAuthContext(t *testing.T) {
	handler := newManagementModelCheckOptionsHandler(&managementModelCheckServiceStub{}, managementModelCheckScopeSelf)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-model-checks/options", nil))
	assertModelCheckMessage(t, rec, 500, "服务器内部错误")
}

func TestRouterRegistersModelChecksAsNoStoreLimitedReadRoutes(t *testing.T) {
	authenticator := &managementAccountTestOptionsAuthenticator{authContext: managementauth.Context{SystemAccountID: "sys_admin", Role: "admin", SessionID: "sess_admin"}}
	ipLimiter := &managementAccountTestOptionsIPLimiter{decision: SystemAPIRateLimitDecision{Allowed: true}}
	userLimiter := &managementAccountTestOptionsUserLimiter{decision: SystemAPIRateLimitDecision{Allowed: true}}
	handlerCalls := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerCalls++
		w.WriteHeader(http.StatusNoContent)
	})
	opts := RouterOptions{
		Config: config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		SystemAPIRateLimitReader: managementAccountTestOptionsRateLimitReader{settings: port.SystemAPIRateLimitSettings{
			IPReadPerMinute: 600, IPReadBurstPer10Seconds: 120, UserReadPerMinute: 300,
		}},
		SystemAPIIPRateLimiter:               ipLimiter,
		SystemAPIAuthenticatedRateLimiter:    userLimiter,
		ManagementAPIAuthMiddleware:          NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware:     NewManagementAPIAuthTouchMiddleware(authenticator),
		ManagementModelCheckOptionsHandler:   handler,
		ManagementMyModelCheckOptionsHandler: handler,
		ManagementModelCheckActiveHandler:    handler,
		ManagementMyModelCheckActiveHandler:  handler,
		ManagementModelCheckListHandler:      handler,
		ManagementMyModelCheckListHandler:    handler,
		ManagementModelCheckDetailHandler:    handler,
		ManagementMyModelCheckDetailHandler:  handler,
	}
	if !managementBusinessRoutesConfigured(opts) || managementWriteRoutesConfigured(opts) {
		t.Fatal("model-check GET routes must be read-only management business routes")
	}
	router := NewRouter(opts)
	paths := []string{
		"/__aisys__/api/model-checks/options", "/__aisys__/api/my-model-checks/options",
		"/__aisys__/api/model-checks/run/active", "/__aisys__/api/my-model-checks/run/active",
		"/__aisys__/api/model-checks/runs", "/__aisys__/api/my-model-checks/runs",
		"/__aisys__/api/model-checks/runs/mcr_1", "/__aisys__/api/my-model-checks/runs/mcr_1",
	}
	for _, path := range paths {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Cookie", "juhe_ai_session=session-token")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s status/cache = %d %q; body=%s", path, rec.Code, rec.Header().Get("Cache-Control"), rec.Body.String())
		}
	}
	if handlerCalls != len(paths) || authenticator.readCalls != len(paths) || authenticator.touchCalls != 0 {
		t.Fatalf("handler/read/touch = %d/%d/%d", handlerCalls, authenticator.readCalls, authenticator.touchCalls)
	}
	if ipLimiter.calls != len(paths) || userLimiter.calls != len(paths) {
		t.Fatalf("IP/user limiter calls = %d/%d", ipLimiter.calls, userLimiter.calls)
	}
}

func TestRouterDoesNotRegisterModelChecksWhenManagementAPIDisabled(t *testing.T) {
	handlerCalls := 0
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { handlerCalls++ })
	router := NewRouter(RouterOptions{
		Config:                               config.Config{Host: "127.0.0.1", Port: 3000},
		ManagementModelCheckOptionsHandler:   handler,
		ManagementMyModelCheckOptionsHandler: handler,
		ManagementModelCheckActiveHandler:    handler,
		ManagementMyModelCheckActiveHandler:  handler,
		ManagementModelCheckListHandler:      handler,
		ManagementMyModelCheckListHandler:    handler,
		ManagementModelCheckDetailHandler:    handler,
		ManagementMyModelCheckDetailHandler:  handler,
	})
	for _, path := range []string{
		"/__aisys__/api/model-checks/options", "/__aisys__/api/my-model-checks/run/active",
		"/__aisys__/api/model-checks/runs", "/__aisys__/api/my-model-checks/runs/mcr_1",
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != 404 {
			t.Fatalf("%s status=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}
	if handlerCalls != 0 {
		t.Fatalf("disabled router called handlers %d times", handlerCalls)
	}
}

type managementModelCheckServiceStub struct {
	listResult    managementmodelchecks.ListResult
	activeResult  managementmodelchecks.ActiveRunSummary
	activeFound   bool
	detailResult  managementmodelchecks.RunDetail
	detailFound   bool
	err           error
	listInput     managementmodelchecks.ListInput
	activeActorID string
	detailInput   managementmodelchecks.DetailInput
	listCalls     int
	activeCalls   int
	detailCalls   int
}

func (s *managementModelCheckServiceStub) Options() managementmodelchecks.OptionsResult {
	return managementmodelchecks.Options()
}

func (s *managementModelCheckServiceStub) List(_ *http.Request, input managementmodelchecks.ListInput) (managementmodelchecks.ListResult, error) {
	s.listCalls++
	s.listInput = input
	return s.listResult, s.err
}

func (s *managementModelCheckServiceStub) Active(_ *http.Request, actorSystemAccountID string) (managementmodelchecks.ActiveRunSummary, bool, error) {
	s.activeCalls++
	s.activeActorID = actorSystemAccountID
	return s.activeResult, s.activeFound, s.err
}

func (s *managementModelCheckServiceStub) Detail(_ *http.Request, input managementmodelchecks.DetailInput) (managementmodelchecks.RunDetail, bool, error) {
	s.detailCalls++
	s.detailInput = input
	return s.detailResult, s.detailFound, s.err
}

func managementModelCheckRequest(path string, auth managementauth.Context, id string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	ctx := context.WithValue(req.Context(), managementAuthContextKey, auth)
	if id != "" {
		routeCtx := chi.NewRouteContext()
		routeCtx.URLParams.Add("id", id)
		ctx = context.WithValue(ctx, chi.RouteCtxKey, routeCtx)
	}
	return req.WithContext(ctx)
}

func assertModelCheckMessage(t *testing.T, rec *httptest.ResponseRecorder, status int, message string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, status, rec.Body.String())
	}
	var body struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Message != message {
		t.Fatalf("body=%s want message=%q err=%v", rec.Body.String(), message, err)
	}
}

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
	"juhe-ai/backend-go/internal/modules/publicgroups"
)

func TestPublicGroupHandlersAddThroughShell(t *testing.T) {
	groupService := &publicGroupServiceStub{
		addResponse: publicgroups.GroupResponse{
			Source:      "stats",
			GeneratedAt: "2026-07-07T10:00:00Z",
			Action:      "created",
			Target:      publicgroups.Target{Username: "admin", DisplayName: "Admin", SystemAccountID: "sys_admin", Created: false},
			Group:       &publicgroups.GroupSummary{ID: "grp_1", Name: "福利", ProviderCode: "gpt", Enabled: true, GroupType: "personal"},
		},
	}
	authenticator := newPublicGroupAPIAuthStub()
	limiter := &publicAPIShellLimiterStub{decision: publicapiratelimit.Decision{Allowed: true}}
	logQueue := &publicAPIShellLogQueueStub{}
	router := newTestPublicAPIShell(authenticator, limiter, logQueue, newPublicGroupHandlers(groupService), time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC))

	req := httptest.NewRequest(http.MethodPost, "/__aipublic__/group/add", strings.NewReader(`{"targetUsername":"admin","name":"福利","providerCode":"gpt"}`))
	req.Header.Set("Authorization", "Bearer juis_plain")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if authenticator.scope != publicapi.ScopeGroupAddWrite || limiter.calls != 1 {
		t.Fatalf("auth scope/limiter = %q/%d", authenticator.scope, limiter.calls)
	}
	if groupService.addCalls != 1 || groupService.addInput.TargetUsername != "admin" || groupService.addInput.GroupType != "" {
		t.Fatalf("add input = calls %d %+v", groupService.addCalls, groupService.addInput)
	}
	var body struct {
		Data publicgroups.GroupResponse `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.Action != "created" || body.Data.Group == nil || body.Data.Group.ID != "grp_1" {
		t.Fatalf("body = %+v", body.Data)
	}
	log := singlePublicAPILog(t, logQueue)
	if log.StatusCode == nil || *log.StatusCode != http.StatusCreated || !log.Success {
		t.Fatalf("log status/success = %v/%v", log.StatusCode, log.Success)
	}
}

func TestPublicGroupHandlersRejectStrictAndNonCoercedFields(t *testing.T) {
	tests := []struct {
		name string
		path string
		body string
	}{
		{name: "add unknown field", path: "/__aipublic__/group/add", body: `{"targetUsername":"admin","name":"福利","providerCode":"gpt","extra":1}`},
		{name: "add string bool", path: "/__aipublic__/group/add", body: `{"targetUsername":"admin","name":"福利","providerCode":"gpt","enabled":"true"}`},
		{name: "update unknown field", path: "/__aipublic__/group/update", body: `{"groupId":"grp_1","name":"福利","extra":1}`},
		{name: "update string bool", path: "/__aipublic__/group/update", body: `{"groupId":"grp_1","enabled":"true"}`},
		{name: "delete unknown field", path: "/__aipublic__/group/del", body: `{"groupId":"grp_1","extra":1}`},
		{name: "delete non string group id", path: "/__aipublic__/group/del", body: `{"groupId":123}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			groupService := &publicGroupServiceStub{}
			router := newTestPublicAPIShell(
				newPublicGroupAPIAuthStub(),
				&publicAPIShellLimiterStub{decision: publicapiratelimit.Decision{Allowed: true}},
				&publicAPIShellLogQueueStub{},
				newPublicGroupHandlers(groupService),
				time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC),
			)

			req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body))
			req.Header.Set("Authorization", "Bearer juis_plain")
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if groupService.addCalls != 0 {
				t.Fatalf("add calls = %d, want 0", groupService.addCalls)
			}
			if groupService.updateCalls != 0 {
				t.Fatalf("update calls = %d, want 0", groupService.updateCalls)
			}
			if groupService.deleteCalls != 0 {
				t.Fatalf("delete calls = %d, want 0", groupService.deleteCalls)
			}
		})
	}
}

func TestPublicGroupHandlersCoerceListPagination(t *testing.T) {
	groupService := &publicGroupServiceStub{listResponse: publicgroups.GroupListResponse{
		Source:         "stats",
		GeneratedAt:    "2026-07-07T10:00:00Z",
		Target:         publicgroups.Target{Username: "admin", DisplayName: "Admin", SystemAccountID: "sys_admin"},
		Page:           2,
		PageSize:       10,
		PageUpperBound: 11,
		Items:          []publicgroups.GroupSummary{},
	}}
	router := newTestPublicAPIShell(
		newPublicGroupAPIAuthStub(),
		&publicAPIShellLimiterStub{decision: publicapiratelimit.Decision{Allowed: true}},
		&publicAPIShellLogQueueStub{},
		newPublicGroupHandlers(groupService),
		time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC),
	)

	req := httptest.NewRequest(http.MethodGet, "/__aipublic__/group/list?targetUsername=admin&page=2&pageSize=10", nil)
	req.Header.Set("Authorization", "Bearer juis_plain")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if groupService.listInput.Page != 2 || groupService.listInput.PageSize != 10 {
		t.Fatalf("list input = %+v", groupService.listInput)
	}
}

func TestPublicGroupHandlersMapServiceErrors(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		body       string
		configure  func(*publicGroupServiceStub)
		wantStatus int
		wantMsg    string
	}{
		{
			name: "update not found",
			path: "/__aipublic__/group/update",
			body: `{"groupId":"grp_1","name":"福利"}`,
			configure: func(stub *publicGroupServiceStub) {
				stub.updateErr = publicgroups.ErrGroupNotFound
			},
			wantStatus: http.StatusNotFound,
			wantMsg:    "分组不存在",
		},
		{
			name: "update duplicate",
			path: "/__aipublic__/group/update",
			body: `{"groupId":"grp_1","name":"福利"}`,
			configure: func(stub *publicGroupServiceStub) {
				stub.updateErr = fmt.Errorf("%w: %s", publicgroups.ErrDuplicateGroupName, "福利")
			},
			wantStatus: http.StatusConflict,
			wantMsg:    "同一供应商下分组名称已存在：福利",
		},
		{
			name: "update default readonly",
			path: "/__aipublic__/group/update",
			body: `{"groupId":"grp_1","name":"福利"}`,
			configure: func(stub *publicGroupServiceStub) {
				stub.updateErr = publicgroups.ErrDefaultGroupReadonly
			},
			wantStatus: http.StatusBadRequest,
			wantMsg:    "默认分组不允许修改",
		},
		{
			name: "update provider has account",
			path: "/__aipublic__/group/update",
			body: `{"groupId":"grp_1","providerCode":"gpt"}`,
			configure: func(stub *publicGroupServiceStub) {
				stub.updateErr = publicgroups.ErrGroupProviderHasAccount
			},
			wantStatus: http.StatusBadRequest,
			wantMsg:    "已有账户的分组不允许修改供应商",
		},
		{
			name: "delete not found",
			path: "/__aipublic__/group/del",
			body: `{"groupId":"grp_1"}`,
			configure: func(stub *publicGroupServiceStub) {
				stub.deleteErr = publicgroups.ErrGroupNotFound
			},
			wantStatus: http.StatusNotFound,
			wantMsg:    "分组不存在",
		},
		{
			name: "delete default",
			path: "/__aipublic__/group/del",
			body: `{"groupId":"grp_1"}`,
			configure: func(stub *publicGroupServiceStub) {
				stub.deleteErr = publicgroups.ErrDefaultGroupDelete
			},
			wantStatus: http.StatusBadRequest,
			wantMsg:    "默认分组不能删除",
		},
		{
			name: "delete would lose route strategy group",
			path: "/__aipublic__/group/del",
			body: `{"groupId":"grp_1"}`,
			configure: func(stub *publicGroupServiceStub) {
				stub.deleteErr = fmt.Errorf("%w: 无法删除“福利”：该分组仍是活跃策略路由的唯一可用启用分组", publicgroups.ErrRouteStrategyWouldLose)
			},
			wantStatus: http.StatusBadRequest,
			wantMsg:    "无法删除“福利”：该分组仍是活跃策略路由的唯一可用启用分组",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			groupService := &publicGroupServiceStub{}
			tt.configure(groupService)
			router := newTestPublicAPIShell(
				newPublicGroupAPIAuthStub(),
				&publicAPIShellLimiterStub{decision: publicapiratelimit.Decision{Allowed: true}},
				&publicAPIShellLogQueueStub{},
				newPublicGroupHandlers(groupService),
				time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC),
			)

			req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body))
			req.Header.Set("Authorization", "Bearer juis_plain")
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			assertPublicGroupMessageError(t, rec, tt.wantStatus, tt.wantMsg)
		})
	}
}

func TestPublicGroupHandlersTestTokenSkipsService(t *testing.T) {
	authContext := publicAPIShellAuthContext()
	authContext.IsTestToken = true
	groupService := &publicGroupServiceStub{addErr: errors.New("should not call service")}
	router := newTestPublicAPIShell(
		&publicAPIShellAuthStub{ctx: authContext},
		&publicAPIShellLimiterStub{decision: publicapiratelimit.Decision{Allowed: true}},
		&publicAPIShellLogQueueStub{},
		newPublicGroupHandlers(groupService),
		time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC),
	)

	req := httptest.NewRequest(http.MethodPost, "/__aipublic__/group/add", strings.NewReader(`{"targetUsername":"admin","name":"福利","providerCode":"gpt"}`))
	req.Header.Set("Authorization", "Bearer juis_test")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if groupService.addCalls != 0 {
		t.Fatalf("add calls = %d, want 0", groupService.addCalls)
	}
}

type publicGroupServiceStub struct {
	listInput   publicgroups.ListInput
	addInput    publicgroups.AddInput
	updateInput publicgroups.UpdateInput
	deleteInput publicgroups.DeleteInput

	listCalls   int
	addCalls    int
	updateCalls int
	deleteCalls int

	listResponse   publicgroups.GroupListResponse
	addResponse    publicgroups.GroupResponse
	updateResponse publicgroups.GroupResponse
	deleteResponse publicgroups.GroupResponse

	listErr   error
	addErr    error
	updateErr error
	deleteErr error
}

func (s *publicGroupServiceStub) List(_ *http.Request, input publicgroups.ListInput) (publicgroups.GroupListResponse, error) {
	s.listCalls++
	s.listInput = input
	return s.listResponse, s.listErr
}

func (s *publicGroupServiceStub) Add(_ *http.Request, input publicgroups.AddInput) (publicgroups.GroupResponse, error) {
	s.addCalls++
	s.addInput = input
	return s.addResponse, s.addErr
}

func (s *publicGroupServiceStub) Update(_ *http.Request, input publicgroups.UpdateInput) (publicgroups.GroupResponse, error) {
	s.updateCalls++
	s.updateInput = input
	return s.updateResponse, s.updateErr
}

func (s *publicGroupServiceStub) Delete(_ *http.Request, input publicgroups.DeleteInput) (publicgroups.GroupResponse, error) {
	s.deleteCalls++
	s.deleteInput = input
	return s.deleteResponse, s.deleteErr
}

func newPublicGroupAPIAuthStub() *publicAPIShellAuthStub {
	ctx := publicAPIShellAuthContext()
	ctx.IsTestToken = false
	return &publicAPIShellAuthStub{ctx: ctx}
}

func assertPublicGroupMessageError(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int, wantMessage string) {
	t.Helper()

	if rec.Code != wantStatus {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Message != wantMessage {
		t.Fatalf("message = %q, want %q", body.Message, wantMessage)
	}
}

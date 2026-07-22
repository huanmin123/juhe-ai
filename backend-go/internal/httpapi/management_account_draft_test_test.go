package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/managementaccountbalance"
	"juhe-ai/backend-go/internal/modules/managementaccountdraft"
	"juhe-ai/backend-go/internal/modules/managementaccounttestdispatch"
	"juhe-ai/backend-go/internal/modules/managementauth"
)

func TestManagementAccountDraftTestHandlersMapAdminAndSelfScope(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		scope      managementAccountDraftScope
		role       string
		actor      string
		wantFilter string
	}{
		{name: "admin", path: "/accounts/test-draft?systemAccountId=owner_1", scope: managementAccountDraftScopeAdmin, role: "admin", actor: "admin_1", wantFilter: "owner_1"},
		{name: "self", path: "/my-accounts/test-draft?systemAccountId=other", scope: managementAccountDraftScopeSelf, role: "user", actor: "user_1", wantFilter: "user_1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &draftDispatchHTTPStub{task: managementaccounttestdispatch.Task{ID: "accttest_1", Status: "queued"}}
			handler := newManagementAccountDraftTestHandler(service, tt.scope)
			request := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(validDraftTestBody()))
			request = request.WithContext(context.WithValue(request.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: tt.actor, Role: tt.role}))
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, request)

			if recorder.Code != http.StatusAccepted || service.input.Access.FilterSystemAccountID != tt.wantFilter || service.input.Account.GroupID != "group_1" {
				t.Fatalf("status=%d input=%+v body=%s", recorder.Code, service.input, recorder.Body.String())
			}
		})
	}
}

func TestManagementAccountDraftTestHandlerRejectsUnknownNestedField(t *testing.T) {
	service := &draftDispatchHTTPStub{}
	handler := newManagementAccountDraftTestHandler(service, managementAccountDraftScopeSelf)
	request := httptest.NewRequest(http.MethodPost, "/my-accounts/test-draft", strings.NewReader(`{
		"account":{"providerCode":"openai","providerProtocolProfileId":"profile","name":"Draft","type":"api_key","credentials":{"api_key":"sk"},"healthCheckModel":"gpt","healthCheckEndpointMode":"chat_json","groupId":"group","status":"active"}
	}`))
	request = request.WithContext(context.WithValue(request.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "user_1", Role: "user"}))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest || service.called {
		t.Fatalf("status=%d called=%v body=%s", recorder.Code, service.called, recorder.Body.String())
	}
}

func TestManagementAccountBalanceDraftTestHandlerDoesNotRequireSavedAccountID(t *testing.T) {
	service := &draftBalanceHTTPStub{snapshot: managementaccountbalance.Snapshot{Status: "fresh", Balance: "10.50"}}
	handler := newManagementAccountBalanceDraftTestHandler(service, managementAccountDraftScopeSelf)
	request := httptest.NewRequest(http.MethodPost, "/my-accounts/balance/test-draft", strings.NewReader(`{
		"account":{"providerCode":"openai","providerProtocolProfileId":"profile","name":"Draft","type":"api_key","credentials":{"api_key":"sk"},"healthCheckModel":"gpt","healthCheckEndpointMode":"chat_json","groupId":"group"},
		"balanceQueryConfig":{"adapter":"builtin"}
	}`))
	request = request.WithContext(context.WithValue(request.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "user_1", Role: "user"}))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || service.input.Access.FilterSystemAccountID != "user_1" || service.input.Config.Adapter != "builtin" {
		t.Fatalf("status=%d input=%+v body=%s", recorder.Code, service.input, recorder.Body.String())
	}
}

func TestRouterRegistersFourAccountDraftTestRoutes(t *testing.T) {
	dispatch := &draftDispatchHTTPStub{task: managementaccounttestdispatch.Task{ID: "accttest_1", Status: "queued"}}
	balance := &draftBalanceHTTPStub{snapshot: managementaccountbalance.Snapshot{Status: "fresh"}}
	router := NewRouter(RouterOptions{
		Config:                                     config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                                     slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementAccountDraftTestHandler:          newManagementAccountDraftTestHandler(dispatch, managementAccountDraftScopeAdmin),
		ManagementMyAccountDraftTestHandler:        newManagementAccountDraftTestHandler(dispatch, managementAccountDraftScopeSelf),
		ManagementAccountBalanceDraftTestHandler:   newManagementAccountBalanceDraftTestHandler(balance, managementAccountDraftScopeAdmin),
		ManagementMyAccountBalanceDraftTestHandler: newManagementAccountBalanceDraftTestHandler(balance, managementAccountDraftScopeSelf),
		ManagementAPIAuthTouchMiddleware:           NewManagementAPIAuthTouchMiddleware(&managementAPIAuthenticatorStub{context: managementauth.Context{SystemAccountID: "admin_1", Role: "admin"}}),
		ManagementAPIAuthMiddleware:                NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{context: managementauth.Context{SystemAccountID: "admin_1", Role: "admin"}}),
	})
	tests := []struct {
		path string
		body string
		want int
	}{
		{path: "/__aisys__/api/accounts/test-draft?systemAccountId=owner_1", body: validDraftTestBody(), want: http.StatusAccepted},
		{path: "/__aisys__/api/my-accounts/test-draft", body: validDraftTestBody(), want: http.StatusAccepted},
		{path: "/__aisys__/api/accounts/balance/test-draft?systemAccountId=owner_1", body: validBalanceDraftBody(), want: http.StatusOK},
		{path: "/__aisys__/api/my-accounts/balance/test-draft", body: validBalanceDraftBody(), want: http.StatusOK},
	}
	for _, tt := range tests {
		request := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body))
		request.Header.Set("Cookie", "juhe_ai_session=session-token")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		if recorder.Code != tt.want {
			t.Fatalf("%s status=%d want=%d body=%s", tt.path, recorder.Code, tt.want, recorder.Body.String())
		}
	}
}

func validDraftTestBody() string {
	return `{"account":{"providerCode":"openai","providerProtocolProfileId":"profile","name":"Draft","type":"api_key","credentials":{"api_key":"sk"},"healthCheckModel":"gpt","healthCheckEndpointMode":"chat_json","groupId":"group_1"},"testSessionId":"session_1"}`
}

func validBalanceDraftBody() string {
	return `{"account":{"providerCode":"openai","providerProtocolProfileId":"profile","name":"Draft","type":"api_key","credentials":{"api_key":"sk"},"healthCheckModel":"gpt","healthCheckEndpointMode":"chat_json","groupId":"group_1"},"balanceQueryConfig":{"adapter":"builtin"}}`
}

type draftDispatchHTTPStub struct {
	input  managementaccounttestdispatch.DraftInput
	task   managementaccounttestdispatch.Task
	err    error
	called bool
}

func (s *draftDispatchHTTPStub) DispatchDraft(_ context.Context, input managementaccounttestdispatch.DraftInput) (managementaccounttestdispatch.Task, error) {
	s.called = true
	s.input = input
	return s.task, s.err
}

type draftBalanceHTTPStub struct {
	input    managementaccountbalance.DraftInput
	snapshot managementaccountbalance.Snapshot
	err      error
}

func (s *draftBalanceHTTPStub) TestDraft(_ context.Context, input managementaccountbalance.DraftInput) (managementaccountbalance.Snapshot, error) {
	s.input = input
	return s.snapshot, s.err
}

var _ = managementaccountdraft.ErrInvalid

package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/store/port"
)

func TestAccountTestSessionAdminAndSelfScopes(t *testing.T) {
	tests := []struct {
		name, path, role, actor, wantFilter string
		scope                               managementAccountTestScope
	}{{"admin", "/test-sessions?systemAccountId=owner", "admin", "admin", "owner", managementAccountTestScopeAdmin}, {"self", "/test-sessions?systemAccountId=other", "user", "self", "", managementAccountTestScopeSelf}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := &sessionHTTPStub{}
			req := httptest.NewRequest(http.MethodPost, tt.path, nil)
			req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: tt.actor, Role: tt.role}))
			rec := httptest.NewRecorder()
			newAccountTestSessionCreateHandler(stub, tt.scope).ServeHTTP(rec, req)
			if rec.Code != 201 || stub.access.ActorSystemAccountID != tt.actor || stub.access.FilterSystemAccountID != tt.wantFilter {
				t.Fatalf("status=%d access=%+v body=%s", rec.Code, stub.access, rec.Body.String())
			}
		})
	}
}
func TestAccountTestSessionCancelNotFound(t *testing.T) {
	stub := &sessionHTTPStub{found: false}
	req := testRouteRequest(http.MethodPost, "/test-sessions/missing/cancel", "sessionId", "missing")
	rec := httptest.NewRecorder()
	newAccountTestSessionMutationHandler(stub, managementAccountTestScopeAdmin, "cancel").ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

type sessionHTTPStub struct {
	access port.ManagementAccountTestAccess
	found  bool
}

func (s *sessionHTTPStub) Create(_ *http.Request, a port.ManagementAccountTestAccess) (port.ManagementAccountTestSession, error) {
	s.access = a
	return port.ManagementAccountTestSession{ID: "session", Status: "running"}, nil
}
func (s *sessionHTTPStub) Heartbeat(*http.Request, string, port.ManagementAccountTestAccess) (port.ManagementAccountTestSession, bool, error) {
	return port.ManagementAccountTestSession{}, s.found, nil
}
func (s *sessionHTTPStub) Complete(*http.Request, string, port.ManagementAccountTestAccess) (port.ManagementAccountTestSession, bool, error) {
	return port.ManagementAccountTestSession{}, s.found, nil
}
func (s *sessionHTTPStub) CancelSession(*http.Request, string, port.ManagementAccountTestAccess) (port.ManagementAccountTestSession, bool, error) {
	return port.ManagementAccountTestSession{}, s.found, nil
}
func (s *sessionHTTPStub) CancelTask(*http.Request, string, port.ManagementAccountTestAccess) (port.ManagementAccountTestTask, bool, error) {
	return port.ManagementAccountTestTask{}, s.found, nil
}
func testRouteRequest(method, path, key, value string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	route := chi.NewRouteContext()
	route.URLParams.Add(key, value)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, route)
	ctx = context.WithValue(ctx, managementAuthContextKey, managementauth.Context{SystemAccountID: "admin", Role: "admin"})
	return req.WithContext(ctx)
}

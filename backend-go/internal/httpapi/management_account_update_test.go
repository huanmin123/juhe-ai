package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"juhe-ai/backend-go/internal/modules/managementaccountupdate"
	"juhe-ai/backend-go/internal/modules/managementauth"
)

func TestManagementAccountUpdateHandlerRequiresRevisionAndMapsConflict(t *testing.T) {
	service := &managementAccountUpdateServiceStub{err: managementaccountupdate.ErrVersionConflict}
	handler := newManagementAccountUpdateHandler(service, managementAccountUpdateScopeSelf)
	req := httptest.NewRequest(http.MethodPatch, "/my-accounts/a", strings.NewReader(`{"name":"new"}`))
	req = managementAccountUpdateRequestWithAuth(req, "a", "owner", "user")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing revision status = %d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPatch, "/my-accounts/a", strings.NewReader(`{"configRevision":2,"name":"new"}`))
	req = managementAccountUpdateRequestWithAuth(req, "a", "owner", "user")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("conflict status = %d body=%s", rec.Code, rec.Body.String())
	}
}

type managementAccountUpdateServiceStub struct {
	input managementaccountupdate.UpdateInput
	err   error
}

func (s *managementAccountUpdateServiceStub) Update(_ *http.Request, input managementaccountupdate.UpdateInput) (managementaccountupdate.Result, error) {
	s.input = input
	return managementaccountupdate.Result{}, s.err
}

func managementAccountUpdateRequestWithAuth(req *http.Request, id, accountID, role string) *http.Request {
	route := chi.NewRouteContext()
	route.URLParams.Add("id", id)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, route)
	ctx = context.WithValue(ctx, managementAuthContextKey, managementauth.Context{SystemAccountID: accountID, Role: role})
	return req.WithContext(ctx)
}

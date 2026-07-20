package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"juhe-ai/backend-go/internal/modules/managementaccountcreate"
	"juhe-ai/backend-go/internal/modules/managementauth"
)

type accountCreateHTTPStub struct{ input managementaccountcreate.Input }

func (s *accountCreateHTTPStub) Create(_ *http.Request, input managementaccountcreate.Input) (map[string]any, error) {
	s.input = input
	return map[string]any{"id": "acct", "systemAccountId": input.SystemAccountID, "name": input.Name, "providerCode": input.ProviderCode, "status": "pending_test"}, nil
}

func TestManagementAccountCreateHandlerMapsBasicInput(t *testing.T) {
	service := &accountCreateHTTPStub{}
	handler := newManagementAccountCreateHandler(service, false)
	req := httptest.NewRequest(http.MethodPost, "/accounts?systemAccountId=owner", strings.NewReader(`{"providerCode":"openai","providerProtocolProfileId":"profile","name":"test","type":"api_key","credentials":{"api_key":"secret"},"supportedModels":["gpt-test"],"groupId":"group"}`))
	route := chi.NewRouteContext()
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
	req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "admin", Role: "admin"}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated || service.input.SystemAccountID != "owner" || service.input.GroupID != "group" || len(service.input.SupportedModels) != 1 {
		t.Fatalf("status=%d input=%+v body=%s", rec.Code, service.input, rec.Body.String())
	}
}

func TestManagementAccountCreateHandlerRequiresAdmin(t *testing.T) {
	handler := newManagementAccountCreateHandler(&accountCreateHTTPStub{}, false)
	req := httptest.NewRequest(http.MethodPost, "/accounts", strings.NewReader(`{}`)).WithContext(context.WithValue(context.Background(), managementAuthContextKey, managementauth.Context{SystemAccountID: "user", Role: "user"}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

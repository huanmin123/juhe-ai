package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	operationlogjob "juhe-ai/backend-go/internal/jobs/operationlog"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementexternalintegrationsources"
	"juhe-ai/backend-go/internal/store/port"
)

func TestManagementExternalIntegrationSourceTokenUpdateHandlerPresenceAndNarrowResponse(t *testing.T) {
	service := &managementExternalIntegrationSourceTokenUpdateServiceStub{result: managementexternalintegrationsources.TokenUpdateResult{
		Before: managementExternalIntegrationSourceTokenUpdateToken("before"),
		After:  managementExternalIntegrationSourceTokenUpdateToken("after"),
	}}
	handler := newManagementExternalIntegrationSourceTokenUpdateHandler(service, managementOperationLogOptions{})
	req := managementExternalIntegrationSourceTokenUpdateRequest(" source_1 ", " token_1 ", `{"name":" New ","status":"disabled","scopes":["ignored"],"expiresAt":null}`)
	req = requestWithManagementExternalIntegrationSourceAuthContext(req, managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Pragma") != "" {
		t.Fatalf("status=%d cache-control=%q pragma=%q", rec.Code, rec.Header().Get("Cache-Control"), rec.Header().Get("Pragma"))
	}
	var response struct {
		Data managementexternalintegrationsources.Token `json:"data"`
	}
	responseBody := rec.Body.String()
	if err := json.Unmarshal([]byte(responseBody), &response); err != nil || !reflect.DeepEqual(response.Data, service.result.After) {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	encoded := responseBody
	for _, forbidden := range []string{`"source"`, `"hash"`, `"token"`, `"encryptedToken"`, `"plaintext"`} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("response contains forbidden field %s: %s", forbidden, encoded)
		}
	}
	input := service.input
	if input.SourceID != " source_1 " || input.TokenID != " token_1 " || !input.HasName || input.Name != " New " ||
		!input.HasStatus || input.Status != "disabled" || !input.HasScopes || !input.HasExpiresAt || input.ExpiresAt != nil {
		t.Fatalf("service input=%#v", input)
	}
}

func TestManagementExternalIntegrationSourceTokenUpdateHandlerStrictBody(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{"unknown", `{"unknown":true}`}, {"array", `[]`}, {"trailing", `{} {}`},
		{"name type", `{"name":null}`}, {"status type", `{"status":1}`},
		{"scopes type", `{"scopes":null}`}, {"expires type", `{"expiresAt":1}`},
		{"too large", `{"name":"` + strings.Repeat("x", (256<<10)+1) + `"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &managementExternalIntegrationSourceTokenUpdateServiceStub{}
			handler := newManagementExternalIntegrationSourceTokenUpdateHandler(service, managementOperationLogOptions{})
			req := managementExternalIntegrationSourceTokenUpdateRequest("source_1", "token_1", test.body)
			req = requestWithManagementExternalIntegrationSourceAuthContext(req, managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"})
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			want := http.StatusBadRequest
			if test.name == "too large" {
				want = http.StatusRequestEntityTooLarge
			}
			if rec.Code != want || service.calls != 0 {
				t.Fatalf("status=%d want=%d calls=%d body=%s", rec.Code, want, service.calls, rec.Body.String())
			}
		})
	}
	service := &managementExternalIntegrationSourceTokenUpdateServiceStub{result: managementexternalintegrationsources.TokenUpdateResult{After: managementExternalIntegrationSourceTokenUpdateToken("after")}}
	handler := newManagementExternalIntegrationSourceTokenUpdateHandler(service, managementOperationLogOptions{})
	req := managementExternalIntegrationSourceTokenUpdateRequest("source_1", "token_1", `{}`)
	req = requestWithManagementExternalIntegrationSourceAuthContext(req, managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || service.calls != 1 {
		t.Fatalf("empty object status=%d calls=%d", rec.Code, service.calls)
	}
}

func TestManagementExternalIntegrationSourceTokenUpdateHandlerMapsErrorsAndEmptyIDs(t *testing.T) {
	tests := []struct {
		name          string
		source, token string
		err           error
		want          int
	}{
		{"empty source", "", "token_1", nil, http.StatusBadRequest},
		{"empty token", "source_1", "", nil, http.StatusBadRequest},
		{"validation", "source_1", "token_1", managementExternalIntegrationSourceTokenUpdateValidationError(), http.StatusBadRequest},
		{"built in", "source_1", "token_1", managementexternalintegrationsources.ErrBuiltInTokenUpdateRestricted, http.StatusBadRequest},
		{"built in before not found", "source_1", "token_1", errors.Join(managementexternalintegrationsources.ErrBuiltInTokenUpdateRestricted, managementexternalintegrationsources.ErrTokenNotFound), http.StatusBadRequest},
		{"not found", "source_1", "token_1", managementexternalintegrationsources.ErrTokenNotFound, http.StatusNotFound},
		{"unknown", "source_1", "token_1", errors.New("db down"), http.StatusInternalServerError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &managementExternalIntegrationSourceTokenUpdateServiceStub{err: test.err}
			handler := newManagementExternalIntegrationSourceTokenUpdateHandler(service, managementOperationLogOptions{})
			req := managementExternalIntegrationSourceTokenUpdateRequest(test.source, test.token, `{}`)
			req = requestWithManagementExternalIntegrationSourceAuthContext(req, managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"})
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != test.want {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, test.want, rec.Body.String())
			}
		})
	}
}

func TestManagementExternalIntegrationSourceTokenUpdateOperationLogIsSanitizedAndBestEffort(t *testing.T) {
	result := managementexternalintegrationsources.TokenUpdateResult{Before: managementExternalIntegrationSourceTokenUpdateToken("before"), After: managementExternalIntegrationSourceTokenUpdateToken("after")}
	queue := &operationLogQueueStub{}
	handler := newManagementExternalIntegrationSourceTokenUpdateHandler(&managementExternalIntegrationSourceTokenUpdateServiceStub{result: result}, newManagementOperationLogOptions(ManagementOperationLogOptions{Client: queue, NewLogID: func() string { return "oplog_token_update" }}))
	req := managementExternalIntegrationSourceTokenUpdateRequest("source_1", "token_1", `{}`)
	req = requestWithManagementExternalIntegrationSourceAuthContext(req, managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	logInput, err := operationlogjob.DecodeWriteTaskPayload(queue.payload)
	if err != nil {
		t.Fatal(err)
	}
	if logInput.Module != "external_integration_sources" || logInput.Action != "update_token" || logInput.OperationKey != "external_integration_sources.update_token" || logInput.ResourceID != "source_1" || logInput.ResourceName != "source_1" || logInput.Summary != "更新外部来源系统 Token：after" || logInput.DetailLevel != "full" || logInput.VisibilityScope != "admin_only" || len(logInput.Changes) != 3 {
		t.Fatalf("log=%#v", logInput)
	}
	for _, change := range logInput.Changes {
		if change.Field != "tokenName" && change.Field != "tokenStatus" && change.Field != "expiresAt" {
			t.Fatalf("unsafe change=%#v", change)
		}
		if change.Before != nil {
			t.Fatalf("change includes before value=%#v", change)
		}
		if change.Field == "tokenName" && change.After != "after" {
			t.Fatalf("name change=%#v", change)
		}
	}
	if strings.Contains(string(queue.payload), "secret") || strings.Contains(string(queue.payload), "credential") || strings.Contains(string(queue.payload), "scopes") {
		t.Fatalf("sensitive payload=%s", queue.payload)
	}
	failing := &operationLogQueueStub{err: errors.New("queue down")}
	handler = newManagementExternalIntegrationSourceTokenUpdateHandler(&managementExternalIntegrationSourceTokenUpdateServiceStub{result: result}, newManagementOperationLogOptions(ManagementOperationLogOptions{Client: failing}))
	req = managementExternalIntegrationSourceTokenUpdateRequest("source_1", "token_1", `{}`)
	req = requestWithManagementExternalIntegrationSourceAuthContext(req, managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"})
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("log failure status=%d", rec.Code)
	}
}

func TestManagementExternalIntegrationSourceTokenUpdateMutationGuardFingerprintUsesRawFieldsWithoutScopes(t *testing.T) {
	req := managementExternalIntegrationSourceTokenUpdateRequest(" raw source ", " raw token ", `{"name":" raw name ","status":"disabled","scopes":["secret"],"expiresAt":" raw expiry "}`)
	guardConfig := managementExternalIntegrationSourceTokenUpdateMutationGuardConfig()
	fingerprint, err := guardConfig.fingerprint(httptest.NewRecorder(), req)
	if err != nil {
		t.Fatal(err)
	}
	got := fingerprint.(map[string]any)
	for key, want := range map[string]any{"id": " raw source ", "tokenId": " raw token ", "name": " raw name ", "status": "disabled", "expiresAt": " raw expiry "} {
		if got[key] != want {
			t.Fatalf("%s=%#v want %#v", key, got[key], want)
		}
	}
	if _, ok := got["scopes"]; ok {
		t.Fatalf("fingerprint contains scopes: %#v", got)
	}
	if guardConfig.operationKey != "external_integration_sources.update_token" {
		t.Fatalf("operation key=%q", guardConfig.operationKey)
	}
	downstreamBody, err := io.ReadAll(req.Body)
	if err != nil || string(downstreamBody) != `{"name":" raw name ","status":"disabled","scopes":["secret"],"expiresAt":" raw expiry "}` {
		t.Fatalf("downstream body=%q err=%v", downstreamBody, err)
	}
}

func managementExternalIntegrationSourceTokenUpdateRequest(sourceID, tokenID, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPatch, "/__aisys__/api/external-integration-sources/source/tokens/token", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("id", sourceID)
	routeContext.URLParams.Add("tokenId", tokenID)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext))
}

func managementExternalIntegrationSourceTokenUpdateToken(name string) managementexternalintegrationsources.Token {
	return managementexternalintegrationsources.Token{ID: "token_1", Name: name, TokenPrefix: "sk", TokenSuffix: "tail", Status: "enabled", Scopes: []string{"secret-scope"}, ExpiresAt: managementExternalIntegrationSourceTokenUpdateStringPointer("2026-08-01T00:00:00.000Z"), CreatedAt: "created", UpdatedAt: "updated"}
}

func managementExternalIntegrationSourceTokenUpdateStringPointer(value string) *string { return &value }

func managementExternalIntegrationSourceTokenUpdateValidationError() error {
	service := managementexternalintegrationsources.NewTokenUpdateService(managementExternalIntegrationSourceTokenUpdatePortStub{})
	_, err := service.Update(context.Background(), managementexternalintegrationsources.TokenUpdateInput{SourceID: "source_1", TokenID: "token_1", HasName: true, Name: " "})
	return err
}

type managementExternalIntegrationSourceTokenUpdatePortStub struct{}

func (managementExternalIntegrationSourceTokenUpdatePortStub) UpdateManagementExternalIntegrationSourceToken(
	context.Context,
	port.ManagementExternalIntegrationSourceTokenUpdateInput,
	func(port.ManagementExternalIntegrationSourceTokenUpdateResult) error,
) (port.ManagementExternalIntegrationSourceTokenUpdateResult, error) {
	return port.ManagementExternalIntegrationSourceTokenUpdateResult{}, nil
}

type managementExternalIntegrationSourceTokenUpdateServiceStub struct {
	input  managementexternalintegrationsources.TokenUpdateInput
	result managementexternalintegrationsources.TokenUpdateResult
	err    error
	calls  int
}

func (s *managementExternalIntegrationSourceTokenUpdateServiceStub) Update(_ context.Context, input managementexternalintegrationsources.TokenUpdateInput) (managementexternalintegrationsources.TokenUpdateResult, error) {
	s.calls++
	s.input = input
	return s.result, s.err
}

var _ managementExternalIntegrationSourceTokenUpdateService = (*managementExternalIntegrationSourceTokenUpdateServiceStub)(nil)

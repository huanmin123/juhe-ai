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

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementexternalintegrationsources"
	"juhe-ai/backend-go/internal/secretcrypto"
	"juhe-ai/backend-go/internal/store/port"
)

const managementExternalIntegrationSourceTokenSecretTestSecret = "management-external-integration-source-token-secret-handler-test"

func TestManagementExternalIntegrationSourceTokenSecretHandlerRequiresAdmin(t *testing.T) {
	reader := &managementExternalIntegrationSourceTokenSecretReaderStub{}
	service := managementExternalIntegrationSourceTokenSecretTestService(t, reader, "secret")
	tests := []struct {
		name       string
		auth       *managementauth.Context
		wantStatus int
		wantText   string
	}{
		{name: "missing auth context", wantStatus: http.StatusInternalServerError, wantText: "服务器内部错误"},
		{
			name:       "non-admin",
			auth:       &managementauth.Context{SystemAccountID: "sys_user", Role: "user"},
			wantStatus: http.StatusForbidden,
			wantText:   "需要管理员权限",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := managementExternalIntegrationSourceTokenSecretRequest("source_1", "token_1", tt.auth)
			rec := httptest.NewRecorder()

			NewManagementExternalIntegrationSourceTokenSecretHandler(service).ServeHTTP(rec, req)

			assertManagementExternalIntegrationSourceTokenSecretMessage(t, rec, tt.wantStatus, tt.wantText)
		})
	}
	if len(reader.calls) != 0 {
		t.Fatal("authentication rejection must happen before token lookup")
	}
}

func TestManagementExternalIntegrationSourceTokenSecretHandlerValidatesIdentifiers(t *testing.T) {
	tests := []struct {
		name       string
		sourceID   string
		tokenID    string
		wantStatus int
		wantText   string
	}{
		{
			name:       "empty source after ECMAScript trim",
			sourceID:   "\uFEFF\u00A0\t\r\n",
			tokenID:    "token_1",
			wantStatus: http.StatusBadRequest,
			wantText:   "来源系统不存在",
		},
		{
			name:       "empty token after ECMAScript trim",
			sourceID:   "source_1",
			tokenID:    "\uFEFF\u00A0\t\r\n",
			wantStatus: http.StatusBadRequest,
			wantText:   "Token 不存在",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := &managementExternalIntegrationSourceTokenSecretReaderStub{err: errors.New("empty identifiers must not query")}
			service := managementExternalIntegrationSourceTokenSecretTestService(t, reader, "secret")
			req := managementExternalIntegrationSourceTokenSecretRequest(tt.sourceID, tt.tokenID, managementExternalIntegrationSourceTokenSecretAdmin())
			rec := httptest.NewRecorder()

			NewManagementExternalIntegrationSourceTokenSecretHandler(service).ServeHTTP(rec, req)

			assertManagementExternalIntegrationSourceTokenSecretMessage(t, rec, tt.wantStatus, tt.wantText)
			if len(reader.calls) != 0 {
				t.Fatalf("reader calls = %#v, want none", reader.calls)
			}
		})
	}
}

func TestManagementExternalIntegrationSourceTokenSecretHandlerReturnsNotFoundForMissingOrMismatchedToken(t *testing.T) {
	tests := []struct {
		name     string
		sourceID string
		tokenID  string
	}{
		{name: "missing token", sourceID: "source_1", tokenID: "token_missing"},
		{name: "token belongs to another source", sourceID: "source_other", tokenID: "token_1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := &managementExternalIntegrationSourceTokenSecretReaderStub{}
			service := managementExternalIntegrationSourceTokenSecretTestService(t, reader, "unused")
			req := managementExternalIntegrationSourceTokenSecretRequest(tt.sourceID, tt.tokenID, managementExternalIntegrationSourceTokenSecretAdmin())
			rec := httptest.NewRecorder()

			NewManagementExternalIntegrationSourceTokenSecretHandler(service).ServeHTTP(rec, req)

			assertManagementExternalIntegrationSourceTokenSecretMessage(t, rec, http.StatusNotFound, "Token 不存在")
			wantCalls := []managementExternalIntegrationSourceTokenSecretReaderCall{{sourceID: tt.sourceID, tokenID: tt.tokenID}}
			if !reflect.DeepEqual(reader.calls, wantCalls) {
				t.Fatalf("reader calls = %#v, want %#v", reader.calls, wantCalls)
			}
		})
	}
}

func TestManagementExternalIntegrationSourceTokenSecretHandlerHidesServiceErrors(t *testing.T) {
	tests := []struct {
		name    string
		service *managementexternalintegrationsources.Service
	}{
		{name: "nil service"},
		{
			name: "store error",
			service: managementexternalintegrationsources.NewServiceWithOptions(
				managementexternalintegrationsources.ServiceOptions{
					SecretReader: &managementExternalIntegrationSourceTokenSecretReaderStub{err: errors.New("postgres password leaked")},
					Secret:       managementExternalIntegrationSourceTokenSecretTestSecret,
				},
			),
		},
		{
			name: "decrypt error",
			service: managementexternalintegrationsources.NewServiceWithOptions(
				managementexternalintegrationsources.ServiceOptions{
					SecretReader: &managementExternalIntegrationSourceTokenSecretReaderStub{encrypted: "not-ciphertext", found: true},
					Secret:       managementExternalIntegrationSourceTokenSecretTestSecret,
				},
			),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := managementExternalIntegrationSourceTokenSecretRequest("source_1", "token_1", managementExternalIntegrationSourceTokenSecretAdmin())
			rec := httptest.NewRecorder()

			NewManagementExternalIntegrationSourceTokenSecretHandler(tt.service).ServeHTTP(rec, req)

			assertManagementExternalIntegrationSourceTokenSecretMessage(t, rec, http.StatusInternalServerError, "服务器内部错误")
		})
	}
}

func TestManagementExternalIntegrationSourceTokenSecretHandlerReturnsExactPlaintextWithoutCaching(t *testing.T) {
	const plaintext = "  arbitrary external token \twith no required prefix  "
	reader := &managementExternalIntegrationSourceTokenSecretReaderStub{found: true}
	service := managementExternalIntegrationSourceTokenSecretTestService(t, reader, plaintext)
	req := managementExternalIntegrationSourceTokenSecretRequest(
		"\uFEFF \tsource_1\r\n",
		"\u00A0token_1\uFEFF",
		managementExternalIntegrationSourceTokenSecretAdmin(),
	)
	rec := httptest.NewRecorder()

	NewManagementExternalIntegrationSourceTokenSecretHandler(service).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if got := rec.Header().Get("Pragma"); got != "no-cache" {
		t.Fatalf("Pragma = %q, want no-cache", got)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	wantBody := map[string]any{"data": map[string]any{"token": plaintext}}
	if !reflect.DeepEqual(body, wantBody) {
		t.Fatal("token secret response shape or plaintext does not match")
	}
	wantCalls := []managementExternalIntegrationSourceTokenSecretReaderCall{{sourceID: "source_1", tokenID: "token_1"}}
	if !reflect.DeepEqual(reader.calls, wantCalls) {
		t.Fatalf("reader calls = %#v, want %#v", reader.calls, wantCalls)
	}
}

func managementExternalIntegrationSourceTokenSecretTestService(
	t *testing.T,
	reader *managementExternalIntegrationSourceTokenSecretReaderStub,
	plaintext string,
) *managementexternalintegrationsources.Service {
	t.Helper()
	if reader.found && reader.encrypted == "" {
		var err error
		reader.encrypted, err = secretcrypto.NewJSONCodec(managementExternalIntegrationSourceTokenSecretTestSecret).
			EncryptJSON(map[string]any{"token": plaintext})
		if err != nil {
			t.Fatalf("encrypt token fixture: %v", err)
		}
	}
	return managementexternalintegrationsources.NewServiceWithOptions(
		managementexternalintegrationsources.ServiceOptions{
			SecretReader: reader,
			Secret:       managementExternalIntegrationSourceTokenSecretTestSecret,
		},
	)
}

func managementExternalIntegrationSourceTokenSecretRequest(
	sourceID string,
	tokenID string,
	auth *managementauth.Context,
) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/external-integration-sources/source/tokens/token/secret", nil)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("id", sourceID)
	routeContext.URLParams.Add("tokenId", tokenID)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeContext)
	if auth != nil {
		ctx = context.WithValue(ctx, managementAuthContextKey, *auth)
	}
	return req.WithContext(ctx)
}

func managementExternalIntegrationSourceTokenSecretAdmin() *managementauth.Context {
	return &managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"}
}

func assertManagementExternalIntegrationSourceTokenSecretMessage(
	t *testing.T,
	rec *httptest.ResponseRecorder,
	wantStatus int,
	wantMessage string,
) {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, wantStatus, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body) != 1 || body["message"] != wantMessage {
		t.Fatalf("body = %#v, want exact message %q", body, wantMessage)
	}
}

type managementExternalIntegrationSourceTokenSecretReaderCall struct {
	sourceID string
	tokenID  string
}

type managementExternalIntegrationSourceTokenSecretReaderStub struct {
	encrypted string
	found     bool
	err       error
	calls     []managementExternalIntegrationSourceTokenSecretReaderCall
}

func (s *managementExternalIntegrationSourceTokenSecretReaderStub) FindManagementExternalIntegrationSourceTokenSecret(
	_ context.Context,
	sourceID string,
	tokenID string,
) (string, bool, error) {
	s.calls = append(s.calls, managementExternalIntegrationSourceTokenSecretReaderCall{sourceID: sourceID, tokenID: tokenID})
	return s.encrypted, s.found, s.err
}

var _ port.ManagementExternalIntegrationSourceTokenSecretReader = (*managementExternalIntegrationSourceTokenSecretReaderStub)(nil)
var _ func(*managementexternalintegrationsources.Service) http.Handler = NewManagementExternalIntegrationSourceTokenSecretHandler

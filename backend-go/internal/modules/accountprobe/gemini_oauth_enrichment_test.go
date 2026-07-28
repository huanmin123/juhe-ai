package accountprobe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

type geminiEnrichmentMockExecutor struct {
	responses []GeminiOAuthEnrichmentHTTPResponse
	requests  []GeminiOAuthEnrichmentHTTPRequest
	called    int
}

func (m *geminiEnrichmentMockExecutor) ExecuteGeminiOAuthEnrichment(_ context.Context, request GeminiOAuthEnrichmentHTTPRequest) (GeminiOAuthEnrichmentHTTPResponse, error) {
	m.requests = append(m.requests, request)
	if m.called >= len(m.responses) {
		return GeminiOAuthEnrichmentHTTPResponse{}, errors.New("mock response exhausted")
	}
	response := m.responses[m.called]
	m.called++
	return response, nil
}

func enrichmentResponse(status int, body string) GeminiOAuthEnrichmentHTTPResponse {
	return NewGeminiOAuthEnrichmentHTTPResponse(status, []byte(body), false)
}

func newTestGeminiEnricher(mock *geminiEnrichmentMockExecutor) *GeminiOAuthEnricher {
	e := NewGeminiOAuthEnricher(mock)
	e.now = func() time.Time { return time.Date(2026, 7, 28, 1, 2, 3, 0, time.UTC) }
	e.sleep = func(context.Context, time.Duration) error { return nil }
	return e
}

func TestGeminiOAuthEnrichmentCodeAssistLoadProjectAndTier(t *testing.T) {
	mock := &geminiEnrichmentMockExecutor{responses: []GeminiOAuthEnrichmentHTTPResponse{
		enrichmentResponse(http.StatusOK, `{"cloudaicompanionProject":{"id":"project-load"},"paidTier":{"id":"enterprise"}}`),
	}}
	out, err := newTestGeminiEnricher(mock).EnrichGeminiOAuth(context.Background(), GeminiOAuthEnrichmentInput{
		OAuthType: "code_assist", Secrets: NewGeminiOAuthEnrichmentSecrets("access-secret"), ProxyURL: "http://proxy.test:8080",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.ProjectID != "project-load" || out.TierID != "gcp_enterprise" || len(mock.requests) != 1 {
		t.Fatalf("unexpected output: %#v requests=%d", out, len(mock.requests))
	}
	assertGeminiRequest(t, mock.requests[0], "POST", geminiOAuthCloudCodeURL+"/v1internal:loadCodeAssist", "http://proxy.test:8080", `{"metadata":{"ideType":"ANTIGRAVITY","platform":"PLATFORM_UNSPECIFIED","pluginType":"GEMINI"}}`)
}

func TestGeminiOAuthEnrichmentCodeAssistOnboardAndResourceManagerFallback(t *testing.T) {
	mock := &geminiEnrichmentMockExecutor{responses: []GeminiOAuthEnrichmentHTTPResponse{
		enrichmentResponse(http.StatusOK, `{}`),
		enrichmentResponse(http.StatusOK, `{"done":false}`),
		enrichmentResponse(http.StatusOK, `{"done":true,"response":{}}`),
		enrichmentResponse(http.StatusOK, `{"projects":[{"projectId":"inactive","lifecycleState":"DELETE_REQUESTED"},{"projectId":"default-project","name":"Default","lifecycleState":"ACTIVE"},{"projectId":"companion-project","name":"cloud-ai-companion","lifecycleState":"ACTIVE"}]}`),
	}}
	out, err := newTestGeminiEnricher(mock).EnrichGeminiOAuth(context.Background(), GeminiOAuthEnrichmentInput{
		OAuthType: "code_assist", Secrets: NewGeminiOAuthEnrichmentSecrets("token"), ProxyURL: "socks5h://proxy.test:1080",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.ProjectID != "companion-project" || out.TierID != "gcp_standard" || len(mock.requests) != 4 {
		t.Fatalf("unexpected output: %#v requests=%d", out, len(mock.requests))
	}
	if mock.requests[1].Method() != "POST" || mock.requests[1].URL() != geminiOAuthCloudCodeURL+"/v1internal:onboardUser" {
		t.Fatalf("unexpected onboard request: %#v", mock.requests[1])
	}
	for index, request := range mock.requests {
		if request.ProxyURL() != "socks5h://proxy.test:1080" || request.Timeout() != geminiOAuthEnrichmentTimeout || request.MaxResponseBytes() != geminiOAuthEnrichmentMaxBody {
			t.Fatalf("request %d lost inherited transport bounds: %#v", index, request)
		}
	}
}

func TestGeminiOAuthEnrichmentRegisteredTierRequiresProject(t *testing.T) {
	mock := &geminiEnrichmentMockExecutor{responses: []GeminiOAuthEnrichmentHTTPResponse{
		enrichmentResponse(http.StatusOK, `{"currentTier":{"id":"standard"}}`),
		enrichmentResponse(http.StatusOK, `{"projects":[]}`),
	}}
	_, err := newTestGeminiEnricher(mock).EnrichGeminiOAuth(context.Background(), GeminiOAuthEnrichmentInput{
		OAuthType: "code_assist", Secrets: NewGeminiOAuthEnrichmentSecrets("token"),
	})
	if err == nil || !strings.Contains(err.Error(), "project") || len(mock.requests) != 2 {
		t.Fatalf("expected required project error, err=%v requests=%d", err, len(mock.requests))
	}
}

func TestGeminiOAuthEnrichmentGoogleOneDriveIsOptionalAndInfersTier(t *testing.T) {
	mock := &geminiEnrichmentMockExecutor{responses: []GeminiOAuthEnrichmentHTTPResponse{
		enrichmentResponse(http.StatusOK, `{"cloudaicompanionProject":"project"}`),
		enrichmentResponse(http.StatusOK, `{"storageQuota":{"limit":"2199023255552","usage":"42"}}`),
	}}
	out, err := newTestGeminiEnricher(mock).EnrichGeminiOAuth(context.Background(), GeminiOAuthEnrichmentInput{
		OAuthType: "google_one", Secrets: NewGeminiOAuthEnrichmentSecrets("token"), Scope: "openid https://www.googleapis.com/auth/drive.metadata.readonly",
	})
	if err != nil || out.ProjectID != "project" || out.TierID != "google_ai_pro" || out.DriveStorageLimit == nil || *out.DriveStorageUsage != 42 || !out.DriveTierUpdatedAt.Equal(time.Date(2026, 7, 28, 1, 2, 3, 0, time.UTC)) {
		t.Fatalf("unexpected Google One output: %#v err=%v", out, err)
	}
	if mock.requests[1].URL() != geminiOAuthGoogleAPIsURL+"/drive/v3/about?fields=storageQuota" {
		t.Fatalf("unexpected Drive URL: %s", mock.requests[1].URL())
	}
}

func TestGeminiOAuthEnrichmentGoogleOneDriveFailureDoesNotFailRequiredProject(t *testing.T) {
	mock := &geminiEnrichmentMockExecutor{responses: []GeminiOAuthEnrichmentHTTPResponse{
		enrichmentResponse(http.StatusOK, `{"cloudaicompanionProject":"project"}`),
		enrichmentResponse(http.StatusForbidden, `{"error":"secret should not be surfaced"}`),
	}}
	out, err := newTestGeminiEnricher(mock).EnrichGeminiOAuth(context.Background(), GeminiOAuthEnrichmentInput{
		OAuthType: "google_one", Secrets: NewGeminiOAuthEnrichmentSecrets("access-secret"), Scope: geminiOAuthDriveMetadataScope,
	})
	if err != nil || out.ProjectID != "project" || out.TierID != "google_one_free" || out.DriveStorageLimit != nil {
		t.Fatalf("unexpected optional Drive result: %#v err=%v", out, err)
	}
	if strings.Contains(errString(err), "secret") || strings.Contains(fmt.Sprintf("%#v", mock.requests[0]), "access-secret") {
		t.Fatal("sensitive diagnostic leaked")
	}
}

func TestGeminiOAuthEnrichmentDriveMissingQuotaIsSuccessfulZeroSnapshot(t *testing.T) {
	mock := &geminiEnrichmentMockExecutor{responses: []GeminiOAuthEnrichmentHTTPResponse{
		enrichmentResponse(http.StatusOK, `{}`),
	}}
	out, err := newTestGeminiEnricher(mock).EnrichGeminiOAuth(context.Background(), GeminiOAuthEnrichmentInput{
		OAuthType: "google_one", Secrets: NewGeminiOAuthEnrichmentSecrets("token"), ProjectID: "provided-project", Scope: geminiOAuthDriveMetadataScope,
	})
	if err != nil || out.DriveStorageLimit == nil || *out.DriveStorageLimit != 0 || out.DriveStorageUsage == nil || *out.DriveStorageUsage != 0 || out.DriveTierUpdatedAt.IsZero() {
		t.Fatalf("expected successful zero quota snapshot: %#v err=%v", out, err)
	}
}

func TestGeminiOAuthEnrichmentAIStudioSkipsHTTP(t *testing.T) {
	mock := &geminiEnrichmentMockExecutor{}
	out, err := newTestGeminiEnricher(mock).EnrichGeminiOAuth(context.Background(), GeminiOAuthEnrichmentInput{OAuthType: "ai_studio", Secrets: NewGeminiOAuthEnrichmentSecrets("token")})
	if err != nil || out.TierID != "aistudio_free" || len(mock.requests) != 0 {
		t.Fatalf("unexpected AI Studio result: %#v requests=%d err=%v", out, len(mock.requests), err)
	}
}

func TestGeminiOAuthEnrichmentBoundAndCancellation(t *testing.T) {
	mock := &geminiEnrichmentMockExecutor{responses: []GeminiOAuthEnrichmentHTTPResponse{
		NewGeminiOAuthEnrichmentHTTPResponse(http.StatusOK, make([]byte, geminiOAuthEnrichmentMaxBody+1), false),
		enrichmentResponse(http.StatusOK, `{"done":true,"response":{}}`),
		enrichmentResponse(http.StatusOK, `{"projects":[]}`),
	}}
	_, err := newTestGeminiEnricher(mock).EnrichGeminiOAuth(context.Background(), GeminiOAuthEnrichmentInput{OAuthType: "code_assist", Secrets: NewGeminiOAuthEnrichmentSecrets("token")})
	if err == nil || !strings.Contains(err.Error(), "loadCodeAssist") {
		t.Fatalf("expected bounded response error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = newTestGeminiEnricher(&geminiEnrichmentMockExecutor{}).EnrichGeminiOAuth(ctx, GeminiOAuthEnrichmentInput{OAuthType: "code_assist", Secrets: NewGeminiOAuthEnrichmentSecrets("token")})
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation: %v", err)
	}
}

func TestGeminiOAuthEnrichmentSecretTypesRedact(t *testing.T) {
	secret := NewGeminiOAuthEnrichmentSecrets("super-secret")
	if strings.Contains(secret.String(), "super-secret") || strings.Contains(secret.GoString(), "super-secret") {
		t.Fatal("secret formatted")
	}
	if got := stringMustMarshal(secret); got != "{}" {
		t.Fatalf("unexpected secret JSON: %s", got)
	}
}

func TestGeminiOAuthEnrichmentErrorsRedactExecutorCause(t *testing.T) {
	secretCause := errors.New("transport exposed access-secret")
	err := wrapGeminiOAuthEnrichmentError("loadCodeAssist", 0, secretCause)
	for _, formatted := range []string{fmt.Sprint(err), fmt.Sprintf("%#v", err), stringMustMarshal(err)} {
		if strings.Contains(formatted, "access-secret") {
			t.Fatalf("secret leaked through error formatting: %s", formatted)
		}
	}
	if !errors.Is(err, secretCause) || !errors.Is(err, ErrGeminiOAuthEnrichment) {
		t.Fatal("redacted error must retain machine-readable causes")
	}
}

func TestCodeAssistTierSelectionDoesNotSkipChosenInvalidAuthorityTier(t *testing.T) {
	if got := codeAssistTier(map[string]any{"paidTier": "future-tier", "currentTier": "enterprise"}); got != "" {
		t.Fatalf("paid tier must win before canonicalization: %q", got)
	}
	if got := codeAssistTier(map[string]any{"allowedTiers": []any{map[string]any{"id": "enterprise"}, map[string]any{"id": "future-tier", "isDefault": true}}}); got != "" {
		t.Fatalf("default allowed tier must win before canonicalization: %q", got)
	}
}

func assertGeminiRequest(t *testing.T, request GeminiOAuthEnrichmentHTTPRequest, method, url, proxy, body string) {
	t.Helper()
	if request.Method() != method || request.URL() != url || request.ProxyURL() != proxy || string(request.Body()) != body {
		t.Fatalf("unexpected request method=%s url=%s proxy=%s body=%s", request.Method(), request.URL(), request.ProxyURL(), request.Body())
	}
	if request.Timeout() != geminiOAuthEnrichmentTimeout || request.MaxResponseBytes() != geminiOAuthEnrichmentMaxBody {
		t.Fatalf("unexpected bounds timeout=%s bytes=%d", request.Timeout(), request.MaxResponseBytes())
	}
	if request.Header().Get("Authorization") != "Bearer access-secret" && request.Header().Get("Authorization") != "Bearer token" {
		t.Fatalf("missing auth header")
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func stringMustMarshal(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err.Error()
	}
	return string(encoded)
}

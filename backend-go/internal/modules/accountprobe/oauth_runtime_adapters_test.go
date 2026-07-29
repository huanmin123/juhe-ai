package accountprobe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
	"juhe-ai/backend-go/internal/platform/upstreamtransport"
	"juhe-ai/backend-go/internal/platform/upstreamurlpolicy"
	"juhe-ai/backend-go/internal/store/port"
)

func TestOAuthSnapshotLoaderUsesEffectiveSourceCiphertextAndFullValues(t *testing.T) {
	candidate := oauthCoordinatorCandidate(7, "stale", "stale-refresh", time.Now().Add(time.Hour))
	candidate.Projection.ResourceAccountID = "source"
	candidate.Projection.ResourceCredentialsEncrypted = "source-cipher"
	candidate.Projection.ResourceProviderCode = "openai"
	candidate.Projection.ResourceType = "oauth"
	candidate.Projection.ResourceConfigRevision = 9
	codec := &oauthRuntimeCodecStub{decrypted: map[string]any{
		"access_token": "fresh", "refresh_token": "refresh", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
		"request_overrides": map[string]any{"preserved": true},
	}}

	snapshot, err := (OAuthSnapshotLoader{Codec: codec}).Snapshot(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if codec.decryptedCipher != "source-cipher" || snapshot.Credentials().AccessToken() != "fresh" {
		t.Fatalf("cipher=%q access=%q", codec.decryptedCipher, snapshot.Credentials().AccessToken())
	}
	if value, ok := snapshot.Candidate().Credentials.Value("request_overrides"); !ok || value == nil {
		t.Fatal("snapshot candidate lost full credential fields")
	}
}

func TestOAuthCredentialCASAdapterPreparesNodeCompatibleMetadata(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	codec := &oauthRuntimeCodecStub{encrypted: "encrypted"}
	store := &oauthRuntimeCASStoreStub{applied: true}
	adapter := OAuthCredentialCASAdapter{Codec: codec, Store: store, Now: func() time.Time { return now }}
	input := OAuthCredentialCASInput{
		accountID: "source", systemAccountID: "owner", expectedAccountType: "oauth", expectedConfigRevision: 7,
		connectionIdentityChanged: true,
		patch: OAuthCredentialPatch{values: map[string]any{
			"access_token": "access", "refresh_token": "refresh-secret-123456",
			"expires_at": "2026-07-28T13:00:00Z", "request_overrides": map[string]any{"keep": true},
		}},
	}

	prepared, err := adapter.PrepareOAuthProbeCredentialCAS(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	applied, err := adapter.CompareAndSwapOAuthProbeCredentials(t.Context(), prepared)
	if err != nil || !applied {
		t.Fatalf("applied=%t error=%v", applied, err)
	}
	got := store.input
	fingerprint := sha256.Sum256([]byte("refresh-secret-123456"))
	if got.AccountID != "source" || got.SystemAccountID != "owner" || got.ExpectedConfigRevision != 7 ||
		got.Secrets.CredentialsEncrypted() != "encrypted" || got.Secrets.CredentialFingerprint() != hex.EncodeToString(fingerprint[:]) ||
		got.Secrets.CredentialMask() != "refres***3456" || !got.RefreshTokenPresent || !got.CircuitOwnerConfigurationChanged ||
		got.AccessTokenExpiresAt == nil || !got.AccessTokenExpiresAt.Equal(time.Date(2026, 7, 28, 13, 0, 0, 0, time.UTC)) {
		t.Fatalf("prepared CAS = %s", got.String())
	}
	if codec.encryptedValues["request_overrides"] == nil {
		t.Fatal("CAS encryption lost a non-refresh credential field")
	}
}

func TestOAuthRefreshTransportExecutorRequiresCompleteFraming(t *testing.T) {
	request, err := BuildOAuthRefreshRequest(mustOAuthCredentials(t, OAuthOpenAI, map[string]any{
		"access_token": "old", "refresh_token": "refresh",
	}))
	if err != nil {
		t.Fatal(err)
	}
	transport := &oauthRuntimeTransportStub{result: upstreamtransport.Result{
		StatusCode: 200, FramingComplete: true, Body: []byte(`{"access_token":"next","expires_in":3600}`),
	}}
	executor := OAuthRefreshTransportExecutor{Factory: oauthRuntimeTransportFactory{transport: transport}}
	response, err := executor.ExecuteOAuthRefresh(t.Context(), gatewaycandidatewindow.Candidate{}, request)
	if err != nil || response.StatusCode() != 200 || !strings.Contains(string(response.Body()), "next") {
		t.Fatalf("response=%v error=%v", response.StatusCode(), err)
	}
	transport.result.FramingComplete = false
	transport.err = errors.New("read failed")
	if _, err := executor.ExecuteOAuthRefresh(t.Context(), gatewaycandidatewindow.Candidate{}, request); err == nil {
		t.Fatal("incomplete OAuth refresh framing was accepted")
	}
}

func TestOAuthRefreshTransportExecutorAppliesRequestTimeoutAndBodyBound(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			select {
			case <-request.Context().Done():
				return
			case <-time.After(200 * time.Millisecond):
				_, _ = writer.Write([]byte(`{"access_token":"late"}`))
			}
		}))
		defer server.Close()
		request := OAuthRefreshRequest{provider: OAuthOpenAI, url: server.URL, header: make(http.Header), body: []byte(`{}`), timeout: 20 * time.Millisecond}
		executor := OAuthRefreshTransportExecutor{URLPolicy: upstreamurlpolicy.Config{PrivateBaseURLAllowlist: []string{server.URL}}}
		if _, err := executor.ExecuteOAuthRefresh(t.Context(), gatewaycandidatewindow.Candidate{}, request); err == nil {
			t.Fatal("OAuth refresh request timeout was ignored")
		}
	})

	t.Run("body bound", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write([]byte(strings.Repeat("x", oauthResponseMaxSize+1)))
		}))
		defer server.Close()
		request := OAuthRefreshRequest{provider: OAuthOpenAI, url: server.URL, header: make(http.Header), body: []byte(`{}`), timeout: time.Second}
		executor := OAuthRefreshTransportExecutor{URLPolicy: upstreamurlpolicy.Config{PrivateBaseURLAllowlist: []string{server.URL}}}
		response, err := executor.ExecuteOAuthRefresh(t.Context(), gatewaycandidatewindow.Candidate{}, request)
		if err != nil || !response.Truncated() || len(response.Body()) != oauthResponseMaxSize {
			t.Fatalf("response=%v truncated=%v bytes=%d error=%v", response.StatusCode(), response.Truncated(), len(response.Body()), err)
		}
	})
}

func TestGeminiOAuthEnrichmentTransportExecutorUsesBoundedURLPolicyTransport(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("request method=%q authorization=%q", request.Method, request.Header.Get("Authorization"))
		}
		_, _ = writer.Write([]byte(`{"project":"project"}`))
	}))
	defer server.Close()
	request := GeminiOAuthEnrichmentHTTPRequest{
		method: http.MethodPost, url: server.URL, header: http.Header{"Authorization": []string{"Bearer secret"}},
		body: []byte(`{}`), timeout: time.Second,
	}
	executor := GeminiOAuthEnrichmentTransportExecutor{URLPolicy: upstreamurlpolicy.Config{PrivateBaseURLAllowlist: []string{server.URL}}}
	response, err := executor.ExecuteGeminiOAuthEnrichment(t.Context(), request)
	if err != nil || response.StatusCode() != http.StatusOK || string(response.Body()) != `{"project":"project"}` {
		t.Fatalf("response=%d body=%q error=%v", response.StatusCode(), response.Body(), err)
	}
}

func TestOAuthGeminiRefreshEnricherAppliesAIStudioTierWithoutHTTP(t *testing.T) {
	httpExecutor := &geminiEnrichmentMockExecutor{}
	enricher := OAuthGeminiRefreshEnricher{Enricher: NewGeminiOAuthEnricher(httpExecutor)}
	result, err := enricher.EnrichOAuthRefresh(t.Context(), gatewaycandidatewindow.Candidate{}, OAuthRefreshResult{
		provider: OAuthGemini,
		values:   map[string]any{"access_token": "secret", "oauth_type": "ai_studio"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.values["tier_id"] != "aistudio_free" || len(httpExecutor.requests) != 0 {
		t.Fatalf("values=%v requests=%d", result.values, len(httpExecutor.requests))
	}
}

type oauthRuntimeCodecStub struct {
	decrypted       map[string]any
	decryptedCipher string
	encrypted       string
	encryptedValues map[string]any
}

func (c *oauthRuntimeCodecStub) DecryptJSON(ciphertext string) (map[string]any, error) {
	c.decryptedCipher = ciphertext
	return cloneOAuthMap(c.decrypted), nil
}

func (c *oauthRuntimeCodecStub) EncryptJSON(values map[string]any) (string, error) {
	c.encryptedValues = cloneOAuthMap(values)
	return c.encrypted, nil
}

type oauthRuntimeCASStoreStub struct {
	input   port.OAuthCredentialRefreshCASInput
	applied bool
}

func (s *oauthRuntimeCASStoreStub) CompareAndSwapOAuthCredentials(_ context.Context, input port.OAuthCredentialRefreshCASInput) (port.OAuthCredentialRefreshCASResult, bool, error) {
	s.input = input
	return port.OAuthCredentialRefreshCASResult{}, s.applied, nil
}

type oauthRuntimeTransportFactory struct{ transport AttemptTransport }

func (f oauthRuntimeTransportFactory) New(gatewaycandidatewindow.Candidate) (AttemptTransport, error) {
	return f.transport, nil
}

type oauthRuntimeTransportStub struct {
	result upstreamtransport.Result
	err    error
}

func (s *oauthRuntimeTransportStub) ExecuteWithFence(_ context.Context, request *http.Request, fence func(context.Context) error) (upstreamtransport.Result, error) {
	if request.Method != http.MethodPost || fence != nil {
		return upstreamtransport.Result{}, errors.New("unexpected OAuth refresh transport request")
	}
	return s.result, s.err
}

func mustOAuthCredentials(t *testing.T, provider OAuthProvider, values map[string]any) OAuthCredentials {
	t.Helper()
	credentials, err := ParseOAuthCredentials(provider, values)
	if err != nil {
		t.Fatal(err)
	}
	return credentials
}

package accounthealth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProbeOpenAIChatUsesDirectNativeRequest(t *testing.T) {
	secret := "test-secret"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions" || request.Method != http.MethodPost {
			t.Fatalf("unexpected direct path: %s %s", request.Method, request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Fatalf("unexpected authorization: %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "gpt-test" || body["stream"] != false || body["max_tokens"] != float64(256) {
			t.Fatalf("unexpected direct request body: %#v", body)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"content":"juhe"}}]}`))
	}))
	defer server.Close()

	result := ProbeOpenAI(context.Background(), testInput(server.URL, "chat_json"), CredentialEnvelope{Kind: "api_key", Ciphertext: testEnvelope(t, secret, `{"api_key":"sk-test"}`)}, ProbeOptions{Secret: secret, Timeout: time.Second})
	if result.Outcome != OutcomeSuccess || result.StatusCode != http.StatusOK {
		t.Fatalf("unexpected probe result: %#v", result)
	}
}

func TestProbeTransportDisablesHTTP2ForDirectProviderProbe(t *testing.T) {
	transport, err := probeTransport(Input{}, ProbeOptions{})
	if err != nil {
		t.Fatalf("probeTransport() error = %v", err)
	}
	if transport.ForceAttemptHTTP2 {
		t.Fatal("direct provider probe must not negotiate HTTP/2")
	}
}

func TestProbeOpenAIClassifiesCompleteSemanticFailureAsNeutral(t *testing.T) {
	secret := "test-secret"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[]}`))
	}))
	defer server.Close()
	result := ProbeOpenAI(context.Background(), testInput(server.URL, "chat_json"), CredentialEnvelope{Kind: "api_key", Ciphertext: testEnvelope(t, secret, "sk-test")}, ProbeOptions{Secret: secret, Timeout: time.Second})
	if result.Outcome != OutcomeNeutral || result.ErrorCode != "upstream_protocol_invalid" {
		t.Fatalf("unexpected probe result: %#v", result)
	}
}

func TestProbeOpenAIOAuthUsesCodexEndpointAndHeaders(t *testing.T) {
	secret := "test-secret"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/backend-api/codex/responses" {
			t.Fatalf("unexpected OAuth path: %s", request.URL.Path)
		}
		if request.Header.Get("OpenAI-Beta") != "responses=experimental" || request.Header.Get("ChatGPT-Account-Id") != "chatgpt-account-1" {
			t.Fatalf("missing OAuth headers: %#v", request.Header)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"output_text":"juhe"}`))
	}))
	defer server.Close()
	input := testInput(server.URL+"/backend-api/codex", "responses_json")
	input.Type = "oauth"
	input.OAuthAccountID = "chatgpt-account-1"
	expiresAt := time.Now().UTC().Add(time.Hour)
	input.OAuthExpiresAt = &expiresAt
	result := ProbeOpenAI(context.Background(), input, CredentialEnvelope{Kind: "oauth_access", Ciphertext: testEnvelope(t, secret, `{"access_token":"oauth-token"}`)}, ProbeOptions{Secret: secret, Timeout: time.Second})
	if result.Outcome != OutcomeSuccess {
		t.Fatalf("unexpected OAuth probe result: %#v", result)
	}
}

func TestProbeOpenAIRejectsExpiredInputBeforeHTTP(t *testing.T) {
	input := testInput("https://example.invalid", "chat_json")
	input.ExpiresAt = time.Now().UTC().Add(-time.Second)
	result := ProbeOpenAI(context.Background(), input, CredentialEnvelope{}, ProbeOptions{Secret: "test-secret"})
	if result.Outcome != OutcomeTaskFailed || result.ErrorCode != "invalid_input" {
		t.Fatalf("expired input must be task failure: %#v", result)
	}
}

func TestProbeOpenAICancellationIsTaskFailure(t *testing.T) {
	secret := "test-secret"
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		close(started)
		time.Sleep(100 * time.Millisecond)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan ProbeResult, 1)
	go func() {
		result <- ProbeOpenAI(ctx, testInput(server.URL, "chat_json"), CredentialEnvelope{Kind: "api_key", Ciphertext: testEnvelope(t, secret, "sk-test")}, ProbeOptions{Secret: secret, Timeout: time.Second})
	}()
	select {
	case <-started:
		cancel()
	case <-time.After(time.Second):
		t.Fatal("probe did not issue its direct upstream request")
	}
	select {
	case outcome := <-result:
		if outcome.Outcome != OutcomeTaskFailed || outcome.ErrorCode != "probe_cancelled" {
			t.Fatalf("cancelled probe must be task failure, got %#v", outcome)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled probe did not return")
	}
}

func TestProbeOpenAITimeoutAfterRequestIsUpstreamFailure(t *testing.T) {
	secret := "test-secret"
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		close(started)
		time.Sleep(100 * time.Millisecond)
	}))
	defer server.Close()
	result := ProbeOpenAI(context.Background(), testInput(server.URL, "chat_json"), CredentialEnvelope{Kind: "api_key", Ciphertext: testEnvelope(t, secret, "sk-test")}, ProbeOptions{Secret: secret, Timeout: 25 * time.Millisecond})
	select {
	case <-started:
	default:
		t.Fatal("timeout test did not issue its direct upstream request")
	}
	if result.Outcome != OutcomeUpstreamFailed || result.ErrorCode != "upstream_timeout" {
		t.Fatalf("timed out upstream probe must be upstream failure, got %#v", result)
	}
}

func TestDecryptV1EnvelopeRoundTrip(t *testing.T) {
	secret := "test-secret"
	plain := "credential-value"
	decrypted, err := DecryptV1Envelope(secret, testEnvelope(t, secret, plain))
	if err != nil || string(decrypted) != plain {
		t.Fatalf("decrypt result=%q err=%v", decrypted, err)
	}
}

func testInput(baseURL, mode string) Input {
	now := time.Now().UTC()
	return Input{
		AccountID:            "account-1",
		InputVersion:         1,
		ConfigRevision:       1,
		DispatchRevision:     1,
		Provider:             "openai",
		Type:                 "api_key",
		EndpointMode:         mode,
		HealthModel:          "gpt-test",
		BaseURL:              baseURL,
		IssuedAt:             now,
		ExpiresAt:            now.Add(time.Hour),
		TLSPolicyVersion:     "test",
		AllowInsecureBaseURL: strings.HasPrefix(baseURL, "http://"),
	}
}

func testEnvelope(t *testing.T, secret, plaintext string) string {
	t.Helper()
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	iv := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(iv); err != nil {
		t.Fatal(err)
	}
	encoded := gcm.Seal(nil, iv, []byte(plaintext), nil)
	ciphertext := encoded[:len(encoded)-gcm.Overhead()]
	tag := encoded[len(encoded)-gcm.Overhead():]
	return "v1:" + base64.RawURLEncoding.EncodeToString(iv) + ":" + base64.RawURLEncoding.EncodeToString(tag) + ":" + base64.RawURLEncoding.EncodeToString(ciphertext)
}

package accounthealth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
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

func TestProbeOpenAIResponsesSSEUsesCompletedStream(t *testing.T) {
	secret := "test-secret"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/responses" || request.Method != http.MethodPost {
			t.Fatalf("unexpected direct path: %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Accept") != "text/event-stream" {
			t.Fatalf("unexpected accept header: %q", request.Header.Get("Accept"))
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "gpt-test" || body["stream"] != true || body["max_output_tokens"] != float64(256) {
			t.Fatalf("unexpected SSE request body: %#v", body)
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"juhe\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"))
	}))
	defer server.Close()

	result := ProbeOpenAI(context.Background(), testInput(server.URL, "responses_sse"), CredentialEnvelope{Kind: "api_key", Ciphertext: testEnvelope(t, secret, `{"api_key":"sk-test"}`)}, ProbeOptions{Secret: secret, Timeout: time.Second})
	if result.Outcome != OutcomeSuccess || result.StatusCode != http.StatusOK {
		t.Fatalf("unexpected SSE probe result: %#v", result)
	}
}

func TestProbeOpenAICodexResponsesMatchesManualCompatibilityContract(t *testing.T) {
	secret := "test-secret"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Originator") != "Codex Desktop" || request.Header.Get("User-Agent") != "Codex Desktop/0.145.0 (Windows 10.0.22621; x86_64) unknown (codex_exec; 0.145.0)" {
			t.Fatalf("unexpected Codex client headers: %#v", request.Header)
		}
		sessionID := request.Header.Get("Session-Id")
		if _, err := uuid.Parse(sessionID); err != nil || request.Header.Get("Thread-Id") != sessionID || request.Header.Get("X-Codex-Window-Id") != sessionID+":0" || request.Header.Get("X-OpenAI-Internal-Codex-Responses-Lite") != "true" {
			t.Fatalf("unexpected Codex identity headers: %#v", request.Header)
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["store"] != false || body["parallel_tool_calls"] != false || body["prompt_cache_key"] != sessionID {
			t.Fatalf("unexpected Codex compatibility body: %#v", body)
		}
		include, ok := body["include"].([]any)
		if !ok || len(include) != 1 || include[0] != "reasoning.encrypted_content" {
			t.Fatalf("unexpected Codex include: %#v", body["include"])
		}
		reasoning, ok := body["reasoning"].(map[string]any)
		if !ok || reasoning["context"] != "all_turns" {
			t.Fatalf("unexpected Codex reasoning: %#v", body["reasoning"])
		}
		metadata, ok := body["client_metadata"].(map[string]any)
		if !ok || metadata["x-codex-window-id"] != sessionID+":0" || metadata["session_id"] != sessionID || metadata["x-codex-turn-metadata"] != request.Header.Get("X-Codex-Turn-Metadata") {
			t.Fatalf("unexpected Codex metadata: %#v", body["client_metadata"])
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"juhe\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"))
	}))
	defer server.Close()

	input := testInput(server.URL, "responses_sse")
	input.ClientCompatibility = "codex_responses"
	input.HealthModel = "gpt-5.6-terra"
	result := ProbeOpenAI(context.Background(), input, CredentialEnvelope{Kind: "api_key", Ciphertext: testEnvelope(t, secret, `{"api_key":"sk-test"}`)}, ProbeOptions{Secret: secret, Timeout: time.Second})
	if result.Outcome != OutcomeSuccess || result.StatusCode != http.StatusOK {
		t.Fatalf("unexpected Codex compatibility probe result: %#v", result)
	}
}

func TestProbeGPTOAuthResponsesSSEUsesCodexPath(t *testing.T) {
	secret := "test-secret"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/responses" {
			t.Fatalf("GPT OAuth must use Codex responses path, got %s", request.URL.Path)
		}
		if request.Header.Get("OpenAI-Beta") != "responses=experimental" {
			t.Fatalf("missing Codex OpenAI-Beta header: %q", request.Header.Get("OpenAI-Beta"))
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"juhe\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"))
	}))
	defer server.Close()
	input := testInput(server.URL, "responses_sse")
	input.Type = "oauth"
	input.ProtocolProfileID = "profile_gpt_openai_v1"
	input.ProtocolCode = "openai"
	expiresAt := time.Now().UTC().Add(time.Hour)
	input.OAuthExpiresAt = &expiresAt
	result := ProbeOpenAI(context.Background(), input, CredentialEnvelope{Kind: "oauth_access", Ciphertext: testEnvelope(t, secret, `{"access_token":"oauth-token"}`)}, ProbeOptions{Secret: secret, Timeout: time.Second})
	if result.Outcome != OutcomeSuccess {
		t.Fatalf("unexpected GPT OAuth result: %#v", result)
	}
}

func TestProbeOpenAIResponsesSSERequiresCompletionEvent(t *testing.T) {
	secret := "test-secret"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"juhe\"}\n\n"))
	}))
	defer server.Close()

	result := ProbeOpenAI(context.Background(), testInput(server.URL, "responses_sse"), CredentialEnvelope{Kind: "api_key", Ciphertext: testEnvelope(t, secret, "sk-test")}, ProbeOptions{Secret: secret, Timeout: time.Second})
	if result.Outcome != OutcomeNeutral || result.ErrorCode != "upstream_protocol_invalid" {
		t.Fatalf("incomplete SSE stream must be neutral, got %#v", result)
	}
}

func TestProbeOpenAIResponsesSSERejectsJSONImposter(t *testing.T) {
	secret := "test-secret"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"type":"response.completed","output_text":"juhe"}`))
	}))
	defer server.Close()

	result := ProbeOpenAI(context.Background(), testInput(server.URL, "responses_sse"), CredentialEnvelope{Kind: "api_key", Ciphertext: testEnvelope(t, secret, "sk-test")}, ProbeOptions{Secret: secret, Timeout: time.Second})
	if result.Outcome != OutcomeNeutral || result.ErrorCode != "upstream_protocol_invalid" {
		t.Fatalf("JSON cannot satisfy an SSE probe, got %#v", result)
	}
}

func TestProbeOpenAIImagesUsesGenerationEndpointAndRequiresImageResult(t *testing.T) {
	secret := "test-secret"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/images/generations" || request.Method != http.MethodPost {
			t.Fatalf("unexpected image probe path: %s %s", request.Method, request.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "gpt-test" || body["prompt"] != "Solid black." || body["n"] != float64(1) || body["size"] != "1024x1024" || body["quality"] != "low" || body["output_format"] != "webp" || body["output_compression"] != float64(100) {
			t.Fatalf("unexpected image probe body: %#v", body)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"data":[{"b64_json":"c2FtcGxl"}]}`))
	}))
	defer server.Close()

	result := ProbeOpenAI(context.Background(), testInput(server.URL, "images_json"), CredentialEnvelope{Kind: "api_key", Ciphertext: testEnvelope(t, secret, `{"api_key":"sk-test"}`)}, ProbeOptions{Secret: secret, Timeout: time.Second})
	if result.Outcome != OutcomeSuccess || result.StatusCode != http.StatusOK {
		t.Fatalf("unexpected image probe result: %#v", result)
	}

	missingResultServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"data":[{"revised_prompt":"Solid black."}]}`))
	}))
	defer missingResultServer.Close()
	result = ProbeOpenAI(context.Background(), testInput(missingResultServer.URL, "images_json"), CredentialEnvelope{Kind: "api_key", Ciphertext: testEnvelope(t, secret, `{"api_key":"sk-test"}`)}, ProbeOptions{Secret: secret, Timeout: time.Second})
	if result.Outcome != OutcomeNeutral || result.ErrorCode != "upstream_protocol_invalid" {
		t.Fatalf("missing image result must be neutral, got %#v", result)
	}
}

func TestProbeAnthropicMessagesUsesNativeHeadersAndCompletion(t *testing.T) {
	secret := "test-secret"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/messages" || request.Header.Get("x-api-key") != "anthropic-key" || request.Header.Get("anthropic-version") != "2023-06-01" {
			t.Fatalf("unexpected Anthropic request: path=%s headers=%#v", request.URL.Path, request.Header)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"type":"message","stop_reason":"end_turn","content":[{"type":"text","text":"juhe"}]}`))
	}))
	defer server.Close()
	input := testInput(server.URL, "messages_json")
	input.Provider = "anthropic"
	input.ProtocolProfileID = "profile_anthropic_anthropic_v1"
	input.ProtocolCode = "anthropic"
	result := ProbeOpenAI(context.Background(), input, CredentialEnvelope{Kind: "api_key", Ciphertext: testEnvelope(t, secret, `{"api_key":"anthropic-key"}`)}, ProbeOptions{Secret: secret, Timeout: time.Second})
	if result.Outcome != OutcomeSuccess {
		t.Fatalf("unexpected Anthropic result: %#v", result)
	}
}

func TestProbeGeminiNativeUsesGoogleOAuthHeadersAndCompletion(t *testing.T) {
	secret := "test-secret"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1beta/models/gemini-test:generateContent" || request.Header.Get("Authorization") != "Bearer google-token" || request.Header.Get("x-goog-user-project") != "quota-1" {
			t.Fatalf("unexpected Gemini request: path=%s headers=%#v", request.URL.Path, request.Header)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"juhe"}]}}]}`))
	}))
	defer server.Close()
	input := testInput(server.URL, "generate_content_json")
	input.Provider = "gemini"
	input.ProtocolProfileID = "profile_gemini_native_v1beta"
	input.ProtocolCode = "gemini"
	input.ProtocolVersion = "v1beta"
	input.HealthModel = "gemini-test"
	input.Type = "google_oauth"
	input.OAuthQuotaProjectID = "quota-1"
	expiresAt := time.Now().UTC().Add(time.Hour)
	input.OAuthExpiresAt = &expiresAt
	result := ProbeOpenAI(context.Background(), input, CredentialEnvelope{Kind: "oauth_access", Ciphertext: testEnvelope(t, secret, `{"access_token":"google-token"}`)}, ProbeOptions{Secret: secret, Timeout: time.Second})
	if result.Outcome != OutcomeSuccess {
		t.Fatalf("unexpected Gemini result: %#v", result)
	}
}

func TestProbeGeminiInteractionsSSERequiresCompletedEvent(t *testing.T) {
	secret := "test-secret"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1beta/interactions" || request.Header.Get("api-revision") != "2026-05-20" || request.Header.Get("Accept") != "text/event-stream" {
			t.Fatalf("unexpected Gemini Interactions request: path=%s headers=%#v", request.URL.Path, request.Header)
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"type\":\"interaction.delta\",\"text\":\"juhe\"}\n\ndata: {\"type\":\"interaction.completed\",\"status\":\"completed\"}\n\n"))
	}))
	defer server.Close()
	input := testInput(server.URL, "interactions_sse")
	input.Provider = "gemini"
	input.ProtocolProfileID = "profile_gemini_native_v1beta"
	input.ProtocolCode = "gemini"
	input.ProtocolVersion = "v1beta"
	result := ProbeOpenAI(context.Background(), input, CredentialEnvelope{Kind: "api_key", Ciphertext: testEnvelope(t, secret, `{"api_key":"gemini-key"}`)}, ProbeOptions{Secret: secret, Timeout: time.Second})
	if result.Outcome != OutcomeSuccess {
		t.Fatalf("unexpected Gemini Interactions result: %#v", result)
	}
}

func TestProbeTransportEnablesHTTP2ForCustomDialer(t *testing.T) {
	transport, err := probeTransport(Input{}, ProbeOptions{})
	if err != nil {
		t.Fatalf("probeTransport() error = %v", err)
	}
	if !transport.ForceAttemptHTTP2 {
		t.Fatal("direct provider probe must explicitly enable HTTP/2 when it owns a custom dialer")
	}
	if transport.MaxConnsPerHost != 0 || transport.MaxIdleConnsPerHost != 0 {
		t.Fatalf("direct provider probe must not impose a per-host connection cap: max=%d idle=%d", transport.MaxConnsPerHost, transport.MaxIdleConnsPerHost)
	}
}

func TestProbeHTTPClientReusesSharedTransportPolicy(t *testing.T) {
	first, err := probeHTTPClient(Input{}, ProbeOptions{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	second, err := probeHTTPClient(Input{}, ProbeOptions{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("account-health probes must reuse the shared HTTP client for the same transport policy")
	}
}

func TestProbeTransportReadsHTTP2ResponseWithCustomDialer(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()

	transport, err := probeTransport(Input{}, ProbeOptions{Timeout: time.Second})
	if err != nil {
		t.Fatalf("probeTransport() error = %v", err)
	}
	serverTransport := server.Client().Transport.(*http.Transport)
	transport.TLSClientConfig = serverTransport.TLSClientConfig.Clone()
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	client := &http.Client{Transport: transport}
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("HTTP/2 response through custom dialer failed: %v", err)
	}
	defer response.Body.Close()
	if response.ProtoMajor != 2 {
		t.Fatalf("response protocol = HTTP/%d.%d, want HTTP/2", response.ProtoMajor, response.ProtoMinor)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil || string(body) != `{"ok":true}` {
		t.Fatalf("response body = %q, read error = %v", body, err)
	}
}

func TestTransportFailureClassifiesClosedConnectionWithoutRawError(t *testing.T) {
	result := transportFailure(&url.Error{Op: "Post", URL: "https://example.invalid/v1/chat/completions", Err: io.EOF})
	if result.Outcome != OutcomeUpstreamFailed || result.ErrorCode != "upstream_connection_closed" || result.ErrorMessage != "上游提前关闭连接" {
		t.Fatalf("unexpected closed connection classification: %#v", result)
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

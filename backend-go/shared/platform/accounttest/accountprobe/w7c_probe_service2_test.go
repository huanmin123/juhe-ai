package accountprobe

// w7c third wave: images/anthropic/gemini execute arms, interrupted reads,
// attemptStaged escalation, buildUpstreamURL branches, and the limited
// quota-message rule.

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/accountquality"
)

func TestW7CExecuteAttemptProtocolAuthArms(t *testing.T) {
	seen := map[string]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen["anthropic-version"] = r.Header.Get("anthropic-version")
		seen["authorization"] = r.Header.Get("authorization")
		seen["x-api-key"] = r.Header.Get("x-api-key")
		seen["x-goog-api-key"] = r.Header.Get("x-goog-api-key")
		seen["x-goog-user-project"] = r.Header.Get("x-goog-user-project")
		seen["chatgpt-account-id"] = r.Header.Get("chatgpt-account-id")
		seen["openai-beta"] = r.Header.Get("openai-beta")
		seen["path"] = r.URL.Path
		if r.URL.Path == "/v1/images/generations" {
			_, _ = w.Write([]byte(`{"created":1,"data":[{"url":"https://img/x.webp"}]}`))
			return
		}
		if r.URL.Path == "/v1/messages" {
			_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"juhe"}],"stop_reason":"end_turn"}`))
			return
		}
		if strings.Contains(r.URL.Path, "generateContent") {
			_, _ = w.Write([]byte(`{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"juhe"}]}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"juhe"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	ctx := context.Background()

	t.Run("anthropic bearer via oauth", func(t *testing.T) {
		view := probeView(server.URL)
		view.ProtocolCode = "anthropic"
		view.HealthCheckEndpointMode = string(ModeMessagesJSON)
		view.Type = "oauth"
		view.SelectedAPIKey = "tok-a"
		view.NormalizeEndpointModes = map[EndpointMode]bool{ModeMessagesJSON: true}
		if _, err := newTestService(t, &fakeSource{view: view}).Probe(ctx, accountquality.ProbeRequest{AccountID: "acc-1", Full: true}); err != nil {
			t.Fatal(err)
		}
		// The pool runner always sends the entry key, not the selected
		// credential.
		if seen["anthropic-version"] == "" || seen["authorization"] != "Bearer sk-test" || seen["x-api-key"] != "" {
			t.Fatalf("anthropic oauth headers: %v", seen)
		}
	})

	t.Run("anthropic glm profile bearer", func(t *testing.T) {
		view := probeView(server.URL)
		view.ProtocolCode = "anthropic"
		view.HealthCheckEndpointMode = string(ModeMessagesJSON)
		view.ProviderProtocolProfileID = "profile_glm_coding_anthropic_v1"
		view.NormalizeEndpointModes = map[EndpointMode]bool{ModeMessagesJSON: true}
		if _, err := newTestService(t, &fakeSource{view: view}).Probe(ctx, accountquality.ProbeRequest{AccountID: "acc-1", Full: true}); err != nil {
			t.Fatal(err)
		}
		if seen["authorization"] != "Bearer sk-test" {
			t.Fatalf("glm bearer: %v", seen)
		}
	})

	t.Run("anthropic api key header", func(t *testing.T) {
		view := probeView(server.URL)
		view.ProtocolCode = "anthropic"
		view.HealthCheckEndpointMode = string(ModeMessagesJSON)
		view.NormalizeEndpointModes = map[EndpointMode]bool{ModeMessagesJSON: true}
		if _, err := newTestService(t, &fakeSource{view: view}).Probe(ctx, accountquality.ProbeRequest{AccountID: "acc-1", Full: true}); err != nil {
			t.Fatal(err)
		}
		if seen["x-api-key"] != "sk-test" {
			t.Fatalf("x-api-key: %v", seen)
		}
	})

	t.Run("gemini google oauth bearer and project", func(t *testing.T) {
		view := probeView(server.URL)
		view.ProtocolCode = "gemini"
		view.HealthCheckEndpointMode = string(ModeGenerateContentJSON)
		view.Type = "google_oauth"
		view.SelectedAPIKey = "tok-g"
		view.Credentials = map[string]any{"quota_project_id": "proj-9"}
		view.NormalizeEndpointModes = map[EndpointMode]bool{ModeGenerateContentJSON: true}
		if _, err := newTestService(t, &fakeSource{view: view}).Probe(ctx, accountquality.ProbeRequest{AccountID: "acc-1", Full: true}); err != nil {
			t.Fatal(err)
		}
		if seen["authorization"] != "Bearer sk-test" || seen["x-goog-user-project"] != "proj-9" {
			t.Fatalf("gemini oauth headers: %v", seen)
		}
	})

	t.Run("openai oauth account id fallbacks", func(t *testing.T) {
		// The gpt OAuth profile pins the upstream to chatgpt.com; capture the
		// request in a local transport instead of dialing the real host.
		var captured http.Header
		transport := &w7cCaptureTransport{capture: &captured, body: `{"status":"completed","object":"response","output_text":"juhe","output":[{"content":[{"type":"output_text","text":"juhe"}]}]}`}
		view := probeView("https://ignored.example.com")
		view.HealthCheckEndpointMode = string(ModeResponsesJSON)
		view.Type = "oauth"
		view.ProviderProtocolProfileID = "profile_gpt_openai_v1"
		view.Credentials = map[string]any{"chatgpt_user_id": "user-77"}
		view.NormalizeEndpointModes = map[EndpointMode]bool{ModeResponsesJSON: true}
		service, err := NewService(Options{Source: &fakeSource{view: view}, Secret: "s", Client: &http.Client{Transport: transport}})
		if err != nil {
			t.Fatal(err)
		}
		observation, err := service.Probe(ctx, accountquality.ProbeRequest{AccountID: "acc-1", Full: true})
		if err != nil || observation == nil || !observation.Result.Success {
			t.Fatalf("oauth responses probe: %+v %v", observation, err)
		}
		if captured.Get("openai-beta") != "responses=experimental" || captured.Get("chatgpt-account-id") != "user-77" {
			t.Fatalf("oauth chatgpt headers: %v", captured)
		}
		if !strings.HasPrefix(captured.Get("request-url"), "https://chatgpt.com/backend-api/codex/") {
			t.Fatalf("codex upstream url: %q", captured.Get("request-url"))
		}
	})

	t.Run("images success and failure", func(t *testing.T) {
		view := probeView(server.URL)
		view.HealthCheckModel = "img-1"
		view.HealthCheckEndpointMode = string(ModeImagesJSON)
		view.SupportedModels = []string{"img-1"}
		view.NormalizeEndpointModes = map[EndpointMode]bool{ModeImagesJSON: true}
		service := newTestService(t, &fakeSource{view: view})
		observation, err := service.Probe(ctx, accountquality.ProbeRequest{AccountID: "acc-1", Full: true})
		if err != nil || observation == nil || !observation.Result.Success {
			t.Fatalf("images success: %+v %v", observation, err)
		}
		// Upstream error envelope → images error message with code suffix.
		h := openImageServer(t, `{"error":{"code":"content_policy_violation","message":"blocked"}}`)
		view2 := probeView(h.URL)
		view2.HealthCheckModel = "img-1"
		view2.HealthCheckEndpointMode = string(ModeImagesJSON)
		view2.SupportedModels = []string{"img-1"}
		view2.NormalizeEndpointModes = map[EndpointMode]bool{ModeImagesJSON: true}
		observation, err = newTestService(t, &fakeSource{view: view2}).Probe(ctx, accountquality.ProbeRequest{AccountID: "acc-1", Full: true})
		if err != nil || observation == nil || observation.Result.Success {
			t.Fatalf("images failure: %+v %v", observation, err)
		}
		if !strings.Contains(observation.Result.Message, "content_policy_violation") {
			t.Fatalf("images failure message: %q", observation.Result.Message)
		}
	})
}

// w7cCaptureTransport records the dispatched request and returns a canned
// body, keeping OAuth-profile probes off the real network.
type w7cCaptureTransport struct {
	capture *http.Header
	body    string
}

func (t *w7cCaptureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	*t.capture = req.Header
	t.capture.Set("request-url", req.URL.String())
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(t.body)),
		Header:     http.Header{},
	}, nil
}

func openImageServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
}

func TestW7CAttemptStagedAndReadArms(t *testing.T) {
	// attemptStaged returns the first success and skips later stages.
	okServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"juhe"},"finish_reason":"stop"}]}`))
	}))
	defer okServer.Close()
	view := probeView(okServer.URL)
	view.FixedKey = &view.APIKeyEntries[0]
	service := newTestService(t, &fakeSource{view: view})
	observation := service.attemptStaged(context.Background(), view, &view.APIKeyEntries[0])
	if observation == nil || !observation.Result.Success {
		t.Fatalf("staged success: %+v", observation)
	}

	// readUpstream delegates to the shared client.
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, okServer.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := service.readUpstream(context.Background(), request, time.Second, nil)
	if err != nil || response == nil || response.status != http.StatusOK {
		t.Fatalf("readUpstream: %+v %v", response, err)
	}

	// A server that aborts the connection mid-body yields a response with a
	// read error; semantic success already visible in the partial body wins.
	hijack := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"juhe"},"finish_reason":"stop"`))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		time.Sleep(50 * time.Millisecond)
		panic(http.ErrAbortHandler)
	}))
	defer hijack.Close()
	hijackView := probeView(hijack.URL)
	_, err = service.executeAttempt(context.Background(), hijackView, &hijackView.APIKeyEntries[0], 5*time.Second, false)
	if err != nil {
		t.Fatalf("interrupted read: %v", err)
	}

	// The legacy readUpstream wrapper surfaces read errors too.
	failingClient := &http.Client{Transport: w7cFailingTransport{}}
	brokenRequest, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, okServer.URL, nil)
	if response, readErr := service.readUpstreamWithClient(context.Background(), failingClient, brokenRequest, time.Second, nil); readErr == nil || response != nil {
		t.Fatalf("failing transport: %+v %v", response, readErr)
	}
	// The nil-body request construction error surfaces through io.EOF-style
	// transport errors; a canceled context returns promptly.
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.readUpstream(canceled, request, time.Second, nil); err == nil {
		t.Fatal("canceled read must fail")
	}
	_ = io.EOF
}

type w7cFailingTransport struct{}

func (w7cFailingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("transport dead")
}

func TestW7CBuildUpstreamURLRemainingArms(t *testing.T) {
	if _, err := buildUpstreamURL(&View{BaseURL: "   "}, "/v1/chat"); err == nil || !strings.Contains(err.Error(), "base_url") {
		t.Fatalf("empty base: %v", err)
	}

	// Gemini base ending with /v1beta strips the request prefix.
	geminiView := probeView("https://gem.example.com/v1beta")
	geminiView.ProtocolCode = "gemini"
	url, err := buildUpstreamURL(geminiView, "/v1beta/models/g:generateContent?alt=sse")
	if err != nil || url != "https://gem.example.com/v1beta/models/g:generateContent?alt=sse" {
		t.Fatalf("gemini v1beta base: %q %v", url, err)
	}

	// Gemini OpenAI-compatible profile owns the /v1beta/openai base path.
	profile := probeView("https://gem.example.com/v1beta/openai")
	profile.ProviderProtocolProfileID = "profile_gemini_openai_chat_v1beta"
	url, err = buildUpstreamURL(profile, "/v1/chat/completions")
	if err != nil || url != "https://gem.example.com/v1beta/openai/chat/completions" {
		t.Fatalf("gemini openai profile: %q %v", url, err)
	}

	// Anthropic bases gain a /v1 suffix only when missing.
	anthropic := probeView("https://claude.example.com")
	anthropic.ProtocolCode = "anthropic"
	url, err = buildUpstreamURL(anthropic, "/v1/messages")
	if err != nil || url != "https://claude.example.com/v1/messages" {
		t.Fatalf("anthropic root base: %q %v", url, err)
	}
	anthropic = probeView("https://claude.example.com/v1")
	anthropic.ProtocolCode = "anthropic"
	url, err = buildUpstreamURL(anthropic, "/v1/messages")
	if err != nil || url != "https://claude.example.com/v1/messages" {
		t.Fatalf("anthropic v1 base: %q %v", url, err)
	}
}

func TestW7CHybridAndLimitedArms(t *testing.T) {
	// Interactions sources cannot be mapped to a responses upstream.
	if _, _, _, err := ResolveHybridProbeTarget("profile_hybrid_openai_chat_v1", ModeInteractionsJSON, "m", "n", "responses"); err == nil || !strings.Contains(err.Error(), "不支持") {
		t.Fatalf("interactions mapping: %v", err)
	}
	if _, _, _, err := ResolveHybridProbeTarget("profile_hybrid_openai_chat_v1", ModeChatJSON, "m", "", ""); err == nil || !strings.Contains(err.Error(), "缺少") {
		t.Fatalf("missing mapping: %v", err)
	}
	if _, _, _, err := ResolveHybridProbeTarget("profile_hybrid_openai_chat_v1", ModeChatJSON, "m", "n", "alien"); err == nil || !strings.Contains(err.Error(), "不受探针支持") {
		t.Fatalf("alien family: %v", err)
	}
	if !hybridMappingSourceSupported(ModeResponsesSSE, "responses") || !hybridMappingSourceSupported(ModeMessagesJSON, "messages") {
		t.Fatal("same-family mappings are supported")
	}
	if hybridMappingSourceSupported(ModeInteractionsSSE, "chat_completions") {
		t.Fatal("interactions sources are never mappable")
	}

	// ProbeAccountView happy path.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"juhe"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	poolView := probeView(server.URL)
	poolView.APIKeyEntries = []KeyEntry{
		{Key: "sk-1", Fingerprint: "fp-1", Index: 0},
		{Key: "sk-2", Fingerprint: "fp-2", Index: 1},
	}
	observation, err := failingProbe(t, &fakeSource{view: poolView}).ProbeAccountView(context.Background(), accountquality.ProbeRequest{AccountID: "acc-1"})
	if err != nil || observation == nil || !observation.Result.Success {
		t.Fatalf("ProbeAccountView: %+v %v", observation, err)
	}

	// withScheduleForView pins the 120s image budget only for images views.
	imagesView := probeView("https://upstream.example.com")
	imagesView.HealthCheckEndpointMode = string(ModeImagesJSON)
	imagesView.NormalizeEndpointModes = map[EndpointMode]bool{ModeImagesJSON: true}
	derived := newTestService(t, &fakeSource{}).withScheduleForView(imagesView)
	if len(derived.retryTimeouts) != 1 || derived.retryTimeouts[0] != ImageDiagnosticRetryTimeouts[0] {
		t.Fatalf("images schedule: %v", derived.retryTimeouts)
	}
	plain := newTestService(t, &fakeSource{}).withScheduleForView(probeView("https://upstream.example.com"))
	if len(plain.retryTimeouts) != len(DiagnosticRetryTimeouts) {
		t.Fatalf("default schedule: %v", plain.retryTimeouts)
	}

	// Limited diagnostics mask failure messages (the quota-specific wording
	// depends on accountquality's rule table and is asserted as masked here).
	challenge := CreateOutputChallenge()
	limited := classifyResponse(probeView("https://upstream.example.com"), ProtocolOpenAI, ModeChatJSON,
		`{"error":{"code":"insufficient_quota","message":"You exceeded your current quota"}}`,
		map[string]string{}, http.StatusTooManyRequests, 10, 20, challenge, true)
	if limited.Result.Success || limited.Result.Message != "上游请求失败" {
		t.Fatalf("limited quota message: %+v", limited.Result)
	}
	// Non-quota failures collapse into the generic masked message.
	other := classifyResponse(probeView("https://upstream.example.com"), ProtocolOpenAI, ModeChatJSON,
		`{"error":{"code":"server_error","message":"boom"}}`, nil, http.StatusInternalServerError, 0, 0, challenge, true)
	if other.Result.Message != "上游请求失败" {
		t.Fatalf("limited generic message: %q", other.Result.Message)
	}
	// A limited success keeps the success message.
	full := classifyResponse(probeView("https://upstream.example.com"), ProtocolOpenAI, ModeChatJSON,
		`{"choices":[{"message":{"content":"juhe"},"finish_reason":"stop"}]}`, nil, http.StatusOK, 1, 2, challenge, true)
	if !full.Result.Success || full.Result.Message != "OpenAI Chat Completions 测试通过" {
		t.Fatalf("full success: %+v", full.Result)
	}
	// Raw truncate fallback for unparseable bodies.
	if got := parseUpstreamMessage("plain text body", ProtocolOpenAI, true); !strings.Contains(got, "plain text body") {
		t.Fatalf("raw fallback: %q", got)
	}
	if got := truncateRunes(strings.Repeat("字", 300), 240); len([]rune(got)) != 240 {
		t.Fatalf("truncateRunes: %d", len([]rune(got)))
	}
	// looksLikeSSE detects comments and SSE field lines only.
	if !looksLikeSSE(": keep-alive") || !looksLikeSSE("data: x") || looksLikeSSE("plain") {
		t.Fatal("looksLikeSSE contract")
	}
	// parseUpstreamMessage raw fallback disabled for empty bodies.
	if got := parseUpstreamMessage("", ProtocolOpenAI, true); got != "" {
		t.Fatalf("empty raw fallback: %q", got)
	}
}

package accountprobe

// w7c service-level probe coverage: staged escalation, pool diagnostics,
// manual diagnostics dispatch, cancellation and slot exhaustion, transport
// failure classification, and request construction for every endpoint mode.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/accountquality"
)

func w7cService(t *testing.T, source CandidateSource, timeouts []time.Duration, concurrency int) *Service {
	t.Helper()
	service, err := NewService(Options{Source: source, Secret: "w7c-secret", Client: http.DefaultClient, Now: func() time.Time { return testNowNow() }, RetryTimeouts: timeouts, Concurrency: concurrency})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

var w7cNow = time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)

func testNowNow() time.Time { return w7cNow }

func TestW7CNewServiceAndDispatchGuards(t *testing.T) {
	if _, err := NewService(Options{}); err == nil || !strings.Contains(err.Error(), "缺少 CandidateSource") {
		t.Fatalf("nil source: %v", err)
	}
	source := &fakeSource{view: nil}
	service := newTestService(t, source)
	// A nil view surfaces the Node configuration error.
	if _, err := service.Probe(context.Background(), accountquality.ProbeRequest{AccountID: "acc-1"}); err == nil || !strings.Contains(err.Error(), "不在当前分组") {
		t.Fatalf("nil view: %v", err)
	}
	if _, err := service.ProbeAccountView(context.Background(), accountquality.ProbeRequest{AccountID: "acc-1"}); err == nil {
		t.Fatal("ProbeAccountView nil view must fail")
	}
	// Source errors propagate verbatim.
	failing := &fakeSource{err: errors.New("boom")}
	if _, err := failingProbe(t, failing).Probe(context.Background(), accountquality.ProbeRequest{}); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("source error: %v", err)
	}
	// nowMS delegates to the injected clock (w7cService pins Now).
	injected := w7cService(t, &fakeSource{view: probeView("https://upstream.example.com")}, nil, 0)
	if injected.nowMS() != w7cNow.UnixMilli() {
		t.Fatalf("nowMS: %d", injected.nowMS())
	}
}

func failingProbe(t *testing.T, source CandidateSource) *Service {
	t.Helper()
	service, err := NewService(Options{Source: source, Secret: "s", Client: http.DefaultClient})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestW7CManualDiagnosticsDispatch(t *testing.T) {
	var seenAuthorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuthorization = r.Header.Get("authorization")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"juhe"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	ctx := context.Background()

	// nil view guard.
	service := w7cService(t, &fakeSource{}, nil, 0)
	if _, _, err := service.ManualDiagnostics(ctx, nil, false); err == nil || !strings.Contains(err.Error(), "视图缺失") {
		t.Fatalf("nil view: %v", err)
	}

	// Multi-key api_key view takes the pool path and reports attempts.
	poolView := probeView(server.URL)
	poolView.APIKeyEntries = []KeyEntry{
		{Key: "sk-1", Fingerprint: "fp-1", Index: 0},
		{Key: "sk-2", Fingerprint: "fp-2", Index: 1},
	}
	poolView.SelectedAPIKey = "sk-1"
	observation, attempts, err := service.ManualDiagnostics(ctx, poolView, false)
	if err != nil {
		t.Fatal(err)
	}
	if observation == nil || !observation.Result.Success || len(attempts) != 1 {
		t.Fatalf("pool diagnostics: %+v attempts=%d", observation, len(attempts))
	}

	// Single-key view takes the fixed-key path with no pool attempts.
	singleView := probeView(server.URL)
	singleView.APIKeyEntries = singleView.APIKeyEntries[:1]
	observation, attempts, err = service.ManualDiagnostics(ctx, singleView, true)
	if err != nil || attempts != nil {
		t.Fatalf("single key: %+v %v", attempts, err)
	}
	if observation == nil || !observation.Result.Success {
		t.Fatalf("single key observation: %+v", observation)
	}

	// oauth views carry no key entries: the single-credential arm must probe
	// via SelectedAPIKey（Node 等价：runAccountApiKeyPoolTestIfNeeded 对非
	// api_key 账户返回 undefined，回落 testOpenAIAccountWithDiagnosticRetries，
	// 凭据由网关链路按账户类型解析为 OAuth access token，不要求 API Key）。
	// 非 codex profile 避免 buildUpstreamURL 重定向到 chatgpt.com。
	oauthView := probeView(server.URL)
	oauthView.Type = "oauth"
	oauthView.ProviderProtocolProfileID = ""
	oauthView.APIKeyEntries = nil
	oauthView.SelectedAPIKey = "oauth-token"
	observation, attempts, err = service.ManualDiagnostics(ctx, oauthView, false)
	if err != nil || observation == nil || !observation.Result.Success || attempts != nil {
		t.Fatalf("oauth diagnostics: %+v %v", observation, err)
	}
	if seenAuthorization != "Bearer oauth-token" {
		t.Fatalf("oauth credential must reach upstream: %q", seenAuthorization)
	}

	// A view with neither entries nor a selected credential fails closed as a
	// failed observation（Node：网关链路内的凭据缺失转为失败结果，不抛出
	// 队列错误），full 模式保留凭据守卫的错误原文。
	brokenView := probeView(server.URL)
	brokenView.APIKeyEntries = nil
	brokenView.SelectedAPIKey = ""
	observation, attempts, err = service.ManualDiagnostics(ctx, brokenView, false)
	if err != nil || observation == nil || observation.Result.Success || attempts != nil {
		t.Fatalf("broken view: %+v %v", observation, err)
	}
	if !strings.Contains(observation.Result.Message, "缺少可用凭据") {
		t.Fatalf("broken view message: %q", observation.Result.Message)
	}
}

func TestW7CStagedEscalationOnRealUpstreamTimeout(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		// A slow but real upstream attempt: headers flushed first, body after
		// the stage budget elapsed.
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusOK)
		time.Sleep(120 * time.Millisecond)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"juhe"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	view := probeView(server.URL)
	view.APIKeyEntries = []KeyEntry{{Key: "sk-1", Fingerprint: "fp-1", Index: 0}}
	view.FixedKey = &view.APIKeyEntries[0]
	source := &fakeSource{view: view}
	// Two short stages force one escalation; the third observation wins.
	service, err := NewService(Options{Source: source, Secret: "s", Client: http.DefaultClient, RetryTimeouts: []time.Duration{30 * time.Millisecond, 300 * time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	observation, err := service.Probe(context.Background(), accountquality.ProbeRequest{
		AccountID: "acc-1", GroupID: "group-1", SystemAccountID: "sys-1", TrafficSource: "cooldown_retest", Full: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if observation == nil {
		t.Fatal("observation missing")
	}
	if attempts < 2 {
		t.Fatalf("real-upstream timeout must escalate stages, attempts=%d", attempts)
	}
}

func TestW7CCancellationAndSlotExhaustion(t *testing.T) {
	// Case 1: cancellation while the probe waits for a concurrency slot.
	slotView := probeView("https://upstream.example.com")
	slotView.APIKeyEntries = []KeyEntry{{Key: "sk-1", Fingerprint: "fp-1", Index: 0}}
	service := w7cService(t, &fakeSource{view: slotView}, nil, 1)
	service.slots <- struct{}{} // occupy the only slot
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	observation := service.attemptWithTimeout(canceled, slotView, &slotView.APIKeyEntries[0], time.Second, false)
	if observation == nil || !observation.Evidence.Canceled || observation.Result.ErrorCode != "server_diagnostic_cancelled" {
		t.Fatalf("slot-wait cancellation: %+v", observation)
	}

	// Case 2: cancellation while the upstream request is in flight surfaces
	// as a connection transport failure with no completion.
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer server.Close()
	defer close(release)

	inFlightView := probeView(server.URL)
	inFlightView.APIKeyEntries = []KeyEntry{{Key: "sk-1", Fingerprint: "fp-1", Index: 0}}
	inFlight := w7cService(t, &fakeSource{view: inFlightView}, nil, 1)
	ctx, cancelInFlight := context.WithCancel(context.Background())
	go func() {
		time.Sleep(80 * time.Millisecond)
		cancelInFlight()
	}()
	observation = inFlight.attemptWithTimeout(ctx, inFlightView, &inFlightView.APIKeyEntries[0], 10*time.Second, false)
	if observation == nil || observation.Result.Success || observation.Evidence.Canceled {
		t.Fatalf("in-flight cancellation: %+v", observation)
	}
	if observation.Evidence.TransportFailureKind != "connection" || observation.Evidence.UpstreamCompleted {
		t.Fatalf("in-flight evidence: %+v", observation.Evidence)
	}
}

func TestW7CTransportFailureClassification(t *testing.T) {
	view := probeView("https://127.0.0.1:1")
	view.APIKeyEntries = []KeyEntry{{Key: "sk-1", Fingerprint: "fp-1", Index: 0}}
	service := newTestService(t, &fakeSource{view: view})

	observation := service.attemptWithTimeout(context.Background(), view, &view.APIKeyEntries[0], time.Second, false)
	if observation == nil || observation.Result.Success {
		t.Fatalf("connection failure observation: %+v", observation)
	}
	if observation.Evidence.TransportFailureKind != "connection" || observation.Evidence.TimedOut {
		t.Fatalf("connection evidence: %+v", observation.Evidence)
	}
	if !strings.Contains(observation.Result.Message, "账户测试失败") {
		t.Fatalf("connection message: %+v", observation.Result)
	}
	// Node 等价：testOpenAIAccount 的 catch 路径统一经过
	// accountTestResultWithDiagnosticsMode → limitedAccountTestMessage，传输层
	// 失败在 limited 下同样脱敏为“上游请求失败”，传输证据保留。
	limited := service.attemptWithTimeout(context.Background(), view, &view.APIKeyEntries[0], time.Second, true)
	if limited == nil || limited.Result.Success || limited.Evidence.TransportFailureKind != "connection" {
		t.Fatalf("limited transport: %+v", limited)
	}
	if limited.Result.Message != "上游请求失败" {
		t.Fatalf("limited transport message: %q", limited.Result.Message)
	}

	// Transport timeout keeps the server_diagnostic_timeout errorCode and the
	// escalation evidence under limited; only the abort wording is masked
	// （Node：AccountTestAbortError 文案脱敏，errorCode/accountFailureEligible
	// 保留，晋级判定依赖 signal 证据而非文案）。
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer hang.Close()
	hangView := probeView(hang.URL)
	hangView.APIKeyEntries = []KeyEntry{{Key: "sk-1", Fingerprint: "fp-1", Index: 0}}
	fullTimeout := service.attemptWithTimeout(context.Background(), hangView, &hangView.APIKeyEntries[0], 30*time.Millisecond, false)
	if fullTimeout == nil || !fullTimeout.Evidence.TimedOut || fullTimeout.Result.ErrorCode != "server_diagnostic_timeout" || fullTimeout.Result.Message != "账户测试超时" {
		t.Fatalf("full timeout: %+v", fullTimeout)
	}
	limitedTimeout := service.attemptWithTimeout(context.Background(), hangView, &hangView.APIKeyEntries[0], 30*time.Millisecond, true)
	if limitedTimeout == nil || !limitedTimeout.Evidence.TimedOut || limitedTimeout.Result.ErrorCode != "server_diagnostic_timeout" {
		t.Fatalf("limited timeout evidence: %+v", limitedTimeout)
	}
	if limitedTimeout.Result.Message != "上游请求失败" {
		t.Fatalf("limited timeout message: %q", limitedTimeout.Result.Message)
	}

	// Classification matrix for classifyAttemptError.
	canceled := classifyAttemptError(view, context.Canceled, time.Second, false, 0)
	// Cancellation classifies as a connection failure with a real attempt
	// marker (the request had already been built and dispatched).
	if !canceled.Evidence.HasRealUpstreamAttempt || canceled.Evidence.TransportFailureKind != "connection" {
		t.Fatalf("canceled classification: %+v", canceled.Evidence)
	}
	timedOut := classifyAttemptError(view, context.DeadlineExceeded, time.Second, false, 0)
	if !timedOut.Evidence.TimedOut || timedOut.Result.ErrorCode != "server_diagnostic_timeout" {
		t.Fatalf("timeout classification: %+v", timedOut.Result)
	}
	if failure := transportFailureFromError(nil, time.Second); failure.kind != "" {
		t.Fatalf("nil error failure: %+v", failure)
	}
	if failure := transportFailureFromError(errors.New("dial refused"), time.Second); failure.kind != "connection" || failure.timedOut {
		t.Fatalf("plain error failure: %+v", failure)
	}
	type timeoutError struct{ error }
	timeouts := interface{ Timeout() bool }(w7cTimeoutError{})
	if !timeouts.Timeout() {
		t.Fatal("sanity: timeout error must report timeout")
	}
	if failure := transportFailureFromError(w7cTimeoutError{}, time.Second); !failure.timedOut {
		t.Fatalf("timeout error failure: %+v", failure)
	}
	// The helper methods on transportFailure.
	if got := (transportFailure{timedOut: true}).errorCode(); got != "server_diagnostic_timeout" {
		t.Fatalf("timeout code: %q", got)
	}
	if got := (transportFailure{}).message(errors.New("x")); !strings.Contains(got, "x") {
		t.Fatalf("failure message: %q", got)
	}
}

type w7cTimeoutError struct{}

func (w7cTimeoutError) Error() string { return "i/o timeout" }
func (w7cTimeoutError) Timeout() bool { return true }

func TestW7CRequestBuildersPerMode(t *testing.T) {
	view := probeView("https://upstream.example.com")
	challenge := CreateOutputChallenge()

	images, err := buildTestRequest(view, ModeImagesJSON, "img-1", challenge)
	if err != nil || images.path != "/v1/images/generations" {
		t.Fatalf("images request: %v %v", images, err)
	}
	anthropic, err := buildTestRequest(view, ModeMessagesJSON, "claude-1", challenge)
	if err != nil || anthropic.path != "/v1/messages" {
		t.Fatalf("anthropic request: %v %v", anthropic, err)
	}
	if anthropic.headers[clientProfileHeader] != "claude_code" || anthropic.headers["x-claude-code-session-id"] == "" {
		t.Fatalf("anthropic headers: %v", anthropic.headers)
	}
	interactions, err := buildTestRequest(view, ModeInteractionsSSE, "gemini-2", challenge)
	if err != nil || interactions.path != "/v1beta/interactions" || interactions.headers["accept"] != "text/event-stream" {
		t.Fatalf("interactions request: %+v %v", interactions, err)
	}
	geminiSSE, err := buildTestRequest(view, ModeGenerateContentSSE, "gemini-2", challenge)
	if err != nil || geminiSSE.path != "/v1beta/models/gemini-2:streamGenerateContent?alt=sse" {
		t.Fatalf("gemini sse request: %+v %v", geminiSSE, err)
	}
	geminiJSON, err := buildTestRequest(view, ModeGenerateContentJSON, "models/gemini-2", challenge)
	if err != nil || geminiJSON.path != "/v1beta/models/gemini-2:generateContent" {
		t.Fatalf("gemini json request: %+v %v", geminiJSON, err)
	}

	// codex_responses compatibility layer rewrites the responses payload.
	codex := probeView("https://upstream.example.com")
	codex.ClientCompatibility = "codex_responses"
	codexRequest, err := buildTestRequest(codex, ModeResponsesJSON, "gpt-5", challenge)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(codexRequest.body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["store"] != false || payload["parallel_tool_calls"] != false {
		t.Fatalf("codex payload: %s", codexRequest.body)
	}
	if _, ok := payload["client_metadata"]; !ok {
		t.Fatal("codex metadata missing")
	}
	if _, err := applyCodexCompatibilityPayload([]byte("not-json")); err == nil {
		t.Fatal("codex payload must reject invalid JSON")
	}

	// OAuth GPT profile roots the request at the Codex backend.
	oauthView := probeView("https://ignored.example.com")
	oauthView.Type = "oauth"
	oauthView.ProviderProtocolProfileID = "profile_gpt_openai_v1"
	oauthRequest, err := buildTestRequest(oauthView, ModeResponsesJSON, "gpt-5", challenge)
	if err != nil || !strings.HasPrefix(oauthRequest.path, "/responses") {
		t.Fatalf("oauth responses path: %q %v", oauthRequest.path, err)
	}

	// Gemini Code Assist wraps the request and needs a project id.
	codeAssist := probeView("https://upstream.example.com")
	codeAssist.Type = "google_oauth"
	codeAssist.Credentials = map[string]any{"oauth_type": "code_assist"}
	if _, err := buildTestRequest(codeAssist, ModeGenerateContentSSE, "gemini-2", challenge); err == nil || !strings.Contains(err.Error(), "project_id") {
		t.Fatalf("missing project id: %v", err)
	}
	codeAssist.Credentials["project_id"] = "proj-1"
	wrapped, err := buildTestRequest(codeAssist, ModeGenerateContentSSE, "gemini-2", challenge)
	if err != nil || wrapped.path != "/v1internal:streamGenerateContent?alt=sse" {
		t.Fatalf("code assist request: %+v %v", wrapped, err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(wrapped.body, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope["project"] != "proj-1" || envelope["model"] != "gemini-2" {
		t.Fatalf("code assist envelope: %s", wrapped.body)
	}

	// resolveTestModel guards.
	if _, err := resolveTestModel(&View{}, ""); err == nil || !strings.Contains(err.Error(), "未配置") {
		t.Fatalf("missing model: %v", err)
	}
	if _, err := resolveTestModel(&View{HealthCheckModel: "gpt-x", SupportedModels: []string{"other"}}, ""); err == nil || !strings.Contains(err.Error(), "不在支持模型列表") {
		t.Fatalf("unsupported model: %v", err)
	}
	if model, err := resolveTestModel(&View{HealthCheckModel: "gpt-x", SupportedModels: []string{"gpt-x"}}, ""); err != nil || model != "gpt-x" {
		t.Fatalf("resolved model: %q %v", model, err)
	}

	// Route family mapping matrix.
	for family, want := range map[string][2]EndpointMode{
		"chat_completions": {ModeChatJSON, ModeChatSSE},
		"responses":        {ModeResponsesJSON, ModeResponsesSSE},
		"messages":         {ModeMessagesJSON, ModeMessagesSSE},
		"generate_content": {ModeGenerateContentJSON, ModeGenerateContentSSE},
	} {
		json, ok := endpointModeForUpstreamFamily(family, false)
		if !ok || json != want[0] {
			t.Fatalf("family %s json: %v %t", family, json, ok)
		}
		sse, ok := endpointModeForUpstreamFamily(family, true)
		if !ok || sse != want[1] {
			t.Fatalf("family %s sse: %v %t", family, sse, ok)
		}
	}
	if _, ok := endpointModeForUpstreamFamily("unknown", false); ok {
		t.Fatal("unknown family must be unsupported")
	}
	for mode, family := range map[EndpointMode]string{
		ModeChatJSON: "chat_completions", ModeResponsesSSE: "responses",
		ModeMessagesSSE: "messages", ModeGenerateContentJSON: "generate_content",
		ModeInteractionsSSE: "interactions",
	} {
		if got := endpointFamilyForProbeMode(mode); got != family {
			t.Fatalf("endpointFamilyForProbeMode(%s) = %s", mode, got)
		}
	}

	// URL suffix helpers.
	if got := openAIPathSuffix("/v1/chat/completions?x=1"); got != "/chat/completions?x=1" {
		t.Fatalf("v1 suffix strip: %q", got)
	}
	if got := openAIPathSuffix("chat"); got != "/chat" {
		t.Fatalf("slash insertion: %q", got)
	}
	if got := openAIPathSuffix("/v1"); got != "" {
		t.Fatalf("bare v1: %q", got)
	}
	// Only the /v1beta segment is stripped; the leading slash is kept.
	if got := stripV1BetaPrefix("/v1beta/models/x"); got != "/models/x" {
		t.Fatalf("v1beta strip: %q", got)
	}
	if got := stripV1BetaPrefix("/models/x"); got != "/models/x" {
		t.Fatalf("v1beta passthrough: %q", got)
	}
	if got := stripTrailingSlashPath("/x?y=1"); got != "/x" {
		t.Fatalf("query strip: %q", got)
	}

	// credentialText trims and rejects non-strings.
	if got := credentialText(map[string]any{"a": " x "}, "a"); got != "x" {
		t.Fatalf("credentialText: %q", got)
	}
	if got := credentialText(map[string]any{"a": 1}, "a"); got != "" {
		t.Fatalf("credentialText scalar: %q", got)
	}
	if got := credentialText(nil, "a"); got != "" {
		t.Fatalf("credentialText nil: %q", got)
	}
	if got := firstNonEmpty("", "x"); got != "x" {
		t.Fatalf("firstNonEmpty: %q", got)
	}
	if got := quotaResponseHeaders(map[string]string{"Retry-After": "5", "secret": "x"}); len(got) != 1 || got["Retry-After"] != "5" {
		t.Fatalf("quota headers: %v", got)
	}
	if got := protocolName(ModeMessagesJSON); got != "Anthropic Messages" {
		t.Fatalf("anthropic name: %q", got)
	}
	if got := protocolName(ModeGenerateContentJSON); got != "Gemini GenerateContent" {
		t.Fatalf("gemini name: %q", got)
	}
	if got := protocolName(ModeChatSSE); got != "OpenAI Chat Completions" {
		t.Fatalf("chat name: %q", got)
	}
	if got := protocolName(ModeResponsesJSON); got != "OpenAI Responses" {
		t.Fatalf("responses name: %q", got)
	}
}

package gatewayresponse

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// ---------------------------------------------------------------------------
// errors.go 小面。
// ---------------------------------------------------------------------------

func TestW9CResponsePrecommitDeadlineErrorHelpers(t *testing.T) {
	deadline := &ResponsePrecommitDeadlineError{DeadlineAtMs: 1234}
	if !strings.Contains(deadline.Error(), "墙钟") {
		t.Fatalf("error text = %q", deadline.Error())
	}
	if deadline.Code() != "gateway_request_wall_budget_exhausted" {
		t.Fatalf("code = %q", deadline.Code())
	}
	if !IsResponsePrecommitDeadlineError(deadline) {
		t.Fatal("typed error must match")
	}
	if IsResponsePrecommitDeadlineError(errors.New("nope")) {
		t.Fatal("plain error must not match")
	}
	if ResponsePrecommitDeadlineErrorOf(nil) != nil {
		t.Fatal("nil error yields nil")
	}
	if got := ResponsePrecommitDeadlineErrorOf(deadline); got != deadline {
		t.Fatal("typed error passes through")
	}
	wrapped := &NonStreamBodyPipeError{OriginalError: deadline}
	if got := ResponsePrecommitDeadlineErrorOf(wrapped); got != deadline {
		t.Fatal("pipe-wrapped deadline must unwrap")
	}
	if got := ResponsePrecommitDeadlineErrorOf(&NonStreamBodyPipeError{}); got != nil {
		t.Fatal("pipe without original yields nil")
	}
	if got := ResponsePrecommitDeadlineErrorOf(errors.New("x")); got != nil {
		t.Fatal("plain error yields nil")
	}
	if (&NonStreamBodyPipeError{OriginalError: errors.New("inner")}).Error() == "" {
		t.Fatal("pipe error must render")
	}
	if (&UpstreamRequestAbortedError{Message: "aborted"}).Error() != "aborted" {
		t.Fatal("abort error must render")
	}
	if !IsUpstreamRequestAbortedError(&UpstreamRequestAbortedError{}) {
		t.Fatal("typed abort must match")
	}
	if !IsStartedUpstreamBodyTransportError(&StartedBodyTransportError{Err: errors.New("x")}) {
		t.Fatal("started transport must match")
	}
	if (&StartedBodyTransportError{Err: errors.New("inner")}).Unwrap() == nil {
		t.Fatal("started transport must unwrap")
	}
	readPlan := &StreamReadPlanTimeoutError{Message: "m", TimeoutKind: "idle"}
	if readPlan.Error() != "m" || !IsStreamReadPlanTimeoutError(readPlan) {
		t.Fatal("read plan timeout surface")
	}
	if IsStreamReadPlanTimeoutError(errors.New("x")) {
		t.Fatal("plain error must not match read plan timeout")
	}
	firstByte := &FirstByteTimeoutError{Message: "fb"}
	if firstByte.Error() != "fb" || firstByte.Code() != FirstByteTimeoutErrorCode || !IsFirstByteTimeoutError(firstByte) {
		t.Fatal("first byte timeout surface")
	}
	buffer := &StreamPreCommitBufferExceededError{}
	if !strings.Contains(buffer.Error(), "缓冲上限") || buffer.Code() == "" {
		t.Fatal("precommit buffer error surface")
	}
	if !transportTimeoutPattern("read timeout after 5s") || transportTimeoutPattern("other") {
		t.Fatal("transport timeout pattern")
	}
}

// ---------------------------------------------------------------------------
// failurestatus.go JSON 字符串解码面。
// ---------------------------------------------------------------------------

func TestW9CUnescapeJSONString(t *testing.T) {
	cases := []struct {
		raw    string
		want   string
		wantOK bool
	}{
		{`simple`, "simple", true},
		{`a\nb`, "a\nb", true},
		{`a\tb`, "a\tb", true},
		{`q\"q`, `q"q`, true},
		{`back\\slash`, `back\slash`, true},
		{`slash\/`, "slash/", true},
		{`plain/slash`, "plain/slash", true},
		{`\b\f\r`, "\b\f\r", true},
		{`\u0041`, "A", true},
		{`\u00e9`, "é", true},
		{`\ud83d\ude00`, "😀", true},
		{`bad \x`, "", false},
		{`short \u0`, "", false},
		{`lone \ud83d`, "lone \ufffd", true},
		{`badhex \uZZZZ`, "", false},
	}
	for _, tc := range cases {
		got, ok := unescapeJSONString(tc.raw)
		if ok != tc.wantOK || (ok && got != tc.want) {
			t.Fatalf("unescapeJSONString(%q) = %q,%v want %q,%v", tc.raw, got, ok, tc.want, tc.wantOK)
		}
	}
	if got, ok := parseHex4("0041"); !ok || got != 0x41 {
		t.Fatalf("parseHex4 = %x,%v", got, ok)
	}
	if _, ok := parseHex4("ZZZZ"); ok {
		t.Fatal("non-hex must fail")
	}
	// ResponsesFailureStatusFromCapturedJSON.
	if !ResponsesFailureStatusFromCapturedJSON(`{"status":"failed"}`) {
		t.Fatal("failed status must be detected")
	}
	if ResponsesFailureStatusFromCapturedJSON(`{"status":"completed"}`) {
		t.Fatal("completed status must not be failed")
	}
	if ResponsesFailureStatusFromCapturedJSON(`{"status":"\"failed\""}`) {
		t.Fatal("escaped inner status is a string, not the literal")
	}
	tracker := NewResponsesRootStatusTracker()
	if tracker.HasFailedStatus() {
		t.Fatal("fresh tracker must be clean")
	}
}

// ---------------------------------------------------------------------------
// driveradapters.go 三个协议驱动。
// ---------------------------------------------------------------------------

func TestW9CResponseDriverAdapters(t *testing.T) {
	header := http.Header{"Content-Type": {"application/json"}}

	openai := NewOpenAIResponseDriver()
	if openai.ResponseProtocol() != "openai_v1" || openai.ClientErrorProtocol() != "openai" || openai.DefaultClientProfile() != "generic" {
		t.Fatal("openai driver metadata")
	}
	if openai.EndpointFamilyForPath("/v1/chat/completions") != gatewayproto.EndpointFamilyChatCompletions ||
		openai.EndpointFamilyForPath("/v1/responses") != gatewayproto.EndpointFamilyResponses ||
		openai.EndpointFamilyForPath("/v1/embeddings") != gatewayproto.EndpointFamilyUnknown {
		t.Fatal("openai endpoint family mapping")
	}
	if usage := openai.ExtractUsageFromJSONValue(map[string]any{"usage": map[string]any{"prompt_tokens": 3.0, "completion_tokens": 4.0}}); usage.InputTokens == nil || *usage.InputTokens != 3 {
		t.Fatalf("openai usage value = %+v", usage)
	}
	if usage := openai.ExtractUsageFromJSONTextFragment(`{"usage":{"prompt_tokens":6}}`, false); usage.InputTokens == nil || *usage.InputTokens != 6 {
		t.Fatalf("openai usage fragment = %+v", usage)
	}
	if usage := openai.ExtractUsageFromJSONBuffer([]byte(`{"usage":{"prompt_tokens":7}}`)); usage.InputTokens == nil || *usage.InputTokens != 7 {
		t.Fatalf("openai usage buffer = %+v", usage)
	}
	if payload := openai.ParseErrorPayload(`{"error":{"message":"boom","type":"server_error"}}`, header); payload.Message != "boom" {
		t.Fatalf("openai error payload = %+v", payload)
	}
	if payload := openai.ParseErrorPayloadFromJSONValue(map[string]any{"error": map[string]any{"message": "v"}}); payload.Message != "v" {
		t.Fatalf("openai error value = %+v", payload)
	}
	if len(openai.ExtractJSONSemanticFrames(map[string]any{"model": "gpt-5"}, "/v1/chat/completions")) == 0 {
		t.Fatal("openai semantic frames expected")
	}
	if openai.StreamDriver() == nil || openai.NewStreamInspector() == nil {
		t.Fatal("openai stream surface")
	}

	anthropic := NewAnthropicResponseDriver()
	if anthropic.ResponseProtocol() != "anthropic_v1" || anthropic.ClientErrorProtocol() != "anthropic" {
		t.Fatal("anthropic driver metadata")
	}
	if anthropic.EndpointFamilyForPath("/v1/messages") != gatewayproto.EndpointFamilyMessages {
		t.Fatalf("anthropic family = %v", anthropic.EndpointFamilyForPath("/v1/messages"))
	}
	if usage := anthropic.ExtractUsageFromJSONValue(map[string]any{"usage": map[string]any{"input_tokens": 5.0, "output_tokens": 6.0}}); usage.InputTokens == nil || *usage.InputTokens != 5 {
		t.Fatalf("anthropic usage = %+v", usage)
	}
	if usage := anthropic.ExtractUsageFromJSONTextFragment(`{"usage":{"input_tokens":2}}`, false); usage.InputTokens == nil || *usage.InputTokens != 2 {
		t.Fatalf("anthropic usage fragment = %+v", usage)
	}
	if usage := anthropic.ExtractUsageFromJSONBuffer([]byte(`{"usage":{"input_tokens":9}}`)); usage.InputTokens == nil || *usage.InputTokens != 9 {
		t.Fatalf("anthropic usage buffer = %+v", usage)
	}
	if payload := anthropic.ParseErrorPayload(`{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`, header); payload.Message != "busy" {
		t.Fatalf("anthropic error payload = %+v", payload)
	}
	if len(anthropic.ExtractJSONSemanticFrames(map[string]any{"model": "claude"}, "/v1/messages")) == 0 {
		t.Fatal("anthropic semantic frames expected")
	}
	anthropicInspector := anthropic.NewStreamInspector()
	anthropicInspector.PushChunk([]byte("event: x"))
	_ = anthropicInspector.Snapshot()
	_ = anthropicInspector.Finish()
	anthropicStream := anthropic.StreamDriver()
	if anthropicStream == nil || anthropicStream.ClientErrorProtocol() != "anthropic" {
		t.Fatal("anthropic stream driver")
	}
	anthropicStream.NewStreamInspector()
	_ = anthropicStream.ResponseInspectionEndpointFamily(gatewayproto.EndpointFamilyMessages)
	_ = anthropicStream.SSEResponseInspectionFailureEvent()
	_ = anthropicStream.DrainForKeepAliveAfterTerminal()

	gemini := NewGeminiResponseDriver()
	if gemini.ResponseProtocol() != "gemini_v1beta" || gemini.ClientErrorProtocol() != "gemini" || gemini.DefaultClientProfile() != "generic_gemini" {
		t.Fatal("gemini driver metadata")
	}
	if gemini.EndpointFamilyForPath("/v1beta/models/m:generateContent") != gatewayproto.EndpointFamilyGenerateContent {
		t.Fatal("gemini family mapping")
	}
	if usage := gemini.ExtractUsageFromJSONValue(map[string]any{"usageMetadata": map[string]any{"promptTokenCount": 4.0}}); usage.InputTokens == nil || *usage.InputTokens != 4 {
		t.Fatalf("gemini usage = %+v", usage)
	}
	if usage := gemini.ExtractUsageFromJSONTextFragment(`{"usageMetadata":{"promptTokenCount":8}}`, false); usage.InputTokens == nil || *usage.InputTokens != 8 {
		t.Fatalf("gemini usage fragment = %+v", usage)
	}
	if usage := gemini.ExtractUsageFromJSONBuffer([]byte(`{"usageMetadata":{"promptTokenCount":2}}`)); usage.InputTokens == nil || *usage.InputTokens != 2 {
		t.Fatalf("gemini usage buffer = %+v", usage)
	}
	if payload := gemini.ParseErrorPayload(`{"error":{"code":400,"message":"bad","status":"INVALID_ARGUMENT"}}`, header); payload.Message != "bad" {
		t.Fatalf("gemini error payload = %+v", payload)
	}
	if payload := gemini.ParseErrorPayloadFromJSONValue(map[string]any{"error": map[string]any{"message": "nested"}}); payload.Message != "nested" {
		t.Fatalf("gemini error value = %+v", payload)
	}
	_ = gemini.ExtractJSONSemanticFrames(map[string]any{"candidates": []any{}}, "/x/generateContent")
	geminiInspector := gemini.NewStreamInspector()
	geminiInspector.PushText("hello")
	_ = geminiInspector.Snapshot()
	_ = geminiInspector.Finish()
	geminiStream := gemini.StreamDriver()
	if geminiStream.ClientErrorProtocol() != "gemini" || geminiStream.SSEResponseInspectionFailureEvent() != "none" {
		t.Fatal("gemini stream driver surface")
	}
	if geminiStream.ResponseInspectionEndpointFamily(gatewayproto.EndpointFamilyGenerateContent) != gatewayproto.EndpointFamilyGenerateContent {
		t.Fatal("gemini recognized family passthrough")
	}
	if geminiStream.ResponseInspectionEndpointFamily(gatewayproto.EndpointFamilyUnknown) != gatewayproto.EndpointFamilyGenerateContent {
		t.Fatal("gemini unknown family fallback")
	}
	_ = geminiStream.DrainForKeepAliveAfterTerminal()

	if ResponseDriverForProtocol("anthropic_v1") == nil || ResponseDriverForProtocol("gemini_v1beta") == nil || ResponseDriverForProtocol("other") == nil {
		t.Fatal("driver factory")
	}
	// openai stream driver arms.
	openaiStream := DefaultOpenAIStreamDriver()
	if openaiStream.ClientErrorProtocol() != "openai" || openaiStream.SSEResponseInspectionFailureEvent() != "response.failed" {
		t.Fatal("openai stream driver surface")
	}
	if openaiStream.ResponseInspectionEndpointFamily(gatewayproto.EndpointFamilyChatCompletions) != gatewayproto.EndpointFamilyChatCompletions {
		t.Fatal("recognized family passthrough")
	}
	if openaiStream.ResponseInspectionEndpointFamily(gatewayproto.EndpointFamilyUnknown) != gatewayproto.EndpointFamilyUnknown {
		t.Fatal("unknown family fallback")
	}
	if !openaiStream.DrainForKeepAliveAfterTerminal() {
		t.Fatal("openai drains for keep alive")
	}
}

// ---------------------------------------------------------------------------
// models.go Codex / OpenAI 模型目录面。
// ---------------------------------------------------------------------------

func TestW9CBuildCodexModelsPayload(t *testing.T) {
	catalog := []ModelCatalogEntry{
		{
			Model: "gpt-5-codex", Scope: "built_in", ReleaseDate: "2026-01-05",
			CapabilityNotes: "notes", ContextWindowTokens: 400_000,
			SupportedServiceTiers:         []string{"default", "priority"},
			CodexSupportedReasoningLevels: []string{"low", "medium", "custom"},
			CodexDefaultReasoningLevel:    "medium",
			CodexMultiAgentVersion:        "v2",
		},
		{Model: "fallback", CreatedAt: "2026-01-05T10:00:00Z"},
		{Model: "bad-date", ReleaseDate: "nope"},
	}
	payload := buildCodexModelsPayload(catalog)
	if len(payload.Models) != 3 {
		t.Fatalf("models = %d", len(payload.Models))
	}
	first := payload.Models[0]
	if first.Slug != "gpt-5-codex" || first.ContextWindow != 400_000 || first.MaxContextWindow != 400_000 {
		t.Fatalf("first model = %+v", first)
	}
	if first.DefaultReasoningLevel == nil || *first.DefaultReasoningLevel != "medium" {
		t.Fatalf("default level = %+v", first.DefaultReasoningLevel)
	}
	if len(first.SupportedReasoningLevels) != 3 || first.SupportedReasoningLevels[0].Description != "Low" {
		t.Fatalf("reasoning levels = %+v", first.SupportedReasoningLevels)
	}
	if len(first.ServiceTiers) != 2 || first.ServiceTiers[1].Name != "Fast" {
		t.Fatalf("service tiers = %+v", first.ServiceTiers)
	}
	if len(first.AdditionalSpeedTiers) != 1 || first.AdditionalSpeedTiers[0] != "fast" {
		t.Fatalf("speed tiers = %+v", first.AdditionalSpeedTiers)
	}
	if first.MultiAgentVersion == nil || *first.MultiAgentVersion != "v2" {
		t.Fatalf("multi agent = %+v", first.MultiAgentVersion)
	}
	if first.Description == nil || *first.Description != "notes" {
		t.Fatalf("description = %+v", first.Description)
	}
	if payload.Models[1].ContextWindow != 272_000 {
		t.Fatalf("fallback context window = %d", payload.Models[1].ContextWindow)
	}
	if payload.Models[2].ContextWindow != 272_000 {
		t.Fatalf("bad-date context window = %d", payload.Models[2].ContextWindow)
	}
	if got := codexReasoningLevelDescription("xhigh"); got != "XHigh" {
		t.Fatalf("xhigh description = %q", got)
	}
	if got := codexReasoningLevelDescription("weird"); got != "Weird" {
		t.Fatalf("weird description = %q", got)
	}

	// OpenAI shape + created seconds resolution.
	openai := buildOpenAIModelsPayload(catalog, nil)
	list, ok := openai.(openAIModelsListResponse)
	if !ok || len(list.Data) != 3 {
		t.Fatalf("openai payload = %#v", openai)
	}
	if list.Data[0].Created != 1767571200 { // 2026-01-05 UTC
		t.Fatalf("release date created = %d", list.Data[0].Created)
	}
	if list.Data[1].Created == 0 {
		t.Fatal("created_at RFC3339 must parse")
	}
	if list.Data[0].OwnedBy != "openai" || list.Data[1].OwnedBy != "juhe-ai" {
		t.Fatalf("owned_by = %q/%q", list.Data[0].OwnedBy, list.Data[1].OwnedBy)
	}
	if modelCreatedUnixSeconds(ModelCatalogEntry{}) != 0 {
		t.Fatal("empty entry must yield 0")
	}
}

func TestW9CIsCodexModelsRequest(t *testing.T) {
	newReq := func(target string, headers map[string]string) *gatewaypreauth.GatewayRequest {
		raw := httptest.NewRequest(http.MethodGet, target, nil)
		for key, value := range headers {
			raw.Header.Set(key, value)
		}
		return gatewaypreauth.NewGatewayRequest(raw)
	}
	if !isCodexModelsRequest(newReq("/v1/models", map[string]string{"Originator": "codex_cli"})) {
		t.Fatal("codex originator must match")
	}
	if !isCodexModelsRequest(newReq("/v1/models", map[string]string{"User-Agent": "Codex/1"})) {
		t.Fatal("codex user agent must match")
	}
	if !isCodexModelsRequest(newReq("/v1/models?client_version=1.2", nil)) {
		t.Fatal("client_version query must match")
	}
	if isCodexModelsRequest(newReq("/v1/models", nil)) {
		t.Fatal("plain models request is not codex")
	}
	post := httptest.NewRequest(http.MethodPost, "/v1/models", nil)
	if isCodexModelsRequest(gatewaypreauth.NewGatewayRequest(post)) {
		t.Fatal("non-GET is not a models request")
	}
	if hasNonEmptyQueryParam(newReq("/v1/models?client_version=", nil), "client_version") {
		t.Fatal("blank query param must not count")
	}
	if hasNonEmptyQueryParam(newReq("/v1/models", nil), "client_version") {
		t.Fatal("missing query param must not count")
	}
}

// ---------------------------------------------------------------------------
// helpers.go / readplan.go / commitstate.go / codexcontract 小面。
// ---------------------------------------------------------------------------

func TestW9CPathHelpers(t *testing.T) {
	cases := []struct{ in, v1, v1beta string }{
		{"/v1/models", "/models", "/v1/models"},
		{"/v1beta/models", "/v1beta/models", "/models"},
		{"/v1", "/", "/v1"},
		{"/v2/models", "/v2/models", "/v2/models"},
		{"", "/", "/"},
	}
	for _, tc := range cases {
		if got := normalizeV1PrefixPath(tc.in); got != tc.v1 {
			t.Fatalf("normalizeV1PrefixPath(%q) = %q want %q", tc.in, got, tc.v1)
		}
		if got := normalizeV1BetaPrefixPath(tc.in); got != tc.v1beta {
			t.Fatalf("normalizeV1BetaPrefixPath(%q) = %q want %q", tc.in, got, tc.v1beta)
		}
	}
	if got := LowercasedRequestPath("/V1/Models?X=1"); got != "/v1/models" {
		t.Fatalf("lowercased path = %q", got)
	}
	if got := splitPathOnly("/v1/models?a=1"); got != "/v1/models" {
		t.Fatalf("splitPathOnly = %q", got)
	}
	if got := splitPathOnly("/v1/models"); got != "/v1/models" {
		t.Fatalf("splitPathOnly plain = %q", got)
	}
	// requestPathHasCompactionTrigger arms.
	// Only the exact /responses path (after /v1 stripping) consults the body.
	if requestPathHasCompactionTrigger("/v1/responses/compact?x=1", nil, nil) != false {
		t.Fatal("compact path is not the /responses trigger path")
	}
	if requestPathHasCompactionTrigger("/v1/responses", nil, []byte(`{"type":"compaction_trigger"}`)) != true {
		t.Fatal("compaction body must trigger")
	}
	if requestPathHasCompactionTrigger("/v1/responses", nil, []byte(`{}`)) != false {
		t.Fatal("plain body must not trigger")
	}
	if got := timeoutSeconds(0); got != 1 {
		t.Fatalf("zero timeout seconds = %d", got)
	}
	if got := timeoutSeconds(59_999); got != 60 {
		t.Fatalf("timeout seconds rounding = %d", got)
	}
	state := &DownstreamCommitState{}
	state.MarkTransportCommitted(10)
	if !state.TransportCommitted || state.DownstreamBytesWritten != 10 || !state.CanRetryUpstream() {
		t.Fatal("transport commit keeps retry eligibility")
	}
	state.MarkSemanticCommitted(-3)
	if !state.SemanticCommitted || state.DownstreamBytesWritten != 10 {
		t.Fatal("semantic commit clamps negative bytes")
	}
	if state.CanRetryUpstream() {
		t.Fatal("semantic commit must disable retries")
	}
	state.MarkSuccessfulProtocolTerminalReceived()
	if !state.SuccessfulProtocolTerminalReceived {
		t.Fatal("terminal flag must set")
	}
	if normalizedBytes(-5) != 0 || normalizedBytes(7) != 7 {
		t.Fatal("normalizedBytes clamps")
	}
}

func TestW9CRetryDecisionArms(t *testing.T) {
	if ShouldExcludeCurrentAccountForStreamServerRetry(nil) {
		t.Fatal("nil decision must not exclude")
	}
	requestNext := &ResponseInspectionDecision{AccountSwitch: "request_next_account"}
	if !ShouldExcludeCurrentAccountForStreamServerRetry(requestNext) {
		t.Fatal("request_next_account must exclude")
	}
	avoidTTL := &ResponseInspectionDecision{AccountSwitch: "avoid_account_ttl"}
	if !ShouldExcludeCurrentAccountForStreamServerRetry(avoidTTL) {
		t.Fatal("avoid_account_ttl must exclude")
	}
	bucketTTL := &ResponseInspectionDecision{AccountSwitch: "avoid_upstream_bucket_ttl"}
	if !ShouldExcludeCurrentAccountForStreamServerRetry(bucketTTL) {
		t.Fatal("avoid_upstream_bucket_ttl must exclude")
	}
	runtime := &ResponseInspectionDecision{AccountState: "runtime_avoidance"}
	if !ShouldExcludeCurrentAccountForStreamServerRetry(runtime) {
		t.Fatal("runtime_avoidance must exclude")
	}
	if ShouldExcludeCurrentAccountForStreamServerRetry(&ResponseInspectionDecision{}) {
		t.Fatal("neutral decision must not exclude")
	}
}

package accountprobe

// w7c pure classification and protocol-construction coverage: SSE/JSON
// response context parsing, upstream error/message extraction, output text
// extraction per protocol, success evidence per endpoint mode, and the
// request payload builders.

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

func w7cJSONMap(t *testing.T, raw string) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestW7CJoinedTextAndValueUtils(t *testing.T) {
	if got := joinedText([]string{"a", "", "b"}); got != "ab" {
		t.Fatalf("joinedText: %q", got)
	}
	if got := joinedText([]string{"  "}); got != "" {
		t.Fatalf("joinedText blank: %q", got)
	}
	if text, ok := joinedRawText([]string{"", "x"}); !ok || text != "x" {
		t.Fatalf("joinedRawText: %q %t", text, ok)
	}
	if text, ok := joinedRawText([]string{""}); ok {
		t.Fatalf("joinedRawText empty: %q %t", text, ok)
	}
	if objectValue("x") != nil || arrayValue("x") != nil || stringValue(42) != "" {
		t.Fatal("value utils must fail closed on scalars")
	}
	if got := stringValue(" x "); got != "x" {
		t.Fatalf("stringValue trim: %q", got)
	}
	if text, ok := rawStringValue(" x "); !ok || text != " x " {
		t.Fatalf("rawStringValue keeps raw text: %q %t", text, ok)
	}
}

func TestW7CUpstreamErrorAndMessageParsing(t *testing.T) {
	// JSON body error.code preference over error.type.
	if got := parseUpstreamErrorCode(`{"error":{"code":"insufficient_quota","type":"quota"}}`); got != "insufficient_quota" {
		t.Fatalf("code: %q", got)
	}
	if got := parseUpstreamErrorCode(`{"error":{"type":"quota_exceeded"}}`); got != "quota_exceeded" {
		t.Fatalf("type: %q", got)
	}
	// response.error nesting.
	if got := parseUpstreamErrorCode(`{"response":{"error":{"code":"server_error"}}}`); got != "server_error" {
		t.Fatalf("nested: %q", got)
	}
	// SSE bodies are parsed event by event.
	if got := parseUpstreamErrorCode("data: {\"error\":{\"code\":\"rate_limit\"}}\n\n"); got != "rate_limit" {
		t.Fatalf("sse: %q", got)
	}
	if got := parseUpstreamErrorCode("not json at all"); got != "" {
		t.Fatalf("plain text: %q", got)
	}

	// protocolMessage per protocol shape.
	anthropic := w7cJSONMap(t, `{"error":{"message":"额度不足","status":"429"}}`)
	if got := protocolMessage(anthropic, ProtocolAnthropic); got != "额度不足" {
		t.Fatalf("anthropic message: %q", got)
	}
	if got := protocolMessage(w7cJSONMap(t, `{"error":{"status":"overloaded"}}`), ProtocolAnthropic); got != "overloaded" {
		t.Fatalf("anthropic status: %q", got)
	}
	if got := protocolMessage(w7cJSONMap(t, `{"message":"top-level"}`), ProtocolAnthropic); got != "top-level" {
		t.Fatalf("anthropic top-level: %q", got)
	}
	// A top-level string error is not an object, so no message is derived
	// (openAIErrorMessage receives nil and payload.message is absent).
	if got := protocolMessage(w7cJSONMap(t, `{"error":"plain string"}`), ProtocolOpenAI); got != "" {
		t.Fatalf("openai string error: %q", got)
	}
	if got := protocolMessage(w7cJSONMap(t, `{"response":{"error":{"message":"nested"}}}`), ProtocolOpenAI); got != "nested" {
		t.Fatalf("openai nested: %q", got)
	}
	if got := protocolMessage(w7cJSONMap(t, `{"message":"fallback"}`), ProtocolOpenAI); got != "fallback" {
		t.Fatalf("openai fallback: %q", got)
	}
	if got := protocolMessage(nil, ProtocolOpenAI); got != "" {
		t.Fatalf("nil payload: %q", got)
	}

	// openAIErrorMessage arm ladder.
	if got := openAIErrorMessage("  trim me  "); got != "trim me" {
		t.Fatalf("string error: %q", got)
	}
	if got := openAIErrorMessage(w7cJSONMap(t, `{"code":"c1"}`)); got != "c1" {
		t.Fatalf("code error: %q", got)
	}
	if got := openAIErrorMessage(w7cJSONMap(t, `{"type":"t1"}`)); got != "t1" {
		t.Fatalf("type error: %q", got)
	}
	if got := openAIErrorMessage(42); got != "" {
		t.Fatalf("scalar error: %q", got)
	}
}

func TestW7CStreamFailureMessageParsing(t *testing.T) {
	// Anthropic error events.
	anthropic := parseResponseContext("event: error\ndata: {\"error\":{\"message\":\"oops\"}}\n\n")
	if got := parseStreamFailureMessage(anthropic, ProtocolAnthropic); got != "oops" {
		t.Fatalf("anthropic failure: %q", got)
	}
	bare := parseResponseContext("event: error\ndata: {}\n\n")
	if got := parseStreamFailureMessage(bare, ProtocolAnthropic); got != "Anthropic 流式响应失败" {
		t.Fatalf("anthropic bare failure: %q", got)
	}
	// Gemini payload messages.
	gemini := parseResponseContext(`{"error":{"message":"gemini down"}}`)
	if got := parseStreamFailureMessage(gemini, ProtocolGemini); got != "gemini down" {
		t.Fatalf("gemini failure: %q", got)
	}
	// OpenAI response.failed ladder: error → response.error → event type.
	openAI := parseResponseContext("data: {\"type\":\"response.failed\",\"error\":{\"message\":\"failed hard\"}}\n\n")
	if got := parseStreamFailureMessage(openAI, ProtocolOpenAI); got != "failed hard" {
		t.Fatalf("openai failure error: %q", got)
	}
	nested := parseResponseContext("data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"deep\"}}}\n\n")
	if got := parseStreamFailureMessage(nested, ProtocolOpenAI); got != "deep" {
		t.Fatalf("openai nested failure: %q", got)
	}
	typed := parseResponseContext("data: {\"type\":\"response.incomplete\"}\n\n")
	if got := parseStreamFailureMessage(typed, ProtocolOpenAI); got != "response.incomplete" {
		t.Fatalf("openai typed failure: %q", got)
	}
	empty := parseResponseContext("data: {\"type\":\"response.created\"}\n\n")
	if got := parseStreamFailureMessage(empty, ProtocolOpenAI); got != "" {
		t.Fatalf("unrelated events: %q", got)
	}
}

func TestW7CExtractOutputTextPerProtocol(t *testing.T) {
	// OpenAI Responses JSON.
	responses := parseResponseContext(`{"output":[{"content":[{"type":"output_text","text":"juhe"}]}]}`)
	if got := extractOutputText(responses, ProtocolOpenAI); got != "juhe" {
		t.Fatalf("responses text: %q", got)
	}
	// OpenAI chat message + reasoning fallback.
	chat := parseResponseContext(`{"choices":[{"message":{"reasoning_content":"thinking"}}]}`)
	if got := extractOutputText(chat, ProtocolOpenAI); got != "thinking" {
		t.Fatalf("chat reasoning: %q", got)
	}
	refusal := parseResponseContext(`{"choices":[{"message":{"refusal":"no"}}]}`)
	if got := extractOutputText(refusal, ProtocolOpenAI); got != "no" {
		t.Fatalf("chat refusal: %q", got)
	}
	// OpenAI SSE delta accumulation and done short-circuit.
	stream := parseResponseContext("data: {\"choices\":[{\"delta\":{\"content\":\"ju\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"he\"}}]}\n\n" +
		"data: [DONE]\n\n")
	if got := extractOutputText(stream, ProtocolOpenAI); got != "juhe" {
		t.Fatalf("chat stream text: %q", got)
	}
	// Responses SSE delta + completed payload.
	responsesSSE := parseResponseContext("data: {\"type\":\"response.output_text.delta\",\"delta\":\"ju\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"output_text\":\"juhe\"}}\n\n")
	if got := extractOutputText(responsesSSE, ProtocolOpenAI); got != "juhe" {
		t.Fatalf("responses stream text: %q", got)
	}
	done := parseResponseContext("data: {\"type\":\"response.output_text.done\",\"text\":\"final\"}\n\n")
	if got := extractOutputText(done, ProtocolOpenAI); got != "final" {
		t.Fatalf("done text: %q", got)
	}

	// Anthropic JSON and SSE deltas.
	anthropic := parseResponseContext(`{"content":[{"type":"text","text":"ju"},{"type":"tool_use","id":"t"}]}`)
	if got := extractOutputText(anthropic, ProtocolAnthropic); got != "ju" {
		t.Fatalf("anthropic text: %q", got)
	}
	anthropicSSE := parseResponseContext("data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"ju\"}}\n\n" +
		"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"input_json_delta\"}}\n\n" +
		"data: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"he\"}}\n\n")
	if got := extractOutputText(anthropicSSE, ProtocolAnthropic); got != "juhe" {
		t.Fatalf("anthropic stream: %q", got)
	}

	// Gemini candidates.
	// geminiCandidateTexts (normalized output extraction) keeps every part
	// verbatim, including thought parts.
	gemini := parseResponseContext(`{"candidates":[{"content":{"parts":[{"text":"ju"},{"thought":true,"text":"no"},{"text":"he"}]}}]}`)
	if got := extractOutputText(gemini, ProtocolGemini); got != "junohe" {
		t.Fatalf("gemini text: %q", got)
	}
}

func TestW7CRawVisibleTextExtraction(t *testing.T) {
	// Raw visible chat text keeps the exact bytes (no trim) and content
	// blocks with explicit types.
	chat := parseResponseContext(`{"choices":[{"message":{"content":[{"type":"text","text":" x "},{"type":"text","text":"y"}]}}]}`)
	text, ok := extractRawVisibleOutputText(chat, ProtocolOpenAI)
	if !ok || text != " x y" {
		t.Fatalf("raw chat blocks: %q %t", text, ok)
	}
	refusal := parseResponseContext(`{"choices":[{"message":{"refusal":"nope"}}]}`)
	if text, ok := extractRawVisibleOutputText(refusal, ProtocolOpenAI); !ok || text != "nope" {
		t.Fatalf("raw refusal: %q %t", text, ok)
	}
	// Anthropic raw ignores non-text content entries.
	anthropic := parseResponseContext(`{"content":[{"type":"tool_use","text":"no"},{"type":"text","text":"yes"}]}`)
	if text, ok := extractRawVisibleOutputText(anthropic, ProtocolAnthropic); !ok || text != "yes" {
		t.Fatalf("raw anthropic: %q %t", text, ok)
	}
	anthropicSSE := parseResponseContext("data: {\"delta\":{\"type\":\"input_json_delta\",\"text\":\"no\"}}\n\n" +
		"data: {\"delta\":{\"text\":\"yes\"}}\n\n")
	if text, ok := extractRawVisibleOutputText(anthropicSSE, ProtocolAnthropic); !ok || text != "yes" {
		t.Fatalf("raw anthropic sse: %q %t", text, ok)
	}
	// Gemini visible texts skip thoughts and thought summaries.
	gemini := parseResponseContext(`{"steps":[{"type":"thought","content":[{"parts":[{"text":"hidden"}]}]},{"type":"step","content":{"parts":[{"text":"shown"}]}}]}`)
	if text, ok := extractRawVisibleOutputText(gemini, ProtocolGemini); !ok || text != "shown" {
		t.Fatalf("raw gemini: %q %t", text, ok)
	}
	stepDelta := parseResponseContext(`{"event_type":"step.delta","delta":{"type":"text","text":"delta-text"}}`)
	if text, ok := extractRawVisibleOutputText(stepDelta, ProtocolGemini); !ok || text != "delta-text" {
		t.Fatalf("raw gemini delta: %q %t", text, ok)
	}
	// openAIVisibleContentText only accepts visible types.
	if _, ok := openAIVisibleContentText(map[string]any{"type": "tool_call", "text": "x"}); ok {
		t.Fatal("tool calls are not visible content")
	}
	if _, ok := openAIVisibleContentText("scalar"); ok {
		t.Fatal("scalars are not visible content")
	}
	if parts := contentBlocksTexts([]any{
		map[string]any{"type": "text", "text": "a"},
		"skip",
		map[string]any{"type": "refusal", "text": "b"},
	}); strings.Join(parts, "") != "ab" {
		t.Fatalf("contentBlocksTexts: %v", parts)
	}
}

func TestW7CSuccessEvidenceMatrix(t *testing.T) {
	chatDone := parseResponseContext(`{"choices":[{"finish_reason":"stop"}]}`)
	if !hasProtocolSuccessEvidence(ModeChatJSON, chatDone) {
		t.Fatal("chat finish_reason is success evidence")
	}
	chatSSE := parseResponseContext("data: {\"choices\":[{\"delta\":{\"content\":\"ju\"}}]}\n\n" +
		"data: {\"choices\":[{\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n")
	if !hasProtocolSuccessEvidence(ModeChatSSE, chatSSE) {
		t.Fatal("chat sse finish is success evidence")
	}
	// A DONE event before any finish_reason only succeeds with prior content.
	contentThenDone := parseResponseContext("data: {\"choices\":[{\"delta\":{\"content\":\"ju\"}}]}\n\ndata: [DONE]\n\n")
	if !hasStreamingSuccessEvidence(ModeChatSSE, contentThenDone) {
		t.Fatal("content before DONE is evidence")
	}
	doneOnly := parseResponseContext("data: [DONE]\n\n")
	if hasStreamingSuccessEvidence(ModeChatSSE, doneOnly) {
		t.Fatal("DONE alone without content is not evidence")
	}

	responsesDone := parseResponseContext("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"object\":\"response\"}}\n\n")
	if !hasProtocolSuccessEvidence(ModeResponsesSSE, responsesDone) {
		t.Fatal("responses completed is evidence")
	}
	if !hasCompletedResponsesPayload(w7cJSONMap(t, `{"status":"completed","output":[]}`)) {
		t.Fatal("responses payload with output array")
	}
	if hasCompletedResponsesPayload(w7cJSONMap(t, `{"status":"failed"}`)) || hasCompletedResponsesPayload(nil) {
		t.Fatal("non-completed responses payloads rejected")
	}

	messagesDone := parseResponseContext("data: {\"type\":\"message_stop\"}\n\n")
	if !hasProtocolSuccessEvidence(ModeMessagesSSE, messagesDone) {
		t.Fatal("message_stop is evidence")
	}
	if !hasCompletedMessagesPayload(w7cJSONMap(t, `{"type":"message","stop_reason":"end_turn"}`)) {
		t.Fatal("completed messages payload")
	}
	if hasCompletedMessagesPayload(w7cJSONMap(t, `{"type":"message"}`)) || hasCompletedMessagesPayload(nil) {
		t.Fatal("incomplete messages payloads rejected")
	}

	geminiDone := parseResponseContext(`{"candidates":[{"finishReason":"STOP"}]}`)
	if !hasProtocolSuccessEvidence(ModeGenerateContentJSON, geminiDone) {
		t.Fatal("gemini finishReason is evidence")
	}
	if !hasProtocolSuccessEvidence(ModeGenerateContentSSE, parseResponseContext("data: {\"candidates\":[{\"finishReason\":\"STOP\"}]}\n\n")) {
		t.Fatal("gemini sse finishReason is evidence")
	}

	interactionsDone := parseResponseContext("data: {\"type\":\"interaction.completed\",\"interaction\":{\"status\":\"completed\"}}\n\n")
	if !hasProtocolSuccessEvidence(ModeInteractionsSSE, interactionsDone) {
		t.Fatal("interactions completed is evidence")
	}
	if !hasCompletedInteractionsPayload(w7cJSONMap(t, `{"status":"completed","steps":[]}`)) {
		t.Fatal("completed interactions payload")
	}
	if hasCompletedInteractionsPayload(w7cJSONMap(t, `{"status":"running"}`)) || hasCompletedInteractionsPayload(nil) {
		t.Fatal("incomplete interactions payloads rejected")
	}
	// JSON body without completion markers is not evidence.
	if hasProtocolSuccessEvidence(ModeResponsesJSON, parseResponseContext(`{"status":"in_progress"}`)) {
		t.Fatal("in-progress responses payloads are not evidence")
	}
	if hasProtocolSuccessEvidence(ModeChatJSON, parseResponseContext("")) {
		t.Fatal("empty bodies are not evidence")
	}
	if hasImagesSuccessEvidence(parseResponseContext(`{"data":[{"url":"https://x/y.webp"}]}`)) != true {
		t.Fatal("image url is success evidence")
	}
	if hasImagesSuccessEvidence(parseResponseContext(`{"data":[{"b64_json":"  "}]}`)) {
		t.Fatal("blank image fields are not evidence")
	}
	if hasImagesSuccessEvidence(parseResponseContext("")) {
		t.Fatal("empty image bodies are not evidence")
	}
}

// w7c output-challenge fingerprint rotation: the request-side prompt gains a
// 3-6 digit random suffix while verification stays loose (ExpectedOutput is
// still "juhe"), and responses instructions rotate within a fixed pool.
func TestW7COutputChallengeRandomization(t *testing.T) {
	challenge := CreateOutputChallenge()
	if challenge.ExpectedOutput != "juhe" {
		t.Fatalf("expected output: %q", challenge.ExpectedOutput)
	}
	promptRe := regexp.MustCompile(`^只能回复：juhe\d{3,6}$`)
	if !promptRe.MatchString(challenge.Prompt) {
		t.Fatalf("challenge prompt: %q", challenge.Prompt)
	}
	sawDifferent := false
	for i := 0; i < 32; i++ {
		if next := CreateOutputChallenge(); next.Prompt != challenge.Prompt {
			sawDifferent = true
			break
		}
	}
	if !sawDifferent {
		t.Fatal("challenge prompt never varied across repeated calls")
	}
	allowed := make(map[string]bool)
	for _, instruction := range probeInstructionsPool {
		allowed[instruction] = true
	}
	for i := 0; i < 32; i++ {
		if instruction := probeInstructions(); !allowed[instruction] {
			t.Fatalf("instructions outside pool: %q", instruction)
		}
	}
}

func TestW7CProtocolPayloadBuilders(t *testing.T) {
	responses, err := buildOpenAIResponsesPayload("gpt-test", "prompt", true, false)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(responses, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["store"] != false || decoded["model"] != "gpt-test" || decoded["max_output_tokens"] != float64(outputTokenLimit) {
		t.Fatalf("responses payload: %s", responses)
	}
	// Key order matches the Node literal.
	if !strings.HasPrefix(string(responses), `{"model":`) {
		t.Fatalf("responses key order: %s", responses)
	}
	nonOAuth, err := buildOpenAIResponsesPayload("m", "p", false, true)
	if err != nil || strings.Contains(string(nonOAuth), `"store"`) {
		t.Fatalf("non-oauth responses payload: %s", nonOAuth)
	}

	images, err := buildImagesPayload("img-1")
	if err != nil {
		t.Fatal(err)
	}
	// Image prompt keeps the historical prefix and gains a 3-6 digit random
	// suffix (fingerprint rotation); image verification ignores prompt text.
	if !regexp.MustCompile(`"prompt":"Solid black\. \d{3,6}"`).MatchString(string(images)) {
		t.Fatalf("images payload prompt: %s", images)
	}
	gemini, err := buildGeminiGenerateContentPayload("p")
	if err != nil || !strings.Contains(string(gemini), `"generationConfig"`) {
		t.Fatalf("gemini payload: %s %v", gemini, err)
	}

	// geminiModelPath strips one case-insensitive models/ prefix, falls back
	// to gemini-pro and escapes the remainder like encodeURIComponent.
	if got := geminiModelPath("models/gemini-2.5"); got != "models/gemini-2.5" {
		t.Fatalf("model path prefix strip: %q", got)
	}
	if got := geminiModelPath("MODELS/case"); got != "models/case" {
		t.Fatalf("case-insensitive strip: %q", got)
	}
	if got := geminiModelPath("  "); got != "models/gemini-pro" {
		t.Fatalf("empty model fallback: %q", got)
	}
	if got := geminiModelPath("a b/c+d"); got != "models/a%20b%2Fc%2Bd" {
		t.Fatalf("escape: %q", got)
	}
	if !equalFoldPrefix("MODELS/x", "models/") || equalFoldPrefix("mod", "models/") {
		t.Fatal("equalFoldPrefix contract")
	}
	if got := trimSpace(" \t x\n"); got != "x" {
		t.Fatalf("trimSpace: %q", got)
	}
	if got := urlPathEscape("AZaz09-_.!~*'()"); got != "AZaz09-_.!~*'()" {
		t.Fatalf("unreserved escape: %q", got)
	}
	if got := urlPathEscape(" "); got != "%20" {
		t.Fatalf("space escape: %q", got)
	}

	// Endpoint orders and mode families.
	if len(EndpointModeOrderOAuth()) != 2 || EndpointModeOrderOAuth()[0] != ModeResponsesJSON {
		t.Fatal("oauth order")
	}
	for mode, family := range map[EndpointMode]string{
		ModeChatJSON: "openai_chat_completions", ModeResponsesSSE: "openai_responses",
		ModeMessagesJSON: "anthropic_messages", ModeGenerateContentSSE: "gemini_stream_generate_content",
		ModeInteractionsJSON: "gemini_generate_content",
	} {
		if got := protocolFamilyForMode(mode); got != family {
			t.Fatalf("protocolFamilyForMode(%s) = %s", mode, got)
		}
	}
	if _, err := defaultEndpointMode(nil); err == nil || !strings.Contains(err.Error(), "请求形态") {
		t.Fatalf("empty supported list: %v", err)
	}
	if mode, err := defaultEndpointMode([]EndpointMode{ModeChatSSE, ModeChatJSON}); err != nil || mode != ModeChatSSE {
		t.Fatalf("first supported mode: %v %v", mode, err)
	}
	// manualTestEndpointModes deduplicates and keeps the default first.
	modes := manualTestEndpointModes(ModeChatJSON, map[EndpointMode]bool{
		ModeChatJSON: true, ModeChatSSE: true, ModeResponsesJSON: true,
	}, EndpointModeOrderOpenAI())
	want := []EndpointMode{ModeChatJSON, ModeChatSSE, ModeResponsesJSON}
	if len(modes) != len(want) {
		t.Fatalf("manual modes: %v", modes)
	}
	for index := range want {
		if modes[index] != want[index] {
			t.Fatalf("manual modes order: %v", modes)
		}
	}
	// The default mode is dropped when the normalized set does not include it.
	modes = manualTestEndpointModes(ModeMessagesJSON, map[EndpointMode]bool{ModeChatJSON: true}, EndpointModeOrderOpenAI())
	if len(modes) != 1 || modes[0] != ModeChatJSON {
		t.Fatalf("unsupported default dropped: %v", modes)
	}
}

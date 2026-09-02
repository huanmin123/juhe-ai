package modelcheckprobe

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	keymodelruntime "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/key_model_runtime"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

func TestBuildStructuredAndToolRequestsPreserveProtocolShapes(t *testing.T) {
	protocols := []modelcheckprofile.Protocol{
		modelcheckprofile.ProtocolOpenAIResponses,
		modelcheckprofile.ProtocolOpenAIChat,
		modelcheckprofile.ProtocolAnthropic,
		modelcheckprofile.ProtocolGeminiNative,
	}
	for _, protocol := range protocols {
		t.Run(string(protocol), func(t *testing.T) {
			structured, err := BuildStructured(protocol, "gpt-5.6-sol", false)
			if err != nil {
				t.Fatal(err)
			}
			var structuredBody map[string]any
			if err := json.Unmarshal(structured.Body, &structuredBody); err != nil {
				t.Fatal(err)
			}
			tool, err := BuildTool(protocol, "gpt-5.6-sol", false)
			if err != nil {
				t.Fatal(err)
			}
			var toolBody map[string]any
			if err := json.Unmarshal(tool.Body, &toolBody); err != nil {
				t.Fatal(err)
			}
			if _, ok := toolBody["tools"]; !ok {
				t.Fatalf("tool body missing tools: %#v", toolBody)
			}
			assertProbeSamplingTunings(t, protocol, structuredBody, "structured")
			assertProbeSamplingTunings(t, protocol, toolBody, "tool")
			switch protocol {
			case modelcheckprofile.ProtocolOpenAIResponses:
				if _, ok := structuredBody["text"]; !ok {
					t.Fatalf("responses structured body missing text: %#v", structuredBody)
				}
			case modelcheckprofile.ProtocolOpenAIChat:
				if _, ok := structuredBody["response_format"]; !ok {
					t.Fatalf("chat structured body missing response_format: %#v", structuredBody)
				}
			case modelcheckprofile.ProtocolGeminiNative:
				generation, ok := structuredBody["generationConfig"].(map[string]any)
				if !ok || generation["responseMimeType"] != "application/json" {
					t.Fatalf("gemini structured body=%#v", structuredBody)
				}
			}
			if strings.Contains(string(tool.Body), "secret") || strings.Contains(string(tool.Body), "Bearer") {
				t.Fatal("probe body must not contain credentials")
			}
		})
	}
}

func assertProbeSamplingTunings(t *testing.T, protocol modelcheckprofile.Protocol, payload map[string]any, kind string) {
	t.Helper()
	switch protocol {
	case modelcheckprofile.ProtocolOpenAIResponses, modelcheckprofile.ProtocolOpenAIChat:
		if got, ok := payload["temperature"].(float64); !ok || got != 0 {
			t.Fatalf("%s %s temperature=%#v, want 0", protocol, kind, payload["temperature"])
		}
	case modelcheckprofile.ProtocolGeminiNative:
		generation, ok := payload["generationConfig"].(map[string]any)
		if !ok || generation["temperature"] != float64(0) || generation["maxOutputTokens"] != float64(128) {
			t.Fatalf("%s %s generationConfig=%#v, want temperature=0 maxOutputTokens=128", protocol, kind, payload["generationConfig"])
		}
	}
}

func TestBuildBasicForEndpointModeUsesExplicitPathAndStream(t *testing.T) {
	tests := []struct {
		name, mode, wantPath string
		protocol             modelcheckprofile.Protocol
		stream               bool
	}{
		{"responses json", modelcheckprofile.EndpointModeResponsesJSON, "/v1/responses", modelcheckprofile.ProtocolOpenAIResponses, false},
		{"responses sse", modelcheckprofile.EndpointModeResponsesSSE, "/v1/responses", modelcheckprofile.ProtocolOpenAIResponses, true},
		{"chat json", modelcheckprofile.EndpointModeChatJSON, "/v1/chat/completions", modelcheckprofile.ProtocolOpenAIChat, false},
		{"chat sse", modelcheckprofile.EndpointModeChatSSE, "/v1/chat/completions", modelcheckprofile.ProtocolOpenAIChat, true},
		{"messages sse", modelcheckprofile.EndpointModeMessagesSSE, "/v1/messages", modelcheckprofile.ProtocolAnthropic, true},
		{"gemini sse", modelcheckprofile.EndpointModeGenerateContentSSE, "/v1beta/models/gemini-model:streamGenerateContent?alt=sse", modelcheckprofile.ProtocolGeminiNative, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, err := BuildBasicForEndpointMode(test.protocol, "gemini-model", "hello", test.mode)
			if err != nil {
				t.Fatal(err)
			}
			if request.Path != test.wantPath || request.EndpointMode != test.mode {
				t.Fatalf("request path=%q mode=%q want path=%q mode=%q", request.Path, request.EndpointMode, test.wantPath, test.mode)
			}
			var payload map[string]any
			if err := json.Unmarshal(request.Body, &payload); err != nil {
				t.Fatal(err)
			}
			if got, ok := payload["stream"].(bool); ok && got != test.stream {
				t.Fatalf("stream=%v want=%v payload=%#v", got, test.stream, payload)
			}
			if test.protocol == modelcheckprofile.ProtocolGeminiNative {
				if _, ok := payload["stream"]; ok {
					t.Fatal("Gemini native request must select stream through action/query, not JSON stream")
				}
			}
		})
	}
}

func TestBuildBasicForEndpointModeRejectsUnsupportedCrossProtocolMode(t *testing.T) {
	for _, mode := range []string{"images_json", "interactions_json", modelcheckprofile.EndpointModeChatJSON} {
		if _, err := BuildBasicForEndpointMode(modelcheckprofile.ProtocolOpenAIResponses, "model", "hello", mode); err == nil {
			t.Fatalf("mode %q must fail closed", mode)
		}
	}
}

func TestBuildAndExecuteOpenAIProbe(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("request path=%s auth=%s", r.URL.Path, r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"gpt-5.6-sol","output_text":"ok","usage":{"input_tokens":2}}`))
	}))
	defer server.Close()
	request, err := BuildBasic(modelcheckprofile.ProtocolOpenAIResponses, "gpt-5.6-sol", "hello", false)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Execute(context.Background(), request, Options{Endpoint: server.URL, Headers: http.Header{"Authorization": []string{"Bearer secret"}}, Timeout: time.Second})
	if err != nil || !result.Success || result.ObservedModel != "gpt-5.6-sol" || result.Output != "ok" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestExecuteOpenAIOAuthCodexAdapterUsesFixedCodexResponsesSSE(t *testing.T) {
	var captured *http.Request
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		captured = request
		body, _ := io.ReadAll(request.Body)
		request.Body = io.NopCloser(strings.NewReader(string(body)))
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"model\":\"gpt-5.6-sol\",\"output_text\":\"OK-MODEL-CHECK\"}}\n\ndata: [DONE]\n")), Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Request: request}, nil
	})
	request, err := BuildBasicForEndpointMode(modelcheckprofile.ProtocolOpenAIResponses, "gpt-5.6-sol", "hello", modelcheckprofile.EndpointModeResponsesJSON)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Execute(context.Background(), request, Options{Adapter: AdapterOpenAIOAuthCodex, Endpoint: "https://custom.example/v1", Headers: http.Header{"Authorization": []string{"Bearer access-token"}, "chatgpt-account-id": []string{"acct"}}, Client: &http.Client{Transport: transport}, Timeout: time.Second})
	if err != nil || !result.Success || result.Output != "OK-MODEL-CHECK" || result.ObservedModel != "gpt-5.6-sol" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if captured == nil || captured.URL.String() != OpenAIOAuthCodexBaseURL+"/responses" {
		t.Fatalf("OAuth Codex URL=%v", captured)
	}
	if captured.Header.Get("Authorization") != "Bearer access-token" || captured.Header.Get("Accept") != "text/event-stream" || captured.Header.Get("Originator") != "Codex Desktop" || captured.Header.Get("Openai-Beta") != "responses=experimental" {
		t.Fatalf("OAuth Codex headers=%v", captured.Header)
	}
	body, _ := io.ReadAll(captured.Body)
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["stream"] != true || payload["store"] != false || payload["max_output_tokens"] != nil {
		t.Fatalf("OAuth Codex body=%#v", payload)
	}
	if _, ok := payload["input"].([]any); !ok {
		t.Fatalf("OAuth Codex input was not normalized: %#v", payload["input"])
	}
}

func TestExecuteOpenAIOAuthCodexAdapterRejectsChatModes(t *testing.T) {
	request, err := BuildBasicForEndpointMode(modelcheckprofile.ProtocolOpenAIChat, "model", "hello", modelcheckprofile.EndpointModeChatSSE)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Execute(context.Background(), request, Options{Adapter: AdapterOpenAIOAuthCodex, Endpoint: "https://custom.example", Headers: http.Header{"Authorization": []string{"Bearer access"}}, Timeout: time.Second})
	if err == nil || !strings.Contains(err.Error(), "endpoint mode is unsupported") {
		t.Fatalf("OAuth Codex chat mode must fail closed, err=%v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

type responseReadErrorBody struct {
	read bool
}

func (body *responseReadErrorBody) Read(buffer []byte) (int, error) {
	if body.read {
		return 0, io.EOF
	}
	body.read = true
	copy(buffer, "partial response")
	return len("partial response"), errors.New("synthetic response read failure")
}

func (body *responseReadErrorBody) Close() error { return nil }

func TestExecutePreservesHTTPStatusOnReadAndLimitFailures(t *testing.T) {
	request, err := BuildBasic(modelcheckprofile.ProtocolOpenAIResponses, "model", "hello", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		status    int
		body      io.ReadCloser
		maxBytes  int64
		errorText string
	}{
		{name: "read failure", status: http.StatusBadGateway, body: &responseReadErrorBody{}, errorText: "J3b upstream response read failed"},
		{name: "response limit", status: http.StatusRequestEntityTooLarge, body: io.NopCloser(strings.NewReader(strings.Repeat("x", 32))), maxBytes: 8, errorText: "J3b upstream response exceeded limit"},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: test.status, Body: test.body, Request: request}, nil
			})
			result, err := Execute(context.Background(), request, Options{Endpoint: "https://example.test", Client: &http.Client{Transport: transport}, MaxResponseBytes: test.maxBytes, Timeout: time.Second})
			if err != nil || result.HTTPStatus != test.status || result.Success || result.ErrorMessage != test.errorText {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestExecuteHTTP200SSEFailureEnvelopeFailsClosedAfterFullBody(t *testing.T) {
	request, err := BuildBasicForEndpointMode(modelcheckprofile.ProtocolOpenAIResponses, "model", "hello", modelcheckprofile.EndpointModeResponsesSSE)
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Join([]string{
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","delta":"partial"}`,
		``,
		`event: response.failed`,
		`data: {"type":"response.failed","response":{"error":{"code":"overloaded","message":"late stream failure"}}}`,
		``,
		`data: [DONE]`,
	}, "\n")
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})
	result, err := Execute(context.Background(), request, Options{Endpoint: "https://example.test", Client: &http.Client{Transport: transport}, Timeout: time.Second})
	if err != nil || result.HTTPStatus != http.StatusOK || result.Success || result.ErrorMessage != "late stream failure" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestParseResponseSSEAccumulatesEventsBeforeReturning(t *testing.T) {
	for _, test := range []struct {
		name     string
		protocol modelcheckprofile.Protocol
		body     string
		want     string
	}{
		{
			name:     "chat",
			protocol: modelcheckprofile.ProtocolOpenAIChat,
			body:     "event: chunk\ndata: {\"model\":\"chat-model\",\"choices\":[{\"delta\":{\"content\":\"hel\"}}]}\n\nevent: chunk\ndata: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n",
			want:     "hello",
		},
		{
			name:     "anthropic",
			protocol: modelcheckprofile.ProtocolAnthropic,
			body:     "event: content_block_delta\ndata: {\"delta\":{\"text\":\"hel\"}}\n\nevent: content_block_delta\ndata: {\"delta\":{\"text\":\"lo\"}}\n",
			want:     "hello",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, output, _, _ := parseResponse(test.protocol, []byte(test.body))
			if output != test.want {
				t.Fatalf("output=%q want=%q", output, test.want)
			}
		})
	}
}

type clientCapturingDispatcher struct {
	legacyCalls int
	client      *http.Client
}

func (d *clientCapturingDispatcher) Dispatch(context.Context, *http.Request, keymodelruntime.Capability, string) (*http.Response, func(bool), error) {
	d.legacyCalls++
	return nil, nil, errors.New("legacy dispatcher path must not be used")
}

func (d *clientCapturingDispatcher) DispatchWithClient(_ context.Context, _ *http.Request, _ keymodelruntime.Capability, _ string, client *http.Client) (*http.Response, func(bool), error) {
	d.client = client
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"model":"gpt-5.6-sol","output_text":"ok"}`))}, func(bool) {}, nil
}

func TestExecuteDispatcherUsesResolvedTargetClient(t *testing.T) {
	request, err := BuildBasic(modelcheckprofile.ProtocolOpenAIResponses, "gpt-5.6-sol", "hello", false)
	if err != nil {
		t.Fatal(err)
	}
	resolvedClient := &http.Client{}
	dispatcher := &clientCapturingDispatcher{}
	result, err := Execute(context.Background(), request, Options{Endpoint: "https://example.test", Client: resolvedClient, Dispatcher: dispatcher, Timeout: time.Second})
	if err != nil || !result.Success {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if dispatcher.client != resolvedClient || dispatcher.legacyCalls != 0 {
		t.Fatalf("dispatcher did not receive resolved client: got=%p want=%p legacyCalls=%d", dispatcher.client, resolvedClient, dispatcher.legacyCalls)
	}
}

func TestExecuteParsesAnthropicAndGeminiText(t *testing.T) {
	for _, test := range []struct {
		name, body string
		protocol   modelcheckprofile.Protocol
	}{
		{"anthropic", `{"model":"claude-opus-5","content":[{"type":"text","text":"OK-MODEL-CHECK"}],"usage":{"input_tokens":1}}`, modelcheckprofile.ProtocolAnthropic},
		{"gemini", `{"model":"gemini-3.5-flash","candidates":[{"content":{"parts":[{"text":"OK-MODEL-CHECK"}]}}],"usageMetadata":{"totalTokenCount":2}}`, modelcheckprofile.ProtocolGeminiNative},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			request, err := BuildBasic(test.protocol, "model", "hello", false)
			if err != nil {
				t.Fatal(err)
			}
			result, err := Execute(context.Background(), request, Options{Endpoint: server.URL, Timeout: time.Second})
			if err != nil || !result.Success || result.Output != "OK-MODEL-CHECK" || len(result.Usage) != 1 {
				t.Fatalf("result=%#v err=%v", result, err)
			}
		})
	}
}

func TestEvaluateStabilityPreservesPartialEvidence(t *testing.T) {
	result := EvaluateStability([]Result{{Success: true, ObservedModel: "gpt-5.6-sol", Output: "VECTOR"}, {Success: false, ErrorMessage: "timeout"}}, "gpt-5.6-sol")
	if result.Status != "warning" || result.Evidence["partial"] != true || result.Evidence["requestSuccessCount"] != 1 {
		t.Fatalf("stability=%#v", result)
	}
}

func TestProbeRejectsUnsafeEndpointAndBoundsResponse(t *testing.T) {
	request, _ := BuildBasic(modelcheckprofile.ProtocolOpenAIResponses, "gpt-5.6-sol", "hello", false)
	if _, err := Execute(context.Background(), request, Options{Endpoint: "https://user:pass@example.com"}); err == nil {
		t.Fatal("userinfo endpoint must be rejected")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(strings.Repeat("x", 128))) }))
	defer server.Close()
	result, err := Execute(context.Background(), request, Options{Endpoint: server.URL, MaxResponseBytes: 16, Timeout: time.Second})
	if err != nil || result.ErrorMessage == "" || result.Success {
		t.Fatalf("bounded result=%+v err=%v", result, err)
	}
}

func TestEvaluateStructuredToolAndUsage(t *testing.T) {
	structured := EvaluateStructured(Result{Success: true, ObservedModel: "gpt-5.6-sol", Output: `prefix {"status":"ok","value":7,"secret":"redacted"}`}, "gpt-5.6-sol")
	if structured.Status != "passed" || structured.Score != 15 {
		t.Fatalf("structured=%#v", structured)
	}
	if len(structured.Evidence["outputJson"].(map[string]any)) != 2 {
		t.Fatalf("credential/raw fields leaked: %#v", structured.Evidence)
	}
	tool := EvaluateTool(Result{Success: true, ObservedModel: "gpt-5.6-sol", JSON: map[string]any{"output": []any{map[string]any{"type": "function_call", "name": "record_model_check", "arguments": `{"code":"ok","count":1}`}}}}, "gpt-5.6-sol")
	if tool.Status != "passed" || tool.Score != 15 {
		t.Fatalf("tool=%#v", tool)
	}
	usage := EvaluateUsage([]Result{{Success: true, Usage: map[string]any{"total_tokens": float64(4), "secret": "x"}}})
	if usage.Status != "passed" || len(usage.Evidence["usage"].(map[string]any)) != 1 {
		t.Fatalf("usage=%#v", usage)
	}
}

func TestEvaluateMissingResponseModelIsNeutralEvidence(t *testing.T) {
	basic := EvaluateBasic(Result{Success: true, ObservedModel: "", Output: "OK-MODEL-CHECK"}, "gpt-5.6-sol")
	if basic.Status != "warning" || basic.Score != 7 || basic.Evidence["modelMismatch"] != false {
		t.Fatalf("basic missing model=%#v", basic)
	}
	structured := EvaluateStructured(Result{Success: true, ObservedModel: "", Output: `{"status":"ok","value":7}`}, "gpt-5.6-sol")
	if structured.Status != "warning" || structured.Score != 12 || structured.Evidence["modelMismatch"] != false {
		t.Fatalf("structured missing model=%#v", structured)
	}
	tool := EvaluateTool(Result{Success: true, ObservedModel: "", JSON: map[string]any{"output": []any{map[string]any{"type": "function_call", "name": "record_model_check", "arguments": `{"code":"ok","count":1}`}}}}, "gpt-5.6-sol")
	if tool.Status != "warning" || tool.Score != 12 || tool.Evidence["modelMismatch"] != false {
		t.Fatalf("tool missing model=%#v", tool)
	}
	stream := EvaluateProtocolStream(Result{Success: true, ObservedModel: "", Output: "STREAM-OK"}, "gpt-5.6-sol", modelcheckprofile.ProtocolOpenAIResponses)
	if stream.Status != "warning" || stream.Score != 12 || stream.Evidence["modelMismatch"] != false {
		t.Fatalf("stream missing model=%#v", stream)
	}
}

func TestEvaluateExplicitModelMismatchUsesReducedNodeScores(t *testing.T) {
	basic := EvaluateBasic(Result{Success: true, ObservedModel: "other-model", Output: "OK-MODEL-CHECK"}, "gpt-5.6-sol")
	if basic.Status != "failed" || basic.Score != 1 {
		t.Fatalf("basic mismatch=%#v", basic)
	}
	structured := EvaluateStructured(Result{Success: true, ObservedModel: "other-model", Output: `{"status":"ok","value":7}`}, "gpt-5.6-sol")
	if structured.Status != "failed" || structured.Score != 5 {
		t.Fatalf("structured mismatch=%#v", structured)
	}
	tool := EvaluateTool(Result{Success: true, ObservedModel: "other-model", JSON: map[string]any{"output": []any{map[string]any{"type": "function_call", "name": "record_model_check", "arguments": `{"code":"ok","count":1}`}}}}, "gpt-5.6-sol")
	if tool.Status != "failed" || tool.Score != 5 {
		t.Fatalf("tool mismatch=%#v", tool)
	}
}

func TestEvaluateMappedRequestModelEchoIsConfiguredMappingEvidence(t *testing.T) {
	item := EvaluateBasic(Result{
		Success:             true,
		ObservedModel:       "gpt-5.6-sol",
		RequestModel:        "gpt-5.6-sol",
		ModelMappingApplied: true,
		Output:              "OK-MODEL-CHECK",
	}, "gpt-5.6-terra")
	if item.Status != "passed" || item.Score != 10 || item.Evidence["modelMismatch"] != false || item.Evidence["modelMappingApplied"] != true {
		t.Fatalf("mapped public-model echo=%#v", item)
	}
}

func TestRunSuiteStopsOnFailureAndFormsCredentialFreeEvaluations(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"gpt-5.6-sol","output_text":"OK-MODEL-CHECK","usage":{"total_tokens":2}}`))
	}))
	defer server.Close()
	items, err := RunSuite(context.Background(), Suite{Endpoint: server.URL, Model: "gpt-5.6-sol", Protocol: modelcheckprofile.ProtocolOpenAIResponses}, time.Second)
	if err != nil || len(items) != 4 || items[0].Kind != "protocol_basic" {
		t.Fatalf("items=%#v err=%v", items, err)
	}
}

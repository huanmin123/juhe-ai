package modelcheckprobe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	keymodelruntime "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/key_model_runtime"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamenvelope"
)

type w1EnvelopeTransport struct {
	body    string
	readErr error
	request *http.Request
}

func (transport *w1EnvelopeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.request = request
	_, _ = io.ReadAll(request.Body)
	reader := io.Reader(strings.NewReader(transport.body))
	if transport.readErr != nil {
		reader = &w1ReadErrorBody{body: transport.body, err: transport.readErr}
	}
	contentType := "text/event-stream"
	if strings.HasPrefix(strings.TrimSpace(transport.body), "{") {
		contentType = "application/json"
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(reader),
		Request:    request,
	}, nil
}

type w1ReadErrorBody struct {
	body string
	err  error
	done bool
}

func (body *w1ReadErrorBody) Read(buffer []byte) (int, error) {
	if body.done {
		return 0, io.EOF
	}
	body.done = true
	return copy(buffer, body.body), body.err
}

func w1OAuthRequest(t *testing.T) Request {
	t.Helper()
	request, err := BuildOpenAIOAuthCodexBasic("gpt-5.2", "hello", false)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func w1CompletedResponse(model, output string) string {
	encoded, _ := json.Marshal(map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"status": "completed", "model": model, "output_text": output,
			"usage": map[string]any{"total_tokens": 2},
		},
	})
	return "event: response.completed\ndata: " + string(encoded) + "\n\n"
}

func TestW1SuiteEnvelopeProfileAndCredentialReachTransport(t *testing.T) {
	cases := []struct {
		name           string
		profile        string
		provider       string
		endpoint       string
		mode           string
		credentialType string
		protocol       modelcheckprofile.Protocol
		headers        http.Header
		response       string
		wantURL        string
		wantAccept     string
		wantAuthHeader string
	}{
		{
			name: "GLM 官方版本根", profile: "profile_glm_general_openai_v1", provider: "glm",
			endpoint: "https://open.bigmodel.cn/api/paas/v4", mode: modelcheckprofile.EndpointModeChatJSON,
			headers:  http.Header{"Authorization": []string{"Bearer glm-token"}},
			response: `{"model":"glm-model","choices":[{"message":{"content":"OK-MODEL-CHECK"}}]}`,
			wantURL:  "https://open.bigmodel.cn/api/paas/v4/chat/completions", wantAccept: "application/json",
		},
		{
			name: "GLM 代理 /v1 根", profile: "profile_glm_coding_openai_v1", provider: "glm",
			endpoint: "https://proxy.example/v1", mode: modelcheckprofile.EndpointModeChatJSON,
			headers:  http.Header{"Authorization": []string{"Bearer glm-token"}},
			response: `{"model":"glm-model","choices":[{"message":{"content":"OK-MODEL-CHECK"}}]}`,
			wantURL:  "https://proxy.example/v1/chat/completions", wantAccept: "application/json",
		},
		{
			// v2 直拼：base 无版本根时不再补 /v1，出站=origin+模板
			//（旧「GLM 空根补 /v1」行为随上游 Base URL 版本根契约删除）。
			name: "GLM 空根", profile: "profile_glm_general_openai_v1", provider: "glm",
			endpoint: "https://proxy.example", mode: modelcheckprofile.EndpointModeChatJSON,
			headers:  http.Header{"Authorization": []string{"Bearer glm-token"}},
			response: `{"model":"glm-model","choices":[{"message":{"content":"OK-MODEL-CHECK"}}]}`,
			wantURL:  "https://proxy.example/chat/completions", wantAccept: "application/json",
		},
		{
			name: "Gemini OpenAI OAuth SSE", profile: "profile_gemini_openai_chat_v1beta", provider: "gemini",
			endpoint: "https://generativelanguage.googleapis.com/v1beta/openai", mode: modelcheckprofile.EndpointModeChatSSE,
			credentialType: "google_oauth", headers: http.Header{"Authorization": []string{"Bearer gemini-token"}},
			response: "data: {\"choices\":[{\"delta\":{\"content\":\"OK-MODEL-CHECK\"}}]}\n\n",
			wantURL:  "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions", wantAccept: "text/event-stream",
		},
		{
			// v2 直拼：base 不再补 /v1（profile_anthropic_anthropic_v1 的版本
			// 段由账户 base_url 版本根承载，如 https://api.anthropic.com/v1）。
			name: "Anthropic OAuth messages SSE", profile: "profile_anthropic_anthropic_v1", provider: "anthropic",
			endpoint: "https://api.anthropic.com", mode: modelcheckprofile.EndpointModeMessagesSSE,
			credentialType: "oauth", protocol: modelcheckprofile.ProtocolAnthropic,
			headers:        http.Header{"Authorization": []string{"Bearer anthropic-token"}},
			response:       "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"OK-MODEL-CHECK\"}}\n\n",
			wantURL:        "https://api.anthropic.com/messages",
			wantAccept:     "text/event-stream",
			wantAuthHeader: "Bearer anthropic-token",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			transport := &w1EnvelopeTransport{body: test.response}
			protocol := test.protocol
			if protocol == "" {
				protocol = modelcheckprofile.ProtocolOpenAIChat
			}
			suite := Suite{
				Endpoint: test.endpoint, ProviderCode: test.provider,
				ProviderProtocolProfileID: test.profile, CredentialType: test.credentialType,
				Headers: test.headers, Client: &http.Client{Transport: transport},
				Protocol: protocol, EndpointMode: test.mode,
			}
			options := suite.options(test.endpoint, test.headers, time.Second)
			if options.ProtocolProfileID != test.profile || options.CredentialType != test.credentialType {
				t.Fatalf("suite options profile=%q credential=%q; want profile=%q credential=%q", options.ProtocolProfileID, options.CredentialType, test.profile, test.credentialType)
			}
			wantAuthStrategy := upstreamenvelope.AuthBearer
			if test.credentialType == "google_oauth" || test.credentialType == "oauth" {
				wantAuthStrategy = upstreamenvelope.AuthOAuth
			}
			if auth := resolveAPIKeyAuth(test.headers, test.credentialType); auth.Strategy != wantAuthStrategy {
				t.Fatalf("resolved auth strategy=%q want=%q", auth.Strategy, wantAuthStrategy)
			}
			request, err := BuildBasicForEndpointMode(protocol, "probe-model", "hello", test.mode)
			if err != nil {
				t.Fatal(err)
			}
			result, err := suite.execute(context.Background(), request, time.Second)
			if err != nil || !result.Success {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if transport.request == nil || transport.request.URL.String() != test.wantURL {
				t.Fatalf("request URL=%v want=%q", transport.request, test.wantURL)
			}
			if got := transport.request.Header.Get("Accept"); got != test.wantAccept {
				t.Fatalf("Accept=%q want=%q", got, test.wantAccept)
			}
			if test.wantAuthHeader != "" && transport.request.Header.Get("Authorization") != test.wantAuthHeader {
				t.Fatalf("Authorization=%q want=%q", transport.request.Header.Get("Authorization"), test.wantAuthHeader)
			}
		})
	}
}

func TestW1SuiteProfileFallbackKeepsProtocolAndModel(t *testing.T) {
	profile := (Suite{Protocol: modelcheckprofile.ProtocolOpenAIChat, Model: "not-in-catalog"}).ProfileForModel()
	if profile.Protocol != modelcheckprofile.ProtocolOpenAIChat || len(profile.Models) != 1 || profile.Models[0] != "not-in-catalog" {
		t.Fatalf("fallback profile=%+v", profile)
	}
}

func TestW1ProbeBuildersCoverProtocolShapes(t *testing.T) {
	protocols := []modelcheckprofile.Protocol{
		modelcheckprofile.ProtocolOpenAIResponses,
		modelcheckprofile.ProtocolOpenAIChat,
		modelcheckprofile.ProtocolAnthropic,
		modelcheckprofile.ProtocolGeminiNative,
	}
	for _, protocol := range protocols {
		t.Run(string(protocol), func(t *testing.T) {
			basic, err := BuildBasic(protocol, "gpt-5.2", "hello", true)
			if err != nil || len(basic.Body) == 0 {
				t.Fatalf("basic=%+v err=%v", basic, err)
			}
			if _, err := BuildStructured(protocol, "gpt-5.2", false); err != nil {
				t.Fatalf("structured: %v", err)
			}
			if _, err := BuildTool(protocol, "gpt-5.2", false); err != nil {
				t.Fatalf("tool: %v", err)
			}
			mode := modelcheckprofile.EndpointModeForProtocol(protocol, true)
			if _, err := BuildBasicForEndpointMode(protocol, "gpt-5.2", "hello", mode); err != nil {
				t.Fatalf("basic endpoint mode: %v", err)
			}
			if _, err := BuildStructuredForEndpointMode(protocol, "gpt-5.2", mode); err != nil {
				t.Fatalf("structured endpoint mode: %v", err)
			}
			if _, err := BuildToolForEndpointMode(protocol, "gpt-5.2", mode); err != nil {
				t.Fatalf("tool endpoint mode: %v", err)
			}
			if _, err := buildBasicWithTunings(protocol, "gpt-5.2", "hello", mode, true, 16, 0.2); err != nil {
				t.Fatalf("tunings: %v", err)
			}
		})
	}
	if _, err := BuildBasic(modelcheckprofile.Protocol("unsupported"), "gpt-5.2", "hello", false); err == nil {
		t.Fatal("unsupported protocol should fail")
	}
	if _, err := BuildBasic(modelcheckprofile.ProtocolOpenAIChat, "", "hello", false); err == nil {
		t.Fatal("empty model should fail")
	}
	if _, err := BuildBasicForEndpointMode(modelcheckprofile.ProtocolOpenAIChat, "gpt-5.2", "hello", modelcheckprofile.EndpointModeResponsesSSE); err == nil {
		t.Fatal("mismatched endpoint mode should fail")
	}
	if _, err := BuildOpenAIOAuthCodexStructured("gpt-5.2", false); err != nil {
		t.Fatalf("oauth structured: %v", err)
	}
	if _, err := BuildOpenAIOAuthCodexTool("gpt-5.2", false); err != nil {
		t.Fatalf("oauth tool: %v", err)
	}
	if _, err := BuildOpenAIOAuthCodexStructured("", false); err == nil {
		t.Fatal("empty oauth model should fail")
	}
}

func TestW1ProbeBudgetAndAuthBoundaries(t *testing.T) {
	if got := resolveAPIKeyEndpointMode(Request{Protocol: modelcheckprofile.ProtocolGeminiNative, Path: "/v1beta/models/x:streamGenerateContent?alt=sse"}); got != upstreamenvelope.ModeGenerateContentSSE {
		t.Fatalf("gemini stream mode=%q", got)
	}
	if got := resolveAPIKeyEndpointMode(Request{Protocol: modelcheckprofile.ProtocolOpenAIChat, Path: "/v1/chat/completions"}); got != upstreamenvelope.ModeChatJSON {
		t.Fatalf("chat fallback mode=%q", got)
	}
	for _, value := range []any{float64(0), float64(-1), float64(1.5), "bad"} {
		if limit, ok := budgetLimit(map[string]any{"max_tokens": value}, "max_tokens"); ok || limit != 0 {
			t.Fatalf("invalid budget value=%v => %d,%v", value, limit, ok)
		}
	}
	payload := map[string]any{"max_tokens": float64(7)}
	if budget := extractBudgetPolicy(upstreamenvelope.BudgetFieldMaxTokens, payload); budget.Name() != "bounded" || budget.Limit() != 7 || payload["max_tokens"] != nil {
		t.Fatalf("bounded budget=%+v payload=%v", budget, payload)
	}
	if got := resolveAPIKeyAuth(http.Header{"Authorization": []string{"Bearer oauth-token"}}, "oauth"); got.Strategy != upstreamenvelope.AuthOAuth {
		t.Fatalf("oauth auth=%+v", got)
	}
	if got := resolveAPIKeyAuth(http.Header{"Authorization": []string{"Bearer token"}}, ""); got.Strategy != upstreamenvelope.AuthBearer {
		t.Fatalf("bearer auth=%+v", got)
	}
	if got := resolveAPIKeyAuth(http.Header{"X-Api-Key": []string{"anthropic"}}, ""); got.Strategy != upstreamenvelope.AuthAnthropicAPIKey {
		t.Fatalf("anthropic auth=%+v", got)
	}
	if got := resolveAPIKeyAuth(http.Header{"X-Goog-Api-Key": []string{"gemini"}}, ""); got.Strategy != upstreamenvelope.AuthGeminiAPIKey {
		t.Fatalf("gemini auth=%+v", got)
	}
	if got := resolveAPIKeyAuth(http.Header{}, ""); got.Strategy != upstreamenvelope.AuthNone {
		t.Fatalf("none auth=%+v", got)
	}
}

func TestW1OAuthCodexRequiresCompletedSSE(t *testing.T) {
	tests := []struct {
		name           string
		body           string
		wantError      string
		wantIncomplete bool
	}{
		{
			name:      "JSON body rejected",
			body:      `{"model":"gpt-5.2","output_text":"OK-MODEL-CHECK"}`,
			wantError: "must be SSE", wantIncomplete: true,
		},
		{
			name:      "JSON null error remains a rejected success body",
			body:      `{"error":null,"message":"ordinary success message"}`,
			wantError: "must be SSE", wantIncomplete: true,
		},
		{
			name:      "delta without completed rejected",
			body:      "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"OK-MODEL-CHECK\"}\n\n",
			wantError: "response.completed event missing", wantIncomplete: true,
		},
		{
			name:      "empty SSE rejected",
			body:      "event: response.output_text.delta\ndata: malformed-json\n\n",
			wantError: "malformed SSE data", wantIncomplete: true,
		},
		{
			name:      "stream error before completion remains error",
			body:      "event: error\ndata: {\"type\":\"error\",\"error\":{\"message\":\"ORIGINAL-STREAM-ERROR\"}}\n\n" + w1CompletedResponse("gpt-5.2", "OK-MODEL-CHECK"),
			wantError: "ORIGINAL-STREAM-ERROR",
		},
		{
			name:      "stream error after completion remains error",
			body:      w1CompletedResponse("gpt-5.2", "OK-MODEL-CHECK") + "event: response.failed\ndata: {\"type\":\"response.failed\",\"error\":{\"message\":\"LATE-STREAM-ERROR\"}}\n\n",
			wantError: "LATE-STREAM-ERROR",
		},
		{
			name:      "completed status failure rejected",
			body:      "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"failed\",\"error\":{\"message\":\"STATUS-ERROR\"}}}\n\n",
			wantError: "STATUS-ERROR",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := &w1EnvelopeTransport{body: test.body}
			result, err := Execute(context.Background(), w1OAuthRequest(t), Options{
				Endpoint: "https://custom.example", Headers: http.Header{"Authorization": []string{"Bearer token"}},
				Client: &http.Client{Transport: transport}, Timeout: time.Second, Adapter: AdapterOpenAIOAuthCodex,
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.Success || result.Incomplete != test.wantIncomplete || !strings.Contains(result.ErrorMessage, test.wantError) {
				t.Fatalf("result=%+v want error containing %q incomplete=%v", result, test.wantError, test.wantIncomplete)
			}
		})
	}
}

func TestW1OAuthCodexParserBoundaryEvents(t *testing.T) {
	tests := []struct {
		name         string
		body         string
		wantModel    string
		wantOutput   string
		wantError    string
		wantComplete bool
	}{
		{name: "plain body rejected", body: "upstream unavailable", wantOutput: "upstream unavailable", wantError: "must be SSE"},
		{name: "bare done has no terminal", body: "data: [DONE]\n\n", wantError: "response.completed event missing"},
		{name: "empty parsed object has no terminal", body: "data: {}\n\n", wantError: "response.completed event missing"},
		{
			name:      "event name supplies missing type at EOF",
			body:      "event: response.completed\ndata: {\"response\":{\"status\":\"completed\",\"model\":\"gpt-5.2\",\"output_text\":\"eof-complete\"}}",
			wantModel: "gpt-5.2", wantOutput: "eof-complete", wantComplete: true,
		},
		{
			name:         "completed non-success status without error",
			body:         "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"incomplete\"}}\n\n",
			wantError:    "response.completed status incomplete",
			wantComplete: true,
		},
		{
			name:         "failed event without payload error preserves event name",
			body:         "event: response.failed\ndata: {\"type\":\"response.failed\"}\n\n",
			wantError:    "response.failed",
			wantComplete: true,
		},
		{
			name:      "failed before completion remains failure",
			body:      "event: response.failed\ndata: {\"type\":\"response.failed\",\"error\":{\"message\":\"FAILED-FIRST\"}}\n\n" + w1CompletedResponse("gpt-5.2", "done"),
			wantModel: "gpt-5.2", wantOutput: "done", wantError: "FAILED-FIRST",
			wantComplete: true,
		},
		{
			name:         "error event cannot be overridden by completed payload",
			body:         "event: error\ndata: {\"type\":\"response.completed\",\"message\":\"ERROR-EVENT-RAW\",\"response\":{\"status\":\"completed\",\"model\":\"gpt-5.2\",\"output_text\":\"done\"}}\n\n",
			wantModel:    "gpt-5.2",
			wantOutput:   "done",
			wantError:    "ERROR-EVENT-RAW",
			wantComplete: true,
		},
		{
			name:         "failed payload cannot be overridden by completed event",
			body:         "event: response.completed\ndata: {\"type\":\"response.failed\",\"error\":{}}\n\n",
			wantError:    "response.failed",
			wantComplete: true,
		},
		{
			name:         "null error on completed payload is not a failure",
			body:         "event: response.completed\ndata: {\"type\":\"response.completed\",\"error\":null,\"response\":{\"status\":\"completed\",\"model\":\"gpt-5.2\",\"output_text\":\"done\"}}\n\n",
			wantModel:    "gpt-5.2",
			wantOutput:   "done",
			wantComplete: true,
		},
		{
			name:         "completed ordinary message remains successful",
			body:         "event: response.completed\ndata: {\"type\":\"response.completed\",\"message\":\"ordinary success message\",\"response\":{\"status\":\"completed\",\"model\":\"gpt-5.2\",\"output_text\":\"done\"}}\n\n",
			wantModel:    "gpt-5.2",
			wantOutput:   "done",
			wantComplete: true,
		},
		{
			name:         "malformed data before completion remains terminal failure",
			body:         "event: response.output_text.delta\ndata: {bad-json}\n\n" + w1CompletedResponse("gpt-5.2", "done"),
			wantModel:    "gpt-5.2",
			wantOutput:   "done",
			wantError:    "malformed SSE data",
			wantComplete: true,
		},
		{
			name:         "malformed completed event is incomplete",
			body:         "event: response.completed\ndata: {bad-json}\n\n",
			wantError:    "malformed SSE data",
			wantComplete: false,
		},
		{
			name:         "empty error event is terminal failure",
			body:         "event: error\n\n",
			wantError:    "error",
			wantComplete: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model, output, _, _, errorMessage, complete := parseOAuthCodexResponseDetailed([]byte(test.body))
			errorMatches := errorMessage == ""
			if test.wantError != "" {
				errorMatches = strings.Contains(errorMessage, test.wantError)
			}
			if model != test.wantModel || output != test.wantOutput || !errorMatches || complete != test.wantComplete {
				t.Fatalf("model=%q output=%q error=%q complete=%v; want model=%q output=%q error containing %q complete=%v", model, output, errorMessage, complete, test.wantModel, test.wantOutput, test.wantError, test.wantComplete)
			}
		})
	}
}

func TestW1OAuthCodexRawErrorPriorityAndNestedFields(t *testing.T) {
	if got := responseRawErrorMessage(nil); got != "" {
		t.Fatalf("nil raw error message=%q", got)
	}
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "error code precedes top-level message",
			body: "event: response.failed\ndata: {\"type\":\"response.failed\",\"message\":\"TOP-MESSAGE\",\"error\":{\"code\":\"RAW-CODE\"}}\n\n" + w1CompletedResponse("gpt-5.2", "done"),
			want: "RAW-CODE",
		},
		{
			name: "first raw message wins across event order",
			body: "event: error\ndata: {\"type\":\"error\",\"message\":\"FIRST-MESSAGE\"}\n\n" + w1CompletedResponse("gpt-5.2", "done") + "event: response.failed\ndata: {\"type\":\"response.failed\",\"error\":{\"code\":\"LATER-CODE\"}}\n\n",
			want: "FIRST-MESSAGE",
		},
		{
			name: "nested response error code is preserved",
			body: "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"NESTED-CODE\"}},\"message\":\"OUTER-MESSAGE\"}\n\n" + w1CompletedResponse("gpt-5.2", "done"),
			want: "NESTED-CODE",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, _, _, errorMessage, complete := parseOAuthCodexResponseDetailed([]byte(test.body))
			if !complete || errorMessage != test.want {
				t.Fatalf("error=%q complete=%v; want error=%q and complete terminal", errorMessage, complete, test.want)
			}
		})
	}
}

func TestW1OAuthCodexIncompleteDeltaRetriesAndCanRecover(t *testing.T) {
	transport := &w1SequenceTransport{bodies: []w1TransportBody{
		{body: "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n", readErr: errors.New("synthetic tail read error")},
		{body: w1CompletedResponse("gpt-5.2", "OK-MODEL-CHECK")},
	}}
	result, err := ExecuteWithRetry(context.Background(), w1OAuthRequest(t), Options{
		Endpoint: "https://custom.example", Headers: http.Header{"Authorization": []string{"Bearer token"}},
		Client: &http.Client{Transport: transport}, Adapter: AdapterOpenAIOAuthCodex,
	}, RetryOptions{AttemptTimeouts: []time.Duration{time.Second, time.Second}, Delay: func(context.Context) error { return nil }})
	if err != nil || !result.Success || result.Incomplete || result.RetryAttemptCount != 1 || transport.calls != 2 {
		t.Fatalf("result=%+v calls=%d err=%v", result, transport.calls, err)
	}
	if len(result.AttemptDetails) != 2 || result.AttemptDetails[0].Error != "J3b OAuth Codex response.completed event missing" {
		t.Fatalf("retry evidence=%+v", result.AttemptDetails)
	}
}

func TestW1OAuthCodexCompletedSSEClearsTailReadError(t *testing.T) {
	transport := &w1EnvelopeTransport{body: w1CompletedResponse("gpt-5.2", "OK-MODEL-CHECK"), readErr: errors.New("synthetic tail read error")}
	result, err := Execute(context.Background(), w1OAuthRequest(t), Options{
		Endpoint: "https://custom.example", Headers: http.Header{"Authorization": []string{"Bearer token"}},
		Client: &http.Client{Transport: transport}, Adapter: AdapterOpenAIOAuthCodex,
	})
	if err != nil || !result.Success || result.Incomplete || result.ErrorMessage != "" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestW1OAuthCodexFailureRemainsTerminalWithTailReadError(t *testing.T) {
	body := w1CompletedResponse("gpt-5.2", "OK-MODEL-CHECK") + "event: response.failed\ndata: {\"type\":\"response.failed\",\"error\":{\"message\":\"ORIGINAL-STREAM-ERROR\"}}\n\n"
	transport := &w1EnvelopeTransport{body: body, readErr: errors.New("synthetic tail read error")}
	result, err := Execute(context.Background(), w1OAuthRequest(t), Options{
		Endpoint: "https://custom.example", Headers: http.Header{"Authorization": []string{"Bearer token"}},
		Client: &http.Client{Transport: transport}, Adapter: AdapterOpenAIOAuthCodex,
	})
	if err != nil || result.Success || result.Incomplete || result.ErrorMessage != "ORIGINAL-STREAM-ERROR" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestW1OAuthCodexResponseLimitPrecedesParsing(t *testing.T) {
	transport := &w1EnvelopeTransport{body: w1CompletedResponse("gpt-5.2", "OK-MODEL-CHECK")}
	result, err := Execute(context.Background(), w1OAuthRequest(t), Options{
		Endpoint: "https://custom.example", Headers: http.Header{"Authorization": []string{"Bearer token"}},
		Client: &http.Client{Transport: transport}, Adapter: AdapterOpenAIOAuthCodex, MaxResponseBytes: 8,
	})
	if err != nil || result.Success || !strings.Contains(result.ErrorMessage, "response exceeded limit") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestW1OrdinaryJSONParserKeepsCompleteReadErrorSemantics(t *testing.T) {
	transport := &w1EnvelopeTransport{
		body:    `{"model":"gpt-5.2","choices":[{"message":{"content":"OK-MODEL-CHECK"}}]}`,
		readErr: errors.New("synthetic tail read error"),
	}
	request, err := BuildBasicForEndpointMode(modelcheckprofile.ProtocolOpenAIChat, "gpt-5.2", "hello", modelcheckprofile.EndpointModeChatJSON)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Execute(context.Background(), request, Options{
		Endpoint: "https://custom.example", Headers: http.Header{"Authorization": []string{"Bearer token"}},
		Client: &http.Client{Transport: transport},
	})
	if err != nil || !result.Success || result.Incomplete || result.Output != "OK-MODEL-CHECK" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

type w1ZeroReadBody struct{}

func (w1ZeroReadBody) Read([]byte) (int, error) { return 0, nil }

type w1ZeroReadTransport struct{}

func (w1ZeroReadTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(w1ZeroReadBody{}),
		Request:    request,
	}, nil
}

func TestW1OAuthCodexZeroReadBodyIsIncomplete(t *testing.T) {
	result, err := Execute(context.Background(), w1OAuthRequest(t), Options{
		Endpoint: "https://custom.example", Headers: http.Header{"Authorization": []string{"Bearer token"}},
		Client: &http.Client{Transport: w1ZeroReadTransport{}}, Adapter: AdapterOpenAIOAuthCodex,
	})
	if err != nil || result.Success || !result.Incomplete || !strings.Contains(result.ErrorMessage, "must be SSE") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

type w1TransportBody struct {
	body    string
	readErr error
}

type w1SequenceTransport struct {
	bodies []w1TransportBody
	calls  int
}

func (transport *w1SequenceTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if transport.calls >= len(transport.bodies) {
		return nil, errors.New("unexpected transport attempt")
	}
	body := transport.bodies[transport.calls]
	transport.calls++
	reader := io.Reader(strings.NewReader(body.body))
	if body.readErr != nil {
		reader = &w1ReadErrorBody{body: body.body, err: body.readErr}
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(reader), Request: request}, nil
}

type w1SettlingDispatcher struct {
	body        string
	settlements []bool
}

func (dispatcher *w1SettlingDispatcher) Dispatch(_ context.Context, request *http.Request, _ keymodelruntime.Capability, _ string) (*http.Response, func(bool), error) {
	settle := func(success bool) { dispatcher.settlements = append(dispatcher.settlements, success) }
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(dispatcher.body)), Request: request}, settle, nil
}

type w1DispatchReply struct {
	status  int
	body    string
	readErr error
}

type w1SequencedSettlingDispatcher struct {
	replies     []w1DispatchReply
	calls       int
	settlements []bool
}

func (dispatcher *w1SequencedSettlingDispatcher) Dispatch(_ context.Context, request *http.Request, _ keymodelruntime.Capability, _ string) (*http.Response, func(bool), error) {
	if dispatcher.calls >= len(dispatcher.replies) {
		return nil, nil, errors.New("unexpected dispatcher attempt")
	}
	reply := dispatcher.replies[dispatcher.calls]
	dispatcher.calls++
	settle := func(success bool) { dispatcher.settlements = append(dispatcher.settlements, success) }
	reader := io.Reader(strings.NewReader(reply.body))
	if reply.readErr != nil {
		reader = &w1ReadErrorBody{body: reply.body, err: reply.readErr}
	}
	return &http.Response{StatusCode: reply.status, Header: make(http.Header), Body: io.NopCloser(reader), Request: request}, settle, nil
}

func TestW1OAuthCodexJSONFailureRetryAndSettlement(t *testing.T) {
	tests := []struct {
		name            string
		replies         []w1DispatchReply
		wantMessage     string
		wantCalls       int
		wantStatuses    []int
		wantSettlements []bool
		wantSuccess     bool
		wantFinalError  bool
	}{
		{
			name: "401 top-level message is preserved before retry",
			replies: []w1DispatchReply{
				{status: http.StatusUnauthorized, body: `{"message":"AUTH-DENIED-RAW"}`},
				{status: http.StatusOK, body: w1CompletedResponse("gpt-5.2", "OK-MODEL-CHECK")},
			},
			wantMessage: "AUTH-DENIED-RAW", wantCalls: 2,
			wantStatuses: []int{http.StatusUnauthorized, http.StatusOK}, wantSettlements: []bool{false, true}, wantSuccess: true,
		},
		{
			name: "429 top-level message is preserved before retry",
			replies: []w1DispatchReply{
				{status: http.StatusTooManyRequests, body: `{"message":"RATE-LIMIT-RAW"}`},
				{status: http.StatusOK, body: w1CompletedResponse("gpt-5.2", "OK-MODEL-CHECK")},
			},
			wantMessage: "RATE-LIMIT-RAW", wantCalls: 2,
			wantStatuses: []int{http.StatusTooManyRequests, http.StatusOK}, wantSettlements: []bool{false, true}, wantSuccess: true,
		},
		{
			name: "complete 200 error is final without retry",
			replies: []w1DispatchReply{
				{status: http.StatusOK, body: `{"error":{"message":"MODEL-FAILED-RAW","type":"invalid_request_error"}}`},
			},
			wantMessage: "MODEL-FAILED-RAW", wantCalls: 1,
			wantStatuses: []int{http.StatusOK}, wantSettlements: []bool{false},
			wantFinalError: true,
		},
		{
			name: "complete 200 empty error object is final without retry",
			replies: []w1DispatchReply{
				{status: http.StatusOK, body: `{"error":{}}`},
			},
			wantMessage: "J3b OAuth Codex response failed", wantCalls: 1,
			wantStatuses: []int{http.StatusOK}, wantSettlements: []bool{false},
			wantFinalError: true,
		},
		{
			name: "complete 200 failed status without message is final",
			replies: []w1DispatchReply{
				{status: http.StatusOK, body: `{"response":{"status":"failed"}}`},
			},
			wantMessage: "response.completed status failed", wantCalls: 1,
			wantStatuses: []int{http.StatusOK}, wantSettlements: []bool{false},
			wantFinalError: true,
		},
		{
			name: "complete 200 top-level failed status without message is final",
			replies: []w1DispatchReply{
				{status: http.StatusOK, body: `{"error":null,"status":"failed"}`},
			},
			wantMessage: "response.status failed", wantCalls: 1,
			wantStatuses: []int{http.StatusOK}, wantSettlements: []bool{false},
			wantFinalError: true,
		},
		{
			name: "JSON null error alone is rejected and retried as incomplete",
			replies: []w1DispatchReply{
				{status: http.StatusOK, body: `{"error":null}`},
				{status: http.StatusOK, body: w1CompletedResponse("gpt-5.2", "OK-MODEL-CHECK")},
			},
			wantMessage: "J3b OAuth Codex response must be SSE", wantCalls: 2,
			wantStatuses: []int{http.StatusOK, http.StatusOK}, wantSettlements: []bool{false, true}, wantSuccess: true,
		},
		{
			name: "malformed completed frame with tail read error remains retryable",
			replies: []w1DispatchReply{
				{status: http.StatusOK, body: "event: response.completed\ndata: {bad-json}\n\n", readErr: errors.New("synthetic tail read error")},
				{status: http.StatusOK, body: w1CompletedResponse("gpt-5.2", "OK-MODEL-CHECK")},
			},
			wantMessage: "J3b OAuth Codex response contains malformed SSE data", wantCalls: 2,
			wantStatuses: []int{http.StatusOK, http.StatusOK}, wantSettlements: []bool{false, true}, wantSuccess: true,
		},
		{
			name: "complete 200 failure with tail read error is final once",
			replies: []w1DispatchReply{
				{status: http.StatusOK, body: `{"error":{}}`, readErr: errors.New("synthetic tail read error")},
			},
			wantMessage: "J3b OAuth Codex response failed", wantCalls: 1,
			wantStatuses: []int{http.StatusOK}, wantSettlements: []bool{false},
			wantFinalError: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dispatcher := &w1SequencedSettlingDispatcher{replies: test.replies}
			result, err := ExecuteWithRetry(context.Background(), w1OAuthRequest(t), Options{
				Endpoint: "https://custom.example", Headers: http.Header{"Authorization": []string{"Bearer token"}},
				Dispatcher: dispatcher, Adapter: AdapterOpenAIOAuthCodex,
			}, RetryOptions{AttemptTimeouts: []time.Duration{time.Second, time.Second}, Delay: func(context.Context) error { return nil }})
			if err != nil {
				t.Fatal(err)
			}
			if result.Success != test.wantSuccess || (test.wantFinalError && !strings.Contains(result.ErrorMessage, test.wantMessage)) || (!test.wantFinalError && result.ErrorMessage != "") {
				t.Fatalf("result=%+v want success=%v final message containing %q", result, test.wantSuccess, test.wantMessage)
			}
			if dispatcher.calls != test.wantCalls || result.Incomplete || result.RetryAttemptCount != test.wantCalls-1 || !reflect.DeepEqual(result.AttemptStatusCodes, test.wantStatuses) {
				t.Fatalf("result=%+v calls=%d want calls=%d statuses=%v", result, dispatcher.calls, test.wantCalls, test.wantStatuses)
			}
			if len(result.AttemptDetails) != len(test.wantStatuses) || result.AttemptDetails[0].Error != test.wantMessage {
				t.Fatalf("attempt details=%+v want first error=%q", result.AttemptDetails, test.wantMessage)
			}
			if !reflect.DeepEqual(dispatcher.settlements, test.wantSettlements) {
				t.Fatalf("settlements=%v want=%v", dispatcher.settlements, test.wantSettlements)
			}
		})
	}
}

func TestW1OAuthCodexDispatcherSettlesExactlyOnce(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		want bool
	}{
		{name: "completed", body: w1CompletedResponse("gpt-5.2", "OK-MODEL-CHECK"), want: true},
		{name: "failed", body: "event: response.failed\ndata: {\"type\":\"response.failed\",\"error\":{\"message\":\"upstream failed\"}}\n\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dispatcher := &w1SettlingDispatcher{body: test.body}
			result, err := Execute(context.Background(), w1OAuthRequest(t), Options{
				Endpoint: "https://custom.example", Headers: http.Header{"Authorization": []string{"Bearer token"}},
				Dispatcher: dispatcher, Adapter: AdapterOpenAIOAuthCodex,
			})
			if err != nil || result.Success != test.want || len(dispatcher.settlements) != 1 || dispatcher.settlements[0] != test.want {
				t.Fatalf("result=%+v settlements=%v err=%v", result, dispatcher.settlements, err)
			}
		})
	}
}

func TestW1OAuthCodexReusesAndCompletesCallerIdentity(t *testing.T) {
	cases := []struct {
		name  string
		extra map[string]string
		want  map[string]string
	}{
		{
			name: "existing identity values",
			extra: map[string]string{
				"Session-Id": "session-existing", "Thread-Id": "thread-existing",
				"X-Client-Request-Id": "request-existing", "X-Codex-Window-Id": "window-existing",
				"X-Codex-Installation-Id": "installation-existing", "X-Codex-Turn-Metadata": "stale-metadata",
			},
			want: map[string]string{
				"session_id": "session-existing", "thread_id": "thread-existing", "turn_id": "request-existing",
				"window_id": "window-existing", "installation_id": "installation-existing",
			},
		},
		{
			name:  "partial request id",
			extra: map[string]string{"X-Client-Request-Id": "request-only"},
			want:  map[string]string{"turn_id": "request-only"},
		},
		{
			name:  "partial session id",
			extra: map[string]string{"Session-Id": "session-only"},
			want: map[string]string{
				"session_id": "session-only", "thread_id": "session-only", "turn_id": "session-only",
				"window_id": "session-only:0",
			},
		},
		{
			name: "partial thread and window ids",
			extra: map[string]string{
				"Thread-Id": "thread-only", "X-Codex-Window-Id": "window-only",
				"X-Codex-Installation-Id": "installation-only",
			},
			want: map[string]string{
				"thread_id": "thread-only", "window_id": "window-only", "installation_id": "installation-only",
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			headers := http.Header{"Authorization": []string{"Bearer token"}}
			for name, value := range test.extra {
				headers.Set(name, value)
			}
			headers["X-Empty-Values"] = []string{}
			transport := &w1EnvelopeTransport{body: w1CompletedResponse("gpt-5.2", "OK-MODEL-CHECK")}
			result, err := Execute(context.Background(), w1OAuthRequest(t), Options{
				Endpoint: "https://custom.example", Headers: headers,
				Client: &http.Client{Transport: transport}, Adapter: AdapterOpenAIOAuthCodex,
			})
			if err != nil || !result.Success {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			requestHeaders := transport.request.Header
			metadata := map[string]any{}
			if err := json.Unmarshal([]byte(requestHeaders.Get("X-Codex-Turn-Metadata")), &metadata); err != nil {
				t.Fatalf("turn metadata invalid: %v", err)
			}
			for key, expected := range test.want {
				if metadata[key] != expected {
					t.Fatalf("metadata[%s]=%v want %q", key, metadata[key], expected)
				}
			}
			if test.name == "partial request id" {
				sessionID, _ := metadata["session_id"].(string)
				threadID, _ := metadata["thread_id"].(string)
				if sessionID == "" || sessionID == "request-only" || threadID != sessionID {
					t.Fatalf("partial identity session=%q thread=%q; want generated session and thread fallback", sessionID, threadID)
				}
				if metadata["window_id"] != threadID+":0" || metadata["installation_id"] == "" {
					t.Fatalf("partial identity metadata=%v; want derived window and installation", metadata)
				}
			}
			if test.name == "partial thread and window ids" {
				sessionID, _ := metadata["session_id"].(string)
				if sessionID == "" || metadata["turn_id"] != sessionID {
					t.Fatalf("partial identity session=%q turn=%v; want generated session turn fallback", sessionID, metadata["turn_id"])
				}
			}
			if got := requestHeaders.Get("Session-Id"); got != metadata["session_id"] {
				t.Fatalf("Session-Id=%q metadata=%v", got, metadata)
			}
			if got := requestHeaders.Get("Thread-Id"); got != metadata["thread_id"] {
				t.Fatalf("Thread-Id=%q metadata=%v", got, metadata)
			}
			if got := requestHeaders.Get("X-Client-Request-Id"); got != metadata["turn_id"] {
				t.Fatalf("X-Client-Request-Id=%q metadata=%v", got, metadata)
			}
			if got := requestHeaders.Get("X-Codex-Window-Id"); got != metadata["window_id"] {
				t.Fatalf("X-Codex-Window-Id=%q metadata=%v", got, metadata)
			}
			if got := requestHeaders.Get("X-Codex-Installation-Id"); got != metadata["installation_id"] || got == "" {
				t.Fatalf("X-Codex-Installation-Id=%q metadata=%v", got, metadata)
			}
			if _, ok := requestHeaders["X-Empty-Values"]; ok {
				t.Fatalf("empty caller header values must not be forwarded: %v", requestHeaders)
			}
		})
	}
}

// w1HTTPStatusTransport 回放非 200 的普通 HTTP 响应，用于验证状态证据在
// 非 SSE 响应中仍被保留，不把上游正文误判成成功输出。
type w1HTTPStatusTransport struct {
	status  int
	body    string
	request *http.Request
}

func (transport *w1HTTPStatusTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.request = request
	_, _ = io.ReadAll(request.Body)
	return &http.Response{
		StatusCode: transport.status,
		Header:     http.Header{"Content-Type": []string{"text/plain"}},
		Body:       io.NopCloser(strings.NewReader(transport.body)),
		Request:    request,
	}, nil
}

// w1CancelSettlingDispatcher 在返回部分正文前取消外层 context，保留
// Dispatcher 的结算回调，覆盖“已读部分正文 + 取消 + settle(false)”组合。
type w1CancelSettlingDispatcher struct {
	cancel      context.CancelFunc
	body        string
	readErr     error
	settlements []bool
}

func (dispatcher *w1CancelSettlingDispatcher) Dispatch(_ context.Context, request *http.Request, _ keymodelruntime.Capability, _ string) (*http.Response, func(bool), error) {
	if dispatcher.cancel != nil {
		dispatcher.cancel()
	}
	settle := func(success bool) { dispatcher.settlements = append(dispatcher.settlements, success) }
	reader := io.Reader(strings.NewReader(dispatcher.body))
	if dispatcher.readErr != nil {
		reader = &w1ReadErrorBody{body: dispatcher.body, err: dispatcher.readErr}
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(reader), Request: request}, settle, nil
}

func TestW1OAuthCodexCommentAndTerminalBoundary(t *testing.T) {
	tests := []struct {
		name           string
		body           string
		wantSuccess    bool
		wantIncomplete bool
	}{
		{name: "comment-only heartbeat has no events", body: ": data:heartbeat\n\n", wantIncomplete: true},
		{name: "comment before completed is ignored", body: ": keepalive\n\n" + w1CompletedResponse("gpt-5.2", "COMMENT-COMPLETE"), wantSuccess: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := Execute(context.Background(), w1OAuthRequest(t), Options{
				Endpoint: "https://custom.example", Headers: http.Header{"Authorization": []string{"Bearer token"}},
				Client: &http.Client{Transport: &w1EnvelopeTransport{body: test.body}}, Adapter: AdapterOpenAIOAuthCodex,
			})
			if err != nil || result.Success != test.wantSuccess || result.Incomplete != test.wantIncomplete {
				t.Fatalf("result=%+v err=%v want success=%v incomplete=%v", result, err, test.wantSuccess, test.wantIncomplete)
			}
		})
	}
}

func TestW1OAuthCodexNonJSONHTTPFailureKeepsStatus(t *testing.T) {
	transport := &w1HTTPStatusTransport{status: http.StatusServiceUnavailable, body: "upstream unavailable"}
	result, err := Execute(context.Background(), w1OAuthRequest(t), Options{
		Endpoint: "https://custom.example", Headers: http.Header{"Authorization": []string{"Bearer token"}},
		Client: &http.Client{Transport: transport}, Adapter: AdapterOpenAIOAuthCodex,
	})
	if err != nil || result.Success || !result.Incomplete || result.HTTPStatus != http.StatusServiceUnavailable || result.ErrorMessage == "" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestW1APIKeyCanceledPartialReadKeepsOutputAndSettlesFalse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	dispatcher := &w1CancelSettlingDispatcher{
		cancel: cancel, body: `{"choices":[{"message":{"content":"partial-output"}}`, readErr: errors.New("synthetic partial read"),
	}
	request, err := BuildBasicForEndpointMode(modelcheckprofile.ProtocolOpenAIChat, "gpt-5.2", "hello", modelcheckprofile.EndpointModeChatJSON)
	if err != nil {
		t.Fatal(err)
	}
	result, executeErr := Execute(ctx, request, Options{
		Endpoint: "https://custom.example", Headers: http.Header{"Authorization": []string{"Bearer token"}},
		Dispatcher: dispatcher,
	})
	if executeErr != nil || result.Success || !result.Incomplete || result.ErrorMessage != "J3b probe canceled" || !strings.Contains(result.Output, "partial-output") {
		t.Fatalf("result=%+v err=%v", result, executeErr)
	}
	if !reflect.DeepEqual(dispatcher.settlements, []bool{false}) {
		t.Fatalf("settlements=%v want [false]", dispatcher.settlements)
	}
}

// w1IdentityStageTransport 在既有 identityEchoTransport 上只替换指定阶段，
// 使 RunIdentityFamily 的跳过、终态和重试分支保持真实请求正文形状。
type w1IdentityStageTransport struct {
	mode     string
	requests int
	echo     identityEchoTransport
}

func (transport *w1IdentityStageTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.requests++
	body := readIdentityBody(request)
	request.Body = io.NopCloser(strings.NewReader(body))
	stage := ""
	switch {
	case strings.Contains(body, "仓库清点锚点题"):
		stage = "anchor"
	case strings.Contains(body, "复读") || strings.Contains(body, "转录") || strings.Contains(body, "审计模式"):
		stage = "extraction"
	default:
		stage = "self"
	}
	if strings.HasPrefix(transport.mode, "stage-503:") && strings.TrimPrefix(transport.mode, "stage-503:") == stage {
		return w1IdentityFailureResponse(request, http.StatusServiceUnavailable, "IDENTITY-STAGE-503"), nil
	}
	switch transport.mode {
	case "self-200-error":
		if stage == "self" {
			return w1IdentityFailureResponse(request, http.StatusOK, "IDENTITY-SELF-200"), nil
		}
	case "extraction-200-error":
		if stage == "extraction" {
			return w1IdentityFailureResponse(request, http.StatusOK, "IDENTITY-EXTRACTION-200"), nil
		}
	case "extraction-503":
		if stage == "extraction" {
			return w1IdentityFailureResponse(request, http.StatusServiceUnavailable, "IDENTITY-EXTRACTION-503"), nil
		}
	case "anchor-missing":
		if stage == "anchor" {
			return identityJSONResponse(request, "gpt-6-luna", ""), nil
		}
	case "anchor-503":
		if stage == "anchor" {
			return w1IdentityFailureResponse(request, http.StatusServiceUnavailable, "IDENTITY-ANCHOR-503"), nil
		}
	}
	return transport.echo.RoundTrip(request)
}

func w1IdentityFailureResponse(request *http.Request, status int, message string) *http.Response {
	body := fmt.Sprintf(`{"error":{"message":%q}}`, message)
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}
}

func w1IdentitySuite(transport http.RoundTripper) Suite {
	anchor := IdentityAnchorProbe{Question: "仓库清点锚点题：甲仓库有 3 个红箱，每个红箱装 5 个零件；乙仓库有 4 个蓝箱，每个蓝箱比每个红箱多装 2 个零件。现在从乙仓库运走 1 个蓝箱。只输出甲乙两仓库剩余零件总数（只输出一个数字，不要输出其他内容）。", Answer: 40}
	return Suite{
		Endpoint: "https://identity.example", Client: &http.Client{Transport: transport},
		ProviderCode: "openai", ProviderProtocolProfileID: "profile_openai_openai_v1",
		Model: "gpt-6-luna", Profile: "quick", Protocol: modelcheckprofile.ProtocolOpenAIResponses,
		Tokenizer: deterministicTokenizer{}, anchorProbe: &anchor,
	}
}

func TestW1RunIdentityFamilyHTTP200ErrorsSkipWithoutDroppingLaterStages(t *testing.T) {
	for _, mode := range []string{"self-200-error", "extraction-200-error"} {
		t.Run(mode, func(t *testing.T) {
			transport := &w1IdentityStageTransport{mode: mode}
			items, _, terminal, err := RunIdentityFamily(context.Background(), w1IdentitySuite(transport), time.Second)
			if err != nil || terminal || len(items) != 3 || transport.requests != 5 {
				t.Fatalf("items=%+v terminal=%v requests=%d err=%v", items, terminal, transport.requests, err)
			}
			failed := items[0]
			if mode == "extraction-200-error" {
				failed = items[1]
			}
			if failed.Status != "skipped" || failed.Evidence["requestFailure"] != true || failed.Evidence["excludedFromScoring"] != true {
				t.Fatalf("stage=%s item=%+v", mode, failed)
			}
		})
	}
}

func TestW1RunIdentityFamilyStageFailuresStopAtExpectedRequest(t *testing.T) {
	tests := []struct {
		name      string
		mode      string
		wantCalls int
		wantItems int
	}{
		{name: "extraction 503 terminal", mode: "extraction-503", wantCalls: 3, wantItems: 2},
		{name: "anchor missing output", mode: "anchor-missing", wantCalls: 5, wantItems: 3},
		{name: "anchor 503 terminal", mode: "anchor-503", wantCalls: 5, wantItems: 3},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := &w1IdentityStageTransport{mode: test.mode}
			suite := w1IdentitySuite(transport)
			items, _, terminal, err := RunIdentityFamily(context.Background(), suite, time.Second)
			if err != nil || transport.requests != test.wantCalls || len(items) != test.wantItems {
				t.Fatalf("items=%+v terminal=%v requests=%d err=%v", items, terminal, transport.requests, err)
			}
			if test.mode == "anchor-missing" && terminal {
				t.Fatalf("missing anchor output should remain non-terminal: %+v", items)
			}
			if test.mode != "anchor-missing" && !terminal {
				t.Fatalf("stage failure should be terminal: %+v", items)
			}
		})
	}
}

func TestW1RunIdentityFamilyRetryDelayErrorStopsEachStage(t *testing.T) {
	delayErr := errors.New("identity delay sentinel")
	for _, test := range []struct {
		stage     string
		wantCalls int
	}{
		{stage: "self", wantCalls: 1}, {stage: "extraction", wantCalls: 3}, {stage: "anchor", wantCalls: 5},
	} {
		t.Run(test.stage, func(t *testing.T) {
			transport := &w1IdentityStageTransport{mode: "stage-503:" + test.stage}
			suite := w1IdentitySuite(transport)
			suite.Retry = RetryOptions{AttemptTimeouts: []time.Duration{time.Second, time.Second}, Delay: func(context.Context) error { return delayErr }}
			_, _, _, err := RunIdentityFamily(context.Background(), suite, time.Second)
			if !errors.Is(err, delayErr) || transport.requests != test.wantCalls {
				t.Fatalf("err=%v requests=%d want err=%v requests=%d", err, transport.requests, delayErr, test.wantCalls)
			}
		})
	}
}

func TestW1RunIdentityFamilyRejectsIncompatibleEndpointBeforeTransport(t *testing.T) {
	transport := &identityEchoTransport{}
	suite := w1IdentitySuite(transport)
	suite.Protocol = modelcheckprofile.ProtocolOpenAIChat
	suite.EndpointMode = modelcheckprofile.EndpointModeResponsesSSE
	if _, _, _, err := RunIdentityFamily(context.Background(), suite, time.Second); err == nil {
		t.Fatal("incompatible endpoint mode should return an error")
	}
	if transport.requests != 0 {
		t.Fatalf("incompatible endpoint must not dispatch: %d", transport.requests)
	}
}

func TestW1RunQuizFamilyRetryDelayAndEndpointBoundaries(t *testing.T) {
	delayErr := errors.New("quiz delay sentinel")
	tests := []struct {
		name      string
		transport *quizTransport
		wantCalls int
	}{
		{name: "answer 503", transport: &quizTransport{failAnswerWhenContains: quizTestQuestionText}, wantCalls: 1},
		{name: "grade 503", transport: &quizTransport{gradeStatus: http.StatusServiceUnavailable}, wantCalls: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			suite := quizTestSuite("quick", test.transport)
			suite.QuizQuestions = quizTestQuestions()
			suite.Retry = RetryOptions{AttemptTimeouts: []time.Duration{time.Second, time.Second}, Delay: func(context.Context) error { return delayErr }}
			_, err := RunQuizFamily(context.Background(), suite, time.Second)
			if !errors.Is(err, delayErr) || len(test.transport.bodies) != test.wantCalls {
				t.Fatalf("err=%v requests=%d want err=%v requests=%d", err, len(test.transport.bodies), delayErr, test.wantCalls)
			}
		})
	}
	transport := &quizTransport{}
	suite := quizTestSuite("quick", transport)
	suite.QuizQuestions = quizTestQuestions()
	suite.EndpointMode = modelcheckprofile.EndpointModeResponsesSSE
	if _, err := RunQuizFamily(context.Background(), suite, time.Second); err == nil {
		t.Fatal("incompatible endpoint mode should return an error")
	}
	if len(transport.bodies) != 0 {
		t.Fatalf("incompatible endpoint must not dispatch: %d", len(transport.bodies))
	}
}

func TestW1RunQuizDiagnosisMalformedKeyPointResultsRemainTolerant(t *testing.T) {
	for _, gradeOutput := range []string{
		`{"verdict":"pass","keyPointResults":[null,"junk"]}`,
		`{"verdict":"pass","keyPointResults":{}}`,
	} {
		transport := &quizTransport{gradeOutput: gradeOutput}
		diagnosis, err := RunQuizDiagnosis(context.Background(), quizDiagnosisTestSuite(transport), time.Second)
		if err != nil || diagnosis.Verdict != "pass" || diagnosis.Alignment != "unknown" || diagnosis.KeyPointResults != nil {
			t.Fatalf("grade=%s diagnosis=%+v err=%v", gradeOutput, diagnosis, err)
		}
	}
}

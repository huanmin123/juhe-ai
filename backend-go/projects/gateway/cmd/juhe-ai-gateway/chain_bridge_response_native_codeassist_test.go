package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// nativeCodeAssistTransformerTestInput builds the transform input for a
// native Gemini Code Assist request on a google_oauth account without any
// cross-protocol model mapping (the mapping resolver returns nil).
func nativeCodeAssistTransformerTestInput(t *testing.T, method, target, body string, account gatewaydispatch.AccountCandidate, response *gatewaydispatch.GatewayUpstreamResponse) gatewaydispatch.UpstreamResponseTransformInput {
	t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	return gatewaydispatch.UpstreamResponseTransformInput{
		Req:      gatewaypreauth.NewGatewayRequest(request),
		Account:  account,
		Response: response,
	}
}

func codeAssistAccount() gatewaydispatch.AccountCandidate {
	return gatewaydispatch.AccountCandidate{
		ID:           "acc_ca_1",
		ProviderCode: "google",
		ProtocolCode: "gemini",
		Type:         "google_oauth",
		Credentials: map[string]any{
			"oauth_type": "code_assist",
			"project_id": "proj-1",
		},
	}
}

func upstreamSseResponse(contentType string, sse string) *gatewaydispatch.GatewayUpstreamResponse {
	header := http.Header{}
	header.Set("Content-Type", contentType)
	return gatewaydispatch.NewGatewayUpstreamResponseForTransform(http.StatusOK, header, io.NopCloser(strings.NewReader(sse)))
}

// TestChainBridgeResponseUnwrapsNativeCodeAssistWithoutMapping pins the
// nil-mapping unwrap: the native Code Assist request carries no cross-protocol
// mapping, yet the {response:...} upstream envelope must not reach the client
// (chain_bridge_response transformGeminiCodeAssistIfApplicable ahead of the
// nil passthrough, Node transformGeminiCodeAssistUpstreamResponse).
func TestChainBridgeResponseUnwrapsNativeCodeAssistWithoutMapping(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	// The Code Assist upstream endpoint is SSE even for non-stream downstream
	// requests (alt=sse, constant): the non-stream branch collects the events
	// into one unwrapped JSON payload.
	response := upstreamSseResponse("text/event-stream",
		"data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hi\"}]}}],\"modelVersion\":\"gem-1\"}}\n\n")
	input := nativeCodeAssistTransformerTestInput(t, http.MethodPost,
		"/v1beta/models/gem-1:generateContent", `{"contents":[{"parts":[{"text":"ping"}]}]}`,
		codeAssistAccount(), response)

	got, err := transformer.TransformUpstreamResponseForAccount(input)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	body, err := io.ReadAll(got.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	text := string(body)
	if strings.Contains(text, `"response"`) {
		t.Fatalf("native code assist payload must drop the {response:...} envelope: %s", text)
	}
	if !strings.Contains(text, `"candidates"`) || !strings.Contains(text, "hi") {
		t.Fatalf("unwrapped payload must carry the inner gemini candidates: %s", text)
	}
	if ct := got.Header.Get("Content-Type"); !strings.Contains(strings.ToLower(ct), "json") {
		t.Fatalf("collected non-stream unwrap must answer JSON, got %q", ct)
	}
}

// TestChainBridgeResponseUnwrapsNativeCodeAssistStreamWithoutMapping pins the
// streaming unwrap: every SSE event loses the {response:...} wrapper while the
// event framing survives.
func TestChainBridgeResponseUnwrapsNativeCodeAssistStreamWithoutMapping(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	sse := "data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"he\"}]}}]}}\n\n" +
		"data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"llo\"}]}}]}}\n\n"
	response := upstreamSseResponse("text/event-stream", sse)
	input := nativeCodeAssistTransformerTestInput(t, http.MethodPost,
		"/v1beta/models/gem-1:streamGenerateContent?alt=sse", `{"contents":[{"parts":[{"text":"ping"}]}]}`,
		codeAssistAccount(), response)

	got, err := transformer.TransformUpstreamResponseForAccount(input)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	body, err := io.ReadAll(got.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	text := string(body)
	if strings.Contains(text, `"response":`) {
		t.Fatalf("stream unwrap must drop every {response:...} envelope: %s", text)
	}
	if strings.Count(text, "data: ") != 2 || !strings.Contains(text, "he") || !strings.Contains(text, "llo") {
		t.Fatalf("stream unwrap must keep the two inner events: %s", text)
	}
	if ct := got.Header.Get("Content-Type"); !strings.Contains(strings.ToLower(ct), "event-stream") {
		t.Fatalf("stream unwrap must keep the SSE content type, got %q", ct)
	}
}

// TestChainBridgeResponsePassthroughNonCodeAssistNilMapping pins the
// passthrough regression: a non-Code-Assist account with no mapping keeps the
// upstream response verbatim.
func TestChainBridgeResponsePassthroughNonCodeAssistNilMapping(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	payload := `{"candidates":[{"content":{"parts":[{"text":"raw"}]}}]}`
	response := upstreamSseResponse("application/json", payload)
	request := httptest.NewRequest(http.MethodPost, "/v1beta/models/gem-1:generateContent", strings.NewReader(`{}`))
	input := gatewaydispatch.UpstreamResponseTransformInput{
		Req: gatewaypreauth.NewGatewayRequest(request),
		Account: gatewaydispatch.AccountCandidate{
			ID: "acc_plain", ProviderCode: "google", ProtocolCode: "gemini", Type: "api_key",
		},
		Response: response,
	}

	got, err := transformer.TransformUpstreamResponseForAccount(input)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	body, err := io.ReadAll(got.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != payload {
		t.Fatalf("nil-mapping passthrough must stay verbatim: %s", body)
	}
}

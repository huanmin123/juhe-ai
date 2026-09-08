package gatewaypreauth

import (
	"net/http/httptest"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
)

// D-155（BUG-0175）SupportedEndpointModes hunk: request shape -> account
// endpoint-mode vocabulary.

func newEndpointModeRequest(method, target string, stream bool) *GatewayRequest {
	req := &GatewayRequest{HTTP: httptest.NewRequest(method, target, nil)}
	if stream {
		req.Body = &gatewaybody.Request{Body: map[string]any{"stream": true}}
	}
	return req
}

func TestRequestSupportedEndpointMode(t *testing.T) {
	cases := []struct {
		name   string
		req    *GatewayRequest
		expect string
	}{
		{
			name:   "chat json",
			req:    &GatewayRequest{HTTP: httptest.NewRequest("POST", "/v1/chat/completions", nil)},
			expect: EndpointModeChatJSON,
		},
		{
			name: "chat sse via body stream",
			req: &GatewayRequest{
				HTTP: httptest.NewRequest("POST", "/v1/chat/completions", nil),
				Body: &gatewaybody.Request{Body: map[string]any{"stream": true}},
			},
			expect: EndpointModeChatSSE,
		},
		{
			name: "responses sse via body stream",
			req: &GatewayRequest{
				HTTP: httptest.NewRequest("POST", "/responses", nil),
				Body: &gatewaybody.Request{Body: map[string]any{"stream": true}},
			},
			expect: EndpointModeResponsesSSE,
		},
		{
			name: "anthropic messages sse via body stream",
			req: &GatewayRequest{
				HTTP: httptest.NewRequest("POST", "/v1/messages", nil),
				Body: &gatewaybody.Request{Body: map[string]any{"stream": true}},
			},
			expect: EndpointModeMessagesSSE,
		},
		{
			name:   "responses json",
			req:    &GatewayRequest{HTTP: httptest.NewRequest("POST", "/v1/responses", nil)},
			expect: EndpointModeResponsesJSON,
		},
		{
			name:   "anthropic messages json",
			req:    &GatewayRequest{HTTP: httptest.NewRequest("POST", "/v1/messages", nil)},
			expect: EndpointModeMessagesJSON,
		},
		{
			name:   "anthropic count tokens",
			req:    &GatewayRequest{HTTP: httptest.NewRequest("POST", "/v1/messages/count_tokens", nil)},
			expect: EndpointModeMessageTokenCounting,
		},
		{
			name:   "gemini generate content json",
			req:    &GatewayRequest{HTTP: httptest.NewRequest("POST", "/v1beta/models/gemini-2.5:generateContent", nil)},
			expect: EndpointModeGenerateContentJSON,
		},
		{
			name:   "gemini stream generate content",
			req:    &GatewayRequest{HTTP: httptest.NewRequest("POST", "/v1beta/models/gemini-2.5:streamGenerateContent", nil)},
			expect: EndpointModeGenerateContentSSE,
		},
		{
			name:   "gemini count tokens",
			req:    &GatewayRequest{HTTP: httptest.NewRequest("POST", "/v1beta/models/gemini-2.5:countTokens", nil)},
			expect: EndpointModeCountTokens,
		},
		{
			name:   "gemini embed content",
			req:    &GatewayRequest{HTTP: httptest.NewRequest("POST", "/v1beta/models/gemini-2.5:embedContent", nil)},
			expect: EndpointModeEmbedContent,
		},
		{
			name:   "openai images",
			req:    &GatewayRequest{HTTP: httptest.NewRequest("POST", "/v1/images/generations", nil)},
			expect: EndpointModeImagesJSON,
		},
		{
			name:   "ungated shape (GET models)",
			req:    &GatewayRequest{HTTP: httptest.NewRequest("GET", "/v1/models", nil)},
			expect: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RequestSupportedEndpointMode(tc.req); got != tc.expect {
				t.Errorf("RequestSupportedEndpointMode = %q, want %q", got, tc.expect)
			}
		})
	}
}

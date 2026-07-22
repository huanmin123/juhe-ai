package gateway

import "testing"

func TestEndpointFamilyFromPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		protocol ProtocolCode
		path     string
		want     EndpointFamily
	}{
		{name: "openai chat", protocol: ProtocolOpenAI, path: "/v1/chat/completions?trace=1", want: EndpointChatCompletions},
		{name: "openai responses compact", protocol: ProtocolOpenAI, path: "/responses/compact", want: EndpointResponses},
		{name: "openai models", protocol: ProtocolOpenAI, path: "/v1/models", want: EndpointModels},
		{name: "openai images", protocol: ProtocolOpenAI, path: "/v1/images/edits", want: EndpointImages},
		{name: "openai embeddings", protocol: ProtocolOpenAI, path: "/embeddings", want: EndpointEmbeddings},
		{name: "openai audio", protocol: ProtocolOpenAI, path: "/v1/audio/transcriptions", want: EndpointAudio},
		{name: "anthropic messages", protocol: ProtocolAnthropic, path: "v1/messages", want: EndpointMessages},
		{name: "anthropic count tokens", protocol: ProtocolAnthropic, path: "/messages/count_tokens", want: EndpointMessageTokenCounting},
		{name: "gemini generate", protocol: ProtocolGemini, path: "/v1beta/models/gemini-2.5-pro:generateContent", want: EndpointGenerateContent},
		{name: "gemini stream", protocol: ProtocolGemini, path: "/models/gemini-2.5-pro:streamGenerateContent?alt=sse", want: EndpointStreamGenerateContent},
		{name: "gemini count", protocol: ProtocolGemini, path: "/models/gemini-2.5-pro:countTokens", want: EndpointCountTokens},
		{name: "gemini embed", protocol: ProtocolGemini, path: "/models/text-embedding:embedContent", want: EndpointEmbedContent},
		{name: "gemini interaction", protocol: ProtocolGemini, path: "/v1beta/interactions/abc/cancel", want: EndpointInteractions},
		{name: "segment false positive is rejected", protocol: ProtocolOpenAI, path: "/foo/responses-evil", want: EndpointUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := EndpointFamilyFromPath(tt.protocol, tt.path); got != tt.want {
				t.Fatalf("EndpointFamilyFromPath(%q, %q) = %q, want %q", tt.protocol, tt.path, got, tt.want)
			}
		})
	}
}

func TestNativeProtocolForRequestHonorsMethods(t *testing.T) {
	t.Parallel()

	tests := []struct {
		request RequestShape
		want    ProtocolCode
		ok      bool
	}{
		{request: RequestShape{Method: "PATCH", Path: "/v1/responses/x"}, want: ProtocolOpenAI, ok: true},
		{request: RequestShape{Method: "GET", Path: "/v1/messages"}, ok: false},
		{request: RequestShape{Method: "POST", Path: "/v1/messages/count_tokens"}, want: ProtocolAnthropic, ok: true},
		{request: RequestShape{Method: "DELETE", Path: "/v1beta/interactions/abc"}, want: ProtocolGemini, ok: true},
		{request: RequestShape{Method: "GET", Path: "/v1beta/models/gemini:generateContent"}, ok: false},
	}

	for _, tt := range tests {
		got, ok := NativeProtocolForRequest(tt.request)
		if got != tt.want || ok != tt.ok {
			t.Errorf("NativeProtocolForRequest(%#v) = %q, %v, want %q, %v", tt.request, got, ok, tt.want, tt.ok)
		}
	}
}

func TestResolveDownstreamProtocol(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		protocol ProtocolCode
		request  RequestShape
		want     DownstreamProtocol
	}{
		{name: "responses body stream", protocol: ProtocolOpenAI, request: RequestShape{Method: "POST", Path: "/v1/responses", Stream: true}, want: DownstreamResponsesSSE},
		{name: "responses compact stream is not response stream", protocol: ProtocolOpenAI, request: RequestShape{Method: "POST", Path: "/responses/compact", Stream: true}, want: DownstreamUnknownStream},
		{name: "chat accept stream", protocol: ProtocolOpenAI, request: RequestShape{Method: "POST", Path: "/chat/completions", Headers: map[string]string{"Accept": "text/event-stream"}}, want: DownstreamChatCompletionsSSE},
		{name: "chat rejected event stream", protocol: ProtocolOpenAI, request: RequestShape{Method: "POST", Path: "/chat/completions", Headers: map[string]string{"Accept": "text/event-stream; q=0"}}, want: DownstreamJSON},
		{name: "unknown openai stream", protocol: ProtocolOpenAI, request: RequestShape{Method: "POST", Path: "/embeddings", Stream: true}, want: DownstreamUnknownStream},
		{name: "anthropic messages", protocol: ProtocolAnthropic, request: RequestShape{Method: "POST", Path: "/v1/messages", Stream: true}, want: DownstreamMessagesSSE},
		{name: "anthropic quoted accept parameter", protocol: ProtocolAnthropic, request: RequestShape{Method: "POST", Path: "/v1/messages", Headers: map[string]string{"Accept": `application/json, text/event-stream; profile="a,b"; q=0.8`}}, want: DownstreamMessagesSSE},
		{name: "anthropic invalid event stream quality", protocol: ProtocolAnthropic, request: RequestShape{Method: "POST", Path: "/v1/messages", Headers: map[string]string{"Accept": "text/event-stream; q=NaN"}}, want: DownstreamJSON},
		{name: "gemini stream method", protocol: ProtocolGemini, request: RequestShape{Method: "POST", Path: "/v1beta/models/gemini:streamGenerateContent"}, want: DownstreamGeminiGenerateContentSSE},
		{name: "gemini alt sse", protocol: ProtocolGemini, request: RequestShape{Method: "POST", Path: "/models/gemini:generateContent?alt=sse"}, want: DownstreamGeminiGenerateContentSSE},
		{name: "gemini interaction", protocol: ProtocolGemini, request: RequestShape{Method: "POST", Path: "/interactions", Stream: true}, want: DownstreamGeminiInteractionsSSE},
		{name: "gemini interaction resource", protocol: ProtocolGemini, request: RequestShape{Method: "GET", Path: "/interactions/id-1", Stream: true}, want: DownstreamGeminiInteractionsSSE},
		{name: "gemini interaction query stream", protocol: ProtocolGemini, request: RequestShape{Method: "GET", Path: "/interactions/id-1?stream=TRUE"}, want: DownstreamGeminiInteractionsSSE},
		{name: "gemini interaction cancel is unknown stream", protocol: ProtocolGemini, request: RequestShape{Method: "GET", Path: "/interactions/id-1/cancel", Stream: true}, want: DownstreamUnknownStream},
		{name: "plain json", protocol: ProtocolOpenAI, request: RequestShape{Method: "POST", Path: "/responses"}, want: DownstreamJSON},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ResolveDownstreamProtocol(tt.protocol, tt.request); got != tt.want {
				t.Fatalf("ResolveDownstreamProtocol() = %q, want %q", got, tt.want)
			}
		})
	}
}

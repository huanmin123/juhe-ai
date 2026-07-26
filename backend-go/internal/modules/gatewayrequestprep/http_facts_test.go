package gatewayrequestprep

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPrepareHTTPRequestMapsMetadataWithoutReadingBody(t *testing.T) {
	t.Parallel()
	body := &panicReadCloser{}
	request := httptest.NewRequest(http.MethodPost, "http://gateway.test/v1/responses?key=secret-value&alt=sse", body)
	request.Header.Set("Accept", "application/json, text/event-stream; q=1")
	request.Header.Set("X-Juhe-Client-Profile", "codex")
	request.Header.Set("Authorization", "Bearer secret-value")
	originalURI := request.URL.RequestURI()
	originalAccept := append([]string(nil), request.Header.Values("Accept")...)

	got, err := PrepareHTTPRequest(request, HTTPFacts{StreamRequested: true, CodexTurnMetadataValid: true})
	if err != nil {
		t.Fatalf("PrepareHTTPRequest() error = %v", err)
	}
	if body.reads != 0 {
		t.Fatalf("PrepareHTTPRequest() read body %d times", body.reads)
	}
	if request.URL.RequestURI() != originalURI || !equalStrings(request.Header.Values("Accept"), originalAccept) {
		t.Fatal("PrepareHTTPRequest() mutated request metadata")
	}
	if got.Protocol() != ProtocolOpenAI || got.DownstreamProtocol() != DownstreamResponsesSSE || got.ClientProfile() != ClientProfileCodex {
		t.Fatalf("prepared result = protocol=%q downstream=%q profile=%q", got.Protocol(), got.DownstreamProtocol(), got.ClientProfile())
	}
	if got.CommittedFailureSignal() == "" {
		t.Fatal("prepared result has no committed failure signal")
	}
	if strings.Contains(fmt.Sprintf("%#v", got), "secret-value") {
		t.Fatal("prepared result retained a credential value")
	}
}

func TestPrepareHTTPRequestUsesSSEAndNativeFactsOnly(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		url         string
		prepare     func(*http.Request)
		facts       HTTPFacts
		wantStream  DownstreamProtocol
		wantProfile ClientProfile
	}{
		{
			name: "Gemini alt SSE and credential presence", url: "http://gateway.test/v1beta/models/gemini:generateContent?alt=sse&key=opaque",
			prepare:    func(request *http.Request) { request.Header.Set("User-Agent", "GeminiCLI/1.0") },
			wantStream: DownstreamGeminiStreamGenerateContentSSE, wantProfile: ClientProfileGeminiCLI,
		},
		{
			name: "SSE quality zero is not an event-stream request", url: "http://gateway.test/v1/chat/completions",
			prepare:    func(request *http.Request) { request.Header.Set("Accept", "text/event-stream;q=0") },
			wantStream: DownstreamJSON, wantProfile: ClientProfileGenericOpenAI,
		},
		{
			name: "Claude signature headers map without credentials", url: "http://gateway.test/v1/messages",
			prepare: func(request *http.Request) {
				request.Header.Set("User-Agent", "claude-cli/1.0")
				request.Header.Set("Anthropic-Beta", "claude-code-20250219")
			},
			facts: HTTPFacts{StreamRequested: true}, wantStream: DownstreamMessagesSSE, wantProfile: ClientProfileClaudeCode,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequest(http.MethodPost, test.url, nil)
			test.prepare(request)
			got, err := PrepareHTTPRequest(request, test.facts)
			if err != nil || got.DownstreamProtocol() != test.wantStream || got.ClientProfile() != test.wantProfile {
				t.Fatalf("PrepareHTTPRequest() = downstream=%q profile=%q err=%v", got.DownstreamProtocol(), got.ClientProfile(), err)
			}
		})
	}
}

func TestPrepareHTTPRequestRejectsNil(t *testing.T) {
	t.Parallel()
	if _, err := PrepareHTTPRequest(nil, HTTPFacts{}); !errors.Is(err, ErrHTTPRequestRequired) {
		t.Fatalf("PrepareHTTPRequest(nil) error = %v", err)
	}
}

func TestPrepareHTTPRequestHandlesRequestWithoutURL(t *testing.T) {
	t.Parallel()
	request := &http.Request{Method: http.MethodPost, Header: http.Header{}}
	got, err := PrepareHTTPRequest(request, HTTPFacts{FallbackProtocol: ProtocolOpenAI})
	if err != nil || got.Protocol() != ProtocolOpenAI || got.DownstreamProtocol() != DownstreamJSON {
		t.Fatalf("PrepareHTTPRequest() = protocol=%q downstream=%q err=%v", got.Protocol(), got.DownstreamProtocol(), err)
	}
}

type panicReadCloser struct{ reads int }

func (body *panicReadCloser) Read([]byte) (int, error) {
	body.reads++
	panic("gateway request preparation must not read body")
}

func (body *panicReadCloser) Close() error { return nil }

var _ io.ReadCloser = (*panicReadCloser)(nil)

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

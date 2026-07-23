package gatewayupstream

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/protocols/gateway"
	"juhe-ai/backend-go/internal/store/port"
)

func TestBuildOpenAIRequestOwnsBoundedBodyAndSanitizesHeaders(t *testing.T) {
	credential, err := NewCredential("upstream-secret", CredentialOptions{})
	if err != nil {
		t.Fatalf("NewCredential() error = %v", err)
	}
	body := []byte(`{"model":"gpt-5.5"}`)
	headers := http.Header{
		"Authorization":       {"Bearer local-key"},
		"Host":                {"local.gateway.test"},
		"Content-Length":      {"999999"},
		"Connection":          {"keep-alive, X-Remove-Me"},
		"X-Remove-Me":         {"connection-scoped"},
		"Proxy-Authorization": {"Basic leaked"},
		"X-Forwarded-For":     {"198.51.100.9"},
		"Cookie":              {"session=local"},
		"User-Agent":          {"codex-cli/1.0"},
		"OpenAI-Beta":         {"responses=v1"},
	}

	ctx, cancel := context.WithCancel(context.Background())
	req, definition, err := (Builder{MaxBodyBytes: 1024}).Build(Input{
		Context: ctx,
		Request: gateway.RequestShape{Method: http.MethodPost, Path: "/v1/responses?trace=1"},
		Candidate: port.GatewayAccountCandidate{
			ProtocolCode:    "openai",
			ProtocolVersion: "v1",
			Type:            "api_key",
		},
		BaseURL:    "https://api.example.test/openai/v1/",
		Credential: credential,
		Headers:    headers,
		Body:       body,
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if definition.ID != "openai-v1" {
		t.Fatalf("definition = %q, want openai-v1", definition.ID)
	}
	if got := req.URL.String(); got != "https://api.example.test/openai/v1/responses?trace=1" {
		t.Fatalf("URL = %q", got)
	}
	if req.Host != "api.example.test" {
		t.Fatalf("Host = %q", req.Host)
	}
	if req.ContentLength != int64(len(body)) || req.Header.Get("Content-Length") != "" {
		t.Fatalf("content length field/header = %d/%q", req.ContentLength, req.Header.Get("Content-Length"))
	}
	if req.Header.Get("Authorization") != "Bearer upstream-secret" {
		t.Fatalf("authorization = %q", req.Header.Get("Authorization"))
	}
	for _, name := range []string{"Connection", "X-Remove-Me", "Proxy-Authorization", "X-Forwarded-For", "Cookie", "Host"} {
		if got := req.Header.Get(name); got != "" {
			t.Errorf("%s leaked upstream: %q", name, got)
		}
	}
	if req.Header.Get("User-Agent") != "codex-cli/1.0" || req.Header.Get("OpenAI-Beta") != "responses=v1" {
		t.Fatalf("semantic headers were not preserved: %#v", req.Header)
	}

	// The request must own its bytes; later caller mutations cannot alter a retry.
	for i := range body {
		body[i] = 'x'
	}
	gotBody, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("ReadAll(body) error = %v", err)
	}
	if string(gotBody) != `{"model":"gpt-5.5"}` {
		t.Fatalf("owned body = %q", gotBody)
	}
	retryBody, err := req.GetBody()
	if err != nil {
		t.Fatalf("GetBody() error = %v", err)
	}
	retryBytes, err := io.ReadAll(retryBody)
	if err != nil {
		t.Fatalf("ReadAll(retry body) error = %v", err)
	}
	_ = retryBody.Close()
	if string(retryBytes) != `{"model":"gpt-5.5"}` {
		t.Fatalf("retry body = %q", retryBytes)
	}
	cancel()
	if !errors.Is(req.Context().Err(), context.Canceled) {
		t.Fatalf("request context error = %v", req.Context().Err())
	}
}

func TestBuildProtocolEndpointsAndAuthentication(t *testing.T) {
	tests := []struct {
		name       string
		request    gateway.RequestShape
		candidate  port.GatewayAccountCandidate
		baseURL    string
		options    CredentialOptions
		wantURL    string
		wantHeader string
		wantValue  string
	}{
		{
			name:       "openai oauth codex endpoint does not invent a v1 segment",
			request:    gateway.RequestShape{Method: http.MethodPost, Path: "/v1/responses", Stream: true},
			candidate:  port.GatewayAccountCandidate{ProtocolCode: "openai", ProtocolVersion: "v1", Type: "oauth"},
			baseURL:    "https://chatgpt.com/backend-api/codex",
			options:    CredentialOptions{AccountID: "account-1"},
			wantURL:    "https://chatgpt.com/backend-api/codex/responses",
			wantHeader: "Authorization",
			wantValue:  "Bearer secret",
		},
		{
			name:       "anthropic api key",
			request:    gateway.RequestShape{Method: http.MethodPost, Path: "/v1/messages?beta=1"},
			candidate:  port.GatewayAccountCandidate{ProtocolCode: "anthropic", ProtocolVersion: "v1", Type: "api_key"},
			baseURL:    "https://api.anthropic.test/custom",
			wantURL:    "https://api.anthropic.test/custom/v1/messages?beta=1",
			wantHeader: "X-Api-Key",
			wantValue:  "secret",
		},
		{
			name:       "gemini api key strips inbound credential query and adds stream response",
			request:    gateway.RequestShape{Method: http.MethodPost, Path: "/v1beta/models/gemini-2.5-pro:streamGenerateContent?key=local&trace=1"},
			candidate:  port.GatewayAccountCandidate{ProtocolCode: "gemini", ProtocolVersion: "v1beta", Type: "api_key"},
			baseURL:    "https://generativelanguage.test/proxy/v1beta",
			wantURL:    "https://generativelanguage.test/proxy/v1beta/models/gemini-2.5-pro:streamGenerateContent?alt=sse&trace=1",
			wantHeader: "X-Goog-Api-Key",
			wantValue:  "secret",
		},
		{
			name:       "gemini google oauth",
			request:    gateway.RequestShape{Method: http.MethodPost, Path: "/v1beta/models/gemini-2.5-pro:generateContent"},
			candidate:  port.GatewayAccountCandidate{ProtocolCode: "gemini", ProtocolVersion: "v1beta", Type: "google_oauth"},
			baseURL:    "https://generativelanguage.test",
			options:    CredentialOptions{QuotaProjectID: "quota-project"},
			wantURL:    "https://generativelanguage.test/v1beta/models/gemini-2.5-pro:generateContent",
			wantHeader: "Authorization",
			wantValue:  "Bearer secret",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			credential, err := NewCredential("secret", tt.options)
			if err != nil {
				t.Fatalf("NewCredential() error = %v", err)
			}
			req, _, err := (Builder{}).Build(Input{
				Context: context.Background(), Request: tt.request, Candidate: tt.candidate,
				BaseURL: tt.baseURL, Credential: credential,
				Headers: http.Header{"Authorization": {"Bearer local"}, "X-Api-Key": {"local"}, "X-Goog-Api-Key": {"local"}},
				Body:    []byte(`{}`),
			})
			if err != nil {
				t.Fatalf("Build() error = %v", err)
			}
			if req.URL.String() != tt.wantURL {
				t.Fatalf("URL = %q, want %q", req.URL, tt.wantURL)
			}
			if got := req.Header.Get(tt.wantHeader); got != tt.wantValue {
				t.Fatalf("%s = %q, want %q", tt.wantHeader, got, tt.wantValue)
			}
			if tt.candidate.ProtocolCode == "anthropic" && req.Header.Get("Anthropic-Version") != defaultAnthropicVersion {
				t.Fatalf("anthropic-version = %q", req.Header.Get("Anthropic-Version"))
			}
			if tt.candidate.Type == "google_oauth" && req.Header.Get("X-Goog-User-Project") != "quota-project" {
				t.Fatalf("quota project = %q", req.Header.Get("X-Goog-User-Project"))
			}
			if tt.candidate.Type == "oauth" && req.Header.Get("Chatgpt-Account-Id") != "account-1" {
				t.Fatalf("ChatGPT account id = %q", req.Header.Get("Chatgpt-Account-Id"))
			}
		})
	}
}

func TestBuildUsesAuthorizedResourceProtocol(t *testing.T) {
	credential, _ := NewCredential("secret", CredentialOptions{})
	req, definition, err := (Builder{}).Build(Input{
		Context: context.Background(),
		Request: gateway.RequestShape{Method: http.MethodPost, Path: "/v1/messages"},
		Candidate: port.GatewayAccountCandidate{
			ProtocolCode: "openai", ProtocolVersion: "v1", Type: "api_key",
			ResourceAccountID: "resource-1", ResourceProtocolCode: "anthropic", ResourceProtocolVersion: "v1", ResourceType: "api_key",
		},
		BaseURL: "https://api.anthropic.test", Credential: credential, Body: []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if definition.Code != gateway.ProtocolAnthropic || req.Header.Get("X-Api-Key") != "secret" {
		t.Fatalf("resource protocol was not selected: definition=%+v headers=%#v", definition, req.Header)
	}
}

func TestBuildUsesCandidateProtocolInsteadOfClientPathInference(t *testing.T) {
	credential, _ := NewCredential("secret", CredentialOptions{})
	_, _, err := (Builder{}).Build(Input{
		Context:   context.Background(),
		Request:   gateway.RequestShape{Method: http.MethodPost, Path: "/v1/responses"},
		Candidate: port.GatewayAccountCandidate{ProtocolCode: "unknown", ProtocolVersion: "v1", Type: "api_key"},
		BaseURL:   "https://api.example.test", Credential: credential, Body: []byte(`{}`),
	})
	if !errors.Is(err, ErrUnsupportedProtocol) {
		t.Fatalf("Build() error = %v, want unsupported candidate protocol", err)
	}
}

func TestBuildRejectsUnsafeOrUnboundedInput(t *testing.T) {
	credential, _ := NewCredential("secret", CredentialOptions{})
	base := Input{
		Context:   context.Background(),
		Request:   gateway.RequestShape{Method: http.MethodPost, Path: "/v1/responses"},
		Candidate: port.GatewayAccountCandidate{ProtocolCode: "openai", ProtocolVersion: "v1", Type: "api_key"},
		BaseURL:   "https://api.example.test", Credential: credential,
	}

	tests := []struct {
		name  string
		input Input
		build Builder
		want  error
	}{
		{name: "body too large", input: withBody(base, []byte("12345")), build: Builder{MaxBodyBytes: 4}, want: ErrBodyTooLarge},
		{name: "absolute request target", input: withPath(base, "https://attacker.test/v1/responses"), want: ErrInvalidRequestTarget},
		{name: "userinfo base url", input: withBaseURL(base, "https://user:pass@api.example.test/v1"), want: ErrInvalidBaseURL},
		{name: "unsupported scheme", input: withBaseURL(base, "file:///tmp/socket"), want: ErrInvalidBaseURL},
		{name: "missing context", input: withContext(base, nil), want: ErrContextRequired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := tt.build.Build(tt.input)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Build() error = %v, want %v", err, tt.want)
			}
		})
	}

	if _, err := NewCredential(strings.Repeat("x", maxCredentialBytes+1), CredentialOptions{}); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("oversized credential error = %v", err)
	}
	if _, err := NewCredential("secret", CredentialOptions{AccountID: "account\r\ninjected: yes"}); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("unsafe credential metadata error = %v", err)
	}
}

func withBody(input Input, body []byte) Input            { input.Body = body; return input }
func withPath(input Input, path string) Input            { input.Request.Path = path; return input }
func withBaseURL(input Input, baseURL string) Input      { input.BaseURL = baseURL; return input }
func withContext(input Input, ctx context.Context) Input { input.Context = ctx; return input }

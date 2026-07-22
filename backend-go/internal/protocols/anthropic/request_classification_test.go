package anthropic

import "testing"

func TestClassifyPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		rawTarget string
		valid     bool
		path      string
		endpoint  Endpoint
		betaQuery bool
	}{
		{name: "messages without version", rawTarget: "/messages", valid: true, path: "/messages", endpoint: EndpointMessages},
		{name: "messages with version and query", rawTarget: "/v1/messages?beta=true", valid: true, path: "/messages", endpoint: EndpointMessages, betaQuery: true},
		{name: "count tokens", rawTarget: "/v1/messages/count_tokens", valid: true, path: "/messages/count_tokens", endpoint: EndpointMessageTokenCounting},
		{name: "models", rawTarget: "/v1/models?limit=20", valid: true, path: "/models", endpoint: EndpointModels},
		{name: "version root", rawTarget: "/v1", valid: true, path: "/", endpoint: EndpointUnknown},
		{name: "empty target", rawTarget: "", valid: true, path: "/", endpoint: EndpointUnknown},
		{name: "missing leading slash", rawTarget: "v1/messages", valid: true, path: "/messages", endpoint: EndpointMessages},
		{name: "version boundary", rawTarget: "/v10/messages", valid: true, path: "/v10/messages", endpoint: EndpointUnknown},
		{name: "path is case sensitive", rawTarget: "/V1/messages", valid: true, path: "/V1/messages", endpoint: EndpointUnknown},
		{name: "encoded slash is not decoded", rawTarget: "/v1/messages%2Fcount_tokens", valid: true, path: "/messages%2Fcount_tokens", endpoint: EndpointUnknown},
		{name: "unrelated semicolon query remains valid", rawTarget: "/v1/messages?metadata=a;b", valid: true, path: "/messages", endpoint: EndpointMessages},
		{name: "invalid beta escape does not invalidate path", rawTarget: "/v1/messages?beta=%zz", valid: true, path: "/messages", endpoint: EndpointMessages},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ClassifyPath(tt.rawTarget)
			if got.Valid != tt.valid {
				t.Fatalf("Valid = %v, want %v", got.Valid, tt.valid)
			}
			if got.NormalizedPath != tt.path {
				t.Fatalf("NormalizedPath = %q, want %q", got.NormalizedPath, tt.path)
			}
			if got.Endpoint != tt.endpoint {
				t.Fatalf("Endpoint = %q, want %q", got.Endpoint, tt.endpoint)
			}
			if got.BetaQuery != tt.betaQuery {
				t.Fatalf("BetaQuery = %v, want %v", got.BetaQuery, tt.betaQuery)
			}
		})
	}
}

func TestClassifyRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     RequestInput
		supported bool
		endpoint  Endpoint
		mode      EndpointMode
		stream    bool
	}{
		{
			name:      "messages json",
			input:     RequestInput{Method: "post", Target: "/v1/messages"},
			supported: true,
			endpoint:  EndpointMessages,
			mode:      EndpointModeMessagesJSON,
		},
		{
			name:      "messages sse from body",
			input:     RequestInput{Method: "POST", Target: "/messages", Stream: true},
			supported: true,
			endpoint:  EndpointMessages,
			mode:      EndpointModeMessagesSSE,
			stream:    true,
		},
		{
			name:      "messages sse from accept",
			input:     RequestInput{Method: "POST", Target: "/messages", Accept: "application/json, Text/Event-Stream; q=0.8"},
			supported: true,
			endpoint:  EndpointMessages,
			mode:      EndpointModeMessagesSSE,
			stream:    true,
		},
		{
			name:      "messages sse from accept with quoted comma",
			input:     RequestInput{Method: "POST", Target: "/messages", Accept: `text/event-stream; profile="a,b"; q=0.8`},
			supported: true,
			endpoint:  EndpointMessages,
			mode:      EndpointModeMessagesSSE,
			stream:    true,
		},
		{
			name:      "event stream rejected by q zero",
			input:     RequestInput{Method: "POST", Target: "/messages", Accept: "text/event-stream; q=0"},
			supported: true,
			endpoint:  EndpointMessages,
			mode:      EndpointModeMessagesJSON,
		},
		{
			name:      "event stream rejected by invalid quality",
			input:     RequestInput{Method: "POST", Target: "/messages", Accept: "text/event-stream; q=NaN"},
			supported: true,
			endpoint:  EndpointMessages,
			mode:      EndpointModeMessagesJSON,
		},
		{
			name:      "count tokens",
			input:     RequestInput{Method: "POST", Target: "/v1/messages/count_tokens"},
			supported: true,
			endpoint:  EndpointMessageTokenCounting,
			mode:      EndpointModeMessageTokenCounting,
		},
		{
			name:      "models",
			input:     RequestInput{Method: "GET", Target: "/v1/models"},
			supported: true,
			endpoint:  EndpointModels,
			mode:      EndpointModeNone,
		},
		{
			name:     "wrong messages method",
			input:    RequestInput{Method: "GET", Target: "/v1/messages"},
			endpoint: EndpointMessages,
			mode:     EndpointModeNone,
		},
		{
			name:     "wrong models method",
			input:    RequestInput{Method: "POST", Target: "/v1/models"},
			endpoint: EndpointModels,
			mode:     EndpointModeNone,
		},
		{
			name:     "unknown endpoint",
			input:    RequestInput{Method: "POST", Target: "/v1/unknown", Stream: true},
			endpoint: EndpointUnknown,
			mode:     EndpointModeNone,
			stream:   true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ClassifyRequest(tt.input)
			if got.Supported != tt.supported {
				t.Fatalf("Supported = %v, want %v", got.Supported, tt.supported)
			}
			if got.Endpoint != tt.endpoint {
				t.Fatalf("Endpoint = %q, want %q", got.Endpoint, tt.endpoint)
			}
			if got.Mode != tt.mode {
				t.Fatalf("Mode = %q, want %q", got.Mode, tt.mode)
			}
			if got.Stream != tt.stream {
				t.Fatalf("Stream = %v, want %v", got.Stream, tt.stream)
			}
		})
	}
}

func TestClassifyClientProfile(t *testing.T) {
	t.Parallel()

	messagesJSON := ClassifyRequest(RequestInput{Method: "POST", Target: "/v1/messages"})
	messagesSSE := ClassifyRequest(RequestInput{Method: "POST", Target: "/v1/messages?beta=true", Stream: true})
	messagesSSENoBeta := ClassifyRequest(RequestInput{Method: "POST", Target: "/v1/messages", Stream: true})
	countTokens := ClassifyRequest(RequestInput{Method: "POST", Target: "/v1/messages/count_tokens"})
	models := ClassifyRequest(RequestInput{Method: "GET", Target: "/v1/models"})
	unknown := ClassifyRequest(RequestInput{Method: "POST", Target: "/v1/unknown"})

	tests := []struct {
		name        string
		input       ClientProfileInput
		profile     ClientProfile
		source      ClientProfileSource
		signalCount int
	}{
		{
			name:    "generic messages client",
			input:   ClientProfileInput{Request: messagesJSON},
			profile: ClientProfileGenericAnthropic,
			source:  ClientProfileSourceDefault,
		},
		{
			name:    "explicit hyphenated claude code",
			input:   ClientProfileInput{Request: messagesSSE, ExplicitProfile: " Claude-Code "},
			profile: ClientProfileClaudeCode,
			source:  ClientProfileSourceExplicitHeader,
		},
		{
			name: "real cli signature",
			input: ClientProfileInput{
				Request:             messagesSSE,
				UserAgent:           "claude-cli/2.1.181 (external, sdk-cli)",
				AnthropicBeta:       "claude-code-20250219, interleaved-thinking-2025-05-14",
				ClaudeCodeSessionID: "session_123",
			},
			profile:     ClientProfileClaudeCode,
			source:      ClientProfileSourceClaudeCodeSignature,
			signalCount: 4,
		},
		{
			name: "two independent signals",
			input: ClientProfileInput{
				Request:           messagesJSON,
				UserAgent:         "tool claude-cli/2.1.181",
				ClaudeCodeAgentID: "agent_123",
			},
			profile:     ClientProfileClaudeCode,
			source:      ClientProfileSourceClaudeCodeSignature,
			signalCount: 2,
		},
		{
			name: "single signal does not upgrade",
			input: ClientProfileInput{
				Request:   messagesSSENoBeta,
				UserAgent: "claude-cli/2.1.181",
			},
			profile:     ClientProfileGenericAnthropic,
			source:      ClientProfileSourceDefault,
			signalCount: 1,
		},
		{
			name: "session and agent headers are one signal",
			input: ClientProfileInput{
				Request:             messagesJSON,
				ClaudeCodeSessionID: "session_123",
				ClaudeCodeAgentID:   "agent_123",
			},
			profile:     ClientProfileGenericAnthropic,
			source:      ClientProfileSourceDefault,
			signalCount: 1,
		},
		{
			name:    "unknown explicit profile ignored",
			input:   ClientProfileInput{Request: messagesJSON, ExplicitProfile: "codex"},
			profile: ClientProfileGenericAnthropic,
			source:  ClientProfileSourceDefault,
		},
		{
			name:    "explicit profile cannot upgrade count tokens",
			input:   ClientProfileInput{Request: countTokens, ExplicitProfile: "claude_code"},
			profile: ClientProfileGenericAnthropic,
			source:  ClientProfileSourceDefault,
		},
		{
			name:    "explicit profile cannot upgrade models",
			input:   ClientProfileInput{Request: models, ExplicitProfile: "claude_code"},
			profile: ClientProfileGenericAnthropic,
			source:  ClientProfileSourceDefault,
		},
		{
			name:    "explicit profile cannot upgrade unknown path",
			input:   ClientProfileInput{Request: unknown, ExplicitProfile: "claude_code"},
			profile: ClientProfileGenericAnthropic,
			source:  ClientProfileSourceDefault,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ClassifyClientProfile(tt.input)
			if got.Profile != tt.profile {
				t.Fatalf("Profile = %q, want %q", got.Profile, tt.profile)
			}
			if got.Source != tt.source {
				t.Fatalf("Source = %q, want %q", got.Source, tt.source)
			}
			if got.SignatureSignalCount != tt.signalCount {
				t.Fatalf("SignatureSignalCount = %d, want %d", got.SignatureSignalCount, tt.signalCount)
			}
		})
	}
}

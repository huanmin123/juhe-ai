package accountprobe

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
	"juhe-ai/backend-go/internal/store/port"
)

func TestPrepareAPIKeyAttemptForOpenAIAnthropicAndGemini(t *testing.T) {
	now := time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		candidate  gatewaycandidatewindow.Candidate
		input      RequestInput
		wantURL    string
		wantHeader string
		wantValue  string
	}{
		{
			name: "OpenAI credential base URL wins",
			candidate: apiKeyCandidate("openai", "gpt", "profile_gpt_openai_v1", "https://default.example/v1", map[string]any{
				"api_key": "openai-key", "base_url": "https://override.example/openai",
			}),
			input:   RequestInput{Mode: ModeResponsesJSON, Model: "model"},
			wantURL: "https://override.example/openai/v1/responses", wantHeader: "Authorization", wantValue: "Bearer openai-key",
		},
		{
			name: "Anthropic Claude Code compatibility",
			candidate: apiKeyCandidate("anthropic", "anthropic", "profile_anthropic_anthropic_v1", "https://api.anthropic.com/v1", map[string]any{
				"api_key": "anthropic-key",
			}),
			input:   RequestInput{Mode: ModeMessagesSSE, Model: "model", SessionID: "session", Today: "2026-07-28", WorkingDirectory: `F:\work`},
			wantURL: "https://api.anthropic.com/v1/messages?beta=true", wantHeader: "X-Api-Key", wantValue: "anthropic-key",
		},
		{
			name: "GLM Anthropic bearer exception",
			candidate: apiKeyCandidate("anthropic", "glm", "profile_glm_coding_anthropic_v1", "https://glm.example/v1", map[string]any{
				"api_key": "glm-token",
			}),
			input:   RequestInput{Mode: ModeMessagesJSON, Model: "model", SessionID: "session", Today: "2026-07-28", WorkingDirectory: `F:\work`},
			wantURL: "https://glm.example/v1/messages?beta=true", wantHeader: "Authorization", wantValue: "Bearer glm-token",
		},
		{
			name: "Gemini native",
			candidate: apiKeyCandidate("gemini", "gemini", "profile_gemini_native_v1beta", "https://generativelanguage.googleapis.com", map[string]any{
				"api_key": "gemini-key",
			}),
			input:   RequestInput{Mode: ModeGenerateContentSSE, Model: "model"},
			wantURL: "https://generativelanguage.googleapis.com/v1beta/models/model:streamGenerateContent?alt=sse", wantHeader: "X-Goog-Api-Key", wantValue: "gemini-key",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prepared, err := PrepareRequest(test.candidate, test.input)
			if err != nil {
				t.Fatalf("PrepareRequest() error = %v", err)
			}
			attempt, err := PrepareAPIKeyAttempt(test.candidate, prepared, now)
			if err != nil {
				t.Fatalf("PrepareAPIKeyAttempt() error = %v", err)
			}
			header := attempt.Header()
			if attempt.URL() != test.wantURL || header.Get(test.wantHeader) != test.wantValue || attempt.KeyFingerprint() != "fingerprint" || attempt.KeyIndex() != 0 {
				t.Fatalf("attempt URL = %q headers=%#v", attempt.URL(), header)
			}
			if header.Get("X-Juhe-Client-Profile") != "" {
				t.Fatal("internal profile header leaked upstream")
			}
			if strings.Contains(test.name, "Anthropic") && (header.Get("Anthropic-Version") != "2023-06-01" || !strings.Contains(header.Get("Anthropic-Beta"), "effort-2025-11-24") || !strings.HasPrefix(header.Get("User-Agent"), "claude-cli/")) {
				t.Fatalf("Anthropic compatibility headers = %#v", header)
			}
		})
	}
}

func TestPrepareAPIKeyAttemptUsesAuthorizedResourceIdentity(t *testing.T) {
	candidate := apiKeyCandidate("openai", "binding", "binding-profile", "https://resource.example", map[string]any{"api_key": "key"})
	candidate.Projection.ResourceAccountID = "resource"
	candidate.Projection.ResourceProviderCode = "gemini"
	candidate.Projection.ResourceProviderProtocolProfileID = "profile_gemini_native_v1beta"
	candidate.Projection.ResourceProtocolCode = "gemini"
	candidate.Projection.ResourceProtocolVersion = "v1beta"
	candidate.Projection.ResourceType = "api_key"
	prepared, err := PrepareRequest(candidate, RequestInput{Mode: ModeGenerateContentJSON, Model: "model"})
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := PrepareAPIKeyAttempt(candidate, prepared, time.Now())
	if err != nil || attempt.Header().Get("X-Goog-Api-Key") != "key" || attempt.Header().Get("Authorization") != "" {
		t.Fatalf("attempt=%+v error=%v", attempt, err)
	}
}

func TestPrepareAPIKeyAttemptFailsClosed(t *testing.T) {
	candidate := apiKeyCandidate("openai", "gpt", "profile", "https://api.example", map[string]any{"api_key": "key"})
	prepared, err := PrepareRequest(candidate, RequestInput{Mode: ModeResponsesJSON, Model: "model"})
	if err != nil {
		t.Fatal(err)
	}
	candidate.Projection.Type = "oauth"
	if _, err := PrepareAPIKeyAttempt(candidate, prepared, time.Now()); !errors.Is(err, ErrUnsupportedCredential) {
		t.Fatalf("OAuth error = %v", err)
	}
	candidate.Projection.Type = "api_key"
	candidate.APIKeyRuntime[0].Status = "disabled"
	if _, err := PrepareAPIKeyAttempt(candidate, prepared, time.Now()); !errors.Is(err, ErrCredentialUnavailable) {
		t.Fatalf("disabled error = %v", err)
	}
	candidate.APIKeyRuntime[0].Status = "active"
	candidate.DefaultBaseURL = "ftp://api.example"
	if _, err := PrepareAPIKeyAttempt(candidate, prepared, time.Now()); !errors.Is(err, ErrInvalidBaseURL) {
		t.Fatalf("base URL error = %v", err)
	}
}

func TestAPIKeyAttemptDoesNotExposeCredentialThroughFormattingOrJSON(t *testing.T) {
	candidate := apiKeyCandidate("openai", "gpt", "profile", "https://api.example/path%20with%20space", map[string]any{"api_key": "super-secret"})
	prepared, err := PrepareRequest(candidate, RequestInput{Mode: ModeResponsesJSON, Model: "model"})
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := PrepareAPIKeyAttempt(candidate, prepared, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got := attempt.URL(); got != "https://api.example/path%20with%20space/v1/responses" {
		t.Fatalf("URL() = %q", got)
	}
	for name, formatted := range map[string]string{
		"String":   fmt.Sprintf("%v", attempt),
		"GoString": fmt.Sprintf("%#v", attempt),
	} {
		if strings.Contains(formatted, "super-secret") || formatted != "[REDACTED]" {
			t.Fatalf("%s formatting = %q", name, formatted)
		}
	}
	encoded, err := json.Marshal(attempt)
	if err != nil || string(encoded) != "{}" {
		t.Fatalf("JSON = %s error=%v", encoded, err)
	}
	header := attempt.Header()
	header.Set("Authorization", "changed")
	if attempt.Header().Get("Authorization") != "Bearer super-secret" {
		t.Fatal("Header() did not return a defensive clone")
	}
}

func apiKeyCandidate(protocol, provider, profile, baseURL string, credentials map[string]any) gatewaycandidatewindow.Candidate {
	return gatewaycandidatewindow.Candidate{
		Projection: port.GatewayAccountCandidate{
			AccountID: "account", ProviderCode: provider, ProviderProtocolProfileID: profile,
			ProtocolCode: protocol, ProtocolVersion: map[string]string{"openai": "v1", "anthropic": "v1", "gemini": "v1beta"}[protocol], Type: "api_key",
		},
		Credentials: gatewaycandidatewindow.NewCredentialSet(credentials), DefaultBaseURL: baseURL,
		SupportedModels: []string{"model"},
		APIKeyRuntime:   []gatewaycandidatewindow.APIKeyRuntime{{KeyIndex: 0, KeyFingerprint: "fingerprint", Status: "active"}},
	}
}

package upstreamidentity

import (
	"net/http"
	"testing"
)

func TestApplySystemClientHeadersUsesZCodeForExactGLMCodingProfiles(t *testing.T) {
	for _, profileID := range []string{ProfileGLMCodingOpenAIV1, ProfileGLMCodingAnthropicV1} {
		t.Run(profileID, func(t *testing.T) {
			headers := http.Header{}
			ApplySystemClientHeaders(headers, Input{ProviderCode: "glm", ProviderProtocolProfileID: profileID, CredentialType: "api_key"})
			if headers.Get("User-Agent") != "ZCode/3.11.2" || headers.Get("HTTP-Referer") != "https://zcode.z.ai" || headers.Get("X-ZCode-App-Version") != "3.11.2" || headers.Get("X-Title") != "Z Code@electron" {
				t.Fatalf("ZCode identity headers=%v", headers)
			}
			for _, dynamic := range []string{"X-Client-Ts", "X-Client-Sig", "X-Client-Nonce", "X-Device-Mid", "X-Client-Pow", "X-Session-Id"} {
				if value := headers.Get(dynamic); value != "" {
					t.Fatalf("dynamic header %s=%q must not be fabricated", dynamic, value)
				}
			}
		})
	}
}

func TestApplySystemClientHeadersUsesOpenCodeFallbackForGenericAPIKeys(t *testing.T) {
	for _, input := range []Input{
		{ProviderCode: "glm", ProviderProtocolProfileID: "profile_glm_general_openai_v1", CredentialType: "api_key"},
		{ProviderCode: "openai", ProviderProtocolProfileID: "profile_openai_openai_v1", CredentialType: "api_key"},
		{ProviderCode: "deepseek", ProviderProtocolProfileID: "profile_deepseek_anthropic_v1", CredentialType: "api_key"},
		{ProviderCode: "hybrid", ProviderProtocolProfileID: "profile_hybrid_openai_chat_v1", CredentialType: "api_key"},
		{ProviderCode: "hybrid", ProviderProtocolProfileID: "profile_hybrid_anthropic_messages_v1", CredentialType: "api_key"},
	} {
		headers := http.Header{}
		ApplySystemClientHeaders(headers, input)
		if headers.Get("User-Agent") != OpenCodeUserAgent || len(headers) != 1 {
			t.Fatalf("generic API-key profile must use the minimal OpenCode fallback: input=%+v headers=%v", input, headers)
		}
	}
}

func TestApplySystemClientHeadersKeepsExistingUserAgent(t *testing.T) {
	headers := http.Header{"User-Agent": []string{"custom-system-client/1.0"}}
	ApplySystemClientHeaders(headers, Input{ProviderCode: "openai", ProviderProtocolProfileID: "profile_openai_openai_v1", CredentialType: "api_key"})
	if got := headers.Get("User-Agent"); got != "custom-system-client/1.0" {
		t.Fatalf("existing User-Agent must win over fallback, got %q", got)
	}
}

func TestApplySystemClientHeadersDoesNotImpersonateOpenCodeForOAuthFallback(t *testing.T) {
	for _, input := range []Input{
		{ProviderCode: "openai", ProviderProtocolProfileID: "profile_openai_openai_v1", CredentialType: "oauth"},
		{ProviderCode: "gemini", ProviderProtocolProfileID: "profile_gemini_native_v1beta", CredentialType: "google_oauth", OAuthType: "ai_studio"},
		{ProviderCode: "xai", ProviderProtocolProfileID: ProfileXAIOpenAIV1, CredentialType: "oauth", UpstreamHostname: "api.x.ai"},
	} {
		headers := http.Header{}
		ApplySystemClientHeaders(headers, input)
		if len(headers) != 0 {
			t.Fatalf("OAuth without an exact client identity must not receive OpenCode fallback: input=%+v headers=%v", input, headers)
		}
	}
}

func TestApplySystemClientHeadersKeepsKnownClientBoundIdentities(t *testing.T) {
	claude := http.Header{}
	ApplySystemClientHeaders(claude, Input{ProviderCode: "anthropic", ProviderProtocolProfileID: ProfileAnthropicAnthropicV1, CredentialType: "oauth"})
	if claude.Get("User-Agent") != "claude-cli/2.1.161 (external, cli)" || claude.Get("x-stainless-runtime") != "node" || claude.Get("anthropic-beta") == "" {
		t.Fatalf("Claude Code identity headers=%v", claude)
	}

	gemini := http.Header{}
	ApplySystemClientHeaders(gemini, Input{ProviderCode: "gemini", ProviderProtocolProfileID: ProfileGeminiNativeV1Beta, CredentialType: "google_oauth", OAuthType: "code_assist"})
	if gemini.Get("User-Agent") != "GeminiCLI/0.1.5 (Windows; AMD64)" {
		t.Fatalf("Gemini CLI identity headers=%v", gemini)
	}

	grok := http.Header{}
	ApplySystemClientHeaders(grok, Input{ProviderCode: "xai", ProviderProtocolProfileID: ProfileXAIOpenAIV1, CredentialType: "oauth", UpstreamHostname: "cli-chat-proxy.grok.com"})
	if grok.Get("User-Agent") != "xai-grok-workspace/0.2.93" || grok.Get("x-xai-token-auth") != "xai-grok-cli" {
		t.Fatalf("Grok identity headers=%v", grok)
	}

	for name, headers := range map[string]http.Header{"claude": claude, "gemini": gemini, "grok": grok} {
		if headers.Get("User-Agent") == OpenCodeUserAgent || headers.Get("X-Opencode-Session") != "" || headers.Get("X-Opencode-Project") != "" {
			t.Fatalf("exact %s identity must not be replaced or supplemented with OpenCode session headers: %v", name, headers)
		}
	}
}

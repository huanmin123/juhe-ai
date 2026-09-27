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

// BUG-0201：GLM 家族（provider glm、profile_glm_ 前缀、两个 hybrid 桥接档案）
// 的 API Key 系统请求统一走 ZCode 全套身份，OpenCode 万金油兜底删除。
func TestApplySystemClientHeadersSelectsZCodeForWholeGLMFamily(t *testing.T) {
	for _, input := range []Input{
		{ProviderCode: "glm", ProviderProtocolProfileID: "profile_glm_general_openai_v1", CredentialType: "api_key"},
		{ProviderCode: "", ProviderProtocolProfileID: "profile_glm_general_openai_v1", CredentialType: "api_key"},
		{ProviderCode: "hybrid", ProviderProtocolProfileID: ProfileHybridOpenAIChatV1, CredentialType: "api_key"},
		{ProviderCode: "hybrid", ProviderProtocolProfileID: ProfileHybridAnthropicMessagesV1, CredentialType: "api_key"},
		{ProviderCode: "", ProviderProtocolProfileID: ProfileHybridOpenAIChatV1, CredentialType: "api_key"},
		{ProviderCode: "", ProviderProtocolProfileID: ProfileHybridAnthropicMessagesV1, CredentialType: "api_key"},
	} {
		headers := http.Header{}
		ApplySystemClientHeaders(headers, input)
		if headers.Get("User-Agent") != "ZCode/3.11.2" || headers.Get("HTTP-Referer") != "https://zcode.z.ai" || headers.Get("X-ZCode-App-Version") != "3.11.2" || headers.Get("X-Title") != "Z Code@electron" {
			t.Fatalf("GLM family must use the full ZCode identity: input=%+v headers=%v", input, headers)
		}
		if headers.Get("anthropic-beta") != "" || headers.Get("User-Agent") == OpenCodeUserAgent {
			t.Fatalf("GLM family identity must not leak OpenCode or Anthropic OAuth headers: input=%+v headers=%v", input, headers)
		}
	}
}

// BUG-0201：GPT/Codex 家族的 API Key 系统请求仅注入 Codex Desktop 静态 UA；
// originator/session-id 等每请求动态头由调用方生成。
func TestApplySystemClientHeadersUsesCodexDesktopUAForGPTFamilyAPIKeys(t *testing.T) {
	for _, input := range []Input{
		{ProviderCode: "gpt", ProviderProtocolProfileID: "profile_gpt_openai_v1", CredentialType: "api_key"},
		{ProviderCode: "codex", CredentialType: "api_key"},
		{ProviderCode: "", ProviderProtocolProfileID: "profile_codex_responses_v1", CredentialType: "api_key"},
	} {
		headers := http.Header{}
		ApplySystemClientHeaders(headers, input)
		if headers.Get("User-Agent") != CodexDesktopUserAgent || len(headers) != 1 {
			t.Fatalf("GPT family API-key requests must carry the static Codex Desktop UA only: input=%+v headers=%v", input, headers)
		}
		for _, dynamic := range []string{"Originator", "Session-Id", "Thread-Id", "X-Codex-Window-Id"} {
			if value := headers.Get(dynamic); value != "" {
				t.Fatalf("dynamic header %s=%q must stay with the caller", dynamic, value)
			}
		}
	}
}

// BUG-0201：无法识别的上游一律不注入身份。provider openai 承载「通用
// OpenAI-compatible 供应商」泛化档案（supeai 类第三方上游即挂此组合），
// 不构成家族依据。
func TestApplySystemClientHeadersLeavesUnrecognizedUpstreamsWithoutIdentity(t *testing.T) {
	for _, input := range []Input{
		{ProviderCode: "openai", ProviderProtocolProfileID: "profile_openai_openai_v1", CredentialType: "api_key"},
		{ProviderCode: "", ProviderProtocolProfileID: "profile_openai_openai_v1", CredentialType: "api_key"},
		{ProviderCode: "deepseek", ProviderProtocolProfileID: "profile_deepseek_anthropic_v1", CredentialType: "api_key"},
		{ProviderCode: "supeai", CredentialType: "api_key"},
		{ProviderCode: "", CredentialType: "api_key"},
	} {
		headers := http.Header{}
		ApplySystemClientHeaders(headers, input)
		if len(headers) != 0 {
			t.Fatalf("unrecognized upstream must not receive any fabricated identity: input=%+v headers=%v", input, headers)
		}
	}
}

func TestApplySystemClientHeadersKeepsExistingUserAgent(t *testing.T) {
	headers := http.Header{"User-Agent": []string{"custom-system-client/1.0"}}
	ApplySystemClientHeaders(headers, Input{ProviderCode: "openai", ProviderProtocolProfileID: "profile_openai_openai_v1", CredentialType: "api_key"})
	if got := headers.Get("User-Agent"); got != "custom-system-client/1.0" {
		t.Fatalf("existing User-Agent must win over fallback, got %q", got)
	}
	// 已有显式 UA 时即使命中家族也不得补充或部分覆盖成混合身份。
	familyHeaders := http.Header{"User-Agent": []string{"custom-system-client/1.0"}}
	ApplySystemClientHeaders(familyHeaders, Input{ProviderCode: "glm", ProviderProtocolProfileID: ProfileGLMCodingOpenAIV1, CredentialType: "api_key"})
	if got := familyHeaders.Get("User-Agent"); got != "custom-system-client/1.0" || familyHeaders.Get("X-ZCode-App-Version") != "" || familyHeaders.Get("HTTP-Referer") != "" {
		t.Fatalf("existing User-Agent must suppress the whole family identity, got headers=%v", familyHeaders)
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
			t.Fatalf("OAuth without an exact client identity must not receive a fabricated identity: input=%+v headers=%v", input, headers)
		}
	}
}

func TestApplySystemClientHeadersKeepsKnownClientBoundIdentities(t *testing.T) {
	claude := http.Header{}
	ApplySystemClientHeaders(claude, Input{ProviderCode: "anthropic", ProviderProtocolProfileID: ProfileAnthropicAnthropicV1, CredentialType: "oauth"})
	if claude.Get("User-Agent") != "claude-cli/2.1.161 (external, cli)" || claude.Get("x-stainless-runtime") != "node" || claude.Get("anthropic-beta") == "" {
		t.Fatalf("Claude Code identity headers=%v", claude)
	}

	// Anthropic 家族 API Key：不注入任何 Claude 客户端身份（BUG-0201，实测
	// supeai.cc 对 claude-cli UA 的 POST 做无响应挂起），保持传输层默认 Go
	// UA；profile 单独也可确立家族。
	for _, input := range []Input{
		{ProviderCode: "anthropic", ProviderProtocolProfileID: ProfileAnthropicAnthropicV1, CredentialType: "api_key"},
		{ProviderCode: "", ProviderProtocolProfileID: ProfileAnthropicAnthropicV1, CredentialType: "api_key"},
	} {
		claudeAPIKey := http.Header{}
		ApplySystemClientHeaders(claudeAPIKey, input)
		if claudeAPIKey.Get("User-Agent") != "" || claudeAPIKey.Get("x-app") != "" ||
			claudeAPIKey.Get("x-stainless-runtime") != "" || claudeAPIKey.Get("anthropic-dangerous-direct-browser-access") != "" {
			t.Fatalf("Anthropic API-key requests must not fabricate any client identity: input=%+v headers=%v", input, claudeAPIKey)
		}
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

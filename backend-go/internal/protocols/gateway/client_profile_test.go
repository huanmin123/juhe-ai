package gateway

import "testing"

func TestResolveClientProfile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		protocol ProtocolCode
		request  RequestShape
		want     ClientProfileResolution
	}{
		{
			name:     "codex turn metadata on responses stream",
			protocol: ProtocolOpenAI,
			request:  RequestShape{Method: "POST", Path: "/v1/responses", Stream: true, Headers: map[string]string{"X-Codex-Turn-Metadata": `{"turn_id":"turn-1"}`}},
			want:     ClientProfileResolution{Profile: ClientProfileCodex, Source: ClientProfileSourceCodexTurnMetadata, Compatibility: CompatibilityCodexResponses},
		},
		{
			name:     "codex compact does not require stream",
			protocol: ProtocolOpenAI,
			request:  RequestShape{Method: "POST", Path: "/responses/compact", Headers: map[string]string{"x-codex-turn-metadata": `{"turn_id":"turn-1"}`}},
			want:     ClientProfileResolution{Profile: ClientProfileCodex, Source: ClientProfileSourceCodexTurnMetadata, Compatibility: CompatibilityCodexResponses},
		},
		{
			name:     "codex metadata on json response is ignored",
			protocol: ProtocolOpenAI,
			request:  RequestShape{Method: "POST", Path: "/responses", Headers: map[string]string{"x-codex-turn-metadata": `{"turn_id":"turn-1"}`}},
			want:     ClientProfileResolution{Profile: ClientProfileGenericOpenAI, Source: ClientProfileSourceDefault, Compatibility: CompatibilityOpenAIStandard},
		},
		{
			name:     "explicit codex uses canonical OpenAI compatibility",
			protocol: ProtocolOpenAI,
			request:  RequestShape{Method: "POST", Path: "/responses", Headers: map[string]string{"x-juhe-client-profile": "codex"}},
			want:     ClientProfileResolution{Profile: ClientProfileCodex, Source: ClientProfileSourceExplicitHeader, Compatibility: CompatibilityOpenAIStandard},
		},
		{
			name:     "explicit claude code",
			protocol: ProtocolAnthropic,
			request:  RequestShape{Method: "POST", Path: "/v1/messages", Headers: map[string]string{"x-juhe-client-profile": " Claude--Code "}},
			want:     ClientProfileResolution{Profile: ClientProfileClaudeCode, Source: ClientProfileSourceExplicitHeader, Compatibility: CompatibilityClaudeCode},
		},
		{
			name:     "claude signature requires two signals",
			protocol: ProtocolAnthropic,
			request:  RequestShape{Method: "POST", Path: "/messages?beta=true", Headers: map[string]string{"User-Agent": "claude-cli/1.2.3"}},
			want:     ClientProfileResolution{Profile: ClientProfileClaudeCode, Source: ClientProfileSourceClaudeSignature, Compatibility: CompatibilityClaudeCode},
		},
		{
			name:     "explicit claude code supports count tokens",
			protocol: ProtocolAnthropic,
			request:  RequestShape{Method: "POST", Path: "/messages/count_tokens", Headers: map[string]string{"x-juhe-client-profile": "claude_code"}},
			want:     ClientProfileResolution{Profile: ClientProfileClaudeCode, Source: ClientProfileSourceExplicitHeader, Compatibility: CompatibilityClaudeCode},
		},
		{
			name:     "claude signature does not infer count tokens",
			protocol: ProtocolAnthropic,
			request:  RequestShape{Method: "POST", Path: "/messages/count_tokens?beta=true", Headers: map[string]string{"user-agent": "claude-cli/1.2.3"}},
			want:     ClientProfileResolution{Profile: ClientProfileGenericAnthropic, Source: ClientProfileSourceDefault, Compatibility: CompatibilityAnthropicNative},
		},
		{
			name:     "one claude signal is generic",
			protocol: ProtocolAnthropic,
			request:  RequestShape{Method: "POST", Path: "/messages", Headers: map[string]string{"User-Agent": "claude-cli/1.2.3"}},
			want:     ClientProfileResolution{Profile: ClientProfileGenericAnthropic, Source: ClientProfileSourceDefault, Compatibility: CompatibilityAnthropicNative},
		},
		{
			name:     "claude signature requires post",
			protocol: ProtocolAnthropic,
			request:  RequestShape{Method: "GET", Path: "/messages?beta=true", Headers: map[string]string{"user-agent": "claude-cli/1.2.3"}},
			want:     ClientProfileResolution{Profile: ClientProfileGenericAnthropic, Source: ClientProfileSourceDefault, Compatibility: CompatibilityAnthropicNative},
		},
		{
			name:     "gemini signature",
			protocol: ProtocolGemini,
			request:  RequestShape{Method: "POST", Path: "/v1beta/models/gemini:generateContent", Headers: map[string]string{"User-Agent": "GeminiCLI/0.9", "Authorization": "Bearer redacted"}},
			want:     ClientProfileResolution{Profile: ClientProfileGeminiCLI, Source: ClientProfileSourceGeminiSignature, Compatibility: CompatibilityOpenAIStandard},
		},
		{
			name:     "explicit gemini cli supports interactions",
			protocol: ProtocolGemini,
			request:  RequestShape{Method: "POST", Path: "/interactions", Headers: map[string]string{"x-juhe-client-profile": "gemini_cli"}},
			want:     ClientProfileResolution{Profile: ClientProfileGeminiCLI, Source: ClientProfileSourceExplicitHeader, Compatibility: CompatibilityOpenAIStandard},
		},
		{
			name:     "gemini signature does not infer interactions",
			protocol: ProtocolGemini,
			request:  RequestShape{Method: "POST", Path: "/interactions", Headers: map[string]string{"user-agent": "GeminiCLI/0.9", "authorization": "Bearer redacted"}},
			want:     ClientProfileResolution{Profile: ClientProfileGenericGemini, Source: ClientProfileSourceDefault, Compatibility: CompatibilityOpenAIStandard},
		},
		{
			name:     "explicit gemini cli rejects unsupported interaction resource post",
			protocol: ProtocolGemini,
			request:  RequestShape{Method: "POST", Path: "/interactions/abc", Headers: map[string]string{"x-juhe-client-profile": "gemini_cli"}},
			want:     ClientProfileResolution{Profile: ClientProfileGenericGemini, Source: ClientProfileSourceDefault, Compatibility: CompatibilityOpenAIStandard},
		},
		{
			name:     "explicit gemini cli rejects interaction root get",
			protocol: ProtocolGemini,
			request:  RequestShape{Method: "GET", Path: "/interactions", Headers: map[string]string{"x-juhe-client-profile": "gemini_cli"}},
			want:     ClientProfileResolution{Profile: ClientProfileGenericGemini, Source: ClientProfileSourceDefault, Compatibility: CompatibilityOpenAIStandard},
		},
		{
			name:     "gemini signature requires post",
			protocol: ProtocolGemini,
			request:  RequestShape{Method: "GET", Path: "/models/gemini:generateContent", Headers: map[string]string{"user-agent": "GeminiCLI/0.9", "authorization": "Bearer redacted"}},
			want:     ClientProfileResolution{Profile: ClientProfileGenericGemini, Source: ClientProfileSourceDefault, Compatibility: CompatibilityOpenAIStandard},
		},
		{
			name:     "gemini signature does not infer count tokens",
			protocol: ProtocolGemini,
			request:  RequestShape{Method: "POST", Path: "/models/gemini:countTokens", Headers: map[string]string{"user-agent": "GeminiCLI/0.9", "x-goog-api-key": "redacted"}},
			want:     ClientProfileResolution{Profile: ClientProfileGenericGemini, Source: ClientProfileSourceDefault, Compatibility: CompatibilityOpenAIStandard},
		},
		{
			name:     "gemini signature does not infer embed content",
			protocol: ProtocolGemini,
			request:  RequestShape{Method: "POST", Path: "/models/gemini:embedContent", Headers: map[string]string{"user-agent": "GeminiCLI/0.9", "x-goog-api-key": "redacted"}},
			want:     ClientProfileResolution{Profile: ClientProfileGenericGemini, Source: ClientProfileSourceDefault, Compatibility: CompatibilityOpenAIStandard},
		},
		{
			name:     "double underscore profile is rejected",
			protocol: ProtocolAnthropic,
			request:  RequestShape{Method: "POST", Path: "/messages", Headers: map[string]string{"x-juhe-client-profile": "claude__code"}},
			want:     ClientProfileResolution{Profile: ClientProfileGenericAnthropic, Source: ClientProfileSourceDefault, Compatibility: CompatibilityAnthropicNative},
		},
		{
			name:     "explicit profile is constrained to native shape",
			protocol: ProtocolGemini,
			request:  RequestShape{Method: "GET", Path: "/unknown", Headers: map[string]string{"x-juhe-client-profile": "gemini_cli"}},
			want:     ClientProfileResolution{Profile: ClientProfileGenericGemini, Source: ClientProfileSourceDefault, Compatibility: CompatibilityOpenAIStandard},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := ResolveClientProfile(tt.protocol, tt.request)
			if !ok || got != tt.want {
				t.Fatalf("ResolveClientProfile() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestResolveClientProfileRejectsUnknownProtocol(t *testing.T) {
	t.Parallel()

	if got, ok := ResolveClientProfile("unknown", RequestShape{}); ok || got != (ClientProfileResolution{}) {
		t.Fatalf("ResolveClientProfile() = %#v, %v", got, ok)
	}
}

func TestRequestShapeHeaderIsCaseInsensitive(t *testing.T) {
	t.Parallel()

	request := RequestShape{Headers: map[string]string{"X-Goog-Api-Key": "secret"}}
	if got := request.Header("x-goog-api-key"); got != "secret" {
		t.Fatalf("Header() = %q", got)
	}
}

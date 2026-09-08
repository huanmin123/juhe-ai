package gatewayopenai

import "testing"

func TestBuildUpstreamURL(t *testing.T) {
	cases := []struct {
		name, base, path, want string
	}{
		{
			name: "standard base URL appends /v1",
			base: "https://api.openai.com",
			path: "/chat/completions",
			want: "https://api.openai.com/v1/chat/completions",
		},
		{
			name: "base URL with existing /v1 suffix does not duplicate",
			base: "https://api.openai.com/v1",
			path: "/chat/completions",
			want: "https://api.openai.com/v1/chat/completions",
		},
		{
			name: "base URL without trailing slash",
			base: "https://api.openai.com/",
			path: "/models",
			want: "https://api.openai.com/v1/models",
		},
		// D-152: gemini-openai-chat v1beta profile URL handling.
		{
			name: "gemini-openai-chat v1beta base strips /v1 from client path",
			base: "https://generativelanguage.googleapis.com/v1beta/openai",
			path: "/v1/chat/completions",
			want: "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions",
		},
		{
			name: "gemini-openai-chat v1beta with query",
			base: "https://generativelanguage.googleapis.com/v1beta/openai/",
			path: "/v1/models?key=abc",
			want: "https://generativelanguage.googleapis.com/v1beta/openai/models?key=abc",
		},
		{
			name: "gemini-openai-chat v1beta chat path only",
			base: "https://generativelanguage.googleapis.com/v1beta/openai",
			path: "/v1/chat",
			want: "https://generativelanguage.googleapis.com/v1beta/openai/chat",
		},
		// D-152 补充覆盖（BUG-0175 波4 W4-F）：
		{
			name: "gemini-openai-chat v1beta base with already-stripped client path",
			base: "https://generativelanguage.googleapis.com/v1beta/openai",
			path: "/chat/completions",
			want: "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions",
		},
		{
			name: "gemini-openai-chat v1beta base matching is case-insensitive",
			base: "https://generativelanguage.googleapis.com/V1Beta/OpenAI",
			path: "/v1/chat/completions",
			want: "https://generativelanguage.googleapis.com/V1Beta/OpenAI/chat/completions",
		},
		{
			name: "gemini v1beta base without /openai suffix keeps standard /v1 append",
			base: "https://generativelanguage.googleapis.com/v1beta",
			path: "/v1/chat/completions",
			want: "https://generativelanguage.googleapis.com/v1beta/v1/chat/completions",
		},
		{
			name: "standard base still strips client /v1 (no double version)",
			base: "https://api.openai.com",
			path: "/v1/chat/completions",
			want: "https://api.openai.com/v1/chat/completions",
		},
		{
			name: "gemini-openai-chat v1beta responses path with query",
			base: "https://generativelanguage.googleapis.com/v1beta/openai",
			path: "/v1/responses?a=1",
			want: "https://generativelanguage.googleapis.com/v1beta/openai/responses?a=1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := BuildUpstreamURL(tc.base, tc.path)
			if got != tc.want {
				t.Errorf("BuildUpstreamURL(%q, %q) = %q, want %q", tc.base, tc.path, got, tc.want)
			}
		})
	}
}

func TestGeminiOpenAIChatBaseUrlOwnsOpenAIPath(t *testing.T) {
	cases := []struct {
		base   string
		expect bool
	}{
		{"https://generativelanguage.googleapis.com/v1beta/openai", true},
		{"https://generativelanguage.googleapis.com/v1beta/openai/", true},
		{"https://generativelanguage.googleapis.com/V1BETA/openai", true}, // case insensitive
		{"https://generativelanguage.googleapis.com/v1/openai", false},    // v1, not v1beta
		{"https://api.openai.com/v1", false},
		{"https://example.com/v1beta/openai/path", false}, // doesn't end with /v1beta/openai
		{"https://example.com/openai", false},
	}
	for _, tc := range cases {
		got := geminiOpenAIChatBaseUrlOwnsOpenAIPath(tc.base)
		if got != tc.expect {
			t.Errorf("geminiOpenAIChatBaseUrlOwnsOpenAIPath(%q) = %v, want %v", tc.base, got, tc.expect)
		}
	}
}

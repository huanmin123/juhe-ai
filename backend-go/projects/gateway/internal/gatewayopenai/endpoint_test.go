package gatewayopenai

import "testing"

// TestBuildUpstreamURL 钉 v2 直拼契约（上游 Base URL 版本根语义 v2）：
// base_url 即版本根，TrimRight 去尾斜杠后直拼 opPath（入站路径剥可选 /v1
// 前缀的剩余路径 + query）；不再对 base 补 /v1、不再做 gemini-openai-chat
// 豁免、不做版本段去重。
func TestBuildUpstreamURL(t *testing.T) {
	cases := []struct {
		name, base, path, want string
	}{
		// 行为修复（v2 契约）：裸域 base 不再补 /v1——base_url 必须配置为
		// 版本根（如 https://api.openai.com/v1），裸域直拼得 /chat/completions。
		{
			name: "bare origin base concatenates opPath directly (v2)",
			base: "https://api.openai.com",
			path: "/chat/completions",
			want: "https://api.openai.com/chat/completions",
		},
		{
			// 行为修复（v2 契约）：版本根 base 自带 /v1，不再有去重分支。
			name: "version-root base with /v1 suffix",
			base: "https://api.openai.com/v1",
			path: "/chat/completions",
			want: "https://api.openai.com/v1/chat/completions",
		},
		{
			name: "base URL trailing slash trimmed before concat",
			base: "https://api.openai.com/",
			path: "/models",
			want: "https://api.openai.com/models",
		},
		{
			name: "volcengine ark version root (头号验收用例)：/api/v3 + 入站 /v1 剥离直拼",
			base: "https://ark.cn-beijing.volces.com/api/v3",
			path: "/v1/chat/completions",
			want: "https://ark.cn-beijing.volces.com/api/v3/chat/completions",
		},
		{
			// 行为修复（v2 契约）：GLM 版本根 /api/paas/v4 + 入站 /v1 剥离直拼；
			// 旧行为在此形态多拼一层 /v1（/api/paas/v4/v1/chat/completions），
			// v2 删除补缀后为正确官方端点。
			name: "glm version root /api/paas/v4 (旧行为多拼 /v1，此为行为修复)",
			base: "https://open.bigmodel.cn/api/paas/v4",
			path: "/v1/chat/completions",
			want: "https://open.bigmodel.cn/api/paas/v4/chat/completions",
		},
		// gemini-openai-chat v1beta 档案（D-152 场景保留）：base 自带完整服务
		// 路径，直拼语义与 v2 一致（原豁免分支删除后同一结果）。
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
		{
			name: "gemini-openai-chat v1beta base with already-stripped client path",
			base: "https://generativelanguage.googleapis.com/v1beta/openai",
			path: "/chat/completions",
			want: "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions",
		},
		{
			// 行为修复（v2 契约）：gemini v1beta 根（非 /openai 档案）不再补
			// /v1，直拼得 /v1beta/chat/completions。
			name: "gemini v1beta base without /openai suffix concatenates directly",
			base: "https://generativelanguage.googleapis.com/v1beta",
			path: "/v1/chat/completions",
			want: "https://generativelanguage.googleapis.com/v1beta/chat/completions",
		},
		{
			name: "standard base still strips client /v1 (no double version)",
			base: "https://api.openai.com",
			path: "/v1/chat/completions",
			want: "https://api.openai.com/chat/completions",
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
			got, err := BuildUpstreamURL(tc.base, tc.path)
			if err != nil {
				t.Fatalf("BuildUpstreamURL(%q, %q) error: %v", tc.base, tc.path, err)
			}
			if got != tc.want {
				t.Errorf("BuildUpstreamURL(%q, %q) = %q, want %q", tc.base, tc.path, got, tc.want)
			}
		})
	}
}

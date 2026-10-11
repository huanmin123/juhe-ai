package accountprobe

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamenvelope"
)

// TestM4EnvelopeMigrationAnchors 钉住 M4 组合式迁移的接缝（信封统一设计
// §6 M4）：预算常量单一来源、URL 归一化经 upstreamenvelope.UpstreamURL、
// 标准头 accept 经 upstreamenvelope.AcceptHeader、账户形态 → 信封认证策略
// 映射。payload 内容（L3）与认证头写入按行为零变化保留在本包，不经本测试
// 断言（既有 payload / 认证头测试继续覆盖）。
func TestM4EnvelopeMigrationAnchors(t *testing.T) {
	// 锚点一：outputTokenLimit 引用信封 DefaultProbeOutputTokens，BUG-0201
	// 下 1024 的既有语义保持。
	if outputTokenLimit != upstreamenvelope.DefaultProbeOutputTokens {
		t.Fatalf("outputTokenLimit=%d 与信封 DefaultProbeOutputTokens=%d 脱钩", outputTokenLimit, upstreamenvelope.DefaultProbeOutputTokens)
	}
	if outputTokenLimit != 1024 {
		t.Fatalf("BUG-0201 既有语义变化：outputTokenLimit=%d", outputTokenLimit)
	}

	// 锚点二：代表性 URL（三协议族 + gemini-openai 档案 + Codex 固定上游）
	// 与信封直连结果一致；accountprobe 侧分支选择经 catalog 代表 mode
	// （与 modelcheckprobe buildURL 的 M2 同规），Codex 经
	// AdapterOpenAIOAuthCodex 表达。
	urlCases := []struct {
		name    string
		view    *View
		path    string
		mode    upstreamenvelope.EndpointMode
		profile string
		adapter upstreamenvelope.Adapter
		want    string
	}{
		{
			name: "openai appends v1 and strips request prefix",
			view: &View{BaseURL: "https://api.example.com"}, path: "/v1/chat/completions",
			mode: upstreamenvelope.ModeChatJSON, want: "https://api.example.com/v1/chat/completions",
		},
		{
			name: "anthropic keeps full path and appends v1 when missing",
			view: &View{BaseURL: "https://claude.example.com", ProtocolCode: "anthropic"}, path: "/v1/messages",
			mode: upstreamenvelope.ModeMessagesJSON, want: "https://claude.example.com/v1/messages",
		},
		{
			name: "gemini preserves base path and alt query",
			view: &View{BaseURL: "https://gem.example.com/v1beta", ProtocolCode: "gemini"}, path: "/v1beta/models/g:streamGenerateContent?alt=sse",
			mode: upstreamenvelope.ModeGenerateContentJSON, want: "https://gem.example.com/v1beta/models/g:streamGenerateContent?alt=sse",
		},
		{
			name: "gemini openai profile passes through to envelope",
			view: &View{BaseURL: "https://gem.example.com/v1beta/openai", ProviderProtocolProfileID: "profile_gemini_openai_chat_v1beta"}, path: "/v1/chat/completions",
			mode: upstreamenvelope.ModeChatJSON, profile: "profile_gemini_openai_chat_v1beta",
			want: "https://gem.example.com/v1beta/openai/chat/completions",
		},
		{
			name: "oauth gpt profile roots at codex fixed upstream",
			view: &View{BaseURL: "https://ignored.example.com", Type: "oauth", ProviderProtocolProfileID: "profile_gpt_openai_v1"}, path: "/responses",
			mode: upstreamenvelope.ModeResponsesJSON, adapter: upstreamenvelope.AdapterOpenAIOAuthCodex,
			want: "https://chatgpt.com/backend-api/codex/responses",
		},
	}
	for _, tc := range urlCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildUpstreamURL(tc.view, tc.path)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("buildUpstreamURL = %q, want %q", got, tc.want)
			}
			envelopeURL, err := upstreamenvelope.UpstreamURL(tc.view.BaseURL, tc.mode, tc.profile, tc.adapter, tc.path)
			if err != nil {
				t.Fatal(err)
			}
			if got != envelopeURL {
				t.Fatalf("buildUpstreamURL %q 与信封 UpstreamURL %q 分叉", got, envelopeURL)
			}
		})
	}

	// 锚点三：accept 标准头矩阵经信封 AcceptHeader——JSON/multipart 一律
	// application/json；SSE oauth 纯流式、api-key 双 accept。
	acceptCases := []struct {
		name string
		view *View
		mode EndpointMode
		want string
	}{
		{name: "json api-key", view: &View{Type: "api_key"}, mode: ModeChatJSON, want: "application/json"},
		{name: "multipart api-key", view: &View{Type: "api_key"}, mode: ModeAudioTranscriptionJSON, want: "application/json"},
		{name: "sse api-key double accept", view: &View{Type: "api_key"}, mode: ModeChatSSE, want: "application/json, text/event-stream"},
		{name: "sse oauth pure stream", view: &View{Type: "oauth"}, mode: ModeChatSSE, want: "text/event-stream"},
		{name: "sse google oauth pure stream", view: &View{Type: "google_oauth"}, mode: ModeGenerateContentSSE, want: "text/event-stream"},
	}
	for _, tc := range acceptCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := upstreamenvelope.AcceptHeader(upstreamenvelope.EndpointMode(tc.mode), upstreamAuthStrategy(tc.view))
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("AcceptHeader = %q, want %q", got, tc.want)
			}
		})
	}

	// 锚点四：账户形态 → 信封认证策略映射与 executeAttempt 认证头分支同源
	// （M4 只消费该策略的 accept 语义；认证头写入保留本包现状）。
	strategyCases := []struct {
		name string
		view *View
		want upstreamenvelope.AuthStrategy
	}{
		{name: "openai api-key bearer", view: &View{Type: "api_key"}, want: upstreamenvelope.AuthBearer},
		{name: "anthropic api-key", view: &View{Type: "api_key", ProtocolCode: "anthropic"}, want: upstreamenvelope.AuthAnthropicAPIKey},
		{name: "glm anthropic profile bearer", view: &View{Type: "api_key", ProtocolCode: "anthropic", ProviderProtocolProfileID: "profile_glm_coding_anthropic_v1"}, want: upstreamenvelope.AuthBearer},
		{name: "gemini api-key", view: &View{Type: "api_key", ProtocolCode: "gemini"}, want: upstreamenvelope.AuthGeminiAPIKey},
		{name: "oauth pure stream family", view: &View{Type: "oauth"}, want: upstreamenvelope.AuthOAuth},
		{name: "google oauth pure stream family", view: &View{Type: "google_oauth"}, want: upstreamenvelope.AuthOAuth},
	}
	for _, tc := range strategyCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := upstreamAuthStrategy(tc.view); got != tc.want {
				t.Fatalf("upstreamAuthStrategy = %q, want %q", got, tc.want)
			}
		})
	}
}

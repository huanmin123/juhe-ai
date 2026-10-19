package accounts

// 账户级协议上下文推导测试（protocol_context.go，#B1 剩余①的语义替代面）：
// profile 行删除后，连接载荷的 registry preset/surface 是协议身份的唯一来源。

import (
	"testing"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts/accountscore"
)

func TestDeriveConnectionProtocolContext(t *testing.T) {
	cases := []struct {
		name        string
		connections []resolvedConnection
		want        protocolContext
	}{
		{
			name:        "gpt 文本连接",
			connections: []resolvedConnection{{preset: "gpt_openai_v1", surface: "openai_v1"}},
			want: protocolContext{
				protocolCode:    accountscore.OpenAIProtocolCode,
				protocolVersion: accountscore.OpenAIProtocolVersion,
				profileID:       accountscore.GptOpenAIV1ProfileID,
			},
		},
		{
			name:        "deepseek anthropic 连接",
			connections: []resolvedConnection{{preset: "deepseek_anthropic_v1", surface: "anthropic_v1"}},
			want: protocolContext{
				protocolCode:    accountscore.AnthropicProtocolCode,
				protocolVersion: accountscore.AnthropicProtocolVersion,
				profileID:       accountscore.DeepSeekAnthropicV1ProfileID,
			},
		},
		{
			name:        "多连接取首个（跨协议面账户）",
			connections: []resolvedConnection{
				{preset: "gemini_native_v1beta", surface: "gemini_v1beta"},
				{preset: "gemini_openai_chat_v1beta", surface: "openai_v1"},
			},
			want: protocolContext{
				protocolCode:    accountscore.GeminiProtocolCode,
				protocolVersion: accountscore.GeminiProtocolVersion,
				profileID:       geminiNativeV1BetaProfileID,
			},
		},
		{
			// 纯媒体预设（surface 空）凭 preset 对应表落 pin，协议对退 openai 族
			// ——丢 pin 会让 legacy 词表路由解析不到媒体档案行、合法媒体域创建
			// 被误拒（minimax 音频 / volcengine 语音域）。
			name:        "minimax 纯媒体连接保留 pin",
			connections: []resolvedConnection{{preset: "minimax_media", surface: ""}},
			want: protocolContext{
				protocolCode:    accountscore.OpenAIProtocolCode,
				protocolVersion: accountscore.OpenAIProtocolVersion,
				profileID:       accountscore.MinimaxOpenAIV1ProfileID,
			},
		},
		{
			name:        "volcengine 纯媒体语音连接保留 pin",
			connections: []resolvedConnection{{preset: "volcengine_openspeech", surface: ""}},
			want: protocolContext{
				protocolCode:    accountscore.OpenAIProtocolCode,
				protocolVersion: accountscore.OpenAIProtocolVersion,
				profileID:       accountscore.VolcengineOpenAIV1ProfileID,
			},
		},
		{
			name:        "generic 预设无 pin",
			connections: []resolvedConnection{{preset: "generic_openai_v1", surface: "openai_v1"}},
			want: protocolContext{
				protocolCode:    accountscore.OpenAIProtocolCode,
				protocolVersion: accountscore.OpenAIProtocolVersion,
			},
		},
		{
			name:        "generic 纯媒体集合退 openai 族无 pin",
			connections: []resolvedConnection{{preset: "generic_openai_v1", surface: ""}},
			want: protocolContext{
				protocolCode:    accountscore.OpenAIProtocolCode,
				protocolVersion: accountscore.OpenAIProtocolVersion,
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := deriveConnectionProtocolContext(testCase.connections)
			if got != testCase.want {
				t.Fatalf("推导 = %+v, want %+v", got, testCase.want)
			}
		})
	}
}

func TestVendorDefaultProtocolContext(t *testing.T) {
	// vendor 语义默认（草稿/探针链，无 connections 载荷）：gemini 默认 native
	// 档案、anthropic 官方无 pin、generic 语义退 openai 族无 pin。
	if got := vendorDefaultProtocolContext("gemini"); got.profileID != geminiNativeV1BetaProfileID ||
		got.protocolCode != accountscore.GeminiProtocolCode {
		t.Fatalf("gemini 默认 = %+v", got)
	}
	if got := vendorDefaultProtocolContext("anthropic"); got.profileID != "" ||
		got.protocolCode != accountscore.AnthropicProtocolCode {
		t.Fatalf("anthropic 默认 = %+v", got)
	}
	if got := vendorDefaultProtocolContext("gpt"); got.profileID != accountscore.GptOpenAIV1ProfileID {
		t.Fatalf("gpt 默认 = %+v", got)
	}
	if got := vendorDefaultProtocolContext("openai"); got.profileID != "" ||
		got.protocolCode != accountscore.OpenAIProtocolCode {
		t.Fatalf("generic 默认 = %+v", got)
	}
}

func TestVendorDefaultBaseURL(t *testing.T) {
	// OAuth 草稿缺省 base_url（§1.1 default_base_url 表）：按 vendor 默认
	// preset 查 registry 注册根——anthropic/xai 得到各自上游根，不再是恒
	// OpenAI 官方根（问题 B 修复）；generic 语义无 vendor preset 返回空串，
	// 由调用方落 legacy OpenAI 官方根。
	cases := []struct {
		providerCode string
		want         string
	}{
		{"anthropic", "https://api.anthropic.com/v1"},
		{"xai", "https://api.x.ai/v1"},
		{"gpt", "https://api.openai.com/v1"},
		{"deepseek", "https://api.deepseek.com"},
		{"gemini", "https://generativelanguage.googleapis.com"},
		{"glm", "https://open.bigmodel.cn/api/paas/v4"},
		{"openai", ""},
		{"hybrid", ""},
		{"nope", ""},
	}
	for _, testCase := range cases {
		if got := vendorDefaultBaseURL(testCase.providerCode); got != testCase.want {
			t.Errorf("vendorDefaultBaseURL(%q) = %q, want %q", testCase.providerCode, got, testCase.want)
		}
	}
}

func TestVendorDefaultPresetPairsWithProtocolContext(t *testing.T) {
	// vendorDefaultPreset 与 vendorDefaultProtocolContext 的 vendor 行必须
	// 同批维护：九真实厂商两侧同时有行（preset 均为 §1.1 注册值），generic
	// 语义两侧同时为空。
	presets := map[string]bool{
		contracts.PresetGPTOpenAIV1: true, contracts.PresetXAIOpenAIV1: true,
		contracts.PresetDeepSeekOpenAIV1: true, contracts.PresetAnthropicV1: true,
		contracts.PresetGeminiNativeV1Beta: true, contracts.PresetGLMGeneralOpenAIV1: true,
		contracts.PresetMiniMaxMedia: true, contracts.PresetVolcengineArk: true,
		contracts.PresetQwenDashScope: true,
	}
	for _, vendor := range []string{"gpt", "xai", "deepseek", "anthropic", "gemini",
		"glm", "minimax", "volcengine", "qwen"} {
		if preset := vendorDefaultPreset(vendor); !presets[preset] {
			t.Errorf("vendor %q 的默认 preset %q 不是 §1.1 注册厂商默认值", vendor, preset)
		}
	}
	for _, generic := range []string{"openai", "hybrid", "nope"} {
		if preset := vendorDefaultPreset(generic); preset != "" {
			t.Errorf("generic 语义 %q 不应有 vendor 默认 preset, got %q", generic, preset)
		}
	}
}

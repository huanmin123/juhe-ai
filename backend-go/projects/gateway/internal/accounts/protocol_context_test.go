package accounts

// 账户级协议上下文推导测试（protocol_context.go，#B1 剩余①的语义替代面）：
// profile 行删除后，连接载荷的 registry preset/surface 是协议身份的唯一来源。

import (
	"testing"

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

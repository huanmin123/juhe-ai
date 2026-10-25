package accounts

// 账户级协议上下文推导测试（protocol_context.go，#B1 剩余①的语义替代面）：
// profile 行删除后，连接载荷的 registry preset/surface 是协议身份的唯一来源。

import (
	"context"
	"strings"
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

func TestDraftProtocolContext(t *testing.T) {
	// 草稿链入参推导（#B4 ⑩）：显式 profileId 按反查表落上下文；无入参或
	// 词表外 ID 回落 vendor 默认（gemini → native）。
	if got := draftProtocolContext("gemini", accountscore.GeminiOpenAIChatV1BetaProfileID); got != (protocolContext{
		protocolCode:    accountscore.OpenAIProtocolCode,
		protocolVersion: accountscore.OpenAIProtocolVersion,
		profileID:       accountscore.GeminiOpenAIChatV1BetaProfileID,
	}) {
		t.Fatalf("openai_chat 入参应落 openai 族上下文：%+v", got)
	}
	if got := draftProtocolContext("gemini", geminiNativeV1BetaProfileID); got.profileID != geminiNativeV1BetaProfileID ||
		got.protocolCode != accountscore.GeminiProtocolCode {
		t.Fatalf("native 入参应落 gemini 上下文：%+v", got)
	}
	if got := draftProtocolContext("deepseek", accountscore.DeepSeekAnthropicV1ProfileID); got.protocolCode != accountscore.AnthropicProtocolCode {
		t.Fatalf("anthropic 档案入参应落 anthropic 上下文：%+v", got)
	}
	// 无入参 = vendor 默认（现状不变）。
	if got, want := draftProtocolContext("gemini", ""), vendorDefaultProtocolContext("gemini"); got != want {
		t.Fatalf("无入参应回落 vendor 默认：%+v != %+v", got, want)
	}
	// 词表外 ID 无协议事实，回落 vendor 默认（不新增 400 路径）。
	if got, want := draftProtocolContext("gemini", "profile-bogus"), vendorDefaultProtocolContext("gemini"); got != want {
		t.Fatalf("词表外入参应回落 vendor 默认：%+v != %+v", got, want)
	}
}

func TestProfileIDProtocolContextPairsWithPresetTable(t *testing.T) {
	// profileIDProtocolContext 与 presetProfileID 同批维护：两表键集互为镜像，
	// 新增/删除 profile pin 必须同批落两侧。
	reverse := map[string]string{}
	for preset, id := range presetProfileID {
		if _, ok := profileIDProtocolContext[id]; !ok {
			t.Errorf("preset %q 的 pin %q 缺 draft 反查行", preset, id)
		}
		reverse[id] = preset
	}
	for id := range profileIDProtocolContext {
		if _, ok := reverse[id]; !ok {
			t.Errorf("draft 反查行 %q 不是 presetProfileID 的任何 pin", id)
		}
	}
	// 关键协议对与 registry surface 语义一致（§1.1）：gemini openai_chat 是
	// openai_v1 surface、native 是 gemini_v1beta、双 anthropic 档案是
	// anthropic_v1。
	pairs := map[string]protocolContext{
		accountscore.GeminiOpenAIChatV1BetaProfileID: {
			protocolCode: accountscore.OpenAIProtocolCode, protocolVersion: accountscore.OpenAIProtocolVersion,
			profileID: accountscore.GeminiOpenAIChatV1BetaProfileID,
		},
		geminiNativeV1BetaProfileID: {
			protocolCode: accountscore.GeminiProtocolCode, protocolVersion: accountscore.GeminiProtocolVersion,
			profileID: geminiNativeV1BetaProfileID,
		},
		accountscore.DeepSeekAnthropicV1ProfileID: {
			protocolCode: accountscore.AnthropicProtocolCode, protocolVersion: accountscore.AnthropicProtocolVersion,
			profileID: accountscore.DeepSeekAnthropicV1ProfileID,
		},
		accountscore.GlmCodingAnthropicV1ProfileID: {
			protocolCode: accountscore.AnthropicProtocolCode, protocolVersion: accountscore.AnthropicProtocolVersion,
			profileID: accountscore.GlmCodingAnthropicV1ProfileID,
		},
		accountscore.GptOpenAIV1ProfileID: {
			protocolCode: accountscore.OpenAIProtocolCode, protocolVersion: accountscore.OpenAIProtocolVersion,
			profileID: accountscore.GptOpenAIV1ProfileID,
		},
	}
	for id, want := range pairs {
		if got := profileIDProtocolContext[id]; got != want {
			t.Errorf("反查行 %q = %+v, want %+v", id, got, want)
		}
	}
}

func TestB4DraftBalanceRespectsInputProfileID(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.exec(t, `INSERT INTO vendors (id, code, name, enabled, created_at, updated_at)
		VALUES ('ven-fixture-gemini', 'gemini', 'Gemini', 1, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')
		ON CONFLICT DO NOTHING`)
	access := AccessScope{ViewerID: adminID}
	groupID := "grp-default-" + adminID

	// 带入参（#B4 ⑩）：gemini openai_chat 存量账户的 openai 族端点模式草稿
	// 不再被 vendor 默认 native 上下文归一进 gemini 词表 → 不再 400。
	row, err := env.store.prepareBalanceDraft(context.Background(), map[string]any{
		"groupId": groupID, "providerCode": "gemini", "type": "api_key",
		"providerProtocolProfileId": accountscore.GeminiOpenAIChatV1BetaProfileID,
		"credentials": map[string]any{
			"api_key": "sk-gemini-chat", "base_url": "https://generativelanguage.googleapis.com/v1beta/openai",
			"supported_endpoint_modes": []any{"chat_json", "chat_sse"},
		},
	}, access)
	if err != nil || row == nil {
		t.Fatalf("openai_chat 入参草稿应通过：%v", err)
	}
	if row.providerProfile == nil ||
		row.providerProfile.id != accountscore.GeminiOpenAIChatV1BetaProfileID ||
		row.providerProfile.protocolCode != accountscore.OpenAIProtocolCode {
		t.Fatalf("草稿档案应按入参落 openai_chat/openai：%+v", row.providerProfile)
	}

	// 无入参 + gemini 族模式：回落 vendor 默认 native，现状通过路径不变。
	row, err = env.store.prepareBalanceDraft(context.Background(), map[string]any{
		"groupId": groupID, "providerCode": "gemini", "type": "api_key",
		"credentials": map[string]any{
			"api_key": "sk-gemini-native", "base_url": "https://generativelanguage.googleapis.com",
			"supported_endpoint_modes": []any{"generate_content_json", "generate_content_sse"},
		},
	}, access)
	if err != nil || row == nil {
		t.Fatalf("无入参 gemini 草稿应通过：%v", err)
	}
	if row.providerProfile == nil || row.providerProfile.id != geminiNativeV1BetaProfileID ||
		row.providerProfile.protocolCode != accountscore.GeminiProtocolCode {
		t.Fatalf("无入参应落 native/gemini：%+v", row.providerProfile)
	}

	// 无入参 + openai 族模式：仍被 gemini 词表拒绝（不带入参行为与现状一致）。
	if _, err := env.store.prepareBalanceDraft(context.Background(), map[string]any{
		"groupId": groupID, "providerCode": "gemini", "type": "api_key",
		"credentials": map[string]any{
			"api_key": "sk-gemini-chat", "base_url": "https://generativelanguage.googleapis.com/v1beta/openai",
			"supported_endpoint_modes": []any{"chat_json", "chat_sse"},
		},
	}, access); err == nil || !strings.Contains(err.Error(), "上游接口能力包含不支持的能力") {
		t.Fatalf("无入参 openai 族模式应保持现状 400：%v", err)
	}

	// 词表外入参 ID：回落 vendor 默认（与无入参同败），不新增 400 之外的路径。
	if _, err := env.store.prepareBalanceDraft(context.Background(), map[string]any{
		"groupId": groupID, "providerCode": "gemini", "type": "api_key",
		"providerProtocolProfileId": "profile-bogus",
		"credentials": map[string]any{
			"api_key": "sk-gemini-chat", "base_url": "https://generativelanguage.googleapis.com/v1beta/openai",
			"supported_endpoint_modes": []any{"chat_json", "chat_sse"},
		},
	}, access); err == nil || !strings.Contains(err.Error(), "上游接口能力包含不支持的能力") {
		t.Fatalf("词表外入参应回落 vendor 默认（同败）：%v", err)
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

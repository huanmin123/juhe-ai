package accountscore

// M2 允许集计算纯函数测试（计划 §服务端统一校验 + §域 × 供应商合法矩阵）：
// AllowedEndpointModes 的显式矩阵门 × 域 lane 集 × 写侧词表叠加（矩阵标
// 「—」的格一律拒绝，2026-10-12 主代理裁决定案）、DefaultEndpointModesForDomain
// 的按域默认集、按域健康检查默认落值。

import (
	"reflect"
	"testing"
)

func capabilityAllowedPredicate(providerCode, protocolCode, protocolVersion, profileID string) ProtocolPredicate {
	return ProtocolPredicate{
		ProviderCode:              providerCode,
		ProtocolCode:              protocolCode,
		ProtocolVersion:           protocolVersion,
		ProviderProtocolProfileID: profileID,
	}
}

func TestAllowedEndpointModesIntersectsWithWriteVocabulary(t *testing.T) {
	gptOpenAI := capabilityAllowedPredicate("gpt", "openai", "v1", GptOpenAIV1ProfileID)
	anthropicV1 := capabilityAllowedPredicate("anthropic", "anthropic", "v1", "profile_anthropic_v1")
	glmOpenAI := capabilityAllowedPredicate("glm", "openai", "v1", GlmGeneralOpenAIV1ProfileID)
	glmCodingAnthropic := capabilityAllowedPredicate("glm", "anthropic", "v1", GlmCodingAnthropicV1ProfileID)
	geminiNative := capabilityAllowedPredicate("gemini", "gemini", "v1beta", "profile_gemini_native_v1beta")
	geminiOpenAIChat := capabilityAllowedPredicate("gemini", "openai", "v1", GeminiOpenAIChatV1BetaProfileID)
	volcengine := capabilityAllowedPredicate("volcengine", "openai", "v1", VolcengineOpenAIV1ProfileID)
	qwen := capabilityAllowedPredicate("qwen", "openai", "v1", QwenOpenAIV1ProfileID)
	minimax := capabilityAllowedPredicate("minimax", "openai", "v1", MinimaxOpenAIV1ProfileID)
	hybrid := capabilityAllowedPredicate("hybrid", "", "", "")
	deepSeekOpenAI := capabilityAllowedPredicate("deepseek", "openai", "v1", DeepSeekOpenAIV1ProfileID)
	xaiOpenAI := capabilityAllowedPredicate("xai", "openai", "v1", XaiOpenAIV1ProfileID)

	cases := []struct {
		name         string
		domain       string
		providerCode string
		accountType  string
		profile      ProtocolPredicate
		want         []string
	}{
		// 矩阵标「—」的格与矩阵外组合一律拒绝（矩阵门 × 词表求交叠加）。
		{name: "anthropic+video 空", domain: "video", providerCode: "anthropic", accountType: "api_key", profile: anthropicV1, want: []string{}},
		{name: "gpt oauth+video 空", domain: "video", providerCode: "gpt", accountType: "oauth", profile: gptOpenAI, want: []string{}},
		{name: "glm+image 空", domain: "image", providerCode: "glm", accountType: "api_key", profile: glmOpenAI, want: []string{}},
		{name: "volcengine+image 空", domain: "image", providerCode: "volcengine", accountType: "api_key", profile: volcengine, want: []string{}},
		{name: "minimax+chat 矩阵拒绝", domain: "chat", providerCode: "minimax", accountType: "api_key", profile: minimax, want: []string{}},
		{name: "gemini native+image 空", domain: "image", providerCode: "gemini", accountType: "api_key", profile: geminiNative, want: []string{}},
		{name: "deepseek openai+video 矩阵拒绝", domain: "video", providerCode: "deepseek", accountType: "api_key", profile: deepSeekOpenAI, want: []string{}},
		{name: "deepseek openai+audio 矩阵拒绝", domain: "audio", providerCode: "deepseek", accountType: "api_key", profile: deepSeekOpenAI, want: []string{}},
		{name: "deepseek openai+image 矩阵拒绝", domain: "image", providerCode: "deepseek", accountType: "api_key", profile: deepSeekOpenAI, want: []string{}},
		{name: "xai+audio 矩阵拒绝", domain: "audio", providerCode: "xai", accountType: "api_key", profile: xaiOpenAI, want: []string{}},
		{name: "xai+image 矩阵拒绝", domain: "image", providerCode: "xai", accountType: "api_key", profile: xaiOpenAI, want: []string{}},
		{name: "gemini openai-chat+video 矩阵拒绝", domain: "video", providerCode: "gemini", accountType: "api_key", profile: geminiOpenAIChat, want: []string{}},
		{name: "gemini openai-chat+audio 矩阵拒绝", domain: "audio", providerCode: "gemini", accountType: "api_key", profile: geminiOpenAIChat, want: []string{}},
		{name: "gemini openai-chat+image 矩阵拒绝", domain: "image", providerCode: "gemini", accountType: "api_key", profile: geminiOpenAIChat, want: []string{}},
		{name: "glm-coding-anthropic+video 矩阵拒绝", domain: "video", providerCode: "glm", accountType: "api_key", profile: glmCodingAnthropic, want: []string{}},
		{name: "glm-coding-anthropic+audio 矩阵拒绝", domain: "audio", providerCode: "glm", accountType: "api_key", profile: glmCodingAnthropic, want: []string{}},
		{name: "未知域空", domain: "bogus", providerCode: "gpt", accountType: "api_key", profile: gptOpenAI, want: []string{}},
		{name: "未注册上下文空", domain: "chat", providerCode: "unknown", accountType: "api_key", profile: capabilityAllowedPredicate("unknown", "openai", "v1", "profile_unknown"), want: []string{}},

		// 词表交集为空的矩阵外组合（矩阵内行 + 类型/词表收窄）。
		{name: "qwen audio 无 audio_speech", domain: "audio", providerCode: "qwen", accountType: "api_key", profile: qwen, want: []string{"audio_job_create", "audio_job_get", "audio_job_content", "audio_job_cancel"}},

		// 矩阵内组合。
		{name: "gpt chat 含对话锚与共享 lane", domain: "chat", providerCode: "gpt", accountType: "api_key", profile: gptOpenAI,
			want: []string{"chat_json", "chat_sse", "responses_json", "responses_sse", "realtime_session", "audio_transcription_json", "images_json"}},
		{name: "gpt oauth chat 只剩 responses 对", domain: "chat", providerCode: "gpt", accountType: "oauth", profile: gptOpenAI,
			want: []string{"responses_json", "responses_sse"}},
		{name: "gpt video 四值组", domain: "video", providerCode: "gpt", accountType: "api_key", profile: gptOpenAI,
			want: []string{"video_create", "video_get", "video_content", "video_cancel"}},
		{name: "anthropic chat", domain: "chat", providerCode: "anthropic", accountType: "api_key", profile: anthropicV1,
			want: []string{"messages_json", "messages_sse", "message_token_counting"}},
		{name: "deepseek openai chat 矩阵内", domain: "chat", providerCode: "deepseek", accountType: "api_key", profile: deepSeekOpenAI,
			want: []string{"chat_json", "chat_sse", "responses_json", "responses_sse", "realtime_session", "audio_transcription_json", "images_json"}},
		{name: "xai video 矩阵内", domain: "video", providerCode: "xai", accountType: "api_key", profile: xaiOpenAI,
			want: []string{"video_create", "video_get", "video_content", "video_cancel"}},
		{name: "xai chat 矩阵内", domain: "chat", providerCode: "xai", accountType: "api_key", profile: xaiOpenAI,
			want: []string{"chat_json", "chat_sse", "responses_json", "responses_sse", "realtime_session", "audio_transcription_json", "images_json"}},
		{name: "gemini openai-chat chat 矩阵内", domain: "chat", providerCode: "gemini", accountType: "api_key", profile: geminiOpenAIChat,
			want: []string{"chat_json", "chat_sse", "responses_json", "responses_sse", "realtime_session", "audio_transcription_json", "images_json"}},
		{name: "glm chat 含 STT 透传", domain: "chat", providerCode: "glm", accountType: "api_key", profile: glmOpenAI,
			want: []string{"chat_json", "chat_sse", "audio_transcription_json"}},
		{name: "glm video", domain: "video", providerCode: "glm", accountType: "api_key", profile: glmOpenAI,
			want: []string{"video_create", "video_get", "video_content", "video_cancel"}},
		{name: "glm audio_speech", domain: "audio", providerCode: "glm", accountType: "api_key", profile: glmOpenAI,
			want: []string{"audio_speech"}},
		{name: "glm-coding-anthropic chat", domain: "chat", providerCode: "glm", accountType: "api_key", profile: glmCodingAnthropic,
			want: []string{"messages_json", "messages_sse"}},
		{name: "gemini native video", domain: "video", providerCode: "gemini", accountType: "api_key", profile: geminiNative,
			want: []string{"video_create", "video_get", "video_content", "video_cancel"}},
		{name: "gemini native audio_speech", domain: "audio", providerCode: "gemini", accountType: "google_oauth", profile: geminiNative,
			want: []string{"audio_speech"}},
		{name: "volcengine chat 对", domain: "chat", providerCode: "volcengine", accountType: "api_key", profile: volcengine,
			want: []string{"chat_json", "chat_sse"}},
		{name: "minimax video 矩阵内", domain: "video", providerCode: "minimax", accountType: "api_key", profile: minimax,
			want: []string{"video_create", "video_get", "video_content", "video_cancel"}},
		{name: "minimax audio 矩阵内", domain: "audio", providerCode: "minimax", accountType: "api_key", profile: minimax,
			want: []string{"audio_speech"}},
		{name: "hybrid 四域非空", domain: "chat", providerCode: "hybrid", accountType: "api_key", profile: hybrid,
			want: nil}, // 非空断言走下方分支
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := AllowedEndpointModes(tc.domain, tc.providerCode, tc.accountType, tc.profile)
			if tc.want == nil {
				if len(got) == 0 {
					t.Fatalf("hybrid chat 允许集应非空：%v", got)
				}
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("AllowedEndpointModes(%s,%s,%s) = %v, want %v", tc.domain, tc.providerCode, tc.accountType, got, tc.want)
			}
		})
	}
}

func TestDefaultEndpointModesForDomain(t *testing.T) {
	volcengine := capabilityAllowedPredicate("volcengine", "openai", "v1", VolcengineOpenAIV1ProfileID)
	qwen := capabilityAllowedPredicate("qwen", "openai", "v1", QwenOpenAIV1ProfileID)
	gptOpenAI := capabilityAllowedPredicate("gpt", "openai", "v1", GptOpenAIV1ProfileID)
	geminiNative := capabilityAllowedPredicate("gemini", "gemini", "v1beta", "profile_gemini_native_v1beta")
	deepSeekOpenAI := capabilityAllowedPredicate("deepseek", "openai", "v1", DeepSeekOpenAIV1ProfileID)

	if got := DefaultEndpointModesForDomain("video", "volcengine", "api_key", volcengine); !reflect.DeepEqual(got, []string{"video_create", "video_get", "video_content", "video_cancel"}) {
		t.Fatalf("volcengine video 默认集应为四值组：%v", got)
	}
	if got := DefaultEndpointModesForDomain("audio", "qwen", "api_key", qwen); !reflect.DeepEqual(got, []string{"audio_job_create", "audio_job_get", "audio_job_content", "audio_job_cancel"}) {
		t.Fatalf("qwen audio 默认集应为长音频四值：%v", got)
	}
	if got := DefaultEndpointModesForDomain("audio", "volcengine", "api_key", volcengine); !reflect.DeepEqual(got, []string{"audio_speech"}) {
		t.Fatalf("volcengine audio 默认集应为 audio_speech：%v", got)
	}
	if got := DefaultEndpointModesForDomain("image", "gpt", "api_key", gptOpenAI); !reflect.DeepEqual(got, []string{"images_json"}) {
		t.Fatalf("gpt image 默认集应为 images_json：%v", got)
	}
	if got := DefaultEndpointModesForDomain("image", "volcengine", "api_key", volcengine); len(got) != 0 {
		t.Fatalf("volcengine image 默认集应为空（矩阵拒绝该域）：%v", got)
	}
	if got := DefaultEndpointModesForDomain("video", "deepseek", "api_key", deepSeekOpenAI); len(got) != 0 {
		t.Fatalf("deepseek video 默认集应为空（矩阵拒绝该域）：%v", got)
	}
	if got := DefaultEndpointModesForDomain("chat", "minimax", "api_key",
		capabilityAllowedPredicate("minimax", "openai", "v1", MinimaxOpenAIV1ProfileID)); len(got) != 0 {
		t.Fatalf("minimax chat 默认集应为空（矩阵拒绝该域）：%v", got)
	}
	if got := DefaultEndpointModesForDomain("video", "gemini", "api_key", geminiNative); len(got) != 4 {
		t.Fatalf("gemini native video 默认集应为四值组：%v", got)
	}
	if got := DefaultEndpointModesForDomain("bogus", "gpt", "api_key", gptOpenAI); len(got) != 0 {
		t.Fatalf("非法域默认集应为空：%v", got)
	}
	// chat 域默认集 = 现行默认（逐项不变，I3；与归一化路径的全量对照在
	// 门面测试 m2_capability_save_rules_test.go 钉住）。
	if got := DefaultEndpointModesForDomain("chat", "gpt", "api_key", gptOpenAI); !reflect.DeepEqual(got, []string{"chat_json", "chat_sse", "responses_json", "responses_sse"}) {
		t.Fatalf("gpt chat 默认集应与现行一致：%v", got)
	}
}

func TestDefaultHealthCheckEndpointModeForDomain(t *testing.T) {
	cases := map[string]string{
		"chat":  "chat_json",
		"image": "images_json",
		"audio": "audio_speech",
		"video": "video_create",
		"bogus": "chat_json",
		"":      "chat_json",
	}
	for domain, want := range cases {
		if got := DefaultHealthCheckEndpointModeForDomain(domain); got != want {
			t.Fatalf("DefaultHealthCheckEndpointModeForDomain(%q) = %q, want %q", domain, got, want)
		}
	}
}

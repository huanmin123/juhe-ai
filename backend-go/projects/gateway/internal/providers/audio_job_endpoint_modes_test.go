package providers

import (
	"slices"
	"testing"
)

// 长音频任务账户（qwen paraformer 等）只声明 audio_job_* 端点；目录模型
// supportedApiProtocols=audio_transcription 必须同时派生同步转写与异步任务
// 创建两个 mode，否则账户健康检查三件套无法落在连接已启用的 mode 上。
func TestAudioTranscriptionIncludesJobCreateMode(t *testing.T) {
	modes, err := ForwardProtocolsToEndpointModes([]string{"audio_transcription"})
	if err != nil {
		t.Fatalf("ForwardProtocolsToEndpointModes: %v", err)
	}
	for _, want := range []string{"audio_transcription_json", "audio_job_create"} {
		if !slices.Contains(modes, want) {
			t.Fatalf("modes=%v missing %s", modes, want)
		}
	}

	mode, protocols := DeriveLegacyModeAndProtocols([]string{"audio_job_create"})
	if mode != "audio" || !slices.Contains(protocols, "audio_transcription") {
		t.Fatalf("derive audio_job_create = (%q, %v), want audio + audio_transcription", mode, protocols)
	}
}

// 计数辅助 mode（anthropic message_token_counting / gemini count_tokens）是
// 对话族的附属接口，不得把含对话生成 mode 的目录行主 mode 抬成 embedding
// （六域拆分后 embedding 是模型广场独立分类，2026-11-21 复查定案）；纯向量行
// 与纯辅助行保持 embedding 派生。协议投影始终全量。
func TestDeriveLegacyModeIgnoresAuxiliaryCountingForPrimaryMode(t *testing.T) {
	cases := []struct {
		name       string
		modes      []string
		wantMode   string
		wantProtos []string
	}{
		{
			name:       "claude 对话行含计数辅助：主 mode 保持 chat，协议全量",
			modes:      []string{"messages_json", "messages_sse", "message_token_counting"},
			wantMode:   "chat",
			wantProtos: []string{"message_token_counting", "messages"},
		},
		{
			name:       "gemini 对话行含 count_tokens：主 mode 保持 chat",
			modes:      []string{"generate_content_json", "generate_content_sse", "count_tokens", "chat_json"},
			wantMode:   "chat",
			wantProtos: []string{"chat_completions", "count_tokens", "generate_content", "stream_generate_content"},
		},
		{
			name:       "纯向量行保持 embedding",
			modes:      []string{"embed_content"},
			wantMode:   "embedding",
			wantProtos: []string{"embed_content"},
		},
		{
			name:       "纯辅助行回落 embedding（旧行为）",
			modes:      []string{"message_token_counting"},
			wantMode:   "embedding",
			wantProtos: []string{"message_token_counting"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mode, protocols := DeriveLegacyModeAndProtocols(tc.modes)
			if mode != tc.wantMode {
				t.Fatalf("mode = %q, want %q", mode, tc.wantMode)
			}
			if !slices.Equal(protocols, tc.wantProtos) {
				t.Fatalf("protocols = %v, want %v", protocols, tc.wantProtos)
			}
		})
	}
}

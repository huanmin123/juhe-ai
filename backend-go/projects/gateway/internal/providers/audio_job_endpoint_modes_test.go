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

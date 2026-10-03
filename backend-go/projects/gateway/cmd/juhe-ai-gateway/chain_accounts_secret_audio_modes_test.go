package main

// M1 同步音频 endpoint mode 投影断言（音频设计 §11.6）：显式
// supported_endpoint_modes 携带 audio_speech / audio_transcription_json 的账户
// 经 chain secret 投影（chainNormalizeGatewayEndpointModesForRuntime）不被滤
// 除——词表缺口会让 /v1/audio 派发无账户可命中；gemini 族仅补 audio_speech
// （无 STT 直连形态）；默认回落集不因此扩容（opt-in 语义与 images_json 同）。
import (
	"sort"
	"testing"
)

func sortedStringCopyOf(values []string) []string {
	out := append([]string{}, values...)
	sort.Strings(out)
	return out
}

func TestChainSecretProjectionKeepsAudioEndpointModes(t *testing.T) {
	retain := []any{"chat_json", "audio_speech", "audio_transcription_json"}

	// openai 族（gpt + openai/v1 档案）：两 audio 模式全部保留。
	openAI := chainNormalizeGatewayEndpointModesForRuntime(retain, "gpt", "api_key", "openai_standard", "", "openai", "v1")
	want := []string{"audio_speech", "audio_transcription_json", "chat_json"}
	if got := sortedStringCopyOf(openAI); !equalStringSlices(got, want) {
		t.Fatalf("openai 投影 = %v, want %v", got, want)
	}

	// gemini 族（gemini/v1beta 档案）：audio_speech 保留；
	// audio_transcription_json（gemini 无 STT 直连形态）与 openai 族
	// chat_json（非 gemini 模式）滤除。
	gemini := chainNormalizeGatewayEndpointModesForRuntime(retain, "gemini", "api_key", "openai_standard", "", "gemini", "v1beta")
	want = []string{"audio_speech"}
	if got := sortedStringCopyOf(gemini); !equalStringSlices(got, want) {
		t.Fatalf("gemini 投影 = %v, want %v", got, want)
	}

	// hybrid 供应商（三族并集）：两 audio 模式保留。
	hybrid := chainNormalizeGatewayEndpointModesForRuntime(retain, "hybrid", "api_key", "openai_standard", "", "openai", "v1")
	want = []string{"audio_speech", "audio_transcription_json", "chat_json"}
	if got := sortedStringCopyOf(hybrid); !equalStringSlices(got, want) {
		t.Fatalf("hybrid 投影 = %v, want %v", got, want)
	}

	// anthropic 档案不吸收 openai 族 audio 模式（族间不串味）。
	anthropic := chainNormalizeGatewayEndpointModesForRuntime(retain, "anthropic", "api_key", "openai_standard", "", "anthropic", "v1")
	for _, mode := range anthropic {
		if mode == "audio_speech" || mode == "audio_transcription_json" {
			t.Fatalf("anthropic 投影不得保留 openai 族 audio 模式: %v", anthropic)
		}
	}
}

// TestChainSecretProjectionAudioModesNotDefaults：默认回落集保持四模式
// chat/responses（audio 与 images 同为 opt-in，不因词表扩容泄漏进默认集）。
func TestChainSecretProjectionAudioModesNotDefaults(t *testing.T) {
	defaults := chainNormalizeGatewayEndpointModesForRuntime(nil, "gpt", "api_key", "openai_standard", "", "openai", "v1")
	want := []string{"chat_json", "chat_sse", "responses_json", "responses_sse"}
	if got := sortedStringCopyOf(defaults); !equalStringSlices(got, want) {
		t.Fatalf("openai 默认回落集 = %v, want %v", got, want)
	}

	geminiDefaults := chainNormalizeGatewayEndpointModesForRuntime([]any{"generate_content_json"}, "gemini", "api_key", "openai_standard", "", "gemini", "v1beta")
	want = []string{"generate_content_json"}
	if got := sortedStringCopyOf(geminiDefaults); !equalStringSlices(got, want) {
		t.Fatalf("gemini 显式集不含 audio 时不新增 audio 模式: %v, want %v", got, want)
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}

// speechmeter.go 承载 M1 同步音频的网关侧计量来源（契约 §2.8 计量来源表，
// 不猜测）：
//   - openai TTS：上游无 usage 回报 → 网关按请求 input 字符数（UTF-8 rune
//     数）自算 TtsInputChars；token 口径模型（gpt-4o-mini-tts / gpt-4o-tts
//     系）无 token 事实时置 usage_missing；
//   - openai STT：响应 usage.input_tokens/output_tokens 优先（沿既有 token
//     字段，由 openai usage 抽取链承载），否则 verbose_json 顶层 duration
//     落 AudioInputSeconds；两者都缺 → 计量 0 + usage_missing 标记；
//   - gemini TTS：usageMetadata token 计量沿既有 gemini usage 抽取链，本
//     模块不涉及。
package gatewaymedia

import (
	"strings"
)

// IsSpeechPath 判定请求路径是否 TTS speech 形态（/v1/audio/speech，含 /v1
// 前缀变体）。
func IsSpeechPath(path string) bool {
	normalized := normalizeAudioPath(path)
	return normalized == "/audio/speech"
}

// IsTranscriptionPath 判定请求路径是否 STT 形态（transcriptions/translations）。
func IsTranscriptionPath(path string) bool {
	normalized := normalizeAudioPath(path)
	return normalized == "/audio/transcriptions" || normalized == "/audio/translations"
}

// normalizeAudioPath 去查询串、小写化并剥 /v1 前缀段。
func normalizeAudioPath(path string) string {
	if index := strings.IndexByte(path, '?'); index >= 0 {
		path = path[:index]
	}
	path = strings.ToLower(strings.TrimSpace(path))
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if strings.HasPrefix(path, "/v1/") {
		path = path[len("/v1"):]
	}
	return path
}

// SpeechInputCharsFromBody 从 speech 请求 JSON 对象提取 input 字符数
// （nil body 或 input 非字符串返回 0——调用方只对成功响应计量，缺失即 0，
// 不猜测）。
func SpeechInputCharsFromBody(body map[string]any) int64 {
	if body == nil {
		return 0
	}
	input, _ := body["input"].(string)
	return int64(len([]rune(input)))
}

// IsTokenMeteredSpeechModel 判定 TTS 模型是否 token 口径计价（口径文档
// §2.5 / 契约 §2.8：gpt-4o-mini-tts、gpt-4o-tts 系复用 audio token 单价；
// 上游二进制响应无 usage 回报，网关亦无 token 自算维度 → 计量 0 +
// usage_missing 标记）。gpt-4o-transcribe 系（STT）不命中本词表。
func IsTokenMeteredSpeechModel(model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(normalized, "gpt-4o-mini-tts") ||
		strings.HasPrefix(normalized, "gpt-4o-tts")
}

// SttDurationSeconds 从 STT 响应 JSON 根对象提取 verbose_json 的顶层
// duration 秒数。ok=false 表示无 duration 事实。
func SttDurationSeconds(root map[string]any) (float64, bool) {
	if root == nil {
		return 0, false
	}
	duration, ok := root["duration"].(float64)
	if !ok || duration < 0 {
		return 0, false
	}
	return duration, true
}

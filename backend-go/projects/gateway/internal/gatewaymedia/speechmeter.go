// speechmeter.go 承载同步音频的网关侧计量来源（契约 §2.8 计量来源表，
// 不猜测）：
//   - TTS：上游回报时长优先（volcengine 顶层 duration 秒 / minimax
//     extra_info.audio_length 毫秒），否则由响应管道解析产物音频，最后按
//     请求 input 字符数 ÷ charsPerSecond 估算（统一媒体计费设计 §7.2）；
//     legacy 行仍按字符/token 口径运行；
//   - openai STT：响应 usage.input_tokens/output_tokens 优先（沿既有 token
//     字段，由 openai usage 抽取链承载），否则 verbose_json 顶层 duration
//     或请求上传音频解析落 AudioInputSeconds；全部缺失 → 计量 0 +
//     usage_missing 标记；
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

// AudioEndpointPath 返回归一后的同步音频端点路径（"/audio/speech" |
// "/audio/transcriptions" | "/audio/translations"），非同步音频端点返回
// ok=false。M6 glm 透传分支消费（契约 §7.2）：把统一面路径改写为 glm
// 通用根 /api/paas/v4 前缀形态（与 IsSpeechPath/IsTranscriptionPath 同一
// 归一口径）。
func AudioEndpointPath(pathAndQuery string) (string, bool) {
	normalized := normalizeAudioPath(pathAndQuery)
	switch normalized {
	case "/audio/speech", "/audio/transcriptions", "/audio/translations":
		return normalized, true
	}
	return "", false
}

// normalizeAudioPath 去查询串、剥离可选的 HTTP 方法前缀、小写化并剥 /v1
// 前缀段。usage 上下文的 Endpoint 是审计格式 "METHOD path"（如
// "POST /v1/audio/speech"），而请求路径调用方传纯路径；两种形态都必须
// 归一为同一分类输入，否则 chain 侧 legacy 秒计判定会把上游时长误清空。
func normalizeAudioPath(path string) string {
	if index := strings.IndexByte(path, '?'); index >= 0 {
		path = path[:index]
	}
	path = strings.ToLower(strings.TrimSpace(path))
	if fields := strings.Fields(path); len(fields) == 2 && isHTTPMethod(fields[0]) &&
		strings.HasPrefix(fields[1], "/") {
		path = fields[1]
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if strings.HasPrefix(path, "/v1/") {
		path = path[len("/v1"):]
	}
	return path
}

// isHTTPMethod 判定首段是否为 HTTP 方法（只用于剥离审计 endpoint 前缀，
// 不做请求方法校验）。
func isHTTPMethod(value string) bool {
	switch value {
	case "get", "post", "put", "patch", "delete", "head", "options", "trace", "connect":
		return true
	default:
		return false
	}
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

// DefaultTTSCharsPerSecond 是 TTS 无音频体/无法解析时的字符→秒兜底常量
// （非对话媒体统一计费设计 §7.2/§14.4 #9：默认 4 字符/秒，按模型族可后续
// 由 adapter 覆盖；不按语言/音色细分）。
const DefaultTTSCharsPerSecond = 4.0

// TTSCharsFallbackSeconds 按字符数估算 TTS 秒数；chars<=0 返回 0（调用方
// 置 usage_missing，不猜测）。
func TTSCharsFallbackSeconds(chars int64) float64 {
	if chars <= 0 {
		return 0
	}
	return float64(chars) / DefaultTTSCharsPerSecond
}

// SpeechReportedSecondsFromJSON 汇总同步 TTS 的上游时长回报：
//   - volcengine 1.0 顶层 duration（秒，官方计费原始时长）；
//   - minimax extra_info.audio_length（毫秒）。
//
// 两者都缺失返回 ok=false；非对应 provider 形态的根对象不会误命中。
func SpeechReportedSecondsFromJSON(root any) (float64, bool) {
	if seconds, ok := SpeechReportedDurationSecondsFromJSON(root); ok {
		return seconds, true
	}
	if milliseconds, ok := minimaxReportedAudioLengthMillis(root); ok {
		return milliseconds / 1000, true
	}
	return 0, false
}

// minimaxReportedAudioLengthMillis 提取 minimax t2a_v2 extra_info.audio_length
// （官方字段单位为毫秒；mock 3056 对应 3.056 秒）。缺失/非正值返回 false。
func minimaxReportedAudioLengthMillis(root any) (float64, bool) {
	object, ok := root.(map[string]any)
	if !ok {
		return 0, false
	}
	extra, ok := object["extra_info"].(map[string]any)
	if !ok {
		return 0, false
	}
	length, ok := extra["audio_length"].(float64)
	if !ok || length <= 0 {
		return 0, false
	}
	return length, true
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

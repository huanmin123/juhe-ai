package accountscore

// Endpoint-mode predicate layer (REFACTOR-0005 阶段 0 下沉): the port of
// backend/src/domain/provider-protocol.ts plus the openai/anthropic/gemini
// endpoint-mode families (openai-endpoint-modes.ts, anthropic-endpoint-modes.ts,
// gemini-endpoint-modes.ts). Only the shared predicate family, the mode value
// tables and the runtime default resolution sink here; the write-side
// normalization, the credential driver registry and the compatibility asserts
// stay in the accounts facade (they consume these symbols through the facade
// wrappers).

const (
	// GptVendorCode mirrors the gpt vendor token (provider-protocol.ts:4,
	// shared with the import-source slice).
	GptVendorCode = "gpt"
	// XaiProviderCode / DeepSeekProviderCode / GlmProviderCode /
	// GeminiProviderCode / AnthropicProviderCode mirror the vendor tokens.
	// MinimaxProviderCode / VolcengineProviderCode / QwenProviderCode 是 M3
	// 媒体新增供应商 token（媒体设计 §9/契约 §8/§9/§10，仅媒体能力——不参与
	// DefaultOpenAIEndpointModes 的 chat 默认分支）。
	XaiProviderCode        = "xai"
	DeepSeekProviderCode   = "deepseek"
	GlmProviderCode        = "glm"
	GeminiProviderCode     = "gemini"
	AnthropicProviderCode  = "anthropic"
	MinimaxProviderCode    = "minimax"
	VolcengineProviderCode = "volcengine"
	QwenProviderCode       = "qwen"
	// HybridProviderCode mirrors the hybrid pseudo provider token.
	HybridProviderCode = "hybrid"
	// OpenAICompatibleProviderCode mirrors openai (openai-compatible token).
	OpenAICompatibleProviderCode = "openai"
	// Protocol code/version pairs mirror provider-protocol.ts:1-2 and the
	// anthropic/gemini protocol constants.
	OpenAIProtocolCode       = "openai"
	OpenAIProtocolVersion    = "v1"
	AnthropicProtocolCode    = "anthropic"
	AnthropicProtocolVersion = "v1"
	GeminiProtocolCode       = "gemini"
	GeminiProtocolVersion    = "v1beta"
	// Profile IDs the runtime default resolution pins against.
	DeepSeekAnthropicV1ProfileID  = "profile_deepseek_anthropic_v1"
	GlmCodingAnthropicV1ProfileID = "profile_glm_coding_anthropic_v1"
)

// Endpoint-mode value tables mirror the *-endpoint-modes.ts families.
// OpenAIEndpointModeValues carries images_json first (order mirrors
// accountHealthCheckEndpointModeOrder): the Images API is an expressible
// openai-family capability, so explicit supported_endpoint_modes containing
// images_json survive the dispatch candidate filter instead of every new
// account being rejected by the images lane (endpoint_mode_unsupported).
var (
	// M1 同步音频（音频设计 §11.6）：audio_speech / audio_transcription_json
	// 是可表达、opt-in 的 openai 族能力（与 images_json 同语义：进词表、
	// 不进默认集，显式 supported_endpoint_modes 含它的账户在 audio 车道
	// 存活）。
	// M2 同步视频（媒体设计 §11.6）：video_create / video_get /
	// video_content / video_cancel 四 token 同语义追加（进词表、opt-in、
	// 不进默认集；与 gatewaypreauth 词表、链路投影词表
	// chainOpenAIEndpointModeValues 及 accounts.health_check_endpoint_mode
	// CHECK 一致。词表缺失会让管理面无法创建视频账户，/v1/videos 创建链
	// 按 video_create 候选过滤无账户可命中）。
	// M3f 长音频任务族（媒体设计 §4.2，契约 §10.2）：audio_job_create /
	// audio_job_get / audio_job_content / audio_job_cancel 四 token 同语义
	// 追加（audio_job_create 是 /v1/audio/jobs 创建链候选过滤消费面；词表
	// 缺失会让管理面无法勾选长转写能力，创建链无账户可命中）。
	OpenAIEndpointModeValues     = []string{"images_json", "chat_json", "chat_sse", "responses_json", "responses_sse", "audio_speech", "audio_transcription_json", "video_create", "video_get", "video_content", "video_cancel", "audio_job_create", "audio_job_get", "audio_job_content", "audio_job_cancel"}
	OpenAIChatEndpointModes      = []string{"chat_json", "chat_sse"}
	OpenAIResponsesEndpointModes = []string{"responses_json", "responses_sse"}
	// OpenAIDefaultEndpointModes is the write-side default set for new openai
	// family accounts: the chat and responses pairs only. images_json stays
	// opt-in and never enters the defaults (DefaultOpenAIEndpointModes pins
	// this list so expanding OpenAIEndpointModeValues does not leak into
	// account defaults).
	OpenAIDefaultEndpointModes  = []string{"chat_json", "chat_sse", "responses_json", "responses_sse"}
	AnthropicEndpointModeValues = []string{"messages_json", "messages_sse", "message_token_counting"}
	// gemini 族 M1 补 audio_speech（gemini TTS adapter 承载；gemini 无
	// /v1/audio/transcriptions 直连形态）；M3 视频补 video_create /
	// video_get / video_content / video_cancel（veo adapter 承载
	// predictLongRunning 形态，契约 §5.2；与 openai 族同 token——跨协议
	// 共享，opt-in、不进默认集 DefaultGeminiEndpointModes）。
	GeminiEndpointModeValues = []string{
		"generate_content_json", "generate_content_sse", "count_tokens",
		"embed_content", "interactions_json", "interactions_sse", "audio_speech",
		"video_create", "video_get", "video_content", "video_cancel",
	}
	HybridEndpointModeValues = append(append(append([]string{}, OpenAIEndpointModeValues...), AnthropicEndpointModeValues...), GeminiEndpointModeValues...)
)

var (
	openAIEndpointModeSet     = StringSet(OpenAIEndpointModeValues)
	anthropicEndpointModeSet  = StringSet(AnthropicEndpointModeValues)
	geminiEndpointModeSet     = StringSet(GeminiEndpointModeValues)
	hybridEndpointModeSet     = StringSet(HybridEndpointModeValues)
	openAIChatEndpointModeSet = StringSet(OpenAIChatEndpointModes)
)

// IsOpenAIEndpointMode mirrors isOpenAIEndpointMode.
func IsOpenAIEndpointMode(value string) bool { return openAIEndpointModeSet[value] }

// IsAnthropicEndpointMode mirrors isAnthropicEndpointMode.
func IsAnthropicEndpointMode(value string) bool { return anthropicEndpointModeSet[value] }

// IsGeminiEndpointMode mirrors isGeminiEndpointMode.
func IsGeminiEndpointMode(value string) bool { return geminiEndpointModeSet[value] }

// IsHybridEndpointMode mirrors isHybridEndpointMode.
func IsHybridEndpointMode(value string) bool { return hybridEndpointModeSet[value] }

// ---- provider-protocol.ts predicates ----

// IsGptVendorCodeToken mirrors isGptVendorCodeToken.
func IsGptVendorCodeToken(value string) bool {
	return NormalizeProviderToken(value) == GptVendorCode
}

// IsXaiProviderCodeToken mirrors isXaiProviderCodeToken.
func IsXaiProviderCodeToken(value string) bool {
	return NormalizeProviderToken(value) == XaiProviderCode
}

// IsDeepSeekProviderCodeToken mirrors isDeepSeekProviderCodeToken.
func IsDeepSeekProviderCodeToken(value string) bool {
	return NormalizeProviderToken(value) == DeepSeekProviderCode
}

// IsGlmProviderCodeToken mirrors isGlmProviderCodeToken.
func IsGlmProviderCodeToken(value string) bool {
	return NormalizeProviderToken(value) == GlmProviderCode
}

// IsMinimaxProviderCodeToken mirrors isMinimaxProviderCodeToken.
func IsMinimaxProviderCodeToken(value string) bool {
	return NormalizeProviderToken(value) == MinimaxProviderCode
}

// IsVolcengineProviderCodeToken mirrors isVolcengineProviderCodeToken.
func IsVolcengineProviderCodeToken(value string) bool {
	return NormalizeProviderToken(value) == VolcengineProviderCode
}

// IsQwenProviderCodeToken mirrors isQwenProviderCodeToken.
func IsQwenProviderCodeToken(value string) bool {
	return NormalizeProviderToken(value) == QwenProviderCode
}

// IsGeminiProviderCodeToken mirrors isGeminiProviderCodeToken.
func IsGeminiProviderCodeToken(value string) bool {
	return NormalizeProviderToken(value) == GeminiProviderCode
}

// IsHybridProviderCodeToken mirrors isHybridProviderCodeToken.
func IsHybridProviderCodeToken(value string) bool {
	return NormalizeProviderToken(value) == HybridProviderCode
}

// ProtocolPredicate mirrors the ProviderProtocolDefinition/
// ProviderProtocolProfileDefinition shapes the predicates consume.
type ProtocolPredicate struct {
	ProviderCode              string
	ProtocolCode              string
	ProtocolVersion           string
	ProviderProtocolProfileID string
}

// IsOpenAIProtocolProfileOf mirrors isOpenAIProtocolProfileOf.
func IsOpenAIProtocolProfileOf(input ProtocolPredicate) bool {
	return NormalizeProviderToken(input.ProtocolCode) == OpenAIProtocolCode &&
		NormalizeProviderToken(input.ProtocolVersion) == OpenAIProtocolVersion
}

// IsAnthropicProtocolProfileOf mirrors isAnthropicProtocolProfileOf.
func IsAnthropicProtocolProfileOf(input ProtocolPredicate) bool {
	return NormalizeProviderToken(input.ProtocolCode) == AnthropicProtocolCode &&
		NormalizeProviderToken(input.ProtocolVersion) == AnthropicProtocolVersion
}

// IsGeminiProtocolProfileOf mirrors isGeminiProtocolProfileOf.
func IsGeminiProtocolProfileOf(input ProtocolPredicate) bool {
	return NormalizeProviderToken(input.ProtocolCode) == GeminiProtocolCode &&
		NormalizeProviderToken(input.ProtocolVersion) == GeminiProtocolVersion
}

// ModeDefaultContext mirrors OpenAIEndpointModeDefaultContext /
// AnthropicEndpointModeDefaultContext / GeminiEndpointModeDefaultContext (the
// shared field set; unused fields stay empty per family).
type ModeDefaultContext struct {
	ProviderCode              string
	AccountType               string
	ProtocolCode              string
	ProtocolVersion           string
	ProviderProtocolProfileID string
	ClientCompatibility       string
}

// DefaultOpenAIEndpointModes mirrors defaultOpenAIEndpointModes. The api_key
// defaults stay the four chat/responses modes (OpenAIDefaultEndpointModes) —
// images_json is expressible but opt-in, never a default.
func DefaultOpenAIEndpointModes(input ModeDefaultContext) []string {
	if input.AccountType == "oauth" {
		return append([]string{}, OpenAIResponsesEndpointModes...)
	}
	providerCode := NormalizeProviderToken(input.ProviderCode)
	switch providerCode {
	case GptVendorCode, DeepSeekProviderCode:
		return append([]string{}, OpenAIDefaultEndpointModes...)
	case OpenAICompatibleProviderCode, GlmProviderCode, GeminiProviderCode, HybridProviderCode:
		return append([]string{}, OpenAIChatEndpointModes...)
	}
	return append([]string{}, OpenAIDefaultEndpointModes...)
}

// DefaultAnthropicEndpointModes mirrors defaultAnthropicEndpointModes.
func DefaultAnthropicEndpointModes(input ModeDefaultContext) []string {
	if input.ProviderProtocolProfileID == DeepSeekAnthropicV1ProfileID ||
		input.ProviderProtocolProfileID == GlmCodingAnthropicV1ProfileID {
		return []string{"messages_json", "messages_sse"}
	}
	return append([]string{}, AnthropicEndpointModeValues...)
}

// DefaultGeminiEndpointModes mirrors defaultGeminiEndpointModes.
func DefaultGeminiEndpointModes(ModeDefaultContext) []string {
	return []string{"generate_content_json", "generate_content_sse", "count_tokens",
		"interactions_json", "interactions_sse"}
}

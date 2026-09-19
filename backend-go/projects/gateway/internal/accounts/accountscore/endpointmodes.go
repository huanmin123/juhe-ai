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
	XaiProviderCode       = "xai"
	DeepSeekProviderCode  = "deepseek"
	GlmProviderCode       = "glm"
	GeminiProviderCode    = "gemini"
	AnthropicProviderCode = "anthropic"
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
var (
	OpenAIEndpointModeValues     = []string{"chat_json", "chat_sse", "responses_json", "responses_sse"}
	OpenAIChatEndpointModes      = []string{"chat_json", "chat_sse"}
	OpenAIResponsesEndpointModes = []string{"responses_json", "responses_sse"}
	AnthropicEndpointModeValues  = []string{"messages_json", "messages_sse", "message_token_counting"}
	GeminiEndpointModeValues     = []string{
		"generate_content_json", "generate_content_sse", "count_tokens",
		"embed_content", "interactions_json", "interactions_sse",
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

// DefaultOpenAIEndpointModes mirrors defaultOpenAIEndpointModes.
func DefaultOpenAIEndpointModes(input ModeDefaultContext) []string {
	if input.AccountType == "oauth" {
		return append([]string{}, OpenAIResponsesEndpointModes...)
	}
	providerCode := NormalizeProviderToken(input.ProviderCode)
	switch providerCode {
	case GptVendorCode, DeepSeekProviderCode:
		return append([]string{}, OpenAIEndpointModeValues...)
	case OpenAICompatibleProviderCode, GlmProviderCode, GeminiProviderCode, HybridProviderCode:
		return append([]string{}, OpenAIChatEndpointModes...)
	}
	return append([]string{}, OpenAIEndpointModeValues...)
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

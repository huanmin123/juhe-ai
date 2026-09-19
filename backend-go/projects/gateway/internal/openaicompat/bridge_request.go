package openaicompat

import (
	"regexp"
	"strings"
)

// B-4 bridge request builders (D-149/D-157). Faithful ports of the archived
// Node conversion cores:
//
//	openai-anthropic-bridge.ts        buildOpenAIToAnthropicBridgeBody
//	                                  (chatBodyToAnthropicMessages +
//	                                  responsesBodyToAnthropicMessages +
//	                                  baseAnthropicBody + tools/thinking)
//	gemini-openai-chat-bridge.ts      buildGeminiGenerateContentChatBridgeBody
//	anthropic-openai-chat-bridge.ts   buildAnthropicMessagesChatBridgeBody
//	codex-responses-chat-bridge.ts    buildCodexResponsesChatBridgeBody
//	gemini-anthropic-messages path    (chat -> gemini generateContent builder
//	                                  kept on the openai-anthropic-gemini-native
//	                                  surface: BuildOpenAIChatToGeminiBody)
//
// Local-runtime hosted tool emulation (code interpreter / computer use /
// image generation executors) stays behind the existing executor ports and is
// not re-implemented here; unsupported hosted tools surface the same guidance
// error codes as Node.

// BridgeRequestBodyOptions carries the per-request bridge options shared by
// the Node BuildXBridgeBody option bags.
type BridgeRequestBodyOptions struct {
	DefaultModel         string
	GuidanceProviderName string
	ModelOverride        string
	TargetPathAndQuery   string
	ClientCompatibility  map[string]any
	MaxTokens            *int64
	Temperature          *float64
	// Stream mirrors requestStream(req): the effective client stream flag.
	Stream bool
	// DefaultMaxTokens mirrors options.defaultMaxTokens (OpenAI -> Anthropic).
	DefaultMaxTokens *int64
	// FileResolver resolves OpenAI file references for document blocks
	// (openAIToAnthropicBridgeFileResolverForTest ?? options.fileResolver).
	FileResolver FileResolver
	// HostedToolModes carries the hosted tool runtime mode bag (Node
	// runtimeConfig.hostedToolRuntimes); zero value reads as guidance for
	// every type (Node default).
	HostedToolModes OpenAIHostedToolRuntimeModes

	// geminiNativeSourceFamily / geminiNativeSourceModel carry the guidance
	// render context for the gemini-native-target builders (Node
	// guidance(req, mapping) reads downstreamProtocol(mapping) and
	// mapping.sourceModel). The package dispatch and the builder entries set
	// them; direct callers leave the zero values.
	geminiNativeSourceFamily string
	geminiNativeSourceModel  string
}

// bridgeValidationError mirrors bridgeValidationError: 400 invalid_request_error.
func bridgeValidationError(message, code string) *BridgeRequestError {
	return bridgeError(message, code, 400, "invalid_request_error")
}

// providerLabel mirrors providerLabel(providerName).
func providerLabel(name string) string {
	if name == "" {
		return ""
	}
	return " " + name
}

const (
	defaultAnthropicMaxTokens = int64(4096)
	// structuredOutputSyntheticToolName mirrors the same Node constant.
	structuredOutputSyntheticToolName = "emit_structured_output"
)

var supportedAnthropicBridgeReasoningEfforts = map[string]bool{
	"none": true, "minimal": true, "low": true, "medium": true,
	"high": true, "xhigh": true, "max": true,
}

var supportedAnthropicBridgeReasoningSummaries = map[string]bool{
	"auto": true, "concise": true, "detailed": true, "none": true,
}

func appendSystemText(parts *[]string, value string) {
	if text := strings.TrimSpace(value); text != "" {
		*parts = append(*parts, text)
	}
}

// appendAnthropicMessage mirrors appendAnthropicMessage: same-role messages
// merge their content blocks.
func appendAnthropicMessage(messages []any, next map[string]any) []any {
	if len(messages) > 0 {
		if previous := bridgeObjectValue(messages[len(messages)-1]); previous != nil &&
			bridgeStringValue(previous["role"]) == bridgeStringValue(next["role"]) {
			previousBlocks, _ := bridgeIsArray(previous["content"])
			nextBlocks, _ := bridgeIsArray(next["content"])
			previous["content"] = append(previousBlocks, nextBlocks...)
			return messages
		}
	}
	return append(messages, next)
}

var bridgeWhitespacePattern = regexp.MustCompile(`\s+`)

func normalizeBridgeWhitespace(value string) string {
	return strings.TrimSpace(bridgeWhitespacePattern.ReplaceAllString(value, " "))
}

// stopSequencesValue mirrors stopSequencesValue: string or string[] -> []string.
func stopSequencesValue(value any) []string {
	switch typed := value.(type) {
	case string:
		if typed == "" {
			return nil
		}
		return []string{typed}
	case []any:
		out := []string{}
		for _, item := range typed {
			if text, ok := item.(string); ok && text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func hasOwnKey(object map[string]any, key string) bool {
	if object == nil {
		return false
	}
	_, ok := object[key]
	return ok
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func int64ToText(value int64) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	digits := []byte{}
	for value > 0 {
		digits = append(digits, byte('0'+value%10))
		value /= 10
	}
	if negative {
		digits = append(digits, '-')
	}
	for left, right := 0, len(digits)-1; left < right; left, right = left+1, right-1 {
		digits[left], digits[right] = digits[right], digits[left]
	}
	return string(digits)
}

// ---------------------------------------------------------------------------
// Legacy entry points (kept for the existing driver call surface)
// ---------------------------------------------------------------------------

// BuildOpenAIChatBridgeBody builds the upstream bridge body for an OpenAI chat
// completions client request. The upstream family selects the builder:
// anthropic messages / gemini generateContent bodies from the archived Node
// bridges. The legacy gemini-only flattening of the previous Go revision was
// wrong for both targets and is superseded here.
func BuildOpenAIChatBridgeBody(clientBody map[string]any, options BridgeRequestBodyOptions) (map[string]any, error) {
	return BuildOpenAIChatToAnthropicMessagesBody(clientBody, options)
}

// BuildAnthropicMessagesBody keeps its historical name: an Anthropic messages
// body from an OpenAI chat completions request.
func BuildAnthropicMessagesBody(clientBody map[string]any, options BridgeRequestBodyOptions) (map[string]any, error) {
	return BuildOpenAIChatToAnthropicMessagesBody(clientBody, options)
}

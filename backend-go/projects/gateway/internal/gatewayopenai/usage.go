package gatewayopenai

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// ParseUsageFromJSONBuffer mirrors parseOpenAIUsageFromJsonBuffer: try the
// full document first, fall back to the text-fragment scan.
func ParseUsageFromJSONBuffer(body []byte) gatewayproto.ParsedUsage {
	if len(body) == 0 {
		return gatewayproto.EmptyUsage()
	}
	text := string(body)
	var root map[string]any
	if err := json.Unmarshal(body, &root); err == nil && root != nil {
		return ExtractUsage(root["usage"], normalizeServiceTier(root["service_tier"]))
	}
	return ParseUsageFromJSONTextFragment(text)
}

// ParseUsageFromJSONValue mirrors parseOpenAIUsageFromJsonValue.
func ParseUsageFromJSONValue(value any) gatewayproto.ParsedUsage {
	root, ok := value.(map[string]any)
	if !ok {
		return gatewayproto.EmptyUsage()
	}
	return ExtractUsage(root["usage"], normalizeServiceTier(root["service_tier"]))
}

// ParseUsageFromJSONTextFragment mirrors parseOpenAIUsageFromJsonTextFragment:
// scan (from the end) for the last balanced "usage" object plus the
// "service_tier" string.
func ParseUsageFromJSONTextFragment(text string) gatewayproto.ParsedUsage {
	if text == "" {
		return gatewayproto.EmptyUsage()
	}
	usageText, ok := extractJSONObjectPropertyFromTextFragment(text, "usage")
	serviceTier := normalizeServiceTier(extractJSONStringPropertyFromTextFragment(text, "service_tier"))
	if !ok {
		if serviceTier != "" {
			return gatewayproto.ParsedUsage{ServiceTier: serviceTier}
		}
		return gatewayproto.EmptyUsage()
	}
	var usageValue any
	if err := json.Unmarshal([]byte(usageText), &usageValue); err != nil {
		return gatewayproto.EmptyUsage()
	}
	return ExtractUsage(usageValue, serviceTier)
}

// ExtractUsage mirrors extractOpenAIUsage: the full OpenAI / Responses /
// Claude-cache fallback chains.
func ExtractUsage(value any, serviceTier string) gatewayproto.ParsedUsage {
	usage, ok := value.(map[string]any)
	if !ok {
		return gatewayproto.EmptyUsage()
	}
	responsesInputDetails, _ := usage["input_tokens_details"].(map[string]any)
	chatInputDetails, _ := usage["prompt_tokens_details"].(map[string]any)
	inputTokens := numberValue(usage["input_tokens"])
	if inputTokens == nil {
		inputTokens = numberValue(usage["prompt_tokens"])
	}
	outputTokens := numberValue(usage["output_tokens"])
	if outputTokens == nil {
		outputTokens = numberValue(usage["completion_tokens"])
	}
	cacheReadTokens := numberValue(responsesInputDetails["cached_tokens"])
	if cacheReadTokens == nil {
		cacheReadTokens = numberValue(chatInputDetails["cached_tokens"])
	}
	if cacheReadTokens == nil {
		cacheReadTokens = numberValue(usage["prompt_cache_hit_tokens"])
	}
	responsesCacheCreation, _ := responsesInputDetails["cache_creation"].(map[string]any)
	chatCacheCreation, _ := chatInputDetails["cache_creation"].(map[string]any)
	rootCacheCreation, _ := usage["cache_creation"].(map[string]any)
	cacheWrite5mTokens := firstNumberValue(
		responsesInputDetails["cache_write_5m_tokens"],
		responsesInputDetails["cache_write_5m_input_tokens"],
		responsesInputDetails["cache_creation_5m_tokens"],
		responsesInputDetails["cache_creation_5m_input_tokens"],
		responsesCacheCreation["ephemeral_5m_input_tokens"],
		chatInputDetails["cache_write_5m_tokens"],
		chatInputDetails["cache_write_5m"],
		chatInputDetails["cache_write_5m_input_tokens"],
		chatInputDetails["cache_creation_5m_tokens"],
		chatInputDetails["cache_creation_5m_input_tokens"],
		chatCacheCreation["ephemeral_5m_input_tokens"],
		usage["cache_write_5m_tokens"],
		usage["cache_write_5m_input_tokens"],
		usage["cache_creation_5m_tokens"],
		usage["cache_creation_5m_input_tokens"],
		usage["cache_creation_5_m_tokens"],
		usage["claude_cache_creation_5m_tokens"],
		usage["claude_cache_creation_5_m_tokens"],
		rootCacheCreation["ephemeral_5m_input_tokens"],
	)
	cacheWrite1hTokens := firstNumberValue(
		responsesInputDetails["cache_write_1h_tokens"],
		responsesInputDetails["cache_write_1h_input_tokens"],
		responsesInputDetails["cache_creation_1h_tokens"],
		responsesInputDetails["cache_creation_1h_input_tokens"],
		responsesCacheCreation["ephemeral_1h_input_tokens"],
		chatInputDetails["cache_write_1h_tokens"],
		chatInputDetails["cache_write_1h_input_tokens"],
		chatInputDetails["cache_creation_1h_tokens"],
		chatInputDetails["cache_creation_1h_input_tokens"],
		chatCacheCreation["ephemeral_1h_input_tokens"],
		usage["cache_write_1h_tokens"],
		usage["cache_write_1h_input_tokens"],
		usage["cache_creation_1h_tokens"],
		usage["cache_creation_1h_input_tokens"],
		usage["cache_creation_1_h_tokens"],
		usage["claude_cache_creation_1h_tokens"],
		usage["claude_cache_creation_1_h_tokens"],
		rootCacheCreation["ephemeral_1h_input_tokens"],
	)
	cacheWriteDetailTokens := sumDefined(cacheWrite5mTokens, cacheWrite1hTokens)
	cacheWriteTokens := firstNumberValue(
		responsesInputDetails["cache_write_tokens"],
		responsesInputDetails["cache_write_input_tokens"],
		responsesInputDetails["cache_creation_tokens"],
		responsesInputDetails["cache_creation_input_tokens"],
		chatInputDetails["cache_write_tokens"],
		chatInputDetails["cache_write_input_tokens"],
		chatInputDetails["cache_creation_tokens"],
		chatInputDetails["cache_creation_input_tokens"],
		usage["cache_write_tokens"],
		usage["cache_write_input_tokens"],
		usage["cache_creation_tokens"],
		usage["cache_creation_input_tokens"],
		cacheWriteDetailTokens,
	)
	outputDetails, okOutput := usage["output_tokens_details"].(map[string]any)
	if !okOutput {
		outputDetails, _ = usage["completion_tokens_details"].(map[string]any)
	}
	inputImageTokens := numberValue(responsesInputDetails["image_tokens"])
	if inputImageTokens == nil {
		inputImageTokens = numberValue(chatInputDetails["image_tokens"])
	}
	outputImageTokens := numberValue(outputDetails["image_tokens"])
	inputAudioTokens := numberValue(responsesInputDetails["audio_tokens"])
	if inputAudioTokens == nil {
		inputAudioTokens = numberValue(chatInputDetails["audio_tokens"])
	}
	outputAudioTokens := numberValue(outputDetails["audio_tokens"])
	thinkingTokens := numberValue(outputDetails["reasoning_tokens"])
	outputImageCount := outputImageCountValue(usage)
	return gatewayproto.ParsedUsage{
		ServiceTier:        serviceTier,
		InputTokens:        inputTokens,
		OutputTokens:       outputTokens,
		CacheReadTokens:    cacheReadTokens,
		CacheWriteTokens:   cacheWriteTokens,
		CacheWrite1hTokens: cacheWrite1hTokens,
		InputImageTokens:   inputImageTokens,
		OutputImageTokens:  outputImageTokens,
		InputAudioTokens:   inputAudioTokens,
		OutputAudioTokens:  outputAudioTokens,
		ThinkingTokens:     thinkingTokens,
		OutputImageCount:   outputImageCount,
	}
}

var usageServiceTierPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

// normalizeServiceTier mirrors normalizeOptionalUsageServiceTier.
func normalizeServiceTier(value any) string {
	text, ok := value.(string)
	if !ok || text != strings.TrimSpace(text) {
		return ""
	}
	if !usageServiceTierPattern.MatchString(text) {
		return ""
	}
	return text
}

func outputImageCountValue(usage map[string]any) *int {
	value := numberValue(usage["output_image_count"])
	if value == nil {
		value = numberValue(usage["output_images"])
	}
	if value == nil {
		value = numberValue(usage["image_count"])
	}
	if value == nil || *value <= 0 {
		return nil
	}
	return value
}

// numberValue mirrors numberValue: numbers or numeric strings, finite and
// non-negative, truncated.
func numberValue(value any) *int {
	switch typed := value.(type) {
	case float64:
		if !math.IsNaN(typed) && !math.IsInf(typed, 0) && typed >= 0 {
			token := int(math.Trunc(typed))
			return &token
		}
	case int:
		if typed >= 0 {
			token := typed
			return &token
		}
	case *int:
		if typed != nil && *typed >= 0 {
			token := *typed
			return &token
		}
	case string:
		if parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64); err == nil &&
			!math.IsNaN(parsed) && !math.IsInf(parsed, 0) && parsed >= 0 {
			token := int(math.Trunc(parsed))
			return &token
		}
	}
	return nil
}

func firstNumberValue(values ...any) *int {
	for _, value := range values {
		if number := numberValue(value); number != nil {
			return number
		}
	}
	return nil
}

func sumDefined(values ...*int) *int {
	var total int
	defined := false
	for _, value := range values {
		if value == nil {
			continue
		}
		total += *value
		defined = true
	}
	if !defined {
		return nil
	}
	return &total
}

// extractJSONStringPropertyFromTextFragment mirrors
// extractJsonStringPropertyFromTextFragment: last "name":"value" match.
func extractJSONStringPropertyFromTextFragment(text, propertyName string) string {
	pattern := regexp.MustCompile(`"` + regexp.QuoteMeta(propertyName) + `"\s*:\s*"([^"\\]*(?:\\.[^"\\]*)*)"`)
	matches := pattern.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return ""
	}
	return matches[len(matches)-1][1]
}

// extractJSONObjectPropertyFromTextFragment mirrors
// extractJsonObjectPropertyFromTextFragment: scan from the end for the last
// "name": { ... } with balanced braces (string-aware).
func extractJSONObjectPropertyFromTextFragment(text, propertyName string) (string, bool) {
	token := `"` + propertyName + `"`
	searchFrom := len(text)
	for searchFrom > 0 {
		tokenIndex := strings.LastIndex(text[:searchFrom], token)
		if tokenIndex < 0 {
			return "", false
		}
		cursor := tokenIndex + len(token)
		cursor = skipJSONWhitespace(text, cursor)
		if cursor >= len(text) || text[cursor] != ':' {
			searchFrom = tokenIndex
			continue
		}
		cursor = skipJSONWhitespace(text, cursor+1)
		if cursor >= len(text) || text[cursor] != '{' {
			searchFrom = tokenIndex
			continue
		}
		if objectText, ok := extractJSONObjectAt(text, cursor); ok {
			return objectText, true
		}
		searchFrom = tokenIndex
	}
	return "", false
}

func extractJSONObjectAt(text string, startIndex int) (string, bool) {
	depth := 0
	inString := false
	escaping := false
	for index := startIndex; index < len(text); index++ {
		char := text[index]
		if inString {
			if escaping {
				escaping = false
			} else if char == '\\' {
				escaping = true
			} else if char == '"' {
				inString = false
			}
			continue
		}
		switch char {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return text[startIndex : index+1], true
			}
		}
	}
	return "", false
}

func skipJSONWhitespace(text string, startIndex int) int {
	index := startIndex
	for index < len(text) && isJSONSpace(text[index]) {
		index++
	}
	return index
}

func isJSONSpace(char byte) bool {
	return char == ' ' || char == '\t' || char == '\n' || char == '\r'
}

// ---- token estimation (stream-events.ts estimator port) ----

// Token estimator limits (mirrors the Node constants).
const (
	tokenEstimateMaxDepth      = 8
	tokenEstimateMaxNodes      = 5000
	tokenEstimateMaxArrayItems = 200
	tokenEstimateMaxObjectKeys = 120
)

var (
	requestTokenEstimateSkippedKeys = map[string]bool{
		"model": true, "stream": true, "stream_options": true,
		"metadata": true, "user": true,
	}
	binaryPayloadEstimateSkippedKeys = map[string]bool{
		"data": true, "b64_json": true, "partial_image_b64": true,
		"result": true, "file_data": true, "audio": true, "image": true,
	}
	outputTokenEstimateSkippedKeys = map[string]bool{
		"object": true, "model": true, "status": true, "created": true,
		"created_at": true, "sequence_number": true, "output_index": true,
		"content_index": true, "item_id": true, "id": true, "index": true,
		"type": true, "role": true, "finish_reason": true, "logprobs": true,
		"usage": true, "error": true,
	}
)

type tokenEstimateContext struct {
	seen  map[string]bool
	nodes int
}

// EstimateTokensFromRequestValue mirrors estimateTokensFromRequestValue.
func EstimateTokensFromRequestValue(value any) int {
	return estimateTokensFromValue(value, "", requestTokenEstimateSkippedKeys)
}

// EstimateTokensFromOutputValue mirrors estimateTokensFromOutputValue.
func EstimateTokensFromOutputValue(value any) int {
	return estimateTokensFromValue(value, "", outputTokenEstimateSkippedKeys)
}

func estimateTokensFromValue(value any, key string, skippedKeys map[string]bool) int {
	context := &tokenEstimateContext{seen: map[string]bool{}}
	return estimateTokensFromValueWithContext(value, key, skippedKeys, context, 0)
}

func estimateTokensFromValueWithContext(value any, key string, skippedKeys map[string]bool, context *tokenEstimateContext, depth int) int {
	if context.nodes >= tokenEstimateMaxNodes || depth > tokenEstimateMaxDepth {
		return 0
	}
	context.nodes++
	switch typed := value.(type) {
	case string:
		if shouldSkipEstimatedString(typed, key) {
			return 0
		}
		return EstimateTokenCountFromText(typed)
	case []any:
		total := 0
		length := len(typed)
		if length > tokenEstimateMaxArrayItems {
			length = tokenEstimateMaxArrayItems
		}
		for index := 0; index < length; index++ {
			total += estimateTokensFromValueWithContext(typed[index], key, skippedKeys, context, depth+1)
			if context.nodes >= tokenEstimateMaxNodes {
				break
			}
		}
		return total
	case map[string]any:
		seenKey := fmt.Sprintf("%p", typed)
		if context.seen[seenKey] {
			return 0
		}
		context.seen[seenKey] = true
		total := 0
		visitedKeys := 0
		for childKey, childValue := range typed {
			if visitedKeys >= tokenEstimateMaxObjectKeys {
				break
			}
			visitedKeys++
			if skippedKeys[childKey] {
				continue
			}
			total += estimateTokensFromValueWithContext(childValue, childKey, skippedKeys, context, depth+1)
			if context.nodes >= tokenEstimateMaxNodes {
				break
			}
		}
		return total
	}
	return 0
}

// EstimateTokenCountFromText mirrors estimateTokenCountFromText: CJK chars
// count one token each, ASCII four per token, other runes two per token.
func EstimateTokenCountFromText(text string) int {
	if strings.TrimSpace(text) == "" {
		return 0
	}
	asciiLikeChars := 0
	cjkChars := 0
	otherChars := 0
	for _, char := range text {
		code := int(char)
		switch {
		case isCjkCodePoint(code):
			cjkChars++
		case code <= 0x7f:
			asciiLikeChars++
		default:
			otherChars++
		}
	}
	total := ceilDiv(asciiLikeChars, 4) + cjkChars + ceilDiv(otherChars, 2)
	if total < 1 {
		return 1
	}
	return total
}

func ceilDiv(value, divisor int) int {
	if divisor <= 0 {
		return 0
	}
	return (value + divisor - 1) / divisor
}

// EstimateTokenCountFromByteLength mirrors estimateTokenCountFromByteLength.
func EstimateTokenCountFromByteLength(bytes int) (int, bool) {
	if bytes <= 0 {
		return 0, false
	}
	tokens := ceilDiv(bytes, 4)
	if tokens < 1 {
		return 1, true
	}
	return tokens, true
}

func shouldSkipEstimatedString(value, key string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return true
	}
	if key == "url" && strings.HasPrefix(trimmed, "data:") {
		return true
	}
	if largeBase64Pattern.MatchString(trimmed) {
		return true
	}
	return looksLikeLargeBase64Payload(trimmed, key)
}

var largeBase64Pattern = regexp.MustCompile(`(?i)^data:[^,]+;base64,`)

func looksLikeLargeBase64Payload(value, key string) bool {
	if !binaryPayloadEstimateSkippedKeys[key] {
		return false
	}
	if len(value) < 512 || strings.ContainsAny(value, " \t\n\r") {
		return false
	}
	normalized := strings.NewReplacer("-", "+", "_", "/").Replace(value)
	if !base64BodyPattern.MatchString(normalized) {
		return false
	}
	return len(normalized)%4 == 0
}

var base64BodyPattern = regexp.MustCompile(`^[A-Za-z0-9+/]+={0,2}$`)

func isCjkCodePoint(code int) bool {
	return (code >= 0x3400 && code <= 0x9fff) ||
		(code >= 0xf900 && code <= 0xfaff) ||
		(code >= 0x20000 && code <= 0x2ebef)
}

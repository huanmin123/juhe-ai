// Package gatewaygemini 迁移 Node 网关 Gemini v1beta 协议切片
// （backend/src/modules/gateway/protocols/gemini-v1beta/*），含
// Interaction 账号亲和（Redis 等运行时状态存储经接口注入）。
//
// 本包自包含：不 import 其他协议包（含 internal/gatewayproto、
// internal/gatewayanthropic），Driver 等价接口、usage、SSE 事件原语、
// 失败归因均在包内定义。
package gatewaygemini

import (
	"encoding/json"
	"math"
	"regexp"
	"strings"
)

// ParsedUsage 对齐 Node gateway/usage/types.ts 的 ParsedUsage。
type ParsedUsage struct {
	UpstreamResponseModel string
	ServiceTier           string
	InputTokens           *int
	OutputTokens          *int
	CacheReadTokens       *int
	CacheWriteTokens      *int
	CacheWrite1hTokens    *int
	ThinkingTokens        *int
	InputImageTokens      *int
	OutputImageTokens     *int
	InputAudioTokens      *int
	OutputAudioTokens     *int
	OutputImageCount      *int
	// ImageCandidatesPresent 标记完整非流式响应根存在 candidates 数组但
	// 没有 image/* inlineData（上游明确 0 张）。仅作为 gatewayresponse 图片
	// 计量的零张依据，不进入 HasAnyUsageValue（文本响应仍是“无图片事实”）。
	ImageCandidatesPresent bool
	// QuantitySource 标记 OutputImageCount 的来源（upstream=响应 inlineData
	// 计数；request=请求 candidateCount 兜底由 gatewayresponse 补齐）。
	QuantitySource string
}

// EmptyUsage 返回空 usage。
func EmptyUsage() ParsedUsage { return ParsedUsage{} }

// MergeUsage 对齐 mergeUsage。
func MergeUsage(current, next ParsedUsage) ParsedUsage {
	pick := func(a, b *int) *int {
		if b != nil {
			return b
		}
		return a
	}
	pickString := func(a, b string) string {
		if b != "" {
			return b
		}
		return a
	}
	return ParsedUsage{
		UpstreamResponseModel:  pickString(current.UpstreamResponseModel, next.UpstreamResponseModel),
		ServiceTier:            pickString(current.ServiceTier, next.ServiceTier),
		InputTokens:            pick(current.InputTokens, next.InputTokens),
		OutputTokens:           pick(current.OutputTokens, next.OutputTokens),
		CacheReadTokens:        pick(current.CacheReadTokens, next.CacheReadTokens),
		CacheWriteTokens:       pick(current.CacheWriteTokens, next.CacheWriteTokens),
		CacheWrite1hTokens:     pick(current.CacheWrite1hTokens, next.CacheWrite1hTokens),
		ThinkingTokens:         pick(current.ThinkingTokens, next.ThinkingTokens),
		InputImageTokens:       pick(current.InputImageTokens, next.InputImageTokens),
		OutputImageTokens:      pick(current.OutputImageTokens, next.OutputImageTokens),
		InputAudioTokens:       pick(current.InputAudioTokens, next.InputAudioTokens),
		OutputAudioTokens:      pick(current.OutputAudioTokens, next.OutputAudioTokens),
		OutputImageCount:       pick(current.OutputImageCount, next.OutputImageCount),
		ImageCandidatesPresent: current.ImageCandidatesPresent || next.ImageCandidatesPresent,
		QuantitySource:         pickString(current.QuantitySource, next.QuantitySource),
	}
}

// HasAnyUsageValue 对齐 hasAnyUsageValue。
func HasAnyUsageValue(value ParsedUsage) bool {
	return value.ServiceTier != "" ||
		value.InputTokens != nil ||
		value.OutputTokens != nil ||
		value.CacheReadTokens != nil ||
		value.CacheWriteTokens != nil ||
		value.CacheWrite1hTokens != nil ||
		value.ThinkingTokens != nil ||
		value.InputImageTokens != nil ||
		value.OutputImageTokens != nil ||
		value.InputAudioTokens != nil ||
		value.OutputAudioTokens != nil ||
		value.OutputImageCount != nil
}

// NormalizeOptionalUsageServiceTier 对齐 usage/service-tier.ts。
func NormalizeOptionalUsageServiceTier(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	if text == "" || strings.TrimSpace(text) != text {
		return ""
	}
	if len(text) > 64 || !serviceTierTokenPattern.MatchString(text) {
		return ""
	}
	return text
}

var serviceTierTokenPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// ParseUsageFromJSONBuffer 对齐 parseGeminiUsageFromJsonBuffer。
func ParseUsageFromJSONBuffer(responseBody []byte) ParsedUsage {
	if len(responseBody) == 0 {
		return EmptyUsage()
	}
	var value any
	if err := json.Unmarshal(responseBody, &value); err == nil {
		// 完整非流式响应：candidates 在场即视为上游明确的图片张数事实，
		// 无 inlineData 图片时写 0，不能回落请求 candidateCount 造成超收。
		return usageWithInlineImageCount(value, ExtractUsage(value), true)
	}
	return ParseUsageFromJSONTextFragment(string(responseBody), false)
}

// ParseUsageFromJSONValue 对齐 parseGeminiUsageFromJsonValue：整对象入口。
func ParseUsageFromJSONValue(value any) ParsedUsage {
	return usageWithInlineImageCount(value, ExtractUsage(value), true)
}

// usageWithInlineImageCount 从 generateContent 完整响应统计内联图片张数
// （非流式唯一事实；流式分片不计数，避免把同一张图的分片重复计费，由
// gatewayresponse 用请求 candidateCount 兜底）。张数进入统一媒体计费
// image 维度（设计 §6.2/§7.3），上游计数优先。complete=true 时 candidates
// 数组在场但无图片 part 视为上游明确 0 张；complete=false（流式片段/文本
// 片段）只在 count>0 时写入，避免分片把兜底路径提前置 0。
func usageWithInlineImageCount(value any, usage ParsedUsage, complete bool) ParsedUsage {
	if usage.OutputImageCount != nil {
		return usage
	}
	count, present := geminiInlineImageCount(value)
	if count > 0 {
		usage.OutputImageCount = &count
		usage.QuantitySource = "upstream"
	} else if complete && present {
		// 完整响应有 candidates 但没有图片 part：上游明确 0 张。由
		// gatewayresponse 仅对图片请求写 0，避免文本响应被误计为图片。
		usage.ImageCandidatesPresent = true
	}
	return usage
}

// geminiInlineImageCount 统计 candidates[].content.parts[].inlineData 中
// mimeType 为 image/* 且带 data 的 part 数；present 表示响应根存在
// candidates 数组（即使为空或全是文本 part），用于区分「上游明确 0 张」与
// 「不是 generateContent 响应」。音频/文本 part 不计入。
func geminiInlineImageCount(value any) (count int, present bool) {
	root, ok := value.(map[string]any)
	if !ok {
		return 0, false
	}
	candidates, ok := root["candidates"].([]any)
	if !ok {
		return 0, false
	}
	for _, candidate := range candidates {
		candidateObject, _ := candidate.(map[string]any)
		content, _ := candidateObject["content"].(map[string]any)
		parts, _ := content["parts"].([]any)
		for _, part := range parts {
			partObject, _ := part.(map[string]any)
			inline, _ := partObject["inlineData"].(map[string]any)
			if inline == nil {
				continue
			}
			data, _ := inline["data"].(string)
			mimeType, _ := inline["mimeType"].(string)
			if data != "" && strings.HasPrefix(strings.ToLower(mimeType), "image/") {
				count++
			}
		}
	}
	return count, true
}

// ParseUsageFromJSONTextFragment 对齐 parseGeminiUsageFromJsonTextFragment：
// 先尝试完整 JSON 文档；失败（或显式跳过）时从片段中抽取
// service_tier 与 total_usage/usageMetadata/usage 对象。
func ParseUsageFromJSONTextFragment(text string, skipFullDocumentParse bool) ParsedUsage {
	if text == "" {
		return EmptyUsage()
	}
	if !skipFullDocumentParse {
		var value any
		if err := json.Unmarshal([]byte(text), &value); err == nil {
			return usageWithInlineImageCount(value, ExtractUsage(value), false)
		}
		// 大响应检查可能只提供有界 JSON 片段而不是完整文档。
	}
	serviceTier := NormalizeOptionalUsageServiceTier(extractJSONStringPropertyFromTextFragment(text, "service_tier"))
	for _, propertyName := range []string{"total_usage", "usageMetadata", "usage"} {
		usageText, ok := extractJSONObjectPropertyFromTextFragment(text, propertyName)
		if !ok {
			continue
		}
		var value any
		if err := json.Unmarshal([]byte(usageText), &value); err != nil {
			continue
		}
		usage := ExtractUsage(value)
		if usageHasDefinedValue(usage) {
			if serviceTier != "" {
				usage.ServiceTier = serviceTier
			}
			return usage
		}
	}
	if serviceTier != "" {
		return ParsedUsage{ServiceTier: serviceTier}
	}
	return EmptyUsage()
}

// usageHasDefinedValue 对齐 Object.values(usage).some(v => v !== undefined)。
func usageHasDefinedValue(usage ParsedUsage) bool {
	return HasAnyUsageValue(usage) || usage.UpstreamResponseModel != ""
}

// ExtractUsage 对齐 extractGeminiUsage：按 interaction/metadata 包裹层级
// 递归下钻，再按 Gemini 与 Interactions 两套字段别名解析。
func ExtractUsage(value any) ParsedUsage {
	usage, ok := value.(map[string]any)
	if !ok {
		return EmptyUsage()
	}
	interaction, _ := usage["interaction"].(map[string]any)
	serviceTier := NormalizeOptionalUsageServiceTier(firstDefined(usage["service_tier"], interaction["service_tier"]))
	nested, hasNested := firstDefinedObject(
		lookupObject(usage, "metadata", "total_usage"),
		optionalObjectOf(usage["total_usage"]),
		optionalObjectOf(usage["usageMetadata"]),
		optionalObjectOf(usage["usage"]),
		optionalObjectOf(interaction["usage"]),
	)
	if hasNested {
		nestedUsage := ExtractUsage(nested)
		if serviceTier != "" {
			nestedUsage.ServiceTier = serviceTier
		}
		return nestedUsage
	}
	candidateTokens := firstNumber(usage,
		"candidatesTokenCount", "totalOutputTokens", "total_output_tokens", "outputTokens", "output_tokens")
	thinkingTokens := firstNumber(usage,
		"thoughtsTokenCount", "totalThoughtTokens", "total_thought_tokens", "thoughtTokens", "thought_tokens")
	result := ParsedUsage{
		InputTokens: firstNumber(usage,
			"promptTokenCount", "totalInputTokens", "total_input_tokens", "inputTokens", "input_tokens"),
		OutputTokens: sumDefined(candidateTokens, thinkingTokens),
		CacheReadTokens: firstNumber(usage,
			"cachedContentTokenCount", "totalCachedTokens", "total_cached_tokens", "cachedTokens", "cached_tokens"),
		ThinkingTokens: thinkingTokens,
	}
	if serviceTier != "" {
		result.ServiceTier = serviceTier
	}
	return result
}

func firstDefined(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

// optionalObject 承载「可能存在的嵌套对象」候选。
type optionalObject struct {
	value map[string]any
	ok    bool
}

func optionalObjectOf(value any) optionalObject {
	object, ok := value.(map[string]any)
	return optionalObject{value: object, ok: ok}
}

func lookupObject(value map[string]any, keys ...string) optionalObject {
	current := value
	for _, key := range keys {
		next, ok := current[key].(map[string]any)
		if !ok {
			return optionalObject{}
		}
		current = next
	}
	return optionalObject{value: current, ok: true}
}

func firstDefinedObject(candidates ...optionalObject) (map[string]any, bool) {
	for _, candidate := range candidates {
		if candidate.ok {
			return candidate.value, true
		}
	}
	return nil, false
}

func firstNumber(value map[string]any, keys ...string) *int {
	for _, key := range keys {
		if number := numberValue(value[key]); number != nil {
			return number
		}
	}
	return nil
}

// numberValue 对齐 usage.ts 的 numberValue。
func numberValue(value any) *int {
	var number float64
	switch typed := value.(type) {
	case float64:
		number = typed
	case string:
		if err := json.Unmarshal([]byte(typed), &number); err != nil {
			return nil
		}
	default:
		return nil
	}
	if math.IsNaN(number) || math.IsInf(number, 0) || number < 0 {
		return nil
	}
	tokens := int(math.Trunc(number))
	return &tokens
}

func sumDefined(values ...*int) *int {
	total := 0
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

// extractJSONStringPropertyFromTextFragment 对齐 extractJsonStringPropertyFromTextFragment：
// 返回最后一个匹配的字符串属性值。
func extractJSONStringPropertyFromTextFragment(text, propertyName string) string {
	pattern, err := regexp.Compile(`"` + regexp.QuoteMeta(propertyName) + `"\s*:\s*"([^"\\]*(?:\\.[^"\\]*)*)"`)
	if err != nil {
		return ""
	}
	value := ""
	for _, match := range pattern.FindAllStringSubmatch(text, -1) {
		value = match[1]
	}
	return value
}

// extractJSONObjectPropertyFromTextFragment / extractJSONObjectAt /
// skipJSONWhitespace 与 usage.ts 共用同一算法（反查最后一个完整 JSON 对象）。
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
	for index < len(text) && isJSONWhitespace(text[index]) {
		index++
	}
	return index
}

func isJSONWhitespace(char byte) bool {
	return char == ' ' || char == '\t' || char == '\n' || char == '\r' || char == '\v' || char == '\f'
}

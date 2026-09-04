// Package gatewayanthropic 迁移 Node 网关 Anthropic /v1 协议切片
// （backend/src/modules/gateway/protocols/anthropic-v1/*）。
//
// 本包自包含：不 import 其他协议包（含 internal/gatewayproto、internal/gatewaygemini），
// Driver 等价接口、usage、SSE 事件原语均在包内定义。
package gatewayanthropic

import (
	"encoding/json"
	"math"
	"regexp"
	"strings"
)

// ParsedUsage 对齐 Node gateway/usage/types.ts 的 ParsedUsage。
// 指针字段为 nil 表示上游未上报该值。
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
}

// EmptyUsage 返回空 usage（对齐 emptyUsage）。
func EmptyUsage() ParsedUsage { return ParsedUsage{} }

// MergeUsage 对齐 mergeUsage：next 中非 nil 的字段覆盖 current。
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
		UpstreamResponseModel: pickString(current.UpstreamResponseModel, next.UpstreamResponseModel),
		ServiceTier:           pickString(current.ServiceTier, next.ServiceTier),
		InputTokens:           pick(current.InputTokens, next.InputTokens),
		OutputTokens:          pick(current.OutputTokens, next.OutputTokens),
		CacheReadTokens:       pick(current.CacheReadTokens, next.CacheReadTokens),
		CacheWriteTokens:      pick(current.CacheWriteTokens, next.CacheWriteTokens),
		CacheWrite1hTokens:    pick(current.CacheWrite1hTokens, next.CacheWrite1hTokens),
		ThinkingTokens:        pick(current.ThinkingTokens, next.ThinkingTokens),
		InputImageTokens:      pick(current.InputImageTokens, next.InputImageTokens),
		OutputImageTokens:     pick(current.OutputImageTokens, next.OutputImageTokens),
		InputAudioTokens:      pick(current.InputAudioTokens, next.InputAudioTokens),
		OutputAudioTokens:     pick(current.OutputAudioTokens, next.OutputAudioTokens),
		OutputImageCount:      pick(current.OutputImageCount, next.OutputImageCount),
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

// NormalizeOptionalUsageServiceTier 对齐 usage/service-tier.ts 的
// normalizeOptionalUsageServiceTier：非空、无首尾空白且匹配
// /^[a-z0-9][a-z0-9._-]{0,63}$/i 的字符串才有效。
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

// ParseUsageFromJSONBuffer 对齐 parseAnthropicUsageFromJsonBuffer。
func ParseUsageFromJSONBuffer(responseBody []byte) ParsedUsage {
	if len(responseBody) == 0 {
		return EmptyUsage()
	}
	return ParseUsageFromJSONTextFragment(string(responseBody))
}

// ParseUsageFromJSONValue 对齐 parseAnthropicUsageFromJsonValue：只读根对象的 usage 属性。
func ParseUsageFromJSONValue(value any) ParsedUsage {
	root, ok := value.(map[string]any)
	if !ok {
		return EmptyUsage()
	}
	return ExtractUsage(root["usage"])
}

// ParseUsageFromJSONTextFragment 对齐 parseAnthropicUsageFromJsonTextFragment：
// 在大响应文本片段中反向查找最后一个完整的 "usage":{...} 对象。
func ParseUsageFromJSONTextFragment(text string) ParsedUsage {
	if text == "" {
		return EmptyUsage()
	}
	usageText, ok := extractJSONObjectPropertyFromTextFragment(text, "usage")
	if !ok {
		return EmptyUsage()
	}
	var value any
	if err := json.Unmarshal([]byte(usageText), &value); err != nil {
		return EmptyUsage()
	}
	return ExtractUsage(value)
}

// ExtractUsage 对齐 extractAnthropicUsage。
func ExtractUsage(value any) ParsedUsage {
	usage, ok := value.(map[string]any)
	if !ok {
		return EmptyUsage()
	}
	cacheCreation, _ := usage["cache_creation"].(map[string]any)
	cacheWrite5mTokens := numberValue(cacheCreation["ephemeral_5m_input_tokens"])
	cacheWrite1hTokens := numberValue(cacheCreation["ephemeral_1h_input_tokens"])
	cacheWriteDetailTokens := sumDefined(cacheWrite5mTokens, cacheWrite1hTokens)
	cacheWriteTokens := numberValue(usage["cache_creation_input_tokens"])
	if cacheWriteTokens == nil {
		cacheWriteTokens = cacheWriteDetailTokens
	}
	outputTokenDetails, _ := usage["output_tokens_details"].(map[string]any)
	return ParsedUsage{
		ServiceTier:        NormalizeOptionalUsageServiceTier(usage["speed"]),
		InputTokens:        numberValue(usage["input_tokens"]),
		OutputTokens:       numberValue(usage["output_tokens"]),
		CacheReadTokens:    numberValue(usage["cache_read_input_tokens"]),
		CacheWriteTokens:   cacheWriteTokens,
		CacheWrite1hTokens: cacheWrite1hTokens,
		ThinkingTokens:     numberValue(outputTokenDetails["thinking_tokens"]),
	}
}

// numberValue 对齐 usage.ts 的 numberValue：数字或数字字符串，
// 必须有限且 >= 0，截断小数。
func numberValue(value any) *int {
	var number float64
	switch typed := value.(type) {
	case float64:
		number = typed
	case string:
		parsed, err := parseJSONNumber(typed)
		if err != nil {
			return nil
		}
		number = parsed
	default:
		return nil
	}
	if math.IsNaN(number) || math.IsInf(number, 0) || number < 0 {
		return nil
	}
	tokens := int(math.Trunc(number))
	return &tokens
}

// parseJSONNumber 对齐 JS Number(value) 的宽松解析范围（仅接受 JSON 数字字面量）。
func parseJSONNumber(text string) (float64, error) {
	var value float64
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		return 0, err
	}
	return value, nil
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

// extractJSONObjectPropertyFromTextFragment 对齐 usage.ts 同名函数：
// 在文本片段中反向查找最后一个 "propertyName":{...} 完整对象。
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

// extractJSONObjectAt 对齐 extractJsonObjectAt：从 startIndex 的 '{' 开始
// 提取配平的对象文本（支持字符串内的转义）。
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

// skipJSONWhitespace 对齐 skipJsonWhitespace。
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

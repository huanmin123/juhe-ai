package chat

// 通用 JSON 工具（原随 responses SSE 解析器落在 responses_sse.go；Responses
// 主流解析删除后保留仍被图像生成响应解析等路径消费的纯工具函数）。

import (
	"bytes"
	"errors"
	"regexp"
	"strings"
)

var jsonStringFieldPatterns = map[string]*regexp.Regexp{}

func fieldPattern(field, suffix string) *regexp.Regexp {
	pattern := jsonStringFieldPatterns[field+suffix]
	if pattern == nil {
		pattern = regexp.MustCompile(`"` + regexp.QuoteMeta(field) + `"\s*:\s*` + suffix)
		jsonStringFieldPatterns[field+suffix] = pattern
	}
	return pattern
}

func extractJSONStringField(data, field string) string {
	match := fieldPattern(field, `"([^"\\]{1,512})"`).FindStringSubmatch(data)
	if match == nil {
		return ""
	}
	return match[1]
}

func objectItem(value any) map[string]any {
	if item, ok := value.(map[string]any); ok {
		return item
	}
	return map[string]any{}
}

func stringValueItem(item map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := item[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func sortInt64s(values []int64) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

// stripImageResultStrings splits one spooled JSON document into the payload
// with image base64 values removed plus the extracted values in order. It
// mirrors the readSpooledCompletedResponse / extractSpooledImageResultChunks
// streaming state machine (Node chat-responses-sse.ts) including the
func stripImageResultStrings(data string, resultFields ...string) (string, []string, error) {
	var values []string
	var out bytes.Buffer
	var current bytes.Buffer
	inString := false
	escaped := false
	stringToken := ""
	stringTokenEscaped := false
	stringIsValue := false
	stringIsImageResult := false
	collectingResult := false
	var lastStringToken *string
	awaitingValue := false
	var valueKey *string

	for _, character := range data {
		if inString {
			if stringIsImageResult {
				if escaped || character == '\\' {
					return "", nil, errors.New("生成图片 Base64 不允许 JSON 转义")
				}
				if character == '"' {
					if collectingResult && current.Len() == 0 {
						return "", nil, errors.New("生成图片 Base64 不能为空")
					}
					if collectingResult {
						values = append(values, current.String())
					}
					current.Reset()
					out.WriteByte('"')
					inString = false
					stringIsImageResult = false
					collectingResult = false
					lastStringToken = nil
					continue
				}
				if collectingResult {
					current.WriteRune(character)
				}
				continue
			}
			out.WriteRune(character)
			if escaped {
				escaped = false
				stringTokenEscaped = true
				continue
			}
			if character == '\\' {
				escaped = true
				stringTokenEscaped = true
				continue
			}
			if character == '"' {
				inString = false
				if stringIsValue || stringTokenEscaped {
					lastStringToken = nil
				} else {
					copied := stringToken
					lastStringToken = &copied
				}
				continue
			}
			if !stringTokenEscaped && len(stringToken) <= 64 {
				stringToken += string(character)
			}
			continue
		}
		out.WriteRune(character)
		if character == '"' {
			inString = true
			escaped = false
			stringToken = ""
			stringTokenEscaped = false
			stringIsValue = awaitingValue
			stringIsImageResult = awaitingValue && valueKey != nil && isImageResultFieldName(*valueKey, resultFields...)
			if stringIsImageResult {
				collectingResult = true
				current.Reset()
				out.Truncate(out.Len() - 1)
			}
			awaitingValue = false
			valueKey = nil
			lastStringToken = nil
			continue
		}
		if character == ' ' || character == '\t' || character == '\n' || character == '\r' {
			continue
		}
		if character == ':' {
			if lastStringToken != nil {
				copied := *lastStringToken
				valueKey = &copied
				awaitingValue = true
			} else {
				valueKey = nil
				awaitingValue = false
			}
			lastStringToken = nil
			continue
		}
		if awaitingValue {
			awaitingValue = false
			valueKey = nil
		}
		lastStringToken = nil
	}
	if inString {
		return "", nil, errors.New("上游 Responses 终态 JSON 被截断")
	}
	return out.String(), values, nil
}

func isImageResultFieldName(value string, resultFields ...string) bool {
	if len(resultFields) == 0 {
		resultFields = []string{"result", "b64_json"}
	}
	return containsString(resultFields, value)
}

// extractImageResultChunks mirrors imageResultChunks: the ordered base64
// payloads of the result/b64_json fields in one data document.
func extractImageResultChunks(data string) []string {
	_, values, err := stripImageResultStrings(data, "result", "b64_json")
	if err != nil {
		return nil
	}
	return values
}

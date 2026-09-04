package gatewayanthropic

import (
	"encoding/json"
	"net/http"
	"strings"
)

// ErrorPayload 对齐 GatewayProtocolErrorPayload（code/type/message 三字段契约）。
type ErrorPayload struct {
	Code    string
	Type    string
	Message string
}

// IsZero 表示上游响应没有可解析的错误负载。
func (p ErrorPayload) IsZero() bool { return p.Code == "" && p.Type == "" && p.Message == "" }

// ParseErrorPayload 对齐 parseAnthropicErrorPayload：仅解析 JSON 负载
// （Content-Type 含 json 或文本以 { 开头）。
func ParseErrorPayload(text string, header http.Header) ErrorPayload {
	parsed, ok := parseJSONObjectErrorPayload(text, header)
	if !ok {
		return ErrorPayload{}
	}
	return anthropicErrorPayloadFromParsed(parsed.payload, parsed.errorObject)
}

// ParseErrorPayloadFromJSONValue 对齐 parseAnthropicErrorPayloadFromJsonValue。
func ParseErrorPayloadFromJSONValue(value any) ErrorPayload {
	payload, errorObject, ok := jsonObjectErrorPayload(value)
	if !ok {
		return ErrorPayload{}
	}
	return anthropicErrorPayloadFromParsed(payload, errorObject)
}

func anthropicErrorPayloadFromParsed(payload, errorObject map[string]any) ErrorPayload {
	if payload == nil {
		return ErrorPayload{}
	}
	typeValue := orString(stringFieldValue(errorObject["type"]), stringFieldValue(payload["type"]))
	return ErrorPayload{
		Code: orString(
			stringFieldValue(errorObject["code"]),
			stringFieldValue(payload["code"]),
			typeValue,
		),
		Type: typeValue,
		Message: orString(
			stringFieldValue(errorObject["message"]),
			stringFieldValue(errorObject["msg"]),
			stringFieldValue(errorObject["error_message"]),
			stringFieldValue(errorObject["error_description"]),
			stringFieldValue(errorObject["detail"]),
			stringFieldValue(payload["message"]),
			stringFieldValue(payload["msg"]),
			stringFieldValue(payload["error_message"]),
			stringFieldValue(payload["error_description"]),
			stringFieldValue(payload["detail"]),
		),
	}
}

// parsedErrorPayload 持有根对象与其中的 error 子对象（error 缺省时为根对象）。
type parsedErrorPayload struct {
	payload     map[string]any
	errorObject map[string]any
}

// parseJSONObjectErrorPayload 对齐 _shared/error-payload.ts 的
// parseJsonObjectErrorPayload。
func parseJSONObjectErrorPayload(text string, header http.Header) (parsedErrorPayload, bool) {
	trimmed := strings.TrimSpace(text)
	contentType := ""
	if header != nil {
		contentType = header.Get("Content-Type")
	}
	if !strings.Contains(contentType, "json") && !strings.HasPrefix(trimmed, "{") {
		return parsedErrorPayload{}, false
	}
	var value any
	if err := json.Unmarshal([]byte(trimmed), &value); err != nil {
		return parsedErrorPayload{}, false
	}
	payload, errorObject, ok := jsonObjectErrorPayload(value)
	if !ok {
		return parsedErrorPayload{}, false
	}
	return parsedErrorPayload{payload: payload, errorObject: errorObject}, true
}

// jsonObjectErrorPayload 对齐 jsonObjectErrorPayload。
func jsonObjectErrorPayload(value any) (map[string]any, map[string]any, bool) {
	payload, ok := value.(map[string]any)
	if !ok {
		return nil, nil, false
	}
	errorObject, ok := payload["error"].(map[string]any)
	if !ok {
		errorObject = payload
	}
	return payload, errorObject, true
}

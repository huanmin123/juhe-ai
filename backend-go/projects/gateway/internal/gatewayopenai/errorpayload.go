package gatewayopenai

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// ParseErrorPayload mirrors parseOpenAIErrorPayload.
func ParseErrorPayload(bodyText string, header http.Header) gatewayproto.ErrorPayload {
	payload, errorObject := parseJSONObjectErrorPayload(bodyText, header)
	if payload == nil {
		return gatewayproto.ErrorPayload{}
	}
	return openAIErrorPayloadFromParsed(payload, errorObject)
}

// ParseErrorPayloadFromJSONValue mirrors parseOpenAIErrorPayloadFromJsonValue.
func ParseErrorPayloadFromJSONValue(value any) gatewayproto.ErrorPayload {
	root, ok := value.(map[string]any)
	if !ok {
		return gatewayproto.ErrorPayload{}
	}
	errorObject, _ := root["error"].(map[string]any)
	return openAIErrorPayloadFromParsed(root, errorObject)
}

// parseJSONObjectErrorPayload mirrors parseJsonObjectErrorPayload.
func parseJSONObjectErrorPayload(bodyText string, header http.Header) (map[string]any, map[string]any) {
	trimmed := strings.TrimSpace(bodyText)
	if header != nil {
		if contentType := header.Get("Content-Type"); contentType != "" {
			if !strings.Contains(contentType, "json") && !strings.HasPrefix(trimmed, "{") {
				return nil, nil
			}
		} else if !strings.HasPrefix(trimmed, "{") {
			return nil, nil
		}
	} else if !strings.HasPrefix(trimmed, "{") {
		return nil, nil
	}
	var value any
	if err := json.Unmarshal([]byte(trimmed), &value); err != nil {
		return nil, nil
	}
	root, ok := value.(map[string]any)
	if !ok {
		return nil, nil
	}
	errorObject, _ := root["error"].(map[string]any)
	return root, errorObject
}

// openAIErrorPayloadFromParsed mirrors openAIErrorPayloadFromParsed.
func openAIErrorPayloadFromParsed(payload, errorObject map[string]any) gatewayproto.ErrorPayload {
	if payload == nil {
		return gatewayproto.ErrorPayload{}
	}
	if errorObject == nil {
		errorObject = payload
	}
	nestedError := nestedErrorObject(errorObject)
	nestedPayload := nestedErrorObject(payload)
	return gatewayproto.ErrorPayload{
		Code: firstErrorFieldText(
			errorObject["code"], payload["code"],
			nestedErrorField(nestedError, "code"), nestedPayloadField(nestedPayload, "code"),
			errorObject["type"], payload["type"],
		),
		Type: firstErrorFieldText(
			errorObject["type"], payload["type"],
			nestedErrorField(nestedError, "type"), nestedPayloadField(nestedPayload, "type"),
		),
		Message: firstErrorFieldText(
			errorObject["message"], errorObject["msg"],
			errorObject["error_message"], errorObject["error_description"],
			errorObject["detail"], errorObject["reason"],
			nestedErrorField(nestedError, "message"), nestedErrorField(nestedError, "msg"),
			nestedErrorField(nestedError, "error_message"), nestedErrorField(nestedError, "error_description"),
			nestedErrorField(nestedError, "detail"),
			nestedPayloadField(nestedPayload, "message"), nestedPayloadField(nestedPayload, "msg"),
			nestedPayloadField(nestedPayload, "error_message"), nestedPayloadField(nestedPayload, "error_description"),
			nestedPayloadField(nestedPayload, "detail"),
			payload["message"], payload["msg"],
			payload["error_message"], payload["error_description"],
			payload["detail"], payload["reason"],
		),
	}
}

func nestedErrorField(nested map[string]any, field string) any {
	if nested == nil {
		return nil
	}
	return nested[field]
}

func nestedPayloadField(nested map[string]any, field string) any {
	if nested == nil {
		return nil
	}
	return nested[field]
}

// nestedErrorObject mirrors nestedErrorObject.
func nestedErrorObject(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	for _, key := range []string{"error", "err", "detail", "details", "data"} {
		if child, ok := value[key].(map[string]any); ok {
			return child
		}
	}
	return nil
}

// firstErrorFieldText mirrors firstErrorFieldText.
func firstErrorFieldText(values ...any) string {
	for _, value := range values {
		if text := errorFieldText(value); text != "" {
			return text
		}
	}
	return ""
}

// errorFieldText mirrors errorFieldText.
func errorFieldText(value any) string {
	if scalar := stringErrorField(value); scalar != "" {
		return scalar
	}
	record, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	return firstErrorFieldText(
		record["message"], record["msg"],
		record["error_message"], record["error_description"],
		record["detail"], record["reason"],
		record["code"], record["type"],
	)
}

// stringErrorField mirrors stringErrorField.
func stringErrorField(value any) string {
	switch typed := value.(type) {
	case string:
		text := strings.TrimSpace(typed)
		return text
	case float64:
		return trimNumber(typed)
	case bool:
		if typed {
			return "true"
		}
		return "false"
	}
	return ""
}

func trimNumber(value float64) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(encoded)
}

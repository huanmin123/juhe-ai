// Package kernel owns the Go gateway HTTP boundary contracts that Node's
// system-api-app.ts and shared middlewares provided: response envelopes,
// error localization, management security headers, compression, no-store,
// body limits, request context and mutation deduplication. Semantics mirror
// the Node sources; behavior differences must fail the kernel parity tests.
package kernel

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
)

// Status messages mirror shared/system-error-message.ts exactly.
func SystemErrorMessageForStatus(statusCode int) string {
	switch statusCode {
	case http.StatusBadRequest:
		return "请求参数无效"
	case http.StatusUnauthorized:
		return "身份验证失败，请检查访问凭据"
	case http.StatusForbidden:
		return "无权执行此操作"
	case http.StatusNotFound:
		return "请求的资源不存在"
	case http.StatusMethodNotAllowed:
		return "请求方法不被支持"
	case http.StatusConflict:
		return "请求状态已发生变化，请刷新后重试"
	case http.StatusRequestEntityTooLarge:
		return "请求内容过大"
	case http.StatusUnprocessableEntity:
		return "请求内容无法处理"
	case http.StatusTooManyRequests:
		return "请求过于频繁，请稍后重试"
	case http.StatusBadGateway:
		return "服务处理上游响应失败，请稍后重试"
	case http.StatusServiceUnavailable:
		return "服务暂时不可用，请稍后重试"
	case http.StatusGatewayTimeout:
		return "服务处理超时，请稍后重试"
	default:
		return "请求处理失败，请稍后重试"
	}
}

var chineseCharacterPattern = regexp.MustCompile("[\u3400-\u9fff]")

// LocalizeSystemErrorMessage mirrors localizeSystemErrorMessage: a non-empty
// message already containing CJK characters is preserved; anything else is
// replaced by the status default.
func LocalizeSystemErrorMessage(message string, statusCode int) string {
	normalized := strings.TrimSpace(message)
	if normalized != "" && chineseCharacterPattern.MatchString(normalized) {
		return message
	}
	return SystemErrorMessageForStatus(statusCode)
}

// localizeSystemErrorPayload mirrors localizeSystemErrorPayload: for >=400
// responses it rewrites top-level "message" and nested "error.message".
func localizeSystemErrorPayload(payload []byte, statusCode int, preserveUpstream bool) ([]byte, bool) {
	if statusCode < 400 || preserveUpstream || len(payload) == 0 {
		return payload, false
	}
	var doc any
	if err := json.Unmarshal(payload, &doc); err != nil {
		if isPlainTextJSON(payload) {
			return []byte(LocalizeSystemErrorMessage(strings.TrimSpace(string(payload)), statusCode)), true
		}
		return payload, false
	}
	switch value := doc.(type) {
	case string:
		encoded, err := json.Marshal(LocalizeSystemErrorMessage(value, statusCode))
		if err != nil {
			return payload, false
		}
		return encoded, true
	case map[string]any:
		changed := false
		if raw, ok := value["message"].(string); ok {
			localized := LocalizeSystemErrorMessage(raw, statusCode)
			if localized != raw {
				value["message"] = localized
				changed = true
			}
		}
		if nested, ok := value["error"].(map[string]any); ok {
			if raw, ok := nested["message"].(string); ok {
				localized := LocalizeSystemErrorMessage(raw, statusCode)
				if localized != raw {
					nested["message"] = localized
					changed = true
				}
			}
		}
		if !changed {
			return payload, false
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return payload, false
		}
		return encoded, true
	default:
		return payload, false
	}
}

func isPlainTextJSON(payload []byte) bool {
	trimmed := strings.TrimSpace(string(payload))
	return trimmed != "" && !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") && !strings.HasPrefix(trimmed, "\"")
}

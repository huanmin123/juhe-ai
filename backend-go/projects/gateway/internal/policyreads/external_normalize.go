// external_normalize.go holds the pure normalizer/decoder helpers for the
// external integration source domain: scopes, rate limits, statuses, names and
// nullable field inputs, plus token hashing/generation.
package policyreads

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/apikeys"
)

// ---------------------------------------------------------------------------
// Scope normalizers (external-integration-source-normalizers.ts).
// ---------------------------------------------------------------------------

var externalRateLimitRuleKeys = []string{"windowSeconds", "maxRequests"}

func externalScopeSupported(value string) bool {
	for _, option := range externalIntegrationScopeOptions {
		if option.Value == value {
			return true
		}
	}
	return false
}

// normalizeExternalScopes mirrors normalizeScopes.
func normalizeExternalScopes(scopes any) ([]string, error) {
	items, ok := scopes.([]any)
	if !ok {
		if scopes == nil {
			return []string{}, nil
		}
		return nil, &ValidationError{Message: "来源系统 scopes 必须是字符串数组"}
	}
	seen := map[string]bool{}
	out := []string{}
	for _, item := range items {
		text, isString := item.(string)
		if !isString {
			return nil, &ValidationError{Message: "来源系统 scopes 必须是字符串数组"}
		}
		value := strings.TrimSpace(text)
		if value == "" {
			return nil, &ValidationError{Message: "来源系统 scopes 不能为空"}
		}
		if !externalScopeSupported(value) {
			return nil, &ValidationError{Message: "来源系统 scope 不受支持：" + value}
		}
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return uniqueSortedStrings(out), nil
}

// decodeExternalScopes mirrors decodeScopes: unknown scopes on stored rows are
// dropped, the rest re-normalized.
func decodeExternalScopes(value string) ([]string, error) {
	var parsed any
	if err := json.Unmarshal([]byte(value), &parsed); err != nil {
		return nil, err
	}
	if items, isArray := parsed.([]any); isArray {
		known := []any{}
		for _, item := range items {
			text, isString := item.(string)
			if !isString || externalScopeSupported(strings.TrimSpace(text)) {
				known = append(known, item)
			}
		}
		return normalizeExternalScopes(known)
	}
	return normalizeExternalScopes(parsed)
}

// normalizeExternalRateLimits mirrors normalizeRateLimits.
func normalizeExternalRateLimits(rules any) ([]ExternalRateLimitRule, error) {
	items, ok := rules.([]any)
	if !ok {
		if rules == nil {
			return []ExternalRateLimitRule{}, nil
		}
		return nil, &ValidationError{Message: "来源系统限频规则必须是数组"}
	}
	if len(items) > 8 {
		return nil, &ValidationError{Message: "来源系统限频规则最多 8 条"}
	}
	normalized := []ExternalRateLimitRule{}
	seen := map[int]bool{}
	for _, item := range items {
		record, isObject := item.(map[string]any)
		if !isObject {
			return nil, &ValidationError{Message: "来源系统限频规则必须是对象"}
		}
		unknown := []string{}
		for key := range record {
			if !containsString(externalRateLimitRuleKeys, key) {
				unknown = append(unknown, key)
			}
		}
		if len(unknown) > 0 {
			return nil, &ValidationError{Message: "来源系统限频规则包含未知字段：" + strings.Join(unknown, "、")}
		}
		windowSeconds, err := externalRateLimitInteger(record["windowSeconds"], 1, 86400, "来源系统限频窗口")
		if err != nil {
			return nil, err
		}
		maxRequests, err := externalRateLimitInteger(record["maxRequests"], 1, 100000, "来源系统限频次数")
		if err != nil {
			return nil, err
		}
		if seen[windowSeconds] {
			return nil, &ValidationError{Message: "来源系统限频窗口不能重复"}
		}
		seen[windowSeconds] = true
		normalized = append(normalized, ExternalRateLimitRule{WindowSeconds: windowSeconds, MaxRequests: maxRequests})
	}
	for i := 1; i < len(normalized); i++ {
		for j := i; j > 0 && normalized[j].WindowSeconds < normalized[j-1].WindowSeconds; j-- {
			normalized[j], normalized[j-1] = normalized[j-1], normalized[j]
		}
	}
	return normalized, nil
}

func externalRateLimitInteger(value any, min, max int, label string) (int, error) {
	number, isNumber := value.(float64)
	if !isNumber {
		return 0, &ValidationError{Message: label + "必须是整数"}
	}
	if number != float64(int64(number)) {
		return 0, &ValidationError{Message: label + "必须是整数"}
	}
	result := int(number)
	if result < min || result > max {
		return 0, &ValidationError{Message: label + "必须在 " + strconv.Itoa(min) + " 到 " + strconv.Itoa(max) + " 之间"}
	}
	return result, nil
}

func decodeExternalRateLimits(value string) ([]ExternalRateLimitRule, error) {
	if value == "" {
		return []ExternalRateLimitRule{}, nil
	}
	var parsed any
	if err := json.Unmarshal([]byte(value), &parsed); err != nil {
		return nil, err
	}
	return normalizeExternalRateLimits(parsed)
}

// normalizeExternalNullableISO mirrors normalizeNullableIso.
func normalizeExternalNullableISO(value any) (*string, error) {
	if value == nil {
		return nil, nil
	}
	text, isString := value.(string)
	if !isString {
		return nil, &ValidationError{Message: "过期时间无效"}
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil, &ValidationError{Message: "过期时间无效"}
	}
	canonical, ok := canonicalRFC3339Millis(trimmed)
	if !ok {
		return nil, &ValidationError{Message: "过期时间无效"}
	}
	return &canonical, nil
}

// normalizeExternalNullableText mirrors normalizeNullableText.
func normalizeExternalNullableText(value any) (*string, error) {
	if value == nil {
		return nil, nil
	}
	text, isString := value.(string)
	if !isString {
		return nil, &ValidationError{Message: "备注必须是字符串"}
	}
	trimmed := strings.TrimSpace(text)
	if runeLen(trimmed) > 500 {
		return nil, &ValidationError{Message: "备注不能超过 500 个字符"}
	}
	if trimmed == "" {
		return nil, nil
	}
	return &trimmed, nil
}

func normalizeExternalName(value any, message string) (string, error) {
	text, isString := value.(string)
	if !isString {
		return "", &ValidationError{Message: message}
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", &ValidationError{Message: message}
	}
	if runeLen(trimmed) > 80 {
		return "", &ValidationError{Message: "来源系统名称不能超过 80 个字符"}
	}
	return trimmed, nil
}

func normalizeSourceStatus(value string) (string, error) {
	if value == "active" || value == "disabled" {
		return value, nil
	}
	return "", &ValidationError{Message: "来源系统状态无效"}
}

func normalizeSourceStatusInput(status any) (string, error) {
	if status == nil {
		return "active", nil
	}
	text, isString := status.(string)
	if !isString {
		return "", &ValidationError{Message: "来源系统状态无效"}
	}
	return normalizeSourceStatus(text)
}

func normalizeTokenStatus(value string) (string, error) {
	if value == "active" || value == "disabled" || value == "revoked" {
		return value, nil
	}
	return "", &ValidationError{Message: "来源系统 token 状态无效"}
}

func normalizeTokenStatusInput(status any) (string, error) {
	if status == nil {
		return "active", nil
	}
	text, isString := status.(string)
	if !isString {
		return "", &ValidationError{Message: "来源系统 token 状态无效"}
	}
	return normalizeTokenStatus(text)
}

// hashExternalSourceToken mirrors hashExternalIntegrationSourceTokenValue.
func hashExternalSourceToken(token string) string {
	return apikeys.HashSecret("external-integration-source-token:" + token)
}

// createExternalSourceTokenValue mirrors createExternalIntegrationSourceTokenValue.
func createExternalSourceTokenValue() string {
	return "juis_" + randomBase64URLBytes(32)
}

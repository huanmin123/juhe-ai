// external_parse.go mirrors the M16b zod schemas: list query parsing and the
// strict create/update/delete body validators for sources and tokens.
package policyreads

import (
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// M16b zod schema mirrors.
// ---------------------------------------------------------------------------

// parseExternalListQuery mirrors listQuerySchema (non-strict).
func parseExternalListQuery(query map[string][]string) (page, pageSize *int, keyword, status, message string) {
	if _, exists := firstQueryValue(query, "page"); exists {
		number, issue := coerceQueryNumber(query["page"])
		if issue != "" {
			return nil, nil, "", "", issue
		}
		if number != float64(int64(number)) {
			return nil, nil, "", "", "Expected integer, received float"
		}
		value := int(number)
		if value < 1 {
			return nil, nil, "", "", zodNumberMin(1)
		}
		page = &value
	}
	if _, exists := firstQueryValue(query, "pageSize"); exists {
		number, issue := coerceQueryNumber(query["pageSize"])
		if issue != "" {
			return nil, nil, "", "", issue
		}
		if number != float64(int64(number)) {
			return nil, nil, "", "", "Expected integer, received float"
		}
		value := int(number)
		if value < 1 {
			return nil, nil, "", "", zodNumberMin(1)
		}
		if value > 100 {
			return nil, nil, "", "", zodNumberMax(100)
		}
		pageSize = &value
	}
	if raw, exists := firstQueryValue(query, "keyword"); exists {
		if len(query["keyword"]) > 1 {
			return nil, nil, "", "", zodInvalidType("string", []any{})
		}
		keyword = strings.TrimSpace(raw)
	}
	if raw, exists := firstQueryValue(query, "status"); exists {
		if len(query["status"]) > 1 {
			return nil, nil, "", "", zodInvalidType("string", []any{})
		}
		if raw != "all" && raw != "active" && raw != "disabled" {
			return nil, nil, "", "", zodEnumMessage([]string{"all", "active", "disabled"}, raw)
		}
		status = raw
	}
	return page, pageSize, keyword, status, ""
}

// coerceQueryNumber mirrors z.coerce.number: blank text coerces to 0 (fails
// min(1)); non-numeric text is NaN ("Expected number, received nan").
func coerceQueryNumber(values []string) (float64, string) {
	if len(values) > 1 {
		return 0, "Expected number, received nan"
	}
	text := strings.TrimSpace(values[0])
	if text == "" {
		return 0, ""
	}
	number, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return 0, "Expected number, received nan"
	}
	return number, ""
}

// parseExternalSourceBody mirrors sourceBodySchema (strict).
func parseExternalSourceBody(body map[string]any) (externalSourceInput, string) {
	input := externalSourceInput{}
	raw, present := body["name"]
	if !present {
		return input, zodRequired
	}
	name, isString := raw.(string)
	if !isString {
		return input, zodInvalidType("string", raw)
	}
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return input, "来源系统名称不能为空"
	}
	if runeLen(trimmed) > 80 {
		return input, "来源系统名称不能超过 80 个字符"
	}
	input.Name = trimmed
	if value, exists := body["status"]; exists && value != nil {
		text, isString := value.(string)
		if !isString {
			return input, zodInvalidType("string", value)
		}
		if text != "active" && text != "disabled" {
			return input, zodEnumMessage([]string{"active", "disabled"}, text)
		}
		input.Status = text
	}
	if value, exists := body["scopes"]; exists && value != nil {
		items, isList := value.([]any)
		if !isList {
			return input, zodInvalidType("array", value)
		}
		for _, item := range items {
			text, isString := item.(string)
			if !isString {
				return input, zodInvalidType("string", item)
			}
			if strings.TrimSpace(text) == "" {
				return input, zodStringMin(1)
			}
		}
		input.Scopes = value
	}
	if value, exists := body["rateLimits"]; exists && value != nil {
		items, isList := value.([]any)
		if !isList {
			return input, zodInvalidType("array", value)
		}
		if len(items) > 8 {
			return input, "限频规则最多 8 条"
		}
		for _, item := range items {
			if message := validateExternalRateLimitRule(item); message != "" {
				return input, message
			}
		}
		input.RateLimits = value
	}
	if value, exists := body["expiresAt"]; exists && value != nil {
		if _, isString := value.(string); !isString {
			return input, "过期时间无效"
		}
		if _, ok := canonicalRFC3339Millis(value.(string)); !ok {
			return input, "过期时间无效"
		}
		input.ExpiresAt = value
	}
	if value, exists := body["notes"]; exists && value != nil {
		text, isString := value.(string)
		if !isString {
			return input, zodInvalidType("string", value)
		}
		if runeLen(strings.TrimSpace(text)) > 500 {
			return input, "备注不能超过 500 个字符"
		}
		input.Notes = value
	}
	if message := externalUnknownBodyKey(body, externalSourceBodyKeys); message != "" {
		return input, message
	}
	return input, ""
}

var externalSourceBodyKeys = []string{"name", "status", "scopes", "rateLimits", "expiresAt", "notes"}

// validateExternalRateLimitRule mirrors rateLimitRuleSchema (strict).
func validateExternalRateLimitRule(item any) string {
	record, isObject := item.(map[string]any)
	if !isObject {
		return zodInvalidType("object", item)
	}
	unknown := []string{}
	for key := range record {
		if !containsString(externalRateLimitRuleKeys, key) {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		return zodUnrecognizedKeys(unknown)
	}
	window, ok := record["windowSeconds"].(float64)
	if !ok {
		return zodInvalidType("number", record["windowSeconds"])
	}
	if window != float64(int64(window)) {
		return "Expected integer, received float"
	}
	if int(window) < 1 {
		return "限频窗口不能小于 1 秒"
	}
	if int(window) > 86400 {
		return "限频窗口不能超过 86400 秒"
	}
	maxRequests, ok := record["maxRequests"].(float64)
	if !ok {
		return zodInvalidType("number", record["maxRequests"])
	}
	if maxRequests != float64(int64(maxRequests)) {
		return "Expected integer, received float"
	}
	if int(maxRequests) < 1 {
		return "限频次数不能小于 1"
	}
	if int(maxRequests) > 100000 {
		return "限频次数不能超过 100000"
	}
	return ""
}

func externalUnknownBodyKey(body map[string]any, allowed []string) string {
	unknown := []string{}
	for key := range body {
		if !containsString(allowed, key) {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) == 0 {
		return ""
	}
	return zodUnrecognizedKeys(unknown)
}

// parseExternalSourceUpdateBody mirrors sourceUpdateBodySchema.
func parseExternalSourceUpdateBody(body map[string]any) (externalSourceUpdateInput, string) {
	input := externalSourceUpdateInput{SetFields: map[string]bool{}}
	raw, present := body["expectedUpdatedAt"]
	if !present {
		return input, zodRequired
	}
	expected, isString := raw.(string)
	if !isString {
		return input, zodInvalidType("string", raw)
	}
	canonical, ok := canonicalRFC3339Millis(expected)
	if !ok {
		return input, "外部来源配置版本格式不正确"
	}
	input.ExpectedUpdatedAt = canonical
	if value, exists := body["name"]; exists && value != nil {
		text, isString := value.(string)
		if !isString {
			return input, zodInvalidType("string", value)
		}
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			return input, "来源系统名称不能为空"
		}
		if runeLen(trimmed) > 80 {
			return input, "来源系统名称不能超过 80 个字符"
		}
		input.Name = trimmed
		input.SetFields["name"] = true
	}
	if value, exists := body["status"]; exists && value != nil {
		text, isString := value.(string)
		if !isString {
			return input, zodInvalidType("string", value)
		}
		if text != "active" && text != "disabled" {
			return input, zodEnumMessage([]string{"active", "disabled"}, text)
		}
		input.Status = text
		input.SetFields["status"] = true
	}
	if value, exists := body["scopes"]; exists && value != nil {
		items, isList := value.([]any)
		if !isList {
			return input, zodInvalidType("array", value)
		}
		for _, item := range items {
			text, isString := item.(string)
			if !isString {
				return input, zodInvalidType("string", item)
			}
			if strings.TrimSpace(text) == "" {
				return input, zodStringMin(1)
			}
		}
		input.Scopes = value
		input.SetFields["scopes"] = true
	}
	if value, exists := body["rateLimits"]; exists && value != nil {
		items, isList := value.([]any)
		if !isList {
			return input, zodInvalidType("array", value)
		}
		if len(items) > 8 {
			return input, "限频规则最多 8 条"
		}
		for _, item := range items {
			if message := validateExternalRateLimitRule(item); message != "" {
				return input, message
			}
		}
		input.RateLimits = value
		input.SetFields["rateLimits"] = true
	}
	if value, exists := body["expiresAt"]; exists {
		if value == nil {
			input.ExpiresAt = nil
			input.SetFields["expiresAt"] = true
		} else {
			text, isString := value.(string)
			if !isString {
				return input, "过期时间无效"
			}
			canonical, ok := canonicalRFC3339Millis(text)
			if !ok {
				return input, "过期时间无效"
			}
			input.ExpiresAt = canonical
			input.SetFields["expiresAt"] = true
		}
	}
	if value, exists := body["notes"]; exists {
		if value == nil {
			input.Notes = nil
			input.SetFields["notes"] = true
		} else {
			text, isString := value.(string)
			if !isString {
				return input, zodInvalidType("string", value)
			}
			if runeLen(strings.TrimSpace(text)) > 500 {
				return input, "备注不能超过 500 个字符"
			}
			input.Notes = text
			input.SetFields["notes"] = true
		}
	}
	if message := externalUnknownBodyKey(body, append(externalSourceBodyKeys, "expectedUpdatedAt")); message != "" {
		return input, message
	}
	hasChange := false
	for _, field := range externalSourceBodyKeys {
		if input.SetFields[field] {
			hasChange = true
			break
		}
	}
	if !hasChange {
		return input, "请提供要修改的来源配置字段"
	}
	return input, ""
}

// parseExternalDeleteBody mirrors sourceDeleteBodySchema (strict).
func parseExternalDeleteBody(body map[string]any) (string, string) {
	raw, present := body["expectedUpdatedAt"]
	if !present {
		return "", zodRequired
	}
	text, isString := raw.(string)
	if !isString {
		return "", zodInvalidType("string", raw)
	}
	canonical, ok := canonicalRFC3339Millis(text)
	if !ok {
		return "", "外部来源配置版本格式不正确"
	}
	if message := externalUnknownBodyKey(body, []string{"expectedUpdatedAt"}); message != "" {
		return "", message
	}
	return canonical, ""
}

// parseExternalTokenBody mirrors tokenBodySchema (strict).
func parseExternalTokenBody(body map[string]any) (externalTokenInput, string) {
	input := externalTokenInput{}
	raw, present := body["name"]
	if !present {
		return input, zodRequired
	}
	name, isString := raw.(string)
	if !isString {
		return input, zodInvalidType("string", raw)
	}
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return input, "Token 名称不能为空"
	}
	if runeLen(trimmed) > 80 {
		return input, "Token 名称不能超过 80 个字符"
	}
	input.Name = trimmed
	if value, exists := body["status"]; exists && value != nil {
		text, isString := value.(string)
		if !isString {
			return input, zodInvalidType("string", value)
		}
		if text != "active" && text != "disabled" && text != "revoked" {
			return input, zodEnumMessage([]string{"active", "disabled", "revoked"}, text)
		}
		input.Status = text
	}
	if value, exists := body["scopes"]; exists && value != nil {
		items, isList := value.([]any)
		if !isList {
			return input, zodInvalidType("array", value)
		}
		for _, item := range items {
			text, isString := item.(string)
			if !isString {
				return input, zodInvalidType("string", item)
			}
			if strings.TrimSpace(text) == "" {
				return input, zodStringMin(1)
			}
		}
		input.Scopes = value
	}
	if value, exists := body["expiresAt"]; exists && value != nil {
		if _, isString := value.(string); !isString {
			return input, "过期时间无效"
		}
		if _, ok := canonicalRFC3339Millis(value.(string)); !ok {
			return input, "过期时间无效"
		}
		input.ExpiresAt = value
	}
	if message := externalUnknownBodyKey(body, []string{"name", "status", "scopes", "expiresAt"}); message != "" {
		return input, message
	}
	return input, ""
}

// parseExternalTokenUpdateBody mirrors tokenUpdateBodySchema.
func parseExternalTokenUpdateBody(body map[string]any) (externalTokenUpdateInput, string) {
	input := externalTokenUpdateInput{SetFields: map[string]bool{}}
	raw, present := body["expectedUpdatedAt"]
	if !present {
		return input, zodRequired
	}
	expected, isString := raw.(string)
	if !isString {
		return input, zodInvalidType("string", raw)
	}
	canonical, ok := canonicalRFC3339Millis(expected)
	if !ok {
		return input, "外部来源配置版本格式不正确"
	}
	input.ExpectedUpdatedAt = canonical
	hasChange := false
	if value, exists := body["name"]; exists && value != nil {
		text, isString := value.(string)
		if !isString {
			return input, zodInvalidType("string", value)
		}
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			return input, "Token 名称不能为空"
		}
		if runeLen(trimmed) > 80 {
			return input, "Token 名称不能超过 80 个字符"
		}
		input.Name = trimmed
		input.SetFields["name"] = true
		hasChange = true
	}
	if value, exists := body["status"]; exists && value != nil {
		text, isString := value.(string)
		if !isString {
			return input, zodInvalidType("string", value)
		}
		if text != "active" && text != "disabled" && text != "revoked" {
			return input, zodEnumMessage([]string{"active", "disabled", "revoked"}, text)
		}
		input.Status = text
		input.SetFields["status"] = true
		hasChange = true
	}
	if value, exists := body["scopes"]; exists && value != nil {
		items, isList := value.([]any)
		if !isList {
			return input, zodInvalidType("array", value)
		}
		for _, item := range items {
			text, isString := item.(string)
			if !isString {
				return input, zodInvalidType("string", item)
			}
			if strings.TrimSpace(text) == "" {
				return input, zodStringMin(1)
			}
		}
		input.Scopes = value
		input.SetFields["scopes"] = true
		hasChange = true
	}
	if value, exists := body["expiresAt"]; exists {
		if value == nil {
			input.ExpiresAt = nil
			input.SetFields["expiresAt"] = true
			hasChange = true
		} else {
			text, isString := value.(string)
			if !isString {
				return input, "过期时间无效"
			}
			canonical, ok := canonicalRFC3339Millis(text)
			if !ok {
				return input, "过期时间无效"
			}
			input.ExpiresAt = canonical
			input.SetFields["expiresAt"] = true
			hasChange = true
		}
	}
	if message := externalUnknownBodyKey(body, []string{"expectedUpdatedAt", "name", "status", "scopes", "expiresAt"}); message != "" {
		return input, message
	}
	if !hasChange {
		return input, "请提供要修改的 Token 字段"
	}
	return input, ""
}

// inspection_parse.go mirrors the M16a zod schemas: provider options query
// parsing and the strict create/patch body validators.
package policyreads

import (
	"strings"
)

// ---------------------------------------------------------------------------
// M16a zod schema mirrors (zod v3 messages).
// ---------------------------------------------------------------------------

// parseInspectionProviderOptionsQuery mirrors providerOptionsQuerySchema.
func parseInspectionProviderOptionsQuery(query map[string][]string) (protocolCode, scopeType, keyword, message string) {
	for key := range query {
		switch key {
		case "protocolCode", "scopeType", "keyword":
		default:
			return "", "", "", zodUnrecognizedKeys([]string{key})
		}
	}
	rawProtocol, hasProtocol := firstQueryValue(query, "protocolCode")
	if !hasProtocol {
		return "", "", "", "请选择响应检查策略协议"
	}
	if len(query["protocolCode"]) > 1 {
		return "", "", "", "响应检查策略协议无效"
	}
	if !inspectionSupportedProtocol(rawProtocol) {
		return "", "", "", zodEnumMessage(inspectionProtocolCodes, rawProtocol)
	}
	rawScope, hasScope := firstQueryValue(query, "scopeType")
	if !hasScope {
		return "", "", "", zodRequired
	}
	if len(query["scopeType"]) > 1 {
		return "", "", "", zodInvalidType("string", []any{})
	}
	if rawScope != "provider" && rawScope != "protocol" {
		return "", "", "", zodEnumMessage([]string{"provider", "protocol"}, rawScope)
	}
	keywordValue, hasKeyword := firstQueryValue(query, "keyword")
	if hasKeyword {
		if len(query["keyword"]) > 1 {
			return "", "", "", zodInvalidType("string", []any{})
		}
		trimmed := strings.TrimSpace(keywordValue)
		if runeLen(trimmed) > 80 {
			return "", "", "", zodStringMax(80)
		}
		keyword = trimmed
	}
	return rawProtocol, rawScope, keyword, ""
}

// parseInspectionCreateBody mirrors policyBodySchema (shape issues in
// definition order, then unknown keys, then superRefine issues).
func parseInspectionCreateBody(body map[string]any) (*InspectionCreateInput, string) {
	input := &InspectionCreateInput{}
	// name: z.string().trim().min(1, '规则名称不能为空').max(100, '...')
	raw, present := body["name"]
	if !present {
		return nil, zodRequired
	}
	name, isString := raw.(string)
	if !isString {
		return nil, zodInvalidType("string", raw)
	}
	input.Name = strings.TrimSpace(name)
	if input.Name == "" {
		return nil, "规则名称不能为空"
	}
	if runeLen(input.Name) > 100 {
		return nil, "规则名称不能超过 100 个字符"
	}
	// enabled: z.boolean().optional()
	if value, exists := body["enabled"]; exists && value != nil {
		enabled, isBool := value.(bool)
		if !isBool {
			return nil, zodInvalidType("boolean", value)
		}
		input.Enabled = &enabled
	}
	// priority: z.number().int().min(1).max(9999).optional()
	if value, exists := body["priority"]; exists && value != nil {
		number, isNumber := value.(float64)
		if !isNumber {
			return nil, zodInvalidType("number", value)
		}
		if number != float64(int64(number)) {
			return nil, "Expected integer, received float"
		}
		intValue := int(number)
		if intValue < 1 {
			return nil, zodNumberMin(1)
		}
		if intValue > 9999 {
			return nil, zodNumberMax(9999)
		}
		input.Priority = &intValue
	}
	// scopeType: z.enum(['protocol','provider'], {required_error, invalid_type_error})
	raw, present = body["scopeType"]
	if !present {
		return nil, "请选择响应检查策略作用层级"
	}
	scopeText, isString := raw.(string)
	if !isString {
		return nil, "响应检查策略作用层级无效"
	}
	if scopeText != "protocol" && scopeText != "provider" {
		return nil, zodEnumMessage([]string{"protocol", "provider"}, scopeText)
	}
	input.ScopeType = scopeText
	// protocolCode: z.enum([...], {required_error, invalid_type_error})
	raw, present = body["protocolCode"]
	if !present {
		return nil, "请选择响应检查策略协议"
	}
	protocolText, isString := raw.(string)
	if !isString {
		return nil, "响应检查策略协议无效"
	}
	if !inspectionSupportedProtocol(protocolText) {
		return nil, zodEnumMessage(inspectionProtocolCodes, protocolText)
	}
	input.ProtocolCode = protocolText
	// providerCode: z.string().trim().min(1, ...).max(80, ...).nullable().optional()
	if value, exists := body["providerCode"]; exists && value != nil {
		text, isString := value.(string)
		if !isString {
			return nil, zodInvalidType("string", value)
		}
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			return nil, "请选择供应商"
		}
		if runeLen(trimmed) > 80 {
			return nil, "供应商编码不能超过 80 个字符"
		}
		input.ProviderCode = &trimmed
	}
	// match: matchSchema.optional()
	if value, exists := body["match"]; exists && value != nil {
		message := validateInspectionMatchSchema(value)
		if message != "" {
			return nil, message
		}
		input.Match = value
	} else {
		input.Match = map[string]any{}
	}
	// action: z.enum([...])
	raw, present = body["action"]
	if !present {
		return nil, zodRequired
	}
	actionText, isString := raw.(string)
	if !isString {
		return nil, zodInvalidType("string", raw)
	}
	if !containsString(inspectionPolicyActions, actionText) {
		return nil, zodEnumMessage(inspectionPolicyActions, actionText)
	}
	input.Action = actionText
	// notes: z.string().trim().max(1000, ...).nullable().optional()
	if value, exists := body["notes"]; exists && value != nil {
		text, isString := value.(string)
		if !isString {
			return nil, zodInvalidType("string", value)
		}
		trimmed := strings.TrimSpace(text)
		if runeLen(trimmed) > 1000 {
			return nil, "备注不能超过 1000 个字符"
		}
		input.Notes = &trimmed
	}
	// strict(): unknown keys after the shape pass.
	if message := inspectionUnknownBodyKey(body, inspectionCreateBodyKeys); message != "" {
		return nil, message
	}
	// superRefine.
	if input.ScopeType == "protocol" && input.ProviderCode != nil && *input.ProviderCode != "" {
		return nil, "协议层响应检查策略不能绑定供应商"
	}
	if input.ScopeType == "provider" && (input.ProviderCode == nil || *input.ProviderCode == "") {
		return nil, "供应商层响应检查策略必须选择供应商"
	}
	hasMatcher := false
	matchMap, _ := input.Match.(map[string]any)
	for _, key := range inspectionPositiveMatchKeys {
		items, _ := asStringList(matchMap[key])
		if len(items) > 0 {
			hasMatcher = true
			break
		}
	}
	if !hasMatcher {
		return nil, "至少需要填写一个匹配条件"
	}
	return input, ""
}

// validateInspectionMatchSchema mirrors matchSchema (strict + partial).
func validateInspectionMatchSchema(value any) string {
	match, isObject := value.(map[string]any)
	if !isObject {
		return zodInvalidType("object", value)
	}
	for key := range match {
		switch key {
		case "clientProfiles", "outputTextIncludes", "outputTextExcludes", "errorCodes",
			"errorTypes", "errorMessageIncludes", "finishReasons", "jsonPathsExists", "rawTextIncludes":
		default:
			return zodUnrecognizedKeys([]string{key})
		}
	}
	for _, key := range inspectionMatchKeys {
		raw, exists := match[key]
		if !exists || raw == nil {
			continue
		}
		items, isList := raw.([]any)
		if !isList {
			return zodInvalidType("array", raw)
		}
		maxItems := 50
		if key == "clientProfiles" {
			maxItems = 6
		}
		for _, item := range items {
			text, isString := item.(string)
			if !isString {
				return zodInvalidType("string", item)
			}
			trimmed := strings.TrimSpace(text)
			if trimmed == "" {
				return zodStringMin(1)
			}
			if runeLen(trimmed) > 200 {
				return zodStringMax(200)
			}
			if key == "clientProfiles" && !containsString(inspectionClientProfiles, trimmed) {
				return zodEnumMessage(inspectionClientProfiles, trimmed)
			}
		}
		if len(items) > maxItems {
			return zodArrayMax(maxItems)
		}
	}
	return ""
}

var inspectionCreateBodyKeys = []string{"name", "enabled", "priority", "scopeType", "protocolCode", "providerCode", "match", "action", "notes"}

func inspectionUnknownBodyKey(body map[string]any, allowed []string) string {
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

// parseInspectionPatchBody mirrors policyPatchSchema (strict + refine).
func parseInspectionPatchBody(body map[string]any) (*InspectionPatch, string) {
	patch := &InspectionPatch{SetFields: map[string]bool{}}
	hasChange := false
	// expectedUpdatedAt: rfc3339InstantSchema('响应检查策略版本无效')
	raw, present := body["expectedUpdatedAt"]
	if !present {
		return nil, zodRequired
	}
	expectedText, isString := raw.(string)
	if !isString {
		return nil, zodInvalidType("string", raw)
	}
	canonical, ok := canonicalRFC3339Millis(expectedText)
	if !ok {
		return nil, "响应检查策略版本无效"
	}
	patch.ExpectedAt = canonical
	// name
	if value, exists := body["name"]; exists && value != nil {
		text, isString := value.(string)
		if !isString {
			return nil, zodInvalidType("string", value)
		}
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			return nil, "规则名称不能为空"
		}
		if runeLen(trimmed) > 100 {
			return nil, "规则名称不能超过 100 个字符"
		}
		patch.Name = &trimmed
		patch.SetFields["name"] = true
		hasChange = true
	}
	// enabled
	if value, exists := body["enabled"]; exists && value != nil {
		enabled, isBool := value.(bool)
		if !isBool {
			return nil, zodInvalidType("boolean", value)
		}
		patch.Enabled = &enabled
		patch.SetFields["enabled"] = true
		hasChange = true
	}
	// priority
	if value, exists := body["priority"]; exists && value != nil {
		number, isNumber := value.(float64)
		if !isNumber {
			return nil, zodInvalidType("number", value)
		}
		if number != float64(int64(number)) {
			return nil, "Expected integer, received float"
		}
		intValue := int(number)
		if intValue < 1 {
			return nil, zodNumberMin(1)
		}
		if intValue > 9999 {
			return nil, zodNumberMax(9999)
		}
		patch.Priority = &intValue
		patch.SetFields["priority"] = true
		hasChange = true
	}
	// scopeType: default enum messages on patch
	if value, exists := body["scopeType"]; exists && value != nil {
		text, isString := value.(string)
		if !isString {
			return nil, zodInvalidType("string", value)
		}
		if text != "protocol" && text != "provider" {
			return nil, zodEnumMessage([]string{"protocol", "provider"}, text)
		}
		patch.ScopeType = &text
		patch.SetFields["scopeType"] = true
		hasChange = true
	}
	// protocolCode: default enum messages on patch
	if value, exists := body["protocolCode"]; exists && value != nil {
		text, isString := value.(string)
		if !isString {
			return nil, zodInvalidType("string", value)
		}
		if !inspectionSupportedProtocol(text) {
			return nil, zodEnumMessage(inspectionProtocolCodes, text)
		}
		patch.ProtocolCode = &text
		patch.SetFields["protocolCode"] = true
		hasChange = true
	}
	// providerCode: nullable optional
	if value, exists := body["providerCode"]; exists {
		if value == nil {
			patch.ProviderCode = nil
			patch.SetFields["providerCode"] = true
			hasChange = true
		} else {
			text, isString := value.(string)
			if !isString {
				return nil, zodInvalidType("string", value)
			}
			trimmed := strings.TrimSpace(text)
			if trimmed == "" {
				return nil, "请选择供应商"
			}
			if runeLen(trimmed) > 80 {
				return nil, "供应商编码不能超过 80 个字符"
			}
			patch.ProviderCode = trimmed
			patch.SetFields["providerCode"] = true
			hasChange = true
		}
	}
	// match
	if value, exists := body["match"]; exists && value != nil {
		if message := validateInspectionMatchSchema(value); message != "" {
			return nil, message
		}
		patch.Match = value
		patch.SetFields["match"] = true
		hasChange = true
	}
	// action: default enum messages on patch
	if value, exists := body["action"]; exists && value != nil {
		text, isString := value.(string)
		if !isString {
			return nil, zodInvalidType("string", value)
		}
		if !containsString(inspectionPolicyActions, text) {
			return nil, zodEnumMessage(inspectionPolicyActions, text)
		}
		patch.Action = &text
		patch.SetFields["action"] = true
		hasChange = true
	}
	// notes: nullable optional
	if value, exists := body["notes"]; exists {
		if value == nil {
			patch.Notes = nil
			patch.SetFields["notes"] = true
			hasChange = true
		} else {
			text, isString := value.(string)
			if !isString {
				return nil, zodInvalidType("string", value)
			}
			trimmed := strings.TrimSpace(text)
			if runeLen(trimmed) > 1000 {
				return nil, "备注不能超过 1000 个字符"
			}
			patch.Notes = trimmed
			patch.SetFields["notes"] = true
			hasChange = true
		}
	}
	// strict(): unknown keys after the shape pass (expectedUpdatedAt included).
	if message := inspectionUnknownBodyKey(body, append(inspectionCreateBodyKeys, "expectedUpdatedAt")); message != "" {
		return nil, message
	}
	// refine: at least one field beyond expectedUpdatedAt.
	if !hasChange {
		return nil, "至少需要提交一个变化字段"
	}
	return patch, ""
}

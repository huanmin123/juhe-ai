package accounts

import (
	"fmt"
	"strings"
)

// Account error-handling policy validation: the port of
// backend/src/modules/accounts/account-error-policy-validation.ts and
// account-error-policy-system-rules.ts restricted to the write-path
// normalization (the runtime matcher systemInsufficientQuotaRuleMatches and
// effectiveAccountErrorHandlingRules belong to the circuit dispatch slices).

type errorHandlingRule struct {
	enabled         bool
	name            string
	priority        float64
	action          string
	statusCodes     []any
	errorCodes      []any
	errorTypes      []any
	keywords        []any
	resetStrategy   string
	durationHours   float64
	dailyResetHour  float64
	weeklyResetDay  float64
	weeklyResetHour float64
	description     string
}

// normalizeAccountErrorHandlingRules mirrors normalizeAccountErrorHandlingRules:
// undefined normalizes to an empty list, arrays of strict rule objects.
func normalizeAccountErrorHandlingRules(value any) ([]any, error) {
	if value == nil {
		return []any{}, nil
	}
	list, ok := value.([]any)
	if !ok {
		return nil, &ValidationError{Message: "错误处理策略规则格式无效"}
	}
	output := []any{}
	for index, item := range list {
		rule, err := normalizeAccountErrorHandlingRule(item, index+1)
		if err != nil {
			return nil, err
		}
		output = append(output, rule)
	}
	return output, nil
}

func normalizeAccountErrorHandlingRule(value any, index int) (map[string]any, error) {
	record, ok := value.(map[string]any)
	if !ok {
		return nil, &ValidationError{Message: fmt.Sprintf("第 %d 条错误处理策略规则格式无效", index)}
	}
	if textEqual(record["source"], "system") || textEqual(record["inherited"], true) || textEqual(record["editable"], false) {
		return nil, &ValidationError{Message: fmt.Sprintf("第 %d 条错误处理策略规则不能写入系统继承规则", index)}
	}
	allowed := map[string]bool{
		"enabled": true, "name": true, "priority": true, "status_codes": true,
		"error_codes": true, "error_types": true, "keywords": true, "action": true,
		"reset_strategy": true, "duration_hours": true, "daily_reset_hour": true,
		"weekly_reset_day": true, "weekly_reset_hour": true, "description": true,
	}
	for key := range record {
		if !allowed[key] {
			return nil, &ValidationError{Message: fmt.Sprintf("第 %d 条错误处理策略规则包含不支持字段：%s", index, key)}
		}
	}
	enabled, err := requiredRuleBoolean(record["enabled"], fmt.Sprintf("第 %d 条规则启用状态", index))
	if err != nil {
		return nil, err
	}
	name, err := requiredRuleString(record["name"], fmt.Sprintf("第 %d 条规则名称", index))
	if err != nil {
		return nil, err
	}
	priority, err := requiredRulePositiveInteger(record["priority"], fmt.Sprintf("第 %d 条规则优先级", index))
	if err != nil {
		return nil, err
	}
	action, err := requiredRuleAction(record["action"], index)
	if err != nil {
		return nil, err
	}
	statusCodes, err := optionalRuleStatusCodes(record["status_codes"], index)
	if err != nil {
		return nil, err
	}
	errorCodes, err := optionalRuleErrorCodeList(record["error_codes"], fmt.Sprintf("第 %d 条规则错误码", index))
	if err != nil {
		return nil, err
	}
	errorTypes, err := optionalRuleStringList(record["error_types"], fmt.Sprintf("第 %d 条规则错误类型", index))
	if err != nil {
		return nil, err
	}
	keywords, err := optionalRuleStringList(record["keywords"], fmt.Sprintf("第 %d 条规则关键字", index))
	if err != nil {
		return nil, err
	}
	description, err := optionalRuleText(record["description"], fmt.Sprintf("第 %d 条规则描述", index))
	if err != nil {
		return nil, err
	}
	if enabled && len(statusCodes) == 0 && len(errorCodes) == 0 && len(errorTypes) == 0 && len(keywords) == 0 {
		return nil, &ValidationError{Message: fmt.Sprintf("第 %d 条规则至少需要一个匹配条件", index)}
	}
	rule := map[string]any{
		"enabled":  enabled,
		"name":     name,
		"priority": priority,
		"action":   action,
	}
	if statusCodes != nil {
		rule["status_codes"] = statusCodes
	}
	if errorCodes != nil {
		rule["error_codes"] = errorCodes
	}
	if errorTypes != nil {
		rule["error_types"] = errorTypes
	}
	if keywords != nil {
		rule["keywords"] = keywords
	}
	if description != "" {
		rule["description"] = description
	}
	if action == "rate_limited" {
		strategy, err := requiredRuleResetStrategy(record["reset_strategy"], index)
		if err != nil {
			return nil, err
		}
		rule["reset_strategy"] = strategy
		switch strategy {
		case "duration":
			hours, err := requiredRulePositiveInteger(record["duration_hours"], fmt.Sprintf("第 %d 条限流规则恢复小时数", index))
			if err != nil {
				return nil, err
			}
			rule["duration_hours"] = hours
		case "daily":
			hour, err := requiredRuleHour(record["daily_reset_hour"], fmt.Sprintf("第 %d 条限流规则每日恢复小时", index))
			if err != nil {
				return nil, err
			}
			rule["daily_reset_hour"] = hour
		default:
			day, err := requiredRuleWeekday(record["weekly_reset_day"], fmt.Sprintf("第 %d 条限流规则每周恢复日期", index))
			if err != nil {
				return nil, err
			}
			hour, err := requiredRuleHour(record["weekly_reset_hour"], fmt.Sprintf("第 %d 条限流规则每周恢复小时", index))
			if err != nil {
				return nil, err
			}
			rule["weekly_reset_day"] = day
			rule["weekly_reset_hour"] = hour
		}
	}
	return rule, nil
}

func textEqual(value any, target any) bool {
	switch typed := value.(type) {
	case string:
		text, ok := target.(string)
		return ok && typed == text
	case bool:
		flag, ok := target.(bool)
		return ok && typed == flag
	}
	return false
}

func requiredRuleBoolean(value any, label string) (bool, error) {
	if flag, ok := value.(bool); ok {
		return flag, nil
	}
	return false, &ValidationError{Message: label + "必须是布尔值"}
}

func requiredRuleString(value any, label string) (string, error) {
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return "", &ValidationError{Message: label + "不能为空"}
	}
	return strings.TrimSpace(text), nil
}

func requiredRulePositiveInteger(value any, label string) (float64, error) {
	number, ok := value.(float64)
	if !ok || number != float64(int64(number)) || number <= 0 {
		return 0, &ValidationError{Message: label + "必须是大于 0 的整数"}
	}
	return number, nil
}

func requiredRuleHour(value any, label string) (float64, error) {
	number, ok := value.(float64)
	if !ok || number != float64(int64(number)) || number < 0 || number > 23 {
		return 0, &ValidationError{Message: label + "必须是 0-23 的整数"}
	}
	return number, nil
}

func requiredRuleWeekday(value any, label string) (float64, error) {
	number, ok := value.(float64)
	if !ok || number != float64(int64(number)) || number < 0 || number > 6 {
		return 0, &ValidationError{Message: label + "必须是 0-6 的整数"}
	}
	return number, nil
}

func requiredRuleAction(value any, index int) (string, error) {
	switch value {
	case "retry_next", "temp_unschedulable", "rate_limited", "error_disabled":
		return value.(string), nil
	}
	return "", &ValidationError{Message: fmt.Sprintf("第 %d 条规则错误处理动作无效", index)}
}

func requiredRuleResetStrategy(value any, index int) (string, error) {
	switch value {
	case "duration", "daily", "weekly":
		return value.(string), nil
	}
	return "", &ValidationError{Message: fmt.Sprintf("第 %d 条限流规则恢复策略无效", index)}
}

func optionalRuleStatusCodes(value any, index int) ([]any, error) {
	if value == nil {
		return nil, nil
	}
	list, ok := value.([]any)
	if !ok {
		return nil, &ValidationError{Message: fmt.Sprintf("第 %d 条规则状态码必须是数字数组", index)}
	}
	output := []any{}
	seen := map[float64]bool{}
	for _, item := range list {
		number, ok := item.(float64)
		if !ok || number != float64(int64(number)) || number < 100 || number > 599 {
			return nil, &ValidationError{Message: fmt.Sprintf("第 %d 条规则状态码不合法", index)}
		}
		if number >= 200 && number <= 299 {
			return nil, &ValidationError{Message: fmt.Sprintf("第 %d 条规则的状态码不能填写 2xx 成功状态码，例如 200", index)}
		}
		if seen[number] {
			continue
		}
		seen[number] = true
		output = append(output, number)
	}
	if len(output) == 0 {
		return nil, nil
	}
	return output, nil
}

func optionalRuleStringList(value any, label string) ([]any, error) {
	if value == nil {
		return nil, nil
	}
	list, ok := value.([]any)
	if !ok {
		return nil, &ValidationError{Message: label + "必须是字符串数组"}
	}
	output := []any{}
	seen := map[string]bool{}
	for _, item := range list {
		text, err := requiredRuleString(item, label)
		if err != nil {
			return nil, err
		}
		if seen[text] {
			continue
		}
		seen[text] = true
		output = append(output, text)
	}
	if len(output) == 0 {
		return nil, nil
	}
	return output, nil
}

func optionalRuleErrorCodeList(value any, label string) ([]any, error) {
	output, err := optionalRuleStringList(value, label)
	if err != nil {
		return nil, err
	}
	for _, item := range output {
		text := item.(string)
		if isAllDigits(text) {
			number := 0
			for _, digit := range text {
				number = number*10 + int(digit-'0')
			}
			if number >= 200 && number <= 299 {
				return nil, &ValidationError{Message: label + "不能填写 2xx 成功码，例如 200"}
			}
		}
	}
	return output, nil
}

func isAllDigits(text string) bool {
	if text == "" {
		return false
	}
	for _, r := range text {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func optionalRuleText(value any, label string) (string, error) {
	if value == nil {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", &ValidationError{Message: label + "必须是字符串"}
	}
	return strings.TrimSpace(text), nil
}

// ---- error policy overrides (account-error-policy-system-rules.ts) ----

const systemInsufficientQuotaErrorPolicyRuleID = "system.upstream_insufficient_quota"

// normalizeAccountErrorPolicyOverrides mirrors normalizeAccountErrorPolicyOverrides.
func normalizeAccountErrorPolicyOverrides(value any) ([]any, error) {
	if value == nil {
		return []any{}, nil
	}
	list, ok := value.([]any)
	if !ok {
		return nil, &ValidationError{Message: "错误处理策略覆盖格式无效"}
	}
	output := []any{}
	for index, item := range list {
		record, ok := item.(map[string]any)
		if !ok {
			return nil, &ValidationError{Message: fmt.Sprintf("第 %d 条错误处理策略覆盖格式无效", index+1)}
		}
		if !textEqual(record["system_rule_id"], systemInsufficientQuotaErrorPolicyRuleID) {
			return nil, &ValidationError{Message: fmt.Sprintf("第 %d 条错误处理策略覆盖的系统规则 ID 无效", index+1)}
		}
		action, _ := record["action"].(string)
		if action != "replace" && action != "delete" {
			return nil, &ValidationError{Message: fmt.Sprintf("第 %d 条错误处理策略覆盖动作无效", index+1)}
		}
		allowed := map[string]bool{"system_rule_id": true, "action": true}
		if action == "replace" {
			allowed["rule_index"] = true
		}
		for key := range record {
			if !allowed[key] {
				return nil, &ValidationError{Message: fmt.Sprintf("第 %d 条错误处理策略覆盖包含不支持字段：%s", index+1, key)}
			}
		}
		if action == "replace" {
			ruleIndex, ok := record["rule_index"].(float64)
			if !ok || ruleIndex != float64(int64(ruleIndex)) || ruleIndex < 0 {
				return nil, &ValidationError{Message: fmt.Sprintf("第 %d 条错误处理策略覆盖规则索引无效", index+1)}
			}
			output = append(output, map[string]any{
				"system_rule_id": systemInsufficientQuotaErrorPolicyRuleID,
				"action":         "replace",
				"rule_index":     ruleIndex,
			})
			continue
		}
		output = append(output, map[string]any{
			"system_rule_id": systemInsufficientQuotaErrorPolicyRuleID,
			"action":         "delete",
		})
	}
	return output, nil
}

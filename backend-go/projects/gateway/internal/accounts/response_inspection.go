package accounts

import (
	"fmt"
	"strings"
)

// Response inspection policy validation: the port of
// backend/src/modules/accounts/account-response-inspection-policy-validation.ts
// (the zod accountResponseInspectionRuleSchema as manual strict-object
// validation). The runtime match engine belongs to the gateway response
// inspection slices.

var responseInspectionClientProfiles = map[string]bool{
	"codex": true, "generic_openai": true, "claude_code": true,
	"generic_anthropic": true, "generic_gemini": true, "gemini_cli": true,
}

var responseInspectionActions = map[string]bool{
	"observe": true, "drop_event": true, "retry_no_avoidance": true,
	"retry_next_account": true, "avoid_account_ttl": true, "avoid_upstream_bucket_ttl": true,
}

const (
	responseInspectionTextMaxItems  = 50
	responseInspectionTextMaxRunes  = 200
	responseInspectionMaxRules      = 20
	responseInspectionNameMaxRunes  = 100
	responseInspectionPriorityMax   = 9999
	responseInspectionNotesMaxRunes = 1000
)

// normalizeAccountResponseInspectionRules mirrors
// normalizeAccountResponseInspectionRules.
func normalizeAccountResponseInspectionRules(value any) ([]any, error) {
	if value == nil {
		return []any{}, nil
	}
	list, ok := value.([]any)
	if !ok {
		return nil, &ValidationError{Message: "账户响应检查规则必须是数组"}
	}
	if len(list) > responseInspectionMaxRules {
		return nil, &ValidationError{Message: "账户响应检查规则不能超过 20 条"}
	}
	output := []any{}
	for index, item := range list {
		ruleIndex := index + 1
		rule, err := normalizeResponseInspectionRule(item)
		if err != nil {
			return nil, &ValidationError{Message: fmt.Sprintf("第 %d 条响应检查规则参数无效", ruleIndex)}
		}
		if rule["enabled"] != false && !responseInspectionRuleHasMatcher(rule) {
			return nil, &ValidationError{Message: fmt.Sprintf("第 %d 条响应检查规则至少需要一个匹配条件", ruleIndex)}
		}
		output = append(output, rule)
	}
	return output, nil
}

func normalizeResponseInspectionRule(value any) (map[string]any, error) {
	record, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("rule must be an object")
	}
	allowed := map[string]bool{"enabled": true, "name": true, "priority": true, "match": true, "action": true, "notes": true}
	for key := range record {
		if !allowed[key] {
			return nil, fmt.Errorf("unsupported key %s", key)
		}
	}
	enabled, ok := record["enabled"].(bool)
	if !ok {
		return nil, fmt.Errorf("enabled must be boolean")
	}
	nameText, ok := record["name"].(string)
	if !ok {
		return nil, fmt.Errorf("name must be string")
	}
	name := strings.TrimSpace(nameText)
	if name == "" || len([]rune(name)) > responseInspectionNameMaxRunes {
		return nil, fmt.Errorf("name out of range")
	}
	priority, ok := record["priority"].(float64)
	if !ok || priority != float64(int64(priority)) || priority < 1 || priority > responseInspectionPriorityMax {
		return nil, fmt.Errorf("priority out of range")
	}
	match, err := normalizeResponseInspectionMatch(record["match"])
	if err != nil {
		return nil, err
	}
	action, ok := record["action"].(string)
	if !ok || !responseInspectionActions[action] {
		return nil, fmt.Errorf("action invalid")
	}
	rule := map[string]any{
		"enabled":  enabled,
		"name":     name,
		"priority": priority,
		"match":    match,
		"action":   action,
	}
	if rawNotes, exists := record["notes"]; exists && rawNotes != nil {
		notes, ok := rawNotes.(string)
		if !ok {
			return nil, fmt.Errorf("notes must be string")
		}
		trimmed := strings.TrimSpace(notes)
		if len([]rune(trimmed)) > responseInspectionNotesMaxRunes {
			return nil, fmt.Errorf("notes out of range")
		}
		rule["notes"] = trimmed
	}
	return rule, nil
}

var responseInspectionMatchTextFields = []string{
	"outputTextIncludes", "outputTextExcludes", "errorCodes", "errorTypes",
	"errorMessageIncludes", "finishReasons", "jsonPathsExists", "rawTextIncludes",
}

func normalizeResponseInspectionMatch(value any) (map[string]any, error) {
	record, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("match must be an object")
	}
	match := map[string]any{}
	if raw, exists := record["clientProfiles"]; exists && raw != nil {
		list, ok := raw.([]any)
		if !ok {
			return nil, fmt.Errorf("clientProfiles must be array")
		}
		if len(list) > 6 {
			return nil, fmt.Errorf("clientProfiles too long")
		}
		profiles := []any{}
		for _, item := range list {
			text, ok := item.(string)
			if !ok || !responseInspectionClientProfiles[text] {
				return nil, fmt.Errorf("clientProfiles invalid")
			}
			profiles = append(profiles, text)
		}
		match["clientProfiles"] = profiles
	}
	for _, field := range responseInspectionMatchTextFields {
		if raw, exists := record[field]; exists && raw != nil {
			list, ok := raw.([]any)
			if !ok {
				return nil, fmt.Errorf("%s must be array", field)
			}
			if len(list) > responseInspectionTextMaxItems {
				return nil, fmt.Errorf("%s too long", field)
			}
			values := []any{}
			for _, item := range list {
				text, ok := item.(string)
				if !ok {
					return nil, fmt.Errorf("%s must be strings", field)
				}
				trimmed := strings.TrimSpace(text)
				if trimmed == "" || len([]rune(trimmed)) > responseInspectionTextMaxRunes {
					return nil, fmt.Errorf("%s item out of range", field)
				}
				values = append(values, trimmed)
			}
			match[field] = values
		}
	}
	for key := range record {
		known := key == "clientProfiles"
		for _, field := range responseInspectionMatchTextFields {
			if key == field {
				known = true
			}
		}
		if !known {
			return nil, fmt.Errorf("unsupported match key %s", key)
		}
	}
	return match, nil
}

func responseInspectionRuleHasMatcher(rule map[string]any) bool {
	match, _ := rule["match"].(map[string]any)
	if match == nil {
		return false
	}
	for _, field := range responseInspectionMatchTextFields {
		if list, ok := match[field].([]any); ok {
			for _, item := range list {
				if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
					return true
				}
			}
		}
	}
	return false
}

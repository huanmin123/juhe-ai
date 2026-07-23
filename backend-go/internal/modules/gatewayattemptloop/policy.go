package gatewayattemptloop

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

type PolicyAction string

const (
	PolicyActionNone      PolicyAction = "none"
	PolicyActionRetryNext PolicyAction = "retry_next"
	PolicyActionCooldown  PolicyAction = "cooldown"
	PolicyActionDisable   PolicyAction = "disable"
)

type CooldownStatus string

const (
	CooldownRateLimited          CooldownStatus = "rate_limited"
	CooldownTemporaryUnavailable CooldownStatus = "temporary_unavailable"
)

type PolicyDecision struct {
	Action         PolicyAction
	RuleName       string
	CooldownStatus CooldownStatus
	CooldownUntil  *time.Time
}

type FailureFacts struct {
	StatusCode int
	ErrorCode  string
	ErrorType  string
	BodyText   string `json:"-"`
	Message    string
}

type PolicySettings struct {
	DefaultTemporaryCooldown time.Duration
}

type policyRule struct {
	enabled         bool
	name            string
	priority        int
	action          string
	statusCodes     []int
	errorCodes      []string
	errorTypes      []string
	keywords        []string
	resetStrategy   string
	durationHours   int
	dailyResetHour  int
	weeklyResetDay  int
	weeklyResetHour int
}

func DecidePolicy(raw any, failure FailureFacts, settings PolicySettings, now time.Time) (PolicyDecision, error) {
	if failure.StatusCode >= 200 && failure.StatusCode <= 299 {
		return PolicyDecision{Action: PolicyActionNone}, nil
	}
	rules, err := normalizeRules(raw)
	if err != nil {
		return PolicyDecision{}, err
	}
	slices.SortStableFunc(rules, func(left, right policyRule) int { return left.priority - right.priority })
	for _, rule := range rules {
		if !rule.enabled || !rule.matches(failure) {
			continue
		}
		return decisionForRule(rule, settings, now), nil
	}
	return PolicyDecision{Action: PolicyActionNone}, nil
}

func (r policyRule) matches(failure FailureFacts) bool {
	return matchesInt(r.statusCodes, failure.StatusCode) &&
		matchesFold(r.errorCodes, failure.ErrorCode, false) &&
		matchesFold(r.errorTypes, failure.ErrorType, false) &&
		matchesFold(r.keywords, failure.BodyText, true)
}

func decisionForRule(rule policyRule, settings PolicySettings, now time.Time) PolicyDecision {
	decision := PolicyDecision{RuleName: rule.name}
	switch rule.action {
	case "retry_next":
		decision.Action = PolicyActionRetryNext
	case "error_disabled":
		decision.Action = PolicyActionDisable
	case "rate_limited":
		decision.Action = PolicyActionCooldown
		decision.CooldownStatus = CooldownRateLimited
		until := resetTime(rule, now)
		decision.CooldownUntil = &until
	default:
		decision.Action = PolicyActionCooldown
		decision.CooldownStatus = CooldownTemporaryUnavailable
		duration := settings.DefaultTemporaryCooldown
		if duration <= 0 {
			duration = 5 * time.Minute
		}
		until := now.Add(duration)
		decision.CooldownUntil = &until
	}
	return decision
}

func resetTime(rule policyRule, now time.Time) time.Time {
	if rule.resetStrategy == "duration" {
		hours := rule.durationHours
		if hours < 1 {
			hours = 1
		}
		return now.Add(time.Duration(hours) * time.Hour)
	}
	hour := rule.dailyResetHour
	if rule.resetStrategy == "weekly" {
		hour = rule.weeklyResetHour
	}
	target := time.Date(now.Year(), now.Month(), now.Day(), hour, 0, 0, 0, now.Location())
	if rule.resetStrategy == "weekly" {
		days := (rule.weeklyResetDay - int(target.Weekday()) + 7) % 7
		target = target.AddDate(0, 0, days)
	}
	if !target.After(now) {
		if rule.resetStrategy == "weekly" {
			target = target.AddDate(0, 0, 7)
		} else {
			target = target.AddDate(0, 0, 1)
		}
	}
	return target
}

func normalizeRules(raw any) ([]policyRule, error) {
	if raw == nil {
		return []policyRule{}, nil
	}
	values, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("account error handling rules must be an array")
	}
	if len(values) > 128 {
		return nil, fmt.Errorf("account error handling rules exceed limit")
	}
	result := make([]policyRule, 0, len(values))
	for index, value := range values {
		record, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("account error handling rule %d must be an object", index+1)
		}
		rule, err := normalizeRule(record, index+1)
		if err != nil {
			return nil, err
		}
		result = append(result, rule)
	}
	return result, nil
}

func normalizeRule(value map[string]any, index int) (policyRule, error) {
	allowed := map[string]struct{}{
		"enabled": {}, "name": {}, "priority": {}, "action": {}, "status_codes": {},
		"error_codes": {}, "error_types": {}, "keywords": {}, "reset_strategy": {},
		"duration_hours": {}, "daily_reset_hour": {}, "weekly_reset_day": {},
		"weekly_reset_hour": {}, "description": {},
	}
	for key := range value {
		if _, ok := allowed[key]; !ok {
			return policyRule{}, fmt.Errorf("account error handling rule %d contains unsupported field %q", index, key)
		}
	}
	enabled, ok := value["enabled"].(bool)
	if !ok {
		return policyRule{}, fmt.Errorf("account error handling rule %d enabled must be boolean", index)
	}
	name, ok := boundedString(value["name"], 256)
	if !ok {
		return policyRule{}, fmt.Errorf("account error handling rule %d name is invalid", index)
	}
	priority, ok := positiveInt(value["priority"])
	if !ok {
		return policyRule{}, fmt.Errorf("account error handling rule %d priority is invalid", index)
	}
	action, ok := boundedString(value["action"], 64)
	if !ok || (action != "retry_next" && action != "temp_unschedulable" && action != "rate_limited" && action != "error_disabled") {
		return policyRule{}, fmt.Errorf("account error handling rule %d action is invalid", index)
	}
	rule := policyRule{enabled: enabled, name: name, priority: priority, action: action}
	var err error
	if rule.statusCodes, err = optionalStatusCodes(value["status_codes"]); err != nil {
		return policyRule{}, fmt.Errorf("account error handling rule %d: %w", index, err)
	}
	if rule.errorCodes, err = optionalStringList(value["error_codes"]); err != nil {
		return policyRule{}, fmt.Errorf("account error handling rule %d error codes: %w", index, err)
	}
	for _, code := range rule.errorCodes {
		if allDigits(code) {
			number := 0
			for _, digit := range code {
				number = number*10 + int(digit-'0')
			}
			if number >= 200 && number <= 299 {
				return policyRule{}, fmt.Errorf("account error handling rule %d error code cannot be a 2xx status", index)
			}
		}
	}
	if rule.errorTypes, err = optionalStringList(value["error_types"]); err != nil {
		return policyRule{}, fmt.Errorf("account error handling rule %d error types: %w", index, err)
	}
	if rule.keywords, err = optionalStringList(value["keywords"]); err != nil {
		return policyRule{}, fmt.Errorf("account error handling rule %d keywords: %w", index, err)
	}
	if enabled && len(rule.statusCodes)+len(rule.errorCodes)+len(rule.errorTypes)+len(rule.keywords) == 0 {
		return policyRule{}, fmt.Errorf("account error handling rule %d requires a matcher", index)
	}
	if action == "rate_limited" {
		rule.resetStrategy, _ = boundedString(value["reset_strategy"], 32)
		switch rule.resetStrategy {
		case "duration":
			rule.durationHours, ok = positiveInt(value["duration_hours"])
		case "daily":
			rule.dailyResetHour, ok = rangedInt(value["daily_reset_hour"], 0, 23)
		case "weekly":
			rule.weeklyResetDay, ok = rangedInt(value["weekly_reset_day"], 0, 6)
			if ok {
				rule.weeklyResetHour, ok = rangedInt(value["weekly_reset_hour"], 0, 23)
			}
		default:
			ok = false
		}
		if !ok {
			return policyRule{}, fmt.Errorf("account error handling rule %d reset strategy is invalid", index)
		}
	}
	return rule, nil
}

func optionalStatusCodes(value any) ([]int, error) {
	if value == nil {
		return nil, nil
	}
	values, ok := value.([]any)
	if !ok || len(values) > 64 {
		return nil, fmt.Errorf("status codes must be a bounded array")
	}
	result := make([]int, 0, len(values))
	seen := map[int]struct{}{}
	for _, value := range values {
		code, ok := rangedInt(value, 100, 599)
		if !ok || (code >= 200 && code <= 299) {
			return nil, fmt.Errorf("status code is invalid")
		}
		if _, duplicate := seen[code]; !duplicate {
			seen[code] = struct{}{}
			result = append(result, code)
		}
	}
	return result, nil
}

func optionalStringList(value any) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	values, ok := value.([]any)
	if !ok || len(values) > 64 {
		return nil, fmt.Errorf("value must be a bounded string array")
	}
	result := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		text, ok := boundedString(value, 256)
		if !ok {
			return nil, fmt.Errorf("value is invalid")
		}
		key := strings.ToLower(text)
		if _, duplicate := seen[key]; !duplicate {
			seen[key] = struct{}{}
			result = append(result, text)
		}
	}
	return result, nil
}

func matchesInt(values []int, target int) bool {
	if len(values) == 0 {
		return true
	}
	return slices.Contains(values, target)
}

func matchesFold(values []string, target string, contains bool) bool {
	if len(values) == 0 {
		return true
	}
	target = strings.ToLower(target)
	for _, value := range values {
		value = strings.ToLower(value)
		if (!contains && value == target) || (contains && strings.Contains(target, value)) {
			return true
		}
	}
	return false
}

func boundedString(value any, limit int) (string, bool) {
	text, ok := value.(string)
	text = strings.TrimSpace(text)
	return text, ok && text != "" && len(text) <= limit
}

func positiveInt(value any) (int, bool) { return rangedInt(value, 1, 1_000_000_000) }

func rangedInt(value any, minValue, maxValue int) (int, bool) {
	var number float64
	switch typed := value.(type) {
	case float64:
		number = typed
	case int:
		number = float64(typed)
	default:
		return 0, false
	}
	if number < float64(minValue) || number > float64(maxValue) {
		return 0, false
	}
	integer := int(number)
	return integer, number == float64(integer)
}

func allDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

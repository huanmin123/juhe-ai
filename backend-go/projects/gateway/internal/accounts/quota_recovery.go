package accounts

import (
	"encoding/json"
	"strings"
	"time"
)

// Quota recovery policy normalization: the write-path port of
// backend/src/modules/accounts/quota-recovery-policy.ts
// normalizeQuotaRecoveryPolicy. The cooldown boundary computation
// (quotaRecoveryCooldownUntil / passive jitter) belongs to the balance/quota
// job slices.

const (
	quotaRecoveryFixedJitterMinutes = 15
	quotaRecoveryMaxPolicyBytes     = 4096
	quotaRecoveryMaxDurationMinutes = 7 * 24 * 60
)

// normalizeQuotaRecoveryPolicy mirrors normalizeQuotaRecoveryPolicy: an object
// keyed by account type with strict per-type schedules.
func normalizeQuotaRecoveryPolicy(value any) (map[string]any, error) {
	if value == nil {
		return map[string]any{}, nil
	}
	input, ok := value.(map[string]any)
	if !ok {
		return nil, &ValidationError{Message: "额度恢复策略必须是对象"}
	}
	allowed := map[string]bool{"api_key": true, "oauth": true, "google_oauth": true}
	for key := range input {
		if !allowed[key] {
			return nil, &ValidationError{Message: "额度恢复策略字段 " + key + " 不受支持"}
		}
	}
	output := map[string]any{}
	for _, accountType := range []string{"api_key", "oauth", "google_oauth"} {
		schedule, exists := input[accountType]
		if !exists {
			continue
		}
		normalized, err := normalizeQuotaRecoverySchedule(schedule)
		if err != nil {
			return nil, err
		}
		output[accountType] = normalized
	}
	// Node measures JSON.stringify(output).length — UTF-16 code units. The
	// stored values are ASCII-safe keys plus user-supplied timezone text, so a
	// rune count with the same cap matches for every valid policy.
	encoded := jsonEncodeLength(output)
	if encoded > quotaRecoveryMaxPolicyBytes {
		return nil, &ValidationError{Message: "额度恢复策略过大"}
	}
	return output, nil
}

func normalizeQuotaRecoverySchedule(value any) (map[string]any, error) {
	input, ok := value.(map[string]any)
	if !ok {
		return nil, &ValidationError{Message: "额度恢复策略项必须是对象"}
	}
	strategy, _ := input["reset_strategy"].(string)
	if strategy != "duration" && strategy != "daily" && strategy != "weekly" {
		return nil, &ValidationError{Message: "额度恢复策略 reset_strategy 必须是 duration、daily 或 weekly"}
	}
	output := map[string]any{"reset_strategy": strategy}
	switch strategy {
	case "duration":
		minutes, err := quotaRecoveryIntegerInRange(input["duration_minutes"], 30, quotaRecoveryMaxDurationMinutes, "duration_minutes")
		if err != nil {
			return nil, err
		}
		output["duration_minutes"] = minutes
	case "daily":
		hour, err := quotaRecoveryIntegerInRange(input["daily_reset_hour"], 0, 23, "daily_reset_hour")
		if err != nil {
			return nil, err
		}
		output["daily_reset_hour"] = hour
	default:
		day, err := quotaRecoveryIntegerInRange(input["weekly_reset_day"], 0, 6, "weekly_reset_day")
		if err != nil {
			return nil, err
		}
		hour, err := quotaRecoveryIntegerInRange(input["weekly_reset_hour"], 0, 23, "weekly_reset_hour")
		if err != nil {
			return nil, err
		}
		output["weekly_reset_day"] = day
		output["weekly_reset_hour"] = hour
	}
	jitter, exists := input["jitter_minutes"]
	if !exists {
		jitter = float64(quotaRecoveryFixedJitterMinutes)
	}
	number, ok := jitter.(float64)
	if !ok || number != float64(quotaRecoveryFixedJitterMinutes) {
		return nil, &ValidationError{Message: "额度恢复策略 jitter_minutes固定15，仅作为兼容字段"}
	}
	output["jitter_minutes"] = float64(quotaRecoveryFixedJitterMinutes)
	timezone, exists := input["timezone"]
	if !exists {
		timezone = "UTC"
	}
	text, ok := timezone.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return nil, &ValidationError{Message: "额度恢复策略 timezone 无效"}
	}
	trimmed := strings.TrimSpace(text)
	if !validQuotaRecoveryTimezone(trimmed) {
		return nil, &ValidationError{Message: "额度恢复策略 timezone 无效：" + trimmed}
	}
	output["timezone"] = trimmed
	return output, nil
}

// validQuotaRecoveryTimezone mirrors new Intl.DateTimeFormat({timeZone}) for
// the accepted shapes: IANA zone names plus the UTC/GMT offset spellings Intl
// also takes (Go LoadLocation only knows the IANA half).
func validQuotaRecoveryTimezone(name string) bool {
	if _, err := time.LoadLocation(name); err == nil {
		return true
	}
	offsetPattern := strings.ToUpper(name)
	if !strings.HasPrefix(offsetPattern, "UTC") && !strings.HasPrefix(offsetPattern, "GMT") {
		return false
	}
	rest := offsetPattern[3:]
	if rest == "" {
		return true
	}
	if rest[0] == '+' || rest[0] == '-' {
		parts := strings.Split(rest[1:], ":")
		if len(parts) > 2 {
			return false
		}
		for index, part := range parts {
			if len(part) != 2 {
				return false
			}
			for _, digit := range part {
				if digit < '0' || digit > '9' {
					return false
				}
			}
			if index == 1 && strings.Compare(part, "59") > 0 {
				return false
			}
		}
		return true
	}
	return false
}

func quotaRecoveryIntegerInRange(value any, min, max int, label string) (float64, error) {
	number, ok := value.(float64)
	if !ok || number != float64(int64(number)) || number < float64(min) || number > float64(max) {
		return 0, &ValidationError{Message: "额度恢复策略 " + label + " 必须是 " + itoa(min) + "-" + itoa(max) + " 的整数"}
	}
	return number, nil
}

// jsonEncodeLength renders the JSON byte length used for the size cap.
func jsonEncodeLength(value any) int {
	encoded, err := json.Marshal(value)
	if err != nil {
		return 0
	}
	return len(encoded)
}

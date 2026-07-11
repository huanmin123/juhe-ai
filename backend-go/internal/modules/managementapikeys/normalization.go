package managementapikeys

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"juhe-ai/backend-go/internal/modules/apikeyschedule"
	"juhe-ai/backend-go/internal/store/port"
)

var serverDateTimePattern = regexp.MustCompile(
	`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{3})?Z$`,
)

var quotaNumberPattern = regexp.MustCompile(
	`^-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?$`,
)

func normalizeMutationDescription(value any) (*string, error) {
	if value == nil {
		return nil, nil
	}
	text, ok := value.(string)
	if !ok {
		return nil, errors.New("API Key 说明必须是字符串")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil
	}
	if len(utf16.Encode([]rune(text))) > 200 {
		return nil, errors.New("API Key 说明不能超过 200 个字符")
	}
	return &text, nil
}

func normalizeMutationStatus(value string, defaultStatus string) (string, error) {
	switch strings.TrimSpace(value) {
	case "":
		if defaultStatus != "" {
			return defaultStatus, nil
		}
	case "active":
		return "active", nil
	case "disabled":
		return "disabled", nil
	}
	return "", errors.New("API Key 状态无效")
}

func normalizeMutationExpiresAt(value any) (*time.Time, error) {
	if value == nil {
		return nil, nil
	}
	text, ok := value.(string)
	if !ok {
		return nil, errors.New("API Key 过期时间必须是有效时间字符串")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil
	}
	if !serverDateTimePattern.MatchString(text) {
		return nil, errors.New("API Key 过期时间必须是有效时间字符串")
	}
	layout := "2006-01-02T15:04:05Z"
	if strings.Contains(text, ".") {
		layout = "2006-01-02T15:04:05.000Z"
	}
	parsed, err := time.Parse(layout, text)
	if err != nil {
		return nil, errors.New("API Key 过期时间必须是有效时间字符串")
	}
	parsed = parsed.UTC()
	return &parsed, nil
}

func normalizeMutationQuotaLimits(
	value any,
) (port.ManagementRequestQuotaLimits, *string, *int, error) {
	if value == nil {
		return port.ManagementRequestQuotaLimits{}, nil, nil, nil
	}
	record, ok := value.(map[string]any)
	if !ok {
		return port.ManagementRequestQuotaLimits{}, nil, nil, errors.New("请求额度限制参数无效")
	}
	allowedKeys := map[string]bool{
		"hourly":  true,
		"daily":   true,
		"weekly":  true,
		"monthly": true,
		"total":   true,
	}
	for key := range record {
		if !allowedKeys[key] {
			return port.ManagementRequestQuotaLimits{}, nil, nil,
				fmt.Errorf("请求额度限制包含不支持字段：%s", key)
		}
	}
	if len(record) == 0 {
		return port.ManagementRequestQuotaLimits{}, nil, nil, nil
	}

	normalizedJSON := make(map[string]any, len(record))
	dto := port.ManagementRequestQuotaLimits{}
	var hourlyHours *int
	for key, raw := range record {
		item, ok := raw.(map[string]any)
		if !ok {
			return port.ManagementRequestQuotaLimits{}, nil, nil,
				fmt.Errorf("%s额度参数无效", quotaLabel(key))
		}
		normalized, amount, hours, err := normalizeMutationQuotaLimit(key, item)
		if err != nil {
			return port.ManagementRequestQuotaLimits{}, nil, nil, err
		}
		normalizedJSON[key] = normalized
		limit := &port.ManagementRequestQuotaLimit{Enabled: true, Limit: amount}
		switch key {
		case "hourly":
			hourlyHours = &hours
			dto.Hourly = &port.ManagementRequestHourlyQuotaLimit{
				Enabled: true,
				Hours:   hours,
				Limit:   amount,
			}
		case "daily":
			dto.Daily = limit
		case "weekly":
			dto.Weekly = limit
		case "monthly":
			dto.Monthly = limit
		case "total":
			dto.Total = limit
		}
	}
	data, err := json.Marshal(normalizedJSON)
	if err != nil {
		return port.ManagementRequestQuotaLimits{}, nil, nil,
			fmt.Errorf("请求额度限制无法序列化: %w", err)
	}
	text := string(data)
	return dto, &text, hourlyHours, nil
}

func normalizeMutationQuotaLimit(
	key string,
	item map[string]any,
) (map[string]any, float64, int, error) {
	allowedFields := map[string]bool{"enabled": true, "limit": true}
	if key == "hourly" {
		allowedFields["hours"] = true
	}
	for field := range item {
		if !allowedFields[field] {
			return nil, 0, 0,
				fmt.Errorf("%s额度包含不支持字段：%s", quotaLabel(key), field)
		}
	}
	enabled, ok := item["enabled"].(bool)
	if !ok || !enabled {
		return nil, 0, 0, fmt.Errorf("%s启用状态必须为 true", quotaLabel(key))
	}
	number, amount, err := normalizedMutationQuotaNumber(item["limit"])
	if err != nil {
		return nil, 0, 0, fmt.Errorf("%s金额无效", quotaLabel(key))
	}
	normalized := map[string]any{"enabled": true, "limit": number}
	if key != "hourly" {
		return normalized, amount, 0, nil
	}
	hours, err := normalizedMutationQuotaHours(item["hours"])
	if err != nil {
		return nil, 0, 0, errors.New("小时额度窗口必须在 1-720 之间")
	}
	normalized["hours"] = hours
	return normalized, amount, hours, nil
}

func normalizedMutationQuotaNumber(raw any) (json.Number, float64, error) {
	text, err := mutationNumberText(raw)
	if err != nil {
		return "", 0, err
	}
	if !validMutationQuotaNumberText(text) {
		return "", 0, errors.New("invalid quota number")
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return "", 0, errors.New("invalid quota number")
	}
	return json.Number(text), value, nil
}

func validMutationQuotaNumberText(text string) bool {
	if !quotaNumberPattern.MatchString(text) || strings.HasPrefix(text, "-") {
		return false
	}

	mantissa := text
	var exponent int64
	if exponentIndex := strings.IndexAny(text, "eE"); exponentIndex >= 0 {
		mantissa = text[:exponentIndex]
		parsed, err := strconv.ParseInt(text[exponentIndex+1:], 10, 64)
		if err != nil {
			return false
		}
		exponent = parsed
	}

	integerPart := mantissa
	fractionPart := ""
	if decimalIndex := strings.IndexByte(mantissa, '.'); decimalIndex >= 0 {
		integerPart = mantissa[:decimalIndex]
		fractionPart = mantissa[decimalIndex+1:]
	}
	digits := strings.TrimLeft(integerPart+fractionPart, "0")
	if digits == "" {
		return false
	}

	digitCount := int64(len(digits))
	if exponent > digitCount+int64(len(strconv.FormatInt(maxQuotaAmount, 10))) ||
		exponent < -digitCount-6 {
		return false
	}
	scale := int64(len(fractionPart)) - exponent
	for scale > 0 && strings.HasSuffix(digits, "0") {
		digits = strings.TrimSuffix(digits, "0")
		scale--
	}
	if scale > 6 {
		return false
	}

	maxText := strconv.FormatInt(maxQuotaAmount, 10)
	if scale <= 0 {
		totalDigits := int64(len(digits)) - scale
		if totalDigits != int64(len(maxText)) {
			return totalDigits < int64(len(maxText))
		}
		candidate := digits + strings.Repeat("0", int(-scale))
		return candidate <= maxText
	}

	maxScaled := maxText + strings.Repeat("0", int(scale))
	if len(digits) != len(maxScaled) {
		return len(digits) < len(maxScaled)
	}
	return digits <= maxScaled
}

func normalizedMutationQuotaHours(raw any) (int, error) {
	text, err := mutationNumberText(raw)
	if err != nil || strings.ContainsAny(text, ".eE") {
		return 0, errors.New("invalid quota hours")
	}
	value, err := strconv.Atoi(text)
	if err != nil || value < 1 || value > 720 {
		return 0, errors.New("invalid quota hours")
	}
	return value, nil
}

func mutationNumberText(raw any) (string, error) {
	switch value := raw.(type) {
	case json.Number:
		return value.String(), nil
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return "", errors.New("invalid number")
		}
		return strconv.FormatFloat(value, 'f', -1, 64), nil
	case float32:
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return "", errors.New("invalid number")
		}
		return strconv.FormatFloat(float64(value), 'f', -1, 32), nil
	case int:
		return strconv.Itoa(value), nil
	case int8:
		return strconv.FormatInt(int64(value), 10), nil
	case int16:
		return strconv.FormatInt(int64(value), 10), nil
	case int32:
		return strconv.FormatInt(int64(value), 10), nil
	case int64:
		return strconv.FormatInt(value, 10), nil
	case uint:
		return strconv.FormatUint(uint64(value), 10), nil
	case uint8:
		return strconv.FormatUint(uint64(value), 10), nil
	case uint16:
		return strconv.FormatUint(uint64(value), 10), nil
	case uint32:
		return strconv.FormatUint(uint64(value), 10), nil
	case uint64:
		return strconv.FormatUint(value, 10), nil
	default:
		return "", errors.New("invalid number")
	}
}

func quotaLabel(key string) string {
	switch key {
	case "hourly":
		return "小时"
	case "daily":
		return "日"
	case "weekly":
		return "周"
	case "monthly":
		return "月"
	default:
		return "总"
	}
}

func normalizeMutationAvailabilitySchedule(
	ctx context.Context,
	timezoneReader port.ManagementUsageStatsTimezoneReader,
	value any,
	now time.Time,
) (map[string]any, *string, *time.Time, bool, error) {
	record, ok := value.(map[string]any)
	if !ok {
		return nil, nil, nil, false, errors.New("API Key 时间计划参数无效")
	}
	defaultTimezone := "UTC"
	if _, hasTimezone := record["timezone"]; !hasTimezone && timezoneReader != nil {
		value, found, err := timezoneReader.GetManagementUsageStatsTimezone(ctx)
		if err == nil && found && strings.TrimSpace(value) != "" {
			defaultTimezone = strings.TrimSpace(value)
		}
	}
	schedule, allowed, err := apikeyschedule.Normalize(record, now, defaultTimezone)
	if err != nil {
		return nil, nil, nil, false, err
	}
	data, err := json.Marshal(schedule)
	if err != nil {
		return nil, nil, nil, false,
			fmt.Errorf("API Key 时间计划无法序列化: %w", err)
	}
	text := string(data)
	return schedule, &text, apikeyschedule.NextCheckAt(schedule, now), allowed, nil
}

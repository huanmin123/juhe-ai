package gatewayquota

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// Constants mirror storage/request-quota-limits.ts.
const (
	// MaxRequestQuotaHourlyWindowHours mirrors maxRequestQuotaHourlyWindowHours.
	MaxRequestQuotaHourlyWindowHours = 24 * 30
	// MaxRequestQuotaAmountUsd mirrors maxRequestQuotaAmountUsd
	// (Number.MAX_SAFE_INTEGER).
	MaxRequestQuotaAmountUsd = float64(1<<53 - 1)
	// quotaAmountPrecision mirrors QUOTA_AMOUNT_PRECISION (6 decimals).
	quotaAmountPrecision = 1_000_000
)

// QuotaLimit mirrors RequestQuotaLimit (enabled is always true after
// normalization; disabled entries are stripped).
type QuotaLimit struct {
	Enabled bool    `json:"enabled"`
	Limit   float64 `json:"limit"`
}

// HourlyQuotaLimit mirrors RequestHourlyQuotaLimit.
type HourlyQuotaLimit struct {
	Enabled bool    `json:"enabled"`
	Hours   int     `json:"hours"`
	Limit   float64 `json:"limit"`
}

// RequestQuotaLimits mirrors RequestQuotaLimits. nil mirrors the undefined
// per-window fields.
type RequestQuotaLimits struct {
	Hourly  *HourlyQuotaLimit
	Daily   *QuotaLimit
	Weekly  *QuotaLimit
	Monthly *QuotaLimit
	Total   *QuotaLimit
}

// EmptyRequestQuotaLimits mirrors emptyRequestQuotaLimits.
func EmptyRequestQuotaLimits() RequestQuotaLimits { return RequestQuotaLimits{} }

// nullValue is the JSON null sentinel. Go maps collapse "absent" and "null"
// while JS distinguishes undefined from null ({"daily":null} must throw
// 日额度参数无效 instead of being treated as absent), so decoded nulls keep
// their own identity through normalization.
type nullValue struct{}

// ParseRequestQuotaLimitsJSON mirrors parseRequestQuotaLimitsJson: blank
// input yields the empty limits; JSON parse errors and normalization errors
// (unsupported field, bad enabled flag, bad amounts) propagate verbatim.
func ParseRequestQuotaLimitsJSON(value string) (RequestQuotaLimits, error) {
	if strings.TrimSpace(value) == "" {
		return EmptyRequestQuotaLimits(), nil
	}
	decoded, err := decodeStoredJSONValue(value)
	if err != nil {
		return EmptyRequestQuotaLimits(), err
	}
	return NormalizeRequestQuotaLimits(decoded)
}

// decodeStoredJSONValue decodes a stored limits document preserving null vs
// absent (top-level null becomes Go nil == JS null; nested nulls become
// nullValue{}). A non-object top level (array/number/...) is returned as-is
// so normalization rejects it with the 参数无效 contract instead of a JSON
// unmarshal error.
func decodeStoredJSONValue(value string) (any, error) {
	var topLevel any
	if err := json.Unmarshal([]byte(value), &topLevel); err != nil {
		return nil, err
	}
	raws, ok := topLevel.(map[string]any)
	if !ok {
		return topLevel, nil
	}
	decoded := make(map[string]any, len(raws))
	for key, raw := range raws {
		encoded, err := json.Marshal(raw)
		if err != nil {
			return nil, err
		}
		if string(encoded) == "null" {
			decoded[key] = nullValue{}
			continue
		}
		decoded[key] = raw
	}
	return decoded, nil
}

// NormalizeRequestQuotaLimits mirrors normalizeRequestQuotaLimits with the
// JS-undefined fallback collapsing to the empty limits (the gateway only
// normalizes decoded JSON objects).
func NormalizeRequestQuotaLimits(value any) (RequestQuotaLimits, error) {
	return normalizeRequestQuotaLimits(value, EmptyRequestQuotaLimits())
}

func normalizeRequestQuotaLimits(value any, fallback RequestQuotaLimits) (RequestQuotaLimits, error) {
	if value == nil {
		// JS null -> empty limits (undefined -> fallback is unreachable for
		// decoded JSON and kept private above).
		return EmptyRequestQuotaLimits(), nil
	}
	record, ok := value.(map[string]any)
	if !ok {
		return RequestQuotaLimits{}, fmt.Errorf("请求额度限制参数无效")
	}
	if err := assertOnlyKeys(record, []string{"hourly", "daily", "weekly", "monthly", "total"}, "请求额度限制"); err != nil {
		return RequestQuotaLimits{}, err
	}
	hourly, err := normalizeHourlyQuotaLimit(record["hourly"])
	if err != nil {
		return RequestQuotaLimits{}, err
	}
	daily, err := normalizeQuotaLimit(record["daily"], "日额度")
	if err != nil {
		return RequestQuotaLimits{}, err
	}
	weekly, err := normalizeQuotaLimit(record["weekly"], "周额度")
	if err != nil {
		return RequestQuotaLimits{}, err
	}
	monthly, err := normalizeQuotaLimit(record["monthly"], "月额度")
	if err != nil {
		return RequestQuotaLimits{}, err
	}
	total, err := normalizeQuotaLimit(record["total"], "总额度")
	if err != nil {
		return RequestQuotaLimits{}, err
	}
	// stripDisabledQuotaLimits.
	limits := RequestQuotaLimits{}
	if hourly != nil && hourly.Enabled {
		limits.Hourly = hourly
	}
	if daily != nil && daily.Enabled {
		limits.Daily = daily
	}
	if weekly != nil && weekly.Enabled {
		limits.Weekly = weekly
	}
	if monthly != nil && monthly.Enabled {
		limits.Monthly = monthly
	}
	if total != nil && total.Enabled {
		limits.Total = total
	}
	return limits, nil
}

// HasEnabledRequestQuotaLimit mirrors hasEnabledRequestQuotaLimit.
func HasEnabledRequestQuotaLimit(limits RequestQuotaLimits) bool {
	return (limits.Hourly != nil && limits.Hourly.Enabled) ||
		(limits.Daily != nil && limits.Daily.Enabled) ||
		(limits.Weekly != nil && limits.Weekly.Enabled) ||
		(limits.Monthly != nil && limits.Monthly.Enabled) ||
		(limits.Total != nil && limits.Total.Enabled)
}

func normalizeQuotaLimit(value any, label string) (*QuotaLimit, error) {
	if value == nil {
		return nil, nil
	}
	record, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s参数无效", label)
	}
	if err := assertOnlyKeys(record, []string{"enabled", "limit"}, label); err != nil {
		return nil, err
	}
	if record["enabled"] != true {
		return nil, fmt.Errorf("%s启用状态必须为 true", label)
	}
	limit, err := positiveAmount(record["limit"], label)
	if err != nil {
		return nil, err
	}
	return &QuotaLimit{Enabled: true, Limit: limit}, nil
}

func normalizeHourlyQuotaLimit(value any) (*HourlyQuotaLimit, error) {
	if value == nil {
		return nil, nil
	}
	record, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("小时额度参数无效")
	}
	if err := assertOnlyKeys(record, []string{"enabled", "limit", "hours"}, "小时额度"); err != nil {
		return nil, err
	}
	if record["enabled"] != true {
		return nil, fmt.Errorf("小时额度启用状态必须为 true")
	}
	limit, err := positiveAmount(record["limit"], "小时额度")
	if err != nil {
		return nil, err
	}
	hours, err := positiveInteger(record["hours"], "小时额度窗口")
	if err != nil {
		return nil, err
	}
	return &HourlyQuotaLimit{Enabled: true, Hours: hours, Limit: limit}, nil
}

func positiveInteger(value any, label string) (int, error) {
	number, ok := value.(float64)
	if !ok || number != math.Trunc(number) {
		return 0, fmt.Errorf("%s必须是数字", label)
	}
	if number <= 0 || number > MaxRequestQuotaHourlyWindowHours {
		return 0, fmt.Errorf("%s必须在 1-%d 之间", label, MaxRequestQuotaHourlyWindowHours)
	}
	return int(number), nil
}

func positiveAmount(value any, label string) (float64, error) {
	number, ok := value.(float64)
	if !ok || math.IsNaN(number) || math.IsInf(number, 0) || number <= 0 || number > MaxRequestQuotaAmountUsd {
		return 0, fmt.Errorf("%s金额必须是大于 0 的数字", label)
	}
	scaled := number * quotaAmountPrecision
	if math.Round(scaled) != scaled {
		return 0, fmt.Errorf("%s金额最多支持 6 位小数", label)
	}
	return math.Round(scaled) / quotaAmountPrecision, nil
}

// assertOnlyKeys mirrors assertOnlyKeys. JS reports the first unexpected key
// in insertion order; Go maps lose that order, so keys are reported in
// sorted order (same error text, deterministic report).
func assertOnlyKeys(record map[string]any, allowedKeys []string, label string) error {
	allowed := make(map[string]struct{}, len(allowedKeys))
	for _, key := range allowedKeys {
		allowed[key] = struct{}{}
	}
	unexpected := make([]string, 0)
	for key := range record {
		if _, ok := allowed[key]; !ok {
			unexpected = append(unexpected, key)
		}
	}
	if len(unexpected) == 0 {
		return nil
	}
	sort.Strings(unexpected)
	return fmt.Errorf("%s包含不支持字段：%s", label, unexpected[0])
}

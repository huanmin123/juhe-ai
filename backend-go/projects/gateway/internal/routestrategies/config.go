package routestrategies

import (
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"
)

// Route strategy mode/config normalization mirrors
// backend/src/domain/route-strategy.ts. Only the five RouteStrategyMode values
// are accepted ('normal' | 'weighted' | 'failover' | 'round_robin' | 'merge');
// the scheduling preference (normalRoutingConfig) is carried by every mode and
// still only persists when speed_first (cost_first keeps config_json NULL).

// Route strategy modes (RouteStrategyMode, domain/types.ts).
const (
	ModeNormal     = "normal"
	ModeWeighted   = "weighted"
	ModeFailover   = "failover"
	ModeRoundRobin = "round_robin"
	ModeMerge      = "merge"
)

// IsRouteStrategyMode reports whether the raw value is one of the five modes.
func IsRouteStrategyMode(value string) bool {
	switch value {
	case ModeNormal, ModeWeighted, ModeFailover, ModeRoundRobin, ModeMerge:
		return true
	}
	return false
}

// ModeSupportsSchedulingPreference reports whether the mode may carry the
// scheduling preference (normalRoutingConfig): normal plus the passthrough
// modes weighted/failover/round_robin and merge.
func ModeSupportsSchedulingPreference(mode string) bool {
	switch mode {
	case ModeNormal, ModeWeighted, ModeFailover, ModeRoundRobin, ModeMerge:
		return true
	}
	return false
}

// defaults mirror domain/route-strategy.ts.
const (
	defaultNormalSchedulingPreference = "cost_first"
	defaultSpeedFirstDeadlineMs       = 30_000
)

// SpeedFirstConfig mirrors RouteStrategySpeedFirstConfig.
type SpeedFirstConfig struct {
	SlowTriggerCount              int `json:"slowTriggerCount"`
	SlowWindowSeconds             int `json:"slowWindowSeconds"`
	RecoverySuccessCount          int `json:"recoverySuccessCount"`
	ProbeIntervalSeconds          int `json:"probeIntervalSeconds"`
	DegradedTtlSeconds            int `json:"degradedTtlSeconds"`
	MaxFirstByteRetriesPerRequest int `json:"maxFirstByteRetriesPerRequest"`
}

// NormalRoutingConfig mirrors RouteStrategyNormalRoutingConfig: cost_first
// renders only schedulingPreference; speed_first always carries the deadline
// plus the full speedFirstConfig object.
type NormalRoutingConfig struct {
	SchedulingPreference string            `json:"schedulingPreference"`
	FirstByteDeadlineMs  *int              `json:"firstByteDeadlineMs,omitempty"`
	SpeedFirstConfig     *SpeedFirstConfig `json:"speedFirstConfig,omitempty"`
}

// storedConfig is the config_json document (routeStrategyConfigJson): only a
// non-default normalRoutingConfig persists.
type storedConfig struct {
	NormalRoutingConfig *NormalRoutingConfig `json:"normalRoutingConfig,omitempty"`
}

// routeStrategyConfigJSON mirrors routeStrategyConfigJson: the stored JSON is
// NULL when nothing non-default remains.
func routeStrategyConfigJSON(normal *NormalRoutingConfig) sql.NullString {
	document := storedConfig{}
	if normal != nil && normal.SchedulingPreference != defaultNormalSchedulingPreference {
		document.NormalRoutingConfig = normal
	}
	if document.NormalRoutingConfig == nil {
		return sql.NullString{}
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return sql.NullString{}
	}
	return sql.NullString{String: string(encoded), Valid: true}
}

// parseStoredConfig mirrors parseRouteStrategyRuntimeConfigJson: unknown keys
// are ignored on read; broken values surface the domain errors.
func parseStoredConfig(raw sql.NullString) (*NormalRoutingConfig, error) {
	if !raw.Valid || raw.String == "" {
		return nil, nil
	}
	var document storedConfig
	if err := json.Unmarshal([]byte(raw.String), &document); err != nil {
		return nil, &ValidationError{Message: "策略路由配置无效"}
	}
	if document.NormalRoutingConfig == nil {
		return nil, nil
	}
	// Re-normalize through the raw shape so legacy/partial rows repair.
	encoded, encodeErr := json.Marshal(document.NormalRoutingConfig)
	if encodeErr != nil {
		return nil, &ValidationError{Message: "策略路由配置无效"}
	}
	var decoded any
	_ = json.Unmarshal(encoded, &decoded)
	return normalizeNormalRoutingConfig(decoded)
}

// normalizeConfigForWrite mirrors normalizeRouteStrategyConfigForWrite: every
// mode accepts the scheduling preference (normalRoutingConfig); the former
// hybridRoutingConfig key is gone so no hybrid branch remains.
func normalizeConfigForWrite(normalRaw any, mode string) (*NormalRoutingConfig, error) {
	normal, err := normalizeNormalRoutingConfig(normalRaw)
	if err != nil {
		return nil, err
	}
	return normal, nil
}

// normalizeMode mirrors normalizeRouteStrategyMode: absent falls back to
// normal, unknown values throw.
func normalizeMode(value *string) (string, error) {
	if value == nil || *value == "" {
		return ModeNormal, nil
	}
	if IsRouteStrategyMode(*value) {
		return *value, nil
	}
	return "", &ValidationError{Message: "路由策略模式无效"}
}

// normalizeStatus mirrors normalizeRouteStrategyStatus.
func normalizeStatus(value *string, fallback string) (string, error) {
	if value == nil || *value == "" {
		return fallback, nil
	}
	if *value == "active" || *value == "disabled" {
		return *value, nil
	}
	return "", &ValidationError{Message: "策略路由状态无效"}
}

// normalizeNormalRoutingConfig mirrors domain/route-strategy.ts.
func normalizeNormalRoutingConfig(value any) (*NormalRoutingConfig, error) {
	if value == nil {
		return &NormalRoutingConfig{SchedulingPreference: defaultNormalSchedulingPreference}, nil
	}
	record, ok := value.(map[string]any)
	if !ok {
		return nil, &ValidationError{Message: "调度配置无效"}
	}
	preference, err := normalizeSchedulingPreference(record["schedulingPreference"])
	if err != nil {
		return nil, err
	}
	if preference == defaultNormalSchedulingPreference {
		return &NormalRoutingConfig{SchedulingPreference: preference}, nil
	}
	speedFirstRecord, err := optionalRecord(record["speedFirstConfig"], "速度优先配置无效")
	if err != nil {
		return nil, err
	}
	hasCommonDeadline := hasConfiguredValue(record["firstByteDeadlineMs"])
	hasLegacyDeadline := speedFirstRecord != nil && hasConfiguredValue(speedFirstRecord["firstByteThresholdMs"])
	if hasCommonDeadline && hasLegacyDeadline {
		return nil, &ValidationError{Message: "首字截止时间不能同时配置 firstByteDeadlineMs 和旧 firstByteThresholdMs"}
	}
	deadlineSource := any(nil)
	if hasCommonDeadline {
		deadlineSource = record["firstByteDeadlineMs"]
	} else if hasLegacyDeadline {
		deadlineSource = speedFirstRecord["firstByteThresholdMs"]
	}
	deadline, err := normalizeIntegerRange(deadlineSource, defaultSpeedFirstDeadlineMs, 10_000, 60_000, "首字截止时间必须是 10000-60000 毫秒")
	if err != nil {
		return nil, err
	}
	speedFirst, err := normalizeSpeedFirstConfig(speedFirstRecord)
	if err != nil {
		return nil, err
	}
	return &NormalRoutingConfig{
		SchedulingPreference: preference,
		FirstByteDeadlineMs:  &deadline,
		SpeedFirstConfig:     speedFirst,
	}, nil
}

func normalizeSchedulingPreference(value any) (string, error) {
	if value == nil || value == "" {
		return defaultNormalSchedulingPreference, nil
	}
	if text, ok := value.(string); ok && (text == "cost_first" || text == "speed_first") {
		return text, nil
	}
	return "", &ValidationError{Message: "调度偏好无效"}
}

// normalizeSpeedFirstConfig fills each missing knob from the built-in
// defaults and range-checks the rest (速度优先 messages mirror the source).
func normalizeSpeedFirstConfig(value any) (*SpeedFirstConfig, error) {
	fallback := SpeedFirstConfig{
		SlowTriggerCount:              3,
		SlowWindowSeconds:             120,
		RecoverySuccessCount:          3,
		ProbeIntervalSeconds:          30,
		DegradedTtlSeconds:            300,
		MaxFirstByteRetriesPerRequest: 2,
	}
	if value == nil {
		return &fallback, nil
	}
	record, ok := value.(map[string]any)
	if !ok {
		return nil, &ValidationError{Message: "速度优先配置无效"}
	}
	slowTriggerCount, err := normalizeIntegerRange(record["slowTriggerCount"], fallback.SlowTriggerCount, 2, 10, "速度优先触发次数必须是 2-10")
	if err != nil {
		return nil, err
	}
	slowWindowSeconds, err := normalizeIntegerRange(record["slowWindowSeconds"], fallback.SlowWindowSeconds, 60, 600, "速度优先窗口期必须是 60-600 秒")
	if err != nil {
		return nil, err
	}
	recoverySuccessCount, err := normalizeIntegerRange(record["recoverySuccessCount"], fallback.RecoverySuccessCount, 3, 10, "速度优先恢复次数必须是 3-10")
	if err != nil {
		return nil, err
	}
	probeIntervalSeconds, err := normalizeIntegerRange(record["probeIntervalSeconds"], fallback.ProbeIntervalSeconds, 10, 300, "速度优先探针间隔必须是 10-300 秒")
	if err != nil {
		return nil, err
	}
	degradedTtlSeconds, err := normalizeIntegerRange(record["degradedTtlSeconds"], fallback.DegradedTtlSeconds, 60, 3600, "速度优先降级保留时间必须是 60-3600 秒")
	if err != nil {
		return nil, err
	}
	maxRetries, err := normalizeIntegerRange(record["maxFirstByteRetriesPerRequest"], fallback.MaxFirstByteRetriesPerRequest, 1, 3, "速度优先单请求切号次数必须是 1-3")
	if err != nil {
		return nil, err
	}
	return &SpeedFirstConfig{
		SlowTriggerCount:              slowTriggerCount,
		SlowWindowSeconds:             slowWindowSeconds,
		RecoverySuccessCount:          recoverySuccessCount,
		ProbeIntervalSeconds:          probeIntervalSeconds,
		DegradedTtlSeconds:            degradedTtlSeconds,
		MaxFirstByteRetriesPerRequest: maxRetries,
	}, nil
}

// ---- shared raw-value helpers ----

func optionalRecord(value any, message string) (map[string]any, error) {
	if value == nil || value == "" {
		return nil, nil
	}
	record, ok := value.(map[string]any)
	if !ok {
		return nil, &ValidationError{Message: message}
	}
	return record, nil
}

func hasConfiguredValue(value any) bool {
	return value != nil && value != ""
}

// normalizeIntegerRange mirrors normalizeIntegerRange: absent/empty falls back,
// non-integers or out-of-range values throw the labeled message.
func normalizeIntegerRange(value any, fallback, min, max int, message string) (int, error) {
	if value == nil || value == "" {
		return fallback, nil
	}
	number, ok := numericValue(value)
	if !ok {
		return 0, &ValidationError{Message: message}
	}
	if number < min || number > max {
		return 0, &ValidationError{Message: message}
	}
	return number, nil
}

// numericValue accepts JSON numbers and numeric strings (repository Number()
// coercion); non-integral values are rejected like Number.isInteger.
func numericValue(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		if typed != float64(int64(typed)) {
			return 0, false
		}
		return int(typed), true
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return 0, false
		}
		parsed, err := strconv.ParseInt(trimmed, 10, 64)
		if err != nil {
			return 0, false
		}
		return int(parsed), true
	default:
		return 0, false
	}
}

// configValuesEqual mirrors routeStrategyPatchValuesEqual (JSON stringify).
func configValuesEqual(left any, right any) bool {
	leftJSON, err := json.Marshal(left)
	if err != nil {
		return false
	}
	rightJSON, err := json.Marshal(right)
	if err != nil {
		return false
	}
	return string(leftJSON) == string(rightJSON)
}

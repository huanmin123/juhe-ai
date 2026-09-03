package routestrategies

import (
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"
)

// Route strategy mode/config normalization mirrors
// backend/src/domain/route-strategy.ts and api-key-hybrid-routing.ts. Only the
// five RouteStrategyMode values are accepted ('normal' | 'hybrid_smart' |
// 'weighted' | 'failover' | 'round_robin'); weighted/failover/round_robin
// carry no per-mode config object (stored config stays NULL).

// Route strategy modes (RouteStrategyMode, domain/types.ts).
const (
	ModeNormal      = "normal"
	ModeHybridSmart = "hybrid_smart"
	ModeWeighted    = "weighted"
	ModeFailover    = "failover"
	ModeRoundRobin  = "round_robin"
)

// IsRouteStrategyMode reports whether the raw value is one of the five modes.
func IsRouteStrategyMode(value string) bool {
	switch value {
	case ModeNormal, ModeHybridSmart, ModeWeighted, ModeFailover, ModeRoundRobin:
		return true
	}
	return false
}

// defaults mirror domain/route-strategy.ts + api-key-hybrid-routing.ts.
const (
	defaultNormalSchedulingPreference = "cost_first"
	defaultSpeedFirstDeadlineMs       = 30_000
	defaultHybridScoringTimeoutMs     = 15_000
	defaultHybridFallbackMaxLevel     = 5
	defaultHybridScoringCacheTTL      = 300
	defaultHybridAffinityTTL          = 900
	defaultHybridSwitchMinLevelDelta  = 2
	defaultHybridDowngradeLowCount    = 2
	hybridLevelRouteMaxCount          = 5
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

// HybridLevelRoute mirrors ApiKeyHybridLevelRoute.
type HybridLevelRoute struct {
	MinLevel    int    `json:"minLevel"`
	MaxLevel    int    `json:"maxLevel"`
	TargetModel string `json:"targetModel"`
	Enabled     bool   `json:"enabled"`
}

// HybridQualityInspection mirrors ApiKeyHybridQualityInspectionConfig; the
// repository always materializes it (defaults when absent from input).
type HybridQualityInspection struct {
	Enabled           bool    `json:"enabled"`
	ScoringGroupID    *string `json:"scoringGroupId,omitempty"`
	ScoringModel      string  `json:"scoringModel"`
	TriggerMode       string  `json:"triggerMode"`
	MaxTriggerLevel   int     `json:"maxTriggerLevel"`
	MaxRetries        int     `json:"maxRetries"`
	FailureAction     string  `json:"failureAction"`
	UnavailableAction string  `json:"unavailableAction"`
}

// HybridRoutingConfig mirrors ApiKeyHybridRoutingConfig (normalized output
// shape: scoringGroupId dropped when empty, qualityInspection always present).
type HybridRoutingConfig struct {
	ScoringGroupID               *string                  `json:"scoringGroupId,omitempty"`
	ScoringModel                 string                   `json:"scoringModel"`
	ScoringContextMode           string                   `json:"scoringContextMode"`
	QualityPreference            string                   `json:"qualityPreference"`
	ScoringTimeoutMs             int                      `json:"scoringTimeoutMs"`
	ScoringFallbackMaxLevel      int                      `json:"scoringFallbackMaxLevel"`
	ScoringCacheEnabled          bool                     `json:"scoringCacheEnabled"`
	ScoringCacheTTLSeconds       int                      `json:"scoringCacheTtlSeconds"`
	CacheAffinityEnabled         bool                     `json:"cacheAffinityEnabled"`
	AffinityTTLSeconds           int                      `json:"affinityTtlSeconds"`
	SwitchMinLevelDelta          int                      `json:"switchMinLevelDelta"`
	DowngradeConsecutiveLowCount int                      `json:"downgradeConsecutiveLowCount"`
	LevelRoutes                  []HybridLevelRoute       `json:"levelRoutes"`
	QualityInspection            *HybridQualityInspection `json:"qualityInspection"`
}

// storedConfig is the config_json document (routeStrategyConfigJson): only
// non-default normalRoutingConfig and hybrid_smart hybridRoutingConfig persist.
type storedConfig struct {
	NormalRoutingConfig *NormalRoutingConfig `json:"normalRoutingConfig,omitempty"`
	HybridRoutingConfig *HybridRoutingConfig `json:"hybridRoutingConfig,omitempty"`
}

// routeStrategyConfigJSON mirrors routeStrategyConfigJson: the stored JSON is
// NULL when nothing non-default remains.
func routeStrategyConfigJSON(normal *NormalRoutingConfig, hybrid *HybridRoutingConfig) sql.NullString {
	document := storedConfig{}
	if normal != nil && normal.SchedulingPreference != defaultNormalSchedulingPreference {
		document.NormalRoutingConfig = normal
	}
	if hybrid != nil {
		document.HybridRoutingConfig = hybrid
	}
	if document.NormalRoutingConfig == nil && document.HybridRoutingConfig == nil {
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
func parseStoredConfig(raw sql.NullString) (normal *NormalRoutingConfig, hybrid *HybridRoutingConfig, err error) {
	if !raw.Valid || raw.String == "" {
		return nil, nil, nil
	}
	var document storedConfig
	if err := json.Unmarshal([]byte(raw.String), &document); err != nil {
		return nil, nil, &ValidationError{Message: "策略路由配置无效"}
	}
	if document.NormalRoutingConfig != nil {
		// Re-normalize through the raw shape so legacy/partial rows repair.
		encoded, encodeErr := json.Marshal(document.NormalRoutingConfig)
		if encodeErr != nil {
			return nil, nil, &ValidationError{Message: "策略路由配置无效"}
		}
		var decoded any
		_ = json.Unmarshal(encoded, &decoded)
		normal, err = normalizeNormalRoutingConfig(decoded)
		if err != nil {
			return nil, nil, err
		}
	}
	if document.HybridRoutingConfig != nil {
		encoded, encodeErr := json.Marshal(document.HybridRoutingConfig)
		if encodeErr != nil {
			return nil, nil, &ValidationError{Message: "策略路由配置无效"}
		}
		var decoded any
		_ = json.Unmarshal(encoded, &decoded)
		hybrid, err = normalizeHybridRoutingConfig(decoded)
		if err != nil {
			return nil, nil, err
		}
	}
	return normal, hybrid, nil
}

// normalizeConfigForWrite mirrors normalizeRouteStrategyConfigForWrite: only
// normal routes may carry normalRoutingConfig, only hybrid_smart may carry
// hybridRoutingConfig, and hybrid_smart requires the hybrid config.
func normalizeConfigForWrite(normalRaw, hybridRaw any, mode string) (*NormalRoutingConfig, *HybridRoutingConfig, error) {
	if mode == ModeNormal {
		if rawValueConfigured(hybridRaw) {
			return nil, nil, &ValidationError{Message: "普通路由不能配置混合评分规则"}
		}
		normal, err := normalizeNormalRoutingConfig(normalRaw)
		if err != nil {
			return nil, nil, err
		}
		return normal, nil, nil
	}
	if rawValueConfigured(normalRaw) {
		return nil, nil, &ValidationError{Message: "只有普通路由可以配置调度偏好"}
	}
	if mode == ModeHybridSmart {
		hybrid, err := normalizeHybridRoutingConfig(hybridRaw)
		if err != nil {
			return nil, nil, err
		}
		return nil, hybrid, nil
	}
	if rawValueConfigured(hybridRaw) {
		return nil, nil, &ValidationError{Message: "只有混合智能路由可以配置混合评分规则"}
	}
	return nil, nil, nil
}

// rawValueConfigured mirrors `value !== undefined && value !== null`.
func rawValueConfigured(value any) bool {
	return value != nil
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
		return nil, &ValidationError{Message: "普通路由调度配置无效"}
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
	return "", &ValidationError{Message: "普通路由调度偏好无效"}
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

// normalizeHybridRoutingConfig mirrors domain/api-key-hybrid-routing.ts.
func normalizeHybridRoutingConfig(value any) (*HybridRoutingConfig, error) {
	if value == nil {
		return nil, &ValidationError{Message: "混合路由配置不能为空"}
	}
	record, ok := value.(map[string]any)
	if !ok {
		return nil, &ValidationError{Message: "混合路由配置不能为空"}
	}
	scoringGroupID := optionalTrimmedString(record["scoringGroupId"])
	scoringModel, err := requiredTrimmedString(record["scoringModel"], "混合路由评分模型不能为空")
	if err != nil {
		return nil, err
	}
	scoringContextMode, err := normalizeEnumField(record["scoringContextMode"], "full_request", []string{"full_request"}, "混合路由评分上下文模式无效")
	if err != nil {
		return nil, err
	}
	qualityPreference, err := normalizeEnumField(record["qualityPreference"], "balanced",
		[]string{"cost_first", "balanced", "quality_first"}, "混合路由质量偏好无效")
	if err != nil {
		return nil, err
	}
	scoringTimeoutMs, err := normalizeIntegerRange(record["scoringTimeoutMs"], defaultHybridScoringTimeoutMs, 1000, 60_000, "混合路由评分超时时间必须是 1000-60000 毫秒")
	if err != nil {
		return nil, err
	}
	scoringFallbackMaxLevel, err := normalizeIntegerRange(record["scoringFallbackMaxLevel"], defaultHybridFallbackMaxLevel, 2, 5, "混合路由评分不可用兜底上限必须是 2-5")
	if err != nil {
		return nil, err
	}
	scoringCacheTTLSeconds, err := normalizeIntegerRange(record["scoringCacheTtlSeconds"], defaultHybridScoringCacheTTL, 1, 3600, "混合路由评分缓存 TTL 必须是 1-3600 秒")
	if err != nil {
		return nil, err
	}
	affinityTTLSeconds, err := normalizeIntegerRange(record["affinityTtlSeconds"], defaultHybridAffinityTTL, 1, 86_400, "混合路由缓存亲和 TTL 必须是 1-86400 秒")
	if err != nil {
		return nil, err
	}
	switchMinLevelDelta, err := normalizeIntegerRange(record["switchMinLevelDelta"], defaultHybridSwitchMinLevelDelta, 0, 9, "混合路由切换等级差必须是 0-9")
	if err != nil {
		return nil, err
	}
	downgradeLowCount, err := normalizeIntegerRange(record["downgradeConsecutiveLowCount"], defaultHybridDowngradeLowCount, 1, 20, "混合路由降级确认次数必须是 1-20")
	if err != nil {
		return nil, err
	}
	levelRoutes, err := normalizeHybridLevelRoutes(record["levelRoutes"])
	if err != nil {
		return nil, err
	}
	qualityInspection, err := normalizeQualityInspection(record["qualityInspection"], scoringModel)
	if err != nil {
		return nil, err
	}
	config := &HybridRoutingConfig{
		ScoringModel:                 scoringModel,
		ScoringContextMode:           scoringContextMode,
		QualityPreference:            qualityPreference,
		ScoringTimeoutMs:             scoringTimeoutMs,
		ScoringFallbackMaxLevel:      scoringFallbackMaxLevel,
		ScoringCacheEnabled:          true,
		ScoringCacheTTLSeconds:       scoringCacheTTLSeconds,
		CacheAffinityEnabled:         true,
		AffinityTTLSeconds:           affinityTTLSeconds,
		SwitchMinLevelDelta:          switchMinLevelDelta,
		DowngradeConsecutiveLowCount: downgradeLowCount,
		LevelRoutes:                  levelRoutes,
		QualityInspection:            qualityInspection,
	}
	if scoringGroupID != "" {
		config.ScoringGroupID = &scoringGroupID
	}
	return config, nil
}

// normalizeHybridLevelRoutes enforces the full coverage contract: enabled
// routes only, at most 5, at least 2 distinct target models, first tier
// 1-2..1-5, contiguous coverage of levels 1-10.
func normalizeHybridLevelRoutes(value any) ([]HybridLevelRoute, error) {
	list, ok := value.([]any)
	if !ok || len(list) == 0 {
		return nil, &ValidationError{Message: "混合路由等级范围不能为空"}
	}
	normalized := make([]HybridLevelRoute, 0, len(list))
	for _, item := range list {
		record, ok := item.(map[string]any)
		if !ok {
			return nil, &ValidationError{Message: "混合路由等级范围无效"}
		}
		minLevel, err := normalizeIntegerRange(record["minLevel"], 0, 1, 10, "混合路由最小等级必须是 1-10")
		if err != nil {
			return nil, err
		}
		maxLevel, err := normalizeIntegerRange(record["maxLevel"], 0, 1, 10, "混合路由最大等级必须是 1-10")
		if err != nil {
			return nil, err
		}
		if minLevel > maxLevel {
			return nil, &ValidationError{Message: "混合路由等级范围最小值不能大于最大值"}
		}
		targetModel, err := requiredTrimmedString(record["targetModel"], "混合路由目标模型不能为空")
		if err != nil {
			return nil, err
		}
		enabled := true
		if raw, present := record["enabled"]; present && raw != nil {
			enabled, ok = raw.(bool)
			if !ok {
				return nil, &ValidationError{Message: "混合路由等级范围启用状态必须是布尔值"}
			}
		}
		if enabled {
			normalized = append(normalized, HybridLevelRoute{MinLevel: minLevel, MaxLevel: maxLevel, TargetModel: targetModel, Enabled: true})
		}
	}
	if len(normalized) == 0 {
		return nil, &ValidationError{Message: "混合路由至少需要一个启用的等级范围"}
	}
	if len(normalized) > hybridLevelRouteMaxCount {
		return nil, &ValidationError{Message: "混合路由最多只能配置 5 个等级范围"}
	}
	modelKeys := map[string]bool{}
	for _, route := range normalized {
		modelKeys[strings.ToLower(route.TargetModel)] = true
	}
	if len(modelKeys) < 2 {
		return nil, &ValidationError{Message: "混合路由至少需要配置 2 个不同的目标模型"}
	}
	if normalized[0].MinLevel != 1 || normalized[0].MaxLevel < 2 || normalized[0].MaxLevel > 5 {
		return nil, &ValidationError{Message: "混合路由最低档必须从等级 1 开始，并覆盖 1-2 到 1-5 之间的范围"}
	}
	expectedMinLevel := 1
	for index, route := range normalized {
		if route.MinLevel != expectedMinLevel {
			return nil, &ValidationError{Message: "混合路由第 " + strconv.Itoa(index+1) + " 个等级范围必须从等级 " + strconv.Itoa(expectedMinLevel) + " 开始"}
		}
		expectedMinLevel = route.MaxLevel + 1
	}
	if expectedMinLevel != 11 {
		return nil, &ValidationError{Message: "混合路由等级范围必须按从小到大连续覆盖 1-10"}
	}
	return normalized, nil
}

// normalizeQualityInspection mirrors normalizeQualityInspectionConfig: absent
// input materializes the defaults with the primary scoring model inherited.
func normalizeQualityInspection(value any, primaryScoringModel string) (*HybridQualityInspection, error) {
	if value == nil {
		return &HybridQualityInspection{
			Enabled:           true,
			ScoringModel:      primaryScoringModel,
			TriggerMode:       "risk_based",
			MaxTriggerLevel:   6,
			MaxRetries:        2,
			FailureAction:     "repair_then_upgrade",
			UnavailableAction: "pass_through",
		}, nil
	}
	record, ok := value.(map[string]any)
	if !ok {
		return nil, &ValidationError{Message: "混合路由质量评分配置无效"}
	}
	enabled := true
	if raw, present := record["enabled"]; present && raw != nil {
		enabled, ok = raw.(bool)
		if !ok {
			return nil, &ValidationError{Message: "混合路由质量评分开关必须是布尔值"}
		}
	}
	scoringGroupID := optionalTrimmedString(record["scoringGroupId"])
	scoringModel := optionalTrimmedString(record["scoringModel"])
	if scoringModel == "" {
		scoringModel = primaryScoringModel
	}
	if enabled && scoringModel == "" {
		return nil, &ValidationError{Message: "混合路由质量评分模型不能为空"}
	}
	triggerMode, err := normalizeEnumField(record["triggerMode"], "risk_based",
		[]string{"quality_first_only", "risk_based", "always_for_hybrid"}, "混合路由质量评分触发模式无效")
	if err != nil {
		return nil, err
	}
	maxTriggerLevel, err := normalizeIntegerRange(record["maxTriggerLevel"], 6, 1, 10, "混合路由质量评分最高触发等级必须是 1-10")
	if err != nil {
		return nil, err
	}
	maxRetries, err := normalizeIntegerRange(record["maxRetries"], 2, 0, 2, "混合路由质量评分重试次数必须是 0-2")
	if err != nil {
		return nil, err
	}
	failureAction, err := normalizeEnumField(record["failureAction"], "repair_then_upgrade",
		[]string{"repair_then_upgrade", "upgrade_next_level", "retry_same_model", "return_error"}, "混合路由质量评分失败动作无效")
	if err != nil {
		return nil, err
	}
	unavailableAction, err := normalizeEnumField(record["unavailableAction"], "pass_through",
		[]string{"pass_through", "return_error"}, "混合路由质量评分不可用处理方式无效")
	if err != nil {
		return nil, err
	}
	inspection := &HybridQualityInspection{
		Enabled:           enabled,
		ScoringModel:      scoringModel,
		TriggerMode:       triggerMode,
		MaxTriggerLevel:   maxTriggerLevel,
		MaxRetries:        maxRetries,
		FailureAction:     failureAction,
		UnavailableAction: unavailableAction,
	}
	if scoringGroupID != "" {
		inspection.ScoringGroupID = &scoringGroupID
	}
	return inspection, nil
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

func normalizeEnumField(value any, fallback string, allowed []string, message string) (string, error) {
	if value == nil || value == "" {
		return fallback, nil
	}
	text, ok := value.(string)
	if !ok {
		return "", &ValidationError{Message: message}
	}
	for _, candidate := range allowed {
		if text == candidate {
			return text, nil
		}
	}
	return "", &ValidationError{Message: message}
}

func optionalTrimmedString(value any) string {
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}

func requiredTrimmedString(value any, message string) (string, error) {
	text := optionalTrimmedString(value)
	if text == "" {
		return "", &ValidationError{Message: message}
	}
	return text, nil
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

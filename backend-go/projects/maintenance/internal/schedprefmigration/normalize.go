// 配置规范化（迁移侧复制本）。
//
// 来源与同步纪律：本文件是 gateway internal/routestrategies/config.go 中
// normalRoutingConfig / speedFirstConfig 规范化语义的**逐字复制本**（2026-10-12
// 自当时源码复制），错误文案逐字保留。maintenance 不可 import gateway internal，
// 把 normalize 下沉到共享包则要求改写 gateway/internal/routestrategies（P2 迁
// 移任务红线禁改），故按最小方案复制；「规范化参数等值比较」契约（计划 D2：
// 迁移只比较字段与有效运行参数，不比较 JSON 键顺序或原始字节）依赖与读取链
// 完全同源的 normalize 行为。gateway 侧语义变更时必须同步本复制本，否则迁移
// 预检的等值判定会与运行时读取漂移——两侧漂移由预检的坏配置清单暴露，不得静
// 默降级。
//
// 覆盖范围（迁移只消费这个子集）：normalizeNormalRoutingConfig 及其全部传递
// 闭包（normalizeSchedulingPreference / normalizeSpeedFirstConfig /
// optionalRecord / hasConfiguredValue / normalizeIntegerRange / numericValue）
// 与 config_json 文档投影 parseStrategyConfigJSON（镜像 parseStoredConfig：一
// 次 unmarshal 到 map 后直接归一化，不走 struct 往返，缺失键保持缺失按缺省默
// 认生效）。策略写侧的未知键拒绝、双唯一名称等逻辑不属于迁移读取面，不在复
// 制范围内。

package schedprefmigration

import (
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"
)

// ValidationError 与 gateway routestrategies.ValidationError 同构（Message 即
// 用户可见错误原文）。
type ValidationError struct{ Message string }

// Error 实现 error。
func (e *ValidationError) Error() string { return e.Message }

// defaults 镜像 gateway config.go 同名常量（缺省值与合法范围的单一事实来自
// gateway 侧；本文件不另立默认）。
const (
	defaultNormalSchedulingPreference           = "cost_first"
	defaultSpeedFirstDeadlineMs                 = 30_000
	defaultSpeedFirstTotalTimeSeconds           = 120
	defaultSpeedFirstCompactionTotalTimeSeconds = 300
)

// SpeedFirstConfig 镜像 gateway routestrategies.SpeedFirstConfig（json tag 逐
// 字一致，供规范化 JSON 序列化键序稳定）。
type SpeedFirstConfig struct {
	SlowTriggerCount              int `json:"slowTriggerCount"`
	SlowWindowSeconds             int `json:"slowWindowSeconds"`
	RecoverySuccessCount          int `json:"recoverySuccessCount"`
	ProbeIntervalSeconds          int `json:"probeIntervalSeconds"`
	DegradedTtlSeconds            int `json:"degradedTtlSeconds"`
	MaxFirstByteRetriesPerRequest int `json:"maxFirstByteRetriesPerRequest"`
	// TotalTimeDeadlineSeconds / CompactionTotalTimeDeadlineSeconds 是总时间
	// 兜底截止（设计 6.2）：普通档作用于速度优先可重放文本，压缩档作用于压
	// 缩请求与估算大输入请求。
	TotalTimeDeadlineSeconds           int `json:"totalTimeDeadlineSeconds"`
	CompactionTotalTimeDeadlineSeconds int `json:"compactionTotalTimeDeadlineSeconds"`
}

// NormalRoutingConfig 镜像 gateway routestrategies.NormalRoutingConfig。
type NormalRoutingConfig struct {
	SchedulingPreference string            `json:"schedulingPreference"`
	FirstByteDeadlineMs  *int              `json:"firstByteDeadlineMs,omitempty"`
	SpeedFirstConfig     *SpeedFirstConfig `json:"speedFirstConfig,omitempty"`
}

// parseStrategyConfigJSON 镜像 gateway parseStoredConfig：config_json 为
// NULL/空或缺 normalRoutingConfig 键时返回 (nil, nil)（缺省成本优先）；损坏
// JSON 报「策略路由配置无效」，结构非法保留 normalize 原始中文错误。
func parseStrategyConfigJSON(raw sql.NullString) (*NormalRoutingConfig, error) {
	if !raw.Valid || raw.String == "" {
		return nil, nil
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(raw.String), &document); err != nil {
		return nil, &ValidationError{Message: "策略路由配置无效"}
	}
	normalValue, exists := document["normalRoutingConfig"]
	if !exists || normalValue == nil {
		return nil, nil
	}
	return normalizeNormalRoutingConfig(normalValue)
}

// normalizeNormalRoutingConfig 与 gateway 内部 normalize 逐字同语义（含「调度
// 配置无效」「调度偏好无效」「首字截止时间不能同时配置 firstByteDeadlineMs
// 和旧 firstByteThresholdMs」等原始文案）；旧 firstByteThresholdMs 读取别名按
// 现行规则处理，双字段同时出现仍拒绝。
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

// normalizeSchedulingPreference 镜像 gateway 同名函数。
func normalizeSchedulingPreference(value any) (string, error) {
	if value == nil || value == "" {
		return defaultNormalSchedulingPreference, nil
	}
	if text, ok := value.(string); ok && (text == "cost_first" || text == "speed_first") {
		return text, nil
	}
	return "", &ValidationError{Message: "调度偏好无效"}
}

// normalizeSpeedFirstConfig 镜像 gateway 同名函数：缺省回填、越界/非整数按
// 原始文案拒绝。
func normalizeSpeedFirstConfig(value any) (*SpeedFirstConfig, error) {
	fallback := SpeedFirstConfig{
		SlowTriggerCount:                   3,
		SlowWindowSeconds:                  120,
		RecoverySuccessCount:               3,
		ProbeIntervalSeconds:               30,
		DegradedTtlSeconds:                 300,
		MaxFirstByteRetriesPerRequest:      2,
		TotalTimeDeadlineSeconds:           defaultSpeedFirstTotalTimeSeconds,
		CompactionTotalTimeDeadlineSeconds: defaultSpeedFirstCompactionTotalTimeSeconds,
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
	totalTimeDeadlineSeconds, err := normalizeIntegerRange(record["totalTimeDeadlineSeconds"], fallback.TotalTimeDeadlineSeconds, 60, 270, "请求总时间截止必须是 60-270 秒")
	if err != nil {
		return nil, err
	}
	compactionTotalTimeDeadlineSeconds, err := normalizeIntegerRange(record["compactionTotalTimeDeadlineSeconds"], fallback.CompactionTotalTimeDeadlineSeconds, 300, 900, "压缩总时间截止必须是 300-900 秒")
	if err != nil {
		return nil, err
	}
	return &SpeedFirstConfig{
		SlowTriggerCount:                   slowTriggerCount,
		SlowWindowSeconds:                  slowWindowSeconds,
		RecoverySuccessCount:               recoverySuccessCount,
		ProbeIntervalSeconds:               probeIntervalSeconds,
		DegradedTtlSeconds:                 degradedTtlSeconds,
		MaxFirstByteRetriesPerRequest:      maxRetries,
		TotalTimeDeadlineSeconds:           totalTimeDeadlineSeconds,
		CompactionTotalTimeDeadlineSeconds: compactionTotalTimeDeadlineSeconds,
	}, nil
}

// optionalRecord / hasConfiguredValue / normalizeIntegerRange / numericValue
// 镜像 gateway config.go 同名 shared helpers。

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

// equalNormalConfig 判定两份规范化配置的「规范化参数等值」（计划 D2：比较字
// 段与有效运行参数，不比较 JSON 键顺序或原始字节）。
func equalNormalConfig(left, right *NormalRoutingConfig) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	if left.SchedulingPreference != right.SchedulingPreference {
		return false
	}
	if (left.FirstByteDeadlineMs == nil) != (right.FirstByteDeadlineMs == nil) {
		return false
	}
	if left.FirstByteDeadlineMs != nil && *left.FirstByteDeadlineMs != *right.FirstByteDeadlineMs {
		return false
	}
	if (left.SpeedFirstConfig == nil) != (right.SpeedFirstConfig == nil) {
		return false
	}
	return left.SpeedFirstConfig == nil || *left.SpeedFirstConfig == *right.SpeedFirstConfig
}

// parsePreferenceSpeedConfig 解析偏好行存储的 chat_speed_first_config_json 并
// 经同一 normalize 还原有效配置（供等值比较：喂入 schedulingPreference=
// speed_first 的合成形状），损坏/结构非法原样上抛，不静默回退。
func parsePreferenceSpeedConfig(raw sql.NullString) (*NormalRoutingConfig, error) {
	if !raw.Valid || raw.String == "" {
		return nil, &ValidationError{Message: "调度偏好速度配置缺失"}
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(raw.String), &document); err != nil {
		return nil, &ValidationError{Message: "调度偏好速度配置无效"}
	}
	synthetic := map[string]any{
		"schedulingPreference": "speed_first",
		"firstByteDeadlineMs":  document["firstByteDeadlineMs"],
		"speedFirstConfig":     document["speedFirstConfig"],
	}
	return normalizeNormalRoutingConfig(synthetic)
}

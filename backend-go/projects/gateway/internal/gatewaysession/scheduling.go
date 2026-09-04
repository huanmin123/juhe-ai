package gatewaysession

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// Local projection of domain/group-scheduling.ts: only the fields the
// session-affinity service consumes plus the validation rules that decide
// whether a policy value is usable at all.

// Default high-concurrency scheduling policy values mirror
// DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY. DefaultSoftConcurrency /
// MaxQueueSize / PerAPIKeyQueueLimit come from
// runtimeConfig.concurrency.globalMax (default 5000) and are injected via
// SchedulingDefaults.
type SchedulingPolicyValues struct {
	Mode                            string
	DefaultSoftConcurrency          int64
	FastFirstEnabled                bool
	FallbackOnQueueEnabled          bool
	BreakAffinityOnSoftLimit        bool
	BreakAffinityOnQueueWaitMs      int64
	SlowRequestThresholdMs          int64
	FirstOutputSlowThresholdMs      int64
	RecentTimeoutWindowSeconds      int64
	RecentTimeoutPenaltyThreshold   int64
	MaxQueueWaitMs                  int64
	MaxQueueSize                    int64
	PerAPIKeyQueueLimit             int64
	ClientIPConcurrencyLimit        int64
	ClientIPConcurrencyOverflowMode string
	ImageLaneMaxConcurrency         int64
}

// SchedulingDefaults carries the runtimeConfig.concurrency.globalMax
// projection used for the globalMax-backed policy defaults.
type SchedulingDefaults struct {
	GlobalMax int64
}

// DefaultHighConcurrencyGroupSchedulingPolicy mirrors the Node default with
// the globalMax fields filled from defaults.GlobalMax.
func DefaultHighConcurrencyGroupSchedulingPolicy(defaults SchedulingDefaults) SchedulingPolicyValues {
	return SchedulingPolicyValues{
		Mode:                            "balanced_fast",
		DefaultSoftConcurrency:          defaults.GlobalMax,
		FastFirstEnabled:                true,
		FallbackOnQueueEnabled:          true,
		BreakAffinityOnSoftLimit:        true,
		BreakAffinityOnQueueWaitMs:      0,
		SlowRequestThresholdMs:          30_000,
		FirstOutputSlowThresholdMs:      15_000,
		RecentTimeoutWindowSeconds:      120,
		RecentTimeoutPenaltyThreshold:   2,
		MaxQueueWaitMs:                  60_000,
		MaxQueueSize:                    defaults.GlobalMax,
		PerAPIKeyQueueLimit:             defaults.GlobalMax,
		ClientIPConcurrencyLimit:        0,
		ClientIPConcurrencyOverflowMode: "reject",
		ImageLaneMaxConcurrency:         0,
	}
}

// storedGroupSchedulingPolicyKeys mirrors storedGroupSchedulingPolicyKeys.
var storedGroupSchedulingPolicyKeys = []string{
	"mode",
	"defaultSoftConcurrency",
	"fastFirstEnabled",
	"fallbackOnQueueEnabled",
	"breakAffinityOnSoftLimit",
	"breakAffinityOnQueueWaitMs",
	"slowRequestThresholdMs",
	"firstOutputSlowThresholdMs",
	"recentTimeoutWindowSeconds",
	"recentTimeoutPenaltyThreshold",
	"maxQueueWaitMs",
	"maxQueueSize",
	"perApiKeyQueueLimit",
	"clientIpConcurrencyLimit",
	"clientIpConcurrencyOverflowMode",
	"imageLaneMaxConcurrency",
}

// ResolveGroupSchedulingPolicy mirrors resolveGroupSchedulingPolicy for the
// high_concurrency group type. groupType != high_concurrency yields nil
// without validation, exactly like Node.
func ResolveGroupSchedulingPolicy(groupType string, value map[string]any, defaults SchedulingDefaults) (*SchedulingPolicyValues, error) {
	if groupType != GroupTypeHighConcurrency {
		return nil, nil
	}
	policy, err := validateGroupSchedulingPolicy(value, defaults)
	if err != nil {
		return nil, err
	}
	return policy, nil
}

func validateGroupSchedulingPolicy(value map[string]any, defaults SchedulingDefaults) (*SchedulingPolicyValues, error) {
	base := DefaultHighConcurrencyGroupSchedulingPolicy(defaults)

	// objectValue: nil/undefined -> {}.
	if value != nil {
		if err := assertOnlyKeys(value, storedGroupSchedulingPolicyKeys, "分组调度策略"); err != nil {
			return nil, err
		}
	}

	maxQueueSize, err := numericPolicy(value["maxQueueSize"], "maxQueueSize", base.MaxQueueSize)
	if err != nil {
		return nil, err
	}
	mode, err := modePolicy(value["mode"], base.Mode)
	if err != nil {
		return nil, err
	}
	defaultSoftConcurrency, err := numericPolicy(value["defaultSoftConcurrency"], "defaultSoftConcurrency", base.DefaultSoftConcurrency)
	if err != nil {
		return nil, err
	}
	fastFirstEnabled, err := booleanPolicy(value["fastFirstEnabled"], "fastFirstEnabled", base.FastFirstEnabled)
	if err != nil {
		return nil, err
	}
	fallbackOnQueueEnabled, err := booleanPolicy(value["fallbackOnQueueEnabled"], "fallbackOnQueueEnabled", base.FallbackOnQueueEnabled)
	if err != nil {
		return nil, err
	}
	breakAffinityOnSoftLimit, err := booleanPolicy(value["breakAffinityOnSoftLimit"], "breakAffinityOnSoftLimit", base.BreakAffinityOnSoftLimit)
	if err != nil {
		return nil, err
	}
	breakAffinityOnQueueWaitMs, err := numericPolicy(value["breakAffinityOnQueueWaitMs"], "breakAffinityOnQueueWaitMs", base.BreakAffinityOnQueueWaitMs)
	if err != nil {
		return nil, err
	}
	slowRequestThresholdMs, err := numericPolicy(value["slowRequestThresholdMs"], "slowRequestThresholdMs", base.SlowRequestThresholdMs)
	if err != nil {
		return nil, err
	}
	firstOutputSlowThresholdMs, err := numericPolicy(value["firstOutputSlowThresholdMs"], "firstOutputSlowThresholdMs", base.FirstOutputSlowThresholdMs)
	if err != nil {
		return nil, err
	}
	recentTimeoutWindowSeconds, err := numericPolicy(value["recentTimeoutWindowSeconds"], "recentTimeoutWindowSeconds", base.RecentTimeoutWindowSeconds)
	if err != nil {
		return nil, err
	}
	recentTimeoutPenaltyThreshold, err := numericPolicy(value["recentTimeoutPenaltyThreshold"], "recentTimeoutPenaltyThreshold", base.RecentTimeoutPenaltyThreshold)
	if err != nil {
		return nil, err
	}
	maxQueueWaitMs, err := numericPolicy(value["maxQueueWaitMs"], "maxQueueWaitMs", base.MaxQueueWaitMs)
	if err != nil {
		return nil, err
	}
	perAPIKeyQueueLimit, err := resolvePerAPIKeyQueueLimit(value["perApiKeyQueueLimit"], maxQueueSize)
	if err != nil {
		return nil, err
	}
	clientIPConcurrencyLimit, err := numericPolicy(value["clientIpConcurrencyLimit"], "clientIpConcurrencyLimit", base.ClientIPConcurrencyLimit)
	if err != nil {
		return nil, err
	}
	overflowMode, err := clientIPConcurrencyOverflowMode(value["clientIpConcurrencyOverflowMode"], base.ClientIPConcurrencyOverflowMode)
	if err != nil {
		return nil, err
	}
	imageLaneMaxConcurrency, err := numericPolicy(value["imageLaneMaxConcurrency"], "imageLaneMaxConcurrency", base.ImageLaneMaxConcurrency)
	if err != nil {
		return nil, err
	}

	return &SchedulingPolicyValues{
		Mode:                            mode,
		DefaultSoftConcurrency:          defaultSoftConcurrency,
		FastFirstEnabled:                fastFirstEnabled,
		FallbackOnQueueEnabled:          fallbackOnQueueEnabled,
		BreakAffinityOnSoftLimit:        breakAffinityOnSoftLimit,
		BreakAffinityOnQueueWaitMs:      breakAffinityOnQueueWaitMs,
		SlowRequestThresholdMs:          slowRequestThresholdMs,
		FirstOutputSlowThresholdMs:      firstOutputSlowThresholdMs,
		RecentTimeoutWindowSeconds:      recentTimeoutWindowSeconds,
		RecentTimeoutPenaltyThreshold:   recentTimeoutPenaltyThreshold,
		MaxQueueWaitMs:                  maxQueueWaitMs,
		MaxQueueSize:                    maxQueueSize,
		PerAPIKeyQueueLimit:             perAPIKeyQueueLimit,
		ClientIPConcurrencyLimit:        clientIPConcurrencyLimit,
		ClientIPConcurrencyOverflowMode: overflowMode,
		ImageLaneMaxConcurrency:         imageLaneMaxConcurrency,
	}, nil
}

func assertOnlyKeys(value map[string]any, allowedKeys []string, label string) error {
	allowed := make(map[string]struct{}, len(allowedKeys))
	for _, key := range allowedKeys {
		allowed[key] = struct{}{}
	}
	var unknownKeys []string
	for key := range value {
		if _, ok := allowed[key]; !ok {
			unknownKeys = append(unknownKeys, key)
		}
	}
	if len(unknownKeys) == 0 {
		return nil
	}
	// Node iterates Object.keys (insertion order). Map order is random in Go;
	// sort so the message stays deterministic.
	sort.Strings(unknownKeys)
	return fmt.Errorf("%s包含未知字段：%s", label, strings.Join(unknownKeys, "、"))
}

// numericPolicy mirrors numericPolicy + boundedInteger.
func numericPolicy(value any, key string, fallback int64) (int64, error) {
	max := int64(1_000_000)
	if key == "maxQueueWaitMs" {
		max = 3_600_000
	}
	min := int64(1)
	switch key {
	case "breakAffinityOnQueueWaitMs", "clientIpConcurrencyLimit", "imageLaneMaxConcurrency":
		min = 0
	}
	return boundedInteger(value, fallback, min, max, key)
}

// boundedInteger mirrors boundedInteger. JSON-decoded values arrive as
// float64; integral Go integers are accepted equivalently.
func boundedInteger(value any, fallback int64, min int64, max int64, key string) (int64, error) {
	if value == nil {
		return fallback, nil
	}
	number, ok := jsNumberValue(value)
	if !ok {
		return 0, fmt.Errorf("分组调度策略 %s 必须是整数", key)
	}
	if math.IsNaN(number) || math.IsInf(number, 0) || number != math.Trunc(number) {
		return 0, fmt.Errorf("分组调度策略 %s 必须是整数", key)
	}
	intValue := int64(number)
	if intValue < min || intValue > max {
		return 0, fmt.Errorf("分组调度策略 %s 必须在 %d-%d 之间", key, min, max)
	}
	return intValue, nil
}

func jsNumberValue(value any) (float64, bool) {
	switch number := value.(type) {
	case float64:
		return number, true
	case float32:
		return float64(number), true
	case int:
		return float64(number), true
	case int32:
		return float64(number), true
	case int64:
		return float64(number), true
	default:
		return 0, false
	}
}

// booleanPolicy mirrors booleanPolicy.
func booleanPolicy(value any, key string, fallback bool) (bool, error) {
	if value == nil {
		return fallback, nil
	}
	flag, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("分组调度策略 %s 必须是布尔值", key)
	}
	return flag, nil
}

// modePolicy mirrors modePolicy.
func modePolicy(value any, fallback string) (string, error) {
	if value == nil {
		return fallback, nil
	}
	if mode, ok := value.(string); ok && mode == "balanced_fast" {
		return mode, nil
	}
	return "", fmt.Errorf("分组调度策略 mode 无效")
}

// clientIPConcurrencyOverflowMode mirrors clientIpConcurrencyOverflowMode.
func clientIPConcurrencyOverflowMode(value any, fallback string) (string, error) {
	if value == nil {
		return fallback, nil
	}
	if mode, ok := value.(string); ok && (mode == "reject" || mode == "queue") {
		return mode, nil
	}
	return "", fmt.Errorf("分组调度策略 clientIpConcurrencyOverflowMode 无效")
}

// resolvePerAPIKeyQueueLimit mirrors resolvePerApiKeyQueueLimit.
func resolvePerAPIKeyQueueLimit(value any, maxQueueSize int64) (int64, error) {
	if value == nil {
		return maxQueueSize, nil
	}
	return boundedInteger(value, maxQueueSize, 1, maxQueueSize, "perApiKeyQueueLimit")
}

// positiveIntegerBounded mirrors positiveInteger for the effective-limit
// helpers.
func positiveIntegerBounded(value int64, fallback int64, max int64) int64 {
	result, err := boundedInteger(value, fallback, 1, max, "positiveInteger")
	if err != nil {
		return fallback
	}
	return result
}

// EffectiveSoftConcurrencyLimit mirrors effectiveSoftConcurrencyLimit with a
// raw (unvalidated) policy map: validation errors surface to the caller like
// the Node throws.
func EffectiveSoftConcurrencyLimit(accountConcurrencyLimit int64, policy map[string]any, defaults SchedulingDefaults) (int64, error) {
	resolved, err := ResolveGroupSchedulingPolicy(GroupTypeHighConcurrency, policy, defaults)
	if err != nil {
		return 0, err
	}
	return effectiveSoftConcurrencyLimitResolved(accountConcurrencyLimit, resolved), nil
}

// EffectiveImageLaneConcurrencyLimit mirrors effectiveImageLaneConcurrencyLimit
// with a raw (unvalidated) policy map.
func EffectiveImageLaneConcurrencyLimit(accountConcurrencyLimit int64, policy map[string]any, defaults SchedulingDefaults) (int64, error) {
	resolved, err := ResolveGroupSchedulingPolicy(GroupTypeHighConcurrency, policy, defaults)
	if err != nil {
		return 0, err
	}
	return effectiveImageLaneConcurrencyLimitResolved(accountConcurrencyLimit, resolved), nil
}

// effectiveSoftConcurrencyLimitResolved is the inner body of
// effectiveSoftConcurrencyLimit; a nil resolved policy means the Node
// `?? DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY` fallback.
func effectiveSoftConcurrencyLimitResolved(accountConcurrencyLimit int64, policy *SchedulingPolicyValues) int64 {
	hardLimit := positiveIntegerBounded(accountConcurrencyLimit, 1, 1_000_000)
	base := DefaultHighConcurrencyGroupSchedulingPolicy(SchedulingDefaults{}).DefaultSoftConcurrency
	if policy != nil {
		base = policy.DefaultSoftConcurrency
	}
	return int64(math.Min(float64(hardLimit), float64(int64(math.Max(1, math.Trunc(float64(base)))))))
}

// effectiveImageLaneConcurrencyLimitResolved is the inner body of
// effectiveImageLaneConcurrencyLimit.
func effectiveImageLaneConcurrencyLimitResolved(accountConcurrencyLimit int64, policy *SchedulingPolicyValues) int64 {
	hardLimit := positiveIntegerBounded(accountConcurrencyLimit, 1, 1_000_000)
	configured := DefaultHighConcurrencyGroupSchedulingPolicy(SchedulingDefaults{}).ImageLaneMaxConcurrency
	if policy != nil {
		configured = policy.ImageLaneMaxConcurrency
	}
	if configured > 0 {
		return int64(math.Min(float64(hardLimit), float64(int64(math.Max(1, math.Trunc(float64(configured)))))))
	}
	return hardLimit
}

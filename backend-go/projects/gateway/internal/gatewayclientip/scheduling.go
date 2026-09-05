package gatewayclientip

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Group scheduling policy resolution mirroring the consumed subset of
// backend/src/domain/group-scheduling.ts. The gateway runtime family only
// reads: maxQueueWaitMs, maxQueueSize, perApiKeyQueueLimit,
// imageLaneMaxConcurrency, clientIpConcurrencyLimit and
// clientIpConcurrencyOverflowMode. Node carries those fields on the resolved
// GroupSchedulingPolicy object; Go keeps the raw decoded JSON map
// (gatewayruntimecache.GroupSchedulingPolicy) and resolves here.

// HighConcurrencyPolicyDefaults carries the DEFAULT_HIGH_CONCURRENCY_GROUP_
// SCHEDULING_POLICY values the Node runtime config derives from
// runtimeConfig.concurrency.globalMax. Production wiring supplies them; zero
// values fall back to the static Node defaults where they exist.
type HighConcurrencyPolicyDefaults struct {
	// MaxQueueSize mirrors DEFAULT.maxQueueSize (= concurrency.globalMax).
	MaxQueueSize int
	// PerAPIKeyQueueLimit mirrors DEFAULT.perApiKeyQueueLimit
	// (= concurrency.globalMax).
	PerAPIKeyQueueLimit int
}

// GroupSchedulingPolicy is the resolved policy subset the runtime family
// consumes.
type GroupSchedulingPolicy struct {
	MaxQueueWaitMs                int64
	MaxQueueSize                  int
	PerAPIKeyQueueLimit           int
	ImageLaneMaxConcurrency       int
	ClientIPConcurrencyLimit      int
	ClientIPConcurrencyOverflowMode string // "reject" | "queue"
}

// resolveGroupSchedulingPolicy mirrors resolveGroupSchedulingPolicy
// ('high_concurrency', value) for the consumed fields, then falls back to
// DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY when value is nil.
// Malformed values throw like the Node boundedInteger / enum validators.
func resolveGroupSchedulingPolicy(value map[string]any, defaults HighConcurrencyPolicyDefaults) (GroupSchedulingPolicy, error) {
	maxQueueSize, err := boundedPolicyInt(value, "maxQueueSize", nonZero(defaults.MaxQueueSize, 1), 1, 1_000_000)
	if err != nil {
		return GroupSchedulingPolicy{}, err
	}
	// resolvePerApiKeyQueueLimit: unset = maxQueueSize; bounds 1..maxQueueSize.
	perAPIKeyQueueLimit := maxQueueSize
	if raw, ok := lookupPolicyValue(value, "perApiKeyQueueLimit"); ok && raw != nil {
		perAPIKeyQueueLimit, err = boundedPolicyInt(value, "perApiKeyQueueLimit", maxQueueSize, 1, maxQueueSize)
		if err != nil {
			return GroupSchedulingPolicy{}, err
		}
	}
	maxQueueWaitMs, err := boundedPolicyInt64(value, "maxQueueWaitMs", 60_000, 1, 3_600_000)
	if err != nil {
		return GroupSchedulingPolicy{}, err
	}
	imageLaneMaxConcurrency, err := boundedPolicyInt(value, "imageLaneMaxConcurrency", 0, 0, 1_000_000)
	if err != nil {
		return GroupSchedulingPolicy{}, err
	}
	clientIPConcurrencyLimit, err := boundedPolicyInt(value, "clientIpConcurrencyLimit", 0, 0, 1_000_000)
	if err != nil {
		return GroupSchedulingPolicy{}, err
	}
	overflowMode := "reject"
	if raw, ok := lookupPolicyValue(value, "clientIpConcurrencyOverflowMode"); ok && raw != nil {
		text, isText := raw.(string)
		if !isText || (text != "reject" && text != "queue") {
			return GroupSchedulingPolicy{}, fmt.Errorf("分组调度策略 clientIpConcurrencyOverflowMode 无效")
		}
		overflowMode = text
	}
	return GroupSchedulingPolicy{
		MaxQueueWaitMs:                  maxQueueWaitMs,
		MaxQueueSize:                    maxQueueSize,
		PerAPIKeyQueueLimit:             perAPIKeyQueueLimit,
		ImageLaneMaxConcurrency:         imageLaneMaxConcurrency,
		ClientIPConcurrencyLimit:        clientIPConcurrencyLimit,
		ClientIPConcurrencyOverflowMode: overflowMode,
	}, nil
}

// ResolveGroupSchedulingPolicy is the exported composition-root entry to
// resolveGroupSchedulingPolicy: the gateway chain's speed-first body
// admission gate (server.ts admitSpeedFirstRequestBody) resolves the same
// consumed queue subset off the same scheduling_policy_json payload.
func ResolveGroupSchedulingPolicy(value map[string]any, defaults HighConcurrencyPolicyDefaults) (GroupSchedulingPolicy, error) {
	return resolveGroupSchedulingPolicy(value, defaults)
}

// EffectiveImageLaneConcurrencyLimit mirrors effectiveImageLaneConcurrencyLimit.
func EffectiveImageLaneConcurrencyLimit(accountConcurrencyLimit int, policy GroupSchedulingPolicy) int {
	hardLimit := positiveIntClamp(accountConcurrencyLimit, 1, 1_000_000)
	if policy.ImageLaneMaxConcurrency > 0 {
		return minInt(hardLimit, positiveIntClamp(policy.ImageLaneMaxConcurrency, 1, 1_000_000))
	}
	return hardLimit
}

func lookupPolicyValue(value map[string]any, key string) (any, bool) {
	if value == nil {
		return nil, false
	}
	raw, ok := value[key]
	return raw, ok
}

// boundedPolicyInt mirrors numericPolicy/boundedInteger for the int fields:
// unset → fallback; non-integer → error; out of range → error.
func boundedPolicyInt(value map[string]any, key string, fallback, min, max int) (int, error) {
	raw, ok := lookupPolicyValue(value, key)
	if !ok || raw == nil {
		return fallback, nil
	}
	number, ok := toFloat64(raw)
	if !ok {
		return 0, fmt.Errorf("分组调度策略 %s 必须是整数", key)
	}
	asInt := int64(number)
	if float64(asInt) != number {
		return 0, fmt.Errorf("分组调度策略 %s 必须是整数", key)
	}
	if asInt < int64(min) || asInt > int64(max) {
		return 0, fmt.Errorf("分组调度策略 %s 必须在 %d-%d 之间", key, min, max)
	}
	return int(asInt), nil
}

func boundedPolicyInt64(value map[string]any, key string, fallback int64, min, max int64) (int64, error) {
	raw, ok := lookupPolicyValue(value, key)
	if !ok || raw == nil {
		return fallback, nil
	}
	number, ok := toFloat64(raw)
	if !ok {
		return 0, fmt.Errorf("分组调度策略 %s 必须是整数", key)
	}
	asInt := int64(number)
	if float64(asInt) != number {
		return 0, fmt.Errorf("分组调度策略 %s 必须是整数", key)
	}
	if asInt < min || asInt > max {
		return 0, fmt.Errorf("分组调度策略 %s 必须在 %d-%d 之间", key, min, max)
	}
	return asInt, nil
}

// toFloat64 mirrors the JSON-number reality of the stored policy map: values
// decoded from scheduling_policy_json are float64.
func toFloat64(raw any) (float64, bool) {
	switch number := raw.(type) {
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

// normalizeNonNegativeInteger mirrors the local helper of the Node services:
// `typeof value === 'number' ? value : Number(value)` with NaN falling back.
func normalizeNonNegativeInteger(value any, fallback int64) int64 {
	number, ok := coerceNumber(value)
	if !ok {
		return fallback
	}
	return maxInt64(0, int64(number))
}

// normalizePositiveInteger mirrors the local helper of the Node services.
func normalizePositiveInteger(value any, fallback int64) int64 {
	number, ok := coerceNumber(value)
	if !ok {
		return fallback
	}
	return maxInt64(1, int64(number))
}

// coerceNumber mirrors Number(value): numeric strings convert, everything
// else reports failure like NaN.
func coerceNumber(raw any) (float64, bool) {
	if number, ok := toFloat64(raw); ok {
		return number, true
	}
	if text, ok := raw.(string); ok {
		text = strings.TrimSpace(text)
		if text == "" {
			return 0, false
		}
		if number, err := strconv.ParseFloat(text, 64); err == nil {
			return number, true
		}
	}
	return 0, false
}

func positiveIntClamp(value, min, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func nonZero(value, fallback int) int {
	if value == 0 {
		return fallback
	}
	return value
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// sortedMapKeys keeps deterministic iteration in tests and snapshots.
func sortedMapKeys[V any](value map[string]V) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

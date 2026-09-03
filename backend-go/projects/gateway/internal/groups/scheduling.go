package groups

import (
	"encoding/json"
	"fmt"
	"sort"
)

// Group scheduling policy handling mirrors backend/src/domain/group-scheduling.ts:
// only the writable subset of the stored policy is accepted from API input,
// personal groups never carry a policy (NULL column), and high_concurrency
// groups always persist a full policy JSON rooted at the built-in defaults
// (Node DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY with
// JUHE_AI_CONCURRENCY_GLOBAL_MAX default 5000).

// GroupType mirrors GroupType (domain/types.ts).
const (
	GroupTypePersonal        = "personal"
	GroupTypeHighConcurrency = "high_concurrency"
)

// defaultGlobalConcurrencyMax mirrors runtimeConfig.concurrency.globalMax
// (JUHE_AI_CONCURRENCY_GLOBAL_MAX default).
const defaultGlobalConcurrencyMax = 5_000

// writableSchedulingPolicyKeys mirrors writableGroupSchedulingPolicyKeys.
var writableSchedulingPolicyKeys = map[string]bool{
	"defaultSoftConcurrency":          true,
	"maxQueueWaitMs":                  true,
	"clientIpConcurrencyLimit":        true,
	"clientIpConcurrencyOverflowMode": true,
	"imageLaneMaxConcurrency":         true,
}

// schedulingPolicyBounds mirrors numericPolicy/min/max per writable key.
type schedulingPolicyBound struct {
	fallback int
	min      int
	max      int
}

var schedulingPolicyBounds = map[string]schedulingPolicyBound{
	"defaultSoftConcurrency":   {fallback: defaultGlobalConcurrencyMax, min: 1, max: 1_000_000},
	"maxQueueWaitMs":           {fallback: 60_000, min: 1, max: 3_600_000},
	"clientIpConcurrencyLimit": {fallback: 0, min: 0, max: 1_000_000},
	"imageLaneMaxConcurrency":  {fallback: 0, min: 0, max: 1_000_000},
}

// defaultHighConcurrencyPolicy mirrors DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY.
func defaultHighConcurrencyPolicy() map[string]any {
	return map[string]any{
		"mode":                            "balanced_fast",
		"defaultSoftConcurrency":          defaultGlobalConcurrencyMax,
		"fastFirstEnabled":                true,
		"fallbackOnQueueEnabled":          true,
		"breakAffinityOnSoftLimit":        true,
		"breakAffinityOnQueueWaitMs":      0,
		"slowRequestThresholdMs":          30_000,
		"firstOutputSlowThresholdMs":      15_000,
		"recentTimeoutWindowSeconds":      120,
		"recentTimeoutPenaltyThreshold":   2,
		"maxQueueWaitMs":                  60_000,
		"maxQueueSize":                    defaultGlobalConcurrencyMax,
		"perApiKeyQueueLimit":             defaultGlobalConcurrencyMax,
		"clientIpConcurrencyLimit":        0,
		"clientIpConcurrencyOverflowMode": "reject",
		"imageLaneMaxConcurrency":         0,
	}
}

// normalizeGroupType mirrors normalizeGroupType (domain/group-scheduling.ts):
// absent input falls back to personal.
func normalizeGroupType(value *string) (string, error) {
	if value == nil {
		return GroupTypePersonal, nil
	}
	if *value == GroupTypePersonal || *value == GroupTypeHighConcurrency {
		return *value, nil
	}
	return "", &ValidationError{Message: "分组类型无效"}
}

// schedulingPolicyJSON mirrors groupSchedulingPolicyJson: personal groups
// store NULL; high_concurrency groups merge writable overrides into the
// built-in defaults and persist the full policy JSON. Input must be a JSON
// object (or absent) with only writable keys.
func schedulingPolicyJSON(groupType string, input any) (json.RawMessage, error) {
	if groupType != GroupTypeHighConcurrency {
		return nil, nil
	}
	object, err := policyInputObject(input)
	if err != nil {
		return nil, err
	}
	unknown := make([]string, 0, len(object))
	for key := range object {
		if !writableSchedulingPolicyKeys[key] {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, &ValidationError{Message: "分组调度策略包含未知字段：" + joinCN(unknown)}
	}
	policy := defaultHighConcurrencyPolicy()
	for _, key := range []string{"defaultSoftConcurrency", "maxQueueWaitMs", "clientIpConcurrencyLimit", "imageLaneMaxConcurrency"} {
		raw, present := object[key]
		if !present || raw == nil {
			continue
		}
		number, ok := raw.(float64)
		if !ok || number != float64(int64(number)) {
			return nil, &ValidationError{Message: fmt.Sprintf("分组调度策略 %s 必须是整数", key)}
		}
		bound := schedulingPolicyBounds[key]
		value := int(number)
		if value < bound.min || value > bound.max {
			return nil, &ValidationError{Message: fmt.Sprintf("分组调度策略 %s 必须在 %d-%d 之间", key, bound.min, bound.max)}
		}
		policy[key] = value
	}
	if raw, present := object["clientIpConcurrencyOverflowMode"]; present && raw != nil {
		mode, ok := raw.(string)
		if !ok || (mode != "reject" && mode != "queue") {
			return nil, &ValidationError{Message: "分组调度策略 clientIpConcurrencyOverflowMode 无效"}
		}
		policy["clientIpConcurrencyOverflowMode"] = mode
	}
	encoded, err := json.Marshal(policy)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

// parseSchedulingPolicy mirrors parseGroupSchedulingPolicyJson for response
// payloads: personal groups render null; high_concurrency groups render the
// stored JSON object (missing rows render null instead of the Node throw so
// legacy rows cannot 500 the read path).
func parseSchedulingPolicy(raw string, valid bool, groupType string) any {
	if groupType != GroupTypeHighConcurrency {
		return nil
	}
	if !valid || raw == "" {
		return nil
	}
	var policy any
	if err := json.Unmarshal([]byte(raw), &policy); err != nil {
		return nil
	}
	return policy
}

func policyInputObject(input any) (map[string]any, error) {
	switch typed := input.(type) {
	case nil:
		return map[string]any{}, nil
	case map[string]any:
		return typed, nil
	default:
		return nil, &ValidationError{Message: "分组调度策略无效"}
	}
}

func joinCN(values []string) string {
	out := ""
	for index, value := range values {
		if index > 0 {
			out += "、"
		}
		out += value
	}
	return out
}

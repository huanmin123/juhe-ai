package groups

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Group scheduling policy handling mirrors backend/src/domain/group-scheduling.ts:
// only the writable subset of the stored policy is accepted from API input,
// personal groups never carry a policy (NULL column), and high_concurrency
// groups always persist a full policy JSON rooted at the built-in defaults
// (Node DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY with
// runtimeConfig.concurrency.globalMax — JUHE_AI_CONCURRENCY_GLOBAL_MAX,
// default 5000 — read live for defaultSoftConcurrency/maxQueueSize/
// perApiKeyQueueLimit). Stored policies are re-validated strictly on read
// (Node parseGroupSchedulingPolicyJson throws instead of rendering broken
// rows as usable defaults).

// GroupType mirrors GroupType (domain/types.ts).
const (
	GroupTypePersonal        = "personal"
	GroupTypeHighConcurrency = "high_concurrency"
)

// defaultGlobalConcurrencyMax mirrors the runtimeConfig.concurrency.globalMax
// default (JUHE_AI_CONCURRENCY_GLOBAL_MAX). A store built without
// WithGlobalConcurrencyMax keeps this constant; the compose wiring passes the
// parsed runtime value so deployments overriding the env stay aligned.
const defaultGlobalConcurrencyMax = 5_000

// writableSchedulingPolicyKeys mirrors writableGroupSchedulingPolicyKeys.
var writableSchedulingPolicyKeys = map[string]bool{
	"defaultSoftConcurrency":          true,
	"maxQueueWaitMs":                  true,
	"clientIpConcurrencyLimit":        true,
	"clientIpConcurrencyOverflowMode": true,
	"imageLaneMaxConcurrency":         true,
}

// storedSchedulingPolicyKeys mirrors storedGroupSchedulingPolicyKeys in
// definition order (Node assertRequiredKeys reports missing keys in this
// order).
var storedSchedulingPolicyKeys = []string{
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

// schedulingPolicyBound mirrors numericPolicy's per-key min/max. The fallback
// always comes from defaultHighConcurrencyPolicy (globalMax aware).
type schedulingPolicyBound struct {
	min int
	max int
}

// numericPolicyBounds mirrors numericPolicy: max is 3_600_000 for
// maxQueueWaitMs and 1_000_000 for every other key; min is 0 for the
// wait/ip/image keys and 1 otherwise.
func numericPolicyBounds(key string) schedulingPolicyBound {
	max := 1_000_000
	if key == "maxQueueWaitMs" {
		max = 3_600_000
	}
	min := 1
	if key == "breakAffinityOnQueueWaitMs" || key == "clientIpConcurrencyLimit" || key == "imageLaneMaxConcurrency" {
		min = 0
	}
	return schedulingPolicyBound{min: min, max: max}
}

// defaultHighConcurrencyPolicy mirrors DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY
// with globalMax substituted for runtimeConfig.concurrency.globalMax.
func defaultHighConcurrencyPolicy(globalMax int) map[string]any {
	return map[string]any{
		"mode":                            "balanced_fast",
		"defaultSoftConcurrency":          globalMax,
		"fastFirstEnabled":                true,
		"fallbackOnQueueEnabled":          true,
		"breakAffinityOnSoftLimit":        true,
		"breakAffinityOnQueueWaitMs":      0,
		"slowRequestThresholdMs":          30_000,
		"firstOutputSlowThresholdMs":      15_000,
		"recentTimeoutWindowSeconds":      120,
		"recentTimeoutPenaltyThreshold":   2,
		"maxQueueWaitMs":                  60_000,
		"maxQueueSize":                    globalMax,
		"perApiKeyQueueLimit":             globalMax,
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

// normalizeStoredGroupType mirrors normalizeGroupType on read paths: unknown
// stored values are contract errors (the caller renders 500), never a silent
// personal fallback that would mask corrupted rows.
func normalizeStoredGroupType(value string) (string, error) {
	if value == GroupTypePersonal || value == GroupTypeHighConcurrency {
		return value, nil
	}
	return "", errors.New("分组类型无效")
}

// schedulingPolicyJSON mirrors groupSchedulingPolicyJson: personal groups
// store NULL; high_concurrency groups merge writable overrides into the
// built-in defaults and persist the full policy JSON. Input must be a JSON
// object (or absent) with only writable keys. globalMax stands in for
// runtimeConfig.concurrency.globalMax.
func schedulingPolicyJSON(groupType string, input any, globalMax int) (json.RawMessage, error) {
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
	policy, err := resolveHighConcurrencyPolicy(object, globalMax, false)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(policy)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

// parseStoredSchedulingPolicy mirrors parseGroupSchedulingPolicyJson +
// resolveStoredGroupSchedulingPolicy for response payloads: personal groups
// render null; high_concurrency groups require a non-empty strict policy JSON
// (missing/invalid/untyped/out-of-range rows are read errors, matching the
// Node throw that surfaces as the 500 服务器内部错误 envelope).
func parseStoredSchedulingPolicy(raw string, valid bool, groupType string, globalMax int) (any, error) {
	if groupType != GroupTypeHighConcurrency {
		return nil, nil
	}
	if !valid || strings.TrimSpace(raw) == "" {
		return nil, errors.New("高并发分组调度策略缺失")
	}
	var decoded any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return nil, err
	}
	object, ok := decoded.(map[string]any)
	if !ok {
		return nil, errors.New("分组调度策略无效")
	}
	return resolveHighConcurrencyPolicy(object, globalMax, true)
}

// resolveHighConcurrencyPolicy mirrors resolveGroupSchedulingPolicy: per-key
// type/range checks rooted at the globalMax-aware defaults. stored=true adds
// the resolveStoredGroupSchedulingPolicy strict-keys/required-keys gates
// (read path); the writable input path keeps missing keys on their defaults.
func resolveHighConcurrencyPolicy(object map[string]any, globalMax int, stored bool) (map[string]any, error) {
	defaults := defaultHighConcurrencyPolicy(globalMax)
	if stored {
		known := map[string]bool{}
		for _, key := range storedSchedulingPolicyKeys {
			known[key] = true
		}
		unknown := make([]string, 0, len(object))
		for key := range object {
			if !known[key] {
				unknown = append(unknown, key)
			}
		}
		if len(unknown) > 0 {
			sort.Strings(unknown)
			return nil, errors.New("分组调度策略包含未知字段：" + joinCN(unknown))
		}
		missing := make([]string, 0, len(storedSchedulingPolicyKeys))
		for _, key := range storedSchedulingPolicyKeys {
			if value, present := object[key]; !present || value == nil {
				missing = append(missing, key)
			}
		}
		if len(missing) > 0 {
			return nil, errors.New("分组调度策略缺少字段：" + joinCN(missing))
		}
	}

	policy := map[string]any{}
	mode, err := policyMode(object, defaults)
	if err != nil {
		return nil, err
	}
	policy["mode"] = mode
	for _, key := range []string{
		"defaultSoftConcurrency",
		"breakAffinityOnQueueWaitMs",
		"slowRequestThresholdMs",
		"firstOutputSlowThresholdMs",
		"recentTimeoutWindowSeconds",
		"recentTimeoutPenaltyThreshold",
		"maxQueueWaitMs",
		"maxQueueSize",
		"clientIpConcurrencyLimit",
		"imageLaneMaxConcurrency",
	} {
		bound := numericPolicyBounds(key)
		value, numberErr := boundedPolicyInteger(object, key, bound.min, bound.max, defaults[key])
		if numberErr != nil {
			return nil, numberErr
		}
		policy[key] = value
	}
	for _, key := range []string{"fastFirstEnabled", "fallbackOnQueueEnabled", "breakAffinityOnSoftLimit"} {
		value, boolErr := policyBoolean(object, key, defaults[key])
		if boolErr != nil {
			return nil, boolErr
		}
		policy[key] = value
	}
	overflow, err := policyOverflowMode(object, defaults)
	if err != nil {
		return nil, err
	}
	policy["clientIpConcurrencyOverflowMode"] = overflow
	// resolvePerApiKeyQueueLimit: absent falls back to the parsed maxQueueSize
	// and the accepted range is 1..maxQueueSize.
	perKeyLimit := policy["maxQueueSize"].(int)
	if value, present := object["perApiKeyQueueLimit"]; present && value != nil {
		number, ok := value.(float64)
		if !ok || number != float64(int64(number)) {
			return nil, fmt.Errorf("分组调度策略 perApiKeyQueueLimit 必须是整数")
		}
		perKeyLimit = int(number)
		if perKeyLimit < 1 || perKeyLimit > policy["maxQueueSize"].(int) {
			return nil, fmt.Errorf("分组调度策略 perApiKeyQueueLimit 必须在 1-%d 之间", policy["maxQueueSize"].(int))
		}
	}
	policy["perApiKeyQueueLimit"] = perKeyLimit
	return policy, nil
}

func boundedPolicyInteger(object map[string]any, key string, min, max int, fallback any) (int, error) {
	value, present := object[key]
	if !present || value == nil {
		return fallback.(int), nil
	}
	number, ok := value.(float64)
	if !ok || number != float64(int64(number)) {
		return 0, fmt.Errorf("分组调度策略 %s 必须是整数", key)
	}
	parsed := int(number)
	if parsed < min || parsed > max {
		return 0, fmt.Errorf("分组调度策略 %s 必须在 %d-%d 之间", key, min, max)
	}
	return parsed, nil
}

func policyBoolean(object map[string]any, key string, fallback any) (bool, error) {
	value, present := object[key]
	if !present || value == nil {
		return fallback.(bool), nil
	}
	parsed, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("分组调度策略 %s 必须是布尔值", key)
	}
	return parsed, nil
}

func policyMode(object map[string]any, defaults map[string]any) (string, error) {
	value, present := object["mode"]
	if !present || value == nil {
		return defaults["mode"].(string), nil
	}
	parsed, ok := value.(string)
	if !ok || parsed != "balanced_fast" {
		return "", errors.New("分组调度策略 mode 无效")
	}
	return parsed, nil
}

func policyOverflowMode(object map[string]any, defaults map[string]any) (string, error) {
	value, present := object["clientIpConcurrencyOverflowMode"]
	if !present || value == nil {
		return defaults["clientIpConcurrencyOverflowMode"].(string), nil
	}
	parsed, ok := value.(string)
	if !ok || (parsed != "reject" && parsed != "queue") {
		return "", errors.New("分组调度策略 clientIpConcurrencyOverflowMode 无效")
	}
	return parsed, nil
}

// validateSchedulingPolicyInput mirrors the route-level groupSchema
// schedulingPolicy object (zod strict + per-key int/min/max/enum, optional
// keys): null/float/out-of-range/unknown values are route 400s.
func validateSchedulingPolicyInput(policy map[string]any) bool {
	for key := range policy {
		if !writableSchedulingPolicyKeys[key] {
			return false
		}
	}
	for _, key := range []string{"defaultSoftConcurrency", "maxQueueWaitMs", "clientIpConcurrencyLimit", "imageLaneMaxConcurrency"} {
		raw, present := policy[key]
		if !present {
			continue
		}
		if raw == nil {
			return false
		}
		number, ok := raw.(float64)
		if !ok || number != float64(int64(number)) {
			return false
		}
		bound := schedulingPolicyBound{min: 1, max: 1_000_000}
		if key == "maxQueueWaitMs" {
			bound.max = 3_600_000
		}
		if key == "clientIpConcurrencyLimit" || key == "imageLaneMaxConcurrency" {
			bound.min = 0
		}
		if int(number) < bound.min || int(number) > bound.max {
			return false
		}
	}
	if raw, present := policy["clientIpConcurrencyOverflowMode"]; present {
		if raw == nil {
			return false
		}
		mode, ok := raw.(string)
		if !ok || (mode != "reject" && mode != "queue") {
			return false
		}
	}
	return true
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

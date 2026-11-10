package groups

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayscheduling"
)

// Group scheduling policy handling. 2026-11-09 用户裁决后只有普通分组：
// scheduling_policy_json 可空，非空时严格只含 defaultSoftConcurrency 与
// clientIpConcurrencyLimit 两个键；默认值 -1/0 表示不启用，两项都默认时存
// NULL。high_concurrency 作为退役类型只在读路径识别，写面拒绝；旧 16 键策略
// 在迁移前按 globalMax 映射为两个键（见 docs/functions/普通分组可选调度策略设计.md）。

const (
	GroupTypePersonal = "personal"
)

const (
	policyKeyDefaultSoftConcurrency   = "defaultSoftConcurrency"
	policyKeyClientIPConcurrencyLimit = "clientIpConcurrencyLimit"
)

// defaultSchedulingPolicy returns the closed policy values.
func defaultSchedulingPolicy() map[string]any {
	return map[string]any{
		policyKeyDefaultSoftConcurrency:   -1,
		policyKeyClientIPConcurrencyLimit: 0,
	}
}

// normalizeGroupType mirrors the write-face contract: absent input falls back
// to personal; high_concurrency is retired and rejected like any other unknown
// value.
func normalizeGroupType(value *string) (string, error) {
	if value == nil {
		return GroupTypePersonal, nil
	}
	if *value == GroupTypePersonal {
		return *value, nil
	}
	return "", &ValidationError{Message: "分组类型无效"}
}

// isLegacyGroupType reports whether a stored value is the retired
// high_concurrency type. Read paths normalize it to personal and map its
// policy; write paths never accept it.
func isLegacyGroupType(value string) bool {
	return value == gatewayscheduling.LegacyGroupTypeHighConcurrency
}

// normalizeStoredGroupType normalizes stored values for response/runtime use:
// personal and legacy high_concurrency both render as personal; unknown values
// remain contract errors (500), never a silent personal fallback.
func normalizeStoredGroupType(value string) (string, error) {
	if value == GroupTypePersonal {
		return value, nil
	}
	if isLegacyGroupType(value) {
		return GroupTypePersonal, nil
	}
	return "", errors.New("分组类型无效")
}

// schedulingPolicyJSON validates API input and renders the stored two-key
// policy. nil/absent means clear; an object must contain both keys; both
// default values store NULL.
func schedulingPolicyJSON(input any) (json.RawMessage, error) {
	if input == nil {
		return nil, nil
	}
	object, ok := input.(map[string]any)
	if !ok {
		return nil, &ValidationError{Message: "分组调度策略无效"}
	}
	policy, err := validatePolicyObject(object)
	if err != nil {
		return nil, err
	}
	if isDefaultSchedulingPolicy(policy) {
		return nil, nil
	}
	encoded, err := json.Marshal(policy)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

// parseStoredSchedulingPolicy reads the policy column for response/runtime
// payloads. Legacy high_concurrency rows are mapped with globalMax; non-legacy
// rows must already be the strict two-key shape. NULL/empty renders null.
func parseStoredSchedulingPolicy(raw string, valid bool, legacy bool, globalMax int) (any, error) {
	if !valid || strings.TrimSpace(raw) == "" {
		if legacy {
			return nil, errors.New("分组调度策略缺失")
		}
		return nil, nil
	}
	var decoded any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return nil, err
	}
	object, ok := decoded.(map[string]any)
	if !ok {
		return nil, errors.New("分组调度策略无效")
	}
	if legacy {
		policy, err := mapLegacySchedulingPolicy(object, globalMax)
		if err != nil {
			return nil, err
		}
		if isDefaultSchedulingPolicy(policy) {
			return nil, nil
		}
		return policy, nil
	}
	policy, err := validatePolicyObject(object)
	if err != nil {
		return nil, err
	}
	if isDefaultSchedulingPolicy(policy) {
		return nil, nil
	}
	return policy, nil
}

// mapLegacySchedulingPolicy maps the old 16-key high_concurrency policy to the
// two-key contract. It intentionally ignores retired keys, but rejects
// malformed values instead of guessing.
func mapLegacySchedulingPolicy(object map[string]any, globalMax int) (map[string]any, error) {
	threshold := -1
	if raw, present := object[policyKeyDefaultSoftConcurrency]; present && raw != nil {
		value, ok := policyInteger(raw)
		if !ok {
			return nil, fmt.Errorf("分组调度策略 %s 必须是整数", policyKeyDefaultSoftConcurrency)
		}
		// 旧默认 globalMax 等价于硬上限，不迁移成阈值。
		if value >= 1 && value < globalMax {
			threshold = value
		}
	}
	ipLimit := 0
	if raw, present := object[policyKeyClientIPConcurrencyLimit]; present && raw != nil {
		value, ok := policyInteger(raw)
		if !ok {
			return nil, fmt.Errorf("分组调度策略 %s 必须是整数", policyKeyClientIPConcurrencyLimit)
		}
		if value < 0 || value > 1_000_000 {
			return nil, fmt.Errorf("分组调度策略 %s 必须在 0-1000000 之间", policyKeyClientIPConcurrencyLimit)
		}
		ipLimit = value
	}
	return map[string]any{
		policyKeyDefaultSoftConcurrency:   threshold,
		policyKeyClientIPConcurrencyLimit: ipLimit,
	}, nil
}

// validatePolicyObject enforces the strict two-key contract shared by API
// input and stored rows after migration.
func validatePolicyObject(object map[string]any) (map[string]any, error) {
	unknown := make([]string, 0, len(object))
	for key := range object {
		if key != policyKeyDefaultSoftConcurrency && key != policyKeyClientIPConcurrencyLimit {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, &ValidationError{Message: "分组调度策略包含未知字段：" + joinCN(unknown)}
	}
	missing := make([]string, 0, 2)
	for _, key := range []string{policyKeyDefaultSoftConcurrency, policyKeyClientIPConcurrencyLimit} {
		if value, present := object[key]; !present || value == nil {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return nil, &ValidationError{Message: "分组调度策略缺少字段：" + joinCN(missing)}
	}
	threshold, ok := policyInteger(object[policyKeyDefaultSoftConcurrency])
	if !ok {
		return nil, &ValidationError{Message: "分组调度策略 defaultSoftConcurrency 必须是整数"}
	}
	if threshold != -1 && (threshold < 1 || threshold > 1_000_000) {
		return nil, &ValidationError{Message: "分组调度策略 defaultSoftConcurrency 必须是 -1 或 1-1000000"}
	}
	ipLimit, ok := policyInteger(object[policyKeyClientIPConcurrencyLimit])
	if !ok {
		return nil, &ValidationError{Message: "分组调度策略 clientIpConcurrencyLimit 必须是整数"}
	}
	if ipLimit < 0 || ipLimit > 1_000_000 {
		return nil, &ValidationError{Message: "分组调度策略 clientIpConcurrencyLimit 必须在 0-1000000 之间"}
	}
	return map[string]any{
		policyKeyDefaultSoftConcurrency:   threshold,
		policyKeyClientIPConcurrencyLimit: ipLimit,
	}, nil
}

func isDefaultSchedulingPolicy(policy map[string]any) bool {
	threshold, _ := policyInteger(policy[policyKeyDefaultSoftConcurrency])
	ipLimit, _ := policyInteger(policy[policyKeyClientIPConcurrencyLimit])
	return threshold == -1 && ipLimit == 0
}

// validateSchedulingPolicyInput is the route-level check for API objects: two
// keys, both present, integer/range valid. null/float/unknown values are 400s.
func validateSchedulingPolicyInput(policy map[string]any) bool {
	if _, err := validatePolicyObject(policy); err != nil {
		return false
	}
	return true
}

func policyInteger(raw any) (int, bool) {
	switch value := raw.(type) {
	case float64:
		number := int64(value)
		if float64(number) != value {
			return 0, false
		}
		return int(number), true
	case float32:
		number := int64(value)
		if float32(number) != value {
			return 0, false
		}
		return int(number), true
	case int:
		return value, true
	case int32:
		return int(value), true
	case int64:
		return int(value), true
	default:
		return 0, false
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

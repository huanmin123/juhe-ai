package groups

import (
	"encoding/json"
	"errors"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayscheduling"
)

// Group scheduling policy handling. 2026-11-19 用户裁决后只有普通分组：
// scheduling_policy_json 可空，非空时严格只含 defaultSoftConcurrency 一个键；
// 默认值 -1 表示不启用，默认时存 NULL。单 IP 并发限制已迁至 API Key
// （api_keys.client_ip_concurrency_limit）。读路径兼容 revision 22 的旧两键
// JSON 并忽略 clientIpConcurrencyLimit；写面只接受单键，旧 IP 键按未知键
// 拒绝。high_concurrency 作为退役类型只在读路径识别，写面拒绝；旧 16 键策略
// 在迁移前按 globalMax 映射（见 docs/functions/普通分组可选调度策略设计.md、
// docs/functions/APIKey单IP并发限制设计.md）。

const (
	GroupTypePersonal = "personal"
)

const policyKeyDefaultSoftConcurrency = gatewayscheduling.KeyDefaultSoftConcurrency

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

// schedulingPolicyJSON validates API input and renders the stored single-key
// policy. nil/absent means clear; an object must contain only
// defaultSoftConcurrency; the default -1 stores NULL.
func schedulingPolicyJSON(input any) (json.RawMessage, error) {
	if input == nil {
		return nil, nil
	}
	object, ok := input.(map[string]any)
	if !ok {
		return nil, &ValidationError{Message: "分组调度策略无效"}
	}
	threshold, err := validatePolicyObject(object)
	if err != nil {
		return nil, err
	}
	if threshold == -1 {
		return nil, nil
	}
	encoded, err := json.Marshal(map[string]any{policyKeyDefaultSoftConcurrency: threshold})
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

// parseStoredSchedulingPolicy reads the policy column for response/runtime
// payloads. Legacy high_concurrency rows are mapped with globalMax; non-legacy
// rows accept the single key plus the retired clientIpConcurrencyLimit key
// (read-compatible, ignored). NULL/empty renders null.
func parseStoredSchedulingPolicy(raw string, valid bool, legacy bool, globalMax int) (any, error) {
	policy, err := gatewayscheduling.ParseStored(raw, valid, legacy, globalMax)
	if err != nil {
		return nil, err
	}
	if policy.IsDefault() {
		return nil, nil
	}
	return map[string]any{policyKeyDefaultSoftConcurrency: policy.DefaultSoftConcurrency}, nil
}

// validatePolicyObject enforces the strict single-key contract shared by API
// input and returns the normalized threshold. The retired
// clientIpConcurrencyLimit key is rejected as unknown on the write face.
func validatePolicyObject(object map[string]any) (int, error) {
	policy, err := gatewayscheduling.ParseWrite(object)
	if err != nil {
		return 0, &ValidationError{Message: err.Error()}
	}
	return policy.DefaultSoftConcurrency, nil
}

// validateSchedulingPolicyInput is the route-level check for API objects:
// exactly one key, present, integer/range valid. null/float/unknown values are
// 400s.
func validateSchedulingPolicyInput(policy map[string]any) bool {
	if _, err := validatePolicyObject(policy); err != nil {
		return false
	}
	return true
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

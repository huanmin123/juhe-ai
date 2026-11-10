// Package gatewayscheduling owns the retired high_concurrency policy mapping and
// the two-key ordinary-group policy contract shared by the management plane,
// runtime cache and dispatch. It has no store or HTTP dependencies.
package gatewayscheduling

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

const (
	GroupTypePersonal = "personal"
	// LegacyGroupTypeHighConcurrency 只用于迁移前旧行归一，不允许写入。
	LegacyGroupTypeHighConcurrency = "high_concurrency"
	DefaultGlobalConcurrencyMax    = 5_000
)

const (
	KeyDefaultSoftConcurrency   = "defaultSoftConcurrency"
	KeyClientIPConcurrencyLimit = "clientIpConcurrencyLimit"
)

// Policy is the closed ordinary-group scheduling policy: -1/0 mean disabled.
type Policy struct {
	DefaultSoftConcurrency   int `json:"defaultSoftConcurrency"`
	ClientIPConcurrencyLimit int `json:"clientIpConcurrencyLimit"`
}

func DefaultPolicy() Policy {
	return Policy{DefaultSoftConcurrency: -1, ClientIPConcurrencyLimit: 0}
}

func (p Policy) IsDefault() bool {
	return p.DefaultSoftConcurrency == -1 && p.ClientIPConcurrencyLimit == 0
}

// SoftLimit returns the effective soft threshold for an account hard limit;
// 0 means disabled.
func (p Policy) SoftLimit(hardLimit int) int {
	if p.DefaultSoftConcurrency <= 0 {
		return 0
	}
	if hardLimit < 1 {
		hardLimit = 1
	}
	if p.DefaultSoftConcurrency < hardLimit {
		return p.DefaultSoftConcurrency
	}
	return hardLimit
}

func (p Policy) SoftBusy(currentConcurrency, hardLimit int) bool {
	limit := p.SoftLimit(hardLimit)
	return limit > 0 && currentConcurrency >= limit
}

// NormalizeGroupType maps the retired type to personal for read paths and
// reports whether the raw value was legacy. Empty/whitespace is invalid data
// (column NOT NULL DEFAULT 'personal'), never a silent personal fallback.
func NormalizeGroupType(value string) (normalized string, legacy bool, err error) {
	value = strings.TrimSpace(value)
	if value == GroupTypePersonal {
		return GroupTypePersonal, false, nil
	}
	if value == LegacyGroupTypeHighConcurrency {
		return GroupTypePersonal, true, nil
	}
	return "", false, errors.New("分组类型无效")
}

// ParseStored parses scheduling_policy_json for runtime/response use. NULL or
// empty yields the default policy. Legacy rows map the old 16-key policy with
// globalMax; non-legacy rows must be the strict two-key shape.
func ParseStored(raw string, valid bool, legacy bool, globalMax int) (Policy, error) {
	if !valid || strings.TrimSpace(raw) == "" {
		if legacy {
			return Policy{}, errors.New("分组调度策略缺失")
		}
		return DefaultPolicy(), nil
	}
	var decoded any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return Policy{}, err
	}
	object, ok := decoded.(map[string]any)
	if !ok {
		return Policy{}, errors.New("分组调度策略无效")
	}
	return ParseMap(object, legacy, globalMax)
}

func ParseMap(object map[string]any, legacy bool, globalMax int) (Policy, error) {
	if legacy {
		return mapLegacyPolicy(object, globalMax)
	}
	return validateTwoKeyPolicy(object)
}

func mapLegacyPolicy(object map[string]any, globalMax int) (Policy, error) {
	if globalMax <= 0 {
		globalMax = DefaultGlobalConcurrencyMax
	}
	policy := DefaultPolicy()
	if raw, present := object[KeyDefaultSoftConcurrency]; present && raw != nil {
		value, ok := policyInteger(raw)
		if !ok {
			return Policy{}, fmt.Errorf("分组调度策略 %s 必须是整数", KeyDefaultSoftConcurrency)
		}
		// 旧默认 globalMax 等价于硬上限，不迁移成阈值。
		if value >= 1 && value < globalMax {
			policy.DefaultSoftConcurrency = value
		}
	}
	if raw, present := object[KeyClientIPConcurrencyLimit]; present && raw != nil {
		value, ok := policyInteger(raw)
		if !ok {
			return Policy{}, fmt.Errorf("分组调度策略 %s 必须是整数", KeyClientIPConcurrencyLimit)
		}
		if value < 0 || value > 1_000_000 {
			return Policy{}, fmt.Errorf("分组调度策略 %s 必须在 0-1000000 之间", KeyClientIPConcurrencyLimit)
		}
		policy.ClientIPConcurrencyLimit = value
	}
	return policy, nil
}

func validateTwoKeyPolicy(object map[string]any) (Policy, error) {
	unknown := make([]string, 0, len(object))
	for key := range object {
		if key != KeyDefaultSoftConcurrency && key != KeyClientIPConcurrencyLimit {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return Policy{}, fmt.Errorf("分组调度策略包含未知字段：%s", strings.Join(unknown, "、"))
	}
	missing := make([]string, 0, 2)
	for _, key := range []string{KeyDefaultSoftConcurrency, KeyClientIPConcurrencyLimit} {
		if value, present := object[key]; !present || value == nil {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return Policy{}, fmt.Errorf("分组调度策略缺少字段：%s", strings.Join(missing, "、"))
	}
	threshold, ok := policyInteger(object[KeyDefaultSoftConcurrency])
	if !ok {
		return Policy{}, fmt.Errorf("分组调度策略 %s 必须是整数", KeyDefaultSoftConcurrency)
	}
	if threshold != -1 && (threshold < 1 || threshold > 1_000_000) {
		return Policy{}, fmt.Errorf("分组调度策略 %s 必须是 -1 或 1-1000000", KeyDefaultSoftConcurrency)
	}
	ipLimit, ok := policyInteger(object[KeyClientIPConcurrencyLimit])
	if !ok {
		return Policy{}, fmt.Errorf("分组调度策略 %s 必须是整数", KeyClientIPConcurrencyLimit)
	}
	if ipLimit < 0 || ipLimit > 1_000_000 {
		return Policy{}, fmt.Errorf("分组调度策略 %s 必须在 0-1000000 之间", KeyClientIPConcurrencyLimit)
	}
	return Policy{DefaultSoftConcurrency: threshold, ClientIPConcurrencyLimit: ipLimit}, nil
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

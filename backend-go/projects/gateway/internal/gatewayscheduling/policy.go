// Package gatewayscheduling owns the retired high_concurrency policy mapping and
// the single-key ordinary-group policy contract shared by the management plane,
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

const KeyDefaultSoftConcurrency = "defaultSoftConcurrency"

// legacyKeyClientIPConcurrencyLimit 是已迁至 API Key 的旧分组策略键：
// 读取旧两键 JSON 时识别并忽略（仅读兼容），写面把该键当未知键拒绝。
const legacyKeyClientIPConcurrencyLimit = "clientIpConcurrencyLimit"

// Policy is the closed ordinary-group scheduling policy: -1 means disabled.
type Policy struct {
	DefaultSoftConcurrency int `json:"defaultSoftConcurrency"`
}

func DefaultPolicy() Policy {
	return Policy{DefaultSoftConcurrency: -1}
}

func (p Policy) IsDefault() bool {
	return p.DefaultSoftConcurrency == -1
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
// globalMax; non-legacy rows accept the single-key shape plus the retired
// clientIpConcurrencyLimit key, which is read-compatible and ignored.
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
	return parseSingleKeyPolicy(object)
}

// ParseWrite validates a management-plane create/PATCH policy object against
// the strict single-key contract: the retired clientIpConcurrencyLimit key is
// no longer accepted on the write face and is rejected as unknown.
func ParseWrite(object map[string]any) (Policy, error) {
	return validateSingleKeyPolicy(object, false)
}

// mapLegacyPolicy maps the retired 16-key high_concurrency policy onto the
// single-key contract. The migrated clientIpConcurrencyLimit key is ignored.
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
	return policy, nil
}

// parseSingleKeyPolicy is the read-side validator: only defaultSoftConcurrency
// is accepted as data; the retired clientIpConcurrencyLimit key is tolerated
// and ignored (revision 25 moved it to API Key), any other key is rejected.
func parseSingleKeyPolicy(object map[string]any) (Policy, error) {
	return validateSingleKeyPolicy(object, true)
}

func validateSingleKeyPolicy(object map[string]any, allowLegacyIPKey bool) (Policy, error) {
	unknown := make([]string, 0, len(object))
	for key := range object {
		legacyIPKey := allowLegacyIPKey && key == legacyKeyClientIPConcurrencyLimit
		if key != KeyDefaultSoftConcurrency && !legacyIPKey {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return Policy{}, fmt.Errorf("分组调度策略包含未知字段：%s", strings.Join(unknown, "、"))
	}
	raw, present := object[KeyDefaultSoftConcurrency]
	if !present || raw == nil {
		return Policy{}, fmt.Errorf("分组调度策略缺少字段：%s", KeyDefaultSoftConcurrency)
	}
	threshold, ok := policyInteger(raw)
	if !ok {
		return Policy{}, fmt.Errorf("分组调度策略 %s 必须是整数", KeyDefaultSoftConcurrency)
	}
	if threshold != -1 && (threshold < 1 || threshold > 1_000_000) {
		return Policy{}, fmt.Errorf("分组调度策略 %s 必须是 -1 或 1-1000000", KeyDefaultSoftConcurrency)
	}
	return Policy{DefaultSoftConcurrency: threshold}, nil
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

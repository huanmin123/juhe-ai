package accountbalance

import (
	"fmt"
	"regexp"
	"strings"
)

const MultiKeyMessage = "多 Key 账户不支持余额查询，保存后将自动关闭余额查询"

type CustomConfig struct {
	Path             string `json:"path"`
	RemainingPointer string `json:"remainingPointer,omitempty"`
	TotalPointer     string `json:"totalPointer,omitempty"`
	UsedPointer      string `json:"usedPointer,omitempty"`
	Divisor          string `json:"divisor,omitempty"`
}

type QueryConfig struct {
	Adapter                 Adapter       `json:"adapter"`
	IntervalMinutes         int           `json:"intervalMinutes"`
	PreferredBuiltinAdapter Adapter       `json:"preferredBuiltinAdapter,omitempty"`
	Custom                  *CustomConfig `json:"custom,omitempty"`
}

type CapabilityInput struct {
	Type               string
	Credentials        map[string]any
	AuthorizedInstance bool
}

type CapabilityDecision struct {
	Enabled                     bool
	AutoDisabledForMultipleKeys bool
}

var jsonPointerPattern = regexp.MustCompile(`^(?:/(?:[^~/]|~[01])*)*$`)

// NormalizeConfig validates the strict J2 query configuration and fills the
// current default refresh interval. It deliberately accepts only map-shaped
// decoded JSON so unknown fields can be rejected by the caller before this
// normalized value crosses a storage boundary.
func NormalizeConfig(input map[string]any) (QueryConfig, error) {
	for key := range input {
		switch key {
		case "adapter", "intervalMinutes", "preferredBuiltinAdapter", "custom":
		default:
			return QueryConfig{}, fmt.Errorf("余额查询配置包含未知字段：%s", key)
		}
	}
	adapterText, ok := input["adapter"].(string)
	if !ok || (adapterText != "builtin" && adapterText != "custom") {
		return QueryConfig{}, fmt.Errorf("余额查询类型无效")
	}
	interval := 5
	if value, present := input["intervalMinutes"]; present {
		parsed, ok := integerValue(value)
		if !ok || parsed < 1 || parsed > 10 {
			return QueryConfig{}, fmt.Errorf("余额刷新周期无效")
		}
		interval = parsed
	}
	result := QueryConfig{Adapter: Adapter(adapterText), IntervalMinutes: interval}
	if value, present := input["preferredBuiltinAdapter"]; present {
		preferred, ok := value.(string)
		if !ok || !isBuiltinAdapter(Adapter(preferred)) {
			return QueryConfig{}, fmt.Errorf("余额查询适配器偏好无效")
		}
		result.PreferredBuiltinAdapter = Adapter(preferred)
	}
	if value, present := input["custom"]; present {
		customMap, ok := value.(map[string]any)
		if !ok {
			return QueryConfig{}, fmt.Errorf("自定义查询配置无效")
		}
		custom, err := normalizeCustomConfig(customMap)
		if err != nil {
			return QueryConfig{}, err
		}
		result.Custom = &custom
	}
	if result.Adapter == Adapter("custom") && result.Custom == nil {
		return QueryConfig{}, fmt.Errorf("自定义查询必须提供查询配置")
	}
	if result.Adapter == Adapter("builtin") && result.Custom != nil {
		return QueryConfig{}, fmt.Errorf("内置查询类型不能提供自定义配置")
	}
	if result.Adapter == Adapter("custom") && result.PreferredBuiltinAdapter != "" {
		return QueryConfig{}, fmt.Errorf("自定义查询不能提供内置适配偏好")
	}
	return result, nil
}

func normalizeCustomConfig(input map[string]any) (CustomConfig, error) {
	for key := range input {
		switch key {
		case "path", "remainingPointer", "totalPointer", "usedPointer", "divisor":
		default:
			return CustomConfig{}, fmt.Errorf("自定义查询配置包含未知字段：%s", key)
		}
	}
	path, ok := input["path"].(string)
	path = strings.TrimSpace(path)
	if !ok || path == "" || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return CustomConfig{}, fmt.Errorf("自定义查询地址必须是同源相对路径")
	}
	custom := CustomConfig{Path: path}
	for key, target := range map[string]*string{
		"remainingPointer": &custom.RemainingPointer,
		"totalPointer":     &custom.TotalPointer,
		"usedPointer":      &custom.UsedPointer,
	} {
		if value, present := input[key]; present {
			text, ok := value.(string)
			if !ok || !jsonPointerPattern.MatchString(strings.TrimSpace(text)) {
				return CustomConfig{}, fmt.Errorf("%s 必须是合法 JSON Pointer", key)
			}
			*target = strings.TrimSpace(text)
		}
	}
	hasRemaining := custom.RemainingPointer != ""
	hasTotalAndUsed := custom.TotalPointer != "" && custom.UsedPointer != ""
	if hasRemaining == hasTotalAndUsed {
		return CustomConfig{}, fmt.Errorf("自定义查询必须配置余额 JSON Pointer，或同时配置总额和已用 JSON Pointer")
	}
	if value, present := input["divisor"]; present {
		text, ok := value.(string)
		if !ok {
			return CustomConfig{}, fmt.Errorf("自定义金额除数必须是正数")
		}
		text = strings.TrimSpace(text)
		divisor, err := parseDecimal(text, "divisor")
		if err != nil || divisor.coefficient.Sign() <= 0 {
			return CustomConfig{}, fmt.Errorf("自定义金额除数必须是正数")
		}
		custom.Divisor = text
	}
	return custom, nil
}

func ValidateCapability(input CapabilityInput, enabled bool) (CapabilityDecision, error) {
	if input.AuthorizedInstance {
		if enabled {
			return CapabilityDecision{}, fmt.Errorf("授权实例不能配置上游余额查询")
		}
		return CapabilityDecision{}, nil
	}
	if enabled && input.Type != "api_key" {
		return CapabilityDecision{}, fmt.Errorf("上游余额查询仅支持 API Key 账户")
	}
	keyCount := len(EffectiveAPIKeys(input.Credentials))
	if input.Type == "api_key" && keyCount > 1 {
		return CapabilityDecision{AutoDisabledForMultipleKeys: true}, nil
	}
	if !enabled {
		return CapabilityDecision{}, nil
	}
	if keyCount != 1 {
		return CapabilityDecision{}, fmt.Errorf("上游余额查询需要一个有效的 API Key")
	}
	return CapabilityDecision{Enabled: true}, nil
}

func EffectiveAPIKeys(credentials map[string]any) []string {
	seen := map[string]struct{}{}
	keys := make([]string, 0)
	appendKey := func(value any) {
		text, ok := value.(string)
		if !ok {
			return
		}
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		if _, exists := seen[text]; exists {
			return
		}
		seen[text] = struct{}{}
		keys = append(keys, text)
	}
	if pool, ok := credentials["api_keys"].([]any); ok {
		for _, value := range pool {
			appendKey(value)
		}
	} else if pool, ok := credentials["api_keys"].([]string); ok {
		for _, value := range pool {
			appendKey(value)
		}
	}
	if len(keys) > 0 {
		return keys
	}
	appendKey(credentials["api_key"])
	return keys
}

func integerValue(value any) (int, bool) {
	switch number := value.(type) {
	case int:
		return number, true
	case int64:
		return int(number), int64(int(number)) == number
	case float64:
		return int(number), number == float64(int(number))
	default:
		return 0, false
	}
}

func isBuiltinAdapter(adapter Adapter) bool {
	switch adapter {
	case AdapterSub2API, AdapterNewAPI, AdapterOpenAIBilling, AdapterLiteLLM, AdapterUserBalance:
		return true
	default:
		return false
	}
}

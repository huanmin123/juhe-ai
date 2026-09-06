package accounts

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// Balance query config validation: the port of
// backend/src/modules/accounts/account-balance-config.ts (the
// accountBalanceQueryConfigSchema strict object plus
// normalizeAccountBalanceConfig / validateAccountBalanceCapability /
// effectiveAccountApiKeys). The create/PATCH body parsers run the normalize
// step the same way the Node routes do, so an illegal non-empty config fails
// with 400 even when balanceQueryEnabled stays false.

var (
	balanceBuiltinAdapterValues = map[string]bool{
		"sub2api": true, "newapi": true, "openai_billing": true,
		"litellm": true, "user_balance": true,
	}
	balanceDecimalPattern     = regexp.MustCompile(`^(?:0|[1-9]\d*)(?:\.\d+)?$`)
	balanceAllZeroPattern     = regexp.MustCompile(`^0(?:\.0+)?$`)
	balanceJSONPointerPattern = regexp.MustCompile(`^(?:\/(?:[^~/]|~[01])*)*$`)
)

// NormalizeAccountBalanceConfig mirrors normalizeAccountBalanceConfig: strict
// unknown-key rejection, per-field validation and the canonical record shape
// (intervalMinutes defaults to 5). The returned map is the exact object the
// Node repository JSON.stringify writes into balance_query_config_json.
func NormalizeAccountBalanceConfig(input any) (map[string]any, error) {
	record, ok := input.(map[string]any)
	if !ok {
		return nil, balanceConfigError("余额查询配置无效")
	}
	for key := range record {
		switch key {
		case "adapter", "intervalMinutes", "preferredBuiltinAdapter", "custom":
		default:
			return nil, balanceConfigError("余额查询配置无效")
		}
	}
	adapter, ok := record["adapter"].(string)
	if !ok {
		return nil, balanceConfigError("余额查询类型无效")
	}
	if adapter != "builtin" && adapter != "custom" {
		return nil, balanceConfigError("余额查询类型无效")
	}
	intervalMinutes := float64(5)
	if value, exists := record["intervalMinutes"]; exists && value != nil {
		number, ok := value.(float64)
		if !ok || number != float64(int(number)) || number < 1 || number > 10 {
			return nil, balanceConfigError("余额刷新周期无效")
		}
		intervalMinutes = number
	}
	preferredBuiltinAdapter := ""
	if value, exists := record["preferredBuiltinAdapter"]; exists && value != nil {
		text, ok := value.(string)
		if !ok || !balanceBuiltinAdapterValues[text] {
			return nil, balanceConfigError("余额查询配置无效")
		}
		preferredBuiltinAdapter = text
	}
	customValue, hasCustom := record["custom"]
	custom, err := normalizeBalanceCustomConfig(customValue, hasCustom)
	if err != nil {
		return nil, err
	}
	if adapter == "custom" && !hasCustom {
		return nil, balanceConfigError("自定义查询必须提供查询配置")
	}
	if adapter != "custom" && hasCustom {
		return nil, balanceConfigError("内置查询类型不能提供自定义配置")
	}
	if adapter == "custom" && preferredBuiltinAdapter != "" {
		return nil, balanceConfigError("自定义查询不能提供内置适配偏好")
	}

	canonical := map[string]any{
		"adapter":         adapter,
		"intervalMinutes": intervalMinutes,
	}
	if preferredBuiltinAdapter != "" {
		canonical["preferredBuiltinAdapter"] = preferredBuiltinAdapter
	}
	if custom != nil {
		canonical["custom"] = custom
	}
	return canonical, nil
}

// normalizeBalanceCustomConfig mirrors accountBalanceCustomConfigSchema
// (strict object + superRefine). Returns nil when the custom block is absent.
func normalizeBalanceCustomConfig(value any, present bool) (map[string]any, error) {
	if !present || value == nil {
		return nil, nil
	}
	record, ok := value.(map[string]any)
	if !ok {
		return nil, balanceConfigError("余额查询配置无效")
	}
	for key := range record {
		switch key {
		case "path", "remainingPointer", "totalPointer", "usedPointer", "divisor":
		default:
			return nil, balanceConfigError("余额查询配置无效")
		}
	}
	path, ok := record["path"].(string)
	if !ok {
		return nil, balanceConfigError("自定义查询地址必须是同源相对路径")
	}
	path = strings.TrimSpace(path)
	if path == "" || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return nil, balanceConfigError("自定义查询地址必须是同源相对路径")
	}
	custom := map[string]any{"path": path}
	pointers := map[string]string{}
	for _, field := range []string{"remainingPointer", "totalPointer", "usedPointer"} {
		value, exists := record[field]
		if !exists || value == nil {
			continue
		}
		text, ok := value.(string)
		if !ok {
			return nil, balanceConfigError(field + " 必须是合法 JSON Pointer")
		}
		trimmed := strings.TrimSpace(text)
		if !balanceJSONPointerPattern.MatchString(trimmed) {
			return nil, balanceConfigError(field + " 必须是合法 JSON Pointer")
		}
		pointers[field] = trimmed
		custom[field] = trimmed
	}
	hasRemaining := pointers["remainingPointer"] != ""
	hasTotalAndUsed := pointers["totalPointer"] != "" && pointers["usedPointer"] != ""
	if hasRemaining == hasTotalAndUsed {
		return nil, balanceConfigError("自定义查询必须配置余额 JSON Pointer，或同时配置总额和已用 JSON Pointer")
	}
	if value, exists := record["divisor"]; exists && value != nil {
		text, ok := value.(string)
		if !ok {
			return nil, balanceConfigError("自定义金额除数必须是正数")
		}
		trimmed := strings.TrimSpace(text)
		if !balanceDecimalPattern.MatchString(trimmed) || balanceAllZeroPattern.MatchString(trimmed) {
			return nil, balanceConfigError("自定义金额除数必须是正数")
		}
		custom["divisor"] = trimmed
	}
	return custom, nil
}

func balanceConfigError(message string) error {
	return &ValidationError{Message: message}
}

// EffectiveAccountApiKeys mirrors effectiveAccountApiKeys: the deduplicated,
// trimmed pool (api_keys wins over the legacy api_key singleton).
func EffectiveAccountApiKeys(credentials Credentials) []string {
	pool := []any{}
	if credentials != nil {
		if list, ok := credentials["api_keys"].([]any); ok {
			pool = list
		}
	}
	seen := map[string]bool{}
	keys := []string{}
	for _, item := range pool {
		text, ok := item.(string)
		if !ok {
			continue
		}
		trimmed := strings.TrimSpace(text)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		keys = append(keys, trimmed)
	}
	if len(keys) > 0 {
		return keys
	}
	if credentials != nil {
		if legacy, ok := credentials["api_key"].(string); ok {
			if trimmed := strings.TrimSpace(legacy); trimmed != "" {
				return []string{trimmed}
			}
		}
	}
	return []string{}
}

// EffectiveAccountApiKeyCount mirrors effectiveAccountApiKeyCount.
func EffectiveAccountApiKeyCount(credentials Credentials) int {
	return len(EffectiveAccountApiKeys(credentials))
}

// BalanceCapabilityInput mirrors AccountBalanceCapabilityInput.
type BalanceCapabilityInput struct {
	AccountType        string
	Credentials        Credentials
	AuthorizedInstance bool
}

// ValidateAccountBalanceCapability mirrors validateAccountBalanceCapability:
// authorized instances can never enable the query, non-API-Key accounts are
// rejected and an enabled query needs at least one effective key. Returns the
// (possibly unchanged) enabled decision.
func ValidateAccountBalanceCapability(account BalanceCapabilityInput, enabled bool) (bool, error) {
	if account.AuthorizedInstance {
		if enabled {
			return false, balanceConfigError("授权实例不能配置上游余额查询")
		}
		return false, nil
	}
	if enabled && account.AccountType != "api_key" {
		return false, balanceConfigError("上游余额查询仅支持 API Key 账户")
	}
	if !enabled {
		return false, nil
	}
	if EffectiveAccountApiKeyCount(account.Credentials) < 1 {
		return false, balanceConfigError("上游余额查询需要至少一个有效的 API Key")
	}
	return true, nil
}

// balanceAPIKeyFingerprint mirrors accountBalanceApiKeyFingerprint: a stable
// server-side HMAC identity for one Key; the raw credential never leaves the
// backend. The HMAC key material is the store secret (runtimeConfig.secret).
func (s *Store) balanceAPIKeyFingerprint(value string) string {
	if value == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(s.secret))
	mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))
}

// normalizedBalanceBaseURL mirrors normalizedBalanceBaseUrl: URL-normalized
// origin+path without hash/query and trailing slashes; a non-parsing value
// keeps its trimmed text minus trailing slashes.
func normalizedBalanceBaseURL(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	parsed, err := url.Parse(text)
	if err == nil && parsed.Scheme != "" && parsed.Host != "" {
		parsed.Fragment = ""
		parsed.RawFragment = ""
		parsed.RawQuery = ""
		rendered := parsed.String()
		return strings.TrimRight(rendered, "/")
	}
	return strings.TrimRight(text, "/")
}

// canonicalBalanceConfigJSON renders the normalized config as the compact
// JSON text the repository writes (JSON.stringify shape; consumers only
// deep-compare parsed values).
func canonicalBalanceConfigJSON(config map[string]any) (string, error) {
	raw, err := json.Marshal(config)
	if err != nil {
		return "", fmt.Errorf("余额查询配置序列化失败: %w", err)
	}
	return string(raw), nil
}

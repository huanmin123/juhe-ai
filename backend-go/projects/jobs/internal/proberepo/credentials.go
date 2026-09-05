package proberepo

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/oauthrefresh"
)

// 本文件移植 Node storage/account-api-key-rotation.ts 的凭据池窄投影：
// accountApiKeyEntries / fingerprintAccountApiKey / isAccountApiKeyPoolIsolationEnabled，
// 以及 storage/crypto.ts decryptJson（复用 oauthrefresh 的 AES v1 信封实现）。

// KeyEntry 等价 Node AccountApiKeyEntry。
type KeyEntry struct {
	ID          string
	Key         string
	Fingerprint string
	Index       int
	Weight      int
}

// AccountAPIKeyEntries 等价 accountApiKeyEntries：api_keys 池优先，回落单
// api_key；去空白、去重，指纹 = HMAC-SHA256(secret, key) hex。
func (s *Store) AccountAPIKeyEntries(credentials map[string]any) []KeyEntry {
	var rawKeys []any
	if list, ok := credentials["api_keys"].([]any); ok && len(list) > 0 {
		rawKeys = list
	} else {
		rawKeys = []any{credentials["api_key"]}
	}
	weights, _ := credentials["api_key_weights"].([]any)
	entries := make([]KeyEntry, 0, len(rawKeys))
	seen := map[string]bool{}
	for index, value := range rawKeys {
		text, ok := value.(string)
		if !ok {
			continue
		}
		key := strings.TrimSpace(text)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		entries = append(entries, KeyEntry{
			ID:          s.FingerprintAPIKey(key),
			Key:         key,
			Fingerprint: s.FingerprintAPIKey(key),
			Index:       index,
			Weight:      normalizeAPIKeyWeight(mapIndex(weights, index)),
		})
	}
	return entries
}

func mapIndex(values []any, index int) any {
	if index >= 0 && index < len(values) {
		return values[index]
	}
	return nil
}

func normalizeAPIKeyWeight(value any) int {
	number, ok := asFloat(value)
	if !ok {
		return 1
	}
	if number >= 1 && number <= 100 {
		return int(number)
	}
	return 1
}

func asFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	}
	return 0, false
}

// FingerprintAPIKey 等价 fingerprintAccountApiKey。
func (s *Store) FingerprintAPIKey(key string) string {
	mac := hmac.New(sha256.New, []byte(s.secret))
	mac.Write([]byte(key))
	return hex.EncodeToString(mac.Sum(nil))
}

// DecryptCredentials 等价 decryptJson<Record<string, unknown>>。
func (s *Store) DecryptCredentials(envelope string) (map[string]any, error) {
	var credentials map[string]any
	if err := oauthrefresh.DecryptJSON(s.secret, envelope, &credentials); err != nil {
		return nil, err
	}
	return credentials, nil
}

// IsAccountAPIKeyPoolIsolationEnabled 等价 isAccountApiKeyPoolIsolationEnabled：
// 仅 api_key 类型 + 支持的 provider/protocol + 池内 Key 数 > 1。
func (s *Store) IsAccountAPIKeyPoolIsolationEnabled(providerCode, protocolCode, protocolVersion, accountType string, credentials map[string]any) bool {
	if accountType != "api_key" {
		return false
	}
	if !isAccountAPIKeyPoolProviderSupported(providerCode, protocolCode, protocolVersion) {
		return false
	}
	return len(s.AccountAPIKeyEntries(credentials)) > 1
}

// isAccountAPIKeyPoolProviderSupported 对齐 Node isAccountApiKeyPoolProviderSupported：
// openai / gpt / openai-compatible（OPENAI_COMPATIBLE_PROVIDER_CODE='openai'）、
// deepseek、glm、gemini、hybrid provider，或 anthropic 协议 profile / anthropic provider。
func isAccountAPIKeyPoolProviderSupported(providerCode, protocolCode, protocolVersion string) bool {
	code := normalizeProviderToken(providerCode)
	switch code {
	case "openai", "gpt", "openai-compatible", "deepseek", "glm", "gemini", "hybrid", "anthropic":
		return true
	}
	return normalizeProviderToken(protocolCode) == "anthropic"
}

func normalizeProviderToken(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

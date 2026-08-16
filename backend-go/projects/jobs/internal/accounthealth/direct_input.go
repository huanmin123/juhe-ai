package accounthealth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"
	"time"
)

// DirectInput is the typed result expected from the PostgreSQL read-only
// adapter. Keeping business evidence here prevents a SQL row with an omitted
// authorization/source/proxy condition from silently becoming probeable.
type DirectInput struct {
	Account       DirectAccount
	Authorization *DirectAuthorization
	Source        *DirectSource
	Binding       DirectBinding
	Proxy         *DirectProxy
	InputVersion  int64
	IssuedAt      time.Time
	ExpiresAt     time.Time
	TLSPolicy     string
	Schedule      Schedule
}

type DirectAccount struct {
	ID                   string
	ConfigRevision       int64
	DispatchRevision     int64
	Provider             string
	Type                 string
	Status               string
	Schedulable          bool
	EndpointMode         string
	HealthModel          string
	CredentialsEncrypted string
	AccountExpiresAt     *time.Time
	CooldownUntil        *time.Time
	Cooldown             *CooldownFence
}

type DirectSource struct {
	ID                   string
	ConfigRevision       int64
	Provider             string
	Type                 string
	Status               string
	Schedulable          bool
	AccountExpiresAt     *time.Time
	CooldownUntil        *time.Time
	LastErrorCode        string
	CredentialsEncrypted string
}

type DirectAuthorization struct {
	ID            string
	Status        string
	ExpiresAt     *time.Time
	QuotaEligible bool
}

type DirectBinding struct {
	GroupID                string
	Enabled                bool
	AuthorizationBindingID string
}

type DirectProxy struct {
	ID                string
	Enabled           bool
	Type              string
	Host              string
	Port              int
	Username          string
	PasswordEncrypted string
}

func (d DirectInput) ToInput(secret string, now time.Time) (Input, error) {
	if strings.TrimSpace(secret) == "" {
		return Input{}, fmt.Errorf("PG direct input 缺少业务凭据 secret")
	}
	if err := validateDirectAccount(d.Account, now); err != nil {
		return Input{}, err
	}
	if !d.Binding.Enabled || strings.TrimSpace(d.Binding.GroupID) == "" {
		return Input{}, fmt.Errorf("PG direct input 缺少启用的 group binding")
	}
	if d.InputVersion < 1 || d.IssuedAt.IsZero() || d.ExpiresAt.IsZero() || !d.ExpiresAt.After(now) {
		return Input{}, fmt.Errorf("PG direct input 的 epoch 或有效期无效")
	}
	if strings.TrimSpace(d.TLSPolicy) == "" {
		return Input{}, fmt.Errorf("PG direct input 缺少 TLS policy version")
	}
	effective := d.Account
	sourceRevision := (*int64)(nil)
	if d.Authorization != nil || d.Source != nil {
		if err := validateDirectAuthorization(d, now); err != nil {
			return Input{}, err
		}
		effective.Provider = d.Source.Provider
		effective.Type = d.Source.Type
		effective.CredentialsEncrypted = d.Source.CredentialsEncrypted
		sourceRevision = int64Pointer(d.Source.ConfigRevision)
	}
	if effective.Provider != "openai" || effective.Type != d.Account.Type {
		return Input{}, fmt.Errorf("PG direct input 的有效来源 provider/type 不受 J1 支持")
	}
	result := Input{
		AccountID:        d.Account.ID,
		InputVersion:     d.InputVersion,
		ConfigRevision:   d.Account.ConfigRevision,
		DispatchRevision: d.Account.DispatchRevision,
		Provider:         d.Account.Provider,
		Type:             d.Account.Type,
		EndpointMode:     d.Account.EndpointMode,
		HealthModel:      d.Account.HealthModel,
		IssuedAt:         d.IssuedAt.UTC(),
		ExpiresAt:        d.ExpiresAt.UTC(),
		TLSPolicyVersion: d.TLSPolicy,
		Eligibility:      Eligibility{AccountStatus: d.Account.Status, Schedulable: d.Account.Schedulable, BoundGroup: true, AuthorizationEligible: true, SourceConfigRevision: sourceRevision, CooldownUntil: cloneTime(d.Account.CooldownUntil)},
		Cooldown:         d.Account.Cooldown,
		Schedule:         d.Schedule,
	}
	if (d.Account.Status == "temporary_unavailable" || d.Account.Status == "rate_limited") && !validCooldownFence(d.Account.Cooldown, result) {
		return Input{}, fmt.Errorf("PG direct input 的冷却账户缺少完整 fence")
	}
	credentials, err := decryptJSONObject(secret, effective.CredentialsEncrypted, "账户凭据")
	if err != nil {
		return Input{}, err
	}
	result.BaseURL = directBaseURL(credentials, result.Type)
	if d.Proxy != nil {
		proxy, err := directProxyEnvelope(secret, *d.Proxy)
		if err != nil {
			return Input{}, err
		}
		result.Proxy = &proxy
	}
	if result.Type == "api_key" {
		keys := directAPIKeys(credentials)
		if len(keys) == 0 {
			return Input{}, fmt.Errorf("PG direct input 的 API Key pool 为空")
		}
		for index, key := range keys {
			ciphertext, err := EncryptV1Envelope(secret, []byte(`{"api_key":`+mustJSON(key)+`}`))
			if err != nil {
				return Input{}, err
			}
			fingerprint := directKeyFingerprint(secret, key)
			result.APIKeys = append(result.APIKeys, APIKeyInput{Index: index, Fingerprint: fingerprint, Credential: CredentialEnvelope{Kind: "api_key", Ciphertext: ciphertext}})
		}
		result.KeySetFingerprint = directKeySetFingerprint(result.APIKeys)
		return result, nil
	}
	accessToken, ok := directString(credentials, "access_token")
	if !ok {
		return Input{}, fmt.Errorf("PG direct input 的 OAuth access token 缺失")
	}
	expiresAt, ok := directTime(credentials, "expires_at")
	if !ok || !expiresAt.After(now.Add(time.Minute)) {
		return Input{}, fmt.Errorf("PG direct input 的 OAuth access token 已到期或接近到期")
	}
	ciphertext, err := EncryptV1Envelope(secret, []byte(`{"access_token":`+mustJSON(accessToken)+`}`))
	if err != nil {
		return Input{}, err
	}
	result.OAuthAccess = &CredentialEnvelope{Kind: "oauth_access", Ciphertext: ciphertext}
	result.OAuthExpiresAt = &expiresAt
	if accountID, found := directString(credentials, "account_id"); found {
		result.OAuthAccountID = accountID
	} else if accountID, found := directString(credentials, "chatgpt_user_id"); found {
		result.OAuthAccountID = accountID
	}
	return result, nil
}

func validateDirectAccount(account DirectAccount, now time.Time) error {
	if strings.TrimSpace(account.ID) == "" || account.ConfigRevision < 1 || account.DispatchRevision < 1 {
		return fmt.Errorf("PG direct input 的账户或 revision 无效")
	}
	if account.Provider != "openai" || (account.Type != "api_key" && account.Type != "oauth") {
		return fmt.Errorf("PG direct input 的 provider/type 不受 J1 支持")
	}
	if account.EndpointMode != "chat_json" && account.EndpointMode != "responses_json" && account.EndpointMode != "images_json" {
		return fmt.Errorf("PG direct input 的 endpoint mode 不受 J1 支持")
	}
	if account.Type == "oauth" && account.EndpointMode != "responses_json" {
		return fmt.Errorf("PG direct input 的 OAuth 仅支持 responses_json")
	}
	if account.Status != "active" && account.Status != "pending_test" && account.Status != "temporary_unavailable" && account.Status != "rate_limited" {
		return fmt.Errorf("PG direct input 的账户状态不可探活")
	}
	if account.Status != "pending_test" && !account.Schedulable {
		return fmt.Errorf("PG direct input 的 active 账户不可调度")
	}
	if account.AccountExpiresAt != nil && !account.AccountExpiresAt.After(now) {
		return fmt.Errorf("PG direct input 的账户已到期")
	}
	if account.CooldownUntil != nil && account.CooldownUntil.After(now) {
		return fmt.Errorf("PG direct input 的账户仍在冷却")
	}
	if strings.TrimSpace(account.HealthModel) == "" || strings.TrimSpace(account.CredentialsEncrypted) == "" {
		return fmt.Errorf("PG direct input 缺少健康模型或凭据")
	}
	return nil
}

func validateDirectAuthorization(input DirectInput, now time.Time) error {
	if input.Authorization == nil || input.Source == nil {
		return fmt.Errorf("PG direct input 的授权账户缺少 authorization/source")
	}
	authorization := input.Authorization
	source := input.Source
	if strings.TrimSpace(authorization.ID) == "" || authorization.Status != "active" || !authorization.QuotaEligible || (authorization.ExpiresAt != nil && !authorization.ExpiresAt.After(now)) {
		return fmt.Errorf("PG direct input 的授权不可用")
	}
	if input.Binding.AuthorizationBindingID != authorization.ID {
		return fmt.Errorf("PG direct input 的授权 group binding 不匹配")
	}
	if strings.TrimSpace(source.ID) == "" || source.ConfigRevision < 1 || source.Provider != "openai" || source.Type != input.Account.Type || source.Status != "active" || !source.Schedulable || source.LastErrorCode == "account_expired" || strings.TrimSpace(source.CredentialsEncrypted) == "" {
		return fmt.Errorf("PG direct input 的物理来源账户不可用")
	}
	if source.AccountExpiresAt != nil && !source.AccountExpiresAt.After(now) {
		return fmt.Errorf("PG direct input 的物理来源账户已到期")
	}
	if source.CooldownUntil != nil && source.CooldownUntil.After(now) {
		return fmt.Errorf("PG direct input 的物理来源账户仍在冷却")
	}
	return nil
}

func decryptJSONObject(secret, encrypted, label string) (map[string]json.RawMessage, error) {
	plaintext, err := DecryptV1Envelope(secret, encrypted)
	if err != nil {
		return nil, fmt.Errorf("解封 PG direct input %s失败: %w", label, err)
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(plaintext, &result); err != nil {
		return nil, fmt.Errorf("解析 PG direct input %s失败", label)
	}
	return result, nil
}

func directAPIKeys(credentials map[string]json.RawMessage) []string {
	values := make([]string, 0)
	if raw, found := credentials["api_keys"]; found {
		_ = json.Unmarshal(raw, &values)
	}
	if len(values) == 0 {
		if key, found := directString(credentials, "api_key"); found {
			values = []string{key}
		}
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		key := strings.TrimSpace(value)
		if key == "" {
			continue
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, key)
	}
	return result
}

func directProxyEnvelope(secret string, proxy DirectProxy) (CredentialEnvelope, error) {
	if strings.TrimSpace(proxy.ID) == "" || !proxy.Enabled || proxy.Port < 1 || proxy.Port > 65535 || net.ParseIP(proxy.Host) == nil && strings.TrimSpace(proxy.Host) == "" {
		return CredentialEnvelope{}, fmt.Errorf("PG direct input 的 proxy profile 不可用")
	}
	scheme := proxy.Type
	if scheme == "socks5" {
		scheme = "socks5h"
	}
	if scheme != "http" && scheme != "https" && scheme != "socks5h" {
		return CredentialEnvelope{}, fmt.Errorf("PG direct input 的 proxy 协议不受支持")
	}
	endpoint := &url.URL{Scheme: scheme, Host: net.JoinHostPort(proxy.Host, fmt.Sprintf("%d", proxy.Port))}
	if strings.TrimSpace(proxy.Username) != "" {
		if strings.TrimSpace(proxy.PasswordEncrypted) == "" {
			return CredentialEnvelope{}, fmt.Errorf("PG direct input 的 proxy password 缺失")
		}
		password, err := decryptJSONObject(secret, proxy.PasswordEncrypted, "proxy password")
		if err != nil {
			return CredentialEnvelope{}, err
		}
		secretValue, found := directString(password, "password")
		if !found {
			return CredentialEnvelope{}, fmt.Errorf("PG direct input 的 proxy password 缺失")
		}
		endpoint.User = url.UserPassword(proxy.Username, secretValue)
	}
	ciphertext, err := EncryptV1Envelope(secret, []byte(`{"url":`+mustJSON(endpoint.String())+`}`))
	if err != nil {
		return CredentialEnvelope{}, err
	}
	return CredentialEnvelope{Kind: "proxy_url", Ciphertext: ciphertext}, nil
}

func directBaseURL(credentials map[string]json.RawMessage, accountType string) string {
	if value, found := directString(credentials, "base_url"); found {
		return strings.TrimRight(value, "/")
	}
	if accountType == "oauth" {
		return "https://chatgpt.com/backend-api/codex"
	}
	return "https://api.openai.com"
}

func directString(values map[string]json.RawMessage, key string) (string, bool) {
	var value string
	if raw, found := values[key]; !found || json.Unmarshal(raw, &value) != nil || strings.TrimSpace(value) == "" {
		return "", false
	}
	return strings.TrimSpace(value), true
}

func directTime(values map[string]json.RawMessage, key string) (time.Time, bool) {
	value, found := directString(values, key)
	if !found {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339, value)
	return parsed, err == nil
}

func directKeyFingerprint(secret, key string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(key))
	return hex.EncodeToString(mac.Sum(nil))
}

func directKeySetFingerprint(keys []APIKeyInput) string {
	fingerprints := make([]string, 0, len(keys))
	for _, key := range keys {
		fingerprints = append(fingerprints, key.Fingerprint)
	}
	sort.Strings(fingerprints)
	var hash uint32 = 2166136261
	for _, fingerprint := range fingerprints {
		for _, character := range fingerprint {
			hash ^= uint32(character)
			hash *= 16777619
		}
		hash ^= 0
		hash *= 16777619
	}
	return fmt.Sprintf("%08x", hash)
}

func mustJSON(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func int64Pointer(value int64) *int64 { return &value }

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	result := value.UTC()
	return &result
}

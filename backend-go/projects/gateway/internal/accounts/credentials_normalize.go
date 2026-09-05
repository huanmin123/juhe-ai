package accounts

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Credentials write normalization (第 1 段): the port of
// backend/src/storage/account-credentials-normalization.ts. Every write of
// accounts.credentials_encrypted funnels through
// NormalizeAccountCredentialsForWrite: unknown-key rejection, per-type
// required/optional field handling, endpoint-mode defaults via the credential
// driver registry, policy attachments and the overall JSON size cap. The
// output seals through the shared AES v1 envelope (crypto.go EncryptJSON).

const (
	accountCredentialBaseURLMaxBytes  = 2048
	accountCredentialSecretMaxBytes   = 16 * 1024
	accountCredentialMetadataMaxBytes = 4096
	accountCredentialsJSONMaxBytes    = 32 * 1024
	accountAPIKeyListMaxItems         = 10
)

var apiKeyAccountCredentialKeys = map[string]bool{
	"api_key": true, "api_keys": true, "api_key_strategy": true, "api_key_weights": true,
	"base_url": true, "supported_endpoint_modes": true, "service_tier_override": true,
	"reasoning_effort_override": true, "error_handling_rules": true,
	"error_handling_rule_overrides": true, "response_inspection_rules": true,
	"quota_recovery_policy": true,
}

var oauthAccountCredentialKeys = map[string]bool{
	"access_token": true, "refresh_token": true, "expires_at": true, "client_id": true,
	"id_token": true, "token_type": true, "scope": true, "email": true, "account_id": true,
	"chatgpt_account_id": true, "organization_id": true, "chatgpt_user_id": true,
	"plan_type": true, "sub": true, "team_id": true, "subscription_tier": true,
	"entitlement_status": true, "base_url": true, "supported_endpoint_modes": true,
	"service_tier_override": true, "reasoning_effort_override": true,
	"error_handling_rules": true, "error_handling_rule_overrides": true,
	"response_inspection_rules": true, "quota_recovery_policy": true,
}

var googleOAuthAccountCredentialKeys = map[string]bool{
	"access_token": true, "refresh_token": true, "expires_at": true, "client_id": true,
	"client_secret": true, "quota_project_id": true, "oauth_type": true, "project_id": true,
	"tier_id": true, "scope": true, "token_type": true, "drive_storage_limit": true,
	"drive_storage_usage": true, "drive_tier_updated_at": true, "base_url": true,
	"supported_endpoint_modes": true, "service_tier_override": true,
	"reasoning_effort_override": true, "error_handling_rules": true,
	"error_handling_rule_overrides": true, "response_inspection_rules": true,
	"quota_recovery_policy": true,
}

var deprecatedAccountCredentialKeys = map[string]bool{
	"codex_responses_safe_repair_enabled":      true,
	"codex_responses_strict_intercept_enabled": true,
}

// EndpointModeDefaultContext mirrors ProviderAccountCredentialContext: the
// provider profile identity the endpoint-mode defaults resolve against.
type EndpointModeDefaultContext struct {
	ProviderCode              string
	AccountType               string
	ClientCompatibility       string
	ProtocolCode              string
	ProtocolVersion           string
	ProviderProtocolProfileID string
}

// modeContextWith pins the account type exactly like the Node call sites that
// spread `{ ...endpointModeDefaults, accountType: '<type>' }` into the
// endpoint-mode defaults.
func (c EndpointModeDefaultContext) modeContextWith(accountType string) endpointModeDefaultContext {
	copied := c.modeContext()
	copied.accountType = accountType
	return copied
}

func (c EndpointModeDefaultContext) modeContext() endpointModeDefaultContext {
	return endpointModeDefaultContext{
		providerCode:              c.ProviderCode,
		accountType:               c.AccountType,
		protocolCode:              c.ProtocolCode,
		protocolVersion:           c.ProtocolVersion,
		providerProtocolProfileID: c.ProviderProtocolProfileID,
		clientCompatibility:       c.ClientCompatibility,
	}
}

// NormalizeAccountCredentialsForWrite mirrors normalizeAccountCredentialsForWrite.
// A nil defaults context means Node's `{ accountType }` fallback.
func NormalizeAccountCredentialsForWrite(accountType string, value Credentials, defaults *EndpointModeDefaultContext) (Credentials, error) {
	if defaults == nil {
		defaults = &EndpointModeDefaultContext{AccountType: accountType}
	}
	input, err := accountCredentialsRecord(value)
	if err != nil {
		return nil, err
	}
	input = stripDeprecatedAccountCredentialKeys(input)
	allowedKeys, err := accountCredentialAllowedKeys(accountType)
	if err != nil {
		return nil, err
	}
	if err := assertKnownInputKeys(input, allowedKeys, "账户凭据"); err != nil {
		return nil, err
	}
	switch accountType {
	case "api_key":
		normalized, err := normalizeAPIKeyAccountCredentials(input, *defaults)
		if err != nil {
			return nil, err
		}
		return canonicalizeNormalizedCredentials(normalized)
	case "oauth":
		normalized, err := normalizeOAuthAccountCredentials(input, *defaults)
		if err != nil {
			return nil, err
		}
		return canonicalizeNormalizedCredentials(normalized)
	case "google_oauth":
		normalized, err := normalizeGoogleOAuthAccountCredentials(input, *defaults)
		if err != nil {
			return nil, err
		}
		return canonicalizeNormalizedCredentials(normalized)
	}
	return nil, &ValidationError{Message: "账户类型 " + accountType + " 不支持凭据写入"}
}

func accountCredentialsRecord(value Credentials) (map[string]any, error) {
	if value == nil {
		return map[string]any{}, nil
	}
	return map[string]any(value), nil
}

func stripDeprecatedAccountCredentialKeys(input map[string]any) map[string]any {
	found := false
	for key := range input {
		if deprecatedAccountCredentialKeys[key] {
			found = true
			break
		}
	}
	if !found {
		return input
	}
	sanitized := make(map[string]any, len(input))
	for key, value := range input {
		if deprecatedAccountCredentialKeys[key] {
			continue
		}
		sanitized[key] = value
	}
	return sanitized
}

func accountCredentialAllowedKeys(accountType string) (map[string]bool, error) {
	switch accountType {
	case "api_key":
		return apiKeyAccountCredentialKeys, nil
	case "oauth":
		return oauthAccountCredentialKeys, nil
	case "google_oauth":
		return googleOAuthAccountCredentialKeys, nil
	}
	return nil, &ValidationError{Message: "账户类型 " + accountType + " 不支持凭据写入"}
}

func normalizeAPIKeyAccountCredentials(input map[string]any, defaults EndpointModeDefaultContext) (Credentials, error) {
	baseURL, err := requiredCredentialTextInput(input["base_url"], "Base URL", accountCredentialBaseURLMaxBytes)
	if err != nil {
		return nil, err
	}
	if err := assertSafeUpstreamBaseURL(baseURL); err != nil {
		return nil, err
	}
	apiKeys, err := normalizeAPIKeyCredentialList(input)
	if err != nil {
		return nil, err
	}
	credentials := Credentials{
		"api_key":  apiKeys[0],
		"base_url": baseURL,
	}
	modes, err := normalizeEndpointModesForWrite(credentialField(input, "supported_endpoint_modes"), defaults.modeContextWith("api_key"))
	if err != nil {
		return nil, err
	}
	credentials["supported_endpoint_modes"] = modes
	if len(apiKeys) > 1 {
		credentials["api_keys"] = anyStrings(apiKeys)
		strategy := normalizeAPIKeyStrategy(credentialField(input, "api_key_strategy").value)
		credentials["api_key_strategy"] = strategy
		if strategy == "weighted_round_robin" {
			weights, err := normalizeAPIKeyWeights(credentialField(input, "api_key_weights").value, len(apiKeys))
			if err != nil {
				return nil, err
			}
			credentials["api_key_weights"] = weights
		}
	}
	if err := normalizeAccountCredentialPolicies(input, credentials); err != nil {
		return nil, err
	}
	if err := normalizeGPTAccountRequestOverrides(input, credentials, defaults); err != nil {
		return nil, err
	}
	if err := assertAccountCredentialsJSONSize(credentials); err != nil {
		return nil, err
	}
	return credentials, nil
}

// credentialField renders the optionalValue tri-state from the raw record.
func credentialField(input map[string]any, key string) optionalValue {
	value, present := input[key]
	return optionalValue{value: value, present: present}
}

func (c endpointModeDefaultContext) modeContextWith(accountType string) endpointModeDefaultContext {
	copied := c
	copied.accountType = accountType
	return copied
}

func anyStrings(values []string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

func normalizeAPIKeyCredentialList(input map[string]any) ([]string, error) {
	rawList, listPresent := input["api_keys"].([]any)
	var sourceValues []any
	if listPresent && len(rawList) > 0 {
		sourceValues = rawList
	} else if apiKey, present := input["api_key"]; present {
		sourceValues = []any{apiKey}
	} else {
		sourceValues = []any{nil}
	}
	if len(sourceValues) > accountAPIKeyListMaxItems {
		return nil, &ValidationError{Message: fmt.Sprintf("单个账户最多配置 %d 个 API Key", accountAPIKeyListMaxItems)}
	}
	output := []string{}
	seen := map[string]bool{}
	for _, value := range sourceValues {
		key, err := requiredCredentialTextInput(value, "API Key", accountCredentialSecretMaxBytes)
		if err != nil {
			return nil, err
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		output = append(output, key)
	}
	if len(output) == 0 {
		return nil, &ValidationError{Message: "API Key不能为空"}
	}
	return output, nil
}

func normalizeAPIKeyStrategy(value any) string {
	switch value {
	case "failover":
		return "failover"
	case "weighted_round_robin":
		return "weighted_round_robin"
	case "round_robin":
		return "round_robin"
	}
	return "failover"
}

func normalizeAPIKeyWeights(value any, count int) ([]any, error) {
	list, _ := value.([]any)
	output := make([]any, 0, count)
	for index := 0; index < count; index++ {
		weight, err := normalizeAPIKeyWeight(list, index)
		if err != nil {
			return nil, err
		}
		output = append(output, weight)
	}
	return output, nil
}

func normalizeAPIKeyWeight(list []any, index int) (float64, error) {
	if index >= len(list) || list[index] == nil {
		return 1, nil
	}
	number, ok := list[index].(float64)
	if !ok || number != float64(int64(number)) || number < 1 || number > 100 {
		return 0, &ValidationError{Message: "API Key 权重必须是 1-100 之间的整数"}
	}
	return number, nil
}

func normalizeOAuthAccountCredentials(input map[string]any, defaults EndpointModeDefaultContext) (Credentials, error) {
	accessToken, err := optionalCredentialText(credentialField(input, "access_token"), "Access Token", accountCredentialSecretMaxBytes)
	if err != nil {
		return nil, err
	}
	refreshToken, err := optionalCredentialText(credentialField(input, "refresh_token"), "Refresh Token", accountCredentialSecretMaxBytes)
	if err != nil {
		return nil, err
	}
	anthropicProfile := isAnthropicProtocolProfileOf(defaults.modeContext().predicate())
	if anthropicProfile && accessToken == "" {
		return nil, &ValidationError{Message: "Anthropic OAuth Access Token 不能为空"}
	}
	if !anthropicProfile && refreshToken == "" && accessToken == "" {
		return nil, &ValidationError{Message: "OAuth 凭据不能为空"}
	}
	baseURL, err := requiredCredentialTextInput(input["base_url"], "Base URL", accountCredentialBaseURLMaxBytes)
	if err != nil {
		return nil, err
	}
	modes, err := normalizeEndpointModesForWrite(credentialField(input, "supported_endpoint_modes"), defaults.modeContextWith("oauth"))
	if err != nil {
		return nil, err
	}
	credentials := Credentials{"base_url": baseURL, "supported_endpoint_modes": modes}
	if err := assertSafeUpstreamBaseURL(baseURL); err != nil {
		return nil, err
	}
	if accessToken != "" {
		credentials["access_token"] = accessToken
	}
	if refreshToken != "" {
		credentials["refresh_token"] = refreshToken
	}
	expiresAt, err := optionalCredentialDateTime(credentialField(input, "expires_at"), "Access Token 到期时间")
	if err != nil {
		return nil, err
	}
	if expiresAt != "" {
		credentials["expires_at"] = expiresAt
	}
	if err := copyOptionalCredentialText(input, credentials, "client_id", "OAuth client_id", accountCredentialMetadataMaxBytes); err != nil {
		return nil, err
	}
	if err := copyOptionalCredentialText(input, credentials, "id_token", "OAuth id_token", accountCredentialSecretMaxBytes); err != nil {
		return nil, err
	}
	if err := copyOptionalCredentialText(input, credentials, "token_type", "OAuth token_type", accountCredentialMetadataMaxBytes); err != nil {
		return nil, err
	}
	if err := copyOptionalCredentialText(input, credentials, "scope", "OAuth scope", accountCredentialMetadataMaxBytes); err != nil {
		return nil, err
	}
	if err := copyOptionalCredentialText(input, credentials, "email", "OAuth email", accountCredentialMetadataMaxBytes); err != nil {
		return nil, err
	}
	openAIAccountID, err := optionalCredentialText(credentialField(input, "account_id"), "OpenAI account_id", accountCredentialMetadataMaxBytes)
	if err != nil {
		return nil, err
	}
	if openAIAccountID == "" {
		openAIAccountID, err = optionalCredentialText(credentialField(input, "chatgpt_account_id"), "OpenAI chatgpt_account_id", accountCredentialMetadataMaxBytes)
		if err != nil {
			return nil, err
		}
	}
	if defaults.ProviderCode == gptVendorCode && accessToken != "" && openAIAccountID == "" {
		return nil, &ValidationError{Message: "OpenAI OAuth Access Token 缺少 account_id"}
	}
	if openAIAccountID != "" {
		credentials["account_id"] = openAIAccountID
	}
	pairs := []struct {
		key, label string
		maxBytes   int
	}{
		{"organization_id", "Anthropic organization_id", accountCredentialMetadataMaxBytes},
		{"chatgpt_user_id", "OpenAI chatgpt_user_id", accountCredentialMetadataMaxBytes},
		{"plan_type", "OpenAI plan_type", accountCredentialMetadataMaxBytes},
		{"sub", "xAI subject", accountCredentialMetadataMaxBytes},
		{"team_id", "xAI team_id", accountCredentialMetadataMaxBytes},
		{"subscription_tier", "xAI subscription_tier", accountCredentialMetadataMaxBytes},
		{"entitlement_status", "xAI entitlement_status", accountCredentialMetadataMaxBytes},
	}
	for _, pair := range pairs {
		if err := copyOptionalCredentialText(input, credentials, pair.key, pair.label, pair.maxBytes); err != nil {
			return nil, err
		}
	}
	if err := normalizeAccountCredentialPolicies(input, credentials); err != nil {
		return nil, err
	}
	if err := normalizeGPTAccountRequestOverrides(input, credentials, defaults); err != nil {
		return nil, err
	}
	if err := assertAccountCredentialsJSONSize(credentials); err != nil {
		return nil, err
	}
	return credentials, nil
}

func normalizeGoogleOAuthAccountCredentials(input map[string]any, defaults EndpointModeDefaultContext) (Credentials, error) {
	accessToken, err := optionalCredentialText(credentialField(input, "access_token"), "Google Access Token", accountCredentialSecretMaxBytes)
	if err != nil {
		return nil, err
	}
	refreshToken, err := optionalCredentialText(credentialField(input, "refresh_token"), "Google Refresh Token", accountCredentialSecretMaxBytes)
	if err != nil {
		return nil, err
	}
	if accessToken == "" && refreshToken == "" {
		return nil, &ValidationError{Message: "Google OAuth 凭据不能为空"}
	}
	clientID, err := optionalCredentialText(credentialField(input, "client_id"), "Google OAuth Client ID", accountCredentialMetadataMaxBytes)
	if err != nil {
		return nil, err
	}
	clientSecret, err := optionalCredentialText(credentialField(input, "client_secret"), "Google OAuth Client Secret", accountCredentialSecretMaxBytes)
	if err != nil {
		return nil, err
	}
	if refreshToken != "" && (clientID == "" || clientSecret == "") {
		return nil, &ValidationError{Message: "Google Refresh Token 需要同时配置 Client ID 和 Client Secret"}
	}
	baseURL, err := requiredCredentialTextInput(input["base_url"], "Base URL", accountCredentialBaseURLMaxBytes)
	if err != nil {
		return nil, err
	}
	modes, err := normalizeEndpointModesForWrite(credentialField(input, "supported_endpoint_modes"), defaults.modeContextWith("google_oauth"))
	if err != nil {
		return nil, err
	}
	credentials := Credentials{"base_url": baseURL, "supported_endpoint_modes": modes}
	if err := assertSafeUpstreamBaseURL(baseURL); err != nil {
		return nil, err
	}
	if accessToken != "" {
		credentials["access_token"] = accessToken
	}
	if refreshToken != "" {
		credentials["refresh_token"] = refreshToken
	}
	if clientID != "" {
		credentials["client_id"] = clientID
	}
	if clientSecret != "" {
		credentials["client_secret"] = clientSecret
	}
	expiresAt, err := optionalCredentialDateTime(credentialField(input, "expires_at"), "Google Access Token 到期时间")
	if err != nil {
		return nil, err
	}
	if expiresAt != "" {
		credentials["expires_at"] = expiresAt
	}
	textPairs := []struct {
		key, label string
		maxBytes   int
	}{
		{"quota_project_id", "Google Quota Project ID", accountCredentialMetadataMaxBytes},
		{"oauth_type", "Gemini OAuth 类型", accountCredentialMetadataMaxBytes},
		{"project_id", "Gemini Project ID", accountCredentialMetadataMaxBytes},
		{"tier_id", "Gemini Tier ID", accountCredentialMetadataMaxBytes},
		{"scope", "Google OAuth scope", accountCredentialMetadataMaxBytes},
		{"token_type", "Google OAuth token_type", accountCredentialMetadataMaxBytes},
		{"drive_tier_updated_at", "Google Drive tier 更新时间", accountCredentialMetadataMaxBytes},
	}
	for _, pair := range textPairs {
		if err := copyOptionalCredentialText(input, credentials, pair.key, pair.label, pair.maxBytes); err != nil {
			return nil, err
		}
	}
	for _, pair := range []struct{ key, label string }{
		{"drive_storage_limit", "Google Drive 存储上限"},
		{"drive_storage_usage", "Google Drive 已用存储"},
	} {
		if err := copyOptionalCredentialNonNegativeInteger(input, credentials, pair.key, pair.label); err != nil {
			return nil, err
		}
	}
	if err := normalizeAccountCredentialPolicies(input, credentials); err != nil {
		return nil, err
	}
	if err := normalizeGPTAccountRequestOverrides(input, credentials, defaults); err != nil {
		return nil, err
	}
	if err := assertAccountCredentialsJSONSize(credentials); err != nil {
		return nil, err
	}
	return credentials, nil
}

// normalizeAccountCredentialPolicies mirrors normalizeAccountCredentialPolicies.
func normalizeAccountCredentialPolicies(input map[string]any, credentials Credentials) error {
	if raw, present := input["error_handling_rules"]; present {
		rules, err := normalizeAccountErrorHandlingRules(raw)
		if err != nil {
			return err
		}
		credentials["error_handling_rules"] = rules
	}
	if raw, present := input["error_handling_rule_overrides"]; present {
		overrides, err := normalizeAccountErrorPolicyOverrides(raw)
		if err != nil {
			return err
		}
		credentials["error_handling_rule_overrides"] = overrides
	}
	if raw, present := input["response_inspection_rules"]; present {
		rules, err := normalizeAccountResponseInspectionRules(raw)
		if err != nil {
			return err
		}
		credentials["response_inspection_rules"] = rules
	}
	if raw, present := input["quota_recovery_policy"]; present {
		policy, err := normalizeQuotaRecoveryPolicy(raw)
		if err != nil {
			return err
		}
		credentials["quota_recovery_policy"] = policy
	}
	return nil
}

var credentialOverrideTokenPattern = regexp.MustCompile(`(?i)^[a-z0-9][a-z0-9._-]{0,63}$`)

// normalizeGPTAccountRequestOverrides mirrors
// normalizeGptAccountRequestOverrides: the Gemini generate-content guard, the
// token shape check and the gpt enum whitelist.
func normalizeGPTAccountRequestOverrides(input map[string]any, credentials Credentials, context EndpointModeDefaultContext) error {
	_, hasServiceTier := input["service_tier_override"]
	_, hasReasoningEffort := input["reasoning_effort_override"]
	if !hasServiceTier && !hasReasoningEffort {
		return nil
	}
	if context.ProviderCode == "gemini" {
		if modes, ok := credentials["supported_endpoint_modes"].([]string); ok {
			if !containsString(modes, "generate_content_json") && !containsString(modes, "generate_content_sse") {
				return nil
			}
		}
	}
	serviceTier, err := optionalCredentialToken(credentialField(input, "service_tier_override"), "服务等级覆盖")
	if err != nil {
		return err
	}
	reasoningEffort, err := optionalCredentialToken(credentialField(input, "reasoning_effort_override"), "思考级别覆盖")
	if err != nil {
		return err
	}
	if context.ProviderCode == gptVendorCode {
		if serviceTier != "" && serviceTier != "default" && serviceTier != "priority" && serviceTier != "flex" {
			return &ValidationError{Message: "服务等级覆盖无效"}
		}
		switch reasoningEffort {
		case "", "none", "minimal", "low", "medium", "high", "xhigh", "max":
		default:
			return &ValidationError{Message: "思考级别覆盖无效"}
		}
	}
	if serviceTier != "" {
		credentials["service_tier_override"] = serviceTier
	}
	if reasoningEffort != "" {
		credentials["reasoning_effort_override"] = reasoningEffort
	}
	return nil
}

func optionalCredentialToken(value optionalValue, label string) (string, error) {
	if !value.present || value.value == nil {
		return "", nil
	}
	text, ok := value.value.(string)
	if !ok || text == "" {
		if !ok {
			return "", &ValidationError{Message: label + "无效"}
		}
		return "", nil
	}
	if text != strings.TrimSpace(text) || !credentialOverrideTokenPattern.MatchString(text) {
		return "", &ValidationError{Message: label + "无效"}
	}
	return text, nil
}

func requiredCredentialTextInput(value any, label string, maxBytes int) (string, error) {
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return "", &ValidationError{Message: label + "不能为空"}
	}
	trimmed := strings.TrimSpace(text)
	if err := assertCredentialTextByteLength(trimmed, label, maxBytes); err != nil {
		return "", err
	}
	return trimmed, nil
}

func optionalCredentialText(value optionalValue, label string, maxBytes int) (string, error) {
	if !value.present {
		return "", nil
	}
	text, ok := value.value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return "", &ValidationError{Message: label + "不能为空"}
	}
	trimmed := strings.TrimSpace(text)
	if err := assertCredentialTextByteLength(trimmed, label, maxBytes); err != nil {
		return "", err
	}
	return trimmed, nil
}

func optionalCredentialDateTime(value optionalValue, label string) (string, error) {
	if !value.present {
		return "", nil
	}
	// Node optionalServerDateTimeIso: any present value that fails to
	// canonicalize (null, non-string, malformed) throws.
	text, _ := value.value.(string)
	if strings.TrimSpace(text) == "" {
		return "", &ValidationError{Message: label + "必须是有效时间字符串"}
	}
	canonical, valid := canonicalRFC3339(text)
	if !valid {
		return "", &ValidationError{Message: label + "必须是有效时间字符串"}
	}
	return canonical, nil
}

func copyOptionalCredentialText(input map[string]any, output Credentials, key, label string, maxBytes int) error {
	value, err := optionalCredentialText(credentialField(input, key), label, maxBytes)
	if err != nil {
		return err
	}
	if value != "" {
		output[key] = value
	}
	return nil
}

func copyOptionalCredentialNonNegativeInteger(input map[string]any, output Credentials, key, label string) error {
	raw, present := input[key]
	if !present || raw == nil || raw == "" {
		return nil
	}
	if number, ok := raw.(float64); ok && number == float64(int64(number)) && number >= 0 {
		output[key] = number
		return nil
	}
	if text, ok := raw.(string); ok && isAllDigits(strings.TrimSpace(text)) {
		output[key] = strings.TrimSpace(text)
		return nil
	}
	return &ValidationError{Message: label + "必须是非负整数"}
}

func assertCredentialTextByteLength(value, label string, maxBytes int) error {
	// len() is the UTF-8 byte length, matching Buffer.byteLength(value, 'utf8').
	if len(value) > maxBytes {
		return &ValidationError{Message: fmt.Sprintf("%s不能超过 %d 字节", label, maxBytes)}
	}
	return nil
}

func assertAccountCredentialsJSONSize(credentials Credentials) error {
	encoded, err := json.Marshal(map[string]any(credentials))
	if err != nil {
		return err
	}
	if len(encoded) > accountCredentialsJSONMaxBytes {
		return &ValidationError{Message: fmt.Sprintf("账户凭据整体大小不能超过 %d 字节", accountCredentialsJSONMaxBytes)}
	}
	return nil
}

func assertKnownInputKeys(input map[string]any, allowedKeys map[string]bool, label string) error {
	unknownKeys := []string{}
	for key := range input {
		if !allowedKeys[key] {
			unknownKeys = append(unknownKeys, key)
		}
	}
	if len(unknownKeys) == 0 {
		return nil
	}
	sortStrings(unknownKeys)
	return &ValidationError{Message: label + "包含不支持的字段：" + strings.Join(unknownKeys, ", ")}
}

// canonicalizeNormalizedCredentials JSON-round-trips the normalized record so
// every value carries the decoded-JSON shape (float64 numbers, []any lists,
// map[string]any objects) — the same shape a Node decryptJson produces for the
// deep-equal change detection.
func canonicalizeNormalizedCredentials(credentials Credentials) (Credentials, error) {
	encoded, err := json.Marshal(map[string]any(credentials))
	if err != nil {
		return nil, err
	}
	decoded := Credentials{}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

// credentialsDeepEqual mirrors Node isDeepStrictEqual over decoded-JSON
// records: canonicalize both sides to JSON text with sorted keys.
func credentialsDeepEqual(left, right Credentials) bool {
	if (left == nil) != (right == nil) {
		// Node compares {} and undefined as different when a credentials
		// record existed vs not; the sealed column never stores null, so the
		// nil side renders as an empty record.
		return len(left) == 0 && len(right) == 0
	}
	leftEncoded, err := json.Marshal(canonicalizeJSONValue(map[string]any(left)))
	if err != nil {
		return false
	}
	rightEncoded, err := json.Marshal(canonicalizeJSONValue(map[string]any(right)))
	if err != nil {
		return false
	}
	return string(leftEncoded) == string(rightEncoded)
}

// canonicalizeJSONValue JSON-round-trips arbitrary decoded values so both
// sides of a comparison carry identical shapes (float64, []any, map[string]any).
func canonicalizeJSONValue(value any) any {
	encoded, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return value
	}
	return decoded
}

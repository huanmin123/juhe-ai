package main

// G20 phase-3 account secret projection: the openAIAccountSecretFromRow port
// (openai-account-selector.repository.ts) plus its collaborators:
//
//   - account-api-key-rotation.ts: accountApiKeyEntries (HMAC-SHA256
//     fingerprints over runtimeConfig.secret), isAccountApiKeyPoolIsolationEnabled,
//   - runtimeOpenAIAccountCredentials (the runtime credential key projection),
//   - openai-endpoint-modes.ts / anthropic-endpoint-modes.ts /
//     gemini-endpoint-modes.ts / hybrid account-credentials.ts
//     (normalizeGatewayEndpointModesForRuntime + per-protocol defaults),
//   - provider-protocol.ts (defaultBaseUrlForProtocol),
//   - resource-authorization-helpers.ts (resourceAuthorizationQuotaLimited),
//   - group-scheduling.ts (normalizeGroupType + parseGroupSchedulingPolicyJson
//     guards).

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// chainSecretOptions mirrors OpenAIAccountSecretOptions (the hydration loads
// the caller prefetched for the batch).
type chainSecretOptions struct {
	proxyProfiles      map[string]chainProxyProfileResolution
	supportedModels    map[string][]string
	modelMappings      map[string][]gatewayruntimecache.AccountModelMapping
	apiKeyRuntimeState map[string][]gatewayruntimecache.AccountAPIKeyRuntimeSelectionState
}

// openAIAccountSecretFromRow mirrors openAIAccountSecretFromRow: nil without
// an error mirrors the Node undefined return (undecryptable credentials /
// missing api key / missing authorization binding), a returned error mirrors
// the Node throw paths.
func (s *chainAccountsSelector) openAIAccountSecretFromRow(ctx context.Context, row *chainCandidateRow, groupAccess *gatewayruntimecache.GroupUsageAccessMetadata, access *chainAccountAccess, options chainSecretOptions) (*gatewayruntimecache.OpenAIAccountSecret, error) {
	cooldownUntil, err := chainOptionalRFC3339(row.CooldownUntil, "AI 账户 cooldownUntil")
	if err != nil {
		return nil, err
	}
	streamFailureWindowStartedAt, err := chainOptionalRFC3339(row.StreamFailureWindowStartedAt, "AI 账户 streamFailureWindowStartedAt")
	if err != nil {
		return nil, err
	}
	accountExpiresAt, err := chainOptionalRFC3339(row.AccountExpiresAt, "AI 账户 accountExpiresAt")
	if err != nil {
		return nil, err
	}
	accountAuthorizationExpiresAt, err := chainOptionalRFC3339Raw(access.expiresAt, "账户授权 expiresAt")
	if err != nil {
		return nil, err
	}
	groupAuthorizationExpiresAt, err := chainOptionalRFC3339Raw(groupAccess.GroupAuthorizationExpiresAt, "分组授权 expiresAt")
	if err != nil {
		return nil, err
	}

	resourceType := row.resourceType()
	var credentials map[string]any
	if encrypted := row.resourceCredentialsEncrypted(); encrypted != "" {
		if err := accounts.DecryptJSON(s.secret, encrypted, &credentials); err != nil {
			// Node swallows the decrypt failure and drops the account.
			return nil, nil
		}
	}
	if credentials == nil {
		credentials = map[string]any{}
	}

	credentialExpiresAt, err := chainOptionalRFC3339Raw(anyStringPtr(credentials["expires_at"]), "AI 账户凭据 expires_at")
	if err != nil {
		return nil, err
	}
	entries := chainAccountAPIKeyEntries(s.secret, credentials)
	apiKey := chainAPIKeyEntriesFirstKey(entries)
	if apiKey == "" {
		return nil, nil
	}
	var apiKeys []string
	if resourceType == "api_key" {
		apiKeys = make([]string, 0, len(entries))
		for _, entry := range entries {
			apiKeys = append(apiKeys, entry.key)
		}
	}

	resourceAccountID := row.resourceAccountID()
	apiKeyPoolEnabled := chainAccountAPIKeyPoolIsolationEnabled(
		row.resourceProviderCode(), row.resourceProtocolCode(), row.resourceProtocolVersion(), resourceType, credentials)
	resourceProxyProfileID := row.resourceProxyProfileID()
	proxyProfile := chainResolveProxyURL(resourceProxyProfileID, options.proxyProfiles)

	isAccountAuthorized := access.accountAccessType == chainAccountAccessAuthorized
	isLocalAccountAuthorized := isAccountAuthorized && row.AccountAuthorizationID.Valid && row.AccountAuthorizationID.String != ""
	var bindingSystemAccountID *string
	if isLocalAccountAuthorized {
		trimmed := strings.TrimSpace(row.BindingSystemAccountID.String)
		if trimmed == "" {
			return nil, fmt.Errorf("授权账户绑定缺少系统账户上下文")
		}
		bindingSystemAccountID = &trimmed
	}

	runtimeStatus := row.Status
	dispatchPriority := row.Priority
	if row.LocalPriority.Valid {
		dispatchPriority = int(row.LocalPriority.Int64)
	}
	dispatchSuperPriorityEnabled := row.LocalSuperPriority.Valid && row.LocalSuperPriority.Int64 == 1
	dispatchFallbackEnabled := row.LocalFallback.Valid && row.LocalFallback.Int64 == 1
	accountOwnerSystemAccountID := row.SystemAccountID
	if isAccountAuthorized {
		if access.accountOwnerID != nil && *access.accountOwnerID != "" {
			accountOwnerSystemAccountID = *access.accountOwnerID
		} else if row.AuthorizationInstanceOwnerSystemAccountI.Valid && row.AuthorizationInstanceOwnerSystemAccountI.String != "" {
			accountOwnerSystemAccountID = row.AuthorizationInstanceOwnerSystemAccountI.String
		}
	}

	providerCode := row.resourceProviderCode()
	protocolCode := row.resourceProtocolCode()
	protocolVersion := row.resourceProtocolVersion()
	clientCompatibility := chainResourceClientCompatibility(row, providerCode, protocolCode, protocolVersion)
	providerProtocolProfileID := row.ProviderProtocolProfileID.String
	if row.ResourceProviderProtocolProfileID.Valid && row.ResourceProviderProtocolProfileID.String != "" {
		providerProtocolProfileID = row.ResourceProviderProtocolProfileID.String
	}
	supportedEndpointModes := chainNormalizeGatewayEndpointModesForRuntime(anyToAnyList(credentials["supported_endpoint_modes"]), providerCode, resourceType, clientCompatibility, providerProtocolProfileID, protocolCode, protocolVersion)

	secret := &gatewayruntimecache.OpenAIAccountSecret{
		ID:                               row.ID,
		ConfigRevision:                   chainConfigRevisionOf(row.ConfigRevision),
		DispatchRevision:                 nullInt64Ptr(row.DispatchRevision),
		ProviderCode:                     providerCode,
		ProviderProtocolProfileID:        providerProtocolProfileID,
		ProtocolCode:                     protocolCode,
		ProtocolVersion:                  protocolVersion,
		SystemAccountID:                  row.SystemAccountID,
		AccountOwnerSystemAccountID:      accountOwnerSystemAccountID,
		GroupOwnerSystemAccountID:        groupAccess.GroupOwnerSystemAccountID,
		AccountAccessType:                string(access.accountAccessType),
		GroupAccessType:                  groupAccess.GroupAccessType,
		AccountAuthorizationExpiresAt:    accountAuthorizationExpiresAt,
		AccountAuthorizationQuotaLimited: access.quotaLimited,
		AccountAuthorizationSourceType:   access.sourceType,
		AccountAuthorizationSourceTeamID: access.sourceTeamID,
		BindingSystemAccountID:           bindingSystemAccountID,
		GroupAuthorizationID:             groupAccess.GroupAuthorizationID,
		GroupAuthorizationExpiresAt:      groupAuthorizationExpiresAt,
		GroupAuthorizationQuotaLimited:   groupAccess.GroupAuthorizationQuotaLimited,
		GroupAuthorizationSourceType:     groupAccess.GroupAuthorizationSourceType,
		GroupAuthorizationSourceTeamID:   groupAccess.GroupAuthorizationSourceTeamID,
		Name:                             row.Name,
		Type:                             resourceType,
		Status:                           runtimeStatus,
		ConcurrencyLimit:                 row.resourceConcurrencyLimit(),
		Priority:                         dispatchPriority,
		SuperPriorityEnabled:             runtimeStatus == "active" && dispatchSuperPriorityEnabled,
		FallbackEnabled:                  runtimeStatus == "active" && dispatchFallbackEnabled,
		ClientCompatibility:              clientCompatibility,
		SupportedEndpointModes:           supportedEndpointModes,
		SupportedModels:                  []string{},
		ModelMappings:                    []gatewayruntimecache.AccountModelMapping{},
		HealthCheckEndpointMode:          row.HealthCheckEndpointMode.String,
		QualityScore:                     row.QualityScore,
		QualityState:                     row.QualityState,
		QualityEwmaFirstTokenMs:          row.QualityFirstTokenMs,
		BaseURL:                          chainBaseURLOf(credentials),
		APIKey:                           apiKey,
		APIKeys:                          apiKeys,
		RefreshToken:                     anyStringPtr(credentials["refresh_token"]),
		ClientID:                         anyStringPtr(credentials["client_id"]),
		ProxyProfileID:                   nilIfEmpty(resourceProxyProfileID),
		ProxyURL:                         proxyProfile.proxyURL,
		ProxyProfileUnavailable:          proxyProfile.unavailable,
		ProxyProfileErrorMessage:         proxyProfile.errorMessage,
		CooldownUntil:                    cooldownUntil,
		LastErrorMessage:                 nullStringPtr(row.LastErrorMessage),
		StreamFailureCount:               chainStreamFailureCount(row.StreamFailureCount),
		StreamFailureWindowStartedAt:     streamFailureWindowStartedAt,
		AccountExpiresAt:                 accountExpiresAt,
		ExpiresAt:                        credentialExpiresAt,
		Credentials:                      runtimeCredentialsOf(credentials),
	}
	secret.AccountAuthorizationID = access.accountAuthorizationID
	if isLocalAccountAuthorized {
		boundGroupID := row.GroupID.String
		secret.BoundGroupID = &boundGroupID
	}
	if trimmed := strings.TrimSpace(row.HealthCheckModel.String); trimmed != "" {
		secret.HealthCheckModel = trimmed
	}
	if models, ok := options.supportedModels[resourceAccountID]; ok {
		secret.SupportedModels = append([]string{}, models...)
	}
	if mappings, ok := options.modelMappings[resourceAccountID]; ok {
		secret.ModelMappings = append([]gatewayruntimecache.AccountModelMapping{}, mappings...)
	}
	if apiKeyPoolEnabled {
		if states, ok := options.apiKeyRuntimeState[resourceAccountID]; ok {
			cloned := make([]gatewayruntimecache.AccountAPIKeyRuntimeSelectionState, 0, len(states))
			for _, state := range states {
				cloned = append(cloned, state.Clone())
			}
			secret.APIKeyRuntimeStates = cloned
		}
	}
	if resourceAccountID != row.ID {
		secret.CredentialSourceAccountID = &resourceAccountID
	}
	_ = ctx
	return secret, nil
}

// ---------------------------------------------------------------------------
// api-key rotation pool (account-api-key-rotation.ts)
// ---------------------------------------------------------------------------

// chainAPIKeyEntry mirrors AccountApiKeyEntry.
type chainAPIKeyEntry struct {
	key         string
	fingerprint string
	index       int
	weight      int
}

func chainAPIKeyEntriesFirstKey(entries []chainAPIKeyEntry) string {
	if len(entries) == 0 {
		return ""
	}
	return entries[0].key
}

// chainAccountAPIKeyEntries mirrors accountApiKeyEntries: the plural
// api_keys rotation list degrades to the single api_key credential, with
// HMAC-SHA256 fingerprints over the runtime secret.
func chainAccountAPIKeyEntries(secret string, credentials map[string]any) []chainAPIKeyEntry {
	var rawKeys []any
	if raw, ok := credentials["api_keys"].([]any); ok && len(raw) > 0 {
		rawKeys = raw
	} else if _, present := credentials["api_key"]; present {
		rawKeys = []any{credentials["api_key"]}
	}
	weights := anyToFloatList(credentials["api_key_weights"])
	entries := []chainAPIKeyEntry{}
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
		entries = append(entries, chainAPIKeyEntry{
			key:         key,
			fingerprint: chainFingerprintAPIKey(secret, key),
			index:       index,
			weight:      chainNormalizeAPIKeyWeight(index, weights),
		})
	}
	return entries
}

// chainFingerprintAPIKey mirrors fingerprintAccountApiKey
// (createHmac('sha256', runtimeConfig.secret)). Node's createHmac computes
// normally with an empty key, so deployments without JUHE_AI_SECRET still get
// the HMAC digest as the weighted entry id — no empty-string shortcut.
func chainFingerprintAPIKey(secret, key string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(key))
	return hex.EncodeToString(mac.Sum(nil))
}

// chainAccountAPIKeyPoolIsolationEnabled mirrors
// isAccountApiKeyPoolIsolationEnabled.
func chainAccountAPIKeyPoolIsolationEnabled(providerCode, protocolCode, protocolVersion, accountType string, credentials map[string]any) bool {
	if accountType != "api_key" {
		return false
	}
	if !chainAccountAPIKeyPoolProviderSupported(providerCode, protocolCode, protocolVersion) {
		return false
	}
	return len(chainAccountAPIKeyEntries("", credentials)) > 1
}

// chainAccountAPIKeyPoolProviderSupported mirrors
// isAccountApiKeyPoolProviderSupported.
func chainAccountAPIKeyPoolProviderSupported(providerCode, protocolCode, protocolVersion string) bool {
	code := chainNormalizeProviderToken(providerCode)
	switch code {
	case "openai", "gpt", "xai", "deepseek", "glm", "gemini", "hybrid", "anthropic":
		return true
	}
	if chainNormalizeProviderToken(protocolCode) == "anthropic" && chainNormalizeProviderToken(protocolVersion) == "v1" {
		return true
	}
	return false
}

func chainNormalizeAPIKeyWeight(index int, weights []float64) int {
	if index < 0 || index >= len(weights) {
		return 1
	}
	value := weights[index]
	if value == float64(int(value)) && value >= 1 && value <= 100 {
		return int(value)
	}
	return 1
}

// chainRuntimeCredentialSource mirrors runtimeCredentialSource.
func chainRuntimeCredentialSource(accountType string, credentials map[string]any, apiKey string) string {
	if accountType == "oauth" || accountType == "google_oauth" {
		if access, ok := credentials["access_token"].(string); ok && access != "" {
			return access
		}
		if refresh, ok := credentials["refresh_token"].(string); ok {
			return refresh
		}
		return ""
	}
	return apiKey
}

// ---------------------------------------------------------------------------
// credential projection (runtimeOpenAIAccountCredentials)
// ---------------------------------------------------------------------------

// runtimeCredentialsOf mirrors runtimeOpenAIAccountCredentials: only the
// runtime-relevant credential keys ride on the dispatch secret.
func runtimeCredentialsOf(credentials map[string]any) map[string]any {
	out := map[string]any{}
	for _, key := range []string{
		"access_token", "refresh_token", "expires_at", "client_id", "client_secret",
		"quota_project_id", "oauth_type", "project_id", "tier_id", "token_type", "scope",
	} {
		chainCopyRuntimeCredentialText(credentials, out, key)
	}
	chainCopyRuntimeCredentialText(credentials, out, "account_id")
	chainCopyRuntimeCredentialText(credentials, out, "api_key_strategy")
	chainCopyRuntimeCredentialText(credentials, out, "service_tier_override")
	chainCopyRuntimeCredentialText(credentials, out, "reasoning_effort_override")
	chainCopyRuntimeCredentialValue(credentials, out, "supported_endpoint_modes")
	chainCopyRuntimeCredentialValue(credentials, out, "api_key_weights")
	chainCopyRuntimeCredentialValue(credentials, out, "error_handling_rules")
	chainCopyRuntimeCredentialValue(credentials, out, "response_inspection_rules")
	chainCopyRuntimeCredentialValue(credentials, out, "quota_recovery_policy")
	return out
}

func chainCopyRuntimeCredentialText(input, output map[string]any, key string) {
	if value, ok := input[key].(string); ok && strings.TrimSpace(value) != "" {
		output[key] = strings.TrimSpace(value)
	}
}

func chainCopyRuntimeCredentialValue(input, output map[string]any, key string) {
	if value, ok := input[key]; ok {
		output[key] = value
	}
}

func chainBaseURLOf(credentials map[string]any) string {
	if base, ok := credentials["base_url"].(string); ok && base != "" {
		return base
	}
	return ""
}

// ---------------------------------------------------------------------------
// endpoint modes (normalizeGatewayEndpointModesForRuntime)
// ---------------------------------------------------------------------------

var (
	chainOpenAIEndpointModeValues = []string{"chat_json", "chat_sse", "responses_json", "responses_sse"}
	chainOpenAIChatEndpointModes  = []string{"chat_json", "chat_sse"}
	chainOpenAIResponsesModes     = []string{"responses_json", "responses_sse"}
	chainAnthropicEndpointModes   = []string{"messages_json", "messages_sse", "message_token_counting"}
	chainGeminiDefaultModes       = []string{"generate_content_json", "generate_content_sse", "count_tokens", "interactions_json", "interactions_sse"}
	chainHybridEndpointModes      = []string{
		"chat_json", "chat_sse", "responses_json", "responses_sse",
		"messages_json", "messages_sse", "message_token_counting",
		"generate_content_json", "generate_content_sse", "count_tokens", "embed_content", "interactions_json", "interactions_sse",
	}
)

// chainNormalizeGatewayEndpointModesForRuntime mirrors
// normalizeGatewayEndpointModesForRuntime (openai / anthropic / gemini /
// hybrid profile branches).
func chainNormalizeGatewayEndpointModesForRuntime(value []any, providerCode, accountType, clientCompatibility, providerProtocolProfileID, protocolCode, protocolVersion string) []string {
	code := chainNormalizeProviderToken(providerCode)
	if code == "hybrid" {
		return chainFilterEndpointModes(value, chainHybridEndpointModes)
	}
	if chainIsAnthropicProtocolProfile(protocolCode, protocolVersion) {
		defaults := chainAnthropicEndpointModes
		if providerProtocolProfileID == "profile_deepseek_anthropic_v1" || providerProtocolProfileID == "profile_glm_coding_anthropic_v1" {
			defaults = []string{"messages_json", "messages_sse"}
		}
		return chainFilterEndpointModes(value, defaults)
	}
	if chainIsGeminiProtocolProfile(protocolCode, protocolVersion) {
		return chainFilterEndpointModes(value, chainGeminiDefaultModes)
	}
	return chainNormalizeOpenAIEndpointModesForRuntime(value, providerCode, accountType, clientCompatibility)
}

// chainNormalizeOpenAIEndpointModesForRuntime mirrors
// normalizeOpenAIEndpointModesForRuntime + defaultOpenAIEndpointModes.
func chainNormalizeOpenAIEndpointModesForRuntime(value []any, providerCode, accountType, clientCompatibility string) []string {
	filtered := chainFilterEndpointModes(value, chainOpenAIEndpointModeValues)
	if len(filtered) > 0 {
		return filtered
	}
	if accountType == "oauth" {
		return append([]string{}, chainOpenAIResponsesModes...)
	}
	code := chainNormalizeProviderToken(providerCode)
	switch code {
	case "gpt", "deepseek":
		return append([]string{}, chainOpenAIEndpointModeValues...)
	case "openai", "glm", "gemini", "hybrid":
		return append([]string{}, chainOpenAIChatEndpointModes...)
	}
	if clientCompatibility == "codex_responses" {
		return append([]string{}, chainOpenAIEndpointModeValues...)
	}
	return append([]string{}, chainOpenAIEndpointModeValues...)
}

func chainFilterEndpointModes(value []any, allowed []string) []string {
	if value == nil {
		return nil
	}
	allowedSet := map[string]bool{}
	for _, mode := range allowed {
		allowedSet[mode] = true
	}
	out := []string{}
	seen := map[string]bool{}
	for _, item := range value {
		text, ok := item.(string)
		if !ok || !allowedSet[text] || seen[text] {
			continue
		}
		seen[text] = true
		out = append(out, text)
	}
	return out
}

func chainIsAnthropicProtocolProfile(protocolCode, protocolVersion string) bool {
	return chainNormalizeProviderToken(protocolCode) == "anthropic" && chainNormalizeProviderToken(protocolVersion) == "v1"
}

func chainIsGeminiProtocolProfile(protocolCode, protocolVersion string) bool {
	return chainNormalizeProviderToken(protocolCode) == "gemini" && chainNormalizeProviderToken(protocolVersion) == "v1beta"
}

// chainResourceClientCompatibility mirrors openAIAccountResourceClientCompatibility:
// the resource row wins, the normalized projection keeps 'openai_standard'
// as the compatibility default.
func chainResourceClientCompatibility(row *chainCandidateRow, providerCode, protocolCode, protocolVersion string) string {
	value := ""
	if row.ResourceClientCompatibility.Valid {
		value = row.ResourceClientCompatibility.String
	} else if row.ClientCompatibility.Valid {
		value = row.ClientCompatibility.String
	}
	trimmed := strings.TrimSpace(value)
	if trimmed != "" {
		return trimmed
	}
	return "openai_standard"
}

// chainResolveProxyURL mirrors resolveOpenAIAccountProxyUrl.
func chainResolveProxyURL(proxyProfileID string, profiles map[string]chainProxyProfileResolution) chainProxyProfileResolution {
	if proxyProfileID != "" && profiles != nil {
		if resolution, ok := profiles[proxyProfileID]; ok {
			return resolution
		}
		message := "代理不存在或已停用，请选择一个已启用的代理"
		return chainProxyUnavailable(message)
	}
	// Node resolveProxyUrlForProfile(nil) resolves to undefined without a
	// lookup; a missing profile id surfaces as the unavailable contract only
	// when an id exists but was not prefetched.
	if proxyProfileID == "" {
		return chainProxyProfileResolution{}
	}
	message := "代理不存在或已停用，请选择一个已启用的代理"
	return chainProxyUnavailable(message)
}

// ---------------------------------------------------------------------------
// authorization quota / group scheduling helpers
// ---------------------------------------------------------------------------

// chainResourceAuthorizationQuotaLimited mirrors
// resourceAuthorizationQuotaLimited (hasEnabledRequestQuotaLimit over
// limits_json).
func chainResourceAuthorizationQuotaLimited(limitsJSON *string) *bool {
	if limitsJSON == nil || strings.TrimSpace(*limitsJSON) == "" {
		return boolPtr(false)
	}
	var parsed struct {
		Hourly *struct {
			Enabled bool `json:"enabled"`
		} `json:"hourly"`
		Daily *struct {
			Enabled bool `json:"enabled"`
		} `json:"daily"`
		Weekly *struct {
			Enabled bool `json:"enabled"`
		} `json:"weekly"`
		Monthly *struct {
			Enabled bool `json:"enabled"`
		} `json:"monthly"`
		Total *struct {
			Enabled bool `json:"enabled"`
		} `json:"total"`
	}
	if err := json.Unmarshal([]byte(*limitsJSON), &parsed); err != nil {
		return boolPtr(false)
	}
	limited := (parsed.Hourly != nil && parsed.Hourly.Enabled) ||
		(parsed.Daily != nil && parsed.Daily.Enabled) ||
		(parsed.Weekly != nil && parsed.Weekly.Enabled) ||
		(parsed.Monthly != nil && parsed.Monthly.Enabled) ||
		(parsed.Total != nil && parsed.Total.Enabled)
	return boolPtr(limited)
}

// chainNormalizeGroupType mirrors normalizeGroupType.
func chainNormalizeGroupType(value sql.NullString) (*string, error) {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		personal := "personal"
		return &personal, nil
	}
	if value.String == "personal" || value.String == "high_concurrency" {
		return &value.String, nil
	}
	return nil, fmt.Errorf("分组类型无效")
}

// chainParseGroupSchedulingPolicy mirrors parseGroupSchedulingPolicyJson: the
// policy only exists for high_concurrency groups; a missing payload throws
// like the Node implementation.
func chainParseGroupSchedulingPolicy(value sql.NullString, groupType *string) (*gatewayruntimecache.GroupSchedulingPolicy, error) {
	if groupType == nil || *groupType != "high_concurrency" {
		return nil, nil
	}
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil, fmt.Errorf("高并发分组调度策略缺失")
	}
	policy := gatewayruntimecache.GroupSchedulingPolicy{}
	if err := json.Unmarshal([]byte(value.String), &policy); err != nil {
		return nil, err
	}
	return &policy, nil
}

// ---------------------------------------------------------------------------
// scalar helpers
// ---------------------------------------------------------------------------

func chainNormalizeProviderToken(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func chainOptionalRFC3339(value sql.NullString, label string) (*string, error) {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil, nil
	}
	if _, err := chainRFC3339Millis(value.String); err != nil {
		return nil, fmt.Errorf("%s必须是带 Z 或数值 offset 的 RFC3339 时间", label)
	}
	return &value.String, nil
}

func chainOptionalRFC3339Raw(value *string, label string) (*string, error) {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil, nil
	}
	if _, err := chainRFC3339Millis(*value); err != nil {
		return nil, fmt.Errorf("%s必须是带 Z 或数值 offset 的 RFC3339 时间", label)
	}
	return value, nil
}

func anyStringPtr(value any) *string {
	if text, ok := value.(string); ok {
		return &text
	}
	return nil
}

func anyToAnyList(value any) []any {
	if list, ok := value.([]any); ok {
		return list
	}
	return nil
}

func anyToFloatList(value any) []float64 {
	list, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]float64, 0, len(list))
	for _, item := range list {
		if number, ok := item.(float64); ok {
			out = append(out, number)
		}
	}
	return out
}

func chainStreamFailureCount(value sql.NullInt64) int {
	if !value.Valid || value.Int64 < 0 {
		return 0
	}
	return int(value.Int64)
}

// nilIfEmpty renders empty strings as the Node undefined optional.
func nilIfEmpty(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}

// chainConfigRevisionOf mirrors Number(row.config_revision ?? 1): the config
// revision defaults to 1.
func chainConfigRevisionOf(value sql.NullInt64) *int64 {
	if value.Valid {
		out := value.Int64
		return &out
	}
	out := int64(1)
	return &out
}

// chainURIEncode mirrors encodeURIComponent (space renders as %20, unlike
// url.QueryEscape's '+').
func chainURIEncode(value string) string {
	return strings.ReplaceAll(url.QueryEscape(value), "+", "%20")
}

// chainTimeLayout matches the Node nowIso() millisecond ISO output.
const chainTimeLayout = "2006-01-02T15:04:05.000Z07:00"

// chainRFC3339Millis mirrors requiredRfc3339Timestamp
// (rfc3339InstantMilliseconds): an RFC3339 instant with a mandatory offset
// rendered as unix milliseconds.
func chainRFC3339Millis(value string) (int64, error) {
	trimmed := strings.TrimSpace(value)
	parsed, err := time.Parse(time.RFC3339Nano, trimmed)
	if err != nil {
		return 0, fmt.Errorf("%s必须是带 Z 或数值 offset 的 RFC3339 时间", value)
	}
	return parsed.UnixMilli(), nil
}

// nullInt64Ptr converts a nullable SQL integer into the optional int64 the
// secret shape carries.
func nullInt64Ptr(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	out := value.Int64
	return &out
}

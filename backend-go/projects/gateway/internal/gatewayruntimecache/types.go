// Package gatewayruntimecache is the Go port of the Node gateway runtime
// read-only cache (work package G10):
//
//   - backend/src/modules/gateway/runtime/runtime-cache.service.ts (core),
//   - backend/src/modules/gateway/runtime/runtime-snapshot.service.ts,
//   - backend/src/modules/gateway/runtime/internal-gateway-registry.ts.
//
// The cache serves the gateway hot path with account / API key / group /
// route-strategy / settings read models. Cache keys, invalidation timing,
// snapshot version semantics, negative caching and TTL behaviour mirror the
// Node service exactly; the exported entity snapshot structs mirror the Node
// return shapes field by field.
//
// Downstream packages (gatewaypreauth/gatewayrouting/gatewayquota/
// gatewayhybrid) consume the exported read-only methods; the underlying
// read-model fetches are behind the ReadModels interface so every cache
// behaviour is testable against mocks and future slices can supply their own
// selectors. SQLReadModels provides the default dual-mode (SQLite +
// PostgreSQL) read-only queries for the reads that had no Go home yet.
package gatewayruntimecache

import "encoding/json"

// Route strategy modes mirror domain/route-strategy.ts RouteStrategyMode.
const (
	RouteStrategyModeNormal     = "normal"
	RouteStrategyModeHybridSmart = "hybrid_smart"
	RouteStrategyModeWeighted   = "weighted"
	RouteStrategyModeFailover   = "failover"
	RouteStrategyModeRoundRobin = "round_robin"
)

// IsDynamicRouteStrategyMode mirrors isDynamicRouteStrategyMode: only
// round_robin and weighted re-select a group per read.
func IsDynamicRouteStrategyMode(mode string) bool {
	switch mode {
	case RouteStrategyModeRoundRobin, RouteStrategyModeWeighted:
		return true
	default:
		return false
	}
}

// NormalizeRouteStrategyMode mirrors normalizeRouteStrategyMode: unknown or
// empty values fall back to "normal".
func NormalizeRouteStrategyMode(value string) string {
	switch value {
	case RouteStrategyModeNormal, RouteStrategyModeHybridSmart, RouteStrategyModeWeighted,
		RouteStrategyModeFailover, RouteStrategyModeRoundRobin:
		return value
	default:
		return RouteStrategyModeNormal
	}
}

// GatewaySettings mirrors GatewaySettings in
// modules/gateway/policy/account-error-policy.service.ts. streamCircuitBreakerEnabled
// is forced to true by the Node projection (cloneGatewaySettings keeps it true).
type GatewaySettings struct {
	GatewayTextRawBodyLimitMegabytes          int64  `json:"gatewayTextRawBodyLimitMegabytes"`
	AccountCircuitConfirmationFailuresRequired int64  `json:"accountCircuitConfirmationFailuresRequired"`
	GatewayUserRequestLimitPerMinute          *int64 `json:"gatewayUserRequestLimitPerMinute,omitempty"`
	GatewayUserRequestLimitPerDay             *int64 `json:"gatewayUserRequestLimitPerDay,omitempty"`
	GatewayUserRequestLimitPerWeek            *int64 `json:"gatewayUserRequestLimitPerWeek,omitempty"`
	GatewayUserRequestLimitPerMonth           *int64 `json:"gatewayUserRequestLimitPerMonth,omitempty"`
	UsageStatsTimezone                        string `json:"usageStatsTimezone"`
	DefaultTemporaryUnschedulableMinutes      int64  `json:"defaultTemporaryUnschedulableMinutes"`
	TemporaryUnschedulableRetryIntervalSeconds int64  `json:"temporaryUnschedulableRetryIntervalSeconds"`
	TemporaryUnschedulableRetryAttempts       int64  `json:"temporaryUnschedulableRetryAttempts"`
	StreamCircuitBreakerEnabled               bool   `json:"streamCircuitBreakerEnabled"`
	TextFirstResponseTimeoutSeconds           int64  `json:"textFirstResponseTimeoutSeconds"`
	TextStreamIdleTimeoutSeconds              int64  `json:"textStreamIdleTimeoutSeconds"`
	TextUncommittedAttemptMaxLifetimeSeconds  int64  `json:"textUncommittedAttemptMaxLifetimeSeconds"`
	ImageFirstResponseTimeoutSeconds          int64  `json:"imageFirstResponseTimeoutSeconds"`
	ImageStreamIdleTimeoutSeconds             int64  `json:"imageStreamIdleTimeoutSeconds"`
	ImageUncommittedAttemptMaxLifetimeSeconds int64  `json:"imageUncommittedAttemptMaxLifetimeSeconds"`
	ImageRequestWallTimeoutSeconds            int64  `json:"imageRequestWallTimeoutSeconds"`
	NoAvailableAccountWaitTimeoutSeconds      int64  `json:"noAvailableAccountWaitTimeoutSeconds"`
	StreamFailureThresholdCount               int64  `json:"streamFailureThresholdCount"`
	StreamFailureThresholdWindowMinutes       int64  `json:"streamFailureThresholdWindowMinutes"`
}

// CloneGatewaySettings mirrors cloneGatewaySettings: the shallow copy keeps
// streamCircuitBreakerEnabled pinned to true.
func CloneGatewaySettings(settings GatewaySettings) GatewaySettings {
	settings.StreamCircuitBreakerEnabled = true
	return settings
}

// UserRequestLimits mirrors UserRequestLimits (system_accounts.request_limits_json).
type UserRequestLimits struct {
	PerMinute *int64  `json:"perMinute,omitempty"`
	PerDay    *int64  `json:"perDay,omitempty"`
	PerWeek   *int64  `json:"perWeek,omitempty"`
	PerMonth  *int64  `json:"perMonth,omitempty"`
	ExpiresOn *string `json:"expiresOn,omitempty"`
}

// Clone mirrors the JSON round-trip clone semantics.
func (l UserRequestLimits) Clone() UserRequestLimits {
	return UserRequestLimits{
		PerMinute: cloneInt64(l.PerMinute),
		PerDay:    cloneInt64(l.PerDay),
		PerWeek:   cloneInt64(l.PerWeek),
		PerMonth:  cloneInt64(l.PerMonth),
		ExpiresOn: cloneString(l.ExpiresOn),
	}
}

// RouteStrategyNormalRoutingConfig mirrors the stored normal routing config
// object. The runtime cache treats it as an opaque snapshot with one rule from
// Node cloneGatewayApiKeyRow: only schedulingPreference === 'speed_first'
// keeps the full stored object, every other preference collapses to the bare
// 'cost_first' shape. Raw carries every stored field verbatim so downstream
// consumers see the exact row payload; typed decoding lives in G08.
type RouteStrategyNormalRoutingConfig struct {
	SchedulingPreference string          `json:"schedulingPreference,omitempty"`
	Raw                  json.RawMessage `json:"-"`
}

// Clone mirrors cloneGatewayApiKeyRow's normal_routing_config branch.
func (c RouteStrategyNormalRoutingConfig) Clone() RouteStrategyNormalRoutingConfig {
	if c.SchedulingPreference != "speed_first" {
		return RouteStrategyNormalRoutingConfig{SchedulingPreference: "cost_first"}
	}
	return RouteStrategyNormalRoutingConfig{
		SchedulingPreference: c.SchedulingPreference,
		Raw:                  append(json.RawMessage(nil), c.Raw...),
	}
}

// ApiKeyHybridRoutingConfig mirrors the stored hybrid routing config object as
// an opaque snapshot: the Node cache only ever deep-clones it
// ({...config, levelRoutes: config.levelRoutes.map(r => ({...r}))}), which is
// value-identical to a byte copy of the stored JSON.
type ApiKeyHybridRoutingConfig struct {
	Raw json.RawMessage `json:"-"`
}

// Clone mirrors the hybrid deep clone.
func (c ApiKeyHybridRoutingConfig) Clone() ApiKeyHybridRoutingConfig {
	return ApiKeyHybridRoutingConfig{Raw: append(json.RawMessage(nil), c.Raw...)}
}

// GatewayAPIKeyGroupBindingRow mirrors GatewayApiKeyGroupBindingRow
// (storage/gateway-api-key.repository.ts).
type GatewayAPIKeyGroupBindingRow struct {
	ID               string `json:"id"`
	APIKeyID         string `json:"api_key_id"`
	SystemAccountID  string `json:"system_account_id"`
	GroupID          string `json:"group_id"`
	Priority         int    `json:"priority"`
	Weight           int    `json:"weight"`
	Status           string `json:"status"`
	ProviderCode     string `json:"provider_code"`
	GroupEnabled     int    `json:"group_enabled"`
}

// Clone mirrors the per-binding spread clone.
func (b GatewayAPIKeyGroupBindingRow) Clone() GatewayAPIKeyGroupBindingRow { return b }

// GatewayAPIKeyRow mirrors GatewayApiKeyRow: the api_keys row joined with the
// route strategy and the owner's system account, plus the normalized runtime
// route fields and the active group bindings.
type GatewayAPIKeyRow struct {
	ID                                    string                            `json:"id"`
	SystemAccountID                       string                            `json:"system_account_id"`
	RouteStrategyID                       string                            `json:"route_strategy_id"`
	RouteStrategyMode                     string                            `json:"route_strategy_mode"`
	RouteStrategyConfigJSON               *string                           `json:"route_strategy_config_json"`
	SelectedGroupID                       string                            `json:"selected_group_id"`
	Status                                string                            `json:"status"`
	ExpiresAt                             *string                           `json:"expires_at"`
	QuotaLimitsJSON                       *string                           `json:"quota_limits_json"`
	NormalRoutingConfig                   *RouteStrategyNormalRoutingConfig `json:"normal_routing_config,omitempty"`
	HybridRoutingConfig                   *ApiKeyHybridRoutingConfig        `json:"hybrid_routing_config,omitempty"`
	SystemAccountImageGenerationEnabled   int                               `json:"system_account_image_generation_enabled"`
	SystemAccountRequestLimitsJSON        *string                           `json:"system_account_request_limits_json,omitempty"`
	SystemAccountRequestLimits            *UserRequestLimits                `json:"system_account_request_limits,omitempty"`
	GroupBindings                         []GatewayAPIKeyGroupBindingRow    `json:"group_bindings,omitempty"`
}

// CloneGatewayAPIKeyRow mirrors cloneGatewayApiKeyRow.
func CloneGatewayAPIKeyRow(row GatewayAPIKeyRow) GatewayAPIKeyRow {
	out := row
	out.RouteStrategyConfigJSON = cloneString(row.RouteStrategyConfigJSON)
	out.ExpiresAt = cloneString(row.ExpiresAt)
	out.QuotaLimitsJSON = cloneString(row.QuotaLimitsJSON)
	out.SystemAccountRequestLimitsJSON = cloneString(row.SystemAccountRequestLimitsJSON)
	if row.NormalRoutingConfig != nil {
		cloned := row.NormalRoutingConfig.Clone()
		out.NormalRoutingConfig = &cloned
	}
	if row.HybridRoutingConfig != nil {
		cloned := row.HybridRoutingConfig.Clone()
		out.HybridRoutingConfig = &cloned
	}
	if row.SystemAccountRequestLimits != nil {
		cloned := row.SystemAccountRequestLimits.Clone()
		out.SystemAccountRequestLimits = &cloned
	}
	if row.GroupBindings != nil {
		out.GroupBindings = append([]GatewayAPIKeyGroupBindingRow(nil), row.GroupBindings...)
	}
	return out
}

// Account access / type enums mirror domain/types.ts unions; they are kept as
// plain strings so repository rows scan directly.
const (
	AccountAccessTypeOwner            = "owner"
	AccountAccessTypeAccountAuthorized = "account_authorized"
	AccountAccessTypeGroupAuthorized   = "group_authorized"

	GroupAccessTypeOwner      = "owner"
	GroupAccessTypeAuthorized = "authorized"

	AccountStatusActive              = "active"
	AccountStatusTemporaryUnavailable = "temporary_unavailable"
	AccountStatusRateLimited         = "rate_limited"
)

// AccountModelMapping mirrors AccountModelMapping.
type AccountModelMapping struct {
	SourceModel            string  `json:"sourceModel"`
	SourceEndpointFamily   string  `json:"sourceEndpointFamily"`
	UpstreamModel          string  `json:"upstreamModel"`
	UpstreamEndpointFamily string  `json:"upstreamEndpointFamily"`
	Enabled                bool    `json:"enabled"`
	RuntimeSource          *string `json:"runtimeSource,omitempty"`
	RuntimeRouteRuleID     *string `json:"runtimeRouteRuleId,omitempty"`
}

// Clone mirrors the per-mapping spread clone in cloneStaticOpenAIAccountSecret.
func (m AccountModelMapping) Clone() AccountModelMapping {
	out := m
	out.RuntimeSource = cloneString(m.RuntimeSource)
	out.RuntimeRouteRuleID = cloneString(m.RuntimeRouteRuleID)
	return out
}

// AccountAPIKeyRuntimeSelectionState mirrors AccountApiKeyRuntimeSelectionState
// (storage/account-api-key-rotation.ts) runtime subset carried on the secret.
type AccountAPIKeyRuntimeSelectionState struct {
	APIKeyID          string  `json:"apiKeyId"`
	Fingerprint       string  `json:"fingerprint"`
	Disabled          bool    `json:"disabled"`
	CooldownUntil     *string `json:"cooldownUntil,omitempty"`
	FailureCount      int     `json:"failureCount"`
	LastErrorCode     *string `json:"lastErrorCode,omitempty"`
	RecoveryStartedAt *string `json:"recoveryStartedAt,omitempty"`
	Generation        *string `json:"generation,omitempty"`
}

// Clone mirrors the per-state spread clone.
func (s AccountAPIKeyRuntimeSelectionState) Clone() AccountAPIKeyRuntimeSelectionState {
	out := s
	out.CooldownUntil = cloneString(s.CooldownUntil)
	out.LastErrorCode = cloneString(s.LastErrorCode)
	out.RecoveryStartedAt = cloneString(s.RecoveryStartedAt)
	out.Generation = cloneString(s.Generation)
	return out
}

// OpenAIAccountSecret mirrors OpenAIAccountSecret
// (storage/openai-account-selector.types.ts) field by field. It carries
// decrypted upstream credentials and therefore never leaves the process:
// exactly like the Node service, it is only cached process-locally.
type OpenAIAccountSecret struct {
	ID                                 string                                  `json:"id"`
	ConfigRevision                     *int64                                  `json:"configRevision,omitempty"`
	DispatchRevision                   *int64                                  `json:"dispatchRevision,omitempty"`
	ProviderCode                       string                                  `json:"providerCode"`
	ProviderProtocolProfileID          string                                  `json:"providerProtocolProfileId"`
	ProtocolCode                       string                                  `json:"protocolCode"`
	ProtocolVersion                    string                                  `json:"protocolVersion"`
	SystemAccountID                    string                                  `json:"systemAccountId"`
	AccountOwnerSystemAccountID        string                                  `json:"accountOwnerSystemAccountId"`
	GroupOwnerSystemAccountID          string                                  `json:"groupOwnerSystemAccountId"`
	AccountAccessType                  string                                  `json:"accountAccessType"`
	GroupAccessType                    string                                  `json:"groupAccessType"`
	AccountAuthorizationID             *string                                 `json:"accountAuthorizationId,omitempty"`
	AccountAuthorizationExpiresAt      *string                                 `json:"accountAuthorizationExpiresAt,omitempty"`
	AccountAuthorizationQuotaLimited   *bool                                   `json:"accountAuthorizationQuotaLimited,omitempty"`
	AccountAuthorizationSourceType     *string                                 `json:"accountAuthorizationSourceType,omitempty"`
	AccountAuthorizationSourceTeamID   *string                                 `json:"accountAuthorizationSourceTeamId,omitempty"`
	BindingSystemAccountID             *string                                 `json:"bindingSystemAccountId,omitempty"`
	BoundGroupID                       *string                                 `json:"boundGroupId,omitempty"`
	GroupAuthorizationID               *string                                 `json:"groupAuthorizationId,omitempty"`
	GroupAuthorizationExpiresAt        *string                                 `json:"groupAuthorizationExpiresAt,omitempty"`
	GroupAuthorizationQuotaLimited     *bool                                   `json:"groupAuthorizationQuotaLimited,omitempty"`
	GroupAuthorizationSourceType       *string                                 `json:"groupAuthorizationSourceType,omitempty"`
	GroupAuthorizationSourceTeamID     *string                                 `json:"groupAuthorizationSourceTeamId,omitempty"`
	Name                               string                                  `json:"name"`
	Type                               string                                  `json:"type"`
	Status                             string                                  `json:"status"`
	ConcurrencyLimit                   int                                     `json:"concurrencyLimit"`
	Priority                           int                                     `json:"priority"`
	SuperPriorityEnabled               bool                                    `json:"superPriorityEnabled"`
	FallbackEnabled                    bool                                    `json:"fallbackEnabled"`
	ClientCompatibility                string                                  `json:"clientCompatibility"`
	SupportedEndpointModes             []string                                `json:"supportedEndpointModes,omitempty"`
	SupportedModels                    []string                                `json:"supportedModels,omitempty"`
	ModelMappings                      []AccountModelMapping                   `json:"modelMappings,omitempty"`
	HealthCheckModel                   string                                  `json:"healthCheckModel"`
	HealthCheckEndpointMode            string                                  `json:"healthCheckEndpointMode"`
	QualityScore                       *float64                                `json:"qualityScore,omitempty"`
	QualityState                       *string                                 `json:"qualityState,omitempty"`
	QualityEwmaFirstTokenMs            *float64                                `json:"qualityEwmaFirstTokenMs,omitempty"`
	CurrentConcurrency                 *int                                    `json:"currentConcurrency,omitempty"`
	BaseURL                            string                                  `json:"baseUrl"`
	APIKey                             string                                  `json:"apiKey"`
	APIKeys                            []string                                `json:"apiKeys,omitempty"`
	APIKeyRuntimeStates                []AccountAPIKeyRuntimeSelectionState    `json:"apiKeyRuntimeStates,omitempty"`
	SelectedAPIKeyFingerprint          *string                                 `json:"selectedApiKeyFingerprint,omitempty"`
	SelectedAPIKeyIndex                *int                                    `json:"selectedApiKeyIndex,omitempty"`
	SelectedAPIKeyTransientGeneration  *string                                 `json:"selectedApiKeyTransientGeneration,omitempty"`
	SelectedAPIKeyRecoveryStartedAt    *string                                 `json:"selectedApiKeyRecoveryStartedAt,omitempty"`
	APIKeyRuntimeStateDisabled         bool                                    `json:"apiKeyRuntimeStateDisabled,omitempty"`
	RefreshToken                       *string                                 `json:"refreshToken,omitempty"`
	ClientID                           *string                                 `json:"clientId,omitempty"`
	CredentialSourceAccountID          *string                                 `json:"credentialSourceAccountId,omitempty"`
	ProxyProfileID                     *string                                 `json:"proxyProfileId,omitempty"`
	ProxyURL                           *string                                 `json:"proxyUrl,omitempty"`
	ProxyProfileUnavailable            *bool                                   `json:"proxyProfileUnavailable,omitempty"`
	ProxyProfileErrorMessage           *string                                 `json:"proxyProfileErrorMessage,omitempty"`
	CooldownUntil                      *string                                 `json:"cooldownUntil,omitempty"`
	LastErrorMessage                   *string                                 `json:"lastErrorMessage,omitempty"`
	StreamFailureCount                 int                                     `json:"streamFailureCount"`
	StreamFailureWindowStartedAt       *string                                 `json:"streamFailureWindowStartedAt,omitempty"`
	AccountExpiresAt                   *string                                 `json:"accountExpiresAt,omitempty"`
	ExpiresAt                          *string                                 `json:"expiresAt,omitempty"`
	Credentials                        map[string]any                          `json:"credentials"`
}

// CloneStaticOpenAIAccountSecret mirrors cloneStaticOpenAIAccountSecret: the
// per-request volatile fields (currentConcurrency, selected api key placement)
// are stripped so the cached snapshot stays static.
func CloneStaticOpenAIAccountSecret(account OpenAIAccountSecret) OpenAIAccountSecret {
	out := account
	out.CurrentConcurrency = nil
	out.SelectedAPIKeyFingerprint = nil
	out.SelectedAPIKeyIndex = nil
	out.SelectedAPIKeyTransientGeneration = nil
	out.SelectedAPIKeyRecoveryStartedAt = nil
	if account.SupportedEndpointModes != nil {
		out.SupportedEndpointModes = append([]string(nil), account.SupportedEndpointModes...)
	}
	out.SupportedModels = append([]string(nil), account.SupportedModels...)
	if account.APIKeys != nil {
		out.APIKeys = append([]string(nil), account.APIKeys...)
	}
	if account.APIKeyRuntimeStates != nil {
		out.APIKeyRuntimeStates = make([]AccountAPIKeyRuntimeSelectionState, len(account.APIKeyRuntimeStates))
		for i, state := range account.APIKeyRuntimeStates {
			out.APIKeyRuntimeStates[i] = state.Clone()
		}
	}
	out.ModelMappings = make([]AccountModelMapping, len(account.ModelMappings))
	for i, mapping := range account.ModelMappings {
		out.ModelMappings[i] = mapping.Clone()
	}
	out.Credentials = cloneCredentials(account.Credentials)
	return out
}

// Node drops nested credential identity via structuredClone-safe spreads;
// credentials are shallow-copied per key exactly like { ...account.credentials }.
func cloneCredentials(input map[string]any) map[string]any {
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

// GroupSchedulingPolicy mirrors the scheduling_policy_json payload shape. The
// runtime cache treats it as opaque — Node only ever clones it with
// {...policy} — so the Go cache carries it as the decoded JSON object; typed
// decoding belongs to the groups / high-concurrency slices.
type GroupSchedulingPolicy = map[string]any

// GroupUsageAccessMetadata mirrors GroupUsageAccessMetadata
// (storage/openai-account-selector.types.ts).
type GroupUsageAccessMetadata struct {
	GroupOwnerSystemAccountID        string                 `json:"groupOwnerSystemAccountId"`
	ProviderCode                     string                 `json:"providerCode"`
	GroupAccessType                  string                 `json:"groupAccessType"`
	GroupType                        *string                `json:"groupType,omitempty"`
	SchedulingPolicy                 *GroupSchedulingPolicy `json:"schedulingPolicy,omitempty"`
	GroupAuthorizationID             *string                `json:"groupAuthorizationId,omitempty"`
	GroupAuthorizationExpiresAt      *string                `json:"groupAuthorizationExpiresAt,omitempty"`
	GroupAuthorizationQuotaLimited   *bool                  `json:"groupAuthorizationQuotaLimited,omitempty"`
	GroupAuthorizationSourceType     *string                `json:"groupAuthorizationSourceType,omitempty"`
	GroupAuthorizationSourceTeamID   *string                `json:"groupAuthorizationSourceTeamId,omitempty"`
}

// CloneGroupUsageAccessMetadata mirrors cloneGroupUsageAccessMetadata.
func CloneGroupUsageAccessMetadata(value GroupUsageAccessMetadata) GroupUsageAccessMetadata {
	out := value
	if value.SchedulingPolicy != nil {
		policy := make(GroupSchedulingPolicy, len(*value.SchedulingPolicy))
		for key, item := range *value.SchedulingPolicy {
			policy[key] = item
		}
		out.SchedulingPolicy = &policy
	}
	return out
}

// OpenAIAccountsForGroupDiagnostics mirrors OpenAIAccountsForGroupDiagnostics.
type OpenAIAccountsForGroupDiagnostics struct {
	ScanLimit             int   `json:"scanLimit"`
	FinalLimit            int   `json:"finalLimit"`
	CandidateRowCount     int   `json:"candidateRowCount"`
	ScannedRowCount       int   `json:"scannedRowCount"`
	EligibleRowCount      int   `json:"eligibleRowCount"`
	HydrationBatchCount   int   `json:"hydrationBatchCount"`
	HydratedAccountCount  int   `json:"hydratedAccountCount"`
	HydrationDroppedCount int   `json:"hydrationDroppedCount"`
	FinalAccountCount     int   `json:"finalAccountCount"`
	ScanLimitReached      bool  `json:"scanLimitReached"`
}

// OpenAIAccountsForGroupResult mirrors OpenAIAccountsForGroupResult.
type OpenAIAccountsForGroupResult struct {
	Accounts    []OpenAIAccountSecret             `json:"accounts"`
	Diagnostics *OpenAIAccountsForGroupDiagnostics `json:"diagnostics,omitempty"`
}

// CachedOpenAIAccountsForGroupOptions mirrors CachedOpenAIAccountsForGroupOptions.
type CachedOpenAIAccountsForGroupOptions struct {
	RequestedModel         string
	RequestedEndpointFamily string
}

// ResponseInspectionPolicyMatch mirrors ResponseInspectionPolicyMatch; the
// gateway runtime consumers only read known keys.
type ResponseInspectionPolicyMatch struct {
	ClientProfiles        []string `json:"clientProfiles,omitempty"`
	OutputTextIncludes    []string `json:"outputTextIncludes,omitempty"`
	OutputTextExcludes    []string `json:"outputTextExcludes,omitempty"`
	ErrorCodes            []string `json:"errorCodes,omitempty"`
	ErrorTypes            []string `json:"errorTypes,omitempty"`
	ErrorMessagesIncludes []string `json:"errorMessageIncludes,omitempty"`
	FinishReasons         []string `json:"finishReasons,omitempty"`
	JSONPathsExists       []string `json:"jsonPathsExists,omitempty"`
	RawTextIncludes       []string `json:"rawTextIncludes,omitempty"`
}

// CloneResponseInspectionPolicyMatch mirrors { ...policy.match } plus list copies.
func (m ResponseInspectionPolicyMatch) Clone() ResponseInspectionPolicyMatch {
	return ResponseInspectionPolicyMatch{
		ClientProfiles:        append([]string(nil), m.ClientProfiles...),
		OutputTextIncludes:    append([]string(nil), m.OutputTextIncludes...),
		OutputTextExcludes:    append([]string(nil), m.OutputTextExcludes...),
		ErrorCodes:            append([]string(nil), m.ErrorCodes...),
		ErrorTypes:            append([]string(nil), m.ErrorTypes...),
		ErrorMessagesIncludes: append([]string(nil), m.ErrorMessagesIncludes...),
		FinishReasons:         append([]string(nil), m.FinishReasons...),
		JSONPathsExists:       append([]string(nil), m.JSONPathsExists...),
		RawTextIncludes:       append([]string(nil), m.RawTextIncludes...),
	}
}

// ResponseInspectionPolicySummary mirrors ResponseInspectionPolicySummary
// (storage/response-inspection-policy.repository.ts).
type ResponseInspectionPolicySummary struct {
	ID           string                        `json:"id"`
	DefaultRule  bool                          `json:"defaultRule"`
	Editable     bool                          `json:"editable"`
	Name         string                        `json:"name"`
	Enabled      bool                          `json:"enabled"`
	Priority     int                           `json:"priority"`
	ScopeType    string                        `json:"scopeType"`
	ProtocolCode string                        `json:"protocolCode"`
	ProviderCode *string                       `json:"providerCode,omitempty"`
	Match        ResponseInspectionPolicyMatch `json:"match"`
	Action       string                        `json:"action"`
	Notes        *string                       `json:"notes,omitempty"`
	CreatedAt    *string                       `json:"createdAt,omitempty"`
	UpdatedAt    *string                       `json:"updatedAt,omitempty"`
}

// CloneResponseInspectionPolicy mirrors cloneResponseInspectionPolicy.
func CloneResponseInspectionPolicy(policy ResponseInspectionPolicySummary) ResponseInspectionPolicySummary {
	out := policy
	out.ProviderCode = cloneString(policy.ProviderCode)
	out.Notes = cloneString(policy.Notes)
	out.CreatedAt = cloneString(policy.CreatedAt)
	out.UpdatedAt = cloneString(policy.UpdatedAt)
	out.Match = policy.Match.Clone()
	return out
}

// CloneResponseInspectionPolicies clones a summary list.
func CloneResponseInspectionPolicies(policies []ResponseInspectionPolicySummary) []ResponseInspectionPolicySummary {
	out := make([]ResponseInspectionPolicySummary, len(policies))
	for i, policy := range policies {
		out[i] = CloneResponseInspectionPolicy(policy)
	}
	return out
}

// ProviderModelRouteResolution mirrors ProviderModelRouteResolution: matched
// carries the single winning provider, missing/ambiguous only the candidates.
type ProviderModelRouteResolution struct {
	Outcome              ProviderModelRouteOutcome `json:"outcome"`
	ModelKey             string                    `json:"modelKey"`
	ProviderCode         string                    `json:"providerCode,omitempty"`
	MatchedProviderCodes []string                  `json:"matchedProviderCodes"`
}

// ProviderModelRouteOutcome mirrors the discriminated union outcome strings.
type ProviderModelRouteOutcome string

const (
	ProviderModelRouteMatched   ProviderModelRouteOutcome = "matched"
	ProviderModelRouteMissing   ProviderModelRouteOutcome = "missing"
	ProviderModelRouteAmbiguous ProviderModelRouteOutcome = "ambiguous"
)

// GatewayRuntime mirrors DbServiceGatewayRuntime
// (modules/db-service/db-service-types.ts): the full validated runtime
// snapshot served to the dispatch path.
type GatewayRuntime struct {
	APIKey                     *GatewayAPIKeyRow                 `json:"apiKey,omitempty"`
	Settings                   GatewaySettings                   `json:"settings"`
	GroupAccess                *GroupUsageAccessMetadata         `json:"groupAccess,omitempty"`
	Accounts                   []OpenAIAccountSecret             `json:"accounts"`
	AccountDispatchDiagnostics *OpenAIAccountsForGroupDiagnostics `json:"accountDispatchDiagnostics,omitempty"`
	ResponseInspectionPolicies []ResponseInspectionPolicySummary `json:"responseInspectionPolicies,omitempty"`
}

// ProviderModelCatalogItem mirrors ProviderModelCatalogItem
// (modules/model-pricing/model-catalog.service.ts = ProviderModelPricing minus
// defaultReasoningEffort plus the catalog extension fields). The runtime cache
// is a pass-through carrier for these items (route resolution only reads
// Model), so the deep billing structures stay raw JSON exactly as delivered by
// the loader; the flat pricing fields are mirrored field by field.
type ProviderModelCatalogItem struct {
	ID                                       *string          `json:"id,omitempty"`
	Scope                                    string           `json:"scope"`
	Status                                   string           `json:"status"`
	ProviderCode                             string           `json:"providerCode"`
	Model                                    string           `json:"model"`
	Mode                                     *string          `json:"mode,omitempty"`
	CatalogOrder                             *int             `json:"catalogOrder,omitempty"`
	ReleaseDate                              *string          `json:"releaseDate,omitempty"`
	ShutdownDate                             *string          `json:"shutdownDate,omitempty"`
	SupportedAPIProtocols                    []string         `json:"supportedApiProtocols"`
	InputModalities                          []string         `json:"inputModalities"`
	OutputModalities                         []string         `json:"outputModalities"`
	SupportedTools                           []string         `json:"supportedTools"`
	GenerationParameterCapabilities          json.RawMessage  `json:"generationParameterCapabilities,omitempty"`
	InputUsdPer1M                            *float64         `json:"inputUsdPer1M,omitempty"`
	OutputUsdPer1M                           *float64         `json:"outputUsdPer1M,omitempty"`
	CachedInputUsdPer1M                      *float64         `json:"cachedInputUsdPer1M,omitempty"`
	CacheWriteUsdPer1M                       *float64         `json:"cacheWriteUsdPer1M,omitempty"`
	CacheWrite1hUsdPer1M                     *float64         `json:"cacheWrite1hUsdPer1M,omitempty"`
	CacheStorageUsdPer1MPerHour              *float64         `json:"cacheStorageUsdPer1MPerHour,omitempty"`
	ServiceTierPrices                        json.RawMessage  `json:"serviceTierPrices,omitempty"`
	ImageInputUsdPer1M                       *float64         `json:"imageInputUsdPer1M,omitempty"`
	CachedImageInputUsdPer1M                 *float64         `json:"cachedImageInputUsdPer1M,omitempty"`
	ImageOutputUsdPer1M                      *float64         `json:"imageOutputUsdPer1M,omitempty"`
	AudioInputUsdPer1M                       *float64         `json:"audioInputUsdPer1M,omitempty"`
	AudioOutputUsdPer1M                      *float64         `json:"audioOutputUsdPer1M,omitempty"`
	OutputUsdPerImage                        *float64         `json:"outputUsdPerImage,omitempty"`
	ContextWindowTokens                      *int64           `json:"contextWindowTokens,omitempty"`
	MaxInputTokens                           *int64           `json:"maxInputTokens,omitempty"`
	MaxOutputTokens                          *int64           `json:"maxOutputTokens,omitempty"`
	MaxTokens                                *int64           `json:"maxTokens,omitempty"`
	LongContextInputTokenThreshold           *int64           `json:"longContextInputTokenThreshold,omitempty"`
	LongContextInputTokenThresholdInclusive  *bool            `json:"longContextInputTokenThresholdInclusive,omitempty"`
	LongContextInputCostMultiplier           *float64         `json:"longContextInputCostMultiplier,omitempty"`
	LongContextOutputCostMultiplier          *float64         `json:"longContextOutputCostMultiplier,omitempty"`
	SupportsPromptCaching                    bool             `json:"supportsPromptCaching"`
	SupportedServiceTiers                    []string         `json:"supportedServiceTiers"`
	SupportedReasoningEfforts                []string         `json:"supportedReasoningEfforts"`
	DefaultReasoningEffort                   *string          `json:"defaultReasoningEffort,omitempty"`
	CodexSupportedReasoningLevels            json.RawMessage  `json:"codexSupportedReasoningLevels,omitempty"`
	CodexDefaultReasoningLevel               json.RawMessage  `json:"codexDefaultReasoningLevel,omitempty"`
	CodexMultiAgentVersion                   *string          `json:"codexMultiAgentVersion,omitempty"`
	SupportsServiceTier                      bool             `json:"supportsServiceTier"`
	CatalogVisible                           *bool            `json:"catalogVisible,omitempty"`
	SourcePricingCurrency                    *string          `json:"sourcePricingCurrency,omitempty"`
	SourceExchangeRateToUsd                  *float64         `json:"sourceExchangeRateToUsd,omitempty"`
	SourceExchangeRateDate                   *string          `json:"sourceExchangeRateDate,omitempty"`
	SourcePricingNote                        *string          `json:"sourcePricingNote,omitempty"`
	Source                                   string           `json:"source"`
	SystemAccountID                          *string          `json:"systemAccountId,omitempty"`
	PricingNotes                             *string          `json:"pricingNotes,omitempty"`
	CapabilityNotes                          *string          `json:"capabilityNotes,omitempty"`
	Notes                                    *string          `json:"notes,omitempty"`
	CreatedAt                                *string          `json:"createdAt,omitempty"`
	UpdatedAt                                *string          `json:"updatedAt,omitempty"`
	CatalogDisplay                           json.RawMessage  `json:"catalogDisplay,omitempty"`
}

// CloneProviderModelCatalogItems mirrors the per-item {...item} shallow clone.
func CloneProviderModelCatalogItems(items []ProviderModelCatalogItem) []ProviderModelCatalogItem {
	out := make([]ProviderModelCatalogItem, len(items))
	for i, item := range items {
		cloned := item
		if item.SupportedAPIProtocols != nil {
			cloned.SupportedAPIProtocols = append([]string(nil), item.SupportedAPIProtocols...)
		}
		if item.InputModalities != nil {
			cloned.InputModalities = append([]string(nil), item.InputModalities...)
		}
		if item.OutputModalities != nil {
			cloned.OutputModalities = append([]string(nil), item.OutputModalities...)
		}
		if item.SupportedTools != nil {
			cloned.SupportedTools = append([]string(nil), item.SupportedTools...)
		}
		if item.SupportedServiceTiers != nil {
			cloned.SupportedServiceTiers = append([]string(nil), item.SupportedServiceTiers...)
		}
		if item.SupportedReasoningEfforts != nil {
			cloned.SupportedReasoningEfforts = append([]string(nil), item.SupportedReasoningEfforts...)
		}
		out[i] = cloned
	}
	return out
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

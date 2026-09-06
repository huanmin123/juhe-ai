package accounts

// M11 advanced read family: GET /{id}/advanced, GET
// /{id}/oauth-reauthorization-context and GET /{id}/api-key-runtime (Node
// account-detail.routes.ts + account-advanced-detail.repository.ts +
// account-interaction-context.repository.ts +
// account-api-key-runtime.repository.ts + account-api-key-pool-runtime.ts).
// All three reads share the parseRequestScopeQuery + canManageResourceOwner
// guard: the row must exist, stay undeleted and sit inside the request scope.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

// advancedEditableCredentialKeys mirrors advancedEditableCredentialKeys
// (account-advanced-detail.repository.ts): the credential subset the advanced
// editor owns. The Go gateway masks secret material through
// maskCredentialValue exactly like the edit-basic surface.
var advancedEditableCredentialKeys = []string{
	"service_tier_override",
	"reasoning_effort_override",
	"error_handling_rules",
	"error_handling_rule_overrides",
	"response_inspection_rules",
	"quota_recovery_policy",
}

// EffectiveAccountErrorHandlingRule mirrors EffectiveAccountErrorHandlingRule
// (account-error-policy-system-rules.ts): the account rule plus the system
// projection fields.
type EffectiveAccountErrorHandlingRule struct {
	ID        string `json:"id"`
	Source    string `json:"source"`
	Inherited bool   `json:"inherited"`
	Editable  bool   `json:"editable"`
	Rule      any    `json:"-"`
}

// MarshalJSON flattens the rule fields into the object exactly like the Node
// spread ({...cloneRule(rule), id, source, inherited, editable}).
func (r EffectiveAccountErrorHandlingRule) MarshalJSON() ([]byte, error) {
	base, ok := r.Rule.(map[string]any)
	if !ok {
		base = map[string]any{}
	}
	merged := make(map[string]any, len(base)+4)
	for key, value := range base {
		merged[key] = value
	}
	merged["id"] = r.ID
	merged["source"] = r.Source
	merged["inherited"] = r.Inherited
	merged["editable"] = r.Editable
	return json.Marshal(merged)
}

// systemInsufficientQuotaRuleID mirrors SYSTEM_INSUFFICIENT_QUOTA_ERROR_POLICY_RULE_ID.
const systemInsufficientQuotaRuleID = "system.upstream_insufficient_quota"

// systemAccountErrorHandlingRule mirrors the code-defined system registry
// (account-error-policy-system-rules.ts systemRules[0]).
func systemAccountErrorHandlingRule() map[string]any {
	return map[string]any{
		"enabled":         true,
		"name":            "上游额度不足",
		"priority":        float64(1),
		"action":          "rate_limited",
		"reset_strategy":  "duration",
		"duration_hours":  float64(1),
		"status_codes":    []any{float64(402), float64(403)},
		"error_codes":     []any{"insufficient_user_quota", "insufficient_quota", "insufficient_balance", "quota_exceeded", "quota_exhausted", "default_group_global_quota_exhausted", "billing_hard_limit_reached", "wallet_balance_exhausted", "pre_consume_token_quota_failed"},
		"keywords":        []any{"余额不足", "额度不足", "insufficient balance", "insufficient quota", "subscription quota insufficient", "credit balance too low", "wallet balance exhausted"},
		"description":     "匹配 HTTP 402 或 HTTP 403 的明确余额/额度不足响应；默认进入限流，可按普通规则调整恢复策略。",
	}
}

// effectiveAccountErrorHandlingRules mirrors effectiveAccountErrorHandlingRules:
// the system quota rule (unless deleted/replaced) followed by the account
// rules ordered by priority with the stable original indexes.
func effectiveAccountErrorHandlingRules(value any, overridesValue any) ([]EffectiveAccountErrorHandlingRule, error) {
	accountRules, err := normalizeAccountErrorHandlingRules(value)
	if err != nil {
		return nil, err
	}
	overrides, err := normalizeAccountErrorPolicyOverrides(overridesValue)
	if err != nil {
		return nil, err
	}
	var quotaOverride map[string]any
	for _, overrideAny := range overrides {
		if override, ok := overrideAny.(map[string]any); ok {
			quotaOverride = override
		}
		break
	}
	replacedIndex := -1
	if quotaOverride != nil && quotaOverride["action"] == "replace" {
		if number, ok := quotaOverride["rule_index"].(float64); ok {
			replacedIndex = int(number)
		}
	}
	type indexedRule struct {
		rule  map[string]any
		index int
	}
	ordered := make([]indexedRule, 0, len(accountRules))
	for index, rule := range accountRules {
		typed, ok := rule.(map[string]any)
		if !ok {
			typed = map[string]any{}
		}
		ordered = append(ordered, indexedRule{rule: cloneRuleMap(typed), index: index})
	}
	sort.SliceStable(ordered, func(left, right int) bool {
		return rulePriority(ordered[left].rule) < rulePriority(ordered[right].rule)
	})
	effective := []EffectiveAccountErrorHandlingRule{}
	if quotaOverride == nil || quotaOverride["action"] != "delete" && quotaOverride["action"] != "replace" {
		effective = append(effective, EffectiveAccountErrorHandlingRule{
			ID: systemInsufficientQuotaRuleID, Source: "system", Inherited: true, Editable: false,
			Rule: systemAccountErrorHandlingRule(),
		})
	}
	for _, item := range ordered {
		id := "account." + itoa(item.index+1)
		if item.index == replacedIndex {
			id = systemInsufficientQuotaRuleID
		}
		effective = append(effective, EffectiveAccountErrorHandlingRule{
			ID: id, Source: "account", Inherited: false, Editable: true,
			Rule: item.rule,
		})
	}
	return effective, nil
}

func cloneRuleMap(rule map[string]any) map[string]any {
	clone := make(map[string]any, len(rule))
	for key, value := range rule {
		clone[key] = value
	}
	return clone
}

func rulePriority(rule map[string]any) float64 {
	if number, ok := rule["priority"].(float64); ok {
		return number
	}
	return 0
}

// defaultAPIKeyQuotaRecoverySchedule / defaultOAuthQuotaRecoverySchedule
// mirror the Node default schedules (quota-recovery-policy.ts:26-38).
func defaultQuotaRecoverySchedule(accountType string) map[string]any {
	if accountType == "api_key" {
		return map[string]any{
			"reset_strategy":  "duration",
			"duration_minutes": float64(60),
			"jitter_minutes":  float64(quotaRecoveryFixedJitterMinutes),
			"timezone":        "UTC",
		}
	}
	return map[string]any{
		"reset_strategy":  "daily",
		"daily_reset_hour": float64(0),
		"jitter_minutes":  float64(quotaRecoveryFixedJitterMinutes),
		"timezone":        "UTC",
	}
}

// quotaRecoveryScheduleForAccount mirrors quotaRecoveryScheduleForAccount:
// the typed fallback merged with the configured schedule.
func quotaRecoveryScheduleForAccount(policy map[string]any, accountType string) map[string]any {
	fallback := defaultQuotaRecoverySchedule(accountType)
	configured, _ := policy[accountType].(map[string]any)
	merged := make(map[string]any, len(fallback)+len(configured))
	for key, value := range fallback {
		merged[key] = value
	}
	for key, value := range configured {
		merged[key] = value
	}
	return merged
}

// AdvancedDetail mirrors AccountAdvancedDetail
// (account-advanced-detail.repository.ts).
type AdvancedDetail struct {
	ID                                            string                              `json:"id"`
	ConfigRevision                                int64                               `json:"configRevision"`
	AccessType                                    string                              `json:"accessType"`
	Credentials                                   Credentials                         `json:"credentials,omitempty"`
	EffectiveQuotaRecoveryPolicy                  map[string]map[string]any           `json:"effectiveQuotaRecoveryPolicy"`
	EffectiveErrorHandlingRules                   []EffectiveAccountErrorHandlingRule `json:"effectiveErrorHandlingRules"`
	ModelMappings                                 []ModelMapping                      `json:"modelMappings"`
	ProxyProfileID                                *string                             `json:"proxyProfileId,omitempty"`
	AvailabilitySchedule                          *AvailabilitySchedule               `json:"availabilitySchedule,omitempty"`
	AccountExpiresAt                              *string                             `json:"accountExpiresAt,omitempty"`
	TemporaryUnavailableContinuousProbeEnabled    bool                                `json:"temporaryUnavailableContinuousProbeEnabled"`
	BalanceQueryEnabled                           bool                                `json:"balanceQueryEnabled"`
	BalanceQueryConfig                            map[string]any                      `json:"balanceQueryConfig,omitempty"`
	AuthorizationInstanceSourceAccountStatus      *string                             `json:"authorizationInstanceSourceAccountStatus,omitempty"`
	AuthorizationInstanceSourceAccountSchedulable *bool                               `json:"authorizationInstanceSourceAccountSchedulable,omitempty"`
	LockEnabled                                   bool                                `json:"lockEnabled"`
	LockState                                     string                              `json:"lockState"`
	LockDeathTimeoutSeconds                       int                                 `json:"lockDeathTimeoutSeconds"`
	LockRetryIntervalSeconds                      int                                 `json:"lockRetryIntervalSeconds"`
}

// FindAdvancedDetail mirrors findAccountAdvancedDetailAsync: the scope-checked
// row plus the effective policy projection. Returns (nil, nil) when the
// account is missing, outside the scope or a stamped instance whose runtime
// authorization is no longer active (route renders 404 账户不存在).
func (s *Store) FindAdvancedDetail(ctx context.Context, accountID string, access AccessScope) (*AdvancedDetail, error) {
	ctx = ensureCtx(ctx)
	id := strings.TrimSpace(accountID)
	if id == "" {
		return nil, nil
	}
	now := isoMillis(s.now())
	var row struct {
		id                         string
		configRevision             int64
		systemAccountID            string
		credentialsEncrypted       sql.NullString
		sourceCredentialsEncrypted sql.NullString
		proxyProfileID             sql.NullString
		availabilityScheduleJSON   sql.NullString
		accountExpiresAt           sql.NullString
		probeEnabled               int
		balanceQueryEnabled        int
		balanceQueryConfigJSON     string
		authorizationID            sql.NullString
		sourceAccountID            sql.NullString
		activeAuthorizationID      sql.NullString
		sourceStatus               sql.NullString
		sourceSchedulable          sql.NullInt64
		sourceProxyProfileID       sql.NullString
		sourceAvailabilityJSON     sql.NullString
		sourceAccountExpiresAt     sql.NullString
		sourceProbeEnabled         sql.NullInt64
	}
	authorized := s.authorizedReadableIDs(ctx, access)[id]
	scopeClause := ""
	args := []any{now, id}
	if scoped := access.manageableID(); scoped != "" && !authorized {
		scopeClause = " AND accounts.system_account_id = ?"
		args = append(args, scoped)
	}
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT accounts.id, accounts.config_revision,
			accounts.system_account_id,
			CASE
				WHEN accounts.authorization_instance_authorization_id IS NULL
					AND accounts.authorization_instance_source_account_id IS NULL
				THEN accounts.credentials_encrypted
				ELSE NULL
			END AS credentials_encrypted,
			source_accounts.credentials_encrypted AS source_credentials_encrypted,
			accounts.proxy_profile_id,
			accounts.availability_schedule_json,
			accounts.account_expires_at,
			accounts.temporary_unavailable_continuous_probe_enabled,
			accounts.balance_query_enabled,
			accounts.balance_query_config_json,
			accounts.authorization_instance_authorization_id,
			accounts.authorization_instance_source_account_id,
			active_authorizations.id AS active_authorization_id,
			source_accounts.status AS source_status,
			source_accounts.schedulable AS source_schedulable,
			source_accounts.proxy_profile_id AS source_proxy_profile_id,
			source_accounts.availability_schedule_json AS source_availability_schedule_json,
			source_accounts.account_expires_at AS source_account_expires_at,
			source_accounts.temporary_unavailable_continuous_probe_enabled AS source_temporary_unavailable_continuous_probe_enabled
		FROM `+s.table("accounts")+` accounts
		LEFT JOIN `+s.table("resource_authorizations")+` active_authorizations
			ON active_authorizations.id = accounts.authorization_instance_authorization_id
			AND active_authorizations.resource_type = 'account'
			AND active_authorizations.resource_id = accounts.authorization_instance_source_account_id
			AND active_authorizations.resource_owner_system_account_id = accounts.authorization_instance_owner_system_account_id
			AND active_authorizations.grantee_system_account_id = accounts.system_account_id
			AND active_authorizations.status = 'active'
			AND active_authorizations.effective_source_type IN ('manual', 'team')
			AND (active_authorizations.expires_at IS NULL OR active_authorizations.expires_at > `+"?"+`)
		LEFT JOIN `+s.table("accounts")+` source_accounts
			ON source_accounts.id = accounts.authorization_instance_source_account_id
			AND source_accounts.system_account_id = active_authorizations.resource_owner_system_account_id
			AND source_accounts.deleted_at IS NULL
		WHERE accounts.id = ?
			AND accounts.deleted_at IS NULL`+scopeClause+`
		LIMIT 1`), args...).Scan(
		&row.id, &row.configRevision, &row.systemAccountID,
		&row.credentialsEncrypted, &row.sourceCredentialsEncrypted,
		&row.proxyProfileID, &row.availabilityScheduleJSON, &row.accountExpiresAt,
		&row.probeEnabled, &row.balanceQueryEnabled, &row.balanceQueryConfigJSON,
		&row.authorizationID, &row.sourceAccountID, &row.activeAuthorizationID,
		&row.sourceStatus, &row.sourceSchedulable,
		&row.sourceProxyProfileID, &row.sourceAvailabilityJSON,
		&row.sourceAccountExpiresAt, &row.sourceProbeEnabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !access.canAccessAll() && row.systemAccountID != access.ViewerID && !authorized {
		return nil, nil
	}
	isAuthorized := row.authorizationID.Valid && row.authorizationID.String != "" ||
		row.sourceAccountID.Valid && row.sourceAccountID.String != ""
	if isAuthorized && !row.activeAuthorizationID.Valid {
		return nil, nil
	}
	factAccountID := row.id
	if isAuthorized {
		factAccountID = row.sourceAccountID.String
	}
	mappingRows, err := s.db.QueryContext(ctx, s.bind(`SELECT source_model, source_endpoint_family, upstream_model, upstream_endpoint_family, enabled
		FROM `+s.table("account_model_mappings")+`
		WHERE account_id = ?
		ORDER BY source_model ASC, source_endpoint_family ASC`), factAccountID)
	if err != nil {
		return nil, err
	}
	mappings := []ModelMapping{}
	for mappingRows.Next() {
		var mapping ModelMapping
		var enabled int
		if err := mappingRows.Scan(&mapping.SourceModel, &mapping.SourceEndpointFamily,
			&mapping.UpstreamModel, &mapping.UpstreamEndpointFamily, &enabled); err != nil {
			mappingRows.Close()
			return nil, err
		}
		enabledFlag := enabled == 1
		mapping.Enabled = &enabledFlag
		mappings = append(mappings, mapping)
	}
	mappingRows.Close()
	if err := mappingRows.Err(); err != nil {
		return nil, err
	}
	// Credentials: the owner branch decrypts the row envelope and projects the
	// advanced-editable keys; the authorized branch derives the effective
	// policy from the source credentials and never surfaces material.
	ownerCredentials := Credentials{}
	if !isAuthorized && row.credentialsEncrypted.Valid && row.credentialsEncrypted.String != "" {
		if err := DecryptJSON(s.secret, row.credentialsEncrypted.String, &ownerCredentials); err != nil {
			return nil, err
		}
	}
	sourceCredentials := Credentials{}
	if isAuthorized && row.sourceCredentialsEncrypted.Valid && row.sourceCredentialsEncrypted.String != "" {
		if err := DecryptJSON(s.secret, row.sourceCredentialsEncrypted.String, &sourceCredentials); err != nil {
			return nil, err
		}
	}
	effectivePolicySource := ownerCredentials
	if isAuthorized {
		effectivePolicySource = sourceCredentials
	}
	policy, err := normalizeQuotaRecoveryPolicy(effectivePolicySource["quota_recovery_policy"])
	if err != nil {
		return nil, err
	}
	effectivePolicy := map[string]map[string]any{
		"api_key":      quotaRecoveryScheduleForAccount(policy, "api_key"),
		"oauth":        quotaRecoveryScheduleForAccount(policy, "oauth"),
		"google_oauth": quotaRecoveryScheduleForAccount(policy, "google_oauth"),
	}
	effectiveRules, err := effectiveAccountErrorHandlingRules(
		ownerCredentials["error_handling_rules"], ownerCredentials["error_handling_rule_overrides"])
	if err != nil {
		return nil, err
	}
	detail := &AdvancedDetail{
		ID:                           row.id,
		ConfigRevision:               row.configRevision,
		AccessType:                   "owner",
		EffectiveQuotaRecoveryPolicy: effectivePolicy,
		EffectiveErrorHandlingRules:  effectiveRules,
		ModelMappings:                mappings,
		ProxyProfileID:               nullPtrString(row.proxyProfileID),
		AccountExpiresAt:             nullPtrString(row.accountExpiresAt),
		TemporaryUnavailableContinuousProbeEnabled: row.probeEnabled == 1,
		BalanceQueryEnabled:                        row.balanceQueryEnabled == 1,
		LockEnabled:             false,
		LockState:               "UNLOCKED",
		LockDeathTimeoutSeconds: 300,
		LockRetryIntervalSeconds: 5,
	}
	if isAuthorized {
		detail.AccessType = "authorized"
		detail.ProxyProfileID = nullPtrString(row.sourceProxyProfileID)
		detail.AccountExpiresAt = nullPtrString(row.sourceAccountExpiresAt)
		detail.TemporaryUnavailableContinuousProbeEnabled = row.sourceProbeEnabled.Valid && row.sourceProbeEnabled.Int64 == 1
		detail.BalanceQueryEnabled = false
		if row.sourceStatus.Valid && row.sourceStatus.String != "" {
			status := row.sourceStatus.String
			detail.AuthorizationInstanceSourceAccountStatus = &status
		}
		if row.sourceSchedulable.Valid {
			schedulable := row.sourceSchedulable.Int64 == 1
			detail.AuthorizationInstanceSourceAccountSchedulable = &schedulable
		}
	} else {
		if credentials := projectAdvancedEditableCredentials(ownerCredentials); len(credentials) > 0 {
			detail.Credentials = credentials
		}
		if raw := strings.TrimSpace(row.balanceQueryConfigJSON); raw != "" {
			var parsed any
			if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
				if normalized, err := NormalizeAccountBalanceConfig(parsed); err == nil {
					detail.BalanceQueryConfig = normalized
				}
			}
		}
	}
	if scheduleJSON := strings.TrimSpace(valueOrSource(row.availabilityScheduleJSON, row.sourceAvailabilityJSON, isAuthorized)); scheduleJSON != "" {
		var parsed any
		if err := json.Unmarshal([]byte(scheduleJSON), &parsed); err == nil {
			if schedule, err := NormalizeSchedule(parsed); err == nil && schedule != nil {
				detail.AvailabilitySchedule = schedule
			}
		}
	}
	lock, err := s.findAccountLockState(ctx, s.db, row.id)
	if err != nil {
		return nil, err
	}
	if lock != nil {
		detail.LockEnabled = lock.enabled == 1
		detail.LockState = lock.lockState
		detail.LockDeathTimeoutSeconds = lock.deathTimeout
		detail.LockRetryIntervalSeconds = lock.retryInterval
	}
	return detail, nil
}

func valueOrSource(owner, source sql.NullString, authorized bool) string {
	if authorized {
		if source.Valid {
			return source.String
		}
		return ""
	}
	if owner.Valid {
		return owner.String
	}
	return ""
}

// projectAdvancedEditableCredentials mirrors projectAdvancedEditableCredentials
// with the Go slice hardening: secret material stays masked.
func projectAdvancedEditableCredentials(credentials Credentials) Credentials {
	output := Credentials{}
	for _, key := range advancedEditableCredentialKeys {
		value, ok := credentials[key]
		if !ok {
			continue
		}
		output[key] = maskCredentialValue(key, value)
	}
	return output
}

// OAuthReauthorizationContext mirrors AccountOAuthReauthorizationContext.
type OAuthReauthorizationContext struct {
	ID             string  `json:"id"`
	ConfigRevision int64   `json:"configRevision"`
	OAuthType      string  `json:"oauthType"`
	ClientID       *string `json:"clientId,omitempty"`
	ClientSecret   *string `json:"clientSecret,omitempty"`
	QuotaProjectID *string `json:"quotaProjectId,omitempty"`
	ProjectID      *string `json:"projectId,omitempty"`
	TierID         *string `json:"tierId,omitempty"`
	BaseURL        *string `json:"baseUrl,omitempty"`
}

// interactionContextForbiddenError mirrors AccountInteractionContextForbiddenError.
type interactionContextForbiddenError struct{ message string }

func (e *interactionContextForbiddenError) Error() string { return e.message }

// geminiCLIOAuthClientID mirrors geminiCliOAuthClientId.
const geminiCLIOAuthClientID = "681255809395-oo8ft2oprdrnp9e3aqf6av3hmdib135j.apps.googleusercontent.com"

// geminiOAuthMetadataKeys mirrors geminiOAuthMetadataKeys.
var geminiOAuthMetadataKeys = []string{
	"oauth_type", "client_id", "client_secret", "quota_project_id",
	"project_id", "tier_id", "base_url",
}

// geminiOAuthContextType mirrors geminiOAuthContextType.
func geminiOAuthContextType(credentials Credentials) string {
	switch credentialText(credentials["oauth_type"]) {
	case "code_assist", "google_one", "ai_studio":
		return credentialText(credentials["oauth_type"])
	}
	baseURL := credentialText(credentials["base_url"])
	if strings.Contains(baseURL, "generativelanguage.googleapis.com") {
		return "ai_studio"
	}
	if credentialText(credentials["project_id"]) != "" || strings.Contains(baseURL, "cloudcode-pa.googleapis.com") {
		return "code_assist"
	}
	clientID := credentialText(credentials["client_id"])
	if clientID != "" && clientID != geminiCLIOAuthClientID {
		return "ai_studio"
	}
	return "code_assist"
}

func credentialText(value any) string {
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}

// FindOAuthReauthorizationContext mirrors findAccountOAuthReauthorizationContextAsync:
// the Gemini google_oauth row only; instance rows render the 403 forbidden
// error and unknown accounts (nil, nil).
func (s *Store) FindOAuthReauthorizationContext(ctx context.Context, accountID string, access AccessScope) (*OAuthReauthorizationContext, error) {
	ctx = ensureCtx(ctx)
	id := strings.TrimSpace(accountID)
	if id == "" {
		return nil, nil
	}
	authorized := s.authorizedReadableIDs(ctx, access)[id]
	scopeClause := ""
	args := []any{id}
	if scoped := access.manageableID(); scoped != "" && !authorized {
		scopeClause = " AND accounts.system_account_id = ?"
		args = append(args, scoped)
	}
	var row struct {
		id               string
		configRevision   int64
		systemAccountID  string
		credentials      string
		authorizationID  sql.NullString
		sourceAccountID  sql.NullString
	}
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT accounts.id, accounts.config_revision,
			accounts.system_account_id, accounts.credentials_encrypted,
			accounts.authorization_instance_authorization_id,
			accounts.authorization_instance_source_account_id
		FROM `+s.table("accounts")+` accounts
		WHERE accounts.id = ?
			AND accounts.deleted_at IS NULL
			AND accounts.provider_code = 'gemini'
			AND accounts.type = 'google_oauth'`+scopeClause+`
		LIMIT 1`), args...).Scan(
		&row.id, &row.configRevision, &row.systemAccountID, &row.credentials,
		&row.authorizationID, &row.sourceAccountID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !access.canAccessAll() && row.systemAccountID != access.ViewerID && !authorized {
		return nil, nil
	}
	if row.authorizationID.Valid && row.authorizationID.String != "" ||
		row.sourceAccountID.Valid && row.sourceAccountID.String != "" {
		return nil, &interactionContextForbiddenError{message: "授权实例不能重新授权"}
	}
	credentials := Credentials{}
	if err := DecryptJSON(s.secret, row.credentials, &credentials); err != nil {
		return nil, err
	}
	metadata := projectCredentialKeys(credentials, geminiOAuthMetadataKeys)
	oauthType := geminiOAuthContextType(credentials)
	context := &OAuthReauthorizationContext{
		ID:             row.id,
		ConfigRevision: row.configRevision,
		OAuthType:      oauthType,
	}
	if oauthType == "ai_studio" {
		context.ClientID = stringContextField(metadata["client_id"])
		context.ClientSecret = stringContextField(metadata["client_secret"])
	}
	context.QuotaProjectID = stringContextField(metadata["quota_project_id"])
	context.ProjectID = stringContextField(metadata["project_id"])
	context.TierID = stringContextField(metadata["tier_id"])
	context.BaseURL = stringContextField(metadata["base_url"])
	return context, nil
}

func stringContextField(value any) *string {
	if text := credentialText(value); text != "" {
		return &text
	}
	return nil
}

// projectCredentialKeys mirrors projectCredentialKeys
// (account-interaction-context.repository.ts): the trimmed allow-list subset.
func projectCredentialKeys(credentials Credentials, keys []string) Credentials {
	output := Credentials{}
	for _, key := range keys {
		if value, ok := credentials[key]; ok {
			if text, ok := value.(string); ok {
				output[key] = strings.TrimSpace(text)
			}
		}
	}
	return output
}

// APIKeyRuntimeAccount mirrors AccountApiKeyRuntimeAccountProjection
// (account-api-key-runtime.repository.ts).
type APIKeyRuntimeAccount struct {
	ID                   string `json:"id"`
	ConfigRevision       int64  `json:"configRevision"`
	AccessType           string `json:"accessType"`
	OwnerSystemAccountID string `json:"ownerSystemAccountId"`
}

// APIKeyRuntimeResponse mirrors AccountApiKeyRuntimeResponse
// (account-api-key-pool-runtime.ts). Items ride the narrow
// APIKeyRuntimeDetailsReader port as pre-rendered Node contract objects.
type APIKeyRuntimeResponse struct {
	AccountID      string           `json:"accountId"`
	ConfigRevision int64            `json:"configRevision"`
	Items          []map[string]any `json:"items"`
}

// APIKeyRuntimeDetailsReader is the narrow cross-package port of the api-key
// runtime projection (Node loadAccountApiKeyRuntimeDetailsByAccountIdsAsync,
// account_api_key_runtime_states table + the key entries expansion). The
// composition root bridges it to the accountkeystates slice; a nil reader
// keeps the empty projection (Node renders [] for accounts without runtime
// rows).
type APIKeyRuntimeDetailsReader interface {
	LoadAPIKeyRuntimeDetails(ctx context.Context, accountID string) ([]map[string]any, error)
}

// SetAPIKeyRuntimeDetailsReader wires the reader (composition-root handover).
func (s *Store) SetAPIKeyRuntimeDetailsReader(reader APIKeyRuntimeDetailsReader) {
	s.apiKeyRuntimeDetails = reader
}

// FindAPIKeyRuntimeAccount mirrors findAccountApiKeyRuntimeAccountAsync: the
// scope-checked account projection the runtime read authorizes against.
func (s *Store) FindAPIKeyRuntimeAccount(ctx context.Context, accountID string, access AccessScope) (*APIKeyRuntimeAccount, error) {
	ctx = ensureCtx(ctx)
	id := strings.TrimSpace(accountID)
	if id == "" {
		return nil, nil
	}
	authorized := s.authorizedReadableIDs(ctx, access)[id]
	scopeClause := ""
	args := []any{id}
	if scoped := access.manageableID(); scoped != "" && !authorized {
		scopeClause = " AND accounts.system_account_id = ?"
		args = append(args, scoped)
	}
	var row struct {
		id             string
		configRevision int64
		systemAccountID string
		authorized     int
	}
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT accounts.id, accounts.config_revision,
			accounts.system_account_id,
			CASE
				WHEN accounts.authorization_instance_authorization_id IS NOT NULL
					OR accounts.authorization_instance_source_account_id IS NOT NULL
				THEN 1
				ELSE 0
			END AS authorized_instance
		FROM `+s.table("accounts")+` accounts
		WHERE accounts.id = ?
			AND accounts.deleted_at IS NULL`+scopeClause+`
		LIMIT 1`), args...).Scan(&row.id, &row.configRevision, &row.systemAccountID, &row.authorized)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !access.canAccessAll() && row.systemAccountID != access.ViewerID && !authorized {
		return nil, nil
	}
	accessType := "owner"
	if row.authorized == 1 {
		accessType = "authorized"
	}
	return &APIKeyRuntimeAccount{
		ID:                   row.id,
		ConfigRevision:       row.configRevision,
		AccessType:           accessType,
		OwnerSystemAccountID: row.systemAccountID,
	}, nil
}

// LoadAPIKeyRuntimeResponse mirrors loadOwnerAccountApiKeyRuntimeResponse:
// instance rows never surface the source pool; the reader supplies items.
func (s *Store) LoadAPIKeyRuntimeResponse(ctx context.Context, account *APIKeyRuntimeAccount) (*APIKeyRuntimeResponse, error) {
	if account.AccessType != "owner" {
		return nil, nil
	}
	items := []map[string]any{}
	if s.apiKeyRuntimeDetails != nil {
		loaded, err := s.apiKeyRuntimeDetails.LoadAPIKeyRuntimeDetails(ctx, account.ID)
		if err != nil {
			return nil, err
		}
		if loaded != nil {
			items = loaded
		}
	}
	return &APIKeyRuntimeResponse{
		AccountID:      account.ID,
		ConfigRevision: account.ConfigRevision,
		Items:          items,
	}, nil
}

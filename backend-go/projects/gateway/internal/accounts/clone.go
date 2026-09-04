package accounts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

// M09 clone-context slice: GET /accounts/{id}/clone-context ported from
// findAccountCloneContextAsync (storage/account-interaction-context.repository.ts).
// The context carries every cloneable field plus credential *options* (counts
// and non-secret configuration only) — raw secret material (api_key values,
// tokens) is never returned.

// CloneCredentialOptions mirrors AccountCloneCredentialOptions.
type CloneCredentialOptions struct {
	APIKeyCount             *int           `json:"api_key_count,omitempty"`
	APIKeyStrategy          *string        `json:"api_key_strategy,omitempty"`
	APIKeyWeights           []int          `json:"api_key_weights,omitempty"`
	BaseURL                 *string        `json:"base_url,omitempty"`
	SupportedEndpointModes  []string       `json:"supported_endpoint_modes,omitempty"`
	ClientID                *string        `json:"client_id,omitempty"`
	QuotaProjectID          *string        `json:"quota_project_id,omitempty"`
	OAuthType               *string        `json:"oauth_type,omitempty"`
	TierID                  *string        `json:"tier_id,omitempty"`
	ProjectID               *string        `json:"project_id,omitempty"`
	ServiceTierOverride     *string        `json:"service_tier_override,omitempty"`
	ReasoningEffortOverride *string        `json:"reasoning_effort_override,omitempty"`
	ErrorHandlingRules      []any          `json:"error_handling_rules,omitempty"`
	ResponseInspectionRules []any          `json:"response_inspection_rules,omitempty"`
	QuotaRecoveryPolicy     map[string]any `json:"quota_recovery_policy,omitempty"`
}

// CloneContext mirrors AccountCloneContext.
type CloneContext struct {
	ID                                         string                 `json:"id"`
	ConfigRevision                             int64                  `json:"configRevision"`
	ProviderCode                               string                 `json:"providerCode"`
	ProviderProtocolProfileID                  string                 `json:"providerProtocolProfileId"`
	ProtocolCode                               string                 `json:"protocolCode"`
	ProtocolVersion                            string                 `json:"protocolVersion"`
	Name                                       string                 `json:"name"`
	Notes                                      *string                `json:"notes,omitempty"`
	Type                                       string                 `json:"type"`
	Status                                     string                 `json:"status"`
	CredentialOptions                          CloneCredentialOptions `json:"credentialOptions"`
	ConcurrencyLimit                           int                    `json:"concurrencyLimit"`
	Priority                                   int                    `json:"priority"`
	SuperPriorityEnabled                       bool                   `json:"superPriorityEnabled"`
	FallbackEnabled                            bool                   `json:"fallbackEnabled"`
	ClientCompatibility                        string                 `json:"clientCompatibility"`
	SupportedModels                            []string               `json:"supportedModels"`
	Tags                                       []TagSummary           `json:"tags"`
	HealthCheckModel                           string                 `json:"healthCheckModel"`
	HealthCheckEndpointMode                    string                 `json:"healthCheckEndpointMode"`
	BoundGroupID                               *string                `json:"boundGroupId,omitempty"`
	BoundGroupName                             *string                `json:"boundGroupName,omitempty"`
	ModelMappings                              []ModelMapping         `json:"modelMappings"`
	ProxyProfileID                             *string                `json:"proxyProfileId,omitempty"`
	AvailabilitySchedule                       *AvailabilitySchedule  `json:"availabilitySchedule,omitempty"`
	AccountExpiresAt                           *string                `json:"accountExpiresAt,omitempty"`
	TemporaryUnavailableContinuousProbeEnabled bool                   `json:"temporaryUnavailableContinuousProbeEnabled"`
	BalanceQueryEnabled                        bool                   `json:"balanceQueryEnabled"`
	BalanceQueryConfig                         map[string]any         `json:"balanceQueryConfig,omitempty"`
}

// cloneInteractionForbiddenError mirrors AccountInteractionContextForbiddenError.
type cloneInteractionForbiddenError struct{ Message string }

func (e *cloneInteractionForbiddenError) Error() string { return e.Message }

// cloneInteractionConflictError mirrors AccountInteractionContextConflictError.
type cloneInteractionConflictError struct{}

func (e *cloneInteractionConflictError) Error() string {
	return "账户配置已发生变化，请重试"
}

// FindCloneContext mirrors findAccountCloneContextAsync: the scope-checked
// owner row plus its relations are read twice and the config_revision +
// bound-group triple must stay stable across both reads, otherwise the read
// retries once and then surfaces the conflict error. Authorization instances
// are forbidden (403 授权实例不能克隆). Returns (nil, nil) when the account is
// missing or outside the access scope (404 账户不存在).
func (s *Store) FindCloneContext(ctx context.Context, accountID string, access AccessScope) (*CloneContext, error) {
	ctx = ensureCtx(ctx)
	id := strings.TrimSpace(accountID)
	if id == "" {
		return nil, nil
	}
	for attempt := 0; attempt < 2; attempt++ {
		context, retry, err := s.readCloneContextOnce(ctx, id, access)
		if err != nil {
			return nil, err
		}
		if !retry {
			return context, nil
		}
	}
	return nil, &cloneInteractionConflictError{}
}

// boundGroupProjection mirrors accountInteractionContextCloneGroupProjection:
// the enabled binding wins, then the most recent updated_at, then the id.
func (s *Store) boundGroupProjection(column string) string {
	return `(
		SELECT ` + column + `
		FROM ` + s.table("group_accounts") + ` group_accounts
		INNER JOIN ` + s.table("groups") + ` groups
			ON groups.id = group_accounts.group_id
			AND groups.system_account_id = accounts.system_account_id
		WHERE group_accounts.account_id = accounts.id
			AND group_accounts.system_account_id = accounts.system_account_id
		ORDER BY CASE WHEN group_accounts.enabled = 1 THEN 0 ELSE 1 END,
			group_accounts.updated_at DESC, group_accounts.group_id ASC
		LIMIT 1
	)`
}

func (s *Store) boundGroupBindingUpdatedAtProjection() string {
	return `(
		SELECT group_accounts.updated_at
		FROM ` + s.table("group_accounts") + ` group_accounts
		INNER JOIN ` + s.table("groups") + ` groups
			ON groups.id = group_accounts.group_id
			AND groups.system_account_id = accounts.system_account_id
		WHERE group_accounts.account_id = accounts.id
			AND group_accounts.system_account_id = accounts.system_account_id
		ORDER BY CASE WHEN group_accounts.enabled = 1 THEN 0 ELSE 1 END,
			group_accounts.updated_at DESC, group_accounts.group_id ASC
		LIMIT 1
	)`
}

func (s *Store) readCloneContextOnce(ctx context.Context, id string, access AccessScope) (*CloneContext, bool, error) {
	scopeClause := ""
	args := []any{id}
	if scoped := access.manageableID(); scoped != "" {
		scopeClause = " AND accounts.system_account_id = ?"
		args = append(args, scoped)
	}
	var row struct {
		id                        string
		configRevision            int64
		systemAccountID           string
		providerCode              string
		providerProtocolProfileID string
		protocolCode              string
		protocolVersion           string
		name                      string
		notes                     sql.NullString
		accountType               string
		status                    string
		credentialsEncrypted      string
		concurrencyLimit          int
		priority                  int
		superPriorityEnabled      int64
		fallbackEnabled           int64
		clientCompatibility       string
		healthCheckModel          string
		healthCheckEndpointMode   string
		proxyProfileID            sql.NullString
		availabilitySchedule      sql.NullString
		accountExpiresAt          sql.NullString
		continuousProbeEnabled    int64
		balanceQueryEnabled       int64
		balanceQueryConfigJSON    string
		authorizationID           sql.NullString
		sourceAccountID           sql.NullString
		boundGroupID              sql.NullString
		boundGroupName            sql.NullString
		boundGroupBindingUpdAt    sql.NullString
		boundGroupRecordUpdAt     sql.NullString
	}
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT accounts.id, accounts.config_revision,
			accounts.system_account_id, accounts.provider_code, accounts.provider_protocol_profile_id,
			accounts.protocol_code, accounts.protocol_version, accounts.name, accounts.notes,
			accounts.type, accounts.status, accounts.credentials_encrypted, accounts.concurrency_limit,
			accounts.priority, accounts.super_priority_enabled, accounts.fallback_enabled,
			accounts.client_compatibility, accounts.health_check_model, accounts.health_check_endpoint_mode,
			accounts.proxy_profile_id, accounts.availability_schedule_json, accounts.account_expires_at,
			accounts.temporary_unavailable_continuous_probe_enabled, accounts.balance_query_enabled,
			accounts.balance_query_config_json,
			accounts.authorization_instance_authorization_id, accounts.authorization_instance_source_account_id,
			`+s.boundGroupProjection("group_accounts.group_id")+` AS bound_group_id,
			`+s.boundGroupProjection("groups.name")+` AS bound_group_name,
			`+s.boundGroupBindingUpdatedAtProjection()+` AS bound_group_binding_updated_at,
			`+s.boundGroupProjection("groups.updated_at")+` AS bound_group_record_updated_at
		FROM `+s.table("accounts")+` accounts
		WHERE accounts.id = ?
			AND accounts.deleted_at IS NULL`+scopeClause+`
		LIMIT 1`), args...).Scan(
		&row.id, &row.configRevision, &row.systemAccountID, &row.providerCode,
		&row.providerProtocolProfileID, &row.protocolCode, &row.protocolVersion, &row.name,
		&row.notes, &row.accountType, &row.status, &row.credentialsEncrypted,
		&row.concurrencyLimit, &row.priority, &row.superPriorityEnabled, &row.fallbackEnabled,
		&row.clientCompatibility, &row.healthCheckModel, &row.healthCheckEndpointMode,
		&row.proxyProfileID, &row.availabilitySchedule, &row.accountExpiresAt,
		&row.continuousProbeEnabled, &row.balanceQueryEnabled, &row.balanceQueryConfigJSON,
		&row.authorizationID, &row.sourceAccountID, &row.boundGroupID, &row.boundGroupName,
		&row.boundGroupBindingUpdAt, &row.boundGroupRecordUpdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	// canManageResourceOwner: users only ever manage their own rows.
	if !access.canAccessAll() && row.systemAccountID != access.ViewerID {
		return nil, false, nil
	}
	if row.authorizationID.Valid && row.authorizationID.String != "" ||
		row.sourceAccountID.Valid && row.sourceAccountID.String != "" {
		return nil, false, &cloneInteractionForbiddenError{Message: "授权实例不能克隆"}
	}

	// Relations: models, tags and mappings in the Node UNION ordering.
	relationRows, err := s.db.QueryContext(ctx, s.bind(`SELECT 'mapping' AS relation_kind,
			account_model_mappings.source_model AS value_a,
			account_model_mappings.source_endpoint_family AS value_b,
			account_model_mappings.upstream_model AS value_c,
			account_model_mappings.upstream_endpoint_family AS value_d,
			account_model_mappings.enabled AS enabled
		FROM `+s.table("account_model_mappings")+` account_model_mappings
		WHERE account_model_mappings.account_id = ?
		UNION ALL
		SELECT 'model' AS relation_kind, account_supported_models.model AS value_a,
			NULL AS value_b, NULL AS value_c, NULL AS value_d, NULL AS enabled
		FROM `+s.table("account_supported_models")+` account_supported_models
		WHERE account_supported_models.account_id = ?
		UNION ALL
		SELECT 'tag' AS relation_kind, account_tags.id AS value_a, account_tags.name AS value_b,
			NULL AS value_c, NULL AS value_d, NULL AS enabled
		FROM `+s.table("account_tag_bindings")+` account_tag_bindings
		INNER JOIN `+s.table("account_tags")+` account_tags
			ON account_tags.id = account_tag_bindings.tag_id
		WHERE account_tag_bindings.account_id = ?
			AND account_tag_bindings.system_account_id = ?
		ORDER BY relation_kind ASC, value_a ASC, value_b ASC`),
		row.id, row.id, row.id, row.systemAccountID)
	if err != nil {
		return nil, false, err
	}
	models := []string{}
	tags := []TagSummary{}
	mappings := []ModelMapping{}
	for relationRows.Next() {
		var kind, valueA string
		var valueB, valueC, valueD sql.NullString
		var enabled sql.NullInt64
		if err := relationRows.Scan(&kind, &valueA, &valueB, &valueC, &valueD, &enabled); err != nil {
			relationRows.Close()
			return nil, false, err
		}
		switch kind {
		case "model":
			models = append(models, valueA)
		case "tag":
			tags = append(tags, TagSummary{ID: valueA, Name: valueB.String})
		case "mapping":
			mappings = append(mappings, ModelMapping{
				SourceModel:            valueA,
				SourceEndpointFamily:   valueB.String,
				UpstreamModel:          valueC.String,
				UpstreamEndpointFamily: valueD.String,
				Enabled:                boolPtr(enabled.Int64 == 1),
			})
		}
	}
	relationRows.Close()
	if err := relationRows.Err(); err != nil {
		return nil, false, err
	}

	// Revision stability re-read (config_revision + bound-group triple).
	var revision struct {
		configRevision        int64
		boundGroupID          sql.NullString
		bindingUpdatedAt      sql.NullString
		boundGroupRecordUpdAt sql.NullString
	}
	err = s.db.QueryRowContext(ctx, s.bind(`SELECT accounts.config_revision,
			`+s.boundGroupProjection("group_accounts.group_id")+` AS bound_group_id,
			`+s.boundGroupBindingUpdatedAtProjection()+` AS bound_group_binding_updated_at,
			`+s.boundGroupProjection("groups.updated_at")+` AS bound_group_record_updated_at
		FROM `+s.table("accounts")+` accounts
		WHERE accounts.id = ?
			AND accounts.deleted_at IS NULL`+scopeClause+`
		LIMIT 1`), args...).Scan(
		&revision.configRevision, &revision.boundGroupID,
		&revision.bindingUpdatedAt, &revision.boundGroupRecordUpdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if revision.configRevision != row.configRevision ||
		!nullableTextEqual(nullPtrString(revision.boundGroupID), nullPtrString(row.boundGroupID)) ||
		!nullableTextEqual(nullPtrString(revision.bindingUpdatedAt), nullPtrString(row.boundGroupBindingUpdAt)) ||
		!nullableTextEqual(nullPtrString(revision.boundGroupRecordUpdAt), nullPtrString(row.boundGroupRecordUpdAt)) {
		return nil, true, nil
	}

	credentials := Credentials{}
	if strings.TrimSpace(row.credentialsEncrypted) != "" {
		if err := DecryptJSON(s.secret, row.credentialsEncrypted, &credentials); err != nil {
			return nil, false, err
		}
	}
	context := &CloneContext{
		ID:                        row.id,
		ConfigRevision:            row.configRevision,
		ProviderCode:              row.providerCode,
		ProviderProtocolProfileID: row.providerProtocolProfileID,
		ProtocolCode:              row.protocolCode,
		ProtocolVersion:           row.protocolVersion,
		Name:                      row.name,
		Notes:                     nullPtrString(row.notes),
		Type:                      row.accountType,
		Status:                    row.status,
		CredentialOptions:         projectCloneCredentialOptions(credentials),
		ConcurrencyLimit:          row.concurrencyLimit,
		Priority:                  row.priority,
		SuperPriorityEnabled:      row.superPriorityEnabled == 1,
		FallbackEnabled:           row.fallbackEnabled == 1,
		ClientCompatibility:       normalizeClientCompatibility(row.clientCompatibility),
		SupportedModels:           models,
		Tags:                      tags,
		HealthCheckModel:          strings.TrimSpace(row.healthCheckModel),
		HealthCheckEndpointMode:   row.healthCheckEndpointMode,
		BoundGroupID:              nullPtrString(row.boundGroupID),
		BoundGroupName:            nullPtrString(row.boundGroupName),
		ModelMappings:             mappings,
		ProxyProfileID:            nullPtrString(row.proxyProfileID),
		AccountExpiresAt:          nullPtrString(row.accountExpiresAt),
		TemporaryUnavailableContinuousProbeEnabled: row.continuousProbeEnabled == 1,
		BalanceQueryEnabled:                        row.balanceQueryEnabled == 1,
		BalanceQueryConfig:                         parseCloneBalanceConfig(row.balanceQueryConfigJSON),
	}
	if schedule, err := ParseScheduleJSON(row.availabilitySchedule.String); err == nil {
		context.AvailabilitySchedule = schedule
	}
	return context, false, nil
}

// projectCloneCredentialOptions mirrors projectCloneCredentialOptions: counts
// and configuration survive, secret material does not.
func projectCloneCredentialOptions(credentials Credentials) CloneCredentialOptions {
	out := CloneCredentialOptions{}
	if count := cloneCredentialAPIKeyCount(credentials); count > 0 {
		out.APIKeyCount = &count
	}
	if strategy, ok := credentials["api_key_strategy"].(string); ok {
		switch strategy {
		case "round_robin", "weighted_round_robin", "failover":
			text := strategy
			out.APIKeyStrategy = &text
		}
	}
	if list, ok := credentials["api_key_weights"].([]any); ok {
		weights := []int{}
		for _, item := range list {
			number, ok := item.(float64)
			if !ok || number != float64(int(number)) || int(number) < 1 || int(number) > 100 {
				continue
			}
			weights = append(weights, int(number))
		}
		out.APIKeyWeights = weights
	}
	out.BaseURL = credentialTextPointer(credentials, "base_url")
	if modes := storedEndpointModes(credentials["supported_endpoint_modes"]); len(modes) > 0 {
		out.SupportedEndpointModes = modes
	}
	out.ClientID = credentialTextPointer(credentials, "client_id")
	out.QuotaProjectID = credentialTextPointer(credentials, "quota_project_id")
	if oauthType, ok := credentials["oauth_type"].(string); ok {
		switch oauthType {
		case "code_assist", "google_one", "ai_studio":
			text := oauthType
			out.OAuthType = &text
		}
	}
	out.TierID = credentialTextPointer(credentials, "tier_id")
	out.ProjectID = credentialTextPointer(credentials, "project_id")
	out.ServiceTierOverride = credentialTextPointer(credentials, "service_tier_override")
	out.ReasoningEffortOverride = credentialTextPointer(credentials, "reasoning_effort_override")
	if list, ok := credentials["error_handling_rules"].([]any); ok {
		out.ErrorHandlingRules = list
	}
	if list, ok := credentials["response_inspection_rules"].([]any); ok {
		out.ResponseInspectionRules = list
	}
	if policy, ok := credentials["quota_recovery_policy"].(map[string]any); ok {
		out.QuotaRecoveryPolicy = policy
	}
	return out
}

func cloneCredentialAPIKeyCount(credentials Credentials) int {
	if list, ok := credentials["api_keys"].([]any); ok {
		count := 0
		for _, item := range list {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				count++
			}
		}
		if count > 50 {
			return 50
		}
		return count
	}
	if text, ok := credentials["api_key"].(string); ok && strings.TrimSpace(text) != "" {
		return 1
	}
	return 0
}

func credentialTextPointer(credentials Credentials, key string) *string {
	if text, ok := credentials[key].(string); ok {
		return &text
	}
	return nil
}

// parseCloneBalanceConfig mirrors parseAccountCloneBalanceConfig: a non-empty
// stored object passes through (the write path already normalized it).
func parseCloneBalanceConfig(raw string) map[string]any {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	var parsed any
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return nil
	}
	object, ok := parsed.(map[string]any)
	if !ok || len(object) == 0 {
		return nil
	}
	return object
}

package main

// G20 phase-2 composition-root account read seam: the
// gatewayruntimecache.AccountsSelector implementation the runtime cache needs
// to hydrate dispatchable OpenAI accounts for a group.
//
// Node authority: storage/openai-account-selector.repository.ts
// (listOpenAIAccountsForGroupResult) + storage/gateway-dispatch-candidate-window.repository.ts
// (listGatewayDispatchCandidateRows + ordering). The full Node repository
// (~1.4k lines incl. api-key rotation states, fresh quality scoring and proxy
// profiles) is its own migration slice; this composition selector covers the
// dispatch-critical core so the enabled chain serves real traffic:
//
//   - the Node candidate-window SQL verbatim (group_accounts join accounts,
//     source-account join for authorization instances, availability gates,
//     ownership/authorization access, local ordering),
//   - supported models + model mappings hydration,
//   - AES-GCM credential decryption (accounts.DecryptJSON) with the Node
//     runtimeOpenAIAccountCredentials projection.
//
// Registered takeover point (logged once at startup, chainRuntimeDeps log):
// the remaining branches (per-account api-key rotation runtime states, fresh
// gateway dispatch quality rows, proxy profile hydration, per-scan
// diagnostics) stay Node-owned until the dedicated account-selector slice
// lands; when it ships this file shrinks to the SQL handle wiring.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// dispatchCandidateScanLimit mirrors gatewayDispatchAccountCandidateScanLimit.
const dispatchCandidateScanLimit = 200

// dispatchCandidateFinalLimit mirrors gatewayDispatchAccountCandidateLimit.
const dispatchCandidateFinalLimit = 20

// chainAccountsSelector implements gatewayruntimecache.AccountsSelector over
// the business database.
type chainAccountsSelector struct {
	db       *sql.DB
	postgres bool
	secret   string
	now      func() time.Time
}

func newChainAccountsSelector(db *sql.DB, postgres bool, secret string, now func() time.Time) (*chainAccountsSelector, error) {
	if db == nil {
		return nil, fmt.Errorf("网关链账户选择器需要业务数据库")
	}
	if now == nil {
		now = time.Now
	}
	return &chainAccountsSelector{db: db, postgres: postgres, secret: secret, now: now}, nil
}

func (s *chainAccountsSelector) table(name string) string {
	if s.postgres {
		return "juhe_business." + name
	}
	return name
}

func (s *chainAccountsSelector) bind(query string) string {
	if !s.postgres {
		return query
	}
	var out strings.Builder
	index := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			out.WriteString("$" + fmt.Sprint(index))
			index++
		} else {
			out.WriteByte(query[i])
		}
	}
	return out.String()
}

// chainCandidateRow is the group_accounts + accounts join row the selector
// hydrates (the OpenAIGroupAccountSelectionRow subset this slice consumes).
type chainCandidateRow struct {
	AccountID              string
	BindingSystemAccountID string
	LocalPriority          sql.NullInt64
	LocalSuperPriority     sql.NullInt64
	LocalFallback          sql.NullInt64

	ID                        string
	SystemAccountID           string
	ProviderCode              string
	ProviderProtocolProfileID string
	ProtocolCode              string
	ProtocolVersion           string
	Name                      string
	Type                      string
	Status                    string
	Schedulable               int
	ConcurrencyLimit          int
	Priority                  int
	SuperPriorityEnabled      int
	FallbackEnabled           int
	ClientCompatibility       string
	ConfigRevision            sql.NullInt64
	DispatchRevision          sql.NullInt64
	CredentialsEncrypted      string
	CooldownUntil             sql.NullString
	AccountExpiresAt          sql.NullString
	ResourceCredentials       sql.NullString
	ResourceAccountID         sql.NullString
}

const chainCandidateColumns = `group_accounts.account_id,
		group_accounts.system_account_id AS binding_system_account_id,
		group_accounts.local_priority, group_accounts.local_super_priority_enabled, group_accounts.local_fallback_enabled,
		accounts.id, accounts.system_account_id, accounts.provider_code, accounts.provider_protocol_profile_id,
		accounts.protocol_code, accounts.protocol_version, accounts.name, accounts.type, accounts.status,
		accounts.schedulable, accounts.concurrency_limit, accounts.priority, accounts.super_priority_enabled,
		accounts.fallback_enabled, accounts.client_compatibility,
		accounts.config_revision, accounts.dispatch_revision, accounts.credentials_encrypted,
		accounts.cooldown_until, accounts.account_expires_at,
		source_accounts.credentials_encrypted AS resource_credentials_encrypted,
		source_accounts.id AS resource_account_id`

// chainCandidateFrom unlocks the index-free variant of the Node candidate
// window (the INDEXED BY hint is SQLite-specific tuning and omitted here).
const chainCandidateFrom = `FROM %s group_accounts
		INNER JOIN %s accounts ON accounts.id = group_accounts.account_id
		LEFT JOIN %s source_accounts ON source_accounts.id = accounts.authorization_instance_source_account_id`

// ListOpenAIAccountsForGroupResult mirrors listOpenAIAccountsForGroupResult
// over the compact candidate window.
func (s *chainAccountsSelector) ListOpenAIAccountsForGroupResult(ctx context.Context, groupID, systemAccountID string, opts gatewayruntimecache.OpenAIAccountsForGroupOptions) (gatewayruntimecache.OpenAIAccountsForGroupResult, error) {
	groupAccess := opts.PreResolvedGroupAccess
	if groupAccess == nil {
		meta, err := s.resolveGroupAccess(ctx, groupID, systemAccountID)
		if err != nil {
			return gatewayruntimecache.OpenAIAccountsForGroupResult{}, err
		}
		if meta == nil {
			return gatewayruntimecache.OpenAIAccountsForGroupResult{Accounts: []gatewayruntimecache.OpenAIAccountSecret{}}, nil
		}
		groupAccess = meta
	}
	rows, err := s.listCandidateRows(ctx, groupID, systemAccountID, groupAccess, opts.IncludeUnavailable)
	if err != nil {
		return gatewayruntimecache.OpenAIAccountsForGroupResult{}, err
	}
	if len(opts.RequestedModel) > 0 && len(rows) > 1 {
		rows = s.preferModelMatches(rows, opts.RequestedModel)
	}
	accountsOut := make([]gatewayruntimecache.OpenAIAccountSecret, 0, len(rows))
	for _, row := range rows {
		if len(accountsOut) >= dispatchCandidateFinalLimit {
			break
		}
		account, err := s.hydrateAccount(ctx, row, groupAccess, systemAccountID)
		if err != nil {
			return gatewayruntimecache.OpenAIAccountsForGroupResult{}, err
		}
		if account != nil {
			accountsOut = append(accountsOut, *account)
		}
	}
	return gatewayruntimecache.OpenAIAccountsForGroupResult{
		Accounts: accountsOut,
		Diagnostics: &gatewayruntimecache.OpenAIAccountsForGroupDiagnostics{
			FinalLimit:           dispatchCandidateFinalLimit,
			CandidateRowCount:    len(rows),
			ScannedRowCount:      len(rows),
			EligibleRowCount:     len(rows),
			HydratedAccountCount: len(accountsOut),
			FinalAccountCount:    len(accountsOut),
			ScanLimitReached:     len(rows) >= dispatchCandidateScanLimit,
		},
	}, nil
}

// listCandidateRows ports listGatewayDispatchCandidateRows (compact).
func (s *chainAccountsSelector) listCandidateRows(ctx context.Context, groupID, systemAccountID string, groupAccess *gatewayruntimecache.GroupUsageAccessMetadata, includeUnavailable bool) ([]chainCandidateRow, error) {
	nowISO := s.now().UTC().Format("2006-01-02T15:04:05.000") + "Z"
	statusSet := "'active'"
	if includeUnavailable {
		statusSet = "'active', 'rate_limited', 'temporary_unavailable'"
	}
	includeFlag := 0
	if includeUnavailable {
		includeFlag = 1
	}
	query := fmt.Sprintf(`SELECT `+chainCandidateColumns+` `+chainCandidateFrom+`
		WHERE group_accounts.group_id = ?
			AND group_accounts.system_account_id = ?
			AND group_accounts.enabled = 1
			AND accounts.provider_code = ?
			AND accounts.deleted_at IS NULL
			AND accounts.status IN (`+statusSet+`)
			AND accounts.schedulable = 1
			AND (? = 1 OR accounts.cooldown_until IS NULL OR accounts.cooldown_until <= ?)
			AND (accounts.account_expires_at IS NULL OR accounts.account_expires_at > ?)
			AND (
				(accounts.authorization_instance_authorization_id IS NULL AND accounts.type IN ('api_key', 'oauth', 'google_oauth'))
				OR (
					accounts.authorization_instance_authorization_id IS NOT NULL
					AND source_accounts.deleted_at IS NULL
					AND source_accounts.provider_code = ?
					AND source_accounts.type IN ('api_key', 'oauth', 'google_oauth')
					AND source_accounts.status IN (`+statusSet+`)
					AND source_accounts.schedulable = 1
					AND (source_accounts.account_expires_at IS NULL OR source_accounts.account_expires_at > ?)
				)
			)
			AND (
				accounts.system_account_id = group_accounts.system_account_id
				OR EXISTS (
					SELECT 1 FROM `+s.table("resource_authorizations")+` account_authorization
					WHERE account_authorization.resource_type = 'account'
						AND account_authorization.resource_id = accounts.id
						AND account_authorization.grantee_system_account_id = group_accounts.system_account_id
						AND account_authorization.status = 'active'
						AND (account_authorization.expires_at IS NULL OR account_authorization.expires_at > ?)
				)
			)
		ORDER BY
			group_accounts.local_fallback_enabled ASC,
			group_accounts.local_super_priority_enabled DESC,
			group_accounts.local_priority ASC,
			group_accounts.created_at ASC,
			group_accounts.account_id ASC
		LIMIT ?`,
		s.table("group_accounts"), s.table("accounts"), s.table("accounts"))
	rows, err := s.db.QueryContext(ctx, s.bind(query), groupID, groupAccess.GroupOwnerSystemAccountID, groupAccess.ProviderCode,
		includeFlag, nowISO, nowISO, groupAccess.ProviderCode, nowISO, nowISO, dispatchCandidateScanLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []chainCandidateRow{}
	for rows.Next() {
		var row chainCandidateRow
		var providerProfileID, protocolCode, protocolVersion, clientCompatibility sql.NullString
		if err := rows.Scan(&row.AccountID, &row.BindingSystemAccountID,
			&row.LocalPriority, &row.LocalSuperPriority, &row.LocalFallback,
			&row.ID, &row.SystemAccountID, &row.ProviderCode, &providerProfileID,
			&protocolCode, &protocolVersion, &row.Name, &row.Type, &row.Status,
			&row.Schedulable, &row.ConcurrencyLimit, &row.Priority, &row.SuperPriorityEnabled,
			&row.FallbackEnabled, &clientCompatibility,
			&row.ConfigRevision, &row.DispatchRevision, &row.CredentialsEncrypted,
			&row.CooldownUntil, &row.AccountExpiresAt,
			&row.ResourceCredentials, &row.ResourceAccountID); err != nil {
			return nil, err
		}
		row.ProviderProtocolProfileID = providerProfileID.String
		row.ProtocolCode = protocolCode.String
		row.ProtocolVersion = protocolVersion.String
		row.ClientCompatibility = clientCompatibility.String
		out = append(out, row)
	}
	return out, rows.Err()
}

// preferModelMatches mirrors the model-candidate rank: accounts whose
// supported models or mapping sources contain the requested model order
// first (the Node model-candidate window merged ahead of the base window).
func (s *chainAccountsSelector) preferModelMatches(rows []chainCandidateRow, requestedModel string) []chainCandidateRow {
	target := strings.TrimSpace(requestedModel)
	ranked := make([]chainCandidateRow, 0, len(rows))
	rest := make([]chainCandidateRow, 0, len(rows))
	for _, row := range rows {
		models, err := s.loadSupportedModels(context.Background(), s.resourceIDOf(row))
		if err == nil {
			matched := false
			for _, model := range models {
				if strings.TrimSpace(model) == target {
					matched = true
					break
				}
			}
			if matched {
				ranked = append(ranked, row)
				continue
			}
		}
		rest = append(rest, row)
	}
	return append(ranked, rest...)
}

func (s *chainAccountsSelector) resourceIDOf(row chainCandidateRow) string {
	if row.ResourceAccountID.Valid && row.ResourceAccountID.String != "" {
		return row.ResourceAccountID.String
	}
	return row.ID
}

// resolveGroupAccess re-reads the group row for the selector scope (the Node
// resolveGroupUsageAccessMetadata subset the candidate window needs: owner +
// provider code).
func (s *chainAccountsSelector) resolveGroupAccess(ctx context.Context, groupID, systemAccountID string) (*gatewayruntimecache.GroupUsageAccessMetadata, error) {
	var ownerID, providerCode string
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT system_account_id, provider_code FROM `+s.table("groups")+` WHERE id = ?`), groupID).
		Scan(&ownerID, &providerCode)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &gatewayruntimecache.GroupUsageAccessMetadata{
		GroupOwnerSystemAccountID: ownerID,
		ProviderCode:              providerCode,
	}, nil
}

// hydrateAccount ports openAIAccountSecretFromRow over the compact row.
func (s *chainAccountsSelector) hydrateAccount(ctx context.Context, row chainCandidateRow, groupAccess *gatewayruntimecache.GroupUsageAccessMetadata, systemAccountID string) (*gatewayruntimecache.OpenAIAccountSecret, error) {
	credentials := map[string]any{}
	credentialSource := row.CredentialsEncrypted
	if row.ResourceCredentials.Valid && row.ResourceCredentials.String != "" {
		credentialSource = row.ResourceCredentials.String
	}
	if credentialSource != "" {
		if err := accounts.DecryptJSON(s.secret, credentialSource, &credentials); err != nil {
			return nil, fmt.Errorf("解密账户 %s 凭据失败: %w", row.ID, err)
		}
	}
	account := &gatewayruntimecache.OpenAIAccountSecret{
		ID:                          row.ID,
		ConfigRevision:              nullInt64Ptr(row.ConfigRevision),
		DispatchRevision:            nullInt64Ptr(row.DispatchRevision),
		ProviderCode:                row.ProviderCode,
		ProviderProtocolProfileID:   row.ProviderProtocolProfileID,
		ProtocolCode:                row.ProtocolCode,
		ProtocolVersion:             row.ProtocolVersion,
		SystemAccountID:             row.SystemAccountID,
		AccountOwnerSystemAccountID: row.SystemAccountID,
		GroupOwnerSystemAccountID:   groupAccess.GroupOwnerSystemAccountID,
		AccountAccessType:           accountAccessTypeOf(row.Type),
		GroupAccessType:             groupAccess.GroupAccessType,
		BindingSystemAccountID:      &row.BindingSystemAccountID,
		BoundGroupID:                nil,
		Name:                        row.Name,
		Type:                        row.Type,
		Status:                      row.Status,
		ConcurrencyLimit:            row.ConcurrencyLimit,
		Priority:                    row.Priority,
		SuperPriorityEnabled:        row.SuperPriorityEnabled == 1,
		FallbackEnabled:             row.FallbackEnabled == 1,
		ClientCompatibility:         row.ClientCompatibility,
		BaseURL:                     textCredential(credentials, "base_url"),
		Credentials:                 runtimeCredentialsOf(credentials),
		SupportedModels:             []string{},
		ModelMappings:               []gatewayruntimecache.AccountModelMapping{},
	}
	if groupAccess.GroupAuthorizationID != nil {
		id := *groupAccess.GroupAuthorizationID
		account.GroupAuthorizationID = &id
	}
	if row.LocalPriority.Valid {
		priority := int(row.LocalPriority.Int64)
		account.Priority = priority
	}
	resourceID := s.resourceIDOf(row)
	models, err := s.loadSupportedModels(ctx, resourceID)
	if err != nil {
		return nil, err
	}
	account.SupportedModels = models
	mappings, err := s.loadModelMappings(ctx, resourceID)
	if err != nil {
		return nil, err
	}
	account.ModelMappings = mappings
	keys := apiKeyListOf(credentials)
	account.APIKeys = keys
	if len(keys) > 0 {
		account.APIKey = keys[0]
	} else {
		account.APIKey = textCredential(credentials, "api_key")
	}
	_ = systemAccountID
	return account, nil
}

func (s *chainAccountsSelector) loadSupportedModels(ctx context.Context, accountID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT model FROM `+s.table("account_supported_models")+` WHERE account_id = ? ORDER BY created_at ASC, model ASC`), accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var model string
		if err := rows.Scan(&model); err != nil {
			return nil, err
		}
		out = append(out, model)
	}
	return out, rows.Err()
}

func (s *chainAccountsSelector) loadModelMappings(ctx context.Context, accountID string) ([]gatewayruntimecache.AccountModelMapping, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT source_model, source_endpoint_family, upstream_model, upstream_endpoint_family, enabled
		FROM `+s.table("account_model_mappings")+` WHERE account_id = ? ORDER BY created_at ASC, id ASC`), accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []gatewayruntimecache.AccountModelMapping{}
	for rows.Next() {
		var mapping gatewayruntimecache.AccountModelMapping
		var enabled int
		var sourceFamily, upstreamFamily sql.NullString
		if err := rows.Scan(&mapping.SourceModel, &sourceFamily, &mapping.UpstreamModel, &upstreamFamily, &enabled); err != nil {
			return nil, err
		}
		mapping.SourceEndpointFamily = sourceFamily.String
		mapping.UpstreamEndpointFamily = upstreamFamily.String
		mapping.Enabled = enabled == 1
		out = append(out, mapping)
	}
	return out, rows.Err()
}

// accountAccessTypeOf mirrors the Node access type projection for the
// supported account types.
func accountAccessTypeOf(accountType string) string {
	switch accountType {
	case "oauth", "google_oauth":
		return "oauth"
	default:
		return "api_key"
	}
}

// runtimeCredentialsOf mirrors runtimeOpenAIAccountCredentials: only the
// runtime-relevant credential keys ride on the dispatch secret.
func runtimeCredentialsOf(credentials map[string]any) map[string]any {
	out := map[string]any{}
	for _, key := range []string{
		"access_token", "refresh_token", "expires_at", "client_id", "client_secret",
		"quota_project_id", "oauth_type", "project_id", "tier_id", "token_type", "scope",
		"account_id", "api_key_strategy", "service_tier_override", "reasoning_effort_override",
		"supported_endpoint_modes", "api_key_weights", "error_handling_rules",
		"response_inspection_rules", "quota_recovery_policy",
	} {
		if value, ok := credentials[key]; ok {
			out[key] = value
		}
	}
	return out
}

func textCredential(credentials map[string]any, key string) string {
	if value, ok := credentials[key].(string); ok {
		return value
	}
	return ""
}

// apiKeyListOf extracts the api_key entries list (Node accountApiKeysOf
// subset: the plural rotation list degrades to the single credential).
func apiKeyListOf(credentials map[string]any) []string {
	if raw, ok := credentials["api_keys"]; ok {
		switch typed := raw.(type) {
		case []any:
			out := make([]string, 0, len(typed))
			for _, item := range typed {
				if text, ok := item.(string); ok && text != "" {
					out = append(out, text)
				}
			}
			return out
		case []string:
			return typed
		}
	}
	return nil
}

func nullInt64Ptr(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	out := value.Int64
	return &out
}

// ---------------------------------------------------------------------------
// provider model catalog source (gatewayruntimecache.CatalogSource)
// ---------------------------------------------------------------------------

// chainCatalogSource implements gatewayruntimecache.CatalogSource over the
// provider_model_catalog table (Node provider-model-catalog.repository.ts
// listBuiltInProviderModels + listProviderAccountModels). The row mapping
// round-trips the Node camelCase projection through the shared item JSON
// shape so the cache and downstream consumers see identical payloads.
type chainCatalogSource struct {
	db       *sql.DB
	postgres bool
}

func newChainCatalogSource(db *sql.DB, postgres bool) (*chainCatalogSource, error) {
	if db == nil {
		return nil, fmt.Errorf("网关链模型目录源需要业务数据库")
	}
	return &chainCatalogSource{db: db, postgres: postgres}, nil
}

// chainCatalogColumns pairs the table column with the item JSON key the Node
// fromRow projection emits.
var chainCatalogColumns = [][2]string{
	{"id", "id"},
	{"scope", "scope"},
	{"status", "status"},
	{"provider_code", "providerCode"},
	{"model", "model"},
	{"mode", "mode"},
	{"catalog_order", "catalogOrder"},
	{"release_date", "releaseDate"},
	{"shutdown_date", "shutdownDate"},
	{"supported_api_protocols", "supportedApiProtocols"},
	{"input_modalities", "inputModalities"},
	{"output_modalities", "outputModalities"},
	{"supported_tools", "supportedTools"},
	{"input_usd_per_1m", "inputUsdPer1M"},
	{"output_usd_per_1m", "outputUsdPer1M"},
	{"cached_input_usd_per_1m", "cachedInputUsdPer1M"},
	{"cache_write_usd_per_1m", "cacheWriteUsdPer1M"},
	{"cache_write_1h_usd_per_1m", "cacheWrite1hUsdPer1M"},
	{"cache_storage_usd_per_1m_per_hour", "cacheStorageUsdPer1MPerHour"},
	{"service_tier_prices", "serviceTierPrices"},
	{"image_input_usd_per_1m", "imageInputUsdPer1M"},
	{"image_output_usd_per_1m", "imageOutputUsdPer1M"},
	{"output_usd_per_image", "outputUsdPerImage"},
	{"context_window_tokens", "contextWindowTokens"},
	{"max_input_tokens", "maxInputTokens"},
	{"max_output_tokens", "maxOutputTokens"},
	{"long_context_input_cost_multiplier", "longContextInputCostMultiplier"},
	{"long_context_output_cost_multiplier", "longContextOutputCostMultiplier"},
	{"supports_prompt_caching", "supportsPromptCaching"},
	{"supported_service_tiers", "supportedServiceTiers"},
	{"supported_reasoning_efforts", "supportedReasoningEfforts"},
	{"default_reasoning_effort", "defaultReasoningEffort"},
	{"supports_service_tier", "supportsServiceTier"},
	{"catalog_visible", "catalogVisible"},
	{"source", "source"},
	{"system_account_id", "systemAccountId"},
	{"notes", "notes"},
}

// ListProviderModelCatalog mirrors listProviderModelCatalog: built-in catalog
// rows for the provider plus the account-scoped rows for the system account.
func (s *chainCatalogSource) ListProviderModelCatalog(ctx context.Context, input gatewayruntimecache.ModelCatalogListOptions) ([]gatewayruntimecache.ProviderModelCatalogItem, error) {
	availability := ""
	if !input.IncludeInactive {
		availability = " AND status = 'active' AND catalog_visible = 1 AND (shutdown_date IS NULL OR trim(shutdown_date) = '' OR shutdown_date > ?) "
	}
	columns := make([]string, 0, len(chainCatalogColumns))
	for _, pair := range chainCatalogColumns {
		columns = append(columns, pair[0])
	}
	base := fmt.Sprintf(`SELECT %s FROM %s`, strings.Join(columns, ", "), s.table("provider_model_catalog"))
	now := s.now().UTC().Format("2006-01-02")
	builtInQuery := base + " WHERE provider_code = ? " + availability + " ORDER BY catalog_order, model, id"
	builtInArgs := []any{input.ProviderCode}
	if availability != "" {
		builtInArgs = append(builtInArgs, now)
	}
	items := []gatewayruntimecache.ProviderModelCatalogItem{}
	rows, err := s.db.QueryContext(ctx, s.bind(builtInQuery), builtInArgs...)
	if err != nil {
		return nil, err
	}
	scanned, err := scanCatalogRows(rows, len(chainCatalogColumns))
	if err != nil {
		return nil, err
	}
	items = append(items, scanned...)
	accountQuery := base + " WHERE provider_code = ? AND system_account_id = ? " + availability + " ORDER BY catalog_order, model, id"
	accountArgs := []any{input.ProviderCode, input.SystemAccountID}
	if availability != "" {
		accountArgs = append(accountArgs, now)
	}
	rows, err = s.db.QueryContext(ctx, s.bind(accountQuery), accountArgs...)
	if err != nil {
		return nil, err
	}
	scanned, err = scanCatalogRows(rows, len(chainCatalogColumns))
	if err != nil {
		return nil, err
	}
	items = append(items, scanned...)
	return items, nil
}

// catalogBoolColumns are the boolean table columns the SQLite driver surfaces
// as integers (0/1) and the item JSON shape expects as bools.
var catalogBoolColumns = map[string]bool{
	"supportsPromptCaching": true,
	"supportsServiceTier":   true,
	"catalogVisible":        true,
}

// scanCatalogRows converts one result set into the shared catalog items: each
// row scans into a generic slice keyed by the camelCase projection and then
// decodes through the item JSON shape (the Go item tags mirror the Node row
// payload byte for byte).
func scanCatalogRows(rows *sql.Rows, columnCount int) ([]gatewayruntimecache.ProviderModelCatalogItem, error) {
	defer rows.Close()
	items := []gatewayruntimecache.ProviderModelCatalogItem{}
	for rows.Next() {
		values := make([]any, columnCount)
		pointers := make([]any, columnCount)
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return nil, err
		}
		row := map[string]any{}
		for index, pair := range chainCatalogColumns {
			value := normalizeCatalogValue(values[index])
			if catalogBoolColumns[pair[1]] {
				value = catalogBoolValue(value)
			}
			row[pair[1]] = value
		}
		encoded, err := json.Marshal(row)
		if err != nil {
			return nil, err
		}
		var item gatewayruntimecache.ProviderModelCatalogItem
		if err := json.Unmarshal(encoded, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// catalogBoolValue renders 0/1 integers (SQLite ints decode as float64) into
// JSON bools; NULL stays absent.
func catalogBoolValue(value any) any {
	switch typed := value.(type) {
	case float64:
		return typed != 0
	case int64:
		return typed != 0
	case int:
		return typed != 0
	case nil:
		return false
	default:
		return value
	}
}

// normalizeCatalogValue renders SQL values into the JSON shapes the item
// decode expects (JSON text columns decode through a second pass; numerics
// arrive as float64 from SQLite; booleans arrive as int).
func normalizeCatalogValue(value any) any {
	switch typed := value.(type) {
	case []byte:
		text := string(typed)
		var decoded any
		if len(text) > 0 && (text[0] == '[' || text[0] == '{') {
			if err := json.Unmarshal([]byte(text), &decoded); err == nil {
				return decoded
			}
		}
		return text
	case string:
		var decoded any
		if len(typed) > 0 && (typed[0] == '[' || typed[0] == '{') {
			if err := json.Unmarshal([]byte(typed), &decoded); err == nil {
				return decoded
			}
		}
		return typed
	default:
		return typed
	}
}

func (s *chainCatalogSource) table(name string) string {
	if s.postgres {
		return "juhe_business." + name
	}
	return name
}

func (s *chainCatalogSource) bind(query string) string {
	if !s.postgres {
		return query
	}
	var out strings.Builder
	index := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			out.WriteString("$" + fmt.Sprint(index))
			index++
		} else {
			out.WriteByte(query[i])
		}
	}
	return out.String()
}

func (s *chainCatalogSource) now() time.Time { return time.Now() }

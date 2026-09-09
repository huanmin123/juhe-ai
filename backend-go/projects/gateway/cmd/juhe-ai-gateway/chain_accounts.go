package main

// G20 phase-3 composition-root account read seam: the full
// gatewayruntimecache.AccountsSelector implementation the runtime cache needs
// to hydrate dispatchable OpenAI accounts for a group.
//
// Node authority (backend/src/storage):
//   - openai-account-selector.repository.ts (listOpenAIAccountsForGroupResult,
//     resolveGroupUsageAccessMetadata, resolveOpenAIAccountAccess,
//     canScheduleAuthorizedAccount, openAIAccountSecretFromRow,
//     runtimeOpenAIAccountCredentials),
//   - gateway-dispatch-candidate-window.repository.ts
//     (listGatewayDispatchCandidateRows + listGatewayDispatchModelCandidateRows
//     + orderGatewayDispatchCandidateRowsForDispatch + the fresh quality rows),
//   - account-api-key-rotation.ts (accountApiKeyEntries +
//     isAccountApiKeyPoolIsolationEnabled + HMAC fingerprints),
//   - account-api-key-runtime-state.repository.ts
//     (loadAccountApiKeyRuntimeStatesByAccountIds),
//   - proxy.repository.ts (resolveProxyUrlsForProfiles + proxyUrlFromRow),
//   - resource-authorization-helpers.ts / request-quota-limits.ts
//     (resourceAuthorizationQuotaLimited),
//   - openai-endpoint-modes.ts + provider-protocol.ts (runtime endpoint modes).
//
// Every field of the Node OpenAIAccountSecret projection is hydrated; nothing
// degrades silently (a missing stats quality table or an undecryptable proxy
// credential surfaces as the Node error / `unavailable` contract).

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// dispatchCandidateScanLimitFor mirrors
// gatewayDispatchAccountCandidateScanLimit = limit * 2
// (openai-account-selector.types.ts:208). The final limit itself is
// configuration: Node dispatchAccountCandidateLimit =
// integerConfig('JUHE_AI_GATEWAY_DISPATCH_ACCOUNT_CANDIDATE_LIMIT',
// concurrency.globalMax, 1, 50_000) (runtime.ts:770) — the candidate windows
// (LIMIT ?), the hydration batching and the diagnostics all consume the
// runtime value instead of a fixed constant.
func dispatchCandidateScanLimitFor(finalLimit int) int { return finalLimit * 2 }

// chainAccountsSelector implements gatewayruntimecache.AccountsSelector over
// the business + stats databases.
type chainAccountsSelector struct {
	db       *sql.DB
	statsDB  *sql.DB
	postgres bool
	secret   string
	now      func() time.Time
	// candidateFinalLimit mirrors gatewayDispatchAccountCandidateLimit
	// (runtimeConfig.gateway.dispatchAccountCandidateLimit, default
	// concurrency.globalMax); candidateScanLimit = finalLimit * 2.
	candidateFinalLimit int
	candidateScanLimit  int
}

// newChainAccountsSelector keeps the phase-2 constructor signature; quality
// reads fall back to the business handle when no separate stats handle is
// supplied (the dual-handle constructor is newChainAccountsSelectorWithStats).
// dispatchAccountCandidateLimit comes from the runtime config (Node default
// concurrency.globalMax, bounded 1..50000); out-of-range values fail the
// constructor like the Node integerConfig range error.
func newChainAccountsSelector(db *sql.DB, postgres bool, secret string, now func() time.Time, dispatchAccountCandidateLimit int) (*chainAccountsSelector, error) {
	return newChainAccountsSelectorWithStats(db, db, postgres, secret, now, dispatchAccountCandidateLimit)
}

// newChainAccountsSelectorWithStats wires the stats database the fresh
// account_quality_scores reads run against (Node getStatsDatabase()).
func newChainAccountsSelectorWithStats(db *sql.DB, statsDB *sql.DB, postgres bool, secret string, now func() time.Time, dispatchAccountCandidateLimit int) (*chainAccountsSelector, error) {
	if db == nil {
		return nil, fmt.Errorf("网关链账户选择器需要业务数据库")
	}
	if dispatchAccountCandidateLimit < 1 || dispatchAccountCandidateLimit > 50000 {
		return nil, fmt.Errorf("JUHE_AI_GATEWAY_DISPATCH_ACCOUNT_CANDIDATE_LIMIT 必须在 1-50000 范围内: %d", dispatchAccountCandidateLimit)
	}
	if statsDB == nil {
		statsDB = db
	}
	if now == nil {
		now = time.Now
	}
	return &chainAccountsSelector{
		db:                  db,
		statsDB:             statsDB,
		postgres:            postgres,
		secret:              secret,
		now:                 now,
		candidateFinalLimit: dispatchAccountCandidateLimit,
		candidateScanLimit:  dispatchCandidateScanLimitFor(dispatchAccountCandidateLimit),
	}, nil
}

func (s *chainAccountsSelector) table(name string) string {
	if s.postgres {
		return "juhe_business." + name
	}
	return name
}

func (s *chainAccountsSelector) statsTable(name string) string {
	if s.postgres {
		return "juhe_stats." + name
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

// ---------------------------------------------------------------------------
// candidate row (OpenAIGroupAccountSelectionRow)
// ---------------------------------------------------------------------------

// chainCandidateRow is the group_accounts + accounts join row the selector
// hydrates (the full Node OpenAIGroupAccountSelectionRow shape).
type chainCandidateRow struct {
	AccountID              string
	BindingSystemAccountID sql.NullString
	GroupID                sql.NullString
	AccountAuthorizationID sql.NullString
	LocalPriority          sql.NullInt64
	LocalSuperPriority     sql.NullInt64
	LocalFallback          sql.NullInt64
	BindingCreatedAt       sql.NullString // model-candidate window only

	ID                                       string
	SystemAccountID                          string
	ProviderCode                             string
	ProviderProtocolProfileID                sql.NullString
	ProtocolCode                             sql.NullString
	ProtocolVersion                          sql.NullString
	Name                                     string
	Type                                     string
	Status                                   string
	Schedulable                              int
	ConcurrencyLimit                         int
	Priority                                 int
	SuperPriorityEnabled                     int
	FallbackEnabled                          int
	ClientCompatibility                      sql.NullString
	ConfigRevision                           sql.NullInt64
	DispatchRevision                         sql.NullInt64
	CredentialsEncrypted                     sql.NullString
	ProxyProfileID                           sql.NullString
	CooldownUntil                            sql.NullString
	LastErrorMessage                         sql.NullString
	StreamFailureCount                       sql.NullInt64
	StreamFailureWindowStartedAt             sql.NullString
	AccountExpiresAt                         sql.NullString
	HealthCheckModel                         sql.NullString
	HealthCheckEndpointMode                  sql.NullString
	AuthorizationInstanceSourceAccountID     sql.NullString
	AuthorizationInstanceAuthorizationID     sql.NullString
	AuthorizationInstanceOwnerSystemAccountI sql.NullString

	ResourceAccountID                 sql.NullString
	ResourceProviderCode              sql.NullString
	ResourceProviderProtocolProfileID sql.NullString
	ResourceProtocolCode              sql.NullString
	ResourceProtocolVersion           sql.NullString
	ResourceType                      sql.NullString
	ResourceStatus                    sql.NullString
	ResourceSchedulable               sql.NullInt64
	ResourceAccountExpiresAt          sql.NullString
	ResourceCooldownUntil             sql.NullString
	ResourceLastErrorCode             sql.NullString
	ResourceCredentialsEncrypted      sql.NullString
	ResourceProxyProfileID            sql.NullString
	ResourceConcurrencyLimit          sql.NullInt64
	ResourceClientCompatibility       sql.NullString

	QualityScore        *float64
	QualityState        *string
	QualityFirstTokenMs *float64

	modelResourceID       sql.NullString // model-candidate window only
	modelResourceProvider sql.NullString
	ModelRank             int // -1 when the row did not come from a ranked window
}

const chainCandidateColumns = `group_accounts.account_id,
			group_accounts.system_account_id AS binding_system_account_id,
			group_accounts.group_id, group_accounts.account_authorization_id,
			group_accounts.local_priority, group_accounts.local_super_priority_enabled, group_accounts.local_fallback_enabled,
			accounts.id, accounts.system_account_id, accounts.provider_code, accounts.provider_protocol_profile_id,
			accounts.protocol_code, accounts.protocol_version, accounts.name, accounts.type, accounts.status,
			accounts.schedulable, accounts.concurrency_limit, accounts.priority, accounts.super_priority_enabled,
			accounts.fallback_enabled, accounts.client_compatibility,
			accounts.config_revision, accounts.dispatch_revision, accounts.credentials_encrypted,
			accounts.proxy_profile_id, accounts.cooldown_until, accounts.last_error_message,
			accounts.stream_failure_count, accounts.stream_failure_window_started_at,
			accounts.account_expires_at, accounts.health_check_model, accounts.health_check_endpoint_mode,
			accounts.authorization_instance_source_account_id, accounts.authorization_instance_authorization_id,
			accounts.authorization_instance_owner_system_account_id,
			source_accounts.id AS resource_account_id,
			source_accounts.provider_code AS resource_provider_code,
			source_accounts.provider_protocol_profile_id AS resource_provider_protocol_profile_id,
			source_accounts.protocol_code AS resource_protocol_code,
			source_accounts.protocol_version AS resource_protocol_version,
			source_accounts.type AS resource_type,
			source_accounts.status AS resource_status,
			source_accounts.schedulable AS resource_schedulable,
			source_accounts.account_expires_at AS resource_account_expires_at,
			source_accounts.cooldown_until AS resource_cooldown_until,
			source_accounts.last_error_code AS resource_last_error_code,
			source_accounts.credentials_encrypted AS resource_credentials_encrypted,
			source_accounts.proxy_profile_id AS resource_proxy_profile_id,
			source_accounts.concurrency_limit AS resource_concurrency_limit,
			source_accounts.client_compatibility AS resource_client_compatibility`

// chainCandidateModelTail extends the model-candidate window projection.
const chainCandidateModelTail = `,
			group_accounts.created_at AS binding_created_at,
			COALESCE(source_accounts.id, accounts.id) AS model_resource_account_id,
			COALESCE(source_accounts.provider_code, accounts.provider_code) AS model_resource_provider_code`

// chainCandidateWhere mirrors the shared candidate window predicates (the
// availability belt both windows repeat verbatim).
const chainCandidateWhere = `WHERE group_accounts.group_id = ?
			AND group_accounts.system_account_id = ?
			AND group_accounts.enabled = 1
			AND accounts.provider_code = ?
			AND accounts.deleted_at IS NULL
			AND accounts.status IN ({statusSet})
			AND accounts.schedulable = 1
			AND (? = 1 OR accounts.cooldown_until IS NULL OR accounts.cooldown_until <= ?)
			AND (
				(accounts.authorization_instance_authorization_id IS NULL AND accounts.type IN ('api_key', 'oauth', 'google_oauth'))
				OR (
					accounts.authorization_instance_authorization_id IS NOT NULL
					AND source_accounts.deleted_at IS NULL
					AND source_accounts.provider_code = ?
					AND source_accounts.type IN ('api_key', 'oauth', 'google_oauth')
					AND source_accounts.status IN ({statusSet})
					AND source_accounts.schedulable = 1
					AND (? = 1 OR source_accounts.cooldown_until IS NULL OR source_accounts.cooldown_until <= ?)
					AND (source_accounts.account_expires_at IS NULL OR source_accounts.account_expires_at > ?)
					AND (source_accounts.last_error_code IS NULL OR source_accounts.last_error_code <> 'account_expired')
				)
			)
			AND (accounts.account_expires_at IS NULL OR accounts.account_expires_at > ?)`

// chainCandidateFrom unlocks the index-free variant of the Node candidate
// window (the INDEXED BY hint is SQLite-specific tuning and omitted here).
const chainCandidateFrom = `FROM %s group_accounts
			INNER JOIN %s accounts ON accounts.id = group_accounts.account_id
			LEFT JOIN %s source_accounts ON source_accounts.id = accounts.authorization_instance_source_account_id`

// chainEligibleRow mirrors EligibleOpenAIGroupAccountSelection.
type chainEligibleRow struct {
	row    *chainCandidateRow
	access *chainAccountAccess
}

// ListOpenAIAccountsForGroupResult mirrors listOpenAIAccountsForGroupResult.
func (s *chainAccountsSelector) ListOpenAIAccountsForGroupResult(ctx context.Context, groupID, systemAccountID string, opts gatewayruntimecache.OpenAIAccountsForGroupOptions) (gatewayruntimecache.OpenAIAccountsForGroupResult, error) {
	now := chainNowISO(s.now)
	qualityFreshAfter := chainQualityFreshAfterISO(s.now)
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

	// Model candidate window (requested model only) + base window merged in
	// Node order (mergeOpenAIGroupAccountRowsForDispatch).
	requestedModel := strings.TrimSpace(opts.RequestedModel)
	var modelRows []chainCandidateRow
	var modelRanks map[string]int
	if requestedModel != "" {
		rows, ranks, err := s.listModelCandidateRows(ctx, groupID, groupAccess, now, opts.RequestedModel, opts.RequestedEndpointFamily, opts.IncludeUnavailable)
		if err != nil {
			return gatewayruntimecache.OpenAIAccountsForGroupResult{}, err
		}
		modelRows, modelRanks = rows, ranks
	}
	baseRows, err := s.listCandidateRows(ctx, groupID, groupAccess, now, opts.IncludeUnavailable)
	if err != nil {
		return gatewayruntimecache.OpenAIAccountsForGroupResult{}, err
	}
	groupAccountRows := baseRows
	if modelRows != nil {
		groupAccountRows = mergeChainCandidateRows(modelRows, baseRows)
	}

	// Account authorizations for owner-scope accounts
	// (loadAccountAuthorizationsForSelection: nil map for authorized groups —
	// the per-row reads then hit the database like the Node fallback).
	authorizations, err := s.loadAccountAuthorizationsForSelection(ctx, groupAccountRows, groupAccess, systemAccountID)
	if err != nil {
		return gatewayruntimecache.OpenAIAccountsForGroupResult{}, err
	}

	// Eligibility: schedulable access + availability gates + binding match.
	eligible := make([]chainEligibleRow, 0, len(groupAccountRows))
	for index := range groupAccountRows {
		row := &groupAccountRows[index]
		access, err := s.resolveChainAccountAccess(ctx, row, systemAccountID, groupAccess, row.AccountAuthorizationID.String, authorizations)
		if err != nil {
			return gatewayruntimecache.OpenAIAccountsForGroupResult{}, err
		}
		if access == nil {
			continue
		}
		if !canScheduleChainAuthorizedAccount(row, authorizations, access) {
			continue
		}
		if access.accountAccessType == chainAccountAccessAuthorized && !row.ResourceAccountID.Valid {
			continue
		}
		available, err := chainAccountAvailableForSelection(row, now, opts.IncludeUnavailable)
		if err != nil {
			return gatewayruntimecache.OpenAIAccountsForGroupResult{}, err
		}
		if !available {
			continue
		}
		if access.accountAccessType == chainAccountAccessAuthorized {
			// isOpenAIAccountAvailableForSelection binding gate.
			if !row.GroupID.Valid || row.GroupID.String == "" ||
				!row.AccountAuthorizationID.Valid || row.AccountAuthorizationID.String == "" ||
				row.AccountAuthorizationID.String != *access.accountAuthorizationID {
				continue
			}
		}
		eligible = append(eligible, chainEligibleRow{row: row, access: access})
	}

	// Fresh quality rows (stats account_quality_scores, 24h window).
	accountIDs := make([]string, 0, len(eligible))
	for _, item := range eligible {
		accountIDs = append(accountIDs, item.row.AccountID)
	}
	qualityByAccountID, err := s.loadFreshQualityRows(ctx, accountIDs, qualityFreshAfter)
	if err != nil {
		return gatewayruntimecache.OpenAIAccountsForGroupResult{}, err
	}
	for index := range eligible {
		row := eligible[index].row
		if quality, ok := qualityByAccountID[row.ID]; ok {
			row.QualityScore = quality.score
			row.QualityState = quality.state
			row.QualityFirstTokenMs = quality.firstTokenMs
		}
	}

	ordered := orderChainCandidateRowsForDispatch(eligible, modelRanks)

	// Hydration batches of gatewayDispatchAccountCandidateLimit.
	accountsOut := make([]gatewayruntimecache.OpenAIAccountSecret, 0, len(ordered))
	hydrationBatchCount := 0
	hydrationDroppedCount := 0
	for offset := 0; offset < len(ordered) && len(accountsOut) < s.candidateFinalLimit; offset += s.candidateFinalLimit {
		end := offset + s.candidateFinalLimit
		if end > len(ordered) {
			end = len(ordered)
		}
		batch := ordered[offset:end]
		hydrationBatchCount++
		resourceIDs := make([]string, 0, len(batch))
		for _, item := range batch {
			resourceIDs = append(resourceIDs, item.row.resourceAccountID())
		}
		supportedModels, err := s.loadSupportedModelsByAccountIds(ctx, resourceIDs)
		if err != nil {
			return gatewayruntimecache.OpenAIAccountsForGroupResult{}, err
		}
		modelMappings, err := s.loadModelMappingsByAccountIds(ctx, resourceIDs)
		if err != nil {
			return gatewayruntimecache.OpenAIAccountsForGroupResult{}, err
		}
		apiKeyRuntimeStates, err := s.loadAPIKeyRuntimeStatesByAccountIds(ctx, resourceIDs)
		if err != nil {
			return gatewayruntimecache.OpenAIAccountsForGroupResult{}, err
		}
		proxyProfileIDs := make([]string, 0, len(batch))
		for _, item := range batch {
			if id := item.row.resourceProxyProfileID(); id != "" {
				proxyProfileIDs = append(proxyProfileIDs, id)
			}
		}
		proxyProfiles, err := s.resolveProxyURLsForProfiles(ctx, proxyProfileIDs)
		if err != nil {
			return gatewayruntimecache.OpenAIAccountsForGroupResult{}, err
		}
		for _, item := range batch {
			account, err := s.openAIAccountSecretFromRow(ctx, item.row, groupAccess, item.access, chainSecretOptions{
				proxyProfiles:      proxyProfiles,
				supportedModels:    supportedModels,
				modelMappings:      modelMappings,
				apiKeyRuntimeState: apiKeyRuntimeStates,
			})
			if err != nil {
				return gatewayruntimecache.OpenAIAccountsForGroupResult{}, err
			}
			if account != nil {
				accountsOut = append(accountsOut, *account)
				if len(accountsOut) >= s.candidateFinalLimit {
					break
				}
			} else {
				hydrationDroppedCount++
			}
		}
	}

	return gatewayruntimecache.OpenAIAccountsForGroupResult{
		Accounts: accountsOut,
		Diagnostics: &gatewayruntimecache.OpenAIAccountsForGroupDiagnostics{
			ScanLimit:             s.candidateScanLimit,
			FinalLimit:            s.candidateFinalLimit,
			CandidateRowCount:     len(groupAccountRows),
			ScannedRowCount:       len(groupAccountRows),
			EligibleRowCount:      len(eligible),
			HydrationBatchCount:   hydrationBatchCount,
			HydratedAccountCount:  len(accountsOut),
			HydrationDroppedCount: hydrationDroppedCount,
			FinalAccountCount:     len(accountsOut),
			ScanLimitReached:      len(groupAccountRows) >= s.candidateScanLimit,
		},
	}, nil
}

// ---------------------------------------------------------------------------
// SQL windows
// ---------------------------------------------------------------------------

// candidateScanDest builds the scan destination for the base window column
// order (chainCandidateColumns).
func candidateScanDest(row *chainCandidateRow) []any {
	return []any{
		&row.AccountID, &row.BindingSystemAccountID, &row.GroupID, &row.AccountAuthorizationID,
		&row.LocalPriority, &row.LocalSuperPriority, &row.LocalFallback,
		&row.ID, &row.SystemAccountID, &row.ProviderCode, &row.ProviderProtocolProfileID,
		&row.ProtocolCode, &row.ProtocolVersion, &row.Name, &row.Type, &row.Status,
		&row.Schedulable, &row.ConcurrencyLimit, &row.Priority, &row.SuperPriorityEnabled,
		&row.FallbackEnabled, &row.ClientCompatibility,
		&row.ConfigRevision, &row.DispatchRevision, &row.CredentialsEncrypted,
		&row.ProxyProfileID, &row.CooldownUntil, &row.LastErrorMessage,
		&row.StreamFailureCount, &row.StreamFailureWindowStartedAt,
		&row.AccountExpiresAt, &row.HealthCheckModel, &row.HealthCheckEndpointMode,
		&row.AuthorizationInstanceSourceAccountID, &row.AuthorizationInstanceAuthorizationID,
		&row.AuthorizationInstanceOwnerSystemAccountI,
		&row.ResourceAccountID,
		&row.ResourceProviderCode,
		&row.ResourceProviderProtocolProfileID,
		&row.ResourceProtocolCode,
		&row.ResourceProtocolVersion,
		&row.ResourceType,
		&row.ResourceStatus,
		&row.ResourceSchedulable,
		&row.ResourceAccountExpiresAt,
		&row.ResourceCooldownUntil,
		&row.ResourceLastErrorCode,
		&row.ResourceCredentialsEncrypted,
		&row.ResourceProxyProfileID,
		&row.ResourceConcurrencyLimit,
		&row.ResourceClientCompatibility,
	}
}

// listCandidateRows ports listGatewayDispatchCandidateRows.
func (s *chainAccountsSelector) listCandidateRows(ctx context.Context, groupID string, groupAccess *gatewayruntimecache.GroupUsageAccessMetadata, now string, includeUnavailable bool) ([]chainCandidateRow, error) {
	statusSet := "'active'"
	if includeUnavailable {
		statusSet = "'active', 'rate_limited', 'temporary_unavailable'"
	}
	includeFlag := 0
	if includeUnavailable {
		includeFlag = 1
	}
	where := strings.NewReplacer("{statusSet}", statusSet).Replace(chainCandidateWhere)
	query := strings.NewReplacer(
		"{columns}", chainCandidateColumns,
		"{from}", fmt.Sprintf(chainCandidateFrom, s.table("group_accounts"), s.table("accounts"), s.table("accounts")),
		"{where}", where,
	).Replace(`SELECT {columns},
			NULL AS quality_score, NULL AS quality_state, NULL AS quality_ewma_first_token_ms
			{from} {where}
			ORDER BY
				group_accounts.local_fallback_enabled ASC,
				group_accounts.local_super_priority_enabled DESC,
				group_accounts.local_priority ASC,
				group_accounts.created_at ASC,
				group_accounts.account_id ASC
			LIMIT ?`)
	rows, err := s.db.QueryContext(ctx, s.bind(query), groupID, groupAccess.GroupOwnerSystemAccountID, groupAccess.ProviderCode,
		includeFlag, now, groupAccess.ProviderCode, includeFlag, now, now, now, s.candidateScanLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []chainCandidateRow{}
	for rows.Next() {
		var row chainCandidateRow
		dest := candidateScanDest(&row)
		dest = append(dest, &nullStringSink{}, &nullStringSink{}, &nullStringSink{})
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		row.ModelRank = -1
		out = append(out, row)
	}
	return out, rows.Err()
}

// listModelCandidateRows ports listGatewayDispatchModelCandidateRows: the
// CTE model-rank window (0 = direct supported model, 1 = enabled mapping,
// 2 = no supported-model list, everything else dropped).
func (s *chainAccountsSelector) listModelCandidateRows(ctx context.Context, groupID string, groupAccess *gatewayruntimecache.GroupUsageAccessMetadata, now, requestedModel, requestedEndpointFamily string, includeUnavailable bool) ([]chainCandidateRow, map[string]int, error) {
	model := strings.TrimSpace(requestedModel)
	if model == "" {
		return []chainCandidateRow{}, map[string]int{}, nil
	}
	statusSet := "'active'"
	if includeUnavailable {
		statusSet = "'active', 'rate_limited', 'temporary_unavailable'"
	}
	includeFlag := 0
	if includeUnavailable {
		includeFlag = 1
	}
	where := strings.NewReplacer("{statusSet}", statusSet).Replace(chainCandidateWhere)
	tables := fmt.Sprintf(chainCandidateFrom, s.table("group_accounts"), s.table("accounts"), s.table("accounts"))
	supportedModelsTable := s.table("account_supported_models")
	mappingsTable := s.table("account_model_mappings")
	query := strings.NewReplacer(
		"{columns}", chainCandidateColumns,
		"{tail}", chainCandidateModelTail,
		"{tables}", tables,
		"{where}", where,
		"{supportedModels}", supportedModelsTable,
		"{mappings}", mappingsTable,
	).Replace(`WITH eligible_rows AS (
		SELECT {columns}{tail}
		{tables} {where}
		),
		ranked_rows AS (
			SELECT
				CASE
					WHEN EXISTS (
						SELECT 1 FROM {supportedModels} direct_models
						WHERE direct_models.account_id = eligible_rows.model_resource_account_id
							AND direct_models.provider_code = eligible_rows.model_resource_provider_code
							AND direct_models.model = ?
					) THEN 0
					WHEN EXISTS (
						SELECT 1 FROM {mappings} model_mappings
						WHERE model_mappings.account_id = eligible_rows.model_resource_account_id
							AND model_mappings.provider_code = eligible_rows.model_resource_provider_code
							AND model_mappings.source_model = ?
							AND model_mappings.source_endpoint_family = ?
							AND model_mappings.enabled = 1
							AND (
								model_mappings.upstream_model <> model_mappings.source_model
								OR model_mappings.upstream_endpoint_family <> model_mappings.source_endpoint_family
							)
							AND (
								NOT EXISTS (
									SELECT 1 FROM {supportedModels} limited_supported
									WHERE limited_supported.account_id = eligible_rows.model_resource_account_id
								)
								OR EXISTS (
									SELECT 1 FROM {supportedModels} mapped_supported
									WHERE mapped_supported.account_id = eligible_rows.model_resource_account_id
										AND mapped_supported.provider_code = eligible_rows.model_resource_provider_code
										AND mapped_supported.model = model_mappings.upstream_model
								)
							)
					) THEN 1
					WHEN NOT EXISTS (
						SELECT 1 FROM {supportedModels} limited_models
						WHERE limited_models.account_id = eligible_rows.model_resource_account_id
					) THEN 2
					ELSE 3
				END AS model_rank,
				eligible_rows.*
			FROM eligible_rows
		)
		SELECT * FROM ranked_rows
		WHERE model_rank < 3
		ORDER BY
			model_rank ASC,
			local_fallback_enabled ASC,
			local_super_priority_enabled DESC,
			COALESCE(local_priority, priority, 0) ASC,
			binding_created_at ASC,
			account_id ASC
		LIMIT ?`)
	rows, err := s.db.QueryContext(ctx, s.bind(query), groupID, groupAccess.GroupOwnerSystemAccountID, groupAccess.ProviderCode,
		includeFlag, now, groupAccess.ProviderCode, includeFlag, now, now, now,
		model, model, requestedEndpointFamily, s.candidateScanLimit)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	out := []chainCandidateRow{}
	for rows.Next() {
		var row chainCandidateRow
		var modelRank int
		dest := []any{&modelRank}
		dest = append(dest, candidateScanDest(&row)...)
		dest = append(dest, &row.BindingCreatedAt, &row.modelResourceID, &row.modelResourceProvider)
		if err := rows.Scan(dest...); err != nil {
			return nil, nil, err
		}
		row.ModelRank = modelRank
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	modelRankByAccountID := map[string]int{}
	unique := make([]chainCandidateRow, 0, len(out))
	seen := map[string]bool{}
	for _, row := range out {
		accountID := row.AccountID
		if accountID == "" {
			accountID = row.ID
		}
		if seen[accountID] {
			continue
		}
		seen[accountID] = true
		modelRankByAccountID[row.ID] = row.ModelRank
		modelRankByAccountID[accountID] = row.ModelRank
		unique = append(unique, row)
	}
	return unique, modelRankByAccountID, nil
}

// mergeChainCandidateRows mirrors mergeOpenAIGroupAccountRowsForDispatch:
// model-window rows keep their order, base-window duplicates are dropped.
func mergeChainCandidateRows(preferred, fallback []chainCandidateRow) []chainCandidateRow {
	seen := map[string]bool{}
	rows := make([]chainCandidateRow, 0, len(preferred)+len(fallback))
	for _, row := range preferred {
		accountID := row.AccountID
		if accountID == "" {
			accountID = row.ID
		}
		if seen[accountID] {
			continue
		}
		seen[accountID] = true
		rows = append(rows, row)
	}
	for _, row := range fallback {
		accountID := row.AccountID
		if accountID == "" {
			accountID = row.ID
		}
		if seen[accountID] {
			continue
		}
		seen[accountID] = true
		rows = append(rows, row)
	}
	return rows
}

// ---------------------------------------------------------------------------
// dispatch ordering (orderGatewayDispatchCandidateRowsForDispatch)
// ---------------------------------------------------------------------------

// orderChainCandidateRowsForDispatch orders the eligible rows: model rank,
// fallback, super, priority, then the quality tie-break inside buckets with
// >=2 members and the zh-CN name/id tail.
func orderChainCandidateRowsForDispatch(rows []chainEligibleRow, modelRanks map[string]int) []chainEligibleRow {
	out := append([]chainEligibleRow{}, rows...)
	collator := chainNameCollator()
	sort.SliceStable(out, func(left, right int) bool {
		l, r := out[left].row, out[right].row
		if lr, rr := chainModelRank(l, modelRanks), chainModelRank(r, modelRanks); lr != rr {
			return lr < rr
		}
		if lf, rf := chainFallbackRank(l), chainFallbackRank(r); lf != rf {
			return lf < rf
		}
		if ls, rs := chainSuperRank(l), chainSuperRank(r); ls != rs {
			return ls > rs
		}
		if lp, rp := chainPriorityRank(l), chainPriorityRank(r); lp != rp {
			return lp < rp
		}
		if chainBucketKey(l) == chainBucketKey(r) && chainBucketSize(out, chainBucketKey(l)) >= 2 {
			if delta := compareChainQuality(l, r, collator); delta != 0 {
				return delta < 0
			}
		}
		if delta := collator.CompareString(l.Name, r.Name); delta != 0 {
			return delta < 0
		}
		return l.ID < r.ID
	})
	return out
}

func chainModelRank(row *chainCandidateRow, modelRanks map[string]int) int {
	if modelRanks == nil {
		return 0
	}
	if rank, ok := modelRanks[row.ID]; ok {
		return rank
	}
	if rank, ok := modelRanks[row.AccountID]; ok {
		return rank
	}
	return 3
}

func chainFallbackRank(row *chainCandidateRow) int {
	return boolToInt(row.LocalFallback.Valid && row.LocalFallback.Int64 == 1)
}

func chainSuperRank(row *chainCandidateRow) int {
	return boolToInt(row.LocalSuperPriority.Valid && row.LocalSuperPriority.Int64 == 1)
}

func chainPriorityRank(row *chainCandidateRow) int {
	if row.LocalPriority.Valid {
		return int(row.LocalPriority.Int64)
	}
	return row.Priority
}

func chainBucketKey(row *chainCandidateRow) string {
	return fmt.Sprintf("%d:%d:%d", chainFallbackRank(row), chainSuperRank(row), chainPriorityRank(row))
}

func chainBucketSize(rows []chainEligibleRow, key string) int {
	count := 0
	for _, item := range rows {
		if chainBucketKey(item.row) == key {
			count++
		}
	}
	return count
}

// compareChainQuality mirrors compareGatewayDispatchCandidateRowsByQuality:
// rows with a fresh score order first, then ascending score, then name/id.
func compareChainQuality(left, right *chainCandidateRow, collator *collate.Collator) int {
	leftHas, rightHas := left.QualityScore != nil, right.QualityScore != nil
	if leftHas != rightHas {
		if leftHas {
			return -1
		}
		return 1
	}
	if leftHas && rightHas && *left.QualityScore != *right.QualityScore {
		if *left.QualityScore < *right.QualityScore {
			return -1
		}
		return 1
	}
	if delta := collator.CompareString(left.Name, right.Name); delta != 0 {
		return delta
	}
	return strings.Compare(left.ID, right.ID)
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// chainNameCollator builds the zh-CN collator (Node localeCompare('zh-CN')).
func chainNameCollator() *collate.Collator { return collate.New(language.Chinese) }

// ---------------------------------------------------------------------------
// group access metadata (resolveGroupUsageAccessMetadata)
// ---------------------------------------------------------------------------

// resolveGroupAccess mirrors resolveGroupUsageAccessMetadata including the
// authorized-group branch (group authorization row + local settings
// overrides).
func (s *chainAccountsSelector) resolveGroupAccess(ctx context.Context, groupID, systemAccountID string) (*gatewayruntimecache.GroupUsageAccessMetadata, error) {
	var ownerID, providerCode string
	var enabled int
	var groupType, schedulingPolicyJSON sql.NullString
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT system_account_id, provider_code, enabled, group_type, scheduling_policy_json FROM `+s.table("groups")+` WHERE id = ?`),
		groupID).Scan(&ownerID, &providerCode, &enabled, &groupType, &schedulingPolicyJSON)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if ownerID == "" || providerCode == "" {
		return nil, nil
	}
	if enabled != 1 {
		return nil, nil
	}
	normalizedType, err := chainNormalizeGroupType(groupType)
	if err != nil {
		return nil, err
	}
	policy, err := chainParseGroupSchedulingPolicy(schedulingPolicyJSON, normalizedType)
	if err != nil {
		return nil, err
	}
	if ownerID == systemAccountID {
		return &gatewayruntimecache.GroupUsageAccessMetadata{
			GroupOwnerSystemAccountID: ownerID,
			ProviderCode:              providerCode,
			GroupAccessType:           gatewayruntimecache.GroupAccessTypeOwner,
			GroupType:                 normalizedType,
			SchedulingPolicy:          policy,
		}, nil
	}
	authorization, err := s.activeResourceAuthorization(ctx, "group", groupID, systemAccountID)
	if err != nil {
		return nil, err
	}
	if authorization == nil {
		return nil, nil
	}
	var localEnabled sql.NullInt64
	var localType, localPolicyJSON sql.NullString
	err = s.db.QueryRowContext(ctx, s.bind(`SELECT enabled, group_type, scheduling_policy_json FROM `+s.table("group_authorization_settings")+` WHERE authorization_id = ? AND system_account_id = ? AND group_id = ? LIMIT 1`),
		authorization.id, systemAccountID, groupID).Scan(&localEnabled, &localType, &localPolicyJSON)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if localEnabled.Valid && localEnabled.Int64 == 0 {
		return nil, nil
	}
	localTypeValue := normalizedType
	if localType.Valid && strings.TrimSpace(localType.String) != "" {
		normalized, typeErr := chainNormalizeGroupType(localType)
		if typeErr != nil {
			return nil, typeErr
		}
		localTypeValue = normalized
	}
	policyJSON := localPolicyJSON
	if !policyJSON.Valid || strings.TrimSpace(policyJSON.String) == "" {
		policyJSON = schedulingPolicyJSON
	}
	localPolicy, err := chainParseGroupSchedulingPolicy(policyJSON, localTypeValue)
	if err != nil {
		return nil, err
	}
	return &gatewayruntimecache.GroupUsageAccessMetadata{
		GroupOwnerSystemAccountID:      ownerID,
		ProviderCode:                   providerCode,
		GroupAccessType:                gatewayruntimecache.GroupAccessTypeAuthorized,
		GroupType:                      localTypeValue,
		SchedulingPolicy:               localPolicy,
		GroupAuthorizationID:           nullStringPtr(sql.NullString{String: authorization.id, Valid: true}),
		GroupAuthorizationExpiresAt:    authorization.expiresAt,
		GroupAuthorizationQuotaLimited: chainResourceAuthorizationQuotaLimited(authorization.limitsJSON),
		GroupAuthorizationSourceType:   authorization.effectiveSourceType,
		GroupAuthorizationSourceTeamID: authorization.effectiveSourceTeamID,
	}, nil
}

// chainAuthorizationRow mirrors the ResourceAuthorizationRow subset the
// selector consumes.
type chainAuthorizationRow struct {
	id                    string
	resourceID            string
	resourceOwnerID       string
	expiresAt             *string
	effectiveSourceType   *string
	effectiveSourceTeamID *string
	limitsJSON            *string
}

const chainAuthorizationColumns = `id, resource_id, resource_owner_system_account_id, expires_at, effective_source_type, effective_source_team_id, limits_json`

// activeResourceAuthorization mirrors activeResourceAuthorization.
func (s *chainAccountsSelector) activeResourceAuthorization(ctx context.Context, resourceType, resourceID, granteeSystemAccountID string) (*chainAuthorizationRow, error) {
	now := chainNowISO(s.now)
	query := fmt.Sprintf(`SELECT %s FROM %s WHERE resource_type = ? AND resource_id = ? AND grantee_system_account_id = ? AND status = 'active' AND (expires_at IS NULL OR expires_at > ?) LIMIT 1`,
		chainAuthorizationColumns, s.table("resource_authorizations"))
	var authorization chainAuthorizationRow
	var resourceOwner sql.NullString
	err := s.db.QueryRowContext(ctx, s.bind(query), resourceType, resourceID, granteeSystemAccountID, now).
		Scan(&authorization.id, &authorization.resourceID, &resourceOwner, &authorization.expiresAt,
			&authorization.effectiveSourceType, &authorization.effectiveSourceTeamID, &authorization.limitsJSON)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	authorization.resourceOwnerID = resourceOwner.String
	return &authorization, nil
}

// activeResourceAuthorizationByID mirrors activeResourceAuthorizationById.
func (s *chainAccountsSelector) activeResourceAuthorizationByID(ctx context.Context, authorizationID, granteeSystemAccountID string) (*chainAuthorizationRow, error) {
	now := chainNowISO(s.now)
	query := fmt.Sprintf(`SELECT %s FROM %s WHERE id = ? AND grantee_system_account_id = ? AND status = 'active' AND (expires_at IS NULL OR expires_at > ?) LIMIT 1`,
		chainAuthorizationColumns, s.table("resource_authorizations"))
	var authorization chainAuthorizationRow
	var resourceOwner sql.NullString
	err := s.db.QueryRowContext(ctx, s.bind(query), authorizationID, granteeSystemAccountID, now).
		Scan(&authorization.id, &authorization.resourceID, &resourceOwner, &authorization.expiresAt,
			&authorization.effectiveSourceType, &authorization.effectiveSourceTeamID, &authorization.limitsJSON)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	authorization.resourceOwnerID = resourceOwner.String
	return &authorization, nil
}

// activeResourceAuthorizationsByIDs mirrors activeResourceAuthorizationsByIds
// keyed by both id and resource id (Node builds the merged lookup map).
func (s *chainAccountsSelector) activeResourceAuthorizationsByIDs(ctx context.Context, authorizationIDs []string, granteeSystemAccountID string) (map[string]*chainAuthorizationRow, error) {
	ids := uniqueNonEmpty(authorizationIDs)
	result := map[string]*chainAuthorizationRow{}
	if len(ids) == 0 {
		return result, nil
	}
	now := chainNowISO(s.now)
	placeholders := strings.TrimRight(strings.Repeat("?, ", len(ids)), ", ")
	query := fmt.Sprintf(`SELECT %s FROM %s WHERE grantee_system_account_id = ? AND status = 'active' AND (expires_at IS NULL OR expires_at > ?) AND id IN (%s)`,
		chainAuthorizationColumns, s.table("resource_authorizations"), placeholders)
	args := []any{granteeSystemAccountID, now}
	args = append(args, rowsArgs(ids)...)
	rows, err := s.db.QueryContext(ctx, s.bind(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var authorization chainAuthorizationRow
		var resourceOwner sql.NullString
		if err := rows.Scan(&authorization.id, &authorization.resourceID, &resourceOwner, &authorization.expiresAt,
			&authorization.effectiveSourceType, &authorization.effectiveSourceTeamID, &authorization.limitsJSON); err != nil {
			return nil, err
		}
		authorization.resourceOwnerID = resourceOwner.String
		result[authorization.id] = &authorization
		result[authorization.resourceID] = &authorization
	}
	return result, rows.Err()
}

// loadAccountAuthorizationsForSelection mirrors loadAccountAuthorizationsForSelection:
// only owner-scope groups prefetch the account authorizations (nil map keeps
// the Node authorized-group fallback semantics at the call sites).
func (s *chainAccountsSelector) loadAccountAuthorizationsForSelection(ctx context.Context, rows []chainCandidateRow, groupAccess *gatewayruntimecache.GroupUsageAccessMetadata, systemAccountID string) (map[string]*chainAuthorizationRow, error) {
	if groupAccess.GroupAccessType == gatewayruntimecache.GroupAccessTypeAuthorized {
		return nil, nil
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		if value := strings.TrimSpace(row.AuthorizationInstanceAuthorizationID.String); value != "" {
			ids = append(ids, value)
		}
	}
	return s.activeResourceAuthorizationsByIDs(ctx, ids, systemAccountID)
}

// ---------------------------------------------------------------------------
// access resolution (resolveOpenAIAccountAccess + canScheduleAuthorizedAccount)
// ---------------------------------------------------------------------------

type chainAccountAccessType string

const (
	chainAccountAccessOwner           chainAccountAccessType = "owner"
	chainAccountAccessAuthorized      chainAccountAccessType = "account_authorized"
	chainAccountAccessGroupAuthorized chainAccountAccessType = "group_authorized"
)

// chainAccountAccess mirrors OpenAIAccountAccess.
type chainAccountAccess struct {
	accountAccessType      chainAccountAccessType
	accountOwnerID         *string
	accountAuthorizationID *string
	expiresAt              *string
	quotaLimited           *bool
	sourceType             *string
	sourceTeamID           *string
}

// chainAccountAccessFromAuthorization mirrors the authorized arm of
// resolveOpenAIAccountAccess.
func chainAccountAccessFromAuthorization(authorization *chainAuthorizationRow) *chainAccountAccess {
	return &chainAccountAccess{
		accountAccessType:      chainAccountAccessAuthorized,
		accountOwnerID:         strPtr(authorization.resourceOwnerID),
		accountAuthorizationID: strPtr(authorization.id),
		expiresAt:              authorization.expiresAt,
		quotaLimited:           chainResourceAuthorizationQuotaLimited(authorization.limitsJSON),
		sourceType:             authorization.effectiveSourceType,
		sourceTeamID:           authorization.effectiveSourceTeamID,
	}
}

// resolveChainAccountAccess mirrors resolveOpenAIAccountAccess: the prefetch
// map is authoritative for owner-scope groups (nil map = authorized group,
// the per-row database read then applies like the Node fallback).
func (s *chainAccountsSelector) resolveChainAccountAccess(ctx context.Context, row *chainCandidateRow, callerSystemAccountID string, groupAccess *gatewayruntimecache.GroupUsageAccessMetadata, boundAccountAuthorizationID string, authorizations map[string]*chainAuthorizationRow) (*chainAccountAccess, error) {
	accountOwnerID := row.SystemAccountID
	if authID := strings.TrimSpace(row.AuthorizationInstanceAuthorizationID.String); authID != "" {
		if accountOwnerID != callerSystemAccountID {
			return nil, nil
		}
		var authorization *chainAuthorizationRow
		var err error
		if authorizations != nil {
			authorization = authorizations[authID]
		} else {
			authorization, err = s.activeResourceAuthorizationByID(ctx, authID, callerSystemAccountID)
			if err != nil {
				return nil, err
			}
		}
		if authorization == nil {
			return nil, nil
		}
		if boundAccountAuthorizationID != "" && authorization.id != boundAccountAuthorizationID {
			return nil, nil
		}
		return chainAccountAccessFromAuthorization(authorization), nil
	}
	if accountOwnerID == callerSystemAccountID {
		return &chainAccountAccess{accountAccessType: chainAccountAccessOwner}, nil
	}
	if groupAccess.GroupAccessType == gatewayruntimecache.GroupAccessTypeAuthorized {
		if accountOwnerID == groupAccess.GroupOwnerSystemAccountID {
			return &chainAccountAccess{accountAccessType: chainAccountAccessGroupAuthorized}, nil
		}
		return nil, nil
	}
	var authorization *chainAuthorizationRow
	var err error
	if authorizations != nil {
		lookup := boundAccountAuthorizationID
		if lookup == "" {
			lookup = row.ID
		}
		authorization = authorizations[lookup]
	} else {
		authorization, err = s.activeResourceAuthorization(ctx, "account", row.ID, callerSystemAccountID)
		if err != nil {
			return nil, err
		}
	}
	if boundAccountAuthorizationID != "" && (authorization == nil || authorization.id != boundAccountAuthorizationID) {
		return nil, nil
	}
	if authorization == nil {
		return nil, nil
	}
	return chainAccountAccessFromAuthorization(authorization), nil
}

// canScheduleChainAuthorizedAccount mirrors canScheduleAuthorizedAccount.
func canScheduleChainAuthorizedAccount(row *chainCandidateRow, authorizations map[string]*chainAuthorizationRow, access *chainAccountAccess) bool {
	if access.accountAccessType == chainAccountAccessOwner || access.accountAccessType == chainAccountAccessGroupAuthorized {
		return true
	}
	if access.accountAuthorizationID == nil || *access.accountAuthorizationID == "" {
		return false
	}
	authorizationID := *access.accountAuthorizationID
	if authorizations != nil {
		authorization := authorizations[authorizationID]
		if authorization == nil {
			authorization = authorizations[row.ID]
		}
		return authorization != nil && authorization.id == authorizationID
	}
	return false
}

// ---------------------------------------------------------------------------
// availability gates (isOpenAIAccountAvailableForSelection)
// ---------------------------------------------------------------------------

func (row *chainCandidateRow) resourceAccountID() string {
	if row.ResourceAccountID.Valid && row.ResourceAccountID.String != "" {
		return row.ResourceAccountID.String
	}
	return row.ID
}

func (row *chainCandidateRow) resourceProxyProfileID() string {
	if row.ResourceProxyProfileID.Valid && row.ResourceProxyProfileID.String != "" {
		return row.ResourceProxyProfileID.String
	}
	if row.ProxyProfileID.Valid {
		return row.ProxyProfileID.String
	}
	return ""
}

func (row *chainCandidateRow) resourceProviderCode() string {
	if row.ResourceProviderCode.Valid && row.ResourceProviderCode.String != "" {
		return row.ResourceProviderCode.String
	}
	return row.ProviderCode
}

func (row *chainCandidateRow) resourceProtocolCode() string {
	if row.ResourceProtocolCode.Valid && row.ResourceProtocolCode.String != "" {
		return row.ResourceProtocolCode.String
	}
	return row.ProtocolCode.String
}

func (row *chainCandidateRow) resourceProtocolVersion() string {
	if row.ResourceProtocolVersion.Valid && row.ResourceProtocolVersion.String != "" {
		return row.ResourceProtocolVersion.String
	}
	return row.ProtocolVersion.String
}

func (row *chainCandidateRow) resourceType() string {
	if row.ResourceType.Valid && row.ResourceType.String != "" {
		return row.ResourceType.String
	}
	return row.Type
}

func (row *chainCandidateRow) resourceCredentialsEncrypted() string {
	if row.ResourceCredentialsEncrypted.Valid && row.ResourceCredentialsEncrypted.String != "" {
		return row.ResourceCredentialsEncrypted.String
	}
	return row.CredentialsEncrypted.String
}

func (row *chainCandidateRow) resourceConcurrencyLimit() int {
	if row.ResourceConcurrencyLimit.Valid {
		return int(row.ResourceConcurrencyLimit.Int64)
	}
	return row.ConcurrencyLimit
}

// chainAccountAvailableForSelection mirrors isOpenAIAccountAvailableForSelection
// (physical gates + resource-account gates; the authorized-binding gate runs
// in the eligibility loop where the access object is known).
func chainAccountAvailableForSelection(row *chainCandidateRow, now string, includeUnavailable bool) (bool, error) {
	nowMillis, err := chainRFC3339Millis(now)
	if err != nil {
		return false, fmt.Errorf("账户选择当前时间必须是带 Z 或数值 offset 的 RFC3339 时间")
	}
	physical, err := chainPhysicalAccountAvailable(row, nowMillis, includeUnavailable)
	if err != nil {
		return false, err
	}
	if !physical {
		return false, nil
	}
	return chainResourceAccountAvailable(row, nowMillis, includeUnavailable)
}

func chainPhysicalAccountAvailable(row *chainCandidateRow, nowMillis int64, includeUnavailable bool) (bool, error) {
	if row.AccountExpiresAt.Valid && strings.TrimSpace(row.AccountExpiresAt.String) != "" {
		expires, err := chainRFC3339Millis(row.AccountExpiresAt.String)
		if err != nil {
			return false, fmt.Errorf("AI 账户 accountExpiresAt必须是带 Z 或数值 offset 的 RFC3339 时间")
		}
		if expires <= nowMillis {
			return false, nil
		}
	}
	if row.Schedulable != 1 {
		return false, nil
	}
	if includeUnavailable {
		return row.Status == "active" || row.Status == "rate_limited" || row.Status == "temporary_unavailable", nil
	}
	if row.Status != "active" {
		return false, nil
	}
	if !row.CooldownUntil.Valid || strings.TrimSpace(row.CooldownUntil.String) == "" {
		return true, nil
	}
	cooldown, err := chainRFC3339Millis(row.CooldownUntil.String)
	if err != nil {
		return false, fmt.Errorf("AI 账户 cooldownUntil必须是带 Z 或数值 offset 的 RFC3339 时间")
	}
	return cooldown <= nowMillis, nil
}

func chainResourceAccountAvailable(row *chainCandidateRow, nowMillis int64, includeUnavailable bool) (bool, error) {
	if !row.AuthorizationInstanceAuthorizationID.Valid || strings.TrimSpace(row.AuthorizationInstanceAuthorizationID.String) == "" {
		return true, nil
	}
	if !row.ResourceAccountID.Valid || row.ResourceAccountID.String == "" || !row.ResourceStatus.Valid || row.ResourceStatus.String == "" {
		return false, nil
	}
	if row.ResourceAccountExpiresAt.Valid && strings.TrimSpace(row.ResourceAccountExpiresAt.String) != "" {
		expires, err := chainRFC3339Millis(row.ResourceAccountExpiresAt.String)
		if err != nil {
			return false, fmt.Errorf("授权来源账户 accountExpiresAt必须是带 Z 或数值 offset 的 RFC3339 时间")
		}
		if expires <= nowMillis {
			return false, nil
		}
	}
	if row.ResourceLastErrorCode.Valid && row.ResourceLastErrorCode.String == "account_expired" {
		return false, nil
	}
	if !row.ResourceSchedulable.Valid || row.ResourceSchedulable.Int64 != 1 {
		return false, nil
	}
	if includeUnavailable {
		status := row.ResourceStatus.String
		return status == "active" || status == "rate_limited" || status == "temporary_unavailable", nil
	}
	if row.ResourceStatus.String != "active" {
		return false, nil
	}
	if !row.ResourceCooldownUntil.Valid || strings.TrimSpace(row.ResourceCooldownUntil.String) == "" {
		return true, nil
	}
	cooldown, err := chainRFC3339Millis(row.ResourceCooldownUntil.String)
	if err != nil {
		return false, fmt.Errorf("授权来源账户 cooldownUntil必须是带 Z 或数值 offset 的 RFC3339 时间")
	}
	return cooldown <= nowMillis, nil
}

// ---------------------------------------------------------------------------
// fresh quality rows (loadFreshGatewayDispatchCandidateQualityRows)
// ---------------------------------------------------------------------------

type chainQualityRow struct {
	score        *float64
	state        *string
	firstTokenMs *float64
}

// loadFreshQualityRows mirrors loadFreshGatewayDispatchCandidateQualityRows:
// one chunked read against the stats account_quality_scores table with the
// 24h freshness window.
func (s *chainAccountsSelector) loadFreshQualityRows(ctx context.Context, accountIDs []string, freshAfter string) (map[string]chainQualityRow, error) {
	ids := uniqueNonEmpty(accountIDs)
	result := map[string]chainQualityRow{}
	if len(ids) == 0 {
		return result, nil
	}
	for start := 0; start < len(ids); start += 900 {
		end := start + 900
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[start:end]
		placeholders := strings.TrimRight(strings.Repeat("?, ", len(chunk)), ", ")
		query := fmt.Sprintf(`SELECT account_id, quality_score, quality_state, ewma_first_token_ms
			FROM %s WHERE account_id IN (%s) AND last_sample_at >= ?`, s.statsTable("account_quality_scores"), placeholders)
		args := rowsArgs(chunk)
		args = append(args, freshAfter)
		rows, err := s.statsDB.QueryContext(ctx, s.bind(query), args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var accountID string
			var quality chainQualityRow
			if err := rows.Scan(&accountID, &quality.score, &quality.state, &quality.firstTokenMs); err != nil {
				rows.Close()
				return nil, err
			}
			result[accountID] = quality
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return result, nil
}

// chainQualityFreshAfterISO mirrors gatewayDispatchCandidateQualityFreshAfterIso.
func chainQualityFreshAfterISO(now func() time.Time) string {
	return now().UTC().Add(-24 * time.Hour).UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

// ---------------------------------------------------------------------------
// hydration loads (models / mappings / rotation states / proxy profiles)
// ---------------------------------------------------------------------------

func (s *chainAccountsSelector) loadSupportedModelsByAccountIds(ctx context.Context, accountIDs []string) (map[string][]string, error) {
	result := map[string][]string{}
	ids := uniqueNonEmpty(accountIDs)
	if len(ids) == 0 {
		return result, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?, ", len(ids)), ", ")
	query := fmt.Sprintf(`SELECT account_id, model FROM %s WHERE account_id IN (%s) ORDER BY created_at ASC, model ASC`, s.table("account_supported_models"), placeholders)
	rows, err := s.db.QueryContext(ctx, s.bind(query), rowsArgs(ids)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var accountID, model string
		if err := rows.Scan(&accountID, &model); err != nil {
			return nil, err
		}
		result[accountID] = append(result[accountID], model)
	}
	return result, rows.Err()
}

func (s *chainAccountsSelector) loadModelMappingsByAccountIds(ctx context.Context, accountIDs []string) (map[string][]gatewayruntimecache.AccountModelMapping, error) {
	result := map[string][]gatewayruntimecache.AccountModelMapping{}
	ids := uniqueNonEmpty(accountIDs)
	if len(ids) == 0 {
		return result, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?, ", len(ids)), ", ")
	// Node loadModelMappingsByAccountIds orders by the composite primary key
	// columns (account_model_mappings has NO id column — Node
	// business-schema.ts and the maintenance DDL define PRIMARY KEY
	// (account_id, source_model, source_endpoint_family)); ordering by the
	// drifted `id` column 500s every runtime-resolution read on fresh
	// databases.
	query := fmt.Sprintf(`SELECT account_id, source_model, source_endpoint_family, upstream_model, upstream_endpoint_family, enabled
		FROM %s WHERE account_id IN (%s) ORDER BY account_id ASC, source_model ASC, source_endpoint_family ASC`, s.table("account_model_mappings"), placeholders)
	rows, err := s.db.QueryContext(ctx, s.bind(query), rowsArgs(ids)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var accountID string
		var mapping gatewayruntimecache.AccountModelMapping
		var sourceFamily, upstreamFamily sql.NullString
		var enabled int
		if err := rows.Scan(&accountID, &mapping.SourceModel, &sourceFamily, &mapping.UpstreamModel, &upstreamFamily, &enabled); err != nil {
			return nil, err
		}
		mapping.SourceEndpointFamily = sourceFamily.String
		mapping.UpstreamEndpointFamily = upstreamFamily.String
		mapping.Enabled = enabled == 1
		result[accountID] = append(result[accountID], mapping)
	}
	return result, rows.Err()
}

// loadAPIKeyRuntimeStatesByAccountIds mirrors
// loadAccountApiKeyRuntimeStatesByAccountIds. The Go runtime-state struct
// carries the dispatch subset (fingerprint / disabled / cooldown /
// recovery); key_index and next_probe_at stay Node-owned columns.
func (s *chainAccountsSelector) loadAPIKeyRuntimeStatesByAccountIds(ctx context.Context, accountIDs []string) (map[string][]gatewayruntimecache.AccountAPIKeyRuntimeSelectionState, error) {
	result := map[string][]gatewayruntimecache.AccountAPIKeyRuntimeSelectionState{}
	ids := uniqueNonEmpty(accountIDs)
	if len(ids) == 0 {
		return result, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?, ", len(ids)), ", ")
	query := fmt.Sprintf(`SELECT account_id, key_fingerprint, key_index, status, cooldown_until, next_probe_at, recovery_started_at
		FROM %s WHERE account_id IN (%s)`, s.table("account_api_key_runtime_states"), placeholders)
	rows, err := s.db.QueryContext(ctx, s.bind(query), rowsArgs(ids)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var accountID string
		var state gatewayruntimecache.AccountAPIKeyRuntimeSelectionState
		var keyIndex sql.NullInt64
		var status sql.NullString
		var cooldown, nextProbe, recovery sql.NullString
		if err := rows.Scan(&accountID, &state.Fingerprint, &keyIndex, &status, &cooldown, &nextProbe, &recovery); err != nil {
			return nil, err
		}
		state.Disabled = !status.Valid || status.String != "active"
		state.CooldownUntil = nullStringPtr(cooldown)
		state.RecoveryStartedAt = nullStringPtr(recovery)
		_ = keyIndex
		_ = nextProbe
		result[accountID] = append(result[accountID], state)
	}
	return result, rows.Err()
}

// chainProxyProfileResolution mirrors ProxyProfileUrlResolution.
type chainProxyProfileResolution struct {
	proxyURL     *string
	unavailable  *bool
	errorMessage *string
}

// resolveProxyURLsForProfiles mirrors resolveProxyUrlsForProfiles: the proxy
// profiles table read with the AES-GCM password decryption; disabled or
// missing profiles surface the Node unavailable contract (never an error).
func (s *chainAccountsSelector) resolveProxyURLsForProfiles(ctx context.Context, proxyProfileIDs []string) (map[string]chainProxyProfileResolution, error) {
	ids := uniqueNonEmpty(proxyProfileIDs)
	output := map[string]chainProxyProfileResolution{}
	if len(ids) == 0 {
		return output, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?, ", len(ids)), ", ")
	query := fmt.Sprintf(`SELECT id, type, host, port, username, password_encrypted, enabled FROM %s WHERE id IN (%s)`, s.table("proxy_profiles"), placeholders)
	rows, err := s.db.QueryContext(ctx, s.bind(query), rowsArgs(ids)...)
	if err != nil {
		return nil, err
	}
	type proxyRow struct {
		id                string
		proxyType         string
		host              string
		port              int
		username          sql.NullString
		passwordEncrypted sql.NullString
		enabled           sql.NullInt64
	}
	rowsByID := map[string]*proxyRow{}
	for rows.Next() {
		proxy := &proxyRow{}
		if err := rows.Scan(&proxy.id, &proxy.proxyType, &proxy.host, &proxy.port, &proxy.username, &proxy.passwordEncrypted, &proxy.enabled); err != nil {
			rows.Close()
			return nil, err
		}
		rowsByID[proxy.id] = proxy
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for _, id := range ids {
		proxy := rowsByID[id]
		if proxy == nil || !proxy.enabled.Valid || proxy.enabled.Int64 != 1 {
			output[id] = chainProxyUnavailable("代理不存在或已停用，请选择一个已启用的代理")
			continue
		}
		url, err := chainProxyURLFromRow(s.secret, proxy.proxyType, proxy.host, proxy.port, proxy.username.String, proxy.passwordEncrypted.String)
		if err != nil {
			output[id] = chainProxyUnavailable("代理凭据不可解密，请检查代理配置")
			continue
		}
		output[id] = chainProxyProfileResolution{proxyURL: &url}
	}
	return output, nil
}

func chainProxyUnavailable(message string) chainProxyProfileResolution {
	unavailable := true
	return chainProxyProfileResolution{unavailable: &unavailable, errorMessage: &message}
}

// chainProxyURLFromRow mirrors proxyUrlFromRow + proxyPassword.
func chainProxyURLFromRow(secret, proxyType, host string, port int, username, passwordEncrypted string) (string, error) {
	protocol := proxyType
	if proxyType == "socks5h" || proxyType == "socks5" {
		protocol = "socks5h"
	}
	password := ""
	if passwordEncrypted != "" {
		var envelope struct {
			Password *string `json:"password"`
		}
		if err := accounts.DecryptJSON(secret, passwordEncrypted, &envelope); err != nil {
			return "", err
		}
		if envelope.Password != nil {
			password = *envelope.Password
		}
	}
	credentials := ""
	if username != "" {
		credentials = chainURIEncode(username)
		if password != "" {
			credentials += ":" + chainURIEncode(password)
		}
		credentials += "@"
	}
	return fmt.Sprintf("%s://%s%s:%d", protocol, credentials, host, port), nil
}

// ---------------------------------------------------------------------------
// shared small helpers
// ---------------------------------------------------------------------------

func chainNowISO(now func() time.Time) string {
	return now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

func uniqueNonEmpty(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		out = append(out, trimmed)
	}
	return out
}

func rowsArgs(values []string) []any {
	args := make([]any, 0, len(values))
	for _, value := range values {
		args = append(args, value)
	}
	return args
}

func strPtr(value string) *string { return &value }

func nullStringPtr(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

type nullStringSink = sql.NullString

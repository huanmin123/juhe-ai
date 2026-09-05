package accounts

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// List sort fields mirror AccountListSortField (account-list-options.ts).
const (
	defaultAccountListPageSize = 50
	maxAccountListPageSize     = 200
	maxAccountOptionPageSize   = 50
	defaultListWindowRowsTotal = 1000
	maxAccountListIDs          = 200
	maxAccountOptionIDs        = 50
	maxAccountListTagFilters   = 100
)

// ListSort is one AccountListSort entry.
type ListSort struct {
	Field string
	Order string // asc | desc
}

// ListOptions mirrors AccountListOptions.
type ListOptions struct {
	Sorts                     []ListSort
	IDs                       []string
	Page                      int
	PageSize                  int
	Keyword                   string
	ProviderCode              string
	ProviderProtocolProfileID string
	GroupID                   string
	TagIDs                    []string
	Type                      string
	Status                    string
	Schedulable               string // all | enabled | disabled | cooling
}

// NormalizedListOptions mirrors NormalizedAccountListOptions.
type NormalizedListOptions struct {
	Sorts                     []ListSort
	IDs                       []string
	Page                      int
	PageSize                  int
	Keyword                   string
	ProviderCode              string
	ProviderProtocolProfileID string
	GroupID                   string
	TagIDs                    []string
	Type                      string
	Status                    string
	Schedulable               string
}

var accountListSortFields = map[string]bool{
	"priority": true, "superPriority": true, "fallback": true, "name": true,
	"type": true, "providerCode": true, "systemAccount": true, "concurrency": true,
	"status": true, "accountExpiresAt": true, "lastUsedAt": true,
}

// normalizeListOptions mirrors normalizeAccountListOptions: deduplicated sort
// fields with priority (default asc) pinned first and the optional status sort
// second, clamped paging, trimmed filters.
func normalizeListOptions(options ListOptions) NormalizedListOptions {
	seen := map[string]bool{}
	inputSorts := []ListSort{}
	for _, sort := range options.Sorts {
		if !accountListSortFields[sort.Field] || (sort.Order != "asc" && sort.Order != "desc") {
			continue
		}
		if seen[sort.Field] {
			continue
		}
		seen[sort.Field] = true
		inputSorts = append(inputSorts, sort)
	}
	prioritySort := ListSort{Field: "priority", Order: "asc"}
	for _, sort := range inputSorts {
		if sort.Field == "priority" {
			prioritySort = sort
			break
		}
	}
	var statusSort *ListSort
	for index := range inputSorts {
		if inputSorts[index].Field == "status" {
			statusSort = &inputSorts[index]
			break
		}
	}
	sorts := []ListSort{prioritySort}
	if statusSort != nil {
		sorts = append(sorts, *statusSort)
	}
	for _, sort := range inputSorts {
		if sort.Field != "priority" && sort.Field != "status" {
			sorts = append(sorts, sort)
		}
	}
	pageSize := defaultAccountListPageSize
	if options.PageSize > 0 {
		pageSize = minInt(maxAccountListPageSize, maxInt(1, options.PageSize))
	}
	page := 1
	if options.Page > 0 {
		pageCap := maxInt(1, (defaultListWindowRowsTotal-1)/pageSize)
		page = minInt(pageCap, maxInt(1, options.Page))
	}
	schedulable := options.Schedulable
	if schedulable != "enabled" && schedulable != "disabled" && schedulable != "cooling" {
		schedulable = "all"
	}
	return NormalizedListOptions{
		Sorts: sorts, IDs: normalizeTextList(options.IDs, maxAccountListIDs),
		Page: page, PageSize: pageSize,
		Keyword:                   strings.TrimSpace(options.Keyword),
		ProviderCode:              strings.TrimSpace(options.ProviderCode),
		ProviderProtocolProfileID: strings.TrimSpace(options.ProviderProtocolProfileID),
		GroupID:                   strings.TrimSpace(options.GroupID),
		TagIDs:                    normalizeTextList(options.TagIDs, maxAccountListTagFilters),
		Type:                      strings.TrimSpace(options.Type),
		Status:                    strings.TrimSpace(options.Status),
		Schedulable:               schedulable,
	}
}

// TagSummary mirrors AccountTagSummary (id/name only on list items).
type TagSummary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Permissions mirrors AccountListPermissions (ownerPermissions()).
type Permissions struct {
	CanUse                 bool `json:"canUse"`
	CanEdit                bool `json:"canEdit"`
	CanDelete              bool `json:"canDelete"`
	CanReturnAuthorization bool `json:"canReturnAuthorization"`
	CanAuthorize           bool `json:"canAuthorize"`
	CanViewCredentials     bool `json:"canViewCredentials"`
	CanLock                bool `json:"canLock"`
}

func ownerPermissions() Permissions {
	return Permissions{CanUse: true, CanEdit: true, CanDelete: true, CanReturnAuthorization: false, CanAuthorize: true, CanViewCredentials: true, CanLock: true}
}

// EffectiveAvailability mirrors AccountEffectiveAvailability.
type EffectiveAvailability struct {
	Available    bool    `json:"available"`
	Status       string  `json:"status"`
	Label        string  `json:"label"`
	Color        string  `json:"color"`
	BlockerScope *string `json:"blockerScope,omitempty"`
	Reason       *string `json:"reason,omitempty"`
	RetryAt      *string `json:"retryAt,omitempty"`
}

// UsageSummary mirrors AccountListUsageSummary; the populated variant is
// owned by the J5 stats slice, so the slice renders the shared zero value.
type UsageSummary struct {
	RequestCount int     `json:"requestCount"`
	TotalTokens  int     `json:"totalTokens"`
	TotalCost    float64 `json:"totalCost"`
}

func emptyUsageSummary() UsageSummary { return UsageSummary{} }

// LockStatePublic mirrors the lock fields joined onto list items; the fields
// stay omitted when the account has no lock row (Node only assigns them from
// the lock-state map).
type LockStatePublic struct {
	LockEnabled              *bool   `json:"lockEnabled,omitempty"`
	LockState                *string `json:"lockState,omitempty"`
	LockDeathTimeoutSeconds  *int    `json:"lockDeathTimeoutSeconds,omitempty"`
	LockRetryIntervalSeconds *int    `json:"lockRetryIntervalSeconds,omitempty"`
}

// ListItem mirrors the hydrated AccountListItem the management list returns
// (owner mode). Runtime overlays (runtimeAvailability, circuitSummary,
// balanceSnapshot, apiKeyRuntime) belong to the runtime/circuit/balance
// companion slices and stay omitted, exactly like the usage zero value.
type ListItem struct {
	ID                                   string                `json:"id"`
	ConfigRevision                       int64                 `json:"configRevision"`
	SystemAccountID                      *string               `json:"systemAccountId,omitempty"`
	SystemAccountName                    *string               `json:"systemAccountName,omitempty"`
	OwnerSystemAccountID                 string                `json:"ownerSystemAccountId"`
	OwnerSystemAccountName               *string               `json:"ownerSystemAccountName,omitempty"`
	ProviderCode                         string                `json:"providerCode"`
	ProviderName                         *string               `json:"providerName,omitempty"`
	ProviderProtocolProfileID            string                `json:"providerProtocolProfileId"`
	ProtocolCode                         string                `json:"protocolCode"`
	ProtocolVersion                      string                `json:"protocolVersion"`
	Name                                 string                `json:"name"`
	Notes                                *string               `json:"notes,omitempty"`
	Type                                 string                `json:"type"`
	Status                               string                `json:"status"`
	ConcurrencyLimit                     int                   `json:"concurrencyLimit"`
	Priority                             int                   `json:"priority"`
	SuperPriorityEnabled                 bool                  `json:"superPriorityEnabled"`
	FallbackEnabled                      bool                  `json:"fallbackEnabled"`
	ClientCompatibility                  string                `json:"clientCompatibility"`
	Tags                                 []TagSummary          `json:"tags"`
	HealthCheckModel                     string                `json:"healthCheckModel"`
	HealthCheckEndpointMode              string                `json:"healthCheckEndpointMode"`
	ProxyProfileID                       *string               `json:"proxyProfileId,omitempty"`
	ProxyProfileName                     *string               `json:"proxyProfileName,omitempty"`
	ProxyProfileType                     *string               `json:"proxyProfileType,omitempty"`
	ProxyProfileEnabled                  *bool                 `json:"proxyProfileEnabled,omitempty"`
	ProxyProfileUnavailable              *bool                 `json:"proxyProfileUnavailable,omitempty"`
	ProxyProfileErrorMessage             *string               `json:"proxyProfileErrorMessage,omitempty"`
	Schedulable                          bool                  `json:"schedulable"`
	AvailabilitySchedule                 *AvailabilitySchedule `json:"availabilitySchedule,omitempty"`
	AccountExpiresAt                     *string               `json:"accountExpiresAt,omitempty"`
	CooldownUntil                        *string               `json:"cooldownUntil,omitempty"`
	LastErrorCode                        *string               `json:"lastErrorCode,omitempty"`
	LastErrorMessage                     *string               `json:"lastErrorMessage,omitempty"`
	LastErrorTraceID                     *string               `json:"lastErrorTraceId,omitempty"`
	LastUsedAt                           *string               `json:"lastUsedAt,omitempty"`
	EffectiveAvailability                EffectiveAvailability `json:"effectiveAvailability"`
	TodayUsage                           UsageSummary          `json:"todayUsage"`
	Usage                                UsageSummary          `json:"usage"`
	AccessType                           string                `json:"accessType"`
	AccountAuthorizationID               *string               `json:"accountAuthorizationId,omitempty"`
	AuthorizationInstanceSourceAccountID *string               `json:"authorizationInstanceSourceAccountId,omitempty"`
	BoundGroupID                         *string               `json:"boundGroupId,omitempty"`
	BoundGroupName                       *string               `json:"boundGroupName,omitempty"`
	GroupBindStatus                      *string               `json:"groupBindStatus,omitempty"`
	BindingSystemAccountID               *string               `json:"bindingSystemAccountId,omitempty"`
	Permissions                          Permissions           `json:"permissions"`
	LockStatePublic
}

// ListPageResult mirrors AccountManagementListResult.
type ListPageResult struct {
	Items       []ListItem `json:"items"`
	Total       int        `json:"total"`
	HasMore     bool       `json:"hasMore"`
	Page        int        `json:"page"`
	PageSize    int        `json:"pageSize"`
	GeneratedAt string     `json:"generatedAt"`
}

// listRow is the shared scan target for the management list rows.
type listRow struct {
	id                        string
	configRevision            int64
	systemAccountID           string
	systemAccountName         sql.NullString
	providerCode              string
	providerName              sql.NullString
	providerProtocolProfileID string
	protocolCode              string
	protocolVersion           string
	name                      string
	notes                     sql.NullString
	accountType               string
	status                    string
	concurrencyLimit          int
	priority                  int
	superPriorityEnabled      int
	fallbackEnabled           int
	clientCompatibility       string
	schedulable               int
	availabilityScheduleJSON  sql.NullString
	accountExpiresAt          sql.NullString
	cooldownUntil             sql.NullString
	lastErrorCode             sql.NullString
	lastErrorMessage          sql.NullString
	lastErrorTraceID          sql.NullString
	lastUsedAt                sql.NullString
	healthCheckModel          string
	healthCheckEndpointMode   string
	proxyProfileID            sql.NullString
	proxyProfileName          sql.NullString
	proxyProfileType          sql.NullString
	proxyProfileEnabled       sql.NullInt64
	bindingSystemAccountID    sql.NullString
	boundGroupID              sql.NullString
	boundGroupName            sql.NullString
	// M10 authorized-instance projection columns: the runtime authorization
	// stamp plus the source account's live values and the bound group's local
	// scheduling overrides (Node account-management-list.repository.ts:290-322,
	// :428-434). All nullable: owner rows and missing joins leave them NULL.
	authorizationID                     sql.NullString
	sourceAccountID                     sql.NullString
	authorizationEffectiveSourceType    sql.NullString
	sourceProviderCode                  sql.NullString
	sourceProviderName                  sql.NullString
	sourceProviderProtocolProfileID     sql.NullString
	sourceProtocolCode                  sql.NullString
	sourceProtocolVersion               sql.NullString
	sourceAccountType                   sql.NullString
	sourceProxyProfileID                sql.NullString
	sourceConcurrencyLimit              sql.NullInt64
	sourceClientCompatibility           sql.NullString
	resolvedSourceProxyProfileID        sql.NullString
	sourceProxyProfileName              sql.NullString
	sourceProxyProfileType              sql.NullString
	sourceProxyProfileEnabled           sql.NullInt64
	boundGroupLocalPriority             sql.NullInt64
	boundGroupLocalSuperPriorityEnabled sql.NullInt64
	boundGroupLocalFallbackEnabled      sql.NullInt64
}

func listItemColumns(alias string) []string {
	if alias == "" {
		alias = "accounts"
	}
	return []string{
		alias + ".id",
		alias + ".config_revision",
		alias + ".system_account_id",
		"COALESCE(system_accounts_ref.display_name, system_accounts_ref.username, " + alias + ".system_account_id) AS system_account_name",
		alias + ".provider_code",
		"providers.name AS provider_name",
		alias + ".provider_protocol_profile_id",
		alias + ".protocol_code",
		alias + ".protocol_version",
		alias + ".name",
		alias + ".notes",
		alias + ".type",
		alias + ".status",
		alias + ".concurrency_limit",
		alias + ".priority",
		alias + ".super_priority_enabled",
		alias + ".fallback_enabled",
		alias + ".client_compatibility",
		alias + ".schedulable",
		alias + ".availability_schedule_json",
		alias + ".account_expires_at",
		alias + ".cooldown_until",
		alias + ".last_error_code",
		alias + ".last_error_message",
		alias + ".last_error_trace_id",
		alias + ".last_used_at",
		alias + ".health_check_model",
		alias + ".health_check_endpoint_mode",
		alias + ".proxy_profile_id",
		"proxy_profiles.name AS proxy_profile_name",
		"proxy_profiles.type AS proxy_profile_type",
		"proxy_profiles.enabled AS proxy_profile_enabled",
		"group_bindings.system_account_id AS binding_system_account_id",
		"group_bindings.group_id AS bound_group_id",
		"bound_groups.name AS bound_group_name",
		// M10 authorized-instance projection: the instance stamp plus the
		// source account's live values and the bound group's local scheduling
		// overrides (Node account-management-list.repository.ts:290-322,
		// :428-434). Owner rows keep the frozen instance columns because every
		// source_* column is NULL when the instance stamp is NULL.
		alias + ".authorization_instance_authorization_id AS authorization_id",
		alias + ".authorization_instance_source_account_id AS authorization_instance_source_account_id",
		"authorizations.effective_source_type AS authorization_effective_source_type",
		"source_accounts.provider_code AS source_provider_code",
		"source_providers.name AS source_provider_name",
		"source_accounts.provider_protocol_profile_id AS source_provider_protocol_profile_id",
		"source_accounts.protocol_code AS source_protocol_code",
		"source_accounts.protocol_version AS source_protocol_version",
		"source_accounts.type AS source_type",
		"source_accounts.proxy_profile_id AS source_proxy_profile_id",
		"source_accounts.concurrency_limit AS source_concurrency_limit",
		"source_accounts.client_compatibility AS source_client_compatibility",
		"source_proxy_profiles.id AS resolved_source_proxy_profile_id",
		"source_proxy_profiles.name AS source_proxy_profile_name",
		"source_proxy_profiles.type AS source_proxy_profile_type",
		"source_proxy_profiles.enabled AS source_proxy_profile_enabled",
		"group_bindings.local_priority AS bound_group_local_priority",
		"group_bindings.local_super_priority_enabled AS bound_group_local_super_priority_enabled",
		"group_bindings.local_fallback_enabled AS bound_group_local_fallback_enabled",
	}
}

// listJoins renders the management-list joins: ranked group bindings,
// bound group name, system account names, provider display name, the proxy
// profile display row and the stamped instance's runtime authorization row
// (Node account-management-list.repository.ts:324-325 — both dialects share
// the plain LEFT JOIN and the `authorizations` alias). The source trio mirrors
// Node :326-328 plus the COALESCE display joins :451-454 split into dedicated
// `source_providers`/`source_proxy_profiles` aliases so owner rows keep the
// instance-frozen display values. Every join is LEFT and keyed on a unique
// column, so pagination's pageSize+1 probe cannot fan out.
func (s *Store) listJoins() (cte, joins string) {
	if s.pg {
		joins = ` LEFT JOIN LATERAL (
			SELECT group_accounts.system_account_id, group_accounts.group_id,
				group_accounts.account_authorization_id, group_accounts.local_priority,
				group_accounts.local_super_priority_enabled, group_accounts.local_fallback_enabled
			FROM ` + s.table("group_accounts") + ` group_accounts
			WHERE group_accounts.account_id = accounts.id
				AND group_accounts.system_account_id = accounts.system_account_id
				AND group_accounts.enabled = 1
			ORDER BY group_accounts.updated_at DESC, group_accounts.group_id ASC, group_accounts.account_id ASC
			LIMIT 1
		) group_bindings ON true`
	} else {
		cte = ` WITH ranked_group_bindings AS (
			SELECT group_accounts.account_id, group_accounts.system_account_id, group_accounts.group_id,
				group_accounts.account_authorization_id, group_accounts.local_priority,
				group_accounts.local_super_priority_enabled, group_accounts.local_fallback_enabled,
				ROW_NUMBER() OVER (
					PARTITION BY group_accounts.account_id, group_accounts.system_account_id
					ORDER BY group_accounts.updated_at DESC, group_accounts.group_id ASC, group_accounts.account_id ASC
				) AS binding_rank
			FROM ` + s.table("group_accounts") + ` group_accounts
			WHERE group_accounts.enabled = 1
		)`
		joins = ` LEFT JOIN ranked_group_bindings group_bindings
			ON group_bindings.account_id = accounts.id
			AND group_bindings.system_account_id = accounts.system_account_id
			AND group_bindings.binding_rank = 1`
	}
	joins += ` LEFT JOIN ` + s.table("groups") + ` bound_groups ON bound_groups.id = group_bindings.group_id
		LEFT JOIN ` + s.table("system_accounts") + ` system_accounts_ref
			ON system_accounts_ref.id = accounts.system_account_id
		LEFT JOIN ` + s.table("proxy_profiles") + ` proxy_profiles
			ON proxy_profiles.id = accounts.proxy_profile_id
		LEFT JOIN ` + s.table("providers") + ` providers
			ON providers.code = accounts.provider_code
		LEFT JOIN ` + s.table("resource_authorizations") + ` authorizations
			ON authorizations.id = accounts.authorization_instance_authorization_id
		LEFT JOIN ` + s.table("accounts") + ` source_accounts
			ON source_accounts.id = accounts.authorization_instance_source_account_id
			AND source_accounts.deleted_at IS NULL
		LEFT JOIN ` + s.table("providers") + ` source_providers
			ON source_providers.code = source_accounts.provider_code
		LEFT JOIN ` + s.table("proxy_profiles") + ` source_proxy_profiles
			ON source_proxy_profiles.id = source_accounts.proxy_profile_id`
	return cte, joins
}

func scanListRow(scan func(...any) error) (listRow, error) {
	var row listRow
	err := scan(
		&row.id, &row.configRevision, &row.systemAccountID, &row.systemAccountName,
		&row.providerCode, &row.providerName, &row.providerProtocolProfileID,
		&row.protocolCode, &row.protocolVersion, &row.name, &row.notes,
		&row.accountType, &row.status, &row.concurrencyLimit, &row.priority,
		&row.superPriorityEnabled, &row.fallbackEnabled, &row.clientCompatibility,
		&row.schedulable, &row.availabilityScheduleJSON, &row.accountExpiresAt,
		&row.cooldownUntil, &row.lastErrorCode, &row.lastErrorMessage,
		&row.lastErrorTraceID, &row.lastUsedAt, &row.healthCheckModel,
		&row.healthCheckEndpointMode, &row.proxyProfileID, &row.proxyProfileName,
		&row.proxyProfileType, &row.proxyProfileEnabled,
		&row.bindingSystemAccountID, &row.boundGroupID, &row.boundGroupName,
		&row.authorizationID, &row.sourceAccountID, &row.authorizationEffectiveSourceType,
		&row.sourceProviderCode, &row.sourceProviderName, &row.sourceProviderProtocolProfileID,
		&row.sourceProtocolCode, &row.sourceProtocolVersion, &row.sourceAccountType,
		&row.sourceProxyProfileID, &row.sourceConcurrencyLimit, &row.sourceClientCompatibility,
		&row.resolvedSourceProxyProfileID, &row.sourceProxyProfileName,
		&row.sourceProxyProfileType, &row.sourceProxyProfileEnabled,
		&row.boundGroupLocalPriority, &row.boundGroupLocalSuperPriorityEnabled,
		&row.boundGroupLocalFallbackEnabled,
	)
	return row, err
}

// listFilters mirrors accountManagementListFilters (owner mode: the source
// account COALESCE pair collapses to the plain column). authorized carries
// the M10 authorized instance account ids: they pass the owner scope filter
// through an id IN clause (access_type='authorized' rows).
func (s *Store) listFilters(options NormalizedListOptions, scoped, now string, authorized map[string]bool, args *[]any) string {
	clauses := []string{"accounts.deleted_at IS NULL"}
	// Node account-management-list.repository.ts:331-334: revoke and return
	// only flip the runtime authorization row status, so the stamped instance
	// is hidden through this unconditional status guard (admin and self alike;
	// the status set is literal in Node, no bind params).
	clauses = append(clauses,
		"(accounts.authorization_instance_authorization_id IS NULL OR authorizations.status IN ('active', 'paused', 'expired'))")
	if scoped != "" {
		if ids := authorizedIDList(authorized); len(ids) > 0 {
			clauses = append(clauses, "(accounts.system_account_id = ? OR accounts.id IN ("+placeholders(len(ids))+"))")
			*args = append(*args, scoped)
			*args = append(*args, anySlice(ids)...)
		} else {
			clauses = append(clauses, "accounts.system_account_id = ?")
			*args = append(*args, scoped)
		}
	}
	if len(options.IDs) > 0 {
		clauses = append(clauses, "accounts.id IN ("+placeholders(len(options.IDs))+")")
		*args = append(*args, anySlice(options.IDs)...)
	}
	keyword := normalizeAccountNameSearchText(options.Keyword)
	if keyword != "" {
		keywordClauses := []string{"(accounts.name >= ? AND accounts.name < ?)"}
		*args = append(*args, keyword, textPrefixUpperBound(keyword))
		if terms := accountNameSearchQueryTerms(options.Keyword); len(terms) > 0 {
			keywordClauses = append(keywordClauses, `accounts.id IN (
				SELECT search.account_id
				FROM `+s.table("account_name_search_terms")+` search
				INNER JOIN `+s.table("account_name_search_documents")+` documents
					ON documents.account_id = search.account_id
				WHERE instr(documents.normalized_name, ?) > 0
					AND search.term IN (`+placeholders(len(terms))+`)
				GROUP BY search.account_id
				HAVING COUNT(DISTINCT search.term) = ?)`)
			*args = append(*args, keyword)
			*args = append(*args, anySlice(terms)...)
			*args = append(*args, len(terms))
		}
		clauses = append(clauses, "("+strings.Join(keywordClauses, " OR ")+")")
	}
	if options.ProviderCode != "" && options.ProviderCode != "all" {
		clauses = append(clauses, "accounts.provider_code = ?")
		*args = append(*args, options.ProviderCode)
	}
	if options.ProviderProtocolProfileID != "" && options.ProviderProtocolProfileID != "all" {
		clauses = append(clauses, "accounts.provider_protocol_profile_id = ?")
		*args = append(*args, options.ProviderProtocolProfileID)
	}
	if options.GroupID != "" {
		clauses = append(clauses, "group_bindings.group_id = ?")
		*args = append(*args, options.GroupID)
	}
	if len(options.TagIDs) > 0 {
		clauses = append(clauses, `EXISTS (
			SELECT 1
			FROM `+s.table("account_tag_bindings")+` tag_filter
			WHERE tag_filter.account_id = accounts.id
				AND tag_filter.system_account_id = accounts.system_account_id
				AND tag_filter.tag_id IN (`+placeholders(len(options.TagIDs))+`))`)
		*args = append(*args, anySlice(options.TagIDs)...)
	}
	if options.Type != "" && options.Type != "all" {
		clauses = append(clauses, "accounts.type = ?")
		*args = append(*args, options.Type)
	}
	effective := ownerEffectiveStatusSQL("accounts", sqlQuoteISO(now))
	statuses := accountStatusFilterValues(options.Status)
	if len(statuses) > 0 {
		clauses = append(clauses, effective+" IN ("+placeholders(len(statuses))+")")
		*args = append(*args, anySlice(statuses)...)
	}
	switch options.Schedulable {
	case "enabled":
		clauses = append(clauses, effective+" = 'active'")
	case "disabled":
		clauses = append(clauses, effective+" NOT IN ('active', 'rate_limited', 'temporary_unavailable')")
	case "cooling":
		clauses = append(clauses, effective+" IN ('rate_limited', 'temporary_unavailable')")
	}
	return " WHERE " + strings.Join(clauses, " AND ")
}

// listOrderClause mirrors accountManagementListOrderClause +
// accountManagementSortColumn (owner mode).
func (s *Store) listOrderClause(options NormalizedListOptions) string {
	now := sqlQuoteISO(isoMillis(s.now()))
	parts := []string{}
	for _, sort := range options.Sorts {
		direction := "ASC"
		if sort.Order == "desc" {
			direction = "DESC"
		}
		column := listSortColumn(sort.Field, now)
		if sort.Field == "lastUsedAt" {
			parts = append(parts, "CASE WHEN "+column+" IS NULL THEN 1 ELSE 0 END ASC", column+" "+direction)
			continue
		}
		parts = append(parts, column+" "+direction)
	}
	return " ORDER BY " + strings.Join(append(parts, "accounts.created_at ASC", "accounts.id ASC"), ", ")
}

func listSortColumn(field, nowLiteral string) string {
	switch field {
	case "priority":
		return "accounts.priority"
	case "superPriority":
		return "accounts.super_priority_enabled"
	case "fallback":
		return "accounts.fallback_enabled"
	case "name":
		return "accounts.name"
	case "type":
		return "accounts.type"
	case "providerCode":
		return "accounts.provider_code"
	case "systemAccount":
		return "COALESCE(system_accounts_ref.display_name, system_accounts_ref.username, accounts.system_account_id)"
	case "concurrency":
		return "accounts.concurrency_limit"
	case "status":
		return statusRankSQL(ownerEffectiveStatusSQL("accounts", nowLiteral))
	case "accountExpiresAt":
		return "accounts.account_expires_at"
	case "lastUsedAt":
		return "accounts.last_used_at"
	default:
		return "accounts.priority"
	}
}

// ListPage mirrors listAccountManagementItemsPageAsync + the hydrate step
// (owner mode): scope + filters, pinned sort order, pageSize+1 probe, the
// paged total upper bound, tag and lock hydration.
func (s *Store) ListPage(ctx context.Context, access AccessScope, options ListOptions) (*ListPageResult, error) {
	ctx = ensureCtx(ctx)
	normalized := normalizeListOptions(options)
	scoped := access.manageableID()
	now := isoMillis(s.now())
	authorized := s.authorizedReadableIDs(ctx, access)
	cte, joins := s.listJoins()
	args := []any{}
	where := s.listFilters(normalized, scoped, now, authorized, &args)
	order := s.listOrderClause(normalized)
	args = append(args, normalized.PageSize+1, (normalized.Page-1)*normalized.PageSize)
	rows, err := s.db.QueryContext(ctx, s.bind(cte+`SELECT `+strings.Join(listItemColumns("accounts"), ", ")+`
		FROM `+s.table("accounts")+` accounts`+joins+where+order+`
		LIMIT ? OFFSET ?`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []listRow{}
	for rows.Next() {
		row, scanErr := scanListRow(rows.Scan)
		if scanErr != nil {
			return nil, scanErr
		}
		records = append(records, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	hasMore := len(records) > normalized.PageSize
	if hasMore {
		records = records[:normalized.PageSize]
	}
	items := make([]ListItem, 0, len(records))
	ids := make([]string, 0, len(records))
	for _, row := range records {
		items = append(items, s.newListItem(row, access, authorized[row.id]))
		ids = append(ids, row.id)
	}
	if err := s.hydrateTags(ctx, items, ids); err != nil {
		return nil, err
	}
	if err := s.hydrateLockStates(ctx, items, ids); err != nil {
		return nil, err
	}
	total := (normalized.Page-1)*normalized.PageSize + len(items)
	if hasMore {
		total++
	}
	return &ListPageResult{
		Items: items, Total: total, HasMore: hasMore,
		Page: normalized.Page, PageSize: normalized.PageSize,
		GeneratedAt: isoMillis(s.now()),
	}, nil
}

func placeholders(count int) string {
	if count <= 0 {
		return "?"
	}
	parts := make([]string, count)
	for index := range parts {
		parts[index] = "?"
	}
	return strings.Join(parts, ", ")
}

func anySlice(values []string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

// newListItem mirrors accountManagementListItemFromRow plus the hydrate
// status/schedulable/availability fields. authorized marks the M10
// authorized-instance rows: accessType flips to 'authorized' with the
// authorized permission set (use/lock, plus return for manual sources), and
// the frozen instance snapshot is replaced by the source account's live
// values and the bound group's local scheduling overrides
// (Node account-management-list.repository.ts:501-555). The instance stamp
// fields are row facts and surface for both access types; they stay omitted
// on owner rows (NULL stamp, Node :568-569).
func (s *Store) newListItem(row listRow, access AccessScope, authorized bool) ListItem {
	now := s.now()
	item := ListItem{
		ID:                        row.id,
		ConfigRevision:            row.configRevision,
		OwnerSystemAccountID:      row.systemAccountID,
		ProviderCode:              row.providerCode,
		ProviderName:              nullPtrString(row.providerName),
		ProviderProtocolProfileID: row.providerProtocolProfileID,
		ProtocolCode:              row.protocolCode,
		ProtocolVersion:           row.protocolVersion,
		Name:                      row.name,
		Notes:                     nullPtrString(row.notes),
		Type:                      row.accountType,
		Status:                    row.status,
		ConcurrencyLimit:          row.concurrencyLimit,
		Priority:                  row.priority,
		SuperPriorityEnabled:      row.superPriorityEnabled == 1,
		FallbackEnabled:           row.fallbackEnabled == 1,
		ClientCompatibility:       normalizeClientCompatibility(row.clientCompatibility),
		Tags:                      []TagSummary{},
		HealthCheckModel:          strings.TrimSpace(row.healthCheckModel),
		HealthCheckEndpointMode:   row.healthCheckEndpointMode,
		Schedulable:               row.schedulable == 1,
		AvailabilitySchedule:      parseScheduleOrNull(row.availabilityScheduleJSON),
		AccountExpiresAt:          nullPtrString(row.accountExpiresAt),
		CooldownUntil:             nullPtrString(row.cooldownUntil),
		LastErrorCode:             nullPtrString(row.lastErrorCode),
		LastErrorMessage:          nullPtrString(row.lastErrorMessage),
		LastErrorTraceID:          nullPtrString(row.lastErrorTraceID),
		LastUsedAt:                nullPtrString(row.lastUsedAt),
		AccessType:                "owner",
		Permissions:               ownerPermissions(),
		TodayUsage:                emptyUsageSummary(),
		Usage:                     emptyUsageSummary(),
	}
	if authorized {
		item.AccessType = "authorized"
		item.Permissions = authorizedPermissions(nullPtrString(row.authorizationEffectiveSourceType))
		// Node account-management-list.repository.ts:501-523: the source
		// account's live values replace the frozen instance snapshot, each
		// field falling back to the instance value when the source join misses.
		if row.sourceProviderCode.Valid && row.sourceProviderCode.String != "" {
			item.ProviderCode = row.sourceProviderCode.String
			// Node resolves provider_name through
			// COALESCE(source_provider_code, provider_code) (:453-454), so a
			// missing source provider row renders without a display name.
			item.ProviderName = nullPtrString(row.sourceProviderName)
		}
		if row.sourceProviderProtocolProfileID.Valid && row.sourceProviderProtocolProfileID.String != "" {
			item.ProviderProtocolProfileID = row.sourceProviderProtocolProfileID.String
		}
		if row.sourceProtocolCode.Valid && row.sourceProtocolCode.String != "" {
			item.ProtocolCode = row.sourceProtocolCode.String
		}
		if row.sourceProtocolVersion.Valid && row.sourceProtocolVersion.String != "" {
			item.ProtocolVersion = row.sourceProtocolVersion.String
		}
		if row.sourceAccountType.Valid && row.sourceAccountType.String != "" {
			item.Type = row.sourceAccountType.String
		}
		if row.sourceConcurrencyLimit.Valid {
			item.ConcurrencyLimit = int(row.sourceConcurrencyLimit.Int64)
		}
		if row.sourceClientCompatibility.Valid {
			item.ClientCompatibility = normalizeClientCompatibility(row.sourceClientCompatibility.String)
		}
		// Node :547-555: the bound group's local scheduling values take over —
		// priority keeps the instance fallback while the flags render NULL as
		// false without a fallback.
		if row.boundGroupLocalPriority.Valid {
			item.Priority = int(row.boundGroupLocalPriority.Int64)
		}
		item.SuperPriorityEnabled = row.boundGroupLocalSuperPriorityEnabled.Int64 == 1
		item.FallbackEnabled = row.boundGroupLocalFallbackEnabled.Int64 == 1
	}
	item.AccountAuthorizationID = nullPtrString(row.authorizationID)
	item.AuthorizationInstanceSourceAccountID = nullPtrString(row.sourceAccountID)
	if access.canAccessAll() {
		id := row.systemAccountID
		item.SystemAccountID = &id
		item.SystemAccountName = nullPtrString(row.systemAccountName)
	}
	item.OwnerSystemAccountName = nullPtrString(row.systemAccountName)
	if row.boundGroupID.Valid && row.boundGroupID.String != "" {
		item.BoundGroupID = &row.boundGroupID.String
		item.BoundGroupName = nullPtrString(row.boundGroupName)
		status := groupBindStatus(row)
		item.GroupBindStatus = &status
		binding := row.bindingSystemAccountID.String
		if binding != "" {
			item.BindingSystemAccountID = &binding
		}
	}
	if authorized {
		// Node :525-528 + :561-565: the authorized proxy id is source-only
		// while the display row resolves through
		// COALESCE(source_proxy_profile_id, configured_proxy_profile_id)
		// (:451-452). With no source proxy the display row falls back to the
		// configured profile and the id stays undefined.
		if row.sourceProxyProfileID.Valid && row.sourceProxyProfileID.String != "" {
			proxyID := row.sourceProxyProfileID.String
			item.ProxyProfileID = &proxyID
			resolved := row.resolvedSourceProxyProfileID.Valid && row.resolvedSourceProxyProfileID.String != ""
			if resolved {
				item.ProxyProfileName = nullPtrString(row.sourceProxyProfileName)
				if proxyType := normalizedProxyType(row.sourceProxyProfileType); proxyType != nil {
					item.ProxyProfileType = proxyType
				}
				if row.sourceProxyProfileEnabled.Valid {
					enabled := row.sourceProxyProfileEnabled.Int64 == 1
					item.ProxyProfileEnabled = &enabled
				}
			}
			unavailable := !resolved || !row.sourceProxyProfileEnabled.Valid || row.sourceProxyProfileEnabled.Int64 != 1
			if unavailable {
				item.ProxyProfileUnavailable = &unavailable
				if access.canAccessAll() {
					message := "代理不存在或已停用，请选择一个已启用的代理"
					item.ProxyProfileErrorMessage = &message
				}
			}
		} else if row.proxyProfileID.Valid && row.proxyProfileID.String != "" {
			item.ProxyProfileName = nullPtrString(row.proxyProfileName)
			if proxyType := normalizedProxyType(row.proxyProfileType); proxyType != nil {
				item.ProxyProfileType = proxyType
			}
			if row.proxyProfileEnabled.Valid {
				enabled := row.proxyProfileEnabled.Int64 == 1
				item.ProxyProfileEnabled = &enabled
			}
		}
	} else if row.proxyProfileID.Valid && row.proxyProfileID.String != "" {
		item.ProxyProfileID = &row.proxyProfileID.String
		item.ProxyProfileName = nullPtrString(row.proxyProfileName)
		if proxyType := normalizedProxyType(row.proxyProfileType); proxyType != nil {
			item.ProxyProfileType = proxyType
		}
		enabled := row.proxyProfileEnabled.Int64 == 1 && row.proxyProfileEnabled.Valid
		item.ProxyProfileEnabled = &enabled
		unavailable := !row.proxyProfileEnabled.Valid || row.proxyProfileEnabled.Int64 != 1
		if unavailable {
			item.ProxyProfileUnavailable = &unavailable
			if access.canAccessAll() {
				message := "代理不存在或已停用，请选择一个已启用的代理"
				item.ProxyProfileErrorMessage = &message
			}
		}
	}
	item.EffectiveAvailability = ownerEffectiveAvailability(item, now)
	return item
}

func groupBindStatus(row listRow) string {
	if row.bindingSystemAccountID.Valid && row.bindingSystemAccountID.String != row.systemAccountID {
		return ""
	}
	return "bound"
}

func normalizedProxyType(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	switch value.String {
	case "http", "https", "socks5", "socks5h":
		text := value.String
		return &text
	default:
		return nil
	}
}

func normalizeClientCompatibility(value string) string {
	if value == "codex_responses" {
		return value
	}
	return "openai_standard"
}

func parseScheduleOrNull(raw sql.NullString) *AvailabilitySchedule {
	schedule, err := ParseScheduleJSON(raw.String)
	if err != nil {
		return nil
	}
	return schedule
}

// hydrateTags mirrors loadAccountTagsByAccountIdsAsync.
func (s *Store) hydrateTags(ctx context.Context, items []ListItem, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT account_tag_bindings.account_id,
			account_tags.id, account_tags.name
		FROM `+s.table("account_tag_bindings")+` account_tag_bindings
		INNER JOIN `+s.table("account_tags")+` account_tags
			ON account_tags.id = account_tag_bindings.tag_id
		WHERE account_tag_bindings.account_id IN (`+placeholders(len(ids))+`)
		ORDER BY account_tags.name ASC, account_tags.id ASC`), anySlice(ids)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	byAccount := map[string][]TagSummary{}
	for rows.Next() {
		var accountID, tagID, name string
		if err := rows.Scan(&accountID, &tagID, &name); err != nil {
			return err
		}
		byAccount[accountID] = append(byAccount[accountID], TagSummary{ID: tagID, Name: name})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for index := range items {
		if tags, ok := byAccount[items[index].ID]; ok {
			items[index].Tags = tags
		}
	}
	return nil
}

// hydrateLockStates mirrors listAccountLockStatesAsync (list projection).
func (s *Store) hydrateLockStates(ctx context.Context, items []ListItem, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT account_id, enabled, lock_state,
			lock_death_timeout_seconds, lock_retry_interval_seconds
		FROM `+s.table("account_lock_states")+`
		WHERE account_id IN (`+placeholders(len(ids))+`)`), anySlice(ids)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	byAccount := map[string]LockStatePublic{}
	for rows.Next() {
		var accountID, lockState string
		var enabled, deathTimeout, retryInterval int64
		if err := rows.Scan(&accountID, &enabled, &lockState, &deathTimeout, &retryInterval); err != nil {
			return err
		}
		lockEnabled := enabled == 1
		death := normalizeLockRange(int(deathTimeout), defaultLockDeathTimeoutSeconds, 30, 3600)
		retry := normalizeLockRange(int(retryInterval), defaultLockRetryIntervalSeconds, 5, 30)
		byAccount[accountID] = LockStatePublic{
			LockEnabled:              &lockEnabled,
			LockState:                &lockState,
			LockDeathTimeoutSeconds:  &death,
			LockRetryIntervalSeconds: &retry,
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for index := range items {
		if lock, ok := byAccount[items[index].ID]; ok {
			items[index].LockStatePublic = lock
		}
	}
	return nil
}

func normalizeLockRange(value, fallback, min, max int) int {
	if value < min || value > max {
		return fallback
	}
	return value
}

// ownerEffectiveAvailability mirrors accountEffectiveAvailability for owner
// rows without runtime overlays (the instance branch only).
func ownerEffectiveAvailability(item ListItem, now time.Time) EffectiveAvailability {
	expired := false
	if item.AccountExpiresAt != nil {
		if parsed, err := time.Parse(time.RFC3339Nano, *item.AccountExpiresAt); err == nil {
			expired = parsed.UnixMilli() <= now.UnixMilli()
		}
	}
	if item.LastErrorCode != nil && *item.LastErrorCode == "account_expired" || expired {
		return blockedAvailability("instance_expired", "账户到期", "red", "account", "账户已到期，当前不可用", nil)
	}
	nowMillis := now.UnixMilli()
	cooldownFuture := false
	var retryAt *string
	if item.CooldownUntil != nil {
		if parsed, err := time.Parse(time.RFC3339Nano, *item.CooldownUntil); err == nil && parsed.UnixMilli() > nowMillis {
			cooldownFuture = true
			retryAt = item.CooldownUntil
		}
	}
	switch item.Status {
	case "disabled":
		return blockedAvailability("instance_disabled", "账户停用", "default", "account", "账户已停用，当前不可用", nil)
	case "pending_test":
		return blockedAvailability("instance_pending_test", "账户待检查", "blue", "account", "账户正在等待后台健康检查，检查通过前不会参与调度", nil)
	case "error":
		reason := "账户处于异常状态，当前不可用"
		if item.LastErrorMessage != nil {
			reason = *item.LastErrorMessage
		}
		return blockedAvailability("instance_error", "账户异常", "red", "account", reason, nil)
	case "rate_limited":
		reason := "账户限流中，恢复前不会参与调度"
		if item.LastErrorMessage != nil {
			reason = *item.LastErrorMessage
		}
		return blockedAvailability("instance_rate_limited", "账户限流中", "orange", "account", reason, nil)
	case "temporary_unavailable":
		reason := "账户临时不可调用，恢复前不会参与调度"
		if item.LastErrorMessage != nil {
			reason = *item.LastErrorMessage
		}
		return blockedAvailability("instance_temporary_unavailable", "账户临时不可调用", "gold", "account", reason, retryAt)
	case "quality_isolated":
		reason := "账户因模型质量不达标已隔离，质量恢复检查通过前不会参与调度"
		if item.LastErrorMessage != nil {
			reason = *item.LastErrorMessage
		}
		return blockedAvailability("instance_quality_isolated", "账户质量隔离", "red", "account", reason, nil)
	}
	if cooldownFuture {
		return blockedAvailability("instance_cooldown", "账户冷却", "gold", "account", "账户正在冷却，恢复前不会参与调度", retryAt)
	}
	if !item.Schedulable {
		return blockedAvailability("instance_unschedulable", "账户停调", "orange", "account", "账户暂时不可调用，恢复前不会参与调度", nil)
	}
	return EffectiveAvailability{Available: true, Status: "available", Label: "可调度", Color: "green"}
}

func blockedAvailability(status, label, color, scope, reason string, retryAt *string) EffectiveAvailability {
	return EffectiveAvailability{
		Available: false, Status: status, Label: label, Color: color,
		BlockerScope: &scope, Reason: &reason, RetryAt: retryAt,
	}
}

// OptionSummary mirrors AccountOptionSummary (owner mode).
type OptionSummary struct {
	ID                        string      `json:"id"`
	SystemAccountID           *string     `json:"systemAccountId,omitempty"`
	SystemAccountName         *string     `json:"systemAccountName,omitempty"`
	OwnerSystemAccountID      string      `json:"ownerSystemAccountId"`
	OwnerSystemAccountName    *string     `json:"ownerSystemAccountName,omitempty"`
	ProviderCode              string      `json:"providerCode"`
	ProviderProtocolProfileID string      `json:"providerProtocolProfileId"`
	ProtocolCode              string      `json:"protocolCode"`
	ProtocolVersion           string      `json:"protocolVersion"`
	Name                      string      `json:"name"`
	Type                      string      `json:"type"`
	Status                    string      `json:"status"`
	AccessType                string      `json:"accessType"`
	AccountExpiresAt          *string     `json:"accountExpiresAt,omitempty"`
	Permissions               Permissions `json:"permissions"`
}

// ListOptionsPage mirrors listAccountOptionsAsync (owner rows only).
func (s *Store) ListOptionSummaries(ctx context.Context, access AccessScope, options ListOptions) ([]OptionSummary, error) {
	ctx = ensureCtx(ctx)
	scoped := access.manageableID()
	if scoped == "" && !access.canAccessAll() {
		return nil, &ValidationError{Message: "缺少系统账户上下文"}
	}
	// normalizeAccountOptionListOptions: pageSize := limit (1..50, default 50),
	// sorts cleared.
	options.PageSize = minInt(maxAccountOptionPageSize, maxInt(1, options.PageSize))
	if options.PageSize == 0 {
		options.PageSize = maxAccountOptionPageSize
	}
	normalized := normalizeListOptions(options)
	now := isoMillis(s.now())
	clauses := []string{"accounts.deleted_at IS NULL", "accounts.authorization_instance_authorization_id IS NULL"}
	args := []any{}
	if scoped != "" {
		clauses = append(clauses, "accounts.system_account_id = ?")
		args = append(args, scoped)
	}
	if len(normalized.IDs) > 0 {
		clauses = append(clauses, "accounts.id IN ("+placeholders(len(normalized.IDs))+")")
		args = append(args, anySlice(normalized.IDs)...)
	}
	keyword := normalizeAccountNameSearchText(normalized.Keyword)
	if keyword != "" {
		clauses = append(clauses, "(accounts.name >= ? AND accounts.name < ?)")
		args = append(args, keyword, textPrefixUpperBound(keyword))
	}
	if normalized.ProviderCode != "" && normalized.ProviderCode != "all" {
		clauses = append(clauses, "accounts.provider_code = ?")
		args = append(args, normalized.ProviderCode)
	}
	if normalized.ProviderProtocolProfileID != "" && normalized.ProviderProtocolProfileID != "all" {
		clauses = append(clauses, "accounts.provider_protocol_profile_id = ?")
		args = append(args, normalized.ProviderProtocolProfileID)
	}
	if normalized.GroupID != "" {
		clauses = append(clauses, `accounts.id IN (
			SELECT option_group_accounts.account_id
			FROM `+s.table("group_accounts")+` option_group_accounts
			WHERE option_group_accounts.group_id = ?
				AND option_group_accounts.enabled = 1`+func() string {
			if scoped != "" {
				return " AND option_group_accounts.system_account_id = ?"
			}
			return ""
		}()+`)`)
		if scoped != "" {
			args = append(args, scoped)
		}
		args = append(args, normalized.GroupID)
	}
	if len(normalized.TagIDs) > 0 {
		clauses = append(clauses, `EXISTS (
			SELECT 1
			FROM `+s.table("account_tag_bindings")+` option_tag_bindings
			WHERE option_tag_bindings.account_id = accounts.id
				AND option_tag_bindings.system_account_id = accounts.system_account_id
				AND option_tag_bindings.tag_id IN (`+placeholders(len(normalized.TagIDs))+`))`)
		args = append(args, anySlice(normalized.TagIDs)...)
	}
	if normalized.Type != "" && normalized.Type != "all" {
		clauses = append(clauses, "accounts.type = ?")
		args = append(args, normalized.Type)
	}
	effective := ownerEffectiveStatusSQL("accounts", sqlQuoteISO(now))
	statuses := accountStatusFilterValues(normalized.Status)
	if len(statuses) > 0 {
		clauses = append(clauses, effective+" IN ("+placeholders(len(statuses))+")")
		args = append(args, anySlice(statuses)...)
	}
	switch normalized.Schedulable {
	case "enabled":
		clauses = append(clauses, `accounts.status = 'active'
			AND accounts.schedulable = 1
			AND (accounts.cooldown_until IS NULL OR accounts.cooldown_until <= `+sqlQuoteISO(now)+`)
			AND (accounts.account_expires_at IS NULL OR accounts.account_expires_at > `+sqlQuoteISO(now)+`)
			AND (accounts.last_error_code IS NULL OR accounts.last_error_code <> 'account_expired')`)
	case "disabled":
		clauses = append(clauses, `(accounts.status = 'disabled'
			OR accounts.schedulable <> 1
			OR accounts.last_error_code = 'account_expired'
			OR (accounts.account_expires_at IS NOT NULL AND accounts.account_expires_at <= `+sqlQuoteISO(now)+`))`)
	case "cooling":
		clauses = append(clauses, `(accounts.status IN ('rate_limited', 'temporary_unavailable')
			OR (accounts.cooldown_until IS NOT NULL AND accounts.cooldown_until > `+sqlQuoteISO(now)+`))`)
	}
	args = append(args, normalized.PageSize, (normalized.Page-1)*normalized.PageSize)
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT accounts.id, accounts.system_account_id,
			COALESCE(system_accounts_ref.display_name, system_accounts_ref.username, accounts.system_account_id) AS system_account_name,
			accounts.provider_code, accounts.provider_protocol_profile_id, accounts.protocol_code,
			accounts.protocol_version, accounts.name, accounts.type,
			`+effective+` AS effective_status, accounts.account_expires_at
		FROM `+s.table("accounts")+` accounts
		LEFT JOIN `+s.table("system_accounts")+` system_accounts_ref
			ON system_accounts_ref.id = accounts.system_account_id
		WHERE `+strings.Join(clauses, " AND ")+`
		ORDER BY accounts.priority ASC, accounts.created_at ASC, accounts.id ASC
		LIMIT ? OFFSET ?`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	summaries := []OptionSummary{}
	for rows.Next() {
		var summary OptionSummary
		var systemAccountID, effectiveStatus string
		var systemAccountName sql.NullString
		var accountExpiresAt sql.NullString
		if err := rows.Scan(&summary.ID, &systemAccountID, &systemAccountName,
			&summary.ProviderCode, &summary.ProviderProtocolProfileID, &summary.ProtocolCode,
			&summary.ProtocolVersion, &summary.Name, &summary.Type,
			&effectiveStatus, &accountExpiresAt); err != nil {
			return nil, err
		}
		summary.OwnerSystemAccountID = systemAccountID
		summary.OwnerSystemAccountName = nullPtrString(systemAccountName)
		summary.Status = effectiveStatus
		summary.AccessType = "owner"
		summary.AccountExpiresAt = nullPtrString(accountExpiresAt)
		summary.Permissions = ownerPermissions()
		if access.canAccessAll() {
			summary.SystemAccountID = &systemAccountID
			summary.SystemAccountName = nullPtrString(systemAccountName)
		}
		summaries = append(summaries, summary)
	}
	return summaries, rows.Err()
}

// EditBasicDetail mirrors AccountEditBasicDetail. Credential secret fields are
// masked on this surface: the Go gateway never returns clear-text credentials.
type EditBasicDetail struct {
	ID                        string       `json:"id"`
	ConfigRevision            int64        `json:"configRevision"`
	SystemAccountID           *string      `json:"systemAccountId,omitempty"`
	OwnerSystemAccountID      string       `json:"ownerSystemAccountId"`
	ProviderCode              string       `json:"providerCode"`
	ProviderProtocolProfileID string       `json:"providerProtocolProfileId"`
	ProtocolCode              string       `json:"protocolCode"`
	ProtocolVersion           string       `json:"protocolVersion"`
	Name                      string       `json:"name"`
	Notes                     *string      `json:"notes,omitempty"`
	Type                      string       `json:"type"`
	Credentials               Credentials  `json:"credentials"`
	Status                    string       `json:"status"`
	ConcurrencyLimit          int          `json:"concurrencyLimit"`
	Priority                  int          `json:"priority"`
	SuperPriorityEnabled      bool         `json:"superPriorityEnabled"`
	FallbackEnabled           bool         `json:"fallbackEnabled"`
	ClientCompatibility       string       `json:"clientCompatibility"`
	SupportedModels           []string     `json:"supportedModels"`
	Tags                      []TagSummary `json:"tags"`
	HealthCheckModel          string       `json:"healthCheckModel"`
	HealthCheckEndpointMode   string       `json:"healthCheckEndpointMode"`
	BoundGroupID              *string      `json:"boundGroupId,omitempty"`
	BoundGroupName            *string      `json:"boundGroupName,omitempty"`
}

// Credentials mirrors AccountCredentials: an open record of credential fields.
type Credentials map[string]any

// editBasicForbiddenError mirrors AccountEditBasicForbiddenError.
type editBasicForbiddenError struct{}

func (e *editBasicForbiddenError) Error() string { return "无权查看账户凭据" }

// FindEditBasicDetail mirrors findAccountEditBasicDetailAsync: the scope
// checked owner row plus the projected editable credentials, supported models
// and tags. Returns (nil, nil) when the account is missing or outside the
// access scope (route renders 404 账户不存在).
func (s *Store) FindEditBasicDetail(ctx context.Context, accountID string, access AccessScope) (*EditBasicDetail, error) {
	ctx = ensureCtx(ctx)
	id := strings.TrimSpace(accountID)
	if id == "" {
		return nil, nil
	}
	// M10 authorized-instance pass-through: an authorized instance account is
	// found outside the owner scope so the grantee reaches the same reserved
	// branch Node renders for every instance row. The pass-through never
	// widens visibility: the instance branch below denies the credential
	// surface unconditionally (Node account-edit-basic.repository.ts:149-151
	// throws AccountEditBasicForbiddenError for any instance row, regardless
	// of the runtime authorization status — revoke/return only flip the
	// authorization row, resource-authorization-write.repository.ts:986-992,
	// so a revoked stamped instance still renders 403 here, matching Node).
	authorized := s.authorizedReadableIDs(ctx, access)[id]
	scopeClause := ""
	args := []any{id}
	if scoped := access.manageableID(); scoped != "" && !authorized {
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
		credentialsEncrypted      string
		status                    string
		concurrencyLimit          int
		priority                  int
		superPriorityEnabled      int
		fallbackEnabled           int
		clientCompatibility       string
		healthCheckModel          string
		healthCheckEndpointMode   string
		authorizationID           sql.NullString
		sourceAccountID           sql.NullString
		boundGroupID              sql.NullString
		boundGroupName            sql.NullString
	}
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT accounts.id, accounts.config_revision,
			accounts.system_account_id, accounts.provider_code, accounts.provider_protocol_profile_id,
			accounts.protocol_code, accounts.protocol_version, accounts.name, accounts.notes,
			accounts.type, accounts.credentials_encrypted, accounts.status, accounts.concurrency_limit,
			accounts.priority, accounts.super_priority_enabled, accounts.fallback_enabled,
			accounts.client_compatibility, accounts.health_check_model, accounts.health_check_endpoint_mode,
			accounts.authorization_instance_authorization_id, accounts.authorization_instance_source_account_id,
			(
				SELECT group_accounts.group_id
				FROM `+s.table("group_accounts")+` group_accounts
				WHERE group_accounts.account_id = accounts.id
					AND group_accounts.system_account_id = accounts.system_account_id
					AND group_accounts.enabled = 1
				ORDER BY group_accounts.updated_at DESC, group_accounts.group_id ASC
				LIMIT 1
			) AS bound_group_id,
			(
				SELECT bound_groups.name
				FROM `+s.table("group_accounts")+` group_accounts
				INNER JOIN `+s.table("groups")+` bound_groups
					ON bound_groups.id = group_accounts.group_id
				WHERE group_accounts.account_id = accounts.id
					AND group_accounts.system_account_id = accounts.system_account_id
					AND group_accounts.enabled = 1
				ORDER BY group_accounts.updated_at DESC, group_accounts.group_id ASC
				LIMIT 1
			) AS bound_group_name
		FROM `+s.table("accounts")+` accounts
		WHERE accounts.id = ?
			AND accounts.deleted_at IS NULL`+scopeClause+`
		LIMIT 1`), args...).Scan(
		&row.id, &row.configRevision, &row.systemAccountID, &row.providerCode,
		&row.providerProtocolProfileID, &row.protocolCode, &row.protocolVersion,
		&row.name, &row.notes, &row.accountType, &row.credentialsEncrypted,
		&row.status, &row.concurrencyLimit, &row.priority, &row.superPriorityEnabled,
		&row.fallbackEnabled, &row.clientCompatibility, &row.healthCheckModel,
		&row.healthCheckEndpointMode, &row.authorizationID, &row.sourceAccountID,
		&row.boundGroupID, &row.boundGroupName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// canManageResourceOwner (Node account-edit-basic.repository.ts:148):
	// users may only manage their own rows; M10 authorized instance accounts
	// stay visible (the instance branch below renders the reserved 403) but
	// never manageable.
	if !access.canAccessAll() && row.systemAccountID != access.ViewerID && !authorized {
		return nil, nil
	}
	if row.authorizationID.Valid && row.authorizationID.String != "" ||
		row.sourceAccountID.Valid && row.sourceAccountID.String != "" {
		return nil, &editBasicForbiddenError{}
	}
	models := []string{}
	modelRows, err := s.db.QueryContext(ctx, s.bind(`SELECT model FROM `+s.table("account_supported_models")+`
		WHERE account_id = ? ORDER BY model ASC`), row.id)
	if err != nil {
		return nil, err
	}
	for modelRows.Next() {
		var model string
		if err := modelRows.Scan(&model); err != nil {
			modelRows.Close()
			return nil, err
		}
		models = append(models, model)
	}
	modelRows.Close()
	if err := modelRows.Err(); err != nil {
		return nil, err
	}
	tags := []TagSummary{}
	tagRows, err := s.db.QueryContext(ctx, s.bind(`SELECT account_tags.id, account_tags.name
		FROM `+s.table("account_tag_bindings")+` account_tag_bindings
		INNER JOIN `+s.table("account_tags")+` account_tags
			ON account_tags.id = account_tag_bindings.tag_id
		WHERE account_tag_bindings.account_id = ?
		ORDER BY account_tags.name ASC, account_tags.id ASC`), row.id)
	if err != nil {
		return nil, err
	}
	for tagRows.Next() {
		var tag TagSummary
		if err := tagRows.Scan(&tag.ID, &tag.Name); err != nil {
			tagRows.Close()
			return nil, err
		}
		tags = append(tags, tag)
	}
	tagRows.Close()
	if err := tagRows.Err(); err != nil {
		return nil, err
	}
	var credentials Credentials
	if err := DecryptJSON(s.secret, row.credentialsEncrypted, &credentials); err != nil {
		return nil, err
	}
	detail := &EditBasicDetail{
		ID:                        row.id,
		ConfigRevision:            row.configRevision,
		OwnerSystemAccountID:      row.systemAccountID,
		ProviderCode:              row.providerCode,
		ProviderProtocolProfileID: row.providerProtocolProfileID,
		ProtocolCode:              row.protocolCode,
		ProtocolVersion:           row.protocolVersion,
		Name:                      row.name,
		Notes:                     nullPtrString(row.notes),
		Type:                      row.accountType,
		Credentials:               projectEditableCredentials(row.accountType, credentials),
		Status:                    row.status,
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
	}
	if access.canAccessAll() {
		detail.SystemAccountID = &row.systemAccountID
	}
	return detail, nil
}

// basicEditableCredentialKeys and editableCredentialKeysByAccountType mirror
// account-edit-basic.repository.ts: only these keys surface for editing.
var basicEditableCredentialKeys = []string{"base_url", "supported_endpoint_modes"}

var editableCredentialKeysByAccountType = map[string][]string{
	"api_key":      {"api_key", "api_keys", "api_key_strategy", "api_key_weights"},
	"oauth":        {"access_token", "refresh_token"},
	"google_oauth": {"access_token", "refresh_token", "client_id", "client_secret", "quota_project_id", "oauth_type", "project_id", "tier_id"},
}

// projectEditableCredentials mirrors projectEditableCredentials with the Go
// slice hardening: secret values are masked (MaskSecret) so no clear-text
// credential material leaves the server.
func projectEditableCredentials(accountType string, credentials Credentials) Credentials {
	output := Credentials{}
	for _, key := range append(append([]string{}, basicEditableCredentialKeys...), editableCredentialKeysByAccountType[accountType]...) {
		value, ok := credentials[key]
		if !ok {
			continue
		}
		output[key] = maskCredentialValue(key, value)
	}
	return output
}

var credentialSecretKeys = map[string]bool{
	"api_key": true, "access_token": true, "refresh_token": true,
	"client_secret": true, "identity_token": true, "id_token": true,
}

func maskCredentialValue(key string, value any) any {
	if key == "api_keys" {
		if list, ok := value.([]any); ok {
			masked := make([]any, 0, len(list))
			for _, item := range list {
				masked = append(masked, MaskSecret(item))
			}
			return masked
		}
	}
	if credentialSecretKeys[key] {
		return MaskSecret(value)
	}
	return value
}

// Package groups owns the M05 vertical slice: dual-mode group persistence
// (SQLite + PostgreSQL) and the /groups + /my-groups route family ported from
// backend/src/modules/groups/groups.routes.ts plus the group-* repositories
// under backend/src/storage/. The slice covers owner-scope CRUD with
// optimistic locking (expectedUpdatedAt), default-group and route-strategy
// binding protection, member counts, group_account_stats_dirty marking and
// the gateway runtime invalidation hook. The authorized-group view
// (resource_authorizations + group_authorization_settings overrides) and the
// juhe_stats group_account_stats projection belong to the authorization and
// stats slices; this package marks dirty rows and hands invalidation to the
// inval bus instead.
package groups

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// ConflictError maps to Node GroupPatchConflictError and the duplicate group
// name errors — both render as 409 in the route family.
type ConflictError struct{ Message string }

func (e *ConflictError) Error() string { return e.Message }

// ValidationError maps to Node throw-Error paths rendered as 400
// (DefaultGroupReadonlyError, provider availability, route-strategy guards,
// input normalization).
type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

// RuntimeInvalidator is the K5 gateway runtime cache invalidation port
// (Node notifyGatewayRuntimeCacheInvalidation). *inval.Bus satisfies it; nil
// keeps the slice self-contained with no-op invalidation.
type RuntimeInvalidator interface {
	Invalidate(topic, reason string)
}

// TopicGatewayRuntime mirrors the Node gateway runtime cache topic constant.
const TopicGatewayRuntime = "topic:gateway_runtime_cache"

// maxRouteStrategyAvailabilityLossCandidates mirrors
// route-strategy-group-binding-limits.ts.
const maxRouteStrategyAvailabilityLossCandidates = 100

// AccessScope mirrors storage/access-scope.ts for the owner-view subset this
// slice implements: admins see everything unless a filter is set; users are
// pinned to their own rows (forceSelfAccessScope).
type AccessScope struct {
	ViewerID string
	IsAdmin  bool
	FilterID string
}

// manageableID mirrors manageableSystemAccountId: empty result + admin means
// unscoped (all groups); non-admins always scope to themselves.
func (a AccessScope) manageableID() string {
	if a.IsAdmin {
		return a.FilterID
	}
	return a.ViewerID
}

func (a AccessScope) canAccessAll() bool { return a.IsAdmin }

// writeSystemAccountID mirrors writeSystemAccountId: the owner stamped on
// newly created rows.
func (a AccessScope) writeSystemAccountID() (string, error) {
	if id := a.manageableID(); id != "" {
		return id, nil
	}
	if a.ViewerID != "" {
		return a.ViewerID, nil
	}
	return "", &ValidationError{Message: "缺少系统账户上下文"}
}

// Store is the dual-mode group persistence.
type Store struct {
	db        *sql.DB
	pg        bool
	now       func() time.Time
	newI      func(prefix string) string
	inval     RuntimeInvalidator
	globalMax int
}

// StoreOption adjusts optional store collaborators.
type StoreOption func(*Store)

// WithGlobalConcurrencyMax injects runtimeConfig.concurrency.globalMax
// (JUHE_AI_CONCURRENCY_GLOBAL_MAX). Values below 1 keep the built-in default.
func WithGlobalConcurrencyMax(value int) StoreOption {
	return func(s *Store) {
		if value >= 1 {
			s.globalMax = value
		}
	}
}

// globalConcurrencyMax mirrors runtimeConfig.concurrency.globalMax at the
// DEFAULT policy root (Node reads the runtime config live).
func (s *Store) globalConcurrencyMax() int {
	if s.globalMax >= 1 {
		return s.globalMax
	}
	return defaultGlobalConcurrencyMax
}

// NewStore builds the store; inval may be nil (no-op invalidation until K5
// wires the bus).
func NewStore(db *sql.DB, postgres bool, now func() time.Time, newID func(string) string, inval RuntimeInvalidator, options ...StoreOption) (*Store, error) {
	if db == nil {
		return nil, errors.New("groups store requires a database")
	}
	if now == nil {
		now = time.Now
	}
	if newID == nil {
		newID = func(prefix string) string { return randomID(prefix) }
	}
	store := &Store{db: db, pg: postgres, now: now, newI: newID, inval: inval}
	for _, option := range options {
		option(store)
	}
	return store, nil
}

// randomID mirrors Node newId('grp') (random hex suffix).
func randomID(prefix string) string {
	buf := make([]byte, 12)
	_, _ = rand.Read(buf)
	return prefix + "_" + hex.EncodeToString(buf)
}

func (s *Store) table(name string) string {
	if s.pg {
		return "juhe_business." + name
	}
	return name
}

func (s *Store) bind(query string) string {
	if !s.pg {
		return query
	}
	var out strings.Builder
	index := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			out.WriteString("$" + itoa(index))
			index++
		} else {
			out.WriteByte(query[i])
		}
	}
	return out.String()
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	digits := ""
	for v > 0 {
		digits = string(rune('0'+v%10)) + digits
		v /= 10
	}
	return digits
}

func ensureCtx(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// isoMillis mirrors Node nowIso()/toISOString() millisecond precision.
func isoMillis(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

func (s *Store) nowISO() string { return isoMillis(s.now()) }

// invalidateRuntime mirrors invalidateGatewayRuntimeAfterBusinessWrite.
func (s *Store) invalidateRuntime(reason string) {
	if s.inval != nil {
		s.inval.Invalidate(TopicGatewayRuntime, reason)
	}
}

// AccountStats mirrors the Node GroupAccountStats projection
// (group-account-stats.mapper.ts). The populated variant reads
// juhe_stats.group_account_stats plus the usage/runtime summaries; until that
// wiring lands the slice renders the emptyGroupAccountStats shape.
type AccountStats struct {
	Total              int `json:"total"`
	Available          int `json:"available"`
	Active             int `json:"active"`
	Disabled           int `json:"disabled"`
	Error              int `json:"error"`
	RateLimited        int `json:"rateLimited"`
	CurrentConcurrency int `json:"currentConcurrency"`
	ConcurrencyLimit   int `json:"concurrencyLimit"`
	// TodayUsage/Usage carry the hydrate/summary shapes; nil (the create
	// payload) omits the keys exactly like the Node groupCreateListItem.
	TodayUsage any `json:"todayUsage,omitempty"`
	Usage      any `json:"usage,omitempty"`
}

// emptyAccountUsageSummary mirrors emptyAccountUsageSummary
// (storage/usage-stats-helpers.ts).
func emptyAccountUsageSummary() map[string]any {
	return map[string]any{
		"requestCount":       0,
		"inputTokens":        0,
		"outputTokens":       0,
		"cacheReadTokens":    0,
		"cacheReadCost":      0,
		"cacheWriteTokens":   0,
		"cacheWrite1hTokens": 0,
		"cacheWriteCost":     0,
		"thinkingTokens":     0,
		"inputImageTokens":   0,
		"outputImageTokens":  0,
		"totalTokens":        0,
		"totalCost":          0,
	}
}

// emptyAccountStats mirrors emptyGroupAccountStats.
func emptyAccountStats() AccountStats {
	return AccountStats{TodayUsage: emptyAccountUsageSummary(), Usage: emptyAccountUsageSummary()}
}

// AuthorizationSourceSummary mirrors ResourceAuthorizationSourceSummary as
// sanitized for the authorized viewer (sanitizeAuthorizationSourcesForViewer
// with limited=true: team id, endedAt, revokedBy/revokedAt are dropped and
// createdBy is blanked).
type AuthorizationSourceSummary struct {
	ID             string `json:"id"`
	AuthorizationID string `json:"authorizationId"`
	SourceType     string `json:"sourceType"`
	SourceTeamName *string `json:"sourceTeamName,omitempty"`
	Status         string `json:"status"`
	ActivatedAt    *string `json:"activatedAt,omitempty"`
	EndedReason    *string `json:"endedReason,omitempty"`
	CreatedBy      string `json:"createdBy"`
	CreatedAt      string `json:"createdAt"`
	UpdatedAt      string `json:"updatedAt"`
}

// AuthorizationSourceSummarySummary mirrors GroupListItem['authorizationSourceSummary'].
type AuthorizationSourceSummarySummary struct {
	ActiveSourceCount int      `json:"activeSourceCount"`
	HasManual         bool     `json:"hasManual"`
	HasTeam           bool     `json:"hasTeam"`
	TeamNames         []string `json:"teamNames"`
}

// ListItem mirrors the Node GroupListItem projection (groups table): no
// memberCount/accountIds, authorization fields only on authorized rows, and
// accountStats carrying the hydrate keys (currentConcurrency/todayUsage).
type ListItem struct {
	ID                          string                           `json:"id"`
	SystemAccountID             *string                          `json:"systemAccountId,omitempty"`
	SystemAccountName           *string                          `json:"systemAccountName,omitempty"`
	OwnerSystemAccountID        string                           `json:"ownerSystemAccountId"`
	OwnerSystemAccountName      *string                          `json:"ownerSystemAccountName,omitempty"`
	Name                        string                           `json:"name"`
	ProviderCode                string                           `json:"providerCode"`
	Description                 *string                          `json:"description,omitempty"`
	Enabled                     bool                             `json:"enabled"`
	IsDefault                   bool                             `json:"isDefault"`
	GroupType                   string                           `json:"groupType"`
	AccessType                  string                           `json:"accessType"`
	GroupAuthorizationID        *string                          `json:"groupAuthorizationId,omitempty"`
	AuthorizationStatus         *string                          `json:"authorizationStatus,omitempty"`
	AuthorizationExpiresAt      *string                          `json:"authorizationExpiresAt,omitempty"`
	AuthorizationSourceSummary  *AuthorizationSourceSummarySummary `json:"authorizationSourceSummary,omitempty"`
	UpdatedAt                   string                           `json:"updatedAt"`
	AccountStats                AccountStats                     `json:"accountStats"`
	CanEdit                     bool                             `json:"canEdit"`
	CanDelete                   bool                             `json:"canDelete"`
	CanReturn                   bool                             `json:"canReturn"`
}

// Detail mirrors the Node GroupSummary projection (findGroupSummary): the
// authorization view carries the limits document, sanitized sources and the
// effective permissions; the accountStats numeric projection (juhe_stats +
// runtime concurrency) is owned by the stats wiring.
type Detail struct {
	ID                     string                       `json:"id"`
	SystemAccountID        *string                      `json:"systemAccountId,omitempty"`
	SystemAccountName      *string                      `json:"systemAccountName,omitempty"`
	OwnerSystemAccountID   string                       `json:"ownerSystemAccountId"`
	OwnerSystemAccountName *string                      `json:"ownerSystemAccountName,omitempty"`
	Name                   string                       `json:"name"`
	ProviderCode           string                       `json:"providerCode"`
	Description            *string                      `json:"description,omitempty"`
	Enabled                bool                         `json:"enabled"`
	IsDefault              bool                         `json:"isDefault"`
	GroupType              string                       `json:"groupType"`
	SchedulingPolicy       any                          `json:"schedulingPolicy"`
	AccountIDs             []string                     `json:"accountIds"`
	AccountStats           AccountStats                 `json:"accountStats"`
	AccessType             string                       `json:"accessType"`
	GroupAuthorizationID   *string                      `json:"groupAuthorizationId,omitempty"`
	AuthorizationStatus    *string                      `json:"authorizationStatus,omitempty"`
	AuthorizationExpiresAt *string                      `json:"authorizationExpiresAt,omitempty"`
	AuthorizationLimits    any                          `json:"authorizationLimits"`
	AuthorizationSources   []AuthorizationSourceSummary `json:"authorizationSources,omitempty"`
	Permissions            ResourcePermissions          `json:"permissions"`
	// UpdatedAt stays a Go-side superset field: the delegated/aipublic faces
	// read it for optimistic locking (Node clients use /:id/edit-basic).
	// Known superset deviation of the detail envelope, registered in the
	// BUG-0163 review notes.
	UpdatedAt string `json:"updatedAt"`
}

// ListPageResult mirrors GroupListPageResult after hydrateGroupListPage:
// generatedAt stamps the response even when the stats hydration stays on the
// empty projection.
type ListPageResult struct {
	Items       []ListItem `json:"items"`
	Total       int        `json:"total"`
	HasMore     bool       `json:"hasMore"`
	Page        int        `json:"page"`
	PageSize    int        `json:"pageSize"`
	GeneratedAt string     `json:"generatedAt"`
}

// groupRow is the shared scan target for list/detail/patch locator rows.
type groupRow struct {
	id              string
	systemAccountID string
	name            string
	providerCode    string
	description     sql.NullString
	enabled         bool
	isDefault       bool
	groupType       string
	schedulingJSON  sql.NullString
	createdAt       string
	updatedAt       string
}

const groupRowColumns = "g.id, g.system_account_id, g.name, g.provider_code, g.description, g.enabled, g.is_default, g.group_type, g.scheduling_policy_json, g.created_at, g.updated_at"

func scanGroupRow(scan func(...any) error) (groupRow, error) {
	var row groupRow
	var enabled, isDefault int
	err := scan(&row.id, &row.systemAccountID, &row.name, &row.providerCode, &row.description,
		&enabled, &isDefault, &row.groupType, &row.schedulingJSON, &row.createdAt, &row.updatedAt)
	if err != nil {
		return groupRow{}, err
	}
	row.enabled = enabled == 1
	row.isDefault = isDefault == 1
	return row, nil
}

// ownerClause mirrors the owner branch of queryGroupRowsForAccess: admins
// without a filter see all rows, everyone else is pinned to the scope id.
func ownerClause(access AccessScope) (string, []any, bool) {
	if access.canAccessAll() {
		ownerID := access.manageableID()
		if ownerID == "" {
			return "", nil, true
		}
		return "g.system_account_id = ?", []any{ownerID}, true
	}
	if access.ViewerID == "" {
		return "", nil, false
	}
	return "g.system_account_id = ?", []any{access.ViewerID}, true
}

// listPageBounds mirrors query-utils normalizeListPage's window bound:
// pageUpperBoundForWindow = max(1, floor((1001-1)/pageSize)).
func listPageBounds(pageSize int) int {
	if pageSize < 1 {
		return 1
	}
	bound := 1000 / pageSize
	if bound < 1 {
		bound = 1
	}
	return bound
}

// normalizeListPageValues mirrors normalizeGroupListOptions +
// normalizeListPage: pageSize clamps to 1..500 (default 50), page clamps to
// 1..floor(1000/pageSize) (default 1).
func normalizeListPageValues(page, pageSize int) (int, int) {
	if pageSize < 1 {
		pageSize = 50
	}
	if pageSize > 500 {
		pageSize = 500
	}
	if page < 1 {
		page = 1
	}
	if bound := listPageBounds(pageSize); page > bound {
		page = bound
	}
	return page, pageSize
}

// keywordFilter mirrors buildGroupFilter: case-sensitive prefix range over
// name OR provider_code (textPrefixUpperBound).
func keywordFilter(keyword string) (string, []any) {
	text := strings.TrimSpace(keyword)
	if text == "" {
		return "", nil
	}
	upper := textPrefixUpperBound(text)
	return "(g.name >= ? AND g.name < ? OR g.provider_code >= ? AND g.provider_code < ?)",
		[]any{text, upper, text, upper}
}

func textPrefixUpperBound(value string) string {
	runes := []rune(value)
	for index := len(runes) - 1; index >= 0; index-- {
		if runes[index] < 0x10ffff {
			runes[index]++
			return string(runes[:index+1])
		}
	}
	return value + "\uffff"
}

// accessListRow is the access-aware scan target: a group row plus the
// access_type/authorization columns of the owner UNION ALL authorized query.
type accessListRow struct {
	row                    groupRow
	accessType             string
	authorizationID        sql.NullString
	authorizationStatus    sql.NullString
	authorizationExpiresAt sql.NullString
	authorizationLimits    sql.NullString
}

func scanAccessListRowWithAuth(scan func(...any) error) (accessListRow, error) {
	var out accessListRow
	var enabled, isDefault int
	err := scan(&out.row.id, &out.row.systemAccountID, &out.row.name, &out.row.providerCode, &out.row.description,
		&enabled, &isDefault, &out.row.groupType, &out.row.schedulingJSON, &out.row.createdAt, &out.row.updatedAt,
		&out.accessType, &out.authorizationID, &out.authorizationStatus, &out.authorizationExpiresAt, &out.authorizationLimits)
	if err != nil {
		return accessListRow{}, err
	}
	out.row.enabled = enabled == 1
	out.row.isDefault = isDefault == 1
	return out, nil
}

const ownerAccessColumns = `'owner' AS access_type, NULL AS authorization_id, NULL AS authorization_status, NULL AS authorization_expires_at, NULL AS authorization_limits_json`

// authorizedListArm mirrors the authorized arm of queryGroupRowsForAccess:
// grantee-scoped runtime rows with the management-list status guard and the
// per-grantee settings overrides, excluding rows the owner arm returned. The
// column order matches groupRowColumns + the access columns (description
// fifth) so the UNION ALL aligns positionally.
func (s *Store) authorizedListArm() string {
	return `SELECT g.id, g.system_account_id, g.name, g.provider_code, g.description,
			CASE WHEN g.enabled = 1 THEN COALESCE(s.enabled, 1) ELSE 0 END AS enabled,
			g.is_default,
			COALESCE(s.group_type, g.group_type) AS group_type,
			CASE WHEN COALESCE(s.group_type, g.group_type) = 'high_concurrency'
				THEN COALESCE(s.scheduling_policy_json, g.scheduling_policy_json) ELSE NULL END AS scheduling_policy_json,
			g.created_at,
			COALESCE(s.updated_at, g.updated_at) AS updated_at,
			'authorized' AS access_type,
			ra.id AS authorization_id,
			ra.status AS authorization_status,
			ra.expires_at AS authorization_expires_at,
			ra.limits_json AS authorization_limits_json
		FROM ` + s.table("resource_authorizations") + ` ra
		INNER JOIN ` + s.table("groups") + ` g ON g.id = ra.resource_id
		` + s.authorizationSettingsJoin() + `
		` + authorizedArmWhere
}

// ListPage mirrors listGroupRowsPageForAccessAsync + listGroupItemsPageAsync
// + hydrateGroupListPage: pageSize+1 probe, ORDER BY updated_at DESC, id DESC,
// the owner UNION ALL authorized access query and the generatedAt stamp.
// The accountStats numeric projection (juhe_stats.group_account_stats +
// usage summaries + runtime concurrency) stays on the empty shape until the
// stats wiring hands this store its readers.
func (s *Store) ListPage(ctx context.Context, access AccessScope, page, pageSize int, keyword string) (*ListPageResult, error) {
	ctx = ensureCtx(ctx)
	page, pageSize = normalizeListPageValues(page, pageSize)
	owner := access.manageableID()
	viewer := owner
	if viewer == "" {
		viewer = access.ViewerID
	}

	var (
		queryText string
		queryArgs []any
	)
	switch {
	case owner == "" && access.canAccessAll():
		// Direct owner query over every row (Node's unscoped admin branch).
		clauses, args := []string{}, []any{}
		if clause, clauseArgs := keywordFilter(keyword); clause != "" {
			clauses = append(clauses, clause)
			args = append(args, clauseArgs...)
		}
		where := ""
		if len(clauses) > 0 {
			where = " WHERE " + strings.Join(clauses, " AND ")
		}
		queryText = `SELECT ` + groupRowColumns + `, ` + ownerAccessColumns + `
			FROM ` + s.table("groups") + ` g` + where + `
			ORDER BY g.updated_at DESC, g.id DESC
			LIMIT ? OFFSET ?`
		queryArgs = args
	default:
		if viewer == "" {
			return &ListPageResult{Items: []ListItem{}, Page: page, PageSize: pageSize, GeneratedAt: s.nowISO()}, nil
		}
		clauses, outerArgs := rowFilter("", "", keyword, nil)
		outerWhere := ""
		if len(clauses) > 0 {
			outerWhere = " WHERE " + strings.Join(clauses, " AND ")
		}
		queryText = `SELECT id, system_account_id, name, provider_code, description, enabled, is_default,
				group_type, scheduling_policy_json, created_at, updated_at,
				access_type, authorization_id, authorization_status, authorization_expires_at, authorization_limits_json
			FROM (
				SELECT ` + groupRowColumns + `, ` + ownerAccessColumns + `
				FROM ` + s.table("groups") + ` g
				WHERE g.system_account_id = ?
				UNION ALL
				` + s.authorizedListArm() + `
			) group_rows` + outerWhere + `
			ORDER BY updated_at DESC, id DESC
			LIMIT ? OFFSET ?`
		queryArgs = append([]any{ownerOrViewer(owner, viewer), viewer, ownerOrViewer(owner, viewer)}, outerArgs...)
	}
	queryArgs = append(queryArgs, pageSize+1, (page-1)*pageSize)
	rows, err := s.db.QueryContext(ctx, s.bind(queryText), queryArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := []accessListRow{}
	scan := scanAccessListRowWithAuth
	if owner == "" && access.canAccessAll() {
		// The unscoped admin query selects the owner-authorization columns in
		// the same order as the union output.
		scan = func(scanFn func(...any) error) (accessListRow, error) {
			var out accessListRow
			var enabled, isDefault int
			err := scanFn(&out.row.id, &out.row.systemAccountID, &out.row.name, &out.row.providerCode, &out.row.description,
				&enabled, &isDefault, &out.row.groupType, &out.row.schedulingJSON, &out.row.createdAt, &out.row.updatedAt,
				&out.accessType, &out.authorizationID, &out.authorizationStatus, &out.authorizationExpiresAt, &out.authorizationLimits)
			if err != nil {
				return accessListRow{}, err
			}
			out.row.enabled = enabled == 1
			out.row.isDefault = isDefault == 1
			return out, nil
		}
	}
	for rows.Next() {
		row, scanErr := scan(rows.Scan)
		if scanErr != nil {
			return nil, scanErr
		}
		groups = append(groups, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	hasMore := len(groups) > pageSize
	if hasMore {
		groups = groups[:pageSize]
	}
	rows_ := make([]groupRow, 0, len(groups))
	for _, item := range groups {
		rows_ = append(rows_, item.row)
	}
	names := map[string]string{}
	if includeSystemAccountFieldsForList(access, groups) {
		names, err = s.systemAccountNames(ctx, groupOwnerIDs(rows_))
		if err != nil {
			return nil, err
		}
	}
	items := make([]ListItem, 0, len(groups))
	for _, item := range groups {
		listItem, buildErr := s.newListItem(ctx, item, names, access.canAccessAll(), viewer)
		if buildErr != nil {
			return nil, buildErr
		}
		items = append(items, listItem)
	}
	total := (page-1)*pageSize + len(items)
	if hasMore {
		total++
	}
	return &ListPageResult{Items: items, Total: total, HasMore: hasMore, Page: page, PageSize: pageSize, GeneratedAt: s.nowISO()}, nil
}

// includeSystemAccountFieldsForList mirrors buildGroupListItems: admin scopes
// and any authorized row load the account-name map.
func includeSystemAccountFieldsForList(access AccessScope, rows []accessListRow) bool {
	if access.canAccessAll() {
		return true
	}
	for _, row := range rows {
		if row.accessType == "authorized" {
			return true
		}
	}
	return false
}

func groupOwnerIDs(rows []groupRow) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.systemAccountID)
	}
	return ids
}

// systemAccountNames mirrors loadSystemAccountNameMapByIds.
func (s *Store) systemAccountNames(ctx context.Context, ids []string) (map[string]string, error) {
	unique := uniqueStrings(ids)
	names := map[string]string{}
	if len(unique) == 0 {
		return names, nil
	}
	placeholders := make([]string, len(unique))
	args := make([]any, len(unique))
	for i, id := range unique {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT id, display_name FROM `+s.table("system_accounts")+`
		WHERE id IN (`+strings.Join(placeholders, ",")+`)`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		names[id] = name
	}
	return names, rows.Err()
}

// groupAccountIDsByGroupIDs mirrors loadGroupAccountIdsByGroupIds: enabled
// membership joined to non-deleted accounts, kept only when the account owner
// equals the group owner or the binding's runtime authorization row is
// active/paused/expired.
func (s *Store) groupAccountIDsByGroupIDs(ctx context.Context, groupIDs []string) (map[string][]string, error) {
	result := map[string][]string{}
	unique := uniqueStrings(groupIDs)
	if len(unique) == 0 {
		return result, nil
	}
	placeholders := make([]string, len(unique))
	args := make([]any, len(unique))
	for i, id := range unique {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT group_accounts.group_id, group_accounts.account_id
		FROM `+s.table("group_accounts")+` group_accounts
		INNER JOIN `+s.table("groups")+` groups ON groups.id = group_accounts.group_id
		INNER JOIN `+s.table("accounts")+` accounts ON accounts.id = group_accounts.account_id
		LEFT JOIN `+s.table("resource_authorizations")+` resource_authorization_rows
			ON resource_authorization_rows.id = group_accounts.account_authorization_id
		WHERE group_accounts.enabled = 1
			AND accounts.deleted_at IS NULL
			AND group_accounts.group_id IN (`+strings.Join(placeholders, ",")+`)
			AND (
				accounts.system_account_id = groups.system_account_id
				OR resource_authorization_rows.status IN ('active', 'paused', 'expired')
			)
		ORDER BY group_accounts.group_id ASC, group_accounts.created_at ASC, group_accounts.account_id ASC`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for _, id := range unique {
		result[id] = []string{}
	}
	for rows.Next() {
		var groupID, accountID string
		if err := rows.Scan(&groupID, &accountID); err != nil {
			return nil, err
		}
		result[groupID] = append(result[groupID], accountID)
	}
	return result, rows.Err()
}

// groupAccountIDs mirrors loadGroupAccountIdsByGroupIds for the owner detail
// member list.
func (s *Store) groupAccountIDs(ctx context.Context, groupID string) ([]string, error) {
	byGroup, err := s.groupAccountIDsByGroupIDs(ctx, []string{groupID})
	if err != nil {
		return nil, err
	}
	ids, ok := byGroup[groupID]
	if !ok {
		return []string{}, nil
	}
	return ids, nil
}

// canBindAccessRowValues mirrors canBindAuthorizedGroupRowToApiKey for the
// list/detail projections: enabled && authorization_status='active' && not
// expired.
func (s *Store) canBindAccessRowValues(enabled bool, status, expiresAt sql.NullString) (bool, error) {
	if !enabled {
		return false, nil
	}
	if !status.Valid || status.String != "active" {
		return false, nil
	}
	expired, err := s.authorizationExpired(expiresAt)
	if err != nil {
		return false, err
	}
	return !expired, nil
}

// authorizedSourceRow is the resource_authorization_sources scan target
// (authorization-read-loaders.ts) before viewer sanitization.
type authorizedSourceRow struct {
	id             string
	authorizationID string
	sourceType     string
	sourceTeamID   sql.NullString
	sourceTeamName sql.NullString
	status         string
	activatedAt    sql.NullString
	endedAt        sql.NullString
	endedReason    sql.NullString
	createdBy      string
	createdAt      string
	revokedBy      sql.NullString
	revokedAt      sql.NullString
	updatedAt      string
}

// authorizationSources mirrors loadResourceAuthorizationSourcesByAuthorizationIds
// (uncached): one query for the given authorization ids ordered by
// status/created_at/id.
func (s *Store) authorizationSources(ctx context.Context, authorizationIDs []string) (map[string][]authorizedSourceRow, error) {
	result := map[string][]authorizedSourceRow{}
	unique := uniqueStrings(authorizationIDs)
	if len(unique) == 0 {
		return result, nil
	}
	placeholders := make([]string, len(unique))
	args := make([]any, len(unique))
	for i, id := range unique {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT ras.id, ras.authorization_id, ras.source_type, ras.source_team_id,
			system_teams.name, ras.status, ras.activated_at, ras.ended_at, ras.ended_reason,
			ras.created_by, ras.created_at, ras.revoked_by, ras.revoked_at, ras.updated_at
		FROM `+s.table("resource_authorization_sources")+` ras
		LEFT JOIN `+s.table("system_teams")+` system_teams ON system_teams.id = ras.source_team_id
		WHERE ras.authorization_id IN (`+strings.Join(placeholders, ",")+`)
		ORDER BY ras.status ASC, ras.created_at ASC, ras.id ASC`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var row authorizedSourceRow
		if err := rows.Scan(&row.id, &row.authorizationID, &row.sourceType, &row.sourceTeamID,
			&row.sourceTeamName, &row.status, &row.activatedAt, &row.endedAt, &row.endedReason,
			&row.createdBy, &row.createdAt, &row.revokedBy, &row.revokedAt, &row.updatedAt); err != nil {
			return nil, err
		}
		result[row.authorizationID] = append(result[row.authorizationID], row)
	}
	return result, rows.Err()
}

// summarizeAuthorizationSourcesForViewer mirrors
// sanitizeAuthorizationSourcesForViewer(limited=true) for the detail payload.
func sanitizedAuthorizationSources(rows []authorizedSourceRow) []AuthorizationSourceSummary {
	out := make([]AuthorizationSourceSummary, 0, len(rows))
	for _, row := range rows {
		out = append(out, AuthorizationSourceSummary{
			ID:              row.id,
			AuthorizationID: row.authorizationID,
			SourceType:      row.sourceType,
			SourceTeamName:  nullPtrString(row.sourceTeamName),
			Status:          row.status,
			ActivatedAt:     nullPtrString(row.activatedAt),
			EndedReason:     nullPtrString(row.endedReason),
			CreatedBy:       "",
			CreatedAt:       row.createdAt,
			UpdatedAt:       row.updatedAt,
		})
	}
	return out
}

// summarizeAuthorizationSources mirrors summarizeGroupAuthorizationSources.
func summarizeAuthorizationSources(rows []authorizedSourceRow) *AuthorizationSourceSummarySummary {
	active := []authorizedSourceRow{}
	for _, row := range rows {
		if row.status == "active" {
			active = append(active, row)
		}
	}
	teamNames := []string{}
	seen := map[string]bool{}
	for _, row := range active {
		name := strings.TrimSpace(row.sourceTeamName.String)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		teamNames = append(teamNames, name)
	}
	hasManual := false
	hasTeam := false
	for _, row := range active {
		if row.sourceType == "manual" {
			hasManual = true
		}
		if row.sourceType == "team" {
			hasTeam = true
		}
	}
	for _, row := range rows {
		if row.sourceType == "team" {
			hasTeam = true
		}
	}
	return &AuthorizationSourceSummarySummary{
		ActiveSourceCount: len(active),
		HasManual:         hasManual,
		HasTeam:           hasTeam,
		TeamNames:         teamNames,
	}
}

// hasActiveManualSource mirrors hasActiveManualAuthorizationSource.
func hasActiveManualSource(rows []authorizedSourceRow) bool {
	for _, row := range rows {
		if row.sourceType == "manual" && row.status == "active" {
			return true
		}
	}
	return false
}

// newListItem mirrors groupListItemFromRow: the authorized view forces
// isDefault=false, carries the runtime authorization columns plus the source
// summary, and renders the authorized permissions whenever the row owner
// differs from the viewer.
// newListItem mirrors groupListItemFromRow: the list projection validates the
// stored group type but (like Node) omits schedulingPolicy and does not parse
// the policy JSON; the authorized view forces isDefault=false, carries the
// runtime authorization columns plus the source summary, and renders the
// authorized permissions whenever the row owner differs from the viewer.
func (s *Store) newListItem(ctx context.Context, row accessListRow, names map[string]string, includeSystemAccountFields bool, viewer string) (ListItem, error) {
	groupType, err := normalizeStoredGroupType(row.row.groupType)
	if err != nil {
		return ListItem{}, err
	}
	authorized := row.accessType == "authorized"
	authorizedView := authorized && row.row.systemAccountID != viewer
	permissions := ownerPermissions()
	if authorizedView {
		canBind, bindErr := s.canBindAccessRowValues(row.row.enabled, row.authorizationStatus, row.authorizationExpiresAt)
		if bindErr != nil {
			return ListItem{}, bindErr
		}
		permissions = authorizedGroupPermissions(canBind, false)
	}
	item := ListItem{
		ID:                     row.row.id,
		OwnerSystemAccountID:   row.row.systemAccountID,
		OwnerSystemAccountName: ptrString(names[row.row.systemAccountID]),
		Name:                   row.row.name,
		ProviderCode:           row.row.providerCode,
		Description:            nullPtrString(row.row.description),
		Enabled:                row.row.enabled,
		IsDefault:              !authorized && row.row.isDefault,
		GroupType:              groupType,
		AccessType:             row.accessType,
		GroupAuthorizationID:   nullPtrString(row.authorizationID),
		AuthorizationStatus:    nullPtrString(row.authorizationStatus),
		AuthorizationExpiresAt: nullPtrString(row.authorizationExpiresAt),
		UpdatedAt:              row.row.updatedAt,
		AccountStats:           emptyAccountStats(),
		CanEdit:                !row.row.isDefault && permissions.CanEdit,
		CanDelete:              !row.row.isDefault && permissions.CanDelete,
		CanReturn:              authorized && permissions.CanReturnAuthorization,
	}
	if authorizedView {
		sources, sourceErr := s.authorizationSources(ctx, []string{row.authorizationID.String})
		if sourceErr != nil {
			return ListItem{}, sourceErr
		}
		item.AuthorizationSourceSummary = summarizeAuthorizationSources(sources[row.authorizationID.String])
	}
	if includeSystemAccountFields {
		item.SystemAccountID = ptrString(row.row.systemAccountID)
		item.SystemAccountName = item.OwnerSystemAccountName
	}
	return item, nil
}

// FindDetail mirrors findGroupRowForAccessAsync + buildGroupSummaries: the
// admin-unscoped branch reads the direct owner query, every other scope reads
// the owner UNION ALL authorized rows; the authorized view returns empty
// accountIds, the limits document, sanitized sources and the effective
// permissions. nil when the group is missing or invisible (route renders
// 404 分组不存在).
func (s *Store) FindDetail(ctx context.Context, id string, access AccessScope) (*Detail, error) {
	ctx = ensureCtx(ctx)
	owner := access.manageableID()
	viewer := owner
	if viewer == "" {
		viewer = access.ViewerID
	}
	var (
		queryText string
		queryArgs []any
	)
	if owner == "" && access.canAccessAll() {
		queryText = `SELECT ` + groupRowColumns + `, ` + ownerAccessColumns + `
			FROM ` + s.table("groups") + ` g WHERE g.id = ? LIMIT 1`
		queryArgs = []any{id}
	} else {
		if viewer == "" {
			return nil, nil
		}
		queryText = `SELECT id, system_account_id, name, provider_code, description, enabled, is_default,
				group_type, scheduling_policy_json, created_at, updated_at,
				access_type, authorization_id, authorization_status, authorization_expires_at, authorization_limits_json
			FROM (
				SELECT ` + groupRowColumns + `, ` + ownerAccessColumns + `
				FROM ` + s.table("groups") + ` g
				WHERE g.id = ? AND g.system_account_id = ?
				UNION ALL
				SELECT g.id, g.system_account_id, g.name, g.provider_code, g.description,
					CASE WHEN g.enabled = 1 THEN COALESCE(s.enabled, 1) ELSE 0 END AS enabled,
					g.is_default,
					COALESCE(s.group_type, g.group_type) AS group_type,
					CASE WHEN COALESCE(s.group_type, g.group_type) = 'high_concurrency'
						THEN COALESCE(s.scheduling_policy_json, g.scheduling_policy_json) ELSE NULL END AS scheduling_policy_json,
					g.created_at,
					COALESCE(s.updated_at, g.updated_at) AS updated_at,
					'authorized' AS access_type,
					ra.id AS authorization_id,
					ra.status AS authorization_status,
					ra.expires_at AS authorization_expires_at,
					ra.limits_json AS authorization_limits_json
				FROM ` + s.table("resource_authorizations") + ` ra
				INNER JOIN ` + s.table("groups") + ` g ON g.id = ra.resource_id
				` + s.authorizationSettingsJoin() + `
				WHERE g.id = ?
					AND ra.resource_type = 'group'
					AND ra.grantee_system_account_id = ?
					AND ra.status IN ('active', 'paused', 'expired')
					AND g.system_account_id <> ?
			) group_rows LIMIT 1`
		queryArgs = []any{id, ownerOrViewer(owner, viewer), id, viewer, ownerOrViewer(owner, viewer)}
	}
	row, err := scanAccessListRowWithAuth(func(targets ...any) error {
		return s.db.QueryRowContext(ctx, s.bind(queryText), queryArgs...).Scan(targets...)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	groupType, err := normalizeStoredGroupType(row.row.groupType)
	if err != nil {
		return nil, err
	}
	policy, err := parseStoredSchedulingPolicy(row.row.schedulingJSON.String, row.row.schedulingJSON.Valid, groupType, s.globalConcurrencyMax())
	if err != nil {
		return nil, err
	}
	names, err := s.systemAccountNames(ctx, []string{row.row.systemAccountID})
	if err != nil {
		return nil, err
	}
	authorized := row.accessType == "authorized"
	authorizedView := authorized && row.row.systemAccountID != viewer
	accountIDs := []string{}
	accountStats := emptyAccountStats()
	sourcesByAuthorization := map[string][]authorizedSourceRow{}
	if !authorized {
		accountIDs, err = s.groupAccountIDs(ctx, row.row.id)
		if err != nil {
			return nil, err
		}
		accountStats.Total = len(accountIDs)
	} else {
		sourcesByAuthorization, err = s.authorizationSources(ctx, []string{row.authorizationID.String})
		if err != nil {
			return nil, err
		}
	}
	limits, err := parseAuthorizationLimitsView(row.authorizationLimits)
	if err != nil {
		return nil, err
	}
	permissions := ownerPermissions()
	if authorizedView {
		canBind, bindErr := s.canBindAccessRowValues(row.row.enabled, row.authorizationStatus, row.authorizationExpiresAt)
		if bindErr != nil {
			return nil, bindErr
		}
		permissions = authorizedGroupPermissions(canBind, hasActiveManualSource(sourcesByAuthorization[row.authorizationID.String]))
	}
	detail := &Detail{
		ID:                     row.row.id,
		OwnerSystemAccountID:   row.row.systemAccountID,
		OwnerSystemAccountName: ptrString(names[row.row.systemAccountID]),
		Name:                   row.row.name,
		ProviderCode:           row.row.providerCode,
		Description:            nullPtrString(row.row.description),
		Enabled:                row.row.enabled,
		IsDefault:              !authorized && row.row.isDefault,
		GroupType:              groupType,
		SchedulingPolicy:       policy,
		AccountIDs:             accountIDs,
		AccountStats:           accountStats,
		AccessType:             row.accessType,
		GroupAuthorizationID:   nullPtrString(row.authorizationID),
		AuthorizationStatus:    nullPtrString(row.authorizationStatus),
		AuthorizationExpiresAt: nullPtrString(row.authorizationExpiresAt),
		AuthorizationLimits:    limits,
		Permissions:            permissions,
		UpdatedAt:              row.row.updatedAt,
	}
	if authorizedView {
		detail.AuthorizationSources = sanitizedAuthorizationSources(sourcesByAuthorization[row.authorizationID.String])
	}
	if access.canAccessAll() {
		detail.SystemAccountID = ptrString(row.row.systemAccountID)
		detail.SystemAccountName = detail.OwnerSystemAccountName
	}
	return detail, nil
}

// MutationInput is the normalized create/patch payload; nil pointers mean
// "field absent" (hasOwnInput semantics).
type MutationInput struct {
	Name             *string
	ProviderCode     *string
	Description      *string
	Enabled          *bool
	GroupType        *string
	SchedulingPolicy any // decoded JSON object when present
}

// Empty reports whether no patchable field is present.
func (m MutationInput) Empty() bool {
	return m.Name == nil && m.ProviderCode == nil && m.Description == nil &&
		m.Enabled == nil && m.GroupType == nil && m.SchedulingPolicy == nil
}

// Create mirrors createGroupWithReceiptAsync: provider availability check,
// normalized fields, owner-scoped unique name
// (idx_groups_owner_provider_name_unique) and the gateway invalidation hook.
func (s *Store) Create(ctx context.Context, input MutationInput, access AccessScope) (*ListItem, error) {
	ctx = ensureCtx(ctx)
	ownerID, err := access.writeSystemAccountID()
	if err != nil {
		return nil, err
	}
	name, err := requiredText(input.Name, "分组名称")
	if err != nil {
		return nil, err
	}
	providerCode, err := requiredText(input.ProviderCode, "供应商")
	if err != nil {
		return nil, err
	}
	groupType, err := normalizeGroupType(input.GroupType)
	if err != nil {
		return nil, err
	}
	policyJSON, err := schedulingPolicyJSON(groupType, input.SchedulingPolicy, s.globalConcurrencyMax())
	if err != nil {
		return nil, err
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	description, err := nullableText(input.Description, "分组说明")
	if err != nil {
		return nil, err
	}
	if err := s.assertProviderAvailable(ctx, s.db, providerCode); err != nil {
		return nil, err
	}
	now := s.nowISO()
	id := s.newI("grp")
	policyValue := policyJSONString(policyJSON)
	_, err = s.db.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("groups")+`
		(id, system_account_id, name, provider_code, description, enabled, is_default, group_type, scheduling_policy_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?)`),
		id, ownerID, name, providerCode, description, boolToInt(enabled), groupType, policyValue, now, now)
	if err != nil {
		if duplicate := duplicateGroupNameError(err, name); duplicate != nil {
			return nil, duplicate
		}
		return nil, err
	}
	// Node invalidateGroupLookupCache + notifyGatewayRuntimeCacheInvalidation.
	s.invalidateRuntime("group_created")
	// groupCreateListItem: the 201 payload is the GroupListItem shape whose
	// accountStats carries only the seven counters plus currentConcurrency
	// (no todayUsage/usage keys) and whose ownerSystemAccountName mirrors the
	// group's systemAccountName (admin scopes only).
	ownerName := s.lookupName(ctx, ownerID)
	if !access.canAccessAll() {
		ownerName = nil
	}
	item := &ListItem{
		ID:                     id,
		SystemAccountID:        nil,
		OwnerSystemAccountID:   ownerID,
		OwnerSystemAccountName: ownerName,
		Name:                   name,
		ProviderCode:           providerCode,
		Description:            description,
		Enabled:                enabled,
		IsDefault:              false,
		GroupType:              groupType,
		AccessType:             "owner",
		UpdatedAt:              now,
		AccountStats:           AccountStats{},
		CanEdit:                true,
		CanDelete:              true,
		CanReturn:              false,
	}
	if access.canAccessAll() {
		item.SystemAccountID = ptrString(ownerID)
		item.SystemAccountName = ownerName
	}
	return item, nil
}

// policyJSONString renders schedulingPolicyJSON for storage/sql.NullString reuse.
func policyJSONString(raw json.RawMessage) sql.NullString {
	if len(raw) == 0 {
		return sql.NullString{}
	}
	return sql.NullString{String: string(raw), Valid: true}
}

func (s *Store) lookupName(ctx context.Context, id string) *string {
	names, err := s.systemAccountNames(ctx, []string{id})
	if err != nil || names[id] == "" {
		return nil
	}
	return ptrString(names[id])
}

// assertProviderAvailable mirrors assertGroupPatchProviderRow +
// findProviderOptionByCodeAsync route validation.
func (s *Store) assertProviderAvailable(ctx context.Context, q queryer, code string) error {
	var enabled sql.NullInt64
	err := q.QueryRowContext(ctx, s.bind(`SELECT enabled FROM `+s.table("providers")+` WHERE code = ? LIMIT 1`), code).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return &ValidationError{Message: "不支持的供应商：" + code}
	}
	if err != nil {
		return err
	}
	if !enabled.Valid || enabled.Int64 != 1 {
		return &ValidationError{Message: "供应商已停用：" + code}
	}
	return nil
}

// ownedGroupHasAccounts mirrors ownedGroupHasAccountsAsync.
func (s *Store) ownedGroupHasAccounts(ctx context.Context, q queryer, groupID string) (bool, error) {
	var found int
	err := q.QueryRowContext(ctx, s.bind(`SELECT 1 FROM `+s.table("group_accounts")+` group_accounts
		INNER JOIN `+s.table("accounts")+` accounts ON accounts.id = group_accounts.account_id
		WHERE group_accounts.group_id = ? AND group_accounts.enabled = 1 AND accounts.deleted_at IS NULL
		LIMIT 1`), groupID).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// Change mirrors GroupManagementPatchChange with stringified values for the
// operation log.
type Change struct {
	Field  string
	Before string
	After  string
}

// PatchResult mirrors GroupManagementPatchResult (route response subset).
type PatchResult struct {
	ID                   string   `json:"id"`
	Name                 string   `json:"name"`
	AccessType           string   `json:"accessType"`
	ChangedFields        []string `json:"changedFields"`
	UpdatedAt            string   `json:"updatedAt"`
	OwnerSystemAccountID string   `json:"-"`
	Changes              []Change `json:"-"`
}

// Patch mirrors patchGroupAsync (owner branch): default-group readonly guard,
// expectedUpdatedAt optimistic lock, only-changed columns, provider-change
// and disable-binding protection, monotonic updatedAt.
func (s *Store) Patch(ctx context.Context, id string, input MutationInput, expectedUpdatedAt string, access AccessScope) (*PatchResult, error) {
	ctx = ensureCtx(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	clause, clauseArgs, ok := ownerClause(access)
	if !ok {
		return nil, nil
	}
	where := ""
	args := []any{id}
	if clause != "" {
		where = " AND " + clause
		args = append(args, clauseArgs...)
	}
	current, err := scanGroupRow(func(targets ...any) error {
		return tx.QueryRowContext(ctx, s.bind(`SELECT `+groupRowColumns+`
			FROM `+s.table("groups")+` g WHERE g.id = ?`+where+` LIMIT 1`), args...).Scan(targets...)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if current.isDefault {
		return nil, &ValidationError{Message: "默认分组不允许修改"}
	}
	if expected := strings.TrimSpace(expectedUpdatedAt); expected != "" && expected != current.updatedAt {
		return nil, &ConflictError{Message: "分组已被其他操作更新，请刷新后重试"}
	}

	assignments := []string{}
	updateArgs := []any{}
	changes := []Change{}
	addChange := func(field string, before, after string) {
		if before != after {
			changes = append(changes, Change{Field: field, Before: before, After: after})
		}
	}

	nextName := current.name
	if input.Name != nil {
		name, textErr := requiredText(input.Name, "分组名称")
		if textErr != nil {
			return nil, textErr
		}
		if name != current.name {
			assignments = append(assignments, "name = ?")
			updateArgs = append(updateArgs, name)
		}
		addChange("name", current.name, name)
		nextName = name
	}
	if input.ProviderCode != nil {
		providerCode, textErr := requiredText(input.ProviderCode, "供应商")
		if textErr != nil {
			return nil, textErr
		}
		if providerCode != current.providerCode {
			assignments = append(assignments, "provider_code = ?")
			updateArgs = append(updateArgs, providerCode)
		}
		addChange("providerCode", current.providerCode, providerCode)
	}
	if input.Description != nil {
		description, textErr := nullableText(input.Description, "分组说明")
		if textErr != nil {
			return nil, textErr
		}
		if !sameNullableText(current.description, description) {
			assignments = append(assignments, "description = ?")
			updateArgs = append(updateArgs, description)
		}
		addChange("description", nullText(current.description), derefOrEmpty(description))
	}
	if input.Enabled != nil {
		if *input.Enabled != current.enabled {
			assignments = append(assignments, "enabled = ?")
			updateArgs = append(updateArgs, boolToInt(*input.Enabled))
		}
		addChange("enabled", boolText(current.enabled), boolText(*input.Enabled))
	}

	hasGroupTypeInput := input.GroupType != nil
	hasSchedulingPolicyInput := input.SchedulingPolicy != nil
	if hasGroupTypeInput || hasSchedulingPolicyInput {
		beforeGroupType, typeErr := normalizeStoredGroupType(current.groupType)
		if typeErr != nil {
			// Node buildGroupPatchPlan throws through the patch route's 400
			// branch (分组类型无效).
			return nil, &ValidationError{Message: typeErr.Error()}
		}
		afterGroupType := beforeGroupType
		if hasGroupTypeInput {
			normalized, typeErr := normalizeGroupType(input.GroupType)
			if typeErr != nil {
				return nil, typeErr
			}
			afterGroupType = normalized
			if beforeGroupType != afterGroupType {
				assignments = append(assignments, "group_type = ?")
				updateArgs = append(updateArgs, afterGroupType)
			}
			addChange("groupType", beforeGroupType, afterGroupType)
		}
		var policyInput any
		if hasSchedulingPolicyInput {
			policyInput = input.SchedulingPolicy
		}
		// Node compares the parsed policy objects (isDeepStrictEqual), so a
		// stored JSON differing only in key order or whitespace is a no-op.
		canonicalPolicy := func(value any) string {
			if value == nil {
				return "null"
			}
			encoded, marshalErr := json.Marshal(value)
			if marshalErr != nil {
				return fmt.Sprint(value)
			}
			return string(encoded)
		}
		parseCurrentPolicy := func() (any, error) {
			parsed, parseErr := parseStoredSchedulingPolicy(current.schedulingJSON.String, current.schedulingJSON.Valid, beforeGroupType, s.globalConcurrencyMax())
			if parseErr != nil {
				return nil, &ValidationError{Message: parseErr.Error()}
			}
			return parsed, nil
		}
		if beforeGroupType != afterGroupType {
			// Node: group-type transitions reset the stored policy to the
			// defaults (plus writable input) unconditionally.
			afterJSON, policyErr := schedulingPolicyJSON(afterGroupType, policyInput, s.globalConcurrencyMax())
			if policyErr != nil {
				return nil, policyErr
			}
			assignments = append(assignments, "scheduling_policy_json = ?")
			updateArgs = append(updateArgs, policyJSONString(afterJSON))
			if hasSchedulingPolicyInput {
				beforePolicy, beforeErr := parseCurrentPolicy()
				if beforeErr != nil {
					return nil, beforeErr
				}
				afterPolicy, afterErr := parseStoredSchedulingPolicy(string(afterJSON), true, afterGroupType, s.globalConcurrencyMax())
				if afterErr != nil {
					return nil, &ValidationError{Message: afterErr.Error()}
				}
				addChange("schedulingPolicy", canonicalPolicy(beforePolicy), canonicalPolicy(afterPolicy))
			}
		} else if hasSchedulingPolicyInput {
			beforePolicy, beforeErr := parseCurrentPolicy()
			if beforeErr != nil {
				return nil, beforeErr
			}
			afterJSON, policyErr := schedulingPolicyJSON(afterGroupType, policyInput, s.globalConcurrencyMax())
			if policyErr != nil {
				return nil, policyErr
			}
			afterPolicy, afterErr := parseStoredSchedulingPolicy(string(afterJSON), true, afterGroupType, s.globalConcurrencyMax())
			if afterErr != nil {
				return nil, &ValidationError{Message: afterErr.Error()}
			}
			if canonicalPolicy(beforePolicy) != canonicalPolicy(afterPolicy) {
				assignments = append(assignments, "scheduling_policy_json = ?")
				updateArgs = append(updateArgs, policyJSONString(afterJSON))
			}
			addChange("schedulingPolicy", canonicalPolicy(beforePolicy), canonicalPolicy(afterPolicy))
		}
	}

	result := &PatchResult{
		ID:                   current.id,
		Name:                 nextName,
		AccessType:           "owner",
		ChangedFields:        []string{},
		UpdatedAt:            current.updatedAt,
		OwnerSystemAccountID: current.systemAccountID,
		Changes:              changes,
	}
	if len(assignments) == 0 {
		return result, nil
	}

	if columnChanged(assignments, "provider_code") {
		providerCode, textErr := requiredText(input.ProviderCode, "供应商")
		if textErr != nil {
			return nil, textErr
		}
		if assertErr := s.assertProviderAvailable(ctx, tx, providerCode); assertErr != nil {
			return nil, assertErr
		}
		hasAccounts, hasErr := s.ownedGroupHasAccounts(ctx, tx, current.id)
		if hasErr != nil {
			return nil, hasErr
		}
		if hasAccounts {
			return nil, &ValidationError{Message: "已有账户的分组不允许修改供应商"}
		}
	}
	if columnChanged(assignments, "enabled") && !*input.Enabled && current.enabled {
		if _, guardErr := s.assertRouteStrategiesCanLoseGroupAvailability(ctx, tx, current.id, current.name, "停用分组"); guardErr != nil {
			return nil, guardErr
		}
	}

	nextUpdatedAt, err := nextGroupUpdatedAt(current.updatedAt, s.now())
	if err != nil {
		return nil, err
	}
	updateArgs = append(updateArgs, nextUpdatedAt)
	updateArgs = append(updateArgs, id, current.systemAccountID, current.updatedAt)
	update, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("groups")+` SET
		`+strings.Join(assignments, ", ")+`, updated_at = ?
		WHERE id = ? AND system_account_id = ? AND updated_at = ?`), updateArgs...)
	if err != nil {
		if duplicate := duplicateGroupNameError(err, nextName); duplicate != nil {
			return nil, duplicate
		}
		return nil, err
	}
	if affected, _ := update.RowsAffected(); affected != 1 {
		return nil, &ConflictError{Message: "分组已被其他操作更新，请刷新后重试"}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	result.UpdatedAt = nextUpdatedAt
	result.ChangedFields = sortedChangedFields(changes)
	if result.ChangedFields != nil && gatewayRuntimeChanged(result.ChangedFields) {
		s.invalidateRuntime("group_updated")
	}
	return result, nil
}

func columnChanged(assignments []string, column string) bool {
	for _, assignment := range assignments {
		if strings.HasPrefix(assignment, column+" ") {
			return true
		}
	}
	return false
}

func gatewayRuntimeChanged(fields []string) bool {
	for _, field := range fields {
		switch field {
		case "providerCode", "enabled", "groupType", "schedulingPolicy":
			return true
		}
	}
	return false
}

func sortedChangedFields(changes []Change) []string {
	fields := make([]string, 0, len(changes))
	for _, change := range changes {
		fields = append(fields, change.Field)
	}
	sortStrings(fields)
	return fields
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

// queryer abstracts *sql.DB / *sql.Tx reads so the transactional paths never
// touch s.db while a transaction holds the connection (the SQLite test
// runtime runs with MaxOpenConns(1)).
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Duplicate-free owner clause helpers and guards live next to their callers.

// DeleteResult mirrors DeleteGroupResult.
type DeleteResult struct {
	Deleted                 bool
	OwnerSystemAccountID    string
	Name                    string
	AffectedRouteStrategies []RouteStrategyChange
}

// RouteStrategyChange mirrors DeletedGroupRouteStrategyChange (the delete
// audit metadata sample rows).
type RouteStrategyChange struct {
	RouteStrategyID      string  `json:"routeStrategyId"`
	RouteStrategyName    string  `json:"routeStrategyName"`
	RemovedGroupID       string  `json:"removedGroupId"`
	RemovedGroupName     string  `json:"removedGroupName,omitempty"`
	RemovedBindingStatus *string `json:"removedBindingStatus,omitempty"`
}

// Delete mirrors deleteGroupAsync: default-group refusal, route-strategy
// binding preservation guard, hard delete cascading group_accounts +
// group_authorization_settings + route_strategy_groups, stats dirty marking
// and gateway invalidation.
func (s *Store) Delete(ctx context.Context, id string, access AccessScope) (*DeleteResult, error) {
	ctx = ensureCtx(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	clause, clauseArgs, ok := ownerClause(access)
	if !ok {
		return &DeleteResult{}, nil
	}
	where := ""
	args := []any{id}
	if clause != "" {
		where = " AND " + clause
		args = append(args, clauseArgs...)
	}
	var ownerID, name string
	var isDefault int
	err = tx.QueryRowContext(ctx, s.bind(`SELECT g.system_account_id, g.name, g.is_default
		FROM `+s.table("groups")+` g WHERE g.id = ?`+where+` LIMIT 1`), args...).Scan(&ownerID, &name, &isDefault)
	if errors.Is(err, sql.ErrNoRows) {
		return &DeleteResult{}, nil
	}
	if err != nil {
		return nil, err
	}
	if isDefault == 1 {
		return nil, &ValidationError{Message: "默认分组不能删除"}
	}
	affected, err := s.preserveRouteStrategiesBeforeDelete(ctx, tx, id, name)
	if err != nil {
		return nil, err
	}
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{s.bind(`DELETE FROM ` + s.table("route_strategy_groups") + ` WHERE group_id = ?`), []any{id}},
		{s.bind(`DELETE FROM ` + s.table("group_accounts") + ` WHERE group_id = ?`), []any{id}},
		{s.bind(`DELETE FROM ` + s.table("group_authorization_settings") + ` WHERE group_id = ?`), []any{id}},
	} {
		if _, err := tx.ExecContext(ctx, statement.sql, statement.args...); err != nil {
			return nil, err
		}
	}
	result, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("groups")+` WHERE id = ? AND system_account_id = ?`), id, ownerID)
	if err != nil {
		return nil, err
	}
	deleted, _ := result.RowsAffected()
	if deleted == 0 {
		return &DeleteResult{}, nil
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if err := s.refreshStatsAfterWrite(ctx, []string{id}, "group_deleted"); err != nil {
		return nil, err
	}
	// Node invalidateGroupLookupCache + invalidateGroupAccountIdsCache.
	s.invalidateRuntime("group_deleted")
	return &DeleteResult{
		Deleted:                 true,
		OwnerSystemAccountID:    ownerID,
		Name:                    name,
		AffectedRouteStrategies: affected,
	}, nil
}

// preserveRouteStrategiesBeforeDelete mirrors preserveRouteStrategiesBeforeGroupDeleteAsync
// plus assertAffectedRouteStrategiesCanLoseGroupAvailability (owner-branch
// binding counts; the authorized-grantee binding branch belongs to the
// authorization slice).
func (s *Store) preserveRouteStrategiesBeforeDelete(ctx context.Context, tx *sql.Tx, groupID, groupName string) ([]RouteStrategyChange, error) {
	return s.assertRouteStrategiesCanLoseGroupAvailability(ctx, tx, groupID, groupName, "删除分组")
}

// assertRouteStrategiesCanLoseGroupAvailability mirrors
// assertRouteStrategiesCanLoseGroupAvailabilityAsync +
// assertAffectedRouteStrategiesCanLoseGroupAvailabilityAsync: loads the
// active route strategies binding the group, refuses beyond the candidate
// limit, and refuses when the group is the only active enabled binding of a
// strategy. On success it returns the affected-strategy change list.
func (s *Store) assertRouteStrategiesCanLoseGroupAvailability(ctx context.Context, q queryer, groupID, groupName, actionLabel string) ([]RouteStrategyChange, error) {
	rows, err := q.QueryContext(ctx, s.bind(`SELECT
			route_strategy_groups.route_strategy_id,
			route_strategies.name,
			route_strategy_groups.status
		FROM `+s.table("route_strategy_groups")+` route_strategy_groups
		INNER JOIN `+s.table("route_strategies")+` route_strategies
			ON route_strategies.id = route_strategy_groups.route_strategy_id
			AND route_strategies.system_account_id = route_strategy_groups.system_account_id
			AND route_strategies.status = 'active'
		WHERE route_strategy_groups.group_id = ?
		ORDER BY route_strategy_groups.route_strategy_id ASC
		LIMIT ?`), groupID, maxRouteStrategyAvailabilityLossCandidates+1)
	if err != nil {
		return nil, err
	}
	type candidate struct {
		id, name, bindingStatus string
	}
	candidates := []candidate{}
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.id, &item.name, &item.bindingStatus); err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if len(candidates) == 0 {
		return nil, nil
	}
	if len(candidates) > maxRouteStrategyAvailabilityLossCandidates {
		return nil, &ValidationError{Message: "该分组关联的策略路由超过 100 个，请先分批解除绑定后再" + actionLabel}
	}

	strategyIDs := make([]string, 0, len(candidates))
	for _, item := range candidates {
		strategyIDs = append(strategyIDs, item.id)
	}
	activeCounts, err := s.activeBindingCountsExcludingGroup(ctx, q, groupID, strategyIDs)
	if err != nil {
		return nil, err
	}
	blockerNames := []string{}
	for _, item := range candidates {
		if item.bindingStatus != "active" {
			continue
		}
		if activeCounts[item.id] == 0 {
			blockerNames = append(blockerNames, item.name)
		}
	}
	if len(blockerNames) > 0 {
		subject := "该分组"
		if groupName != "" {
			subject = "“" + groupName + "”"
		}
		names := blockerNames
		suffix := ""
		if len(blockerNames) > 3 {
			names = blockerNames[:3]
			suffix = " 等 " + itoa(len(blockerNames)) + " 个"
		}
		return nil, &ValidationError{Message: "无法" + actionLabel + subject + "：该分组仍是以下策略路由的唯一可用启用分组：" + joinCN(names) + suffix + "。请先到策略路由中切换或新增启用分组，或删除这些策略路由后再操作。"}
	}

	changes := make([]RouteStrategyChange, 0, len(candidates))
	for _, item := range candidates {
		change := RouteStrategyChange{
			RouteStrategyID:   item.id,
			RouteStrategyName: item.name,
			RemovedGroupID:    groupID,
			RemovedGroupName:  groupName,
		}
		if item.bindingStatus != "" {
			bindingStatus := item.bindingStatus
			change.RemovedBindingStatus = &bindingStatus
		}
		changes = append(changes, change)
	}
	return changes, nil
}

// activeBindingCountsExcludingGroup mirrors
// loadActiveRouteStrategyGroupCountExcludingGroup: besides the owner's own
// enabled bindings it also counts groups the strategy's owning account holds
// through an active resource_authorizations row (not expired) whose
// authorization settings keep it enabled.
func (s *Store) activeBindingCountsExcludingGroup(ctx context.Context, q queryer, groupID string, strategyIDs []string) (map[string]int, error) {
	counts := map[string]int{}
	if len(strategyIDs) == 0 {
		return counts, nil
	}
	placeholders := make([]string, len(strategyIDs))
	args := []any{s.nowISO(), groupID}
	for i, id := range strategyIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	rows, err := q.QueryContext(ctx, s.bind(`SELECT route_strategy_groups.route_strategy_id, COUNT(*)
		FROM `+s.table("route_strategy_groups")+` route_strategy_groups
		INNER JOIN `+s.table("groups")+` groups
			ON groups.id = route_strategy_groups.group_id AND groups.enabled = 1
		LEFT JOIN `+s.table("resource_authorizations")+` group_authorization
			ON group_authorization.resource_type = 'group'
			AND group_authorization.resource_id = groups.id
			AND group_authorization.grantee_system_account_id = route_strategy_groups.system_account_id
			AND group_authorization.status = 'active'
			AND (group_authorization.expires_at IS NULL OR group_authorization.expires_at > ?)
		LEFT JOIN `+s.table("group_authorization_settings")+` group_authorization_settings
			ON group_authorization_settings.authorization_id = group_authorization.id
			AND group_authorization_settings.system_account_id = route_strategy_groups.system_account_id
			AND group_authorization_settings.group_id = groups.id
		WHERE route_strategy_groups.status = 'active'
			AND (
				groups.system_account_id = route_strategy_groups.system_account_id
				OR (group_authorization.id IS NOT NULL AND COALESCE(group_authorization_settings.enabled, 1) = 1)
			)
			AND route_strategy_groups.group_id <> ?
			AND route_strategy_groups.route_strategy_id IN (`+strings.Join(placeholders, ",")+`)
		GROUP BY route_strategy_groups.route_strategy_id`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var count int
		if err := rows.Scan(&id, &count); err != nil {
			return nil, err
		}
		counts[id] = count
	}
	return counts, rows.Err()
}

// refreshStatsAfterWrite mirrors refreshGroupAccountStatsAfterWrite: SQLite
// marks the dirty rows synchronously (a failure fails the request), while
// PostgreSQL hands the write to a background task whose failure only logs
// postgres_group_account_stats_dirty_mark_failed — the committed business
// write never turns into a 500 because the stats database hiccupped.
func (s *Store) refreshStatsAfterWrite(ctx context.Context, groupIDs []string, reason string) error {
	if !s.pg {
		return s.markStatsDirty(ctx, groupIDs, reason)
	}
	go func() {
		if err := s.markStatsDirty(context.Background(), groupIDs, reason); err != nil {
			slog.Error("PostgreSQL 分组账户统计脏标记写入失败",
				"event", "postgres_group_account_stats_dirty_mark_failed",
				"reason", reason,
				"error", err)
		}
	}()
	return nil
}

// markStatsDirty mirrors markGroupAccountStatsDirty /
// refreshGroupAccountStatsAfterWrite({groupIds, reason}): the refresh worker
// itself belongs to the J5 stats slice.
func (s *Store) markStatsDirty(ctx context.Context, groupIDs []string, reason string) error {
	ctx = ensureCtx(ctx)
	updatedAt := s.nowISO()
	for _, id := range uniqueStrings(groupIDs) {
		if _, err := s.db.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("group_account_stats_dirty")+` (group_id, reason, updated_at)
			VALUES (?, ?, ?)
			ON CONFLICT(group_id) DO UPDATE SET
				reason = excluded.reason,
				updated_at = excluded.updated_at`), id, reason, updatedAt); err != nil {
			return err
		}
	}
	return nil
}

// duplicateGroupNameError mirrors isDuplicateGroupNameError.
func duplicateGroupNameError(err error, name string) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	if strings.Contains(message, "idx_groups_owner_provider_name_unique") ||
		strings.Contains(message, "idx_groups_owner_provider_name_unique_lower") ||
		strings.Contains(message, "UNIQUE constraint failed: groups.system_account_id, groups.provider_code, groups.name") ||
		strings.Contains(message, "UNIQUE constraint failed: juhe_business.groups.system_account_id, juhe_business.groups.provider_code, juhe_business.groups.name") {
		return &ConflictError{Message: "同一供应商下分组名称已存在：" + name}
	}
	return nil
}

// nextGroupUpdatedAt mirrors nextGroupUpdatedAt: monotonic RFC3339 millis.
func nextGroupUpdatedAt(current string, now time.Time) (string, error) {
	parsed, err := time.Parse(time.RFC3339Nano, current)
	if err != nil {
		return "", &ValidationError{Message: "分组 updatedAt 必须是带 Z 或数值 offset 的 RFC3339 时间：" + current}
	}
	next := now.UnixMilli()
	if floor := parsed.UnixMilli() + 1; next < floor {
		next = floor
	}
	return isoMillis(time.UnixMilli(next)), nil
}

func requiredText(value *string, label string) (string, error) {
	if value == nil || strings.TrimSpace(*value) == "" {
		return "", &ValidationError{Message: label + "不能为空"}
	}
	return strings.TrimSpace(*value), nil
}

func nullableText(value *string, label string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil, nil
	}
	return ptrString(trimmed), nil
}

func sameNullableText(current sql.NullString, next *string) bool {
	if next == nil {
		return !current.Valid || current.String == ""
	}
	return current.Valid && current.String == *next
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func boolText(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func ptrString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func nullPtrString(value sql.NullString) *string {
	if !value.Valid || value.String == "" {
		return nil
	}
	return &value.String
}

// nullText renders a nullable column for change tracking.
func nullText(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func derefOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

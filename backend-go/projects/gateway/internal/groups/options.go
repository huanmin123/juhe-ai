// options.go ports the M05 low-value deferral surface of
// backend/src/modules/groups/groups.routes.ts: GET /options and
// GET /:id/edit-basic (backend/src/storage/group-summary.repository.ts +
// group-read.repository.ts).
//
// Access branches mirror queryGroupRowsForAccess / findGroupEditRowForAccess
// (group-read.repository.ts:180-229, 451-489): admins without a filter read
// the direct owner query; the filtered/non-admin branch reads the two-arm
// UNION — own groups plus the authorized view
// (resource_authorizations × groups LEFT JOIN group_authorization_settings,
// grantee-scoped runtime rows with status IN ('active','paused','expired'),
// same read contract as the authz slice's authorized reads) whose overrides
// project access_type='authorized' rows exactly like
// authorizedGroupRowSelectColumns / groupEditAuthorizedSelectColumns.
package groups

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayquota"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// ResourcePermissions mirrors ResourcePermissions (storage/resource-permissions.ts).
type ResourcePermissions struct {
	CanUse                 bool `json:"canUse"`
	CanEdit                bool `json:"canEdit"`
	CanDelete              bool `json:"canDelete"`
	CanReturnAuthorization bool `json:"canReturnAuthorization"`
	CanAuthorize           bool `json:"canAuthorize"`
	CanViewCredentials     bool `json:"canViewCredentials"`
	CanManageAccounts      bool `json:"canManageAccounts"`
	CanBindToApiKey        bool `json:"canBindToApiKey"`
}

// ownerPermissions mirrors ownerPermissions(): the projection every
// owner-branch group row carries.
func ownerPermissions() ResourcePermissions {
	return ResourcePermissions{
		CanUse:                 true,
		CanEdit:                true,
		CanDelete:              true,
		CanReturnAuthorization: false,
		CanAuthorize:           true,
		CanViewCredentials:     true,
		CanManageAccounts:      true,
		CanBindToApiKey:        true,
	}
}

// OptionSummary mirrors GroupOptionSummary (owner + authorized views).
type OptionSummary struct {
	ID                     string              `json:"id"`
	SystemAccountID        *string             `json:"systemAccountId,omitempty"`
	SystemAccountName      *string             `json:"systemAccountName,omitempty"`
	OwnerSystemAccountID   string              `json:"ownerSystemAccountId"`
	OwnerSystemAccountName *string             `json:"ownerSystemAccountName,omitempty"`
	Name                   string              `json:"name"`
	ProviderCode           string              `json:"providerCode"`
	Enabled                bool                `json:"enabled"`
	IsDefault              bool                `json:"isDefault"`
	GroupType              string              `json:"groupType"`
	SchedulingPolicy       any                 `json:"schedulingPolicy"`
	AccessType             string              `json:"accessType"`
	GroupAuthorizationID   *string             `json:"groupAuthorizationId,omitempty"`
	AuthorizationExpiresAt *string             `json:"authorizationExpiresAt,omitempty"`
	AuthorizationLimits    any                 `json:"authorizationLimits"`
	AuthorizationStatus    *string             `json:"authorizationStatus,omitempty"`
	Permissions            ResourcePermissions `json:"permissions"`
}

// EditDetail mirrors GroupEditDetail.
type EditDetail struct {
	Name             string  `json:"name"`
	ProviderCode     string  `json:"providerCode"`
	Description      *string `json:"description,omitempty"`
	Enabled          bool    `json:"enabled"`
	GroupType        string  `json:"groupType"`
	SchedulingPolicy any     `json:"schedulingPolicy"`
	UpdatedAt        string  `json:"updatedAt"`
}

// OptionsQuery mirrors parseGroupOptionListOptions after route validation.
type OptionsQuery struct {
	IDs            []string
	Keyword        string
	ProviderCode   string
	ManageableOnly bool
	PreferDefault  bool
	Limit          int // route-clamped to 1..50 (default 50)
	Page           int // default 1; only the integerQueryValue contract
}

// optionRow is the projected groups row the options surface reads (the
// groupRowSelectColumns subset the option summaries consume, plus the
// authorization columns of the authorized UNION arm).
type optionRow struct {
	id                      string
	systemAccountID         string
	name                    string
	providerCode            string
	enabled                 int64
	isDefault               int64
	groupType               string
	schedulingJSON          sql.NullString
	createdAt               string
	updatedAt               string
	accessType              string
	authorizationID         sql.NullString
	authorizationStatus     sql.NullString
	authorizationExpiresAt  sql.NullString
	authorizationLimitsJSON sql.NullString
}

const optionRowSelectColumns = `g.id, g.system_account_id, g.name, g.provider_code,
		g.enabled, g.is_default, g.group_type, g.scheduling_policy_json,
		g.created_at, g.updated_at`

const optionOuterSelectColumns = `id, system_account_id, name, provider_code,
		enabled, is_default, group_type, scheduling_policy_json,
		created_at, updated_at,
		access_type, authorization_id, authorization_status, authorization_expires_at, authorization_limits_json`

// authorizedOptionRowSelectColumns mirrors authorizedGroupRowSelectColumns:
// per-grantee settings overrides for enabled/group_type/scheduling_policy_json
// plus the settings-aware updated_at.
const authorizedOptionRowSelectColumns = `g.id, g.system_account_id, g.name, g.provider_code,
		CASE WHEN g.enabled = 1 THEN COALESCE(s.enabled, 1) ELSE 0 END AS enabled,
		g.is_default,
		COALESCE(s.group_type, g.group_type) AS group_type,
		CASE WHEN COALESCE(s.group_type, g.group_type) = 'high_concurrency'
			THEN COALESCE(s.scheduling_policy_json, g.scheduling_policy_json) ELSE NULL END AS scheduling_policy_json,
		g.created_at,
		COALESCE(s.updated_at, g.updated_at) AS updated_at`

// authorizedArmWhere mirrors the authorized-arm predicate: grantee-scoped
// runtime rows with the management-list status guard, excluding rows the
// owner arm already returned.
const authorizedArmWhere = `WHERE ra.resource_type = 'group'
		AND ra.grantee_system_account_id = ?
		AND ra.status IN ('active', 'paused', 'expired')
		AND g.system_account_id <> ?`

// authorizationSettingsJoin mirrors the LEFT JOIN every authorized arm shares
// (group-read.repository.ts:214-217 / 391-394 / 476-479).
func (s *Store) authorizationSettingsJoin() string {
	return `LEFT JOIN ` + s.table("group_authorization_settings") + ` s
		ON s.authorization_id = ra.id
		AND s.system_account_id = ra.grantee_system_account_id
		AND s.group_id = ra.resource_id`
}

func (s *Store) authorizedOptionArm() string {
	return `SELECT ` + authorizedOptionRowSelectColumns + `,
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

func scanOptionRow(scan func(...any) error) (optionRow, error) {
	var row optionRow
	err := scan(&row.id, &row.systemAccountID, &row.name, &row.providerCode,
		&row.enabled, &row.isDefault, &row.groupType, &row.schedulingJSON,
		&row.createdAt, &row.updatedAt,
		&row.accessType, &row.authorizationID, &row.authorizationStatus,
		&row.authorizationExpiresAt, &row.authorizationLimitsJSON)
	return row, err
}

// rowFilter mirrors buildGroupFilter over the union output (unqualified
// columns) or the direct owner query (alias-qualified columns).
func rowFilter(alias, providerCode, keyword string, ids []string) (clauses []string, args []any) {
	column := func(name string) string {
		if alias == "" {
			return name
		}
		return alias + "." + name
	}
	if len(ids) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
		clauses = append(clauses, column("id")+" IN ("+placeholders+")")
		for _, id := range ids {
			args = append(args, id)
		}
	}
	if code := strings.TrimSpace(providerCode); code != "" {
		clauses = append(clauses, column("provider_code")+" = ?")
		args = append(args, code)
	}
	if clause, clauseArgs := keywordFilterAlias(alias, keyword); clause != "" {
		clauses = append(clauses, clause)
		args = append(args, clauseArgs...)
	}
	return clauses, args
}

// Options mirrors listGroupSelectOptionsAsync (purpose=select returns
// GroupSelectOption rows the route shapes) and listGroupOptionsAsync
// (purpose=account returns GroupOptionSummary rows). The store always builds
// the full summaries; the route projects {id,name} for purpose=select.
// Access branches mirror listGroupOptionRowsForAccess: admins without a
// filter read all rows, manageableOnly reads the owner arm only, everything
// else reads the owner UNION ALL authorized rows of queryGroupRowsForAccess.
func (s *Store) Options(ctx context.Context, access AccessScope, query OptionsQuery) ([]OptionSummary, error) {
	ctx = ensureCtx(ctx)
	pageSize := query.Limit
	if pageSize < 1 {
		pageSize = 50
	}
	if pageSize > 500 {
		pageSize = 500
	}
	page := query.Page
	if page < 1 {
		page = 1
	}

	owner := access.manageableID()
	viewer := owner
	if viewer == "" {
		viewer = access.ViewerID
	}
	order := " ORDER BY updated_at DESC, id DESC"
	if query.PreferDefault {
		order = " ORDER BY is_default DESC, updated_at DESC, id DESC"
	}

	var (
		queryText string
		queryArgs []any
	)
	outerClauses, outerArgs := rowFilter("", query.ProviderCode, query.Keyword, query.IDs)
	outerWhere := ""
	if len(outerClauses) > 0 {
		outerWhere = " WHERE " + strings.Join(outerClauses, " AND ")
	}
	switch {
	case owner == "" && access.canAccessAll():
		// Direct owner query over every row (Node's unscoped admin branch).
		clauses, args := rowFilter("g", query.ProviderCode, query.Keyword, query.IDs)
		where := ""
		if len(clauses) > 0 {
			where = " WHERE " + strings.Join(clauses, " AND ")
		}
		orderColumn := "g.updated_at DESC, g.id DESC"
		if query.PreferDefault {
			orderColumn = "g.is_default DESC, g.updated_at DESC, g.id DESC"
		}
		queryText = `SELECT ` + optionRowSelectColumns + `,
			'owner' AS access_type,
			NULL AS authorization_id,
			NULL AS authorization_status,
			NULL AS authorization_expires_at,
			NULL AS authorization_limits_json
			FROM ` + s.table("groups") + ` g` + where + `
			ORDER BY ` + orderColumn
		queryArgs = args
	case query.ManageableOnly:
		if viewer == "" {
			return nil, &ValidationError{Message: "缺少系统账户上下文"}
		}
		clauses, args := rowFilter("g", query.ProviderCode, query.Keyword, query.IDs)
		clauses = append(clauses, "g.system_account_id = ?")
		args = append(args, viewer)
		orderColumn := "g.updated_at DESC, g.id DESC"
		if query.PreferDefault {
			orderColumn = "g.is_default DESC, g.updated_at DESC, g.id DESC"
		}
		queryText = `SELECT ` + optionRowSelectColumns + `,
			'owner' AS access_type,
			NULL AS authorization_id,
			NULL AS authorization_status,
			NULL AS authorization_expires_at,
			NULL AS authorization_limits_json
			FROM ` + s.table("groups") + ` g
			WHERE ` + strings.Join(clauses, " AND ") + `
			ORDER BY ` + orderColumn
		queryArgs = args
	default:
		if viewer == "" {
			return nil, &ValidationError{Message: "缺少系统账户上下文"}
		}
		queryText = `SELECT ` + optionOuterSelectColumns + ` FROM (
			SELECT ` + optionRowSelectColumns + `,
				'owner' AS access_type,
				NULL AS authorization_id,
				NULL AS authorization_status,
				NULL AS authorization_expires_at,
				NULL AS authorization_limits_json
			FROM ` + s.table("groups") + ` g
			WHERE g.system_account_id = ?
			UNION ALL
			` + s.authorizedOptionArm() + `
		) group_rows` + outerWhere + order
		queryArgs = append([]any{ownerOrViewer(owner, viewer), viewer, ownerOrViewer(owner, viewer)}, outerArgs...)
	}
	queryText += ` LIMIT ? OFFSET ?`
	queryArgs = append(queryArgs, pageSize, (page-1)*pageSize)

	rows, err := s.db.QueryContext(ctx, s.bind(queryText), queryArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []optionRow{}
	for rows.Next() {
		row, scanErr := scanOptionRow(rows.Scan)
		if scanErr != nil {
			return nil, scanErr
		}
		records = append(records, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	names, err := s.systemAccountNames(ctx, optionOwnerIDs(records))
	if err != nil {
		return nil, err
	}
	includeOwnerFields := access.canAccessAll()
	summaries := make([]OptionSummary, 0, len(records))
	for _, row := range records {
		summary, buildErr := s.newOptionSummary(row, names, includeOwnerFields, viewer)
		if buildErr != nil {
			return nil, buildErr
		}
		summaries = append(summaries, summary)
	}
	return summaries, nil
}

// RouteStrategyOptions mirrors listRouteStrategyGroupOptionRowsForAccessAsync +
// listRouteStrategyGroupOptionsAsync: the authorization-aware group rows
// projected to {id,name,providerCode,enabled}, with the authorized arm
// reflecting the per-grantee settings override of enabled.
func (s *Store) RouteStrategyOptions(ctx context.Context, access AccessScope, query OptionsQuery) ([]RouteStrategyGroupOption, error) {
	ctx = ensureCtx(ctx)
	pageSize := query.Limit
	if pageSize < 1 {
		pageSize = 50
	}
	if pageSize > 500 {
		pageSize = 500
	}
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
		clauses, args := rowFilter("g", query.ProviderCode, query.Keyword, query.IDs)
		where := ""
		if len(clauses) > 0 {
			where = " WHERE " + strings.Join(clauses, " AND ")
		}
		queryText = `SELECT g.id, g.name, g.provider_code, g.enabled
			FROM ` + s.table("groups") + ` g` + where + `
			ORDER BY g.updated_at DESC, g.id DESC
			LIMIT ?`
		queryArgs = args
	default:
		if viewer == "" {
			return nil, &ValidationError{Message: "缺少系统账户上下文"}
		}
		clauses, outerArgs := rowFilter("", query.ProviderCode, query.Keyword, query.IDs)
		outerWhere := ""
		if len(clauses) > 0 {
			outerWhere = " WHERE " + strings.Join(clauses, " AND ")
		}
		queryText = `SELECT id, name, provider_code, enabled FROM (
				SELECT g.id, g.name, g.provider_code, g.enabled, g.is_default, g.updated_at
				FROM ` + s.table("groups") + ` g
				WHERE g.system_account_id = ?
				UNION ALL
				SELECT g.id, g.name, g.provider_code,
					CASE WHEN g.enabled = 1 THEN COALESCE(s.enabled, 1) ELSE 0 END AS enabled,
					g.is_default,
					COALESCE(s.updated_at, g.updated_at) AS updated_at
				FROM ` + s.table("resource_authorizations") + ` ra
				INNER JOIN ` + s.table("groups") + ` g ON g.id = ra.resource_id
				` + s.authorizationSettingsJoin() + `
				` + authorizedArmWhere + `
			) group_rows` + outerWhere + `
			ORDER BY updated_at DESC, id DESC
			LIMIT ?`
		queryArgs = append([]any{ownerOrViewer(owner, viewer), viewer, ownerOrViewer(owner, viewer)}, outerArgs...)
	}
	queryArgs = append(queryArgs, pageSize)
	rows, err := s.db.QueryContext(ctx, s.bind(queryText), queryArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	options := []RouteStrategyGroupOption{}
	for rows.Next() {
		var option RouteStrategyGroupOption
		var enabled int64
		if err := rows.Scan(&option.ID, &option.Name, &option.ProviderCode, &enabled); err != nil {
			return nil, err
		}
		option.Enabled = enabled == 1
		options = append(options, option)
	}
	return options, rows.Err()
}

// ownerOrViewer mirrors Node's `ownerSystemAccountId ?? viewerSystemAccountId`
// page parameters (the two never disagree: manageableSystemAccountId already
// falls back to the viewer for non-admins).
func ownerOrViewer(owner, viewer string) string {	if owner != "" {
		return owner
	}
	return viewer
}

// newOptionSummary mirrors buildGroupOptionSummaries' per-row projection:
// authorized rows force isDefault=false, carry the runtime authorization
// columns plus the parsed limits document, and render
// authorizedGroupPermissions(canBindAuthorizedGroupRowToApiKey(row)) whenever
// the row's owner differs from the viewer. Corrupted stored group types or
// scheduling policies propagate as read errors (the Node read path throws).
func (s *Store) newOptionSummary(row optionRow, names map[string]string, includeOwnerFields bool, viewer string) (OptionSummary, error) {
	authorized := row.accessType == "authorized"
	groupType, err := normalizeStoredGroupType(row.groupType)
	if err != nil {
		return OptionSummary{}, err
	}
	policy, err := parseStoredSchedulingPolicy(row.schedulingJSON.String, row.schedulingJSON.Valid, groupType, s.globalConcurrencyMax())
	if err != nil {
		return OptionSummary{}, err
	}
	summary := OptionSummary{
		ID:                     row.id,
		OwnerSystemAccountID:   row.systemAccountID,
		OwnerSystemAccountName: ptrString(names[row.systemAccountID]),
		Name:                   row.name,
		ProviderCode:           row.providerCode,
		Enabled:                row.enabled == 1,
		IsDefault:              !authorized && row.isDefault == 1,
		GroupType:              groupType,
		SchedulingPolicy:       policy,
		AccessType:             row.accessType,
		GroupAuthorizationID:   nullPtrString(row.authorizationID),
		AuthorizationExpiresAt: nullPtrString(row.authorizationExpiresAt),
		AuthorizationStatus:    nullPtrString(row.authorizationStatus),
		Permissions:            ownerPermissions(),
	}
	limits, err := parseAuthorizationLimitsView(row.authorizationLimitsJSON)
	if err != nil {
		return OptionSummary{}, err
	}
	summary.AuthorizationLimits = limits
	if authorized && row.systemAccountID != viewer {
		canBind, err := s.canBindAuthorizedGroupRow(row)
		if err != nil {
			return OptionSummary{}, err
		}
		summary.Permissions = authorizedGroupPermissions(canBind, false)
	}
	if includeOwnerFields {
		summary.SystemAccountID = ptrString(row.systemAccountID)
		summary.SystemAccountName = ptrString(names[row.systemAccountID])
	}
	return summary, nil
}

// EditDetail mirrors findGroupEditRowForAccess: admins read any row
// (unscoped when no filter is set), every other scope reads the two-arm
// union where the authorized arm applies the per-grantee settings overrides
// (groupEditAuthorizedSelectColumns).
func (s *Store) EditDetail(ctx context.Context, id string, access AccessScope) (*EditDetail, error) {
	ctx = ensureCtx(ctx)
	owner := access.manageableID()
	viewer := owner
	if viewer == "" {
		viewer = access.ViewerID
	}
	var (
		name           string
		providerCode   string
		description    sql.NullString
		enabled        int64
		groupType      string
		schedulingJSON sql.NullString
		updatedAt      string
	)
	var queryText string
	var args []any
	if owner == "" && access.canAccessAll() {
		queryText = `SELECT g.name, g.provider_code, g.description, g.enabled,
			g.group_type, g.scheduling_policy_json, g.updated_at
			FROM ` + s.table("groups") + ` g WHERE g.id = ? LIMIT 1`
		args = []any{id}
	} else {
		if viewer == "" {
			return nil, &ValidationError{Message: "缺少系统账户上下文"}
		}
		queryText = `SELECT name, provider_code, description, enabled, group_type, scheduling_policy_json, updated_at FROM (
			SELECT g.name, g.provider_code, g.description, g.enabled, g.group_type, g.scheduling_policy_json, g.updated_at
			FROM ` + s.table("groups") + ` g
			WHERE g.id = ? AND g.system_account_id = ?
			UNION ALL
			SELECT g.name, g.provider_code, g.description,
				CASE WHEN g.enabled = 1 THEN COALESCE(s.enabled, 1) ELSE 0 END AS enabled,
				COALESCE(s.group_type, g.group_type) AS group_type,
				CASE WHEN COALESCE(s.group_type, g.group_type) = 'high_concurrency'
					THEN COALESCE(s.scheduling_policy_json, g.scheduling_policy_json) ELSE NULL END AS scheduling_policy_json,
				COALESCE(s.updated_at, g.updated_at) AS updated_at
			FROM ` + s.table("resource_authorizations") + ` ra
			INNER JOIN ` + s.table("groups") + ` g ON g.id = ra.resource_id
			` + s.authorizationSettingsJoin() + `
			WHERE g.id = ?
				AND ra.resource_type = 'group'
				AND ra.grantee_system_account_id = ?
				AND ra.status IN ('active', 'paused', 'expired')
				AND g.system_account_id <> ?
		) group_edit_rows LIMIT 1`
		args = []any{id, ownerOrViewer(owner, viewer), id, viewer, ownerOrViewer(owner, viewer)}
	}
	err := s.db.QueryRowContext(ctx, s.bind(queryText), args...).Scan(
		&name, &providerCode, &description, &enabled, &groupType, &schedulingJSON, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	normalizedType, err := normalizeStoredGroupType(groupType)
	if err != nil {
		return nil, err
	}
	policy, err := parseStoredSchedulingPolicy(schedulingJSON.String, schedulingJSON.Valid, normalizedType, s.globalConcurrencyMax())
	if err != nil {
		return nil, err
	}
	return &EditDetail{
		Name:             name,
		ProviderCode:     providerCode,
		Description:      nullPtrString(description),
		Enabled:          enabled == 1,
		GroupType:        normalizedType,
		SchedulingPolicy: policy,
		UpdatedAt:        updatedAt,
	}, nil
}

func optionOwnerIDs(rows []optionRow) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.systemAccountID)
	}
	return ids
}

// optionsHandler mirrors GET /options (groups.routes.ts:54): purpose gates
// the projection — 'select' renders {id,name} rows, 'account' renders the
// full GroupOptionSummary; any other value rejects with the Node 400 message.
func (d *Deps) options(w http.ResponseWriter, r *http.Request, access AccessScope) {
	query := r.URL.Query()
	for _, name := range []string{"ids", "keyword", "providerCode", "limit", "manageableOnly", "preferDefault", "purpose"} {
		if len(query[name]) > 1 {
			kernel.WriteBadRequest(w, "分组选项 purpose 仅支持 select 或 account")
			return
		}
	}
	ids := queryTextList(query["ids"], 50)
	keyword := strings.TrimSpace(query.Get("keyword"))
	providerCode := strings.TrimSpace(query.Get("providerCode"))
	limit := optionLimitValue(query.Get("limit"))
	manageableOnly := booleanQueryValue(query.Get("manageableOnly"))
	preferDefault := booleanQueryValue(query.Get("preferDefault"))
	purpose, purposeOK := groupOptionPurpose(query.Get("purpose"))
	if !purposeOK {
		kernel.WriteBadRequest(w, "分组选项 purpose 仅支持 select 或 account")
		return
	}
	summaries, err := d.Store.Options(r.Context(), access, OptionsQuery{
		IDs:            ids,
		Keyword:        keyword,
		ProviderCode:   providerCode,
		ManageableOnly: manageableOnly,
		PreferDefault:  preferDefault,
		Limit:          limit,
	})
	if err != nil {
		d.writeOptionsError(w, err)
		return
	}
	if purpose == "select" {
		projected := make([]map[string]string, 0, len(summaries))
		for _, summary := range summaries {
			projected = append(projected, map[string]string{"id": summary.ID, "name": summary.Name})
		}
		kernel.WriteOK(w, projected, "")
		return
	}
	kernel.WriteOK(w, summaries, "")
}

// editBasic mirrors GET /:id/edit-basic (groups.routes.ts:103).
func (d *Deps) editBasic(w http.ResponseWriter, r *http.Request, access AccessScope) {
	detail, err := d.Store.EditDetail(r.Context(), r.PathValue("id"), access)
	if err != nil {
		d.writeOptionsError(w, err)
		return
	}
	if detail == nil {
		kernel.WriteError(w, http.StatusNotFound, "分组不存在")
		return
	}
	kernel.WriteOK(w, detail, "")
}

// GroupAuthorizationOption mirrors GroupAuthorizationOption
// (listGroupAuthorizationOptionsAsync).
type GroupAuthorizationOption struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	CanAuthorize bool   `json:"canAuthorize"`
}

// authorizationOptions mirrors GET /authorization-options
// (groups.routes.ts:71): the same authorization-aware group options projected
// to {id,name,canAuthorize} for the authorization editor.
func (d *Deps) authorizationOptions(w http.ResponseWriter, r *http.Request, access AccessScope) {
	query, ok := parseGroupOptionQuery(w, r)
	if !ok {
		return
	}
	summaries, err := d.Store.Options(r.Context(), access, query)
	if err != nil {
		d.writeOptionsError(w, err)
		return
	}
	options := make([]GroupAuthorizationOption, 0, len(summaries))
	for _, summary := range summaries {
		options = append(options, GroupAuthorizationOption{
			ID:           summary.ID,
			Name:         summary.Name,
			CanAuthorize: summary.Permissions.CanAuthorize,
		})
	}
	kernel.WriteOK(w, options, "")
}

// AccountGroupOptionSummary mirrors AccountGroupOptionSummary
// (listAccountGroupOptionsAsync): the full option summary plus accountIds —
// the owner view lists the group's member account ids while the authorized
// view always returns an empty array so the owner's accounts never leak.
type AccountGroupOptionSummary struct {
	OptionSummary
	AccountIDs []string `json:"accountIds"`
}

// accountOptions mirrors GET /account-options (groups.routes.ts:82).
func (d *Deps) accountOptions(w http.ResponseWriter, r *http.Request, access AccessScope) {
	query, ok := parseGroupOptionQuery(w, r)
	if !ok {
		return
	}
	owner := access.manageableID()
	viewer := ownerOrViewer(owner, access.ViewerID)
	summaries, err := d.Store.Options(r.Context(), access, query)
	if err != nil {
		d.writeOptionsError(w, err)
		return
	}
	ids := make([]string, 0, len(summaries))
	for _, summary := range summaries {
		ids = append(ids, summary.ID)
	}
	accountIDsByGroup, err := d.Store.groupAccountIDsByGroupIDs(r.Context(), ids)
	if err != nil {
		d.writeOptionsError(w, err)
		return
	}
	options := make([]AccountGroupOptionSummary, 0, len(summaries))
	for _, summary := range summaries {
		accountIDs := []string{}
		if !(summary.AccessType == "authorized" && summary.OwnerSystemAccountID != viewer) {
			if groupIDs, exists := accountIDsByGroup[summary.ID]; exists {
				accountIDs = groupIDs
			}
		}
		options = append(options, AccountGroupOptionSummary{OptionSummary: summary, AccountIDs: accountIDs})
	}
	kernel.WriteOK(w, options, "")
}

// RouteStrategyGroupOption mirrors RouteStrategyGroupOption
// (listRouteStrategyGroupOptionsAsync): the authorization settings override
// of enabled reflected back to the route-strategy selector.
type RouteStrategyGroupOption struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	ProviderCode string `json:"providerCode"`
	Enabled      bool   `json:"enabled"`
}

// routeStrategyOptions mirrors GET /route-strategy-options
// (groups.routes.ts:93): ids/keyword/providerCode/limit over the
// authorization-aware group rows with {id,name,providerCode,enabled}.
func (d *Deps) routeStrategyOptions(w http.ResponseWriter, r *http.Request, access AccessScope) {
	query, ok := parseGroupOptionQuery(w, r)
	if !ok {
		return
	}
	options, err := d.Store.RouteStrategyOptions(r.Context(), access, query)
	if err != nil {
		d.writeOptionsError(w, err)
		return
	}
	kernel.WriteOK(w, options, "")
}

// parseGroupOptionQuery mirrors parseGroupOptionListOptions without the
// purpose gate: ids/keyword/providerCode/limit/manageableOnly/preferDefault.
func parseGroupOptionQuery(w http.ResponseWriter, r *http.Request) (OptionsQuery, bool) {
	query := r.URL.Query()
	for _, name := range []string{"ids", "keyword", "providerCode", "limit", "manageableOnly", "preferDefault"} {
		if len(query[name]) > 1 {
			kernel.WriteBadRequest(w, "分组参数无效")
			return OptionsQuery{}, false
		}
	}
	return OptionsQuery{
		IDs:            queryTextList(query["ids"], 50),
		Keyword:        strings.TrimSpace(query.Get("keyword")),
		ProviderCode:   strings.TrimSpace(query.Get("providerCode")),
		ManageableOnly: booleanQueryValue(query.Get("manageableOnly")),
		PreferDefault:  booleanQueryValue(query.Get("preferDefault")),
		Limit:          optionLimitValue(query.Get("limit")),
	}, true
}

func (d *Deps) writeOptionsError(w http.ResponseWriter, err error) {
	var validation *ValidationError
	if errors.As(err, &validation) {
		kernel.WriteBadRequest(w, validation.Message)
		return
	}
	kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
}

// queryTextList mirrors shared/query-values.ts queryTextList: comma-split,
// trimmed, deduplicated, capped at maxItems.
func queryTextList(values []string, maxItems int) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0, len(values))
	for _, value := range values {
		for _, piece := range strings.Split(value, ",") {
			text := strings.TrimSpace(piece)
			if text == "" {
				continue
			}
			if _, ok := seen[text]; ok {
				continue
			}
			seen[text] = struct{}{}
			out = append(out, text)
			if len(out) >= maxItems {
				return out
			}
		}
	}
	return out
}

// optionLimitValue mirrors optionLimitValue + integerQueryValue: integers
// clamp to 1..50, anything else falls back to 50.
func optionLimitValue(raw string) int {
	text := strings.TrimSpace(raw)
	if text == "" {
		return 50
	}
	value, err := strconv.Atoi(text)
	if err != nil {
		return 50
	}
	if value < 1 {
		return 1
	}
	if value > 50 {
		return 50
	}
	return value
}

// booleanQueryValue mirrors the Node query boolean: 1/true/yes and
// 0/false/no (case-insensitive), everything else absent.
func booleanQueryValue(raw string) bool {
	text := strings.ToLower(strings.TrimSpace(raw))
	switch text {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

// groupOptionPurpose mirrors groupOptionPurpose: absent and 'select' both
// normalize to 'select' (the 400 fires only on a value that is neither
// select nor account), 'account' → 'account', anything else invalid.
func groupOptionPurpose(value string) (string, bool) {
	purpose := strings.TrimSpace(value)
	if purpose == "" || purpose == "select" {
		return "select", true
	}
	if purpose == "account" {
		return "account", true
	}
	return "", false
}

// keywordFilterAlias is keywordFilter with a controllable column prefix (the
// union's outer WHERE reads the unqualified output columns).
func keywordFilterAlias(alias, keyword string) (string, []any) {
	text := strings.TrimSpace(keyword)
	if text == "" {
		return "", nil
	}
	upper := textPrefixUpperBound(text)
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	return "(" + prefix + "name >= ? AND " + prefix + "name < ? OR " + prefix + "provider_code >= ? AND " + prefix + "provider_code < ?)",
		[]any{text, upper, text, upper}
}

// authorizedGroupPermissions mirrors authorizedGroupPermissions
// (storage/resource-permissions.ts): the authorized base with editing kept,
// canReturnAuthorization threaded through, and canBindToApiKey gated on the
// runtime row state.
func authorizedGroupPermissions(canBindToApiKey, canReturnAuthorization bool) ResourcePermissions {
	return ResourcePermissions{
		CanUse:                 true,
		CanEdit:                true,
		CanDelete:              false,
		CanReturnAuthorization: canReturnAuthorization,
		CanAuthorize:           false,
		CanViewCredentials:     false,
		CanManageAccounts:      false,
		CanBindToApiKey:        canBindToApiKey,
	}
}

// canBindAuthorizedGroupRow mirrors canBindAuthorizedGroupRowToApiKey:
// enabled && authorization_status='active' && !isResourceAuthorizationExpired.
func (s *Store) canBindAuthorizedGroupRow(row optionRow) (bool, error) {
	if row.enabled != 1 {
		return false, nil
	}
	if !row.authorizationStatus.Valid || row.authorizationStatus.String != "active" {
		return false, nil
	}
	expired, err := s.authorizationExpired(row.authorizationExpiresAt)
	if err != nil {
		return false, err
	}
	return !expired, nil
}

// authorizationExpired mirrors isResourceAuthorizationExpired: absent
// expires_at never expires; a malformed instant is a contract error (the
// Node read path throws rfc3339InstantMilliseconds' message).
func (s *Store) authorizationExpired(expiresAt sql.NullString) (bool, error) {
	if !expiresAt.Valid || strings.TrimSpace(expiresAt.String) == "" {
		return false, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(expiresAt.String))
	if err != nil {
		return false, &ValidationError{Message: "授权 expiresAt 必须是带 Z 或数值 offset 的 RFC3339 时间：" + expiresAt.String}
	}
	return parsed.UnixMilli() <= s.now().UnixMilli(), nil
}

// authorizedQuotaLimit/authorizedQuotaLimits mirror the JSON projection of
// RequestQuotaLimit/RequestHourlyQuotaLimit/RequestQuotaLimits (disabled
// entries stripped by normalization, omitted from the document).
type authorizedQuotaLimit struct {
	Enabled bool    `json:"enabled"`
	Hours   int     `json:"hours,omitempty"`
	Limit   float64 `json:"limit"`
}

type authorizedQuotaLimits struct {
	Hourly  *authorizedQuotaLimit `json:"hourly,omitempty"`
	Daily   *authorizedQuotaLimit `json:"daily,omitempty"`
	Weekly  *authorizedQuotaLimit `json:"weekly,omitempty"`
	Monthly *authorizedQuotaLimit `json:"monthly,omitempty"`
	Total   *authorizedQuotaLimit `json:"total,omitempty"`
}

// parseAuthorizationLimitsView mirrors parseRequestQuotaLimitsJson: absent
// columns render the empty limits document, stored documents are normalized
// through the shared gatewayquota alignment (ParseRequestQuotaLimitsJSON)
// and parse/normalize errors propagate to the caller (the Node read path
// throws into the route's 500).
func parseAuthorizationLimitsView(raw sql.NullString) (any, error) {
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return struct{}{}, nil
	}
	limits, err := gatewayquota.ParseRequestQuotaLimitsJSON(raw.String)
	if err != nil {
		return nil, err
	}
	view := authorizedQuotaLimits{}
	if limits.Hourly != nil {
		view.Hourly = &authorizedQuotaLimit{Enabled: limits.Hourly.Enabled, Hours: limits.Hourly.Hours, Limit: limits.Hourly.Limit}
	}
	convert := func(limit *gatewayquota.QuotaLimit) *authorizedQuotaLimit {
		if limit == nil {
			return nil
		}
		return &authorizedQuotaLimit{Enabled: limit.Enabled, Limit: limit.Limit}
	}
	view.Daily = convert(limits.Daily)
	view.Weekly = convert(limits.Weekly)
	view.Monthly = convert(limits.Monthly)
	view.Total = convert(limits.Total)
	return view, nil
}

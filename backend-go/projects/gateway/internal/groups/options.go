// options.go ports the M05 low-value deferral surface of
// backend/src/modules/groups/groups.routes.ts: GET /options and
// GET /:id/edit-basic (backend/src/storage/group-summary.repository.ts +
// group-read.repository.ts).
//
// Scope note: the owner/admin branches of the Node access query are ported
// byte-for-byte. The authorized-visibility UNION arm of the same queries
// stays with the M05 authorized read-branch deferral (it needs the M04
// resource_authorizations join), so authorized rows are not part of this
// surface yet — exactly the rows the pre-M05 owner slice already skipped.
package groups

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"

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

// OptionSummary mirrors GroupOptionSummary for the owner branch.
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
// groupRowSelectColumns subset the option summaries consume).
type optionRow struct {
	id              string
	systemAccountID string
	name            string
	providerCode    string
	enabled         int64
	isDefault       int64
	groupType       string
	schedulingJSON  sql.NullString
}

// Options mirrors listGroupSelectOptionsAsync (purpose=select returns
// GroupSelectOption rows the route shapes) and listGroupOptionsAsync
// (purpose=account returns GroupOptionSummary rows). The store always builds
// the full summaries; the route projects {id,name} for purpose=select.
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

	clauses := []string{}
	args := []any{}
	if !access.canAccessAll() {
		if access.ViewerID == "" {
			return nil, &ValidationError{Message: "缺少系统账户上下文"}
		}
		owner := access.manageableID()
		if query.ManageableOnly && owner != "" {
			clauses = append(clauses, "g.system_account_id = ?")
			args = append(args, owner)
		} else {
			clauses = append(clauses, "g.system_account_id = ?")
			args = append(args, access.ViewerID)
		}
	} else if owner := access.manageableID(); query.ManageableOnly && owner != "" {
		clauses = append(clauses, "g.system_account_id = ?")
		args = append(args, owner)
	}
	if len(query.IDs) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(query.IDs)), ",")
		clauses = append(clauses, "g.id IN ("+placeholders+")")
		for _, id := range query.IDs {
			args = append(args, id)
		}
	}
	if providerCode := strings.TrimSpace(query.ProviderCode); providerCode != "" {
		clauses = append(clauses, "g.provider_code = ?")
		args = append(args, providerCode)
	}
	if clause, clauseArgs := keywordFilter(query.Keyword); clause != "" {
		clauses = append(clauses, clause)
		args = append(args, clauseArgs...)
	}
	where := ""
	if len(clauses) > 0 {
		where = " WHERE " + strings.Join(clauses, " AND ")
	}
	order := " ORDER BY g.updated_at DESC, g.id DESC"
	if query.PreferDefault {
		order = " ORDER BY g.is_default DESC, g.updated_at DESC, g.id DESC"
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT g.id, g.system_account_id, g.name, g.provider_code,
		g.enabled, g.is_default, g.group_type, g.scheduling_policy_json
		FROM `+s.table("groups")+` g`+where+order+` LIMIT ? OFFSET ?`),
		append(args, pageSize, (page-1)*pageSize)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []optionRow{}
	for rows.Next() {
		var row optionRow
		if err := rows.Scan(&row.id, &row.systemAccountID, &row.name, &row.providerCode,
			&row.enabled, &row.isDefault, &row.groupType, &row.schedulingJSON); err != nil {
			return nil, err
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
		summary := OptionSummary{
			ID:                     row.id,
			OwnerSystemAccountID:   row.systemAccountID,
			OwnerSystemAccountName: ptrString(names[row.systemAccountID]),
			Name:                   row.name,
			ProviderCode:           row.providerCode,
			Enabled:                row.enabled == 1,
			IsDefault:              row.isDefault == 1,
			GroupType:              normalizeGroupTypeStored(row.groupType),
			SchedulingPolicy:       parseSchedulingPolicy(row.schedulingJSON.String, row.schedulingJSON.Valid, row.groupType),
			AccessType:             "owner",
			Permissions:            ownerPermissions(),
		}
		if includeOwnerFields {
			summary.SystemAccountID = ptrString(row.systemAccountID)
			summary.SystemAccountName = ptrString(names[row.systemAccountID])
		}
		summaries = append(summaries, summary)
	}
	return summaries, nil
}

// EditDetail mirrors findGroupEditRowForAccess: admins read any row, the
// self surface reads caller-owned rows only (Node's first UNION arm).
func (s *Store) EditDetail(ctx context.Context, id string, access AccessScope) (*EditDetail, error) {
	ctx = ensureCtx(ctx)
	where := " WHERE g.id = ?"
	args := []any{id}
	if !access.canAccessAll() || access.manageableID() != "" {
		owner := access.manageableID()
		if owner == "" {
			owner = access.ViewerID
		}
		if owner == "" {
			return nil, &ValidationError{Message: "缺少系统账户上下文"}
		}
		where += " AND g.system_account_id = ?"
		args = append(args, owner)
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
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT g.name, g.provider_code, g.description, g.enabled,
		g.group_type, g.scheduling_policy_json, g.updated_at
		FROM `+s.table("groups")+` g`+where+` LIMIT 1`), args...).Scan(
		&name, &providerCode, &description, &enabled, &groupType, &schedulingJSON, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &EditDetail{
		Name:             name,
		ProviderCode:     providerCode,
		Description:      nullPtrString(description),
		Enabled:          enabled == 1,
		GroupType:        normalizeGroupTypeStored(groupType),
		SchedulingPolicy: parseSchedulingPolicy(schedulingJSON.String, schedulingJSON.Valid, groupType),
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

// Public group family (list/add/update/del), ported from the group branches
// of external-integrations.routes.ts plus external-public-account-push
// service/target/payload helpers (listPublicGroupsAsync, addPublicGroupAsync,
// updatePublicGroupAsync, deletePublicGroupAsync). The public group list runs
// its own lightweight SQL because the Node public projection
// (sanitizeGroup: 7 fields, manageableOnly owner rows only, provider_code
// filter) has no equivalent in the management store.
package aipublic

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/groups"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// groupListQuery mirrors groupListQuerySchema (strict).
type groupListQuery struct {
	TargetUsername string
	ProviderCode   string
	HasProvider    bool
	Keyword        string
	HasKeyword     bool
	Page           int
	HasPage        bool
	PageSize       int
	HasPageSize    bool
}

func parseGroupListQuery(values url.Values) (*groupListQuery, string) {
	unknown := strictObjectKeys(valuesAsMap(values), "targetUsername", "providerCode", "keyword", "page", "pageSize")
	if unknown != nil {
		return nil, zodUnrecognizedKeys(unknown...)
	}
	query := &groupListQuery{}
	username, issue := parseQueryString(values, "targetUsername", true, 2, 80)
	if issue != "" {
		return nil, issue
	}
	query.TargetUsername = username
	provider, has, issue := parseOptionalQueryString(values, "providerCode", 1, 60)
	if issue != "" {
		return nil, issue
	}
	query.ProviderCode, query.HasProvider = provider, has
	keyword, has, issue := parseOptionalQueryString(values, "keyword", 0, 80)
	if issue != "" {
		return nil, issue
	}
	query.Keyword, query.HasKeyword = keyword, has
	page, has, issue := parseOptionalQueryInt(values, "page", 1, 0)
	if issue != "" {
		return nil, issue
	}
	query.Page, query.HasPage = page, has
	pageSize, has, issue := parseOptionalQueryInt(values, "pageSize", 1, 100)
	if issue != "" {
		return nil, issue
	}
	query.PageSize, query.HasPageSize = pageSize, has
	return query, ""
}

// valuesAsMap exposes the query keys for the strict-key check.
func valuesAsMap(values url.Values) map[string]any {
	out := make(map[string]any, len(values))
	for key := range values {
		out[key] = values.Get(key)
	}
	return out
}

func (d *Deps) listGroups(w http.ResponseWriter, r *http.Request) {
	query, issue := parseGroupListQuery(r.URL.Query())
	if issue != "" {
		kernel.WriteBadRequest(w, issue)
		return
	}
	context := AuthContextFrom(r)
	if context.IsTestToken {
		page, pageSize := d.mockPaging(query.HasPage, query.Page, query.HasPageSize, query.PageSize)
		d.mockGroupList(w, map[string]string{
			"targetUsername": query.TargetUsername,
			"keyword":        query.Keyword,
			"providerCode":   query.ProviderCode,
		}, page, pageSize)
		return
	}
	target, err := d.requirePublicTarget(r.Context(), query.TargetUsername)
	if err != nil {
		d.writeServiceError(w, err, "分组列表读取失败")
		return
	}
	if target.Account.Status != "active" {
		kernel.WriteBadRequest(w, "目标用户已停用："+target.Account.Username)
		return
	}
	page, pageSize := d.paging(query.HasPage, query.Page, query.HasPageSize, query.PageSize)
	result, err := d.listGroupRows(r.Context(), target.SystemAccountID, page, pageSize, query.Keyword, query.ProviderCode)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	d.writeStatsEnvelope(w, map[string]any{
		"target":         target.Public,
		"page":           page,
		"pageSize":       pageSize,
		"pageUpperBound": result.total,
		"hasMore":        result.hasMore,
		"items":          result.items,
	})
}

type groupRowsPage struct {
	items   []PublicGroupSummary
	total   int
	hasMore bool
}

// listGroupRows mirrors listGroupsPageAsync(manageableOnly): owner rows only,
// provider_code equality, keyword prefix range, updated_at DESC ordering.
func (d *Deps) listGroupRows(ctx context.Context, ownerID string, page, pageSize int, keyword, providerCode string) (*groupRowsPage, error) {
	clauses := []string{"groups.system_account_id = ?"}
	args := []any{ownerID}
	if providerCode != "" {
		clauses = append(clauses, "groups.provider_code = ?")
		args = append(args, providerCode)
	}
	if keyword != "" {
		upper := textPrefixUpperBound(keyword)
		clauses = append(clauses, "((groups.name >= ? AND groups.name < ?) OR (groups.provider_code >= ? AND groups.provider_code < ?))")
		args = append(args, keyword, upper, keyword, upper)
	}
	args = append(args, pageSize+1, (page-1)*pageSize)
	rows, err := d.db().QueryContext(ctx, d.bind(`SELECT groups.id, groups.name, groups.provider_code,
		groups.description, groups.enabled, groups.group_type, groups.is_default
	FROM `+d.table("groups")+` AS groups
	WHERE `+strings.Join(clauses, " AND ")+`
	ORDER BY groups.updated_at DESC, groups.id DESC
	LIMIT ? OFFSET ?`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	pageRows := []PublicGroupSummary{}
	for rows.Next() {
		var item PublicGroupSummary
		var description sql.NullString
		var enabled, isDefault int64
		if err := rows.Scan(&item.ID, &item.Name, &item.ProviderCode, &description, &enabled, &item.GroupType, &isDefault); err != nil {
			return nil, err
		}
		item.Enabled = enabled == 1
		item.IsDefault = isDefault == 1
		if description.Valid {
			value := description.String
			item.Description = &value
		}
		pageRows = append(pageRows, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	hasMore := len(pageRows) > pageSize
	if hasMore {
		pageRows = pageRows[:pageSize]
	}
	return &groupRowsPage{items: pageRows, total: pagedTotalUpperBound(page, pageSize, len(pageRows), hasMore), hasMore: hasMore}, nil
}

// textPrefixUpperBound mirrors textPrefixUpperBound (last code point + 1).
func textPrefixUpperBound(value string) string {
	runes := []rune(value)
	for index := len(runes) - 1; index >= 0; index-- {
		if runes[index] < 0x10ffff {
			return string(runes[:index]) + string(runes[index]+1)
		}
	}
	return value + "\uffff"
}

// pagedTotalUpperBound mirrors pagedTotalUpperBound.
func pagedTotalUpperBound(page, pageSize, pageLen int, hasMore bool) int {
	return (page-1)*pageSize + pageLen + boolToInt(hasMore)
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (d *Deps) paging(hasPage bool, page int, hasPageSize bool, pageSize int) (int, int) {
	currentPage := 1
	if hasPage && page > 0 {
		currentPage = page
	}
	size := 20
	if hasPageSize && pageSize > 0 {
		size = pageSize
	}
	return currentPage, size
}

func (d *Deps) mockPaging(hasPage bool, page int, hasPageSize bool, pageSize int) (int, int) {
	return d.paging(hasPage, page, hasPageSize, pageSize)
}

// groupAddBody mirrors groupAddSchema (strict).
type groupAddBody struct {
	TargetUsername    string
	TargetDisplayName *string
	Name              string
	ProviderCode      string
	Description       *string
	Enabled           *bool
	HasEnabled        bool
	GroupType         string
	HasGroupType      bool
}

func parseGroupAddBody(body map[string]any) (*groupAddBody, string) {
	unknown := strictObjectKeys(body, "targetUsername", "targetDisplayName", "name", "providerCode", "description", "enabled", "groupType")
	if unknown != nil {
		return nil, zodUnrecognizedKeys(unknown...)
	}
	parsed := &groupAddBody{}
	username, issue := requiredTrimmedBody(body, "targetUsername", 2, 80)
	if issue != "" {
		return nil, issue
	}
	parsed.TargetUsername = username
	displayName, issue := optionalTrimmedBody(body, "targetDisplayName", 1, 80)
	if issue != "" {
		return nil, issue
	}
	parsed.TargetDisplayName = displayName
	name, issue := requiredTrimmedBody(body, "name", 1, 80)
	if issue != "" {
		return nil, issue
	}
	parsed.Name = name
	providerCode, issue := requiredTrimmedBody(body, "providerCode", 1, 60)
	if issue != "" {
		return nil, issue
	}
	parsed.ProviderCode = providerCode
	description, issue := optionalTrimmedBody(body, "description", 0, 500)
	if issue != "" {
		return nil, issue
	}
	parsed.Description = description
	enabled, hasEnabled, issue := bodyOptionalBoolField(body, "enabled")
	if issue != "" {
		return nil, issue
	}
	parsed.Enabled, parsed.HasEnabled = &enabled, hasEnabled
	groupType, hasGroupType, issue := bodyOptionalEnumField(body, "groupType", []string{"personal", "high_concurrency"})
	if issue != "" {
		return nil, issue
	}
	parsed.GroupType, parsed.HasGroupType = groupType, hasGroupType
	return parsed, ""
}

func (d *Deps) addGroup(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	parsed, issue := parseGroupAddBody(body)
	if issue != "" {
		kernel.WriteBadRequest(w, issue)
		return
	}
	context := AuthContextFrom(r)
	if context.IsTestToken {
		d.mockGroupAdd(w, body)
		return
	}
	// Node asserts the provider before resolving the target user.
	if err := d.assertProviderCodeEnabled(r.Context(), parsed.ProviderCode); err != nil {
		kernel.WriteBadRequest(w, err.Error())
		return
	}
	target, err := d.ensureTargetSystemAccount(r.Context(), parsed.TargetUsername, parsed.TargetDisplayName)
	if err != nil {
		kernel.WriteBadRequest(w, serviceMessage(err, "分组新增失败"))
		return
	}
	if target.Account.Status != "active" {
		kernel.WriteBadRequest(w, "目标用户已停用："+target.Account.Username)
		return
	}
	existing, err := d.findExistingTargetGroup(r.Context(), target.Account.ID, parsed.ProviderCode, parsed.Name)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if existing != nil {
		summary, err := d.Groups.FindDetail(r.Context(), existing.ID, groups.AccessScope{ViewerID: target.Account.ID})
		if err != nil || summary == nil {
			kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		d.writeStatsEnvelope(w, map[string]any{
			"action": "existing", "target": target.Public, "group": sanitizeGroup(summary),
		})
		return
	}
	created, err := d.Groups.Create(r.Context(), groups.MutationInput{
		Name:         &parsed.Name,
		ProviderCode: &parsed.ProviderCode,
		Description:  parsed.Description,
		GroupType:    strPtr(groupTypeOr(parsed.GroupType, parsed.HasGroupType)),
	}, groups.AccessScope{ViewerID: target.Account.ID})
	if err != nil {
		kernel.WriteBadRequest(w, serviceMessage(err, "分组新增失败"))
		return
	}
	summary, err := d.Groups.FindDetail(r.Context(), created.ID, groups.AccessScope{ViewerID: target.Account.ID})
	if err != nil || summary == nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	d.writeStatsCreated(w, map[string]any{
		"action": "created", "target": target.Public, "group": sanitizeGroup(summary),
	})
}

func groupTypeOr(value string, present bool) string {
	if present && value != "" {
		return value
	}
	return "personal"
}

// groupUpdateBody mirrors groupUpdateSchema (strict + refine).
type groupUpdateBody struct {
	TargetUsername string
	HasTarget      bool
	GroupID        string
	Name           *string
	ProviderCode   *string
	Description    *string
	HasDescription bool
	Enabled        *bool
	HasEnabled     bool
	GroupType      *string
	HasGroupType   bool
}

var groupUpdateMutableFields = []string{"name", "providerCode", "description", "enabled", "groupType"}

func parseGroupUpdateBody(body map[string]any) (*groupUpdateBody, string) {
	unknown := strictObjectKeys(body, "targetUsername", "groupId", "name", "providerCode", "description", "enabled", "groupType")
	if unknown != nil {
		return nil, zodUnrecognizedKeys(unknown...)
	}
	parsed := &groupUpdateBody{}
	if bodyHas(body, "targetUsername") {
		username, issue := requiredTrimmedBody(body, "targetUsername", 2, 80)
		if issue != "" {
			return nil, issue
		}
		parsed.TargetUsername, parsed.HasTarget = username, true
	}
	groupID, issue := requiredTrimmedBody(body, "groupId", 1, 120)
	if issue != "" {
		return nil, issue
	}
	parsed.GroupID = groupID
	if bodyHas(body, "name") {
		name, issue := optionalTrimmedBody(body, "name", 1, 80)
		if issue != "" {
			return nil, issue
		}
		parsed.Name = name
	}
	if bodyHas(body, "providerCode") {
		providerCode, issue := optionalTrimmedBody(body, "providerCode", 1, 60)
		if issue != "" {
			return nil, issue
		}
		parsed.ProviderCode = providerCode
	}
	if bodyHas(body, "description") {
		description, issue := nullableTrimmedBodyField(body, "description", 500)
		if issue != "" {
			return nil, issue
		}
		parsed.Description, parsed.HasDescription = description, true
	}
	enabled, hasEnabled, issue := bodyOptionalBoolField(body, "enabled")
	if issue != "" {
		return nil, issue
	}
	parsed.Enabled, parsed.HasEnabled = &enabled, hasEnabled
	if bodyHas(body, "groupType") {
		groupType, _, issue := bodyOptionalEnumField(body, "groupType", []string{"personal", "high_concurrency"})
		if issue != "" {
			return nil, issue
		}
		parsed.GroupType, parsed.HasGroupType = &groupType, true
	}
	if !hasAnyField(body, groupUpdateMutableFields) {
		return nil, "分组修改至少提供一个要修改的字段"
	}
	return parsed, ""
}

func (d *Deps) updateGroup(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	parsed, issue := parseGroupUpdateBody(body)
	if issue != "" {
		kernel.WriteBadRequest(w, issue)
		return
	}
	context := AuthContextFrom(r)
	if context.IsTestToken {
		d.mockGroupUpdate(w, body)
		return
	}
	owner, err := d.findGroupOwnerByID(r.Context(), parsed.GroupID)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	target, targetErr := d.resolveOwnedTarget(r.Context(), parsed.TargetUsername, parsed.HasTarget, owner)
	if targetErr != nil || target == nil {
		d.writeNotFoundEnvelope(w, "分组不存在")
		return
	}
	if target.Account.Status != "active" {
		kernel.WriteBadRequest(w, "目标用户已停用："+target.Account.Username)
		return
	}
	access := groups.AccessScope{ViewerID: target.Account.ID}
	group, err := d.Groups.FindDetail(r.Context(), parsed.GroupID, access)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if group == nil {
		d.writeNotFoundEnvelope(w, "分组不存在")
		return
	}
	if parsed.ProviderCode != nil {
		if err := d.assertProviderCodeEnabled(r.Context(), *parsed.ProviderCode); err != nil {
			kernel.WriteBadRequest(w, err.Error())
			return
		}
	}
	mutation := groups.MutationInput{}
	if parsed.Name != nil {
		mutation.Name = parsed.Name
	}
	if parsed.ProviderCode != nil {
		mutation.ProviderCode = parsed.ProviderCode
	}
	mutation.Description = parsed.Description
	if parsed.HasEnabled {
		enabled := parsed.Enabled != nil && *parsed.Enabled
		mutation.Enabled = &enabled
	}
	if parsed.HasGroupType {
		mutation.GroupType = parsed.GroupType
	}
	updated, err := d.Groups.Patch(r.Context(), parsed.GroupID, mutation, group.UpdatedAt, access)
	if err != nil {
		kernel.WriteBadRequest(w, serviceMessage(err, "分组修改失败"))
		return
	}
	if updated == nil {
		d.writeNotFoundEnvelope(w, "分组不存在")
		return
	}
	summary, err := d.Groups.FindDetail(r.Context(), parsed.GroupID, access)
	if err != nil || summary == nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	d.writeStatsEnvelope(w, map[string]any{
		"action": "updated", "target": target.Public, "group": sanitizeGroup(summary),
	})
}

func (d *Deps) deleteGroup(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	unknown := strictObjectKeys(body, "targetUsername", "groupId")
	if unknown != nil {
		kernel.WriteBadRequest(w, zodUnrecognizedKeys(unknown...))
		return
	}
	parsed := &groupDeleteBody{}
	if bodyHas(body, "targetUsername") {
		username, issue := requiredTrimmedBody(body, "targetUsername", 2, 80)
		if issue != "" {
			kernel.WriteBadRequest(w, issue)
			return
		}
		parsed.TargetUsername, parsed.HasTarget = username, true
	}
	groupID, issue := requiredTrimmedBody(body, "groupId", 1, 120)
	if issue != "" {
		kernel.WriteBadRequest(w, issue)
		return
	}
	parsed.GroupID = groupID
	context := AuthContextFrom(r)
	if context.IsTestToken {
		d.mockGroupDelete(w, body)
		return
	}
	owner, err := d.findGroupOwnerByID(r.Context(), parsed.GroupID)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	target, targetErr := d.resolveOwnedTarget(r.Context(), parsed.TargetUsername, parsed.HasTarget, owner)
	if targetErr != nil || target == nil {
		d.writeNotFoundEnvelope(w, "分组不存在")
		return
	}
	if target.Account.Status != "active" {
		kernel.WriteBadRequest(w, "目标用户已停用："+target.Account.Username)
		return
	}
	access := groups.AccessScope{ViewerID: target.Account.ID}
	group, err := d.Groups.FindDetail(r.Context(), parsed.GroupID, access)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if group == nil {
		d.writeNotFoundEnvelope(w, "分组不存在")
		return
	}
	deleted := sanitizeGroup(group)
	result, err := d.Groups.Delete(r.Context(), parsed.GroupID, access)
	if err != nil {
		kernel.WriteBadRequest(w, serviceMessage(err, "分组删除失败"))
		return
	}
	if result == nil || !result.Deleted {
		d.writeNotFoundEnvelope(w, "分组不存在")
		return
	}
	d.writeStatsEnvelope(w, map[string]any{
		"action": "deleted", "target": target.Public, "group": deleted,
	})
}

// writeNotFoundEnvelope mirrors the Node not_found branch:
// res.status(404).json({message}).
func (d *Deps) writeNotFoundEnvelope(w http.ResponseWriter, message string) {
	kernel.WriteError(w, http.StatusNotFound, message)
}

// groupDeleteBody mirrors groupDeleteSchema (strict).
type groupDeleteBody struct {
	TargetUsername string
	HasTarget      bool
	GroupID        string
}

var _ = errors.New

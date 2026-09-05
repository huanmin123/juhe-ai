// Public API Key family (list/add/update/del), ported from the api-key
// branches of external-integrations.routes.ts plus
// publicApiKeyPayload/related service helpers. add returns the plaintext key
// exactly once (includeSecret), matching Node createApiKeyRecordAsync.
package aipublic

import (
	"net/http"
	"net/url"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/apikeys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// apiKeyListQuery mirrors apiKeyListQuerySchema (strict).
type apiKeyListQuery struct {
	TargetUsername  string
	RouteStrategyID string
	HasStrategy     bool
	Keyword         string
	HasKeyword      bool
	Status          string
	HasStatus       bool
	Page            int
	HasPage         bool
	PageSize        int
	HasPageSize     bool
}

func parseApiKeyListQuery(values url.Values) (*apiKeyListQuery, string) {
	unknown := strictObjectKeys(valuesAsMap(values), "targetUsername", "routeStrategyId", "keyword", "status", "page", "pageSize")
	if unknown != nil {
		return nil, zodUnrecognizedKeys(unknown...)
	}
	query := &apiKeyListQuery{}
	username, issue := parseQueryString(values, "targetUsername", true, 2, 80)
	if issue != "" {
		return nil, issue
	}
	query.TargetUsername = username
	strategy, has, issue := parseOptionalQueryString(values, "routeStrategyId", 1, 120)
	if issue != "" {
		return nil, issue
	}
	query.RouteStrategyID, query.HasStrategy = strategy, has
	keyword, has, issue := parseOptionalQueryString(values, "keyword", 0, 120)
	if issue != "" {
		return nil, issue
	}
	query.Keyword, query.HasKeyword = keyword, has
	status, has, issue := parseOptionalQueryEnum(values, "status", []string{"active", "disabled", "all"})
	if issue != "" {
		return nil, issue
	}
	query.Status, query.HasStatus = status, has
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

func (d *Deps) listApiKeys(w http.ResponseWriter, r *http.Request) {
	query, issue := parseApiKeyListQuery(r.URL.Query())
	if issue != "" {
		kernel.WriteBadRequest(w, issue)
		return
	}
	context := AuthContextFrom(r)
	if context.IsTestToken {
		page, pageSize := d.apiKeyPaging(query.HasPage, query.Page, query.HasPageSize, query.PageSize)
		d.mockApiKeyList(w, map[string]string{
			"targetUsername":  query.TargetUsername,
			"routeStrategyId": query.RouteStrategyID,
			"keyword":         query.Keyword,
			"status":          query.Status,
		}, page, pageSize)
		return
	}
	target, err := d.requirePublicTarget(r.Context(), query.TargetUsername)
	if err != nil {
		d.writeServiceError(w, err, "API Key 列表读取失败")
		return
	}
	if target.Account.Status != "active" {
		kernel.WriteBadRequest(w, "目标用户已停用："+target.Account.Username)
		return
	}
	page, pageSize := d.apiKeyPaging(query.HasPage, query.Page, query.HasPageSize, query.PageSize)
	options := apikeys.ListOptions{
		Page: page, PageSet: true, PageSize: pageSize, PageSizeSet: true,
		Keyword: query.Keyword, Status: query.Status, RouteStrategyID: query.RouteStrategyID,
	}
	result, err := d.ApiKeys.ListPage(r.Context(), apikeys.AccessScope{ViewerID: target.SystemAccountID}, options)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	items := make([]PublicApiKeySummary, 0, len(result.Items))
	for index := range result.Items {
		items = append(items, sanitizeApiKeyItem(&result.Items[index]))
	}
	d.writeStatsEnvelope(w, map[string]any{
		"target":         target.Public,
		"page":           result.Page,
		"pageSize":       result.PageSize,
		"pageUpperBound": result.Total,
		"hasMore":        result.HasMore,
		"items":          items,
	})
}

func (d *Deps) apiKeyPaging(hasPage bool, page int, hasPageSize bool, pageSize int) (int, int) {
	// normalizeApiKeysListOptions defaults pageSize 50; the public schema caps
	// explicit sizes at 100.
	currentPage := 1
	if hasPage && page > 0 {
		currentPage = page
	}
	size := 50
	if hasPageSize && pageSize > 0 {
		size = pageSize
	}
	return currentPage, size
}

// apiKeyAddBody mirrors apiKeyAddSchema (strict).
type apiKeyAddBody struct {
	TargetUsername       string
	Name                 string
	Description          *string
	HasDescription       bool
	RouteStrategyID      string
	Status               string
	HasStatus            bool
	ExpiresAt            string
	HasExpiresAt         bool
	QuotaLimits          any
	HasQuotaLimits       bool
	AvailabilitySchedule any
	HasSchedule          bool
}

func parseApiKeyAddBody(body map[string]any) (*apiKeyAddBody, string) {
	unknown := strictObjectKeys(body, "targetUsername", "name", "description", "routeStrategyId", "status", "expiresAt", "quotaLimits", "availabilitySchedule")
	if unknown != nil {
		return nil, zodUnrecognizedKeys(unknown...)
	}
	parsed := &apiKeyAddBody{}
	username, issue := requiredTrimmedBody(body, "targetUsername", 2, 80)
	if issue != "" {
		return nil, issue
	}
	parsed.TargetUsername = username
	name, issue := requiredTrimmedBody(body, "name", 1, 120)
	if issue != "" {
		return nil, issue
	}
	parsed.Name = name
	if bodyHas(body, "description") {
		description, issue := nullableTrimmedBodyField(body, "description", 200)
		if issue != "" {
			return nil, issue
		}
		parsed.Description, parsed.HasDescription = description, true
	}
	strategyID, issue := requiredTrimmedBody(body, "routeStrategyId", 1, 120)
	if issue != "" {
		return nil, issue
	}
	parsed.RouteStrategyID = strategyID
	status, hasStatus, issue := bodyOptionalEnumField(body, "status", []string{"active", "disabled"})
	if issue != "" {
		return nil, issue
	}
	parsed.Status, parsed.HasStatus = status, hasStatus
	if bodyHas(body, "expiresAt") {
		expiresAt, issue := requiredTrimmedBody(body, "expiresAt", 0, 0)
		if issue != "" {
			return nil, issue
		}
		parsed.ExpiresAt, parsed.HasExpiresAt = expiresAt, true
	}
	if bodyHas(body, "quotaLimits") {
		parsed.QuotaLimits, parsed.HasQuotaLimits = body["quotaLimits"], true
	}
	if bodyHas(body, "availabilitySchedule") {
		parsed.AvailabilitySchedule, parsed.HasSchedule = body["availabilitySchedule"], true
	}
	return parsed, ""
}

func (d *Deps) addApiKey(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	parsed, issue := parseApiKeyAddBody(body)
	if issue != "" {
		kernel.WriteBadRequest(w, issue)
		return
	}
	context := AuthContextFrom(r)
	if context.IsTestToken {
		d.mockApiKeyAdd(w, body)
		return
	}
	target, err := d.requirePublicTarget(r.Context(), parsed.TargetUsername)
	if err != nil {
		d.writeServiceError(w, err, "API Key 新增失败")
		return
	}
	access := apikeys.AccessScope{ViewerID: target.SystemAccountID}
	input := apikeys.CreateInput{
		Name:            parsed.Name,
		Description:     parsed.Description,
		RouteStrategyID: &parsed.RouteStrategyID,
	}
	if parsed.HasStatus {
		status := parsed.Status
		input.Status = &status
	}
	if parsed.HasExpiresAt {
		expiresAt := parsed.ExpiresAt
		input.ExpiresAt = &expiresAt
	}
	if parsed.HasQuotaLimits {
		input.QuotaLimits = parsed.QuotaLimits
	}
	if parsed.HasSchedule {
		input.AvailabilitySchedule = parsed.AvailabilitySchedule
	}
	created, _, err := d.ApiKeys.Create(r.Context(), input, access)
	if err != nil {
		d.writeServiceError(w, err, "API Key 新增失败")
		return
	}
	// Node returns the full sanitized summary with the plaintext key; the
	// strategy fields come from the created row read-back.
	item, err := d.ApiKeys.FindDetail(r.Context(), created.ID, access)
	if err != nil || item == nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	summary := sanitizeApiKeyItem(item)
	key := created.Key
	summary.Key = &key
	d.writeStatsCreated(w, map[string]any{
		"action": "created", "target": target.Public, "apiKey": summary,
	})
}

// apiKeyUpdateBody mirrors apiKeyUpdateSchema (strict + refine).
type apiKeyUpdateBody struct {
	TargetUsername       string
	HasTarget            bool
	ApiKeyID             string
	Name                 *string
	HasName              bool
	Description          *string
	HasDescription       bool
	RouteStrategyID      string
	HasStrategy          bool
	Status               string
	HasStatus            bool
	ExpiresAt            *string
	HasExpiresAt         bool
	QuotaLimits          any
	HasQuotaLimits       bool
	AvailabilitySchedule any
	HasSchedule          bool
}

var apiKeyUpdateMutableFields = []string{"name", "description", "routeStrategyId", "status", "expiresAt", "quotaLimits", "availabilitySchedule"}

func parseApiKeyUpdateBody(body map[string]any) (*apiKeyUpdateBody, string) {
	unknown := strictObjectKeys(body, "targetUsername", "apiKeyId", "name", "description", "routeStrategyId", "status", "expiresAt", "quotaLimits", "availabilitySchedule")
	if unknown != nil {
		return nil, zodUnrecognizedKeys(unknown...)
	}
	parsed := &apiKeyUpdateBody{}
	if bodyHas(body, "targetUsername") {
		username, issue := requiredTrimmedBody(body, "targetUsername", 2, 80)
		if issue != "" {
			return nil, issue
		}
		parsed.TargetUsername, parsed.HasTarget = username, true
	}
	apiKeyID, issue := requiredTrimmedBody(body, "apiKeyId", 1, 120)
	if issue != "" {
		return nil, issue
	}
	parsed.ApiKeyID = apiKeyID
	if bodyHas(body, "name") {
		name, issue := optionalTrimmedBody(body, "name", 1, 120)
		if issue != "" {
			return nil, issue
		}
		parsed.Name, parsed.HasName = name, true
	}
	if bodyHas(body, "description") {
		description, issue := nullableTrimmedBodyField(body, "description", 200)
		if issue != "" {
			return nil, issue
		}
		parsed.Description, parsed.HasDescription = description, true
	}
	if bodyHas(body, "routeStrategyId") {
		strategyID, issue := optionalTrimmedBody(body, "routeStrategyId", 1, 120)
		if issue != "" {
			return nil, issue
		}
		if strategyID != nil {
			parsed.RouteStrategyID, parsed.HasStrategy = *strategyID, true
		}
	}
	status, hasStatus, issue := bodyOptionalEnumField(body, "status", []string{"active", "disabled"})
	if issue != "" {
		return nil, issue
	}
	parsed.Status, parsed.HasStatus = status, hasStatus
	if bodyHas(body, "expiresAt") {
		expiresAt, issue := nullableTrimmedBodyField(body, "expiresAt", 0)
		if issue != "" {
			return nil, issue
		}
		parsed.ExpiresAt, parsed.HasExpiresAt = expiresAt, true
	}
	if bodyHas(body, "quotaLimits") {
		parsed.QuotaLimits, parsed.HasQuotaLimits = body["quotaLimits"], true
	}
	if bodyHas(body, "availabilitySchedule") {
		parsed.AvailabilitySchedule, parsed.HasSchedule = body["availabilitySchedule"], true
	}
	if !hasAnyField(body, apiKeyUpdateMutableFields) {
		return nil, "API Key 修改至少提供一个要修改的字段"
	}
	return parsed, ""
}

func (d *Deps) updateApiKey(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	parsed, issue := parseApiKeyUpdateBody(body)
	if issue != "" {
		kernel.WriteBadRequest(w, issue)
		return
	}
	context := AuthContextFrom(r)
	if context.IsTestToken {
		d.mockApiKeyUpdate(w, body)
		return
	}
	owner, err := d.findApiKeyOwnerByID(r.Context(), parsed.ApiKeyID)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	target, targetErr := d.resolveOwnedTarget(r.Context(), parsed.TargetUsername, parsed.HasTarget, owner)
	if targetErr != nil || target == nil {
		d.writeNotFoundEnvelope(w, "API Key 不存在")
		return
	}
	if target.Account.Status != "active" {
		kernel.WriteBadRequest(w, "目标用户已停用："+target.Account.Username)
		return
	}
	access := apikeys.AccessScope{ViewerID: target.SystemAccountID}
	// Node updateApiKeyAsync reads the current revision itself; the Go store
	// takes the expected revision explicitly, so read it from the current row.
	current, err := d.ApiKeys.FindDetail(r.Context(), parsed.ApiKeyID, access)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if current == nil {
		d.writeNotFoundEnvelope(w, "API Key 不存在")
		return
	}
	input := &apikeys.PatchInput{ExpectedRevision: current.Revision}
	if parsed.HasName && parsed.Name != nil {
		input.Name = *parsed.Name
		input.HasName = true
	}
	if parsed.HasDescription {
		input.Description = parsed.Description
		input.HasDescription = true
	}
	if parsed.HasStrategy {
		input.RouteStrategyID = parsed.RouteStrategyID
		input.HasRouteStrategyID = true
	}
	if parsed.HasStatus {
		input.Status = parsed.Status
		input.HasStatus = true
	}
	if parsed.HasExpiresAt {
		input.ExpiresAt = parsed.ExpiresAt
		input.HasExpiresAt = true
	}
	if parsed.HasQuotaLimits {
		input.QuotaLimits = parsed.QuotaLimits
		input.HasQuotaLimits = true
	}
	if parsed.HasSchedule {
		input.AvailabilitySchedule = parsed.AvailabilitySchedule
		input.HasSchedule = true
	}
	outcome, err := d.ApiKeys.Patch(r.Context(), parsed.ApiKeyID, input, access)
	if err != nil {
		d.writeServiceError(w, err, "API Key 修改失败")
		return
	}
	if outcome != nil && outcome.ValidationCacheError != nil {
		kernelWriteError(w, http.StatusInternalServerError, "API Key 已更新，但 validation cache 失效失败")
		return
	}
	if outcome == nil {
		d.writeNotFoundEnvelope(w, "API Key 不存在")
		return
	}
	item, err := d.ApiKeys.FindDetail(r.Context(), parsed.ApiKeyID, access)
	if err != nil || item == nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	d.writeStatsEnvelope(w, map[string]any{
		"action": "updated", "target": target.Public, "apiKey": sanitizeApiKeyItem(item),
	})
}

func (d *Deps) deleteApiKey(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	unknown := strictObjectKeys(body, "targetUsername", "apiKeyId")
	if unknown != nil {
		kernel.WriteBadRequest(w, zodUnrecognizedKeys(unknown...))
		return
	}
	parsed := &apiKeyDeleteBody{}
	if bodyHas(body, "targetUsername") {
		username, issue := requiredTrimmedBody(body, "targetUsername", 2, 80)
		if issue != "" {
			kernel.WriteBadRequest(w, issue)
			return
		}
		parsed.TargetUsername, parsed.HasTarget = username, true
	}
	apiKeyID, issue := requiredTrimmedBody(body, "apiKeyId", 1, 120)
	if issue != "" {
		kernel.WriteBadRequest(w, issue)
		return
	}
	parsed.ApiKeyID = apiKeyID
	context := AuthContextFrom(r)
	if context.IsTestToken {
		d.mockApiKeyDelete(w, body)
		return
	}
	owner, err := d.findApiKeyOwnerByID(r.Context(), parsed.ApiKeyID)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	target, targetErr := d.resolveOwnedTarget(r.Context(), parsed.TargetUsername, parsed.HasTarget, owner)
	if targetErr != nil || target == nil {
		// Node api-key/del keeps 200 for the not_found action.
		d.writeApiKeyAction(w, "not_found", target, nil)
		return
	}
	if target.Account.Status != "active" {
		kernel.WriteBadRequest(w, "目标用户已停用："+target.Account.Username)
		return
	}
	access := apikeys.AccessScope{ViewerID: target.SystemAccountID}
	current, err := d.ApiKeys.FindDetail(r.Context(), parsed.ApiKeyID, access)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if current == nil {
		d.writeApiKeyAction(w, "not_found", target, nil)
		return
	}
	deleted := sanitizeApiKeyItem(current)
	result, err := d.ApiKeys.Delete(r.Context(), parsed.ApiKeyID, access)
	if err != nil {
		kernelWriteBadRequest(w, serviceMessage(err, "API Key 删除失败"))
		return
	}
	if result != nil && result.ValidationCacheError != nil {
		kernelWriteError(w, http.StatusInternalServerError, "API Key 已删除，但 validation cache 失效失败")
		return
	}
	if result == nil || !result.Deleted {
		d.writeApiKeyAction(w, "not_found", target, nil)
		return
	}
	d.writeApiKeyAction(w, "deleted", target, &deleted)
}

// writeApiKeyAction mirrors the api-key/del branch: ok(result) always 200.
func (d *Deps) writeApiKeyAction(w http.ResponseWriter, action string, target *ResolvedPublicTarget, summary *PublicApiKeySummary) {
	var targetValue any
	if target != nil {
		targetValue = target.Public
	} else {
		targetValue = emptyTarget("")
	}
	rest := map[string]any{"action": action, "target": targetValue}
	if summary != nil {
		rest["apiKey"] = *summary
	} else {
		rest["apiKey"] = nil
	}
	d.writeStatsEnvelope(w, rest)
}

// apiKeyDeleteBody mirrors apiKeyDeleteSchema (strict).
type apiKeyDeleteBody struct {
	TargetUsername string
	HasTarget      bool
	ApiKeyID       string
}

// Public route-strategy family (list/add/update/del), ported from
// external-public-route-strategy.service.ts plus the routes/schema layer in
// external-integrations.routes.ts. The list reuses the M06 store page and
// hydrates the full binding summaries (priority/weight) the public DTO
// carries, exactly like Node's routeStrategySummariesFromRows.
package aipublic

import (
	"context"
	"database/sql"
	"net/http"
	"net/url"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/routestrategies"
)

// strategyListQuery mirrors routeStrategyListQuerySchema (strict).
type strategyListQuery struct {
	TargetUsername string
	Keyword        string
	HasKeyword     bool
	Mode           string
	HasMode        bool
	Status         string
	HasStatus      bool
	Page           int
	HasPage        bool
	PageSize       int
	HasPageSize    bool
}

var strategyModeOptions = []string{"normal", "hybrid_smart", "weighted", "failover", "round_robin", "all"}
var strategyStatusOptions = []string{"active", "disabled", "all"}

func parseStrategyListQuery(values url.Values) (*strategyListQuery, string) {
	unknown := strictObjectKeys(valuesAsMap(values), "targetUsername", "keyword", "mode", "status", "page", "pageSize")
	if unknown != nil {
		return nil, zodUnrecognizedKeys(unknown...)
	}
	query := &strategyListQuery{}
	username, issue := parseQueryString(values, "targetUsername", true, 2, 80)
	if issue != "" {
		return nil, issue
	}
	query.TargetUsername = username
	keyword, has, issue := parseOptionalQueryString(values, "keyword", 0, 120)
	if issue != "" {
		return nil, issue
	}
	query.Keyword, query.HasKeyword = keyword, has
	mode, has, issue := parseOptionalQueryEnum(values, "mode", strategyModeOptions)
	if issue != "" {
		return nil, issue
	}
	query.Mode, query.HasMode = mode, has
	status, has, issue := parseOptionalQueryEnum(values, "status", strategyStatusOptions)
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

func (d *Deps) listRouteStrategies(w http.ResponseWriter, r *http.Request) {
	query, issue := parseStrategyListQuery(r.URL.Query())
	if issue != "" {
		kernel.WriteBadRequest(w, issue)
		return
	}
	context := AuthContextFrom(r)
	if context.IsTestToken {
		page, pageSize := d.strategyPaging(query.HasPage, query.Page, query.HasPageSize, query.PageSize)
		d.mockStrategyList(w, map[string]string{
			"targetUsername": query.TargetUsername,
			"keyword":        query.Keyword,
			"mode":           query.Mode,
			"status":         query.Status,
		}, page, pageSize)
		return
	}
	target, err := d.requirePublicTarget(r.Context(), query.TargetUsername)
	if err != nil {
		d.writeServiceError(w, err, "路由策略列表读取失败")
		return
	}
	if target.Account.Status != "active" {
		kernel.WriteBadRequest(w, "目标用户已停用："+target.Account.Username)
		return
	}
	page, pageSize := d.strategyPaging(query.HasPage, query.Page, query.HasPageSize, query.PageSize)
	options := routestrategies.ListOptions{Page: page, PageSize: pageSize, Keyword: query.Keyword}
	if query.HasMode && query.Mode != "all" {
		options.Mode = query.Mode
	}
	if query.HasStatus && query.Status != "all" {
		options.Status = query.Status
	}
	result, err := d.Strategies.ListPage(r.Context(), routestrategies.AccessScope{ViewerID: target.SystemAccountID}, options)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	ids := make([]string, 0, len(result.Items))
	for _, item := range result.Items {
		ids = append(ids, item.ID)
	}
	bindings, err := d.loadStrategyBindings(r.Context(), ids)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	items := make([]PublicStrategySummary, 0, len(result.Items))
	for index := range result.Items {
		item := &result.Items[index]
		summary := PublicStrategySummary{
			ID: item.ID, Name: item.Name, Description: item.Description,
			Mode: item.Mode, Status: item.Status, IsDefault: item.IsDefault,
			GroupBindings: bindings[item.ID],
			APIKeyCount:   item.APIKeyCount,
			CreatedAt:     item.CreatedAt, UpdatedAt: item.UpdatedAt,
		}
		if item.NormalRoutingConfig != nil {
			summary.NormalRoutingConfig = item.NormalRoutingConfig
		}
		items = append(items, summary)
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

func (d *Deps) strategyPaging(hasPage bool, page int, hasPageSize bool, pageSize int) (int, int) {
	// normalizeRouteStrategyListOptions: default pageSize 50 (clamp 1..200);
	// the public schema caps explicit sizes at 100.
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

// strategyBinding is the hydrated public binding (Node
// RouteStrategyGroupBindingSummary shape).
type strategyBindingRow struct {
	id              string
	routeStrategyID string
	groupID         string
	priority        int
	weight          int
	status          string
	groupName       sql.NullString
	providerCode    sql.NullString
	groupEnabled    bool
}

// loadStrategyBindings mirrors loadRouteStrategyGroupBindingSummariesByRouteStrategyIds
// (same columns/order as the M06 store's private loader).
func (d *Deps) loadStrategyBindings(ctx context.Context, strategyIDs []string) (map[string][]PublicBindingSummary, error) {
	result := map[string][]PublicBindingSummary{}
	if len(strategyIDs) == 0 {
		return result, nil
	}
	unique := uniqueSortedStrings(strategyIDs)
	placeholders := make([]string, len(unique))
	args := make([]any, 0, len(unique))
	for i, id := range unique {
		placeholders[i] = "?"
		args = append(args, id)
	}
	rows, err := d.db().QueryContext(ctx, d.bind(`SELECT route_strategy_groups.id,
		route_strategy_groups.route_strategy_id,
		route_strategy_groups.group_id,
		route_strategy_groups.priority,
		route_strategy_groups.weight,
		route_strategy_groups.status,
		groups.name AS group_name,
		groups.provider_code,
		CASE
			WHEN groups.id IS NULL THEN 0
			WHEN groups.system_account_id = route_strategy_groups.system_account_id THEN groups.enabled
			ELSE 0
		END AS group_enabled
	FROM `+d.table("route_strategy_groups")+` route_strategy_groups
	LEFT JOIN `+d.table("groups")+` groups ON groups.id = route_strategy_groups.group_id
	WHERE route_strategy_groups.route_strategy_id IN (`+strings.Join(placeholders, ",")+`)
	ORDER BY route_strategy_groups.route_strategy_id ASC,
		CASE WHEN route_strategy_groups.status = 'active' THEN 0 ELSE 1 END ASC,
		route_strategy_groups.priority ASC,
		route_strategy_groups.created_at ASC,
		route_strategy_groups.id ASC`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var row strategyBindingRow
		var enabled int64
		if err := rows.Scan(&row.id, &row.routeStrategyID, &row.groupID, &row.priority, &row.weight,
			&row.status, &row.groupName, &row.providerCode, &enabled); err != nil {
			return nil, err
		}
		summary := PublicBindingSummary{
			ID: row.id, GroupID: row.groupID, GroupName: nullPtrString(row.groupName),
			ProviderCode: nullPtrString(row.providerCode), Priority: row.priority,
			Weight: row.weight, Status: row.status, GroupEnabled: enabled == 1,
		}
		result[row.routeStrategyID] = append(result[row.routeStrategyID], summary)
	}
	for key, value := range result {
		if value == nil {
			result[key] = []PublicBindingSummary{}
		}
	}
	return result, rows.Err()
}

// strategyAddBody mirrors routeStrategyAddSchema (strict).
type strategyAddBody struct {
	TargetUsername  string
	Name            string
	Description     *string
	HasDescription  bool
	Mode            string
	HasMode         bool
	Status          string
	HasStatus       bool
	Bindings        []strategyBindingInput
	HasBindings     bool
	NormalConfig    any
	HasNormalConfig bool
	HybridConfig    any
	HasHybridConfig bool
}

type strategyBindingInput struct {
	GroupID  string
	Priority *int
	Weight   *int
	Status   string
}

var strategyMutationModes = []string{"normal", "hybrid_smart", "weighted", "failover", "round_robin"}

func parseStrategyBindings(body map[string]any) ([]strategyBindingInput, string) {
	raw, exists := body["groupBindings"]
	if !exists || raw == nil {
		return nil, zodRequired
	}
	items, isList := raw.([]any)
	if !isList {
		return nil, zodInvalidType("array", raw)
	}
	if len(items) < 1 || len(items) > 20 {
		if len(items) < 1 {
			return nil, "Array must contain at least 1 element(s)"
		}
		return nil, "Array must contain at most 20 element(s)"
	}
	out := make([]strategyBindingInput, 0, len(items))
	for _, item := range items {
		record, isObject := item.(map[string]any)
		if !isObject {
			return nil, zodInvalidType("object", item)
		}
		if unknown := strictObjectKeys(record, "groupId", "priority", "weight", "status"); unknown != nil {
			return nil, zodUnrecognizedKeys(unknown...)
		}
		groupID, issue := requiredTrimmedBody(record, "groupId", 1, 120)
		if issue != "" {
			return nil, issue
		}
		binding := strategyBindingInput{GroupID: groupID}
		if value, exists := record["priority"]; exists && value != nil {
			number, isNumber := value.(float64)
			if !isNumber || number != float64(int64(number)) || int(number) < 1 {
				return nil, zodNumberMin(1)
			}
			priority := int(number)
			binding.Priority = &priority
		}
		if value, exists := record["weight"]; exists && value != nil {
			number, isNumber := value.(float64)
			if !isNumber || number != float64(int64(number)) || int(number) < 1 {
				return nil, zodNumberMin(1)
			}
			if int(number) > 100 {
				return nil, zodNumberMax(100)
			}
			weight := int(number)
			binding.Weight = &weight
		}
		if value, exists := record["status"]; exists && value != nil {
			text, isString := value.(string)
			if !isString || (text != "active" && text != "disabled") {
				if !isString {
					return nil, zodInvalidType("string", value)
				}
				return nil, zodEnumMessage([]string{"active", "disabled"}, text)
			}
			binding.Status = text
		}
		out = append(out, binding)
	}
	return out, ""
}

func parseStrategyAddBody(body map[string]any) (*strategyAddBody, string) {
	unknown := strictObjectKeys(body, "targetUsername", "name", "description", "mode", "status", "groupBindings", "normalRoutingConfig", "hybridRoutingConfig")
	if unknown != nil {
		return nil, zodUnrecognizedKeys(unknown...)
	}
	parsed := &strategyAddBody{}
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
	mode, hasMode, issue := bodyOptionalEnumField(body, "mode", strategyMutationModes)
	if issue != "" {
		return nil, issue
	}
	parsed.Mode, parsed.HasMode = mode, hasMode
	status, hasStatus, issue := bodyOptionalEnumField(body, "status", []string{"active", "disabled"})
	if issue != "" {
		return nil, issue
	}
	parsed.Status, parsed.HasStatus = status, hasStatus
	bindings, issue := parseStrategyBindings(body)
	if issue != "" {
		return nil, issue
	}
	parsed.Bindings, parsed.HasBindings = bindings, true
	if value, exists := body["normalRoutingConfig"]; exists {
		if value == nil || isPlainObject(value) {
			parsed.NormalConfig, parsed.HasNormalConfig = value, true
		} else {
			return nil, zodInvalidType("object", value)
		}
	}
	if value, exists := body["hybridRoutingConfig"]; exists {
		if value == nil || isPlainObject(value) {
			parsed.HybridConfig, parsed.HasHybridConfig = value, true
		} else {
			return nil, zodInvalidType("object", value)
		}
	}
	return parsed, ""
}

func isPlainObject(value any) bool {
	_, ok := value.(map[string]any)
	return ok
}

func (d *Deps) addRouteStrategy(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	parsed, issue := parseStrategyAddBody(body)
	if issue != "" {
		kernel.WriteBadRequest(w, issue)
		return
	}
	context := AuthContextFrom(r)
	if context.IsTestToken {
		d.mockStrategyAdd(w, body)
		return
	}
	target, err := d.requirePublicTarget(r.Context(), parsed.TargetUsername)
	if err != nil {
		d.writeServiceError(w, err, "路由策略新增失败")
		return
	}
	mutation := strategyMutationFrom(parsed, nil)
	created, err := d.Strategies.Create(r.Context(), mutation, routestrategies.AccessScope{ViewerID: target.SystemAccountID})
	if err != nil {
		d.writeServiceError(w, err, "路由策略新增失败")
		return
	}
	detail, err := d.Strategies.FindDetail(r.Context(), created.ID, routestrategies.AccessScope{ViewerID: target.SystemAccountID})
	if err != nil || detail == nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	d.writeStatsCreated(w, map[string]any{
		"action": "created", "target": target.Public, "routeStrategy": sanitizeStrategy(detail),
	})
}

// strategyMutationFrom converts the parsed public payload to the M06 store
// input; currentUpdatedAt carries the expected version for updates (Node
// passes owner.updatedAt).
func strategyMutationFrom(parsed *strategyAddBody, bindings []strategyBindingInput) routestrategies.MutationInput {
	mutation := routestrategies.MutationInput{}
	name := parsed.Name
	if name != "" {
		mutation.Name = &name
	}
	if parsed.HasDescription {
		mutation.Description = parsed.Description
		mutation.HasDescription = true
	}
	if parsed.HasMode {
		mode := parsed.Mode
		mutation.Mode = &mode
	}
	if parsed.HasStatus {
		status := parsed.Status
		mutation.Status = &status
	}
	source := parsed.Bindings
	if bindings != nil {
		source = bindings
	}
	mutation.HasBindings = true
	for index, binding := range source {
		priority := index + 1
		if binding.Priority != nil {
			priority = *binding.Priority
		}
		weight := 1
		if binding.Weight != nil {
			weight = *binding.Weight
		}
		status := binding.Status
		if status == "" {
			status = "active"
		}
		mutation.Bindings = append(mutation.Bindings, routestrategies.BindingInput{
			GroupID: binding.GroupID, Priority: &priority, Weight: &weight, Status: status,
		})
	}
	if parsed.HasNormalConfig {
		mutation.HasNormalConfig = true
		mutation.NormalConfigRaw = parsed.NormalConfig
	}
	if parsed.HasHybridConfig {
		mutation.HasHybridConfig = true
		mutation.HybridConfigRaw = parsed.HybridConfig
	}
	return mutation
}

// strategyUpdateBody mirrors routeStrategyUpdateSchema (strict + refine).
type strategyUpdateBody struct {
	TargetUsername  string
	HasTarget       bool
	RouteStrategyID string
	Name            *string
	HasName         bool
	Description     *string
	HasDescription  bool
	Mode            string
	HasMode         bool
	Status          string
	HasStatus       bool
	Bindings        []strategyBindingInput
	HasBindings     bool
	NormalConfig    any
	HasNormalConfig bool
	HybridConfig    any
	HasHybridConfig bool
}

var strategyUpdateMutableFields = []string{"name", "description", "mode", "status", "groupBindings", "normalRoutingConfig", "hybridRoutingConfig"}

func parseStrategyUpdateBody(body map[string]any) (*strategyUpdateBody, string) {
	unknown := strictObjectKeys(body, "targetUsername", "routeStrategyId", "name", "description", "mode", "status", "groupBindings", "normalRoutingConfig", "hybridRoutingConfig")
	if unknown != nil {
		return nil, zodUnrecognizedKeys(unknown...)
	}
	parsed := &strategyUpdateBody{}
	if bodyHas(body, "targetUsername") {
		username, issue := requiredTrimmedBody(body, "targetUsername", 2, 80)
		if issue != "" {
			return nil, issue
		}
		parsed.TargetUsername, parsed.HasTarget = username, true
	}
	strategyID, issue := requiredTrimmedBody(body, "routeStrategyId", 1, 120)
	if issue != "" {
		return nil, issue
	}
	parsed.RouteStrategyID = strategyID
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
	mode, hasMode, issue := bodyOptionalEnumField(body, "mode", strategyMutationModes)
	if issue != "" {
		return nil, issue
	}
	parsed.Mode, parsed.HasMode = mode, hasMode
	status, hasStatus, issue := bodyOptionalEnumField(body, "status", []string{"active", "disabled"})
	if issue != "" {
		return nil, issue
	}
	parsed.Status, parsed.HasStatus = status, hasStatus
	if bodyHas(body, "groupBindings") {
		bindings, issue := parseStrategyBindings(body)
		if issue != "" {
			return nil, issue
		}
		parsed.Bindings, parsed.HasBindings = bindings, true
	}
	if value, exists := body["normalRoutingConfig"]; exists {
		if value == nil || isPlainObject(value) {
			parsed.NormalConfig, parsed.HasNormalConfig = value, true
		} else {
			return nil, zodInvalidType("object", value)
		}
	}
	if value, exists := body["hybridRoutingConfig"]; exists {
		if value == nil || isPlainObject(value) {
			parsed.HybridConfig, parsed.HasHybridConfig = value, true
		} else {
			return nil, zodInvalidType("object", value)
		}
	}
	if !hasAnyField(body, strategyUpdateMutableFields) {
		return nil, "路由策略修改至少提供一个要修改的字段"
	}
	return parsed, ""
}

func (d *Deps) updateRouteStrategy(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	parsed, issue := parseStrategyUpdateBody(body)
	if issue != "" {
		kernel.WriteBadRequest(w, issue)
		return
	}
	context := AuthContextFrom(r)
	if context.IsTestToken {
		d.mockStrategyUpdate(w, body)
		return
	}
	owner, err := d.findStrategyOwnerByID(r.Context(), parsed.RouteStrategyID)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	target, targetErr := d.resolveOwnedTarget(r.Context(), parsed.TargetUsername, parsed.HasTarget, &ownerLookup{ID: ownerID(owner), SystemAccountID: ownerAccountID(owner)})
	if targetErr != nil || target == nil {
		d.writeNotFoundEnvelope(w, "路由策略不存在")
		return
	}
	if target.Account.Status != "active" {
		kernel.WriteBadRequest(w, "目标用户已停用："+target.Account.Username)
		return
	}
	access := routestrategies.AccessScope{ViewerID: target.SystemAccountID}
	current, err := d.Strategies.FindDetail(r.Context(), parsed.RouteStrategyID, access)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if current == nil {
		d.writeNotFoundEnvelope(w, "路由策略不存在")
		return
	}
	mutation := routestrategies.MutationInput{}
	if parsed.HasName {
		mutation.Name = parsed.Name
	}
	if parsed.HasDescription {
		mutation.Description = parsed.Description
		mutation.HasDescription = true
	}
	if parsed.HasMode {
		mode := parsed.Mode
		mutation.Mode = &mode
	}
	if parsed.HasStatus {
		status := parsed.Status
		mutation.Status = &status
	}
	if parsed.HasBindings {
		mutation.HasBindings = true
		for index, binding := range parsed.Bindings {
			priority := index + 1
			if binding.Priority != nil {
				priority = *binding.Priority
			}
			weight := 1
			if binding.Weight != nil {
				weight = *binding.Weight
			}
			status := binding.Status
			if status == "" {
				status = "active"
			}
			mutation.Bindings = append(mutation.Bindings, routestrategies.BindingInput{
				GroupID: binding.GroupID, Priority: &priority, Weight: &weight, Status: status,
			})
		}
	}
	if parsed.HasNormalConfig {
		mutation.HasNormalConfig = true
		mutation.NormalConfigRaw = parsed.NormalConfig
	}
	if parsed.HasHybridConfig {
		mutation.HasHybridConfig = true
		mutation.HybridConfigRaw = parsed.HybridConfig
	}
	updated, err := d.Strategies.Patch(r.Context(), parsed.RouteStrategyID, mutation, ownerUpdatedAt(owner), access)
	if err != nil {
		d.writeServiceError(w, err, "路由策略修改失败")
		return
	}
	if updated == nil {
		d.writeNotFoundEnvelope(w, "路由策略不存在")
		return
	}
	detail, err := d.Strategies.FindDetail(r.Context(), parsed.RouteStrategyID, access)
	if err != nil || detail == nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	d.writeStatsEnvelope(w, map[string]any{
		"action": "updated", "target": target.Public, "routeStrategy": sanitizeStrategy(detail),
	})
}

func (d *Deps) deleteRouteStrategy(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	unknown := strictObjectKeys(body, "targetUsername", "routeStrategyId")
	if unknown != nil {
		kernel.WriteBadRequest(w, zodUnrecognizedKeys(unknown...))
		return
	}
	parsed := &strategyDeleteBody{}
	if bodyHas(body, "targetUsername") {
		username, issue := requiredTrimmedBody(body, "targetUsername", 2, 80)
		if issue != "" {
			kernel.WriteBadRequest(w, issue)
			return
		}
		parsed.TargetUsername, parsed.HasTarget = username, true
	}
	strategyID, issue := requiredTrimmedBody(body, "routeStrategyId", 1, 120)
	if issue != "" {
		kernel.WriteBadRequest(w, issue)
		return
	}
	parsed.RouteStrategyID = strategyID
	context := AuthContextFrom(r)
	if context.IsTestToken {
		d.mockStrategyDelete(w, body)
		return
	}
	owner, err := d.findStrategyOwnerByID(r.Context(), parsed.RouteStrategyID)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	target, targetErr := d.resolveOwnedTarget(r.Context(), parsed.TargetUsername, parsed.HasTarget, &ownerLookup{ID: ownerID(owner), SystemAccountID: ownerAccountID(owner)})
	if targetErr != nil || target == nil {
		d.writeNotFoundEnvelope(w, "路由策略不存在")
		return
	}
	if target.Account.Status != "active" {
		kernel.WriteBadRequest(w, "目标用户已停用："+target.Account.Username)
		return
	}
	access := routestrategies.AccessScope{ViewerID: target.SystemAccountID}
	current, err := d.Strategies.FindDetail(r.Context(), parsed.RouteStrategyID, access)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if current == nil {
		d.writeNotFoundEnvelope(w, "路由策略不存在")
		return
	}
	deleted := sanitizeStrategy(current)
	result, err := d.Strategies.Delete(r.Context(), parsed.RouteStrategyID, access)
	if err != nil {
		kernelWriteBadRequest(w, serviceMessage(err, "路由策略删除失败"))
		return
	}
	if result == nil || !result.Deleted {
		d.writeNotFoundEnvelope(w, "路由策略不存在")
		return
	}
	d.writeStatsEnvelope(w, map[string]any{
		"action": "deleted", "target": target.Public, "routeStrategy": deleted,
	})
}

// strategyDeleteBody mirrors routeStrategyDeleteSchema (strict).
type strategyDeleteBody struct {
	TargetUsername  string
	HasTarget       bool
	RouteStrategyID string
}

func ownerID(owner *strategyOwnerLookup) string {
	if owner == nil {
		return ""
	}
	return owner.ID
}

func ownerAccountID(owner *strategyOwnerLookup) string {
	if owner == nil {
		return ""
	}
	return owner.SystemAccountID
}

func ownerUpdatedAt(owner *strategyOwnerLookup) string {
	if owner == nil {
		return ""
	}
	return owner.UpdatedAt
}

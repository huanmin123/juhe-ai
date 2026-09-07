// Authorization usage route family (BUG-0165 slice): the 8 aggregate usage
// endpoints plus the per-authorization usage detail, ported from
// authorizations.routes.ts (:54-60, :62-95, :167-215, :552-584) with the
// strict query schemas and the admin / my-* permission gates
// (docs/functions/接口契约与权限矩阵.md:543-548).
package authz

import (
	"net/http"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// usageQueryKeywords mirrors the strict usage schema key sets; the strict
// objects reject unknown query parameters with the zod unrecognized-keys
// message while the list and per-id usage schemas strip them
// (authorizations.routes.ts:37-60).
var (
	usageRowsQueryKeys = map[string]bool{
		"systemAccountId": true, "resourceType": true, "resourceId": true,
		"teamId": true, "startDate": true, "endDate": true,
		"page": true, "pageSize": true,
	}
	usageUserRowsQueryKeys = map[string]bool{
		"systemAccountId": true, "resourceType": true, "resourceId": true,
		"teamId": true, "granteeSystemAccountId": true,
		"startDate": true, "endDate": true, "page": true, "pageSize": true,
	}
	usageSummaryQueryKeys = map[string]bool{
		"systemAccountId": true, "resourceType": true, "resourceId": true,
		"teamId": true, "startDate": true, "endDate": true,
	}
	usageUserSummaryQueryKeys = map[string]bool{
		"systemAccountId": true, "resourceType": true, "resourceId": true,
		"teamId": true, "granteeSystemAccountId": true,
		"startDate": true, "endDate": true,
	}
	// usageIdQueryKeys is the non-strict per-id usage schema
	// (authorizationUsageQuerySchema :54-60): unknown parameters are stripped.
	usageIdQueryKeys = map[string]bool{}
	// listQueryKeys is the non-strict list schema (authorizationsQuerySchema
	// :37-52); unknown parameters are stripped, validated keys keep their
	// enums/ranges.
	listQueryKeys = map[string]bool{}
)

// resolveUsageRange mirrors normalizeAuthorizationUsageRangeAsync
// (authorizations.routes.ts:669-678): each side falls back independently —
// startDate ?? endDate ?? defaultRange.startDate, endDate ?? startDate ??
// defaultRange.endDate.
func (d *Deps) resolveUsageRange(startDate, endDate string) (UsageStatsRange, error) {
	timezone, err := d.Store.usageStatsTimezone(nil)
	if err != nil {
		return UsageStatsRange{}, err
	}
	defaultRange := defaultUsageStatsRange(timezone, d.Store.now())
	resolvedStart := startDate
	if resolvedStart == "" {
		resolvedStart = endDate
	}
	if resolvedStart == "" {
		resolvedStart = defaultRange.StartDate
	}
	resolvedEnd := endDate
	if resolvedEnd == "" {
		resolvedEnd = startDate
	}
	if resolvedEnd == "" {
		resolvedEnd = defaultRange.EndDate
	}
	return normalizeUsageStatsRange(resolvedStart, resolvedEnd, timezone, d.Store.now()), nil
}

// mountUsageRoutes wires the usage family onto both prefixes
// (docs/functions/接口契约与权限矩阵.md:543-548: the admin surfaces require
// the admin gate, the my-* surfaces accept every logged-in user with the self
// scope pinned).
func (d *Deps) mountUsageRoutes(k *kernel.Kernel, adminPrefix, selfPrefix string) {
	admin := d.RequireAdminAuthz
	self := d.RequireSelf
	k.Register("GET "+adminPrefix+"/usage/team-details", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.usageRows(w, r, "team", false)
	})))
	k.Register("GET "+adminPrefix+"/usage/team-summary", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.usageSummary(w, r, "team", false)
	})))
	k.Register("GET "+adminPrefix+"/usage/user-details", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.usageRows(w, r, "user", false)
	})))
	k.Register("GET "+adminPrefix+"/usage/user-summary", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.usageSummary(w, r, "user", false)
	})))
	k.Register("GET "+adminPrefix+"/{id}/usage", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.usageDetail(w, r, false)
	})))
	k.Register("DELETE "+adminPrefix+"/{id}/return", admin(http.HandlerFunc(d.adminReturn)))

	k.Register("GET "+selfPrefix+"/usage/team-details", self(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.usageRows(w, r, "team", true)
	})))
	k.Register("GET "+selfPrefix+"/usage/team-summary", self(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.usageSummary(w, r, "team", true)
	})))
	k.Register("GET "+selfPrefix+"/usage/user-details", self(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.usageRows(w, r, "user", true)
	})))
	k.Register("GET "+selfPrefix+"/usage/user-summary", self(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.usageSummary(w, r, "user", true)
	})))
	k.Register("GET "+selfPrefix+"/{id}/usage", self(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.usageDetail(w, r, true)
	})))
}

// usageQueryFailure renders a failed query parse (the English zod default
// messages stay verbatim through the upstream marker).
func usageQueryFailure(w http.ResponseWriter, parser *queryParser, fallback string) bool {
	if parser.issue != "" {
		if !containsCJK(parser.issue) {
			kernel.MarkUpstreamError(w)
		}
		kernel.WriteBadRequest(w, parser.issue)
		return false
	}
	kernel.WriteBadRequest(w, fallback)
	return false
}

// rejectUnknownQueryKeys mirrors the zod .strict() query objects
// (authorizations.routes.ts:75/:82/:86/:91): unknown keys reject with the
// zod default message before any field validation.
func rejectUnknownQueryKeys(w http.ResponseWriter, r *http.Request, allowed map[string]bool) bool {
	var unknown []string
	for key := range r.URL.Query() {
		if !allowed[key] {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) == 0 {
		return true
	}
	sortStrings(unknown)
	quoted := make([]string, 0, len(unknown))
	for _, key := range unknown {
		quoted = append(quoted, "'"+key+"'")
	}
	// The English zod text is preserved verbatim like the Node response.
	kernel.MarkUpstreamError(w)
	kernel.WriteBadRequest(w, "Unrecognized key(s) in object: "+strings.Join(quoted, ", "))
	return false
}

// parseUsageCommonQuery validates the shared usage filter fields
// (authorizationUsageCommonQueryShape :62-69).
func parseUsageCommonQuery(parser *queryParser) (UsageFilters, bool) {
	resourceType, ok := parser.enum("resourceType", "account", "group")
	if !ok {
		return UsageFilters{}, false
	}
	resourceID, ok := parser.text("resourceId", "授权资源 ID 不能为空")
	if !ok {
		return UsageFilters{}, false
	}
	teamID, ok := parser.text("teamId", "团队 ID 不能为空")
	if !ok {
		return UsageFilters{}, false
	}
	if _, ok := parser.date("startDate", "开始日期格式应为 YYYY-MM-DD"); !ok {
		return UsageFilters{}, false
	}
	if _, ok := parser.date("endDate", "结束日期格式应为 YYYY-MM-DD"); !ok {
		return UsageFilters{}, false
	}
	return UsageFilters{
		ResourceType: resourceType,
		ResourceID:   resourceID,
		TeamID:       teamID,
	}, true
}

// validUsageResourceFilter mirrors validAuthorizationUsageResourceFilter
// (:93-95): resourceId without resourceType is a refine failure on resourceId.
func validUsageResourceFilter(w http.ResponseWriter, filters UsageFilters) bool {
	if filters.ResourceID != "" && filters.ResourceType == "" {
		kernel.WriteBadRequest(w, "按资源筛选时必须指定资源类型")
		return false
	}
	return true
}

// usageRows serves GET {prefix}/usage/{team,user}-details
// (authorizations.routes.ts:167-180/:192-205).
func (d *Deps) usageRows(w http.ResponseWriter, r *http.Request, dimension string, selfOnly bool) {
	allowed := usageRowsQueryKeys
	if dimension == "user" {
		allowed = usageUserRowsQueryKeys
	}
	if !rejectUnknownQueryKeys(w, r, allowed) {
		return
	}
	parser := newQueryParser(r)
	filters, ok := parseUsageCommonQuery(parser)
	if !ok {
		usageQueryFailure(w, parser, "查询参数不合法")
		return
	}
	if dimension == "user" {
		granteeID, ok := parser.text("granteeSystemAccountId", "被授权用户 ID 不能为空")
		if !ok {
			usageQueryFailure(w, parser, "查询参数不合法")
			return
		}
		filters.GranteeID = granteeID
	}
	page, ok := parser.int("page", "页码必须大于 0", 0, "")
	if !ok {
		usageQueryFailure(w, parser, "查询参数不合法")
		return
	}
	pageSize, ok := parser.int("pageSize", "每页数量必须大于 0", UsageMaxPageSize, "每页最多 200 条")
	if !ok {
		usageQueryFailure(w, parser, "查询参数不合法")
		return
	}
	if !validUsageResourceFilter(w, filters) {
		return
	}
	if !parser.writeBadRequest(w, "查询参数不合法") {
		return
	}
	rng, err := d.resolveUsageRange(parser.query.Get("startDate"), parser.query.Get("endDate"))
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	page, pageSize = normalizeUsagePageOptions(page, pageSize)
	access := d.accessFor(r, selfOnly)
	if dimension == "team" {
		result, err := d.Store.teamUsageRows(r.Context(), filters, access, rng, page, pageSize)
		if err != nil {
			kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		kernel.WriteOK(w, result, "")
		return
	}
	result, err := d.Store.userUsageRows(r.Context(), filters, access, rng, page, pageSize)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	kernel.WriteOK(w, result, "")
}

// usageSummary serves GET {prefix}/usage/{team,user}-summary
// (authorizations.routes.ts:182-190/:207-215). The summary schemas accept no
// pagination keys (strict objects reject them).
func (d *Deps) usageSummary(w http.ResponseWriter, r *http.Request, dimension string, selfOnly bool) {
	allowed := usageSummaryQueryKeys
	if dimension == "user" {
		allowed = usageUserSummaryQueryKeys
	}
	if !rejectUnknownQueryKeys(w, r, allowed) {
		return
	}
	parser := newQueryParser(r)
	filters, ok := parseUsageCommonQuery(parser)
	if !ok {
		usageQueryFailure(w, parser, "查询参数不合法")
		return
	}
	if dimension == "user" {
		granteeID, ok := parser.text("granteeSystemAccountId", "被授权用户 ID 不能为空")
		if !ok {
			usageQueryFailure(w, parser, "查询参数不合法")
			return
		}
		filters.GranteeID = granteeID
	}
	if !validUsageResourceFilter(w, filters) {
		return
	}
	if !parser.writeBadRequest(w, "查询参数不合法") {
		return
	}
	rng, err := d.resolveUsageRange(parser.query.Get("startDate"), parser.query.Get("endDate"))
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	access := d.accessFor(r, selfOnly)
	if dimension == "team" {
		result, err := d.Store.teamUsageSummary(r.Context(), filters, access, rng)
		if err != nil {
			kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		kernel.WriteOK(w, result, "")
		return
	}
	result, err := d.Store.userUsageSummary(r.Context(), filters, access, rng)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	kernel.WriteOK(w, result, "")
}

// usageDetail serves GET {authorizations,my-authorizations}/{id}/usage
// (authorizations.routes.ts:552-584): the query schema is non-strict and
// validates the scope account, the optional date range and pagination; the
// grant must exist inside the access scope (404 otherwise).
func (d *Deps) usageDetail(w http.ResponseWriter, r *http.Request, selfOnly bool) {
	// authorizationUsageQuerySchema is a plain object: unknown query keys are
	// stripped, the declared keys keep their validation.
	parser := newQueryParser(r)
	if _, ok := parser.text("systemAccountId", "系统账号 ID 不能为空"); !ok {
		usageQueryFailure(w, parser, "查询参数不合法")
		return
	}
	startDate, _ := parser.date("startDate", "开始日期格式应为 YYYY-MM-DD")
	endDate, _ := parser.date("endDate", "结束日期格式应为 YYYY-MM-DD")
	page, ok := parser.int("page", "页码必须大于 0", 0, "")
	if !ok {
		usageQueryFailure(w, parser, "查询参数不合法")
		return
	}
	// The per-id usage schema caps pageSize only through the detail
	// normalization (max 200, default 200): no schema max here.
	pageSize, ok := parser.int("pageSize", "每页数量必须大于 0", 0, "")
	if !ok {
		usageQueryFailure(w, parser, "查询参数不合法")
		return
	}
	if !parser.writeBadRequest(w, "查询参数不合法") {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		kernel.WriteBadRequest(w, "授权记录 ID 不能为空")
		return
	}
	rng, err := d.resolveUsageRange(startDate, endDate)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	// Node reads run the expiry sweep before the summary lookup
	// (getResourceAuthorizationUsageAsync :94).
	if _, err := d.Store.ExpireSweep(r.Context(), 0); err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	access := d.accessFor(r, selfOnly)
	summary, err := d.Store.usageDetailSummary(r.Context(), id, access, rng, page, pageSize)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if summary == nil {
		kernel.WriteError(w, http.StatusNotFound, "授权记录不存在")
		return
	}
	kernel.WriteOK(w, summary, "")
}

// adminReturn serves DELETE /authorizations/{id}/return: the shared return
// mutation with the grantee resolved through userVisibleSystemAccountId
// (return.repository.ts:121/:166).
func (d *Deps) adminReturn(w http.ResponseWriter, r *http.Request) {
	d.returnValue(w, r, false)
}

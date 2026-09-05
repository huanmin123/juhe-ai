package statreads

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// Account-usage read family (Node repositories.ts
// getAccountUsageStatsOverviewPageAsync + account-usage.repository.ts list/
// summary/trend). The page query aggregates usage_stats_daily per account
// scope; the summary reads the system_account scope; the trend merges daily
// series from usage_stats_daily + usage_scope_range_windows.

const (
	accountUsageSelectedAccountLimit = 50
	accountUsageMaxListWindowRows    = 1001
)

// schedulableQueryValue mirrors schedulableQueryValue: all/enabled/disabled/
// cooling keep the value, everything else (including absent) is undefined.
// The account-usage page read never consumes it today; it only tolerates it.
func schedulableQueryValue(raw string) string {
	switch raw {
	case "all", "enabled", "disabled", "cooling":
		return raw
	default:
		return ""
	}
}

func (d *Deps) accountUsageHandler(selfOnly bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if queryIsPresent(r.URL.Query(), "includeSummary") {
			kernel.WriteBadRequest(w, "account-usage 列表不支持 includeSummary，请使用 /account-usage/summary")
			return
		}
		location, err := d.timezoneLocation(r.Context())
		if err != nil {
			d.writeReadError(w, err)
			return
		}
		todayKey := dateKeyIn(d.Now(), location)
		values := r.URL.Query()
		startDate := optionalQueryText(values, "startDate")
		endDate := optionalQueryText(values, "endDate")
		var rng Range
		if startDate != "" || endDate != "" {
			rng = normalizeRange(startDate, endDate, todayKey)
		} else {
			rng = fixedUsageStatsDefaultRange(todayKey)
		}
		scope := requestScope(r)
		if selfOnly {
			scope = selfScope(r)
		}
		page, hasPage := integerQueryValue(values, "page")
		pageSize, hasPageSize := integerQueryValue(values, "pageSize")
		if !hasPageSize {
			pageSize = 10
		}
		payload, err := d.accountUsageOverviewPage(r, accountUsagePageInput{
			Access:   scope,
			Range:    rng,
			Page:     page,
			HasPage:  hasPage,
			PageSize: pageSize,
			Keyword:  optionalQueryText(values, "keyword"),
			Accounts: parseAccountIDs(values["accountIds"]),
		})
		d.writeSection(w, payload, err)
	}
}

func (d *Deps) accountUsageOptionsHandler(selfOnly bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		values := r.URL.Query()
		keyword, keywordTooLong := boundedKeyword(values, "keyword")
		if keywordTooLong {
			// Node schema: keyword max 200; longer keywords are a 400.
			kernel.WriteBadRequest(w, "账户候选参数不合法")
			return
		}
		limit, hasLimit := integerQueryValue(values, "limit")
		if hasLimit && (limit < 1 || limit > 50) {
			kernel.WriteBadRequest(w, "账户候选参数不合法")
			return
		}
		selectedIds := parseAccountIDs(values["selectedIds"])
		selectedIds = append(selectedIds, parseAccountIDs(values["selectedIds[]"])...)
		if len(selectedIds) > 20 {
			selectedIds = selectedIds[:20]
		}
		scope := requestScope(r)
		if selfOnly {
			scope = selfScope(r)
		}
		payload, err := d.accountUsageOptions(r, scope, keyword, limit, hasLimit, selectedIds)
		d.writeSection(w, payload, err)
	}
}

func (d *Deps) accountUsageSummaryHandler(selfOnly bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		location, err := d.timezoneLocation(r.Context())
		if err != nil {
			d.writeReadError(w, err)
			return
		}
		todayKey := dateKeyIn(d.Now(), location)
		values := r.URL.Query()
		rng := normalizeRange(optionalQueryText(values, "startDate"), optionalQueryText(values, "endDate"), todayKey)
		scope := requestScope(r)
		if selfOnly {
			scope = selfScope(r)
		}
		summary, err := d.loadAccountUsageOverviewSummary(r, scope, rng)
		if err != nil {
			d.writeReadError(w, err)
			return
		}
		kernel.WriteOK(w, map[string]any{"range": rng, "summary": summary}, "")
	}
}

func (d *Deps) accountUsageTrendHandler(selfOnly bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		location, err := d.timezoneLocation(r.Context())
		if err != nil {
			d.writeReadError(w, err)
			return
		}
		todayKey := dateKeyIn(d.Now(), location)
		values := r.URL.Query()
		rng := normalizeRange(optionalQueryText(values, "startDate"), optionalQueryText(values, "endDate"), todayKey)
		accountIds := parseAccountIDs(values["accountIds"])
		if len(accountIds) > 10 {
			accountIds = accountIds[:10]
		}
		scope := requestScope(r)
		if selfOnly {
			scope = selfScope(r)
		}
		payload, err := d.accountUsageTrend(r, scope, rng, accountIds)
		d.writeSection(w, payload, err)
	}
}

// boundedKeyword mirrors the zod `.trim().max(200)` optional keyword schema;
// the second return distinguishes present-but-too-long keywords.
func boundedKeyword(values url.Values, key string) (string, bool) {
	raw, present := values[key]
	if !present {
		return "", false
	}
	text := strings.TrimSpace(raw[0])
	return text, len(text) > 200
}

// accountUsagePageInput mirrors AccountUsageStatsPageOptions for the page read.
type accountUsagePageInput struct {
	Access   AccessScope
	Range    Range
	Page     int
	HasPage  bool
	PageSize int
	Keyword  string
	Accounts []string
}

// accountUsageOverviewPage mirrors getAccountUsageStatsOverviewPageFromWindows.
func (d *Deps) accountUsageOverviewPage(r *http.Request, input accountUsagePageInput) (any, error) {
	pageSize := clampInt(input.PageSize, 1, 200)
	maxPage := accountUsageMaxListWindowRows / pageSize
	if (accountUsageMaxListWindowRows-1)/pageSize > maxPage {
		maxPage = (accountUsageMaxListWindowRows - 1) / pageSize
	}
	if maxPage < 1 {
		maxPage = 1
	}
	page := input.Page
	if !input.HasPage || page < 1 {
		page = 1
	}
	if page > maxPage {
		page = maxPage
	}
	usageScope := accountUsageListScope(input.Access)
	filterSQL, filterParams, err := d.accountUsageFilter(r, input, usageScope.scopeType)
	if err != nil {
		return nil, err
	}
	rows, err := d.queryStats(r, `
		SELECT
			usage_window.scope_id,
			SUM(usage_window.request_count) AS request_count,
			SUM(usage_window.input_tokens) AS input_tokens,
			SUM(usage_window.output_tokens) AS output_tokens,
			SUM(usage_window.cache_read_tokens) AS cache_read_tokens,
			SUM(usage_window.cache_read_cost_usd) AS cache_read_cost_usd,
			SUM(usage_window.total_cost_usd) AS total_cost,
			MAX(usage_window.last_used_at) AS last_used_at
		FROM `+d.statsTable("usage_stats_daily")+` usage_window
		WHERE usage_window.system_account_id = ?
			AND usage_window.scope_type = ?
			AND usage_window.stat_date >= ?
			AND usage_window.stat_date <= ?
			`+filterSQL+`
		GROUP BY usage_window.scope_id
		HAVING (
			SUM(usage_window.request_count) > 0
			OR SUM(usage_window.input_tokens) > 0
			OR SUM(usage_window.output_tokens) > 0
			OR SUM(usage_window.cache_read_tokens) > 0
			OR SUM(usage_window.total_cost_usd) > 0
			OR MAX(usage_window.last_used_at) IS NOT NULL
		)
		ORDER BY request_count DESC, total_cost DESC, (SUM(usage_window.input_tokens) + SUM(usage_window.output_tokens)) DESC, last_used_at DESC, usage_window.scope_id ASC
		LIMIT ? OFFSET ?
	`, flatParams([]any{usageScope.systemAccountID, usageScope.scopeType, input.Range.StartDate, input.Range.EndDate}, filterParams, pageSize+1, (page-1)*pageSize)...)
	if err != nil {
		return nil, err
	}
	pageRows, hasMore := takePageRows(rows, pageSize)
	excluded := make([]string, 0, len(pageRows))
	for _, row := range pageRows {
		excluded = append(excluded, row.text("scope_id"))
	}
	selectedRows, err := d.loadSelectedAccountUsageRows(r, input, usageScope, excluded)
	if err != nil {
		return nil, err
	}
	sourceRows := mergeAccountUsageSourceRows(pageRows, selectedRows)
	scopeIds := make([]string, 0, len(sourceRows))
	for _, row := range sourceRows {
		scopeIds = append(scopeIds, row.text("scope_id"))
	}
	metadataRows, err := d.loadAccountUsageMetadataRows(r, input.Access, scopeIds, usageScope.scopeType)
	if err != nil {
		return nil, err
	}
	metadataByID := map[string]Row{}
	for _, metadata := range metadataRows {
		metadataByID[metadata.text("id")] = metadata
	}
	overviewRows := []accountUsageStatsRow{}
	for _, row := range sourceRows {
		metadata, ok := metadataByID[row.text("scope_id")]
		if !ok {
			continue
		}
		systemAccountID := (*string)(nil)
		systemAccountName := (*string)(nil)
		if input.Access.canAccessAll() {
			id := metadata.text("system_account_id")
			systemAccountID = &id
			if value := metadata.nullText("system_account_name"); value != nil {
				systemAccountName = value
			}
		}
		ownerID := metadata.text("system_account_id")
		overviewRows = append(overviewRows, accountUsageStatsRow{
			ID:                     metadata.text("id"),
			SystemAccountID:        systemAccountID,
			SystemAccountName:      systemAccountName,
			OwnerSystemAccountID:   ownerID,
			OwnerSystemAccountName: metadata.nullText("system_account_name"),
			ProviderCode:           metadata.text("provider_code"),
			Name:                   metadata.text("name"),
			Type:                   metadata.text("type"),
			Status:                 metadata.text("status"),
			AccessType:             metadata.text("access_type"),
			RangeUsage:             mapUsageSummaryAggregate(row),
			DailyUsage:             []accountUsageDailyPoint{},
		})
	}
	defaultTrendIds, err := d.defaultTrendAccountIds(r, input.Access, overviewRows)
	if err != nil {
		return nil, err
	}
	upperBound := pagedTotalUpperBound(page, pageSize, len(pageRows), hasMore)
	alternative := (page-1)*pageSize + len(overviewRows)
	total := upperBound
	if alternative > total {
		total = alternative
	}
	return accountUsageStatsListResult{
		Range:                  input.Range,
		Rows:                   overviewRows,
		DefaultTrendAccountIds: defaultTrendIds,
		Total:                  total,
		HasMore:                hasMore,
		Page:                   page,
		PageSize:               pageSize,
	}, nil
}

// defaultTrendAccountIds mirrors loadAccountUsageDefaultTrendAccountIds plus
// withAllAccountsDefaultTrendIds: the latest last7d caller/account rank
// snapshot, falling back to the first ten rows with usage when the snapshot is
// empty for unscoped admins.
func (d *Deps) defaultTrendAccountIds(r *http.Request, scope AccessScope, rows []accountUsageStatsRow) ([]string, error) {
	scopedID := scope.scopedID()
	systemAccountID := scopedID
	if systemAccountID == "" && scope.canAccessAll() {
		systemAccountID = globalStatsSystemAccountID
	}
	if systemAccountID != "" {
		scopeType := "account"
		if scopedID != "" {
			scopeType = "caller_account"
		}
		snapshotRows, err := d.queryStats(r, `
			SELECT scope_id
			FROM `+d.statsTable("usage_rank_snapshots")+`
			WHERE system_account_id = ?
				AND scope_type = ?
				AND window_key = 'last7d'
				AND metric = 'request_count'
				AND snapshot_at = (
					SELECT MAX(snapshot_at)
					FROM `+d.statsTable("usage_rank_snapshots")+`
					WHERE system_account_id = ?
						AND scope_type = ?
						AND window_key = 'last7d'
						AND metric = 'request_count'
				)
			ORDER BY rank ASC
			LIMIT 10
		`, systemAccountID, scopeType, systemAccountID, scopeType)
		if err != nil {
			return nil, err
		}
		ids := []string{}
		for _, row := range snapshotRows {
			if id := row.text("scope_id"); id != "" {
				ids = append(ids, id)
			}
		}
		if len(ids) > 0 {
			return ids, nil
		}
	}
	if scope.scopedID() != "" || !scope.canAccessAll() {
		return []string{}, nil
	}
	fallback := []string{}
	for _, row := range rows {
		if len(fallback) >= 10 {
			break
		}
		if row.RangeUsage.RequestCount > 0 || row.RangeUsage.TotalTokens > 0 || row.RangeUsage.TotalCost > 0 {
			fallback = append(fallback, row.ID)
		}
	}
	if len(fallback) == 0 {
		return []string{}, nil
	}
	return fallback, nil
}

// loadSelectedAccountUsageRows mirrors loadSelectedAccountUsageRows: explicit
// accountIds not already on the page.
func (d *Deps) loadSelectedAccountUsageRows(r *http.Request, input accountUsagePageInput, usageScope accountUsageScopeState, excludeAccountIds []string) ([]Row, error) {
	excluded := map[string]bool{}
	for _, id := range excludeAccountIds {
		excluded[id] = true
	}
	selectedSet := map[string]bool{}
	accountIds := []string{}
	for _, id := range input.Accounts {
		if id == "" || excluded[id] || selectedSet[id] {
			continue
		}
		selectedSet[id] = true
		accountIds = append(accountIds, id)
	}
	if len(accountIds) > accountUsageSelectedAccountLimit {
		accountIds = accountIds[:accountUsageSelectedAccountLimit]
	}
	if len(accountIds) == 0 {
		return nil, nil
	}
	filterSQL, filterParams := scopeIDFilter(accountIds)
	return d.queryStats(r, `
		SELECT
			usage_window.scope_id,
			SUM(usage_window.request_count) AS request_count,
			SUM(usage_window.input_tokens) AS input_tokens,
			SUM(usage_window.output_tokens) AS output_tokens,
			SUM(usage_window.cache_read_tokens) AS cache_read_tokens,
			SUM(usage_window.cache_read_cost_usd) AS cache_read_cost_usd,
			SUM(usage_window.total_cost_usd) AS total_cost,
			MAX(usage_window.last_used_at) AS last_used_at
		FROM `+d.statsTable("usage_stats_daily")+` usage_window
		WHERE usage_window.system_account_id = ?
			AND usage_window.scope_type = ?
			AND usage_window.stat_date >= ?
			AND usage_window.stat_date <= ?
			AND `+filterSQL+`
		GROUP BY usage_window.scope_id
		ORDER BY request_count DESC, total_cost DESC, (SUM(usage_window.input_tokens) + SUM(usage_window.output_tokens)) DESC, last_used_at DESC, usage_window.scope_id ASC
	`, flatParams([]any{usageScope.systemAccountID, usageScope.scopeType, input.Range.StartDate, input.Range.EndDate}, filterParams)...)
}

func mergeAccountUsageSourceRows(pageRows, selectedRows []Row) []Row {
	seen := map[string]bool{}
	merged := []Row{}
	for _, row := range append(append([]Row{}, pageRows...), selectedRows...) {
		id := row.text("scope_id")
		if seen[id] {
			continue
		}
		seen[id] = true
		merged = append(merged, row)
	}
	return merged
}

// accountUsageOptions mirrors listAccountUsageOptions: a business-database
// account option search plus the selected ids window.
func (d *Deps) accountUsageOptions(r *http.Request, scope AccessScope, keyword string, limit int, hasLimit bool, selectedIds []string) (any, error) {
	ownerScope := accountUsageOptionScope(scope)
	safeLimit := 50
	if hasLimit {
		safeLimit = clampInt(limit, 1, 50)
	}
	searchRows, err := d.queryAccountUsageOptionRows(r, ownerScope, keyword, nil, safeLimit)
	if err != nil {
		return nil, err
	}
	var selectedRows []Row
	if len(selectedIds) > 0 {
		selectedRows, err = d.queryAccountUsageOptionRows(r, ownerScope, "", selectedIds, len(selectedIds))
		if err != nil {
			return nil, err
		}
	}
	seen := map[string]bool{}
	rows := []Row{}
	for _, row := range append(append([]Row{}, selectedRows...), searchRows...) {
		id := row.text("id")
		if seen[id] {
			continue
		}
		seen[id] = true
		rows = append(rows, row)
	}
	includeSystemAccount := scope.canAccessAll()
	options := make([]accountUsageStatsOption, 0, len(rows))
	for _, row := range rows {
		option := accountUsageStatsOption{
			ID:                     row.text("id"),
			OwnerSystemAccountID:   row.text("owner_system_account_id"),
			OwnerSystemAccountName: row.nullText("owner_system_account_name"),
			ProviderCode:           row.text("provider_code"),
			ProviderName:           row.text("provider_name"),
			Name:                   row.text("name"),
			Type:                   row.text("type"),
			Status:                 row.text("status"),
			AccessType:             row.text("access_type"),
		}
		if includeSystemAccount {
			id := row.text("system_account_id")
			option.SystemAccountID = &id
			if value := row.nullText("system_account_name"); value != nil {
				option.SystemAccountName = value
			}
		}
		options = append(options, option)
	}
	return options, nil
}

func (d *Deps) queryAccountUsageOptionRows(r *http.Request, ownerSystemAccountID string, keyword string, ids []string, limit int) ([]Row, error) {
	clauses := []string{"accounts.deleted_at IS NULL"}
	params := []any{}
	if ownerSystemAccountID != "" {
		clauses = append(clauses, "accounts.system_account_id = ?")
		params = append(params, ownerSystemAccountID)
	} else {
		clauses = append(clauses, "accounts.authorization_instance_authorization_id IS NULL")
	}
	if len(ids) > 0 {
		clauses = append(clauses, "accounts.id IN ("+placeholders(len(ids))+")")
		params = append(params, idsToAny(ids)...)
	} else if keyword != "" {
		clauses = append(clauses, "instr(accounts.name, ?) > 0")
		params = append(params, keyword)
	}
	return d.queryBusiness(r, `
		SELECT
			accounts.id,
			accounts.system_account_id,
			COALESCE(system_accounts.display_name, system_accounts.username) AS system_account_name,
			COALESCE(accounts.authorization_instance_owner_system_account_id, accounts.system_account_id) AS owner_system_account_id,
			COALESCE(owner_accounts.display_name, owner_accounts.username) AS owner_system_account_name,
			accounts.provider_code,
			COALESCE(providers.name, accounts.provider_code) AS provider_name,
			accounts.name,
			accounts.type,
			accounts.status,
			CASE WHEN accounts.authorization_instance_authorization_id IS NULL THEN 'owner' ELSE 'authorized' END AS access_type
		FROM `+d.businessTable("accounts")+` accounts
		LEFT JOIN `+d.businessTable("providers")+` providers ON providers.code = accounts.provider_code
		LEFT JOIN `+d.businessTable("system_accounts")+` system_accounts ON system_accounts.id = accounts.system_account_id
		LEFT JOIN `+d.businessTable("system_accounts")+` owner_accounts
			ON owner_accounts.id = COALESCE(accounts.authorization_instance_owner_system_account_id, accounts.system_account_id)
		WHERE `+strings.Join(clauses, " AND ")+`
		ORDER BY accounts.name ASC, accounts.id ASC
		LIMIT ?
	`, append(params, limit)...)
}

// loadAccountUsageOverviewSummary mirrors loadAccountUsageOverviewSummary.
func (d *Deps) loadAccountUsageOverviewSummary(r *http.Request, scope AccessScope, rng Range) (accountUsageSummary, error) {
	scopeSystemAccountID, scopeID := accountUsageOverviewSummaryScope(scope)
	rows, err := d.queryStats(r, `
		SELECT
			COALESCE(SUM(request_count), 0) AS request_count,
			COALESCE(SUM(input_tokens), 0) AS input_tokens,
			COALESCE(SUM(output_tokens), 0) AS output_tokens,
			COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens,
			COALESCE(SUM(cache_read_cost_usd), 0) AS cache_read_cost_usd,
			COALESCE(SUM(cache_write_tokens), 0) AS cache_write_tokens,
			COALESCE(SUM(cache_write_1h_tokens), 0) AS cache_write_1h_tokens,
			COALESCE(SUM(cache_write_cost_usd), 0) AS cache_write_cost_usd,
			COALESCE(SUM(thinking_tokens), 0) AS thinking_tokens,
			COALESCE(SUM(input_image_tokens), 0) AS input_image_tokens,
			COALESCE(SUM(output_image_tokens), 0) AS output_image_tokens,
			COALESCE(SUM(total_cost_usd), 0) AS total_cost,
			MAX(last_used_at) AS last_used_at
		FROM `+d.statsTable("usage_stats_daily")+`
		WHERE system_account_id = ?
			AND scope_type = 'system_account'
			AND scope_id = ?
			AND stat_date >= ?
			AND stat_date <= ?
	`, scopeSystemAccountID, scopeID, rng.StartDate, rng.EndDate)
	row, found, err := firstRow(rows, err)
	if err != nil {
		return accountUsageSummary{}, err
	}
	if !found {
		return emptyAccountUsageSummary(), nil
	}
	return mapUsageSummaryAggregate(row), nil
}

// accountUsageTrend mirrors getAccountUsageStatsTrendAsync.
func (d *Deps) accountUsageTrend(r *http.Request, scope AccessScope, rng Range, accountIds []string) (any, error) {
	ids := uniqueNonEmpty(accountIds)
	if len(ids) > 10 {
		ids = ids[:10]
	}
	if len(ids) == 0 {
		return accountUsageStatsTrendOverview{Range: rng, Rows: []accountUsageStatsTrendRow{}}, nil
	}
	input := accountUsagePageInput{Access: scope, Range: rng, Accounts: ids}
	usageScope := accountUsageListScope(scope)
	sourceRows, err := d.loadSelectedAccountUsageRows(r, input, usageScope, nil)
	if err != nil {
		return nil, err
	}
	scopeIds := make([]string, 0, len(sourceRows))
	for _, row := range sourceRows {
		scopeIds = append(scopeIds, row.text("scope_id"))
	}
	metadataRows, err := d.loadAccountUsageMetadataRows(r, scope, scopeIds, usageScope.scopeType)
	if err != nil {
		return nil, err
	}
	dailySeries, err := d.loadUsageDailySeries(r, trendScopes(usageScope, metadataRows), rng)
	if err != nil {
		return nil, err
	}
	rows := make([]accountUsageStatsTrendRow, 0, len(metadataRows))
	for _, metadata := range metadataRows {
		id := metadata.text("id")
		row := accountUsageStatsTrendRow{
			ID:                     id,
			OwnerSystemAccountID:   metadata.text("system_account_id"),
			OwnerSystemAccountName: metadata.nullText("system_account_name"),
			ProviderCode:           metadata.text("provider_code"),
			Name:                   metadata.text("name"),
			AccessType:             metadata.text("access_type"),
			DailyUsage:             []accountUsageDailyPoint{},
		}
		if scope.canAccessAll() {
			systemAccountID := metadata.text("system_account_id")
			row.SystemAccountID = &systemAccountID
			row.SystemAccountName = metadata.nullText("system_account_name")
		}
		if series, ok := dailySeries[id]; ok {
			row.DailyUsage = series
		}
		rows = append(rows, row)
	}
	return accountUsageStatsTrendOverview{Range: rng, Rows: rows}, nil
}

// trendScopes mirrors accountUsageTrendScopes.
func trendScopes(usageScope accountUsageScopeState, metadataRows []Row) []usageScopeRequest {
	scopes := make([]usageScopeRequest, 0, len(metadataRows))
	for _, metadata := range metadataRows {
		scopes = append(scopes, usageScopeRequest{
			RowKey:          metadata.text("id"),
			SystemAccountID: usageScope.systemAccountID,
			ScopeType:       usageScope.scopeType,
			ScopeID:         metadata.text("id"),
		})
	}
	return scopes
}

// loadUsageDailySeries mirrors loadUsageDailySeriesForScopeRequests: the daily
// buckets from usage_stats_daily plus the range summary row from
// usage_scope_range_windows.
func (d *Deps) loadUsageDailySeries(r *http.Request, scopes []usageScopeRequest, rng Range) (map[string][]accountUsageDailyPoint, error) {
	dateKeys := dateKeysInRange(rng)
	result := map[string][]accountUsageDailyPoint{}
	validScopes := []usageScopeRequest{}
	for _, scope := range scopes {
		if scope.RowKey == "" || scope.SystemAccountID == "" || scope.ScopeType == "" || scope.ScopeID == "" {
			continue
		}
		validScopes = append(validScopes, scope)
		result[scope.RowKey] = emptyDailyUsage(dateKeys)
	}
	if len(validScopes) == 0 || len(dateKeys) == 0 {
		return result, nil
	}
	scopesBySystemAccount := map[string][]usageScopeRequest{}
	systemAccountOrder := []string{}
	for _, scope := range validScopes {
		if _, ok := scopesBySystemAccount[scope.SystemAccountID]; !ok {
			systemAccountOrder = append(systemAccountOrder, scope.SystemAccountID)
		}
		scopesBySystemAccount[scope.SystemAccountID] = append(scopesBySystemAccount[scope.SystemAccountID], scope)
	}
	var rows []Row
	for _, systemAccountID := range systemAccountOrder {
		systemScopes := scopesBySystemAccount[systemAccountID]
		scopeTypes := []string{}
		scopeTypeSeen := map[string]bool{}
		scopeIds := []string{}
		scopeIDSeen := map[string]bool{}
		for _, scope := range systemScopes {
			if !scopeTypeSeen[scope.ScopeType] {
				scopeTypeSeen[scope.ScopeType] = true
				scopeTypes = append(scopeTypes, scope.ScopeType)
			}
			if !scopeIDSeen[scope.ScopeID] {
				scopeIDSeen[scope.ScopeID] = true
				scopeIds = append(scopeIds, scope.ScopeID)
			}
		}
		for _, chunk := range chunkStrings(scopeIds, 400) {
			dailyRows, err := d.queryStats(r, `
				SELECT
					system_account_id,
					scope_type,
					scope_id,
					stat_date,
					request_count,
					input_tokens,
					output_tokens,
					cache_read_tokens,
					cache_read_cost_usd,
					cache_write_tokens,
					cache_write_1h_tokens,
					cache_write_cost_usd,
					thinking_tokens,
					input_image_tokens,
					output_image_tokens,
					total_cost_usd AS total_cost,
					last_used_at
				FROM `+d.statsTable("usage_stats_daily")+`
				WHERE system_account_id = ?
					AND scope_type IN (`+placeholders(len(scopeTypes))+`)
					AND scope_id IN (`+placeholders(len(chunk))+`)
					AND stat_date >= ?
					AND stat_date <= ?
			`, flatParams([]any{systemAccountID}, idsToAny(scopeTypes), idsToAny(chunk), rng.StartDate, rng.EndDate)...)
			if err != nil {
				return nil, err
			}
			rows = append(rows, dailyRows...)
			rangeRows, err := d.queryStats(r, `
				SELECT
					system_account_id,
					scope_type,
					scope_id,
					NULL AS stat_date,
					request_count,
					input_tokens,
					output_tokens,
					cache_read_tokens,
					cache_read_cost_usd,
					cache_write_tokens,
					cache_write_1h_tokens,
					cache_write_cost_usd,
					thinking_tokens,
					input_image_tokens,
					output_image_tokens,
					total_cost_usd AS total_cost,
					last_used_at
				FROM `+d.statsTable("usage_scope_range_windows")+`
				WHERE system_account_id = ?
					AND scope_type IN (`+placeholders(len(scopeTypes))+`)
					AND scope_id IN (`+placeholders(len(chunk))+`)
					AND start_date = ?
					AND end_date = ?
			`, flatParams([]any{systemAccountID}, idsToAny(scopeTypes), idsToAny(chunk), rng.StartDate, rng.EndDate)...)
			if err != nil {
				return nil, err
			}
			rows = append(rows, rangeRows...)
		}
	}
	dateIndex := map[string]int{}
	for index, statDate := range dateKeys {
		dateIndex[statDate] = index
	}
	for _, row := range rows {
		rowKey := row.text("scope_id")
		series, ok := result[rowKey]
		if !ok {
			continue
		}
		statDate := row.nullText("stat_date")
		if statDate == nil {
			// Range summary rows only contribute when the caller asked for the
			// rangeUsage; the trend rows map daily buckets only. Node keeps
			// rangeUsage in a sibling field the trend route never returns.
			continue
		}
		index, ok := dateIndex[*statDate]
		if !ok {
			continue
		}
		series[index] = accountUsageDailyPointFromRow(row, *statDate)
	}
	return result, nil
}

func emptyDailyUsage(dateKeys []string) []accountUsageDailyPoint {
	points := make([]accountUsageDailyPoint, 0, len(dateKeys))
	for _, statDate := range dateKeys {
		points = append(points, accountUsageDailyPoint{StatDate: statDate})
	}
	return points
}

// accountUsageScopeState mirrors the accountUsageListScope output.
type accountUsageScopeState struct {
	systemAccountID string
	scopeType       string
}

func accountUsageListScope(scope AccessScope) accountUsageScopeState {
	if scopedID := scope.scopedID(); scopedID != "" {
		return accountUsageScopeState{systemAccountID: scopedID, scopeType: "caller_account"}
	}
	return accountUsageScopeState{systemAccountID: globalStatsSystemAccountID, scopeType: "account"}
}

func accountUsageOptionScope(scope AccessScope) string {
	if scopedID := scope.scopedID(); scopedID != "" {
		return scopedID
	}
	if scope.canAccessAll() {
		return ""
	}
	return scope.currentID()
}

func accountUsageOverviewSummaryScope(scope AccessScope) (string, string) {
	if scopedID := scope.scopedID(); scopedID != "" {
		return scopedID, scopedID
	}
	if scope.canAccessAll() {
		return globalStatsSystemAccountID, globalStatsScopeID
	}
	id := scope.currentID()
	return id, id
}

// accountUsageFilter mirrors accountUsageFilterPredicate (type is never
// provided by the account-usage route; only the keyword path filters).
func (d *Deps) accountUsageFilter(r *http.Request, input accountUsagePageInput, scopeType string) (string, []any, error) {
	keyword := strings.TrimSpace(input.Keyword)
	if scopeType == "account" && keyword == "" {
		return "", nil, nil
	}
	if keyword != "" {
		accountIds, err := d.loadAccountUsageKeywordAccountIds(r, input.Access, keyword, scopeType)
		if err != nil {
			return "", nil, err
		}
		if len(accountIds) == 0 {
			return "AND 0 = 1", nil, nil
		}
		filterSQL, filterParams := scopeIDFilter(accountIds)
		return "AND " + filterSQL, filterParams, nil
	}
	return "", nil, nil
}

// loadAccountUsageKeywordAccountIds mirrors loadAccountUsageKeywordAccountIds
// (SQLite instr variant, selected-account limit 50).
func (d *Deps) loadAccountUsageKeywordAccountIds(r *http.Request, scope AccessScope, keyword string, scopeType string) ([]string, error) {
	viewerID := scope.scopedID()
	if viewerID == "" {
		viewerID = scope.currentID()
	}
	ids := []string{}
	appendIDs := func(rows []Row) {
		seen := map[string]bool{}
		for _, id := range ids {
			seen[id] = true
		}
		for _, row := range rows {
			id := row.text("id")
			if id == "" || seen[id] || len(ids) >= accountUsageSelectedAccountLimit {
				continue
			}
			seen[id] = true
			ids = append(ids, id)
		}
	}
	base := `(
		(instr(accounts.name, ?) > 0)
		OR (instr(accounts.provider_code, ?) > 0)
		OR (instr(accounts.type, ?) > 0)
		OR EXISTS (
			SELECT 1
			FROM ` + d.businessTable("group_accounts") + ` group_accounts
			INNER JOIN ` + d.businessTable("groups") + ` groups ON groups.id = group_accounts.group_id
			WHERE group_accounts.account_id = accounts.id
				AND group_accounts.system_account_id = ?
				AND group_accounts.enabled = 1
				AND instr(groups.name, ?) > 0
		)
	)`
	baseParams := []any{keyword, keyword, keyword, viewerID, keyword}
	clauses := []string{base}
	params := append([]any{}, baseParams...)
	if scopeType == "caller_account" {
		clauses = append(clauses, `(
			accounts.system_account_id = ?
			OR EXISTS (
				SELECT 1
				FROM `+d.businessTable("resource_authorizations")+` visible_authorization
				WHERE visible_authorization.resource_type = 'account'
					AND visible_authorization.resource_id = accounts.id
					AND visible_authorization.grantee_system_account_id = ?
					AND visible_authorization.status = 'active'
					AND (visible_authorization.expires_at IS NULL OR visible_authorization.expires_at > ?)
			)
			OR EXISTS (
				SELECT 1
				FROM `+d.businessTable("group_accounts")+` visible_group_account
				INNER JOIN `+d.businessTable("resource_authorizations")+` visible_group_authorization
					ON visible_group_authorization.resource_type = 'group'
					AND visible_group_authorization.resource_id = visible_group_account.group_id
					AND visible_group_authorization.grantee_system_account_id = ?
					AND visible_group_authorization.status = 'active'
					AND (visible_group_authorization.expires_at IS NULL OR visible_group_authorization.expires_at > ?)
				WHERE visible_group_account.account_id = accounts.id
					AND visible_group_account.enabled = 1
			)
		)`)
		now := rfc3339Millis(d.Now())
		params = append(params, viewerID, viewerID, now, viewerID, now)
	}
	rows, err := d.queryBusiness(r, `
		SELECT accounts.id
		FROM `+d.businessTable("accounts")+` accounts
		WHERE `+strings.Join(clauses, " AND ")+`
		ORDER BY accounts.name ASC, accounts.id ASC
		LIMIT ?
	`, append(params, accountUsageSelectedAccountLimit)...)
	if err != nil {
		return nil, err
	}
	appendIDs(rows)
	// Authorized instance accounts whose source account name matches.
	instanceParams := []any{keyword}
	instanceClauses := []string{"(instr(source_accounts.name, ?) > 0)"}
	if scopeType == "caller_account" {
		instanceClauses = append(instanceClauses, "instance_accounts.system_account_id = ?")
		instanceParams = append(instanceParams, viewerID)
	}
	rows, err = d.queryBusiness(r, `
		SELECT instance_accounts.id
		FROM `+d.businessTable("accounts")+` source_accounts
		INNER JOIN `+d.businessTable("accounts")+` instance_accounts
			ON instance_accounts.authorization_instance_source_account_id = source_accounts.id
		WHERE `+joinAND(instanceClauses)+`
		ORDER BY source_accounts.name ASC, instance_accounts.id ASC
		LIMIT ?
	`, append(instanceParams, accountUsageSelectedAccountLimit)...)
	if err != nil {
		return nil, err
	}
	appendIDs(rows)
	if scopeType == "caller_account" {
		now := rfc3339Millis(d.Now())
		rows, err = d.queryBusiness(r, `
			SELECT accounts.id
			FROM `+d.businessTable("accounts")+` accounts
			INNER JOIN `+d.businessTable("group_accounts")+` group_accounts
				ON group_accounts.account_id = accounts.id
				AND group_accounts.enabled = 1
			INNER JOIN `+d.businessTable("resource_authorizations")+` group_authorization
				ON group_authorization.resource_type = 'group'
				AND group_authorization.resource_id = group_accounts.group_id
				AND group_authorization.grantee_system_account_id = ?
				AND group_authorization.status = 'active'
				AND (group_authorization.expires_at IS NULL OR group_authorization.expires_at > ?)
			WHERE (instr(accounts.name, ?) > 0)
			ORDER BY accounts.name ASC, accounts.id ASC
			LIMIT ?
		`, viewerID, now, keyword, accountUsageSelectedAccountLimit)
		if err != nil {
			return nil, err
		}
		appendIDs(rows)
	}
	return ids, nil
}

// loadAccountUsageMetadataRows mirrors loadAccountUsageMetadataRows.
func (d *Deps) loadAccountUsageMetadataRows(r *http.Request, scope AccessScope, accountIds []string, scopeType string) ([]Row, error) {
	ids := uniqueNonEmpty(accountIds)
	if len(ids) == 0 {
		return nil, nil
	}
	viewerID := scope.scopedID()
	if viewerID == "" {
		viewerID = scope.currentID()
	}
	authorizationJoin := ""
	accessTypeExpr := "'owner'"
	authorizationIDExpr := "NULL"
	var headParams []any
	if scopeType == "caller_account" {
		authorizationJoin = ` LEFT JOIN ` + d.businessTable("resource_authorizations") + ` usage_authorization
			ON usage_authorization.resource_type = 'account'
			AND usage_authorization.grantee_system_account_id = ?
			AND usage_authorization.status = 'active'
			AND (usage_authorization.expires_at IS NULL OR usage_authorization.expires_at > ?)
			AND (
				usage_authorization.id = accounts.authorization_instance_authorization_id
				OR (
					accounts.authorization_instance_authorization_id IS NULL
					AND usage_authorization.resource_id = accounts.id
				)
			)`
		accessTypeExpr = "CASE WHEN accounts.authorization_instance_authorization_id IS NOT NULL THEN 'authorized' WHEN accounts.system_account_id = ? THEN 'owner' ELSE 'authorized' END"
		authorizationIDExpr = "COALESCE(accounts.authorization_instance_authorization_id, usage_authorization.id)"
		headParams = []any{viewerID, viewerID, rfc3339Millis(d.Now()), viewerID}
	}
	rows := []Row{}
	for _, chunk := range chunkStrings(ids, 900) {
		chunkRows, err := d.queryBusiness(r, `
			SELECT
				accounts.id,
				accounts.system_account_id,
				COALESCE(system_accounts.display_name, system_accounts.username, accounts.system_account_id) AS system_account_name,
				accounts.provider_code,
				accounts.name,
				accounts.type,
				accounts.status,
				`+accessTypeExpr+` AS access_type,
				`+authorizationIDExpr+` AS authorization_id
			FROM `+d.businessTable("accounts")+` accounts
			LEFT JOIN `+d.businessTable("system_accounts")+` system_accounts ON system_accounts.id = accounts.system_account_id
			`+authorizationJoin+`
			WHERE accounts.id IN (`+placeholders(len(chunk))+`)
		`, append(flatParams(headParams), idsToAny(chunk)...)...)
		if err != nil {
			return nil, err
		}
		rows = append(rows, chunkRows...)
	}
	// Preserve the input order (Node orders by the ids index).
	order := map[string]int{}
	for index, id := range ids {
		order[id] = index
	}
	for i := 0; i < len(rows); i++ {
		for j := i + 1; j < len(rows); j++ {
			if order[rows[j].text("id")] < order[rows[i].text("id")] {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
	}
	return rows, nil
}

// scopeIDFilter mirrors buildAccountUsageScopeIdFilter.
func scopeIDFilter(accountIds []string) (string, []any) {
	ids := uniqueNonEmpty(accountIds)
	if len(ids) == 0 {
		return "0 = 1", nil
	}
	chunks := chunkStrings(ids, 400)
	if len(chunks) == 1 {
		return "usage_window.scope_id IN (" + placeholders(len(chunks[0])) + ")", idsToAny(chunks[0])
	}
	clauses := []string{}
	params := []any{}
	for _, chunk := range chunks {
		clauses = append(clauses, "usage_window.scope_id IN ("+placeholders(len(chunk))+")")
		params = append(params, idsToAny(chunk)...)
	}
	return "(" + strings.Join(clauses, " OR ") + ")", params
}

// mapUsageSummaryAggregate mirrors usageSummaryFromAggregate.
func mapUsageSummaryAggregate(row Row) accountUsageSummary {
	inputTokens := row.number("input_tokens")
	outputTokens := row.number("output_tokens")
	return accountUsageSummary{
		RequestCount:       int64(row.number("request_count")),
		InputTokens:        int64(inputTokens),
		OutputTokens:       int64(outputTokens),
		CacheReadTokens:    int64(row.number("cache_read_tokens")),
		CacheReadCost:      row.number("cache_read_cost_usd"),
		CacheWriteTokens:   int64(row.number("cache_write_tokens")),
		CacheWrite1hTokens: int64(row.number("cache_write_1h_tokens")),
		CacheWriteCost:     row.number("cache_write_cost_usd"),
		ThinkingTokens:     int64(row.number("thinking_tokens")),
		InputImageTokens:   int64(row.number("input_image_tokens")),
		OutputImageTokens:  int64(row.number("output_image_tokens")),
		TotalTokens:        int64(inputTokens + outputTokens),
		TotalCost:          row.number("total_cost"),
		LastUsedAt:         row.nullText("last_used_at"),
	}
}

func emptyAccountUsageSummary() accountUsageSummary {
	return accountUsageSummary{}
}

func accountUsageDailyPointFromRow(row Row, statDate string) accountUsageDailyPoint {
	point := accountUsageDailyPoint{StatDate: statDate}
	summary := mapUsageSummaryAggregate(row)
	point.RequestCount = summary.RequestCount
	point.InputTokens = summary.InputTokens
	point.OutputTokens = summary.OutputTokens
	point.CacheReadTokens = summary.CacheReadTokens
	point.CacheReadCost = summary.CacheReadCost
	point.CacheWriteTokens = summary.CacheWriteTokens
	point.CacheWrite1hTokens = summary.CacheWrite1hTokens
	point.CacheWriteCost = summary.CacheWriteCost
	point.ThinkingTokens = summary.ThinkingTokens
	point.InputImageTokens = summary.InputImageTokens
	point.OutputImageTokens = summary.OutputImageTokens
	point.TotalTokens = summary.TotalTokens
	point.TotalCost = summary.TotalCost
	point.LastUsedAt = summary.LastUsedAt
	return point
}

// accountUsageStatsListResult mirrors AccountUsageStatsListResult.
type accountUsageStatsListResult struct {
	Range                  Range                  `json:"range"`
	Rows                   []accountUsageStatsRow `json:"rows"`
	DefaultTrendAccountIds []string               `json:"defaultTrendAccountIds"`
	Total                  int                    `json:"total"`
	HasMore                bool                   `json:"hasMore"`
	Page                   int                    `json:"page"`
	PageSize               int                    `json:"pageSize"`
}

type accountUsageStatsRow struct {
	ID                     string                   `json:"id"`
	SystemAccountID        *string                  `json:"systemAccountId,omitempty"`
	SystemAccountName      *string                  `json:"systemAccountName,omitempty"`
	OwnerSystemAccountID   string                   `json:"ownerSystemAccountId"`
	OwnerSystemAccountName *string                  `json:"ownerSystemAccountName,omitempty"`
	ProviderCode           string                   `json:"providerCode"`
	Name                   string                   `json:"name"`
	Type                   string                   `json:"type"`
	Status                 string                   `json:"status"`
	AccessType             string                   `json:"accessType"`
	RangeUsage             accountUsageSummary      `json:"rangeUsage"`
	DailyUsage             []accountUsageDailyPoint `json:"dailyUsage"`
}

type accountUsageStatsTrendOverview struct {
	Range Range                       `json:"range"`
	Rows  []accountUsageStatsTrendRow `json:"rows"`
}

type accountUsageStatsTrendRow struct {
	ID                     string                   `json:"id"`
	SystemAccountID        *string                  `json:"systemAccountId,omitempty"`
	SystemAccountName      *string                  `json:"systemAccountName,omitempty"`
	OwnerSystemAccountID   string                   `json:"ownerSystemAccountId"`
	OwnerSystemAccountName *string                  `json:"ownerSystemAccountName,omitempty"`
	ProviderCode           string                   `json:"providerCode"`
	Name                   string                   `json:"name"`
	AccessType             string                   `json:"accessType"`
	DailyUsage             []accountUsageDailyPoint `json:"dailyUsage"`
}

type accountUsageStatsOption struct {
	ID                     string  `json:"id"`
	SystemAccountID        *string `json:"systemAccountId,omitempty"`
	SystemAccountName      *string `json:"systemAccountName,omitempty"`
	OwnerSystemAccountID   string  `json:"ownerSystemAccountId"`
	OwnerSystemAccountName *string `json:"ownerSystemAccountName,omitempty"`
	ProviderCode           string  `json:"providerCode"`
	ProviderName           string  `json:"providerName"`
	Name                   string  `json:"name"`
	Type                   string  `json:"type"`
	Status                 string  `json:"status"`
	AccessType             string  `json:"accessType"`
}

type accountUsageSummary struct {
	RequestCount       int64   `json:"requestCount"`
	InputTokens        int64   `json:"inputTokens"`
	OutputTokens       int64   `json:"outputTokens"`
	CacheReadTokens    int64   `json:"cacheReadTokens"`
	CacheReadCost      float64 `json:"cacheReadCost"`
	CacheWriteTokens   int64   `json:"cacheWriteTokens"`
	CacheWrite1hTokens int64   `json:"cacheWrite1hTokens"`
	CacheWriteCost     float64 `json:"cacheWriteCost"`
	ThinkingTokens     int64   `json:"thinkingTokens"`
	InputImageTokens   int64   `json:"inputImageTokens"`
	OutputImageTokens  int64   `json:"outputImageTokens"`
	TotalTokens        int64   `json:"totalTokens"`
	TotalCost          float64 `json:"totalCost"`
	LastUsedAt         *string `json:"lastUsedAt"`
}

type accountUsageDailyPoint struct {
	StatDate           string  `json:"statDate"`
	RequestCount       int64   `json:"requestCount"`
	InputTokens        int64   `json:"inputTokens"`
	OutputTokens       int64   `json:"outputTokens"`
	CacheReadTokens    int64   `json:"cacheReadTokens"`
	CacheReadCost      float64 `json:"cacheReadCost"`
	CacheWriteTokens   int64   `json:"cacheWriteTokens"`
	CacheWrite1hTokens int64   `json:"cacheWrite1hTokens"`
	CacheWriteCost     float64 `json:"cacheWriteCost"`
	ThinkingTokens     int64   `json:"thinkingTokens"`
	InputImageTokens   int64   `json:"inputImageTokens"`
	OutputImageTokens  int64   `json:"outputImageTokens"`
	TotalTokens        int64   `json:"totalTokens"`
	TotalCost          float64 `json:"totalCost"`
	LastUsedAt         *string `json:"lastUsedAt,omitempty"`
}

type usageScopeRequest struct {
	RowKey          string
	SystemAccountID string
	ScopeType       string
	ScopeID         string
}

// takePageRows mirrors takePageRows.
func takePageRows(rows []Row, pageSize int) ([]Row, bool) {
	if len(rows) > pageSize {
		return rows[:pageSize], true
	}
	return rows, false
}

// pagedTotalUpperBound mirrors pagedTotalUpperBound.
func pagedTotalUpperBound(page, pageSize, itemCount int, hasMore bool) int {
	return (page-1)*pageSize + itemCount + boolToInt(hasMore)
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func clampInt(value, minValue, maxValue int) int {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func chunkStrings(values []string, chunkSize int) [][]string {
	size := chunkSize
	if size < 1 {
		size = 1
	}
	chunks := [][]string{}
	for index := 0; index < len(values); index += size {
		end := index + size
		if end > len(values) {
			end = len(values)
		}
		chunks = append(chunks, values[index:end])
	}
	return chunks
}

func flatParams(groups ...any) []any {
	params := []any{}
	for _, group := range groups {
		switch typed := group.(type) {
		case []any:
			params = append(params, typed...)
		case []string:
			params = append(params, idsToAny(typed)...)
		default:
			params = append(params, typed)
		}
	}
	return params
}

func joinAND(clauses []string) string { return strings.Join(clauses, " AND ") }

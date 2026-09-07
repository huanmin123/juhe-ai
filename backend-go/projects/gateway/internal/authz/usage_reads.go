// Authorization usage window reads (BUG-0165 slice): the
// authorization_team_usage_range_windows / authorization_user_usage_range_windows
// query family ported from storage/authorization-usage.repository.ts. Window
// rows live in the stats database (juhe_stats on PostgreSQL, the dedicated
// stats SQLite file otherwise), keyed by
// (system_account_id, start_date, end_date, [grantee_]team/resource filters).
package authz

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// usageStatsMaxRangeDays mirrors ACCOUNT_USAGE_STATS_MAX_RANGE_DAYS
// (usage-stats-helpers.ts:11) and FIXED_RANGE_WINDOW_DAYS (:4 of
// usage-stats-window-helpers.ts) — both 31.
const usageStatsMaxRangeDays = 31

// UsageStatsRange mirrors AccountUsageStatsRange.
type UsageStatsRange struct {
	StartDate string `json:"startDate"`
	EndDate   string `json:"endDate"`
	Days      int    `json:"days"`
	MaxDays   int    `json:"maxDays"`
}

// UsageRowSummary mirrors AuthorizationUsageRowSummary
// (authorization-usage.repository.ts authorizationUsageRowSummary).
type UsageRowSummary struct {
	RequestCount float64 `json:"requestCount"`
	TotalTokens  float64 `json:"totalTokens"`
	TotalCost    float64 `json:"totalCost"`
}

// UsageAggregateSummary mirrors AuthorizationUsageAggregateSummary
// (authorizationUsageAggregateSummary :688-698).
type UsageAggregateSummary struct {
	RequestCount     float64  `json:"requestCount"`
	InputTokens      float64  `json:"inputTokens"`
	CacheWriteTokens float64  `json:"cacheWriteTokens"`
	TotalTokens      float64  `json:"totalTokens"`
	TotalCost        float64  `json:"totalCost"`
	LastUsedAt       *string  `json:"lastUsedAt,omitempty"`
}

// TeamUsageRow mirrors AuthorizationTeamUsageRow (:1920-1931 domain/types.ts):
// always-present string fields keep the Node `?? ''` shape, optional owner and
// timestamp fields stay absent.
type TeamUsageRow struct {
	ID                            string          `json:"id"`
	TeamID                        string          `json:"teamId"`
	TeamName                      string          `json:"teamName"`
	ResourceType                  string          `json:"resourceType"`
	ResourceID                    string          `json:"resourceId"`
	ResourceName                  string          `json:"resourceName"`
	AccountOwnerSystemAccountID   string          `json:"accountOwnerSystemAccountId,omitempty"`
	AccountOwnerSystemAccountName string          `json:"accountOwnerSystemAccountName,omitempty"`
	Usage                         UsageRowSummary `json:"usage"`
	LastUsedAt                    string          `json:"lastUsedAt,omitempty"`
}

// TeamUsageRowsResult mirrors AuthorizationTeamUsageRowsResult.
type TeamUsageRowsResult struct {
	Range    UsageStatsRange `json:"range"`
	Rows     []TeamUsageRow  `json:"rows"`
	Total    int             `json:"total"`
	Page     int             `json:"page"`
	PageSize int             `json:"pageSize"`
	HasMore  bool            `json:"hasMore"`
}

// TeamUsageSummaryResult mirrors AuthorizationTeamUsageSummary.
type TeamUsageSummaryResult struct {
	Range   UsageStatsRange       `json:"range"`
	Summary UsageAggregateSummary `json:"summary"`
}

// UserUsageRow mirrors AuthorizationUserUsageRow (:1947-1958): no resourceId /
// teamId fields; teamNames is always an array (possibly empty).
type UserUsageRow struct {
	ID                            string          `json:"id"`
	UserName                      string          `json:"userName"`
	Username                      string          `json:"username,omitempty"`
	TeamNames                     []string        `json:"teamNames"`
	ResourceType                  string          `json:"resourceType"`
	ResourceName                  string          `json:"resourceName"`
	AccountOwnerSystemAccountName string          `json:"accountOwnerSystemAccountName,omitempty"`
	Usage                         UsageRowSummary `json:"usage"`
	LastUsedAt                    string          `json:"lastUsedAt,omitempty"`
}

// UserUsageRowsResult mirrors AuthorizationUserUsageRowsResult.
type UserUsageRowsResult struct {
	Range    UsageStatsRange `json:"range"`
	Rows     []UserUsageRow  `json:"rows"`
	Total    int             `json:"total"`
	Page     int             `json:"page"`
	PageSize int             `json:"pageSize"`
	HasMore  bool            `json:"hasMore"`
}

// UserUsageSummaryResult mirrors AuthorizationUserUsageSummary.
type UserUsageSummaryResult struct {
	Range   UsageStatsRange       `json:"range"`
	Summary UsageAggregateSummary `json:"summary"`
}

// UsageFilters mirrors AuthorizationUsageFilters; resourceID is only applied
// when resourceType is set (Node `filters.resourceId && filters.resourceType`).
type UsageFilters struct {
	ResourceType string
	ResourceID   string
	TeamID       string
	GranteeID    string
}

// usageWindowRow is the shared numeric projection of both window tables.
type usageWindowRow struct {
	AggregateKey       string
	TeamFilterID       string
	GranteeFilterID    string
	ResourceFilterType string
	ResourceFilterID   string
	RequestCount       float64
	InputTokens        float64
	OutputTokens       float64
	CacheReadTokens    float64
	CacheReadCostUsd   float64
	CacheWriteTokens   float64
	CacheWrite1hTokens float64
	CacheWriteCostUsd  float64
	ThinkingTokens     float64
	InputImageTokens   float64
	OutputImageTokens  float64
	TotalCostUsd       float64
	LastUsedAt         sql.NullString
}

// statsQueryDB resolves the stats database handle: PostgreSQL shares the
// business pool (juhe_stats qualification), SQLite uses the injected stats
// file handle and falls back to the business handle in the single-file test
// fixtures.
func (s *Store) statsQueryDB() *sql.DB {
	if s.stats != nil {
		return s.stats
	}
	return s.db
}

// usageScopeKey mirrors authorizationReportFilterKey
// (authorization-usage.repository.ts:465-478): an administrator without a
// filter reads the 'global' aggregate rows, everyone else is pinned to the
// manageable owner; a non-admin without an identity yields no key (empty
// result, never another owner's data).
func usageScopeKey(access accessInfo) (string, bool) {
	if access.IsAdmin {
		if access.FilterID != "" {
			return access.FilterID, true
		}
		return "global", true
	}
	if access.ViewerID != "" {
		return access.ViewerID, true
	}
	return "", false
}

// usageResourceFilter mirrors authorizationReportFilterKey's resourceFilterId
// rule: the resource id only applies when the type is present.
func (f UsageFilters) resourceFilterID() string {
	if f.ResourceID != "" && f.ResourceType != "" {
		return f.ResourceID
	}
	return ""
}

// usageDetailResourcePredicate mirrors authorizationDetailResourcePredicate
// (:480-497).
func usageDetailResourcePredicate(filterType, filterID string) (string, []string) {
	switch {
	case filterType == "" || filterType == "all":
		return "report.resource_filter_type IN ('account', 'group') AND report.resource_filter_id <> ''", nil
	case filterID == "":
		return "report.resource_filter_type = ? AND report.resource_filter_id <> ''", []string{filterType}
	default:
		return "report.resource_filter_type = ? AND report.resource_filter_id = ?", []string{filterType, filterID}
	}
}

const usageWindowRowColumns = `report.request_count, report.input_tokens, report.output_tokens,
	report.cache_read_tokens, report.cache_read_cost_usd, report.cache_write_tokens, report.cache_write_1h_tokens,
	report.cache_write_cost_usd, report.thinking_tokens, report.input_image_tokens, report.output_image_tokens,
	report.total_cost_usd, report.last_used_at`

func scanUsageWindowRow(scanner interface{ Scan(...any) error }) (usageWindowRow, error) {
	var row usageWindowRow
	err := scanner.Scan(&row.RequestCount, &row.InputTokens, &row.OutputTokens,
		&row.CacheReadTokens, &row.CacheReadCostUsd, &row.CacheWriteTokens, &row.CacheWrite1hTokens,
		&row.CacheWriteCostUsd, &row.ThinkingTokens, &row.InputImageTokens, &row.OutputImageTokens,
		&row.TotalCostUsd, &row.LastUsedAt)
	return row, err
}

// summaryAggregate mirrors usageSummaryFromAggregate for the aggregate
// summary projection (:683-698).
func summaryAggregate(row *usageWindowRow) UsageAggregateSummary {
	if row == nil {
		return UsageAggregateSummary{}
	}
	lastUsedAt := (*string)(nil)
	if row.LastUsedAt.Valid && row.LastUsedAt.String != "" {
		value := row.LastUsedAt.String
		lastUsedAt = &value
	}
	return UsageAggregateSummary{
		RequestCount:     row.RequestCount,
		InputTokens:      row.InputTokens,
		CacheWriteTokens: row.CacheWriteTokens,
		TotalTokens:      row.InputTokens + row.OutputTokens,
		TotalCost:        row.TotalCostUsd,
		LastUsedAt:       lastUsedAt,
	}
}

// rowSummary mirrors authorizationUsageRowSummary (:683-686).
func rowSummary(row usageWindowRow) UsageRowSummary {
	return UsageRowSummary{
		RequestCount: row.RequestCount,
		TotalTokens:  row.InputTokens + row.OutputTokens,
		TotalCost:    row.TotalCostUsd,
	}
}

// teamUsageRows mirrors getAuthorizationTeamUsageRows (:93-164 SQLite and the
// PG variant :181-267; the two share one SQL modulo numeric casts that
// database/sql handles through the driver).
func (s *Store) teamUsageRows(ctx context.Context, filters UsageFilters, access accessInfo, rng UsageStatsRange, page, pageSize int) (*TeamUsageRowsResult, error) {
	key, ok := usageScopeKey(access)
	if !ok {
		return emptyTeamRows(rng, page, pageSize), nil
	}
	filterType := filters.ResourceType
	if filterType == "" {
		filterType = "all"
	}
	resourcePredicate, predicateArgs := usageDetailResourcePredicate(filterType, filters.resourceFilterID())
	query := `SELECT report.team_filter_id, report.resource_filter_type, report.resource_filter_id,
		` + usageWindowRowColumns + `
		FROM ` + s.statsTable("authorization_team_usage_range_windows") + ` report
		WHERE report.system_account_id = ?
			AND report.start_date = ?
			AND report.end_date = ?
			AND report.team_filter_id <> ''
			AND (? = '' OR report.team_filter_id = ?)
			AND ` + resourcePredicate + `
		ORDER BY report.total_cost_usd DESC, report.request_count DESC, report.last_used_at DESC,
			report.team_filter_id ASC, report.resource_filter_type ASC, report.resource_filter_id ASC
		LIMIT ? OFFSET ?`
	args := []any{key, rng.StartDate, rng.EndDate, filters.TeamID, filters.TeamID}
	args = append(args, toStrings(predicateArgs)...)
	args = append(args, pageSize+1, (page-1)*pageSize)
	rows, err := s.statsQueryDB().QueryContext(ctx, s.bind(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var windowRows []usageWindowRow
	for rows.Next() {
		var aggregate usageWindowRow
		var teamID, resourceType, resourceID string
		if err := rows.Scan(&teamID, &resourceType, &resourceID,
			&aggregate.RequestCount, &aggregate.InputTokens, &aggregate.OutputTokens,
			&aggregate.CacheReadTokens, &aggregate.CacheReadCostUsd, &aggregate.CacheWriteTokens, &aggregate.CacheWrite1hTokens,
			&aggregate.CacheWriteCostUsd, &aggregate.ThinkingTokens, &aggregate.InputImageTokens, &aggregate.OutputImageTokens,
			&aggregate.TotalCostUsd, &aggregate.LastUsedAt); err != nil {
			return nil, err
		}
		aggregate.TeamFilterID = teamID
		aggregate.ResourceFilterType = resourceType
		aggregate.ResourceFilterID = resourceID
		windowRows = append(windowRows, aggregate)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	hasMore := len(windowRows) > pageSize
	if hasMore {
		windowRows = windowRows[:pageSize]
	}
	teams, err := s.loadTeamNameMap(ctx, windowRowTeamIDs(windowRows))
	if err != nil {
		return nil, err
	}
	resources, err := s.loadResourceInfoMap(ctx, windowRows)
	if err != nil {
		return nil, err
	}
	owners, err := s.loadOwnerNameMap(ctx, resources)
	if err != nil {
		return nil, err
	}
	items := make([]TeamUsageRow, 0, len(windowRows))
	for _, row := range windowRows {
		resource := resources[resourceKey(row.ResourceFilterType, row.ResourceFilterID)]
		item := TeamUsageRow{
			ID:           strings.Join(nonEmpty([]string{row.TeamFilterID, row.ResourceFilterType, row.ResourceFilterID}), ":"),
			TeamID:       row.TeamFilterID,
			TeamName:     teams[row.TeamFilterID],
			ResourceType: row.ResourceFilterType,
			ResourceID:   row.ResourceFilterID,
			ResourceName: resource.name,
			Usage:        rowSummary(row),
		}
		if resource.ownerID != "" {
			item.AccountOwnerSystemAccountID = resource.ownerID
			item.AccountOwnerSystemAccountName = owners[resource.ownerID]
		}
		if row.LastUsedAt.Valid && row.LastUsedAt.String != "" {
			item.LastUsedAt = row.LastUsedAt.String
		}
		items = append(items, item)
	}
	return &TeamUsageRowsResult{
		Range: rng, Rows: items,
		Total:    pagedTotalUpperBound(page, pageSize, len(items), hasMore),
		Page:     page, PageSize: pageSize, HasMore: hasMore,
	}, nil
}

// teamUsageSummary mirrors getAuthorizationTeamUsageSummary +
// loadAuthorizationTeamUsageSummary (:166-179, :499-532).
func (s *Store) teamUsageSummary(ctx context.Context, filters UsageFilters, access accessInfo, rng UsageStatsRange) (*TeamUsageSummaryResult, error) {
	key, ok := usageScopeKey(access)
	if !ok {
		return &TeamUsageSummaryResult{Range: rng, Summary: UsageAggregateSummary{}}, nil
	}
	filterType := filters.ResourceType
	if filterType == "" {
		filterType = "all"
	}
	query := `SELECT ` + windowAggregateColumns + `
		FROM ` + s.statsTable("authorization_team_usage_range_windows") + `
		WHERE system_account_id = ?
			AND start_date = ?
			AND end_date = ?
			AND team_filter_id = ?
			AND resource_filter_type = ?
			AND resource_filter_id = ?
		LIMIT 1`
	row := s.statsQueryDB().QueryRowContext(ctx, s.bind(query), key,
		rng.StartDate, rng.EndDate, filters.TeamID, filterType, filters.resourceFilterID())
	aggregate, err := scanUsageWindowRow(row)
	if err == sql.ErrNoRows {
		return &TeamUsageSummaryResult{Range: rng, Summary: UsageAggregateSummary{}}, nil
	}
	if err != nil {
		return nil, err
	}
	return &TeamUsageSummaryResult{Range: rng, Summary: summaryAggregate(&aggregate)}, nil
}

// userUsageRows mirrors getAuthorizationUserUsageRows (:269-345 SQLite, PG
// :362-454): the team filter matches unconditionally (empty string means the
// no-team aggregate rows).
func (s *Store) userUsageRows(ctx context.Context, filters UsageFilters, access accessInfo, rng UsageStatsRange, page, pageSize int) (*UserUsageRowsResult, error) {
	key, ok := usageScopeKey(access)
	if !ok {
		return emptyUserRows(rng, page, pageSize), nil
	}
	filterType := filters.ResourceType
	if filterType == "" {
		filterType = "all"
	}
	resourcePredicate, predicateArgs := usageDetailResourcePredicate(filterType, filters.resourceFilterID())
	query := `SELECT report.team_filter_id, report.grantee_filter_system_account_id,
		report.resource_filter_type, report.resource_filter_id,
		` + usageWindowRowColumns + `
		FROM ` + s.statsTable("authorization_user_usage_range_windows") + ` report
		WHERE report.system_account_id = ?
			AND report.start_date = ?
			AND report.end_date = ?
			AND report.team_filter_id = ?
			AND report.grantee_filter_system_account_id <> ''
			AND (? = '' OR report.grantee_filter_system_account_id = ?)
			AND ` + resourcePredicate + `
		ORDER BY report.total_cost_usd DESC, report.request_count DESC, report.last_used_at DESC,
			report.grantee_filter_system_account_id ASC, report.resource_filter_type ASC, report.resource_filter_id ASC
		LIMIT ? OFFSET ?`
	args := []any{key, rng.StartDate, rng.EndDate, filters.TeamID,
		filters.GranteeID, filters.GranteeID}
	args = append(args, toStrings(predicateArgs)...)
	args = append(args, pageSize+1, (page-1)*pageSize)
	rows, err := s.statsQueryDB().QueryContext(ctx, s.bind(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var windowRows []usageWindowRow
	for rows.Next() {
		var aggregate usageWindowRow
		var teamID, granteeID, resourceType, resourceID string
		if err := rows.Scan(&teamID, &granteeID, &resourceType, &resourceID,
			&aggregate.RequestCount, &aggregate.InputTokens, &aggregate.OutputTokens,
			&aggregate.CacheReadTokens, &aggregate.CacheReadCostUsd, &aggregate.CacheWriteTokens, &aggregate.CacheWrite1hTokens,
			&aggregate.CacheWriteCostUsd, &aggregate.ThinkingTokens, &aggregate.InputImageTokens, &aggregate.OutputImageTokens,
			&aggregate.TotalCostUsd, &aggregate.LastUsedAt); err != nil {
			return nil, err
		}
		aggregate.TeamFilterID = teamID
		aggregate.GranteeFilterID = granteeID
		aggregate.ResourceFilterType = resourceType
		aggregate.ResourceFilterID = resourceID
		windowRows = append(windowRows, aggregate)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	hasMore := len(windowRows) > pageSize
	if hasMore {
		windowRows = windowRows[:pageSize]
	}
	accounts, err := s.loadPrincipalMap(ctx, windowRowGranteeIDs(windowRows))
	if err != nil {
		return nil, err
	}
	teams, err := s.loadTeamNameMap(ctx, windowRowTeamIDs(windowRows))
	if err != nil {
		return nil, err
	}
	resources, err := s.loadResourceInfoMap(ctx, windowRows)
	if err != nil {
		return nil, err
	}
	owners, err := s.loadOwnerNameMap(ctx, resources)
	if err != nil {
		return nil, err
	}
	items := make([]UserUsageRow, 0, len(windowRows))
	for _, row := range windowRows {
		account := accounts[row.GranteeFilterID]
		resource := resources[resourceKey(row.ResourceFilterType, row.ResourceFilterID)]
		item := UserUsageRow{
			ID:           strings.Join(nonEmpty([]string{row.GranteeFilterID, row.ResourceFilterType, row.ResourceFilterID}), ":"),
			UserName:     account.displayName,
			ResourceType: row.ResourceFilterType,
			ResourceName: resource.name,
			TeamNames:    userUsageTeamNames(row.TeamFilterID, teams),
			Usage:        rowSummary(row),
		}
		if account.username != "" {
			item.Username = account.username
		}
		if resource.ownerID != "" {
			item.AccountOwnerSystemAccountName = owners[resource.ownerID]
		}
		if row.LastUsedAt.Valid && row.LastUsedAt.String != "" {
			item.LastUsedAt = row.LastUsedAt.String
		}
		items = append(items, item)
	}
	return &UserUsageRowsResult{
		Range: rng, Rows: items,
		Total:    pagedTotalUpperBound(page, pageSize, len(items), hasMore),
		Page:     page, PageSize: pageSize, HasMore: hasMore,
	}, nil
}

// userUsageSummary mirrors getAuthorizationUserUsageSummary +
// loadAuthorizationUserUsageSummary (:347-360, :569-604).
func (s *Store) userUsageSummary(ctx context.Context, filters UsageFilters, access accessInfo, rng UsageStatsRange) (*UserUsageSummaryResult, error) {
	key, ok := usageScopeKey(access)
	if !ok {
		return &UserUsageSummaryResult{Range: rng, Summary: UsageAggregateSummary{}}, nil
	}
	filterType := filters.ResourceType
	if filterType == "" {
		filterType = "all"
	}
	query := `SELECT ` + windowAggregateColumns + `
		FROM ` + s.statsTable("authorization_user_usage_range_windows") + `
		WHERE system_account_id = ?
			AND start_date = ?
			AND end_date = ?
			AND team_filter_id = ?
			AND grantee_filter_system_account_id = ?
			AND resource_filter_type = ?
			AND resource_filter_id = ?
		LIMIT 1`
	row := s.statsQueryDB().QueryRowContext(ctx, s.bind(query), key,
		rng.StartDate, rng.EndDate, filters.TeamID, filters.GranteeID, filterType, filters.resourceFilterID())
	aggregate, err := scanUsageWindowRow(row)
	if err == sql.ErrNoRows {
		return &UserUsageSummaryResult{Range: rng, Summary: UsageAggregateSummary{}}, nil
	}
	if err != nil {
		return nil, err
	}
	return &UserUsageSummaryResult{Range: rng, Summary: summaryAggregate(&aggregate)}, nil
}

// windowAggregateColumns is the summary select list (the row column set
// without the group key columns).
const windowAggregateColumns = `request_count, input_tokens, output_tokens,
	cache_read_tokens, cache_read_cost_usd, cache_write_tokens, cache_write_1h_tokens,
	cache_write_cost_usd, thinking_tokens, input_image_tokens, output_image_tokens,
	total_cost_usd, last_used_at`

func emptyTeamRows(rng UsageStatsRange, page, pageSize int) *TeamUsageRowsResult {
	return &TeamUsageRowsResult{Range: rng, Rows: []TeamUsageRow{},
		Page: page, PageSize: pageSize}
}

func emptyUserRows(rng UsageStatsRange, page, pageSize int) *UserUsageRowsResult {
	return &UserUsageRowsResult{Range: rng, Rows: []UserUsageRow{},
		Page: page, PageSize: pageSize}
}

func windowRowTeamIDs(rows []usageWindowRow) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.TeamFilterID != "" {
			ids = append(ids, row.TeamFilterID)
		}
	}
	return ids
}

func windowRowGranteeIDs(rows []usageWindowRow) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.GranteeFilterID != "" {
			ids = append(ids, row.GranteeFilterID)
		}
	}
	return ids
}

func userUsageTeamNames(teamID string, teams map[string]string) []string {
	if teamID == "" {
		return []string{}
	}
	if name := teams[teamID]; name != "" {
		return []string{name}
	}
	return []string{}
}

func nonEmpty(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}

func toStrings(values []string) []any {
	result := make([]any, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	return result
}

// normalizeUsagePageOptions mirrors normalizeAuthorizationUsagePageOptions
// (:456-463): pageSize defaults to 20 with a 200 cap, and the page is clamped
// into the 1001-row window bound.
func normalizeUsagePageOptions(page, pageSize int) (int, int) {
	if pageSize < 1 {
		pageSize = UsageDefaultPageSize
	}
	if pageSize > UsageMaxPageSize {
		pageSize = UsageMaxPageSize
	}
	maxPage := (UsageMaxListWindowRows - 1) / pageSize
	if maxPage < 1 {
		maxPage = 1
	}
	if page < 1 {
		page = 1
	}
	if page > maxPage {
		page = maxPage
	}
	return page, pageSize
}

// pagedTotalUpperBound mirrors query-utils.ts pagedTotalUpperBound.
func pagedTotalUpperBound(page, pageSize, itemCount int, hasMore bool) int {
	return (page - 1) * pageSize + itemCount + boolToInt(hasMore)
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// usageStatsTimezone resolves the usageStatsTimezone system setting through
// the injected source, defaulting to the system_settings read of the business
// handle (Node usageStatsTimezoneAsync).
func (s *Store) usageStatsTimezone(ctx context.Context) (string, error) {
	if s.timezone != nil {
		return s.timezone(ctx)
	}
	return readSystemSettingsTimezone(ctx, s.db, s.pg)
}

// defaultUsageStatsRange mirrors fixedUsageStatsDefaultRange
// (usage-stats-window-helpers.ts:90-99): the trailing 31-day fixed window.
func defaultUsageStatsRange(timezone string, now time.Time) UsageStatsRange {
	todayKey := dateKeyAt(now, timezone)
	return UsageStatsRange{
		StartDate: addCalendarDays(todayKey, -(usageStatsMaxRangeDays - 1)),
		EndDate:   todayKey,
		Days:      usageStatsMaxRangeDays,
		MaxDays:   usageStatsMaxRangeDays,
	}
}

// normalizeUsageStatsRange mirrors normalizeAccountUsageStatsRange
// (usage-stats-helpers.ts:182-214): clamp both ends to today and the trailing
// 31-day window, then collapse an inverted range.
func normalizeUsageStatsRange(startDate, endDate, timezone string, now time.Time) UsageStatsRange {
	todayKey := dateKeyAt(now, timezone)
	earliest := addCalendarDays(todayKey, -(usageStatsMaxRangeDays - 1))
	end := todayKey
	if endDate != "" {
		end = clampCalendarDate(endDate, todayKey, earliest)
	}
	start := todayKey
	if startDate != "" {
		start = clampCalendarDate(startDate, todayKey, earliest)
	}
	if start > end {
		start = end
	}
	if earliestStart := addCalendarDays(end, -(usageStatsMaxRangeDays - 1)); start < earliestStart {
		start = earliestStart
	}
	return UsageStatsRange{
		StartDate: start,
		EndDate:   end,
		Days:      calendarDaysBetweenInclusive(start, end),
		MaxDays:   usageStatsMaxRangeDays,
	}
}

func clampCalendarDate(value, todayKey, earliest string) string {
	if value > todayKey {
		return todayKey
	}
	if value < earliest {
		return earliest
	}
	return value
}

// dateKeyAt mirrors dateKey(date, timezone) (usage-stats-helpers.ts:140-143).
func dateKeyAt(now time.Time, timezone string) string {
	location, err := time.LoadLocation(timezone)
	if err != nil || location == nil {
		return now.UTC().Format("2006-01-02")
	}
	return now.In(location).Format("2006-01-02")
}

// addCalendarDays advances a YYYY-MM-DD key by calendar days without
// consulting the host timezone (nextCalendarDateKey semantics).
func addCalendarDays(value string, days int) string {
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		return value
	}
	return parsed.AddDate(0, 0, days).Format("2006-01-02")
}

func calendarDaysBetweenInclusive(start, end string) int {
	startTime, startErr := time.Parse("2006-01-02", start)
	endTime, endErr := time.Parse("2006-01-02", end)
	if startErr != nil || endErr != nil {
		return 1
	}
	days := int(endTime.Sub(startTime).Hours()/24) + 1
	if days < 1 {
		return 1
	}
	return days
}

// list.go ports client-ip-stats-list.repository.ts: the pre-aggregated list
// query over client_ip_registry LEFT JOIN client_ip_usage_range_windows with
// client_ip_policies status labeling, stats_job_state readiness, keyword
// prefix filtering, static whitelist ordering, the lastUsed window filter and
// the bounded 1001-row progressive pagination window.
package ipstats

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode/utf16"
)

// List executes the list read. The Node source queries with empty join dates
// while the range window is not ready, so registry rows still surface with
// zeroed usage; empty stats tables therefore return an empty set.
func (s *Store) List(ctx context.Context, options ListOptions) (*ListResult, error) {
	ctx = ensureCtx(ctx)
	timezoneName, err := s.tz(ctx)
	if err != nil {
		return nil, err
	}
	location, err := time.LoadLocation(timezoneName)
	if err != nil {
		return nil, errors.New("系统设置 usageStatsTimezone 无效：" + timezoneName)
	}
	now := s.now()
	searchRange := normalizeRange(options.StartDate, options.EndDate, now, location)
	page, pageSize := options.Page, options.PageSize
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize

	ready, err := s.rangeReady(ctx, searchRange.StartDate, searchRange.EndDate)
	if err != nil {
		return nil, err
	}
	queryStartDate := searchRange.StartDate
	queryEndDate := searchRange.EndDate
	if !ready {
		queryStartDate = ""
		queryEndDate = ""
	}

	policyNow := s.nowISO()
	where, whereArgs, err := s.buildRangeWhere(ctx, options, policyNow)
	if err != nil {
		return nil, err
	}
	policySets, err := s.activePolicySets(ctx, policyNow)
	if err != nil {
		return nil, err
	}

	rows, err := s.queryListRows(ctx, queryStartDate, queryEndDate, where, whereArgs, options)
	if err != nil {
		return nil, err
	}

	lastUsedRange := normalizeRange(options.LastUsedStartDate, options.LastUsedEndDate, now, location)
	var lastUsedWindow *lastUsedEpochWindow
	if options.LastUsedStartDate != "" || options.LastUsedEndDate != "" {
		lastUsedWindow = newLastUsedEpochWindow(lastUsedRange, location)
	}

	filtered := make([]ListRow, 0, len(rows))
	for _, row := range rows {
		labelRowStatus(&row, policySets)
		if !rowMatchesLastUsedWindow(row, lastUsedWindow) {
			continue
		}
		filtered = append(filtered, row)
	}
	sorted, err := sortRows(filtered, options.SortField, options.SortOrder, options.LastUsedSortScope)
	if err != nil {
		return nil, err
	}

	end := offset + pageSize
	hasMore := len(sorted) > end || len(rows) == maxListWindowRows
	pageRows := sorted
	if offset < len(sorted) {
		if end > len(sorted) {
			end = len(sorted)
		}
		pageRows = sorted[offset:end]
	} else {
		pageRows = []ListRow{}
	}
	return &ListResult{
		Items:          pageRows,
		PageUpperBound: (page-1)*pageSize + len(pageRows) + boolToInt(hasMore),
		HasMore:        hasMore,
		Page:           page,
		PageSize:       pageSize,
		Range:          searchRange,
		RangeReady:     ready,
	}, nil
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// rangeReady mirrors clientIpUsageRangeWindowReady: pending dirty hashes make
// the window stale; otherwise the stats_job_state success cursor decides and
// the fallback is any materialized window row.
func (s *Store) rangeReady(ctx context.Context, startDate, endDate string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT 1 FROM `+s.table("client_ip_range_window_dirty_ips")+` LIMIT 1`)).Scan(&one)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// no pending dirty hashes
	case err != nil:
		return false, err
	default:
		return false, nil
	}
	scopeID := startDate + ":" + endDate
	var lastSuccess sql.NullString
	err = s.db.QueryRowContext(ctx, s.bind(`SELECT last_success_at FROM `+s.table("stats_job_state")+`
		WHERE scope_type = ? AND scope_id = ? AND job_name = ? LIMIT 1`),
		rangeWindowScopeType, scopeID, rangeWindowJobName).Scan(&lastSuccess)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// fall through to the materialized-window fallback
	case err != nil:
		return false, err
	default:
		return lastSuccess.Valid && lastSuccess.String != "", nil
	}
	err = s.db.QueryRowContext(ctx, s.bind(`SELECT 1 FROM `+s.table("client_ip_usage_range_windows")+`
		WHERE start_date = ? AND end_date = ? LIMIT 1`), startDate, endDate).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

var rangeStatsColumns = `range_stats.request_count, range_stats.success_count, range_stats.error_count,
	range_stats.input_tokens, range_stats.output_tokens, range_stats.cache_read_tokens,
	range_stats.cache_read_cost_usd, range_stats.cache_write_tokens, range_stats.cache_write_1h_tokens,
	range_stats.cache_write_cost_usd, range_stats.thinking_tokens, range_stats.input_image_tokens,
	range_stats.output_image_tokens, range_stats.total_cost_usd,
	range_stats.duration_ms_sum, range_stats.duration_ms_count, range_stats.duration_ms_max,
	range_stats.average_duration_ms,
	range_stats.first_token_ms_sum, range_stats.first_token_ms_count,
	range_stats.average_first_token_ms,
	range_stats.active_days, range_stats.last_used_at, range_stats.last_error_at`

func (s *Store) queryListRows(ctx context.Context, startDate, endDate, where string, whereArgs []any, options ListOptions) ([]ListRow, error) {
	query := s.bind(`SELECT
		registry.ip_hash, registry.aggregate_ip_key, registry.last_seen_at AS registry_last_seen_at,
		` + rangeStatsColumns + `
	FROM ` + s.table("client_ip_registry") + ` registry
	LEFT JOIN ` + s.table("client_ip_usage_range_windows") + ` range_stats
	  ON range_stats.ip_hash = registry.ip_hash
	 AND range_stats.start_date = ?
	 AND range_stats.end_date = ?` + where + `
	ORDER BY ` + listOrderBy(options.SortField, options.SortOrder) + `
	LIMIT ?`)
	args := []any{startDate, endDate}
	args = append(args, whereArgs...)
	args = append(args, maxListWindowRows)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ListRow
	for rows.Next() {
		var (
			row              ListRow
			lastSeenAt       sql.NullString
			avgDuration      sql.NullFloat64
			avgFirstToken    sql.NullFloat64
			lastUsedAt       sql.NullString
			lastErrorAt      sql.NullString
			requestCount     sql.NullInt64
			successCount     sql.NullInt64
			errorCount       sql.NullInt64
			inputTokens      sql.NullInt64
			outputTokens     sql.NullInt64
			cacheReadTokens  sql.NullInt64
			cacheReadCost    sql.NullFloat64
			cacheWriteTokens sql.NullInt64
			cacheWrite1h     sql.NullInt64
			cacheWriteCost   sql.NullFloat64
			thinkingTokens   sql.NullInt64
			inputImageTok    sql.NullInt64
			outputImageTok   sql.NullInt64
			totalCost        sql.NullFloat64
			durationSum      sql.NullInt64
			durationCount    sql.NullInt64
			durationMax      sql.NullInt64
			firstTokenSum    sql.NullInt64
			firstTokenCount  sql.NullInt64
			activeDays       sql.NullInt64
		)
		if err := rows.Scan(&row.IPHash, &row.AggregateIPKey, &lastSeenAt,
			&requestCount, &successCount, &errorCount,
			&inputTokens, &outputTokens, &cacheReadTokens,
			&cacheReadCost, &cacheWriteTokens, &cacheWrite1h,
			&cacheWriteCost, &thinkingTokens, &inputImageTok,
			&outputImageTok, &totalCost,
			&durationSum, &durationCount, &durationMax,
			&avgDuration,
			&firstTokenSum, &firstTokenCount,
			&avgFirstToken,
			&activeDays, &lastUsedAt, &lastErrorAt); err != nil {
			return nil, err
		}
		row.LastSeenAt = nullText(lastSeenAt)
		row.RangeUsage = usageSummary(usageSummaryRow{
			requestCount: requestCount, successCount: successCount, errorCount: errorCount,
			inputTokens: inputTokens, outputTokens: outputTokens,
			cacheReadTokens: cacheReadTokens, cacheReadCost: cacheReadCost,
			cacheWriteTokens: cacheWriteTokens, cacheWrite1h: cacheWrite1h, cacheWriteCost: cacheWriteCost,
			thinkingTokens: thinkingTokens, inputImageTok: inputImageTok, outputImageTok: outputImageTok,
			totalCost: totalCost, durationSum: durationSum, durationCount: durationCount,
			durationMax: durationMax, avgDuration: avgDuration,
			firstTokenSum: firstTokenSum, firstTokenCount: firstTokenCount, avgFirstToken: avgFirstToken,
			activeDays: activeDays, lastUsedAt: lastUsedAt, lastErrorAt: lastErrorAt,
		})
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if out == nil {
		out = []ListRow{}
	}
	return out, nil
}

type usageSummaryRow struct {
	requestCount, successCount, errorCount        sql.NullInt64
	inputTokens, outputTokens                     sql.NullInt64
	cacheReadTokens                               sql.NullInt64
	cacheReadCost                                 sql.NullFloat64
	cacheWriteTokens, cacheWrite1h                sql.NullInt64
	cacheWriteCost                                sql.NullFloat64
	thinkingTokens, inputImageTok, outputImageTok sql.NullInt64
	totalCost                                     sql.NullFloat64
	durationSum, durationCount, durationMax       sql.NullInt64
	avgDuration                                   sql.NullFloat64
	firstTokenSum, firstTokenCount                sql.NullInt64
	avgFirstToken                                 sql.NullFloat64
	activeDays                                    sql.NullInt64
	lastUsedAt, lastErrorAt                       sql.NullString
}

// usageSummary mirrors usageSummaryFromRow (including the average fallback
// from sum/count and the undefined-when-empty optionals).
func usageSummary(row usageSummaryRow) UsageSummary {
	requestCount := row.requestCount.Int64
	successCount := row.successCount.Int64
	errorCount := row.errorCount.Int64
	inputTokens := row.inputTokens.Int64
	outputTokens := row.outputTokens.Int64
	durationCount := row.durationCount.Int64
	firstTokenCount := row.firstTokenCount.Int64
	summary := UsageSummary{
		RequestCount:       requestCount,
		SuccessCount:       successCount,
		ErrorCount:         errorCount,
		InputTokens:        inputTokens,
		OutputTokens:       outputTokens,
		CacheReadTokens:    row.cacheReadTokens.Int64,
		CacheReadCost:      row.cacheReadCost.Float64,
		CacheWriteTokens:   row.cacheWriteTokens.Int64,
		CacheWrite1hTokens: row.cacheWrite1h.Int64,
		CacheWriteCost:     row.cacheWriteCost.Float64,
		ThinkingTokens:     row.thinkingTokens.Int64,
		InputImageTokens:   row.inputImageTok.Int64,
		OutputImageTokens:  row.outputImageTok.Int64,
		TotalTokens:        inputTokens + outputTokens,
		TotalCost:          row.totalCost.Float64,
		ActiveDays:         row.activeDays.Int64,
	}
	if requestCount > 0 {
		summary.ErrorRate = float64(errorCount) / float64(requestCount)
	}
	if row.avgDuration.Valid {
		value := row.avgDuration.Float64
		summary.AverageDurationMs = &value
	} else if durationCount > 0 {
		value := float64(row.durationSum.Int64) / float64(durationCount)
		summary.AverageDurationMs = &value
	}
	if row.avgFirstToken.Valid {
		value := row.avgFirstToken.Float64
		summary.AverageFirstTokenMs = &value
	} else if firstTokenCount > 0 {
		value := float64(row.firstTokenSum.Int64) / float64(firstTokenCount)
		summary.AverageFirstTokenMs = &value
	}
	if durationCount > 0 && row.durationMax.Int64 > 0 {
		value := row.durationMax.Int64
		summary.MaxDurationMs = &value
	}
	summary.LastUsedAt = nullText(row.lastUsedAt)
	summary.LastErrorAt = nullText(row.lastErrorAt)
	return summary
}

// buildRangeWhere mirrors buildClientIpRangeWhere + activePolicyExistsSql.
func (s *Store) buildRangeWhere(ctx context.Context, options ListOptions, policyNow string) (string, []any, error) {
	var clauses []string
	var args []any
	if keyword := strings.TrimSpace(options.Keyword); keyword != "" {
		upperBound := keywordPrefixUpperBound(keyword)
		clauses = append(clauses, `((registry.aggregate_ip_key >= ? AND registry.aggregate_ip_key < ?) OR (registry.client_ip >= ? AND registry.client_ip < ?))`)
		args = append(args, keyword, upperBound, keyword, upperBound)
	}
	status := options.Status
	if status == "" {
		status = StatusAll
	}
	expiresAfterNow := `unixepoch(active_policies.expires_at) > unixepoch(?)`
	if s.pg {
		expiresAfterNow = `EXTRACT(EPOCH FROM active_policies.expires_at::timestamptz) > EXTRACT(EPOCH FROM ?::timestamptz)`
	}
	activePolicyExists := func(ipHashExpression, policyType string) string {
		return `EXISTS (
		SELECT 1
		FROM ` + s.table("client_ip_policies") + ` active_policies
		WHERE active_policies.status = 'active'
		  AND active_policies.policy_type = '` + policyType + `'
		  AND active_policies.ip_hash = ` + ipHashExpression + `
		  AND (active_policies.expires_at IS NULL OR ` + expiresAfterNow + `)
		LIMIT 1
	)`
	}
	switch status {
	case StatusBlacklisted:
		clauses = append(clauses, activePolicyExists("registry.ip_hash", PolicyTypeBlacklist))
		args = append(args, policyNow)
	case StatusAllowlisted:
		clauses = append(clauses, activePolicyExists("registry.ip_hash", PolicyTypeAllowlist))
		args = append(args, policyNow)
	case StatusNormal:
		clauses = append(clauses, `NOT `+activePolicyExists("registry.ip_hash", PolicyTypeBlacklist))
		clauses = append(clauses, `NOT `+activePolicyExists("registry.ip_hash", PolicyTypeAllowlist))
		args = append(args, policyNow, policyNow)
	case StatusAll:
	default:
		return "", nil, &ValidationError{Message: "IP 统计参数无效"}
	}
	if len(clauses) == 0 {
		return "", args, nil
	}
	return ` WHERE ` + strings.Join(clauses, ` AND `), args, nil
}

// activePolicySets mirrors listActiveClientIpPolicies + activeClientIpPolicySets.
type policySets struct {
	blacklist map[string]bool
	allowlist map[string]bool
}

func (s *Store) activePolicySets(ctx context.Context, policyNow string) (policySets, error) {
	sets := policySets{blacklist: map[string]bool{}, allowlist: map[string]bool{}}
	nowMs := s.now().UnixMilli()
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT policies.ip_hash, policies.policy_type, policies.expires_at
		FROM `+s.table("client_ip_policies")+` policies
		INNER JOIN `+s.table("client_ip_registry")+` registry ON registry.ip_hash = policies.ip_hash
		WHERE policies.status = 'active'`))
	if err != nil {
		return sets, err
	}
	defer rows.Close()
	for rows.Next() {
		var ipHash, policyType string
		var expiresAt sql.NullString
		if err := rows.Scan(&ipHash, &policyType, &expiresAt); err != nil {
			return sets, err
		}
		if expiresAt.Valid {
			expiresMs, err := rfc3339Millis(expiresAt.String)
			if err != nil {
				return sets, errors.New("Client-IP 策略 expiresAt必须是带 Z 或数值 offset 的 RFC3339 时间")
			}
			if expiresMs <= nowMs {
				continue
			}
		}
		if normalizePolicyType(policyType) == PolicyTypeAllowlist {
			sets.allowlist[ipHash] = true
		} else {
			sets.blacklist[ipHash] = true
		}
	}
	if err := rows.Err(); err != nil {
		return sets, err
	}
	return sets, nil
}

// labelRowStatus mirrors mapClientIpStatsRangeRow: blacklist wins over
// allowlist when both are somehow active.
func labelRowStatus(row *ListRow, sets policySets) {
	switch {
	case sets.blacklist[row.IPHash]:
		row.Status = StatusBlacklisted
	case sets.allowlist[row.IPHash]:
		row.Status = StatusAllowlisted
	default:
		row.Status = StatusNormal
	}
}

// listOrderBy mirrors clientIpStatsOrderBy (static whitelist, stable ip_hash
// tiebreak, lastUsedAt delegated to the in-memory sort).
func listOrderBy(field, order string) string {
	direction := "DESC"
	if order == "asc" {
		direction = "ASC"
	}
	switch field {
	case "successCount":
		return `COALESCE(range_stats.success_count, 0) ` + direction + `, registry.ip_hash ASC`
	case "errorCount":
		return `COALESCE(range_stats.error_count, 0) ` + direction + `, registry.ip_hash ASC`
	case "errorRate":
		return `CASE WHEN COALESCE(range_stats.request_count, 0) > 0 THEN CAST(range_stats.error_count AS REAL) / range_stats.request_count ELSE 0 END ` + direction + `, registry.ip_hash ASC`
	case "totalTokens":
		return `(COALESCE(range_stats.input_tokens, 0) + COALESCE(range_stats.output_tokens, 0)) ` + direction + `, registry.ip_hash ASC`
	case "activeDays":
		return `COALESCE(range_stats.active_days, 0) ` + direction + `, registry.ip_hash ASC`
	case "lastUsedAt":
		return `registry.ip_hash ASC`
	case "requestCount":
		return `COALESCE(range_stats.request_count, 0) ` + direction + `, registry.ip_hash ASC`
	case "totalCost":
		return `COALESCE(range_stats.total_cost_usd, 0) ` + direction + `, registry.ip_hash ASC`
	default:
		return `COALESCE(range_stats.request_count, 0) DESC, registry.ip_hash ASC`
	}
}

// sortRows mirrors sortClientIpStatsRows: only lastUsedAt reorders rows in
// memory, using the registry lastSeenAt for the global scope (the list route
// pins lastUsedSortScope=global). Rows without a timestamp lead ascending and
// trail descending; ties order ipHash descending on asc, ascending on desc.
func sortRows(rows []ListRow, field, order, lastUsedSortScope string) ([]ListRow, error) {
	if field != "lastUsedAt" {
		return rows, nil
	}
	direction := -1
	if order == "asc" {
		direction = 1
	}
	global := lastUsedSortScope == "global"
	tieDirection := 1
	if global && direction == 1 {
		tieDirection = -1
	}
	type entry struct {
		row      ListRow
		millis   int64
		hasValue bool
	}
	entries := make([]entry, 0, len(rows))
	for _, row := range rows {
		item := entry{row: row}
		value := row.RangeUsage.LastUsedAt
		if global {
			value = row.LastSeenAt
		}
		if value != nil {
			millis, err := rfc3339Millis(*value)
			if err != nil {
				return nil, errors.New("客户端 IP lastUsedAt必须是带 Z 或数值 offset 的 RFC3339 时间")
			}
			item.millis, item.hasValue = millis, true
		}
		entries = append(entries, item)
	}
	sort.SliceStable(entries, func(left, right int) bool {
		a, b := entries[left], entries[right]
		if a.millis != b.millis || a.hasValue != b.hasValue {
			if !a.hasValue {
				return direction == 1
			}
			if !b.hasValue {
				return direction != 1
			}
			if a.millis < b.millis {
				return direction == 1
			}
			return direction != 1
		}
		if a.row.IPHash == b.row.IPHash {
			return false
		}
		less := a.row.IPHash < b.row.IPHash
		if tieDirection == -1 {
			return !less
		}
		return less
	})
	for index := range entries {
		rows[index] = entries[index].row
	}
	return rows, nil
}

// keywordPrefixUpperBound mirrors clientIpKeywordPrefixUpperBound: increment
// the last UTF-16 code unit below 0xffff.
func keywordPrefixUpperBound(value string) string {
	units := utf16.Encode([]rune(value))
	for index := len(units) - 1; index >= 0; index-- {
		if units[index] < 0xffff {
			units[index]++
			return string(utf16.Decode(units))
		}
	}
	return value + "\uffff"
}

// rfc3339Millis mirrors rfc3339InstantMilliseconds (Z or numeric offset).
func rfc3339Millis(value string) (int64, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return 0, err
	}
	return parsed.UnixMilli(), nil
}

type lastUsedEpochWindow struct {
	startMs        int64
	endExclusiveMs int64
}

// normalizeRange mirrors normalizeAccountUsageStatsRange: missing/invalid
// dates fall back to the configured-today key, future dates clamp to today,
// the window clamps to the most recent maxRangeDays days.
func normalizeRange(startRaw, endRaw string, now time.Time, location *time.Location) Range {
	todayKey := now.In(location).Format("2006-01-02")
	end := normalizeDateKey(endRaw, todayKey)
	if end > todayKey {
		end = todayKey
	}
	start := normalizeDateKey(startRaw, todayKey)
	if start > todayKey {
		start = todayKey
	}
	if start > end {
		start = end
	}
	endTime := parseDateKeyOrToday(end, todayKey)
	earliest := endTime.AddDate(0, 0, -(maxRangeDays - 1)).Format("2006-01-02")
	if start < earliest {
		start = earliest
	}
	if end < earliest {
		end = earliest
	}
	return Range{
		StartDate: start,
		EndDate:   end,
		Days:      daysBetweenInclusive(start, end, todayKey),
		MaxDays:   maxRangeDays,
	}
}

func normalizeDateKey(value, fallback string) string {
	text := strings.TrimSpace(value)
	if text == "" {
		return fallback
	}
	if _, err := time.Parse("2006-01-02", text); err != nil {
		return fallback
	}
	return text
}

func parseDateKeyOrToday(value, fallback string) time.Time {
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		parsed, _ = time.Parse("2006-01-02", fallback)
	}
	return parsed
}

func daysBetweenInclusive(start, end, fallback string) int {
	startTime := parseDateKeyOrToday(start, fallback)
	endTime := parseDateKeyOrToday(end, fallback)
	days := int(endTime.Sub(startTime).Hours()/24) + 1
	if days < 1 {
		return 1
	}
	return days
}

// newLastUsedEpochWindow mirrors clientIpLastUsedEpochWindow: configured-zone
// [start-day, end-day + 1) bounds.
func newLastUsedEpochWindow(searchRange Range, location *time.Location) *lastUsedEpochWindow {
	start := zonedDayStart(searchRange.StartDate, location)
	end := zonedDayStart(nextDateKey(searchRange.EndDate), location)
	if start == nil || end == nil {
		return nil
	}
	return &lastUsedEpochWindow{startMs: start.UnixMilli(), endExclusiveMs: end.UnixMilli()}
}

func zonedDayStart(dateKey string, location *time.Location) *time.Time {
	parsed, err := time.Parse("2006-01-02", dateKey)
	if err != nil {
		return nil
	}
	start := time.Date(parsed.Year(), parsed.Month(), parsed.Day(), 0, 0, 0, 0, location)
	return &start
}

func nextDateKey(value string) string {
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		return value
	}
	return parsed.AddDate(0, 0, 1).Format("2006-01-02")
}

// rowMatchesLastUsedWindow mirrors clientIpStatsRowMatchesLastUsedWindow:
// rows without registry lastSeenAt drop out of a lastUsed-filtered page.
func rowMatchesLastUsedWindow(row ListRow, window *lastUsedEpochWindow) bool {
	if window == nil {
		return true
	}
	if row.LastSeenAt == nil {
		return false
	}
	lastSeenMs, err := rfc3339Millis(*row.LastSeenAt)
	if err != nil {
		return false
	}
	return lastSeenMs >= window.startMs && lastSeenMs < window.endExclusiveMs
}

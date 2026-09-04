// detail.go ports client-ip-stats-detail.repository.ts: the per-IP account
// usage detail read behind GET /:ipHash/detail. The window rows come from
// client_ip_account_usage_range_windows; account and owner display names come
// from the business database through the DetailAccountLookup port (Node sync
// branch reads the business handle, the PostgreSQL branch cross-queries
// juhe_business on the same pool — both render through the same mapper).
package ipstats

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// AccountLookup mirrors BusinessResourceLookup for the detail items.
type AccountLookup struct {
	ID              string
	Name            string
	SystemAccountID string
}

// DetailAccountLookup is the business-database read port for account names
// and owner display names. A nil lookup (composition root not wired yet)
// degrades to nameless rows instead of failing the read.
type DetailAccountLookup interface {
	LookupAccounts(ctx context.Context, accountIDs []string) (map[string]AccountLookup, error)
	SystemAccountNames(ctx context.Context, systemAccountIDs []string) (map[string]string, error)
}

// NewBusinessAccountLookup builds the default lookup. businessDB is the
// business database handle for SQLite; for PostgreSQL the stats connection
// already reaches juhe_business through schema qualification, so businessDB
// may be nil.
func NewBusinessAccountLookup(businessDB *sql.DB, postgres bool) DetailAccountLookup {
	return &businessAccountLookup{business: businessDB, pg: postgres}
}

type businessAccountLookup struct {
	business *sql.DB
	pg       bool
}

func (l *businessAccountLookup) db() *sql.DB {
	if l.pg {
		if l.business != nil {
			return l.business
		}
		return nil
	}
	return l.business
}

// table qualifies business-schema tables the way Node resolves them: the
// PostgreSQL detail client cross-queries juhe_business.*, the SQLite handle
// uses the unqualified business tables.
func (l *businessAccountLookup) table(name string) string {
	if l.pg {
		return "juhe_business." + name
	}
	return name
}

func (l *businessAccountLookup) bind(query string) string {
	if !l.pg {
		return query
	}
	return pgBind(query)
}

func pgBind(query string) string {
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

func (l *businessAccountLookup) LookupAccounts(ctx context.Context, accountIDs []string) (map[string]AccountLookup, error) {
	ctx = ensureCtx(ctx)
	unique := uniqueStrings(accountIDs)
	out := make(map[string]AccountLookup, len(unique))
	handle := l.db()
	if handle == nil || len(unique) == 0 {
		return out, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(unique)), ",")
	query := l.bind(`SELECT id, name, system_account_id FROM ` + l.table("accounts") + ` WHERE id IN (` + placeholders + `)`)
	args := make([]any, 0, len(unique))
	for _, id := range unique {
		args = append(args, id)
	}
	rows, err := handle.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var lookup AccountLookup
		if err := rows.Scan(&lookup.ID, &lookup.Name, &lookup.SystemAccountID); err != nil {
			return nil, err
		}
		out[lookup.ID] = lookup
	}
	return out, rows.Err()
}

func (l *businessAccountLookup) SystemAccountNames(ctx context.Context, systemAccountIDs []string) (map[string]string, error) {
	ctx = ensureCtx(ctx)
	unique := uniqueStrings(systemAccountIDs)
	out := make(map[string]string, len(unique))
	handle := l.db()
	if handle == nil || len(unique) == 0 {
		return out, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(unique)), ",")
	query := l.bind(`SELECT id, display_name FROM ` + l.table("system_accounts") + ` WHERE id IN (` + placeholders + `)`)
	args := make([]any, 0, len(unique))
	for _, id := range unique {
		args = append(args, id)
	}
	rows, err := handle.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, displayName string
		if err := rows.Scan(&id, &displayName); err != nil {
			return nil, err
		}
		out[id] = displayName
	}
	return out, rows.Err()
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

// DetailOptions mirrors ClientIpStatsDetailOptions after the route-level zod
// validation; Store clamps page/pageSize like boundedDetailPage/PageSize.
type DetailOptions struct {
	IPHash    string
	Page      int
	PageSize  int
	StartDate string
	EndDate   string
	SortField string
	SortOrder string
}

// AccountUsageDetailItem mirrors ClientIpAccountUsageRow.
type AccountUsageDetailItem struct {
	AccountID                     string       `json:"accountId"`
	AccountName                   *string      `json:"accountName,omitempty"`
	AccountOwnerSystemAccountID   *string      `json:"accountOwnerSystemAccountId,omitempty"`
	AccountOwnerSystemAccountName *string      `json:"accountOwnerSystemAccountName,omitempty"`
	RangeUsage                    UsageSummary `json:"rangeUsage"`
}

// DetailResult mirrors ClientIpStatsDetailResult.
type DetailResult struct {
	IPHash         string                   `json:"ipHash"`
	AggregateIPKey string                   `json:"aggregateIpKey"`
	LastSeenAt     *string                  `json:"lastSeenAt,omitempty"`
	Items          []AccountUsageDetailItem `json:"items"`
	PageUpperBound int                      `json:"pageUpperBound"`
	HasMore        bool                     `json:"hasMore"`
	Page           int                      `json:"page"`
	PageSize       int                      `json:"pageSize"`
	Range          Range                    `json:"range"`
	RangeReady     bool                     `json:"rangeReady"`
}

// maxDetailWindowRows mirrors clientIpStatsDetailMaxWindowRows.
const maxDetailWindowRows = 1001

// SetDetailAccountLookup wires the business-database lookup; nil keeps the
// nameless degradation.
func (s *Store) SetDetailAccountLookup(lookup DetailAccountLookup) {
	s.detailAccounts = lookup
}

// Detail mirrors getClientIpStatsDetailAsync. A nil result means the IP hash
// failed normalization or the registry row is missing (Node returns
// undefined → 404 IP 不存在).
func (s *Store) Detail(ctx context.Context, options DetailOptions) (*DetailResult, error) {
	ctx = ensureCtx(ctx)
	ipHash, err := NormalizeIPHash(options.IPHash)
	if err != nil {
		return nil, nil
	}
	var (
		aggregateIPKey string
		lastSeenAt     sql.NullString
	)
	err = s.db.QueryRowContext(ctx, s.bind(`SELECT ip_hash, aggregate_ip_key, last_seen_at
		FROM `+s.table("client_ip_registry")+` WHERE ip_hash = ? LIMIT 1`), ipHash).Scan(&ipHash, &aggregateIPKey, &lastSeenAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	timezoneName, err := s.tz(ctx)
	if err != nil {
		return nil, err
	}
	location, err := time.LoadLocation(timezoneName)
	if err != nil {
		return nil, errors.New("系统设置 usageStatsTimezone 无效：" + timezoneName)
	}
	searchRange := normalizeRange(options.StartDate, options.EndDate, s.now(), location)
	pageSize := boundedDetailPageSize(options.PageSize)
	page := boundedDetailPage(options.Page, pageSize)
	ready, err := s.rangeReady(ctx, searchRange.StartDate, searchRange.EndDate)
	if err != nil {
		return nil, err
	}
	if !ready {
		return &DetailResult{
			IPHash:         ipHash,
			AggregateIPKey: aggregateIPKey,
			LastSeenAt:     nullText(lastSeenAt),
			Items:          []AccountUsageDetailItem{},
			PageUpperBound: 0,
			HasMore:        false,
			Page:           page,
			PageSize:       pageSize,
			Range:          searchRange,
			RangeReady:     false,
		}, nil
	}

	offset := (page - 1) * pageSize
	query := s.bind(`SELECT
		account_id,
		request_count, success_count, error_count,
		input_tokens, output_tokens, cache_read_tokens,
		cache_read_cost_usd, cache_write_tokens, cache_write_1h_tokens,
		cache_write_cost_usd, thinking_tokens, input_image_tokens,
		output_image_tokens, total_cost_usd,
		duration_ms_sum, duration_ms_count, duration_ms_max,
		average_duration_ms,
		first_token_ms_sum, first_token_ms_count,
		average_first_token_ms,
		active_days, last_used_at, last_error_at
	FROM ` + s.table("client_ip_account_usage_range_windows") + `
	WHERE ip_hash = ?
		AND start_date = ?
		AND end_date = ?
	ORDER BY ` + detailAccountStatsOrderBy(options.SortField, options.SortOrder) + `
	LIMIT ? OFFSET ?`)
	rows, err := s.db.QueryContext(ctx, query, ipHash, searchRange.StartDate, searchRange.EndDate, pageSize+1, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	windowRows := []detailWindowRow{}
	for rows.Next() {
		var row detailWindowRow
		if err := rows.Scan(&row.accountID, &row.requestCount, &row.successCount, &row.errorCount,
			&row.inputTokens, &row.outputTokens, &row.cacheReadTokens,
			&row.cacheReadCost, &row.cacheWriteTokens, &row.cacheWrite1hTokens,
			&row.cacheWriteCost, &row.thinkingTokens, &row.inputImageTokens,
			&row.outputImageTokens, &row.totalCost,
			&row.durationSum, &row.durationCount, &row.durationMax,
			&row.avgDuration,
			&row.firstTokenSum, &row.firstTokenCount,
			&row.avgFirstToken,
			&row.activeDays, &row.lastUsedAt, &row.lastErrorAt); err != nil {
			return nil, err
		}
		windowRows = append(windowRows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	hasMore := len(windowRows) > pageSize
	if hasMore {
		windowRows = windowRows[:pageSize]
	}

	items := make([]AccountUsageDetailItem, 0, len(windowRows))
	var lookups map[string]AccountLookup
	var ownerNames map[string]string
	if s.detailAccounts != nil && len(windowRows) > 0 {
		ids := make([]string, 0, len(windowRows))
		for _, row := range windowRows {
			ids = append(ids, row.accountID)
		}
		lookups, err = s.detailAccounts.LookupAccounts(ctx, ids)
		if err != nil {
			return nil, err
		}
		ownerIDs := make([]string, 0, len(lookups))
		for _, lookup := range lookups {
			ownerIDs = append(ownerIDs, lookup.SystemAccountID)
		}
		ownerNames, err = s.detailAccounts.SystemAccountNames(ctx, ownerIDs)
		if err != nil {
			return nil, err
		}
	}
	for _, row := range windowRows {
		item := AccountUsageDetailItem{
			AccountID:  row.accountID,
			RangeUsage: detailUsageSummary(row),
		}
		if lookup, ok := lookups[row.accountID]; ok {
			name := lookup.Name
			owner := lookup.SystemAccountID
			item.AccountName = &name
			item.AccountOwnerSystemAccountID = &owner
			if ownerName, ok := ownerNames[lookup.SystemAccountID]; ok {
				item.AccountOwnerSystemAccountName = &ownerName
			}
		}
		items = append(items, item)
	}

	return &DetailResult{
		IPHash:         ipHash,
		AggregateIPKey: aggregateIPKey,
		LastSeenAt:     nullText(lastSeenAt),
		Items:          items,
		PageUpperBound: (page-1)*pageSize + len(items) + boolToInt(hasMore),
		HasMore:        hasMore,
		Page:           page,
		PageSize:       pageSize,
		Range:          searchRange,
		RangeReady:     true,
	}, nil
}

type detailWindowRow struct {
	accountID                                           string
	requestCount                                        sql.NullInt64
	successCount                                        sql.NullInt64
	errorCount                                          sql.NullInt64
	inputTokens                                         sql.NullInt64
	outputTokens                                        sql.NullInt64
	cacheReadTokens                                     sql.NullInt64
	cacheReadCost                                       sql.NullFloat64
	cacheWriteTokens, cacheWrite1hTokens                sql.NullInt64
	cacheWriteCost                                      sql.NullFloat64
	thinkingTokens, inputImageTokens, outputImageTokens sql.NullInt64
	totalCost                                           sql.NullFloat64
	durationSum, durationCount, durationMax             sql.NullInt64
	avgDuration                                         sql.NullFloat64
	firstTokenSum, firstTokenCount                      sql.NullInt64
	avgFirstToken                                       sql.NullFloat64
	activeDays                                          sql.NullInt64
	lastUsedAt, lastErrorAt                             sql.NullString
}

// detailAccountStatsOrderBy mirrors clientIpAccountStatsOrderBy: static
// whitelist with an inverted account_id tiebreak.
func detailAccountStatsOrderBy(field, order string) string {
	direction := "DESC"
	if order == "asc" {
		direction = "ASC"
	}
	tieDirection := "ASC"
	if direction == "ASC" {
		tieDirection = "DESC"
	}
	switch field {
	case "successCount":
		return `success_count ` + direction + `, account_id ` + tieDirection
	case "errorCount":
		return `error_count ` + direction + `, account_id ` + tieDirection
	case "errorRate":
		return `CASE WHEN request_count > 0 THEN CAST(error_count AS REAL) / request_count ELSE 0 END ` + direction + `, account_id ` + tieDirection
	case "totalTokens":
		return `(input_tokens + output_tokens) ` + direction + `, account_id ` + tieDirection
	case "activeDays":
		return `active_days ` + direction + `, account_id ` + tieDirection
	case "lastUsedAt":
		return `last_used_at ` + direction + `, account_id ` + tieDirection
	case "totalCost":
		return `total_cost_usd ` + direction + `, account_id ` + tieDirection
	case "requestCount":
		return `request_count ` + direction + `, account_id ` + tieDirection
	default:
		return `request_count ` + direction + `, account_id ` + tieDirection
	}
}

// detailUsageSummary mirrors usageSummaryFromRow in the detail repository
// (zero-filled numerics, sum/count average fallback, max duration guard).
func detailUsageSummary(row detailWindowRow) UsageSummary {
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
		CacheWrite1hTokens: row.cacheWrite1hTokens.Int64,
		CacheWriteCost:     row.cacheWriteCost.Float64,
		ThinkingTokens:     row.thinkingTokens.Int64,
		InputImageTokens:   row.inputImageTokens.Int64,
		OutputImageTokens:  row.outputImageTokens.Int64,
		TotalTokens:        inputTokens + outputTokens,
		TotalCost:          row.totalCost.Float64,
		ActiveDays:         row.activeDays.Int64,
	}
	if requestCount > 0 {
		summary.ErrorRate = float64(errorCount) / float64(requestCount)
	}
	if row.avgDuration.Valid {
		summary.AverageDurationMs = &row.avgDuration.Float64
	} else if durationCount > 0 {
		value := float64(row.durationSum.Int64) / float64(durationCount)
		summary.AverageDurationMs = &value
	}
	if row.avgFirstToken.Valid {
		summary.AverageFirstTokenMs = &row.avgFirstToken.Float64
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

// boundedDetailPage mirrors boundedDetailPage: clamp into the 1000-row
// detail window.
func boundedDetailPage(page, pageSize int) int {
	maxPage := (maxDetailWindowRows - 1) / pageSize
	if maxPage < 1 {
		maxPage = 1
	}
	if page > maxPage {
		return maxPage
	}
	if page < 1 {
		return 1
	}
	return page
}

// boundedDetailPageSize mirrors boundedDetailPageSize: 1..100, default 20.
func boundedDetailPageSize(pageSize int) int {
	if pageSize < 1 {
		return 20
	}
	if pageSize > 100 {
		return 100
	}
	return pageSize
}

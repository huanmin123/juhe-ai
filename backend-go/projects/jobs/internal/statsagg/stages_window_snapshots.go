package statsagg

import (
	"context"
	"database/sql"
	"sort"
	"time"
)

// usageOverviewScope mirrors usageOverviewSnapshotScopes 的行形态。
type usageOverviewScope struct {
	systemAccountID string
	scopeID         string
}

const maxUsageOverviewSnapshotScopes = 5000

// usageOverviewSnapshotScopes mirrors usageOverviewSnapshotScopes：
// usage_stats_totals 中 system_account scope 的 (system, scope) 列表，
// 末尾补 global（若缺）。
func (w *WindowRefresher) usageOverviewSnapshotScopes(ctx context.Context, tx *sql.Tx) ([]usageOverviewScope, error) {
	query := w.Dialect.bind(`
		SELECT system_account_id, scope_id
		FROM ` + w.Dialect.StatsTable("usage_stats_totals") + `
		WHERE scope_type = 'system_account'
		ORDER BY updated_at DESC, system_account_id ASC, scope_id ASC
		LIMIT ?
	`)
	rows, err := tx.QueryContext(ctx, query, maxUsageOverviewSnapshotScopes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	scopes := []usageOverviewScope{}
	for rows.Next() {
		var systemAccountID, scopeID sql.NullString
		if err := rows.Scan(&systemAccountID, &scopeID); err != nil {
			return nil, err
		}
		if systemAccountID.String == "" || scopeID.String == "" {
			continue
		}
		scopes = append(scopes, usageOverviewScope{systemAccountID.String, scopeID.String})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	hasGlobal := false
	for _, scope := range scopes {
		if scope.systemAccountID == GlobalStatsSystemAccountID && scope.scopeID == GlobalStatsScopeID {
			hasGlobal = true
		}
	}
	if !hasGlobal {
		scopes = append(scopes, usageOverviewScope{GlobalStatsSystemAccountID, GlobalStatsScopeID})
	}
	return scopes, nil
}

// refreshUsageOverviewWindowSnapshots mirrors refreshUsageOverviewWindowSnapshots
// （SQLite 全量重建路径；PG 增量 dirty 队列是等价优化，最终行集一致，
// 见包注释「行为差异」）。
func (w *WindowRefresher) refreshUsageOverviewWindowSnapshots(ctx context.Context, tx *sql.Tx, stageContext refreshStageContext) error {
	scopes, err := w.usageOverviewSnapshotScopes(ctx, tx)
	if err != nil {
		return err
	}
	ranges := FixedUsageStatsRanges(stageContext.todayKey)
	earliestDate := stageContext.todayKey
	if len(ranges) > 0 {
		earliestDate = ranges[0].StartDate
	}
	uniqueSystemAccountIDs := map[string]struct{}{}
	for _, scope := range scopes {
		if scope.systemAccountID != GlobalStatsSystemAccountID {
			uniqueSystemAccountIDs[scope.systemAccountID] = struct{}{}
		}
	}
	for _, tableName := range []string{"usage_overview_summary_windows", "usage_overview_trend_windows", "usage_model_rank_windows", "usage_error_rank_windows"} {
		if _, err := tx.ExecContext(ctx, w.Dialect.bind(`DELETE FROM `+w.Dialect.StatsTable(tableName))); err != nil {
			return err
		}
	}
	// summary：scope × range 汇总 usage_stats_daily
	for _, scope := range scopes {
		rows, err := w.loadDailyWindowRows(ctx, tx, scope.systemAccountID, scope.scopeID, earliestDate, stageContext.todayKey)
		if err != nil {
			return err
		}
		rowsByDate := RowsByStatDate(rows, func(r DailyWindowRow) string { return r.StatDate })
		for _, rangeValue := range ranges {
			aggregate := AggregateUsageRowsForRange(rowsByDate, rangeValue)
			query := w.Dialect.bind(`
				INSERT INTO ` + w.Dialect.StatsTable("usage_overview_summary_windows") + ` (
				  system_account_id, window_key, start_date, end_date, request_count, success_count, error_count,
				  input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd,
				  thinking_tokens, input_image_tokens, output_image_tokens, total_cost_usd,
				  duration_ms_sum, duration_ms_count, first_token_ms_sum, first_token_ms_count,
				  last_used_at, updated_at)
				VALUES (` + placeholders(24) + `)
			`)
			var lastUsedAt any
			if aggregate.LastUsedAt != "" {
				lastUsedAt = aggregate.LastUsedAt
			}
			if _, err := tx.ExecContext(ctx, query,
				scope.systemAccountID, RangeWindowKey(rangeValue), rangeValue.StartDate, rangeValue.EndDate,
				aggregate.RequestCount, aggregate.SuccessCount, aggregate.ErrorCount,
				aggregate.InputTokens, aggregate.OutputTokens, aggregate.CacheReadTokens, aggregate.CacheReadCostUsd,
				aggregate.CacheWriteTokens, aggregate.CacheWrite1hTokens, aggregate.CacheWriteCostUsd,
				aggregate.ThinkingTokens, aggregate.InputImageTokens, aggregate.OutputImageTokens, aggregate.TotalCostUsd,
				aggregate.DurationMsSum, aggregate.DurationMsCount, aggregate.FirstTokenMsSum, aggregate.FirstTokenMsCount,
				lastUsedAt, stageContext.updatedAt); err != nil {
				return err
			}
		}
	}
	// trend：scope × range 汇总 usage_stats_hourly 趋势桶
	for _, scope := range scopes {
		rows, err := w.loadHourlyWindowRows(ctx, tx, scope.systemAccountID, scope.scopeID, earliestDate, stageContext.todayKey)
		if err != nil {
			return err
		}
		for _, rangeValue := range ranges {
			buckets := AggregateUsageTrendBuckets(rows, rangeValue)
			for _, bucketKey := range SortedMapKeys(buckets) {
				bucket := buckets[bucketKey]
				query := w.Dialect.bind(`
					INSERT INTO ` + w.Dialect.StatsTable("usage_overview_trend_windows") + ` (
					  system_account_id, window_key, start_date, end_date, bucket_key, request_count, error_count,
					  input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd,
					  thinking_tokens, input_image_tokens, output_image_tokens, total_cost_usd,
					  duration_ms_sum, duration_ms_count, updated_at)
					VALUES (` + placeholders(21) + `)
				`)
				if _, err := tx.ExecContext(ctx, query,
					scope.systemAccountID, RangeWindowKey(rangeValue), rangeValue.StartDate, rangeValue.EndDate, bucketKey,
					bucket.RequestCount, bucket.ErrorCount,
					bucket.InputTokens, bucket.OutputTokens, bucket.CacheReadTokens, bucket.CacheReadCostUsd,
					bucket.CacheWriteTokens, bucket.CacheWrite1hTokens, bucket.CacheWriteCostUsd,
					bucket.ThinkingTokens, bucket.InputImageTokens, bucket.OutputImageTokens, bucket.TotalCostUsd,
					bucket.DurationMsSum, bucket.DurationMsCount, stageContext.updatedAt); err != nil {
					return err
				}
			}
		}
	}
	// model rank：uniqueSystemAccountIds + global × range
	systemAccountIDList := mapKeyList(uniqueSystemAccountIDs)
	for _, systemAccountID := range append(systemAccountIDList, GlobalStatsSystemAccountID) {
		if err := w.refreshUsageModelRankWindows(ctx, tx, systemAccountID, ranges, earliestDate, stageContext.todayKey, stageContext.updatedAt); err != nil {
			return err
		}
		if err := w.refreshUsageErrorRankWindows(ctx, tx, systemAccountID, ranges, earliestDate, stageContext.todayKey, stageContext.updatedAt); err != nil {
			return err
		}
	}
	return nil
}

func mapKeyList(set map[string]struct{}) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (w *WindowRefresher) loadDailyWindowRows(ctx context.Context, tx *sql.Tx, systemAccountID, scopeID, earliestDate, todayKey string) ([]DailyWindowRow, error) {
	query := w.Dialect.bind(`
		SELECT stat_date, request_count, success_count, error_count, input_tokens, output_tokens, cache_read_tokens,
		  cache_read_cost_usd, cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd,
		  thinking_tokens, input_image_tokens, output_image_tokens, total_cost_usd, duration_ms_sum, duration_ms_count, duration_ms_max,
		  first_token_ms_sum, first_token_ms_count, first_token_ms_max, last_used_at
		FROM ` + w.Dialect.StatsTable("usage_stats_daily") + `
		WHERE system_account_id = ? AND scope_type = 'system_account' AND scope_id = ?
		  AND stat_date >= ? AND stat_date <= ?
		ORDER BY stat_date ASC
	`)
	return scanDailyWindowRows(tx.QueryContext(ctx, query, systemAccountID, scopeID, earliestDate, todayKey))
}

func (w *WindowRefresher) loadHourlyWindowRows(ctx context.Context, tx *sql.Tx, systemAccountID, scopeID, earliestDate, todayKey string) ([]HourlyWindowRow, error) {
	query := w.Dialect.bind(`
		SELECT stat_hour, request_count, error_count, input_tokens, output_tokens, cache_read_tokens,
		  cache_read_cost_usd, cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd,
		  thinking_tokens, input_image_tokens, output_image_tokens, total_cost_usd, duration_ms_sum, duration_ms_count
		FROM ` + w.Dialect.StatsTable("usage_stats_hourly") + `
		WHERE system_account_id = ? AND scope_type = 'system_account' AND scope_id = ?
		  AND stat_hour >= ? AND stat_hour <= ?
		ORDER BY stat_hour ASC
	`)
	return scanHourlyWindowRows(tx.QueryContext(ctx, query, systemAccountID, scopeID, earliestDate+"T00", todayKey+"T23"))
}

func scanDailyWindowRows(rows *sql.Rows, err error) ([]DailyWindowRow, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []DailyWindowRow
	for rows.Next() {
		var row DailyWindowRow
		var lastUsedAt sql.NullString
		if err := rows.Scan(&row.StatDate, &row.RequestCount, &row.SuccessCount, &row.ErrorCount,
			&row.InputTokens, &row.OutputTokens, &row.CacheReadTokens, &row.CacheReadCostUsd,
			&row.CacheWriteTokens, &row.CacheWrite1hTokens, &row.CacheWriteCostUsd,
			&row.ThinkingTokens, &row.InputImageTokens, &row.OutputImageTokens, &row.TotalCostUsd,
			&row.DurationMsSum, &row.DurationMsCount, &row.DurationMsMax,
			&row.FirstTokenMsSum, &row.FirstTokenMsCount, &row.FirstTokenMsMax, &lastUsedAt); err != nil {
			return nil, err
		}
		row.LastUsedAt = lastUsedAt.String
		result = append(result, row)
	}
	return result, rows.Err()
}

func scanHourlyWindowRows(rows *sql.Rows, err error) ([]HourlyWindowRow, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []HourlyWindowRow
	for rows.Next() {
		var row HourlyWindowRow
		if err := rows.Scan(&row.StatHour, &row.RequestCount, &row.ErrorCount,
			&row.InputTokens, &row.OutputTokens, &row.CacheReadTokens, &row.CacheReadCostUsd,
			&row.CacheWriteTokens, &row.CacheWrite1hTokens, &row.CacheWriteCostUsd,
			&row.ThinkingTokens, &row.InputImageTokens, &row.OutputImageTokens, &row.TotalCostUsd,
			&row.DurationMsSum, &row.DurationMsCount); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// usageModelWindowRow / usageErrorWindowRow 对齐
// usage-stats-window-aggregates.ts 的 UsageModelWindowRow / UsageErrorWindowRow。
type usageModelWindowRow struct {
	StatDate           string
	ProviderCode       string
	Model              string
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
}

type usageErrorWindowRow struct {
	StatDate     string
	ErrorGroup   string
	ProviderCode string
	ErrorCode    string
	StatusCode   float64
	ErrorMessage string // '' 表示 NULL
	ErrorCount   float64
}

// usageModelWindowAggregate mirrors UsageModelWindowAggregate。
type usageModelWindowAggregate struct {
	ProviderCode       string
	Model              string
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
}

// aggregateUsageModelRows mirrors aggregateUsageModelRows（含排序）。
func aggregateUsageModelRows(rowsByDate map[string][]usageModelWindowRow, rangeValue StatsRange) []usageModelWindowAggregate {
	buckets := map[string]*usageModelWindowAggregate{}
	order := []string{}
	for _, row := range RowsForDateRange(rowsByDate, rangeValue.StartDate, rangeValue.EndDate) {
		providerCode := row.ProviderCode
		if providerCode == "" {
			providerCode = "unknown"
		}
		model := row.Model
		if model == "" {
			model = "unknown"
		}
		key := providerCode + "\n" + model
		bucket, ok := buckets[key]
		if !ok {
			bucket = &usageModelWindowAggregate{ProviderCode: providerCode, Model: model}
			buckets[key] = bucket
			order = append(order, key)
		}
		bucket.RequestCount += row.RequestCount
		bucket.InputTokens += row.InputTokens
		bucket.OutputTokens += row.OutputTokens
		bucket.CacheReadTokens += row.CacheReadTokens
		bucket.CacheReadCostUsd += row.CacheReadCostUsd
		bucket.CacheWriteTokens += row.CacheWriteTokens
		bucket.CacheWrite1hTokens += row.CacheWrite1hTokens
		bucket.CacheWriteCostUsd += row.CacheWriteCostUsd
		bucket.ThinkingTokens += row.ThinkingTokens
		bucket.InputImageTokens += row.InputImageTokens
		bucket.OutputImageTokens += row.OutputImageTokens
		bucket.TotalCostUsd += row.TotalCostUsd
	}
	result := make([]usageModelWindowAggregate, 0, len(order))
	for _, key := range order {
		result = append(result, *buckets[key])
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].RequestCount != result[j].RequestCount {
			return result[i].RequestCount > result[j].RequestCount
		}
		if CompareText(result[i].ProviderCode, result[j].ProviderCode) != 0 {
			return CompareText(result[i].ProviderCode, result[j].ProviderCode) < 0
		}
		return CompareText(result[i].Model, result[j].Model) < 0
	})
	return result
}

// usageErrorWindowAggregate mirrors UsageErrorWindowAggregate。
type usageErrorWindowAggregate struct {
	ErrorGroup   string
	ProviderCode string
	ErrorCode    string
	StatusCode   float64
	ErrorMessage string
	ErrorCount   float64
}

// aggregateUsageErrorRows mirrors aggregateUsageErrorRows（含排序）。
func aggregateUsageErrorRows(rowsByDate map[string][]usageErrorWindowRow, rangeValue StatsRange) []usageErrorWindowAggregate {
	buckets := map[string]*usageErrorWindowAggregate{}
	order := []string{}
	for _, row := range RowsForDateRange(rowsByDate, rangeValue.StartDate, rangeValue.EndDate) {
		errorGroup := row.ErrorGroup
		if errorGroup == "" {
			errorGroup = "unknown"
		}
		providerCode := row.ProviderCode
		if providerCode == "" {
			providerCode = "unknown"
		}
		errorCode := row.ErrorCode
		if errorCode == "" {
			errorCode = "unknown"
		}
		key := errorGroup + "\n" + providerCode + "\n" + errorCode
		bucket, ok := buckets[key]
		if !ok {
			bucket = &usageErrorWindowAggregate{ErrorGroup: errorGroup, ProviderCode: providerCode, ErrorCode: errorCode}
			buckets[key] = bucket
			order = append(order, key)
		}
		if row.StatusCode > bucket.StatusCode {
			bucket.StatusCode = row.StatusCode
		}
		if row.ErrorMessage != "" {
			if bucket.ErrorMessage == "" || row.ErrorMessage > bucket.ErrorMessage {
				bucket.ErrorMessage = row.ErrorMessage
			}
		}
		bucket.ErrorCount += row.ErrorCount
	}
	result := make([]usageErrorWindowAggregate, 0, len(order))
	for _, key := range order {
		result = append(result, *buckets[key])
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].ErrorCount != result[j].ErrorCount {
			return result[i].ErrorCount > result[j].ErrorCount
		}
		if CompareText(result[i].ProviderCode, result[j].ProviderCode) != 0 {
			return CompareText(result[i].ProviderCode, result[j].ProviderCode) < 0
		}
		if CompareText(result[i].ErrorCode, result[j].ErrorCode) != 0 {
			return CompareText(result[i].ErrorCode, result[j].ErrorCode) < 0
		}
		return CompareText(result[i].ErrorGroup, result[j].ErrorGroup) < 0
	})
	return result
}

func (w *WindowRefresher) refreshUsageModelRankWindows(ctx context.Context, tx *sql.Tx, systemAccountID string, ranges []StatsRange, earliestDate, todayKey, updatedAt string) error {
	query := w.Dialect.bind(`
		SELECT stat_date, provider_code, model, request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd,
		  cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd, thinking_tokens, input_image_tokens, output_image_tokens, total_cost_usd
		FROM ` + w.Dialect.StatsTable("usage_model_daily") + `
		WHERE system_account_id = ? AND stat_date >= ? AND stat_date <= ?
		ORDER BY stat_date ASC
	`)
	rows, err := tx.QueryContext(ctx, query, systemAccountID, earliestDate, todayKey)
	if err != nil {
		return err
	}
	modelRows := []usageModelWindowRow{}
	scan := func(statDate, providerCode, model string, numbers []float64) {
		modelRows = append(modelRows, usageModelWindowRow{
			StatDate: statDate, ProviderCode: providerCode, Model: model,
			RequestCount: numbers[0], InputTokens: numbers[1], OutputTokens: numbers[2],
			CacheReadTokens: numbers[3], CacheReadCostUsd: numbers[4],
			CacheWriteTokens: numbers[5], CacheWrite1hTokens: numbers[6], CacheWriteCostUsd: numbers[7],
			ThinkingTokens: numbers[8], InputImageTokens: numbers[9], OutputImageTokens: numbers[10],
			TotalCostUsd: numbers[11],
		})
	}
	if err := consumeModelRows(rows, scan); err != nil {
		return err
	}
	rowsByDate := RowsByStatDate(modelRows, func(r usageModelWindowRow) string { return r.StatDate })
	for _, rangeValue := range ranges {
		ranked := aggregateUsageModelRows(rowsByDate, rangeValue)
		if len(ranked) > 10 {
			ranked = ranked[:10]
		}
		for index, row := range ranked {
			insert := w.Dialect.bind(`
				INSERT INTO ` + w.Dialect.StatsTable("usage_model_rank_windows") + ` (
				  system_account_id, window_key, start_date, end_date, rank, provider_code, model,
				  request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd,
				  thinking_tokens, input_image_tokens, output_image_tokens, total_cost_usd, updated_at)
				VALUES (` + placeholders(20) + `)
			`)
			if _, err := tx.ExecContext(ctx, insert,
				systemAccountID, RangeWindowKey(rangeValue), rangeValue.StartDate, rangeValue.EndDate, index+1,
				row.ProviderCode, row.Model,
				row.RequestCount, row.InputTokens, row.OutputTokens, row.CacheReadTokens, row.CacheReadCostUsd,
				row.CacheWriteTokens, row.CacheWrite1hTokens, row.CacheWriteCostUsd,
				row.ThinkingTokens, row.InputImageTokens, row.OutputImageTokens, row.TotalCostUsd, updatedAt); err != nil {
				return err
			}
		}
	}
	return nil
}

func consumeModelRows(rows *sql.Rows, scan func(statDate, providerCode, model string, numbers []float64)) error {
	if rows == nil {
		return nil
	}
	defer rows.Close()
	for rows.Next() {
		var statDate, providerCode, model string
		numbers := make([]float64, 12)
		if err := rows.Scan(&statDate, &providerCode, &model,
			&numbers[0], &numbers[1], &numbers[2], &numbers[3], &numbers[4], &numbers[5],
			&numbers[6], &numbers[7], &numbers[8], &numbers[9], &numbers[10], &numbers[11]); err != nil {
			return err
		}
		scan(statDate, providerCode, model, numbers)
	}
	return rows.Err()
}

func (w *WindowRefresher) refreshUsageErrorRankWindows(ctx context.Context, tx *sql.Tx, systemAccountID string, ranges []StatsRange, earliestDate, todayKey, updatedAt string) error {
	query := w.Dialect.bind(`
		SELECT stat_date, error_group, provider_code, error_code, status_code, error_message, error_count
		FROM ` + w.Dialect.StatsTable("usage_error_daily") + `
		WHERE system_account_id = ? AND stat_date >= ? AND stat_date <= ?
		ORDER BY stat_date ASC
	`)
	rows, err := tx.QueryContext(ctx, query, systemAccountID, earliestDate, todayKey)
	if err != nil {
		return err
	}
	defer rows.Close()
	errorRows := []usageErrorWindowRow{}
	for rows.Next() {
		var row usageErrorWindowRow
		var errorMessage sql.NullString
		if err := rows.Scan(&row.StatDate, &row.ErrorGroup, &row.ProviderCode, &row.ErrorCode, &row.StatusCode, &errorMessage, &row.ErrorCount); err != nil {
			return err
		}
		row.ErrorMessage = errorMessage.String
		errorRows = append(errorRows, row)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rowsByDate := RowsByStatDate(errorRows, func(r usageErrorWindowRow) string { return r.StatDate })
	for _, rangeValue := range ranges {
		ranked := aggregateUsageErrorRows(rowsByDate, rangeValue)
		if len(ranked) > 10 {
			ranked = ranked[:10]
		}
		for index, row := range ranked {
			insert := w.Dialect.bind(`
				INSERT INTO ` + w.Dialect.StatsTable("usage_error_rank_windows") + ` (
				  system_account_id, window_key, start_date, end_date, rank, provider_code, error_code,
				  status_code, error_message, error_count, updated_at)
				VALUES (` + placeholders(11) + `)
			`)
			var errorMessage any
			if row.ErrorMessage != "" {
				errorMessage = row.ErrorMessage
			}
			if _, err := tx.ExecContext(ctx, insert,
				systemAccountID, RangeWindowKey(rangeValue), rangeValue.StartDate, rangeValue.EndDate, index+1,
				row.ProviderCode, row.ErrorCode, row.StatusCode, errorMessage, row.ErrorCount, updatedAt); err != nil {
				return err
			}
		}
	}
	return nil
}

// refreshAiPerformanceSummaryWindows mirrors
// refreshAiPerformanceSummaryWindowSnapshots（SQLite 全量重建路径）：
// DELETE 全表后按 uniqueSystemAccountIds + global 重建 31 天固定窗口。
func (w *WindowRefresher) refreshAiPerformanceSummaryWindows(ctx context.Context, tx *sql.Tx, stageContext refreshStageContext) error {
	if _, err := tx.ExecContext(ctx, w.Dialect.bind(`DELETE FROM `+w.Dialect.StatsTable("ai_performance_summary_windows"))); err != nil {
		return err
	}
	scopes, err := w.usageOverviewSnapshotScopes(ctx, tx)
	if err != nil {
		return err
	}
	uniqueSystemAccountIDs := map[string]struct{}{}
	for _, scope := range scopes {
		if scope.systemAccountID != GlobalStatsSystemAccountID {
			uniqueSystemAccountIDs[scope.systemAccountID] = struct{}{}
		}
	}
	ranges := FixedUsageStatsRanges(stageContext.todayKey)
	earliestDate := stageContext.todayKey
	if len(ranges) > 0 {
		earliestDate = ranges[0].StartDate
	}
	for _, systemAccountID := range append(mapKeyList(uniqueSystemAccountIDs), GlobalStatsSystemAccountID) {
		rows, err := w.loadAiPerformanceSourceRows(ctx, tx, systemAccountID, earliestDate, stageContext.todayKey)
		if err != nil {
			return err
		}
		rowsByDate := RowsByStatDate(rows, func(r DailyWindowRow) string { return r.StatDate })
		for _, rangeValue := range ranges {
			aggregate := AggregateUsageRowsForRange(rowsByDate, rangeValue)
			insert := w.Dialect.bind(`
				INSERT INTO ` + w.Dialect.StatsTable("ai_performance_summary_windows") + ` (
				  system_account_id, window_key, start_date, end_date, request_count, duration_ms_sum, duration_ms_count,
				  duration_ms_max, first_token_ms_sum, first_token_ms_count, first_token_ms_max, updated_at)
				VALUES (` + placeholders(12) + `)
			`)
			if _, err := tx.ExecContext(ctx, insert,
				systemAccountID, RangeWindowKey(rangeValue), rangeValue.StartDate, rangeValue.EndDate,
				aggregate.RequestCount, aggregate.DurationMsSum, aggregate.DurationMsCount,
				aggregate.DurationMsMax, aggregate.FirstTokenMsSum, aggregate.FirstTokenMsCount, aggregate.FirstTokenMsMax,
				stageContext.updatedAt); err != nil {
				return err
			}
		}
	}
	return nil
}

func (w *WindowRefresher) loadAiPerformanceSourceRows(ctx context.Context, tx *sql.Tx, systemAccountID, earliestDate, todayKey string) ([]DailyWindowRow, error) {
	query := w.Dialect.bind(`
		SELECT stat_date,
		  COALESCE(SUM(request_count), 0) AS request_count,
		  COALESCE(SUM(success_count), 0) AS success_count,
		  COALESCE(SUM(error_count), 0) AS error_count,
		  COALESCE(SUM(input_tokens), 0) AS input_tokens,
		  COALESCE(SUM(output_tokens), 0) AS output_tokens,
		  COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens,
		  COALESCE(SUM(total_cost_usd), 0) AS total_cost_usd,
		  COALESCE(SUM(duration_ms_sum), 0) AS duration_ms_sum,
		  COALESCE(SUM(duration_ms_count), 0) AS duration_ms_count,
		  COALESCE(MAX(duration_ms_max), 0) AS duration_ms_max,
		  COALESCE(SUM(first_token_ms_sum), 0) AS first_token_ms_sum,
		  COALESCE(SUM(first_token_ms_count), 0) AS first_token_ms_count,
		  COALESCE(MAX(first_token_ms_max), 0) AS first_token_ms_max,
		  MAX(last_used_at) AS last_used_at
		FROM ` + w.Dialect.StatsTable("usage_stats_daily") + `
		WHERE system_account_id = ? AND scope_type = 'account' AND stat_date >= ? AND stat_date <= ?
		GROUP BY stat_date
		ORDER BY stat_date ASC
	`)
	rows, err := tx.QueryContext(ctx, query, systemAccountID, earliestDate, todayKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []DailyWindowRow
	for rows.Next() {
		var row DailyWindowRow
		var lastUsedAt sql.NullString
		if err := rows.Scan(&row.StatDate, &row.RequestCount, &row.SuccessCount, &row.ErrorCount,
			&row.InputTokens, &row.OutputTokens, &row.CacheReadTokens, &row.TotalCostUsd,
			&row.DurationMsSum, &row.DurationMsCount, &row.DurationMsMax,
			&row.FirstTokenMsSum, &row.FirstTokenMsCount, &row.FirstTokenMsMax, &lastUsedAt); err != nil {
			return nil, err
		}
		row.LastUsedAt = lastUsedAt.String
		result = append(result, row)
	}
	return result, rows.Err()
}

// refreshUsageScopeRangeWindows mirrors refreshUsageScopeRangeWindowSnapshots：
// 热窗口范围的全 scope 聚合（DELETE 区间行 + INSERT SELECT）。
func (w *WindowRefresher) refreshUsageScopeRangeWindows(ctx context.Context, tx *sql.Tx, stageContext refreshStageContext) error {
	ranges := HotUsageStatsRanges(stageContext.todayKey)
	if len(ranges) == 0 {
		return nil
	}
	earliestStartDate := ranges[0].StartDate
	for _, rangeValue := range ranges {
		if rangeValue.StartDate < earliestStartDate {
			earliestStartDate = rangeValue.StartDate
		}
	}
	if _, err := tx.ExecContext(ctx, w.Dialect.bind(`DELETE FROM `+w.Dialect.StatsTable("usage_scope_range_windows")+
		` WHERE end_date >= ? AND end_date <= ?`), earliestStartDate, stageContext.todayKey); err != nil {
		return err
	}
	for _, rangeValue := range ranges {
		query := w.Dialect.bind(scopeRangeWindowInsertSQL(w.Dialect, false))
		if _, err := tx.ExecContext(ctx, query, rangeValue.StartDate, rangeValue.EndDate, stageContext.updatedAt, rangeValue.StartDate, rangeValue.EndDate); err != nil {
			return err
		}
	}
	return nil
}

// refreshAuthorizationUsageRangeWindows mirrors
// refreshAuthorizationUsageRangeWindowSnapshots。
func (w *WindowRefresher) refreshAuthorizationUsageRangeWindows(ctx context.Context, tx *sql.Tx, stageContext refreshStageContext) error {
	ranges := HotUsageStatsRanges(stageContext.todayKey)
	if len(ranges) == 0 {
		return nil
	}
	earliestStartDate := ranges[0].StartDate
	for _, rangeValue := range ranges {
		if rangeValue.StartDate < earliestStartDate {
			earliestStartDate = rangeValue.StartDate
		}
	}
	for _, tableName := range []string{"authorization_team_usage_range_windows", "authorization_user_usage_range_windows"} {
		if _, err := tx.ExecContext(ctx, w.Dialect.bind(`DELETE FROM `+w.Dialect.StatsTable(tableName)+
			` WHERE end_date >= ? AND end_date <= ?`), earliestStartDate, stageContext.todayKey); err != nil {
			return err
		}
	}
	for _, rangeValue := range ranges {
		teamQuery := w.Dialect.bind(authorizationRangeWindowInsertSQL(w.Dialect, true))
		if _, err := tx.ExecContext(ctx, teamQuery, rangeValue.StartDate, rangeValue.EndDate, stageContext.updatedAt, rangeValue.StartDate, rangeValue.EndDate); err != nil {
			return err
		}
		userQuery := w.Dialect.bind(authorizationRangeWindowInsertSQL(w.Dialect, false))
		if _, err := tx.ExecContext(ctx, userQuery, rangeValue.StartDate, rangeValue.EndDate, stageContext.updatedAt, rangeValue.StartDate, rangeValue.EndDate); err != nil {
			return err
		}
	}
	return nil
}

func scopeRangeWindowInsertSQL(dialect Dialect, _ bool) string {
	return `
		INSERT INTO ` + dialect.StatsTable("usage_scope_range_windows") + ` (
		  system_account_id, scope_type, scope_id, start_date, end_date,
		  request_count, success_count, error_count, input_tokens, output_tokens, cache_read_tokens,
		  cache_read_cost_usd, cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd, thinking_tokens, input_image_tokens, output_image_tokens, total_cost_usd, duration_ms_sum, duration_ms_count, duration_ms_max,
		  first_token_ms_sum, first_token_ms_count, first_token_ms_max, active_days,
		  last_used_at, last_error_at, updated_at)
		SELECT
		  system_account_id,
		  scope_type,
		  scope_id,
		  ?,
		  ?,
		  COALESCE(SUM(request_count), 0),
		  COALESCE(SUM(success_count), 0),
		  COALESCE(SUM(error_count), 0),
		  COALESCE(SUM(input_tokens), 0),
		  COALESCE(SUM(output_tokens), 0),
		  COALESCE(SUM(cache_read_tokens), 0),
		  COALESCE(SUM(cache_read_cost_usd), 0),
		  COALESCE(SUM(cache_write_tokens), 0),
		  COALESCE(SUM(cache_write_1h_tokens), 0),
		  COALESCE(SUM(cache_write_cost_usd), 0),
		  COALESCE(SUM(thinking_tokens), 0),
		  COALESCE(SUM(input_image_tokens), 0),
		  COALESCE(SUM(output_image_tokens), 0),
		  COALESCE(SUM(total_cost_usd), 0),
		  COALESCE(SUM(duration_ms_sum), 0),
		  COALESCE(SUM(duration_ms_count), 0),
		  COALESCE(MAX(duration_ms_max), 0),
		  COALESCE(SUM(first_token_ms_sum), 0),
		  COALESCE(SUM(first_token_ms_count), 0),
		  COALESCE(MAX(first_token_ms_max), 0),
		  COUNT(CASE
			WHEN request_count > 0
			  OR input_tokens > 0
			  OR output_tokens > 0
			  OR cache_read_tokens > 0
			  OR cache_write_tokens > 0
			  OR cache_write_1h_tokens > 0
			  OR thinking_tokens > 0
			  OR input_image_tokens > 0
			  OR output_image_tokens > 0
			  OR total_cost_usd > 0
		  THEN 1 END),
		  MAX(last_used_at),
		  MAX(last_error_at),
		  ?
		FROM ` + dialect.StatsTable("usage_stats_daily") + `
		WHERE stat_date >= ?
		  AND stat_date <= ?
		GROUP BY system_account_id, scope_type, scope_id
		HAVING COALESCE(SUM(request_count), 0) > 0
		  OR COALESCE(SUM(input_tokens), 0) > 0
		  OR COALESCE(SUM(output_tokens), 0) > 0
		  OR COALESCE(SUM(cache_read_tokens), 0) > 0
		  OR COALESCE(SUM(cache_read_cost_usd), 0) > 0
		  OR COALESCE(SUM(cache_write_tokens), 0) > 0
		  OR COALESCE(SUM(cache_write_1h_tokens), 0) > 0
		  OR COALESCE(SUM(cache_write_cost_usd), 0) > 0
		  OR COALESCE(SUM(thinking_tokens), 0) > 0
		  OR COALESCE(SUM(input_image_tokens), 0) > 0
		  OR COALESCE(SUM(output_image_tokens), 0) > 0
		  OR COALESCE(SUM(total_cost_usd), 0) > 0
	`
}

func authorizationRangeWindowInsertSQL(dialect Dialect, team bool) string {
	// 列顺序与 Node usage-range-windows.repository.ts 的 insertTeamRange /
	// insertUserRange 逐字对齐。
	tableName := "authorization_user_usage_range_windows"
	sourceTable := "authorization_user_usage_summary_daily"
	insertColumns := `system_account_id, start_date, end_date, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id,
		  request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd, thinking_tokens, input_image_tokens, output_image_tokens, total_cost_usd, last_used_at, updated_at`
	selectColumns := `
		  system_account_id,
		  ?,
		  ?,
		  team_filter_id,
		  grantee_filter_system_account_id,
		  resource_filter_type,
		  resource_filter_id,`
	groupColumns := `system_account_id, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id`
	if team {
		tableName = "authorization_team_usage_range_windows"
		sourceTable = "authorization_team_usage_summary_daily"
		insertColumns = `system_account_id, start_date, end_date, team_filter_id, resource_filter_type, resource_filter_id,
		  request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd, thinking_tokens, input_image_tokens, output_image_tokens, total_cost_usd, last_used_at, updated_at`
		selectColumns = `
		  system_account_id,
		  ?,
		  ?,
		  team_filter_id,
		  resource_filter_type,
		  resource_filter_id,`
		groupColumns = `system_account_id, team_filter_id, resource_filter_type, resource_filter_id`
	}
	return `
		INSERT INTO ` + dialect.StatsTable(tableName) + ` (
		  ` + insertColumns + `)
		SELECT` + selectColumns + `
		  COALESCE(SUM(request_count), 0),
		  COALESCE(SUM(input_tokens), 0),
		  COALESCE(SUM(output_tokens), 0),
		  COALESCE(SUM(cache_read_tokens), 0),
		  COALESCE(SUM(cache_read_cost_usd), 0),
		  COALESCE(SUM(cache_write_tokens), 0),
		  COALESCE(SUM(cache_write_1h_tokens), 0),
		  COALESCE(SUM(cache_write_cost_usd), 0),
		  COALESCE(SUM(thinking_tokens), 0),
		  COALESCE(SUM(input_image_tokens), 0),
		  COALESCE(SUM(output_image_tokens), 0),
		  COALESCE(SUM(total_cost_usd), 0),
		  MAX(last_used_at),
		  ?
		FROM ` + dialect.StatsTable(sourceTable) + `
		WHERE stat_date >= ?
		  AND stat_date <= ?
		GROUP BY ` + groupColumns + `
		HAVING COALESCE(SUM(request_count), 0) > 0
		  OR COALESCE(SUM(input_tokens), 0) > 0
		  OR COALESCE(SUM(output_tokens), 0) > 0
		  OR COALESCE(SUM(cache_read_tokens), 0) > 0
		  OR COALESCE(SUM(cache_read_cost_usd), 0) > 0
		  OR COALESCE(SUM(cache_write_tokens), 0) > 0
		  OR COALESCE(SUM(cache_write_1h_tokens), 0) > 0
		  OR COALESCE(SUM(cache_write_cost_usd), 0) > 0
		  OR COALESCE(SUM(thinking_tokens), 0) > 0
		  OR COALESCE(SUM(input_image_tokens), 0) > 0
		  OR COALESCE(SUM(output_image_tokens), 0) > 0
		  OR COALESCE(SUM(total_cost_usd), 0) > 0
	`
}

var _ = time.Time{}

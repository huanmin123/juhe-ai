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

// overviewWindowTableNames 是 StageUsageOverviewWindows 的 4 张目标窗口表
// （全量清空、增量逐账户删除与孤儿清理共用同一清单）。
var overviewWindowTableNames = []string{
	"usage_overview_summary_windows",
	"usage_overview_trend_windows",
	"usage_model_rank_windows",
	"usage_error_rank_windows",
}

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

// usageOverviewScopesForAccount 读取单账户在 usage_stats_totals 的
// (system, scope) 清单（排序与 LIMIT 语义与 usageOverviewSnapshotScopes
// 一致）；空清单 = 该账户在 totals 已无 system_account scope 行，不参与
// 增量重建（窗口残留由孤儿清理删除）。
func (w *WindowRefresher) usageOverviewScopesForAccount(ctx context.Context, tx *sql.Tx, systemAccountID string) ([]usageOverviewScope, error) {
	query := w.Dialect.bind(`
		SELECT system_account_id, scope_id
		FROM ` + w.Dialect.StatsTable("usage_stats_totals") + `
		WHERE scope_type = 'system_account' AND system_account_id = ?
		ORDER BY updated_at DESC, system_account_id ASC, scope_id ASC
		LIMIT ?
	`)
	rows, err := tx.QueryContext(ctx, query, systemAccountID, maxUsageOverviewSnapshotScopes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	scopes := []usageOverviewScope{}
	for rows.Next() {
		var accountID, scopeID sql.NullString
		if err := rows.Scan(&accountID, &scopeID); err != nil {
			return nil, err
		}
		if accountID.String == "" || scopeID.String == "" {
			continue
		}
		scopes = append(scopes, usageOverviewScope{accountID.String, scopeID.String})
	}
	return scopes, rows.Err()
}

// hasUsageStatsTotalsSystemAccount 判断账户在 usage_stats_totals 是否仍有
// system_account scope 行（ai-performance 增量的 claimed ∩ totals 交集判定；
// overview 增量经 usageOverviewScopesForAccount 同源判定）。
func (w *WindowRefresher) hasUsageStatsTotalsSystemAccount(ctx context.Context, tx *sql.Tx, systemAccountID string) (bool, error) {
	query := w.Dialect.bind(`SELECT 1 FROM ` + w.Dialect.StatsTable("usage_stats_totals") + `
		WHERE scope_type = 'system_account' AND system_account_id = ?
		LIMIT 1`)
	rows, err := tx.QueryContext(ctx, query, systemAccountID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	return rows.Next(), rows.Err()
}

// windowDirtyRow 是两张窗口脏标记表（PK system_account_id，generation 冲突
// 自增）的消费行形态。
type windowDirtyRow struct {
	systemAccountID string
	generation      int64
}

// claimUsageOverviewDirtyRows 读取 usage_overview_dirty_scopes 全部脏行
// （表体量 ≈ 账户数，无需分页）。
func (w *WindowRefresher) claimUsageOverviewDirtyRows(ctx context.Context, tx *sql.Tx) ([]windowDirtyRow, error) {
	query := w.Dialect.bind(`SELECT system_account_id, generation FROM ` +
		w.Dialect.StatsTable("usage_overview_dirty_scopes") + ` ORDER BY system_account_id ASC`)
	return scanWindowDirtyRows(tx.QueryContext(ctx, query))
}

// claimAiPerformanceDirtyRows 读取 ai_performance_summary_dirty_system_accounts
// 全部脏行。
func (w *WindowRefresher) claimAiPerformanceDirtyRows(ctx context.Context, tx *sql.Tx) ([]windowDirtyRow, error) {
	query := w.Dialect.bind(`SELECT system_account_id, generation FROM ` +
		w.Dialect.StatsTable("ai_performance_summary_dirty_system_accounts") + ` ORDER BY system_account_id ASC`)
	return scanWindowDirtyRows(tx.QueryContext(ctx, query))
}

func scanWindowDirtyRows(rows *sql.Rows, err error) ([]windowDirtyRow, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []windowDirtyRow{}
	for rows.Next() {
		var row windowDirtyRow
		if err := rows.Scan(&row.systemAccountID, &row.generation); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// consumeUsageOverviewDirtyRows 逐行按 (system_account_id, generation) CAS
// 消费脏行（镜像 deleteConsumedQuotaHourlyWindowDirtyScopes 的双方言 CAS
// 谓词），返回实际消费成功的行；generation 已被并发聚合推进的行未命中保留，
// 留给下轮消费。
func (w *WindowRefresher) consumeUsageOverviewDirtyRows(ctx context.Context, tx *sql.Tx, rows []windowDirtyRow) ([]windowDirtyRow, error) {
	return consumeWindowDirtyRows(ctx, tx, w.Dialect, "usage_overview_dirty_scopes", rows)
}

// consumeAiPerformanceDirtyRows 语义同 consumeUsageOverviewDirtyRows，消费
// ai_performance_summary_dirty_system_accounts。
func (w *WindowRefresher) consumeAiPerformanceDirtyRows(ctx context.Context, tx *sql.Tx, rows []windowDirtyRow) ([]windowDirtyRow, error) {
	return consumeWindowDirtyRows(ctx, tx, w.Dialect, "ai_performance_summary_dirty_system_accounts", rows)
}

func consumeWindowDirtyRows(ctx context.Context, tx *sql.Tx, dialect Dialect, tableName string, rows []windowDirtyRow) ([]windowDirtyRow, error) {
	query := dialect.bind(`DELETE FROM ` + dialect.StatsTable(tableName) + ` WHERE system_account_id = ? AND generation = ?`)
	consumed := make([]windowDirtyRow, 0, len(rows))
	for _, row := range rows {
		result, err := tx.ExecContext(ctx, query, row.systemAccountID, row.generation)
		if err != nil {
			return nil, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if affected > 0 {
			consumed = append(consumed, row)
		}
	}
	return consumed, nil
}

// usageWindowDirtyRemaining 检查脏表在事务内是否仍有剩余行（含 CAS miss 与
// 并发再打脏）：true 时调用方保留该 job 的 stats_job_state 水位不推进。
func usageWindowDirtyRemaining(ctx context.Context, tx *sql.Tx, dialect Dialect, tableName string) (bool, error) {
	query := dialect.bind(`SELECT 1 FROM ` + dialect.StatsTable(tableName) + ` LIMIT 1`)
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	return rows.Next(), rows.Err()
}

// deleteOrphanUsageWindowRows 反连接删除孤儿窗口行：system_account_id 不在
// usage_stats_totals（scope_type='system_account'）且不是 global 的行
// （global 永远豁免孤儿清理）。
func deleteOrphanUsageWindowRows(ctx context.Context, tx *sql.Tx, dialect Dialect, tableName string) error {
	query := dialect.bind(`DELETE FROM ` + dialect.StatsTable(tableName) + `
		WHERE system_account_id <> 'global'
		  AND system_account_id NOT IN (
			SELECT system_account_id FROM ` + dialect.StatsTable("usage_stats_totals") + `
			WHERE scope_type = 'system_account'
		  )`)
	_, err := tx.ExecContext(ctx, query)
	return err
}

// refreshUsageOverviewWindowSnapshots 刷新 4 张 overview 窗口表：todayKey
// 与 overviewFullRebuildDayKey 不同（含启动首轮）→ 天滚动全量重建（原实现）；
// 同一天 → 脏范围增量（读 usage_overview_dirty_scopes 全部行，空 → noop
// 零 DELETE；非空 → 逐行 generation CAS 消费后按 claimed ∩ totals 逐账户
// 重建 + global 无条件重建 + 反连接孤儿清理，消费/重建/清理同一事务）。
// 返回本轮模式与事务内脏表剩余行标记（true 时调用方保留 watermark）。
func (w *WindowRefresher) refreshUsageOverviewWindowSnapshots(ctx context.Context, tx *sql.Tx, stageContext refreshStageContext) (string, bool, error) {
	mode := WindowRefreshModeFull
	if w.overviewFullRebuildDayKey == stageContext.todayKey {
		var err error
		mode, err = w.rebuildUsageOverviewWindowsIncremental(ctx, tx, stageContext)
		if err != nil {
			return mode, false, err
		}
	} else {
		scopes, err := w.usageOverviewSnapshotScopes(ctx, tx)
		if err != nil {
			return mode, false, err
		}
		for _, tableName := range overviewWindowTableNames {
			if _, err := tx.ExecContext(ctx, w.Dialect.bind(`DELETE FROM `+w.Dialect.StatsTable(tableName))); err != nil {
				return mode, false, err
			}
		}
		if err := w.rebuildUsageOverviewWindowsForScopes(ctx, tx, scopes, stageContext); err != nil {
			return mode, false, err
		}
	}
	remaining, err := usageWindowDirtyRemaining(ctx, tx, w.Dialect, "usage_overview_dirty_scopes")
	if err != nil {
		return mode, false, err
	}
	return mode, remaining, nil
}

// rebuildUsageOverviewWindowsIncremental 是同日增量轮：消费 overview 脏行并
// 只重建受影响账户与 global，返回实际模式（incremental 或 noop）。
func (w *WindowRefresher) rebuildUsageOverviewWindowsIncremental(ctx context.Context, tx *sql.Tx, stageContext refreshStageContext) (string, error) {
	claimed, err := w.claimUsageOverviewDirtyRows(ctx, tx)
	if err != nil {
		return WindowRefreshModeIncremental, err
	}
	if len(claimed) == 0 {
		// 脏表为空：本轮 no-op，不 DELETE 任何窗口表行。
		return WindowRefreshModeNoop, nil
	}
	consumed, err := w.consumeUsageOverviewDirtyRows(ctx, tx, claimed)
	if err != nil {
		return WindowRefreshModeIncremental, err
	}
	// claimed ∩ totals：totals 已无对应行的账户不重建（孤儿清理负责删残留）。
	scopes := []usageOverviewScope{}
	rebuildAccounts := make([]string, 0, len(consumed))
	for _, row := range consumed {
		accountScopes, err := w.usageOverviewScopesForAccount(ctx, tx, row.systemAccountID)
		if err != nil {
			return WindowRefreshModeIncremental, err
		}
		if len(accountScopes) == 0 {
			continue
		}
		rebuildAccounts = append(rebuildAccounts, row.systemAccountID)
		scopes = append(scopes, accountScopes...)
	}
	hasGlobal := false
	for _, account := range rebuildAccounts {
		if account == GlobalStatsSystemAccountID {
			hasGlobal = true
		}
	}
	// 任一 claimed 非空即重建 global 行（与全量的 global 兜底同形）。
	if !hasGlobal {
		rebuildAccounts = append(rebuildAccounts, GlobalStatsSystemAccountID)
		scopes = append(scopes, usageOverviewScope{GlobalStatsSystemAccountID, GlobalStatsScopeID})
	}
	for _, tableName := range overviewWindowTableNames {
		for _, systemAccountID := range rebuildAccounts {
			if _, err := tx.ExecContext(ctx, w.Dialect.bind(`DELETE FROM `+w.Dialect.StatsTable(tableName)+` WHERE system_account_id = ?`), systemAccountID); err != nil {
				return WindowRefreshModeIncremental, err
			}
		}
	}
	if err := w.rebuildUsageOverviewWindowsForScopes(ctx, tx, scopes, stageContext); err != nil {
		return WindowRefreshModeIncremental, err
	}
	for _, tableName := range overviewWindowTableNames {
		if err := deleteOrphanUsageWindowRows(ctx, tx, w.Dialect, tableName); err != nil {
			return WindowRefreshModeIncremental, err
		}
	}
	return WindowRefreshModeIncremental, nil
}

// rebuildUsageOverviewWindowsForScopes 是 4 张 overview 窗口表的行构建共享
// 路径（全量与增量共用同一份行构建代码）：summary/trend 按 scopes 清单逐
// (system, scope) 对重建，model/error rank 按 unique system + global 重建；
// 调用方负责先删除待重建行集。
func (w *WindowRefresher) rebuildUsageOverviewWindowsForScopes(ctx context.Context, tx *sql.Tx, scopes []usageOverviewScope, stageContext refreshStageContext) error {
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
	// summary：scope × range 汇总 usage_stats_daily（每 scope 合并为一条多行
	// VALUES INSERT，ranges 迭代顺序即 VALUES 行序；行集/行值与逐行执行一致）。
	summaryInsert := `
		INSERT INTO ` + w.Dialect.StatsTable("usage_overview_summary_windows") + ` (
		  system_account_id, window_key, start_date, end_date, request_count, success_count, error_count,
		  input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd,
		  thinking_tokens, input_image_tokens, output_image_tokens, total_cost_usd,
		  duration_ms_sum, duration_ms_count, first_token_ms_sum, first_token_ms_count,
		  last_used_at, updated_at)
		VALUES `
	for _, scope := range scopes {
		rows, err := w.loadDailyWindowRows(ctx, tx, scope.systemAccountID, scope.scopeID, earliestDate, stageContext.todayKey)
		if err != nil {
			return err
		}
		rowsByDate := RowsByStatDate(rows, func(r DailyWindowRow) string { return r.StatDate })
		summaryRows := make([][]any, 0, len(ranges))
		for _, rangeValue := range ranges {
			aggregate := AggregateUsageRowsForRange(rowsByDate, rangeValue)
			var lastUsedAt any
			if aggregate.LastUsedAt != "" {
				lastUsedAt = aggregate.LastUsedAt
			}
			summaryRows = append(summaryRows, []any{
				scope.systemAccountID, RangeWindowKey(rangeValue), rangeValue.StartDate, rangeValue.EndDate,
				aggregate.RequestCount, aggregate.SuccessCount, aggregate.ErrorCount,
				aggregate.InputTokens, aggregate.OutputTokens, aggregate.CacheReadTokens, aggregate.CacheReadCostUsd,
				aggregate.CacheWriteTokens, aggregate.CacheWrite1hTokens, aggregate.CacheWriteCostUsd,
				aggregate.ThinkingTokens, aggregate.InputImageTokens, aggregate.OutputImageTokens, aggregate.TotalCostUsd,
				aggregate.DurationMsSum, aggregate.DurationMsCount, aggregate.FirstTokenMsSum, aggregate.FirstTokenMsCount,
				lastUsedAt, stageContext.updatedAt,
			})
		}
		if err := execOverviewWindowInsertRows(ctx, tx, w.Dialect, summaryInsert, 24, summaryRows); err != nil {
			return err
		}
	}
	// trend：scope × range 汇总 usage_stats_hourly 趋势桶（每 scope 合并为
	// 多行 VALUES INSERT，「ranges 外层顺序 + 桶键字典序」即 VALUES 行序）。
	trendInsert := `
		INSERT INTO ` + w.Dialect.StatsTable("usage_overview_trend_windows") + ` (
		  system_account_id, window_key, start_date, end_date, bucket_key, request_count, error_count,
		  input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd,
		  thinking_tokens, input_image_tokens, output_image_tokens, total_cost_usd,
		  duration_ms_sum, duration_ms_count, updated_at)
		VALUES `
	for _, scope := range scopes {
		rows, err := w.loadHourlyWindowRows(ctx, tx, scope.systemAccountID, scope.scopeID, earliestDate, stageContext.todayKey)
		if err != nil {
			return err
		}
		trendRows := [][]any{}
		for _, rangeValue := range ranges {
			buckets := AggregateUsageTrendBuckets(rows, rangeValue)
			for _, bucketKey := range SortedMapKeys(buckets) {
				bucket := buckets[bucketKey]
				trendRows = append(trendRows, []any{
					scope.systemAccountID, RangeWindowKey(rangeValue), rangeValue.StartDate, rangeValue.EndDate, bucketKey,
					bucket.RequestCount, bucket.ErrorCount,
					bucket.InputTokens, bucket.OutputTokens, bucket.CacheReadTokens, bucket.CacheReadCostUsd,
					bucket.CacheWriteTokens, bucket.CacheWrite1hTokens, bucket.CacheWriteCostUsd,
					bucket.ThinkingTokens, bucket.InputImageTokens, bucket.OutputImageTokens, bucket.TotalCostUsd,
					bucket.DurationMsSum, bucket.DurationMsCount, stageContext.updatedAt,
				})
			}
		}
		if err := execOverviewWindowInsertRows(ctx, tx, w.Dialect, trendInsert, 21, trendRows); err != nil {
			return err
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

// overviewWindowInsertMaxRows 是 overview 族窗口表多行 VALUES INSERT 单条
// 语句的行数上限：最宽 24 列 × 200 行 = 4800 个绑定参数，低于 SQLite
// （modernc，32766）与 PG（65535）单语句参数上限；行数超出时按行序分片为
// 多条语句，行集、行值与行序不变。
const overviewWindowInsertMaxRows = 200

// placeholderRows 生成 rows 行 × cols 列的多行 VALUES 占位符串
// （"(?, ?, ...), (?, ?, ...)"形态；SQLite `?` 原样，PG 经 bind 得到跨行
// 连续编号的 $1..$n）。
func placeholderRows(rows, cols int) string {
	row := "(" + placeholders(cols) + ")"
	result := ""
	for index := 0; index < rows; index++ {
		if index > 0 {
			result += ", "
		}
		result += row
	}
	return result
}

// execOverviewWindowInsertRows 把同一目标表的全部数据行按
// overviewWindowInsertMaxRows 分片合并为多行 VALUES INSERT 执行：
// 行集、行值与行序与逐行单行 INSERT 完全一致，仅语句数收敛。
// insertSQL 是不含占位符的 "INSERT INTO ... (columns) VALUES " 前缀，
// 占位符由 placeholderRows 按片内行数生成后统一 bind（双方言）。
func execOverviewWindowInsertRows(ctx context.Context, tx *sql.Tx, dialect Dialect, insertSQL string, columnCount int, rows [][]any) error {
	for start := 0; start < len(rows); start += overviewWindowInsertMaxRows {
		end := start + overviewWindowInsertMaxRows
		if end > len(rows) {
			end = len(rows)
		}
		query := dialect.bind(insertSQL + placeholderRows(end-start, columnCount))
		args := make([]any, 0, (end-start)*columnCount)
		for _, row := range rows[start:end] {
			args = append(args, row...)
		}
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return err
		}
	}
	return nil
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
	// 每个系统账号合并为多行 VALUES INSERT（range 外序 + rank 序即 VALUES 行序）。
	modelRankInsert := `
		INSERT INTO ` + w.Dialect.StatsTable("usage_model_rank_windows") + ` (
		  system_account_id, window_key, start_date, end_date, rank, provider_code, model,
		  request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd,
		  thinking_tokens, input_image_tokens, output_image_tokens, total_cost_usd, updated_at)
		VALUES `
	rankRows := make([][]any, 0, len(ranges)*10)
	for _, rangeValue := range ranges {
		ranked := aggregateUsageModelRows(rowsByDate, rangeValue)
		if len(ranked) > 10 {
			ranked = ranked[:10]
		}
		for index, row := range ranked {
			rankRows = append(rankRows, []any{
				systemAccountID, RangeWindowKey(rangeValue), rangeValue.StartDate, rangeValue.EndDate, index + 1,
				row.ProviderCode, row.Model,
				row.RequestCount, row.InputTokens, row.OutputTokens, row.CacheReadTokens, row.CacheReadCostUsd,
				row.CacheWriteTokens, row.CacheWrite1hTokens, row.CacheWriteCostUsd,
				row.ThinkingTokens, row.InputImageTokens, row.OutputImageTokens, row.TotalCostUsd, updatedAt,
			})
		}
	}
	return execOverviewWindowInsertRows(ctx, tx, w.Dialect, modelRankInsert, 20, rankRows)
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
	// 每个系统账号合并为多行 VALUES INSERT（range 外序 + rank 序即 VALUES 行序）。
	errorRankInsert := `
		INSERT INTO ` + w.Dialect.StatsTable("usage_error_rank_windows") + ` (
		  system_account_id, window_key, start_date, end_date, rank, provider_code, error_code,
		  status_code, error_message, error_count, updated_at)
		VALUES `
	rankRows := make([][]any, 0, len(ranges)*10)
	for _, rangeValue := range ranges {
		ranked := aggregateUsageErrorRows(rowsByDate, rangeValue)
		if len(ranked) > 10 {
			ranked = ranked[:10]
		}
		for index, row := range ranked {
			var errorMessage any
			if row.ErrorMessage != "" {
				errorMessage = row.ErrorMessage
			}
			rankRows = append(rankRows, []any{
				systemAccountID, RangeWindowKey(rangeValue), rangeValue.StartDate, rangeValue.EndDate, index + 1,
				row.ProviderCode, row.ErrorCode, row.StatusCode, errorMessage, row.ErrorCount, updatedAt,
			})
		}
	}
	return execOverviewWindowInsertRows(ctx, tx, w.Dialect, errorRankInsert, 11, rankRows)
}

// refreshAiPerformanceSummaryWindows 刷新 ai_performance_summary_windows：
// todayKey 与 aiPerformanceFullRebuildDayKey 不同（含启动首轮）→ 天滚动
// 全量重建（全表 DELETE + 全账户重插）；同一天 → 脏范围增量
// （ai_performance_summary_dirty_system_accounts 全行读取，空 → noop 零
// DELETE；非空 → 逐行 generation CAS 消费后按 claimed ∩ totals 逐账户 +
// global 重建 + 反连接孤儿清理）。全量与增量共用同一行构建代码：多行
// VALUES INSERT（12 列、200 行/片），行集/行值/行序与逐行单条 INSERT
// 时代一致。返回本轮模式与事务内脏表剩余行标记。
func (w *WindowRefresher) refreshAiPerformanceSummaryWindows(ctx context.Context, tx *sql.Tx, stageContext refreshStageContext) (string, bool, error) {
	mode := WindowRefreshModeFull
	var accounts []string
	if w.aiPerformanceFullRebuildDayKey == stageContext.todayKey {
		var err error
		mode, accounts, err = w.rebuildAiPerformanceSummaryWindowsIncremental(ctx, tx)
		if err != nil {
			return mode, false, err
		}
		if mode == WindowRefreshModeNoop {
			remaining, err := usageWindowDirtyRemaining(ctx, tx, w.Dialect, "ai_performance_summary_dirty_system_accounts")
			if err != nil {
				return WindowRefreshModeNoop, false, err
			}
			return WindowRefreshModeNoop, remaining, nil
		}
	} else {
		if _, err := tx.ExecContext(ctx, w.Dialect.bind(`DELETE FROM `+w.Dialect.StatsTable("ai_performance_summary_windows"))); err != nil {
			return mode, false, err
		}
		scopes, err := w.usageOverviewSnapshotScopes(ctx, tx)
		if err != nil {
			return mode, false, err
		}
		uniqueSystemAccountIDs := map[string]struct{}{}
		for _, scope := range scopes {
			if scope.systemAccountID != GlobalStatsSystemAccountID {
				uniqueSystemAccountIDs[scope.systemAccountID] = struct{}{}
			}
		}
		accounts = append(mapKeyList(uniqueSystemAccountIDs), GlobalStatsSystemAccountID)
	}
	rows, err := w.aiPerformanceSummaryRows(ctx, tx, accounts, stageContext)
	if err != nil {
		return mode, false, err
	}
	aiInsert := `
		INSERT INTO ` + w.Dialect.StatsTable("ai_performance_summary_windows") + ` (
		  system_account_id, window_key, start_date, end_date, request_count, duration_ms_sum, duration_ms_count,
		  duration_ms_max, first_token_ms_sum, first_token_ms_count, first_token_ms_max, updated_at)
		VALUES `
	if err := execOverviewWindowInsertRows(ctx, tx, w.Dialect, aiInsert, 12, rows); err != nil {
		return mode, false, err
	}
	remaining, err := usageWindowDirtyRemaining(ctx, tx, w.Dialect, "ai_performance_summary_dirty_system_accounts")
	if err != nil {
		return mode, false, err
	}
	return mode, remaining, nil
}

// rebuildAiPerformanceSummaryWindowsIncremental 是同日增量轮：消费
// ai-performance 脏行并只重建受影响账户与 global 的窗口行，返回实际模式
// （incremental 或 noop）与重建账户清单（含 global，升序 global 末位）。
func (w *WindowRefresher) rebuildAiPerformanceSummaryWindowsIncremental(ctx context.Context, tx *sql.Tx) (string, []string, error) {
	claimed, err := w.claimAiPerformanceDirtyRows(ctx, tx)
	if err != nil {
		return WindowRefreshModeIncremental, nil, err
	}
	if len(claimed) == 0 {
		// 脏表为空：本轮 no-op，不 DELETE 任何窗口表行。
		return WindowRefreshModeNoop, nil, nil
	}
	consumed, err := w.consumeAiPerformanceDirtyRows(ctx, tx, claimed)
	if err != nil {
		return WindowRefreshModeIncremental, nil, err
	}
	// claimed ∩ totals：totals 已无对应行的账户不重建（孤儿清理负责删残留）。
	accounts := make([]string, 0, len(consumed))
	for _, row := range consumed {
		has, err := w.hasUsageStatsTotalsSystemAccount(ctx, tx, row.systemAccountID)
		if err != nil {
			return WindowRefreshModeIncremental, nil, err
		}
		if !has {
			continue
		}
		accounts = append(accounts, row.systemAccountID)
	}
	hasGlobal := false
	for _, account := range accounts {
		if account == GlobalStatsSystemAccountID {
			hasGlobal = true
		}
	}
	// 任一 claimed 非空即重建 global 行。
	if !hasGlobal {
		accounts = append(accounts, GlobalStatsSystemAccountID)
	}
	for _, systemAccountID := range accounts {
		if _, err := tx.ExecContext(ctx, w.Dialect.bind(`DELETE FROM `+w.Dialect.StatsTable("ai_performance_summary_windows")+` WHERE system_account_id = ?`), systemAccountID); err != nil {
			return WindowRefreshModeIncremental, nil, err
		}
	}
	if err := deleteOrphanUsageWindowRows(ctx, tx, w.Dialect, "ai_performance_summary_windows"); err != nil {
		return WindowRefreshModeIncremental, nil, err
	}
	return WindowRefreshModeIncremental, accounts, nil
}

// aiPerformanceSummaryRows 按 (account, range) 顺序构建 AI 性能摘要行
// （全量与增量共用；行集/行值/行序与逐行单条 INSERT 时代一致）。
func (w *WindowRefresher) aiPerformanceSummaryRows(ctx context.Context, tx *sql.Tx, systemAccountIDs []string, stageContext refreshStageContext) ([][]any, error) {
	ranges := FixedUsageStatsRanges(stageContext.todayKey)
	earliestDate := stageContext.todayKey
	if len(ranges) > 0 {
		earliestDate = ranges[0].StartDate
	}
	rows := make([][]any, 0, len(systemAccountIDs)*len(ranges))
	for _, systemAccountID := range systemAccountIDs {
		source, err := w.loadAiPerformanceSourceRows(ctx, tx, systemAccountID, earliestDate, stageContext.todayKey)
		if err != nil {
			return nil, err
		}
		rowsByDate := RowsByStatDate(source, func(r DailyWindowRow) string { return r.StatDate })
		for _, rangeValue := range ranges {
			aggregate := AggregateUsageRowsForRange(rowsByDate, rangeValue)
			rows = append(rows, []any{
				systemAccountID, RangeWindowKey(rangeValue), rangeValue.StartDate, rangeValue.EndDate,
				aggregate.RequestCount, aggregate.DurationMsSum, aggregate.DurationMsCount,
				aggregate.DurationMsMax, aggregate.FirstTokenMsSum, aggregate.FirstTokenMsCount, aggregate.FirstTokenMsMax,
				stageContext.updatedAt,
			})
		}
	}
	return rows, nil
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
		query := w.Dialect.bind(scopeRangeWindowInsertSQL(w.Dialect))
		if _, err := tx.ExecContext(ctx, query, rangeValue.StartDate, rangeValue.EndDate, stageContext.updatedAt, rangeValue.StartDate, rangeValue.EndDate); err != nil {
			return err
		}
	}
	return nil
}

func scopeRangeWindowInsertSQL(dialect Dialect) string {
	return `
		INSERT INTO ` + dialect.StatsTable("usage_scope_range_windows") + ` (
		  system_account_id, scope_type, scope_id, start_date, end_date,
		  request_count, success_count, error_count, input_tokens, output_tokens, cache_read_tokens,
		  cache_read_cost_usd, cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd, thinking_tokens, input_image_tokens, output_image_tokens,
		  input_audio_tokens, output_audio_tokens, tts_input_chars, audio_input_seconds, output_video_seconds,
		  total_cost_usd, duration_ms_sum, duration_ms_count, duration_ms_max,
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
		  COALESCE(SUM(input_audio_tokens), 0),
		  COALESCE(SUM(output_audio_tokens), 0),
		  COALESCE(SUM(tts_input_chars), 0),
		  COALESCE(SUM(audio_input_seconds), 0),
		  COALESCE(SUM(output_video_seconds), 0),
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
			  OR input_audio_tokens > 0
			  OR output_audio_tokens > 0
			  OR tts_input_chars > 0
			  OR audio_input_seconds > 0
			  OR output_video_seconds > 0
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
		  OR COALESCE(SUM(input_audio_tokens), 0) > 0
		  OR COALESCE(SUM(output_audio_tokens), 0) > 0
		  OR COALESCE(SUM(tts_input_chars), 0) > 0
		  OR COALESCE(SUM(audio_input_seconds), 0) > 0
		  OR COALESCE(SUM(output_video_seconds), 0) > 0
		  OR COALESCE(SUM(total_cost_usd), 0) > 0
	`
}

// authorizationRangeWindowInsertSQL 已删除：授权消耗明细读端改为按日范围直读
// authorization_team_usage_summary_daily / authorization_user_usage_summary_daily，
// 范围窗口表不再由 jobs 刷新（建表定义保留以兼容存量库）。

var _ = time.Time{}

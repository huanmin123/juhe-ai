package statsagg

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
)

// statsParamsTail mirrors usage-stats.repository.ts statsParamsTail：accumulator
// 按列顺序展开为参数，时间戳列以 ” 表示 NULL。
func statsParamsTail(stats UsageStatsAccumulator, updatedAt string) []any {
	var lastUsedAt any
	if stats.LastUsedAt != "" {
		lastUsedAt = stats.LastUsedAt
	}
	var lastErrorAt any
	if stats.LastErrorAt != "" {
		lastErrorAt = stats.LastErrorAt
	}
	return []any{
		stats.RequestCount, stats.SuccessCount, stats.ErrorCount,
		stats.InputTokens, stats.OutputTokens,
		stats.CacheReadTokens, stats.CacheReadCostUsd,
		stats.CacheWriteTokens, stats.CacheWrite1hTokens, stats.CacheWriteCostUsd,
		stats.ThinkingTokens, stats.InputImageTokens, stats.OutputImageTokens,
		stats.TotalCostUsd,
		stats.DurationMsSum, stats.DurationMsCount, stats.DurationMsMax,
		stats.FirstTokenMsSum, stats.FirstTokenMsCount, stats.FirstTokenMsMax,
		lastUsedAt, lastErrorAt, updatedAt,
	}
}

const usageStatsMetricColumns = `
	request_count, success_count, error_count,
	input_tokens, output_tokens,
	cache_read_tokens, cache_read_cost_usd,
	cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd,
	thinking_tokens, input_image_tokens, output_image_tokens,
	total_cost_usd,
	duration_ms_sum, duration_ms_count, duration_ms_max,
	first_token_ms_sum, first_token_ms_count, first_token_ms_max,
	last_used_at, last_error_at, updated_at
`

// upsertDeltaExpr 生成 `表.列 + EXCLUDED.列` 增量合并表达式（对齐 Node
// upsertPostgresUsageStatsTotals 的 ON CONFLICT SET 列表）。
func upsertDeltaExpr(target string) []string {
	columns := []string{
		"request_count", "success_count", "error_count",
		"input_tokens", "output_tokens",
		"cache_read_tokens", "cache_read_cost_usd",
		"cache_write_tokens", "cache_write_1h_tokens", "cache_write_cost_usd",
		"thinking_tokens", "input_image_tokens", "output_image_tokens",
		"total_cost_usd",
		"duration_ms_sum", "duration_ms_count",
		"first_token_ms_sum", "first_token_ms_count",
	}
	exprs := make([]string, 0, len(columns)+4)
	for _, column := range columns {
		exprs = append(exprs, fmt.Sprintf("%s = %s.%s + excluded.%s", column, target, column, column))
	}
	exprs = append(exprs,
		fmt.Sprintf("duration_ms_max = CASE WHEN %s.duration_ms_max > excluded.duration_ms_max THEN %s.duration_ms_max ELSE excluded.duration_ms_max END", target, target),
		fmt.Sprintf("first_token_ms_max = CASE WHEN %s.first_token_ms_max > excluded.first_token_ms_max THEN %s.first_token_ms_max ELSE excluded.first_token_ms_max END", target, target),
		fmt.Sprintf(`last_used_at = CASE WHEN excluded.last_used_at IS NULL THEN %[1]s.last_used_at WHEN %[1]s.last_used_at IS NULL OR excluded.last_used_at > %[1]s.last_used_at THEN excluded.last_used_at ELSE %[1]s.last_used_at END`, target),
		fmt.Sprintf(`last_error_at = CASE WHEN excluded.last_error_at IS NULL THEN %[1]s.last_error_at WHEN %[1]s.last_error_at IS NULL OR excluded.last_error_at > %[1]s.last_error_at THEN excluded.last_error_at ELSE %[1]s.last_error_at END`, target),
		"updated_at = excluded.updated_at",
	)
	return exprs
}

// upsertUsageStatsTotals mirrors upsertPostgresUsageStatsTotals。
func (a *Aggregator) upsertUsageStatsTotals(ctx context.Context, tx *sql.Tx, entries map[statsTotalsKey]*UsageStatsAccumulator, updatedAt string) error {
	if len(entries) == 0 {
		return nil
	}
	// Node 按批内 Map 插入序写库；Go map 序随机，但 UPSERT 键互不相交，
	// 结果等价。排序保证确定性行序，便于测试与审计。
	keys := make([]statsTotalsKey, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].SystemAccountID != keys[j].SystemAccountID {
			return keys[i].SystemAccountID < keys[j].SystemAccountID
		}
		if keys[i].ScopeType != keys[j].ScopeType {
			return keys[i].ScopeType < keys[j].ScopeType
		}
		return keys[i].ScopeID < keys[j].ScopeID
	})
	for _, key := range keys {
		accumulator := *entries[key]
		query := a.Dialect.bind(`
			INSERT INTO ` + a.Dialect.StatsTable("usage_stats_totals") + ` (
			  system_account_id, scope_type, scope_id, ` + usageStatsMetricColumns + `)
			VALUES (?, ?, ?, ` + placeholders(23) + `)
			ON CONFLICT(system_account_id, scope_type, scope_id) DO UPDATE SET
			  ` + joinExprs(upsertDeltaExpr(a.Dialect.qualifiedTarget("usage_stats_totals"))) + `
		`)
		args := []any{key.SystemAccountID, key.ScopeType, key.ScopeID}
		args = append(args, statsParamsTail(accumulator, updatedAt)...)
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return err
		}
	}
	return nil
}

// upsertUsageStatsTimeBucket mirrors upsertPostgresUsageStatsTimeBucket。
func (a *Aggregator) upsertUsageStatsTimeBucket(ctx context.Context, tx *sql.Tx, bucket TimeBucketDefinition, entries []*aggregatedTimeEntry, updatedAt string) error {
	if len(entries) == 0 {
		return nil
	}
	sorted := make([]*aggregatedTimeEntry, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool {
		left := sorted[i].SystemAccountID + "\x00" + sorted[i].ScopeType + "\x00" + sorted[i].ScopeID + "\x00" + sorted[i].TimeValue
		right := sorted[j].SystemAccountID + "\x00" + sorted[j].ScopeType + "\x00" + sorted[j].ScopeID + "\x00" + sorted[j].TimeValue
		return left < right
	})
	target := a.Dialect.qualifiedTarget(bucket.TableName)
	conflictColumn := bucket.ColumnName
	for _, entry := range sorted {
		query := a.Dialect.bind(`
			INSERT INTO ` + a.Dialect.StatsTable(bucket.TableName) + ` (
			  system_account_id, scope_type, scope_id, ` + conflictColumn + `, ` + usageStatsMetricColumns + `)
			VALUES (?, ?, ?, ?, ` + placeholders(23) + `)
			ON CONFLICT(system_account_id, scope_type, scope_id, ` + conflictColumn + `) DO UPDATE SET
			  ` + joinExprs(upsertDeltaExpr(target)) + `
		`)
		args := []any{entry.SystemAccountID, entry.ScopeType, entry.ScopeID, entry.TimeValue}
		args = append(args, statsParamsTail(entry.Accumulator, updatedAt)...)
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return err
		}
	}
	return nil
}

// upsertUsageLatencyEntries mirrors upsertPostgresUsageLatencyEntries。
func (a *Aggregator) upsertUsageLatencyEntries(ctx context.Context, tx *sql.Tx, entries map[latencyEntryKey]*AggregatedLatencyEntry, updatedAt string) error {
	if len(entries) == 0 {
		return nil
	}
	byTable := map[string][]*AggregatedLatencyEntry{}
	for _, entry := range entries {
		byTable[entry.Bucket.TableName] = append(byTable[entry.Bucket.TableName], entry)
	}
	tableNames := make([]string, 0, len(byTable))
	for tableName := range byTable {
		tableNames = append(tableNames, tableName)
	}
	sort.Strings(tableNames)
	for _, tableName := range tableNames {
		tableEntries := byTable[tableName]
		bucket := tableEntries[0].Bucket
		target := a.Dialect.qualifiedTarget(tableName)
		sort.Slice(tableEntries, func(i, j int) bool {
			return tableEntries[i].TimeValue+"\x00"+tableEntries[i].SystemAccountID < tableEntries[j].TimeValue+"\x00"+tableEntries[j].SystemAccountID
		})
		for _, entry := range tableEntries {
			query := a.Dialect.bind(`
				INSERT INTO ` + a.Dialect.StatsTable(tableName) + ` (
				  system_account_id, scope_type, scope_id, metric_type, ` + bucket.ColumnName + `, bucket_upper_bound_ms, sample_count, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT(system_account_id, scope_type, scope_id, metric_type, ` + bucket.ColumnName + `, bucket_upper_bound_ms) DO UPDATE SET
				  sample_count = ` + target + `.sample_count + excluded.sample_count,
				  updated_at = excluded.updated_at
			`)
			if _, err := tx.ExecContext(ctx, query,
				entry.SystemAccountID, entry.ScopeType, entry.ScopeID,
				string(entry.MetricType), entry.TimeValue, entry.BucketUpperBoundMs, entry.SampleCount, updatedAt); err != nil {
				return err
			}
		}
	}
	return nil
}

// upsertUsageModelEntries mirrors upsertPostgresUsageModelEntries。
func (a *Aggregator) upsertUsageModelEntries(ctx context.Context, tx *sql.Tx, entries map[statsModelKey]*aggregatedUsageModelEntry, updatedAt string) error {
	if len(entries) == 0 {
		return nil
	}
	byTable := map[string][]*aggregatedUsageModelEntry{}
	for _, entry := range entries {
		byTable[entry.Bucket.TableName] = append(byTable[entry.Bucket.TableName], entry)
	}
	tableNames := make([]string, 0, len(byTable))
	for tableName := range byTable {
		tableNames = append(tableNames, tableName)
	}
	sort.Strings(tableNames)
	for _, tableName := range tableNames {
		tableEntries := byTable[tableName]
		bucket := tableEntries[0].Bucket
		target := a.Dialect.qualifiedTarget(tableName)
		sort.Slice(tableEntries, func(i, j int) bool {
			return modelEntrySortKey(tableEntries[i]) < modelEntrySortKey(tableEntries[j])
		})
		for _, entry := range tableEntries {
			stats := entry.Accumulator
			query := a.Dialect.bind(`
				INSERT INTO ` + a.Dialect.StatsTable(tableName) + ` (
				  system_account_id, ` + bucket.ColumnName + `, provider_code, model,
				  request_count, success_count, error_count,
				  input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd,
				  cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd,
				  thinking_tokens, input_image_tokens, output_image_tokens, total_cost_usd, updated_at)
				VALUES (?, ?, ?, ?, ` + placeholders(15) + `)
				ON CONFLICT(system_account_id, ` + bucket.ColumnName + `, provider_code, model) DO UPDATE SET
				  request_count = ` + target + `.request_count + excluded.request_count,
				  success_count = ` + target + `.success_count + excluded.success_count,
				  error_count = ` + target + `.error_count + excluded.error_count,
				  input_tokens = ` + target + `.input_tokens + excluded.input_tokens,
				  output_tokens = ` + target + `.output_tokens + excluded.output_tokens,
				  cache_read_tokens = ` + target + `.cache_read_tokens + excluded.cache_read_tokens,
				  cache_read_cost_usd = ` + target + `.cache_read_cost_usd + excluded.cache_read_cost_usd,
				  cache_write_tokens = ` + target + `.cache_write_tokens + excluded.cache_write_tokens,
				  cache_write_1h_tokens = ` + target + `.cache_write_1h_tokens + excluded.cache_write_1h_tokens,
				  cache_write_cost_usd = ` + target + `.cache_write_cost_usd + excluded.cache_write_cost_usd,
				  thinking_tokens = ` + target + `.thinking_tokens + excluded.thinking_tokens,
				  input_image_tokens = ` + target + `.input_image_tokens + excluded.input_image_tokens,
				  output_image_tokens = ` + target + `.output_image_tokens + excluded.output_image_tokens,
				  total_cost_usd = ` + target + `.total_cost_usd + excluded.total_cost_usd,
				  updated_at = excluded.updated_at
			`)
			if _, err := tx.ExecContext(ctx, query,
				entry.SystemAccountID, entry.TimeValue, entry.ProviderCode, entry.Model,
				stats.RequestCount, stats.SuccessCount, stats.ErrorCount,
				stats.InputTokens, stats.OutputTokens, stats.CacheReadTokens, stats.CacheReadCostUsd,
				stats.CacheWriteTokens, stats.CacheWrite1hTokens, stats.CacheWriteCostUsd,
				stats.ThinkingTokens, stats.InputImageTokens, stats.OutputImageTokens, stats.TotalCostUsd,
				updatedAt); err != nil {
				return err
			}
		}
	}
	return nil
}

func modelEntrySortKey(entry *aggregatedUsageModelEntry) string {
	return entry.TimeValue + "\x00" + entry.SystemAccountID + "\x00" + entry.ProviderCode + "\x00" + entry.Model
}

// upsertUsageErrorEntries mirrors upsertPostgresUsageErrorEntries。
func (a *Aggregator) upsertUsageErrorEntries(ctx context.Context, tx *sql.Tx, entries map[statsErrorKey]*aggregatedUsageErrorEntry, updatedAt string) error {
	if len(entries) == 0 {
		return nil
	}
	byTable := map[string][]*aggregatedUsageErrorEntry{}
	for _, entry := range entries {
		byTable[entry.Bucket.TableName] = append(byTable[entry.Bucket.TableName], entry)
	}
	tableNames := make([]string, 0, len(byTable))
	for tableName := range byTable {
		tableNames = append(tableNames, tableName)
	}
	sort.Strings(tableNames)
	for _, tableName := range tableNames {
		tableEntries := byTable[tableName]
		bucket := tableEntries[0].Bucket
		target := a.Dialect.qualifiedTarget(tableName)
		sort.Slice(tableEntries, func(i, j int) bool {
			return errorEntrySortKey(tableEntries[i]) < errorEntrySortKey(tableEntries[j])
		})
		for _, entry := range tableEntries {
			var errorMessage any
			if entry.ErrorMessage != nil {
				errorMessage = *entry.ErrorMessage
			}
			query := a.Dialect.bind(`
				INSERT INTO ` + a.Dialect.StatsTable(tableName) + ` (
				  system_account_id, ` + bucket.ColumnName + `, error_group, provider_code, error_code, status_code, error_message,
				  request_count, error_count, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT(system_account_id, ` + bucket.ColumnName + `, error_group, provider_code, error_code, status_code) DO UPDATE SET
				  error_message = COALESCE(excluded.error_message, ` + target + `.error_message),
				  request_count = ` + target + `.request_count + excluded.request_count,
				  error_count = ` + target + `.error_count + excluded.error_count,
				  updated_at = excluded.updated_at
			`)
			if _, err := tx.ExecContext(ctx, query,
				entry.SystemAccountID, entry.TimeValue, entry.ErrorGroup, entry.ProviderCode, entry.ErrorCode, entry.StatusCode,
				errorMessage, entry.RequestCount, entry.ErrorCount, updatedAt); err != nil {
				return err
			}
		}
	}
	return nil
}

func errorEntrySortKey(entry *aggregatedUsageErrorEntry) string {
	return entry.TimeValue + "\x00" + entry.SystemAccountID + "\x00" + entry.ErrorGroup + "\x00" + entry.ProviderCode + "\x00" + entry.ErrorCode + "\x00" + fmt.Sprintf("%v", entry.StatusCode)
}

// upsertAccountQualityEntries mirrors upsertPostgresAccountQualityEntries
// （含 account_quality_dirty_accounts 标记）。
func (a *Aggregator) upsertAccountQualityEntries(ctx context.Context, tx *sql.Tx, entries map[string]*aggregatedAccountQualityEntry, updatedAt string) error {
	if len(entries) == 0 {
		return nil
	}
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		entry := entries[key]
		var lastSuccessAt, lastErrorAt, lastErrorMessage any
		if entry.LastSuccessAt != "" {
			lastSuccessAt = entry.LastSuccessAt
		}
		if entry.LastErrorAt != "" {
			lastErrorAt = entry.LastErrorAt
		}
		if entry.LastErrorMessage != "" {
			lastErrorMessage = entry.LastErrorMessage
		}
		query := a.Dialect.bind(`
			INSERT INTO ` + a.Dialect.StatsTable("account_quality_minute_stats") + ` (
			  account_id, system_account_id, provider_code, stat_minute,
			  request_count, success_count, error_count, first_token_ms_sum, first_token_ms_count,
			  last_sample_at, last_success_at, last_error_at, last_error_message, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(account_id, stat_minute) DO UPDATE SET
			  system_account_id = excluded.system_account_id,
			  provider_code = excluded.provider_code,
			  request_count = ` + a.Dialect.qualifiedTarget("account_quality_minute_stats") + `.request_count + excluded.request_count,
			  success_count = ` + a.Dialect.qualifiedTarget("account_quality_minute_stats") + `.success_count + excluded.success_count,
			  error_count = ` + a.Dialect.qualifiedTarget("account_quality_minute_stats") + `.error_count + excluded.error_count,
			  first_token_ms_sum = ` + a.Dialect.qualifiedTarget("account_quality_minute_stats") + `.first_token_ms_sum + excluded.first_token_ms_sum,
			  first_token_ms_count = ` + a.Dialect.qualifiedTarget("account_quality_minute_stats") + `.first_token_ms_count + excluded.first_token_ms_count,
			  last_sample_at = CASE WHEN ` + a.Dialect.qualifiedTarget("account_quality_minute_stats") + `.last_sample_at IS NULL OR excluded.last_sample_at > ` + a.Dialect.qualifiedTarget("account_quality_minute_stats") + `.last_sample_at THEN excluded.last_sample_at ELSE ` + a.Dialect.qualifiedTarget("account_quality_minute_stats") + `.last_sample_at END,
			  last_success_at = CASE WHEN excluded.last_success_at IS NULL THEN ` + a.Dialect.qualifiedTarget("account_quality_minute_stats") + `.last_success_at WHEN ` + a.Dialect.qualifiedTarget("account_quality_minute_stats") + `.last_success_at IS NULL OR excluded.last_success_at > ` + a.Dialect.qualifiedTarget("account_quality_minute_stats") + `.last_success_at THEN excluded.last_success_at ELSE ` + a.Dialect.qualifiedTarget("account_quality_minute_stats") + `.last_success_at END,
			  last_error_at = CASE WHEN excluded.last_error_at IS NULL THEN ` + a.Dialect.qualifiedTarget("account_quality_minute_stats") + `.last_error_at WHEN ` + a.Dialect.qualifiedTarget("account_quality_minute_stats") + `.last_error_at IS NULL OR excluded.last_error_at > ` + a.Dialect.qualifiedTarget("account_quality_minute_stats") + `.last_error_at THEN excluded.last_error_at ELSE ` + a.Dialect.qualifiedTarget("account_quality_minute_stats") + `.last_error_at END,
			  last_error_message = CASE WHEN excluded.last_error_at IS NULL THEN ` + a.Dialect.qualifiedTarget("account_quality_minute_stats") + `.last_error_message WHEN ` + a.Dialect.qualifiedTarget("account_quality_minute_stats") + `.last_error_at IS NULL OR excluded.last_error_at >= ` + a.Dialect.qualifiedTarget("account_quality_minute_stats") + `.last_error_at THEN excluded.last_error_message ELSE ` + a.Dialect.qualifiedTarget("account_quality_minute_stats") + `.last_error_message END,
			  updated_at = excluded.updated_at
		`)
		if _, err := tx.ExecContext(ctx, query,
			entry.AccountID, entry.SystemAccountID, entry.ProviderCode, entry.StatMinute,
			entry.RequestCount, entry.SuccessCount, entry.ErrorCount,
			entry.FirstTokenMsSum, entry.FirstTokenMsCount,
			entry.LastSampleAt, lastSuccessAt, lastErrorAt, lastErrorMessage, updatedAt); err != nil {
			return err
		}
	}
	accountIDs := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.AccountID != "" {
			accountIDs = append(accountIDs, entry.AccountID)
		}
	}
	sort.Strings(accountIDs)
	for _, accountID := range accountIDs {
		query := a.Dialect.bind(`
			INSERT INTO ` + a.Dialect.StatsTable("account_quality_dirty_accounts") + ` (account_id, first_dirty_at, updated_at)
			VALUES (?, ?, ?)
			ON CONFLICT(account_id) DO UPDATE SET updated_at = excluded.updated_at
		`)
		if _, err := tx.ExecContext(ctx, query, accountID, updatedAt, updatedAt); err != nil {
			return err
		}
	}
	return nil
}

// upsertAccountHealthEntries mirrors upsertPostgresAccountHealthEntries：
// 仅当 observed 更新（last_observed_at/last_record_id 更大）时覆盖。
func (a *Aggregator) upsertAccountHealthEntries(ctx context.Context, tx *sql.Tx, entries map[string]*aggregatedAccountHealthEntry, updatedAt string) error {
	if len(entries) == 0 {
		return nil
	}
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		entry := entries[key]
		var statusCode, errorCode, errorMessage any
		if entry.StatusCode != nil {
			statusCode = *entry.StatusCode
		}
		if entry.ErrorCode != nil {
			errorCode = *entry.ErrorCode
		}
		if entry.ErrorMessage != nil {
			errorMessage = *entry.ErrorMessage
		}
		target := a.Dialect.qualifiedTarget("account_health_hourly")
		query := a.Dialect.bind(`
			INSERT INTO ` + a.Dialect.StatsTable("account_health_hourly") + ` (
			  account_id, system_account_id, provider_code, stat_hour, status,
			  last_observed_at, last_record_id, status_code, error_code, error_message, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(account_id, stat_hour) DO UPDATE SET
			  system_account_id = excluded.system_account_id,
			  provider_code = excluded.provider_code,
			  status = excluded.status,
			  last_observed_at = excluded.last_observed_at,
			  last_record_id = excluded.last_record_id,
			  status_code = excluded.status_code,
			  error_code = excluded.error_code,
			  error_message = excluded.error_message,
			  updated_at = excluded.updated_at
			WHERE excluded.last_observed_at > ` + target + `.last_observed_at
			   OR (excluded.last_observed_at = ` + target + `.last_observed_at AND excluded.last_record_id > ` + target + `.last_record_id)
		`)
		if _, err := tx.ExecContext(ctx, query,
			entry.AccountID, entry.SystemAccountID, entry.ProviderCode, entry.StatHour, entry.Status,
			entry.LastObservedAt, entry.LastRecordID, statusCode, errorCode, errorMessage, updatedAt); err != nil {
			return err
		}
	}
	return nil
}

func placeholders(count int) string {
	result := ""
	for index := 0; index < count; index++ {
		if index > 0 {
			result += ", "
		}
		result += "?"
	}
	return result
}

func joinExprs(exprs []string) string {
	result := ""
	for index, expr := range exprs {
		if index > 0 {
			result += ",\n\t\t  "
		}
		result += expr
	}
	return result
}

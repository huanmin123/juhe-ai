package cleanuprepo

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsagg"
)

// usage-stats-writers.ts subtractUsageStatsRecord 及其子 writer 的 SQLite 移植
// （usage stats totals/time buckets/latency/model/error/auth-daily/
// account-quality/account-health），加上 stats writer 的记录清理结算入口
// （CleanupDeletedApiKeyRecordStatsData / CleanupDeletedAccountRecordStatsData）
// 与 account_usage_snapshots upsert。行对象在 Node 侧经 IPC JSON 序列化为
// snake_case map，这里提供双向转换保持同一契约。

// usageStatsRecordSelectColumns 照 USAGE_STATS_RECORD_SELECT_COLUMNS（顺序一致）。
const usageStatsRecordSelectColumns = `
  id, system_account_id, trace_id, traffic_source, client_ip, api_key_id, group_id,
  account_id, endpoint, provider_code, provider_protocol_profile_id, model, status_code,
  success, failure_attribution, first_token_ms, duration_ms, input_tokens, output_tokens,
  cache_read_tokens, cache_read_cost_usd, cache_write_tokens, cache_write_1h_tokens,
  cache_write_cost_usd, thinking_tokens, input_image_tokens, output_image_tokens, cost_usd,
  error_code, error_message, account_owner_system_account_id, group_owner_system_account_id,
  account_access_type, group_access_type, account_authorization_id, account_authorization_source_type,
  account_authorization_source_team_id, group_authorization_id, group_authorization_source_type,
  group_authorization_source_team_id, created_at
`

// scanUsageStatsRecordRows 照 statsagg 的行扫描（列序与 select 列一致）。
func scanUsageStatsRecordRows(rows *sql.Rows) ([]statsagg.UsageStatsRecordRow, error) {
	defer rows.Close()
	var output []statsagg.UsageStatsRecordRow
	for rows.Next() {
		row := &statsagg.UsageStatsRecordRow{}
		scan := []any{
			&row.ID, &row.SystemAccountID, &row.TraceID, &row.TrafficSource,
			&row.ClientIP, &row.APIKeyID, &row.GroupID, &row.AccountID, &row.Endpoint,
			&row.ProviderCode, &row.ProviderProtocolProfileID, &row.Model,
			&row.StatusCode, &row.Success, &row.FailureAttribution,
			&row.FirstTokenMs, &row.DurationMs,
			&row.InputTokens, &row.OutputTokens,
			&row.CacheReadTokens, &row.CacheReadCostUsd,
			&row.CacheWriteTokens, &row.CacheWrite1hTokens, &row.CacheWriteCostUsd,
			&row.ThinkingTokens, &row.InputImageTokens, &row.OutputImageTokens,
			&row.CostUsd, &row.ErrorCode, &row.ErrorMessage,
			&row.AccountOwnerSystemAccountID, &row.GroupOwnerSystemAccountID,
			&row.AccountAccessType, &row.GroupAccessType,
			&row.AccountAuthorizationID, &row.AccountAuthorizationSourceType, &row.AccountAuthorizationSourceTeamID,
			&row.GroupAuthorizationID, &row.GroupAuthorizationSourceType, &row.GroupAuthorizationSourceTeamID,
			&row.CreatedAt,
		}
		if err := rows.Scan(scan...); err != nil {
			return nil, err
		}
		output = append(output, *row)
	}
	return output, rows.Err()
}

// shardUsageRowsToMaps 把分片行转换为 Node IPC JSON 同构的 snake_case map。
func shardUsageRowsToMaps(rows []shardUsageRow) []map[string]any {
	if len(rows) == 0 {
		return nil
	}
	output := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		output = append(output, statsaggRowToMap(row.UsageStatsRecordRow, row.SourceShardKey))
	}
	return output
}

func optionalNumberPtr(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

func optionalTextPtr(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

// statsaggRowToMap 输出 Node UsageStatsRecordRow JSON 形状。
func statsaggRowToMap(row statsagg.UsageStatsRecordRow, sourceShardKey string) map[string]any {
	return map[string]any{
		"id": row.ID, "system_account_id": row.SystemAccountID, "trace_id": row.TraceID,
		"traffic_source": row.TrafficSource, "client_ip": optionalTextPtr(row.ClientIP),
		"api_key_id": optionalTextPtr(row.APIKeyID), "group_id": optionalTextPtr(row.GroupID),
		"account_id": optionalTextPtr(row.AccountID), "endpoint": optionalTextPtr(row.Endpoint),
		"provider_code":                optionalTextPtr(row.ProviderCode),
		"provider_protocol_profile_id": optionalTextPtr(row.ProviderProtocolProfileID),
		"model":                        optionalTextPtr(row.Model),
		"status_code":                  optionalNumberPtr(row.StatusCode), "success": row.Success,
		"failure_attribution": optionalTextPtr(row.FailureAttribution),
		"first_token_ms":      optionalNumberPtr(row.FirstTokenMs), "duration_ms": optionalNumberPtr(row.DurationMs),
		"input_tokens": optionalNumberPtr(row.InputTokens), "output_tokens": optionalNumberPtr(row.OutputTokens),
		"cache_read_tokens": optionalNumberPtr(row.CacheReadTokens), "cache_read_cost_usd": optionalNumberPtr(row.CacheReadCostUsd),
		"cache_write_tokens": optionalNumberPtr(row.CacheWriteTokens), "cache_write_1h_tokens": optionalNumberPtr(row.CacheWrite1hTokens),
		"cache_write_cost_usd": optionalNumberPtr(row.CacheWriteCostUsd), "thinking_tokens": optionalNumberPtr(row.ThinkingTokens),
		"input_image_tokens": optionalNumberPtr(row.InputImageTokens), "output_image_tokens": optionalNumberPtr(row.OutputImageTokens),
		"cost_usd": optionalNumberPtr(row.CostUsd), "error_code": optionalTextPtr(row.ErrorCode),
		"error_message":                        optionalTextPtr(row.ErrorMessage),
		"account_owner_system_account_id":      optionalTextPtr(row.AccountOwnerSystemAccountID),
		"group_owner_system_account_id":        optionalTextPtr(row.GroupOwnerSystemAccountID),
		"account_access_type":                  optionalTextPtr(row.AccountAccessType),
		"group_access_type":                    optionalTextPtr(row.GroupAccessType),
		"account_authorization_id":             optionalTextPtr(row.AccountAuthorizationID),
		"account_authorization_source_type":    optionalTextPtr(row.AccountAuthorizationSourceType),
		"account_authorization_source_team_id": optionalTextPtr(row.AccountAuthorizationSourceTeamID),
		"group_authorization_id":               optionalTextPtr(row.GroupAuthorizationID),
		"group_authorization_source_type":      optionalTextPtr(row.GroupAuthorizationSourceType),
		"group_authorization_source_team_id":   optionalTextPtr(row.GroupAuthorizationSourceTeamID),
		"created_at":                           row.CreatedAt,
		"source_shard_key":                     sourceShardKey,
	}
}

func mapString(value map[string]any, key string) *string {
	raw, ok := value[key]
	if !ok || raw == nil {
		return nil
	}
	switch typed := raw.(type) {
	case string:
		if typed == "" {
			return nil
		}
		return &typed
	case []byte:
		text := string(typed)
		if text == "" {
			return nil
		}
		return &text
	}
	return nil
}

func mapNumber(value map[string]any, key string) *float64 {
	raw, ok := value[key]
	if !ok || raw == nil {
		return nil
	}
	switch typed := raw.(type) {
	case float64:
		return &typed
	case float32:
		out := float64(typed)
		return &out
	case int64:
		out := float64(typed)
		return &out
	case int:
		out := float64(typed)
		return &out
	case []byte:
		var out float64
		if _, err := fmt.Sscanf(strings.TrimSpace(string(typed)), "%g", &out); err == nil {
			return &out
		}
	case string:
		var out float64
		if _, err := fmt.Sscanf(strings.TrimSpace(typed), "%g", &out); err == nil {
			return &out
		}
	}
	return nil
}

// statsaggRowFromMap 照 Node IPC JSON 反序列化后的行读取。
func statsaggRowFromMap(value map[string]any) (statsagg.UsageStatsRecordRow, error) {
	row := statsagg.UsageStatsRecordRow{}
	row.ID, _ = value["id"].(string)
	systemAccountID, _ := value["system_account_id"].(string)
	row.SystemAccountID = systemAccountID
	row.TraceID, _ = value["trace_id"].(string)
	row.TrafficSource, _ = value["traffic_source"].(string)
	row.ClientIP = mapString(value, "client_ip")
	row.APIKeyID = mapString(value, "api_key_id")
	row.GroupID = mapString(value, "group_id")
	row.AccountID = mapString(value, "account_id")
	row.Endpoint = mapString(value, "endpoint")
	row.ProviderCode = mapString(value, "provider_code")
	row.ProviderProtocolProfileID = mapString(value, "provider_protocol_profile_id")
	row.Model = mapString(value, "model")
	row.StatusCode = mapNumber(value, "status_code")
	success := mapNumber(value, "success")
	if success != nil {
		row.Success = *success
	}
	row.FailureAttribution = mapString(value, "failure_attribution")
	row.FirstTokenMs = mapNumber(value, "first_token_ms")
	row.DurationMs = mapNumber(value, "duration_ms")
	row.InputTokens = mapNumber(value, "input_tokens")
	row.OutputTokens = mapNumber(value, "output_tokens")
	row.CacheReadTokens = mapNumber(value, "cache_read_tokens")
	row.CacheReadCostUsd = mapNumber(value, "cache_read_cost_usd")
	row.CacheWriteTokens = mapNumber(value, "cache_write_tokens")
	row.CacheWrite1hTokens = mapNumber(value, "cache_write_1h_tokens")
	row.CacheWriteCostUsd = mapNumber(value, "cache_write_cost_usd")
	row.ThinkingTokens = mapNumber(value, "thinking_tokens")
	row.InputImageTokens = mapNumber(value, "input_image_tokens")
	row.OutputImageTokens = mapNumber(value, "output_image_tokens")
	row.CostUsd = mapNumber(value, "cost_usd")
	row.ErrorCode = mapString(value, "error_code")
	row.ErrorMessage = mapString(value, "error_message")
	row.AccountOwnerSystemAccountID = mapString(value, "account_owner_system_account_id")
	row.GroupOwnerSystemAccountID = mapString(value, "group_owner_system_account_id")
	row.AccountAccessType = mapString(value, "account_access_type")
	row.GroupAccessType = mapString(value, "group_access_type")
	row.AccountAuthorizationID = mapString(value, "account_authorization_id")
	row.AccountAuthorizationSourceType = mapString(value, "account_authorization_source_type")
	row.AccountAuthorizationSourceTeamID = mapString(value, "account_authorization_source_team_id")
	row.GroupAuthorizationID = mapString(value, "group_authorization_id")
	row.GroupAuthorizationSourceType = mapString(value, "group_authorization_source_type")
	row.GroupAuthorizationSourceTeamID = mapString(value, "group_authorization_source_team_id")
	createdAt, _ := value["created_at"].(string)
	row.CreatedAt = createdAt
	if sourceShardKey, ok := value["source_shard_key"].(string); ok {
		row.SourceShardKey = sourceShardKey
	}
	return row, nil
}

// subtractUsageStatsRecord 照 subtractUsageStatsRecord（SQLite；时间键按业务时区）。
func (s *RecordCleanupStore) subtractUsageStatsRecord(ctx context.Context, tx *sql.Tx, row statsagg.UsageStatsRecordRow, updatedAt string, timezone *time.Location) error {
	if !statsagg.ShouldAggregateUsageStatsRecord(row) {
		return nil
	}
	timeKeys, err := statsagg.UsageStatsTimeKeysFor(row.CreatedAt, timezone)
	if err != nil {
		return err
	}
	for _, entry := range statsagg.UsageStatsEntries(row, nil) {
		if err := s.subtractStatsTotalsAndBuckets(ctx, tx, entry, timeKeys, updatedAt); err != nil {
			return err
		}
		if err := s.subtractLatencyEntry(ctx, tx, entry, row, timeKeys, updatedAt); err != nil {
			return err
		}
	}
	if err := s.subtractAuthorizationUsageReportRows(ctx, tx, row, timeKeys.StatDate, updatedAt); err != nil {
		return err
	}
	if err := s.subtractModelBuckets(ctx, tx, row, timeKeys, updatedAt); err != nil {
		return err
	}
	if row.Success != 1 {
		if err := s.subtractErrorBuckets(ctx, tx, row, timeKeys, updatedAt); err != nil {
			return err
		}
	}
	if shouldRecordAccountQualityStats(row) {
		if err := s.subtractAccountQualityMinuteStats(ctx, tx, row, updatedAt, timezone); err != nil {
			return err
		}
	}
	if err := s.deleteAccountHealthHourForRecord(ctx, tx, row); err != nil {
		return err
	}
	return nil
}

func shouldRecordAccountQualityStats(row statsagg.UsageStatsRecordRow) bool {
	switch row.TrafficSource {
	case "runtime_recovery_probe", "cooldown_retest", "hybrid_scoring", "hybrid_quality_scoring":
		return false
	}
	if row.Success == 1 {
		return true
	}
	return row.FailureAttribution != nil &&
		(*row.FailureAttribution == "account_upstream" || *row.FailureAttribution == "account_dependency")
}

func (s *RecordCleanupStore) subtractStatsTotalsAndBuckets(ctx context.Context, tx *sql.Tx, entry statsagg.UsageStatsEntry, timeKeys statsagg.UsageStatsTimeKeys, updatedAt string) error {
	params := statsSubtractParams(entry.Accumulator)
	where := s.Stats.Bind("WHERE system_account_id = ? AND scope_type = ? AND scope_id = ?")
	// totals
	totalUpdate := fmt.Sprintf(`
    UPDATE usage_stats_totals
    SET %s,
        last_used_at = CASE WHEN request_count <= ? THEN NULL ELSE last_used_at END,
        last_error_at = CASE WHEN error_count <= ? THEN NULL ELSE last_error_at END,
        updated_at = ?
    %s
	`, statsSubtractSetExpr(""), where)
	args := append(append([]any{}, params...), entry.Accumulator.RequestCount, entry.Accumulator.ErrorCount, updatedAt,
		entry.SystemAccountID, entry.ScopeType, entry.ScopeID)
	if _, err := tx.ExecContext(ctx, totalUpdate, args...); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
    DELETE FROM usage_stats_totals
    %s
      AND request_count = 0 AND success_count = 0 AND error_count = 0
      AND input_tokens = 0 AND output_tokens = 0 AND cache_read_tokens = 0 AND cache_read_cost_usd = 0
      AND cache_write_tokens = 0 AND cache_write_1h_tokens = 0 AND cache_write_cost_usd = 0
      AND thinking_tokens = 0 AND input_image_tokens = 0 AND output_image_tokens = 0 AND total_cost_usd = 0
	`, where), entry.SystemAccountID, entry.ScopeType, entry.ScopeID); err != nil {
		return err
	}
	// time buckets
	for _, bucket := range usageStatsBucketDefs {
		column := bucket.ColumnName
		bucketUpdate := fmt.Sprintf(`
      UPDATE %s
      SET %s,
          duration_ms_max = CASE WHEN duration_ms_count <= ? THEN 0 ELSE duration_ms_max END,
          first_token_ms_max = CASE WHEN first_token_ms_count <= ? THEN 0 ELSE first_token_ms_max END,
          last_used_at = CASE WHEN request_count <= ? THEN NULL ELSE last_used_at END,
          last_error_at = CASE WHEN error_count <= ? THEN NULL ELSE last_error_at END,
          updated_at = ?
      WHERE system_account_id = ? AND scope_type = ? AND scope_id = ? AND %s = ?
		`, bucket.TableName, statsSubtractSetExpr(""), column)
		bucketArgs := append(append([]any{}, params...),
			entry.Accumulator.DurationMsCount, entry.Accumulator.FirstTokenMsCount,
			entry.Accumulator.RequestCount, entry.Accumulator.ErrorCount, updatedAt,
			entry.SystemAccountID, entry.ScopeType, entry.ScopeID, timeKeyValue(timeKeys, bucket.ValueKey))
		if _, err := tx.ExecContext(ctx, bucketUpdate, bucketArgs...); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
      DELETE FROM %s
      WHERE system_account_id = ? AND scope_type = ? AND scope_id = ? AND %s = ?
        AND request_count = 0 AND success_count = 0 AND error_count = 0
        AND input_tokens = 0 AND output_tokens = 0 AND cache_read_tokens = 0 AND cache_read_cost_usd = 0
        AND cache_write_tokens = 0 AND cache_write_1h_tokens = 0 AND cache_write_cost_usd = 0
        AND thinking_tokens = 0 AND input_image_tokens = 0 AND output_image_tokens = 0 AND total_cost_usd = 0
		`, bucket.TableName, column),
			entry.SystemAccountID, entry.ScopeType, entry.ScopeID, timeKeyValue(timeKeys, bucket.ValueKey)); err != nil {
			return err
		}
	}
	return nil
}

// statsSubtractSetExpr 照 subtractUsageStatsTotal 的 MAX(0, col - ?) 列清单。
func statsSubtractSetExpr(prefix string) string {
	columns := []string{
		"request_count", "success_count", "error_count", "input_tokens", "output_tokens",
		"cache_read_tokens", "cache_read_cost_usd", "cache_write_tokens", "cache_write_1h_tokens",
		"cache_write_cost_usd", "thinking_tokens", "input_image_tokens", "output_image_tokens",
		"total_cost_usd", "duration_ms_sum", "duration_ms_count",
	}
	assignments := make([]string, 0, len(columns)+4)
	for _, column := range columns {
		assignments = append(assignments, fmt.Sprintf("%s = MAX(0, %s - ?)", column, column))
	}
	assignments = append(assignments,
		"first_token_ms_sum = MAX(0, first_token_ms_sum - ?)",
		"first_token_ms_count = MAX(0, first_token_ms_count - ?)")
	return strings.Join(assignments, ", ")
}

// statsSubtractParams 照 statsSubtractParams（列序与 set 表达式一致）。
func statsSubtractParams(stats statsagg.UsageStatsAccumulator) []any {
	return []any{
		stats.RequestCount, stats.SuccessCount, stats.ErrorCount,
		stats.InputTokens, stats.OutputTokens, stats.CacheReadTokens, stats.CacheReadCostUsd,
		stats.CacheWriteTokens, stats.CacheWrite1hTokens, stats.CacheWriteCostUsd, stats.ThinkingTokens,
		stats.InputImageTokens, stats.OutputImageTokens, stats.TotalCostUsd,
		stats.DurationMsSum, stats.DurationMsCount,
		stats.FirstTokenMsSum, stats.FirstTokenMsCount,
	}
}

// usageStatsBucketDefs / usageModelBucketDefs / usageErrorBucketDefs / usageLatencyBucketDefs
// 照 usage-stats-time-buckets.ts。
var (
	usageStatsBucketDefs = []statsagg.TimeBucketDefinition{
		{TableName: "usage_stats_minute", ColumnName: "stat_minute", ValueKey: "statMinute"},
		{TableName: "usage_stats_hourly", ColumnName: "stat_hour", ValueKey: "statHour"},
		{TableName: "usage_stats_daily", ColumnName: "stat_date", ValueKey: "statDate"},
		{TableName: "usage_stats_weekly", ColumnName: "stat_week", ValueKey: "statWeek"},
		{TableName: "usage_stats_monthly", ColumnName: "stat_month", ValueKey: "statMonth"},
	}
	usageModelBucketDefs = []statsagg.TimeBucketDefinition{
		{TableName: "usage_model_minute", ColumnName: "stat_minute", ValueKey: "statMinute"},
		{TableName: "usage_model_hourly", ColumnName: "stat_hour", ValueKey: "statHour"},
		{TableName: "usage_model_daily", ColumnName: "stat_date", ValueKey: "statDate"},
		{TableName: "usage_model_weekly", ColumnName: "stat_week", ValueKey: "statWeek"},
		{TableName: "usage_model_monthly", ColumnName: "stat_month", ValueKey: "statMonth"},
	}
	usageErrorBucketDefs = []statsagg.TimeBucketDefinition{
		{TableName: "usage_error_minute", ColumnName: "stat_minute", ValueKey: "statMinute"},
		{TableName: "usage_error_hourly", ColumnName: "stat_hour", ValueKey: "statHour"},
		{TableName: "usage_error_daily", ColumnName: "stat_date", ValueKey: "statDate"},
		{TableName: "usage_error_weekly", ColumnName: "stat_week", ValueKey: "statWeek"},
		{TableName: "usage_error_monthly", ColumnName: "stat_month", ValueKey: "statMonth"},
	}
	usageLatencyBucketDefs = []statsagg.TimeBucketDefinition{
		{TableName: "usage_latency_minute", ColumnName: "stat_minute", ValueKey: "statMinute"},
		{TableName: "usage_latency_hourly", ColumnName: "stat_hour", ValueKey: "statHour"},
		{TableName: "usage_latency_daily", ColumnName: "stat_date", ValueKey: "statDate"},
		{TableName: "usage_latency_weekly", ColumnName: "stat_week", ValueKey: "statWeek"},
		{TableName: "usage_latency_monthly", ColumnName: "stat_month", ValueKey: "statMonth"},
	}
)

var latencyBucketUpperBoundsMs = []int64{100, 250, 500, 1000, 2000, 5000, 10000, 30000, 60000, -1}

func latencyBucketUpperBound(value float64) int64 {
	for _, upperBound := range latencyBucketUpperBoundsMs {
		if upperBound == -1 || value <= float64(upperBound) {
			return upperBound
		}
	}
	return -1
}

type latencySample struct {
	metricType  string
	bucketBound int64
}

func latencySamples(row statsagg.UsageStatsRecordRow) []latencySample {
	var samples []latencySample
	if row.DurationMs != nil && *row.DurationMs >= 0 {
		samples = append(samples, latencySample{"duration_ms", latencyBucketUpperBound(*row.DurationMs)})
	}
	if row.FirstTokenMs != nil && *row.FirstTokenMs >= 0 {
		samples = append(samples, latencySample{"first_token_ms", latencyBucketUpperBound(*row.FirstTokenMs)})
	}
	return samples
}

func (s *RecordCleanupStore) subtractLatencyEntry(ctx context.Context, tx *sql.Tx, entry statsagg.UsageStatsEntry, row statsagg.UsageStatsRecordRow, timeKeys statsagg.UsageStatsTimeKeys, updatedAt string) error {
	for _, metric := range latencySamples(row) {
		for _, bucket := range usageLatencyBucketDefs {
			if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
        UPDATE %s
        SET sample_count = MAX(0, sample_count - 1), updated_at = ?
        WHERE system_account_id = ? AND scope_type = ? AND scope_id = ?
          AND metric_type = ? AND %s = ? AND bucket_upper_bound_ms = ?
			`, bucket.TableName, bucket.ColumnName),
				updatedAt, entry.SystemAccountID, entry.ScopeType, entry.ScopeID,
				metric.metricType, timeKeyValue(timeKeys, bucket.ValueKey), metric.bucketBound); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
        DELETE FROM %s
        WHERE system_account_id = ? AND scope_type = ? AND scope_id = ?
          AND metric_type = ? AND %s = ? AND bucket_upper_bound_ms = ?
          AND sample_count = 0
			`, bucket.TableName, bucket.ColumnName),
				entry.SystemAccountID, entry.ScopeType, entry.ScopeID,
				metric.metricType, timeKeyValue(timeKeys, bucket.ValueKey), metric.bucketBound); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *RecordCleanupStore) subtractModelBuckets(ctx context.Context, tx *sql.Tx, row statsagg.UsageStatsRecordRow, timeKeys statsagg.UsageStatsTimeKeys, updatedAt string) error {
	model := ""
	if row.Model != nil {
		model = strings.TrimSpace(*row.Model)
	}
	if model == "" {
		return nil
	}
	stats := statsagg.UsageStatsAccumulatorFromRecord(row)
	providerCode := "unknown"
	if row.ProviderCode != nil && *row.ProviderCode != "" {
		providerCode = *row.ProviderCode
	}
	params := statsSubtractParams(stats)[:14]
	for _, systemAccountID := range []string{row.SystemAccountID, statsagg.GlobalStatsSystemAccountID} {
		for _, bucket := range usageModelBucketDefs {
			args := append(append([]any{}, params...), updatedAt,
				systemAccountID, timeKeyValue(timeKeys, bucket.ValueKey), providerCode, model)
			if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
        UPDATE %s
        SET request_count = MAX(0, request_count - ?),
            success_count = MAX(0, success_count - ?),
            error_count = MAX(0, error_count - ?),
            input_tokens = MAX(0, input_tokens - ?),
            output_tokens = MAX(0, output_tokens - ?),
            cache_read_tokens = MAX(0, cache_read_tokens - ?),
            cache_read_cost_usd = MAX(0, cache_read_cost_usd - ?),
            cache_write_tokens = MAX(0, cache_write_tokens - ?),
            cache_write_1h_tokens = MAX(0, cache_write_1h_tokens - ?),
            cache_write_cost_usd = MAX(0, cache_write_cost_usd - ?),
            thinking_tokens = MAX(0, thinking_tokens - ?),
            input_image_tokens = MAX(0, input_image_tokens - ?),
            output_image_tokens = MAX(0, output_image_tokens - ?),
            total_cost_usd = MAX(0, total_cost_usd - ?),
            updated_at = ?
        WHERE system_account_id = ? AND %s = ? AND provider_code = ? AND model = ?
			`, bucket.TableName, bucket.ColumnName), args...); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
        DELETE FROM %s
        WHERE system_account_id = ? AND %s = ? AND provider_code = ? AND model = ?
          AND request_count = 0 AND success_count = 0 AND error_count = 0
          AND input_tokens = 0 AND output_tokens = 0 AND cache_read_tokens = 0 AND cache_read_cost_usd = 0
          AND cache_write_tokens = 0 AND cache_write_1h_tokens = 0 AND cache_write_cost_usd = 0
          AND thinking_tokens = 0 AND input_image_tokens = 0 AND output_image_tokens = 0 AND total_cost_usd = 0
			`, bucket.TableName, bucket.ColumnName),
				systemAccountID, timeKeyValue(timeKeys, bucket.ValueKey), providerCode, model); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *RecordCleanupStore) subtractErrorBuckets(ctx context.Context, tx *sql.Tx, row statsagg.UsageStatsRecordRow, timeKeys statsagg.UsageStatsTimeKeys, updatedAt string) error {
	errorGroup := "unknown"
	if row.ProviderCode != nil && *row.ProviderCode != "" {
		errorGroup = *row.ProviderCode
	}
	providerCode := errorGroup
	errorCode := "unknown"
	if row.ErrorCode != nil && *row.ErrorCode != "" {
		errorCode = *row.ErrorCode
	} else if row.StatusCode != nil {
		errorCode = fmt.Sprintf("%v", *row.StatusCode)
	}
	statusCode := 0.0
	if row.StatusCode != nil {
		statusCode = *row.StatusCode
	}
	for _, systemAccountID := range []string{row.SystemAccountID, statsagg.GlobalStatsSystemAccountID} {
		for _, bucket := range usageErrorBucketDefs {
			if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
        UPDATE %s
        SET request_count = MAX(0, request_count - 1),
            error_count = MAX(0, error_count - 1),
            updated_at = ?
        WHERE system_account_id = ? AND %s = ? AND error_group = ? AND provider_code = ? AND error_code = ? AND status_code = ?
			`, bucket.TableName, bucket.ColumnName),
				updatedAt, systemAccountID, timeKeyValue(timeKeys, bucket.ValueKey), errorGroup, providerCode, errorCode, statusCode); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
        DELETE FROM %s
        WHERE system_account_id = ? AND %s = ? AND error_group = ? AND provider_code = ? AND error_code = ? AND status_code = ?
          AND request_count = 0 AND error_count = 0
			`, bucket.TableName, bucket.ColumnName),
				systemAccountID, timeKeyValue(timeKeys, bucket.ValueKey), errorGroup, providerCode, errorCode, statusCode); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *RecordCleanupStore) subtractAccountQualityMinuteStats(ctx context.Context, tx *sql.Tx, row statsagg.UsageStatsRecordRow, updatedAt string, timezone *time.Location) error {
	if row.AccountID == nil || row.APIKeyID == nil {
		return nil
	}
	timeKeys, err := statsagg.UsageStatsTimeKeysFor(row.CreatedAt, timezone)
	if err != nil {
		return err
	}
	success := row.Success == 1
	firstTokenMs := 0.0
	firstTokenCount := 0.0
	if success && row.FirstTokenMs != nil && *row.FirstTokenMs >= 0 {
		firstTokenMs = *row.FirstTokenMs
		firstTokenCount = 1
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
    UPDATE account_quality_minute_stats
    SET request_count = MAX(0, request_count - 1),
        success_count = MAX(0, success_count - ?),
        error_count = MAX(0, error_count - ?),
        first_token_ms_sum = MAX(0, first_token_ms_sum - ?),
        first_token_ms_count = MAX(0, first_token_ms_count - ?),
        updated_at = ?
    WHERE account_id = ? AND stat_minute = ?
	`), boolToFloat(success), boolToFloat(!success), firstTokenMs, firstTokenCount, updatedAt, *row.AccountID, timeKeys.StatMinute); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
    DELETE FROM account_quality_minute_stats
    WHERE account_id = ? AND stat_minute = ?
      AND request_count = 0 AND success_count = 0 AND error_count = 0
      AND first_token_ms_sum = 0 AND first_token_ms_count = 0
	`, *row.AccountID, timeKeys.StatMinute); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
    INSERT INTO account_quality_dirty_accounts (account_id, first_dirty_at, updated_at)
    VALUES (?, ?, ?)
    ON CONFLICT(account_id) DO UPDATE SET updated_at = excluded.updated_at
	`, *row.AccountID, updatedAt, updatedAt)
	return err
}

func boolToFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

func (s *RecordCleanupStore) deleteAccountHealthHourForRecord(ctx context.Context, tx *sql.Tx, row statsagg.UsageStatsRecordRow) error {
	if row.TrafficSource != "account_health_check" || row.AccountID == nil {
		return nil
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM account_health_hourly WHERE account_id = ? AND last_record_id = ?`, *row.AccountID, row.ID)
	return err
}

// subtractAuthorizationUsageReportRows 照 subtractAuthorizationUsageReportRows
// （SQLite 路径；manual/team source 过滤与 all/类型/资源三级 resource filter）。
func (s *RecordCleanupStore) subtractAuthorizationUsageReportRows(ctx context.Context, tx *sql.Tx, row statsagg.UsageStatsRecordRow, statDate, updatedAt string) error {
	stats := statsagg.UsageStatsAccumulatorFromRecord(row)
	reportRows := authorizationReportRowsOf(row)
	for _, reportRow := range reportRows {
		scopes := []authorizationReportScope{{owner: reportRow.owner}}
		if reportRow.owner != statsagg.GlobalStatsSystemAccountID {
			scopes = append(scopes, authorizationReportScope{owner: statsagg.GlobalStatsSystemAccountID})
		}
		for _, scope := range scopes {
			if err := s.subtractAuthorizationSummaryRows(ctx, tx, reportRow, scope, stats, statDate, updatedAt); err != nil {
				return err
			}
		}
	}
	return nil
}

type authorizationReportScope struct{ owner string }

type authorizationReportRowData struct {
	authorizationID string
	owner           string
	grantee         string
	resourceType    string
	resourceID      string
	sourceType      *string
	sourceTeamID    *string
}

func authorizationReportRowsOf(row statsagg.UsageStatsRecordRow) []authorizationReportRowData {
	var rows []authorizationReportRowData
	if row.AccountAuthorizationID != nil && row.AccountID != nil && row.AccountOwnerSystemAccountID != nil &&
		*row.AccountOwnerSystemAccountID != row.SystemAccountID {
		rows = append(rows, authorizationReportRowData{
			authorizationID: "account:" + *row.AccountAuthorizationID,
			owner:           *row.AccountOwnerSystemAccountID,
			grantee:         row.SystemAccountID,
			resourceType:    "account",
			resourceID:      *row.AccountID,
			sourceType:      row.AccountAuthorizationSourceType,
			sourceTeamID:    row.AccountAuthorizationSourceTeamID,
		})
	}
	if row.GroupAuthorizationID != nil && row.GroupID != nil && row.GroupOwnerSystemAccountID != nil &&
		*row.GroupOwnerSystemAccountID != row.SystemAccountID {
		hitOwner := row.GroupOwnerSystemAccountID
		if row.AccountOwnerSystemAccountID != nil {
			hitOwner = row.AccountOwnerSystemAccountID
		}
		hitAccount := ""
		if row.AccountID != nil {
			hitAccount = *row.AccountID
		}
		rows = append(rows, authorizationReportRowData{
			authorizationID: "group:" + *row.GroupAuthorizationID,
			owner:           *row.GroupOwnerSystemAccountID,
			grantee:         row.SystemAccountID,
			resourceType:    "group",
			resourceID:      *row.GroupID,
			sourceType:      row.GroupAuthorizationSourceType,
			sourceTeamID:    row.GroupAuthorizationSourceTeamID,
		})
		_ = hitOwner
		_ = hitAccount
	}
	return rows
}

func (s *RecordCleanupStore) subtractAuthorizationSummaryRows(ctx context.Context, tx *sql.Tx, reportRow authorizationReportRowData, scope authorizationReportScope, stats statsagg.UsageStatsAccumulator, statDate, updatedAt string) error {
	filters := [][2]string{
		{"all", ""},
		{reportRow.resourceType, ""},
		{reportRow.resourceType, reportRow.resourceID},
	}
	params := statsSubtractParams(stats)
	for _, filter := range filters {
		teamFilters := [][2]string{{"", ""}}
		if reportRow.sourceType != nil && *reportRow.sourceType == "team" && reportRow.sourceTeamID != nil {
			teamFilters = append(teamFilters, [2]string{*reportRow.sourceTeamID, ""})
		}
		for _, teamFilter := range teamFilters {
			if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
        UPDATE authorization_team_usage_summary_daily
        SET request_count = MAX(0, request_count - ?),
            success_count = MAX(0, success_count - ?),
            error_count = MAX(0, error_count - ?),
            input_tokens = MAX(0, input_tokens - ?),
            output_tokens = MAX(0, output_tokens - ?),
            cache_read_tokens = MAX(0, cache_read_tokens - ?),
            cache_read_cost_usd = MAX(0, cache_read_cost_usd - ?),
            cache_write_tokens = MAX(0, cache_write_tokens - ?),
            cache_write_1h_tokens = MAX(0, cache_write_1h_tokens - ?),
            cache_write_cost_usd = MAX(0, cache_write_cost_usd - ?),
            thinking_tokens = MAX(0, thinking_tokens - ?),
            input_image_tokens = MAX(0, input_image_tokens - ?),
            output_image_tokens = MAX(0, output_image_tokens - ?),
            total_cost_usd = MAX(0, total_cost_usd - ?),
            updated_at = ?
        WHERE system_account_id = ? AND stat_date = ?
          AND resource_filter_type = ? AND resource_filter_id = ?
          AND team_filter_id = ?
          AND grantee_filter_system_account_id = ?
			`), append(append([]any{}, params...), updatedAt,
				scope.owner, statDate, filter[0], filter[1], teamFilter[0], reportRow.grantee)...); err != nil {
				return err
			}
		}
		userFilters := [][2]string{{"", ""}, {"", reportRow.grantee}}
		if reportRow.sourceType != nil && *reportRow.sourceType == "team" && reportRow.sourceTeamID != nil {
			userFilters = append(userFilters, [2]string{*reportRow.sourceTeamID, ""}, [2]string{*reportRow.sourceTeamID, reportRow.grantee})
		}
		for _, userFilter := range userFilters {
			if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
        UPDATE authorization_user_usage_summary_daily
        SET request_count = MAX(0, request_count - ?),
            success_count = MAX(0, success_count - ?),
            error_count = MAX(0, error_count - ?),
            input_tokens = MAX(0, input_tokens - ?),
            output_tokens = MAX(0, output_tokens - ?),
            cache_read_tokens = MAX(0, cache_read_tokens - ?),
            cache_read_cost_usd = MAX(0, cache_read_cost_usd - ?),
            cache_write_tokens = MAX(0, cache_write_tokens - ?),
            cache_write_1h_tokens = MAX(0, cache_write_1h_tokens - ?),
            cache_write_cost_usd = MAX(0, cache_write_cost_usd - ?),
            thinking_tokens = MAX(0, thinking_tokens - ?),
            input_image_tokens = MAX(0, input_image_tokens - ?),
            output_image_tokens = MAX(0, output_image_tokens - ?),
            total_cost_usd = MAX(0, total_cost_usd - ?),
            updated_at = ?
        WHERE system_account_id = ? AND stat_date = ?
          AND resource_filter_type = ? AND resource_filter_id = ?
          AND team_filter_id = ?
          AND grantee_filter_system_account_id = ?
			`), append(append([]any{}, params...), updatedAt,
				scope.owner, statDate, filter[0], filter[1], userFilter[0], userFilter[1])...); err != nil {
				return err
			}
		}
	}
	return nil
}

// ---- stats writer 结算入口 ----

// CleanupAPIKeyRecordStatsData 照 cleanupDeletedApiKeyRecordStatsData。
func (s *RecordCleanupStore) CleanupAPIKeyRecordStatsData(ctx context.Context, target retention.APIKeyCleanupTarget, rows []map[string]any, updatedAt string, shardDeleted bool, timezone *time.Location) error {
	parsedRows := make([]statsagg.UsageStatsRecordRow, 0, len(rows))
	for _, rowMap := range rows {
		parsed, err := statsaggRowFromMap(rowMap)
		if err != nil {
			return err
		}
		parsedRows = append(parsedRows, parsed)
	}
	tx, err := s.Stats.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.subtractAPIKeyUsageRowsOnce(ctx, tx, parsedRows, target.APIKeyID, target.SystemAccountID, updatedAt, timezone); err != nil {
		return err
	}
	if shardDeleted {
		if err := s.markUsageRowsDeleted(ctx, tx, parsedRows, target.APIKeyID, target.SystemAccountID, updatedAt); err != nil {
			return err
		}
	}
	if len(parsedRows) == 0 {
		if err := s.deleteAPIKeyScopeStatsRows(ctx, tx, target.APIKeyID, target.SystemAccountID); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return s.refreshDerivedWindows(ctx, target.APIKeyID)
	}
	return tx.Commit()
}

// subtractAPIKeyUsageRowsOnce 照 subtractApiKeyUsageRowsOnce：扣减台账 + 单次扣减。
func (s *RecordCleanupStore) subtractAPIKeyUsageRowsOnce(ctx context.Context, tx *sql.Tx, rows []statsagg.UsageStatsRecordRow, apiKeyID, systemAccountID, updatedAt string, timezone *time.Location) error {
	if len(rows) == 0 {
		return nil
	}
	location := timezone
	if location == nil {
		location = time.UTC
	}
	for _, row := range rows {
		recordJSON, err := jsonMarshal(statsaggRowToMap(row, row.SourceShardKey))
		if err != nil {
			return err
		}
		accountID := optionalTextPtr(row.AccountID)
		if _, err := tx.ExecContext(ctx, s.Stats.Bind(fmt.Sprintf(`
      INSERT INTO usage_record_cleanup_deductions (
        usage_id, api_key_id, account_id, system_account_id, source_shard_key, record_json,
        stats_subtracted_at, shard_deleted_at, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?)
      ON CONFLICT(usage_id, source_shard_key) DO UPDATE SET
        account_id = COALESCE(usage_record_cleanup_deductions.account_id, excluded.account_id),
        updated_at = excluded.updated_at
		`)), row.ID, apiKeyID, accountID, systemAccountID, row.SourceShardKey, recordJSON, updatedAt, updatedAt); err != nil {
			return err
		}
		var statsSubtractedAt sql.NullString
		err = tx.QueryRowContext(ctx, s.Stats.Bind(`
      SELECT stats_subtracted_at
      FROM usage_record_cleanup_deductions
      WHERE usage_id = ? AND source_shard_key = ?
      LIMIT 1
		`), row.ID, row.SourceShardKey).Scan(&statsSubtractedAt)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if err == nil && statsSubtractedAt.Valid && statsSubtractedAt.String != "" {
			continue
		}
		if err := s.subtractUsageStatsRecord(ctx, tx, row, updatedAt, location); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, s.Stats.Bind(`
      UPDATE usage_record_cleanup_deductions
      SET stats_subtracted_at = COALESCE(stats_subtracted_at, ?), updated_at = ?
      WHERE usage_id = ? AND source_shard_key = ?
		`), updatedAt, updatedAt, row.ID, row.SourceShardKey); err != nil {
			return err
		}
	}
	return nil
}

// markUsageRowsDeleted 照 markApiKeyUsageCleanupRowsDeleted。
func (s *RecordCleanupStore) markUsageRowsDeleted(ctx context.Context, tx *sql.Tx, rows []statsagg.UsageStatsRecordRow, apiKeyID, systemAccountID, updatedAt string) error {
	if len(rows) == 0 {
		return nil
	}
	for _, row := range rows {
		if _, err := tx.ExecContext(ctx, s.Stats.Bind(`
      UPDATE usage_record_cleanup_deductions
      SET shard_deleted_at = COALESCE(shard_deleted_at, ?), updated_at = ?
      WHERE usage_id = ? AND source_shard_key = ?
		`), updatedAt, updatedAt, row.ID, row.SourceShardKey); err != nil {
			return err
		}
	}
	_ = apiKeyID
	_ = systemAccountID
	return nil
}

func (s *RecordCleanupStore) deleteAPIKeyScopeStatsRows(ctx context.Context, tx *sql.Tx, apiKeyID, systemAccountID string) error {
	for _, tableName := range apiKeyScopeStatsTables {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(
			`DELETE FROM %s WHERE system_account_id = ? AND scope_type = 'api_key' AND scope_id = ?`, tableName),
			systemAccountID, apiKeyID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM stats_job_state WHERE scope_type = 'api_key' AND scope_id = ?`, apiKeyID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM usage_record_cleanup_deductions WHERE api_key_id = ? AND system_account_id = ?`, apiKeyID, systemAccountID); err != nil {
		return err
	}
	return nil
}

// CleanupAccountRecordStatsData 照 cleanupDeletedAccountRecordStatsData。
func (s *RecordCleanupStore) CleanupAccountRecordStatsData(ctx context.Context, target retention.ExpiredDeletedAccountTarget, rows []map[string]any, updatedAt string, shardDeleted bool, timezone *time.Location) error {
	parsedRows := make([]statsagg.UsageStatsRecordRow, 0, len(rows))
	for _, rowMap := range rows {
		parsed, err := statsaggRowFromMap(rowMap)
		if err != nil {
			return err
		}
		parsedRows = append(parsedRows, parsed)
	}
	tx, err := s.Stats.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.subtractAccountUsageRowsOnce(ctx, tx, parsedRows, target, updatedAt, timezone); err != nil {
		return err
	}
	if shardDeleted {
		if err := s.markUsageRowsDeleted(ctx, tx, parsedRows, target.AccountID, target.SystemAccountID, updatedAt); err != nil {
			return err
		}
	}
	if len(parsedRows) == 0 {
		if err := s.deleteAccountScopeStatsRows(ctx, tx, target); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return s.refreshDerivedWindows(ctx, target.AccountID)
	}
	return tx.Commit()
}

// subtractAccountUsageRowsOnce 照 subtractAccountUsageRowsOnce（account 变体）。
func (s *RecordCleanupStore) subtractAccountUsageRowsOnce(ctx context.Context, tx *sql.Tx, rows []statsagg.UsageStatsRecordRow, target retention.ExpiredDeletedAccountTarget, updatedAt string, timezone *time.Location) error {
	if len(rows) == 0 {
		return nil
	}
	location := timezone
	if location == nil {
		location = time.UTC
	}
	for _, row := range rows {
		recordJSON, err := jsonMarshal(statsaggRowToMap(row, row.SourceShardKey))
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, s.Stats.Bind(fmt.Sprintf(`
      INSERT INTO usage_record_cleanup_deductions (
        usage_id, api_key_id, account_id, system_account_id, source_shard_key, record_json,
        stats_subtracted_at, shard_deleted_at, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?)
      ON CONFLICT(usage_id, source_shard_key) DO UPDATE SET
        account_id = COALESCE(usage_record_cleanup_deductions.account_id, excluded.account_id),
        updated_at = excluded.updated_at
		`)), row.ID, nil, optionalTextPtr(row.AccountID), target.SystemAccountID, row.SourceShardKey, recordJSON, updatedAt, updatedAt); err != nil {
			return err
		}
		var statsSubtractedAt sql.NullString
		err = tx.QueryRowContext(ctx, s.Stats.Bind(`
      SELECT stats_subtracted_at FROM usage_record_cleanup_deductions
      WHERE usage_id = ? AND source_shard_key = ? LIMIT 1
		`), row.ID, row.SourceShardKey).Scan(&statsSubtractedAt)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if err == nil && statsSubtractedAt.Valid && statsSubtractedAt.String != "" {
			continue
		}
		if err := s.subtractUsageStatsRecord(ctx, tx, row, updatedAt, location); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, s.Stats.Bind(`
      UPDATE usage_record_cleanup_deductions
      SET stats_subtracted_at = COALESCE(stats_subtracted_at, ?), updated_at = ?
      WHERE usage_id = ? AND source_shard_key = ?
		`), updatedAt, updatedAt, row.ID, row.SourceShardKey); err != nil {
			return err
		}
	}
	return nil
}

func (s *RecordCleanupStore) deleteAccountScopeStatsRows(ctx context.Context, tx *sql.Tx, target retention.ExpiredDeletedAccountTarget) error {
	accountIDs := uniqueNonEmpty(append([]string{target.AccountID}, target.RelatedAccountIDs...))
	authorizationIDs := uniqueNonEmpty(target.AuthorizationIDs)
	teamScopeIDs := uniqueNonEmpty(target.TeamScopeIDs)
	for _, tableName := range accountScopeStatsTables {
		for _, accountID := range accountIDs {
			if _, err := tx.ExecContext(ctx, fmt.Sprintf(
				`DELETE FROM %s WHERE scope_type IN ('account', 'caller_account') AND scope_id = ?`, tableName), accountID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, fmt.Sprintf(
				`DELETE FROM %s WHERE scope_type = 'account_authorization_team' AND scope_id LIKE ? ESCAPE '\\'`, tableName),
				escapeLikePrefix(accountID)+":%"); err != nil {
				return err
			}
		}
		for _, chunk := range chunkValues(authorizationIDs, 400) {
			if len(chunk) == 0 {
				continue
			}
			if _, err := tx.ExecContext(ctx, s.Stats.Bind(fmt.Sprintf(
				`DELETE FROM %s WHERE scope_type = 'account_authorization' AND scope_id IN (%s)`, tableName, placeholderList(len(chunk)))),
				stringSliceToAny(chunk)...); err != nil {
				return err
			}
		}
		for _, chunk := range chunkValues(teamScopeIDs, 400) {
			if len(chunk) == 0 {
				continue
			}
			if _, err := tx.ExecContext(ctx, s.Stats.Bind(fmt.Sprintf(
				`DELETE FROM %s WHERE scope_type = 'account_authorization_team' AND scope_id IN (%s)`, tableName, placeholderList(len(chunk)))),
				stringSliceToAny(chunk)...); err != nil {
				return err
			}
		}
	}
	for _, accountID := range accountIDs {
		for _, condition := range [][2]string{
			{"stats_job_state", "scope_type IN ('account', 'caller_account') AND scope_id = ?"},
			{"stats_job_state", "scope_type = 'account_authorization_team' AND scope_id LIKE ? ESCAPE '\\'"},
			{"account_quality_scores", "account_id = ?"},
			{"account_quality_dirty_accounts", "account_id = ?"},
			{"account_quality_minute_stats", "account_id = ?"},
			{"account_health_hourly", "account_id = ?"},
			{"account_usage_snapshots", "account_id = ?"},
			{"usage_record_cleanup_deductions", "account_id = ?"},
		} {
			args := []any{accountID}
			if strings.Contains(condition[1], "LIKE") {
				args = []any{escapeLikePrefix(accountID) + ":%"}
			}
			if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE %s`, condition[0], condition[1]), args...); err != nil {
				return err
			}
		}
		for _, tableName := range accountAuthorizationReportTables {
			if _, err := tx.ExecContext(ctx, fmt.Sprintf(
				`DELETE FROM %s WHERE resource_filter_type = 'account' AND resource_filter_id = ?`, tableName), accountID); err != nil {
				return err
			}
		}
	}
	for _, chunk := range chunkValues(authorizationIDs, 400) {
		if len(chunk) == 0 {
			continue
		}
		condition := fmt.Sprintf("scope_type = 'account_authorization' AND scope_id IN (%s)", placeholderList(len(chunk)))
		for _, tableName := range accountScopeStatsTables {
			if _, err := tx.ExecContext(ctx, s.Stats.Bind(fmt.Sprintf(`DELETE FROM %s WHERE %s`, tableName, condition)), stringSliceToAny(chunk)...); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, s.Stats.Bind(fmt.Sprintf(`DELETE FROM stats_job_state WHERE %s`, condition)), stringSliceToAny(chunk)...); err != nil {
			return err
		}
	}
	for _, chunk := range chunkValues(teamScopeIDs, 400) {
		if len(chunk) == 0 {
			continue
		}
		condition := fmt.Sprintf("scope_type = 'account_authorization_team' AND scope_id IN (%s)", placeholderList(len(chunk)))
		for _, tableName := range accountScopeStatsTables {
			if _, err := tx.ExecContext(ctx, s.Stats.Bind(fmt.Sprintf(`DELETE FROM %s WHERE %s`, tableName, condition)), stringSliceToAny(chunk)...); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, s.Stats.Bind(fmt.Sprintf(`DELETE FROM stats_job_state WHERE %s`, condition)), stringSliceToAny(chunk)...); err != nil {
			return err
		}
	}
	return nil
}

// refreshDerivedWindows 照 refreshDeletedApiKeyDerivedWindowsIfNeeded /
// refreshDeletedAccountDerivedWindowsIfNeeded 的刷新半区。
func (s *RecordCleanupStore) refreshDerivedWindows(ctx context.Context, accountID string) error {
	if s.DerivedWindows == nil {
		if s.OnDerivedWindowsSkipped != nil {
			s.OnDerivedWindowsSkipped("已删除 " + accountID + " 衍生统计窗口刷新由调度式窗口刷新 jobs 收敛，record cleanup 跳过同步重算")
		}
		return nil
	}
	if err := s.DerivedWindows.RefreshQuotaHourlyWindows(ctx); err != nil {
		return err
	}
	return s.DerivedWindows.RefreshRankSnapshots(ctx)
}

// UpsertAccountUsageSnapshots 照 upsertAccountUsageSnapshots：owners 归属查自
// business 库 accounts（Node loadAccountSystemAccountIds）。
func (s *RecordCleanupStore) UpsertAccountUsageSnapshots(ctx context.Context, business *DB, inputs []retention.AccountUsageSnapshotUpsertInput) error {
	if len(inputs) == 0 {
		return nil
	}
	now := s.nowIso()
	tx, err := s.Stats.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, input := range inputs {
		updatedAt := input.UpdatedAt
		if updatedAt == "" {
			updatedAt = now
		}
		if _, ok := parseInstant(updatedAt); !ok {
			return fmt.Errorf("账户用量快照 updatedAt必须是带 Z 或数值 offset 的 RFC3339 时间")
		}
		ownerRows, err := queryRows(ctx, business, business.Bind(fmt.Sprintf(
			`SELECT system_account_id FROM %s WHERE id = ?`, business.Table("juhe_business", "accounts"))), input.AccountID)
		if err != nil {
			return err
		}
		if len(ownerRows) == 0 {
			return fmt.Errorf("账户用量快照缺少账户归属：%s", input.AccountID)
		}
		systemAccountID := textOf(ownerRows[0]["system_account_id"])
		snapshotJSON, err := jsonMarshal(input.Snapshot)
		if err != nil {
			return err
		}
		// Node SQLite 路径写裸表名 + excluded 小写；PG 路径写
		// juhe_stats.account_usage_snapshots + EXCLUDED 大写
		// （account-usage-snapshot.repository.ts 两个分支的逐字段对照）。
		snapshotStatement := `
      INSERT INTO account_usage_snapshots (
        system_account_id, account_id, kind, source, snapshot_json, refresh_status,
        last_success_at, last_error_message, updated_at, created_at
      )
      VALUES (?, ?, ?, ?, ?, 'fresh', ?, NULL, ?, ?)
      ON CONFLICT(system_account_id, account_id, kind) DO UPDATE SET
        system_account_id = excluded.system_account_id,
        source = excluded.source,
        snapshot_json = excluded.snapshot_json,
        refresh_status = 'fresh',
        last_success_at = excluded.last_success_at,
        last_error_message = NULL,
        updated_at = excluded.updated_at
		`
		if s.Stats.Postgres {
			snapshotStatement = `
      INSERT INTO juhe_stats.account_usage_snapshots (
        system_account_id, account_id, kind, source, snapshot_json, refresh_status,
        last_success_at, last_error_message, updated_at, created_at
      )
      VALUES (?, ?, ?, ?, ?, 'fresh', ?, NULL, ?, ?)
      ON CONFLICT(system_account_id, account_id, kind) DO UPDATE SET
        system_account_id = EXCLUDED.system_account_id,
        source = EXCLUDED.source,
        snapshot_json = EXCLUDED.snapshot_json,
        refresh_status = 'fresh',
        last_success_at = EXCLUDED.last_success_at,
        last_error_message = NULL,
        updated_at = EXCLUDED.updated_at
		`
		}
		if _, err := tx.ExecContext(ctx, s.Stats.Bind(snapshotStatement),
			systemAccountID, input.AccountID, input.Kind, input.Source, snapshotJSON, updatedAt, updatedAt, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// timeKeyValue 取 statsagg.UsageStatsTimeKeys 的 bucket valueKey 值
// （statsagg 的 timeValue 为包内私有，这里按同一映射展开）。
func timeKeyValue(keys statsagg.UsageStatsTimeKeys, valueKey string) string {
	switch valueKey {
	case "statMinute":
		return keys.StatMinute
	case "statHour":
		return keys.StatHour
	case "statDate":
		return keys.StatDate
	case "statWeek":
		return keys.StatWeek
	case "statMonth":
		return keys.StatMonth
	}
	return ""
}

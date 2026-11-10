package cleanuprepo

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsagg"
)

// 记录清理共用行对象转换与 account_usage_snapshots upsert。统计扣减本身走
// statssubtractpostgres.go（PG 唯一实现）。行对象在 Node 侧经 IPC JSON
// 序列化为 snake_case map，这里提供双向转换保持同一契约。

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

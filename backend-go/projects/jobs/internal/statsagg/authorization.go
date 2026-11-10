package statsagg

import (
	"context"
	"database/sql"
	"sort"
)

// 授权用量日报 writer（usage 聚合 job 的伴随写入），移植
// usage-stats-authorization-daily-writer.ts：每条带授权字段的 usage_record
// 展开为 team / user 两类摘要行（授权 owner 维度 + global 维度），
// ON CONFLICT 增量合并。写入表是 authorization-usage-range-windows job 的
// 源表。

type authorizationReportRow struct {
	authorizationID        string
	ownerSystemAccountID   string
	granteeSystemAccountID string
	resourceType           string
	resourceID             string
	sourceType             string // '' 当 null
	sourceTeamID           string // '' 当 null
}

type authorizationResourceFilter struct {
	resourceFilterType string
	resourceFilterID   string
}

type authorizationSummaryKey struct {
	teamFilterID                 string
	granteeFilterSystemAccountID string // 仅 user 摘要使用
	resourceFilterType           string
	resourceFilterID             string
}

// authorizationSummaryEntryKey 是批内预聚合的输出行键（表由 team/user 两个
// map 区分）；statDate 来自记录时间，跨日批次不会错误合并。
type authorizationSummaryEntryKey struct {
	systemAccountID string
	statDate        string
	summary         authorizationSummaryKey
}

// addAuthorizationUsageReportEntries 把单条记录的授权摘要展开结果累加进批内
// 预聚合表（2026-11-09）。原实现逐记录直接 UPSERT：单批 2000 行按每记录最多 24+
// 条语句会把事务拉到万级语句，并放大 authorization_*_usage_summary_daily 的
// 行锁与 WAL。预聚合按输出行键折叠后，同批同键只在批末落一条 UPSERT，累计
// 语义（含 max/last_used_at）由 MergeAccumulator 保持。
func addAuthorizationUsageReportEntries(
	teamEntries map[authorizationSummaryEntryKey]*UsageStatsAccumulator,
	userEntries map[authorizationSummaryEntryKey]*UsageStatsAccumulator,
	row UsageStatsRecordRow,
	statDate string,
	lookup *AuthorizationLookup,
) error {
	stats := UsageStatsAccumulatorFromRecord(row)
	for _, reportRow := range authorizationReportRows(row, lookup) {
		filters := authorizationReportResourceFilters(reportRow)
		for _, scopedReportRow := range authorizationReportScopeRows(reportRow) {
			teamKeys, userKeys := authorizationSummaryKeys(scopedReportRow, filters)
			for _, key := range teamKeys {
				entryKey := authorizationSummaryEntryKey{scopedReportRow.ownerSystemAccountID, statDate, key}
				if target, ok := teamEntries[entryKey]; ok {
					if err := MergeAccumulator(target, stats); err != nil {
						return err
					}
					continue
				}
				entry := stats
				teamEntries[entryKey] = &entry
			}
			for _, key := range userKeys {
				entryKey := authorizationSummaryEntryKey{scopedReportRow.ownerSystemAccountID, statDate, key}
				if target, ok := userEntries[entryKey]; ok {
					if err := MergeAccumulator(target, stats); err != nil {
						return err
					}
					continue
				}
				entry := stats
				userEntries[entryKey] = &entry
			}
		}
	}
	return nil
}

// flushAuthorizationUsageSummaryEntries 按排序后的键逐行 UPSERT。排序让同批
// 落库顺序确定（便于复现与排查；离线 rebuild 有 jobs 停止门禁，不与在线
// 聚合并发）。同键已在内存折叠，语句量取决于批内不同键数而非记录数。
func (a *Aggregator) flushAuthorizationUsageSummaryEntries(ctx context.Context, tx *sql.Tx, teamEntries, userEntries map[authorizationSummaryEntryKey]*UsageStatsAccumulator, updatedAt string) error {
	for _, entryKey := range sortedAuthorizationSummaryEntryKeys(teamEntries) {
		if err := a.upsertAuthorizationTeamUsageSummaryRow(ctx, tx, entryKey.systemAccountID, entryKey.statDate, entryKey.summary, *teamEntries[entryKey], updatedAt); err != nil {
			return err
		}
	}
	for _, entryKey := range sortedAuthorizationSummaryEntryKeys(userEntries) {
		if err := a.upsertAuthorizationUserUsageSummaryRow(ctx, tx, entryKey.systemAccountID, entryKey.statDate, entryKey.summary, *userEntries[entryKey], updatedAt); err != nil {
			return err
		}
	}
	return nil
}

func sortedAuthorizationSummaryEntryKeys(entries map[authorizationSummaryEntryKey]*UsageStatsAccumulator) []authorizationSummaryEntryKey {
	keys := make([]authorizationSummaryEntryKey, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		left, right := keys[i], keys[j]
		if left.systemAccountID != right.systemAccountID {
			return left.systemAccountID < right.systemAccountID
		}
		if left.statDate != right.statDate {
			return left.statDate < right.statDate
		}
		leftKey, rightKey := left.summary, right.summary
		if leftKey.teamFilterID != rightKey.teamFilterID {
			return leftKey.teamFilterID < rightKey.teamFilterID
		}
		if leftKey.granteeFilterSystemAccountID != rightKey.granteeFilterSystemAccountID {
			return leftKey.granteeFilterSystemAccountID < rightKey.granteeFilterSystemAccountID
		}
		if leftKey.resourceFilterType != rightKey.resourceFilterType {
			return leftKey.resourceFilterType < rightKey.resourceFilterType
		}
		return leftKey.resourceFilterID < rightKey.resourceFilterID
	})
	return keys
}

// authorizationReportRows mirrors authorizationReportRows。
func authorizationReportRows(row UsageStatsRecordRow, context *AuthorizationLookup) []authorizationReportRow {
	var rows []authorizationReportRow
	seen := map[string]struct{}{}
	add := func(row authorizationReportRow) {
		if _, ok := seen[row.authorizationID]; ok {
			return
		}
		seen[row.authorizationID] = struct{}{}
		rows = append(rows, row)
	}
	if isSet(row.AccountAuthorizationID) && isSet(row.AccountID) &&
		isSet(row.AccountOwnerSystemAccountID) && *row.AccountOwnerSystemAccountID != row.SystemAccountID {
		resourceID := *row.AccountID
		if context != nil && row.AccountAuthorizationID != nil {
			if mapped, ok := context.AccountAuthorizationResourceIDs[*row.AccountAuthorizationID]; ok && mapped != "" {
				resourceID = mapped
			}
		}
		add(authorizationReportRow{
			authorizationID:        "account:" + *row.AccountAuthorizationID,
			ownerSystemAccountID:   *row.AccountOwnerSystemAccountID,
			granteeSystemAccountID: row.SystemAccountID,
			resourceType:           "account",
			resourceID:             resourceID,
			sourceType:             deref(row.AccountAuthorizationSourceType),
			sourceTeamID:           deref(row.AccountAuthorizationSourceTeamID),
		})
	}
	if isSet(row.GroupAuthorizationID) && isSet(row.GroupID) &&
		isSet(row.GroupOwnerSystemAccountID) && *row.GroupOwnerSystemAccountID != row.SystemAccountID {
		add(authorizationReportRow{
			authorizationID:        "group:" + *row.GroupAuthorizationID,
			ownerSystemAccountID:   *row.GroupOwnerSystemAccountID,
			granteeSystemAccountID: row.SystemAccountID,
			resourceType:           "group",
			resourceID:             *row.GroupID,
			sourceType:             deref(row.GroupAuthorizationSourceType),
			sourceTeamID:           deref(row.GroupAuthorizationSourceTeamID),
		})
	}
	return rows
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// authorizationReportScopeRows mirrors authorizationReportScopeRows：非 global
// owner 追加一条 global 维度。
func authorizationReportScopeRows(row authorizationReportRow) []authorizationReportRow {
	if row.ownerSystemAccountID == GlobalStatsSystemAccountID {
		return []authorizationReportRow{row}
	}
	globalRow := row
	globalRow.ownerSystemAccountID = GlobalStatsSystemAccountID
	return []authorizationReportRow{row, globalRow}
}

// authorizationReportResourceFilters mirrors authorizationReportResourceFilters。
func authorizationReportResourceFilters(row authorizationReportRow) []authorizationResourceFilter {
	return []authorizationResourceFilter{
		{"all", ""},
		{row.resourceType, ""},
		{row.resourceType, row.resourceID},
	}
}

// authorizationSummaryKeys mirrors upsertAuthorizationSummaryRows 的键展开。
func authorizationSummaryKeys(row authorizationReportRow, filters []authorizationResourceFilter) (teamKeys, userKeys []authorizationSummaryKey) {
	for _, filter := range filters {
		userKeys = append(userKeys, authorizationSummaryKey{"", "", filter.resourceFilterType, filter.resourceFilterID})
		userKeys = append(userKeys, authorizationSummaryKey{"", row.granteeSystemAccountID, filter.resourceFilterType, filter.resourceFilterID})
		if row.sourceType == "team" && row.sourceTeamID != "" {
			teamKeys = append(teamKeys, authorizationSummaryKey{"", "", filter.resourceFilterType, filter.resourceFilterID})
			teamKeys = append(teamKeys, authorizationSummaryKey{row.sourceTeamID, "", filter.resourceFilterType, filter.resourceFilterID})
			userKeys = append(userKeys, authorizationSummaryKey{row.sourceTeamID, "", filter.resourceFilterType, filter.resourceFilterID})
			userKeys = append(userKeys, authorizationSummaryKey{row.sourceTeamID, row.granteeSystemAccountID, filter.resourceFilterType, filter.resourceFilterID})
		}
	}
	return teamKeys, userKeys
}

// upsertAuthorizationTeamUsageSummaryRow mirrors
// upsertAuthorizationTeamUsageSummaryRowAsync。
func (a *Aggregator) upsertAuthorizationTeamUsageSummaryRow(ctx context.Context, tx *sql.Tx, systemAccountID, statDate string, key authorizationSummaryKey, stats UsageStatsAccumulator, updatedAt string) error {
	target := a.Dialect.qualifiedTarget("authorization_team_usage_summary_daily")
	query := a.Dialect.bind(`
		INSERT INTO ` + a.Dialect.StatsTable("authorization_team_usage_summary_daily") + ` (
		  system_account_id, stat_date, team_filter_id, resource_filter_type, resource_filter_id, row_count,
		  request_count, success_count, error_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd,
		  cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd, thinking_tokens, input_image_tokens, output_image_tokens, total_cost_usd,
		  duration_ms_sum, duration_ms_count, duration_ms_max, first_token_ms_sum, first_token_ms_count, first_token_ms_max,
		  last_used_at, last_error_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 1, ` + placeholders(23) + `)
		ON CONFLICT(system_account_id, stat_date, team_filter_id, resource_filter_type, resource_filter_id) DO UPDATE SET
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
		  duration_ms_sum = ` + target + `.duration_ms_sum + excluded.duration_ms_sum,
		  duration_ms_count = ` + target + `.duration_ms_count + excluded.duration_ms_count,
		  duration_ms_max = CASE WHEN ` + target + `.duration_ms_max > excluded.duration_ms_max THEN ` + target + `.duration_ms_max ELSE excluded.duration_ms_max END,
		  first_token_ms_sum = ` + target + `.first_token_ms_sum + excluded.first_token_ms_sum,
		  first_token_ms_count = ` + target + `.first_token_ms_count + excluded.first_token_ms_count,
		  first_token_ms_max = CASE WHEN ` + target + `.first_token_ms_max > excluded.first_token_ms_max THEN ` + target + `.first_token_ms_max ELSE excluded.first_token_ms_max END,
		  last_used_at = CASE WHEN excluded.last_used_at IS NULL THEN ` + target + `.last_used_at WHEN ` + target + `.last_used_at IS NULL OR excluded.last_used_at > ` + target + `.last_used_at THEN excluded.last_used_at ELSE ` + target + `.last_used_at END,
		  last_error_at = CASE WHEN excluded.last_error_at IS NULL THEN ` + target + `.last_error_at WHEN ` + target + `.last_error_at IS NULL OR excluded.last_error_at > ` + target + `.last_error_at THEN excluded.last_error_at ELSE ` + target + `.last_error_at END,
		  updated_at = excluded.updated_at
	`)
	args := []any{systemAccountID, statDate, key.teamFilterID, key.resourceFilterType, key.resourceFilterID}
	args = append(args, statsParamsTail(stats, updatedAt)...)
	_, err := tx.ExecContext(ctx, query, args...)
	return err
}

// upsertAuthorizationUserUsageSummaryRow mirrors
// upsertAuthorizationUserUsageSummaryRowAsync。
func (a *Aggregator) upsertAuthorizationUserUsageSummaryRow(ctx context.Context, tx *sql.Tx, systemAccountID, statDate string, key authorizationSummaryKey, stats UsageStatsAccumulator, updatedAt string) error {
	target := a.Dialect.qualifiedTarget("authorization_user_usage_summary_daily")
	query := a.Dialect.bind(`
		INSERT INTO ` + a.Dialect.StatsTable("authorization_user_usage_summary_daily") + ` (
		  system_account_id, stat_date, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id, row_count,
		  request_count, success_count, error_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd,
		  cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd, thinking_tokens, input_image_tokens, output_image_tokens, total_cost_usd,
		  duration_ms_sum, duration_ms_count, duration_ms_max, first_token_ms_sum, first_token_ms_count, first_token_ms_max,
		  last_used_at, last_error_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 1, ` + placeholders(23) + `)
		ON CONFLICT(system_account_id, stat_date, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id) DO UPDATE SET
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
		  duration_ms_sum = ` + target + `.duration_ms_sum + excluded.duration_ms_sum,
		  duration_ms_count = ` + target + `.duration_ms_count + excluded.duration_ms_count,
		  duration_ms_max = CASE WHEN ` + target + `.duration_ms_max > excluded.duration_ms_max THEN ` + target + `.duration_ms_max ELSE excluded.duration_ms_max END,
		  first_token_ms_sum = ` + target + `.first_token_ms_sum + excluded.first_token_ms_sum,
		  first_token_ms_count = ` + target + `.first_token_ms_count + excluded.first_token_ms_count,
		  first_token_ms_max = CASE WHEN ` + target + `.first_token_ms_max > excluded.first_token_ms_max THEN ` + target + `.first_token_ms_max ELSE excluded.first_token_ms_max END,
		  last_used_at = CASE WHEN excluded.last_used_at IS NULL THEN ` + target + `.last_used_at WHEN ` + target + `.last_used_at IS NULL OR excluded.last_used_at > ` + target + `.last_used_at THEN excluded.last_used_at ELSE ` + target + `.last_used_at END,
		  last_error_at = CASE WHEN excluded.last_error_at IS NULL THEN ` + target + `.last_error_at WHEN ` + target + `.last_error_at IS NULL OR excluded.last_error_at > ` + target + `.last_error_at THEN excluded.last_error_at ELSE ` + target + `.last_error_at END,
		  updated_at = excluded.updated_at
	`)
	args := []any{systemAccountID, statDate, key.teamFilterID, key.granteeFilterSystemAccountID, key.resourceFilterType, key.resourceFilterID}
	args = append(args, statsParamsTail(stats, updatedAt)...)
	_, err := tx.ExecContext(ctx, query, args...)
	return err
}

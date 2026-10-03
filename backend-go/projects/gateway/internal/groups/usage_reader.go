// usage_reader.go closes the 分组列表"用量(日)"列恒 0 defect: the group
// list/detail projections hydrate accountStats.todayUsage/usage from the
// stats database exactly like Node group-summary.repository.ts
// loadGroupUsageSummariesForScopes (usage-summary-loaders.ts
// loadUsageSummariesForScopeRequests): todayUsage reads usage_stats_daily at
// today's stat_date, usage reads usage_stats_totals, and both render through
// the full AccountUsageSummary projection (usage-stats-helpers.ts
// usageSummaryFromAggregate).
//
// 契约（docs/functions/统计指标与分层聚合设计.md）：分组列表累计用量读
// usage_stats_totals、今日用量读 usage_stats_daily，维度 group（自有行）/
// group_authorization（授权行），system_account_id 恒为分组所有者。
package groups

import (
	"context"
	"database/sql"
	"strings"
)

// Usage scope types mirror the Node UsageSummaryScopeType subset the group
// list reads: own groups aggregate the group scope, authorized rows aggregate
// the runtime group_authorization row.
const (
	usageScopeTypeGroup              = "group"
	usageScopeTypeGroupAuthorization = "group_authorization"
)

// UsageScope mirrors Node UsageSummaryScopeRequest: RowKey maps the result
// back to the list/detail row (the group id), the remaining triple locates
// the stats rows. SystemAccountID is always the group owner.
type UsageScope struct {
	RowKey          string
	SystemAccountID string
	ScopeType       string
	ScopeID         string
}

// UsageSummary mirrors the Node AccountUsageSummary projection
// (usageSummaryFromAggregate): totalTokens is input+output (cache excluded)
// and lastUsedAt only renders when the stats row carries a non-null
// timestamp (omitempty, like the Node optional key).
type UsageSummary struct {
	RequestCount       int     `json:"requestCount"`
	InputTokens        int     `json:"inputTokens"`
	OutputTokens       int     `json:"outputTokens"`
	CacheReadTokens    int     `json:"cacheReadTokens"`
	CacheReadCost      float64 `json:"cacheReadCost"`
	CacheWriteTokens   int     `json:"cacheWriteTokens"`
	CacheWrite1hTokens int     `json:"cacheWrite1hTokens"`
	CacheWriteCost     float64 `json:"cacheWriteCost"`
	ThinkingTokens     int     `json:"thinkingTokens"`
	InputImageTokens   int     `json:"inputImageTokens"`
	OutputImageTokens  int     `json:"outputImageTokens"`
	TotalTokens        int     `json:"totalTokens"`
	TotalCost          float64 `json:"totalCost"`
	LastUsedAt         *string `json:"lastUsedAt,omitempty"`
}

// UsageSource is the stats-database read port behind the group list/detail
// usage hydration (Node loadGroupUsageSummariesForScopes). statDate selects
// the usage_stats_daily bucket (today's key) or, when empty, the
// usage_stats_totals aggregate. Implementations must be safe for concurrent
// use. Nil (the default) keeps the zero-summary degradation — the same
// policy as the stats reader.
type UsageSource interface {
	GroupListUsageSummaries(ctx context.Context, scopes []UsageScope, statDate string) (map[string]UsageSummary, error)
}

// groupUsageScope mirrors the Node group scope construction
// (group-summary.repository.ts): own rows read the group scope, authorized
// rows read the group_authorization scope of the runtime authorization row,
// and system_account_id is always the group owner (both scope families stamp
// the owner, never the grantee).
func groupUsageScope(rowKey, ownerSystemAccountID, accessType, authorizationID string) UsageScope {
	if accessType == "authorized" && authorizationID != "" {
		return UsageScope{RowKey: rowKey, SystemAccountID: ownerSystemAccountID, ScopeType: usageScopeTypeGroupAuthorization, ScopeID: authorizationID}
	}
	return UsageScope{RowKey: rowKey, SystemAccountID: ownerSystemAccountID, ScopeType: usageScopeTypeGroup, ScopeID: rowKey}
}

// uniqueUsageScopes mirrors uniqueScopes (usage-summary-loaders.ts): drop
// scopes with any empty component and dedupe on the full
// rowKey/systemAccountId/scopeType/scopeId tuple (same shape as the accounts
// slice's list_usage.go helper).
func uniqueUsageScopes(scopes []UsageScope) []UsageScope {
	unique := make(map[string]struct{}, len(scopes))
	out := make([]UsageScope, 0, len(scopes))
	for _, scope := range scopes {
		if scope.RowKey == "" || scope.SystemAccountID == "" || scope.ScopeType == "" || scope.ScopeID == "" {
			continue
		}
		key := scope.RowKey + "\x00" + scope.SystemAccountID + "\x00" + scope.ScopeType + "\x00" + scope.ScopeID
		if _, seen := unique[key]; seen {
			continue
		}
		unique[key] = struct{}{}
		out = append(out, scope)
	}
	return out
}

// GroupListUsageSummaries renders the requested scopes through the
// COALESCE'd 14-field projection (Node usageSummaryFromAggregate over the
// loadUsageSummariesForScopeRequests column list). Missing rows still join
// (LEFT JOIN) so every valid scope returns the empty summary shape — 13 zero
// keys, no lastUsedAt — exactly like the Node usageMap. 成本列读
// total_cost_usd：分组投影按 Node 列表读 total_cost_usd（账户面展示口径的
// success_cost_usd 是另一语义的列，不得混用）。
//
// SQL 形状沿用账户面 StatsUsageSource.AccountListUsageSummaries 的 VALUES
// LEFT JOIN 模板：scope 数以列表页为上界（pageSize ≤ 500，参数总量远低于
// 驱动上限），无需 Node IN 查询形式的 systemAccountId 分组 + scope id ≤400
// 分批；过滤语义等价（system_account_id/scope_type/scope_id 三元相等）。
func (r *GroupAccountStatsDBReader) GroupListUsageSummaries(ctx context.Context, scopes []UsageScope, statDate string) (map[string]UsageSummary, error) {
	normalized := uniqueUsageScopes(scopes)
	if len(normalized) == 0 || r.db == nil {
		return map[string]UsageSummary{}, nil
	}
	tableName := r.table("usage_stats_totals")
	if statDate != "" {
		tableName = r.table("usage_stats_daily")
	}
	values := make([]string, len(normalized))
	args := make([]any, 0, len(normalized)*4+1)
	for index, scope := range normalized {
		values[index] = "(?, ?, ?, ?)"
		args = append(args, scope.RowKey, scope.SystemAccountID, scope.ScopeType, scope.ScopeID)
	}
	if statDate != "" {
		args = append(args, statDate)
	}
	query := r.bind(`
    WITH requested(row_key, system_account_id, scope_type, scope_id) AS (
      VALUES ` + strings.Join(values, ", ") + `
    )
    SELECT
      requested.row_key,
      COALESCE(usage_rows.request_count, 0) AS request_count,
      COALESCE(usage_rows.input_tokens, 0) AS input_tokens,
      COALESCE(usage_rows.output_tokens, 0) AS output_tokens,
      COALESCE(usage_rows.cache_read_tokens, 0) AS cache_read_tokens,
      COALESCE(usage_rows.cache_read_cost_usd, 0) AS cache_read_cost,
      COALESCE(usage_rows.cache_write_tokens, 0) AS cache_write_tokens,
      COALESCE(usage_rows.cache_write_1h_tokens, 0) AS cache_write_1h_tokens,
      COALESCE(usage_rows.cache_write_cost_usd, 0) AS cache_write_cost,
      COALESCE(usage_rows.thinking_tokens, 0) AS thinking_tokens,
      COALESCE(usage_rows.input_image_tokens, 0) AS input_image_tokens,
      COALESCE(usage_rows.output_image_tokens, 0) AS output_image_tokens,
      COALESCE(usage_rows.total_cost_usd, 0) AS total_cost,
      usage_rows.last_used_at AS last_used_at
    FROM requested
    LEFT JOIN ` + tableName + ` usage_rows
      ON usage_rows.system_account_id = requested.system_account_id
      AND usage_rows.scope_type = requested.scope_type
      AND usage_rows.scope_id = requested.scope_id
      ` + func() string {
		if statDate != "" {
			return "AND usage_rows.stat_date = ?"
		}
		return ""
	}() + `
  `)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	summaries := make(map[string]UsageSummary, len(normalized))
	for rows.Next() {
		var (
			rowKey             string
			requestCount       sql.NullInt64
			inputTokens        sql.NullInt64
			outputTokens       sql.NullInt64
			cacheReadTokens    sql.NullInt64
			cacheReadCost      sql.NullFloat64
			cacheWriteTokens   sql.NullInt64
			cacheWrite1hTokens sql.NullInt64
			cacheWriteCost     sql.NullFloat64
			thinkingTokens     sql.NullInt64
			inputImageTokens   sql.NullInt64
			outputImageTokens  sql.NullInt64
			totalCost          sql.NullFloat64
			lastUsedAt         sql.NullString
		)
		if err := rows.Scan(&rowKey, &requestCount, &inputTokens, &outputTokens, &cacheReadTokens,
			&cacheReadCost, &cacheWriteTokens, &cacheWrite1hTokens, &cacheWriteCost, &thinkingTokens,
			&inputImageTokens, &outputImageTokens, &totalCost, &lastUsedAt); err != nil {
			return nil, err
		}
		summaries[rowKey] = UsageSummary{
			RequestCount:       int(requestCount.Int64),
			InputTokens:        int(inputTokens.Int64),
			OutputTokens:       int(outputTokens.Int64),
			CacheReadTokens:    int(cacheReadTokens.Int64),
			CacheReadCost:      cacheReadCost.Float64,
			CacheWriteTokens:   int(cacheWriteTokens.Int64),
			CacheWrite1hTokens: int(cacheWrite1hTokens.Int64),
			CacheWriteCost:     cacheWriteCost.Float64,
			ThinkingTokens:     int(thinkingTokens.Int64),
			InputImageTokens:   int(inputImageTokens.Int64),
			OutputImageTokens:  int(outputImageTokens.Int64),
			TotalTokens:        int(inputTokens.Int64 + outputTokens.Int64),
			TotalCost:          totalCost.Float64,
			LastUsedAt:         nullPtrString(lastUsedAt),
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return summaries, nil
}

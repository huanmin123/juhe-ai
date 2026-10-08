// list_usage.go closes the BUG-0175 D-126 账户面 deferral: the management
// list rows hydrate todayUsage/usage from the stats database exactly like
// Node hydrateAccountManagementStatusSeedsDirect
// (account-status-snapshot.repository.ts:256-278) — the per-row
// {systemAccountId, scopeType, scopeId} VALUES-join read against
// usage_stats_daily (stat_date = today in the usageStatsTimezone) for
// todayUsage and usage_stats_totals for usage, rendered through the bounded
// AccountListUsageSummary projection (account-management-list-usage.repository.ts).
//
// Degradation: a nil UsageSource (stats slice not wired) renders the zero
// summaries. A wired source errors fail the list (Node: the
// loadAccountManagementListUsageAsync SQLite busy-lock wrap rethrows and the
// PostgreSQL path has no catch — only the missing-file read-worker gate of
// the summary loaders skips reads, which the Go single-process gateway does
// not emulate here).
package accounts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Usage scope types mirror AccountManagementListUsageScopeType: owner rows
// aggregate the account scope, stamped authorized instances aggregate the
// runtime authorization row (Node account-status-snapshot.repository.ts:267-271).
const (
	usageScopeTypeAccount              = "account"
	usageScopeTypeAccountAuthorization = "account_authorization"
)

// UsageScope mirrors Node AccountManagementListUsageScope: rowKey maps the
// result back to the list row, the remaining triple locates the stats rows.
type UsageScope struct {
	RowKey          string
	SystemAccountID string
	ScopeType       string
	ScopeID         string
}

// UsageSource is the stats-database read port behind the list usage
// hydration (Node loadAccountManagementListUsageAsync). statDate selects the
// usage_stats_daily bucket (today's key) or, when empty, the
// usage_stats_totals aggregate. 今日臂返回 TodayUsageSummary（含缓存率投影），
// 累计臂由消费方映射回三字段 UsageSummary（设计 9.1，今日字段不进累计 DTO）。
// Implementations must be safe for concurrent use.
type UsageSource interface {
	AccountListUsageSummaries(ctx context.Context, scopes []UsageScope, statDate string) (map[string]TodayUsageSummary, error)
}

// SetUsageSource wires the stats reader; nil (the default) keeps the
// zero-value degradation.
func (s *Store) SetUsageSource(source UsageSource) {
	s.usage = source
}

// accountUsageScope mirrors the Node scope construction
// (account-status-snapshot.repository.ts:267-271): stamped instances read
// the account_authorization scope of the runtime authorization row.
func accountUsageScope(rowKey, systemAccountID, authorizationID string) UsageScope {
	if authorizationID != "" {
		return UsageScope{RowKey: rowKey, SystemAccountID: systemAccountID, ScopeType: usageScopeTypeAccountAuthorization, ScopeID: authorizationID}
	}
	return UsageScope{RowKey: rowKey, SystemAccountID: systemAccountID, ScopeType: usageScopeTypeAccount, ScopeID: rowKey}
}

// usageStatsTodayKey mirrors todayDateKey(usageStatsTimezone()): the
// sys_admin usageStatsTimezone system setting (invalid or absent falls back
// to UTC) applied to the current time.
func (s *Store) usageStatsTodayKey(ctx context.Context) string {
	var raw sql.NullString
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT value_json FROM `+s.table("system_settings")+`
		WHERE system_account_id = 'sys_admin' AND key = 'usageStatsTimezone'
		LIMIT 1`)).Scan(&raw)
	location := time.UTC
	if err == nil && raw.Valid {
		var text string
		if json.Unmarshal([]byte(raw.String), &text) == nil {
			if loaded, loadErr := time.LoadLocation(strings.TrimSpace(text)); loadErr == nil {
				location = loaded
			}
		}
	}
	return s.now().In(location).Format("2006-01-02")
}

// hydrateListUsage fills each item's todayUsage (daily bucket at today's
// stat date) and usage (totals aggregate) from the UsageSource. Missing map
// entries keep the zero summary (Node `todayUsage.get(row.id) ??
// emptyAccountManagementListUsage`); a nil source degrades the whole pass;
// source errors FAIL the list.
func (s *Store) hydrateListUsage(ctx context.Context, items []ListItem, records []listRow) error {
	if len(items) == 0 || len(items) != len(records) {
		return nil
	}
	if s.usage == nil {
		return nil
	}
	scopes := make([]UsageScope, 0, len(records))
	for _, row := range records {
		scopes = append(scopes, accountUsageScope(row.id, row.systemAccountID, row.authorizationID.String))
	}
	todayKey := s.usageStatsTodayKey(ctx)
	todays, err := s.usage.AccountListUsageSummaries(ctx, scopes, todayKey)
	if err != nil {
		return err
	}
	totals, err := s.usage.AccountListUsageSummaries(ctx, scopes, "")
	if err != nil {
		return err
	}
	for index := range items {
		if summary, ok := todays[items[index].ID]; ok {
			items[index].TodayUsage = summary
		}
		if summary, ok := totals[items[index].ID]; ok {
			// 累计 usage 固定映射回三字段 UsageSummary（设计 9.1）：今日扩展
			// 字段（inputTokens/cacheReadTokens/cacheReadRate）在映射处丢弃，
			// ListItem.Usage 序列化保持 requestCount/totalTokens/totalCost。
			items[index].Usage = UsageSummary{
				RequestCount: summary.RequestCount,
				TotalTokens:  summary.TotalTokens,
				TotalCost:    summary.TotalCost,
			}
		}
	}
	return nil
}

// uniqueUsageScopes mirrors uniqueScopes: dedupe on
// rowKey\0systemAccountId\0scopeType\0scopeId and drop scopes with any empty
// component.
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

// StatsUsageSource is the default UsageSource over a stats-database handle
// (PostgreSQL juhe_stats schema or the unqualified SQLite tables), mirroring
// the Node usageSql VALUES-join (account-management-list-usage.repository.ts).
type StatsUsageSource struct {
	db *sql.DB
	pg bool
}

// NewStatsUsageSource builds the reader; db must point at the stats database.
func NewStatsUsageSource(db *sql.DB, postgres bool) (*StatsUsageSource, error) {
	if db == nil {
		return nil, errors.New("accounts stats usage source requires a stats database")
	}
	return &StatsUsageSource{db: db, pg: postgres}, nil
}

// table qualifies stats-schema tables for PostgreSQL.
func (s *StatsUsageSource) table(name string) string {
	if s.pg {
		return "juhe_stats." + name
	}
	return name
}

// bind rewrites ? placeholders to $N for PostgreSQL.
func (s *StatsUsageSource) bind(query string) string {
	if !s.pg {
		return query
	}
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

// AccountListUsageSummaries renders the requested rows through the
// COALESCE'd projection: request_count, input+output tokens（totalTokens 语义
// 保持 input+output 不变）、拆分输出的 input_tokens 与 cache_read_tokens 两列
// （设计 9.1 今日行扩展）以及 total cost. Missing rows still join (LEFT JOIN)
// so the caller's zero fallback equals Node's usageMap with zeroed COALESCE
// values. 成本列读 success_cost_usd：展示面与配额执法口径（quota/hourly 窗口
// 的成功交付成本）一致，失败尝试成本只留在 total_cost_usd 供账号成本观测。
// 该函数同时服务今日（statDate 非空）与累计（statDate 为空）两条调用臂；
// 累计臂的结果由 hydrateListUsage 映射回三字段 UsageSummary，今日字段不进
// 累计 DTO。cacheReadRate 在装配处按样本门计算（cacheReadRate）。
func (s *StatsUsageSource) AccountListUsageSummaries(ctx context.Context, scopes []UsageScope, statDate string) (map[string]TodayUsageSummary, error) {
	normalized := uniqueUsageScopes(scopes)
	if len(normalized) == 0 {
		return map[string]TodayUsageSummary{}, nil
	}
	tableName := s.table("usage_stats_totals")
	if statDate != "" {
		tableName = s.table("usage_stats_daily")
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
	query := s.bind(`
    WITH requested(row_key, system_account_id, scope_type, scope_id) AS (
      VALUES ` + strings.Join(values, ", ") + `
    )
    SELECT
      requested.row_key,
      COALESCE(usage_rows.request_count, 0) AS request_count,
      COALESCE(usage_rows.input_tokens, 0) + COALESCE(usage_rows.output_tokens, 0) AS total_tokens,
      COALESCE(usage_rows.input_tokens, 0) AS input_tokens,
      COALESCE(usage_rows.cache_read_tokens, 0) AS cache_read_tokens,
      COALESCE(usage_rows.success_cost_usd, 0) AS total_cost
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
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	summaries := make(map[string]TodayUsageSummary, len(normalized))
	for rows.Next() {
		var (
			rowKey          string
			requestCount    sql.NullInt64
			totalTokens     sql.NullInt64
			inputTokens     sql.NullInt64
			cacheReadTokens sql.NullInt64
			totalCost       sql.NullFloat64
		)
		if err := rows.Scan(&rowKey, &requestCount, &totalTokens, &inputTokens, &cacheReadTokens, &totalCost); err != nil {
			return nil, err
		}
		input := int(inputTokens.Int64)
		cacheRead := int(cacheReadTokens.Int64)
		summaries[rowKey] = TodayUsageSummary{
			RequestCount:    int(requestCount.Int64),
			TotalTokens:     int(totalTokens.Int64),
			TotalCost:       totalCost.Float64,
			InputTokens:     input,
			CacheReadTokens: cacheRead,
			CacheReadRate:   cacheReadRate(input, cacheRead),
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return summaries, nil
}

// cacheReadRate computes the today cache-read ratio（设计 9.1 / 第 10 节）：
// input_tokens 未达样本门（CacheMinSampleInputTokens）或 cache_read > input
// 的异常行都视为无有效数据返回 nil；有效样本 rate = cache_read / input。
func cacheReadRate(inputTokens, cacheReadTokens int) *float64 {
	if inputTokens < CacheMinSampleInputTokens || cacheReadTokens > inputTokens {
		return nil
	}
	rate := float64(cacheReadTokens) / float64(inputTokens)
	return &rate
}

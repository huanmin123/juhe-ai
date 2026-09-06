// usage.go closes the M07 deferral "usage 渲染 (J5)": the api-keys list rows
// hydrate their per-key usage summary exactly like Node
// loadApiKeyListUsageSummariesForScopes (backend/src/storage/
// usage-summary-loaders.ts) — a single VALUES-join read against the stats
// database usage_stats_totals table scoped to scope_type='api_key' — and the
// detail projection loads the full AccountUsageSummary aggregate.
//
// Degradation: a nil UsageSource (stats slice not wired) renders the zero
// summaries. A wired source may only degrade on the missing-resource SQLite
// arm of isMissingSqliteStatsReadError ("no such table" / "unable to open
// database file"); every other error — and every PostgreSQL error — fails
// the read exactly like Node's throw.
package apikeys

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// UsageScope mirrors UsageSummaryScopeRequest for one api_keys row.
type UsageScope struct {
	RowKey          string
	SystemAccountID string
	ScopeID         string
}

// UsageSource is the stats-database read port: the bounded three-field
// summaries for list rows and the full AccountUsageSummary aggregate for the
// detail projection (Node loadApiKeyListUsageSummariesForScopesAsync /
// loadApiKeyUsageSummariesForScopesAsync). Implementations must be safe for
// concurrent use.
type UsageSource interface {
	ApiKeyListUsageSummaries(ctx context.Context, scopes []UsageScope) (map[string]ListUsageSummary, error)
	ApiKeyUsageSummaries(ctx context.Context, scopes []UsageScope) (map[string]AccountUsageSummary, error)
}

// SetUsageSource wires the J5 stats reader; nil (the default) keeps the
// zero-value degradation.
func (s *Store) SetUsageSource(source UsageSource) {
	s.usage = source
}

// uniqueUsageScopes mirrors uniqueUsageSummaryScopes: dedupe on
// rowKey\0systemAccountId\0scopeId and drop scopes with any empty component.
func uniqueUsageScopes(scopes []UsageScope) []UsageScope {
	unique := make(map[string]struct{}, len(scopes))
	out := make([]UsageScope, 0, len(scopes))
	for _, scope := range scopes {
		if scope.RowKey == "" || scope.SystemAccountID == "" || scope.ScopeID == "" {
			continue
		}
		key := scope.RowKey + "\x00" + scope.SystemAccountID + "\x00" + scope.ScopeID
		if _, seen := unique[key]; seen {
			continue
		}
		unique[key] = struct{}{}
		out = append(out, scope)
	}
	return out
}

// hydrateListUsage fills each item's usage from the UsageSource. Missing map
// entries degrade to the zero summary (Node
// `usage.get(row.id) ?? emptyApiKeyListUsageSummary`); a nil source degrades
// the whole pass. Source errors FAIL the list (Node: PostgreSQL errors and
// non-missing SQLite errors propagate out of
// loadApiKeyListUsageSummariesForScopesAsync — only missing-resource SQLite
// reads degrade inside the source).
func (s *Store) hydrateListUsage(ctx context.Context, items []ListItem, records []apiKeyRow) error {
	if len(items) == 0 || len(items) != len(records) {
		return nil
	}
	if s.usage == nil {
		return nil
	}
	scopes := make([]UsageScope, 0, len(records))
	for _, row := range records {
		scopes = append(scopes, UsageScope{RowKey: row.id, SystemAccountID: row.systemAccountID, ScopeID: row.id})
	}
	summaries, err := s.usage.ApiKeyListUsageSummaries(ctx, scopes)
	if err != nil {
		return err
	}
	for index := range items {
		if summary, ok := summaries[items[index].ID]; ok {
			items[index].Usage = summary
		}
	}
	return nil
}

// hydrateDetailUsage mirrors the detail projection's usage load
// (loadApiKeyUsageSummariesForScopesAsync over {systemAccountId, scopeId}):
// a missing entry keeps the zero AccountUsageSummary, source errors fail the
// read.
func (s *Store) hydrateDetailUsage(ctx context.Context, item *ListItem, systemAccountID string) error {
	if s.usage == nil {
		return nil
	}
	summaries, err := s.usage.ApiKeyUsageSummaries(ctx, []UsageScope{{
		RowKey: item.ID, SystemAccountID: systemAccountID, ScopeID: item.ID,
	}})
	if err != nil {
		return err
	}
	if summary, ok := summaries[item.ID]; ok {
		item.Usage = summary
	}
	return nil
}

// StatsUsageSource is the default UsageSource over a stats-database handle
// (PostgreSQL juhe_stats schema or the unqualified SQLite tables).
type StatsUsageSource struct {
	db *sql.DB
	pg bool
}

// NewStatsUsageSource builds the reader; db must point at the stats database.
func NewStatsUsageSource(db *sql.DB, postgres bool) (*StatsUsageSource, error) {
	if db == nil {
		return nil, errors.New("apikeys stats usage source requires a stats database")
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

// ApiKeyListUsageSummaries mirrors loadApiKeyListUsageSummariesForScopes
// (sync branch): the VALUES requested-rows left join against
// usage_stats_totals rendered through the bounded COALESCE projection.
func (s *StatsUsageSource) ApiKeyListUsageSummaries(ctx context.Context, scopes []UsageScope) (map[string]ListUsageSummary, error) {
	normalized := uniqueUsageScopes(scopes)
	if len(normalized) == 0 {
		return map[string]ListUsageSummary{}, nil
	}
	values := make([]string, len(normalized))
	args := make([]any, 0, len(normalized)*3)
	for index, scope := range normalized {
		values[index] = "(?, ?, ?)"
		args = append(args, scope.RowKey, scope.SystemAccountID, scope.ScopeID)
	}
	query := s.bind(`
    WITH requested(row_key, system_account_id, scope_id) AS (
      VALUES ` + strings.Join(values, ", ") + `
    )
    SELECT
      requested.row_key,
      COALESCE(usage_totals.request_count, 0) AS request_count,
      COALESCE(usage_totals.input_tokens, 0) + COALESCE(usage_totals.output_tokens, 0) AS total_tokens,
      COALESCE(usage_totals.total_cost_usd, 0) AS total_cost
    FROM requested
    LEFT JOIN ` + s.table("usage_stats_totals") + ` usage_totals
      ON usage_totals.system_account_id = requested.system_account_id
      AND usage_totals.scope_type = 'api_key'
      AND usage_totals.scope_id = requested.scope_id
  `)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		if degradeOnMissingStats(s.pg, err) {
			return map[string]ListUsageSummary{}, nil
		}
		return nil, err
	}
	defer rows.Close()
	summaries := make(map[string]ListUsageSummary, len(normalized))
	for rows.Next() {
		var (
			rowKey       string
			requestCount sql.NullInt64
			totalTokens  sql.NullInt64
			totalCost    sql.NullFloat64
		)
		if err := rows.Scan(&rowKey, &requestCount, &totalTokens, &totalCost); err != nil {
			return nil, err
		}
		summaries[rowKey] = ListUsageSummary{
			RequestCount: int(requestCount.Int64),
			TotalTokens:  int(totalTokens.Int64),
			TotalCost:    totalCost.Float64,
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return summaries, nil
}

// ApiKeyUsageSummaries mirrors loadApiKeyUsageSummariesForScopesAsync
// (usage_stats_totals aggregate without a stat date): the full
// AccountUsageSummary projection detail routes render. Scopes are grouped by
// owner and chunked at 400 like Node's chunkValues.
func (s *StatsUsageSource) ApiKeyUsageSummaries(ctx context.Context, scopes []UsageScope) (map[string]AccountUsageSummary, error) {
	normalized := uniqueUsageScopes(scopes)
	if len(normalized) == 0 {
		return map[string]AccountUsageSummary{}, nil
	}
	ownerOrder := []string{}
	scopesByOwner := map[string][]UsageScope{}
	for _, scope := range normalized {
		if _, seen := scopesByOwner[scope.SystemAccountID]; !seen {
			ownerOrder = append(ownerOrder, scope.SystemAccountID)
		}
		scopesByOwner[scope.SystemAccountID] = append(scopesByOwner[scope.SystemAccountID], scope)
	}
	summaries := make(map[string]AccountUsageSummary, len(normalized))
	for _, ownerID := range ownerOrder {
		for _, chunk := range chunkUsageScopes(scopesByOwner[ownerID], 400) {
			placeholders := make([]string, len(chunk))
			args := make([]any, 0, len(chunk)+1)
			args = append(args, ownerID)
			for index, scope := range chunk {
				placeholders[index] = s.dialectPlaceholder(len(args) + 1)
				args = append(args, scope.ScopeID)
			}
			query := s.bind(`
    SELECT scope_id,
      COALESCE(request_count, 0) AS request_count,
      COALESCE(input_tokens, 0) AS input_tokens,
      COALESCE(output_tokens, 0) AS output_tokens,
      COALESCE(cache_read_tokens, 0) AS cache_read_tokens,
      cache_read_cost_usd,
      COALESCE(cache_write_tokens, 0) AS cache_write_tokens,
      COALESCE(cache_write_1h_tokens, 0) AS cache_write_1h_tokens,
      COALESCE(cache_write_cost_usd, 0) AS cache_write_cost_usd,
      COALESCE(thinking_tokens, 0) AS thinking_tokens,
      COALESCE(input_image_tokens, 0) AS input_image_tokens,
      COALESCE(output_image_tokens, 0) AS output_image_tokens,
      COALESCE(total_cost_usd, 0) AS total_cost,
      last_used_at
    FROM ` + s.table("usage_stats_totals") + `
    WHERE system_account_id = ? AND scope_type = 'api_key' AND scope_id IN (` + strings.Join(placeholders, ", ") + `)
  `)
			rows, err := s.db.QueryContext(ctx, query, args...)
			if err != nil {
				if degradeOnMissingStats(s.pg, err) {
					continue
				}
				return nil, err
			}
			if err := collectUsageSummaryRows(rows, summaries); err != nil {
				return nil, err
			}
		}
	}
	return summaries, nil
}

// chunkUsageScopes mirrors chunkValues' 400-item chunking.
func chunkUsageScopes(scopes []UsageScope, size int) [][]UsageScope {
	chunks := make([][]UsageScope, 0, (len(scopes)+size-1)/size)
	for start := 0; start < len(scopes); start += size {
		end := start + size
		if end > len(scopes) {
			end = len(scopes)
		}
		chunks = append(chunks, scopes[start:end])
	}
	return chunks
}

// collectUsageSummaryRows renders the aggregate rows through Node's
// usageSummaryFromAggregate semantics: optional token counters default to 0,
// cache_read_cost_usd is REQUIRED, last_used_at is canonicalized when present.
func collectUsageSummaryRows(rows *sql.Rows, summaries map[string]AccountUsageSummary) error {
	defer rows.Close()
	for rows.Next() {
		var (
			scopeID            string
			requestCount       sql.NullFloat64
			inputTokens        sql.NullFloat64
			outputTokens       sql.NullFloat64
			cacheReadTokens    sql.NullFloat64
			cacheReadCost      sql.NullFloat64
			cacheWriteTokens   sql.NullFloat64
			cacheWrite1hTokens sql.NullFloat64
			cacheWriteCost     sql.NullFloat64
			thinkingTokens     sql.NullFloat64
			inputImageTokens   sql.NullFloat64
			outputImageTokens  sql.NullFloat64
			totalCost          sql.NullFloat64
			lastUsedAt         sql.NullString
		)
		if err := rows.Scan(&scopeID, &requestCount, &inputTokens, &outputTokens, &cacheReadTokens,
			&cacheReadCost, &cacheWriteTokens, &cacheWrite1hTokens, &cacheWriteCost, &thinkingTokens,
			&inputImageTokens, &outputImageTokens, &totalCost, &lastUsedAt); err != nil {
			return err
		}
		summary := AccountUsageSummary{
			RequestCount:       int(requestCount.Float64),
			InputTokens:        int(inputTokens.Float64),
			OutputTokens:       int(outputTokens.Float64),
			CacheReadTokens:    int(cacheReadTokens.Float64),
			CacheWriteTokens:   int(cacheWriteTokens.Float64),
			CacheWrite1hTokens: int(cacheWrite1hTokens.Float64),
			CacheWriteCost:     cacheWriteCost.Float64,
			ThinkingTokens:     int(thinkingTokens.Float64),
			InputImageTokens:   int(inputImageTokens.Float64),
			OutputImageTokens:  int(outputImageTokens.Float64),
			TotalTokens:        int(inputTokens.Float64 + outputTokens.Float64),
			TotalCost:          totalCost.Float64,
		}
		if !cacheReadCost.Valid {
			return errors.New("统计聚合字段 cache_read_cost_usd 必须是数字")
		}
		summary.CacheReadCost = cacheReadCost.Float64
		if lastUsedAt.Valid && lastUsedAt.String != "" {
			canonical, ok := canonicalRFC3339(lastUsedAt.String)
			if !ok {
				return errors.New("统计聚合 last_used_at必须是带 Z 或数值 offset 的 RFC3339 时间")
			}
			summary.LastUsedAt = &canonical
		}
		summaries[scopeID] = summary
	}
	return rows.Err()
}

// dialectPlaceholder renders the next positional placeholder ($N on
// PostgreSQL, ? on SQLite).
func (s *StatsUsageSource) dialectPlaceholder(index int) string {
	if !s.pg {
		return "?"
	}
	return "$" + itoa(index)
}

// degradeOnMissingStats mirrors isMissingSqliteStatsReadError's
// missing-resource arm: only SQLite "no such table" / "unable to open
// database file" failures degrade to empty usage. Every other error — and
// every PostgreSQL error (Node has no catch on the PG path) — fails the read.
func degradeOnMissingStats(pg bool, err error) bool {
	if pg {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "no such table:") || strings.Contains(message, "unable to open database file")
}

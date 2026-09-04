// usage.go closes the M07 deferral "usage 渲染 (J5)": the api-keys list rows
// now hydrate their per-key usage summary exactly like Node
// loadApiKeyListUsageSummariesForScopes (backend/src/storage/
// usage-summary-loaders.ts) — a single VALUES-join read against the stats
// database usage_stats_totals table scoped to scope_type='api_key'.
//
// Degradation: the stats database is owned by the J5 slice and may not exist
// yet in a Go-only deployment. A nil UsageSource (or a missing stats table)
// renders the zero summary — the same empty map the Node read-worker path
// returns when the stats database is absent — instead of failing the list.
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

// UsageSource is the stats-database read port for the list usage hydration.
// Implementations must be safe for concurrent use.
type UsageSource interface {
	ApiKeyListUsageSummaries(ctx context.Context, scopes []UsageScope) (map[string]ListUsageSummary, error)
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
// entries and a nil source both degrade to the zero summary (Node
// `usage.get(row.id) ?? emptyApiKeyListUsageSummary`). Source errors are
// degraded too: the usage column is advisory data and the J5 slice owns its
// availability.
func (s *Store) hydrateListUsage(ctx context.Context, items []ListItem, records []apiKeyRow) {
	if len(items) == 0 || len(items) != len(records) {
		return
	}
	if s.usage == nil {
		return
	}
	scopes := make([]UsageScope, 0, len(records))
	for _, row := range records {
		scopes = append(scopes, UsageScope{RowKey: row.id, SystemAccountID: row.systemAccountID, ScopeID: row.id})
	}
	summaries, err := s.usage.ApiKeyListUsageSummaries(ctx, scopes)
	if err != nil || len(summaries) == 0 {
		return
	}
	for index := range items {
		if summary, ok := summaries[items[index].ID]; ok {
			items[index].Usage = summary
		}
	}
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
		if isMissingStatsTableError(err) {
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

// isMissingStatsTableError mirrors the degradation arm of
// isMissingSqliteStatsReadError: a stats database that has not been
// provisioned yet renders zero usage instead of failing the list.
func isMissingStatsTableError(err error) bool {
	message := err.Error()
	return strings.Contains(message, "no such table:") || strings.Contains(message, "no such schema") || strings.Contains(message, "does not exist")
}

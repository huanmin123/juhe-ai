package statsverify

import (
	"context"
	"testing"
)

// Golden derivations follow checkUsageStatsConsistency
// (usage-stats.repository.ts lines 3076-3179):
//   - only daily rows with stat_date < today(stats tz) are sampled;
//   - the hourly reference sums usage_stats_hourly over
//     [stat_date+"T00", nextDateKey(stat_date)+"T00");
//   - cost metrics tolerate 1e-6 absolute drift; all other metrics must be
//     exact (tolerance 0);
//   - mismatches are reported, never repaired.

func seedConsistencyDaily(t *testing.T, ctx context.Context, store *Store, systemAccountID, scopeType, scopeID, statDate string, requestCount int, totalCost float64) {
	t.Helper()
	mustExec(t, ctx, store.db, `
		INSERT INTO usage_stats_daily (
		  system_account_id, scope_type, scope_id, stat_date,
		  request_count, success_count, error_count, input_tokens, output_tokens,
		  cache_read_tokens, cache_read_cost_usd, cache_write_tokens, cache_write_1h_tokens,
		  cache_write_cost_usd, thinking_tokens, input_image_tokens, output_image_tokens,
		  total_cost_usd, success_cost_usd, updated_at
		) VALUES (?, ?, ?, ?, ?, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, ?, '2026-03-01T00:00:00.000Z')
	`, systemAccountID, scopeType, scopeID, statDate, requestCount, totalCost)
}

func seedConsistencyHourly(t *testing.T, ctx context.Context, store *Store, systemAccountID, scopeType, scopeID, statHour string, requestCount int, totalCost float64) {
	t.Helper()
	mustExec(t, ctx, store.db, `
		INSERT INTO usage_stats_hourly (
		  system_account_id, scope_type, scope_id, stat_hour,
		  request_count, success_count, error_count, input_tokens, output_tokens,
		  cache_read_tokens, cache_read_cost_usd, cache_write_tokens, cache_write_1h_tokens,
		  cache_write_cost_usd, thinking_tokens, input_image_tokens, output_image_tokens,
		  total_cost_usd, success_cost_usd, updated_at
		) VALUES (?, ?, ?, ?, ?, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, ?, '2026-03-01T00:00:00.000Z')
	`, systemAccountID, scopeType, scopeID, statHour, requestCount, totalCost)
}

func seedConsistencyBuckets(t *testing.T, ctx context.Context, store *Store, systemAccountID, scopeType, scopeID, statDate, statHour string, requestCount int, totalCost float64) {
	t.Helper()
	seedConsistencyDaily(t, ctx, store, systemAccountID, scopeType, scopeID, statDate, requestCount, totalCost)
	seedConsistencyHourly(t, ctx, store, systemAccountID, scopeType, scopeID, statHour, requestCount, totalCost)
}


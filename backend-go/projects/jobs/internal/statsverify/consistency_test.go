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
		  total_cost_usd, updated_at
		) VALUES (?, ?, ?, ?, ?, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, ?, '2026-03-01T00:00:00.000Z')
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
		  total_cost_usd, updated_at
		) VALUES (?, ?, ?, ?, ?, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, ?, '2026-03-01T00:00:00.000Z')
	`, systemAccountID, scopeType, scopeID, statHour, requestCount, totalCost)
}

func seedConsistencyBuckets(t *testing.T, ctx context.Context, store *Store, systemAccountID, scopeType, scopeID, statDate, statHour string, requestCount int, totalCost float64) {
	t.Helper()
	seedConsistencyDaily(t, ctx, store, systemAccountID, scopeType, scopeID, statDate, requestCount, totalCost)
	seedConsistencyHourly(t, ctx, store, systemAccountID, scopeType, scopeID, statHour, requestCount, totalCost)
}

func TestCheckUsageStatsConsistencyConsistentBucketsYieldNoIssues(t *testing.T) {
	store := openTestStore(t, "UTC")
	ctx := context.Background()
	now := fixedUTC(t, "2026-03-10T12:00:00Z").Now()
	seedConsistencyDaily(t, ctx, store, "sys", "system_account", "sys", "2026-03-08", 5, 1.5)
	seedConsistencyHourly(t, ctx, store, "sys", "system_account", "sys", "2026-03-08T00", 3, 1.0)
	seedConsistencyHourly(t, ctx, store, "sys", "system_account", "sys", "2026-03-08T05", 2, 0.5)

	issues, err := store.CheckUsageStatsConsistency(ctx, UsageStatsConsistencyOptions{Now: now})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("issues=%+v, want none", issues)
	}
}

func TestCheckUsageStatsConsistencyMismatchReportsAllDriftedMetrics(t *testing.T) {
	store := openTestStore(t, "UTC")
	ctx := context.Background()
	now := fixedUTC(t, "2026-03-10T12:00:00Z").Now()
	// Daily says 10 requests / 2.0 cost; hourly sums to 7 requests / 1.4.
	seedConsistencyBuckets(t, ctx, store, "sys", "system_account", "sys", "2026-03-08", "2026-03-08T00", 7, 1.4)
	mustExec(t, ctx, store.db,
		`UPDATE usage_stats_daily SET request_count = 10, total_cost_usd = 2.0 WHERE scope_id = 'sys' AND stat_date = '2026-03-08'`)

	issues, err := store.CheckUsageStatsConsistency(ctx, UsageStatsConsistencyOptions{Now: now})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("issues=%d (%+v), want 2", len(issues), issues)
	}
	metrics := map[string][2]float64{}
	for _, issue := range issues {
		metrics[issue.Metric] = [2]float64{issue.DailyValue, issue.HourlyValue}
		if issue.SystemAccountID != "sys" || issue.ScopeType != "system_account" || issue.StatDate != "2026-03-08" {
			t.Fatalf("issue identity wrong: %+v", issue)
		}
	}
	if _, ok := metrics["request_count"]; !ok {
		t.Fatalf("request_count drift missing: %+v", issues)
	}
	if _, ok := metrics["total_cost_usd"]; !ok {
		t.Fatalf("total_cost_usd drift missing: %+v", issues)
	}
	if got := metrics["request_count"]; got[0] != 10 || got[1] != 7 {
		t.Fatalf("request_count daily/hourly=%v, want [10 7]", got)
	}
}

func TestCheckUsageStatsConsistencyCostTolerance(t *testing.T) {
	store := openTestStore(t, "UTC")
	ctx := context.Background()
	now := fixedUTC(t, "2026-03-10T12:00:00Z").Now()
	seedConsistencyBuckets(t, ctx, store, "sys", "system_account", "sys", "2026-03-08", "2026-03-08T00", 1, 1.0000005)

	// 5e-7 cost drift is inside the 1e-6 tolerance; the integer request_count
	// matches exactly, so nothing is reported.
	issues, err := store.CheckUsageStatsConsistency(ctx, UsageStatsConsistencyOptions{Now: now})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("tolerated drift reported: %+v", issues)
	}

	// 2e-6 cost drift exceeds the tolerance.
	mustExec(t, ctx, store.db, `UPDATE usage_stats_daily SET total_cost_usd = 1.000002 WHERE stat_date = '2026-03-08'`)
	issues, err = store.CheckUsageStatsConsistency(ctx, UsageStatsConsistencyOptions{Now: now})
	if err != nil {
		t.Fatalf("check2: %v", err)
	}
	if len(issues) != 1 || issues[0].Metric != "total_cost_usd" {
		t.Fatalf("issues=%+v, want single total_cost_usd issue", issues)
	}

	// An integer metric never tolerates any drift.
	mustExec(t, ctx, store.db, `UPDATE usage_stats_daily SET request_count = 2 WHERE stat_date = '2026-03-08'`)
	issues, err = store.CheckUsageStatsConsistency(ctx, UsageStatsConsistencyOptions{Now: now})
	if err != nil {
		t.Fatalf("check3: %v", err)
	}
	metrics := map[string]struct{}{}
	for _, issue := range issues {
		metrics[issue.Metric] = struct{}{}
	}
	if _, ok := metrics["request_count"]; !ok {
		t.Fatalf("exact-metric drift must be reported: %+v", issues)
	}
}

func TestCheckUsageStatsConsistencyExcludesTodayAndFutureHourly(t *testing.T) {
	store := openTestStore(t, "UTC")
	ctx := context.Background()
	now := fixedUTC(t, "2026-03-10T12:00:00Z").Now()

	// Today's bucket is never sampled (stat_date < today).
	seedConsistencyBuckets(t, ctx, store, "sys", "system_account", "sys", "2026-03-10", "2026-03-10T00", 1, 1)
	// Hourly rows outside the [00:00, next-day 00:00) window are excluded from
	// the reference sum.
	seedConsistencyHourly(t, ctx, store, "sys", "system_account", "sys", "2026-03-07T23", 3, 3)
	seedConsistencyHourly(t, ctx, store, "sys", "system_account", "sys", "2026-03-09T00", 5, 5)
	// The in-window hour defines the reference.
	seedConsistencyBuckets(t, ctx, store, "sys", "system_account", "sys", "2026-03-08", "2026-03-08T13", 2, 2)

	// Daily (2) equals the in-window hourly sum (2): the boundary hours at
	// 2026-03-07T23 (before the window) and 2026-03-09T00 (after it) must be
	// excluded, so no drift is reported.
	issues, err := store.CheckUsageStatsConsistency(ctx, UsageStatsConsistencyOptions{Now: now})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("out-of-window hours must not enter the reference sum, got %+v", issues)
	}
}

func TestCheckUsageStatsConsistencySampleLimitBounds(t *testing.T) {
	store := openTestStore(t, "UTC")
	ctx := context.Background()
	now := fixedUTC(t, "2026-03-10T12:00:00Z").Now()
	// Three drifted buckets on distinct days.
	for index, day := range []string{"2026-03-05", "2026-03-06", "2026-03-07"} {
		seedConsistencyBuckets(t, ctx, store, "sys", "system_account", "sys", day, day+"T00", index+1, 0)
		mustExec(t, ctx, store.db, `UPDATE usage_stats_daily SET request_count = ? WHERE stat_date = ?`, index+10, day)
	}

	cases := []struct {
		name      string
		sampleLim int
		want      int
	}{
		{name: "default clamps non-positive to 20", sampleLim: 0, want: 3},
		{name: "limit 2 samples two", sampleLim: 2, want: 2},
		{name: "limit above pool", sampleLim: 50, want: 3},
		{name: "limit clamps at 100", sampleLim: 1000, want: 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issues, err := store.CheckUsageStatsConsistency(ctx, UsageStatsConsistencyOptions{SampleLimit: tc.sampleLim, Now: now})
			if err != nil {
				t.Fatalf("check: %v", err)
			}
			// Each drifted bucket drifts request_count only (cost matched).
			requestIssues := 0
			for _, issue := range issues {
				if issue.Metric == "request_count" {
					requestIssues++
				}
			}
			if requestIssues != tc.want {
				t.Fatalf("request issues=%d, want %d", requestIssues, tc.want)
			}
		})
	}
}

func TestCheckUsageStatsConsistencyEmptyInput(t *testing.T) {
	store := openTestStore(t, "UTC")
	ctx := context.Background()
	now := fixedUTC(t, "2026-03-10T12:00:00Z").Now()
	issues, err := store.CheckUsageStatsConsistency(ctx, UsageStatsConsistencyOptions{Now: now})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("empty store must report no issues, got %+v", issues)
	}
}

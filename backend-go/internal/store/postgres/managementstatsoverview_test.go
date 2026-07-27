package postgres

import (
	"strings"
	"testing"
)

func TestManagementStatsOverviewSQLMatchesNodeFreshSummaryAndPreaggregatedDetails(t *testing.T) {
	t.Run("summary reads one published window", func(t *testing.T) {
		for _, want := range []string{
			"juhe_stats.usage_overview_summary_windows",
			"system_account_id = $1",
			"window_key = $2",
			"start_date = $3",
			"end_date = $4",
		} {
			if !strings.Contains(managementStatsOverviewSummarySQL, want) {
				t.Fatalf("summary SQL missing %q:\n%s", want, managementStatsOverviewSummarySQL)
			}
		}
		for _, forbidden := range []string{"usage_records", "usage_stats_daily", " sum(", " group by "} {
			if strings.Contains(strings.ToLower(managementStatsOverviewSummarySQL), forbidden) {
				t.Fatalf("summary SQL must not contain %q:\n%s", forbidden, managementStatsOverviewSummarySQL)
			}
		}
	})

	t.Run("daily reads one bounded source range", func(t *testing.T) {
		for _, want := range []string{
			"juhe_stats.usage_stats_daily", "scope_type = 'system_account'", "scope_id = $1",
			"stat_date >= $2", "stat_date <= $3", "ORDER BY stat_date ASC", "LIMIT 31",
		} {
			if !strings.Contains(managementStatsOverviewDailySQL, want) {
				t.Fatalf("daily SQL missing %q:\n%s", want, managementStatsOverviewDailySQL)
			}
		}
		for _, forbidden := range []string{"usage_records", "usage_overview_", " sum(", " group by "} {
			if strings.Contains(strings.ToLower(managementStatsOverviewDailySQL), forbidden) {
				t.Fatalf("daily SQL must not contain %q:\n%s", forbidden, managementStatsOverviewDailySQL)
			}
		}
	})

	queries := []struct {
		name string
		sql  string
		want []string
	}{
		{name: "trend", sql: managementStatsOverviewTrendSQL, want: []string{"juhe_stats.usage_overview_trend_windows", "ORDER BY bucket_key ASC", "LIMIT 744"}},
		{name: "models", sql: managementStatsOverviewModelsSQL, want: []string{"juhe_stats.usage_model_rank_windows", "ORDER BY rank ASC, provider_code ASC, model ASC", "LIMIT 10"}},
		{name: "errors", sql: managementStatsOverviewErrorsSQL, want: []string{"juhe_stats.usage_error_rank_windows", "ORDER BY rank ASC, provider_code ASC, error_code ASC, status_code ASC", "LIMIT 10"}},
	}
	for _, query := range queries {
		t.Run(query.name, func(t *testing.T) {
			for _, want := range query.want {
				if !strings.Contains(query.sql, want) {
					t.Fatalf("SQL missing %q:\n%s", want, query.sql)
				}
			}
			lower := strings.ToLower(query.sql)
			for _, forbidden := range []string{"usage_records", "usage_stats_daily", "usage_stats_hourly", " group by ", " sum("} {
				if strings.Contains(lower, forbidden) {
					t.Fatalf("SQL must not contain %q:\n%s", forbidden, query.sql)
				}
			}
		})
	}
}

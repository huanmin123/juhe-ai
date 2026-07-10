package postgres

import (
	"os"
	"strings"
	"testing"
)

func TestGatewayQuotaSnapshotStoreQueriesReadAggregatesOnly(t *testing.T) {
	source, err := os.ReadFile("gatewayquota.go")
	if err != nil {
		t.Fatalf("read gatewayquota.go: %v", err)
	}
	text := string(source)
	for _, want := range []string{
		"juhe_business.api_keys",
		"juhe_business.resource_authorizations AS ra",
		"juhe_business.resource_authorization_grants AS grant_rows",
		"juhe_stats.usage_stats_totals",
		"juhe_stats.usage_stats_daily",
		"juhe_stats.usage_stats_weekly",
		"juhe_stats.usage_stats_monthly",
		"juhe_stats.usage_quota_hourly_windows",
		"LIMIT $1",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("gateway quota snapshot store missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"juhe_usage.usage_records",
		" usage_records",
		"OFFSET",
		"SUM(",
		"GROUP BY",
		"DELETE ",
		"UPDATE ",
		"INSERT ",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("gateway quota snapshot store should not contain %q", forbidden)
		}
	}
}

func TestGatewayQuotaCostRowsQueryIsBoundedAndParameterized(t *testing.T) {
	query, args := gatewayQuotaCostRowsQuery(
		"juhe_stats.usage_stats_daily",
		[]string{"system_account_id", "scope_type", "scope_id", "stat_date"},
		[][]any{
			{"sys_1", "api_key", "key_1", "2026-07-09"},
			{"sys_2", "group_authorization", "auth_2", "2026-07-09"},
		},
	)
	for _, want := range []string{
		"SELECT system_account_id, scope_type, scope_id, stat_date, CAST(COALESCE(total_cost_usd, 0) AS double precision) AS total_cost",
		"FROM juhe_stats.usage_stats_daily",
		"system_account_id = $1",
		"stat_date = $4",
		"system_account_id = $5",
		"stat_date = $8",
	} {
		if !strings.Contains(query, want) {
			t.Fatalf("query missing %q:\n%s", want, query)
		}
	}
	if len(args) != 8 {
		t.Fatalf("args = %v, want 8 args", args)
	}
	for _, forbidden := range []string{"usage_records", "SUM(", "GROUP BY", "OFFSET"} {
		if strings.Contains(query, forbidden) {
			t.Fatalf("query should not contain %q:\n%s", forbidden, query)
		}
	}
}

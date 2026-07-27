package postgres

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/store/port"
)

func TestManagementStatsSQLReadsOnlyBoundedPreaggregates(t *testing.T) {
	source, err := os.ReadFile("managementstats.go")
	if err != nil {
		t.Fatalf("read management stats store: %v", err)
	}
	text := strings.ToLower(string(source))
	for _, required := range []string{
		"juhe_stats.usage_scope_range_windows",
		"juhe_stats.usage_rank_snapshots",
		"juhe_stats.usage_stats_daily",
		"juhe_stats.usage_stats_hourly",
		"juhe_stats.ai_performance_summary_windows",
		"juhe_business.accounts",
		"juhe_business.system_accounts",
		"juhe_business.resource_authorizations",
		"juhe_business.group_accounts",
		"limit $",
		"offset $",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("management stats store missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"juhe_usage.usage_records",
		"from usage_records",
		" sum(",
		" count(",
		" group by ",
		"insert into",
		"update juhe_stats",
		"delete from",
		"usage_range_window_requests",
		"$5::text = ''",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("management stats store must not contain %q", forbidden)
		}
	}
}

func TestManagementStatsProgressiveCatalogMigrationOwnsSchemaNotWriter(t *testing.T) {
	source, err := os.ReadFile("../../../db/migrations/000088_w6_progressive_stats_read_catalog.sql")
	if err != nil {
		t.Fatalf("read progressive stats migration: %v", err)
	}
	text := strings.ToLower(string(source))
	for _, required := range []string{
		"create table if not exists juhe_stats.usage_stats_hourly",
		"create table if not exists juhe_stats.usage_rank_snapshots",
		"create table if not exists juhe_stats.ai_performance_summary_windows",
		"create table if not exists juhe_stats.usage_scope_range_windows",
		"window_key text generated always as (start_date || ':' || end_date) stored",
		"idx_usage_scope_range_windows_account_usage_order",
		"idx_usage_rank_snapshots_lookup",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("progressive stats migration missing %q", required)
		}
	}
	down := strings.Split(text, "-- +goose down")
	if len(down) != 2 {
		t.Fatalf("progressive stats migration missing Goose Down section")
	}
	for _, forbidden := range []string{"drop table", "delete from", "truncate", "insert into", "update "} {
		if strings.Contains(down[1], forbidden) {
			t.Fatalf("progressive stats Down must preserve shared Node-writer data; found %q", forbidden)
		}
	}
}

func TestManagementStatsProgressiveReadsStaySplit(t *testing.T) {
	source, err := os.ReadFile("managementstats.go")
	if err != nil {
		t.Fatalf("read management stats store: %v", err)
	}
	text := string(source)
	list := sourceFunction(t, text, "func (s *Store) ReadManagementAccountUsage(", "func (s *Store) readManagementAccountUsageRows(")
	if strings.Contains(list, "ReadManagementAccountUsageSummary") || strings.Contains(list, "readManagementAccountUsageSummary") {
		t.Fatal("account usage list must not execute summary query")
	}
	series := sourceFunction(t, text, "func (s *Store) ReadManagementAIPerformanceSeries(", "func (s *Store) ReadManagementAIPerformanceAccounts(")
	for _, forbidden := range []string{"readManagementStatsRankedAccounts", "readManagementAIPerformanceSummary", "ai_performance_summary_windows"} {
		if strings.Contains(series, forbidden) {
			t.Fatalf("AI performance series must not contain %q", forbidden)
		}
	}
}

func sourceFunction(t *testing.T, source, startToken, endToken string) string {
	t.Helper()
	start := strings.Index(source, startToken)
	if start < 0 {
		t.Fatalf("source missing %q", startToken)
	}
	end := strings.Index(source[start:], endToken)
	if end < 0 {
		t.Fatalf("source after %q missing %q", startToken, endToken)
	}
	return source[start : start+end]
}

func TestManagementStatsSelectedInputsAreCappedBeforeSQL(t *testing.T) {
	for _, test := range []struct {
		name  string
		input []string
		limit int
		want  int
	}{
		{name: "trend", input: elevenStatsIDs(), limit: 10, want: 10},
		{name: "performance", input: twentyOneStatsIDs(), limit: 20, want: 20},
		{name: "account list", input: fiftyOneStatsIDs(), limit: 50, want: 50},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := len(boundedManagementStatsIDs(test.input, test.limit)); got != test.want {
				t.Fatalf("bounded IDs = %d, want %d", got, test.want)
			}
		})
	}
}

func TestManagementStatsKeywordCandidateQueriesAreSplitAndBounded(t *testing.T) {
	accountQuery, sourceQuery := managementStatsKeywordAccountIDQueries(port.ManagementStatsScope{
		SystemAccountID:       "sys_self",
		ViewerSystemAccountID: "sys_self",
		ScopeType:             "caller_account",
	})

	for _, required := range []string{
		"FROM juhe_business.accounts AS accounts",
		`accounts.name COLLATE "C" >= $3::text`,
		`starts_with(accounts.name, $3::text)`,
		`ORDER BY accounts.name COLLATE "C" ASC, accounts.id ASC`,
		"LIMIT $5::integer",
		"OFFSET $6::integer",
	} {
		if !strings.Contains(accountQuery, required) {
			t.Fatalf("account-name candidate query missing %q:\n%s", required, accountQuery)
		}
	}
	for _, forbidden := range []string{"source_accounts", "COALESCE("} {
		if strings.Contains(accountQuery, forbidden) {
			t.Fatalf("account-name candidate query contains %q:\n%s", forbidden, accountQuery)
		}
	}

	for _, required := range []string{
		"FROM juhe_business.accounts AS source_accounts",
		"INNER JOIN juhe_business.accounts AS instance_accounts",
		`source_accounts.name COLLATE "C" >= $3::text`,
		`starts_with(source_accounts.name, $3::text)`,
		"instance_accounts.system_account_id = $2::text",
		`ORDER BY source_accounts.name COLLATE "C" ASC, instance_accounts.id ASC`,
		"LIMIT $5::integer",
		"OFFSET $6::integer",
	} {
		if !strings.Contains(sourceQuery, required) {
			t.Fatalf("source-name candidate query missing %q:\n%s", required, sourceQuery)
		}
	}
	for _, forbidden := range []string{"COALESCE(", " OR (source_accounts.name"} {
		if strings.Contains(sourceQuery, forbidden) {
			t.Fatalf("source-name candidate query contains %q:\n%s", forbidden, sourceQuery)
		}
	}
}

func TestMergeManagementStatsKeywordAccountIDsKeepsAccountCandidatesFirst(t *testing.T) {
	got := mergeManagementStatsKeywordAccountIDs(
		[]string{"own_a", "shared", "own_b"},
		[]string{"source_a", "shared", "source_b"},
		4,
	)
	want := []string{"own_a", "shared", "own_b", "source_a"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merged IDs = %v, want %v", got, want)
	}
}

func TestNormalizeManagementStatsKeywordUsesNFKCAndTrim(t *testing.T) {
	if got := normalizeManagementStatsKeyword("  ＡＢＣ  "); got != "ABC" {
		t.Fatalf("normalized keyword = %q, want ABC", got)
	}
}

func elevenStatsIDs() []string    { return makeStatsIDs(11) }
func twentyOneStatsIDs() []string { return makeStatsIDs(21) }
func fiftyOneStatsIDs() []string  { return makeStatsIDs(51) }

func makeStatsIDs(count int) []string {
	result := make([]string, count)
	for index := range result {
		result[index] = string(rune('a' + index))
	}
	return result
}

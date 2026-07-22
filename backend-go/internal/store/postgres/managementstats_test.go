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

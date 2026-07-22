package postgres

import (
	"os"
	"strings"
	"testing"
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

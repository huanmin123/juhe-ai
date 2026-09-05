// Tests for the PostgreSQL J1 outcome reader (readPostgresOutcomesForAccounts
// port): the slice query contract (account/hour unnest arrays, zoned-hour
// LATERAL window, non-stale guard), the input normalization errors and the
// payload decoding.
package statreads

import (
	"testing"
	"time"
)

// TestJ1PostgresOutcomeQueryContract locks the slice query: the account and
// hour buckets travel as text arrays, the observed-after instant and the
// timezone name pin the zoned hour window, and the payload decode keeps the
// storage_observed_at projection.
func TestJ1PostgresOutcomeQueryContract(t *testing.T) {
	location := time.UTC
	query, args, err := j1PostgresOutcomeQuery(
		[]string{"acct-a", "acct-b", "acct-a"},
		[]string{"2026-09-04T09", "2026-09-04T10", "2026-09-04T09"},
		"2026-09-03T00:00:00.000Z", location.String())
	if err != nil {
		t.Fatalf("query build: %v", err)
	}
	for _, needle := range []string{
		"juhe_jobs.account_health_outcomes",
		"unnest($1::text[])",
		"unnest($2::text[])",
		"observed_at >= $3::timestamptz",
		"AT TIME ZONE $4",
		"outcome <> 'stale'",
		"ORDER BY observed_at DESC, outcome_id DESC",
	} {
		if !containsText(query, needle) {
			t.Fatalf("query missing %q: %s", needle, query)
		}
	}
	if len(args) != 4 {
		t.Fatalf("argument count wrong: %d", len(args))
	}
	accounts, ok := args[0].([]string)
	if !ok || len(accounts) != 2 || accounts[0] != "acct-a" || accounts[1] != "acct-b" {
		t.Fatalf("account array wrong: %#v", args[0])
	}
	buckets, ok := args[1].([]string)
	if !ok || len(buckets) != 2 || buckets[0] != "2026-09-04T09" || buckets[1] != "2026-09-04T10" {
		t.Fatalf("hour buckets must dedupe in order: %#v", args[1])
	}
	if args[2] != "2026-09-03T00:00:00.000Z" {
		t.Fatalf("observedAfter wrong: %#v", args[2])
	}
	if args[3] != "UTC" {
		t.Fatalf("timezone name wrong: %#v", args[3])
	}
}

// TestJ1PostgresOutcomeQueryRejectsInvalidInput mirrors
// normalizeAccountOutcomeQuery / normalizeAiHealthHourBuckets.
func TestJ1PostgresOutcomeQueryRejectsInvalidInput(t *testing.T) {
	if _, _, err := j1PostgresOutcomeQuery(make([]string, 51), []string{"2026-09-04T10"}, "2026-09-03T00:00:00.000Z", "UTC"); err == nil {
		t.Fatalf("51 accounts must fail")
	}
	oversizedHour := make([]string, 31*24+1)
	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	for index := range oversizedHour {
		oversizedHour[index] = base.Add(time.Duration(index) * time.Hour).Format("2006-01-02T15")
	}
	if _, _, err := j1PostgresOutcomeQuery([]string{"acct-a"}, oversizedHour, "2026-09-03T00:00:00.000Z", "UTC"); err == nil {
		t.Fatalf("oversized hour buckets must fail")
	}
	if _, _, err := j1PostgresOutcomeQuery([]string{"acct-a"}, []string{"2026-09-04T25"}, "2026-09-03T00:00:00.000Z", "UTC"); err == nil {
		t.Fatalf("invalid stat hour must fail")
	}
}

// TestDecodeJ1OutcomeRows mirrors the payload decode: undecodable rows are
// skipped, fields land on the j1Outcome projection the merge consumes.
func TestDecodeJ1OutcomeRows(t *testing.T) {
	rows := []Row{
		{"payload": `{"outcomeId":"out-1","requestId":"req-1","accountId":"acct-a","outcome":"complete_success","observedAt":"2026-09-04T09:30:00.000Z","statusCode":200,"nextDueAt":"2026-09-04T10:00:00.000Z"}`, "storage_observed_at": "2026-09-04T09:30:00.123456Z"},
		{"payload": `not-json`, "storage_observed_at": "2026-09-04T09:31:00.123456Z"},
		{"payload": `{"outcomeId":"out-2","requestId":"req-2","accountId":"acct-b","outcome":"upstream_failure","observedAt":"2026-09-04T10:15:00.000Z","errorCode":"upstream_error","errorMessage":"上游失败"}`, "storage_observed_at": "2026-09-04T10:15:00.123456Z"},
	}
	outcomes, err := decodeJ1OutcomeRows(rows)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(outcomes) != 2 {
		t.Fatalf("undecodable rows must be skipped: %#v", outcomes)
	}
	success := outcomes[0]
	if success.OutcomeID != "out-1" || success.Outcome != "complete_success" || success.AccountID != "acct-a" {
		t.Fatalf("success outcome mismatch: %#v", success)
	}
	if success.StatusCode == nil || *success.StatusCode != 200 {
		t.Fatalf("status code mismatch: %#v", success.StatusCode)
	}
	failure := outcomes[1]
	if failure.ErrorCode == nil || *failure.ErrorCode != "upstream_error" {
		t.Fatalf("error code mismatch: %#v", failure)
	}
	if j1OutcomeHealthStatus(success) != "success" || j1OutcomeHealthStatus(failure) != "failure" {
		t.Fatalf("health status mapping mismatch")
	}
}

func containsText(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for index := 0; index+len(needle) <= len(haystack); index++ {
		if haystack[index:index+len(needle)] == needle {
			return index
		}
	}
	return -1
}

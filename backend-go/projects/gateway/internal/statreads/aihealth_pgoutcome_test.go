// Tests for the PostgreSQL J1 outcome reader: the single ranged-SELECT
// contract (entity-column projection, non-stale guard, ASC ordering that
// feeds the merge layer's last-wins), the input normalization errors and the
// row building without any payload decode path.
package statreads

import (
	"testing"
	"time"
)

// TestJ1PostgresOutcomeQueryContract locks the ranged query: accounts travel
// as a single text array, the observed-after instant bounds the window, stale
// outcomes are excluded in SQL, the projection reads entity columns (only
// next_due_at comes from the payload as a single key), and the ASC ordering
// makes the merge layer's >= last-wins reproduce the old
// "observed_at DESC, outcome_id DESC LIMIT 1" winner.
func TestJ1PostgresOutcomeQueryContract(t *testing.T) {
	query, args, err := j1PostgresOutcomeQuery(
		[]string{"acct-a", "acct-b", "acct-a"},
		"2026-09-03T00:00:00.000Z")
	if err != nil {
		t.Fatalf("query build: %v", err)
	}
	for _, needle := range []string{
		"juhe_jobs.account_health_outcomes",
		"account_id = ANY($1::text[])",
		"observed_at >= $2::timestamptz",
		"outcome <> 'stale'",
		"payload->>'next_due_at'",
		"ORDER BY observed_at ASC, outcome_id ASC",
		"status_code::bigint",
	} {
		if !containsText(query, needle) {
			t.Fatalf("query missing %q: %s", needle, query)
		}
	}
	for _, banned := range []string{
		"unnest(",
		"LATERAL",
		"AT TIME ZONE",
		"INCLUDE",
		"LIMIT 1",
	} {
		if containsText(query, banned) {
			t.Fatalf("query must not carry the legacy per-hour probe (%q): %s", banned, query)
		}
	}
	if len(args) != 2 {
		t.Fatalf("argument count wrong: %d", len(args))
	}
	accounts, ok := args[0].([]string)
	if !ok || len(accounts) != 2 || accounts[0] != "acct-a" || accounts[1] != "acct-b" {
		t.Fatalf("account array must dedupe in order: %#v", args[0])
	}
	if args[1] != "2026-09-03T00:00:00.000Z" {
		t.Fatalf("observedAfter must pass through unchanged: %#v", args[1])
	}
}

// TestJ1PostgresOutcomeQueryRejectsInvalidInput keeps the only remaining
// input guard; the hour-bucket range/shape validation moved to the handler
// layer (boundedQueryInt / statHourPattern) and must not reappear here.
func TestJ1PostgresOutcomeQueryRejectsInvalidInput(t *testing.T) {
	if _, _, err := j1PostgresOutcomeQuery(make([]string, 51), "2026-09-03T00:00:00.000Z"); err == nil {
		t.Fatalf("51 accounts must fail")
	}
	if _, _, err := j1PostgresOutcomeQuery([]string{"acct-a"}, "2026-09-03T00:00:00.000Z"); err != nil {
		t.Fatalf("valid input must build: %v", err)
	}
}

// TestBuildJ1OutcomeRows locks the entity-column mapping: typed columns land
// on the j1Outcome projection the merge consumes; nullable columns stay nil
// on NULL, and next_due_at arrives as the payload single-key projection.
func TestBuildJ1OutcomeRows(t *testing.T) {
	statusCode := int64(200)
	rows := []Row{
		{
			"outcome_id": "out-1", "request_id": "req-1", "account_id": "acct-a",
			"outcome": "complete_success", "storage_observed_at": "2026-09-04T09:30:00.123456Z",
			"status_code": statusCode, "error_code": nil, "error_message": nil,
			"next_due_at": "2026-09-04T10:00:00.000Z",
		},
		{
			"outcome_id": "out-2", "request_id": "req-2", "account_id": "acct-b",
			"outcome": "upstream_failure", "storage_observed_at": "2026-09-04T10:15:00.123456Z",
			"status_code": nil, "error_code": "upstream_error", "error_message": "上游失败",
			"next_due_at": nil,
		},
	}
	outcomes := buildJ1OutcomeRows(rows)
	if len(outcomes) != len(rows) {
		t.Fatalf("范围读取无跳行分支，行数必须一一对应: %d != %d", len(outcomes), len(rows))
	}
	success := outcomes[0]
	if success.OutcomeID != "out-1" || success.RequestID != "req-1" || success.AccountID != "acct-a" ||
		success.Outcome != "complete_success" || success.ObservedAt != "2026-09-04T09:30:00.123456Z" {
		t.Fatalf("success outcome mapping mismatch: %#v", success)
	}
	if success.StatusCode == nil || *success.StatusCode != 200 {
		t.Fatalf("status code mismatch: %#v", success.StatusCode)
	}
	if success.ErrorCode != nil || success.ErrorMessage != nil {
		t.Fatalf("NULL text columns must stay nil: %#v %#v", success.ErrorCode, success.ErrorMessage)
	}
	if success.NextDueAt == nil || *success.NextDueAt != "2026-09-04T10:00:00.000Z" {
		t.Fatalf("next_due_at projection mismatch: %#v", success.NextDueAt)
	}
	failure := outcomes[1]
	if failure.StatusCode != nil {
		t.Fatalf("NULL status_code must stay nil: %#v", failure.StatusCode)
	}
	if failure.ErrorCode == nil || *failure.ErrorCode != "upstream_error" || failure.ErrorMessage == nil || *failure.ErrorMessage != "上游失败" {
		t.Fatalf("error columns mismatch: %#v %#v", failure.ErrorCode, failure.ErrorMessage)
	}
	if failure.NextDueAt != nil {
		t.Fatalf("NULL next_due_at must stay nil: %#v", failure.NextDueAt)
	}
	if j1OutcomeHealthStatus(success) != "success" || j1OutcomeHealthStatus(failure) != "failure" {
		t.Fatalf("health status mapping mismatch")
	}
	if buildJ1OutcomeRows(nil) == nil || len(buildJ1OutcomeRows(nil)) != 0 {
		t.Fatalf("空输入应得空切片")
	}
}

// TestJ1OutcomeRowsRangedOrderEquivalence proves the new read path preserves
// the old merge winner without the SQL LIMIT 1: the ranged rows arrive in
// (observed_at ASC, outcome_id ASC) — exactly what j1PostgresOutcomeQuery
// orders — and the merge layer's >= last-wins must then pick the larger
// outcome_id on an observed_at tie and the newer observed_at otherwise.
// Stale rows stay excluded through both the SQL guard and the merge skip.
func TestJ1OutcomeRowsRangedOrderEquivalence(t *testing.T) {
	location := time.UTC
	hourBuckets := []string{"2026-09-04T10", "2026-09-04T11"}

	// Tie on observed_at inside the 10:00 hour: ranged ASC order puts the
	// smaller outcome_id first, so last-wins must keep out-2 (complete_success).
	tie := buildJ1OutcomeRows([]Row{
		{"outcome_id": "out-1", "account_id": "acct-a", "outcome": "upstream_failure", "storage_observed_at": "2026-09-04T10:30:00.000Z"},
		{"outcome_id": "out-2", "account_id": "acct-a", "outcome": "complete_success", "storage_observed_at": "2026-09-04T10:30:00.000Z"},
	})
	rows := j1OutcomeHealthRows(tie, hourBuckets, location)
	if len(rows) != 1 {
		t.Fatalf("同账户同小时 tie 必须只剩一行: %#v", rows)
	}
	if rows[0].text("status") != "success" {
		t.Fatalf("tie 必须取 outcome_id 较大者（out-2 → success）: %#v", rows[0])
	}

	// Different observed_at across the two buckets: each hour keeps its own
	// latest, and a later stale outcome is dropped by the merge layer too.
	mixed := buildJ1OutcomeRows([]Row{
		{"outcome_id": "out-a", "account_id": "acct-a", "outcome": "complete_success", "storage_observed_at": "2026-09-04T10:15:00.000Z"},
		{"outcome_id": "out-b", "account_id": "acct-a", "outcome": "upstream_failure", "storage_observed_at": "2026-09-04T11:20:00.000Z"},
		{"outcome_id": "out-c", "account_id": "acct-a", "outcome": "stale", "storage_observed_at": "2026-09-04T11:40:00.000Z"},
	})
	rows = j1OutcomeHealthRows(mixed, hourBuckets, location)
	if len(rows) != 2 {
		t.Fatalf("两桶各保留最新非 stale 一行: %#v", rows)
	}
	statusByHour := map[string]string{}
	for _, row := range rows {
		statusByHour[row.text("stat_hour")] = row.text("status")
	}
	if statusByHour["2026-09-04T10"] != "success" || statusByHour["2026-09-04T11"] != "failure" {
		t.Fatalf("不同 observed_at 必须各取较新者且 stale 被丢弃: %#v", statusByHour)
	}
	latest := latestJ1OutcomeForAccount(mixed, "acct-a")
	if latest == nil || latest.OutcomeID != "out-b" {
		t.Fatalf("stale 之后的较新非 stale 才是账户级最新: %#v", latest)
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

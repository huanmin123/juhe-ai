// Tests for the PostgreSQL J1 outcome reader: the incremental per-account
// range contract, the latest-quality probe, the detail hour range, input
// normalization and row building without any payload decode path.
package statreads

import (
	"testing"
	"time"
)

// TestJ1PostgresOutcomeGapQueryContract locks the incremental query: the page
// travels as per-account (account_id, observed_after, observed_before) ranges,
// overlapping ranges collapse, the query is one bounded LATERAL index scan per
// range, non-quality outcomes are filtered in SQL, and only the narrow list
// projection is returned.
func TestJ1PostgresOutcomeGapQueryContract(t *testing.T) {
	query, args, err := j1PostgresOutcomeGapQuery([]j1OutcomeRange{
		{AccountID: "acct-a", ObservedAfter: "2026-09-03T00:00:00.000Z", ObservedBefore: "2026-09-03T01:00:00.000Z"},
		{AccountID: "acct-b", ObservedAfter: "2026-09-03T01:00:00.000Z", ObservedBefore: "2026-09-03T02:00:00.000Z"},
		{AccountID: "acct-a", ObservedAfter: "2026-09-03T00:30:00.000Z", ObservedBefore: "2026-09-03T01:30:00.000Z"},
	})
	if err != nil {
		t.Fatalf("query build: %v", err)
	}
	for _, needle := range []string{
		"juhe_jobs.account_health_outcomes",
		"FROM (VALUES",
		"($1::text, $2::timestamptz, $3::timestamptz)",
		") AS b(account_id, observed_after, observed_before)",
		"CROSS JOIN LATERAL",
		"WHERE account_id = b.account_id",
		"AND observed_at >= b.observed_after",
		"AND observed_at < b.observed_before",
		"outcome NOT IN (",
		"'stale'",
		"'upstream_http_failure'",
		"to_char(timezone('UTC', o.next_due_at), 'YYYY-MM-DD\"T\"HH24:MI:SS.US\"Z\"') AS next_due_at",
		"ORDER BY o.observed_at ASC, o.outcome_id ASC",
	} {
		if !containsText(query, needle) {
			t.Fatalf("query missing %q: %s", needle, query)
		}
	}
	for _, banned := range []string{
		"ANY($1::text[])",
		"AT TIME ZONE",
		"payload->>",
		"LIMIT 1",
		"status_code",
		"request_id",
	} {
		if containsText(query, banned) {
			t.Fatalf("list gap query must not carry %q: %s", banned, query)
		}
	}
	if len(args) != 6 {
		t.Fatalf("argument count wrong: %d", len(args))
	}
	if args[0] != "acct-a" || args[1] != "2026-09-03T00:00:00.000Z" || args[2] != "2026-09-03T01:30:00.000Z" ||
		args[3] != "acct-b" || args[4] != "2026-09-03T01:00:00.000Z" || args[5] != "2026-09-03T02:00:00.000Z" {
		t.Fatalf("ranges must merge per account: %#v", args)
	}
}

func TestJ1PostgresOutcomeGapQueryRejectsInvalidInput(t *testing.T) {
	if _, _, err := j1PostgresOutcomeGapQuery(nil); err == nil {
		t.Fatalf("empty ranges must fail")
	}
	if _, _, err := j1PostgresOutcomeGapQuery(make([]j1OutcomeRange, j1PostgresOutcomeRangeLimit+1)); err == nil {
		t.Fatalf("over-limit ranges must fail")
	}
	if _, _, err := j1PostgresOutcomeGapQuery([]j1OutcomeRange{{AccountID: "", ObservedAfter: "2026-09-03T00:00:00.000Z", ObservedBefore: "2026-09-03T01:00:00.000Z"}}); err == nil {
		t.Fatalf("empty account must fail")
	}
	if _, _, err := j1PostgresOutcomeGapQuery([]j1OutcomeRange{{AccountID: "acct-a", ObservedAfter: "not-a-time", ObservedBefore: "2026-09-03T01:00:00.000Z"}}); err == nil {
		t.Fatalf("invalid start must fail")
	}
	if _, _, err := j1PostgresOutcomeGapQuery([]j1OutcomeRange{{AccountID: "acct-a", ObservedAfter: "2026-09-03T00:00:00.000Z", ObservedBefore: "not-a-time"}}); err == nil {
		t.Fatalf("invalid end must fail")
	}
	if _, _, err := j1PostgresOutcomeGapQuery([]j1OutcomeRange{{AccountID: "acct-a", ObservedAfter: "2026-09-03T01:00:00.000Z", ObservedBefore: "2026-09-03T01:00:00.000Z"}}); err == nil {
		t.Fatalf("empty range must fail")
	}
	if _, _, err := j1PostgresOutcomeGapQuery([]j1OutcomeRange{{AccountID: "acct-a", ObservedAfter: "2026-09-03T00:00:00.000Z", ObservedBefore: "2026-09-03T01:00:00.000Z"}}); err != nil {
		t.Fatalf("valid input must build: %v", err)
	}
}

// TestJ1PostgresLatestQualityQueryContract pins the per-account header read:
// one backward index scan per account, bounded by the same half-open window as
// the gap read, and non-quality rows filtered before LIMIT 1 so a non-quality
// row can never become the displayed latest check.
func TestJ1PostgresLatestQualityQueryContract(t *testing.T) {
	query, args, err := j1PostgresLatestQualityQuery(
		[]string{"acct-a", "acct-b", "acct-a"},
		"2026-09-03T00:00:00.000Z", "2026-09-10T02:00:00.000Z")
	if err != nil {
		t.Fatalf("query build: %v", err)
	}
	for _, needle := range []string{
		"juhe_jobs.account_health_outcomes",
		"FROM (VALUES",
		"($1::text), ($2::text)",
		"CROSS JOIN LATERAL",
		"observed_at >= $3::timestamptz",
		"observed_at < $4::timestamptz",
		"outcome NOT IN (",
		"ORDER BY observed_at DESC, outcome_id DESC",
		"LIMIT 1",
	} {
		if !containsText(query, needle) {
			t.Fatalf("query missing %q: %s", needle, query)
		}
	}
	if len(args) != 4 || args[0] != "acct-a" || args[1] != "acct-b" ||
		args[2] != "2026-09-03T00:00:00.000Z" || args[3] != "2026-09-10T02:00:00.000Z" {
		t.Fatalf("latest query args wrong: %#v", args)
	}
	if _, _, err := j1PostgresLatestQualityQuery(make([]string, 51), "2026-09-03T00:00:00.000Z", "2026-09-10T02:00:00.000Z"); err == nil {
		t.Fatalf("51 accounts must fail")
	}
	if _, _, err := j1PostgresLatestQualityQuery(nil, "2026-09-03T00:00:00.000Z", "2026-09-10T02:00:00.000Z"); err == nil {
		t.Fatalf("empty accounts must fail")
	}
	if _, _, err := j1PostgresLatestQualityQuery([]string{"acct-a"}, "not-a-time", "2026-09-10T02:00:00.000Z"); err == nil {
		t.Fatalf("invalid window start must fail")
	}
	if _, _, err := j1PostgresLatestQualityQuery([]string{"acct-a"}, "2026-09-03T00:00:00.000Z", "not-a-time"); err == nil {
		t.Fatalf("invalid window end must fail")
	}
	if _, _, err := j1PostgresLatestQualityQuery([]string{"acct-a"}, "2026-09-10T02:00:00.000Z", "2026-09-03T00:00:00.000Z"); err == nil {
		t.Fatalf("inverted window must fail")
	}
}

// TestJ1PostgresOutcomeHourQueryContract pins the detail read: the full entity
// projection stays available, but the range is exactly one hour and non-quality
// rows are filtered before the merge sees them.
func TestJ1PostgresOutcomeHourQueryContract(t *testing.T) {
	query, args, err := j1PostgresOutcomeHourQuery(
		"acct-a", "2026-09-04T10:00:00.000Z", "2026-09-04T11:00:00.000Z")
	if err != nil {
		t.Fatalf("query build: %v", err)
	}
	for _, needle := range []string{
		"juhe_jobs.account_health_outcomes",
		"request_id",
		"status_code::bigint",
		"error_code",
		"error_message",
		"WHERE account_id = $1::text",
		"observed_at >= $2::timestamptz",
		"observed_at < $3::timestamptz",
		"outcome NOT IN (",
		"ORDER BY observed_at ASC, outcome_id ASC",
	} {
		if !containsText(query, needle) {
			t.Fatalf("query missing %q: %s", needle, query)
		}
	}
	for _, banned := range []string{"ANY($1::text[])", "LATERAL", "LIMIT 1", "payload->>"} {
		if containsText(query, banned) {
			t.Fatalf("hour query must not carry %q: %s", banned, query)
		}
	}
	if len(args) != 3 || args[0] != "acct-a" || args[1] != "2026-09-04T10:00:00.000Z" || args[2] != "2026-09-04T11:00:00.000Z" {
		t.Fatalf("hour query args wrong: %#v", args)
	}
	if _, _, err := j1PostgresOutcomeHourQuery("", "2026-09-04T10:00:00.000Z", "2026-09-04T11:00:00.000Z"); err == nil {
		t.Fatalf("empty account must fail")
	}
	if _, _, err := j1PostgresOutcomeHourQuery("acct-a", "bad", "2026-09-04T11:00:00.000Z"); err == nil {
		t.Fatalf("invalid start must fail")
	}
	if _, _, err := j1PostgresOutcomeHourQuery("acct-a", "2026-09-04T10:00:00.000Z", "bad"); err == nil {
		t.Fatalf("invalid end must fail")
	}
}

func TestJ1NonQualityOutcomeSQLPredicateCoversMap(t *testing.T) {
	if !containsText(j1NonQualityOutcomeSQLPredicate, "outcome NOT IN (") {
		t.Fatalf("predicate shape wrong: %s", j1NonQualityOutcomeSQLPredicate)
	}
	for outcome := range j1NonQualityOutcomes {
		if !containsText(j1NonQualityOutcomeSQLPredicate, "'"+outcome+"'") {
			t.Fatalf("predicate missing non-quality outcome %q: %s", outcome, j1NonQualityOutcomeSQLPredicate)
		}
	}
}

// TestBuildJ1OutcomeRows locks the entity-column mapping: typed columns land
// on the j1Outcome projection the merge consumes; nullable columns stay nil
// on NULL, and next_due_at arrives as the jobs-owned entity column.
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

// TestJ1OutcomeRangesForHourlyRows pins the per-account range rules: covered
// accounts only read the freshness overlap; an isolated missing hour yields a
// separate range; missing hours adjacent to the freshness range merge; no
// hourly rows fall back to the full window; a malformed hourly timestamp
// forces the whole account back to the full window; duplicate page account IDs
// collapse.
func TestJ1OutcomeRangesForHourlyRows(t *testing.T) {
	hourBuckets := []string{"2026-09-04T10", "2026-09-04T11", "2026-09-04T12"}
	windowStart := "2026-09-04T07:00:00.000Z"
	windowEnd := "2026-09-04T14:00:00.000Z"
	freshnessFloor := "2026-09-04T12:00:00.000Z"
	rows := []Row{
		{"account_id": "acct-covered", "stat_hour": "2026-09-04T10", "last_observed_at": "2026-09-04T10:30:00.000Z"},
		{"account_id": "acct-covered", "stat_hour": "2026-09-04T11", "last_observed_at": "2026-09-04T11:45:00.000Z"},
		{"account_id": "acct-covered", "stat_hour": "2026-09-04T12", "last_observed_at": "2026-09-04T12:05:00.000Z"},

		{"account_id": "acct-missing", "stat_hour": "2026-09-04T11", "last_observed_at": "2026-09-04T11:40:00.000Z"},
		{"account_id": "acct-missing", "stat_hour": "2026-09-04T12", "last_observed_at": "2026-09-04T12:10:00.000Z"},

		{"account_id": "acct-fresh", "stat_hour": "2026-09-04T10", "last_observed_at": "2026-09-04T10:20:00.000Z"},
		{"account_id": "acct-fresh", "stat_hour": "2026-09-04T11", "last_observed_at": "2026-09-04T11:40:00.000Z"},

		{"account_id": "acct-bad", "stat_hour": "2026-09-04T10", "last_observed_at": "not-a-time"},
		{"account_id": "acct-bad", "stat_hour": "2026-09-04T11", "last_observed_at": "2026-09-04T11:30:00.000Z"},
	}
	ranges := j1OutcomeRangesForHourlyRows(
		[]string{"acct-covered", "acct-missing", "acct-fresh", "acct-empty", "acct-bad", "acct-missing"},
		hourBuckets, rows, windowStart, windowEnd, freshnessFloor, time.UTC)
	got := map[string][]j1OutcomeRange{}
	for _, r := range ranges {
		got[r.AccountID] = append(got[r.AccountID], r)
	}
	want := map[string][][2]string{
		"acct-covered": {{"2026-09-04T12:00:00.000Z", windowEnd}},
		"acct-missing": {
			{"2026-09-04T10:00:00.000Z", "2026-09-04T11:00:00.000Z"},
			{"2026-09-04T12:00:00.000Z", windowEnd},
		},
		"acct-fresh": {{"2026-09-04T11:40:00.000Z", windowEnd}},
		"acct-empty": {{windowStart, windowEnd}},
		"acct-bad":   {{windowStart, windowEnd}},
	}
	if len(got) != len(want) {
		t.Fatalf("range account count wrong: %#v", ranges)
	}
	for accountID, wantRanges := range want {
		actual := got[accountID]
		if len(actual) != len(wantRanges) {
			t.Fatalf("account %s range count = %d, want %d (all: %#v)", accountID, len(actual), len(wantRanges), ranges)
		}
		for index, wantRange := range wantRanges {
			if actual[index].ObservedAfter != wantRange[0] || actual[index].ObservedBefore != wantRange[1] {
				t.Fatalf("account %s range %d = [%s, %s), want [%s, %s)",
					accountID, index, actual[index].ObservedAfter, actual[index].ObservedBefore, wantRange[0], wantRange[1])
			}
		}
	}
}

func TestMergeJ1OutcomeRanges(t *testing.T) {
	merged := mergeJ1OutcomeRanges([]j1OutcomeRange{
		{AccountID: "acct-a", ObservedAfter: "2026-09-04T11:00:00.000Z", ObservedBefore: "2026-09-04T12:00:00.000Z"},
		{AccountID: "acct-a", ObservedAfter: "2026-09-04T10:00:00.000Z", ObservedBefore: "2026-09-04T10:30:00.000Z"},
		{AccountID: "acct-a", ObservedAfter: "2026-09-04T11:30:00.000Z", ObservedBefore: "2026-09-04T13:00:00.000Z"},
	})
	if len(merged) != 2 || merged[0].ObservedAfter != "2026-09-04T10:00:00.000Z" ||
		merged[0].ObservedBefore != "2026-09-04T10:30:00.000Z" ||
		merged[1].ObservedAfter != "2026-09-04T11:00:00.000Z" ||
		merged[1].ObservedBefore != "2026-09-04T13:00:00.000Z" {
		t.Fatalf("overlapping ranges must merge: %#v", merged)
	}
	adjacent := mergeJ1OutcomeRanges([]j1OutcomeRange{
		{AccountID: "acct-a", ObservedAfter: "2026-09-04T10:00:00.000Z", ObservedBefore: "2026-09-04T11:00:00.000Z"},
		{AccountID: "acct-a", ObservedAfter: "2026-09-04T11:00:00.000Z", ObservedBefore: "2026-09-04T12:00:00.000Z"},
	})
	if len(adjacent) != 1 || adjacent[0].ObservedBefore != "2026-09-04T12:00:00.000Z" {
		t.Fatalf("adjacent ranges must merge: %#v", adjacent)
	}
	if got := mergeJ1OutcomeRanges(nil); got != nil {
		t.Fatalf("nil ranges must stay nil: %#v", got)
	}
}

func TestJ1AccountsNeedingLatestProbe(t *testing.T) {
	windowStart := "2026-09-04T07:00:00.000Z"
	windowEnd := "2026-09-04T14:00:00.000Z"
	ranges := []j1OutcomeRange{
		// An earlier gap row does not prove the newest quality outcome is in
		// the gap: a covered hour between ranges may hold a newer one.
		{AccountID: "acct-with-gap", ObservedAfter: "2026-09-04T11:00:00.000Z", ObservedBefore: "2026-09-04T12:00:00.000Z"},
		{AccountID: "acct-with-gap", ObservedAfter: "2026-09-04T12:00:00.000Z", ObservedBefore: windowEnd},
		{AccountID: "acct-needs-latest", ObservedAfter: "2026-09-04T11:00:00.000Z", ObservedBefore: "2026-09-04T12:00:00.000Z"},
		{AccountID: "acct-needs-latest", ObservedAfter: "2026-09-04T12:00:00.000Z", ObservedBefore: windowEnd},
		{AccountID: "acct-full-window", ObservedAfter: windowStart, ObservedBefore: windowEnd},
		// Duplicate ranges must still yield one account id.
		{AccountID: "acct-with-gap", ObservedAfter: "2026-09-04T11:00:00.000Z", ObservedBefore: windowEnd},
	}
	missing := j1AccountsNeedingLatestProbe(ranges, windowStart, windowEnd)
	if len(missing) != 2 || missing[0] != "acct-with-gap" || missing[1] != "acct-needs-latest" {
		t.Fatalf("latest-probe accounts wrong: %#v", missing)
	}
	if got := j1AccountsNeedingLatestProbe([]j1OutcomeRange{
		{AccountID: "acct-full-window", ObservedAfter: windowStart, ObservedBefore: windowEnd},
	}, windowStart, windowEnd); len(got) != 0 {
		t.Fatalf("full-window account must skip the latest query: %#v", got)
	}
}

func TestHourBucketStartEndUTCHonorsStatsTimezone(t *testing.T) {
	location := time.FixedZone("CST", 8*3600)
	if got := hourBucketStartUTC("2026-09-04T10", location); got != "2026-09-04T02:00:00.000Z" {
		t.Fatalf("hour bucket start = %q, want 2026-09-04T02:00:00.000Z", got)
	}
	if got := hourBucketEndUTC("2026-09-04T10", location); got != "2026-09-04T03:00:00.000Z" {
		t.Fatalf("hour bucket end = %q, want 2026-09-04T03:00:00.000Z", got)
	}
	if got := hourBucketStartUTC("not-an-hour", location); got != "" {
		t.Fatalf("invalid hour bucket must yield empty start: %q", got)
	}
	if got := hourBucketEndUTC("not-an-hour", location); got != "" {
		t.Fatalf("invalid hour bucket must yield empty end: %q", got)
	}
}

func TestJ1OutcomeRangesForHourlyRowsKeepsManyIsolatedGaps(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	const hours = 744
	hourBuckets := make([]string, hours)
	for index := 0; index < hours; index++ {
		hourBuckets[index] = start.Add(time.Duration(index) * time.Hour).Format("2006-01-02T15")
	}
	accountIds := []string{"acct-1", "acct-2", "acct-3", "acct-4", "acct-5", "acct-6"}
	rows := make([]Row, 0, len(accountIds)*hours/2)
	for _, accountID := range accountIds {
		// Every other hour is present, so each account contributes 372
		// isolated missing-hour/freshness ranges; the page total exceeds the
		// per-query chunk size and must stay incremental instead of falling
		// back to a full-window read.
		for index := 0; index < hours; index += 2 {
			observed := start.Add(time.Duration(index)*time.Hour + 10*time.Minute)
			rows = append(rows, Row{
				"account_id":       accountID,
				"stat_hour":        hourBuckets[index],
				"last_observed_at": observed.Format("2006-01-02T15:04:05.000Z"),
			})
		}
	}
	windowStart := start.Format("2006-01-02T15:04:05.000Z")
	windowEnd := start.Add(hours * time.Hour).Format("2006-01-02T15:04:05.000Z")
	// A freshness floor at the window start would produce a full-window
	// freshness range that merges every missing-hour gap, so the range count
	// would never exceed the chunk size. Use the window end to keep the
	// freshness range near the tail and let the isolated gaps count.
	ranges := j1OutcomeRangesForHourlyRows(accountIds, hourBuckets, rows, windowStart, windowEnd, windowEnd, time.UTC)
	if len(ranges) != len(accountIds)*372 {
		t.Fatalf("isolated range count = %d, want %d", len(ranges), len(accountIds)*372)
	}
	for _, r := range ranges {
		if r.ObservedAfter == windowStart && r.ObservedBefore == windowEnd {
			t.Fatalf("range set must not fall back to full window: %#v", r)
		}
	}
	chunks := j1OutcomeRangeChunks(ranges, j1PostgresOutcomeRangeLimit)
	if len(chunks) != 2 {
		t.Fatalf("range chunk count = %d, want 2", len(chunks))
	}
	if len(chunks[0]) != j1PostgresOutcomeRangeLimit || len(chunks[1]) != len(ranges)-j1PostgresOutcomeRangeLimit {
		t.Fatalf("range chunks wrong: sizes=%d/%d", len(chunks[0]), len(chunks[1]))
	}
	flattened := append(append([]j1OutcomeRange{}, chunks[0]...), chunks[1]...)
	if len(flattened) != len(ranges) || flattened[0] != ranges[0] || flattened[len(flattened)-1] != ranges[len(ranges)-1] {
		t.Fatalf("chunking must preserve range order and content")
	}
}

func TestLocalHourBoundsUTC(t *testing.T) {
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("加载时区失败: %v", err)
	}
	chatham, err := time.LoadLocation("Pacific/Chatham")
	if err != nil {
		t.Skipf("加载时区失败: %v", err)
	}
	cases := []struct {
		name      string
		hour      string
		location  *time.Location
		wantStart string
		wantEnd   string
		wantOK    bool
	}{
		{"utc normal", "2026-09-04T10", time.UTC, "2026-09-04T10:00:00.000Z", "2026-09-04T11:00:00.000Z", true},
		{"nil location", "2026-09-04T10", nil, "2026-09-04T10:00:00.000Z", "2026-09-04T11:00:00.000Z", true},
		{"ny fall-back repeated hour", "2026-11-01T01", newYork, "2026-11-01T05:00:00.000Z", "2026-11-01T07:00:00.000Z", true},
		{"ny spring-forward before gap", "2026-03-08T01", newYork, "2026-03-08T06:00:00.000Z", "2026-03-08T07:00:00.000Z", true},
		{"ny spring-forward skipped hour", "2026-03-08T02", newYork, "", "", false},
		{"chatham fall-back repeated hour", "2026-04-05T02", chatham, "2026-04-04T12:15:00.000Z", "2026-04-04T14:15:00.000Z", true},
		{"chatham spring-forward partial hour", "2026-09-27T03", chatham, "2026-09-26T14:00:00.000Z", "2026-09-26T14:15:00.000Z", true},
		{"out of range year", "9999-12-31T23", time.UTC, "", "", false},
		{"zero year", "0000-01-01T00", time.UTC, "", "", false},
		{"invalid calendar date", "2026-02-30T10", time.UTC, "", "", false},
	}
	for _, test := range cases {
		start, end, ok := localHourBoundsUTC(test.hour, test.location)
		if ok != test.wantOK {
			t.Fatalf("%s ok = %v, want %v", test.name, ok, test.wantOK)
		}
		if !ok {
			continue
		}
		if got := start.UTC().Format("2006-01-02T15:04:05.000Z"); got != test.wantStart {
			t.Fatalf("%s start = %q, want %q", test.name, got, test.wantStart)
		}
		if got := end.UTC().Format("2006-01-02T15:04:05.000Z"); got != test.wantEnd {
			t.Fatalf("%s end = %q, want %q", test.name, got, test.wantEnd)
		}
	}
}

func TestHourBucketsUntilNowCollapsesFallBackHour(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("加载时区失败: %v", err)
	}
	// 2026-11-01T08:30Z is 03:30 EST; the five absolute hours back cross the
	// repeated 01:00 local hour and must collapse to one display bucket.
	now := time.Date(2026, 11, 1, 8, 30, 0, 0, time.UTC)
	buckets := hourBucketsUntilNow(5, now, location)
	want := []string{"2026-11-01T00", "2026-11-01T01", "2026-11-01T02", "2026-11-01T03"}
	if len(buckets) != len(want) {
		t.Fatalf("fall-back buckets = %#v, want %#v", buckets, want)
	}
	for index := range want {
		if buckets[index] != want[index] {
			t.Fatalf("fall-back bucket %d = %q, want %q", index, buckets[index], want[index])
		}
	}
}

func TestJ1OutcomeRangesForHourlyRowsCoversDSTFallBackHour(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("加载时区失败: %v", err)
	}
	// 2026-11-01 01:00 occurs twice in New York. The display bucket aggregates
	// both occurrences, so the missing-hour range must end at 02:00 EST
	// (07:00Z), not at the end of the first occurrence (06:00Z).
	hourBuckets := []string{"2026-11-01T00", "2026-11-01T01", "2026-11-01T02"}
	rows := []Row{
		{"account_id": "acct-a", "stat_hour": "2026-11-01T00", "last_observed_at": "2026-11-01T04:10:00.000Z"},
		{"account_id": "acct-a", "stat_hour": "2026-11-01T02", "last_observed_at": "2026-11-01T07:10:00.000Z"},
	}
	ranges := j1OutcomeRangesForHourlyRows(
		[]string{"acct-a"}, hourBuckets, rows,
		"2026-11-01T03:00:00.000Z", "2026-11-01T09:00:00.000Z", "2026-11-01T08:00:00.000Z", location)
	if len(ranges) != 2 {
		t.Fatalf("DST fall-back ranges wrong: %#v", ranges)
	}
	if ranges[0].ObservedAfter != "2026-11-01T05:00:00.000Z" || ranges[0].ObservedBefore != "2026-11-01T07:00:00.000Z" {
		t.Fatalf("ambiguous hour range = [%s, %s), want [2026-11-01T05:00:00.000Z, 2026-11-01T07:00:00.000Z)",
			ranges[0].ObservedAfter, ranges[0].ObservedBefore)
	}
	if ranges[1].ObservedAfter != "2026-11-01T07:10:00.000Z" || ranges[1].ObservedBefore != "2026-11-01T09:00:00.000Z" {
		t.Fatalf("freshness range wrong: %#v", ranges[1])
	}
}

func TestBuildJ1ListOutcomeRowsKeepsNarrowProjection(t *testing.T) {
	nextDue := "2026-09-04T13:00:00.000Z"
	rows := []Row{{
		"outcome_id": "out-1", "account_id": "acct-a", "outcome": "complete_success",
		"storage_observed_at": "2026-09-04T12:10:00.000Z", "next_due_at": nextDue,
		"request_id": "ignored", "status_code": int64(200), "error_message": "ignored",
	}}
	outcomes := buildJ1ListOutcomeRows(rows)
	if len(outcomes) != 1 {
		t.Fatalf("outcome count wrong: %#v", outcomes)
	}
	outcome := outcomes[0]
	if outcome.OutcomeID != "out-1" || outcome.AccountID != "acct-a" || outcome.Outcome != "complete_success" ||
		outcome.ObservedAt != "2026-09-04T12:10:00.000Z" || outcome.NextDueAt == nil || *outcome.NextDueAt != nextDue {
		t.Fatalf("narrow projection mismatch: %#v", outcome)
	}
	if outcome.RequestID != "" || outcome.StatusCode != nil || outcome.ErrorCode != nil || outcome.ErrorMessage != nil {
		t.Fatalf("list projection must stay narrow: %#v", outcome)
	}
}

// TestJ1OutcomeRowsRangedOrderEquivalence proves the new read path preserves
// the old merge winner without a SQL LIMIT 1: the ranged rows arrive in
// (observed_at ASC, outcome_id ASC) — exactly what the gap query orders — and
// the merge layer's >= last-wins must then pick the larger outcome_id on an
// observed_at tie and the newer observed_at otherwise. Stale rows stay
// excluded through both the SQL predicate and the merge skip.
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

// TestJ1OutcomeHealthStatusExcludesNonQualityOutcomes pins C1-H1: the AI-health
// rate only counts quality outcomes. complete_success is the sole positive
// sample; stale and the five non-quality terminal outcomes only feed
// diagnostics (docs/functions/模型价格与用量统计口径.md §2) and must map to the
// "not counted" arm; real account-quality failures keep the failure arm.
func TestJ1OutcomeHealthStatusExcludesNonQualityOutcomes(t *testing.T) {
	cases := []struct {
		outcome string
		want    string
	}{
		{"complete_success", "success"},
		{"upstream_failure", "failure"},
		{"probe_task_failure", "failure"},
		// Attribution-shaped failures are still real account-quality failures.
		{"account_upstream", "failure"},
		{"account_dependency", "failure"},
		{"unexpected_outcome", "failure"},
		{"stale", ""},
		{"upstream_http_failure", ""},
		{"framing_complete_neutral", ""},
		{"opaque_upstream", ""},
		{"gateway_capacity", ""},
		{"gateway_policy", ""},
		{"downstream_closed", ""},
	}
	for _, tc := range cases {
		if got := j1OutcomeHealthStatus(j1Outcome{Outcome: tc.outcome}); got != tc.want {
			t.Fatalf("outcome %q health status = %q, want %q", tc.outcome, got, tc.want)
		}
	}
}

// TestJ1NonQualityOutcomesStayOutOfHealthRate proves the merge and aggregation
// layers agree with the mapping: non-quality outcomes produce no hourly row, never
// become the latest/displayed status, and leave the hour unknown instead of
// counting it as a failure hour.
func TestJ1NonQualityOutcomesStayOutOfHealthRate(t *testing.T) {
	location := time.UTC
	hourBuckets := []string{
		"2026-09-04T00", "2026-09-04T01", "2026-09-04T02", "2026-09-04T03",
		"2026-09-04T04", "2026-09-04T05", "2026-09-04T06", "2026-09-04T07",
	}
	outcomes := []j1Outcome{
		{AccountID: "acct-a", Outcome: "complete_success", ObservedAt: "2026-09-04T00:30:00.000Z"},
		{AccountID: "acct-a", Outcome: "upstream_failure", ObservedAt: "2026-09-04T01:30:00.000Z"},
		{AccountID: "acct-a", Outcome: "upstream_http_failure", ObservedAt: "2026-09-04T02:30:00.000Z"},
		{AccountID: "acct-a", Outcome: "opaque_upstream", ObservedAt: "2026-09-04T03:30:00.000Z"},
		{AccountID: "acct-a", Outcome: "gateway_capacity", ObservedAt: "2026-09-04T04:30:00.000Z"},
		{AccountID: "acct-a", Outcome: "gateway_policy", ObservedAt: "2026-09-04T05:30:00.000Z"},
		{AccountID: "acct-a", Outcome: "downstream_closed", ObservedAt: "2026-09-04T06:30:00.000Z"},
		{AccountID: "acct-a", Outcome: "stale", ObservedAt: "2026-09-04T07:30:00.000Z"},
	}
	rows := j1OutcomeHealthRows(outcomes, hourBuckets, location)
	if len(rows) != 2 {
		t.Fatalf("只有 success/failure 两个质量小时可成行: %#v", rows)
	}
	result := mapAiHealthList([]aiHealthAccount{{ID: "acct-a"}}, false,
		aiHealthOptions{Page: 1, PageSize: 20}, rows, hourBuckets, outcomes)
	if len(result.Items) != 1 {
		t.Fatalf("账户行数错误: %#v", result.Items)
	}
	item := result.Items[0]
	if item.SuccessHours != 1 || item.FailureHours != 1 {
		t.Fatalf("非质量/stale 不得计入成功或失败小时: %+v", item)
	}
	if item.UnknownHours != 6 {
		t.Fatalf("非质量终态所在小时应为 unknown: %+v", item)
	}
	if item.HealthRate == nil || *item.HealthRate != 50 {
		t.Fatalf("健康率只按 1 成功 / 1 失败计算: %#v", item.HealthRate)
	}
	if item.LatestStatus != "failure" {
		t.Fatalf("最新状态必须是最后的真实质量终态: %q", item.LatestStatus)
	}
	latest := latestJ1OutcomeForAccount(outcomes, "acct-a")
	if latest == nil || latest.Outcome != "upstream_failure" {
		t.Fatalf("非质量/stale 不得成为账户最新质量终态: %#v", latest)
	}
	if got := newestJ1OutcomeHourRow(outcomes, "2026-09-04T06", location); got != nil {
		t.Fatalf("小时详情不得把非质量终态当作健康结论: %#v", got)
	}
	if got := newestJ1OutcomeHourRow(outcomes, "2026-09-04T01", location); got == nil || got.Outcome != "upstream_failure" {
		t.Fatalf("小时详情应保留真实失败结论: %#v", got)
	}
}

package postgres

import (
	"context"
	"errors"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

func TestW6ManagementClientIPStatsMigrationAddsOnlyReaderTables(t *testing.T) {
	source, err := os.ReadFile("../../../db/migrations/000040_w6_management_client_ip_stats_list.sql")
	if err != nil {
		t.Fatalf("read client IP stats migration: %v", err)
	}
	sql := string(source)
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS juhe_stats.client_ip_usage_range_windows",
		"CREATE TABLE IF NOT EXISTS juhe_stats.client_ip_range_window_dirty_ips",
		"CREATE TABLE IF NOT EXISTS juhe_stats.client_ip_account_range_window_dirty_ips",
		"request_count bigint NOT NULL DEFAULT 0",
		"average_duration_ms double precision",
		"last_used_at text",
		"PRIMARY KEY (ip_hash, start_date, end_date)",
		"idx_client_ip_range_requests",
		"idx_client_ip_range_dirty_updated",
		"idx_client_ip_account_range_dirty_updated",
		"idx_client_ip_policies_ip",
		"aggregate_ip_key COLLATE \"C\"",
		"client_ip COLLATE \"C\"",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("client IP stats migration missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"client_ip_stats_daily",
		"client_ip_account_stats_daily",
		"client_ip_account_usage_range_windows",
		"ALTER TABLE",
		"DROP TABLE",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("reader migration must not add or mutate %q", forbidden)
		}
	}
}

func TestW6ManagementClientIPStatsSQLIsPreaggregatedBoundedAndStatic(t *testing.T) {
	source, err := os.ReadFile("queries/w6_management_client_ip_stats.sql")
	if err != nil {
		t.Fatalf("read client IP stats SQL: %v", err)
	}
	sql := string(source)
	readySQL := managementClientIPStatsNamedSQLSection(
		t,
		sql,
		"ManagementClientIPStatsRangeReady",
	)
	for _, required := range []string{
		"client_ip_range_window_dirty_ips",
		"client_ip_account_range_window_dirty_ips",
		"stats_job_state",
		"scope_type = 'client_ip_range_window'",
		"job_name = 'client_ip_range_window_refresh'",
		"last_success_at IS NOT NULL",
		"last_success_at <> ''",
		"ELSE EXISTS",
		"client_ip_usage_range_windows",
	} {
		if !strings.Contains(readySQL, required) {
			t.Fatalf("range readiness SQL missing %q:\n%s", required, readySQL)
		}
	}

	listSQL := managementClientIPStatsNamedSQLSection(t, sql, "ListManagementClientIPStats")
	for _, required := range []string{
		"FROM juhe_stats.client_ip_usage_range_windows AS range_stats",
		"INNER JOIN juhe_stats.client_ip_registry AS registry",
		"active_policies.status = 'active'",
		"active_policies.expires_at > sqlc.arg(policy_now)::text",
		"starts_with(registry.aggregate_ip_key, sqlc.arg(keyword)::text)",
		"starts_with(registry.client_ip, sqlc.arg(keyword)::text)",
		"COLLATE \"C\"",
		"sqlc.arg(status_filter)::text = 'normal'",
		"CASE WHEN sqlc.arg(sort_field)::text = 'errorRate'",
		"CASE WHEN sqlc.arg(sort_field)::text = 'lastUsedAt'",
		"LIMIT sqlc.arg(row_limit)::int",
		"OFFSET sqlc.arg(row_offset)::int",
	} {
		if !strings.Contains(listSQL, required) {
			t.Fatalf("client IP stats list SQL missing %q", required)
		}
	}
	for _, field := range []string{
		"requestCount",
		"successCount",
		"errorCount",
		"errorRate",
		"totalTokens",
		"totalCost",
		"activeDays",
		"lastUsedAt",
	} {
		if !strings.Contains(listSQL, "= '"+field+"'") {
			t.Fatalf("client IP stats list SQL lacks static sort branch for %q", field)
		}
	}
	lowerSQL := strings.ToLower(sql)
	for _, forbidden := range []string{
		"client_ip_stats_daily",
		"client_ip_account_stats_daily",
		"usage_records",
		"count(",
		"sum(",
		"group by",
	} {
		if strings.Contains(lowerSQL, forbidden) {
			t.Fatalf("client IP stats request SQL must not contain %q", forbidden)
		}
	}
}

func TestListManagementClientIPStatsShortCircuitsWhenRangeIsNotReady(t *testing.T) {
	q := &managementClientIPStatsQueriesStub{ready: false}
	page, err := listManagementClientIPStats(
		context.Background(),
		q,
		port.ManagementClientIPStatsListInput{
			StartDate: "2026-07-01",
			EndDate:   "2026-07-07",
			Limit:     21,
		},
	)
	if err != nil {
		t.Fatalf("listManagementClientIPStats() error = %v", err)
	}
	if page.RangeReady || page.HasMore || len(page.Rows) != 0 {
		t.Fatalf("not-ready page = %+v", page)
	}
	if len(q.readyCalls) != 1 || len(q.listCalls) != 0 {
		t.Fatalf("ready/list calls = %d/%d", len(q.readyCalls), len(q.listCalls))
	}
	if q.readyCalls[0].StartDate != "2026-07-01" || q.readyCalls[0].EndDate != "2026-07-07" {
		t.Fatalf("ready params = %+v", q.readyCalls[0])
	}
}

func TestListManagementClientIPStatsMapsRowsAndUsesProbeLimit(t *testing.T) {
	lastUsedStart := time.Date(2026, 7, 1, 0, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	lastUsedEnd := time.Date(2026, 7, 8, 0, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	lastUsedAt := "2026-07-07T05:00:00.000Z"
	lastErrorAt := "2026-07-06T05:00:00.000Z"
	q := &managementClientIPStatsQueriesStub{
		ready: true,
		rows: []postgresqueries.ListManagementClientIPStatsRow{
			{
				IpHash:              "hash_1",
				AggregateIpKey:      "203.0.113.8",
				RegistryLastSeenAt:  "2026-07-07T06:00:00.000Z",
				RequestCount:        4,
				SuccessCount:        3,
				ErrorCount:          1,
				ErrorRate:           0.25,
				InputTokens:         5,
				OutputTokens:        6,
				TotalTokens:         11,
				CacheReadTokens:     7,
				CacheReadCostUsd:    0.1,
				CacheWriteTokens:    8,
				CacheWrite1hTokens:  9,
				CacheWriteCostUsd:   0.2,
				ThinkingTokens:      10,
				InputImageTokens:    11,
				OutputImageTokens:   12,
				TotalCostUsd:        0.3,
				DurationMsSum:       300,
				DurationMsCount:     2,
				DurationMsMax:       200,
				FirstTokenMsSum:     40,
				FirstTokenMsCount:   2,
				AverageFirstTokenMs: pgtype.Float8{Float64: 12.5, Valid: true},
				ActiveDays:          2,
				LastUsedAt:          pgtype.Text{String: lastUsedAt, Valid: true},
				LastErrorAt:         pgtype.Text{String: lastErrorAt, Valid: true},
				Blacklisted:         true,
				Allowlisted:         true,
			},
			{
				IpHash:             "hash_2",
				AggregateIpKey:     "203.0.113.9",
				RegistryLastSeenAt: "2026-07-07T04:00:00.000Z",
				Allowlisted:        true,
			},
			{IpHash: "probe_row"},
		},
	}

	page, err := listManagementClientIPStats(
		context.Background(),
		q,
		port.ManagementClientIPStatsListInput{
			StartDate:              "2026-07-01",
			EndDate:                "2026-07-07",
			Keyword:                " 203.0.113 ",
			Status:                 port.ManagementClientIPStatsStatusNormal,
			LastUsedStartAt:        &lastUsedStart,
			LastUsedEndExclusiveAt: &lastUsedEnd,
			SortField:              port.ManagementClientIPStatsSortTotalTokens,
			SortOrder:              port.ManagementClientIPStatsSortAscending,
			Now:                    time.Date(2026, 7, 7, 16, 30, 0, 123456789, time.FixedZone("UTC+8", 8*60*60)),
			Limit:                  3,
			Offset:                 2_000,
		},
	)
	if err != nil {
		t.Fatalf("listManagementClientIPStats() error = %v", err)
	}
	if !page.RangeReady || !page.HasMore || len(page.Rows) != 2 {
		t.Fatalf("page = %+v", page)
	}
	if len(q.listCalls) != 1 {
		t.Fatalf("list calls = %d", len(q.listCalls))
	}
	call := q.listCalls[0]
	if call.PolicyNow != "2026-07-07T08:30:00.123Z" ||
		call.StartDate != "2026-07-01" ||
		call.EndDate != "2026-07-07" ||
		!call.HasLastUsedRange ||
		call.LastUsedStartAt != "2026-06-30T16:00:00.000Z" ||
		call.LastUsedEndExclusiveAt != "2026-07-07T16:00:00.000Z" ||
		call.Keyword != "203.0.113" ||
		call.KeywordUpper != "203.0.114" ||
		call.StatusFilter != "normal" ||
		call.SortField != "totalTokens" ||
		call.SortOrder != "asc" ||
		call.RowLimit != 3 ||
		call.RowOffset != 999 {
		t.Fatalf("list params = %+v", call)
	}
	first := page.Rows[0]
	if first.IPHash != "hash_1" ||
		first.Status != port.ManagementClientIPStatsStatusBlacklisted ||
		first.RangeUsage.TotalTokens != 11 ||
		first.RangeUsage.AverageDurationMs == nil ||
		*first.RangeUsage.AverageDurationMs != 150 ||
		first.RangeUsage.AverageFirstTokenMs == nil ||
		*first.RangeUsage.AverageFirstTokenMs != 12.5 ||
		first.RangeUsage.MaxDurationMs == nil ||
		*first.RangeUsage.MaxDurationMs != 200 ||
		first.RangeUsage.LastUsedAt == nil ||
		*first.RangeUsage.LastUsedAt != lastUsedAt ||
		first.RangeUsage.LastErrorAt == nil ||
		*first.RangeUsage.LastErrorAt != lastErrorAt {
		t.Fatalf("first row = %+v", first)
	}
	if page.Rows[1].Status != port.ManagementClientIPStatsStatusAllowlisted {
		t.Fatalf("second row status = %q", page.Rows[1].Status)
	}
}

func TestListManagementClientIPStatsClampsAdapterInputs(t *testing.T) {
	q := &managementClientIPStatsQueriesStub{ready: true}
	page, err := listManagementClientIPStats(
		context.Background(),
		q,
		port.ManagementClientIPStatsListInput{
			StartDate: "2026-07-01",
			EndDate:   "2026-07-07",
			Status:    "invalid",
			SortField: "invalid",
			SortOrder: "invalid",
			Limit:     500,
			Offset:    -3,
		},
	)
	if err != nil {
		t.Fatalf("listManagementClientIPStats() error = %v", err)
	}
	if !page.RangeReady || page.HasMore || len(page.Rows) != 0 {
		t.Fatalf("page = %+v", page)
	}
	call := q.listCalls[0]
	if call.StatusFilter != "all" ||
		call.SortField != "requestCount" ||
		call.SortOrder != "desc" ||
		call.RowLimit != 101 ||
		call.RowOffset != 0 {
		t.Fatalf("clamped list params = %+v", call)
	}
}

func TestListManagementClientIPStatsPreservesNonECMAScriptWhitespaceKeyword(t *testing.T) {
	const keyword = "\u0085"
	q := &managementClientIPStatsQueriesStub{ready: true}
	_, err := listManagementClientIPStats(
		context.Background(),
		q,
		port.ManagementClientIPStatsListInput{
			StartDate: "2026-07-01",
			EndDate:   "2026-07-07",
			Keyword:   keyword,
			Limit:     21,
		},
	)
	if err != nil {
		t.Fatalf("listManagementClientIPStats() error = %v", err)
	}
	if got := q.listCalls[0].Keyword; got != keyword {
		t.Fatalf("keyword = %q, want preserved %q", got, keyword)
	}
	if got := q.listCalls[0].KeywordUpper; got != "\u0086" {
		t.Fatalf("keyword upper = %q, want U+0086", got)
	}
}

func TestListManagementClientIPStatsRejectsPartialLastUsedRange(t *testing.T) {
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	q := &managementClientIPStatsQueriesStub{ready: true}
	_, err := listManagementClientIPStats(
		context.Background(),
		q,
		port.ManagementClientIPStatsListInput{
			StartDate:       "2026-07-01",
			EndDate:         "2026-07-07",
			LastUsedStartAt: &start,
			Limit:           21,
		},
	)
	if err == nil || !strings.Contains(err.Error(), "requires both boundaries") {
		t.Fatalf("partial last-used range error = %v", err)
	}
	if len(q.listCalls) != 0 {
		t.Fatalf("list calls = %d, want 0", len(q.listCalls))
	}
}

func TestManagementClientIPStatsAverageDropsNonFiniteValues(t *testing.T) {
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if got := managementClientIPStatsAverage(
			pgtype.Float8{Float64: value, Valid: true},
			1,
			1,
		); got != nil {
			t.Fatalf("average(%v) = %v, want nil", value, *got)
		}
	}
}

func TestListManagementClientIPStatsWrapsReadinessFailure(t *testing.T) {
	readinessErr := errors.New("readiness failed")
	q := &managementClientIPStatsQueriesStub{readyErr: readinessErr}
	_, err := listManagementClientIPStats(
		context.Background(),
		q,
		port.ManagementClientIPStatsListInput{},
	)
	if !errors.Is(err, readinessErr) ||
		!strings.Contains(err.Error(), "check management client IP stats range readiness") {
		t.Fatalf("readiness error = %v", err)
	}
}

func managementClientIPStatsNamedSQLSection(t *testing.T, sql string, name string) string {
	t.Helper()
	marker := "-- name: " + name
	start := strings.Index(sql, marker)
	if start < 0 {
		t.Fatalf("SQL query %q not found", name)
	}
	rest := sql[start+len(marker):]
	if next := strings.Index(rest, "-- name: "); next >= 0 {
		return sql[start : start+len(marker)+next]
	}
	return sql[start:]
}

type managementClientIPStatsQueriesStub struct {
	ready      bool
	readyErr   error
	rows       []postgresqueries.ListManagementClientIPStatsRow
	listErr    error
	readyCalls []postgresqueries.ManagementClientIPStatsRangeReadyParams
	listCalls  []postgresqueries.ListManagementClientIPStatsParams
}

func (s *managementClientIPStatsQueriesStub) ManagementClientIPStatsRangeReady(
	_ context.Context,
	arg postgresqueries.ManagementClientIPStatsRangeReadyParams,
) (bool, error) {
	s.readyCalls = append(s.readyCalls, arg)
	return s.ready, s.readyErr
}

func (s *managementClientIPStatsQueriesStub) ListManagementClientIPStats(
	_ context.Context,
	arg postgresqueries.ListManagementClientIPStatsParams,
) ([]postgresqueries.ListManagementClientIPStatsRow, error) {
	s.listCalls = append(s.listCalls, arg)
	return s.rows, s.listErr
}

var _ managementClientIPStatsQueries = (*managementClientIPStatsQueriesStub)(nil)

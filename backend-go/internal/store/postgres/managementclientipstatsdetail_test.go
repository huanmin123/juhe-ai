package postgres

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

func TestGetManagementClientIPStatsDetailStopsWhenRegistryIsMissing(t *testing.T) {
	queries := &managementClientIPStatsDetailQueriesStub{registryErr: pgx.ErrNoRows}

	result, found, err := findManagementClientIPStatsRegistry(
		context.Background(),
		queries,
		strings.Repeat("a", 64),
	)
	if err != nil {
		t.Fatalf("find registry: %v", err)
	}
	if found || result != (port.ManagementClientIPStatsRegistry{}) || len(queries.registryCalls) != 1 || len(queries.readyCalls) != 0 || len(queries.listCalls) != 0 || len(queries.requestCountDescCalls) != 0 {
		t.Fatalf("missing registry result = %+v, queries = %+v", result, queries)
	}
}

func TestFindManagementClientIPStatsRegistryMapsMetadata(t *testing.T) {
	queries := &managementClientIPStatsDetailQueriesStub{
		registry: postgresqueries.GetManagementClientIPStatsRegistryRow{
			IpHash:         strings.Repeat("b", 64),
			AggregateIpKey: "198.18.20",
			LastSeenAt:     "2026-07-14T03:04:05.000Z",
		},
	}

	result, found, err := findManagementClientIPStatsRegistry(
		context.Background(),
		queries,
		strings.Repeat("b", 64),
	)
	if err != nil {
		t.Fatalf("find registry: %v", err)
	}
	if !found || result.IPHash != strings.Repeat("b", 64) || result.AggregateIPKey != "198.18.20" || result.LastSeenAt != "2026-07-14T03:04:05.000Z" {
		t.Fatalf("registry result = %+v, found = %v", result, found)
	}
}

func TestListManagementClientIPStatsDetailShortCircuitsWhenRangeIsNotReady(t *testing.T) {
	queries := &managementClientIPStatsDetailQueriesStub{}

	result, err := listManagementClientIPStatsDetail(context.Background(), queries, port.ManagementClientIPStatsDetailInput{
		IPHash:    strings.Repeat("B", 64),
		StartDate: "2026-07-13",
		EndDate:   "2026-07-14",
		Limit:     21,
	})
	if err != nil {
		t.Fatalf("get detail: %v", err)
	}
	if result.RangeReady {
		t.Fatalf("not-ready result = %+v", result)
	}
	if result.Rows == nil || len(result.Rows) != 0 || len(queries.registryCalls) != 0 || len(queries.readyCalls) != 1 || len(queries.listCalls) != 0 || len(queries.requestCountDescCalls) != 0 {
		t.Fatalf("not-ready rows/calls = %+v / %+v", result.Rows, queries)
	}
}

func TestGetManagementClientIPStatsDetailUsesStaticDefaultQueryAndMapsProbeRows(t *testing.T) {
	queries := &managementClientIPStatsDetailQueriesStub{
		ready: true,
		requestCountDescRows: []postgresqueries.ListManagementClientIPAccountUsageRequestCountDescRow{
			managementClientIPAccountUsageRequestCountDescFixture("account_1", 9),
			managementClientIPAccountUsageRequestCountDescFixture("account_2", 8),
			managementClientIPAccountUsageRequestCountDescFixture("account_3", 7),
		},
	}

	result, err := listManagementClientIPStatsDetail(context.Background(), queries, port.ManagementClientIPStatsDetailInput{
		IPHash:    strings.Repeat("c", 64),
		StartDate: "2026-07-01",
		EndDate:   "2026-07-14",
		Limit:     3,
		Offset:    4,
	})
	if err != nil {
		t.Fatalf("get detail: %v", err)
	}
	if !result.RangeReady || !result.HasMore || len(result.Rows) != 2 {
		t.Fatalf("detail result = %+v", result)
	}
	if len(queries.listCalls) != 0 || len(queries.requestCountDescCalls) != 1 {
		t.Fatalf("default query calls = generic %d static %d", len(queries.listCalls), len(queries.requestCountDescCalls))
	}
	call := queries.requestCountDescCalls[0]
	if call.IpHash != strings.Repeat("c", 64) || call.StartDate != "2026-07-01" || call.EndDate != "2026-07-14" || call.RowLimit != 3 || call.RowOffset != 4 {
		t.Fatalf("static query input = %+v", call)
	}
	row := result.Rows[0]
	if row.AccountID != "account_1" || row.AccountName == nil || *row.AccountName != "账号一" || row.AccountOwnerSystemAccountID == nil || *row.AccountOwnerSystemAccountID != "sys_owner" || row.AccountOwnerSystemAccountName == nil || *row.AccountOwnerSystemAccountName != "所有者" {
		t.Fatalf("mapped account row = %+v", row)
	}
	if row.RangeUsage.RequestCount != 9 || row.RangeUsage.ErrorRate != float64(2)/9 || row.RangeUsage.TotalTokens != 15 || row.RangeUsage.AverageDurationMs == nil || *row.RangeUsage.AverageDurationMs != 5 || row.RangeUsage.AverageFirstTokenMs == nil || *row.RangeUsage.AverageFirstTokenMs != 4 || row.RangeUsage.MaxDurationMs == nil || *row.RangeUsage.MaxDurationMs != 8 {
		t.Fatalf("mapped usage = %+v", row.RangeUsage)
	}
}

func TestGetManagementClientIPStatsDetailUsesGenericAscendingSortAndClampsAdapterInputs(t *testing.T) {
	queries := &managementClientIPStatsDetailQueriesStub{
		ready: true,
		rows:  []postgresqueries.ListManagementClientIPAccountUsageRow{{AccountID: "deleted_account"}},
	}

	result, err := listManagementClientIPStatsDetail(context.Background(), queries, port.ManagementClientIPStatsDetailInput{
		IPHash:    strings.Repeat("d", 64),
		StartDate: "2026-07-14",
		EndDate:   "2026-07-14",
		SortField: port.ManagementClientIPStatsSortLastUsedAt,
		SortOrder: port.ManagementClientIPStatsSortAscending,
		Limit:     500,
		Offset:    5000,
	})
	if err != nil {
		t.Fatalf("get detail: %v", err)
	}
	if len(queries.requestCountDescCalls) != 0 || len(queries.listCalls) != 1 {
		t.Fatalf("ascending query calls = generic %d static %d", len(queries.listCalls), len(queries.requestCountDescCalls))
	}
	call := queries.listCalls[0]
	if call.SortField != "lastUsedAt" || call.SortOrder != "asc" || call.RowLimit != 101 || call.RowOffset != 999 {
		t.Fatalf("generic query input = %+v", call)
	}
	if len(result.Rows) != 1 || result.Rows[0].AccountName != nil || result.Rows[0].AccountOwnerSystemAccountID != nil || result.Rows[0].AccountOwnerSystemAccountName != nil {
		t.Fatalf("missing account metadata must remain omitted: %+v", result.Rows)
	}
}

func TestManagementClientIPStatsDetailSQLStaysOnPreaggregatedWindow(t *testing.T) {
	source, err := os.ReadFile("queries/w6_management_client_ip_stats_detail.sql")
	if err != nil {
		t.Fatalf("read detail SQL: %v", err)
	}
	sql := string(source)
	for _, required := range []string{
		"FROM juhe_stats.client_ip_account_usage_range_windows AS range_stats",
		"ORDER BY range_stats.request_count DESC, range_stats.account_id ASC",
		"CASE WHEN sqlc.arg(sort_order)::text = 'asc' THEN range_stats.account_id END DESC",
		"LIMIT sqlc.arg(row_limit)::int",
		"OFFSET sqlc.arg(row_offset)::int",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("detail SQL missing %q", required)
		}
	}
	for _, forbidden := range []string{"usage_records", "client_ip_account_stats_daily", "SUM(", "GROUP BY"} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("detail request SQL must not contain %q", forbidden)
		}
	}
}

func managementClientIPAccountUsageRequestCountDescFixture(
	accountID string,
	requestCount int64,
) postgresqueries.ListManagementClientIPAccountUsageRequestCountDescRow {
	return postgresqueries.ListManagementClientIPAccountUsageRequestCountDescRow{
		AccountID:                     accountID,
		AccountName:                   pgtype.Text{String: "账号一", Valid: true},
		AccountOwnerSystemAccountID:   pgtype.Text{String: "sys_owner", Valid: true},
		AccountOwnerSystemAccountName: pgtype.Text{String: "所有者", Valid: true},
		RequestCount:                  requestCount,
		SuccessCount:                  requestCount - 2,
		ErrorCount:                    2,
		InputTokens:                   10,
		OutputTokens:                  5,
		DurationMsSum:                 10,
		DurationMsCount:               2,
		DurationMsMax:                 8,
		FirstTokenMsSum:               8,
		FirstTokenMsCount:             2,
		ActiveDays:                    1,
	}
}

type managementClientIPStatsDetailQueriesStub struct {
	registry              postgresqueries.GetManagementClientIPStatsRegistryRow
	registryErr           error
	ready                 bool
	readyErr              error
	rows                  []postgresqueries.ListManagementClientIPAccountUsageRow
	listErr               error
	requestCountDescRows  []postgresqueries.ListManagementClientIPAccountUsageRequestCountDescRow
	requestCountDescErr   error
	registryCalls         []string
	readyCalls            []postgresqueries.ManagementClientIPStatsRangeReadyParams
	listCalls             []postgresqueries.ListManagementClientIPAccountUsageParams
	requestCountDescCalls []postgresqueries.ListManagementClientIPAccountUsageRequestCountDescParams
}

func (s *managementClientIPStatsDetailQueriesStub) GetManagementClientIPStatsRegistry(
	_ context.Context,
	ipHash string,
) (postgresqueries.GetManagementClientIPStatsRegistryRow, error) {
	s.registryCalls = append(s.registryCalls, ipHash)
	return s.registry, s.registryErr
}

func (s *managementClientIPStatsDetailQueriesStub) ManagementClientIPStatsRangeReady(
	_ context.Context,
	arg postgresqueries.ManagementClientIPStatsRangeReadyParams,
) (bool, error) {
	s.readyCalls = append(s.readyCalls, arg)
	return s.ready, s.readyErr
}

func (s *managementClientIPStatsDetailQueriesStub) ListManagementClientIPAccountUsage(
	_ context.Context,
	arg postgresqueries.ListManagementClientIPAccountUsageParams,
) ([]postgresqueries.ListManagementClientIPAccountUsageRow, error) {
	s.listCalls = append(s.listCalls, arg)
	return s.rows, s.listErr
}

func (s *managementClientIPStatsDetailQueriesStub) ListManagementClientIPAccountUsageRequestCountDesc(
	_ context.Context,
	arg postgresqueries.ListManagementClientIPAccountUsageRequestCountDescParams,
) ([]postgresqueries.ListManagementClientIPAccountUsageRequestCountDescRow, error) {
	s.requestCountDescCalls = append(s.requestCountDescCalls, arg)
	return s.requestCountDescRows, s.requestCountDescErr
}

var _ managementClientIPStatsDetailQueries = (*managementClientIPStatsDetailQueriesStub)(nil)
var _ managementClientIPStatsRegistryQueries = (*managementClientIPStatsDetailQueriesStub)(nil)

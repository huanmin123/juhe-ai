package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

func TestListManagementExternalIntegrationSourcesUsesBoundedLiteralPrefixParams(t *testing.T) {
	createdAt := time.Date(2026, 7, 15, 8, 1, 2, 345678000, time.FixedZone("UTC+8", 8*60*60))
	updatedAt := createdAt.Add(time.Hour)
	expiresAt := updatedAt.Add(24 * time.Hour)
	lastUsedAt := updatedAt.Add(-time.Minute)
	q := &managementExternalIntegrationSourceListQueriesStub{
		sourceRows: []postgresqueries.JuheBusinessExternalIntegrationSource{{
			ID:             "source_1",
			Name:           "Name",
			Status:         "active",
			ScopesJson:     `["juhe_ai_public:group_list:read"]`,
			RateLimitsJson: `[{"windowSeconds":60,"maxRequests":10}]`,
			ExpiresAt:      pgtype.Timestamptz{Time: expiresAt, Valid: true},
			Notes:          pgtype.Text{String: "notes", Valid: true},
			LastUsedAt:     pgtype.Timestamptz{Time: lastUsedAt, Valid: true},
			CreatedAt:      pgtype.Timestamptz{Time: createdAt, Valid: true},
			UpdatedAt:      pgtype.Timestamptz{Time: updatedAt, Valid: true},
		}},
	}
	tests := []struct {
		status      string
		keyword     string
		wantKeyword string
		wantUpper   string
	}{
		{status: "all", keyword: "  NaMe%_  ", wantKeyword: "name%_", wantUpper: "name%`"},
		{status: "active"},
		{status: "disabled"},
	}
	var mappedRow port.ManagementExternalIntegrationSourceListRow
	for _, test := range tests {
		rows, err := listManagementExternalIntegrationSources(context.Background(), q, port.ManagementExternalIntegrationSourceListInput{
			Status:  " " + test.status + " ",
			Keyword: test.keyword,
			Limit:   500,
			Offset:  -3,
		})
		if err != nil {
			t.Fatalf("listManagementExternalIntegrationSources(%s) error = %v", test.status, err)
		}
		if len(rows) != 1 {
			t.Fatalf("listManagementExternalIntegrationSources(%s) rows = %#v", test.status, rows)
		}
		mappedRow = rows[0]
	}
	if len(q.sourceCalls) != len(tests) {
		t.Fatalf("source query calls = %d, want %d", len(q.sourceCalls), len(tests))
	}
	for i, test := range tests {
		call := q.sourceCalls[i]
		if call.Status != test.status || call.Keyword != test.wantKeyword || call.KeywordUpper != test.wantUpper {
			t.Fatalf("source query call %d = %#v", i, call)
		}
		if call.RowLimit != maxManagementExternalIntegrationSourceListRows || call.RowOffset != 0 {
			t.Fatalf("source query bounds = %#v", call)
		}
	}
	row := mappedRow
	if row.ID != "source_1" || row.Notes == nil || *row.Notes != "notes" {
		t.Fatalf("mapped source row = %#v", row)
	}
	if !row.CreatedAt.Equal(createdAt.UTC()) || !row.UpdatedAt.Equal(updatedAt.UTC()) {
		t.Fatalf("mapped required times = %v / %v", row.CreatedAt, row.UpdatedAt)
	}
	if row.ExpiresAt == nil || !row.ExpiresAt.Equal(expiresAt.UTC()) || row.LastUsedAt == nil || !row.LastUsedAt.Equal(lastUsedAt.UTC()) {
		t.Fatalf("mapped optional times = %#v / %#v", row.ExpiresAt, row.LastUsedAt)
	}
}

func TestManagementExternalIntegrationSourceTokenReadersUseOneBoundedQueryEach(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 999999999, time.FixedZone("UTC+8", 8*60*60))
	expiredAt := now.Add(-time.Hour)
	q := &managementExternalIntegrationSourceListQueriesStub{
		statsRows: []postgresqueries.ListManagementExternalIntegrationSourceTokenStatsRow{{
			SourceRefID:      "source_1",
			TokenCount:       3,
			ActiveTokenCount: 2,
		}},
		primaryRows: []postgresqueries.ListManagementExternalIntegrationSourcePrimaryTokensRow{{
			SourceRefID: "source_1",
			ID:          "token_active_expired",
			Name:        "Expired Active",
			TokenPrefix: "juis_pre",
			TokenSuffix: "suffix01",
			Status:      "active",
			ScopesJson:  `[]`,
			ExpiresAt:   pgtype.Timestamptz{Time: expiredAt, Valid: true},
			CreatedAt:   pgtype.Timestamptz{Time: now, Valid: true},
			UpdatedAt:   pgtype.Timestamptz{Time: now, Valid: true},
		}},
	}
	ids := make([]string, 0, 104)
	ids = append(ids, "", " source_1 ", "source_1")
	for i := 2; i <= 102; i++ {
		ids = append(ids, fmt.Sprintf("source_%03d", i))
	}

	stats, err := listManagementExternalIntegrationSourceTokenStats(context.Background(), q, ids)
	if err != nil {
		t.Fatalf("list token stats: %v", err)
	}
	primary, err := listManagementExternalIntegrationSourcePrimaryTokens(context.Background(), q, ids)
	if err != nil {
		t.Fatalf("list primary tokens: %v", err)
	}
	if len(q.statsCalls) != 1 || len(q.primaryCalls) != 1 {
		t.Fatalf("token query calls = stats:%d primary:%d", len(q.statsCalls), len(q.primaryCalls))
	}
	if len(q.statsCalls[0]) != maxManagementExternalIntegrationSourceIDs || !reflect.DeepEqual(q.statsCalls[0], q.primaryCalls[0]) {
		t.Fatalf("bounded token IDs = stats:%#v primary:%#v", q.statsCalls[0], q.primaryCalls[0])
	}
	if len(stats) != 1 || stats[0].TokenCount != 3 || stats[0].ActiveTokenCount != 2 {
		t.Fatalf("token stats = %#v", stats)
	}
	if len(primary) != 1 || primary[0].ID != "token_active_expired" || primary[0].Status != "active" {
		t.Fatalf("primary tokens = %#v", primary)
	}
	if primary[0].ExpiresAt == nil || !primary[0].ExpiresAt.Equal(expiredAt.UTC()) {
		t.Fatalf("expired active primary expiry = %#v", primary[0].ExpiresAt)
	}

	if _, err := listManagementExternalIntegrationSourceTokenStats(context.Background(), q, []string{"", "  "}); err != nil {
		t.Fatalf("empty token stats: %v", err)
	}
	if _, err := listManagementExternalIntegrationSourcePrimaryTokens(context.Background(), q, nil); err != nil {
		t.Fatalf("empty primary tokens: %v", err)
	}
	if len(q.statsCalls) != 1 || len(q.primaryCalls) != 1 {
		t.Fatal("empty source ID batches must not execute token queries")
	}
}

func TestManagementExternalIntegrationSourceStorePropagatesQueryAndInvalidTimeErrors(t *testing.T) {
	wantErr := errors.New("postgres unavailable")
	q := &managementExternalIntegrationSourceListQueriesStub{sourceErr: wantErr}
	_, err := listManagementExternalIntegrationSources(context.Background(), q, port.ManagementExternalIntegrationSourceListInput{
		Status: "all",
		Limit:  21,
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("source query error = %v, want wrapped postgres error", err)
	}

	q = &managementExternalIntegrationSourceListQueriesStub{sourceRows: []postgresqueries.JuheBusinessExternalIntegrationSource{{
		ID:        "source_invalid_time",
		CreatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}}}
	_, err = listManagementExternalIntegrationSources(context.Background(), q, port.ManagementExternalIntegrationSourceListInput{
		Status: "all",
		Limit:  21,
	})
	if err == nil || !strings.Contains(err.Error(), "updated_at") {
		t.Fatalf("invalid required time error = %v", err)
	}
}

func TestManagementExternalIntegrationSourceListSQLIsSafeAndKeepsExpiredActiveSemantics(t *testing.T) {
	source, err := os.ReadFile("queries/w2_management_external_integration_source_list.sql")
	if err != nil {
		t.Fatalf("read management external integration source list SQL: %v", err)
	}
	sql := strings.ReplaceAll(string(source), "\r\n", "\n")
	sourceQuery := managementExternalIntegrationSourceSQLSection(
		t,
		sql,
		"-- name: ListManagementExternalIntegrationSources :many",
		"-- name: ListManagementExternalIntegrationSourceTokenStats :many",
	)
	for _, required := range []string{
		"sources.id,",
		"sources.updated_at",
		"lower(sources.name) COLLATE \"C\" >= sqlc.arg(keyword)::text",
		"lower(sources.name) COLLATE \"C\" < sqlc.arg(keyword_upper)::text",
		"starts_with(lower(sources.name), sqlc.arg(keyword)::text)",
		"ORDER BY sources.updated_at DESC, sources.id DESC",
		"LIMIT sqlc.arg(row_limit)::int",
		"OFFSET sqlc.arg(row_offset)::int",
	} {
		if !strings.Contains(sourceQuery, required) {
			t.Fatalf("source list query missing %q", required)
		}
	}
	for _, forbidden := range []string{"SELECT *", " JOIN ", "external_integration_source_tokens"} {
		if strings.Contains(sourceQuery, forbidden) {
			t.Fatalf("source list query must not contain %q", forbidden)
		}
	}

	statsQuery := managementExternalIntegrationSourceSQLSection(
		t,
		sql,
		"-- name: ListManagementExternalIntegrationSourceTokenStats :many",
		"-- name: ListManagementExternalIntegrationSourcePrimaryTokens :many",
	)
	if !strings.Contains(statsQuery, "COUNT(*) AS token_count") ||
		!strings.Contains(statsQuery, "COUNT(*) FILTER (WHERE tokens.status = 'active') AS active_token_count") ||
		strings.Contains(strings.ToLower(statsQuery), "expires_at") {
		t.Fatalf("token stats query changed all-status/active-status semantics:\n%s", statsQuery)
	}

	primaryQuery := managementExternalIntegrationSourceSQLSection(
		t,
		sql,
		"-- name: ListManagementExternalIntegrationSourcePrimaryTokens :many",
		"",
	)
	primaryOrder := strings.Join(strings.Fields(`
      CASE WHEN tokens.status = 'active' THEN 0 ELSE 1 END ASC,
      tokens.created_at DESC,
      tokens.id DESC
    `), " ")
	if !strings.Contains(strings.Join(strings.Fields(primaryQuery), " "), primaryOrder) {
		t.Fatalf("primary token ordering must prefer active status before newest token:\n%s", primaryQuery)
	}
	for _, forbidden := range []string{
		"expires_at <",
		"expires_at >",
		"expires_at IS NULL",
		"now()",
		"SELECT *",
	} {
		if strings.Contains(primaryQuery, forbidden) {
			t.Fatalf("primary token query must not contain %q", forbidden)
		}
	}

	lowerSQL := strings.ToLower(statsQuery + primaryQuery)
	for _, forbidden := range []string{"token_hash", "token_secret_encrypted"} {
		if strings.Contains(lowerSQL, forbidden) {
			t.Fatalf("token summary queries must not select sensitive column %q", forbidden)
		}
	}
}

func managementExternalIntegrationSourceSQLSection(t *testing.T, source string, start string, end string) string {
	t.Helper()
	startIndex := strings.Index(source, start)
	if startIndex < 0 {
		t.Fatalf("SQL source missing marker %q", start)
	}
	section := source[startIndex+len(start):]
	if end == "" {
		return section
	}
	endIndex := strings.Index(section, end)
	if endIndex < 0 {
		t.Fatalf("SQL source missing marker %q", end)
	}
	return section[:endIndex]
}

type managementExternalIntegrationSourceListQueriesStub struct {
	sourceRows   []postgresqueries.JuheBusinessExternalIntegrationSource
	sourceErr    error
	sourceCalls  []postgresqueries.ListManagementExternalIntegrationSourcesParams
	statsRows    []postgresqueries.ListManagementExternalIntegrationSourceTokenStatsRow
	statsErr     error
	statsCalls   [][]string
	primaryRows  []postgresqueries.ListManagementExternalIntegrationSourcePrimaryTokensRow
	primaryErr   error
	primaryCalls [][]string
}

func (s *managementExternalIntegrationSourceListQueriesStub) ListManagementExternalIntegrationSources(
	_ context.Context,
	arg postgresqueries.ListManagementExternalIntegrationSourcesParams,
) ([]postgresqueries.JuheBusinessExternalIntegrationSource, error) {
	s.sourceCalls = append(s.sourceCalls, arg)
	return s.sourceRows, s.sourceErr
}

func (s *managementExternalIntegrationSourceListQueriesStub) ListManagementExternalIntegrationSourceTokenStats(
	_ context.Context,
	sourceIDs []string,
) ([]postgresqueries.ListManagementExternalIntegrationSourceTokenStatsRow, error) {
	s.statsCalls = append(s.statsCalls, append([]string(nil), sourceIDs...))
	return s.statsRows, s.statsErr
}

func (s *managementExternalIntegrationSourceListQueriesStub) ListManagementExternalIntegrationSourcePrimaryTokens(
	_ context.Context,
	sourceIDs []string,
) ([]postgresqueries.ListManagementExternalIntegrationSourcePrimaryTokensRow, error) {
	s.primaryCalls = append(s.primaryCalls, append([]string(nil), sourceIDs...))
	return s.primaryRows, s.primaryErr
}

var _ managementExternalIntegrationSourceListQueries = (*managementExternalIntegrationSourceListQueriesStub)(nil)

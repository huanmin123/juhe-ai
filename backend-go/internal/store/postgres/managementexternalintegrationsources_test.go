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
	updatedAt := time.Date(2026, 7, 15, 9, 1, 2, 345678000, time.FixedZone("UTC+8", 8*60*60))
	expiresAt := updatedAt.Add(24 * time.Hour)
	lastUsedAt := updatedAt.Add(-time.Minute)
	q := &managementExternalIntegrationSourceListQueriesStub{
		sourceRows: []postgresqueries.ListManagementExternalIntegrationSourcesRow{{
			ID:             "source_1",
			Name:           "Name",
			Status:         "active",
			ScopesJson:     `["juhe_ai_public:group_list:read"]`,
			RateLimitsJson: `[{"windowSeconds":60,"maxRequests":10}]`,
			ExpiresAt:      pgtype.Timestamptz{Time: expiresAt, Valid: true},
			Notes:          pgtype.Text{String: "notes", Valid: true},
			LastUsedAt:     pgtype.Timestamptz{Time: lastUsedAt, Valid: true},
		}},
	}
	tests := []struct {
		status      string
		keyword     string
		wantKeyword string
		wantUpper   string
	}{
		{status: "all", keyword: "  NaMe%_  ", wantKeyword: "name%_", wantUpper: "name%`"},
		{status: "all", keyword: "\u0085NaMe\u0085", wantKeyword: "\u0085name\u0085", wantUpper: "\u0085name\u0086"},
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
	if row.ExpiresAt == nil || !row.ExpiresAt.Equal(expiresAt.UTC()) || row.LastUsedAt == nil || !row.LastUsedAt.Equal(lastUsedAt.UTC()) {
		t.Fatalf("mapped optional times = %#v / %#v", row.ExpiresAt, row.LastUsedAt)
	}
}

func TestManagementExternalIntegrationSourceIDsPreserveNonECMAScriptWhitespace(t *testing.T) {
	got := managementExternalIntegrationSourceIDs([]string{
		"\u0085source_1\u0085",
		" \uFEFFsource_2\u3000 ",
	})
	want := []string{"\u0085source_1\u0085", "source_2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("management external integration source IDs = %#v, want %#v", got, want)
	}
}

func TestManagementExternalIntegrationSourcePrimaryTokenReaderUsesOneBoundedQuery(t *testing.T) {
	q := &managementExternalIntegrationSourceListQueriesStub{
		primaryRows: []postgresqueries.ListManagementExternalIntegrationSourcePrimaryTokensRow{{
			SourceRefID: "source_1",
			ID:          "token_active_expired",
			TokenPrefix: "juis_pre",
			TokenSuffix: "suffix01",
		}},
	}
	ids := make([]string, 0, 104)
	ids = append(ids, "", " source_1 ", "source_1")
	for i := 2; i <= 102; i++ {
		ids = append(ids, fmt.Sprintf("source_%03d", i))
	}

	primary, err := listManagementExternalIntegrationSourcePrimaryTokens(context.Background(), q, ids)
	if err != nil {
		t.Fatalf("list primary tokens: %v", err)
	}
	if len(q.primaryCalls) != 1 {
		t.Fatalf("primary token query calls = %d", len(q.primaryCalls))
	}
	if len(q.primaryCalls[0]) != maxManagementExternalIntegrationSourceIDs {
		t.Fatalf("bounded token IDs = %#v", q.primaryCalls[0])
	}
	if len(primary) != 1 || primary[0].ID != "token_active_expired" || primary[0].TokenPrefix != "juis_pre" {
		t.Fatalf("primary tokens = %#v", primary)
	}
	if _, err := listManagementExternalIntegrationSourcePrimaryTokens(context.Background(), q, nil); err != nil {
		t.Fatalf("empty primary tokens: %v", err)
	}
	if len(q.primaryCalls) != 1 {
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
		"-- name: ListManagementExternalIntegrationSourcePrimaryTokens :many",
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

	primaryQuery := managementExternalIntegrationSourceSQLSection(
		t,
		sql,
		"-- name: ListManagementExternalIntegrationSourcePrimaryTokens :many",
		"-- name: FindManagementExternalIntegrationSource :one",
	)
	primaryOrder := strings.Join(strings.Fields(`
      CASE WHEN tokens.status = 'active' THEN 0 ELSE 1 END ASC,
      tokens.created_at DESC,
      tokens.id DESC
    `), " ")
	if !strings.Contains(strings.Join(strings.Fields(primaryQuery), " "), primaryOrder) {
		t.Fatalf("primary token ordering must prefer active status before newest token:\n%s", primaryQuery)
	}
	for _, forbidden := range []string{"tokens.name", "tokens.scopes_json", "tokens.expires_at", "tokens.last_used_at", "tokens.revoked_at"} {
		if strings.Contains(primaryQuery, forbidden) {
			t.Fatalf("primary token list must not project %s:\n%s", forbidden, primaryQuery)
		}
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

	lowerSQL := strings.ToLower(primaryQuery)
	for _, forbidden := range []string{"token_hash", "token_secret_encrypted"} {
		if strings.Contains(lowerSQL, forbidden) {
			t.Fatalf("primary token query must not select sensitive column %q", forbidden)
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
	sourceRows   []postgresqueries.ListManagementExternalIntegrationSourcesRow
	sourceErr    error
	sourceCalls  []postgresqueries.ListManagementExternalIntegrationSourcesParams
	primaryRows  []postgresqueries.ListManagementExternalIntegrationSourcePrimaryTokensRow
	primaryErr   error
	primaryCalls [][]string
}

func (s *managementExternalIntegrationSourceListQueriesStub) ListManagementExternalIntegrationSources(
	_ context.Context,
	arg postgresqueries.ListManagementExternalIntegrationSourcesParams,
) ([]postgresqueries.ListManagementExternalIntegrationSourcesRow, error) {
	s.sourceCalls = append(s.sourceCalls, arg)
	return s.sourceRows, s.sourceErr
}

func (s *managementExternalIntegrationSourceListQueriesStub) ListManagementExternalIntegrationSourcePrimaryTokens(
	_ context.Context,
	sourceIDs []string,
) ([]postgresqueries.ListManagementExternalIntegrationSourcePrimaryTokensRow, error) {
	s.primaryCalls = append(s.primaryCalls, append([]string(nil), sourceIDs...))
	return s.primaryRows, s.primaryErr
}

var _ managementExternalIntegrationSourceListQueries = (*managementExternalIntegrationSourceListQueriesStub)(nil)

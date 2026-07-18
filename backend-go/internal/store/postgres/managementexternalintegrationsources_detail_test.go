package postgres

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

func TestFindManagementExternalIntegrationSourceMapsFieldsAndTimes(t *testing.T) {
	createdAt := time.Date(2026, 7, 15, 8, 1, 2, 345678000, time.FixedZone("UTC+8", 8*60*60))
	updatedAt := createdAt.Add(2 * time.Hour)
	expiresAt := updatedAt.Add(24 * time.Hour)
	lastUsedAt := updatedAt.Add(-time.Minute)
	q := &managementExternalIntegrationSourceDetailQueriesStub{
		sourceRow: postgresqueries.JuheBusinessExternalIntegrationSource{
			ID:             "source_1",
			Name:           "Source One",
			Status:         "active",
			ScopesJson:     `["juhe_ai_public:group_list:read"]`,
			RateLimitsJson: `[{"windowSeconds":60,"maxRequests":10}]`,
			ExpiresAt:      pgtype.Timestamptz{Time: expiresAt, Valid: true},
			Notes:          pgtype.Text{String: "detail notes", Valid: true},
			LastUsedAt:     pgtype.Timestamptz{Time: lastUsedAt, Valid: true},
			CreatedAt:      pgtype.Timestamptz{Time: createdAt, Valid: true},
			UpdatedAt:      pgtype.Timestamptz{Time: updatedAt, Valid: true},
		},
	}

	got, found, err := findManagementExternalIntegrationSource(context.Background(), q, "  source_1  ")
	if err != nil {
		t.Fatalf("find management external integration source: %v", err)
	}
	if !found {
		t.Fatal("find management external integration source found = false")
	}
	if len(q.sourceCalls) != 1 || q.sourceCalls[0] != "source_1" {
		t.Fatalf("source query calls = %#v", q.sourceCalls)
	}
	if got.ID != "source_1" ||
		got.Name != "Source One" ||
		got.Status != "active" ||
		got.ScopesJSON != `["juhe_ai_public:group_list:read"]` ||
		got.RateLimitsJSON != `[{"windowSeconds":60,"maxRequests":10}]` ||
		got.Notes == nil || *got.Notes != "detail notes" {
		t.Fatalf("mapped source detail = %#v", got)
	}
	assertExternalIntegrationSourceUTCTime(t, "created_at", got.CreatedAt, createdAt)
	assertExternalIntegrationSourceUTCTime(t, "updated_at", got.UpdatedAt, updatedAt)
	assertExternalIntegrationSourceUTCOptionalTime(t, "expires_at", got.ExpiresAt, expiresAt)
	assertExternalIntegrationSourceUTCOptionalTime(t, "last_used_at", got.LastUsedAt, lastUsedAt)
}

func TestFindManagementExternalIntegrationSourceReturnsNotFound(t *testing.T) {
	q := &managementExternalIntegrationSourceDetailQueriesStub{sourceErr: pgx.ErrNoRows}

	got, found, err := findManagementExternalIntegrationSource(context.Background(), q, "missing_source")
	if err != nil {
		t.Fatalf("find missing management external integration source: %v", err)
	}
	if found || got.ID != "" {
		t.Fatalf("missing source = %#v, found = %v", got, found)
	}
	if len(q.sourceCalls) != 1 || q.sourceCalls[0] != "missing_source" {
		t.Fatalf("source query calls = %#v", q.sourceCalls)
	}
}

func TestFindManagementExternalIntegrationSourceWrapsQueryError(t *testing.T) {
	wantErr := errors.New("detail query failed")
	q := &managementExternalIntegrationSourceDetailQueriesStub{sourceErr: wantErr}

	got, found, err := findManagementExternalIntegrationSource(context.Background(), q, "source_1")
	if found || got.ID != "" {
		t.Fatalf("source on query error = %#v, found = %v", got, found)
	}
	if !errors.Is(err, wantErr) || !strings.Contains(err.Error(), "find management external integration source") {
		t.Fatalf("find source error = %v", err)
	}
}

func TestManagementExternalIntegrationSourceDetailReadersSkipEmptyID(t *testing.T) {
	for _, sourceID := range []string{"", " \t\r\n "} {
		t.Run(strings.ReplaceAll(sourceID, "\t", `\t`), func(t *testing.T) {
			q := &managementExternalIntegrationSourceDetailQueriesStub{}

			detail, found, err := findManagementExternalIntegrationSource(context.Background(), q, sourceID)
			if err != nil || found || detail.ID != "" {
				t.Fatalf("empty ID detail = %#v, found = %v, err = %v", detail, found, err)
			}
			tokens, err := listManagementExternalIntegrationSourceTokens(context.Background(), q, sourceID)
			if err != nil {
				t.Fatalf("list tokens for empty ID: %v", err)
			}
			if tokens == nil || len(tokens) != 0 {
				t.Fatalf("tokens for empty ID = %#v, want non-nil empty slice", tokens)
			}
			if len(q.sourceCalls) != 0 || len(q.tokenCalls) != 0 {
				t.Fatalf("empty ID query calls = source:%#v tokens:%#v", q.sourceCalls, q.tokenCalls)
			}
		})
	}
}

func TestManagementExternalIntegrationSourceDetailReadersPreserveNonECMAScriptWhitespaceID(t *testing.T) {
	const sourceID = "\u0085source_1\u0085"
	q := &managementExternalIntegrationSourceDetailQueriesStub{sourceErr: pgx.ErrNoRows}

	_, found, err := findManagementExternalIntegrationSource(context.Background(), q, sourceID)
	if err != nil || found {
		t.Fatalf("find source with non ECMAScript whitespace found = %v, err = %v", found, err)
	}
	tokens, err := listManagementExternalIntegrationSourceTokens(context.Background(), q, sourceID)
	if err != nil {
		t.Fatalf("list source tokens with non ECMAScript whitespace: %v", err)
	}
	if tokens == nil || len(tokens) != 0 {
		t.Fatalf("source tokens = %#v, want non-nil empty slice", tokens)
	}
	if !reflect.DeepEqual(q.sourceCalls, []string{sourceID}) || !reflect.DeepEqual(q.tokenCalls, []string{sourceID}) {
		t.Fatalf("detail query calls = source:%#v tokens:%#v", q.sourceCalls, q.tokenCalls)
	}
}

func TestListManagementExternalIntegrationSourceTokensMapsFieldsTimesAndOrder(t *testing.T) {
	createdAt := time.Date(2026, 7, 15, 9, 10, 11, 123456789, time.FixedZone("UTC+8", 8*60*60))
	updatedAt := createdAt.Add(time.Minute)
	expiresAt := createdAt.Add(24 * time.Hour)
	lastUsedAt := createdAt.Add(2 * time.Hour)
	revokedAt := createdAt.Add(3 * time.Hour)
	q := &managementExternalIntegrationSourceDetailQueriesStub{
		tokenRows: []postgresqueries.ListManagementExternalIntegrationSourceTokensRow{
			{
				SourceRefID: "source_1",
				ID:          "token_newer",
				Name:        "Newer token",
				TokenPrefix: "juis_new",
				TokenSuffix: "suffix01",
				Status:      "revoked",
				ScopesJson:  `["juhe_ai_public:group_list:read"]`,
				ExpiresAt:   pgtype.Timestamptz{Time: expiresAt, Valid: true},
				LastUsedAt:  pgtype.Timestamptz{Time: lastUsedAt, Valid: true},
				CreatedAt:   pgtype.Timestamptz{Time: createdAt, Valid: true},
				UpdatedAt:   pgtype.Timestamptz{Time: updatedAt, Valid: true},
				RevokedAt:   pgtype.Timestamptz{Time: revokedAt, Valid: true},
			},
			{
				SourceRefID: "source_1",
				ID:          "token_older",
				Name:        "Older token",
				TokenPrefix: "juis_old",
				TokenSuffix: "suffix02",
				Status:      "active",
				ScopesJson:  `[]`,
				CreatedAt:   pgtype.Timestamptz{Time: createdAt.Add(-time.Hour), Valid: true},
				UpdatedAt:   pgtype.Timestamptz{Time: updatedAt.Add(-time.Hour), Valid: true},
			},
		},
	}

	got, err := listManagementExternalIntegrationSourceTokens(context.Background(), q, " source_1 ")
	if err != nil {
		t.Fatalf("list management external integration source tokens: %v", err)
	}
	if len(q.tokenCalls) != 1 || q.tokenCalls[0] != "source_1" {
		t.Fatalf("token query calls = %#v", q.tokenCalls)
	}
	if len(got) != 2 || got[0].ID != "token_newer" || got[1].ID != "token_older" {
		t.Fatalf("mapped token order = %#v", got)
	}
	newer := got[0]
	if newer.SourceRefID != "source_1" ||
		newer.Name != "Newer token" ||
		newer.TokenPrefix != "juis_new" ||
		newer.TokenSuffix != "suffix01" ||
		newer.Status != "revoked" ||
		newer.ScopesJSON != `["juhe_ai_public:group_list:read"]` {
		t.Fatalf("mapped token = %#v", newer)
	}
	assertExternalIntegrationSourceUTCTime(t, "token created_at", newer.CreatedAt, createdAt)
	assertExternalIntegrationSourceUTCTime(t, "token updated_at", newer.UpdatedAt, updatedAt)
	assertExternalIntegrationSourceUTCOptionalTime(t, "token expires_at", newer.ExpiresAt, expiresAt)
	assertExternalIntegrationSourceUTCOptionalTime(t, "token last_used_at", newer.LastUsedAt, lastUsedAt)
	assertExternalIntegrationSourceUTCOptionalTime(t, "token revoked_at", newer.RevokedAt, revokedAt)
	if got[1].ExpiresAt != nil || got[1].LastUsedAt != nil || got[1].RevokedAt != nil {
		t.Fatalf("nullable token times must remain nil: %#v", got[1])
	}
}

func TestListManagementExternalIntegrationSourceTokensWrapsQueryError(t *testing.T) {
	wantErr := errors.New("token query failed")
	q := &managementExternalIntegrationSourceDetailQueriesStub{tokenErr: wantErr}

	got, err := listManagementExternalIntegrationSourceTokens(context.Background(), q, " source_1 ")
	if got != nil {
		t.Fatalf("tokens on query error = %#v, want nil", got)
	}
	if !errors.Is(err, wantErr) || !strings.Contains(err.Error(), "list management external integration source tokens") {
		t.Fatalf("list source tokens error = %v", err)
	}
	if len(q.tokenCalls) != 1 || q.tokenCalls[0] != "source_1" {
		t.Fatalf("token query calls = %#v", q.tokenCalls)
	}
}

func TestListManagementExternalIntegrationSourceTokensRejectsInvalidRequiredTime(t *testing.T) {
	q := &managementExternalIntegrationSourceDetailQueriesStub{
		tokenRows: []postgresqueries.ListManagementExternalIntegrationSourceTokensRow{{
			ID:        "token_invalid_time",
			CreatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
		}},
	}

	_, err := listManagementExternalIntegrationSourceTokens(context.Background(), q, "source_1")
	if err == nil || !strings.Contains(err.Error(), `row "token_invalid_time" has invalid updated_at`) {
		t.Fatalf("invalid token required time error = %v", err)
	}
}

func TestManagementExternalIntegrationSourceDetailSQLUsesSafePointQueries(t *testing.T) {
	source, err := os.ReadFile("queries/w2_management_external_integration_source_list.sql")
	if err != nil {
		t.Fatalf("read management external integration source SQL: %v", err)
	}
	sql := strings.ReplaceAll(string(source), "\r\n", "\n")
	detailQuery := managementExternalIntegrationSourceSQLSection(
		t,
		sql,
		"-- name: FindManagementExternalIntegrationSource :one",
		"-- name: ListManagementExternalIntegrationSourceTokens :many",
	)
	for _, required := range []string{
		"sources.id,",
		"sources.name,",
		"sources.status,",
		"sources.scopes_json,",
		"sources.rate_limits_json,",
		"sources.expires_at,",
		"sources.notes,",
		"sources.last_used_at,",
		"sources.created_at,",
		"sources.updated_at",
		"FROM juhe_business.external_integration_sources AS sources",
		"WHERE sources.id = sqlc.arg(source_id)::text",
	} {
		if !strings.Contains(detailQuery, required) {
			t.Fatalf("source detail query missing %q:\n%s", required, detailQuery)
		}
	}
	for _, forbidden := range []string{"SELECT *", " JOIN ", "external_integration_source_tokens", "ORDER BY", "LIMIT "} {
		if strings.Contains(detailQuery, forbidden) {
			t.Fatalf("source detail point query must not contain %q:\n%s", forbidden, detailQuery)
		}
	}

	tokenQuery := managementExternalIntegrationSourceSQLSection(
		t,
		sql,
		"-- name: ListManagementExternalIntegrationSourceTokens :many",
		"-- name: FindManagementExternalIntegrationSourceTokenSecret :one",
	)
	for _, required := range []string{
		"tokens.source_ref_id,",
		"tokens.id,",
		"tokens.name,",
		"tokens.token_prefix,",
		"tokens.token_suffix,",
		"tokens.status,",
		"tokens.scopes_json,",
		"tokens.expires_at,",
		"tokens.last_used_at,",
		"tokens.created_at,",
		"tokens.updated_at,",
		"tokens.revoked_at",
		"FROM juhe_business.external_integration_source_tokens AS tokens",
		"WHERE tokens.source_ref_id = sqlc.arg(source_id)::text",
		"ORDER BY tokens.created_at DESC, tokens.id DESC",
	} {
		if !strings.Contains(tokenQuery, required) {
			t.Fatalf("source token detail query missing %q:\n%s", required, tokenQuery)
		}
	}
	for _, forbidden := range []string{"select *", "token_hash", "token_secret_encrypted", " join "} {
		if strings.Contains(strings.ToLower(tokenQuery), forbidden) {
			t.Fatalf("source token detail query must not contain %q:\n%s", forbidden, tokenQuery)
		}
	}
}

func TestManagementExternalIntegrationSourceDetailMigrationAddsTokenOrderIndex(t *testing.T) {
	source, err := os.ReadFile("../../../db/migrations/000050_w2_management_external_integration_source_detail.sql")
	if err != nil {
		t.Fatalf("read management external integration source detail migration: %v", err)
	}
	sql := strings.Join(strings.Fields(string(source)), " ")
	for _, required := range []string{
		"CREATE INDEX IF NOT EXISTS idx_external_integration_source_tokens_source_created",
		"ON juhe_business.external_integration_source_tokens (source_ref_id, created_at DESC, id DESC)",
		"DROP INDEX IF EXISTS juhe_business.idx_external_integration_source_tokens_source_created",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("source detail migration missing %q: %s", required, sql)
		}
	}
}

func assertExternalIntegrationSourceUTCTime(t *testing.T, field string, got time.Time, want time.Time) {
	t.Helper()
	if !got.Equal(want.UTC()) || got.Location() != time.UTC {
		t.Fatalf("%s = %v (%v), want %v in UTC", field, got, got.Location(), want.UTC())
	}
}

func assertExternalIntegrationSourceUTCOptionalTime(t *testing.T, field string, got *time.Time, want time.Time) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s = nil, want %v", field, want.UTC())
	}
	assertExternalIntegrationSourceUTCTime(t, field, *got, want)
}

type managementExternalIntegrationSourceDetailQueriesStub struct {
	sourceRow   postgresqueries.JuheBusinessExternalIntegrationSource
	sourceErr   error
	sourceCalls []string
	tokenRows   []postgresqueries.ListManagementExternalIntegrationSourceTokensRow
	tokenErr    error
	tokenCalls  []string
}

func (s *managementExternalIntegrationSourceDetailQueriesStub) FindManagementExternalIntegrationSource(
	_ context.Context,
	sourceID string,
) (postgresqueries.JuheBusinessExternalIntegrationSource, error) {
	s.sourceCalls = append(s.sourceCalls, sourceID)
	return s.sourceRow, s.sourceErr
}

func (s *managementExternalIntegrationSourceDetailQueriesStub) ListManagementExternalIntegrationSourceTokens(
	_ context.Context,
	sourceID string,
) ([]postgresqueries.ListManagementExternalIntegrationSourceTokensRow, error) {
	s.tokenCalls = append(s.tokenCalls, sourceID)
	return s.tokenRows, s.tokenErr
}

var _ managementExternalIntegrationSourceDetailQueries = (*managementExternalIntegrationSourceDetailQueriesStub)(nil)

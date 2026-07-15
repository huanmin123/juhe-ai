package postgres

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

func TestFindManagementExternalIntegrationSourceTokenSecretReturnsExactCiphertext(t *testing.T) {
	q := &managementExternalIntegrationSourceTokenSecretQueriesStub{
		ciphertext: "v1:nonce:tag:ciphertext",
	}

	got, found, err := findManagementExternalIntegrationSourceTokenSecret(
		context.Background(),
		q,
		" \t source_1 \r\n",
		"\uFEFFtoken_1\u00A0",
	)
	if err != nil {
		t.Fatalf("find token secret: %v", err)
	}
	if !found || got != q.ciphertext {
		t.Fatalf("token secret = %q, found = %v", got, found)
	}
	want := []postgresqueries.FindManagementExternalIntegrationSourceTokenSecretParams{{
		SourceID: "source_1",
		TokenID:  "token_1",
	}}
	if !reflect.DeepEqual(q.calls, want) {
		t.Fatalf("query calls = %#v, want %#v", q.calls, want)
	}
}

func TestFindManagementExternalIntegrationSourceTokenSecretPreservesNonECMAScriptWhitespace(t *testing.T) {
	const sourceID = "\u0085source_1\u0085"
	const tokenID = "\u0085token_1\u0085"
	q := &managementExternalIntegrationSourceTokenSecretQueriesStub{err: pgx.ErrNoRows}

	_, found, err := findManagementExternalIntegrationSourceTokenSecret(context.Background(), q, sourceID, tokenID)
	if err != nil || found {
		t.Fatalf("find token secret found = %v, err = %v", found, err)
	}
	want := []postgresqueries.FindManagementExternalIntegrationSourceTokenSecretParams{{
		SourceID: sourceID,
		TokenID:  tokenID,
	}}
	if !reflect.DeepEqual(q.calls, want) {
		t.Fatalf("query calls = %#v, want %#v", q.calls, want)
	}
}

func TestFindManagementExternalIntegrationSourceTokenSecretSkipsEmptyIDs(t *testing.T) {
	tests := []struct {
		name     string
		sourceID string
		tokenID  string
	}{
		{name: "empty source", sourceID: " \t\r\n ", tokenID: "token_1"},
		{name: "empty token", sourceID: "source_1", tokenID: "\uFEFF\u00A0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := &managementExternalIntegrationSourceTokenSecretQueriesStub{}
			got, found, err := findManagementExternalIntegrationSourceTokenSecret(
				context.Background(), q, tt.sourceID, tt.tokenID,
			)
			if err != nil || found || got != "" {
				t.Fatalf("token secret = %q, found = %v, err = %v", got, found, err)
			}
			if len(q.calls) != 0 {
				t.Fatalf("empty IDs must not query: %#v", q.calls)
			}
		})
	}
}

func TestFindManagementExternalIntegrationSourceTokenSecretHandlesNotFoundAndQueryError(t *testing.T) {
	t.Run("not found", func(t *testing.T) {
		q := &managementExternalIntegrationSourceTokenSecretQueriesStub{err: pgx.ErrNoRows}
		got, found, err := findManagementExternalIntegrationSourceTokenSecret(
			context.Background(), q, "source_1", "token_1",
		)
		if err != nil || found || got != "" {
			t.Fatalf("token secret = %q, found = %v, err = %v", got, found, err)
		}
	})

	t.Run("query error", func(t *testing.T) {
		wantErr := errors.New("query failed")
		q := &managementExternalIntegrationSourceTokenSecretQueriesStub{err: wantErr}
		got, found, err := findManagementExternalIntegrationSourceTokenSecret(
			context.Background(), q, "source_1", "token_1",
		)
		if got != "" || found || !errors.Is(err, wantErr) ||
			!strings.Contains(err.Error(), "find management external integration source token secret") {
			t.Fatalf("token secret = %q, found = %v, err = %v", got, found, err)
		}
	})
}

func TestManagementExternalIntegrationSourceTokenSecretSQLIsNarrowPointLookup(t *testing.T) {
	source, err := os.ReadFile("queries/w2_management_external_integration_source_list.sql")
	if err != nil {
		t.Fatalf("read management external integration source SQL: %v", err)
	}
	sql := strings.ReplaceAll(string(source), "\r\n", "\n")
	query := managementExternalIntegrationSourceSQLSection(
		t,
		sql,
		"-- name: FindManagementExternalIntegrationSourceTokenSecret :one",
		"",
	)
	for _, required := range []string{
		"SELECT tokens.token_secret_encrypted",
		"FROM juhe_business.external_integration_source_tokens AS tokens",
		"JOIN juhe_business.external_integration_sources AS sources",
		"ON sources.id = tokens.source_ref_id",
		"WHERE sources.id = sqlc.arg(source_id)::text",
		"AND tokens.id = sqlc.arg(token_id)::text",
	} {
		if !strings.Contains(query, required) {
			t.Fatalf("token secret query missing %q:\n%s", required, query)
		}
	}
	lower := strings.ToLower(query)
	for _, forbidden := range []string{
		"select *", "token_hash", "token_prefix", "token_suffix", "status =", "expires_at", "revoked_at", "order by", "limit ",
	} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("token secret point query must not contain %q:\n%s", forbidden, query)
		}
	}
}

type managementExternalIntegrationSourceTokenSecretQueriesStub struct {
	ciphertext string
	err        error
	calls      []postgresqueries.FindManagementExternalIntegrationSourceTokenSecretParams
}

func (s *managementExternalIntegrationSourceTokenSecretQueriesStub) FindManagementExternalIntegrationSourceTokenSecret(
	_ context.Context,
	params postgresqueries.FindManagementExternalIntegrationSourceTokenSecretParams,
) (string, error) {
	s.calls = append(s.calls, params)
	return s.ciphertext, s.err
}

var _ managementExternalIntegrationSourceTokenSecretQueries = (*managementExternalIntegrationSourceTokenSecretQueriesStub)(nil)

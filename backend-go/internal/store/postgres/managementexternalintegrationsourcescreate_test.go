package postgres

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

func TestManagementExternalIntegrationSourceCreateSQLContract(t *testing.T) {
	raw, err := os.ReadFile("queries/w2_management_external_integration_source_list.sql")
	if err != nil {
		t.Fatalf("read external integration source SQL: %v", err)
	}
	sql := string(raw)
	for _, required := range []string{
		"-- name: InsertManagementExternalIntegrationSource :one",
		"INSERT INTO juhe_business.external_integration_sources",
		"-- name: InsertManagementExternalIntegrationSourceToken :one",
		"INSERT INTO juhe_business.external_integration_source_tokens",
		"token_secret_encrypted",
		"RETURNING",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("external integration source create SQL missing %q", required)
		}
	}
	createStart := strings.Index(sql, "-- name: InsertManagementExternalIntegrationSource :one")
	deleteStart := strings.Index(sql, "-- name: CountManagementExternalIntegrationSourceTokensForDelete :one")
	if createStart < 0 || deleteStart <= createStart {
		t.Fatal("external integration source create query ordering is invalid")
	}
	createSQL := strings.ToLower(sql[createStart:deleteStart])
	for _, forbidden := range []string{"select count(", "select exists", "for update"} {
		if strings.Contains(createSQL, forbidden) {
			t.Fatalf("external integration source create must rely on insert constraints, found %q", forbidden)
		}
	}
}

func TestCreateManagementExternalIntegrationSourceTx(t *testing.T) {
	now := time.Date(2026, 7, 16, 10, 11, 12, 0, time.FixedZone("UTC+8", 8*60*60))
	expiresAt := now.Add(24 * time.Hour)
	notes := "partner source"
	q := &managementExternalIntegrationSourceCreateQueriesStub{
		sourceRow: postgresqueries.JuheBusinessExternalIntegrationSource{
			ID:             "extsrc_create",
			Name:           "Partner",
			Status:         "active",
			ScopesJson:     `["responses:write"]`,
			RateLimitsJson: `[]`,
			ExpiresAt:      pgTimestamptz(expiresAt),
			Notes:          pgTextFromStringPtr(&notes),
			CreatedAt:      pgTimestamptz(now),
			UpdatedAt:      pgTimestamptz(now),
		},
		tokenRow: postgresqueries.InsertManagementExternalIntegrationSourceTokenRow{
			SourceRefID: "extsrc_create",
			ID:          "exttok_create",
			Name:        "Partner 生产 Token",
			TokenPrefix: "juis_abc",
			TokenSuffix: "12345678",
			Status:      "active",
			ScopesJson:  `["responses:write"]`,
			ExpiresAt:   pgTimestamptz(expiresAt),
			CreatedAt:   pgTimestamptz(now),
			UpdatedAt:   pgTimestamptz(now),
		},
	}
	input := port.ManagementExternalIntegrationSourceCreateInput{
		SourceID:             "extsrc_create",
		Name:                 "Partner",
		Status:               "active",
		ScopesJSON:           `["responses:write"]`,
		RateLimitsJSON:       `[]`,
		ExpiresAt:            &expiresAt,
		Notes:                &notes,
		TokenID:              "exttok_create",
		TokenName:            "Partner 生产 Token",
		TokenHash:            "token-hash",
		TokenSecretEncrypted: "encrypted-json",
		TokenPrefix:          "juis_abc",
		TokenSuffix:          "12345678",
		TokenStatus:          "active",
		TokenScopesJSON:      `["responses:write"]`,
		TokenExpiresAt:       &expiresAt,
		CreatedAt:            now,
		UpdatedAt:            now,
	}

	result, err := createManagementExternalIntegrationSourceTx(context.Background(), q, input)
	if err != nil {
		t.Fatalf("create external integration source: %v", err)
	}
	if got := strings.Join(q.calls, ","); got != "source,token" {
		t.Fatalf("query order = %q, want source,token", got)
	}
	if q.sourceArg.SourceID != input.SourceID || q.sourceArg.Name != input.Name ||
		q.sourceArg.ScopesJson != input.ScopesJSON || q.sourceArg.RateLimitsJson != input.RateLimitsJSON {
		t.Fatalf("unexpected source insert params: %+v", q.sourceArg)
	}
	if !q.sourceArg.ExpiresAt.Valid || !q.sourceArg.ExpiresAt.Time.Equal(expiresAt) ||
		!q.sourceArg.Notes.Valid || q.sourceArg.Notes.String != notes {
		t.Fatalf("source nullable params were not preserved: %+v", q.sourceArg)
	}
	if !q.sourceArg.CreatedAt.Time.Equal(now) || !q.sourceArg.UpdatedAt.Time.Equal(now) {
		t.Fatalf("source timestamps were not preserved: %+v", q.sourceArg)
	}
	if q.tokenArg.TokenID != input.TokenID || q.tokenArg.SourceID != input.SourceID ||
		q.tokenArg.TokenHash != input.TokenHash ||
		q.tokenArg.TokenSecretEncrypted != input.TokenSecretEncrypted ||
		q.tokenArg.TokenPrefix != input.TokenPrefix || q.tokenArg.TokenSuffix != input.TokenSuffix {
		t.Fatalf("unexpected token insert params: %+v", q.tokenArg)
	}
	if result.Source.ID != input.SourceID || result.Source.Name != input.Name ||
		result.Source.ExpiresAt == nil || !result.Source.ExpiresAt.Equal(expiresAt.UTC()) {
		t.Fatalf("unexpected created source: %+v", result.Source)
	}
	if result.Token.SourceRefID != input.SourceID || result.Token.ID != input.TokenID ||
		result.Token.TokenPrefix != input.TokenPrefix || result.Token.TokenSuffix != input.TokenSuffix {
		t.Fatalf("unexpected created token: %+v", result.Token)
	}
}

func TestCreateManagementExternalIntegrationSourceTxMapsConstraintsAndPreservesErrors(t *testing.T) {
	sourceFailure := errors.New("source insert unavailable")
	tokenFailure := errors.New("token insert unavailable")
	tests := []struct {
		name           string
		sourceErr      error
		tokenErr       error
		wantErr        error
		wantTokenCalls int
	}{
		{
			name: "duplicate source name",
			sourceErr: &pgconn.PgError{
				Code:           "23505",
				ConstraintName: "idx_external_integration_sources_name_unique_lower",
			},
			wantErr: port.ErrManagementExternalIntegrationSourceNameExists,
		},
		{
			name:      "unknown source failure",
			sourceErr: sourceFailure,
			wantErr:   sourceFailure,
		},
		{
			name: "duplicate token hash",
			tokenErr: &pgconn.PgError{
				Code:           "23505",
				ConstraintName: "external_integration_source_tokens_token_hash_key",
			},
			wantErr:        port.ErrManagementExternalIntegrationSourceTokenHashExists,
			wantTokenCalls: 1,
		},
		{
			name:           "unknown token failure",
			tokenErr:       tokenFailure,
			wantErr:        tokenFailure,
			wantTokenCalls: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, 7, 16, 2, 3, 4, 0, time.UTC)
			q := &managementExternalIntegrationSourceCreateQueriesStub{
				sourceRow: postgresqueries.JuheBusinessExternalIntegrationSource{
					ID:        "extsrc_create",
					Name:      "Partner",
					Status:    "active",
					CreatedAt: pgTimestamptz(now),
					UpdatedAt: pgTimestamptz(now),
				},
				sourceErr: test.sourceErr,
				tokenErr:  test.tokenErr,
			}
			_, err := createManagementExternalIntegrationSourceTx(
				context.Background(),
				q,
				port.ManagementExternalIntegrationSourceCreateInput{
					SourceID:  "extsrc_create",
					TokenID:   "exttok_create",
					CreatedAt: now,
					UpdatedAt: now,
				},
			)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want errors.Is(_, %v)", err, test.wantErr)
			}
			tokenCalls := 0
			for _, call := range q.calls {
				if call == "token" {
					tokenCalls++
				}
			}
			if tokenCalls != test.wantTokenCalls {
				t.Fatalf("token calls = %d, want %d; calls=%v", tokenCalls, test.wantTokenCalls, q.calls)
			}
		})
	}
}

type managementExternalIntegrationSourceCreateQueriesStub struct {
	sourceRow postgresqueries.JuheBusinessExternalIntegrationSource
	sourceErr error
	tokenRow  postgresqueries.InsertManagementExternalIntegrationSourceTokenRow
	tokenErr  error
	sourceArg postgresqueries.InsertManagementExternalIntegrationSourceParams
	tokenArg  postgresqueries.InsertManagementExternalIntegrationSourceTokenParams
	calls     []string
}

func (s *managementExternalIntegrationSourceCreateQueriesStub) InsertManagementExternalIntegrationSource(
	_ context.Context,
	arg postgresqueries.InsertManagementExternalIntegrationSourceParams,
) (postgresqueries.JuheBusinessExternalIntegrationSource, error) {
	s.calls = append(s.calls, "source")
	s.sourceArg = arg
	return s.sourceRow, s.sourceErr
}

func (s *managementExternalIntegrationSourceCreateQueriesStub) InsertManagementExternalIntegrationSourceToken(
	_ context.Context,
	arg postgresqueries.InsertManagementExternalIntegrationSourceTokenParams,
) (postgresqueries.InsertManagementExternalIntegrationSourceTokenRow, error) {
	s.calls = append(s.calls, "token")
	s.tokenArg = arg
	return s.tokenRow, s.tokenErr
}

var _ managementExternalIntegrationSourceCreateQueries = (*managementExternalIntegrationSourceCreateQueriesStub)(nil)

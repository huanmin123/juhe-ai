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
	"github.com/jackc/pgx/v5/pgconn"

	"juhe-ai/backend-go/internal/modules/publicapi"
	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

func TestManagementExternalIntegrationSourceTokenCreateSQLContract(t *testing.T) {
	raw, err := os.ReadFile("queries/w2_management_external_integration_source_list.sql")
	if err != nil {
		t.Fatalf("read external integration source SQL: %v", err)
	}
	sql := strings.ReplaceAll(string(raw), "\r\n", "\n")
	for _, required := range []string{
		"-- name: FindManagementExternalIntegrationSourceForUpdate :one",
		"WHERE sources.id = sqlc.arg(source_id)::text\nFOR UPDATE;",
		"-- name: ListManagementExternalIntegrationSourceTokens :many",
		"WHERE tokens.source_ref_id = sqlc.arg(source_id)::text",
		"ORDER BY tokens.created_at DESC, tokens.id DESC;",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("external integration source token create SQL missing %q", required)
		}
	}
}

func TestCreateManagementExternalIntegrationSourceToken(t *testing.T) {
	now := time.Date(2026, 7, 16, 10, 11, 12, 123000000, time.FixedZone("UTC+8", 8*60*60))
	expiresAt := now.Add(24 * time.Hour)
	notes := "partner source"
	input := managementExternalIntegrationSourceTokenCreateTestInput(now, &expiresAt)
	q := successfulManagementExternalIntegrationSourceTokenCreateQueries(input, notes)

	result, err := createManagementExternalIntegrationSourceToken(context.Background(), q, input)
	if err != nil {
		t.Fatalf("create external integration source token: %v", err)
	}
	if got, want := q.calls, []string{"lock", "insert"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("query order = %#v, want %#v", got, want)
	}
	if q.lockSourceID != input.SourceID {
		t.Fatalf("source-specific query IDs = lock %q", q.lockSourceID)
	}
	wantInsert := postgresqueries.InsertManagementExternalIntegrationSourceTokenParams{
		TokenID:              input.TokenID,
		SourceID:             input.SourceID,
		TokenName:            input.Name,
		TokenHash:            input.TokenHash,
		TokenSecretEncrypted: input.TokenSecretEncrypted,
		TokenPrefix:          input.TokenPrefix,
		TokenSuffix:          input.TokenSuffix,
		TokenStatus:          input.Status,
		TokenScopesJson:      input.ScopesJSON,
		TokenExpiresAt:       pgTimestamptzPtr(input.ExpiresAt),
		CreatedAt:            pgTimestamptz(input.CreatedAt),
		UpdatedAt:            pgTimestamptz(input.UpdatedAt),
	}
	if !reflect.DeepEqual(q.insertArg, wantInsert) {
		t.Fatalf("insert params = %#v, want %#v", q.insertArg, wantInsert)
	}
	if result.CreatedTokenID != input.TokenID {
		t.Fatalf("created token ID = %q, want %q", result.CreatedTokenID, input.TokenID)
	}
	if result.Source.ID != input.SourceID || result.Source.Name != "Partner" ||
		result.Source.Notes == nil || *result.Source.Notes != notes ||
		result.Source.ExpiresAt == nil || !result.Source.ExpiresAt.Equal(expiresAt.UTC()) {
		t.Fatalf("source result = %#v", result.Source)
	}
	if len(result.Tokens) != 1 || result.Tokens[0].ID != input.TokenID || result.Tokens[0].TokenPrefix != input.TokenPrefix {
		t.Fatalf("token result = %#v", result.Tokens)
	}
}

func TestCreateManagementExternalIntegrationSourceTokenRejectsMissingAndBuiltInSources(t *testing.T) {
	tests := []struct {
		name    string
		queries *managementExternalIntegrationSourceTokenCreateQueriesStub
		wantErr error
	}{
		{
			name:    "missing",
			queries: &managementExternalIntegrationSourceTokenCreateQueriesStub{lockErr: pgx.ErrNoRows},
			wantErr: port.ErrManagementExternalIntegrationSourceNotFound,
		},
		{
			name: "built in",
			queries: &managementExternalIntegrationSourceTokenCreateQueriesStub{lockRow: postgresqueries.JuheBusinessExternalIntegrationSource{
				ID: publicapi.BuiltInTestSourceID,
			}},
			wantErr: port.ErrManagementExternalIntegrationSourceBuiltInTokenCreateRestricted,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := createManagementExternalIntegrationSourceToken(
				context.Background(),
				test.queries,
				port.ManagementExternalIntegrationSourceTokenCreateInput{SourceID: "source_1"},
			)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want errors.Is(_, %v)", err, test.wantErr)
			}
			if !reflect.DeepEqual(test.queries.calls, []string{"lock"}) {
				t.Fatalf("calls = %#v, want lock only", test.queries.calls)
			}
		})
	}
}

func TestCreateManagementExternalIntegrationSourceTokenMapsOnlyExactTokenHashConstraint(t *testing.T) {
	tests := []struct {
		name    string
		failure error
		wantErr error
	}{
		{
			name: "exact token hash constraint",
			failure: &pgconn.PgError{
				Code:           "23505",
				ConstraintName: managementExternalIntegrationSourceTokenHashUniqueConstraint,
			},
			wantErr: port.ErrManagementExternalIntegrationSourceTokenHashExists,
		},
		{
			name: "other unique constraint",
			failure: &pgconn.PgError{
				Code:           "23505",
				ConstraintName: "external_integration_source_tokens_pkey",
			},
		},
		{
			name:    "other insert error",
			failure: errors.New("insert unavailable"),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			q := &managementExternalIntegrationSourceTokenCreateQueriesStub{
				lockRow:   postgresqueries.JuheBusinessExternalIntegrationSource{ID: "source_1"},
				insertErr: test.failure,
			}
			_, err := createManagementExternalIntegrationSourceToken(
				context.Background(),
				q,
				port.ManagementExternalIntegrationSourceTokenCreateInput{SourceID: "source_1"},
			)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("error = %v, want errors.Is(_, %v)", err, test.wantErr)
				}
			} else if !errors.Is(err, test.failure) || !strings.Contains(err.Error(), "insert management external integration source token") {
				t.Fatalf("error = %v, want wrapped original insert failure", err)
			}
			if !reflect.DeepEqual(q.calls, []string{"lock", "insert"}) {
				t.Fatalf("calls = %#v, want lock then insert", q.calls)
			}
		})
	}
}

func TestCreateManagementExternalIntegrationSourceTokenPreservesQueryFailures(t *testing.T) {
	lockErr := errors.New("lock unavailable")
	tests := []struct {
		name      string
		configure func(*managementExternalIntegrationSourceTokenCreateQueriesStub)
		wantErr   error
		wantCalls []string
	}{
		{
			name: "lock",
			configure: func(q *managementExternalIntegrationSourceTokenCreateQueriesStub) {
				q.lockErr = lockErr
			},
			wantErr:   lockErr,
			wantCalls: []string{"lock"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, 7, 16, 2, 3, 4, 0, time.UTC)
			input := managementExternalIntegrationSourceTokenCreateTestInput(now, nil)
			q := successfulManagementExternalIntegrationSourceTokenCreateQueries(input, "notes")
			test.configure(q)
			_, err := createManagementExternalIntegrationSourceToken(context.Background(), q, input)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want errors.Is(_, %v)", err, test.wantErr)
			}
			if !reflect.DeepEqual(q.calls, test.wantCalls) {
				t.Fatalf("calls = %#v, want %#v", q.calls, test.wantCalls)
			}
		})
	}
}

func TestCreateManagementExternalIntegrationSourceTokenValidatesReadback(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*managementExternalIntegrationSourceTokenCreateQueriesStub, port.ManagementExternalIntegrationSourceTokenCreateInput)
		wantText  string
	}{
		{
			name: "created ID missing",
			configure: func(q *managementExternalIntegrationSourceTokenCreateQueriesStub, input port.ManagementExternalIntegrationSourceTokenCreateInput) {
				q.tokenRows = q.tokenRows[:1]
			},
			wantText: "created token ID readback count = 0",
		},
		{
			name: "created ID duplicated",
			configure: func(q *managementExternalIntegrationSourceTokenCreateQueriesStub, input port.ManagementExternalIntegrationSourceTokenCreateInput) {
				q.tokenRows = append(q.tokenRows, q.tokenRows[1])
			},
			wantText: "created token ID readback count = 2",
		},
		{
			name: "foreign source row",
			configure: func(q *managementExternalIntegrationSourceTokenCreateQueriesStub, input port.ManagementExternalIntegrationSourceTokenCreateInput) {
				q.tokenRows[0].SourceRefID = "source_other"
			},
			wantText: "unexpected source_ref_id",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, 7, 16, 2, 3, 4, 0, time.UTC)
			input := managementExternalIntegrationSourceTokenCreateTestInput(now, nil)
			q := successfulManagementExternalIntegrationSourceTokenCreateQueries(input, "notes")
			test.configure(q, input)
			_, err := createManagementExternalIntegrationSourceToken(context.Background(), q, input)
			if err != nil {
				t.Fatalf("error = %v, want lightweight insert result", err)
			}
			if !reflect.DeepEqual(q.calls, []string{"lock", "insert"}) {
				t.Fatalf("calls = %#v", q.calls)
			}
		})
	}
}

func TestCreateManagementExternalIntegrationSourceTokenTransactionCommitsSuccess(t *testing.T) {
	now := time.Date(2026, 7, 16, 2, 3, 4, 0, time.UTC)
	input := managementExternalIntegrationSourceTokenCreateTestInput(now, nil)
	q := successfulManagementExternalIntegrationSourceTokenCreateQueries(input, "notes")
	tx := &managementExternalIntegrationSourceTokenCreateTxStub{}

	result, err := createManagementExternalIntegrationSourceTokenInTx(
		context.Background(),
		func(context.Context, pgx.TxOptions) (pgx.Tx, error) { return tx, nil },
		func(got pgx.Tx) managementExternalIntegrationSourceTokenCreateQueries {
			if got != tx {
				t.Fatalf("queries tx = %T, want transaction stub", got)
			}
			return q
		},
		input,
	)
	if err != nil {
		t.Fatalf("create token in tx: %v", err)
	}
	if result.CreatedTokenID != input.TokenID || tx.commitCalls != 1 || tx.rollbackCalls != 0 {
		t.Fatalf("result=%#v commit/rollback=%d/%d", result, tx.commitCalls, tx.rollbackCalls)
	}
}

func TestCreateManagementExternalIntegrationSourceTokenTransactionRollsBackFailures(t *testing.T) {
	now := time.Date(2026, 7, 16, 2, 3, 4, 0, time.UTC)
	input := managementExternalIntegrationSourceTokenCreateTestInput(now, nil)
	operationErr := errors.New("insert unavailable")
	commitErr := errors.New("commit unavailable")
	tests := []struct {
		name      string
		beginErr  error
		commitErr error
		queryErr  error
		wantErr   error
		wantBegin string
		commit    int
		rollback  int
	}{
		{name: "begin error", beginErr: errors.New("begin unavailable"), wantBegin: "begin management external integration source token create", commit: 0, rollback: 0},
		{name: "operation error", queryErr: operationErr, wantErr: operationErr, commit: 0, rollback: 1},
		{name: "commit error", commitErr: commitErr, wantErr: commitErr, commit: 1, rollback: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			tx := &managementExternalIntegrationSourceTokenCreateTxStub{commitErr: test.commitErr}
			q := successfulManagementExternalIntegrationSourceTokenCreateQueries(input, "notes")
			q.insertErr = test.queryErr
			_, err := createManagementExternalIntegrationSourceTokenInTx(
				ctx,
				func(context.Context, pgx.TxOptions) (pgx.Tx, error) {
					if test.beginErr != nil {
						return nil, test.beginErr
					}
					cancel()
					return tx, nil
				},
				func(pgx.Tx) managementExternalIntegrationSourceTokenCreateQueries { return q },
				input,
			)
			if test.beginErr != nil {
				if !errors.Is(err, test.beginErr) || !strings.Contains(err.Error(), test.wantBegin) {
					t.Fatalf("begin error = %v", err)
				}
			} else if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want errors.Is(_, %v)", err, test.wantErr)
			}
			if tx.commitCalls != test.commit || tx.rollbackCalls != test.rollback {
				t.Fatalf("commit/rollback = %d/%d, want %d/%d", tx.commitCalls, tx.rollbackCalls, test.commit, test.rollback)
			}
			if test.rollback == 1 && tx.rollbackContextErr != nil {
				t.Fatalf("rollback context error = %v, want independent live context", tx.rollbackContextErr)
			}
		})
	}
}

func managementExternalIntegrationSourceTokenCreateTestInput(
	now time.Time,
	expiresAt *time.Time,
) port.ManagementExternalIntegrationSourceTokenCreateInput {
	return port.ManagementExternalIntegrationSourceTokenCreateInput{
		TokenID:              "exttok_create",
		SourceID:             "extsrc_create",
		Name:                 "Partner token",
		TokenHash:            "token-hash",
		TokenSecretEncrypted: "encrypted-json",
		TokenPrefix:          "juis_abc",
		TokenSuffix:          "12345678",
		Status:               "active",
		ScopesJSON:           `["responses:write"]`,
		ExpiresAt:            expiresAt,
		CreatedAt:            now,
		UpdatedAt:            now,
	}
}

func successfulManagementExternalIntegrationSourceTokenCreateQueries(
	input port.ManagementExternalIntegrationSourceTokenCreateInput,
	notes string,
) *managementExternalIntegrationSourceTokenCreateQueriesStub {
	sourceRow := postgresqueries.JuheBusinessExternalIntegrationSource{
		ID: input.SourceID, Name: "Partner", Status: "active", ScopesJson: `["responses:write"]`,
		RateLimitsJson: `[]`, ExpiresAt: pgTimestamptzPtr(input.ExpiresAt), Notes: pgTextFromStringPtr(&notes),
		CreatedAt: pgTimestamptz(input.CreatedAt), UpdatedAt: pgTimestamptz(input.UpdatedAt),
	}
	return &managementExternalIntegrationSourceTokenCreateQueriesStub{
		lockRow:   sourceRow,
		sourceRow: sourceRow,
		insertRow: postgresqueries.InsertManagementExternalIntegrationSourceTokenRow{
			SourceRefID: input.SourceID, ID: input.TokenID, Name: input.Name,
			TokenPrefix: input.TokenPrefix, TokenSuffix: input.TokenSuffix, Status: input.Status,
			ScopesJson: input.ScopesJSON, ExpiresAt: pgTimestamptzPtr(input.ExpiresAt),
			CreatedAt: pgTimestamptz(input.CreatedAt), UpdatedAt: pgTimestamptz(input.UpdatedAt),
		},
		tokenRows: []postgresqueries.ListManagementExternalIntegrationSourceTokensRow{
			{
				SourceRefID: input.SourceID,
				ID:          "exttok_existing",
				Name:        "Existing token",
				TokenPrefix: "juis_old",
				TokenSuffix: "87654321",
				Status:      "active",
				ScopesJson:  `[]`,
				CreatedAt:   pgTimestamptz(input.CreatedAt.Add(-time.Hour)),
				UpdatedAt:   pgTimestamptz(input.UpdatedAt.Add(-time.Hour)),
			},
			{
				SourceRefID: input.SourceID,
				ID:          input.TokenID,
				Name:        input.Name,
				TokenPrefix: input.TokenPrefix,
				TokenSuffix: input.TokenSuffix,
				Status:      input.Status,
				ScopesJson:  input.ScopesJSON,
				ExpiresAt:   pgTimestamptzPtr(input.ExpiresAt),
				CreatedAt:   pgTimestamptz(input.CreatedAt),
				UpdatedAt:   pgTimestamptz(input.UpdatedAt),
			},
		},
	}
}

type managementExternalIntegrationSourceTokenCreateQueriesStub struct {
	lockRow        postgresqueries.JuheBusinessExternalIntegrationSource
	lockErr        error
	insertRow      postgresqueries.InsertManagementExternalIntegrationSourceTokenRow
	insertErr      error
	sourceRow      postgresqueries.JuheBusinessExternalIntegrationSource
	sourceErr      error
	tokenRows      []postgresqueries.ListManagementExternalIntegrationSourceTokensRow
	tokensErr      error
	lockSourceID   string
	insertArg      postgresqueries.InsertManagementExternalIntegrationSourceTokenParams
	sourceID       string
	tokensSourceID string
	calls          []string
}

func (s *managementExternalIntegrationSourceTokenCreateQueriesStub) FindManagementExternalIntegrationSourceForUpdate(
	_ context.Context,
	sourceID string,
) (postgresqueries.JuheBusinessExternalIntegrationSource, error) {
	s.calls = append(s.calls, "lock")
	s.lockSourceID = sourceID
	return s.lockRow, s.lockErr
}

func (s *managementExternalIntegrationSourceTokenCreateQueriesStub) InsertManagementExternalIntegrationSourceToken(
	_ context.Context,
	arg postgresqueries.InsertManagementExternalIntegrationSourceTokenParams,
) (postgresqueries.InsertManagementExternalIntegrationSourceTokenRow, error) {
	s.calls = append(s.calls, "insert")
	s.insertArg = arg
	return s.insertRow, s.insertErr
}

func (s *managementExternalIntegrationSourceTokenCreateQueriesStub) FindManagementExternalIntegrationSource(
	_ context.Context,
	sourceID string,
) (postgresqueries.JuheBusinessExternalIntegrationSource, error) {
	s.calls = append(s.calls, "source")
	s.sourceID = sourceID
	return s.sourceRow, s.sourceErr
}

func (s *managementExternalIntegrationSourceTokenCreateQueriesStub) ListManagementExternalIntegrationSourceTokens(
	_ context.Context,
	sourceID string,
) ([]postgresqueries.ListManagementExternalIntegrationSourceTokensRow, error) {
	s.calls = append(s.calls, "tokens")
	s.tokensSourceID = sourceID
	return append([]postgresqueries.ListManagementExternalIntegrationSourceTokensRow(nil), s.tokenRows...), s.tokensErr
}

type managementExternalIntegrationSourceTokenCreateTxStub struct {
	pgx.Tx
	commitErr          error
	commitCalls        int
	rollbackCalls      int
	rollbackContextErr error
}

func (s *managementExternalIntegrationSourceTokenCreateTxStub) Commit(context.Context) error {
	s.commitCalls++
	return s.commitErr
}

func (s *managementExternalIntegrationSourceTokenCreateTxStub) Rollback(ctx context.Context) error {
	s.rollbackCalls++
	s.rollbackContextErr = ctx.Err()
	return nil
}

var _ managementExternalIntegrationSourceTokenCreateQueries = (*managementExternalIntegrationSourceTokenCreateQueriesStub)(nil)
var _ pgx.Tx = (*managementExternalIntegrationSourceTokenCreateTxStub)(nil)

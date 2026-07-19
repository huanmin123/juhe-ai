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
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

func TestBuiltInExternalIntegrationSourceResetSQLContract(t *testing.T) {
	raw, err := os.ReadFile("queries/w2_management_external_integration_source_list.sql")
	if err != nil {
		t.Fatal(err)
	}
	allSQL := string(raw)
	start := strings.Index(allSQL, "-- name: ResetBuiltInExternalIntegrationSourceToken")
	end := strings.Index(allSQL[start:], "-- name: TouchBuiltInExternalIntegrationSource")
	if start < 0 || end < 0 {
		t.Fatal("reset query section missing")
	}
	resetSQL := allSQL[start : start+end]
	sql := strings.ToLower(resetSQL)
	for _, fragment := range []string{
		"token_hash =", "token_secret_encrypted =", "token_prefix =", "token_suffix =",
		"status = 'active'", "revoked_at = null", "updated_at =",
		"where id = 'exttok_builtin_test'", "and source_ref_id = 'extsrc_builtin_test'",
	} {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("reset SQL missing %q:\n%s", fragment, resetSQL)
		}
	}
	for _, forbidden := range []string{"name =", "scopes_json =", "expires_at =", "last_used_at ="} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("reset SQL must preserve %q:\n%s", forbidden, resetSQL)
		}
	}
	if !strings.Contains(allSQL, "WHERE sources.id = 'extsrc_builtin_test'\nFOR UPDATE") ||
		!strings.Contains(allSQL, "WHERE tokens.id = 'exttok_builtin_test'") {
		t.Fatal("built-in reset must lock fixed source before fixed token")
	}
}

func TestResetBuiltInExternalIntegrationSourceTokenSuccess(t *testing.T) {
	now := time.Date(2026, 7, 19, 9, 8, 7, 0, time.UTC)
	q := successfulBuiltInResetQueries(now)
	tx := &builtInResetTxStub{}
	result, err := resetManagementExternalIntegrationSourceBuiltInTokenInTx(
		context.Background(),
		func(context.Context, pgx.TxOptions) (pgx.Tx, error) { return tx, nil },
		func(pgx.Tx) managementExternalIntegrationSourceBuiltInResetQueries { return q },
		port.ManagementExternalIntegrationSourceBuiltInResetInput{
			TokenHash: "new-hash", TokenSecretEncrypted: "new-secret",
			TokenPrefix: "juis_new", TokenSuffix: "87654321", UpdatedAt: now,
		},
	)
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if got, want := q.calls, []string{"source-lock", "token-lock", "token-update", "source-touch", "source-read", "token-read"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %#v, want %#v", got, want)
	}
	if result.OldTokenHash != "old-hash" || result.Source.ID != "extsrc_builtin_test" || result.Token.ID != "exttok_builtin_test" {
		t.Fatalf("result = %#v", result)
	}
	if q.resetArg.TokenHash != "new-hash" || q.resetArg.TokenSecretEncrypted != "new-secret" || q.resetArg.TokenPrefix != "juis_new" || q.resetArg.TokenSuffix != "87654321" || !q.resetArg.UpdatedAt.Time.Equal(now) {
		t.Fatalf("reset args = %#v", q.resetArg)
	}
	if tx.commitCalls != 1 || tx.rollbackCalls != 0 {
		t.Fatalf("commit/rollback = %d/%d", tx.commitCalls, tx.rollbackCalls)
	}
}

func TestResetBuiltInExternalIntegrationSourceTokenMapsMissingRows(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(*builtInResetQueriesStub)
		wantCalls []string
	}{
		{name: "source", configure: func(q *builtInResetQueriesStub) { q.sourceLockErr = pgx.ErrNoRows }, wantCalls: []string{"source-lock"}},
		{name: "token", configure: func(q *builtInResetQueriesStub) { q.tokenLockErr = pgx.ErrNoRows }, wantCalls: []string{"source-lock", "token-lock"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := successfulBuiltInResetQueries(time.Now())
			tc.configure(q)
			_, err := resetManagementExternalIntegrationSourceBuiltInToken(context.Background(), q, port.ManagementExternalIntegrationSourceBuiltInResetInput{})
			if !errors.Is(err, port.ErrManagementExternalIntegrationSourceBuiltInResetNotFound) {
				t.Fatalf("err = %v", err)
			}
			if !reflect.DeepEqual(q.calls, tc.wantCalls) {
				t.Fatalf("calls = %#v", q.calls)
			}
		})
	}
}

func TestResetBuiltInExternalIntegrationSourceTokenMapsHashConflict(t *testing.T) {
	q := successfulBuiltInResetQueries(time.Now())
	q.resetErr = &pgconn.PgError{Code: "23505", ConstraintName: managementExternalIntegrationSourceTokenHashUniqueConstraint}
	_, err := resetManagementExternalIntegrationSourceBuiltInToken(context.Background(), q, port.ManagementExternalIntegrationSourceBuiltInResetInput{})
	if !errors.Is(err, port.ErrManagementExternalIntegrationSourceTokenHashExists) {
		t.Fatalf("err = %v", err)
	}
}

func TestResetBuiltInExternalIntegrationSourceTokenRollsBackWithIndependentContext(t *testing.T) {
	operationErr := errors.New("reset unavailable")
	commitErr := errors.New("commit unavailable")
	for _, tc := range []struct {
		name                    string
		operationErr, commitErr error
		wantCommit              int
	}{
		{name: "operation", operationErr: operationErr},
		{name: "commit", commitErr: commitErr, wantCommit: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			tx := &builtInResetTxStub{commitErr: tc.commitErr}
			q := successfulBuiltInResetQueries(time.Now())
			q.resetErr = tc.operationErr
			_, err := resetManagementExternalIntegrationSourceBuiltInTokenInTx(ctx,
				func(context.Context, pgx.TxOptions) (pgx.Tx, error) { cancel(); return tx, nil },
				func(pgx.Tx) managementExternalIntegrationSourceBuiltInResetQueries { return q },
				port.ManagementExternalIntegrationSourceBuiltInResetInput{})
			if err == nil {
				t.Fatal("want error")
			}
			if tx.commitCalls != tc.wantCommit || tx.rollbackCalls != 1 || tx.rollbackContextErr != nil {
				t.Fatalf("commit/rollback/context = %d/%d/%v", tx.commitCalls, tx.rollbackCalls, tx.rollbackContextErr)
			}
		})
	}
}

func successfulBuiltInResetQueries(now time.Time) *builtInResetQueriesStub {
	return &builtInResetQueriesStub{
		sourceLock: postgresqueries.JuheBusinessExternalIntegrationSource{ID: "extsrc_builtin_test"},
		tokenLock:  postgresqueries.LockBuiltInExternalIntegrationSourceTokenForResetRow{TokenHash: "old-hash", ID: "exttok_builtin_test"},
		sourceRead: postgresqueries.JuheBusinessExternalIntegrationSource{ID: "extsrc_builtin_test", Name: "Built-in", Status: "active", ScopesJson: "[]", RateLimitsJson: "[]", CreatedAt: pgtype.Timestamptz{Time: now.Add(-time.Hour), Valid: true}, UpdatedAt: pgtype.Timestamptz{Time: now, Valid: true}},
		tokenRead:  postgresqueries.ReadBuiltInExternalIntegrationSourceTokenAfterResetRow{SourceRefID: "extsrc_builtin_test", ID: "exttok_builtin_test", Name: "Built-in token", TokenPrefix: "juis_new", TokenSuffix: "87654321", Status: "active", ScopesJson: "[]", CreatedAt: pgtype.Timestamptz{Time: now.Add(-time.Hour), Valid: true}, UpdatedAt: pgtype.Timestamptz{Time: now, Valid: true}},
	}
}

type builtInResetQueriesStub struct {
	calls         []string
	sourceLock    postgresqueries.JuheBusinessExternalIntegrationSource
	sourceLockErr error
	tokenLock     postgresqueries.LockBuiltInExternalIntegrationSourceTokenForResetRow
	tokenLockErr  error
	resetArg      postgresqueries.ResetBuiltInExternalIntegrationSourceTokenParams
	resetRows     int64
	resetErr      error
	touchRows     int64
	touchErr      error
	sourceRead    postgresqueries.JuheBusinessExternalIntegrationSource
	sourceReadErr error
	tokenRead     postgresqueries.ReadBuiltInExternalIntegrationSourceTokenAfterResetRow
	tokenReadErr  error
}

func (q *builtInResetQueriesStub) LockBuiltInExternalIntegrationSourceForReset(context.Context) (postgresqueries.JuheBusinessExternalIntegrationSource, error) {
	q.calls = append(q.calls, "source-lock")
	return q.sourceLock, q.sourceLockErr
}
func (q *builtInResetQueriesStub) LockBuiltInExternalIntegrationSourceTokenForReset(context.Context) (postgresqueries.LockBuiltInExternalIntegrationSourceTokenForResetRow, error) {
	q.calls = append(q.calls, "token-lock")
	return q.tokenLock, q.tokenLockErr
}
func (q *builtInResetQueriesStub) ResetBuiltInExternalIntegrationSourceToken(_ context.Context, arg postgresqueries.ResetBuiltInExternalIntegrationSourceTokenParams) (int64, error) {
	q.calls = append(q.calls, "token-update")
	q.resetArg = arg
	if q.resetRows == 0 && q.resetErr == nil {
		return 1, nil
	}
	return q.resetRows, q.resetErr
}
func (q *builtInResetQueriesStub) TouchBuiltInExternalIntegrationSource(_ context.Context, _ pgtype.Timestamptz) (int64, error) {
	q.calls = append(q.calls, "source-touch")
	if q.touchRows == 0 && q.touchErr == nil {
		return 1, nil
	}
	return q.touchRows, q.touchErr
}
func (q *builtInResetQueriesStub) ReadBuiltInExternalIntegrationSourceAfterReset(context.Context) (postgresqueries.JuheBusinessExternalIntegrationSource, error) {
	q.calls = append(q.calls, "source-read")
	return q.sourceRead, q.sourceReadErr
}
func (q *builtInResetQueriesStub) ReadBuiltInExternalIntegrationSourceTokenAfterReset(context.Context) (postgresqueries.ReadBuiltInExternalIntegrationSourceTokenAfterResetRow, error) {
	q.calls = append(q.calls, "token-read")
	return q.tokenRead, q.tokenReadErr
}

type builtInResetTxStub struct {
	pgx.Tx
	commitErr                  error
	commitCalls, rollbackCalls int
	rollbackContextErr         error
}

func (t *builtInResetTxStub) Commit(context.Context) error { t.commitCalls++; return t.commitErr }
func (t *builtInResetTxStub) Rollback(ctx context.Context) error {
	t.rollbackCalls++
	t.rollbackContextErr = ctx.Err()
	return nil
}

var _ managementExternalIntegrationSourceBuiltInResetQueries = (*builtInResetQueriesStub)(nil)
var _ pgx.Tx = (*builtInResetTxStub)(nil)

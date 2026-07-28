package postgres

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"juhe-ai/backend-go/internal/store/port"
)

func TestOAuthCredentialRefreshSQLKeepsCASDispatchFamilyAndOutboxAtomic(t *testing.T) {
	if lockOAuthCredentialRefreshAccountsTableSQL != "LOCK TABLE juhe_business.accounts IN SHARE ROW EXCLUSIVE MODE NOWAIT" {
		t.Fatalf("accounts table lock SQL = %q", lockOAuthCredentialRefreshAccountsTableSQL)
	}
	for _, fragment := range []string{
		"accounts.id = $1::text",
		"authorization_instance_source_account_id = $1::text",
		"ORDER BY accounts.id ASC",
		"FOR UPDATE OF accounts",
	} {
		if !strings.Contains(lockOAuthCredentialRefreshFamilySQL, fragment) {
			t.Fatalf("family-first lock SQL missing %q", fragment)
		}
	}
	for _, fragment := range []string{
		"credentials_encrypted = $1::text",
		"credential_fingerprint = $2::text",
		"credential_mask = $3::text",
		"oauth_access_token_expires_at = $4::text",
		"oauth_refresh_token_present = CASE WHEN $5::boolean THEN 1 ELSE 0 END",
		"config_revision = config_revision + 1",
		"id = $7::text",
		"system_account_id = $8::text",
		"config_revision = $9::integer",
		"type = $10::text",
		"deleted_at IS NULL",
		"authorization_instance_source_account_id IS NULL",
		"authorization_instance_authorization_id IS NULL",
		"RETURNING config_revision, dispatch_revision",
	} {
		if !strings.Contains(compareAndSwapOAuthCredentialsSQL, fragment) {
			t.Fatalf("credential CAS SQL missing %q", fragment)
		}
	}
	for _, sql := range []string{advanceOAuthCredentialRefreshSourceDispatchSQL, advanceOAuthCredentialRefreshAuthorizedFamilySQL} {
		for _, fragment := range []string{
			"dispatch_revision =",
			"INSERT INTO juhe_business.account_circuit_outbox",
			"'dispatch_revision_changed'",
			"'pending'",
			"attempt_count",
		} {
			if !strings.Contains(sql, fragment) {
				t.Fatalf("dispatch SQL missing %q", fragment)
			}
		}
	}
	for _, fragment := range []string{
		"authorization_instance_source_account_id = $1::text",
		"ORDER BY accounts.id ASC",
		"FOR UPDATE OF accounts",
		"accounts.dispatch_revision = family.dispatch_revision",
	} {
		if !strings.Contains(advanceOAuthCredentialRefreshAuthorizedFamilySQL, fragment) {
			t.Fatalf("authorized family SQL missing %q", fragment)
		}
	}
}

func TestCompareAndSwapOAuthCredentialsCommitsCredentialOnlyRefresh(t *testing.T) {
	tx := &oauthCredentialRefreshTxStub{rows: []pgx.Row{
		oauthCredentialRefreshRow{values: []any{int64(3)}},
		oauthCredentialRefreshRow{values: []any{8, int64(12)}},
	}}
	input := validOAuthCredentialRefreshInput()

	result, applied, err := compareAndSwapOAuthCredentialsInTx(
		context.Background(),
		func(context.Context, pgx.TxOptions) (oauthCredentialRefreshTx, error) { return tx, nil },
		func() oauthCredentialRefreshIDs {
			t.Fatal("dispatch IDs must not be allocated")
			return oauthCredentialRefreshIDs{}
		},
		input,
	)
	if err != nil || !applied {
		t.Fatalf("result=%+v applied=%t error=%v", result, applied, err)
	}
	if result.ConfigRevision != 8 || result.DispatchRevision != 12 || tx.commitCalls != 1 || tx.rollbackCalls != 0 || len(tx.queries) != 3 {
		t.Fatalf("result=%+v commit=%d rollback=%d queries=%d", result, tx.commitCalls, tx.rollbackCalls, len(tx.queries))
	}
	if tx.queries[0] != lockOAuthCredentialRefreshAccountsTableSQL || tx.queries[1] != lockOAuthCredentialRefreshFamilySQL || tx.queries[2] != compareAndSwapOAuthCredentialsSQL {
		t.Fatalf("query order=%#v", tx.queries)
	}
	if got := tx.args[2][3]; got != "2026-07-28T02:30:00.123Z" {
		t.Fatalf("expiry arg=%#v", got)
	}
}

func TestCompareAndSwapOAuthCredentialsAdvancesSourceAndAuthorizedFamily(t *testing.T) {
	tx := &oauthCredentialRefreshTxStub{rows: []pgx.Row{
		oauthCredentialRefreshRow{values: []any{int64(3)}},
		oauthCredentialRefreshRow{values: []any{8, int64(12)}},
		oauthCredentialRefreshRow{values: []any{int64(13), int64(1), int64(1)}},
		oauthCredentialRefreshRow{values: []any{int64(2), int64(2), int64(2)}},
	}}
	input := validOAuthCredentialRefreshInput()
	input.CircuitOwnerConfigurationChanged = true
	ids := oauthCredentialRefreshIDs{transitionID: "oauth-refresh:transition", eventID: "event-1", familySeed: "family:"}

	result, applied, err := compareAndSwapOAuthCredentialsInTx(
		context.Background(),
		func(context.Context, pgx.TxOptions) (oauthCredentialRefreshTx, error) { return tx, nil },
		func() oauthCredentialRefreshIDs { return ids },
		input,
	)
	if err != nil || !applied {
		t.Fatalf("result=%+v applied=%t error=%v", result, applied, err)
	}
	if result != (port.OAuthCredentialRefreshCASResult{ConfigRevision: 8, DispatchRevision: 13}) {
		t.Fatalf("result=%+v", result)
	}
	if tx.commitCalls != 1 || tx.rollbackCalls != 0 || len(tx.queries) != 5 {
		t.Fatalf("commit=%d rollback=%d queries=%d", tx.commitCalls, tx.rollbackCalls, len(tx.queries))
	}
	if tx.queries[0] != lockOAuthCredentialRefreshAccountsTableSQL || tx.queries[1] != lockOAuthCredentialRefreshFamilySQL || tx.queries[2] != compareAndSwapOAuthCredentialsSQL || tx.queries[3] != advanceOAuthCredentialRefreshSourceDispatchSQL || tx.queries[4] != advanceOAuthCredentialRefreshAuthorizedFamilySQL {
		t.Fatalf("query order=%#v", tx.queries)
	}
	if got := tx.args[3][4]; got != port.GatewayAccountCircuitProjectionKey {
		t.Fatalf("source projection key=%#v", got)
	}
	if len(tx.args[4]) != 6 {
		t.Fatalf("family args=%#v", tx.args[4])
	}
}

func TestCompareAndSwapOAuthCredentialsReturnsStaleWithoutDispatch(t *testing.T) {
	tx := &oauthCredentialRefreshTxStub{rows: []pgx.Row{
		oauthCredentialRefreshRow{values: []any{int64(3)}},
		oauthCredentialRefreshRow{err: pgx.ErrNoRows},
	}}

	result, applied, err := compareAndSwapOAuthCredentialsInTx(
		context.Background(),
		func(context.Context, pgx.TxOptions) (oauthCredentialRefreshTx, error) { return tx, nil },
		func() oauthCredentialRefreshIDs {
			t.Fatal("dispatch IDs must not be allocated")
			return oauthCredentialRefreshIDs{}
		},
		validOAuthCredentialRefreshInput(),
	)
	if err != nil || applied || result != (port.OAuthCredentialRefreshCASResult{}) {
		t.Fatalf("result=%+v applied=%t error=%v", result, applied, err)
	}
	if tx.commitCalls != 0 || tx.rollbackCalls != 1 || len(tx.queries) != 3 {
		t.Fatalf("commit=%d rollback=%d queries=%d", tx.commitCalls, tx.rollbackCalls, len(tx.queries))
	}
}

func TestCompareAndSwapOAuthCredentialsFailsBeforeFamilyReadWhenTableBusy(t *testing.T) {
	tx := &oauthCredentialRefreshTxStub{execErr: fmt.Errorf("55P03 lock unavailable")}

	_, applied, err := compareAndSwapOAuthCredentialsInTx(
		context.Background(),
		func(context.Context, pgx.TxOptions) (oauthCredentialRefreshTx, error) { return tx, nil },
		newOAuthCredentialRefreshIDs,
		validOAuthCredentialRefreshInput(),
	)
	if err == nil || applied || !strings.Contains(err.Error(), "lock OAuth credential refresh accounts table") {
		t.Fatalf("applied=%t error=%v", applied, err)
	}
	if len(tx.queries) != 1 || tx.queries[0] != lockOAuthCredentialRefreshAccountsTableSQL || tx.rollbackCalls != 1 {
		t.Fatalf("queries=%v rollback=%d", tx.queries, tx.rollbackCalls)
	}
}

func TestCompareAndSwapOAuthCredentialsRollsBackFamilyMismatch(t *testing.T) {
	tx := &oauthCredentialRefreshTxStub{rows: []pgx.Row{
		oauthCredentialRefreshRow{values: []any{int64(3)}},
		oauthCredentialRefreshRow{values: []any{8, int64(12)}},
		oauthCredentialRefreshRow{values: []any{int64(13), int64(1), int64(1)}},
		oauthCredentialRefreshRow{values: []any{int64(2), int64(1), int64(1)}},
	}}
	input := validOAuthCredentialRefreshInput()
	input.CircuitOwnerConfigurationChanged = true

	_, applied, err := compareAndSwapOAuthCredentialsInTx(
		context.Background(),
		func(context.Context, pgx.TxOptions) (oauthCredentialRefreshTx, error) { return tx, nil },
		func() oauthCredentialRefreshIDs {
			return oauthCredentialRefreshIDs{transitionID: "oauth-refresh:transition", eventID: "event-1", familySeed: "family:"}
		},
		input,
	)
	if err == nil || applied || !strings.Contains(err.Error(), "family count mismatch") {
		t.Fatalf("applied=%t error=%v", applied, err)
	}
	if tx.commitCalls != 0 || tx.rollbackCalls != 1 {
		t.Fatalf("commit=%d rollback=%d", tx.commitCalls, tx.rollbackCalls)
	}
}

func TestCompareAndSwapOAuthCredentialsRejectsInvalidInputBeforeBegin(t *testing.T) {
	input := validOAuthCredentialRefreshInput()
	input.Secrets = port.NewOAuthCredentialRefreshSecrets(" secret ", "fingerprint", "mask")
	beginCalls := 0
	_, applied, err := compareAndSwapOAuthCredentialsInTx(
		context.Background(),
		func(context.Context, pgx.TxOptions) (oauthCredentialRefreshTx, error) { beginCalls++; return nil, nil },
		newOAuthCredentialRefreshIDs,
		input,
	)
	if err == nil || applied || beginCalls != 0 {
		t.Fatalf("applied=%t beginCalls=%d error=%v", applied, beginCalls, err)
	}
}

func validOAuthCredentialRefreshInput() port.OAuthCredentialRefreshCASInput {
	expiresAt := time.Date(2026, 7, 28, 10, 30, 0, 123000000, time.FixedZone("test", 8*60*60))
	return port.OAuthCredentialRefreshCASInput{
		AccountID:              "acct-1",
		SystemAccountID:        "system-1",
		ExpectedAccountType:    "google_oauth",
		ExpectedConfigRevision: 7,
		Secrets:                port.NewOAuthCredentialRefreshSecrets("encrypted", "fingerprint", "acce...oken"),
		AccessTokenExpiresAt:   &expiresAt,
		RefreshTokenPresent:    true,
		UpdatedAt:              time.Date(2026, 7, 28, 8, 0, 0, 0, time.UTC),
	}
}

type oauthCredentialRefreshTxStub struct {
	rows          []pgx.Row
	queries       []string
	args          [][]any
	commitCalls   int
	rollbackCalls int
	commitErr     error
	rollbackErr   error
	execErr       error
}

func (tx *oauthCredentialRefreshTxStub) Exec(_ context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	tx.queries = append(tx.queries, query)
	tx.args = append(tx.args, append([]any(nil), args...))
	return pgconn.NewCommandTag("LOCK TABLE"), tx.execErr
}

func (tx *oauthCredentialRefreshTxStub) QueryRow(_ context.Context, query string, args ...any) pgx.Row {
	tx.queries = append(tx.queries, query)
	tx.args = append(tx.args, append([]any(nil), args...))
	if len(tx.rows) == 0 {
		return oauthCredentialRefreshRow{err: fmt.Errorf("unexpected query")}
	}
	row := tx.rows[0]
	tx.rows = tx.rows[1:]
	return row
}

func (tx *oauthCredentialRefreshTxStub) Commit(context.Context) error {
	tx.commitCalls++
	return tx.commitErr
}

func (tx *oauthCredentialRefreshTxStub) Rollback(context.Context) error {
	tx.rollbackCalls++
	return tx.rollbackErr
}

type oauthCredentialRefreshRow struct {
	values []any
	err    error
}

func (row oauthCredentialRefreshRow) Scan(dest ...any) error {
	if row.err != nil {
		return row.err
	}
	if len(dest) != len(row.values) {
		return fmt.Errorf("scan destinations=%d values=%d", len(dest), len(row.values))
	}
	for index, value := range row.values {
		switch target := dest[index].(type) {
		case *int:
			*target = value.(int)
		case *int64:
			*target = value.(int64)
		default:
			return fmt.Errorf("unsupported scan destination %T", dest[index])
		}
	}
	return nil
}

var _ oauthCredentialRefreshTx = (*oauthCredentialRefreshTxStub)(nil)

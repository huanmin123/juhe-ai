package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"juhe-ai/backend-go/internal/store/port"
)

const oauthCredentialRefreshISOTimeLayout = "2006-01-02T15:04:05.000Z"

type oauthCredentialRefreshTx interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
	Commit(context.Context) error
	Rollback(context.Context) error
}

type beginOAuthCredentialRefreshTx func(context.Context, pgx.TxOptions) (oauthCredentialRefreshTx, error)

type oauthCredentialRefreshIDs struct {
	transitionID string
	eventID      string
	familySeed   string
}

func (s *Store) CompareAndSwapOAuthCredentials(
	ctx context.Context,
	input port.OAuthCredentialRefreshCASInput,
) (port.OAuthCredentialRefreshCASResult, bool, error) {
	return compareAndSwapOAuthCredentialsInTx(ctx, func(ctx context.Context, options pgx.TxOptions) (oauthCredentialRefreshTx, error) {
		return s.pool.BeginTx(ctx, options)
	}, newOAuthCredentialRefreshIDs, input)
}

func compareAndSwapOAuthCredentialsInTx(
	ctx context.Context,
	beginTx beginOAuthCredentialRefreshTx,
	newIDs func() oauthCredentialRefreshIDs,
	input port.OAuthCredentialRefreshCASInput,
) (port.OAuthCredentialRefreshCASResult, bool, error) {
	if err := validateOAuthCredentialRefreshCASInput(input); err != nil {
		return port.OAuthCredentialRefreshCASResult{}, false, err
	}
	if beginTx == nil || newIDs == nil {
		return port.OAuthCredentialRefreshCASResult{}, false, fmt.Errorf("OAuth credential refresh transaction dependencies are required")
	}

	tx, err := beginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return port.OAuthCredentialRefreshCASResult{}, false, fmt.Errorf("begin OAuth credential refresh tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = tx.Rollback(rollbackCtx)
		}
	}()
	if _, err := tx.Exec(ctx, lockOAuthCredentialRefreshAccountsTableSQL); err != nil {
		return port.OAuthCredentialRefreshCASResult{}, false, fmt.Errorf("lock OAuth credential refresh accounts table: %w", err)
	}

	var lockedCount int64
	if err := tx.QueryRow(ctx, lockOAuthCredentialRefreshFamilySQL, input.AccountID).Scan(&lockedCount); err != nil {
		return port.OAuthCredentialRefreshCASResult{}, false, fmt.Errorf("lock OAuth credential refresh account family: %w", err)
	}

	var expiresAt any
	if input.AccessTokenExpiresAt != nil {
		expiresAt = input.AccessTokenExpiresAt.UTC().Format(oauthCredentialRefreshISOTimeLayout)
	}
	var fingerprint any
	if strings.TrimSpace(input.Secrets.CredentialFingerprint()) != "" {
		fingerprint = input.Secrets.CredentialFingerprint()
	}

	var result port.OAuthCredentialRefreshCASResult
	err = tx.QueryRow(ctx, compareAndSwapOAuthCredentialsSQL,
		input.Secrets.CredentialsEncrypted(),
		fingerprint,
		input.Secrets.CredentialMask(),
		expiresAt,
		input.RefreshTokenPresent,
		input.UpdatedAt.UTC(),
		input.AccountID,
		input.SystemAccountID,
		input.ExpectedConfigRevision,
		input.ExpectedAccountType,
	).Scan(&result.ConfigRevision, &result.DispatchRevision)
	if errors.Is(err, pgx.ErrNoRows) {
		if rollbackErr := tx.Rollback(ctx); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			return port.OAuthCredentialRefreshCASResult{}, false, fmt.Errorf("rollback stale OAuth credential refresh: %w", rollbackErr)
		}
		committed = true
		return port.OAuthCredentialRefreshCASResult{}, false, nil
	}
	if err != nil {
		return port.OAuthCredentialRefreshCASResult{}, false, fmt.Errorf("compare and swap OAuth credentials: %w", err)
	}

	if input.CircuitOwnerConfigurationChanged {
		ids := newIDs()
		if err := validateOAuthCredentialRefreshIDs(ids); err != nil {
			return port.OAuthCredentialRefreshCASResult{}, false, err
		}
		var updatedCount, insertedCount int64
		if err := tx.QueryRow(ctx, advanceOAuthCredentialRefreshSourceDispatchSQL,
			input.AccountID,
			result.DispatchRevision,
			input.UpdatedAt.UTC(),
			ids.eventID,
			port.GatewayAccountCircuitProjectionKey,
			"dispatch:"+ids.transitionID,
			ids.transitionID,
			input.UpdatedAt.UTC().UnixMilli(),
		).Scan(&result.DispatchRevision, &updatedCount, &insertedCount); err != nil {
			return port.OAuthCredentialRefreshCASResult{}, false, fmt.Errorf("advance OAuth credential refresh source dispatch revision: %w", err)
		}
		if updatedCount != 1 || insertedCount != 1 {
			return port.OAuthCredentialRefreshCASResult{}, false, fmt.Errorf(
				"advance OAuth credential refresh source dispatch count mismatch: updated=%d outbox=%d",
				updatedCount,
				insertedCount,
			)
		}

		var familyCount, familyUpdatedCount, familyInsertedCount int64
		if err := tx.QueryRow(ctx, advanceOAuthCredentialRefreshAuthorizedFamilySQL,
			input.AccountID,
			ids.transitionID,
			ids.familySeed,
			input.UpdatedAt.UTC(),
			input.UpdatedAt.UTC().UnixMilli(),
			port.GatewayAccountCircuitProjectionKey,
		).Scan(&familyCount, &familyUpdatedCount, &familyInsertedCount); err != nil {
			return port.OAuthCredentialRefreshCASResult{}, false, fmt.Errorf("advance OAuth credential refresh authorized family: %w", err)
		}
		if familyUpdatedCount != familyCount || familyInsertedCount != familyCount {
			return port.OAuthCredentialRefreshCASResult{}, false, fmt.Errorf(
				"advance OAuth credential refresh authorized family count mismatch: family=%d updated=%d outbox=%d",
				familyCount,
				familyUpdatedCount,
				familyInsertedCount,
			)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		if errors.Is(err, pgx.ErrTxCommitRollback) {
			return port.OAuthCredentialRefreshCASResult{}, false, fmt.Errorf("commit OAuth credential refresh tx rolled back: %w", err)
		}
		return port.OAuthCredentialRefreshCASResult{}, false, fmt.Errorf("commit OAuth credential refresh tx: %w", err)
	}
	committed = true
	return result, true, nil
}

func validateOAuthCredentialRefreshCASInput(input port.OAuthCredentialRefreshCASInput) error {
	for name, value := range map[string]string{
		"account id":            input.AccountID,
		"system account id":     input.SystemAccountID,
		"credentials encrypted": input.Secrets.CredentialsEncrypted(),
		"credential mask":       input.Secrets.CredentialMask(),
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value || !utf8.ValidString(value) {
			return fmt.Errorf("OAuth credential refresh %s is invalid", name)
		}
	}
	if input.ExpectedConfigRevision < 1 || input.UpdatedAt.IsZero() || input.UpdatedAt.UnixMilli() < 0 {
		return fmt.Errorf("OAuth credential refresh fence is invalid")
	}
	if input.ExpectedAccountType != "oauth" && input.ExpectedAccountType != "google_oauth" {
		return fmt.Errorf("OAuth credential refresh account type is invalid")
	}
	if input.Secrets.CredentialFingerprint() != "" &&
		(strings.TrimSpace(input.Secrets.CredentialFingerprint()) != input.Secrets.CredentialFingerprint() || !utf8.ValidString(input.Secrets.CredentialFingerprint())) {
		return fmt.Errorf("OAuth credential refresh credential fingerprint is invalid")
	}
	if input.AccessTokenExpiresAt != nil && input.AccessTokenExpiresAt.IsZero() {
		return fmt.Errorf("OAuth credential refresh access token expiry is invalid")
	}
	return nil
}

func newOAuthCredentialRefreshIDs() oauthCredentialRefreshIDs {
	return oauthCredentialRefreshIDs{
		transitionID: "oauth-refresh:" + uuid.NewString(),
		eventID:      uuid.NewString(),
		familySeed:   uuid.NewString() + ":",
	}
}

func validateOAuthCredentialRefreshIDs(ids oauthCredentialRefreshIDs) error {
	if strings.TrimSpace(ids.transitionID) == "" || len(ids.transitionID) > 180 ||
		strings.TrimSpace(ids.eventID) == "" || len(ids.eventID) > 256 ||
		strings.TrimSpace(ids.familySeed) == "" || len(ids.familySeed) > 200 {
		return fmt.Errorf("OAuth credential refresh dispatch identity is invalid")
	}
	return nil
}

const compareAndSwapOAuthCredentialsSQL = `
UPDATE juhe_business.accounts
SET credentials_encrypted = $1::text,
    credential_fingerprint = $2::text,
    credential_mask = $3::text,
    oauth_access_token_expires_at = $4::text,
    oauth_refresh_token_present = CASE WHEN $5::boolean THEN 1 ELSE 0 END,
    config_revision = config_revision + 1,
    updated_at = $6::timestamptz
WHERE id = $7::text
  AND system_account_id = $8::text
  AND config_revision = $9::integer
  AND type = $10::text
  AND deleted_at IS NULL
  AND authorization_instance_source_account_id IS NULL
  AND authorization_instance_authorization_id IS NULL
RETURNING config_revision, dispatch_revision`

// Refresh is rare and crosses Node/Go ownership during migration. A fail-fast
// table lock prevents a new/restored authorization instance from appearing
// between the ordered family prelock and dispatch propagation, and also avoids
// source-first Node writers reintroducing the opposite row-lock order.
const lockOAuthCredentialRefreshAccountsTableSQL = `LOCK TABLE juhe_business.accounts IN SHARE ROW EXCLUSIVE MODE NOWAIT`

const lockOAuthCredentialRefreshFamilySQL = `
WITH locked AS MATERIALIZED (
  SELECT accounts.id
  FROM juhe_business.accounts AS accounts
  WHERE accounts.id = $1::text
     OR (
       accounts.authorization_instance_source_account_id = $1::text
       AND accounts.deleted_at IS NULL
     )
  ORDER BY accounts.id ASC
  FOR UPDATE OF accounts
)
SELECT count(*) FROM locked`

const advanceOAuthCredentialRefreshSourceDispatchSQL = `
WITH updated AS (
  UPDATE juhe_business.accounts
  SET dispatch_revision = dispatch_revision + 1,
      updated_at = $3::timestamptz
  WHERE id = $1::text
    AND dispatch_revision = $2::bigint
    AND deleted_at IS NULL
    AND authorization_instance_source_account_id IS NULL
    AND authorization_instance_authorization_id IS NULL
  RETURNING id, dispatch_revision
), inserted AS (
  INSERT INTO juhe_business.account_circuit_outbox (
    event_id, projection_key, dedupe_key, event_type, account_id,
    account_runtime_key, transition_id, dispatch_revision, status,
    available_at_ms, attempt_count, created_at_ms, updated_at_ms
  )
  SELECT
    $4::text, $5::text, $6::text, 'dispatch_revision_changed', updated.id,
    updated.id, $7::text, updated.dispatch_revision, 'pending',
    $8::bigint, 0, $8::bigint, $8::bigint
  FROM updated
  RETURNING account_id
)
SELECT
  COALESCE((SELECT dispatch_revision FROM updated), 0),
  (SELECT count(*) FROM updated),
  (SELECT count(*) FROM inserted)`

const advanceOAuthCredentialRefreshAuthorizedFamilySQL = `
WITH family AS MATERIALIZED (
  SELECT accounts.id, accounts.dispatch_revision
  FROM juhe_business.accounts AS accounts
  WHERE accounts.authorization_instance_source_account_id = $1::text
    AND accounts.deleted_at IS NULL
  ORDER BY accounts.id ASC
  FOR UPDATE OF accounts
), updated AS (
  UPDATE juhe_business.accounts AS accounts
  SET dispatch_revision = accounts.dispatch_revision + 1,
      updated_at = $4::timestamptz
  FROM family
  WHERE accounts.id = family.id
    AND accounts.dispatch_revision = family.dispatch_revision
    AND accounts.deleted_at IS NULL
  RETURNING accounts.id, accounts.dispatch_revision
), inserted AS (
  INSERT INTO juhe_business.account_circuit_outbox (
    event_id, projection_key, dedupe_key, event_type, account_id,
    account_runtime_key, transition_id, dispatch_revision, status,
    available_at_ms, attempt_count, created_at_ms, updated_at_ms
  )
  SELECT
    $3::text || md5(updated.id),
    $6::text,
    'dispatch:' || left($2::text || ':authorized:' || md5(updated.id), 230),
    'dispatch_revision_changed',
    updated.id,
    updated.id,
    left($2::text || ':authorized:' || md5(updated.id), 230),
    updated.dispatch_revision,
    'pending',
    $5::bigint,
    0,
    $5::bigint,
    $5::bigint
  FROM updated
  RETURNING account_id
)
SELECT
  (SELECT count(*) FROM family),
  (SELECT count(*) FROM updated),
  (SELECT count(*) FROM inserted)`

var _ port.OAuthCredentialRefreshStore = (*Store)(nil)

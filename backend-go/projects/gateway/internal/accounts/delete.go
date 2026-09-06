package accounts

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// Delete mirrors deleteAccountWithRelatedCleanupAsync (owner mode): the
// scope-checked account is soft deleted (status='disabled', schedulable=0,
// cooldown cleared, deleted_at/deleted_by stamped) together with its
// authorization instances, tag bindings and name search terms. Authorization
// instances must go through the return flow instead. Before the soft delete
// the account's resource-authorization chain (grants, sources, authorizations
// and the quota scope bindings they produced) is revoked inside the same
// transaction (Node revokeAccountAuthorizationsForDeletedResource), and the
// health-input tombstone outbox rows (kind='tombstone',
// reason='account_deleted') are enqueued for the health-capable deleted
// accounts (Node logicallyDeleteAccounts →
// reserveAndEnqueueAccountHealthJobsInputInTransaction). Returns (false, nil)
// when the account is missing or outside the access scope.
func (s *Store) Delete(ctx context.Context, accountID string, access AccessScope) (bool, error) {
	ctx = ensureCtx(ctx)
	id := strings.TrimSpace(accountID)
	if id == "" {
		return false, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	scoped := access.manageableID()
	scopeClause := ""
	args := []any{id}
	if scoped != "" {
		scopeClause = " AND accounts.system_account_id = ?"
		args = append(args, scoped)
	}
	var rowID, systemAccountID string
	var authorizationID sql.NullString
	err = tx.QueryRowContext(ctx, s.bind(`SELECT id, system_account_id, authorization_instance_authorization_id
		FROM `+s.table("accounts")+`
		WHERE id = ?
			AND deleted_at IS NULL`+scopeClause), args...).
		Scan(&rowID, &systemAccountID, &authorizationID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if authorizationID.Valid && authorizationID.String != "" {
		return false, &ValidationError{Message: "授权账户请使用归还操作"}
	}

	actor := access.ViewerID
	deletedAt := isoMillis(s.now())
	// Node deletes the resource-owner authorization chain first, inside the
	// same transaction (account-delete-cleanup.repository.ts:145-148).
	if err := s.revokeAccountAuthorizationsForDeletedResource(ctx, tx, rowID, actor, deletedAt); err != nil {
		return false, err
	}
	instanceRows, err := tx.QueryContext(ctx, s.bind(`SELECT id
		FROM `+s.table("accounts")+`
		WHERE authorization_instance_source_account_id = ?
			AND deleted_at IS NULL
		ORDER BY created_at ASC, id ASC`), rowID)
	if err != nil {
		return false, err
	}
	accountIDs := []string{rowID}
	for instanceRows.Next() {
		var instanceID string
		if err := instanceRows.Scan(&instanceID); err != nil {
			instanceRows.Close()
			return false, err
		}
		accountIDs = append(accountIDs, instanceID)
	}
	instanceRows.Close()
	if err := instanceRows.Err(); err != nil {
		return false, err
	}

	for _, account := range accountIDs {
		result, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("accounts")+`
			SET status = 'disabled',
				schedulable = 0,
				cooldown_until = NULL,
				deleted_at = ?,
				deleted_by = ?,
				updated_at = ?
			WHERE deleted_at IS NULL AND id = ?`), deletedAt, actor, deletedAt, account)
		if err != nil {
			return false, err
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return false, nil
		}
	}
	// Health-input tombstones ride the same transaction for the health-capable
	// providers/types (account-delete-cleanup.repository.ts:259-275).
	if err := s.enqueueDeletedAccountHealthTombstones(ctx, tx, accountIDs, deletedAt); err != nil {
		return false, err
	}
	for _, account := range accountIDs {
		if _, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("account_tag_bindings")+` WHERE account_id = ?`), account); err != nil {
			return false, err
		}
	}
	s.deleteAccountNameSearchTermsForAccounts(ctx, tx, accountIDs)
	if err := tx.Commit(); err != nil {
		return false, err
	}
	// Post-commit invalidation (T2 audit; Node
	// account-delete-cleanup.repository.ts:197-201): one lookup flush per
	// deleted account plus one whole-surface runtime invalidation,
	// best-effort.
	s.finishDeleteSideEffects(ctx, accountIDs)
	return true, nil
}

// revokeAccountAuthorizationsForDeletedResource mirrors
// revokeAccountAuthorizationsForDeletedResource(Async)
// (account-delete-cleanup.repository.ts:420-492): the authorization rows of
// the deleted account resource (resource_type='account') lose their quota
// scope bindings first, then grants, sources and authorizations flip to the
// 'revoked' terminal state inside the caller's transaction.
//
// The Node dialects do NOT land identically here. The PG async arm
// bulk-writes this exact shape: grants 'revoked' (COALESCE-preserving),
// sources 'revoked' with ended_reason='account_deleted' (active/superseded),
// authorizations 'revoked' unconditionally with the effective source nulled
// and revoked_reason='account_deleted', quota scope bindings deleted
// outright. The SQLite sync arm instead walks the account's grants one by
// one through the resource-authorization runtime-sync domain
// (revokeResourceAuthorizationGrant → syncResourceAuthorizationGrantRuntime):
// manual-source-scoped updates with ended_reason='authorization_revoked', a
// conditional terminal refresh that can leave the runtime authorization
// alive while another source stands, per-grant quota re-sync and health
// fanout. Both arms still terminate at 'revoked' — the 'returned' states in
// the archived file belong to revokeAuthorizationInstanceForDeletedAccount,
// which no archived delete flow calls. This store mirrors the PG async arm;
// the SQLite per-grant runtime sync domain is a resource-authorization
// migration slice of its own and stays untouched here.
func (s *Store) revokeAccountAuthorizationsForDeletedResource(ctx context.Context, tx *sql.Tx, accountID, actor, deletedAt string) error {
	var authorizationIDs []string
	authRows, err := tx.QueryContext(ctx, s.bind(`SELECT id
		FROM `+s.table("resource_authorizations")+`
		WHERE resource_type = 'account'
			AND resource_id = ?
			AND status <> 'returned'`), accountID)
	if err != nil {
		return err
	}
	for authRows.Next() {
		var authID string
		if err := authRows.Scan(&authID); err != nil {
			authRows.Close()
			return err
		}
		if strings.TrimSpace(authID) != "" {
			authorizationIDs = append(authorizationIDs, authID)
		}
	}
	authRows.Close()
	if err := authRows.Err(); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("request_quota_hourly_window_scope_bindings")+`
		WHERE source_type = 'resource_authorization_grant'
			AND source_id IN (
				SELECT id FROM `+s.table("resource_authorization_grants")+`
				WHERE resource_type = 'account' AND resource_id = ?
			)`), accountID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_grants")+`
		SET status = 'revoked',
			revoked_by = COALESCE(revoked_by, ?),
			revoked_at = COALESCE(revoked_at, ?),
			updated_at = ?
		WHERE resource_type = 'account'
			AND resource_id = ?
			AND status NOT IN ('revoked', 'returned')`), actor, deletedAt, deletedAt, accountID); err != nil {
		return err
	}
	if len(authorizationIDs) == 0 {
		return nil
	}
	idsClause := strings.TrimRight(strings.Repeat("?, ", len(authorizationIDs)), ", ")
	authArgs := stringSliceToAny(authorizationIDs)
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_sources")+`
		SET status = 'revoked',
			ended_at = COALESCE(ended_at, ?),
			ended_reason = COALESCE(ended_reason, 'account_deleted'),
			revoked_by = ?,
			revoked_at = ?,
			updated_at = ?
		WHERE authorization_id IN (`+idsClause+`)
			AND status IN ('active', 'superseded')`),
		append([]any{deletedAt, actor, deletedAt, deletedAt}, authArgs...)...); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorizations")+`
		SET status = 'revoked',
			effective_source_type = NULL,
			effective_source_team_id = NULL,
			revoked_by = COALESCE(revoked_by, ?),
			revoked_at = COALESCE(revoked_at, ?),
			revoked_reason = COALESCE(revoked_reason, 'account_deleted'),
			last_source_changed_at = ?,
			updated_at = ?
		WHERE id IN (`+idsClause+`)
			AND status <> 'returned'`),
		append([]any{actor, deletedAt, deletedAt, deletedAt}, authArgs...)...)
	return err
}

// accountHealthTombstoneProviderCodes / Types mirror the health-capable
// filter of logicallyDeleteAccounts (provider_code IN (...) AND type IN (...)).
var (
	accountHealthTombstoneProviderCodes = []string{"gpt", "openai", "xai", "anthropic", "deepseek", "glm", "gemini", "hybrid"}
	accountHealthTombstoneAccountTypes  = []string{"api_key", "oauth", "google_oauth"}
)

// enqueueDeletedAccountHealthTombstones mirrors the tombstone arm of
// logicallyDeleteAccounts(Async): for the just-deleted health-capable
// accounts, reserve the next health-input epoch and write the
// kind='tombstone', reason='account_deleted' outbox row in the caller's
// transaction, so the health projection consumes the version-fenced removal.
func (s *Store) enqueueDeletedAccountHealthTombstones(ctx context.Context, tx *sql.Tx, accountIDs []string, deletedAt string) error {
	idsClause := strings.TrimRight(strings.Repeat("?, ", len(accountIDs)), ", ")
	providerClause := strings.TrimRight(strings.Repeat("?, ", len(accountHealthTombstoneProviderCodes)), ", ")
	typeClause := strings.TrimRight(strings.Repeat("?, ", len(accountHealthTombstoneAccountTypes)), ", ")
	lockSuffix := ""
	if s.pg {
		lockSuffix = " FOR UPDATE"
	}
	args := []any{deletedAt}
	args = append(args, stringSliceToAny(accountIDs)...)
	args = append(args, stringSliceToAny(accountHealthTombstoneProviderCodes)...)
	args = append(args, stringSliceToAny(accountHealthTombstoneAccountTypes)...)
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT id, config_revision, dispatch_revision
		FROM `+s.table("accounts")+`
		WHERE deleted_at = ?
			AND id IN (`+idsClause+`)
			AND provider_code IN (`+providerClause+`)
			AND type IN (`+typeClause+`)
		ORDER BY id ASC`+lockSuffix), args...)
	if err != nil {
		return err
	}
	type tombstoneRow struct {
		accountID        string
		configRevision   int64
		dispatchRevision int64
	}
	tombstones := []tombstoneRow{}
	for rows.Next() {
		var row tombstoneRow
		if err := rows.Scan(&row.accountID, &row.configRevision, &row.dispatchRevision); err != nil {
			rows.Close()
			return err
		}
		tombstones = append(tombstones, row)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, tombstone := range tombstones {
		if err := s.reserveAndEnqueueAccountHealthTombstone(ctx, tx, tombstone.accountID,
			tombstone.configRevision, tombstone.dispatchRevision, deletedAt); err != nil {
			return err
		}
	}
	return nil
}

// reserveAndEnqueueAccountHealthTombstone mirrors
// reserveAndEnqueueAccountHealthJobsInputInTransaction for the delete path:
// bump account_health_jobs_input_versions (or seed it at 1), then insert the
// pending tombstone intent keyed by the reserved version.
func (s *Store) reserveAndEnqueueAccountHealthTombstone(ctx context.Context, tx *sql.Tx, accountID string, configRevision, dispatchRevision int64, now string) error {
	normalized := strings.TrimSpace(accountID)
	if normalized == "" {
		return errors.New("J1 snapshot version 缺少 account ID")
	}
	var currentVersion sql.NullInt64
	err := tx.QueryRowContext(ctx, s.bind(`SELECT current_version FROM `+s.table("account_health_jobs_input_versions")+`
		WHERE account_id = ?`), normalized).Scan(&currentVersion)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	nextVersion := int64(1)
	if err == nil && currentVersion.Valid {
		nextVersion = currentVersion.Int64 + 1
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("account_health_jobs_input_versions")+`
			SET current_version = ?, reserved_at = ? WHERE account_id = ?`), nextVersion, now, normalized); err != nil {
			return err
		}
	} else {
		if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("account_health_jobs_input_versions")+`
			(account_id, current_version, reserved_at) VALUES (?, ?, ?)`), normalized, nextVersion, now); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("account_health_jobs_input_outbox")+`
		(event_id, account_id, input_version, event_kind, reason,
		 config_revision, dispatch_revision, status, available_at,
		 created_at, updated_at)
		VALUES (?, ?, ?, 'tombstone', 'account_deleted', ?, ?, 'pending', ?, ?, ?)`),
		s.newI("acchev"), normalized, nextVersion, configRevision, dispatchRevision, now, now, now)
	return err
}

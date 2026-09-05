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
// instances must go through the return flow instead. Returns (false, nil)
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

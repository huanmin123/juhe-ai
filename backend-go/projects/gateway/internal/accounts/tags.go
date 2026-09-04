package accounts

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// Tag endpoints mirror storage/account-tags.repository.ts +
// modules/accounts/account-tags.routes.ts.

// AccountTagSummary mirrors AccountTagSummary with the tag-list projection.
type AccountTagSummary struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	AccountCount int    `json:"accountCount"`
	CreatedAt    string `json:"createdAt"`
	UpdatedAt    string `json:"updatedAt"`
}

// TagInUseError mirrors AccountTagInUseError.
type TagInUseError struct{}

func (e *TagInUseError) Error() string { return tagInUseMessage }

// tagOwnerSystemAccountID mirrors accountTagOwnerSystemAccountId.
func tagOwnerSystemAccountID(access AccessScope) (string, error) {
	if id := access.manageableID(); id != "" {
		return id, nil
	}
	if access.ViewerID != "" {
		return access.ViewerID, nil
	}
	return "", &ValidationError{Message: "缺少系统账户上下文"}
}

// ListTags mirrors listAccountTagsAsync: the owner's tags with the active
// account binding count.
func (s *Store) ListTags(ctx context.Context, access AccessScope) ([]AccountTagSummary, error) {
	ctx = ensureCtx(ctx)
	ownerID, err := tagOwnerSystemAccountID(access)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT account_tags.id, account_tags.system_account_id,
			account_tags.name,
			COUNT(CASE WHEN active_accounts.id IS NOT NULL THEN active_accounts.id END) AS account_count,
			account_tags.created_at, account_tags.updated_at
		FROM `+s.table("account_tags")+` account_tags
		LEFT JOIN `+s.table("account_tag_bindings")+` account_tag_bindings
			ON account_tag_bindings.tag_id = account_tags.id
		LEFT JOIN `+s.table("accounts")+` active_accounts
			ON active_accounts.id = account_tag_bindings.account_id
			AND active_accounts.deleted_at IS NULL
		WHERE account_tags.system_account_id = ?
		GROUP BY account_tags.id
		ORDER BY account_tags.name ASC, account_tags.id ASC`), ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	summaries := []AccountTagSummary{}
	for rows.Next() {
		var summary AccountTagSummary
		var owner string
		var createdAt, updatedAt string
		if err := rows.Scan(&summary.ID, &owner, &summary.Name, &summary.AccountCount, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		summary.AccountCount = maxInt(0, summary.AccountCount)
		summary.CreatedAt = createdAt
		summary.UpdatedAt = updatedAt
		summaries = append(summaries, summary)
	}
	return summaries, rows.Err()
}

// DeleteTag mirrors deleteAccountTagAsync: owner-scoped, refuses tags that are
// still bound to live accounts. Returns false when the tag is missing.
func (s *Store) DeleteTag(ctx context.Context, tagID string, access AccessScope) (bool, error) {
	ctx = ensureCtx(ctx)
	id := strings.TrimSpace(tagID)
	if id == "" {
		return false, nil
	}
	ownerID, err := tagOwnerSystemAccountID(access)
	if err != nil {
		return false, err
	}
	var rowID string
	err = s.db.QueryRowContext(ctx, s.bind(`SELECT id FROM `+s.table("account_tags")+`
		WHERE id = ? AND system_account_id = ? LIMIT 1`), id, ownerID).Scan(&rowID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var bound int
	if err := s.db.QueryRowContext(ctx, s.bind(`SELECT 1
		FROM `+s.table("account_tag_bindings")+` account_tag_bindings
		INNER JOIN `+s.table("accounts")+` accounts
			ON accounts.id = account_tag_bindings.account_id
			AND accounts.deleted_at IS NULL
		WHERE account_tag_bindings.tag_id = ?
		LIMIT 1`), id).Scan(&bound); errors.Is(err, sql.ErrNoRows) {
		// Not in use: fall through to the delete.
	} else if err != nil {
		return false, err
	} else {
		return false, &TagInUseError{}
	}
	result, err := s.db.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("account_tags")+`
		WHERE id = ? AND system_account_id = ?`), id, ownerID)
	if err != nil {
		return false, err
	}
	affected, _ := result.RowsAffected()
	return affected > 0, nil
}

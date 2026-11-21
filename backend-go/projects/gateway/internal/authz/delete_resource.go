// Per-grant resource-deletion arm of the resource-authorization runtime
// domain: the live grants of one deleted resource are revoked one by one and
// their runtimes re-projected through recomputeGrantRuntimes, so terminal
// runtimes disable their bindings and instances (send-and-claim model; the
// legacy sync chain and the quota scope binding rebuild no longer exist).
package authz

import (
	"context"
	"database/sql"
)

// RevokeGrantsForResourceDeleted is the export the accounts delete path calls
// inside its own transaction. Grants already at a terminal state other than
// 'returned' are skipped, so their rows and sources stay untouched.
func (s *Store) RevokeGrantsForResourceDeleted(ctx context.Context, tx *sql.Tx, resourceType, resourceID, actor, now string) error {
	ctx = ensureCtx(ctx)
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT id FROM `+s.table("resource_authorization_grants")+` g
		WHERE g.resource_type = ?
			AND g.resource_id = ?
			AND g.status NOT IN ('revoked', 'returned')
		ORDER BY g.created_at ASC, g.id ASC`), resourceType, resourceID)
	if err != nil {
		return err
	}
	var grantIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		grantIDs = append(grantIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, grantID := range grantIDs {
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_grants")+`
			SET status = 'revoked', ended_at = COALESCE(ended_at, ?), ended_reason = COALESCE(ended_reason, 'account_deleted'),
				revoked_by = ?, revoked_at = ?, updated_at = ?
			WHERE id = ?`), now, actor, now, now, grantID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_sources")+`
			SET status = 'revoked', ended_at = COALESCE(ended_at, ?), ended_reason = COALESCE(ended_reason, 'account_deleted'),
				revoked_by = ?, revoked_at = ?, updated_at = ?
			WHERE grant_id = ? AND status IN ('pending','active')`), now, actor, now, now, grantID); err != nil {
			return err
		}
		if err := s.recomputeGrantRuntimes(ctx, tx, grantID, actor, now); err != nil {
			return err
		}
	}
	return nil
}

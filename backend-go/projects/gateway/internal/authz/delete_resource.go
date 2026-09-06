// Per-grant resource-deletion arm of the resource-authorization runtime sync
// domain. The archived Node delete cleanup carries two dialect arms:
//   - PostgreSQL (revokeAccountAuthorizationsForDeletedResourceAsync,
//     account-delete-cleanup.repository.ts:420-477): one bulk pass that
//     flips grants/sources/authorizations to 'revoked' with
//     ended_reason/revoked_reason='account_deleted' and deletes the resource's
//     quota scope bindings outright. Mirrored by the accounts slice
//     (internal/accounts delete.go bulk arm).
//   - SQLite (revokeAccountAuthorizationsForDeletedResource,
//     account-delete-cleanup.repository.ts:479-492): the resource's live
//     grants are walked one by one through revokeResourceAuthorizationGrant
//     (write-state.repository.ts:726-733) so every write lands through the
//     per-grant runtime sync domain.
//
// This file ports that SQLite arm: RevokeGrantsForResourceDeleted is the
// export the accounts delete path calls inside its own transaction.
package authz

import (
	"context"
	"database/sql"
)

// RevokeGrantsForResourceDeleted mirrors the SQLite sync arm
// (account-delete-cleanup.repository.ts:479-492): the live grants of one
// resource (status NOT IN ('revoked','returned'), oldest first) are each run
// through revokeResourceAuthorizationGrant (write-state.repository.ts:726-733):
//
//   - the grant row flips to 'revoked' with a direct (non-COALESCE)
//     revoked_by/revoked_at overwrite (:727-729); safe because the scan
//     filtered terminal grants;
//   - syncResourceAuthorizationGrantRuntime (:744-751) re-projects the
//     runtime: direct grants revoke the manual sources (active/superseded)
//     with ended_reason='authorization_revoked' and force the conditional
//     terminal refresh (:763-823), team grants cascade to the team sources
//     (:949-972), then the per-grant quota scope bindings are re-synced and
//     the account-health inputs fan out with reason
//     'authorization_grant_changed' — all inside the caller's transaction;
//   - the per-grant cleanupInactiveAuthorizationBindings tail (:731) is a
//     pure cache invalidation in the archive (write-state.repository.ts:643-648);
//     the caller serves it with its post-commit invalidation.
//
// Grants already at a terminal state are skipped, so their rows, sources and
// quota scope bindings stay untouched — the documented dialect difference
// against the PostgreSQL bulk arm, which drops the whole resource's bindings
// (including terminal grants'). The team cascade reuses the shared
// syncGrantRuntime projection (sync.go), whose team-source refresh keeps the
// default preserve-expired options of the async chain; the manual-source and
// terminal semantics of the direct-grant path are byte-identical between the
// archived sync and async variants.
func (s *Store) RevokeGrantsForResourceDeleted(ctx context.Context, tx *sql.Tx, resourceType, resourceID, actor, now string) error {
	ctx = ensureCtx(ctx)
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT `+grantColumns+`
		FROM `+s.table("resource_authorization_grants")+` g
		WHERE g.resource_type = ?
			AND g.resource_id = ?
			AND g.status NOT IN ('revoked', 'returned')
		ORDER BY g.created_at ASC, g.id ASC`), resourceType, resourceID)
	if err != nil {
		return err
	}
	var grants []grantRow
	for rows.Next() {
		grant, scanErr := s.scanGrant(rows)
		if scanErr != nil {
			rows.Close()
			return scanErr
		}
		grants = append(grants, grant)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, grant := range grants {
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_grants")+`
			SET status = 'revoked', revoked_by = ?, revoked_at = ?, updated_at = ?
			WHERE id = ?`), actor, now, now, grant.ID); err != nil {
			return err
		}
		// syncResourceAuthorizationGrantRuntime receives the revoked projection
		// ({...grant, status:'revoked', revoked_by, revoked_at, updated_at},
		// :730-732).
		next := grant
		next.Status = StatusRevoked
		next.RevokedBy = sql.NullString{String: actor, Valid: true}
		next.RevokedAt = sql.NullString{String: now, Valid: true}
		next.UpdatedAt = now
		if err := s.syncGrantRuntime(ctx, tx, &next, actor, now); err != nil {
			return err
		}
	}
	return nil
}

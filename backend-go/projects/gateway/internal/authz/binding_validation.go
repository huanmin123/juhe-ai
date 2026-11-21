// Write-time validation for binding an authorized group to a route strategy.
// The gateway repeats the same checks per request; this gate prevents a stale
// acceptance from being persisted as a new binding in the first place.
package authz

import (
	"context"
	"database/sql"
	"errors"

	"github.com/huanminabc/juhe-ai/backend-go-platform/authzrevision"
)

func (s *Store) ValidateGroupBinding(ctx context.Context, q authzrevision.Queryer, granteeID, groupID, ownerID, now string) (bool, error) {
	if granteeID == "" || groupID == "" || ownerID == "" {
		return false, nil
	}
	secret, err := s.revisionSecretOrFail()
	if err != nil {
		return false, err
	}
	var runtimeID, runtimeOwner, effectiveType, effectiveTeam string
	var expiresAt, snapshotRevision string
	var settingsEnabled int
	err = q.QueryRowContext(ctx, s.bind(`SELECT ra.id, ra.resource_owner_system_account_id,
			COALESCE(ra.effective_source_type, ''), COALESCE(ra.effective_source_team_id, ''),
			COALESCE(ra.expires_at, ''), COALESCE(gs.enabled, 0), COALESCE(gs.snapshot_revision, '')
		FROM `+s.table("resource_authorizations")+` ra
		JOIN `+s.table("group_authorization_settings")+` gs
		  ON gs.authorization_id = ra.id AND gs.system_account_id = ra.grantee_system_account_id
		WHERE ra.resource_type = 'group' AND ra.resource_id = ? AND ra.grantee_system_account_id = ?
		  AND ra.status = 'active'
		ORDER BY ra.updated_at DESC, ra.id DESC LIMIT 1`), groupID, granteeID).
		Scan(&runtimeID, &runtimeOwner, &effectiveType, &effectiveTeam, &expiresAt, &settingsEnabled, &snapshotRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if runtimeOwner != ownerID || effectiveType == "" || settingsEnabled != 1 || snapshotRevision == "" {
		return false, nil
	}
	if expiresAt != "" && expiresAt <= now {
		return false, nil
	}
	var currentOwner string
	err = q.QueryRowContext(ctx, s.bind(`SELECT system_account_id FROM `+s.table("groups")+` WHERE id = ?`), groupID).Scan(&currentOwner)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if currentOwner != ownerID {
		return false, nil
	}
	var acceptedRevision string
	var acceptedVersion, revisionVersion int
	err = q.QueryRowContext(ctx, s.bind(`SELECT COALESCE(s.accepted_source_revision, ''),
			COALESCE(s.accepted_source_revision_version, 0), COALESCE(s.revision_version, 0)
		FROM `+s.table("resource_authorization_sources")+` s
		JOIN `+s.table("resource_authorization_grants")+` g ON g.id = s.grant_id
		WHERE s.authorization_id = ? AND s.status = 'active' AND s.source_type = ?
		  AND (s.source_type = 'manual' OR s.source_team_id = ?)
		  AND g.status = 'active'
		  AND (COALESCE(g.expires_at, '') = '' OR g.expires_at > ?)
		ORDER BY s.updated_at DESC, s.id DESC LIMIT 1`), runtimeID, effectiveType, effectiveTeam, now).
		Scan(&acceptedRevision, &acceptedVersion, &revisionVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	currentRevision := authzrevision.Group(secret, groupID, ownerID)
	if acceptedRevision == "" || acceptedRevision != currentRevision ||
		acceptedVersion != authzrevision.Version || revisionVersion != authzrevision.Version {
		return false, nil
	}

	rows, err := q.QueryContext(ctx, s.bind(`SELECT account_id, source_revision, revision_version
		FROM `+s.table("resource_authorization_group_accounts")+`
		WHERE authorization_id = ? ORDER BY account_id`), runtimeID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var accountID, storedRevision string
		var storedVersion int
		if err := rows.Scan(&accountID, &storedRevision, &storedVersion); err != nil {
			return false, err
		}
		currentRevision, version, snapshot, err := authzrevision.Compute(ctx, q, accountID, secret)
		if err != nil {
			return false, err
		}
		if version != storedVersion || currentRevision != storedRevision {
			return false, nil
		}
		// A snapshot account that changed owner is no longer part of this
		// group's authorization; the recipient must refresh the scope.
		var accountOwner string
		if err := q.QueryRowContext(ctx, s.bind(`SELECT system_account_id FROM `+s.table("accounts")+`
			WHERE id = ? AND deleted_at IS NULL`), accountID).Scan(&accountOwner); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return false, nil
			}
			return false, err
		}
		if accountOwner != ownerID || snapshot.AccountID != accountID {
			return false, nil
		}
		// The account must still be an enabled member of the authorized group;
		// snapshot rows alone can lag a group-account removal.
		var membership int
		if err := q.QueryRowContext(ctx, s.bind(`SELECT count(*) FROM `+s.table("group_accounts")+`
			WHERE group_id = ? AND account_id = ? AND system_account_id = ? AND enabled = 1`),
			groupID, accountID, ownerID).Scan(&membership); err != nil {
			return false, err
		}
		if membership == 0 {
			return false, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return true, nil
}

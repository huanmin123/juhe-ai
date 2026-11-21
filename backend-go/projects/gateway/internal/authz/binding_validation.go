// Write-time validation for binding an authorized group to a route strategy.
// The gateway repeats the same checks per request; this gate prevents a stale
// claim from being persisted as a new binding in the first place. The
// send-and-claim contract is purely liveness-based: an active runtime behind
// an active, unexpired grant plus the claim row in group_authorization_settings.
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
	var runtimeID, runtimeOwner, effectiveType, effectiveTeam string
	var expiresAt string
	err := q.QueryRowContext(ctx, s.bind(`SELECT ra.id, ra.resource_owner_system_account_id,
			COALESCE(ra.effective_source_type, ''), COALESCE(ra.effective_source_team_id, ''),
			COALESCE(ra.expires_at, '')
		FROM `+s.table("resource_authorizations")+` ra
		JOIN `+s.table("group_authorization_settings")+` gs
		  ON gs.authorization_id = ra.id AND gs.system_account_id = ra.grantee_system_account_id
		WHERE ra.resource_type = 'group' AND ra.resource_id = ? AND ra.grantee_system_account_id = ?
		  AND ra.status = 'active'
		ORDER BY ra.updated_at DESC, ra.id DESC LIMIT 1`), groupID, granteeID).
		Scan(&runtimeID, &runtimeOwner, &effectiveType, &effectiveTeam, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if runtimeOwner != ownerID || effectiveType == "" {
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
	// The effective source must still be active behind an active, unexpired
	// grant; a stale runtime projection alone never authorizes a binding.
	var one int
	err = q.QueryRowContext(ctx, s.bind(`SELECT 1
		FROM `+s.table("resource_authorization_sources")+` s
		JOIN `+s.table("resource_authorization_grants")+` g ON g.id = s.grant_id
		WHERE s.authorization_id = ? AND s.status = 'active' AND s.source_type = ?
		  AND (s.source_type = 'manual' OR s.source_team_id = ?)
		  AND g.status = 'active'
		  AND (COALESCE(g.expires_at, '') = '' OR g.expires_at > ?)
		LIMIT 1`), runtimeID, effectiveType, effectiveTeam, now).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

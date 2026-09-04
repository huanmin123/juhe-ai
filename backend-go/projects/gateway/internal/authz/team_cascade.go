// Team-status cascade into the authorization domain: mirrors Node
// revokeAllTeamSourcesAsync (:1631-1660) /
// applyActiveTeamGrantsToMembersAsync (:1574-1596) /
// revokeTeamSourcesForMemberAsync (:1598-1629).
package authz

import (
	"context"
	"database/sql"
	"time"
)

// RevokeAllTeamSources revokes every active team source of a team (reason
// e.g. team_disabled) and refreshes each affected runtime row with the default
// (preserve-expired) refresh options, matching the Node cascade.
func (s *Store) RevokeAllTeamSources(ctx context.Context, teamID, reason string) error {
	ctx = ensureCtx(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.revokeAllTeamSourcesTx(ctx, tx, teamID, reason); err != nil {
		return err
	}
	return tx.Commit()
}

// RevokeAllTeamSourcesTx is the transaction-bound variant used by team
// patch cascades.
func (s *Store) RevokeAllTeamSourcesTx(ctx context.Context, tx *sql.Tx, teamID, reason string) error {
	return s.revokeAllTeamSourcesTx(ctx, tx, teamID, reason)
}

func (s *Store) revokeAllTeamSourcesTx(ctx context.Context, tx *sql.Tx, teamID, reason string) error {
	now := s.now().UTC().Format(time.RFC3339Nano)
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT DISTINCT authorization_id
		FROM `+s.table("resource_authorization_sources")+`
		WHERE source_type = 'team' AND source_team_id = ? AND status = 'active'
		ORDER BY authorization_id ASC`), teamID)
	if err != nil {
		return err
	}
	var authorizationIDs []string
	for rows.Next() {
		var authorizationID string
		if err := rows.Scan(&authorizationID); err != nil {
			rows.Close()
			return err
		}
		authorizationIDs = append(authorizationIDs, authorizationID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, authorizationID := range authorizationIDs {
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_sources")+`
			SET status = 'revoked', ended_at = COALESCE(ended_at, ?), ended_reason = COALESCE(ended_reason, ?),
				updated_at = ?
			WHERE authorization_id = ? AND source_type = 'team' AND source_team_id = ? AND status = 'active'`),
			now, reason, now, authorizationID, teamID); err != nil {
			return err
		}
		// Node :1658 refreshes with default options (no terminal reason).
		if err := s.refreshEffectiveSource(ctx, tx, authorizationID, "system", now, refreshOptions{}); err != nil {
			return err
		}
	}
	return nil
}

// ReactivateTeamGrants re-applies active team grants to their members after a
// team is re-enabled (mirrors reactivateTeamGrantSourcesAsync →
// applyActiveTeamGrantsToMembersAsync :1574-1596: the grant's remark, expiry
// and limits drive the per-member team-source upsert).
func (s *Store) ReactivateTeamGrants(ctx context.Context, teamID string) error {
	ctx = ensureCtx(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	grants, err := s.activeTeamGrants(ctx, tx, teamID)
	if err != nil {
		return err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	for _, grant := range grants {
		members, err := s.activeTeamMemberIDs(ctx, tx, teamID)
		if err != nil {
			return err
		}
		limits, err := normalizeAuthorizationLimitsJSON(grant.LimitsJSON.String)
		if err != nil {
			return err
		}
		projection := runtimeProjection{
			Remark:     nullStringPointer(grant.Remark),
			ExpiresAt:  nullStringPointer(grant.ExpiresAt),
			LimitsJSON: limits,
		}
		teamIDCopy := grant.GranteeTeamID.String
		for _, memberID := range members {
			if memberID == grant.OwnerID {
				continue
			}
			if err := s.upsertRuntimeForUser(ctx, tx, grant.ResourceType, grant.ResourceID, grant.OwnerID,
				memberID, &teamIDCopy, projection, "system", now); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// RevokeTeamSourcesForMember revokes the team sources of one removed member
// (mirrors revokeTeamSourcesForMemberAsync: the runtime rows keep the default
// refresh options).
func (s *Store) RevokeTeamSourcesForMember(ctx context.Context, teamID, memberAccountID string) error {
	ctx = ensureCtx(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now().UTC().Format(time.RFC3339Nano)
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT DISTINCT s.authorization_id
		FROM `+s.table("resource_authorization_sources")+` s
		INNER JOIN `+s.table("resource_authorizations")+` r ON r.id = s.authorization_id
		WHERE s.source_type = 'team' AND s.source_team_id = ? AND s.status = 'active'
			AND r.grantee_system_account_id = ? AND r.status = 'active'`), teamID, memberAccountID)
	if err != nil {
		return err
	}
	var authorizationIDs []string
	for rows.Next() {
		var authorizationID string
		if err := rows.Scan(&authorizationID); err != nil {
			rows.Close()
			return err
		}
		authorizationIDs = append(authorizationIDs, authorizationID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, authorizationID := range authorizationIDs {
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_sources")+`
			SET status = 'revoked', ended_at = COALESCE(ended_at, ?), ended_reason = 'member_removed',
				revoked_by = 'system', revoked_at = ?, updated_at = ?
			WHERE authorization_id = ? AND source_type = 'team' AND source_team_id = ? AND status = 'active'`),
			now, now, now, authorizationID, teamID); err != nil {
			return err
		}
		if err := s.refreshEffectiveSource(ctx, tx, authorizationID, "system", now, refreshOptions{}); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) activeTeamGrants(ctx context.Context, tx *sql.Tx, teamID string) ([]grantRow, error) {
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT `+grantColumns+` FROM `+s.table("resource_authorization_grants")+` g
		WHERE g.grantee_type = 'team' AND g.grantee_team_id = ? AND g.status = 'active'`), teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	grants := []grantRow{}
	for rows.Next() {
		grant, err := s.scanGrant(rows)
		if err != nil {
			return nil, err
		}
		grants = append(grants, grant)
	}
	return grants, rows.Err()
}

func (s *Store) activeTeamMemberIDs(ctx context.Context, tx *sql.Tx, teamID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT m.system_account_id
		FROM `+s.table("system_team_members")+` m
		INNER JOIN `+s.table("system_accounts")+` a ON a.id = m.system_account_id
		WHERE m.team_id = ? AND m.status = 'active' AND a.status = 'active'`), teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

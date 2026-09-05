// Team-status cascade into the authorization domain: faithful port of Node
// resource-authorization-write.repository.ts
//   - applyActiveTeamGrantsToMembersAsync (:1574-1596)
//   - revokeTeamSourcesForMemberAsync     (:1598-1629)
//   - revokeAllTeamSourcesAsync           (:1631-1660)
//   - reactivateTeamGrantSourcesAsync     (:1662-1665)
//   - activeTeamMemberRowsAsync           (:1532-1548)
//   - activeTeamGrantRowsAsync            (:1554-1568)
//
// All mutating entry points are transaction-bound Tx variants: the Node
// cascades run inside the caller's transaction (system-team.repository.ts
// updateSystemTeamAsync :527/:531, addSystemTeamMembersAsync :696,
// removeSystemTeamMemberAsync :777) and the standalone wrappers merely open
// one on the caller's behalf. Node evidence for the guardrails implemented
// here: real actor propagated into revoked_by/revoked_at and the effective
// source refresh (:1626-1627, :1652-1658), fan-out LIMIT ceilings
// members*grants+1 (:1640) and grants+1 (:1609, :1563, :1543).
package authz

import (
	"context"
	"database/sql"
	"time"
)

// cascadeTeamMemberLimit mirrors activeTeamMemberRowsAsync's ceiling:
// maxSystemTeamMembersPerTeam + 1 (:1543).
func cascadeTeamMemberLimit() int { return MaxTeamMembersPerTeam + 1 }

// cascadeTeamGrantLimit mirrors activeTeamGrantRowsAsync's /
// revokeTeamSourcesForMemberAsync's ceiling: maxSystemTeamActiveGrantCount + 1
// (:1563, :1609).
func cascadeTeamGrantLimit() int { return MaxTeamActiveGrantCount + 1 }

// cascadeFanoutLimit mirrors revokeAllTeamSourcesAsync's ceiling:
// maxSystemTeamMembersPerTeam * maxSystemTeamActiveGrantCount + 1 (:1640).
func cascadeFanoutLimit() int { return MaxTeamMembersPerTeam*MaxTeamActiveGrantCount + 1 }

// RevokeAllTeamSources opens its own transaction and revokes every active
// team source of a team (reason e.g. team_disabled).
func (s *Store) RevokeAllTeamSources(ctx context.Context, teamID, actor, reason string) error {
	ctx = ensureCtx(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.revokeAllTeamSourcesTx(ctx, tx, teamID, actor, s.now().UTC().Format(time.RFC3339Nano), reason); err != nil {
		return err
	}
	return tx.Commit()
}

// RevokeAllTeamSourcesTx is the transaction-bound variant used by the team
// patch cascade (Node passes the new team updated_at as `now`,
// system-team.repository.ts :527).
func (s *Store) RevokeAllTeamSourcesTx(ctx context.Context, tx *sql.Tx, teamID, actor, now, reason string) error {
	return s.revokeAllTeamSourcesTx(ctx, tx, teamID, actor, now, reason)
}

func (s *Store) revokeAllTeamSourcesTx(ctx context.Context, tx *sql.Tx, teamID, actor, now, reason string) error {
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT DISTINCT authorization_id
		FROM `+s.table("resource_authorization_sources")+`
		WHERE source_type = 'team' AND source_team_id = ? AND status = 'active'
		ORDER BY authorization_id ASC LIMIT ?`), teamID, cascadeFanoutLimit())
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
	// Node :1641-1643: fan-out above the members×grants ceiling aborts the
	// cascade (and therefore the whole enclosing transaction).
	if len(authorizationIDs) > MaxTeamMembersPerTeam*MaxTeamActiveGrantCount {
		return failf("授权团队来源展开超过当前系统上限，请先拆分团队或回收部分授权")
	}
	for _, authorizationID := range authorizationIDs {
		// Node :1645-1657: real actor lands in revoked_by/revoked_at.
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_sources")+`
			SET status = 'revoked', ended_at = COALESCE(ended_at, ?), ended_reason = COALESCE(ended_reason, ?),
				revoked_by = ?, revoked_at = ?, updated_at = ?
			WHERE authorization_id = ? AND source_type = 'team' AND source_team_id = ? AND status = 'active'`),
			now, reason, actor, now, now, authorizationID, teamID); err != nil {
			return err
		}
		// Node :1658 refreshes with default options (no terminal reason).
		if err := s.refreshEffectiveSource(ctx, tx, authorizationID, actor, now, refreshOptions{}); err != nil {
			return err
		}
	}
	return nil
}

// ReactivateTeamGrants opens its own transaction and re-applies active team
// grants to their members after a team is re-enabled.
func (s *Store) ReactivateTeamGrants(ctx context.Context, teamID, actor string) error {
	ctx = ensureCtx(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.ReactivateTeamGrantsTx(ctx, tx, teamID, actor, s.now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

// ReactivateTeamGrantsTx mirrors reactivateTeamGrantSourcesAsync
// (:1662-1665): the active member list drives
// applyActiveTeamGrantsToMembersAsync inside the caller's transaction (Node
// system-team.repository.ts :531 passes the team tx and updated_at).
func (s *Store) ReactivateTeamGrantsTx(ctx context.Context, tx *sql.Tx, teamID, actor, now string) error {
	members, err := s.activeTeamMemberRowsTx(ctx, tx, teamID)
	if err != nil {
		return err
	}
	return s.ApplyActiveTeamGrantsToMembersTx(ctx, tx, teamID, members, actor, now)
}

// ApplyActiveTeamGrantsToMembersTx mirrors applyActiveTeamGrantsToMembersAsync
// (:1574-1596): for every requested member × active team grant, upsert the
// member runtime with sourceType='team' (+ sourceTeamId) carrying the grant's
// remark/expires_at/limits, skipping the resource owner, all inside the
// caller's transaction.
func (s *Store) ApplyActiveTeamGrantsToMembersTx(ctx context.Context, tx *sql.Tx, teamID string, memberAccountIDs []string, actor, now string) error {
	grants, err := s.activeTeamGrantRowsTx(ctx, tx, teamID)
	if err != nil {
		return err
	}
	teamIDCopy := teamID
	for _, memberID := range memberAccountIDs {
		for _, grant := range grants {
			// Node :1579: the resource owner never receives a team-sourced
			// runtime row of their own resource.
			if grant.OwnerID == memberID {
				continue
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
			if err := s.upsertRuntimeForUser(ctx, tx, grant.ResourceType, grant.ResourceID, grant.OwnerID,
				memberID, &teamIDCopy, projection, actor, now); err != nil {
				return err
			}
		}
	}
	return nil
}

// RevokeTeamSourcesForMember opens its own transaction and revokes the team
// sources of one removed member.
func (s *Store) RevokeTeamSourcesForMember(ctx context.Context, teamID, memberAccountID, actor string) error {
	ctx = ensureCtx(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.RevokeTeamSourcesForMemberTx(ctx, tx, teamID, memberAccountID, actor, s.now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

// RevokeTeamSourcesForMemberTx mirrors revokeTeamSourcesForMemberAsync
// (:1598-1629) inside the caller's transaction (Node
// removeSystemTeamMemberAsync :777 passes the team tx and updated_at): the
// removed member's active team sources flip to revoked with the real actor in
// revoked_by/revoked_at and each runtime row is refreshed with the default
// (preserve-expired) options.
func (s *Store) RevokeTeamSourcesForMemberTx(ctx context.Context, tx *sql.Tx, teamID, memberAccountID, actor, now string) error {
	// Node :1599-1609: no ra.status filter — the grantee link alone selects
	// the rows; ORDER BY authorization_id ASC + grants+1 ceiling.
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT ras.authorization_id
		FROM `+s.table("resource_authorization_sources")+` ras
		INNER JOIN `+s.table("resource_authorizations")+` ra ON ra.id = ras.authorization_id
		WHERE ras.source_type = 'team' AND ras.source_team_id = ? AND ras.status = 'active'
			AND ra.grantee_system_account_id = ?
		ORDER BY ras.authorization_id ASC LIMIT ?`), teamID, memberAccountID, cascadeTeamGrantLimit())
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
	// Node :1610-1612.
	if len(authorizationIDs) > MaxTeamActiveGrantCount {
		return failf("单个授权团队最多支持 %d 条有效授权，请先回收或停用部分授权", MaxTeamActiveGrantCount)
	}
	for _, authorizationID := range authorizationIDs {
		// Node :1614-1626: ended_reason COALESCE keeps an earlier terminal
		// reason; the real actor lands in revoked_by/revoked_at.
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_sources")+`
			SET status = 'revoked', ended_at = COALESCE(ended_at, ?),
				ended_reason = COALESCE(ended_reason, 'member_removed'),
				revoked_by = ?, revoked_at = ?, updated_at = ?
			WHERE authorization_id = ? AND source_type = 'team' AND source_team_id = ? AND status = 'active'`),
			now, actor, now, now, authorizationID, teamID); err != nil {
			return err
		}
		if err := s.refreshEffectiveSource(ctx, tx, authorizationID, actor, now, refreshOptions{}); err != nil {
			return err
		}
	}
	return nil
}

// activeTeamGrantRowsTx mirrors activeTeamGrantRowsAsync (:1554-1568): active
// team grants ordered created_at ASC, id ASC with the grants+1 ceiling.
func (s *Store) activeTeamGrantRowsTx(ctx context.Context, tx *sql.Tx, teamID string) ([]grantRow, error) {
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT resource_type, resource_id, resource_owner_system_account_id,
		remark, expires_at, limits_json
		FROM `+s.table("resource_authorization_grants")+`
		WHERE grantee_type = 'team' AND grantee_team_id = ? AND status = 'active'
		ORDER BY created_at ASC, id ASC LIMIT ?`), teamID, cascadeTeamGrantLimit())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	grants := []grantRow{}
	for rows.Next() {
		var grant grantRow
		if err := rows.Scan(&grant.ResourceType, &grant.ResourceID, &grant.OwnerID,
			&grant.Remark, &grant.ExpiresAt, &grant.LimitsJSON); err != nil {
			return nil, err
		}
		grants = append(grants, grant)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(grants) > MaxTeamActiveGrantCount {
		// Node :1564-1566.
		return nil, failf("单个授权团队最多支持 %d 条有效授权，请先回收或停用部分授权", MaxTeamActiveGrantCount)
	}
	return grants, nil
}

// activeTeamMemberIDs is the unlimited member-id lookup retained for the
// sync.go grant→runtime chain (syncTeamGrantRuntime); the cascade paths use
// the ceiling-guarded activeTeamMemberRowsTx above.
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

// activeTeamMemberRowsTx mirrors activeTeamMemberRowsAsync (:1532-1548):
// active members of active accounts ordered joined_at ASC, id ASC with the
// members+1 ceiling.
func (s *Store) activeTeamMemberRowsTx(ctx context.Context, tx *sql.Tx, teamID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT m.system_account_id
		FROM `+s.table("system_team_members")+` m
		INNER JOIN `+s.table("system_accounts")+` a ON a.id = m.system_account_id
		WHERE m.team_id = ? AND m.status = 'active' AND a.status = 'active'
		ORDER BY m.joined_at ASC, m.id ASC LIMIT ?`), teamID, cascadeTeamMemberLimit())
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) > MaxTeamMembersPerTeam {
		// Node :1544-1546.
		return nil, failf("授权团队最多支持 %d 个成员，请先移除部分成员后再继续", MaxTeamMembersPerTeam)
	}
	return ids, nil
}

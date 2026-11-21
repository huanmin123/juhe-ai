// Team lifecycle propagation for the send-and-claim model: team membership
// only creates/withdraws member sources; instances, group bindings and group
// settings rows are created by the member's own claim. These
// transaction-bound entry points are called by systemteams inside the
// member-add/remove and team status transactions.
package authz

import (
	"context"
	"database/sql"
	"errors"
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

// RevokeAllTeamSources opens its own transaction and revokes every non-terminal
// team source of a team (team disabled/deleted or admin recovery).
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
	if err := tx.Commit(); err != nil {
		return err
	}
	s.invalidateAfterBusinessWrite(ctx, invalidationReasonRevoked)
	return nil
}

// RevokeAllTeamSourcesTx is the transaction-bound variant used by team status
// transitions. It revokes every non-terminal team source and every non-terminal
// team grant, then re-projects each affected runtime. A team that is disabled
// or deleted cannot resurrect old grants when re-enabled: the resource must be
// sent again.
func (s *Store) RevokeAllTeamSourcesTx(ctx context.Context, tx *sql.Tx, teamID, actor, now, reason string) error {
	return s.revokeAllTeamSourcesTx(ctx, tx, teamID, actor, now, reason)
}

func (s *Store) revokeAllTeamSourcesTx(ctx context.Context, tx *sql.Tx, teamID, actor, now, reason string) error {
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT id, COALESCE(grant_id, ''), authorization_id
		FROM `+s.table("resource_authorization_sources")+`
		WHERE source_type = 'team' AND source_team_id = ? AND status IN ('pending','active')
		ORDER BY authorization_id ASC, id ASC LIMIT ?`), teamID, cascadeFanoutLimit())
	if err != nil {
		return err
	}
	type sourceRow struct{ ID, GrantID, RuntimeID string }
	var sources []sourceRow
	for rows.Next() {
		var row sourceRow
		if err := rows.Scan(&row.ID, &row.GrantID, &row.RuntimeID); err != nil {
			rows.Close()
			return err
		}
		sources = append(sources, row)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(sources) > MaxTeamMembersPerTeam*MaxTeamActiveGrantCount {
		return failf("授权团队来源展开超过当前系统上限，请先拆分团队或回收部分授权")
	}

	grantIDs := map[string]bool{}
	runtimeIDs := map[string]bool{}
	for _, source := range sources {
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_sources")+`
			SET status = 'revoked', ended_at = COALESCE(ended_at, ?), ended_reason = ?,
				revoked_by = ?, revoked_at = ?, updated_at = ?
			WHERE id = ? AND status IN ('pending','active')`),
			now, reason, actor, now, now, source.ID); err != nil {
			return err
		}
		grantIDs[source.GrantID] = true
		runtimeIDs[source.RuntimeID] = true
	}

	// Zero-source team grants (sent to a team with no eligible member) must
	// also terminate; otherwise a disabled team would keep a pending offer
	// alive forever.
	grantRows, err := tx.QueryContext(ctx, s.bind(`SELECT id FROM `+s.table("resource_authorization_grants")+`
		WHERE grantee_type = 'team' AND grantee_team_id = ?
		  AND status IN ('pending','active','paused')
		ORDER BY id LIMIT ?`), teamID, cascadeFanoutLimit())
	if err != nil {
		return err
	}
	for grantRows.Next() {
		var id string
		if err := grantRows.Scan(&id); err != nil {
			grantRows.Close()
			return err
		}
		grantIDs[id] = true
	}
	grantRows.Close()
	if err := grantRows.Err(); err != nil {
		return err
	}
	for grantID := range grantIDs {
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_grants")+`
			SET status = 'revoked', ended_at = COALESCE(ended_at, ?), ended_reason = ?, updated_at = ?
			WHERE id = ? AND status IN ('pending','active','paused')`),
			now, reason, now, grantID); err != nil {
			return err
		}
	}

	for runtimeID := range runtimeIDs {
		if _, err := s.recomputeRuntime(ctx, tx, runtimeID, actor, now); err != nil {
			return err
		}
	}
	return nil
}

// ReactivateTeamGrants is intentionally a no-op. Revoked team sources are
// terminal; re-enabling a team must not silently re-grant access. The resource
// must be sent again.
func (s *Store) ReactivateTeamGrants(ctx context.Context, teamID, actor string) error {
	_ = ctx
	_ = teamID
	_ = actor
	return nil
}

// ReactivateTeamGrantsTx is the transaction-bound no-op counterpart.
func (s *Store) ReactivateTeamGrantsTx(ctx context.Context, tx *sql.Tx, teamID, actor, now string) error {
	_ = ctx
	_ = tx
	_ = teamID
	_ = actor
	_ = now
	return nil
}

// ApplyActiveTeamGrantsToMembersTx turns newly added team members into pending
// member sources: runtime + pending team source, no instance/binding. A member
// who previously declined or returned may receive the source again — the
// membership join is the delivery, claiming stays theirs to decide.
func (s *Store) ApplyActiveTeamGrantsToMembersTx(ctx context.Context, tx *sql.Tx, teamID string, memberAccountIDs []string, actor, now string) error {
	if len(memberAccountIDs) == 0 {
		return nil
	}
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT id FROM `+s.table("resource_authorization_grants")+`
		WHERE grantee_type = 'team' AND grantee_team_id = ?
		  AND status IN ('pending','active','paused')
		ORDER BY created_at ASC, id ASC LIMIT ?`), teamID, cascadeTeamGrantLimit())
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
	if len(grantIDs) > MaxTeamActiveGrantCount {
		return failf("单个授权团队最多支持 %d 条有效授权，请先回收或停用部分授权", MaxTeamActiveGrantCount)
	}
	for _, grantID := range grantIDs {
		grant, err := s.getOfferForUpdate(ctx, tx, grantID)
		if err != nil {
			return err
		}
		if grant == nil {
			continue
		}
		for _, memberID := range memberAccountIDs {
			if memberID == "" || memberID == grant.OwnerID {
				continue
			}
			if err := s.createTeamMemberSource(ctx, tx, grant, memberID, actor, now); err != nil {
				return err
			}
		}
	}
	return nil
}

// createTeamMemberSource creates one pending member source on the member's
// runtime unless the grant already has a pending/active source for them. Only
// database errors surface.
func (s *Store) createTeamMemberSource(ctx context.Context, tx *sql.Tx, grant *offerRecord, memberID, actor, now string) error {
	var status string
	var deletedAt sql.NullString
	err := tx.QueryRowContext(ctx, s.bind(`SELECT status, deleted_at FROM `+s.table("system_accounts")+` WHERE id = ?`), memberID).
		Scan(&status, &deletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if status != "active" || deletedAt.Valid {
		return nil
	}

	// Same team+resource live source already covers this member.
	var live int
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT count(*)
		FROM `+s.table("resource_authorization_sources")+` s
		JOIN `+s.table("resource_authorization_grants")+` g ON g.id = s.grant_id
		JOIN `+s.table("resource_authorizations")+` ra ON ra.id = s.authorization_id
		WHERE ra.grantee_system_account_id = ? AND s.source_type = 'team' AND s.source_team_id = ?
		  AND g.resource_type = ? AND g.resource_id = ? AND s.status IN ('pending','active')
		LIMIT 1`), memberID, grant.GranteeTeamID, grant.ResourceType, grant.ResourceID).Scan(&live); err != nil {
		return err
	}
	if live > 0 {
		return nil
	}

	if err := s.appendTeamPendingSource(ctx, tx, grant.ID, grant.GranteeTeamID, grant.ResourceType,
		grant.ResourceID, grant.OwnerID, memberID, grant.SourceRevision, grant.ExpiryCycle, actor, now); err != nil {
		return err
	}
	return nil
}

// RevokeTeamSourcesForMember opens its own transaction and revokes the
// non-terminal team sources of one removed member.
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
	if err := tx.Commit(); err != nil {
		return err
	}
	s.invalidateAfterBusinessWrite(ctx, invalidationReasonRevoked)
	return nil
}

// RevokeTeamSourcesForMemberTx revokes every non-terminal team source of the
// removed member (ended_reason=member_removed), re-projects the member's
// runtime and lets the team grant aggregate decide whether the whole team
// grant has ended.
func (s *Store) RevokeTeamSourcesForMemberTx(ctx context.Context, tx *sql.Tx, teamID, memberAccountID, actor, now string) error {
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT s.id, COALESCE(s.grant_id, ''), s.authorization_id
		FROM `+s.table("resource_authorization_sources")+` s
		JOIN `+s.table("resource_authorizations")+` ra ON ra.id = s.authorization_id
		WHERE s.source_type = 'team' AND s.source_team_id = ?
		  AND ra.grantee_system_account_id = ? AND s.status IN ('pending','active')
		ORDER BY s.authorization_id ASC, s.id ASC LIMIT ?`), teamID, memberAccountID, cascadeTeamGrantLimit())
	if err != nil {
		return err
	}
	type sourceRow struct{ ID, GrantID, RuntimeID string }
	var sources []sourceRow
	for rows.Next() {
		var row sourceRow
		if err := rows.Scan(&row.ID, &row.GrantID, &row.RuntimeID); err != nil {
			rows.Close()
			return err
		}
		sources = append(sources, row)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(sources) > MaxTeamActiveGrantCount {
		return failf("单个授权团队最多支持 %d 条有效授权，请先回收或停用部分授权", MaxTeamActiveGrantCount)
	}

	grantIDs := map[string]bool{}
	runtimeIDs := map[string]bool{}
	for _, source := range sources {
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_sources")+`
			SET status = 'revoked', ended_at = COALESCE(ended_at, ?), ended_reason = 'member_removed',
				revoked_by = ?, revoked_at = ?, updated_at = ?
			WHERE id = ? AND status IN ('pending','active')`),
			now, actor, now, now, source.ID); err != nil {
			return err
		}
		grantIDs[source.GrantID] = true
		runtimeIDs[source.RuntimeID] = true
	}
	for runtimeID := range runtimeIDs {
		if _, err := s.recomputeRuntime(ctx, tx, runtimeID, actor, now); err != nil {
			return err
		}
	}
	for grantID := range grantIDs {
		grant, err := s.getOfferForUpdate(ctx, tx, grantID)
		if err != nil {
			return err
		}
		if grant == nil {
			continue
		}
		if err := s.recomputeTeamGrantAggregate(ctx, tx, grant, actor, now); err != nil {
			return err
		}
	}
	return nil
}

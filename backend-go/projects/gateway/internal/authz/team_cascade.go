// Team lifecycle propagation for the recipient-consent model.
//
// The legacy Node cascades eagerly provisioned instances and pushed accounts
// into member groups. The recipient-consent contract changes that boundary:
// team membership only creates/withdraws member *offers*; instances, group
// bindings and snapshots are created by the member's own accept/bind actions.
// These transaction-bound entry points are called by systemteams inside the
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
// or deleted cannot resurrect old consents when re-enabled: the offer must be
// sent and approved again.
func (s *Store) RevokeAllTeamSourcesTx(ctx context.Context, tx *sql.Tx, teamID, actor, now, reason string) error {
	return s.revokeAllTeamSourcesTx(ctx, tx, teamID, actor, now, reason)
}

func (s *Store) revokeAllTeamSourcesTx(ctx context.Context, tx *sql.Tx, teamID, actor, now, reason string) error {
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT id, COALESCE(grant_id, ''), authorization_id
		FROM `+s.table("resource_authorization_sources")+`
		WHERE source_type = 'team' AND source_team_id = ? AND status IN ('pending','active','dormant','needs_review')
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
				revoked_by = ?, revoked_at = ?, pending_expires_at = NULL, updated_at = ?
			WHERE id = ? AND status IN ('pending','active','dormant','needs_review')`),
			now, reason, actor, now, now, source.ID); err != nil {
			return err
		}
		grantIDs[source.GrantID] = true
		runtimeIDs[source.RuntimeID] = true
	}

	// Zero-source team grants (pending_owner_approval / pending_team_approval /
	// pending_member_acceptance with no eligible member) must also terminate;
	// otherwise a disabled team would keep a pending offer alive forever.
	grantRows, err := tx.QueryContext(ctx, s.bind(`SELECT id FROM `+s.table("resource_authorization_grants")+`
		WHERE grantee_type = 'team' AND grantee_team_id = ?
		  AND status IN ('pending_owner_approval','pending_team_approval','pending','pending_member_acceptance','active','paused')
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
			SET status = 'revoked', ended_at = COALESCE(ended_at, ?), ended_reason = ?,
				pending_expires_at = NULL, updated_at = ?
			WHERE id = ?
			  AND status IN ('pending_owner_approval','pending_team_approval','pending','pending_member_acceptance','active','paused')`),
			now, reason, now, grantID); err != nil {
			return err
		}
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
		if err := s.syncRuntimeQuotaBindings(ctx, tx, grant, now); err != nil {
			return err
		}
		for _, target := range s.grantRuntimeGrantees(ctx, tx, grantID) {
			if err := s.insertNotification(ctx, tx, target, "offer_revoked", grantID, "", nil,
				"team-revoked:"+grantID+":"+target+":"+now, now); err != nil {
				return err
			}
		}
	}
	return nil
}

// ReactivateTeamGrants is intentionally a no-op in the recipient-consent
// model. Revoked team sources are terminal (design §4.2); re-enabling a team
// must not silently re-grant access. A new team offer must be created and
// approved again.
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

// ApplyActiveTeamGrantsToMembersTx turns newly added team members into member
// offers (design §5.8): runtime + pending team source, no instance/snapshot/
// binding. Suppression (blocklist/cooldown/history/limits) never fails the
// team membership write; it only records a notification for the member.
func (s *Store) ApplyActiveTeamGrantsToMembersTx(ctx context.Context, tx *sql.Tx, teamID string, memberAccountIDs []string, actor, now string) error {
	if len(memberAccountIDs) == 0 {
		return nil
	}
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT id FROM `+s.table("resource_authorization_grants")+`
		WHERE grantee_type = 'team' AND grantee_team_id = ?
		  AND status IN ('pending_member_acceptance','active','paused')
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
		createdAny := false
		for _, memberID := range memberAccountIDs {
			if memberID == "" || memberID == grant.OwnerID {
				continue
			}
			created, err := s.createTeamMemberOffer(ctx, tx, grant, memberID, actor, now)
			if err != nil {
				return err
			}
			if created {
				createdAny = true
			}
		}
		if createdAny && grant.PendingExpiresAt != "" {
			if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_grants")+`
				SET pending_expires_at = NULL, updated_at = ? WHERE id = ?`), now, grant.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// createTeamMemberOffer creates one pending member offer after applying the
// same eligibility gates as team approval. It returns created=false for every
// suppressed case; only database errors surface.
func (s *Store) createTeamMemberOffer(ctx context.Context, tx *sql.Tx, grant *offerRecord, memberID, actor, now string) (bool, error) {
	var status string
	var deletedAt sql.NullString
	err := tx.QueryRowContext(ctx, s.bind(`SELECT status, deleted_at FROM `+s.table("system_accounts")+` WHERE id = ?`), memberID).
		Scan(&status, &deletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if status != "active" || deletedAt.Valid {
		return false, nil
	}

	blocked, err := s.isBlocked(ctx, tx, memberID, grant.OwnerID, grant.ResourceType, grant.ResourceID)
	if err != nil {
		return false, err
	}
	if blocked {
		// Privacy: the grantor must not learn that this member blocked them.
		return false, nil
	}

	// Same team+resource live source already covers this member.
	var live int
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT count(*)
		FROM `+s.table("resource_authorization_sources")+` s
		JOIN `+s.table("resource_authorization_grants")+` g ON g.id = s.grant_id
		JOIN `+s.table("resource_authorizations")+` ra ON ra.id = s.authorization_id
		WHERE ra.grantee_system_account_id = ? AND s.source_type = 'team' AND s.source_team_id = ?
		  AND g.resource_type = ? AND g.resource_id = ? AND s.status IN ('pending','active','dormant','needs_review')
		LIMIT 1`), memberID, grant.GranteeTeamID, grant.ResourceType, grant.ResourceID).Scan(&live); err != nil {
		return false, err
	}
	if live > 0 {
		return false, nil
	}
	// A member who previously declined or returned this team's offer for the
	// same resource is not auto-reoffered; the grantor must re-invite.
	var exited int
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT count(*)
		FROM `+s.table("resource_authorization_sources")+` s
		JOIN `+s.table("resource_authorization_grants")+` g ON g.id = s.grant_id
		JOIN `+s.table("resource_authorizations")+` ra ON ra.id = s.authorization_id
		WHERE ra.grantee_system_account_id = ? AND s.source_type = 'team' AND s.source_team_id = ?
		  AND g.resource_type = ? AND g.resource_id = ? AND s.status IN ('declined','returned')
		LIMIT 1`), memberID, grant.GranteeTeamID, grant.ResourceType, grant.ResourceID).Scan(&exited); err != nil {
		return false, err
	}
	if exited > 0 {
		return false, nil
	}

	if err := s.checkCooldown(ctx, tx, grant.OwnerID, "team_member", "", grant.GranteeTeamID, memberID,
		grant.ResourceType, grant.ResourceID, now); err != nil {
		if _, ok := err.(*Fail); ok {
			if err := s.notifySuppressedOffer(ctx, tx, memberID, grant, "cooldown", now); err != nil {
				return false, err
			}
			return false, nil
		}
		return false, err
	}
	var active, pending int
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT
			COALESCE(SUM(CASE WHEN s.status IN ('active','dormant','needs_review') THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN s.status = 'pending' THEN 1 ELSE 0 END), 0)
		FROM `+s.table("resource_authorization_sources")+` s
		JOIN `+s.table("resource_authorizations")+` ra ON ra.id = s.authorization_id
		WHERE ra.grantee_system_account_id = ?`), memberID).Scan(&active, &pending); err != nil {
		return false, err
	}
	if active >= MaxActiveSourcesPerGrantee || pending >= MaxPendingSourcesPerGrantee {
		if err := s.notifySuppressedOffer(ctx, tx, memberID, grant, "limit_reached", now); err != nil {
			return false, err
		}
		return false, nil
	}

	runtime, err := s.ensureRuntime(ctx, tx, grant.ResourceType, grant.ResourceID, grant.OwnerID, memberID, actor, now)
	if err != nil {
		return false, err
	}
	revision, version, err := s.sourceCurrentRevision(ctx, tx, grant.ResourceType, grant.ResourceID, grant.OwnerID)
	if err != nil {
		return false, err
	}
	sourceID := newSourceID(s.now)
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("resource_authorization_sources")+`
		(id, grant_id, authorization_id, source_type, source_team_id, status, observed_source_revision,
		 revision_version, expiry_cycle, pending_expires_at, created_by, created_at, updated_at, acceptance_mode)
		VALUES (?, ?, ?, 'team', ?, 'pending', ?, ?, ?, ?, ?, ?, ?, 'recipient')`),
		sourceID, grant.ID, runtime.ID, grant.GranteeTeamID, revision, version, grant.ExpiryCycle,
		nowPlus(parseTimeOrNow(now), MemberOfferTTL), actor, now, now); err != nil {
		return false, err
	}
	if _, err := s.recomputeRuntime(ctx, tx, runtime.ID, actor, now); err != nil {
		return false, err
	}
	if err := s.insertNotification(ctx, tx, memberID, "member_offer_pending", grant.ID, sourceID,
		map[string]any{"resourceType": grant.ResourceType, "resourceId": grant.ResourceID},
		"member-offer:"+sourceID, now); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) notifySuppressedOffer(ctx context.Context, tx *sql.Tx, memberID string, grant *offerRecord, reason, now string) error {
	return s.insertNotification(ctx, tx, memberID, "offer_suppressed", grant.ID, "",
		map[string]any{"reason": reason, "resourceType": grant.ResourceType, "resourceId": grant.ResourceID},
		"offer-suppressed:"+grant.ID+":"+memberID+":"+now, now)
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
// offer has ended.
func (s *Store) RevokeTeamSourcesForMemberTx(ctx context.Context, tx *sql.Tx, teamID, memberAccountID, actor, now string) error {
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT s.id, COALESCE(s.grant_id, ''), s.authorization_id
		FROM `+s.table("resource_authorization_sources")+` s
		JOIN `+s.table("resource_authorizations")+` ra ON ra.id = s.authorization_id
		WHERE s.source_type = 'team' AND s.source_team_id = ?
		  AND ra.grantee_system_account_id = ? AND s.status IN ('pending','active','dormant','needs_review')
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
				revoked_by = ?, revoked_at = ?, pending_expires_at = NULL, updated_at = ?
			WHERE id = ? AND status IN ('pending','active','dormant','needs_review')`),
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
		if err := s.syncRuntimeQuotaBindings(ctx, tx, grant, now); err != nil {
			return err
		}
		if err := s.insertNotification(ctx, tx, memberAccountID, "source_revoked", grantID, "", nil,
			"member-removed:"+grantID+":"+memberAccountID, now); err != nil {
			return err
		}
		if err := s.insertNotification(ctx, tx, grant.OwnerID, "member_source_revoked", grantID, "", nil,
			"member-removed-owner:"+grantID+":"+memberAccountID, now); err != nil {
			return err
		}
	}
	return nil
}

// activeTeamGrantRowsTx mirrors activeTeamGrantRowsAsync (:1554-1568): active
// team grants ordered created_at ASC, id ASC with the grants+1 ceiling. The id
// column rides along for the quota-scope bindings tail (source_id).
func (s *Store) activeTeamGrantRowsTx(ctx context.Context, tx *sql.Tx, teamID string) ([]grantRow, error) {
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT id, resource_type, resource_id, resource_owner_system_account_id,
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
		if err := rows.Scan(&grant.ID, &grant.ResourceType, &grant.ResourceID, &grant.OwnerID,
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
// the ceiling-guarded activeTeamMemberRowsTx above. B3-H1: the lookup joins
// system_teams and requires status='active' so a disabled team never expands
// its members again (the team-disable cascade only revokes sources; a later
// active re-sync must not re-provision member runtime rows / instances).
func (s *Store) activeTeamMemberIDs(ctx context.Context, tx *sql.Tx, teamID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT m.system_account_id
		FROM `+s.table("system_team_members")+` m
		INNER JOIN `+s.table("system_accounts")+` a ON a.id = m.system_account_id
		INNER JOIN `+s.table("system_teams")+` t ON t.id = m.team_id
		WHERE m.team_id = ? AND m.status = 'active' AND a.status = 'active' AND t.status = 'active'`), teamID)
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

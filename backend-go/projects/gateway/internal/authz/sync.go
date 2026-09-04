// Faithful port of the Node grant→runtime projection chain in
// resource-authorization-write.repository.ts:
//   - syncResourceAuthorizationGrantRuntimeAsync (:995-1000)
//   - syncUserGrantRuntimeAsync (:1249-1332)
//   - syncTeamGrantMemberAuthorizationsAsync (:1334-1390)
//   - upsertResourceAuthorizationForUserAsync (:1122-1247)
//   - upsertResourceAuthorizationSourceAsync (:1448-1509)
//   - refreshResourceAuthorizationEffectiveSourceAsync (:2026-2160)
//
// Every patch/revoke/return/expire/create write routes through this chain so
// the runtime row is explicitly rewritten (status, revoked_*, expires_at,
// limits_json, source upsert) instead of being left to the standalone refresh
// CASE, which only pins whatever status the row already has.
package authz

import (
	"context"
	"database/sql"
)

// runtimeProjection carries the grant-derived fields the runtime upsert
// writes. A nil Remark/ExpiresAt maps to SQL NULL exactly like the Node
// `input.remark ?? null` / `input.expiresAt ?? null` bindings; LimitsJSON is
// canonical limits JSON where "" means SQL NULL.
type runtimeProjection struct {
	Remark     *string
	ExpiresAt  *string
	LimitsJSON *string
}

// syncGrantRuntime mirrors syncResourceAuthorizationGrantRuntimeAsync
// (:995-1000): direct grants fan to the single runtime row, team grants to
// their member rows.
func (s *Store) syncGrantRuntime(ctx context.Context, tx *sql.Tx, grant *grantRow, actor, now string) error {
	if grant.GranteeType == "system_account" {
		return s.syncUserGrantRuntime(ctx, tx, grant, actor, now)
	}
	return s.syncTeamGrantRuntime(ctx, tx, grant, actor, now)
}

// syncUserGrantRuntime mirrors syncUserGrantRuntimeAsync (:1249-1332).
func (s *Store) syncUserGrantRuntime(ctx context.Context, tx *sql.Tx, grant *grantRow, actor, now string) error {
	if !grant.GranteeUserID.Valid || grant.GranteeUserID.String == "" {
		return nil
	}
	runtimeID, err := s.findRuntimeIDForUserGrant(ctx, tx, grant.ResourceType, grant.ResourceID, grant.GranteeUserID.String)
	if err != nil {
		return err
	}
	if grant.Status == StatusActive {
		// Node re-normalizes the stored limits before writing the runtime row
		// (parseRequestQuotaLimitsJson → normalizeRequestQuotaLimits →
		// requestQuotaLimitsJson, write.repository.ts:1157 and write-state
		// variant :776). An invalid stored document fails the same way.
		limits, err := normalizeAuthorizationLimitsJSON(grant.LimitsJSON.String)
		if err != nil {
			return err
		}
		projection := runtimeProjection{
			Remark:     nullStringPointer(grant.Remark),
			ExpiresAt:  nullStringPointer(grant.ExpiresAt),
			LimitsJSON: limits,
		}
		return s.upsertRuntimeForUser(ctx, tx, grant.ResourceType, grant.ResourceID, grant.OwnerID,
			grant.GranteeUserID.String, nil, projection, actor, now)
	}
	if runtimeID == "" {
		return nil
	}
	// paused | expired: explicit write-back (:1300-1313). revocations keep the
	// existing revoked_by/revoked_at when present (COALESCE), and a pause
	// clears them.
	if grant.Status == StatusPaused || grant.Status == StatusExpired {
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorizations")+`
			SET status = ?,
				expires_at = ?,
				limits_json = ?,
				revoked_by = CASE WHEN ? = 'expired' THEN COALESCE(revoked_by, ?) ELSE NULL END,
				revoked_at = CASE WHEN ? = 'expired' THEN COALESCE(revoked_at, ?) ELSE NULL END,
				revoked_reason = CASE WHEN ? = 'expired' THEN 'authorization_expired' ELSE 'authorization_paused' END,
				updated_at = ?
			WHERE id = ?`),
			grant.Status, nullableString(nullStringPointer(grant.ExpiresAt)), nullableString(limitsPointerOrNil(grant.LimitsJSON)),
			grant.Status, actor, grant.Status, now, grant.Status, now, runtimeID); err != nil {
			return err
		}
		return s.refreshEffectiveSource(ctx, tx, runtimeID, actor, now, refreshOptions{})
	}	// revoked | returned: revoke the manual sources (active or superseded) and
	// force the terminal state (:1315-1331). Team sources are left untouched,
	// so a runtime covered by a live team grant stays active.
	reason := "authorization_revoked"
	terminal := StatusRevoked
	if grant.Status == StatusReturned {
		reason = "grantee_returned"
		terminal = StatusReturned
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_sources")+`
		SET status = 'revoked',
			ended_at = COALESCE(ended_at, ?),
			ended_reason = COALESCE(ended_reason, ?),
			revoked_by = ?,
			revoked_at = ?,
			updated_at = ?
		WHERE authorization_id = ?
			AND source_type = 'manual'
			AND status IN ('active', 'superseded')`),
		now, reason, actor, now, now, runtimeID); err != nil {
		return err
	}
	noPreserve := false
	return s.refreshEffectiveSource(ctx, tx, runtimeID, actor, now, refreshOptions{
		noActiveSourceReason:              reason,
		preserveExpiredWhenNoActiveSource: &noPreserve,
		terminalStatus:                    terminal,
	})
}

// syncTeamGrantRuntime mirrors syncTeamGrantMemberAuthorizationsAsync
// (:1334-1390).
func (s *Store) syncTeamGrantRuntime(ctx context.Context, tx *sql.Tx, grant *grantRow, actor, now string) error {
	if !grant.GranteeTeamID.Valid || grant.GranteeTeamID.String == "" {
		return nil
	}
	teamID := grant.GranteeTeamID.String
	if grant.Status == StatusRevoked || grant.Status == StatusReturned {
		return s.revokeTeamGrantSources(ctx, tx, grant.ResourceType, grant.ResourceID, teamID, actor, now)
	}
	if grant.Status == StatusActive {
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
		for _, memberID := range members {
			if memberID == grant.OwnerID {
				continue
			}
			if err := s.upsertRuntimeForUser(ctx, tx, grant.ResourceType, grant.ResourceID, grant.OwnerID,
				memberID, &teamID, projection, actor, now); err != nil {
				return err
			}
		}
	}
	// Rows loop: only runtime rows still carried by an ACTIVE team source of
	// this team+resource are rewritten (claim #9 baseline, :1361-1372).
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT ra.id
		FROM `+s.table("resource_authorizations")+` ra
		INNER JOIN `+s.table("resource_authorization_sources")+` ras ON ras.authorization_id = ra.id
		WHERE ra.resource_type = ?
			AND ra.resource_id = ?
			AND ras.source_type = 'team'
			AND ras.source_team_id = ?
			AND ras.status = 'active'
		ORDER BY ra.id ASC
		LIMIT ?`), grant.ResourceType, grant.ResourceID, teamID, MaxTeamMembersPerTeam+1)
	if err != nil {
		return err
	}
	var runtimeIDs []string
	for rows.Next() {
		var runtimeID string
		if err := rows.Scan(&runtimeID); err != nil {
			rows.Close()
			return err
		}
		runtimeIDs = append(runtimeIDs, runtimeID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(runtimeIDs) > MaxTeamMembersPerTeam {
		return failf("授权团队来源展开超过当前系统上限，请先拆分团队或回收部分授权")
	}
	limits, err := normalizeAuthorizationLimitsJSON(grant.LimitsJSON.String)
	if err != nil {
		return err
	}
	for _, runtimeID := range runtimeIDs {
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorizations")+`
			SET expires_at = ?,
				revoked_by = CASE WHEN ? IN ('active', 'paused') THEN NULL ELSE ? END,
				revoked_at = CASE WHEN ? IN ('active', 'paused') THEN NULL ELSE ? END,
				revoked_reason = CASE WHEN ? = 'expired' THEN 'authorization_expired' WHEN ? = 'paused' THEN 'authorization_paused' ELSE NULL END,
				limits_json = ?,
				updated_at = ?
			WHERE id = ?`),
			nullableString(nullStringPointer(grant.ExpiresAt)), grant.Status, grant.RevokedBy,
			grant.Status, grant.RevokedAt, grant.Status, grant.Status,
			nullableString(limits), now, runtimeID); err != nil {
			return err
		}
		if err := s.refreshEffectiveSource(ctx, tx, runtimeID, actor, now, refreshOptions{}); err != nil {
			return err
		}
	}
	return nil
}

// revokeTeamGrantSources mirrors revokeTeamGrantSourcesAsync (:1392-1433):
// revoke the active team sources of one team+resource pair and refresh each
// affected runtime row with the default (preserve-expired) options.
func (s *Store) revokeTeamGrantSources(ctx context.Context, tx *sql.Tx, resourceType, resourceID, teamID, actor, now string) error {
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT ras.authorization_id
		FROM `+s.table("resource_authorization_sources")+` ras
		INNER JOIN `+s.table("resource_authorizations")+` ra ON ra.id = ras.authorization_id
		WHERE ras.source_type = 'team'
			AND ras.source_team_id = ?
			AND ras.status = 'active'
			AND ra.resource_type = ?
			AND ra.resource_id = ?
		ORDER BY ras.authorization_id ASC
		LIMIT ?`), teamID, resourceType, resourceID, MaxTeamMembersPerTeam+1)
	if err != nil {
		return err
	}
	var runtimeIDs []string
	for rows.Next() {
		var runtimeID string
		if err := rows.Scan(&runtimeID); err != nil {
			rows.Close()
			return err
		}
		runtimeIDs = append(runtimeIDs, runtimeID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(runtimeIDs) > MaxTeamMembersPerTeam {
		return failf("授权团队最多支持 %d 个成员，请先移除部分成员后再继续", MaxTeamMembersPerTeam)
	}
	for _, runtimeID := range runtimeIDs {
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_sources")+`
			SET status = 'revoked',
				ended_at = COALESCE(ended_at, ?),
				ended_reason = COALESCE(ended_reason, 'team_revoked'),
				revoked_by = ?,
				revoked_at = ?,
				updated_at = ?
			WHERE authorization_id = ?
				AND source_type = 'team'
				AND source_team_id = ?
				AND status = 'active'`), now, actor, now, now, runtimeID, teamID); err != nil {
			return err
		}
		if err := s.refreshEffectiveSource(ctx, tx, runtimeID, actor, now, refreshOptions{}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) findRuntimeIDForUserGrant(ctx context.Context, tx *sql.Tx, resourceType, resourceID, granteeUserID string) (string, error) {
	var runtimeID string
	err := tx.QueryRowContext(ctx, s.bind(`SELECT id FROM `+s.table("resource_authorizations")+`
		WHERE resource_type = ? AND resource_id = ? AND grantee_system_account_id = ?
		LIMIT 1`), resourceType, resourceID, granteeUserID).Scan(&runtimeID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return runtimeID, nil
}

// upsertRuntimeForUser mirrors upsertResourceAuthorizationForUserAsync
// (:1122-1247): runtime row upsert with explicit status/revoked/expires/limits
// write-back, source upsert, team-source supersede and effective-source
// refresh. sourceTeamID nil selects the manual source type.
func (s *Store) upsertRuntimeForUser(ctx context.Context, tx *sql.Tx, resourceType, resourceID, ownerID, granteeUserID string, sourceTeamID *string, projection runtimeProjection, actor, now string) error {
	if granteeUserID == ownerID {
		return failf("不能授权给资源所有者自己")
	}
	isTeamSource := sourceTeamID != nil
	var existingID string
	var existingLimits, existingRemark, existingRevokedBy, existingRevokedAt sql.NullString
	hasExisting := false
	err := tx.QueryRowContext(ctx, s.bind(`SELECT id, limits_json, remark, revoked_by, revoked_at
		FROM `+s.table("resource_authorizations")+`
		WHERE resource_type = ? AND resource_id = ? AND grantee_system_account_id = ?
		LIMIT 1`), resourceType, resourceID, granteeUserID).
		Scan(&existingID, &existingLimits, &existingRemark, &existingRevokedBy, &existingRevokedAt)
	if err == nil {
		hasExisting = true
	} else if err != sql.ErrNoRows {
		return err
	}
	hasActiveTeamSource := false
	var firstTeamSource any
	if hasExisting {
		hasActiveTeamSource, err = s.hasActiveTeamSourceForRuntime(ctx, tx, existingID, now)
		if err != nil {
			return err
		}
		if hasActiveTeamSource && !isTeamSource {
			firstTeamID, err := s.firstActiveTeamSourceID(ctx, tx, existingID, now)
			if err != nil {
				return err
			}
			if firstTeamID != "" {
				firstTeamSource = firstTeamID
			}
		}
	}
	// Node always passes the expiresAt key on this path (:1151-1153), so an
	// absent projection value maps to SQL NULL, not to "keep existing".
	nextExpires := nullableString(projection.ExpiresAt)
	nextStatus := StatusActive
	if text, ok := nextExpires.(string); ok && authorizationExpiresPassed(text, parseTimeOrNow(now)) {
		nextStatus = StatusExpired
	}
	// A manual write over a runtime carried by a live team source preserves the
	// team's limits (:1155-1157).
	var nextLimits any
	if !isTeamSource && hasActiveTeamSource {
		if existingLimits.Valid && existingLimits.String != "" {
			nextLimits = existingLimits.String
		} else {
			nextLimits = nil
		}
	} else {
		normalized, err := normalizeAuthorizationLimitsJSON(projection.LimitsJSONText())
		if err != nil {
			return err
		}
		nextLimits = nullableString(normalized)
	}
	nextRevokedBy, nextRevokedAt, nextRevokedReason := any(nil), any(nil), any(nil)
	if nextStatus == StatusExpired {
		if existingRevokedBy.Valid && existingRevokedBy.String != "" {
			nextRevokedBy = existingRevokedBy.String
		} else {
			nextRevokedBy = actor
		}
		if existingRevokedAt.Valid && existingRevokedAt.String != "" {
			nextRevokedAt = existingRevokedAt.String
		} else {
			nextRevokedAt = now
		}
		nextRevokedReason = "authorization_expired"
	}
	nextEffectiveType := "manual"
	if isTeamSource || hasActiveTeamSource {
		nextEffectiveType = "team"
	}
	var nextEffectiveTeamID any
	if isTeamSource {
		nextEffectiveTeamID = *sourceTeamID
	} else {
		nextEffectiveTeamID = firstTeamSource
	}
	nextRemark := any(nil)
	if projection.Remark != nil && *projection.Remark != "" {
		nextRemark = *projection.Remark
	}
	if !hasExisting {
		existingID = newRuntimeID(s.now)
		if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("resource_authorizations")+`
			(id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id,
			 scope, status, effective_source_type, effective_source_team_id, activated_at, last_source_changed_at,
			 remark, expires_at, limits_json, created_by, created_at, revoked_by, revoked_at, revoked_reason, updated_at)
			VALUES (?, ?, ?, ?, ?, 'use', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
			existingID, resourceType, resourceID, ownerID, granteeUserID,
			nextStatus, nextEffectiveType, nextEffectiveTeamID, now, now,
			nextRemark, nextExpires, nextLimits, actor, now, nextRevokedBy, nextRevokedAt, nextRevokedReason, now); err != nil {
			return err
		}
	} else {
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorizations")+`
			SET resource_owner_system_account_id = ?,
				status = ?,
				effective_source_type = COALESCE(?, effective_source_type),
				effective_source_team_id = ?,
				activated_at = COALESCE(activated_at, ?),
				last_source_changed_at = ?,
				remark = COALESCE(?, remark),
				expires_at = ?,
				limits_json = ?,
				revoked_by = ?,
				revoked_at = ?,
				revoked_reason = ?,
				updated_at = ?
			WHERE id = ?`),
			ownerID, nextStatus, nextEffectiveType, nextEffectiveTeamID, now, now,
			nextRemark, nextExpires, nextLimits, nextRevokedBy, nextRevokedAt, nextRevokedReason, now,
			existingID); err != nil {
			return err
		}
	}
	// Source upsert (:1224): team writes stay active, a manual write over a
	// team-carried runtime is recorded as superseded.
	requestedStatus := "active"
	if !isTeamSource && hasActiveTeamSource {
		requestedStatus = "superseded"
	}
	if err := s.upsertSourceForRuntime(ctx, tx, existingID, isTeamSource, sourceTeamID, actor, now, requestedStatus); err != nil {
		return err
	}
	if isTeamSource {
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_sources")+`
			SET status = 'superseded',
				ended_at = COALESCE(ended_at, ?),
				ended_reason = COALESCE(ended_reason, 'covered_by_team'),
				updated_at = ?
			WHERE authorization_id = ?
				AND source_type = 'manual'
				AND status = 'active'`), now, now, existingID); err != nil {
			return err
		}
	}
	return s.refreshEffectiveSource(ctx, tx, existingID, actor, now, refreshOptions{})
}

// upsertSourceForRuntime mirrors upsertResourceAuthorizationSourceAsync
// (:1448-1509): latest matching source row is reactivated/reactivated-with-
// clears, otherwise inserted with the requested status.
func (s *Store) upsertSourceForRuntime(ctx context.Context, tx *sql.Tx, authorizationID string, isTeamSource bool, sourceTeamID *string, actor, now, requestedStatus string) error {
	var sourceTypeAny any = "manual"
	var teamIDAny any
	if isTeamSource && sourceTeamID != nil {
		sourceTypeAny = "team"
		teamIDAny = *sourceTeamID
	}
	var existingID string
	err := tx.QueryRowContext(ctx, s.bind(`SELECT id FROM `+s.table("resource_authorization_sources")+`
		WHERE authorization_id = ? AND source_type = ? AND COALESCE(source_team_id, '') = COALESCE(?, '')
		ORDER BY created_at DESC, id DESC LIMIT 1`), authorizationID, sourceTypeAny, teamIDAny).Scan(&existingID)
	endedReason := any(nil)
	if requestedStatus == "superseded" {
		endedReason = "covered_by_team"
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_sources")+`
			SET status = ?,
				activated_at = COALESCE(activated_at, ?),
				ended_at = CASE WHEN ? = 'active' THEN NULL ELSE COALESCE(ended_at, ?) END,
				ended_reason = CASE WHEN ? = 'active' THEN NULL ELSE COALESCE(ended_reason, ?) END,
				revoked_by = CASE WHEN ? = 'active' THEN NULL ELSE revoked_by END,
				revoked_at = CASE WHEN ? = 'active' THEN NULL ELSE revoked_at END,
				updated_at = ?
			WHERE id = ?`),
			requestedStatus, now, requestedStatus, now, requestedStatus, endedReason,
			requestedStatus, requestedStatus, now, existingID)
		return err
	}
	if err != sql.ErrNoRows {
		return err
	}
	endedAt := any(nil)
	if requestedStatus != "active" {
		endedAt = now
	}
	_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("resource_authorization_sources")+`
		(id, authorization_id, source_type, source_team_id, status, activated_at, ended_at, ended_reason,
		 created_by, created_at, revoked_by, revoked_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?)`),
		newSourceID(s.now), authorizationID, sourceTypeAny, teamIDAny, requestedStatus, now, endedAt, endedReason,
		actor, now, now)
	return err
}

// hasActiveTeamSourceForRuntime mirrors hasActiveTeamAuthorizationSourceAsync
// (:1682-1700): an active team source backed by an active, unexpired team
// grant.
func (s *Store) hasActiveTeamSourceForRuntime(ctx context.Context, tx *sql.Tx, authorizationID, now string) (bool, error) {
	var id string
	err := tx.QueryRowContext(ctx, s.bind(`SELECT ras.id
		FROM `+s.table("resource_authorization_sources")+` ras
		INNER JOIN `+s.table("resource_authorizations")+` ra ON ra.id = ras.authorization_id
		INNER JOIN `+s.table("resource_authorization_grants")+` trg
			ON trg.resource_type = ra.resource_type
			AND trg.resource_id = ra.resource_id
			AND trg.grantee_type = 'team'
			AND trg.grantee_team_id = ras.source_team_id
			AND trg.status = 'active'
			AND (trg.expires_at IS NULL OR trg.expires_at > ?)
		WHERE ras.authorization_id = ?
			AND ras.source_type = 'team'
			AND ras.status = 'active'
		LIMIT 1`), now, authorizationID).Scan(&id)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// firstActiveTeamSourceID mirrors firstActiveTeamSourceIdAsync (:1702+).
func (s *Store) firstActiveTeamSourceID(ctx context.Context, tx *sql.Tx, authorizationID, now string) (string, error) {
	var teamID sql.NullString
	err := tx.QueryRowContext(ctx, s.bind(`SELECT ras.source_team_id
		FROM `+s.table("resource_authorization_sources")+` ras
		INNER JOIN `+s.table("resource_authorizations")+` ra ON ra.id = ras.authorization_id
		INNER JOIN `+s.table("resource_authorization_grants")+` trg
			ON trg.resource_type = ra.resource_type
			AND trg.resource_id = ra.resource_id
			AND trg.grantee_type = 'team'
			AND trg.grantee_team_id = ras.source_team_id
			AND trg.status = 'active'
			AND (trg.expires_at IS NULL OR trg.expires_at > ?)
		WHERE ras.authorization_id = ?
			AND ras.source_type = 'team'
			AND ras.status = 'active'
		ORDER BY ras.activated_at ASC, ras.created_at ASC, ras.id ASC
		LIMIT 1`), now, authorizationID).Scan(&teamID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return teamID.String, nil
}

// refreshOptions mirror the Node options object of
// refreshResourceAuthorizationEffectiveSourceAsync. A nil
// preserveExpiredWhenNoActiveSource keeps the Node default (true).
type refreshOptions struct {
	noActiveSourceReason              string
	preserveExpiredWhenNoActiveSource *bool
	terminalStatus                    string
}

func (o refreshOptions) preserveExpired() bool {
	if o.preserveExpiredWhenNoActiveSource == nil {
		return true
	}
	return *o.preserveExpiredWhenNoActiveSource
}

func (o refreshOptions) terminal() string {
	if o.terminalStatus == "" {
		return StatusRevoked
	}
	return o.terminalStatus
}

// refreshEffectiveSource mirrors
// refreshResourceAuthorizationEffectiveSourceAsync (:2026-2160). The four
// branches rewrite status, effective source, revoked_* and bookkeeping exactly
// as the Node CASE expressions do; the standalone call pins an existing
// status, which is why every mutation must pair it with the explicit sync
// write-back above.
func (s *Store) refreshEffectiveSource(ctx context.Context, tx *sql.Tx, authorizationID, actor, now string, options refreshOptions) error {
	// Active team source backed by an active, unexpired team grant (:2037-2065).
	var teamID sql.NullString
	err := tx.QueryRowContext(ctx, s.bind(`SELECT ras.source_team_id
		FROM `+s.table("resource_authorization_sources")+` ras
		INNER JOIN `+s.table("resource_authorizations")+` ra ON ra.id = ras.authorization_id
		INNER JOIN `+s.table("resource_authorization_grants")+` trg
			ON trg.resource_type = ra.resource_type
			AND trg.resource_id = ra.resource_id
			AND trg.grantee_type = 'team'
			AND trg.grantee_team_id = ras.source_team_id
			AND trg.status = 'active'
			AND (trg.expires_at IS NULL OR trg.expires_at > ?)
		WHERE ras.authorization_id = ?
			AND ras.source_type = 'team'
			AND ras.status = 'active'
		ORDER BY ras.activated_at ASC, ras.created_at ASC, ras.id ASC
		LIMIT 1`), now, authorizationID).Scan(&teamID)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == nil && teamID.String != "" {
		_, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorizations")+`
			SET status = CASE
					WHEN expires_at IS NOT NULL AND expires_at <= ? THEN 'expired'
					WHEN status = 'paused' THEN 'paused'
					ELSE 'active'
				END,
				effective_source_type = 'team',
				effective_source_team_id = ?,
				revoked_by = CASE WHEN expires_at IS NOT NULL AND expires_at <= ? THEN COALESCE(revoked_by, ?) ELSE NULL END,
				revoked_at = CASE WHEN expires_at IS NOT NULL AND expires_at <= ? THEN COALESCE(revoked_at, ?) ELSE NULL END,
				revoked_reason = CASE WHEN expires_at IS NOT NULL AND expires_at <= ? THEN 'authorization_expired' ELSE NULL END,
				last_source_changed_at = ?,
				updated_at = ?
			WHERE id = ?`),
			now, teamID.String, now, actor, now, now, now, now, now, authorizationID)
		return err
	}

	// Active team source whose team grant is paused (:2068-2098).
	err = tx.QueryRowContext(ctx, s.bind(`SELECT ras.source_team_id
		FROM `+s.table("resource_authorization_sources")+` ras
		INNER JOIN `+s.table("resource_authorizations")+` ra ON ra.id = ras.authorization_id
		INNER JOIN `+s.table("resource_authorization_grants")+` trg
			ON trg.resource_type = ra.resource_type
			AND trg.resource_id = ra.resource_id
			AND trg.grantee_type = 'team'
			AND trg.grantee_team_id = ras.source_team_id
			AND trg.status = 'paused'
		WHERE ras.authorization_id = ?
			AND ras.source_type = 'team'
			AND ras.status = 'active'
		ORDER BY ras.activated_at ASC, ras.created_at ASC, ras.id ASC
		LIMIT 1`), authorizationID).Scan(&teamID)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == nil && teamID.String != "" {
		_, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorizations")+`
			SET status = CASE
					WHEN expires_at IS NOT NULL AND expires_at <= ? THEN 'expired'
					ELSE 'paused'
				END,
				effective_source_type = 'team',
				effective_source_team_id = ?,
				revoked_by = CASE WHEN expires_at IS NOT NULL AND expires_at <= ? THEN COALESCE(revoked_by, ?) ELSE NULL END,
				revoked_at = CASE WHEN expires_at IS NOT NULL AND expires_at <= ? THEN COALESCE(revoked_at, ?) ELSE NULL END,
				revoked_reason = CASE WHEN expires_at IS NOT NULL AND expires_at <= ? THEN 'authorization_expired' ELSE 'authorization_paused' END,
				last_source_changed_at = ?,
				updated_at = ?
			WHERE id = ?`),
			now, teamID.String, now, actor, now, now, now, now, now, authorizationID)
		return err
	}

	// Active manual source (:2101-2128).
	var manualID string
	err = tx.QueryRowContext(ctx, s.bind(`SELECT id
		FROM `+s.table("resource_authorization_sources")+`
		WHERE authorization_id = ?
			AND source_type = 'manual'
			AND status = 'active'
		ORDER BY activated_at ASC, created_at ASC, id ASC
		LIMIT 1`), authorizationID).Scan(&manualID)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == nil && manualID != "" {
		_, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorizations")+`
			SET status = CASE
					WHEN expires_at IS NOT NULL AND expires_at <= ? THEN 'expired'
					WHEN status = 'paused' THEN 'paused'
					ELSE 'active'
				END,
				effective_source_type = 'manual',
				effective_source_team_id = NULL,
				revoked_by = CASE WHEN expires_at IS NOT NULL AND expires_at <= ? THEN COALESCE(revoked_by, ?) ELSE NULL END,
				revoked_at = CASE WHEN expires_at IS NOT NULL AND expires_at <= ? THEN COALESCE(revoked_at, ?) ELSE NULL END,
				revoked_reason = CASE WHEN expires_at IS NOT NULL AND expires_at <= ? THEN 'authorization_expired' ELSE NULL END,
				last_source_changed_at = ?,
				updated_at = ?
			WHERE id = ?`),
			now, now, actor, now, now, now, now, now, authorizationID)
		return err
	}

	// Terminal branch (:2131-2173).
	preserveExpired := 0
	if options.preserveExpired() {
		preserveExpired = 1
	}
	hasReason := 0
	noActiveSourceReason := any(nil)
	if options.noActiveSourceReason != "" {
		hasReason = 1
		noActiveSourceReason = options.noActiveSourceReason
	}
	terminalStatus := options.terminal()
	_, err = tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorizations")+`
		SET status = CASE WHEN ? = 1 AND expires_at IS NOT NULL AND expires_at <= ? THEN 'expired' ELSE ? END,
			effective_source_type = NULL,
			effective_source_team_id = NULL,
			revoked_by = CASE WHEN ? = 1 THEN ? ELSE COALESCE(revoked_by, ?) END,
			revoked_at = CASE WHEN ? = 1 THEN ? ELSE COALESCE(revoked_at, ?) END,
			revoked_reason = CASE
				WHEN ? = 1 AND expires_at IS NOT NULL AND expires_at <= ? THEN 'authorization_expired'
				WHEN ? = 1 THEN ?
				ELSE COALESCE(revoked_reason, 'no_active_source')
			END,
			last_source_changed_at = ?,
			updated_at = ?
		WHERE id = ?`),
		preserveExpired, now, terminalStatus,
		hasReason, actor, actor,
		hasReason, now, now,
		preserveExpired, now,
		hasReason, noActiveSourceReason,
		now, now, authorizationID)
	return err
}

func nullStringPointer(value sql.NullString) *string {
	if !value.Valid || value.String == "" {
		return nil
	}
	v := value.String
	return &v
}

func limitsPointerOrNil(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	v := value.String
	return &v
}

// LimitsJSONText renders a projection limits value for normalization; a nil
// pointer normalizes like Node's undefined limits (empty limits → NULL).
func (p runtimeProjection) LimitsJSONText() string {
	if p.LimitsJSON == nil {
		return ""
	}
	return *p.LimitsJSON
}

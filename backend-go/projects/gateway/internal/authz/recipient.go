// Receiver-side lifecycle: claim (accept), decline and return, plus the
// team/personal aggregate transitions those actions trigger. The simplified
// send-and-claim model has no revision checkpoints: claiming an account
// authorization provisions the instance and binds it into one of the
// grantee's own groups in the same transaction; claiming a group
// authorization takes effect immediately.
package authz

import (
	"context"
	"database/sql"
	"errors"
)

// AcceptInput carries the claim request. TargetGroupID selects the grantee's
// own group that receives an account authorization; empty falls back to the
// grantee's default group for the account's vendor.
type AcceptInput struct {
	TargetGroupID string
}

// AcceptResult is the claim response.
type AcceptResult struct {
	SourceID   string `json:"sourceId"`
	Status     string `json:"status"`
	InstanceID string `json:"instanceId,omitempty"`
	Bound      bool   `json:"bound"`
}

func (s *Store) loadRuntimeForSourceAccept(ctx context.Context, tx *sql.Tx, sourceID string) (*sourceRecord, *runtimeRecord, *offerRecord, error) {
	// Lock order is grant → runtime → source. Reading the two foreign keys
	// without a lock first lets the subsequent FOR UPDATE calls follow that
	// order without deadlocking against claim/revoke paths that update the
	// same rows.
	var grantID, authorizationID string
	err := tx.QueryRowContext(ctx, s.bind(`SELECT COALESCE(grant_id, ''), authorization_id
		FROM `+s.table("resource_authorization_sources")+` WHERE id = ?`), sourceID).
		Scan(&grantID, &authorizationID)
	if errors.Is(err, sql.ErrNoRows) || grantID == "" {
		return nil, nil, nil, failf("授权不存在")
	}
	if err != nil {
		return nil, nil, nil, err
	}
	grant, err := s.getOfferForUpdate(ctx, tx, grantID)
	if err != nil {
		return nil, nil, nil, err
	}
	if grant == nil {
		return nil, nil, nil, failf("授权不存在")
	}
	runtime, err := s.getRuntimeForUpdate(ctx, tx, authorizationID)
	if err != nil {
		return nil, nil, nil, err
	}
	if runtime == nil {
		return nil, nil, nil, failf("授权不存在")
	}
	source, err := s.getSourceForUpdate(ctx, tx, sourceID)
	if err != nil {
		return nil, nil, nil, err
	}
	if source == nil || source.GrantID != grantID || source.AuthorizationID != authorizationID {
		return nil, nil, nil, failf("授权不存在")
	}
	return source, runtime, grant, nil
}

func (s *Store) ensureResourceStillOwned(ctx context.Context, tx *sql.Tx, resourceType, resourceID, ownerID string) (bool, error) {
	switch resourceType {
	case "account":
		var currentOwner string
		err := tx.QueryRowContext(ctx, s.bind(`SELECT system_account_id FROM `+s.table("accounts")+`
			WHERE id = ? AND deleted_at IS NULL AND authorization_instance_authorization_id IS NULL`), resourceID).Scan(&currentOwner)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return currentOwner == ownerID, nil
	case "group":
		var currentOwner string
		err := tx.QueryRowContext(ctx, s.bind(`SELECT system_account_id FROM `+s.table("groups")+`
			WHERE id = ?`), resourceID).Scan(&currentOwner)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return currentOwner == ownerID, nil
	default:
		return false, nil
	}
}

// ClaimSource claims a pending offer. Group authorizations take effect
// immediately; account authorizations provision the instance and bind it into
// the grantee-selected group in the same transaction.
func (s *Store) ClaimSource(ctx context.Context, actorID, sourceID string, input AcceptInput, now string) (*AcceptResult, error) {
	ctx = ensureCtx(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	source, runtime, grant, err := s.loadRuntimeForSourceAccept(ctx, tx, sourceID)
	if err != nil {
		return nil, err
	}
	if runtime.GranteeID != actorID {
		return nil, failf("授权不存在")
	}
	if grant.Status == StatusPaused {
		return nil, failCode("state_conflict", "授权已暂停，请等待授权方恢复后再领取")
	}
	if source.Status != StatusPending {
		return nil, failCode("state_conflict", "该来源当前不可领取")
	}
	if grant.GranteeType == "system_account" {
		if grant.Status != StatusPending && grant.Status != StatusActive {
			return nil, failCode("state_conflict", "授权当前不可领取")
		}
	} else {
		if grant.Status != StatusPending && grant.Status != StatusActive {
			return nil, failCode("state_conflict", "授权当前不可领取")
		}
		// A team source can only be claimed by a current active member of an
		// active team; a missed member-removal cascade must not leave a stale
		// claim path open.
		var teamStatus string
		var teamDeleted sql.NullString
		if err := tx.QueryRowContext(ctx, s.bind(`SELECT status, deleted_at FROM `+s.table("system_teams")+`
			WHERE id = ?`), source.SourceTeamID).Scan(&teamStatus, &teamDeleted); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, failCode("team_not_found", "团队不存在或已停用")
			}
			return nil, err
		}
		if teamStatus != "active" || teamDeleted.Valid {
			return nil, failCode("team_not_found", "团队不存在或已停用")
		}
		var memberCount int
		if err := tx.QueryRowContext(ctx, s.bind(`SELECT count(*) FROM `+s.table("system_team_members")+`
			WHERE team_id = ? AND system_account_id = ? AND status = 'active'`),
			source.SourceTeamID, actorID).Scan(&memberCount); err != nil {
			return nil, err
		}
		if memberCount == 0 {
			return nil, failCode("team_not_found", "你已不是该团队成员，无法领取该授权")
		}
	}
	owned, err := s.ensureResourceStillOwned(ctx, tx, grant.ResourceType, grant.ResourceID, grant.OwnerID)
	if err != nil {
		return nil, err
	}
	if !owned {
		if err := s.revokeSourceForResourceChange(ctx, tx, source, runtime, grant, actorID, now); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		s.invalidateGovernance(ctx, invalidationReasonRevoked)
		return nil, failCode("resource_changed", "授权资源已变化，本次授权已撤销")
	}

	instanceID := ""
	bound := false
	if grant.ResourceType == "account" {
		provision := authorizedInstanceProvision{
			RuntimeAuthorizationID: runtime.ID,
			SourceAccountID:        grant.ResourceID,
			OwnerID:                grant.OwnerID,
			GranteeID:              runtime.GranteeID,
		}
		if input.TargetGroupID != "" {
			target := input.TargetGroupID
			provision.TargetGroupID = &target
		}
		instance, err := s.ensureAccountAuthorizationInstance(ctx, tx, provision, parseTimeOrNow(now), now)
		if err != nil {
			return nil, err
		}
		if instance == nil || instance.id == "" {
			return nil, failCode("source_unavailable", "来源账户不可用，无法完成领取")
		}
		if err := s.syncInstanceFromSource(ctx, tx, instance.id, grant.ResourceID, now); err != nil {
			return nil, err
		}
		// One step claim: create the instance and bind it into the grantee's
		// own group; an empty target falls back to the grantee's default group.
		if err := s.bindInstanceToGranteeGroup(ctx, tx, instance, provision, now); err != nil {
			return nil, err
		}
		instanceID = instance.id
		bound = true
	} else if grant.ResourceType == "group" {
		// The settings row's existence plus the runtime status is the whole
		// visibility contract for an authorized group.
		if err := s.recordGroupAuthorizationClaim(ctx, tx, runtime, now); err != nil {
			return nil, err
		}
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_sources")+`
		SET status = 'active', accepted_at = ?, accepted_by = ?,
			activated_at = COALESCE(activated_at, ?),
			declined_at = NULL, declined_by = NULL, returned_at = NULL, returned_by = NULL,
			ended_at = NULL, ended_reason = NULL, updated_at = ?
		WHERE id = ? AND status = 'pending'`), now, actorID, now, now, source.ID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_grants")+`
		SET status = 'active', updated_at = ?
		WHERE id = ? AND status IN ('pending','active')`), now, grant.ID); err != nil {
		return nil, err
	}
	if _, err := s.recomputeRuntime(ctx, tx, runtime.ID, actorID, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.invalidateGovernance(ctx, invalidationReasonUpdated)
	return &AcceptResult{SourceID: source.ID, Status: StatusActive, InstanceID: instanceID, Bound: bound}, nil
}

// AcceptSource keeps the historical entry point name for existing callers.
func (s *Store) AcceptSource(ctx context.Context, actorID, sourceID string, input AcceptInput, now string) (*AcceptResult, error) {
	return s.ClaimSource(ctx, actorID, sourceID, input, now)
}

func (s *Store) revokeSourceForResourceChange(ctx context.Context, tx *sql.Tx, source *sourceRecord,
	runtime *runtimeRecord, grant *offerRecord, actor, now string) error {
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_sources")+`
		SET status = 'revoked', ended_at = ?, ended_reason = 'resource_changed', updated_at = ?
		WHERE id = ? AND status IN ('pending','active','paused')`), now, now, source.ID); err != nil {
		return err
	}
	if grant.GranteeType == "system_account" {
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_grants")+`
			SET status = 'revoked', ended_at = ?, ended_reason = 'resource_changed', updated_at = ?
			WHERE id = ? AND status NOT IN ('revoked','returned','declined','expired')`), now, now, grant.ID); err != nil {
			return err
		}
	}
	if _, err := s.recomputeRuntime(ctx, tx, runtime.ID, actor, now); err != nil {
		return err
	}
	return nil
}

// syncInstanceFromSource refreshes the static configuration the instance
// inherits and reactivates a previously disabled instance on re-claim.
func (s *Store) syncInstanceFromSource(ctx context.Context, tx *sql.Tx, instanceID, sourceAccountID, now string) error {
	_, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("accounts")+` inst
		SET vendor_code = NULLIF(src.vendor_code, ''),
			connection_driver_code = src.connection_driver_code,
			protocol_code = src.protocol_code,
			protocol_version = src.protocol_version,
			type = src.type,
			capability_domain = COALESCE(src.capability_domain, 'chat'),
			background_probe_enabled = COALESCE(src.background_probe_enabled, 1),
			temporary_unavailable_continuous_probe_enabled = COALESCE(src.temporary_unavailable_continuous_probe_enabled, 0),
			health_check_model = COALESCE(src.health_check_model, ''),
			health_check_endpoint_mode = COALESCE(src.health_check_endpoint_mode, ''),
			status = 'active', schedulable = 1, updated_at = ?
		FROM `+s.table("accounts")+` src
		WHERE inst.id = ? AND src.id = ? AND src.deleted_at IS NULL`), now, instanceID, sourceAccountID)
	return err
}

// recordGroupAuthorizationClaim upserts the group authorization settings row
// for a claimed group grant.
func (s *Store) recordGroupAuthorizationClaim(ctx context.Context, tx *sql.Tx, runtime *runtimeRecord, now string) error {
	_, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("group_authorization_settings")+`
		(authorization_id, system_account_id, group_id, group_type, created_at, updated_at)
		VALUES (?, ?, ?, 'personal', ?, ?)
		ON CONFLICT (authorization_id) DO UPDATE SET updated_at = excluded.updated_at`),
		runtime.ID, runtime.GranteeID, runtime.ResourceID, now, now)
	return err
}

func (s *Store) updatePersonalGrantAggregate(ctx context.Context, tx *sql.Tx, grantID, actor, now string) error {
	if grantID == "" {
		return nil
	}
	var live int
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT count(*) FROM `+s.table("resource_authorization_sources")+`
		WHERE grant_id = ? AND status IN ('pending','active','paused')`), grantID).Scan(&live); err != nil {
		return err
	}
	if live > 0 {
		return nil
	}
	var status, endedAt, endedReason string
	err := tx.QueryRowContext(ctx, s.bind(`SELECT status, COALESCE(ended_at, ''), COALESCE(ended_reason, '')
		FROM `+s.table("resource_authorization_sources")+`
		WHERE grant_id = ? ORDER BY COALESCE(ended_at, '') DESC, id DESC LIMIT 1`), grantID).
		Scan(&status, &endedAt, &endedReason)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_grants")+`
		SET status = ?, ended_at = COALESCE(NULLIF(?, ''), ?), ended_reason = COALESCE(NULLIF(?, ''), 'all_sources_ended'),
			updated_at = ?
		WHERE id = ? AND status NOT IN ('revoked','returned','declined','expired')`),
		status, endedAt, now, endedReason, now, grantID)
	return err
}

// recomputeTeamGrantAggregate derives the team grant status from its member
// sources: a grant is terminal only when every member source is terminal.
// The caller must hold the grant row lock.
func (s *Store) recomputeTeamGrantAggregate(ctx context.Context, tx *sql.Tx, grant *offerRecord, actor, now string) error {
	if grant.GranteeType != "team" {
		return nil
	}
	switch grant.Status {
	case StatusPending, StatusActive, StatusPaused:
	default:
		return nil
	}
	type agg struct {
		status       string
		reason       string
		claimed      bool
		expiredCycle bool
	}
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT status, COALESCE(ended_reason, ''),
			COALESCE(accepted_at, '') <> '',
			expiry_cycle = ?
		FROM `+s.table("resource_authorization_sources")+`
		WHERE grant_id = ? ORDER BY id`), grant.ExpiryCycle, grant.ID)
	if err != nil {
		return err
	}
	var items []agg
	live := 0
	currentCycle := 0
	for rows.Next() {
		var status, reason string
		var claimed, current bool
		if err := rows.Scan(&status, &reason, &claimed, &current); err != nil {
			rows.Close()
			return err
		}
		items = append(items, agg{status: status, reason: reason, claimed: claimed, expiredCycle: current})
		if current {
			currentCycle++
		}
		if isLiveStatus(status) {
			live++
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if live > 0 {
		return nil
	}
	if currentCycle == 0 {
		if grant.Status == StatusActive || grant.Status == StatusPaused {
			return s.terminalTeamGrant(ctx, tx, grant, StatusRevoked, "no_sources", actor, now)
		}
		return nil
	}
	allExpired, allRevoked, allDeclined, allRemoved := true, true, true, true
	everClaimed := false
	resourceDeleted := ""
	for _, item := range items {
		if !item.expiredCycle {
			continue
		}
		if item.claimed {
			everClaimed = true
		}
		if item.status != StatusExpired {
			allExpired = false
		}
		if item.status != StatusRevoked {
			allRevoked = false
		}
		if item.status != StatusDeclined {
			allDeclined = false
		}
		if item.status != StatusRevoked || item.reason != "member_removed" {
			allRemoved = false
		}
		if item.status == StatusRevoked && (item.reason == "source_deleted" || item.reason == "group_deleted") {
			resourceDeleted = item.reason
		}
	}
	switch {
	case resourceDeleted != "":
		return s.terminalTeamGrant(ctx, tx, grant, StatusRevoked, resourceDeleted, actor, now)
	case allRemoved:
		return s.terminalTeamGrant(ctx, tx, grant, StatusDeclined, "all_members_removed", actor, now)
	case allExpired:
		return s.terminalTeamGrant(ctx, tx, grant, StatusExpired, "authorization_expired", actor, now)
	case everClaimed:
		return s.terminalTeamGrant(ctx, tx, grant, StatusDeclined, "all_members_ended", actor, now)
	case allDeclined:
		return s.terminalTeamGrant(ctx, tx, grant, StatusDeclined, "all_members_declined", actor, now)
	case allRevoked:
		return s.terminalTeamGrant(ctx, tx, grant, StatusRevoked, "all_members_revoked", actor, now)
	default:
		return s.terminalTeamGrant(ctx, tx, grant, StatusDeclined, "all_members_ended", actor, now)
	}
}

func (s *Store) terminalTeamGrant(ctx context.Context, tx *sql.Tx, grant *offerRecord, status, reason, actor, now string) error {
	_, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_grants")+`
		SET status = ?, ended_at = COALESCE(ended_at, ?), ended_reason = COALESCE(ended_reason, ?),
			updated_at = ?
		WHERE id = ? AND status IN ('pending','active','paused')`),
		status, now, reason, now, grant.ID)
	if err != nil {
		return err
	}
	grant.Status = status
	grant.EndedAt = now
	grant.EndedReason = reason
	_ = actor
	return nil
}

// DeclineSource rejects a pending member offer.
func (s *Store) DeclineSource(ctx context.Context, actorID, sourceID, now string) error {
	ctx = ensureCtx(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	source, runtime, grant, err := s.loadRuntimeForSourceAccept(ctx, tx, sourceID)
	if err != nil {
		return err
	}
	if runtime.GranteeID != actorID {
		return failf("授权不存在")
	}
	if source.Status != StatusPending {
		return failCode("state_conflict", "该来源当前不可拒绝")
	}
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_sources")+`
		SET status = 'declined', declined_at = ?, declined_by = ?, ended_at = ?,
			ended_reason = 'source_declined', updated_at = ?
		WHERE id = ? AND status = 'pending'`), now, actorID, now, now, source.ID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return &Conflict{}
	}
	if grant.GranteeType == "system_account" {
		if err := s.updatePersonalGrantAggregate(ctx, tx, grant.ID, actorID, now); err != nil {
			return err
		}
	} else {
		if err := s.recomputeTeamGrantAggregate(ctx, tx, grant, actorID, now); err != nil {
			return err
		}
	}
	if _, err := s.recomputeRuntime(ctx, tx, runtime.ID, actorID, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.invalidateGovernance(ctx, invalidationReasonUpdated)
	return nil
}

// ReturnSourceForGrantee is the compatibility bridge used by the
// account/group return endpoints.
func (s *Store) ReturnSourceForGrantee(ctx context.Context, sourceID, actorID string) error {
	return s.ReturnSource(ctx, actorID, sourceID, formatTime(s.now().UTC()))
}

// ReturnSource returns a claimed source; bindings are disabled only if the
// runtime actually becomes terminal.
func (s *Store) ReturnSource(ctx context.Context, actorID, sourceID, now string) error {
	ctx = ensureCtx(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	source, runtime, grant, err := s.loadRuntimeForSourceAccept(ctx, tx, sourceID)
	if err != nil {
		return err
	}
	if runtime.GranteeID != actorID {
		return failf("授权不存在")
	}
	returnable := source.Status == StatusActive ||
		(grant.Status == StatusPaused && source.Status == StatusPending)
	if !returnable {
		return failCode("state_conflict", "该来源当前不可归还")
	}
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_sources")+`
		SET status = 'returned', returned_at = ?, returned_by = ?, ended_at = ?,
			ended_reason = 'source_returned', updated_at = ?
		WHERE id = ? AND status IN ('pending','active')`), now, actorID, now, now, source.ID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return &Conflict{}
	}
	if grant.GranteeType == "system_account" {
		if err := s.updatePersonalGrantAggregate(ctx, tx, grant.ID, actorID, now); err != nil {
			return err
		}
	} else {
		if err := s.recomputeTeamGrantAggregate(ctx, tx, grant, actorID, now); err != nil {
			return err
		}
	}
	if _, err := s.recomputeRuntime(ctx, tx, runtime.ID, actorID, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.invalidateGovernance(ctx, invalidationReasonUpdated)
	return nil
}

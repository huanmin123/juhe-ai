// Receiver-side lifecycle: accept/reconfirm, decline, return, block, plus the
// team/personal aggregate transitions those actions trigger (design §5.2-§5.4,
// §4.2/§4.4).
package authz

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// AcceptInput carries the revision the receiver actually reviewed.
type AcceptInput struct {
	ExpectedRevision       string
	ExpectedRevisionVersion int
	RetainFallback         *bool
}

// AcceptResult is the acceptance response.
type AcceptResult struct {
	SourceID         string `json:"sourceId"`
	Status           string `json:"status"`
	InstanceID       string `json:"instanceId,omitempty"`
	SnapshotRevision string `json:"snapshotRevision,omitempty"`
	Bound            bool   `json:"bound"`
}

func (s *Store) loadRuntimeForSourceAccept(ctx context.Context, tx *sql.Tx, sourceID string) (*sourceRecord, *runtimeRecord, *offerRecord, error) {
	// Lock order is grant → runtime → source (design §5.3). Reading the two
	// foreign keys without a lock first lets the subsequent FOR UPDATE calls
	// follow that order without deadlocking against accept/reconfirm/revoke
	// paths that update the same rows.
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
			WHERE id = ? AND group_kind = 'standard'`), resourceID).Scan(&currentOwner)
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

func (s *Store) enforceAcceptanceLimits(ctx context.Context, tx *sql.Tx, runtime *runtimeRecord, now string) error {
	var active, pending, instances int
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT count(*) FROM `+s.table("resource_authorization_sources")+` s
		JOIN `+s.table("resource_authorizations")+` ra ON ra.id = s.authorization_id
		WHERE ra.grantee_system_account_id = ? AND s.status IN ('active','dormant','needs_review')`),
		runtime.GranteeID).Scan(&active); err != nil {
		return err
	}
	if active >= MaxActiveSourcesPerGrantee {
		return errOfferSuppressed
	}
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT count(*) FROM `+s.table("resource_authorization_sources")+` s
		JOIN `+s.table("resource_authorizations")+` ra ON ra.id = s.authorization_id
		WHERE ra.grantee_system_account_id = ? AND s.status = 'pending'`), runtime.GranteeID).Scan(&pending); err != nil {
		return err
	}
	if pending > MaxPendingSourcesPerGrantee {
		return errOfferSuppressed
	}
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT count(*) FROM `+s.table("accounts")+`
		WHERE system_account_id = ? AND authorization_instance_authorization_id IS NOT NULL AND deleted_at IS NULL`),
		runtime.GranteeID).Scan(&instances); err != nil {
		return err
	}
	if instances > MaxAuthorizedInstancesPerGrantee {
		return errOfferSuppressed
	}
	_ = now
	return nil
}

// AcceptSource accepts a pending offer or re-confirms a material-changed one.
func (s *Store) AcceptSource(ctx context.Context, actorID, sourceID string, input AcceptInput, now string) (*AcceptResult, error) {
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
	if grant.Status == StatusPendingOwnerApproval {
		return nil, failf("授权不存在")
	}
	if grant.Status == StatusPaused {
		return nil, failCode("state_conflict", "授权已暂停，请等待授权方恢复后再确认")
	}
	if source.Status != StatusPending && source.Status != StatusNeedsReview {
		return nil, failCode("state_conflict", "该来源当前不可确认")
	}
	reconfirmation := source.Status == StatusNeedsReview
	if reconfirmation && input.RetainFallback != nil && !*input.RetainFallback {
		return nil, failf("重新确认不能改变个人回退选择")
	}
	if grant.GranteeType == "system_account" {
		if grant.Status != StatusPending && grant.Status != StatusActive {
			return nil, failCode("state_conflict", "授权当前不可确认")
		}
	} else {
		if grant.Status != StatusPendingMemberAcceptance && grant.Status != StatusActive {
			return nil, failCode("state_conflict", "授权当前不可确认")
		}
		// A team source can only be accepted by a current active member of an
		// active team; a missed member-removal cascade must not leave a stale
		// acceptance path open.
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
			return nil, failCode("team_not_found", "你已不是该团队成员，无法接受该授权")
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
	currentRevision, currentVersion, err := s.sourceCurrentRevision(ctx, tx, grant.ResourceType, grant.ResourceID, grant.OwnerID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(input.ExpectedRevision) == "" ||
		input.ExpectedRevision != source.ObservedSourceRevision ||
		input.ExpectedRevision != currentRevision ||
		input.ExpectedRevisionVersion != source.RevisionVersion ||
		currentVersion != source.RevisionVersion {
		if source.ObservedSourceRevision != currentRevision || source.RevisionVersion != currentVersion {
			if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_sources")+`
				SET observed_source_revision = ?, revision_version = ?, updated_at = ? WHERE id = ?`),
				currentRevision, currentVersion, now, source.ID); err != nil {
				return nil, err
			}
			if err := tx.Commit(); err != nil {
				return nil, err
			}
		}
		return nil, failCode("offer_stale_source_changed", "授权来源已变化，请重新查看后再确认")
	}
	if blocked, err := s.isBlocked(ctx, tx, actorID, grant.OwnerID, grant.ResourceType, grant.ResourceID); err != nil {
		return nil, err
	} else if blocked {
		return nil, errOfferSuppressed
	}
	if err := s.enforceAcceptanceLimits(ctx, tx, runtime, now); err != nil {
		return nil, err
	}

	instanceID := ""
	if grant.ResourceType == "account" {
		instance, err := s.ensureAccountAuthorizationInstance(ctx, tx, authorizedInstanceProvision{
			RuntimeAuthorizationID: runtime.ID,
			SourceAccountID:        grant.ResourceID,
			OwnerID:                grant.OwnerID,
			GranteeID:              runtime.GranteeID,
		}, parseTimeOrNow(now), now)
		if err != nil {
			return nil, err
		}
		if instance == nil || instance.id == "" {
			return nil, failCode("source_unavailable", "来源账户不可用，无法完成接受")
		}
		if err := s.syncInstanceFromSource(ctx, tx, instance.id, grant.ResourceID, now); err != nil {
			return nil, err
		}
		instanceID = instance.id
	} else if grant.ResourceType == "group" {
		if err := s.ensureGroupSnapshotForAccept(ctx, tx, runtime, currentRevision, currentVersion, now); err != nil {
			return nil, err
		}
	}
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_sources")+`
		SET status = 'active', accepted_source_revision = ?, accepted_source_revision_version = ?,
			accepted_at = ?, accepted_by = ?, activated_at = COALESCE(activated_at, ?),
			needs_review_at = NULL, needs_review_reason = NULL, needs_review_detail_json = NULL,
			declined_at = NULL, declined_by = NULL, returned_at = NULL, returned_by = NULL,
			ended_at = NULL, ended_reason = NULL, pending_expires_at = NULL,
			acceptance_mode = 'recipient', updated_at = ?
		WHERE id = ? AND status IN ('pending','needs_review') AND observed_source_revision = ? AND revision_version = ?`),
		currentRevision, currentVersion, now, actorID, now, now, source.ID,
		currentRevision, currentVersion)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return nil, failCode("offer_stale_source_changed", "授权来源已变化，请重新查看后再确认")
	}
	if grant.GranteeType == "system_account" {
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_grants")+`
			SET status = 'active', accepted_at = COALESCE(accepted_at, ?), accepted_by = COALESCE(accepted_by, ?),
				acceptance_mode = 'recipient', pending_expires_at = NULL, updated_at = ?
			WHERE id = ? AND status IN ('pending','active')`), now, actorID, now, grant.ID); err != nil {
			return nil, err
		}
	} else if grant.Status == StatusPendingMemberAcceptance {
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_grants")+`
			SET status = 'active', accepted_at = COALESCE(accepted_at, ?), accepted_by = COALESCE(accepted_by, ?),
				pending_expires_at = NULL, updated_at = ? WHERE id = ? AND status = 'pending_member_acceptance'`),
			now, actorID, now, grant.ID); err != nil {
			return nil, err
		}
	}
	if grant.GranteeType == "team" && input.RetainFallback != nil && !*input.RetainFallback {
		if err := s.returnManualFallbackForTeamAccept(ctx, tx, runtime, actorID, now); err != nil {
			return nil, err
		}
	}
	if _, err := s.recomputeRuntime(ctx, tx, runtime.ID, actorID, now); err != nil {
		return nil, err
	}
	if err := s.syncRuntimeQuotaBindings(ctx, tx, grant, now); err != nil {
		return nil, err
	}
	target := grant.OwnerID
	if grant.GranteeType == "team" {
		_ = tx.QueryRowContext(ctx, s.bind(`SELECT created_by FROM `+s.table("system_teams")+` WHERE id = ?`), grant.GranteeTeamID).Scan(&target)
	}
	if err := s.insertNotification(ctx, tx, target, "member_offer_accepted", grant.ID, source.ID,
		map[string]any{"resourceType": grant.ResourceType, "resourceId": grant.ResourceID},
		"accepted:"+source.ID, now); err != nil {
		return nil, err
	}
	resultStatus := StatusActive
	updated, err := s.getSourceForUpdate(ctx, tx, source.ID)
	if err != nil {
		return nil, err
	}
	if updated != nil {
		resultStatus = updated.Status
	}
	snapshotRevision := ""
	if grant.ResourceType == "group" {
		_, _, snapshotRevision, _, err = s.settingsRow(ctx, tx, runtime.ID, false)
		if err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.invalidateGovernance(ctx, invalidationReasonUpdated)
	return &AcceptResult{SourceID: source.ID, Status: resultStatus, InstanceID: instanceID,
		SnapshotRevision: snapshotRevision, Bound: false}, nil
}

func (s *Store) revokeSourceForResourceChange(ctx context.Context, tx *sql.Tx, source *sourceRecord,
	runtime *runtimeRecord, grant *offerRecord, actor, now string) error {
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_sources")+`
		SET status = 'revoked', ended_at = ?, ended_reason = 'resource_changed', updated_at = ?
		WHERE id = ? AND status IN ('pending','active','dormant','needs_review')`), now, now, source.ID); err != nil {
		return err
	}
	if grant.GranteeType == "system_account" {
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_grants")+`
			SET status = 'revoked', ended_at = ?, ended_reason = 'resource_changed', pending_expires_at = NULL, updated_at = ?
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
// inherits and reactivates a previously disabled instance on re-acceptance.
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

func (s *Store) returnManualFallbackForTeamAccept(ctx context.Context, tx *sql.Tx, runtime *runtimeRecord, actor, now string) error {
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT id, COALESCE(grant_id, '') FROM `+s.table("resource_authorization_sources")+`
		WHERE authorization_id = ? AND source_type = 'manual'
		  AND status IN ('active','dormant','needs_review')
		  AND COALESCE(accepted_source_revision, '') <> ''
		ORDER BY id`), runtime.ID)
	if err != nil {
		return err
	}
	type item struct{ sourceID, grantID string }
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.sourceID, &it.grantID); err != nil {
			rows.Close()
			return err
		}
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, it := range items {
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_sources")+`
			SET status = 'returned', ended_at = ?, ended_reason = 'fallback_declined', updated_at = ?
			WHERE id = ? AND status IN ('active','dormant','needs_review')`), now, now, it.sourceID); err != nil {
			return err
		}
		if err := s.updatePersonalGrantAggregate(ctx, tx, it.grantID, actor, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) updatePersonalGrantAggregate(ctx context.Context, tx *sql.Tx, grantID, actor, now string) error {
	if grantID == "" {
		return nil
	}
	var live int
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT count(*) FROM `+s.table("resource_authorization_sources")+`
		WHERE grant_id = ? AND status IN ('pending','active','dormant','needs_review')`), grantID).Scan(&live); err != nil {
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
			pending_expires_at = NULL, updated_at = ?
		WHERE id = ? AND status NOT IN ('revoked','returned','declined','expired')`),
		status, endedAt, now, endedReason, now, grantID)
	return err
}

// recomputeTeamGrantAggregate applies design §4.2 item 2 for the current
// expiry cycle. The caller must hold the grant row lock.
func (s *Store) recomputeTeamGrantAggregate(ctx context.Context, tx *sql.Tx, grant *offerRecord, actor, now string) error {
	if grant.GranteeType != "team" {
		return nil
	}
	switch grant.Status {
	case StatusPendingMemberAcceptance, StatusActive, StatusPaused:
	default:
		return nil
	}
	type agg struct {
		status        string
		reason        string
		accepted      bool
		expiredCycle  bool
	}
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT status, COALESCE(ended_reason, ''),
			COALESCE(accepted_source_revision, '') <> '' OR COALESCE(accepted_at, '') <> '',
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
		var accepted, current bool
		if err := rows.Scan(&status, &reason, &accepted, &current); err != nil {
			rows.Close()
			return err
		}
		items = append(items, agg{status: status, reason: reason, accepted: accepted, expiredCycle: current})
		if current {
			currentCycle++
		}
		if isLiveStatus(status) || status == StatusNeedsReview {
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
	everAccepted := false
	resourceDeleted := ""
	for _, item := range items {
		if !item.expiredCycle {
			continue
		}
		if item.accepted {
			everAccepted = true
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
		return s.terminalTeamGrant(ctx, tx, grant, StatusExpired, "all_member_offers_expired", actor, now)
	case everAccepted:
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
			pending_expires_at = NULL, updated_at = ?
		WHERE id = ? AND status IN ('pending_member_acceptance','active','paused')`),
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

// DeclineSource rejects a pending/needs-review member offer.
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
	if runtime.GranteeID != actorID || grant.Status == StatusPendingOwnerApproval {
		return failf("授权不存在")
	}
	if source.Status != StatusPending && source.Status != StatusNeedsReview {
		return failCode("state_conflict", "该来源当前不可拒绝")
	}
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_sources")+`
		SET status = 'declined', declined_at = ?, declined_by = ?, ended_at = ?,
			ended_reason = 'source_declined', pending_expires_at = NULL, updated_at = ?
		WHERE id = ? AND status IN ('pending','needs_review')`), now, actorID, now, now, source.ID)
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
	granteeType, granteeUserID, teamID, memberID := "system_account", actorID, "", ""
	if grant.GranteeType == "team" {
		granteeType, granteeUserID, teamID, memberID = "team_member", "", grant.GranteeTeamID, actorID
	}
	if err := s.upsertCooldown(ctx, tx, grant.OwnerID, granteeType, granteeUserID, teamID, memberID,
		grant.ResourceType, grant.ResourceID, "source_declined", now); err != nil {
		return err
	}
	if _, err := s.recomputeRuntime(ctx, tx, runtime.ID, actorID, now); err != nil {
		return err
	}
	target := grant.OwnerID
	if grant.GranteeType == "team" {
		_ = tx.QueryRowContext(ctx, s.bind(`SELECT created_by FROM `+s.table("system_teams")+` WHERE id = ?`), grant.GranteeTeamID).Scan(&target)
	}
	if err := s.insertNotification(ctx, tx, target, "member_offer_declined", grant.ID, source.ID, nil,
		"declined:"+source.ID, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.invalidateGovernance(ctx, invalidationReasonUpdated)
	return nil
}

// ReturnSourceForGrantee is the compatibility bridge used by the legacy
// account/group return endpoints. It delegates to the source-level state
// machine so the old endpoint cannot return a whole grant with multiple
// non-terminal sources.
func (s *Store) ReturnSourceForGrantee(ctx context.Context, sourceID, actorID string) error {
	return s.ReturnSource(ctx, actorID, sourceID, formatTime(s.now().UTC()))
}

// ReturnSource returns an accepted (or paused-parent) source; bindings are
// disabled only if the runtime actually becomes terminal.
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
	if runtime.GranteeID != actorID || grant.Status == StatusPendingOwnerApproval {
		return failf("授权不存在")
	}
	returnable := source.Status == StatusActive || source.Status == StatusDormant || source.Status == StatusNeedsReview ||
		(grant.Status == StatusPaused && (source.Status == StatusPending || source.Status == StatusActive ||
			source.Status == StatusDormant || source.Status == StatusNeedsReview))
	if !returnable {
		return failCode("state_conflict", "该来源当前不可归还")
	}
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_sources")+`
		SET status = 'returned', returned_at = ?, returned_by = ?, ended_at = ?,
			ended_reason = 'source_returned', pending_expires_at = NULL, updated_at = ?
		WHERE id = ? AND status IN ('pending','active','dormant','needs_review')`), now, actorID, now, now, source.ID)
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
	granteeType, granteeUserID, teamID, memberID := "system_account", actorID, "", ""
	if grant.GranteeType == "team" {
		granteeType, granteeUserID, teamID, memberID = "team_member", "", grant.GranteeTeamID, actorID
	}
	if err := s.upsertCooldown(ctx, tx, grant.OwnerID, granteeType, granteeUserID, teamID, memberID,
		grant.ResourceType, grant.ResourceID, "source_returned", now); err != nil {
		return err
	}
	if _, err := s.recomputeRuntime(ctx, tx, runtime.ID, actorID, now); err != nil {
		return err
	}
	target := grant.OwnerID
	if grant.GranteeType == "team" {
		_ = tx.QueryRowContext(ctx, s.bind(`SELECT created_by FROM `+s.table("system_teams")+` WHERE id = ?`), grant.GranteeTeamID).Scan(&target)
	}
	if err := s.insertNotification(ctx, tx, target, "member_offer_returned", grant.ID, source.ID, nil,
		"returned:"+source.ID, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.invalidateGovernance(ctx, invalidationReasonUpdated)
	return nil
}

// BlockInput selects the block scope and whether existing sources are returned.
type BlockInput struct {
	Scope         string `json:"scope"` // grantor | resource
	ReturnExisting bool  `json:"returnExisting"`
	Reason        string `json:"reason"`
}

// BlockResult reports what the block withdrew.
type BlockResult struct {
	BlockID          string `json:"blockId"`
	WithdrawnPending int    `json:"withdrawnPending"`
	ReturnedExisting int    `json:"returnedExisting"`
}

func (s *Store) BlockSource(ctx context.Context, actorID, sourceID string, input BlockInput, now string) (*BlockResult, error) {
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
	_ = source
	if runtime.GranteeID != actorID || grant.Status == StatusPendingOwnerApproval {
		return nil, failf("授权不存在")
	}
	scope := strings.TrimSpace(input.Scope)
	if scope == "" {
		scope = "grantor"
	}
	var blockAccount, blockResourceType, blockResourceID any
	switch scope {
	case "grantor":
		blockAccount = grant.OwnerID
	case "resource":
		// Schema CHECK requires resource-scope blocks to carry the resource
		// owner as blocked_system_account_id as well.
		blockAccount = grant.OwnerID
		blockResourceType = grant.ResourceType
		blockResourceID = grant.ResourceID
	default:
		return nil, failf("屏蔽范围无效")
	}
	blockID := "blk_" + randomSuffix()
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("resource_authorization_blocks")+`
		(id, blocker_system_account_id, blocked_system_account_id, blocked_resource_type, blocked_resource_id,
		 reason, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`),
		blockID, actorID, blockAccount, blockResourceType, blockResourceID,
		nullIfEmpty(input.Reason), now, now); err != nil {
		return nil, err
	}
	// Reuse the persisted row id when the same-scope block already existed.
	var lookupErr error
	if scope == "grantor" {
		lookupErr = tx.QueryRowContext(ctx, s.bind(`SELECT id FROM `+s.table("resource_authorization_blocks")+`
			WHERE blocker_system_account_id = ? AND blocked_system_account_id = ?
			  AND blocked_resource_type IS NULL AND blocked_resource_id IS NULL
			LIMIT 1`), actorID, blockAccount).Scan(&blockID)
	} else {
		lookupErr = tx.QueryRowContext(ctx, s.bind(`SELECT id FROM `+s.table("resource_authorization_blocks")+`
			WHERE blocker_system_account_id = ? AND blocked_system_account_id = ?
			  AND blocked_resource_type = ? AND blocked_resource_id = ?
			LIMIT 1`), actorID, blockAccount, blockResourceType, blockResourceID).Scan(&blockID)
	}
	if lookupErr != nil {
		if !errors.Is(lookupErr, sql.ErrNoRows) {
			return nil, lookupErr
		}
	}
	where := `s.authorization_id IN (SELECT id FROM ` + s.table("resource_authorizations") + ` WHERE grantee_system_account_id = ?)
		AND s.status IN ('pending','active','dormant','needs_review') AND g.id = s.grant_id`
	args := []any{actorID}
	if scope == "grantor" {
		where += " AND g.resource_owner_system_account_id = ?"
		args = append(args, grant.OwnerID)
	} else {
		where += " AND g.resource_type = ? AND g.resource_id = ?"
		args = append(args, grant.ResourceType, grant.ResourceID)
	}
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT s.id, s.status, s.grant_id, s.authorization_id
		FROM `+s.table("resource_authorization_sources")+` s
		JOIN `+s.table("resource_authorization_grants")+` g ON g.id = s.grant_id
		WHERE `+where+` ORDER BY s.id`), args...)
	if err != nil {
		return nil, err
	}
	type affected struct {
		sourceID, status, grantID, runtimeID string
	}
	var items []affected
	for rows.Next() {
		var it affected
		if err := rows.Scan(&it.sourceID, &it.status, &it.grantID, &it.runtimeID); err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := &BlockResult{BlockID: blockID}
	grants := map[string]bool{}
	for _, it := range items {
		next := ""
		switch {
		case it.status == StatusPending:
			next = StatusDeclined
			out.WithdrawnPending++
		case input.ReturnExisting && (it.status == StatusActive || it.status == StatusDormant || it.status == StatusNeedsReview):
			next = StatusReturned
			out.ReturnedExisting++
		default:
			continue
		}
		reason := "blocked"
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_sources")+`
			SET status = ?, ended_at = ?, ended_reason = ?, pending_expires_at = NULL,
				declined_at = CASE WHEN ? = 'declined' THEN ? ELSE declined_at END,
				declined_by = CASE WHEN ? = 'declined' THEN ? ELSE declined_by END,
				returned_at = CASE WHEN ? = 'returned' THEN ? ELSE returned_at END,
				returned_by = CASE WHEN ? = 'returned' THEN ? ELSE returned_by END,
				updated_at = ?
			WHERE id = ? AND status IN ('pending','active','dormant','needs_review')`),
			next, now, reason, next, now, next, actorID, next, now, next, actorID, now, it.sourceID); err != nil {
			return nil, err
		}
		grant, err := s.getOfferForUpdate(ctx, tx, it.grantID)
		if err != nil {
			return nil, err
		}
		if grant != nil {
			if grant.GranteeType == "system_account" {
				if err := s.updatePersonalGrantAggregate(ctx, tx, grant.ID, actorID, now); err != nil {
					return nil, err
				}
			} else {
				grants[grant.ID] = true
			}
		}
		if _, err := s.recomputeRuntime(ctx, tx, it.runtimeID, actorID, now); err != nil {
			return nil, err
		}
	}
	for grantID := range grants {
		grant, err := s.getOfferForUpdate(ctx, tx, grantID)
		if err != nil {
			return nil, err
		}
		if grant != nil {
			if err := s.recomputeTeamGrantAggregate(ctx, tx, grant, actorID, now); err != nil {
				return nil, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.invalidateGovernance(ctx, invalidationReasonUpdated)
	return out, nil
}

var _ = time.Now

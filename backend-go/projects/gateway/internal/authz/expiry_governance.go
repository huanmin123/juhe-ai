// TTL expiry and owner-initiated extension (design §5.5, §8.5).
package authz

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
)

// ExpireResult counts worker effects for observability.
type ExpireResult struct {
	GrantsExpired  int `json:"grantsExpired"`
	SourcesExpired int `json:"sourcesExpired"`
}

// ExpireDueOffers expires offer/approval/member TTLs. It is safe to run
// concurrently; every transition re-checks the row state under lock.
func (s *Store) ExpireDueOffers(ctx context.Context, now string, limit int) (ExpireResult, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	var out ExpireResult
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT id FROM `+s.table("resource_authorization_grants")+`
		WHERE status IN ('pending_owner_approval','pending','pending_team_approval','pending_member_acceptance','active','paused')
		  AND ((COALESCE(pending_expires_at, '') <> '' AND pending_expires_at <= ?)
		    OR (COALESCE(expires_at, '') <> '' AND expires_at <= ?))
		ORDER BY COALESCE(pending_expires_at, expires_at), id LIMIT ?`), now, now, limit)
	if err != nil {
		return out, err
	}
	var grantIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return out, err
		}
		grantIDs = append(grantIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	for _, id := range grantIDs {
		expired, err := s.expireGrant(ctx, id, now)
		if err != nil {
			return out, err
		}
		if expired {
			out.GrantsExpired++
		}
	}

	rows, err = s.db.QueryContext(ctx, s.bind(`SELECT id FROM `+s.table("resource_authorization_sources")+`
		WHERE status = 'pending' AND COALESCE(pending_expires_at, '') <> '' AND pending_expires_at <= ?
		ORDER BY pending_expires_at, id LIMIT ?`), now, limit)
	if err != nil {
		return out, err
	}
	var sourceIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return out, err
		}
		sourceIDs = append(sourceIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	for _, id := range sourceIDs {
		expired, err := s.expirePendingSource(ctx, id, now)
		if err != nil {
			return out, err
		}
		if expired {
			out.SourcesExpired++
		}
	}
	return out, nil
}

func (s *Store) expireGrant(ctx context.Context, grantID, now string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	grant, err := s.getOfferForUpdate(ctx, tx, grantID)
	if err != nil || grant == nil {
		return false, err
	}
	switch grant.Status {
	case StatusPendingOwnerApproval, StatusPending, StatusPendingTeamApproval,
		StatusPendingMemberAcceptance, StatusActive, StatusPaused:
	default:
		return false, nil
	}
	pendingDue := grant.PendingExpiresAt != "" && grant.PendingExpiresAt <= now
	expiresDue := grant.ExpiresAt != "" && grant.ExpiresAt <= now
	if !pendingDue && !expiresDue {
		return false, nil
	}
	reason := "offer_expired"
	fromStatus := grant.Status
	advanceCycle := false
	switch grant.Status {
	case StatusPendingOwnerApproval:
		reason = "owner_approval_timeout"
	case StatusPendingTeamApproval:
		reason = "team_approval_timeout"
	case StatusPendingMemberAcceptance:
		var count int
		if err := tx.QueryRowContext(ctx, s.bind(`SELECT count(*) FROM `+s.table("resource_authorization_sources")+`
			WHERE grant_id = ? AND expiry_cycle = ?`), grant.ID, grant.ExpiryCycle).Scan(&count); err != nil {
			return false, err
		}
		if count == 0 {
			reason = "no_member_offer"
		} else {
			reason = "offer_expired"
			advanceCycle = true
		}
	case StatusPending, StatusActive, StatusPaused:
		reason = "offer_expired"
		advanceCycle = true
	}
	nextCycle := grant.ExpiryCycle
	if advanceCycle {
		nextCycle++
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_grants")+`
		SET status = 'expired', expired_from_status = ?, expiry_cycle = ?,
			ended_at = ?, ended_reason = ?, pending_expires_at = NULL, updated_at = ?
		WHERE id = ? AND status = ?`), fromStatus, nextCycle, now, reason, now, grant.ID, grant.Status); err != nil {
		return false, err
	}
	if advanceCycle {
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_sources")+`
			SET status = 'expired', expired_from_status = status, expiry_cycle = ?,
				ended_at = ?, ended_reason = 'offer_expired', pending_expires_at = NULL, updated_at = ?
			WHERE grant_id = ? AND status IN ('pending','active','dormant','needs_review')`),
			nextCycle, now, now, grant.ID); err != nil {
			return false, err
		}
	} else {
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_sources")+`
			SET status = 'expired', expired_from_status = status,
				ended_at = ?, ended_reason = ?, pending_expires_at = NULL, updated_at = ?
			WHERE grant_id = ? AND status IN ('pending','active','dormant','needs_review')`),
			now, reason, now, grant.ID); err != nil {
			return false, err
		}
	}
	if err := s.recomputeGrantRuntimes(ctx, tx, grant.ID, "system:expiry", now); err != nil {
		return false, err
	}
	for _, target := range s.grantRuntimeGrantees(ctx, tx, grant.ID) {
		if err := s.insertNotification(ctx, tx, target, "offer_expired", grant.ID, "", nil,
			"expired:"+grant.ID+":"+strconv.Itoa(nextCycle)+":"+target, now); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	s.invalidateGovernance(ctx, invalidationReasonExpired)
	return true, nil
}

func (s *Store) expirePendingSource(ctx context.Context, sourceID, now string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	source, err := s.getSourceForUpdate(ctx, tx, sourceID)
	if err != nil || source == nil {
		return false, err
	}
	if source.Status != StatusPending || source.PendingExpiresAt == "" || source.PendingExpiresAt > now {
		return false, nil
	}
	grant, err := s.getOfferForUpdate(ctx, tx, source.GrantID)
	if err != nil || grant == nil {
		return false, err
	}
	if grant.Status == StatusExpired || grant.Status == StatusRevoked || grant.Status == StatusReturned || isTerminalStatus(grant.Status) {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_sources")+`
		SET status = 'expired', expired_from_status = 'pending', ended_at = ?,
			ended_reason = 'member_offer_expired', pending_expires_at = NULL, updated_at = ?
		WHERE id = ? AND status = 'pending'`), now, now, source.ID); err != nil {
		return false, err
	}
	if source.SourceType == "manual" {
		var live int
		if err := tx.QueryRowContext(ctx, s.bind(`SELECT count(*) FROM `+s.table("resource_authorization_sources")+`
			WHERE grant_id = ? AND status IN ('pending','active','dormant','needs_review')`), grant.ID).Scan(&live); err != nil {
			return false, err
		}
		if live == 0 {
			nextCycle := grant.ExpiryCycle + 1
			if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_grants")+`
				SET status = 'expired', expired_from_status = ?, expiry_cycle = ?, ended_at = ?,
					ended_reason = 'offer_expired', pending_expires_at = NULL, updated_at = ?
				WHERE id = ? AND status IN ('pending','active','paused')`),
				grant.Status, nextCycle, now, now, grant.ID); err != nil {
				return false, err
			}
			if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_sources")+`
				SET ended_reason = 'offer_expired', expiry_cycle = ? WHERE id = ?`), nextCycle, source.ID); err != nil {
				return false, err
			}
		}
	} else {
		updated, err := s.getOfferForUpdate(ctx, tx, grant.ID)
		if err != nil {
			return false, err
		}
		if updated != nil {
			if err := s.recomputeTeamGrantAggregate(ctx, tx, updated, "system:expiry", now); err != nil {
				return false, err
			}
		}
	}
	if _, err := s.recomputeRuntime(ctx, tx, source.AuthorizationID, "system:expiry", now); err != nil {
		return false, err
	}
	if err := s.insertNotification(ctx, tx, grant.OwnerID, "member_offer_expired", grant.ID, source.ID, nil,
		"source-expired:"+source.ID, now); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	s.invalidateGovernance(ctx, invalidationReasonExpired)
	return true, nil
}

// ExtendInput is the explicit extend contract.
type ExtendInput struct {
	ExpiresAt                string `json:"expiresAt"`
	ConfirmExpiredFromStatus string `json:"confirmExpiredFromStatus"`
}

// ExtendOffer extends an active or expired offer and restores only the
// current-cycle sources ended by the grant's own expiry.
func (s *Store) ExtendOffer(ctx context.Context, actorID, grantID string, input ExtendInput, isAdmin bool, now string) error {
	expiresAt := strings.TrimSpace(input.ExpiresAt)
	if expiresAt == "" {
		return failf("请选择新的到期时间")
	}
	nowTime := parseTimeOrNow(now)
	if !parseTimeOrNow(expiresAt).After(nowTime) {
		return failf("新的到期时间必须晚于当前时间")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	grant, err := s.getOfferForUpdate(ctx, tx, grantID)
	if err != nil {
		return err
	}
	if grant == nil {
		return failf("授权不存在")
	}
	if !isAdmin && actorID != grant.OwnerID {
		return failf("授权不存在")
	}
	// Extend is an increase/resume action: a suspended owner (or a suspended
	// administrator acting on it) must not bypass the freeze.
	if err := s.ensureNotSuspended(ctx, tx, grant.OwnerID); err != nil {
		return err
	}
	if actorID != grant.OwnerID {
		if err := s.ensureNotSuspended(ctx, tx, actorID); err != nil {
			return err
		}
	}
	if grant.Status == StatusPaused {
		return failCode("state_conflict", "暂停中的授权请先恢复再延长")
	}
	if grant.Status != StatusActive && grant.Status != StatusExpired {
		return failCode("state_conflict", "当前状态不允许延长")
	}
	owned, err := s.ensureResourceStillOwned(ctx, tx, grant.ResourceType, grant.ResourceID, grant.OwnerID)
	if err != nil {
		return err
	}
	if !owned {
		return failCode("resource_changed", "授权资源归属已变化，请重新授权")
	}
	if grant.ExpiresAt != "" && !parseTimeOrNow(expiresAt).After(parseTimeOrNow(grant.ExpiresAt)) {
		return failf("新的到期时间必须晚于当前到期时间")
	}
	if grant.ExpiresAt == "" {
		return failf("当前授权永不过期，无需延长")
	}
	if grant.Status == StatusActive {
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_grants")+`
			SET expires_at = ?, updated_at = ? WHERE id = ? AND status = 'active'`), expiresAt, now, grant.ID); err != nil {
			return err
		}
		grant.ExpiresAt = expiresAt
		if err := s.recomputeGrantRuntimes(ctx, tx, grant.ID, actorID, now); err != nil {
			return err
		}
		if err := s.notifyExtend(ctx, tx, grant, now); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		s.invalidateGovernance(ctx, invalidationReasonUpdated)
		return nil
	}
	return s.extendExpiredGrant(ctx, tx, grant, actorID, input, isAdmin, expiresAt, now)
}

func (s *Store) extendExpiredGrant(ctx context.Context, tx *sql.Tx, grant *offerRecord, actorID string,
	input ExtendInput, isAdmin bool, expiresAt, now string) error {
	if grant.AcceptanceMode == "legacy" && grant.ResourceType == "group" {
		return errNoRestorable
	}
	if grant.ExpiredFromStatus == "" {
		return failCode("legacy_expiry_unknown", "该授权到期前状态未知，请管理员确认后重试")
	}
	fromStatus := grant.ExpiredFromStatus
	// Migration records the legacy uncertainty marker in legacy_basis while
	// keeping expired_from_status='active' (design §7.6 item 16). Treat that
	// marker as unknown only for the legacy expiry cycle, not for a later
	// worker-driven expiry after a successful extend.
	legacyExpiryUnknown := fromStatus == "legacy_expiry_unknown" ||
		(grant.EndedReason == "authorization_expired" && strings.Contains(grant.LegacyBasis, "legacy_expiry_unknown"))
	if legacyExpiryUnknown {
		confirmed := strings.TrimSpace(input.ConfirmExpiredFromStatus)
		if !isAdmin {
			return failCode("legacy_expiry_unknown", "请管理员确认到期前状态")
		}
		if confirmed != StatusActive && confirmed != StatusPaused {
			return failCode("legacy_expiry_unknown", "管理员必须确认到期前状态为 active 或 paused")
		}
		fromStatus = confirmed
	}
	if fromStatus == StatusPendingOwnerApproval || fromStatus == StatusPendingTeamApproval {
		return failCode("state_conflict", "审批超时的授权不可延长，请重新发起")
	}
	if grant.EndedReason != "offer_expired" && grant.EndedReason != "authorization_expired" {
		return errNoRestorable
	}
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT `+sourceColumns+` FROM `+s.table("resource_authorization_sources")+` s
		WHERE s.grant_id = ? AND s.status = 'expired' AND s.expiry_cycle = ?
		  AND s.ended_reason IN ('offer_expired','authorization_expired')
		ORDER BY s.id FOR UPDATE`), grant.ID, grant.ExpiryCycle)
	if err != nil {
		return err
	}
	var sources []sourceRecord
	for rows.Next() {
		row, err := scanSource(rows)
		if err != nil {
			rows.Close()
			return err
		}
		sources = append(sources, row)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(sources) == 0 {
		return errNoRestorable
	}
	restoredAccepted := false
	restoredPending := false
	for i := range sources {
		source := &sources[i]
		nextStatus := StatusNeedsReview
		prior := source.ExpiredFromStatus
		if prior == "" {
			prior = fromStatus
		}
		switch prior {
		case StatusActive, StatusDormant:
			currentRevision, currentVersion, err := s.sourceCurrentRevision(ctx, tx, grant.ResourceType, grant.ResourceID, grant.OwnerID)
			if err != nil {
				return err
			}
			if source.AcceptedSourceRevision == "" || source.AcceptedRevisionVersion != currentVersion ||
				source.AcceptedSourceRevision != currentRevision {
				nextStatus = StatusNeedsReview
			} else {
				nextStatus = prior
				restoredAccepted = true
			}
		case StatusNeedsReview:
			nextStatus = StatusNeedsReview
		case StatusPending:
			nextStatus = StatusPending
			restoredPending = true
		default:
			if source.AcceptedSourceRevision == "" {
				nextStatus = StatusNeedsReview
			} else {
				nextStatus = StatusNeedsReview
			}
		}
		if nextStatus == StatusActive || nextStatus == StatusDormant {
			restoredAccepted = true
		}
		pendingExpires := any(nil)
		if nextStatus == StatusPending {
			pendingExpires = nowPlus(parseTimeOrNow(now), MemberOfferTTL)
		}
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_sources")+`
			SET status = ?, expired_from_status = NULL, ended_at = NULL, ended_reason = NULL,
				needs_review_at = CASE WHEN ? = 'needs_review' THEN ? ELSE NULL END,
				needs_review_reason = CASE WHEN ? = 'needs_review' THEN 'source_changed_after_extend' ELSE NULL END,
				needs_review_detail_json = NULL, pending_expires_at = ?, updated_at = ?
			WHERE id = ? AND status = 'expired'`),
			nextStatus, nextStatus, now, nextStatus, pendingExpires, now, source.ID); err != nil {
			return err
		}
	}
	nextGrantStatus := StatusPending
	if restoredAccepted {
		nextGrantStatus = StatusActive
	} else if grant.GranteeType == "team" {
		nextGrantStatus = StatusPendingMemberAcceptance
	}
	if fromStatus == StatusPaused {
		nextGrantStatus = StatusPaused
	}
	var grantPendingExpiry any
	if nextGrantStatus == StatusPending || nextGrantStatus == StatusPendingMemberAcceptance {
		grantPendingExpiry = nowPlus(parseTimeOrNow(now), pendingTTL(nextGrantStatus))
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_grants")+`
		SET status = ?, expires_at = ?, expired_from_status = NULL, ended_at = NULL, ended_reason = NULL,
			pending_expires_at = ?, updated_at = ? WHERE id = ? AND status = 'expired'`),
		nextGrantStatus, expiresAt, grantPendingExpiry, now, grant.ID); err != nil {
		return err
	}
	grant.Status = nextGrantStatus
	grant.ExpiresAt = expiresAt
	_ = restoredPending
	if err := s.recomputeGrantRuntimes(ctx, tx, grant.ID, actorID, now); err != nil {
		return err
	}
	if err := s.notifyExtend(ctx, tx, grant, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.invalidateGovernance(ctx, invalidationReasonUpdated)
	return nil
}

func (s *Store) notifyExtend(ctx context.Context, tx *sql.Tx, grant *offerRecord, now string) error {
	for _, target := range s.grantRuntimeGrantees(ctx, tx, grant.ID) {
		if err := s.insertNotification(ctx, tx, target, "offer_extended", grant.ID, "", nil,
			"extended:"+grant.ID+":"+target+":"+now, now); err != nil {
			return err
		}
	}
	return nil
}

var _ = errors.Is

// Grant expiry by authorized lifetime (expires_at) and owner-initiated
// extension. Pending offers never expire on their own: they stay claimable
// until the grant lifetime ends or the owner revokes it.
package authz

import (
	"context"
	"database/sql"
	"strings"
)

// ExpireResult counts worker effects for observability.
type ExpireResult struct {
	GrantsExpired  int `json:"grantsExpired"`
	SourcesExpired int `json:"sourcesExpired"`
}

// ExpireDueOffers flips grants whose authorized lifetime (expires_at) has
// passed. It is safe to run concurrently; every transition re-checks the row
// state under lock.
func (s *Store) ExpireDueOffers(ctx context.Context, now string, limit int) (ExpireResult, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	var out ExpireResult
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT id FROM `+s.table("resource_authorization_grants")+`
		WHERE status IN ('pending','active','paused')
		  AND COALESCE(expires_at, '') <> '' AND expires_at <= ?
		ORDER BY expires_at, id LIMIT ?`), now, limit)
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
	case StatusPending, StatusActive, StatusPaused:
	default:
		return false, nil
	}
	if grant.ExpiresAt == "" || grant.ExpiresAt > now {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_grants")+`
		SET status = 'expired', expiry_cycle = expiry_cycle + 1,
			ended_at = ?, ended_reason = 'authorization_expired', updated_at = ?
		WHERE id = ? AND status = ?`), now, now, grant.ID, grant.Status); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_sources")+`
		SET status = 'expired', expiry_cycle = ?,
			ended_at = ?, ended_reason = 'authorization_expired', updated_at = ?
		WHERE grant_id = ? AND status IN ('pending','active','paused')`),
		grant.ExpiryCycle+1, now, now, grant.ID); err != nil {
		return false, err
	}
	if err := s.recomputeGrantRuntimes(ctx, tx, grant.ID, "system:expiry", now); err != nil {
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
	ExpiresAt string `json:"expiresAt"`
}

// ExtendOffer extends an active or expired offer. For an expired grant the
// current-cycle sources are restored: sources the grantee had claimed
// (accepted_at set) go back to active, never-claimed sources go back to
// pending so they stay claimable.
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
	if grant.Status == StatusActive {
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_grants")+`
			SET expires_at = ?, updated_at = ? WHERE id = ? AND status = 'active'`), expiresAt, now, grant.ID); err != nil {
			return err
		}
		grant.ExpiresAt = expiresAt
		if err := s.recomputeGrantRuntimes(ctx, tx, grant.ID, actorID, now); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		s.invalidateGovernance(ctx, invalidationReasonUpdated)
		return nil
	}
	if err := s.extendExpiredGrant(ctx, tx, grant, expiresAt, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.invalidateGovernance(ctx, invalidationReasonUpdated)
	return nil
}

// extendExpiredGrant restores an expired grant. Sources the grantee claimed
// return to active; unclaimed sources return to pending. Sources ended by a
// revoke/decline/return are left alone.
func (s *Store) extendExpiredGrant(ctx context.Context, tx *sql.Tx, grant *offerRecord, expiresAt, now string) error {
	if grant.EndedReason != "authorization_expired" {
		return errNoRestorable
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_sources")+`
		SET status = CASE WHEN COALESCE(accepted_at, '') <> '' THEN 'active' ELSE 'pending' END,
			ended_at = NULL, ended_reason = NULL, updated_at = ?
		WHERE grant_id = ? AND status = 'expired' AND ended_reason = 'authorization_expired'`),
		now, grant.ID); err != nil {
		return err
	}
	var claimed int
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT count(*) FROM `+s.table("resource_authorization_sources")+`
		WHERE grant_id = ? AND status = 'active'`), grant.ID).Scan(&claimed); err != nil {
		return err
	}
	nextGrantStatus := StatusPending
	if claimed > 0 {
		nextGrantStatus = StatusActive
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_grants")+`
		SET status = ?, expires_at = ?, ended_at = NULL, ended_reason = NULL, updated_at = ?
		WHERE id = ? AND status = 'expired'`), nextGrantStatus, expiresAt, now, grant.ID); err != nil {
		return err
	}
	grant.Status = nextGrantStatus
	grant.ExpiresAt = expiresAt
	if err := s.recomputeGrantRuntimes(ctx, tx, grant.ID, "system:expiry", now); err != nil {
		return err
	}
	return nil
}

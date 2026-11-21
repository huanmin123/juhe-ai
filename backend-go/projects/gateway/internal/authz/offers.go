// Offer lifecycle: creation, owner approval, team approval, re-invite,
// pause/resume and revoke (design §4.1/§4.2/§5.1/§5.5/§5.7/§5.8).
package authz

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/authzrevision"
)

// OfferInput is the create contract (targetGroupId removed).
type OfferInput struct {
	ResourceType           string
	ResourceID             string
	GranteeType            string
	GranteeUsername        string
	GranteeSystemAccountID string
	TeamName               string
	TeamID                 string
	Remark                 *string
	ExpiresAt              *string
	LimitsJSON             *string
}

// OfferResult is the create response.
type OfferResult struct {
	GrantID               string `json:"grantId"`
	SourceID              string `json:"sourceId,omitempty"`
	Status                string `json:"status"`
	OwnerApprovalRequired bool   `json:"ownerApprovalRequired"`
	Created               bool   `json:"created"`
	PreviousStatus        string `json:"previousStatus,omitempty"`
}

type offerResourceInfo struct {
	ID            string
	OwnerID       string
	AccountExpiry *string
	GroupKind     string
}

func (s *Store) resolveResource(ctx context.Context, q revisionQueryer, resourceType, resourceID string) (*offerResourceInfo, error) {
	var info offerResourceInfo
	switch resourceType {
	case "account":
		var expiry sql.NullString
		err := q.QueryRowContext(ctx, `SELECT id, system_account_id, account_expires_at
			FROM juhe_business.accounts
			WHERE id = $1 AND deleted_at IS NULL AND authorization_instance_authorization_id IS NULL`, resourceID).
			Scan(&info.ID, &info.OwnerID, &expiry)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if expiry.Valid {
			info.AccountExpiry = &expiry.String
		}
		info.GroupKind = ""
		return &info, nil
	case "group":
		err := q.QueryRowContext(ctx, `SELECT id, system_account_id, group_kind
			FROM juhe_business.groups WHERE id = $1`, resourceID).Scan(&info.ID, &info.OwnerID, &info.GroupKind)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return &info, nil
	default:
		return nil, failf("请选择授权资源")
	}
}

func (s *Store) resolveGrantee(ctx context.Context, q revisionQueryer, input *OfferInput) (string, string, error) {
	switch input.GranteeType {
	case "system_account":
		query := `SELECT id FROM juhe_business.system_accounts WHERE status = 'active' AND deleted_at IS NULL`
		var arg string
		if strings.TrimSpace(input.GranteeSystemAccountID) != "" {
			query += " AND id = $1"
			arg = strings.TrimSpace(input.GranteeSystemAccountID)
		} else if strings.TrimSpace(input.GranteeUsername) != "" {
			query += " AND lower(username) = lower($1)"
			arg = strings.TrimSpace(input.GranteeUsername)
		} else {
			return "", "", failf("请输入被授权用户名")
		}
		var id string
		err := q.QueryRowContext(ctx, query, arg).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", failCode("grantee_not_found", "被授权用户不存在或已停用")
		}
		if err != nil {
			return "", "", err
		}
		input.GranteeSystemAccountID = id
		return id, "", nil
	case "team":
		query := `SELECT id FROM juhe_business.system_teams WHERE status = 'active' AND deleted_at IS NULL`
		var arg string
		if strings.TrimSpace(input.TeamID) != "" {
			query += " AND id = $1"
			arg = strings.TrimSpace(input.TeamID)
		} else if strings.TrimSpace(input.TeamName) != "" {
			query += " AND name = $1"
			arg = strings.TrimSpace(input.TeamName)
		} else {
			return "", "", failf("请输入授权团队名")
		}
		var id string
		err := q.QueryRowContext(ctx, query, arg).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", failCode("team_not_found", "团队不存在或已停用")
		}
		if err != nil {
			return "", "", err
		}
		input.TeamID = id
		return "", id, nil
	default:
		return "", "", failf("被授权对象类型无效")
	}
}

func (s *Store) findOpenOffer(ctx context.Context, q revisionQueryer, resourceType, resourceID, granteeType, userID, teamID string) (*offerRecord, error) {
	query := `SELECT ` + offerColumns + ` FROM juhe_business.resource_authorization_grants g
		WHERE g.resource_type = $1 AND g.resource_id = $2 AND g.grantee_type = $3`
	args := []any{resourceType, resourceID, granteeType}
	if granteeType == "team" {
		query += " AND g.grantee_team_id = $4"
		args = append(args, teamID)
	} else {
		query += " AND g.grantee_system_account_id = $4"
		args = append(args, userID)
	}
	query += " AND g.status IN ('pending_owner_approval','pending','pending_team_approval','pending_member_acceptance','active','paused') LIMIT 1"
	row, err := scanOffer(q.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (s *Store) findReusableOffer(ctx context.Context, tx *sql.Tx, resourceType, resourceID, granteeType, userID, teamID string) (*offerRecord, error) {
	query := `SELECT ` + offerColumns + ` FROM ` + s.table("resource_authorization_grants") + ` g
		WHERE g.resource_type = $1 AND g.resource_id = $2 AND g.grantee_type = $3`
	args := []any{resourceType, resourceID, granteeType}
	if granteeType == "team" {
		query += " AND g.grantee_team_id = $4"
		args = append(args, teamID)
	} else {
		query += " AND g.grantee_system_account_id = $4"
		args = append(args, userID)
	}
	query += " ORDER BY g.created_at DESC, g.id DESC LIMIT 1 FOR UPDATE"
	row, err := scanOffer(tx.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func pendingDeadline(now time.Time, ttl time.Duration, expiresAt string) string {
	deadline := now.Add(ttl)
	if expiresAt != "" {
		if parsed := parseTimeOrNow(expiresAt); parsed.Before(deadline) {
			deadline = parsed
		}
	}
	return formatTime(deadline)
}

// CreateOffer creates a pending offer only. It never provisions an instance,
// writes group_accounts or builds a group snapshot.
func (s *Store) CreateOffer(ctx context.Context, input OfferInput, actorID string, isAdmin bool, ownerScope string) (*OfferResult, error) {
	ctx = ensureCtx(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	nowTime := s.now().UTC()
	now := formatTime(nowTime)

	if strings.TrimSpace(input.ResourceType) == "" || strings.TrimSpace(input.ResourceID) == "" {
		return nil, failf("请选择授权资源")
	}
	resource, err := s.resolveResource(ctx, tx, input.ResourceType, input.ResourceID)
	if err != nil {
		return nil, err
	}
	if resource == nil {
		return nil, failf("授权资源不存在")
	}
	if input.ResourceType == "group" && resource.GroupKind != "standard" {
		return nil, failCode("group_kind_conflict", "超级分组不能作为授权资源")
	}
	if isAdmin {
		if strings.TrimSpace(ownerScope) == "" || ownerScope != resource.OwnerID {
			return nil, failf("授权资源不存在")
		}
	} else if actorID != resource.OwnerID {
		return nil, failf("授权资源不存在")
	}
	if err := s.ensureNotSuspended(ctx, tx, resource.OwnerID); err != nil {
		return nil, err
	}
	if err := s.ensureNotSuspended(ctx, tx, actorID); err != nil {
		return nil, err
	}
	normalizedExpires, err := normalizeAuthorizationExpiresAt(input.ExpiresAt)
	if err != nil {
		return nil, err
	}
	normalizedExpires, err = validateAuthorizationCreateExpiresAt(normalizedExpires, resource.AccountExpiry, nowTime)
	if err != nil {
		return nil, err
	}
	input.ExpiresAt = normalizedExpires
	if input.LimitsJSON != nil {
		normalized, err := normalizeAuthorizationLimitsJSON(*input.LimitsJSON)
		if err != nil {
			return nil, err
		}
		if normalized != nil {
			input.LimitsJSON = normalized
		} else {
			empty := ""
			input.LimitsJSON = &empty
		}
	}
	granteeUserID, teamID, err := s.resolveGrantee(ctx, tx, &input)
	if err != nil {
		return nil, err
	}
	if input.GranteeType == "system_account" && granteeUserID == resource.OwnerID {
		return nil, failCode("self_authorization_forbidden", "不能授权给资源所有者自己")
	}
	if input.GranteeType == "system_account" {
		if err := s.checkBlocked(ctx, tx, granteeUserID, resource.OwnerID, input.ResourceType, input.ResourceID); err != nil {
			return nil, err
		}
	}
	if err := s.checkCooldown(ctx, tx, resource.OwnerID, input.GranteeType, granteeUserID, teamID, "", input.ResourceType, input.ResourceID, now); err != nil {
		return nil, err
	}
	if err := s.enforceOfferLimits(ctx, tx, resource.OwnerID, input.GranteeType, granteeUserID, teamID, input.ResourceType, input.ResourceID, now); err != nil {
		return nil, err
	}
	if input.GranteeType == "team" {
		var memberCount int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM juhe_business.system_team_members m
			JOIN juhe_business.system_accounts a ON a.id = m.system_account_id
			WHERE m.team_id = $1 AND m.status = 'active' AND a.status = 'active' AND a.deleted_at IS NULL`,
			teamID).Scan(&memberCount); err != nil {
			return nil, err
		}
		if memberCount > MaxTeamMembersPerTeam {
			return nil, failf("授权团队最多支持 %d 个成员，请先移除部分成员后再继续", MaxTeamMembersPerTeam)
		}
	}

	existing, err := s.findOpenOffer(ctx, tx, input.ResourceType, input.ResourceID, input.GranteeType, granteeUserID, teamID)
	if err != nil {
		return nil, err
	}
	previousStatus := ""
	if existing != nil {
		switch existing.Status {
		case StatusPendingOwnerApproval, StatusPending, StatusPendingTeamApproval, StatusPendingMemberAcceptance:
			sourceID := ""
			if existing.GranteeType == "system_account" {
				_ = tx.QueryRowContext(ctx, s.bind(`SELECT id FROM `+s.table("resource_authorization_sources")+`
					WHERE grant_id = ? AND source_type = 'manual' AND status = 'pending'
					ORDER BY created_at DESC LIMIT 1`), existing.ID).Scan(&sourceID)
			}
			return &OfferResult{GrantID: existing.ID, SourceID: sourceID, Status: existing.Status,
				OwnerApprovalRequired: existing.Status == StatusPendingOwnerApproval, Created: false}, nil
		case StatusActive, StatusPaused:
			return nil, failCode("offer_already_active", "该资源已授权给该接收方并处于生效状态")
		}
	}

	ownerApproval := isAdmin && actorID != resource.OwnerID
	initialStatus := StatusPending
	if ownerApproval {
		initialStatus = StatusPendingOwnerApproval
	} else if input.GranteeType == "team" {
		initialStatus = StatusPendingTeamApproval
	}
	baseline, _, err := s.sourceCurrentRevision(ctx, tx, input.ResourceType, input.ResourceID, resource.OwnerID)
	if err != nil {
		return nil, err
	}
	var grantID string
	created := false
	expiryCycle := 0
	if existing != nil {
		previousStatus = existing.Status
		result, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_grants")+`
			SET status = ?, remark = ?, expires_at = ?, limits_json = ?, created_by = ?,
				resource_owner_system_account_id = ?, accepted_at = NULL, accepted_by = NULL,
				team_approved_at = NULL, team_approved_by = NULL, owner_approved_at = NULL, owner_approved_by = NULL,
				source_revision = ?, pending_expires_at = ?, expired_from_status = NULL,
				ended_at = NULL, ended_reason = NULL, legacy_basis = NULL, legacy_review_deadline = NULL,
				acceptance_mode = 'recipient', updated_at = ?
			WHERE id = ? AND status = ?`),
			initialStatus, nullIfEmpty(stringValue(input.Remark)), nullIfEmpty(stringValue(input.ExpiresAt)),
			nullIfEmpty(stringValue(input.LimitsJSON)), actorID, resource.OwnerID, baseline,
			nullIfEmpty(pendingDeadline(nowTime, pendingTTL(initialStatus), stringValue(input.ExpiresAt))),
			now, existing.ID, existing.Status)
		if err != nil {
			return nil, err
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return nil, &Conflict{}
		}
		grantID = existing.ID
		expiryCycle = existing.ExpiryCycle
	} else {
		created = true
		err = tx.QueryRowContext(ctx, s.bind(`INSERT INTO `+s.table("resource_authorization_grants")+`
			(id, resource_type, resource_id, resource_owner_system_account_id, grantee_type,
			 grantee_system_account_id, grantee_team_id, scope, status, remark, expires_at, limits_json,
			 created_by, acceptance_mode, source_revision, expiry_cycle, pending_expires_at, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), 'use', ?, ?, ?, ?, ?, 'recipient', ?, 0, ?, ?, ?)
			RETURNING id`),
			newGrantID(s.now), input.ResourceType, input.ResourceID, resource.OwnerID, input.GranteeType,
			granteeUserID, teamID, initialStatus, nullIfEmpty(stringValue(input.Remark)),
			nullIfEmpty(stringValue(input.ExpiresAt)), nullIfEmpty(stringValue(input.LimitsJSON)), actorID,
			baseline, nullIfEmpty(pendingDeadline(nowTime, pendingTTL(initialStatus), stringValue(input.ExpiresAt))),
			now, now).Scan(&grantID)
		if err != nil {
			return nil, err
		}
	}

	sourceID := ""
	if input.GranteeType == "system_account" && !ownerApproval {
		// Runtime must exist before the source insert: appendManualPendingSource
		// attaches the source to the receiver runtime row in one INSERT ... SELECT.
		runtime, err := s.ensureRuntime(ctx, tx, input.ResourceType, input.ResourceID, resource.OwnerID, granteeUserID, actorID, now)
		if err != nil {
			return nil, err
		}
		if runtime.Status != StatusPending {
			if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorizations")+`
				SET status = 'pending', status_reason = 'offer_created', status_changed_at = ?,
					effective_source_type = NULL, effective_source_team_id = NULL,
					revoked_by = NULL, revoked_at = NULL, revoked_reason = NULL,
					acceptance_mode = 'recipient', last_source_changed_at = ?, updated_at = ?
				WHERE id = ?`), now, now, now, runtime.ID); err != nil {
				return nil, err
			}
		}
		sourceID, err = s.appendManualPendingSource(ctx, tx, grantID, input.ResourceType, input.ResourceID,
			resource.OwnerID, granteeUserID, baseline, expiryCycle, actorID, now, stringValue(input.ExpiresAt))
		if err != nil {
			return nil, err
		}
		if _, err := s.recomputeRuntime(ctx, tx, runtime.ID, actorID, now); err != nil {
			return nil, err
		}
	} else if input.GranteeType == "system_account" && ownerApproval {
		// Owner approval gate: create the runtime + pending source but keep
		// the source hidden from the receiver until owner-approve.
		runtime, err := s.ensureRuntime(ctx, tx, input.ResourceType, input.ResourceID, resource.OwnerID, granteeUserID, actorID, now)
		if err != nil {
			return nil, err
		}
		if err := s.resetRuntimeForOffer(ctx, tx, runtime.ID, now); err != nil {
			return nil, err
		}
		sourceID, err = s.appendManualPendingSource(ctx, tx, grantID, input.ResourceType, input.ResourceID,
			resource.OwnerID, granteeUserID, baseline, expiryCycle, actorID, now, stringValue(input.ExpiresAt))
		if err != nil {
			return nil, err
		}
		if _, err := s.recomputeRuntime(ctx, tx, runtime.ID, actorID, now); err != nil {
			return nil, err
		}
	}

	if err := s.notifyOfferCreated(ctx, tx, grantID, sourceID, input, resource.OwnerID, granteeUserID, teamID,
		ownerApproval, initialStatus, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.invalidateGovernance(ctx, invalidationReasonCreated)
	return &OfferResult{GrantID: grantID, SourceID: sourceID, Status: initialStatus,
		OwnerApprovalRequired: ownerApproval, Created: created, PreviousStatus: previousStatus}, nil
}

func (s *Store) resetRuntimeForOffer(ctx context.Context, tx *sql.Tx, runtimeID, now string) error {
	_, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorizations")+`
		SET status = 'pending', status_reason = 'offer_created', status_changed_at = ?,
			effective_source_type = NULL, effective_source_team_id = NULL,
			revoked_by = NULL, revoked_at = NULL, revoked_reason = NULL,
			acceptance_mode = 'recipient', last_source_changed_at = ?, updated_at = ?
		WHERE id = ?`), now, now, now, runtimeID)
	return err
}

func pendingTTL(status string) time.Duration {
	switch status {
	case StatusPendingOwnerApproval:
		return OwnerApprovalTTL
	case StatusPendingTeamApproval:
		return TeamApprovalTTL
	case StatusPendingMemberAcceptance:
		return TeamMemberlessTTL
	default:
		return PendingOfferTTL
	}
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func (s *Store) appendManualPendingSource(ctx context.Context, tx *sql.Tx, grantID, resourceType, resourceID,
	ownerID, granteeID, revision string, expiryCycle int, actor, now, expiresAt string) (string, error) {
	revisionVersion := authzrevision.Version
	pendingExpires := pendingDeadline(parseTimeOrNow(now), PendingOfferTTL, expiresAt)
	id := newSourceID(s.now)
	// Idempotent duplicate pending source returns the existing row.
	var existingID string
	err := tx.QueryRowContext(ctx, s.bind(`SELECT id FROM `+s.table("resource_authorization_sources")+`
		WHERE grant_id = ? AND source_type = 'manual' AND status = 'pending' LIMIT 1`), grantID).Scan(&existingID)
	if err == nil {
		return existingID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	result, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("resource_authorization_sources")+`
		(id, grant_id, authorization_id, source_type, source_team_id, status, observed_source_revision,
		 revision_version, expiry_cycle, pending_expires_at, created_by, created_at, updated_at, acceptance_mode)
		SELECT ?, ?, ra.id, 'manual', NULL, 'pending', ?, ?, ?, ?, ?, ?, ?, 'recipient'
		FROM `+s.table("resource_authorizations")+` ra
		WHERE ra.resource_type = ? AND ra.resource_id = ? AND ra.grantee_system_account_id = ?`),
		id, grantID, revision, revisionVersion, expiryCycle, pendingExpires, actor, now, now,
		resourceType, resourceID, granteeID)
	if err != nil {
		return "", err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return "", fmt.Errorf("authz: receiver runtime missing for grant %s", grantID)
	}
	return id, nil
}

func (s *Store) notifyOfferCreated(ctx context.Context, tx *sql.Tx, grantID, sourceID string, input OfferInput,
	ownerID, granteeUserID, teamID string, ownerApproval bool, status, now string) error {
	if ownerApproval {
		return s.insertNotification(ctx, tx, ownerID, "owner_approval_pending", grantID, sourceID,
			map[string]any{"resourceType": input.ResourceType, "resourceId": input.ResourceID},
			"owner-approval:"+grantID, now)
	}
	if input.GranteeType == "team" {
		var approver string
		_ = tx.QueryRowContext(ctx, s.bind(`SELECT created_by FROM `+s.table("system_teams")+` WHERE id = ?`), teamID).Scan(&approver)
		return s.insertNotification(ctx, tx, approver, "team_offer_pending_approval", grantID, "",
			map[string]any{"resourceType": input.ResourceType, "resourceId": input.ResourceID},
			"team-approval:"+grantID, now)
	}
	return s.insertNotification(ctx, tx, granteeUserID, "offer_pending", grantID, sourceID,
		map[string]any{"resourceType": input.ResourceType, "resourceId": input.ResourceID},
		"offer-pending:"+sourceID, now)
}

// OwnerApprove releases an admin-created offer to the receiver/team.
func (s *Store) OwnerApprove(ctx context.Context, ownerID, grantID string, now string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	grant, err := s.getOfferForUpdate(ctx, tx, grantID)
	if err != nil {
		return err
	}
	if grant == nil || grant.OwnerID != ownerID {
		return failf("授权不存在")
	}
	if grant.Status != StatusPendingOwnerApproval {
		return failCode("state_conflict", "授权当前不在待所有者确认状态")
	}
	if err := s.ensureNotSuspended(ctx, tx, ownerID); err != nil {
		return err
	}
	nextStatus := StatusPending
	if grant.GranteeType == "team" {
		nextStatus = StatusPendingTeamApproval
	}
	_, err = tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_grants")+`
		SET status = ?, owner_approved_at = ?, owner_approved_by = ?, pending_expires_at = ?, updated_at = ?
		WHERE id = ? AND status = 'pending_owner_approval'`),
		nextStatus, now, ownerID, nowPlus(parseTimeOrNow(now), pendingTTL(nextStatus)), now, grantID)
	if err != nil {
		return err
	}
	// Personal source TTL resets to the receiver window; team has no source.
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_sources")+`
		SET pending_expires_at = ?, updated_at = ? WHERE grant_id = ? AND status = 'pending'`),
		nowPlus(parseTimeOrNow(now), PendingOfferTTL), now, grantID); err != nil {
		return err
	}
	if err := s.notifyOwnerDecision(ctx, tx, grant, true, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.invalidateGovernance(ctx, invalidationReasonUpdated)
	return nil
}

// OwnerDecline rejects an admin-created offer; the receiver never sees it.
func (s *Store) OwnerDecline(ctx context.Context, ownerID, grantID string, now string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	grant, err := s.getOfferForUpdate(ctx, tx, grantID)
	if err != nil {
		return err
	}
	if grant == nil || grant.OwnerID != ownerID {
		return failf("授权不存在")
	}
	if grant.Status != StatusPendingOwnerApproval {
		return failCode("state_conflict", "授权当前不在待所有者确认状态")
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_grants")+`
		SET status = 'declined', ended_at = ?, ended_reason = 'owner_declined', pending_expires_at = NULL, updated_at = ?
		WHERE id = ? AND status = 'pending_owner_approval'`), now, now, grantID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_sources")+`
		SET status = 'declined', ended_at = ?, ended_reason = 'owner_declined', pending_expires_at = NULL, updated_at = ?
		WHERE grant_id = ? AND status = 'pending'`), now, now, grantID); err != nil {
		return err
	}
	if err := s.recomputeGrantRuntimes(ctx, tx, grantID, ownerID, now); err != nil {
		return err
	}
	if grant.CreatedBy != "" {
		if err := s.insertNotification(ctx, tx, grant.CreatedBy, "offer_declined", grantID, "",
			map[string]any{"reason": "owner_declined"}, "owner-declined:"+grantID, now); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.invalidateGovernance(ctx, invalidationReasonUpdated)
	return nil
}

func (s *Store) notifyOwnerDecision(ctx context.Context, tx *sql.Tx, grant *offerRecord, approved bool, now string) error {
	if grant.GranteeType == "team" {
		var approver string
		_ = tx.QueryRowContext(ctx, s.bind(`SELECT created_by FROM `+s.table("system_teams")+` WHERE id = ?`), grant.GranteeTeamID).Scan(&approver)
		return s.insertNotification(ctx, tx, approver, "team_offer_pending_approval", grant.ID, "",
			map[string]any{"resourceType": grant.ResourceType, "resourceId": grant.ResourceID},
			"team-approval:"+grant.ID+":"+now, now)
	}
	return s.insertNotification(ctx, tx, grant.GranteeUserID, "offer_pending", grant.ID, "",
		map[string]any{"resourceType": grant.ResourceType, "resourceId": grant.ResourceID},
		"offer-pending:"+grant.ID+":"+now, now)
}

// TeamApprove expands a team offer into member pending sources.
func (s *Store) TeamApprove(ctx context.Context, teamID, grantID, actorID string, isAdmin bool, now string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	grant, err := s.getOfferForUpdate(ctx, tx, grantID)
	if err != nil {
		return err
	}
	if grant == nil || grant.GranteeType != "team" || grant.GranteeTeamID != teamID {
		return failf("授权不存在")
	}
	if grant.Status != StatusPendingTeamApproval {
		return failCode("state_conflict", "授权当前不在待团队审批状态")
	}
	var creator, teamStatus string
	var deleted sql.NullString
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT created_by, status, deleted_at FROM `+s.table("system_teams")+` WHERE id = ?`), teamID).
		Scan(&creator, &teamStatus, &deleted); err != nil {
		return err
	}
	if teamStatus != "active" || deleted.Valid {
		return failCode("team_not_found", "团队不存在或已停用")
	}
	owned, err := s.ensureResourceStillOwned(ctx, tx, grant.ResourceType, grant.ResourceID, grant.OwnerID)
	if err != nil {
		return err
	}
	if !owned {
		return failCode("resource_changed", "授权资源归属已变化，请重新授权")
	}
	approverValid := false
	if creator != "" {
		var status string
		var deletedAt sql.NullString
		err := tx.QueryRowContext(ctx, s.bind(`SELECT status, deleted_at FROM `+s.table("system_accounts")+` WHERE id = ?`), creator).
			Scan(&status, &deletedAt)
		approverValid = err == nil && status == "active" && !deletedAt.Valid && creator != grant.OwnerID
	}
	selfApproval := creator == grant.OwnerID
	// The resource owner can never approve their own team offer, including via
	// the admin surface (design §4.2 / §13 #11).
	if actorID == grant.OwnerID {
		return failCode("team_approval_forbidden", "资源所有者不能审批自己的团队授权")
	}
	if !isAdmin {
		if actorID != creator || selfApproval {
			return failCode("team_approval_forbidden", "无权审批该团队授权")
		}
	} else if approverValid && !selfApproval && actorID != creator {
		return failCode("team_approval_forbidden", "团队审批人有效时管理员不得代行审批")
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_grants")+`
		SET status = 'pending_member_acceptance', team_approved_at = ?, team_approved_by = ?, updated_at = ?
		WHERE id = ? AND status = 'pending_team_approval'`), now, actorID, now, grantID); err != nil {
		return err
	}
	eligible, err := s.eligibleTeamMembers(ctx, tx, grant, now)
	if err != nil {
		return err
	}
	createdSources := 0
	for _, memberID := range eligible {
		runtime, err := s.ensureRuntime(ctx, tx, grant.ResourceType, grant.ResourceID, grant.OwnerID, memberID, actorID, now)
		if err != nil {
			return err
		}
		if runtime.Status != StatusPending {
			if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorizations")+`
				SET status = 'pending', status_reason = 'team_offer_pending', status_changed_at = ?,
					effective_source_type = NULL, effective_source_team_id = NULL,
					revoked_by = NULL, revoked_at = NULL, revoked_reason = NULL,
					acceptance_mode = 'recipient', last_source_changed_at = ?, updated_at = ?
				WHERE id = ?`), now, now, now, runtime.ID); err != nil {
				return err
			}
		}
		revision, version, err := s.sourceCurrentRevision(ctx, tx, grant.ResourceType, grant.ResourceID, grant.OwnerID)
		if err != nil {
			return err
		}
		sourceID := newSourceID(s.now)
		_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("resource_authorization_sources")+`
			(id, grant_id, authorization_id, source_type, source_team_id, status, observed_source_revision,
			 revision_version, expiry_cycle, pending_expires_at, created_by, created_at, updated_at, acceptance_mode)
			VALUES (?, ?, ?, 'team', ?, 'pending', ?, ?, ?, ?, ?, ?, ?, 'recipient')`),
			sourceID, grantID, runtime.ID, teamID, revision, version, grant.ExpiryCycle,
			nowPlus(parseTimeOrNow(now), MemberOfferTTL), actorID, now, now)
		if err != nil {
			return err
		}
		createdSources++
		if _, err := s.recomputeRuntime(ctx, tx, runtime.ID, actorID, now); err != nil {
			return err
		}
		if err := s.insertNotification(ctx, tx, memberID, "member_offer_pending", grantID, sourceID,
			map[string]any{"resourceType": grant.ResourceType, "resourceId": grant.ResourceID},
			"member-offer:"+sourceID, now); err != nil {
			return err
		}
	}
	pendingExpires := any(nil)
	if createdSources == 0 {
		pendingExpires = nowPlus(parseTimeOrNow(now), TeamMemberlessTTL)
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_grants")+`
		SET pending_expires_at = ? WHERE id = ?`), pendingExpires, grantID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.invalidateGovernance(ctx, invalidationReasonUpdated)
	return nil
}

func (s *Store) eligibleTeamMembers(ctx context.Context, tx *sql.Tx, grant *offerRecord, now string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT m.system_account_id
		FROM `+s.table("system_team_members")+` m
		JOIN `+s.table("system_accounts")+` a ON a.id = m.system_account_id
		WHERE m.team_id = ? AND m.status = 'active' AND a.status = 'active' AND a.deleted_at IS NULL
		  AND m.system_account_id <> ?
		ORDER BY m.system_account_id`), grant.GranteeTeamID, grant.OwnerID)
	if err != nil {
		return nil, err
	}
	var members []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		members = append(members, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := []string{}
	for _, memberID := range members {
		if blocked, err := s.isBlocked(ctx, tx, memberID, grant.OwnerID, grant.ResourceType, grant.ResourceID); err != nil {
			return nil, err
		} else if blocked {
			continue
		}
		if err := s.checkCooldown(ctx, tx, grant.OwnerID, "team_member", "", grant.GranteeTeamID, memberID, grant.ResourceType, grant.ResourceID, now); err != nil {
			if _, ok := err.(*Fail); ok {
				continue
			}
			return nil, err
		}
		// Team fan-out must honour the receiver-side pending/active/instance
		// caps and the per-grantor 24h member-offer window; suppress only that
		// member, never fail the whole team approval.
		if err := s.enforceGranteeSourceLimits(ctx, tx, memberID); err != nil {
			if errors.Is(err, errOfferSuppressed) {
				continue
			}
			return nil, err
		}
		var recentMemberOffers int
		if err := tx.QueryRowContext(ctx, s.bind(`SELECT count(*) FROM `+s.table("resource_authorization_sources")+` s
			JOIN `+s.table("resource_authorization_grants")+` g ON g.id = s.grant_id
			JOIN `+s.table("resource_authorizations")+` ra ON ra.id = s.authorization_id
			WHERE g.resource_owner_system_account_id = ? AND ra.grantee_system_account_id = ?
			  AND s.created_at > ?`), grant.OwnerID, memberID, nowMinus24h(now)).Scan(&recentMemberOffers); err != nil {
			return nil, err
		}
		if recentMemberOffers >= MaxOffersPerGrantorPerGrantee24h {
			continue
		}
		var live int
		if err := tx.QueryRowContext(ctx, s.bind(`SELECT count(*) FROM `+s.table("resource_authorization_sources")+`
			WHERE grant_id = ? AND source_type = 'team' AND source_team_id = ? AND status IN ('pending','active','dormant','needs_review')`),
			grant.ID, grant.GranteeTeamID).Scan(&live); err != nil {
			return nil, err
		}
		// Per-member existing source check is separate from the grant-wide
		// count; use the member runtime.
		var runtimeID string
		_ = tx.QueryRowContext(ctx, s.bind(`SELECT id FROM `+s.table("resource_authorizations")+`
			WHERE resource_type = ? AND resource_id = ? AND grantee_system_account_id = ?`),
			grant.ResourceType, grant.ResourceID, memberID).Scan(&runtimeID)
		if runtimeID != "" {
			var memberLive int
			if err := tx.QueryRowContext(ctx, s.bind(`SELECT count(*) FROM `+s.table("resource_authorization_sources")+`
				WHERE authorization_id = ? AND source_type = 'team' AND source_team_id = ? AND status IN ('pending','active','dormant','needs_review')`),
				runtimeID, grant.GranteeTeamID).Scan(&memberLive); err != nil {
				return nil, err
			}
			if memberLive > 0 {
				continue
			}
			var closed int
			if err := tx.QueryRowContext(ctx, s.bind(`SELECT count(*) FROM `+s.table("resource_authorization_sources")+`
				WHERE authorization_id = ? AND source_type = 'team' AND source_team_id = ? AND status IN ('declined','returned')`),
				runtimeID, grant.GranteeTeamID).Scan(&closed); err != nil {
				return nil, err
			}
			if closed > 0 {
				continue
			}
		}
		out = append(out, memberID)
	}
	return out, nil
}

// TeamDecline rejects a team offer and starts the team-level cooldown.
func (s *Store) TeamDecline(ctx context.Context, teamID, grantID, actorID string, isAdmin bool, now string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	grant, err := s.getOfferForUpdate(ctx, tx, grantID)
	if err != nil {
		return err
	}
	if grant == nil || grant.GranteeType != "team" || grant.GranteeTeamID != teamID {
		return failf("授权不存在")
	}
	if grant.Status != StatusPendingTeamApproval {
		return failCode("state_conflict", "授权当前不在待团队审批状态")
	}
	if !isAdmin {
		var creator string
		_ = tx.QueryRowContext(ctx, s.bind(`SELECT created_by FROM `+s.table("system_teams")+` WHERE id = ?`), teamID).Scan(&creator)
		if actorID != creator || creator == grant.OwnerID {
			return failCode("team_approval_forbidden", "无权审批该团队授权")
		}
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_grants")+`
		SET status = 'declined', team_approved_at = ?, team_approved_by = ?, ended_at = ?, ended_reason = 'team_declined',
			pending_expires_at = NULL, updated_at = ? WHERE id = ? AND status = 'pending_team_approval'`),
		now, actorID, now, now, grantID); err != nil {
		return err
	}
	if err := s.upsertCooldown(ctx, tx, grant.OwnerID, "team", "", teamID, "", grant.ResourceType, grant.ResourceID, "team_declined", now); err != nil {
		return err
	}
	if err := s.insertNotification(ctx, tx, grant.CreatedBy, "offer_declined", grantID, "",
		map[string]any{"reason": "team_declined"}, "team-declined:"+grantID, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.invalidateGovernance(ctx, invalidationReasonUpdated)
	return nil
}

// Reinvite creates one member pending source for an existing team grant.
func (s *Store) Reinvite(ctx context.Context, grantID, memberID, actorID, now string) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	grant, err := s.getOfferForUpdate(ctx, tx, grantID)
	if err != nil {
		return "", err
	}
	if grant == nil || grant.GranteeType != "team" {
		return "", failf("授权不存在")
	}
	if actorID != grant.OwnerID && actorID != grant.CreatedBy {
		return "", failf("授权不存在")
	}
	if grant.Status != StatusPendingMemberAcceptance && grant.Status != StatusActive {
		return "", failCode("state_conflict", "当前状态不允许重新邀请")
	}
	if err := s.ensureNotSuspended(ctx, tx, grant.OwnerID); err != nil {
		return "", err
	}
	if actorID != grant.OwnerID {
		if err := s.ensureNotSuspended(ctx, tx, actorID); err != nil {
			return "", err
		}
	}
	var memberStatus string
	var deletedAt sql.NullString
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT status, deleted_at FROM `+s.table("system_accounts")+` WHERE id = ?`), memberID).
		Scan(&memberStatus, &deletedAt); err != nil || memberStatus != "active" || deletedAt.Valid {
		return "", failCode("grantee_not_found", "团队成员不存在或已停用")
	}
	if memberID == grant.OwnerID {
		return "", failCode("self_authorization_forbidden", "不能给自己授权")
	}
	if blocked, err := s.isBlocked(ctx, tx, memberID, grant.OwnerID, grant.ResourceType, grant.ResourceID); err != nil {
		return "", err
	} else if blocked {
		return "", errOfferSuppressed
	}
	if err := s.checkCooldown(ctx, tx, grant.OwnerID, "team_member", "", grant.GranteeTeamID, memberID, grant.ResourceType, grant.ResourceID, now); err != nil {
		return "", err
	}
	if err := s.enforceOfferLimits(ctx, tx, grant.OwnerID, "team_member", memberID,
		grant.GranteeTeamID, grant.ResourceType, grant.ResourceID, now); err != nil {
		return "", err
	}
	runtime, err := s.ensureRuntime(ctx, tx, grant.ResourceType, grant.ResourceID, grant.OwnerID, memberID, actorID, now)
	if err != nil {
		return "", err
	}
	var existing string
	err = tx.QueryRowContext(ctx, s.bind(`SELECT id FROM `+s.table("resource_authorization_sources")+`
		WHERE authorization_id = ? AND source_type = 'team' AND source_team_id = ?
		  AND status IN ('pending','active','dormant','needs_review') LIMIT 1`),
		runtime.ID, grant.GranteeTeamID).Scan(&existing)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	revision, version, err := s.sourceCurrentRevision(ctx, tx, grant.ResourceType, grant.ResourceID, grant.OwnerID)
	if err != nil {
		return "", err
	}
	sourceID := newSourceID(s.now)
	_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("resource_authorization_sources")+`
		(id, grant_id, authorization_id, source_type, source_team_id, status, observed_source_revision,
		 revision_version, expiry_cycle, pending_expires_at, created_by, created_at, updated_at, acceptance_mode)
		VALUES (?, ?, ?, 'team', ?, 'pending', ?, ?, ?, ?, ?, ?, ?, 'recipient')`),
		sourceID, grantID, runtime.ID, grant.GranteeTeamID, revision, version, grant.ExpiryCycle,
		nowPlus(parseTimeOrNow(now), MemberOfferTTL), actorID, now, now)
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_grants")+`
		SET pending_expires_at = NULL, updated_at = ? WHERE id = ?`), now, grantID); err != nil {
		return "", err
	}
	if _, err := s.recomputeRuntime(ctx, tx, runtime.ID, actorID, now); err != nil {
		return "", err
	}
	if err := s.insertNotification(ctx, tx, memberID, "member_offer_pending", grantID, sourceID,
		map[string]any{"resourceType": grant.ResourceType, "resourceId": grant.ResourceID},
		"member-offer:"+sourceID, now); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	s.invalidateGovernance(ctx, invalidationReasonUpdated)
	return sourceID, nil
}

// SetOfferPaused pauses/resumes an active offer.
func (s *Store) SetOfferPaused(ctx context.Context, grantID, actorID string, pause bool, isAdmin bool, now string) error {
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
	if pause && grant.Status != StatusActive {
		return failCode("state_conflict", "只有生效中的授权可以暂停")
	}
	if !pause && grant.Status != StatusPaused {
		return failCode("state_conflict", "只有暂停中的授权可以恢复")
	}
	if !pause {
		// Pausing must remain available while the owner is suspended; resuming
		// an offer is a reactivation and must respect the suspension freeze.
		if err := s.ensureNotSuspended(ctx, tx, grant.OwnerID); err != nil {
			return err
		}
		if actorID != grant.OwnerID {
			if err := s.ensureNotSuspended(ctx, tx, actorID); err != nil {
				return err
			}
		}
	}
	next := StatusPaused
	if !pause {
		next = StatusActive
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_grants")+`
		SET status = ?, updated_at = ? WHERE id = ? AND status = ?`), next, now, grantID, grant.Status); err != nil {
		return err
	}
	if err := s.recomputeGrantRuntimes(ctx, tx, grantID, actorID, now); err != nil {
		return err
	}
	notifType := "offer_paused"
	if !pause {
		notifType = "offer_resumed"
	}
	for _, target := range s.grantRuntimeGrantees(ctx, tx, grantID) {
		if err := s.insertNotification(ctx, tx, target, notifType, grantID, "", nil,
			fmt.Sprintf("%s:%s:%s", notifType, grantID, target), now); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.invalidateGovernance(ctx, invalidationReasonUpdated)
	return nil
}

func (s *Store) grantRuntimeGrantees(ctx context.Context, tx *sql.Tx, grantID string) []string {
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT DISTINCT ra.grantee_system_account_id
		FROM `+s.table("resource_authorization_sources")+` s
		JOIN `+s.table("resource_authorizations")+` ra ON ra.id = s.authorization_id
		WHERE s.grant_id = ?`), grantID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			out = append(out, id)
		}
	}
	return out
}

func (s *Store) recomputeGrantRuntimes(ctx context.Context, tx *sql.Tx, grantID, actor, now string) error {
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT id FROM `+s.table("resource_authorizations")+`
		WHERE id IN (SELECT authorization_id FROM `+s.table("resource_authorization_sources")+` WHERE grant_id = ?)
		ORDER BY id`), grantID)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := s.recomputeRuntime(ctx, tx, id, actor, now); err != nil {
			return err
		}
	}
	return nil
}

// RevokeOffer revokes all non-terminal sources and materializes runtime terminal
// states. Historical rows are retained.
func (s *Store) RevokeOffer(ctx context.Context, grantID, actorID string, isAdmin bool, now string) error {
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
	if grant.Status == StatusRevoked {
		return nil
	}
	if isTerminalStatus(grant.Status) {
		return failCode("state_conflict", "终态授权不可回收")
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_grants")+`
		SET status = 'revoked', ended_at = COALESCE(ended_at, ?), ended_reason = COALESCE(ended_reason, 'authorization_revoked'),
			pending_expires_at = NULL, updated_at = ? WHERE id = ?`), now, now, grantID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_sources")+`
		SET status = 'revoked', ended_at = COALESCE(ended_at, ?), ended_reason = COALESCE(ended_reason, 'authorization_revoked'),
			pending_expires_at = NULL, revoked_by = ?, revoked_at = ?, updated_at = ?
		WHERE grant_id = ? AND status IN ('pending','active','dormant','needs_review')`),
		now, actorID, now, now, grantID); err != nil {
		return err
	}
	if err := s.recomputeGrantRuntimes(ctx, tx, grantID, actorID, now); err != nil {
		return err
	}
	for _, target := range s.grantRuntimeGrantees(ctx, tx, grantID) {
		if err := s.insertNotification(ctx, tx, target, "offer_revoked", grantID, "", nil,
			"revoked:"+grantID+":"+target, now); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.invalidateGovernance(ctx, invalidationReasonRevoked)
	return nil
}

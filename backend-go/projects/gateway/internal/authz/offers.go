// Offer lifecycle for the simplified send-and-claim model: direct send on
// creation (team grants fan out one pending source per active member),
// pause/resume and revoke. Owner/team approval, cooldown, offer limits,
// suspensions and the notification audit trail no longer exist.
package authz

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// OfferInput is the create contract.
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
}

// OfferResult is the create response.
type OfferResult struct {
	GrantID  string `json:"grantId"`
	SourceID string `json:"sourceId,omitempty"`
	Status   string `json:"status"`
	Created  bool   `json:"created"`
}

type offerResourceInfo struct {
	ID            string
	OwnerID       string
	AccountExpiry *string
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
		return &info, nil
	case "group":
		err := q.QueryRowContext(ctx, `SELECT id, system_account_id
			FROM juhe_business.groups WHERE id = $1`, resourceID).Scan(&info.ID, &info.OwnerID)
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
	query += " AND g.status IN ('pending','active','paused') LIMIT 1"
	row, err := scanOffer(q.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// CreateOffer sends an authorization directly: the grant is created pending
// and every receiver immediately sees a claimable source. A personal grant
// carries one manual source; a team grant fans out one team source per active
// member (members joining later get theirs through the membership cascade).
// It never provisions an instance, writes group_accounts or binds groups.
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
	if isAdmin {
		if strings.TrimSpace(ownerScope) == "" || ownerScope != resource.OwnerID {
			return nil, failf("授权资源不存在")
		}
	} else if actorID != resource.OwnerID {
		return nil, failf("授权资源不存在")
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
	granteeUserID, teamID, err := s.resolveGrantee(ctx, tx, &input)
	if err != nil {
		return nil, err
	}
	if granteeUserID == resource.OwnerID {
		return nil, failCode("self_authorization_forbidden", "不能授权给资源所有者自己")
	}

	existing, err := s.findOpenOffer(ctx, tx, input.ResourceType, input.ResourceID, input.GranteeType, granteeUserID, teamID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		switch existing.Status {
		case StatusPending:
			sourceID := ""
			_ = tx.QueryRowContext(ctx, s.bind(`SELECT id FROM `+s.table("resource_authorization_sources")+`
				WHERE grant_id = ? AND status = 'pending'
				ORDER BY created_at DESC LIMIT 1`), existing.ID).Scan(&sourceID)
			return &OfferResult{GrantID: existing.ID, SourceID: sourceID, Status: existing.Status, Created: false}, nil
		case StatusActive, StatusPaused:
			return nil, failCode("offer_already_active", "该资源已授权给该接收方并处于生效状态")
		}
	}

	baseline, _, err := s.sourceCurrentRevision(ctx, tx, input.ResourceType, input.ResourceID, resource.OwnerID)
	if err != nil {
		return nil, err
	}
	var grantID string
	// 开放 grant（pending/active/paused）之外的旧 grant 一律不复用：重发就是
	// 一条全新 pending grant，历史终态行保持归档语义。
	err = tx.QueryRowContext(ctx, s.bind(`INSERT INTO `+s.table("resource_authorization_grants")+`
		(id, resource_type, resource_id, resource_owner_system_account_id, grantee_type,
		 grantee_system_account_id, grantee_team_id, scope, status, remark, expires_at,
		 created_by, source_revision, expiry_cycle, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), 'use', ?, ?, ?, ?, ?, 0, ?, ?)
		RETURNING id`),
		newGrantID(s.now), input.ResourceType, input.ResourceID, resource.OwnerID, input.GranteeType,
		granteeUserID, teamID, StatusPending, nullIfEmpty(stringValue(input.Remark)),
		nullIfEmpty(stringValue(input.ExpiresAt)), actorID, baseline, now, now).Scan(&grantID)
	if err != nil {
		return nil, err
	}

	sourceID := ""
	if input.GranteeType == "system_account" {
		runtime, err := s.ensureRuntime(ctx, tx, input.ResourceType, input.ResourceID, resource.OwnerID, granteeUserID, actorID, now)
		if err != nil {
			return nil, err
		}
		if runtime.Status != StatusPending {
			if err := s.resetRuntimeForOffer(ctx, tx, runtime.ID, now); err != nil {
				return nil, err
			}
		}
		sourceID, err = s.appendManualPendingSource(ctx, tx, grantID, input.ResourceType, input.ResourceID,
			resource.OwnerID, granteeUserID, baseline, 0, actorID, now, stringValue(input.ExpiresAt))
		if err != nil {
			return nil, err
		}
		if _, err := s.recomputeRuntime(ctx, tx, runtime.ID, actorID, now); err != nil {
			return nil, err
		}
	} else {
		members, err := s.teamActiveMembers(ctx, tx, teamID, resource.OwnerID)
		if err != nil {
			return nil, err
		}
		for _, memberID := range members {
			if err := s.appendTeamPendingSource(ctx, tx, grantID, teamID, input.ResourceType,
				input.ResourceID, resource.OwnerID, memberID, baseline, 0, actorID, now); err != nil {
				return nil, err
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.invalidateGovernance(ctx, invalidationReasonCreated)
	return &OfferResult{GrantID: grantID, SourceID: sourceID, Status: StatusPending, Created: true}, nil
}

func (s *Store) resetRuntimeForOffer(ctx context.Context, tx *sql.Tx, runtimeID, now string) error {
	_, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorizations")+`
		SET status = 'pending',
			effective_source_type = NULL, effective_source_team_id = NULL,
			revoked_by = NULL, revoked_at = NULL, revoked_reason = NULL,
			last_source_changed_at = ?, updated_at = ?
		WHERE id = ?`), now, now, runtimeID)
	return err
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func (s *Store) appendManualPendingSource(ctx context.Context, tx *sql.Tx, grantID, resourceType, resourceID,
	ownerID, granteeID, revision string, expiryCycle int, actor, now, expiresAt string) (string, error) {
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
		(id, grant_id, authorization_id, source_type, source_team_id, status, expiry_cycle,
		 created_by, created_at, updated_at)
		SELECT ?, ?, ra.id, 'manual', NULL, 'pending', ?, ?, ?, ?
		FROM `+s.table("resource_authorizations")+` ra
		WHERE ra.resource_type = ? AND ra.resource_id = ? AND ra.grantee_system_account_id = ?`),
		id, grantID, expiryCycle, actor, now, now, resourceType, resourceID, granteeID)
	if err != nil {
		return "", err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return "", fmt.Errorf("authz: receiver runtime missing for grant %s", grantID)
	}
	_ = revision
	_ = expiresAt
	return id, nil
}

// teamActiveMembers lists the active members eligible for the initial fan-out:
// every active account except the resource owner.
func (s *Store) teamActiveMembers(ctx context.Context, tx *sql.Tx, teamID, ownerID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT m.system_account_id
		FROM `+s.table("system_team_members")+` m
		JOIN `+s.table("system_accounts")+` a ON a.id = m.system_account_id
		WHERE m.team_id = ? AND m.status = 'active' AND a.status = 'active' AND a.deleted_at IS NULL
		  AND m.system_account_id <> ?
		ORDER BY m.system_account_id`), teamID, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// appendTeamPendingSource creates the member's pending team source on their
// runtime unless that grant already has a pending/active source for them.
func (s *Store) appendTeamPendingSource(ctx context.Context, tx *sql.Tx, grantID, teamID, resourceType, resourceID,
	ownerID, memberID, revision string, expiryCycle int, actor, now string) error {
	runtime, err := s.ensureRuntime(ctx, tx, resourceType, resourceID, ownerID, memberID, actor, now)
	if err != nil {
		return err
	}
	var existing string
	err = tx.QueryRowContext(ctx, s.bind(`SELECT id FROM `+s.table("resource_authorization_sources")+`
		WHERE grant_id = ? AND authorization_id = ? AND source_type = 'team' AND status IN ('pending','active') LIMIT 1`),
		grantID, runtime.ID).Scan(&existing)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	sourceID := newSourceID(s.now)
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("resource_authorization_sources")+`
		(id, grant_id, authorization_id, source_type, source_team_id, status, expiry_cycle,
		 created_by, created_at, updated_at)
		VALUES (?, ?, ?, 'team', ?, 'pending', ?, ?, ?, ?)`),
		sourceID, grantID, runtime.ID, teamID, expiryCycle, actor, now, now); err != nil {
		return err
	}
	if runtime.Status != StatusPending {
		if err := s.resetRuntimeForOffer(ctx, tx, runtime.ID, now); err != nil {
			return err
		}
	}
	_ = revision
	if _, err := s.recomputeRuntime(ctx, tx, runtime.ID, actor, now); err != nil {
		return err
	}
	return nil
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
	if err := tx.Commit(); err != nil {
		return err
	}
	s.invalidateGovernance(ctx, invalidationReasonUpdated)
	return nil
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
			updated_at = ? WHERE id = ?`), now, now, grantID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_sources")+`
		SET status = 'revoked', ended_at = COALESCE(ended_at, ?), ended_reason = COALESCE(ended_reason, 'authorization_revoked'),
			revoked_by = ?, revoked_at = ?, updated_at = ?
		WHERE grant_id = ? AND status IN ('pending','active')`),
		now, actorID, now, now, grantID); err != nil {
		return err
	}
	if err := s.recomputeGrantRuntimes(ctx, tx, grantID, actorID, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.invalidateGovernance(ctx, invalidationReasonRevoked)
	return nil
}

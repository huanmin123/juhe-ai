// Governance helpers: blocklist, cooldown, per-grantor/grantee limits,
// administrator suspension and in-app notifications.
package authz

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Notification is one authorization inbox entry.
type Notification struct {
	ID        string  `json:"id"`
	Type      string  `json:"type"`
	GrantID   *string `json:"grantId,omitempty"`
	SourceID  *string `json:"sourceId,omitempty"`
	Payload   any     `json:"payload,omitempty"`
	ReadAt    *string `json:"readAt,omitempty"`
	CreatedAt string  `json:"createdAt"`
}

func (s *Store) isSuspended(ctx context.Context, q revisionQueryer, accountID string) (bool, error) {
	var count int
	err := q.QueryRowContext(ctx, `SELECT count(*) FROM juhe_business.system_account_authorization_suspensions
		WHERE system_account_id = $1 AND lifted_at IS NULL`, accountID).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *Store) ensureNotSuspended(ctx context.Context, q revisionQueryer, accountID string) error {
	suspended, err := s.isSuspended(ctx, q, accountID)
	if err != nil {
		return err
	}
	if suspended {
		return failCode("authorization_suspended", "该账户已被暂停发起授权")
	}
	return nil
}

// isBlocked reports whether grantee has blocked owner or the exact resource.
func (s *Store) isBlocked(ctx context.Context, q revisionQueryer, granteeID, ownerID, resourceType, resourceID string) (bool, error) {
	var count int
	err := q.QueryRowContext(ctx, `SELECT count(*) FROM juhe_business.resource_authorization_blocks
		WHERE blocker_system_account_id = $1 AND (
			(blocked_resource_type IS NULL AND blocked_system_account_id = $2)
			OR (blocked_resource_type = $3 AND blocked_resource_id = $4)
		)`, granteeID, ownerID, resourceType, resourceID).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *Store) checkBlocked(ctx context.Context, q revisionQueryer, granteeID, ownerID, resourceType, resourceID string) error {
	blocked, err := s.isBlocked(ctx, q, granteeID, ownerID, resourceType, resourceID)
	if err != nil {
		return err
	}
	if blocked {
		return errOfferSuppressed
	}
	return nil
}

func (s *Store) checkCooldown(ctx context.Context, q revisionQueryer, grantorID, granteeType, granteeUserID, teamID, memberID, resourceType, resourceID, now string) error {
	var count int
	switch granteeType {
	case "system_account":
		err := q.QueryRowContext(ctx, `SELECT count(*) FROM juhe_business.resource_authorization_offer_cooldowns
			WHERE grantor_system_account_id = $1 AND grantee_type = 'system_account'
			  AND grantee_system_account_id = $2 AND resource_type = $3 AND resource_id = $4 AND until_at > $5`,
			grantorID, granteeUserID, resourceType, resourceID, now).Scan(&count)
		if err != nil {
			return err
		}
	case "team":
		err := q.QueryRowContext(ctx, `SELECT count(*) FROM juhe_business.resource_authorization_offer_cooldowns
			WHERE grantor_system_account_id = $1 AND grantee_type = 'team'
			  AND grantee_team_id = $2 AND resource_type = $3 AND resource_id = $4 AND until_at > $5`,
			grantorID, teamID, resourceType, resourceID, now).Scan(&count)
		if err != nil {
			return err
		}
	case "team_member":
		err := q.QueryRowContext(ctx, `SELECT count(*) FROM juhe_business.resource_authorization_offer_cooldowns
			WHERE grantor_system_account_id = $1 AND grantee_type = 'team_member'
			  AND grantee_team_id = $2 AND member_system_account_id = $3
			  AND resource_type = $4 AND resource_id = $5 AND until_at > $6`,
			grantorID, teamID, memberID, resourceType, resourceID, now).Scan(&count)
		if err != nil {
			return err
		}
	}
	if count > 0 {
		return errOfferSuppressed
	}
	return nil
}

func (s *Store) upsertCooldown(ctx context.Context, tx *sql.Tx, grantorID, granteeType, granteeUserID, teamID, memberID, resourceType, resourceID, reason, now string) error {
	until := nowPlus(parseTimeOrNow(now), OfferCooldown)
	var existingID string
	var err error
	switch granteeType {
	case "system_account":
		err = tx.QueryRowContext(ctx, s.bind(`SELECT id FROM `+s.table("resource_authorization_offer_cooldowns")+`
			WHERE grantor_system_account_id = ? AND grantee_type = 'system_account'
			  AND grantee_system_account_id = ? AND resource_type = ? AND resource_id = ? LIMIT 1`),
			grantorID, granteeUserID, resourceType, resourceID).Scan(&existingID)
	case "team":
		err = tx.QueryRowContext(ctx, s.bind(`SELECT id FROM `+s.table("resource_authorization_offer_cooldowns")+`
			WHERE grantor_system_account_id = ? AND grantee_type = 'team'
			  AND grantee_team_id = ? AND resource_type = ? AND resource_id = ? LIMIT 1`),
			grantorID, teamID, resourceType, resourceID).Scan(&existingID)
	case "team_member":
		err = tx.QueryRowContext(ctx, s.bind(`SELECT id FROM `+s.table("resource_authorization_offer_cooldowns")+`
			WHERE grantor_system_account_id = ? AND grantee_type = 'team_member'
			  AND grantee_team_id = ? AND member_system_account_id = ?
			  AND resource_type = ? AND resource_id = ? LIMIT 1`),
			grantorID, teamID, memberID, resourceType, resourceID).Scan(&existingID)
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if existingID != "" {
		_, err = tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("resource_authorization_offer_cooldowns")+`
			SET until_at = ?, reason = ? WHERE id = ?`), until, reason, existingID)
		return err
	}
	_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("resource_authorization_offer_cooldowns")+`
		(id, grantor_system_account_id, grantee_type, grantee_system_account_id, grantee_team_id,
		 member_system_account_id, resource_type, resource_id, until_at, reason, created_at)
		VALUES (?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, ?, ?)`),
		"authcooldown_"+randomSuffix(), grantorID, granteeType,
		granteeUserID, teamID, memberID, resourceType, resourceID, until, reason, now)
	return err
}

// enforceOfferLimits applies the per-grantor and per-grantee caps. All
// counts use the resource owner (grantor) as the key, never the admin creator.
func (s *Store) enforceOfferLimits(ctx context.Context, q revisionQueryer, grantorID, granteeType, granteeUserID, teamID, resourceType, resourceID, now string) error {
	var count int
	windowStart := nowMinus24h(now)
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM juhe_business.resource_authorization_grants
		WHERE resource_owner_system_account_id = $1 AND status NOT IN ('expired','revoked','returned','declined')`,
		grantorID).Scan(&count); err != nil {
		return err
	}
	if count >= MaxPendingOffersPerGrantor {
		return errOfferSuppressed
	}
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM juhe_business.resource_authorization_grants
		WHERE resource_owner_system_account_id = $1 AND created_at > $2`,
		grantorID, windowStart).Scan(&count); err != nil {
		return err
	}
	if count >= MaxOffersPerGrantor24h {
		return errOfferSuppressed
	}
	// Per-grantor → same receiver 24h windows.
	switch granteeType {
	case "system_account":
		if err := q.QueryRowContext(ctx, `SELECT count(*) FROM juhe_business.resource_authorization_grants
			WHERE resource_owner_system_account_id = $1 AND grantee_type = 'system_account'
			  AND grantee_system_account_id = $2 AND created_at > $3`,
			grantorID, granteeUserID, windowStart).Scan(&count); err != nil {
			return err
		}
		if count >= MaxOffersPerGrantorPerGrantee24h {
			return errOfferSuppressed
		}
	case "team", "team_member":
		if err := q.QueryRowContext(ctx, `SELECT count(*) FROM juhe_business.resource_authorization_grants
			WHERE resource_owner_system_account_id = $1 AND grantee_type = 'team'
			  AND grantee_team_id = $2 AND created_at > $3`,
			grantorID, teamID, windowStart).Scan(&count); err != nil {
			return err
		}
		if count >= MaxTeamOffersPerGrantorPerTeam24h {
			return errOfferSuppressed
		}
		if err := q.QueryRowContext(ctx, `SELECT count(*) FROM juhe_business.resource_authorization_grants
			WHERE resource_owner_system_account_id = $1 AND grantee_type = 'team' AND created_at > $2`,
			grantorID, windowStart).Scan(&count); err != nil {
			return err
		}
		if count >= MaxTeamOffersPerGrantor24h {
			return errOfferSuppressed
		}
		// Team member offers count at the source level: one grant may fan out
		// to many members, so grant rows cannot enforce this window.
		if granteeType == "team_member" {
			if err := q.QueryRowContext(ctx, `SELECT count(*) FROM juhe_business.resource_authorization_sources s
				JOIN juhe_business.resource_authorization_grants g ON g.id = s.grant_id
				JOIN juhe_business.resource_authorizations ra ON ra.id = s.authorization_id
				WHERE g.resource_owner_system_account_id = $1
				  AND ra.grantee_system_account_id = $2 AND s.created_at > $3`,
				grantorID, granteeUserID, windowStart).Scan(&count); err != nil {
				return err
			}
			if count >= MaxOffersPerGrantorPerGrantee24h {
				return errOfferSuppressed
			}
		}
	}
	// Distinct receivers touched by this grantor in the last 24h.
	if err := q.QueryRowContext(ctx, `SELECT count(DISTINCT COALESCE(grantee_system_account_id, grantee_team_id))
		FROM juhe_business.resource_authorization_grants
		WHERE resource_owner_system_account_id = $1 AND created_at > $2`,
		grantorID, windowStart).Scan(&count); err != nil {
		return err
	}
	if count >= MaxDistinctGranteesPerGrantor24h {
		return errOfferSuppressed
	}
	if granteeType == "system_account" || granteeType == "team_member" {
		if err := s.enforceGranteeSourceLimits(ctx, q, granteeUserID); err != nil {
			return err
		}
	}
	if granteeType == "team" {
		if err := q.QueryRowContext(ctx, `SELECT count(*) FROM juhe_business.resource_authorization_grants
			WHERE grantee_type = 'team' AND grantee_team_id = $1
			  AND status IN ('pending_team_approval','pending_member_acceptance','active','paused')`,
			teamID).Scan(&count); err != nil {
			return err
		}
		if count >= MaxTeamActiveGrantCount {
			return failf("单个授权团队最多支持 20 条有效授权，请先回收或停用部分授权")
		}
		if err := q.QueryRowContext(ctx, `SELECT count(*) FROM juhe_business.resource_authorization_grants
			WHERE grantee_type = 'team' AND grantee_team_id = $1 AND status = 'pending_team_approval'`,
			teamID).Scan(&count); err != nil {
			return err
		}
		if count >= MaxPendingTeamApprovalsPerTeam {
			return errOfferSuppressed
		}
	}
	return nil
}

// enforceGranteeSourceLimits applies the receiver-side pending/active/instance
// caps shared by direct offers, team approval fanout and re-invite.
func (s *Store) enforceGranteeSourceLimits(ctx context.Context, q revisionQueryer, granteeUserID string) error {
	if strings.TrimSpace(granteeUserID) == "" {
		return nil
	}
	var count int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM juhe_business.resource_authorization_sources s
		JOIN juhe_business.resource_authorizations ra ON ra.id = s.authorization_id
		WHERE ra.grantee_system_account_id = $1 AND s.status = 'pending'`, granteeUserID).Scan(&count); err != nil {
		return err
	}
	if count >= MaxPendingSourcesPerGrantee {
		return errOfferSuppressed
	}
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM juhe_business.resource_authorization_sources s
		JOIN juhe_business.resource_authorizations ra ON ra.id = s.authorization_id
		WHERE ra.grantee_system_account_id = $1 AND s.status IN ('active','dormant','needs_review')`, granteeUserID).Scan(&count); err != nil {
		return err
	}
	if count >= MaxActiveSourcesPerGrantee {
		return errOfferSuppressed
	}
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM juhe_business.accounts
		WHERE system_account_id = $1 AND authorization_instance_authorization_id IS NOT NULL AND deleted_at IS NULL`,
		granteeUserID).Scan(&count); err != nil {
		return err
	}
	if count >= MaxAuthorizedInstancesPerGrantee {
		return errOfferSuppressed
	}
	return nil
}

func nowMinus24h(now string) string {
	return formatTime(parseTimeOrNow(now).Add(-24 * time.Hour))
}

// insertNotification is dedupe-safe (unique dedupe_key).
func (s *Store) insertNotification(ctx context.Context, tx *sql.Tx, systemAccountID, notificationType, grantID, sourceID string, payload any, dedupeKey, now string) error {
	if strings.TrimSpace(systemAccountID) == "" {
		return nil
	}
	body := ""
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = string(encoded)
	}
	_, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("authorization_notifications")+`
		(id, system_account_id, type, grant_id, source_id, payload_json, dedupe_key, created_at)
		VALUES (?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, ?)
		ON CONFLICT (dedupe_key) DO NOTHING`),
		"authnotif_"+randomSuffix(), systemAccountID, notificationType, grantID, sourceID, body, dedupeKey, now)
	return err
}

// ListNotifications returns the caller's inbox.
func (s *Store) ListNotifications(ctx context.Context, systemAccountID string, page, pageSize int) ([]Notification, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 200 {
		pageSize = 50
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT id, type, COALESCE(grant_id, ''), COALESCE(source_id, ''),
			COALESCE(payload_json, ''), COALESCE(read_at, ''), created_at
		FROM `+s.table("authorization_notifications")+`
		WHERE system_account_id = ? ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`),
		systemAccountID, pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Notification{}
	for rows.Next() {
		var item Notification
		var grantID, sourceID, payload, readAt string
		if err := rows.Scan(&item.ID, &item.Type, &grantID, &sourceID, &payload, &readAt, &item.CreatedAt); err != nil {
			return nil, 0, err
		}
		if grantID != "" {
			item.GrantID = &grantID
		}
		if sourceID != "" {
			item.SourceID = &sourceID
		}
		if readAt != "" {
			item.ReadAt = &readAt
		}
		if payload != "" {
			var decoded any
			if json.Unmarshal([]byte(payload), &decoded) == nil {
				item.Payload = decoded
			}
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var total int
	if err := s.db.QueryRowContext(ctx, s.bind(`SELECT count(*) FROM `+s.table("authorization_notifications")+`
		WHERE system_account_id = ?`), systemAccountID).Scan(&total); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// MarkNotificationRead only touches the caller's row.
func (s *Store) MarkNotificationRead(ctx context.Context, systemAccountID, notificationID, now string) (bool, error) {
	result, err := s.db.ExecContext(ctx, s.bind(`UPDATE `+s.table("authorization_notifications")+`
		SET read_at = COALESCE(read_at, ?) WHERE id = ? AND system_account_id = ?`),
		now, notificationID, systemAccountID)
	if err != nil {
		return false, err
	}
	affected, _ := result.RowsAffected()
	return affected == 1, nil
}

// InboxSummary returns the badge counts for the authorization center.
type InboxSummary struct {
	PendingSources      int `json:"pendingSources"`
	NeedsReviewSources  int `json:"needsReviewSources"`
	PendingBindings     int `json:"pendingBindings"`
	TeamApprovals       int `json:"teamApprovals"`
	OwnerApprovals      int `json:"ownerApprovals"`
	UnreadNotifications int `json:"unreadNotifications"`
}

func (s *Store) InboxSummary(ctx context.Context, systemAccountID string, now string) (InboxSummary, error) {
	var out InboxSummary
	queries := []struct {
		target *int
		query  string
		args   []any
	}{
		{&out.PendingSources, `SELECT count(*) FROM juhe_business.resource_authorization_sources s
			JOIN juhe_business.resource_authorizations ra ON ra.id = s.authorization_id
			WHERE ra.grantee_system_account_id = $1 AND s.status = 'pending'
			  AND (s.pending_expires_at IS NULL OR s.pending_expires_at > $2)`, []any{systemAccountID, now}},
		{&out.NeedsReviewSources, `SELECT count(*) FROM juhe_business.resource_authorization_sources s
			JOIN juhe_business.resource_authorizations ra ON ra.id = s.authorization_id
			WHERE ra.grantee_system_account_id = $1 AND s.status = 'needs_review'`, []any{systemAccountID}},
		{&out.PendingBindings, `SELECT count(*) FROM juhe_business.resource_authorizations ra
			WHERE ra.grantee_system_account_id = $1 AND ra.status = 'active'
			  AND ra.resource_type = 'account'
			  AND EXISTS (SELECT 1 FROM juhe_business.accounts a WHERE a.authorization_instance_authorization_id = ra.id AND a.deleted_at IS NULL)
			  AND NOT EXISTS (SELECT 1 FROM juhe_business.group_accounts ga WHERE ga.account_authorization_id = ra.id AND ga.enabled = 1)
			UNION ALL
			SELECT count(*) FROM juhe_business.resource_authorizations ra
			JOIN juhe_business.group_authorization_settings s ON s.authorization_id = ra.id
			WHERE ra.grantee_system_account_id = $1 AND ra.status = 'active' AND s.enabled = 0`, []any{systemAccountID}},
		{&out.TeamApprovals, `SELECT count(*) FROM juhe_business.resource_authorization_grants g
			JOIN juhe_business.system_teams t ON t.id = g.grantee_team_id
			WHERE g.grantee_type = 'team' AND g.status = 'pending_team_approval' AND t.created_by = $1`, []any{systemAccountID}},
		{&out.OwnerApprovals, `SELECT count(*) FROM juhe_business.resource_authorization_grants
			WHERE resource_owner_system_account_id = $1 AND status = 'pending_owner_approval'`, []any{systemAccountID}},
		{&out.UnreadNotifications, `SELECT count(*) FROM juhe_business.authorization_notifications
			WHERE system_account_id = $1 AND read_at IS NULL`, []any{systemAccountID}},
	}
	for _, item := range queries {
		if strings.Contains(item.query, "UNION ALL") {
			// Two counts: sum them.
			rows, err := s.db.QueryContext(ctx, item.query, item.args...)
			if err != nil {
				return out, err
			}
			sum := 0
			for rows.Next() {
				var value int
				if err := rows.Scan(&value); err != nil {
					rows.Close()
					return out, err
				}
				sum += value
			}
			rows.Close()
			*item.target = sum
			continue
		}
		if err := s.db.QueryRowContext(ctx, item.query, item.args...).Scan(item.target); err != nil {
			return out, err
		}
	}
	return out, nil
}

// Block is the receiver's persisted blocklist row.
type Block struct {
	ID             string  `json:"id"`
	BlockedAccount *string `json:"blockedSystemAccountId,omitempty"`
	ResourceType   *string `json:"blockedResourceType,omitempty"`
	ResourceID     *string `json:"blockedResourceId,omitempty"`
	Reason         *string `json:"reason,omitempty"`
	CreatedAt      string  `json:"createdAt"`
	UpdatedAt      string  `json:"updatedAt"`
}

func (s *Store) ListBlocks(ctx context.Context, systemAccountID string) ([]Block, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT id, COALESCE(blocked_system_account_id, ''),
		COALESCE(blocked_resource_type, ''), COALESCE(blocked_resource_id, ''), COALESCE(reason, ''), created_at, updated_at
		FROM `+s.table("resource_authorization_blocks")+`
		WHERE blocker_system_account_id = ? ORDER BY created_at DESC, id DESC`), systemAccountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Block{}
	for rows.Next() {
		var item Block
		var account, resourceType, resourceID, reason string
		if err := rows.Scan(&item.ID, &account, &resourceType, &resourceID, &reason, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		if account != "" {
			item.BlockedAccount = &account
		}
		if resourceType != "" {
			item.ResourceType = &resourceType
		}
		if resourceID != "" {
			item.ResourceID = &resourceID
		}
		if reason != "" {
			item.Reason = &reason
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) DeleteBlock(ctx context.Context, systemAccountID, blockID string) (bool, error) {
	result, err := s.db.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("resource_authorization_blocks")+`
		WHERE id = ? AND blocker_system_account_id = ?`), blockID, systemAccountID)
	if err != nil {
		return false, err
	}
	affected, _ := result.RowsAffected()
	return affected == 1, nil
}

// AdminListBlocks returns all blocks for abuse investigation.
func (s *Store) AdminListBlocks(ctx context.Context) ([]Block, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, COALESCE(blocked_system_account_id, ''),
		COALESCE(blocked_resource_type, ''), COALESCE(blocked_resource_id, ''), COALESCE(reason, ''), created_at, updated_at
		FROM juhe_business.resource_authorization_blocks ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Block{}
	for rows.Next() {
		var item Block
		var account, resourceType, resourceID, reason string
		if err := rows.Scan(&item.ID, &account, &resourceType, &resourceID, &reason, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		if account != "" {
			item.BlockedAccount = &account
		}
		if resourceType != "" {
			item.ResourceType = &resourceType
		}
		if resourceID != "" {
			item.ResourceID = &resourceID
		}
		if reason != "" {
			item.Reason = &reason
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// SuspensionStatus is the current administrator suspension for an account.
type SuspensionStatus struct {
	Suspended   bool    `json:"suspended"`
	SuspendedBy *string `json:"suspendedBy,omitempty"`
	Reason      *string `json:"reason,omitempty"`
	SuspendedAt *string `json:"suspendedAt,omitempty"`
}

func (s *Store) GetSuspension(ctx context.Context, accountID string) (SuspensionStatus, error) {
	var status SuspensionStatus
	var suspendedBy, reason, suspendedAt sql.NullString
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT suspended_by, reason, suspended_at
		FROM `+s.table("system_account_authorization_suspensions")+`
		WHERE system_account_id = ? AND lifted_at IS NULL ORDER BY suspended_at DESC LIMIT 1`), accountID).
		Scan(&suspendedBy, &reason, &suspendedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return status, nil
	}
	if err != nil {
		return status, err
	}
	status.Suspended = true
	if suspendedBy.Valid {
		status.SuspendedBy = &suspendedBy.String
	}
	if reason.Valid {
		status.Reason = &reason.String
	}
	if suspendedAt.Valid {
		status.SuspendedAt = &suspendedAt.String
	}
	return status, nil
}

func (s *Store) Suspend(ctx context.Context, accountID, adminID, reason, now string) error {
	if _, err := s.db.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("system_account_authorization_suspensions")+`
		(id, system_account_id, suspended_by, reason, suspended_at)
		VALUES (?, ?, ?, NULLIF(?, ''), ?)`),
		"authsusp_"+randomSuffix(), accountID, adminID, reason, now); err != nil {
		return err
	}
	return nil
}

func (s *Store) Unsuspend(ctx context.Context, accountID, adminID, now string) (bool, error) {
	result, err := s.db.ExecContext(ctx, s.bind(`UPDATE `+s.table("system_account_authorization_suspensions")+`
		SET lifted_at = ?, lifted_by = ? WHERE system_account_id = ? AND lifted_at IS NULL`),
		now, adminID, accountID)
	if err != nil {
		return false, err
	}
	affected, _ := result.RowsAffected()
	return affected > 0, nil
}

var _ = fmt.Sprintf

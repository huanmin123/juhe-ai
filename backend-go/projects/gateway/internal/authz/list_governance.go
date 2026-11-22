// Read models for the simplified send-and-claim authorization center.
package authz

import (
	"context"
	"database/sql"
	"strings"
)

type SourceSummary struct {
	SourceID          string `json:"sourceId"`
	GrantID           string `json:"grantId"`
	RuntimeID         string `json:"authorizationId"`
	ResourceType      string `json:"resourceType"`
	ResourceID        string `json:"resourceId"`
	ResourceName      string `json:"resourceName"`
	ResourceOwnerID   string `json:"resourceOwnerSystemAccountId"`
	ResourceOwnerName string `json:"resourceOwnerName"`
	GranteeType       string `json:"granteeType"`
	TeamID            string `json:"teamId,omitempty"`
	TeamName          string `json:"teamName,omitempty"`
	SourceType        string `json:"sourceType"`
	Status            string `json:"status"`
	RuntimeStatus     string `json:"runtimeStatus"`
	ExpiresAt         string `json:"expiresAt,omitempty"`
	AcceptedAt        string `json:"acceptedAt,omitempty"`
	Bound             bool   `json:"bound"`
	CreatedAt         string `json:"createdAt"`
	CanAccept         bool   `json:"canAccept"`
	CanDecline        bool   `json:"canDecline"`
	CanReturn         bool   `json:"canReturn"`
}

// inboundSourceQuery assembles the receiver-visible source projection with
// the given WHERE clause. 绑定列是授权域事实（领取即绑定过分组），不看
// group_accounts.enabled：该开关是账户域运行时投射（授权暂停/实例停用会
// 置 0），反映的是调度可用性，不是授权绑定本身——授权中心只维护授权状态。
func (s *Store) inboundSourceQuery(where string) string {
	return `SELECT s.id, COALESCE(s.grant_id, ''), s.authorization_id,
	g.resource_type, g.resource_id,
	COALESCE(a.name, g2.name, ''), g.resource_owner_system_account_id,
	COALESCE(owner.display_name, owner.username, ''),
	g.grantee_type, g.status, COALESCE(g.grantee_team_id, ''), COALESCE(t.name, ''),
	s.source_type, s.status, ra.status,
	COALESCE(ra.expires_at, ''), COALESCE(s.accepted_at, ''),
	COALESCE((SELECT 1 FROM ` + s.table("group_accounts") + ` ga
		WHERE ga.account_id = inst.id
			AND ga.system_account_id = ra.grantee_system_account_id
			AND ga.account_authorization_id = ra.id
		LIMIT 1), 0),
	s.created_at
	FROM ` + s.table("resource_authorization_sources") + ` s
	JOIN ` + s.table("resource_authorization_grants") + ` g ON g.id = s.grant_id
	JOIN ` + s.table("resource_authorizations") + ` ra ON ra.id = s.authorization_id
	LEFT JOIN ` + s.table("accounts") + ` a ON g.resource_type = 'account' AND a.id = g.resource_id
	LEFT JOIN ` + s.table("groups") + ` g2 ON g.resource_type = 'group' AND g2.id = g.resource_id
	LEFT JOIN ` + s.table("system_accounts") + ` owner ON owner.id = g.resource_owner_system_account_id
	LEFT JOIN ` + s.table("system_teams") + ` t ON t.id = g.grantee_team_id
	LEFT JOIN ` + s.table("accounts") + ` inst ON inst.authorization_instance_authorization_id = ra.id
		AND inst.system_account_id = ra.grantee_system_account_id AND inst.deleted_at IS NULL
	WHERE ` + where
}

// ListInboundSources returns the receiver's authorization-center rows.
func (s *Store) ListInboundSources(ctx context.Context, accountID string) ([]SourceSummary, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(s.inboundSourceQuery(`ra.grantee_system_account_id = ?`)+
		` ORDER BY s.created_at DESC, s.id DESC LIMIT 500`), accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return s.scanInboundRows(ctx, rows)
}

func (s *Store) scanInboundRows(ctx context.Context, rows *sql.Rows) ([]SourceSummary, error) {
	out := []SourceSummary{}
	for rows.Next() {
		var item SourceSummary
		var bound int
		var grantStatus string
		if err := rows.Scan(&item.SourceID, &item.GrantID, &item.RuntimeID,
			&item.ResourceType, &item.ResourceID, &item.ResourceName, &item.ResourceOwnerID,
			&item.ResourceOwnerName, &item.GranteeType, &grantStatus, &item.TeamID, &item.TeamName,
			&item.SourceType, &item.Status, &item.RuntimeStatus,
			&item.ExpiresAt, &item.AcceptedAt,
			&bound, &item.CreatedAt); err != nil {
			return nil, err
		}
		item.Bound = bound == 1
		// Action flags mirror the store gates: a pending source is claimable
		// unless its parent grant is paused; decline only requires a pending
		// source; return needs a claimed source.
		item.CanAccept = item.Status == StatusPending &&
			(grantStatus == StatusPending || grantStatus == StatusActive)
		item.CanDecline = item.Status == StatusPending
		item.CanReturn = item.Status == StatusActive ||
			(grantStatus == StatusPaused && item.Status == StatusPending)
		out = append(out, item)
	}
	return out, rows.Err()
}

// OfferSummary is one outbound/owned grant row.
type OfferSummary struct {
	GrantID         string `json:"grantId"`
	ResourceType    string `json:"resourceType"`
	ResourceID      string `json:"resourceId"`
	ResourceName    string `json:"resourceName"`
	ResourceOwnerID string `json:"resourceOwnerSystemAccountId,omitempty"`
	GranteeType     string `json:"granteeType"`
	GranteeUserID   string `json:"granteeSystemAccountId,omitempty"`
	GranteeName     string `json:"granteeName,omitempty"`
	TeamID          string `json:"teamId,omitempty"`
	TeamName        string `json:"teamName,omitempty"`
	Status          string `json:"status"`
	StatusReason    string `json:"statusReason,omitempty"`
	Remark          string `json:"remark,omitempty"`
	ExpiresAt       string `json:"expiresAt,omitempty"`
	CreatedBy       string `json:"createdBy"`
	CreatedByName   string `json:"createdByName,omitempty"`
	PendingCount    int    `json:"pendingCount"`
	ActiveCount     int    `json:"activeCount"`
	TeamApprovedAt  string `json:"teamApprovedAt,omitempty"`
	OwnerApprovedAt string `json:"ownerApprovedAt,omitempty"`
	AcceptedAt      string `json:"acceptedAt,omitempty"`
	CreatedAt       string `json:"createdAt"`
	EndedAt         string `json:"endedAt,omitempty"`
	EndedReason     string `json:"endedReason,omitempty"`
}

func (s *Store) listOffers(ctx context.Context, where string, args ...any) ([]OfferSummary, error) {
	// accepted_at 取该 grant 最早领取时间；零已领取来源（pending 等）时
	// min() 为 NULL，必须 COALESCE 空串，否则 Scan 进 string 直接 500。
	query := `SELECT g.id, g.resource_type, g.resource_id, COALESCE(a.name, gr.name, ''),
			g.resource_owner_system_account_id,
			g.grantee_type, COALESCE(g.grantee_system_account_id, ''), COALESCE(gu.display_name, gu.username, ''),
			COALESCE(g.grantee_team_id, ''), COALESCE(t.name, ''),
			g.status, COALESCE(g.ended_reason, ''), COALESCE(g.remark, ''),
			COALESCE(g.expires_at, ''),
			g.created_by, COALESCE(cu.display_name, cu.username, ''),
			COALESCE((SELECT min(s.accepted_at) FROM ` + s.table("resource_authorization_sources") + ` s
				WHERE s.grant_id = g.id AND s.accepted_at IS NOT NULL), ''),
			g.created_at, COALESCE(g.ended_at, ''),
			(SELECT count(*) FROM ` + s.table("resource_authorization_sources") + ` s WHERE s.grant_id = g.id AND s.status = 'pending'),
			(SELECT count(*) FROM ` + s.table("resource_authorization_sources") + ` s WHERE s.grant_id = g.id AND s.status = 'active')
		FROM ` + s.table("resource_authorization_grants") + ` g
		LEFT JOIN ` + s.table("accounts") + ` a ON g.resource_type = 'account' AND a.id = g.resource_id
		LEFT JOIN ` + s.table("groups") + ` gr ON g.resource_type = 'group' AND gr.id = g.resource_id
		LEFT JOIN ` + s.table("system_accounts") + ` gu ON gu.id = g.grantee_system_account_id
		LEFT JOIN ` + s.table("system_teams") + ` t ON t.id = g.grantee_team_id
		LEFT JOIN ` + s.table("system_accounts") + ` cu ON cu.id = g.created_by
		WHERE ` + where + ` ORDER BY g.created_at DESC, g.id DESC LIMIT 500`
	rows, err := s.db.QueryContext(ctx, s.bind(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OfferSummary{}
	for rows.Next() {
		var item OfferSummary
		if err := rows.Scan(&item.GrantID, &item.ResourceType, &item.ResourceID, &item.ResourceName,
			&item.ResourceOwnerID, &item.GranteeType, &item.GranteeUserID, &item.GranteeName,
			&item.TeamID, &item.TeamName,
			&item.Status, &item.StatusReason, &item.Remark, &item.ExpiresAt,
			&item.CreatedBy, &item.CreatedByName, &item.AcceptedAt,
			&item.CreatedAt, &item.EndedAt,
			&item.PendingCount, &item.ActiveCount); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// ListOutboundOffers is the sender view: grants on resources the account owns
// or created on someone's behalf, both personal and team grantees（原 owned
// 视角 = owner 维度的子集，已并入此处，页签只保留收发两面）。
func (s *Store) ListOutboundOffers(ctx context.Context, accountID string) ([]OfferSummary, error) {
	return s.listOffers(ctx, `(g.resource_owner_system_account_id = ? OR g.created_by = ?)`, accountID, accountID)
}

func (s *Store) ListAllOffers(ctx context.Context, status string) ([]OfferSummary, error) {
	if strings.TrimSpace(status) == "" {
		return s.listOffers(ctx, `1=1`)
	}
	return s.listOffers(ctx, `g.status = ?`, strings.TrimSpace(status))
}

// ListScopedOffers applies the administrator ?systemAccountId scope and the
// direction filter from the API contract: outbound = resources the account
// owns; inbound = direct grants plus grants to teams where the account is an
// active member; no direction = the union of both.
func (s *Store) ListScopedOffers(ctx context.Context, accountID, direction, status string) ([]OfferSummary, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return s.ListAllOffers(ctx, status)
	}
	inbound := `(g.grantee_type = 'system_account' AND g.grantee_system_account_id = ?) OR ` +
		`(g.grantee_type = 'team' AND EXISTS (
			SELECT 1 FROM ` + s.table("system_team_members") + ` m
			WHERE m.team_id = g.grantee_team_id AND m.system_account_id = ?))`
	outbound := `g.resource_owner_system_account_id = ?`
	where := ""
	args := []any{}
	switch strings.TrimSpace(direction) {
	case "outbound":
		where = outbound
		args = append(args, accountID)
	case "inbound":
		where = `(` + inbound + `)`
		args = append(args, accountID, accountID)
	case "":
		where = `((` + outbound + `) OR (` + inbound + `))`
		args = append(args, accountID, accountID, accountID)
	default:
		return nil, failf("direction 参数无效")
	}
	if status = strings.TrimSpace(status); status != "" {
		where += ` AND g.status = ?`
		args = append(args, status)
	}
	return s.listOffers(ctx, where, args...)
}

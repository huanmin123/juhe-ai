// Read models for the simplified send-and-claim authorization center.
package authz

import (
	"context"
	"database/sql"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-platform/authzrevision"
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
	VendorCode        string `json:"vendorCode,omitempty"`
	ProxySummary      string `json:"proxySummary,omitempty"`
	ExpiresAt         string `json:"expiresAt,omitempty"`
	AcceptedAt        string `json:"acceptedAt,omitempty"`
	InstanceID        string `json:"instanceId,omitempty"`
	Bound             bool   `json:"bound"`
	CreatedAt         string `json:"createdAt"`
	CanAccept         bool   `json:"canAccept"`
	CanDecline        bool   `json:"canDecline"`
	CanReturn         bool   `json:"canReturn"`
}

const inboundSourceColumns = `s.id, COALESCE(s.grant_id, ''), s.authorization_id,
	g.resource_type, g.resource_id,
	COALESCE(a.name, g2.name, ''), g.resource_owner_system_account_id,
	COALESCE(owner.display_name, owner.username, ''),
	g.grantee_type, g.status, COALESCE(g.grantee_team_id, ''), COALESCE(t.name, ''),
	s.source_type, s.status, ra.status,
	COALESCE(src.vendor_code, ''),
	COALESCE(ra.expires_at, ''), COALESCE(s.accepted_at, ''),
	COALESCE(inst.id, ''), COALESCE(ga.enabled, 0),
	s.created_at`

// inboundSourceQuery assembles the receiver-visible source projection with
// the given WHERE clause (group bindings read the live group_accounts rows).
func (s *Store) inboundSourceQuery(where string) string {
	return `SELECT ` + inboundSourceColumns + `
	FROM ` + s.table("resource_authorization_sources") + ` s
	JOIN ` + s.table("resource_authorization_grants") + ` g ON g.id = s.grant_id
	JOIN ` + s.table("resource_authorizations") + ` ra ON ra.id = s.authorization_id
	LEFT JOIN ` + s.table("accounts") + ` a ON g.resource_type = 'account' AND a.id = g.resource_id
	LEFT JOIN ` + s.table("groups") + ` g2 ON g.resource_type = 'group' AND g2.id = g.resource_id
	LEFT JOIN ` + s.table("system_accounts") + ` owner ON owner.id = g.resource_owner_system_account_id
	LEFT JOIN ` + s.table("system_teams") + ` t ON t.id = g.grantee_team_id
	LEFT JOIN ` + s.table("accounts") + ` src ON g.resource_type = 'account' AND src.id = g.resource_id
	LEFT JOIN ` + s.table("accounts") + ` inst ON inst.authorization_instance_authorization_id = ra.id
		AND inst.system_account_id = ra.grantee_system_account_id AND inst.deleted_at IS NULL
	LEFT JOIN ` + s.table("group_accounts") + ` ga ON ga.account_id = inst.id AND ga.enabled = 1
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
			&item.SourceType, &item.Status, &item.RuntimeStatus, &item.VendorCode,
			&item.ExpiresAt, &item.AcceptedAt,
			&item.InstanceID, &bound, &item.CreatedAt); err != nil {
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
		if item.ResourceType == "account" {
			s.enrichSourceMaterial(ctx, &item)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) enrichSourceMaterial(ctx context.Context, item *SourceSummary) {
	secret, err := s.revisionSecretOrFail()
	if err != nil {
		return
	}
	snapshot, err := authzrevision.Load(ctx, s.db, item.ResourceID, secret)
	if err != nil {
		return
	}
	if snapshot.ProxyProfile != nil {
		item.ProxySummary = snapshot.ProxyProfile.Type + " " + snapshot.ProxyProfile.Host
	} else if snapshot.ProxyPool != nil {
		item.ProxySummary = "pool " + snapshot.ProxyPool.ID
	}
	if snapshot.VendorCode != "" {
		item.VendorCode = snapshot.VendorCode
	}
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
	query := `SELECT g.id, g.resource_type, g.resource_id, COALESCE(a.name, gr.name, ''),
			g.resource_owner_system_account_id,
			g.grantee_type, COALESCE(g.grantee_system_account_id, ''), COALESCE(gu.display_name, gu.username, ''),
			COALESCE(g.grantee_team_id, ''), COALESCE(t.name, ''),
			g.status, COALESCE(g.ended_reason, ''), COALESCE(g.remark, ''),
			COALESCE(g.expires_at, ''),
			g.created_by, COALESCE(cu.display_name, cu.username, ''),
			(SELECT min(s.accepted_at) FROM ` + s.table("resource_authorization_sources") + ` s
				WHERE s.grant_id = g.id AND s.accepted_at IS NOT NULL),
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

func (s *Store) ListOutboundOffers(ctx context.Context, accountID string) ([]OfferSummary, error) {
	return s.listOffers(ctx, `(g.resource_owner_system_account_id = ? OR g.created_by = ?) AND g.grantee_type = 'system_account'`, accountID, accountID)
}

func (s *Store) ListOwnedOffers(ctx context.Context, ownerID string) ([]OfferSummary, error) {
	return s.listOffers(ctx, `g.resource_owner_system_account_id = ?`, ownerID)
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
			WHERE m.team_id = g.grantee_team_id AND m.system_account_id = ? AND m.status = 'active'))`
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

// SourceDetail is the full receiver-visible source view.
type SourceDetail struct {
	SourceSummary
	Connections []authzrevision.Connection `json:"connections"`
	Models      []string                   `json:"supportedModels"`
}

func (s *Store) GetSourceDetail(ctx context.Context, accountID, sourceID string) (*SourceDetail, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(s.inboundSourceQuery(`s.id = ? AND ra.grantee_system_account_id = ?`)+
		` LIMIT 1`), sourceID, accountID)
	if err != nil {
		return nil, err
	}
	items, err := s.scanInboundRows(ctx, rows)
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	detail := &SourceDetail{SourceSummary: items[0]}
	if detail.ResourceType == "account" {
		if secret, secretErr := s.revisionSecretOrFail(); secretErr == nil {
			snapshot, err := authzrevision.Load(ctx, s.db, detail.ResourceID, secret)
			if err == nil {
				detail.Connections = redactedConnections(snapshot.Connections)
				detail.Models = snapshot.SupportedModels
			}
		}
	}
	return detail, nil
}

// redactedConnections projects the snapshot connections without URL path/query
// or userinfo; the receiver only needs the endpoint host and protocol modes.
func redactedConnections(in []authzrevision.Connection) []authzrevision.Connection {
	out := make([]authzrevision.Connection, 0, len(in))
	for _, connection := range in {
		connection.BaseURL = connectionHost(connection.BaseURL)
		out = append(out, connection)
	}
	return out
}

// connectionHost strips scheme, path, query and userinfo from a base URL; the
// receiver-visible surface may disclose the endpoint host only.
func connectionHost(raw string) string {
	host := raw
	if strings.Contains(host, "://") {
		if idx := strings.Index(host, "://"); idx >= 0 {
			host = host[idx+3:]
		}
	}
	if idx := strings.Index(host, "@"); idx >= 0 {
		host = host[idx+1:]
	}
	if idx := strings.IndexAny(host, "/?#"); idx >= 0 {
		host = host[:idx]
	}
	return host
}

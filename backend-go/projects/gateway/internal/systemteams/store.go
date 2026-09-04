// Package systemteams implements the M03 slice: team CRUD, membership
// management, member history and access scoping, ported from Node
// system-team.repository.ts + system-teams.routes.ts. Team disable/enable
// cascades into the authorization domain via the authz Store
// (revokeAllTeamSources / reactivateTeamGrants), matching Node
// updateSystemTeam's revokeAllTeamSources/reactivateTeamGrantSources calls.
package systemteams

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authz"
)

const (
	MaxMemberBatchSize = 20
	MaxMembersPerTeam  = 20
	defaultPageSize    = 20
	maxPageSize        = 100
)

// Conflict mirrors the Node patch conflict outcome (409).
type Conflict struct{}

func (c *Conflict) Error() string { return "团队已被其他操作更新，请刷新后重试" }

// ValidationError maps to Node throw-Error paths rendered as 400 by routes.
type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

// AccessScope mirrors access-scope.ts: admins see everything unless filtered;
// non-admins see only teams where they hold an active membership.
type AccessScope struct {
	ViewerID string
	IsAdmin  bool
	FilterID string
}

// ScopedID returns the membership-narrowing account id (empty = unscoped).
func (a AccessScope) ScopedID() string {
	if a.IsAdmin {
		return a.FilterID
	}
	return a.ViewerID
}

// Store is the dual-mode team persistence.
type Store struct {
	db    *sql.DB
	pg    bool
	now   func() time.Time
	authz *authz.Store
}

func NewStore(db *sql.DB, postgres bool, now func() time.Time, authzStore *authz.Store) (*Store, error) {
	if db == nil {
		return nil, errors.New("systemteams store requires a database")
	}
	if now == nil {
		now = time.Now
	}
	return &Store{db: db, pg: postgres, now: now, authz: authzStore}, nil
}

func (s *Store) table(name string) string {
	if s.pg {
		return "juhe_business." + name
	}
	return name
}

func (s *Store) bind(query string) string {
	if !s.pg {
		return query
	}
	var out strings.Builder
	index := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			out.WriteString("$" + itoa(index))
			index++
		} else {
			out.WriteByte(query[i])
		}
	}
	return out.String()
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	digits := ""
	for v > 0 {
		digits = string(rune('0'+v%10)) + digits
		v /= 10
	}
	return digits
}

func ensureCtx(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// ListItem mirrors SystemTeamListItem.
type ListItem struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
	Status      string  `json:"status"`
	MemberCount int     `json:"memberCount"`
	CreatedAt   string  `json:"createdAt"`
	UpdatedAt   string  `json:"updatedAt"`
	EditVersion string  `json:"editVersion"`
}

// ListPage mirrors listSystemTeamsPageAsync with keyword prefix scan.
func (s *Store) ListPage(ctx context.Context, access AccessScope, page, pageSize int, keyword string) (items []ListItem, hasMore bool, err error) {
	ctx = ensureCtx(ctx)
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = defaultPageSize
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}
	clauses := []string{"1=1"}
	args := []any{}
	scopedID := access.ScopedID()
	if scopedID != "" {
		clauses = append(clauses, `EXISTS (
			SELECT 1 FROM `+s.table("system_team_members")+` m
			WHERE m.team_id = t.id AND m.system_account_id = ? AND m.status = 'active')`)
		args = append(args, scopedID)
	}
	if keyword != "" {
		clauses = append(clauses, "(t.name >= ? AND t.name < ?)")
		args = append(args, keyword, keywordUpperBound(keyword))
	}
	where := strings.Join(clauses, " AND ")
	query := `SELECT t.id, t.name, COALESCE(t.description,''), t.status, t.created_at, t.updated_at,
		(SELECT COUNT(*) FROM ` + s.table("system_team_members") + ` m2
		 WHERE m2.team_id = t.id AND m2.status = 'active') AS member_count
		FROM ` + s.table("system_teams") + ` t
		WHERE ` + where + ` ORDER BY t.updated_at DESC, t.id DESC LIMIT ? OFFSET ?`
	rows, err := s.db.QueryContext(ctx, s.bind(query), append(args, pageSize+1, (page-1)*pageSize)...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	items = []ListItem{}
	for rows.Next() {
		var item ListItem
		var description string
		if err := rows.Scan(&item.ID, &item.Name, &description, &item.Status, &item.CreatedAt, &item.UpdatedAt, &item.MemberCount); err != nil {
			return nil, false, err
		}
		if description != "" {
			item.Description = &description
		}
		item.EditVersion = item.UpdatedAt
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore = len(items) > pageSize
	if hasMore {
		items = items[:pageSize]
	}
	return items, hasMore, nil
}

// Detail mirrors findSystemTeamDetailAsync: team row + active member details.
type Detail struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description *string        `json:"description,omitempty"`
	Status      string         `json:"status"`
	MemberCount int            `json:"memberCount"`
	CreatedAt   string         `json:"createdAt"`
	UpdatedAt   string         `json:"updatedAt"`
	EditVersion string         `json:"editVersion"`
	Members     []MemberDetail `json:"members,omitempty"`
}

type MemberDetail struct {
	ID              string `json:"id"`
	SystemAccountID string `json:"systemAccountId"`
	DisplayName     string `json:"displayName"`
	JoinedAt        string `json:"joinedAt"`
}

// FindDetail loads team + members; nil when absent or outside scope.
func (s *Store) FindDetail(ctx context.Context, id string, access AccessScope) (*Detail, error) {
	ctx = ensureCtx(ctx)
	var detail Detail
	var description string
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT id, name, COALESCE(description,''), status, created_at, updated_at
		FROM `+s.table("system_teams")+` WHERE id = ?`), id).
		Scan(&detail.ID, &detail.Name, &description, &detail.Status, &detail.CreatedAt, &detail.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if description != "" {
		detail.Description = &description
	}
	detail.EditVersion = detail.UpdatedAt
	scopedID := access.ScopedID()
	if scopedID != "" {
		var count int
		if err := s.db.QueryRowContext(ctx, s.bind(`SELECT COUNT(*) FROM `+s.table("system_team_members")+`
			WHERE team_id = ? AND system_account_id = ? AND status = 'active'`), id, scopedID).Scan(&count); err != nil {
			return nil, err
		}
		if count == 0 {
			return nil, nil
		}
	}
	members, err := s.members(ctx, id)
	if err != nil {
		return nil, err
	}
	detail.Members = members
	detail.MemberCount = len(members)
	return &detail, nil
}

func (s *Store) members(ctx context.Context, teamID string) ([]MemberDetail, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT m.id, m.system_account_id, COALESCE(a.display_name,''), m.joined_at
		FROM `+s.table("system_team_members")+` m
		INNER JOIN `+s.table("system_accounts")+` a ON a.id = m.system_account_id
		WHERE m.team_id = ? AND m.status = 'active'
		ORDER BY m.joined_at ASC, m.id ASC`), teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	members := []MemberDetail{}
	for rows.Next() {
		var member MemberDetail
		if err := rows.Scan(&member.ID, &member.SystemAccountID, &member.DisplayName, &member.JoinedAt); err != nil {
			return nil, err
		}
		members = append(members, member)
	}
	return members, rows.Err()
}

// HistoryEntry mirrors SystemTeamMemberHistoryRow.
type HistoryEntry struct {
	ID              string  `json:"id"`
	SystemAccountID string  `json:"systemAccountId"`
	DisplayName     string  `json:"displayName"`
	JoinedAt        string  `json:"joinedAt"`
	Status          string  `json:"status"`
	RemovedAt       *string `json:"removedAt,omitempty"`
}

// ListHistory mirrors listSystemTeamMemberHistoryAsync.
func (s *Store) ListHistory(ctx context.Context, id string, access AccessScope, page, pageSize int) ([]HistoryEntry, bool, error) {
	ctx = ensureCtx(ctx)
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = defaultPageSize
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}
	scopedID := access.ScopedID()
	if scopedID != "" {
		var count int
		if err := s.db.QueryRowContext(ctx, s.bind(`SELECT COUNT(*) FROM `+s.table("system_team_members")+`
			WHERE team_id = ? AND system_account_id = ? AND status = 'active'`), id, scopedID).Scan(&count); err != nil {
			return nil, false, err
		}
		if count == 0 {
			return nil, false, nil
		}
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT m.id, m.system_account_id, COALESCE(a.display_name,''), m.joined_at, m.status, m.removed_at
		FROM `+s.table("system_team_members")+` m
		INNER JOIN `+s.table("system_accounts")+` a ON a.id = m.system_account_id
		WHERE m.team_id = ?
		ORDER BY m.created_at DESC, m.id DESC LIMIT ? OFFSET ?`), id, pageSize+1, (page-1)*pageSize)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	entries := []HistoryEntry{}
	for rows.Next() {
		var entry HistoryEntry
		var removedAt sql.NullString
		if err := rows.Scan(&entry.ID, &entry.SystemAccountID, &entry.DisplayName, &entry.JoinedAt, &entry.Status, &removedAt); err != nil {
			return nil, false, err
		}
		if removedAt.Valid {
			entry.RemovedAt = &removedAt.String
		}
		entries = append(entries, entry)
	}
	hasMore := len(entries) > pageSize
	if hasMore {
		entries = entries[:pageSize]
	}
	return entries, hasMore, rows.Err()
}

func normalizeName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", &ValidationError{Message: "团队名称不能为空"}
	}
	if len([]rune(trimmed)) > 100 {
		return "", &ValidationError{Message: "团队名称不能超过 100 个字符"}
	}
	return trimmed, nil
}

func normalizeDescription(value *string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	trimmed := strings.TrimSpace(*value)
	if len([]rune(trimmed)) > 200 {
		return nil, &ValidationError{Message: "团队说明不能超过 200 个字符"}
	}
	return &trimmed, nil
}

func normalizeStatus(value *string, fallback string) (string, error) {
	if value == nil {
		return fallback, nil
	}
	if *value == "active" || *value == "disabled" {
		return *value, nil
	}
	return "", &ValidationError{Message: "团队状态无效"}
}

// Create mirrors createSystemTeamAsync (duplicate name → 团队名称已存在).
func (s *Store) Create(ctx context.Context, name string, description *string, status *string, actorID string) (*ListItem, error) {
	ctx = ensureCtx(ctx)
	normalizedName, err := normalizeName(name)
	if err != nil {
		return nil, err
	}
	normalizedDescription, err := normalizeDescription(description)
	if err != nil {
		return nil, err
	}
	normalizedStatus, err := normalizeStatus(status, "active")
	if err != nil {
		return nil, err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	id := "team_" + randomHex()
	_, err = s.db.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("system_teams")+`
		(id, name, description, status, created_by, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`),
		id, normalizedName, normalizedDescription, normalizedStatus, actorID, now, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") || strings.Contains(err.Error(), "duplicate key") {
			return nil, &ValidationError{Message: "团队名称已存在"}
		}
		return nil, err
	}
	return &ListItem{
		ID: id, Name: normalizedName, Description: normalizedDescription, Status: normalizedStatus,
		MemberCount: 0, CreatedAt: now, UpdatedAt: now, EditVersion: now,
	}, nil
}

// PatchOutcome mirrors SystemTeamPatchOutcome.
type PatchOutcome struct {
	Status string // not_found|conflict|noop|updated
	Result *ListItem
	Change *MemberChange
}

// MemberChange records add/remove outcomes for operation logs.
type MemberChange struct {
	Field      string
	Before     string
	After      string
	TargetID   string
	TargetName string
}

// randomHex is provided by the build via helper file.

// keywordUpperBound mirrors the prefix-scan upper bound (last codePoint + 1).
func keywordUpperBound(prefix string) string {
	if prefix == "" {
		return prefix
	}
	runes := []rune(prefix)
	runes[len(runes)-1]++
	return string(runes)
}

func nullableString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

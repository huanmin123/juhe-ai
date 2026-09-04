// Package authz implements the M04 authorizations vertical slice: the
// grant/source/runtime three-table state machine ported from Node
// resource-authorization-*.repository.ts. Statuses: active|paused|expired|
// revoked|returned (grants and runtime); sources: active|superseded|revoked.
// Version contract: nextResourceAuthorizationVersion = current >= now ?
// current + 1ms : now; optimistic lock via WHERE id = ? AND updated_at = ?.
package authz

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Limits mirror system-team-limits.ts and authorization-sweep-limits.ts.
const (
	MaxTeamMembersPerTeam   = 20
	MaxTeamActiveGrantCount = 20
	MaxExpirySweepBatchSize = 20
	UsageMaxListWindowRows  = 1001
	UsageDefaultPageSize    = 20
	UsageMaxPageSize        = 200
	MaxMemberBatchSize      = 20
)

// Terminal statuses shared by grants and runtime rows.
const (
	StatusActive   = "active"
	StatusPaused   = "paused"
	StatusExpired  = "expired"
	StatusRevoked  = "revoked"
	StatusReturned = "returned"
)

// Conflict mirrors the Node optimistic-concurrency 409.
type Conflict struct {
	CurrentUpdatedAt string
}

func (c *Conflict) Error() string { return "授权配置已被其他操作更新，请刷新后重试" }

// Fail is a domain rejection rendered as 400 with the verbatim Node message.
type Fail struct{ Message string }

func (f *Fail) Error() string { return f.Message }

func failf(format string, args ...any) *Fail {
	return &Fail{Message: sprintf(format, args...)}
}

func sprintf(format string, args ...any) string {
	return fmt.Sprintf(format, args...)
}

// Store is the dual-mode authorization persistence.
type Store struct {
	db  *sql.DB
	pg  bool
	now func() time.Time
}

func NewStore(db *sql.DB, postgres bool, now func() time.Time) (*Store, error) {
	if db == nil {
		return nil, errors.New("authz store requires a database")
	}
	if now == nil {
		now = time.Now
	}
	return &Store{db: db, pg: postgres, now: now}, nil
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

// NextVersion mirrors nextResourceAuthorizationVersion.
func NextVersion(current string, now time.Time) string {
	parsed, err := time.Parse(time.RFC3339Nano, current)
	if err != nil {
		return now.UTC().Format(time.RFC3339Nano)
	}
	floor := parsed.Add(time.Millisecond)
	if now.Before(floor) {
		return floor.UTC().Format(time.RFC3339Nano)
	}
	return now.UTC().Format(time.RFC3339Nano)
}

// Filters mirrors the list query contract (status 'all' disables the filter).
type Filters struct {
	ResourceType                 string
	ResourceID                   string
	ResourceOwnerSystemAccountID string
	GranteeSystemAccountID       string
	TeamID                       string
	Status                       string // active|paused|expired|revoked|returned|all
	Direction                    string // all|outbound|inbound (my-* only)
	SourceType                   string // all|manual|team
	Keyword                      string
	ViewerSystemAccountID        string // set by my-* scope
	IsAdmin                      bool
}

type grantRow struct {
	ID            string
	ResourceType  string
	ResourceID    string
	OwnerID       string
	GranteeType   string
	GranteeUserID sql.NullString
	GranteeTeamID sql.NullString
	Status        string
	Remark        sql.NullString
	ExpiresAt     sql.NullString
	CreatedAt     string
	UpdatedAt     string
}

const grantColumns = `g.id, g.resource_type, g.resource_id, g.resource_owner_system_account_id,
	g.grantee_type, g.grantee_system_account_id, g.grantee_team_id, g.status,
	g.remark, g.expires_at, g.created_at, g.updated_at`

func (s *Store) scanGrant(scanner interface{ Scan(...any) error }) (grantRow, error) {
	var row grantRow
	err := scanner.Scan(&row.ID, &row.ResourceType, &row.ResourceID, &row.OwnerID,
		&row.GranteeType, &row.GranteeUserID, &row.GranteeTeamID, &row.Status,
		&row.Remark, &row.ExpiresAt, &row.CreatedAt, &row.UpdatedAt)
	return row, err
}

// buildFilters converts the contract filters into WHERE clauses. The scope
// rules: non-admin sees rows where owner = viewer OR grantee user = viewer OR
// grantee team has viewer as an active member; direction refines that.
func (s *Store) buildFilters(filters Filters) (string, []any) {
	clauses := []string{"1=1"}
	args := []any{}
	if filters.ResourceType != "" {
		clauses = append(clauses, "g.resource_type = ?")
		args = append(args, filters.ResourceType)
	}
	if filters.ResourceID != "" {
		clauses = append(clauses, "g.resource_id = ?")
		args = append(args, filters.ResourceID)
	}
	if filters.ResourceOwnerSystemAccountID != "" {
		clauses = append(clauses, "g.resource_owner_system_account_id = ?")
		args = append(args, filters.ResourceOwnerSystemAccountID)
	}
	if filters.GranteeSystemAccountID != "" {
		clauses = append(clauses, "g.grantee_system_account_id = ?", "g.grantee_type = 'system_account'")
		args = append(args, filters.GranteeSystemAccountID)
	}
	if filters.TeamID != "" {
		clauses = append(clauses, "g.grantee_team_id = ?")
		args = append(args, filters.TeamID)
	}
	if filters.Status != "" && filters.Status != "all" {
		clauses = append(clauses, "g.status = ?")
		args = append(args, filters.Status)
	}
	switch filters.SourceType {
	case "manual":
		clauses = append(clauses, "g.grantee_type = 'system_account'")
	case "team":
		clauses = append(clauses, "g.grantee_type = 'team'")
	}
	// Direction + self scope (my-*).
	if filters.ViewerSystemAccountID != "" {
		viewer := filters.ViewerSystemAccountID
		switch filters.Direction {
		case "outbound":
			clauses = append(clauses, "g.resource_owner_system_account_id = ?")
			args = append(args, viewer)
		case "inbound":
			clauses = append(clauses, `(g.grantee_system_account_id = ? OR (
				g.grantee_type = 'team' AND EXISTS (
					SELECT 1 FROM `+s.table("system_team_members")+` m
					WHERE m.team_id = g.grantee_team_id AND m.system_account_id = ? AND m.status = 'active')))`)
			args = append(args, viewer, viewer)
		default:
			clauses = append(clauses, `(g.resource_owner_system_account_id = ? OR g.grantee_system_account_id = ? OR (
				g.grantee_type = 'team' AND EXISTS (
					SELECT 1 FROM `+s.table("system_team_members")+` m
					WHERE m.team_id = g.grantee_team_id AND m.system_account_id = ? AND m.status = 'active')))`)
			args = append(args, viewer, viewer, viewer)
		}
	}
	// Keyword: prefix match over id/resource_id/remark (codePoint upper bound).
	if filters.Keyword != "" {
		kw := filters.Keyword
		upper := keywordUpperBound(kw)
		clauses = append(clauses, `(
			g.id >= ? AND g.id < ?
			OR g.resource_id >= ? AND g.resource_id < ?
			OR (g.remark IS NOT NULL AND g.remark >= ? AND g.remark < ?)
		)`)
		args = append(args, kw, upper, kw, upper, kw, upper)
	}
	return strings.Join(clauses, " AND "), args
}

// keywordUpperBound mirrors systemTeamTextPrefixUpperBound: the smallest
// string greater than every string with the given prefix (codePoint + 1 on
// the last character).
func keywordUpperBound(prefix string) string {
	if prefix == "" {
		return prefix
	}
	runes := []rune(prefix)
	last := runes[len(runes)-1]
	runes[len(runes)-1] = last + 1
	return string(runes)
}

// Summary is the list/detail item shape (usage included by J5 later).
type Summary struct {
	ID            string  `json:"id"`
	ResourceType  string  `json:"resourceType"`
	ResourceID    string  `json:"resourceId"`
	OwnerID       string  `json:"resourceOwnerSystemAccountId"`
	GranteeType   string  `json:"granteeType"`
	GranteeUserID *string `json:"granteeSystemAccountId,omitempty"`
	GranteeTeamID *string `json:"granteeTeamId,omitempty"`
	Status        string  `json:"status"`
	Remark        *string `json:"remark,omitempty"`
	ExpiresAt     *string `json:"expiresAt,omitempty"`
	CreatedAt     string  `json:"createdAt"`
	UpdatedAt     string  `json:"updatedAt"`
}

func (g grantRow) summary() Summary {
	summary := Summary{
		ID: g.ID, ResourceType: g.ResourceType, ResourceID: g.ResourceID,
		OwnerID: g.OwnerID, GranteeType: g.GranteeType, Status: g.Status,
		CreatedAt: g.CreatedAt, UpdatedAt: g.UpdatedAt,
	}
	if g.GranteeUserID.Valid {
		v := g.GranteeUserID.String
		summary.GranteeUserID = &v
	}
	if g.GranteeTeamID.Valid {
		v := g.GranteeTeamID.String
		summary.GranteeTeamID = &v
	}
	if g.Remark.Valid {
		v := g.Remark.String
		summary.Remark = &v
	}
	if g.ExpiresAt.Valid {
		v := g.ExpiresAt.String
		summary.ExpiresAt = &v
	}
	return summary
}

// ListPage mirrors listResourceAuthorizationSummariesPageAsync.
func (s *Store) ListPage(ctx context.Context, filters Filters, page, pageSize int) (items []Summary, total int, hasMore bool, err error) {
	ctx = ensureCtx(ctx)
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 50
	}
	if pageSize > 500 {
		pageSize = 500
	}
	where, args := s.buildFilters(filters)
	query := `SELECT ` + grantColumns + ` FROM ` + s.table("resource_authorization_grants") + ` g
		WHERE ` + where + ` ORDER BY g.created_at DESC, g.id DESC LIMIT ? OFFSET ?`
	rows, err := s.db.QueryContext(ctx, s.bind(query), append(args, pageSize+1, (page-1)*pageSize)...)
	if err != nil {
		return nil, 0, false, err
	}
	defer rows.Close()
	items = []Summary{}
	for rows.Next() {
		row, scanErr := s.scanGrant(rows)
		if scanErr != nil {
			return nil, 0, false, scanErr
		}
		items = append(items, row.summary())
	}
	if err := rows.Err(); err != nil {
		return nil, 0, false, err
	}
	hasMore = len(items) > pageSize
	if hasMore {
		items = items[:pageSize]
	}
	total = (page-1)*pageSize + len(items)
	if hasMore {
		total++
	}
	return items, total, hasMore, nil
}

// Find mirrors findResourceAuthorizationAsync (status forced to all).
func (s *Store) Find(ctx context.Context, id string) (*Summary, error) {
	ctx = ensureCtx(ctx)
	row := s.db.QueryRowContext(ctx, s.bind(`SELECT `+grantColumns+` FROM `+s.table("resource_authorization_grants")+` g WHERE g.id = ?`), id)
	grant, err := s.scanGrant(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	summary := grant.summary()
	return &summary, nil
}

// GetGrantForMutation loads a grant with the version for mutation checks.
func (s *Store) GetGrantForMutation(ctx context.Context, tx *sql.Tx, id string) (*grantRow, error) {
	ctx = ensureCtx(ctx)
	query := `SELECT ` + grantColumns + ` FROM ` + s.table("resource_authorization_grants") + ` g WHERE g.id = ?`
	if s.pg {
		query += " FOR UPDATE"
	}
	if tx != nil {
		grant, err := s.scanGrant(tx.QueryRowContext(ctx, s.bind(query), id))
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return &grant, err
	}
	grant, err := s.scanGrant(s.db.QueryRowContext(ctx, s.bind(query), id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &grant, err
}

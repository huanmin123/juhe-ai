// Package systemteams implements the M03 slice: team CRUD, membership
// management, member history and access scoping, ported from Node
// system-team.repository.ts + system-teams.routes.ts. Team disable/enable
// cascades into the authorization domain via the authz Store
// (RevokeAllTeamSourcesTx / ReactivateTeamGrantsTx) inside the caller's
// transaction, matching Node updateSystemTeamAsync (:527/:531).
package systemteams

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authz"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/inval"
)

const (
	MaxMemberBatchSize = 20
	MaxMembersPerTeam  = 20
	// maxSystemTeamListPageSize (system-team-limits.ts:1) caps page size on
	// every M03 list surface (team list :1012-1014, member history :1023-1028,
	// member list :1030-1035); the default is the same 20.
	defaultPageSize = 20
	maxPageSize     = 20
	// canonicalInstantLayout mirrors Node toISOString(): UTC milliseconds with
	// a literal Z (shared/rfc3339.ts canonicalizeRfc3339Instant).
	canonicalInstantLayout = "2006-01-02T15:04:05.000Z"
)

// instantPattern mirrors rfc3339InstantPattern (shared/rfc3339.ts:1): an
// RFC3339 date-time with mandatory Z or numeric offset; timezone-less values
// are rejected.
var instantPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,9})?(Z|[+-]\d{2}:\d{2})$`)

// CanonicalizeInstant mirrors canonicalizeRfc3339Instant
// (shared/rfc3339.ts:26-28): parse the instant (offset required), render the
// canonical UTC milliseconds form. ok=false mirrors the undefined result that
// rfc3339InstantSchema turns into a 400 (system-teams.routes.ts:42).
func CanonicalizeInstant(value string) (string, bool) {
	text := strings.TrimSpace(value)
	if !instantPattern.MatchString(text) {
		return "", false
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return "", false
	}
	return parsed.UTC().Format(canonicalInstantLayout), true
}

// ValidationError maps to Node throw-Error paths rendered as 400 by routes.
type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

// StatsDirtyMarker is the group-stats dirty port the Node repository triggers
// after a committed mutation (markAllGroupAccountStatsDirtyAsync,
// system-team.repository.ts :1483-1489). *group_dirty_cursor.Store satisfies
// it; nil keeps the store self-contained.
type StatsDirtyMarker interface {
	MarkAllGroupAccountStatsDirty(ctx context.Context, reason string) error
}

// RuntimeInvalidator is the cache invalidation port for the committed-write
// side effects (notifyGatewayRuntimeCacheInvalidation +
// notifyAuthorizationQuotaCacheInvalidation, system-team.repository.ts
// :1491-1494). *inval.Bus satisfies it; nil keeps invalidation off.
type RuntimeInvalidator interface {
	Invalidate(topic, reason string)
}

// Option customizes the store.
type Option func(*Store)

// WithSideEffects wires the committed-write side-effect ports (C9).
func WithSideEffects(stats StatsDirtyMarker, invalidator RuntimeInvalidator) Option {
	return func(s *Store) {
		s.stats = stats
		s.inval = invalidator
	}
}

// AccessScope mirrors access-scope.ts: admins see everything unless filtered;
// non-admins see only teams where they hold an active membership.
type AccessScope struct {
	ViewerID string
	IsAdmin  bool
	FilterID string
}

// ScopedID returns the membership-narrowing account id (empty = unscoped).
// This is scopedSystemAccountId (access-scope.ts:29-40): admins narrow by the
// ?systemAccountId filter, everyone else by their own account.
func (a AccessScope) ScopedID() string {
	if a.IsAdmin {
		return a.FilterID
	}
	return a.ViewerID
}

// ActorID returns the mutation actor (currentSystemAccountId,
// access-scope.ts:15-22): the signed-in account; missing context mirrors the
// Node 缺少系统账户上下文 throw.
func (a AccessScope) ActorID() (string, error) {
	if strings.TrimSpace(a.ViewerID) == "" {
		return "", &ValidationError{Message: "缺少系统账户上下文"}
	}
	return a.ViewerID, nil
}

// Store is the dual-mode team persistence.
type Store struct {
	db    *sql.DB
	pg    bool
	now   func() time.Time
	authz *authz.Store
	stats StatsDirtyMarker
	inval RuntimeInvalidator
}

func NewStore(db *sql.DB, postgres bool, now func() time.Time, authzStore *authz.Store, options ...Option) (*Store, error) {
	if db == nil {
		return nil, errors.New("systemteams store requires a database")
	}
	if now == nil {
		now = time.Now
	}
	s := &Store{db: db, pg: postgres, now: now, authz: authzStore}
	for _, option := range options {
		option(s)
	}
	return s, nil
}

// afterCommit mirrors refreshGroupAccountStatsAfterWriteAsync +
// invalidateAuthorizationRuntimeAfterBusinessWrite
// (system-team.repository.ts:1478-1494): one group-stats dirty marker write
// plus gateway runtime / authorization quota cache invalidation, both keyed
// by the Node reason strings.
func (s *Store) afterCommit(ctx context.Context, reason string) error {
	if s.stats != nil {
		if err := s.stats.MarkAllGroupAccountStatsDirty(ctx, reason); err != nil {
			return err
		}
	}
	if s.inval != nil {
		s.inval.Invalidate(inval.TopicGatewayRuntime, reason)
		s.inval.Invalidate(inval.TopicAuthorizationQuota, reason)
	}
	return nil
}

// canonicalNow renders the injected clock the way Node nowIso() does:
// UTC milliseconds (Date.now is ms-precision, toISOString keeps 3 digits).
func (s *Store) canonicalNow() string {
	return s.now().UTC().Truncate(time.Millisecond).Format(canonicalInstantLayout)
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
// Ordering mirrors querySystemTeamRowsAsync :935:
// ORDER BY status ASC, updated_at DESC, name ASC, id ASC (C1).
func (s *Store) ListPage(ctx context.Context, access AccessScope, page, pageSize int, keyword string) (items []ListItem, hasMore bool, err error) {
	ctx = ensureCtx(ctx)
	page, pageSize = normalizeListWindow(page, pageSize)
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
		WHERE ` + where + ` ORDER BY t.status ASC, t.updated_at DESC, t.name ASC, t.id ASC LIMIT ? OFFSET ?`
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

// normalizeListWindow mirrors normalizeSystemTeamListOptions and the shared
// member/history option normalizers: page >= 1, pageSize clamped to
// [1, maxSystemTeamListPageSize], defaults 20.
func normalizeListWindow(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = defaultPageSize
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}
	return page, pageSize
}

// Detail mirrors SystemTeamDetail (systemTeamDetailFromRow :1189-1199): the
// team row + memberCount; members are exposed by the paginated member list.
type Detail struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
	Status      string  `json:"status"`
	MemberCount int     `json:"memberCount"`
	CreatedAt   string  `json:"createdAt"`
	UpdatedAt   string  `json:"updatedAt"`
	EditVersion string  `json:"editVersion"`
}

// MemberDetail mirrors SystemTeamMemberDetail
// (systemTeamMemberDetailFromRow :1201-1208): systemAccountName is the JSON
// field name (Node display_name projection), not displayName.
type MemberDetail struct {
	ID                string  `json:"id"`
	SystemAccountID   string  `json:"systemAccountId"`
	SystemAccountName *string `json:"systemAccountName"`
	JoinedAt          string  `json:"joinedAt"`
}

// MembersPage mirrors SystemTeamMembersResult (systemTeamMembersResult
// :1227-1244): id/memberCount/updatedAt ride along with the page envelope and
// total equals memberCount.
type MembersPage struct {
	ID          string         `json:"id"`
	Items       []MemberDetail `json:"items"`
	MemberCount int            `json:"memberCount"`
	UpdatedAt   string         `json:"updatedAt"`
	Total       int            `json:"total"`
	HasMore     bool           `json:"hasMore"`
	Page        int            `json:"page"`
	PageSize    int            `json:"pageSize"`
}

// FindDetail loads the team row; nil when absent or outside scope
// (findSystemTeamDetailRowForAccessAsync :1170-1187).
func (s *Store) FindDetail(ctx context.Context, id string, access AccessScope) (*Detail, error) {
	ctx = ensureCtx(ctx)
	var detail Detail
	var description string
	query := `SELECT id, name, COALESCE(description,''), status, created_at, updated_at
		FROM ` + s.table("system_teams") + ` WHERE id = ?`
	if err := s.db.QueryRowContext(ctx, s.bind(query), id).
		Scan(&detail.ID, &detail.Name, &description, &detail.Status, &detail.CreatedAt, &detail.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
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
	// memberCount mirrors listSystemTeamMemberCountsForTeamIdsAsync (:1072+):
	// COUNT(*) over active member rows.
	memberCount, err := s.memberCount(ctx, id)
	if err != nil {
		return nil, err
	}
	detail.MemberCount = memberCount
	return &detail, nil
}

func (s *Store) memberCount(ctx context.Context, teamID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT COUNT(*) FROM `+s.table("system_team_members")+`
		WHERE team_id = ? AND status = 'active'`), teamID).Scan(&count)
	return count, err
}

// ListMembers mirrors listSystemTeamMembersAsync (:292-332): scope-checked
// team row, memberCount from the team, then a dedicated paginated active-only
// member query (joined_at ASC, id ASC, pageSize+1 lookahead) — not the full
// member scan the previous port reused from FindDetail (C2).
func (s *Store) ListMembers(ctx context.Context, id string, access AccessScope, page, pageSize int) (*MembersPage, error) {
	ctx = ensureCtx(ctx)
	page, pageSize = normalizeListWindow(page, pageSize)
	var teamID, teamUpdatedAt string
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT id, updated_at FROM `+s.table("system_teams")+` WHERE id = ?`), id).
		Scan(&teamID, &teamUpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
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
	memberCount, err := s.memberCount(ctx, id)
	if err != nil {
		return nil, err
	}
	items := []MemberDetail{}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT m.id, m.system_account_id, a.display_name, m.joined_at
		FROM `+s.table("system_team_members")+` m
		INNER JOIN `+s.table("system_accounts")+` a ON a.id = m.system_account_id
		WHERE m.team_id = ? AND m.status = 'active'
		ORDER BY m.joined_at ASC, m.id ASC LIMIT ? OFFSET ?`), id, pageSize+1, (page-1)*pageSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var member MemberDetail
		if err := rows.Scan(&member.ID, &member.SystemAccountID, &member.SystemAccountName, &member.JoinedAt); err != nil {
			return nil, err
		}
		items = append(items, member)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	hasMore := len(items) > pageSize
	if hasMore {
		items = items[:pageSize]
	}
	// Node :1234-1243: total = memberCount (upper-bound reads ride hasMore).
	return &MembersPage{
		ID:          teamID,
		Items:       items,
		MemberCount: memberCount,
		UpdatedAt:   teamUpdatedAt,
		Total:       memberCount,
		HasMore:     hasMore,
		Page:        page,
		PageSize:    pageSize,
	}, nil
}

// HistoryEntry mirrors SystemTeamMemberHistoryItem
// (systemTeamMemberHistoryResult :1210-1225): a member detail plus the
// hardcoded status 'removed' and removedAt.
type HistoryEntry struct {
	ID                string  `json:"id"`
	SystemAccountID   string  `json:"systemAccountId"`
	SystemAccountName *string `json:"systemAccountName"`
	JoinedAt          string  `json:"joinedAt"`
	Status            string  `json:"status"`
	RemovedAt         *string `json:"removedAt,omitempty"`
}

// HistoryPage mirrors SystemTeamMemberHistoryResult (:1217-1224).
type HistoryPage struct {
	ID       string         `json:"id"`
	Items    []HistoryEntry `json:"items"`
	Total    int            `json:"total"`
	HasMore  bool           `json:"hasMore"`
	Page     int            `json:"page"`
	PageSize int            `json:"pageSize"`
}

// ListHistory mirrors listSystemTeamMemberHistoryAsync (:334-374): only
// status='removed' rows, ordered joined_at DESC, id DESC (C4).
func (s *Store) ListHistory(ctx context.Context, id string, access AccessScope, page, pageSize int) (*HistoryPage, error) {
	ctx = ensureCtx(ctx)
	page, pageSize = normalizeListWindow(page, pageSize)
	var teamID string
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT id FROM `+s.table("system_teams")+` WHERE id = ?`), id).Scan(&teamID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
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
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT m.id, m.system_account_id, a.display_name, m.joined_at, m.status, m.removed_at
		FROM `+s.table("system_team_members")+` m
		INNER JOIN `+s.table("system_accounts")+` a ON a.id = m.system_account_id
		WHERE m.team_id = ? AND m.status = 'removed'
		ORDER BY m.joined_at DESC, m.id DESC LIMIT ? OFFSET ?`), id, pageSize+1, (page-1)*pageSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := []HistoryEntry{}
	for rows.Next() {
		var entry HistoryEntry
		var removedAt sql.NullString
		if err := rows.Scan(&entry.ID, &entry.SystemAccountID, &entry.SystemAccountName, &entry.JoinedAt, &entry.Status, &removedAt); err != nil {
			return nil, err
		}
		if removedAt.Valid {
			entry.RemovedAt = &removedAt.String
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	hasMore := len(entries) > pageSize
	if hasMore {
		entries = entries[:pageSize]
	}
	// pagedTotalUpperBound (:23-28).
	total := (page-1)*pageSize + len(entries)
	if hasMore {
		total++
	}
	return &HistoryPage{ID: id, Items: entries, Total: total, HasMore: hasMore, Page: page, PageSize: pageSize}, nil
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
		if fallback == "active" || fallback == "disabled" {
			return fallback, nil
		}
		return "", &ValidationError{Message: "团队状态无效"}
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
	now := s.canonicalNow()
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

// keywordUpperBound mirrors systemTeamTextPrefixUpperBound (:1496-1504).
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

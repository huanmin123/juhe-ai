// Package announcements owns the M01 vertical slice: dual-mode announcement
// store (SQLite + PostgreSQL) and the full route family contract ported from
// backend/src/modules/announcements/announcements.routes.ts, including
// revision conflicts (409 + currentRevision), publish/unpublish via status
// patch, read tracking, and operation-log emission.
package announcements

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// ConflictError maps to AnnouncementRevisionConflictError (409 + currentRevision).
type ConflictError struct {
	Message         string
	CurrentRevision string
}

func (e *ConflictError) Error() string { return e.Message }

// ValidationError maps to Node throw-Error paths rendered as 409 by the
// route handlers (公告文本不能为空 / 公告级别无效 / ...).
type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

var levels = map[string]bool{"critical": true, "warning": true, "info": true, "normal": true}
var statuses = map[string]bool{"draft": true, "published": true, "archived": true}

const publicLimit = 30

// Store is the dual-mode announcement persistence.
type Store struct {
	db   *sql.DB
	pg   bool
	now  func() time.Time
	newI func(prefix string) string
}

func NewStore(db *sql.DB, postgres bool, now func() time.Time, newID func(string) string) (*Store, error) {
	if db == nil {
		return nil, errors.New("announcements store requires a database")
	}
	if now == nil {
		now = time.Now
	}
	if newID == nil {
		newID = func(prefix string) string { return prefix + "_" + now().UTC().Format("20060102150405") }
	}
	return &Store{db: db, pg: postgres, now: now, newI: newID}, nil
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

// PublicListItem mirrors PublicAnnouncementListItem.
type PublicListItem struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	Level       string  `json:"level"`
	PublishedAt string  `json:"publishedAt"`
	ReadAt      *string `json:"readAt,omitempty"`
}

// PublicDetail mirrors PublicAnnouncementDetail.
type PublicDetail struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Content     string `json:"content"`
	Level       string `json:"level"`
	PublishedAt string `json:"publishedAt"`
}

// ListPublic mirrors listPublicAnnouncementsAsync (published only, join read
// state, LIMIT normalizePublicLimit).
func (s *Store) ListPublic(ctx context.Context, systemAccountID string, limit int) ([]PublicListItem, error) {
	ctx = ensureCtx(ctx)
	if limit <= 0 {
		limit = publicLimit
	}
	if limit > publicLimit {
		limit = publicLimit
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT a.id, a.title, a.level, a.published_at, r.read_at
		FROM `+s.table("announcements")+` a
		LEFT JOIN `+s.table("announcement_reads")+` r
			ON r.announcement_id = a.id AND r.system_account_id = ?
		WHERE a.status = 'published' AND a.published_at IS NOT NULL
		ORDER BY a.published_at DESC, a.created_at DESC, a.id DESC
		LIMIT ?`), systemAccountID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []PublicListItem{}
	for rows.Next() {
		var item PublicListItem
		var readAt sql.NullString
		if err := rows.Scan(&item.ID, &item.Title, &item.Level, &item.PublishedAt, &readAt); err != nil {
			return nil, err
		}
		if readAt.Valid {
			item.ReadAt = &readAt.String
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// FindPublic mirrors findPublicAnnouncementAsync (nil when not published).
func (s *Store) FindPublic(ctx context.Context, id string) (*PublicDetail, error) {
	ctx = ensureCtx(ctx)
	var detail PublicDetail
	var publishedAt string
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT id, title, content, level, published_at
		FROM `+s.table("announcements")+` WHERE id = ? AND status = 'published' AND published_at IS NOT NULL`), id).
		Scan(&detail.ID, &detail.Title, &detail.Content, &detail.Level, &publishedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	detail.PublishedAt = publishedAt
	return &detail, nil
}

// ReadResult mirrors AnnouncementReadResult.
type ReadResult struct {
	ReadAt string `json:"readAt"`
	Count  int64  `json:"count"`
}

// MarkRead mirrors markPublicAnnouncementsReadAsync: dedupe ids (max 30),
// insert-select published only, ON CONFLICT DO NOTHING.
func (s *Store) MarkRead(ctx context.Context, systemAccountID string, announcementIDs []string) (ReadResult, error) {
	ctx = ensureCtx(ctx)
	seen := map[string]bool{}
	ids := make([]string, 0, len(announcementIDs))
	for _, id := range announcementIDs {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		ids = append(ids, trimmed)
		if len(ids) >= publicLimit {
			break
		}
	}
	readAt := s.now().UTC().Format(time.RFC3339Nano)
	if len(ids) == 0 {
		return ReadResult{ReadAt: readAt}, nil
	}
	placeholders := make([]string, len(ids))
	args := []any{systemAccountID, readAt}
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}
	result, err := s.db.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("announcement_reads")+` (announcement_id, system_account_id, read_at)
		SELECT a.id, ?, ? FROM `+s.table("announcements")+` a
		WHERE a.id IN (`+strings.Join(placeholders, ",")+`) AND a.status = 'published' AND a.published_at IS NOT NULL
		ON CONFLICT (announcement_id, system_account_id) DO NOTHING`), args...)
	if err != nil {
		return ReadResult{}, err
	}
	count, _ := result.RowsAffected()
	return ReadResult{ReadAt: readAt, Count: count}, nil
}

// AdminListItem mirrors AnnouncementListItem: the management list projection
// with SQL-computed contentPreview/contentTruncated, the updated actor
// display name, and revision = updated_at (announcements.repository.ts
// announcementListItem).
type AdminListItem struct {
	ID               string  `json:"id"`
	Title            string  `json:"title"`
	ContentPreview   string  `json:"contentPreview"`
	ContentTruncated bool    `json:"contentTruncated"`
	Level            string  `json:"level"`
	Status           string  `json:"status"`
	UpdatedByName    *string `json:"updatedByName,omitempty"`
	PublishedAt      *string `json:"publishedAt,omitempty"`
	Revision         string  `json:"revision"`
}

// AdminListResult mirrors AnnouncementListResult (normalized pagination
// metadata, same key set as the Node repository result).
type AdminListResult struct {
	Items    []AdminListItem `json:"items"`
	Total    int             `json:"total"`
	HasMore  bool            `json:"hasMore"`
	Page     int             `json:"page"`
	PageSize int             `json:"pageSize"`
}

const (
	// defaultAnnouncementPageSize mirrors defaultAnnouncementPageSize (50,
	// not 20).
	defaultAnnouncementPageSize = 50
	// maxAnnouncementPageSize mirrors maxAnnouncementPageSize.
	maxAnnouncementPageSize = 100
	// listWindowRows mirrors query-utils defaultListWindowRows: the list is
	// served from a 1001-row window, capping the normalized page at
	// floor((windowRows-1)/pageSize).
	listWindowRows = 1001
	// announcementPreviewLimit mirrors the 240-char preview cut in
	// announcementListSelectColumns.
	announcementPreviewLimit = 240
)

// pageUpperBoundForWindow mirrors query-utils pageUpperBoundForWindow.
func pageUpperBoundForWindow(pageSize int) int {
	if pageSize < 1 {
		pageSize = 1
	}
	bound := (listWindowRows - 1) / pageSize
	if bound < 1 {
		bound = 1
	}
	return bound
}

// normalizeListPage mirrors query-utils normalizeListPage: a validated page
// clamps into the window bound, anything else falls back to page 1.
func normalizeListPage(page *int, pageSize int) int {
	if page == nil || *page < 1 {
		return 1
	}
	bound := pageUpperBoundForWindow(pageSize)
	if *page > bound {
		return bound
	}
	return *page
}

// normalizeListOptions mirrors normalizeAnnouncementListOptions: pageSize
// defaults to 50 and clamps to 1..100 before the page window is derived.
func normalizeListOptions(page, pageSize *int) (int, int) {
	size := defaultAnnouncementPageSize
	if pageSize != nil {
		size = *pageSize
		if size < 1 {
			size = 1
		}
		if size > maxAnnouncementPageSize {
			size = maxAnnouncementPageSize
		}
	}
	return normalizeListPage(page, size), size
}

// pagedTotalUpperBound mirrors query-utils pagedTotalUpperBound.
func pagedTotalUpperBound(page, pageSize, itemCount int, hasMore bool) int {
	if page < 1 {
		page = 1
	}
	if pageSize < 0 {
		pageSize = 0
	}
	if itemCount < 0 {
		itemCount = 0
	}
	total := (page-1)*pageSize + itemCount
	if hasMore {
		total++
	}
	return total
}

// ListPage mirrors listAnnouncementsPageAsync: pageSize+1 probe, windowed
// page normalization, preview truncation in SQL, updated-actor join, and
// total/hasMore computed from the normalized values.
func (s *Store) ListPage(ctx context.Context, page, pageSize *int) (AdminListResult, error) {
	ctx = ensureCtx(ctx)
	normalizedPage, normalizedPageSize := normalizeListOptions(page, pageSize)
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT a.id, a.title,
			CASE WHEN length(a.content) > ? THEN substr(a.content, 1, ?) || '...' ELSE a.content END AS content_preview,
			CASE WHEN length(a.content) > ? THEN 1 ELSE 0 END AS content_truncated,
			a.level, a.status, updated_actor.display_name AS updated_by_name,
			a.published_at, a.updated_at
		FROM `+s.table("announcements")+` a
		LEFT JOIN `+s.table("system_accounts")+` updated_actor ON updated_actor.id = a.updated_by
		ORDER BY a.updated_at DESC, a.created_at DESC, a.id DESC
		LIMIT ? OFFSET ?`),
		announcementPreviewLimit, announcementPreviewLimit, announcementPreviewLimit,
		normalizedPageSize+1, (normalizedPage-1)*normalizedPageSize)
	if err != nil {
		return AdminListResult{}, err
	}
	defer rows.Close()
	items := []AdminListItem{}
	for rows.Next() {
		var item AdminListItem
		var truncated int
		var updatedByName, publishedAt sql.NullString
		if err := rows.Scan(&item.ID, &item.Title, &item.ContentPreview, &truncated,
			&item.Level, &item.Status, &updatedByName, &publishedAt, &item.Revision); err != nil {
			return AdminListResult{}, err
		}
		item.ContentTruncated = truncated == 1
		item.UpdatedByName = nullStringPtr(updatedByName)
		item.PublishedAt = nullStringPtr(publishedAt)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return AdminListResult{}, err
	}
	hasMore := len(items) > normalizedPageSize
	if hasMore {
		items = items[:normalizedPageSize]
	}
	return AdminListResult{
		Items:    items,
		Total:    pagedTotalUpperBound(normalizedPage, normalizedPageSize, len(items), hasMore),
		HasMore:  hasMore,
		Page:     normalizedPage,
		PageSize: normalizedPageSize,
	}, nil
}

// nullStringPtr renders a nullable column as the omitted-when-null JSON
// contract (Node `row.x ?? undefined`).
func nullStringPtr(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	text := value.String
	return &text
}

// EditDetail mirrors AnnouncementEditDetail.
type EditDetail struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Content  string `json:"content"`
	Level    string `json:"level"`
	Status   string `json:"status"`
	Revision string `json:"revision"`
}

// FindEditDetail mirrors findAnnouncementEditDetailAsync.
func (s *Store) FindEditDetail(ctx context.Context, id string) (*EditDetail, error) {
	ctx = ensureCtx(ctx)
	var detail EditDetail
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT id, title, content, level, status, updated_at
		FROM `+s.table("announcements")+` WHERE id = ?`), id).
		Scan(&detail.ID, &detail.Title, &detail.Content, &detail.Level, &detail.Status, &detail.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &detail, nil
}

// MutationInput is the normalized create/patch payload.
type MutationInput struct {
	Title   *string
	Content *string
	Level   *string
	Status  *string
}

func normalizeText(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", &ValidationError{Message: "公告文本不能为空"}
	}
	return trimmed, nil
}

func normalizeLevel(value *string, fallback string) (string, error) {
	if value == nil {
		return fallback, nil
	}
	if levels[*value] {
		return *value, nil
	}
	return "", &ValidationError{Message: "公告级别无效"}
}

func normalizeStatus(value *string, fallback string) (string, error) {
	if value == nil {
		return fallback, nil
	}
	if statuses[*value] {
		return *value, nil
	}
	return "", &ValidationError{Message: "公告状态无效"}
}

func nextRevision(current string, now time.Time) string {
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

// MutationReceipt mirrors AnnouncementMutationReceipt.
type MutationReceipt struct {
	ID       string `json:"id"`
	Revision string `json:"revision"`
}

// Create mirrors createAnnouncementForManagementAsync.
func (s *Store) Create(ctx context.Context, input MutationInput, actorSystemAccountID string) (MutationReceipt, error) {
	ctx = ensureCtx(ctx)
	if input.Title == nil {
		return MutationReceipt{}, &ValidationError{Message: "公告文本不能为空"}
	}
	title, err := normalizeText(*input.Title)
	if err != nil {
		return MutationReceipt{}, err
	}
	if input.Content == nil {
		return MutationReceipt{}, &ValidationError{Message: "公告文本不能为空"}
	}
	content, err := normalizeText(*input.Content)
	if err != nil {
		return MutationReceipt{}, err
	}
	level, err := normalizeLevel(input.Level, "info")
	if err != nil {
		return MutationReceipt{}, err
	}
	status, err := normalizeStatus(input.Status, "draft")
	if err != nil {
		return MutationReceipt{}, err
	}
	revision := s.now().UTC().Format(time.RFC3339Nano)
	id := s.newI("ann")
	// announcement-management-write.repository.ts writes published_at =
	// revision only for published creates and never puts booleans into the
	// time columns; created_at and updated_at both carry the revision.
	var publishedAt any
	if status == "published" {
		publishedAt = revision
	}
	_, err = s.db.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("announcements")+`
		(id, title, content, level, status, created_by, updated_by, published_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		id, title, content, level, status, actorSystemAccountID, actorSystemAccountID,
		publishedAt, revision, revision)
	if err != nil {
		return MutationReceipt{}, err
	}
	return MutationReceipt{ID: id, Revision: revision}, nil
}

// MutationState mirrors AnnouncementMutationState (the logged before/after
// projection). Content is present only when the patch carried it (Node
// includeContent), and PublishedAt/Revision back the publish diff fields.
type MutationState struct {
	ID          string
	Title       string
	Content     *string
	Level       string
	Status      string
	PublishedAt *string
	Revision    string
}

// MutationOutcome mirrors AnnouncementManagementMutationOutcome: the receipt
// plus the before/after logged state and the changed flag so the route can
// mirror Node's log-only-when-changed semantics.
type MutationOutcome struct {
	Receipt MutationReceipt
	Before  MutationState
	After   MutationState
	Changed bool
}

// Patch mirrors patchAnnouncementForManagementAsync: FOR UPDATE (pg),
// expectedRevision compare, changed-columns only (content included via the
// includeContent read), published transition clears read state, nextRevision
// monotonic. An input that changes nothing is a no-op that returns the
// current revision without bumping it.
func (s *Store) Patch(ctx context.Context, id string, input MutationInput, expectedRevision, actorSystemAccountID string) (*MutationOutcome, error) {
	ctx = ensureCtx(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Node reads content only when the patch carries it
	// (findAnnouncementMutationRow includeContent).
	columns := "id, title, level, status, published_at, updated_at"
	var current MutationState
	var contentNull, publishedAtNull sql.NullString
	dest := []any{&current.ID, &current.Title, &current.Level, &current.Status, &publishedAtNull, &current.Revision}
	if input.Content != nil {
		columns = "id, title, content, level, status, published_at, updated_at"
		dest = []any{&current.ID, &current.Title, &contentNull, &current.Level, &current.Status, &publishedAtNull, &current.Revision}
	}
	rows := tx.QueryRowContext(ctx, s.bind(`SELECT `+columns+` FROM `+s.table("announcements")+` WHERE id = ?`), id)
	if err := rows.Scan(dest...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	current.PublishedAt = nullStringPtr(publishedAtNull)
	if input.Content != nil {
		// content is NOT NULL; mirror Node's `row.content ?? undefined`.
		content := contentNull.String
		current.Content = &content
	}
	before := current

	expected := strings.TrimSpace(expectedRevision)
	if expected == "" || current.Revision != expected {
		return nil, &ConflictError{Message: "公告已被其他操作更新，请刷新后重试", CurrentRevision: current.Revision}
	}

	assignments := []string{}
	args := []any{}
	if input.Title != nil {
		title, err := normalizeText(*input.Title)
		if err != nil {
			return nil, err
		}
		if title != current.Title {
			assignments = append(assignments, "title = ?")
			args = append(args, title)
			current.Title = title
		}
	}
	if input.Content != nil {
		content, err := normalizeText(*input.Content)
		if err != nil {
			return nil, err
		}
		// Node addChangedAssignment: same content is a no-op column.
		if current.Content == nil || content != *current.Content {
			assignments = append(assignments, "content = ?")
			args = append(args, content)
		}
		current.Content = &content
	}
	if input.Level != nil {
		level, err := normalizeLevel(input.Level, current.Level)
		if err != nil {
			return nil, err
		}
		if level != current.Level {
			assignments = append(assignments, "level = ?")
			args = append(args, level)
			current.Level = level
		}
	}
	becamePublished := false
	if input.Status != nil {
		status, err := normalizeStatus(input.Status, current.Status)
		if err != nil {
			return nil, err
		}
		if status != current.Status {
			assignments = append(assignments, "status = ?")
			args = append(args, status)
			becamePublished = status == "published"
			current.Status = status
		}
	}

	if len(assignments) == 0 {
		// No-op: the receipt carries the unchanged revision (Node changed:
		// false, after: current).
		return &MutationOutcome{
			Receipt: MutationReceipt{ID: before.ID, Revision: before.Revision},
			Before:  before,
			After:   before,
			Changed: false,
		}, nil
	}

	revision := nextRevision(before.Revision, s.now())
	after := current
	after.Revision = revision
	if becamePublished {
		assignments = append(assignments, "published_at = ?")
		args = append(args, revision)
		publishedAt := revision
		after.PublishedAt = &publishedAt
	}
	assignments = append(assignments, "updated_by = ?", "updated_at = ?")
	args = append(args, actorSystemAccountID, revision)
	args = append(args, id, before.Revision)
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("announcements")+` SET `+strings.Join(assignments, ", ")+` WHERE id = ? AND updated_at = ?`), args...)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return nil, &ConflictError{Message: "公告已被其他操作更新，请刷新后重试", CurrentRevision: before.Revision}
	}
	if becamePublished {
		if _, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("announcement_reads")+` WHERE announcement_id = ?`), id); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &MutationOutcome{
		Receipt: MutationReceipt{ID: id, Revision: revision},
		Before:  before,
		After:   after,
		Changed: true,
	}, nil
}

// DeleteOutcome mirrors the delete AnnouncementManagementMutationOutcome:
// the receipt plus the pre-delete logged state for the operation log.
type DeleteOutcome struct {
	Receipt MutationReceipt
	Before  MutationState
}

// Delete mirrors deleteAnnouncementForManagementAsync: expectedRevision
// compare then a guarded DELETE; the outcome carries the before state.
func (s *Store) Delete(ctx context.Context, id, expectedRevision string) (*DeleteOutcome, error) {
	ctx = ensureCtx(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var before MutationState
	var publishedAtNull sql.NullString
	err = tx.QueryRowContext(ctx, s.bind(`SELECT id, title, level, status, published_at, updated_at
		FROM `+s.table("announcements")+` WHERE id = ?`), id).
		Scan(&before.ID, &before.Title, &before.Level, &before.Status, &publishedAtNull, &before.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	before.PublishedAt = nullStringPtr(publishedAtNull)
	expected := strings.TrimSpace(expectedRevision)
	if expected == "" || before.Revision != expected {
		return nil, &ConflictError{Message: "公告已被其他操作更新，请刷新后重试", CurrentRevision: before.Revision}
	}
	result, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("announcements")+` WHERE id = ? AND updated_at = ?`), id, before.Revision)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return nil, &ConflictError{Message: "公告已被其他操作更新，请刷新后重试", CurrentRevision: before.Revision}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &DeleteOutcome{Receipt: MutationReceipt{ID: id, Revision: before.Revision}, Before: before}, nil
}

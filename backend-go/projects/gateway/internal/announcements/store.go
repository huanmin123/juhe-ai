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

// AdminListItem mirrors AnnouncementListItem (editVersion = updated_at).
type AdminListItem struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	Level       string  `json:"level"`
	Status      string  `json:"status"`
	PublishedAt *string `json:"publishedAt,omitempty"`
	CreatedAt   string  `json:"createdAt"`
	UpdatedAt   string  `json:"updatedAt"`
	EditVersion string  `json:"editVersion"`
}

// ListPage mirrors listAnnouncementsPageAsync (pageSize+1 probe).
func (s *Store) ListPage(ctx context.Context, page, pageSize int) (items []AdminListItem, hasMore bool, err error) {
	ctx = ensureCtx(ctx)
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT a.id, a.title, a.level, a.status, a.published_at, a.created_at, a.updated_at
		FROM `+s.table("announcements")+` a
		ORDER BY a.updated_at DESC, a.created_at DESC, a.id DESC
		LIMIT ? OFFSET ?`), pageSize+1, (page-1)*pageSize)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	items = []AdminListItem{}
	for rows.Next() {
		var item AdminListItem
		var publishedAt sql.NullString
		if err := rows.Scan(&item.ID, &item.Title, &item.Level, &item.Status, &publishedAt, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, false, err
		}
		if publishedAt.Valid {
			item.PublishedAt = &publishedAt.String
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
	_, err = s.db.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("announcements")+`
		(id, title, content, level, status, created_by, updated_by, published_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		id, title, content, level, status, actorSystemAccountID, actorSystemAccountID,
		status == "published", status == "published", revision, revision)
	if err != nil {
		return MutationReceipt{}, err
	}
	return MutationReceipt{ID: id, Revision: revision}, nil
}

// Patch mirrors patchAnnouncementForManagementAsync: FOR UPDATE (pg),
// expectedRevision compare, only-changed columns, published transition clears
// read state, nextRevision monotonic.
func (s *Store) Patch(ctx context.Context, id string, input MutationInput, expectedRevision, actorSystemAccountID string) (*MutationReceipt, error) {
	ctx = ensureCtx(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	columns := "id, title, level, status, published_at, updated_at"
	rows := tx.QueryRowContext(ctx, s.bind(`SELECT `+columns+` FROM `+s.table("announcements")+` WHERE id = ?`), id)
	var current struct {
		id, title, level, status string
		publishedAt              sql.NullString
		revision                 string
	}
	if err := rows.Scan(&current.id, &current.title, &current.level, &current.status, &current.publishedAt, &current.revision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	expected := strings.TrimSpace(expectedRevision)
	if expected == "" || current.revision != expected {
		return nil, &ConflictError{Message: "公告已被其他操作更新，请刷新后重试", CurrentRevision: current.revision}
	}

	assignments := []string{}
	args := []any{}
	next := map[string]string{"title": current.title, "level": current.level, "status": current.status}
	if input.Title != nil {
		title, err := normalizeText(*input.Title)
		if err != nil {
			return nil, err
		}
		if title != current.title {
			assignments = append(assignments, "title = ?")
			args = append(args, title)
		}
		next["title"] = title
	}
	if input.Content != nil {
		content, err := normalizeText(*input.Content)
		if err != nil {
			return nil, err
		}
		assignments = append(assignments, "content = ?")
		args = append(args, content)
	}
	if input.Level != nil {
		level, err := normalizeLevel(input.Level, current.level)
		if err != nil {
			return nil, err
		}
		if level != current.level {
			assignments = append(assignments, "level = ?")
			args = append(args, level)
		}
		next["level"] = level
	}
	becamePublished := false
	if input.Status != nil {
		status, err := normalizeStatus(input.Status, current.status)
		if err != nil {
			return nil, err
		}
		if status != current.status {
			assignments = append(assignments, "status = ?")
			args = append(args, status)
			becamePublished = status == "published"
		}
		next["status"] = status
	}

	if len(assignments) == 0 {
		receipt := MutationReceipt{ID: current.id, Revision: current.revision}
		return &receipt, nil
	}

	revision := nextRevision(current.revision, s.now())
	if becamePublished {
		assignments = append(assignments, "published_at = ?")
		args = append(args, revision)
	}
	assignments = append(assignments, "updated_by = ?", "updated_at = ?")
	args = append(args, actorSystemAccountID, revision)
	args = append(args, id, current.revision)
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("announcements")+` SET `+strings.Join(assignments, ", ")+` WHERE id = ? AND updated_at = ?`), args...)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return nil, &ConflictError{Message: "公告已被其他操作更新，请刷新后重试", CurrentRevision: current.revision}
	}
	if becamePublished {
		if _, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("announcement_reads")+` WHERE announcement_id = ?`), id); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &MutationReceipt{ID: id, Revision: revision}, nil
}

// Delete mirrors deleteAnnouncementForManagementAsync.
func (s *Store) Delete(ctx context.Context, id, expectedRevision string) (*MutationReceipt, error) {
	ctx = ensureCtx(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var revision string
	err = tx.QueryRowContext(ctx, s.bind(`SELECT updated_at FROM `+s.table("announcements")+` WHERE id = ?`), id).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	expected := strings.TrimSpace(expectedRevision)
	if expected == "" || revision != expected {
		return nil, &ConflictError{Message: "公告已被其他操作更新，请刷新后重试", CurrentRevision: revision}
	}
	result, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("announcements")+` WHERE id = ? AND updated_at = ?`), id, revision)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return nil, &ConflictError{Message: "公告已被其他操作更新，请刷新后重试", CurrentRevision: revision}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &MutationReceipt{ID: id, Revision: revision}, nil
}

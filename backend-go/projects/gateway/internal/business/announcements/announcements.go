// Package announcements contains the Gateway-owned Business announcement
// primitives. It is intentionally storage-local: callers provide the
// authenticated actor, while this package only enforces the Business owner
// handoff gate and SQL transaction semantics.
//
// The package does not create schema, bind HTTP routes, call Node, or publish
// directly to an operation-log/cache transport. AfterCommitPort is the only
// side-effect seam; when configured on Service it is called only after a
// successful database commit.
package announcements

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

var (
	ErrOwnerGate        = errors.New("Business SQLite owner handoff gate is not satisfied")
	ErrForbidden        = errors.New("actor is not allowed to manage announcements")
	ErrNotFound         = errors.New("announcement not found")
	ErrRevisionConflict = errors.New("announcement revision conflict")
	ErrCAS              = ErrRevisionConflict
	ErrInvalidInput     = errors.New("announcement input is invalid")
	ErrInvalidMode      = errors.New("announcement database mode is invalid")
	ErrInvalidSchema    = errors.New("announcement PostgreSQL schema is invalid")
)

type Mode string

const (
	SQLite   Mode = "sqlite"
	Postgres Mode = "postgres"
)

const (
	PublicLimit          = 30
	DefaultPageSize      = 50
	MaxPageSize          = 100
	AdminWindowRows      = 1001
	TitleMaxUTF16        = 120
	ContentMaxUTF16      = 5000
	ContentPreviewLength = 240
)

var postgresIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var idSequence atomic.Uint64

// OwnerGate is external, auditable handoff evidence. A partial handoff never
// permits an announcement write even if the relations happen to exist.
type OwnerGate struct {
	Confirmed         bool
	SchemaReady       bool
	NodeWriterStopped bool
}

func (g OwnerGate) Ready() bool {
	return g.Confirmed && g.SchemaReady && g.NodeWriterStopped
}

// Actor is authenticated by the caller. This package does not infer scope
// from a database row or session and never broadens a caller-provided scope.
type Actor struct {
	SystemAccountID string
	Role            string
}

func (a Actor) Admin() bool {
	return a.Role == "admin" || a.Role == "super_admin"
}

type Level string

// Compatibility aliases retain the Node-shaped domain vocabulary for future
// adapters without introducing a second representation.
type AnnouncementLevel = Level

const (
	LevelCritical Level = "critical"
	LevelWarning  Level = "warning"
	LevelInfo     Level = "info"
	LevelNormal   Level = "normal"
)

type Status string

type AnnouncementStatus = Status

const (
	StatusDraft     Status = "draft"
	StatusPublished Status = "published"
	StatusArchived  Status = "archived"
)

type Announcement struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	Content     string  `json:"content"`
	Level       Level   `json:"level"`
	Status      Status  `json:"status"`
	CreatedBy   string  `json:"createdBy,omitempty"`
	UpdatedBy   string  `json:"updatedBy,omitempty"`
	PublishedAt *string `json:"publishedAt,omitempty"`
	CreatedAt   string  `json:"createdAt"`
	UpdatedAt   string  `json:"updatedAt"`
}

type AnnouncementSummary = Announcement

type PublicListItem struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	Level       Level   `json:"level"`
	PublishedAt string  `json:"publishedAt"`
	ReadAt      *string `json:"readAt,omitempty"`
}

type PublicDetail struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Content     string `json:"content"`
	Level       Level  `json:"level"`
	PublishedAt string `json:"publishedAt"`
}

type AdminListItem struct {
	ID               string  `json:"id"`
	Title            string  `json:"title"`
	ContentPreview   string  `json:"contentPreview"`
	ContentTruncated bool    `json:"contentTruncated"`
	Level            Level   `json:"level"`
	Status           Status  `json:"status"`
	UpdatedByName    *string `json:"updatedByName,omitempty"`
	PublishedAt      *string `json:"publishedAt,omitempty"`
	Revision         string  `json:"revision"`
}

type AnnouncementListItem = AdminListItem

type AdminDetail struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Content  string `json:"content"`
	Level    Level  `json:"level"`
	Status   Status `json:"status"`
	Revision string `json:"revision"`
}

type AnnouncementEditDetail = AdminDetail

type CreateInput struct {
	Title   string `json:"title"`
	Content string `json:"content"`
	Level   Level  `json:"level,omitempty"`
	Status  Status `json:"status,omitempty"`
}

type AnnouncementInput = CreateInput
type AnnouncementManagementCreateInput = CreateInput

type PatchInput struct {
	ExpectedRevision string  `json:"expectedRevision,omitempty"`
	Title            *string `json:"title,omitempty"`
	Content          *string `json:"content,omitempty"`
	Level            *Level  `json:"level,omitempty"`
	Status           *Status `json:"status,omitempty"`
}

type AnnouncementManagementPatchInput = PatchInput

// PatchRequest is the strict JSON request shape used by management patch,
// publish and unpublish callers. The Store itself accepts a separated CAS
// token so transport code cannot accidentally omit it.
type PatchRequest struct {
	ExpectedRevision string  `json:"expectedRevision"`
	Title            *string `json:"title,omitempty"`
	Content          *string `json:"content,omitempty"`
	Level            *Level  `json:"level,omitempty"`
	Status           *Status `json:"status,omitempty"`
}

type ReadResult struct {
	ReadAt string `json:"readAt"`
	Count  int64  `json:"count"`
}

type AnnouncementReadResult = ReadResult

type ListOptions struct {
	Page     int
	PageSize int
}

type AnnouncementListOptions = ListOptions

type ListResult struct {
	Items    []AdminListItem `json:"items"`
	Total    int             `json:"total"`
	HasMore  bool            `json:"hasMore"`
	Page     int             `json:"page"`
	PageSize int             `json:"pageSize"`
}

type AnnouncementListResult = ListResult
type PublicAnnouncementListItem = PublicListItem
type PublicAnnouncementDetail = PublicDetail

type MutationReceipt struct {
	ID       string `json:"id"`
	Revision string `json:"revision"`
}

type MutationState struct {
	ID          string
	Title       string
	Content     string
	Level       Level
	Status      Status
	PublishedAt *string
	Revision    string
}

type MutationOutcome struct {
	Receipt MutationReceipt
	Before  *MutationState
	After   *MutationState
	Changed bool
}

type AnnouncementManagementMutationOutcome = MutationOutcome
type AnnouncementMutationReceipt = MutationReceipt
type AnnouncementMutationState = MutationState

// DeleteResult makes the HTTP 204 decision explicit to a future adapter.
// Deleted=false is a deterministic not-found result; no deletion is implied.
type DeleteResult struct {
	Deleted  bool
	ID       string
	Revision string
	Before   *MutationState
}

// AfterCommitEvent contains only safe identity/state metadata. In particular,
// announcement content is never sent to operation-log or cache adapters.
type AfterCommitEvent struct {
	Action               string `json:"action"`
	AnnouncementID       string `json:"announcementId"`
	Revision             string `json:"revision"`
	Status               Status `json:"status"`
	ActorSystemAccountID string `json:"actorSystemAccountId,omitempty"`
	ActorRole            string `json:"actorRole,omitempty"`
	PublicAction         string `json:"publicAction,omitempty"` // "upsert", "delete", or ""
}

// AfterCommitPort is deliberately narrow. Implementations may enqueue or
// invalidate outside this package; this package never claims delivery.
type AfterCommitPort interface {
	AfterAnnouncementCommit(context.Context, AfterCommitEvent) error
}

type Store struct {
	db     *sql.DB
	mode   Mode
	schema string
	gate   OwnerGate
	now    func() time.Time
	lastMS atomic.Int64
}

func NewStore(db *sql.DB, mode Mode, schema string, gate OwnerGate) (*Store, error) {
	if db == nil {
		return nil, errors.New("announcements database is required")
	}
	if mode != SQLite && mode != Postgres {
		return nil, ErrInvalidMode
	}
	schema = strings.TrimSpace(schema)
	if mode == Postgres {
		if schema == "" {
			schema = "juhe_business"
		}
		if !postgresIdentifier.MatchString(schema) {
			return nil, ErrInvalidSchema
		}
	}
	return &Store{db: db, mode: mode, schema: schema, gate: gate, now: time.Now}, nil
}

// NewStoreWithClock is useful to deterministic integration callers. The clock
// is an in-process dependency only; it cannot alter persisted revision input.
func NewStoreWithClock(db *sql.DB, mode Mode, schema string, gate OwnerGate, now func() time.Time) (*Store, error) {
	store, err := NewStore(db, mode, schema, gate)
	if err != nil {
		return nil, err
	}
	if now != nil {
		store.now = now
	}
	return store, nil
}

// New constructs the caller-facing service with no configured side-effect
// sink. Storage-only tests and future adapters can use NewStore instead.
func New(db *sql.DB, mode Mode, schema string, gate OwnerGate) (*Service, error) {
	store, err := NewStore(db, mode, schema, gate)
	if err != nil {
		return nil, err
	}
	return &Service{store: store}, nil
}

func (s *Store) requireOwner() error {
	if s == nil || s.db == nil || !s.gate.Ready() {
		return ErrOwnerGate
	}
	return nil
}

func (s *Store) table(name string) string {
	if s.mode == Postgres {
		// schema is validated at construction; table names are package
		// constants, so qualification remains injection-safe and matches the
		// existing Gateway SQL contract.
		return s.schema + "." + name
	}
	return name
}

func (s *Store) bind(query string) string {
	if s.mode != Postgres {
		return query
	}
	var b strings.Builder
	index := 1
	for _, r := range query {
		if r == '?' {
			fmt.Fprintf(&b, "$%d", index)
			index++
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// CheckContract verifies pre-existing relations only. It never creates or
// alters schema and does not require the write gate, allowing preflight to
// establish SchemaReady evidence.
func (s *Store) CheckContract(ctx context.Context) error {
	if s == nil || s.db == nil {
		return ErrOwnerGate
	}
	for _, relation := range []struct {
		name    string
		columns string
	}{
		{name: "announcements", columns: "id,title,content,level,status,created_by,updated_by,published_at,created_at,updated_at"},
		{name: "announcement_reads", columns: "announcement_id,system_account_id,read_at"},
		{name: "system_accounts", columns: "id,display_name"},
	} {
		if _, err := s.db.ExecContext(ctx, "SELECT "+relation.columns+" FROM "+s.table(relation.name)+" LIMIT 0"); err != nil {
			return fmt.Errorf("verify announcements relation %s: %w", relation.name, err)
		}
	}
	return nil
}

// ModeName returns the configured SQL mode for diagnostics and contract tests;
// it does not expose the underlying database handle.
func (s *Store) ModeName() Mode {
	if s == nil {
		return ""
	}
	return s.mode
}

func (s *Store) stamp() string {
	// Node Date#toISOString() emits milliseconds. The monotonic fence avoids a
	// same-clock-tick revision collision under concurrent management writes.
	current := s.now().UTC().UnixMilli()
	for {
		previous := s.lastMS.Load()
		if current <= previous {
			current = previous + 1
		}
		if s.lastMS.CompareAndSwap(previous, current) {
			return time.UnixMilli(current).UTC().Format("2006-01-02T15:04:05.000Z")
		}
	}
}

func newID() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("ann_%d_%d", time.Now().UnixNano(), idSequence.Add(1))
	}
	return "ann_" + hex.EncodeToString(raw[:]) + fmt.Sprintf("_%d", idSequence.Add(1))
}

func (s *Store) ListPublicAnnouncements(ctx context.Context, systemAccountID string, limit int) ([]PublicListItem, error) {
	if err := s.requireOwner(); err != nil {
		return nil, err
	}
	systemAccountID = strings.TrimSpace(systemAccountID)
	if systemAccountID == "" {
		return nil, ErrForbidden
	}
	limit, err := normalizePublicLimit(limit)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`
		SELECT a.id,a.title,a.level,a.published_at,r.read_at
		FROM `+s.table("announcements")+` a
		LEFT JOIN `+s.table("announcement_reads")+` r
		  ON r.announcement_id=a.id AND r.system_account_id=?
		WHERE a.status='published' AND a.published_at IS NOT NULL
		ORDER BY a.published_at DESC,a.created_at DESC,a.id DESC
		LIMIT ?`), systemAccountID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]PublicListItem, 0, limit)
	for rows.Next() {
		var item PublicListItem
		var readAt sql.NullString
		if err := rows.Scan(&item.ID, &item.Title, &item.Level, &item.PublishedAt, &readAt); err != nil {
			return nil, err
		}
		if readAt.Valid {
			item.ReadAt = &readAt.String
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) FindPublicAnnouncement(ctx context.Context, id string) (PublicDetail, error) {
	if err := s.requireOwner(); err != nil {
		return PublicDetail{}, err
	}
	if strings.TrimSpace(id) == "" {
		return PublicDetail{}, ErrNotFound
	}
	var out PublicDetail
	err := s.db.QueryRowContext(ctx, s.bind(`
		SELECT id,title,content,level,published_at
		FROM `+s.table("announcements")+`
		WHERE id=? AND status='published' AND published_at IS NOT NULL`), id).
		Scan(&out.ID, &out.Title, &out.Content, &out.Level, &out.PublishedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return PublicDetail{}, ErrNotFound
	}
	if err != nil {
		return PublicDetail{}, err
	}
	return out, nil
}

func (s *Store) MarkPublicAnnouncementsRead(ctx context.Context, systemAccountID string, ids []string) (ReadResult, error) {
	if err := s.requireOwner(); err != nil {
		return ReadResult{}, err
	}
	systemAccountID = strings.TrimSpace(systemAccountID)
	if systemAccountID == "" {
		return ReadResult{}, ErrForbidden
	}
	normalized, err := normalizeIDs(ids)
	if err != nil {
		return ReadResult{}, err
	}
	readAt := s.stamp()
	if len(normalized) == 0 {
		return ReadResult{ReadAt: readAt}, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ReadResult{}, err
	}
	defer tx.Rollback()
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(normalized)), ",")
	args := make([]any, 0, len(normalized)+2)
	args = append(args, systemAccountID, readAt)
	for _, id := range normalized {
		args = append(args, id)
	}
	result, err := tx.ExecContext(ctx, s.bind(`
		INSERT INTO `+s.table("announcement_reads")+` (announcement_id,system_account_id,read_at)
		SELECT a.id,?,? FROM `+s.table("announcements")+` a
		WHERE a.id IN (`+placeholders+`) AND a.status='published' AND a.published_at IS NOT NULL
		ON CONFLICT(announcement_id,system_account_id) DO NOTHING`), args...)
	if err != nil {
		return ReadResult{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return ReadResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ReadResult{}, err
	}
	return ReadResult{ReadAt: readAt, Count: count}, nil
}

func (s *Store) ListAnnouncements(ctx context.Context, options ListOptions) (ListResult, error) {
	if err := s.requireOwner(); err != nil {
		return ListResult{}, err
	}
	page, pageSize, err := normalizeListOptions(options)
	if err != nil {
		return ListResult{}, err
	}
	offset := (page - 1) * pageSize
	rows, err := s.db.QueryContext(ctx, s.bind(`
		SELECT a.id,a.title,
		 CASE WHEN length(a.content)>240 THEN substr(a.content,1,240)||'...' ELSE a.content END,
		 CASE WHEN length(a.content)>240 THEN 1 ELSE 0 END,
		 a.level,a.status,sa.display_name,a.published_at,a.updated_at
		FROM `+s.table("announcements")+` a
		LEFT JOIN `+s.table("system_accounts")+` sa ON sa.id=a.updated_by
		ORDER BY a.updated_at DESC,a.created_at DESC,a.id DESC
		LIMIT ? OFFSET ?`), pageSize+1, offset)
	if err != nil {
		return ListResult{}, err
	}
	defer rows.Close()
	items := make([]AdminListItem, 0, pageSize)
	rowCount := 0
	for rows.Next() {
		var item AdminListItem
		var truncated int
		var updatedByName sql.NullString
		var publishedAt sql.NullString
		if err := rows.Scan(&item.ID, &item.Title, &item.ContentPreview, &truncated, &item.Level, &item.Status, &updatedByName, &publishedAt, &item.Revision); err != nil {
			return ListResult{}, err
		}
		rowCount++
		if rowCount > pageSize {
			continue
		}
		item.ContentTruncated = truncated != 0
		if updatedByName.Valid {
			item.UpdatedByName = &updatedByName.String
		}
		if publishedAt.Valid {
			item.PublishedAt = &publishedAt.String
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return ListResult{}, err
	}
	// A page-size-plus-one query provides an upper bound, not an exact count.
	hasMore := rowCount > pageSize
	return ListResult{Items: items, Total: offset + len(items) + boolInt(hasMore), HasMore: hasMore, Page: page, PageSize: pageSize}, nil
}

func (s *Store) FindAnnouncement(ctx context.Context, id string) (Announcement, error) {
	if err := s.requireOwner(); err != nil {
		return Announcement{}, err
	}
	var out Announcement
	var published, updatedBy sql.NullString
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT id,title,content,level,status,created_by,updated_by,published_at,created_at,updated_at FROM `+s.table("announcements")+` WHERE id=?`), id).
		Scan(&out.ID, &out.Title, &out.Content, &out.Level, &out.Status, &out.CreatedBy, &updatedBy, &published, &out.CreatedAt, &out.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Announcement{}, ErrNotFound
	}
	if err != nil {
		return Announcement{}, err
	}
	if updatedBy.Valid {
		out.UpdatedBy = updatedBy.String
	}
	if published.Valid {
		out.PublishedAt = &published.String
	}
	return out, nil
}

func (s *Store) FindAnnouncementDetail(ctx context.Context, id string) (AdminDetail, error) {
	if err := s.requireOwner(); err != nil {
		return AdminDetail{}, err
	}
	var out AdminDetail
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT id,title,content,level,status,updated_at FROM `+s.table("announcements")+` WHERE id=?`), id).
		Scan(&out.ID, &out.Title, &out.Content, &out.Level, &out.Status, &out.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return AdminDetail{}, ErrNotFound
	}
	if err != nil {
		return AdminDetail{}, err
	}
	return out, nil
}

func (s *Store) CreateAnnouncement(ctx context.Context, input CreateInput, actorSystemAccountID string) (MutationOutcome, error) {
	if err := s.requireOwner(); err != nil {
		return MutationOutcome{}, err
	}
	actorSystemAccountID = strings.TrimSpace(actorSystemAccountID)
	if actorSystemAccountID == "" {
		return MutationOutcome{}, ErrForbidden
	}
	title, content, level, status, err := normalizeCreate(input)
	if err != nil {
		return MutationOutcome{}, err
	}
	id, revision := newID(), s.stamp()
	var published any
	if status == StatusPublished {
		published = revision
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MutationOutcome{}, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("announcements")+` (id,title,content,level,status,created_by,updated_by,published_at,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?)`), id, title, content, level, status, actorSystemAccountID, actorSystemAccountID, published, revision, revision)
	if err != nil {
		return MutationOutcome{}, err
	}
	if err := tx.Commit(); err != nil {
		return MutationOutcome{}, err
	}
	after := &MutationState{ID: id, Title: title, Content: content, Level: level, Status: status, Revision: revision}
	if status == StatusPublished {
		after.PublishedAt = &revision
	}
	return MutationOutcome{Receipt: MutationReceipt{ID: id, Revision: revision}, After: after, Changed: true}, nil
}

func (s *Store) PatchAnnouncement(ctx context.Context, id, actorSystemAccountID, expectedRevision string, input PatchInput) (*MutationOutcome, error) {
	if err := s.requireOwner(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(actorSystemAccountID) == "" {
		return nil, ErrForbidden
	}
	if err := validatePatch(input); err != nil {
		return nil, err
	}
	inputExpectedRevision := strings.TrimSpace(input.ExpectedRevision)
	expectedRevision = strings.TrimSpace(expectedRevision)
	if inputExpectedRevision != "" {
		if expectedRevision != "" && expectedRevision != inputExpectedRevision {
			return nil, fmt.Errorf("%w: expected revisions disagree", ErrInvalidInput)
		}
		expectedRevision = inputExpectedRevision
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	selectSQL := `SELECT id,title,content,level,status,published_at,updated_at FROM ` + s.table("announcements") + ` WHERE id=?`
	if s.mode == Postgres {
		selectSQL += " FOR UPDATE"
	}
	current, err := scanMutationRow(ctx, tx, s.bind(selectSQL), id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if expectedRevision == "" || current.Revision != expectedRevision {
		return nil, &RevisionConflictError{AnnouncementID: id, ExpectedRevision: expectedRevision, CurrentRevision: current.Revision}
	}
	next := *current
	if input.Title != nil {
		next.Title, err = normalizeText(*input.Title, "title", TitleMaxUTF16)
		if err != nil {
			return nil, err
		}
	}
	if input.Content != nil {
		next.Content, err = normalizeText(*input.Content, "content", ContentMaxUTF16)
		if err != nil {
			return nil, err
		}
	}
	if input.Level != nil {
		next.Level, err = normalizeLevel(*input.Level)
		if err != nil {
			return nil, err
		}
	}
	if input.Status != nil {
		next.Status, err = normalizeStatus(*input.Status)
		if err != nil {
			return nil, err
		}
	}
	if next.Title == current.Title && next.Content == current.Content && next.Level == current.Level && next.Status == current.Status {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return &MutationOutcome{Receipt: MutationReceipt{ID: current.ID, Revision: current.Revision}, Before: current, After: current, Changed: false}, nil
	}
	revision, err := s.nextRevision(current.Revision)
	if err != nil {
		return nil, err
	}
	assignments := make([]string, 0, 6)
	args := make([]any, 0, 8)
	if next.Title != current.Title {
		assignments = append(assignments, "title=?")
		args = append(args, next.Title)
	}
	if next.Content != current.Content {
		assignments = append(assignments, "content=?")
		args = append(args, next.Content)
	}
	if next.Level != current.Level {
		assignments = append(assignments, "level=?")
		args = append(args, next.Level)
	}
	if next.Status != current.Status {
		assignments = append(assignments, "status=?")
		args = append(args, next.Status)
	}
	if next.Status == StatusPublished && current.Status != StatusPublished {
		assignments = append(assignments, "published_at=?")
		args = append(args, revision)
	}
	assignments = append(assignments, "updated_by=?", "updated_at=?")
	args = append(args, actorSystemAccountID, revision, id, current.Revision)
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("announcements")+` SET `+strings.Join(assignments, ",")+` WHERE id=? AND updated_at=?`), args...)
	if err != nil {
		return nil, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return nil, &RevisionConflictError{AnnouncementID: id, ExpectedRevision: expectedRevision, CurrentRevision: current.Revision}
	}
	becamePublished := next.Status == StatusPublished && current.Status != StatusPublished
	if becamePublished {
		if _, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("announcement_reads")+` WHERE announcement_id=?`), id); err != nil {
			return nil, err
		}
		next.PublishedAt = stringPtr(revision)
	}
	next.Revision = revision
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &MutationOutcome{Receipt: MutationReceipt{ID: id, Revision: revision}, Before: current, After: &next, Changed: true}, nil
}

func (s *Store) PublishAnnouncement(ctx context.Context, id, actorSystemAccountID, expectedRevision string) (*MutationOutcome, error) {
	status := StatusPublished
	return s.PatchAnnouncement(ctx, id, actorSystemAccountID, expectedRevision, PatchInput{Status: &status})
}

func (s *Store) UnpublishAnnouncement(ctx context.Context, id, actorSystemAccountID, expectedRevision string) (*MutationOutcome, error) {
	status := StatusArchived
	return s.PatchAnnouncement(ctx, id, actorSystemAccountID, expectedRevision, PatchInput{Status: &status})
}

func (s *Store) DeleteAnnouncement(ctx context.Context, id, expectedRevision string) (DeleteResult, error) {
	if err := s.requireOwner(); err != nil {
		return DeleteResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DeleteResult{}, err
	}
	defer tx.Rollback()
	selectSQL := `SELECT id,title,content,level,status,published_at,updated_at FROM ` + s.table("announcements") + ` WHERE id=?`
	if s.mode == Postgres {
		selectSQL += " FOR UPDATE"
	}
	current, err := scanMutationRow(ctx, tx, s.bind(selectSQL), id)
	if errors.Is(err, sql.ErrNoRows) {
		return DeleteResult{ID: id}, nil
	}
	if err != nil {
		return DeleteResult{}, err
	}
	if strings.TrimSpace(expectedRevision) == "" || current.Revision != strings.TrimSpace(expectedRevision) {
		return DeleteResult{}, &RevisionConflictError{AnnouncementID: id, ExpectedRevision: strings.TrimSpace(expectedRevision), CurrentRevision: current.Revision}
	}
	if _, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("announcement_reads")+` WHERE announcement_id=?`), id); err != nil {
		return DeleteResult{}, err
	}
	result, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("announcements")+` WHERE id=? AND updated_at=?`), id, current.Revision)
	if err != nil {
		return DeleteResult{}, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return DeleteResult{}, &RevisionConflictError{AnnouncementID: id, ExpectedRevision: expectedRevision, CurrentRevision: current.Revision}
	}
	if err := tx.Commit(); err != nil {
		return DeleteResult{}, err
	}
	return DeleteResult{Deleted: true, ID: id, Revision: current.Revision, Before: current}, nil
}

func (s *Store) nextRevision(current string) (string, error) {
	parsed, err := time.Parse(time.RFC3339Nano, current)
	if err != nil {
		return "", fmt.Errorf("announcement revision must be RFC3339: %w", err)
	}
	candidate := s.now().UTC().UnixMilli()
	if candidate <= parsed.UnixMilli() {
		candidate = parsed.UnixMilli() + 1
	}
	for {
		previous := s.lastMS.Load()
		if candidate <= previous {
			candidate = previous + 1
		}
		if s.lastMS.CompareAndSwap(previous, candidate) {
			return time.UnixMilli(candidate).UTC().Format("2006-01-02T15:04:05.000Z"), nil
		}
	}
}

type RevisionConflictError struct {
	AnnouncementID   string
	ExpectedRevision string
	CurrentRevision  string
}

type AnnouncementRevisionConflictError = RevisionConflictError

func (e *RevisionConflictError) Error() string {
	return fmt.Sprintf("%v: expected %q actual %q", ErrRevisionConflict, e.ExpectedRevision, e.CurrentRevision)
}

func (e *RevisionConflictError) Unwrap() error { return ErrRevisionConflict }

// Service applies caller-provided actor scope/auth and optionally invokes a
// post-commit side-effect seam. A side-effect error never rolls back a commit.
type Service struct {
	store       *Store
	afterCommit AfterCommitPort
}

func NewService(store *Store, afterCommit AfterCommitPort) (*Service, error) {
	if store == nil {
		return nil, errors.New("announcements store is required")
	}
	return &Service{store: store, afterCommit: afterCommit}, nil
}

func NewServiceWithAfterCommit(store *Store, afterCommit AfterCommitPort) (*Service, error) {
	return NewService(store, afterCommit)
}

func (s *Service) requireActor(actor Actor) error {
	if s == nil || s.store == nil {
		return ErrOwnerGate
	}
	if err := s.store.requireOwner(); err != nil {
		return err
	}
	if strings.TrimSpace(actor.SystemAccountID) == "" {
		return ErrForbidden
	}
	return nil
}

func (s *Service) CheckContract(ctx context.Context) error {
	if s == nil || s.store == nil {
		return ErrOwnerGate
	}
	return s.store.CheckContract(ctx)
}

func (s *Service) requireAdmin(actor Actor) error {
	if err := s.requireActor(actor); err != nil {
		return err
	}
	if !actor.Admin() {
		return ErrForbidden
	}
	return nil
}

func (s *Service) ListPublicAnnouncements(ctx context.Context, actor Actor, limit int) ([]PublicListItem, error) {
	if err := s.requireActor(actor); err != nil {
		return nil, err
	}
	return s.store.ListPublicAnnouncements(ctx, actor.SystemAccountID, limit)
}

func (s *Service) FindPublicAnnouncement(ctx context.Context, actor Actor, id string) (PublicDetail, error) {
	if err := s.requireActor(actor); err != nil {
		return PublicDetail{}, err
	}
	return s.store.FindPublicAnnouncement(ctx, id)
}

func (s *Service) MarkPublicAnnouncementsRead(ctx context.Context, actor Actor, ids []string) (ReadResult, error) {
	if err := s.requireActor(actor); err != nil {
		return ReadResult{}, err
	}
	return s.store.MarkPublicAnnouncementsRead(ctx, actor.SystemAccountID, ids)
}

func (s *Service) ListAnnouncements(ctx context.Context, actor Actor, options ListOptions) (ListResult, error) {
	if err := s.requireAdmin(actor); err != nil {
		return ListResult{}, err
	}
	return s.store.ListAnnouncements(ctx, options)
}

func (s *Service) FindAnnouncement(ctx context.Context, actor Actor, id string) (AdminDetail, error) {
	if err := s.requireAdmin(actor); err != nil {
		return AdminDetail{}, err
	}
	return s.store.FindAnnouncementDetail(ctx, id)
}

func (s *Service) CreateAnnouncement(ctx context.Context, actor Actor, input CreateInput) (MutationOutcome, error) {
	if err := s.requireAdmin(actor); err != nil {
		return MutationOutcome{}, err
	}
	outcome, err := s.store.CreateAnnouncement(ctx, input, actor.SystemAccountID)
	if err != nil {
		return MutationOutcome{}, err
	}
	return outcome, s.emit(ctx, AfterCommitEvent{Action: "create", AnnouncementID: outcome.Receipt.ID, Revision: outcome.Receipt.Revision, Status: outcome.After.Status, ActorSystemAccountID: actor.SystemAccountID, ActorRole: actor.Role, PublicAction: publicActionForCreate(outcome.After.Status)})
}

func (s *Service) PatchAnnouncement(ctx context.Context, actor Actor, id, expectedRevision string, input PatchInput) (*MutationOutcome, error) {
	if err := s.requireAdmin(actor); err != nil {
		return nil, err
	}
	outcome, err := s.store.PatchAnnouncement(ctx, id, actor.SystemAccountID, expectedRevision, input)
	if err != nil || outcome == nil || !outcome.Changed {
		return outcome, err
	}
	return outcome, s.emit(ctx, eventForMutation("update", actor, outcome))
}

func (s *Service) PublishAnnouncement(ctx context.Context, actor Actor, id, expectedRevision string) (*MutationOutcome, error) {
	if err := s.requireAdmin(actor); err != nil {
		return nil, err
	}
	outcome, err := s.store.PublishAnnouncement(ctx, id, actor.SystemAccountID, expectedRevision)
	if err != nil || outcome == nil || !outcome.Changed {
		return outcome, err
	}
	return outcome, s.emit(ctx, eventForMutation("publish", actor, outcome))
}

func (s *Service) UnpublishAnnouncement(ctx context.Context, actor Actor, id, expectedRevision string) (*MutationOutcome, error) {
	if err := s.requireAdmin(actor); err != nil {
		return nil, err
	}
	outcome, err := s.store.UnpublishAnnouncement(ctx, id, actor.SystemAccountID, expectedRevision)
	if err != nil || outcome == nil || !outcome.Changed {
		return outcome, err
	}
	return outcome, s.emit(ctx, eventForMutation("unpublish", actor, outcome))
}

func (s *Service) DeleteAnnouncement(ctx context.Context, actor Actor, id, expectedRevision string) (DeleteResult, error) {
	if err := s.requireAdmin(actor); err != nil {
		return DeleteResult{}, err
	}
	result, err := s.store.DeleteAnnouncement(ctx, id, expectedRevision)
	if err != nil || !result.Deleted {
		return result, err
	}
	return result, s.emit(ctx, AfterCommitEvent{Action: "delete", AnnouncementID: result.ID, Revision: result.Revision, Status: result.Before.Status, ActorSystemAccountID: actor.SystemAccountID, ActorRole: actor.Role, PublicAction: publicActionForDelete(result.Before.Status)})
}

// Short aliases mirror the route-family vocabulary while keeping the longer
// methods self-documenting for dependency injection callers.
func (s *Service) ListPublic(ctx context.Context, actor Actor, limit int) ([]PublicListItem, error) {
	return s.ListPublicAnnouncements(ctx, actor, limit)
}
func (s *Service) GetPublic(ctx context.Context, actor Actor, id string) (PublicDetail, error) {
	return s.FindPublicAnnouncement(ctx, actor, id)
}
func (s *Service) MarkRead(ctx context.Context, actor Actor, ids []string) (ReadResult, error) {
	return s.MarkPublicAnnouncementsRead(ctx, actor, ids)
}
func (s *Service) ListAdmin(ctx context.Context, actor Actor, options ListOptions) (ListResult, error) {
	return s.ListAnnouncements(ctx, actor, options)
}
func (s *Service) GetAdmin(ctx context.Context, actor Actor, id string) (AdminDetail, error) {
	return s.FindAnnouncement(ctx, actor, id)
}
func (s *Service) Create(ctx context.Context, actor Actor, input CreateInput) (MutationOutcome, error) {
	return s.CreateAnnouncement(ctx, actor, input)
}
func (s *Service) Patch(ctx context.Context, actor Actor, id, expectedRevision string, input PatchInput) (*MutationOutcome, error) {
	return s.PatchAnnouncement(ctx, actor, id, expectedRevision, input)
}
func (s *Service) Publish(ctx context.Context, actor Actor, id, expectedRevision string) (*MutationOutcome, error) {
	return s.PublishAnnouncement(ctx, actor, id, expectedRevision)
}
func (s *Service) Unpublish(ctx context.Context, actor Actor, id, expectedRevision string) (*MutationOutcome, error) {
	return s.UnpublishAnnouncement(ctx, actor, id, expectedRevision)
}
func (s *Service) Delete(ctx context.Context, actor Actor, id, expectedRevision string) (DeleteResult, error) {
	return s.DeleteAnnouncement(ctx, actor, id, expectedRevision)
}

func (s *Service) ListPublicAnnouncementsPage(ctx context.Context, actor Actor, limit int) ([]PublicListItem, error) {
	return s.ListPublicAnnouncements(ctx, actor, limit)
}
func (s *Service) FindAnnouncementEditDetail(ctx context.Context, actor Actor, id string) (AdminDetail, error) {
	return s.FindAnnouncement(ctx, actor, id)
}
func (s *Service) CreateAnnouncementForManagement(ctx context.Context, actor Actor, input CreateInput) (MutationOutcome, error) {
	return s.CreateAnnouncement(ctx, actor, input)
}
func (s *Service) PatchAnnouncementForManagement(ctx context.Context, actor Actor, id, expectedRevision string, input PatchInput) (*MutationOutcome, error) {
	return s.PatchAnnouncement(ctx, actor, id, expectedRevision, input)
}
func (s *Service) PublishAnnouncementForManagement(ctx context.Context, actor Actor, id, expectedRevision string) (*MutationOutcome, error) {
	return s.PublishAnnouncement(ctx, actor, id, expectedRevision)
}
func (s *Service) UnpublishAnnouncementForManagement(ctx context.Context, actor Actor, id, expectedRevision string) (*MutationOutcome, error) {
	return s.UnpublishAnnouncement(ctx, actor, id, expectedRevision)
}
func (s *Service) DeleteAnnouncementForManagement(ctx context.Context, actor Actor, id, expectedRevision string) (DeleteResult, error) {
	return s.DeleteAnnouncement(ctx, actor, id, expectedRevision)
}

func (s *Service) emit(ctx context.Context, event AfterCommitEvent) error {
	if s.afterCommit == nil {
		return nil
	}
	if err := s.afterCommit.AfterAnnouncementCommit(ctx, event); err != nil {
		return fmt.Errorf("announcement after-commit effect failed after commit: %w", err)
	}
	return nil
}

// Port is the future handler boundary; it contains no raw database handle.
type Port interface {
	CheckContract(context.Context) error
	ListPublicAnnouncements(context.Context, Actor, int) ([]PublicListItem, error)
	FindPublicAnnouncement(context.Context, Actor, string) (PublicDetail, error)
	MarkPublicAnnouncementsRead(context.Context, Actor, []string) (ReadResult, error)
	ListAnnouncements(context.Context, Actor, ListOptions) (ListResult, error)
	FindAnnouncement(context.Context, Actor, string) (AdminDetail, error)
	CreateAnnouncement(context.Context, Actor, CreateInput) (MutationOutcome, error)
	PatchAnnouncement(context.Context, Actor, string, string, PatchInput) (*MutationOutcome, error)
	PublishAnnouncement(context.Context, Actor, string, string) (*MutationOutcome, error)
	UnpublishAnnouncement(context.Context, Actor, string, string) (*MutationOutcome, error)
	DeleteAnnouncement(context.Context, Actor, string, string) (DeleteResult, error)
	ListPublic(context.Context, Actor, int) ([]PublicListItem, error)
	GetPublic(context.Context, Actor, string) (PublicDetail, error)
	MarkRead(context.Context, Actor, []string) (ReadResult, error)
	ListAdmin(context.Context, Actor, ListOptions) (ListResult, error)
	GetAdmin(context.Context, Actor, string) (AdminDetail, error)
	Create(context.Context, Actor, CreateInput) (MutationOutcome, error)
	Patch(context.Context, Actor, string, string, PatchInput) (*MutationOutcome, error)
	Publish(context.Context, Actor, string, string) (*MutationOutcome, error)
	Unpublish(context.Context, Actor, string, string) (*MutationOutcome, error)
	Delete(context.Context, Actor, string, string) (DeleteResult, error)
}

var _ Port = (*Service)(nil)

// CoveredManifestOperations is evidence only; this package never changes a
// capability manifest or claims that the Node owner has been cut over.
var CoveredManifestOperations = []string{
	"list_public_announcements",
	"find_public_announcement",
	"mark_public_announcements_read",
	"list_announcements",
	"find_announcement",
	"create_announcement",
	"patch_announcement",
	"publish_announcement",
	"unpublish_announcement",
	"delete_announcement",
}

func (s *Store) PublicList(ctx context.Context, systemAccountID string, limit int) ([]PublicListItem, error) {
	return s.ListPublicAnnouncements(ctx, systemAccountID, limit)
}
func (s *Store) PublicDetail(ctx context.Context, id string) (PublicDetail, error) {
	return s.FindPublicAnnouncement(ctx, id)
}
func (s *Store) PublicRead(ctx context.Context, systemAccountID string, ids []string) (ReadResult, error) {
	return s.MarkPublicAnnouncementsRead(ctx, systemAccountID, ids)
}
func (s *Store) ListAdmin(ctx context.Context, options ListOptions) (ListResult, error) {
	return s.ListAnnouncements(ctx, options)
}
func (s *Store) GetAdmin(ctx context.Context, id string) (AdminDetail, error) {
	return s.FindAnnouncementDetail(ctx, id)
}
func (s *Store) FindAnnouncementEditDetail(ctx context.Context, id string) (AdminDetail, error) {
	return s.FindAnnouncementDetail(ctx, id)
}
func (s *Store) Create(ctx context.Context, input CreateInput, actorSystemAccountID string) (MutationOutcome, error) {
	return s.CreateAnnouncement(ctx, input, actorSystemAccountID)
}
func (s *Store) Patch(ctx context.Context, id, actorSystemAccountID, expectedRevision string, input PatchInput) (*MutationOutcome, error) {
	return s.PatchAnnouncement(ctx, id, actorSystemAccountID, expectedRevision, input)
}
func (s *Store) Publish(ctx context.Context, id, actorSystemAccountID, expectedRevision string) (*MutationOutcome, error) {
	return s.PublishAnnouncement(ctx, id, actorSystemAccountID, expectedRevision)
}
func (s *Store) Unpublish(ctx context.Context, id, actorSystemAccountID, expectedRevision string) (*MutationOutcome, error) {
	return s.UnpublishAnnouncement(ctx, id, actorSystemAccountID, expectedRevision)
}
func (s *Store) Delete(ctx context.Context, id, expectedRevision string) (DeleteResult, error) {
	return s.DeleteAnnouncement(ctx, id, expectedRevision)
}

func normalizePublicLimit(limit int) (int, error) {
	if limit == 0 {
		return PublicLimit, nil
	}
	if limit < 0 || limit > PublicLimit {
		return 0, fmt.Errorf("%w: public announcement limit must be between 1 and %d", ErrInvalidInput, PublicLimit)
	}
	return limit, nil
}

func normalizeListOptions(options ListOptions) (int, int, error) {
	pageSize := options.PageSize
	if pageSize < 0 {
		return 0, 0, fmt.Errorf("%w: announcement page size must be positive", ErrInvalidInput)
	}
	if pageSize <= 0 {
		pageSize = DefaultPageSize
	}
	if pageSize > MaxPageSize {
		return 0, 0, fmt.Errorf("%w: announcement page size must be between 1 and %d", ErrInvalidInput, MaxPageSize)
	}
	page := options.Page
	if page < 0 {
		return 0, 0, fmt.Errorf("%w: announcement page must be positive", ErrInvalidInput)
	}
	if page <= 0 {
		page = 1
	}
	maxPage := (AdminWindowRows - 1) / pageSize
	if maxPage < 1 {
		maxPage = 1
	}
	if page > maxPage {
		page = maxPage
	}
	return page, pageSize, nil
}

func normalizeIDs(ids []string) ([]string, error) {
	if len(ids) > PublicLimit {
		return nil, fmt.Errorf("%w: announcement read list cannot exceed %d ids", ErrInvalidInput, PublicLimit)
	}
	seen := make(map[string]struct{}, len(ids))
	result := make([]string, 0, min(len(ids), PublicLimit))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			return nil, fmt.Errorf("%w: announcement read id cannot be empty", ErrInvalidInput)
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result, nil
}

func normalizeCreate(input CreateInput) (string, string, Level, Status, error) {
	title, err := normalizeText(input.Title, "title", TitleMaxUTF16)
	if err != nil {
		return "", "", "", "", err
	}
	content, err := normalizeText(input.Content, "content", ContentMaxUTF16)
	if err != nil {
		return "", "", "", "", err
	}
	level := input.Level
	if level == "" {
		level = LevelInfo
	}
	level, err = normalizeLevel(level)
	if err != nil {
		return "", "", "", "", err
	}
	status := input.Status
	if status == "" {
		status = StatusDraft
	}
	status, err = normalizeStatus(status)
	if err != nil {
		return "", "", "", "", err
	}
	return title, content, level, status, nil
}

func validatePatch(input PatchInput) error {
	if input.Title == nil && input.Content == nil && input.Level == nil && input.Status == nil {
		return fmt.Errorf("%w: at least one announcement change is required", ErrInvalidInput)
	}
	return nil
}

func normalizeText(value, field string, max int) (string, error) {
	if !utf8.ValidString(value) {
		return "", fmt.Errorf("%w: announcement %s must be valid UTF-8", ErrInvalidInput, field)
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%w: announcement %s cannot be empty", ErrInvalidInput, field)
	}
	if len(utf16.Encode([]rune(value))) > max {
		return "", fmt.Errorf("%w: announcement %s exceeds %d UTF-16 units", ErrInvalidInput, field, max)
	}
	return value, nil
}

func normalizeLevel(value Level) (Level, error) {
	switch value {
	case LevelCritical, LevelWarning, LevelInfo, LevelNormal:
		return value, nil
	default:
		return "", fmt.Errorf("%w: invalid announcement level", ErrInvalidInput)
	}
}

func normalizeStatus(value Status) (Status, error) {
	switch value {
	case StatusDraft, StatusPublished, StatusArchived:
		return value, nil
	default:
		return "", fmt.Errorf("%w: invalid announcement status", ErrInvalidInput)
	}
}

func scanMutationRow(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, query string, id string) (*MutationState, error) {
	var state MutationState
	var published sql.NullString
	err := queryer.QueryRowContext(ctx, query, id).Scan(&state.ID, &state.Title, &state.Content, &state.Level, &state.Status, &published, &state.Revision)
	if published.Valid {
		state.PublishedAt = &published.String
	}
	if err != nil {
		return nil, err
	}
	return &state, nil
}

func (s *Store) nextRevisionForTest(current string) (string, error) {
	return s.nextRevision(current)
}

func eventForMutation(action string, actor Actor, outcome *MutationOutcome) AfterCommitEvent {
	event := AfterCommitEvent{Action: action, AnnouncementID: outcome.Receipt.ID, Revision: outcome.Receipt.Revision, Status: outcome.After.Status, ActorSystemAccountID: actor.SystemAccountID, ActorRole: actor.Role}
	if outcome.Before != nil && outcome.Before.Status == StatusPublished && outcome.After.Status != StatusPublished {
		event.PublicAction = "delete"
	} else if outcome.After.Status == StatusPublished {
		event.PublicAction = "upsert"
	}
	return event
}

func publicActionForCreate(status Status) string {
	if status == StatusPublished {
		return "upsert"
	}
	return ""
}

func publicActionForDelete(status Status) string {
	if status == StatusPublished {
		return "delete"
	}
	return ""
}

func stringPtr(value string) *string { return &value }
func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// DecodeCreateInput and DecodePatchRequest are strict transport helpers. They
// reject unknown fields and trailing JSON while retaining Node's last-value
// behavior for duplicate JSON keys via encoding/json's normal decoder rules.
func DecodeCreateInput(reader io.Reader) (CreateInput, error) {
	var input CreateInput
	raw, err := decodeObject(reader)
	if err != nil {
		return CreateInput{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	fields := objectFields(raw)
	if value, ok := fields["level"]; ok && stringValueEmpty(value) {
		return CreateInput{}, fmt.Errorf("%w: announcement level cannot be empty", ErrInvalidInput)
	}
	if value, ok := fields["status"]; ok && stringValueEmpty(value) {
		return CreateInput{}, fmt.Errorf("%w: announcement status cannot be empty", ErrInvalidInput)
	}
	if err := decodeStrictObject(raw, &input); err != nil {
		return CreateInput{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	input.Title, err = normalizeText(input.Title, "title", TitleMaxUTF16)
	if err != nil {
		return CreateInput{}, err
	}
	input.Content, err = normalizeText(input.Content, "content", ContentMaxUTF16)
	if err != nil {
		return CreateInput{}, err
	}
	if input.Level != "" {
		input.Level, err = normalizeLevel(input.Level)
		if err != nil {
			return CreateInput{}, err
		}
	}
	if input.Status != "" {
		input.Status, err = normalizeStatus(input.Status)
		if err != nil {
			return CreateInput{}, err
		}
	}
	return input, nil
}

func DecodePatchRequest(reader io.Reader) (PatchRequest, error) {
	var input PatchRequest
	raw, err := decodeObject(reader)
	if err != nil {
		return PatchRequest{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	if err := decodeStrictObject(raw, &input); err != nil {
		return PatchRequest{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	input.ExpectedRevision = strings.TrimSpace(input.ExpectedRevision)
	if input.ExpectedRevision == "" {
		return PatchRequest{}, fmt.Errorf("%w: expectedRevision cannot be empty", ErrInvalidInput)
	}
	if input.Title != nil {
		value, err := normalizeText(*input.Title, "title", TitleMaxUTF16)
		if err != nil {
			return PatchRequest{}, err
		}
		input.Title = &value
	}
	if input.Content != nil {
		value, err := normalizeText(*input.Content, "content", ContentMaxUTF16)
		if err != nil {
			return PatchRequest{}, err
		}
		input.Content = &value
	}
	if input.Level != nil {
		value, err := normalizeLevel(*input.Level)
		if err != nil {
			return PatchRequest{}, err
		}
		input.Level = &value
	}
	if input.Status != nil {
		value, err := normalizeStatus(*input.Status)
		if err != nil {
			return PatchRequest{}, err
		}
		input.Status = &value
	}
	if input.Title == nil && input.Content == nil && input.Level == nil && input.Status == nil {
		return PatchRequest{}, fmt.Errorf("%w: at least one announcement change is required", ErrInvalidInput)
	}
	return input, nil
}

func decodeObject(reader io.Reader) (json.RawMessage, error) {
	var raw json.RawMessage
	decoder := json.NewDecoder(reader)
	if err := decoder.Decode(&raw); err != nil {
		return nil, err
	}
	if fields := objectFields(raw); fields == nil {
		return nil, errors.New("JSON object is required")
	}
	fields := objectFields(raw)
	for name, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, fmt.Errorf("field %s cannot be null", name)
		}
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New("multiple JSON values are not allowed")
		}
		return nil, err
	}
	return raw, nil
}

func objectFields(raw json.RawMessage) map[string]json.RawMessage {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil
	}
	return fields
}

func decodeStrictObject(raw json.RawMessage, target any) error {
	strict := json.NewDecoder(bytes.NewReader(raw))
	strict.DisallowUnknownFields()
	if err := strict.Decode(target); err != nil {
		return err
	}
	return nil
}

func stringValueEmpty(value json.RawMessage) bool {
	var text string
	return json.Unmarshal(value, &text) == nil && text == ""
}

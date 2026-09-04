package openaicompat

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Store is the dual-mode persistence for the openai-compatible file and
// vector-store repositories (storage/openai-compatible-files.repository.ts +
// storage/openai-compatible-vector-stores.repository.ts). SQLite mode keeps
// bare table names and '?' placeholders; PostgreSQL mode qualifies tables
// with the juhe_business schema and rewrites '?' to '$n', mirroring the Node
// database-client dialect helpers.
type Store struct {
	db    *sql.DB
	pg    bool
	now   func() time.Time
	newID func(kind string) string
}

// StoreOption customizes the store (clock and id generators are injectable
// so tests stay deterministic).
type StoreOption func(*Store)

// WithNow overrides the wall clock (Node nowIso()).
func WithNow(now func() time.Time) StoreOption {
	return func(s *Store) { s.now = now }
}

// WithIDGenerator overrides id generation. kind is "file", "vector_store" or
// "chunk".
func WithIDGenerator(newID func(kind string) string) StoreOption {
	return func(s *Store) { s.newID = newID }
}

// NewStore builds the dual-mode store.
func NewStore(db *sql.DB, postgres bool, options ...StoreOption) (*Store, error) {
	if db == nil {
		return nil, errors.New("openaicompat store requires a database")
	}
	store := &Store{db: db, pg: postgres, now: time.Now}
	for _, option := range options {
		option(store)
	}
	return store, nil
}

func (s *Store) table(name string) string {
	if s.pg {
		return "juhe_business." + name
	}
	return name
}

// bind mirrors the Node pg dialect placeholder rewrite ('?' -> '$n').
func (s *Store) bind(query string) string {
	if !s.pg {
		return query
	}
	var out strings.Builder
	index := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			out.WriteString("$" + strconv.Itoa(index))
			index++
		} else {
			out.WriteByte(query[i])
		}
	}
	return out.String()
}

func (s *Store) nowISO() string { return isoMillis(s.now()) }

func ensureCtx(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// generateID routes to the injected or default generator.
func (s *Store) generateID(kind string) string {
	if s.newID != nil {
		return s.newID(kind)
	}
	switch kind {
	case "file":
		return newOpenAICompatibleFileID(s.now())
	case "vector_store":
		return newOpenAICompatibleVectorStoreID(s.now())
	default:
		return newVectorStoreChunkID()
	}
}

// coerceInt64 mirrors Number(row.value) || 0 for driver-specific numeric
// representations.
func coerceInt64(value any) int64 {
	switch typed := value.(type) {
	case int64:
		return typed
	case int:
		return int64(typed)
	case float64:
		return int64(typed)
	case []byte:
		return parseInt64(string(typed))
	case string:
		return parseInt64(typed)
	default:
		return 0
	}
}

func parseInt64(text string) int64 {
	value, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
	if err != nil {
		return 0
	}
	return value
}

func nullString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func fromNullString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	copied := value.String
	return &copied
}

func fromNullInt64(value sql.NullInt64) *int {
	if !value.Valid {
		return nil
	}
	number := int(value.Int64)
	return &number
}

// parseJSONObject mirrors parseJsonObject: invalid or non-object JSON
// collapses to {}.
func parseJSONObject(text string) map[string]any {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return map[string]any{}
	}
	var parsed any
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return map[string]any{}
	}
	if record, ok := parsed.(map[string]any); ok {
		return record
	}
	return map[string]any{}
}

// trimmedPointer mirrors queryString/stringValue: trimmed non-empty strings
// only.
func trimmedPointer(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func listOrder(order string) string {
	if order == "asc" {
		return "asc"
	}
	return "desc"
}

// cursorClause mirrors the (created_at, tiebreaker) keyset predicate for a
// single cursor bound in the list direction.
func cursorClause(order, timeColumn, idColumn string, after bool) string {
	comparisonTime, comparisonID := "<", "<"
	if order == "asc" {
		comparisonTime, comparisonID = ">", ">"
	}
	if !after { // a "before" cursor walks against the list order
		comparisonTime, comparisonID = invert(comparisonTime), invert(comparisonID)
	}
	return "(" + timeColumn + " " + comparisonTime + " ? OR (" + timeColumn + " = ? AND " + idColumn + " " + comparisonID + " ?))"
}

func invert(operator string) string {
	if operator == "<" {
		return ">"
	}
	return "<"
}

const (
	maxListLimit       = 100
	defaultListLimit   = 20
	maxSearchLimit     = 50
	defaultSearchLimit = 10
)

func normalizeListLimit(limit *int) int {
	return clampLimit(limit, defaultListLimit, maxListLimit)
}

func normalizeSearchLimit(limit *int) int {
	return clampLimit(limit, defaultSearchLimit, maxSearchLimit)
}

func clampLimit(limit *int, fallback, maximum int) int {
	if limit == nil {
		return fallback
	}
	value := *limit
	if value < 1 {
		value = 1
	}
	if value > maximum {
		value = maximum
	}
	return value
}

// ---------------------------------------------------------------------------
// OpenAI compatible files repository
// ---------------------------------------------------------------------------

// FileRecord mirrors OpenAICompatibleFileRecord.
type FileRecord struct {
	ID              string
	SystemAccountID string
	APIKeyID        string
	Purpose         string
	ContainerID     *string
	Filename        string
	Bytes           int64
	MediaType       *string
	StorageKey      string
	SHA256          string
	Status          string // processed | deleted
	CreatedAt       string
	UpdatedAt       string
	ExpiresAt       *string
	DeletedAt       *string
}

// FileCreateInput mirrors OpenAICompatibleFileCreateInput.
type FileCreateInput struct {
	ID              string
	SystemAccountID string
	APIKeyID        string
	Purpose         string
	ContainerID     *string
	Filename        string
	Bytes           int64
	MediaType       *string
	StorageKey      string
	SHA256          string
	ExpiresAt       *string
}

// FileListOptions mirrors OpenAICompatibleFileListOptions.
type FileListOptions struct {
	SystemAccountID string
	APIKeyID        string
	Purpose         *string
	ContainerID     *string
	Limit           *int
	Order           string // asc | desc (anything else -> desc)
	After           string
}

// FileListResult mirrors OpenAICompatibleFileListResult.
type FileListResult struct {
	Items   []FileRecord
	HasMore bool
}

const fileRowColumns = "id, system_account_id, api_key_id, purpose, container_id, filename, bytes, media_type, " +
	"storage_key, sha256, status, created_at, updated_at, expires_at, deleted_at"

func scanFileRecord(scan func(...any) error) (FileRecord, error) {
	var record FileRecord
	var containerID, mediaType, expiresAt, deletedAt sql.NullString
	var bytesAny any
	err := scan(&record.ID, &record.SystemAccountID, &record.APIKeyID, &record.Purpose, &containerID,
		&record.Filename, &bytesAny, &mediaType, &record.StorageKey, &record.SHA256, &record.Status,
		&record.CreatedAt, &record.UpdatedAt, &expiresAt, &deletedAt)
	if err != nil {
		return FileRecord{}, err
	}
	record.ContainerID = fromNullString(containerID)
	record.MediaType = fromNullString(mediaType)
	record.Bytes = coerceInt64(bytesAny)
	record.ExpiresAt = fromNullString(expiresAt)
	record.DeletedAt = fromNullString(deletedAt)
	if record.Status != "deleted" {
		record.Status = "processed"
	}
	return record, nil
}

// CreateFile mirrors createOpenAICompatibleFile(Async).
func (s *Store) CreateFile(ctx context.Context, input FileCreateInput) (FileRecord, error) {
	ctx = ensureCtx(ctx)
	now := s.nowISO()
	_, err := s.db.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("openai_compatible_files")+` (
		id, system_account_id, api_key_id, purpose, container_id, filename, bytes, media_type,
		storage_key, sha256, status, created_at, updated_at, expires_at, deleted_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'processed', ?, ?, ?, NULL)`),
		input.ID, input.SystemAccountID, input.APIKeyID, input.Purpose, nullString(input.ContainerID),
		input.Filename, input.Bytes, nullString(input.MediaType), input.StorageKey, input.SHA256,
		now, now, nullString(input.ExpiresAt))
	if err != nil {
		return FileRecord{}, err
	}
	record, err := s.FindFile(ctx, input.ID, input.SystemAccountID, input.APIKeyID)
	if err != nil {
		return FileRecord{}, err
	}
	if record == nil {
		return FileRecord{}, errors.New("OpenAI compatible file " + input.ID + " was not readable after insert")
	}
	return *record, nil
}

// ListFiles mirrors listOpenAICompatibleFiles(Async).
func (s *Store) ListFiles(ctx context.Context, options FileListOptions) (FileListResult, error) {
	ctx = ensureCtx(ctx)
	limit := normalizeListLimit(options.Limit)
	order := listOrder(options.Order)
	params := []any{options.SystemAccountID, options.APIKeyID}
	clauses := []string{"system_account_id = ?", "api_key_id = ?", "deleted_at IS NULL"}
	if purpose := trimmedPointer(options.Purpose); purpose != nil {
		clauses = append(clauses, "purpose = ?")
		params = append(params, *purpose)
	}
	if containerID := trimmedPointer(options.ContainerID); containerID != nil {
		clauses = append(clauses, "container_id = ?")
		params = append(params, *containerID)
	}
	if options.After != "" {
		cursor, err := s.findFileCursor(ctx, options.After, options.SystemAccountID, options.APIKeyID)
		if err != nil {
			return FileListResult{}, err
		}
		if cursor == nil {
			return FileListResult{Items: []FileRecord{}}, nil
		}
		clauses = append(clauses, cursorClause(order, "created_at", "id", true))
		params = append(params, cursor.createdAt, cursor.createdAt, cursor.id)
	}
	args := append(params, limit+1)
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT `+fileRowColumns+`
		FROM `+s.table("openai_compatible_files")+`
		WHERE `+strings.Join(clauses, " AND ")+`
		ORDER BY created_at `+strings.ToUpper(order)+`, id `+strings.ToUpper(order)+`
		LIMIT ?`), args...)
	if err != nil {
		return FileListResult{}, err
	}
	defer rows.Close()
	records := []FileRecord{}
	for rows.Next() {
		record, scanErr := scanFileRecord(rows.Scan)
		if scanErr != nil {
			return FileListResult{}, scanErr
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return FileListResult{}, err
	}
	hasMore := len(records) > limit
	if hasMore {
		records = records[:limit]
	}
	return FileListResult{Items: records, HasMore: hasMore}, nil
}

// fileCursor mirrors findOpenAICompatibleFileCursor: the cursor lookup does
// not apply purpose/container filters.
func (s *Store) findFileCursor(ctx context.Context, fileID, systemAccountID, apiKeyID string) (*fileCursorRow, error) {
	var row fileCursorRow
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT id, created_at
		FROM `+s.table("openai_compatible_files")+`
		WHERE id = ? AND system_account_id = ? AND api_key_id = ? AND deleted_at IS NULL
		LIMIT 1`), fileID, systemAccountID, apiKeyID).Scan(&row.id, &row.createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

type fileCursorRow struct {
	id        string
	createdAt string
}

// FindFile mirrors findOpenAICompatibleFile(Async): nil when missing,
// deleted, or owned by another scope (越权 lookups collapse to 404).
func (s *Store) FindFile(ctx context.Context, fileID, systemAccountID, apiKeyID string) (*FileRecord, error) {
	ctx = ensureCtx(ctx)
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT `+fileRowColumns+`
		FROM `+s.table("openai_compatible_files")+`
		WHERE id = ?
			AND system_account_id = ?
			AND api_key_id = ?
			AND deleted_at IS NULL
		LIMIT 1`), fileID, systemAccountID, apiKeyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	record, err := scanFileRecord(rows.Scan)
	if err != nil {
		return nil, err
	}
	return &record, nil
}

// DeleteFile mirrors deleteOpenAICompatibleFile(Async): soft delete scoped to
// owner + key.
func (s *Store) DeleteFile(ctx context.Context, fileID, systemAccountID, apiKeyID string) (*FileRecord, error) {
	ctx = ensureCtx(ctx)
	existing, err := s.FindFile(ctx, fileID, systemAccountID, apiKeyID)
	if err != nil || existing == nil {
		return nil, err
	}
	now := s.nowISO()
	_, err = s.db.ExecContext(ctx, s.bind(`UPDATE `+s.table("openai_compatible_files")+`
		SET status = 'deleted',
		    deleted_at = ?,
		    updated_at = ?
		WHERE id = ? AND system_account_id = ? AND api_key_id = ? AND deleted_at IS NULL`),
		now, now, fileID, systemAccountID, apiKeyID)
	if err != nil {
		return nil, err
	}
	deleted := *existing
	deleted.Status = "deleted"
	deleted.DeletedAt = &now
	deleted.UpdatedAt = now
	return &deleted, nil
}

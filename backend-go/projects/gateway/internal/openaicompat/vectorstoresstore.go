package openaicompat

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"unicode"
)

// ---------------------------------------------------------------------------
// OpenAI compatible vector stores repository
// (storage/openai-compatible-vector-stores.repository.ts)
// ---------------------------------------------------------------------------

// VectorStoreStatus / VectorStoreFileStatus mirror the Node unions.
const (
	VectorStoreStatusActive  = "active"
	VectorStoreStatusDeleted = "deleted"

	VectorStoreFileStatusInProgress = "in_progress"
	VectorStoreFileStatusCompleted  = "completed"
	VectorStoreFileStatusFailed     = "failed"
	VectorStoreFileStatusCancelled  = "cancelled"
)

// FileCounts mirrors OpenAICompatibleVectorStoreFileCounts.
type FileCounts struct {
	InProgress int
	Completed  int
	Failed     int
	Cancelled  int
	Total      int
}

// VectorStoreRecord mirrors OpenAICompatibleVectorStoreRecord.
type VectorStoreRecord struct {
	ID                 string
	SystemAccountID    string
	APIKeyID           string
	Name               *string
	Description        *string
	Metadata           map[string]any
	Bytes              int64
	Status             string
	CreatedAt          string
	UpdatedAt          string
	ExpiresAfterAnchor *string
	ExpiresAfterDays   *int
	ExpiresAt          *string
	DeletedAt          *string
	FileCounts         FileCounts
}

// VectorStoreCreateInput mirrors OpenAICompatibleVectorStoreCreateInput.
type VectorStoreCreateInput struct {
	SystemAccountID    string
	APIKeyID           string
	Name               *string
	Description        *string
	Metadata           map[string]any
	ExpiresAfterAnchor *string
	ExpiresAfterDays   *int
	ExpiresAt          *string
}

// VectorStoreListOptions mirrors OpenAICompatibleVectorStoreListOptions.
type VectorStoreListOptions struct {
	SystemAccountID string
	APIKeyID        string
	Limit           *int
	Order           string
	After           string
	Before          string
}

// ListResult is the shared {items, hasMore} shape.
type ListResult[T any] struct {
	Items   []T
	HasMore bool
}

// VectorStoreFileRecord mirrors OpenAICompatibleVectorStoreFileRecord.
type VectorStoreFileRecord struct {
	VectorStoreID    string
	FileID           string
	SystemAccountID  string
	APIKeyID         string
	Attributes       map[string]any
	ChunkingStrategy map[string]any
	Status           string
	UsageBytes       int64
	LastError        map[string]any
	HasLastError     bool
	CreatedAt        string
	UpdatedAt        string
	DeletedAt        *string
	File             *FileRecord
}

// VectorStoreFileCreateInput mirrors OpenAICompatibleVectorStoreFileCreateInput.
type VectorStoreFileCreateInput struct {
	VectorStoreID    string
	FileID           string
	SystemAccountID  string
	APIKeyID         string
	Attributes       map[string]any
	ChunkingStrategy map[string]any
	Status           string
	UsageBytes       *int64
	LastError        map[string]any
	Chunks           []ChunkInput
}

// VectorStoreFileListOptions mirrors OpenAICompatibleVectorStoreFileListOptions.
type VectorStoreFileListOptions struct {
	VectorStoreID   string
	SystemAccountID string
	APIKeyID        string
	Limit           *int
	Order           string
	After           string
}

// ChunkInput mirrors OpenAICompatibleVectorStoreChunkInput.
type ChunkInput struct {
	ContentText      string
	ContentPreview   string
	TokenEstimate    int
	KeywordIndexText string
}

// SearchOptions mirrors OpenAICompatibleVectorStoreSearchOptions.
type SearchOptions struct {
	VectorStoreID   string
	SystemAccountID string
	APIKeyID        string
	Query           string
	MaxNumResults   *int
	Filters         map[string]any
	ScoreThreshold  *float64
}

// SearchResult mirrors OpenAICompatibleVectorStoreSearchResult.
type SearchResult struct {
	ChunkID        string
	VectorStoreID  string
	FileID         string
	Filename       string
	Attributes     map[string]any
	ChunkIndex     int
	Score          float64
	ContentText    string
	ContentPreview string
}

// FileChunkRecord mirrors OpenAICompatibleVectorStoreFileChunkRecord.
type FileChunkRecord struct {
	ChunkID        string
	VectorStoreID  string
	FileID         string
	Filename       string
	ChunkIndex     int
	ContentText    string
	ContentPreview string
}

const (
	vectorStoreSelectColumns = "id, system_account_id, api_key_id, name, description, metadata_json, bytes, " +
		"status, created_at, updated_at, expires_after_anchor, expires_after_days, expires_at, deleted_at"

	vectorStoreFileSelectColumns = "vector_store_id, file_id, system_account_id, api_key_id, attributes_json, " +
		"chunking_strategy_json, status, usage_bytes, last_error_json, created_at, updated_at, deleted_at"
)

func scanVectorStore(scan func(...any) error) (VectorStoreRecord, error) {
	var record VectorStoreRecord
	var name, description, anchor, expiresAt, deletedAt sql.NullString
	var days sql.NullInt64
	var bytesAny, metadataJSON any
	err := scan(&record.ID, &record.SystemAccountID, &record.APIKeyID, &name, &description, &metadataJSON,
		&bytesAny, &record.Status, &record.CreatedAt, &record.UpdatedAt, &anchor, &days, &expiresAt, &deletedAt)
	if err != nil {
		return VectorStoreRecord{}, err
	}
	record.Name = fromNullString(name)
	record.Description = fromNullString(description)
	record.Metadata = parseJSONObject(coerceString(metadataJSON))
	record.Bytes = coerceInt64(bytesAny)
	record.Status = vectorStoreStatus(record.Status)
	record.ExpiresAfterAnchor = fromNullString(anchor)
	record.ExpiresAfterDays = fromNullInt64(days)
	record.ExpiresAt = fromNullString(expiresAt)
	record.DeletedAt = fromNullString(deletedAt)
	return record, nil
}

func vectorStoreStatus(value string) string {
	if value == VectorStoreStatusDeleted {
		return VectorStoreStatusDeleted
	}
	return VectorStoreStatusActive
}

func coerceString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	case nil:
		return ""
	default:
		return ""
	}
}

// CreateVectorStore mirrors createOpenAICompatibleVectorStore(Async).
func (s *Store) CreateVectorStore(ctx context.Context, id string, input VectorStoreCreateInput) (VectorStoreRecord, error) {
	ctx = ensureCtx(ctx)
	now := s.nowISO()
	metadata := input.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadataJSON, err := marshalJSON(metadata)
	if err != nil {
		return VectorStoreRecord{}, err
	}
	_, err = s.db.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("openai_compatible_vector_stores")+` (
		id, system_account_id, api_key_id, name, description, metadata_json, bytes,
		status, created_at, updated_at, expires_after_anchor, expires_after_days, expires_at, deleted_at
	) VALUES (?, ?, ?, ?, ?, ?, 0, 'active', ?, ?, ?, ?, ?, NULL)`),
		id, input.SystemAccountID, input.APIKeyID, nullString(input.Name), nullString(input.Description),
		string(metadataJSON), now, now, nullString(input.ExpiresAfterAnchor), nullInt64Pointer(input.ExpiresAfterDays),
		nullString(input.ExpiresAt))
	if err != nil {
		return VectorStoreRecord{}, err
	}
	record, err := s.FindVectorStore(ctx, id, input.SystemAccountID, input.APIKeyID)
	if err != nil {
		return VectorStoreRecord{}, err
	}
	if record == nil {
		return VectorStoreRecord{}, errVectorStoreNotReadable(id)
	}
	return *record, nil
}

func nullInt64Pointer(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func errVectorStoreNotReadable(id string) error {
	return &VectorStoreNotReadableError{ID: id}
}

// VectorStoreNotReadableError mirrors the "was not readable after insert"
// invariant failure.
type VectorStoreNotReadableError struct{ ID string }

func (e *VectorStoreNotReadableError) Error() string {
	return "OpenAI compatible vector store " + e.ID + " was not readable after insert"
}

func marshalJSON(value any) ([]byte, error) {
	return json.Marshal(value)
}

// ListVectorStores mirrors listOpenAICompatibleVectorStores(Async) with the
// after/before keyset cursors.
func (s *Store) ListVectorStores(ctx context.Context, options VectorStoreListOptions) (ListResult[VectorStoreRecord], error) {
	ctx = ensureCtx(ctx)
	limit := normalizeListLimit(options.Limit)
	order := listOrder(options.Order)
	params := []any{options.SystemAccountID, options.APIKeyID}
	clauses := []string{"system_account_id = ?", "api_key_id = ?", "deleted_at IS NULL"}
	if options.After != "" {
		cursor, err := s.findVectorStoreCursor(ctx, options.After, options.SystemAccountID, options.APIKeyID)
		if err != nil {
			return ListResult[VectorStoreRecord]{}, err
		}
		if cursor == nil {
			return ListResult[VectorStoreRecord]{Items: []VectorStoreRecord{}}, nil
		}
		clauses = append(clauses, cursorClause(order, "created_at", "id", true))
		params = append(params, cursor.createdAt, cursor.createdAt, cursor.id)
	}
	if options.Before != "" {
		cursor, err := s.findVectorStoreCursor(ctx, options.Before, options.SystemAccountID, options.APIKeyID)
		if err != nil {
			return ListResult[VectorStoreRecord]{}, err
		}
		if cursor == nil {
			return ListResult[VectorStoreRecord]{Items: []VectorStoreRecord{}}, nil
		}
		clauses = append(clauses, cursorClause(order, "created_at", "id", false))
		params = append(params, cursor.createdAt, cursor.createdAt, cursor.id)
	}
	args := append(params, limit+1)
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT `+vectorStoreSelectColumns+`
		FROM `+s.table("openai_compatible_vector_stores")+`
		WHERE `+strings.Join(clauses, " AND ")+`
		ORDER BY created_at `+strings.ToUpper(order)+`, id `+strings.ToUpper(order)+`
		LIMIT ?`), args...)
	if err != nil {
		return ListResult[VectorStoreRecord]{}, err
	}
	records := []VectorStoreRecord{}
	var pending []VectorStoreRecord
	for rows.Next() {
		record, scanErr := scanVectorStore(rows.Scan)
		if scanErr != nil {
			rows.Close()
			return ListResult[VectorStoreRecord]{}, scanErr
		}
		pending = append(pending, record)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return ListResult[VectorStoreRecord]{}, err
	}
	// Close before the nested per-row counts query: the SQLite test/runtime
	// pool runs with MaxOpenConns(1) (mirror of the groups queryer note).
	rows.Close()
	hasMore := len(pending) > limit
	if hasMore {
		pending = pending[:limit]
	}
	for _, record := range pending {
		counts, countErr := s.vectorStoreFileCounts(ctx, record.ID, record.SystemAccountID, record.APIKeyID)
		if countErr != nil {
			return ListResult[VectorStoreRecord]{}, countErr
		}
		record.FileCounts = counts
		records = append(records, record)
	}
	if records == nil {
		records = []VectorStoreRecord{}
	}
	return ListResult[VectorStoreRecord]{Items: records, HasMore: hasMore}, nil
}

// FindVectorStore mirrors findOpenAICompatibleVectorStore(Async) including
// the live file_counts projection.
func (s *Store) FindVectorStore(ctx context.Context, vectorStoreID, systemAccountID, apiKeyID string) (*VectorStoreRecord, error) {
	ctx = ensureCtx(ctx)
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT `+vectorStoreSelectColumns+`
		FROM `+s.table("openai_compatible_vector_stores")+`
		WHERE id = ?
			AND system_account_id = ?
			AND api_key_id = ?
			AND deleted_at IS NULL
		LIMIT 1`), vectorStoreID, systemAccountID, apiKeyID)
	if err != nil {
		return nil, err
	}
	if !rows.Next() {
		closeErr := rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		return nil, nil
	}
	record, err := scanVectorStore(rows.Scan)
	rows.Close()
	if err != nil {
		return nil, err
	}
	// rows are closed before the nested counts query (MaxOpenConns(1) pools).
	counts, err := s.vectorStoreFileCounts(ctx, record.ID, record.SystemAccountID, record.APIKeyID)
	if err != nil {
		return nil, err
	}
	record.FileCounts = counts
	return &record, nil
}

func (s *Store) findVectorStoreFileCursor(ctx context.Context, fileID, vectorStoreID, systemAccountID, apiKeyID string) (*vectorStoreFileCursorRow, error) {
	var row vectorStoreFileCursorRow
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT file_id, created_at
		FROM `+s.table("openai_compatible_vector_store_files")+`
		WHERE file_id = ?
			AND vector_store_id = ?
			AND system_account_id = ?
			AND api_key_id = ?
			AND deleted_at IS NULL
		LIMIT 1`), fileID, vectorStoreID, systemAccountID, apiKeyID).Scan(&row.fileID, &row.createdAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}

type vectorStoreFileCursorRow struct {
	fileID    string
	createdAt string
}

func (s *Store) findVectorStoreCursor(ctx context.Context, vectorStoreID, systemAccountID, apiKeyID string) (*vectorStoreCursorRow, error) {
	var row vectorStoreCursorRow
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT id, created_at
		FROM `+s.table("openai_compatible_vector_stores")+`
		WHERE id = ? AND system_account_id = ? AND api_key_id = ? AND deleted_at IS NULL
		LIMIT 1`), vectorStoreID, systemAccountID, apiKeyID).Scan(&row.id, &row.createdAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}

type vectorStoreCursorRow struct {
	id        string
	createdAt string
}

// DeleteVectorStore mirrors deleteOpenAICompatibleVectorStore(Async): mark
// store + files deleted and drop the chunks inside one transaction.
func (s *Store) DeleteVectorStore(ctx context.Context, vectorStoreID, systemAccountID, apiKeyID string) (*VectorStoreRecord, error) {
	ctx = ensureCtx(ctx)
	existing, err := s.FindVectorStore(ctx, vectorStoreID, systemAccountID, apiKeyID)
	if err != nil || existing == nil {
		return nil, err
	}
	now := s.nowISO()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("openai_compatible_vector_stores")+`
		SET status = 'deleted', deleted_at = ?, updated_at = ?
		WHERE id = ? AND system_account_id = ? AND api_key_id = ? AND deleted_at IS NULL`),
		now, now, vectorStoreID, systemAccountID, apiKeyID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("openai_compatible_vector_store_files")+`
		SET deleted_at = ?, updated_at = ?
		WHERE vector_store_id = ? AND system_account_id = ? AND api_key_id = ? AND deleted_at IS NULL`),
		now, now, vectorStoreID, systemAccountID, apiKeyID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("openai_compatible_vector_store_chunks")+`
		WHERE vector_store_id = ? AND system_account_id = ? AND api_key_id = ?`),
		vectorStoreID, systemAccountID, apiKeyID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	deleted := *existing
	deleted.Status = VectorStoreStatusDeleted
	deleted.DeletedAt = &now
	deleted.UpdatedAt = now
	return &deleted, nil
}

// CreateVectorStoreFile mirrors createOpenAICompatibleVectorStoreFile(Async):
// requires both store and file, upserts the binding, replaces the chunk set
// and refreshes the aggregate bytes inside one transaction.
func (s *Store) CreateVectorStoreFile(ctx context.Context, input VectorStoreFileCreateInput) (*VectorStoreFileRecord, error) {
	ctx = ensureCtx(ctx)
	store, err := s.FindVectorStore(ctx, input.VectorStoreID, input.SystemAccountID, input.APIKeyID)
	if err != nil || store == nil {
		return nil, err
	}
	file, err := s.FindFile(ctx, input.FileID, input.SystemAccountID, input.APIKeyID)
	if err != nil || file == nil {
		return nil, err
	}
	chunks := input.Chunks
	if chunks == nil {
		chunks = []ChunkInput{}
	}
	now := s.nowISO()
	usageBytes := int64(0)
	if input.UsageBytes != nil {
		usageBytes = *input.UsageBytes
	} else {
		for _, chunk := range chunks {
			usageBytes += int64(len(chunk.ContentText))
		}
	}
	attributesJSON, err := marshalJSON(orEmptyObject(input.Attributes))
	if err != nil {
		return nil, err
	}
	chunkingJSON, err := marshalJSON(orEmptyObject(input.ChunkingStrategy))
	if err != nil {
		return nil, err
	}
	var lastErrorAny any
	if input.LastError != nil {
		encoded, encodeErr := marshalJSON(input.LastError)
		if encodeErr != nil {
			return nil, encodeErr
		}
		lastErrorAny = string(encoded)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("openai_compatible_vector_store_files")+` (
		vector_store_id, file_id, system_account_id, api_key_id, attributes_json,
		chunking_strategy_json, status, usage_bytes, last_error_json, created_at, updated_at, deleted_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
	ON CONFLICT(vector_store_id, file_id) DO UPDATE SET
		attributes_json = excluded.attributes_json,
		chunking_strategy_json = excluded.chunking_strategy_json,
		status = excluded.status,
		usage_bytes = excluded.usage_bytes,
		last_error_json = excluded.last_error_json,
		updated_at = excluded.updated_at,
		deleted_at = NULL`), input.VectorStoreID, input.FileID, input.SystemAccountID, input.APIKeyID,
		string(attributesJSON), string(chunkingJSON), input.Status, usageBytes, lastErrorAny, now, now); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("openai_compatible_vector_store_chunks")+`
		WHERE vector_store_id = ? AND file_id = ? AND system_account_id = ? AND api_key_id = ?`),
		input.VectorStoreID, input.FileID, input.SystemAccountID, input.APIKeyID); err != nil {
		return nil, err
	}
	for index, chunk := range chunks {
		if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("openai_compatible_vector_store_chunks")+` (
			id, vector_store_id, file_id, system_account_id, api_key_id, chunk_index,
			content_text, content_preview, token_estimate, keyword_index_text, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
			s.generateID("chunk"), input.VectorStoreID, input.FileID, input.SystemAccountID, input.APIKeyID,
			index, chunk.ContentText, chunk.ContentPreview, chunk.TokenEstimate, chunk.KeywordIndexText, now); err != nil {
			return nil, err
		}
	}
	if err := s.refreshVectorStoreBytesTx(ctx, tx, input.VectorStoreID, input.SystemAccountID, input.APIKeyID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.FindVectorStoreFile(ctx, input.VectorStoreID, input.FileID, input.SystemAccountID, input.APIKeyID)
}

func orEmptyObject(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	return value
}

// ListVectorStoreFiles mirrors listOpenAICompatibleVectorStoreFiles(Async).
func (s *Store) ListVectorStoreFiles(ctx context.Context, options VectorStoreFileListOptions) (ListResult[VectorStoreFileRecord], error) {
	ctx = ensureCtx(ctx)
	limit := normalizeListLimit(options.Limit)
	order := listOrder(options.Order)
	params := []any{options.VectorStoreID, options.SystemAccountID, options.APIKeyID}
	clauses := []string{"vector_store_id = ?", "system_account_id = ?", "api_key_id = ?", "deleted_at IS NULL"}
	if options.After != "" {
		cursor, err := s.findVectorStoreFileCursor(ctx, options.After, options.VectorStoreID, options.SystemAccountID, options.APIKeyID)
		if err != nil {
			return ListResult[VectorStoreFileRecord]{}, err
		}
		if cursor == nil {
			return ListResult[VectorStoreFileRecord]{Items: []VectorStoreFileRecord{}}, nil
		}
		clauses = append(clauses, cursorClause(order, "created_at", "file_id", true))
		params = append(params, cursor.createdAt, cursor.createdAt, cursor.fileID)
	}
	args := append(params, limit+1)
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT `+vectorStoreFileSelectColumns+`
		FROM `+s.table("openai_compatible_vector_store_files")+`
		WHERE `+strings.Join(clauses, " AND ")+`
		ORDER BY created_at `+strings.ToUpper(order)+`, file_id `+strings.ToUpper(order)+`
		LIMIT ?`), args...)
	if err != nil {
		return ListResult[VectorStoreFileRecord]{}, err
	}
	defer rows.Close()
	records := []VectorStoreFileRecord{}
	for rows.Next() {
		record, scanErr := scanVectorStoreFile(rows.Scan)
		if scanErr != nil {
			return ListResult[VectorStoreFileRecord]{}, scanErr
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return ListResult[VectorStoreFileRecord]{}, err
	}
	hasMore := len(records) > limit
	if hasMore {
		records = records[:limit]
	}
	return ListResult[VectorStoreFileRecord]{Items: records, HasMore: hasMore}, nil
}

// FindVectorStoreFile mirrors findOpenAICompatibleVectorStoreFile(Async): the
// record embeds the current underlying file (nil when the file is gone).
func (s *Store) FindVectorStoreFile(ctx context.Context, vectorStoreID, fileID, systemAccountID, apiKeyID string) (*VectorStoreFileRecord, error) {
	ctx = ensureCtx(ctx)
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT `+vectorStoreFileSelectColumns+`
		FROM `+s.table("openai_compatible_vector_store_files")+`
		WHERE vector_store_id = ?
			AND file_id = ?
			AND system_account_id = ?
			AND api_key_id = ?
			AND deleted_at IS NULL
		LIMIT 1`), vectorStoreID, fileID, systemAccountID, apiKeyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		closeErr := rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		return nil, nil
	}
	record, err := scanVectorStoreFile(rows.Scan)
	rows.Close()
	if err != nil {
		return nil, err
	}
	file, err := s.FindFile(ctx, fileID, systemAccountID, apiKeyID)
	if err != nil {
		return nil, err
	}
	record.File = file
	return &record, nil
}

// DeleteVectorStoreFile mirrors deleteOpenAICompatibleVectorStoreFile(Async).
func (s *Store) DeleteVectorStoreFile(ctx context.Context, vectorStoreID, fileID, systemAccountID, apiKeyID string) (*VectorStoreFileRecord, error) {
	ctx = ensureCtx(ctx)
	existing, err := s.FindVectorStoreFile(ctx, vectorStoreID, fileID, systemAccountID, apiKeyID)
	if err != nil || existing == nil {
		return nil, err
	}
	now := s.nowISO()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("openai_compatible_vector_store_files")+`
		SET deleted_at = ?, updated_at = ?
		WHERE vector_store_id = ? AND file_id = ? AND system_account_id = ? AND api_key_id = ? AND deleted_at IS NULL`),
		now, now, vectorStoreID, fileID, systemAccountID, apiKeyID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("openai_compatible_vector_store_chunks")+`
		WHERE vector_store_id = ? AND file_id = ? AND system_account_id = ? AND api_key_id = ?`),
		vectorStoreID, fileID, systemAccountID, apiKeyID); err != nil {
		return nil, err
	}
	if err := s.refreshVectorStoreBytesTx(ctx, tx, vectorStoreID, systemAccountID, apiKeyID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	deleted := *existing
	deleted.DeletedAt = &now
	deleted.UpdatedAt = now
	return &deleted, nil
}

// SearchVectorStore mirrors searchOpenAICompatibleVectorStore(Async): the SQL
// fetches LIKE-matched candidates (capped at max(maxResults*20, 100)), then
// scoring, attribute filtering and sorting happen in memory exactly like the
// Node implementation.
func (s *Store) SearchVectorStore(ctx context.Context, options SearchOptions) ([]SearchResult, error) {
	ctx = ensureCtx(ctx)
	maxResults := normalizeSearchLimit(options.MaxNumResults)
	terms := uniqueSearchTerms(options.Query)
	params := []any{options.VectorStoreID, options.SystemAccountID, options.APIKeyID}
	where := []string{
		"c.vector_store_id = ?",
		"c.system_account_id = ?",
		"c.api_key_id = ?",
		"vsf.deleted_at IS NULL",
		"vsf.status = 'completed'",
	}
	if len(terms) > 0 {
		likes := make([]string, len(terms))
		for index, term := range terms {
			likes[index] = "c.keyword_index_text LIKE ?"
			params = append(params, "%"+escapeSQLLike(term)+"%")
		}
		where = append(where, "("+strings.Join(likes, " OR ")+")")
	}
	probeLimit := maxResults * 20
	if probeLimit < 100 {
		probeLimit = 100
	}
	args := append(params, probeLimit)
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT
		c.id,
		c.vector_store_id,
		c.file_id,
		c.chunk_index,
		c.content_text,
		c.content_preview,
		c.keyword_index_text,
		f.filename,
		vsf.attributes_json
	FROM `+s.table("openai_compatible_vector_store_chunks")+` c
	JOIN `+s.table("openai_compatible_vector_store_files")+` vsf
		ON vsf.vector_store_id = c.vector_store_id
		AND vsf.file_id = c.file_id
		AND vsf.system_account_id = c.system_account_id
		AND vsf.api_key_id = c.api_key_id
	JOIN `+s.table("openai_compatible_files")+` f
		ON f.id = c.file_id
		AND f.system_account_id = c.system_account_id
		AND f.api_key_id = c.api_key_id
		AND f.deleted_at IS NULL
	WHERE `+strings.Join(where, " AND ")+`
	ORDER BY c.file_id ASC, c.chunk_index ASC
	LIMIT ?`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	scored := []SearchResult{}
	for rows.Next() {
		result, scanErr := scanSearchRow(rows.Scan, terms)
		if scanErr != nil {
			return nil, scanErr
		}
		scored = append(scored, result)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	filtered := scored[:0]
	for _, result := range scored {
		if !matchesAttributeFilter(result.Attributes, options.Filters) {
			continue
		}
		threshold := 0.0
		if options.ScoreThreshold != nil {
			threshold = *options.ScoreThreshold
		}
		if result.Score < threshold {
			continue
		}
		filtered = append(filtered, result)
	}
	sort.SliceStable(filtered, func(left, right int) bool {
		l, r := filtered[left], filtered[right]
		if l.Score != r.Score {
			return l.Score > r.Score
		}
		if l.FileID != r.FileID {
			return l.FileID < r.FileID
		}
		return l.ChunkIndex < r.ChunkIndex
	})
	if len(filtered) > maxResults {
		filtered = filtered[:maxResults]
	}
	return filtered, nil
}

// ListVectorStoreFileChunks mirrors listOpenAICompatibleVectorStoreFileChunks(Async).
func (s *Store) ListVectorStoreFileChunks(ctx context.Context, vectorStoreID, fileID, systemAccountID, apiKeyID string, limit *int) ([]FileChunkRecord, error) {
	ctx = ensureCtx(ctx)
	clamped := normalizeSearchLimit(limit)
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT
		c.id,
		c.vector_store_id,
		c.file_id,
		c.chunk_index,
		c.content_text,
		c.content_preview,
		c.keyword_index_text,
		f.filename,
		vsf.attributes_json
	FROM `+s.table("openai_compatible_vector_store_chunks")+` c
	JOIN `+s.table("openai_compatible_vector_store_files")+` vsf
		ON vsf.vector_store_id = c.vector_store_id
		AND vsf.file_id = c.file_id
		AND vsf.system_account_id = c.system_account_id
		AND vsf.api_key_id = c.api_key_id
	JOIN `+s.table("openai_compatible_files")+` f
		ON f.id = c.file_id
		AND f.system_account_id = c.system_account_id
		AND f.api_key_id = c.api_key_id
		AND f.deleted_at IS NULL
	WHERE c.vector_store_id = ?
		AND c.file_id = ?
		AND c.system_account_id = ?
		AND c.api_key_id = ?
		AND vsf.deleted_at IS NULL
	ORDER BY c.chunk_index ASC
	LIMIT ?`), vectorStoreID, fileID, systemAccountID, apiKeyID, clamped)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []FileChunkRecord{}
	for rows.Next() {
		var record FileChunkRecord
		var chunkIndexAny any
		if err := rows.Scan(&record.ChunkID, &record.VectorStoreID, &record.FileID, &chunkIndexAny,
			&record.ContentText, &record.ContentPreview, new(any), &record.Filename, new(any)); err != nil {
			return nil, err
		}
		record.ChunkIndex = int(coerceInt64(chunkIndexAny))
		records = append(records, record)
	}
	return records, rows.Err()
}

func scanVectorStoreFile(scan func(...any) error) (VectorStoreFileRecord, error) {
	var record VectorStoreFileRecord
	var attributesJSON, chunkingJSON any
	var lastError sql.NullString
	var deletedAt sql.NullString
	var usageBytesAny any
	err := scan(&record.VectorStoreID, &record.FileID, &record.SystemAccountID, &record.APIKeyID,
		&attributesJSON, &chunkingJSON, &record.Status, &usageBytesAny, &lastError, &record.CreatedAt, &record.UpdatedAt, &deletedAt)
	if err != nil {
		return VectorStoreFileRecord{}, err
	}
	record.Attributes = parseJSONObject(coerceString(attributesJSON))
	record.ChunkingStrategy = parseJSONObject(coerceString(chunkingJSON))
	record.Status = vectorStoreFileStatus(record.Status)
	record.UsageBytes = coerceInt64(usageBytesAny)
	if lastError.Valid {
		record.LastError = parseJSONObject(lastError.String)
		record.HasLastError = true
	}
	record.DeletedAt = fromNullString(deletedAt)
	return record, nil
}

// vectorStoreFileStatus mirrors vectorStoreFileStatus (unknown -> in_progress).
func vectorStoreFileStatus(value string) string {
	switch value {
	case VectorStoreFileStatusCompleted, VectorStoreFileStatusFailed, VectorStoreFileStatusCancelled:
		return value
	default:
		return VectorStoreFileStatusInProgress
	}
}

// vectorStoreFileCounts mirrors vectorStoreFileCounts(WithClient).
func (s *Store) vectorStoreFileCounts(ctx context.Context, vectorStoreID, systemAccountID, apiKeyID string) (FileCounts, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT status, COUNT(*) AS count
		FROM `+s.table("openai_compatible_vector_store_files")+`
		WHERE vector_store_id = ? AND system_account_id = ? AND api_key_id = ? AND deleted_at IS NULL
		GROUP BY status`), vectorStoreID, systemAccountID, apiKeyID)
	if err != nil {
		return FileCounts{}, err
	}
	defer rows.Close()
	counts := FileCounts{}
	for rows.Next() {
		var status string
		var countAny any
		if err := rows.Scan(&status, &countAny); err != nil {
			return FileCounts{}, err
		}
		count := int(coerceInt64(countAny))
		counts.Total += count
		switch vectorStoreFileStatus(status) {
		case VectorStoreFileStatusInProgress:
			counts.InProgress += count
		case VectorStoreFileStatusCompleted:
			counts.Completed += count
		case VectorStoreFileStatusFailed:
			counts.Failed += count
		case VectorStoreFileStatusCancelled:
			counts.Cancelled += count
		}
	}
	return counts, rows.Err()
}

type searchRowData struct {
	id             string
	vectorStoreID  string
	fileID         string
	chunkIndex     any
	contentText    string
	contentPreview string
	keywordIndex   string
	filename       string
	attributesJSON any
}

func scanSearchRow(scan func(...any) error, terms []string) (SearchResult, error) {
	var row searchRowData
	err := scan(&row.id, &row.vectorStoreID, &row.fileID, &row.chunkIndex, &row.contentText,
		&row.contentPreview, &row.keywordIndex, &row.filename, &row.attributesJSON)
	if err != nil {
		return SearchResult{}, err
	}
	return SearchResult{
		ChunkID:        row.id,
		VectorStoreID:  row.vectorStoreID,
		FileID:         row.fileID,
		Filename:       row.filename,
		Attributes:     parseJSONObject(coerceString(row.attributesJSON)),
		ChunkIndex:     int(coerceInt64(row.chunkIndex)),
		Score:          scoreKeywordMatch(row.keywordIndex, terms),
		ContentText:    row.contentText,
		ContentPreview: row.contentPreview,
	}, nil
}

// refreshVectorStoreBytesTx mirrors refreshVectorStoreBytes(WithClient).
func (s *Store) refreshVectorStoreBytesTx(ctx context.Context, tx *sql.Tx, vectorStoreID, systemAccountID, apiKeyID string) error {
	_, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("openai_compatible_vector_stores")+`
		SET bytes = COALESCE((
			SELECT SUM(usage_bytes)
			FROM `+s.table("openai_compatible_vector_store_files")+`
			WHERE vector_store_id = ?
				AND system_account_id = ?
				AND api_key_id = ?
				AND deleted_at IS NULL
				AND status = 'completed'
		), 0),
		updated_at = ?
	WHERE id = ?
		AND system_account_id = ?
		AND api_key_id = ?
		AND deleted_at IS NULL`),
		vectorStoreID, systemAccountID, apiKeyID, s.nowISO(), vectorStoreID, systemAccountID, apiKeyID)
	return err
}

// scoreKeywordMatch mirrors scoreKeywordMatch: term frequency scored as
// 1 + log2(count), normalized by the term count and capped at 1.
func scoreKeywordMatch(text string, terms []string) float64 {
	if len(terms) == 0 {
		return 0.01
	}
	haystack := strings.ToLower(text)
	score := 0.0
	for _, term := range terms {
		count := 0
		for from := 0; from <= len(haystack); {
			index := strings.Index(haystack[from:], term)
			if index < 0 {
				break
			}
			count++
			from += index + len(term)
		}
		if count > 0 {
			score += 1 + math.Log2(float64(count))
		}
	}
	denominator := float64(len(terms))
	if denominator < 1 {
		denominator = 1
	}
	return math.Min(1, score/denominator)
}

// uniqueSearchTerms mirrors uniqueSearchTerms: lowercase tokens of letters,
// digits, '_' or '-', length >= 2, deduplicated, capped at 20.
func uniqueSearchTerms(query string) []string {
	terms := []string{}
	seen := map[string]bool{}
	lower := strings.ToLower(query)
	current := strings.Builder{}
	flush := func() {
		token := strings.TrimSpace(current.String())
		current.Reset()
		if token == "" {
			return
		}
		runes := []rune(token)
		if len(runes) < 2 {
			return
		}
		if !seen[token] {
			seen[token] = true
			if len(terms) < 20 {
				terms = append(terms, token)
			}
		}
	}
	for _, symbol := range lower {
		if unicode.IsLetter(symbol) || unicode.IsDigit(symbol) || symbol == '_' || symbol == '-' {
			current.WriteRune(symbol)
			continue
		}
		flush()
	}
	flush()
	return terms
}

// matchesAttributeFilter mirrors matchesAttributeFilter (eq/ne/in/nin and
// gt/gte/lt/lte over numeric coercions).
func matchesAttributeFilter(attributes map[string]any, filter map[string]any) bool {
	if len(filter) == 0 {
		return true
	}
	filterType, _ := filter["type"].(string)
	switch filterType {
	case "and":
		for _, item := range filterList(filter["filters"]) {
			if !matchesAttributeFilter(attributes, item) {
				return false
			}
		}
		return true
	case "or":
		for _, item := range filterList(filter["filters"]) {
			if matchesAttributeFilter(attributes, item) {
				return true
			}
		}
		return false
	}
	key, _ := filter["key"].(string)
	if key == "" {
		return true
	}
	actual, hasActual := attributes[key]
	expected := filter["value"]
	switch filterType {
	case "ne":
		return !hasActual || !jsonScalarEqual(actual, expected)
	case "in":
		list, ok := expected.([]any)
		if !ok {
			return false
		}
		for _, item := range list {
			if hasActual && jsonScalarEqual(actual, item) {
				return true
			}
		}
		return false
	case "nin":
		list, ok := expected.([]any)
		if !ok {
			return false
		}
		for _, item := range list {
			if hasActual && jsonScalarEqual(actual, item) {
				return false
			}
		}
		return true
	case "gt", "gte", "lt", "lte":
		left, leftOK := asNumber(actual)
		right, rightOK := asNumber(expected)
		if !leftOK || !rightOK {
			return false
		}
		switch filterType {
		case "gt":
			return left > right
		case "gte":
			return left >= right
		case "lt":
			return left < right
		default:
			return left <= right
		}
	}
	return hasActual && jsonScalarEqual(actual, expected)
}

func filterList(value any) []map[string]any {
	list, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(list))
	for _, item := range list {
		if record, ok := item.(map[string]any); ok {
			out = append(out, record)
		}
	}
	return out
}

func asNumber(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case string:
		if strings.TrimSpace(typed) == "" {
			return 0, false
		}
		parsed, ok := parseJSNumber(typed)
		if !ok || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

// jsonScalarEqual mirrors jsonScalarEqual (strict equality over decoded JSON).
func jsonScalarEqual(left, right any) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left == right
}

// escapeSQLLike mirrors escapeSqlLike (backslash-escaping % and _).
func escapeSQLLike(value string) string {
	replacer := strings.NewReplacer("%", "\\%", "_", "\\_")
	return replacer.Replace(value)
}

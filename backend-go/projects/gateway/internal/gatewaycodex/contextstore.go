package gatewaycodex

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"unicode/utf16"

	"github.com/huanminabc/juhe-ai/backend-go-platform/sqlpool"
	_ "modernc.org/sqlite"
)

// Port of storage/codex-context-state.repository.ts (the response/compact
// index part of the codex-responses import chain) with the db-service
// dual-mode rule: the sqlite driver keeps one DatabaseSync per shard file
// under JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT (state-000.sqlite3 …), the
// postgres driver stores the same rows in the juhe_codex_context schema
// through the sqlpool registry. Row layout, outcome semantics, chain walk,
// expiry comparison (ISO string compare) and the touch-on-read refresh are
// byte-identical with the Node repository.

// CodexContextStateBoundary mirrors CodexContextStateBoundary.
type CodexContextStateBoundary struct {
	SystemAccountID string `json:"systemAccountId"`
	// APIKeyID empty mirrors the Node undefined api key.
	APIKeyID     string `json:"apiKeyId,omitempty"`
	GroupID      string `json:"groupId"`
	ProviderCode string `json:"providerCode"`
}

// CodexContextPayloadReference mirrors CodexContextPayloadReference.
type CodexContextPayloadReference struct {
	StorageKey          string `json:"storageKey"`
	StorageOffsetBytes  int64  `json:"storageOffsetBytes"`
	SHA256              string `json:"sha256"`
	RawSizeBytes        int64  `json:"rawSizeBytes"`
	CompressedSizeBytes int64  `json:"compressedSizeBytes"`
	Compression         string `json:"compression"` // 'gzip'
	SchemaVersion       int64  `json:"schemaVersion"`
}

// CodexContextResponseStateIndex mirrors CodexContextResponseStateIndex.
type CodexContextResponseStateIndex struct {
	CodexContextStateBoundary
	CodexContextPayloadReference
	ResponseID         string
	SessionID          string
	PreviousResponseID string
	UpstreamAccountID  string
	Model              string
	UpstreamModel      string
	CreatedAt          string
	UpdatedAt          string
	LastUsedAt         string
	ExpiresAt          string
}

// CodexContextCompactStateIndex mirrors CodexContextCompactStateIndex.
type CodexContextCompactStateIndex struct {
	CodexContextStateBoundary
	CodexContextPayloadReference
	CompactID         string
	SessionID         string
	SourceResponseID  string
	SummaryDigest     string
	UpstreamAccountID string
	Model             string
	UpstreamModel     string
	CreatedAt         string
	UpdatedAt         string
	LastUsedAt        string
	ExpiresAt         string
}

// Codex context read outcomes mirror the Node result unions.
const (
	CodexContextOutcomeFound          = "found"
	CodexContextOutcomeNotFound       = "not_found"
	CodexContextOutcomeExpired        = "expired"
	CodexContextOutcomeBoundaryMismat = "boundary_mismatch"
	CodexContextOutcomeChainTooDeep   = "chain_too_deep"
	CodexContextOutcomeChainBroken    = "chain_broken"
)

// CodexContextResponseChainReadResult mirrors
// CodexContextResponseChainReadResult.
type CodexContextResponseChainReadResult struct {
	Outcome   string
	SessionID string
	Responses []CodexContextResponseStateIndex
	// ResponseID mirrors the failure outcome responseId.
	ResponseID string
}

// CodexContextCompactReadResult mirrors CodexContextCompactReadResult.
type CodexContextCompactReadResult struct {
	Outcome   string
	Compact   *CodexContextCompactStateIndex
	CompactID string
	SessionID string
}

// CodexContextRowStore is the per-row persistence seam shared by the sqlite
// shard and postgres drivers; tests inject a memory implementation.
type CodexContextRowStore interface {
	SaveResponseStateRow(ctx context.Context, row CodexContextResponseStateIndex) error
	SaveCompactStateRow(ctx context.Context, row CodexContextCompactStateIndex) error
	ReadResponseStateRow(ctx context.Context, responseID string) (*CodexContextResponseStateIndex, error)
	ReadCompactStateRow(ctx context.Context, compactID string) (*CodexContextCompactStateIndex, error)
	// TouchOnFound mirrors the read refresh: sessions row of the chain head,
	// every chain response row, or the compact row + its sessions row.
	TouchResponseChain(ctx context.Context, rows []CodexContextResponseStateIndex, now, refreshExpiresAt string) error
	TouchCompact(ctx context.Context, row CodexContextCompactStateIndex, now, refreshExpiresAt string) error
}

// SaveCodexContextResponseStateIndex mirrors saveCodexContextResponseStateIndex
// (both drivers share the session index upsert).
func SaveCodexContextResponseStateIndex(ctx context.Context, store CodexContextRowStore, row CodexContextResponseStateIndex) error {
	if err := validateResponseStateIndex(&row); err != nil {
		return err
	}
	return store.SaveResponseStateRow(ctx, row)
}

// SaveCodexContextCompactStateIndex mirrors saveCodexContextCompactStateIndex.
func SaveCodexContextCompactStateIndex(ctx context.Context, store CodexContextRowStore, row CodexContextCompactStateIndex) error {
	if strings.TrimSpace(row.CompactID) == "" {
		return errors.New("compactId 不能为空")
	}
	if strings.TrimSpace(row.SessionID) == "" {
		return errors.New("sessionId 不能为空")
	}
	return store.SaveCompactStateRow(ctx, row)
}

// ResponseChainReadInput mirrors readCodexContextResponseStateChain's input.
type ResponseChainReadInput struct {
	ResponseID       string
	Boundary         CodexContextStateBoundary
	MaxDepth         int
	Now              string
	RefreshExpiresAt string
}

// CompactStateReadInput mirrors readCodexContextCompactState's input.
type CompactStateReadInput struct {
	CompactID        string
	Boundary         CodexContextStateBoundary
	Now              string
	RefreshExpiresAt string
}

// ReadCodexContextResponseStateChain mirrors
// readCodexContextResponseStateChain: the walk, expiry, boundary and depth
// semantics are shared by both drivers.
func ReadCodexContextResponseStateChain(ctx context.Context, store CodexContextRowStore, input ResponseChainReadInput) (CodexContextResponseChainReadResult, error) {
	responseID := strings.TrimSpace(input.ResponseID)
	if responseID == "" {
		return CodexContextResponseChainReadResult{}, errors.New("responseId 不能为空")
	}
	now := input.Now
	maxDepth := clampChainDepth(input.MaxDepth)
	rows := make([]CodexContextResponseStateIndex, 0, 8)
	cursor := responseID
	for depth := 0; cursor != "" && depth < maxDepth; depth++ {
		mapped, err := store.ReadResponseStateRow(ctx, cursor)
		if err != nil {
			return CodexContextResponseChainReadResult{}, err
		}
		if mapped == nil {
			outcome := CodexContextOutcomeChainBroken
			if len(rows) == 0 {
				outcome = CodexContextOutcomeNotFound
			}
			return CodexContextResponseChainReadResult{Outcome: outcome, ResponseID: cursor}, nil
		}
		if mapped.ExpiresAt < now {
			return CodexContextResponseChainReadResult{Outcome: CodexContextOutcomeExpired, ResponseID: mapped.ResponseID, SessionID: mapped.SessionID}, nil
		}
		if !codexContextBoundaryMatches(&mapped.CodexContextStateBoundary, &input.Boundary) {
			return CodexContextResponseChainReadResult{Outcome: CodexContextOutcomeBoundaryMismat, ResponseID: mapped.ResponseID, SessionID: mapped.SessionID}, nil
		}
		rows = append(rows, *mapped)
		cursor = mapped.PreviousResponseID
	}
	if cursor != "" {
		sessionID := ""
		if len(rows) > 0 {
			sessionID = rows[0].SessionID
		}
		return CodexContextResponseChainReadResult{Outcome: CodexContextOutcomeChainTooDeep, ResponseID: cursor, SessionID: sessionID}, nil
	}
	reverseResponseRows(rows)
	if err := store.TouchResponseChain(ctx, rows, now, orDefault(input.RefreshExpiresAt, now)); err != nil {
		return CodexContextResponseChainReadResult{}, err
	}
	sessionID := responseID
	if len(rows) > 0 {
		sessionID = rows[0].SessionID
	}
	return CodexContextResponseChainReadResult{Outcome: CodexContextOutcomeFound, SessionID: sessionID, Responses: rows}, nil
}

// ReadCodexContextCompactState mirrors readCodexContextCompactState.
func ReadCodexContextCompactState(ctx context.Context, store CodexContextRowStore, input CompactStateReadInput) (CodexContextCompactReadResult, error) {
	compactID := strings.TrimSpace(input.CompactID)
	if compactID == "" {
		return CodexContextCompactReadResult{}, errors.New("compactId 不能为空")
	}
	now := input.Now
	mapped, err := store.ReadCompactStateRow(ctx, compactID)
	if err != nil {
		return CodexContextCompactReadResult{}, err
	}
	if mapped == nil {
		return CodexContextCompactReadResult{Outcome: CodexContextOutcomeNotFound, CompactID: compactID}, nil
	}
	if mapped.ExpiresAt < now {
		return CodexContextCompactReadResult{Outcome: CodexContextOutcomeExpired, CompactID: compactID, SessionID: mapped.SessionID}, nil
	}
	if !codexContextBoundaryMatches(&mapped.CodexContextStateBoundary, &input.Boundary) {
		return CodexContextCompactReadResult{Outcome: CodexContextOutcomeBoundaryMismat, CompactID: compactID, SessionID: mapped.SessionID}, nil
	}
	if err := store.TouchCompact(ctx, *mapped, now, orDefault(input.RefreshExpiresAt, now)); err != nil {
		return CodexContextCompactReadResult{}, err
	}
	return CodexContextCompactReadResult{Outcome: CodexContextOutcomeFound, Compact: mapped}, nil
}

func clampChainDepth(maxDepth int) int {
	normalized := maxDepth
	if maxDepth == 0 {
		normalized = 64
	}
	if normalized < 1 {
		normalized = 1
	}
	if normalized > 256 {
		normalized = 256
	}
	return normalized
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func reverseResponseRows(rows []CodexContextResponseStateIndex) {
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
}

// codexContextBoundaryMatches mirrors matchesBoundary: an absent api key
// compares as the empty string.
func codexContextBoundaryMatches(row, boundary *CodexContextStateBoundary) bool {
	return row.SystemAccountID == boundary.SystemAccountID &&
		row.APIKeyID == boundary.APIKeyID &&
		row.GroupID == boundary.GroupID &&
		row.ProviderCode == boundary.ProviderCode
}

func validateResponseStateIndex(row *CodexContextResponseStateIndex) error {
	if strings.TrimSpace(row.ResponseID) == "" {
		return errors.New("responseId 不能为空")
	}
	if strings.TrimSpace(row.SessionID) == "" {
		return errors.New("sessionId 不能为空")
	}
	return nil
}

// ---------------------------------------------------------------------------
// sqlite shard driver (sqlitepath mode)
// ---------------------------------------------------------------------------

// SQLiteShardStoreConfig carries the shard root and count
// (JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT / _COUNT).
type SQLiteShardStoreConfig struct {
	Root       string
	ShardCount int
}

// SQLiteShardContextStateStore mirrors the Node shard DatabaseSync map: one
// sqlite file per shard, opened lazily, schema applied on create.
type SQLiteShardContextStateStore struct {
	config SQLiteShardStoreConfig

	mu  sync.Mutex
	dbs map[int]*sql.DB
}

// NewSQLiteShardContextStateStore builds the sqlite driver.
func NewSQLiteShardContextStateStore(config SQLiteShardStoreConfig) (*SQLiteShardContextStateStore, error) {
	if strings.TrimSpace(config.Root) == "" {
		return nil, errors.New("JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT 在 sqlite driver 下必须配置")
	}
	return &SQLiteShardContextStateStore{
		config: config,
		dbs:    map[int]*sql.DB{},
	}, nil
}

// CodexContextStateShardCount mirrors codexContextStateShardCount.
func (s *SQLiteShardContextStateStore) shardCount() int {
	count := s.config.ShardCount
	if count < 1 {
		count = 1
	}
	if count > 256 {
		count = 256
	}
	return count
}

// CodexContextStateShardIndexForKey mirrors codexContextStateShardIndexForKey:
// FNV-1a over the UTF-16 code units of the key, modulo the shard count
// clamped to 1..256 exactly like codexContextStateShardCount.
func CodexContextStateShardIndexForKey(key string, shardCount int) int {
	if shardCount < 1 {
		shardCount = 1
	}
	if shardCount > 256 {
		shardCount = 256
	}
	hash := uint32(2166136261)
	for _, unit := range utf16.Encode([]rune(key)) {
		hash ^= uint32(unit)
		hash *= 16777619
	}
	return int(hash % uint32(shardCount))
}

func (s *SQLiteShardContextStateStore) databaseForKey(key string) (*sql.DB, error) {
	shardIndex := CodexContextStateShardIndexForKey(key, s.shardCount())
	s.mu.Lock()
	defer s.mu.Unlock()
	if db, ok := s.dbs[shardIndex]; ok {
		return db, nil
	}
	path := filepath.Join(s.config.Root, "state-"+padLeft3(shardIndex)+".sqlite3")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create codex context shard directory: %w", err)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("open codex context state shard %d: %w", shardIndex, err)
	}
	if _, err := db.Exec(codexContextStateSchemaSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply codex context state schema on shard %d: %w", shardIndex, err)
	}
	s.dbs[shardIndex] = db
	return db, nil
}

func padLeft3(value int) string {
	text := strconv.Itoa(value)
	for len(text) < 3 {
		text = "0" + text
	}
	return text
}

// SaveResponseStateRow implements CodexContextRowStore.
func (s *SQLiteShardContextStateStore) SaveResponseStateRow(ctx context.Context, row CodexContextResponseStateIndex) error {
	db, err := s.databaseForKey(row.ResponseID)
	if err != nil {
		return err
	}
	sessionDB, err := s.databaseForKey(row.SessionID)
	if err != nil {
		return err
	}
	tx, err := sessionDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := upsertSessionRow(ctx, tx, sessionUpsertInputFromResponse(row)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return withSQLTx(ctx, db, func(tx *sql.Tx) error {
		return upsertResponseStateRow(ctx, tx, &row)
	})
}

// SaveCompactStateRow implements CodexContextRowStore.
func (s *SQLiteShardContextStateStore) SaveCompactStateRow(ctx context.Context, row CodexContextCompactStateIndex) error {
	db, err := s.databaseForKey(row.CompactID)
	if err != nil {
		return err
	}
	sessionDB, err := s.databaseForKey(row.SessionID)
	if err != nil {
		return err
	}
	tx, err := sessionDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := upsertSessionRow(ctx, tx, sessionUpsertInputFromCompact(row)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return withSQLTx(ctx, db, func(tx *sql.Tx) error {
		return upsertCompactStateRow(ctx, tx, &row)
	})
}

// ReadResponseStateRow implements CodexContextRowStore.
func (s *SQLiteShardContextStateStore) ReadResponseStateRow(ctx context.Context, responseID string) (*CodexContextResponseStateIndex, error) {
	db, err := s.databaseForKey(responseID)
	if err != nil {
		return nil, err
	}
	row := db.QueryRowContext(ctx, `SELECT * FROM codex_context_responses WHERE response_id = ? LIMIT 1`, responseID)
	return scanResponseStateRow(row)
}

// ReadCompactStateRow implements CodexContextRowStore.
func (s *SQLiteShardContextStateStore) ReadCompactStateRow(ctx context.Context, compactID string) (*CodexContextCompactStateIndex, error) {
	db, err := s.databaseForKey(compactID)
	if err != nil {
		return nil, err
	}
	row := db.QueryRowContext(ctx, `SELECT * FROM codex_context_compacts WHERE compact_id = ? LIMIT 1`, compactID)
	return scanCompactStateRow(row)
}

// TouchResponseChain implements CodexContextRowStore.
func (s *SQLiteShardContextStateStore) TouchResponseChain(ctx context.Context, rows []CodexContextResponseStateIndex, now, refreshExpiresAt string) error {
	if len(rows) == 0 {
		return nil
	}
	sessionID := ""
	if len(rows) > 0 {
		sessionID = rows[0].SessionID
	}
	if sessionDB, err := s.databaseForKey(sessionID); err == nil {
		_, _ = sessionDB.ExecContext(ctx, `UPDATE codex_context_sessions SET last_used_at = ?, updated_at = ?, expires_at = ? WHERE id = ?`, now, now, refreshExpiresAt, sessionID)
	}
	for _, row := range rows {
		db, err := s.databaseForKey(row.ResponseID)
		if err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, `UPDATE codex_context_responses SET last_used_at = ?, updated_at = ?, expires_at = ? WHERE response_id = ?`, now, now, refreshExpiresAt, row.ResponseID); err != nil {
			return err
		}
	}
	return nil
}

// TouchCompact implements CodexContextRowStore.
func (s *SQLiteShardContextStateStore) TouchCompact(ctx context.Context, row CodexContextCompactStateIndex, now, refreshExpiresAt string) error {
	db, err := s.databaseForKey(row.CompactID)
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `UPDATE codex_context_compacts SET last_used_at = ?, updated_at = ?, expires_at = ? WHERE compact_id = ?`, now, now, refreshExpiresAt, row.CompactID); err != nil {
		return err
	}
	sessionDB, err := s.databaseForKey(row.SessionID)
	if err != nil {
		return err
	}
	_, err = sessionDB.ExecContext(ctx, `UPDATE codex_context_sessions SET last_used_at = ?, updated_at = ?, expires_at = ? WHERE id = ?`, now, now, refreshExpiresAt, row.SessionID)
	return err
}

// Close releases every open shard database.
func (s *SQLiteShardContextStateStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var first error
	for shardIndex, db := range s.dbs {
		if err := db.Close(); err != nil && first == nil {
			first = err
		}
		delete(s.dbs, shardIndex)
	}
	return first
}

// ---------------------------------------------------------------------------
// postgres driver (sqlpool mode)
// ---------------------------------------------------------------------------

// PostgresContextStateStore mirrors the node:postgres driver rows. The Node
// dialect qualifies every codex context table into the juhe_codex_context
// schema.
type PostgresContextStateStore struct {
	db *sql.DB
}

// NewPostgresContextStateStore builds the postgres driver over an acquired
// sqlpool handle.
func NewPostgresContextStateStore(handle *sqlpool.Handle) (*PostgresContextStateStore, error) {
	if handle == nil || handle.DB() == nil {
		return nil, errors.New("codex context postgres pool 未初始化")
	}
	return &PostgresContextStateStore{db: handle.DB()}, nil
}

func codexContextTablePostgres(tableName string) string {
	return `juhe_codex_context.` + tableName
}

// SaveResponseStateRow implements CodexContextRowStore.
func (s *PostgresContextStateStore) SaveResponseStateRow(ctx context.Context, row CodexContextResponseStateIndex) error {
	return withSQLTx(ctx, s.db, func(tx *sql.Tx) error {
		if err := upsertSessionRowPostgres(ctx, tx, sessionUpsertInputFromResponse(row)); err != nil {
			return err
		}
		return upsertResponseStateRowPostgres(ctx, tx, &row)
	})
}

// SaveCompactStateRow implements CodexContextRowStore.
func (s *PostgresContextStateStore) SaveCompactStateRow(ctx context.Context, row CodexContextCompactStateIndex) error {
	return withSQLTx(ctx, s.db, func(tx *sql.Tx) error {
		if err := upsertSessionRowPostgres(ctx, tx, sessionUpsertInputFromCompact(row)); err != nil {
			return err
		}
		return upsertCompactStateRowPostgres(ctx, tx, &row)
	})
}

// ReadResponseStateRow implements CodexContextRowStore.
func (s *PostgresContextStateStore) ReadResponseStateRow(ctx context.Context, responseID string) (*CodexContextResponseStateIndex, error) {
	row := s.db.QueryRowContext(ctx, `SELECT * FROM `+codexContextTablePostgres("codex_context_responses")+` WHERE response_id = $1 LIMIT 1`, responseID)
	return scanResponseStateRow(row)
}

// ReadCompactStateRow implements CodexContextRowStore.
func (s *PostgresContextStateStore) ReadCompactStateRow(ctx context.Context, compactID string) (*CodexContextCompactStateIndex, error) {
	row := s.db.QueryRowContext(ctx, `SELECT * FROM `+codexContextTablePostgres("codex_context_compacts")+` WHERE compact_id = $1 LIMIT 1`, compactID)
	return scanCompactStateRow(row)
}

// TouchResponseChain implements CodexContextRowStore.
func (s *PostgresContextStateStore) TouchResponseChain(ctx context.Context, rows []CodexContextResponseStateIndex, now, refreshExpiresAt string) error {
	if len(rows) == 0 {
		return nil
	}
	return withSQLTx(ctx, s.db, func(tx *sql.Tx) error {
		sessionID := ""
		if len(rows) > 0 {
			sessionID = rows[0].SessionID
		}
		if sessionID != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE `+codexContextTablePostgres("codex_context_sessions")+` SET last_used_at = $1, updated_at = $1, expires_at = $2 WHERE id = $3`, now, refreshExpiresAt, sessionID); err != nil {
				return err
			}
		}
		for _, row := range rows {
			if _, err := tx.ExecContext(ctx, `UPDATE `+codexContextTablePostgres("codex_context_responses")+` SET last_used_at = $1, updated_at = $1, expires_at = $2 WHERE response_id = $3`, now, refreshExpiresAt, row.ResponseID); err != nil {
				return err
			}
		}
		return nil
	})
}

// TouchCompact implements CodexContextRowStore.
func (s *PostgresContextStateStore) TouchCompact(ctx context.Context, row CodexContextCompactStateIndex, now, refreshExpiresAt string) error {
	return withSQLTx(ctx, s.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE `+codexContextTablePostgres("codex_context_compacts")+` SET last_used_at = $1, updated_at = $1, expires_at = $2 WHERE compact_id = $3`, now, refreshExpiresAt, row.CompactID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE `+codexContextTablePostgres("codex_context_sessions")+` SET last_used_at = $1, updated_at = $1, expires_at = $2 WHERE id = $3`, now, refreshExpiresAt, row.SessionID); err != nil {
			return err
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// shared row helpers
// ---------------------------------------------------------------------------

type sessionUpsertInput struct {
	sessionID        string
	boundary         CodexContextStateBoundary
	sourceResponseID string
	latestResponseID string
	latestCompactID  string
	now              string
	expiresAt        string
}

func sessionUpsertInputFromResponse(row CodexContextResponseStateIndex) sessionUpsertInput {
	sourceResponseID := ""
	if row.PreviousResponseID == "" {
		sourceResponseID = row.ResponseID
	}
	return sessionUpsertInput{
		sessionID:        row.SessionID,
		boundary:         row.CodexContextStateBoundary,
		sourceResponseID: sourceResponseID,
		latestResponseID: row.ResponseID,
		now:              row.UpdatedAt,
		expiresAt:        row.ExpiresAt,
	}
}

func sessionUpsertInputFromCompact(row CodexContextCompactStateIndex) sessionUpsertInput {
	return sessionUpsertInput{
		sessionID:       row.SessionID,
		boundary:        row.CodexContextStateBoundary,
		latestCompactID: row.CompactID,
		now:             row.UpdatedAt,
		expiresAt:       row.ExpiresAt,
	}
}

const codexContextSessionUpsertSQL = `
INSERT INTO %s (
  id, system_account_id, api_key_id, group_id, provider_code,
  source_response_id, latest_response_id, latest_compact_id,
  created_at, updated_at, last_used_at, expires_at
)
VALUES (%s)
ON CONFLICT(id) DO UPDATE SET
  source_response_id = COALESCE(codex_context_sessions.source_response_id, excluded.source_response_id),
  latest_response_id = COALESCE(excluded.latest_response_id, codex_context_sessions.latest_response_id),
  latest_compact_id = COALESCE(excluded.latest_compact_id, codex_context_sessions.latest_compact_id),
  updated_at = excluded.updated_at,
  last_used_at = excluded.last_used_at,
  expires_at = excluded.expires_at
`

func upsertSessionRow(ctx context.Context, tx *sql.Tx, input sessionUpsertInput) error {
	values := sessionUpsertValues(input)
	_, err := tx.ExecContext(ctx, fmt.Sprintf(codexContextSessionUpsertSQL, "codex_context_sessions", sqlPlaceholders(12)), values...)
	return err
}

func upsertSessionRowPostgres(ctx context.Context, tx *sql.Tx, input sessionUpsertInput) error {
	values := sessionUpsertValues(input)
	_, err := tx.ExecContext(ctx, fmt.Sprintf(codexContextSessionUpsertSQL, codexContextTablePostgres("codex_context_sessions"), postgresPlaceholders(12)), values...)
	return err
}

func sessionUpsertValues(input sessionUpsertInput) []any {
	return []any{
		input.sessionID,
		input.boundary.SystemAccountID,
		nullableText(input.boundary.APIKeyID),
		input.boundary.GroupID,
		input.boundary.ProviderCode,
		nullableText(input.sourceResponseID),
		nullableText(input.latestResponseID),
		nullableText(input.latestCompactID),
		input.now,
		input.now,
		input.now,
		input.expiresAt,
	}
}

func upsertResponseStateRow(ctx context.Context, tx *sql.Tx, row *CodexContextResponseStateIndex) error {
	values := responseStateValues(row)
	_, err := tx.ExecContext(ctx, fmt.Sprintf(codexContextResponseUpsertSQL, "codex_context_responses", sqlPlaceholders(21)), values...)
	return err
}

func upsertResponseStateRowPostgres(ctx context.Context, tx *sql.Tx, row *CodexContextResponseStateIndex) error {
	values := responseStateValues(row)
	_, err := tx.ExecContext(ctx, fmt.Sprintf(codexContextResponseUpsertSQL, codexContextTablePostgres("codex_context_responses"), postgresPlaceholders(21)), values...)
	return err
}

const codexContextResponseUpsertSQL = `
INSERT INTO %s (
  response_id, session_id, previous_response_id, system_account_id, api_key_id, group_id,
  provider_code,
  upstream_account_id, model, upstream_model, storage_key, storage_offset_bytes, sha256,
  raw_size_bytes, compressed_size_bytes, compression, schema_version,
  created_at, updated_at, last_used_at, expires_at
)
VALUES (%s)
ON CONFLICT(response_id) DO UPDATE SET
  session_id = excluded.session_id,
  previous_response_id = excluded.previous_response_id,
  upstream_account_id = excluded.upstream_account_id,
  model = excluded.model,
  upstream_model = excluded.upstream_model,
  storage_key = excluded.storage_key,
  storage_offset_bytes = excluded.storage_offset_bytes,
  sha256 = excluded.sha256,
  raw_size_bytes = excluded.raw_size_bytes,
  compressed_size_bytes = excluded.compressed_size_bytes,
  compression = excluded.compression,
  schema_version = excluded.schema_version,
  updated_at = excluded.updated_at,
  last_used_at = excluded.last_used_at,
  expires_at = excluded.expires_at
`

func responseStateValues(row *CodexContextResponseStateIndex) []any {
	return []any{
		row.ResponseID,
		row.SessionID,
		nullableText(row.PreviousResponseID),
		row.SystemAccountID,
		nullableText(row.APIKeyID),
		row.GroupID,
		row.ProviderCode,
		nullableText(row.UpstreamAccountID),
		nullableText(row.Model),
		nullableText(row.UpstreamModel),
		row.StorageKey,
		row.StorageOffsetBytes,
		row.SHA256,
		row.RawSizeBytes,
		row.CompressedSizeBytes,
		row.Compression,
		row.SchemaVersion,
		row.CreatedAt,
		row.UpdatedAt,
		row.LastUsedAt,
		row.ExpiresAt,
	}
}

func upsertCompactStateRow(ctx context.Context, tx *sql.Tx, row *CodexContextCompactStateIndex) error {
	values := compactStateValues(row)
	_, err := tx.ExecContext(ctx, fmt.Sprintf(codexContextCompactUpsertSQL, "codex_context_compacts", sqlPlaceholders(22)), values...)
	return err
}

func upsertCompactStateRowPostgres(ctx context.Context, tx *sql.Tx, row *CodexContextCompactStateIndex) error {
	values := compactStateValues(row)
	_, err := tx.ExecContext(ctx, fmt.Sprintf(codexContextCompactUpsertSQL, codexContextTablePostgres("codex_context_compacts"), postgresPlaceholders(22)), values...)
	return err
}

const codexContextCompactUpsertSQL = `
INSERT INTO %s (
  compact_id, session_id, source_response_id, summary_digest, system_account_id, api_key_id,
  group_id, provider_code,
  upstream_account_id, model, upstream_model, storage_key, storage_offset_bytes, sha256,
  raw_size_bytes, compressed_size_bytes, compression, schema_version,
  created_at, updated_at, last_used_at, expires_at
)
VALUES (%s)
ON CONFLICT(compact_id) DO UPDATE SET
  session_id = excluded.session_id,
  source_response_id = excluded.source_response_id,
  summary_digest = excluded.summary_digest,
  upstream_account_id = excluded.upstream_account_id,
  model = excluded.model,
  upstream_model = excluded.upstream_model,
  storage_key = excluded.storage_key,
  storage_offset_bytes = excluded.storage_offset_bytes,
  sha256 = excluded.sha256,
  raw_size_bytes = excluded.raw_size_bytes,
  compressed_size_bytes = excluded.compressed_size_bytes,
  compression = excluded.compression,
  schema_version = excluded.schema_version,
  updated_at = excluded.updated_at,
  last_used_at = excluded.last_used_at,
  expires_at = excluded.expires_at
`

func compactStateValues(row *CodexContextCompactStateIndex) []any {
	return []any{
		row.CompactID,
		row.SessionID,
		nullableText(row.SourceResponseID),
		row.SummaryDigest,
		row.SystemAccountID,
		nullableText(row.APIKeyID),
		row.GroupID,
		row.ProviderCode,
		nullableText(row.UpstreamAccountID),
		nullableText(row.Model),
		nullableText(row.UpstreamModel),
		row.StorageKey,
		row.StorageOffsetBytes,
		row.SHA256,
		row.RawSizeBytes,
		row.CompressedSizeBytes,
		row.Compression,
		row.SchemaVersion,
		row.CreatedAt,
		row.UpdatedAt,
		row.LastUsedAt,
		row.ExpiresAt,
	}
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func sqlPlaceholders(count int) string {
	parts := make([]string, count)
	for index := range parts {
		parts[index] = "?"
	}
	return strings.Join(parts, ", ")
}

func postgresPlaceholders(count int) string {
	parts := make([]string, count)
	for index := range parts {
		parts[index] = "$" + strconv.Itoa(index+1)
	}
	return strings.Join(parts, ", ")
}

func withSQLTx(ctx context.Context, db *sql.DB, operation func(tx *sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := operation(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// The codex context schema mirrors storage/schema/codex-context-state-schema.ts
// (sqlite DDL); the postgres migration owns the equivalent SQL and the
// runtime statements are compatible with both dialects.
const codexContextStateSchemaSQL = `
CREATE TABLE IF NOT EXISTS codex_context_sessions (
  id TEXT PRIMARY KEY,
  system_account_id TEXT NOT NULL,
  api_key_id TEXT,
  group_id TEXT NOT NULL,
  provider_code TEXT NOT NULL,
  source_response_id TEXT,
  latest_response_id TEXT,
  latest_compact_id TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  last_used_at TEXT NOT NULL,
  expires_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS codex_context_responses (
  response_id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  previous_response_id TEXT,
  system_account_id TEXT NOT NULL,
  api_key_id TEXT,
  group_id TEXT NOT NULL,
  provider_code TEXT NOT NULL,
  upstream_account_id TEXT,
  model TEXT,
  upstream_model TEXT,
  storage_key TEXT NOT NULL,
  storage_offset_bytes INTEGER NOT NULL,
  sha256 TEXT NOT NULL,
  raw_size_bytes INTEGER NOT NULL,
  compressed_size_bytes INTEGER NOT NULL,
  compression TEXT NOT NULL DEFAULT 'gzip',
  schema_version INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  last_used_at TEXT NOT NULL,
  expires_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS codex_context_compacts (
  compact_id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  source_response_id TEXT,
  summary_digest TEXT NOT NULL,
  system_account_id TEXT NOT NULL,
  api_key_id TEXT,
  group_id TEXT NOT NULL,
  provider_code TEXT NOT NULL,
  upstream_account_id TEXT,
  model TEXT,
  upstream_model TEXT,
  storage_key TEXT NOT NULL,
  storage_offset_bytes INTEGER NOT NULL,
  sha256 TEXT NOT NULL,
  raw_size_bytes INTEGER NOT NULL,
  compressed_size_bytes INTEGER NOT NULL,
  compression TEXT NOT NULL DEFAULT 'gzip',
  schema_version INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  last_used_at TEXT NOT NULL,
  expires_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_codex_context_sessions_expires ON codex_context_sessions(expires_at ASC, id ASC);
CREATE INDEX IF NOT EXISTS idx_codex_context_sessions_last_used ON codex_context_sessions(last_used_at ASC, id ASC);
CREATE INDEX IF NOT EXISTS idx_codex_context_sessions_boundary ON codex_context_sessions(system_account_id, api_key_id, group_id, provider_code);
CREATE INDEX IF NOT EXISTS idx_codex_context_responses_session ON codex_context_responses(session_id, created_at ASC, response_id);
CREATE INDEX IF NOT EXISTS idx_codex_context_responses_previous ON codex_context_responses(previous_response_id) WHERE previous_response_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_codex_context_responses_expires ON codex_context_responses(expires_at ASC, response_id);
CREATE INDEX IF NOT EXISTS idx_codex_context_responses_boundary ON codex_context_responses(system_account_id, api_key_id, group_id, provider_code, response_id);
CREATE INDEX IF NOT EXISTS idx_codex_context_compacts_session ON codex_context_compacts(session_id, created_at ASC, compact_id);
CREATE INDEX IF NOT EXISTS idx_codex_context_compacts_source_response ON codex_context_compacts(source_response_id);
CREATE INDEX IF NOT EXISTS idx_codex_context_compacts_expires ON codex_context_compacts(expires_at ASC, compact_id);
CREATE INDEX IF NOT EXISTS idx_codex_context_compacts_boundary ON codex_context_compacts(system_account_id, api_key_id, group_id, provider_code, compact_id);
`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanResponseStateRow(row rowScanner) (*CodexContextResponseStateIndex, error) {
	index := &CodexContextResponseStateIndex{}
	var apiKeyID, previousResponseID, upstreamAccountID, model, upstreamModel sql.NullString
	err := row.Scan(
		&index.ResponseID,
		&index.SessionID,
		&previousResponseID,
		&index.SystemAccountID,
		&apiKeyID,
		&index.GroupID,
		&index.ProviderCode,
		&upstreamAccountID,
		&model,
		&upstreamModel,
		&index.StorageKey,
		&index.StorageOffsetBytes,
		&index.SHA256,
		&index.RawSizeBytes,
		&index.CompressedSizeBytes,
		&index.Compression,
		&index.SchemaVersion,
		&index.CreatedAt,
		&index.UpdatedAt,
		&index.LastUsedAt,
		&index.ExpiresAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	index.PreviousResponseID = previousResponseID.String
	index.APIKeyID = apiKeyID.String
	index.UpstreamAccountID = upstreamAccountID.String
	index.Model = model.String
	index.UpstreamModel = upstreamModel.String
	return index, nil
}

func scanCompactStateRow(row rowScanner) (*CodexContextCompactStateIndex, error) {
	index := &CodexContextCompactStateIndex{}
	var sourceResponseID, apiKeyID, upstreamAccountID, model, upstreamModel sql.NullString
	err := row.Scan(
		&index.CompactID,
		&index.SessionID,
		&sourceResponseID,
		&index.SummaryDigest,
		&index.SystemAccountID,
		&apiKeyID,
		&index.GroupID,
		&index.ProviderCode,
		&upstreamAccountID,
		&model,
		&upstreamModel,
		&index.StorageKey,
		&index.StorageOffsetBytes,
		&index.SHA256,
		&index.RawSizeBytes,
		&index.CompressedSizeBytes,
		&index.Compression,
		&index.SchemaVersion,
		&index.CreatedAt,
		&index.UpdatedAt,
		&index.LastUsedAt,
		&index.ExpiresAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	index.SourceResponseID = sourceResponseID.String
	index.APIKeyID = apiKeyID.String
	index.UpstreamAccountID = upstreamAccountID.String
	index.Model = model.String
	index.UpstreamModel = upstreamModel.String
	return index, nil
}

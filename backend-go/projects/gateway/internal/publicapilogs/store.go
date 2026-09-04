// Store ported from storage/public-api-logs.repository.ts: dual-mode
// (SQLite + PostgreSQL) persistence for public_api_logs. The insert shape
// matches Node exactly — SQLite wraps the whole batch in one transaction over
// a single prepared INSERT, PostgreSQL inserts 1000-row multi-VALUE chunks
// inside one transaction with ON CONFLICT(id) DO NOTHING for at-least-once
// idempotency. CleanupBefore keeps the Node repository semantics: candidates
// are chosen by created_at ASC, id ASC with a hard LIMIT, then deleted by id
// inside a transaction (the W7 contract allows this equivalent transactional
// form).
package publicapilogs

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"
)

const (
	// publicApiLogPostgresRowsPerInsert mirrors publicApiLogPostgresRowsPerInsert.
	publicApiLogPostgresRowsPerInsert = 1000
)

// Store is the dual-mode public API log writer persistence.
type Store struct {
	db    *sql.DB
	pg    bool
	now   func() time.Time
	newID func(prefix string) string
}

// NewStore builds the store over the dataset database handle.
func NewStore(db *sql.DB, postgres bool, now func() time.Time, newID func(string) string) (*Store, error) {
	if db == nil {
		return nil, errors.New("publicapilogs store requires a dataset database")
	}
	if now == nil {
		now = time.Now
	}
	if newID == nil {
		newID = func(prefix string) string { return newRecordID(prefix, now) }
	}
	return &Store{db: db, pg: postgres, now: now, newID: newID}, nil
}

func (s *Store) table(name string) string {
	if s.pg {
		return "juhe_dataset." + name
	}
	return name
}

// bind converts ? placeholders to PostgreSQL $n ordinals.
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

const publicApiLogInsertColumns = `id, trace_id, source_ref_id, source_name, token_id, token_name, token_prefix, is_test_token,
	method, path, query_string, client_ip, user_agent, status_code, success, duration_ms,
	request_size_bytes, response_size_bytes, request_capture_status, response_capture_status,
	request_data_json, response_data_json, error_code, error_message, started_at, ended_at, created_at`

// InsertBatch mirrors createPublicApiLogsBatch / createPublicApiLogsBatchAsync.
// Empty batches are a no-op.
func (s *Store) InsertBatch(ctx context.Context, inputs []Input) error {
	if len(inputs) == 0 {
		return nil
	}
	ctx = ensureCtx(ctx)
	rows := make([]normalized, 0, len(inputs))
	for _, input := range inputs {
		rows = append(rows, normalizeInput(input, s.now, s.newID))
	}
	if s.pg {
		return s.insertBatchPostgres(ctx, rows)
	}
	return s.insertBatchSQLite(ctx, rows)
}

func (s *Store) insertBatchSQLite(ctx context.Context, rows []normalized) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	insert, err := tx.PrepareContext(ctx, s.bind(`INSERT INTO `+s.table("public_api_logs")+` (
		`+publicApiLogInsertColumns+`
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`))
	if err != nil {
		return err
	}
	defer insert.Close()
	for _, row := range rows {
		if _, err := insert.ExecContext(ctx, normalizedArgs(row)...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) insertBatchPostgres(ctx context.Context, rows []normalized) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for start := 0; start < len(rows); start += publicApiLogPostgresRowsPerInsert {
		end := start + publicApiLogPostgresRowsPerInsert
		if end > len(rows) {
			end = len(rows)
		}
		chunk := rows[start:end]
		query := s.bind(`INSERT INTO ` + s.table("public_api_logs") + ` (
		` + publicApiLogInsertColumns + `
		) VALUES ` + multiRowPlaceholders(len(chunk), 27) + `
		ON CONFLICT(id) DO NOTHING`)
		args := make([]any, 0, len(chunk)*27)
		for _, row := range chunk {
			args = append(args, normalizedArgs(row)...)
		}
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func normalizedArgs(row normalized) []any {
	return []any{
		row.id,
		row.traceID,
		row.sourceRefID,
		row.sourceName,
		row.tokenID,
		row.tokenName,
		row.tokenPrefix,
		row.isTestToken,
		row.method,
		row.path,
		row.queryString,
		row.clientIP,
		row.userAgent,
		row.statusCode,
		row.success,
		row.durationMS,
		row.requestSizeBytes,
		row.responseSizeBytes,
		row.requestCaptureStatus,
		row.responseCaptureStatus,
		row.requestDataJSON,
		row.responseDataJSON,
		row.errorCode,
		row.errorMessage,
		row.startedAt,
		row.endedAt,
		row.createdAt,
	}
}

func multiRowPlaceholders(rowCount, columnCount int) string {
	row := "(" + strings.TrimSuffix(strings.Repeat("?, ", columnCount), ", ") + ")"
	rows := make([]string, 0, rowCount)
	for i := 0; i < rowCount; i++ {
		rows = append(rows, row)
	}
	return strings.Join(rows, ", ")
}

// CleanupBefore mirrors cleanupPublicApiLogsBefore: at most limit rows are
// chosen by (created_at ASC, id ASC) among records strictly older than the
// cutoff, then deleted by id inside one transaction. Returns the deleted row
// count.
func (s *Store) CleanupBefore(ctx context.Context, cutoffCreatedAt string, limit int) (int, error) {
	ctx = ensureCtx(ctx)
	if limit < 1 {
		limit = 1
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT id FROM `+s.table("public_api_logs")+`
		WHERE created_at < ?
		ORDER BY created_at ASC, id ASC
		LIMIT ?`), cutoffCreatedAt, limit)
	if err != nil {
		return 0, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		if id != "" {
			ids = append(ids, id)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	if len(ids) == 0 {
		return 0, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	result, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("public_api_logs")+` WHERE id IN (`+strings.Join(placeholders, ", ")+`)`), args...)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return len(ids), nil
	}
	return int(deleted), nil
}

func ensureCtx(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

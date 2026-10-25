package cleanuprepo

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strings"
	"time"
)

// codex-context-state.repository.ts 的清理侧移植：过期会话批清理
// （cleanupExpiredCodexContextStates / Async）与 storage cleanup queue 结算
// （settleCodexContextStorageCleanup / Async）。SQLite 为多分片库
// （state-<i>.sqlite3），PostgreSQL 为 juhe_codex_context 单 schema。

// CodexContextStore 承载 codex context 状态清理访问。
type CodexContextStore struct {
	// PG 是 postgres 模式的共享句柄。
	PG *DB
	// Now 注入时钟（nil 取 time.Now）。
	Now func() time.Time
	// RetryJitter 注入 passiveScheduleDelayMs 抖动（nil 取 0；测试可固定）。
	RetryJitter func(delayMs int64) int64
}

func (s *CodexContextStore) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *CodexContextStore) nowIso() string { return ISOOf(s.now()) }

// ISOOf 格式化 Node toISOString 等价串。
func ISOOf(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

func (s *CodexContextStore) jitter(delayMs int64) int64 {
	if s.RetryJitter == nil {
		return 0
	}
	return s.RetryJitter(delayMs)
}

// ExpiredCleanupResult 照 CodexContextExpiredStateCleanupResult。
type ExpiredCleanupResult struct {
	DeletedSessions  int64
	DeletedResponses int64
	DeletedCompacts  int64
	StorageKeys      []string
	HasMore          bool
}

// CleanupExpiredStates 照 cleanupExpiredCodexContextStates / Async。
func (s *CodexContextStore) CleanupExpiredStates(ctx context.Context, expiredBefore string, limit int) (*ExpiredCleanupResult, error) {
	expiredBefore = strings.TrimSpace(expiredBefore)
	if expiredBefore == "" {
		expiredBefore = s.nowIso()
	}
	normalizedLimit := batchLimit(limit)
	if normalizedLimit > 10000 {
		normalizedLimit = 10000
	}
	return s.cleanupExpiredPostgres(ctx, expiredBefore, normalizedLimit)
}

type expiredSession struct {
	ID         string
	ExpiresAt  string
	ShardIndex int
}

type pendingKeys struct {
	StorageKeys []string
	HasMore     bool
}

func deleteStorageCleanupQueueRows(ctx context.Context, tx *sql.Tx, storageKeys []string) (int64, error) {
	var deleted int64
	for _, chunk := range chunkValues(storageKeys, 900) {
		// chunkValues 不产出空块，空块守卫已按 w13e 收尾授权删除。
		result, err := tx.ExecContext(ctx, fmt.Sprintf(
			`DELETE FROM codex_context_storage_cleanup_queue WHERE storage_key IN (%s)`, placeholderList(len(chunk))),
			stringSliceToAny(chunk)...)
		if err != nil {
			return deleted, err
		}
		affected, err := changes(result)
		if err != nil {
			return deleted, err
		}
		deleted += affected
	}
	return deleted, nil
}

func (s *CodexContextStore) cleanupExpiredPostgres(ctx context.Context, expiredBefore string, limit int) (*ExpiredCleanupResult, error) {
	client := s.PG
	rows, err := client.QueryContext(ctx, client.Bind(`
      SELECT id, expires_at
      FROM juhe_codex_context.codex_context_sessions
      WHERE expires_at < ?
      ORDER BY expires_at ASC, id ASC
      LIMIT ?
	`), expiredBefore, limit+1)
	if err != nil {
		return nil, err
	}
	var sessions []expiredSession
	for rows.Next() {
		var id, expiresAt sql.NullString
		if err := rows.Scan(&id, &expiresAt); err != nil {
			rows.Close()
			return nil, err
		}
		sessions = append(sessions, expiredSession{ID: id.String, ExpiresAt: expiresAt.String})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if len(sessions) == 0 {
		pending, err := s.selectPendingStorageKeysPostgres(ctx, limit)
		if err != nil {
			return nil, err
		}
		return &ExpiredCleanupResult{StorageKeys: pending.StorageKeys, HasMore: pending.HasMore}, nil
	}
	hasMore := len(sessions) > limit
	if hasMore {
		sessions = sessions[:limit]
	}
	sessionIDs := make([]string, 0, len(sessions))
	for _, session := range sessions {
		sessionIDs = append(sessionIDs, session.ID)
	}
	var deletedSessions, deletedResponses, deletedCompacts int64
	tx, err := client.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	deletedResponses, err = s.deleteExpiredRowsBySessionIDsPostgres(ctx, tx, "codex_context_responses", sessionIDs, expiredBefore)
	if err != nil {
		return nil, err
	}
	deletedCompacts, err = s.deleteExpiredRowsBySessionIDsPostgres(ctx, tx, "codex_context_compacts", sessionIDs, expiredBefore)
	if err != nil {
		return nil, err
	}
	remainingExpiresAt, err := selectRemainingExpiresAtPostgres(ctx, tx, sessionIDs)
	if err != nil {
		return nil, err
	}
	for _, session := range sessions {
		if remaining, ok := remainingExpiresAt[session.ID]; ok {
			if _, err := tx.ExecContext(ctx, client.Bind(`
          UPDATE juhe_codex_context.codex_context_sessions
          SET updated_at = ?, expires_at = ?
          WHERE id = ?
			`), s.nowIso(), remaining, session.ID); err != nil {
				return nil, err
			}
			continue
		}
		result, err := tx.ExecContext(ctx, client.Bind(
			`DELETE FROM juhe_codex_context.codex_context_sessions WHERE id = ?`), session.ID)
		if err != nil {
			return nil, err
		}
		affected, err := changes(result)
		if err != nil {
			return nil, err
		}
		deletedSessions += affected
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	pending, err := s.selectPendingStorageKeysPostgres(ctx, limit)
	if err != nil {
		return nil, err
	}
	return &ExpiredCleanupResult{
		DeletedSessions:  deletedSessions,
		DeletedResponses: deletedResponses,
		DeletedCompacts:  deletedCompacts,
		StorageKeys:      pending.StorageKeys,
		HasMore:          hasMore || pending.HasMore,
	}, nil
}

func (s *CodexContextStore) deleteExpiredRowsBySessionIDsPostgres(ctx context.Context, tx *sql.Tx, table string, sessionIDs []string, expiredBefore string) (int64, error) {
	var deleted int64
	now := s.nowIso()
	for _, chunk := range chunkValues(sessionIDs, 900) {
		query := fmt.Sprintf(`
      SELECT storage_key
      FROM juhe_codex_context.%s
      WHERE session_id IN (%s) AND expires_at < ?
		`, table, placeholderList(len(chunk)))
		rows, err := tx.QueryContext(ctx, (&DB{Postgres: true}).Bind(query), append(stringSliceToAny(chunk), expiredBefore)...)
		if err != nil {
			return deleted, err
		}
		var storageKeys []string
		for rows.Next() {
			var storageKey sql.NullString
			if err := rows.Scan(&storageKey); err != nil {
				rows.Close()
				return deleted, err
			}
			deleted++
			storageKeys = append(storageKeys, storageKey.String)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return deleted, err
		}
		rows.Close()
		if err := enqueueStorageCleanupKeysPostgres(ctx, tx, now, storageKeys); err != nil {
			return deleted, err
		}
		result, err := tx.ExecContext(ctx, (&DB{Postgres: true}).Bind(fmt.Sprintf(
			`DELETE FROM juhe_codex_context.%s WHERE session_id IN (%s) AND expires_at < ?`, table, placeholderList(len(chunk)))),
			append(stringSliceToAny(chunk), expiredBefore)...)
		if err != nil {
			return deleted, err
		}
		if _, err := changes(result); err != nil {
			return deleted, err
		}
	}
	return deleted, nil
}

func enqueueStorageCleanupKeysPostgres(ctx context.Context, tx *sql.Tx, now string, storageKeys []string) error {
	keys := uniqueNonEmpty(storageKeys)
	if len(keys) == 0 {
		return nil
	}
	for _, chunk := range chunkValues(keys, 200) {
		values := make([]any, 0, len(chunk)*4)
		placeholders := make([]string, 0, len(chunk))
		for _, storageKey := range chunk {
			values = append(values, storageKey, now, now, now)
			placeholders = append(placeholders, "(?, ?, ?, ?, 0, NULL)")
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
      INSERT INTO juhe_codex_context.codex_context_storage_cleanup_queue (
        storage_key, enqueued_at, updated_at, next_attempt_at, attempt_count, last_error
      ) VALUES %s
      ON CONFLICT(storage_key) DO NOTHING
		`, strings.Join(placeholders, ", ")), values...); err != nil {
			return err
		}
	}
	return nil
}

func selectRemainingExpiresAtPostgres(ctx context.Context, tx *sql.Tx, sessionIDs []string) (map[string]string, error) {
	expiresAtBySessionID := map[string]string{}
	bind := (&DB{Postgres: true}).Bind
	for _, table := range []string{"codex_context_responses", "codex_context_compacts"} {
		for _, chunk := range chunkValues(sessionIDs, 900) {
			query := bind(fmt.Sprintf(`
        SELECT session_id, MAX(expires_at) AS expires_at
        FROM juhe_codex_context.%s
        WHERE session_id IN (%s)
        GROUP BY session_id
			`, table, placeholderList(len(chunk))))
			rows, err := tx.QueryContext(ctx, query, stringSliceToAny(chunk)...)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var sessionID, expiresAt sql.NullString
				if err := rows.Scan(&sessionID, &expiresAt); err != nil {
					rows.Close()
					return nil, err
				}
				sessionIDText := strings.TrimSpace(sessionID.String)
				expiresAtText := strings.TrimSpace(expiresAt.String)
				if sessionIDText == "" || expiresAtText == "" {
					continue
				}
				if existing, ok := expiresAtBySessionID[sessionIDText]; !ok || expiresAtText > existing {
					expiresAtBySessionID[sessionIDText] = expiresAtText
				}
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return nil, err
			}
			rows.Close()
		}
	}
	return expiresAtBySessionID, nil
}

func (s *CodexContextStore) selectPendingStorageKeysPostgres(ctx context.Context, limit int) (pendingKeys, error) {
	normalizedLimit := batchLimit(limit)
	if normalizedLimit > 10000 {
		normalizedLimit = 10000
	}
	rows, err := s.PG.QueryContext(ctx, `
      SELECT storage_key
      FROM juhe_codex_context.codex_context_storage_cleanup_queue
      WHERE next_attempt_at <= ?
      ORDER BY next_attempt_at ASC, enqueued_at ASC, storage_key ASC
      LIMIT ?
	`, s.nowIso(), normalizedLimit+1)
	if err != nil {
		return pendingKeys{}, err
	}
	defer rows.Close()
	var pending []string
	for rows.Next() {
		var storageKey sql.NullString
		if err := rows.Scan(&storageKey); err != nil {
			return pendingKeys{}, err
		}
		key := strings.TrimSpace(storageKey.String)
		if key != "" {
			pending = append(pending, key)
		}
	}
	if err := rows.Err(); err != nil {
		return pendingKeys{}, err
	}
	deletable, err := s.filterUnreferencedStorageKeysPostgres(ctx, pending)
	if err != nil {
		return pendingKeys{}, err
	}
	unreferencedSet := map[string]bool{}
	for _, key := range deletable {
		unreferencedSet[key] = true
	}
	var referenced []string
	for _, key := range pending {
		if !unreferencedSet[key] {
			referenced = append(referenced, key)
		}
	}
	if len(referenced) > 0 {
		tx, err := s.PG.BeginTx(ctx, nil)
		if err != nil {
			return pendingKeys{}, err
		}
		if _, err := deleteStorageCleanupQueueRows(ctx, tx, referenced); err != nil {
			_ = tx.Rollback()
			return pendingKeys{}, err
		}
		if err := tx.Commit(); err != nil {
			return pendingKeys{}, err
		}
	}
	hasMore := len(deletable) > normalizedLimit || len(pending) > normalizedLimit
	if len(deletable) > normalizedLimit {
		deletable = deletable[:normalizedLimit]
	}
	return pendingKeys{StorageKeys: deletable, HasMore: hasMore}, nil
}

func (s *CodexContextStore) filterUnreferencedStorageKeysPostgres(ctx context.Context, keys []string) ([]string, error) {
	deletable := map[string]bool{}
	for _, key := range keys {
		deletable[key] = true
	}
	if len(deletable) == 0 {
		return []string{}, nil
	}
	for _, table := range []string{"codex_context_responses", "codex_context_compacts"} {
		all := make([]string, 0, len(deletable))
		for key := range deletable {
			all = append(all, key)
		}
		for _, chunk := range chunkValues(all, 900) {
			var remaining []string
			for _, key := range chunk {
				if deletable[key] {
					remaining = append(remaining, key)
				}
			}
			if len(remaining) == 0 {
				continue
			}
			query := fmt.Sprintf(`
        SELECT DISTINCT storage_key
        FROM juhe_codex_context.%s
        WHERE storage_key IN (%s)
			`, table, placeholderList(len(remaining)))
			rows, err := s.PG.QueryContext(ctx, query, stringSliceToAny(remaining)...)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var storageKey sql.NullString
				if err := rows.Scan(&storageKey); err != nil {
					rows.Close()
					return nil, err
				}
				key := strings.TrimSpace(storageKey.String)
				if key != "" {
					delete(deletable, key)
				}
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return nil, err
			}
			rows.Close()
		}
	}
	output := make([]string, 0, len(deletable))
	for _, key := range keys {
		if deletable[key] {
			output = append(output, key)
		}
	}
	return output, nil
}

// Settlement 照 CodexContextStorageCleanupSettlement。
type Settlement struct {
	SucceededStorageKeys []string
	Failures             []SettlementFailure
	Now                  string
}

// SettlementFailure 照 CodexContextStorageCleanupFailure。
type SettlementFailure struct {
	StorageKey string
	Error      string
}

// SettlementResult 照 CodexContextStorageCleanupSettlementResult。
type SettlementResult struct {
	Acknowledged int64
	Deferred     int64
}

// SettleStorageCleanup 照 settleCodexContextStorageCleanup / Async。
func (s *CodexContextStore) SettleStorageCleanup(ctx context.Context, settlement Settlement) (SettlementResult, error) {
	succeededKeys := uniqueNonEmpty(settlement.SucceededStorageKeys)
	failures := normalizedFailures(settlement.Failures)
	now := settlement.Now
	if strings.TrimSpace(now) == "" {
		now = s.nowIso()
	}
	tx, err := s.PG.BeginTx(ctx, nil)
	if err != nil {
		return SettlementResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	acknowledged, err := deleteStorageCleanupQueueRows(ctx, tx, succeededKeys)
	if err != nil {
		return SettlementResult{}, err
	}
	deferred, err := s.deferStorageCleanupRowsPostgres(ctx, tx, failures, now)
	if err != nil {
		return SettlementResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return SettlementResult{}, err
	}
	return SettlementResult{Acknowledged: acknowledged, Deferred: deferred}, nil
}

func normalizedFailures(failures []SettlementFailure) []SettlementFailure {
	output := make([]SettlementFailure, 0, len(failures))
	for _, failure := range failures {
		key := strings.TrimSpace(failure.StorageKey)
		message := failure.Error
		if key == "" || message == "" {
			continue
		}
		output = append(output, SettlementFailure{StorageKey: key, Error: message})
	}
	return output
}

func (s *CodexContextStore) deferStorageCleanupRowsPostgres(ctx context.Context, tx *sql.Tx, failures []SettlementFailure, now string) (int64, error) {
	var deferred int64
	for _, failure := range failures {
		var attemptCount sql.NullInt64
		err := tx.QueryRowContext(ctx, `
      SELECT attempt_count
      FROM juhe_codex_context.codex_context_storage_cleanup_queue
      WHERE storage_key = ?
		`, failure.StorageKey).Scan(&attemptCount)
		if err != nil && err != sql.ErrNoRows {
			return deferred, err
		}
		nextAttempt := int(attemptCount.Int64) + 1
		result, err := tx.ExecContext(ctx, `
      UPDATE juhe_codex_context.codex_context_storage_cleanup_queue
      SET attempt_count = attempt_count + 1,
          last_error = ?,
          updated_at = ?,
          next_attempt_at = ?
      WHERE storage_key = ?
		`, failure.Error, now, storageCleanupRetryAt(s.now(), s.jitter, nextAttempt), failure.StorageKey)
		if err != nil {
			return deferred, err
		}
		affected, err := changes(result)
		if err != nil {
			return deferred, err
		}
		deferred += affected
	}
	return deferred, nil
}

// storageCleanupRetryAt 照 storageCleanupRetryAt：30s 指数退避，上限 6h，
// 外加 passiveScheduleDelayMs 抖动。
func storageCleanupRetryAt(now time.Time, jitter func(int64) int64, attemptCount int) string {
	const baseDelayMs = int64(30_000)
	const maxDelayMs = int64(6 * 60 * 60 * 1000)
	shift := attemptCount - 1
	if shift < 0 {
		shift = 0
	}
	if shift > 10 {
		shift = 10
	}
	delayMs := baseDelayMs << shift
	if delayMs > maxDelayMs || delayMs <= 0 {
		delayMs = maxDelayMs
	}
	if jitter != nil {
		extra := jitter(delayMs)
		if extra > 0 {
			if extra > math.MaxInt64-delayMs {
				delayMs = maxDelayMs
			} else {
				delayMs += extra
			}
		}
	}
	return ISOOf(now.Add(time.Duration(delayMs) * time.Millisecond))
}

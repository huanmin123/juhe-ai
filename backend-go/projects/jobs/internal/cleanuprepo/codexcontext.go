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
	// Postgres 为 true 时使用 PG；为 false 时使用 ShardRoot 下的分片 SQLite。
	Postgres bool
	// PG 是 postgres 模式的共享句柄。
	PG *DB
	// ShardRoot 是 JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT。
	ShardRoot string
	// ShardCount 是 JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT（1..256）。
	ShardCount int
	// Now 注入时钟（nil 取 time.Now）。
	Now func() time.Time
	// RetryJitter 注入 passiveScheduleDelayMs 抖动（nil 取 0；测试可固定）。
	RetryJitter func(delayMs int64) int64

	shards map[int]*sql.DB
}

// shardPath 照 codexContextStateShardPath：root/state-<NNN>.sqlite3。
func (s *CodexContextStore) shardPath(shardIndex int) string {
	return fmt.Sprintf("%s/state-%03d.sqlite3", s.ShardRoot, shardIndex)
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

func (s *CodexContextStore) shardIndexes() []int {
	count := s.ShardCount
	if count < 1 {
		count = 1
	}
	indexes := make([]int, 0, count)
	for index := 0; index < count; index++ {
		indexes = append(indexes, index)
	}
	return indexes
}

// shard 返回（并缓存）一个分片连接。
func (s *CodexContextStore) shard(shardIndex int) (*sql.DB, error) {
	if s.Postgres {
		return nil, fmt.Errorf("codex context 分片句柄仅在 SQLite 模式可用")
	}
	if cached, ok := s.shards[shardIndex]; ok {
		return cached, nil
	}
	db, err := sql.Open("sqlite", "file:"+s.shardPath(shardIndex)+"?_pragma=busy_timeout(5000)&_txlock=immediate")
	if err != nil {
		return nil, fmt.Errorf("open codex context shard sqlite 失败: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if s.shards == nil {
		s.shards = map[int]*sql.DB{}
	}
	s.shards[shardIndex] = db
	return db, nil
}

// Close 关闭全部分片连接。
func (s *CodexContextStore) Close() error {
	var firstErr error
	for index, db := range s.shards {
		if err := db.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		delete(s.shards, index)
	}
	return firstErr
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
	if s.Postgres {
		return s.cleanupExpiredPostgres(ctx, expiredBefore, normalizedLimit)
	}
	return s.cleanupExpiredSQLite(ctx, expiredBefore, normalizedLimit)
}

// selectExpiredSessions（SQLite）：跨分片取最旧的过期会话。
func (s *CodexContextStore) selectExpiredSessionsSQLite(ctx context.Context, expiredBefore string, limit int, shardIndexes []int) ([]expiredSession, bool, error) {
	var rows []expiredSession
	hasMore := false
	for _, shardIndex := range shardIndexes {
		remaining := limit - len(rows)
		if remaining <= 0 {
			hasMore = true
			break
		}
		db, err := s.shard(shardIndex)
		if err != nil {
			return nil, false, err
		}
		query := fmt.Sprintf(`
      SELECT id, expires_at
      FROM codex_context_sessions
      WHERE expires_at < ?
      ORDER BY expires_at ASC, id ASC
      LIMIT ?
		`)
		resultRows, err := db.QueryContext(ctx, query, expiredBefore, remaining+1)
		if err != nil {
			return nil, false, err
		}
		var shardRows []expiredSession
		for resultRows.Next() {
			var row expiredSession
			var id, expiresAt sql.NullString
			if err := resultRows.Scan(&id, &expiresAt); err != nil {
				resultRows.Close()
				return nil, false, err
			}
			row.ID = id.String
			row.ExpiresAt = expiresAt.String
			row.ShardIndex = shardIndex
			shardRows = append(shardRows, row)
		}
		if err := resultRows.Err(); err != nil {
			resultRows.Close()
			return nil, false, err
		}
		resultRows.Close()
		if len(shardRows) > remaining {
			hasMore = true
			shardRows = shardRows[:remaining]
		}
		rows = append(rows, shardRows...)
	}
	return rows, hasMore, nil
}

type expiredSession struct {
	ID         string
	ExpiresAt  string
	ShardIndex int
}

func (s *CodexContextStore) cleanupExpiredSQLite(ctx context.Context, expiredBefore string, limit int) (*ExpiredCleanupResult, error) {
	sessions, hasMore, err := s.selectExpiredSessionsSQLite(ctx, expiredBefore, limit, s.shardIndexes())
	if err != nil {
		return nil, err
	}
	if len(sessions) == 0 {
		pending, err := s.selectPendingStorageKeysSQLite(ctx, limit)
		if err != nil {
			return nil, err
		}
		return &ExpiredCleanupResult{StorageKeys: pending.StorageKeys, HasMore: pending.HasMore}, nil
	}
	sessionIDs := make([]string, 0, len(sessions))
	for _, session := range sessions {
		sessionIDs = append(sessionIDs, session.ID)
	}
	deletedResponses, err := s.deleteExpiredRowsBySessionIDsSQLite(ctx, "codex_context_responses", sessionIDs, expiredBefore)
	if err != nil {
		return nil, err
	}
	deletedCompacts, err := s.deleteExpiredRowsBySessionIDsSQLite(ctx, "codex_context_compacts", sessionIDs, expiredBefore)
	if err != nil {
		return nil, err
	}
	remainingExpiresAt, err := s.selectRemainingExpiresAtSQLite(ctx, sessionIDs)
	if err != nil {
		return nil, err
	}
	deletedSessions, err := s.deleteOrRefreshSessionsSQLite(ctx, sessions, remainingExpiresAt)
	if err != nil {
		return nil, err
	}
	pending, err := s.selectPendingStorageKeysSQLite(ctx, limit)
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

// deleteExpiredRowsBySessionIds（SQLite）：入队 storage key 后按过期时间删除。
func (s *CodexContextStore) deleteExpiredRowsBySessionIDsSQLite(ctx context.Context, table string, sessionIDs []string, expiredBefore string) (int64, error) {
	var deleted int64
	for _, shardIndex := range s.shardIndexes() {
		db, err := s.shard(shardIndex)
		if err != nil {
			return deleted, err
		}
		for _, chunk := range chunkValues(sessionIDs, 900) {
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				return deleted, err
			}
			query := fmt.Sprintf(`
        SELECT storage_key FROM %s
        WHERE session_id IN (%s) AND expires_at < ?
			`, table, placeholderList(len(chunk)))
			rows, err := tx.QueryContext(ctx, query, append(stringSliceToAny(chunk), expiredBefore)...)
			if err != nil {
				_ = tx.Rollback()
				return deleted, err
			}
			var storageKeys []string
			for rows.Next() {
				var storageKey sql.NullString
				if err := rows.Scan(&storageKey); err != nil {
					rows.Close()
					_ = tx.Rollback()
					return deleted, err
				}
				deleted++
				storageKeys = append(storageKeys, storageKey.String)
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				_ = tx.Rollback()
				return deleted, err
			}
			rows.Close()
			if err := enqueueStorageCleanupKeys(ctx, tx, s.nowIso(), storageKeys); err != nil {
				_ = tx.Rollback()
				return deleted, err
			}
			result, err := tx.ExecContext(ctx, fmt.Sprintf(
				`DELETE FROM %s WHERE session_id IN (%s) AND expires_at < ?`, table, placeholderList(len(chunk))),
				append(stringSliceToAny(chunk), expiredBefore)...)
			if err != nil {
				_ = tx.Rollback()
				return deleted, err
			}
			if _, err := changes(result); err != nil {
				_ = tx.Rollback()
				return deleted, err
			}
			if err := tx.Commit(); err != nil {
				return deleted, err
			}
		}
	}
	return deleted, nil
}

// enqueueStorageCleanupKeys 照 enqueueCodexContextStorageCleanupKeys。
func enqueueStorageCleanupKeys(ctx context.Context, tx *sql.Tx, now string, storageKeys []string) error {
	keys := uniqueNonEmpty(storageKeys)
	if len(keys) == 0 {
		return nil
	}
	statement, err := tx.PrepareContext(ctx, `
    INSERT INTO codex_context_storage_cleanup_queue (
      storage_key, enqueued_at, updated_at, next_attempt_at, attempt_count, last_error
    ) VALUES (?, ?, ?, ?, 0, NULL)
    ON CONFLICT(storage_key) DO NOTHING
	`)
	if err != nil {
		return err
	}
	defer statement.Close()
	for _, storageKey := range keys {
		if _, err := statement.ExecContext(ctx, storageKey, now, now, now); err != nil {
			return err
		}
	}
	return nil
}

// selectRemainingSessionExpiresAtBySessionIds（SQLite）。
func (s *CodexContextStore) selectRemainingExpiresAtSQLite(ctx context.Context, sessionIDs []string) (map[string]string, error) {
	expiresAtBySessionID := map[string]string{}
	for _, table := range []string{"codex_context_responses", "codex_context_compacts"} {
		for _, shardIndex := range s.shardIndexes() {
			db, err := s.shard(shardIndex)
			if err != nil {
				return nil, err
			}
			for _, chunk := range chunkValues(sessionIDs, 900) {
				query := fmt.Sprintf(`
          SELECT session_id, MAX(expires_at) AS expires_at
          FROM %s
          WHERE session_id IN (%s)
          GROUP BY session_id
				`, table, placeholderList(len(chunk)))
				rows, err := db.QueryContext(ctx, query, stringSliceToAny(chunk)...)
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
	}
	return expiresAtBySessionID, nil
}

// deleteOrRefreshSessionRows（SQLite）。
func (s *CodexContextStore) deleteOrRefreshSessionsSQLite(ctx context.Context, sessions []expiredSession, remainingExpiresAt map[string]string) (int64, error) {
	var deleted int64
	refreshGrouped := map[int][][2]string{}
	deleteGrouped := map[int][]string{}
	now := s.nowIso()
	for _, session := range sessions {
		if remaining, ok := remainingExpiresAt[session.ID]; ok {
			refreshGrouped[session.ShardIndex] = append(refreshGrouped[session.ShardIndex], [2]string{session.ID, remaining})
			continue
		}
		deleteGrouped[session.ShardIndex] = append(deleteGrouped[session.ShardIndex], session.ID)
	}
	for shardIndex, values := range refreshGrouped {
		db, err := s.shard(shardIndex)
		if err != nil {
			return deleted, err
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return deleted, err
		}
		statement, err := tx.PrepareContext(ctx, `
      UPDATE codex_context_sessions
      SET updated_at = ?, expires_at = ?
      WHERE id = ?
		`)
		if err != nil {
			_ = tx.Rollback()
			return deleted, err
		}
		for _, value := range values {
			if _, err := statement.ExecContext(ctx, now, value[1], value[0]); err != nil {
				statement.Close()
				_ = tx.Rollback()
				return deleted, err
			}
		}
		statement.Close()
		if err := tx.Commit(); err != nil {
			return deleted, err
		}
	}
	for shardIndex, ids := range deleteGrouped {
		db, err := s.shard(shardIndex)
		if err != nil {
			return deleted, err
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return deleted, err
		}
		for _, chunk := range chunkValues(ids, 900) {
			result, err := tx.ExecContext(ctx, fmt.Sprintf(
				`DELETE FROM codex_context_sessions WHERE id IN (%s)`, placeholderList(len(chunk))),
				stringSliceToAny(chunk)...)
			if err != nil {
				_ = tx.Rollback()
				return deleted, err
			}
			affected, err := changes(result)
			if err != nil {
				_ = tx.Rollback()
				return deleted, err
			}
			deleted += affected
		}
		if err := tx.Commit(); err != nil {
			return deleted, err
		}
	}
	return deleted, nil
}

// pendingKeys 照 { storageKeys, hasMore }。
type pendingKeys struct {
	StorageKeys []string
	HasMore     bool
}

// selectPendingCodexContextStorageCleanupKeys（SQLite）。
func (s *CodexContextStore) selectPendingStorageKeysSQLite(ctx context.Context, limit int) (pendingKeys, error) {
	normalizedLimit := batchLimit(limit)
	if normalizedLimit > 10000 {
		normalizedLimit = 10000
	}
	now := s.nowIso()
	pendingSet := map[string]bool{}
	var pending []string
	for _, shardIndex := range s.shardIndexes() {
		db, err := s.shard(shardIndex)
		if err != nil {
			return pendingKeys{}, err
		}
		rows, err := db.QueryContext(ctx, `
      SELECT storage_key
      FROM codex_context_storage_cleanup_queue
      WHERE next_attempt_at <= ?
      ORDER BY next_attempt_at ASC, enqueued_at ASC, storage_key ASC
      LIMIT ?
		`, now, normalizedLimit+1)
		if err != nil {
			return pendingKeys{}, err
		}
		for rows.Next() {
			var storageKey sql.NullString
			if err := rows.Scan(&storageKey); err != nil {
				rows.Close()
				return pendingKeys{}, err
			}
			key := strings.TrimSpace(storageKey.String)
			if key == "" || pendingSet[key] {
				continue
			}
			pendingSet[key] = true
			pending = append(pending, key)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return pendingKeys{}, err
		}
		rows.Close()
	}
	return s.filterAndDiscardReferencedKeysSQLite(ctx, pending, normalizedLimit)
}

func (s *CodexContextStore) filterAndDiscardReferencedKeysSQLite(ctx context.Context, pending []string, limit int) (pendingKeys, error) {
	referenced, err := s.filterUnreferencedStorageKeysSQLite(ctx, pending)
	if err != nil {
		return pendingKeys{}, err
	}
	unreferencedSet := map[string]bool{}
	for _, key := range referenced {
		unreferencedSet[key] = true
	}
	var referencedKeys []string
	for _, key := range pending {
		if !unreferencedSet[key] {
			referencedKeys = append(referencedKeys, key)
		}
	}
	if len(referencedKeys) > 0 {
		for _, shardIndex := range s.shardIndexes() {
			db, err := s.shard(shardIndex)
			if err != nil {
				return pendingKeys{}, err
			}
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				return pendingKeys{}, err
			}
			if _, err := deleteStorageCleanupQueueRows(ctx, tx, referencedKeys); err != nil {
				_ = tx.Rollback()
				return pendingKeys{}, err
			}
			if err := tx.Commit(); err != nil {
				return pendingKeys{}, err
			}
		}
	}
	hasMore := len(referenced) > limit || len(pending) > limit
	if len(referenced) > limit {
		referenced = referenced[:limit]
	}
	return pendingKeys{StorageKeys: referenced, HasMore: hasMore}, nil
}

// filterUnreferencedStorageKeys（SQLite）：仍被 responses/compacts 引用的 key
// 不可删除，返回可删除集合。
func (s *CodexContextStore) filterUnreferencedStorageKeysSQLite(ctx context.Context, keys []string) ([]string, error) {
	deletable := map[string]bool{}
	for _, key := range keys {
		deletable[key] = true
	}
	if len(deletable) == 0 {
		return []string{}, nil
	}
	for _, table := range []string{"codex_context_responses", "codex_context_compacts"} {
		for _, shardIndex := range s.shardIndexes() {
			db, err := s.shard(shardIndex)
			if err != nil {
				return nil, err
			}
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
          FROM %s
          WHERE storage_key IN (%s)
				`, table, placeholderList(len(remaining)))
				rows, err := db.QueryContext(ctx, query, stringSliceToAny(remaining)...)
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
	}
	output := make([]string, 0, len(deletable))
	for _, key := range keys {
		if deletable[key] {
			output = append(output, key)
		}
	}
	return output, nil
}

func deleteStorageCleanupQueueRows(ctx context.Context, tx *sql.Tx, storageKeys []string) (int64, error) {
	var deleted int64
	for _, chunk := range chunkValues(storageKeys, 900) {
		if len(chunk) == 0 {
			continue
		}
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
	if s.Postgres {
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
	var acknowledged, deferred int64
	for _, shardIndex := range s.shardIndexes() {
		db, err := s.shard(shardIndex)
		if err != nil {
			return SettlementResult{}, err
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return SettlementResult{}, err
		}
		acknowledgedPart, err := deleteStorageCleanupQueueRows(ctx, tx, succeededKeys)
		if err != nil {
			_ = tx.Rollback()
			return SettlementResult{}, err
		}
		deferredPart, err := s.deferStorageCleanupRowsSQLite(ctx, tx, failures, now)
		if err != nil {
			_ = tx.Rollback()
			return SettlementResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return SettlementResult{}, err
		}
		acknowledged += acknowledgedPart
		deferred += deferredPart
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

func (s *CodexContextStore) deferStorageCleanupRowsSQLite(ctx context.Context, tx *sql.Tx, failures []SettlementFailure, now string) (int64, error) {
	var deferred int64
	for _, failure := range failures {
		attemptCount, err := currentAttemptCountSQLite(ctx, tx, failure.StorageKey)
		if err != nil {
			return deferred, err
		}
		result, err := tx.ExecContext(ctx, `
      UPDATE codex_context_storage_cleanup_queue
      SET attempt_count = attempt_count + 1,
          last_error = ?,
          updated_at = ?,
          next_attempt_at = ?
      WHERE storage_key = ?
		`, failure.Error, now, storageCleanupRetryAt(s.now(), s.jitter, int(attemptCount)+1), failure.StorageKey)
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

func currentAttemptCountSQLite(ctx context.Context, tx *sql.Tx, storageKey string) (int64, error) {
	var attemptCount sql.NullInt64
	err := tx.QueryRowContext(ctx, `
    SELECT attempt_count
    FROM codex_context_storage_cleanup_queue
    WHERE storage_key = ?
	`, storageKey).Scan(&attemptCount)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return attemptCount.Int64, nil
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

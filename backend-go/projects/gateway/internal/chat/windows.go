package chat

import (
	"sync"
)

// Per-user policy serialization for SQLite mirrors withSqliteChatUserPolicyLock:
// PostgreSQL serializes through pg_advisory_xact_lock; SQLite relies on this
// in-process queue keyed by system account id. MaxOpenConns(1) runtimes are
// additionally serialized by the single connection.
var sqliteUserPolicyLocks sync.Map

// lockUserPolicy returns the release func for the per-owner chat policy lock
// (conversation create/delete/clear and turn acceptance in SQLite mode). In
// PostgreSQL mode it is a no-op: the advisory xact lock inside the
// transaction provides the same ordering.
func (s *Store) lockUserPolicy(ownerID string) func() {
	if s.pg {
		return func() {}
	}
	value, _ := sqliteUserPolicyLocks.LoadOrStore(ownerID, &policyLockGuard{})
	guard := value.(*policyLockGuard)
	guard.mu.Lock()
	guard.depth++
	return func() {
		guard.mu.Unlock()
	}
}

type policyLockGuard struct {
	mu    sync.Mutex
	depth int
}

// releaseChatConversationStorageAndExpireAssets mirrors
// releaseChatConversationStorageAndExpireAssets: per-bucket storage window
// decrement, zero-window cleanup and asset expiry.
func (s *Store) releaseConversationStorageAndExpireAssets(tx queryer, conversationID, ownerID, now string) error {
	rows, err := tx.Query(s.bind(`SELECT created_at, content_bytes, storage_reserved_bytes
		FROM `+s.table("chat_messages")+` WHERE conversation_id = ? AND system_account_id = ?`), conversationID, ownerID)
	if err != nil {
		return err
	}
	buckets := map[string][2]int64{}
	for rows.Next() {
		var createdAt string
		var contentBytes, reservedBytes int64
		if err := rows.Scan(&createdAt, &contentBytes, &reservedBytes); err != nil {
			rows.Close()
			return err
		}
		bucketDate, err := requireRFC3339Instant(createdAt, "聊天消息 created_at")
		if err != nil {
			rows.Close()
			return err
		}
		bucket := buckets[bucketDate[:10]]
		bucket[0] += contentBytes
		bucket[1] += reservedBytes
		buckets[bucketDate[:10]] = bucket
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for bucketDate, bucket := range buckets {
		if _, err := tx.Exec(s.bind(`UPDATE `+s.table("chat_user_storage_windows")+`
			SET content_bytes = CASE WHEN content_bytes > ? THEN content_bytes - ? ELSE 0 END,
				reserved_bytes = CASE WHEN reserved_bytes > ? THEN reserved_bytes - ? ELSE 0 END,
				updated_at = ?
			WHERE system_account_id = ? AND bucket_date = ?`),
			bucket[0], bucket[0], bucket[1], bucket[1], now, ownerID, bucketDate); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(s.bind(`DELETE FROM `+s.table("chat_user_storage_windows")+`
		WHERE system_account_id = ? AND content_bytes = 0 AND reserved_bytes = 0`), ownerID); err != nil {
		return err
	}
	return s.expireChatAssetsForConversation(tx, conversationID, ownerID, now)
}

// recentStorageBytes mirrors recentStorageBytes: sum over the retention window
// buckets (bucket_date >= now-retentionDays, ISO date compare).
func (s *Store) recentStorageBytes(tx queryer, ownerID, now string, retentionDays int) (int64, error) {
	start, err := parseRFC3339Instant(now)
	if err != nil {
		return 0, &DomainError{Message: "聊天容量统计 now必须是带 Z 或数值 offset 的 RFC3339 时间"}
	}
	cutoff := isoMillis(start.UTC().AddDate(0, 0, -retentionDays))[:10]
	var total int64
	err = tx.QueryRow(s.bind(`SELECT COALESCE(SUM(content_bytes + reserved_bytes), 0) AS total
		FROM `+s.table("chat_user_storage_windows")+`
		WHERE system_account_id = ? AND bucket_date >= ?`), ownerID, cutoff).Scan(&total)
	if err != nil {
		return 0, err
	}
	return total, nil
}

// lockChatUserStorageQuota mirrors lockChatUserStorageQuota (PG only).
func (s *Store) lockChatUserStorageQuota(tx queryer, ownerID string) error {
	if !s.pg {
		return nil
	}
	_, err := tx.Exec(s.bind(`SELECT pg_advisory_xact_lock(hashtextextended(?, 0))`), "juhe-ai:chat-storage:"+ownerID)
	return err
}

// incrementStorageWindow mirrors incrementStorageWindow with the per-dialect
// upsert spelling.
func (s *Store) incrementStorageWindow(tx queryer, ownerID, now string, contentBytes, reservedBytes int64) error {
	normalized, err := requireRFC3339Instant(now, "聊天容量窗口 now")
	if err != nil {
		return err
	}
	bucketDate := normalized[:10]
	table := s.table("chat_user_storage_windows")
	if s.pg {
		_, err = tx.Exec(s.bind(`INSERT INTO `+table+` (system_account_id, bucket_date, content_bytes, reserved_bytes, updated_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (system_account_id, bucket_date)
			DO UPDATE SET content_bytes = `+table+`.content_bytes + EXCLUDED.content_bytes,
			              reserved_bytes = `+table+`.reserved_bytes + EXCLUDED.reserved_bytes,
			              updated_at = EXCLUDED.updated_at`),
			ownerID, bucketDate, contentBytes, reservedBytes, normalized)
		return err
	}
	_, err = tx.Exec(s.bind(`INSERT INTO `+table+` (system_account_id, bucket_date, content_bytes, reserved_bytes, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(system_account_id, bucket_date)
		DO UPDATE SET content_bytes = content_bytes + excluded.content_bytes,
		              reserved_bytes = reserved_bytes + excluded.reserved_bytes,
		              updated_at = excluded.updated_at`),
		ownerID, bucketDate, contentBytes, reservedBytes, normalized)
	return err
}

// settleStorageWindowReservationStrict mirrors settleStorageWindowReservationStrict.
func (s *Store) settleStorageWindowReservationStrict(tx queryer, ownerID, createdAt string, reservationBytes, contentBytes int64, now string) error {
	if contentBytes < 0 || contentBytes > reservationBytes {
		return &DomainError{Message: "助手消息实际字节超过预留"}
	}
	bucketDate, err := requireRFC3339Instant(createdAt, "聊天消息 created_at")
	if err != nil {
		return err
	}
	bucket := bucketDate[:10]
	result, err := tx.Exec(s.bind(`UPDATE `+s.table("chat_user_storage_windows")+`
		SET content_bytes = content_bytes + ?, reserved_bytes = reserved_bytes - ?, updated_at = ?
		WHERE system_account_id = ? AND bucket_date = ? AND reserved_bytes >= ?`),
		contentBytes, reservationBytes, now, ownerID, bucket, reservationBytes)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return &DomainError{Message: "聊天容量预留数据不一致：" + bucket + " 日桶缺失或不足"}
	}
	return nil
}

// releaseStorageWindowReservationStrict mirrors releaseStorageWindowReservationStrict.
func (s *Store) releaseStorageWindowReservationStrict(tx queryer, ownerID, createdAt string, reservationBytes int64, now string) error {
	bucketDate, err := requireRFC3339Instant(createdAt, "聊天消息 created_at")
	if err != nil {
		return err
	}
	bucket := bucketDate[:10]
	result, err := tx.Exec(s.bind(`UPDATE `+s.table("chat_user_storage_windows")+`
		SET reserved_bytes = reserved_bytes - ?, updated_at = ?
		WHERE system_account_id = ? AND bucket_date = ? AND reserved_bytes >= ?`),
		reservationBytes, now, ownerID, bucket, reservationBytes)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return &DomainError{Message: "聊天容量预留数据不一致：" + bucket + " 日桶缺失或不足"}
	}
	_, err = tx.Exec(s.bind(`DELETE FROM `+s.table("chat_user_storage_windows")+`
		WHERE system_account_id = ? AND bucket_date = ? AND content_bytes = 0 AND reserved_bytes = 0`),
		ownerID, bucket)
	return err
}

// decrementStorageWindowStrict mirrors decrementStorageWindowStrict.
func (s *Store) decrementStorageWindowStrict(tx queryer, ownerID, bucketDate string, bytes int64, now string) error {
	result, err := tx.Exec(s.bind(`UPDATE `+s.table("chat_user_storage_windows")+`
		SET content_bytes = content_bytes - ?, updated_at = ?
		WHERE system_account_id = ? AND bucket_date = ? AND content_bytes >= ?`),
		bytes, now, ownerID, bucketDate, bytes)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return &DomainError{Message: "聊天容量窗口数据不一致：" + bucketDate + " 日桶缺失或不足"}
	}
	_, err = tx.Exec(s.bind(`DELETE FROM `+s.table("chat_user_storage_windows")+`
		WHERE system_account_id = ? AND bucket_date = ? AND content_bytes = 0 AND reserved_bytes = 0`),
		ownerID, bucketDate)
	return err
}

// requiredAssistantStorageReservation mirrors requiredAssistantStorageReservation.
func requiredAssistantStorageReservation(reservedBytes int64) (int64, error) {
	if reservedBytes != AssistantStorageReservationBytes {
		return 0, &DomainError{Message: "助手消息存储预留数据不一致"}
	}
	return reservedBytes, nil
}

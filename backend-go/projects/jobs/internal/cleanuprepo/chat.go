package cleanuprepo

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
)

// chat.repository.ts cleanupChatRetention + chat-context.repository.ts 的
// 压缩恢复/检查点清理 + modules/chat/chat-asset-cleanup.ts 的资产清理链移植。
// 单事务覆盖：分区裁剪（PG 每日分区）→ 中断轮次恢复 → 过期轮次删除 →
// 幂等键/容量窗口收缩 → 空会话清理 → 标题回退 → 压缩恢复 → 检查点清理 →
// 资产认领删除。

// ChatAssistantStorageReservationBytes 照 chatAssistantStorageReservationBytes。
const ChatAssistantStorageReservationBytes = (192 + 192 + 64) * 1024

// chatContextMaintenanceMaxBatchSize 照 Node 常量。
const chatContextMaintenanceMaxBatchSize = 500

// ChatStore 承载 chat 库（juhe_chat）清理访问。
type ChatStore struct {
	DB *DB
	// AssetsRoot 是 JUHE_AI_CHAT_ASSETS_ROOT（聊天资产文件根目录）。
	AssetsRoot string
	// Now 注入时钟。
	Now func() time.Time
	// IsActiveTurn 照 isActiveChatGeneration：gateway 运行态在 jobs 进程不可见，
	// 组合根保持 nil（所有超时轮次视为中断）。
	IsActiveTurn func(ownerId, conversationId, turnId string) bool
}

func (s *ChatStore) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *ChatStore) table(name string) string {
	return s.DB.Table("juhe_chat", name)
}

func (s *ChatStore) nowIso() string { return ISOOf(s.now()) }

// pinScheduledLease 照 pinScheduledJobLeaseInTransaction（仅 PG）。
func pinScheduledLease(ctx context.Context, db *DB, tx *sql.Tx, lease *retention.ScheduledLeaseFence) error {
	if lease == nil {
		return nil
	}
	if !db.Postgres {
		return fmt.Errorf("后台周期任务共享租约只支持 PostgreSQL")
	}
	leaseKey := strings.TrimSpace(lease.LeaseKey)
	ownerID := strings.TrimSpace(lease.OwnerID)
	if leaseKey == "" {
		return fmt.Errorf("leaseKey 不能为空")
	}
	if ownerID == "" {
		return fmt.Errorf("ownerId 不能为空")
	}
	var found sql.NullString
	err := tx.QueryRowContext(ctx, `
    SELECT lease_key
    FROM juhe_stats.background_job_leases
    WHERE lease_key = ? AND owner_id = ? AND fencing_token = ?
      AND lease_until > ? AT TIME ZONE 'utc'
    LIMIT 1
    FOR UPDATE
	`, leaseKey, ownerID, lease.FencingToken, time.Now().UTC()).Scan(&found)
	if err == sql.ErrNoRows {
		return fmt.Errorf("后台任务租约已失效：%s", leaseKey)
	}
	return err
}

var chatTimestampLabelPattern = regexp.MustCompile(`^[0-9]{4}-`)

// requiredChatTimestamp 照 requiredChatTimestamp。
func requiredChatTimestamp(value any, label string) (string, error) {
	text := textOf(value)
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("%s必须是带 Z 或数值 offset 的 RFC3339 时间", label)
	}
	normalized, ok := parseInstant(text)
	if !ok {
		return "", fmt.Errorf("%s必须是带 Z 或数值 offset 的 RFC3339 时间", label)
	}
	return normalized.UTC().Format("2006-01-02T15:04:05.000Z07:00"), nil
}

// CleanupRetention 照 DB service `cleanup_chat_retention` 全链（retention 轮次/
// 消息/会话/分区 + 压缩恢复 + 检查点 + 资产）。
func (s *ChatStore) CleanupRetention(ctx context.Context, input retention.ChatRetentionInput) (*retention.ChatRetentionResult, error) {
	now, ok := parseInstant(input.Now)
	if !ok {
		return nil, fmt.Errorf("聊天保留清理 now必须是带 Z 或数值 offset 的 RFC3339 时间")
	}
	interruptedBefore, ok := parseInstant(input.InterruptedBefore)
	if !ok {
		return nil, fmt.Errorf("聊天保留清理 interruptedBefore必须是带 Z 或数值 offset 的 RFC3339 时间")
	}
	nowText := now.UTC().Format("2006-01-02T15:04:05.000Z07:00")
	interruptedBeforeText := interruptedBefore.UTC().Format("2006-01-02T15:04:05.000Z07:00")
	limit := input.Limit
	if limit < 2 {
		limit = 2
	}
	if limit > 1000 {
		limit = 1000
	}
	retentionDays := input.RetentionDays

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := pinScheduledLease(ctx, s.DB, tx, input.ScheduledLease); err != nil {
		return nil, err
	}

	result := &retention.ChatRetentionResult{}
	affectedConversations := map[string][2]string{}
	addAffected := func(conversationID, systemAccountID string) {
		affectedConversations[systemAccountID+"\x00"+conversationID] = [2]string{conversationID, systemAccountID}
	}

	// ---- 分区裁剪（仅 PG）----
	advancedConversationKeys := map[string]bool{}
	if s.DB.Postgres {
		partitionOutcome, err := s.dropExpiredChatPartitions(ctx, tx, nowText, retentionDays, addAffected)
		if err != nil {
			return nil, err
		}
		result.DroppedPartitions = partitionOutcome.DroppedPartitions
		for key := range partitionOutcome.AdvancedKeys {
			advancedConversationKeys[key] = true
		}
	}

	// ---- 中断轮次恢复 ----
	staleLimit := limit / 2
	if staleLimit < 1 {
		staleLimit = 1
	}
	staleRows, err := queryRows(ctx, tx, s.DB.Bind(fmt.Sprintf(`
      SELECT id, system_account_id, active_turn_id, active_started_at
      FROM %s
      WHERE active_turn_id IS NOT NULL AND active_started_at <= ?
      ORDER BY active_started_at ASC, id ASC LIMIT ?
	`, s.table("chat_conversations"))), interruptedBeforeText, staleLimit)
	if err != nil {
		return nil, err
	}
	for _, stale := range staleRows {
		if _, err := requiredChatTimestamp(stale["active_started_at"], "聊天会话 active_started_at"); err != nil {
			return nil, err
		}
		conversationID := textOf(stale["id"])
		systemAccountID := textOf(stale["system_account_id"])
		activeTurnID := textOf(stale["active_turn_id"])
		if s.IsActiveTurn != nil && s.IsActiveTurn(systemAccountID, conversationID, activeTurnID) {
			continue
		}
		assistantRows, err := queryRows(ctx, tx, s.DB.Bind(fmt.Sprintf(`
        SELECT * FROM %s
        WHERE conversation_id = ? AND system_account_id = ? AND turn_id = ?
          AND role = 'assistant' AND status = 'streaming'
		`, s.table("chat_messages"))), conversationID, systemAccountID, activeTurnID)
		if err != nil {
			return nil, err
		}
		updated, err := execChangedQ(ctx, tx, s.DB.Bind(fmt.Sprintf(`
        UPDATE %s
        SET status = 'failed', storage_reserved_bytes = 0,
            error_code = 'stream_interrupted',
            error_message = '生成进程异常中断，未取得原始异常详情', completed_at = ?
        WHERE conversation_id = ? AND system_account_id = ? AND turn_id = ?
          AND role = 'assistant' AND status = 'streaming'
		`, s.table("chat_messages"))), nowText, conversationID, systemAccountID, activeTurnID)
		if err != nil {
			return nil, err
		}
		if updated == 1 && len(assistantRows) > 0 {
			createdAt, err := requiredChatTimestamp(assistantRows[0]["created_at"], "聊天消息 created_at")
			if err != nil {
				return nil, err
			}
			reservationBytes, err := requiredAssistantStorageReservation(assistantRows[0]["storage_reserved_bytes"])
			if err != nil {
				return nil, err
			}
			if err := s.releaseStorageWindowReservationStrict(ctx, tx, systemAccountID, createdAt, reservationBytes, nowText); err != nil {
				return nil, err
			}
		}
		if _, err := execChangedQ(ctx, tx, s.DB.Bind(fmt.Sprintf(`
        UPDATE %s
        SET active_turn_id = NULL, active_started_at = NULL,
            message_revision = message_revision + ?, updated_at = ?
        WHERE id = ? AND system_account_id = ? AND active_turn_id = ?
		`, s.table("chat_conversations"))), updated, nowText, conversationID, systemAccountID, activeTurnID); err != nil {
			return nil, err
		}
		result.RecoveredTurns += updated
	}

	// ---- 过期轮次删除 ----
	expiredTurns, err := queryRows(ctx, tx, s.DB.Bind(fmt.Sprintf(`
      SELECT conversation_id, system_account_id, turn_id
      FROM %s
      GROUP BY conversation_id, system_account_id, turn_id
      HAVING MAX(expires_at) <= ?
      ORDER BY MIN(expires_at) ASC, turn_id ASC LIMIT ?
	`, s.table("chat_messages"))), nowText, staleLimit)
	if err != nil {
		return nil, err
	}
	for _, expired := range expiredTurns {
		conversationID := textOf(expired["conversation_id"])
		systemAccountID := textOf(expired["system_account_id"])
		turnID := textOf(expired["turn_id"])
		turnMessages, err := queryRows(ctx, tx, s.DB.Bind(fmt.Sprintf(`
        SELECT created_at, expires_at, content_bytes, storage_reserved_bytes
        FROM %s
        WHERE conversation_id = ? AND system_account_id = ? AND turn_id = ?
		`, s.table("chat_messages"))), conversationID, systemAccountID, turnID)
		if err != nil {
			return nil, err
		}
		bucketOrder := []string{}
		buckets := map[string][2]float64{}
		for _, message := range turnMessages {
			if _, err := requiredChatTimestamp(message["expires_at"], "聊天消息 expires_at"); err != nil {
				return nil, err
			}
			createdAt, err := requiredChatTimestamp(message["created_at"], "聊天消息 created_at")
			if err != nil {
				return nil, err
			}
			bucketDate := createdAt[:10]
			bucket := buckets[bucketDate]
			if bucket == ([2]float64{}) && !containsKey(buckets, bucketDate) {
				bucketOrder = append(bucketOrder, bucketDate)
			}
			bucket[0] += numberOf(message["content_bytes"])
			bucket[1] += numberOf(message["storage_reserved_bytes"])
			buckets[bucketDate] = bucket
		}
		for _, bucketDate := range bucketOrder {
			bucket := buckets[bucketDate]
			if _, err := execChangedQ(ctx, tx, s.DB.Bind(fmt.Sprintf(`
          UPDATE %s
          SET content_bytes = CASE WHEN content_bytes > ? THEN content_bytes - ? ELSE 0 END,
              reserved_bytes = CASE WHEN reserved_bytes > ? THEN reserved_bytes - ? ELSE 0 END,
              updated_at = ?
          WHERE system_account_id = ? AND bucket_date = ?
			`, s.table("chat_user_storage_windows"))), bucket[0], bucket[0], bucket[1], bucket[1], nowText, systemAccountID, bucketDate); err != nil {
				return nil, err
			}
		}
		if _, err := execChangedQ(ctx, tx, fmt.Sprintf(
			`DELETE FROM %s WHERE conversation_id = ? AND turn_id = ?`, s.table("chat_message_idempotency")),
			conversationID, turnID); err != nil {
			return nil, err
		}
		deleted, err := execChangedQ(ctx, tx, fmt.Sprintf(
			`DELETE FROM %s WHERE conversation_id = ? AND system_account_id = ? AND turn_id = ?`,
			s.table("chat_messages")), conversationID, systemAccountID, turnID)
		if err != nil {
			return nil, err
		}
		if _, err := execChangedQ(ctx, tx, fmt.Sprintf(`
        UPDATE %s
        SET active_turn_id = NULL, active_started_at = NULL, updated_at = ?
        WHERE id = ? AND system_account_id = ? AND active_turn_id = ?
		`, s.table("chat_conversations")), nowText, conversationID, systemAccountID, turnID); err != nil {
			return nil, err
		}
		result.DeletedMessages += deleted
		if deleted > 0 {
			addAffected(conversationID, systemAccountID)
		}
	}
	for key, conversation := range affectedConversations {
		if advancedConversationKeys[key] {
			continue
		}
		if _, err := execChangedQ(ctx, tx, fmt.Sprintf(`
      UPDATE %s
      SET message_revision = message_revision + 1, updated_at = ?
      WHERE id = ? AND system_account_id = ?
		`, s.table("chat_conversations")), nowText, conversation[0], conversation[1]); err != nil {
			return nil, err
		}
		advancedConversationKeys[key] = true
	}
	if _, err := execChangedQ(ctx, tx, fmt.Sprintf(
		`DELETE FROM %s WHERE expires_at <= ?`, s.table("chat_message_idempotency")), nowText); err != nil {
		return nil, err
	}
	storageWindowCutoff, err := storageWindowCutoffDate(nowText, retentionDays)
	if err != nil {
		return nil, err
	}
	if s.DB.Postgres {
		if _, err := tx.ExecContext(ctx, `
      DELETE FROM juhe_chat.chat_user_storage_windows AS storage_window
      WHERE (content_bytes = 0 AND reserved_bytes = 0)
        OR (
          bucket_date < $1
          AND NOT EXISTS (
            SELECT 1 FROM juhe_chat.chat_messages AS message
            WHERE message.system_account_id = storage_window.system_account_id
              AND substr(message.created_at, 1, 10) = storage_window.bucket_date
            LIMIT 1
          )
        )
		`, storageWindowCutoff); err != nil {
			return nil, err
		}
	} else {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
      DELETE FROM %s AS storage_window
      WHERE (content_bytes = 0 AND reserved_bytes = 0)
        OR (
          bucket_date < ?
          AND NOT EXISTS (
            SELECT 1 FROM %s AS message
            WHERE message.system_account_id = storage_window.system_account_id
              AND substr(message.created_at, 1, 10) = storage_window.bucket_date
            LIMIT 1
          )
        )
		`, s.table("chat_user_storage_windows"), s.table("chat_messages")), storageWindowCutoff); err != nil {
			return nil, err
		}
	}
	for _, conversation := range affectedConversations {
		deleted, err := execChangedQ(ctx, tx, fmt.Sprintf(`
      DELETE FROM %s
      WHERE id = ? AND system_account_id = ? AND active_turn_id IS NULL
        AND NOT EXISTS (SELECT 1 FROM %s WHERE conversation_id = ? LIMIT 1)
		`, s.table("chat_conversations"), s.table("chat_messages")),
			conversation[0], conversation[1], conversation[0])
		if err != nil {
			return nil, err
		}
		result.DeletedConversations += deleted
	}

	// ---- 标题回退 ----
	staleTitles, err := queryRows(ctx, tx, s.DB.Bind(fmt.Sprintf(`
      SELECT id, system_account_id FROM %s conversation
      WHERE title_source_message_id IS NOT NULL
        AND NOT EXISTS (
          SELECT 1 FROM %s message
          WHERE message.conversation_id = conversation.id AND message.id = conversation.title_source_message_id
        )
      ORDER BY updated_at ASC, id ASC LIMIT ?
	`, s.table("chat_conversations"), s.table("chat_messages"))), staleLimit)
	if err != nil {
		return nil, err
	}
	for _, conversation := range staleTitles {
		conversationID := textOf(conversation["id"])
		ownerID := textOf(conversation["system_account_id"])
		firstUser, err := queryOne(ctx, tx, s.DB.Bind(fmt.Sprintf(`
        SELECT id, content_text, expires_at FROM %s
        WHERE conversation_id = ? AND system_account_id = ? AND role = 'user' AND expires_at > ?
        ORDER BY sequence_no ASC LIMIT 1
		`, s.table("chat_messages"))), conversationID, ownerID, nowText)
		if err != nil {
			return nil, err
		}
		if firstUser == nil {
			continue
		}
		if _, err := requiredChatTimestamp((*firstUser)["expires_at"], "聊天消息 expires_at"); err != nil {
			return nil, err
		}
		if _, err := execChangedQ(ctx, tx, fmt.Sprintf(`
        UPDATE %s
        SET title = ?, title_source_message_id = ?, updated_at = ?
        WHERE id = ? AND system_account_id = ?
		`, s.table("chat_conversations")), titleFromContent(textOf((*firstUser)["content_text"])), textOf((*firstUser)["id"]), nowText, conversationID, ownerID); err != nil {
			return nil, err
		}
	}
	emptyBefore := ISOOf(now.AddDate(0, 0, -1))
	emptyDeleted, err := execChangedQ(ctx, tx, fmt.Sprintf(`
    DELETE FROM %s
    WHERE active_turn_id IS NULL AND created_at <= ?
      AND NOT EXISTS (SELECT 1 FROM %s WHERE conversation_id = %s.id LIMIT 1)
	`, s.table("chat_conversations"), s.table("chat_messages"), s.table("chat_conversations")), emptyBefore)
	if err != nil {
		return nil, err
	}
	result.DeletedConversations += emptyDeleted
	result.HasMore = len(expiredTurns)*2 >= limit || len(staleRows)*2 >= limit

	// ---- 压缩恢复 / 检查点 / 资产（Node 在独立事务中执行；同链顺序保持）----
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	recoveredCompactions, err := s.recoverStaleCompactions(ctx, nowText, interruptedBeforeText, limit)
	if err != nil {
		return nil, err
	}
	result.RecoveredCompactions = recoveredCompactions
	contextLimit := limit
	if contextLimit > chatContextMaintenanceMaxBatchSize {
		contextLimit = chatContextMaintenanceMaxBatchSize
	}
	if contextLimit < 1 {
		contextLimit = 1
	}
	checkpoints, err := s.cleanupExpiredCheckpoints(ctx, nowText, contextLimit)
	if err != nil {
		return nil, err
	}
	result.DeletedCheckpoints = checkpoints.DeletedCheckpoints
	result.HasMoreCheckpoints = checkpoints.HasMore
	assets, err := s.cleanupExpiredAssets(ctx, nowText, contextLimit)
	if err != nil {
		return nil, err
	}
	result.ClaimedAssets = assets.ClaimedAssets
	result.DeletedAssets = assets.DeletedAssets
	result.FailedAssets = assets.FailedAssets
	result.HasMoreAssets = assets.HasMoreAssets
	result.HasMore = result.HasMore || checkpoints.HasMore || assets.HasMoreAssets
	return result, nil
}

type chatPartitionDropOutcome struct {
	DroppedPartitions int64
	AdvancedKeys      map[string]bool
}

var chatPartitionNamePattern = regexp.MustCompile(`^chat_messages_(\d{4})(\d{2})(\d{2})$`)

// dropExpiredChatPartitions 照 dropExpiredPostgresChatPartitions。
func (s *ChatStore) dropExpiredChatPartitions(ctx context.Context, tx *sql.Tx, now string, retentionDays int, addAffected func(conversationID, systemAccountID string)) (chatPartitionDropOutcome, error) {
	outcome := chatPartitionDropOutcome{AdvancedKeys: map[string]bool{}}
	cutoff := parseInstantMust(now).AddDate(0, 0, -retentionDays)
	rows, err := tx.QueryContext(ctx, `
    SELECT child.relname AS partition_name
    FROM pg_inherits
    JOIN pg_class parent ON parent.oid = inhparent
    JOIN pg_namespace namespace ON namespace.oid = parent.relnamespace
    JOIN pg_class child ON child.oid = inhrelid
    WHERE namespace.nspname = 'juhe_chat' AND parent.relname = 'chat_messages'
	`)
	if err != nil {
		return outcome, err
	}
	var expiredPartitionNames []string
	for rows.Next() {
		var name sql.NullString
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return outcome, err
		}
		match := chatPartitionNamePattern.FindStringSubmatch(strings.TrimSpace(name.String))
		if match == nil {
			continue
		}
		partitionEnd := time.Date(atoi(match[1]), time.Month(atoi(match[2])), atoi(match[3])+1, 0, 0, 0, 0, time.UTC)
		if partitionEnd.After(cutoff) {
			continue
		}
		expiredPartitionNames = append(expiredPartitionNames, match[0])
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return outcome, err
	}
	rows.Close()
	affected := map[string][2]string{}
	for _, name := range expiredPartitionNames {
		conversationRows, err := queryRows(ctx, tx, fmt.Sprintf(
			`SELECT DISTINCT conversation_id, system_account_id FROM juhe_chat.%q`, name))
		if err != nil {
			return outcome, err
		}
		for _, row := range conversationRows {
			conversationID := textOf(row["conversation_id"])
			systemAccountID := textOf(row["system_account_id"])
			affected[systemAccountID+"\x00"+conversationID] = [2]string{conversationID, systemAccountID}
			addAffected(conversationID, systemAccountID)
		}
	}
	for key, conversation := range affected {
		if _, err := execChangedQ(ctx, tx, fmt.Sprintf(`
      UPDATE juhe_chat.chat_conversations
      SET message_revision = message_revision + 1, updated_at = ?
      WHERE id = ? AND system_account_id = ?
		`), now, conversation[0], conversation[1]); err != nil {
			return outcome, err
		}
		outcome.AdvancedKeys[key] = true
	}
	for _, name := range expiredPartitionNames {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DROP TABLE IF EXISTS juhe_chat.%q`, name)); err != nil {
			return outcome, err
		}
	}
	outcome.DroppedPartitions = int64(len(expiredPartitionNames))
	return outcome, nil
}

func parseInstantMust(value string) time.Time {
	parsed, _ := parseInstant(value)
	return parsed
}

// recoverStaleCompactions 照 recoverStaleChatContextCompactions。
func (s *ChatStore) recoverStaleCompactions(ctx context.Context, now, staleClaimBefore string, limit int) (int64, error) {
	if limit > chatContextMaintenanceMaxBatchSize {
		limit = chatContextMaintenanceMaxBatchSize
	}
	if limit < 1 {
		limit = 1
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	lockSuffix := ""
	if s.DB.Postgres {
		lockSuffix = " FOR UPDATE SKIP LOCKED"
	}
	rows, err := queryRows(ctx, tx, s.DB.Bind(fmt.Sprintf(`
      SELECT id, system_account_id
      FROM %s
      WHERE context_state = 'compacting' AND context_claimed_at <= ?
      ORDER BY context_claimed_at ASC, id ASC
      LIMIT ?%s
	`, s.table("chat_conversations"), lockSuffix)), staleClaimBefore, limit)
	if err != nil {
		return 0, err
	}
	var recovered int64
	for _, row := range rows {
		updated, err := execChangedQ(ctx, tx, s.DB.Bind(fmt.Sprintf(`
      UPDATE %s
      SET context_state = 'compact_failed', context_claim_id = NULL,
          context_claim_revision = NULL, context_claim_through_sequence = NULL,
          context_claimed_at = NULL, context_retry_at = ?,
          context_error_code = 'chat_context_compaction_stale',
          context_progress_sequence = 0, context_progress_earliest_expires_at = NULL,
          updated_at = ?
      WHERE id = ? AND system_account_id = ?
        AND context_state = 'compacting' AND context_claimed_at <= ?
		`, s.table("chat_conversations"))), now, now, textOf(row["id"]), textOf(row["system_account_id"]), staleClaimBefore)
		if err != nil {
			return 0, err
		}
		recovered += updated
	}
	return recovered, tx.Commit()
}

type checkpointCleanupOutcome struct {
	DeletedCheckpoints int64
	HasMore            bool
}

// cleanupExpiredCheckpoints 照 cleanupExpiredChatContextCheckpoints。
func (s *ChatStore) cleanupExpiredCheckpoints(ctx context.Context, now string, limit int) (checkpointCleanupOutcome, error) {
	outcome := checkpointCleanupOutcome{}
	if limit > chatContextMaintenanceMaxBatchSize {
		limit = chatContextMaintenanceMaxBatchSize
	}
	if limit < 1 {
		limit = 1
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return outcome, err
	}
	defer func() { _ = tx.Rollback() }()
	lockSuffix := ""
	if s.DB.Postgres {
		lockSuffix = " FOR UPDATE SKIP LOCKED"
	}
	rows, err := queryRows(ctx, tx, s.DB.Bind(fmt.Sprintf(`
      SELECT id, conversation_id, status
      FROM %s
      WHERE expires_at <= ?
      ORDER BY expires_at ASC, id ASC
      LIMIT ?%s
	`, s.table("chat_context_checkpoints"), lockSuffix)), now, limit)
	if err != nil {
		return outcome, err
	}
	var deletableIDs []string
	for _, row := range rows {
		checkpointID := textOf(row["id"])
		if checkpointID == "" {
			continue
		}
		if textOf(row["status"]) == "active" {
			detached, err := execChangedQ(ctx, tx, s.DB.Bind(fmt.Sprintf(`
        UPDATE %s
        SET context_revision = context_revision + 1, active_checkpoint_id = NULL,
            compacted_through_sequence = 0, context_state = 'ready',
            active_context_tokens = NULL, effective_context_limit_tokens = NULL,
            context_usage_estimated = 1,
            context_retry_at = NULL, context_error_code = NULL,
            context_progress_sequence = 0, context_progress_earliest_expires_at = NULL,
            updated_at = ?
        WHERE id = ? AND active_checkpoint_id = ? AND context_state != 'compacting'
			`, s.table("chat_conversations"))), now, textOf(row["conversation_id"]), checkpointID)
			if err != nil {
				return outcome, err
			}
			if detached != 1 {
				continue
			}
		}
		deletableIDs = append(deletableIDs, checkpointID)
	}
	if len(deletableIDs) == 0 {
		outcome.HasMore = len(rows) == limit
		return outcome, tx.Commit()
	}
	deleted, err := execChangedQ(ctx, tx, fmt.Sprintf(
		`DELETE FROM %s WHERE id IN (%s)`, s.table("chat_context_checkpoints"), s.DB.BindIn(len(deletableIDs))),
		stringSliceToAny(deletableIDs)...)
	if err != nil {
		return outcome, err
	}
	outcome.DeletedCheckpoints = deleted
	outcome.HasMore = len(rows) == limit
	return outcome, tx.Commit()
}

// assetCleanupOutcome 照 ChatAssetCleanupResult。
type assetCleanupOutcome struct {
	ClaimedAssets int64
	DeletedAssets int64
	FailedAssets  int64
	HasMoreAssets bool
}

// cleanupExpiredAssets 照 cleanupExpiredChatAssets（认领 → 删文件 → 结算）。
func (s *ChatStore) cleanupExpiredAssets(ctx context.Context, now string, limit int) (assetCleanupOutcome, error) {
	outcome := assetCleanupOutcome{}
	if limit > 500 {
		limit = 500
	}
	if limit < 1 {
		limit = 1
	}
	claimID := fmt.Sprintf("chat_asset_cleanup_%s", randomHex32())
	nowMs := parseInstantMust(now).UnixMilli()
	staleClaimBefore := ISOOf(time.UnixMilli(nowMs - 15*60*1000))
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return outcome, err
	}
	defer func() { _ = tx.Rollback() }()
	lockSuffix := ""
	if s.DB.Postgres {
		lockSuffix = " FOR UPDATE SKIP LOCKED"
	}
	rows, err := queryRows(ctx, tx, s.DB.Bind(fmt.Sprintf(`
      SELECT id FROM %s
      WHERE expires_at <= ?
        AND (
          cleanup_status = 'active'
          OR (cleanup_status = 'failed' AND cleanup_retry_at <= ?)
          OR (cleanup_status = 'claimed' AND cleanup_claimed_at <= ?)
        )
      ORDER BY expires_at ASC, id ASC
      LIMIT ?%s
	`, s.table("chat_assets"), lockSuffix)), now, now, staleClaimBefore, limit)
	if err != nil {
		return outcome, err
	}
	var assetIDs []string
	for _, row := range rows {
		if id := textOf(row["id"]); id != "" {
			assetIDs = append(assetIDs, id)
		}
	}
	if len(assetIDs) == 0 {
		return outcome, tx.Commit()
	}
	updateArgs := []any{claimID, now, now}
	updateArgs = append(updateArgs, stringSliceToAny(assetIDs)...)
	updated, err := execChangedQ(ctx, tx, s.DB.Bind(fmt.Sprintf(`
      UPDATE %s
      SET cleanup_status = 'claimed', cleanup_claim_id = ?, cleanup_claimed_at = ?,
          cleanup_attempt_count = cleanup_attempt_count + 1, cleanup_retry_at = NULL,
          cleanup_error_code = NULL, updated_at = ?
      WHERE id IN (%s)
	`, s.table("chat_assets"), s.DB.BindIn(len(assetIDs)))), updateArgs...)
	if err != nil {
		return outcome, err
	}
	if updated != int64(len(assetIDs)) {
		return outcome, fmt.Errorf("聊天资产清理认领发生并发冲突")
	}
	claimedRows, err := queryRows(ctx, tx, s.DB.Bind(fmt.Sprintf(`
      SELECT * FROM %s WHERE cleanup_claim_id = ? ORDER BY expires_at ASC, id ASC
	`, s.table("chat_assets"))), claimID)
	if err != nil {
		return outcome, err
	}
	if err := tx.Commit(); err != nil {
		return outcome, err
	}
	outcome.ClaimedAssets = int64(len(claimedRows))
	outcome.HasMoreAssets = len(assetIDs) == limit
	for _, asset := range claimedRows {
		storageKey := optionalText(asset["storage_key"])
		previewKey := optionalText(asset["preview_storage_key"])
		deleteErr := deleteChatAssetObjects(s.AssetsRoot, storageKey, previewKey)
		if deleteErr == nil {
			deleted, completeErr := s.completeAssetDeletion(ctx, textOf(asset["id"]), claimID)
			if completeErr != nil {
				deleteErr = completeErr
			} else if !deleted {
				deleteErr = fmt.Errorf("聊天资产清理认领已变化")
			} else {
				outcome.DeletedAssets++
				continue
			}
		}
		outcome.FailedAssets++
		attemptCount := int(numberOf(asset["cleanup_attempt_count"]))
		retryDelay := cleanupRetryDelayMs(attemptCount)
		retryAt := ISOOf(time.UnixMilli(nowMs + retryDelay))
		errorCode := "chat_asset_cleanup_failed"
		_, _ = s.releaseAssetDeletionClaim(ctx, textOf(asset["id"]), claimID, errorCode, retryAt, now)
	}
	return outcome, nil
}

func cleanupRetryDelayMs(attemptCount int) int64 {
	delay := int64(60_000)
	base := int64(1)
	shift := attemptCount - 1
	if shift < 0 {
		shift = 0
	}
	if shift > 6 {
		shift = 6
	}
	for index := 0; index < shift; index++ {
		base *= 2
	}
	delay *= base
	if delay > 60*60_000 {
		delay = 60 * 60_000
	}
	return delay
}

// completeAssetDeletion 照 completeChatAssetDeletion。
func (s *ChatStore) completeAssetDeletion(ctx context.Context, assetID, claimID string) (bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	asset, err := queryOne(ctx, tx, s.DB.Bind(fmt.Sprintf(`
    SELECT * FROM %s
    WHERE id = ? AND cleanup_status = 'claimed' AND cleanup_claim_id = ?
    LIMIT 1
	`, s.table("chat_assets"))), assetID, claimID)
	if err != nil {
		return false, err
	}
	if asset == nil {
		return false, nil
	}
	if s.DB.Postgres {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(?, 0))`,
			"juhe-ai:chat-asset-storage:"+textOf((*asset)["system_account_id"])); err != nil {
			return false, err
		}
	}
	deleted, err := execChangedQ(ctx, tx, fmt.Sprintf(
		`DELETE FROM %s WHERE id = ? AND cleanup_status = 'claimed' AND cleanup_claim_id = ?`,
		s.table("chat_assets")), assetID, claimID)
	if err != nil {
		return false, err
	}
	if deleted != 1 {
		return false, nil
	}
	quotaBytes := int64(numberOf((*asset)["quota_bytes"]))
	if _, err := execChangedQ(ctx, tx, s.DB.Bind(fmt.Sprintf(`
    UPDATE %s
    SET asset_bytes = asset_bytes - ?, asset_count = asset_count - 1, updated_at = ?
    WHERE system_account_id = ? AND asset_bytes >= ? AND asset_count >= 1
	`, s.table("chat_user_asset_usage"))), quotaBytes, s.nowIso(), textOf((*asset)["system_account_id"]), quotaBytes); err != nil {
		return false, err
	}
	if _, err := execChangedQ(ctx, tx, fmt.Sprintf(
		`DELETE FROM %s WHERE system_account_id = ? AND asset_bytes = 0 AND asset_count = 0`,
		s.table("chat_user_asset_usage")), textOf((*asset)["system_account_id"])); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// releaseAssetDeletionClaim 照 releaseChatAssetDeletionClaim。
func (s *ChatStore) releaseAssetDeletionClaim(ctx context.Context, assetID, claimID, errorCode, retryAt, now string) (bool, error) {
	if strings.TrimSpace(errorCode) == "" {
		errorCode = "chat_asset_cleanup_failed"
	}
	if len(errorCode) > 120 {
		errorCode = errorCode[:120]
	}
	changedRows, err := execChangedQ(ctx, s.DB, s.DB.Bind(fmt.Sprintf(`
    UPDATE %s
    SET cleanup_status = 'failed', cleanup_claim_id = NULL, cleanup_claimed_at = NULL,
        cleanup_retry_at = ?, cleanup_error_code = ?, updated_at = ?
    WHERE id = ? AND cleanup_status = 'claimed' AND cleanup_claim_id = ?
	`, s.table("chat_assets"))), retryAt, errorCode, now, assetID, claimID)
	if err != nil {
		return false, err
	}
	return changedRows == 1, nil
}

// deleteChatAssetObjects 照 deleteChatAssetObjects：root 内相对路径删除，
// 缺失不计失败。
func deleteChatAssetObjects(root string, storageKeys ...string) error {
	keys := make([]string, 0, len(storageKeys))
	seen := map[string]bool{}
	for _, key := range storageKeys {
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		keys = append(keys, key)
	}
	if len(keys) > 2 {
		return fmt.Errorf("单个聊天资产最多包含两个待删除对象")
	}
	for _, key := range keys {
		normalized := strings.ReplaceAll(key, "\\", "/")
		if normalized == "" || strings.HasPrefix(normalized, "/") || strings.Contains(normalized, "\x00") {
			return fmt.Errorf("聊天资产存储键无效")
		}
		for _, segment := range strings.Split(normalized, "/") {
			if segment == ".." {
				return fmt.Errorf("聊天资产存储键无效")
			}
		}
		if err := os.Remove(root + string(os.PathSeparator) + filepathFromSlash(normalized)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func filepathFromSlash(value string) string {
	if os.PathSeparator == '/' {
		return value
	}
	return strings.ReplaceAll(value, "/", string(os.PathSeparator))
}

// ---- 行读取辅助 ----

type row map[string]any

func textOf(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case []byte:
		return string(typed)
	case time.Time:
		return typed.UTC().Format("2006-01-02T15:04:05.000Z07:00")
	}
	return fmt.Sprintf("%v", value)
}

func optionalText(value any) string {
	text := textOf(value)
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return text
}

func numberOf(value any) float64 {
	switch typed := value.(type) {
	case nil:
		return 0
	case float64:
		return typed
	case int64:
		return float64(typed)
	case int:
		return float64(typed)
	case []byte:
		return parseNumber(string(typed))
	case string:
		return parseNumber(typed)
	}
	return 0
}

func parseNumber(value string) float64 {
	var out float64
	_, err := fmt.Sscanf(strings.TrimSpace(value), "%g", &out)
	if err != nil {
		return 0
	}
	return out
}

func queryRows(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, query string, args ...any) ([]row, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var output []row
	for rows.Next() {
		scan := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for index := range scan {
			pointers[index] = &scan[index]
		}
		if err := rows.Scan(pointers...); err != nil {
			return nil, err
		}
		current := row{}
		for index, column := range columns {
			current[column] = scan[index]
		}
		output = append(output, current)
	}
	return output, rows.Err()
}

func queryOne(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, query string, args ...any) (*row, error) {
	rows, err := queryRows(ctx, q, query, args...)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

func execChangedQ(ctx context.Context, q interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, query string, args ...any) (int64, error) {
	result, err := q.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return changes(result)
}

func containsKey(target map[string][2]float64, key string) bool {
	_, ok := target[key]
	return ok
}

// releaseStorageWindowReservationStrict 照 Node 同名函数。
func (s *ChatStore) releaseStorageWindowReservationStrict(ctx context.Context, tx *sql.Tx, systemAccountID, createdAt string, reservationBytes int64, now string) error {
	bucketDate := createdAt[:10]
	updated, err := execChangedQ(ctx, tx, s.DB.Bind(fmt.Sprintf(`
    UPDATE %s
    SET reserved_bytes = reserved_bytes - ?, updated_at = ?
    WHERE system_account_id = ? AND bucket_date = ? AND reserved_bytes >= ?
	`, s.table("chat_user_storage_windows"))), reservationBytes, now, systemAccountID, bucketDate, reservationBytes)
	if err != nil {
		return err
	}
	if updated != 1 {
		return fmt.Errorf("聊天容量预留数据不一致：%s 日桶缺失或不足", bucketDate)
	}
	_, err = execChangedQ(ctx, tx, fmt.Sprintf(
		`DELETE FROM %s WHERE system_account_id = ? AND bucket_date = ? AND content_bytes = 0 AND reserved_bytes = 0`,
		s.table("chat_user_storage_windows")), systemAccountID, bucketDate)
	return err
}

func requiredAssistantStorageReservation(value any) (int64, error) {
	reservationBytes := int64(numberOf(value))
	if reservationBytes != ChatAssistantStorageReservationBytes {
		return 0, fmt.Errorf("助手消息存储预留数据不一致")
	}
	return reservationBytes, nil
}

// storageWindowCutoffDate 照 storageWindowCutoffDate。
func storageWindowCutoffDate(now string, retentionDays int) (string, error) {
	parsed, ok := parseInstant(now)
	if !ok {
		return "", fmt.Errorf("聊天容量窗口 now必须是带 Z 或数值 offset 的 RFC3339 时间")
	}
	return parsed.UTC().AddDate(0, 0, -retentionDays).Format("2006-01-02"), nil
}

// titleFromContent 照 titleFromContent。
func titleFromContent(content string) string {
	cleaned := strings.Map(func(r rune) rune {
		if r <= 0x1f || r == 0x7f {
			return ' '
		}
		return r
	}, content)
	if index := strings.IndexAny(cleaned, "\r\n"); index >= 0 {
		cleaned = cleaned[:index]
	}
	cleaned = strings.Join(strings.Fields(cleaned), " ")
	runes := []rune(cleaned)
	if len(runes) > 60 {
		cleaned = string(runes[:60])
	}
	if cleaned == "" {
		return "新对话"
	}
	return cleaned
}

var randomHex32 = newRandomHex32

func newRandomHex32() string {
	buffer := make([]byte, 16)
	if _, err := cryptoRead(buffer); err != nil {
		return fmt.Sprintf("%016x", time.Now().UnixNano())
	}
	return fmt.Sprintf("%x", buffer)
}

package cleanuprepo

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
)

// chat.go 的语义测试：SQLite 真库覆盖 cleanupChatRetention 主链（中断轮次
// 恢复 → 过期轮次删除 → 容量窗口收缩 → 空会话清理 → 标题回退）、压缩恢复、
// 检查点清理与资产认领删除；PG 录制驱动覆盖分区裁剪。

func createKitChatSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	execKitSchema(t, db, `CREATE TABLE IF NOT EXISTS chat_conversations (
      id TEXT NOT NULL, system_account_id TEXT NOT NULL, title TEXT DEFAULT '',
      title_source_message_id TEXT, active_turn_id TEXT, active_started_at TEXT,
      message_revision INTEGER DEFAULT 0, context_state TEXT DEFAULT 'ready',
      context_claim_id TEXT, context_claim_revision INTEGER, context_claim_through_sequence INTEGER,
      context_claimed_at TEXT, context_retry_at TEXT, context_error_code TEXT,
      context_progress_sequence INTEGER DEFAULT 0, context_progress_earliest_expires_at TEXT,
      context_revision INTEGER DEFAULT 0, active_checkpoint_id TEXT,
      compacted_through_sequence INTEGER DEFAULT 0, active_context_tokens INTEGER,
      effective_context_limit_tokens INTEGER, context_usage_estimated INTEGER DEFAULT 0,
      created_at TEXT DEFAULT '', updated_at TEXT DEFAULT '',
      PRIMARY KEY (id, system_account_id))`,
		`CREATE TABLE IF NOT EXISTS chat_messages (
      id TEXT PRIMARY KEY, conversation_id TEXT NOT NULL, system_account_id TEXT NOT NULL,
      turn_id TEXT NOT NULL, role TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'completed',
      sequence_no INTEGER DEFAULT 0, content_text TEXT DEFAULT '', content_bytes REAL DEFAULT 0,
      storage_reserved_bytes REAL DEFAULT 0, error_code TEXT, error_message TEXT,
      created_at TEXT NOT NULL, expires_at TEXT NOT NULL, completed_at TEXT)`,
		`CREATE TABLE IF NOT EXISTS chat_message_idempotency (
      idempotency_key TEXT PRIMARY KEY, conversation_id TEXT NOT NULL, turn_id TEXT NOT NULL,
      expires_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS chat_user_storage_windows (
      system_account_id TEXT NOT NULL, bucket_date TEXT NOT NULL,
      content_bytes REAL DEFAULT 0, reserved_bytes REAL DEFAULT 0, updated_at TEXT,
      PRIMARY KEY (system_account_id, bucket_date))`,
		`CREATE TABLE IF NOT EXISTS chat_context_checkpoints (
      id TEXT PRIMARY KEY, conversation_id TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'active',
      expires_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS chat_assets (
      id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, storage_key TEXT DEFAULT '',
      preview_storage_key TEXT DEFAULT '', quota_bytes REAL DEFAULT 0, expires_at TEXT NOT NULL,
      cleanup_status TEXT NOT NULL DEFAULT 'active', cleanup_claim_id TEXT, cleanup_claimed_at TEXT,
      cleanup_attempt_count INTEGER DEFAULT 0, cleanup_retry_at TEXT, cleanup_error_code TEXT,
      updated_at TEXT DEFAULT '')`,
		`CREATE TABLE IF NOT EXISTS chat_user_asset_usage (
      system_account_id TEXT NOT NULL PRIMARY KEY, asset_bytes REAL DEFAULT 0,
      asset_count INTEGER DEFAULT 0, updated_at TEXT DEFAULT '')`)
}

func newKitChatStore(t *testing.T) (*ChatStore, *DB) {
	t.Helper()
	db := openKitSQLite(t, "chat")
	createKitChatSchema(t, db.DB)
	return &ChatStore{DB: db, AssetsRoot: filepath.Join(t.TempDir(), "assets"), Now: kitNow}, db
}

func seedKitConversation(t *testing.T, db *DB, id, activeTurnID, activeStartedAt string) {
	t.Helper()
	mustExecKit(t, db, `INSERT INTO chat_conversations (id, system_account_id, active_turn_id, active_started_at, created_at, updated_at)
    VALUES (?, 'sys-1', ?, ?, '2026-09-01T00:00:00.000Z', '2026-09-01T00:00:00.000Z')`, id, activeTurnID, activeStartedAt)
}

func seedKitMessage(t *testing.T, db *DB, id, conversationID, turnID, role, status, createdAt, expiresAt string,
	contentBytes, reservedBytes float64) {
	t.Helper()
	mustExecKit(t, db, `INSERT INTO chat_messages
    (id, conversation_id, system_account_id, turn_id, role, status, sequence_no, content_text,
     content_bytes, storage_reserved_bytes, created_at, expires_at)
    VALUES (?, ?, 'sys-1', ?, ?, ?, 1, '你好 世界', ?, ?, ?, ?)`,
		id, conversationID, turnID, role, status, contentBytes, reservedBytes, createdAt, expiresAt)
}

// TestChatRetentionRecoversInterruptedTurns：超时未完成轮次标记失败并释放
// 容量预留；IsActiveTurn 命中的轮次跳过。
func TestChatRetentionRecoversInterruptedTurns(t *testing.T) {
	store, db := newKitChatStore(t)
	seedKitConversation(t, db, "conv-1", "turn-1", "2026-09-09T00:00:00.000Z")
	seedKitMessage(t, db, "msg-1", "conv-1", "turn-1", "assistant", "streaming",
		"2026-09-09T01:00:00.000Z", "2026-09-30T00:00:00.000Z", 10, ChatAssistantStorageReservationBytes)
	seedKitConversation(t, db, "conv-2", "turn-2", "2026-09-09T00:00:00.000Z")
	seedKitMessage(t, db, "msg-2", "conv-2", "turn-2", "assistant", "streaming",
		"2026-09-09T01:00:00.000Z", "2026-09-30T00:00:00.000Z", 10, ChatAssistantStorageReservationBytes)
	mustExecKit(t, db, `INSERT INTO chat_user_storage_windows (system_account_id, bucket_date, content_bytes, reserved_bytes, updated_at)
    VALUES ('sys-1','2026-09-09', 10, ?, '2026-09-09T01:00:00.000Z')`, ChatAssistantStorageReservationBytes)

	store.IsActiveTurn = func(ownerID, conversationID, turnID string) bool {
		return conversationID == "conv-2" // 运行态仍活跃的轮次跳过
	}
	result, err := store.CleanupRetention(context.Background(), retention.ChatRetentionInput{
		Now: kitUpdatedAt, InterruptedBefore: "2026-09-10T00:00:00.000Z", Limit: 8, RetentionDays: 30,
	})
	if err != nil {
		t.Fatalf("CleanupRetention: %v", err)
	}
	if result.RecoveredTurns != 1 {
		t.Fatalf("RecoveredTurns = %d, 期望 1", result.RecoveredTurns)
	}
	var status, errorCode string
	var reserved float64
	if err := db.QueryRowContext(context.Background(), `SELECT status, error_code FROM chat_messages WHERE id = 'msg-1'`).Scan(&status, &errorCode); err != nil {
		t.Fatalf("read message: %v", err)
	}
	if status != "failed" || errorCode != "stream_interrupted" {
		t.Fatalf("中断消息状态 = %q/%q", status, errorCode)
	}
	if err := db.QueryRowContext(context.Background(), `SELECT reserved_bytes FROM chat_user_storage_windows
    WHERE system_account_id = 'sys-1' AND bucket_date = '2026-09-09'`).Scan(&reserved); err != nil {
		if err != sql.ErrNoRows {
			t.Fatalf("read window: %v", err)
		}
		reserved = -1
	}
	if reserved != 0 {
		t.Fatalf("容量预留未释放：%v", reserved)
	}
	var activeTurn sql.NullString
	if err := db.QueryRowContext(context.Background(), `SELECT active_turn_id FROM chat_conversations WHERE id = 'conv-2'`).Scan(&activeTurn); err != nil {
		t.Fatalf("read conversation: %v", err)
	}
	if !activeTurn.Valid || activeTurn.String != "turn-2" {
		t.Fatalf("活跃轮次不应被恢复：%v", activeTurn)
	}
}

// TestChatRetentionDeletesExpiredTurns：过期轮次整轮删除（消息/幂等键/
// 容量窗口扣减/会话 revision/空会话清理）。
func TestChatRetentionDeletesExpiredTurns(t *testing.T) {
	store, db := newKitChatStore(t)
	seedKitConversation(t, db, "conv-1", "turn-live", "2026-09-09T00:00:00.000Z")
	seedKitMessage(t, db, "msg-1", "conv-1", "turn-old", "user", "completed",
		"2026-09-01T01:00:00.000Z", "2026-09-09T00:00:00.000Z", 100, 30)
	seedKitMessage(t, db, "msg-2", "conv-1", "turn-old", "assistant", "completed",
		"2026-09-01T01:00:05.000Z", "2026-09-09T00:00:00.000Z", 50, 20)
	mustExecKit(t, db, `INSERT INTO chat_message_idempotency (idempotency_key, conversation_id, turn_id, expires_at)
    VALUES ('idem-1','conv-1','turn-old','2026-09-09T00:00:00.000Z')`)
	mustExecKit(t, db, `INSERT INTO chat_user_storage_windows (system_account_id, bucket_date, content_bytes, reserved_bytes, updated_at)
    VALUES ('sys-1','2026-09-01', 150, 50, '2026-09-01T01:00:00.000Z')`)

	result, err := store.CleanupRetention(context.Background(), retention.ChatRetentionInput{
		Now: kitUpdatedAt, InterruptedBefore: "2026-09-10T00:00:00.000Z", Limit: 8, RetentionDays: 30,
	})
	if err != nil {
		t.Fatalf("CleanupRetention: %v", err)
	}
	if result.DeletedMessages != 2 {
		t.Fatalf("DeletedMessages = %d, 期望 2", result.DeletedMessages)
	}
	if result.DeletedConversations != 1 {
		t.Fatalf("空会话应被清理，DeletedConversations = %d", result.DeletedConversations)
	}
	if result.HasMore {
		t.Fatalf("limit 充足时 HasMore 应为 false")
	}
	if got := mustQueryCountKit(t, db, `SELECT COUNT(*) FROM chat_messages`); got != 0 {
		t.Fatalf("过期消息应删净")
	}
	if got := mustQueryCountKit(t, db, `SELECT COUNT(*) FROM chat_message_idempotency`); got != 0 {
		t.Fatalf("过期幂等键应删净")
	}
	if got := mustQueryCountKit(t, db, `SELECT COUNT(*) FROM chat_user_storage_windows`); got != 0 {
		t.Fatalf("归零容量窗口应被收缩")
	}
	if got := mustQueryCountKit(t, db, `SELECT COUNT(*) FROM chat_conversations`); got != 0 {
		t.Fatalf("空会话应被删除")
	}
}

// TestChatRetentionTitleFallback：标题源消息丢失时回退到最早未过期 user 消息。
func TestChatRetentionTitleFallback(t *testing.T) {
	store, db := newKitChatStore(t)
	mustExecKit(t, db, `INSERT INTO chat_conversations (id, system_account_id, title, title_source_message_id, created_at, updated_at)
    VALUES ('conv-1','sys-1','旧标题','msg-gone','2026-09-09T00:00:00.000Z','2026-09-09T00:00:00.000Z')`)
	seedKitMessage(t, db, "msg-u1", "conv-1", "turn-1", "user", "completed",
		"2026-09-09T01:00:00.000Z", "2026-09-30T00:00:00.000Z", 10, 0)
	// 全部 user 消息已过期的会话 → 无法回退，跳过。
	mustExecKit(t, db, `INSERT INTO chat_conversations (id, system_account_id, title, title_source_message_id, created_at, updated_at)
    VALUES ('conv-2','sys-1','旧标题','msg-gone2','2026-09-09T00:00:00.000Z','2026-09-09T00:00:00.000Z')`)

	if _, err := store.CleanupRetention(context.Background(), retention.ChatRetentionInput{
		Now: kitUpdatedAt, InterruptedBefore: "2026-09-01T00:00:00.000Z", Limit: 8, RetentionDays: 30,
	}); err != nil {
		t.Fatalf("CleanupRetention: %v", err)
	}
	var title, source string
	if err := db.QueryRowContext(context.Background(), `SELECT title, title_source_message_id FROM chat_conversations WHERE id = 'conv-1'`).Scan(&title, &source); err != nil {
		t.Fatalf("read conversation: %v", err)
	}
	if title != "你好 世界" || source != "msg-u1" {
		t.Fatalf("标题回退 = %q / %q", title, source)
	}
}

// TestRecoverStaleCompactions：compacting 超时置为 compact_failed 并重置进度。
func TestRecoverStaleCompactions(t *testing.T) {
	store, db := newKitChatStore(t)
	seedKitConversation(t, db, "conv-1", "", "")
	seedKitConversation(t, db, "conv-2", "", "")
	mustExecKit(t, db, `UPDATE chat_conversations SET context_state = 'compacting',
    context_claimed_at = '2026-09-09T00:00:00.000Z', context_progress_sequence = 7`)

	recovered, err := store.recoverStaleCompactions(context.Background(), kitUpdatedAt, "2026-09-10T00:00:00.000Z", 500)
	if err != nil {
		t.Fatalf("recoverStaleCompactions: %v", err)
	}
	if recovered != 2 {
		t.Fatalf("recovered = %d, 期望 2", recovered)
	}
	var state, progress, retryAt sql.NullString
	if err := db.QueryRowContext(context.Background(), `SELECT context_state, context_progress_sequence, context_retry_at
    FROM chat_conversations WHERE id = 'conv-1'`).Scan(&state, new(any), &retryAt); err != nil {
		t.Fatalf("read conversation: %v", err)
	}
	_ = progress
	if state.String != "compact_failed" || !retryAt.Valid {
		t.Fatalf("压缩恢复状态 = %v / %v", state, retryAt)
	}
}

// TestCleanupExpiredCheckpoints：active 检查点先脱离会话再删除；无法脱离
// （压缩中）的保留；HasMore 按批大小判定。
func TestCleanupExpiredCheckpoints(t *testing.T) {
	store, db := newKitChatStore(t)
	seedKitConversation(t, db, "conv-1", "", "")
	seedKitConversation(t, db, "conv-2", "", "")
	mustExecKit(t, db, `UPDATE chat_conversations SET active_checkpoint_id = 'cp-1' WHERE id = 'conv-1'`)
	mustExecKit(t, db, `UPDATE chat_conversations SET active_checkpoint_id = 'cp-2', context_state = 'compacting' WHERE id = 'conv-2'`)
	for _, id := range []string{"cp-1", "cp-2"} {
		mustExecKit(t, db, `INSERT INTO chat_context_checkpoints (id, conversation_id, status, expires_at)
      VALUES (?, ?, 'active', '2026-09-09T00:00:00.000Z')`, id, map[string]string{"cp-1": "conv-1", "cp-2": "conv-2"}[id])
	}
	// 非 active 检查点无需脱离，可直接删除。
	mustExecKit(t, db, `INSERT INTO chat_context_checkpoints (id, conversation_id, status, expires_at)
    VALUES ('cp-3','conv-1','expired','2026-09-09T00:00:00.000Z')`)

	outcome, err := store.cleanupExpiredCheckpoints(context.Background(), kitUpdatedAt, 10)
	if err != nil {
		t.Fatalf("cleanupExpiredCheckpoints: %v", err)
	}
	if outcome.DeletedCheckpoints != 2 || outcome.HasMore {
		t.Fatalf("outcome = %+v", outcome)
	}
	if got := mustQueryCountKit(t, db, `SELECT COUNT(*) FROM chat_context_checkpoints WHERE id = 'cp-2'`); got != 1 {
		t.Fatalf("无法脱离的 active 检查点应保留")
	}
	var activeCheckpoint, contextState sql.NullString
	if err := db.QueryRowContext(context.Background(), `SELECT active_checkpoint_id, context_state FROM chat_conversations WHERE id = 'conv-1'`).Scan(&activeCheckpoint, &contextState); err != nil {
		t.Fatalf("read conversation: %v", err)
	}
	if activeCheckpoint.Valid || contextState.String != "ready" {
		t.Fatalf("脱离后会话状态 = %v / %v", activeCheckpoint, contextState)
	}

	// rows == limit → HasMore。
	outcome, err = store.cleanupExpiredCheckpoints(context.Background(), kitUpdatedAt, 1)
	if err != nil {
		t.Fatalf("cleanupExpiredCheckpoints(limit=1): %v", err)
	}
	if !outcome.HasMore {
		t.Fatalf("批满应 HasMore")
	}
}

// TestCleanupExpiredAssetsCompletes：认领 → 删文件 → 结算（配额回退）。
func TestCleanupExpiredAssetsCompletes(t *testing.T) {
	store, db := newKitChatStore(t)
	if err := os.MkdirAll(filepath.Join(store.AssetsRoot, "a"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	mainFile := filepath.Join(store.AssetsRoot, "a", "main.bin")
	previewFile := filepath.Join(store.AssetsRoot, "a", "preview.bin")
	for _, path := range []string{mainFile, previewFile} {
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	mustExecKit(t, db, `INSERT INTO chat_assets (
      id, system_account_id, storage_key, preview_storage_key, quota_bytes, expires_at,
      cleanup_status, cleanup_attempt_count, updated_at)
    VALUES ('asset-1','sys-1','a/main.bin','a/preview.bin',100,'2026-09-09T00:00:00.000Z','active',0,'2026-09-01T00:00:00.000Z')`)
	mustExecKit(t, db, `INSERT INTO chat_user_asset_usage (system_account_id, asset_bytes, asset_count, updated_at)
    VALUES ('sys-1',100,1,'2026-09-01T00:00:00.000Z')`)

	outcome, err := store.cleanupExpiredAssets(context.Background(), kitUpdatedAt, 10)
	if err != nil {
		t.Fatalf("cleanupExpiredAssets: %v", err)
	}
	if outcome.ClaimedAssets != 1 || outcome.DeletedAssets != 1 || outcome.FailedAssets != 0 {
		t.Fatalf("outcome = %+v", outcome)
	}
	for _, path := range []string{mainFile, previewFile} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("资产文件应被删除：%s", path)
		}
	}
	if got := mustQueryCountKit(t, db, `SELECT COUNT(*) FROM chat_assets`); got != 0 {
		t.Fatalf("已删资产行应移除")
	}
	if got := mustQueryCountKit(t, db, `SELECT COUNT(*) FROM chat_user_asset_usage`); got != 0 {
		t.Fatalf("归零配额行应移除")
	}
}

// TestCleanupExpiredAssetsFailure：文件删除失败 → 结算为 failed 并按重试
// 退避释放认领。
func TestCleanupExpiredAssetsFailure(t *testing.T) {
	store, db := newKitChatStore(t)
	// storage_key 以 / 开头 → deleteChatAssetObjects 拒绝。
	mustExecKit(t, db, `INSERT INTO chat_assets (
      id, system_account_id, storage_key, quota_bytes, expires_at, cleanup_status,
      cleanup_attempt_count, updated_at)
    VALUES ('asset-bad','sys-1','/etc/passwd',100,'2026-09-09T00:00:00.000Z','active',2,'2026-09-01T00:00:00.000Z')`)
	mustExecKit(t, db, `INSERT INTO chat_user_asset_usage (system_account_id, asset_bytes, asset_count, updated_at)
    VALUES ('sys-1',100,1,'2026-09-01T00:00:00.000Z')`)

	outcome, err := store.cleanupExpiredAssets(context.Background(), kitUpdatedAt, 10)
	if err != nil {
		t.Fatalf("cleanupExpiredAssets: %v", err)
	}
	if outcome.DeletedAssets != 0 || outcome.FailedAssets != 1 {
		t.Fatalf("outcome = %+v", outcome)
	}
	var status, errorCode, retryAt string
	var attempts int64
	if err := db.QueryRowContext(context.Background(), `SELECT cleanup_status, cleanup_error_code, cleanup_retry_at, cleanup_attempt_count
    FROM chat_assets WHERE id = 'asset-bad'`).Scan(&status, &errorCode, &retryAt, &attempts); err != nil {
		t.Fatalf("read asset: %v", err)
	}
	if status != "failed" || errorCode != "chat_asset_cleanup_failed" || attempts != 3 {
		t.Fatalf("失败结算 = %q/%q/%d", status, errorCode, attempts)
	}
	if _, err := time.Parse("2006-01-02T15:04:05.000Z07:00", retryAt); err != nil {
		t.Fatalf("retry_at 非法：%q", retryAt)
	}
	if got := mustQueryCountKit(t, db, `SELECT asset_bytes FROM chat_user_asset_usage`); got != 100 {
		t.Fatalf("失败不应回退配额")
	}
}

// TestCompleteAssetDeletionClaimChanged：认领不匹配时返回 false（不删行）。
func TestCompleteAssetDeletionClaimChanged(t *testing.T) {
	store, db := newKitChatStore(t)
	mustExecKit(t, db, `INSERT INTO chat_assets (
      id, system_account_id, storage_key, quota_bytes, expires_at, cleanup_status,
      cleanup_claim_id, cleanup_attempt_count, updated_at)
    VALUES ('asset-1','sys-1','a.bin',100,'2026-09-09T00:00:00.000Z','claimed','claim-1',1,'2026-09-01T00:00:00.000Z')`)
	deleted, err := store.completeAssetDeletion(context.Background(), "asset-1", "claim-other")
	if err != nil || deleted {
		t.Fatalf("认领不匹配应返回 false：%v, %v", deleted, err)
	}
	deleted, err = store.completeAssetDeletion(context.Background(), "asset-missing", "claim-1")
	if err != nil || deleted {
		t.Fatalf("资产缺失应返回 false：%v, %v", deleted, err)
	}
	if got := mustQueryCountKit(t, db, `SELECT COUNT(*) FROM chat_assets`); got != 1 {
		t.Fatalf("认领不匹配不应删行")
	}
}

// TestDeleteChatAssetObjects：路径校验（..、绝对路径、数量上限）与删除语义。
func TestDeleteChatAssetObjects(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "a"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	existing := filepath.Join(root, "a", "b.bin")
	if err := os.WriteFile(existing, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := deleteChatAssetObjects(root, "a/b.bin", "a/missing.bin"); err != nil {
		t.Fatalf("删除应容忍缺失文件：%v", err)
	}
	if _, err := os.Stat(existing); !os.IsNotExist(err) {
		t.Fatalf("存在的文件应被删除")
	}
	if err := deleteChatAssetObjects(root, "a/../escape.bin"); err == nil {
		t.Fatalf(".. 路径应被拒绝")
	}
	if err := deleteChatAssetObjects(root, "/abs.bin"); err == nil {
		t.Fatalf("绝对路径应被拒绝")
	}
	if err := deleteChatAssetObjects(root, "a\x00b"); err == nil {
		t.Fatalf("空字节应被拒绝")
	}
	if err := deleteChatAssetObjects(root, "1.bin", "2.bin", "3.bin"); err == nil {
		t.Fatalf("超过两个对象应被拒绝")
	}
	// 重复键去重后仍可执行。
	if err := deleteChatAssetObjects(root, "a/missing.bin", "a/missing.bin"); err != nil {
		t.Fatalf("重复键应去重：%v", err)
	}
}

// TestDropExpiredChatPartitions：PG 分区裁剪（过期分区收集会话并 bump
// revision 后 DROP）。
func TestDropExpiredChatPartitions(t *testing.T) {
	rec := newPGRecorder()
	store := &ChatStore{DB: openRecorderPG(rec), Now: kitNow}
	rec.script("FROM pg_inherits", []string{"partition_name"}, [][]driver.Value{
		{"chat_messages_20260101"},
		{"chat_messages_20990101"}, // 未过期 → 跳过
		{"chat_messages_legacy"},   // 命名不匹配 → 跳过
	})
	rec.script("chat_messages_20260101", []string{"conversation_id", "system_account_id"},
		[][]driver.Value{{"conv-1", "sys-1"}, {"conv-2", "sys-1"}})

	tx, err := store.DB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	affected := map[string][2]string{}
	outcome, err := store.dropExpiredChatPartitions(context.Background(), tx, kitUpdatedAt, 30,
		func(conversationID, systemAccountID string) {
			affected[systemAccountID+"\x00"+conversationID] = [2]string{conversationID, systemAccountID}
		})
	if err != nil {
		t.Fatalf("dropExpiredChatPartitions: %v", err)
	}
	if outcome.DroppedPartitions != 1 || len(outcome.AdvancedKeys) != 2 {
		t.Fatalf("outcome = %+v", outcome)
	}
	if len(affected) != 2 {
		t.Fatalf("addAffected 回调 = %v", affected)
	}
	var dropped bool
	for _, statement := range rec.all() {
		if strings.Contains(statement.query, "DROP TABLE IF EXISTS juhe_chat") &&
			strings.Contains(statement.query, "chat_messages_20260101") {
			dropped = true
		}
	}
	if !dropped {
		t.Fatalf("过期分区应被 DROP")
	}
}

// TestPinScheduledLease：nil 直通；非 PG 拒绝。
func TestPinScheduledLease(t *testing.T) {
	db := openKitSQLite(t, "chat_lease")
	if err := pinScheduledLease(context.Background(), db, nil, nil); err != nil {
		t.Fatalf("nil lease 应直通：%v", err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	err = pinScheduledLease(context.Background(), db, tx, &retention.ScheduledLeaseFence{LeaseKey: "k", OwnerID: "o", FencingToken: 1})
	if err == nil || !strings.Contains(err.Error(), "只支持 PostgreSQL") {
		t.Fatalf("SQLite 应拒绝共享租约：%v", err)
	}
}

// TestChatPureHelpers：时间/标题/数值读取等纯函数契约。
func TestChatPureHelpers(t *testing.T) {
	t.Run("cleanupRetryDelayMs", func(t *testing.T) {
		cases := []struct {
			attempts int
			want     int64
		}{
			{-5, 60_000}, {0, 60_000}, {1, 60_000}, {2, 120_000}, {3, 240_000}, {7, 3_600_000}, {8, 3_600_000}, {99, 3_600_000},
		}
		for _, item := range cases {
			if got := cleanupRetryDelayMs(item.attempts); got != item.want {
				t.Fatalf("cleanupRetryDelayMs(%d) = %d, 期望 %d", item.attempts, got, item.want)
			}
		}
	})
	t.Run("requiredChatTimestamp", func(t *testing.T) {
		if _, err := requiredChatTimestamp("2026-09-10T00:00:00.500Z", "x"); err != nil {
			t.Fatalf("合法时间不应报错：%v", err)
		}
		for _, value := range []any{"", "   ", nil, "not-a-time"} {
			if _, err := requiredChatTimestamp(value, "x"); err == nil {
				t.Fatalf("%v 应报错", value)
			}
		}
	})
	t.Run("storageWindowCutoffDate", func(t *testing.T) {
		cutoff, err := storageWindowCutoffDate(kitUpdatedAt, 30)
		if err != nil || cutoff != "2026-08-11" {
			t.Fatalf("cutoff = %q, %v", cutoff, err)
		}
		if _, err := storageWindowCutoffDate("bad", 1); err == nil {
			t.Fatalf("非法时间应报错")
		}
	})
	t.Run("titleFromContent", func(t *testing.T) {
		if got := titleFromContent("  hello\tworld\nnext "); got != "hello world next" {
			t.Fatalf("titleFromContent = %q", got)
		}
		if got := titleFromContent(strings.Repeat("长", 100)); len([]rune(got)) != 60 {
			t.Fatalf("超长标题应截断到 60：%d", len([]rune(got)))
		}
		if got := titleFromContent("\x01\x02"); got != "新对话" {
			t.Fatalf("空标题回退 = %q", got)
		}
	})
	t.Run("requiredAssistantStorageReservation", func(t *testing.T) {
		reserved, err := requiredAssistantStorageReservation(int64(ChatAssistantStorageReservationBytes))
		if err != nil || reserved != ChatAssistantStorageReservationBytes {
			t.Fatalf("预留 = %d, %v", reserved, err)
		}
		if _, err := requiredAssistantStorageReservation(int64(1)); err == nil {
			t.Fatalf("预留不一致应报错")
		}
	})
	t.Run("textOf/optionalText/numberOf", func(t *testing.T) {
		stamp, _ := time.Parse(time.RFC3339, "2026-09-10T08:00:00+08:00")
		if got := textOf(stamp); got != "2026-09-10T00:00:00.000Z" {
			t.Fatalf("textOf(time) = %q", got)
		}
		if got := textOf([]byte("raw")); got != "raw" {
			t.Fatalf("textOf([]byte) = %q", got)
		}
		if got := textOf(int64(7)); got != "7" {
			t.Fatalf("textOf(int64) = %q", got)
		}
		if got := optionalText("   "); got != "" {
			t.Fatalf("optionalText 空白 = %q", got)
		}
		if got := numberOf(nil); got != 0 {
			t.Fatalf("numberOf(nil) = %v", got)
		}
		if got := numberOf([]byte("3.5")); got != 3.5 {
			t.Fatalf("numberOf([]byte) = %v", got)
		}
		if got := numberOf("abc"); got != 0 {
			t.Fatalf("numberOf 非法 = %v", got)
		}
		if !containsKey(map[string][2]float64{"k": {}}, "k") || containsKey(map[string][2]float64{}, "k") {
			t.Fatalf("containsKey 语义错误")
		}
		if got := ISOOf(kitNow()); got != kitUpdatedAt {
			t.Fatalf("ISOOf = %q", got)
		}
		if got := fmt.Sprintf("%s", filepathFromSlash("a/b")); os.PathSeparator == '\\' && got != `a\b` {
			t.Fatalf("filepathFromSlash = %q", got)
		}
	})
}

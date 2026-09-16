package cleanuprepo

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
)

// w12e_chat_gaps_test.go 补齐 chat.go 错误臂：输入校验、主链逐阶段失败
// （SQLite RAISE(ABORT) 触发器按语句注入）、标题/空会话/资产链错误，以及
// PG 模式的分区裁剪失败与租约注入。

func w12eChatInput() retention.ChatRetentionInput {
	return retention.ChatRetentionInput{
		Now: kitUpdatedAt, InterruptedBefore: "2026-09-10T00:00:00.000Z", Limit: 8, RetentionDays: 30,
	}
}

// w12eChatTrigger 在 SQLite 上创建注入失败的触发器。
func w12eChatTrigger(t *testing.T, db *DB, triggerSQL string) {
	t.Helper()
	mustExecKit(t, db, triggerSQL)
}

// TestW12EChatRetentionInputValidation：now/interruptedBefore 非法与 BeginTx 失败。
func TestW12EChatRetentionInputValidation(t *testing.T) {
	store, _ := newKitChatStore(t)
	bad := w12eChatInput()
	bad.Now = "not-a-time"
	if _, err := store.CleanupRetention(context.Background(), bad); err == nil || !strings.Contains(err.Error(), "now必须是") {
		t.Fatalf("now 非法应报错: %v", err)
	}
	bad = w12eChatInput()
	bad.InterruptedBefore = "not-a-time"
	if _, err := store.CleanupRetention(context.Background(), bad); err == nil || !strings.Contains(err.Error(), "interruptedBefore必须是") {
		t.Fatalf("interruptedBefore 非法应报错: %v", err)
	}
	// SQLite + ScheduledLease → 只支持 PostgreSQL。
	leaseInput := w12eChatInput()
	leaseInput.ScheduledLease = &retention.ScheduledLeaseFence{LeaseKey: "k", OwnerID: "o", FencingToken: 1}
	if _, err := store.CleanupRetention(context.Background(), leaseInput); err == nil || !strings.Contains(err.Error(), "只支持 PostgreSQL") {
		t.Fatalf("共享租约应被拒绝: %v", err)
	}
	// BeginTx 失败（库已关闭）。
	if err := store.DB.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := store.CleanupRetention(context.Background(), w12eChatInput()); err == nil {
		t.Fatalf("BeginTx 失败应透传")
	}
}

// TestW12EChatRetentionStaleValidationArms：中断轮次恢复链的校验失败臂。
func TestW12EChatRetentionStaleValidationArms(t *testing.T) {
	// active_started_at 非法。
	store, db := newKitChatStore(t)
	mustExecKit(t, db, `INSERT INTO chat_conversations (id, system_account_id, active_turn_id, active_started_at, created_at, updated_at)
    VALUES ('conv-1','sys-1','turn-1','!bad-time','2026-09-01T00:00:00.000Z','2026-09-01T00:00:00.000Z')`)
	if _, err := store.CleanupRetention(context.Background(), w12eChatInput()); err == nil || !strings.Contains(err.Error(), "active_started_at") {
		t.Fatalf("active_started_at 非法应报错: %v", err)
	}
	// created_at 非法。
	store, db = newKitChatStore(t)
	seedKitConversation(t, db, "conv-1", "turn-1", "2026-09-09T00:00:00.000Z")
	mustExecKit(t, db, `INSERT INTO chat_messages
    (id, conversation_id, system_account_id, turn_id, role, status, sequence_no, content_text,
     content_bytes, storage_reserved_bytes, created_at, expires_at)
    VALUES ('msg-1','conv-1','sys-1','turn-1','assistant','streaming',1,'x',10,?,?,?)`,
		ChatAssistantStorageReservationBytes, "bad-created", "2026-09-30T00:00:00.000Z")
	if _, err := store.CleanupRetention(context.Background(), w12eChatInput()); err == nil || !strings.Contains(err.Error(), "created_at") {
		t.Fatalf("created_at 非法应报错: %v", err)
	}
	// 预留字节不一致。
	store, db = newKitChatStore(t)
	seedKitConversation(t, db, "conv-1", "turn-1", "2026-09-09T00:00:00.000Z")
	seedKitMessage(t, db, "msg-1", "conv-1", "turn-1", "assistant", "streaming",
		"2026-09-09T01:00:00.000Z", "2026-09-30T00:00:00.000Z", 10, 1)
	if _, err := store.CleanupRetention(context.Background(), w12eChatInput()); err == nil || !strings.Contains(err.Error(), "存储预留数据不一致") {
		t.Fatalf("预留不一致应报错: %v", err)
	}
	// 严格释放失败：容量日桶缺失。
	store, db = newKitChatStore(t)
	seedKitConversation(t, db, "conv-1", "turn-1", "2026-09-09T00:00:00.000Z")
	seedKitMessage(t, db, "msg-1", "conv-1", "turn-1", "assistant", "streaming",
		"2026-09-09T01:00:00.000Z", "2026-09-30T00:00:00.000Z", 10, ChatAssistantStorageReservationBytes)
	if _, err := store.CleanupRetention(context.Background(), w12eChatInput()); err == nil || !strings.Contains(err.Error(), "容量预留数据不一致") {
		t.Fatalf("日桶缺失应报错: %v", err)
	}
}

// TestW12EChatRetentionStatementAbortArms：RAISE(ABORT) 注入主链逐语句失败。
func TestW12EChatRetentionStatementAbortArms(t *testing.T) {
	cases := []struct {
		name       string
		trigger    string
		seed       func(t *testing.T, db *DB)
		wantSubstr string
	}{
		{
			name:    "中断消息更新失败",
			trigger: `CREATE TRIGGER w12e_abort_msg_update BEFORE UPDATE ON chat_messages BEGIN SELECT RAISE(ABORT,'w12e boom'); END`,
			seed: func(t *testing.T, db *DB) {
				seedKitConversation(t, db, "conv-1", "turn-1", "2026-09-09T00:00:00.000Z")
				seedKitMessage(t, db, "msg-1", "conv-1", "turn-1", "assistant", "streaming",
					"2026-09-09T01:00:00.000Z", "2026-09-30T00:00:00.000Z", 10, ChatAssistantStorageReservationBytes)
			},
			wantSubstr: "w12e boom",
		},
		{
			name:    "会话活跃轮次清空失败",
			trigger: `CREATE TRIGGER w12e_abort_conv_update BEFORE UPDATE ON chat_conversations BEGIN SELECT RAISE(ABORT,'w12e conv boom'); END`,
			seed: func(t *testing.T, db *DB) {
				seedKitConversation(t, db, "conv-1", "turn-1", "2026-09-09T00:00:00.000Z")
			},
			wantSubstr: "w12e conv boom",
		},
		{
			name:    "容量窗口扣减失败",
			trigger: `CREATE TRIGGER w12e_abort_window_update BEFORE UPDATE ON chat_user_storage_windows BEGIN SELECT RAISE(ABORT,'w12e window boom'); END`,
			seed: func(t *testing.T, db *DB) {
				seedKitConversation(t, db, "conv-1", "turn-live", "2026-09-09T00:00:00.000Z")
				seedKitMessage(t, db, "msg-1", "conv-1", "turn-old", "user", "completed",
					"2026-09-01T01:00:00.000Z", "2026-09-09T00:00:00.000Z", 100, 30)
				mustExecKit(t, db, `INSERT INTO chat_user_storage_windows (system_account_id, bucket_date, content_bytes, reserved_bytes, updated_at)
        VALUES ('sys-1','2026-09-01',150,50,'2026-09-01T01:00:00.000Z')`)
			},
			wantSubstr: "w12e window boom",
		},
		{
			name:    "幂等键删除失败",
			trigger: `CREATE TRIGGER w12e_abort_idem_delete BEFORE DELETE ON chat_message_idempotency BEGIN SELECT RAISE(ABORT,'w12e idem boom'); END`,
			seed: func(t *testing.T, db *DB) {
				seedKitConversation(t, db, "conv-1", "turn-live", "2026-09-09T00:00:00.000Z")
				seedKitMessage(t, db, "msg-1", "conv-1", "turn-old", "user", "completed",
					"2026-09-01T01:00:00.000Z", "2026-09-09T00:00:00.000Z", 0, 0)
				mustExecKit(t, db, `INSERT INTO chat_message_idempotency (idempotency_key, conversation_id, turn_id, expires_at)
        VALUES ('idem-1','conv-1','turn-old','2026-09-09T00:00:00.000Z')`)
			},
			wantSubstr: "w12e idem boom",
		},
		{
			name:    "过期消息删除失败",
			trigger: `CREATE TRIGGER w12e_abort_msg_delete BEFORE DELETE ON chat_messages BEGIN SELECT RAISE(ABORT,'w12e msg del boom'); END`,
			seed: func(t *testing.T, db *DB) {
				seedKitConversation(t, db, "conv-1", "turn-live", "2026-09-09T00:00:00.000Z")
				seedKitMessage(t, db, "msg-1", "conv-1", "turn-old", "user", "completed",
					"2026-09-01T01:00:00.000Z", "2026-09-09T00:00:00.000Z", 0, 0)
			},
			wantSubstr: "w12e msg del boom",
		},
		{
			name:    "空会话清理失败",
			trigger: `CREATE TRIGGER w12e_abort_conv_delete BEFORE DELETE ON chat_conversations BEGIN SELECT RAISE(ABORT,'w12e conv del boom'); END`,
			seed: func(t *testing.T, db *DB) {
				seedKitConversation(t, db, "conv-1", "turn-live", "2026-09-09T00:00:00.000Z")
				seedKitMessage(t, db, "msg-1", "conv-1", "turn-old", "user", "completed",
					"2026-09-01T01:00:00.000Z", "2026-09-09T00:00:00.000Z", 0, 0)
			},
			wantSubstr: "w12e conv del boom",
		},
		{
			name:    "过期幂等键清理失败",
			trigger: `CREATE TRIGGER w12e_abort_idem_delete2 BEFORE DELETE ON chat_message_idempotency BEGIN SELECT RAISE(ABORT,'w12e idem2 boom'); END`,
			seed: func(t *testing.T, db *DB) {
				mustExecKit(t, db, `INSERT INTO chat_message_idempotency (idempotency_key, conversation_id, turn_id, expires_at)
        VALUES ('idem-old','conv-1','turn-old','2026-09-01T00:00:00.000Z')`)
			},
			wantSubstr: "w12e idem2 boom",
		},
		{
			name:    "容量窗口收缩失败",
			trigger: `CREATE TRIGGER w12e_abort_window_delete BEFORE DELETE ON chat_user_storage_windows BEGIN SELECT RAISE(ABORT,'w12e window del boom'); END`,
			seed: func(t *testing.T, db *DB) {
				mustExecKit(t, db, `INSERT INTO chat_user_storage_windows (system_account_id, bucket_date, content_bytes, reserved_bytes, updated_at)
        VALUES ('sys-1','2026-08-01',0,0,'2026-08-01T00:00:00.000Z')`)
			},
			wantSubstr: "w12e window del boom",
		},
		{
			name:    "压缩恢复失败",
			trigger: `CREATE TRIGGER w12e_abort_conv_update2 BEFORE UPDATE ON chat_conversations BEGIN SELECT RAISE(ABORT,'w12e recover boom'); END`,
			seed: func(t *testing.T, db *DB) {
				w12eSeedIdleConversation(t, db, "conv-1")
				mustExecKit(t, db, `UPDATE chat_conversations SET context_state = 'compacting',
        context_claimed_at = '2026-09-09T00:00:00.000Z'`)
			},
			wantSubstr: "w12e recover boom",
		},
		{
			name:    "检查点脱离失败",
			trigger: `CREATE TRIGGER w12e_abort_conv_update3 BEFORE UPDATE ON chat_conversations BEGIN SELECT RAISE(ABORT,'w12e detach boom'); END`,
			seed: func(t *testing.T, db *DB) {
				w12eSeedIdleConversation(t, db, "conv-1")
				mustExecKit(t, db, `UPDATE chat_conversations SET active_checkpoint_id = 'cp-1' WHERE id = 'conv-1'`)
				mustExecKit(t, db, `INSERT INTO chat_context_checkpoints (id, conversation_id, status, expires_at)
        VALUES ('cp-1','conv-1','active','2026-09-09T00:00:00.000Z')`)
			},
			wantSubstr: "w12e detach boom",
		},
		{
			name:    "资产认领失败",
			trigger: `CREATE TRIGGER w12e_abort_asset_update BEFORE UPDATE ON chat_assets BEGIN SELECT RAISE(ABORT,'w12e claim boom'); END`,
			seed: func(t *testing.T, db *DB) {
				mustExecKit(t, db, `INSERT INTO chat_assets (id, system_account_id, storage_key, expires_at, cleanup_status, updated_at)
        VALUES ('asset-1','sys-1','a.bin','2026-09-09T00:00:00.000Z','active','2026-09-01T00:00:00.000Z')`)
			},
			wantSubstr: "w12e claim boom",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, db := newKitChatStore(t)
			tc.seed(t, db)
			w12eChatTrigger(t, db, tc.trigger)
			if _, err := store.CleanupRetention(context.Background(), w12eChatInput()); err == nil || !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Fatalf("应注入失败 %q: %v", tc.wantSubstr, err)
			}
		})
	}
}

// TestW12EChatTitleFallbackArms：标题回退的 expires 校验失败臂。
func TestW12EChatTitleFallbackArms(t *testing.T) {
	store, db := newKitChatStore(t)
	mustExecKit(t, db, `INSERT INTO chat_conversations (id, system_account_id, title, title_source_message_id, created_at, updated_at)
    VALUES ('conv-1','sys-1','旧标题','msg-gone','2026-09-09T00:00:00.000Z','2026-09-09T00:00:00.000Z')`)
	mustExecKit(t, db, `INSERT INTO chat_messages
    (id, conversation_id, system_account_id, turn_id, role, status, sequence_no, content_text,
     content_bytes, storage_reserved_bytes, created_at, expires_at)
    VALUES ('msg-u1','conv-1','sys-1','turn-1','user','completed',1,'标题内容',0,0,'2026-09-09T01:00:00.000Z','bad-expires')`)
	if _, err := store.CleanupRetention(context.Background(), w12eChatInput()); err == nil || !strings.Contains(err.Error(), "expires_at") {
		t.Fatalf("标题候选 expires 非法应报错: %v", err)
	}
}

// TestW12EChatHasMoreArms：limit 饱和时 HasMore 判定。
func TestW12EChatHasMoreArms(t *testing.T) {
	store, db := newKitChatStore(t)
	seedKitConversation(t, db, "conv-1", "turn-live", "2026-09-09T00:00:00.000Z")
	seedKitMessage(t, db, "msg-1", "conv-1", "turn-old", "user", "completed",
		"2026-09-01T01:00:00.000Z", "2026-09-09T00:00:00.000Z", 0, 0)
	result, err := store.CleanupRetention(context.Background(), retention.ChatRetentionInput{
		Now: kitUpdatedAt, InterruptedBefore: "2026-09-10T00:00:00.000Z", Limit: 2, RetentionDays: 30,
	})
	if err != nil {
		t.Fatalf("CleanupRetention: %v", err)
	}
	if !result.HasMore {
		t.Fatalf("limit=2 且 1 条过期轮次应 HasMore")
	}
}

// TestW12EChatPGFailArms：PG 模式的租约/分区/存储窗口失败臂。
func TestW12EChatPGFailArms(t *testing.T) {
	ctx := context.Background()
	// pinScheduledLease：租约查询失败。
	rec := newPGRecorder()
	leaseStore := &ChatStore{DB: openKitFailingRecorderPG(rec, "FROM juhe_stats.background_job_leases"), Now: kitNow}
	leaseInput := w12eChatInput()
	leaseInput.ScheduledLease = &retention.ScheduledLeaseFence{LeaseKey: "k", OwnerID: "o", FencingToken: 1}
	if _, err := leaseStore.CleanupRetention(ctx, leaseInput); err == nil {
		t.Fatalf("租约查询失败应透传")
	}
	// pinScheduledLease：租约失效（ErrNoRows）。
	rec = newPGRecorder()
	leaseStore = &ChatStore{DB: openKitFailingRecorderPG(rec, "__never__"), Now: kitNow}
	if _, err := leaseStore.CleanupRetention(ctx, leaseInput); err == nil || !strings.Contains(err.Error(), "后台任务租约已失效") {
		t.Fatalf("无租约行应报失效: %v", err)
	}
	// pinScheduledLease：参数缺失。
	rec = newPGRecorder()
	pgDB := openRecorderPG(rec)
	tx, err := pgDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := pinScheduledLease(ctx, pgDB, tx, &retention.ScheduledLeaseFence{LeaseKey: " ", OwnerID: "o", FencingToken: 1}); err == nil || !strings.Contains(err.Error(), "leaseKey") {
		t.Fatalf("空 leaseKey 应报错: %v", err)
	}
	if err := pinScheduledLease(ctx, pgDB, tx, &retention.ScheduledLeaseFence{LeaseKey: "k", OwnerID: " ", FencingToken: 1}); err == nil || !strings.Contains(err.Error(), "ownerId") {
		t.Fatalf("空 ownerId 应报错: %v", err)
	}
	// 分区裁剪查询失败 → 主链失败。
	rec = newPGRecorder()
	partitionStore := &ChatStore{DB: openKitFailingRecorderPG(rec, "FROM pg_inherits"), Now: kitNow}
	if _, err := partitionStore.CleanupRetention(ctx, w12eChatInput()); err == nil {
		t.Fatalf("分区查询失败应透传")
	}
	// PG 容量窗口收缩失败。
	rec = newPGRecorder()
	windowStore := &ChatStore{DB: openKitFailingRecorderPG(rec, "DELETE FROM juhe_chat.chat_user_storage_windows AS storage_window"), Now: kitNow}
	if _, err := windowStore.CleanupRetention(ctx, w12eChatInput()); err == nil {
		t.Fatalf("PG 容量窗口收缩失败应透传")
	}
	// PG 压缩恢复查询失败（主事务已提交后的独立事务）。
	rec = newPGRecorder()
	recoverStore := &ChatStore{DB: openKitFailingRecorderPG(rec, "context_state = 'compacting'"), Now: kitNow}
	if _, err := recoverStore.CleanupRetention(ctx, w12eChatInput()); err == nil {
		t.Fatalf("压缩恢复失败应透传")
	}
	// PG 检查点清理查询失败。
	rec = newPGRecorder()
	checkpointStore := &ChatStore{DB: openKitFailingRecorderPG(rec, "FROM juhe_chat.chat_context_checkpoints"), Now: kitNow}
	if _, err := checkpointStore.CleanupRetention(ctx, w12eChatInput()); err == nil {
		t.Fatalf("检查点清理失败应透传")
	}
	// PG 资产认领查询失败。
	rec = newPGRecorder()
	assetStore := &ChatStore{DB: openKitFailingRecorderPG(rec, "FROM juhe_chat.chat_assets"), Now: kitNow}
	if _, err := assetStore.CleanupRetention(ctx, w12eChatInput()); err == nil {
		t.Fatalf("资产清理失败应透传")
	}
}

// TestW12EDropExpiredChatPartitionsArms：分区裁剪的逐阶段失败。
func TestW12EDropExpiredChatPartitionsArms(t *testing.T) {
	ctx := context.Background()
	noopAffected := func(conversationID, systemAccountID string) {}
	build := func(failOn ...string) (*ChatStore, *sql.Tx, *pgRecorder) {
		rec := newPGRecorder()
		store := &ChatStore{DB: openKitFailingRecorderPG(rec, failOn...), Now: kitNow}
		tx, err := store.DB.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		return store, tx, rec
	}
	// 分区行迭代失败：借助列数不匹配制造 Scan 错误。
	{
		rec := newPGRecorder()
		store := &ChatStore{DB: openKitFailingRecorderPG(rec), Now: kitNow}
		rec.script("FROM pg_inherits", []string{"a", "b"}, [][]driver.Value{{"chat_messages_20260101", "x"}})
		tx, err := store.DB.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		if _, err := store.dropExpiredChatPartitions(ctx, tx, kitUpdatedAt, 30, noopAffected); err == nil {
			t.Fatalf("分区行扫描失败应透传")
		}
	}
	// 会话收集查询失败。
	{
		store, tx, rec := build("SELECT DISTINCT conversation_id, system_account_id FROM juhe_chat")
		defer func() { _ = tx.Rollback() }()
		rec.script("FROM pg_inherits", []string{"partition_name"}, [][]driver.Value{{"chat_messages_20260101"}})
		if _, err := store.dropExpiredChatPartitions(ctx, tx, kitUpdatedAt, 30, noopAffected); err == nil {
			t.Fatalf("会话收集失败应透传")
		}
	}
	// 会话 revision 推进失败。
	{
		rec := newPGRecorder()
		rec.script("FROM pg_inherits", []string{"partition_name"}, [][]driver.Value{{"chat_messages_20260101"}})
		rec.script("SELECT DISTINCT conversation_id, system_account_id FROM juhe_chat", []string{"conversation_id", "system_account_id"}, [][]driver.Value{{"conv-1", "sys-1"}})
		store := &ChatStore{DB: openKitFailingRecorderPG(rec, "UPDATE juhe_chat.chat_conversations"), Now: kitNow}
		tx, err := store.DB.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		if _, err := store.dropExpiredChatPartitions(ctx, tx, kitUpdatedAt, 30, noopAffected); err == nil {
			t.Fatalf("revision 推进失败应透传")
		}
	}
	// DROP 失败。
	{
		rec := newPGRecorder()
		rec.script("FROM pg_inherits", []string{"partition_name"}, [][]driver.Value{{"chat_messages_20260101"}})
		store := &ChatStore{DB: openKitFailingRecorderPG(rec, "DROP TABLE IF EXISTS juhe_chat"), Now: kitNow}
		tx, err := store.DB.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		if _, err := store.dropExpiredChatPartitions(ctx, tx, kitUpdatedAt, 30, noopAffected); err == nil {
			t.Fatalf("DROP 失败应透传")
		}
	}
}

// TestW12ECompleteAndReleaseAssetArms：资产结算/释放的错误臂。
func TestW12ECompleteAndReleaseAssetArms(t *testing.T) {
	ctx := context.Background()
	// 结算：DELETE 资产失败。
	{
		store, db := newKitChatStore(t)
		seedW12EClaimedAsset(t, db)
		w12eChatTrigger(t, db, `CREATE TRIGGER w12e_abort_asset_delete BEFORE DELETE ON chat_assets BEGIN SELECT RAISE(ABORT,'w12e settle del boom'); END`)
		if _, err := store.completeAssetDeletion(ctx, "asset-1", "claim-1"); err == nil {
			t.Fatalf("资产删除失败应透传")
		}
	}
	// 结算：配额回退失败。
	{
		store, db := newKitChatStore(t)
		seedW12EClaimedAsset(t, db)
		mustExecKit(t, db, `INSERT INTO chat_user_asset_usage (system_account_id, asset_bytes, asset_count, updated_at)
    VALUES ('sys-1',100,1,'2026-09-01T00:00:00.000Z')`)
		w12eChatTrigger(t, db, `CREATE TRIGGER w12e_abort_quota_update BEFORE UPDATE ON chat_user_asset_usage BEGIN SELECT RAISE(ABORT,'w12e quota boom'); END`)
		if _, err := store.completeAssetDeletion(ctx, "asset-1", "claim-1"); err == nil {
			t.Fatalf("配额回退失败应透传")
		}
	}
	// 结算：归零配额行删除失败。
	{
		store, db := newKitChatStore(t)
		seedW12EClaimedAsset(t, db)
		mustExecKit(t, db, `INSERT INTO chat_user_asset_usage (system_account_id, asset_bytes, asset_count, updated_at)
    VALUES ('sys-1',100,1,'2026-09-01T00:00:00.000Z')`)
		w12eChatTrigger(t, db, `CREATE TRIGGER w12e_abort_quota_delete BEFORE DELETE ON chat_user_asset_usage BEGIN SELECT RAISE(ABORT,'w12e quota del boom'); END`)
		if _, err := store.completeAssetDeletion(ctx, "asset-1", "claim-1"); err == nil {
			t.Fatalf("配额行删除失败应透传")
		}
	}
	// 释放认领：UPDATE 失败。
	{
		store, db := newKitChatStore(t)
		seedW12EClaimedAsset(t, db)
		w12eChatTrigger(t, db, `CREATE TRIGGER w12e_abort_asset_update2 BEFORE UPDATE ON chat_assets BEGIN SELECT RAISE(ABORT,'w12e release boom'); END`)
		if _, err := store.releaseAssetDeletionClaim(ctx, "asset-1", "claim-1", "", "2026-09-10T01:00:00.000Z", kitUpdatedAt); err == nil {
			t.Fatalf("释放失败应透传")
		}
	}
	// 释放认领：errorCode 截断到 120。
	{
		store, db := newKitChatStore(t)
		seedW12EClaimedAsset(t, db)
		changed, err := store.releaseAssetDeletionClaim(ctx, "asset-1", "claim-1", strings.Repeat("x", 300), "2026-09-10T01:00:00.000Z", kitUpdatedAt)
		if err != nil || !changed {
			t.Fatalf("释放应成功: %v %v", changed, err)
		}
		var errorCode string
		if err := db.QueryRowContext(ctx, `SELECT cleanup_error_code FROM chat_assets WHERE id = 'asset-1'`).Scan(&errorCode); err != nil {
			t.Fatal(err)
		}
		if len(errorCode) != 120 {
			t.Fatalf("errorCode 应截断到 120: %d", len(errorCode))
		}
	}
	// 释放认领：BeginTx/执行层失败由调用方注入；此处补 changed != 1（缺失认领）。
	{
		store, _ := newKitChatStore(t)
		changed, err := store.releaseAssetDeletionClaim(ctx, "asset-missing", "claim-x", "", "2026-09-10T01:00:00.000Z", kitUpdatedAt)
		if err != nil || changed {
			t.Fatalf("缺失认领应返回 false: %v %v", changed, err)
		}
	}
}

// w12eSeedIdleConversation 插入无活跃轮次的会话（active_turn_id 显式 NULL）。
func w12eSeedIdleConversation(t *testing.T, db *DB, id string) {
	t.Helper()
	mustExecKit(t, db, `INSERT INTO chat_conversations (id, system_account_id, active_turn_id, active_started_at, created_at, updated_at)
    VALUES (?, 'sys-1', NULL, NULL, '2026-09-30T00:00:00.000Z', '2026-09-30T00:00:00.000Z')`, id)
}

func seedW12EClaimedAsset(t *testing.T, db *DB) {
	t.Helper()
	mustExecKit(t, db, `INSERT INTO chat_assets (
      id, system_account_id, storage_key, quota_bytes, expires_at, cleanup_status,
      cleanup_claim_id, cleanup_attempt_count, updated_at)
    VALUES ('asset-1','sys-1','a.bin',100,'2026-09-09T00:00:00.000Z','claimed','claim-1',1,'2026-09-01T00:00:00.000Z')`)
}

// TestW12EReleaseStorageWindowStrictArms：严格释放的直接臂。
func TestW12EReleaseStorageWindowStrictArms(t *testing.T) {
	store, db := newKitChatStore(t)
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := store.releaseStorageWindowReservationStrict(context.Background(), tx, "sys-1", "2026-09-09T01:00:00.000Z", 100, kitUpdatedAt); err == nil || !strings.Contains(err.Error(), "容量预留数据不一致") {
		t.Fatalf("日桶缺失应报错: %v", err)
	}
	// 单连接句柄：先回滚占住连接的事务再继续写库。
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	mustExecKit(t, db, `INSERT INTO chat_user_storage_windows (system_account_id, bucket_date, content_bytes, reserved_bytes, updated_at)
    VALUES ('sys-1','2026-09-09', 0, 100, '2026-09-09T01:00:00.000Z')`)
	// 事务内重新执行（前一次失败已回滚到保存点之外，重新开事务）。
	tx2, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx2.Rollback() }()
	if err := store.releaseStorageWindowReservationStrict(context.Background(), tx2, "sys-1", "2026-09-09T01:00:00.000Z", 100, kitUpdatedAt); err != nil {
		t.Fatalf("严格释放应成功: %v", err)
	}
	// 严格释放不负责提交（由调用方事务提交）；单连接句柄下先提交再查询。
	if err := tx2.Commit(); err != nil {
		t.Fatalf("commit tx2: %v", err)
	}
	if got := mustQueryCountKit(t, db, `SELECT COUNT(*) FROM chat_user_storage_windows`); got != 0 {
		t.Fatalf("归零日桶应被删除")
	}
	_ = time.Second
	_ = sql.ErrNoRows
}

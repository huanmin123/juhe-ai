package cleanuprepo

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
)

// w13e_chat_codex_arms_test.go 覆盖 chat.go / codexcontext.go 剩余错误臂与
// 数据驱动分支：failOn 子串注入、RAISE(IGNORE) 并发冲突模拟、脚本化行集、
// failBegin / failCommit 装饰句柄。

// ---- chat fixture ----

func w13eChatFixture(t *testing.T) (*ChatStore, *DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "w13e-chat.sqlite3")
	seed, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open chat seed: %v", err)
	}
	t.Cleanup(func() { _ = seed.Close() })
	seed.SetMaxOpenConns(1)
	createKitChatSchema(t, seed)
	assetsRoot := filepath.Join(t.TempDir(), "w13e-assets")
	if err := os.MkdirAll(assetsRoot, 0o755); err != nil {
		t.Fatalf("mkdir assets: %v", err)
	}
	store := &ChatStore{DB: &DB{DB: seed}, AssetsRoot: assetsRoot, Now: kitNow}
	return store, &DB{DB: seed}, path
}

// w13eChatSeedSweep 种入覆盖各链路所需的数据：中断轮次 + 过期轮次 + 标题回退。
func w13eChatSeedSweep(t *testing.T, seed *DB) {
	t.Helper()
	seedKitConversation(t, seed, "conv-1", "turn-1", "2026-09-09T00:00:00.000Z")
	// 中断的 streaming assistant 消息（带预留字节）。
	mustExecKit(t, seed, `INSERT INTO chat_messages
    (id, conversation_id, system_account_id, turn_id, role, status, sequence_no, content_text,
     content_bytes, storage_reserved_bytes, created_at, expires_at)
    VALUES ('m-assist', 'conv-1', 'sys-1', 'turn-1', 'assistant', 'streaming', 2, 'x', 10, ?, ?, ?)`,
		ChatAssistantStorageReservationBytes, "2026-09-09T00:00:00.000Z", "2027-01-01T00:00:00.000Z")
	// 容量窗口行（reserved 足够释放）。
	mustExecKit(t, seed, `INSERT INTO chat_user_storage_windows (system_account_id, bucket_date, content_bytes, reserved_bytes, updated_at)
    VALUES ('sys-1', '2026-09-09', 20, ?, '2026-09-09T00:00:00.000Z')`, ChatAssistantStorageReservationBytes)
	// 过期轮次消息（expires_at 早于 now）。
	seedKitMessage(t, seed, "m-old", "conv-1", "turn-old", "user", "completed", "2026-08-30T00:00:00.000Z", "2026-09-01T00:00:00.000Z", 4, 0)
	// 标题回退：title_source_message_id 指向已删除消息 + 一条存活 user 消息。
	mustExecKit(t, seed, `UPDATE chat_conversations SET title_source_message_id = 'msg-gone' WHERE id = 'conv-1'`)
	seedKitMessage(t, seed, "m-user", "conv-1", "turn-1", "user", "completed", "2026-09-09T00:00:00.000Z", "2027-01-01T00:00:00.000Z", 4, 0)
	// 过期的幂等键。
	mustExecKit(t, seed, `INSERT INTO chat_message_idempotency (idempotency_key, conversation_id, turn_id, expires_at)
    VALUES ('idem-1', 'conv-1', 'turn-old', '2026-09-01T00:00:00.000Z')`)
}

func w13eChatInput() retention.ChatRetentionInput {
	return retention.ChatRetentionInput{
		Now: kitUpdatedAt, InterruptedBefore: "2026-09-10T00:00:00.000Z", Limit: 8, RetentionDays: 30,
	}
}

// TestW13eChatRetentionLimitClamps：limit 下限/上限钳制与剩余链路。
func TestW13eChatRetentionLimitClamps(t *testing.T) {
	ctx := context.Background()
	for _, limit := range []int{1, 2000, 600} {
		store, seed, _ := w13eChatFixture(t)
		w13eChatSeedSweep(t, seed)
		input := w13eChatInput()
		input.Limit = limit
		if _, err := store.CleanupRetention(ctx, input); err != nil {
			t.Fatalf("limit=%d 不应报错: %v", limit, err)
		}
	}
}

// TestW13eChatRetentionFailOnArms：主链逐语句失败臂。
func TestW13eChatRetentionFailOnArms(t *testing.T) {
	stages := []pgStage{
		{"assistant select", "SELECT * FROM chat_messages"},
		{"expired turns", "GROUP BY conversation_id, system_account_id, turn_id"},
		{"turn messages", "SELECT created_at, expires_at, content_bytes, storage_reserved_bytes"},
		{"conversation active clear", "SET active_turn_id = NULL, active_started_at = NULL, updated_at = ?"},
		{"conversation revision", "SET message_revision = message_revision + 1, updated_at = ?"},
		{"stale titles", "title_source_message_id IS NOT NULL"},
		{"first user", "role = 'user' AND expires_at > ?"},
		{"title update", "SET title = ?, title_source_message_id = ?"},
		{"empty conversation delete", "WHERE active_turn_id IS NULL AND created_at <= ?"},
		{"storage reservation release", "SET reserved_bytes = reserved_bytes - ?"},
	}
	runSQLiteStages(t, stages, func(t *testing.T, stage pgStage) error {
		store, seed, path := w13eChatFixture(t)
		w13eChatSeedSweep(t, seed)
		store.DB = w13eOpenDecoratedSQLite(t, path, w13eSQLiteOptions{failOn: stage.failOn})
		_, err := store.CleanupRetention(context.Background(), w13eChatInput())
		return err
	})
}

// TestW13eChatRetentionBadTimestamps：行级时间戳校验失败臂。
func TestW13eChatRetentionBadTimestamps(t *testing.T) {
	ctx := context.Background()
	// expires_at 非法（'!' 字典序早于 now，能被过期轮次选中）。
	{
		store, seed, _ := w13eChatFixture(t)
		seedKitMessage(t, seed, "m-bad-exp", "conv-1", "turn-old", "user", "completed", "2026-08-30T00:00:00.000Z", "!bad", 4, 0)
		if _, err := store.CleanupRetention(ctx, w13eChatInput()); err == nil ||
			!strings.Contains(err.Error(), "expires_at") {
			t.Fatalf("非法 expires_at 应报错: %v", err)
		}
	}
	// created_at 非法。
	{
		store, seed, _ := w13eChatFixture(t)
		seedKitMessage(t, seed, "m-bad-created", "conv-1", "turn-old", "user", "completed", "!bad", "2026-09-01T00:00:00.000Z", 4, 0)
		if _, err := store.CleanupRetention(ctx, w13eChatInput()); err == nil ||
			!strings.Contains(err.Error(), "created_at") {
			t.Fatalf("非法 created_at 应报错: %v", err)
		}
	}
}

// TestW13eChatMaintenanceEntryArms：压缩恢复 / 检查点 / 资产清理入口臂。
func TestW13eChatMaintenanceEntryArms(t *testing.T) {
	ctx := context.Background()
	// BeginTx 失败（句柄已关闭）。
	{
		store, seed, _ := w13eChatFixture(t)
		_ = seed
		if err := store.DB.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
		if _, err := store.recoverStaleCompactions(ctx, kitUpdatedAt, kitUpdatedAt, 5); err == nil {
			t.Fatalf("recoverStaleCompactions BeginTx 失败应透传")
		}
	}
	{
		store, _, _ := w13eChatFixture(t)
		if err := store.DB.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
		if _, err := store.cleanupExpiredCheckpoints(ctx, kitUpdatedAt, 5); err == nil {
			t.Fatalf("cleanupExpiredCheckpoints BeginTx 失败应透传")
		}
	}
	{
		store, _, _ := w13eChatFixture(t)
		if err := store.DB.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
		if _, err := store.cleanupExpiredAssets(ctx, kitUpdatedAt, 5); err == nil {
			t.Fatalf("cleanupExpiredAssets BeginTx 失败应透传")
		}
	}
	// asset 上限钳制。
	{
		store, _, _ := w13eChatFixture(t)
		if _, err := store.cleanupExpiredAssets(ctx, kitUpdatedAt, 600); err != nil {
			t.Fatalf("cleanupExpiredAssets limit=600 不应报错: %v", err)
		}
	}
	// checkpoint 空 id 跳过（脚本化行集）。
	{
		store, _, path := w13eChatFixture(t)
		decorated := w13eOpenDecoratedSQLite(t, path, w13eSQLiteOptions{rowsScripts: []w13eRowsScript{{
			match:   "WHERE expires_at <= ?",
			columns: []string{"id", "conversation_id", "status"},
			values:  [][]driver.Value{{"", "conv-1", "active"}}, errAfterRows: -1,
		}}})
		store.DB = decorated
		outcome, err := store.cleanupExpiredCheckpoints(ctx, kitUpdatedAt, 5)
		if err != nil || outcome.DeletedCheckpoints != 0 {
			t.Fatalf("空 id 应跳过: %+v %v", outcome, err)
		}
	}
}

// TestW13eChatAssetArms：资产认领/删除/释放的错误与并发冲突分支。
func TestW13eChatAssetArms(t *testing.T) {
	ctx := context.Background()
	seedAsset := func(t *testing.T, store *ChatStore, status, storageKey string) {
		t.Helper()
		mustExecKit(t, store.DB, `INSERT INTO chat_assets
      (id, system_account_id, storage_key, quota_bytes, expires_at, cleanup_status, updated_at)
      VALUES ('asset-1', 'sys-1', ?, 10, '2026-09-01T00:00:00.000Z', ?, '2026-09-01T00:00:00.000Z')`, storageKey, status)
	}
	// 认领并发冲突：RAISE(IGNORE) 让认领 UPDATE 影响 0 行。
	{
		store, seed, path := w13eChatFixture(t)
		seedAsset(t, store, "active", "")
		w13eTrigger(t, seed.DB, `CREATE TRIGGER w13e_asset_claim_ignore BEFORE UPDATE ON chat_assets
      BEGIN SELECT RAISE(IGNORE); END`)
		store.DB = w13eOpenDecoratedSQLite(t, path, w13eSQLiteOptions{})
		_, err := store.cleanupExpiredAssets(ctx, kitUpdatedAt, 5)
		if err == nil || !strings.Contains(err.Error(), "并发冲突") {
			t.Fatalf("认领并发冲突应报错: %v", err)
		}
	}
	// 认领后 Commit 失败。
	{
		store, seed, path := w13eChatFixture(t)
		seedAsset(t, store, "active", "")
		_ = seed
		store.DB = w13eOpenDecoratedSQLite(t, path, w13eSQLiteOptions{failCommit: true})
		if _, err := store.cleanupExpiredAssets(ctx, kitUpdatedAt, 5); err == nil {
			t.Fatalf("认领 Commit 失败应透传")
		}
	}
	// storage_key 指向非空目录 → 删除失败 → FailedAssets + 释放认领。
	{
		store, seed, path := w13eChatFixture(t)
		badDir := filepath.Join(store.AssetsRoot, "w13e-bad")
		if err := os.MkdirAll(badDir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(badDir, "keep.txt"), []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		seedAsset(t, store, "active", "w13e-bad")
		_ = seed
		store.DB = w13eOpenDecoratedSQLite(t, path, w13eSQLiteOptions{})
		outcome, err := store.cleanupExpiredAssets(ctx, kitUpdatedAt, 5)
		if err != nil || outcome.FailedAssets != 1 || outcome.DeletedAssets != 0 {
			t.Fatalf("文件删除失败应计入 FailedAssets: %+v %v", outcome, err)
		}
	}
	// 结算 DELETE 被 RAISE(IGNORE) → 认领已变化分支。
	{
		store, seed, path := w13eChatFixture(t)
		seedAsset(t, store, "active", "")
		w13eTrigger(t, seed.DB, `CREATE TRIGGER w13e_asset_del_ignore BEFORE DELETE ON chat_assets
      BEGIN SELECT RAISE(IGNORE); END`)
		store.DB = w13eOpenDecoratedSQLite(t, path, w13eSQLiteOptions{})
		outcome, err := store.cleanupExpiredAssets(ctx, kitUpdatedAt, 5)
		if err != nil || outcome.FailedAssets != 1 {
			t.Fatalf("认领已变化应计入 FailedAssets: %+v %v", outcome, err)
		}
	}
	// completeAssetDeletion 直调：句柄关闭 / 认领删除 0 行。
	{
		store, _, _ := w13eChatFixture(t)
		if err := store.DB.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
		if _, err := store.completeAssetDeletion(ctx, "asset-1", "claim-1"); err == nil {
			t.Fatalf("completeAssetDeletion 句柄关闭应报错")
		}
	}
	{
		store, seed, path := w13eChatFixture(t)
		seedAsset(t, store, "claimed", "")
		mustExecKit(t, seed, `UPDATE chat_assets SET cleanup_claim_id = 'claim-1' WHERE id = 'asset-1'`)
		w13eTrigger(t, seed.DB, `CREATE TRIGGER w13e_asset_del_ignore2 BEFORE DELETE ON chat_assets
      BEGIN SELECT RAISE(IGNORE); END`)
		store.DB = w13eOpenDecoratedSQLite(t, path, w13eSQLiteOptions{})
		deleted, err := store.completeAssetDeletion(ctx, "asset-1", "claim-1")
		if err != nil || deleted {
			t.Fatalf("删除 0 行应返回 false: %v %v", deleted, err)
		}
	}
	// 释放认领：错误码截断与状态不变。
	{
		store, seed, path := w13eChatFixture(t)
		seedAsset(t, store, "claimed", "")
		mustExecKit(t, seed, `UPDATE chat_assets SET cleanup_claim_id = 'claim-1' WHERE id = 'asset-1'`)
		store.DB = w13eOpenDecoratedSQLite(t, path, w13eSQLiteOptions{})
		changed, err := store.releaseAssetDeletionClaim(ctx, "asset-1", "claim-1", strings.Repeat("e", 200), kitUpdatedAt, kitUpdatedAt)
		if err != nil || !changed {
			t.Fatalf("释放认领应成功: %v %v", changed, err)
		}
	}
	// 文件删除辅助：非空目录失败、路径逃逸与空键。
	{
		root := t.TempDir()
		badDir := filepath.Join(root, "dir")
		if err := os.MkdirAll(badDir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(badDir, "x.txt"), []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := deleteChatAssetObjects(root, "dir"); err == nil {
			t.Fatalf("非空目录删除应失败")
		}
		if err := deleteChatAssetObjects(root, "/abs", "a/../b"); err == nil {
			t.Fatalf("路径逃逸应失败")
		}
		if err := deleteChatAssetObjects(root, "", "missing.txt"); err != nil {
			t.Fatalf("缺失文件不应报错: %v", err)
		}
	}
	// 纯函数：标题清理与随机回退源。
	if got := titleFromContent("a\r\nb c"); got != "a b c" && got != "a" {
		t.Fatalf("titleFromContent = %q", got)
	}
}

// TestW13eCodexSweepArms：codex SQLite 半区逐语句失败臂。
func TestW13eCodexSweepArms(t *testing.T) {
	ctx := context.Background()
	seedOne := func(t *testing.T) *CodexContextStore {
		store := newKitCodexStore(t, 1)
		seedKitCodexSession(t, store, 0, "s-1", "2026-01-01T00:00:00.000Z")
		seedKitCodexRow(t, store, 0, "codex_context_responses", "r-1", "s-1", "k-1", "2026-01-01T00:00:00.000Z")
		seedKitCodexRow(t, store, 0, "codex_context_compacts", "c-1", "s-1", "k-1", "2026-01-01T00:00:00.000Z")
		// 仍在有效期的引用行：让 refresh（而非 delete）分支被选中；
		// s-2 只有未过期引用行，让 delete 分支同样有会话可走。
		seedKitCodexSession(t, store, 0, "s-2", "2026-01-01T00:00:00.000Z")
		seedKitCodexRow(t, store, 0, "codex_context_responses", "r-3", "s-2", "k-3", "2027-01-01T00:00:00.000Z")
		mustExecKit(t, mustShard(t, store, 0), `INSERT INTO codex_context_storage_cleanup_queue
      (storage_key, enqueued_at, updated_at, next_attempt_at) VALUES ('k-1', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`)
		return store
	}
	stages := []pgStage{
		{"expired sessions", "FROM codex_context_sessions\n      WHERE expires_at < ?"},
		{"storage keys responses", "SELECT storage_key FROM codex_context_responses"},
		{"storage keys compacts", "SELECT storage_key FROM codex_context_compacts"},
		{"enqueue keys", "INSERT INTO codex_context_storage_cleanup_queue"},
		{"delete responses", "DELETE FROM codex_context_responses WHERE session_id IN"},
		{"remaining responses", "FROM codex_context_responses\n          WHERE session_id IN"},
		{"remaining compacts", "FROM codex_context_compacts\n          WHERE session_id IN"},
		{"refresh sessions", "SET updated_at = ?, expires_at = ?"},
		{"delete sessions", "DELETE FROM codex_context_sessions WHERE id IN"},
		{"pending queue", "FROM codex_context_storage_cleanup_queue\n      WHERE next_attempt_at <= ?"},
		{"distinct responses", "SELECT DISTINCT storage_key\n          FROM codex_context_responses"},
		{"distinct compacts", "SELECT DISTINCT storage_key\n          FROM codex_context_compacts"},
		{"queue delete", "DELETE FROM codex_context_storage_cleanup_queue WHERE storage_key IN"},
	}
	runSQLiteStages(t, stages, func(t *testing.T, stage pgStage) error {
		store := seedOne(t)
		if store.shards == nil {
			store.shards = map[int]*sql.DB{}
		}
		for _, cached := range store.shards {
			_ = cached.Close()
		}
		store.shards = map[int]*sql.DB{
			0: w13eOpenDecoratedRawSQLite(t, store.shardPath(0), w13eSQLiteOptions{failOn: stage.failOn}),
		}
		_, err := store.CleanupExpiredStates(ctx, "2026-09-10T00:00:00.000Z", 10)
		if err == nil {
			// CleanupExpiredStates 成功后结算阶段同样尝试注入。
			_, err = store.SettleStorageCleanup(ctx, Settlement{
				SucceededStorageKeys: []string{"k-9"},
				Failures:             []SettlementFailure{{StorageKey: "k-1", Error: "boom"}},
				Now:                  kitUpdatedAt,
			})
		}
		return err
	})
}

// mustShard 返回（并缓存）store 的分片句柄。
func mustShard(t *testing.T, store *CodexContextStore, index int) *sql.DB {
	t.Helper()
	db, err := store.shard(index)
	if err != nil {
		t.Fatalf("shard %d: %v", index, err)
	}
	return db
}

// TestW13eCodexTxFailArms：codex Begin/Commit/changes 失败臂。
func TestW13eCodexTxFailArms(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		opts w13eSQLiteOptions
	}{
		{"Begin 失败", w13eSQLiteOptions{failBegin: true}},
		{"Commit 失败", w13eSQLiteOptions{failCommit: true}},
		{"changes 失败", w13eSQLiteOptions{rowsAffectedFailOn: "DELETE FROM codex_context_sessions WHERE id IN"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newKitCodexStore(t, 1)
			seedKitCodexSession(t, store, 0, "s-1", "2026-01-01T00:00:00.000Z")
			seedKitCodexRow(t, store, 0, "codex_context_responses", "r-1", "s-1", "k-1", "2026-01-01T00:00:00.000Z")
			if store.shards == nil {
				store.shards = map[int]*sql.DB{}
			}
			for _, cached := range store.shards {
				_ = cached.Close()
			}
			store.shards = map[int]*sql.DB{
				0: w13eOpenDecoratedRawSQLite(t, store.shardPath(0), tc.opts),
			}
			if _, err := store.CleanupExpiredStates(ctx, "2026-09-10T00:00:00.000Z", 10); err == nil {
				t.Fatalf("%s 应透传错误", tc.name)
			}
		})
	}
	// SettleStorageCleanup 各失败臂。
	for _, tc := range []struct {
		name string
		opts w13eSQLiteOptions
	}{
		{"结算 Begin 失败", w13eSQLiteOptions{failBegin: true}},
		{"结算 Commit 失败", w13eSQLiteOptions{failCommit: true}},
		{"queue 删除失败", w13eSQLiteOptions{failOn: "DELETE FROM codex_context_storage_cleanup_queue WHERE storage_key IN"}},
		{"attempt 查询失败", w13eSQLiteOptions{failOn: "SELECT attempt_count\n    FROM codex_context_storage_cleanup_queue"}},
		{"attempt 更新失败", w13eSQLiteOptions{failOn: "SET attempt_count = attempt_count + 1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newKitCodexStore(t, 1)
			if store.shards == nil {
				store.shards = map[int]*sql.DB{}
			}
			for _, cached := range store.shards {
				_ = cached.Close()
			}
			store.shards = map[int]*sql.DB{
				0: w13eOpenDecoratedRawSQLite(t, store.shardPath(0), tc.opts),
			}
			if _, err := store.SettleStorageCleanup(ctx, Settlement{
				SucceededStorageKeys: []string{"k-1"},
				Failures:             []SettlementFailure{{StorageKey: "k-1", Error: "boom"}},
			}); err == nil {
				t.Fatalf("%s 应透传错误", tc.name)
			}
		})
	}
}

// TestW13eCodexScriptedRowArms：codex scan / rows.Err / 空键跳过分支。
func TestW13eCodexScriptedRowArms(t *testing.T) {
	ctx := context.Background()
	scripts := map[string]w13eRowsScript{
		"expired scan": {match: "SELECT id, expires_at", columns: []string{"id", "expires_at"},
			values: [][]driver.Value{{struct{}{}, "2026-01-01T00:00:00.000Z"}}, errAfterRows: -1},
		"expired rowsErr": {match: "SELECT id, expires_at", columns: []string{"id", "expires_at"},
			values: [][]driver.Value{{"s-1", "2026-01-01T00:00:00.000Z"}}, errAfterRows: 1},
		"remaining scan": {match: "GROUP BY session_id", columns: []string{"session_id", "expires_at"},
			values: [][]driver.Value{{struct{}{}, "2026-01-01T00:00:00.000Z"}}, errAfterRows: -1},
		"remaining empty": {match: "GROUP BY session_id", columns: []string{"session_id", "expires_at"},
			values: [][]driver.Value{{"", ""}}, errAfterRows: -1},
		"remaining rowsErr": {match: "GROUP BY session_id", columns: []string{"session_id", "expires_at"},
			values: [][]driver.Value{{"s-1", "2026-01-01T00:00:00.000Z"}}, errAfterRows: 1},
		"pending scan": {match: "WHERE next_attempt_at <= ?", columns: []string{"storage_key"},
			values: [][]driver.Value{{struct{}{}}}, errAfterRows: -1},
		"pending rowsErr": {match: "WHERE next_attempt_at <= ?", columns: []string{"storage_key"},
			values: [][]driver.Value{{"k-1"}}, errAfterRows: 1},
		"distinct scan": {match: "SELECT DISTINCT storage_key", columns: []string{"storage_key"},
			values: [][]driver.Value{{struct{}{}}}, errAfterRows: -1},
		"distinct rowsErr": {match: "SELECT DISTINCT storage_key", columns: []string{"storage_key"},
			values: [][]driver.Value{{"k-1"}}, errAfterRows: 1},
	}
	for name, script := range scripts {
		t.Run(name, func(t *testing.T) {
			store := newKitCodexStore(t, 1)
			seedKitCodexSession(t, store, 0, "s-1", "2026-01-01T00:00:00.000Z")
			seedKitCodexRow(t, store, 0, "codex_context_responses", "r-1", "s-1", "k-1", "2026-01-01T00:00:00.000Z")
			if store.shards == nil {
				store.shards = map[int]*sql.DB{}
			}
			for _, cached := range store.shards {
				_ = cached.Close()
			}
			store.shards = map[int]*sql.DB{
				0: w13eOpenDecoratedRawSQLite(t, store.shardPath(0), w13eSQLiteOptions{rowsScripts: []w13eRowsScript{script}}),
			}
			if name == "remaining empty" {
				// 空键行跳过分支：不报错即可。
				if _, err := store.CleanupExpiredStates(ctx, "2026-09-10T00:00:00.000Z", 10); err != nil {
					t.Fatalf("空键行应跳过: %v", err)
				}
				return
			}
			if _, err := store.CleanupExpiredStates(ctx, "2026-09-10T00:00:00.000Z", 10); err == nil {
				t.Fatalf("脚本注入应产生错误")
			}
		})
	}
	// referenced > limit 钳制：队列 3 个键、limit 2。
	{
		store := newKitCodexStore(t, 1)
		for _, key := range []string{"k-1", "k-2", "k-3"} {
			mustExecKit(t, mustShard(t, store, 0), `INSERT INTO codex_context_storage_cleanup_queue
        (storage_key, enqueued_at, updated_at, next_attempt_at) VALUES (?, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`, key)
		}
		result, err := store.CleanupExpiredStates(ctx, "2026-09-10T00:00:00.000Z", 2)
		if err != nil || !result.HasMore || len(result.StorageKeys) != 2 {
			t.Fatalf("pending 应被钳制: %+v %v", result, err)
		}
	}
	// referenced 键被丢弃（仍被 responses 引用）。
	{
		store := newKitCodexStore(t, 1)
		seedKitCodexRow(t, store, 0, "codex_context_responses", "r-1", "s-live", "k-ref", "2027-01-01T00:00:00.000Z")
		mustExecKit(t, mustShard(t, store, 0), `INSERT INTO codex_context_storage_cleanup_queue
      (storage_key, enqueued_at, updated_at, next_attempt_at) VALUES ('k-ref', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`)
		result, err := store.CleanupExpiredStates(ctx, "2026-09-10T00:00:00.000Z", 10)
		if err != nil || len(result.StorageKeys) != 0 {
			t.Fatalf("被引用键应被丢弃: %+v %v", result, err)
		}
	}
}

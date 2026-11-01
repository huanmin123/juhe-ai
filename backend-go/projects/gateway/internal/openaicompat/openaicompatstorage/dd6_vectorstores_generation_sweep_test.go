package openaicompatstorage

// DD6-S7 / DD6-S8 钉住测试：
//
// DD6-S7 删→重加×旧索引任务在途覆盖：任务闭包携带启动时绑定行的
// updated_at 代次快照，回调 upsert 以 AND updated_at = ? 守卫——绑定被
// 删→重加后旧回调整事务放弃（不覆盖新 attributes/chunking、不重插旧
// chunks、不刷聚合字节），正常单次索引不受守卫影响。
//
// DD6-S8 索引任务×进程重启：启动清扫把超过阈值仍 in_progress 且未删的
// 绑定标 failed；新鲜行与已删行（DD2-C7 形态）不动。

import (
	"database/sql"
	"errors"
	"testing"
	"time"
)

// staleBeforeCutoff 早于 fixedClock base（2026-09-04T08:00Z）减去清扫阈值
// （10 分钟）的固定超龄时间戳。
const staleBeforeCutoff = "2026-09-03T00:00:00.000Z"

func TestDD6S7StaleIndexCallbackDoesNotOverwriteReaddedBinding(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	if _, err := store.CreateVectorStore(ctx, "vs-dd6s7", VectorStoreCreateInput{
		SystemAccountID: testScopeA, APIKeyID: testKeyA,
	}); err != nil {
		t.Fatal(err)
	}
	createTestFile(t, store, t.TempDir(), "file-dd6s7", "assistants", []byte("dd6s7 body"), "", nil)

	// 第一次添加：旧索引任务的启动代次（闭包快照 = 此刻 updated_at）。
	first, err := store.CreateVectorStoreFile(ctx, VectorStoreFileCreateInput{
		VectorStoreID: "vs-dd6s7", FileID: "file-dd6s7",
		SystemAccountID:  testScopeA,
		APIKeyID:         testKeyA,
		Attributes:       map[string]any{"lang": "old"},
		ChunkingStrategy: map[string]any{"type": "old_static"},
		Status:           VectorStoreFileStatusInProgress,
	})
	if err != nil || first == nil {
		t.Fatalf("首次添加失败：%v %v", first, err)
	}

	// 索引在途：删除（软删）→ 重加（新代次、新 attributes/chunking）。
	if _, err := store.DeleteVectorStoreFile(ctx, "vs-dd6s7", "file-dd6s7", testScopeA, testKeyA); err != nil {
		t.Fatal(err)
	}
	readded, err := store.CreateVectorStoreFile(ctx, VectorStoreFileCreateInput{
		VectorStoreID: "vs-dd6s7", FileID: "file-dd6s7",
		SystemAccountID:  testScopeA,
		APIKeyID:         testKeyA,
		Attributes:       map[string]any{"lang": "new"},
		ChunkingStrategy: map[string]any{"type": "new_static"},
		Status:           VectorStoreFileStatusInProgress,
	})
	if err != nil || readded == nil {
		t.Fatalf("重加失败：%v %v", readded, err)
	}
	if readded.UpdatedAt == first.UpdatedAt {
		t.Fatalf("重加必须推进 updated_at（代次守卫前提）：first=%s readded=%s", first.UpdatedAt, readded.UpdatedAt)
	}

	// 旧任务回调：携带启动时代次快照与旧 attributes/chunking/旧 chunks →
	// 必须整事务放弃并返回 ErrVectorStoreFileBindingSuperseded。
	staleOutcome, err := store.CreateVectorStoreFile(ctx, VectorStoreFileCreateInput{
		VectorStoreID: "vs-dd6s7", FileID: "file-dd6s7",
		SystemAccountID:   testScopeA,
		APIKeyID:          testKeyA,
		Attributes:        map[string]any{"lang": "old"},
		ChunkingStrategy:  map[string]any{"type": "old_static"},
		Status:            VectorStoreFileStatusCompleted,
		Chunks:            []ChunkInput{{ContentText: "stale-chunk", KeywordIndexText: "stale-chunk"}},
		IndexCallback:     true,
		ExpectedUpdatedAt: first.UpdatedAt,
	})
	if staleOutcome != nil || !errors.Is(err, ErrVectorStoreFileBindingSuperseded) {
		t.Fatalf("过期回调必须返回 superseded：%v %v", staleOutcome, err)
	}

	// 新绑定保持重加时的全部新值：attributes/chunking 未被旧值覆盖、状态仍
	// in_progress、旧 chunks 未落、聚合字节未刷新。
	var attributesJSON, chunkingJSON, status string
	var usageBytes int64
	var deletedAt sql.NullString
	if err := store.db.QueryRow(`SELECT attributes_json, chunking_strategy_json, status, usage_bytes, deleted_at
		FROM openai_compatible_vector_store_files
		WHERE vector_store_id = $1 AND file_id = $2`, "vs-dd6s7", "file-dd6s7").Scan(
		&attributesJSON, &chunkingJSON, &status, &usageBytes, &deletedAt); err != nil {
		t.Fatal(err)
	}
	if attributesJSON != `{"lang":"new"}` {
		t.Fatalf("attributes 必须保持重加新值：%s", attributesJSON)
	}
	if chunkingJSON != `{"type":"new_static"}` {
		t.Fatalf("chunking_strategy 必须保持重加新值：%s", chunkingJSON)
	}
	if status != VectorStoreFileStatusInProgress {
		t.Fatalf("绑定必须保持 in_progress：%s", status)
	}
	if usageBytes != 0 {
		t.Fatalf("usage_bytes 不得被旧回调刷新：%d", usageBytes)
	}
	if deletedAt.Valid {
		t.Fatalf("重加后 deleted_at 必须为 NULL：%q", deletedAt.String)
	}
	if chunks := store.countChunks(t, "vs-dd6s7", "file-dd6s7"); chunks != 0 {
		t.Fatalf("旧回调不得重插 chunks：%d", chunks)
	}
	var storeBytes int64
	if err := store.db.QueryRow(`SELECT bytes FROM openai_compatible_vector_stores WHERE id = $1`, "vs-dd6s7").Scan(&storeBytes); err != nil {
		t.Fatal(err)
	}
	if storeBytes != 0 {
		t.Fatalf("聚合字节不得被旧回调刷新：%d", storeBytes)
	}

	// 新任务回调（携带重加代次）照常完成：状态、chunks、字节全落，且
	// attributes 保持重加时闭包携带的新值。
	freshOutcome, err := store.CreateVectorStoreFile(ctx, VectorStoreFileCreateInput{
		VectorStoreID: "vs-dd6s7", FileID: "file-dd6s7",
		SystemAccountID:   testScopeA,
		APIKeyID:          testKeyA,
		Attributes:        map[string]any{"lang": "new"},
		ChunkingStrategy:  map[string]any{"type": "new_static"},
		Status:            VectorStoreFileStatusCompleted,
		Chunks:            []ChunkInput{{ContentText: "fresh-chunk", KeywordIndexText: "fresh-chunk"}},
		IndexCallback:     true,
		ExpectedUpdatedAt: readded.UpdatedAt,
	})
	if err != nil || freshOutcome == nil || freshOutcome.Status != VectorStoreFileStatusCompleted {
		t.Fatalf("新代次回调必须完成：%v %v", freshOutcome, err)
	}
	if chunks := store.countChunks(t, "vs-dd6s7", "file-dd6s7"); chunks != 1 {
		t.Fatalf("新代次回调应落 1 条 chunk：%d", chunks)
	}
	if err := store.db.QueryRow(`SELECT bytes FROM openai_compatible_vector_stores WHERE id = $1`, "vs-dd6s7").Scan(&storeBytes); err != nil {
		t.Fatal(err)
	}
	if storeBytes != int64(len("fresh-chunk")) {
		t.Fatalf("聚合字节应含新 chunk：%d", storeBytes)
	}
	if err := store.db.QueryRow(`SELECT attributes_json FROM openai_compatible_vector_store_files
		WHERE vector_store_id = $1 AND file_id = $2`, "vs-dd6s7", "file-dd6s7").Scan(&attributesJSON); err != nil {
		t.Fatal(err)
	}
	if attributesJSON != `{"lang":"new"}` {
		t.Fatalf("新回调后 attributes 必须仍是重加闭包携带的新值：%s", attributesJSON)
	}
}

func TestDD6S7IndexCallbackWithoutRebindCompletes(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	if _, err := store.CreateVectorStore(ctx, "vs-dd6s7b", VectorStoreCreateInput{
		SystemAccountID: testScopeA, APIKeyID: testKeyA,
	}); err != nil {
		t.Fatal(err)
	}
	createTestFile(t, store, t.TempDir(), "file-dd6s7b", "assistants", []byte("dd6s7b body"), "", nil)

	created, err := store.CreateVectorStoreFile(ctx, VectorStoreFileCreateInput{
		VectorStoreID: "vs-dd6s7b", FileID: "file-dd6s7b",
		SystemAccountID: testScopeA, APIKeyID: testKeyA,
		Status: VectorStoreFileStatusInProgress,
	})
	if err != nil || created == nil {
		t.Fatalf("添加失败：%v %v", created, err)
	}

	// 无删除/重加：带同代次快照的回调必须成功（守卫不误伤正常单次索引）。
	outcome, err := store.CreateVectorStoreFile(ctx, VectorStoreFileCreateInput{
		VectorStoreID: "vs-dd6s7b", FileID: "file-dd6s7b",
		SystemAccountID:   testScopeA,
		APIKeyID:          testKeyA,
		Status:            VectorStoreFileStatusCompleted,
		Chunks:            []ChunkInput{{ContentText: "normal-chunk", KeywordIndexText: "normal-chunk"}},
		IndexCallback:     true,
		ExpectedUpdatedAt: created.UpdatedAt,
	})
	if err != nil || outcome == nil || outcome.Status != VectorStoreFileStatusCompleted {
		t.Fatalf("正常回调必须完成：%v %v", outcome, err)
	}
	if chunks := store.countChunks(t, "vs-dd6s7b", "file-dd6s7b"); chunks != 1 {
		t.Fatalf("正常回调应落 1 条 chunk：%d", chunks)
	}
	var storeBytes int64
	if err := store.db.QueryRow(`SELECT bytes FROM openai_compatible_vector_stores WHERE id = $1`, "vs-dd6s7b").Scan(&storeBytes); err != nil {
		t.Fatal(err)
	}
	if storeBytes != int64(len("normal-chunk")) {
		t.Fatalf("聚合字节应刷新：%d", storeBytes)
	}

	// 快照过期后再回调（行 updated_at 已被上一回调推进）→ 拒绝。
	dupOutcome, err := store.CreateVectorStoreFile(ctx, VectorStoreFileCreateInput{
		VectorStoreID: "vs-dd6s7b", FileID: "file-dd6s7b",
		SystemAccountID:   testScopeA,
		APIKeyID:          testKeyA,
		Status:            VectorStoreFileStatusCompleted,
		Chunks:            []ChunkInput{{ContentText: "replay-chunk", KeywordIndexText: "replay-chunk"}},
		IndexCallback:     true,
		ExpectedUpdatedAt: created.UpdatedAt,
	})
	if dupOutcome != nil || !errors.Is(err, ErrVectorStoreFileBindingSuperseded) {
		t.Fatalf("重放过期回调必须拒绝：%v %v", dupOutcome, err)
	}
	if chunks := store.countChunks(t, "vs-dd6s7b", "file-dd6s7b"); chunks != 1 {
		t.Fatalf("重放不得追加 chunks：%d", chunks)
	}
}

func TestDD6S8StartupSweepMarksStaleInProgressBindingsFailed(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	if _, err := store.CreateVectorStore(ctx, "vs-dd6s8", VectorStoreCreateInput{
		SystemAccountID: testScopeA, APIKeyID: testKeyA,
	}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"file-dd6s8-stale", "file-dd6s8-fresh", "file-dd6s8-deleted"} {
		createTestFile(t, store, t.TempDir(), id, "assistants", []byte("dd6s8 "+id), "", nil)
		if _, err := store.CreateVectorStoreFile(ctx, VectorStoreFileCreateInput{
			VectorStoreID: "vs-dd6s8", FileID: id,
			SystemAccountID: testScopeA, APIKeyID: testKeyA,
			Status: VectorStoreFileStatusInProgress,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// stale：把 updated_at 推到阈值之前（模拟重启前遗留）。
	execSQL(t, store, `UPDATE openai_compatible_vector_store_files SET updated_at = $1
		WHERE file_id = $2`, staleBeforeCutoff, "file-dd6s8-stale")
	// deleted：先软删（DD2-C7 删除形态：status 停留 in_progress、
	// deleted_at 落值），再把 updated_at 推老——验证 deleted_at IS NULL 条件
	// 独立于时间，已删行即便超龄也不被清扫触碰。
	if _, err := store.DeleteVectorStoreFile(ctx, "vs-dd6s8", "file-dd6s8-deleted", testScopeA, testKeyA); err != nil {
		t.Fatal(err)
	}
	execSQL(t, store, `UPDATE openai_compatible_vector_store_files SET updated_at = $1
		WHERE file_id = $2`, staleBeforeCutoff, "file-dd6s8-deleted")

	changed, err := store.sweepStaleVectorStoreFileIndexing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if changed != 1 {
		t.Fatalf("清扫必须恰好收尾 1 行：%d", changed)
	}
	assertBindingSweepState := func(fileID, wantStatus string, wantDeleted bool) {
		t.Helper()
		var status string
		var deletedAt sql.NullString
		if err := store.db.QueryRow(`SELECT status, deleted_at FROM openai_compatible_vector_store_files
			WHERE vector_store_id = $1 AND file_id = $2`, "vs-dd6s8", fileID).Scan(&status, &deletedAt); err != nil {
			t.Fatal(err)
		}
		if status != wantStatus {
			t.Fatalf("%s status = %s, want %s", fileID, status, wantStatus)
		}
		if deletedAt.Valid != wantDeleted {
			t.Fatalf("%s deleted_at valid = %v, want %v", fileID, deletedAt.Valid, wantDeleted)
		}
	}
	assertBindingSweepState("file-dd6s8-stale", VectorStoreFileStatusFailed, false)
	assertBindingSweepState("file-dd6s8-fresh", VectorStoreFileStatusInProgress, false)
	assertBindingSweepState("file-dd6s8-deleted", VectorStoreFileStatusInProgress, true)

	// 幂等：重复执行空转。
	changed, err = store.sweepStaleVectorStoreFileIndexing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if changed != 0 {
		t.Fatalf("重复清扫必须空转：%d", changed)
	}
}

func TestDD6S8NewStoreSweepsAtConstruction(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	if _, err := store.CreateVectorStore(ctx, "vs-dd6s8b", VectorStoreCreateInput{
		SystemAccountID: testScopeA, APIKeyID: testKeyA,
	}); err != nil {
		t.Fatal(err)
	}
	createTestFile(t, store, t.TempDir(), "file-dd6s8b", "assistants", []byte("dd6s8b body"), "", nil)
	if _, err := store.CreateVectorStoreFile(ctx, VectorStoreFileCreateInput{
		VectorStoreID: "vs-dd6s8b", FileID: "file-dd6s8b",
		SystemAccountID: testScopeA, APIKeyID: testKeyA,
		Status: VectorStoreFileStatusInProgress,
	}); err != nil {
		t.Fatal(err)
	}
	execSQL(t, store, `UPDATE openai_compatible_vector_store_files SET updated_at = $1
		WHERE file_id = $2`, staleBeforeCutoff, "file-dd6s8b")

	// 挂载点端到端：对同一库再构造一个 Store，NewStore 触发的启动清扫必须
	// 把超龄行收尾为 failed（后台 goroutine，轮询等待终态）。
	sweeper, err := NewStore(store.db, WithNow(fixedClock(t)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var status string
		scanErr := sweeper.db.QueryRow(`SELECT status FROM openai_compatible_vector_store_files
			WHERE vector_store_id = $1 AND file_id = $2`, "vs-dd6s8b", "file-dd6s8b").Scan(&status)
		if scanErr != nil {
			t.Fatal(scanErr)
		}
		if status == VectorStoreFileStatusFailed {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("启动清扫未收尾超龄绑定：status=%s", status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

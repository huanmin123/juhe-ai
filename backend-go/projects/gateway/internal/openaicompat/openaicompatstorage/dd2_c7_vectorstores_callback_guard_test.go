package openaicompatstorage

// DD2-C7 钉住测试：索引完成回调（IndexCallback 写面）对用户在途删除的绑定
// 不得复活——已删行不写状态、不重插 chunks、不刷新聚合字节；守卫不得误伤
// 活绑定上的回调，也不得改变用户面「删除后重新添加」的复活契约。

import (
	"database/sql"
	"testing"
)

func TestDD2C7IndexCallbackDoesNotResurrectDeletedBinding(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	if _, err := store.CreateVectorStore(ctx, "vs-dd2c7", VectorStoreCreateInput{
		SystemAccountID: testScopeA, APIKeyID: testKeyA,
	}); err != nil {
		t.Fatal(err)
	}
	fileID := "file-dd2c7"
	createTestFile(t, store, t.TempDir(), fileID, "assistants", []byte("dd2c7 index body"), "", nil)

	// 用户添加：in_progress 绑定落库。
	if _, err := store.CreateVectorStoreFile(ctx, VectorStoreFileCreateInput{
		VectorStoreID: "vs-dd2c7", FileID: fileID,
		SystemAccountID: testScopeA, APIKeyID: testKeyA,
		Status: VectorStoreFileStatusInProgress,
	}); err != nil {
		t.Fatal(err)
	}
	// 索引在途：用户删除绑定（chunks 清空、deleted_at 落值）。
	if _, err := store.DeleteVectorStoreFile(ctx, "vs-dd2c7", fileID, testScopeA, testKeyA); err != nil {
		t.Fatal(err)
	}
	// 索引完成回调：必须 (nil, nil)，整事务回滚。
	outcome, err := store.CreateVectorStoreFile(ctx, VectorStoreFileCreateInput{
		VectorStoreID: "vs-dd2c7", FileID: fileID,
		SystemAccountID: testScopeA, APIKeyID: testKeyA,
		Status:        VectorStoreFileStatusCompleted,
		Chunks:        []ChunkInput{{ContentText: "chunk-a"}, {ContentText: "chunk-b"}},
		IndexCallback: true,
	})
	if err != nil || outcome != nil {
		t.Fatalf("已删绑定的回调必须 (nil, nil)：%v %v", outcome, err)
	}
	var status, deletedAt string
	if err := store.db.QueryRow(`SELECT status, deleted_at FROM openai_compatible_vector_store_files
		WHERE vector_store_id = $1 AND file_id = $2`, "vs-dd2c7", fileID).Scan(&status, &deletedAt); err != nil {
		t.Fatal(err)
	}
	if deletedAt == "" || status != VectorStoreFileStatusInProgress {
		t.Fatalf("绑定必须保持删除形态（status=%s deleted_at=%q）", status, deletedAt)
	}
	if chunks := store.countChunks(t, "vs-dd2c7", fileID); chunks != 0 {
		t.Fatalf("已删绑定不得重插 chunks：%d", chunks)
	}
	var bytes int64
	if err := store.db.QueryRow(`SELECT bytes FROM openai_compatible_vector_stores WHERE id = $1`, "vs-dd2c7").Scan(&bytes); err != nil {
		t.Fatal(err)
	}
	if bytes != 0 {
		t.Fatalf("已删绑定不得刷新聚合字节：%d", bytes)
	}

	// 守卫不改变用户面契约：无 IndexCallback 标记的添加复活已删绑定。
	revived, err := store.CreateVectorStoreFile(ctx, VectorStoreFileCreateInput{
		VectorStoreID: "vs-dd2c7", FileID: fileID,
		SystemAccountID: testScopeA, APIKeyID: testKeyA,
		Status: VectorStoreFileStatusInProgress,
	})
	if err != nil || revived == nil {
		t.Fatalf("用户重加必须复活绑定：%v %v", revived, err)
	}
	var revivedDeletedAt sql.NullString
	if err := store.db.QueryRow(`SELECT deleted_at FROM openai_compatible_vector_store_files
		WHERE vector_store_id = $1 AND file_id = $2`, "vs-dd2c7", fileID).Scan(&revivedDeletedAt); err != nil {
		t.Fatal(err)
	}
	if revivedDeletedAt.Valid {
		t.Fatalf("复活后 deleted_at 必须为 NULL：%q", revivedDeletedAt.String)
	}

	// 守卫不误伤活绑定：回调照常完成（状态、chunks、字节全落）。
	live, err := store.CreateVectorStoreFile(ctx, VectorStoreFileCreateInput{
		VectorStoreID: "vs-dd2c7", FileID: fileID,
		SystemAccountID: testScopeA, APIKeyID: testKeyA,
		Status:        VectorStoreFileStatusCompleted,
		Chunks:        []ChunkInput{{ContentText: "live-chunk"}},
		IndexCallback: true,
	})
	if err != nil || live == nil || live.Status != VectorStoreFileStatusCompleted {
		t.Fatalf("活绑定回调必须完成：%v %v", live, err)
	}
	if chunks := store.countChunks(t, "vs-dd2c7", fileID); chunks != 1 {
		t.Fatalf("活绑定回调应重插 1 条 chunk：%d", chunks)
	}
	if err := store.db.QueryRow(`SELECT bytes FROM openai_compatible_vector_stores WHERE id = $1`, "vs-dd2c7").Scan(&bytes); err != nil {
		t.Fatal(err)
	}
	if bytes != int64(len("live-chunk")) {
		t.Fatalf("聚合字节应含活绑定 chunk：%d", bytes)
	}
}

func (s *Store) countChunks(t *testing.T, vectorStoreID, fileID string) int {
	t.Helper()
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM openai_compatible_vector_store_chunks
		WHERE vector_store_id = $1 AND file_id = $2`, vectorStoreID, fileID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

package openaicompat

// vectorstoresstore.go 可达边角分支的补充覆盖：游标空桶、deleted status
// 投影、metadata 序列化失败与 NotReadable 不变错误文本。

import (
	"context"
	"testing"
)

func TestCovVectorStoreStoreEdgeBranches(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	t.Run("游标指向不存在的 store 返回空页", func(t *testing.T) {
		result, err := store.ListVectorStores(ctx, VectorStoreListOptions{
			SystemAccountID: testScopeA, APIKeyID: testKeyA,
			After: "vs_missing", Before: "vs_missing2",
		})
		if err != nil {
			t.Fatalf("列表失败：%v", err)
		}
		if len(result.Items) != 0 || result.HasMore {
			t.Errorf("游标缺失应得空页：%+v", result)
		}
	})
	t.Run("CreateVectorStore metadata 序列化失败", func(t *testing.T) {
		_, err := store.CreateVectorStore(ctx, "vs_bad_meta", VectorStoreCreateInput{
			SystemAccountID: testScopeA, APIKeyID: testKeyA,
			Metadata: map[string]any{"bad": make(chan int)},
		})
		if err == nil {
			t.Fatal("不可序列化 metadata 应报错")
		}
	})
	t.Run("deleted status 投影", func(t *testing.T) {
		if _, err := store.CreateVectorStore(ctx, "vs_del", VectorStoreCreateInput{
			SystemAccountID: testScopeA, APIKeyID: testKeyA, Name: strPtr("待删"),
		}); err != nil {
			t.Fatalf("创建失败：%v", err)
		}
		// status='deleted' 但 deleted_at 为 NULL：仍可读，投影为 deleted。
		execSQL(t, store, `UPDATE openai_compatible_vector_stores SET status = 'deleted' WHERE id = 'vs_del'`)
		record, err := store.FindVectorStore(ctx, "vs_del", testScopeA, testKeyA)
		if err != nil || record == nil {
			t.Fatalf("查询失败：%v（%v）", record, err)
		}
		if record.Status != VectorStoreStatusDeleted {
			t.Errorf("status = %q，期望 deleted", record.Status)
		}
	})
	t.Run("NotReadable 错误文本", func(t *testing.T) {
		err := errVectorStoreNotReadable("vs_x")
		if err.Error() != "OpenAI compatible vector store vs_x was not readable after insert" {
			t.Errorf("错误文本 = %q", err.Error())
		}
	})
	t.Run("coerceString 字节与 nil", func(t *testing.T) {
		if got := coerceString([]byte("bytes")); got != "bytes" {
			t.Errorf("[]byte = %q", got)
		}
		if got := coerceString(nil); got != "" {
			t.Errorf("nil = %q", got)
		}
		if got := coerceString(42); got != "" {
			t.Errorf("数字 = %q", got)
		}
	})
	t.Run("nullInt64Pointer", func(t *testing.T) {
		if got := nullInt64Pointer(nil); got != nil {
			t.Errorf("nil = %v", got)
		}
		three := 3
		if got := nullInt64Pointer(&three); got != 3 {
			t.Errorf("值 = %v", got)
		}
	})
}

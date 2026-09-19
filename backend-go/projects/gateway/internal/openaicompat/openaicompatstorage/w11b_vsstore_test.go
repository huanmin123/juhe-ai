package openaicompatstorage

// w11b 波次：vector store 存储方法级分支（创建、游标列表、查找、删除、
// 计数）与 code interpreter 工件收集。

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestW11BVectorStoreStoreMethodBranches(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	// 创建：全字段。
	name := "w11b-store"
	description := "desc"
	days := 7
	anchor := "last_active_at"
	created, err := store.CreateVectorStore(ctx, "vs-w11b", VectorStoreCreateInput{
		SystemAccountID: testScopeA, APIKeyID: testKeyA,
		Name: &name, Description: &description,
		Metadata:           map[string]any{"k": "v"},
		ExpiresAfterAnchor: &anchor, ExpiresAfterDays: &days,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != "vs-w11b" || created.Name == nil || *created.Name != name {
		t.Fatalf("created = %+v", created)
	}
	// 缺省字段创建。
	created2, err := store.CreateVectorStore(ctx, "vs-w11b-2", VectorStoreCreateInput{
		SystemAccountID: testScopeA, APIKeyID: testKeyA,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = created2

	// 列表：after 命中与未命中。
	list, err := store.ListVectorStores(ctx, VectorStoreListOptions{SystemAccountID: testScopeA, APIKeyID: testKeyA})
	if err != nil || len(list.Items) != 2 {
		t.Fatalf("list = %+v err = %v", list, err)
	}
	listAfter, err := store.ListVectorStores(ctx, VectorStoreListOptions{SystemAccountID: testScopeA, APIKeyID: testKeyA, After: list.Items[0].ID})
	if err != nil || len(listAfter.Items) != 1 {
		t.Fatalf("after = %+v err = %v", listAfter, err)
	}
	listMissing, err := store.ListVectorStores(ctx, VectorStoreListOptions{SystemAccountID: testScopeA, APIKeyID: testKeyA, After: "missing"})
	if err != nil || len(listMissing.Items) != 0 {
		t.Fatalf("missing after = %+v err = %v", listMissing, err)
	}
	limit := 1
	limited, err := store.ListVectorStores(ctx, VectorStoreListOptions{SystemAccountID: testScopeA, APIKeyID: testKeyA, Limit: &limit, Order: "desc"})
	if err != nil || !limited.HasMore {
		t.Fatalf("limited = %+v err = %v", limited, err)
	}

	// 查找。
	found, err := store.FindVectorStore(ctx, "vs-w11b", testScopeA, testKeyA)
	if err != nil || found == nil || found.ID != "vs-w11b" {
		t.Fatalf("found = %+v err = %v", found, err)
	}
	if missing, err := store.FindVectorStore(ctx, "missing", testScopeA, testKeyA); err != nil || missing != nil {
		t.Fatalf("missing = %+v err = %v", missing, err)
	}

	// 文件：创建 + 列表 + 查找 + 删除。
	root := t.TempDir()
	record := createTestFile(t, store, root, "w11b-vf1", "assistants", []byte("alpha beta gamma"), "text/plain", nil)
	_ = record
	chunks := []ChunkInput{{ContentText: "alpha beta gamma", TokenEstimate: 4, KeywordIndexText: "alpha beta gamma"}}
	if _, err := store.CreateVectorStoreFile(ctx, VectorStoreFileCreateInput{
		VectorStoreID: "vs-w11b", FileID: "w11b-vf1", SystemAccountID: testScopeA, APIKeyID: testKeyA,
		Attributes: map[string]any{"lang": "en"}, Status: "completed", Chunks: chunks,
	}); err != nil {
		t.Fatal(err)
	}
	files, err := store.ListVectorStoreFiles(ctx, VectorStoreFileListOptions{VectorStoreID: "vs-w11b", SystemAccountID: testScopeA, APIKeyID: testKeyA})
	if err != nil || len(files.Items) != 1 {
		t.Fatalf("files = %+v err = %v", files, err)
	}
	filesAfter, err := store.ListVectorStoreFiles(ctx, VectorStoreFileListOptions{VectorStoreID: "vs-w11b", SystemAccountID: testScopeA, APIKeyID: testKeyA, After: "w11b-vf1"})
	if err != nil || len(filesAfter.Items) != 0 {
		t.Fatalf("files after = %+v err = %v", filesAfter, err)
	}
	filesMissingAfter, err := store.ListVectorStoreFiles(ctx, VectorStoreFileListOptions{VectorStoreID: "vs-w11b", SystemAccountID: testScopeA, APIKeyID: testKeyA, After: "missing"})
	if err != nil || len(filesMissingAfter.Items) != 0 {
		t.Fatalf("files missing after = %+v err = %v", filesMissingAfter, err)
	}
	fileRecord, err := store.FindVectorStoreFile(ctx, "vs-w11b", "w11b-vf1", testScopeA, testKeyA)
	if err != nil || fileRecord == nil || fileRecord.File == nil {
		t.Fatalf("file = %+v err = %v", fileRecord, err)
	}
	// 搜索带属性过滤与阈值。
	results, err := store.SearchVectorStore(ctx, SearchOptions{
		VectorStoreID: "vs-w11b", SystemAccountID: testScopeA, APIKeyID: testKeyA,
		Query: "alpha", Filters: map[string]any{"type": "eq", "key": "lang", "value": "en"},
	})
	if err != nil || len(results) != 1 {
		t.Fatalf("search = %+v err = %v", results, err)
	}
	highThreshold := 99.0
	results, err = store.SearchVectorStore(ctx, SearchOptions{
		VectorStoreID: "vs-w11b", SystemAccountID: testScopeA, APIKeyID: testKeyA,
		Query: "alpha", ScoreThreshold: &highThreshold,
	})
	if err != nil || len(results) != 0 {
		t.Fatalf("threshold search = %+v err = %v", results, err)
	}
	// 删除文件与存储。
	deleted, err := store.DeleteVectorStoreFile(ctx, "vs-w11b", "w11b-vf1", testScopeA, testKeyA)
	if err != nil || deleted == nil {
		t.Fatalf("delete file = %+v err = %v", deleted, err)
	}
	if again, err := store.DeleteVectorStoreFile(ctx, "vs-w11b", "w11b-vf1", testScopeA, testKeyA); err != nil || again != nil {
		t.Fatalf("redelete file = %+v err = %v", again, err)
	}
	deletedStore, err := store.DeleteVectorStore(ctx, "vs-w11b", testScopeA, testKeyA)
	if err != nil || deletedStore == nil {
		t.Fatalf("delete store = %+v err = %v", deletedStore, err)
	}
	if again, err := store.DeleteVectorStore(ctx, "vs-w11b", testScopeA, testKeyA); err != nil || again != nil {
		t.Fatalf("redelete store = %+v err = %v", again, err)
	}
}

func TestW11BCodeInterpreterCollectArtifacts(t *testing.T) {
	executor := &CodeInterpreterExecutor{
		config: Config{}.WithDefaults().CodeInterpreter,
	}
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "chart.png"), []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(workDir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "sub", "data.csv"), []byte("a,b"), 0o644); err != nil {
		t.Fatal(err)
	}
	collection, err := executor.collectArtifacts(workDir, "ctr-w11b")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(collection.artifacts) != 2 {
		t.Fatalf("artifacts = %+v", collection.artifacts)
	}
	if collection.metadata["artifacts_scan_failed"] == true {
		t.Fatalf("metadata = %v", collection.metadata)
	}
	// 目录不存在：扫描失败被吞并记录。
	failed, err := executor.collectArtifacts(filepath.Join(workDir, "missing"), "ctr")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if failed.metadata["artifacts_scan_failed"] != true {
		t.Fatalf("failed metadata = %v", failed.metadata)
	}
}

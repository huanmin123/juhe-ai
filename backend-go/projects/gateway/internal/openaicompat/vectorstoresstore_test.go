package openaicompat

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// Vector store store tests mirror
// storage/openai-compatible-vector-stores.repository.ts.

func seedVectorStoreFixture(t *testing.T, store *Store) (string, string) {
	t.Helper()
	ctx := context.Background()
	if _, err := store.CreateVectorStore(ctx, "vs-1", VectorStoreCreateInput{
		SystemAccountID: testScopeA, APIKeyID: testKeyA, Name: strPtr("docs"),
	}); err != nil {
		t.Fatal(err)
	}
	createTestFile(t, store, t.TempDir(), "file-src", "assistants", []byte("alpha beta gamma"), "text/plain", nil)
	return "vs-1", "file-src"
}

func TestVectorStoreLifecycle(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	created, err := store.CreateVectorStore(ctx, "vs-1", VectorStoreCreateInput{
		SystemAccountID: testScopeA, APIKeyID: testKeyA,
		Name: strPtr("docs"), Metadata: map[string]any{"team": "core"},
		ExpiresAfterAnchor: strPtr("last_active_at"), ExpiresAfterDays: intPtr(7),
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != "active" || created.Bytes != 0 || created.FileCounts.Total != 0 {
		t.Fatalf("created = %+v", created)
	}
	if created.ExpiresAt == nil {
		// expiresAt was not passed; stays unset.
	}

	found, err := store.FindVectorStore(ctx, "vs-1", testScopeA, testKeyA)
	if err != nil || found == nil {
		t.Fatalf("find = %v %v", found, err)
	}
	for _, scope := range [][2]string{{testScopeB, testKeyA}, {testScopeA, testKeyB}} {
		if record, err := store.FindVectorStore(ctx, "vs-1", scope[0], scope[1]); err != nil || record != nil {
			t.Fatalf("越权 lookup should miss: %+v %v", record, err)
		}
	}

	// Upsert a completed file with chunks (usage = utf8 bytes of the texts).
	createTestFile(t, store, t.TempDir(), "file-src", "assistants", []byte("alpha beta gamma"), "text/plain", nil)
	fileRecord, err := store.CreateVectorStoreFile(ctx, VectorStoreFileCreateInput{
		VectorStoreID: "vs-1", FileID: "file-src", SystemAccountID: testScopeA, APIKeyID: testKeyA,
		Status: VectorStoreFileStatusCompleted,
		Chunks: []ChunkInput{
			{ContentText: "alpha", ContentPreview: "alpha", TokenEstimate: 1, KeywordIndexText: "alpha"},
			{ContentText: "beta", ContentPreview: "beta", TokenEstimate: 1, KeywordIndexText: "beta"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if fileRecord == nil || fileRecord.Status != "completed" || fileRecord.UsageBytes != 9 {
		t.Fatalf("file record = %+v", fileRecord)
	}
	if fileRecord.File == nil || fileRecord.File.ID != "file-src" {
		t.Fatalf("embedded file missing: %+v", fileRecord)
	}
	refreshed, err := store.FindVectorStore(ctx, "vs-1", testScopeA, testKeyA)
	if err != nil || refreshed.Bytes != 9 || refreshed.FileCounts.Completed != 1 {
		t.Fatalf("bytes refresh failed: %+v", refreshed)
	}

	// Search hits both chunks.
	results, err := store.SearchVectorStore(ctx, SearchOptions{
		VectorStoreID: "vs-1", SystemAccountID: testScopeA, APIKeyID: testKeyA, Query: "alpha beta",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("search results = %+v", results)
	}

	// 删除文件清空 chunks 并复位 bytes。
	deleted, err := store.DeleteVectorStoreFile(ctx, "vs-1", "file-src", testScopeA, testKeyA)
	if err != nil || deleted == nil {
		t.Fatalf("delete file = %v %v", deleted, err)
	}
	chunks, err := store.ListVectorStoreFileChunks(ctx, "vs-1", "file-src", testScopeA, testKeyA, nil)
	if err != nil || len(chunks) != 0 {
		t.Fatalf("chunks after delete = %v %v", chunks, err)
	}
	refreshed, err = store.FindVectorStore(ctx, "vs-1", testScopeA, testKeyA)
	if err != nil || refreshed.Bytes != 0 || refreshed.FileCounts.Completed != 0 {
		t.Fatalf("bytes after delete = %+v", refreshed)
	}

	// 删除 store 级联软删文件与 chunks。
	deletedStore, err := store.DeleteVectorStore(ctx, "vs-1", testScopeA, testKeyA)
	if err != nil || deletedStore == nil || deletedStore.Status != "deleted" {
		t.Fatalf("delete store = %v %v", deletedStore, err)
	}
	if record, err := store.FindVectorStore(ctx, "vs-1", testScopeA, testKeyA); err != nil || record != nil {
		t.Fatalf("deleted store visible: %+v %v", record, err)
	}
}

func TestVectorStoreSearchScoringAndFilters(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	_, _ = seedVectorStoreFixture(t, store)
	if _, err := store.CreateVectorStoreFile(ctx, VectorStoreFileCreateInput{
		VectorStoreID: "vs-1", FileID: "file-src", SystemAccountID: testScopeA, APIKeyID: testKeyA,
		Status:     VectorStoreFileStatusCompleted,
		Attributes: map[string]any{"lang": "go", "size": 3},
		Chunks: []ChunkInput{
			{ContentText: "the go language", ContentPreview: "the go language", TokenEstimate: 4, KeywordIndexText: "the go language"},
			{ContentText: "rust is fast", ContentPreview: "rust is fast", TokenEstimate: 3, KeywordIndexText: "rust is fast"},
			{ContentText: "go go go", ContentPreview: "go go go", TokenEstimate: 2, KeywordIndexText: "go go go"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		options    SearchOptions
		wantTexts  []string
		wantScores []float64
	}{
		{
			// Node caps the per-chunk score at 1, so every matching chunk
			// scores exactly 1 and ties fall back to file_id + chunk_index.
			name:       "matching chunks all cap at score 1 and sort by chunk index",
			options:    SearchOptions{VectorStoreID: "vs-1", SystemAccountID: testScopeA, APIKeyID: testKeyA, Query: "go"},
			wantTexts:  []string{"the go language", "go go go"},
			wantScores: []float64{1, 1},
		},
		{
			name: "attribute equality filter",
			options: SearchOptions{VectorStoreID: "vs-1", SystemAccountID: testScopeA, APIKeyID: testKeyA,
				Query: "go", Filters: map[string]any{"key": "lang", "value": "go"}},
			wantTexts: []string{"the go language", "go go go"},
		},
		{
			name: "attribute ne filter",
			options: SearchOptions{VectorStoreID: "vs-1", SystemAccountID: testScopeA, APIKeyID: testKeyA,
				Query: "go", Filters: map[string]any{"key": "lang", "type": "ne", "value": "go"}},
			wantTexts: []string{},
		},
		{
			name: "gt filter on numeric attribute",
			options: SearchOptions{VectorStoreID: "vs-1", SystemAccountID: testScopeA, APIKeyID: testKeyA,
				Query: "go", Filters: map[string]any{"key": "size", "type": "gt", "value": float64(2)}},
			wantTexts: []string{"the go language", "go go go"},
		},
		{
			name: "in filter",
			options: SearchOptions{VectorStoreID: "vs-1", SystemAccountID: testScopeA, APIKeyID: testKeyA,
				Query: "go", Filters: map[string]any{"key": "lang", "type": "in", "value": []any{"rust"}}},
			wantTexts: []string{},
		},
		{
			name: "and filter mismatch drops all",
			options: SearchOptions{VectorStoreID: "vs-1", SystemAccountID: testScopeA, APIKeyID: testKeyA,
				Query:   "go",
				Filters: map[string]any{"type": "and", "filters": []any{map[string]any{"key": "lang", "value": "go"}, map[string]any{"key": "size", "type": "lt", "value": float64(2)}}}},
			wantTexts: []string{},
		},
		{
			name: "score threshold drops weak matches",
			options: SearchOptions{VectorStoreID: "vs-1", SystemAccountID: testScopeA, APIKeyID: testKeyA,
				Query: "go", ScoreThreshold: floatPtr(0.9)},
			wantTexts: []string{"the go language", "go go go"},
		},
		{
			name: "max num results caps output",
			options: SearchOptions{VectorStoreID: "vs-1", SystemAccountID: testScopeA, APIKeyID: testKeyA,
				Query: "go", MaxNumResults: intPtr(1)},
			wantTexts: []string{"the go language"},
		},
		{
			name:      "越权 search stays empty",
			options:   SearchOptions{VectorStoreID: "vs-1", SystemAccountID: testScopeB, APIKeyID: testKeyB, Query: "go"},
			wantTexts: []string{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			results, err := store.SearchVectorStore(ctx, tc.options)
			if err != nil {
				t.Fatal(err)
			}
			texts := []string{}
			for _, result := range results {
				texts = append(texts, result.ContentText)
			}
			if strings.Join(texts, "|") != strings.Join(tc.wantTexts, "|") {
				t.Fatalf("texts = %v, want %v (scores %v)", texts, tc.wantTexts, results)
			}
			for index, want := range tc.wantScores {
				if results[index].Score != want {
					t.Fatalf("score[%d] = %v, want %v", index, results[index].Score, want)
				}
			}
		})
	}
}

func TestVectorStoreFileCountersAndStatuses(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	vsID, fileID := seedVectorStoreFixture(t, store)
	statuses := []string{VectorStoreFileStatusInProgress, VectorStoreFileStatusCompleted, VectorStoreFileStatusFailed, VectorStoreFileStatusCancelled, "unknown"}
	for index, status := range statuses {
		clone := fileID + fmt.Sprintf("-%d", index)
		// Attach the same underlying file multiple times is impossible (PK),
		// so create additional source files for each status.
		createTestFile(t, store, t.TempDir(), clone, "assistants", []byte("x"), "text/plain", nil)
		if _, err := store.CreateVectorStoreFile(ctx, VectorStoreFileCreateInput{
			VectorStoreID: vsID, FileID: clone, SystemAccountID: testScopeA, APIKeyID: testKeyA,
			Status: status,
		}); err != nil {
			t.Fatal(err)
		}
	}
	record, err := store.FindVectorStore(ctx, vsID, testScopeA, testKeyA)
	if err != nil {
		t.Fatal(err)
	}
	counts := record.FileCounts
	if counts.InProgress != 2 || counts.Completed != 1 || counts.Failed != 1 || counts.Cancelled != 1 || counts.Total != 5 {
		t.Fatalf("counts = %+v (unknown status folded into in_progress)", counts)
	}
	// The stored record stays active; the in_progress state is derived at
	// render time (Node vectorStoreObject status logic).
	if record.Status != "active" {
		t.Fatalf("status = %s", record.Status)
	}
}

func TestVectorStoreListCursors(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	for index := 0; index < 4; index++ {
		if _, err := store.CreateVectorStore(ctx, fmt.Sprintf("vs-%d", index), VectorStoreCreateInput{
			SystemAccountID: testScopeA, APIKeyID: testKeyA,
		}); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name     string
		options  VectorStoreListOptions
		wantIDs  []string
		wantMore bool
	}{
		{
			name:    "desc default",
			options: VectorStoreListOptions{SystemAccountID: testScopeA, APIKeyID: testKeyA},
			wantIDs: []string{"vs-3", "vs-2", "vs-1", "vs-0"},
		},
		{
			name:    "after cursor desc",
			options: VectorStoreListOptions{SystemAccountID: testScopeA, APIKeyID: testKeyA, After: "vs-2"},
			wantIDs: []string{"vs-1", "vs-0"},
		},
		{
			name:    "before cursor desc walks to newer side",
			options: VectorStoreListOptions{SystemAccountID: testScopeA, APIKeyID: testKeyA, Before: "vs-2"},
			wantIDs: []string{"vs-3"},
		},
		{
			name:    "unknown after cursor empty",
			options: VectorStoreListOptions{SystemAccountID: testScopeA, APIKeyID: testKeyA, After: "vs-zzz"},
			wantIDs: []string{},
		},
		{
			name:     "limit + hasMore",
			options:  VectorStoreListOptions{SystemAccountID: testScopeA, APIKeyID: testKeyA, Limit: intPtr(3)},
			wantIDs:  []string{"vs-3", "vs-2", "vs-1"},
			wantMore: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := store.ListVectorStores(ctx, tc.options)
			if err != nil {
				t.Fatal(err)
			}
			ids := []string{}
			for _, item := range result.Items {
				ids = append(ids, item.ID)
			}
			if strings.Join(ids, ",") != strings.Join(tc.wantIDs, ",") {
				t.Fatalf("ids = %v want %v", ids, tc.wantIDs)
			}
			if result.HasMore != tc.wantMore {
				t.Fatalf("hasMore = %v", result.HasMore)
			}
		})
	}
}

func TestVectorStoreCreateRequiresExistingFileAndStore(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if _, err := store.CreateVectorStore(ctx, "vs-1", VectorStoreCreateInput{SystemAccountID: testScopeA, APIKeyID: testKeyA}); err != nil {
		t.Fatal(err)
	}
	createTestFile(t, store, t.TempDir(), "file-1", "assistants", []byte("x"), "text/plain", nil)

	tests := []struct {
		name    string
		input   VectorStoreFileCreateInput
		wantNil bool
	}{
		{
			name: "unknown vector store",
			input: VectorStoreFileCreateInput{VectorStoreID: "vs-x", FileID: "file-1",
				SystemAccountID: testScopeA, APIKeyID: testKeyA, Status: "completed"},
			wantNil: true,
		},
		{
			name: "unknown file",
			input: VectorStoreFileCreateInput{VectorStoreID: "vs-1", FileID: "file-x",
				SystemAccountID: testScopeA, APIKeyID: testKeyA, Status: "completed"},
			wantNil: true,
		},
		{
			name: "越权 file",
			input: VectorStoreFileCreateInput{VectorStoreID: "vs-1", FileID: "file-1",
				SystemAccountID: testScopeB, APIKeyID: testKeyB, Status: "completed"},
			wantNil: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			record, err := store.CreateVectorStoreFile(ctx, tc.input)
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantNil && record != nil {
				t.Fatalf("expected nil record, got %+v", record)
			}
		})
	}
}

func TestVectorStoreUpsertReplacesChunks(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	vsID, fileID := seedVectorStoreFixture(t, store)
	first := []ChunkInput{
		{ContentText: "one", ContentPreview: "one", TokenEstimate: 1, KeywordIndexText: "one"},
		{ContentText: "two", ContentPreview: "two", TokenEstimate: 1, KeywordIndexText: "two"},
	}
	if _, err := store.CreateVectorStoreFile(ctx, VectorStoreFileCreateInput{
		VectorStoreID: vsID, FileID: fileID, SystemAccountID: testScopeA, APIKeyID: testKeyA,
		Status: VectorStoreFileStatusCompleted, Chunks: first,
	}); err != nil {
		t.Fatal(err)
	}
	second := []ChunkInput{{ContentText: "only", ContentPreview: "only", TokenEstimate: 1, KeywordIndexText: "only"}}
	if _, err := store.CreateVectorStoreFile(ctx, VectorStoreFileCreateInput{
		VectorStoreID: vsID, FileID: fileID, SystemAccountID: testScopeA, APIKeyID: testKeyA,
		Status: VectorStoreFileStatusCompleted, Chunks: second,
	}); err != nil {
		t.Fatal(err)
	}
	chunks, err := store.ListVectorStoreFileChunks(ctx, vsID, fileID, testScopeA, testKeyA, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || chunks[0].ContentText != "only" {
		t.Fatalf("chunks after upsert = %+v", chunks)
	}
	if chunks[0].ChunkID == "" || len(chunks[0].ChunkID) != len("vschunk_")+32 {
		t.Fatalf("chunk id shape = %q", chunks[0].ChunkID)
	}
}

func floatPtr(value float64) *float64 { return &value }

package openaicompat

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Bridge executor tests: file resolver + file search.

func TestFileResolver(t *testing.T) {
	env := newRouteEnv(t, nil)
	store := env.Deps.Store
	resolver := env.Deps.FileResolverForScope(&GatewayScope{SystemAccountID: testScopeA, APIKeyID: testKeyA})
	ctx := context.Background()

	textRecord := createTestFile(t, store, env.FilesRoot, "file-text", "assistants", []byte("hello resolver"), "text/plain", nil)
	binaryRecord := createTestFile(t, store, env.FilesRoot, "file-bin", "assistants", []byte{0xde, 0xad, 0xbe, 0xef}, "image/png", nil)

	tests := []struct {
		name       string
		fileID     string
		wantNil    bool
		wantText   string
		wantBase64 string
	}{
		{name: "text media resolves as utf8", fileID: "file-text", wantText: "hello resolver"},
		{name: "binary media resolves as base64", fileID: "file-bin", wantBase64: "3q2+7w=="},
		{name: "missing file resolves nil", fileID: "missing", wantNil: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resolved, err := resolver.ResolveFile(ctx, FileResolveInput{FileID: tc.fileID})
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantNil {
				if resolved != nil {
					t.Fatalf("expected nil, got %+v", resolved)
				}
				return
			}
			if resolved == nil {
				t.Fatal("expected resolved file")
			}
			if resolved.ContentText != tc.wantText || resolved.ContentBase64 != tc.wantBase64 {
				t.Fatalf("resolved = %+v", resolved)
			}
		})
	}
	_ = textRecord
	_ = binaryRecord

	// 越权: scope B resolver cannot see scope A files.
	other := env.Deps.FileResolverForScope(&GatewayScope{SystemAccountID: testScopeB, APIKeyID: testKeyB})
	if resolved, err := other.ResolveFile(ctx, FileResolveInput{FileID: "file-text"}); err != nil || resolved != nil {
		t.Fatalf("越权 resolve = %+v %v", resolved, err)
	}
}

func TestFileResolverOversize(t *testing.T) {
	env := newRouteEnv(t, nil)
	store := env.Deps.Store
	// Record claims > 32 MiB; only the metadata matters for the guard.
	media := "text/plain"
	storageKey := "files/ov/file-big"
	record, err := store.CreateFile(t.Context(), FileCreateInput{
		ID: "file-big", SystemAccountID: testScopeA, APIKeyID: testKeyA,
		Purpose: "assistants", Filename: "big.txt", Bytes: BridgeMaxFileBytes + 1,
		MediaType: &media, StorageKey: storageKey, SHA256: "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = record
	resolver := env.Deps.FileResolverForScope(&GatewayScope{SystemAccountID: testScopeA, APIKeyID: testKeyA})
	_, err = resolver.ResolveFile(t.Context(), FileResolveInput{FileID: "file-big"})
	if err == nil {
		t.Fatal("expected oversize error")
	}
	bridgeErr, ok := err.(*BridgeRequestError)
	if !ok || bridgeErr.Code != "openai_anthropic_bridge_file_too_large" || bridgeErr.StatusCode != 413 {
		t.Fatalf("error = %v", err)
	}
	if bridgeErr.Message != "文件 file-big 超过 Anthropic bridge 单次解析大小上限" {
		t.Fatalf("message = %s", bridgeErr.Message)
	}
}

func TestFileSearchExecutor(t *testing.T) {
	env := newRouteEnv(t, nil)
	store := env.Deps.Store
	ctx := context.Background()

	_, _ = store.CreateVectorStore(ctx, "vs-1", VectorStoreCreateInput{SystemAccountID: testScopeA, APIKeyID: testKeyA})
	_, _ = store.CreateVectorStore(ctx, "vs-2", VectorStoreCreateInput{SystemAccountID: testScopeA, APIKeyID: testKeyA})
	for index, vs := range []string{"vs-1", "vs-2"} {
		createTestFile(t, store, env.FilesRoot, "file-"+vs, "assistants",
			[]byte(fmt.Sprintf("content %d", index)), "text/plain", nil)
		if _, err := store.CreateVectorStoreFile(ctx, VectorStoreFileCreateInput{
			VectorStoreID: vs, FileID: "file-" + vs, SystemAccountID: testScopeA, APIKeyID: testKeyA,
			Status: VectorStoreFileStatusCompleted,
			Chunks: []ChunkInput{
				{ContentText: fmt.Sprintf("shared%d text", index), ContentPreview: "x", TokenEstimate: 1, KeywordIndexText: fmt.Sprintf("shared%d text", index)},
			},
		}); err != nil {
			t.Fatal(err)
		}
	}

	executor := env.Deps.FileSearchExecutorForScope(&GatewayScope{SystemAccountID: testScopeA, APIKeyID: testKeyA})

	// Merged results across stores, sorted by fileId.
	output, err := executor.Search(ctx, FileSearchInput{VectorStoreIDs: []string{"vs-2", "vs-1"}, Query: "shared"})
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Queries) != 1 || output.Queries[0] != "shared" {
		t.Fatalf("queries = %v", output.Queries)
	}
	if len(output.Results) != 2 || output.Results[0].FileID != "file-vs-1" || output.Results[1].FileID != "file-vs-2" {
		t.Fatalf("results = %+v", output.Results)
	}

	// Max results clamp.
	output, err = executor.Search(ctx, FileSearchInput{VectorStoreIDs: []string{"vs-1", "vs-2"}, Query: "shared", MaxNumResults: floatPtr(1)})
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Results) != 1 {
		t.Fatalf("clamped results = %+v", output.Results)
	}

	// Unknown store -> bridge 404.
	_, err = executor.Search(ctx, FileSearchInput{VectorStoreIDs: []string{"vs-none"}, Query: "shared"})
	bridgeErr, ok := err.(*BridgeRequestError)
	if !ok || bridgeErr.Code != "openai_anthropic_bridge_file_search_vector_store_not_found" || bridgeErr.StatusCode != 404 {
		t.Fatalf("error = %v", err)
	}
	if bridgeErr.Message != "向量存储 vs-none 不存在" {
		t.Fatalf("message = %s", bridgeErr.Message)
	}

	// In-progress store -> bridge 409.
	execSQL(t, store, `INSERT INTO openai_compatible_vector_store_files
		(vector_store_id, file_id, system_account_id, api_key_id, attributes_json, chunking_strategy_json, status, usage_bytes, created_at, updated_at)
		VALUES ('vs-1', 'file-progress', ?, ?, '{}', '{}', 'in_progress', 0, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`, testScopeA, testKeyA)
	_, err = executor.Search(ctx, FileSearchInput{VectorStoreIDs: []string{"vs-1"}, Query: "shared"})
	bridgeErr, ok = err.(*BridgeRequestError)
	if !ok || bridgeErr.Code != "openai_anthropic_bridge_file_search_vector_store_not_ready" || bridgeErr.StatusCode != 409 {
		t.Fatalf("error = %v", err)
	}

	// 越权 executor sees nothing.
	other := env.Deps.FileSearchExecutorForScope(&GatewayScope{SystemAccountID: testScopeB, APIKeyID: testKeyB})
	_, err = other.Search(ctx, FileSearchInput{VectorStoreIDs: []string{"vs-1"}, Query: "shared"})
	bridgeErr, ok = err.(*BridgeRequestError)
	if !ok || bridgeErr.StatusCode != 404 {
		t.Fatalf("越权 search error = %v", err)
	}

	// Nil scope builds no executor.
	if env.Deps.FileSearchExecutorForScope(nil) != nil {
		t.Fatal("nil scope must return nil executor")
	}
	if env.Deps.FileResolverForScope(nil) != nil {
		t.Fatal("nil scope must return nil resolver")
	}
}

func TestNormalizeFileSearchMaxResults(t *testing.T) {
	tests := []struct {
		value *float64
		want  int
	}{
		{nil, 10},
		{floatPtr(0), 1},
		{floatPtr(1000), 50},
		{floatPtr(7), 7},
		{floatPtr(2.9), 2},
	}
	for _, tc := range tests {
		if got := normalizeFileSearchMaxResults(tc.value); got != tc.want {
			t.Fatalf("normalize(%v) = %d, want %d", tc.value, got, tc.want)
		}
	}
}

func TestFileObjectPathTraversalGuard(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		storageKey string
		wantErr    bool
	}{
		{storageKey: "files/ab/ok", wantErr: false},
		{storageKey: "../escape", wantErr: true},
		{storageKey: "files/../../escape", wantErr: true},
		{storageKey: "/etc/passwd", wantErr: true},
		{storageKey: ".", wantErr: true},
		{storageKey: "", wantErr: true},
	}
	for _, tc := range tests {
		path, err := FileObjectPath(root, tc.storageKey)
		if tc.wantErr && err == nil {
			t.Fatalf("key %q accepted: %s", tc.storageKey, path)
		}
		if !tc.wantErr && err != nil {
			t.Fatalf("key %q rejected: %v", tc.storageKey, err)
		}
		if err == nil && !strings.HasPrefix(path, root) {
			t.Fatalf("path %s escapes root %s", path, root)
		}
	}
}

func TestStorageKeyForFileShape(t *testing.T) {
	key := StorageKeyForFile("file-abc")
	if !strings.HasPrefix(key, "files/") || !strings.Contains(key, "file-abc") {
		t.Fatalf("key = %s", key)
	}
	parts := strings.Split(key, "/")
	if len(parts) != 3 || parts[0] != "files" || len(parts[1]) != 8 {
		t.Fatalf("key shape = %s (%v)", key, parts)
	}
	// Node: "###" sanitizes to "___" (the "file" fallback is for empty ids).
	if StorageKeyForFile("###") != "files/___/___" {
		t.Fatalf("sanitized key = %s", StorageKeyForFile("###"))
	}
	// An empty id falls back to "file" before the shard is derived, so the
	// shard is "file" as well (Node's 'default' branch is unreachable).
	if StorageKeyForFile("") != "files/file/file" {
		t.Fatalf("empty key = %s", StorageKeyForFile(""))
	}
}

func TestRemoveFileObjectMissing(t *testing.T) {
	root := t.TempDir()
	if err := RemoveFileObject(root, "files/aa/none"); err != nil {
		t.Fatalf("missing remove should be a no-op: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "files")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("root untouched: %v", err)
	}
}

package openaicompat

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// Files store tests mirror storage/openai-compatible-files.repository.ts
// behavior: CRUD, keyset pagination, 越权 scoping and soft delete.

func TestFilesStoreCRUD(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	created, err := store.CreateFile(ctx, FileCreateInput{
		ID: "file-1", SystemAccountID: testScopeA, APIKeyID: testKeyA,
		Purpose: "assistants", Filename: "a.txt", Bytes: 3,
		StorageKey: "files/ab/file-1", SHA256: "aa",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != "processed" || created.Bytes != 3 || created.Filename != "a.txt" {
		t.Fatalf("unexpected created record: %+v", created)
	}

	found, err := store.FindFile(ctx, "file-1", testScopeA, testKeyA)
	if err != nil || found == nil {
		t.Fatalf("FindFile: %v %v", found, err)
	}

	// 越权：另一个 system account / api key 的查询一律落空。
	for _, tc := range []struct{ systemAccount, apiKey string }{
		{testScopeB, testKeyA},
		{testScopeA, testKeyB},
		{testScopeB, testKeyB},
	} {
		if record, err := store.FindFile(ctx, "file-1", tc.systemAccount, tc.apiKey); err != nil || record != nil {
			t.Fatalf("cross-scope lookup should miss: %+v %v", record, err)
		}
	}

	deleted, err := store.DeleteFile(ctx, "file-1", testScopeA, testKeyA)
	if err != nil || deleted == nil {
		t.Fatalf("DeleteFile: %v %v", deleted, err)
	}
	if deleted.Status != "deleted" || deleted.DeletedAt == nil {
		t.Fatalf("soft delete not applied: %+v", deleted)
	}
	if record, err := store.FindFile(ctx, "file-1", testScopeA, testKeyA); err != nil || record != nil {
		t.Fatalf("deleted file must not be findable: %+v %v", record, err)
	}
	if deleted2, err := store.DeleteFile(ctx, "file-1", testScopeA, testKeyA); err != nil || deleted2 != nil {
		t.Fatalf("second delete should miss: %v %v", deleted2, err)
	}
}

func TestFilesStoreListPagination(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	for index := 0; index < 5; index++ {
		if _, err := store.CreateFile(ctx, FileCreateInput{
			ID: fmt.Sprintf("file-%d", index), SystemAccountID: testScopeA, APIKeyID: testKeyA,
			Purpose: "assistants", Filename: fmt.Sprintf("%d.txt", index), Bytes: int64(index),
			StorageKey: fmt.Sprintf("files/aa/file-%d", index), SHA256: "x",
		}); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name     string
		options  FileListOptions
		wantIDs  []string
		wantMore bool
	}{
		{
			name:    "default desc order and default limit",
			options: FileListOptions{SystemAccountID: testScopeA, APIKeyID: testKeyA},
			wantIDs: []string{"file-4", "file-3", "file-2", "file-1", "file-0"},
		},
		{
			name:    "asc order",
			options: FileListOptions{SystemAccountID: testScopeA, APIKeyID: testKeyA, Order: "asc"},
			wantIDs: []string{"file-0", "file-1", "file-2", "file-3", "file-4"},
		},
		{
			name: "purpose filter skips others",
			options: FileListOptions{SystemAccountID: testScopeA, APIKeyID: testKeyA,
				Purpose: strPtr("code_interpreter_output")},
			wantIDs: []string{},
		},
		{
			name:    "limit clamps hasMore",
			options: FileListOptions{SystemAccountID: testScopeA, APIKeyID: testKeyA, Limit: intPtr(2)},
			wantIDs: []string{"file-4", "file-3"}, wantMore: true,
		},
		{
			name:    "limit above max clamps to 100 (no rows lost)",
			options: FileListOptions{SystemAccountID: testScopeA, APIKeyID: testKeyA, Limit: intPtr(1000)},
			wantIDs: []string{"file-4", "file-3", "file-2", "file-1", "file-0"},
		},
		{
			name:    "unknown after cursor yields empty page",
			options: FileListOptions{SystemAccountID: testScopeA, APIKeyID: testKeyA, After: "file-zzz"},
			wantIDs: []string{},
		},
		{
			name:    "after cursor paginates desc",
			options: FileListOptions{SystemAccountID: testScopeA, APIKeyID: testKeyA, After: "file-2"},
			wantIDs: []string{"file-1", "file-0"},
		},
		{
			name:    "越权 scope stays empty",
			options: FileListOptions{SystemAccountID: testScopeB, APIKeyID: testKeyB},
			wantIDs: []string{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := store.ListFiles(ctx, tc.options)
			if err != nil {
				t.Fatal(err)
			}
			got := []string{}
			for _, item := range result.Items {
				got = append(got, item.ID)
			}
			if len(got) == 0 && len(tc.wantIDs) == 0 {
				// ok
			} else if strings.Join(got, ",") != strings.Join(tc.wantIDs, ",") {
				t.Fatalf("ids = %v, want %v", got, tc.wantIDs)
			}
			if result.HasMore != tc.wantMore {
				t.Fatalf("hasMore = %v, want %v", result.HasMore, tc.wantMore)
			}
		})
	}
}

func TestFilesStoreContainerPurposeFilter(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	container := "container-1"
	if _, err := store.CreateFile(ctx, FileCreateInput{
		ID: "file-c", SystemAccountID: testScopeA, APIKeyID: testKeyA,
		Purpose: "code_interpreter_output", ContainerID: &container,
		Filename: "out.png", Bytes: 10, StorageKey: "files/aa/file-c", SHA256: "x",
	}); err != nil {
		t.Fatal(err)
	}
	result, err := store.ListFiles(ctx, FileListOptions{
		SystemAccountID: testScopeA, APIKeyID: testKeyA,
		Purpose: strPtr("code_interpreter_output"), ContainerID: &container,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].ID != "file-c" {
		t.Fatalf("container filter failed: %+v", result.Items)
	}
}

// Dual-mode consistency: PostgreSQL mode must render Node-identical SQL
// (juhe_business-qualified tables and $n placeholders) while SQLite mode
// keeps '?' and bare names.
func TestStoreDualModeSQLRendering(t *testing.T) {
	db := newTestDB(t)
	pgStore, err := NewStore(db, true)
	if err != nil {
		t.Fatal(err)
	}
	sqliteStore, err := NewStore(db, false)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		query      string
		table      string
		wantSQLite string
		wantPG     string
	}{
		{
			name:       "files select",
			query:      "SELECT id FROM {T} WHERE id = ? LIMIT ?",
			table:      "openai_compatible_files",
			wantSQLite: "SELECT id FROM openai_compatible_files WHERE id = ? LIMIT ?",
			wantPG:     "SELECT id FROM juhe_business.openai_compatible_files WHERE id = $1 LIMIT $2",
		},
		{
			name:       "vector store files upsert",
			query:      "INSERT INTO {T} (vector_store_id, file_id) VALUES (?, ?) ON CONFLICT(vector_store_id, file_id) DO UPDATE SET status = excluded.status",
			table:      "openai_compatible_vector_store_files",
			wantSQLite: "INSERT INTO openai_compatible_vector_store_files (vector_store_id, file_id) VALUES (?, ?) ON CONFLICT(vector_store_id, file_id) DO UPDATE SET status = excluded.status",
			wantPG:     "INSERT INTO juhe_business.openai_compatible_vector_store_files (vector_store_id, file_id) VALUES ($1, $2) ON CONFLICT(vector_store_id, file_id) DO UPDATE SET status = excluded.status",
		},
		{
			name:       "chunks delete",
			query:      "DELETE FROM {T} WHERE vector_store_id = ? AND system_account_id = ?",
			table:      "openai_compatible_vector_store_chunks",
			wantSQLite: "DELETE FROM openai_compatible_vector_store_chunks WHERE vector_store_id = ? AND system_account_id = ?",
			wantPG:     "DELETE FROM juhe_business.openai_compatible_vector_store_chunks WHERE vector_store_id = $1 AND system_account_id = $2",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			templated := strings.ReplaceAll(tc.query, "{T}", "'"+tc.table+"'")
			_ = templated
			sqliteRendered := strings.ReplaceAll(tc.query, "{T}", sqliteStore.table(tc.table))
			pgRendered := pgStore.bind(strings.ReplaceAll(tc.query, "{T}", pgStore.table(tc.table)))
			if sqliteRendered != tc.wantSQLite {
				t.Fatalf("sqlite sql = %q, want %q", sqliteRendered, tc.wantSQLite)
			}
			if pgRendered != tc.wantPG {
				t.Fatalf("postgres sql = %q, want %q", pgRendered, tc.wantPG)
			}
		})
	}
}

// RunFilesStoreBehaviorSuite executes the store behavior suite against any
// store instance so both storage modes can share the exact same expectations.
func RunFilesStoreBehaviorSuite(t *testing.T, store *Store) {
	t.Helper()
	ctx := context.Background()
	record, err := store.CreateFile(ctx, FileCreateInput{
		ID: "file-suite", SystemAccountID: "s", APIKeyID: "k",
		Purpose: "assistants", Filename: "suite.txt", Bytes: 1,
		StorageKey: "files/su/file-suite", SHA256: "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	if record.ID != "file-suite" {
		t.Fatalf("unexpected record %s", record.ID)
	}
	if missing, err := store.FindFile(ctx, "file-suite", "s", "other"); err != nil || missing != nil {
		t.Fatalf("expected cross-key miss, got %+v %v", missing, err)
	}
}

func TestFilesStoreBehaviorSuiteSQLite(t *testing.T) {
	RunFilesStoreBehaviorSuite(t, newTestStore(t))
}

func TestNewStoreRequiresDB(t *testing.T) {
	if _, err := NewStore(nil, false); err == nil {
		t.Fatal("expected error for nil db")
	}
	if _, err := NewStore(newTestDB(t), false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCreateFileSurfacesDBErrors(t *testing.T) {
	store := newTestStore(t)
	db := newTestDB(t)
	db.Close()
	// After closing the pool the insert error must surface verbatim.
	broken, err := NewStore(db, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := broken.CreateFile(context.Background(), FileCreateInput{
		ID: "file-x", SystemAccountID: "s", APIKeyID: "k",
		Purpose: "p", Filename: "f", StorageKey: "files/x/file-x", SHA256: "x",
	}); err == nil {
		t.Fatal("expected database error")
	}
	_ = store
}

func strPtr(value string) *string { return &value }

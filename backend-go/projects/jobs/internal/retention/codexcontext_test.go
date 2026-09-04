package retention

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveCodexContextStorageCleanupPath(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name       string
		storageKey string
		wantErr    string
	}{
		{name: "plain relative key", storageKey: "resp/a/b.json"},
		{name: "windows separators normalize", storageKey: "resp\\a\\b.json"},
		{name: "leading slashes stripped", storageKey: "//resp/a/b.json"},
		// A Windows drive prefix is treated as an in-root filename here —
		// stricter than the Node win32 resolve, so the key can never escape.
		{name: "windows drive key stays inside root", storageKey: "C:/escape.json"},
		{name: "dot traversal rejected outright", storageKey: "resp/../a.json", wantErr: "Responses 桥接状态 storage key 非法"},
		{name: "bare dot dot rejected", storageKey: "..", wantErr: "Responses 桥接状态 storage key 非法"},
		{name: "embedded dot dot rejected", storageKey: "resp/a/../b.json", wantErr: "Responses 桥接状态 storage key 非法"},
		{name: "empty key rejected", storageKey: "", wantErr: "Responses 桥接状态 storage key 超出数据目录"},
		{name: "root itself rejected", storageKey: ".", wantErr: "Responses 桥接状态 storage key 超出数据目录"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveCodexContextStorageCleanupPath(root, tt.storageKey)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			// Containment guarantee: the resolved target stays literally
			// inside the root directory tree.
			if !strings.HasPrefix(strings.ToLower(got), strings.ToLower(filepath.Clean(root))+string(filepath.Separator)) {
				t.Fatalf("resolved path %q escapes root %q", got, root)
			}
			if _, err := os.Stat(got); err == nil && got == filepath.Clean(root) {
				t.Fatal("resolved path must not be the root itself")
			}
		})
	}
}

func TestFilesystemKeyDeleter(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "resp"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.json")
	write("resp/b.json")

	deleter := NewFilesystemKeyDeleter(root)
	result, err := deleter.DeleteStorageKeys(context.Background(), []string{
		"a.json",
		"a.json", // dedupe keeps one call
		"missing.json",
		"resp/b.json",
		"../escape.json",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Deleted != 2 {
		t.Fatalf("deleted = %d, want 2", result.Deleted)
	}
	if len(result.SucceededStorageKeys) != 3 {
		t.Fatalf("succeeded = %v, want the three valid keys", result.SucceededStorageKeys)
	}
	if len(result.Failures) != 1 || result.Failures[0].StorageKey != "../escape.json" {
		t.Fatalf("failures = %+v, want the traversal key", result.Failures)
	}
	if result.Failures[0].Error != "Responses 桥接状态 storage key 非法" {
		t.Fatalf("failure error = %q", result.Failures[0].Error)
	}
	if _, err := os.Stat(filepath.Join(root, "a.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a.json should have been deleted")
	}
	if _, err := os.Stat(filepath.Join(root, "resp", "b.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("resp/b.json should have been deleted")
	}
}

// TestCodexContextProcessorSettlesBatch mirrors
// processCodexContextStorageCleanupBatch: delete → settle → warn on failures
// → abort check → deleted count.
func TestCodexContextProcessorSettlesBatch(t *testing.T) {
	t.Run("success path returns deleted count", func(t *testing.T) {
		settler := &fakeDbService{}
		deleter := &scriptedDeleter{result: StorageKeyDeletionResult{
			Deleted:              2,
			SucceededStorageKeys: []string{"a", "b"},
		}}
		processor := &CodexContextStorageProcessor{Deleter: deleter, DB: settler, Logger: discardLogger()}
		deleted, err := processor.ProcessBatch(context.Background(), []string{"a", "b"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if deleted != 2 {
			t.Fatalf("deleted = %d, want 2", deleted)
		}
		if deleter.keys[0][0] != "a" || deleter.keys[0][1] != "b" {
			t.Fatalf("deleter keys = %v", deleter.keys)
		}
	})
	t.Run("settlement failures warn", func(t *testing.T) {
		logger, buffer := newTestLogger()
		settler := &fakeDbService{}
		deleter := &scriptedDeleter{result: StorageKeyDeletionResult{
			Deleted:              1,
			SucceededStorageKeys: []string{"ok"},
			Failures:             []CodexContextCleanupFailure{{StorageKey: "bad", Error: "denied"}},
		}}
		processor := &CodexContextStorageProcessor{Deleter: deleter, DB: settler, Logger: logger}
		deleted, err := processor.ProcessBatch(context.Background(), []string{"ok", "bad"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if deleted != 1 {
			t.Fatalf("deleted = %d, want 1", deleted)
		}
		assertLogContains(t, buffer,
			"codex_context_storage_cleanup_deferred",
			"Codex Context 状态文件删除失败，已持久化等待重试",
			"failedCount",
		)
	})
	t.Run("settle error propagates", func(t *testing.T) {
		settler := &settleErrorDb{err: errors.New("settle failed")}
		processor := &CodexContextStorageProcessor{Deleter: &scriptedDeleter{}, DB: settler, Logger: discardLogger()}
		if _, err := processor.ProcessBatch(context.Background(), []string{"a"}); err == nil || err.Error() != "settle failed" {
			t.Fatalf("error = %v, want settle failed", err)
		}
	})
	t.Run("abort after settle surfaces", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		processor := &CodexContextStorageProcessor{Deleter: &scriptedDeleter{result: StorageKeyDeletionResult{Deleted: 1, SucceededStorageKeys: []string{"a"}}}, DB: &fakeDbService{}, Logger: discardLogger()}
		deleted, err := processor.ProcessBatch(ctx, []string{"a"})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
		if deleted != 1 {
			t.Fatalf("deleted = %d, want the settled count", deleted)
		}
	})
	t.Run("nil deleter fails closed", func(t *testing.T) {
		processor := &CodexContextStorageProcessor{DB: &fakeDbService{}}
		if _, err := processor.ProcessBatch(context.Background(), nil); err == nil {
			t.Fatal("expected the missing-deleter error")
		}
	})
}

type scriptedDeleter struct {
	result StorageKeyDeletionResult
	keys   [][]string
}

func (s *scriptedDeleter) DeleteStorageKeys(_ context.Context, storageKeys []string) (StorageKeyDeletionResult, error) {
	s.keys = append(s.keys, storageKeys)
	return s.result, nil
}

type settleErrorDb struct {
	fakeDbService
	err error
}

func (s *settleErrorDb) SettleCodexContextStorageCleanup(context.Context, CodexContextSettlement) (CodexContextSettlementResult, error) {
	return CodexContextSettlementResult{}, s.err
}

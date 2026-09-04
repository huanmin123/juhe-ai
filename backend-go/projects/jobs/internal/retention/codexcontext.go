package retention

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// Codex-context storage cleanup mirrors
// modules/background/codex-context-storage-cleanup.service.ts: delete the
// settled storage keys under the codex context root, then persist the
// settlement (successes acknowledged, failures deferred for retry).

// ResolveCodexContextStorageCleanupPath mirrors
// resolveCodexContextStorageCleanupPath: backslashes normalize to slashes,
// leading slashes are stripped, any '..' is rejected outright, and the
// resolved target must stay inside the root. Error text is byte-identical.
func ResolveCodexContextStorageCleanupPath(root, storageKey string) (string, error) {
	normalizedKey := strings.ReplaceAll(storageKey, "\\", "/")
	normalizedKey = strings.TrimLeft(normalizedKey, "/")
	if strings.Contains(normalizedKey, "..") {
		return "", errors.New("Responses 桥接状态 storage key 非法")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", errors.New("Responses 桥接状态 storage key 超出数据目录")
	}
	rootAbs = filepath.Clean(rootAbs)
	target := filepath.Clean(filepath.Join(rootAbs, filepath.FromSlash(normalizedKey)))
	rel, relErr := filepath.Rel(rootAbs, target)
	if relErr != nil || rel == "" || rel == "." ||
		rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || strings.HasPrefix(rel, "../") {
		return "", errors.New("Responses 桥接状态 storage key 超出数据目录")
	}
	if filepath.Clean(filepath.Join(rootAbs, rel)) != target {
		return "", errors.New("Responses 桥接状态 storage key 超出数据目录")
	}
	return target, nil
}

// FilesystemKeyDeleter is the default CodexContextStorageKeyDeleter: it
// deletes existing files under Root and reports per-key failures while
// keeping the succeeded list aligned with Node.
type FilesystemKeyDeleter struct {
	Root string
}

// NewFilesystemKeyDeleter builds the default deleter.
func NewFilesystemKeyDeleter(root string) *FilesystemKeyDeleter {
	return &FilesystemKeyDeleter{Root: root}
}

// DeleteStorageKeys mirrors deleteCodexContextStorageKeys: keys are deduped
// in first-seen order, missing files still count as succeeded, and a failed
// key never lands in the succeeded list.
func (d *FilesystemKeyDeleter) DeleteStorageKeys(_ context.Context, storageKeys []string) (StorageKeyDeletionResult, error) {
	result := StorageKeyDeletionResult{SucceededStorageKeys: []string{}}
	seen := make(map[string]bool, len(storageKeys))
	for _, storageKey := range storageKeys {
		if seen[storageKey] {
			continue
		}
		seen[storageKey] = true
		path, err := ResolveCodexContextStorageCleanupPath(d.Root, storageKey)
		if err != nil {
			result.Failures = append(result.Failures, CodexContextCleanupFailure{StorageKey: storageKey, Error: err.Error()})
			continue
		}
		_, statErr := os.Lstat(path)
		if statErr == nil {
			if rmErr := os.RemoveAll(path); rmErr != nil {
				result.Failures = append(result.Failures, CodexContextCleanupFailure{StorageKey: storageKey, Error: rmErr.Error()})
				continue
			}
			result.Deleted++
		} else if !errors.Is(statErr, os.ErrNotExist) {
			result.Failures = append(result.Failures, CodexContextCleanupFailure{StorageKey: storageKey, Error: statErr.Error()})
			continue
		}
		result.SucceededStorageKeys = append(result.SucceededStorageKeys, storageKey)
	}
	return result, nil
}

// CodexContextStorageProcessor mirrors processCodexContextStorageCleanupBatch:
// delete the batch, settle the result through the DB service, warn about
// deferred failures, then surface an abort.
type CodexContextStorageProcessor struct {
	Deleter CodexContextStorageKeyDeleter
	DB      DbService
	Logger  *slog.Logger
}

// NewCodexContextStorageProcessor builds the processor; a nil deleter falls
// back to a FilesystemKeyDeleter over root.
func NewCodexContextStorageProcessor(root string, db DbService, logger *slog.Logger) *CodexContextStorageProcessor {
	return &CodexContextStorageProcessor{Deleter: NewFilesystemKeyDeleter(root), DB: db, Logger: logger}
}

// ProcessBatch mirrors processCodexContextStorageCleanupBatch and returns the
// number of files actually deleted.
func (p *CodexContextStorageProcessor) ProcessBatch(ctx context.Context, storageKeys []string) (int64, error) {
	if p == nil || p.Deleter == nil {
		return 0, errors.New("retention codex context storage deleter 未初始化")
	}
	deletion, err := p.Deleter.DeleteStorageKeys(ctx, storageKeys)
	if err != nil {
		return 0, err
	}
	if _, err := p.db().SettleCodexContextStorageCleanup(ctx, CodexContextSettlement{
		SucceededStorageKeys: deletion.SucceededStorageKeys,
		Failures:             deletion.Failures,
	}); err != nil {
		return deletion.Deleted, err
	}
	if len(deletion.Failures) > 0 {
		p.logger().Warn("Codex Context 状态文件删除失败，已持久化等待重试",
			"event", "codex_context_storage_cleanup_deferred",
			"failedCount", len(deletion.Failures),
			"error", "部分 Codex Context 状态文件删除失败",
		)
	}
	if err := ctx.Err(); err != nil {
		return deletion.Deleted, err
	}
	return deletion.Deleted, nil
}

func (p *CodexContextStorageProcessor) db() DbService {
	if p.DB == nil {
		return missingDbService{}
	}
	return p.DB
}

func (p *CodexContextStorageProcessor) logger() *slog.Logger {
	if p.Logger != nil {
		return p.Logger
	}
	return slog.Default()
}

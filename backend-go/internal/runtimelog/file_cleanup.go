package runtimelog

import (
	"context"
	"errors"
	"os"
	"sort"
	"time"
)

type completedRotatedLogFile struct {
	path  string
	mtime time.Time
}

// cleanupRotatedFiles mirrors the Node log-retention rule: a rotated file is
// removable only after this owner has durably indexed it without a cursor error.
func (indexer *Indexer) cleanupRotatedFiles(ctx context.Context) (int64, error) {
	lease, err := ownerLeaseFromContext(ctx)
	if err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(indexer.config.LogDirectory)
	if err != nil {
		return 0, err
	}

	currentFiles := 0
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if entry.IsDir() {
			continue
		}
		_, kind, ok := ParseLogFileName(entry.Name())
		if ok && kind == LogFileCurrent {
			currentFiles++
		}
	}

	maxRotatedFiles := indexer.config.LogMaxFiles - currentFiles
	if maxRotatedFiles < 0 {
		maxRotatedFiles = 0
	}
	expiresBefore := time.Now().AddDate(0, 0, -indexer.config.LogRetentionDays)
	protectedCount := 0
	var deleted int64
	completed := make([]completedRotatedLogFile, 0)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if entry.IsDir() {
			continue
		}
		_, kind, ok := ParseLogFileName(entry.Name())
		if !ok || kind != LogFileRotated {
			continue
		}
		path := indexer.config.LogDirectory + string(os.PathSeparator) + entry.Name()
		info, statErr := entry.Info()
		if statErr != nil || !info.Mode().IsRegular() {
			protectedCount++
			continue
		}
		identity, identityErr := FileIdentity(path, info)
		if identityErr != nil {
			protectedCount++
			continue
		}
		cursor, cursorErr := indexer.store.FindCursor(ctx, path)
		if cursorErr != nil {
			return 0, cursorErr
		}
		if cursor == nil {
			cursor, cursorErr = indexer.store.FindCursorByIdentity(ctx, identity)
			if cursorErr != nil {
				return 0, cursorErr
			}
		}
		if cursor == nil || cursor.CursorOffset < info.Size() || cursor.LastErrorMessage != "" {
			protectedCount++
			continue
		}
		if info.ModTime().Before(expiresBefore) || maxRotatedFiles == 0 {
			if err := removeRotatedLogFile(ctx, indexer.store, lease, path); err != nil {
				return deleted, err
			}
			deleted++
			continue
		}
		completed = append(completed, completedRotatedLogFile{path: path, mtime: info.ModTime()})
	}

	allowedCompleted := maxRotatedFiles - protectedCount
	if allowedCompleted < 0 {
		allowedCompleted = 0
	}
	sort.Slice(completed, func(left, right int) bool {
		return completed[left].mtime.After(completed[right].mtime)
	})
	for index := allowedCompleted; index < len(completed); index++ {
		if err := ctx.Err(); err != nil {
			return deleted, err
		}
		if err := removeRotatedLogFile(ctx, indexer.store, lease, completed[index].path); err != nil {
			return deleted, err
		}
		deleted++
	}
	return deleted, nil
}

func removeRotatedLogFile(ctx context.Context, store Store, lease OwnerLease, path string) error {
	if err := store.VerifyOwnerLease(ctx, lease); err != nil {
		return err
	}
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

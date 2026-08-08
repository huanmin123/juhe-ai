package runtimelog

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const displacedIdentityPrefix = "__runtime_log_identity__:"
const writeFailureCursorMessage = "运行日志索引写入失败，游标已保留在最近一次成功写入位置，等待下一轮重试"

type cursorCommitError struct {
	cause  error
	cursor Cursor
}

func (err *cursorCommitError) Error() string {
	return err.cause.Error()
}

func (err *cursorCommitError) Unwrap() error {
	return err.cause
}

type Indexer struct {
	config        Config
	store         Store
	retentionDays int
}

func NewIndexer(config Config, store Store) *Indexer {
	return &Indexer{config: config, store: store, retentionDays: config.RetentionDays}
}

func (indexer *Indexer) Run(ctx context.Context) error {
	if err := indexer.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
		indexer.reportFailure("索引", err)
	}
	if err := indexer.RunRetention(ctx); err != nil && !errors.Is(err, context.Canceled) {
		indexer.reportFailure("保留清理", err)
	}
	pollTicker := time.NewTicker(indexer.config.PollInterval)
	retentionTicker := time.NewTicker(indexer.config.RetentionInterval)
	defer pollTicker.Stop()
	defer retentionTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-pollTicker.C:
			if err := indexer.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
				indexer.reportFailure("索引", err)
			}
		case <-retentionTicker.C:
			if err := indexer.RunRetention(ctx); err != nil && !errors.Is(err, context.Canceled) {
				indexer.reportFailure("保留清理", err)
			}
		}
	}
}

func (indexer *Indexer) reportFailure(operation string, err error) {
	fmt.Fprintf(os.Stderr, "运行日志%s失败，将在下一周期重试: %v\n", operation, err)
}

func (indexer *Indexer) RunOnce(ctx context.Context) error {
	if _, err := ownerLeaseFromContext(ctx); err != nil {
		return err
	}
	if err := indexer.refreshRetentionDays(ctx); err != nil {
		return err
	}
	files, err := indexer.discoverFiles()
	if err != nil {
		return err
	}
	var group sync.WaitGroup
	var errorsMu sync.Mutex
	var importErrors []error
	for _, file := range files {
		file := file
		group.Add(1)
		go func() {
			defer group.Done()
			if err := indexer.importFile(ctx, file); err != nil && !errors.Is(err, context.Canceled) {
				errorsMu.Lock()
				importErrors = append(importErrors, fmt.Errorf("%s: %w", file.Path, err))
				errorsMu.Unlock()
			}
		}()
	}
	group.Wait()
	return errors.Join(importErrors...)
}

func (indexer *Indexer) RunRetention(ctx context.Context) error {
	lease, err := ownerLeaseFromContext(ctx)
	if err != nil {
		return err
	}
	if err := indexer.refreshRetentionDays(ctx); err != nil {
		return err
	}
	result, err := indexer.store.Cleanup(ctx, lease, time.Now().UTC().AddDate(0, 0, -indexer.retentionDays), indexer.config.BatchSize, 1_000_000)
	if err != nil {
		return err
	}
	deletedFiles, err := indexer.cleanupRotatedFiles(ctx)
	if err != nil {
		return err
	}
	result.RotatedLogFiles = deletedFiles
	return nil
}

func (indexer *Indexer) discoverFiles() ([]LogFile, error) {
	entries, err := os.ReadDir(indexer.config.LogDirectory)
	if err != nil {
		return nil, err
	}
	files := make([]LogFile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		role, kind, ok := ParseLogFileName(entry.Name())
		if !ok {
			continue
		}
		files = append(files, LogFile{Path: filepath.Join(indexer.config.LogDirectory, entry.Name()), Role: role, Kind: kind})
	}
	sort.Slice(files, func(left int, right int) bool {
		if files[left].Kind != files[right].Kind {
			return files[left].Kind == LogFileRotated
		}
		return files[left].Path < files[right].Path
	})
	return files, nil
}

func (indexer *Indexer) importFile(ctx context.Context, file LogFile) error {
	info, err := os.Stat(file.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	identity, err := FileIdentity(file.Path, info)
	if err != nil {
		return err
	}
	cursor, err := indexer.resolveCursor(ctx, file, info, identity)
	if err != nil {
		return err
	}
	if info.Size() <= cursor.CursorOffset {
		cursor.FileSize = info.Size()
		cursor.FileMtimeMs = info.ModTime().UnixMilli()
		cursor.LastErrorMessage = ""
		return indexer.commit(ctx, nil, *cursor)
	}
	err = indexer.readAndCommit(ctx, file, info, identity, *cursor)
	if err == nil {
		return nil
	}
	var commitError *cursorCommitError
	if !errors.As(err, &commitError) {
		return err
	}
	failureCursor := commitError.cursor
	failureCursor.FileSize = info.Size()
	failureCursor.FileMtimeMs = info.ModTime().UnixMilli()
	failureCursor.LastReadAt = nowISO()
	failureCursor.LastErrorMessage = writeFailureCursorMessage
	if persistErr := indexer.commit(ctx, nil, failureCursor); persistErr != nil {
		return errors.Join(err, fmt.Errorf("持久化运行日志失败游标: %w", persistErr))
	}
	return err
}

func (indexer *Indexer) resolveCursor(ctx context.Context, file LogFile, info os.FileInfo, identity string) (*Cursor, error) {
	existing, err := indexer.store.FindCursor(ctx, file.Path)
	if err != nil {
		return nil, err
	}
	if existing != nil && existing.FileIdentity == identity {
		if info.Size() < existing.CursorOffset || info.Size() < existing.FileSize {
			reset := *existing
			reset.CursorOffset = 0
			reset.LineNumber = 0
			reset.FileSize = info.Size()
			reset.FileMtimeMs = info.ModTime().UnixMilli()
			reset.TruncationGeneration++
			reset.LastErrorMessage = ""
			if err := indexer.commit(ctx, nil, reset); err != nil {
				return nil, err
			}
			return &reset, nil
		}
		return existing, nil
	}
	if existing != nil && existing.FileIdentity != identity {
		created := newCursor(file.Path, identity, 0, info)
		var displaced *Cursor
		if existing.FileIdentity != "" {
			copy := *existing
			copy.LogFile = displacedIdentityPrefix + existing.FileIdentity
			displaced = &copy
		}
		if err := indexer.replaceCursor(ctx, displaced, created); err != nil {
			return nil, err
		}
		return &created, nil
	}
	identityCursor, err := indexer.store.FindCursorByIdentity(ctx, identity)
	if err != nil {
		return nil, err
	}
	if identityCursor != nil {
		if info.Size() < identityCursor.CursorOffset || info.Size() < identityCursor.FileSize {
			reset := *identityCursor
			reset.LogFile = file.Path
			reset.CursorOffset = 0
			reset.LineNumber = 0
			reset.FileSize = info.Size()
			reset.FileMtimeMs = info.ModTime().UnixMilli()
			reset.TruncationGeneration++
			reset.LastErrorMessage = ""
			if err := indexer.commit(ctx, nil, reset); err != nil {
				return nil, err
			}
			return &reset, nil
		}
		if identityCursor.LogFile != file.Path {
			relocated := *identityCursor
			relocated.LogFile = file.Path
			if err := indexer.copyCursor(ctx, relocated); err != nil {
				return nil, err
			}
			identityCursor = &relocated
		}
		return identityCursor, nil
	}
	offset := int64(0)
	if file.Kind == LogFileCurrent {
		offset = info.Size()
	}
	created := newCursor(file.Path, identity, offset, info)
	if err := indexer.commit(ctx, nil, created); err != nil {
		return nil, err
	}
	return &created, nil
}

func newCursor(logFile string, identity string, offset int64, info os.FileInfo) Cursor {
	now := nowISO()
	return Cursor{
		LogFile:      logFile,
		FileIdentity: identity,
		CursorOffset: offset,
		FileSize:     info.Size(),
		FileMtimeMs:  info.ModTime().UnixMilli(),
		LastReadAt:   now,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
}

func (indexer *Indexer) readAndCommit(ctx context.Context, file LogFile, info os.FileInfo, identity string, cursor Cursor) error {
	handle, err := os.Open(file.Path)
	if err != nil {
		return err
	}
	defer handle.Close()
	openedInfo, err := handle.Stat()
	if err != nil {
		return err
	}
	openedIdentity, err := FileIdentity(file.Path, openedInfo)
	if err != nil {
		return err
	}
	if openedIdentity != identity {
		return nil
	}
	if _, err := handle.Seek(cursor.CursorOffset, io.SeekStart); err != nil {
		return err
	}

	nextOffset := cursor.CursorOffset
	nextLineNumber := cursor.LineNumber
	reader := bufio.NewReader(handle)
	batch := make([]Record, 0, indexer.config.BatchSize)
	flush := func() error {
		committedCursor := cursor
		cursor.CursorOffset = nextOffset
		cursor.LineNumber = nextLineNumber
		cursor.FileSize = info.Size()
		cursor.FileMtimeMs = info.ModTime().UnixMilli()
		cursor.LastReadAt = nowISO()
		cursor.LastErrorMessage = ""
		if err := indexer.commit(ctx, batch, cursor); err != nil {
			return &cursorCommitError{cause: err, cursor: committedCursor}
		}
		batch = batch[:0]
		return nil
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, readErr := reader.ReadString('\n')
		if len(line) == 0 && errors.Is(readErr, io.EOF) {
			break
		}
		if !strings.HasSuffix(line, "\n") {
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				return readErr
			}
			break
		}
		lineStart := nextOffset
		nextOffset += int64(len(line))
		nextLineNumber++
		raw := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		sourceKey := identity + ":" + fmt.Sprint(lineStart)
		if cursor.TruncationGeneration > 0 {
			sourceKey = identity + ":" + fmt.Sprint(cursor.TruncationGeneration) + ":" + fmt.Sprint(lineStart)
		}
		if record := ParseLine(raw, LineOptions{SourceKey: sourceKey, LogFile: file.Path, LogOffset: lineStart, LineNumber: nextLineNumber}); record != nil {
			batch = append(batch, *record)
		}
		if len(batch) >= indexer.config.BatchSize {
			if err := flush(); err != nil {
				return err
			}
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
	}
	if len(batch) > 0 || nextOffset != cursor.CursorOffset {
		if err := flush(); err != nil {
			return err
		}
	}
	return nil
}

func (indexer *Indexer) commit(ctx context.Context, records []Record, cursor Cursor) error {
	lease, err := ownerLeaseFromContext(ctx)
	if err != nil {
		return err
	}
	return indexer.store.Commit(ctx, lease, records, cursor, indexer.retentionCutoff())
}

func (indexer *Indexer) copyCursor(ctx context.Context, cursor Cursor) error {
	lease, err := ownerLeaseFromContext(ctx)
	if err != nil {
		return err
	}
	return indexer.store.CopyCursor(ctx, lease, cursor)
}

func (indexer *Indexer) replaceCursor(ctx context.Context, displaced *Cursor, replacement Cursor) error {
	lease, err := ownerLeaseFromContext(ctx)
	if err != nil {
		return err
	}
	return indexer.store.ReplaceCursor(ctx, lease, displaced, replacement)
}

func (indexer *Indexer) retentionCutoff() time.Time {
	return time.Now().UTC().AddDate(0, 0, -indexer.retentionDays)
}

func (indexer *Indexer) refreshRetentionDays(ctx context.Context) error {
	days, err := indexer.store.RuntimeRetentionDays(ctx, indexer.config.RetentionDays)
	if err != nil {
		return err
	}
	indexer.retentionDays = days
	return nil
}

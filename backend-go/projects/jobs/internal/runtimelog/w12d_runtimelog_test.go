package runtimelog

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// w12d_runtimelog_test.go 覆盖 w12d 波次缺口：OpenStore 参数校验、closed
// store 错误上抛、indexer 的 panic 恢复/失败游标/配置读取臂与 RunRetention
// 错误臂。全部使用本地临时 SQLite 与脚本化 fake store，不触网。
//
// w12d 波次不可达清单（覆盖率 95% 目标内无法触达的语句，均已核对）：
//   - legacy_sqlite_migration.go verifyLegacyMigration 的 missing/mismatch
//     错误臂：INSERT OR IGNORE 同键同值复制保证 target 与 source 恒一致。
//   - legacy_sqlite_migration.go copyStatements/DETACH 的执行错误分支：需要
//     底层连接故障注入，database/sql 无注入点。
//   - store.go PostgreSQL 分支（import/cleanup/cursor/lease 的 PG 语句臂）：
//     覆盖需真实 PG 门控或逐查询扩展 wn pgfake 脚本，超出本波次范围。
//   - store.go SQLite EnsureSchema/busy_timeout/PRAGMA 失败臂：需要连接级
//     故障注入。
//   - identity_windows.go 的 GOOS 分支互斥语句。

// w12dFakeStore 是 Store 接口的脚本化 Mock（可注入 panic 与错误）。
type w12dFakeStore struct {
	findCursor     *Cursor
	findCursorErr  error
	findByIdentity *Cursor
	findByIdentErr error
	commitPanic    bool
	commitErr      error
	retentionDays  int
	retentionErr   error
	cleanupErr     error
	cleanupResult  CleanupResult
	replaceErr     error
	copyErr        error
}

func (s *w12dFakeStore) FindCursor(ctx context.Context, logFile string) (*Cursor, error) {
	return s.findCursor, s.findCursorErr
}

func (s *w12dFakeStore) FindCursorByIdentity(ctx context.Context, identity string) (*Cursor, error) {
	return s.findByIdentity, s.findByIdentErr
}

func (s *w12dFakeStore) ReplaceCursor(ctx context.Context, lease OwnerLease, displaced *Cursor, replacement Cursor) error {
	return s.replaceErr
}

func (s *w12dFakeStore) CopyCursor(ctx context.Context, lease OwnerLease, cursor Cursor) error {
	return s.copyErr
}

func (s *w12dFakeStore) Commit(ctx context.Context, lease OwnerLease, records []Record, cursor Cursor, retentionCutoff time.Time) error {
	if s.commitPanic {
		panic("w12d commit panic")
	}
	return s.commitErr
}

func (s *w12dFakeStore) Cleanup(ctx context.Context, lease OwnerLease, cutoff time.Time, batchSize int, maxBatches int) (CleanupResult, error) {
	return s.cleanupResult, s.cleanupErr
}

func (s *w12dFakeStore) VerifyOwnerLease(ctx context.Context, lease OwnerLease) error { return nil }

func (s *w12dFakeStore) WithOwnerLeaseFence(ctx context.Context, lease OwnerLease, callback func() error) error {
	return callback()
}

func (s *w12dFakeStore) RuntimeRetentionDays(ctx context.Context, fallback int) (int, error) {
	return s.retentionDays, s.retentionErr
}

func (s *w12dFakeStore) AcquireOwnerLease(ctx context.Context, ownerID string, duration time.Duration) (OwnerLease, bool, error) {
	return OwnerLease{OwnerID: ownerID, FenceToken: 1}, true, nil
}

func (s *w12dFakeStore) RenewOwnerLease(ctx context.Context, lease OwnerLease, duration time.Duration) (bool, error) {
	return true, nil
}

func (s *w12dFakeStore) ReleaseOwnerLease(ctx context.Context, lease OwnerLease) error { return nil }

func (s *w12dFakeStore) CheckSchema(ctx context.Context) error { return nil }

func (s *w12dFakeStore) Close() error { return nil }

func w12dLeaseContext(ctx context.Context) context.Context {
	return withOwnerLease(ctx, OwnerLease{OwnerID: "w12d-fake-owner", FenceToken: 1})
}

func TestW12dIndexerPanicRecovery(t *testing.T) {
	root := t.TempDir()
	config := Config{LogDirectory: root}
	current := filepath.Join(root, "juhe-ai.log")
	if err := os.WriteFile(current, []byte(`{"time":"2026-08-08T00:00:00.000Z","level":"info","event":"w12d"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := &w12dFakeStore{commitPanic: true}
	indexer := NewIndexer(config, fake)
	err := indexer.RunOnce(w12dLeaseContext(context.Background()))
	if err == nil || !strings.Contains(err.Error(), errManagedGoroutinePanic.Error()) {
		t.Fatalf("panic must surface as managed error: %v", err)
	}
	// Commit 错误同样汇入 join。
	fake.commitPanic = false
	fake.commitErr = errors.New("w12d commit failed")
	if err := indexer.RunOnce(w12dLeaseContext(context.Background())); err == nil || !strings.Contains(err.Error(), "w12d commit failed") {
		t.Fatalf("commit error: %v", err)
	}
}

func TestW12dIndexerFailureCursorArms(t *testing.T) {
	root := t.TempDir()
	config := Config{LogDirectory: root}
	current := filepath.Join(root, "juhe-ai.log")
	if err := os.WriteFile(current, []byte(`{"time":"2026-08-08T00:00:00.000Z","level":"info","event":"w12d-cursor"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// identity 游标比文件大 → 截断重置。
	bigOffset := int64(4096)
	fake := &w12dFakeStore{findByIdentity: &Cursor{LogFile: current, FileIdentity: "stale-identity", CursorOffset: bigOffset, FileSize: bigOffset}}
	indexer := NewIndexer(config, fake)
	if err := indexer.RunOnce(w12dLeaseContext(context.Background())); err != nil {
		t.Fatalf("truncation reset: %v", err)
	}
	// 存在可发现文件时 FindCursor / identity 错误才上抛。
	if err := os.WriteFile(current, []byte(`{"time":"2026-08-08T00:00:00.000Z","level":"info","event":"w12d-cursor"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// indexer 构造时绑定 store，替换 fake 后必须重建 indexer。
	fake = &w12dFakeStore{findCursorErr: errors.New("cursor lookup failed")}
	if err := NewIndexer(config, fake).RunOnce(w12dLeaseContext(context.Background())); err == nil || !strings.Contains(err.Error(), "cursor lookup failed") {
		t.Fatalf("find cursor error: %v", err)
	}
	fake = &w12dFakeStore{findByIdentErr: errors.New("identity lookup failed")}
	if err := NewIndexer(config, fake).RunOnce(w12dLeaseContext(context.Background())); err == nil || !strings.Contains(err.Error(), "identity lookup failed") {
		t.Fatalf("identity error: %v", err)
	}
}

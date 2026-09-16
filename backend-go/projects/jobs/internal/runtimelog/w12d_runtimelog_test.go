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
	findCursor      *Cursor
	findCursorErr   error
	findByIdentity  *Cursor
	findByIdentErr  error
	commitPanic     bool
	commitErr       error
	retentionDays   int
	retentionErr    error
	cleanupErr      error
	cleanupResult   CleanupResult
	replaceErr      error
	copyErr         error
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

func w12dFakeConfig(t *testing.T) Config {
	t.Helper()
	return Config{
		Mode:              ModeSQLite,
		RuntimeLogDatabasePath: filepath.Join(t.TempDir(), "w12d-rl.sqlite3"),
		BusinessPath:      filepath.Join(t.TempDir(), "w12d-business.sqlite3"),
		LogDirectory:      t.TempDir(),
		PollInterval:      time.Hour,
		RetentionInterval: time.Hour,
		RetentionDays:     7,
		BatchSize:         16,
	}
}

func w12dLeaseContext(ctx context.Context) context.Context {
	return withOwnerLease(ctx, OwnerLease{OwnerID: "w12d-fake-owner", FenceToken: 1})
}

// TestW12dOpenStoreParameterArms 覆盖 OpenStore 的参数校验与拒绝分支。
func TestW12dOpenStoreParameterArms(t *testing.T) {
	ctx := context.Background()
	if _, err := OpenStore(ctx, Config{Mode: ModeSQLite}); err == nil || !strings.Contains(err.Error(), "缺少运行日志专用数据库路径") {
		t.Fatalf("missing sqlite path: %v", err)
	}
	if _, err := OpenStore(ctx, Config{Mode: Mode("redis")}); err == nil || !strings.Contains(err.Error(), "不支持的运行日志 Store 模式") {
		t.Fatalf("bad mode: %v", err)
	}
	// sqlite 路径指向目录 → 打开后 PRAGMA 失败。
	if _, err := OpenStore(ctx, Config{Mode: ModeSQLite, RuntimeLogDatabasePath: t.TempDir()}); err == nil {
		t.Fatal("directory path must fail")
	}
	// business 只读句柄缺失。
	config := w12dFakeConfig(t)
	config.BusinessPath = filepath.Join(config.LogDirectory, "absent-business.sqlite3")
	if _, err := OpenStore(ctx, config); err == nil {
		t.Fatal("missing business db must fail")
	}
}

// TestW12dClosedSQLiteStoreArms 用关闭后的 store 覆盖各方法错误上抛。
func TestW12dClosedSQLiteStoreArms(t *testing.T) {
	store, _ := openTestSQLiteStore(t)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	lease := OwnerLease{OwnerID: "w12d-closed", FenceToken: 1}
	if _, err := store.FindCursor(ctx, "juhe-ai.log"); err == nil {
		t.Fatal("closed FindCursor must fail")
	}
	if _, err := store.FindCursorByIdentity(ctx, "identity"); err == nil {
		t.Fatal("closed FindCursorByIdentity must fail")
	}
	if err := store.ReplaceCursor(ctx, lease, nil, Cursor{}); err == nil {
		t.Fatal("closed ReplaceCursor must fail")
	}
	if err := store.CopyCursor(ctx, lease, Cursor{}); err == nil {
		t.Fatal("closed CopyCursor must fail")
	}
	if err := store.Commit(ctx, lease, nil, Cursor{}, time.Now()); err == nil {
		t.Fatal("closed Commit must fail")
	}
	if _, err := store.Cleanup(ctx, lease, time.Now(), 10, 10); err == nil {
		t.Fatal("closed Cleanup must fail")
	}
	if err := store.VerifyOwnerLease(ctx, lease); err == nil {
		t.Fatal("closed VerifyOwnerLease must fail")
	}
	if err := store.WithOwnerLeaseFence(ctx, lease, func() error { return nil }); err == nil {
		t.Fatal("closed fence must fail")
	}
	if _, err := store.RuntimeRetentionDays(ctx, 7); err == nil {
		t.Fatal("closed retention days must fail")
	}
	if _, _, err := store.AcquireOwnerLease(ctx, "w12d-closed", time.Minute); err == nil {
		t.Fatal("closed acquire must fail")
	}
	if _, err := store.RenewOwnerLease(ctx, lease, time.Minute); err == nil {
		t.Fatal("closed renew must fail")
	}
	if err := store.ReleaseOwnerLease(ctx, lease); err == nil {
		t.Fatal("closed release must fail")
	}
	if err := store.CheckSchema(ctx); err == nil {
		t.Fatal("closed CheckSchema must fail")
	}
	_ = store.Close()
}

// TestW12dIndexerPanicRecovery 覆盖 RunOnce 的 managed panic 恢复臂。
func TestW12dIndexerPanicRecovery(t *testing.T) {
	root := t.TempDir()
	config := w12dFakeConfig(t)
	config.LogDirectory = root
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

// TestW12dIndexerRetentionAndConfigArms 覆盖 RunRetention 与配置读取错误臂。
func TestW12dIndexerRetentionAndConfigArms(t *testing.T) {
	ctx := w12dLeaseContext(context.Background())
	// retention 配置读取错误。
	fake := &w12dFakeStore{retentionErr: errors.New("retention config failed")}
	if err := NewIndexer(w12dFakeConfig(t), fake).RunOnce(ctx); err == nil {
		t.Fatal("RunOnce must surface retention config error")
	}
	if err := NewIndexer(w12dFakeConfig(t), fake).RunRetention(ctx); err == nil || !strings.Contains(err.Error(), "retention config failed") {
		t.Fatalf("RunRetention config error: %v", err)
	}
	// Cleanup 错误。
	fake = &w12dFakeStore{cleanupErr: errors.New("cleanup failed")}
	if err := NewIndexer(w12dFakeConfig(t), fake).RunRetention(ctx); err == nil || !strings.Contains(err.Error(), "cleanup failed") {
		t.Fatalf("cleanup error: %v", err)
	}
	// 轮转清理：LogDirectory 缺失 → cleanupRotatedFiles 错误。
	config := w12dFakeConfig(t)
	config.LogDirectory = filepath.Join(config.LogDirectory, "absent")
	fake = &w12dFakeStore{}
	if err := NewIndexer(config, fake).RunRetention(ctx); err == nil {
		t.Fatal("absent log directory must fail")
	}
	// 正常保留期清理。
	okStore, config := openTestSQLiteStore(t)
	current := filepath.Join(config.LogDirectory, "juhe-ai.log")
	if err := os.WriteFile(current, []byte(`{"time":"2026-08-08T00:00:00.000Z","level":"info","event":"w12d-retention"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	indexer := NewIndexer(config, okStore)
	runCtx := testOwnerContext(t, okStore)
	if err := indexer.RunOnce(runCtx); err != nil {
		t.Fatal(err)
	}
	if err := indexer.RunRetention(runCtx); err != nil {
		t.Fatalf("retention: %v", err)
	}
}

// TestW12dIndexerFailureCursorArms 覆盖失败游标持久化与截断重置分支。
func TestW12dIndexerFailureCursorArms(t *testing.T) {
	root := t.TempDir()
	config := w12dFakeConfig(t)
	config.LogDirectory = root
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

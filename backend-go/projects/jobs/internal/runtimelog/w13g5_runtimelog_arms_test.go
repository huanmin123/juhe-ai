package runtimelog

// w13g5_runtimelog_arms_test.go 加固索引器游标提交与轮转清理的错误臂，
// 同时保护包级语句覆盖率余量（当前约 95.1%，由 w14i 系列测试与本文件
// 共同支撑）。
//
// 不可达清单（覆盖率登记）：
//   - identity_windows.go identity_windows.go 的 Windows SID 查询失败分支：
//     依赖进程令牌异常，测试环境不可控。

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestW13g5IndexerCursorLeaseArms(t *testing.T) {
	// 无 owner lease 的上下文 → commit/copyCursor/replaceCursor 直接报错。
	store := openW13g5SQLiteStore(t)
	indexer := NewIndexer(Config{PollInterval: time.Minute, RetentionInterval: time.Hour, RetentionDays: 1}, store)
	cursor := Cursor{LogFile: "w13g5.log", CursorOffset: 10}
	if err := indexer.commit(context.Background(), nil, cursor); err == nil {
		t.Fatal("缺少 owner lease 必须报错")
	}
	if err := indexer.copyCursor(context.Background(), cursor); err == nil {
		t.Fatal("缺少 owner lease 必须报错")
	}
	displaced := cursor
	if err := indexer.replaceCursor(context.Background(), &displaced, cursor); err == nil {
		t.Fatal("缺少 owner lease 必须报错")
	}
}

func TestW13g5RemoveRotatedLogFileMissingPathArm(t *testing.T) {
	store := openW13g5SQLiteStore(t)
	lease, acquired, err := store.AcquireOwnerLease(context.Background(), "w13g5-owner", time.Hour)
	if err != nil || !acquired {
		t.Fatalf("租约必须获取: %v %v", acquired, err)
	}
	ctx := withOwnerLease(context.Background(), lease)
	// 不存在的轮转文件 → os.ErrNotExist 收敛为 nil（幂等删除）。
	missing := filepath.Join(t.TempDir(), "w13g5-missing.log")
	if err := removeRotatedLogFile(ctx, store, lease, missing); err != nil {
		t.Fatalf("缺失文件必须幂等: %v", err)
	}
	// ctx 已取消 → 直接传播取消错误。
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := removeRotatedLogFile(cancelled, store, lease, missing); err == nil {
		t.Fatal("取消的上下文必须报错")
	}
}

// openW13g5SQLiteStore 打开一个隔离 SQLite store。
func openW13g5SQLiteStore(t *testing.T) Store {
	t.Helper()
	businessPath := filepath.Join(t.TempDir(), "w13g5-business.sqlite")
	business, err := sql.Open("sqlite", businessPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := business.Exec("CREATE TABLE IF NOT EXISTS marker (id INTEGER PRIMARY KEY)"); err != nil {
		business.Close()
		t.Fatal(err)
	}
	if err := business.Close(); err != nil {
		t.Fatal(err)
	}
	config := Config{
		Mode:                   ModeSQLite,
		DatasetPath:            filepath.Join(t.TempDir(), "w13g5-dataset.sqlite"),
		RuntimeLogDatabasePath: filepath.Join(t.TempDir(), "w13g5-runtime-log.sqlite"),
		BusinessPath:           businessPath,
		LogDirectory:           t.TempDir(),
		PollInterval:           time.Second,
		RetentionInterval:      time.Hour,
		RetentionDays:          1,
	}
	store, err := OpenStore(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := EnsureSchema(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	return store
}


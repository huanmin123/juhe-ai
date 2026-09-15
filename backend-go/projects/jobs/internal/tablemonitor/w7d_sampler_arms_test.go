package tablemonitor

// w7d（tablemonitor 采样器错误臂批次四）：collectSQLiteTarget 损坏文件/目录
// 输入、RunOnce 租约缺失/零时刻/增长基线错误连接臂。进程内确定性。

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestW7DCollectSQLiteTargetRejectsCorruptAndNonRegularInput(t *testing.T) {
	// 损坏的 SQLite 文件：打开成功但 PRAGMA page_size 失败。
	corrupt := filepath.Join(t.TempDir(), "corrupt.sqlite3")
	if err := os.WriteFile(corrupt, []byte("this is not a sqlite database —— garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := collectSQLiteTarget(context.Background(), sqliteTarget{role: "corrupt", path: corrupt}, time.Now(), 10)
	if err == nil || (!strings.Contains(err.Error(), "page_size 失败") && !strings.Contains(err.Error(), "page_count 失败")) {
		t.Fatalf("损坏文件必须在 PRAGMA 指标处失败: %v", err)
	}

	// 目录：openSQLiteReadOnly 拒绝非常规文件。
	_, err = collectSQLiteTarget(context.Background(), sqliteTarget{role: "dir", path: t.TempDir()}, time.Now(), 10)
	if err == nil || !strings.Contains(err.Error(), "不是常规 SQLite 文件") {
		t.Fatalf("目录必须按非常规文件拒绝: %v", err)
	}

	// 缺失文件：Stat 失败。
	_, err = collectSQLiteTarget(context.Background(), sqliteTarget{role: "missing", path: filepath.Join(t.TempDir(), "none.sqlite")}, time.Now(), 10)
	if err == nil {
		t.Fatal("缺失文件必须报错")
	}
}

func TestW7DRunOnceGuardsAndGrowthFailureJoin(t *testing.T) {
	cfg, store := w7dSQLiteFixture(t)

	// 租约缺失。
	if _, err := RunOnce(context.Background(), cfg, store, time.Now().UTC()); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("缺租约必须拒绝: %v", err)
	}

	// 零时刻回落 time.Now（先确保 schema 存在，采样/写入才能走通）。
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	lease, acquired, err := store.AcquireOwnerLease(context.Background(), cfg.InstanceID, cfg.OwnerLease)
	if err != nil || !acquired {
		t.Fatalf("获取租约: acquired=%t err=%v", acquired, err)
	}
	ctx := context.WithValue(context.Background(), ownerLeaseContextKey{}, lease)
	if _, err := RunOnce(ctx, cfg, store, time.Time{}); err != nil {
		t.Fatalf("零时刻必须回落当前时间: %v", err)
	}

	// store 底层库关闭：采样成功但增长基线读取失败 → 错误 join 暴露。
	closed, err := OpenStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.db.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = RunOnce(ctx, cfg, closed, time.Now().UTC())
	if err == nil || !strings.Contains(err.Error(), "读取表监控历史增长基线失败") {
		t.Fatalf("增长基线读取失败必须 join 暴露: %v", err)
	}
}

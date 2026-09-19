package auditlog

// w16g 覆盖收尾第三批：只补纯函数与错误臂的最后缺口，不触碰生产代码。
// 覆盖目标（profile 实测未覆盖块）：
//   - owner.go RunOwner 的租约静默丢失臂（ErrOwnerLeaseLost 透传）
//   - config.go LoadConfig 的 SameFile / PathWithin 错误臂（不可 Stat 路径、
//     不存在的盘符根目录）
//   - retention.go blobFileMatchesMetadata 的 Stat 错误臂
//   - hot_search.go SearchHotSearch 的 scanHotSearchFile 读取错误臂
//   - retention.go scheduleUnreferencedBlobGC 的多行排序闭包
// 所有错误臂通过 os 层确定性失败（NUL 字节路径返回 EINVAL、缺失盘符）
// 触发，不依赖权限位或竞态。

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestW16gRunOwnerReturnsLeaseLostWithoutCause(t *testing.T) {
	// lostCh 已关闭但 LostError 为 nil：RunOwner 必须自行回退到哨兵错误。
	keeper := &LeaseKeeper{lostCh: make(chan struct{})}
	close(keeper.lostCh)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- RunOwner(ctx, &w16aOwnerFakeStore{}, keeper, w16aOwnerConfig(), slog.Default()) }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrOwnerLeaseLost) {
			t.Fatalf("静默租约丢失必须返回 ErrOwnerLeaseLost: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("RunOwner 未在租约丢失后退出")
	}
}

func TestW16gLoadConfigRejectsUnstatableAuditDatabasePath(t *testing.T) {
	root := t.TempDir()
	env := sqliteEnv(root)
	env["JUHE_AI_AUDIT_LOG_DATABASE_PATH"] = filepath.Join(root, "audit\x00bad.sqlite3")
	_, err := LoadConfig(func(name string) string { return env[name] })
	if err == nil || !strings.Contains(err.Error(), "物理隔离失败") {
		t.Fatalf("不可 Stat 的 F3 专库路径必须报物理隔离校验失败: %v", err)
	}
}

func TestW16gLoadConfigRejectsUnstatableBusinessSettingsPath(t *testing.T) {
	root := t.TempDir()
	env := sqliteEnv(root)
	env["JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_PATH"] = filepath.Join(root, "settings\x00bad.sqlite3")
	_, err := LoadConfig(func(name string) string { return env[name] })
	if err == nil || !strings.Contains(err.Error(), "业务设置") {
		t.Fatalf("不可 Stat 的业务设置只读路径必须报隔离校验失败: %v", err)
	}
}

// w16gMissingDriveRoot 探测一个本机不存在的盘符并返回其下的 shard 根目录。
// CanonicalPath 向上找不到任何存在的物理父目录时会报错，从而命中
// PathWithin 的错误臂；找不到缺失盘符时跳过。
func w16gMissingDriveRoot(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("缺失盘符探测仅适用于 Windows")
	}
	for _, letter := range []string{"Q", "Z", "Y", "X", "W", "V", "U", "T", "S", "R", "P", "O", "N", "M", "L"} {
		if _, err := os.Lstat(letter + `:\`); err != nil {
			return letter + `:\juhe-ai-missing-shard-root`
		}
	}
	t.Skip("未找到不存在的盘符")
	return ""
}

func TestW16gLoadConfigRejectsUnresolvableShardRoots(t *testing.T) {
	missing := w16gMissingDriveRoot(t)

	// Codex shard 根目录无法解析为物理路径 → PathWithin 错误臂。
	root := t.TempDir()
	env := sqliteEnv(root)
	env["JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT"] = missing
	if _, err := LoadConfig(func(name string) string { return env[name] }); err == nil || !strings.Contains(err.Error(), "Codex shard 根目录隔离失败") {
		t.Fatalf("不可解析的 Codex shard 根目录必须报隔离校验失败: %v", err)
	}

	// usage shard 根目录无法解析为物理路径 → PathWithin 错误臂。
	env = sqliteEnv(root)
	env["JUHE_AI_USAGE_SHARD_ROOT"] = missing
	if _, err := LoadConfig(func(name string) string { return env[name] }); err == nil || !strings.Contains(err.Error(), "usage shard 根目录隔离失败") {
		t.Fatalf("不可解析的 usage shard 根目录必须报隔离校验失败: %v", err)
	}
}

func TestW16gBlobFileMatchesMetadataRejectsUnstatableKey(t *testing.T) {
	if _, err := blobFileMatchesMetadata(t.TempDir(), "payload\x00bad.bin", 1); err == nil || !strings.Contains(err.Error(), "检查 F3 audit blob 文件失败") {
		t.Fatalf("不可 Stat 的 storage_key 必须报检查失败: %v", err)
	}
}

func TestW16gSearchHotSearchFailsOnOversizedLine(t *testing.T) {
	store := &sqlStore{hotDir: t.TempDir()}
	name := filepath.Join(store.hotDir, hotSearchFileName(time.Now()))
	oversized := strings.Repeat("a", maxHotSearchLineBytes+1)
	if err := os.WriteFile(name, []byte(oversized), 0o640); err != nil {
		t.Fatal(err)
	}
	_, err := store.SearchHotSearch(context.Background(), HotSearchOptions{Keywords: []string{"ab"}})
	if err == nil || !strings.Contains(err.Error(), "读取 F3 hot-search 文件失败") {
		t.Fatalf("超长 hot-search 行必须报读取失败: %v", err)
	}
}

func TestW16gRetentionSortsMultipleUnreferencedBlobs(t *testing.T) {
	ctx := context.Background()
	store, lease := w14hStore(t)
	// 两个不同 body → 两条独立 blob 行，retention 调度 GC 时必须按 id 排序。
	if _, err := store.Persist(ctx, lease, w14hPayloadInput("w16g-gc-1", `{"n":1}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Persist(ctx, lease, w14hPayloadInput("w16g-gc-2", `{"n":2}`)); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	result, err := store.CleanupRetention(ctx, lease, RetentionConfig{
		SuccessHotCutoff: cutoff,
		SuccessCutoff:    cutoff,
		FailureCutoff:    cutoff,
		ErrorGroupCutoff: cutoff,
		BatchSize:        100,
	})
	if err != nil {
		t.Fatalf("retention=%v", err)
	}
	if result.DeletedLogs != 2 {
		t.Fatalf("删除日志数=%d", result.DeletedLogs)
	}
}

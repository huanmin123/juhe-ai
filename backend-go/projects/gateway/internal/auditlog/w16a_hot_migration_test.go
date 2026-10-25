package auditlog

// w16a 覆盖收尾第四批：hot-search 扫描/列举/清理边界臂与 legacy SQLite
// 迁移的错误臂。hot-search 用真实临时目录（含 4098 项截断夹具）；迁移用
// 损坏文件、目录形态目标库、脚本库直调私有校验函数，均不触碰共享数据。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func w16aWriteHotFile(t *testing.T, store *sqlStore, ids []string, createdAt time.Time, text string) string {
	t.Helper()
	lines := make([]hotSearchLine, 0, len(ids))
	for _, id := range ids {
		lines = append(lines, hotSearchLine{AuditLogID: id, CreatedAt: createdAt.UTC().Format(time.RFC3339Nano), Text: text})
	}
	path := filepath.Join(store.hotDir, hotSearchFileName(createdAt))
	if err := appendHotSearchFile(path, lines); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestW16aSearchHotSearchBoundaryArms(t *testing.T) {
	ctx := context.Background()
	store := &sqlStore{hotDir: t.TempDir()}
	now := time.Now().UTC().Add(-time.Hour).Truncate(time.Hour)
	w16aWriteHotFile(t, store, []string{"w16a-s-1", "w16a-s-2", "w16a-s-3"}, now, "w16a-needle content")

	// StartAt 缺省 → end-24h；Limit 超上限 → 收敛到 maxHotSearchLimit。
	result, err := store.SearchHotSearch(ctx, HotSearchOptions{Keywords: []string{"needle"}, EndAt: now.Add(2 * time.Hour), Limit: 100000})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.AuditLogIDs) != 3 || result.ScannedFiles != 1 {
		t.Fatalf("search result=%+v", result)
	}

	// 命中数超过 limit → 截断标记；不同时间戳覆盖排序比较分支。
	extra := filepath.Join(store.hotDir, hotSearchFileName(now))
	if err := appendHotSearchFile(extra, []hotSearchLine{
		{AuditLogID: "w16a-s-4", CreatedAt: now.Add(time.Hour).UTC().Format(time.RFC3339Nano), Text: "w16a-needle later"},
	}); err != nil {
		t.Fatal(err)
	}
	truncated, err := store.SearchHotSearch(ctx, HotSearchOptions{Keywords: []string{"needle"}, StartAt: now.Add(-time.Minute), EndAt: now.Add(2 * time.Hour), Limit: 2})
	if err != nil || !truncated.Truncated || len(truncated.AuditLogIDs) != 2 {
		t.Fatalf("limit 截断必须生效: %+v %v", truncated, err)
	}

	// MaxLines 预算耗尽 → 扫描中断并标记截断。
	budgeted, err := store.SearchHotSearch(ctx, HotSearchOptions{Keywords: []string{"needle"}, StartAt: now.Add(-time.Minute), EndAt: now.Add(time.Hour), MaxLines: 2})
	if err != nil || !budgeted.Truncated {
		t.Fatalf("MaxLines 截断必须生效: %+v %v", budgeted, err)
	}
}

func TestW16aHotDirectoryReadFailures(t *testing.T) {
	ctx := context.Background()
	store, lease := w14hPostgresStore(t)
	broken := filepath.Join(t.TempDir(), "w16a-hot|illegal")
	store.hotDir = broken
	if _, _, err := store.listHotSearchFiles(time.Now().UTC().Add(-24*time.Hour), time.Now().UTC(), 8); err == nil {
		t.Fatal("非法 hot 目录必须使列举失败")
	}
	if _, err := store.cleanupHotSearchFilesBefore(time.Now().UTC().Add(time.Hour), 8); err == nil {
		t.Fatal("非法 hot 目录必须使清理失败")
	}
	if _, err := store.SearchHotSearch(ctx, HotSearchOptions{Keywords: []string{"needle"}}); err == nil {
		t.Fatal("非法 hot 目录必须使搜索失败")
	}
	if _, err := store.CleanupHotSearch(ctx, lease, time.Now().UTC().Add(time.Hour), 8); err == nil {
		t.Fatal("非法 hot 目录必须使清理入口失败")
	}
}

func TestW16aHotDirectoryTruncationArms(t *testing.T) {
	root := t.TempDir()
	store := &sqlStore{hotDir: root}
	now := time.Now().UTC()
	// 49 个窗口内的小时桶文件 + 3790 个无关文件 + 8 个目录 > 4096 项，
	// 触发 entries 截断与窗口内候选截断。
	for i := 0; i < 49; i++ {
		bucket := now.Add(-time.Duration(60+i) * time.Hour).Truncate(time.Hour)
		path := filepath.Join(root, hotSearchFileName(bucket))
		if err := os.WriteFile(path, []byte("{\"auditLogId\":\"w16a-old\"}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 4045; i++ {
		name := filepath.Join(root, fmt.Sprintf("junk-000-%d", 100000+i))
		if err := os.WriteFile(name, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 8; i++ {
		if err := os.MkdirAll(filepath.Join(root, fmt.Sprintf("audit-hot-dir-%d.ndjson", i)), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	paths, truncated, err := store.listHotSearchFiles(now.Add(-100*time.Hour), now, 4)
	if err != nil || !truncated || len(paths) != 4 {
		t.Fatalf("超过 4096 项必须截断: paths=%d truncated=%t err=%v", len(paths), truncated, err)
	}
	deleted, err := store.cleanupHotSearchFilesBefore(now, 4)
	if err != nil || deleted != 4 {
		t.Fatalf("清理必须在 maxFiles 处停止: deleted=%d err=%v", deleted, err)
	}
}

func TestW16aCleanupHotSearchFilesHonorsMaxFiles(t *testing.T) {
	root := t.TempDir()
	store := &sqlStore{hotDir: root}
	old := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Hour)
	for _, hour := range []int{0, 1} {
		path := filepath.Join(root, hotSearchFileName(old.Add(time.Duration(hour)*time.Hour)))
		if err := appendHotSearchFile(path, []hotSearchLine{{AuditLogID: "w16a-old", CreatedAt: old.Format(time.RFC3339Nano), Text: "w16a"}}); err != nil {
			t.Fatal(err)
		}
	}
	deleted, err := store.cleanupHotSearchFilesBefore(old.Add(3*time.Hour), 1)
	if err != nil || deleted != 1 {
		t.Fatalf("maxFiles=1 必须只删除一个文件: deleted=%d err=%v", deleted, err)
	}
}

func TestW16aScanHotSearchFileArms(t *testing.T) {
	dir := t.TempDir()
	keywords := []string{"needle"}
	start := time.Now().UTC().Add(-24 * time.Hour)
	end := time.Now().UTC()
	seen := map[string]time.Time{}

	// 非法 JSON 行必须跳过，合法行继续命中。
	badPath := filepath.Join(dir, "audit-hot-bad.ndjson")
	if err := appendHotSearchFile(badPath, []hotSearchLine{{AuditLogID: "w16a-ok", CreatedAt: end.Format(time.RFC3339Nano), Text: "w16a-needle"}}); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(badPath, os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("{\"broken\": \n"); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	count, err := scanHotSearchFile(context.Background(), badPath, keywords, start, end, 100, seen)
	if err != nil || count != 2 || len(seen) != 1 {
		t.Fatalf("非法 JSON 行必须跳过: count=%d seen=%d err=%v", count, len(seen), err)
	}

	// 超长行触发 scanner 错误臂。
	longPath := filepath.Join(dir, "audit-hot-long.ndjson")
	file, err = os.OpenFile(longPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(strings.Repeat("w16a-needle", 30000) + "\n"); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	if _, err := scanHotSearchFile(context.Background(), longPath, keywords, start, end, 100, seen); err == nil {
		t.Fatal("超长行必须触发读取错误")
	}

	// 已取消上下文必须在扫描循环内返回 ctx 错误。
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := scanHotSearchFile(canceled, badPath, keywords, start, end, 100, seen); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消上下文必须返回 ctx.Err(): %v", err)
	}
}

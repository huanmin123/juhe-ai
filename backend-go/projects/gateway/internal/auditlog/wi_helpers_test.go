package auditlog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWICompressAuditBlobMatrix(t *testing.T) {
	small := []byte("tiny")
	// 小负载不压缩。
	got, encoding, err := compressAuditBlob(small, "application/json", "")
	if err != nil || encoding != "none" || !bytes.Equal(got, small) {
		t.Fatalf("small=%d %q err=%v", len(got), encoding, err)
	}
	// 大于阈值但非可压缩类型 → none。
	big := make([]byte, auditBlobCompressionThresholdBytes+10)
	if _, encoding, _ := compressAuditBlob(big, "application/octet-stream", ""); encoding != "none" {
		t.Fatalf("二进制类型=%q", encoding)
	}
	// 已有非 identity 编码 → none。
	if _, encoding, _ := compressAuditBlob(big, "application/json", "gzip"); encoding != "none" {
		t.Fatalf("已压缩=%q", encoding)
	}
	// 可压缩 JSON → gzip。
	compressed, encoding, err := compressAuditBlob(bytes.Repeat([]byte(`{"k":"v"}`), 500), "application/json", "")
	if err != nil || encoding != "gzip" || len(compressed) == 0 {
		t.Fatalf("gzip=%d %q err=%v", len(compressed), encoding, err)
	}
	// isCompressibleAuditPayload 矩阵。
	if !isCompressibleAuditPayload("text/plain", "") || isCompressibleAuditPayload("application/json", "br") {
		t.Fatal("isCompressibleAuditPayload 错误")
	}
	if isCompressibleAuditPayload("", "") {
		t.Fatal("空类型不可压缩")
	}
}

func TestWISyncBlobParentAndCleanupTemps(t *testing.T) {
	// Windows 恒 no-op；Unix 分支打开目录 fsync。
	if err := syncBlobParent(t.TempDir()); err != nil {
		t.Fatalf("syncBlobParent=%v", err)
	}
	// cleanupBlobTemps：带 tempPath 的清理 + 无 tempPath 跳过 + 不存在文件忽略。
	temp := filepath.Join(t.TempDir(), "blob.tmp")
	if err := os.WriteFile(temp, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	plans := []blobPlan{{tempPath: temp}, {tempPath: ""}, {tempPath: filepath.Join(t.TempDir(), "missing")}}
	if err := cleanupBlobTemps(plans); err != nil {
		t.Fatalf("cleanup=%v", err)
	}
	if _, err := os.Stat(temp); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("临时文件必须删除: %v", err)
	}
}

func TestWIBuildHotSearchLinesFilters(t *testing.T) {
	root := t.TempDir()
	// 空输入。
	lines, err := buildHotSearchLines(root, nil)
	if err != nil || len(lines) != 0 {
		t.Fatalf("空输入=%v err=%v", lines, err)
	}
	// 非持久化来源被过滤。
	probed := fixture("wi-hot-probe", LifecycleFinalized)
	probed.TrafficSource = TrafficSourceAccountHealthCheck
	lines, err = buildHotSearchLines(root, []AuditLogInput{probed})
	if err != nil || len(lines) != 0 {
		t.Fatalf("探针输入=%v err=%v", lines, err)
	}
	// 空 ID 被过滤。
	blank := fixture(" ", LifecycleFinalized)
	lines, err = buildHotSearchLines(root, []AuditLogInput{blank})
	if err != nil || len(lines) != 0 {
		t.Fatalf("空 ID=%v err=%v", lines, err)
	}
	// createdAt 非法必须报错。
	badTime := fixture("wi-hot-badtime", LifecycleFinalized)
	badTime.CreatedAt = "not-a-time"
	badTime.EndedAt = "  "
	if _, err := buildHotSearchLines(root, []AuditLogInput{badTime}); err == nil {
		t.Fatal("坏 createdAt 必须报错")
	}
	// 正常输入落到按小时命名的桶。
	good := fixture("wi-hot-good", LifecycleFinalized)
	lines, err = buildHotSearchLines(root, []AuditLogInput{good})
	if err != nil || len(lines) == 0 {
		t.Fatalf("正常输入=%v err=%v", lines, err)
	}
	for path := range lines {
		if _, ok := parseHotSearchBucket(filepath.Base(path)); !ok {
			t.Fatalf("桶名非法: %s", path)
		}
	}
}

func TestWIAppendHotSearchFileErrorPaths(t *testing.T) {
	root := t.TempDir()
	// 把目标目录做成文件 → MkdirAll 失败。
	blocker := filepath.Join(root, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := appendHotSearchFile(filepath.Join(blocker, "bucket.ndjson"), []hotSearchLine{{AuditLogID: "a"}}); err == nil {
		t.Fatal("目录创建失败必须报错")
	}
	// 目录存在但路径是目录本身 → 打开失败。
	if err := appendHotSearchFile(root, []hotSearchLine{{AuditLogID: "a"}}); err == nil {
		t.Fatal("打开目录作为文件必须报错")
	}
	// 正常写入 + 追加语义。
	path := filepath.Join(root, hotSearchFileName(time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)))
	if err := appendHotSearchFile(path, []hotSearchLine{{AuditLogID: "a"}}); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if err := appendHotSearchFile(path, []hotSearchLine{{AuditLogID: "b"}}); err != nil {
		t.Fatalf("追加失败: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(strings.Split(strings.TrimSpace(string(raw)), "\n")) != 2 {
		t.Fatalf("追加结果=%s err=%v", raw, err)
	}
}

func TestWIListHotSearchFilesWindowAndTruncation(t *testing.T) {
	store := openSQLiteStore(t, sqliteConfig(t, t.TempDir()))
	defer store.Close()
	implementation := store.(*sqlStore)
	// 目录不存在 → 空 + 未截断。
	paths, truncated, err := implementation.listHotSearchFiles(time.Now().UTC(), time.Now().UTC(), 10)
	if err != nil || len(paths) != 0 || truncated {
		t.Fatalf("空目录=%v %v err=%v", paths, truncated, err)
	}
	if err := os.MkdirAll(implementation.hotDir, 0o750); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	inWindow := filepath.Join(implementation.hotDir, hotSearchFileName(base))
	outWindow := filepath.Join(implementation.hotDir, hotSearchFileName(base.Add(-48*time.Hour)))
	ignored := filepath.Join(implementation.hotDir, "ignore-me.ndjson")
	for _, path := range []string{inWindow, outWindow, ignored} {
		if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	start := base
	end := base.Add(2 * time.Hour)
	paths, truncated, err = implementation.listHotSearchFiles(start, end, 10)
	if err != nil || truncated {
		t.Fatalf("窗口=%v err=%v", paths, err)
	}
	if len(paths) != 1 || paths[0] != inWindow {
		t.Fatalf("窗口过滤=%v", paths)
	}
	// maxFiles 截断：窗口内放两个桶，只取最新的一个。
	secondInWindow := filepath.Join(implementation.hotDir, hotSearchFileName(base.Add(time.Hour)))
	if err := os.WriteFile(secondInWindow, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	paths, truncated, err = implementation.listHotSearchFiles(start, end, 1)
	if err != nil || !truncated || len(paths) != 1 || paths[0] != secondInWindow {
		t.Fatalf("截断=%v truncated=%v err=%v", paths, truncated, err)
	}
	// cleanupHotSearchFilesBefore：目录不存在安全返回 0。
	emptyStore := openSQLiteStore(t, sqliteConfig(t, t.TempDir()))
	defer emptyStore.Close()
	if deleted, err := emptyStore.(*sqlStore).cleanupHotSearchFilesBefore(time.Now().UTC(), 5); err != nil || deleted != 0 {
		t.Fatalf("空目录清理=%d err=%v", deleted, err)
	}
}

func TestWINormalizeHotKeywords(t *testing.T) {
	// 去空白、长度下限、去重、上限。
	got := normalizeHotKeywords([]string{"  Error ", "error", "a", "", "Timeout", stringsRepeat2("长", maxHotSearchKeywordRunes+10)})
	if len(got) != 3 || got[0] != "error" || got[1] != "timeout" {
		t.Fatalf("keywords=%v", got)
	}
	if len([]rune(got[2])) != maxHotSearchKeywordRunes {
		t.Fatalf("超长关键词未截断: %d", len([]rune(got[2])))
	}
	// 关键词上限（maxHotSearchKeywords 个去重后）。
	many := make([]string, 0, maxHotSearchKeywords+5)
	for i := 0; i < maxHotSearchKeywords+5; i++ {
		many = append(many, fmt.Sprintf("kw-%02d-x", i))
	}
	if got := normalizeHotKeywords(many); len(got) != maxHotSearchKeywords {
		t.Fatalf("上限=%d", len(got))
	}
}

func TestWILegacyMigrationPathHelpers(t *testing.T) {
	// equalLegacyPath：清洗后比较。
	if !equalLegacyPath("/v1/x", "/v1/x") || equalLegacyPath("/v1/x", "/v1/y") {
		t.Fatal("equalLegacyPath 错误")
	}
	// 只读 / 目标 DSN 的构造。
	if !strings.Contains(readOnlySQLiteDSN("audit.sqlite3"), "mode=ro") {
		t.Fatalf("readOnlyDSN=%q", readOnlySQLiteDSN("audit.sqlite3"))
	}
	if !strings.Contains(targetSQLiteDSN("audit.sqlite3"), "audit.sqlite3") {
		t.Fatalf("targetDSN=%q", targetSQLiteDSN("audit.sqlite3"))
	}
	// verifyBlobFile：越界 / 绝对路径 storage_key 必须拒绝。
	root := t.TempDir()
	if _, err := verifyBlobFile(root, "../escape", "d", 1, 1, "none"); err == nil {
		t.Fatal("越界 storage_key 必须报错")
	}
	if _, err := verifyBlobFile(root, "missing.bin", "d", 1, 1, "none"); err == nil {
		t.Fatal("缺失 blob 文件必须报错")
	}
	// 大小不匹配。
	if err := os.WriteFile(filepath.Join(root, "blob.bin"), []byte("abcd"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyBlobFile(root, "blob.bin", "d", 4, 3, "none"); err == nil {
		t.Fatal("大小不匹配必须报错")
	}
	// 不支持的压缩格式。
	if _, err := verifyBlobFile(root, "blob.bin", "d", 4, 4, "br"); err == nil {
		t.Fatal("未知压缩必须报错")
	}
	// digest 不匹配。
	if _, err := verifyBlobFile(root, "blob.bin", strings.Repeat("0", 64), 4, 4, "none"); err == nil {
		t.Fatal("digest 不匹配必须报错")
	}
	// 完全匹配（含 gzip）往返。
	summary := fmt.Sprintf("%x", sha256.Sum256([]byte("abcd")))
	if data, err := verifyBlobFile(root, "blob.bin", summary, 4, 4, "none"); err != nil || string(data) != "abcd" {
		t.Fatalf("匹配 blob=%s err=%v", data, err)
	}
}

func TestWILeaseKeeperLostWhenLeaseUsurped(t *testing.T) {
	store := openSQLiteStore(t, sqliteConfig(t, t.TempDir()))
	defer store.Close()
	ctx := context.Background()
	keeper, ok, err := StartLeaseKeeper(ctx, store, "wi-usurped", 2*time.Second, slog.Default())
	if err != nil || !ok {
		t.Fatalf("启动 keeper=%v err=%v", ok, err)
	}
	// 手工把租约行置为过期，随后由另一个 owner 抢占（fence 递增）。
	// keeper 的自动续租会不断延长 lease_until，无法靠等待过期。
	implementation := store.(*sqlStore)
	past := dbTime(ModeSQLite, time.Now().UTC().Add(-time.Minute))
	if _, err := implementation.db.Exec(
		`UPDATE `+implementation.leaseTable()+` SET lease_until=? WHERE lease_key='f3-audit-log-persistence' AND owner_id='wi-usurped'`,
		past); err != nil {
		t.Fatalf("过期租约失败: %v", err)
	}
	_, acquired, err := store.AcquireOwnerLease(ctx, "raider", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("抢占=%v err=%v", acquired, err)
	}
	// 被抢者下一次续租 renewed=false → 终态 ErrOwnerLeaseLost。
	select {
	case <-keeper.Lost():
		if !errors.Is(keeper.LostError(), ErrOwnerLeaseLost) {
			t.Fatalf("LostError=%v", keeper.LostError())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("租约被抢占后 Lost 未关闭")
	}
	keeper.Close()
}

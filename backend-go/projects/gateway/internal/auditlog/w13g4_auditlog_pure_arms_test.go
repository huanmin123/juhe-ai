package auditlog

// w13g4 覆盖率补齐：hot-search 文件/纯函数臂、迁移 DSN/完整性校验臂。
//
// 不可达 / 高成本语句登记（基于 2026-09-18 覆盖率 profile w13g4_auditlog.out）：
//   - legacy_migration.go 217-221（readOnlySQLiteDSN/targetSQLiteDSN 的
//     sqliteDSN 失败回退）：sqliteDSN 仅在 filepath.Abs 失败时出错，常规路径
//     不触发。
//   - legacy_migration.go equalLegacyPath 的非 Windows 分支：当前测试平台为
//     windows，runtime.GOOS 编译期恒定，跨平台分支由构建矩阵覆盖。

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestW13G4LegacyMigrationHelpers 覆盖旧迁移的路径比较与只读 DSN 拼接。
func TestW13G4LegacyMigrationHelpers(t *testing.T) {
	// 当前平台（windows）走 EqualFold + Clean 分支。
	if !equalLegacyPath(`A/B`, `a\b`) {
		t.Fatal("EqualFold 路径应相等")
	}
	if equalLegacyPath(`a/b`, `a/c`) {
		t.Fatal("不同路径不应相等")
	}

	dsn := readOnlySQLiteDSN(filepath.Join(t.TempDir(), "legacy.sqlite3"))
	if !strings.Contains(dsn, "mode=ro") {
		t.Fatalf("只读 DSN = %q", dsn)
	}
	target := targetSQLiteDSN(filepath.Join(t.TempDir(), "target.sqlite3"))
	if !strings.Contains(target, "busy_timeout(5000)") {
		t.Fatalf("目标 DSN = %q", target)
	}
}

// TestW13G4VerifySQLiteIntegrityDB 覆盖完整性校验的成功与损坏文件失败臂。
func TestW13G4VerifySQLiteIntegrityDB(t *testing.T) {
	store := openSQLiteStore(t, sqliteConfig(t, t.TempDir()))
	defer store.Close()
	impl := store.(*sqlStore)
	if err := verifySQLiteIntegrityDB(context.Background(), impl.db, "w13g4-good"); err != nil {
		t.Fatalf("健康库校验 = %v", err)
	}

	corrupt := filepath.Join(t.TempDir(), "corrupt.sqlite3")
	if err := os.WriteFile(corrupt, []byte("not a sqlite database at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", corrupt)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := verifySQLiteIntegrityDB(context.Background(), db, "w13g4-corrupt"); err == nil {
		t.Fatal("损坏库应校验失败")
	}
}

// TestW13G4BuildHotSearchLines 覆盖热搜行构建的过滤、非法时间与分片。
func TestW13G4BuildHotSearchLines(t *testing.T) {
	root := t.TempDir()
	// 非法 createdAt → 错误（hot_search.go 211）。
	if _, err := buildHotSearchLines(root, []AuditLogInput{{ID: "w13g4-hs-bad", CreatedAt: "oops"}}); err == nil || !strings.Contains(err.Error(), "createdAt") {
		t.Fatalf("非法 createdAt = %v", err)
	}
	// 空 ID、非持久化来源、空文本均被跳过。
	inputs := []AuditLogInput{
		{ID: "", CreatedAt: "2026-03-10T08:00:00Z"},
		{ID: "w13g4-hs-probe", TrafficSource: "account_health_check", CreatedAt: "2026-03-10T08:00:00Z"},
		{ID: "w13g4-hs-empty", CreatedAt: "2026-03-10T08:00:00Z"},
	}
	lines, err := buildHotSearchLines(root, inputs)
	if err != nil || len(lines) != 0 {
		t.Fatalf("全过滤输入 = %+v / %v", lines, err)
	}
	// 超长文本触发分片。
	big := inputs[2]
	big.ID = "w13g4-hs-big"
	big.Path = strings.Repeat("p", 200*1024)
	big.Success = false
	big.ErrorCode = "boom"
	lines, err = buildHotSearchLines(root, []AuditLogInput{big})
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, chunkLines := range lines {
		total += len(chunkLines)
	}
	if total < 2 {
		t.Fatalf("长文本应分片 = %d 行", total)
	}
}

// TestW13G4ParseAndScanHotSearchBucket 覆盖桶名解析与文件扫描边界。
func TestW13G4ParseAndScanHotSearchBucket(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit-hot-2026010110.ndjson")
	if err := os.WriteFile(path, []byte(`{"auditLogId":"w13g4-hs-1","createdAt":"2026-03-10T08:00:00Z","text":"needle"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bucket, ok := parseHotSearchBucket("audit-hot-2026010110.ndjson")
	if !ok || bucket.IsZero() {
		t.Fatalf("合法桶名 = %+v / %+v", bucket, ok)
	}
	for _, name := range []string{"audit-hot.ndjson", "audit-hot-20260101.ndjson", "audit-hot-2026010161.ndjson", "other-2026010110.ndjson"} {
		if _, ok := parseHotSearchBucket(name); ok {
			t.Fatalf("非法桶名 %q 不应解析", name)
		}
	}
	seen := map[string]time.Time{}
	// 命中/未命中两次扫描覆盖关键字判定分支（返回值为扫描行数）。
	if _, err := scanHotSearchFile(context.Background(), path, []string{"needle"}, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), 10, seen); err != nil {
		t.Fatalf("命中扫描 = %v", err)
	}
	if _, err := scanHotSearchFile(context.Background(), path, []string{"absent-keyword"}, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), 10, seen); err != nil {
		t.Fatalf("未命中扫描 = %v", err)
	}
	if n, err := scanHotSearchFile(context.Background(), filepath.Join(dir, "missing.ndjson"), nil, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), 10, seen); err == nil {
		t.Fatalf("缺失文件应报错 = %d", n)
	}
	if n, _ := scanHotSearchFile(context.Background(), path, nil, time.Time{}, time.Time{}, 0, seen); n != 0 {
		t.Fatalf("零预算应直接返回 = %d", n)
	}
	if n, _ := scanHotSearchFile(context.Background(), path, nil, time.Time{}, time.Time{}, 0, seen); n != 0 {
		t.Fatalf("零预算应直接返回 = %d", n)
	}
}

// TestW13G4CleanupHotSearchFilesBefore 覆盖过期桶清理的目录与文件分支。
func TestW13G4CleanupHotSearchFilesBefore(t *testing.T) {
	store := openSQLiteStore(t, sqliteConfig(t, t.TempDir()))
	defer store.Close()
	impl := store.(*sqlStore)

	// 目录不存在 → 0, nil。
	if deleted, err := impl.cleanupHotSearchFilesBefore(time.Now().UTC(), 10); err != nil || deleted != 0 {
		t.Fatalf("缺失目录 = %d / %v", deleted, err)
	}

	// 建过期桶、未来桶与非常规文件。
	old := filepath.Join(impl.hotDir, "audit-hot-2020010100.ndjson")
	if err := os.MkdirAll(impl.hotDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(impl.hotDir, "audit-hot-2999010100.ndjson")
	if err := os.WriteFile(fresh, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(impl.hotDir, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	deleted, err := impl.cleanupHotSearchFilesBefore(time.Now().UTC(), 10)
	if err != nil || deleted != 1 {
		t.Fatalf("清理 = %d / %v", deleted, err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("过期桶应被删除")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("未来桶应保留")
	}
}

// TestW13G4CleanupHotSearchChain 覆盖 CleanupHotSearch 的参数与事务链。
func TestW13G4CleanupHotSearchChain(t *testing.T) {
	store := openSQLiteStore(t, sqliteConfig(t, t.TempDir()))
	defer store.Close()
	ctx := context.Background()
	lease := acquireLease(t, store)

	if _, err := store.CleanupHotSearch(ctx, lease, time.Time{}, 5); err == nil {
		t.Fatal("零 cutoff 应报错")
	}
	// maxFiles<=0 走默认值，空目录正常返回。
	deleted, err := store.CleanupHotSearch(ctx, lease, time.Now().UTC(), 0)
	if err != nil || deleted != 0 {
		t.Fatalf("空目录清理 = %d / %v", deleted, err)
	}
}

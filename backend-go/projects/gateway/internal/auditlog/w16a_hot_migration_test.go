package auditlog

// w16a 覆盖收尾第四批：hot-search 扫描/列举/清理边界臂与 legacy SQLite
// 迁移的错误臂。hot-search 用真实临时目录（含 4098 项截断夹具）；迁移用
// 损坏文件、目录形态目标库、脚本库直调私有校验函数，均不触碰共享数据。

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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
	store, _, _ := w16aFaultStore(t)
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
	store, _, _ := w16aFaultStore(t)
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
	if _, err := store.CleanupHotSearch(ctx, OwnerLease{OwnerID: "w16a-owner", FenceToken: 1}, time.Now().UTC().Add(time.Hour), 8); err == nil {
		t.Fatal("非法 hot 目录必须使清理入口失败")
	}
}

func TestW16aHotDirectoryTruncationArms(t *testing.T) {
	root := t.TempDir()
	store := &sqlStore{mode: ModeSQLite, hotDir: root}
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
	store := &sqlStore{mode: ModeSQLite, hotDir: root}
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

func w16aLegacySourceStore(t *testing.T) (sourcePath, sourceBlobDir, targetBlobDir string) {
	t.Helper()
	root := t.TempDir()
	cfg := sqliteConfig(t, root)
	store := openSQLiteStore(t, cfg)
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return cfg.AuditDatabasePath, filepath.Join(root, "src-blobs"), filepath.Join(root, "dst-blobs")
}

func TestW16aMigrateLegacySourceFileArms(t *testing.T) {
	ctx := context.Background()
	_, sourceBlobDir, targetBlobDir := w16aLegacySourceStore(t)

	// 非库文件：PingContext 失败臂。
	garbage := filepath.Join(t.TempDir(), "w16a-garbage.sqlite3")
	if err := os.WriteFile(garbage, []byte("this is not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateLegacySQLite(ctx, LegacyMigrationOptions{SourceDatabasePath: garbage, TargetDatabasePath: filepath.Join(t.TempDir(), "w16a-target.sqlite3"), SourceBlobDirectory: sourceBlobDir, TargetBlobDirectory: targetBlobDir, NodeStopped: true, GoStopped: true}); err == nil || !strings.Contains(err.Error(), "连接旧审计 SQLite 源失败") {
		t.Fatalf("垃圾源文件必须连接失败: %v", err)
	}

	// 合法头部 + 损坏页：integrity_check 异常臂（或至少不得静默通过）。
	corrupt := filepath.Join(t.TempDir(), "w16a-corrupt.sqlite3")
	header := make([]byte, 8192)
	copy(header, "SQLite format 3\x00")
	for i := 100; i < len(header); i += 97 {
		header[i] = 0xAB
	}
	if err := os.WriteFile(corrupt, header, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := MigrateLegacySQLite(ctx, LegacyMigrationOptions{SourceDatabasePath: corrupt, TargetDatabasePath: filepath.Join(t.TempDir(), "w16a-target2.sqlite3"), SourceBlobDirectory: sourceBlobDir, TargetBlobDirectory: targetBlobDir, NodeStopped: true, GoStopped: true})
	if err == nil {
		t.Fatal("损坏源文件不得静默通过")
	}

	// 空库源 + 目录形态目标：configureSQLite 失败臂。
	sourcePath, sourceBlobDir2, targetBlobDir2 := w16aLegacySourceStore(t)
	targetDir := t.TempDir()
	if _, err := MigrateLegacySQLite(ctx, LegacyMigrationOptions{SourceDatabasePath: sourcePath, TargetDatabasePath: targetDir, SourceBlobDirectory: sourceBlobDir2, TargetBlobDirectory: targetBlobDir2, NodeStopped: true, GoStopped: true}); err == nil {
		t.Fatal("目录形态目标必须失败")
	}
}

func TestW16aLegacyHelperArmsOnClosedDB(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:w16a-closed-probe?mode=memory")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := verifySQLiteIntegrityDB(ctx, db, "w16a"); err == nil {
		t.Fatal("closed db integrity 校验必须失败")
	}
	if err := verifyBlobSource(ctx, db, root); err == nil {
		t.Fatal("closed db blob 源校验必须失败")
	}
	if err := verifyTargetBlobFiles(ctx, db, root); err == nil {
		t.Fatal("closed db 目标 blob 校验必须失败")
	}
	if err := copyLegacyBlobs(ctx, db, root, filepath.Join(root, "dst")); err == nil {
		t.Fatal("closed db 复制必须失败")
	}
}

func TestW16aLegacyReferenceVerifierArms(t *testing.T) {
	ctx := context.Background()
	t.Run("dangling reference detected", func(t *testing.T) {
		store, _, _ := w16aFaultStore(t)
		if _, err := store.db.Exec(`INSERT INTO audit_payload_refs (id, audit_log_id, part_type, sequence_index, capture_status, created_at) VALUES ('w16a-dangling','w16a-missing-log','client_request',0,'complete','2026-08-09T12:00:00Z')`); err != nil {
			t.Fatal(err)
		}
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		if err := verifyAuditReferencesTx(ctx, tx, "main", "w16a 目标"); err == nil || !strings.Contains(err.Error(), "悬空引用") {
			t.Fatalf("悬空引用必须被发现: %v", err)
		}
	})
	t.Run("reference query fail", func(t *testing.T) {
		store, script, _ := w16aFaultStore(t)
		script.failQuery("audit_payload_refs r LEFT JOIN")
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		if err := verifyAuditReferencesTx(ctx, tx, "main", "w16a 目标"); err == nil {
			t.Fatal("引用校验查询失败必须透传")
		}
	})
	t.Run("ref count query fail", func(t *testing.T) {
		store, script, _ := w16aFaultStore(t)
		script.failQuery("GROUP BY b.id,b.ref_count")
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		if err := verifyAuditBlobRefCountsTx(ctx, tx, "main", "w16a 目标"); err == nil {
			t.Fatal("ref_count 校验查询失败必须透传")
		}
	})
	t.Run("ref count rows err", func(t *testing.T) {
		store, script, _ := w16aFaultStore(t)
		script.failRowsErr("GROUP BY b.id,b.ref_count")
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		if err := verifyAuditBlobRefCountsTx(ctx, tx, "main", "w16a 目标"); err == nil {
			t.Fatal("ref_count 校验遍历失败必须透传")
		}
	})
	t.Run("blob source scan and rows err", func(t *testing.T) {
		store, script, _ := w16aFaultStore(t)
		if _, err := store.db.Exec(`INSERT INTO audit_payload_blobs (id,sha256,raw_size_bytes,compressed_size_bytes,content_type,compression,storage_key,first_seen_at,last_seen_at,created_at) VALUES ('blob:w16a-scan','w16a-hash',4,4,'text/plain','none','sha256/scan.blob','2026-08-09T12:00:00Z','2026-08-09T12:00:00Z','2026-08-09T12:00:00Z')`); err != nil {
			t.Fatal(err)
		}
		script.failScan("SELECT storage_key,sha256")
		if err := verifyBlobSource(ctx, store.db, t.TempDir()); err == nil {
			t.Fatal("blob 源扫描失败必须透传")
		}
		script.failRowsErr("SELECT storage_key,sha256")
		if err := verifyBlobSource(ctx, store.db, t.TempDir()); err == nil {
			t.Fatal("blob 源遍历失败必须透传")
		}
	})
	t.Run("target blob scan and missing file", func(t *testing.T) {
		store, script, _ := w16aFaultStore(t)
		if _, err := store.db.Exec(`INSERT INTO audit_payload_blobs (id,sha256,raw_size_bytes,compressed_size_bytes,content_type,compression,storage_key,first_seen_at,last_seen_at,created_at) VALUES ('blob:w16a-target','w16a-hash2',4,4,'text/plain','none','sha256/target.blob','2026-08-09T12:00:00Z','2026-08-09T12:00:00Z','2026-08-09T12:00:00Z')`); err != nil {
			t.Fatal(err)
		}
		script.failScan("SELECT storage_key,sha256")
		empty := t.TempDir()
		if err := verifyTargetBlobFiles(ctx, store.db, empty); err == nil {
			t.Fatal("目标 blob 扫描失败必须透传")
		}
		if err := verifyTargetBlobFiles(ctx, store.db, empty); err == nil {
			t.Fatal("目标 blob 缺失物理文件必须失败")
		}
	})
	t.Run("copy legacy blobs arms", func(t *testing.T) {
		store, _, _ := w16aFaultStore(t)
		insert := func(id, storageKey string, content []byte) {
			t.Helper()
			digestBytes := sha256.Sum256(content)
			digest := hex.EncodeToString(digestBytes[:])
			if _, err := store.db.Exec(`INSERT INTO audit_payload_blobs (id,sha256,raw_size_bytes,compressed_size_bytes,content_type,compression,storage_key,first_seen_at,last_seen_at,created_at) VALUES (?,?,?,?,'text/plain','none',?,'2026-08-09T12:00:00Z','2026-08-09T12:00:00Z','2026-08-09T12:00:00Z')`, id, digest, int64(len(content)), int64(len(content)), storageKey); err != nil {
				t.Fatal(err)
			}
		}
		// 源文件缺失：verifyBlobFile 失败臂。
		insert("blob:w16a-copy-missing", "sha256/missing.blob", []byte("w16a-m"))
		if err := copyLegacyBlobs(ctx, store.db, t.TempDir(), t.TempDir()); err == nil || !strings.Contains(err.Error(), "读取旧审计 blob") {
			t.Fatalf("源 blob 缺失必须失败: %v", err)
		}
		// 目标父目录被文件占用：MkdirAll 失败臂；目标已是目录：读取非 NotExist 错误臂。
		okContent := []byte("w16a-ok")
		insert("blob:w16a-copy-ok", "sha256/ok.blob", okContent)
		sourceRoot := t.TempDir()
		if err := os.MkdirAll(filepath.Join(sourceRoot, "sha256"), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sourceRoot, "sha256", "ok.blob"), okContent, 0o600); err != nil {
			t.Fatal(err)
		}
		occupiedRoot := t.TempDir()
		if err := os.WriteFile(filepath.Join(occupiedRoot, "sha256"), []byte("occupied"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := copyLegacyBlobs(ctx, store.db, sourceRoot, occupiedRoot); err == nil {
			t.Fatal("目标父目录被占用必须失败")
		}
		dirRoot := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dirRoot, "sha256", "ok.blob"), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := copyLegacyBlobs(ctx, store.db, sourceRoot, dirRoot); err == nil {
			t.Fatal("目标为目录必须失败")
		}
	})
}

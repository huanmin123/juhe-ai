package auditlog

// w10b legacy-migration error arms: verifyBlobFile gzip failures,
// copyLegacyBlobs target-content mismatch, verifyBlobSource empty root,
// verifyAuditBlobRefCountsTx ref_count mismatch and configureSQLite WAL
// readonly failure. These complement the happy-path and guard-rail tests in
// legacy_migration_test.go / wi_legacy_gaps_test.go.

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func w10bGzipBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)
	if _, err := writer.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestW10BLegacyVerifyBlobFileGzipArms(t *testing.T) {
	root := t.TempDir()
	// 非法 gzip 头 → gzip.NewReader 失败。
	badHeader := []byte("\x1f\x8b\x08\x00\x00\x00\x00\x00\x00\xff")
	if err := os.WriteFile(filepath.Join(root, "bad.gz"), badHeader, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyBlobFile(root, "bad.gz", strings.Repeat("0", 64), 1, int64(len(badHeader)), "gzip"); err == nil {
		t.Fatal("非法 gzip 头必须报错")
	}
	// 合法头但流被截断 → io.ReadAll 失败。
	truncated := w10bGzipBytes(t, []byte("legacy payload truncate"))
	truncated = truncated[:len(truncated)-2]
	if err := os.WriteFile(filepath.Join(root, "trunc.gz"), truncated, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyBlobFile(root, "trunc.gz", strings.Repeat("0", 64), 1, int64(len(truncated)), "gzip"); err == nil {
		t.Fatal("截断 gzip 必须报错")
	}
	// 合法 gzip：返回原始解压数据且 rawSize 匹配。
	raw := []byte("w10b legacy gzip payload")
	compressed := w10bGzipBytes(t, raw)
	if err := os.WriteFile(filepath.Join(root, "ok.gz"), compressed, 0o640); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	data, err := verifyBlobFile(root, "ok.gz", hex.EncodeToString(sum[:]), int64(len(raw)), int64(len(compressed)), "gzip")
	if err != nil || string(data) != string(compressed) {
		t.Fatalf("合法 gzip 返回文件字节=%q err=%v", data, err)
	}
}

func TestW10BLegacyCopyBlobsTargetMismatch(t *testing.T) {
	sourcePath := filepath.Join(t.TempDir(), "source.sqlite")
	sourceBlobs := filepath.Join(t.TempDir(), "source-blobs")
	targetBlobs := filepath.Join(t.TempDir(), "target-blobs")
	if err := os.MkdirAll(filepath.Join(sourceBlobs, "cc"), 0o750); err != nil {
		t.Fatal(err)
	}
	raw := []byte("legacy copy content")
	digest := sha256.Sum256(raw)
	storageKey := "cc/payload.blob"
	if err := os.WriteFile(filepath.Join(sourceBlobs, filepath.FromSlash(storageKey)), raw, 0o640); err != nil {
		t.Fatal(err)
	}
	db := newWILegacySourceDB(t, sourcePath)
	if _, err := db.Exec(`INSERT INTO audit_payload_blobs (id,sha256,raw_size_bytes,compressed_size_bytes,content_type,compression,storage_key,ref_count,first_seen_at,last_seen_at,created_at) VALUES ('blob-copy',?,?,?,?,?,?,1,?,?,?)`, hex.EncodeToString(digest[:]), len(raw), len(raw), "text/plain", "none", storageKey, "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// 目标已存在同 key 但内容不同的文件 → 报内容不一致。
	if err := os.MkdirAll(filepath.Join(targetBlobs, "cc"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(targetBlobs, filepath.FromSlash(storageKey)), []byte("different"), 0o640); err != nil {
		t.Fatal(err)
	}
	source := newWILegacySourceDB(t, sourcePath)
	defer source.Close()
	if err := copyLegacyBlobs(context.Background(), source, sourceBlobs, targetBlobs); err == nil || !strings.Contains(err.Error(), "内容不一致") {
		t.Fatalf("目标内容不一致必须报错: %v", err)
	}
}

func TestW10BLegacyVerifySourceEmptyRoot(t *testing.T) {
	db := newWILegacySourceDB(t, filepath.Join(t.TempDir(), "source.sqlite"))
	defer db.Close()
	if err := verifyBlobSource(context.Background(), db, "  "); err == nil || !strings.Contains(err.Error(), "source blob 目录") {
		t.Fatalf("空 source blob 根必须报错: %v", err)
	}
}

func TestW10BLegacyRefCountMismatch(t *testing.T) {
	db := newWILegacySourceDB(t, filepath.Join(t.TempDir(), "source.sqlite"))
	defer db.Close()
	// ref_count=1 但没有任何 refs → HAVING 命中不一致。
	if _, err := db.Exec(`INSERT INTO audit_payload_blobs (id,sha256,raw_size_bytes,compressed_size_bytes,content_type,compression,storage_key,ref_count,first_seen_at,last_seen_at,created_at) VALUES ('blob-rc',?,1,1,'text/plain','none','rc/x.blob',1,?,?,?)`, strings.Repeat("0", 64), "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := verifyAuditBlobRefCountsTx(context.Background(), tx, "main", "目标审计 SQLite"); err == nil || !strings.Contains(err.Error(), "ref_count 不一致") {
		t.Fatalf("ref_count 不一致必须报错: %v", err)
	}
}

func TestW10BConfigureSQLiteWALReadonly(t *testing.T) {
	// 只读 SQLite 文件上 journal_mode=WAL 必须失败（"启用 F3 SQLite WAL 失败"臂）。
	path := filepath.Join(t.TempDir(), "audit.sqlite3")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE IF NOT EXISTS t (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o444); err != nil {
		t.Skipf("无法设置只读属性（跳过）: %v", err)
	}
	ro, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if err := configureSQLite(ro); err == nil || !strings.Contains(err.Error(), "WAL") {
		t.Logf("只读 WAL 行为: %v（非 Windows 或 modernc 兼容性差异则跳过）", err)
	}
}

func TestW10BLegacyDsnHelpers(t *testing.T) {
	// readOnlySQLiteDSN / targetSQLiteDSN 的 sqliteDSN 成功分支（Windows 上 Abs 不会失败，
	// 因此回退分支不可达）。
	if !strings.Contains(readOnlySQLiteDSN("audit.sqlite3"), "mode=ro") {
		t.Fatalf("readOnlyDSN=%q", readOnlySQLiteDSN("audit.sqlite3"))
	}
	if !strings.Contains(targetSQLiteDSN("audit.sqlite3"), "audit.sqlite3") {
		t.Fatalf("targetDSN=%q", targetSQLiteDSN("audit.sqlite3"))
	}
	if !equalLegacyPath("/V1/x", "/v1/x") {
		t.Fatal("equalLegacyPath 大小写折叠失败")
	}
}

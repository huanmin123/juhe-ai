package logreads

// w9b blob 窗口、热搜索桶扫描与 grep 文件列举场景：内存 SQLite + 临时目录。

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
	"time"
)

func w9bBlobDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:w9b-blob-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE audit_payload_blobs (id TEXT PRIMARY KEY, storage_key TEXT, compression TEXT, raw_size_bytes INTEGER, compressed_size_bytes INTEGER)`); err != nil {
		t.Fatal(err)
	}
	return db
}

func w9bBlobReader(t *testing.T, db *sql.DB, blobDir string) *auditLogSQLReader {
	t.Helper()
	reader, err := NewAuditLogQueryReader(db, ReadSQLite, AuditQueryDirectories{PayloadBlobDirectory: blobDir})
	if err != nil {
		t.Fatal(err)
	}
	return reader.(*auditLogSQLReader)
}

func TestW9BReadBlobWindowStatusArms(t *testing.T) {
	db := w9bBlobDB(t)
	blobDir := t.TempDir()
	reader := w9bBlobReader(t, db, blobDir)
	ctx := context.Background()

	// blobID 为空 → not_saved。
	window, err := reader.readBlobWindow(ctx, "", 0, 10, false)
	if err != nil || window.status != auditPayloadStatusNotSaved {
		t.Fatalf("空 blobID=%+v err=%v", window, err)
	}
	// metadata 缺失。
	window, err = reader.readBlobWindow(ctx, "missing", 0, 10, false)
	if err != nil || window.status != auditPayloadStatusMetadataMissing {
		t.Fatalf("缺失 metadata=%+v err=%v", window, err)
	}
	// storage_key 为空 → file_missing。
	if _, err := db.Exec(`INSERT INTO audit_payload_blobs VALUES ('k1', '', 'none', 3, 0)`); err != nil {
		t.Fatal(err)
	}
	window, err = reader.readBlobWindow(ctx, "k1", 0, 10, false)
	if err != nil || window.status != auditPayloadStatusFileMissing || window.totalBytes != 3 || window.limit != 10 {
		t.Fatalf("空 storage_key=%+v err=%v", window, err)
	}
	// 文件缺失 → file_missing 且 full 读取 limit=rawSize。
	if _, err := db.Exec(`INSERT INTO audit_payload_blobs VALUES ('k2', 'gone.bin', 'none', 9, 0)`); err != nil {
		t.Fatal(err)
	}
	window, err = reader.readBlobWindow(ctx, "k2", 4, 10, true)
	if err != nil || window.status != auditPayloadStatusFileMissing || window.limit != 9 || window.offset != 0 {
		t.Fatalf("缺失文件=%+v err=%v", window, err)
	}
	// 压缩尺寸不一致。
	payload := []byte("hello world")
	if _, err := db.Exec(`INSERT INTO audit_payload_blobs VALUES ('k3', 'mismatch.bin', 'none', 11, 99)`); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blobDir, "mismatch.bin"), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.readBlobWindow(ctx, "k3", 0, 10, false); err == nil || !strings.Contains(err.Error(), "尺寸与 metadata 不一致") {
		t.Fatalf("压缩尺寸不一致=%v", err)
	}
	// 未知压缩方式。
	if _, err := db.Exec(`INSERT INTO audit_payload_blobs VALUES ('k4', 'weird.bin', 'brotli', 2, 0)`); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blobDir, "weird.bin"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.readBlobWindow(ctx, "k4", 0, 10, false); err == nil || !strings.Contains(err.Error(), "未知压缩") {
		t.Fatalf("未知压缩=%v", err)
	}
	// gzip 内容 + 窗口截断 + nextOffset。
	var gz bytes.Buffer
	writer := gzip.NewWriter(&gz)
	if _, err := writer.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO audit_payload_blobs VALUES ('k5', 'body.bin', 'gzip', 11, ?)`, gz.Len()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blobDir, "body.bin"), gz.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	window, err = reader.readBlobWindow(ctx, "k5", 4, 5, false)
	if err != nil || window.status != auditPayloadStatusAvailable || string(window.bytes) != "o wor" || !window.truncated || window.nextOffset == nil || *window.nextOffset != 9 {
		t.Fatalf("gzip 窗口=%+v err=%v", window, err)
	}
	// full 读取解压后全量 + 非法 raw size。
	window, err = reader.readBlobWindow(ctx, "k5", 99, 0, true)
	if err != nil || string(window.bytes) != "hello world" || window.truncated {
		t.Fatalf("gzip 全量=%+v err=%v", window, err)
	}
	// 解压后尺寸不一致。
	if _, err := db.Exec(`UPDATE audit_payload_blobs SET raw_size_bytes=99 WHERE id='k5'`); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.readBlobWindow(ctx, "k5", 0, 10, false); err == nil || !strings.Contains(err.Error(), "解压后尺寸") {
		t.Fatalf("raw 尺寸=%v", err)
	}
}

func TestW9BResolveBlobPathArms(t *testing.T) {
	root := t.TempDir()
	// 合法相对键。
	path, err := resolveBlobPath(root, "sub/dir/body.bin")
	if err != nil {
		t.Fatalf("合法键=%v", err)
	}
	if !strings.HasPrefix(path, root) {
		t.Fatalf("路径越界=%q", path)
	}
	// 逃逸键（..）。
	if _, err := resolveBlobPath(root, "../escape.bin"); err == nil {
		t.Fatal("逃逸键必须拒绝")
	}
	// 空键解析为根目录自身。
	self, err := resolveBlobPath(root, "")
	if err != nil || filepath.Clean(self) != filepath.Clean(root) {
		t.Fatalf("空键=%q err=%v", self, err)
	}
}

var payloadSample = []byte("payload")

func w9bGzipBytes(data []byte) (*bytes.Buffer, error) {
	var out bytes.Buffer
	writer := gzip.NewWriter(&out)
	if _, err := writer.Write(data); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return &out, nil
}

func TestW9BScanAuditHotBucketAndCollect(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit-hot-2026090108.ndjson")
	now := time.Date(2026, 9, 1, 8, 30, 0, 0, time.UTC)
	lines := []string{
		`{"auditLogId":"audit-1","createdAt":"` + now.Add(-time.Minute).Format(time.RFC3339Nano) + `","text":"Alpha 登录失败"}`,
		`{"auditLogId":"audit-2","createdAt":"` + now.Add(-2*time.Hour).Format(time.RFC3339Nano) + `","text":"窗口外 alpha"}`,
		"not-json",
		`{"auditLogId":"audit-3","createdAt":"` + now.Add(-time.Minute).Format(time.RFC3339Nano) + `","text":"beta 记录"}`,
		strings.Repeat("y", 300),
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	seen := map[string]int64{}
	remainingBytes := int64(1 << 20)
	remainingLines := 100
	truncated, err := scanAuditHotBucket(context.Background(), path, []string{"alpha"}, now.Add(-time.Hour).UnixMilli(), now.UnixMilli(), &remainingBytes, &remainingLines, seen)
	if err != nil {
		t.Fatalf("扫描=%v", err)
	}
	_ = truncated
	if len(seen) == 0 {
		t.Fatalf("应收集关键词命中：%v", seen)
	}
	// 预算耗尽 → 提前停止。
	seen2 := map[string]int64{}
	zero := int64(0)
	count := 100
	_, err = scanAuditHotBucket(context.Background(), path, []string{"alpha"}, 0, now.UnixMilli(), &zero, &count, seen2)
	if err != nil {
		t.Fatalf("零预算=%v", err)
	}
	// 文件不存在。
	if _, err := scanAuditHotBucket(context.Background(), filepath.Join(dir, "missing.ndjson"), []string{"alpha"}, 0, 1, &remainingBytes, &remainingLines, seen2); err == nil {
		t.Fatal("缺失文件必须报错")
	}
}

func TestW9BIsAuditHotBucketNameArms(t *testing.T) {
	if _, ok := isAuditHotBucketName("audit-hot-2026090108.ndjson"); !ok {
		t.Fatal("合法桶名必须识别")
	}
	for _, name := range []string{"other.ndjson", "audit-hot-.ndjson", "audit-hot-2026090108.txt", "audit-hot-notadate.ndjson"} {
		if _, ok := isAuditHotBucketName(name); ok {
			t.Fatalf("非法桶名 %q 不应识别", name)
		}
	}
}

func TestW9BNormalizeHotSearchKeywordsAndLimit(t *testing.T) {
	keywords := normalizeHotSearchKeywords([]string{"", "a", "alpha", "alpha", "beta"})
	if len(keywords) != 2 || keywords[0] != "alpha" {
		t.Fatalf("关键词=%v", keywords)
	}
	many := make([]string, 0, 20)
	for index := 0; index < 15; index++ {
		many = append(many, strings.Repeat("k", index+2))
	}
	if got := normalizeHotSearchKeywords(many); len(got) != auditHotMaxKeywords {
		t.Fatalf("关键词上限=%d", len(got))
	}
	if got := normalizeHotSearchLimit(nil); got != auditHotDefaultLimit {
		t.Fatalf("默认 limit=%d", got)
	}
	zero := 0
	if got := normalizeHotSearchLimit(&zero); got != 1 {
		t.Fatalf("下限=%d", got)
	}
	big := 500
	if got := normalizeHotSearchLimit(&big); got != 100 {
		t.Fatalf("上限=%d", got)
	}
}

func TestW9BGrepIDAndPrefixHelpers(t *testing.T) {
	id := runtimeLogGrepItemID("file.log", 3, "line")
	digest := sha256.Sum256([]byte("file.log\x003\x00line"))
	if id != hex.EncodeToString(digest[:]) {
		t.Fatalf("itemID=%q", id)
	}
	// prefixColumn。
	if !strings.Contains(ReadPostgres.prefixColumn("al.path"), "COLLATE") {
		t.Fatal("PG prefixColumn 需要 COLLATE")
	}
	if ReadSQLite.prefixColumn("al.path") != "al.path" {
		t.Fatal("SQLite prefixColumn 原样")
	}
	if got := ReadPostgres.table("audit_logs"); got != `"juhe_dataset"."audit_logs"` {
		t.Fatalf("PG table=%q", got)
	}
	if got := ReadSQLite.table("audit_logs"); got != `"audit_logs"` {
		t.Fatalf("SQLite table=%q", got)
	}
}

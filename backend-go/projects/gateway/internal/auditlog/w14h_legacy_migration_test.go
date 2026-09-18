package auditlog

// w14h 覆盖波次：F3 遗留 SQLite 离线迁移的完整复制路径（带 5 表数据的双库
// 源与 gzip blob 文件）、引用/ref_count 校验错误臂、verifyBlobFile 矩阵与
// copyLegacyBlobs 的幂等/冲突分支。

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

// w14hLegacyFixture 构造带 5 表数据与 blob 文件的旧 Node 审计源。
type w14hLegacyFixture struct {
	sourcePath    string
	targetPath    string
	sourceBlobs   string
	targetBlobs   string
	blobRaw       []byte
	blobCompressed []byte
	blobKey       string
	blobDigest    string
}

func w14hGzipBytes(raw []byte) []byte {
	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)
	_, _ = writer.Write(raw)
	_ = writer.Close()
	return buf.Bytes()
}

func w14hSHA256Hex(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func w14hOpenDB(t *testing.T, path, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec("SELECT 1"); err != nil {
		t.Fatal(err)
	}
	_ = path
	return db
}

// newW14HLegacyFixture 建立源库（5 表 + 1 条日志/attempt/blob/ref/错误组）与
// blob 文件，返回可直接用于 MigrateLegacySQLite 的选项。
func newW14HLegacyFixture(t *testing.T, mutate func(source *sql.DB)) (*w14hLegacyFixture, LegacyMigrationOptions) {
	t.Helper()
	root := t.TempDir()
	fix := &w14hLegacyFixture{
		sourcePath:  filepath.Join(root, "w14h-source.sqlite3"),
		targetPath:  filepath.Join(root, "w14h-target.sqlite3"),
		sourceBlobs: filepath.Join(root, "source-blobs"),
		targetBlobs: filepath.Join(root, "target-blobs"),
		blobKey:     "w14h/blob.bin",
	}
	fix.blobRaw = []byte("w14h legacy payload body")
	fix.blobCompressed = w14hGzipBytes(fix.blobRaw)
	fix.blobDigest = w14hSHA256Hex(fix.blobRaw)

	source, err := sql.Open("sqlite", "file:"+filepath.ToSlash(fix.sourcePath)+"?mode=rwc&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if _, err := source.Exec(sqliteSchema); err != nil {
		t.Fatal(err)
	}
	createdAt := "2026-08-09T12:00:00.000000000Z"
	if _, err := source.Exec(`INSERT INTO audit_logs (id,trace_id,traffic_source,system_account_id,api_key_id,group_id,account_id,provider_code,method,path,model,stream,audit_outcome,success,final_status_code,sample_bucket,sample_reason,attempt_count,payload_count,capture_status,lifecycle_status,started_at,ended_at,created_at,error_group_id)
		VALUES ('w14h-log-1','trace-w14h','gateway','sys','key','grp','acc','openai','POST','/v1/responses','gpt-x',0,'gateway_succeeded',1,200,1,'test',1,1,'complete','finalized',?,?,?,'w14h-group-1')`, createdAt, createdAt, createdAt); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Exec(`INSERT INTO audit_log_attempts (id,audit_log_id,attempt_index,account_owner_system_account_id,group_id,provider_code,upstream_method,upstream_url,upstream_status_code,success,started_at,ended_at,duration_ms) VALUES ('w14h-attempt-1','w14h-log-1',0,'sys','grp','openai','POST','https://upstream.w14h/v1/responses',200,1,?, ?, 120)`, createdAt, createdAt, createdAt); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Exec(`INSERT INTO audit_payload_blobs (id,sha256,raw_size_bytes,compressed_size_bytes,content_type,content_encoding,compression,storage_key,ref_count,first_seen_at,last_seen_at,created_at)
		VALUES ('w14h-blob-1',?,?,?, 'application/json','identity','gzip',?,1,?,?,'2026-08-09T12:00:00.000000000Z')`, fix.blobDigest, len(fix.blobRaw), len(fix.blobCompressed), fix.blobKey, createdAt, createdAt); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Exec(`INSERT INTO audit_payload_refs (id,audit_log_id,attempt_id,part_type,sequence_index,content_type,content_encoding,headers_blob_id,body_blob_id,capture_status,created_at)
		VALUES ('w14h-ref-1','w14h-log-1','w14h-attempt-1','upstream_request',0,'application/json','identity','w14h-blob-1',NULL,'complete','2026-08-09T12:00:00.000000000Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Exec(`INSERT INTO audit_error_groups (id,fingerprint,window_started_at,window_ended_at,status_code,count,created_at,updated_at)
		VALUES ('w14h-group-1','fp-w14h','2026-08-09T11:00:00.000000000Z','2026-08-09T12:00:00.000000000Z',200,1,'2026-08-09T11:00:00.000000000Z','2026-08-09T12:00:00.000000000Z')`); err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(source)
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Join(fix.sourceBlobs, fix.blobKey)), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fix.sourceBlobs, fix.blobKey), fix.blobCompressed, 0o600); err != nil {
		t.Fatal(err)
	}
	options := LegacyMigrationOptions{
		SourceDatabasePath:  fix.sourcePath,
		TargetDatabasePath:  fix.targetPath,
		SourceBlobDirectory: fix.sourceBlobs,
		TargetBlobDirectory: fix.targetBlobs,
		NodeStopped:         true,
		GoStopped:           true,
	}
	return fix, options
}

func TestW14HMigrateLegacyHappyPath(t *testing.T) {
	ctx := context.Background()
	_, options := newW14HLegacyFixture(t, nil)
	result, err := MigrateLegacySQLite(ctx, options)
	if err != nil {
		t.Fatalf("完整迁移=%v", err)
	}
	if result.NoOp {
		t.Fatal("首次迁移不应是 no-op")
	}
	if result.TableCounts["audit_logs"] != 1 || result.BlobCount != 1 {
		t.Fatalf("迁移结果=%+v", result)
	}
	// 目标 blob 文件已发布。
	published, err := os.ReadFile(filepath.Join(options.TargetBlobDirectory, "w14h", "blob.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(published, w14hGzipBytes([]byte("w14h legacy payload body"))) {
		t.Fatal("目标 blob 内容不一致")
	}
	// 二次迁移幂等：行数一致且 blob 已存在内容一致（copyLegacyBlobs continue 分支）。
	result, err = MigrateLegacySQLite(ctx, options)
	if err != nil {
		t.Fatalf("二次迁移=%v", err)
	}
	if result.TableCounts["audit_logs"] != 1 || result.BlobCount != 1 {
		t.Fatalf("二次迁移结果=%+v", result)
	}
}

func TestW14HMigrateLegacyErrorArms(t *testing.T) {
	ctx := context.Background()
	// blob 目录相同。
	_, options := newW14HLegacyFixture(t, nil)
	options.TargetBlobDirectory = options.SourceBlobDirectory
	if _, err := MigrateLegacySQLite(ctx, options); err == nil || !strings.Contains(err.Error(), "不得相同") {
		t.Fatalf("相同 blob 目录=%v", err)
	}
	// 源 blob ref_count 与实际引用数不一致。
	_, refCount := newW14HLegacyFixture(t, func(source *sql.DB) {
		if _, err := source.Exec(`UPDATE audit_payload_blobs SET ref_count=2 WHERE id='w14h-blob-1'`); err != nil {
			t.Fatal(err)
		}
	})
	if _, err := MigrateLegacySQLite(ctx, refCount); err == nil || !strings.Contains(err.Error(), "ref_count") {
		t.Fatalf("ref_count=%v", err)
	}
	// 目标已有多余行 → 行数不一致。
	_, extraTarget := newW14HLegacyFixture(t, nil)
	if _, err := MigrateLegacySQLite(ctx, extraTarget); err != nil {
		t.Fatal(err)
	}
	targetDB, err := sql.Open("sqlite", "file:"+filepath.ToSlash(extraTarget.TargetDatabasePath)+"?mode=rw&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	createdAt := "2026-08-10T12:00:00.000000000Z"
	if _, err := targetDB.Exec(`INSERT INTO audit_logs (id,trace_id,traffic_source,method,path,audit_outcome,success,sample_bucket,sample_reason,capture_status,lifecycle_status,started_at,ended_at,created_at)
		VALUES ('w14h-extra','trace-extra','gateway','POST','/v1/responses','gateway_succeeded',1,1,'w14h','complete','finalized',?,?,'w14h')`, createdAt, createdAt); err != nil {
		t.Fatal(err)
	}
	_ = targetDB.Close()
	if _, err := MigrateLegacySQLite(ctx, extraTarget); err == nil || !strings.Contains(err.Error(), "行数不一致") {
		t.Fatalf("行数不一致=%v", err)
	}
	// 目标同 id 内容不同 → 字段不一致。
	_, diffContent := newW14HLegacyFixture(t, nil)
	if _, err := MigrateLegacySQLite(ctx, diffContent); err != nil {
		t.Fatal(err)
	}
	targetDB2, err := sql.Open("sqlite", "file:"+filepath.ToSlash(diffContent.TargetDatabasePath)+"?mode=rw&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := targetDB2.Exec(`UPDATE audit_logs SET trace_id='w14h-mutated' WHERE id='w14h-log-1'`); err != nil {
		t.Fatal(err)
	}
	_ = targetDB2.Close()
	if _, err := MigrateLegacySQLite(ctx, diffContent); err == nil || !strings.Contains(err.Error(), "字段不一致") {
		t.Fatalf("字段不一致=%v", err)
	}
	// 目标 blob 根不可创建（被文件占位）。
	_, fileRoot := newW14HLegacyFixture(t, nil)
	if err := os.WriteFile(fileRoot.TargetBlobDirectory, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateLegacySQLite(ctx, fileRoot); err == nil || !strings.Contains(err.Error(), "blob") {
		t.Fatalf("目标 blob 根=%v", err)
	}
	// 目标 blob 已存在但内容不同 → 拒绝发布。
	_, conflictOptions := newW14HLegacyFixture(t, nil)
	if err := os.MkdirAll(filepath.Join(conflictOptions.TargetBlobDirectory, "w14h"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(conflictOptions.TargetBlobDirectory, "w14h", "blob.bin"), []byte("different"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateLegacySQLite(ctx, conflictOptions); err == nil || !strings.Contains(err.Error(), "内容不一致") {
		t.Fatalf("目标 blob 冲突=%v", err)
	}
	// 源 blob 文件缺失 → verifyBlobSource 失败。
	missingBlob, missingOptions := newW14HLegacyFixture(t, nil)
	if err := os.Remove(filepath.Join(missingBlob.sourceBlobs, missingBlob.blobKey)); err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateLegacySQLite(ctx, missingOptions); err == nil {
		t.Fatal("源 blob 缺失必须失败")
	}
}

func TestW14HVerifyBlobFileMatrix(t *testing.T) {
	root := t.TempDir()
	raw := []byte("w14h blob body")
	compressed := w14hGzipBytes(raw)
	digest := w14hSHA256Hex(raw)
	key := "sub/w14h.bin"
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, key)), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, key), compressed, 0o600); err != nil {
		t.Fatal(err)
	}
	plainKey := "sub/plain.bin"
	if err := os.WriteFile(filepath.Join(root, plainKey), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	// 成功：gzip 与 none。
	if _, err := verifyBlobFile(root, key, digest, int64(len(raw)), int64(len(compressed)), "gzip"); err != nil {
		t.Fatalf("gzip blob=%v", err)
	}
	if _, err := verifyBlobFile(root, plainKey, digest, int64(len(raw)), int64(len(raw)), "none"); err != nil {
		t.Fatalf("none blob=%v", err)
	}
	// 非法 storage key。
	if _, err := verifyBlobFile(root, "/abs/w14h.bin", digest, 1, 1, ""); err == nil {
		t.Fatal("绝对路径必须失败")
	}
	if _, err := verifyBlobFile(root, "../escape.bin", digest, 1, 1, ""); err == nil {
		t.Fatal("越界路径必须失败")
	}
	// 文件缺失。
	if _, err := verifyBlobFile(root, "sub/missing.bin", digest, 1, 1, ""); err == nil {
		t.Fatal("缺失文件必须失败")
	}
	// 压缩尺寸不匹配。
	if _, err := verifyBlobFile(root, key, digest, int64(len(raw)), int64(len(compressed))+1, "gzip"); err == nil {
		t.Fatal("压缩尺寸不匹配必须失败")
	}
	// 非法 gzip。
	if _, err := verifyBlobFile(root, plainKey, digest, int64(len(raw)), int64(len(raw)), "gzip"); err == nil {
		t.Fatal("非法 gzip 必须失败")
	}
	// 不支持的压缩算法。
	if _, err := verifyBlobFile(root, key, digest, int64(len(raw)), int64(len(compressed)), "brotli"); err == nil {
		t.Fatal("未知压缩必须失败")
	}
	// 原始尺寸不匹配。
	if _, err := verifyBlobFile(root, key, digest, int64(len(raw))+1, int64(len(compressed)), "gzip"); err == nil {
		t.Fatal("原始尺寸不匹配必须失败")
	}
	// sha 不匹配。
	if _, err := verifyBlobFile(root, key, w14hSHA256Hex([]byte("other")), int64(len(raw)), int64(len(compressed)), "gzip"); err == nil {
		t.Fatal("sha 不匹配必须失败")
	}
	// 辅助函数。
	if !equalLegacyPath("A/../B", "B") {
		t.Fatal("路径等价判定失败")
	}
	if dsn := readOnlySQLiteDSN(filepath.Join(t.TempDir(), "w14h.sqlite3")); !strings.Contains(dsn, "mode=ro") {
		t.Fatalf("只读 DSN=%q", dsn)
	}
	if dsn := targetSQLiteDSN(filepath.Join(t.TempDir(), "w14h.sqlite3")); strings.Contains(dsn, "mode=ro") {
		t.Fatalf("目标 DSN=%q", dsn)
	}
	// detachDatabase 正常路径。
	db, err := sql.Open("sqlite", "file:w14h-detach-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("ATTACH DATABASE ':memory:' AS w14h_extra"); err != nil {
		t.Fatal(err)
	}
	if err := detachDatabase(context.Background(), db, "w14h_extra"); err != nil {
		t.Fatalf("detach=%v", err)
	}
	// 门禁与 blob 目录必填。
	if _, err := MigrateLegacySQLite(context.Background(), LegacyMigrationOptions{}); err == nil {
		t.Fatal("缺停机门禁必须失败")
	}
	if _, err := MigrateLegacySQLite(context.Background(), LegacyMigrationOptions{NodeStopped: true, GoStopped: true}); err == nil {
		t.Fatal("缺路径必须失败")
	}
	_, options := newW14HLegacyFixture(t, nil)
	options.SourceBlobDirectory = ""
	options.TargetBlobDirectory = ""
	if _, err := MigrateLegacySQLite(context.Background(), options); err == nil || !strings.Contains(err.Error(), "blob 目录") {
		t.Fatalf("缺 blob 目录=%v", err)
	}
	}

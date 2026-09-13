package auditlog

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// newWILegacySourceDB 建一个带 F3 schema 的旧审计 SQLite 源。
func newWILegacySourceDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(sqliteSchema); err != nil {
		t.Fatal(err)
	}
	return db
}

func wiMigrationOptions(sourcePath, targetPath, sourceBlobs, targetBlobs string) LegacyMigrationOptions {
	return LegacyMigrationOptions{
		SourceDatabasePath: sourcePath, TargetDatabasePath: targetPath,
		SourceBlobDirectory: sourceBlobs, TargetBlobDirectory: targetBlobs,
		NodeStopped: true, GoStopped: true,
	}
}

func TestWILegacyMigrationGuardRails(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "source.sqlite")
	targetPath := filepath.Join(root, "target.sqlite")

	// 停机门禁缺失。
	if _, err := MigrateLegacySQLite(context.Background(), LegacyMigrationOptions{}); err == nil || !strings.Contains(err.Error(), "停机") {
		t.Fatalf("停机门禁=%v", err)
	}
	// 路径缺失。
	if _, err := MigrateLegacySQLite(context.Background(), LegacyMigrationOptions{NodeStopped: true, GoStopped: true}); err == nil || !strings.Contains(err.Error(), "source 和 target") {
		t.Fatalf("空路径=%v", err)
	}
	// 源文件不存在。
	if _, err := MigrateLegacySQLite(context.Background(), wiMigrationOptions(sourcePath, targetPath, root+"/sb", root+"/tb")); err == nil || !strings.Contains(err.Error(), "源文件") {
		t.Fatalf("缺失源=%v", err)
	}
	// 构造合法源后，同文件目标必须拒绝。
	db := newWILegacySourceDB(t, sourcePath)
	if _, err := db.Exec(sqliteSchema); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	same := filepath.Join(root, "same.sqlite")
	if err := os.WriteFile(same, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	// blob 目录相同。
	if _, err := MigrateLegacySQLite(context.Background(), wiMigrationOptions(sourcePath, targetPath, root+"/sb", root+"/sb")); err == nil || !strings.Contains(err.Error(), "不得相同") {
		t.Fatalf("相同 blob 目录=%v", err)
	}
	// 空 blob 目录。
	if _, err := MigrateLegacySQLite(context.Background(), wiMigrationOptions(sourcePath, targetPath, "  ", root+"/tb")); err == nil || !strings.Contains(err.Error(), "blob 目录") {
		t.Fatalf("空 blob 目录=%v", err)
	}
}

func TestWILegacyMigrationRejectsMissingTables(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "source.sqlite")
	targetPath := filepath.Join(root, "target.sqlite")
	// 空库（无 F3 表）：执行一次写入让文件真实生成。
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(sourcePath))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE placeholder (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = MigrateLegacySQLite(context.Background(), wiMigrationOptions(sourcePath, targetPath, filepath.Join(root, "sb"), filepath.Join(root, "tb")))
	if err == nil || !strings.Contains(err.Error(), "缺少必需表") {
		t.Fatalf("缺表=%v", err)
	}
}

func TestWILegacyMigrationRejectsBrokenReferences(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "source.sqlite")
	targetPath := filepath.Join(root, "target.sqlite")
	sourceBlobs := filepath.Join(root, "source-blobs")
	targetBlobs := filepath.Join(root, "target-blobs")
	if err := os.MkdirAll(sourceBlobs, 0o750); err != nil {
		t.Fatal(err)
	}
	db := newWILegacySourceDB(t, sourcePath)
	defer db.Close()
	// 审计行存在，但 payload ref 指向不存在的 blob → 引用完整性校验失败。
	// （源是旧库，构造数据时显式关闭外键强制。）
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO audit_logs (id,trace_id,traffic_source,method,path,audit_outcome,sample_bucket,sample_reason,started_at,ended_at,created_at) VALUES ('log-1','trace-1','gateway','GET','/v1/test','gateway_succeeded',1,'test','2026-01-01T00:00:00Z','2026-01-01T00:00:01Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO audit_payload_refs (id,audit_log_id,part_type,sequence_index,body_blob_id,raw_size_bytes,compressed_size_bytes,capture_status,created_at) VALUES ('ref-1','log-1','gateway_response',0,'missing-blob',1,1,'complete','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	_, err := MigrateLegacySQLite(context.Background(), wiMigrationOptions(sourcePath, targetPath, sourceBlobs, targetBlobs))
	if err == nil {
		t.Fatal("坏引用必须报错")
	}
	// 目标库不应产生完整迁移结果。
	if _, err := os.Stat(targetPath); err != nil {
		t.Fatalf("target=%v", err)
	}
}

func TestWILegacyMigrationDetectsMismatchedRows(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "source.sqlite")
	targetPath := filepath.Join(root, "target.sqlite")
	sourceBlobs := filepath.Join(root, "source-blobs")
	targetBlobs := filepath.Join(root, "target-blobs")
	if err := os.MkdirAll(sourceBlobs, 0o750); err != nil {
		t.Fatal(err)
	}
	db := newWILegacySourceDB(t, sourcePath)
	if _, err := db.Exec(`INSERT INTO audit_logs (id,trace_id,traffic_source,method,path,audit_outcome,sample_bucket,sample_reason,started_at,ended_at,created_at) VALUES ('log-1','trace-1','gateway','GET','/v1/test','gateway_succeeded',1,'test','2026-01-01T00:00:00Z','2026-01-01T00:00:01Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// 目标预置同主键但字段不同的行 → INSERT OR IGNORE 后 EXCEPT 校验发现不一致。
	target, err := sql.Open("sqlite", targetSQLiteDSN(targetPath))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := target.Exec(sqliteSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := target.Exec(`INSERT INTO audit_logs (id,trace_id,traffic_source,method,path,audit_outcome,sample_bucket,sample_reason,started_at,ended_at,created_at) VALUES ('log-1','different','gateway','POST','/v1/other','gateway_succeeded',1,'test','2026-01-01T00:00:00Z','2026-01-01T00:00:01Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = MigrateLegacySQLite(context.Background(), wiMigrationOptions(sourcePath, targetPath, sourceBlobs, targetBlobs))
	if err == nil || !strings.Contains(err.Error(), "字段不一致") {
		t.Fatalf("字段不一致必须报错: %v", err)
	}
	if !errors.Is(context.Canceled, context.Canceled) {
		t.Fatal("unreachable")
	}
}

package auditlog

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMigrateLegacySQLiteRequiresStopGates(t *testing.T) {
	_, err := MigrateLegacySQLite(context.Background(), LegacyMigrationOptions{SourceDatabasePath: "source.sqlite", TargetDatabasePath: "target.sqlite"})
	if err == nil || !containsText(err.Error(), "停机") {
		t.Fatalf("expected shutdown gate error, got %v", err)
	}
}

func TestMigrateLegacySQLiteCopiesRowsAndBlob(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "source.sqlite")
	targetPath := filepath.Join(root, "target.sqlite")
	sourceBlobs := filepath.Join(root, "source-blobs")
	targetBlobs := filepath.Join(root, "target-blobs")
	if err := os.MkdirAll(filepath.Join(sourceBlobs, "aa"), 0o750); err != nil {
		t.Fatal(err)
	}
	raw := []byte("legacy payload")
	digest := sha256.Sum256(raw)
	storageKey := "aa/payload.blob"
	if err := os.WriteFile(filepath.Join(sourceBlobs, filepath.FromSlash(storageKey)), raw, 0o640); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(sourcePath))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(sqliteSchema); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO audit_logs (id,trace_id,traffic_source,method,path,audit_outcome,sample_bucket,sample_reason,started_at,ended_at,created_at) VALUES ('log-1','trace-1','gateway','GET','/v1/test','gateway_succeeded',1,'test','2026-01-01T00:00:00Z','2026-01-01T00:00:01Z','2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO audit_payload_blobs (id,sha256,raw_size_bytes,compressed_size_bytes,content_type,compression,storage_key,ref_count,first_seen_at,last_seen_at,created_at) VALUES ('blob-1',?,?,?,?,?,?,1,?,?,?)`, hex.EncodeToString(digest[:]), len(raw), len(raw), "text/plain", "none", storageKey, "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO audit_payload_refs (id,audit_log_id,part_type,sequence_index,body_blob_id,raw_size_bytes,compressed_size_bytes,capture_status,created_at) VALUES ('ref-1','log-1','gateway_response',0,'blob-1',?,?, 'complete','2026-01-01T00:00:00Z')`, len(raw), len(raw))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	result, err := MigrateLegacySQLite(context.Background(), LegacyMigrationOptions{
		SourceDatabasePath: sourcePath, TargetDatabasePath: targetPath,
		SourceBlobDirectory: sourceBlobs, TargetBlobDirectory: targetBlobs,
		NodeStopped: true, GoStopped: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.TableCounts["audit_logs"] != 1 || result.TableCounts["audit_payload_blobs"] != 1 {
		t.Fatalf("unexpected migration counts: %+v", result.TableCounts)
	}
	if _, err := os.Stat(filepath.Join(targetBlobs, filepath.FromSlash(storageKey))); err != nil {
		t.Fatalf("target blob missing: %v", err)
	}
	target, err := sql.Open("sqlite", "file:"+filepath.ToSlash(targetPath))
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	var count int
	if err := target.QueryRow(`SELECT COUNT(*) FROM audit_payload_refs`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("target payload refs=%d err=%v", count, err)
	}
}

func TestMigrateLegacySQLiteRejectsBlobHashMismatch(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "source.sqlite")
	sourceBlobs := filepath.Join(root, "source-blobs")
	if err := os.MkdirAll(sourceBlobs, 0o750); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(sourcePath))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(sqliteSchema); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO audit_payload_blobs (id,sha256,raw_size_bytes,compressed_size_bytes,content_type,compression,storage_key,first_seen_at,last_seen_at,created_at) VALUES ('blob-1','00',1,1,'text/plain','none','payload.blob','2026-01-01','2026-01-01','2026-01-01')`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceBlobs, "payload.blob"), []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	_, err = MigrateLegacySQLite(context.Background(), LegacyMigrationOptions{SourceDatabasePath: sourcePath, TargetDatabasePath: filepath.Join(root, "target.sqlite"), SourceBlobDirectory: sourceBlobs, TargetBlobDirectory: filepath.Join(root, "target-blobs"), NodeStopped: true, GoStopped: true})
	if err == nil || !containsText(err.Error(), "sha256") {
		t.Fatalf("expected blob hash error, got %v", err)
	}
}

func containsText(value, needle string) bool {
	return strings.Contains(value, needle)
}

package auditlog

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSQLiteRetentionDeletesChildrenBeforeParentAndUnreferencedBlob(t *testing.T) {
	root := t.TempDir()
	store := openSQLiteStore(t, sqliteConfig(t, root))
	defer store.Close()
	ctx := context.Background()
	lease := acquireLease(t, store)
	old := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	input := fixture("retention-old", LifecycleFinalized)
	input.CreatedAt, input.StartedAt, input.EndedAt = old, old, old
	input.AuditOutcome = AuditOutcomeGatewayFailed
	input.Success = false
	input.ErrorMessage = "old failure"
	input.Payloads = []AuditLogPayloadInput{{PartType: PayloadPartGatewayError, Body: PayloadBody{Bytes: []byte("old blob"), Present: true}}}
	if _, err := store.Persist(ctx, lease, input); err != nil {
		t.Fatal(err)
	}
	implementation := store.(*sqlStore)
	var storageKey string
	if err := implementation.db.QueryRow(`SELECT storage_key FROM audit_payload_blobs`).Scan(&storageKey); err != nil {
		t.Fatal(err)
	}
	blobPath := filepath.Join(implementation.blobDir, filepath.FromSlash(storageKey))
	if _, err := os.Stat(blobPath); err != nil {
		t.Fatal(err)
	}

	result, err := store.CleanupRetention(ctx, lease, RetentionConfig{
		SuccessHotCutoff: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		SuccessCutoff:    time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		FailureCutoff:    time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		ErrorGroupCutoff: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		BatchSize:        100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.DeletedLogs != 1 || result.DeletedPayloadBlobs != 1 {
		t.Fatalf("retention result=%+v", result)
	}
	for _, table := range []string{"audit_logs", "audit_log_attempts", "audit_payload_refs", "audit_payload_blobs", "audit_error_groups"} {
		var count int
		if err := implementation.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("table %s retained %d rows", table, count)
		}
	}
	if _, err := os.Stat(blobPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("blob file remains: %v", err)
	}
}

func TestSQLiteRetentionTrimsSuccessHotDetailsButKeepsMetadata(t *testing.T) {
	root := t.TempDir()
	store := openSQLiteStore(t, sqliteConfig(t, root))
	defer store.Close()
	ctx := context.Background()
	lease := acquireLease(t, store)
	input := fixture("retention-success-hot", LifecycleFinalized)
	created := time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	input.CreatedAt, input.StartedAt, input.EndedAt = created, created, created
	input.AuditOutcome = AuditOutcomeSuccess
	input.Success = true
	input.SampleBucket = 2000
	input.Payloads = []AuditLogPayloadInput{{PartType: PayloadPartGatewayResponse, Body: PayloadBody{Bytes: []byte("hot details"), Present: true}}}
	input.Attempts = []AuditLogAttemptInput{{AttemptIndex: 0, UpstreamMethod: "POST", UpstreamURL: "https://upstream.example", StartedAt: created}}
	if _, err := store.Persist(ctx, lease, input); err != nil {
		t.Fatal(err)
	}
	result, err := store.CleanupRetention(ctx, lease, RetentionConfig{SuccessHotCutoff: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC), SuccessCutoff: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), FailureCutoff: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), ErrorGroupCutoff: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), SuccessSampleBucketThreshold: 1000, BatchSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if result.SuccessHotTrimmed != 1 || result.DeletedLogs != 0 {
		t.Fatalf("trim result=%+v", result)
	}
	implementation := store.(*sqlStore)
	var lifecycle, capture string
	var attempts, payloads int
	if err := implementation.db.QueryRow(`SELECT lifecycle_status,capture_status,attempt_count,payload_count FROM audit_logs WHERE id=?`, input.ID).Scan(&lifecycle, &capture, &attempts, &payloads); err != nil {
		t.Fatal(err)
	}
	if lifecycle != string(LifecycleFinalized) || capture != string(AuditCaptureMetadataOnly) || attempts != 0 || payloads != 0 {
		t.Fatalf("metadata after trim: lifecycle=%s capture=%s attempts=%d payloads=%d", lifecycle, capture, attempts, payloads)
	}
	var refs, child int
	if err := implementation.db.QueryRow(`SELECT COUNT(*) FROM audit_payload_refs`).Scan(&refs); err != nil {
		t.Fatal(err)
	}
	if err := implementation.db.QueryRow(`SELECT COUNT(*) FROM audit_log_attempts`).Scan(&child); err != nil {
		t.Fatal(err)
	}
	if refs != 0 || child != 0 {
		t.Fatalf("trim retained children refs=%d attempts=%d", refs, child)
	}
}

func TestSQLiteRetentionRejectsLostLeaseWithoutMutation(t *testing.T) {
	root := t.TempDir()
	store := openSQLiteStore(t, sqliteConfig(t, root))
	defer store.Close()
	ctx := context.Background()
	lease := acquireLease(t, store)
	input := fixture("retention-lease", LifecycleFinalized)
	input.CreatedAt = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	if _, err := store.Persist(ctx, lease, input); err != nil {
		t.Fatal(err)
	}
	if err := store.ReleaseOwnerLease(ctx, lease); err != nil {
		t.Fatal(err)
	}
	_, err := store.CleanupRetention(ctx, lease, RetentionConfig{
		SuccessHotCutoff: time.Now().UTC(), SuccessCutoff: time.Now().UTC(), FailureCutoff: time.Now().UTC(), ErrorGroupCutoff: time.Now().UTC(), BatchSize: 10,
	})
	if !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("lost lease error=%v", err)
	}
	implementation := store.(*sqlStore)
	var count int
	if err := implementation.db.QueryRow(`SELECT COUNT(*) FROM audit_logs`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("lost lease mutated audit_logs, count=%d", count)
	}
}

func TestSQLiteRetentionRemovesPreexistingUnreferencedBlob(t *testing.T) {
	root := t.TempDir()
	store := openSQLiteStore(t, sqliteConfig(t, root))
	defer store.Close()
	ctx := context.Background()
	lease := acquireLease(t, store)
	implementation := store.(*sqlStore)
	if err := os.MkdirAll(implementation.blobDir, 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(implementation.blobDir, "orphan.blob")
	if err := os.WriteFile(path, []byte("orphan"), 0o640); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := implementation.db.Exec(`INSERT INTO audit_payload_blobs (id,sha256,raw_size_bytes,compressed_size_bytes,content_type,storage_key,first_seen_at,last_seen_at,created_at) VALUES (?,?,?,?,?,?,?, ?,?)`, "orphan", "orphan-hash", 6, 6, "text/plain", "orphan.blob", now, now, now); err != nil {
		t.Fatal(err)
	}
	result, err := store.CleanupRetention(ctx, lease, RetentionConfig{SuccessHotCutoff: time.Now().Add(time.Hour), SuccessCutoff: time.Now().Add(-time.Hour), FailureCutoff: time.Now().Add(-time.Hour), ErrorGroupCutoff: time.Now().Add(-time.Hour), BatchSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if result.DeletedPayloadBlobs != 1 {
		t.Fatalf("orphan cleanup result=%+v", result)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan file remains: %v", err)
	}
}

func TestPendingBlobGCNewReferencePreservesCanonicalFile(t *testing.T) {
	root := t.TempDir()
	store := openSQLiteStore(t, sqliteConfig(t, root))
	defer store.Close()
	ctx := context.Background()
	lease := acquireLease(t, store)
	implementation := store.(*sqlStore)

	old := fixture("pending-gc-old", LifecycleFinalized)
	old.Payloads = []AuditLogPayloadInput{{PartType: PayloadPartGatewayResponse, Body: PayloadBody{Bytes: []byte("shared canonical blob"), Present: true}}}
	if _, err := store.Persist(ctx, lease, old); err != nil {
		t.Fatal(err)
	}
	var blobID, storageKey string
	if err := implementation.db.QueryRow(`SELECT id,storage_key FROM audit_payload_blobs`).Scan(&blobID, &storageKey); err != nil {
		t.Fatal(err)
	}
	blobPath := filepath.Join(implementation.blobDir, filepath.FromSlash(storageKey))

	// This commits the same durable state that retention leaves before its
	// separate physical-delete phase, giving the following Persist a precise
	// deterministic interleaving to resolve.
	tx, err := implementation.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`DELETE FROM audit_payload_refs WHERE audit_log_id=?`, old.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`DELETE FROM audit_log_attempts WHERE audit_log_id=?`, old.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`DELETE FROM audit_logs WHERE id=?`, old.ID); err != nil {
		t.Fatal(err)
	}
	rows, err := implementation.unreferencedBlobRows(ctx, tx, []string{blobID}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("pending GC candidates=%+v", rows)
	}
	if err := implementation.scheduleUnreferencedBlobGC(ctx, tx, rows); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	newInput := fixture("pending-gc-new", LifecycleFinalized)
	newInput.Payloads = []AuditLogPayloadInput{{PartType: PayloadPartGatewayResponse, Body: PayloadBody{Bytes: []byte("shared canonical blob"), Present: true}}}
	if _, err := store.Persist(ctx, lease, newInput); err != nil {
		t.Fatal(err)
	}
	removed, err := implementation.cleanupScheduledBlobFile(ctx, lease, pendingBlobGCRow{blobID: blobID, storageKey: storageKey})
	if err != nil {
		t.Fatal(err)
	}
	if removed {
		t.Fatal("stale GC entry deleted a newly referenced canonical blob")
	}
	if _, err := os.Stat(blobPath); err != nil {
		t.Fatalf("new reference lost its canonical blob file: %v", err)
	}
	var refs, gcRows int
	if err := implementation.db.QueryRow(`SELECT COUNT(*) FROM audit_payload_refs WHERE audit_log_id=?`, newInput.ID).Scan(&refs); err != nil {
		t.Fatal(err)
	}
	if err := implementation.db.QueryRow(`SELECT COUNT(*) FROM audit_payload_blob_gc WHERE blob_id=?`, blobID).Scan(&gcRows); err != nil {
		t.Fatal(err)
	}
	if refs != 1 || gcRows != 0 {
		t.Fatalf("new reference/GC state refs=%d gc=%d", refs, gcRows)
	}
}

func TestRetentionBlobDeleteFailureKeepsRetryableGCLocator(t *testing.T) {
	root := t.TempDir()
	store := openSQLiteStore(t, sqliteConfig(t, root))
	defer store.Close()
	ctx := context.Background()
	lease := acquireLease(t, store)
	implementation := store.(*sqlStore)
	storageKey := "delete-failure.blob"
	path := filepath.Join(implementation.blobDir, storageKey)
	if err := os.MkdirAll(path, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "held"), []byte("block deletion"), 0o640); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := implementation.db.Exec(`INSERT INTO audit_payload_blobs (id,sha256,raw_size_bytes,compressed_size_bytes,content_type,storage_key,first_seen_at,last_seen_at,created_at) VALUES (?,?,?,?,?,?,?,?,?)`, "delete-failure", "delete-failure-hash", 1, 1, "text/plain", storageKey, now, now, now); err != nil {
		t.Fatal(err)
	}
	_, err := store.CleanupRetention(ctx, lease, RetentionConfig{SuccessHotCutoff: time.Now().Add(time.Hour), SuccessCutoff: time.Now().Add(-time.Hour), FailureCutoff: time.Now().Add(-time.Hour), ErrorGroupCutoff: time.Now().Add(-time.Hour), BatchSize: 10})
	if err == nil || !strings.Contains(err.Error(), "删除 F3 audit blob 文件失败") {
		t.Fatalf("retention delete failure error=%v", err)
	}
	var blobs, gcRows int
	if err := implementation.db.QueryRow(`SELECT COUNT(*) FROM audit_payload_blobs WHERE id='delete-failure'`).Scan(&blobs); err != nil {
		t.Fatal(err)
	}
	if err := implementation.db.QueryRow(`SELECT COUNT(*) FROM audit_payload_blob_gc WHERE blob_id='delete-failure' AND storage_key=?`, storageKey).Scan(&gcRows); err != nil {
		t.Fatal(err)
	}
	if blobs != 1 || gcRows != 1 {
		t.Fatalf("failed deletion must retain metadata and locator: blobs=%d gc=%d", blobs, gcRows)
	}
}

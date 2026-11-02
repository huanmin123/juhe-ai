package auditlog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// W47 孤儿 blob 治理回归：写侧 Persist 补偿删除 + 读侧 retention tick 的
// FS→DB 对账清扫。所有用例跑 pgtestkit 真实 PG（openPostgresStore），文件面
// 断言直接落在 store 的 blobDir 下。

func TestW47OrphanReconcileDeletesUnreferencedFiles(t *testing.T) {
	store, implementation := openPostgresStore(t)
	defer store.Close()
	ctx := context.Background()
	lease := acquireLease(t, store)
	stale := time.Now().Add(-2 * time.Hour)
	blobName, _ := canonicalBlobFixture('4', ".blob")
	blobPath := writeCanonicalBlobFile(t, implementation.blobDir, blobName, stale)
	gzName, _ := canonicalBlobFixture('5', ".gz")
	gzPath := writeCanonicalBlobFile(t, implementation.blobDir, gzName, stale)

	result, err := store.CleanupRetention(ctx, lease, w47RetentionConfig(100))
	if err != nil {
		t.Fatal(err)
	}
	if result.DeletedOrphanBlobFiles != 2 || result.DeletedPayloadBlobs != 0 {
		t.Fatalf("orphan reconcile result=%+v", result)
	}
	for _, path := range []string{blobPath, gzPath} {
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("orphan file remains: %v", statErr)
		}
	}
}

func TestW47OrphanReconcileKeepsReferencedCanonicalFile(t *testing.T) {
	store, implementation := openPostgresStore(t)
	defer store.Close()
	ctx := context.Background()
	lease := acquireLease(t, store)
	blobName, blobID := canonicalBlobFixture('6', ".blob")
	blobPath := w47InsertReferencedBlob(t, implementation, blobName, blobID)

	result, err := store.CleanupRetention(ctx, lease, w47RetentionConfig(100))
	if err != nil {
		t.Fatal(err)
	}
	if result.DeletedOrphanBlobFiles != 0 {
		t.Fatalf("referenced file must not be reconciled away: result=%+v", result)
	}
	if _, statErr := os.Stat(blobPath); statErr != nil {
		t.Fatalf("referenced canonical file removed: %v", statErr)
	}
	var rows int
	if err := implementation.db.QueryRow(`SELECT COUNT(*) FROM juhe_dataset.audit_payload_blobs WHERE storage_key=$1`, "sha256/"+blobName).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("referenced blob row removed, rows=%d", rows)
	}
}

func TestW47OrphanReconcileSkipsFreshWindowOrphan(t *testing.T) {
	store, implementation := openPostgresStore(t)
	defer store.Close()
	ctx := context.Background()
	lease := acquireLease(t, store)
	blobName, _ := canonicalBlobFixture('7', ".blob")
	blobPath := writeCanonicalBlobFile(t, implementation.blobDir, blobName, time.Now())

	result, err := store.CleanupRetention(ctx, lease, w47RetentionConfig(100))
	if err != nil {
		t.Fatal(err)
	}
	if result.DeletedOrphanBlobFiles != 0 {
		t.Fatalf("fresh-window orphan deleted: result=%+v", result)
	}
	if _, statErr := os.Stat(blobPath); statErr != nil {
		t.Fatalf("fresh-window orphan removed: %v", statErr)
	}
}

func TestW47OrphanReconcileIgnoresTempAndForeignFiles(t *testing.T) {
	store, implementation := openPostgresStore(t)
	defer store.Close()
	ctx := context.Background()
	lease := acquireLease(t, store)
	stale := time.Now().Add(-2 * time.Hour)
	// 临时文件形态与 blobTempPattern 一致：本进程 owner 短哈希 + fence + 随机段。
	tmpName := ".f3-audit-blob-" + shortHash("test-owner") + "-7-random.tmp"
	tmpPath := writeCanonicalBlobFile(t, implementation.blobDir, tmpName, stale)
	foreignPath := writeCanonicalBlobFile(t, implementation.blobDir, "notes.txt", stale)

	result, err := store.CleanupRetention(ctx, lease, w47RetentionConfig(100))
	if err != nil {
		t.Fatal(err)
	}
	if result.DeletedOrphanBlobFiles != 0 {
		t.Fatalf("non-canonical files deleted: result=%+v", result)
	}
	for _, path := range []string{tmpPath, foreignPath} {
		if _, statErr := os.Stat(path); statErr != nil {
			t.Fatalf("non-canonical file touched: %v", statErr)
		}
	}
}

// TestW47OrphanReconcileCursorProgressesBeyondExaminedFiles 验证跨轮游标：
// BatchSize=1 时每轮只检查一个 canonical 文件，游标越过「有表行」文件继续
// 推进，孤儿分轮删除，不永远卡在同一批文件上；走完整棵树后游标回卷。
func TestW47OrphanReconcileCursorProgressesBeyondExaminedFiles(t *testing.T) {
	store, implementation := openPostgresStore(t)
	defer store.Close()
	ctx := context.Background()
	lease := acquireLease(t, store)
	stale := time.Now().Add(-2 * time.Hour)
	rowedName, rowedID := canonicalBlobFixture('0', ".blob")
	rowedPath := w47InsertReferencedBlob(t, implementation, rowedName, rowedID)
	orphanAName, _ := canonicalBlobFixture('1', ".blob")
	orphanAPath := writeCanonicalBlobFile(t, implementation.blobDir, orphanAName, stale)
	orphanBName, _ := canonicalBlobFixture('2', ".blob")
	orphanBPath := writeCanonicalBlobFile(t, implementation.blobDir, orphanBName, stale)

	first, err := store.CleanupRetention(ctx, lease, w47RetentionConfig(1))
	if err != nil {
		t.Fatal(err)
	}
	if first.DeletedOrphanBlobFiles != 0 {
		t.Fatalf("round 1 must only examine the rowed file: %+v", first)
	}
	if _, statErr := os.Stat(rowedPath); statErr != nil {
		t.Fatalf("rowed file removed in round 1: %v", statErr)
	}
	if _, statErr := os.Stat(orphanAPath); statErr != nil {
		t.Fatalf("orphan A removed in round 1: %v", statErr)
	}

	second, err := store.CleanupRetention(ctx, lease, w47RetentionConfig(1))
	if err != nil {
		t.Fatal(err)
	}
	if second.DeletedOrphanBlobFiles != 1 {
		t.Fatalf("round 2 must delete orphan A: %+v", second)
	}
	if _, statErr := os.Stat(orphanAPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("orphan A remains after round 2: %v", statErr)
	}
	if _, statErr := os.Stat(orphanBPath); statErr != nil {
		t.Fatalf("orphan B removed in round 2: %v", statErr)
	}

	third, err := store.CleanupRetention(ctx, lease, w47RetentionConfig(1))
	if err != nil {
		t.Fatal(err)
	}
	if third.DeletedOrphanBlobFiles != 1 {
		t.Fatalf("round 3 must delete orphan B: %+v", third)
	}
	if _, statErr := os.Stat(orphanBPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("orphan B remains after round 3: %v", statErr)
	}

	// 游标回卷后重扫：有表行文件依旧保留。
	fourth, err := store.CleanupRetention(ctx, lease, w47RetentionConfig(1))
	if err != nil {
		t.Fatal(err)
	}
	if fourth.DeletedOrphanBlobFiles != 0 {
		t.Fatalf("round 4 must not delete anything: %+v", fourth)
	}
	if _, statErr := os.Stat(rowedPath); statErr != nil {
		t.Fatalf("rowed file removed after cursor wrap: %v", statErr)
	}
}

// TestW47PersistCompensatesPublishedBlobOnPostPublishFailure 构造「发布成功、
// 提交前失败」：预插同 id 不同 sha 的 blob 行，resolveBlobIDs 的 INSERT 带
// ON CONFLICT(sha256,raw_size_bytes,content_type) DO NOTHING，主键冲突不在
// 仲裁范围而必然报错——该失败臂必须补偿删除本次新发布的文件并回滚事务。
func TestW47PersistCompensatesPublishedBlobOnPostPublishFailure(t *testing.T) {
	store, implementation := openPostgresStore(t)
	defer store.Close()
	ctx := context.Background()
	lease := acquireLease(t, store)
	content := []byte("compensated body")
	digest := w47SHA256Hex(content)
	contentHash := shortHash("text/plain")
	blobID := "blob:" + digest + ":" + contentHash
	now := time.Now().UTC()
	if _, err := implementation.db.Exec(`INSERT INTO juhe_dataset.audit_payload_blobs (id,sha256,raw_size_bytes,compressed_size_bytes,content_type,storage_key,first_seen_at,last_seen_at,created_at) VALUES ($1,$2,$3,$4,'text/plain',$5,$6,$6,$6)`, blobID, strings.Repeat("f", 64), int64(len(content)), int64(len(content)), "sha256/decoy-"+contentHash+".blob", now); err != nil {
		t.Fatal(err)
	}
	input := fixture("w47-compensate", LifecycleFinalized)
	input.Payloads = []AuditLogPayloadInput{{PartType: PayloadPartGatewayError, Body: PayloadBody{Bytes: content, Present: true}, ContentType: "text/plain"}}
	_, err := store.Persist(ctx, lease, input)
	if err == nil || !strings.Contains(err.Error(), "解析已存在 audit blob 元数据失败") {
		t.Fatalf("expected resolveBlobIDs failure, got %v", err)
	}
	publishedPath := filepath.Join(implementation.blobDir, "sha256", digest+"-"+contentHash+".blob")
	if _, statErr := os.Stat(publishedPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("published blob not compensated: %v", statErr)
	}
	var count int
	if err := implementation.db.QueryRow(`SELECT COUNT(*) FROM juhe_dataset.audit_logs WHERE id=$1`, input.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("transaction must roll back, audit_logs rows=%d", count)
	}
}

// TestW47PersistCompensationKeepsExistingCanonicalBlob 验证补偿范围：publish
// 后失败的补偿只删本次新发布的文件，复用的既有 canonical 文件不删。
func TestW47PersistCompensationKeepsExistingCanonicalBlob(t *testing.T) {
	store, implementation := openPostgresStore(t)
	defer store.Close()
	ctx := context.Background()
	lease := acquireLease(t, store)
	existingContent := []byte("existing canonical body")
	existingDigest := w47SHA256Hex(existingContent)
	existingPath := filepath.Join(implementation.blobDir, "sha256", existingDigest+"-"+shortHash("text/plain")+".blob")
	first := fixture("w47-reuse-first", LifecycleFinalized)
	first.Payloads = []AuditLogPayloadInput{{PartType: PayloadPartGatewayResponse, Body: PayloadBody{Bytes: existingContent, Present: true}, ContentType: "text/plain"}}
	if _, err := store.Persist(ctx, lease, first); err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(existingPath); statErr != nil {
		t.Fatal(statErr)
	}

	newContent := []byte("new published body")
	newDigest := w47SHA256Hex(newContent)
	newBlobID := "blob:" + newDigest + ":" + shortHash("text/plain")
	now := time.Now().UTC()
	if _, err := implementation.db.Exec(`INSERT INTO juhe_dataset.audit_payload_blobs (id,sha256,raw_size_bytes,compressed_size_bytes,content_type,storage_key,first_seen_at,last_seen_at,created_at) VALUES ($1,$2,$3,$4,'text/plain',$5,$6,$6,$6)`, newBlobID, strings.Repeat("f", 64), int64(len(newContent)), int64(len(newContent)), "sha256/decoy-"+newDigest+".blob", now); err != nil {
		t.Fatal(err)
	}
	second := fixture("w47-reuse-second", LifecycleFinalized)
	second.Payloads = []AuditLogPayloadInput{
		{PartType: PayloadPartClientRequest, Body: PayloadBody{Bytes: existingContent, Present: true}, ContentType: "text/plain"},
		{PartType: PayloadPartGatewayResponse, Body: PayloadBody{Bytes: newContent, Present: true}, ContentType: "text/plain"},
	}
	_, err := store.Persist(ctx, lease, second)
	if err == nil || !strings.Contains(err.Error(), "解析已存在 audit blob 元数据失败") {
		t.Fatalf("expected resolveBlobIDs failure, got %v", err)
	}
	if _, statErr := os.Stat(existingPath); statErr != nil {
		t.Fatalf("existing canonical blob must survive compensation: %v", statErr)
	}
	newPath := filepath.Join(implementation.blobDir, "sha256", newDigest+"-"+shortHash("text/plain")+".blob")
	if _, statErr := os.Stat(newPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("newly published blob not compensated: %v", statErr)
	}
}

// w47RetentionConfig 提供 cutoff 早于当前时间、不触碰任何 audit 行的对账配置
// （用例数据只含 blob 文件面或 created_at 晚于 cutoff 的引用行）。
func w47RetentionConfig(batchSize int) RetentionConfig {
	cutoff := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	return RetentionConfig{
		SuccessHotCutoff: cutoff,
		SuccessCutoff:    cutoff,
		FailureCutoff:    cutoff,
		ErrorGroupCutoff: cutoff,
		BatchSize:        batchSize,
	}
}

// canonicalBlobFixture 构造 canonical blob 文件名与对应 content-addressed
// blob id：digest 以 digit 重复至 64 位、contentType 短哈希以 'a' 重复至 16 位，
// 与 newBlobRecord 的 id/storage_key 形态逐段一致。
func canonicalBlobFixture(digit byte, ext string) (name, blobID string) {
	digest := strings.Repeat(string(digit), 64)
	contentHash := strings.Repeat("a", 16)
	return digest + "-" + contentHash + ext, "blob:" + digest + ":" + contentHash
}

// writeCanonicalBlobFile 在 blobDir/sha256 下写入 canonical 文件并设定 mtime。
func writeCanonicalBlobFile(t *testing.T, blobDir, name string, modTime time.Time) string {
	t.Helper()
	path := filepath.Join(blobDir, "sha256", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("blob:"+name), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}
	return path
}

// w47InsertReferencedBlob 手工落一套「文件 + blob 元数据行 + payload 引用」，
// 让既有引用感知 GC 不会回收该文件，从而单独检验对账清扫对有表行文件的保留
// 语义（audit_logs 行 created_at 为当前时间，不会被本轮 cutoff 触碰）。
func w47InsertReferencedBlob(t *testing.T, implementation *sqlStore, name, blobID string) string {
	t.Helper()
	path := writeCanonicalBlobFile(t, implementation.blobDir, name, time.Now().Add(-2*time.Hour))
	auditID, traceID := "audit-"+name[:8], "trace-"+name[:8]
	now := time.Now().UTC()
	if _, err := implementation.db.Exec(`INSERT INTO juhe_dataset.audit_logs (id,trace_id,traffic_source,method,path,audit_outcome,sample_bucket,sample_reason,capture_status,lifecycle_status,started_at,ended_at,created_at) VALUES ($1,$2,'gateway','POST','/v1/w47','success',1,'test','complete','finalized',$3,$3,$3)`, auditID, traceID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := implementation.db.Exec(`INSERT INTO juhe_dataset.audit_payload_blobs (id,sha256,raw_size_bytes,compressed_size_bytes,content_type,storage_key,first_seen_at,last_seen_at,created_at) VALUES ($1,$2,5,5,'text/plain',$3,$4,$4,$4)`, blobID, strings.Repeat("e", 64), "sha256/"+name, now); err != nil {
		t.Fatal(err)
	}
	if _, err := implementation.db.Exec(`INSERT INTO juhe_dataset.audit_payload_refs (id,audit_log_id,part_type,sequence_index,headers_blob_id,capture_status,created_at) VALUES ($1,$2,'gateway_response',0,$3,'complete',$4)`, "ref-"+name[:8], auditID, blobID, now); err != nil {
		t.Fatal(err)
	}
	return path
}

func w47SHA256Hex(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

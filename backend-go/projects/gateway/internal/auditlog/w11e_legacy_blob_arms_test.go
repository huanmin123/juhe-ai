package auditlog

// w11e auditlog 第二批错误臂：legacy SQLite 迁移前置校验、blob 压缩与
// 物理文件一致性、hot-search 目录不可用时的失败关闭。

import (
	"context"
	"crypto/rand"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// w11ePayloadDegraded 断言该审计记录的 payload 已降级为 hash_only 且无
// body blob 引用（缺失/截断 blob 的降级契约）。
func w11ePayloadDegraded(t *testing.T, implementation *sqlStore, auditLogID string) bool {
	t.Helper()
	var bodyBlobID, bodySHA sql.NullString
	var captureStatus string
	if err := implementation.db.QueryRow(`SELECT body_blob_id, body_sha256, capture_status
		FROM juhe_dataset.audit_payload_refs WHERE audit_log_id=$1`, auditLogID).
		Scan(&bodyBlobID, &bodySHA, &captureStatus); err != nil {
		t.Fatalf("读取降级 payload ref 失败: %v", err)
	}
	return (!bodyBlobID.Valid || bodyBlobID.String == "") && bodySHA.Valid && bodySHA.String != "" && captureStatus == string(PayloadCaptureHashOnly)
}

func TestW11ECompressAndBlobRecordArms(t *testing.T) {
	// 小于阈值不压缩。
	small, compression, err := compressAuditBlob([]byte("{}"), "application/json", "")
	if err != nil || compression != "none" || string(small) != "{}" {
		t.Fatalf("小 blob=%q %s err=%v", small, compression, err)
	}
	// 不可压缩内容（随机字节 + json 类型）。
	random := make([]byte, 8*1024)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	stored, compression, err := compressAuditBlob(random, "application/json", "")
	if err != nil || compression != "none" {
		t.Fatalf("随机内容 compression=%s err=%v", compression, err)
	}
	// 可压缩内容。
	large := []byte(strings.Repeat(`{"w11e":"payload"}`, 512))
	stored, compression, err = compressAuditBlob(large, "application/json", "")
	if err != nil || compression != "gzip" || len(stored) >= len(large) {
		t.Fatalf("可压缩内容 compression=%s stored=%d raw=%d err=%v", compression, len(stored), len(large), err)
	}
	// 类型与编码判定。
	cases := []struct {
		contentType, encoding string
		want                  bool
	}{
		{"application/json", "", true},
		{"text/plain", "identity", true},
		{"application/xml", "", true},
		{"text/event-stream", "", true},
		{"application/javascript", "", true},
		{"application/x-www-form-urlencoded", "", true},
		{"image/png", "", false},
		{"application/json", "gzip", false},
		{"application/json", "br", false},
	}
	for _, tc := range cases {
		if got := isCompressibleAuditPayload(tc.contentType, tc.encoding); got != tc.want {
			t.Fatalf("isCompressible(%q,%q)=%t want=%t", tc.contentType, tc.encoding, got, tc.want)
		}
	}
	// newBlobRecord 默认类型与后缀。
	record, storedBytes, err := newBlobRecord([]byte("w11e-small"), "  ", "")
	if err != nil || record.contentType != "application/octet-stream" || record.compression != "none" || string(storedBytes) != "w11e-small" {
		t.Fatalf("record=%+v err=%v", record, err)
	}
	if !strings.HasSuffix(record.storageKey, ".blob") || !strings.HasPrefix(record.id, "blob:") {
		t.Fatalf("storageKey=%q id=%q", record.storageKey, record.id)
	}
	// headers 编码往返。
	encoded, err := marshalNodeHeaders(map[string]HeaderValues{"x-api-key": {Values: []string{"v"}, Array: false}})
	if err != nil || !strings.Contains(string(encoded), "x-api-key") {
		t.Fatalf("headers=%q err=%v", encoded, err)
	}
}

func TestW11EBlobPhysicalConsistencyArms(t *testing.T) {
	store, implementation := openPostgresStore(t)
	defer store.Close()
	root := filepath.Dir(implementation.blobDir)
	ctx := context.Background()
	lease := acquireLease(t, store)
	body := strings.Repeat("w11e-payload-", 512)
	first := fixture("w11e-blob-first", LifecycleFinalized)
	first.AuditOutcome = AuditOutcomeGatewayFailed
	first.Success = false
	first.ErrorMessage = "w11e failure"
	first.Payloads = []AuditLogPayloadInput{{PartType: PayloadPartGatewayError, ContentType: "application/json", Body: PayloadBody{Bytes: []byte(body), Present: true}}}
	if _, err := store.Persist(ctx, lease, first); err != nil {
		t.Fatal(err)
	}
	var storageKey string
	if err := implementation.db.QueryRow(`SELECT storage_key FROM juhe_dataset.audit_payload_blobs`).Scan(&storageKey); err != nil {
		t.Fatal(err)
	}
	blobPath := filepath.Join(implementation.blobDir, filepath.FromSlash(storageKey))
	// 物理文件尺寸与元数据不一致 → 本次 payload 降级为 hash_only，不阻塞
	// 审计主记录（2026-11-09 去 fsync 后的降级契约）。
	if err := os.WriteFile(blobPath, []byte("truncated"), 0o640); err != nil {
		t.Fatal(err)
	}
	second := fixture("w11e-blob-second", LifecycleFinalized)
	second.AuditOutcome = AuditOutcomeGatewayFailed
	second.Success = false
	second.ErrorMessage = "w11e failure"
	second.Payloads = first.Payloads
	second.Path = "/v1/w11e-blob-second"
	if _, err := store.Persist(ctx, lease, second); err != nil {
		t.Fatalf("物理不一致应降级 payload 而非拒绝 Persist: %v", err)
	}
	if !w11ePayloadDegraded(t, implementation, second.ID) {
		t.Fatal("截断 blob 的 payload 必须降级为 hash_only")
	}
	// 元数据存在但物理文件缺失 → 同样降级，不阻塞后续 Persist。
	if err := os.Remove(blobPath); err != nil {
		t.Fatal(err)
	}
	third := fixture("w11e-blob-third", LifecycleFinalized)
	third.AuditOutcome = AuditOutcomeGatewayFailed
	third.Success = false
	third.ErrorMessage = "w11e failure"
	third.Payloads = first.Payloads
	third.Path = "/v1/w11e-blob-third"
	if _, err := store.Persist(ctx, lease, third); err != nil {
		t.Fatalf("缺失物理文件应降级 payload 而非拒绝 Persist: %v", err)
	}
	if !w11ePayloadDegraded(t, implementation, third.ID) {
		t.Fatal("缺失 blob 的 payload 必须降级为 hash_only")
	}
	// blob 根目录被文件占用 → 写临时文件失败。
	if _, err := implementation.db.Exec(`DELETE FROM juhe_dataset.audit_payload_refs WHERE audit_log_id LIKE 'w11e-blob-%'`); err != nil {
		t.Fatal(err)
	}
	if _, err := implementation.db.Exec(`DELETE FROM juhe_dataset.audit_logs WHERE id LIKE 'w11e-blob-%'`); err != nil {
		t.Fatal(err)
	}
	if _, err := implementation.db.Exec(`DELETE FROM juhe_dataset.audit_payload_blobs`); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(root, "blob-root-blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	implementation.blobDir = blocker
	blockerInput := fixture("w11e-blob-blocker", LifecycleFinalized)
	blockerInput.AuditOutcome = AuditOutcomeGatewayFailed
	blockerInput.Success = false
	blockerInput.ErrorMessage = "w11e failure"
	blockerInput.Payloads = first.Payloads
	blockerInput.Path = "/v1/w11e-blob-blocker"
	if _, err := store.Persist(ctx, lease, blockerInput); err == nil {
		t.Fatal("被占用 blob 根目录必须失败")
	}
}

func TestW11EHotSearchDirectoryFailureArms(t *testing.T) {
	store, implementation := openPostgresStore(t)
	defer store.Close()
	ctx := context.Background()
	lease := acquireLease(t, store)
	// hotDir 指向文件 → 追加与清理都失败关闭。
	blocker := filepath.Join(filepath.Dir(implementation.hotDir), "hot-blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	implementation.hotDir = blocker
	input := fixture("w11e-hot", LifecycleFinalized)
	input.ErrorMessage = "w11e message"
	if _, err := store.AppendHotSearch(ctx, lease, []AuditLogInput{input}); err == nil {
		t.Fatal("hotDir 被占用时追加必须失败")
	}
	// Windows 上对文件路径 ReadDir 表现为 ErrNotExist，清理按空目录处理为 0。
	if deleted, err := store.CleanupHotSearch(ctx, lease, time.Now().UTC(), 10); err != nil || deleted != 0 {
		t.Fatalf("hotDir 不可读但存在时清理必须无副作用: deleted=%d err=%v", deleted, err)
	}
}

func TestW11EPersistContextArms(t *testing.T) {
	store, _ := openPostgresStore(t)
	defer store.Close()
	lease := acquireLease(t, store)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	input := fixture("w11e-canceled", LifecycleFinalized)
	if _, err := store.Persist(canceled, lease, input); err == nil {
		t.Fatal("canceled Persist 必须失败")
	}
	if err := store.CleanupOwnedBlobTemps(canceled, lease, time.Now().UTC()); err == nil {
		t.Fatal("canceled temps cleanup 必须失败")
	}
	if err := store.CleanupOrphanedBlobTemps(canceled, lease, time.Now().UTC()); err == nil {
		t.Fatal("canceled orphan cleanup 必须失败")
	}
	if _, err := store.CleanupHotSearch(canceled, lease, time.Now().UTC(), 1); err == nil {
		t.Fatal("canceled hot cleanup 必须失败")
	}
	if _, err := store.AppendHotSearch(canceled, lease, []AuditLogInput{input}); err == nil {
		t.Fatal("canceled hot append 必须失败")
	}
	lease2, _, err := store.AcquireOwnerLease(context.Background(), "w11e-other", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	_ = lease2
}

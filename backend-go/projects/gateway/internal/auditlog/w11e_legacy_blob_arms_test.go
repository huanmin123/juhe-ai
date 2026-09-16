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

func TestW11ELegacyMigrationPreflightArms(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	base := LegacyMigrationOptions{NodeStopped: true, GoStopped: true, SourceDatabasePath: filepath.Join(root, "source.sqlite3"), TargetDatabasePath: filepath.Join(root, "target.sqlite3"), SourceBlobDirectory: filepath.Join(root, "source-blobs"), TargetBlobDirectory: filepath.Join(root, "target-blobs")}
	// 未停机。
	notStopped := base
	notStopped.NodeStopped = false
	if _, err := MigrateLegacySQLite(ctx, notStopped); err == nil || !strings.Contains(err.Error(), "停机") {
		t.Fatalf("未停机必须拒绝: %v", err)
	}
	// 缺路径。
	missingPath := base
	missingPath.TargetDatabasePath = " "
	if _, err := MigrateLegacySQLite(ctx, missingPath); err == nil || !strings.Contains(err.Error(), "source 和 target") {
		t.Fatalf("缺路径必须拒绝: %v", err)
	}
	// 源与目标同一文件。
	sameFile := filepath.Join(root, "same.sqlite3")
	if err := os.WriteFile(sameFile, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	same := base
	same.SourceDatabasePath, same.TargetDatabasePath = sameFile, sameFile
	if _, err := MigrateLegacySQLite(ctx, same); err == nil || !strings.Contains(err.Error(), "同一文件") {
		t.Fatalf("同文件必须拒绝: %v", err)
	}
	// 缺源文件。
	missingSource := base
	if _, err := MigrateLegacySQLite(ctx, missingSource); err == nil || !strings.Contains(err.Error(), "源文件失败") {
		t.Fatalf("缺源文件必须拒绝: %v", err)
	}
	// 空源库（缺 legacy 表）。
	emptySource := filepath.Join(root, "empty.sqlite3")
	emptyDB, err := sql.Open("sqlite", "file:"+emptySource+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := emptyDB.Exec(`CREATE TABLE unrelated (id TEXT)`); err != nil {
		t.Fatal(err)
	}
	_ = emptyDB.Close()
	empty := base
	empty.SourceDatabasePath = emptySource
	if _, err := MigrateLegacySQLite(ctx, empty); err == nil || !strings.Contains(err.Error(), "缺少必需表") {
		t.Fatalf("缺 legacy 表必须拒绝: %v", err)
	}
	// 缺 blob 目录参数。
	noBlobs := base
	noBlobs.TargetBlobDirectory = " "
	legacy := filepath.Join(root, "legacy.sqlite3")
	legacyDB, err := sql.Open("sqlite", "file:"+legacy+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range legacyAuditTables {
		if _, err := legacyDB.Exec("CREATE TABLE " + table.name + " (" + table.columns + ")"); err != nil {
			t.Fatalf("建表 %s: %v", table.name, err)
		}
	}
	_ = legacyDB.Close()
	noBlobs.SourceDatabasePath = legacy
	if _, err := MigrateLegacySQLite(ctx, noBlobs); err == nil || !strings.Contains(err.Error(), "blob 目录") {
		t.Fatalf("缺 blob 目录必须拒绝: %v", err)
	}
	// source 与 target blob 目录相同。
	sameBlobs := base
	sameBlobs.SourceDatabasePath = legacy
	sameBlobs.TargetBlobDirectory = sameBlobs.SourceBlobDirectory
	if _, err := MigrateLegacySQLite(ctx, sameBlobs); err == nil || !strings.Contains(err.Error(), "不得相同") {
		t.Fatalf("相同 blob 目录必须拒绝: %v", err)
	}
	// 源 blob 文件缺失。
	blobRoot := filepath.Join(root, "legacy-blobs")
	if err := os.MkdirAll(filepath.Join(blobRoot, "sha256"), 0o750); err != nil {
		t.Fatal(err)
	}
	legacyDB, err = sql.Open("sqlite", "file:"+legacy+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacyDB.Exec(`INSERT INTO audit_payload_blobs (id,sha256,raw_size_bytes,compressed_size_bytes,compression,storage_key) VALUES ('b1','deadbeef',1,1,'none','sha256/missing.bin')`); err != nil {
		t.Fatal(err)
	}
	_ = legacyDB.Close()
	missingBlob := base
	missingBlob.SourceDatabasePath = legacy
	missingBlob.SourceBlobDirectory = blobRoot
	if _, err := MigrateLegacySQLite(ctx, missingBlob); err == nil || !strings.Contains(err.Error(), "校验失败") {
		t.Fatalf("缺源 blob 文件必须拒绝: %v", err)
	}
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
	root := t.TempDir()
	store := openSQLiteStore(t, sqliteConfig(t, root))
	defer store.Close()
	ctx := context.Background()
	lease := acquireLease(t, store)
	implementation := store.(*sqlStore)
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
	if err := implementation.db.QueryRow(`SELECT storage_key FROM audit_payload_blobs`).Scan(&storageKey); err != nil {
		t.Fatal(err)
	}
	blobPath := filepath.Join(implementation.blobDir, filepath.FromSlash(storageKey))
	// 物理文件尺寸与元数据不一致 → 二次持久化拒绝。
	if err := os.WriteFile(blobPath, []byte("truncated"), 0o640); err != nil {
		t.Fatal(err)
	}
	second := fixture("w11e-blob-second", LifecycleFinalized)
	second.AuditOutcome = AuditOutcomeGatewayFailed
	second.Success = false
	second.ErrorMessage = "w11e failure"
	second.Payloads = first.Payloads
	if _, err := store.Persist(ctx, lease, second); err == nil || !strings.Contains(err.Error(), "不一致") {
		t.Fatalf("物理不一致必须拒绝: %v", err)
	}
	// 元数据存在但物理文件缺失 → 拒绝伪造。
	if err := os.Remove(blobPath); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Persist(ctx, lease, second); err == nil || !strings.Contains(err.Error(), "缺少物理文件") {
		t.Fatalf("缺失物理文件必须拒绝: %v", err)
	}
	// blob 根目录被文件占用 → 写临时文件失败。
	if _, err := implementation.db.Exec(`DELETE FROM audit_payload_refs WHERE audit_log_id LIKE 'w11e-blob-%'`); err != nil {
		t.Fatal(err)
	}
	if _, err := implementation.db.Exec(`DELETE FROM audit_logs WHERE id LIKE 'w11e-blob-%'`); err != nil {
		t.Fatal(err)
	}
	if _, err := implementation.db.Exec(`DELETE FROM audit_payload_blobs`); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(root, "blob-root-blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	implementation.blobDir = blocker
	if _, err := store.Persist(ctx, lease, second); err == nil {
		t.Fatal("被占用 blob 根目录必须失败")
	}
}

func TestW11EHotSearchDirectoryFailureArms(t *testing.T) {
	root := t.TempDir()
	store := openSQLiteStore(t, sqliteConfig(t, root))
	defer store.Close()
	ctx := context.Background()
	lease := acquireLease(t, store)
	implementation := store.(*sqlStore)
	// hotDir 指向文件 → 追加与清理都失败关闭。
	blocker := filepath.Join(root, "hot-blocker")
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
	store := openSQLiteStore(t, sqliteConfig(t, t.TempDir()))
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

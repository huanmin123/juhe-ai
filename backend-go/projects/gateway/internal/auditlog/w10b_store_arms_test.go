package auditlog

// w10b SQLite error-arm coverage: writeBlobTemps / publishBlobPlans /
// verifyExistingBlobFiles failure arms, stale-temp cleanup matrices, closed
// store error arms, hot-search error arms and pure helper matrices that the
// existing suite leaves uncovered.

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
	"unicode/utf16"
)

func w10bSQLiteStore(t *testing.T) (Store, *sqlStore) {
	t.Helper()
	store := openSQLiteStore(t, sqliteConfig(t, t.TempDir()))
	t.Cleanup(func() { _ = store.Close() })
	return store, store.(*sqlStore)
}

func w10bLease(t *testing.T, store Store) OwnerLease {
	t.Helper()
	lease, acquired, err := store.AcquireOwnerLease(context.Background(), "w10b-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire lease: %t %v", acquired, err)
	}
	return lease
}

func w10bBlobStorageKey(body []byte, contentType string) string {
	sum := sha256.Sum256(body)
	return fmt.Sprintf("sha256/%s-%s.blob", hex.EncodeToString(sum[:]), shortHash(contentType))
}

func TestW10BWriteBlobTempsErrorArms(t *testing.T) {
	// 最终路径是目录 → 已发布 blob 与待写元数据不一致。
	_, implementation := w10bSQLiteStore(t)
	root := t.TempDir()
	implementation.blobDir = root
	lease := OwnerLease{OwnerID: "w10b-temps", FenceToken: 7}
	body := []byte("w10b temps body")
	key := w10bBlobStorageKey(body, "application/octet-stream")
	dirAsBlob := filepath.Join(root, filepath.FromSlash(key))
	if err := os.MkdirAll(dirAsBlob, 0o750); err != nil {
		t.Fatal(err)
	}
	plans := []blobPlan{{root: root, record: blobRecord{id: "blob:x", storageKey: key, compressedSize: int64(len(body))}, rawBytes: body, bytes: body}}
	if err := implementation.writeBlobTemps(lease, plans); err == nil || !strings.Contains(err.Error(), "不一致") {
		t.Fatalf("目录 blob 必须报不一致: %v", err)
	}

	// sha256 中间路径是文件 → MkdirAll 失败。
	fileRoot := t.TempDir()
	implementation.blobDir = fileRoot
	if err := os.WriteFile(filepath.Join(fileRoot, "sha256"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	plans = []blobPlan{{root: fileRoot, record: blobRecord{id: "blob:x", storageKey: key, compressedSize: int64(len(body))}, rawBytes: body, bytes: body}}
	err := implementation.writeBlobTemps(lease, plans)
	if err == nil {
		t.Fatal("MkdirAll 失败必须报错")
	}
	if !strings.Contains(err.Error(), "子目录") && !strings.Contains(err.Error(), "检查") {
		t.Fatalf("意外错误形态: %v", err)
	}
}

func TestW10BPublishBlobPlansErrorArms(t *testing.T) {
	root := t.TempDir()
	// rename 失败 + 目标不存在 → 原子发布失败。
	temp := filepath.Join(t.TempDir(), "missing-dir", "blob.tmp")
	plans := []blobPlan{{root: root, record: blobRecord{id: "blob:x", storageKey: "sha256/a.blob", compressedSize: 1}, tempPath: temp}}
	if err := publishBlobPlans(plans); err == nil || !strings.Contains(err.Error(), "原子发布") {
		t.Fatalf("缺失 temp 必须报原子发布失败: %v", err)
	}
	// rename 失败 + 目标是目录 → 并发发布不一致。
	temp2 := filepath.Join(root, "blob.tmp")
	if err := os.WriteFile(temp2, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirBlob := filepath.Join(root, "sha256", "b.blob")
	if err := os.MkdirAll(dirBlob, 0o750); err != nil {
		t.Fatal(err)
	}
	plans = []blobPlan{{root: root, record: blobRecord{id: "blob:y", storageKey: "sha256/b.blob", compressedSize: 1}, tempPath: temp2}}
	if err := publishBlobPlans(plans); err == nil || !strings.Contains(err.Error(), "并发发布") {
		t.Fatalf("目录目标必须报并发发布不一致: %v", err)
	}
	if _, err := os.Stat(temp2); err != nil {
		t.Fatalf("temp 仍应存在以便清理: %v", err)
	}
	// 另一进程赢得内容寻址发布：目标为合法文件 → 只清理自己的 temp。
	winner := filepath.Join(root, "sha256", "c.blob")
	if err := os.WriteFile(winner, []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	temp3 := filepath.Join(t.TempDir(), "blob.tmp")
	if err := os.WriteFile(temp3, []byte("z"), 0o644); err != nil {
		t.Fatal(err)
	}
	plans = []blobPlan{{root: root, record: blobRecord{id: "blob:z", storageKey: "sha256/c.blob", compressedSize: 1}, tempPath: temp3}}
	if err := publishBlobPlans(plans); err != nil {
		t.Fatalf("已发布同尺寸文件必须被接受: %v", err)
	}
	if _, err := os.Stat(temp3); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("竞争失败的 temp 必须删除: %v", err)
	}
}

func TestW10BVerifyExistingBlobFilesArms(t *testing.T) {
	root := t.TempDir()
	// existing blob 缺少物理文件。
	plans := []blobPlan{{root: root, record: blobRecord{id: "blob:m", storageKey: "sha256/m.blob", compressedSize: 1}, existing: true}}
	if err := verifyExistingBlobFiles(plans); err == nil || !strings.Contains(err.Error(), "缺少物理文件") {
		t.Fatalf("缺失 existing 文件必须报错: %v", err)
	}
	// 文件存在但大小不一致。
	path := filepath.Join(root, "sha256")
	if err := os.MkdirAll(path, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "s.blob"), []byte("xyz"), 0o644); err != nil {
		t.Fatal(err)
	}
	plans = []blobPlan{{root: root, record: blobRecord{id: "blob:s", storageKey: "sha256/s.blob", compressedSize: 1}, existing: true}}
	if err := verifyExistingBlobFiles(plans); err == nil || !strings.Contains(err.Error(), "不一致") {
		t.Fatalf("大小不一致必须报错: %v", err)
	}
	// 同一 key 去重后只校验一次。
	plans = []blobPlan{
		{root: root, record: blobRecord{id: "blob:ok", storageKey: "sha256/s.blob", compressedSize: 3}, existing: true},
		{root: root, record: blobRecord{id: "blob:ok", storageKey: "sha256/s.blob", compressedSize: 3}, existing: true},
	}
	if err := verifyExistingBlobFiles(plans); err != nil {
		t.Fatalf("匹配文件必须通过: %v", err)
	}
}

func TestW10BStaleTempCleanupMatrices(t *testing.T) {
	root := t.TempDir()
	old := time.Now().Add(-2 * time.Hour)
	// cleanupOwnedStaleBlobTemps：前缀匹配 + 过期删除；新文件保留；非 .tmp 跳过。
	ownedOld := filepath.Join(root, ".f3-audit-blob-abc-7-a.tmp")
	ownedNew := filepath.Join(root, ".f3-audit-blob-abc-7-b.tmp")
	otherFence := filepath.Join(root, ".f3-audit-blob-abc-8-c.tmp")
	notTmp := filepath.Join(root, ".f3-audit-blob-abc-7-d.txt")
	future := time.Now().Add(30 * time.Minute)
	for _, path := range []string{ownedOld, ownedNew, otherFence, notTmp} {
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if path == ownedNew {
			// 新文件用未来 modTime，避免与 cutoff=time.Now() 竞争。
			if err := os.Chtimes(path, future, future); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := cleanupOwnedStaleBlobTemps(root, "abc-7", time.Now()); err != nil {
		t.Fatalf("owned 清理: %v", err)
	}
	if _, err := os.Stat(ownedOld); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("过期 owned temp 必须删除: %v", err)
	}
	for _, keep := range []string{ownedNew, otherFence, notTmp} {
		if _, err := os.Stat(keep); err != nil {
			t.Fatalf("%s 不应被删除: %v", keep, err)
		}
	}
	// 目录不存在 → nil。
	if err := cleanupOwnedStaleBlobTemps(filepath.Join(root, "missing"), "abc-7", time.Now()); err != nil {
		t.Fatalf("缺失根目录: %v", err)
	}

	// cleanupOrphanedStaleBlobTemps 名称矩阵。
	names := map[string]string{
		".f3-audit-blob-x-1-e.tmp":   "orphan-old",   // fence 1 < current → 删除
		".f3-audit-blob-x-9-f.tmp":   "orphan-new",   // fence >= current → 保留
		".f3-audit-blob-x-0-g.tmp":   "fence-zero",   // fence <= 0 → 保留
		".f3-audit-blob-x-yy-h.tmp":  "fence-parse",  // fence 解析失败 → 保留
		".f3-audit-blob--1-i.tmp":    "empty-owner",  // owner 段为空 → 保留
		".f3-audit-blob-short-j.tmp": "two-segments", // 分段不足 → 保留
		".f3-audit-blob-x-1-k.txt":   "suffix",       // 非 .tmp → 保留
		"unrelated.tmp":              "prefix",       // 无前缀 → 保留
	}
	orphanOld := time.Now().Add(-3 * time.Hour)
	for name := range names {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, orphanOld, orphanOld); err != nil {
			t.Fatal(err)
		}
	}
	if err := cleanupOrphanedStaleBlobTemps(root, 5, time.Now()); err != nil {
		t.Fatalf("orphan 清理: %v", err)
	}
	for name, kind := range names {
		_, err := os.Stat(filepath.Join(root, name))
		if kind == "orphan-old" && !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("孤立旧 temp 必须删除: %s %v", name, err)
		}
		if kind != "orphan-old" && err != nil {
			t.Fatalf("%s (%s) 不应被删除: %v", name, kind, err)
		}
	}
	if err := cleanupOrphanedStaleBlobTemps(filepath.Join(root, "missing"), 5, time.Now()); err != nil {
		t.Fatalf("缺失根目录: %v", err)
	}
	// 目录形式的 temp 命名被跳过。
	if err := os.MkdirAll(filepath.Join(root, ".f3-audit-blob-x-1-dir.tmp"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := cleanupOrphanedStaleBlobTemps(root, 5, time.Now()); err != nil {
		t.Fatalf("目录条目必须跳过: %v", err)
	}
}

func TestW10BTempCleanupWithLeaseArms(t *testing.T) {
	store, implementation := w10bSQLiteStore(t)
	ctx := context.Background()
	lease := w10bLease(t, store)

	// 持有租约：文件形态根目录走一次 walk（无匹配）→ nil；缺失根目录 → nil。
	fileRoot := filepath.Join(t.TempDir(), "rootfile")
	if err := os.WriteFile(fileRoot, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	implementation.blobDir = fileRoot
	if err := store.CleanupOrphanedBlobTemps(ctx, lease, time.Now()); err != nil {
		t.Fatalf("文件根目录必须安全返回: %v", err)
	}
	implementation.blobDir = filepath.Join(t.TempDir(), "not-exists")
	if err := store.CleanupOwnedBlobTemps(ctx, lease, time.Now()); err != nil {
		t.Fatalf("缺失根目录必须安全返回: %v", err)
	}

	// 失效租约在写面前被拒绝。
	if err := store.CleanupOwnedBlobTemps(ctx, OwnerLease{}, time.Now()); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("空租约必须拒绝: %v", err)
	}
}

func TestW10BClosedStoreArms(t *testing.T) {
	// EnsureSchema 之后关闭：后续调用命中各 SQL 错误臂。
	store := openSQLiteStore(t, sqliteConfig(t, t.TempDir()))
	implementation := store.(*sqlStore)
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	ctx := context.Background()
	lease := OwnerLease{OwnerID: "w10b-closed", FenceToken: 1}
	input := fixture("w10b-closed-input", LifecycleFinalized)
	if _, _, err := store.AcquireOwnerLease(ctx, "w10b-closed", time.Minute); err == nil {
		t.Fatal("关闭后 acquire 必须失败")
	}
	if _, err := store.RenewOwnerLease(ctx, lease, time.Minute); err == nil {
		t.Fatal("关闭后 renew 必须失败")
	}
	if err := store.ReleaseOwnerLease(ctx, lease); err == nil {
		t.Fatal("关闭后 release 必须失败")
	}
	if _, err := store.Persist(ctx, lease, input); err == nil {
		t.Fatal("关闭后 Persist 必须失败")
	}
	if _, err := store.CleanupRetention(ctx, lease, RetentionConfig{BatchSize: 1}); err == nil {
		t.Fatal("关闭后 retention 必须失败")
	}
	if _, err := store.AppendHotSearch(ctx, lease, []AuditLogInput{input}); err == nil {
		t.Fatal("关闭后 AppendHotSearch 必须失败")
	}
	if _, err := store.CleanupHotSearch(ctx, lease, time.Now().UTC(), 1); err == nil {
		t.Fatal("关闭后 CleanupHotSearch 必须失败")
	}
	if _, err := implementation.cleanupScheduledBlobFiles(ctx, lease, 1); err == nil {
		t.Fatal("关闭后 pending GC 查询必须失败")
	}

	// 从未 EnsureSchema 即关闭：EnsureSchema 命中初始化失败臂。
	fresh := openSQLiteStore(t, sqliteConfig(t, t.TempDir()))
	freshImplementation := fresh.(*sqlStore)
	_ = fresh.Close()
	if err := fresh.EnsureSchema(ctx); err == nil || !strings.Contains(err.Error(), "schema 失败") {
		t.Fatalf("关闭后 EnsureSchema 必须失败: %v", err)
	}
	if err := freshImplementation.db.QueryRow("SELECT 1").Scan(new(any)); err == nil {
		t.Fatal("关闭后的 db 必须拒绝查询")
	}
}

func TestW10BHotSearchArms(t *testing.T) {
	store, implementation := w10bSQLiteStore(t)
	ctx := context.Background()
	lease := w10bLease(t, store)

	// 空输入 / 失效输入。
	if n, err := store.AppendHotSearch(ctx, lease, nil); err != nil || n != 0 {
		t.Fatalf("空输入: %d %v", n, err)
	}
	bad := fixture("w10b-hot-bad", LifecycleFinalized)
	bad.CreatedAt = "not-a-time"
	if _, err := store.AppendHotSearch(ctx, lease, []AuditLogInput{bad}); err == nil {
		t.Fatal("非法 createdAt 必须报错")
	}
	probe := fixture("w10b-hot-probe", LifecycleFinalized)
	probe.TrafficSource = TrafficSourceAccountHealthCheck
	if _, err := store.AppendHotSearch(ctx, lease, []AuditLogInput{probe}); err == nil {
		t.Fatal("探针来源必须被 validateInput 拒绝")
	}

	// 正常追加 + CleanupHotSearch 清理 + 失效租约拒绝。
	good := fixture("w10b-hot-good", LifecycleFinalized)
	if n, err := store.AppendHotSearch(ctx, lease, []AuditLogInput{good}); err != nil || n == 0 {
		t.Fatalf("追加: %d %v", n, err)
	}
	if _, err := store.CleanupHotSearch(ctx, lease, time.Time{}, 1); err == nil {
		t.Fatal("空 cutoff 必须拒绝")
	}
	if deleted, err := store.CleanupHotSearch(ctx, lease, time.Now().UTC().Add(time.Hour), 1); err != nil || deleted == 0 {
		t.Fatalf("清理: %d %v", deleted, err)
	}
	if _, err := store.CleanupHotSearch(ctx, OwnerLease{}, time.Now().UTC(), 1); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("空租约必须拒绝: %v", err)
	}

	// SearchHotSearch 剩余错误臂：ctx / 关键词 / 窗口。
	if _, err := store.SearchHotSearch(ctx, HotSearchOptions{Keywords: []string{"error"}, StartAt: time.Now(), EndAt: time.Now().Add(-time.Hour)}); err == nil {
		t.Fatal("startAt 晚于 endAt 必须拒绝")
	}
	if _, err := store.SearchHotSearch(ctx, HotSearchOptions{Keywords: []string{" "}}); err == nil {
		t.Fatal("空白关键词必须拒绝")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := store.SearchHotSearch(canceled, HotSearchOptions{Keywords: []string{"error"}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled ctx 必须浮出: %v", err)
	}
	// budget 耗尽 / 文件缺失。
	if n, err := scanHotSearchFile(ctx, filepath.Join(implementation.hotDir, "missing.ndjson"), []string{"error"}, time.Now().Add(-time.Hour), time.Now(), 0, map[string]time.Time{}); err != nil || n != 0 {
		t.Fatalf("budget=0: %d %v", n, err)
	}
	if _, err := scanHotSearchFile(ctx, filepath.Join(implementation.hotDir, "missing.ndjson"), []string{"error"}, time.Now().Add(-time.Hour), time.Now(), 1, map[string]time.Time{}); err == nil {
		t.Fatal("缺失文件必须报错")
	}
}

func TestW10BNormalizeRetentionAndHelperArms(t *testing.T) {
	// normalizeRetentionConfig 错误臂。
	base := RetentionConfig{SuccessHotCutoff: time.Now(), SuccessCutoff: time.Now(), FailureCutoff: time.Now(), ErrorGroupCutoff: time.Now()}
	if _, err := normalizeRetentionConfig(RetentionConfig{}); err == nil {
		t.Fatal("零 cutoff 必须拒绝")
	}
	hotBefore := base
	hotBefore.SuccessHotCutoff = base.SuccessCutoff.Add(-time.Minute)
	if _, err := normalizeRetentionConfig(hotBefore); err == nil {
		t.Fatal("hot cutoff 早于 success cutoff 必须拒绝")
	}
	// 边界钳制。
	clamped, err := normalizeRetentionConfig(RetentionConfig{SuccessHotCutoff: base.SuccessHotCutoff, SuccessCutoff: base.SuccessCutoff, FailureCutoff: base.FailureCutoff, ErrorGroupCutoff: base.ErrorGroupCutoff, BatchSize: 99999, SuccessSampleBucketThreshold: -5})
	if err != nil || clamped.BatchSize != 5096 || clamped.SuccessSampleBucketThreshold != 0 {
		t.Fatalf("钳制: %+v %v", clamped, err)
	}

	// derivedErrorGroup：非法 createdAt / 失败 attempt 兜底。
	input := fixture("w10b-eg", LifecycleFinalized)
	input.AuditOutcome = AuditOutcomeUpstreamFailed
	input.CreatedAt = "garbage"
	if _, err := derivedErrorGroup(input, nil); err == nil {
		t.Fatal("非法 createdAt 必须报错")
	}
	input.CreatedAt = time.Date(2026, 9, 1, 10, 7, 0, 0, time.UTC).Format(time.RFC3339Nano)
	input.Attempts = []AuditLogAttemptInput{{AttemptIndex: 1, Success: boolPtr(false), UpstreamStatusCode: intPointer(500), ErrorPhase: "upstream", ErrorCode: "boom", ErrorMessage: "failed 1234 deadbeef"}}
	group, err := derivedErrorGroup(input, nil)
	if err != nil {
		t.Fatalf("derivedErrorGroup: %v", err)
	}
	if group.windowEndedAt == "" || group.requestFingerprint == "" || group.errorFingerprint == "" {
		t.Fatalf("error group 字段缺失: %+v", group)
	}

	// normalizeErrorMessage 截断与替换。
	long := normalizeErrorMessage(strings.Repeat("z", 600))
	if len(long) != 500 {
		t.Fatalf("超长消息必须截断到 500: %d", len(long))
	}
	replaced := utf16.Decode(normalizeErrorMessage("id deadbeefdeadbeef and 12345678 x"))
	if !strings.Contains(string(replaced), "{hex}") || !strings.Contains(string(replaced), "{num}") {
		t.Fatalf("指纹替换: %q", string(replaced))
	}

	// 纯标量 helper 兜底。
	if nodeStatusCode(nil) != "" {
		t.Fatal("nil 状态码必须映射为空字符串")
	}
	if dbTimeText("garbage") != nil || nullableTime(ModeSQLite, "garbage") != nil {
		t.Fatal("非法时间必须映射为 nil")
	}
	if nullIfEmpty("") != nil || nullIfEmpty("v") != "v" {
		t.Fatal("nullIfEmpty")
	}
	var nilBool *bool
	if boolValue(nilBool) != false {
		t.Fatal("nil bool 必须映射 false")
	}
	if maxInt64(1, 2) != 2 || nonNegative(-1) != 0 {
		t.Fatal("标量 helper")
	}
	if placeholders(0) != "NULL" || strings.Count(placeholders(3), "?") != 3 {
		t.Fatal("placeholders")
	}
	if isNonPersistedTrafficSource("gateway") || !isNonPersistedTrafficSource("cooldown_retest") {
		t.Fatal("isNonPersistedTrafficSource")
	}
	if uniqueStrings([]string{"a", "", "a"})[0] != "a" || len(uniqueStrings([]string{"a", "", "a"})) != 1 {
		t.Fatal("uniqueStrings")
	}
	if blobTempOwnerKey(OwnerLease{OwnerID: "o", FenceToken: 3}) == "" || !strings.HasPrefix(blobTempPattern(OwnerLease{OwnerID: "o", FenceToken: 3}), ".f3-audit-blob-") {
		t.Fatal("blobTemp helper")
	}
	if syncBlobParent(t.TempDir()) != nil {
		t.Fatal("Windows 分支必须 no-op")
	}
}

func TestW10BPersistSQLiteArms(t *testing.T) {
	store, implementation := w10bSQLiteStore(t)
	ctx := context.Background()
	lease := w10bLease(t, store)

	// 归一化失败臂。
	invalid := fixture("w10b-persist-invalid", LifecycleFinalized)
	invalid.Path = " "
	if _, err := store.Persist(ctx, lease, invalid); err == nil {
		t.Fatal("非法输入必须报错")
	}

	// 已发布 blob 与待写元数据不一致：预创建目录形态的 canonical 文件。
	body := []byte("w10b persist body")
	key := w10bBlobStorageKey(body, "application/octet-stream")
	dirBlob := filepath.Join(implementation.blobDir, filepath.FromSlash(key))
	if err := os.MkdirAll(dirBlob, 0o750); err != nil {
		t.Fatal(err)
	}
	conflict := fixture("w10b-persist-blob-conflict", LifecycleFinalized)
	conflict.Payloads = []AuditLogPayloadInput{{PartType: PayloadPartClientRequest, ContentType: "application/octet-stream", Body: PayloadBody{Bytes: body, Present: true}}}
	if _, err := store.Persist(ctx, lease, conflict); err == nil || !strings.Contains(err.Error(), "不一致") {
		t.Fatalf("目录 canonical blob 必须报不一致: %v", err)
	}

	// in_progress → finalized 生命周期：第二次写入更新父行。
	progress := fixture("w10b-persist-lifecycle", LifecycleInProgress)
	if _, err := store.Persist(ctx, lease, progress); err != nil {
		t.Fatalf("in_progress 写入: %v", err)
	}
	final := progress
	final.LifecycleStatus = LifecycleFinalized
	if result, err := store.Persist(ctx, lease, final); err != nil || result.Ignored {
		t.Fatalf("finalized 替换: %+v %v", result, err)
	}
	// 反向（finalized 已存在，再写 in_progress）被忽略。
	late, err := store.Persist(ctx, lease, progress)
	if err != nil || !late.Ignored {
		t.Fatalf("finalized 之后的 in_progress 必须被忽略: %+v %v", late, err)
	}

	// 失效租约 Persist 在写面前被拒绝（matched!=1 臂）。
	if err := store.ReleaseOwnerLease(ctx, lease); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Persist(ctx, lease, final); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("已释放租约 Persist 必须拒绝: %v", err)
	}
}

func TestW10BPgTypedHelpers(t *testing.T) {
	// bind()：SQLite 恒返回原查询；PG 占位符替换由 PG 门禁测试覆盖。
	store := &sqlStore{mode: ModeSQLite}
	if got := store.bind("VALUES (?, ?)"); got != "VALUES (?, ?)" {
		t.Fatalf("SQLite bind: %q", got)
	}
	pgStore := &sqlStore{mode: ModePostgres}
	// bind() 不识别 SQL 字符串字面量中的 '?'（现有实现契约；生产查询不含带 ? 的字面量）。
	if got := pgStore.bind("VALUES (?, ?) WHERE a=? AND '?'"); got != "VALUES ($1, $2) WHERE a=$3 AND '$4'" {
		t.Fatalf("PG bind: %q", got)
	}
	if dbTime(ModePostgres, time.Now()) == nil || dbTime(ModeSQLite, time.Now()) == nil {
		t.Fatal("dbTime")
	}
	if nullableTime(ModePostgres, time.Now().UTC().Format(time.RFC3339Nano)) == nil {
		t.Fatal("nullableTime PG 分支")
	}
	if shouldMaintainBlobRefCount(ModePostgres) || !shouldMaintainBlobRefCount(ModeSQLite) {
		t.Fatal("ref_count 维护只属于 SQLite")
	}
	// 空 receiver / 空字段。
	var nilStore *sqlStore
	if err := nilStore.Close(); err != nil {
		t.Fatalf("nil store Close: %v", err)
	}
	empty := &sqlStore{mode: ModeSQLite}
	if leaseTable := empty.leaseTable(); leaseTable != "audit_log_owner_leases" {
		t.Fatalf("SQLite leaseTable: %q", leaseTable)
	}
	if table := empty.table("audit_logs"); table != "audit_logs" {
		t.Fatalf("SQLite table: %q", table)
	}
	var nilDB *sql.DB
	_ = nilDB
}

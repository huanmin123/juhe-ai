package auditlog

// w17a 覆盖收尾：94.8% → ≥95% 硬门槛的定向补测。只触达既有私有分支，
// 不改动生产代码。平台相关错误臂以 runtime.GOOS 显式门控：POSIX 的
// rename/reopen 语义与 Windows 不同，跳过原因在用例内注明。

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// ---- producer：DroppedTotal nil 接收者 + persistOne panic 隔离臂 ----

type w17aPanicStore struct{ fakeStore }

func (s *w17aPanicStore) Persist(context.Context, OwnerLease, AuditLogInput) (PersistResult, error) {
	panic("w17a persist boom")
}

func TestW17AProducerNilDroppedTotalAndPanicIsolation(t *testing.T) {
	var missing *Producer
	if missing.DroppedTotal() != 0 {
		t.Fatal("nil producer 的 DroppedTotal 必须返回 0")
	}

	logger := &fakeProducerLogger{warns: &[]string{}}
	producer := NewProducer(&w17aPanicStore{}, OwnerLease{}, Config{}, logger)
	producer.Capture(producerTestInput("w17a-panic"))
	deadline := time.Now().Add(5 * time.Second)
	for {
		found := false
		for _, line := range logger.snapshot() {
			if strings.Contains(line, "panic") {
				found = true
			}
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker panic 必须被隔离并告警，实际告警=%v", logger.snapshot())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ---- config：零配置默认驱动跟随 + 业务设置缺失 fail-fast 臂 ----

func TestW17ALoadConfigZeroConfigDriverArms(t *testing.T) {
	env := map[string]string{"JUHE_AI_DATABASE_DRIVER": "postgres"}
	cfg, err := LoadConfig(func(name string) string { return env[name] })
	if err == nil {
		t.Fatal("postgres 零配置且无任何业务设置与主 URL 时必须 fail-fast")
	}
	if !strings.Contains(err.Error(), "JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_PATH") {
		t.Fatalf("错误必须指向业务设置缺失: %v", err)
	}
	if cfg.Mode != "" {
		t.Fatalf("失败时必须返回零值 Config: %+v", cfg)
	}
}

// ---- owner maintenance：panic 隔离臂 + 错误遇取消上下文静默退出臂 ----

type w17aMaintStore struct {
	fakeStore
	hook func()
}

func (s *w17aMaintStore) CleanupRetention(context.Context, OwnerLease, RetentionConfig) (RetentionResult, error) {
	s.hook()
	return RetentionResult{}, errors.New("w17a retention boom")
}

func w17aFastConfig() Config {
	return Config{RetentionInterval: time.Millisecond, RetentionBatchSize: 1}
}

func TestW17ARunRetentionMaintenancePanicIsolation(t *testing.T) {
	store := &w17aMaintStore{hook: func() { panic("w17a maintenance boom") }}
	fatal := make(chan error, 1)
	done := runRetentionMaintenance(context.Background(), store, OwnerLease{OwnerID: "w17a", FenceToken: 1}, w17aFastConfig(), slog.Default(), fatal)
	select {
	case err := <-fatal:
		if err == nil || !strings.Contains(err.Error(), "panic") {
			t.Fatalf("maintenance panic 必须上报 fatal: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("maintenance panic 未上报 fatal")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("maintenance panic 后未退出")
	}
}

func TestW17ARunRetentionMaintenanceSilentExitOnCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &w17aMaintStore{hook: cancel}
	done := runRetentionMaintenance(ctx, store, OwnerLease{OwnerID: "w17a", FenceToken: 2}, w17aFastConfig(), slog.Default(), nil)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("错误且上下文已取消时必须静默退出")
	}
}

// ---- store/retention：SQLite 直连的 refs 与 blob GC 复核臂 ----

func TestW17ARetentionDirectArmsOnSQLite(t *testing.T) {
	store := openSQLiteStore(t, sqliteConfig(t, t.TempDir()))
	defer store.Close()
	impl := store.(*sqlStore)
	ctx := context.Background()
	lease := acquireLease(t, store)

	// 基线审计行，满足 refs 的外键约束。
	if _, err := store.Persist(ctx, lease, fixture("w17a-log", LifecycleFinalized)); err != nil {
		t.Fatalf("基线 persist: %v", err)
	}
	now := dbTime(ModeSQLite, time.Now().UTC())
	if _, err := impl.db.Exec(
		`INSERT INTO audit_payload_blobs (id,sha256,raw_size_bytes,compressed_size_bytes,content_type,compression,storage_key,first_seen_at,last_seen_at,created_at) VALUES ('w17a-blob-b','w17a-sha',1,1,'application/octet-stream','none','sha256/w17a-b.bin',?,?,?)`,
		now, now, now); err != nil {
		t.Fatalf("插入 blob: %v", err)
	}
	if _, err := impl.db.Exec(
		`INSERT INTO audit_payload_refs (id,audit_log_id,part_type,sequence_index,headers_blob_id,body_blob_id,capture_status,created_at) VALUES ('w17a-ref','w17a-log','client_request',0,NULL,'w17a-blob-b','complete',?)`,
		now); err != nil {
		t.Fatalf("插入 refs: %v", err)
	}

	// headers_blob_id 为 NULL：headers 分支跳过、body 计入待 GC 集合。
	tx, err := impl.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	ids, blobIDs, err := impl.deleteRetentionChildren(ctx, tx, []string{"w17a-log"}, false)
	if err != nil {
		t.Fatalf("deleteRetentionChildren: %v", err)
	}
	if len(ids) != 1 || ids[0] != "w17a-log" || len(blobIDs) != 1 || blobIDs[0] != "w17a-blob-b" {
		t.Fatalf("删除结果异常: ids=%v blobIDs=%v", ids, blobIDs)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// 候选 blob 行在复核前已消失 → ErrNoRows → 静默跳过。
	tx2, err := impl.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin2: %v", err)
	}
	if err := impl.scheduleUnreferencedBlobGC(ctx, tx2, []retentionBlobRow{{id: "w17a-ghost-blob", storageKey: "sha256/ghost.bin"}}); err != nil {
		t.Fatalf("已消失的候选 blob 必须被跳过: %v", err)
	}
	if err := tx2.Commit(); err != nil {
		t.Fatalf("commit2: %v", err)
	}
}

// ---- writeBlobTemps：路径中存在同名文件阻断子目录的报错臂 ----
// Windows 上 os.Stat 先返回 ErrNotExist、随后 MkdirAll 失败；
// POSIX 上 os.Stat 直接返回 ENOTDIR。两平台都进入各自报错臂。

func TestW17AWriteBlobTempsBlockedDirectoryArm(t *testing.T) {
	store := openSQLiteStore(t, sqliteConfig(t, t.TempDir()))
	defer store.Close()
	impl := store.(*sqlStore)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "blocker.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	plans := []blobPlan{{
		root:   root,
		record: blobRecord{storageKey: "blocker.txt/sub/x.blob", compressedSize: 1},
		bytes:  []byte("y"),
	}}
	if err := impl.writeBlobTemps(OwnerLease{OwnerID: "w17a", FenceToken: 9}, plans); err == nil {
		t.Fatal("以文件阻断 blob 子目录时必须报错")
	}
}

// ---- Windows 专属臂 ----

func TestW17AWindowsOnlyPathArms(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("filepath.Abs 仅在 Windows 拒绝内嵌 NUL 的路径，其余平台不存在该错误臂")
	}
	bad := "a" + string(rune(0)) + "b"
	if _, err := sqliteDSN(bad); err == nil {
		t.Fatal("含 NUL 的路径必须解析失败")
	}
	if ro := readOnlySQLiteDSN(bad); !strings.HasPrefix(ro, "file:") || !strings.HasSuffix(ro, "?mode=ro") {
		t.Fatalf("只读回退 DSN 异常: %q", ro)
	}
	if target := targetSQLiteDSN(bad); !strings.HasPrefix(target, "file:") || !strings.HasSuffix(target, "?_pragma=busy_timeout(5000)") {
		t.Fatalf("目标回退 DSN 异常: %q", target)
	}
	if _, err := blobFilePath(bad, "x/y.blob"); err == nil {
		t.Fatal("非法 blob 根目录必须报错")
	}
}

// publishBlobPlans 落败臂：目标 canonical 文件已存在且不可替换（Windows
// 上只读属性令 MoveFileEx 拒绝覆盖）→ rename 失败，按并发发布契约只清理
// 自己的临时文件。POSIX 上 rename 原子替换同名内容文件，用例仍然通过，
// 但覆盖不到该臂。

func TestW17ABlobPublishLostRaceArm(t *testing.T) {
	root := t.TempDir()
	key := "sha256/w17a-race.blob"
	final := filepath.Join(root, "sha256", "w17a-race.blob")
	if err := os.MkdirAll(filepath.Dir(final), 0o750); err != nil {
		t.Fatal(err)
	}
	payload := []byte("12345")
	if err := os.WriteFile(final, payload, 0o640); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		if err := os.Chmod(final, 0o444); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(final, 0o640)
	}
	temp := filepath.Join(t.TempDir(), "w17a-race.tmp")
	if err := os.WriteFile(temp, payload, 0o640); err != nil {
		t.Fatal(err)
	}
	plans := []blobPlan{{root: root, tempPath: temp, record: blobRecord{storageKey: key, compressedSize: int64(len(payload))}}}
	if err := publishBlobPlans(plans); err != nil {
		t.Fatalf("落败方必须静默清理自己的临时文件: %v", err)
	}
	if runtime.GOOS == "windows" && plans[0].tempPath != "" {
		t.Fatal("落败方的临时文件路径必须清空")
	}
	if _, err := os.Stat(temp); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("落败方的临时文件必须删除: %v", err)
	}
	got, err := os.ReadFile(final)
	if err != nil || string(got) != string(payload) {
		t.Fatalf("已发布文件不得被改写: %s %v", got, err)
	}
}

// 过期 blob 临时文件的 Remove 失败臂：Windows 上打开句柄（无
// FILE_SHARE_DELETE）阻止删除。POSIX 允许删除打开中的文件，跳过。

func TestW17AWindowsStaleTempRemoveFailureArms(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("POSIX 允许删除打开中的文件，无法触发 Remove 失败臂")
	}
	store := openSQLiteStore(t, sqliteConfig(t, t.TempDir()))
	defer store.Close()
	impl := store.(*sqlStore)
	ctx := context.Background()

	// fence 递增：先释放首租约再以新 owner 获取，保证存在 fence-1 的孤儿语义。
	first := acquireLease(t, store)
	if err := store.ReleaseOwnerLease(ctx, first); err != nil {
		t.Fatalf("释放首租约: %v", err)
	}
	reacquired, ok, err := store.AcquireOwnerLease(ctx, "w17a-orphan", time.Minute)
	if err != nil || !ok {
		t.Fatalf("重新获取租约: ok=%v err=%v", ok, err)
	}
	lease := reacquired
	if lease.FenceToken < 2 {
		t.Fatalf("抢占后 fence 必须递增: %d", lease.FenceToken)
	}
	if err := os.MkdirAll(impl.blobDir, 0o750); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Now().UTC().Add(-time.Hour)
	// 文件 mtime 必须早于 cutoff（ModTime.After(cutoff) 的文件被保留）。
	stale := cutoff.Add(-time.Hour)

	owned := filepath.Join(impl.blobDir, ".f3-audit-blob-"+blobTempOwnerKey(lease)+"-1.tmp")
	if err := os.WriteFile(owned, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(owned, stale, stale); err != nil {
		t.Fatal(err)
	}
	ownedHandle, err := os.Open(owned)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CleanupOwnedBlobTemps(ctx, lease, cutoff.Add(-time.Minute)); err == nil {
		t.Fatal("打开中的过期临时文件删除失败必须上抛")
	}
	ownedHandle.Close()
	if err := os.Remove(owned); err != nil {
		t.Fatal(err)
	}

	orphan := filepath.Join(impl.blobDir, fmt.Sprintf(".f3-audit-blob-%s-%d-stale.tmp", "abcdefabcdef", lease.FenceToken-1))
	if err := os.WriteFile(orphan, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(orphan, stale, stale); err != nil {
		t.Fatal(err)
	}
	orphanHandle, err := os.Open(orphan)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CleanupOrphanedBlobTemps(ctx, lease, cutoff.Add(-time.Minute)); err == nil {
		t.Fatal("打开中的孤儿临时文件删除失败必须上抛")
	}
	orphanHandle.Close()
	if err := os.Remove(orphan); err != nil {
		t.Fatal(err)
	}
}

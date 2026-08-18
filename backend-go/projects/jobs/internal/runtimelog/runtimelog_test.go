package runtimelog

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseLineMatchesNodeContract(t *testing.T) {
	now := time.Date(2026, 8, 8, 9, 10, 11, 0, time.UTC)
	record := ParseLine(` {"time":"not-a-timestamp","level":40,"traceId":" trace ","event":"event","msg":" message "} `, LineOptions{
		SourceKey:  "identity:7",
		LogFile:    " runtime.log ",
		LogOffset:  7,
		LineNumber: 2,
		Now:        func() time.Time { return now },
	})
	if record == nil {
		t.Fatal("有效 JSON 对象必须生成运行日志记录")
	}
	if record.ID != "rtlog_194e1917d7d800b70d8dd9313360f26e" {
		t.Fatalf("stable id 不兼容 Node: %s", record.ID)
	}
	if record.Time != "not-a-timestamp" {
		t.Fatalf("Node 应保留原始 time 字符串，实际为 %q", record.Time)
	}
	if record.Level != "warn" || record.TraceID != "trace" || record.Message != "message" {
		t.Fatalf("字段规范化不兼容 Node: %#v", record)
	}

	array := ParseLine(`["not","an","object"]`, LineOptions{Now: func() time.Time { return now }})
	if array == nil || array.ErrorMessage != "运行日志行不是 JSON 对象" {
		t.Fatalf("JSON 数组必须保留 Node 的对象诊断，实际为 %#v", array)
	}
	invalid := ParseLine(`{"broken"`, LineOptions{Now: func() time.Time { return now }})
	if invalid == nil || invalid.ErrorMessage != "运行日志行不是有效 JSON" {
		t.Fatalf("非法 JSON 必须保留 Node 的语法诊断，实际为 %#v", invalid)
	}
}

func TestParseLogFileNameMatchesNodeContract(t *testing.T) {
	tests := []struct {
		name string
		role string
		kind LogFileKind
		ok   bool
	}{
		{name: "juhe-ai.log", role: "server", kind: LogFileCurrent, ok: true},
		{name: "juhe-ai.stats-worker.instance-01.log", role: "stats-worker:instance-01", kind: LogFileCurrent, ok: true},
		{name: "juhe-ai.20260721T121500Z.a1b2.log", role: "server", kind: LogFileRotated, ok: true},
		{name: "juhe-ai.log.20260721T121500Z.invalid!.log", ok: false},
		{name: "other.log", ok: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			role, kind, ok := ParseLogFileName(test.name)
			if ok != test.ok || (ok && (role != test.role || kind != test.kind)) {
				t.Fatalf("ParseLogFileName(%q) = (%q, %q, %t), want (%q, %q, %t)", test.name, role, kind, ok, test.role, test.kind, test.ok)
			}
		})
	}
}

func TestCurrentFileStartsAtTailThenImportsAppend(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	ctx := testOwnerContext(t, store)
	currentPath := filepath.Join(config.LogDirectory, "juhe-ai.log")
	writeTestFile(t, currentPath, logLine("before-start", "2026-08-08T00:00:00.000Z")+"\n")
	indexer := NewIndexer(config, store)
	if err := indexer.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	assertRuntimeLogCount(t, store, 0)

	appendTestFile(t, currentPath, logLine("after-start", "2026-08-08T00:00:01.000Z")+"\n")
	if err := indexer.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	assertRuntimeLogCount(t, store, 1)
	cursor := findCursor(t, store, currentPath)
	info, err := os.Stat(currentPath)
	if err != nil {
		t.Fatal(err)
	}
	if cursor.CursorOffset != info.Size() || cursor.LastErrorMessage != "" {
		t.Fatalf("current 文件 cursor 未追平或仍保留错误: %#v", cursor)
	}
}

func TestRotatedFileIsIdempotent(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	ctx := testOwnerContext(t, store)
	rotatedPath := filepath.Join(config.LogDirectory, "juhe-ai.20260721T121500Z.a1b2.log")
	writeTestFile(t, rotatedPath, logLine("first", "2026-08-08T00:00:00.000Z")+"\n"+logLine("second", "2026-08-08T00:00:01.000Z")+"\n")
	indexer := NewIndexer(config, store)
	if err := indexer.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if err := indexer.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	assertRuntimeLogCount(t, store, 2)
	cursor := findCursor(t, store, rotatedPath)
	if cursor.LineNumber != 2 || cursor.LastErrorMessage != "" {
		t.Fatalf("轮转文件 cursor 不正确: %#v", cursor)
	}
}

func TestRunOnceProcessesFilesSerially(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	ctx := testOwnerContext(t, store)
	writeTestFile(t, filepath.Join(config.LogDirectory, "juhe-ai.20260721T121500Z.a1b2.log"), logLine("first", "2026-08-08T00:00:00.000Z")+"\n")
	writeTestFile(t, filepath.Join(config.LogDirectory, "juhe-ai.20260721T121501Z.c3d4.log"), logLine("second", "2026-08-08T00:00:01.000Z")+"\n")

	probe := &runOnceCommitProbeStore{Store: store}
	if err := NewIndexer(config, probe).RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	assertRuntimeLogCount(t, store, 2)
	if probe.maxConcurrentCommits != 1 {
		t.Fatalf("同一次 RunOnce 的 Commit 必须串行执行，最大并发为 %d", probe.maxConcurrentCommits)
	}
}

func TestIncompleteTailDoesNotAdvanceCursorUntilNewlineArrives(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	ctx := testOwnerContext(t, store)
	rotatedPath := filepath.Join(config.LogDirectory, "juhe-ai.20260721T121500Z.a1b2.log")
	writeTestFile(t, rotatedPath, logLine("large-line", "2026-08-08T00:00:00.000Z"))
	indexer := NewIndexer(config, store)
	if err := indexer.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	assertRuntimeLogCount(t, store, 0)
	cursor := findCursor(t, store, rotatedPath)
	if cursor.CursorOffset != 0 || cursor.LineNumber != 0 {
		t.Fatalf("物理 EOF 前没有换行的记录不得推进 cursor: %#v", cursor)
	}

	appendTestFile(t, rotatedPath, "\n")
	if err := indexer.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	assertRuntimeLogCount(t, store, 1)
}

func TestWriteFailureKeepsLastCommittedCursorAndRecovers(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	ctx := testOwnerContext(t, store)
	rotatedPath := filepath.Join(config.LogDirectory, "juhe-ai.20260721T121500Z.a1b2.log")
	writeTestFile(t, rotatedPath, logLine("recoverable", "2026-08-08T00:00:00.000Z")+"\n")
	if _, err := store.db.Exec(`CREATE TRIGGER reject_runtime_log_insert BEFORE INSERT ON runtime_logs BEGIN SELECT RAISE(ABORT, 'forced write failure'); END`); err != nil {
		t.Fatal(err)
	}
	indexer := NewIndexer(config, store)
	if err := indexer.RunOnce(ctx); err == nil {
		t.Fatal("写入失败必须向调用方暴露")
	}
	assertRuntimeLogCount(t, store, 0)
	cursor := findCursor(t, store, rotatedPath)
	if cursor.CursorOffset != 0 || cursor.LastErrorMessage != writeFailureCursorMessage {
		t.Fatalf("失败时 cursor 必须停留在最近成功位置: %#v", cursor)
	}
	if _, err := store.db.Exec("DROP TRIGGER reject_runtime_log_insert"); err != nil {
		t.Fatal(err)
	}
	if err := indexer.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	assertRuntimeLogCount(t, store, 1)
	cursor = findCursor(t, store, rotatedPath)
	if cursor.LastErrorMessage != "" {
		t.Fatalf("恢复成功后必须清除 cursor 错误: %#v", cursor)
	}
}

func TestRunReturnsManagedImportGoroutinePanic(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	ctx := testOwnerContext(t, store)
	rotatedPath := filepath.Join(config.LogDirectory, "juhe-ai.20260721T121500Z.a1b2.log")
	writeTestFile(t, rotatedPath, logLine("panic", "2026-08-08T00:00:00.000Z")+"\n")
	indexer := NewIndexer(config, panicFindCursorStore{Store: store})
	err := indexer.Run(ctx)
	if !errors.Is(err, errManagedGoroutinePanic) {
		t.Fatalf("managed import panic must leave F1 component for supervisor recovery, got %v", err)
	}
}

func TestRunReturnsOrdinaryRuntimeFailureToSupervisor(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	ctx := testOwnerContext(t, store)
	configuredFailure := errors.New("runtime retention settings fixture failure")
	indexer := NewIndexer(config, failingRuntimeRetentionStore{Store: store, err: configuredFailure})
	err := indexer.Run(ctx)
	if !errors.Is(err, configuredFailure) {
		t.Fatalf("ordinary F1 runtime failure must return to the sidecar supervisor, got %v", err)
	}
}

func TestRunWithOwnerLeaseReleasesLeaseAfterRunPanic(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	config.OwnerID = "panic-owner"
	config.OwnerLease = time.Minute
	err := RunWithOwnerLease(context.Background(), config, store, func(context.Context) error {
		panic("runtime owner callback fixture panic")
	})
	if err == nil || !strings.Contains(err.Error(), "runtime owner callback fixture panic") {
		t.Fatalf("owner callback panic must return with its stack context, got %v", err)
	}
	lease, acquired, acquireErr := store.AcquireOwnerLease(context.Background(), "replacement-owner", time.Minute)
	if acquireErr != nil || !acquired {
		t.Fatalf("panic path must release the old F1 owner lease immediately: acquired=%t err=%v", acquired, acquireErr)
	}
	if err := store.ReleaseOwnerLease(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
}

type panicFindCursorStore struct{ Store }

func (panicFindCursorStore) FindCursor(context.Context, string) (*Cursor, error) {
	panic("find cursor fixture panic")
}

type failingRuntimeRetentionStore struct {
	Store
	err error
}

func (store failingRuntimeRetentionStore) RuntimeRetentionDays(context.Context, int) (int, error) {
	return 0, store.err
}

func TestCurrentLogReplacementPreservesDisplacedCursorAndIndexesNewFile(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	ctx := testOwnerContext(t, store)
	currentPath := filepath.Join(config.LogDirectory, "juhe-ai.log")
	rotatedPath := filepath.Join(config.LogDirectory, "juhe-ai.20260721T121500Z.a1b2.log")
	writeTestFile(t, currentPath, logLine("old-current", "2026-08-08T00:00:00.000Z")+"\n")
	indexer := NewIndexer(config, store)
	if err := indexer.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	oldCursor, err := store.FindCursor(context.Background(), currentPath)
	if err != nil || oldCursor == nil {
		t.Fatalf("应保留旧 current cursor: cursor=%v err=%v", oldCursor, err)
	}
	if err := os.Rename(currentPath, rotatedPath); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, currentPath, logLine("new-current", "2026-08-08T00:01:00.000Z")+"\n")
	if err := indexer.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FindCursor(context.Background(), displacedIdentityPrefix+oldCursor.FileIdentity); err != nil {
		t.Fatal(err)
	}
	newCursor, err := store.FindCursor(context.Background(), currentPath)
	if err != nil || newCursor == nil || newCursor.FileIdentity == oldCursor.FileIdentity {
		t.Fatalf("替换后 current 必须有新 identity cursor: cursor=%v err=%v", newCursor, err)
	}
	assertRuntimeLogCount(t, store, 1)
}

func TestRetentionDeletesExpiredRecordsAndFacets(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	ctx := testOwnerContext(t, store)
	rotatedPath := filepath.Join(config.LogDirectory, "juhe-ai.20260721T121500Z.a1b2.log")
	writeTestFile(t, rotatedPath, logLine("expired", "2000-01-01T00:00:00.000Z")+"\n"+logLine("retained", "2026-08-08T00:00:00.000Z")+"\n")
	indexer := NewIndexer(config, store)
	if err := indexer.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if err := indexer.RunRetention(ctx); err != nil {
		t.Fatal(err)
	}
	assertRuntimeLogCount(t, store, 1)
	var event string
	if err := store.db.QueryRow("SELECT event FROM runtime_logs LIMIT 1").Scan(&event); err != nil {
		t.Fatal(err)
	}
	if event != "retained" {
		t.Fatalf("保留清理删除了错误记录: %q", event)
	}
	var total int
	if err := store.db.QueryRow("SELECT total_count FROM runtime_log_facet_summary WHERE bucket_key = ?", facetBucketKey).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("facet 汇总未随保留清理更新: %d", total)
	}
}

func TestRotatedFileCleanupWaitsForDurableCursor(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	ctx := testOwnerContext(t, store)
	rotatedPath := filepath.Join(config.LogDirectory, "juhe-ai.20260721T121500Z.a1b2.log")
	writeTestFile(t, rotatedPath, logLine("archived", "2026-08-08T00:00:00.000Z")+"\n")
	old := time.Now().AddDate(0, 0, -config.LogRetentionDays-1)
	if err := os.Chtimes(rotatedPath, old, old); err != nil {
		t.Fatal(err)
	}
	indexer := NewIndexer(config, store)
	deleted, err := indexer.cleanupRotatedFiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 0 {
		t.Fatalf("未建立 cursor 的轮转日志不能删除，实际删除 %d", deleted)
	}
	if _, err := os.Stat(rotatedPath); err != nil {
		t.Fatalf("未索引轮转文件必须保留: %v", err)
	}
	if err := indexer.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	deleted, err = indexer.cleanupRotatedFiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("已索引且过期的轮转日志必须删除，实际删除 %d", deleted)
	}
	if _, err := os.Stat(rotatedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("已索引过期轮转文件未删除: %v", err)
	}
}

func TestRotatedFileCleanupRejectsStaleOwnerLease(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	firstCtx := testOwnerContext(t, store)
	rotatedPath := filepath.Join(config.LogDirectory, "juhe-ai.20260721T121500Z.a1b2.log")
	writeTestFile(t, rotatedPath, logLine("archived", "2026-08-08T00:00:00.000Z")+"\n")
	old := time.Now().AddDate(0, 0, -config.LogRetentionDays-1)
	if err := os.Chtimes(rotatedPath, old, old); err != nil {
		t.Fatal(err)
	}
	indexer := NewIndexer(config, store)
	if err := indexer.RunOnce(firstCtx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec("UPDATE runtime_log_index_owner_leases SET lease_until = '2000-01-01T00:00:00.000Z' WHERE lease_key = ?", runtimeLogOwnerLeaseKey); err != nil {
		t.Fatal(err)
	}
	secondLease, acquired, err := store.AcquireOwnerLease(context.Background(), "replacement-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("接管过期 lease 必须成功: lease=%#v acquired=%t err=%v", secondLease, acquired, err)
	}
	t.Cleanup(func() {
		if err := store.ReleaseOwnerLease(context.Background(), secondLease); err != nil {
			t.Errorf("清理 replacement owner lease 失败: %v", err)
		}
	})

	deleted, err := indexer.cleanupRotatedFiles(firstCtx)
	if !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("旧 token 的轮转文件清理必须返回 ErrOwnerLeaseLost，实际为 %v", err)
	}
	if deleted != 0 {
		t.Fatalf("旧 token 的轮转文件清理不能报告删除，实际删除 %d", deleted)
	}
	if _, err := os.Stat(rotatedPath); err != nil {
		t.Fatalf("旧 token 不得物理删除轮转文件: %v", err)
	}
}

func TestOwnerLeaseFenceBlocksReplacementUntilCallbackReturns(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	secondOpened, err := OpenStore(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	second, ok := secondOpened.(*sqliteStore)
	if !ok {
		t.Fatalf("测试预期第二个 SQLite Store，实际为 %T", secondOpened)
	}
	t.Cleanup(func() { _ = second.Close() })

	lease, acquired, err := store.AcquireOwnerLease(context.Background(), "fence-owner", 50*time.Millisecond)
	if err != nil || !acquired {
		t.Fatalf("fence owner 必须获得 lease: lease=%#v acquired=%t err=%v", lease, acquired, err)
	}
	t.Cleanup(func() {
		if err := store.ReleaseOwnerLease(context.Background(), lease); err != nil && !errors.Is(err, ErrOwnerLeaseFenced) {
			t.Errorf("清理 fence owner lease 失败: %v", err)
		}
	})

	target := filepath.Join(config.LogDirectory, "fence-target.log")
	writeTestFile(t, target, "fenced\n")
	entered := make(chan struct{})
	releaseCallback := make(chan struct{})
	fenceErr := make(chan error, 1)
	go func() {
		fenceErr <- store.WithOwnerLeaseFence(context.Background(), lease, func() error {
			close(entered)
			<-releaseCallback
			err := os.Remove(target)
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		})
	}()
	defer func() {
		select {
		case <-releaseCallback:
		default:
			close(releaseCallback)
		}
	}()
	<-entered
	// Let the original lease expire while its fencing transaction remains open.
	time.Sleep(100 * time.Millisecond)

	type acquireResult struct {
		lease    OwnerLease
		acquired bool
		err      error
	}
	replacementResult := make(chan acquireResult, 1)
	go func() {
		replacement, replacementAcquired, replacementErr := second.AcquireOwnerLease(context.Background(), "replacement-owner", time.Minute)
		replacementResult <- acquireResult{lease: replacement, acquired: replacementAcquired, err: replacementErr}
	}()
	select {
	case result := <-replacementResult:
		t.Fatalf("replacement owner 在删除 callback 返回前不应接管: result=%#v", result)
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("删除 callback 尚未返回时文件必须存在: %v", err)
	}

	close(releaseCallback)
	if err := <-fenceErr; err != nil {
		t.Fatalf("owner lease fence callback 失败: %v", err)
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("删除 callback 返回后文件必须被删除: %v", err)
	}
	select {
	case result := <-replacementResult:
		if result.err != nil || !result.acquired {
			t.Fatalf("删除 callback 返回后 replacement owner 应可接管: result=%#v", result)
		}
		if err := second.ReleaseOwnerLease(context.Background(), result.lease); err != nil {
			t.Fatalf("释放 replacement owner lease 失败: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("replacement owner 在 callback 返回后仍未完成接管")
	}
}

func TestRuntimeRetentionDaysReadsBusinessSetting(t *testing.T) {
	store, _ := openTestSQLiteStore(t)
	days, err := store.RuntimeRetentionDays(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if days != 14 {
		t.Fatalf("运行日志保留期必须读取业务设置：got %d want 14", days)
	}
}

func TestStoreNormalizesTimestampsLikeNodeRepository(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	ctx := testOwnerContext(t, store)
	lease := testOwnerLease(t, ctx)
	cursor := Cursor{LogFile: filepath.Join(config.LogDirectory, "juhe-ai.log"), FileIdentity: "test:1:1"}
	records := []Record{{ID: "normalized-valid", Time: "2026-08-08T08:00:00+08:00", CreatedAt: "2026-08-08T08:00:00+08:00", Level: " WARN ", RawJSON: "{}"}}
	if err := store.Commit(ctx, lease, records, cursor, time.Now().AddDate(0, 0, -1)); err != nil {
		t.Fatal(err)
	}
	var validTime, validCreatedAt, validLevel string
	if err := store.db.QueryRow("SELECT time, created_at, level FROM runtime_logs WHERE id = ?", "normalized-valid").Scan(&validTime, &validCreatedAt, &validLevel); err != nil {
		t.Fatal(err)
	}
	if validTime != "2026-08-08T00:00:00.000Z" || validCreatedAt != validTime || validLevel != "warn" {
		t.Fatalf("有效时间和级别未按 Node repository 归一化: %q, %q, %q", validTime, validCreatedAt, validLevel)
	}
	if err := store.Commit(ctx, lease, []Record{{ID: "normalized-invalid", Time: "not-a-date", CreatedAt: "also-not-a-date", RawJSON: "{}"}}, cursor, time.Now().UTC().AddDate(0, 0, -1)); err == nil {
		t.Fatal("invalid supplied runtime timestamp must be rejected instead of replaced with now")
	}
	var invalidCount int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM runtime_logs WHERE id = ?", "normalized-invalid").Scan(&invalidCount); err != nil || invalidCount != 0 {
		t.Fatalf("invalid timestamp record must not persist: count=%d err=%v", invalidCount, err)
	}
}

func TestStoreNormalizesLegacyNodeTimestamp(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	ctx := testOwnerContext(t, store)
	lease := testOwnerLease(t, ctx)
	cursor := Cursor{LogFile: filepath.Join(config.LogDirectory, "juhe-ai.log"), FileIdentity: "legacy:1:1"}
	if err := store.Commit(ctx, lease, []Record{{
		ID:        "legacy-timestamp",
		Time:      "2026-08-17 21:12:43.935+00",
		CreatedAt: "2026-08-17 21:12:43.935+00",
		Level:     "info",
		RawJSON:   "{}",
	}}, cursor, time.Now().UTC().AddDate(0, 0, -1)); err != nil {
		t.Fatalf("旧 Node 时间格式应可归一化: %v", err)
	}
	var got string
	if err := store.db.QueryRow("SELECT time FROM runtime_logs WHERE id = ?", "legacy-timestamp").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "2026-08-17T21:12:43.935Z" {
		t.Fatalf("旧 Node 时间格式归一化错误: got %q", got)
	}
}

func TestNormalizeLegacyNodeTimestampRejectsUnknownFormat(t *testing.T) {
	if _, err := normalizeNodeTimestamp("2026/08/17 21:12:43+00"); err == nil {
		t.Fatal("未知时间格式必须继续拒绝")
	}
}

func TestStoreRejectsRFC1123Timestamp(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	ctx := testOwnerContext(t, store)
	lease := testOwnerLease(t, ctx)
	cursor := Cursor{LogFile: filepath.Join(config.LogDirectory, "juhe-ai.log"), FileIdentity: "test:1:1"}
	if err := store.Commit(ctx, lease, []Record{{
		ID:        "rfc1123",
		Time:      "Tue, 14 Jul 2026 10:45:12 GMT",
		CreatedAt: "Tue, 14 Jul 2026 10:45:13 GMT",
		Level:     "info",
		RawJSON:   "{}",
	}}, cursor, time.Now().UTC().AddDate(0, 0, -1)); err == nil {
		t.Fatal("RFC1123 absolute time must be rejected")
	}
}

func TestPostgresRuntimeLogSchemaUsesTimestamptzForInstants(t *testing.T) {
	for _, fragment := range []string{
		"time timestamptz NOT NULL",
		"created_at timestamptz NOT NULL",
		"last_read_at timestamptz",
		"lease_until timestamptz NOT NULL",
		"earliest_time timestamptz",
		"latest_time timestamptz",
	} {
		if !strings.Contains(postgresSchema, fragment) {
			t.Fatalf("PostgreSQL F1 schema must preserve absolute instants as timestamptz: missing %q", fragment)
		}
	}
}

func TestSQLiteStoreUsesNodeBusyTimeout(t *testing.T) {
	store, _ := openTestSQLiteStore(t)
	var timeout int
	if err := store.db.QueryRow("PRAGMA busy_timeout").Scan(&timeout); err != nil {
		t.Fatal(err)
	}
	if timeout != sqliteBusyTimeoutMs {
		t.Fatalf("SQLite busy_timeout = %d, want %d", timeout, sqliteBusyTimeoutMs)
	}
	var journalMode string
	if err := store.db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		t.Fatalf("SQLite journal_mode = %q, want WAL", journalMode)
	}
}

func TestCheckSchemaRejectsMissingRuntimeLogIndex(t *testing.T) {
	store, _ := openTestSQLiteStore(t)
	if _, err := store.db.Exec("DROP INDEX idx_runtime_logs_trace_id_time"); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckSchema(context.Background()); err == nil || !strings.Contains(err.Error(), "idx_runtime_logs_trace_id_time") {
		t.Fatalf("缺少运行日志索引必须阻止 Go owner 启动，实际为 %v", err)
	}
}

func TestLoadConfigRejectsInvalidBoundedValues(t *testing.T) {
	values := map[string]string{
		"JUHE_AI_RUNTIME_LOG_INSTANCE_ID":   "test-instance",
		"JUHE_AI_RUNTIME_LOG_STORE":         "sqlite",
		"JUHE_AI_DATASET_DATABASE_PATH":     "dataset.sqlite",
		"JUHE_AI_LOG_DIR":                   "logs",
		"JUHE_AI_RUNTIME_LOG_POLL_INTERVAL": "not-a-duration",
	}
	if _, err := LoadConfig(func(name string) string { return values[name] }); err == nil || !strings.Contains(err.Error(), "JUHE_AI_RUNTIME_LOG_POLL_INTERVAL") {
		t.Fatalf("非法 duration 必须返回可诊断错误，实际为 %v", err)
	}
}

func TestSQLiteOwnerLeaseReleasePreservesMonotonicFenceToken(t *testing.T) {
	store, _ := openTestSQLiteStore(t)
	first, acquired, err := store.AcquireOwnerLease(context.Background(), "first", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("首次获取必须成功: lease=%#v acquired=%t err=%v", first, acquired, err)
	}
	if err := store.ReleaseOwnerLease(context.Background(), first); err != nil {
		t.Fatalf("首次释放必须成功: %v", err)
	}
	second, acquired, err := store.AcquireOwnerLease(context.Background(), "second", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("释放后重新获取必须成功: lease=%#v acquired=%t err=%v", second, acquired, err)
	}
	if second.FenceToken <= first.FenceToken {
		t.Fatalf("正常 release/reacquire 必须递增 fence token: first=%d second=%d", first.FenceToken, second.FenceToken)
	}
	if err := store.ReleaseOwnerLease(context.Background(), second); err != nil {
		t.Fatalf("清理第二个 lease 失败: %v", err)
	}
}

func TestLoadConfigRequiresOwnerLeaseInstanceID(t *testing.T) {
	values := map[string]string{
		"JUHE_AI_RUNTIME_LOG_STORE":     "sqlite",
		"JUHE_AI_DATASET_DATABASE_PATH": "dataset.sqlite",
		"JUHE_AI_LOG_DIR":               "logs",
	}
	if _, err := LoadConfig(func(name string) string { return values[name] }); err == nil || !strings.Contains(err.Error(), "JUHE_AI_RUNTIME_LOG_INSTANCE_ID") {
		t.Fatalf("缺少 owner 实例 ID 必须拒绝启动，实际为 %v", err)
	}
}

func TestLoadConfigAcceptsInstanceWithoutNodeGoOwnerSwitch(t *testing.T) {
	values := map[string]string{
		"JUHE_AI_RUNTIME_LOG_INSTANCE_ID":        "test-instance",
		"JUHE_AI_RUNTIME_LOG_STORE":              "sqlite",
		"JUHE_AI_DATASET_DATABASE_PATH":          "dataset.sqlite",
		"JUHE_AI_RUNTIME_LOG_DATABASE_PATH":      "runtime-log.sqlite",
		"JUHE_AI_TABLE_MONITOR_DATABASE_PATH":    "table-monitor.sqlite",
		"JUHE_AI_DATABASE_PATH":                  "business.sqlite",
		"JUHE_AI_USAGE_CATALOG_DATABASE_PATH":    "usage-catalog.sqlite",
		"JUHE_AI_STATS_DATABASE_PATH":            "stats.sqlite",
		"JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT": "codex-context-shards",
		"JUHE_AI_LOG_DIR":                        "logs",
	}
	if _, err := LoadConfig(func(name string) string { return values[name] }); err != nil {
		t.Fatalf("运行日志索引不应依赖 Node/Go owner 开关，实际为 %v", err)
	}
}

func TestLoadConfigRequiresDedicatedRuntimeLogSQLitePath(t *testing.T) {
	values := map[string]string{
		"JUHE_AI_RUNTIME_LOG_INSTANCE_ID": "test-instance",
		"JUHE_AI_RUNTIME_LOG_STORE":       "sqlite",
		"JUHE_AI_DATABASE_PATH":           "business.sqlite",
		"JUHE_AI_LOG_DIR":                 "logs",
	}
	if _, err := LoadConfig(func(name string) string { return values[name] }); err == nil || !strings.Contains(err.Error(), "JUHE_AI_RUNTIME_LOG_DATABASE_PATH") {
		t.Fatalf("缺少专用运行日志 SQLite 路径必须拒绝启动，实际为 %v", err)
	}
}

func TestLoadConfigRejectsSharedDatasetAndRuntimeLogSQLitePath(t *testing.T) {
	_, values := runtimeLogSQLiteConfigValues(t)
	values["JUHE_AI_DATASET_DATABASE_PATH"] = values["JUHE_AI_RUNTIME_LOG_DATABASE_PATH"]
	if _, err := LoadConfig(func(name string) string { return values[name] }); err == nil || !strings.Contains(err.Error(), "不得与 JUHE_AI_DATASET_DATABASE_PATH") {
		t.Fatalf("运行日志 SQLite 不能与 Node dataset 文件共用，实际为 %v", err)
	}
}

func TestLoadConfigRejectsSharedBusinessAndRuntimeLogSQLitePath(t *testing.T) {
	_, values := runtimeLogSQLiteConfigValues(t)
	values["JUHE_AI_DATABASE_PATH"] = values["JUHE_AI_RUNTIME_LOG_DATABASE_PATH"]
	if _, err := LoadConfig(func(name string) string { return values[name] }); err == nil || !strings.Contains(err.Error(), "不得与 JUHE_AI_DATABASE_PATH") {
		t.Fatalf("运行日志 SQLite 不能与 Node 业务库共用，实际为 %v", err)
	}
}

func TestLoadConfigRejectsSharedTableMonitorAndRuntimeLogSQLitePath(t *testing.T) {
	_, values := runtimeLogSQLiteConfigValues(t)
	values["JUHE_AI_TABLE_MONITOR_DATABASE_PATH"] = values["JUHE_AI_RUNTIME_LOG_DATABASE_PATH"]
	if _, err := LoadConfig(func(name string) string { return values[name] }); err == nil || !strings.Contains(err.Error(), "JUHE_AI_TABLE_MONITOR_DATABASE_PATH") {
		t.Fatalf("运行日志 SQLite 不能与 F2 表监控文件共用，实际为 %v", err)
	}
}

func TestLoadConfigRejectsHardLinkedTableMonitorAndRuntimeLogSQLitePath(t *testing.T) {
	root, values := runtimeLogSQLiteConfigValues(t)
	runtimeLogPath := values["JUHE_AI_RUNTIME_LOG_DATABASE_PATH"]
	tableMonitorPath := filepath.Join(root, "table-monitor.sqlite")
	if err := os.Link(runtimeLogPath, tableMonitorPath); err != nil {
		t.Fatalf("创建 F1/F2 硬链接 fixture 失败: %v", err)
	}
	values["JUHE_AI_TABLE_MONITOR_DATABASE_PATH"] = tableMonitorPath
	if _, err := LoadConfig(func(name string) string { return values[name] }); err == nil || !strings.Contains(err.Error(), "JUHE_AI_TABLE_MONITOR_DATABASE_PATH") {
		t.Fatalf("F1/F2 硬链接 SQLite 文件必须拒绝启动，实际为 %v", err)
	}
}

func TestLoadConfigRejectsDanglingSQLiteAliasFailClosed(t *testing.T) {
	root, values := runtimeLogSQLiteConfigValues(t)
	dangling := filepath.Join(root, "dangling-table-monitor.sqlite")
	if err := os.Symlink(filepath.Join(root, "missing-target.sqlite"), dangling); err != nil {
		t.Skipf("当前环境不能创建悬空 SQLite symlink fixture: %v", err)
	}
	values["JUHE_AI_TABLE_MONITOR_DATABASE_PATH"] = dangling
	if _, err := LoadConfig(func(name string) string { return values[name] }); err == nil || !strings.Contains(err.Error(), "隔离失败") {
		t.Fatalf("悬空 SQLite symlink 必须 fail-closed 拒绝启动，实际为 %v", err)
	}
}

func TestLoadConfigRejectsRuntimeLogSQLiteAliasesForAllNodeOwners(t *testing.T) {
	t.Run("usage catalog direct path", func(t *testing.T) {
		_, values := runtimeLogSQLiteConfigValues(t)
		values["JUHE_AI_USAGE_CATALOG_DATABASE_PATH"] = values["JUHE_AI_RUNTIME_LOG_DATABASE_PATH"]
		assertRuntimeLogSQLiteConfigRejected(t, values, "JUHE_AI_USAGE_CATALOG_DATABASE_PATH")
	})

	t.Run("stats hard link", func(t *testing.T) {
		root, values := runtimeLogSQLiteConfigValues(t)
		alias := filepath.Join(root, "stats-hard-link.sqlite")
		if err := os.Link(values["JUHE_AI_RUNTIME_LOG_DATABASE_PATH"], alias); err != nil {
			t.Fatalf("创建 stats 硬链接 fixture 失败: %v", err)
		}
		values["JUHE_AI_STATS_DATABASE_PATH"] = alias
		assertRuntimeLogSQLiteConfigRejected(t, values, "JUHE_AI_STATS_DATABASE_PATH")
	})

	t.Run("codex shard symlink", func(t *testing.T) {
		_, values := runtimeLogSQLiteConfigValues(t)
		shard := filepath.Join(values["JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT"], "state-001.sqlite3")
		if err := os.Symlink(values["JUHE_AI_RUNTIME_LOG_DATABASE_PATH"], shard); err != nil {
			t.Skipf("当前环境不能创建 Codex shard symlink fixture: %v", err)
		}
		assertRuntimeLogSQLiteConfigRejected(t, values, "JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT")
	})

	t.Run("runtime database under codex shard root", func(t *testing.T) {
		_, values := runtimeLogSQLiteConfigValues(t)
		values["JUHE_AI_RUNTIME_LOG_DATABASE_PATH"] = filepath.Join(values["JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT"], "runtime-log.sqlite3")
		assertRuntimeLogSQLiteConfigRejected(t, values, "JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT")
	})
}

func runtimeLogSQLiteConfigValues(t *testing.T) (string, map[string]string) {
	t.Helper()
	root := t.TempDir()
	codexRoot := filepath.Join(root, "codex-context-shards")
	if err := os.MkdirAll(codexRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	runtimePath := filepath.Join(root, "runtime-log.sqlite")
	if err := os.WriteFile(runtimePath, []byte("sqlite-fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, map[string]string{
		"JUHE_AI_RUNTIME_LOG_INSTANCE_ID":        "test-instance",
		"JUHE_AI_RUNTIME_LOG_STORE":              "sqlite",
		"JUHE_AI_RUNTIME_LOG_DATABASE_PATH":      runtimePath,
		"JUHE_AI_TABLE_MONITOR_DATABASE_PATH":    filepath.Join(root, "table-monitor.sqlite"),
		"JUHE_AI_DATABASE_PATH":                  filepath.Join(root, "business.sqlite"),
		"JUHE_AI_DATASET_DATABASE_PATH":          filepath.Join(root, "dataset.sqlite"),
		"JUHE_AI_USAGE_CATALOG_DATABASE_PATH":    filepath.Join(root, "usage-catalog.sqlite"),
		"JUHE_AI_STATS_DATABASE_PATH":            filepath.Join(root, "stats.sqlite"),
		"JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT": codexRoot,
		"JUHE_AI_LOG_DIR":                        filepath.Join(root, "logs"),
	}
}

func assertRuntimeLogSQLiteConfigRejected(t *testing.T, values map[string]string, expected string) {
	t.Helper()
	if _, err := LoadConfig(func(name string) string { return values[name] }); err == nil || !strings.Contains(err.Error(), expected) {
		t.Fatalf("运行日志专库与 %s 共用物理 SQLite 文件必须拒绝启动，实际为 %v", expected, err)
	}
}

func TestOwnerLeaseRejectsSecondGoInstance(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	secondOpened, err := OpenStore(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	second, ok := secondOpened.(*sqliteStore)
	if !ok {
		t.Fatalf("测试预期第二个 SQLite Store，实际为 %T", secondOpened)
	}
	t.Cleanup(func() { _ = second.Close() })
	firstLease, acquired, err := store.AcquireOwnerLease(context.Background(), "first", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("第一个 Go 实例必须获得 lease: acquired=%t err=%v", acquired, err)
	}
	_, acquired, err = second.AcquireOwnerLease(context.Background(), "second", time.Minute)
	if err != nil || acquired {
		t.Fatalf("第二个 Go 实例必须被拒绝: acquired=%t err=%v", acquired, err)
	}
	if err := store.ReleaseOwnerLease(context.Background(), firstLease); err != nil {
		t.Fatal(err)
	}
	_, acquired, err = second.AcquireOwnerLease(context.Background(), "second", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("释放后第二个 Go 实例必须可获得 lease: acquired=%t err=%v", acquired, err)
	}
}

func TestSQLiteFenceTokenMigrationAndStaleWriterRejection(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	if _, err := store.db.Exec("DROP TABLE runtime_log_index_owner_leases"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TABLE runtime_log_index_owner_leases (
    lease_key TEXT PRIMARY KEY,
    owner_id TEXT NOT NULL,
    lease_until TEXT NOT NULL,
    updated_at TEXT NOT NULL
  ); INSERT INTO runtime_log_index_owner_leases (lease_key, owner_id, lease_until, updated_at)
  VALUES ('runtime-log-index-retention', 'legacy', '2000-01-01T00:00:00.000Z', '2000-01-01T00:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	if err := EnsureSchema(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	var migratedToken int64
	if err := store.db.QueryRow("SELECT fence_token FROM runtime_log_index_owner_leases WHERE lease_key = ?", runtimeLogOwnerLeaseKey).Scan(&migratedToken); err != nil {
		t.Fatal(err)
	}
	if migratedToken != 0 {
		t.Fatalf("SQLite 旧 lease 迁移后的初始 fence token = %d, want 0", migratedToken)
	}

	firstLease, acquired, err := store.AcquireOwnerLease(context.Background(), "first", time.Minute)
	if err != nil || !acquired || firstLease.FenceToken != 1 {
		t.Fatalf("首次获取必须生成 token 1: lease=%#v acquired=%t err=%v", firstLease, acquired, err)
	}
	secondOpened, err := OpenStore(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	second := secondOpened.(*sqliteStore)
	t.Cleanup(func() { _ = second.Close() })
	if _, err := store.db.Exec("UPDATE runtime_log_index_owner_leases SET lease_until = '2000-01-01T00:00:00.000Z' WHERE lease_key = ?", runtimeLogOwnerLeaseKey); err != nil {
		t.Fatal(err)
	}
	secondLease, acquired, err := second.AcquireOwnerLease(context.Background(), "second", time.Minute)
	if err != nil || !acquired || secondLease.FenceToken != 2 {
		t.Fatalf("租约接管必须递增 token: lease=%#v acquired=%t err=%v", secondLease, acquired, err)
	}
	if renewed, err := store.RenewOwnerLease(context.Background(), firstLease, time.Minute); err != nil || renewed {
		t.Fatalf("旧 token 不得续约: renewed=%t err=%v", renewed, err)
	}
	staleCursor := Cursor{LogFile: filepath.Join(config.LogDirectory, "stale.log"), FileIdentity: "stale:1:1"}
	if err := store.Commit(context.Background(), firstLease, []Record{{ID: "stale-record", Time: nowISO(), Level: "info", RawJSON: "{}", CreatedAt: nowISO()}}, staleCursor, time.Now().AddDate(0, 0, -1)); !errors.Is(err, ErrOwnerLeaseFenced) {
		t.Fatalf("旧 token 的提交必须被 fence 拒绝，实际为 %v", err)
	}
	if err := store.CopyCursor(context.Background(), firstLease, staleCursor); !errors.Is(err, ErrOwnerLeaseFenced) {
		t.Fatalf("旧 token 的 cursor copy 必须被 fence 拒绝，实际为 %v", err)
	}
	if err := store.ReplaceCursor(context.Background(), firstLease, nil, staleCursor); !errors.Is(err, ErrOwnerLeaseFenced) {
		t.Fatalf("旧 token 的 cursor replace 必须被 fence 拒绝，实际为 %v", err)
	}
	if _, err := store.Cleanup(context.Background(), firstLease, time.Now(), 1, 1); !errors.Is(err, ErrOwnerLeaseFenced) {
		t.Fatalf("旧 token 的 cleanup 必须被 fence 拒绝，实际为 %v", err)
	}
	if err := store.ReleaseOwnerLease(context.Background(), firstLease); !errors.Is(err, ErrOwnerLeaseFenced) {
		t.Fatalf("旧 token 的释放不得删除新 owner lease，实际为 %v", err)
	}
	var ownerID string
	var fenceToken int64
	if err := store.db.QueryRow("SELECT owner_id, fence_token FROM runtime_log_index_owner_leases WHERE lease_key = ?", runtimeLogOwnerLeaseKey).Scan(&ownerID, &fenceToken); err != nil {
		t.Fatal(err)
	}
	if ownerID != "second" || fenceToken != secondLease.FenceToken {
		t.Fatalf("旧 token 不能改写新 owner lease: owner=%q token=%d", ownerID, fenceToken)
	}
	assertRuntimeLogCount(t, store, 0)
	if err := second.ReleaseOwnerLease(context.Background(), secondLease); err != nil {
		t.Fatalf("当前 token 必须能释放自身 lease: %v", err)
	}
}

func TestMigrateLegacySQLiteMovesFactsWithoutSharingNodeDatasetWriter(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	legacy, err := sql.Open("sqlite", config.DatasetPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = legacy.Close() })
	if _, err := legacy.Exec(`
INSERT INTO runtime_logs (id, log_file, log_offset, line_number, time, level, trace_id, event, message, error_message, raw_json, created_at)
VALUES ('legacy-log', 'juhe-ai.log', 18, 2, '2026-08-09T00:00:00.000Z', 'info', 'legacy-trace', 'legacy-event', 'legacy message', NULL, '{}', '2026-08-09T00:00:00.000Z');
INSERT INTO runtime_log_file_cursors (log_file, file_identity, cursor_offset, line_number, file_size, truncation_generation, file_mtime_ms, last_read_at, last_error_message, created_at, updated_at)
VALUES ('juhe-ai.log', 'legacy:1:1', 18, 2, 18, 0, 0, '2026-08-09T00:00:00.000Z', NULL, '2026-08-09T00:00:00.000Z', '2026-08-09T00:00:00.000Z');
INSERT INTO runtime_log_facet_summary (bucket_key, total_count, earliest_time, latest_time, updated_at)
VALUES ('current', 1, '2026-08-09T00:00:00.000Z', '2026-08-09T00:00:00.000Z', '2026-08-09T00:00:00.000Z');
INSERT INTO runtime_log_level_facets (bucket_key, level, count, updated_at)
VALUES ('current', 'info', 1, '2026-08-09T00:00:00.000Z');
INSERT INTO runtime_log_event_facets (bucket_key, event, count, latest_time, updated_at)
VALUES ('current', 'legacy-event', 1, '2026-08-09T00:00:00.000Z', '2026-08-09T00:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}

	ctx := testOwnerContext(t, store)
	if err := MigrateLegacySQLite(ctx, config, store); err != nil {
		t.Fatal(err)
	}
	if err := MigrateLegacySQLite(ctx, config, store); err != nil {
		t.Fatalf("幂等重跑旧 SQLite 迁移失败: %v", err)
	}
	assertRuntimeLogCount(t, store, 1)
	var cursorOffset int64
	if err := store.db.QueryRow("SELECT cursor_offset FROM runtime_log_file_cursors WHERE log_file = ?", "juhe-ai.log").Scan(&cursorOffset); err != nil {
		t.Fatal(err)
	}
	if cursorOffset != 18 {
		t.Fatalf("旧 cursor 未完整迁移: got %d, want 18", cursorOffset)
	}
	var eventCount int
	if err := store.db.QueryRow("SELECT count FROM runtime_log_event_facets WHERE bucket_key = ? AND event = ?", facetBucketKey, "legacy-event").Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 {
		t.Fatalf("旧 event facet 未完整迁移: got %d, want 1", eventCount)
	}
}

func TestMigrateLegacySQLiteRejectsNonCanonicalAbsoluteTime(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	legacy, err := sql.Open("sqlite", config.DatasetPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = legacy.Close() })
	if _, err := legacy.Exec(`INSERT INTO runtime_logs (id, log_file, log_offset, line_number, time, level, raw_json, created_at) VALUES ('legacy-invalid-time', 'juhe-ai.log', 1, 1, '2026-08-09T00:00:00', 'info', '{}', '2026-08-09T00:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	if err := MigrateLegacySQLite(testOwnerContext(t, store), config, store); err == nil {
		t.Fatal("legacy offset-less absolute time must fail closed before copying rows")
	}
}

func TestLoadConfigRequiresExplicitInstanceID(t *testing.T) {
	values := map[string]string{
		"JUHE_AI_RUNTIME_LOG_STORE":     "sqlite",
		"JUHE_AI_DATASET_DATABASE_PATH": "dataset.sqlite",
		"JUHE_AI_LOG_DIR":               "logs",
	}
	if _, err := LoadConfig(func(name string) string { return values[name] }); err == nil || !strings.Contains(err.Error(), "JUHE_AI_RUNTIME_LOG_INSTANCE_ID") {
		t.Fatalf("Go 索引必须拒绝缺少实例 ID 的启动，实际为 %v", err)
	}
}

func TestFileIdentityHasNodeCompatibleShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.log")
	writeTestFile(t, path, "{}\n")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := FileIdentity(path, info)
	if err != nil {
		t.Fatal(err)
	}
	if parts := strings.Split(identity, ":"); len(parts) != 3 || strings.Contains(identity, " ") {
		t.Fatalf("identity 必须是 Node 兼容的 dev:ino:birthtimeMs 三段格式: %q", identity)
	}
}

func TestFileIdentityMatchesNodeFsStat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.log")
	writeTestFile(t, path, "{}\n")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := FileIdentity(path, info)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("node", "-e", "const fs=require('node:fs'); const s=fs.statSync(process.argv[1]); process.stdout.write([s.dev,s.ino,Math.trunc(s.birthtimeMs)].join(':'))", path)
	expectedBytes, err := command.Output()
	if err != nil {
		t.Fatalf("无法执行 Node fs.stat 兼容性检查: %v", err)
	}
	expected := strings.TrimSpace(string(expectedBytes))
	if actual != expected {
		t.Fatalf("Go file identity 与 Node fs.stat 不一致: got %q, want %q", actual, expected)
	}
}

type runOnceCommitProbeStore struct {
	Store
	mu                   sync.Mutex
	activeCommits        int
	maxConcurrentCommits int
}

func (store *runOnceCommitProbeStore) Commit(ctx context.Context, lease OwnerLease, records []Record, cursor Cursor, retentionCutoff time.Time) error {
	store.mu.Lock()
	store.activeCommits++
	if store.activeCommits > store.maxConcurrentCommits {
		store.maxConcurrentCommits = store.activeCommits
	}
	store.mu.Unlock()
	defer func() {
		store.mu.Lock()
		store.activeCommits--
		store.mu.Unlock()
	}()

	time.Sleep(25 * time.Millisecond)
	return store.Store.Commit(ctx, lease, records, cursor, retentionCutoff)
}

func openTestSQLiteStore(t *testing.T) (*sqliteStore, Config) {
	t.Helper()
	businessPath := filepath.Join(t.TempDir(), "business.sqlite")
	business, err := sql.Open("sqlite", businessPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := business.Exec(`CREATE TABLE system_settings (system_account_id TEXT NOT NULL, key TEXT NOT NULL, value_json TEXT NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY (system_account_id, key)); INSERT INTO system_settings (system_account_id, key, value_json, updated_at) VALUES ('sys_admin', 'runtimeLogIndexRetentionDays', '14', '2026-08-08T00:00:00.000Z')`); err != nil {
		business.Close()
		t.Fatal(err)
	}
	if err := business.Close(); err != nil {
		t.Fatal(err)
	}
	config := Config{
		Mode:                   ModeSQLite,
		DatasetPath:            filepath.Join(t.TempDir(), "dataset.sqlite"),
		RuntimeLogDatabasePath: filepath.Join(t.TempDir(), "runtime-log.sqlite"),
		BusinessPath:           businessPath,
		LogDirectory:           t.TempDir(),
		FileEnabled:            true,
		PollInterval:           time.Second,
		RetentionInterval:      time.Hour,
		RetentionDays:          1,
		LogRetentionDays:       30,
		LogMaxFiles:            500,
		BatchSize:              2,
	}
	legacy, err := sql.Open("sqlite", config.DatasetPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(sqliteSchema); err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	opened, err := OpenStore(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	store, ok := opened.(*sqliteStore)
	if !ok {
		t.Fatalf("测试预期 SQLite Store，实际为 %T", opened)
	}
	if err := EnsureSchema(context.Background(), store); err != nil {
		store.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, config
}

func testOwnerContext(t *testing.T, store Store) context.Context {
	t.Helper()
	lease, acquired, err := store.AcquireOwnerLease(context.Background(), strings.ReplaceAll(t.Name(), "/", "-"), time.Minute)
	if err != nil || !acquired {
		t.Fatalf("测试必须获得 owner lease: lease=%#v acquired=%t err=%v", lease, acquired, err)
	}
	t.Cleanup(func() {
		if err := store.ReleaseOwnerLease(context.Background(), lease); err != nil && !errors.Is(err, ErrOwnerLeaseFenced) {
			t.Errorf("清理测试 owner lease 失败: %v", err)
		}
	})
	return withOwnerLease(context.Background(), lease)
}

func testOwnerLease(t *testing.T, ctx context.Context) OwnerLease {
	t.Helper()
	lease, err := ownerLeaseFromContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return lease
}

func logLine(event string, timestamp string) string {
	return `{"time":"` + timestamp + `","level":30,"event":"` + event + `","msg":"` + event + `"}`
}

func writeTestFile(t *testing.T, path string, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendTestFile(t *testing.T, path string, contents string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(contents); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertRuntimeLogCount(t *testing.T, store *sqliteStore, want int) {
	t.Helper()
	var actual int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM runtime_logs").Scan(&actual); err != nil {
		t.Fatal(err)
	}
	if actual != want {
		t.Fatalf("runtime_logs count = %d, want %d", actual, want)
	}
}

func findCursor(t *testing.T, store *sqliteStore, path string) Cursor {
	t.Helper()
	cursor, err := store.FindCursor(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if cursor == nil {
		t.Fatalf("未找到 %s 的运行日志 cursor", path)
	}
	return *cursor
}

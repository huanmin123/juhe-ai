package runtimelog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// wn_gaps2_test.go 补齐最后一批可达分支：文件截断重置、清理配额与候选批、
// 续约成功/panic、SQLite drop/trigger 注入、PG 各语句失败点。

// TestIndexerTruncationResetsCursorAndReindexes 截断后必须重置 cursor 并以
// truncation generation 重新生成 stable id。
func TestIndexerTruncationResetsCursorAndReindexes(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	ctx := testOwnerContext(t, store)
	rotatedPath := filepath.Join(config.LogDirectory, "juhe-ai.20260721T121500Z.a1b2.log")
	writeTestFile(t, rotatedPath, logLine("first", "2026-08-08T00:00:00.000Z")+"\n"+logLine("second", "2026-08-08T00:00:01.000Z")+"\n")
	indexer := NewIndexer(config, store)
	if err := indexer.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	before := findCursor(t, store, rotatedPath)
	assertRuntimeLogCount(t, store, 2)

	// 截断到更小尺寸：cursor 必须重置并递增 truncation generation。
	if err := os.Truncate(rotatedPath, 0); err != nil {
		t.Fatal(err)
	}
	if err := indexer.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	after := findCursor(t, store, rotatedPath)
	if after.TruncationGeneration != before.TruncationGeneration+1 {
		t.Fatalf("截断必须递增 truncation generation: before=%d after=%d", before.TruncationGeneration, after.TruncationGeneration)
	}
	if after.CursorOffset != 0 || after.LineNumber != 0 {
		t.Fatalf("截断后 cursor 必须归零: %#v", after)
	}
	// 架构契约（docs/architecture/架构总览.md）：截断通过持久化代次重置游标，
	// 旧行保留到 retention 清理，不得静默清除。
	assertRuntimeLogCount(t, store, 2)

	// 重新写入内容后按新 generation 重建索引：总行数 +1，新行 id 必须与
	// 忽略 generation 的推导 id 不同，证明新内容未被旧索引 ID 去重。
	writeTestFile(t, rotatedPath, logLine("first", "2026-08-08T00:00:00.000Z")+"\n")
	if err := indexer.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	assertRuntimeLogCount(t, store, 3)
	rows, err := store.db.Query("SELECT id FROM runtime_logs")
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, 3)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	line := logLine("first", "2026-08-08T00:00:00.000Z")
	legacyIDRecord := ParseLine(line, LineOptions{SourceKey: before.FileIdentity + ":1:0:0"})
	reindexedRecord := ParseLine(line, LineOptions{SourceKey: before.FileIdentity + ":" + fmt.Sprint(after.TruncationGeneration) + ":0"})
	if legacyIDRecord == nil || reindexedRecord == nil {
		t.Fatal("fixture 行必须可解析")
	}
	reindexed := false
	for _, id := range ids {
		if id == legacyIDRecord.ID {
			t.Fatalf("重写内容不得以忽略 generation 的 id 重建索引: %s", id)
		}
		if id == reindexedRecord.ID {
			reindexed = true
		}
	}
	if !reindexed {
		t.Fatalf("重写内容必须以 truncation generation %d 生成新 id: %v", after.TruncationGeneration, ids)
	}
}

// TestIndexerRelocatesIdentityCursorToRenamedFile 改名后 identity cursor 必须迁移。
func TestIndexerRelocatesIdentityCursorToRenamedFile(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	ctx := testOwnerContext(t, store)
	rotatedPath := filepath.Join(config.LogDirectory, "juhe-ai.20260721T121500Z.a1b2.log")
	renamedPath := filepath.Join(config.LogDirectory, "juhe-ai.20260721T121501Z.c3d4.log")
	writeTestFile(t, rotatedPath, logLine("archived", "2026-08-08T00:00:00.000Z")+"\n")
	indexer := NewIndexer(config, store)
	if err := indexer.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(rotatedPath, renamedPath); err != nil {
		t.Fatal(err)
	}
	if err := indexer.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if cursor := findCursor(t, store, renamedPath); cursor.CursorOffset == 0 {
		t.Fatalf("改名后必须迁移 identity cursor 并续读: %#v", cursor)
	}
	assertRuntimeLogCount(t, store, 1)
}

// TestRunOnceRejectsMissingLease 覆盖无 lease 上下文的 RunOnce 拒绝分支。
func TestRunOnceRejectsMissingLease(t *testing.T) {
	store, _ := openTestSQLiteStore(t)
	indexer := NewIndexer(Config{}, store)
	if err := indexer.RunOnce(context.Background()); err == nil {
		t.Fatal("缺少 owner lease 必须拒绝运行")
	}
}

// TestDiscoverFilesSkipsDirectoriesAndForeignNames 覆盖目录与非日志名跳过分支。
func TestDiscoverFilesSkipsDirectoriesAndForeignNames(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	ctx := testOwnerContext(t, store)
	if err := os.MkdirAll(filepath.Join(config.LogDirectory, "subdir"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(config.LogDirectory, "unrelated.txt"), "noise\n")
	writeTestFile(t, filepath.Join(config.LogDirectory, "juhe-ai.20260721T121500Z.a1b2.log"), logLine("kept", "2026-08-08T00:00:00.000Z")+"\n")
	indexer := NewIndexer(config, store)
	if err := indexer.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	assertRuntimeLogCount(t, store, 1)
}

// TestRunLoopRunsRetentionTimers 覆盖 Run 循环中 retention 定时器分支。
// 以轮询次数作为同步点，retention 周期严格短于轮询周期，轮询次数达标时
// retention 必然已触发过。
func TestRunLoopRunsRetentionTimers(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	lease, acquired, err := store.AcquireOwnerLease(context.Background(), t.Name(), time.Minute)
	if err != nil || !acquired {
		t.Fatalf("测试必须获得 owner lease: acquired=%t err=%v", acquired, err)
	}
	t.Cleanup(func() {
		if releaseErr := store.ReleaseOwnerLease(context.Background(), lease); releaseErr != nil && !errors.Is(releaseErr, ErrOwnerLeaseFenced) {
			t.Errorf("清理测试 owner lease 失败: %v", releaseErr)
		}
	})
	config.PollInterval = 5 * time.Millisecond
	config.RetentionInterval = time.Millisecond
	runs := make(chan struct{}, 64)
	indexer := NewIndexer(config, &runSignalStore{Store: store, runs: runs})
	runCtx, cancel := context.WithCancel(withOwnerLease(context.Background(), lease))
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- indexer.Run(runCtx) }()
	// 至少两次轮询（>=10ms），retention（1ms 周期）必然已运行。
	<-runs
	<-runs
	<-runs
	cancel()
	select {
	case <-errCh:
	case <-time.After(5 * time.Second):
		t.Fatal("Run 未在取消后退出")
	}
}

// TestRunWithOwnerLeaseRenewsLeaseWhileRunning 覆盖续约成功分支。
func TestRunWithOwnerLeaseRenewsLeaseWhileRunning(t *testing.T) {
	store, _ := openTestSQLiteStore(t)
	config := Config{OwnerID: "renew-success-owner", OwnerLease: 90 * time.Millisecond}
	completed := make(chan struct{})
	run := func(context.Context) error {
		// 覆盖至少两次续约周期（30ms 一次），时长不影响任何断言结果。
		select {
		case <-time.After(100 * time.Millisecond):
		case <-completed:
		}
		return nil
	}
	close(completed)
	if err := RunWithOwnerLease(context.Background(), config, store, run); err != nil {
		t.Fatalf("正常续约运行不得报错: %v", err)
	}
}

type panickingRenewStore struct{ Store }

func (store *panickingRenewStore) RenewOwnerLease(context.Context, OwnerLease, time.Duration) (bool, error) {
	panic("renewal fixture panic")
}

// TestRunWithOwnerLeaseRenewalPanicIsManaged 覆盖续约 goroutine panic 防护。
func TestRunWithOwnerLeaseRenewalPanicIsManaged(t *testing.T) {
	store, _ := openTestSQLiteStore(t)
	config := Config{OwnerID: "renew-panic-owner", OwnerLease: 3 * time.Millisecond}
	started := make(chan struct{})
	run := func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- RunWithOwnerLease(context.Background(), config, &panickingRenewStore{Store: store}, run)
	}()
	<-started
	select {
	case err := <-errCh:
		if err == nil || !strings.Contains(err.Error(), "续租 goroutine panic") {
			t.Fatalf("续约 panic 必须转换为受控错误: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("续约 panic 后未退出")
	}
}

// TestCleanupRotatedFilesKeepsNewestWithinQuota 覆盖 allowedCompleted 排序删除分支。
func TestCleanupRotatedFilesKeepsNewestWithinQuota(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	ctx := testOwnerContext(t, store)
	config.LogMaxFiles = 3
	currentPath := filepath.Join(config.LogDirectory, "juhe-ai.log")
	writeTestFile(t, currentPath, logLine("current", "2026-08-08T00:00:00.000Z")+"\n")
	paths := []string{
		filepath.Join(config.LogDirectory, "juhe-ai.20260721T121500Z.a1b2.log"),
		filepath.Join(config.LogDirectory, "juhe-ai.20260721T121501Z.c3d4.log"),
		filepath.Join(config.LogDirectory, "juhe-ai.20260721T121502Z.e5f6.log"),
	}
	base := time.Now().Add(-24 * time.Hour)
	for index, path := range paths {
		writeTestFile(t, path, logLine("archived", "2026-08-08T00:00:00.000Z")+"\n")
		mtime := base.Add(time.Duration(index) * time.Hour)
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	indexer := NewIndexer(config, store)
	if err := indexer.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	deleted, err := indexer.cleanupRotatedFiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("配额 2 时只能删除最旧 1 个，实际删除 %d", deleted)
	}
	if _, err := os.Stat(paths[0]); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("最旧轮转文件未删除: %v", err)
	}
	for _, path := range paths[1:] {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("较新轮转文件必须保留: %v", err)
		}
	}
}

// TestCleanupRotatedFilesCanceledContext 覆盖取消上下文分支。
func TestCleanupRotatedFilesCanceledContext(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	writeTestFile(t, filepath.Join(config.LogDirectory, "juhe-ai.20260721T121500Z.a1b2.log"), "x\n")
	indexer := NewIndexer(config, store)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := indexer.cleanupRotatedFiles(withOwnerLease(ctx, OwnerLease{OwnerID: "x", FenceToken: 1})); err == nil {
		t.Fatal("取消的上下文必须中止清理")
	}
}

// TestCleanupRotatedFilesRejectsMissingLogDir 覆盖目录枚举失败分支。
func TestCleanupRotatedFilesRejectsMissingLogDir(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	config.LogDirectory = filepath.Join(t.TempDir(), "missing")
	indexer := NewIndexer(config, store)
	if _, err := indexer.cleanupRotatedFiles(testOwnerContext(t, store)); err == nil {
		t.Fatal("日志目录缺失必须报错")
	}
}

// TestMigrateLegacySQLiteRejectsNonCanonicalButValidTime 覆盖可解析但非 canonical 分支。
func TestMigrateLegacySQLiteRejectsNonCanonicalButValidTime(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	legacy, err := sql.Open("sqlite", config.DatasetPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`INSERT INTO runtime_logs (id, log_file, log_offset, line_number, time, level, raw_json, created_at) VALUES ('offset-time', 'juhe-ai.log', 0, 0, '2026-08-09T00:00:00+00:00', 'info', '{}', '2026-08-09T00:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	if err := MigrateLegacySQLite(testOwnerContext(t, store), config, store); err == nil || !strings.Contains(err.Error(), "canonical UTC") {
		t.Fatalf("非 canonical 绝对时间必须要求离线清洗: %v", err)
	}
	detachLegacy(t, store)
}

// TestSQLiteCleanupSelectFailure 注入 runtime_logs 表缺失，覆盖查询失败分支。
func TestSQLiteCleanupSelectFailure(t *testing.T) {
	store, _ := openTestSQLiteStore(t)
	lease := testOwnerLease(t, testOwnerContext(t, store))
	if _, err := store.db.Exec("DROP TABLE runtime_logs"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Cleanup(context.Background(), lease, time.Now(), 10, 1); err == nil {
		t.Fatal("过期记录查询失败必须暴露")
	}
}

// TestSQLiteCleanupCursorSelectFailure 注入 cursor 表缺失，覆盖 cursor 批查询失败分支。
func TestSQLiteCleanupCursorSelectFailure(t *testing.T) {
	store, _ := openTestSQLiteStore(t)
	lease := testOwnerLease(t, testOwnerContext(t, store))
	if _, err := store.db.Exec("DROP TABLE runtime_log_file_cursors"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Cleanup(context.Background(), lease, time.Now(), 10, 1); err == nil {
		t.Fatal("cursor 批查询失败必须暴露")
	}
}

// TestSQLiteCleanupCursorBatchLimit 覆盖候选数达到批上限的分支。
func TestSQLiteCleanupCursorBatchLimit(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	lease := testOwnerLease(t, testOwnerContext(t, store))
	cutoff := time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC)
	old := nodeISO(cutoff.Add(-time.Hour))
	for index, name := range []string{"juhe-ai.20260721T121500Z.a1b2.log", "juhe-ai.20260721T121501Z.c3d4.log"} {
		if err := store.CopyCursor(context.Background(), lease, Cursor{
			LogFile: filepath.Join(config.LogDirectory, name), FileIdentity: "rotated:" + string(rune('a'+index)),
			CursorOffset: 1, FileSize: 1, LastReadAt: old, CreatedAt: old,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.Exec("UPDATE runtime_log_file_cursors SET updated_at = ?", old); err != nil {
		t.Fatal(err)
	}
	result, err := store.Cleanup(context.Background(), lease, cutoff, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if result.RuntimeLogCursors != 2 {
		t.Fatalf("批上限 1 时两个候选需两批删完: %#v", result)
	}
}

// TestSQLiteCleanupCursorDeleteFailure 注入 cursor 删除失败。
func TestSQLiteCleanupCursorDeleteFailure(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	lease := testOwnerLease(t, testOwnerContext(t, store))
	cutoff := time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC)
	old := nodeISO(cutoff.Add(-time.Hour))
	if err := store.CopyCursor(context.Background(), lease, Cursor{
		LogFile:      filepath.Join(config.LogDirectory, "juhe-ai.20260721T121500Z.a1b2.log"),
		FileIdentity: "rotated:a", CursorOffset: 1, FileSize: 1, LastReadAt: old, CreatedAt: old,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec("UPDATE runtime_log_file_cursors SET updated_at = ?", old); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec("CREATE TRIGGER reject_cursor_delete BEFORE DELETE ON runtime_log_file_cursors BEGIN SELECT RAISE(ABORT, 'forced cursor delete failure'); END"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Cleanup(context.Background(), lease, cutoff, 10, 1); err == nil {
		t.Fatal("cursor 删除失败必须暴露")
	}
}

// TestSQLiteCommitFacetLevelAndEventFailure 注入 level/event facet 写入失败。
func TestSQLiteCommitFacetLevelAndEventFailure(t *testing.T) {
	t.Run("level facet failure", func(t *testing.T) {
		store, config := openTestSQLiteStore(t)
		lease := testOwnerLease(t, testOwnerContext(t, store))
		if _, err := store.db.Exec("CREATE TRIGGER reject_level_facet BEFORE INSERT ON runtime_log_level_facets BEGIN SELECT RAISE(ABORT, 'forced level facet failure'); END"); err != nil {
			t.Fatal(err)
		}
		err := store.Commit(context.Background(), lease, []Record{{ID: "r", Time: "2026-08-08T00:00:00.000Z", Level: "info", Event: "e", RawJSON: "{}", CreatedAt: "2026-08-08T00:00:00.000Z"}},
			Cursor{LogFile: filepath.Join(config.LogDirectory, "juhe-ai.log")}, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
		if err == nil {
			t.Fatal("level facet 写入失败必须暴露")
		}
	})
	t.Run("event facet failure", func(t *testing.T) {
		store, config := openTestSQLiteStore(t)
		lease := testOwnerLease(t, testOwnerContext(t, store))
		if _, err := store.db.Exec("CREATE TRIGGER reject_event_facet BEFORE INSERT ON runtime_log_event_facets BEGIN SELECT RAISE(ABORT, 'forced event facet failure'); END"); err != nil {
			t.Fatal(err)
		}
		err := store.Commit(context.Background(), lease, []Record{{ID: "r", Time: "2026-08-08T00:00:00.000Z", Level: "info", Event: "e", RawJSON: "{}", CreatedAt: "2026-08-08T00:00:00.000Z"}},
			Cursor{LogFile: filepath.Join(config.LogDirectory, "juhe-ai.log")}, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
		if err == nil {
			t.Fatal("event facet 写入失败必须暴露")
		}
	})
}

// TestPostgresFakeCursorNormalizeRejectsInvalidTimestamps 覆盖 PG cursor 归一化失败。
func TestPostgresFakeCursorNormalizeRejectsInvalidTimestamps(t *testing.T) {
	server := newPGFakeServer(t)
	registerPostgresCatalog(t, server)
	registerPostgresLeaseHandlers(t, server)
	store := openFakePostgresStore(t, server)
	lease := OwnerLease{OwnerID: "o", FenceToken: 7}
	if err := store.CopyCursor(context.Background(), lease, Cursor{LogFile: "x", CreatedAt: "bad-time"}); err == nil {
		t.Fatal("非法 createdAt 必须拒绝")
	}
	if err := store.CopyCursor(context.Background(), lease, Cursor{LogFile: "x", LastReadAt: "bad-time"}); err == nil {
		t.Fatal("非法 lastReadAt 必须拒绝")
	}
}

// TestPostgresFakeCommitSkipsFacetsWhenAllExpired 全部记录过期时不得写 facet。
func TestPostgresFakeCommitSkipsFacetsWhenAllExpired(t *testing.T) {
	server := newPGFakeServer(t)
	registerPostgresCatalog(t, server)
	registerPostgresLeaseHandlers(t, server)
	registerCommitSuccessPath(t, server)
	server.handleFunc(pgFakeCursorUpsertSQL, nil, func([]string) ([][]any, string, *pgFakeError) {
		return nil, "INSERT 0 1", nil
	})
	store := openFakePostgresStore(t, server)
	lease := OwnerLease{OwnerID: "o", FenceToken: 7}
	err := store.Commit(context.Background(), lease, []Record{{ID: "old", Time: "2020-01-01T00:00:00.000Z", Level: "info", CreatedAt: "2020-01-01T00:00:00.000Z"}},
		Cursor{LogFile: "x"}, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range server.executedLog() {
		if strings.Contains(normalizePGSQL(entry.sql), "INSERT INTO juhe_dataset.runtime_log_facet_summary") {
			t.Fatal("全部过期时不得写入 facet 汇总")
		}
	}
}

// TestPostgresFakeFacetLevelEventFailures 注入 level/event facet 写入失败。
func TestPostgresFakeFacetLevelEventFailures(t *testing.T) {
	t.Run("level facet", func(t *testing.T) {
		server := newPGFakeServer(t)
		registerPostgresCatalog(t, server)
		registerPostgresLeaseHandlers(t, server)
		registerCommitSuccessPath(t, server)
		server.handleErrorPrefix("INSERT INTO juhe_dataset.runtime_log_level_facets", "42501", "permission denied for level facets")
		store := openFakePostgresStore(t, server)
		err := store.Commit(context.Background(), OwnerLease{OwnerID: "o", FenceToken: 7},
			[]Record{{ID: "r", Time: "2026-08-08T00:00:00.000Z", Level: "info", CreatedAt: "2026-08-08T00:00:00.000Z"}},
			Cursor{LogFile: "x"}, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
		if err == nil {
			t.Fatal("level facet 写入失败必须暴露")
		}
	})
	t.Run("event facet", func(t *testing.T) {
		server := newPGFakeServer(t)
		registerPostgresCatalog(t, server)
		registerPostgresLeaseHandlers(t, server)
		registerCommitSuccessPath(t, server)
		server.handleErrorPrefix("INSERT INTO juhe_dataset.runtime_log_event_facets", "42501", "permission denied for event facets")
		store := openFakePostgresStore(t, server)
		err := store.Commit(context.Background(), OwnerLease{OwnerID: "o", FenceToken: 7},
			[]Record{{ID: "r", Time: "2026-08-08T00:00:00.000Z", Level: "info", CreatedAt: "2026-08-08T00:00:00.000Z"}},
			Cursor{LogFile: "x"}, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
		if err == nil {
			t.Fatal("event facet 写入失败必须暴露")
		}
	})
}

// TestPostgresFakeDecrementFacetFailures 注入 facet 扣减各语句失败。
// wn_pgfake 的 resolve 精确匹配优先于前缀匹配，而 registerCleanupSuccessPath
// 已为下列语句注册精确成功脚本，因此必须用整句精确注入才能覆盖成功脚本。
func TestPostgresFakeDecrementFacetFailures(t *testing.T) {
	cases := []struct {
		name    string
		failSQL string
	}{
		{name: "summary update", failSQL: "UPDATE juhe_dataset.runtime_log_facet_summary SET total_count = GREATEST(0, total_count - $1), earliest_time = $2, latest_time = $3, updated_at = $4 WHERE bucket_key = $5"},
		{name: "summary delete", failSQL: "DELETE FROM juhe_dataset.runtime_log_facet_summary WHERE bucket_key = $1 AND total_count <= 0"},
		{name: "level update", failSQL: "UPDATE juhe_dataset.runtime_log_level_facets SET count = GREATEST(0, count - $1), updated_at = $2 WHERE bucket_key = $3 AND level = $4"},
		{name: "level delete", failSQL: "DELETE FROM juhe_dataset.runtime_log_level_facets WHERE bucket_key = $1 AND count <= 0"},
		{name: "event update", failSQL: "UPDATE juhe_dataset.runtime_log_event_facets SET count = GREATEST(0, count - $1), updated_at = $2 WHERE bucket_key = $3 AND event = $4"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			server := newPGFakeServer(t)
			registerPostgresCatalog(t, server)
			registerPostgresLeaseHandlers(t, server)
			registerCleanupSuccessPath(t, server)
			server.handleError(test.failSQL, "42501", "wn_pgfake 注入失败: "+test.name)
			store := openFakePostgresStore(t, server)
			if _, err := store.Cleanup(context.Background(), OwnerLease{OwnerID: "o", FenceToken: 7}, time.Now(), 10, 1); err == nil {
				t.Fatalf("%s 失败必须暴露", test.name)
			}
		})
	}
}

// TestPostgresFakeDecrementSkipsRecordsBelowBaseline 覆盖 PG 扣减基线之下的记录分支。
func TestPostgresFakeDecrementSkipsRecordsBelowBaseline(t *testing.T) {
	server := newPGFakeServer(t)
	registerPostgresCatalog(t, server)
	registerPostgresLeaseHandlers(t, server)
	registerCleanupSuccessPath(t, server)
	baselineColumns := []pgFakeColumn{{name: "coalesce", oid: pgFakeOIDText}}
	server.handleFunc("SELECT COALESCE((SELECT earliest_time::text FROM juhe_dataset.runtime_log_facet_summary WHERE bucket_key = $1), '')", baselineColumns, func([]string) ([][]any, string, *pgFakeError) {
		return [][]any{{"2027-01-01T00:00:00.000Z"}}, "SELECT 1", nil
	})
	store := openFakePostgresStore(t, server)
	result, err := store.Cleanup(context.Background(), OwnerLease{OwnerID: "o", FenceToken: 7}, time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC), 10, 1)
	if err != nil {
		t.Fatal(err)
	}
	if result.RuntimeLogs != 1 {
		t.Fatalf("Cleanup 统计不正确: %#v", result)
	}
	for _, entry := range server.executedLog() {
		if strings.HasPrefix(normalizePGSQL(entry.sql), "UPDATE juhe_dataset.runtime_log_facet_summary") {
			t.Fatal("低于计数基线的记录不得扣减 facet 汇总")
		}
	}
}

// TestPostgresFakeSecondBeginFailure 注入第二次 BEGIN 失败，覆盖 cursor 批开始失败分支。
func TestPostgresFakeSecondBeginFailure(t *testing.T) {
	server := newPGFakeServer(t)
	registerPostgresCatalog(t, server)
	registerPostgresLeaseHandlers(t, server)
	registerCleanupSuccessPath(t, server)
	var begins atomic.Int64
	server.handleFunc("begin", nil, func([]string) ([][]any, string, *pgFakeError) {
		if begins.Add(1) >= 2 {
			return nil, "", &pgFakeError{code: "57P01", message: "crash during second begin"}
		}
		return nil, "BEGIN", nil
	})
	store := openFakePostgresStore(t, server)
	if _, err := store.Cleanup(context.Background(), OwnerLease{OwnerID: "o", FenceToken: 7}, time.Now(), 10, 2); err == nil || !strings.Contains(err.Error(), "second begin") {
		t.Fatalf("cursor 批开始失败必须暴露: %v", err)
	}
}

// TestPostgresFakeEnsureSchemaReportsCheckFailureBeforeAndAfterDDL 覆盖 EnsureSchema
// 前置校验失败与 DDL 后校验失败分支。
func TestPostgresFakeEnsureSchemaReportsCheckFailureBeforeAndAfterDDL(t *testing.T) {
	t.Run("check failure before DDL", func(t *testing.T) {
		server := newPGFakeServer(t)
		server.handleError("SELECT table_name FROM information_schema.tables WHERE table_schema = 'juhe_dataset' AND table_name = $1", "42501", "permission denied for tables")
		store := openFakePostgresStore(t, server)
		if err := EnsureSchema(context.Background(), store); err == nil || !strings.Contains(err.Error(), "permission denied for tables") {
			t.Fatalf("前置校验失败必须暴露: %v", err)
		}
	})
	t.Run("check failure after DDL", func(t *testing.T) {
		server := newPGFakeServer(t)
		var tablesQueryCount atomic.Int64
		server.handleFunc("SELECT table_name FROM information_schema.tables WHERE table_schema = 'juhe_dataset' AND table_name = $1", pgFakeTableNameColumns(), func(args []string) ([][]any, string, *pgFakeError) {
			if tablesQueryCount.Add(1) == 1 {
				return nil, "SELECT 0", nil
			}
			return [][]any{{args[0]}}, "SELECT 1", nil
		})
		// 第二次 CheckSchema 走到列校验时注入失败。
		var columnsQueryCount atomic.Int64
		server.handleFunc("SELECT column_name, data_type FROM information_schema.columns WHERE table_schema = 'juhe_dataset' AND table_name = $1", pgFakeColumnTypeColumns(), func(args []string) ([][]any, string, *pgFakeError) {
			if columnsQueryCount.Add(1) >= 1 {
				return nil, "", &pgFakeError{code: "42501", message: "permission denied for columns after DDL"}
			}
			return nil, "SELECT 0", nil
		})
		store := openFakePostgresStore(t, server)
		if err := EnsureSchema(context.Background(), store); err == nil || !strings.Contains(err.Error(), "初始化 PostgreSQL 运行日志 schema 后校验失败") {
			t.Fatalf("DDL 后校验失败必须暴露: %v", err)
		}
	})
}

// TestPostgresFakeCheckSchemaIndexQueryFailure 覆盖索引元数据非缺行错误分支。
func TestPostgresFakeCheckSchemaIndexQueryFailure(t *testing.T) {
	server := newPGFakeServer(t)
	registerPostgresCatalog(t, server)
	server.handleError("SELECT indexname FROM pg_indexes WHERE schemaname = 'juhe_dataset' AND indexname = $1", "42501", "permission denied for pg_indexes")
	store := openFakePostgresStore(t, server)
	err := store.CheckSchema(context.Background())
	if err == nil || strings.Contains(err.Error(), "schema 不完整") && !strings.Contains(err.Error(), "permission denied for pg_indexes") {
		t.Fatalf("索引元数据错误必须保留原始错误: %v", err)
	}
}

// TestLoadConfigAcceptsExplicitValues 覆盖显式合法值解析分支。
func TestLoadConfigAcceptsExplicitValues(t *testing.T) {
	values := map[string]string{
		"JUHE_AI_RUNTIME_LOG_INSTANCE_ID":        "inst-explicit",
		"JUHE_AI_RUNTIME_LOG_STORE":              "postgres",
		"JUHE_AI_RUNTIME_LOG_POSTGRES_URL":       "postgres://jobs:secret@127.0.0.1:5432/db?sslmode=disable",
		"JUHE_AI_RUNTIME_LOG_OWNER_LEASE":        "45s",
		"JUHE_AI_RUNTIME_LOG_ONCE":               "false",
		"JUHE_AI_RUNTIME_LOG_POLL_INTERVAL":      "2s",
		"JUHE_AI_RUNTIME_LOG_RETENTION_DAYS":     "30",
		"JUHE_AI_RUNTIME_LOG_POSTGRES_MAX_CONNS": "8",
		"JUHE_AI_RUNTIME_LOG_POSTGRES_MIN_CONNS": "0",
		"JUHE_AI_LOG_DIR":                        "logs",
	}
	config, err := LoadConfig(func(name string) string { return values[name] })
	if err != nil {
		t.Fatalf("显式合法配置必须通过: %v", err)
	}
	if config.OwnerLease != 45*time.Second || config.PollInterval != 2*time.Second || config.RetentionDays != 30 || config.PostgresMaxConns != 8 {
		t.Fatalf("显式配置未生效: %#v", config)
	}
}

// TestNormalizeNodeTimestampRejectsBlank 覆盖空白时间拒绝分支。
func TestNormalizeNodeTimestampRejectsBlank(t *testing.T) {
	if _, err := normalizeNodeTimestamp("   "); err == nil {
		t.Fatal("空白时间必须拒绝")
	}
}

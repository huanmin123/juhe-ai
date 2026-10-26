package runtimelog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
	store, config := openKitPostgresStore(t)
	ctx := testOwnerContext(t, store)
	currentPath := filepath.Join(config.LogDirectory, "juhe-ai.log")
	writeTestFile(t, currentPath, logLine("before-start", "2026-08-08T00:00:00.000Z")+"\n")
	indexer := NewIndexer(config, store)
	if err := indexer.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	assertKitRuntimeLogCount(t, store, 0)

	appendTestFile(t, currentPath, logLine("after-start", "2026-08-08T00:00:01.000Z")+"\n")
	if err := indexer.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	assertKitRuntimeLogCount(t, store, 1)
	cursor := kitFindCursor(t, store, currentPath)
	info, err := os.Stat(currentPath)
	if err != nil {
		t.Fatal(err)
	}
	if cursor.CursorOffset != info.Size() || cursor.LastErrorMessage != "" {
		t.Fatalf("current 文件 cursor 未追平或仍保留错误: %#v", cursor)
	}
}

func TestRotatedFileIsIdempotent(t *testing.T) {
	store, config := openKitPostgresStore(t)
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
	assertKitRuntimeLogCount(t, store, 2)
	cursor := kitFindCursor(t, store, rotatedPath)
	if cursor.LineNumber != 2 || cursor.LastErrorMessage != "" {
		t.Fatalf("轮转文件 cursor 不正确: %#v", cursor)
	}
}

func TestRunOnceProcessesFilesSerially(t *testing.T) {
	store, config := openKitPostgresStore(t)
	ctx := testOwnerContext(t, store)
	writeTestFile(t, filepath.Join(config.LogDirectory, "juhe-ai.20260721T121500Z.a1b2.log"), logLine("first", "2026-08-08T00:00:00.000Z")+"\n")
	writeTestFile(t, filepath.Join(config.LogDirectory, "juhe-ai.20260721T121501Z.c3d4.log"), logLine("second", "2026-08-08T00:00:01.000Z")+"\n")

	probe := &runOnceCommitProbeStore{Store: store}
	if err := NewIndexer(config, probe).RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	assertKitRuntimeLogCount(t, store, 2)
	if probe.maxConcurrentCommits != 1 {
		t.Fatalf("同一次 RunOnce 的 Commit 必须串行执行，最大并发为 %d", probe.maxConcurrentCommits)
	}
}

func TestIncompleteTailDoesNotAdvanceCursorUntilNewlineArrives(t *testing.T) {
	store, config := openKitPostgresStore(t)
	ctx := testOwnerContext(t, store)
	rotatedPath := filepath.Join(config.LogDirectory, "juhe-ai.20260721T121500Z.a1b2.log")
	writeTestFile(t, rotatedPath, logLine("large-line", "2026-08-08T00:00:00.000Z"))
	indexer := NewIndexer(config, store)
	if err := indexer.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	assertKitRuntimeLogCount(t, store, 0)
	cursor := kitFindCursor(t, store, rotatedPath)
	if cursor.CursorOffset != 0 || cursor.LineNumber != 0 {
		t.Fatalf("物理 EOF 前没有换行的记录不得推进 cursor: %#v", cursor)
	}

	appendTestFile(t, rotatedPath, "\n")
	if err := indexer.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	assertKitRuntimeLogCount(t, store, 1)
}

func TestRunReturnsManagedImportGoroutinePanic(t *testing.T) {
	store, config := openKitPostgresStore(t)
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
	store, config := openKitPostgresStore(t)
	ctx := testOwnerContext(t, store)
	configuredFailure := errors.New("runtime retention settings fixture failure")
	indexer := NewIndexer(config, failingRuntimeRetentionStore{Store: store, err: configuredFailure})
	err := indexer.Run(ctx)
	if !errors.Is(err, configuredFailure) {
		t.Fatalf("ordinary F1 runtime failure must return to the sidecar supervisor, got %v", err)
	}
}

func TestRunWithOwnerLeaseReleasesLeaseAfterRunPanic(t *testing.T) {
	store, config := openKitPostgresStore(t)
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
	store, config := openKitPostgresStore(t)
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
	assertKitRuntimeLogCount(t, store, 1)
}

func TestRotatedFileCleanupWaitsForDurableCursor(t *testing.T) {
	store, config := openKitPostgresStore(t)
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
	store, config := openKitPostgresStore(t)
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
	if _, err := store.pool.Exec(context.Background(), "UPDATE juhe_dataset.runtime_log_index_owner_leases SET lease_until = '2000-01-01T00:00:00.000Z' WHERE lease_key = $1", runtimeLogOwnerLeaseKey); err != nil {
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
	store, config := openKitPostgresStore(t)
	secondOpened, err := OpenStore(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	second, ok := secondOpened.(*postgresStore)
	if !ok {
		t.Fatalf("测试预期第二个 PostgreSQL Store，实际为 %T", secondOpened)
	}
	t.Cleanup(func() { _ = second.Close() })

	lease, acquired, err := store.AcquireOwnerLease(context.Background(), "fence-owner", 2*time.Second)
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
	select {
	case <-entered:
	case err := <-fenceErr:
		t.Fatalf("fence 在进入回调前失败: %v", err)
	}
	// Let the original lease expire while its fencing transaction remains open.
	// 2s lease / 2.5s wait：租约必须覆盖 fence 入口校验的 PG 往返延迟
	//（实测每往返 ~10ms，入口需 5 个往返），等待须覆盖租约到期。
	time.Sleep(2500 * time.Millisecond)

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

func TestPostgresTransactionGuardsAreBounded(t *testing.T) {
	statements := postgresTransactionGuardStatements()
	want := []string{
		"SET LOCAL statement_timeout = '30s'",
		"SET LOCAL lock_timeout = '5s'",
		"SET LOCAL idle_in_transaction_session_timeout = '5s'",
	}
	if !reflect.DeepEqual(statements, want) {
		t.Fatalf("F1 PostgreSQL transaction guards 不匹配: got=%#v want=%#v", statements, want)
	}
}

func TestPostgresRebuildTransactionGuardsAreBounded(t *testing.T) {
	statements := postgresRebuildTransactionGuardStatements()
	want := []string{
		"SET LOCAL statement_timeout = '15min'",
		"SET LOCAL lock_timeout = '5s'",
	}
	if !reflect.DeepEqual(statements, want) {
		t.Fatalf("F1 facet 重建事务 guards 不匹配: got=%#v want=%#v", statements, want)
	}
}

func TestRuntimeRetentionDaysReadsBusinessSetting(t *testing.T) {
	store, _ := openKitPostgresStore(t)
	days, err := store.RuntimeRetentionDays(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if days != 14 {
		t.Fatalf("运行日志保留期必须读取业务设置：got %d want 14", days)
	}
}

func TestNormalizeLegacyNodeTimestampRejectsUnknownFormat(t *testing.T) {
	if _, err := normalizeNodeTimestamp("2026/08/17 21:12:43+00"); err == nil {
		t.Fatal("未知时间格式必须继续拒绝")
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

func TestPostgresTemporalReadsCastToTextForNodeCompatibleFacets(t *testing.T) {
	source, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatal(err)
	}
	// W23 守卫：facet 链 timestamptz 读出必须经 postgresNodeInstantText 归一
	// 为 nodeISO 毫秒文本（与 cutoff 同格式、同格式定长字符串的字典序=时间
	// 序），且禁止回到 PG 默认 ::text（空格分隔 + "+00"，与 nodeISO 字典序
	// 不可比，会把与 cutoff 同一自然日的记录系统性判早、facet 计数少计）。
	for _, fragment := range []string{
		"postgresNodeInstantText(\"time\")",
		"postgresNodeInstantText(\"earliest_time\")",
	} {
		if !strings.Contains(string(source), fragment) {
			t.Fatalf("PostgreSQL timestamptz facet 读出必须经 postgresNodeInstantText 归一（W23）: missing %q", fragment)
		}
	}
	for _, forbidden := range []string{
		"RETURNING time::text",
		"SELECT time::text",
		"earliest_time::text",
	} {
		if strings.Contains(string(source), forbidden) {
			t.Fatalf("PostgreSQL facet 链禁止回到 timestamptz 默认 ::text 格式（W23）: found %q", forbidden)
		}
	}
}

func TestPostgresSchemaBootstrapOnlyRunsForKnownMissingSchema(t *testing.T) {
	bootstrap, err := postgresRuntimeLogSchemaBootstrapRequired(nil)
	if err != nil || bootstrap {
		t.Fatalf("健康 PostgreSQL schema 必须跳过 bootstrap DDL: bootstrap=%t err=%v", bootstrap, err)
	}
	bootstrap, err = postgresRuntimeLogSchemaBootstrapRequired(fmt.Errorf("%w: 缺少索引", errPostgresRuntimeLogSchemaMissing))
	if err != nil || !bootstrap {
		t.Fatalf("已分类的 PostgreSQL schema 缺失必须允许 bootstrap: bootstrap=%t err=%v", bootstrap, err)
	}
	for _, checkErr := range []error{context.DeadlineExceeded, errors.New("permission denied")} {
		bootstrap, err = postgresRuntimeLogSchemaBootstrapRequired(checkErr)
		if err == nil || bootstrap {
			t.Fatalf("%v 不得触发 PostgreSQL bootstrap DDL: bootstrap=%t err=%v", checkErr, bootstrap, err)
		}
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

func TestOwnerLeaseReleasePreservesMonotonicFenceToken(t *testing.T) {
	store, _ := openKitPostgresStore(t)
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

// TestLoadConfigDefaultsMissingInstanceIDToHostname 覆盖 2026-09-19 零配置
// 决策：INSTANCE_ID 缺省取 os.Hostname()，不再必填报错。
func TestLoadConfigDefaultsMissingInstanceIDToHostname(t *testing.T) {
	values := map[string]string{
		"JUHE_AI_RUNTIME_LOG_POSTGRES_URL": "postgres://unit-test.invalid/f1",
		"JUHE_AI_LOG_DIR":                  "logs",
	}
	config, err := LoadConfig(func(name string) string { return values[name] })
	if err != nil {
		t.Fatalf("缺省 INSTANCE_ID 必须回落 hostname 而非报错: %v", err)
	}
	hostname, _ := os.Hostname()
	if !strings.HasPrefix(config.OwnerID, hostname+":") {
		t.Fatalf("OwnerID 必须由 hostname 缺省实例 ID 组成: %q (hostname=%q)", config.OwnerID, hostname)
	}
}

func TestLoadConfigAcceptsInstanceWithoutNodeGoOwnerSwitch(t *testing.T) {
	values := map[string]string{
		"JUHE_AI_RUNTIME_LOG_INSTANCE_ID":  "test-instance",
		"JUHE_AI_RUNTIME_LOG_POSTGRES_URL": "postgres://unit-test.invalid/f1",
		"JUHE_AI_LOG_DIR":                  "logs",
	}
	if _, err := LoadConfig(func(name string) string { return values[name] }); err != nil {
		t.Fatalf("运行日志索引不应依赖 Node/Go owner 开关，实际为 %v", err)
	}
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
	store, config := openKitPostgresStore(t)
	secondOpened, err := OpenStore(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	second, ok := secondOpened.(*postgresStore)
	if !ok {
		t.Fatalf("测试预期第二个 PostgreSQL Store，实际为 %T", secondOpened)
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

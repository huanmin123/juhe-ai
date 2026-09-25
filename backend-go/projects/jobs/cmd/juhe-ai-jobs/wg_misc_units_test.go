package main

import (
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accounthealth"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/circuitstore"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/accountprobe"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/proberepo"
)

// wgNewProbeStoreFixture 构造探针族存储（业务库 + proberepo.Store），
// 供电路恢复解析器与探针闭包直测。
func wgNewProbeStoreFixture(t *testing.T) *proberepo.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "business.sqlite3")
	seedProbeCoreTables(t, path)
	db, err := sqlOpenSQLiteFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := proberepo.NewStore(proberepo.Config{DB: db, Secret: wgBalanceSecret})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	return store
}

// TestCircuitRecoveryResolverBranches 覆盖电路恢复目标解析的全部否定分支
// （组合根适配器，正路径由调度冒烟的空 Sweep 覆盖）。
func TestCircuitRecoveryResolverBranches(t *testing.T) {
	store := wgNewProbeStoreFixture(t)
	resolver := circuitRecoveryTargetResolver{store: store}
	ctx := context.Background()

	// ctx 已取消 → 错误透传。
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := resolver.Resolve(canceled, opsjobs.CircuitState{}); err == nil {
		t.Fatal("取消的 ctx 必须报错")
	}
	// 运行态键无法解析身份 → (false, nil)。
	target, ok, err := resolver.Resolve(ctx, opsjobs.CircuitState{Scope: opsjobs.CircuitScope{AccountRuntimeKey: "::bad-key"}})
	if err != nil || ok || target.Probe != nil {
		t.Fatalf("非法运行态键必须返回 false: %v %v %v", target, ok, err)
	}
	// owner 身份但账户缺失 → false。
	target, ok, err = resolver.Resolve(ctx, opsjobs.CircuitState{Scope: opsjobs.CircuitScope{AccountRuntimeKey: "gateway:account:wg-missing"}})
	if err != nil || ok {
		t.Fatalf("缺失账户必须返回 false: %v %v %v", target, ok, err)
	}
}

// TestCircuitRecoveryTransportProbeTaskFailure 覆盖探针任务失败分支
// （observation 为空或探针报错 → unknown/task_failure，不计入失败证据）。
func TestCircuitRecoveryTransportProbeTaskFailure(t *testing.T) {
	store := wgNewProbeStoreFixture(t)
	service, err := accountprobe.NewService(accountprobe.Options{Source: store, Secret: wgBalanceSecret, Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	identity := opsjobs.RecoveryRuntimeIdentity{Kind: "owner", AccountID: "wg-missing"}
	// 零值 scope 无 modelBucket → 不钉住，回退健康检查模型（既有行为）。
	req := circuitRecoveryProbeRequest(identity, opsjobs.CircuitState{}, "g1", "sys1")
	outcome := circuitRecoveryTransportProbe(context.Background(), service, req)
	if outcome.Kind != opsjobs.ProbeOutcomeUnknown || outcome.FailureKind != opsjobs.ProbeFailureTaskFailure {
		t.Fatalf("任务失败必须分类为 unknown/task_failure: %+v", outcome)
	}
}

// TestSpeedFirstProbeFuncTaskFailure 覆盖速度优先探针闭包的任务失败分支。
func TestSpeedFirstProbeFuncTaskFailure(t *testing.T) {
	store := wgNewProbeStoreFixture(t)
	service, err := accountprobe.NewService(accountprobe.Options{Source: store, Secret: wgBalanceSecret, Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	probe := speedFirstProbeFunc(service)
	snapshot, outcome := probe(context.Background(), nil, opsjobs.ProbeCandidate{
		AccountID: "wg-missing",
	}, nil)
	if snapshot.Success {
		t.Fatalf("任务失败不得报告成功: %+v", snapshot)
	}
	if outcome.Kind != opsjobs.ProbeOutcomeUnknown || outcome.FailureKind != opsjobs.ProbeFailureTaskFailure {
		t.Fatalf("任务失败必须分类为 unknown/task_failure: %+v", outcome)
	}
}

// TestSlogWriterLoggerAndEnqueuer 覆盖 usagewriter 日志适配器与
// retention 队列投递适配器。
func TestSlogWriterLoggerAndEnqueuer(t *testing.T) {
	// slogWriterLogger（usagewriter 适配器）两个级别不 panic 即可。
	logger := slogWriterLogger{logger: assemblyTestLogger(t)}
	logger.Warn("wg_warn", map[string]any{"k": "v"})
	logger.Error("wg_error", map[string]any{"k": "v"})

	queue := newRecordMaintenanceQueue(queueLimits{MaxItems: 10, MaxBytes: 64 * 1024})
	enqueuer := &queueEnqueuer{queue: queue}
	if result := enqueuer.Enqueue(context.Background(), wgMaintenanceJob("api_key_related_cleanup", "x")); !result.Queued {
		t.Fatalf("正常投递必须成功: %+v", result)
	}
	if err := enqueuer.EnqueueAsync(context.Background(), wgMaintenanceJob("api_key_related_cleanup", "y")); err != nil {
		t.Fatalf("异步投递必须成功: %v", err)
	}
	// 队列满 → 异步投递显式报错。
	tiny := newRecordMaintenanceQueue(queueLimits{MaxItems: 0, MaxBytes: 4})
	if err := (&queueEnqueuer{queue: tiny}).EnqueueAsync(context.Background(), wgMaintenanceJob("api_key_related_cleanup", "z")); err == nil {
		t.Fatal("超限异步投递必须报错")
	}
}

// TestRegisterDisabledJobsStartupRegistersFamilyGaps 验证家族未启用时全部
// GoWired 任务显式登记 disabled（不静默消失）。
func TestRegisterDisabledJobsStartupRegistersFamilyGaps(t *testing.T) {
	assembly := newWorkerAssembly(workerConfig{Driver: "sqlite"}, assemblyTestLogger(t))
	registerDisabledJobsStartup(assembly, assembly.logger)
	if len(assembly.disabledJobs) == 0 {
		t.Fatal("空装配必须登记全部 GoWired 任务为 disabled")
	}
}

// TestWireHealthProbeFenceSettlerWithRedis 覆盖 fence 结算装配的 Redis
// 分支（settler 与 closer 非 nil；无 Redis 分支由既有测试覆盖 warn 语义）。
func TestWireHealthProbeFenceSettlerWithRedis(t *testing.T) {
	redisServer := miniredis.RunT(t)
	assembly := newWorkerAssembly(workerConfig{
		Driver:         "sqlite",
		RedisStateURL:  "redis://" + redisServer.Addr(),
		RedisNamespace: "juhe-ai:wg-test",
	}, assemblyTestLogger(t))
	settler, closer, err := assembly.wireHealthProbeFenceSettler()
	if err != nil || settler == nil || closer == nil {
		t.Fatalf("Redis 就绪时必须装配 settler: %v %v", settler, err)
	}
	t.Cleanup(func() { _ = closer() })
	if err := settler(context.Background(), sourceFenceFixture(), "unknown"); err != nil {
		// 无对应键的结算允许返回错误（键不存在语义），只验证调用面不 panic。
		t.Logf("空键结算返回: %v", err)
	}
}

// TestProjectionRuntimeProbeAndCredentialsAdapter 覆盖列表投影的运行态
// 探针与凭据适配器（Redis 经 miniredis）。
func TestProjectionRuntimeProbeAndCredentialsAdapter(t *testing.T) {
	redisServer := miniredis.RunT(t)
	overlayStore, err := circuitstore.NewOverlayRedisStore(circuitstore.OverlayRedisConfig{
		URL: "redis://" + redisServer.Addr(), Namespace: "juhe-ai:wg-test",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = overlayStore.Close() })
	runtimeReader, err := circuitstore.NewRuntimeStateReader("redis://"+redisServer.Addr(), "juhe-ai:wg-test", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtimeReader.Close() })
	probe := projectionRuntimeProbe{
		concurrency: circuitstore.NewOverlayConcurrencySource(overlayStore),
		runtime:     runtimeReader,
	}
	if _, _, err := probe.Probe(context.Background()); err != nil {
		t.Fatalf("合成键运行态探针必须成功: %v", err)
	}
}

// TestFamilyTableDrainRunnerSnapshotUpsertsAdapter 覆盖 record_maintenance
// 表 drain 的批量快照适配器（接线后空批次直通，真执行链路由
// TestWorkerAssemblySnapshotUpsertChannel 覆盖）。
func TestFamilyTableDrainRunnerSnapshotUpsertsAdapter(t *testing.T) {
	redisServer := miniredis.RunT(t)
	dir := t.TempDir()
	wgSeedAllJobsFixture(t, dir)
	assembly, err := buildWorkerAssembly(loadWorkerConfigOrFatal(t, wgAllJobsAssemblyEnv(t, dir, "redis://"+redisServer.Addr())), nil)
	if err != nil {
		t.Fatalf("buildWorkerAssembly: %v", err)
	}
	defer assembly.closeStores()
	if assembly.retention == nil {
		t.Fatal("retention family 必须装配")
	}
	runner := familyTableDrainRunner{family: assembly.retention}
	if _, err := runner.RunAccountUsageSnapshotUpserts(context.Background(), nil); err != nil {
		t.Fatalf("RunAccountUsageSnapshotUpserts(nil): %v", err)
	}
	// familyStatsWriter.store() 按契约恒 nil（StatsWriter 窄端口）。
	if (&familyStatsWriter{family: assembly.retention}).store() != nil {
		t.Fatal("familyStatsWriter.store() 必须为 nil")
	}
}

// TestFlushLoopRetainsFailingJob 验证本地队列 flush 循环失败重试语义：
// 失败任务保留队头（重新入队），停机信号正常退出。
func TestFlushLoopRetainsFailingJob(t *testing.T) {
	redisServer := miniredis.RunT(t)
	dir := t.TempDir()
	wgSeedAllJobsFixture(t, dir)
	assembly, err := buildWorkerAssembly(loadWorkerConfigOrFatal(t, wgAllJobsAssemblyEnv(t, dir, "redis://"+redisServer.Addr())), nil)
	if err != nil {
		t.Fatalf("buildWorkerAssembly: %v", err)
	}
	defer assembly.closeStores()
	family := assembly.retention
	queue := newRecordMaintenanceQueue(queueLimits{MaxItems: 10, MaxBytes: 64 * 1024})
	// 未知任务类型 → runner 报错 → flush 循环重入队等待重试。
	queue.enqueue(retention.RecordMaintenanceJob{Type: "wg-unknown-type", ID: "x"})
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		family.flushLoop(stop, queue)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if queue.size() == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(stop)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("flush 循环未按停机信号退出")
	}
	if queue.size() != 1 {
		t.Fatalf("失败任务必须保留等待重试: %d", queue.size())
	}
}

// ---- 本文件专用小助手 ----

// sqlOpenSQLiteFile 打开既有 SQLite 文件（WAL）。
func sqlOpenSQLiteFile(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec("PRAGMA journal_mode = WAL;"); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// assemblyTestLogger 返回写到测试日志的 slog logger。
func assemblyTestLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(testWriter{t}, nil))
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(string(p))
	return len(p), nil
}

// sourceFenceFixture 构造最小 SourceFence（键不存在时结算按空语义处理）。
func sourceFenceFixture() accounthealth.SourceFence {
	return accounthealth.SourceFence{
		StateKey:         "wg:state",
		AccountID:        "wg-account",
		SourceGeneration: 1,
		SourceFenceID:    "wg-fence",
		RuntimeKey:       "gateway:account:wg-account",
		ProbeGeneration:  1,
		ConfigRevision:   1,
	}
}

// loadWorkerConfigOrFatal 加载 worker 配置（整合测试共用）。
func loadWorkerConfigOrFatal(t *testing.T, env map[string]string) workerConfig {
	t.Helper()
	config, err := loadWorkerConfig(getenvFrom(env))
	if err != nil {
		t.Fatalf("loadWorkerConfig: %v", err)
	}
	return config
}

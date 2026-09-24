package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobsched"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobssettings"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsagg"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsverify"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/taskruns"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accountbalance"
)

// TestStaticSettingsDefaults 验证无数据库设置源的 Node 默认值语义
// （statsAggregationBatchSize=2000、MaxBatches=5）。
func TestStaticSettingsDefaults(t *testing.T) {
	ctx := context.Background()
	static := staticSettings{}
	batchSize, err := static.statsAggregationBatchSize(ctx)
	if err != nil || batchSize != 2000 {
		t.Fatalf("静态批次大小必须是 2000: %d %v", batchSize, err)
	}
	maxBatches, err := static.statsAggregationMaxBatches(ctx)
	if err != nil || maxBatches != 5 {
		t.Fatalf("静态批次数必须是 5: %d %v", maxBatches, err)
	}
}

// TestOpenSQLiteWALAndRegistry 验证 openSQLite 注册连接与 PRAGMA 失败分支。
func TestOpenSQLiteWALAndRegistry(t *testing.T) {
	assembly := newWorkerAssembly(workerConfig{Driver: "sqlite"}, nil)
	defer assembly.closeStores()
	db, err := assembly.openSQLite(filepath.Join(t.TempDir(), "unit.sqlite3"), "unit")
	if err != nil {
		t.Fatalf("打开合法 SQLite 必须成功: %v", err)
	}
	if len(assembly.sqliteDBs) != 1 {
		t.Fatalf("连接必须登记到 sqliteDBs: %d", len(assembly.sqliteDBs))
	}
	if _, err := db.Exec("CREATE TABLE t (id TEXT)"); err != nil {
		t.Fatalf("连接必须可用: %v", err)
	}
	// 目录不存在 → sql.Open 惰性成功，PRAGMA WAL 执行失败 → 显式报错。
	if _, err := assembly.openSQLite(filepath.Join(t.TempDir(), "missing-dir", "x.sqlite3"), "bad"); err == nil {
		t.Fatal("非法路径必须报错")
	}
}

// TestCloseStoresReportsCloserError 验证 closer 报错时逆向关闭继续执行。
func TestCloseStoresReportsCloserError(t *testing.T) {
	assembly := newWorkerAssembly(workerConfig{Driver: "sqlite"}, slog.Default())
	order := []string{}
	assembly.addCloser(func() error { order = append(order, "first"); return nil })
	assembly.addCloser(func() error { order = append(order, "second"); return errors.New("关闭失败") })
	assembly.closeStores()
	if len(order) != 2 || order[0] != "second" || order[1] != "first" {
		t.Fatalf("closeStores 必须逆向执行全部 closer: %v", order)
	}
	if assembly.closers != nil {
		t.Fatal("closeStores 后 closer 清单必须清空")
	}
}

// TestStatusPayloadWithoutScheduler 验证手工构造的最小装配体也能输出健康载荷
// （nil 调度器分支不 panic）。
func TestStatusPayloadWithoutScheduler(t *testing.T) {
	assembly := &workerAssembly{config: workerConfig{Driver: "sqlite"}}
	payload := assembly.statusPayload()
	if payload["workerEnabled"] != true || payload["workerDriver"] != "sqlite" {
		t.Fatalf("健康载荷基础字段错误: %v", payload)
	}
	if _, ok := payload["workerJobs"].([]jobsched.Snapshot); !ok {
		t.Fatalf("nil 调度器必须输出空快照列表: %T", payload["workerJobs"])
	}
}

// TestRankSnapshotCoreStagesBranches 锁定 usage-rank-snapshots-refresh 的
// databaseDriver 注册分叉：PG 剔除 ai_performance_summary_windows。
func TestRankSnapshotCoreStagesBranches(t *testing.T) {
	sqliteStages := rankSnapshotCoreStages(false)
	pgStages := rankSnapshotCoreStages(true)
	if len(sqliteStages) != len(pgStages)+1 {
		t.Fatalf("SQLite 分支必须多一个 ai_performance stage: sqlite=%d pg=%d", len(sqliteStages), len(pgStages))
	}
	for _, stage := range pgStages {
		if stage == statsagg.StageAiPerformanceSummaryWindows {
			t.Fatal("PG 分支不得包含 ai_performance_summary_windows")
		}
	}
	hot := hotUsageWindowStages()
	if len(hot) != 2 {
		t.Fatalf("热窗口 stage 数错误: %v", hot)
	}
}

// TestRegisterDisabledJobLogsUnknownRegistry 验证非注册表 job 的登记分支。
func TestRegisterDisabledJobLogsUnknownRegistry(t *testing.T) {
	assembly := newWorkerAssembly(workerConfig{Driver: "sqlite"}, slog.Default())
	assembly.registerDisabledJob("wg-not-in-registry", "测试缺口")
	if len(assembly.disabledJobs) != 1 || assembly.disabledJobs[0].JobName != "wg-not-in-registry" {
		t.Fatalf("登记清单错误: %+v", assembly.disabledJobs)
	}
}

// TestRunWiredJobOnceUnknownName 验证未注册任务的显式报错。
func TestRunWiredJobOnceUnknownName(t *testing.T) {
	assembly := newWorkerAssembly(workerConfig{Driver: "sqlite"}, nil)
	if _, err := assembly.runWiredJobOnce(context.Background(), "wg-nope"); err == nil {
		t.Fatal("未注册任务必须报错")
	}
}

// noopWiredTask 是调度注册测试的空任务。
func noopWiredTask(_ context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
	return jobsched.TaskResult{}, nil
}

// TestScheduleWiredJobRejectsNonGoWired 验证非 GoWired 名称被拒绝注册。
func TestScheduleWiredJobRejectsNonGoWired(t *testing.T) {
	assembly := newWorkerAssembly(workerConfig{Driver: "sqlite"}, slog.Default())
	assembly.scheduleWiredJob("wg-definitely-not-a-job", noopWiredTask)
	if len(assembly.wiredJobs) != 0 || len(assembly.wiredTasks) != 0 {
		t.Fatalf("非 GoWired 任务不得注册: %v", assembly.wiredJobs)
	}
}

// TestScheduleWiredJobDriverForkRejectsRegistration 验证 PG 独占任务在
// ResolveScheduleForDriver 反向分支下拒绝注册（T6b 冻结语义）。
func TestScheduleWiredJobDriverForkRejectsRegistration(t *testing.T) {
	assembly := newWorkerAssembly(workerConfig{Driver: "postgres"}, slog.Default())
	// usage-scope-range-windows-refresh 仅默认/SQLite 分支注册。
	assembly.scheduleWiredJob("usage-scope-range-windows-refresh", noopWiredTask)
	if len(assembly.wiredJobs) != 0 {
		t.Fatalf("PG 分支不得注册 SQLite 独占任务: %v", assembly.wiredJobs)
	}
}

// TestTaskRunReconcileRepoInputValidation 覆盖 taskRunReconcileRepo 的
// RFC3339 输入校验分支与正常透传。
func TestTaskRunReconcileRepoInputValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "task-runs.sqlite3")
	store, err := taskruns.OpenStore(taskruns.StoreConfig{Mode: taskruns.ModeSQLite, DatabasePath: path})
	if err != nil {
		t.Fatalf("open taskruns store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	repo := &taskRunReconcileRepo{store: store}
	ctx := context.Background()
	if _, err := repo.ReconcileStale(ctx, opsjobs.TaskRunReconcileInput{QueuedBefore: "not-a-time", RunningHeartbeatBefore: "2026-01-01T00:00:00Z", Limit: 10}); err == nil {
		t.Fatal("非法 queuedBefore 必须报错")
	}
	if _, err := repo.ReconcileStale(ctx, opsjobs.TaskRunReconcileInput{QueuedBefore: "2026-01-01T00:00:00Z", RunningHeartbeatBefore: "nope", Limit: 10}); err == nil {
		t.Fatal("非法 runningHeartbeatBefore 必须报错")
	}
	if _, err := repo.ReconcileStale(ctx, opsjobs.TaskRunReconcileInput{QueuedBefore: "2026-01-01T00:00:00Z", RunningHeartbeatBefore: "2026-01-01T00:00:00Z", Now: "bad-now", Limit: 10}); err == nil {
		t.Fatal("非法 now 必须报错")
	}
	result, err := repo.ReconcileStale(ctx, opsjobs.TaskRunReconcileInput{
		QueuedBefore:           "2026-01-01T00:00:00.000Z",
		RunningHeartbeatBefore: "2026-01-01T00:00:00.000Z",
		Now:                    "2026-01-01T00:00:00.000Z",
		Limit:                  10,
	})
	if err != nil {
		t.Fatalf("合法输入必须成功: %v", err)
	}
	if result.FailedQueuedCount != 0 || result.FailedRunningCount != 0 || result.DeletedExpiredLeaseCount != 0 {
		t.Fatalf("空库对账结果必须为 0: %+v", result)
	}
}

// TestWithLeaseWrapsTaskWhenPostgres 验证 withLease 在 PG 语义下包裹租约
// （存储本身用 SQLite 实现，租约流程与 driver 无关）。
func TestWithLeaseWrapsTaskWhenPostgres(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leases.sqlite3")
	store, err := taskruns.OpenStore(taskruns.StoreConfig{Mode: taskruns.ModeSQLite, DatabasePath: path})
	if err != nil {
		t.Fatalf("open taskruns store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	assembly := &workerAssembly{
		config:        workerConfig{Driver: "postgres", InstanceID: "wg", WorkerRole: "worker", WorkerReplicaIdx: 3},
		taskRunsStore: store,
	}
	executed := false
	wrapped := assembly.withLease("background-task-run-reconcile", time.Minute, func(_ context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
		executed = true
		return jobsched.TaskResult{}, nil
	})
	result, err := wrapped(context.Background(), jobsched.TaskContext{})
	if err != nil || !executed {
		t.Fatalf("租约包裹任务必须执行: executed=%v err=%v", executed, err)
	}
	if result.Outcome != jobsched.OutcomeSuccess {
		t.Fatalf("成功任务必须返回 success: %+v", result)
	}
	// 任务失败路径：错误必须透传。
	failing := assembly.withLease("background-task-run-reconcile", time.Minute, func(_ context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
		return jobsched.TaskResult{}, errors.New("任务失败")
	})
	if _, err := failing(context.Background(), jobsched.TaskContext{}); err == nil {
		t.Fatal("任务失败必须透传错误")
	}
	// ownerID 必须包含 instance:role:replica 三元组与随机 token。
	ownerA := assembly.ownerID()
	ownerB := assembly.ownerID()
	if ownerA == "" || ownerA == ownerB {
		t.Fatalf("ownerID 必须非空且每次随机: %q %q", ownerA, ownerB)
	}
	if !strings.Contains(ownerA, "wg") || !strings.Contains(ownerA, "worker") || !strings.Contains(ownerA, "3") {
		t.Fatalf("ownerID 必须包含三元组: %q", ownerA)
	}
}

// TestWithLeaseSkipsWhenSQLiteOrNoStore 验证 SQLite/无租约存储时任务直跑。
func TestWithLeaseSkipsWhenSQLiteOrNoStore(t *testing.T) {
	assembly := &workerAssembly{config: workerConfig{Driver: "sqlite"}}
	called := false
	wrapped := assembly.withLease("usage-stats-aggregation", time.Minute, func(_ context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
		called = true
		return jobsched.TaskResult{}, nil
	})
	if _, err := wrapped(context.Background(), jobsched.TaskContext{}); err != nil || !called {
		t.Fatalf("SQLite 必须直跑任务: %v %v", called, err)
	}
	nilStoreAssembly := &workerAssembly{config: workerConfig{Driver: "postgres"}}
	nilStoreCalled := false
	nilStoreWrapped := nilStoreAssembly.withLease("usage-stats-aggregation", time.Minute, func(_ context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
		nilStoreCalled = true
		return jobsched.TaskResult{}, nil
	})
	if _, err := nilStoreWrapped(context.Background(), jobsched.TaskContext{}); err != nil || !nilStoreCalled {
		t.Fatalf("无租约存储必须直跑任务: %v %v", nilStoreCalled, err)
	}
}

// TestMarkAllGroupAccountStatsMarkerPaths 覆盖 stats 家族缺席 warn 与
// 真实脏标记两条路径（既有测试只覆盖 marker 接口分支）。
func TestMarkAllGroupAccountStatsMarkerPaths(t *testing.T) {
	ctx := context.Background()
	// marker 为 nil（stats 家族缺席）→ 显式 warn 且不报错。
	if err := markAllGroupAccountStatsAfterAuthzWrite(ctx, slog.Default(), nil, "authorization_expired"); err != nil {
		t.Fatalf("缺席 marker 必须显式 warn 而非报错: %v", err)
	}
	// 真实 marker：SQLite statsverify store。
	root := t.TempDir()
	store, err := statsverify.OpenStore(statsverify.StoreConfig{
		Mode:               statsverify.StoreSQLite,
		SQLiteStatsPath:    filepath.Join(root, "stats.sqlite3"),
		SQLiteBusinessPath: filepath.Join(root, "business.sqlite3"),
	})
	if err != nil {
		t.Fatalf("open statsverify store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	if err := markAllGroupAccountStatsAfterAuthzWrite(ctx, slog.Default(), store, "authorization_expired"); err != nil {
		t.Fatalf("真实脏标记必须成功: %v", err)
	}
}

// TestProbeSettingsSourceFallsBackOnError 验证探针设置源读取失败时回落
// DEFAULT_SYSTEM_SETTINGS 并继续（无错误通道的消费语义）。
func TestProbeSettingsSourceFallsBackOnError(t *testing.T) {
	root := t.TempDir()
	businessPath := filepath.Join(root, "business.sqlite3")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(businessPath)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// 不建 system_settings 表 → SQLite 读模型按语义回落默认。
	assembly := newWorkerAssembly(workerConfig{Driver: "sqlite"}, slog.Default())
	settings := assembly.probeSettingsSource(&businessDB{db: db})
	value := settings("accountQualityWindowMinutes", 5, 120)
	if value < 5 || value > 120 {
		t.Fatalf("回落的窗口分钟数必须落在边界内: %d", value)
	}
}

// TestResolveScheduleSettingsInterval 覆盖设置驱动间隔的命中、上限收敛、
// 读取失败回落与未知 job 分支。
func TestResolveScheduleSettingsInterval(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(root, "settings.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE system_settings (system_account_id TEXT NOT NULL, key TEXT NOT NULL, value_json TEXT NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY (system_account_id, key))`); err != nil {
		t.Fatal(err)
	}
	insertSetting := func(t *testing.T, key, valueJSON string) {
		t.Helper()
		if _, err := db.Exec(`INSERT OR REPLACE INTO system_settings VALUES ('sys_admin', ?, ?, '2026-09-10T00:00:00.000Z')`, key, valueJSON); err != nil {
			t.Fatal(err)
		}
	}
	source := jobssettings.NewSource(jobssettings.Options{DB: db, Mode: jobssettings.SQLite})

	// 未知 job：不命中键表。
	if _, ok := resolveScheduleSettingsInterval(ctx, source, nil, "wg-unknown-job"); ok {
		t.Fatal("未知 job 不得返回设置间隔")
	}
	// 缺行 → 读模型回落 DEFAULT_SYSTEM_SETTINGS（statsAggregationIntervalSeconds
	// 默认 60，与 OnlineFreshnessCap=60 相同）→ 间隔恰为 60s。
	interval, ok := resolveScheduleSettingsInterval(ctx, source, nil, "usage-stats-aggregation")
	if !ok || interval != 60*time.Second {
		t.Fatalf("缺行默认间隔必须是 60s: %v %v", interval, ok)
	}
	// 无 OnlineFreshnessCap 的 job：显式行值原样生效。
	insertSetting(t, "systemMetricsSampleIntervalSeconds", "120")
	interval, ok = resolveScheduleSettingsInterval(ctx, source, nil, "system-metrics-sample")
	if !ok || interval != 120*time.Second {
		t.Fatalf("显式设置值必须生效: %v %v", interval, ok)
	}
	// OnlineFreshnessCap：超大值收敛到 60s。
	insertSetting(t, "usageStatsAggregationIntervalSeconds", "3600")
	interval, ok = resolveScheduleSettingsInterval(ctx, source, nil, "usage-stats-aggregation")
	if !ok || interval != 60*time.Second {
		t.Fatalf("在线新鲜度上限必须收敛到 60s: %v %v", interval, ok)
	}
	// 非整数 → 读失败 → warn + false（Node settingsNumber 失败语义）。
	// client-ip-stats-aggregation 与 usage-stats-aggregation 共用
	// statsAggregationIntervalSeconds 键（jobregistry.SettingsIntervalJobNames）；
	// Source 带 60s TTL 缓存，失败分支必须换新实例才能读到新行。
	insertSetting(t, "statsAggregationIntervalSeconds", `"abc"`)
	failingSource := jobssettings.NewSource(jobssettings.Options{DB: db, Mode: jobssettings.SQLite})
	if _, ok := resolveScheduleSettingsInterval(ctx, failingSource, slog.Default(), "client-ip-stats-aggregation"); ok {
		t.Fatal("非法设置值必须回落注册表默认（ok=false）")
	}
}

// TestIngestDrainGateWithoutWriter 验证 usagewriter 缺席时门控按
// 「快照不可用」失败本轮（Node ingest worker 不可达语义）。
func TestIngestDrainGateWithoutWriter(t *testing.T) {
	assembly := newWorkerAssembly(workerConfig{Driver: "sqlite"}, nil)
	probe := assembly.ingestDrainProbe()
	// probe 本身是闭包；writer 缺席在调用时返回 (nil, nil) 等价 Node
	// ingest worker 不可达。
	status, err := probe(context.Background())
	if err != nil || status != nil {
		t.Fatalf("writer 缺席时 probe 必须返回 nil 状态: %v %v", status, err)
	}
	gate := assembly.ingestDrainGate()
	if err := gate(context.Background()); err == nil {
		t.Fatal("writer 缺席时门控必须失败本轮")
	}
	if _, err := assembly.ingestDrainSafety(context.Background()); err == nil {
		t.Fatal("writer 缺席时 safety 必须失败")
	}
	// ctx 取消时 probe 直接返回 ctx 错误。
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := probe(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消的 ctx 必须透传错误: %v", err)
	}
}

// TestBusinessDBDialectHelpers 覆盖业务库句柄的方言助手矩阵。
func TestBusinessDBDialectHelpers(t *testing.T) {
	sqlite := &businessDB{}
	if sqlite.table("accounts") != "accounts" {
		t.Fatalf("SQLite 表名不加前缀: %s", sqlite.table("accounts"))
	}
	if bound := sqlite.bind("WHERE id = ? AND name = ?"); bound != "WHERE id = ? AND name = ?" {
		t.Fatalf("SQLite bind 原样返回: %s", bound)
	}
	pg := &businessDB{postgres: true}
	if pg.table("accounts") != "juhe_business.accounts" {
		t.Fatalf("PG 表名必须限定 schema: %s", pg.table("accounts"))
	}
	if bound := pg.bind("WHERE id = ? AND name = ?"); bound != "WHERE id = $1 AND name = $2" {
		t.Fatalf("PG bind 必须改写占位符: %s", bound)
	}
	if statsTable(true, "account_usage_snapshots") != "juhe_stats.account_usage_snapshots" {
		t.Fatal("PG 统计表必须限定 juhe_stats")
	}
	if statsTable(false, "account_usage_snapshots") != "account_usage_snapshots" {
		t.Fatal("SQLite 统计表不加前缀")
	}
	if timeParam(true, time.Unix(0, 0).UTC()) != time.Unix(0, 0).UTC() {
		t.Fatal("PG 时间参数必须保持 time.Time")
	}
	if text, ok := timeParam(false, time.Unix(0, 0).UTC()).(string); !ok || text != "1970-01-01T00:00:00Z" {
		t.Fatalf("SQLite 时间参数必须是 RFC3339Nano 文本: %#v", timeParam(false, time.Unix(0, 0).UTC()))
	}
	if textParam("x") != "x" {
		t.Fatal("textParam 必须原样返回")
	}
	cases := []struct {
		postgres bool
		value    bool
		expected string
	}{
		// accounts 布尔标志列在 PG 里也是 integer(0/1)，两个方言统一输出数字，
		// PG 输出 TRUE/FALSE 会报 operator does not exist: integer = boolean。
		{true, true, "1"}, {true, false, "0"}, {false, true, "1"}, {false, false, "0"},
	}
	for _, item := range cases {
		if got := boolLit(item.postgres, item.value); got != item.expected {
			t.Fatalf("boolLit(%v,%v)=%s 期望 %s", item.postgres, item.value, got, item.expected)
		}
	}
}

// TestScanNullTimeMatrix 覆盖可空时间扫描的全部类型分支。
func TestScanNullTimeMatrix(t *testing.T) {
	valid := "2026-09-10T08:30:00.123456789Z"
	expected := time.Date(2026, 9, 10, 8, 30, 0, 123456789, time.UTC)
	cases := []struct {
		name     string
		postgres bool
		value    any
		wantNil  bool
		wantTime time.Time
		wantErr  bool
	}{
		{"nil 值", false, nil, true, time.Time{}, false},
		{"SQLite 字符串", false, valid, false, expected, false},
		{"SQLite 字节串", false, []byte(valid), false, expected, false},
		{"SQLite time.Time", false, expected, false, expected, false},
		{"SQLite 非法字符串", false, "nope", false, time.Time{}, true},
		{"SQLite 非法字节串", false, []byte("bad"), false, time.Time{}, true},
		{"SQLite 非法类型", false, 42, false, time.Time{}, true},
		{"PG time.Time", true, expected, false, expected, false},
		{"PG 字节串", true, []byte(valid), false, expected, false},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			parsed, err := scanNullTime(item.postgres, item.value)
			if item.wantErr {
				if err == nil {
					t.Fatalf("必须报错，得到 %v", parsed)
				}
				return
			}
			if err != nil {
				t.Fatalf("必须成功: %v", err)
			}
			if item.wantNil {
				if parsed != nil {
					t.Fatalf("必须返回 nil，得到 %v", parsed)
				}
				return
			}
			if parsed == nil || !parsed.Equal(item.wantTime) {
				t.Fatalf("时间不一致: %v 期望 %v", parsed, item.wantTime)
			}
		})
	}
}

// TestEnsureAccountsBalanceColumns 验证业务库契约校验：缺列 fail closed、
// 列齐全放行。
func TestEnsureAccountsBalanceColumns(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(root, "business.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	missing := &businessDB{db: db}
	if err := ensureAccountsBalanceColumns(ctx, missing); err == nil {
		t.Fatal("缺 accounts 表必须报错")
	}
	if _, err := db.Exec(`CREATE TABLE accounts (balance_query_enabled INTEGER, balance_query_config_json TEXT, balance_query_next_refresh_at TEXT, config_revision INTEGER, credentials_encrypted TEXT, proxy_profile_id TEXT, dispatch_revision INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if err := ensureAccountsBalanceColumns(ctx, missing); err != nil {
		t.Fatalf("列齐全必须放行: %v", err)
	}
}

// TestBalanceConfigJSONHelpers 锁定余额配置 JSON 的归一化与等价判定
// （Node camelCase 键不得改变）。
func TestBalanceConfigJSONHelpers(t *testing.T) {
	// intervalMinutes=0 归一为 5；preferred 为空省略。
	normalized, err := normalizeBalanceConfigJSON("builtin", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if normalized != `{"adapter":"builtin","intervalMinutes":5}` {
		t.Fatalf("归一化输出漂移: %s", normalized)
	}
	withPreferred, err := normalizeBalanceConfigJSON("builtin", 30, "openai_user_balance")
	if err != nil {
		t.Fatal(err)
	}
	if withPreferred != `{"adapter":"builtin","intervalMinutes":30,"preferredBuiltinAdapter":"openai_user_balance"}` {
		t.Fatalf("preferred 字段输出漂移: %s", withPreferred)
	}
	if !balanceConfigJSONEqual(`{"adapter":"builtin","intervalMinutes":10}`, normalizeBalanceConfigJSONMust(t, "builtin", 10, "")) {
		t.Fatal("语义相同的配置必须判定相等")
	}
	if balanceConfigJSONEqual(`{"adapter":"builtin","intervalMinutes":10}`, normalizeBalanceConfigJSONMust(t, "builtin", 30, "")) {
		t.Fatal("不同配置不得判定相等")
	}
	if balanceConfigJSONEqual(`not-json`, `{"adapter":"builtin"}`) {
		t.Fatal("非法存储 JSON 必须判定不等")
	}
	if balanceConfigJSONEqual(`"string"`, `{"adapter":"builtin"}`) {
		t.Fatal("非对象存储 JSON 必须判定不等")
	}
}

func normalizeBalanceConfigJSONMust(t *testing.T, adapter string, interval int, preferred string) string {
	t.Helper()
	text, err := normalizeBalanceConfigJSON(adapter, interval, preferred)
	if err != nil {
		t.Fatal(err)
	}
	return text
}

// TestBalanceValueHelpers 覆盖余额探测的值转换助手。
func TestBalanceValueHelpers(t *testing.T) {
	if !hasAtLeastOneAPIKey(map[string]any{"api_keys": []any{"sk-a", "sk-b"}}) {
		t.Fatal("api_keys 列表非空必须命中")
	}
	if hasAtLeastOneAPIKey(map[string]any{"api_keys": []any{"  ", ""}}) {
		t.Fatal("api_keys 全空白不得命中")
	}
	if hasAtLeastOneAPIKey(map[string]any{"api_keys": []any{}}) {
		t.Fatal("空 api_keys 不得命中")
	}
	if !hasAtLeastOneAPIKey(map[string]any{"api_key": " sk-x "}) {
		t.Fatal("非空 api_key 必须命中")
	}
	if hasAtLeastOneAPIKey(map[string]any{"api_key": "   "}) {
		t.Fatal("空白 api_key 不得命中")
	}
	if hasAtLeastOneAPIKey(map[string]any{}) {
		t.Fatal("无 Key 字段不得命中")
	}
	if textOrEmpty("x") != "x" || textOrEmpty(42) != "" {
		t.Fatal("textOrEmpty 类型分支错误")
	}
	if intOrZero(float64(7.9)) != 7 || intOrZero(3) != 3 || intOrZero(json.Number("9")) != 9 || intOrZero("x") != 0 {
		t.Fatal("intOrZero 类型分支错误")
	}
	if _, err := parseBalanceInstant("bad"); err == nil {
		t.Fatal("非法时间戳必须报错")
	}
	parsed, err := parseBalanceInstant("2026-09-10T00:00:00Z")
	if err != nil || parsed.IsZero() {
		t.Fatalf("合法时间戳必须解析: %v %v", parsed, err)
	}
	if optionalString("") != nil || *optionalString("v") != "v" {
		t.Fatal("optionalString 空值分支错误")
	}
	if nullableTextPtr(nil) != nil || nullableTextPtr(ptrString("x")) != "x" {
		t.Fatal("nullableTextPtr 空值分支错误")
	}
	// decryptCredentials：明文 JSON、合法封套、非法封套三条路径。
	runtime := &balanceDetectRuntime{secret: "0123456789abcdef0123456789abcdef"}
	plain, ok := runtime.decryptCredentials(`{"api_key":"sk-1"}`)
	if !ok || len(plain) != 1 {
		t.Fatalf("明文 JSON 凭据必须接受: %v %v", plain, ok)
	}
	if _, ok := runtime.decryptCredentials(`{"api_key":"  "}`); ok {
		t.Fatal("无 Key 凭据必须拒绝")
	}
	envelope, err := accountbalance.EncryptV1Envelope(runtime.secret, []byte(`{"api_key":"sk-env"}`))
	if err != nil {
		t.Fatal(err)
	}
	decrypted, ok := runtime.decryptCredentials(envelope)
	if !ok || decrypted["api_key"] != "sk-env" {
		t.Fatalf("封套凭据必须解密: %v %v", decrypted, ok)
	}
	if _, ok := runtime.decryptCredentials("not-an-envelope"); ok {
		t.Fatal("非法封套必须拒绝")
	}
}

// wgMaintenanceJob 构造一个最小 record-maintenance 任务。
func wgMaintenanceJob(jobType, id string) retention.RecordMaintenanceJob {
	return retention.RecordMaintenanceJob{Type: jobType, ID: id, APIKeyID: id, AccountID: id, SystemAccountID: "sys_wg"}
}

// TestRecordMaintenanceQueue 验证本地维护队列的入队/截断/超限丢弃与停机排空。
func TestRecordMaintenanceQueue(t *testing.T) {
	job := wgMaintenanceJob("api_key", "acc-1")
	queue := newRecordMaintenanceQueue(queueLimits{MaxItems: 2, MaxBytes: 64 * 1024})
	if queued, reason := queue.enqueue(job); !queued || reason != "" {
		t.Fatalf("正常入队必须成功: %v %v", queued, reason)
	}
	// 队列满 → 显式丢弃并说明原因。
	queue.enqueue(job)
	if queued, reason := queue.enqueue(job); queued || reason != "worker_local_queue_full" {
		t.Fatalf("超限必须丢弃: %v %v", queued, reason)
	}
	if queue.size() != 2 {
		t.Fatalf("队列长度错误: %d", queue.size())
	}
	batch := queue.takeBatch(1)
	if len(batch) != 1 || queue.size() != 1 {
		t.Fatalf("takeBatch 截断错误: %d/%d", len(batch), queue.size())
	}
	// oversize：单任务超过 MaxBytes。
	tinyQueue := newRecordMaintenanceQueue(queueLimits{MaxItems: 10, MaxBytes: 4})
	if queued, reason := tinyQueue.enqueue(job); queued || reason != "oversize" {
		t.Fatalf("超大任务必须丢弃: %v %v", queued, reason)
	}
	if queue.size() != 1 {
		t.Fatalf("队列长度错误: %d", queue.size())
	}
	// estimateJobBytes 正常序列化。
	if estimateJobBytes(job) <= 0 {
		t.Fatal("任务字节数必须大于 0")
	}
}

// TestRecordMaintenanceQueueDrainShutdown 验证停机排空的失败即停语义。
func TestRecordMaintenanceQueueDrainShutdown(t *testing.T) {
	job := wgMaintenanceJob("account", "acc-2")
	queue := newRecordMaintenanceQueue(queueLimits{MaxItems: 10, MaxBytes: 64 * 1024})
	queue.enqueue(job)
	queue.drainShutdown(func(_ context.Context, _ retention.RecordMaintenanceJob) (map[string]any, error) {
		return nil, errors.New("执行失败")
	})
	if queue.size() != 0 {
		t.Fatalf("排空后队列必须清空（失败即停不重入队）: %d", queue.size())
	}
	queue.enqueue(job)
	queue.enqueue(job)
	queue.drainShutdown(func(_ context.Context, _ retention.RecordMaintenanceJob) (map[string]any, error) {
		return map[string]any{}, nil
	})
	if queue.size() != 0 {
		t.Fatalf("成功排空后队列必须清空: %d", queue.size())
	}
}

// TestCheckpointSQLite 验证 nil 与真实连接两条路径。
func TestCheckpointSQLite(t *testing.T) {
	if err := checkpointSQLite(context.Background(), nil); err != nil {
		t.Fatalf("nil 连接必须直接成功: %v", err)
	}
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "cp.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec("PRAGMA journal_mode = WAL;"); err != nil {
		t.Fatal(err)
	}
	if err := checkpointSQLite(context.Background(), db); err != nil {
		t.Fatalf("PASSIVE checkpoint 必须成功: %v", err)
	}
}

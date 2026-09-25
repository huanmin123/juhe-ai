package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accounthealth"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobregistry"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobsched"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobssettings"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/oauthrefresh"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/pgpool"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsagg"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsverify"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/taskruns"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/usagespooldrain"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/usagewriter"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/accountprobe"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/proberepo"
	"github.com/huanminabc/juhe-ai/backend-go-platform/safego"
	"github.com/huanminabc/juhe-ai/backend-go-platform/supervisor"
)

// workerAssembly 是 jobs 二进制 worker 侧的组合根：按 jobregistry 的
// GoWired 绑定装配 store/runner，经 jobsched 调度器复现 Node worker 的
// 调度语义（passive jitter、lease、退避、lane、停机排空）。
type workerAssembly struct {
	config    workerConfig
	scheduler *jobsched.Scheduler
	logger    *slog.Logger

	running atomic.Bool

	taskRunsStore *taskruns.Store
	statsStore    *statsverify.Store
	aggregator    *statsagg.Aggregator
	windows       *statsagg.WindowRefresher
	oauthStore    *oauthrefresh.Store
	// oauthFailures 是 OAuth 刷新失败状态存储的装配分支记录：nil = refresh
	// job 内存版默认（无 redis-state 的部署）；redis-state 部署为 Redis 版
	//（worker_oauth_failurestate.go），供装配分支测试断言。
	oauthFailures oauthrefresh.FailureStateStore
	writer        *usagewriter.Writer
	// usageSpoolDrain 是 gateway usage spool 交接表 drain（BUG-0175 D-72，
	// wireUsageWriterFamily 尾部接线；supervisor 组件在 components 注册）。
	usageSpoolDrain *usagespooldrain.Drainer
	// usageOverflowReplay 是 writer 溢出 spool 的回放 drain（统计链路排查
	// B②：队列满时记录先落盘溢出 spool，再由本 drain 幂等回放入队；未消费
	// 文件的队头水位并入 ingestgate 多源积压）。
	usageOverflowReplay *usagespooldrain.Drainer

	settings workerSettingsSource

	// scheduleIntervals 解析 SettingsIntervalJobNames 的系统设置驱动间隔
	// （F3-1，worker_schedule_settings.go 装配）；nil 时全部按注册表默认
	// 间隔调度。测试可直接注入 mock 源。
	scheduleIntervals jobregistry.SettingsInterval

	pools []*pgpool.Handle
	// poolRegistry 是 worker 侧全部任务族共享的 PG 池注册表（构造一次，
	// acquirePool 统一经它取池）。pgpool/sqlpool 的去重键是 {URL, role}，
	// 同 URL + 同 role 的多次 Acquire 自动复用同一池并按 refs 引用计数；
	// 历史实现每次 acquirePool 都 NewRegistry()，去重彻底失效，同库被按
	// 任务族打开 12+ 个独立池（每池 MaxOpenConns 默认 50）。统一 role 后
	// 全部任务族命中同一个池条目，句柄仍逐个登记进 a.pools 由既有 closer
	// 链关闭（Handle.Close 引用计数归零时才真正关库，即"registry 的关闭
	// 挂在既有 closer 链"）。
	poolRegistry *pgpool.Registry
	sqliteDBs    []*sql.DB
	closers      []func() error

	wiredJobs []string
	// wiringErr 是装配期第一条结构性错误（当前仅重名注册，缺陷4修复）：
	// scheduleWiredJob* 记录后不再调度/记账，buildWorkerAssembly 收口为启动
	// 失败（fail-fast），避免重名注册被调度器静默忽略的同时组装记账失真。
	wiringErr error
	// retention 是 J6 保留清理家族（worker_retention.go 装配）。
	retention *retentionFamily
	// probeRepoStore / circuitProbeService 由 wireProbeFamily 装配后供账户
	// 电路族（worker_circuit_jobs.go）的恢复目标解析复用。
	probeRepoStore      *proberepo.Store
	circuitProbeService *accountprobe.Service
	// wiredTasks 记录已注册（含租约包裹）的任务闭包，供测试/运维入口
	// 单轮执行；生产调度仍只经 scheduler。
	wiredTasks map[string]jobsched.Task
	// disabledJobs 是注册表已收录但依赖未齐、本轮不调度的 scheduled job
	// 清单（见 worker_partial_jobs.go）。
	disabledJobs []disabledJob
}

// staticSettings 以 Node DEFAULT_SYSTEM_SETTINGS 为默认值解析任务设置；
// stats 家族在 wireStatsFamily 中升级为 dbSettingsSource（system_settings
// 读模型），本类型保留为家族未装配数据库时的默认语义。
type staticSettings struct{}

func (staticSettings) statsAggregationBatchSize(context.Context) (int, error) {
	return 2000, nil
}

func (staticSettings) statsAggregationMaxBatches(context.Context) (int, error) {
	return 5, nil
}

func newRandomToken() string {
	buffer := make([]byte, 16)
	_, _ = rand.Read(buffer)
	return hex.EncodeToString(buffer)
}

func (a *workerAssembly) ownerID() string {
	return fmt.Sprintf("%s:%s:%d:%s", a.config.InstanceID, a.config.WorkerRole, a.config.WorkerReplicaIdx, newRandomToken())
}

// openSQLite 打开单 writer SQLite（与既有 jobs store 约定一致）。
func (a *workerAssembly) openSQLite(path string, label string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_txlock=immediate")
	if err != nil {
		return nil, fmt.Errorf("open %s sqlite 失败: %w", label, err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.Exec("PRAGMA journal_mode = WAL;"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("配置 %s sqlite WAL 失败: %w", label, err)
	}
	a.sqliteDBs = append(a.sqliteDBs, db)
	return db, nil
}

// workerPoolRole 是 worker 侧共享 PG 池的统一 role。历史实现按任务族拼
// "worker-<label>"，role 进入 pgpool 去重键导致同库每族一池；统一为常量后
// 同 URL 全部复用同一池。label 仍保留在错误文案中用于定位获取点。
const workerPoolRole = "worker"

func (a *workerAssembly) acquirePool(url string, label string) (*pgpool.Handle, error) {
	handle, err := a.poolRegistry.Acquire("pgx", url, workerPoolRole, a.config.PostgresMaxOpenConns, a.config.PostgresMaxIdleConns)
	if err != nil {
		return nil, fmt.Errorf("open worker %s postgres pool 失败: %w", label, err)
	}
	a.pools = append(a.pools, handle)
	return handle, nil
}

func (a *workerAssembly) addCloser(closer func() error) {
	a.closers = append(a.closers, closer)
}

// newWorkerAssembly 创建组合根的公共基础部分。它只分配调度器和底层句柄
// 关闭器，不会按任务族打开存储或注册任务；因此可供只承载跨组件面的
// minimal assembly 使用。
func newWorkerAssembly(config workerConfig, logger *slog.Logger) *workerAssembly {
	if logger == nil {
		logger = slog.Default()
	}
	assembly := &workerAssembly{
		config:       config,
		logger:       logger,
		settings:     staticSettings{},
		poolRegistry: pgpool.NewRegistry(),
		wiredTasks:   map[string]jobsched.Task{},
	}
	assembly.scheduler = jobsched.NewScheduler(jobsched.Options{
		StableSeed: fmt.Sprintf("%s:%s:%d", config.InstanceID, config.WorkerRole, config.WorkerReplicaIdx),
		// W2：调度器逐轮 outcome 日志复用进程 logger（JSON stdout）。
		Logger: logger,
	})
	// 先登记原始句柄关闭器（closeStores 逆向执行 → 家族 store 先关，原始
	// SQLite 连接与 PG 池最后关）。
	assembly.addCloser(func() error {
		var firstErr error
		for _, db := range assembly.sqliteDBs {
			if err := db.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		assembly.sqliteDBs = nil
		return firstErr
	})
	assembly.addCloser(func() error {
		var firstErr error
		for _, handle := range assembly.pools {
			if err := handle.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		assembly.pools = nil
		return firstErr
	})
	return assembly
}

// buildWorkerAssembly 装配 worker 组合根（机制强制常开，无总开关门禁）；
// 装配失败返回 error，由调用方 fail-fast。
func buildWorkerAssembly(config workerConfig, logger *slog.Logger) (*workerAssembly, error) {
	assembly := newWorkerAssembly(config, logger)
	if err := assembly.wireFamilies(context.Background()); err != nil {
		assembly.closeStores()
		return nil, err
	}
	// 缺陷4修复：重名注册等装配期结构性错误的收口点——启动失败并指名任务。
	if err := assembly.wiringError(); err != nil {
		assembly.closeStores()
		return nil, err
	}
	return assembly, nil
}

// wiringError 返回装配期记录的第一条结构性错误（无则 nil）。
func (a *workerAssembly) wiringError() error { return a.wiringErr }

// wireFamilies 打开各家族 store 并登记 GoWired 任务。启动顺序：
// store 打开 → schema 校验/初始化 → 启动恢复（taskruns）→ 一次性脏标记
// （group stats，PG）→ 任务注册。
func (a *workerAssembly) wireFamilies(ctx context.Context) error {
	// F3-1：设置驱动间隔源先于全部任务注册装配（Node scheduler.schedule 的
	// settingsNumber 在同一时点读取）。
	if err := a.wireScheduleSettings(); err != nil {
		return err
	}
	if err := a.wireTaskRunsFamily(ctx); err != nil {
		return err
	}
	if err := a.wireStatsFamily(ctx); err != nil {
		return err
	}
	if err := a.wireOAuthFamily(ctx); err != nil {
		return err
	}
	if err := a.wireUsageWriterFamily(ctx); err != nil {
		return err
	}
	if err := a.wireBalanceDetectFamily(ctx); err != nil {
		return err
	}
	if err := a.wireRetentionFamily(ctx); err != nil {
		return err
	}
	if err := a.wireProbeFamily(ctx); err != nil {
		return err
	}
	registerDisabledJobsStartup(a, a.logger)
	return nil
}

// registerDisabledJob 登记并输出一条启动日志（Node registry 显式登记缺失、
// 不静默跳过的语义在组合根侧的等价物）。
func (a *workerAssembly) registerDisabledJob(name, reason string) {
	a.disabledJobs = append(a.disabledJobs, disabledJob{JobName: name, Reason: reason})
	a.logger.Info("后台任务未接线，本轮不调度",
		"job", name,
		"registryStatus", func() string {
			if entry, ok := jobregistry.Find(name); ok {
				return string(entry.GoStatus)
			}
			return "unknown"
		}(),
		"missing", reason)
}

// wireTaskRunsFamily：background_task_runs + background_job_leases 存储、
// 启动对账（kill-restart 收口）与 background-task-run-reconcile 任务；
// 同时作为 postgres 模式下 scheduled lease 的获取点。
func (a *workerAssembly) wireTaskRunsFamily(ctx context.Context) error {
	config := taskruns.StoreConfig{
		Mode:                 taskruns.StoreMode(a.config.Driver),
		DatabasePath:         a.config.TaskRunsSQLitePath,
		PostgresURL:          a.config.PostgresURL,
		PostgresMaxOpenConns: a.config.PostgresMaxOpenConns,
		PostgresMaxIdleConns: a.config.PostgresMaxIdleConns,
	}
	if config.Mode == taskruns.ModePostgres {
		handle, err := a.acquirePool(a.config.PostgresURL, "task-runs")
		if err != nil {
			return err
		}
		config.PostgresPool = handle
	}
	store, err := taskruns.OpenStore(config)
	if err != nil {
		return fmt.Errorf("open task-runs store: %w", err)
	}
	a.taskRunsStore = store
	a.addCloser(store.Close)
	if err := store.EnsureSchema(ctx); err != nil {
		return fmt.Errorf("initialize task-runs schema: %w", err)
	}
	now := time.Now().UTC()
	recovered, err := taskruns.RecoverOnStartup(ctx, store, now, 10*time.Minute, 10*time.Minute, taskruns.ReconcileDefaultLimit)
	if err != nil {
		return fmt.Errorf("run task-runs startup recovery: %w", err)
	}
	a.logger.Info("task-runs startup recovery",
		"failedQueued", recovered.FailedQueuedCount,
		"failedRunning", recovered.FailedRunningCount,
		"deletedExpiredLease", recovered.DeletedExpiredLeaseCount)

	repo := &taskRunReconcileRepo{store: store}
	a.scheduleWiredJob("background-task-run-reconcile", func(taskCtx context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
		_, err := opsjobs.RunTaskRunReconcile(taskCtx, repo, time.Now().UnixMilli(), opsjobs.TaskRunReconcileBatchSize)
		return jobsched.TaskResult{}, err
	})
	return nil
}

// taskRunReconcileRepo 把 taskruns.Store 适配为 opsjobs.TaskRunReconcileRepo
// （时间戳统一 RFC3339 UTC，与 Node 对账输入一致）。
type taskRunReconcileRepo struct{ store *taskruns.Store }

func (r *taskRunReconcileRepo) ReconcileStale(ctx context.Context, input opsjobs.TaskRunReconcileInput) (opsjobs.TaskRunReconcileResult, error) {
	queuedBefore, err := time.Parse(time.RFC3339Nano, input.QueuedBefore)
	if err != nil {
		return opsjobs.TaskRunReconcileResult{}, fmt.Errorf("解析 queuedBefore 失败: %w", err)
	}
	heartbeatBefore, err := time.Parse(time.RFC3339Nano, input.RunningHeartbeatBefore)
	if err != nil {
		return opsjobs.TaskRunReconcileResult{}, fmt.Errorf("解析 runningHeartbeatBefore 失败: %w", err)
	}
	var now *time.Time
	if input.Now != "" {
		parsed, parseErr := time.Parse(time.RFC3339Nano, input.Now)
		if parseErr != nil {
			return opsjobs.TaskRunReconcileResult{}, fmt.Errorf("解析 now 失败: %w", parseErr)
		}
		now = &parsed
	}
	result, err := r.store.ReconcileStale(ctx, taskruns.TaskRunReconcileInput{
		QueuedBefore:           queuedBefore,
		RunningHeartbeatBefore: heartbeatBefore,
		Now:                    now,
		Limit:                  input.Limit,
	})
	if err != nil {
		return opsjobs.TaskRunReconcileResult{}, err
	}
	return opsjobs.TaskRunReconcileResult{
		FailedQueuedCount:        result.FailedQueuedCount,
		FailedRunningCount:       result.FailedRunningCount,
		DeletedExpiredLeaseCount: result.DeletedExpiredLeaseCount,
	}, nil
}

// statsTimezoneSource 适配 statsverify 时区读模型为 statsagg.Clock。
type statsTimezoneSource struct{ store *statsverify.Store }

func (s statsTimezoneSource) StatsTimezone(ctx context.Context) (*time.Location, error) {
	location, _, err := s.store.LoadUsageStatsLocation(ctx, time.Now())
	return location, err
}

// wireStatsFamily：statsverify（client-ip / group stats / 一致性检查）+
// statsagg（在线聚合与全部窗口刷新任务）。
func (a *workerAssembly) wireStatsFamily(ctx context.Context) error {
	config := statsverify.StoreConfig{
		Mode:                 statsverify.StoreMode(a.config.Driver),
		SQLiteStatsPath:      a.config.StatsSQLitePath,
		SQLiteBusinessPath:   a.config.BusinessSQLitePath,
		PostgresURL:          a.config.PostgresURL,
		PostgresMaxOpenConns: a.config.PostgresMaxOpenConns,
		PostgresMaxIdleConns: a.config.PostgresMaxIdleConns,
	}
	if config.Mode == statsverify.StorePostgres {
		handle, err := a.acquirePool(a.config.PostgresURL, "stats-verify")
		if err != nil {
			return err
		}
		config.PostgresPool = handle
	}
	store, err := statsverify.OpenStore(config)
	if err != nil {
		return fmt.Errorf("open stats-verify store: %w", err)
	}
	a.statsStore = store
	a.addCloser(store.Close)
	if err := store.EnsureSchema(ctx); err != nil {
		return fmt.Errorf("initialize stats-verify schema: %w", err)
	}
	postgres := a.config.Driver == "postgres"
	// Node scheduler 首轮 PG 路径的一次性 mark_all_group_account_stats_dirty；
	// SQLite 同样需要启动兜底：存量脏行（如 J1 投影标脏后进程重启、导入期
	// 遗留）只在 RefreshDirtyGroupAccountStats 消费脏行时刷新，不启动兜底就
	// 会一直滞留到下一次 availability 变化，dev 重启后分组统计停在旧快照。
	if err := store.MarkGroupAccountStatsStartupDirty(ctx, time.Now()); err != nil {
		return fmt.Errorf("mark group account stats startup dirty: %w", err)
	}

	// statsagg 需要独立 SQL 句柄（SQLite WAL 双连接读写；PG 共享池）。
	var aggDB *sql.DB
	if postgres {
		handle, err := a.acquirePool(a.config.PostgresURL, "stats-agg")
		if err != nil {
			return err
		}
		aggDB = handle.DB()
	} else {
		if aggDB, err = a.openSQLite(a.config.StatsSQLitePath, "stats-agg"); err != nil {
			return err
		}
	}
	dialect := statsagg.Dialect{Postgres: postgres}
	timezone := statsTimezoneSource{store: store}

	// system_settings 读模型（background-jobs settingsNumber 移植）：PG 复用
	// 共享池，SQLite 读 business 库；读取失败按 Node 语义降级默认（缺表/快照
	// 失败 warn）或使任务失败（整数/边界校验）。
	settingsDB := aggDB
	if !postgres {
		if settingsDB, err = a.openSQLite(a.config.BusinessSQLitePath, "settings"); err != nil {
			return err
		}
	}
	// BusinessDB：授权链查找（resource_authorizations/accounts）在 SQLite 下
	// 走业务库句柄，stats 库没有这两张表（与 settings/窗口刷新同一连接，
	// D-48 生产者接线）；PG 与 stats 同池，聚合事务内 juhe_business. 前缀
	// 直查，句柄仅作占位。
	a.aggregator = &statsagg.Aggregator{DB: aggDB, Dialect: dialect, Clock: timezone, BusinessDB: settingsDB}
	// BusinessDB：配额小时窗读业务库绑定表（request_quota_hourly_window_scope_bindings）。
	// PG 与 stats 同池共用 aggDB（juhe_business. 前缀）；SQLite 用业务库句柄
	//（与 settings 同一连接，D-48 生产者接线）。
	a.windows = &statsagg.WindowRefresher{DB: aggDB, Dialect: dialect, Clock: timezone, BusinessDB: settingsDB}
	a.settings = dbSettingsSource{source: jobssettings.NewSource(jobssettings.Options{
		DB:   settingsDB,
		Mode: settingsMode(postgres),
		Warn: jobssettingsWarn(a.logger),
	})}

	// usage-stats-aggregation（批量循环对齐 stats-writer aggregate_usage_stats）。
	// Node runUsageStatsAggregation 先过 usageStatsAggregationSafety 排干门控
	// （background-jobs.ts:484-507），并把 safeCreatedBefore 传给
	// aggregate_usage_stats，防止统计游标越过排队记录；statsagg 的
	// SafeCreatedBefore 选项承接该交接。
	a.scheduleWiredJob("usage-stats-aggregation", func(taskCtx context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
		safety, err := a.ingestDrainSafety(taskCtx)
		if err != nil {
			return jobsched.TaskResult{}, err
		}
		batchSize, err := a.settings.statsAggregationBatchSize(taskCtx)
		if err != nil {
			return jobsched.TaskResult{}, err
		}
		maxBatches, err := a.settings.statsAggregationMaxBatches(taskCtx)
		if err != nil {
			return jobsched.TaskResult{}, err
		}
		for index := 0; index < maxBatches; index++ {
			if taskCtx.Err() != nil {
				break
			}
			processed, err := a.aggregator.AggregateUsageStatsBatch(taskCtx, statsagg.AggregateOptions{
				BatchSize:         batchSize,
				SafeCreatedBefore: safety.SafeCreatedBefore,
			})
			if err != nil {
				return jobsched.TaskResult{}, err
			}
			if processed < batchSize {
				break
			}
		}
		return jobsched.TaskResult{}, nil
	})

	a.scheduleWiredJob("client-ip-stats-aggregation", func(taskCtx context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
		batchSize, err := a.settings.statsAggregationBatchSize(taskCtx)
		if err != nil {
			return jobsched.TaskResult{}, err
		}
		maxBatches, err := a.settings.statsAggregationMaxBatches(taskCtx)
		if err != nil {
			return jobsched.TaskResult{}, err
		}
		_, err = store.RunClientIPStatsAggregation(taskCtx, statsverify.RunClientIPStatsAggregationOptions{
			IngestGate:                       gateFunc(a.ingestDrainGate()),
			StatsAggregationBatchSize:        batchSize,
			StatsAggregationMaxBatchesPerRun: maxBatches,
		})
		return jobsched.TaskResult{}, err
	})
	a.scheduleWiredJob("group-account-stats-refresh", func(taskCtx context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
		_, err := store.RunGroupAccountStatsRefresh(taskCtx, time.Now())
		return jobsched.TaskResult{}, err
	})
	a.scheduleWiredJob("usage-stats-consistency-check", func(taskCtx context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
		if _, err := store.RunUsageStatsConsistencyCheck(taskCtx, time.Now(), a.logger); err != nil {
			return jobsched.TaskResult{}, err
		}
		return jobsched.TaskResult{}, nil
	})

	windowTask := func(jobName string, stages []statsagg.WindowStageName) jobsched.Task {
		return func(taskCtx context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
			if _, err := a.windows.RunStages(taskCtx, stages, statsagg.RefreshOptions{SkipIfUnchanged: true, JobName: jobName}); err != nil {
				return jobsched.TaskResult{}, err
			}
			return jobsched.TaskResult{}, nil
		}
	}
	// 窗口刷新任务的 databaseDriver 注册分叉（T6b，Node background-jobs.ts
	// :286-316，冻结清单 §2.2/§4.1）：
	//   - usage-rank-snapshots-refresh 的 PG stage 集合剔除
	//     ai_performance_summary_windows（:291 postgresUsageRankSnapshotCoreStageNames）；
	//   - ai-performance-summary-windows-refresh 仅 PG 分支注册（:292），
	//     默认/SQLite 分支该 stage 并入 usage-rank-snapshots-refresh（:310）；
	//   - usage-scope-range-windows-refresh 仅默认/SQLite 分支注册（:313），
	//     PG 分支跳过并打 background_cold_range_window_refresh_disabled（:296-300）；
	//   - usage-overview-windows-refresh interval PG 5min / SQLite 30min
	//     （:294 vs :312，经 ResolveScheduleForDriver 模式分叉消费）。
	if postgres {
		a.scheduleWiredJob("ai-performance-summary-windows-refresh", windowTask("ai_performance_summary_windows", []statsagg.WindowStageName{statsagg.StageAiPerformanceSummaryWindows}))
	} else {
		a.registerDisabledJob("ai-performance-summary-windows-refresh",
			"默认/SQLite 分支不独立注册（Node background-jobs.ts:310 该 stage 并入 usage-rank-snapshots-refresh）")
	}
	a.scheduleWiredJob("usage-rank-snapshots-refresh", windowTask("usage-rank-snapshots-refresh", rankSnapshotCoreStages(postgres)))
	a.scheduleWiredJob("system-metrics-trend-windows-refresh", windowTask("system_metrics_trend_windows", []statsagg.WindowStageName{statsagg.StageSystemMetricsTrendWindows}))
	a.scheduleWiredJob("usage-overview-windows-refresh", windowTask("usage_overview_windows", []statsagg.WindowStageName{statsagg.StageUsageOverviewWindows}))
	if postgres {
		// Node PG 高性能分支跳过在线冷历史范围窗口重刷（:296-300 原文案）。
		a.logger.Info("background_cold_range_window_refresh_disabled",
			slog.String("driver", a.config.Driver),
			slog.Any("hotStages", []string{"usage_scope_range_windows"}),
			slog.String("message", "PG 高性能模式跳过在线冷历史范围窗口重刷，热窗口刷新保持今日范围数据新鲜"))
		a.registerDisabledJob("usage-scope-range-windows-refresh",
			"PG 高性能模式不注册（Node background-jobs.ts:296-300 跳过冷历史范围窗口重刷）")
	} else {
		a.scheduleWiredJob("usage-scope-range-windows-refresh", windowTask("usage_scope_range_windows", []statsagg.WindowStageName{statsagg.StageUsageScopeRangeWindows}))
	}
	a.scheduleWiredJob("authorization-usage-range-windows-refresh", windowTask("authorization_usage_range_windows", []statsagg.WindowStageName{statsagg.StageAuthorizationUsageRangeWindows}))
	a.scheduleWiredJob("usage-hot-window-refresh", windowTask("usage_hot_window_refresh", hotUsageWindowStages()))
	// 配额小时窗刷新（BUG-0175 D-48/D-75/D-86）：不在 RunStages 的 watermark
	// 阶段模型内（归档 :1795-1951 是独立的 expiry 游标 + 脏范围分批消费
	// 编排），按归档结构独立任务注册；hasMore 在任务内续跑排空（对齐
	// rebuild-usage-stats.ts drainPostgresQuotaWindows 的 maxPasses 精神，
	// 上限防御病态循环）。
	a.scheduleWiredJob("usage-quota-hourly-windows-refresh", func(taskCtx context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
		for pass := 0; pass < quotaHourlyWindowMaxPasses; pass++ {
			if taskCtx.Err() != nil {
				break
			}
			result, err := a.windows.RunQuotaHourlyWindows(taskCtx)
			if err != nil {
				return jobsched.TaskResult{}, err
			}
			if !result.HasMore {
				break
			}
		}
		return jobsched.TaskResult{}, nil
	})
	return nil
}

// quotaHourlyWindowMaxPasses 是单轮任务内 hasMore 续跑上限；脏范围单轮消费
// <=128 个，正常积压在数百轮内排空，达到上限按任务失败上报（不静默截断）。
const quotaHourlyWindowMaxPasses = 1000

// rankSnapshotCoreStages 对齐 usageRankSnapshotCoreStageNames（SQLite 分支，
// 含 ai_performance_summary_windows）与 postgresUsageRankSnapshotCoreStageNames
// （PG 分支，由独立 job 承担、不重复入列）。
func rankSnapshotCoreStages(postgres bool) []statsagg.WindowStageName {
	core := []statsagg.WindowStageName{
		statsagg.StageAccountLast7dRequestRank,
		statsagg.StageCallerAccountLast7dRequestRank,
		statsagg.StageApiKeyCurrentMonthCostRank,
		statsagg.StageAccountAuthorizationCurrentMonthRank,
		statsagg.StageGroupAuthorizationCurrentMonthRank,
	}
	if postgres {
		return core
	}
	return append(core, statsagg.StageAiPerformanceSummaryWindows)
}

// hotUsageWindowStages 对齐 Node hotUsageWindowStageNames。
func hotUsageWindowStages() []statsagg.WindowStageName {
	return []statsagg.WindowStageName{statsagg.StageUsageOverviewWindows, statsagg.StageUsageScopeRangeWindows}
}

// wireOAuthFamily：J4 家族（OpenAI OAuth 刷新、anthropic/gemini/grok
// keepalive 刷新、两类可用性排期同步、授权过期 sweep）。
func (a *workerAssembly) wireOAuthFamily(ctx context.Context) error {
	var db *sql.DB
	postgres := a.config.Driver == "postgres"
	if postgres {
		handle, err := a.acquirePool(a.config.PostgresURL, "oauth-refresh")
		if err != nil {
			return err
		}
		db = handle.DB()
	} else {
		var err error
		if db, err = a.openSQLite(a.config.BusinessSQLitePath, "oauth-refresh"); err != nil {
			return err
		}
	}
	mode := oauthrefresh.StoreSQLite
	if postgres {
		mode = oauthrefresh.StorePostgres
	}
	store, err := oauthrefresh.OpenStore(db, mode, a.config.Secret)
	if err != nil {
		return fmt.Errorf("open oauth-refresh store: %w", err)
	}
	a.oauthStore = store

	// P0 修复：OAuth 刷新失败状态存储按运行态部署注入。缺省（无
	// JUHE_AI_REDIS_STATE_URL）保持 NewRefreshJob 的内存版；redis-state 部署
	// 注入 Redis 版，退避状态跨重启与多副本共享（refresh.go 注释声明的装配
	// 约定）。Redis 读取失败由 refresh 任务保守跳过该账户，不静默当“无退避”。
	failures, closeFailures, failureStoreErr := a.oauthFailureStateStore()
	if failureStoreErr != nil {
		return failureStoreErr
	}
	if closeFailures != nil {
		a.addCloser(closeFailures)
	}
	a.oauthFailures = failures
	var refreshOptions []func(*oauthrefresh.RefreshJob)
	if failures != nil {
		refreshOptions = append(refreshOptions, oauthrefresh.WithFailureStateStore(failures))
		a.logger.Info("OAuth 刷新失败状态存储已接入 Redis 运行态",
			"event", "oauth_refresh_failure_state_store_redis")
	}
	refreshOptions = append(refreshOptions, oauthrefresh.WithLogger(a.logger))
	refreshJob := oauthrefresh.NewRefreshJob(store, oauthrefresh.NewHTTPTokenExchanger(), refreshOptions...)
	a.scheduleWiredJob("openai-oauth-access-token-refresh", func(taskCtx context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
		if _, err := refreshJob.RunOnce(taskCtx, oauthrefresh.RefreshOptions{}); err != nil {
			return jobsched.TaskResult{}, err
		}
		return jobsched.TaskResult{}, nil
	})
	// P0 修复：anthropic/gemini/grok OAuth keepalive 接入生产驱动。此前
	// NewKeepaliveJob 零生产调用、注册表无条目、/health 不可见。复用本族
	// 既有 store（候选查询 ListDueKeepaliveAccounts）与 TokenExchanger
	// （NewHTTPTokenExchanger，与 refresh job 同一换发实现）；keepalive 只
	// 处理 anthropic/gemini/grok 三族计划（oauthrefresh.KeepalivePlans），
	// openai 族仍归 openai-oauth-access-token-refresh，互不重叠。
	keepaliveJob := oauthrefresh.NewKeepaliveJob(store, oauthrefresh.NewHTTPTokenExchanger(), oauthrefresh.WithKeepaliveLogger(a.logger))
	a.scheduleWiredJob("oauth-keepalive-token-refresh", func(taskCtx context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
		for _, plan := range oauthrefresh.KeepalivePlans() {
			if taskCtx.Err() != nil {
				break
			}
			result, err := keepaliveJob.RunOnce(taskCtx, plan, 0)
			if err != nil {
				return jobsched.TaskResult{}, err
			}
			a.logger.Debug("oauth keepalive 计划执行完成",
				"job", "oauth-keepalive-token-refresh",
				"provider", result.Provider,
				"scanned", result.Scanned,
				"due", result.Due,
				"refreshed", result.Refreshed,
				"failed", result.Failed,
				"skippedLocked", result.SkippedLocked,
				"skippedFresh", result.SkippedFresh)
		}
		return jobsched.TaskResult{}, nil
	})
	a.scheduleWiredJob("api-key-availability-schedule-status-sync", func(taskCtx context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
		if _, err := store.SyncApiKeyScheduleStatuses(taskCtx, time.Now(), 0); err != nil {
			return jobsched.TaskResult{}, err
		}
		return jobsched.TaskResult{}, nil
	})
	// P0 修复：排期激活 hook 装配。此前以 nil hook 调用，定时窗口启用的账号
	// 只翻状态、不推进 circuit dispatch revision 家族，网关 dispatch revision
	// 门控不解除。hook 复用本族已获取的业务库句柄做方言渲染（同步事务本身
	// 经 hook 传入，不新建连接池）；推进失败 best-effort 只记 warn（见
	// worker_oauth_activation.go）。
	activationBusiness, activationBusinessErr := accounthealth.NewProjectionBusinessDB(db, postgres)
	if activationBusinessErr != nil {
		return activationBusinessErr
	}
	activationHook := newAccountScheduleActivationHook(activationBusiness, a.logger)
	a.scheduleWiredJob("account-availability-schedule-status-sync", func(taskCtx context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
		if _, err := store.SyncAccountScheduleStatuses(taskCtx, time.Now(), 0, activationHook); err != nil {
			return jobsched.TaskResult{}, err
		}
		return jobsched.TaskResult{}, nil
	})
	a.scheduleWiredJob("resource-authorization-expiry-sweep", func(taskCtx context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
		// T6d：GrantFinalizer 装配注入——事务内的健康任务输入 fanout 是
		// Node syncResourceAuthorizationGrantRuntimeAsync 中可移植的下游
		// 副作用；runtime 投影/quota scope bindings 属 gateway authz 域，
		// 由 durable handoff 登记承接（见 worker_oauth_sweep.go）。
		result, err := store.RunAuthorizationExpirySweep(taskCtx, authorizationGrantHealthFanout{store: store}, 0)
		if err != nil {
			return jobsched.TaskResult{}, err
		}
		if result.Expired > 0 {
			// Node expireDueResourceAuthorizationsAsync 尾部：
			// refreshAfterResourceAuthorizationBusinessWriteAsync('authorization_expired')
			// → markAllGroupAccountStatsDirty(reason)（双 driver 同语义）。
			if err := markAllGroupAccountStatsAfterAuthzWrite(taskCtx, a.logger, a.statsStore, "authorization_expired"); err != nil {
				return jobsched.TaskResult{}, err
			}
		}
		return jobsched.TaskResult{}, nil
	})
	return nil
}

// usageWriterMaxWriteAttempts 是生产装配的写入重试上限（统计链路排查
// B③/poison-pill：默认 0 = 无限重试，任一坏记录会让队头批次无限占用队列
// 并让统计门控持续失败）。取 120 ≈ 固定 1s 重试下 2 分钟：容忍常规 PG 重
// 启/抖动，坏记录批次在约 2 分钟后转死信终态（error 日志 +
// deadLetterCount 计数），后续批次继续推进不阻塞。代价权衡：PG 中断超过
// 2 分钟时队头批次记录转死信丢弃（error 留痕），优于无限堆积。
const usageWriterMaxWriteAttempts = 120

// usageOverflowSpoolDirName 是 writer 溢出 spool 目录名（与 gateway
// usage-record-spool 同级、同派生规则：stats 库目录下）。
const usageOverflowSpoolDirName = "usage-record-overflow-spool"

// workerUsageOverflowSpoolDirectory 派生 writer 溢出 spool 目录：stats 库
// 目录下固定名（datadir 约定保证 StatsSQLitePath 恒非空，PG 模式同样生
// 效；溢出 spool 是 jobs 进程私有目录，无需与 gateway 对齐）。
func workerUsageOverflowSpoolDirectory(config workerConfig) string {
	return filepath.Join(filepath.Dir(config.StatsSQLitePath), usageOverflowSpoolDirName)
}

// wireUsageWriterFamily：usagewriter 直接异步写分片（Node Redis Stream /
// ingest-worker IPC 路径按总设计消灭后的 Go 单路径），启动 flush 循环、
// 停机排空。BUG-0175 D-72 补齐注入缺口：
//   - FreezePricing：Node 两条路径都在入队时点冻结定价事实；Go 单路径在
//     writer 侧冻结（C03 目录适配器已接线，见 worker_usage_pricing_catalog.go：
//     数据源=业务库 provider_model_catalog + custom_provider_models；目录读取
//     失败回落确定性 fallback 快照，读侧契约不变）；
//   - CatalogSnapshot：镜像 usageRecordPricingSnapshotForWrite 的
//     `databaseDriver !== 'postgres'` 守卫；
//   - BusinessDB（仅 SQLite；PG 路径的 last_used_at 副作用在主事务内随
//     juhe_business schema 同池完成，无需单独句柄）：openBusinessDB 打开
//     业务库句柄，usagewriter SqliteShardStore 由此回写 accounts.last_used_at
//     与账户健康副作用（此前 nil = queryOnly 静默跳过，业务库副作用断供）；
//   - OverflowSpool + MaxWriteAttempts + Postgres（统计链路排查 B①/B②/B③，
//     见 NewWriter 调用处注释）与溢出 spool 回放 drain。
func (a *workerAssembly) wireUsageWriterFamily(ctx context.Context) error {
	postgres := a.config.Driver == "postgres"
	var catalogDB *sql.DB
	var store usagewriter.ShardStore
	var pricingCatalog *usagePricingCatalog
	if postgres {
		handle, err := a.acquirePool(a.config.PostgresURL, "usage-writer")
		if err != nil {
			return err
		}
		catalogDB = handle.DB()
		store = usagewriter.NewPostgresShardStore(usagewriter.PostgresShardStoreConfig{
			DB:         catalogDB,
			ShardCount: a.config.UsageShardCount,
		})
		// PG 模式：usage-writer 池与业务库同库（juhe_business schema 限定），
		// 定价目录复用该句柄，不额外开池。
		pricingCatalog = newUsagePricingCatalog(catalogDB, true)
	} else {
		var err error
		if catalogDB, err = a.openSQLite(a.config.UsageCatalogSQLitePath, "usage-writer-catalog"); err != nil {
			return err
		}
		business, err := openBusinessDB(a, "usage-writer-business")
		if err != nil {
			return err
		}
		a.addCloser(business.close)
		// SQLite 模式：定价目录读业务库（provider_model_catalog /
		// custom_provider_models），与 shard store 的业务库副作用共用同一句柄。
		pricingCatalog = newUsagePricingCatalog(business.db, false)
		// stats 库镜像写句柄：SQLite standalone 模式下聚合器唯一输入源
		//（usage_records 由 statsverify EnsureSchema 建为聚合形状；stats 家族
		// 先于本家族装配，schema 已就绪）。句柄进入 openSQLite 的统一关闭链。
		statsMirror, err := a.openSQLite(a.config.StatsSQLitePath, "usage-writer-stats-mirror")
		if err != nil {
			return err
		}
		sqliteStore := usagewriter.NewSqliteShardStore(usagewriter.SqliteShardStoreConfig{
			CatalogDB:  catalogDB,
			ShardRoot:  a.config.UsageShardRoot,
			ShardCount: a.config.UsageShardCount,
			BusinessDB: business.db,
			StatsDB:    statsMirror,
		})
		if err := sqliteStore.EnsureCatalogSchema(); err != nil {
			return fmt.Errorf("initialize usage-writer catalog schema: %w", err)
		}
		store = sqliteStore
	}
	// 统计链路排查 B②/B③：
	//   - OverflowSpool：队列满时记录先落盘溢出 spool（历史装配缺省该选项，
	//     溢出记录直接丢弃且无处恢复），再由本进程的回放 drain 幂等重投；
	//   - MaxWriteAttempts：有限重试上限，坏记录批次转死信终态不阻塞后续；
	//   - Postgres：镜像 ShardStore 驱动，恢复写计划期"PG 必须有
	//     systemAccountId"的快速失败（空值此前到 INSERT 才撞 NOT NULL）。
	overflowSpool := usagewriter.NewFileOverflowSpool(workerUsageOverflowSpoolDirectory(a.config))
	writer := usagewriter.NewWriter(usagewriter.Config{
		ShardCount:       a.config.UsageShardCount,
		ShardRoot:        a.config.UsageShardRoot,
		FreezePricing:    true,
		CatalogSnapshot:  !postgres,
		MaxWriteAttempts: usageWriterMaxWriteAttempts,
		Postgres:         postgres,
	}, store, nil,
		usagewriter.WithLogger(slogWriterLogger{logger: a.logger}),
		usagewriter.WithCatalog(pricingCatalog),
		usagewriter.WithOverflowSpool(overflowSpool))
	a.writer = writer
	a.addCloser(func() error {
		if catalogDB != nil {
			return catalogDB.Close()
		}
		return nil
	})
	// BUG-0175 D-72：gateway 把 /v1 用量记录写入文件 spool 交接表，jobs 侧
	// 在此接线唯一的消费方（drain → Enqueue → 分片落库）。
	if err := a.wireUsageSpoolDrain(); err != nil {
		return err
	}
	// 溢出 spool 回放 drain：溢出文件的格式/目录布局与 gateway 交接表同构，
	// 复用 usagespooldrain.Drainer 的解析、幂等入队（ON CONFLICT DO
	// NOTHING）、删除与待删水位语义。目录恒非空（stats 库目录派生），恒接
	// 线；未消费文件的队头水位并入 ingestgate 多源积压（B①）。
	a.usageOverflowReplay = &usagespooldrain.Drainer{
		Directory: workerUsageOverflowSpoolDirectory(a.config),
		Enqueuer:  writer,
		Logger:    a.logger,
	}
	a.logger.Info("usage 溢出 spool 回放已接线",
		"event", "usage_record_overflow_spool_replay_wired",
		"directory", a.usageOverflowReplay.Directory)
	return nil
}

// wireUsageSpoolDrain 接线 gateway usage spool 交接表 drain（supervisor
// 组件由 components 提供）。目录取 JUHE_AI_USAGE_SPOOL_DIRECTORY（或
// sqlite 模式从 stats 库目录的同规则派生，见 worker_config.go）；两者皆空
// 时（PG 模式未配置 env）交接表无人消费，按组合根约定显式告警登记，不静默。
func (a *workerAssembly) wireUsageSpoolDrain() error {
	if a.writer == nil {
		return nil
	}
	if a.config.UsageSpoolDirectory == "" {
		a.logger.Warn("usage spool drain 未接线：JUHE_AI_USAGE_SPOOL_DIRECTORY 未配置且无法从 stats 库路径派生，gateway 用量交接文件将无人消费",
			"event", "usage_record_spool_drain_unwired")
		return nil
	}
	a.usageSpoolDrain = &usagespooldrain.Drainer{
		Directory: a.config.UsageSpoolDirectory,
		Enqueuer:  a.writer,
		Logger:    a.logger,
	}
	a.logger.Info("usage spool drain 已接线",
		"event", "usage_record_spool_drain_wired",
		"directory", a.config.UsageSpoolDirectory,
		"batchSize", usagespooldrain.DefaultBatchSize,
		"flushIntervalMs", usagespooldrain.DefaultFlushIntervalMs)
	return nil
}

type slogWriterLogger struct{ logger *slog.Logger }

func (l slogWriterLogger) Warn(msg string, fields map[string]any) {
	l.logger.Warn(msg, "fields", fields)
}
func (l slogWriterLogger) Error(msg string, fields map[string]any) {
	l.logger.Error(msg, "fields", fields)
}

// scheduleWiredJob 按注册表登记的调度参数注册一个 GoWired 任务，并统一
// 包裹 postgres 租约（对齐 Node runWithPostgresScheduledLease 只在
// driver=postgres 生效的语义）。非 GoWired 名称一律拒绝注册。调度参数经
// ResolveScheduleForDriver 应用 databaseDriver 注册分叉（T6b：
// usage-overview-windows-refresh 的 SQLite 30min、usage-scope-range/ai-
// performance 的分支独占注册）；反向模式返回 !ok 时拒绝注册。
func (a *workerAssembly) scheduleWiredJob(name string, task jobsched.Task) {
	a.scheduleWiredJobWithSettings(name, nil, task)
}

// scheduleWiredJobWithSettings 是 scheduleWiredJob 的间隔覆盖变体（env 驱动
// 的可变 interval，如 account-list-availability-projection-maintenance 的
// accountListAvailabilityProjectionIntervalMs；T6b 冻结清单 §4）。
//
// F3-1：除调用方显式传入的 settings 覆盖外，统一叠加组合根的系统设置间隔
// 源（a.scheduleIntervals），让 SettingsIntervalJobNames 的设置间隔真正
// 生效；两个源对同一 job 不会同时命中（设置驱动 job 不用显式覆盖注册），
// 显式覆盖优先经 ResolveScheduleForDriver 的既有语义保持。
func (a *workerAssembly) scheduleWiredJobWithSettings(name string, settings jobregistry.SettingsInterval, task jobsched.Task) {
	// 缺陷4修复：重名注册此前被 scheduler.Schedule 静默忽略，但本层仍会
	// append wiredJobs 并覆盖 wiredTasks——组装记账失真（重复条目 + 后注册
	// 的闭包顶替先注册者）。这里在调度前自查 fail-fast：启动即报错并指名
	// 重复任务，首次注册保持原状。
	if _, exists := a.wiredTasks[name]; exists {
		if a.wiringErr == nil {
			a.wiringErr = fmt.Errorf("后台任务重复注册：%s 已在装配中登记（同名任务拒绝二次装配，避免 wiredJobs/wiredTasks 记账失真）", name)
		}
		a.logger.Warn("拒绝重复注册后台任务", "job", name)
		return
	}
	entry, ok := jobregistry.Find(name)
	if !ok || entry.GoStatus != jobregistry.GoWired {
		a.logger.Warn("拒绝注册非 GoWired 任务", "job", name)
		return
	}
	if settings == nil {
		settings = a.scheduleIntervals
	}
	schedule, ok := jobregistry.ResolveScheduleForDriver(name, settings, a.config.Driver)
	if !ok {
		a.logger.Warn("任务在当前 databaseDriver 分支不注册（Node 调度分支冻结）", "job", name, "driver", a.config.Driver)
		return
	}
	spec := jobsched.Spec{
		Name:              name,
		Interval:          schedule.Interval,
		InitialDelay:      schedule.InitialDelay,
		StablePhaseWindow: schedule.StablePhaseWindow,
		PassiveJitter:     schedule.PassiveJitter,
		DeferFirstRun:     schedule.DeferFirstRun,
		Timeout:           schedule.Timeout,
		Lane:              schedule.Lane,
		Task:              a.withLease(name, schedule.LeaseTTL, task),
	}
	if schedule.ScheduleMode == "fixedDelay" {
		spec.ScheduleMode = jobsched.ScheduleModeFixedDelay
	} else {
		spec.ScheduleMode = jobsched.ScheduleModeFixedRate
	}
	if schedule.OverlapCoalesce {
		spec.OverlapPolicy = jobsched.OverlapCoalesceOne
	} else {
		spec.OverlapPolicy = jobsched.OverlapSkip
	}
	if schedule.BackoffBase > 0 {
		spec.Backoff = &jobsched.Backoff{Base: schedule.BackoffBase, Max: schedule.BackoffMax}
	}
	a.scheduler.Schedule(spec)
	a.wiredJobs = append(a.wiredJobs, name)
	a.wiredTasks[name] = spec.Task
}

// runWiredJobOnce 直接执行一个已注册任务一轮（含租约包裹），供测试与
// 运维单轮验证使用；生产调度仍只经 scheduler。
func (a *workerAssembly) runWiredJobOnce(ctx context.Context, name string) (jobsched.TaskResult, error) {
	task, ok := a.wiredTasks[name]
	if !ok {
		return jobsched.TaskResult{}, fmt.Errorf("任务 %s 未注册", name)
	}
	return task(ctx, jobsched.TaskContext{})
}

// workerTaskRunRole 是 scheduled worker 运行记录的 worker_role：与
// temporary-maintenance-worker 共享 background_task_runs 表，但运行记录
// 对账（ReconcileStale）只收口临时维护任务，scheduled job 的陈旧行靠
// 终态收口与索引（status, updated_at DESC, run_id DESC）消费。
const workerTaskRunRole = "worker"

// errTaskRunStartSkipped 表示运行记录启动 CAS 未命中（行已非 queued），
// 本轮任务未执行；运行记录已由 RunWithTaskRun 收口为 skipped。
var errTaskRunStartSkipped = errors.New("background_task_run 启动 CAS 未命中，本轮任务未执行")

// withLease 包裹 taskruns.RunWithScheduledLease 与 taskruns.RunWithTaskRun：
// PG 模式下每次 job 执行在持调度租约的同时登记 background_task_runs 运行
// 历史（queued→running→终态）。SQLite 模式与 Node 一致（driver != postgres
// 时任务直跑，不获取 PG 租约，也不写运行历史）。
func (a *workerAssembly) withLease(jobName string, ttl time.Duration, task jobsched.Task) jobsched.Task {
	if a.taskRunsStore == nil || ttl <= 0 || a.config.Driver != "postgres" {
		return task
	}
	store := a.taskRunsStore
	return func(ctx context.Context, taskCtx jobsched.TaskContext) (jobsched.TaskResult, error) {
		ownerID := a.ownerID()
		outcome, err := taskruns.RunWithScheduledLease(ctx, store, taskruns.ScheduledLeaseRunnerOptions{
			JobName: jobName,
			OwnerID: ownerID,
			RunID:   newRandomToken(),
			TTL:     ttl,
		}, func(runCtx context.Context, lease taskruns.LeaseIdentity) error {
			taskErr := a.runWithTaskRunHistory(store, jobName, ownerID, ttl, runCtx, taskCtx, task)
			_ = lease
			return taskErr
		})
		if errors.Is(err, errTaskRunStartSkipped) {
			// 运行记录启动 CAS 未命中：任务本轮未执行，对调度器沿用租约
			// busy 的 skipped 语义（不算失败），warning 保留启动原因。
			return jobsched.TaskResult{
				Outcome:    jobsched.OutcomeSkipped,
				Warning:    errTaskRunStartSkipped.Error(),
				LeaseState: jobsched.LeaseState(outcome.LeaseState),
			}, nil
		}
		result := jobsched.TaskResult{LeaseState: jobsched.LeaseState(outcome.LeaseState)}
		if outcome.Outcome == taskruns.OutcomePartial {
			result.Outcome = jobsched.OutcomePartial
			result.Warning = outcome.Warning
		}
		if outcome.Outcome == taskruns.OutcomeSkipped {
			result.Outcome = jobsched.OutcomeSkipped
			result.Warning = outcome.Warning
		}
		if err != nil {
			return result, err
		}
		if result.Outcome == "" {
			result.Outcome = jobsched.OutcomeSuccess
		}
		return result, nil
	}
}

// runWithTaskRunHistory 把一次已持调度租约的 job 执行登记进
// background_task_runs：RunWithTaskRun 负责 queued→running→终态状态机与
// started_at/finished_at/duration_ms 落列，任务错误在终态 failed +
// error_message 中留痕并原样透传（partial/skipped outcome 语义由外层
// withLease 保持）。
func (a *workerAssembly) runWithTaskRunHistory(store *taskruns.Store, jobName, ownerID string, ttl time.Duration, runCtx context.Context, taskCtx jobsched.TaskContext, task jobsched.Task) error {
	_, outcome, err := taskruns.RunWithTaskRun(runCtx, store, taskruns.TaskRunRunnerOptions{
		JobName:    jobName,
		JobType:    jobName,
		WorkerRole: workerTaskRunRole,
		LeaseKey:   taskruns.ScheduledLeaseKey(jobName, ""),
		OwnerID:    ownerID,
		LeaseTTL:   ttl,
	}, func(taskRunCtx context.Context, _ taskruns.LeaseFence, _ taskruns.TaskRun) (taskruns.TaskRunResult, error) {
		var taskErr error
		func() {
			// 缺陷2修复：panic 在运行记录状态机内转为任务错误，RunWithTaskRun
			// 的终态写（failed + panic 值留痕）与临时租约释放照常收口；否则行
			// 永久停留 running（scheduled job 的 worker_role 不在
			// ReconcileStale 对账范围，无人兜底收口）。调度器侧另有任务级
			// recover 兜底（SQLite 直跑与非包裹路径），两层互为纵深。
			defer safego.Handle("juheaijobs.wiredTask", func(recovered any) {
				taskErr = fmt.Errorf("后台任务 panic：%v", recovered)
			})
			_, taskErr = task(taskRunCtx, taskCtx)
		}()
		if taskErr != nil {
			return taskruns.TaskRunResult{}, taskErr
		}
		return taskruns.TaskRunResult{Status: taskruns.StatusCompleted}, nil
	})
	if err != nil {
		return err
	}
	if outcome.Outcome == taskruns.OutcomeSkipped {
		return errTaskRunStartSkipped
	}
	return nil
}

// closeStores 逆向关闭全部家族存储。
func (a *workerAssembly) closeStores() {
	for index := len(a.closers) - 1; index >= 0; index-- {
		if err := a.closers[index](); err != nil {
			a.logger.Warn("worker store close 失败", "error", err)
		}
	}
	a.closers = nil
}

// taskRunFinishGrace 是停机排空超时后的终态收口宽限：RunWithTaskRun 的终态
// 写/租约释放已改用不受停机取消影响的有限 ctx（taskruns 内 5s bound），这里
// 给同量级宽限让被截断任务的收口先落库，随后才由 Close → closeStores 关闭
// 存储，避免「排空放弃 → 关库」截断收口本身。
const taskRunFinishGrace = 5 * time.Second

// components 返回 supervisor 组件：调度循环（含停机排空）与 usagewriter
// flush 循环；Close 在全部组件停止后关闭家族存储。
func (a *workerAssembly) components() []supervisor.Component {
	components := []supervisor.Component{
		{
			Name: "worker scheduler",
			Run: func(runCtx context.Context) error {
				a.running.Store(true)
				defer a.running.Store(false)
				<-runCtx.Done()
				drained, active := a.scheduler.StopAndDrain(a.config.DrainTimeout)
				if !drained {
					// 缺陷3修复：排空超时（Node stopBackgroundJobs 10s 上限）
					// 放弃在飞任务时显式披露截断，并等待终态收口宽限后再返回
					// （Close 才会关库）；宽限后仍未结束的任务由 warn 留痕。
					a.logger.Warn("worker scheduler 停机排空超时，在飞任务被截断；等待终态收口宽限",
						"event", "worker_scheduler_drain_truncated",
						"active", active,
						"drainTimeoutMs", a.config.DrainTimeout.Milliseconds(),
						"finishGraceMs", taskRunFinishGrace.Milliseconds())
					_, active = a.scheduler.StopAndDrain(taskRunFinishGrace)
					if active > 0 {
						a.logger.Warn("worker scheduler 终态收口宽限耗尽，仍有在飞任务未结束，其终态可能无法落库",
							"event", "worker_scheduler_finish_grace_exhausted", "active", active)
					}
				}
				a.logger.Info("worker scheduler 停机排空完成", "drained", drained, "active", active)
				return nil
			},
			Close: func() error { a.closeStores(); return nil },
		},
	}
	if a.writer != nil {
		components = append(components, supervisor.Component{
			Name: "usage-record writer",
			Run: func(runCtx context.Context) error {
				a.writer.Start()
				<-runCtx.Done()
				a.writer.Close(runCtx)
				return nil
			},
		})
	}
	if a.usageSpoolDrain != nil {
		drainer := a.usageSpoolDrain
		components = append(components, supervisor.Component{
			// gateway usage spool 交接表 drain（BUG-0175 D-72）：Run 内含
			// ctx 取消后的有界停机排空；未消费文件持久留待下次启动。
			Name: "usage-record spool drain",
			Run: func(runCtx context.Context) error {
				drainer.Run(runCtx)
				return nil
			},
		})
	}
	if a.usageOverflowReplay != nil {
		replay := a.usageOverflowReplay
		components = append(components, supervisor.Component{
			// writer 溢出 spool 回放 drain（统计链路排查 B②）：溢出落盘
			// 记录的幂等重投循环；Run 内含停机排空，未消费文件留待下次启动。
			Name: "usage-record overflow replay",
			Run: func(runCtx context.Context) error {
				replay.Run(runCtx)
				return nil
			},
		})
	}
	return components
}

// ready 报告调度循环是否在运行（owner 门禁由 main 的 ownermode 承担）。
func (a *workerAssembly) ready() bool { return a.running.Load() }

// statusPayload 输出 worker 健康载荷：wired/未接线任务清单与各任务快照。
func (a *workerAssembly) statusPayload() map[string]any {
	registeredNotWired := []string{}
	for _, entry := range jobregistry.ScheduledEntries() {
		if entry.GoStatus == jobregistry.GoPartial || entry.GoStatus == jobregistry.NodeOnly {
			registeredNotWired = append(registeredNotWired, entry.JobName)
		}
	}
	snapshots := []jobsched.Snapshot{}
	if a.scheduler != nil {
		snapshots = a.scheduler.Snapshots()
	}
	statusPayload := map[string]any{
		"workerEnabled":         true, // 机制强制常开（2026-09-19 决策），字段保留以稳定 /health 载荷契约
		"workerDriver":          a.config.Driver,
		"workerWiredJobs":       a.wiredJobs,
		"workerRegisteredTodo":  registeredNotWired,
		"workerDisabledJobs":    a.disabledJobs,
		"workerUsageWriter":     a.writer != nil,
		"workerUsageSpoolDrain": a.usageSpoolDrain != nil,
		"workerJobs":            snapshots,
	}
	// 统计链路排查 B②/B③ 观测面：writer 运行态快照（含
	// deadLetterCount / droppedOverflowCount / droppedDispatchCount 等丢弃
	// 计数与队列积压、最旧入队时间）进 /health 载荷，丢弃与积压可被外部
	// 巡检发现；空 assembly 键缺省（与 writer 布尔位一致）。
	if a.writer != nil {
		statusPayload["workerUsageWriterRuntime"] = a.writer.Runtime()
		statusPayload["workerUsageOverflowReplay"] = a.usageOverflowReplay != nil
	}
	return statusPayload
}

package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accountprobe"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/internalapi"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobregistry"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobsched"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobssettings"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/oauthrefresh"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/pgpool"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/proberepo"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsagg"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsverify"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/taskruns"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/usagewriter"
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
	writer        *usagewriter.Writer

	settings workerSettingsSource

	pools     []*pgpool.Handle
	sqliteDBs []*sql.DB
	closers   []func() error

	wiredJobs []string
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

	dispatchHandler http.Handler
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

func (a *workerAssembly) acquirePool(url string, label string) (*pgpool.Handle, error) {
	registry := pgpool.NewRegistry()
	handle, err := registry.Acquire("pgx", url, "worker-"+label, a.config.PostgresMaxOpenConns, a.config.PostgresMaxIdleConns)
	if err != nil {
		return nil, fmt.Errorf("open worker %s postgres pool 失败: %w", label, err)
	}
	a.pools = append(a.pools, handle)
	return handle, nil
}

func (a *workerAssembly) addCloser(closer func() error) {
	a.closers = append(a.closers, closer)
}

// buildWorkerAssembly 装配 worker 组合根；config.Enabled=false 时返回 nil。
func buildWorkerAssembly(config workerConfig, logger *slog.Logger) (*workerAssembly, error) {
	if !config.Enabled {
		return nil, nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	assembly := &workerAssembly{
		config:     config,
		logger:     logger,
		settings:   staticSettings{},
		wiredTasks: map[string]jobsched.Task{},
	}
	assembly.scheduler = jobsched.NewScheduler(jobsched.Options{
		StableSeed: fmt.Sprintf("%s:%s:%d", config.InstanceID, config.WorkerRole, config.WorkerReplicaIdx),
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
	if err := assembly.wireFamilies(context.Background()); err != nil {
		assembly.closeStores()
		return nil, err
	}
	assembly.wireDispatchHandler()
	return assembly, nil
}

// wireFamilies 打开各家族 store 并登记 GoWired 任务。启动顺序：
// store 打开 → schema 校验/初始化 → 启动恢复（taskruns）→ 一次性脏标记
// （group stats，PG）→ 任务注册。
func (a *workerAssembly) wireFamilies(ctx context.Context) error {
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
	if !a.config.TaskRunsEnabled {
		return nil
	}
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
	if !a.config.StatsEnabled {
		return nil
	}
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
	if postgres {
		// Node scheduler 首轮 PG 路径的一次性 mark_all_group_account_stats_dirty。
		if err := store.MarkGroupAccountStatsStartupDirty(ctx, time.Now()); err != nil {
			return fmt.Errorf("mark group account stats startup dirty: %w", err)
		}
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
	a.aggregator = &statsagg.Aggregator{DB: aggDB, Dialect: dialect, Clock: timezone}
	a.windows = &statsagg.WindowRefresher{DB: aggDB, Dialect: dialect, Clock: timezone}

	// system_settings 读模型（background-jobs settingsNumber 移植）：PG 复用
	// 共享池，SQLite 读 business 库；读取失败按 Node 语义降级默认（缺表/快照
	// 失败 warn）或使任务失败（整数/边界校验）。
	settingsDB := aggDB
	if !postgres {
		if settingsDB, err = a.openSQLite(a.config.BusinessSQLitePath, "settings"); err != nil {
			return err
		}
	}
	a.settings = dbSettingsSource{source: jobssettings.NewSource(jobssettings.Options{
		DB:   settingsDB,
		Mode: settingsMode(postgres),
		Warn: jobssettingsWarn(a.logger),
	})}

	// usage-stats-aggregation（批量循环对齐 stats-writer aggregate_usage_stats）。
	a.scheduleWiredJob("usage-stats-aggregation", func(taskCtx context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
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
			processed, err := a.aggregator.AggregateUsageStatsBatch(taskCtx, statsagg.AggregateOptions{BatchSize: batchSize})
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
	a.scheduleWiredJob("usage-rank-snapshots-refresh", windowTask("usage-rank-snapshots-refresh", rankSnapshotCoreStages()))
	a.scheduleWiredJob("ai-performance-summary-windows-refresh", windowTask("ai_performance_summary_windows", []statsagg.WindowStageName{statsagg.StageAiPerformanceSummaryWindows}))
	a.scheduleWiredJob("system-metrics-trend-windows-refresh", windowTask("system_metrics_trend_windows", []statsagg.WindowStageName{statsagg.StageSystemMetricsTrendWindows}))
	a.scheduleWiredJob("usage-overview-windows-refresh", windowTask("usage_overview_windows", []statsagg.WindowStageName{statsagg.StageUsageOverviewWindows}))
	a.scheduleWiredJob("usage-scope-range-windows-refresh", windowTask("usage_scope_range_windows", []statsagg.WindowStageName{statsagg.StageUsageScopeRangeWindows}))
	a.scheduleWiredJob("authorization-usage-range-windows-refresh", windowTask("authorization_usage_range_windows", []statsagg.WindowStageName{statsagg.StageAuthorizationUsageRangeWindows}))
	a.scheduleWiredJob("usage-hot-window-refresh", windowTask("usage_hot_window_refresh", hotUsageWindowStages()))
	return nil
}

// rankSnapshotCoreStages 对齐 postgresUsageRankSnapshotCoreStageNames
// （ai_performance_summary_windows 由独立 job 刷新，不重复入列）。
func rankSnapshotCoreStages() []statsagg.WindowStageName {
	return []statsagg.WindowStageName{
		statsagg.StageAccountLast7dRequestRank,
		statsagg.StageCallerAccountLast7dRequestRank,
		statsagg.StageApiKeyCurrentMonthCostRank,
		statsagg.StageAccountAuthorizationCurrentMonthRank,
		statsagg.StageGroupAuthorizationCurrentMonthRank,
	}
}

// hotUsageWindowStages 对齐 Node hotUsageWindowStageNames。
func hotUsageWindowStages() []statsagg.WindowStageName {
	return []statsagg.WindowStageName{statsagg.StageUsageOverviewWindows, statsagg.StageUsageScopeRangeWindows}
}

// wireOAuthFamily：J4 家族（OpenAI OAuth 刷新、两类可用性排期同步、
// 授权过期 sweep）。
func (a *workerAssembly) wireOAuthFamily(ctx context.Context) error {
	if !a.config.OAuthEnabled {
		return nil
	}
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

	refreshJob := oauthrefresh.NewRefreshJob(store, oauthrefresh.NewHTTPTokenExchanger(), oauthrefresh.WithLogger(a.logger))
	a.scheduleWiredJob("openai-oauth-access-token-refresh", func(taskCtx context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
		if _, err := refreshJob.RunOnce(taskCtx, oauthrefresh.RefreshOptions{}); err != nil {
			return jobsched.TaskResult{}, err
		}
		return jobsched.TaskResult{}, nil
	})
	a.scheduleWiredJob("api-key-availability-schedule-status-sync", func(taskCtx context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
		if _, err := store.SyncApiKeyScheduleStatuses(taskCtx, time.Now(), 0); err != nil {
			return jobsched.TaskResult{}, err
		}
		return jobsched.TaskResult{}, nil
	})
	a.scheduleWiredJob("account-availability-schedule-status-sync", func(taskCtx context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
		if _, err := store.SyncAccountScheduleStatuses(taskCtx, time.Now(), 0, nil); err != nil {
			return jobsched.TaskResult{}, err
		}
		return jobsched.TaskResult{}, nil
	})
	a.scheduleWiredJob("resource-authorization-expiry-sweep", func(taskCtx context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
		if _, err := store.RunAuthorizationExpirySweep(taskCtx, nil, 0); err != nil {
			return jobsched.TaskResult{}, err
		}
		return jobsched.TaskResult{}, nil
	})
	return nil
}

// wireUsageWriterFamily：usagewriter 直接异步写分片（Node Redis Stream /
// ingest-worker IPC 路径按总设计消灭后的 Go 单路径），启动 flush 循环、
// 停机排空。
func (a *workerAssembly) wireUsageWriterFamily(ctx context.Context) error {
	if !a.config.UsageWriterEnabled {
		return nil
	}
	postgres := a.config.Driver == "postgres"
	var catalogDB *sql.DB
	var store usagewriter.ShardStore
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
	} else {
		var err error
		if catalogDB, err = a.openSQLite(a.config.UsageCatalogSQLitePath, "usage-writer-catalog"); err != nil {
			return err
		}
		sqliteStore := usagewriter.NewSqliteShardStore(usagewriter.SqliteShardStoreConfig{
			CatalogDB:  catalogDB,
			ShardRoot:  a.config.UsageShardRoot,
			ShardCount: a.config.UsageShardCount,
		})
		if err := sqliteStore.EnsureCatalogSchema(); err != nil {
			return fmt.Errorf("initialize usage-writer catalog schema: %w", err)
		}
		store = sqliteStore
	}
	writer := usagewriter.NewWriter(usagewriter.Config{
		ShardCount: a.config.UsageShardCount,
		ShardRoot:  a.config.UsageShardRoot,
	}, store, nil, usagewriter.WithLogger(slogWriterLogger{logger: a.logger}))
	a.writer = writer
	a.addCloser(func() error {
		if catalogDB != nil {
			return catalogDB.Close()
		}
		return nil
	})
	return nil
}

type slogWriterLogger struct{ logger *slog.Logger }

func (l slogWriterLogger) Warn(msg string, fields map[string]any) {
	l.logger.Warn(msg, "fields", fields)
}
func (l slogWriterLogger) Error(msg string, fields map[string]any) {
	l.logger.Error(msg, "fields", fields)
}

// wireDispatchHandler 把 internalapi 账户测试派发 handler 挂到 loopback mux
// （由 main 的 jobsHTTPHandler 消费）；账号测试执行器（gateway 域诊断链）
// 未迁移前派发回调返回 false（503 服务暂不可用，任务留在 queued 由
// queued-max-wait sweep 收口），不伪造受理。
func (a *workerAssembly) wireDispatchHandler() {
	if !a.config.InternalAPIEnabled || a.config.Secret == "" {
		return
	}
	a.dispatchHandler = internalapi.NewAccountTestDispatchHandler(internalapi.AccountTestDispatchRouterOptions{
		Secret: a.config.Secret,
		Dispatch: func(ctx context.Context, taskID string) (bool, error) {
			return false, nil
		},
	})
}

// scheduleWiredJob 按注册表登记的调度参数注册一个 GoWired 任务，并统一
// 包裹 postgres 租约（对齐 Node runWithPostgresScheduledLease 只在
// driver=postgres 生效的语义）。非 GoWired 名称一律拒绝注册。
func (a *workerAssembly) scheduleWiredJob(name string, task jobsched.Task) {
	entry, ok := jobregistry.Find(name)
	if !ok || entry.GoStatus != jobregistry.GoWired {
		a.logger.Warn("拒绝注册非 GoWired 任务", "job", name)
		return
	}
	schedule, ok := jobregistry.ResolveSchedule(name, nil)
	if !ok {
		a.logger.Warn("任务缺少调度参数，跳过注册", "job", name)
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

// withLease 包裹 taskruns.RunWithScheduledLease；SQLite 模式与 Node 一致
// （driver != postgres 时任务直跑，不获取 PG 租约）。
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
			_, taskErr := task(runCtx, taskCtx)
			_ = lease
			return taskErr
		})
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

// closeStores 逆向关闭全部家族存储。
func (a *workerAssembly) closeStores() {
	for index := len(a.closers) - 1; index >= 0; index-- {
		if err := a.closers[index](); err != nil {
			a.logger.Warn("worker store close 失败", "error", err)
		}
	}
	a.closers = nil
}

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
	return map[string]any{
		"workerEnabled":         a.config.Enabled,
		"workerDriver":          a.config.Driver,
		"workerWiredJobs":       a.wiredJobs,
		"workerRegisteredTodo":  registeredNotWired,
		"workerDisabledJobs":    a.disabledJobs,
		"workerUsageWriter":     a.writer != nil,
		"workerDispatchMounted": a.dispatchHandler != nil,
		"workerJobs":            snapshots,
	}
}

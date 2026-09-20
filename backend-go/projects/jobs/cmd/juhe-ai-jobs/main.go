package main

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-contracts"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accounthealth"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/keymodelrecovery"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckruntime"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/pgpool"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/proxylatency"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/runtimelog"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/tablemonitor"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accountbalance"
	"github.com/huanminabc/juhe-ai/backend-go-platform/gometrics"
	"github.com/huanminabc/juhe-ai/backend-go-platform/ownermode"
	"github.com/huanminabc/juhe-ai/backend-go-platform/processlog"
	"github.com/huanminabc/juhe-ai/backend-go-platform/supervisor"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// hooksSignalNotifyContext 是 signal.NotifyContext 的测试注入点：生产路径
// 直接转发真实现；进程内测试覆写为可编程取消的 ctx，以驱动优雅停机序列
// （真实 SIGTERM 在测试进程内无法安全投递）。
var hooksSignalNotifyContext = signal.NotifyContext

// run 承载原 main() 的全部线性流程并返回进程退出码。原 fail()（stderr 错误
// 行 + os.Exit(1)）与 flag 用法错误（os.Exit(2)）收敛为返回值；stderr 错误
// 文案与输出流逐字节不变。args 为 flag 解析输入（不含程序名）。
func run(args []string, stdout io.Writer, stderr io.Writer) int {
	flags := flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	version := flags.Bool("version", false, "print the jobs project contract version")
	check := flags.Bool("check-boundary", false, "verify the scaffold boundary")
	once := flags.Bool("once", false, "run one F2 table-monitor sampling cycle and exit")
	runtimeLegacyMigration := flags.Bool("migrate-runtime-log-legacy-sqlite", false, "offline F1 legacy SQLite migration")
	healthAddress := flags.String("health-listen-address", envOrDefault("JUHE_AI_JOBS_HEALTH_LISTEN_ADDRESS", "127.0.0.1:3305"), "loopback health listen address")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *version {
		fmt.Fprintf(stdout, "juhe-ai-jobs project=%s contract=%s\n", contracts.ProjectJobs, contracts.ArchitectureVersion)
		return 0
	}
	if *check {
		fmt.Fprintln(stdout, "juhe-ai-jobs boundary=ready runtime=table-monitor-owner")
		return 0
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "unsupported jobs arguments: %v\n", flags.Args())
		return 2
	}
	if *once && *runtimeLegacyMigration {
		fmt.Fprintln(stderr, "--once and --migrate-runtime-log-legacy-sqlite are mutually exclusive")
		return 2
	}

	// JUHE_AI_LOG_LEVEL (Node log-level.ts): trace..silent, fail fast on an
	// invalid value like the Node startup guard.
	logLevel, err := processlog.LoadLevel(os.Getenv)
	if err != nil {
		return failWith(stderr, err)
	}
	logger := slog.New(slog.NewJSONHandler(stdout, &slog.HandlerOptions{Level: logLevel}))
	processlog.CatchPanic(logger)
	processlog.KeepAliveOnBrokenOutputPipe()
	// 废弃总开关检测（loadWorkerConfig / accounthealth.LoadConfig 之前）：
	// J1 与 worker 机制强制常开，遗留开关值非 true 时提醒部署方清理配置。
	warnDeprecatedSwitches(logger)
	ownerMode, err := ownermode.Load(os.Getenv)
	if err != nil {
		return failWith(stderr, err)
	}
	if !ownerMode.OwnsWork() {
		return runPassiveJobs(*healthAddress, ownerMode, logger, stdout, stderr)
	}
	postgresPools := pgpool.NewRegistry()
	defer postgresPools.Close()
	postgresPools.SetObserver(func(event pgpool.PoolEvent) {
		logger.Debug("jobs postgres pool event",
			"event", event.Kind,
			"role", event.Role,
			"max_open", event.MaxOpen,
			"max_idle", event.MaxIdle,
			"refs", event.Refs,
			"open", event.DBStats.OpenConnections,
			"in_use", event.DBStats.InUse,
			"idle", event.DBStats.Idle,
			"wait_count", event.DBStats.WaitCount,
			"wait_duration_ms", event.DBStats.WaitDuration.Milliseconds(),
		)
	})
	runtimeConfig, err := runtimelog.LoadConfig(os.Getenv)
	if err != nil {
		return failWith(stderr, fmt.Errorf("load F1 runtime-log-indexer config: %w", err))
	}
	if runtimeConfig.Once {
		return failWith(stderr, errors.New("JUHE_AI_RUNTIME_LOG_ONCE=true is not supported by juhe-ai-jobs; use --migrate-runtime-log-legacy-sqlite for the explicit offline F1 migration"))
	}
	if *runtimeLegacyMigration {
		return runRuntimeLegacyMigration(runtimeConfig, stdout, stderr)
	}
	runtimeStore, err := runtimelog.OpenStore(context.Background(), runtimeConfig)
	if err != nil {
		if runtimeConfig.Mode == runtimelog.ModeSQLite {
			if _, statErr := os.Stat(runtimeConfig.BusinessPath); statErr != nil {
				return failWith(stderr, fmt.Errorf("open F1 runtime-log-indexer store: %w（业务库 %s 尚不存在：先启动 juhe-ai-gateway（零配置下会自动初始化业务库），或先运行 juhe-ai-maintenance --ensure-schema）", err, runtimeConfig.BusinessPath))
			}
		}
		return failWith(stderr, fmt.Errorf("open F1 runtime-log-indexer store: %w", err))
	}
	defer runtimeStore.Close()
	if err := runtimelog.EnsureSchema(context.Background(), runtimeStore); err != nil {
		return failWith(stderr, fmt.Errorf("initialize F1 runtime-log-indexer schema: %w", err))
	}
	if err := runtimeStore.CheckSchema(context.Background()); err != nil {
		return failWith(stderr, fmt.Errorf("verify F1 runtime-log-indexer schema: %w", err))
	}
	cfg, err := tablemonitor.LoadConfig(os.Getenv)
	if err != nil {
		return failWith(stderr, fmt.Errorf("load F2 table-monitor config: %w", err))
	}
	if cfg.Mode == tablemonitor.ModePostgres {
		cfg.PostgresPool, err = postgresPools.Acquire("pgx", cfg.PostgresURL, "jobs-store", cfg.PostgresMaxOpenConns, cfg.PostgresMaxIdleConns)
		if err != nil {
			return failWith(stderr, fmt.Errorf("open F2 shared PostgreSQL pool: %w", err))
		}
	}
	store, err := tablemonitor.OpenStore(cfg)
	if err != nil {
		return failWith(stderr, fmt.Errorf("open F2 table-monitor store: %w", err))
	}
	defer store.Close()
	if err := store.EnsureSchema(context.Background()); err != nil {
		return failWith(stderr, fmt.Errorf("initialize F2 table-monitor schema: %w", err))
	}
	if *once {
		result, err := tablemonitor.RunSingleCycle(context.Background(), cfg, store)
		if err != nil {
			return failWith(stderr, fmt.Errorf("run F2 table-monitor sampling cycle: %w", err))
		}
		// 死臂已删（w16j 证据：Encode 到进程内内存 buffer 恒成功）
		_ = json.NewEncoder(stdout).Encode(result)
		return 0
	}
	accountHealthConfig, err := accounthealth.LoadConfig(os.Getenv)
	if err != nil {
		return failWith(stderr, fmt.Errorf("load J1 account-health config: %w", err))
	}
	var accountHealthStore *accounthealth.Store
	var accountHealthInputDB *sql.DB
	var accountHealthInputPool *pgpool.Handle
	// sqlite 直读的输入句柄（业务库 + 统计库；先输入句柄后 store 的 Close
	// 顺序由 J1 组件 Close 链保证）。
	var accountHealthBusinessInputDB *sql.DB
	var accountHealthStatsInputDB *sql.DB
	var accountHealthRunner *accounthealth.Runner
	var accountHealthReader accounthealth.DirectInputReader
	// J1 runner 恒装配（机制强制常开，2026-09-19 决策）；J1 outbox drain 的
	// 装配事实由下方 wireHealthProbeOutboxFace + SetProbeRequestDrain 承担。
	if accountHealthConfig.Store.Mode == accounthealth.StorePostgres {
		accountHealthConfig.Store.PostgresPool, err = postgresPools.Acquire("pgx", accountHealthConfig.Store.PostgresURL, "jobs-store", accountHealthConfig.Store.PostgresMaxOpenConns, accountHealthConfig.Store.PostgresMaxIdleConns)
		// 死臂已删（w16j 证据：pgx 惰性 Open）
	}
	accountHealthStore, err = accounthealth.OpenStore(accountHealthConfig.Store)
	if err != nil {
		return failWith(stderr, fmt.Errorf("open J1 account-health store: %w", err))
	}
	if err := accountHealthStore.EnsureSchema(context.Background()); err != nil {
		_ = accountHealthStore.Close()
		return failWith(stderr, fmt.Errorf("initialize J1 account-health schema: %w", err))
	}
	if accountHealthConfig.InputSource == "postgres" {
		accountHealthInputPool, err = postgresPools.Acquire("pgx", accountHealthConfig.BusinessPostgresURL, "business-input", accountHealthConfig.DirectInputPostgresMaxOpenConns, accountHealthConfig.DirectInputPostgresMaxIdleConns)
		// 死臂已删（w16j 证据：pgx 惰性 Open）
		accountHealthInputDB = accountHealthInputPool.DB()
		pingContext, pingCancel := context.WithTimeout(context.Background(), 10*time.Second)
		pingErr := accountHealthInputDB.PingContext(pingContext)
		pingCancel()
		if pingErr != nil {
			_ = accountHealthInputPool.Close()
			_ = accountHealthStore.Close()
			return failWith(stderr, fmt.Errorf("ping J1 account-health direct-input database: %w", pingErr))
		}
		// 死臂已删（w16j 证据：reader secret/TTL 校验与 LoadConfig 完全重叠）
		reader, _ := accounthealth.NewPostgresDirectInputReader(accountHealthInputDB, accountHealthConfig.CredentialSecret, accountHealthConfig.InputTTL, accountHealthConfig.Now)
		contractContext, contractCancel := context.WithTimeout(context.Background(), 10*time.Second)
		contractErr := reader.CheckContract(contractContext)
		contractCancel()
		if contractErr != nil {
			_ = accountHealthInputPool.Close()
			_ = accountHealthStore.Close()
			return failWith(stderr, fmt.Errorf("verify J1 account-health direct-input contract: %w", contractErr))
		}
		accountHealthReader = reader
		accountHealthRunner = accounthealth.NewRunnerWithDirectInputReader(accountHealthConfig, accountHealthStore, logger, reader)
	} else if accountHealthConfig.InputSource == "sqlite" {
		// 冷启动布局前置：全新环境下 business/stats 两库（含 J1 契约表与
		// settings 缺省）可能尚不存在，而 stats 库布局真正的常规创建方
		// buildWorkerAssembly 在本分支之后才运行——不前置会让只读 Ping/
		// CheckContract 拉停零配置冷启动（旧 files 模式不依赖两库，此为
		// sqlite 直读缺省化引入的回归）。ensure 与 stats-verify ensure 同一
		// DDL 且幂等，内部以 WAL 可写句柄即开即关，不与下方只读句柄共存。
		layoutCtx, layoutCancel := context.WithTimeout(context.Background(), 10*time.Second)
		layoutErr := accounthealth.EnsureSQLiteDirectInputLayout(layoutCtx, accountHealthConfig.BusinessSQLitePath, accountHealthConfig.StatsSQLitePath)
		layoutCancel()
		if layoutErr != nil {
			_ = accountHealthStore.Close()
			return failWith(stderr, fmt.Errorf("初始化 J1 sqlite 直读所需 SQLite 布局失败: %w", layoutErr))
		}
		// sqlite 直读：业务库/统计库两个只读句柄（query_only 强制只读，
		// busy_timeout 与 gateway 写侧短事务错峰；禁止 WAL/txlock=immediate 的
		// 写方 DSN——业务库写入由 gateway 单进程持有）。经上方布局前置后文件
		// 必已存在，Ping 失败只剩真实打开故障（损坏/锁死），仍 fail-fast。
		businessDB, businessErr := sql.Open("sqlite", sqliteReadOnlyFileDSN(accountHealthConfig.BusinessSQLitePath))
		if businessErr != nil {
			_ = accountHealthStore.Close()
			return failWith(stderr, fmt.Errorf("open J1 account-health sqlite direct-input business database: %w", businessErr))
		}
		businessDB.SetMaxOpenConns(1)
		businessPingCtx, businessPingCancel := context.WithTimeout(context.Background(), 10*time.Second)
		businessPingErr := businessDB.PingContext(businessPingCtx)
		businessPingCancel()
		if businessPingErr != nil {
			_ = businessDB.Close()
			_ = accountHealthStore.Close()
			// 布局前置已保证库文件存在，Ping 失败只剩打开/权限/锁或文件损坏
			// 类真实故障：提示指向路径与磁盘排查，不再误导为「gateway 未自举」。
			return failWith(stderr, fmt.Errorf("ping J1 account-health sqlite direct-input business database: %w（%s 已由布局前置创建；请检查路径、进程权限与文件是否被其他进程锁死或损坏）", businessPingErr, accountHealthConfig.BusinessSQLitePath))
		}
		statsDB, statsErr := sql.Open("sqlite", sqliteReadOnlyFileDSN(accountHealthConfig.StatsSQLitePath))
		if statsErr != nil {
			_ = businessDB.Close()
			_ = accountHealthStore.Close()
			return failWith(stderr, fmt.Errorf("open J1 account-health sqlite direct-input stats database: %w", statsErr))
		}
		statsDB.SetMaxOpenConns(1)
		statsPingCtx, statsPingCancel := context.WithTimeout(context.Background(), 10*time.Second)
		statsPingErr := statsDB.PingContext(statsPingCtx)
		statsPingCancel()
		if statsPingErr != nil {
			_ = statsDB.Close()
			_ = businessDB.Close()
			_ = accountHealthStore.Close()
			return failWith(stderr, fmt.Errorf("ping J1 account-health sqlite direct-input stats database: %w", statsPingErr))
		}
		// 死臂已删（w16j 证据：reader secret/TTL 校验与 LoadConfig 完全重叠）。
		reader, _ := accounthealth.NewSQLiteDirectInputReader(businessDB, statsDB, accountHealthConfig.CredentialSecret, accountHealthConfig.InputTTL, accountHealthConfig.Now)
		contractContext, contractCancel := context.WithTimeout(context.Background(), 10*time.Second)
		contractErr := reader.CheckContract(contractContext)
		contractCancel()
		if contractErr != nil {
			_ = statsDB.Close()
			_ = businessDB.Close()
			_ = accountHealthStore.Close()
			return failWith(stderr, fmt.Errorf("verify J1 account-health direct-input contract: %w", contractErr))
		}
		accountHealthBusinessInputDB = businessDB
		accountHealthStatsInputDB = statsDB
		accountHealthReader = reader
		accountHealthRunner = accounthealth.NewRunnerWithDirectInputReader(accountHealthConfig, accountHealthStore, logger, reader)
	} else {
		accountHealthRunner = accounthealth.NewRunner(accountHealthConfig, accountHealthStore, logger)
	}
	modelRecoveryConfig, err := keymodelrecovery.LoadRedisConfig(os.Getenv)
	if err != nil {
		return failWith(stderr, fmt.Errorf("load model-recovery config: %w", err))
	}
	var modelRecoveryStore *keymodelrecovery.RedisStore
	var modelRecoveryRunner *keymodelrecovery.Runner
	if modelRecoveryConfig.Enabled {
		if accountHealthReader == nil {
			return failWith(stderr, errors.New("启用 model-recovery 必须同时启用 J1 direct input reader（postgres 或 sqlite）"))
		}
		modelRecoveryStore, err = keymodelrecovery.OpenRedisStore(modelRecoveryConfig)
		if err != nil {
			return failWith(stderr, fmt.Errorf("open model-recovery Redis store: %w", err))
		}
		pingCtx, pingCancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = modelRecoveryStore.Ping(pingCtx)
		pingCancel()
		if err != nil {
			_ = modelRecoveryStore.Close()
			return failWith(stderr, fmt.Errorf("ping model-recovery Redis store: %w", err))
		}
		modelRecoveryRunner = keymodelrecovery.NewRunner(modelRecoveryStore, accountHealthReader, logger)
	}
	accountBalanceConfig, err := accountbalance.LoadRuntimeConfig(os.Getenv)
	if err != nil {
		return failWith(stderr, fmt.Errorf("load J2 account-balance config: %w", err))
	}
	var accountBalanceService *accountbalance.Service
	if accountBalanceConfig.Enabled {
		accountBalanceConfig.PostgresPool, err = postgresPools.Acquire("pgx", accountBalanceConfig.Store.PostgresURL, "jobs-store", accountBalanceConfig.PostgresMaxOpenConns, accountBalanceConfig.PostgresMaxIdleConns)
		// 死臂已删（w16j 证据：pgx 惰性 Open）
		accountBalanceConfig.InputPostgresPool, err = postgresPools.Acquire("pgx", accountBalanceConfig.BusinessPostgresURL, "business-input", accountBalanceConfig.InputPostgresMaxOpenConns, accountBalanceConfig.InputPostgresMaxIdleConns)
		// 死臂已删（w16j 证据：pgx 惰性 Open）
		accountBalanceService, err = accountbalance.NewService(accountBalanceConfig, logger)
		if err != nil {
			return failWith(stderr, fmt.Errorf("initialize J2 account-balance service: %w", err))
		}
	}
	j3Config, err := proxylatency.LoadRuntimeConfig(os.Getenv)
	if err != nil {
		return failWith(stderr, fmt.Errorf("load J3a proxy-latency config: %w", err))
	}
	j3ManagementConfig, err := proxylatency.LoadManualAdminConfig(os.Getenv)
	if err != nil {
		return failWith(stderr, fmt.Errorf("load J3a proxy-latency management config: %w", err))
	}
	if j3ManagementConfig.Enabled && !j3Config.Enabled {
		return failWith(stderr, errors.New("启用 J3a 管理接口前必须启用 J3a Go owner"))
	}
	var j3Store *proxylatency.Store
	var j3InputDB *sql.DB
	var j3InputPool *pgpool.Handle
	var j3ResultDB *sql.DB
	var j3ResultPool *pgpool.Handle
	var j3Runner *proxylatency.Runner
	var j3Projector *proxylatency.ResultProjector
	var j3ManagementDB *sql.DB
	var j3ManagementPool *pgpool.Handle
	var j3ManagementSource *proxylatency.PostgresManualAdminSource
	if j3Config.Enabled {
		j3Config.Store.PostgresMaxOpenConns = j3Config.PostgresMaxOpenConns
		j3Config.Store.PostgresMaxIdleConns = j3Config.PostgresMaxIdleConns
		j3Config.Store.PostgresPool, err = postgresPools.Acquire("pgx", j3Config.Store.PostgresURL, "jobs-store", j3Config.Store.PostgresMaxOpenConns, j3Config.Store.PostgresMaxIdleConns)
		// 死臂已删（w16j 证据：pgx 惰性 Open）
		j3Store, err = proxylatency.OpenStore(j3Config.Store)
		// 死臂已删（w16j 证据：pool 前置注入 + URL/limits LoadConfig 已校验）
		if err := j3Store.CheckSchema(context.Background()); err != nil {
			_ = j3Store.Close()
			return failWith(stderr, fmt.Errorf("verify pre-provisioned J3a proxy-latency jobs schema: %w", err))
		}
		j3InputPool, err = postgresPools.Acquire("pgx", j3Config.BusinessPostgresURL, "business-input", j3Config.InputPostgresMaxOpenConns, j3Config.InputPostgresMaxIdleConns)
		// 死臂已删（w16j 证据：pgx 惰性 Open）
		j3InputDB = j3InputPool.DB()
		pingContext, pingCancel := context.WithTimeout(context.Background(), 10*time.Second)
		pingErr := j3InputDB.PingContext(pingContext)
		pingCancel()
		if pingErr != nil {
			_ = j3InputPool.Close()
			_ = j3Store.Close()
			return failWith(stderr, fmt.Errorf("ping J3a proxy-latency direct-input database: %w", pingErr))
		}
		// 死臂已删（w16j 证据：reader TTL 校验与 LoadConfig 完全重叠）
		reader, _ := proxylatency.NewPostgresDirectInputReader(j3InputDB, j3Config.InputTTL, j3Config.Now)
		contractContext, contractCancel := context.WithTimeout(context.Background(), 10*time.Second)
		contractErr := reader.CheckContract(contractContext)
		contractCancel()
		if contractErr != nil {
			_ = j3InputPool.Close()
			_ = j3Store.Close()
			return failWith(stderr, fmt.Errorf("verify J3a proxy-latency direct-input contract: %w", contractErr))
		}
		j3ResultPool, err = postgresPools.Acquire("pgx", j3Config.ResultPostgresURL, "business-result", j3Config.InputPostgresMaxOpenConns, j3Config.InputPostgresMaxIdleConns)
		// 死臂已删（w16j 证据：pgx 惰性 Open）
		j3ResultDB = j3ResultPool.DB()
		resultProjector, projectorErr := proxylatency.NewResultProjector(j3Store, j3ResultDB, proxylatency.ResultProjectorConfig{PollInterval: time.Second, BatchSize: j3Config.BatchSize, Now: j3Config.Now}, logger)
		if projectorErr != nil {
			_ = j3ResultPool.Close()
			_ = j3InputPool.Close()
			_ = j3Store.Close()
			return failWith(stderr, fmt.Errorf("initialize J3a Go business-result projector: %w", projectorErr))
		}
		projectorContext, projectorCancel := context.WithTimeout(context.Background(), 10*time.Second)
		projectorContractErr := resultProjector.CheckContract(projectorContext)
		projectorCancel()
		if projectorContractErr != nil {
			_ = j3ResultPool.Close()
			_ = j3InputPool.Close()
			_ = j3Store.Close()
			return failWith(stderr, fmt.Errorf("verify J3a Go business-result contract: %w", projectorContractErr))
		}
		j3Runner = proxylatency.NewRunner(j3Config, j3Store, reader, logger)
		j3Runner.SetResultProjector(resultProjector)
		j3Projector = resultProjector
		if j3ManagementConfig.Enabled {
			j3ManagementPool, err = postgresPools.Acquire("pgx", j3ManagementConfig.PostgresURL, "proxy-latency-management", j3ManagementConfig.MaxOpenConns, j3ManagementConfig.MaxIdleConns)
			if err != nil {
				_ = j3ResultPool.Close()
				_ = j3InputPool.Close()
				_ = j3Store.Close()
				return failWith(stderr, fmt.Errorf("open J3a management PostgreSQL pool: %w", err))
			}
			j3ManagementDB = j3ManagementPool.DB()
			j3ManagementSource, err = proxylatency.NewPostgresManualAdminSource(j3ManagementDB, j3Config.Now)
			if err != nil {
				_ = j3ManagementPool.Close()
				_ = j3ResultPool.Close()
				_ = j3InputPool.Close()
				_ = j3Store.Close()
				return failWith(stderr, fmt.Errorf("initialize J3a management source: %w", err))
			}
			managementContractCtx, managementContractCancel := context.WithTimeout(context.Background(), 10*time.Second)
			managementContractErr := j3ManagementSource.CheckContract(managementContractCtx)
			managementContractCancel()
			if managementContractErr != nil {
				_ = j3ManagementPool.Close()
				_ = j3ResultPool.Close()
				_ = j3InputPool.Close()
				_ = j3Store.Close()
				return failWith(stderr, fmt.Errorf("verify J3a management PostgreSQL contract: %w", managementContractErr))
			}
		}
	}
	j3bConfig, err := modelcheckruntime.LoadConfig(os.Getenv)
	if err != nil {
		return failWith(stderr, fmt.Errorf("load J3b model-check config: %w", err))
	}
	if j3bConfig.Enabled {
		// Solution A reserves the J3b runtime for Gateway. Keep this guard even
		// when a future config parser changes, so jobs can never become a second
		// owner through an accidental startup path.
		return failWith(stderr, errors.New("J3b runtime is Gateway-owned; juhe-ai-jobs cannot be enabled"))
	}

	// worker 组合根（J-A~J-F 任务族）：机制强制常开（2026-09-19 决策，
	// JUHE_AI_JOBS_WORKER_ENABLED 已废弃），只在 owner 模式装配。
	workerCfg, err := loadWorkerConfig(os.Getenv)
	if err != nil {
		return failWith(stderr, fmt.Errorf("load jobs worker config: %w", err))
	}
	worker, err := buildWorkerAssembly(workerCfg, logger)
	if err != nil {
		return failWith(stderr, fmt.Errorf("assemble jobs worker: %w", err))
	}
	workerReady := worker.ready
	workerStatus := worker.statusPayload
	// 健康检查派发 outbox 消费与清理面（去跨进程战役第二刀）：J1 runner 是
	// 唯一探测者，worker 业务库提供 boundary/outbox 读侧
	// （worker_health_probe_outbox.go）。J1 恒装配，drain 随之恒接线
	// （装配失败降级 warn 等同消费面缺席）；prune 组件独立常驻，pending
	// 堆积由保留期删除兜底。装配失败降级 warn，不阻塞启动。
	var probeOutboxPruner *healthProbeOutboxPruner
	var healthOutcomeProjector *accounthealth.OutcomeProjector
	{
		face, faceErr := worker.wireHealthProbeOutboxFace(os.Getenv)
		if faceErr != nil {
			logger.Warn("账户健康探针 outbox 消费面装配失败；outbox 行保持 pending",
				"event", "account_health_probe_outbox_assembly_failed", "error", faceErr.Error())
		} else {
			if face.drain != nil && accountHealthRunner != nil {
				accountHealthRunner.SetProbeRequestDrain(face.drain)
			}
			probeOutboxPruner = face.pruner
		}
		// J1 outcome → 业务账户投影面（BUG-0174 M-1）：独立组件恢复
		// 「探活成功→账户回归轮换」闭环；env 显式关闭时缺席，装配
		// 失败降级 warn（outcome 仅停留 juhe_jobs 审计面），不阻塞启动。
		projector, projectorErr := worker.wireHealthOutcomeProjector(os.Getenv, accountHealthStore)
		if projectorErr != nil {
			logger.Warn("J1 outcome 投影面装配失败；outcome 仅停留 juhe_jobs 审计面",
				"event", "account_health_projection_assembly_failed", "error", projectorErr.Error())
		} else {
			healthOutcomeProjector = projector
		}
	}

	listener, err := listenLoopback(*healthAddress)
	if err != nil {
		return failWith(stderr, fmt.Errorf("listen jobs health endpoint %q: %w", *healthAddress, err))
	}
	defer listener.Close()
	tableRunner := tablemonitor.NewRunner(cfg, store, logger)
	// go-runtime-metrics 采样器来自共享 platform/gometrics（去跨进程战役第三
	// 刀：Sampler/config 迁入共享包，gateway 进程内自采样 role=gateway，jobs
	// 继续自采样 role=jobs；跨进程 trend HTTP 面已删除）。
	goMetricsConfig, err := gometrics.LoadConfig(os.Getenv, "jobs")
	if err != nil {
		return failWith(stderr, fmt.Errorf("load Go runtime metrics config: %w", err))
	}
	goMetricsCollector := gometrics.New(goMetricsConfig.Service, goMetricsConfig.Role)
	var goMetricsStore *gometrics.Store
	var goMetricsDB *sql.DB
	var goMetricsSampler *gometrics.Sampler
	if goMetricsConfig.Enabled {
		goMetricsStore, goMetricsDB, err = gometrics.OpenStore(goMetricsConfig)
		if err != nil {
			return failWith(stderr, fmt.Errorf("open Go runtime metrics store: %w", err))
		}
		if err := gometrics.EnsureReady(context.Background(), goMetricsStore); err != nil {
			_ = goMetricsDB.Close()
			return failWith(stderr, fmt.Errorf("verify Go runtime metrics schema: %w", err))
		}
		goMetricsSampler, err = gometrics.NewSampler(goMetricsCollector, goMetricsStore, goMetricsConfig.Interval)
		if err != nil {
			_ = goMetricsDB.Close()
			return failWith(stderr, fmt.Errorf("initialize Go runtime metrics sampler: %w", err))
		}
		goMetricsSampler.Retention = time.Duration(goMetricsConfig.RetentionDays) * 24 * time.Hour
	}
	runtimeIndexer := runtimelog.NewIndexer(runtimeConfig, runtimeStore)
	var runtimeRunning atomic.Bool
	components := []supervisor.Component{
		{
			Name: "F1 runtime-log-indexer",
			Run: func(runCtx context.Context) error {
				runtimeRunning.Store(true)
				defer runtimeRunning.Store(false)
				return runtimelog.RunWithOwnerLease(runCtx, runtimeConfig, runtimeStore, runtimeIndexer.Run)
			},
			Close: runtimeStore.Close,
		},
		{
			Name:  "F2 table-monitor",
			Run:   tableRunner.Run,
			Close: store.Close,
		},
	}
	if probeOutboxPruner != nil {
		// 业务库句柄的关闭由 worker 组件 Close（closeStores）承担；
		// supervisor 保证所有组件 Run 停止后才调用 Close。
		components = append(components, supervisor.Component{
			Name: "account-health probe-outbox prune",
			Run:  probeOutboxPruner.Run,
		})
	}
	if goMetricsSampler != nil {
		components = append(components, supervisor.Component{
			Name: "Go runtime metrics sampler",
			Run:  goMetricsSampler.Run,
			Close: func() error {
				if goMetricsDB == nil {
					return nil
				}
				return goMetricsDB.Close()
			},
		})
	}
	accountHealthReady := func() bool { return true }
	if accountHealthRunner != nil {
		accountHealthReady = accountHealthRunner.Ready
		components = append(components, supervisor.Component{
			Name: "J1 account-health",
			Run:  accountHealthRunner.Run,
			Close: func() error {
				var closeErr error
				if accountHealthInputPool != nil {
					closeErr = accountHealthInputPool.Close()
				}
				if accountHealthStatsInputDB != nil {
					if err := accountHealthStatsInputDB.Close(); err != nil && closeErr == nil {
						closeErr = err
					}
				}
				if accountHealthBusinessInputDB != nil {
					if err := accountHealthBusinessInputDB.Close(); err != nil && closeErr == nil {
						closeErr = err
					}
				}
				if err := accountHealthStore.Close(); err != nil && closeErr == nil {
					closeErr = err
				}
				return closeErr
			},
		})
	}
	if healthOutcomeProjector != nil {
		// 独立于 J1 runCycle（归档投影运行时即独立轮询循环）；业务库句柄的
		// 关闭由 worker 组件 Close（closeStores）承担，本组件无独立 Close。
		components = append(components, supervisor.Component{
			Name: "J1 outcome projection",
			Run:  healthOutcomeProjector.Run,
		})
	}
	if modelRecoveryRunner != nil {
		components = append(components, supervisor.Component{
			Name:  "model-recovery key-model",
			Run:   modelRecoveryRunner.Run,
			Close: modelRecoveryStore.Close,
		})
	}
	accountBalanceReady := func() bool { return true }
	if accountBalanceService != nil {
		accountBalanceReady = accountBalanceService.Ready
		components = append(components, supervisor.Component{
			Name:  "J2 account-balance",
			Run:   accountBalanceService.Run,
			Close: accountBalanceService.Close,
		})
	}
	j3Ready := func() bool { return true }
	if j3Runner != nil {
		j3Ready = j3Runner.Ready
		components = append(components, supervisor.Component{
			Name: "J3a Go business-result-projector",
			Run:  j3Projector.Run,
			Close: func() error {
				if j3ResultPool != nil {
					return j3ResultPool.Close()
				}
				return nil
			},
		})
		components = append(components, supervisor.Component{
			Name: "J3a proxy-latency",
			Run:  j3Runner.Run,
			Close: func() error {
				var closeErr error
				if j3InputPool != nil {
					closeErr = j3InputPool.Close()
				}
				if err := j3Store.Close(); err != nil && closeErr == nil {
					closeErr = err
				}
				return closeErr
			},
		})
	}
	if j3ManagementConfig.Enabled {
		managementListener, listenErr := net.Listen("tcp", j3ManagementConfig.ListenAddress)
		if listenErr != nil {
			return failWith(stderr, fmt.Errorf("listen J3a management endpoint %q: %w", j3ManagementConfig.ListenAddress, listenErr))
		}
		managementServer := &http.Server{
			Handler:           proxylatency.NewManualAdminHandler(j3Runner, j3ManagementSource, proxylatency.NewPostgresManualAdminAuditAppender(j3ManagementDB), j3ManagementConfig.RequestDeadline, logger),
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       j3ManagementConfig.RequestDeadline + 5*time.Second,
			WriteTimeout:      j3ManagementConfig.RequestDeadline + 5*time.Second,
			IdleTimeout:       30 * time.Second,
		}
		// P2-3：Serve 持久报错时 supervisor 会在同一根 ctx 下反复调用本 Run，
		// stop 监听 goroutine 不能随每次 Run 重复创建（按次累积）。stopDone 与
		// goroutine 一起提升到组件作用域：supervisor 对每次重试传入同一根 ctx，
		// sync.Once 固定首个 runCtx 即唯一停机信号源；stopDone 若留在 Run 内，
		// 重试后的 ErrServerClosed 路径会阻塞在无人关闭的局部 channel 上，
		// 复现「Run 等不可达收尾」的停机死锁。
		var stopOnce sync.Once
		stopDone := make(chan struct{})
		components = append(components, supervisor.Component{
			Name: "J3a management API",
			Run: func(runCtx context.Context) error {
				// supervisor 契约是全部组件 Run 返回后才逆序调 Close；Serve 只能
				// 被 Shutdown 解阻，若把 Shutdown 放在 Close 侧会构成「Run 等
				// Close、Close 等全部 Run」的停机死锁（ctx 取消后其余组件全部
				// 停止，本组件 Serve 永远阻塞）。Run 侧监听停机信号自行 Shutdown
				//（5s 上限）；Close 收缩为只关 management 连接池。
				stopOnce.Do(func() {
					go func() {
						defer close(stopDone)
						<-runCtx.Done()
						shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
						defer cancel()
						_ = managementServer.Shutdown(shutdownCtx)
					}()
				})
				err := managementServer.Serve(managementListener)
				if errors.Is(err, http.ErrServerClosed) {
					// 常规停机路径：等 Shutdown goroutine 收尾后再返回。
					<-stopDone
					return nil
				}
				return err
			},
			Close: func() error {
				return j3ManagementPool.Close()
			},
		})
	}
	j3bReady := func() bool { return true }
	healthServer := &http.Server{
		Handler: jobsHTTPHandler(ownerMode, &runtimeRunning, tableRunner.Ready, true, accountHealthReady, accountBalanceConfig.Enabled, accountBalanceReady, accountBalanceService, accountBalanceConfig.ManualHTTPSecret, j3Config.Enabled, j3Ready, func() proxylatency.RunnerStatus {
			if j3Runner == nil {
				return proxylatency.RunnerStatus{}
			}
			return j3Runner.Status()
		}, func() (proxylatency.RunnerStatus, bool) {
			if j3Runner == nil {
				return proxylatency.RunnerStatus{}, true
			}
			return j3Runner.Snapshot()
		}, false, j3bReady, goMetricsCollector, goMetricsSampler,
			true, workerReady, workerStatus),
		ReadHeaderTimeout: 5 * time.Second,
	}
	ctx, stop := hooksSignalNotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serveErr := make(chan error, 1)
	go func() { serveErr <- healthServer.Serve(listener) }()
	logger.Info("juhe-ai-jobs started", "healthAddress", listener.Addr().String(), "job", "table-monitor", "accountHealthEnabled", true, "accountHealthInputSource", accountHealthConfig.InputSource, "modelRecoveryEnabled", modelRecoveryConfig.Enabled, "accountBalanceEnabled", accountBalanceConfig.Enabled, "proxyLatencyEnabled", j3Config.Enabled, "modelCheckEnabled", false, "workerEnabled", true, "workerWiredJobs", worker.wiredJobs)
	components = append(components, worker.components()...)
	runErr := supervisor.Run(ctx, components, logger)
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	shutdownErr := healthServer.Shutdown(shutdownCtx)
	shutdownCancel()
	serveResult := <-serveErr
	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		return failWith(stderr, fmt.Errorf("F2 table-monitor runner stopped: %w", runErr))
	}
	if shutdownErr != nil {
		return failWith(stderr, fmt.Errorf("shutdown jobs health endpoint: %w", shutdownErr))
	}
	if serveResult != nil && !errors.Is(serveResult, http.ErrServerClosed) {
		return failWith(stderr, fmt.Errorf("jobs health endpoint stopped: %w", serveResult))
	}
	return 0
}

// warnDeprecatedSwitches 对已移除的 Node→Go 迁移过渡期总开关输出废弃告警：
// J1 账户健康与 worker 任务族自 2026-09-19 起强制常开（原防双 owner 理由已随
// Node 后端归档失效），两个变量不再被任何配置读取；值（trim + 大小写不敏感）
// 恰为 true 时保持静默（向后兼容，存量部署的 true 配置不刷告警）。
func warnDeprecatedSwitches(logger *slog.Logger) {
	for _, switchEnv := range []struct{ name, event string }{
		{"JUHE_AI_ACCOUNT_HEALTH_ENABLED", "account_health_enabled_deprecated"},
		{"JUHE_AI_JOBS_WORKER_ENABLED", "jobs_worker_enabled_deprecated"},
	} {
		if value, ok := os.LookupEnv(switchEnv.name); ok && !strings.EqualFold(strings.TrimSpace(value), "true") {
			logger.Warn(fmt.Sprintf("环境变量 %s 已废弃：核心机制强制常开，该值不再生效", switchEnv.name), "event", switchEnv.event)
		}
	}
}

// runPassiveJobs never initializes stores or leases. It exists only for a
// candidate readiness endpoint during standby/drain; ownerReady stays false.
// 返回进程退出码（原 fail() 出口收敛为返回值，stderr 文案不变）。
func runPassiveJobs(healthAddress string, ownerMode ownermode.Mode, logger *slog.Logger, stdout io.Writer, stderr io.Writer) int {
	listener, err := listenLoopback(healthAddress)
	if err != nil {
		return failWith(stderr, fmt.Errorf("listen passive jobs health endpoint %q: %w", healthAddress, err))
	}
	defer listener.Close()
	server := &http.Server{Handler: passiveJobsHealthHandler(ownerMode), ReadHeaderTimeout: 5 * time.Second}
	ctx, stop := hooksSignalNotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	logger.Info("juhe-ai-jobs passive", "healthAddress", listener.Addr().String(), "ownerMode", ownerMode)
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	shutdownErr := server.Shutdown(shutdownCtx)
	cancel()
	serveResult := <-serveErr
	if shutdownErr != nil {
		return failWith(stderr, fmt.Errorf("shutdown passive jobs health endpoint: %w", shutdownErr))
	}
	if serveResult != nil && !errors.Is(serveResult, http.ErrServerClosed) {
		return failWith(stderr, fmt.Errorf("passive jobs health endpoint stopped: %w", serveResult))
	}
	return 0
}

func listenLoopback(address string) (net.Listener, error) {
	if err := validateLoopbackListenAddress(address); err != nil {
		return nil, err
	}
	return net.Listen("tcp", address)
}

func validateLoopbackListenAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid loopback listen address %q: %w", address, err)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return fmt.Errorf("invalid loopback listen address %q: port must be between 1 and 65535", address)
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !isLoopbackListenIP(ip) {
		return fmt.Errorf("invalid loopback listen address %q: host must be localhost or a loopback IP", address)
	}
	return nil
}

func isLoopbackListenIP(ip net.IP) bool {
	if ipv4 := ip.To4(); ipv4 != nil {
		return ipv4[0] == 127
	}
	return ip.Equal(net.IPv6loopback)
}

func passiveJobsHealthHandler(ownerMode ownermode.Mode) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/health" {
			http.NotFound(response, request)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(map[string]any{
			"ready":                 false,
			"ownerReady":            false,
			"ownerMode":             ownerMode,
			"runtimeLogOwnerHeld":   false,
			"tableMonitorReady":     false,
			"accountHealthEnabled":  false,
			"accountHealthReady":    false,
			"accountBalanceEnabled": false,
			"accountBalanceReady":   false,
			"proxyLatencyEnabled":   false,
			"proxyLatencyReady":     false,
			"modelCheckEnabled":     false,
			"modelCheckReady":       false,
		})
	})
}

func jobsHTTPHandler(ownerMode ownermode.Mode, runtimeRunning *atomic.Bool, tableMonitorReady func() bool, accountHealthEnabled bool, accountHealthReady func() bool, accountBalanceEnabled bool, accountBalanceReady func() bool, accountBalanceService *accountbalance.Service, accountBalanceManualSecret string, j3 ...any) http.Handler {
	mux := http.NewServeMux()
	goCollector := gometrics.New("juhe-ai", "jobs")
	if len(j3) > 6 {
		if collector, ok := j3[6].(*gometrics.Collector); ok && collector != nil {
			goCollector = collector
		}
	}
	mux.Handle("/__aisys__/metrics", goCollector.Handler())
	// 去跨进程战役第三刀：/__aisys__/api/stats/go-runtime-trend 路由已删除
	// （TrendHandler 随 gometricsstore 包一起消失；trend 读取改由 gateway
	// 进程内直查共享 Store）。j3[7] 的 *gometrics.Sampler 槽位保留占位，避免
	// 后续 worker 槽位漂移。
	// 去跨进程战役第四刀：/account-balance/manual 手动桥已删除（Node 时代
	// 的手动触发入口，全仓无生产调用方；J2 余额刷新走周期调度与恢复扫描）。
	// 健康监听只剩 /health 与 /__aisys__/metrics（健康探测属编排语义，
	// gateway 蓝绿 readiness 探测与 Prometheus 抓取仍依赖，保留）。
	// 去跨进程战役第二刀：/__aiinternal__ 路由整体消失（原账户测试派发与
	// 账户健康检查派发 handler 已删除；健康检查派发改走 DB outbox 通道，
	// 见 worker_health_probe_outbox.go）。
	// readinessArgs mirrors the healthHandler j2 layout:
	// [accountBalanceEnabled, accountBalanceReady, proxyLatencyEnabled,
	// proxyLatencyReady, proxyLatencyStatus, proxyLatencySnapshot,
	// modelCheckEnabled, modelCheckReady, workerEnabled, workerReady,
	// workerStatus]. The goMetrics slots (j3[6]/j3[7]) are jobsHTTPHandler-only
	// surface and must NOT leak into the readiness args — a leaked
	// *gometrics.Collector would shift the worker fields into the wrong slots
	// and /health would always report workerEnabled=false with no worker
	// snapshot (X05 defect).
	readinessArgs := append([]any{accountBalanceEnabled, accountBalanceReady}, j3[:min(len(j3), 6)]...)
	if len(j3) > 8 {
		readinessArgs = append(readinessArgs, j3[8:min(len(j3), 11)]...)
	}
	mux.Handle("/health", healthHandler(ownerMode, runtimeRunning, tableMonitorReady, accountHealthEnabled, accountHealthReady, readinessArgs...))
	mux.HandleFunc("/account-balance/manual", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || accountBalanceService == nil {
			http.NotFound(response, request)
			return
		}
		if !matchesAccountBalanceManualSecret(request, accountBalanceManualSecret) {
			response.Header().Set("WWW-Authenticate", `Bearer realm="juhe-ai-jobs"`)
			http.Error(response, "J2 manual bridge 未授权", http.StatusUnauthorized)
			return
		}
		request.Body = http.MaxBytesReader(response, request.Body, 512<<10)
		var envelope struct {
			Input accountbalance.Input `json:"input"`
		}
		decoder := json.NewDecoder(request.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&envelope); err != nil {
			http.Error(response, "J2 manual input 无效", http.StatusBadRequest)
			return
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			http.Error(response, "J2 manual input 不得包含尾随 JSON", http.StatusBadRequest)
			return
		}
		if envelope.Input.Trigger == "" {
			envelope.Input.Trigger = accountbalance.TriggerManual
		}
		record, _, err := accountBalanceService.RunManual(request.Context(), envelope.Input)
		if err != nil {
			status := http.StatusBadGateway
			if errors.Is(err, accountbalance.ErrAccountLeaseHeld) {
				status = http.StatusConflict
			}
			if errors.Is(err, accountbalance.ErrOutcomeStale) {
				response.Header().Set("Content-Type", "application/json")
				response.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(response).Encode(manualHandoverResult(envelope.Input, accountbalance.Snapshot{Status: accountbalance.StatusPending}, envelope.Input.NextRefreshAt, false, "stale"))
				return
			}
			http.Error(response, err.Error(), status)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(manualHandoverResult(envelope.Input, record.Snapshot, record.NextRefreshAt, true, manualOutcome(record.Snapshot.Status)))
	})
	return mux
}

func matchesAccountBalanceManualSecret(request *http.Request, expected string) bool {
	if request == nil || len(expected) < 32 {
		return false
	}
	const prefix = "Bearer "
	provided := request.Header.Get("Authorization")
	if len(provided) < len(prefix) || provided[:len(prefix)] != prefix {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided[len(prefix):]), []byte(expected)) == 1
}

func manualHandoverResult(input accountbalance.Input, snapshot accountbalance.Snapshot, nextRefreshAfter *time.Time, committed bool, outcome string) map[string]any {
	result := map[string]any{
		"schemaVersion":    1,
		"job":              "account-balance-refresh",
		"accountId":        input.AccountID,
		"systemAccountId":  input.SystemAccountID,
		"configRevision":   input.ConfigRevision,
		"nextRefreshAfter": nextRefreshAfter,
		"outcome":          outcome,
		"committed":        committed,
		"snapshot":         snapshot,
	}
	if input.Trigger != accountbalance.TriggerManual {
		result["expectedNextRefreshAt"] = input.NextRefreshAt
	}
	return map[string]any{
		"schemaVersion": 1,
		"job":           "account-balance-refresh",
		"result":        result,
	}
}

func manualOutcome(status accountbalance.Status) string {
	if status == accountbalance.StatusUnsupported {
		return "unsupported"
	}
	if status == accountbalance.StatusFresh || status == accountbalance.StatusUnlimited {
		return "refreshed"
	}
	return "failed"
}

func healthHandler(ownerMode ownermode.Mode, runtimeRunning *atomic.Bool, tableMonitorReady func() bool, accountHealthEnabled bool, accountHealthReady func() bool, j2 ...any) http.Handler {
	accountBalanceEnabled := false
	accountBalanceReady := func() bool { return true }
	proxyLatencyEnabled := false
	proxyLatencyReady := func() bool { return true }
	proxyLatencyStatus := func() proxylatency.RunnerStatus { return proxylatency.RunnerStatus{} }
	proxyLatencySnapshot := func() (proxylatency.RunnerStatus, bool) {
		return proxyLatencyStatus(), proxyLatencyReady()
	}
	modelCheckEnabled := false
	modelCheckReady := func() bool { return true }
	workerEnabled := false
	workerReady := func() bool { return true }
	workerStatus := func() map[string]any { return nil }
	if len(j2) > 0 {
		if value, ok := j2[0].(bool); ok {
			accountBalanceEnabled = value
		}
	}
	if len(j2) > 1 {
		if value, ok := j2[1].(func() bool); ok {
			accountBalanceReady = value
		}
	}
	if len(j2) > 2 {
		if value, ok := j2[2].(bool); ok {
			proxyLatencyEnabled = value
		}
	}
	if len(j2) > 3 {
		if value, ok := j2[3].(func() bool); ok {
			proxyLatencyReady = value
		}
	}
	if len(j2) > 4 {
		if value, ok := j2[4].(func() proxylatency.RunnerStatus); ok {
			proxyLatencyStatus = value
		}
	}
	if len(j2) > 5 {
		if value, ok := j2[5].(func() (proxylatency.RunnerStatus, bool)); ok {
			proxyLatencySnapshot = value
		}
	}
	if len(j2) > 6 {
		if value, ok := j2[6].(bool); ok {
			modelCheckEnabled = value
		}
	}
	if len(j2) > 7 {
		if value, ok := j2[7].(func() bool); ok {
			modelCheckReady = value
		}
	}
	if len(j2) > 8 {
		if value, ok := j2[8].(bool); ok {
			workerEnabled = value
		}
	}
	if len(j2) > 9 {
		if value, ok := j2[9].(func() bool); ok {
			workerReady = value
		}
	}
	if len(j2) > 10 {
		if value, ok := j2[10].(func() map[string]any); ok {
			workerStatus = value
		}
	}
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/health" {
			http.NotFound(response, request)
			return
		}
		runtimeLogOwnerHeld := runtimeRunning.Load()
		tableMonitorIsReady := tableMonitorReady()
		accountHealthIsReady := !accountHealthEnabled || accountHealthReady()
		accountBalanceIsReady := !accountBalanceEnabled || accountBalanceReady()
		proxyStatus, proxyReady := proxyLatencySnapshot()
		proxyLatencyIsReady := !proxyLatencyEnabled || proxyReady
		modelCheckIsReady := !modelCheckEnabled || modelCheckReady()
		workerIsReady := !workerEnabled || workerReady()
		response.Header().Set("Content-Type", "application/json")
		ready := runtimeLogOwnerHeld && tableMonitorIsReady && accountHealthIsReady && accountBalanceIsReady && proxyLatencyIsReady && modelCheckIsReady && workerIsReady
		payload := map[string]any{
			"ready":                         ready,
			"ownerReady":                    ready,
			"ownerMode":                     ownerMode,
			"runtimeLogOwnerHeld":           runtimeLogOwnerHeld,
			"tableMonitorReady":             tableMonitorIsReady,
			"accountHealthEnabled":          accountHealthEnabled,
			"accountHealthReady":            accountHealthIsReady,
			"accountBalanceEnabled":         accountBalanceEnabled,
			"accountBalanceReady":           accountBalanceIsReady,
			"proxyLatencyEnabled":           proxyLatencyEnabled,
			"proxyLatencyReady":             proxyLatencyIsReady,
			"modelCheckEnabled":             modelCheckEnabled,
			"modelCheckReady":               modelCheckIsReady,
			"workerEnabled":                 workerEnabled,
			"workerReady":                   workerIsReady,
			"proxyLatencyOwnerHeld":         proxyStatus.OwnerHeld,
			"proxyLatencyLastCycleAt":       proxylatencyTime(proxyStatus.LastCycleAt),
			"proxyLatencyLastSuccessAt":     proxylatencyTime(proxyStatus.LastSuccess),
			"proxyLatencyLastError":         proxyStatus.LastError,
			"proxyLatencyInputs":            proxyStatus.Inputs,
			"proxyLatencyExecuted":          proxyStatus.Executed,
			"proxyLatencyFailures":          proxyStatus.ProxyFailures,
			"proxyLatencySelected":          proxyStatus.Selected,
			"proxyLatencyTarget":            proxyStatus.Target,
			"proxyLatencyClaimed":           proxyStatus.Claimed,
			"proxyLatencyStarted":           proxyStatus.Started,
			"proxyLatencyProcessed":         proxyStatus.Processed,
			"proxyLatencySkippedLeases":     proxyStatus.SkippedLeases,
			"proxyLatencyDeferred":          proxyStatus.Deferred,
			"proxyLatencyExecutionFailures": proxyStatus.ExecutionFailures,
			"proxyLatencyReleaseFailures":   proxyStatus.ReleaseFailures,
			"proxyLatencyPartial":           proxyStatus.Partial,
		}
		if status := workerStatus(); status != nil {
			payload["worker"] = status
		}
		_ = json.NewEncoder(response).Encode(payload)
	})
}

func proxylatencyTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func runRuntimeLegacyMigration(config runtimelog.Config, stdout io.Writer, stderr io.Writer) int {
	store, err := runtimelog.OpenStore(context.Background(), config)
	if err != nil {
		return failWith(stderr, fmt.Errorf("open F1 runtime-log-indexer store: %w", err))
	}
	defer store.Close()
	if err := runtimelog.EnsureSchema(context.Background(), store); err != nil {
		return failWith(stderr, fmt.Errorf("initialize F1 runtime-log-indexer schema: %w", err))
	}
	if err := store.CheckSchema(context.Background()); err != nil {
		return failWith(stderr, fmt.Errorf("verify F1 runtime-log-indexer schema: %w", err))
	}
	ctx, stop := hooksSignalNotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runtimelog.RunWithOwnerLease(ctx, config, store, func(ownerCtx context.Context) error {
		return runtimelog.MigrateLegacySQLite(ownerCtx, config, store)
	}); err != nil {
		return failWith(stderr, err)
	}
	fmt.Fprintln(stdout, "旧运行日志 SQLite 数据迁移和完整性校验完成")
	return 0
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

// sqliteReadOnlyFileDSN 构造只读打开业务/统计 SQLite 的 file URI：路径转
// POSIX 斜杠并补齐根斜杠（Windows 盘符，与 accounthealth sqliteDSN 同款形
// 状）；mode=ro + query_only 双保险只读，busy_timeout=5000 与 gateway 写侧
// 短事务错峰。禁止照抄写方 openSQLite 的 WAL/txlock=immediate。
func sqliteReadOnlyFileDSN(path string) string {
	uriPath := filepath.ToSlash(filepath.Clean(path))
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	return "file:" + uriPath + "?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(5000)"
}

// failWith 保持原 fail() 的错误输出行为（单行错误到 stderr，逐字节一致），
// 返回退出码 1 代替原 os.Exit(1)；run 的各错误出口经它收敛。
func failWith(stderr io.Writer, err error) int {
	fmt.Fprintln(stderr, err)
	return 1
}

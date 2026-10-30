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
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/taskruns"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accountbalance"
	"github.com/huanminabc/juhe-ai/backend-go-platform/ownermode"
	"github.com/huanminabc/juhe-ai/backend-go-platform/processlog"
	"github.com/huanminabc/juhe-ai/backend-go-platform/prommetrics"
	"github.com/huanminabc/juhe-ai/backend-go-platform/schemagate"
	"github.com/huanminabc/juhe-ai/backend-go-platform/supervisor"
	"github.com/huanminabc/juhe-ai/backend-go-platform/version"
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
	printVersion := flags.Bool("version", false, "print the jobs build version, commit and schema revision")
	check := flags.Bool("check-boundary", false, "verify the scaffold boundary")
	runJobsOnce := flags.String("run-jobs-once", "", "run wired worker jobs once (comma-separated task names), print a JSON result array and exit")
	// W33 一次性运维入口：从 runtime_logs 全量重建 F1 facet 三表并退出，
	// 修复 W23 修复前增量写入对同日记录的历史少计。禁止接入定时任务。
	rebuildRuntimeLogFacets := flags.Bool("rebuild-runtime-log-facets", false, "rebuild F1 runtime-log facet tables from runtime_logs once (one-shot maintenance), print a JSON result and exit")
	healthAddress := flags.String("health-listen-address", envOrDefault("JUHE_AI_JOBS_HEALTH_LISTEN_ADDRESS", "127.0.0.1:3305"), "loopback health listen address")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *printVersion {
		fmt.Fprintf(stdout, "juhe-ai-jobs version=%s commit=%s buildTime=%s schemaRevision=%d\n", version.Version, version.Commit, version.BuildTime, contracts.SchemaRevision)
		return 0
	}
	if *check {
		fmt.Fprintln(stdout, "juhe-ai-jobs boundary=ready runtime=worker-owner")
		return 0
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "unsupported jobs arguments: %v\n", flags.Args())
		return 2
	}
	if *rebuildRuntimeLogFacets && *runJobsOnce != "" {
		fmt.Fprintln(stderr, "--rebuild-runtime-log-facets 与 --run-jobs-once 不得同时使用")
		return 2
	}

	// JUHE_AI_LOG_LEVEL (Node log-level.ts): trace..silent, fail fast on an
	// invalid value like the Node startup guard.
	logLevel, err := processlog.LoadLevel(os.Getenv)
	if err != nil {
		return failWith(stderr, err)
	}
	logger := slog.New(slog.NewJSONHandler(stdout, &slog.HandlerOptions{Level: logLevel}))
	// --run-jobs-once 的 stdout 契约是仅输出结果 JSON 数组：该模式下进程
	// logger（含装配期与任务执行期经 worker logger 的全部汇总/告警日志）
	// 重建为写 stderr 的 JSON logger，日志与结果彻底分流；守护路径不受
	// 影响，继续写 stdout。
	if *runJobsOnce != "" {
		logger = slog.New(slog.NewJSONHandler(stderr, &slog.HandlerOptions{Level: logLevel}))
	}
	// 2026-09-28 日志治理：safego 等以 slog.Default() 兜底的组件统一走进程
	// JSON logger。SetDefault 不在此处安装：--run-jobs-once 分支的契约是向
	// stdout 输出单个 JSON 结果，SetDefault 会安装 std log → slog 桥接，任何
	// 后台组件经 std log 的输出都会混入结果流；该分支的全部出口都在下方守护
	// 路径之前提前 return（迁移分支同理），slog.Default() 保持 std 默认
	// （stderr 文本），与改造前 log.Printf 走 stderr 的行为一致——有意保持
	// once 契约。SetDefault 延后到守护路径安装（run-jobs-once 分支之后）。
	processlog.CatchPanic(logger)
	processlog.KeepAliveOnBrokenOutputPipe()
	// --rebuild-runtime-log-facets 分发点：一次性运维操作，先于 owner/passive
	// 形态判定（passive env 下也不允许被静默转成被动健康面）。stdout 契约是
	// 仅输出单个结果 JSON，与 --run-jobs-once 同型。
	if *rebuildRuntimeLogFacets {
		return runRebuildRuntimeLogFacets(os.Getenv, stdout, stderr)
	}
	ownerMode, err := ownermode.Load(os.Getenv)
	if err != nil {
		return failWith(stderr, err)
	}
	if !ownerMode.OwnsWork() {
		return runPassiveJobs(*healthAddress, ownerMode, logger, stdout, stderr)
	}
	// 构建身份首行（升级兼容与迁移账本设计 §6.3）：常驻主路径一次性输出；
	// passive 与 --run-jobs-once 的 stdout 契约不经过此行。
	logger.Info("juhe-ai-jobs starting",
		"version", version.Version,
		"commit", version.Commit,
		"schemaRevision", contracts.SchemaRevision)
	// 升级启动门禁（升级兼容与迁移账本设计 §11.1）：库未被本二进制的
	// upgrade 收敛时 fail-fast；只读检查，绝不自动迁移（§2.2-5）。
	// env 缺失时不拦——交给下方既有配置装载链按原报错顺序 fail-fast
	// （w16h 空环境臂钉死了该顺序），门禁只在主库连接串可用时生效。
	if gateDSN := strings.TrimSpace(os.Getenv("JUHE_AI_POSTGRES_URL")); gateDSN != "" {
		gateDB, gateOpenErr := sql.Open("pgx", gateDSN)
		if gateOpenErr == nil {
			gate := schemagate.Check(context.Background(), gateDB)
			gateDB.Close()
			if !gate.OK {
				return failWith(stderr, errors.New(gate.Message))
			}
		}
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
		return failWith(stderr, errors.New("JUHE_AI_RUNTIME_LOG_ONCE=true is not supported by juhe-ai-jobs"))
	}
	runtimeStore, err := runtimelog.OpenStore(context.Background(), runtimeConfig)
	if err != nil {
		return failWith(stderr, fmt.Errorf("open F1 runtime-log-indexer store: %w", err))
	}
	defer runtimeStore.Close()
	if err := runtimelog.EnsureSchema(context.Background(), runtimeStore); err != nil {
		return failWith(stderr, fmt.Errorf("initialize F1 runtime-log-indexer schema: %w", err))
	}
	if err := runtimeStore.CheckSchema(context.Background()); err != nil {
		return failWith(stderr, fmt.Errorf("verify F1 runtime-log-indexer schema: %w", err))
	}
	accountHealthConfig, err := accounthealth.LoadConfig(os.Getenv)
	if err != nil {
		return failWith(stderr, fmt.Errorf("load J1 account-health config: %w", err))
	}
	var accountHealthStore *accounthealth.Store
	var accountHealthInputPool *pgpool.Handle
	var accountHealthRunner *accounthealth.Runner
	var accountHealthReader accounthealth.DirectInputReader
	// J1 runner 恒装配（机制强制常开，2026-09-19 决策）；J1 outbox drain 的
	// 装配事实由下方 wireHealthProbeOutboxFace + SetProbeRequestDrain 承担。
	// InputSource 已收敛 postgres 单值（D12：sqlite 直读臂与 files 臂随
	// sqlite/files 本地形态物理删除，JUHE_AI_ACCOUNT_HEALTH_INPUT_SOURCE env
	// 退役）。
	accountHealthConfig.Store.PostgresPool, err = postgresPools.Acquire("pgx", accountHealthConfig.Store.PostgresURL, "jobs-store", accountHealthConfig.Store.PostgresMaxOpenConns, accountHealthConfig.Store.PostgresMaxIdleConns)
	// 池获取失败必须显式 fail-fast：不检查会被下方 OpenStore 覆盖 err，
	// 带着 nil pool 进入 J1 装配（对齐 :388 J3a 同型检查）。
	if err != nil {
		return failWith(stderr, fmt.Errorf("open J1 account-health jobs-store PostgreSQL pool: %w", err))
	}
	accountHealthStore, err = accounthealth.OpenStore(accountHealthConfig.Store)
	if err != nil {
		return failWith(stderr, fmt.Errorf("open J1 account-health store: %w", err))
	}
	if err := accountHealthStore.EnsureSchema(context.Background()); err != nil {
		_ = accountHealthStore.Close()
		return failWith(stderr, fmt.Errorf("initialize J1 account-health schema: %w", err))
	}
	accountHealthInputPool, err = postgresPools.Acquire("pgx", accountHealthConfig.BusinessPostgresURL, "business-input", accountHealthConfig.DirectInputPostgresMaxOpenConns, accountHealthConfig.DirectInputPostgresMaxIdleConns)
	if err != nil {
		_ = accountHealthStore.Close()
		return failWith(stderr, fmt.Errorf("open J1 account-health direct-input PostgreSQL pool: %w", err))
	}
	accountHealthInputDB := accountHealthInputPool.DB()
	pingContext, pingCancel := context.WithTimeout(context.Background(), 10*time.Second)
	pingErr := accountHealthInputDB.PingContext(pingContext)
	pingCancel()
	if pingErr != nil {
		_ = accountHealthInputPool.Close()
		_ = accountHealthStore.Close()
		return failWith(stderr, fmt.Errorf("ping J1 account-health direct-input database: %w", pingErr))
	}
	// 死臂已删（w16j 证据：reader secret/TTL 校验与 LoadConfig 完全重叠）
	// 契约：NewPostgresDirectInputReader 仅在 db/secret/TTL 配置无效时
	// 失败，这些条件已由 LoadConfig 与上方装配覆盖，当前路径不可达；但
	// 该构造返回 nil reader + error，吞错会让下方 reader.CheckContract
	// nil-deref。契约上必须显式失败（fail-fast），不允许忽略。
	reader, readerErr := accounthealth.NewPostgresDirectInputReader(accountHealthInputDB, accountHealthConfig.CredentialSecret, accountHealthConfig.InputTTL, accountHealthConfig.Now)
	if readerErr != nil {
		_ = accountHealthInputPool.Close()
		_ = accountHealthStore.Close()
		return failWith(stderr, fmt.Errorf("create J1 account-health direct-input reader: %w", readerErr))
	}
	contractContext, contractCancel := context.WithTimeout(context.Background(), 10*time.Second)
	contractErr := reader.CheckContract(contractContext)
	contractCancel()
	if contractErr != nil {
		_ = accountHealthInputPool.Close()
		_ = accountHealthStore.Close()
		return failWith(stderr, fmt.Errorf("verify J1 account-health direct-input contract: %w", contractErr))
	}
	// 基线播种（BUG-0194）：versions 表只被事件路径惰性创建，迁移/导入
	// 账户无事件即恒空，直读候选 INNER JOIN 筛空导致 J1 无探针可发。
	// 幂等 INSERT...ON CONFLICT DO NOTHING，已播种账户零写入。
	baselineContext, baselineCancel := context.WithTimeout(context.Background(), 30*time.Second)
	seeded, baselineErr := accounthealth.EnsurePostgresDirectInputBaseline(baselineContext, accountHealthInputDB)
	baselineCancel()
	if baselineErr != nil {
		_ = accountHealthInputPool.Close()
		_ = accountHealthStore.Close()
		return failWith(stderr, fmt.Errorf("seed J1 account-health direct-input baseline: %w", baselineErr))
	}
	logger.Info("J1 直读输入基线播种完成", "seeded", seeded)
	accountHealthReader = reader
	accountHealthRunner = accounthealth.NewRunnerWithDirectInputReader(accountHealthConfig, accountHealthStore, logger, reader)
	modelRecoveryConfig, err := keymodelrecovery.LoadRedisConfig(os.Getenv)
	if err != nil {
		return failWith(stderr, fmt.Errorf("load model-recovery config: %w", err))
	}
	var modelRecoveryStore *keymodelrecovery.RedisStore
	var modelRecoveryRunner *keymodelrecovery.Runner
	if modelRecoveryConfig.Enabled {
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
		if err != nil {
			return failWith(stderr, fmt.Errorf("open J3a proxy-latency jobs-store PostgreSQL pool: %w", err))
		}
		j3Store, err = proxylatency.OpenStore(j3Config.Store)
		// OpenStore 在 pool 前置注入后仍校验 mode/limits，可返回 (nil, err)；
		// 吞错会让 nil store 进入下方 CheckSchema（nil-deref），必须 fail-fast。
		if err != nil {
			return failWith(stderr, fmt.Errorf("open J3a proxy-latency store: %w", err))
		}
		if err := j3Store.CheckSchema(context.Background()); err != nil {
			_ = j3Store.Close()
			return failWith(stderr, fmt.Errorf("verify pre-provisioned J3a proxy-latency jobs schema: %w", err))
		}
		j3InputPool, err = postgresPools.Acquire("pgx", j3Config.BusinessPostgresURL, "business-input", j3Config.InputPostgresMaxOpenConns, j3Config.InputPostgresMaxIdleConns)
		if err != nil {
			_ = j3Store.Close()
			return failWith(stderr, fmt.Errorf("open J3a proxy-latency direct-input PostgreSQL pool: %w", err))
		}
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
		if err != nil {
			_ = j3InputPool.Close()
			_ = j3Store.Close()
			return failWith(stderr, fmt.Errorf("open J3a proxy-latency business-result PostgreSQL pool: %w", err))
		}
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
	// 旧名兼容路径披露：只配 JUHE_AI_USAGE_SPOOL_DIR 时 drain 仍与 gateway
	// 写侧同目录交接，但该名已废弃，提示部署改配新名。
	if workerCfg.usageSpoolDirectoryLegacyName {
		logger.Warn("usage spool 目录来自旧名 JUHE_AI_USAGE_SPOOL_DIR 回退（兼容路径，建议改配 JUHE_AI_USAGE_SPOOL_DIRECTORY）",
			"event", "usage_spool_directory_legacy_fallback", "directory", workerCfg.UsageSpoolDirectory)
	}
	// accounts.capability_domain 启动只读校验（AI 账户能力域拆分 M1/M2）：
	// 加列/迁移归 maintenance，jobs 启动仅校验；形状不符或阶段记录未封存
	// m2_finalized 即 fail-fast（部署顺序契约：先迁移后启动新版）。一次性
	// 只读连接，不 ALTER。
	if err := checkAccountsCapabilityDomainSchemaAtStartup(context.Background(), workerCfg); err != nil {
		return failWith(stderr, fmt.Errorf("verify accounts capability_domain schema: %w", err))
	}
	// scheduling_preferences 启动只读校验（调度偏好独立实体 P1）：表形状 /
	// api_keys 绑定列 / 内置行由 maintenance --ensure-schema（ensure 臂补种
	// 内置行）保证，缺失或不符即 fail-fast（部署顺序契约：先 ensure-schema
	// 后启动新版）。一次性只读连接，不 ALTER、不补种。
	if err := checkSchedulingPreferenceSchemaAtStartup(context.Background(), workerCfg); err != nil {
		return failWith(stderr, fmt.Errorf("verify scheduling preference schema: %w", err))
	}
	worker, err := buildWorkerAssembly(workerCfg, logger)
	if err != nil {
		return failWith(stderr, fmt.Errorf("assemble jobs worker: %w", err))
	}
	// --run-jobs-once 与后续任何 error 提前返回都不进 supervisor（组件 Close
	// 链不会执行）：装配成功后立即登记 closeStores。defer LIFO 使家族 store
	// 在 listener/F2/F1 store 与 PG 池的既有 defer 之前关闭；closeStores 幂
	// 等，常驻路径由 supervisor Close 首调、本 defer 二次空转。
	defer worker.closeStores()
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
		// 探针观测补记使用记录（BUG-0194 方案 B）：usagewriter 已在装配内就绪，
		// runner 回绑后探针流量进入使用记录页与全部用量统计（Node 语义对齐）。
		// 装配失败降级 warn（探针观测仅停留 juhe_jobs 审计面），不阻塞启动。
		if accountHealthRunner != nil {
			probeUsage, probeUsageErr := worker.wireProbeUsageRecorder()
			if probeUsageErr != nil {
				logger.Warn("探针使用记录写入面装配失败；探针观测不进入使用记录",
					"event", "account_health_probe_usage_assembly_failed", "error", probeUsageErr.Error())
			} else {
				accountHealthRunner.SetProbeUsageRecorder(probeUsage)
			}
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
			// BUG-0260：runner 的终态脑裂自愈（cooldown_terminal_reproject）复用
			// 同一投影器直调落投影；投影面缺席时不注入，触发时以错误显式暴露
			// 而非回退为复测探针死循环。
			if accountHealthRunner != nil && projector != nil {
				accountHealthRunner.SetOutcomeProjector(projector)
			}
		}
	}

	// --run-jobs-once 分发点：worker 装配（wireFamilies、wiredTasks）完成后、
	// health server 创建/监听之前逐个执行请求任务并退出，不启动任何常驻面。
	if *runJobsOnce != "" {
		return runJobsOnceExit(worker, *runJobsOnce, stdout, stderr)
	}

	// 守护路径（唯一到达点：--once / --run-jobs-once / 迁移 / passive 等全部
	// 提前 return 分支的出口都在上方）：此处安装 std log → slog 桥接的默认
	// logger，safego 等以 slog.Default() 兜底的组件统一走进程 JSON logger。
	// once 语义分支不经过本行，其 stdout 单 JSON 结果契约不受后台日志污染。
	slog.SetDefault(logger)

	listener, err := listenLoopback(*healthAddress)
	if err != nil {
		return failWith(stderr, fmt.Errorf("listen jobs health endpoint %q: %w", *healthAddress, err))
	}
	defer listener.Close()
	// Prometheus 指标面（唯一注册表）：Go + process 收集器随 NewRegistry
	// 注册，taskruns runner 埋点与 /__aisys__/metrics 暴露共用同一 registry。
	promRegistry := prommetrics.NewRegistry()
	taskruns.BindMetrics(promRegistry)
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
	}
	if probeOutboxPruner != nil {
		// 业务库句柄的关闭由 worker 组件 Close（closeStores）承担；
		// supervisor 保证所有组件 Run 停止后才调用 Close。
		components = append(components, supervisor.Component{
			Name: "account-health probe-outbox prune",
			Run:  probeOutboxPruner.Run,
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
	healthServer := &http.Server{
		Handler: jobsHTTPHandler(jobsHTTPDeps{
			OwnerMode:                  ownerMode,
			RuntimeRunning:             &runtimeRunning,
			MetricsHandler:             promRegistry.Handler(),
			AccountHealthEnabled:       true,
			AccountHealthReady:         accountHealthReady,
			AccountBalanceEnabled:      accountBalanceConfig.Enabled,
			AccountBalanceReady:        accountBalanceReady,
			AccountBalanceService:      accountBalanceService,
			AccountBalanceManualSecret: accountBalanceConfig.ManualHTTPSecret,
			ProxyLatencyEnabled:        j3Config.Enabled,
			ProxyLatencyReady:          j3Ready,
			ProxyLatencyStatus: func() proxylatency.RunnerStatus {
				if j3Runner == nil {
					return proxylatency.RunnerStatus{}
				}
				return j3Runner.Status()
			},
			ProxyLatencySnapshot: func() (proxylatency.RunnerStatus, bool) {
				if j3Runner == nil {
					return proxylatency.RunnerStatus{}, true
				}
				return j3Runner.Snapshot()
			},
			WorkerReady:  workerReady,
			WorkerStatus: workerStatus,
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	ctx, stop := hooksSignalNotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serveErr := make(chan error, 1)
	go func() { serveErr <- healthServer.Serve(listener) }()
	logger.Info("juhe-ai-jobs started", "healthAddress", listener.Addr().String(), "accountHealthEnabled", true, "modelRecoveryEnabled", modelRecoveryConfig.Enabled, "accountBalanceEnabled", accountBalanceConfig.Enabled, "proxyLatencyEnabled", j3Config.Enabled, "workerEnabled", true, "workerWiredJobs", worker.wiredJobs)
	components = append(components, worker.components()...)
	runErr := supervisor.Run(ctx, components, logger)
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	shutdownErr := healthServer.Shutdown(shutdownCtx)
	shutdownCancel()
	serveResult := <-serveErr
	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		return failWith(stderr, fmt.Errorf("jobs supervisor component stopped: %w", runErr))
	}
	if shutdownErr != nil {
		return failWith(stderr, fmt.Errorf("shutdown jobs health endpoint: %w", shutdownErr))
	}
	if serveResult != nil && !errors.Is(serveResult, http.ErrServerClosed) {
		return failWith(stderr, fmt.Errorf("jobs health endpoint stopped: %w", serveResult))
	}
	return 0
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
	if err := validateHealthListenAddress(address); err != nil {
		return nil, err
	}
	return net.Listen("tcp", address)
}

// validateHealthListenAddress 校验健康监听地址（BUG-0226：原校验强制
// loopback，容器部署下 gateway 无法跨容器抓取，系统指标页 jobs 段恒降级
// "不可达"）。语义：默认值仍是 127.0.0.1（loopback）；显式配置允许空 host
// （全接口）、0.0.0.0/[::] 与任意具体 IP（compose 内网监听供 gateway 用
// 容器名抓取）。安全边界由部署侧承担：该端口不得映射公网（single-server
// compose 无 ports 映射）。主机名字符串（如容器名）仍拒绝——监听地址
// 必须是 IP 或通配。
func validateHealthListenAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid health listen address %q: %w", address, err)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return fmt.Errorf("invalid health listen address %q: port must be between 1 and 65535", address)
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	if host == "" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("invalid health listen address %q: host must be localhost, an IP or empty (BUG-0226: 容器部署配 0.0.0.0)", address)
	}
	return nil
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
			"accountHealthEnabled":  false,
			"accountHealthReady":    false,
			"accountBalanceEnabled": false,
			"accountBalanceReady":   false,
			"proxyLatencyEnabled":   false,
			"proxyLatencyReady":     false,
		})
	})
}

// jobsHTTPDeps 是 jobsHTTPHandler 的显式依赖集。原 variadic any 槽位布局
// （j3[4]/j3[5] 曾是 gometrics Collector/Sampler 占位）已在表监控与
// go-runtime-metrics 采样器下线时消除：readiness 载荷的每个开关/探针都是
// 具名字段，不存在对象漂移进 readiness 参数的通道（X05 缺陷根因）。
type jobsHTTPDeps struct {
	OwnerMode      ownermode.Mode
	RuntimeRunning *atomic.Bool
	// MetricsHandler 是 /__aisys__/metrics 的暴露 handler（prommetrics）。
	MetricsHandler             http.Handler
	AccountHealthEnabled       bool
	AccountHealthReady         func() bool
	AccountBalanceEnabled      bool
	AccountBalanceReady        func() bool
	AccountBalanceService      *accountbalance.Service
	AccountBalanceManualSecret string
	ProxyLatencyEnabled        bool
	ProxyLatencyReady          func() bool
	ProxyLatencyStatus         func() proxylatency.RunnerStatus
	ProxyLatencySnapshot       func() (proxylatency.RunnerStatus, bool)
	// worker 机制强制常开（2026-09-19 决策），无 Enabled 开关。
	WorkerReady  func() bool
	WorkerStatus func() map[string]any
}

// metricsHandlerOrDefault 返回注入的 /__aisys__/metrics handler；未注入时
// （最小 deps 的进程内单测）回退到独立默认 registry，端点仍暴露标准
// go_*/process_* 族而不是让 ServeMux 因 nil handler panic。生产组合根显式
// 注入已 BindMetrics 的进程 registry，任务指标族只在组合根路径出现。
func metricsHandlerOrDefault(handler http.Handler) http.Handler {
	if handler != nil {
		return handler
	}
	return prommetrics.NewRegistry().Handler()
}

func jobsHTTPHandler(deps jobsHTTPDeps) http.Handler {
	mux := http.NewServeMux()
	// 健康监听只剩 /health 与 /__aisys__/metrics（健康探测属编排语义，
	// gateway 蓝绿 readiness 探测与 Prometheus 抓取仍依赖，保留）。
	// 去跨进程战役第四刀：/account-balance/manual 手动桥已删除（Node 时代
	// 的手动触发入口，全仓无生产调用方；J2 余额刷新走周期调度与恢复扫描）。
	// 去跨进程战役第二刀：/__aiinternal__ 路由整体消失（原账户测试派发与
	// 账户健康检查派发 handler 已删除；健康检查派发改走 DB outbox 通道，
	// 见 worker_health_probe_outbox.go）。
	mux.Handle("/__aisys__/metrics", metricsHandlerOrDefault(deps.MetricsHandler))
	mux.Handle("/health", healthHandler(healthDeps{
		OwnerMode:             deps.OwnerMode,
		RuntimeRunning:        deps.RuntimeRunning,
		AccountHealthEnabled:  deps.AccountHealthEnabled,
		AccountHealthReady:    deps.AccountHealthReady,
		AccountBalanceEnabled: deps.AccountBalanceEnabled,
		AccountBalanceReady:   deps.AccountBalanceReady,
		ProxyLatencyEnabled:   deps.ProxyLatencyEnabled,
		ProxyLatencyReady:     deps.ProxyLatencyReady,
		ProxyLatencyStatus:    deps.ProxyLatencyStatus,
		ProxyLatencySnapshot:  deps.ProxyLatencySnapshot,
		WorkerReady:           deps.WorkerReady,
		WorkerStatus:          deps.WorkerStatus,
	}))
	mux.HandleFunc("/account-balance/manual", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || deps.AccountBalanceService == nil {
			http.NotFound(response, request)
			return
		}
		if !matchesAccountBalanceManualSecret(request, deps.AccountBalanceManualSecret) {
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
		record, _, err := deps.AccountBalanceService.RunManual(request.Context(), envelope.Input)
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

// healthDeps 是 healthHandler 的显式依赖集（worker 机制强制常开，payload
// 的 workerEnabled 恒为 true）。
type healthDeps struct {
	OwnerMode             ownermode.Mode
	RuntimeRunning        *atomic.Bool
	AccountHealthEnabled  bool
	AccountHealthReady    func() bool
	AccountBalanceEnabled bool
	AccountBalanceReady   func() bool
	ProxyLatencyEnabled   bool
	ProxyLatencyReady     func() bool
	ProxyLatencyStatus    func() proxylatency.RunnerStatus
	ProxyLatencySnapshot  func() (proxylatency.RunnerStatus, bool)
	WorkerReady           func() bool
	WorkerStatus          func() map[string]any
}

func healthHandler(deps healthDeps) http.Handler {
	accountHealthReady := deps.AccountHealthReady
	if accountHealthReady == nil {
		accountHealthReady = func() bool { return true }
	}
	accountBalanceReady := deps.AccountBalanceReady
	if accountBalanceReady == nil {
		accountBalanceReady = func() bool { return true }
	}
	proxyLatencyReady := deps.ProxyLatencyReady
	if proxyLatencyReady == nil {
		proxyLatencyReady = func() bool { return true }
	}
	proxyLatencyStatus := deps.ProxyLatencyStatus
	if proxyLatencyStatus == nil {
		proxyLatencyStatus = func() proxylatency.RunnerStatus { return proxylatency.RunnerStatus{} }
	}
	proxyLatencySnapshot := deps.ProxyLatencySnapshot
	if proxyLatencySnapshot == nil {
		proxyLatencySnapshot = func() (proxylatency.RunnerStatus, bool) {
			return proxyLatencyStatus(), proxyLatencyReady()
		}
	}
	workerReady := deps.WorkerReady
	if workerReady == nil {
		workerReady = func() bool { return true }
	}
	workerStatus := deps.WorkerStatus
	if workerStatus == nil {
		workerStatus = func() map[string]any { return nil }
	}
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/health" {
			http.NotFound(response, request)
			return
		}
		runtimeLogOwnerHeld := deps.RuntimeRunning.Load()
		accountHealthIsReady := !deps.AccountHealthEnabled || accountHealthReady()
		accountBalanceIsReady := !deps.AccountBalanceEnabled || accountBalanceReady()
		proxyStatus, proxyReady := proxyLatencySnapshot()
		proxyLatencyIsReady := !deps.ProxyLatencyEnabled || proxyReady
		workerIsReady := workerReady()
		response.Header().Set("Content-Type", "application/json")
		ready := runtimeLogOwnerHeld && accountHealthIsReady && accountBalanceIsReady && proxyLatencyIsReady && workerIsReady
		payload := map[string]any{
			"ready":      ready,
			"ownerReady": ready,
			"ownerMode":  deps.OwnerMode,
			// 构建身份（升级兼容与迁移账本设计 §6.3）：纯加法键，编排探测只读 ready。
			"version":                       version.Version,
			"commit":                        version.Commit,
			"schemaRevision":                contracts.SchemaRevision,
			"runtimeLogOwnerHeld":           runtimeLogOwnerHeld,
			"accountHealthEnabled":          deps.AccountHealthEnabled,
			"accountHealthReady":            accountHealthIsReady,
			"accountBalanceEnabled":         deps.AccountBalanceEnabled,
			"accountBalanceReady":           accountBalanceIsReady,
			"proxyLatencyEnabled":           deps.ProxyLatencyEnabled,
			"proxyLatencyReady":             proxyLatencyIsReady,
			"workerEnabled":                 true,
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

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

// failWith 保持原 fail() 的错误输出行为（单行错误到 stderr，逐字节一致），
// 返回退出码 1 代替原 os.Exit(1)；run 的各错误出口经它收敛。
func failWith(stderr io.Writer, err error) int {
	fmt.Fprintln(stderr, err)
	return 1
}

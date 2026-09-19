package main

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/datadir"
)

// workerConfig 是 jobs 组合根 worker 侧的 env 约定，与 Node worker 进程
// （backend/src/worker.ts + config/runtime.ts）同名 env 对齐：
//   - JUHE_AI_DATABASE_DRIVER：sqlite（默认）| postgres；
//   - JUHE_AI_DATABASE_PATH / JUHE_AI_STATS_DATABASE_PATH：SQLite 双库；
//   - JUHE_AI_POSTGRES_URL：PostgreSQL 连接（performance 模式）；
//   - JUHE_AI_INSTANCE_ID / JUHE_AI_WORKER_ROLE / JUHE_AI_WORKER_REPLICA_INDEX：
//     调度器 stable seed 与租约 owner 前缀；
//   - JUHE_AI_SECRET：凭据封套密钥（oauthrefresh / internalapi 派发签名）；
//   - JUHE_AI_USAGE_CATALOG_DATABASE_PATH / JUHE_AI_USAGE_SHARD_ROOT /
//     JUHE_AI_USAGE_SHARD_COUNT：usagewriter 分片写入。
//
// jobs 专属 env：
//   - JUHE_AI_DATA_DIR（2026-09-19 零配置决策）：数据根目录，缺省 ./data
//     （相对进程 cwd，TrimSpace 后为空也视为未配置）。上列「路径类」env
//     未配置时派生为 <DATA_DIR>/<固定名>（internal/datadir），显式配置始终
//     优先；固定名与 gateway 侧一致，两进程靠相同 DATA_DIR 共享同一业务库。
//   - JUHE_AI_JOBS_WORKER_ENABLED 与 JUHE_AI_JOBS_<FAMILY>_ENABLED（stats/
//     oauth/task_runs/usage_writer/balance_detect/retention/probe）已废弃
//     （2026-09-19 决策）：worker 调度器与全部任务族强制常开，这些变量不再
//     被读取（总开关非 true 值仍在启动早期输出废弃告警）；usage spool 消费、
//     用量记录与统计预聚合/额度快照由常开的 worker 任务族承载；
//   - JUHE_AI_TASK_RUNS_DATABASE_PATH / JUHE_AI_TASK_RUNS_POSTGRES_URL：
//     background_task_runs + background_job_leases 双模存储；
//   - JUHE_AI_JOBS_DRAIN_TIMEOUT_MS：停机排空上限（默认 10s，对齐 Node
//     stopBackgroundJobs(10_000)）。
type workerConfig struct {
	Driver string // sqlite | postgres

	InstanceID       string
	WorkerRole       string
	WorkerReplicaIdx int

	Secret string

	BusinessSQLitePath string
	StatsSQLitePath    string
	TaskRunsSQLitePath string

	PostgresURL          string
	PostgresMaxOpenConns int
	PostgresMaxIdleConns int

	UsageCatalogSQLitePath string
	UsageShardRoot         string
	UsageShardCount        int
	// UsageSpoolDirectory 是 gateway usage-record 文件 spool 交接表的根目录
	// （BUG-0175 D-72 消费侧）：env JUHE_AI_USAGE_SPOOL_DIRECTORY，未配置时按
	// gateway 组合根同规则从 JUHE_AI_STATS_DATABASE_PATH 目录派生
	// <目录>/usage-record-spool（两侧必须同源，drain 才能读到 gateway 写出的
	// 交接文件；PG 模式无 stats 文件路径，须显式配置 env）。
	UsageSpoolDirectory string

	DatasetSQLitePath              string
	ChatSQLitePath                 string
	CodexContextStateShardRoot     string
	CodexContextStateShardCount    int
	ChatAssetsRoot                 string
	CodexContextRoot               string
	ChatRetentionDays              int
	RecordMaintenanceQueueMaxItems int
	RecordMaintenanceQueueMaxMb    int

	// RecordMaintenanceBatchSize / RecordMaintenanceShutdownFlushMaxBatches
	// 是 record_maintenance_jobs 交接表 drain 的批次与停机排空批数
	// （Node background.recordMaintenanceBatchSize / recordMaintenanceShutdownFlushMaxBatches
	// 同名 env 与默认值）。
	RecordMaintenanceBatchSize               int
	RecordMaintenanceShutdownFlushMaxBatches int

	// ProbeConcurrency 限制探针族在途上游诊断请求与队列并发。Node 侧对应
	// globalSharedQueueConcurrency 取 runtimeConfig.concurrency.globalMax
	// （runtime.ts:410，JUHE_AI_CONCURRENCY_GLOBAL_MAX 默认 5000）。Go 默认
	// 512 不是 Node 5000 的直译，而是沿用 jobs 内 J1/J2 家族既有档位
	// （accounthealth/config.go 的 default*Concurrency=512、
	// accountbalance/runtime_config.go 的 defaultAccountBalanceConcurrency=512，
	// 两家族 env 上限均为 5096）；本项 env JUHE_AI_JOBS_PROBE_CONCURRENCY
	// 同样可上调到 5096（下方校验），也可下调。手动账号测试队列并发沿用
	// 同一约定（该队列已随去跨进程战役移交 gateway 装配，gateway 侧读取
	// 同名 env 与默认值）。
	ProbeConcurrency int

	// 账户列表可用性投影维护（Node runtimeConfig.background
	// accountListAvailabilityProjection* 同名 env、默认值与边界）：
	//   - ListProjectionEnabled 默认 false（Node 默认 false，不启用不注册）；
	//   - ListProjectionIntervalMS env 1000..60000 默认 1000；
	//   - ListProjectionBatchSize 1..100 默认 100；
	//   - ListProjectionMaxBatchesPerRun 1..400 默认 200；
	//   - ListProjectionWorkerConcurrency 1..8 默认 4（仅 PG 生效，与 Node 一致）。
	ListProjectionEnabled           bool
	ListProjectionIntervalMS        int
	ListProjectionBatchSize         int
	ListProjectionMaxBatchesPerRun  int
	ListProjectionWorkerConcurrency int

	// RedisStateURL / RedisNamespace 供速度优先恢复探针读写降级运行态
	// （与 Node 网关同一键空间）。
	RedisStateURL  string
	RedisNamespace string

	// circuitCapacityMS 是账户电路运行态容量（JUHE_AI_GATEWAY_ACCOUNT_CIRCUIT_CAPACITY，
	// 默认 50000 与 Node 一致；与网关不一致会导致容量/驱逐判定分歧）。
	circuitCapacity int64

	DrainTimeout time.Duration
}

func workerEnvBool(getenv func(string) string, name string, fallback bool) (bool, error) {
	value := strings.TrimSpace(getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s 必须是布尔值", name)
	}
	return parsed, nil
}

func workerEnvInt(getenv func(string) string, name string, fallback int) (int, error) {
	value := strings.TrimSpace(getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s 必须是整数", name)
	}
	return parsed, nil
}

// CircuitCapacity 返回账户电路运行态容量。
func (c workerConfig) CircuitCapacity() int64 {
	if c.circuitCapacity < 1 {
		return 50_000
	}
	return c.circuitCapacity
}

func loadWorkerConfig(getenv func(string) string) (workerConfig, error) {
	config := workerConfig{
		Driver:               "sqlite",
		InstanceID:           "juhe-ai-jobs",
		WorkerRole:           "worker",
		WorkerReplicaIdx:     0,
		PostgresMaxOpenConns: 50,
		PostgresMaxIdleConns: 50,
		UsageShardCount:      16,
		// CodexContextStateShardCount 对齐 Node runtime.ts:694
		// （JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT 默认 16，1..256）与 gateway
		// 组合根 runtime.go 的同款默认：codex context 状态写入按 key 哈希路由到
		// 每分片独立 SQLite 文件（WAL），写并发上限等于分片数，不是单写者串行
		// 设计；原默认 4 会把写入吞吐压到 1/4，且与 gateway 默认 16 不一致会让
		// retention 清理漏掉 gateway 写出的分片文件。
		CodexContextStateShardCount:              16,
		ChatRetentionDays:                        3,
		RecordMaintenanceQueueMaxItems:           5000,
		RecordMaintenanceQueueMaxMb:              32,
		RecordMaintenanceBatchSize:               10,
		RecordMaintenanceShutdownFlushMaxBatches: 1,
		// 默认 512：jobs 内 J1/J2 家族既有档位（非 Node globalMax 5000 直译，
		// 见 ProbeConcurrency 字段注释）。
		ProbeConcurrency:                512,
		ListProjectionIntervalMS:        1_000,
		ListProjectionBatchSize:         100,
		ListProjectionMaxBatchesPerRun:  200,
		ListProjectionWorkerConcurrency: 4,
		DrainTimeout:                    10 * time.Second,
	}
	var err error
	if value := strings.TrimSpace(getenv("JUHE_AI_DATABASE_DRIVER")); value != "" {
		config.Driver = strings.ToLower(value)
	}
	if config.Driver != "sqlite" && config.Driver != "postgres" {
		return config, fmt.Errorf("JUHE_AI_DATABASE_DRIVER 必须为 sqlite 或 postgres")
	}
	if value := strings.TrimSpace(getenv("JUHE_AI_INSTANCE_ID")); value != "" {
		config.InstanceID = value
	}
	if value := strings.TrimSpace(getenv("JUHE_AI_WORKER_ROLE")); value != "" {
		config.WorkerRole = value
	}
	config.WorkerReplicaIdx, err = workerEnvInt(getenv, "JUHE_AI_WORKER_REPLICA_INDEX", 0)
	if err != nil {
		return config, err
	}
	if config.WorkerReplicaIdx < 0 || config.WorkerReplicaIdx > 63 {
		return config, fmt.Errorf("JUHE_AI_WORKER_REPLICA_INDEX 必须介于 0 和 63 之间")
	}
	config.Secret = strings.TrimSpace(getenv("JUHE_AI_SECRET"))
	// 非生产空 SECRET 回退与 gateway runtime.go defaultRuntimeSecret 同值的开发
	// 密钥（两侧凭据封套互操作要求同值；gateway 对生产强制 ≥32 位真实密钥）。
	if config.Secret == "" {
		if strings.EqualFold(strings.TrimSpace(getenv("NODE_ENV")), "production") {
			return config, fmt.Errorf("production 模式必须配置 JUHE_AI_SECRET（凭据封套密钥）")
		}
		config.Secret = "juhe-ai-dev-secret-change-me"
	}
	// 路径类 env 按 DATA_DIR 约定派生（internal/datadir）：显式配置优先，
	// 未配置落 <DATA_DIR>/<固定名>；固定名与 gateway 侧同名 env 一致。
	config.BusinessSQLitePath = datadir.Path(getenv, "JUHE_AI_DATABASE_PATH", "business.sqlite3")
	config.StatsSQLitePath = datadir.Path(getenv, "JUHE_AI_STATS_DATABASE_PATH", "stats.sqlite3")
	config.TaskRunsSQLitePath = datadir.Path(getenv, "JUHE_AI_TASK_RUNS_DATABASE_PATH", "task-runs.sqlite3")
	config.PostgresURL = strings.TrimSpace(getenv("JUHE_AI_POSTGRES_URL"))
	config.PostgresMaxOpenConns, err = workerEnvInt(getenv, "JUHE_AI_POSTGRES_MAX_OPEN_CONNS", config.PostgresMaxOpenConns)
	if err != nil {
		return config, err
	}
	config.PostgresMaxIdleConns, err = workerEnvInt(getenv, "JUHE_AI_POSTGRES_MAX_IDLE_CONNS", config.PostgresMaxIdleConns)
	if err != nil {
		return config, err
	}
	config.UsageCatalogSQLitePath = datadir.Path(getenv, "JUHE_AI_USAGE_CATALOG_DATABASE_PATH", "usage-catalog.sqlite3")
	config.UsageShardRoot = datadir.Path(getenv, "JUHE_AI_USAGE_SHARD_ROOT", "usage-shards")
	// usage spool 交接表目录：与 gateway 组合根（compose.go spoolDirectory）
	// 同名 env、同派生规则；sqlite 模式从 stats 库目录派生，PG 模式保持为空
	// （drain 未接线并告警），部署须显式配置 JUHE_AI_USAGE_SPOOL_DIRECTORY。
	config.UsageSpoolDirectory = strings.TrimSpace(getenv("JUHE_AI_USAGE_SPOOL_DIRECTORY"))
	if config.UsageSpoolDirectory == "" && config.StatsSQLitePath != "" {
		config.UsageSpoolDirectory = filepath.Join(filepath.Dir(config.StatsSQLitePath), "usage-record-spool")
	}
	config.UsageShardCount, err = workerEnvInt(getenv, "JUHE_AI_USAGE_SHARD_COUNT", config.UsageShardCount)
	if err != nil {
		return config, err
	}
	config.DatasetSQLitePath = datadir.Path(getenv, "JUHE_AI_DATASET_DATABASE_PATH", "dataset.sqlite3")
	config.ChatSQLitePath = datadir.Path(getenv, "JUHE_AI_CHAT_DATABASE_PATH", "chat.sqlite3")
	config.CodexContextStateShardRoot = datadir.Path(getenv, "JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT", "codex-context/state-shards")
	config.CodexContextStateShardCount, err = workerEnvInt(getenv, "JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT", config.CodexContextStateShardCount)
	if err != nil {
		return config, err
	}
	config.ChatAssetsRoot = datadir.Path(getenv, "JUHE_AI_CHAT_ASSETS_ROOT", "chat-assets")
	config.CodexContextRoot = strings.TrimSpace(getenv("JUHE_AI_CODEX_CONTEXT_ROOT"))
	config.ChatRetentionDays, err = workerEnvInt(getenv, "JUHE_AI_CHAT_RETENTION_DAYS", config.ChatRetentionDays)
	if err != nil {
		return config, err
	}
	if config.ChatRetentionDays < 1 || config.ChatRetentionDays > 365 {
		return config, fmt.Errorf("JUHE_AI_CHAT_RETENTION_DAYS 必须在 1 到 365 之间的整数")
	}
	config.RecordMaintenanceQueueMaxItems, err = workerEnvInt(getenv, "JUHE_AI_BACKGROUND_RECORD_MAINTENANCE_QUEUE_MAX_ITEMS", config.RecordMaintenanceQueueMaxItems)
	if err != nil {
		return config, err
	}
	config.RecordMaintenanceQueueMaxMb, err = workerEnvInt(getenv, "JUHE_AI_BACKGROUND_RECORD_MAINTENANCE_QUEUE_MAX_MB", config.RecordMaintenanceQueueMaxMb)
	if err != nil {
		return config, err
	}
	config.RecordMaintenanceBatchSize, err = workerEnvInt(getenv, "JUHE_AI_BACKGROUND_RECORD_MAINTENANCE_BATCH_SIZE", config.RecordMaintenanceBatchSize)
	if err != nil {
		return config, err
	}
	if config.RecordMaintenanceBatchSize < 1 || config.RecordMaintenanceBatchSize > 10_000 {
		return config, fmt.Errorf("JUHE_AI_BACKGROUND_RECORD_MAINTENANCE_BATCH_SIZE 必须介于 1 和 10000 之间")
	}
	config.RecordMaintenanceShutdownFlushMaxBatches, err = workerEnvInt(getenv, "JUHE_AI_BACKGROUND_RECORD_MAINTENANCE_SHUTDOWN_FLUSH_MAX_BATCHES", config.RecordMaintenanceShutdownFlushMaxBatches)
	if err != nil {
		return config, err
	}
	if config.RecordMaintenanceShutdownFlushMaxBatches < 1 || config.RecordMaintenanceShutdownFlushMaxBatches > 10_000 {
		return config, fmt.Errorf("JUHE_AI_BACKGROUND_RECORD_MAINTENANCE_SHUTDOWN_FLUSH_MAX_BATCHES 必须介于 1 和 10000 之间")
	}
	// 家族级开关（JUHE_AI_JOBS_<FAMILY>_ENABLED）已随 2026-09-19 零配置决策
	// 删除：全部任务族强制常开，该循环不再读取任何家族开关变量。
	config.ProbeConcurrency, err = workerEnvInt(getenv, "JUHE_AI_JOBS_PROBE_CONCURRENCY", config.ProbeConcurrency)
	if err != nil {
		return config, err
	}
	// 上限 5096 与 J1/J2 家族既有档位一致（accounthealth maxJ1Concurrency /
	// accountbalance maxAccountBalanceWorkItems）；Node globalMax 5000 只是
	// 参照基线，不是这里的硬上限。
	if config.ProbeConcurrency < 1 || config.ProbeConcurrency > 5096 {
		return config, fmt.Errorf("JUHE_AI_JOBS_PROBE_CONCURRENCY 必须介于 1 和 5096 之间")
	}
	// 手动账号测试队列 env（JUHE_AI_BACKGROUND_ACCOUNT_TEST_*）已随队列
	// 执行权移交 gateway 组合根，jobs 不再读取。
	config.ListProjectionEnabled, err = workerEnvBool(getenv, "JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_ENABLED", false)
	if err != nil {
		return config, err
	}
	config.ListProjectionIntervalMS, err = workerEnvInt(getenv, "JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_INTERVAL_MS", config.ListProjectionIntervalMS)
	if err != nil {
		return config, err
	}
	if config.ListProjectionIntervalMS < 1_000 || config.ListProjectionIntervalMS > 60_000 {
		return config, fmt.Errorf("JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_INTERVAL_MS 必须介于 1000 和 60000 之间")
	}
	config.ListProjectionBatchSize, err = workerEnvInt(getenv, "JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_BATCH_SIZE", config.ListProjectionBatchSize)
	if err != nil {
		return config, err
	}
	if config.ListProjectionBatchSize < 1 || config.ListProjectionBatchSize > 100 {
		return config, fmt.Errorf("JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_BATCH_SIZE 必须介于 1 和 100 之间")
	}
	config.ListProjectionMaxBatchesPerRun, err = workerEnvInt(getenv, "JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_MAX_BATCHES_PER_RUN", config.ListProjectionMaxBatchesPerRun)
	if err != nil {
		return config, err
	}
	if config.ListProjectionMaxBatchesPerRun < 1 || config.ListProjectionMaxBatchesPerRun > 400 {
		return config, fmt.Errorf("JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_MAX_BATCHES_PER_RUN 必须介于 1 和 400 之间")
	}
	config.ListProjectionWorkerConcurrency, err = workerEnvInt(getenv, "JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_WORKER_CONCURRENCY", config.ListProjectionWorkerConcurrency)
	if err != nil {
		return config, err
	}
	if config.ListProjectionWorkerConcurrency < 1 || config.ListProjectionWorkerConcurrency > 8 {
		return config, fmt.Errorf("JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_WORKER_CONCURRENCY 必须介于 1 和 8 之间")
	}
	config.RedisStateURL = strings.TrimSpace(getenv("JUHE_AI_REDIS_STATE_URL"))
	config.RedisNamespace = strings.TrimSpace(getenv("JUHE_AI_REDIS_NAMESPACE"))
	capacity := int64(50_000)
	if value := strings.TrimSpace(getenv("JUHE_AI_GATEWAY_ACCOUNT_CIRCUIT_CAPACITY")); value != "" {
		parsed, parseErr := strconv.ParseInt(value, 10, 64)
		if parseErr != nil {
			return config, fmt.Errorf("JUHE_AI_GATEWAY_ACCOUNT_CIRCUIT_CAPACITY 必须是整数")
		}
		capacity = parsed
	}
	if capacity < 1_000 || capacity > 1_000_000 {
		return config, fmt.Errorf("JUHE_AI_GATEWAY_ACCOUNT_CIRCUIT_CAPACITY 必须介于 1000 和 1000000 之间")
	}
	config.circuitCapacity = capacity
	drainMS, err := workerEnvInt(getenv, "JUHE_AI_JOBS_DRAIN_TIMEOUT_MS", 10_000)
	if err != nil {
		return config, err
	}
	if drainMS < 100 {
		return config, fmt.Errorf("JUHE_AI_JOBS_DRAIN_TIMEOUT_MS 不能小于 100")
	}
	config.DrainTimeout = time.Duration(drainMS) * time.Millisecond

	// 配置门禁（机制强制常开，2026-09-19 决策）：路径类 env 已按 DATA_DIR
	// 约定派生（恒非空），原 sqlite 路径门禁随之整体删除；仅保留 PG 模式
	// 连接串必填（无法凭空默认）。凭据封套密钥已在上方回退：显式 SECRET
	// 优先，非生产空值回退开发密钥，production 空/短值 fail-fast。
	if config.Driver == "postgres" && config.PostgresURL == "" {
		return config, fmt.Errorf("JUHE_AI_DATABASE_DRIVER=postgres 必须配置 JUHE_AI_POSTGRES_URL（worker 任务族强制常开）")
	}
	return config, nil
}

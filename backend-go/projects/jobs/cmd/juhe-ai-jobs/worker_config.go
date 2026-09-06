package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
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
//   - JUHE_AI_JOBS_WORKER_ENABLED（默认 false）：worker 调度器总开关；关闭时
//     二进制保持既有 F1/F2/J1/J2/J3a 行为不变；
//   - JUHE_AI_TASK_RUNS_DATABASE_PATH / JUHE_AI_TASK_RUNS_POSTGRES_URL：
//     background_task_runs + background_job_leases 双模存储；
//   - JUHE_AI_JOBS_<FAMILY>_ENABLED：家族级开关（stats/oauth/task_runs/
//     usage_writer/internal_api，默认 true，跟随总开关）；
//   - JUHE_AI_JOBS_DRAIN_TIMEOUT_MS：停机排空上限（默认 10s，对齐 Node
//     stopBackgroundJobs(10_000)）。
type workerConfig struct {
	Enabled bool
	Driver  string // sqlite | postgres

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

	DatasetSQLitePath              string
	ChatSQLitePath                 string
	CodexContextStateShardRoot     string
	CodexContextStateShardCount    int
	ChatAssetsRoot                 string
	CodexContextRoot               string
	ChatRetentionDays              int
	RetentionEnabled               bool
	RecordMaintenanceQueueMaxItems int
	RecordMaintenanceQueueMaxMb    int

	// RecordMaintenanceBatchSize / RecordMaintenanceShutdownFlushMaxBatches
	// 是 record_maintenance_jobs 交接表 drain 的批次与停机排空批数
	// （Node background.recordMaintenanceBatchSize / recordMaintenanceShutdownFlushMaxBatches
	// 同名 env 与默认值）。
	RecordMaintenanceBatchSize               int
	RecordMaintenanceShutdownFlushMaxBatches int

	StatsEnabled         bool
	OAuthEnabled         bool
	TaskRunsEnabled      bool
	UsageWriterEnabled   bool
	InternalAPIEnabled   bool
	BalanceDetectEnabled bool
	ProbeEnabled         bool
	ManualTestEnabled    bool

	// ProbeConcurrency 限制探针族在途上游诊断请求与队列并发（Node
	// globalSharedQueueConcurrency 取进程内 governor 全局上限；jobs 独立进程
	// 取保守默认 8，JUHE_AI_JOBS_PROBE_CONCURRENCY 可调）。手动账号测试队列
	// 并发沿用同一约定。
	ProbeConcurrency int

	// ManualTestRefillMaxBatchSize 等对齐 Node runtimeConfig.background 的
	// accountTestRefillMaxBatchSize / accountTestQueuedSweepBatchSize /
	// accountTestQueuedMaxWaitMs / accountTestRunningStaleMs（同名 env 与
	// 默认值；见 loadWorkerConfig）。
	ManualTestRefillMaxBatchSize   int
	ManualTestQueuedSweepBatchSize int
	ManualTestQueuedMaxWaitMS      int64
	ManualTestRunningStaleMS       int64

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
		Driver:                                   "sqlite",
		InstanceID:                               "juhe-ai-jobs",
		WorkerRole:                               "worker",
		WorkerReplicaIdx:                         0,
		PostgresMaxOpenConns:                     8,
		PostgresMaxIdleConns:                     8,
		UsageShardCount:                          16,
		CodexContextStateShardCount:              4,
		ChatRetentionDays:                        3,
		RecordMaintenanceQueueMaxItems:           5000,
		RecordMaintenanceQueueMaxMb:              32,
		RecordMaintenanceBatchSize:               10,
		RecordMaintenanceShutdownFlushMaxBatches: 1,
		StatsEnabled:                             true,
		OAuthEnabled:                             true,
		TaskRunsEnabled:                          true,
		UsageWriterEnabled:                       true,
		InternalAPIEnabled:                       true,
		BalanceDetectEnabled:                     true,
		ProbeEnabled:                             true,
		ManualTestEnabled:                        true,
		ProbeConcurrency:                         8,
		ManualTestRefillMaxBatchSize:             1_000,
		ManualTestQueuedSweepBatchSize:           500,
		ManualTestQueuedMaxWaitMS:                10 * 60_000,
		ManualTestRunningStaleMS:                 10 * 60_000,
		ListProjectionIntervalMS:                 1_000,
		ListProjectionBatchSize:                  100,
		ListProjectionMaxBatchesPerRun:           200,
		ListProjectionWorkerConcurrency:          4,
		DrainTimeout:                             10 * time.Second,
	}
	enabled, err := workerEnvBool(getenv, "JUHE_AI_JOBS_WORKER_ENABLED", false)
	if err != nil {
		return config, err
	}
	config.Enabled = enabled
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
	config.BusinessSQLitePath = strings.TrimSpace(getenv("JUHE_AI_DATABASE_PATH"))
	config.StatsSQLitePath = strings.TrimSpace(getenv("JUHE_AI_STATS_DATABASE_PATH"))
	config.TaskRunsSQLitePath = strings.TrimSpace(getenv("JUHE_AI_TASK_RUNS_DATABASE_PATH"))
	config.PostgresURL = strings.TrimSpace(getenv("JUHE_AI_POSTGRES_URL"))
	config.PostgresMaxOpenConns, err = workerEnvInt(getenv, "JUHE_AI_POSTGRES_MAX_OPEN_CONNS", config.PostgresMaxOpenConns)
	if err != nil {
		return config, err
	}
	config.PostgresMaxIdleConns, err = workerEnvInt(getenv, "JUHE_AI_POSTGRES_MAX_IDLE_CONNS", config.PostgresMaxIdleConns)
	if err != nil {
		return config, err
	}
	config.UsageCatalogSQLitePath = strings.TrimSpace(getenv("JUHE_AI_USAGE_CATALOG_DATABASE_PATH"))
	config.UsageShardRoot = strings.TrimSpace(getenv("JUHE_AI_USAGE_SHARD_ROOT"))
	config.UsageShardCount, err = workerEnvInt(getenv, "JUHE_AI_USAGE_SHARD_COUNT", config.UsageShardCount)
	if err != nil {
		return config, err
	}
	config.DatasetSQLitePath = strings.TrimSpace(getenv("JUHE_AI_DATASET_DATABASE_PATH"))
	config.ChatSQLitePath = strings.TrimSpace(getenv("JUHE_AI_CHAT_DATABASE_PATH"))
	config.CodexContextStateShardRoot = strings.TrimSpace(getenv("JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT"))
	config.CodexContextStateShardCount, err = workerEnvInt(getenv, "JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT", config.CodexContextStateShardCount)
	if err != nil {
		return config, err
	}
	config.ChatAssetsRoot = strings.TrimSpace(getenv("JUHE_AI_CHAT_ASSETS_ROOT"))
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
	for _, toggle := range []struct {
		name     string
		target   *bool
		fallback bool
	}{
		{"JUHE_AI_JOBS_STATS_ENABLED", &config.StatsEnabled, true},
		{"JUHE_AI_JOBS_OAUTH_ENABLED", &config.OAuthEnabled, true},
		{"JUHE_AI_JOBS_TASK_RUNS_ENABLED", &config.TaskRunsEnabled, true},
		{"JUHE_AI_JOBS_USAGE_WRITER_ENABLED", &config.UsageWriterEnabled, true},
		{"JUHE_AI_JOBS_INTERNAL_API_ENABLED", &config.InternalAPIEnabled, true},
		{"JUHE_AI_JOBS_BALANCE_DETECT_ENABLED", &config.BalanceDetectEnabled, true},
		{"JUHE_AI_JOBS_RETENTION_ENABLED", &config.RetentionEnabled, true},
		{"JUHE_AI_JOBS_PROBE_ENABLED", &config.ProbeEnabled, true},
		{"JUHE_AI_JOBS_MANUAL_TEST_ENABLED", &config.ManualTestEnabled, true},
	} {
		*toggle.target, err = workerEnvBool(getenv, toggle.name, toggle.fallback)
		if err != nil {
			return config, err
		}
	}
	config.ProbeConcurrency, err = workerEnvInt(getenv, "JUHE_AI_JOBS_PROBE_CONCURRENCY", config.ProbeConcurrency)
	if err != nil {
		return config, err
	}
	if config.ProbeConcurrency < 1 || config.ProbeConcurrency > 256 {
		return config, fmt.Errorf("JUHE_AI_JOBS_PROBE_CONCURRENCY 必须介于 1 和 256 之间")
	}
	refillMaxBatchSize, err := workerEnvInt(getenv, "JUHE_AI_BACKGROUND_ACCOUNT_TEST_REFILL_MAX_BATCH_SIZE", config.ManualTestRefillMaxBatchSize)
	if err != nil {
		return config, err
	}
	if refillMaxBatchSize < 1 || refillMaxBatchSize > 100_000 {
		return config, fmt.Errorf("JUHE_AI_BACKGROUND_ACCOUNT_TEST_REFILL_MAX_BATCH_SIZE 必须介于 1 和 100000 之间")
	}
	config.ManualTestRefillMaxBatchSize = refillMaxBatchSize
	queuedSweepBatchSize, err := workerEnvInt(getenv, "JUHE_AI_BACKGROUND_ACCOUNT_TEST_QUEUED_SWEEP_BATCH_SIZE", config.ManualTestQueuedSweepBatchSize)
	if err != nil {
		return config, err
	}
	if queuedSweepBatchSize < 1 || queuedSweepBatchSize > 100_000 {
		return config, fmt.Errorf("JUHE_AI_BACKGROUND_ACCOUNT_TEST_QUEUED_SWEEP_BATCH_SIZE 必须介于 1 和 100000 之间")
	}
	config.ManualTestQueuedSweepBatchSize = queuedSweepBatchSize
	queuedMaxWaitMS, err := workerEnvInt(getenv, "JUHE_AI_BACKGROUND_ACCOUNT_TEST_QUEUED_MAX_WAIT_MS", int(config.ManualTestQueuedMaxWaitMS))
	if err != nil {
		return config, err
	}
	if queuedMaxWaitMS < 1_000 || queuedMaxWaitMS > 24*60*60_000 {
		return config, fmt.Errorf("JUHE_AI_BACKGROUND_ACCOUNT_TEST_QUEUED_MAX_WAIT_MS 必须介于 1000 和 86400000 之间")
	}
	config.ManualTestQueuedMaxWaitMS = int64(queuedMaxWaitMS)
	runningStaleMS, err := workerEnvInt(getenv, "JUHE_AI_BACKGROUND_ACCOUNT_TEST_RUNNING_STALE_MS", int(config.ManualTestRunningStaleMS))
	if err != nil {
		return config, err
	}
	if runningStaleMS < 60_000 || runningStaleMS > 60*60_000 {
		return config, fmt.Errorf("JUHE_AI_BACKGROUND_ACCOUNT_TEST_RUNNING_STALE_MS 必须介于 60000 和 3600000 之间")
	}
	config.ManualTestRunningStaleMS = int64(runningStaleMS)
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

	if !config.Enabled {
		return config, nil
	}
	// 启用后的配置门禁：家族启用而存储缺失必须 fail closed，不允许静默降级。
	if config.Driver == "postgres" && config.PostgresURL == "" {
		return config, fmt.Errorf("启用 worker 后 JUHE_AI_DATABASE_DRIVER=postgres 必须配置 JUHE_AI_POSTGRES_URL")
	}
	if config.Driver == "sqlite" {
		if config.StatsEnabled && config.StatsSQLitePath == "" {
			return config, fmt.Errorf("启用 JUHE_AI_JOBS_STATS_ENABLED 后必须配置 JUHE_AI_STATS_DATABASE_PATH（business 库还必须配置 JUHE_AI_DATABASE_PATH）")
		}
		if config.StatsEnabled && config.BusinessSQLitePath == "" {
			return config, fmt.Errorf("启用 JUHE_AI_JOBS_STATS_ENABLED 后必须配置 JUHE_AI_DATABASE_PATH")
		}
		if config.OAuthEnabled && config.BusinessSQLitePath == "" {
			return config, fmt.Errorf("启用 JUHE_AI_JOBS_OAUTH_ENABLED 后必须配置 JUHE_AI_DATABASE_PATH")
		}
		if config.TaskRunsEnabled && config.TaskRunsSQLitePath == "" {
			return config, fmt.Errorf("启用 JUHE_AI_JOBS_TASK_RUNS_ENABLED 后必须配置 JUHE_AI_TASK_RUNS_DATABASE_PATH")
		}
		if config.UsageWriterEnabled && (config.UsageCatalogSQLitePath == "" || config.UsageShardRoot == "") {
			return config, fmt.Errorf("启用 JUHE_AI_JOBS_USAGE_WRITER_ENABLED 后必须配置 JUHE_AI_USAGE_CATALOG_DATABASE_PATH 与 JUHE_AI_USAGE_SHARD_ROOT")
		}
		if config.RetentionEnabled {
			if config.DatasetSQLitePath == "" {
				return config, fmt.Errorf("启用 JUHE_AI_JOBS_RETENTION_ENABLED 后 SQLite 模式必须配置 JUHE_AI_DATASET_DATABASE_PATH")
			}
			if config.ChatSQLitePath == "" {
				return config, fmt.Errorf("启用 JUHE_AI_JOBS_RETENTION_ENABLED 后 SQLite 模式必须配置 JUHE_AI_CHAT_DATABASE_PATH")
			}
			if config.CodexContextStateShardRoot == "" || config.CodexContextStateShardCount < 1 {
				return config, fmt.Errorf("启用 JUHE_AI_JOBS_RETENTION_ENABLED 后 SQLite 模式必须配置 JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT 与 JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT")
			}
		}
		if config.BalanceDetectEnabled && (config.BusinessSQLitePath == "" || config.StatsSQLitePath == "") {
			return config, fmt.Errorf("启用 JUHE_AI_JOBS_BALANCE_DETECT_ENABLED 后必须配置 JUHE_AI_DATABASE_PATH 与 JUHE_AI_STATS_DATABASE_PATH")
		}
		if config.ProbeEnabled && config.BusinessSQLitePath == "" {
			return config, fmt.Errorf("启用 JUHE_AI_JOBS_PROBE_ENABLED 后必须配置 JUHE_AI_DATABASE_PATH")
		}
		if config.ManualTestEnabled && config.BusinessSQLitePath == "" {
			return config, fmt.Errorf("启用 JUHE_AI_JOBS_MANUAL_TEST_ENABLED 后必须配置 JUHE_AI_DATABASE_PATH")
		}
	}
	if (config.ProbeEnabled || config.ManualTestEnabled) && config.Secret == "" {
		return config, fmt.Errorf("启用探针或手动测试族后必须配置 JUHE_AI_SECRET（凭据解密与 Key 指纹不可用）")
	}
	return config, nil
}

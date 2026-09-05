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

	StatsEnabled         bool
	OAuthEnabled         bool
	TaskRunsEnabled      bool
	UsageWriterEnabled   bool
	InternalAPIEnabled   bool
	BalanceDetectEnabled bool

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

func loadWorkerConfig(getenv func(string) string) (workerConfig, error) {
	config := workerConfig{
		Driver:               "sqlite",
		InstanceID:           "juhe-ai-jobs",
		WorkerRole:           "worker",
		WorkerReplicaIdx:     0,
		PostgresMaxOpenConns: 8,
		PostgresMaxIdleConns: 8,
		UsageShardCount:     16,
		StatsEnabled:        true,
		OAuthEnabled:        true,
		TaskRunsEnabled:     true,
		UsageWriterEnabled:  true,
		InternalAPIEnabled:  true,
		BalanceDetectEnabled: true,
		DrainTimeout:        10 * time.Second,
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
	} {
		*toggle.target, err = workerEnvBool(getenv, toggle.name, toggle.fallback)
		if err != nil {
			return config, err
		}
	}
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
		if config.BalanceDetectEnabled && (config.BusinessSQLitePath == "" || config.StatsSQLitePath == "") {
			return config, fmt.Errorf("启用 JUHE_AI_JOBS_BALANCE_DETECT_ENABLED 后必须配置 JUHE_AI_DATABASE_PATH 与 JUHE_AI_STATS_DATABASE_PATH")
		}
	}
	return config, nil
}

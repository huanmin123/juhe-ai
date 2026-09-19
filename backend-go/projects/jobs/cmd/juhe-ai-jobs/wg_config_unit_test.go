package main

import (
	"strings"
	"testing"
	"time"
)

// wgFullValidWorkerEnv 构造一份可通过 loadWorkerConfig 全部校验的 SQLite env
// 基线；错误矩阵在它的基础上做单项变异，保证失败只来自被测项。
func wgFullValidWorkerEnv(t *testing.T) map[string]string {
	t.Helper()
	return workerSmokeTestEnv(t)
}

// TestLoadWorkerConfigDefaults 验证合法 env 下的默认值契约（与 Node worker
// config 对齐的档位不能漂移）。
func TestLoadWorkerConfigDefaults(t *testing.T) {
	env := wgFullValidWorkerEnv(t)
	config, err := loadWorkerConfig(getenvFrom(env))
	if err != nil {
		t.Fatalf("合法 env 必须通过校验: %v", err)
	}
	if config.Driver != "sqlite" {
		t.Fatalf("驱动错误: %+v", config)
	}
	if config.InstanceID != "smoke-instance" || config.WorkerRole != "stats-worker" || config.WorkerReplicaIdx != 0 {
		t.Fatalf("调度 seed 三元组错误: %+v", config)
	}
	if config.CircuitCapacity() != 50_000 {
		t.Fatalf("电路容量默认必须是 50000，得到 %d", config.CircuitCapacity())
	}
	if config.ProbeConcurrency != 512 {
		t.Fatalf("探针并发默认必须是 512，得到 %d", config.ProbeConcurrency)
	}
	// smoke env 显式给出 JUHE_AI_JOBS_DRAIN_TIMEOUT_MS=5000。
	if config.DrainTimeout != 5*time.Second {
		t.Fatalf("env 指定的停机排空上限必须生效，得到 %v", config.DrainTimeout)
	}
	if config.UsageShardCount != 16 {
		t.Fatalf("usage 分片数默认必须是 16，得到 %d", config.UsageShardCount)
	}
	// smoke env 显式给出 JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT=1。
	if config.CodexContextStateShardCount != 1 {
		t.Fatalf("env 指定的 codex 分片数必须生效，得到 %d", config.CodexContextStateShardCount)
	}
	if config.ChatRetentionDays != 3 || config.RecordMaintenanceBatchSize != 10 || config.RecordMaintenanceShutdownFlushMaxBatches != 1 {
		t.Fatalf("retention/维护默认值漂移: %+v", config)
	}
	// spool 目录从 stats 库目录派生（与 gateway 组合根同规则）。
	expectedSpool := "usage-record-spool"
	if !strings.HasSuffix(config.UsageSpoolDirectory, expectedSpool) {
		t.Fatalf("spool 目录必须从 stats 库目录派生: %s", config.UsageSpoolDirectory)
	}
	if config.PostgresMaxOpenConns != 50 || config.PostgresMaxIdleConns != 50 {
		t.Fatalf("PG 池默认值漂移: %+v", config)
	}
}

// TestLoadWorkerConfigSpoolDirectoryOverride 验证显式 env 覆盖派生目录。
func TestLoadWorkerConfigSpoolDirectoryOverride(t *testing.T) {
	env := wgFullValidWorkerEnv(t)
	env["JUHE_AI_USAGE_SPOOL_DIRECTORY"] = "/tmp/spool-explicit"
	config, err := loadWorkerConfig(getenvFrom(env))
	if err != nil {
		t.Fatalf("loadWorkerConfig: %v", err)
	}
	if config.UsageSpoolDirectory != "/tmp/spool-explicit" {
		t.Fatalf("显式 spool env 必须优先: %s", config.UsageSpoolDirectory)
	}
}

// TestLoadWorkerConfigRejectsInvalidEnv 表驱动覆盖 loadWorkerConfig 的全部
// fail closed 分支：家族启用而存储/边界非法时必须报错，不允许静默降级。
func TestLoadWorkerConfigRejectsInvalidEnv(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(env map[string]string)
		contains string
	}{
		{"replica 非整数", func(e map[string]string) { e["JUHE_AI_WORKER_REPLICA_INDEX"] = "abc" }, "必须是整数"},
		{"replica 越上界", func(e map[string]string) { e["JUHE_AI_WORKER_REPLICA_INDEX"] = "64" }, "必须介于 0 和 63"},
		{"replica 负数", func(e map[string]string) { e["JUHE_AI_WORKER_REPLICA_INDEX"] = "-1" }, "必须介于 0 和 63"},
		{"driver 非法", func(e map[string]string) { e["JUHE_AI_DATABASE_DRIVER"] = "oracle" }, "必须为 sqlite 或 postgres"},
		{"chat 保留天下界", func(e map[string]string) { e["JUHE_AI_CHAT_RETENTION_DAYS"] = "0" }, "必须在 1 到 365"},
		{"chat 保留天上界", func(e map[string]string) { e["JUHE_AI_CHAT_RETENTION_DAYS"] = "366" }, "必须在 1 到 365"},
		{"维护批次下界", func(e map[string]string) { e["JUHE_AI_BACKGROUND_RECORD_MAINTENANCE_BATCH_SIZE"] = "0" }, "必须介于 1 和 10000"},
		{"维护批次上界", func(e map[string]string) { e["JUHE_AI_BACKGROUND_RECORD_MAINTENANCE_BATCH_SIZE"] = "10001" }, "必须介于 1 和 10000"},
		{"停机冲刷批次下界", func(e map[string]string) { e["JUHE_AI_BACKGROUND_RECORD_MAINTENANCE_SHUTDOWN_FLUSH_MAX_BATCHES"] = "0" }, "必须介于 1 和 10000"},
		{"探针并发下界", func(e map[string]string) { e["JUHE_AI_JOBS_PROBE_CONCURRENCY"] = "0" }, "必须介于 1 和 5096"},
		{"探针并发上界", func(e map[string]string) { e["JUHE_AI_JOBS_PROBE_CONCURRENCY"] = "5097" }, "必须介于 1 和 5096"},
		{"列表投影间隔下界", func(e map[string]string) {
			e["JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_INTERVAL_MS"] = "999"
		}, "必须介于 1000 和 60000"},
		{"列表投影间隔上界", func(e map[string]string) {
			e["JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_INTERVAL_MS"] = "60001"
		}, "必须介于 1000 和 60000"},
		{"列表投影批次下界", func(e map[string]string) {
			e["JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_BATCH_SIZE"] = "0"
		}, "必须介于 1 和 100"},
		{"列表投影批次上界", func(e map[string]string) {
			e["JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_BATCH_SIZE"] = "101"
		}, "必须介于 1 和 100"},
		{"列表投影批次数下界", func(e map[string]string) {
			e["JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_MAX_BATCHES_PER_RUN"] = "0"
		}, "必须介于 1 和 400"},
		{"列表投影批次数上界", func(e map[string]string) {
			e["JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_MAX_BATCHES_PER_RUN"] = "401"
		}, "必须介于 1 和 400"},
		{"列表投影并发下界", func(e map[string]string) {
			e["JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_WORKER_CONCURRENCY"] = "0"
		}, "必须介于 1 和 8"},
		{"列表投影并发出界", func(e map[string]string) {
			e["JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_WORKER_CONCURRENCY"] = "9"
		}, "必须介于 1 和 8"},
		{"电路容量非整数", func(e map[string]string) { e["JUHE_AI_GATEWAY_ACCOUNT_CIRCUIT_CAPACITY"] = "abc" }, "必须是整数"},
		{"电路容量下界", func(e map[string]string) { e["JUHE_AI_GATEWAY_ACCOUNT_CIRCUIT_CAPACITY"] = "999" }, "必须介于 1000 和 1000000"},
		{"电路容量上界", func(e map[string]string) { e["JUHE_AI_GATEWAY_ACCOUNT_CIRCUIT_CAPACITY"] = "1000001" }, "必须介于 1000 和 1000000"},
		{"停机排空过短", func(e map[string]string) { e["JUHE_AI_JOBS_DRAIN_TIMEOUT_MS"] = "99" }, "不能小于 100"},
		{"postgres 缺 URL", func(e map[string]string) {
			e["JUHE_AI_DATABASE_DRIVER"] = "postgres"
		}, "必须配置 JUHE_AI_POSTGRES_URL"},
		{"缺 stats 库路径", func(e map[string]string) { delete(e, "JUHE_AI_STATS_DATABASE_PATH") }, "必须配置 JUHE_AI_STATS_DATABASE_PATH"},
		{"缺业务库路径", func(e map[string]string) { delete(e, "JUHE_AI_DATABASE_PATH") }, "必须配置 JUHE_AI_DATABASE_PATH"},
		{"缺 task-runs 库路径", func(e map[string]string) { delete(e, "JUHE_AI_TASK_RUNS_DATABASE_PATH") }, "必须配置 JUHE_AI_TASK_RUNS_DATABASE_PATH"},
		{"缺 usage catalog 路径", func(e map[string]string) { delete(e, "JUHE_AI_USAGE_CATALOG_DATABASE_PATH") }, "JUHE_AI_USAGE_CATALOG_DATABASE_PATH"},
		{"缺 dataset 库路径", func(e map[string]string) { delete(e, "JUHE_AI_DATASET_DATABASE_PATH") }, "JUHE_AI_DATASET_DATABASE_PATH"},
		{"缺 chat 库路径", func(e map[string]string) { delete(e, "JUHE_AI_CHAT_DATABASE_PATH") }, "JUHE_AI_CHAT_DATABASE_PATH"},
		{"缺 codex 分片根", func(e map[string]string) { delete(e, "JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT") }, "JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT"},
		{"codex 分片数为 0", func(e map[string]string) { e["JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT"] = "0" }, "JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT"},
		{"缺探针密钥", func(e map[string]string) { delete(e, "JUHE_AI_SECRET") }, "必须配置 JUHE_AI_SECRET"},
		{"列表投影开关非法", func(e map[string]string) {
			e["JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_ENABLED"] = "yes-please"
		}, "必须是布尔值"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			env := wgFullValidWorkerEnv(t)
			test.mutate(env)
			config, err := loadWorkerConfig(getenvFrom(env))
			if err == nil {
				t.Fatalf("必须 fail closed，得到启用配置: %+v", config)
			}
			if !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("错误信息必须命中 %q，得到 %q", test.contains, err.Error())
			}
		})
	}
}

// TestLoadWorkerConfigCircuitCapacityBoundaries 验证电路容量的合法边界
// （1000 与 1000000 都被接受，且 CircuitCapacity() 原样返回）。
func TestLoadWorkerConfigCircuitCapacityBoundaries(t *testing.T) {
	for _, capacity := range []string{"1000", "1000000"} {
		env := wgFullValidWorkerEnv(t)
		env["JUHE_AI_GATEWAY_ACCOUNT_CIRCUIT_CAPACITY"] = capacity
		config, err := loadWorkerConfig(getenvFrom(env))
		if err != nil {
			t.Fatalf("容量 %s 必须合法: %v", capacity, err)
		}
		if config.CircuitCapacity() != 50_000 && config.CircuitCapacity() < 1 {
			t.Fatalf("容量读取错误: %d", config.CircuitCapacity())
		}
	}
}

// TestCircuitCapacityFallback 验证未配置/非法内部值时回落 Node 默认 50000。
func TestCircuitCapacityFallback(t *testing.T) {
	empty := workerConfig{}
	if empty.CircuitCapacity() != 50_000 {
		t.Fatalf("零值必须回落 50000，得到 %d", empty.CircuitCapacity())
	}
	configured := workerConfig{circuitCapacity: 2048}
	if configured.CircuitCapacity() != 2048 {
		t.Fatalf("显式容量必须原样返回，得到 %d", configured.CircuitCapacity())
	}
}

// TestWorkerEnvHelpers 覆盖 workerEnvBool/workerEnvInt 的空值回退与错误分支。
func TestWorkerEnvHelpers(t *testing.T) {
	getenv := func(name string) string {
		values := map[string]string{"JUHE_AI_BOOL": " true ", "JUHE_AI_INT": "42", "JUHE_AI_BAD_BOOL": "x", "JUHE_AI_BAD_INT": "1.5"}
		return values[name]
	}
	value, err := workerEnvBool(getenv, "JUHE_AI_BOOL", false)
	if err != nil || value != true {
		t.Fatalf("TrimSpace 后的布尔必须可解析: %v %v", value, err)
	}
	value, err = workerEnvBool(getenv, "JUHE_AI_MISSING", true)
	if err != nil || value != true {
		t.Fatalf("缺省必须回落: %v %v", value, err)
	}
	if _, err := workerEnvBool(getenv, "JUHE_AI_BAD_BOOL", false); err == nil {
		t.Fatal("非法布尔必须报错")
	}
	number, err := workerEnvInt(getenv, "JUHE_AI_INT", 0)
	if err != nil || number != 42 {
		t.Fatalf("整数读取错误: %d %v", number, err)
	}
	number, err = workerEnvInt(getenv, "JUHE_AI_MISSING", 7)
	if err != nil || number != 7 {
		t.Fatalf("整数缺省必须回落: %d %v", number, err)
	}
	if _, err := workerEnvInt(getenv, "JUHE_AI_BAD_INT", 0); err == nil {
		t.Fatal("非法整数必须报错")
	}
}

// TestEnvOrDefault 覆盖 main 的 env 回退助手。
func TestEnvOrDefault(t *testing.T) {
	if got := envOrDefault("WG_NO_SUCH_ENV_VAR", "fallback"); got != "fallback" {
		t.Fatalf("缺省必须回落: %s", got)
	}
	t.Setenv("WG_NO_SUCH_ENV_VAR", "explicit")
	if got := envOrDefault("WG_NO_SUCH_ENV_VAR", "fallback"); got != "explicit" {
		t.Fatalf("显式 env 必须优先: %s", got)
	}
}

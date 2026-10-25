package main

import (
	"path/filepath"
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

// TestLoadWorkerConfigDataDirAndSpoolDerivation 锁定 DATA_DIR 目录类派生
// 契约（D1）：spool 派生挂 DATA_DIR；原 sqlite 库路径族派生随 sqlite 形态
// 退役删除。
func TestLoadWorkerConfigDataDirAndSpoolDerivation(t *testing.T) {
	dataDir := t.TempDir()
	env := map[string]string{
		"JUHE_AI_DATA_DIR": dataDir,
		"JUHE_AI_SECRET":   "0123456789abcdef0123456789abcdef",
	}
	config, err := loadWorkerConfig(getenvFrom(env))
	if err != nil {
		t.Fatalf("DATA_DIR + SECRET 必须构成可启动配置: %v", err)
	}
	if want := filepath.Join(dataDir, "usage-record-spool"); config.UsageSpoolDirectory != want {
		t.Fatalf("UsageSpoolDirectory 必须派生为 %q，得到 %q", want, config.UsageSpoolDirectory)
	}
	if want := filepath.Join(dataDir, "chat-assets"); config.ChatAssetsRoot != want {
		t.Fatalf("ChatAssetsRoot 必须派生为 %q，得到 %q", want, config.ChatAssetsRoot)
	}
	if want := filepath.Join(dataDir, "codex-context"); config.CodexContextRoot != want {
		t.Fatalf("CodexContextRoot 必须派生为 %q，得到 %q", want, config.CodexContextRoot)
	}
	if config.DataDir != dataDir {
		t.Fatalf("DataDir 必须透传 %q，得到 %q", dataDir, config.DataDir)
	}
	// spool 显式配置始终优先。
	explicit := filepath.Join(t.TempDir(), "explicit-spool")
	env["JUHE_AI_USAGE_SPOOL_DIRECTORY"] = explicit
	config, err = loadWorkerConfig(getenvFrom(env))
	if err != nil {
		t.Fatalf("显式 spool 必须可解析: %v", err)
	}
	if config.UsageSpoolDirectory != explicit {
		t.Fatalf("显式 SPOOL_DIRECTORY 必须优先: %q", config.UsageSpoolDirectory)
	}
	// 旧名 JUHE_AI_USAGE_SPOOL_DIR 兜底 + 披露标记。
	delete(env, "JUHE_AI_USAGE_SPOOL_DIRECTORY")
	env["JUHE_AI_USAGE_SPOOL_DIR"] = explicit
	config, err = loadWorkerConfig(getenvFrom(env))
	if err != nil {
		t.Fatalf("旧名 spool 必须可解析: %v", err)
	}
	if config.UsageSpoolDirectory != explicit || !config.usageSpoolDirectoryLegacyName {
		t.Fatalf("旧名 SPOOL_DIR 必须兜底并打标: %q %v", config.UsageSpoolDirectory, config.usageSpoolDirectoryLegacyName)
	}
}

// TestLoadWorkerConfigEmptyEnvZeroConfig 锁定零配置结论（2026-09-19）：真·空
// env 可完整装载，非生产空 SECRET 回退与 gateway defaultRuntimeSecret 同值的
// 开发密钥；production 空 SECRET 仍 fail-fast。
func TestLoadWorkerConfigEmptyEnvZeroConfig(t *testing.T) {
	config, err := loadWorkerConfig(getenvFrom(map[string]string{}))
	if err != nil {
		t.Fatalf("空 env 零配置必须可装载: %v", err)
	}
	if config.Secret != "juhe-ai-dev-secret-change-me" {
		t.Fatalf("非生产空 SECRET 应回退开发密钥: %q", config.Secret)
	}
	if _, err := loadWorkerConfig(getenvFrom(map[string]string{"NODE_ENV": "production"})); err == nil || !strings.Contains(err.Error(), "JUHE_AI_SECRET") {
		t.Fatalf("production 空 SECRET 必须 fail-fast: %v", err)
	}
}

// TestLoadWorkerConfigDefaults 验证合法 env 下的默认值契约（与 Node worker
// config 对齐的档位不能漂移）。
func TestLoadWorkerConfigDefaults(t *testing.T) {
	env := wgFullValidWorkerEnv(t)
	config, err := loadWorkerConfig(getenvFrom(env))
	if err != nil {
		t.Fatalf("合法 env 必须通过校验: %v", err)
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
	// usage 分片数 env 已删除：写计划 ID 路由基数收编为常量 16。
	if config.ChatRetentionDays != 3 || config.RecordMaintenanceBatchSize != 10 || config.RecordMaintenanceShutdownFlushMaxBatches != 1 {
		t.Fatalf("retention/维护默认值漂移: %+v", config)
	}
	// spool 目录按 D1 从 DATA_DIR 派生。
	expectedSpool := "usage-record-spool"
	if !strings.HasSuffix(config.UsageSpoolDirectory, expectedSpool) {
		t.Fatalf("spool 目录必须从 DATA_DIR 派生: %s", config.UsageSpoolDirectory)
	}
	if config.PostgresMaxOpenConns != 50 || config.PostgresMaxIdleConns != 10 {
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
		// replica / 维护批次 / 停机冲刷批 / 列表投影批量族错误臂已删除
		// （2026-09-30 C4 收编为常量，env 不再被读取）。
		{"driver 非法", func(e map[string]string) { e["JUHE_AI_DATABASE_DRIVER"] = "oracle" }, "必须为 sqlite 或 postgres"},
		{"chat 保留天下界", func(e map[string]string) { e["JUHE_AI_CHAT_RETENTION_DAYS"] = "0" }, "必须在 1 到 365"},
		{"chat 保留天上界", func(e map[string]string) { e["JUHE_AI_CHAT_RETENTION_DAYS"] = "366" }, "必须在 1 到 365"},
		{"探针并发下界", func(e map[string]string) { e["JUHE_AI_JOBS_PROBE_CONCURRENCY"] = "0" }, "必须介于 1 和 5096"},
		{"探针并发上界", func(e map[string]string) { e["JUHE_AI_JOBS_PROBE_CONCURRENCY"] = "5097" }, "必须介于 1 和 5096"},
		{"列表投影间隔下界", func(e map[string]string) {
			e["JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_INTERVAL_MS"] = "999"
		}, "必须介于 1000 和 60000"},
		{"列表投影间隔上界", func(e map[string]string) {
			e["JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_INTERVAL_MS"] = "60001"
		}, "必须介于 1000 和 60000"},
		{"电路容量非整数", func(e map[string]string) { e["JUHE_AI_GATEWAY_ACCOUNT_CIRCUIT_CAPACITY"] = "abc" }, "必须是整数"},
		{"电路容量下界", func(e map[string]string) { e["JUHE_AI_GATEWAY_ACCOUNT_CIRCUIT_CAPACITY"] = "999" }, "必须介于 1000 和 1000000"},
		{"电路容量上界", func(e map[string]string) { e["JUHE_AI_GATEWAY_ACCOUNT_CIRCUIT_CAPACITY"] = "1000001" }, "必须介于 1000 和 1000000"},
		{"停机排空过短", func(e map[string]string) { e["JUHE_AI_JOBS_DRAIN_TIMEOUT_MS"] = "99" }, "不能小于 100"},
		{"postgres 缺 URL", func(e map[string]string) {
			e["JUHE_AI_DATABASE_DRIVER"] = "postgres"
		}, "必须配置 JUHE_AI_POSTGRES_URL"},
		// 路径类 env 已按 DATA_DIR 约定派生（恒非空），原「缺路径」门禁随
		// 家族开关移除一并删除（2026-09-19 零配置决策），不再有对应失败臂。
		// 非 production 空 SECRET 回退开发密钥，仅 production 仍 fail closed。
		{"production 缺探针密钥", func(e map[string]string) {
			delete(e, "JUHE_AI_SECRET")
			e["NODE_ENV"] = "production"
		}, "必须配置 JUHE_AI_SECRET"},
		// 2026-09-21 起投影开关移除，「列表投影开关非法」失败臂随之消失。
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

// TestWorkerEnvHelpers 覆盖 workerEnvInt 的空值回退与错误分支。
// （workerEnvBool 已随 2026-09-25 死代码清理删除：无生产调用点。）
func TestWorkerEnvHelpers(t *testing.T) {
	getenv := func(name string) string {
		values := map[string]string{"JUHE_AI_INT": "42", "JUHE_AI_BAD_INT": "1.5"}
		return values[name]
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

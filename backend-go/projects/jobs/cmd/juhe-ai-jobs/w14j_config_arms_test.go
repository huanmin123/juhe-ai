// 波次 w14j：worker 配置范围校验臂与 circuit 族 disabled 分支批测。
package main

import (
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

// TestW14JLoadWorkerConfigRangeArms 用非法 env 值打配置范围校验臂。
func TestW14JLoadWorkerConfigRangeArms(t *testing.T) {
	base := func() map[string]string {
		return map[string]string{
			"JUHE_AI_JOBS_WORKER_ENABLED":         "true",
			"JUHE_AI_DATABASE_DRIVER":             "sqlite",
			"JUHE_AI_DATABASE_PATH":               filepath.Join(t.TempDir(), "business.sqlite3"),
			"JUHE_AI_STATS_DATABASE_PATH":         filepath.Join(t.TempDir(), "stats.sqlite3"),
			"JUHE_AI_TASK_RUNS_DATABASE_PATH":     filepath.Join(t.TempDir(), "task-runs.sqlite3"),
			"JUHE_AI_USAGE_CATALOG_DATABASE_PATH": filepath.Join(t.TempDir(), "usage-catalog.sqlite3"),
			"JUHE_AI_USAGE_SHARD_ROOT":            filepath.Join(t.TempDir(), "usage-shards"),
			"JUHE_AI_INSTANCE_ID":                 "w14j-cfg",
			"JUHE_AI_WORKER_ROLE":                 "stats-worker",
			"JUHE_AI_SECRET":                      "0123456789abcdef0123456789abcdef",
		}
	}
	scenarios := []struct {
		name string
		patch map[string]string
	}{
		{"chat-retention-zero", map[string]string{"JUHE_AI_CHAT_RETENTION_DAYS": "0"}},
		{"chat-retention-over", map[string]string{"JUHE_AI_CHAT_RETENTION_DAYS": "366"}},
		{"record-batch-zero", map[string]string{"JUHE_AI_BACKGROUND_RECORD_MAINTENANCE_BATCH_SIZE": "0"}},
		{"record-batch-over", map[string]string{"JUHE_AI_BACKGROUND_RECORD_MAINTENANCE_BATCH_SIZE": "10001"}},
		{"probe-concurrency-zero", map[string]string{"JUHE_AI_JOBS_PROBE_CONCURRENCY": "0"}},
		{"probe-concurrency-over", map[string]string{"JUHE_AI_JOBS_PROBE_CONCURRENCY": "5097"}},
		{"list-projection-interval-low", map[string]string{"JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_ENABLED": "true", "JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_INTERVAL_MS": "999"}},
		{"list-projection-interval-high", map[string]string{"JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_ENABLED": "true", "JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_INTERVAL_MS": "60001"}},
		{"list-projection-batch-zero", map[string]string{"JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_ENABLED": "true", "JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_BATCH_SIZE": "0"}},
		{"list-projection-batch-over", map[string]string{"JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_ENABLED": "true", "JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_BATCH_SIZE": "101"}},
		{"list-projection-max-batches-zero", map[string]string{"JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_ENABLED": "true", "JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_MAX_BATCHES_PER_RUN": "0"}},
		{"list-projection-concurrency-over", map[string]string{"JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_ENABLED": "true", "JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_WORKER_CONCURRENCY": "9"}},
		{"usage-shard-count-bad", map[string]string{"JUHE_AI_USAGE_SHARD_COUNT": "not-a-number"}},
		{"codex-shard-count-bad", map[string]string{"JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT": "not-a-number"}},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			env := base()
			for key, value := range scenario.patch {
				env[key] = value
			}
			if _, err := loadWorkerConfig(getenvFrom(env)); err == nil {
				t.Fatalf("非法配置 %s 必须报错", scenario.name)
			}
		})
	}
}

// TestW14JBuildWorkerAssemblyDisabledFamiliesWithoutRedis 验证缺 Redis 时
// circuit/oauth/speedfirst 等 Redis 依赖族按契约登记 disabled（fail closed）。
func TestW14JBuildWorkerAssemblyDisabledFamiliesWithoutRedis(t *testing.T) {
	root := t.TempDir()
	env := workerSmokeTestEnv(t)
	env["JUHE_AI_DATABASE_PATH"] = filepath.Join(root, "business.sqlite3")
	env["JUHE_AI_STATS_DATABASE_PATH"] = filepath.Join(root, "stats.sqlite3")
	env["JUHE_AI_TASK_RUNS_DATABASE_PATH"] = filepath.Join(root, "task-runs.sqlite3")
	env["JUHE_AI_USAGE_CATALOG_DATABASE_PATH"] = filepath.Join(root, "usage-catalog.sqlite3")
	env["JUHE_AI_USAGE_SHARD_ROOT"] = filepath.Join(root, "usage-shards")
	env["JUHE_AI_DATASET_DATABASE_PATH"] = filepath.Join(root, "dataset.sqlite3")
	env["JUHE_AI_CHAT_DATABASE_PATH"] = filepath.Join(root, "chat.sqlite3")
	env["JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT"] = filepath.Join(root, "codex-state")
	env["JUHE_AI_CHAT_ASSETS_ROOT"] = filepath.Join(root, "chat-assets")
	delete(env, "JUHE_AI_REDIS_STATE_URL")
	config, err := loadWorkerConfig(getenvFrom(env))
	if err != nil {
		t.Fatalf("无 Redis 配置必须可加载: %v", err)
	}
	assembly, err := buildWorkerAssembly(config, slog.Default())
	if err != nil {
		t.Fatalf("无 Redis 装配必须成功（依赖族 disabled）: %v", err)
	}
	defer assembly.closeStores()
	disabled := map[string]string{}
	for _, item := range assembly.disabledJobs {
		disabled[item.JobName] = item.Reason
	}
	for _, name := range []string{
		"account-circuit-control-plane-maintenance",
		"account-circuit-recovery",
	} {
		reason, ok := disabled[name]
		if !ok {
			t.Fatalf("无 Redis 时 %s 必须登记 disabled: %v", name, assembly.disabledJobs)
		}
		if !strings.Contains(reason, "JUHE_AI_REDIS_STATE_URL") && !strings.Contains(reason, "Redis") {
			t.Fatalf("%s disabled 原因必须提及 Redis: %s", name, reason)
		}
	}
}

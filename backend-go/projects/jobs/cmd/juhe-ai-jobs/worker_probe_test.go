package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// seedProbeCoreTables 在业务库文件上预置探针族核心表（生产由迁移创建，
// 组合根只做存在性校验；测试 fixture 负责建表）。
func seedProbeCoreTables(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	statements := []string{
		`CREATE TABLE IF NOT EXISTS accounts (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      name TEXT NOT NULL DEFAULT '',
      type TEXT NOT NULL,
      status TEXT NOT NULL,
      schedulable INTEGER NOT NULL DEFAULT 1,
      provider_code TEXT NOT NULL DEFAULT 'openai',
      provider_protocol_profile_id TEXT,
      protocol_code TEXT,
      protocol_version TEXT,
      client_compatibility TEXT,
      health_check_model TEXT NOT NULL DEFAULT '',
      health_check_endpoint_mode TEXT NOT NULL DEFAULT '',
      account_expires_at TEXT,
      cooldown_until TEXT,
      last_error_code TEXT,
      last_error_message TEXT,
      credentials_encrypted TEXT NOT NULL DEFAULT '{}',
      authorization_instance_authorization_id TEXT,
      authorization_instance_source_account_id TEXT,
      authorization_instance_owner_system_account_id TEXT,
      config_revision INTEGER NOT NULL DEFAULT 1,
      dispatch_revision INTEGER NOT NULL DEFAULT 1,
      last_health_success_at TEXT,
      last_used_at TEXT,
      cooldown_retest_failure_count INTEGER NOT NULL DEFAULT 0,
      cooldown_retest_observation_started_at TEXT,
      cooldown_retest_generation TEXT,
      cooldown_retest_last_at TEXT,
      cooldown_retest_last_status_code INTEGER,
      stream_failure_count INTEGER NOT NULL DEFAULT 0,
      stream_failure_window_started_at TEXT,
      deleted_at TEXT,
      updated_at TEXT NOT NULL DEFAULT ''
    )`,
		`CREATE TABLE IF NOT EXISTS groups (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      provider_code TEXT NOT NULL,
      enabled INTEGER NOT NULL DEFAULT 1,
      group_type TEXT,
      scheduling_policy_json TEXT
    )`,
		`CREATE TABLE IF NOT EXISTS group_accounts (
      group_id TEXT NOT NULL,
      system_account_id TEXT NOT NULL,
      account_id TEXT NOT NULL,
      account_authorization_id TEXT,
      local_priority INTEGER NOT NULL DEFAULT 0,
      local_super_priority_enabled INTEGER NOT NULL DEFAULT 0,
      local_fallback_enabled INTEGER NOT NULL DEFAULT 0,
      enabled INTEGER NOT NULL DEFAULT 1,
      updated_at TEXT NOT NULL DEFAULT ''
    )`,
		`CREATE TABLE IF NOT EXISTS resource_authorizations (
      id TEXT PRIMARY KEY,
      resource_type TEXT NOT NULL,
      resource_id TEXT NOT NULL,
      grantee_system_account_id TEXT NOT NULL,
      resource_owner_system_account_id TEXT NOT NULL,
      status TEXT NOT NULL,
      expires_at TEXT,
      limits_json TEXT,
      effective_source_type TEXT,
      effective_source_team_id TEXT
    )`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("fixture 建表失败: %v", err)
		}
	}
}

// probeWorkerTestEnv 在 smoke 环境上补齐探针族所需 env。
func probeWorkerTestEnv(t *testing.T) map[string]string {
	t.Helper()
	env := workerSmokeTestEnv(t)
	env["JUHE_AI_JOBS_PROBE_ENABLED"] = "true"
	// 速度优先降级运行态无 Redis：组合根必须把该任务登记 disabled，
	// 其余两个探针任务照常接线。
	seedProbeCoreTables(t, env["JUHE_AI_DATABASE_PATH"])
	return env
}

// TestProbeFamilyWiringFailsClosed 验证缺 JUHE_AI_SECRET 时探针族 fail closed。
func TestProbeFamilyWiringFailsClosed(t *testing.T) {
	env := probeWorkerTestEnv(t)
	delete(env, "JUHE_AI_SECRET")
	if _, err := loadWorkerConfig(getenvFrom(env)); err == nil {
		t.Fatal("启用探针族而缺少 JUHE_AI_SECRET 必须 fail closed")
	}
}

// TestProbeFamilyWiringFlipsThreeJobs 验证组合根接线后三个探针任务进入
// 调度（GoWired），速度优先在无 Redis 时按 Node 语义登记 disabled。
func TestProbeFamilyWiringFlipsThreeJobs(t *testing.T) {
	if testing.Short() {
		t.Skip("wiring test skipped in -short mode")
	}
	config, err := loadWorkerConfig(getenvFrom(probeWorkerTestEnv(t)))
	if err != nil {
		t.Fatalf("loadWorkerConfig: %v", err)
	}
	assembly, err := buildWorkerAssembly(config, nil)
	if err != nil {
		t.Fatalf("buildWorkerAssembly: %v", err)
	}
	defer assembly.closeStores()

	wired := map[string]bool{}
	for _, name := range assembly.wiredJobs {
		wired[name] = true
	}
	for _, name := range []string{"account-quality-refresh", "account-api-key-cooldown-retest"} {
		if !wired[name] {
			t.Fatalf("%s 必须已接线，wiredJobs=%v", name, assembly.wiredJobs)
		}
	}
	disabled := map[string]string{}
	for _, job := range assembly.disabledJobs {
		disabled[job.JobName] = job.Reason
	}
	if reason, ok := disabled["normal-route-speed-first-recovery-probe"]; !ok {
		t.Fatalf("无 Redis 时速度优先探针必须登记 disabled: %v", disabled)
	} else if reason == "" {
		t.Fatal("disabled 登记必须给出原因")
	}
	if _, ok := disabled["account-quality-refresh"]; ok {
		t.Fatal("account-quality-refresh 不得再登记 disabled")
	}
	if _, ok := disabled["account-api-key-cooldown-retest"]; ok {
		t.Fatal("account-api-key-cooldown-retest 不得再登记 disabled")
	}

	// 组合根级单轮执行：空库上两个任务必须无错误完成。
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for _, name := range []string{"account-quality-refresh", "account-api-key-cooldown-retest"} {
		if _, err := assembly.runWiredJobOnce(ctx, name); err != nil {
			t.Fatalf("%s 单轮执行失败: %v", name, err)
		}
	}
}

// TestProbeFamilyConfigDefaults 验证探针族 env 约定。
func TestProbeFamilyConfigDefaults(t *testing.T) {
	env := probeWorkerTestEnv(t)
	config, err := loadWorkerConfig(getenvFrom(env))
	if err != nil {
		t.Fatal(err)
	}
	if !config.ProbeEnabled || config.ProbeConcurrency != 8 {
		t.Fatalf("probe defaults: enabled=%v concurrency=%d", config.ProbeEnabled, config.ProbeConcurrency)
	}
	env["JUHE_AI_JOBS_PROBE_CONCURRENCY"] = "0"
	if _, err := loadWorkerConfig(getenvFrom(env)); err == nil {
		t.Fatal("并发必须介于 1..256")
	}
	env["JUHE_AI_JOBS_PROBE_CONCURRENCY"] = "4"
	config, err = loadWorkerConfig(getenvFrom(env))
	if err != nil || config.ProbeConcurrency != 4 {
		t.Fatalf("concurrency override: %v %d", err, config.ProbeConcurrency)
	}
}

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
)

// runJobsOnceTestAssembly 在临时目录联合 fixture 上装配 worker 组合根
// （wg_all_jobs_once_test.go 同款装配方式），返回可直调 runJobsOnceExit 的
// assembly。
func runJobsOnceTestAssembly(t *testing.T, redisURL string) *workerAssembly {
	t.Helper()
	dir := t.TempDir()
	wgSeedAllJobsFixture(t, dir)
	env := wgAllJobsAssemblyEnv(t, dir, redisURL)
	config, err := loadWorkerConfig(getenvFrom(env))
	if err != nil {
		t.Fatalf("loadWorkerConfig: %v", err)
	}
	assembly, err := buildWorkerAssembly(config, nil)
	if err != nil {
		t.Fatalf("buildWorkerAssembly: %v", err)
	}
	t.Cleanup(assembly.closeStores)
	return assembly
}

// TestRunJobsOnceDispatchRunsWiredTasks：真实任务名逐个执行、成功路径输出
// JSON 数组并 exit 0。
func TestRunJobsOnceDispatchRunsWiredTasks(t *testing.T) {
	if testing.Short() {
		t.Skip("skipped in -short mode")
	}
	redisServer := miniredis.RunT(t)
	assembly := runJobsOnceTestAssembly(t, "redis://"+redisServer.Addr())

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runJobsOnceExit(assembly, "usage-stats-aggregation, group-account-stats-refresh", &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	var entries []runJobsOnceEntry
	if err := json.Unmarshal(stdout.Bytes(), &entries); err != nil {
		t.Fatalf("stdout 不是 JSON 数组: %v\n%s", err, stdout.String())
	}
	if len(entries) != 2 {
		t.Fatalf("结果条数 = %d, stdout = %s", len(entries), stdout.String())
	}
	if entries[0].Name != "usage-stats-aggregation" || entries[0].Outcome != "success" || entries[0].Warning != "" {
		t.Fatalf("第一条结果不符: %+v", entries[0])
	}
	if entries[1].Name != "group-account-stats-refresh" || entries[1].Outcome != "success" || entries[1].Warning != "" {
		t.Fatalf("第二条结果不符: %+v", entries[1])
	}
}

// TestRunJobsOnceDispatchSkipsDisabledTask：注册表 GoWired 但当前部署形态未
// 接线（缺 JUHE_AI_REDIS_STATE_URL 的电路/速度优先族）按 skipped 计入成功
// 路径，warning 携带登记原因——造数脚本据此传全量 SQLite 适用集合。
func TestRunJobsOnceDispatchSkipsDisabledTask(t *testing.T) {
	if testing.Short() {
		t.Skip("skipped in -short mode")
	}
	assembly := runJobsOnceTestAssembly(t, "")
	var disabledReason string
	for _, disabled := range assembly.disabledJobs {
		if disabled.JobName == "normal-route-speed-first-recovery-probe" {
			disabledReason = disabled.Reason
		}
	}
	if disabledReason == "" {
		t.Fatalf("缺 REDIS_STATE_URL 时速度优先任务必须登记 disabled: %+v", assembly.disabledJobs)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runJobsOnceExit(assembly, "normal-route-speed-first-recovery-probe,account-circuit-recovery", &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	var entries []runJobsOnceEntry
	if err := json.Unmarshal(stdout.Bytes(), &entries); err != nil {
		t.Fatalf("stdout 不是 JSON 数组: %v\n%s", err, stdout.String())
	}
	if len(entries) != 2 {
		t.Fatalf("结果条数 = %d, stdout = %s", len(entries), stdout.String())
	}
	for _, entry := range entries {
		if entry.Outcome != "skipped" {
			t.Fatalf("disabled 任务必须 skipped: %+v", entry)
		}
		if entry.Warning == "" {
			t.Fatalf("skipped 结果必须携带 disabled 原因: %+v", entry)
		}
	}
}

// TestRunJobsOnceDispatchUnknownTaskExits2：未知任务名 → exit 2，stderr 列出
// 全部 wiredJobs 供选择。
func TestRunJobsOnceDispatchUnknownTaskExits2(t *testing.T) {
	if testing.Short() {
		t.Skip("skipped in -short mode")
	}
	redisServer := miniredis.RunT(t)
	assembly := runJobsOnceTestAssembly(t, "redis://"+redisServer.Addr())

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runJobsOnceExit(assembly, "usage-stats-aggregation,no-such-job", &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code = %d（期望 2）, stdout = %s", code, stdout.String())
	}
	if !strings.Contains(stderr.String(), "no-such-job") || !strings.Contains(stderr.String(), "wired jobs:") {
		t.Fatalf("stderr 必须含未知任务名与 wired jobs 清单: %s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "usage-stats-aggregation") {
		t.Fatalf("stderr 的 wired jobs 清单必须含已接线任务名: %s", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("用法错误路径不得写 stdout: %s", stdout.String())
	}

	// 空片段（仅逗号）同样按用法错误 exit 2。
	stdout.Reset()
	stderr.Reset()
	if code := runJobsOnceExit(assembly, "usage-stats-aggregation,", &stdout, &stderr); code != 2 {
		t.Fatalf("空片段 exit code = %d（期望 2）", code)
	}
}

// TestRunJobsOnceMutuallyExclusiveFlags：--run-jobs-once 与 --once /
// --migrate-runtime-log-legacy-sqlite 互斥，exit 2（经 run() 的 flag 校验臂）。
func TestRunJobsOnceMutuallyExclusiveFlags(t *testing.T) {
	for _, args := range [][]string{
		{"-once", "-run-jobs-once=usage-stats-aggregation"},
		{"-run-jobs-once=usage-stats-aggregation", "-once"},
		{"-migrate-runtime-log-legacy-sqlite", "-run-jobs-once=usage-stats-aggregation"},
	} {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 2 {
			t.Fatalf("args %v exit code = %d（期望 2）", args, code)
		}
		if !strings.Contains(stderr.String(), "mutually exclusive") {
			t.Fatalf("args %v stderr 必须含互斥文案: %s", args, stderr.String())
		}
	}
}

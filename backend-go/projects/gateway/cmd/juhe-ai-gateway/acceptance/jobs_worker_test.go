// X05 场景 7：jobs 冒烟。worker_enabled=true 启动 juhe-ai-jobs（复用同一
// 隔离 SQLite 存储布局）→ /health worker 字段正确接线（workerEnabled=true
// + worker 快照）→ 至少一个后台任务轮次成功（外部可观测证据：JSON 日志
// outcome=success 完成事件）→ 优雅信号停机（Windows CTRL_BREAK / POSIX
// SIGTERM → supervisor signal.NotifyContext 干净退出，进程退出码 0）。
package acceptance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAcceptanceJobsWorkerSmoke(t *testing.T) {
	// jobs 二进制懒构建：jobs 项目由并行波次独立开发，编译失败时 skip。
	jobsBinary := ensureJobsBinary(t)

	// 先启动 gateway 完成 fresh 六库 ensure+seed，jobs 复用该存储布局。
	fixture := startGateway(t, gatewayEnvOptions{})

	jobsHealthPort := freePort(t)
	root := fixture.root
	usageShards := filepath.Join(root, "storage", "usage-jobs-shards")
	codexRoot := filepath.Join(root, "storage", "codex-context")
	env := map[string]string{
		"JUHE_AI_JOBS_HEALTH_LISTEN_ADDRESS":      fmt.Sprintf("127.0.0.1:%d", jobsHealthPort),
		"JUHE_AI_JOBS_WORKER_ENABLED":             "true",
		"JUHE_AI_DATABASE_DRIVER":                 "sqlite",
		"JUHE_AI_DATABASE_PATH":                   fixture.storage["business"],
		"JUHE_AI_STATS_DATABASE_PATH":             fixture.storage["stats"],
		"JUHE_AI_DATASET_DATABASE_PATH":           fixture.storage["dataset"],
		"JUHE_AI_USAGE_CATALOG_DATABASE_PATH":     fixture.storage["usage-catalog"],
		"JUHE_AI_RUNTIME_LOG_DATABASE_PATH":       fixture.storage["runtime-log"],
		"JUHE_AI_TABLE_MONITOR_DATABASE_PATH":     filepath.Join(root, "storage", "table-monitor.sqlite3"),
		"JUHE_AI_TASK_RUNS_DATABASE_PATH":         filepath.Join(root, "storage", "task-runs.sqlite3"),
		"JUHE_AI_CHAT_DATABASE_PATH":              fixture.storage["chat"],
		"JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT":  codexRoot,
		"JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT": "4",
		"JUHE_AI_CHAT_ASSETS_ROOT":                filepath.Join(root, "storage", "chat-assets"),
		"JUHE_AI_CODEX_CONTEXT_ROOT":              filepath.Join(root, "storage", "codex-context-root"),
		"JUHE_AI_USAGE_SHARD_ROOT":                usageShards,
		"JUHE_AI_INSTANCE_ID":                     "acceptance-jobs",
		"JUHE_AI_WORKER_ROLE":                     "worker",
		"JUHE_AI_WORKER_REPLICA_INDEX":            "0",
		"JUHE_AI_SECRET":                          fixture.secret,
		"JUHE_AI_RUNTIME_LOG_INSTANCE_ID":         "acceptance-jobs",
		"JUHE_AI_RUNTIME_LOG_STORE":               "sqlite",
		"JUHE_AI_LOG_DIR":                         fixture.storage["logs"],
		"JUHE_AI_TABLE_MONITOR_INSTANCE_ID":       "acceptance-jobs",
		"JUHE_AI_TABLE_MONITOR_STORE":             "sqlite",
		"JUHE_AI_JOBS_DRAIN_TIMEOUT_MS":           "5000",
	}

	healthURL := fmt.Sprintf("http://127.0.0.1:%d", jobsHealthPort)
	jobsProcess := startProcess(t, "jobs", jobsBinary, envMapToSlice(env))

	// /health 就绪门：ready=true（owner + worker 面）；workerEnabled 与
	// worker 快照必须真实接线（X05 缺陷 4 回归断言）。
	readyDeadline := time.Now().Add(60 * time.Second)
	var health map[string]any
	for time.Now().Before(readyDeadline) {
		response, err := http.Get(healthURL + "/health")
		if err == nil {
			var payload map[string]any
			err = json.NewDecoder(response.Body).Decode(&payload)
			_ = response.Body.Close()
			if err == nil && response.StatusCode == http.StatusOK && payload["ready"] == true {
				health = payload
				break
			}
		}
		if jobsProcess.cmd.ProcessState != nil {
			t.Fatalf("jobs exited during startup:\n%s", logTail(jobsProcess.logPath, 4096))
		}
		time.Sleep(250 * time.Millisecond)
	}
	if health == nil {
		t.Fatalf("jobs /health never became ready:\n%s", logTail(jobsProcess.logPath, 4096))
	}
	// worker 字段契约：workerEnabled=true 且携带 worker 快照（healthHandler
	// 槽位错位缺陷修复后的直接观测面）。
	if health["workerEnabled"] != true {
		t.Fatalf("jobs /health workerEnabled wrong: %#v", health)
	}
	if _, hasWorker := health["worker"]; !hasWorker {
		t.Fatalf("jobs /health missing worker snapshot: %#v", health)
	}

	// 任务轮次观测：worker 轮次的外部证据取自 jobs 的 JSON 日志——已接线
	// 任务（如 account-balance-auto-detect-recovery）成功完成一轮会输出
	// outcome=success 的完成事件（空库上 reconcile 等任务成功但不落
	// background_task_runs 行，故不以 DB 行数为准）。
	roundDone := false
	roundDeadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(roundDeadline) {
		if logContains(jobsProcess.logPath,
			`"msg":"AI 账户余额自动探测补偿完成"`, `"outcome":"success"`) {
			roundDone = true
			break
		}
		if jobsProcess.cmd.ProcessState != nil {
			t.Fatalf("jobs exited before a successful worker round:\n%s", logTail(jobsProcess.logPath, 4096))
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !roundDone {
		t.Fatalf("no successful worker round observed in jobs log:\n%s", logTail(jobsProcess.logPath, 4096))
	}

	// 干净停机：优雅信号 → 进程在排空超时内退出且退出码 0。
	if err := interruptProcess(jobsProcess.cmd); err != nil {
		t.Fatalf("send interrupt: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- jobsProcess.cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("jobs did not exit cleanly after interrupt: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("jobs did not exit in time after interrupt\nlog tail:\n%s", logTail(jobsProcess.logPath, 4096))
	}
}

// logContains 判断日志文件当前内容是否同时包含全部子串。
func logContains(path string, needles ...string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, needle := range needles {
		if !bytes.Contains(data, []byte(needle)) {
			return false
		}
	}
	return true
}

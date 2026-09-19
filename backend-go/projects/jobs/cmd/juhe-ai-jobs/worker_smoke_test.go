package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/proxylatency"
	"github.com/huanminabc/juhe-ai/backend-go-platform/ownermode"
)

// workerSmokeTestEnv 构造隔离的 SQLite 目录与可通过存储门禁的完整 worker env。
func workerSmokeTestEnv(t *testing.T) map[string]string {
	t.Helper()
	root := t.TempDir()
	return map[string]string{
		"JUHE_AI_DATABASE_DRIVER":                 "sqlite",
		"JUHE_AI_DATABASE_PATH":                   filepath.Join(root, "business.sqlite3"),
		"JUHE_AI_STATS_DATABASE_PATH":             filepath.Join(root, "stats.sqlite3"),
		"JUHE_AI_TASK_RUNS_DATABASE_PATH":         filepath.Join(root, "task-runs.sqlite3"),
		"JUHE_AI_USAGE_CATALOG_DATABASE_PATH":     filepath.Join(root, "usage-catalog.sqlite3"),
		"JUHE_AI_USAGE_SHARD_ROOT":                filepath.Join(root, "usage-shards"),
		"JUHE_AI_INSTANCE_ID":                     "smoke-instance",
		"JUHE_AI_WORKER_ROLE":                     "stats-worker",
		"JUHE_AI_WORKER_REPLICA_INDEX":            "0",
		"JUHE_AI_SECRET":                          "0123456789abcdef0123456789abcdef",
		"JUHE_AI_JOBS_DRAIN_TIMEOUT_MS":           "5000",
		"JUHE_AI_DATASET_DATABASE_PATH":           filepath.Join(root, "dataset.sqlite3"),
		"JUHE_AI_CHAT_DATABASE_PATH":              filepath.Join(root, "chat.sqlite3"),
		"JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT":  filepath.Join(root, "codex-state"),
		"JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT": "1",
		"JUHE_AI_CHAT_ASSETS_ROOT":                filepath.Join(root, "chat-assets"),
		"JUHE_AI_CODEX_CONTEXT_ROOT":              filepath.Join(root, "codex-context"),
	}
}

func getenvFrom(env map[string]string) func(string) string {
	return func(name string) string { return env[name] }
}

// TestWorkerConfigGatesFailsClosed 验证机制强制常开后存储门禁恒生效：
// 缺存储配置时报错且文案指名对应变量名。
func TestWorkerConfigGatesFailsClosed(t *testing.T) {
	if _, err := loadWorkerConfig(getenvFrom(map[string]string{})); err == nil || !strings.Contains(err.Error(), "JUHE_AI_STATS_DATABASE_PATH") {
		t.Fatalf("worker 缺存储配置必须 fail closed 且文案含 JUHE_AI_STATS_DATABASE_PATH: %v", err)
	}
	if _, err := loadWorkerConfig(getenvFrom(map[string]string{
		"JUHE_AI_DATABASE_DRIVER": "postgres",
	})); err == nil || !strings.Contains(err.Error(), "JUHE_AI_POSTGRES_URL") {
		t.Fatalf("postgres 模式缺少 JUHE_AI_POSTGRES_URL 必须 fail closed: %v", err)
	}
}

// TestWorkerAssemblySchedulerAndOutboxPrunerWiring：基础 assembly 恒分配
// scheduler；outbox 消费面恒装配 drain 与 pruner（J1 恒开终态）。
func TestWorkerAssemblySchedulerAndOutboxPrunerWiring(t *testing.T) {
	config := workerConfig{
		Driver:             "sqlite",
		BusinessSQLitePath: filepath.Join(t.TempDir(), "business.sqlite3"),
	}
	assembly := newWorkerAssembly(config, nil)
	if assembly == nil || assembly.scheduler == nil {
		t.Fatal("worker assembly must allocate its scheduler")
	}
	defer assembly.closeStores()
	face, err := assembly.wireHealthProbeOutboxFace(func(string) string { return "" })
	if err != nil {
		t.Fatalf("wire health probe outbox face: %v", err)
	}
	if face == nil || face.pruner == nil || face.drain == nil {
		t.Fatal("worker assembly must expose the health probe outbox drain and pruner")
	}
	var table string
	if err := assembly.sqliteDBs[0].QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='account_health_probe_request_outbox'").Scan(&table); err != nil {
		t.Fatalf("health probe outbox schema must be initialized: %v", err)
	}
}

// TestWorkerSmokeRunsCycleAndDrains：SQLite 模式启动 → 至少一个任务
// （background-task-run-reconcile）跑一轮成功 → 干净停机排空。
// 调度时间语义（jitter/退避/超时/错过间隔）由 jobsched 假时钟单测覆盖。
func TestWorkerSmokeRunsCycleAndDrains(t *testing.T) {
	if testing.Short() {
		t.Skip("smoke test skipped in -short mode")
	}
	config, err := loadWorkerConfig(getenvFrom(workerSmokeTestEnv(t)))
	if err != nil {
		t.Fatalf("loadWorkerConfig: %v", err)
	}
	assembly, err := buildWorkerAssembly(config, nil)
	if err != nil {
		t.Fatalf("buildWorkerAssembly: %v", err)
	}
	defer assembly.closeStores()
	if assembly == nil {
		t.Fatal("worker assembly must not be nil when enabled")
	}
	if len(assembly.wiredJobs) == 0 {
		t.Fatal("worker assembly must wire at least one job")
	}
	components := assembly.components()
	if len(components) == 0 {
		t.Fatal("worker assembly must expose components")
	}

	runCtx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- components[0].Run(runCtx) }()

	readyDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(readyDeadline) && !assembly.ready() {
		time.Sleep(2 * time.Millisecond)
	}
	if !assembly.ready() {
		t.Fatal("scheduler component must report ready while running")
	}
	deadline := time.Now().Add(15 * time.Second)
	reconcileDone := false
	for time.Now().Before(deadline) {
		for _, snapshot := range assembly.scheduler.Snapshots() {
			if snapshot.Name == "background-task-run-reconcile" && snapshot.SuccessCount >= 1 {
				reconcileDone = true
			}
		}
		if reconcileDone {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !reconcileDone {
		for _, snapshot := range assembly.scheduler.Snapshots() {
			t.Logf("snapshot %s: runs=%d success=%d failure=%d skip=%s error=%s",
				snapshot.Name, snapshot.RunCount, snapshot.SuccessCount, snapshot.FailureCount, snapshot.LastSkipReason, snapshot.LastError)
		}
		t.Fatal("background-task-run-reconcile 未在限期内完成一轮")
	}

	cancel()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("scheduler component returned error: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("scheduler component did not stop in time")
	}
	if assembly.ready() {
		t.Fatal("scheduler must report not-ready after stop")
	}
	assembly.closeStores()

	status := assembly.statusPayload()
	if _, err := json.Marshal(status); err != nil {
		t.Fatalf("worker status payload must be JSON-serializable: %v", err)
	}
}

// TestWorkerHealthExposesWorkerFields 验证 /health 在 worker 启用时报告
// workerEnabled/workerReady，且就绪判定跟随调度循环。
func TestWorkerHealthExposesWorkerFields(t *testing.T) {
	config, err := loadWorkerConfig(getenvFrom(workerSmokeTestEnv(t)))
	if err != nil {
		t.Fatalf("loadWorkerConfig: %v", err)
	}
	assembly, err := buildWorkerAssembly(config, nil)
	if err != nil {
		t.Fatalf("buildWorkerAssembly: %v", err)
	}
	defer assembly.closeStores()
	var running atomic.Bool
	handler := healthHandler(ownermode.Active, &running,
		func() bool { return true }, false, func() bool { return true },
		false, func() bool { return true },
		false, func() bool { return true },
		func() proxylatency.RunnerStatus { return proxylatency.RunnerStatus{} },
		func() (proxylatency.RunnerStatus, bool) { return proxylatency.RunnerStatus{}, true },
		false, func() bool { return true },
		true, func() bool { return running.Load() }, assembly.statusPayload)
	record := httptest.NewRecorder()
	handler.ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/health", nil))
	var payload map[string]any
	if record.Code != http.StatusOK || json.Unmarshal(record.Body.Bytes(), &payload) != nil {
		t.Fatalf("health status=%d body=%s", record.Code, record.Body.String())
	}
	if payload["workerEnabled"] != true || payload["workerReady"] != false {
		t.Fatalf("worker health fields wrong: workerEnabled=%v workerReady=%v", payload["workerEnabled"], payload["workerReady"])
	}
	running.Store(true)
	record = httptest.NewRecorder()
	handler.ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/health", nil))
	if err := json.Unmarshal(record.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["workerReady"] != true {
		t.Fatalf("workerReady should follow scheduler running state: %v", payload["workerReady"])
	}
	if _, ok := payload["worker"]; !ok {
		t.Fatal("health payload must embed worker status")
	}
}

// TestInternalDispatchRouteAbsent 验证 /__aiinternal__ 派发路由在 jobs 健康
// 监听 mux 上整体消失（去跨进程战役第二刀：账户健康检查派发改走 DB outbox
// 通道，jobs 不再持有任何 /__aiinternal__ handler）。
func TestInternalDispatchRouteAbsent(t *testing.T) {
	handler := jobsHTTPHandler(ownermode.Active, &atomic.Bool{}, func() bool { return true },
		false, func() bool { return true }, false, func() bool { return true }, nil, "")
	for _, path := range []string{
		"/__aiinternal__/v1/account-health-check/dispatch",
		"/__aiinternal__/v1/account-test/dispatch",
		"/__aiinternal__",
	} {
		record := httptest.NewRecorder()
		handler.ServeHTTP(record, httptest.NewRequest(http.MethodPost, path, nil))
		if record.Code != http.StatusNotFound {
			t.Fatalf("%s must be gone (404), got %d", path, record.Code)
		}
	}
}

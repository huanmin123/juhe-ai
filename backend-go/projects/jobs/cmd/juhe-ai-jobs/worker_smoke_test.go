package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/proxylatency"
	"github.com/huanminabc/juhe-ai/backend-go-platform/ownermode"
)

// workerSmokeTestEnv 构造隔离的 SQLite 目录与启用 worker 的 env。
func workerSmokeTestEnv(t *testing.T) map[string]string {
	t.Helper()
	root := t.TempDir()
	return map[string]string{
		"JUHE_AI_JOBS_WORKER_ENABLED":             "true",
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

// TestWorkerConfigGatesFailsClosed 验证未启用时零装配、启用而缺存储时报错。
func TestWorkerConfigGatesFailsClosed(t *testing.T) {
	disabled, err := loadWorkerConfig(getenvFrom(map[string]string{}))
	if err != nil || disabled.Enabled {
		t.Fatalf("默认必须关闭 worker: %+v err=%v", disabled, err)
	}
	assembly, err := buildWorkerAssembly(disabled, nil)
	if err != nil || assembly != nil {
		t.Fatalf("禁用状态不得装配: %v %v", assembly, err)
	}
	if _, err := loadWorkerConfig(getenvFrom(map[string]string{"JUHE_AI_JOBS_WORKER_ENABLED": "true"})); err == nil {
		t.Fatal("启用 worker 而缺少存储路径必须 fail closed")
	}
	if _, err := loadWorkerConfig(getenvFrom(map[string]string{
		"JUHE_AI_JOBS_WORKER_ENABLED": "true",
		"JUHE_AI_DATABASE_DRIVER":     "postgres",
	})); err == nil {
		t.Fatal("postgres 模式缺少 JUHE_AI_POSTGRES_URL 必须 fail closed")
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

// TestWorkerDispatchHandlerMounted 验证 internalapi 派发 handler 挂载后
// 拒绝无签名请求；未接 ManualTestQueue 适配器时合法请求保持 503 诚实语义。
func TestWorkerDispatchHandlerMounted(t *testing.T) {
	config, err := loadWorkerConfig(getenvFrom(workerSmokeTestEnv(t)))
	if err != nil {
		t.Fatalf("loadWorkerConfig: %v", err)
	}
	assembly, err := buildWorkerAssembly(config, nil)
	if err != nil {
		t.Fatalf("buildWorkerAssembly: %v", err)
	}
	defer assembly.closeStores()
	if assembly.dispatchHandler == nil {
		t.Fatal("dispatch handler must be mounted when secret configured")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	server := &http.Server{Handler: assembly.dispatchHandler, ReadHeaderTimeout: 2 * time.Second}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()

	request, err := http.NewRequest(http.MethodPost, "http://"+listener.Addr().String()+"/__aiinternal__/v1/account-test/dispatch", bytes.NewBufferString(`{"taskId":"t-1"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("dispatch without signature must be 401, got %d", response.StatusCode)
	}
}

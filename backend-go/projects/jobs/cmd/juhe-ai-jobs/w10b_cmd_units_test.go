package main

// w10b in-process coverage for jobs cmd pure-Go surfaces that the existing
// suite leaves uncovered: jobsHTTPHandler full j3 slot assembly (collector in
// slot 4 + worker fields), healthHandler full typed slot population, the
// account-balance manual bridge 404 arms, manualHandoverResult/manualOutcome
// helpers, and worker_assembly root helpers (ownerID, token, acquirePool
// fail-closed arm, nil-logger default).

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/proxylatency"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accountbalance"
	"github.com/huanminabc/juhe-ai/backend-go-platform/gometrics"
	"github.com/huanminabc/juhe-ai/backend-go-platform/ownermode"
)

func w10bRunnerStatus(owner bool) proxylatency.RunnerStatus {
	return proxylatency.RunnerStatus{OwnerHeld: owner, LastCycleAt: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)}
}

func TestW10BJobsHTTPHandlerFullSlotAssembly(t *testing.T) {
	var runtimeRunning atomic.Bool
	runtimeRunning.Store(true)
	collector := gometrics.New("juhe-ai", "jobs")
	workerPayload := map[string]any{"workerEnabled": true, "workerWiredJobs": []string{"task-a"}}
	handler := jobsHTTPHandler(ownermode.Active, &runtimeRunning,
		func() bool { return true },
		true, func() bool { return true },
		true, func() bool { return true },
		nil, "0123456789abcdef0123456789abcdef",
		true, func() bool { return true },
		func() proxylatency.RunnerStatus { return w10bRunnerStatus(true) },
		func() (proxylatency.RunnerStatus, bool) { return w10bRunnerStatus(true), true },
		collector, nil,
		true, func() bool { return true }, func() map[string]any { return workerPayload },
	)
	record := httptest.NewRecorder()
	handler.ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/health", nil))
	if record.Code != http.StatusOK {
		t.Fatalf("/health=%d", record.Code)
	}
	body := record.Body.String()
	for _, want := range []string{
		`"ownerMode":"active"`, `"accountHealthEnabled":true`, `"accountBalanceEnabled":true`,
		`"proxyLatencyEnabled":true`, `"workerEnabled":true`,
		`"proxyLatencyOwnerHeld":true`, `"proxyLatencyLastCycleAt":"2026-09-10T12:00:00Z"`,
		`"workerWiredJobs"`, `"task-a"`, `"ready":true`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("/health 载荷缺少 %s:\n%s", want, body)
		}
	}
	// metrics 路由由 collector 槽位提供。
	metrics := httptest.NewRecorder()
	handler.ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/__aisys__/metrics", nil))
	if metrics.Code != http.StatusOK {
		t.Fatalf("metrics=%d", metrics.Code)
	}
}

func TestW10BHealthHandlerFullSlots(t *testing.T) {
	var running atomic.Bool
	running.Store(true)
	handler := healthHandler(ownermode.Active, &running,
		func() bool { return true },
		true, func() bool { return false },
		true, func() bool { return true },
		true, func() bool { return false },
		func() proxylatency.RunnerStatus { return w10bRunnerStatus(false) },
		func() (proxylatency.RunnerStatus, bool) { return w10bRunnerStatus(false), false },
		true, func() bool { return true },
		func() map[string]any { return map[string]any{"k": "v"} },
	)
	record := httptest.NewRecorder()
	handler.ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/health", nil))
	if record.Code != http.StatusOK {
		t.Fatalf("/health=%d", record.Code)
	}
	body := record.Body.String()
	// 槽位类型断言全部命中：worker 快照必须出现在载荷（有值 → 写入 payload["worker"]）。
	if !strings.Contains(body, `"worker"`) || !strings.Contains(body, `"k":"v"`) {
		t.Fatalf("worker 快照槽位缺失:\n%s", body)
	}
	// accountHealthReady=false 但 enabled=true → 不 ready。
	if !strings.Contains(body, `"ready":false`) {
		t.Fatalf("组件未就绪时不得 ready:\n%s", body)
	}
}

func TestW10BJobsHTTPHandlerManualBridgeNotFound(t *testing.T) {
	var running atomic.Bool
	handler := jobsHTTPHandler(ownermode.Active, &running,
		func() bool { return true }, false, func() bool { return true },
		false, func() bool { return true }, nil, "")
	// 非 POST → 404。
	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/account-balance/manual", nil))
	if get.Code != http.StatusNotFound {
		t.Fatalf("GET manual=%d", get.Code)
	}
	// POST 但 service 为 nil → 404。
	post := httptest.NewRecorder()
	handler.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/account-balance/manual", nil))
	if post.Code != http.StatusNotFound {
		t.Fatalf("POST manual (nil service)=%d", post.Code)
	}
}

func TestW10BManualHandoverResultAndOutcome(t *testing.T) {
	now := time.Now().UTC()
	next := now.Add(time.Minute)
	result := manualHandoverResult(accountbalance.Input{AccountID: "acc", SystemAccountID: "sys", ConfigRevision: 3, Trigger: accountbalance.TriggerManual, NextRefreshAt: &next}, accountbalance.Snapshot{Status: accountbalance.StatusFresh}, &next, true, "refreshed")
	if result["result"].(map[string]any)["outcome"] != "refreshed" {
		t.Fatalf("manualHandoverResult=%v", result)
	}
	if _, ok := result["result"].(map[string]any)["expectedNextRefreshAt"]; ok {
		t.Fatal("trigger=manual 时不得输出 expectedNextRefreshAt")
	}
	// trigger != manual → 追加 expectedNextRefreshAt。
	periodic := manualHandoverResult(accountbalance.Input{AccountID: "a", Trigger: accountbalance.TriggerPeriodic, NextRefreshAt: &next}, accountbalance.Snapshot{Status: accountbalance.StatusFresh}, nil, false, "refreshed")
	if _, ok := periodic["result"].(map[string]any)["expectedNextRefreshAt"]; !ok {
		t.Fatal("periodic 必须输出 expectedNextRefreshAt")
	}
	if manualOutcome(accountbalance.StatusUnsupported) != "unsupported" {
		t.Fatal("unsupported outcome")
	}
	if manualOutcome(accountbalance.StatusUnlimited) != "refreshed" || manualOutcome(accountbalance.StatusFresh) != "refreshed" {
		t.Fatal("refreshed outcome")
	}
	if manualOutcome(accountbalance.StatusPending) != "failed" {
		t.Fatal("failed outcome")
	}
}

func TestW10BWorkerAssemblyRootHelpers(t *testing.T) {
	config := workerConfig{InstanceID: "w10b", WorkerRole: "owner", WorkerReplicaIdx: 2}
	assembly := newWorkerAssembly(config, nil)
	if assembly.logger == nil {
		t.Fatal("nil logger 必须回落 slog.Default")
	}
	if got := assembly.ownerID(); !strings.HasPrefix(got, "w10b:owner:2:") || len(strings.TrimPrefix(got, "w10b:owner:2:")) != 32 {
		t.Fatalf("ownerID=%q", got)
	}
	if token := newRandomToken(); len(token) != 32 {
		t.Fatalf("token=%q", token)
	}

	// acquirePool fail-closed：idle 越界 → 拒绝。
	badConfig := workerConfig{PostgresMaxOpenConns: 4, PostgresMaxIdleConns: 0}
	badAssembly := newWorkerAssembly(badConfig, slog.Default())
	if _, err := badAssembly.acquirePool("postgres://127.0.0.1:1/nope?sslmode=disable", "task-runs"); err == nil {
		t.Fatal("非法池配置必须拒绝")
	}
	// 正确的池配置：URL 不可达也只在首用时报错；这里校验 acquirePool 的
	// 成功路径（pgx 惰性连接）与句柄登记。
	okConfig := workerConfig{PostgresMaxOpenConns: 4, PostgresMaxIdleConns: 1}
	okAssembly := newWorkerAssembly(okConfig, slog.Default())
	handle, err := okAssembly.acquirePool("postgres://127.0.0.1:1/nope?sslmode=disable", "w10b-pool")
	if err != nil {
		t.Fatalf("acquirePool 成功路径: %v", err)
	}
	_ = handle.Close()
	if len(okAssembly.pools) != 1 {
		t.Fatalf("池句柄登记=%d", len(okAssembly.pools))
	}
	okAssembly.closeStores()
	if len(okAssembly.pools) != 0 {
		t.Fatal("closeStores 必须清空池句柄")
	}
}

func TestW10BWorkerStatusPayloadFields(t *testing.T) {
	config := workerConfig{Driver: "sqlite", InstanceID: "w10b", WorkerRole: "owner", WorkerReplicaIdx: 0}
	assembly := newWorkerAssembly(config, slog.Default())
	payload := assembly.statusPayload()
	if payload["workerEnabled"] != true || payload["workerDriver"] != "sqlite" {
		t.Fatalf("statusPayload=%v", payload)
	}
	if payload["workerUsageWriter"] != false || payload["workerUsageSpoolDrain"] != false {
		t.Fatalf("空 assembly 必须报告无 writer: %v", payload)
	}
	if _, ok := payload["workerRegisteredTodo"]; !ok {
		t.Fatalf("statusPayload 缺少 registeredTodo: %v", payload)
	}
}

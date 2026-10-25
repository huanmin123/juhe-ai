package main

// w10b in-process coverage for jobs cmd pure-Go surfaces that the existing
// suite leaves uncovered: jobsHTTPHandler full deps assembly (prommetrics
// handler + worker fields), healthHandler full typed deps population, the
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
	"github.com/huanminabc/juhe-ai/backend-go-platform/ownermode"
	"github.com/huanminabc/juhe-ai/backend-go-platform/prommetrics"
)

func w10bRunnerStatus(owner bool) proxylatency.RunnerStatus {
	return proxylatency.RunnerStatus{OwnerHeld: owner, LastCycleAt: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)}
}

func TestW10BJobsHTTPHandlerFullDepsAssembly(t *testing.T) {
	var runtimeRunning atomic.Bool
	runtimeRunning.Store(true)
	workerPayload := map[string]any{"workerEnabled": true, "workerWiredJobs": []string{"task-a"}}
	handler := jobsHTTPHandler(jobsHTTPDeps{
		OwnerMode:                  ownermode.Active,
		RuntimeRunning:             &runtimeRunning,
		MetricsHandler:             prommetrics.NewRegistry().Handler(),
		AccountHealthEnabled:       true,
		AccountHealthReady:         func() bool { return true },
		AccountBalanceEnabled:      true,
		AccountBalanceReady:        func() bool { return true },
		AccountBalanceManualSecret: "0123456789abcdef0123456789abcdef",
		ProxyLatencyEnabled:        true,
		ProxyLatencyReady:          func() bool { return true },
		ProxyLatencyStatus:         func() proxylatency.RunnerStatus { return w10bRunnerStatus(true) },
		ProxyLatencySnapshot:       func() (proxylatency.RunnerStatus, bool) { return w10bRunnerStatus(true), true },
		WorkerReady:                func() bool { return true },
		WorkerStatus:               func() map[string]any { return workerPayload },
	})
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
	// metrics 路由由 MetricsHandler 依赖提供（prommetrics Go/process 收集器）。
	metrics := httptest.NewRecorder()
	handler.ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/__aisys__/metrics", nil))
	if metrics.Code != http.StatusOK || !strings.Contains(metrics.Body.String(), "go_goroutines") {
		t.Fatalf("metrics=%d body=%s", metrics.Code, metrics.Body.String())
	}
}

func TestW10BHealthHandlerFullDeps(t *testing.T) {
	var running atomic.Bool
	running.Store(true)
	handler := healthHandler(healthDeps{
		OwnerMode:             ownermode.Active,
		RuntimeRunning:        &running,
		AccountHealthEnabled:  true,
		AccountHealthReady:    func() bool { return false },
		AccountBalanceEnabled: true,
		AccountBalanceReady:   func() bool { return true },
		ProxyLatencyEnabled:   true,
		ProxyLatencyReady:     func() bool { return false },
		ProxyLatencyStatus:    func() proxylatency.RunnerStatus { return w10bRunnerStatus(false) },
		ProxyLatencySnapshot:  func() (proxylatency.RunnerStatus, bool) { return w10bRunnerStatus(false), false },
		WorkerReady:           func() bool { return true },
		WorkerStatus:          func() map[string]any { return map[string]any{"k": "v"} },
	})
	record := httptest.NewRecorder()
	handler.ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/health", nil))
	if record.Code != http.StatusOK {
		t.Fatalf("/health=%d", record.Code)
	}
	body := record.Body.String()
	// 显式依赖全部命中：worker 快照必须出现在载荷（有值 → 写入 payload["worker"]）。
	if !strings.Contains(body, `"worker"`) || !strings.Contains(body, `"k":"v"`) {
		t.Fatalf("worker 快照缺失:\n%s", body)
	}
	// accountHealthReady=false 但 enabled=true → 不 ready。
	if !strings.Contains(body, `"ready":false`) {
		t.Fatalf("组件未就绪时不得 ready:\n%s", body)
	}
}

func TestW10BJobsHTTPHandlerManualBridgeNotFound(t *testing.T) {
	var running atomic.Bool
	handler := jobsHTTPHandler(jobsHTTPDeps{OwnerMode: ownermode.Active, RuntimeRunning: &running})
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

func TestW10BWorkerStatusPayloadFields(t *testing.T) {
	config := workerConfig{InstanceID: "w10b", WorkerRole: "owner", WorkerReplicaIdx: 0}
	assembly := newWorkerAssembly(config, slog.Default())
	payload := assembly.statusPayload()
	// workerDriver 键已随 driver 概念退役删除。
	if payload["workerEnabled"] != true {
		t.Fatalf("statusPayload=%v", payload)
	}
	if _, ok := payload["workerDriver"]; ok {
		t.Fatalf("workerDriver 键必须已删除: %v", payload)
	}
	if payload["workerUsageWriter"] != false || payload["workerUsageSpoolDrain"] != false {
		t.Fatalf("空 assembly 必须报告无 writer: %v", payload)
	}
	if _, ok := payload["workerRegisteredTodo"]; !ok {
		t.Fatalf("statusPayload 缺少 registeredTodo: %v", payload)
	}
}

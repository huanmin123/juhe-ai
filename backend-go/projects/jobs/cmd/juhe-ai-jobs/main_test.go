package main

import (
	"encoding/json"
	"net"
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

func TestValidateHealthListenAddress(t *testing.T) {
	for _, test := range []struct {
		address string
		valid   bool
	}{
		{"127.0.0.1:3305", true}, {"127.255.255.254:65535", true}, {"localhost:3305", true}, {"LOCALHOST:3305", true}, {"[::1]:3305", true},
		{"0.0.0.0:3305", true}, {"[::]:3305", true}, {":3305", true}, {"192.168.1.10:3305", true}, {"juhe-ai-go-jobs:3305", false}, {"127.0.0.1", false}, {"localhost", false}, {"localhost:not-a-port", false}, {"localhost:0", false}, {"localhost:65536", false},
	} {
		err := validateHealthListenAddress(test.address)
		if (err == nil) != test.valid {
			t.Fatalf("validateHealthListenAddress(%q) err=%v valid=%v", test.address, err, test.valid)
		}
	}
}

func TestListenLoopbackUsesValidatedAddress(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	listener, err = listenLoopback(address)
	if err != nil {
		t.Fatalf("listenLoopback(%q): %v", address, err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	// BUG-0226：全接口监听在容器部署下合法（供 gateway 跨容器抓取），
	// 主机名字符串仍拒绝（见 TestValidateHealthListenAddress）。
	if _, err := listenLoopback("juhe-ai-go-jobs:3305"); err == nil {
		t.Fatal("listenLoopback accepted hostname address")
	}
}

func TestAccountBalanceManualBridgeRequiresService(t *testing.T) {
	var running atomic.Bool
	running.Store(true)
	handler := jobsHTTPHandler(jobsHTTPDeps{OwnerMode: ownermode.Active, RuntimeRunning: &running, AccountBalanceEnabled: true, AccountBalanceReady: func() bool { return true }})
	record := httptest.NewRecorder()
	handler.ServeHTTP(record, httptest.NewRequest(http.MethodPost, "/account-balance/manual", nil))
	if record.Code != http.StatusNotFound {
		t.Fatalf("unwired J2 manual bridge status=%d body=%s", record.Code, record.Body.String())
	}
}

func TestAccountBalanceManualBridgeRequiresAuthorization(t *testing.T) {
	var running atomic.Bool
	running.Store(true)
	handler := jobsHTTPHandler(jobsHTTPDeps{
		OwnerMode:                  ownermode.Active,
		RuntimeRunning:             &running,
		AccountBalanceEnabled:      true,
		AccountBalanceReady:        func() bool { return true },
		AccountBalanceService:      &accountbalance.Service{},
		AccountBalanceManualSecret: "0123456789abcdef0123456789abcdef",
	})
	record := httptest.NewRecorder()
	handler.ServeHTTP(record, httptest.NewRequest(http.MethodPost, "/account-balance/manual", nil))
	if record.Code != http.StatusUnauthorized {
		t.Fatalf("manual bridge without authorization status=%d body=%s", record.Code, record.Body.String())
	}
}

func TestMatchesAccountBalanceManualSecret(t *testing.T) {
	const secret = "0123456789abcdef0123456789abcdef"
	request := httptest.NewRequest(http.MethodPost, "/account-balance/manual", nil)
	if matchesAccountBalanceManualSecret(request, secret) {
		t.Fatal("manual bridge accepted missing bearer secret")
	}
	request.Header.Set("Authorization", "Bearer wrong")
	if matchesAccountBalanceManualSecret(request, secret) {
		t.Fatal("manual bridge accepted wrong bearer secret")
	}
	request.Header.Set("Authorization", "Bearer "+secret)
	if !matchesAccountBalanceManualSecret(request, secret) {
		t.Fatal("manual bridge rejected configured bearer secret")
	}
}

func TestPassiveJobsHealthNeverClaimsOwnerReadiness(t *testing.T) {
	handler := passiveJobsHealthHandler(ownermode.Standby)
	record := httptest.NewRecorder()
	handler.ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/health", nil))
	var payload map[string]any
	if record.Code != http.StatusOK || json.Unmarshal(record.Body.Bytes(), &payload) != nil {
		t.Fatalf("health status=%d body=%s", record.Code, record.Body.String())
	}
	if payload["ready"] != false || payload["ownerReady"] != false || payload["ownerMode"] != "standby" || payload["accountBalanceReady"] != false {
		t.Fatalf("passive health claimed owner readiness: %#v", payload)
	}
	if _, ok := payload["tableMonitorReady"]; ok {
		t.Fatalf("retired tableMonitorReady field must be gone: %#v", payload)
	}
}

func TestHealthWiresProxyLatencyStatus(t *testing.T) {
	var running atomic.Bool
	running.Store(true)
	now := time.Date(2026, 8, 21, 15, 0, 0, 0, time.UTC)
	status := func() proxylatency.RunnerStatus {
		return proxylatency.RunnerStatus{OwnerHeld: true, LastCycleAt: now, LastSuccess: now, Inputs: 2, Executed: 2}
	}
	handler := healthHandler(healthDeps{
		OwnerMode:             ownermode.Active,
		RuntimeRunning:        &running,
		ProxyLatencyEnabled:   true,
		ProxyLatencyReady:     func() bool { return true },
		ProxyLatencyStatus:    status,
	})
	record := httptest.NewRecorder()
	handler.ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/health", nil))
	var payload map[string]any
	if record.Code != http.StatusOK || json.Unmarshal(record.Body.Bytes(), &payload) != nil {
		t.Fatalf("health status=%d body=%s", record.Code, record.Body.String())
	}
	if payload["proxyLatencyEnabled"] != true || payload["proxyLatencyOwnerHeld"] != true || payload["proxyLatencyExecuted"] != float64(2) || payload["proxyLatencyLastError"] != "" {
		t.Fatalf("missing proxy latency health fields: %#v", payload)
	}
}

func TestHealthKeepsProxyLatencyFailureVisible(t *testing.T) {
	var running atomic.Bool
	running.Store(true)
	status := func() proxylatency.RunnerStatus { return proxylatency.RunnerStatus{LastError: "owner lease lost"} }
	handler := healthHandler(healthDeps{
		OwnerMode:           ownermode.Active,
		RuntimeRunning:      &running,
		ProxyLatencyEnabled: true,
		ProxyLatencyReady:   func() bool { return false },
		ProxyLatencyStatus:  status,
	})
	record := httptest.NewRecorder()
	handler.ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/health", nil))
	var payload map[string]any
	if err := json.Unmarshal(record.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["ready"] != false || payload["proxyLatencyReady"] != false || payload["proxyLatencyLastError"] != "owner lease lost" {
		t.Fatalf("failed proxy latency state was not exposed: %#v", payload)
	}
}

func TestHealthDisabledProxyLatencyDoesNotBlock(t *testing.T) {
	var running atomic.Bool
	running.Store(true)
	handler := healthHandler(healthDeps{
		OwnerMode:           ownermode.Active,
		RuntimeRunning:      &running,
		ProxyLatencyReady:   func() bool { return false },
		ProxyLatencyStatus:  func() proxylatency.RunnerStatus { return proxylatency.RunnerStatus{} },
	})
	record := httptest.NewRecorder()
	handler.ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/health", nil))
	var payload map[string]any
	if err := json.Unmarshal(record.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["ready"] != true || payload["proxyLatencyEnabled"] != false || payload["proxyLatencyReady"] != true {
		t.Fatalf("disabled proxy latency unexpectedly blocked health: %#v", payload)
	}
}

func TestHealthUsesAtomicProxyLatencySnapshot(t *testing.T) {
	var running atomic.Bool
	running.Store(true)
	status := func() proxylatency.RunnerStatus { return proxylatency.RunnerStatus{LastError: "owner lease lost"} }
	snapshot := func() (proxylatency.RunnerStatus, bool) { return status(), false }
	handler := healthHandler(healthDeps{
		OwnerMode:            ownermode.Active,
		RuntimeRunning:       &running,
		ProxyLatencyEnabled:  true,
		ProxyLatencyReady:    func() bool { return true },
		ProxyLatencyStatus:   status,
		ProxyLatencySnapshot: snapshot,
	})
	record := httptest.NewRecorder()
	handler.ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/health", nil))
	var payload map[string]any
	if err := json.Unmarshal(record.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["ready"] != false || payload["proxyLatencyReady"] != false || payload["proxyLatencyLastError"] != "owner lease lost" {
		t.Fatalf("health did not use atomic proxy snapshot: %#v", payload)
	}
}

func TestJobsHTTPHandlerDoesNotExposeRetiredProxyLatencyBridge(t *testing.T) {
	var running atomic.Bool
	running.Store(true)
	handler := jobsHTTPHandler(jobsHTTPDeps{OwnerMode: ownermode.Active, RuntimeRunning: &running})
	record := httptest.NewRecorder()
	handler.ServeHTTP(record, httptest.NewRequest(http.MethodPost, "/proxy-latency/manual", nil))
	if record.Code != http.StatusNotFound {
		t.Fatalf("retired J3a bridge status=%d body=%s", record.Code, record.Body.String())
	}
}

func TestJobsHTTPHandlerExposesPromMetrics(t *testing.T) {
	var running atomic.Bool
	registry := prommetrics.NewRegistry()
	handler := jobsHTTPHandler(jobsHTTPDeps{OwnerMode: ownermode.Active, RuntimeRunning: &running, MetricsHandler: registry.Handler()})
	record := httptest.NewRecorder()
	handler.ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/__aisys__/metrics", nil))
	if record.Code != http.StatusOK {
		t.Fatalf("metrics status=%d body=%s", record.Code, record.Body.String())
	}
	// Go + process 收集器随 NewRegistry 注册：至少暴露 go_* 与 process_* 序列。
	if !strings.Contains(record.Body.String(), "go_goroutines") || !strings.Contains(record.Body.String(), "process_start_time_seconds") {
		t.Fatalf("metrics body must expose go/process collectors: %s", record.Body.String())
	}
}

// TestJobsHTTPHandlerDoesNotExposeGoRuntimeTrend is the 去跨进程战役第三刀
// regression: the TrendHandler route disappears from the jobs health listener
// together with the gometricsstore package; 2026-10-14 起 go-runtime-trend 读族
// 与 gometrics 采样链一并退役（无 in-process 回读，进程指标走 Prometheus）。
func TestJobsHTTPHandlerDoesNotExposeGoRuntimeTrend(t *testing.T) {
	var running atomic.Bool
	handler := jobsHTTPHandler(jobsHTTPDeps{OwnerMode: ownermode.Active, RuntimeRunning: &running})
	record := httptest.NewRecorder()
	handler.ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/__aisys__/api/stats/go-runtime-trend?from=2026-09-01T00:00:00Z&to=2026-09-02T00:00:00Z", nil))
	if record.Code != http.StatusNotFound {
		t.Fatalf("retired go-runtime-trend route status=%d body=%s", record.Code, record.Body.String())
	}
}

// TestJobsHTTPHandlerForwardsWorkerFieldsToHealth 锁定显式依赖集重写后的
// readiness 直达语义：workerEnabled 恒 true，worker readiness gate 与快照
// 到达 /health；/__aiinternal__ 派发路由保持消失（去跨进程战役第二刀：
// 健康检查派发改走 DB outbox）。
func TestJobsHTTPHandlerForwardsWorkerFieldsToHealth(t *testing.T) {
	var running atomic.Bool
	running.Store(true)
	handler := jobsHTTPHandler(jobsHTTPDeps{
		OwnerMode:      ownermode.Active,
		RuntimeRunning: &running,
		WorkerReady:    func() bool { return false },
		WorkerStatus:   func() map[string]any { return map[string]any{"wiredJobs": []string{"account-balance-refresh"}} },
	})

	record := httptest.NewRecorder()
	handler.ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/health", nil))
	if record.Code != http.StatusOK {
		t.Fatalf("health status=%d body=%s", record.Code, record.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(record.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["workerEnabled"] != true {
		t.Fatalf("workerEnabled must reach /health: %#v", payload)
	}
	if payload["workerReady"] != false {
		t.Fatalf("worker readiness gate must reach /health (workerReady=false wired): %#v", payload)
	}
	if payload["ready"] != false {
		t.Fatalf("a not-ready wired worker must block overall readiness: %#v", payload)
	}
	worker, ok := payload["worker"].(map[string]any)
	if !ok || len(worker["wiredJobs"].([]any)) != 1 {
		t.Fatalf("worker snapshot must reach /health: %#v", payload["worker"])
	}

	// The /__aiinternal__ route family must be gone entirely: the health-check
	// dispatch rides the DB outbox channel now, and the jobs health listener
	// serves only /health and the /__aisys__/ surface.
	dispatchRecord := httptest.NewRecorder()
	handler.ServeHTTP(dispatchRecord, httptest.NewRequest(http.MethodPost, "/__aiinternal__/v1/account-health-check/dispatch", nil))
	if dispatchRecord.Code != http.StatusNotFound {
		t.Fatalf("/__aiinternal__ dispatch route must be gone (404), got %d", dispatchRecord.Code)
	}
}

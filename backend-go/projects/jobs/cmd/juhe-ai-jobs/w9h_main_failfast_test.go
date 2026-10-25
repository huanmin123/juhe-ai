package main

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/proxylatency"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accountbalance"
	"github.com/huanminabc/juhe-ai/backend-go-platform/ownermode"
)

// w9h_main_failfast_test.go 复用 wg_main_e2e_test.go 的 main() 子进程协议，
// 覆盖组合根 config-load fail-fast 臂（fail() → os.Exit(2)，只能在子进程
// 运行）。每个臂用基线 owner env 加一条 env 覆盖驱动。

const w9hFailFastTimeout = 90 * time.Second

func TestW9HManualOutcomeAndHandover(t *testing.T) {
	if got := manualOutcome(accountbalance.StatusUnsupported); got != "unsupported" {
		t.Fatalf("unsupported=%q", got)
	}
	if got := manualOutcome(accountbalance.StatusFresh); got != "refreshed" {
		t.Fatalf("fresh=%q", got)
	}
	if got := manualOutcome(accountbalance.StatusUnlimited); got != "refreshed" {
		t.Fatalf("unlimited=%q", got)
	}
	if got := manualOutcome(accountbalance.StatusFailed); got != "failed" {
		t.Fatalf("failed=%q", got)
	}
	input := accountbalance.Input{AccountID: "acc-w9h", SystemAccountID: "sys_admin", ConfigRevision: 3, Trigger: accountbalance.TriggerManual}
	snapshot := accountbalance.Snapshot{}
	next := time.Date(2026, 9, 6, 13, 0, 0, 0, time.UTC)
	// Manual trigger keeps the result lean.
	result := manualHandoverResult(input, snapshot, &next, true, "refreshed")
	inner := result["result"].(map[string]any)
	if inner["committed"] != true || inner["outcome"] != "refreshed" {
		t.Fatalf("manual result=%v", result)
	}
	if _, hasExpected := inner["expectedNextRefreshAt"]; hasExpected {
		t.Fatalf("manual trigger must omit expectedNextRefreshAt: %v", inner)
	}
	// Non-manual trigger carries the expected-next-refresh echo.
	nonManual := input
	nonManual.Trigger = accountbalance.TriggerFirstProbe
	nonManualResult := manualHandoverResult(nonManual, snapshot, &next, false, "failed")
	nonManualInner := nonManualResult["result"].(map[string]any)
	if _, hasExpected := nonManualInner["expectedNextRefreshAt"]; !hasExpected {
		t.Fatalf("periodic trigger must echo expectedNextRefreshAt: %v", nonManualInner)
	}
	// A nil next refresh pointer renders as a typed-nil JSON null.
	nilNext := manualHandoverResult(input, snapshot, nil, true, "refreshed")
	nilInner := nilNext["result"].(map[string]any)
	if pointer, ok := nilInner["nextRefreshAfter"].(*time.Time); !ok || pointer != nil {
		t.Fatalf("nil nextRefreshAfter expected, got %v", nilInner["nextRefreshAfter"])
	}
}

// jobsHTTPHandler 全量依赖装配的健康响应臂（worker 槽位此前只在部分组合下
// 被覆盖；not-ready 各臂经显式 deps 字段锁定）。
func TestW9HJobsHTTPHandlerFieldArms(t *testing.T) {
	running := &atomic.Bool{}
	running.Store(true)
	handler := jobsHTTPHandler(jobsHTTPDeps{
		OwnerMode:             ownermode.Active,
		RuntimeRunning:        running,
		AccountHealthEnabled:  true,
		AccountHealthReady:    func() bool { return false }, // accountHealth enabled + not ready
		AccountBalanceEnabled: true,
		AccountBalanceReady:   func() bool { return true }, // accountBalance enabled + ready, no service
		ProxyLatencyEnabled:   true,
		ProxyLatencyReady:     func() bool { return false }, // proxyLatency enabled + not ready
		ProxyLatencyStatus:    func() proxylatency.RunnerStatus { return proxylatency.RunnerStatus{} },
		ProxyLatencySnapshot:  func() (proxylatency.RunnerStatus, bool) { return proxylatency.RunnerStatus{}, false },
		WorkerReady:           func() bool { return false }, // worker not ready
		WorkerStatus:          func() map[string]any { return map[string]any{"cycle": "w9h"} },
	})
	record := httptest.NewRecorder()
	handler.ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/health", nil))
	if record.Code != http.StatusOK {
		t.Fatalf("health status=%d", record.Code)
	}
}

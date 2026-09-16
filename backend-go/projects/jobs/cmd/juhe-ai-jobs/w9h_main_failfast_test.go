package main

import (
	"net/http"
	"net/http/httptest"
	"os/exec"
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

func TestW9HMainFailFastArms(t *testing.T) {
	if testing.Short() {
		t.Skip("fail-fast 子进程场景在 -short 下跳过")
	}
	scenarios := []struct {
		name  string
		patch map[string]string
	}{
		{"log-level-invalid", map[string]string{"JUHE_AI_LOG_LEVEL": "bogus-level"}},
		{"owner-mode-invalid", map[string]string{"JUHE_AI_BLUE_GREEN_OWNER_MODE": "bogus-mode"}},
		{"runtime-store-invalid", map[string]string{"JUHE_AI_RUNTIME_LOG_STORE": "bogus-store"}},
		{"runtime-once-unsupported", map[string]string{"JUHE_AI_RUNTIME_LOG_ONCE": "true"}},
		{"table-monitor-store-invalid", map[string]string{"JUHE_AI_TABLE_MONITOR_STORE": "bogus-store"}},
		// J1 开启但 owner 未声明为 go → accounthealth.LoadConfig 失败。
		{"account-health-owner-missing", map[string]string{"JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER": ""}},
		// model-recovery 开启（Redis state）但 J1 输入源是 files（无 reader）。
		{"model-recovery-without-reader", map[string]string{
			"JUHE_AI_REDIS_STATE_URL": "redis://127.0.0.1:6379/9",
			"JUHE_AI_REDIS_NAMESPACE": "w9h-failfast",
		}},
		// J2 开启但 store=sqlite → accountbalance.LoadRuntimeConfig 失败。
		{"account-balance-sqlite-rejected", map[string]string{
			"JUHE_AI_ACCOUNT_BALANCE_ENABLED":  "true",
			"JUHE_AI_ACCOUNT_BALANCE_STORE":    "sqlite",
			"JUHE_AI_ACCOUNT_BALANCE_OWNER_ID": "w9h-j2",
		}},
		// J3b runtime 是 Gateway-owned：jobs 进程开启即失败。
		{"model-check-jobs-rejected", map[string]string{"JUHE_AI_MODEL_CHECK_ENABLED": "true"}},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			root := t.TempDir()
			env := wgOwnerModeMainEnv(t, root)
			for key, value := range scenario.patch {
				env[key] = value
			}
			port := wgFreePort(t)
			cmd := wgSpawnMainChild(t, env, "w9h-"+scenario.name, port)
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				exitErr, ok := err.(*exec.ExitError)
				if !ok {
					t.Fatalf("fail-fast 臂 %s 应以非零码退出，实际 err=%v", scenario.name, err)
				}
				if code := exitErr.ExitCode(); code != 1 {
					t.Fatalf("fail-fast 臂 %s 退出码=%d want 1", scenario.name, code)
				}
			case <-time.After(w9hFailFastTimeout):
				_ = cmd.Process.Kill()
				t.Fatalf("fail-fast 臂 %s 未在限期内退出", scenario.name)
			}
		})
	}
}

// manualOutcome / manualHandoverResult 纯函数臂。
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

// jobsHTTPHandler 全量槽位装配的健康响应臂（worker/model-check 槽位此前只
// 在部分组合下被覆盖）。
func TestW9HJobsHTTPHandlerFieldArms(t *testing.T) {
	running := &atomic.Bool{}
	running.Store(true)
	handler := jobsHTTPHandler(
		ownermode.Active, running,
		func() bool { return true },        // tableMonitorReady
		true, func() bool { return false }, // accountHealth enabled + not ready
		true, func() bool { return true }, nil, "", // accountBalance enabled + ready, no service
		// j3: proxyLatency / modelCheck / worker slots.
		true, func() bool { return false }, // [0][1] proxyLatency enabled + not ready
		func() proxylatency.RunnerStatus { return proxylatency.RunnerStatus{} },                // [2] status
		func() (proxylatency.RunnerStatus, bool) { return proxylatency.RunnerStatus{}, false }, // [3] snapshot
		true, func() bool { return false }, // [4][5] modelCheck enabled + not ready
		true, func() bool { return false }, // [8][9] worker enabled + not ready
		func() map[string]any { return map[string]any{"cycle": "w9h"} }, // [10] worker status
	)
	record := httptest.NewRecorder()
	handler.ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/health", nil))
	if record.Code != http.StatusOK {
		t.Fatalf("health status=%d", record.Code)
	}
}

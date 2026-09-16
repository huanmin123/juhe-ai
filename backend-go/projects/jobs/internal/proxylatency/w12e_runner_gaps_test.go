package proxylatency

import (
	"context"
	"crypto/sha256"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// w12e_runner_gaps_test.go 补齐 Runner/Executor 剩余臂：Run 主循环的释放
// 与等待分支、RunManual 全链路（含出站 IP 采集）、runOwned 续租竞争、
// runCycle worker 回退路径，以及 ExecuteIssuedInput 的窗口/门/释放失败臂。

// TestW12EReleaseHelpers：bounded 释放钩子缺失与 joinReleaseFailure。
func TestW12EReleaseHelpers(t *testing.T) {
	if err := (&Runner{}).releaseOwnerLeaseBounded(context.Background(), OwnerLease{}); err == nil || !strings.Contains(err.Error(), "owner lease release hook unavailable") {
		t.Fatalf("owner release 钩子缺失应报错: %v", err)
	}
	if err := (&Runner{}).releaseProxyLeaseBounded(context.Background(), ProxyLease{}); err == nil || !strings.Contains(err.Error(), "proxy lease release hook unavailable") {
		t.Fatalf("proxy release 钩子缺失应报错: %v", err)
	}
	base := errors.New("w12e base")
	wrapped := errors.New("w12e wrapped")
	if got := joinReleaseFailure(nil, "unit", wrapped); got == nil || !strings.Contains(got.Error(), "J3a unit release failed") {
		t.Fatalf("无既有错误时应返回包装错误: %v", got)
	}
	if got := joinReleaseFailure(base, "unit", wrapped); got == nil || !strings.Contains(got.Error(), base.Error()) || !strings.Contains(got.Error(), wrapped.Error()) {
		t.Fatalf("应 join 两个错误: %v", got)
	}
	if got := joinReleaseFailure(base, "unit", nil); got != base {
		t.Fatalf("释放成功应保留既有错误: %v", got)
	}
}

// TestW12ERunContextCancelled：Run 首轮即感知取消。
func TestW12ERunContextCancelled(t *testing.T) {
	cfg := testRuntimeConfig(t)
	store, err := OpenStore(cfg.Store)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runner := NewRunner(cfg, store, fakeInputReader{}, nil)
	if err := runner.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("已取消 ctx 应直接返回: %v", err)
	}
}

// TestW12ERunAcquireContentionLoops：acquire 失败/未获得时的等待续跑分支。
func TestW12ERunAcquireContentionLoops(t *testing.T) {
	cfg := testRuntimeConfig(t)
	cfg.Interval = 5 * time.Millisecond
	store, err := OpenStore(cfg.Store)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	runner := NewRunner(cfg, store, fakeInputReader{}, nil)
	calls := 0
	runner.acquireOwnerLease = func(context.Context, string, time.Duration) (OwnerLease, bool, error) {
		calls++
		if calls%2 == 1 {
			return OwnerLease{}, false, errors.New("w12e acquire boom")
		}
		return OwnerLease{}, false, nil
	}
	if err := runner.Run(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("应等到 ctx 超时: %v", err)
	}
	if calls < 4 {
		t.Fatalf("acquire 循环次数过少: %d", calls)
	}
}

// TestW12ERunReleaseErrorJoins：runOwned 错误与 owner 释放错误合并。
func TestW12ERunReleaseErrorJoins(t *testing.T) {
	cfg := testRuntimeConfig(t)
	cfg.Interval = 5 * time.Millisecond
	store, err := OpenStore(cfg.Store)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runErr := errors.New("w12e cycle fail")
	releaseErr := errors.New("w12e release fail")
	runner := NewRunner(cfg, store, fakeInputReader{}, nil)
	runner.runOwnedFn = func(context.Context, OwnerLease) error { return runErr }
	runner.releaseOwnerLease = func(context.Context, OwnerLease) error { return releaseErr }
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := runner.Run(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("应合并 run/release 错误并循环到超时: %v", err)
	}
	if !strings.Contains(runner.Status().LastError, releaseErr.Error()) {
		t.Fatalf("释放错误应记录: %+v", runner.Status())
	}
}

// TestW12ERunWaitCancelledAfterCycle：runOwned 成功后等待期间被取消。
func TestW12ERunWaitCancelledAfterCycle(t *testing.T) {
	cfg := testRuntimeConfig(t)
	cfg.Interval = time.Second
	store, err := OpenStore(cfg.Store)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, cancel := context.WithCancel(context.Background())
	runner := NewRunner(cfg, store, fakeInputReader{}, nil)
	runner.runOwnedFn = func(context.Context, OwnerLease) error {
		go func() { time.Sleep(20 * time.Millisecond); cancel() }()
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("等待期取消应返回: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run 未在取消后退出")
	}
}

// TestW12ERunManualGuards：未初始化与 no-target 投影失败臂。
func TestW12ERunManualGuards(t *testing.T) {
	// store 缺失。
	runner := NewRunner(RuntimeConfig{ManualDeadline: time.Second}, nil, nil, nil)
	runner.SetResultProjector(&ResultProjector{})
	if _, err := runner.RunManual(context.Background(), testManualReleaseRequest()); err == nil {
		t.Fatalf("store 缺失应报错")
	}
	// projector 缺失。
	runner2 := NewRunner(RuntimeConfig{ManualDeadline: time.Second}, &Store{}, nil, nil)
	if _, err := runner2.RunManual(context.Background(), testManualReleaseRequest()); err == nil {
		t.Fatalf("projector 缺失应报错")
	}
	// no-target 请求 + 投影事务失败。
	rec := newWFRecorder()
	rec.beginErr = errors.New("w12e no-target begin")
	projector := wfNewPGProjector(wfOpenRecorderDB(t, rec))
	runner3 := NewRunner(RuntimeConfig{ManualDeadline: time.Second}, &Store{}, nil, nil)
	runner3.SetResultProjector(projector)
	noTargets := testManualReleaseRequest()
	noTargets.Targets = nil
	if _, err := runner3.RunManual(context.Background(), noTargets); err == nil {
		t.Fatalf("no-target 投影失败应透传")
	}
}

// TestW12ERunManualDeadlineMSFromRequest：DeadlineMS 收紧执行期限。
func TestW12ERunManualDeadlineMSFromRequest(t *testing.T) {
	request := testManualReleaseRequest()
	request.DeadlineMS = 1000
	runner := NewRunner(RuntimeConfig{ManualDeadline: 25 * time.Second}, &Store{}, nil, nil)
	runner.SetResultProjector(&ResultProjector{})
	runner.acquireOwnerLease = func(context.Context, string, time.Duration) (OwnerLease, bool, error) {
		return OwnerLease{OwnerID: "w12e", FenceToken: 1}, true, nil
	}
	runner.releaseOwnerLease = func(context.Context, OwnerLease) error { return nil }
	runner.acquireProxyLease = func(context.Context, OwnerLease, string, time.Duration) (ProxyLease, bool, error) {
		return ProxyLease{}, false, errors.New("w12e proxy acquire boom")
	}
	// acquireProxy 失败先于 issue/execute，仅要求 DeadlineMS 分支已被读取。
	if _, err := runner.RunManual(context.Background(), request); err == nil {
		t.Fatalf("acquireProxy 失败应透传")
	}
}

// TestW12ERunManualStoreHooksFallback：RunManual 无钩子时回退 Store 方法。
func TestW12ERunManualStoreHooksFallback(t *testing.T) {
	cfg := RuntimeConfig{InstanceID: "w12e-manual", OwnerLease: time.Minute, ProxyLease: time.Minute, ManualDeadline: time.Second, Store: StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "w12e-manual.sqlite3")}}
	store, err := OpenStore(cfg.Store)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runner := &Runner{cfg: cfg, store: store, logger: slog.Default()}
	runner.SetResultProjector(&ResultProjector{})
	request := testManualReleaseRequest()
	// 真实 owner/proxy lease 签发 + 真实 IssueInput + 真实 ExecuteIssuedInput
	// （回退路径），最后 projector 无 store 使投影失败收尾（投影失败不写入
	// Runner 状态，直接返回给调用方）。
	if _, err := runner.RunManual(context.Background(), request); err == nil || !strings.Contains(err.Error(), "J3a Go result projector 未初始化") {
		t.Fatalf("应走到投影失败: %v", err)
	}
}

// TestW12ERunManualIssueAndExecuteFailures：issued input → proxy URL / 执行错误臂。
func TestW12ERunManualIssueAndExecuteFailures(t *testing.T) {
	request := testManualReleaseRequest()
	now := time.Now().UTC()
	// issueInput 失败。
	issueErr := errors.New("w12e issue boom")
	runner := newW12EHookedManualRunner()
	runner.issueInput = func(context.Context, InputDraft) (IssuedInput, error) { return IssuedInput{}, issueErr }
	if _, err := runner.RunManual(context.Background(), request); !errors.Is(err, issueErr) {
		t.Fatalf("issue 失败应透传: %v", err)
	}
	// issued input 的代理配置非法 → proxyURLForIssuedInput 失败。
	runner = newW12EHookedManualRunner()
	runner.issueInput = func(context.Context, InputDraft) (IssuedInput, error) {
		return IssuedInput{RequestID: "w12e-bad-proxy", ProxyID: request.ProxyID, InputVersion: 1, ConfigRevision: request.ConfigRevision, Trigger: TriggerManual, IssuedAt: now, ExpiresAt: now.Add(time.Minute), ProxyType: "http", ProxyHost: "127.0.0.1", ProxyPort: 0, Targets: []Target{{Provider: "openai", ProfileID: "manual-profile", ProbeError: targetProbeErrorInvalidURL}}}, nil
	}
	if _, err := runner.RunManual(context.Background(), request); err == nil || !strings.Contains(err.Error(), "proxy 输入无效") {
		t.Fatalf("非法代理应报错: %v", err)
	}
	// ValidateOutcome 身份不匹配。
	runner = newW12EHookedManualRunner()
	runner.issueInput = func(context.Context, InputDraft) (IssuedInput, error) { return w12eManualIssued(request, now), nil }
	runner.executeIssuedInput = func(context.Context, *Store, OwnerLease, ProxyLease, IssuedInput, ExecutorOptions) (Outcome, bool, error) {
		outcome := w12eManualOutcome(request, now)
		outcome.ProxyID = "other-proxy"
		return outcome, true, nil
	}
	if _, err := runner.RunManual(context.Background(), request); err == nil {
		t.Fatalf("ValidateOutcome 失败应报错")
	}
	// 投影读取 committed outcome 失败。
	rec := newWFRecorder()
	rec.failQuery("WHERE outcome_id=$1 AND committed=TRUE", errors.New("w12e find boom"))
	runner = newW12EHookedManualRunner()
	runner.SetResultProjector(wfNewPGProjector(wfOpenRecorderDB(t, rec)))
	runner.issueInput = func(context.Context, InputDraft) (IssuedInput, error) { return w12eManualIssued(request, now), nil }
	runner.executeIssuedInput = func(context.Context, *Store, OwnerLease, ProxyLease, IssuedInput, ExecutorOptions) (Outcome, bool, error) {
		return w12eManualOutcome(request, now), true, nil
	}
	if _, err := runner.RunManual(context.Background(), request); err == nil {
		t.Fatalf("投影失败应透传")
	}
}

// TestW12ERunManualStaleAndOutbound：stale 处置直接返回报告；applied 后采集出站。
func TestW12ERunManualStaleAndOutbound(t *testing.T) {
	request := testManualReleaseRequest()
	now := time.Now().UTC().Truncate(time.Microsecond)
	scriptCommitted := func(rec *wfRecorder) {
		outcome := w12eManualOutcome(request, now)
		payload, err := json.Marshal(outcome)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256Sum(payload)
		rec.script("WHERE outcome_id=$1 AND committed=TRUE",
			[]string{"outcome_id", "request_id", "proxy_id", "input_version", "config_revision", "trigger", "owner_fence_token", "proxy_fence_token", "observed_at", "stored_at", "payload", "payload_digest"},
			[][]driver.Value{{outcome.OutcomeID, outcome.RequestID, outcome.ProxyID, outcome.InputVersion, outcome.ConfigRevision, string(outcome.Trigger), outcome.OwnerFenceToken, outcome.ProxyFenceToken, outcome.ObservedAt, now, payload, sum}})
	}
	build := func(rec *wfRecorder) *Runner {
		runner := newW12EHookedManualRunner()
		runner.SetResultProjector(wfNewPGProjector(wfOpenRecorderDB(t, rec)))
		runner.issueInput = func(context.Context, InputDraft) (IssuedInput, error) { return w12eManualIssued(request, now), nil }
		runner.executeIssuedInput = func(context.Context, *Store, OwnerLease, ProxyLease, IssuedInput, ExecutorOptions) (Outcome, bool, error) {
			return w12eManualOutcome(request, now), true, nil
		}
		return runner
	}
	// stale：CAS 围栏 revision 不匹配 → 不做出站，直接返回报告。
	rec := newWFRecorder()
	scriptCommitted(rec)
	rec.script("FROM juhe_business.proxy_profiles WHERE id=$1 FOR UPDATE",
		[]string{"id", "updated_at", "last_tested_at"},
		[][]driver.Value{{request.ProxyID, "2000-01-01T00:00:00.000000Z", nil}})
	runner := build(rec)
	report, err := runner.RunManual(context.Background(), request)
	if err != nil || report.ProxyID != request.ProxyID {
		t.Fatalf("stale 处置应返回报告: report=%+v err=%v", report, err)
	}

	// applied + 出站采集成功：fake 转发代理返回 ip-api JSON。
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","query":"203.0.113.7","country":"测试地区"}`))
	}))
	defer proxyServer.Close()
	rec2 := newWFRecorder()
	scriptCommitted(rec2)
	rec2.script("FROM juhe_business.proxy_profiles WHERE id=$1 FOR UPDATE",
		[]string{"id", "updated_at", "last_tested_at"},
		[][]driver.Value{{request.ProxyID, request.ConfigRevision, nil}})
	runner = build(rec2)
	// proxy URL 指向 fake 转发代理（host 只取主机名，端口单独传递）。
	runner.acquireProxyLease = func(context.Context, OwnerLease, string, time.Duration) (ProxyLease, bool, error) {
		return ProxyLease{ProxyID: request.ProxyID, OwnerID: "w12e-owner", FenceToken: 1, LeaseUntil: time.Now().Add(time.Minute)}, true, nil
	}
	runner.issueInput = func(context.Context, InputDraft) (IssuedInput, error) {
		issued := w12eManualIssued(request, now)
		issued.ProxyHost = "127.0.0.1"
		issued.ProxyPort = portOfURL(t, proxyServer.URL)
		return issued, nil
	}
	report2, err := runner.RunManual(context.Background(), request)
	if err != nil {
		t.Fatalf("applied + 出站应成功: %v", err)
	}
	if report2.OutboundIP == "" || report2.OutboundRegion == "" {
		t.Fatalf("应采集到出站 IP/地区: %+v", report2)
	}
}

// TestW12ERunManualOutboundDispositions：出站投影 missing/stale/未命中臂。
func TestW12ERunManualOutboundDispositions(t *testing.T) {
	request := testManualReleaseRequest()
	now := time.Now().UTC().Truncate(time.Microsecond)
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","query":"203.0.113.9"}`))
	}))
	defer proxyServer.Close()
	scriptCommitted := func(rec *wfRecorder) {
		outcome := w12eManualOutcome(request, now)
		payload, err := json.Marshal(outcome)
		if err != nil {
			t.Fatal(err)
		}
		rec.script("WHERE outcome_id=$1 AND committed=TRUE",
			[]string{"outcome_id", "request_id", "proxy_id", "input_version", "config_revision", "trigger", "owner_fence_token", "proxy_fence_token", "observed_at", "stored_at", "payload", "payload_digest"},
			[][]driver.Value{{outcome.OutcomeID, outcome.RequestID, outcome.ProxyID, outcome.InputVersion, outcome.ConfigRevision, string(outcome.Trigger), outcome.OwnerFenceToken, outcome.ProxyFenceToken, outcome.ObservedAt, now, payload, sha256Sum(payload)}})
	}
	build := func(rec *wfRecorder) *Runner {
		runner := newW12EHookedManualRunner()
		runner.SetResultProjector(wfNewPGProjector(wfOpenRecorderDB(t, rec)))
		runner.issueInput = func(context.Context, InputDraft) (IssuedInput, error) {
			issued := w12eManualIssued(request, now)
			issued.ProxyHost = "127.0.0.1"
			issued.ProxyPort = portOfURL(t, proxyServer.URL)
			return issued, nil
		}
		runner.executeIssuedInput = func(context.Context, *Store, OwnerLease, ProxyLease, IssuedInput, ExecutorOptions) (Outcome, bool, error) {
			return w12eManualOutcome(request, now), true, nil
		}
		return runner
	}
	applyFence := func(rec *wfRecorder) {
		rec.script("FROM juhe_business.proxy_profiles WHERE id=$1 FOR UPDATE",
			[]string{"id", "updated_at", "last_tested_at"},
			[][]driver.Value{{request.ProxyID, request.ConfigRevision, nil}})
	}
	// missing：出站 CAS 未命中且代理已删除 → ErrManualProxyMissing。
	rec := newWFRecorder()
	scriptCommitted(rec)
	applyFence(rec)
	rec.scriptExec("UPDATE juhe_business.proxy_profiles SET outbound_ip", 0)
	// 出站阶段的第二次围栏查询无脚本 → ErrNoRows → 代理缺失。
	runner := build(rec)
	if _, err := runner.RunManual(context.Background(), request); !errors.Is(err, ErrManualProxyMissing) {
		t.Fatalf("应返回 ErrManualProxyMissing: %v", err)
	}
	// stale：出站 CAS 未命中且 revision 已漂移 → 返回报告不报错。
	rec2 := newWFRecorder()
	scriptCommitted(rec2)
	applyFence(rec2)
	rec2.scriptExec("UPDATE juhe_business.proxy_profiles SET outbound_ip", 0)
	rec2.script("FROM juhe_business.proxy_profiles WHERE id=$1 FOR UPDATE",
		[]string{"id", "updated_at", "last_tested_at"},
		[][]driver.Value{{request.ProxyID, "2000-01-01T00:00:00.000000Z", nil}})
	runner = build(rec2)
	if _, err := runner.RunManual(context.Background(), request); err != nil {
		t.Fatalf("stale 出站应返回报告: %v", err)
	}
	// CAS 未命中但围栏一致 → 明确错误。
	rec3 := newWFRecorder()
	scriptCommitted(rec3)
	applyFence(rec3)
	rec3.scriptExec("UPDATE juhe_business.proxy_profiles SET outbound_ip", 0)
	rec3.script("FROM juhe_business.proxy_profiles WHERE id=$1 FOR UPDATE",
		[]string{"id", "updated_at", "last_tested_at"},
		[][]driver.Value{{request.ProxyID, request.ConfigRevision, now.Format("2006-01-02T15:04:05.999999Z")}})
	runner = build(rec3)
	if _, err := runner.RunManual(context.Background(), request); err == nil || !strings.Contains(err.Error(), "CAS 未命中") {
		t.Fatalf("出站 CAS 未命中应报错: %v", err)
	}
}

// TestW12ERunOwnedRenewalRace：续租失败与循环错误合并 / 续租失败独立返回。
func TestW12ERunOwnedRenewalRace(t *testing.T) {
	// 场景一：runCycle 失败 + 续租失败 → 合并。
	cfg := testRuntimeConfig(t)
	cfg.OwnerLease = 40 * time.Millisecond
	cfg.Interval = time.Hour
	store, err := OpenStore(cfg.Store)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runner := NewRunner(cfg, store, nil, nil)
	runner.renewOwnerLease = func(context.Context, OwnerLease, time.Duration) error { return ErrOwnerLeaseLost }
	runner.reader = &w12ePollingReader{runner: runner}
	owner, acquired, err := store.AcquireOwnerLease(context.Background(), cfg.InstanceID, cfg.OwnerLease)
	if err != nil || !acquired {
		t.Fatalf("owner lease: %v", err)
	}
	err = runner.runOwned(context.Background(), owner)
	if !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("应返回续租错误: %v", err)
	}
}

// TestW12ERunOwnedRenewalFailWhileIdle：空闲等待期间续租失败 → 返回续租错误。
func TestW12ERunOwnedRenewalFailWhileIdle(t *testing.T) {
	cfg := testRuntimeConfig(t)
	cfg.OwnerLease = 60 * time.Millisecond
	cfg.Interval = 5 * time.Second
	store, err := OpenStore(cfg.Store)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runner := NewRunner(cfg, store, fakeInputReader{}, nil)
	runner.renewOwnerLease = func(context.Context, OwnerLease, time.Duration) error { return ErrOwnerLeaseLost }
	owner, acquired, err := store.AcquireOwnerLease(context.Background(), cfg.InstanceID, cfg.OwnerLease)
	if err != nil || !acquired {
		t.Fatalf("owner lease: %v", err)
	}
	err = runner.runOwned(context.Background(), owner)
	if !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("空闲期续租失败应返回续租错误: %v", err)
	}
}

// TestW12ERunOwnedSecondCycleError：第二循环失败并与续租失败合并。
func TestW12ERunOwnedSecondCycleError(t *testing.T) {
	cfg := testRuntimeConfig(t)
	cfg.OwnerLease = 90 * time.Millisecond
	cfg.Interval = 20 * time.Millisecond
	store, err := OpenStore(cfg.Store)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runner := NewRunner(cfg, store, nil, nil)
	runner.renewOwnerLease = func(context.Context, OwnerLease, time.Duration) error {
		// 第一次续租成功，之后失败：保证第一轮完整跑完。
		runner.renewOwnerLease = func(context.Context, OwnerLease, time.Duration) error { return ErrOwnerLeaseLost }
		return nil
	}
	// 第二次 LoadDue 等到续租错误可见后再失败，保证 join 顺序确定。
	reader := &w12eCountingReader{runner: runner, failOn: 2}
	runner.reader = reader
	owner, acquired, err := store.AcquireOwnerLease(context.Background(), cfg.InstanceID, cfg.OwnerLease)
	if err != nil || !acquired {
		t.Fatalf("owner lease: %v", err)
	}
	err = runner.runOwned(context.Background(), owner)
	if !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("第二循环 + 续租失败应合并: %v", err)
	}
	if reader.loads < 2 {
		t.Fatalf("应进入第二轮 LoadDue: %d", reader.loads)
	}
}

// TestW12ERunOwnedTimerReset：循环成功后重置 timer 并再次运行。
func TestW12ERunOwnedTimerReset(t *testing.T) {
	cfg := testRuntimeConfig(t)
	cfg.OwnerLease = time.Minute
	cfg.Interval = 15 * time.Millisecond
	store, err := OpenStore(cfg.Store)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runner := NewRunner(cfg, store, fakeInputReader{}, nil)
	owner, acquired, err := store.AcquireOwnerLease(context.Background(), cfg.InstanceID, cfg.OwnerLease)
	if err != nil || !acquired {
		t.Fatalf("owner lease: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if err := runner.runOwned(ctx, owner); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("应运行多轮后超时: %v", err)
	}
}

// TestW12ERunCycleWorkerFallbacks：worker 的 acquireProxy/execute 回退与
// 超额领取的提前释放；同时启用 DB 门覆盖 withDB 非空路径。
//
// 编排：target=2，3 个候选，2 个 worker。第 1 个 acquire 立即放行；第 2 个
// 放行等到 1 次释放；第 3 个放行等到 2 次释放 → 领取发生在 started==target
// 之后，必然触发「超额领取 → 立即释放」分支。
func TestW12ERunCycleWorkerFallbacks(t *testing.T) {
	cfg := testRuntimeConfig(t)
	cfg.BatchSize = 2
	cfg.CandidatePoolFactor = 1
	cfg.WorkerConcurrency = 2
	cfg.DBConcurrency = 2
	cfg.DBQueueSize = 4
	store, err := OpenStore(cfg.Store)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runner := &Runner{cfg: cfg, store: store, logger: slog.Default()}
	runner.reader = fakeInputReader{drafts: []InputDraft{testDraft("w12e-wa"), testDraft("w12e-wb"), testDraft("w12e-wc")}}
	// execute 用即时钩子：proxy lease 由钩子签发（不在真实 store 中），
	// 真实执行器会在 AppendOutcome 的 proxy 校验处失败并中止循环。
	runner.executeIssuedInput = func(context.Context, *Store, OwnerLease, ProxyLease, IssuedInput, ExecutorOptions) (Outcome, bool, error) {
		return Outcome{}, true, nil
	}
	var mu sync.Mutex
	acquires, releases := 0, 0
	runner.acquireProxyLease = func(ctx context.Context, owner OwnerLease, proxyID string, duration time.Duration) (ProxyLease, bool, error) {
		mu.Lock()
		acquires++
		n := acquires
		for (n == 2 && releases < 1) || (n == 3 && releases < 2) {
			mu.Unlock()
			time.Sleep(2 * time.Millisecond)
			mu.Lock()
		}
		lease := ProxyLease{ProxyID: proxyID, OwnerID: owner.OwnerID, FenceToken: 1, LeaseUntil: time.Now().Add(time.Minute)}
		mu.Unlock()
		return lease, true, nil
	}
	runner.releaseProxyLease = func(context.Context, ProxyLease) error {
		mu.Lock()
		releases++
		mu.Unlock()
		return nil
	}
	owner, acquired, err := store.AcquireOwnerLease(context.Background(), cfg.InstanceID, cfg.OwnerLease)
	if err != nil || !acquired {
		t.Fatalf("owner lease: %v", err)
	}
	if err := runner.runCycle(context.Background(), owner); err != nil {
		t.Fatalf("runCycle: %v", err)
	}
	status := runner.Status()
	if status.Claimed != 3 || status.Executed != 2 || status.ReleaseFailures != 0 {
		t.Fatalf("超额领取语义: %+v", status)
	}
}

// TestW12ERunCycleRealExecutor：worker 走真实 ExecuteIssuedInput（探测失败
// 也能落一个 outcome），覆盖 execute 回退与 claim 释放。
func TestW12ERunCycleRealExecutor(t *testing.T) {
	cfg := testRuntimeConfig(t)
	store, err := OpenStore(cfg.Store)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	draft := testDraft("w12e-real")
	runner := &Runner{cfg: cfg, store: store, logger: slog.Default()}
	runner.reader = fakeInputReader{drafts: []InputDraft{draft}}
	owner, acquired, err := store.AcquireOwnerLease(context.Background(), cfg.InstanceID, cfg.OwnerLease)
	if err != nil || !acquired {
		t.Fatalf("owner lease: %v", err)
	}
	if err := runner.runCycle(context.Background(), owner); err != nil {
		t.Fatalf("runCycle: %v", err)
	}
	status := runner.Status()
	if status.Executed != 1 || status.Processed != 1 {
		t.Fatalf("真实执行应提交 outcome: %+v", status)
	}
}

// TestW12ERunCycleLoadDueError：候选读取失败 → 记录并返回错误。
func TestW12ERunCycleLoadDueError(t *testing.T) {
	cfg := testRuntimeConfig(t)
	store, err := OpenStore(cfg.Store)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runner := NewRunner(cfg, store, fakeInputReader{err: errors.New("w12e load boom")}, nil)
	owner, acquired, err := store.AcquireOwnerLease(context.Background(), cfg.InstanceID, cfg.OwnerLease)
	if err != nil || !acquired {
		t.Fatalf("owner lease: %v", err)
	}
	if err := runner.runCycle(context.Background(), owner); err == nil {
		t.Fatalf("LoadDue 失败应报错")
	}
}

// ---- executor 剩余臂 ----

// TestW12EExecuteAcquireDBCancelled：DB 门排队期间取消。
func TestW12EExecuteAcquireDBCancelled(t *testing.T) {
	gate := NewDBConcurrencyGate(1, 1)
	release, _, err := gate.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	// 占满 queue。
	ctxQueue, cancelQueue := context.WithCancel(context.Background())
	defer cancelQueue()
	go gate.Acquire(ctxQueue) // 等待 slot，占住 queue 名额
	time.Sleep(10 * time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()
	options := ExecutorOptions{Timeout: time.Second, DBGate: gate}
	if _, err := options.acquireDB(ctx, "w12e"); err == nil {
		t.Fatalf("排队取消应报错")
	}
	issued := testIssuedFor("j3a-w12e-gate")
	if _, _, err := ExecuteIssuedInput(ctx, &Store{}, OwnerLease{}, ProxyLease{}, issued, options); err == nil {
		t.Fatalf("admit 前的门失败应透传")
	}
}

// TestW12EExecuteWindowAndLoopCancels：执行窗口过期 / 循环中取消 / 循环后取消。
func TestW12EExecuteWindowAndLoopCancels(t *testing.T) {
	sqlite, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "w12e-exec.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	defer sqlite.Close()
	owner, _, err := sqlite.AcquireOwnerLease(context.Background(), "w12e-owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	proxy, _, err := sqlite.AcquireProxyLease(context.Background(), owner, "w12e-proxy", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	issued, err := sqlite.IssueInput(context.Background(), testInputDraft(proxy.ProxyID, TriggerPeriodic))
	if err != nil {
		t.Fatal(err)
	}
	// 窗口过期：options.Now 返回过期之后的时间 → ErrInputFence，claim 释放
	// 失败与既有错误合并（defer 分支）。
	sqlite.releaseExecutionClaim = func(context.Context, string, string) error { return errors.New("w12e claim release boom") }
	expiredNow := func() time.Time { return issued.ExpiresAt.Add(time.Minute) }
	if _, _, err := ExecuteIssuedInput(context.Background(), sqlite, owner, proxy, issued, ExecutorOptions{Timeout: time.Second, Now: expiredNow}); !errors.Is(err, ErrInputFence) {
		t.Fatalf("窗口过期应 ErrInputFence: %v", err)
	}
	sqlite.releaseExecutionClaim = nil
	// 循环中取消：两个目标，now() 第一次调用即取消 ctx。
	issued2, err := sqlite.IssueInput(context.Background(), testInputDraft(proxy.ProxyID, TriggerPeriodic))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	nowCancel := func() time.Time {
		calls++
		if calls >= 1 {
			cancel()
		}
		return issued2.IssuedAt.Add(time.Millisecond)
	}
	if _, _, err := ExecuteIssuedInput(ctx, sqlite, owner, proxy, issued2, ExecutorOptions{Timeout: time.Second, Now: nowCancel}); !errors.Is(err, context.Canceled) {
		t.Fatalf("循环取消应透传: %v", err)
	}
	// 循环后取消：单目标，now() 取消后循环正常结束，循环后检查命中。
	issued3, err := sqlite.IssueInput(context.Background(), testInputDraft(proxy.ProxyID, TriggerPeriodic))
	if err != nil {
		t.Fatal(err)
	}
	ctx3, cancel3 := context.WithCancel(context.Background())
	afterCalls := 0
	nowCancelAfter := func() time.Time {
		afterCalls++
		if afterCalls >= 2 {
			cancel3()
		}
		return issued3.IssuedAt.Add(time.Millisecond)
	}
	if _, _, err := ExecuteIssuedInput(ctx3, sqlite, owner, proxy, issued3, ExecutorOptions{Timeout: time.Second, Now: nowCancelAfter}); !errors.Is(err, context.Canceled) {
		t.Fatalf("循环后取消应透传: %v", err)
	}
}

// TestW12EExecuteAppendGateCancelled：append 阶段门排队被取消。
func TestW12EExecuteAppendGateCancelled(t *testing.T) {
	sqlite, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "w12e-exec2.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	defer sqlite.Close()
	owner, _, err := sqlite.AcquireOwnerLease(context.Background(), "w12e-owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	proxy, _, err := sqlite.AcquireProxyLease(context.Background(), owner, "w12e-proxy", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// 目标使用 ProbeError（不触发循环内的 now()，也不发起网络请求），让
	// append 阶段的 acquireDB 撞上取消。ProbeError 目标必须在签发时固化
	// （签发后篡改 Targets 会破坏 digest 一致性校验）。
	draft := testInputDraft(proxy.ProxyID, TriggerPeriodic)
	draft.Targets = []Target{{Provider: "openai", ProfileID: "profile", ProbeError: targetProbeErrorInvalidURL}}
	issued, err := sqlite.IssueInput(context.Background(), draft)
	if err != nil {
		t.Fatal(err)
	}
	gate := NewDBConcurrencyGate(1, 1)
	// admit 阶段正常用门；observedAt 处的 now() 首次调用时把门占满（向
	// slot/queue 信号量发送 token），使 append 阶段的 acquireDB 阻塞，
	// 随后 ctx 取消触发失败。
	drained := false
	nowDrain := func() time.Time {
		if !drained {
			drained = true
			gate.slots <- struct{}{}
			gate.queue <- struct{}{}
		}
		return time.Now().UTC()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	options := ExecutorOptions{Timeout: time.Second, DBGate: gate, Now: nowDrain}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	if _, _, err := ExecuteIssuedInput(ctx, sqlite, owner, proxy, issued, options); err == nil {
		t.Fatalf("append 阶段门失败应透传")
	}
	wg.Wait()
}

// TestW12EDecryptArms：密码 envelope 解密的逐段失败臂。
func TestW12EDecryptArms(t *testing.T) {
	envelope := func(iv, tag, cipher string) CredentialEnvelope {
		return CredentialEnvelope{Kind: "proxy_password", Ciphertext: "v1:" + iv + ":" + tag + ":" + cipher}
	}
	bad := []CredentialEnvelope{
		envelope("$$$", "AAAAAAAAAAAAAAAAAAAAAA", "AAAAAAAAAAAAAAAAAAAAAA"), // iv 解码失败
		envelope("AAAAAAAAAAAAAAAAAAAAAA", "$$$", "AAAAAAAAAAAAAAAAAAAAAA"), // tag 解码失败
		envelope("AAAAAAAAAAAAAAAAAAAAAA", "AAAAAAAAAAAAAAAAAAAAAA", "$$$"), // ciphertext 解码失败
		envelope("short", "AAAAAAAAAAAAAAAAAAAAAA", "AAAA"),                // iv 长度非法
		envelope("AAAAAAAAAAAAAAAAAAAAAA", "short", "AAAA"),                // tag 长度非法
	}
	for index, item := range bad {
		if _, err := decryptProxyPasswordV1("secret", item); err == nil {
			t.Fatalf("envelope[%d] 应解密失败", index)
		}
	}
	if _, err := decryptProxyPasswordV1("", bad[0]); err == nil {
		t.Fatalf("空 secret 应失败")
	}
	// 有效长度但内容非法 → GCM Open 失败。
	if _, err := decryptProxyPasswordV1("secret", envelope("AAAAAAAAAAAAAAAA", "AAAAAAAAAAAAAAAAAAAAAA", "AAAAAA")); err == nil {
		t.Fatalf("GCM Open 失败应报错")
	}
}

// TestW12EProxyURLForIssuedInputInvalidTransport：代理 URL 无法构建 transport。
func TestW12EProxyURLForIssuedInputInvalidTransport(t *testing.T) {
	input := IssuedInput{ProxyType: "http", ProxyHost: "\x00bad", ProxyPort: 1}
	if _, err := proxyURLForIssuedInput(input, "secret"); err == nil || !strings.Contains(err.Error(), "proxy URL 无效") {
		t.Fatalf("非法代理主机应报错: %v", err)
	}
	// password 缺 username。
	input2 := IssuedInput{ProxyType: "http", ProxyHost: "127.0.0.1", ProxyPort: 1, ProxyPassword: &CredentialEnvelope{Kind: "proxy_password", Ciphertext: "v1:a:b:c"}}
	if _, err := proxyURLForIssuedInput(input2, "secret"); err == nil {
		t.Fatalf("password-only 应报错")
	}
}

// ---- 测试助手 ----

func newW12EHookedManualRunner() *Runner {
	runner := NewRunner(RuntimeConfig{ManualDeadline: 25 * time.Second, CredentialSecret: "w12e-secret", InstanceID: "w12e-owner", OwnerLease: time.Minute}, &Store{}, nil, nil)
	runner.SetResultProjector(&ResultProjector{})
	runner.acquireOwnerLease = func(context.Context, string, time.Duration) (OwnerLease, bool, error) {
		return OwnerLease{OwnerID: "w12e-owner", FenceToken: 1}, true, nil
	}
	runner.releaseOwnerLease = func(context.Context, OwnerLease) error { return nil }
	runner.acquireProxyLease = func(context.Context, OwnerLease, string, time.Duration) (ProxyLease, bool, error) {
		return ProxyLease{ProxyID: "manual-release-proxy", OwnerID: "w12e-owner", FenceToken: 1, LeaseUntil: time.Now().Add(time.Minute)}, true, nil
	}
	runner.releaseProxyLease = func(context.Context, ProxyLease) error { return nil }
	return runner
}

func w12eManualIssued(request ManualRequest, now time.Time) IssuedInput {
	return IssuedInput{
		RequestID: "w12e-manual-req", ProxyID: request.ProxyID, InputVersion: 1,
		ConfigRevision: request.ConfigRevision, Trigger: TriggerManual,
		IssuedAt: now, ExpiresAt: now.Add(time.Minute), PolicyVersion: proxyLatencyInputPolicyVersion,
		ProxyType: request.ProxyType, ProxyHost: request.ProxyHost, ProxyPort: request.ProxyPort,
		Targets: []Target{{Provider: "openai", ProfileID: "manual-profile", URL: "http://provider.invalid/"}},
	}
}

func w12eManualOutcome(request ManualRequest, now time.Time) Outcome {
	return Outcome{
		OutcomeID: stableOutcomeID("w12e-manual-req"), RequestID: "w12e-manual-req", ProxyID: request.ProxyID,
		ObservedAt: now, InputVersion: 1, ConfigRevision: request.ConfigRevision, Trigger: TriggerManual,
		OwnerFenceToken: 1, ProxyFenceToken: 1, OverallStatus: OverallPassed,
		Items: []ItemResult{{Provider: "openai", ProfileID: "manual-profile", Status: ItemPassed, HTTPStatus: 200, LatencyMS: 12, Outcome: OutcomeSuccess}},
	}
}

func sha256Sum(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// w12ePollingReader 阻塞到 LastError 出现后返回错误（制造 runCycle 失败）。
type w12ePollingReader struct {
	runner *Runner
}

func (r *w12ePollingReader) LoadDue(context.Context, int) ([]InputDraft, error) {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r.runner.Status().LastError != "" {
			return nil, errors.New("w12e cycle boom")
		}
		time.Sleep(2 * time.Millisecond)
	}
	return nil, errors.New("w12e cycle timeout")
}

// w12eCountingReader 第 N 次 LoadDue 等到续租错误可见后返回错误。
type w12eCountingReader struct {
	runner *Runner
	failOn int
	loads  int
}

func (r *w12eCountingReader) LoadDue(context.Context, int) ([]InputDraft, error) {
	r.loads++
	if r.loads < r.failOn {
		return nil, nil
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r.runner.Status().LastError != "" {
			return nil, errors.New("w12e second cycle boom")
		}
		time.Sleep(2 * time.Millisecond)
	}
	return nil, errors.New("w12e second cycle timeout")
}

func portOfURL(t *testing.T, raw string) int {
	t.Helper()
	index := strings.LastIndex(raw, ":")
	if index < 0 {
		t.Fatalf("URL 缺少端口: %s", raw)
	}
	value := 0
	for _, ch := range raw[index+1:] {
		if ch < '0' || ch > '9' {
			t.Fatalf("端口非法: %s", raw)
		}
		value = value*10 + int(ch-'0')
	}
	return value
}

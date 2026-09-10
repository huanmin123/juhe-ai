package proxylatency

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 本文件覆盖 Runner.RunManual 的完整编排（owner/proxy lease → IssueInput →
// 执行 → 投影 → 出口诊断）与 ExecuteIssuedInput 的端到端执行（本地 httptest
// 同时充当代理与目标，绝不产生真实外网请求）。

func TestWFRunnerRunManualNoTargets(t *testing.T) {
	store := wfOpenJobsStore(t)
	business := wfOpenBusinessDB(t)
	projector := wfNewProjector(t, store, business)
	runner := NewRunner(RuntimeConfig{InstanceID: "wf", OwnerLease: time.Hour, Now: func() time.Time { return wfProjBase }}, store, &wfFakeReader{}, nil)
	runner.SetResultProjector(projector)
	ctx := context.Background()

	request := wfManualRequest()
	request.Targets = nil

	// 代理消失 → not found。
	if _, err := runner.RunManual(ctx, request); !errors.Is(err, ErrManualProxyMissing) {
		t.Fatalf("代理消失 err=%v", err)
	}

	// 无 targets：直接投影 no-provider 报告。
	wfSeedProxyRow(t, business, "p-1", wfProjRevision, nil)
	report, err := runner.RunManual(ctx, request)
	if err != nil {
		t.Fatalf("RunManual no-targets 失败: %v", err)
	}
	if report.ProxyID != "p-1" || report.Status != OverallUnknown || len(report.Items) != 1 {
		t.Fatalf("报告=%+v", report)
	}
}

func TestWFRunnerRunManualFullCycle(t *testing.T) {
	store := wfOpenJobsStore(t)
	business := wfOpenBusinessDB(t)
	projector := wfNewProjector(t, store, business)
	runner := NewRunner(RuntimeConfig{InstanceID: "wf", OwnerLease: time.Hour, ProxyLease: time.Hour, CredentialSecret: "wf-secret"}, store, &wfFakeReader{}, nil)
	runner.SetResultProjector(projector)
	ctx := context.Background()

	request := wfManualRequest()
	request.ProxyHost = "127.0.0.1"
	request.ProxyPort = 1 // 出口诊断会连接被拒绝的本地端口，立即失败且无外网流量。
	wfSeedProxyRow(t, business, "p-1", wfProjRevision, nil)

	// executeIssuedInput 钩子：绕过真实探测，直接提交与 issued 匹配的 outcome。
	runner.executeIssuedInput = func(_ context.Context, store *Store, owner OwnerLease, proxy ProxyLease, issued IssuedInput, _ ExecutorOptions) (Outcome, bool, error) {
		outcome := Outcome{
			OutcomeID: stableOutcomeID(issued.RequestID), RequestID: issued.RequestID, ProxyID: issued.ProxyID,
			ObservedAt: issued.IssuedAt.Add(time.Second), InputVersion: issued.InputVersion, ConfigRevision: issued.ConfigRevision,
			Trigger: issued.Trigger, OwnerFenceToken: owner.FenceToken, ProxyFenceToken: proxy.FenceToken,
			OverallStatus: OverallPassed,
			Items:         []ItemResult{{Provider: "gpt", ProfileID: "profile-gpt", Status: ItemPassed, HTTPStatus: 200, LatencyMS: 88, Outcome: OutcomeSuccess}},
		}
		committed, err := store.AppendOutcome(ctx, owner, proxy, outcome)
		if err != nil {
			return Outcome{}, false, err
		}
		return outcome, committed, nil
	}

	report, err := runner.RunManual(ctx, request)
	if err != nil {
		t.Fatalf("RunManual 失败: %v", err)
	}
	if report.Status != OverallPassed || report.ProxyID != "p-1" || report.BaseLatencyMS == nil || *report.BaseLatencyMS != 88 {
		t.Fatalf("报告=%+v", report)
	}
	status, _, _, testedAt := wfBusinessProxyState(t, business, "p-1")
	if status != string(OverallPassed) || !testedAt.Valid {
		t.Fatalf("业务状态=%s testedAt=%v", status, testedAt)
	}
	if !projector.Ready() {
		t.Fatal("成功投影后必须 Ready")
	}

	if _, err := runner.RunManual(ctx, ManualRequest{SchemaVersion: 9}); err == nil {
		t.Fatal("非法请求必须拒绝")
	}
}

func TestWFExecuteIssuedInputEndToEnd(t *testing.T) {
	// httptest 服务器同时充当正向代理与目标（http 目标以绝对形式转发到代理）。
	probeHits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probeHits++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(server.Close)

	store := wfOpenJobsStore(t)
	ctx := context.Background()
	owner, ok, err := store.AcquireOwnerLease(ctx, "wf-exec", time.Hour)
	if err != nil || !ok {
		t.Fatalf("owner lease ok=%v err=%v", ok, err)
	}
	proxy, ok, err := store.AcquireProxyLease(ctx, owner, "p-exec", time.Hour)
	if err != nil || !ok {
		t.Fatalf("proxy lease ok=%v err=%v", ok, err)
	}
	revision := wfProjRevision
	now := time.Now().UTC()
	draft := InputDraft{
		ProxyID: "p-exec", ConfigRevision: revision, Trigger: TriggerPeriodic,
		IssuedAt: now, ExpiresAt: now.Add(5 * time.Minute), PolicyVersion: proxyLatencyInputPolicyVersion,
		ProxyType: "http", ProxyHost: "127.0.0.1", ProxyPort: portOf(server.URL),
		Targets: []Target{{Provider: "gpt", ProfileID: "profile-gpt", URL: "http://127.0.0.1:" + itoaW(portOf(server.URL)) + "/v1"}},
	}
	issued, err := store.IssueInput(ctx, draft)
	if err != nil {
		t.Fatalf("IssueInput 失败: %v", err)
	}
	admitted, claimToken, replay, err := store.AdmitExecution(ctx, owner, proxy, issued)
	if err != nil || replay != nil || claimToken == "" {
		t.Fatalf("AdmitExecution claim=%q err=%v", claimToken, err)
	}
	// 预占的 claim 释放后，执行器才能完成自己的 admission。
	if err := store.ReleaseExecutionClaim(ctx, issued.RequestID, claimToken); err != nil {
		t.Fatalf("释放 claim 失败: %v", err)
	}

	options := ExecutorOptions{CredentialSecret: "wf-secret", Timeout: 5 * time.Second}
	outcome, committed, runErr := ExecuteIssuedInput(ctx, store, owner, proxy, admitted, options)
	if runErr != nil {
		t.Fatalf("ExecuteIssuedInput 失败: %v", runErr)
	}
	if !committed || outcome.OutcomeID != stableOutcomeID(issued.RequestID) || outcome.OverallStatus != OverallPassed {
		t.Fatalf("outcome=%+v committed=%v", outcome, committed)
	}
	if len(outcome.Items) != 1 || outcome.Items[0].Provider != "gpt" || outcome.Items[0].HTTPStatus != 200 || outcome.Items[0].Status != ItemPassed {
		t.Fatalf("items=%+v", outcome.Items)
	}
	if probeHits == 0 {
		t.Fatal("上游必须被访问过一次")
	}

	// 提交后的重放：不再访问上游，committed=false。
	hitsBefore := probeHits
	replayOutcome, committed2, runErr := ExecuteIssuedInput(ctx, store, owner, proxy, issued, options)
	if runErr != nil || committed2 {
		t.Fatalf("重放 committed=%v err=%v", committed2, runErr)
	}
	if replayOutcome.OutcomeID != outcome.OutcomeID || replayOutcome.RequestID != issued.RequestID {
		t.Fatalf("重放 outcome=%+v", replayOutcome)
	}
	if probeHits != hitsBefore {
		t.Fatal("重放不得访问上游")
	}

	// 基础错误路径。
	if _, _, err := ExecuteIssuedInput(ctx, nil, owner, proxy, issued, options); err == nil {
		t.Fatal("缺 Store 必须报错")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := ExecuteIssuedInput(cancelled, store, owner, proxy, issued, options); !errors.Is(err, context.Canceled) {
		t.Fatalf("ctx 取消 err=%v", err)
	}
	if _, _, err := ExecuteIssuedInput(ctx, store, owner, proxy, issued, ExecutorOptions{Timeout: 0}); err == nil {
		t.Fatal("零超时必须报错")
	}
	if _, _, err := ExecuteIssuedInput(ctx, store, owner, proxy, IssuedInput{RequestID: " ", InputVersion: 1}, options); !errors.Is(err, ErrInputFence) {
		t.Fatalf("非法输入 err=%v", err)
	}
	// 篡改输入：fence 不匹配。
	tampered := issued
	tampered.InputVersion = 99
	if _, _, err := ExecuteIssuedInput(ctx, store, owner, proxy, tampered, options); !errors.Is(err, ErrInputFence) {
		t.Fatalf("篡改输入 err=%v", err)
	}
}

// portOf 从 httptest URL 提取端口。
func portOf(raw string) int {
	start := strings.LastIndex(raw, ":")
	value := raw[start+1:]
	port := 0
	for _, ch := range value {
		port = port*10 + int(ch-'0')
	}
	return port
}

// itoaW 是测试内整数转字符串辅助。
func itoaW(value int) string {
	if value == 0 {
		return "0"
	}
	digits := []byte{}
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

func TestWFRunnerRunManualErrorPaths(t *testing.T) {
	store := wfOpenJobsStore(t)
	business := wfOpenBusinessDB(t)
	projector := wfNewProjector(t, store, business)
	runner := NewRunner(RuntimeConfig{InstanceID: "wf", OwnerLease: time.Hour, ProxyLease: time.Hour, CredentialSecret: "wf-secret"}, store, &wfFakeReader{}, nil)
	runner.SetResultProjector(projector)
	ctx := context.Background()
	request := wfManualRequest()
	request.ProxyHost = "127.0.0.1"
	request.ProxyPort = 1

	// 每个子用例使用独立 owner/proxy，避免租约互相冲突。
	newLease := func(instance, proxyID string) (OwnerLease, ProxyLease) {
		owner, ok, err := store.AcquireOwnerLease(ctx, instance, time.Hour)
		if err != nil || !ok {
			t.Fatalf("owner lease ok=%v err=%v", ok, err)
		}
		proxy, ok, err := store.AcquireProxyLease(ctx, owner, proxyID, time.Hour)
		if err != nil || !ok {
			t.Fatalf("proxy lease ok=%v err=%v", ok, err)
		}
		return owner, proxy
	}

	// owner lease 被占。
	runner.acquireOwnerLease = func(context.Context, string, time.Duration) (OwnerLease, bool, error) {
		return OwnerLease{}, false, nil
	}
	if _, err := runner.RunManual(ctx, request); !errors.Is(err, ErrOwnerLeaseHeld) {
		t.Fatalf("owner 被占 err=%v", err)
	}
	// owner lease 获取错误。
	runner.acquireOwnerLease = func(context.Context, string, time.Duration) (OwnerLease, bool, error) {
		return OwnerLease{}, false, errors.New("owner boom")
	}
	if _, err := runner.RunManual(ctx, request); err == nil || !strings.Contains(err.Error(), "owner boom") {
		t.Fatalf("owner 错误 err=%v", err)
	}

	// proxy lease 被占。
	{
		owner, _ := newLease("wf-err-a", "p-err-a")
		runner.acquireOwnerLease = func(context.Context, string, time.Duration) (OwnerLease, bool, error) {
			return owner, true, nil
		}
		runner.acquireProxyLease = func(context.Context, OwnerLease, string, time.Duration) (ProxyLease, bool, error) {
			return ProxyLease{}, false, nil
		}
		if _, err := runner.RunManual(ctx, request); !errors.Is(err, ErrProxyLeaseHeld) {
			t.Fatalf("proxy 被占 err=%v", err)
		}
	}
	// 签发输入失败。
	{
		owner, proxy := newLease("wf-err-b", "p-err-b")
		runner.acquireOwnerLease = func(context.Context, string, time.Duration) (OwnerLease, bool, error) {
			return owner, true, nil
		}
		runner.acquireProxyLease = func(context.Context, OwnerLease, string, time.Duration) (ProxyLease, bool, error) {
			return proxy, true, nil
		}
		runner.issueInput = func(context.Context, InputDraft) (IssuedInput, error) {
			return IssuedInput{}, errors.New("issue boom")
		}
		if _, err := runner.RunManual(ctx, request); err == nil || !strings.Contains(err.Error(), "issue boom") {
			t.Fatalf("issue 错误 err=%v", err)
		}
	}
	// 执行失败。
	{
		owner, proxy := newLease("wf-err-c", "p-err-c")
		runner.acquireOwnerLease = func(context.Context, string, time.Duration) (OwnerLease, bool, error) {
			return owner, true, nil
		}
		runner.acquireProxyLease = func(context.Context, OwnerLease, string, time.Duration) (ProxyLease, bool, error) {
			return proxy, true, nil
		}
		runner.issueInput = nil
		runner.executeIssuedInput = func(context.Context, *Store, OwnerLease, ProxyLease, IssuedInput, ExecutorOptions) (Outcome, bool, error) {
			return Outcome{}, false, errors.New("execute boom")
		}
		if _, err := runner.RunManual(ctx, request); err == nil || !strings.Contains(err.Error(), "execute boom") {
			t.Fatalf("execute 错误 err=%v", err)
		}
	}
	// 投影失败（receipt 表缺失）。
	{
		owner, proxy := newLease("wf-err-d", "p-err-d")
		runner.acquireOwnerLease = func(context.Context, string, time.Duration) (OwnerLease, bool, error) {
			return owner, true, nil
		}
		runner.acquireProxyLease = func(context.Context, OwnerLease, string, time.Duration) (ProxyLease, bool, error) {
			return proxy, true, nil
		}
		runner.issueInput = nil
		runner.executeIssuedInput = func(_ context.Context, store *Store, owner OwnerLease, proxy ProxyLease, issued IssuedInput, _ ExecutorOptions) (Outcome, bool, error) {
			outcome := Outcome{
				OutcomeID: stableOutcomeID(issued.RequestID), RequestID: issued.RequestID, ProxyID: issued.ProxyID,
				ObservedAt: issued.IssuedAt.Add(time.Second), InputVersion: issued.InputVersion, ConfigRevision: issued.ConfigRevision,
				Trigger: issued.Trigger, OwnerFenceToken: owner.FenceToken, ProxyFenceToken: proxy.FenceToken,
				OverallStatus: OverallPassed,
				Items:         []ItemResult{{Provider: "gpt", ProfileID: "profile-gpt", Status: ItemPassed, HTTPStatus: 200, LatencyMS: 88, Outcome: OutcomeSuccess}},
			}
			committed, err := store.AppendOutcome(ctx, owner, proxy, outcome)
			if err != nil {
				return Outcome{}, false, err
			}
			return outcome, committed, nil
		}
		if _, err := business.Exec(`DROP TABLE proxy_latency_projection_receipts`); err != nil {
			t.Fatalf("准备投影失败: %v", err)
		}
		if _, err := runner.RunManual(ctx, request); err == nil {
			t.Fatal("投影失败必须传播")
		}
	}
}

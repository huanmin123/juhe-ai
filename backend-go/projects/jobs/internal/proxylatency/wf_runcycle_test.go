package proxylatency

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// 本文件覆盖 Runner.runCycle 的完整工作循环（读候选 → 领代理 → 签发 →
// 执行 → 投影 → 释放）的成功与失败路径，以及 AppendOutcome 的重放/冲突
// 幂等语义。

// wfCycleDraft 构造一个可用 store 正常签发的输入草稿。
func wfCycleDraft(proxyID string) InputDraft {
	return InputDraft{
		ProxyID: proxyID, ConfigRevision: wfProjRevision, Trigger: TriggerPeriodic,
		IssuedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(5 * time.Minute),
		PolicyVersion: proxyLatencyInputPolicyVersion,
		ProxyType:     "http", ProxyHost: "127.0.0.1", ProxyPort: 1,
		Targets: []Target{{Provider: "gpt", ProfileID: "profile-gpt", URL: "https://api.openai.com/v1"}},
	}
}

// wfCycleCommitOutcome 由 issued 构造可提交的 outcome 并写入 store。
func wfCycleCommitOutcome(t *testing.T, store *Store, owner OwnerLease, proxy ProxyLease, issued IssuedInput) (Outcome, bool) {
	t.Helper()
	outcome := Outcome{
		OutcomeID: stableOutcomeID(issued.RequestID), RequestID: issued.RequestID, ProxyID: issued.ProxyID,
		ObservedAt: issued.IssuedAt.Add(time.Second), InputVersion: issued.InputVersion, ConfigRevision: issued.ConfigRevision,
		Trigger: issued.Trigger, OwnerFenceToken: owner.FenceToken, ProxyFenceToken: proxy.FenceToken,
		OverallStatus: OverallPassed,
		Items:         []ItemResult{{Provider: "gpt", ProfileID: "profile-gpt", Status: ItemPassed, HTTPStatus: 200, LatencyMS: 42, Outcome: OutcomeSuccess}},
	}
	committed, err := store.AppendOutcome(context.Background(), owner, proxy, outcome)
	if err != nil {
		t.Fatalf("AppendOutcome 失败: %v", err)
	}
	return outcome, committed
}

func TestWFRunCycleFullCycle(t *testing.T) {
	store := wfOpenJobsStore(t)
	business := wfOpenBusinessDB(t)
	projector := wfNewProjector(t, store, business)
	runner := NewRunner(RuntimeConfig{InstanceID: "wf-cycle", OwnerLease: time.Hour, ProxyLease: time.Hour, CredentialSecret: "wf"}, store, &wfFakeReader{}, nil)
	runner.SetResultProjector(projector)
	ctx := context.Background()

	wfSeedProxyRow(t, business, "p-cycle", wfProjRevision, nil)
	owner, ok, err := store.AcquireOwnerLease(ctx, "wf-cycle", time.Hour)
	if err != nil || !ok {
		t.Fatalf("owner lease ok=%v err=%v", ok, err)
	}
	runner.reader = &wfFakeReader{drafts: []InputDraft{wfCycleDraft("p-cycle")}}
	runner.executeIssuedInput = func(_ context.Context, store *Store, owner OwnerLease, proxy ProxyLease, issued IssuedInput, _ ExecutorOptions) (Outcome, bool, error) {
		outcome, committed := wfCycleCommitOutcome(t, store, owner, proxy, issued)
		return outcome, committed, nil
	}

	if err := runner.runCycle(ctx, owner); err != nil {
		t.Fatalf("runCycle 失败: %v", err)
	}
	status := runner.Status()
	if status.Selected != 1 || status.Target != 1 || status.Claimed != 1 || status.Started != 1 || status.Processed != 1 || status.Executed != 1 {
		t.Fatalf("计数=%+v", status)
	}
	if status.LastError != "" || status.LastSuccess.IsZero() {
		t.Fatalf("成功状态=%+v", status)
	}
	bizStatus, latency, message, testedAt := wfBusinessProxyState(t, business, "p-cycle")
	if bizStatus != string(OverallPassed) || !latency.Valid || latency.Int64 != 42 {
		t.Fatalf("业务投影=%s %v", bizStatus, latency)
	}
	if message.String != "代理质量检测通过" || !testedAt.Valid {
		t.Fatalf("投影消息=%q testedAt=%v", message.String, testedAt)
	}
}

func TestWFRunCycleFailurePaths(t *testing.T) {
	ctx := context.Background()

	// 执行失败：计数为执行失败且 Partial。
	{
		store := wfOpenJobsStore(t)
		runner := NewRunner(RuntimeConfig{InstanceID: "wf-f1", OwnerLease: time.Hour, ProxyLease: time.Hour, ProbeTimeout: 5 * time.Second}, store, &wfFakeReader{}, nil)
		owner, ok, err := store.AcquireOwnerLease(ctx, "wf-f1", time.Hour)
		if err != nil || !ok {
			t.Fatalf("owner lease ok=%v err=%v", ok, err)
		}
		runner.reader = &wfFakeReader{drafts: []InputDraft{wfCycleDraft("p-f1")}}
		hookRan := false
		runner.executeIssuedInput = func(context.Context, *Store, OwnerLease, ProxyLease, IssuedInput, ExecutorOptions) (Outcome, bool, error) {
			hookRan = true
			return Outcome{}, false, errors.New("exec boom")
		}
		if err := runner.runCycle(ctx, owner); err != nil {
			t.Fatalf("非致命失败不得让 runCycle 报错: %v", err)
		}
		if !hookRan {
			t.Fatal("执行钩子必须被调用")
		}
		status := runner.Status()
		if status.ExecutionFailures != 1 || !status.Partial || !strings.Contains(status.LastError, "exec boom") {
			t.Fatalf("状态=%+v", status)
		}
	}

	// 签发失败：失败计数并释放代理。
	{
		store := wfOpenJobsStore(t)
		runner := NewRunner(RuntimeConfig{InstanceID: "wf-f2", OwnerLease: time.Hour, ProxyLease: time.Hour}, store, &wfFakeReader{}, nil)
		owner, ok, err := store.AcquireOwnerLease(ctx, "wf-f2", time.Hour)
		if err != nil || !ok {
			t.Fatalf("owner lease ok=%v err=%v", ok, err)
		}
		// runCycle 直接调用 store.IssueInput（不走 hook）：
		// 用缺 targets 的无效 draft 让真实签发失败。
		badDraft := wfCycleDraft("p-f2")
		badDraft.Targets = nil
		runner.reader = &wfFakeReader{drafts: []InputDraft{badDraft}}
		if err := runner.runCycle(ctx, owner); err != nil {
			t.Fatalf("非致命失败不得让 runCycle 报错: %v", err)
		}
		status := runner.Status()
		if status.ExecutionFailures != 1 || !strings.Contains(status.LastError, "字段无效") {
			t.Fatalf("状态=%+v", status)
		}
	}
	// 致命租约丢失：runCycle 返回错误。
	{
		store := wfOpenJobsStore(t)
		runner := NewRunner(RuntimeConfig{InstanceID: "wf-f3", OwnerLease: time.Hour, ProxyLease: time.Hour, ProbeTimeout: 5 * time.Second}, store, &wfFakeReader{}, nil)
		owner, ok, err := store.AcquireOwnerLease(ctx, "wf-f3", time.Hour)
		if err != nil || !ok {
			t.Fatalf("owner lease ok=%v err=%v", ok, err)
		}
		runner.reader = &wfFakeReader{drafts: []InputDraft{wfCycleDraft("p-f3")}}
		runner.executeIssuedInput = func(context.Context, *Store, OwnerLease, ProxyLease, IssuedInput, ExecutorOptions) (Outcome, bool, error) {
			return Outcome{}, false, ErrProxyLeaseLost
		}
		err = runner.runCycle(ctx, owner)
		if err == nil || !errors.Is(err, ErrProxyLeaseLost) {
			t.Fatalf("致命租约错误 err=%v", err)
		}
	}
	// owner 校验失败：直接短路。
	{
		store := wfOpenJobsStore(t)
		runner := NewRunner(RuntimeConfig{InstanceID: "wf-f4", OwnerLease: time.Hour, ProxyLease: time.Hour}, store, &wfFakeReader{}, nil)
		err := runner.runCycle(ctx, OwnerLease{OwnerID: "ghost", FenceToken: 9})
		if err == nil || !errors.Is(err, ErrOwnerLeaseLost) {
			t.Fatalf("owner 校验 err=%v", err)
		}
	}
}

func TestWFStoreAppendOutcomeReplayAndConflict(t *testing.T) {
	store := wfOpenJobsStore(t)
	ctx := context.Background()
	owner, ok, err := store.AcquireOwnerLease(ctx, "wf-replay", time.Hour)
	if err != nil || !ok {
		t.Fatalf("owner lease ok=%v err=%v", ok, err)
	}
	proxy, ok, err := store.AcquireProxyLease(ctx, owner, "p-replay", time.Hour)
	if err != nil || !ok {
		t.Fatalf("proxy lease ok=%v err=%v", ok, err)
	}
	draft := wfCycleDraft("p-replay")
	issued, err := store.IssueInput(ctx, draft)
	if err != nil {
		t.Fatalf("IssueInput 失败: %v", err)
	}
	outcome := Outcome{
		OutcomeID: stableOutcomeID(issued.RequestID), RequestID: issued.RequestID, ProxyID: "p-replay",
		ObservedAt: issued.IssuedAt.Add(time.Second), InputVersion: issued.InputVersion, ConfigRevision: issued.ConfigRevision,
		Trigger: issued.Trigger, OwnerFenceToken: owner.FenceToken, ProxyFenceToken: proxy.FenceToken,
		OverallStatus: OverallPassed,
		Items:         []ItemResult{{Provider: "gpt", ProfileID: "profile-gpt", Status: ItemPassed, HTTPStatus: 200, LatencyMS: 42, Outcome: OutcomeSuccess}},
	}
	committed, err := store.AppendOutcome(ctx, owner, proxy, outcome)
	if err != nil || !committed {
		t.Fatalf("首次提交 committed=%v err=%v", committed, err)
	}

	// 完全一致的重放：幂等返回 false。
	committed, err = store.AppendOutcome(ctx, owner, proxy, outcome)
	if err != nil || committed {
		t.Fatalf("幂等重放 committed=%v err=%v", committed, err)
	}

	// 同一 request 身份但内容不同 → ErrRequestConflict。
	conflict := outcome
	conflict.Items = []ItemResult{{Provider: "gpt", ProfileID: "profile-gpt", Status: ItemFailed, Outcome: OutcomeUpstreamFailure}}
	if _, err := store.AppendOutcome(ctx, owner, proxy, conflict); !errors.Is(err, ErrRequestConflict) {
		t.Fatalf("冲突重放 err=%v", err)
	}

	// 已提交 outcome 的重放不校验调用方租约（幂等契约，仅首次插入校验 fence）。
	committed, err = store.AppendOutcome(ctx, owner, ProxyLease{ProxyID: "p-replay", OwnerID: "other", FenceToken: 1}, outcome)
	if err != nil || committed {
		t.Fatalf("重放忽略调用方租约 committed=%v err=%v", committed, err)
	}

	// 未提交的新输入 + 租约不匹配（proxy 归属其他 owner）→ 首次插入 fence 拒绝。
	otherProxy, ok, err := store.AcquireProxyLease(ctx, owner, "p-replay-2", time.Hour)
	if err != nil || !ok {
		t.Fatalf("other proxy lease ok=%v err=%v", ok, err)
	}
	fresh := wfCycleDraft("p-replay-2")
	issuedFresh, err := store.IssueInput(ctx, fresh)
	if err != nil {
		t.Fatalf("IssueInput 失败: %v", err)
	}
	freshOutcome := Outcome{
		OutcomeID: stableOutcomeID(issuedFresh.RequestID), RequestID: issuedFresh.RequestID, ProxyID: "p-replay-2",
		ObservedAt: issuedFresh.IssuedAt.Add(time.Second), InputVersion: issuedFresh.InputVersion, ConfigRevision: issuedFresh.ConfigRevision,
		Trigger: issuedFresh.Trigger, OwnerFenceToken: owner.FenceToken, ProxyFenceToken: otherProxy.FenceToken,
		OverallStatus: OverallPassed,
		Items:         []ItemResult{{Provider: "gpt", ProfileID: "profile-gpt", Status: ItemPassed, HTTPStatus: 200, LatencyMS: 42, Outcome: OutcomeSuccess}},
	}
	if _, err := store.AppendOutcome(ctx, owner, ProxyLease{ProxyID: "p-replay-2", OwnerID: "someone-else", FenceToken: 1}, freshOutcome); err == nil {
		t.Fatal("租约不匹配必须拒绝")
	}
}

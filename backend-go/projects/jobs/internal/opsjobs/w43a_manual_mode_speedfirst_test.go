package opsjobs

// W43-A（后台自动探测总开关 W3）：速度优先恢复探针的手动模式边界。
// 契约源 docs/functions/AI账户后台自动探测总开关设计.md §5/§7：恢复探针不运行，
// 且不得按 ineligible-Discard 清理真实流量建立的降级状态（TTL/真实流量自然恢复）。

import (
	"context"
	"errors"
	"testing"
)

// 手动账户候选：走中性顺延（Defer，保留降级状态），不 Discard、不发探针。
func TestW43AManualModeCandidateDefersWithoutProbe(t *testing.T) {
	disabled := false
	probeCalls := 0
	store := &fakeClaimStore{renewed: true}
	source := &fakeCandidateSource{account: &SpeedFirstAccountSummary{
		Status:                 "active",
		Schedulable:            true,
		BackgroundProbeEnabled: &disabled,
	}}
	runner := newTestSpeedFirstRunner(t, store, source, func(context.Context, *SpeedFirstAccountSummary, ProbeCandidate, *ProbeAccountRef) (ProbeResultSnapshot, TransportProbeOutcome) {
		probeCalls++
		return ProbeResultSnapshot{}, TransportProbeOutcome{Kind: ProbeOutcomeUnknown, FailureKind: ProbeFailureTaskFailure}
	})
	completed, err := runner.Run(context.Background(), testCandidate())
	if err != nil || !completed {
		t.Fatalf("completed=%v err=%v", completed, err)
	}
	if probeCalls != 0 {
		t.Fatalf("手动账户探针执行数 = %d，期望 0", probeCalls)
	}
	if store.deferred != 1 {
		t.Fatalf("手动账户必须 Defer 顺延（保留降级状态）: deferred=%d discarded=%d", store.deferred, store.discarded)
	}
	if store.discarded != 0 {
		t.Fatalf("手动账户不得 Discard 真实流量建立的降级状态: discarded=%d", store.discarded)
	}
	if len(store.failures) != 0 || len(store.successes) != 0 {
		t.Fatalf("手动账户不得写探针结果: failures=%v successes=%d", store.failures, len(store.successes))
	}
}

// 开关读取面未带出（nil）：默认路径不变——资格通过后照常探针（flag=1 逐字节一致）。
func TestW43AEnabledDefaultPathStillProbes(t *testing.T) {
	store := &fakeClaimStore{renewed: true}
	source := &fakeCandidateSource{account: &SpeedFirstAccountSummary{Status: "active", Schedulable: true}, candidate: &ProbeAccountRef{AccountID: "acc-1", GroupID: "group-1"}}
	probeCalls := 0
	runner := newTestSpeedFirstRunner(t, store, source, func(context.Context, *SpeedFirstAccountSummary, ProbeCandidate, *ProbeAccountRef) (ProbeResultSnapshot, TransportProbeOutcome) {
		probeCalls++
		return ProbeResultSnapshot{Success: true, FirstTokenMS: int64PtrW43a(100)}, TransportProbeOutcome{Kind: ProbeOutcomeFramingComplete}
	})
	if _, err := runner.Run(context.Background(), testCandidate()); err != nil {
		t.Fatal(err)
	}
	if probeCalls != 1 {
		t.Fatalf("默认路径探针执行数 = %d，期望 1", probeCalls)
	}
}

// Defer 失败按错误上抛（既有错误语义，不静默吞掉）。
func TestW43AManualModeDeferErrorPropagates(t *testing.T) {
	disabled := false
	store := &w43aDeferErrStore{fakeClaimStore: fakeClaimStore{renewed: true}}
	source := &fakeCandidateSource{account: &SpeedFirstAccountSummary{
		Status:                 "active",
		Schedulable:            true,
		BackgroundProbeEnabled: &disabled,
	}}
	runner := newTestSpeedFirstRunner(t, store, source, func(context.Context, *SpeedFirstAccountSummary, ProbeCandidate, *ProbeAccountRef) (ProbeResultSnapshot, TransportProbeOutcome) {
		return ProbeResultSnapshot{}, TransportProbeOutcome{}
	})
	if _, err := runner.Run(context.Background(), testCandidate()); err == nil {
		t.Fatal("Defer 失败必须上抛")
	}
}

type w43aDeferErrStore struct {
	fakeClaimStore
}

func (f *w43aDeferErrStore) Defer(context.Context, ProbeCandidate) (bool, error) {
	return false, errors.New("defer failed")
}

func int64PtrW43a(value int64) *int64 { return &value }

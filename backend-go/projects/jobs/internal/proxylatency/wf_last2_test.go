package proxylatency

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWFStoreUncommittedReplayAndClaimGuards(t *testing.T) {
	store := wfOpenJobsStore(t)
	ctx := context.Background()
	owner, ok, err := store.AcquireOwnerLease(ctx, "wf-uncommitted", time.Hour)
	if err != nil || !ok {
		t.Fatalf("owner lease ok=%v err=%v", ok, err)
	}
	proxy, ok, err := store.AcquireProxyLease(ctx, owner, "p-uncommitted", time.Hour)
	if err != nil || !ok {
		t.Fatalf("proxy lease ok=%v err=%v", ok, err)
	}
	// 已签发但未提交的 input：LoadCommittedOutcome 返回未找到。
	issued, err := store.IssueInput(ctx, wfCycleDraft("p-uncommitted"))
	if err != nil {
		t.Fatalf("IssueInput 失败: %v", err)
	}
	if _, found, err := store.LoadCommittedOutcome(ctx, issued); err != nil || found {
		t.Fatalf("未提交 found=%v err=%v", found, err)
	}

	// outcome 携带未知 claim token：校验阶段必须 ErrInputFence。
	outcome := Outcome{
		OutcomeID: stableOutcomeID(issued.RequestID), RequestID: issued.RequestID, ProxyID: "p-uncommitted",
		ObservedAt: issued.IssuedAt.Add(time.Second), InputVersion: issued.InputVersion, ConfigRevision: issued.ConfigRevision,
		Trigger: issued.Trigger, OwnerFenceToken: owner.FenceToken, ProxyFenceToken: proxy.FenceToken,
		OverallStatus:       OverallPassed,
		Items:               []ItemResult{{Provider: "gpt", ProfileID: "profile-gpt", Status: ItemPassed, HTTPStatus: 200, LatencyMS: 42, Outcome: OutcomeSuccess}},
		executionClaimToken: "bogus-token",
	}
	if _, err := store.AppendOutcome(ctx, owner, proxy, outcome); !errors.Is(err, ErrInputFence) {
		t.Fatalf("未知 claim token err=%v", err)
	}

	// outcome 指向不存在的 proxy lease：verifyProxy → ErrProxyLeaseLost。
	ghostProxy := ProxyLease{ProxyID: "p-ghost", OwnerID: owner.OwnerID, FenceToken: 42, LeaseUntil: time.Now().Add(time.Hour)}
	outcome2 := outcome
	outcome2.executionClaimToken = ""
	outcome2.ProxyID = "p-ghost"
	outcome2.ProxyFenceToken = 42
	if _, err := store.AppendOutcome(ctx, owner, ghostProxy, outcome2); !errors.Is(err, ErrProxyLeaseLost) {
		t.Fatalf("ghost proxy err=%v", err)
	}
}

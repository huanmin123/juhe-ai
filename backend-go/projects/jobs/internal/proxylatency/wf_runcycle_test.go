package proxylatency

import (
	"context"
	"testing"
	"time"
)

// 本文件覆盖 Runner.runCycle 的完整工作循环（读候选 → 领代理 → 签发 →
// 执行 → 投影 → 释放）的成功与失败路径，以及 AppendOutcome 的重放/冲突
// 幂等语义。

// wfCycleDraft 构造一个可用 store 正常签发的输入草稿。IssuedAt/ExpiresAt
// 截断到微秒，与 PG timestamptz 微秒存储精度对齐（SQLite TEXT 臂无精度差异），
// 使 draft 时间经 DB 往返保持稳定。
func wfCycleDraft(proxyID string) InputDraft {
	return InputDraft{
		ProxyID: proxyID, ConfigRevision: wfProjRevision, Trigger: TriggerPeriodic,
		IssuedAt: time.Now().UTC().Truncate(time.Microsecond), ExpiresAt: time.Now().UTC().Truncate(time.Microsecond).Add(5 * time.Minute),
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


package gatewaydispatch

// 合并路由设计 B14/T4：dispatch 侧配额窗口门在 merge 上下文跳过（解析期逐
// 片段批查是权威门）。窗口组为带组级配额的授权组且组配额耗尽时，未跳过的
// 窗口门会否决整池（429），跳过后账户照常进入派发上下文。

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// TestPrepareDispatchAccountsSkipsGroupQuotaWindowGateForMerge 锁定 T4：
// SkipGroupQuotaWindowCheck 置位时，即使窗口门配额端口否决全部账户，账户也
// 不被剔除（首组配额耗尽 + 次组有配额的 merge 请求得以由次组服务）；不置位
// 时维持现状：全池否决 → 429 authorization_quota_exceeded 终局。
func TestPrepareDispatchAccountsSkipsGroupQuotaWindowGateForMerge(t *testing.T) {
	// 现状臂：窗口门生效——配额端口否决全部账户 → 429 终局。
	{
		pipeline, engine, _, _ := newPipeline(t)
		engine.Quota = &quotaGate{denied: func(string) bool { return true }}
		coordinator := &capturingCoordinator{}
		input := dispatchPreparationInput(t, testAccounts("a-1", "a-2"))
		input.RouteCoordinator = coordinator
		result, err := pipeline.PrepareDispatchAccounts(context.Background(), input)
		if err != nil {
			t.Fatalf("PrepareDispatchAccounts: %v", err)
		}
		if result.Outcome != gatewaypreauth.CandidateOutcomeCompleted {
			t.Fatalf("outcome = %s, want completed", result.Outcome)
		}
		if coordinator.failure == nil || coordinator.failure.StatusCode != 429 ||
			coordinator.failure.ErrorCode != "rate_limit_exceeded" ||
			coordinator.failure.ErrorPhase != "quota" {
			t.Fatalf("failure = %#v, want 429 rate_limit_exceeded (quota)", coordinator.failure)
		}
	}
	// merge 臂：窗口门跳过——全部账户保留进入派发上下文，且配额端口不再被
	// 消费（权威门在解析期）。
	{
		pipeline, engine, _, _ := newPipeline(t)
		quotaCalls := 0
		engine.Quota = &quotaGate{denied: func(string) bool {
			quotaCalls++
			return true
		}}
		input := dispatchPreparationInput(t, testAccounts("a-1", "a-2"))
		input.SkipGroupQuotaWindowCheck = true
		result, err := pipeline.PrepareDispatchAccounts(context.Background(), input)
		if err != nil {
			t.Fatalf("PrepareDispatchAccounts (merge): %v", err)
		}
		if result.Outcome != gatewaypreauth.CandidateOutcomeAccounts {
			t.Fatalf("merge outcome = %s, want accounts", result.Outcome)
		}
		if len(result.Accounts) != 2 {
			t.Fatalf("merge accounts = %v, want both kept", accountIDs(result.Accounts))
		}
		if quotaCalls != 0 {
			t.Fatalf("窗口门跳过后配额端口仍被调用 %d 次", quotaCalls)
		}
	}
}

// TestPrepareDispatchAccountsGroupQuotaWindowGateDeniesOnlyDeniedAccounts 维
// 持存量语义回归：窗口门生效且仅部分账户被否决时，只剔除被否决账户。
func TestPrepareDispatchAccountsGroupQuotaWindowGateDeniesOnlyDeniedAccounts(t *testing.T) {
	pipeline, engine, _, _ := newPipeline(t)
	engine.Quota = &quotaGate{denied: func(accountID string) bool { return accountID == "a-1" }}
	input := dispatchPreparationInput(t, testAccounts("a-1", "a-2"))
	result, err := pipeline.PrepareDispatchAccounts(context.Background(), input)
	if err != nil {
		t.Fatalf("PrepareDispatchAccounts: %v", err)
	}
	if result.Outcome != gatewaypreauth.CandidateOutcomeAccounts {
		t.Fatalf("outcome = %s, want accounts", result.Outcome)
	}
	if len(result.Accounts) != 1 || result.Accounts[0].ID != "a-2" {
		t.Fatalf("accounts = %v, want [a-2]", accountIDs(result.Accounts))
	}
}

package main

// w1（单元层）：错误策略效果桥的决策落地——retry_next 短路、硬不可用短路、
// 冷却写与失败停用写（fixture 业务库真实执行 UPDATE）。

import (
	"context"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accountkeystates"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
)

func w1EffectsBridge(t *testing.T, fixture *chainFixture) *chainErrorPolicyEffectsBridge {
	t.Helper()
	keyStates, err := accountkeystates.NewStore(accountkeystates.Config{
		DB: fixture.db, Postgres: false, Secret: "chain-test-secret", Now: time.Now,
		InvalidateRuntimeCache: func(string) {},
	})
	if err != nil {
		t.Fatalf("keyStates = %v", err)
	}
	return &chainErrorPolicyEffectsBridge{db: fixture.db, pg: false, keyStates: keyStates, now: time.Now}
}

func TestW1ApplyAccountErrorPolicyDecision(t *testing.T) {
	fixture := newChainFixture(t)
	// markCooldown/markDisabledByFailure 的 UPDATE 引用生产新增列，fixture
	// 建表语句尚未同步——测试内补列（SQLite ADD COLUMN 幂等前置于用例）。
	for _, column := range []string{
		"last_error_trace_id TEXT", "cooldown_retest_failure_count INTEGER",
		"cooldown_retest_observation_started_at TEXT", "cooldown_retest_last_at TEXT",
		"cooldown_retest_last_status_code INTEGER", "cooldown_retest_generation TEXT", "updated_at TEXT",
	} {
		if _, err := fixture.db.Exec("ALTER TABLE accounts ADD COLUMN " + column); err != nil {
			t.Fatalf("add column %s = %v", column, err)
		}
	}
	bridge := w1EffectsBridge(t, fixture)
	ctx := context.Background()
	account := gatewaydispatch.AccountCandidate{ID: fixture.accountID}
	// retry_next：直接短路，不改状态。
	changed, status, err := bridge.ApplyAccountErrorPolicyDecision(ctx, account,
		accountErrorPolicyDecision{Action: decisionActionRetryNext},
		chainErrorPolicyFailureInput{})
	if err != nil || changed || status != "" {
		t.Fatalf("retry_next = %v, %q, %v", changed, status, err)
	}
	// 活跃账户：临时不可用冷却真实落库（3 秒初始退避）。
	changed, status, err = bridge.ApplyAccountErrorPolicyDecision(ctx, account,
		accountErrorPolicyDecision{Action: decisionActionCooldown, CooldownStatus: cooldownStatusTemporaryUnavailable, RuleName: "临时规则"},
		chainErrorPolicyFailureInput{HasStatusCode: true, StatusCode: 503, ErrorMessage: "上游 503"})
	if err != nil {
		t.Fatalf("cooldown = %v", err)
	}
	if !changed || status != cooldownStatusTemporaryUnavailable {
		t.Fatalf("cooldown changed = %v, status = %q", changed, status)
	}
	// 限流冷却：带显式 CooldownUntil。
	changed, status, err = bridge.ApplyAccountErrorPolicyDecision(ctx, account,
		accountErrorPolicyDecision{Action: decisionActionCooldown, CooldownStatus: cooldownStatusRateLimited, CooldownUntil: "2030-01-01T00:00:00.000Z", RuleSource: "system", RuleName: "限流规则"},
		chainErrorPolicyFailureInput{HasStatusCode: true, StatusCode: 429})
	if err != nil {
		t.Fatalf("rate limited = %v", err)
	}
	// 停用动作：error_disabled 落库。
	changed, status, err = bridge.ApplyAccountErrorPolicyDecision(ctx, account,
		accountErrorPolicyDecision{Action: decisionActionDisable, RuleName: "停用规则"},
		chainErrorPolicyFailureInput{HasStatusCode: true, StatusCode: 500})
	if err != nil || !changed || status != "error" {
		t.Fatalf("disable = %v, %q, %v", changed, status, err)
	}
	// 再冷却：已是硬不可用 → 幂等短路。
	changed, _, err = bridge.ApplyAccountErrorPolicyDecision(ctx, account,
		accountErrorPolicyDecision{Action: decisionActionCooldown, CooldownStatus: cooldownStatusTemporaryUnavailable},
		chainErrorPolicyFailureInput{})
	if err != nil || changed {
		t.Fatalf("post-disable cooldown = %v, %v", changed, err)
	}
}

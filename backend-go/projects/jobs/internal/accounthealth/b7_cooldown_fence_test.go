package accounthealth

import (
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/exactkeyprobe"
)

// b7CooldownErrorOutcome 构造 cooldown_error 终态投影 outcome（B07 收口）：
// 顶层 fence 与 Projection 一致，ExpectedCooldownFence 恒设置，输出 fence 由
// 入参决定（nil 复现已下线的 legacy 无 fence 形态）。cooldown_error 豁免
// next_due（requiresNextDue=false），Outcome 必须匹配 UpstreamFailed。
func b7CooldownErrorOutcome(outputFence *exactkeyprobe.CooldownFence) Outcome {
	observedAt := time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)
	expectedFence := &exactkeyprobe.CooldownFence{ObservationStartedAt: observedAt.Add(-time.Hour), Generation: "gen-b7"}
	return Outcome{
		OutcomeID:        "outcome-b7-cooldown-error",
		RequestID:        "request-b7-cooldown-error",
		AccountID:        "acct-b7",
		Outcome:          exactkeyprobe.OutcomeUpstreamFailed,
		ObservedAt:       observedAt,
		InputVersion:     1,
		ConfigRevision:   5,
		DispatchRevision: 7,
		StatusCode:       503,
		ErrorCode:        "cooldown_retest_neutral_timeout",
		Projection: &Projection{
			TargetAccountID:       "acct-b7",
			TransitionKind:        "cooldown_error",
			InputVersion:          1,
			ConfigRevision:        5,
			DispatchRevision:      7,
			ExpectedAccountStatus: "temporary_unavailable",
			ExpectedCooldownFence: expectedFence,
			CooldownFence:         outputFence,
			Values:                map[string]any{},
		},
	}
}

// TestB7CooldownErrorWithoutOutputFenceRejected 断言无输出 fence 的 legacy
// cooldown_error 在 validateProjection 被拒绝（不再走“清冷却”兼容分支），
// reason 与既有审计文本保持一致。
func TestB7CooldownErrorWithoutOutputFenceRejected(t *testing.T) {
	validation := validateProjection(b7CooldownErrorOutcome(nil))
	if !validation.terminal || validation.disposition != ProjectionRejected {
		t.Fatalf("无输出 fence 的 cooldown_error 必须被拒绝: terminal=%v disposition=%q reason=%q", validation.terminal, validation.disposition, validation.reason)
	}
	if validation.reason != "projection_output_cooldown_fence_missing" {
		t.Fatalf("拒绝 reason 必须是 projection_output_cooldown_fence_missing: %q", validation.reason)
	}
}

// TestB7CooldownErrorWithMatchingFenceValid 断言输出 fence 与期望 fence 为
// 同一不可变 fence 的 cooldown_error 通过 validateProjection（终态冷却按
// setCooldown 投影，而非清空冷却）。
func TestB7CooldownErrorWithMatchingFenceValid(t *testing.T) {
	observedAt := time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)
	fence := &exactkeyprobe.CooldownFence{ObservationStartedAt: observedAt.Add(-time.Hour), Generation: "gen-b7"}
	validation := validateProjection(b7CooldownErrorOutcome(fence))
	if validation.terminal {
		t.Fatalf("带匹配输出 fence 的 cooldown_error 必须通过校验: disposition=%q reason=%q", validation.disposition, validation.reason)
	}
}

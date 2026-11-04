// cooldown_binary_recovery_test.go 锁定 2026-11-03 裁决的冷却恢复二元契约：
// 只有 complete_success（OutcomeSuccess）算成功；其余真实上游尝试结果
// （upstream_http_failure 与 upstream_failure）统一推进失败计数与
// 3/6/12/24/48 秒快速道、60 秒慢速道、12 小时长期道，不按 HTTP 状态码或
// HTTP 失败/传输分类分叉；HTTP 失败结果不得触发 10 分钟有界 transport 终态。
// probe_task_failure（无真实上游尝试）保留旧的 30 秒基数、15 分钟封顶顺延。
package accounthealth

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/exactkeyprobe"
)

// binaryCooldownPrior 构造与输入版本一致的冷却 prior state。
func binaryCooldownPrior(input exactkeyprobe.Input, fence *exactkeyprobe.CooldownFence, failures int) CurrentState {
	return CurrentState{InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, AccountStatus: "temporary_unavailable", FailureCount: failures, CooldownFence: fence}
}

// binaryCooldownOutcome 走 cooldown_retest 决策链并返回 outcome。
func binaryCooldownOutcome(input exactkeyprobe.Input, prior CurrentState, result string, statusCode int, observed time.Time) Outcome {
	outcome := Outcome{Outcome: result, StatusCode: statusCode, ObservedAt: observed}
	applyOutcomeDecision(&outcome, input, prior, true, "cooldown_retest")
	return outcome
}

// 契约 1/2：HTTP 失败结果（含 403 与 503）首次未成功走 3 秒基准快速道，jitter 后
// 落在 [1.5s,4.5s] 且恒不等于基准；按状态码分叉即失败。
func TestCooldownHTTPFailureStatusCodesShareTheInitialFastBackoff(t *testing.T) {
	for _, statusCode := range []int{403, 503} {
		t.Run(fmt.Sprintf("http_%d", statusCode), func(t *testing.T) {
			now := time.Now().UTC()
			fence := &exactkeyprobe.CooldownFence{ObservationStartedAt: now, Generation: fmt.Sprintf("binary-httpFailure-initial-%d", statusCode)}
			input := newCooldownRetestInput(fence, now.Add(-time.Second))
			prior := binaryCooldownPrior(input, fence, 0)
			outcome := binaryCooldownOutcome(input, prior, exactkeyprobe.OutcomeHTTPFailure, statusCode, now)
			if outcome.NextDueAt == nil || !isJitteredWithin(outcome.NextDueAt.Sub(now), 3*time.Second) {
				t.Fatalf("HTTP 失败 HTTP %d 首次未成功必须落 3s 快速道（jitter [1.5s,4.5s] 且不等于基准）: %v", statusCode, outcome.NextDueAt)
			}
			if outcome.FailureCount != 1 || outcome.AccountStatus != "temporary_unavailable" || outcome.ErrorCode != "" {
				t.Fatalf("HTTP 失败 HTTP %d 必须推进失败计数并保持冷却态、不写错误码: %#v", statusCode, outcome)
			}
			if outcome.Projection == nil || outcome.Projection.TransitionKind != "cooldown_defer" {
				t.Fatalf("HTTP 失败 HTTP %d 必须投影 cooldown_defer: %#v", statusCode, outcome.Projection)
			}
			if outcome.CooldownFence == nil || !sameCooldownFence(outcome.CooldownFence, fence) || outcome.Projection.ExpectedCooldownFence == nil || !sameCooldownFence(outcome.Projection.ExpectedCooldownFence, fence) || outcome.Projection.ExpectedAccountStatus != "temporary_unavailable" {
				t.Fatalf("HTTP 失败 HTTP %d 必须保留五元 fence 与期望状态: %#v", statusCode, outcome.Projection)
			}
		})
	}
}

// 契约 3：HTTP 失败第 6 次起进入 60 秒基准慢速道（jitter 后 [30s,90s]）。
func TestCooldownHTTPFailureSixthFailureUsesSixtySecondSlowLane(t *testing.T) {
	now := time.Now().UTC()
	fence := &exactkeyprobe.CooldownFence{ObservationStartedAt: now, Generation: "binary-httpFailure-slow"}
	input := newCooldownRetestInput(fence, now.Add(-time.Second))
	prior := binaryCooldownPrior(input, fence, 5)
	outcome := binaryCooldownOutcome(input, prior, exactkeyprobe.OutcomeHTTPFailure, 503, now)
	if outcome.NextDueAt == nil || !isJitteredWithin(outcome.NextDueAt.Sub(now), time.Minute) {
		t.Fatalf("HTTP 失败第 6 次必须落 60s 慢速道（jitter [30s,90s] 且不等于基准）: %v", outcome.NextDueAt)
	}
	if outcome.FailureCount != 6 || outcome.ErrorCode != "" || outcome.Projection == nil || outcome.Projection.TransitionKind != "cooldown_defer" {
		t.Fatalf("HTTP 失败第 6 次必须计数到 6 并以 cooldown_defer 顺延: %#v", outcome)
	}
}

// 契约 4：HTTP 失败满最大恢复观察窗（默认 12 小时）后进入 1 小时/轮长期道，
// FailureCount 不变、不写长期道错误码；特供跳过降频继续慢速道。
func TestCooldownHTTPFailureLongTermLaneAfterMaxRecovery(t *testing.T) {
	now := time.Now().UTC()
	fence := &exactkeyprobe.CooldownFence{ObservationStartedAt: now.Add(-13 * time.Hour), Generation: "binary-httpFailure-long"}
	input := newCooldownRetestInput(fence, now.Add(-time.Second))
	prior := binaryCooldownPrior(input, fence, 4)
	outcome := binaryCooldownOutcome(input, prior, exactkeyprobe.OutcomeHTTPFailure, 503, now)
	if outcome.NextDueAt == nil || !isJitteredWithin(outcome.NextDueAt.Sub(now), time.Hour) {
		t.Fatalf("HTTP 失败越过 12h 观察窗必须降为 1 小时/轮（jitter [30m,90m]）: %v", outcome.NextDueAt)
	}
	if outcome.FailureCount != 4 || outcome.ErrorCode != "" || outcome.AccountStatus != "temporary_unavailable" || outcome.Projection == nil || outcome.Projection.TransitionKind != "cooldown_defer" {
		t.Fatalf("HTTP 失败长期道必须保持失败计数、冷却态与 cooldown_defer、不写错误码: %#v", outcome)
	}
	if outcome.CooldownFence == nil || !sameCooldownFence(outcome.CooldownFence, fence) {
		t.Fatalf("HTTP 失败长期道必须保留 fence: %#v", outcome.CooldownFence)
	}

	expeditedFence := &exactkeyprobe.CooldownFence{ObservationStartedAt: now.Add(-13 * time.Hour), Generation: "binary-httpFailure-long-expedited"}
	expedited := newCooldownRetestInput(expeditedFence, now.Add(-time.Second))
	expedited.Eligibility.ExpeditedRecovery = true
	expeditedOutcome := binaryCooldownOutcome(expedited, binaryCooldownPrior(expedited, expeditedFence, 5), exactkeyprobe.OutcomeHTTPFailure, 503, now)
	if expeditedOutcome.NextDueAt == nil || !isJitteredWithin(expeditedOutcome.NextDueAt.Sub(now), 15*time.Second) {
		t.Fatalf("特供HTTP 失败跳过长期降频，必须保持 15s 慢速道（jitter [7.5s,22.5s]）: %v", expeditedOutcome.NextDueAt)
	}
	if expeditedOutcome.ErrorCode != "" {
		t.Fatalf("特供HTTP 失败不得写长期道错误码: %q", expeditedOutcome.ErrorCode)
	}
}

// 契约 5：7 天观察终态保持现状——HTTP 失败仍写 cooldown_retest_http_failure_timeout。
func TestCooldownHTTPFailureSevenDayTerminalStaysAuthoritative(t *testing.T) {
	now := time.Now().UTC()
	fence := &exactkeyprobe.CooldownFence{ObservationStartedAt: now.Add(-cooldownObservationTimeout), Generation: "binary-httpFailure-terminal"}
	input := newCooldownRetestInput(fence, now.Add(-time.Second))
	prior := binaryCooldownPrior(input, fence, 3)
	outcome := binaryCooldownOutcome(input, prior, exactkeyprobe.OutcomeHTTPFailure, 503, now)
	if outcome.AccountStatus != "error" || outcome.NextDueAt != nil || outcome.ErrorCode != "cooldown_retest_http_failure_timeout" {
		t.Fatalf("HTTP 失败满 7 天必须收敛 cooldown_retest_http_failure_timeout/error: %#v", outcome)
	}
	if outcome.Projection == nil || outcome.Projection.TransitionKind != "cooldown_error" || outcome.Projection.CooldownFence == nil || !sameCooldownFence(outcome.Projection.CooldownFence, fence) {
		t.Fatalf("HTTP 失败 7 天终态必须投影 cooldown_error 并保留 fence: %#v", outcome.Projection)
	}
	if !strings.Contains(outcome.ErrorMessage, "7 天") {
		t.Fatalf("HTTP 失败终态错误信息必须说明 7 天观察窗: %q", outcome.ErrorMessage)
	}
}

// 契约 6：未开持续探针且已越过 10 分钟有界窗口时，HTTP 失败结果仍按失败慢速道
// 复测，绝不写 cooldown_retest_limited_probe_timeout（有界终态只对 transport
// 证据成立）。
func TestCooldownHTTPFailureNeverTriggersBoundedTransportTerminal(t *testing.T) {
	now := time.Now().UTC()
	fence := &exactkeyprobe.CooldownFence{ObservationStartedAt: now.Add(-11 * time.Minute), Generation: "binary-httpFailure-bounded"}
	input := newCooldownRetestInput(fence, now.Add(-time.Second))
	input.Eligibility.TemporaryUnavailableContinuousProbeEnabled = boolPointer(false)
	prior := binaryCooldownPrior(input, fence, 8)
	if remaining, bounded := boundedCooldownRemaining(input, "temporary_unavailable", fence, now); !bounded || remaining != 0 {
		t.Fatalf("前置条件：该输入必须有界且预算已耗尽（remaining=%s bounded=%t）", remaining, bounded)
	}
	outcome := binaryCooldownOutcome(input, prior, exactkeyprobe.OutcomeHTTPFailure, 503, now)
	if outcome.NextDueAt == nil || !isJitteredWithin(outcome.NextDueAt.Sub(now), time.Minute) {
		t.Fatalf("HTTP 失败越过 10 分钟有界窗口仍必须落 60s 慢速道（不得被剩余 0 截断）: %v", outcome.NextDueAt)
	}
	if outcome.ErrorCode != "" || outcome.AccountStatus != "temporary_unavailable" || outcome.Projection == nil || outcome.Projection.TransitionKind != "cooldown_defer" {
		t.Fatalf("HTTP 失败不得触发有界 transport 终态: %#v", outcome)
	}
	if outcome.FailureCount != 9 {
		t.Fatalf("HTTP 失败必须继续推进失败计数: %d", outcome.FailureCount)
	}
}

// 契约 7：probe_task_failure（无真实上游尝试）保持旧的 30 秒基数慢速顺延，
// 不推进失败计数、不进入长期道、不触发终态。
func TestCooldownTaskFailureKeepsBoundedDeferAndCounters(t *testing.T) {
	now := time.Now().UTC()
	fence := &exactkeyprobe.CooldownFence{ObservationStartedAt: now, Generation: "binary-task-failed"}
	input := newCooldownRetestInput(fence, now.Add(-time.Second))
	started := now.Add(-30 * time.Minute)
	prior := binaryCooldownPrior(input, fence, 4)
	prior.FailureStartedAt = &started
	outcome := binaryCooldownOutcome(input, prior, exactkeyprobe.OutcomeTaskFailed, 0, now)
	if outcome.NextDueAt == nil || !isJitteredWithin(outcome.NextDueAt.Sub(now), 30*time.Second) {
		t.Fatalf("本地任务故障必须保持 30s 基数顺延（jitter [15s,45s] 且不等于基准）: %v", outcome.NextDueAt)
	}
	if outcome.FailureCount != 4 || outcome.FailureStartedAt == nil || !outcome.FailureStartedAt.Equal(started) {
		t.Fatalf("本地任务故障不得改变失败计数与失败起点: %#v", outcome)
	}
	if outcome.ErrorCode != "" || outcome.AccountStatus != "temporary_unavailable" || outcome.Projection == nil || outcome.Projection.TransitionKind != "cooldown_defer" {
		t.Fatalf("本地任务故障必须保持 cooldown_defer 且不终态化: %#v", outcome)
	}
}

// 契约：二元策略同样覆盖 rate_limited——该状态的冷却复测遇到HTTP 失败未成功也按
// 3 秒快速道推进失败计数，不按账户状态分叉。
func TestCooldownHTTPFailureRateLimitedUsesTheSameFastBackoff(t *testing.T) {
	now := time.Now().UTC()
	fence := &exactkeyprobe.CooldownFence{ObservationStartedAt: now, Generation: "binary-httpFailure-rate-limited"}
	input := newCooldownRetestInput(fence, now.Add(-time.Second))
	input.Eligibility.AccountStatus = "rate_limited"
	prior := binaryCooldownPrior(input, fence, 0)
	prior.AccountStatus = "rate_limited"
	outcome := binaryCooldownOutcome(input, prior, exactkeyprobe.OutcomeHTTPFailure, 429, now)
	if outcome.NextDueAt == nil || !isJitteredWithin(outcome.NextDueAt.Sub(now), 3*time.Second) {
		t.Fatalf("rate_limited HTTP 失败首次未成功必须落 3s 快速道（jitter [1.5s,4.5s] 且不等于基准）: %v", outcome.NextDueAt)
	}
	if outcome.FailureCount != 1 || outcome.AccountStatus != "rate_limited" || outcome.ErrorCode != "" {
		t.Fatalf("rate_limited HTTP 失败必须推进失败计数、保持限流态且不写错误码: %#v", outcome)
	}
	if outcome.Projection == nil || outcome.Projection.TransitionKind != "cooldown_defer" || outcome.Projection.ExpectedAccountStatus != "rate_limited" || outcome.Projection.ExpectedCooldownFence == nil || !sameCooldownFence(outcome.Projection.ExpectedCooldownFence, fence) {
		t.Fatalf("rate_limited HTTP 失败必须投影 cooldown_defer 并保留 fence: %#v", outcome.Projection)
	}
}

// 契约：特供不豁免HTTP 失败 7 天停损——即使长期降频对特供跳过，7 天终态检查仍须
// 先于长期道判定；防止未来把 ExpeditedRecovery 守卫移到终态检查之前，导致
// 特供HTTP 失败结果永不停损。
func TestExpeditedCooldownHTTPFailureSevenDayTerminalStillApplies(t *testing.T) {
	now := time.Now().UTC()
	fence := &exactkeyprobe.CooldownFence{ObservationStartedAt: now.Add(-cooldownObservationTimeout - time.Hour), Generation: "binary-httpFailure-expedited-terminal"}
	input := newCooldownRetestInput(fence, now.Add(-time.Second))
	input.Eligibility.ExpeditedRecovery = true
	prior := binaryCooldownPrior(input, fence, 9)
	outcome := binaryCooldownOutcome(input, prior, exactkeyprobe.OutcomeHTTPFailure, 503, now)
	if outcome.AccountStatus != "error" || outcome.ErrorCode != "cooldown_retest_http_failure_timeout" || outcome.NextDueAt != nil {
		t.Fatalf("特供HTTP 失败满 7 天仍必须收敛 cooldown_retest_http_failure_timeout/error: %#v", outcome)
	}
	if outcome.Projection == nil || outcome.Projection.TransitionKind != "cooldown_error" || outcome.Projection.CooldownFence == nil || !sameCooldownFence(outcome.Projection.CooldownFence, fence) {
		t.Fatalf("特供HTTP 失败 7 天终态必须投影 cooldown_error 并保留 fence: %#v", outcome.Projection)
	}
}

// 契约 8（二元化主证据）：同一失败轮次下HTTP 失败结果与 transport 失败落同一
// 基准：第 1–5 轮 3/6/12/24/48 秒，第 6 轮起 60 秒，失败计数同步推进。
func TestCooldownHTTPFailureAndTransportShareTheSameFailureLane(t *testing.T) {
	now := time.Now().UTC()
	bases := []time.Duration{3 * time.Second, 6 * time.Second, 12 * time.Second, 24 * time.Second, 48 * time.Second, time.Minute}
	for priorFailures := 0; priorFailures < len(bases); priorFailures++ {
		fence := &exactkeyprobe.CooldownFence{ObservationStartedAt: now, Generation: fmt.Sprintf("binary-parity-%d", priorFailures)}
		input := newCooldownRetestInput(fence, now.Add(-time.Second))
		prior := binaryCooldownPrior(input, fence, priorFailures)
		httpFailure := binaryCooldownOutcome(input, prior, exactkeyprobe.OutcomeHTTPFailure, 503, now)
		transport := binaryCooldownOutcome(input, prior, exactkeyprobe.OutcomeUpstreamFailed, 0, now)
		wantBase := bases[priorFailures]
		if httpFailure.NextDueAt == nil || !isJitteredWithin(httpFailure.NextDueAt.Sub(now), wantBase) {
			t.Fatalf("HTTP 失败第 %d 轮延迟必须落在 %s 基准（jitter 区间内且不等于基准）: %v", priorFailures+1, wantBase, httpFailure.NextDueAt)
		}
		if transport.NextDueAt == nil || !isJitteredWithin(transport.NextDueAt.Sub(now), wantBase) {
			t.Fatalf("transport 第 %d 轮延迟必须落在 %s 基准: %v", priorFailures+1, wantBase, transport.NextDueAt)
		}
		if httpFailure.FailureCount != priorFailures+1 || transport.FailureCount != priorFailures+1 {
			t.Fatalf("HTTP 失败/transport 第 %d 轮必须同样推进失败计数: httpFailure=%d transport=%d", priorFailures+1, httpFailure.FailureCount, transport.FailureCount)
		}
		if httpFailure.Projection == nil || httpFailure.Projection.TransitionKind != "cooldown_defer" || transport.Projection == nil || transport.Projection.TransitionKind != "cooldown_failure" {
			t.Fatalf("第 %d 轮投影类别必须保持HTTP 失败 cooldown_defer / transport cooldown_failure: httpFailure=%#v transport=%#v", priorFailures+1, httpFailure.Projection, transport.Projection)
		}
	}
}

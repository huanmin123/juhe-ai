package gatewaycodex

// D-89（BUG-0175 W4-B）owner 侧协作者错误收敛为 probe_task_failure 结算的
// Mock 回归（对照归档 codex-turn-availability-probe.service.ts:69-129 的
// try/catch 契约）：任一 coordinator 错误都结算本代 generation 并保留短期
// 避让，不上抛。

import (
	"context"
	"errors"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

type w4bProbeCoordinator struct {
	acquireResult gatewaycircuit.ProbeAcquireResult
	releaseErr    error
	releaseOK     bool
	// settleErrOnce / settleFenceErrOnce 让错误只命中第一次调用（Node catch
	// 内的兜底结算若再失败会向上抛——持久错误传播本身就是契约的一部分）。
	settleErrOnce      error
	settleFenceErrOnce error
	settleCalls        int
	settleFenceCalls   int

	settled      []gatewaycircuit.SettleProbeInput
	settledFence []gatewaycircuit.SettleDispatchedProbeInput
	released     bool
}

func (c *w4bProbeCoordinator) Acquire(_ context.Context, _ gatewaycircuit.ProbeAcquireInput) (gatewaycircuit.ProbeAcquireResult, error) {
	return c.acquireResult, nil
}

func (c *w4bProbeCoordinator) ReleaseForExecution(_ context.Context, _ gatewaycircuit.ReleaseProbeInput) (bool, error) {
	c.released = true
	if c.releaseErr != nil {
		return false, c.releaseErr
	}
	return c.releaseOK, nil
}

func (c *w4bProbeCoordinator) Settle(_ context.Context, input gatewaycircuit.SettleProbeInput) (bool, error) {
	c.settleCalls++
	if c.settleErrOnce != nil {
		err := c.settleErrOnce
		c.settleErrOnce = nil
		return false, err
	}
	c.settled = append(c.settled, input)
	return true, nil
}

func (c *w4bProbeCoordinator) SettleDispatchedBySourceFence(_ context.Context, input gatewaycircuit.SettleDispatchedProbeInput) (bool, error) {
	c.settleFenceCalls++
	if c.settleFenceErrOnce != nil {
		err := c.settleFenceErrOnce
		c.settleFenceErrOnce = nil
		return false, err
	}
	c.settledFence = append(c.settledFence, input)
	return true, nil
}

func (c *w4bProbeCoordinator) GetState(_ context.Context, _ string) (*gatewaycircuit.ProbeState, error) {
	return nil, nil
}

func w4bProbeInput() CodexTurnAvoidanceProbeInput {
	return CodexTurnAvoidanceProbeInput{
		Account: gatewayruntimecache.OpenAIAccountSecret{ID: "acc-1", ConfigRevision: nil},
		Strategy: OpenAIGatewayClientStrategyContext{ClientSourceAvoidanceStateKey: "state-w4b"},
		Activation: CodexTurnFailureActivation{
			AccountID:        "acc-1",
			SourceGeneration: 3,
			SourceFenceID:    "fence-1",
		},
		// 派发被拒绝：走 settleDispatchedBySourceFence 的 probe_task_failure 主路径。
		Dispatch: func(string, string, string, *SourceProbeFence) HealthCheckDispatchOutcome {
			return HealthCheckDispatchOutcome{Outcome: HealthDispatchRejected, DecisionCode: "input_unavailable"}
		},
	}
}

func w4bOwnerAcquire() gatewaycircuit.ProbeAcquireResult {
	return gatewaycircuit.ProbeAcquireResult{
		Disposition: gatewaycircuit.ProbeDispositionOwner,
		RuntimeKey:  "rk",
		Generation:  2,
		OwnerToken:  "tok",
	}
}

// releaseForExecution 失败 → catch 的 else 臂：owner token 结算
// probe_task_failure，结果带 outcome，不向调用方上抛。
func TestCodexTurnProbeReleaseErrorSettlesProbeTaskFailure(t *testing.T) {
	coordinator := &w4bProbeCoordinator{
		acquireResult: w4bOwnerAcquire(),
		releaseErr:    errors.New("store unavailable"),
	}
	service := &TurnAvoidanceProbeService{
		Coordinator: coordinator,
		TurnRetry:   newTurnRetryService(t),
		Logger:      &recordingLogger{},
		Clock:       SystemClock{},
	}
	result, err := service.RunCodexTurnAvoidanceAvailabilityProbe(context.Background(), w4bProbeInput())
	if err != nil {
		t.Fatalf("release error must not escape: %v", err)
	}
	if result.Disposition != "owner" || result.Generation != 2 || result.Outcome != ProbeOutcomeProbeTaskFailure {
		t.Fatalf("result = %+v, want owner/gen2/probe_task_failure", result)
	}
	if len(coordinator.settled) != 1 || coordinator.settled[0].Outcome != ProbeOutcomeProbeTaskFailure {
		t.Fatalf("settled = %+v, want single probe_task_failure via owner token", coordinator.settled)
	}
	if coordinator.settled[0].OwnerToken != "tok" || coordinator.settled[0].Generation != 2 {
		t.Errorf("settle identity = %+v", coordinator.settled[0])
	}
}

// 已释放后的 fenced settle 失败 → catch 的 releasedForExecution 臂：以同一
// source fence 再次结算 probe_task_failure，不上抛。
func TestCodexTurnProbeFencedSettleErrorRecoversViaFence(t *testing.T) {
	coordinator := &w4bProbeCoordinator{
		acquireResult:      w4bOwnerAcquire(),
		releaseOK:          true,
		settleFenceErrOnce: errors.New("cas lost"),
	}
	service := &TurnAvoidanceProbeService{
		Coordinator: coordinator,
		TurnRetry:   newTurnRetryService(t),
		Logger:      &recordingLogger{},
		Clock:       SystemClock{},
	}
	input := w4bProbeInput()
	result, err := service.RunCodexTurnAvoidanceAvailabilityProbe(context.Background(), input)
	if err != nil {
		t.Fatalf("fenced settle error must not escape: %v", err)
	}
	if result.Outcome != ProbeOutcomeProbeTaskFailure || result.Generation != 2 {
		t.Fatalf("result = %+v, want probe_task_failure/gen2", result)
	}
	// 第一次 fenced 结算失败（settleFenceCalls=1），catch 内以同一 source
	// fence 重试成功（calls=2，成功记录 1 条）。
	if coordinator.settleFenceCalls != 2 || len(coordinator.settledFence) != 1 {
		t.Fatalf("fenced settlements calls=%d recorded=%d, want 2/1", coordinator.settleFenceCalls, len(coordinator.settledFence))
	}
	for _, item := range coordinator.settledFence {
		if item.Outcome != ProbeOutcomeProbeTaskFailure {
			t.Errorf("fenced outcome = %s, want probe_task_failure", item.Outcome)
		}
		if item.SourceFence.StateKey != "state-w4b" || item.SourceFence.SourceFenceID != "fence-1" {
			t.Errorf("fence identity = %+v", item.SourceFence)
		}
	}
	if len(coordinator.settled) != 0 {
		t.Errorf("owner-token settle must not run after release: %+v", coordinator.settled)
	}
}

// !released 的 unknown 结算失败 → catch 的 else 臂接管（owner token 结算
// probe_task_failure），不留 stranded generation。
func TestCodexTurnProbeUnknownSettleFailureFallsBackToTaskFailure(t *testing.T) {
	coordinator := &w4bProbeCoordinator{
		acquireResult: w4bOwnerAcquire(),
		releaseOK:     false,
		settleErrOnce: errors.New("first settle lost"),
	}
	service := &TurnAvoidanceProbeService{
		Coordinator: coordinator,
		TurnRetry:   newTurnRetryService(t),
		Logger:      &recordingLogger{},
		Clock:       SystemClock{},
	}
	result, err := service.RunCodexTurnAvoidanceAvailabilityProbe(context.Background(), w4bProbeInput())
	if err != nil {
		t.Fatalf("settle error must not escape: %v", err)
	}
	if result.Outcome != ProbeOutcomeProbeTaskFailure {
		t.Fatalf("result = %+v, want probe_task_failure", result)
	}
	if len(coordinator.settled) != 1 || coordinator.settled[0].Outcome != ProbeOutcomeProbeTaskFailure {
		t.Fatalf("settled = %+v, want single probe_task_failure retry", coordinator.settled)
	}
}

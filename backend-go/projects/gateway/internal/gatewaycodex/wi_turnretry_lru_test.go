package gatewaycodex

import (
	"context"
	"fmt"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaysession"
)

func TestWITurnRetryMemoryLRUEviction(t *testing.T) {
	service := newTurnRetryService(t)
	// 直接把内存表塞满（避免 5000 次全量扫描），再触发一次写入淘汰。
	service.memory.init()
	for i := 0; i < codexTurnRetryMaxEntries; i++ {
		key := fmt.Sprintf("wi-lru-%05d", i)
		service.memory.entries[key] = &memoryCodexTurnRetryEntry{
			value:     codexTurnRetryState{StateKey: key, CreatedAtMs: int64(i)},
			expiresAt: service.nowMs() + codexTurnRetryTtlMs,
		}
	}
	// 新条目 CreatedAtMs 更大 → 最旧者被淘汰，总量维持上限。
	service.setMemoryCodexTurnRetryState("wi-lru-new", codexTurnRetryState{StateKey: "wi-lru-new", CreatedAtMs: 9_999_999})
	if len(service.memory.entries) != codexTurnRetryMaxEntries {
		t.Fatalf("淘汰后总量=%d want %d", len(service.memory.entries), codexTurnRetryMaxEntries)
	}
	if _, ok := service.memory.entries["wi-lru-new"]; !ok {
		t.Fatal("新条目必须保留")
	}
	if _, ok := service.memory.entries["wi-lru-00000"]; ok {
		t.Fatal("最旧条目必须被淘汰")
	}
}

func TestWITurnRetryGenerationTombstoneEviction(t *testing.T) {
	service := newTurnRetryService(t)
	strategy := avoidanceStrategy("wi-gen-evict")
	// 塞满 generation 墓碑表（全部未过期），触发 activation 时走淘汰分支。
	service.memory.init()
	for i := 0; i < codexTurnRetryMaxEntries; i++ {
		key := fmt.Sprintf("wi-gen-%05d", i)
		service.memory.generations[key] = &avoidanceGenerationTombstone{
			generation:  1,
			expiresAtMs: service.nowMs() + codexTurnRetryTtlMs,
		}
	}
	service.RememberCodexTurnStreamFailure(strategy, "acc-1", CodexTurnFailureInput{})
	second := service.RememberCodexTurnStreamFailure(strategy, "acc-1", CodexTurnFailureInput{})
	if second == nil || second.Activation == nil {
		t.Fatalf("激活失败: %+v", second)
	}
	if len(service.memory.generations) > codexTurnRetryMaxEntries {
		t.Fatalf("墓碑表超限: %d", len(service.memory.generations))
	}
	if second.Activation.SourceGeneration != 1 {
		t.Fatalf("新墓碑 generation=%d want 1", second.Activation.SourceGeneration)
	}
}

func TestWITurnRetryWarnAndEvidenceHelpers(t *testing.T) {
	service := newTurnRetryService(t)
	logger := &recordingLogger{}
	service.Logger = logger
	service.warn("wi-retry-event", map[string]any{"k": "v"}, "message")
	if len(logger.warnings) != 1 {
		t.Fatalf("warn 未记录: %v", logger.warnings)
	}
	quiet := newTurnRetryService(t)
	quiet.warn("e", nil, "m") // 无 Logger 安全 no-op。
	// evidence 缺省回落 retryable。
	if got := orElseEvidence(""); got != EvidenceRetryableFailure {
		t.Fatalf("缺省 evidence=%q", got)
	}
	if got := orElseEvidence("custom"); got != "custom" {
		t.Fatalf("显式 evidence=%q", got)
	}
	// maxInt64 双分支。
	if maxInt64(2, 1) != 2 || maxInt64(1, 2) != 2 {
		t.Fatal("maxInt64 错误")
	}
	// newID 走注入的 CreateID。
	if got := newTurnRetryService(t).newID(); got == "" {
		t.Fatal("注入 CreateID 时 newID 必须返回其值")
	}
}

func TestWIJsJSONArrayControlEscapes(t *testing.T) {
	// \b \f 与其他控制字符的 \u00XX 转义。
	if got := jsJSONArray([]string{"\b\f"}); got != `["\b\f"]` {
		t.Fatalf("短转义=%q", got)
	}
	if got := jsJSONArray([]string{"\x01"}); got != `["\u0001"]` {
		t.Fatalf("控制字符=%q", got)
	}
	if got := jsJSONArray([]string{"\x1f"}); got != `["\u001f"]` {
		t.Fatalf("0x1f=%q", got)
	}
}

func TestWIProbeDispatchFallbacks(t *testing.T) {
	retry := newTurnRetryService(t)
	probe := &TurnAvoidanceProbeService{TurnRetry: retry, Clock: SystemClock{}}
	input := CodexTurnAvoidanceProbeInput{Account: gatewayruntimecache.OpenAIAccountSecret{ID: "acc-1"}}
	// Dispatch 与 DefaultDispatch 都缺 → input_unavailable 拒绝。
	outcome := probe.dispatchSourceFence(input, SourceProbeFence{})
	if outcome.Outcome != HealthDispatchRejected || outcome.DecisionCode != "input_unavailable" {
		t.Fatalf("outcome=%+v", outcome)
	}
	// DefaultDispatch 兜底。
	probe.DefaultDispatch = func(string, string, string, *SourceProbeFence) HealthCheckDispatchOutcome {
		return HealthCheckDispatchOutcome{Outcome: HealthDispatchQueued}
	}
	outcome = probe.dispatchSourceFence(input, SourceProbeFence{})
	if outcome.Outcome != HealthDispatchQueued {
		t.Fatalf("兜底 dispatch=%+v", outcome)
	}
	// AccountRuntimeKey 缺省走 gatewaycircuit 组装。
	key, err := probe.accountRuntimeKey(input.Account)
	if err != nil || key == "" {
		t.Fatalf("runtime key=%q err=%v", key, err)
	}
	// 注入失败。
	probe.AccountRuntimeKey = func(gatewayruntimecache.OpenAIAccountSecret) (string, error) {
		return "", errNew("runtime key 不可用")
	}
	if _, err := probe.accountRuntimeKey(input.Account); err == nil {
		t.Fatal("注入失败必须透传")
	}
	// settleOwnerProbeFailure：fence 结算二次失败 → 错误透传。
	coordinator := &fakeProbeCoordinator{acquireResult: gatewaycircuit.ProbeAcquireResult{RuntimeKey: "rk", Generation: 1, OwnerToken: "t"}}
	failing := &TurnAvoidanceProbeService{Coordinator: &failingSettleCoordinator{inner: coordinator}, TurnRetry: retry}
	if _, err := failing.settleOwnerProbeFailure(context.Background(), CodexTurnAvoidanceProbeInput{Account: input.Account},
		coordinator.acquireResult,
		&SourceProbeFence{StateKey: "s", AccountID: "a", SourceFenceID: "f"}, true, errNew("cause")); err == nil {
		t.Fatal("fence 结算失败必须透传")
	}
}

type failingSettleCoordinator struct {
	inner *fakeProbeCoordinator
}

func (c *failingSettleCoordinator) Acquire(ctx context.Context, input gatewaycircuit.ProbeAcquireInput) (gatewaycircuit.ProbeAcquireResult, error) {
	return c.inner.Acquire(ctx, input)
}

func (c *failingSettleCoordinator) ReleaseForExecution(ctx context.Context, input gatewaycircuit.ReleaseProbeInput) (bool, error) {
	return c.inner.ReleaseForExecution(ctx, input)
}

func (c *failingSettleCoordinator) Settle(ctx context.Context, input gatewaycircuit.SettleProbeInput) (bool, error) {
	return c.inner.Settle(ctx, input)
}

func (c *failingSettleCoordinator) SettleDispatchedBySourceFence(ctx context.Context, input gatewaycircuit.SettleDispatchedProbeInput) (bool, error) {
	return false, errNew("fence 结算失败")
}

func (c *failingSettleCoordinator) GetState(ctx context.Context, runtimeKey string) (*gatewaycircuit.ProbeState, error) {
	return c.inner.GetState(ctx, runtimeKey)
}

type wiProbeCause struct{}

func (e *wiProbeCause) Error() string { return "cause" }

func errNew(message string) error { return &wiProbeCause{} }

func TestWIResolveSessionIdentityMissingResolver(t *testing.T) {
	// Session resolver 未装配 → missing 身份（不 panic）。
	resolver := newSourceResolver(nil)
	identity := resolver.resolveSessionIdentity(nil, GatewayClientSourceIdentityInput{}, "sys", "key")
	if identity.Status != gatewaysession.IdentityStatusMissing {
		t.Fatalf("identity=%+v", identity)
	}
}

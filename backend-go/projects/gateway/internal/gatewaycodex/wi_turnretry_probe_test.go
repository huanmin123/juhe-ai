package gatewaycodex

import (
	"context"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func TestWINormalizeSourceProbeFence(t *testing.T) {
	if NormalizeSourceProbeFence(nil) != nil {
		t.Fatal("nil fence 必须返回 nil")
	}
	// 合法 fence：字段裁剪并逐项带出。
	fence := NormalizeSourceProbeFence(&SourceProbeFence{
		StateKey: " state ", AccountID: " acc ", SourceGeneration: 3,
		SourceFenceID: "fid", RuntimeKey: " rk ", ProbeGeneration: 5, ConfigRevision: 2,
	})
	if fence == nil || fence.StateKey != "state" || fence.AccountID != "acc" || fence.RuntimeKey != "rk" ||
		fence.SourceFenceID != "fid" || fence.SourceGeneration != 3 || fence.ProbeGeneration != 5 || fence.ConfigRevision != 2 {
		t.Fatalf("fence=%+v", fence)
	}
	// 各必填字段为空 / 超限 / generation 非正 → nil。
	invalid := []*SourceProbeFence{
		{StateKey: " ", AccountID: "a", SourceFenceID: "f", RuntimeKey: "r", SourceGeneration: 1, ProbeGeneration: 1, ConfigRevision: 1},
		{StateKey: "s", AccountID: " ", SourceFenceID: "f", RuntimeKey: "r", SourceGeneration: 1, ProbeGeneration: 1, ConfigRevision: 1},
		{StateKey: "s", AccountID: "a", SourceFenceID: "", RuntimeKey: "r", SourceGeneration: 1, ProbeGeneration: 1, ConfigRevision: 1},
		{StateKey: "s", AccountID: "a", SourceFenceID: "f", RuntimeKey: " ", SourceGeneration: 1, ProbeGeneration: 1, ConfigRevision: 1},
		{StateKey: "s", AccountID: "a", SourceFenceID: "f", RuntimeKey: "r", SourceGeneration: 0, ProbeGeneration: 1, ConfigRevision: 1},
		{StateKey: "s", AccountID: "a", SourceFenceID: "f", RuntimeKey: "r", SourceGeneration: 1, ProbeGeneration: -3, ConfigRevision: 1},
		{StateKey: "s", AccountID: "a", SourceFenceID: "f", RuntimeKey: "r", SourceGeneration: 1, ProbeGeneration: 1, ConfigRevision: 0},
		// 超过字段长度上限（512/256/1024/64）。
		{StateKey: makeString("s", 513), AccountID: "a", SourceFenceID: "f", RuntimeKey: "r", SourceGeneration: 1, ProbeGeneration: 1, ConfigRevision: 1},
		{StateKey: "s", AccountID: makeString("a", 257), SourceFenceID: "f", RuntimeKey: "r", SourceGeneration: 1, ProbeGeneration: 1, ConfigRevision: 1},
		{StateKey: "s", AccountID: "a", SourceFenceID: makeString("f", 65), RuntimeKey: "r", SourceGeneration: 1, ProbeGeneration: 1, ConfigRevision: 1},
		{StateKey: "s", AccountID: "a", SourceFenceID: "f", RuntimeKey: makeString("r", 1025), SourceGeneration: 1, ProbeGeneration: 1, ConfigRevision: 1},
	}
	for i, fence := range invalid {
		if got := NormalizeSourceProbeFence(fence); got != nil {
			t.Fatalf("第 %d 个非法 fence 必须返回 nil: %+v", i, got)
		}
	}
	// 边界长度恰好合法。
	exact := NormalizeSourceProbeFence(&SourceProbeFence{
		StateKey: makeString("s", 512), AccountID: makeString("a", 256),
		SourceFenceID: makeString("f", 64), RuntimeKey: makeString("r", 1024),
		SourceGeneration: 1, ProbeGeneration: 1, ConfigRevision: 1,
	})
	if exact == nil {
		t.Fatal("边界长度必须合法")
	}
}

func makeString(ch string, n int) string {
	out := make([]byte, 0, n*len(ch))
	for i := 0; i < n; i++ {
		out = append(out, ch...)
	}
	return string(out)
}

func TestWIAccountConfigRevision(t *testing.T) {
	// nil / 负数回落 1；合法值直通。
	if got := accountConfigRevision(gatewayruntimecache.OpenAIAccountSecret{}); got != 1 {
		t.Fatalf("nil revision=%d", got)
	}
	negative := int64(-4)
	if got := accountConfigRevision(gatewayruntimecache.OpenAIAccountSecret{ConfigRevision: &negative}); got != 1 {
		t.Fatalf("负 revision=%d", got)
	}
	seven := int64(7)
	if got := accountConfigRevision(gatewayruntimecache.OpenAIAccountSecret{ConfigRevision: &seven}); got != 7 {
		t.Fatalf("revision=%d", got)
	}
	if !isFiniteInt64(1) {
		t.Fatal("int64 恒有限")
	}
}

func TestWIRandomIDGeneratorFallback(t *testing.T) {
	// CreateID 未注入 → createID/newID 落到 RandomUUID（v4 形状）。
	service := &TurnRetryService{Secret: "s", Clock: newFakeClock(time.Date(2026, 9, 10, 9, 0, 0, 0, time.UTC))}
	first := service.createID()
	second := service.newID()
	if first == second {
		t.Fatalf("两次 id 不应相同: %s", first)
	}
	if len(first) != 36 || first[14] != '4' {
		t.Fatalf("id=%q 不是 v4 UUID 形状", first)
	}
}

func TestWIClientSourceAliases(t *testing.T) {
	// 契约：ClientSource* 是 codex turn 实现的重命名导出，行为必须逐位一致。
	retry := newTurnRetryService(t)
	strategy := avoidanceStrategy("wi-alias")
	syncResult := retry.OrderOpenAIAccountsByClientSourceAvoidance(
		[]gatewayruntimecache.OpenAIAccountSecret{{ID: "acc-1"}}, strategy, nil)
	directResult := retry.OrderOpenAIAccountsByCodexTurnAvoidance(
		[]gatewayruntimecache.OpenAIAccountSecret{{ID: "acc-1"}}, strategy, nil)
	if syncResult.Applied != directResult.Applied || syncResult.FailureCount != directResult.FailureCount {
		t.Fatalf("别名结果不一致: %+v vs %+v", syncResult, directResult)
	}
	asyncResult, err := retry.OrderOpenAIAccountsByClientSourceAvoidanceAsync(context.Background(),
		[]gatewayruntimecache.OpenAIAccountSecret{{ID: "acc-1"}}, strategy, nil)
	if err != nil || asyncResult.FailureCount != directResult.FailureCount {
		t.Fatalf("async 别名=%+v err=%v", asyncResult, err)
	}
	remembered := retry.RememberGatewayClientSourceFailure(strategy, "acc-1", CodexTurnFailureInput{})
	directRemembered := retry.RememberCodexTurnStreamFailure(avoidanceStrategy("wi-alias-2"), "acc-1", CodexTurnFailureInput{})
	if remembered == nil || directRemembered == nil || remembered.FailureCount != directRemembered.FailureCount {
		t.Fatalf("remember 别名不一致: %+v vs %+v", remembered, directRemembered)
	}
	if _, err := retry.RememberGatewayClientSourceFailureAsync(context.Background(), strategy, "acc-1", CodexTurnFailureInput{}); err != nil {
		t.Fatalf("remember async 别名失败: %v", err)
	}
	// 探活别名：空 state key 走 unknown/joined 短路，与直接入口一致。
	probe := &TurnAvoidanceProbeService{TurnRetry: retry, Clock: SystemClock{}}
	aliasInput := CodexTurnAvoidanceProbeInput{Strategy: OpenAIGatewayClientStrategyContext{}}
	aliased, err := probe.RunGatewayClientSourceAvoidanceAvailabilityProbe(context.Background(), aliasInput)
	direct, err2 := probe.RunCodexTurnAvoidanceAvailabilityProbe(context.Background(), aliasInput)
	if err != nil || err2 != nil || aliased != direct {
		t.Fatalf("probe 别名不一致: %+v/%v vs %+v/%v", aliased, err, direct, err2)
	}
}

func TestWIOrderAccountsByCodexTurnAvoidanceAsync(t *testing.T) {
	t.Run("无 Store 委托 memory", func(t *testing.T) {
		retry := newTurnRetryService(t)
		strategy := avoidanceStrategy("wi-async-order")
		retry.RememberCodexTurnStreamFailure(strategy, "acc-1", CodexTurnFailureInput{})
		retry.RememberCodexTurnStreamFailure(strategy, "acc-1", CodexTurnFailureInput{})
		result, err := retry.OrderOpenAIAccountsByCodexTurnAvoidanceAsync(context.Background(),
			[]gatewayruntimecache.OpenAIAccountSecret{{ID: "acc-1"}, {ID: "acc-2"}}, strategy, nil)
		if err != nil || !result.Applied {
			t.Fatalf("memory 委托=%+v err=%v", result, err)
		}
	})
	t.Run("redis 读取与错误透传", func(t *testing.T) {
		retry := newTurnRetryService(t)
		store := &fakeRedisStore{values: map[string][]byte{}}
		retry.Store = store
		strategy := avoidanceStrategy("wi-async-order-redis")
		ctx := context.Background()
		if _, err := retry.RememberCodexTurnStreamFailureAsync(ctx, strategy, "acc-1", CodexTurnFailureInput{}); err != nil {
			t.Fatalf("准备失败状态出错: %v", err)
		}
		if _, err := retry.RememberCodexTurnStreamFailureAsync(ctx, strategy, "acc-1", CodexTurnFailureInput{}); err != nil {
			t.Fatalf("第二次失败状态出错: %v", err)
		}
		result, err := retry.OrderOpenAIAccountsByCodexTurnAvoidanceAsync(ctx,
			[]gatewayruntimecache.OpenAIAccountSecret{{ID: "acc-1"}, {ID: "acc-2"}}, strategy, nil)
		if err != nil {
			t.Fatalf("redis 排序失败: %v", err)
		}
		// redis generation=7（fake store incr），避让已激活。
		if !result.Applied || len(result.AvoidedAccountIDs) != 1 || result.AvoidedAccountIDs[0] != "acc-1" {
			t.Fatalf("redis 排序=%+v", result)
		}
	})
}

func TestWIClearCodexTurnAccountAvoidanceAsync(t *testing.T) {
	t.Run("memory 委托", func(t *testing.T) {
		retry := newTurnRetryService(t)
		strategy := avoidanceStrategy("wi-clear-mem")
		retry.RememberCodexTurnStreamFailure(strategy, "acc-1", CodexTurnFailureInput{})
		retry.RememberCodexTurnStreamFailure(strategy, "acc-1", CodexTurnFailureInput{})
		cleared, err := retry.ClearCodexTurnAccountAvoidanceAsync(context.Background(), strategy, "acc-1")
		if err != nil || !cleared {
			t.Fatalf("clear=%v err=%v", cleared, err)
		}
		// 已清除后再清 → false。
		if cleared, err := retry.ClearCodexTurnAccountAvoidanceAsync(context.Background(), strategy, "acc-1"); err != nil || cleared {
			t.Fatalf("二次 clear=%v err=%v", cleared, err)
		}
		// 守卫：空 stateKey / 空 accountID。
		guards := []struct {
			strategy  OpenAIGatewayClientStrategyContext
			accountID string
		}{
			{OpenAIGatewayClientStrategyContext{}, "acc-1"},
			{avoidanceStrategy("wi-clear-mem"), "  "},
		}
		for i, tc := range guards {
			if cleared, err := retry.ClearCodexTurnAccountAvoidanceAsync(context.Background(), tc.strategy, tc.accountID); err != nil || cleared {
				t.Fatalf("守卫 %d 未拒绝: cleared=%v err=%v", i, cleared, err)
			}
		}
	})
	t.Run("redis CAS 路径", func(t *testing.T) {
		retry := newTurnRetryService(t)
		store := &fakeRedisStore{values: map[string][]byte{}}
		retry.Store = store
		strategy := avoidanceStrategy("wi-clear-redis")
		ctx := context.Background()
		// 无状态时 clear → false。
		if cleared, err := retry.ClearCodexTurnAccountAvoidanceAsync(ctx, strategy, "acc-1"); err != nil || cleared {
			t.Fatalf("无状态 clear=%v err=%v", cleared, err)
		}
		if _, err := retry.RememberCodexTurnStreamFailureAsync(ctx, strategy, "acc-1", CodexTurnFailureInput{}); err != nil {
			t.Fatalf("准备失败: %v", err)
		}
		if _, err := retry.RememberCodexTurnStreamFailureAsync(ctx, strategy, "acc-1", CodexTurnFailureInput{}); err != nil {
			t.Fatalf("准备失败: %v", err)
		}
		cleared, err := retry.ClearCodexTurnAccountAvoidanceAsync(ctx, strategy, "acc-1")
		if err != nil || !cleared {
			t.Fatalf("redis clear=%v err=%v", cleared, err)
		}
		// 状态已删 → 再清 false。
		if cleared, err := retry.ClearCodexTurnAccountAvoidanceAsync(ctx, strategy, "acc-1"); err != nil || cleared {
			t.Fatalf("二次 clear=%v err=%v", cleared, err)
		}
	})
	t.Run("CAS 持续竞争耗尽后 fail-open", func(t *testing.T) {
		retry := newTurnRetryService(t)
		store := &fakeRedisStore{values: map[string][]byte{}, failCAS: true}
		retry.Store = store
		strategy := avoidanceStrategy("wi-clear-race")
		cleared, err := retry.ClearCodexTurnAccountAvoidanceAsync(context.Background(), strategy, "acc-1")
		if err != nil || cleared {
			t.Fatalf("耗尽后 fail-open: cleared=%v err=%v", cleared, err)
		}
	})
}

func TestWIClearCodexTurnAccountAvoidanceByFenceAsyncRedis(t *testing.T) {
	retry := newTurnRetryService(t)
	store := &fakeRedisStore{values: map[string][]byte{}}
	retry.Store = store
	strategy := avoidanceStrategy("wi-fence-redis")
	ctx := context.Background()
	if _, err := retry.RememberCodexTurnStreamFailureAsync(ctx, strategy, "acc-1", CodexTurnFailureInput{}); err != nil {
		t.Fatalf("首次记录失败: %v", err)
	}
	second, err := retry.RememberCodexTurnStreamFailureAsync(ctx, strategy, "acc-1", CodexTurnFailureInput{})
	if err != nil || second == nil || second.Activation == nil {
		t.Fatalf("激活失败: %+v err=%v", second, err)
	}
	activation := second.Activation
	// fence 完全匹配 → 清除成功。
	cleared, err := retry.ClearCodexTurnAccountAvoidanceByFenceAsync(ctx, ClearCodexTurnAccountAvoidanceByFenceInput{
		StateKey: "wi-fence-redis", AccountID: "acc-1",
		SourceGeneration: activation.SourceGeneration, SourceFenceID: activation.SourceFenceID,
	})
	if err != nil || !cleared {
		t.Fatalf("fence 清除=%v err=%v", cleared, err)
	}
	// 清除后同 fence 再清 → false。
	if cleared, err := retry.ClearCodexTurnAccountAvoidanceByFenceAsync(ctx, ClearCodexTurnAccountAvoidanceByFenceInput{
		StateKey: "wi-fence-redis", AccountID: "acc-1",
		SourceGeneration: activation.SourceGeneration, SourceFenceID: activation.SourceFenceID,
	}); err != nil || cleared {
		t.Fatalf("二次清除=%v err=%v", cleared, err)
	}
	// 守卫：非法 fence id / 空 state key。
	guards := []ClearCodexTurnAccountAvoidanceByFenceInput{
		{StateKey: "", AccountID: "acc-1", SourceFenceID: "f"},
		{StateKey: "s", AccountID: " ", SourceFenceID: "f"},
		{StateKey: "s", AccountID: "acc-1", SourceFenceID: "not-a-uuid"},
	}
	for i, input := range guards {
		if cleared, err := retry.ClearCodexTurnAccountAvoidanceByFenceAsync(ctx, input); err != nil || cleared {
			t.Fatalf("守卫 %d 未拒绝: cleared=%v err=%v", i, cleared, err)
		}
	}
	// redis 里已有状态但 generation 不匹配 → false。
	if _, err := retry.RememberCodexTurnStreamFailureAsync(ctx, strategy, "acc-2", CodexTurnFailureInput{}); err != nil {
		t.Fatalf("准备 acc-2 失败: %v", err)
	}
	if _, err := retry.RememberCodexTurnStreamFailureAsync(ctx, strategy, "acc-2", CodexTurnFailureInput{}); err != nil {
		t.Fatalf("激活 acc-2 失败: %v", err)
	}
	if cleared, err := retry.ClearCodexTurnAccountAvoidanceByFenceAsync(ctx, ClearCodexTurnAccountAvoidanceByFenceInput{
		StateKey: "wi-fence-redis", AccountID: "acc-2",
		SourceGeneration: 999, SourceFenceID: "00000000-0000-4000-8000-00000000000x",
	}); err != nil || cleared {
		t.Fatalf("generation 不匹配=%v err=%v", cleared, err)
	}
}

func TestWIProbeServiceWarnWithoutLogger(t *testing.T) {
	// Logger 未装配时 warn 必须是安全 no-op（不 panic）。
	probe := &TurnAvoidanceProbeService{TurnRetry: newTurnRetryService(t), Clock: SystemClock{}}
	probe.warn("event", map[string]any{"k": "v"}, "message")
	// coordinator 错误短路：空 stateKey 直接 joined/unknown。
	result, err := probe.RunCodexTurnAvoidanceAvailabilityProbe(context.Background(), CodexTurnAvoidanceProbeInput{})
	if err != nil || result.Disposition != "joined" || result.Outcome != ProbeOutcomeUnknown {
		t.Fatalf("空 stateKey 短路=%+v err=%v", result, err)
	}
}

var _ = gatewaycircuit.ProbeDispositionOwner

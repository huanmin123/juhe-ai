package gatewaycircuit

// circuitaudit_keysplit_test.go — R1 键空间互通验收（原复现：键名分裂）。
//
// 已修复的缺陷：gateway 生产装配原使用复数 Name "gateway-account-circuits"
// （cmd/juhe-ai-gateway/chain_wiring_w2c.go newChainAccountCircuitService，
// 已改为单数），而 jobs 侧 circuitstore 固定单数 StoreName
// "gateway-account-circuit"（projects/jobs/internal/circuitstore/store.go:34，
// 键形状注释 :24 与键构造 :713-726）。两侧键构造函数逐字节相同
// （gatewaycircuit/store_redis.go:630-643 redisAccountCircuitStoreKeys），唯一
// 差异曾是这一个 Name 段，导致 gateway 写入的熔断运行态落在
//
//	juhe-ai:{ns}:account-circuit:gateway-account-circuits:{states,due,...}
//
// 而 jobs 的恢复探测 ListDue 读的是
//
//	juhe-ai:{ns}:account-circuit:gateway-account-circuit:{states,due,...}
//
// 复数与单数键空间完全不相交：gateway 记下的 SUSPECT/OPEN 对 jobs 的
// canary / ListDue 恢复探测永久不可见。修复：装配 Name 对齐单数（笔误）。
//
// 本测试是修复后的验收断言：用 gateway 生产装配参数（单数 Name）写入一条
// SUSPECT，然后：
//
//  1. 以 jobs 形状参数（单数 circuitstore.StoreName 同值）建的第二个 store
//     读同一 scope 必须看到该 SUSPECT（Get 同相位、ListDue 包含该 scopeKey）
//     ——两侧键空间互通，jobs 恢复探测能看到 gateway 记录的熔断状态；
//  2. 保留键名字面量锚定断言：gateway 生产参数与 jobs StoreName 现在产出
//     完全相同的键串，任何一侧漂移都会被锚定断言拦截。

import (
	"context"
	"testing"

	redis "github.com/redis/go-redis/v9"
	miniredis "github.com/alicebob/miniredis/v2"
)

func TestCircuitAuditR1KeyspaceGatewayAndJobsInteroperable(t *testing.T) {
	server := miniredis.RunT(t)
	ctx := context.Background()
	clock := int64(1_000_000)
	now := func() int64 { return clock }

	// 与 chain_wiring_w2c.go 修复后的生产装配参数一致（单数 Name）。
	gatewayStore, err := NewRedisStore(RedisStoreOptions{
		RedisURL:  "redis://" + server.Addr(),
		Namespace: "circuit-audit-ns",
		Name:      "gateway-account-circuit",
		Capacity:  10_000,
		Now:       now,
	})
	if err != nil {
		t.Fatalf("NewRedisStore(gateway 生产参数): %v", err)
	}

	scope := accountScope("circuit-audit-acct-1")
	scopeKey := MustScopeKey(scope)

	// 步骤 1：gateway store 写入一条 SUSPECT（模拟一次 transport 失败切号）。
	suspect, err := gatewayStore.Suspect(ctx, SuspectInput{
		Scope:            scope,
		DispatchRevision: "7",
		TransitionID:     "circuit-audit-ks-1",
		Reason:           "transport:circuit-audit connection reset",
		NowMs:            &clock,
	})
	if err != nil || suspect.Status != MutationApplied {
		t.Fatalf("gateway store Suspect = (%s, %v)", suspect.Status, err)
	}
	if suspect.State.Phase != PhaseSuspect {
		t.Fatalf("gateway store Suspect phase = %s", suspect.State.Phase)
	}

	// gateway 自己能看到（证明写入确实成功）。
	got, err := gatewayStore.Get(ctx, scope, &clock)
	if err != nil || got.Phase != PhaseSuspect {
		t.Fatalf("gateway store Get = (%s, %v)", got.Phase, err)
	}
	due, err := gatewayStore.ListDue(ctx, clock+3_000, 10)
	if err != nil {
		t.Fatalf("gateway store ListDue: %v", err)
	}
	foundDue := false
	for _, state := range due {
		if state.ScopeKey == scopeKey {
			foundDue = true
		}
	}
	if !foundDue {
		t.Fatalf("gateway store ListDue 必须包含 %q（写入成功且到期），got %d states", scopeKey, len(due))
	}

	// 步骤 2：键名字面量锚定（修复后两侧同串）。精确断言依据
	// redisAccountCircuitStoreKeys（store_redis.go:630-643）与
	// rediscfg.NamespacedKey（namespace 插在 juhe-ai: 根之后）。
	raw := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = raw.Close() })

	gatewayKeys := redisAccountCircuitStoreKeys("gateway-account-circuit", "circuit-audit-ns")
	jobsKeys := redisAccountCircuitStoreKeys("gateway-account-circuit", "circuit-audit-ns") // jobs circuitstore.StoreName（单数）

	if gatewayKeys.states != "juhe-ai:circuit-audit-ns:account-circuit:gateway-account-circuit:states" {
		t.Fatalf("gateway states 键形状漂移: %q", gatewayKeys.states)
	}
	if jobsKeys.states != "juhe-ai:circuit-audit-ns:account-circuit:gateway-account-circuit:states" {
		t.Fatalf("jobs states 键形状漂移: %q", jobsKeys.states)
	}
	if gatewayKeys.states != jobsKeys.states || gatewayKeys.due != jobsKeys.due {
		t.Fatalf("gateway 与 jobs 键空间仍分裂: gateway=(%s, %s) jobs=(%s, %s)",
			gatewayKeys.states, gatewayKeys.due, jobsKeys.states, jobsKeys.due)
	}

	// 步骤 3：以 jobs 的默认参数（单数 Name = circuitstore.StoreName，与 gateway
	// 生产装配同值）在同一 miniredis 上建第二个 store，验收互通：jobs 形状
	// store 必须读到 gateway 刚写入的 SUSPECT（Get 同相位、ListDue 包含该
	// scopeKey）——恢复探测不再对 gateway 记录的熔断状态失明。
	jobsShapedStore, err := NewRedisStore(RedisStoreOptions{
		RedisURL:  "redis://" + server.Addr(),
		Namespace: "circuit-audit-ns",
		Name:      "gateway-account-circuit", // jobs circuitstore.StoreName（单数，同 gateway 修复值）
		Capacity:  10_000,
		Now:       now,
	})
	if err != nil {
		t.Fatalf("NewRedisStore(jobs 形状参数): %v", err)
	}
	jobsView, err := jobsShapedStore.Get(ctx, scope, &clock)
	if err != nil {
		t.Fatalf("jobs 形状 store Get: %v", err)
	}
	if jobsView.Phase != PhaseSuspect {
		t.Fatalf("jobs 形状 store 看到的 phase = %s, want SUSPECT（键空间互通验收）", jobsView.Phase)
	}
	jobsDueList, err := jobsShapedStore.ListDue(ctx, clock+3_000, 10)
	if err != nil {
		t.Fatalf("jobs 形状 store ListDue: %v", err)
	}
	foundJobsDue := false
	for _, state := range jobsDueList {
		if state.ScopeKey == scopeKey {
			foundJobsDue = true
		}
	}
	if !foundJobsDue {
		t.Fatalf("jobs 形状 store ListDue 必须包含 %q（恢复探测可见性验收），got %d states", scopeKey, len(jobsDueList))
	}

	// 键物理锚定：该 SUSPECT 确实落在互通的单数键空间。
	statesExists, err := raw.HExists(ctx, jobsKeys.states, scopeKey).Result()
	if err != nil {
		t.Fatalf("HExists(states): %v", err)
	}
	if !statesExists {
		t.Fatal("单数 states hash 必须包含该账号 scopeKey（写入应落在互通键空间）")
	}
	dueScore, err := raw.ZScore(ctx, jobsKeys.due, scopeKey).Result()
	if err != nil {
		t.Fatalf("ZScore(due): %v", err)
	}
	if dueScore != float64(clock+3_000) {
		t.Fatalf("due zset score = %v, want %d", dueScore, clock+3_000)
	}
}

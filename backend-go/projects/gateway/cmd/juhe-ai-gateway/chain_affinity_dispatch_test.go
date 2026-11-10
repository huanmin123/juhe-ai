package main

// 会话亲和 dispatch 读写面合流（W17 补修）的组合与适配测试：
//  1. chainDispatchOrderingOptionsOf：dispatch 排序选项 → G14 DispatchOrderingOptions
//    的映射（此前 localSessionAffinity 的 OrderAsync 签名丢弃 options）。
//  2. chainSessionAffinityDispatchPort：Redis cacheDriver 下绑定读写经
//    AffinityService 落 miniredis（dispatch 写 → preauth 读侧可见），
//    绑定读写面与 preauth 共用同一数据面。
//  3. composeGatewayChain 装配分叉：Redis cacheDriver → 适配器端口；
//    memory / nil Identity → localSessionAffinity（本地降级行为不变）。

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	miniredis "github.com/alicebob/miniredis/v2"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaysession"
)

const w17AffinitySecret = "w17-affinity-secret"

func w17Account(id string) gatewayruntimecache.OpenAIAccountSecret {
	return gatewayruntimecache.OpenAIAccountSecret{ID: id, ConcurrencyLimit: 10}
}

// w17BindingKeyExists scans miniredis for the session-affinity binding key of
// the given affinity key (suffix match sidesteps the namespace prefix layout).
func w17BindingKeyExists(t *testing.T, mr *miniredis.Miniredis, affinityKey string) bool {
	t.Helper()
	for _, key := range mr.Keys() {
		if strings.HasSuffix(key, "session-affinity:binding:"+affinityKey) {
			return true
		}
	}
	return false
}

func w17RedisAffinityService(t *testing.T, mr *miniredis.Miniredis, namespace string) *gatewaysession.AffinityService {
	t.Helper()
	service, err := gatewaysession.NewAffinityService(gatewaysession.AffinityConfig{
		CacheDriver:        gatewaysession.CacheDriverRedis,
		RuntimeStateDriver: gatewaysession.RuntimeStateDriverMemory,
		Secret:             w17AffinitySecret,
		RedisURL:           "redis://" + mr.Addr(),
		RedisNamespace:     namespace,
	})
	if err != nil {
		t.Fatalf("create redis affinity service: %v", err)
	}
	return service
}

// TestChainDispatchOrderingOptionsOf: 完整选项映射（含 nil 分支与解引用）。
func TestChainDispatchOrderingOptionsOf(t *testing.T) {
	policy := gatewayruntimecache.GroupSchedulingPolicy{DefaultSoftConcurrency: 7, ClientIPConcurrencyLimit: 3}
	rankByAccountID := map[string]int{"acc-a": 0, "acc-b": 1}
	converted := chainDispatchOrderingOptionsOf(gatewaydispatch.AffinityOrderingOptions{
		GroupType:        "personal",
		SchedulingPolicy: &policy,
		ModelPriority: &gatewaydispatch.ModelPriority{
			RequestedModel:       "gpt-test",
			SourceEndpointFamily: "responses",
			RankByAccountID:      rankByAccountID,
		},
		TrafficMigrationScope: &gatewaydispatch.AffinityScope{SystemAccountID: "sys-1", APIKeyID: "key-1", GroupID: "grp-1"},
	})
	if converted.GroupType != "personal" {
		t.Fatalf("GroupType = %q", converted.GroupType)
	}
	if converted.SchedulingPolicy == nil || *converted.SchedulingPolicy != policy {
		t.Fatalf("SchedulingPolicy = %#v", converted.SchedulingPolicy)
	}
	if converted.ModelPriority == nil || converted.ModelPriority.RequestedModel != "gpt-test" ||
		converted.ModelPriority.SourceEndpointFamily != "responses" || converted.ModelPriority.RankByAccountID["acc-b"] != 1 {
		t.Fatalf("ModelPriority = %#v", converted.ModelPriority)
	}
	if converted.TrafficMigrationScope == nil || converted.TrafficMigrationScope.GroupID != "grp-1" ||
		converted.TrafficMigrationScope.SystemAccountID != "sys-1" || converted.TrafficMigrationScope.APIKeyID != "key-1" {
		t.Fatalf("TrafficMigrationScope = %#v", converted.TrafficMigrationScope)
	}

	empty := chainDispatchOrderingOptionsOf(gatewaydispatch.AffinityOrderingOptions{})
	if empty.SchedulingPolicy != nil || empty.ModelPriority != nil || empty.TrafficMigrationScope != nil || empty.GroupType != "" {
		t.Fatalf("empty mapping = %#v", empty)
	}
}

// TestChainSessionAffinityDispatchPortRedisRoundTrip: 适配器四方法经
// AffinityService 落 miniredis；排序读侧（preauth 同一服务实例）可见；
// OrderAsync 消费 ModelPriority 排序选项。
func TestChainSessionAffinityDispatchPortRedisRoundTrip(t *testing.T) {
	mr := miniredis.RunT(t)
	service := w17RedisAffinityService(t, mr, "w17-port")
	port := &chainSessionAffinityDispatchPort{service: service}
	ctx := context.Background()
	scope := gatewaydispatch.AffinityScope{SystemAccountID: "sys-1", APIKeyID: "key-1", GroupID: "grp-1"}

	// RememberAsync：dispatch 写 → Redis binding 键存在。
	port.RememberAsync(ctx, "aff_w17_remember", "acc-b", scope)
	if !w17BindingKeyExists(t, mr, "aff_w17_remember") {
		t.Fatal("RememberAsync 必须把绑定写入 Redis")
	}

	// OrderAsync：preauth 读侧（同一 AffinityService）可见——绑定账户置前。
	ordered, err := port.OrderAsync(ctx, []gatewaydispatch.AccountCandidate{w17Account("acc-a"), w17Account("acc-b")}, "aff_w17_remember", gatewaydispatch.AffinityOrderingOptions{})
	if err != nil {
		t.Fatalf("OrderAsync: %v", err)
	}
	if len(ordered) != 2 || ordered[0].ID != "acc-b" || ordered[1].ID != "acc-a" {
		t.Fatalf("OrderAsync = %s,%s; want acc-b,acc-a", ordered[0].ID, ordered[1].ID)
	}

	// OrderAsync options 传递：无亲和绑定时 ModelPriority rank 决定顺序
	//（localSessionAffinity 丢弃 options，无此行为）。
	modelPriority := &gatewaydispatch.ModelPriority{RankByAccountID: map[string]int{"acc-a": 1, "acc-b": 0}}
	ordered, err = port.OrderAsync(ctx, []gatewaydispatch.AccountCandidate{w17Account("acc-a"), w17Account("acc-b")}, "aff_w17_model_priority", gatewaydispatch.AffinityOrderingOptions{ModelPriority: modelPriority})
	if err != nil {
		t.Fatalf("OrderAsync with options: %v", err)
	}
	if ordered[0].ID != "acc-b" {
		t.Fatalf("ModelPriority 排序未生效: ordered[0] = %s", ordered[0].ID)
	}

	// ClaimAsync：first binder wins，重复 claim 返回既有 owner。
	owner, ok := port.ClaimAsync(ctx, "aff_w17_claim", "acc-a", scope)
	if !ok || owner != "acc-a" {
		t.Fatalf("ClaimAsync = (%q, %v); want (acc-a, true)", owner, ok)
	}
	if !w17BindingKeyExists(t, mr, "aff_w17_claim") {
		t.Fatal("ClaimAsync 必须把绑定写入 Redis")
	}
	owner, _ = port.ClaimAsync(ctx, "aff_w17_claim", "acc-b", scope)
	if owner != "acc-a" {
		t.Fatalf("重复 claim 应返回既有绑定 owner = %q", owner)
	}

	// ForgetAsync：Redis 绑定清理。
	if err := port.ForgetAsync(ctx, "aff_w17_remember", "acc-b"); err != nil {
		t.Fatalf("ForgetAsync: %v", err)
	}
	if w17BindingKeyExists(t, mr, "aff_w17_remember") {
		t.Fatal("ForgetAsync 必须清理 Redis 绑定")
	}
}

// TestComposeGatewayChainAffinityPortRedisShared: Redis cacheDriver 下
// engine.Affinity 切到共享 AffinityService 适配器——dispatch 写入落 Redis，
// preauth 读侧（services.Identity.Affinity）可见。
func TestComposeGatewayChainAffinityPortRedisShared(t *testing.T) {
	server := miniredis.RunT(t)
	cfg := composeTestConfig(t)
	cfg.RedisNamespace = "w17-compose"
	cfg.RedisURL = "redis://" + server.Addr()
	fixture := newChainFixture(t)
	composed := &composition{db: fixture.db, statsDB: fixture.statsDB, Bus: nil}
	services, servicesErr := composeChainRuntimeServices(composed, cfg, func(string) (string, error) { return "UTC", nil })
	if servicesErr != nil {
		t.Fatalf("composeChainRuntimeServices: %v", servicesErr)
	}
	t.Cleanup(services.Close)
	if services.Identity == nil || services.Identity.Affinity == nil || !services.Identity.Affinity.UsesRedisSessionAffinity() {
		t.Fatalf("Redis cacheDriver 下 Identity.Affinity 必须为 Redis 驱动, got %#v", services.Identity)
	}

	deps := chainSmokeDeps(t, fixture, gatewaypreauth.SystemClock{}, filepath.Join(t.TempDir(), "spool"))
	deps.Identity = services.Identity
	chain, shutdown, assembleErr := composeGatewayChain(deps)
	if assembleErr != nil {
		t.Fatalf("composeGatewayChain: %v", assembleErr)
	}
	defer shutdown()

	port, isAdapter := chain.engine.Affinity.(*chainSessionAffinityDispatchPort)
	if !isAdapter {
		t.Fatalf("Redis cacheDriver 下 engine.Affinity 必须是 chainSessionAffinityDispatchPort, got %T", chain.engine.Affinity)
	}
	if port.service != services.Identity.Affinity {
		t.Fatal("dispatch 适配器必须与 preauth 共用同一 AffinityService 实例")
	}

	// dispatch 写 → miniredis → preauth 读侧可见（端到端两层贯通）。
	scope := gatewaydispatch.AffinityScope{SystemAccountID: "sys-1", APIKeyID: "key-1", GroupID: "grp-1"}
	chain.engine.Affinity.RememberAsync(context.Background(), "aff_w17_wiring", "acc-b", scope)
	if !w17BindingKeyExists(t, server, "aff_w17_wiring") {
		t.Fatal("dispatch 写入必须落 Redis")
	}
	ordered, orderErr := services.Identity.Affinity.OrderOpenAIAccountsBySessionAffinityAsync(
		context.Background(),
		[]gatewayruntimecache.OpenAIAccountSecret{w17Account("acc-a"), w17Account("acc-b")},
		"aff_w17_wiring",
		gatewaysession.DispatchOrderingOptions{},
	)
	if orderErr != nil {
		t.Fatalf("preauth 读侧 OrderAsync: %v", orderErr)
	}
	if len(ordered) != 2 || ordered[0].ID != "acc-b" {
		t.Fatalf("preauth 读侧未看到 dispatch 写入的亲和: ordered[0] = %s", ordered[0].ID)
	}
}

// w17SlotMemberExists reports whether the session is recorded in the given
// account's session-slot ZSET.
func w17SlotMemberExists(t *testing.T, mr *miniredis.Miniredis, accountID, affinityKey string) bool {
	t.Helper()
	suffix := "session-affinity:slot:" + accountID
	for _, key := range mr.Keys() {
		if !strings.HasSuffix(key, suffix) {
			continue
		}
		members, err := mr.ZMembers(key)
		if err != nil {
			t.Fatalf("ZMembers(%s): %v", key, err)
		}
		for _, member := range members {
			if member == affinityKey {
				return true
			}
		}
	}
	return false
}

// TestChainSessionAffinityDispatchPortSlotPlanAndConfirm: 名额端口经适配器
// 落 Redis，确认后写入新 owner 名额并释放旧 owner 名额。
func TestChainSessionAffinityDispatchPortSlotPlanAndConfirm(t *testing.T) {
	mr := miniredis.RunT(t)
	service := w17RedisAffinityService(t, mr, "w17-slot")
	port := &chainSessionAffinityDispatchPort{service: service}
	ctx := context.Background()
	scope := gatewaydispatch.AffinityScope{SystemAccountID: "sys-1", APIKeyID: "key-1", GroupID: "grp-1"}
	if _, ok := port.ClaimAsync(ctx, "aff_slot_port", "acc-a", scope); !ok {
		t.Fatal("ClaimAsync failed")
	}
	if !port.RebindAsync(ctx, "aff_slot_port", "acc-a", "acc-a", scope) {
		t.Fatal("RebindAsync(acc-a) failed")
	}
	candidates := []gatewaydispatch.SessionSlotCandidate{
		{AccountID: "acc-a", ConcurrencyLimit: 10},
		{AccountID: "acc-b", ConcurrencyLimit: 10},
	}
	plan, err := port.PlanSessionSlot(ctx, "aff_slot_port", "", false, candidates, 0)
	if err != nil {
		t.Fatalf("PlanSessionSlot: %v", err)
	}
	if plan.PreferredAccountID != "acc-a" || plan.Fallback {
		t.Fatalf("new-session plan = %+v, want acc-a", plan)
	}
	cap := gatewaysession.SessionSlotCap(10, 0)
	if !port.ConfirmSessionSlot(ctx, "aff_slot_port", "", "acc-a", cap) {
		t.Fatal("ConfirmSessionSlot(acc-a) = false")
	}
	if !w17SlotMemberExists(t, mr, "acc-a", "aff_slot_port") {
		t.Fatal("acc-a 名额未写入 Redis")
	}
	plan, err = port.PlanSessionSlot(ctx, "aff_slot_port", "acc-a", true, candidates, 0)
	if err != nil {
		t.Fatalf("PlanSessionSlot(owner): %v", err)
	}
	if plan.PreferredAccountID != "acc-a" || !plan.SlotActive {
		t.Fatalf("owner plan = %+v, want active acc-a", plan)
	}
	if !port.RebindAsync(ctx, "aff_slot_port", "acc-a", "acc-b", scope) {
		t.Fatal("RebindAsync(acc-b) failed")
	}
	if !port.ConfirmSessionSlot(ctx, "aff_slot_port", "acc-a", "acc-b", cap) {
		t.Fatal("ConfirmSessionSlot(acc-b) = false")
	}
	if w17SlotMemberExists(t, mr, "acc-a", "aff_slot_port") {
		t.Fatal("旧 owner 名额必须释放")
	}
	if !w17SlotMemberExists(t, mr, "acc-b", "aff_slot_port") {
		t.Fatal("新 owner 名额必须写入")
	}
}

// TestComposeChainRuntimeServicesWiresDispatchRecoverableWait: G11 等待引擎
// 同时挂 preauth Recoverable 与 dispatch 侧句柄（同一实例）。
func TestComposeChainRuntimeServicesWiresDispatchRecoverableWait(t *testing.T) {
	fixture := newChainFixture(t)
	cfg := composeTestConfig(t)
	composed := &composition{db: fixture.db, statsDB: fixture.statsDB, Bus: nil}
	services, err := composeChainRuntimeServices(composed, cfg, func(string) (string, error) { return "UTC", nil })
	if err != nil {
		t.Fatalf("composeChainRuntimeServices: %v", err)
	}
	t.Cleanup(services.Close)
	if services.Recoverable == nil || services.DispatchRecoverableWait == nil {
		t.Fatalf("Recoverable / DispatchRecoverableWait 必须同时装配: %v / %v", services.Recoverable, services.DispatchRecoverableWait)
	}
}

// TestComposeGatewayChainWiresEngineRecoverableWait: 组合根把
// DispatchRecoverableWait 适配成 engine.RecoverableWait；nil（既有组合测试
// 默认）保持引擎缺席语义。
func TestComposeGatewayChainWiresEngineRecoverableWait(t *testing.T) {
	fixture := newChainFixture(t)
	deps := chainSmokeDeps(t, fixture, gatewaypreauth.SystemClock{}, filepath.Join(t.TempDir(), "spool"))
	chain, shutdown, assembleErr := composeGatewayChain(deps)
	if assembleErr != nil {
		t.Fatalf("composeGatewayChain: %v", assembleErr)
	}
	defer shutdown()
	if chain.engine.RecoverableWait != nil {
		t.Fatalf("nil DispatchRecoverableWait 下 engine.RecoverableWait 必须保持缺席, got %T", chain.engine.RecoverableWait)
	}

	wiredDeps := chainSmokeDeps(t, fixture, gatewaypreauth.SystemClock{}, filepath.Join(t.TempDir(), "spool"))
	wiredDeps.DispatchRecoverableWait = newW17DispatchRecoverableWait()
	wiredChain, wiredShutdown, wiredErr := composeGatewayChain(wiredDeps)
	if wiredErr != nil {
		t.Fatalf("composeGatewayChain (wired): %v", wiredErr)
	}
	defer wiredShutdown()
	if _, ok := wiredChain.engine.RecoverableWait.(*chainDispatchRecoverableWait); !ok {
		t.Fatalf("engine.RecoverableWait 必须是 chainDispatchRecoverableWait, got %T", wiredChain.engine.RecoverableWait)
	}
}

// newW17DispatchRecoverableWait mirrors the production G11 assembly shape
// (Coordinator + nop logger) for wiring tests.
func newW17DispatchRecoverableWait() *gatewaycircuit.PreAuthRecoverableWait {
	return gatewaycircuit.NewPreAuthRecoverableWait(
		gatewaycircuit.NewWaitCoordinator(gatewaycircuit.WaitCoordinatorOptions{}),
		w17NopWaitLogger{},
	)
}

type w17NopWaitLogger struct{}

func (w17NopWaitLogger) Info(map[string]any, string) {}

func (w17NopWaitLogger) Warn(map[string]any, string) {}

// TestChainSessionAffinityDispatchPortRebind: 账户亲和同层 latch 的链端口
// 重绑走同一 AffinityService；同 owner 刷新 lease、异 owner CAS 改绑、
// expected owner 不匹配时 no-op。
func TestChainSessionAffinityDispatchPortRebind(t *testing.T) {
	mr := miniredis.RunT(t)
	service := w17RedisAffinityService(t, mr, "w17-rebind")
	port := &chainSessionAffinityDispatchPort{service: service}
	ctx := context.Background()
	scope := gatewaydispatch.AffinityScope{SystemAccountID: "sys-1", APIKeyID: "key-1", GroupID: "grp-1"}

	port.RememberAsync(ctx, "aff_w17_rebind", "acc-a", scope)
	if !port.RebindAsync(ctx, "aff_w17_rebind", "acc-a", "acc-a", scope) {
		t.Fatal("same-owner lease refresh must succeed")
	}
	if !port.RebindAsync(ctx, "aff_w17_rebind", "acc-a", "acc-b", scope) {
		t.Fatal("same-layer rebind a->b must succeed")
	}
	if owner, ok := port.ClaimAsync(ctx, "aff_w17_rebind", "acc-c", scope); !ok || owner != "acc-b" {
		t.Fatalf("claim after rebind = %q %v, want acc-b", owner, ok)
	}
	if port.RebindAsync(ctx, "aff_w17_rebind", "acc-a", "acc-c", scope) {
		t.Fatal("rebind with a stale expected owner must be a no-op")
	}
}

// TestChainSessionAffinityDispatchPortSoftThresholdOrder: 生产 Redis 适配器
// 透传普通分组软阈值策略；owner 达到 softBusy 时同层 peer 先于 owner，
// 保证准备期 soft_threshold 让位路径可达（memory 降级适配器不实现该排序）。
func TestChainSessionAffinityDispatchPortSoftThresholdOrder(t *testing.T) {
	mr := miniredis.RunT(t)
	service := w17RedisAffinityService(t, mr, "w17-soft-order")
	port := &chainSessionAffinityDispatchPort{service: service}
	ctx := context.Background()
	scope := gatewaydispatch.AffinityScope{SystemAccountID: "sys-1", APIKeyID: "key-1", GroupID: "grp-1"}
	key := "aff_w17_soft_order"

	if owner, ok := port.ClaimAsync(ctx, key, "acc-a", scope); !ok || owner != "acc-a" {
		t.Fatalf("claim = %q %v", owner, ok)
	}
	ownerConcurrency, peerConcurrency := 1, 0
	owner := w17Account("acc-a")
	owner.CurrentConcurrency = &ownerConcurrency
	peer := w17Account("acc-b")
	peer.CurrentConcurrency = &peerConcurrency
	policy := &gatewayruntimecache.GroupSchedulingPolicy{DefaultSoftConcurrency: 1, ClientIPConcurrencyLimit: 0}

	ordered, err := port.OrderAsync(ctx, []gatewaydispatch.AccountCandidate{owner, peer}, key, gatewaydispatch.AffinityOrderingOptions{
		GroupType:        "personal",
		SchedulingPolicy: policy,
	})
	if err != nil {
		t.Fatalf("OrderAsync: %v", err)
	}
	if len(ordered) != 2 || ordered[0].ID != "acc-b" {
		t.Fatalf("soft-busy owner must yield to same-layer peer, got %v", idsOfCandidates(ordered))
	}
}

func idsOfCandidates(accounts []gatewaydispatch.AccountCandidate) []string {
	out := make([]string, 0, len(accounts))
	for _, account := range accounts {
		out = append(out, account.ID)
	}
	return out
}

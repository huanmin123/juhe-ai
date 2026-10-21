package gatewayruntimecache

import (
	"context"
	"sync"
	"testing"
)

// 组级会话亲和三阶段（策略路由分组会话亲和设计 §4/§8 单元测试矩阵）：
// 静态身份快照不调用排序器、sticky hit 纯 peek 零状态推进、miss 恰好推进一次、
// AdvanceOnStickyMiss=false 零推进（最终 profile 重解析阶段）、StaticResolutionOnly
// 标记在完成第 3 步后清除（防重入二次推进）。

type spyGroupOrderer struct {
	mu         sync.Mutex
	orderCalls int
	peekCalls  int
}

func (s *spyGroupOrderer) OrderAPIKeyGroupBindings(_ context.Context, apiKey GatewayAPIKeyRow) ([]GatewayAPIKeyGroupBindingRow, error) {
	s.mu.Lock()
	s.orderCalls++
	s.mu.Unlock()
	return apiKey.GroupBindings, nil
}

func (s *spyGroupOrderer) PeekAPIKeyGroupBindings(_ context.Context, apiKey GatewayAPIKeyRow) ([]GatewayAPIKeyGroupBindingRow, error) {
	s.mu.Lock()
	s.peekCalls++
	s.mu.Unlock()
	return apiKey.GroupBindings, nil
}

func (s *spyGroupOrderer) counts() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.orderCalls, s.peekCalls
}

func groupStickyTestService(t *testing.T, spy *spyGroupOrderer) (*Service, *fakeModels) {
	t.Helper()
	models := newFakeModels()
	for _, groupID := range []string{"grp_a", "grp_b"} {
		access := &GroupUsageAccessMetadata{GroupOwnerSystemAccountID: "sys_owner"}
		models.groupAccess[groupID+":sys_owner"] = access
		models.accounts[groupID] = OpenAIAccountsForGroupResult{Accounts: []OpenAIAccountSecret{testAccount("acc-"+groupID, "sys_owner")}}
	}
	svc := newTestService(t, models, newManualClock(), func(o *Options) {
		o.Orderer = spy
	})
	return svc, models
}

func TestReadCachedGatewayRuntimeStaticSkipsOrderer(t *testing.T) {
	spy := &spyGroupOrderer{}
	svc, models := groupStickyTestService(t, spy)
	models.runtimes["sk-rr"] = staticRuntime(t, models, testAPIKeyRow("k_rr", RouteStrategyModeRoundRobin, "grp_a", "grp_b"), nil, nil)
	ctx := context.Background()

	// 静态读：不调用排序器、不预选分组（SelectedGroupID 保持存储序首位）、
	// 打 StaticResolutionOnly 标记。
	staticRuntimeRow, err := svc.ReadCachedGatewayRuntimeStaticAsync(ctx, "sk-rr")
	if err != nil {
		t.Fatal(err)
	}
	if !staticRuntimeRow.StaticResolutionOnly {
		t.Fatal("静态读必须打 StaticResolutionOnly 标记")
	}
	if staticRuntimeRow.APIKey == nil || staticRuntimeRow.APIKey.SelectedGroupID != "grp_a" {
		t.Fatalf("静态读 SelectedGroupID = %v, want grp_a（存储序首位）", staticRuntimeRow.APIKey)
	}
	if orderCalls, peekCalls := spy.counts(); orderCalls != 0 || peekCalls != 0 {
		t.Fatalf("静态读排序器调用 order=%d peek=%d, want 0/0（身份快照阶段不得消费状态）", orderCalls, peekCalls)
	}

	// 对照：完整读推进一次（既有行为不变）。
	if _, err := svc.ReadCachedGatewayRuntimeAsync(ctx, "sk-rr"); err != nil {
		t.Fatal(err)
	}
	if orderCalls, _ := spy.counts(); orderCalls != 1 {
		t.Fatalf("完整读排序器调用 = %d, want 1", orderCalls)
	}
}

func groupStickyStaticRow(t *testing.T, svc *Service, models *fakeModels, mode string, groups ...string) GatewayRuntime {
	t.Helper()
	models.runtimes["sk-sticky"] = staticRuntime(t, models, testAPIKeyRow("k_sticky", mode, groups...), nil, nil)
	row, err := svc.ReadCachedGatewayRuntimeStaticAsync(context.Background(), "sk-sticky")
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func TestSelectGatewayRuntimeStickyHitPeeksZeroState(t *testing.T) {
	spy := &spyGroupOrderer{}
	svc, models := groupStickyTestService(t, spy)
	static := groupStickyStaticRow(t, svc, models, RouteStrategyModeRoundRobin, "grp_a", "grp_b")

	selected, err := svc.SelectGatewayRuntimeForDispatchAsync(context.Background(), static, SelectGatewayRuntimeInput{PreferredGroupID: "grp_b"})
	if err != nil {
		t.Fatal(err)
	}
	if selected.APIKey == nil || selected.APIKey.SelectedGroupID != "grp_b" {
		t.Fatalf("黏住选中组 = %v, want grp_b", selected.APIKey)
	}
	if len(selected.Accounts) != 1 || selected.Accounts[0].ID != "acc-grp_b" {
		t.Fatalf("黏住组账户 = %+v, want acc-grp_b", selected.Accounts)
	}
	if orderCalls, peekCalls := spy.counts(); orderCalls != 0 || peekCalls != 1 {
		t.Fatalf("sticky hit 排序器调用 order=%d peek=%d, want 0/1（零状态推进）", orderCalls, peekCalls)
	}
	if selected.StaticResolutionOnly {
		t.Fatal("完成第 3 步后 StaticResolutionOnly 必须清除（防重入二次推进）")
	}
}

func TestSelectGatewayRuntimeStickyHitIsStableAcrossCalls(t *testing.T) {
	spy := &spyGroupOrderer{}
	svc, models := groupStickyTestService(t, spy)
	static := groupStickyStaticRow(t, svc, models, RouteStrategyModeRoundRobin, "grp_a", "grp_b")
	ctx := context.Background()

	// 同一会话连续多次黏住请求：peek 序列恒等，状态永不被消费（轮转计数器
	// 与「只跑无会话请求」的基线一致，设计验收 7）。
	for i := 0; i < 5; i++ {
		selected, err := svc.SelectGatewayRuntimeForDispatchAsync(ctx, static, SelectGatewayRuntimeInput{PreferredGroupID: "grp_b"})
		if err != nil {
			t.Fatal(err)
		}
		if selected.APIKey.SelectedGroupID != "grp_b" {
			t.Fatalf("第 %d 次黏住选中组 = %s, want grp_b", i+1, selected.APIKey.SelectedGroupID)
		}
	}
	if orderCalls, peekCalls := spy.counts(); orderCalls != 0 || peekCalls != 5 {
		t.Fatalf("连续黏住调用 order=%d peek=%d, want 0/5", orderCalls, peekCalls)
	}
}

func TestSelectGatewayRuntimeStickyGroupWithoutAccountsFallsThrough(t *testing.T) {
	spy := &spyGroupOrderer{}
	svc, models := groupStickyTestService(t, spy)
	// 黏住组 grp_b 在活跃绑定中但组内无可派发账户：硬过滤优先于黏性（§4
	// 关键点 1），walk 落下一组，仍零状态消费（重绑由最终成功写回完成）。
	models.accounts["grp_b"] = OpenAIAccountsForGroupResult{Accounts: []OpenAIAccountSecret{}}
	static := groupStickyStaticRow(t, svc, models, RouteStrategyModeRoundRobin, "grp_a", "grp_b")

	selected, err := svc.SelectGatewayRuntimeForDispatchAsync(context.Background(), static, SelectGatewayRuntimeInput{PreferredGroupID: "grp_b"})
	if err != nil {
		t.Fatal(err)
	}
	if selected.APIKey == nil || selected.APIKey.SelectedGroupID != "grp_a" {
		t.Fatalf("黏住组无候选应落下一组，选中 = %v, want grp_a", selected.APIKey)
	}
	if orderCalls, peekCalls := spy.counts(); orderCalls != 0 || peekCalls != 1 {
		t.Fatalf("排序器调用 order=%d peek=%d, want 0/1（落组 walk 零状态消费）", orderCalls, peekCalls)
	}
}

func TestSelectGatewayRuntimeMissAdvancesExactlyOnce(t *testing.T) {
	spy := &spyGroupOrderer{}
	svc, models := groupStickyTestService(t, spy)
	static := groupStickyStaticRow(t, svc, models, RouteStrategyModeRoundRobin, "grp_a", "grp_b")

	selected, err := svc.SelectGatewayRuntimeForDispatchAsync(context.Background(), static, SelectGatewayRuntimeInput{AdvanceOnStickyMiss: true})
	if err != nil {
		t.Fatal(err)
	}
	if selected.APIKey == nil || selected.APIKey.SelectedGroupID != "grp_a" {
		t.Fatalf("无黏住选中组 = %v, want grp_a（存储序首推）", selected.APIKey)
	}
	if orderCalls, peekCalls := spy.counts(); orderCalls != 1 || peekCalls != 0 {
		t.Fatalf("miss 路径排序器调用 order=%d peek=%d, want 1/0（恰好推进一次）", orderCalls, peekCalls)
	}
	if selected.StaticResolutionOnly {
		t.Fatal("完成第 3 步后 StaticResolutionOnly 必须清除")
	}
}

func TestSelectGatewayRuntimeMissWithoutAdvanceKeepsStaticOrder(t *testing.T) {
	spy := &spyGroupOrderer{}
	svc, models := groupStickyTestService(t, spy)
	static := groupStickyStaticRow(t, svc, models, RouteStrategyModeRoundRobin, "grp_a", "grp_b")

	// 最终 profile 重解析阶段的 miss：初始阶段已承担推进预算，此处零推进、
	// 保持既有派发序（§4 关键点 5 不得推进两次）。
	selected, err := svc.SelectGatewayRuntimeForDispatchAsync(context.Background(), static, SelectGatewayRuntimeInput{})
	if err != nil {
		t.Fatal(err)
	}
	if selected.APIKey == nil || selected.APIKey.SelectedGroupID != "grp_a" {
		t.Fatalf("零推进选中组 = %v, want grp_a", selected.APIKey)
	}
	if orderCalls, peekCalls := spy.counts(); orderCalls != 0 || peekCalls != 0 {
		t.Fatalf("AdvanceOnStickyMiss=false 调用 order=%d peek=%d, want 0/0", orderCalls, peekCalls)
	}
}

func TestSelectGatewayRuntimeStickyGroupRemovedBehavesAsMiss(t *testing.T) {
	spy := &spyGroupOrderer{}
	svc, models := groupStickyTestService(t, spy)
	static := groupStickyStaticRow(t, svc, models, RouteStrategyModeRoundRobin, "grp_a", "grp_b")

	// 绑定组已被移出绑定：视同未命中（§6），按 peek 序走第一个 viable 组，
	// 不消费模式状态（重绑由最终成功写回完成）。
	selected, err := svc.SelectGatewayRuntimeForDispatchAsync(context.Background(), static, SelectGatewayRuntimeInput{PreferredGroupID: "grp_z"})
	if err != nil {
		t.Fatal(err)
	}
	if selected.APIKey == nil || selected.APIKey.SelectedGroupID != "grp_a" {
		t.Fatalf("移出绑定后选中组 = %v, want grp_a", selected.APIKey)
	}
	if orderCalls, _ := spy.counts(); orderCalls != 0 {
		t.Fatalf("移出绑定 sticky 不得推进状态，order=%d", orderCalls)
	}
}

func TestSelectGatewayRuntimeFailoverStickyReorders(t *testing.T) {
	spy := &spyGroupOrderer{}
	svc, models := groupStickyTestService(t, spy)
	static := groupStickyStaticRow(t, svc, models, RouteStrategyModeFailover, "grp_a", "grp_b")
	ctx := context.Background()

	// failover 无状态：黏住命中重排（黏备组不回切），未命中原样。
	sticky, err := svc.SelectGatewayRuntimeForDispatchAsync(ctx, static, SelectGatewayRuntimeInput{PreferredGroupID: "grp_b"})
	if err != nil {
		t.Fatal(err)
	}
	if sticky.APIKey.SelectedGroupID != "grp_b" {
		t.Fatalf("failover 黏住选中组 = %s, want grp_b", sticky.APIKey.SelectedGroupID)
	}
	noSticky, err := svc.SelectGatewayRuntimeForDispatchAsync(ctx, static, SelectGatewayRuntimeInput{})
	if err != nil {
		t.Fatal(err)
	}
	if noSticky.APIKey.SelectedGroupID != "grp_a" {
		t.Fatalf("failover 无黏住选中组 = %s, want grp_a（优先序首位）", noSticky.APIKey.SelectedGroupID)
	}
	if orderCalls, peekCalls := spy.counts(); orderCalls != 0 || peekCalls != 0 {
		t.Fatalf("failover 无状态，调用 order=%d peek=%d, want 0/0", orderCalls, peekCalls)
	}
	if sticky.StaticResolutionOnly || noSticky.StaticResolutionOnly {
		t.Fatal("完成第 3 步后 StaticResolutionOnly 必须清除")
	}
}

func TestSelectGatewayRuntimeNormalIsNoop(t *testing.T) {
	spy := &spyGroupOrderer{}
	svc, models := groupStickyTestService(t, spy)
	ctx := context.Background()
	for _, mode := range []string{RouteStrategyModeNormal} {
		static := groupStickyStaticRow(t, svc, models, mode, "grp_a", "grp_b")
		selected, err := svc.SelectGatewayRuntimeForDispatchAsync(ctx, static, SelectGatewayRuntimeInput{PreferredGroupID: "grp_b", AdvanceOnStickyMiss: true})
		if err != nil {
			t.Fatal(err)
		}
		if selected.APIKey.SelectedGroupID != "grp_a" {
			t.Fatalf("%s 不重排，选中组 = %s, want grp_a", mode, selected.APIKey.SelectedGroupID)
		}
		if orderCalls, peekCalls := spy.counts(); orderCalls != 0 || peekCalls != 0 {
			t.Fatalf("%s 排序器调用 order=%d peek=%d, want 0/0（完全现行行为）", mode, orderCalls, peekCalls)
		}
		if selected.StaticResolutionOnly {
			t.Fatalf("%s 完成第 3 步后标记必须清除", mode)
		}
	}
}

package gatewayrouting

import (
	"context"
	"testing"
)

// 组级会话亲和（策略路由分组会话亲和设计 §8 单元测试矩阵）：黏住重排、模式
// 范围边界、peek 不推进 RR/weighted 状态。Redis counter 驱动的 peek 语义由
// rediscounter peek 脚本的 GET-only 实现保证（无 INCR / 无 PEXPIRE）。

func TestGroupStickyApplicable(t *testing.T) {
	cases := []struct {
		mode   string
		count  int
		expect bool
	}{
		{RouteStrategyModeRoundRobin, 2, true},
		{RouteStrategyModeWeighted, 3, true},
		{RouteStrategyModeFailover, 2, true},
		{RouteStrategyModeNormal, 2, false},
		{RouteStrategyModeMerge, 2, false},
		{RouteStrategyModeRoundRobin, 1, false},
		{RouteStrategyModeFailover, 0, false},
		{"", 5, false},
	}
	for _, tc := range cases {
		if got := GroupStickyApplicable(tc.mode, tc.count); got != tc.expect {
			t.Fatalf("GroupStickyApplicable(%q, %d) = %v, want %v", tc.mode, tc.count, got, tc.expect)
		}
	}
}

func TestReorderBindingsForStickyGroup(t *testing.T) {
	bindings := []GroupBindingRow{
		{ID: "b1", GroupID: "grp_a"}, {ID: "b2", GroupID: "grp_b"}, {ID: "b3", GroupID: "grp_c"},
	}
	assertIDs := func(got []GroupBindingRow, want []string) {
		t.Helper()
		ids := make([]string, 0, len(got))
		for _, row := range got {
			ids = append(ids, row.ID)
		}
		if len(ids) != len(want) {
			t.Fatalf("ids = %v, want %v", ids, want)
		}
		for i := range want {
			if ids[i] != want[i] {
				t.Fatalf("ids = %v, want %v", ids, want)
			}
		}
	}

	reordered := ReorderBindingsForStickyGroup(bindings, "grp_c")
	assertIDs(reordered, []string{"b3", "b1", "b2"})

	// 已在首位：原样（不复制不换序）。
	assertIDs(ReorderBindingsForStickyGroup(bindings, "grp_a"), []string{"b1", "b2", "b3"})
	// 不在列表中：视同未命中，原样返回。
	assertIDs(ReorderBindingsForStickyGroup(bindings, "grp_z"), []string{"b1", "b2", "b3"})
	// 空黏住组 / 单绑定：原样。
	assertIDs(ReorderBindingsForStickyGroup(bindings, ""), []string{"b1", "b2", "b3"})
	single := []GroupBindingRow{{ID: "b1", GroupID: "grp_a"}}
	assertIDs(ReorderBindingsForStickyGroup(single, "grp_a"), []string{"b1"})
}

func TestPeekAPIKeyGroupBindingsRoundRobinDoesNotAdvance(t *testing.T) {
	selector := NewAPIKeyGroupRouteSelector("memory", nil, "")
	apiKey := &APIKeyRow{
		ID: "key1", RouteStrategyID: "strategy_rr", RouteStrategyMode: RouteStrategyModeRoundRobin,
		GroupBindings: []GroupBindingRow{
			{ID: "b1", GroupID: "grp_a", Status: RowStatusActive, GroupEnabled: 1, Priority: 1, Weight: int64PtrOf(1)},
			{ID: "b2", GroupID: "grp_b", Status: RowStatusActive, GroupEnabled: 1, Priority: 2, Weight: int64PtrOf(1)},
			{ID: "b3", GroupID: "grp_c", Status: RowStatusActive, GroupEnabled: 1, Priority: 3, Weight: int64PtrOf(1)},
		},
	}
	ctx := context.Background()

	// 基线：Order 一次的序列（推进一格）。
	orderedOnce, err := selector.OrderAPIKeyGroupBindingsForDispatchAsync(ctx, apiKey)
	if err != nil {
		t.Fatal(err)
	}
	wantAfterOneAdvance := bindingIDs(orderedOnce)

	// 全新 selector：连续 peek 多次必须返回同一序列（零状态推进），且该序列
	// 等于基线 selector 推进一格后的结果（peek 读的就是"下一次推进将产生"的
	// 派发序）。
	fresh := NewAPIKeyGroupRouteSelector("memory", nil, "")
	firstPeek, err := fresh.PeekAPIKeyGroupBindingsForDispatchAsync(ctx, apiKey)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		peeked, err := fresh.PeekAPIKeyGroupBindingsForDispatchAsync(ctx, apiKey)
		if err != nil {
			t.Fatal(err)
		}
		if got := bindingIDs(peeked); !equalStrings(got, bindingIDs(firstPeek)) {
			t.Fatalf("第 %d 次 peek 序列漂移：%v，首 peek = %v", i+2, got, bindingIDs(firstPeek))
		}
	}
	if !equalStrings(bindingIDs(firstPeek), wantAfterOneAdvance) {
		t.Fatalf("peek 序列 = %v, want 推进一格后的 %v", bindingIDs(firstPeek), wantAfterOneAdvance)
	}

	// 佐证 peek 后状态未被消费：fresh 直接 Order 得到的序列等于基线 selector
	// 的首次 Order（出厂状态的首推）——peek 前状态 == 出厂状态。
	orderedAfterPeeks, err := fresh.OrderAPIKeyGroupBindingsForDispatchAsync(ctx, apiKey)
	if err != nil {
		t.Fatal(err)
	}
	baseline := NewAPIKeyGroupRouteSelector("memory", nil, "")
	baselineFirst, err := baseline.OrderAPIKeyGroupBindingsForDispatchAsync(ctx, apiKey)
	if err != nil {
		t.Fatal(err)
	}
	if !equalStrings(bindingIDs(orderedAfterPeeks), bindingIDs(baselineFirst)) {
		t.Fatalf("peek 后 Order 序列 = %v, want 基线首推 %v（peek 消费了状态）",
			bindingIDs(orderedAfterPeeks), bindingIDs(baselineFirst))
	}
}

func TestPeekAPIKeyGroupBindingsWeightedDoesNotMutateDebt(t *testing.T) {
	weight := int64PtrOf
	selector := NewAPIKeyGroupRouteSelector("memory", nil, "")
	apiKey := &APIKeyRow{
		ID: "key1", RouteStrategyID: "strategy_w", RouteStrategyMode: RouteStrategyModeWeighted,
		GroupBindings: []GroupBindingRow{
			{ID: "b1", GroupID: "grp_a", Status: RowStatusActive, GroupEnabled: 1, Priority: 1, Weight: weight(2)},
			{ID: "b2", GroupID: "grp_b", Status: RowStatusActive, GroupEnabled: 1, Priority: 2, Weight: weight(1)},
		},
	}
	ctx := context.Background()

	firstPeek, err := selector.PeekAPIKeyGroupBindingsForDispatchAsync(ctx, apiKey)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 7; i++ {
		peeked, err := selector.PeekAPIKeyGroupBindingsForDispatchAsync(ctx, apiKey)
		if err != nil {
			t.Fatal(err)
		}
		if !equalStrings(bindingIDs(peeked), bindingIDs(firstPeek)) {
			t.Fatalf("weighted 第 %d 次 peek 序列漂移：%v", i+2, bindingIDs(peeked))
		}
	}

	// 对照：加权权重 2:1 下首 peek 必须是权重占优组（smooth weighted 首选
	// b1），且连续 peek 不等价于多次推进。
	if got := bindingIDs(firstPeek)[0]; got != "b1" {
		t.Fatalf("weighted 首 peek 首位 = %s, want b1", got)
	}
	// peek 零 debt 变异：随后一次 Order 的结果 == 全新 selector 首次 Order。
	fresh := NewAPIKeyGroupRouteSelector("memory", nil, "")
	freshOrder, err := fresh.OrderAPIKeyGroupBindingsForDispatchAsync(ctx, apiKey)
	if err != nil {
		t.Fatal(err)
	}
	afterPeeks, err := selector.OrderAPIKeyGroupBindingsForDispatchAsync(ctx, apiKey)
	if err != nil {
		t.Fatal(err)
	}
	if !equalStrings(bindingIDs(afterPeeks), bindingIDs(freshOrder)) {
		t.Fatalf("peek 后 Order = %v, want 全新首推 %v（peek 变异了 debt）",
			bindingIDs(afterPeeks), bindingIDs(freshOrder))
	}
}

func TestPeekAPIKeyGroupBindingsStaticModesKeepPriorityOrder(t *testing.T) {
	selector := NewAPIKeyGroupRouteSelector("memory", nil, "")
	apiKey := &APIKeyRow{
		ID: "key1", RouteStrategyID: "strategy_f", RouteStrategyMode: RouteStrategyModeFailover,
		GroupBindings: []GroupBindingRow{
			{ID: "b2", GroupID: "grp_b", Status: RowStatusActive, GroupEnabled: 1, Priority: 2, Weight: int64PtrOf(1)},
			{ID: "b1", GroupID: "grp_a", Status: RowStatusActive, GroupEnabled: 1, Priority: 1, Weight: int64PtrOf(1)},
		},
	}
	ctx := context.Background()
	peeked, err := selector.PeekAPIKeyGroupBindingsForDispatchAsync(ctx, apiKey)
	if err != nil {
		t.Fatal(err)
	}
	if !equalStrings(bindingIDs(peeked), []string{"b1", "b2"}) {
		t.Fatalf("failover peek = %v, want 优先序 [b1 b2]", bindingIDs(peeked))
	}
}

func int64PtrOf(v int64) *int64 { return &v }

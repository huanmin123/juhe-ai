package gatewayrouting

// E1 大池（5000 绑定）路由状态分布回归测试。
//
// 目标：锁定 routeStateRegistry 的可见分布语义。键去重（线性扫描 → map
// 判重）与 weighted 状态存储（整 map 拷贝替换 → 锁内原位更新）的实现变化，
// 不得改变任何一轮的可见顺序。断言全部为确定性序列或宽边界统计，不依赖
// 随机种子，可在任意实现等价重写下稳定重放：
//   - 轮询：nextRoundRobinIndex 的 state % bindingCount 从 0 起严格递增，
//     5000 绑定下第 k 轮首位 = b_k % 5000，且每轮返回完整置换。
//   - 加权等权：平滑加权在等权下退化为按绑定顺序的精确轮转（第 k 轮首位
//     = b_{k % 5000}，每满一个周期全部 current 归零，周期严格重复）。
//   - 加权混合：每 heavyStride 个绑定一个 weight=heavyWeight，其余
//     weight=1。前 50 轮严格按绑定序选中重权绑定（归纳可证的确定性前缀：
//     未选重权 current=(r+1)*heavyWeight 恒大于灯权 r+1，平局按绑定序）；
//     统计窗口内重权组首位占比以宽边界逼近其权重占比 10000/14900。

import (
	"fmt"
	"testing"
)

const (
	e1LargePoolSize   = 5000
	e1HeavyStride     = 50
	e1HeavyWeight     = int64(100)
	e1MixedRounds     = 1200
	e1EqualWrapRounds = e1LargePoolSize + 20
	// e1MixedBaselineHeavyTotal 录制自优化前实现（见文件尾注释）。
	e1MixedBaselineHeavyTotal = 800
)

// e1LargePoolBindings 构建按 priority/group_id 严格升序的 n 个活跃绑定，
// 权重由 weightOf(i) 决定；归一化后的绑定顺序即 b0000..b{n-1}。
func e1LargePoolBindings(n int, weightOf func(i int) int64) []GroupBindingRow {
	bindings := make([]GroupBindingRow, 0, n)
	for i := 0; i < n; i++ {
		bindings = append(bindings, w9eBinding(
			fmt.Sprintf("b%05d", i),
			fmt.Sprintf("grp_%05d", i),
			int64(i),
			w9eInt64(weightOf(i)),
		))
	}
	return bindings
}

func e1LargePoolAPIKey(mode string, bindings []GroupBindingRow) *APIKeyRow {
	return &APIKeyRow{
		ID:                "e1-large-pool-key",
		RouteStrategyID:   "e1-large-pool-strategy",
		RouteStrategyMode: mode,
		GroupBindings:     bindings,
	}
}

// e1AssertPermutation 校验本轮返回恰好是全部绑定的一个置换。
func e1AssertPermutation(t *testing.T, round int, ordered, bindings []GroupBindingRow) {
	t.Helper()
	if len(ordered) != len(bindings) {
		t.Fatalf("round %d length = %d, want %d", round, len(ordered), len(bindings))
	}
	seen := make(map[string]struct{}, len(bindings))
	for _, binding := range ordered {
		if _, dup := seen[binding.ID]; dup {
			t.Fatalf("round %d duplicate binding %s", round, binding.ID)
		}
		seen[binding.ID] = struct{}{}
	}
	for _, binding := range bindings {
		if _, ok := seen[binding.ID]; !ok {
			t.Fatalf("round %d missing binding %s", round, binding.ID)
		}
	}
}

func TestE1LargePoolRoundRobinRotation(t *testing.T) {
	selector := NewAPIKeyGroupRouteSelector("memory", nil, "")
	bindings := e1LargePoolBindings(e1LargePoolSize, func(int) int64 { return 1 })
	apiKey := e1LargePoolAPIKey(RouteStrategyModeRoundRobin, bindings)

	for round := 0; round <= e1LargePoolSize; round++ {
		ordered, err := selector.OrderAPIKeyGroupBindingsForDispatch(apiKey)
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		wantFirst := bindings[round%e1LargePoolSize].ID
		if ordered[0].ID != wantFirst {
			t.Fatalf("round %d first = %s, want %s", round, ordered[0].ID, wantFirst)
		}
		if round == 0 {
			// 首轮起点为 0：完整顺序必须等于归一化绑定序。
			for i, binding := range ordered {
				if binding.ID != bindings[i].ID {
					t.Fatalf("round 0 position %d = %s, want %s", i, binding.ID, bindings[i].ID)
				}
			}
		}
		if round == e1LargePoolSize {
			// 计数器回绕后的首轮：state 回到 0，完整顺序再次等于归一化序。
			e1AssertPermutation(t, round, ordered, bindings)
			for i, binding := range ordered {
				if binding.ID != bindings[i].ID {
					t.Fatalf("wrap round position %d = %s, want %s", i, binding.ID, bindings[i].ID)
				}
			}
		}
	}
}

func TestE1LargePoolWeightedEqualWeightsRotateInBindingOrder(t *testing.T) {
	selector := NewAPIKeyGroupRouteSelector("memory", nil, "")
	bindings := e1LargePoolBindings(e1LargePoolSize, func(int) int64 { return 1 })
	apiKey := e1LargePoolAPIKey(RouteStrategyModeWeighted, bindings)

	for round := 0; round < e1EqualWrapRounds; round++ {
		ordered, err := selector.OrderAPIKeyGroupBindingsForDispatch(apiKey)
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		wantFirst := bindings[round%e1LargePoolSize].ID
		if ordered[0].ID != wantFirst {
			t.Fatalf("round %d first = %s, want %s (平滑加权等权必须退化为按绑定序精确轮转)", round, ordered[0].ID, wantFirst)
		}
		if round == 0 || round == e1EqualWrapRounds-1 {
			e1AssertPermutation(t, round, ordered, bindings)
		}
	}
}

func TestE1LargePoolWeightedMixedWeightProportion(t *testing.T) {
	selector := NewAPIKeyGroupRouteSelector("memory", nil, "")
	bindings := e1LargePoolBindings(e1LargePoolSize, func(i int) int64 {
		if i%e1HeavyStride == 0 {
			return e1HeavyWeight
		}
		return 1
	})
	apiKey := e1LargePoolAPIKey(RouteStrategyModeWeighted, bindings)

	heavyIDs := make(map[string]struct{}, e1LargePoolSize/e1HeavyStride)
	for i := 0; i < e1LargePoolSize; i += e1HeavyStride {
		heavyIDs[bindings[i].ID] = struct{}{}
	}
	heavyTotal := 0
	lightTotal := 0
	for round := 0; round < e1MixedRounds; round++ {
		ordered, err := selector.OrderAPIKeyGroupBindingsForDispatch(apiKey)
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		if round < e1LargePoolSize/e1HeavyStride {
			// 确定性前缀：第 r 轮首位 = 第 r 个重权绑定 b_{r*heavyStride}。
			wantFirst := bindings[round*e1HeavyStride].ID
			if ordered[0].ID != wantFirst {
				t.Fatalf("round %d first = %s, want %s (重权确定性前缀)", round, ordered[0].ID, wantFirst)
			}
		}
		if round == 0 {
			e1AssertPermutation(t, round, ordered, bindings)
		}
		if _, heavy := heavyIDs[ordered[0].ID]; heavy {
			heavyTotal++
		} else {
			lightTotal++
		}
	}
	// totalWeight = 100*100 + 4900*1 = 14900；重权组期望首位占比
	// 10000/14900 ≈ 0.671，1200 轮期望 ≈ 805 次。平滑加权的轮内规律性强于
	// 二项分布（方差更小），±30% 边界在二项假设下已超过 15 个标准差，
	// 对等权退化、权重失效或重权饥饿等系统性破坏有判别力且不会抖动。
	wantHeavy := e1MixedRounds * int(e1HeavyWeight) * (e1LargePoolSize / e1HeavyStride) / (e1LargePoolSize/e1HeavyStride*int(e1HeavyWeight) + e1LargePoolSize - e1LargePoolSize/e1HeavyStride)
	if wantHeavy != 805 {
		t.Fatalf("期望值推导失效: wantHeavy = %d, want 805", wantHeavy)
	}
	if heavyTotal < wantHeavy*7/10 || heavyTotal > wantHeavy*13/10 {
		t.Fatalf("重权组首位次数 = %d, want [%d, %d] (light=%d)", heavyTotal, wantHeavy*7/10, wantHeavy*13/10, lightTotal)
	}
	// 精确基线：路由状态算法无随机源、比较器为全序，序列逐位确定。
	// 该值录制自优化前实现（整 map 拷贝替换版本），优化后必须逐位复现。
	if heavyTotal != e1MixedBaselineHeavyTotal {
		t.Fatalf("重权组首位次数漂移: got %d, want %d (优化前基线)", heavyTotal, e1MixedBaselineHeavyTotal)
	}
}

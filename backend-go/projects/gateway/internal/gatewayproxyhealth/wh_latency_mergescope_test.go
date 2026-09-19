package gatewayproxyhealth

import (
	"reflect"
	"testing"
)

// 合并路由（merge 模式）速度优先状态键逐账号解析（设计 3.6 / B25-B27）：
// 状态键组分量与存储 latencyState.Scope.GroupID 必须同源（同一解析值）；
// 账号 BoundGroupID 非空时解析为其绑定组，否则回落窗口组（存量键形零回归）。

func mergeScope(groupID string) *LatencyDegradationScope {
	return &LatencyDegradationScope{SystemAccountID: "sys", RouteStrategyID: "rs1", GroupID: groupID}
}

func mergeBoundAccount(id, boundGroupID string) SuppressibleGatewayAccount {
	account := latencyAccount(id)
	account.BoundGroupID = boundGroupID
	return account
}

// 解析函数单元行为：回落保持原 scope 原样（逐字节回归红线）、绑定组胜出、
// 空白绑定组视同缺失、解析值只替换组分量。
func TestResolvedLatencyScopeForAccount(t *testing.T) {
	window := *latencyScope()

	if got := resolvedLatencyScopeForAccount(window, latencyAccount("a")); !reflect.DeepEqual(got, window) {
		t.Fatalf("无绑定组必须原样回落: got %+v, want %+v", got, window)
	}
	if got := resolvedLatencyScopeForAccount(window, mergeBoundAccount("a", "   ")); !reflect.DeepEqual(got, window) {
		t.Fatalf("空白绑定组必须视同缺失回落: got %+v, want %+v", got, window)
	}
	got := resolvedLatencyScopeForAccount(window, mergeBoundAccount("a", " g2 "))
	if got.GroupID != "g2" {
		t.Fatalf("绑定组胜出且去除空白: got GroupID=%q", got.GroupID)
	}
	want := window
	want.GroupID = "g2"
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("解析值只允许替换组分量: got %+v, want %+v", got, want)
	}
}

// 回归红线（存量零回归）：无绑定组账号的键形与现状完全一致；
// merge 账号（BoundGroupID=g2，窗口组 g1）的键与存储 Scope.GroupID 均为 g2，
// 且写路径读回（ListDegradedRuntime 的 ScopeGroupID）同值。
func TestMergeScopeSlowSampleKeyMatchesStoredScope(t *testing.T) {
	ctx := contextBackground()
	clock := newFakeClock(1_000_000)
	service, store := newMemoryLatencyService(clock)
	scope := latencyScope()
	config := speedFirstConfig()

	// 存量账号：键形必须与现状逐字节一致（键构造未变，解析回落原 scope）。
	legacy := latencyAccount("legacy")
	for i := 0; i < 3; i++ {
		if _, err := service.RecordNormalRouteFirstByteSlow(ctx, legacy, scope, &config, ""); err != nil {
			t.Fatalf("legacy slow %d: %v", i, err)
		}
	}
	legacyKey := accountLatencyStateKey(*scope, legacy)
	if legacyKey != "v1:sys:rs1:g1:legacy" {
		t.Fatalf("存量键形漂移: %q", legacyKey)
	}
	raw, err := store.GetJSON(ctx, legacyKey)
	if err != nil || raw == nil {
		t.Fatalf("存量键必须存在: raw=%v err=%v", raw, err)
	}
	legacyState, ok := decodeLatencyState(raw)
	if !ok {
		t.Fatalf("存量状态解码失败: %s", string(raw))
	}
	if !reflect.DeepEqual(legacyState.Scope, *scope) {
		t.Fatalf("存量 Scope 必须与现状一致: got %+v, want %+v", legacyState.Scope, *scope)
	}

	// merge 账号：键与存储 Scope 都落在绑定组 g2；窗口组 g1 旧键不得存在。
	merged := mergeBoundAccount("merged", "g2")
	for i := 0; i < 3; i++ {
		if _, err := service.RecordNormalRouteFirstByteSlow(ctx, merged, scope, &config, ""); err != nil {
			t.Fatalf("merged slow %d: %v", i, err)
		}
	}
	windowKey := accountLatencyStateKey(*scope, merged)
	if windowKey != "v1:sys:rs1:g1:merged" {
		t.Fatalf("窗口组旧键形漂移: %q", windowKey)
	}
	if raw, err = store.GetJSON(ctx, windowKey); err != nil || raw != nil {
		t.Fatalf("窗口组旧键不得写入: raw=%v err=%v", raw, err)
	}
	boundScope := *scope
	boundScope.GroupID = "g2"
	boundKey := accountLatencyStateKey(boundScope, merged)
	if boundKey != "v1:sys:rs1:g2:merged" {
		t.Fatalf("merge 键形漂移: %q", boundKey)
	}
	raw, err = store.GetJSON(ctx, boundKey)
	if err != nil || raw == nil {
		t.Fatalf("merge 键必须存在: raw=%v err=%v", raw, err)
	}
	mergedState, ok := decodeLatencyState(raw)
	if !ok {
		t.Fatalf("merge 状态解码失败: %s", string(raw))
	}
	if !reflect.DeepEqual(mergedState.Scope, boundScope) {
		t.Fatalf("存储 Scope 必须为解析值: got %+v, want %+v", mergedState.Scope, boundScope)
	}

	// 写路径读回：管理面运行态查询返回的 ScopeGroupID 与解析值同源。
	items, err := service.ListNormalRouteLatencyDegradedRuntime(ctx, ListNormalRouteLatencyDegradedRuntimeInput{
		SystemAccountID:  stringPtr("sys"),
		RouteStrategyIDs: []string{"rs1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	scopeByAccount := make(map[string]string, len(items))
	for _, item := range items {
		scopeByAccount[item.AccountID] = item.ScopeGroupID
	}
	if scopeByAccount["legacy"] != "g1" || scopeByAccount["merged"] != "g2" {
		t.Fatalf("运行态 ScopeGroupID 必须与解析值同源: got %v", scopeByAccount)
	}

	// 恢复观察写路径：窗口 scope g1 入口必须命中 g2 键上的既有状态。
	result, err := service.RecordNormalRouteFirstByteSuccess(ctx, merged, scope, &config, int64Ptr(100))
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.RecoverySuccessCount != 1 {
		t.Fatalf("恢复观察必须命中解析键上的状态: result=%v err=%v", result, err)
	}
}

// 排序逐账号解析：g1 降级账号后置；g2 账号只受 g2 作用域状态影响——
// 其在 g2 的降级状态生效（后置），窗口组 g1 的旧式状态对它不可见。
func TestMergeScopeOrderAsyncIndependentGroupScopes(t *testing.T) {
	ctx := contextBackground()
	clock := newFakeClock(1_000_000)
	service, store := newMemoryLatencyService(clock)
	windowScope := latencyScope()
	g2Scope := mergeScope("g2")
	config := speedFirstConfig()

	degradedInG1 := latencyAccount("a-g1")             // g1 账号，g1 降级
	degradedInG2 := mergeBoundAccount("b-g2", "g2")    // g2 账号，g2 降级
	normal := latencyAccount("c-normal")               // g1 账号，无状态
	onlyWindowState := mergeBoundAccount("e-g2", "g2") // g2 账号，仅存在 g1 旧式状态

	for i := 0; i < 3; i++ {
		if _, err := service.RecordNormalRouteFirstByteSlow(ctx, degradedInG1, windowScope, &config, ""); err != nil {
			t.Fatalf("g1 slow %d: %v", i, err)
		}
		if _, err := service.RecordNormalRouteFirstByteSlow(ctx, degradedInG2, g2Scope, &config, ""); err != nil {
			t.Fatalf("g2 slow %d: %v", i, err)
		}
	}
	// 旧式窗口组键上的降级状态（历史残留或他组账号写入）。
	staleWindowKey := accountLatencyStateKey(*windowScope, onlyWindowState)
	if err := store.SetJSON(ctx, staleWindowKey, latencyState{
		Generation: `[0,"initial"]`, AccountID: onlyWindowState.ID, RuntimeKey: onlyWindowState.ID,
		Scope: *windowScope, Config: config,
		FirstSlowAtMs: clock.NowMs() - 1_000, LastSlowAtMs: clock.NowMs(), SlowCount: 3,
		DegradedUntilMs: int64Ptr(clock.NowMs() + 100_000), NextProbeAtMs: int64Ptr(clock.NowMs()),
		RecoveryProbeRoundAttemptCount: int64Ptr(0), RecoveryProbeRoundSuccessCount: int64Ptr(0),
		Reason: "旧式键残留",
	}, 120_000); err != nil {
		t.Fatal(err)
	}

	accounts := []SuppressibleGatewayAccount{degradedInG1, degradedInG2, normal, onlyWindowState}
	result, err := OrderGatewayAccountsByNormalRouteLatencyDegradation(
		ctx, service, accounts, func(a SuppressibleGatewayAccount) SuppressibleGatewayAccount { return a },
		windowScope, &config, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	gotIDs := make([]string, 0, len(result.Accounts))
	for _, account := range result.Accounts {
		gotIDs = append(gotIDs, account.ID)
	}
	wantIDs := []string{"c-normal", "e-g2", "a-g1", "b-g2"}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("排序 = %v, want %v（各组状态只对解析作用域生效）", gotIDs, wantIDs)
	}
	if wantDegraded := []string{"a-g1", "b-g2"}; !reflect.DeepEqual(result.DegradedAccountIDs, wantDegraded) {
		t.Fatalf("DegradedAccountIDs = %v, want %v", result.DegradedAccountIDs, wantDegraded)
	}
}

// 清理组无关（B27）：按账号组记键的降级状态必须被 ForAccount 清理覆盖。
func TestMergeScopeClearForAccountIgnoresGroup(t *testing.T) {
	ctx := contextBackground()
	clock := newFakeClock(1_000_000)
	service, _ := newMemoryLatencyService(clock)
	scope := latencyScope()
	config := speedFirstConfig()
	merged := mergeBoundAccount("merged", "g2")

	for i := 0; i < 3; i++ {
		if _, err := service.RecordNormalRouteFirstByteSlow(ctx, merged, scope, &config, ""); err != nil {
			t.Fatalf("slow %d: %v", i, err)
		}
	}
	cleared, err := service.ClearNormalRouteLatencyDegradationForAccount(ctx, ClearNormalRouteLatencyDegradationForAccountInput{
		SystemAccountID: "sys", AccountID: "merged",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cleared != 1 {
		t.Fatalf("ForAccount 清理必须覆盖账号组记键状态: cleared=%d", cleared)
	}
	items, err := service.ListNormalRouteLatencyDegradedRuntime(ctx, ListNormalRouteLatencyDegradedRuntimeInput{
		RouteStrategyIDs: []string{"rs1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("清理后不得残留运行态: %+v", items)
	}
}

// 探针成功回写同源闭环（B26 的 gateway 侧）：candidate 从存储 state 重建
// （Scope.GroupID=账号组），RecordNormalRouteRecoveryProbeSuccess 以同一账号
// 形态校验命中并在同一键上推进/清除，两轮探针成功后状态整体清除。
func TestMergeScopeRecoveryProbeSuccessOnResolvedKey(t *testing.T) {
	ctx := contextBackground()
	clock := newFakeClock(1_000_000)
	service, store := newMemoryLatencyService(clock)
	scope := latencyScope()
	config := speedFirstConfig()
	merged := mergeBoundAccount("merged", "g2")

	for i := 0; i < 3; i++ {
		if _, err := service.RecordNormalRouteFirstByteSlow(ctx, merged, scope, &config, ""); err != nil {
			t.Fatalf("slow %d: %v", i, err)
		}
	}
	// 探针到期（NextProbeAt≈now+5001）之后、降级截止（now+120s）之前。
	future := clock.NowMs() + 6_000
	candidates, err := service.ListNormalRouteLatencyProbeCandidates(ctx, nil, int64Ptr(future))
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("探针候选数 = %d, want 1", len(candidates))
	}
	candidate := candidates[0]
	if candidate.Scope.GroupID != "g2" || candidate.StateKey != "v1:sys:rs1:g2:merged" {
		t.Fatalf("探针候选必须按存储 Scope 重建: StateKey=%q Scope=%+v", candidate.StateKey, candidate.Scope)
	}

	for round := 1; round <= 2; round++ {
		result, err := service.RecordNormalRouteRecoveryProbeSuccess(ctx, merged, candidate, int64Ptr(100))
		if err != nil {
			t.Fatal(err)
		}
		if result == nil {
			t.Fatalf("探针第 %d 轮成功必须命中同一存储状态", round)
		}
		if round == 1 && result.Cleared {
			t.Fatalf("两轮探针未满不得清除: %+v", result)
		}
		if round == 2 && !result.Cleared {
			t.Fatalf("第二轮探针成功必须清除: %+v", result)
		}
		if round < 2 {
			next, err := service.ListNormalRouteLatencyProbeCandidates(ctx, nil, int64Ptr(future))
			if err != nil {
				t.Fatal(err)
			}
			if len(next) != 1 {
				t.Fatalf("第 %d 轮后探针候选数 = %d, want 1", round, len(next))
			}
			candidate = next[0]
		}
	}
	raw, err := store.GetJSON(ctx, "v1:sys:rs1:g2:merged")
	if err != nil || raw != nil {
		t.Fatalf("恢复后解析键必须删除: raw=%v err=%v", raw, err)
	}
}

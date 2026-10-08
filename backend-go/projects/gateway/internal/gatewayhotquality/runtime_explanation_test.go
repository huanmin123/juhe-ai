package gatewayhotquality

// 期二（缓存率感知调度设计 5.6）：OrderGatewayAccountsByHotQuality 把首协
// 议组的排序决策解释随结果带出——消费方直接透传即可拿到决策事实（Candidate
// OrderDetails / SpeedBaseEwmaMs / SpeedThresholdMs / CacheRateStale / Cache
// RateEnabled），无需为解释重跑一轮排序。本测试锁定：返回解释与用同一候选
// 视图直接调 DecideHotQualityCandidate 的产出一致（探索块除外——它反映真实
// 运行态状态机，属探索域既有测试的断言对象）。

import (
	"context"
	"reflect"
	"testing"
)

func TestOrderGatewayAccountsByHotQualityReturnsFirstGroupDecisionExplanation(t *testing.T) {
	t.Cleanup(ResetGatewayHotQualityRuntimeForTest)
	ResetGatewayHotQualityRuntimeForTest()
	ctx := context.Background()
	runtime, err := GetGatewayHotQualityRuntime(ctx, RuntimeDriverConfig{RuntimeMode: "standalone", RuntimeStateDriver: "memory"})
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}

	const (
		systemAccountID = "sys"
		groupID         = "grp"
		requestLane     = "text"
		protocolProfile = "openai:2024"
	)
	nowMs := int64(1_800_000)
	cacheRates := map[string]CacheRateWindow{
		// a1 带有效缓存样本（rate 0.5 → 档位 5），输入序让 a2 在前——
		// 质量排序应把 a1 反超到首位，并体现 QualityReorderedTierKeys。
		"a1": {CacheReadTokens: 50_000, InputTokens: 100_000},
	}
	accounts := []GatewayHotQualityAccountView{testAccount("a2", 1), testAccount("a1", 1)}
	result, err := OrderGatewayAccountsByHotQuality(ctx, runtime, GatewayHotQualityCandidateOrderInput[GatewayHotQualityAccountView]{
		Accounts:                     accounts,
		Base:                         baseView,
		Mode:                         HotQualityModeCostFirst,
		SystemAccountID:              systemAccountID,
		GroupID:                      groupID,
		RequestLane:                  requestLane,
		RequestID:                    "req-explanation",
		NowMs:                        &nowMs,
		CacheRates:                   cacheRates,
		CacheRateStale:               true,
		EligibleFirstPrimaryDispatch: false,
	})
	if err != nil {
		t.Fatalf("OrderGatewayAccountsByHotQuality: %v", err)
	}
	if result.Explanation == nil {
		t.Fatal("非空候选结果必须携带首协议组排序决策解释")
	}

	// 用同一候选视图直接调 DecideHotQualityCandidate（exploration=nil），
	// 断言两路解释一致。
	routeScopeKey, err := GatewayHotQualityRouteScopeKey(gatewayHotQualityRouteScopeKeyInput{
		SystemAccountID: systemAccountID,
		GroupID:         groupID,
		ProtocolProfile: protocolProfile,
		RequestLane:     requestLane,
	})
	if err != nil {
		t.Fatalf("route scope key: %v", err)
	}
	candidates := make([]HotQualityCandidate, 0, len(accounts))
	for index, view := range accounts {
		scope, err := hotQualityScopeForAccount(view, requestLane, nil)
		if err != nil {
			t.Fatalf("scope: %v", err)
		}
		snapshot, err := runtime.HotQualityStore.Get(ctx, scope, &nowMs)
		if err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		runtimeKey, err := GatewayAccountRuntimeKey(view)
		if err != nil {
			t.Fatalf("runtime key: %v", err)
		}
		candidates = append(candidates, HotQualityCandidate{
			AccountID:         view.ID,
			AccountRuntimeKey: runtimeKey,
			RouteScopeKey:     routeScopeKey,
			ConfigurationTier: GatewayAccountConfigurationTier{
				ModelMatchRank: modelRank(view, nil),
				Priority:       view.Priority,
			},
			StableBindingOrder: index,
			HotQuality:         selectionViewOrNil(snapshot),
		})
	}
	direct, err := DecideHotQualityCandidate(DecideHotQualityCandidateInput[HotQualityCandidate]{
		Mode:           HotQualityModeCostFirst,
		RouteScopeKey:  routeScopeKey,
		Candidates:     candidates,
		Base:           func(candidate HotQualityCandidate) HotQualityCandidate { return candidate },
		CacheRates:     cacheRates,
		CacheRateStale: true,
	})
	if err != nil {
		t.Fatalf("DecideHotQualityCandidate: %v", err)
	}

	// 探索块两路本就不同构（runtime 走 Accrue 状态机、direct 无状态），
	// 不属本次断言目标；其余字段（含缓存率设计 5.6 全部字段）必须一致。
	runtimeExplanation := *result.Explanation
	directExplanation := direct.Explanation
	runtimeExplanation.Exploration = SameTierExplorationExplanation{}
	directExplanation.Exploration = SameTierExplorationExplanation{}
	if !reflect.DeepEqual(runtimeExplanation, directExplanation) {
		t.Fatalf("explanation mismatch:\nruntime = %#v\ndirect  = %#v", runtimeExplanation, directExplanation)
	}

	// 语义断言：缓存样本反超 + 档位/哨兵 + stale 透传 + 重排层键。
	if got := accountIDListOf(result.Accounts); !reflect.DeepEqual(got, []string{"a1", "a2"}) {
		t.Fatalf("ordered accounts = %v, want a1（缓存样本）在前", got)
	}
	explanation := result.Explanation
	if explanation.CacheRateStale != true || explanation.CacheRateEnabled != true {
		t.Fatalf("cacheRate stale/enabled = %v/%v", explanation.CacheRateStale, explanation.CacheRateEnabled)
	}
	if explanation.SpeedThresholdMs != SpeedDominanceThresholdCostFirstMs || explanation.SpeedBaseEwmaMs != nil {
		t.Fatalf("speed block = %d/%#v", explanation.SpeedThresholdMs, explanation.SpeedBaseEwmaMs)
	}
	if len(explanation.QualityReorderedTierKeys) == 0 {
		t.Fatalf("qualityReorderedTierKeys = %#v, want 非空（a1 反超）", explanation.QualityReorderedTierKeys)
	}
	if len(explanation.CandidateOrderDetails) != 2 {
		t.Fatalf("candidateOrderDetails = %#v", explanation.CandidateOrderDetails)
	}
	first, second := explanation.CandidateOrderDetails[0], explanation.CandidateOrderDetails[1]
	if first.AccountID != "a1" || first.CacheQuantum != 5 || first.CacheHitRate == nil || *first.CacheHitRate != 0.5 {
		t.Fatalf("details[0] = %#v", first)
	}
	if second.AccountID != "a2" || second.CacheQuantum != -1 || second.CacheHitRate != nil {
		t.Fatalf("details[1] = %#v", second)
	}
}

func accountIDListOf(views []GatewayHotQualityAccountView) []string {
	ids := make([]string, 0, len(views))
	for _, view := range views {
		ids = append(ids, view.ID)
	}
	return ids
}

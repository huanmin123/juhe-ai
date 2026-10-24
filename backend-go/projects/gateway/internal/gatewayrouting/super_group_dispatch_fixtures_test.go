// 超级分组设计 §7 的 dispatch-* 候选层 fixture：成员展开后的 route_rank、
// 同账号去重、启用成员跳过与全部成员不可用的外层回退。上游尝试与配额分类
// （attempt/member_group_id/capacity_exhausted 等）由 chain 层 fixture 覆盖。
package gatewayrouting

import (
	"context"
	"testing"
)

// superDispatchFixtureIDs 是本文件覆盖的 fixture 集合。
var superDispatchFixtureIDs = []string{
	"dispatch-direct",
	"dispatch-mapping",
	"dispatch-member-skip",
	"dispatch-all-members-unavailable",
	"dispatch-duplicate-account",
	"no-merged-route-source",
}

// collectSuperCandidates 直接驱动候选层，暴露 ModelFilter.ModelPriority
// .RankByAccountID，用于断言 direct(0)/同族改名(0)/跨协议映射(1) 排序。
func collectSuperCandidates(t *testing.T, cache *fakeRuntimeCache, bindings ...GroupBindingRow) []ModelTargetGroupCandidate {
	t.Helper()
	return collectSuperCandidatesWithFamily(t, cache, "", bindings...)
}

// collectSuperCandidatesWithFamily 在 collectSuperCandidates 之上允许显式指定
// 请求 endpoint family（跨族映射用例需要 source family 与映射行源族一致）。
func collectSuperCandidatesWithFamily(t *testing.T, cache *fakeRuntimeCache, endpointFamily string, bindings ...GroupBindingRow) []ModelTargetGroupCandidate {
	t.Helper()
	selector := &TargetGroupSelector{RuntimeCache: cache, CapabilityFilter: PassthroughCapabilityFilter{}}
	input := ModelTargetGroupInput{
		Request:      routeTestRequestWithFamily("gpt-4o", endpointFamily),
		APIKeyRecord: routeTestAPIKey(RouteStrategyModeNormal, bindings...),
		Bindings:     bindings,
		TargetModel:  "gpt-4o",
	}
	candidates, err := selector.CollectGatewayModelGroupSegments(context.Background(), input)
	if err != nil {
		t.Fatalf("CollectGatewayModelGroupSegments: %v", err)
	}
	return candidates
}

// routeTestRequestWithFamily 构造带 EndpointFamilyOverride 的请求视图；空
// family 退回 /v1/chat/completions 默认解析。
func routeTestRequestWithFamily(model, endpointFamily string) RequestView {
	request := routeTestRequest(model)
	if endpointFamily != "" {
		request.EndpointFamilyOverride = endpointFamily
	}
	return request
}

// mappingAccount 是同族改名映射账户（chat_completions→chat_completions 别名，
// 方案B 后与直连同权 rank 0）。
func mappingAccount(id, upstream string) UpstreamAccount {
	return UpstreamAccount{
		ID: id, ProviderCode: "anthropic",
		ProtocolCode: "openai", ProtocolVersion: "v1",
		ModelMappings: []gatewayAccountModelMapping{{
			SourceModel: "gpt-4o", SourceEndpointFamily: EndpointFamilyChatCompletions,
			UpstreamModel: upstream, UpstreamEndpointFamily: EndpointFamilyChatCompletions,
		}},
		SupportedModels: []string{upstream},
	}
}

// crossFamilyMappingAccount 是跨协议转换映射账户（responses→chat_completions，
// 方案B 后保留 rank 1 直连优先）。
func crossFamilyMappingAccount(id, upstream string) UpstreamAccount {
	return UpstreamAccount{
		ID: id, ProviderCode: "anthropic",
		ProtocolCode: "openai", ProtocolVersion: "v1",
		ModelMappings: []gatewayAccountModelMapping{{
			SourceModel: "gpt-4o", SourceEndpointFamily: EndpointFamilyResponses,
			UpstreamModel: upstream, UpstreamEndpointFamily: EndpointFamilyChatCompletions,
		}},
		SupportedModels: []string{upstream},
	}
}

func directAccount(id string) UpstreamAccount {
	return UpstreamAccount{ID: id, ProviderCode: "openai", SupportedModels: []string{"gpt-4o"}}
}

func superDispatchCache(t *testing.T) *fakeRuntimeCache {
	t.Helper()
	cache := newFakeRuntimeCache()
	cache.groupAccess["grp_super"] = routeTestAccess()
	cache.providerRoute = func(input ProviderModelRouteInput) (ProviderModelRouteResolution, error) {
		return ProviderModelRouteResolution{Outcome: ProviderModelRouteMissing, ModelKey: input.Model}, nil
	}
	return cache
}

func TestSuperGroupDispatchFixtures(t *testing.T) {
	if len(superDispatchFixtureIDs) != 6 {
		t.Fatalf("dispatch fixture count = %d, want 6", len(superDispatchFixtureIDs))
	}

	t.Run("dispatch-direct", func(t *testing.T) {
		cache := superDispatchCache(t)
		cache.groupAccess["grp_m1"] = routeTestAccess()
		cache.accounts["grp_m1"] = []UpstreamAccount{directAccount("acc_direct")}
		cache.groupAccess["grp_m2"] = routeTestAccess()
		cache.accounts["grp_m2"] = []UpstreamAccount{mappingAccount("acc_mapped", "up_m")}

		candidates := collectSuperCandidates(t, cache,
			superGroupBinding("b1", "grp_super", []string{"grp_m1", "grp_m2"}, 1))
		if len(candidates) != 1 {
			t.Fatalf("candidates = %d, want 1", len(candidates))
		}
		candidate := candidates[0]
		// 方案B：同族改名映射与直连同秩（均 rank 0）；segment 内先后由账户
		// 比较器后续键（此处 member 顺序）决定，acc_direct 的成员在前。
		if got := accountIDs(candidate.Accounts); !equalStrings(got, []string{"acc_direct", "acc_mapped"}) {
			t.Fatalf("accounts = %v, want same-rank order by member order", got)
		}
		ranks := candidate.ModelFilter.ModelPriority.RankByAccountID
		if ranks["acc_direct"] != 0 || ranks["acc_mapped"] != 0 {
			t.Fatalf("route ranks = %v, want direct=0 same-family-rename=0", ranks)
		}
		if got := segmentSummary(candidate.GroupSegments); !equalStrings(got, []string{"grp_m1:acc_direct", "grp_m2:acc_mapped"}) {
			t.Fatalf("segments = %v", got)
		}
	})

	t.Run("dispatch-mapping", func(t *testing.T) {
		cache := superDispatchCache(t)
		cache.groupAccess["grp_map"] = routeTestAccess()
		cache.accounts["grp_map"] = []UpstreamAccount{crossFamilyMappingAccount("acc_map_only", "up_m")}

		// 跨协议转换映射（responses→chat_completions）的源族必须与请求族一致
		// 才能命中；请求视图覆盖为 responses family。
		candidates := collectSuperCandidatesWithFamily(t, cache, EndpointFamilyResponses,
			superGroupBinding("b1", "grp_super", []string{"grp_map"}, 1))
		if len(candidates) != 1 {
			t.Fatalf("candidates = %d, want 1", len(candidates))
		}
		candidate := candidates[0]
		if got := accountIDs(candidate.Accounts); !equalStrings(got, []string{"acc_map_only"}) {
			t.Fatalf("accounts = %v", got)
		}
		if got := candidate.ModelFilter.ModelPriority.RankByAccountID["acc_map_only"]; got != 1 {
			t.Fatalf("cross-protocol mapping route rank = %d, want 1", got)
		}
		if candidate.ModelFilter.DirectMatchedCount != 0 || candidate.ModelFilter.MappingMatchedCount != 1 {
			t.Fatalf("filter counts = direct:%d mapping:%d",
				candidate.ModelFilter.DirectMatchedCount, candidate.ModelFilter.MappingMatchedCount)
		}
	})

	t.Run("dispatch-duplicate-account", func(t *testing.T) {
		cache := superDispatchCache(t)
		// 方案B：同族改名映射与直连同秩；dedup 按 (route_rank, member_priority,
		// member_group_id) 保留优先成员——两处 rank 相同，成员顺序在前的
		// grp_m1（映射出现）胜出，胜者 rank 0。
		cache.groupAccess["grp_m1"] = routeTestAccess()
		cache.accounts["grp_m1"] = []UpstreamAccount{mappingAccount("acc_shared", "up_m")}
		cache.groupAccess["grp_m2"] = routeTestAccess()
		cache.accounts["grp_m2"] = []UpstreamAccount{directAccount("acc_shared")}

		candidates := collectSuperCandidates(t, cache,
			superGroupBinding("b1", "grp_super", []string{"grp_m1", "grp_m2"}, 1))
		if len(candidates) != 1 {
			t.Fatalf("candidates = %d, want 1", len(candidates))
		}
		candidate := candidates[0]
		if got := accountIDs(candidate.Accounts); !equalStrings(got, []string{"acc_shared"}) {
			t.Fatalf("duplicate account must appear once: %v", got)
		}
		if got := segmentSummary(candidate.GroupSegments); !equalStrings(got, []string{"grp_m1:acc_shared"}) {
			t.Fatalf("dedup must keep the first member occurrence at equal rank: %v", got)
		}
		if got := candidate.ModelFilter.ModelPriority.RankByAccountID["acc_shared"]; got != 0 {
			t.Fatalf("dedup rank = %d, want 0 (same-rank rename/direct)", got)
		}
	})

	t.Run("dispatch-member-skip", func(t *testing.T) {
		// enabled=false 成员行在 runtimecache 装载阶段已被剔除；到达候选层
		// 的 MemberGroupIDs 只含 B/C，A 不得进入展开。
		cache := superDispatchCache(t)
		cache.groupAccess["grp_b"] = routeTestAccess()
		cache.accounts["grp_b"] = []UpstreamAccount{directAccount("acc_b")}
		cache.groupAccess["grp_c"] = routeTestAccess()
		cache.accounts["grp_c"] = []UpstreamAccount{directAccount("acc_c")}

		candidates := collectSuperCandidates(t, cache,
			superGroupBinding("b1", "grp_super", []string{"grp_b", "grp_c"}, 1))
		if len(candidates) != 1 {
			t.Fatalf("candidates = %d, want 1", len(candidates))
		}
		if got := segmentSummary(candidates[0].GroupSegments); !equalStrings(got, []string{"grp_b:acc_b", "grp_c:acc_c"}) {
			t.Fatalf("disabled member leaked into expansion: %v", got)
		}
	})

	t.Run("no-merged-route-source", func(t *testing.T) {
		cache := superDispatchCache(t)
		cache.groupAccess["grp_m1"] = routeTestAccess()
		cache.accounts["grp_m1"] = []UpstreamAccount{directAccount("acc_m1")}
		apiKey := routeTestAPIKey(RouteStrategyModeNormal,
			superGroupBinding("b1", "grp_super", []string{"grp_m1"}, 1))
		result := runRouteTest(t, cache, nil, apiKey, routeTestRequest("gpt-4o"))
		if result.Outcome != NormalRouteOutcomeSelected {
			t.Fatalf("outcome = %q", result.Outcome)
		}
		if result.RouteSource != RouteSourceCatalogProvider && result.RouteSource != RouteSourceAccountMapping {
			t.Fatalf("route source = %q, want direct/mapping", result.RouteSource)
		}
		if string(result.RouteSource) == "merged" {
			t.Fatalf("super route must not emit merged route source")
		}
	})

	t.Run("dispatch-all-members-unavailable", func(t *testing.T) {
		cache := superDispatchCache(t)
		cache.groupAccess["grp_m1"] = routeTestAccess()
		cache.accounts["grp_m1"] = []UpstreamAccount{
			{ID: "acc_none", ProviderCode: "openai", SupportedModels: []string{"other-model"}},
		}
		cache.groupAccess["grp_g2"] = routeTestAccess()
		cache.accounts["grp_g2"] = []UpstreamAccount{directAccount("acc_g2")}

		apiKey := routeTestAPIKey(RouteStrategyModeFailover,
			superGroupBinding("b1", "grp_super", []string{"grp_m1"}, 1),
			routeTestBinding("b2", "grp_g2", 2),
		)
		result := runRouteTest(t, cache, nil, apiKey, routeTestRequest("gpt-4o"))
		if result.Outcome != NormalRouteOutcomeSelected || result.GroupID != "grp_g2" {
			t.Fatalf("outer fallback = %q/%q, want selected/grp_g2", result.Outcome, result.GroupID)
		}
		if len(result.GroupSegments) != 0 {
			t.Fatalf("fallback target must not carry super segments: %+v", result.GroupSegments)
		}
		if got := accountIDs(result.Accounts); !equalStrings(got, []string{"acc_g2"}) {
			t.Fatalf("accounts = %v", got)
		}
	})
}

// TestSuperGroupRouteEmptyTargetIsUnavailable 覆盖 0 成员超级分组在运行期
// 的 no_candidate 语义：单独作为目标时返回 503 model_target_group_unavailable；
// 外层 failover 中记录 skippedSuperGroupIDs 后继续到下一个分组目标。
func TestSuperGroupRouteEmptyTargetIsUnavailable(t *testing.T) {
	matchedDirectory := func(input ProviderModelRouteInput) (ProviderModelRouteResolution, error) {
		return ProviderModelRouteResolution{Outcome: ProviderModelRouteMatched, ModelKey: input.Model}, nil
	}

	cache := superDispatchCache(t)
	cache.providerRoute = matchedDirectory
	apiKey := routeTestAPIKey(RouteStrategyModeNormal, superGroupBinding("b1", "grp_super", nil, 1))
	result := runRouteTest(t, cache, nil, apiKey, routeTestRequest("gpt-4o"))
	if result.Outcome != NormalRouteOutcomeFailed || result.StatusCode != 503 || result.Code != FailCodeModelTargetGroupUnavailable {
		t.Fatalf("empty super target = %+v, want 503 %s", result, FailCodeModelTargetGroupUnavailable)
	}

	fallbackCache := superDispatchCache(t)
	fallbackCache.providerRoute = matchedDirectory
	fallbackCache.groupAccess["grp_fallback"] = routeTestAccess()
	fallbackCache.accounts["grp_fallback"] = []UpstreamAccount{directAccount("acc_fallback")}
	fallbackKey := routeTestAPIKey(RouteStrategyModeFailover,
		superGroupBinding("b1", "grp_super", nil, 1),
		routeTestBinding("b2", "grp_fallback", 2),
	)
	fallback := runRouteTest(t, fallbackCache, nil, fallbackKey, routeTestRequest("gpt-4o"))
	if fallback.Outcome != NormalRouteOutcomeSelected || fallback.GroupID != "grp_fallback" {
		t.Fatalf("empty super fallback = %q/%q, want selected/grp_fallback", fallback.Outcome, fallback.GroupID)
	}
	if len(fallback.SkippedSuperGroupIDs) != 1 || fallback.SkippedSuperGroupIDs[0] != "grp_super" {
		t.Fatalf("skipped super IDs = %v, want [grp_super]", fallback.SkippedSuperGroupIDs)
	}
	if len(fallback.GroupSegments) != 0 {
		t.Fatalf("fallback target must not carry super segments: %+v", fallback.GroupSegments)
	}
}

package gatewayrouting

import (
	"testing"
)

// superGroupBinding 构造一个超级分组目标绑定。
func superGroupBinding(id, superGroupID string, memberGroupIDs []string, priority int64) GroupBindingRow {
	binding := routeTestBinding(id, superGroupID, priority)
	binding.GroupKind = GroupKindSuper
	binding.MemberGroupIDs = memberGroupIDs
	return binding
}

// 超级分组目标（设计 §4.2）：逐 enabled 成员展开候选，同一账号跨成员按
// (route_rank, member_priority, member_group_id) 保留最小出现——后成员的
// direct 命中必须胜过前成员的 mapping 命中；route_group 保持超级分组本身。
func TestSuperGroupRouteExpandsMembersAndDeduplicates(t *testing.T) {
	cache := newFakeRuntimeCache()
	cache.groupAccess["grp_super"] = routeTestAccess()
	cache.groupAccess["grp_a"] = routeTestAccess()
	cache.accounts["grp_a"] = []UpstreamAccount{
		{
			ID: "acc_shared", ProviderCode: "anthropic",
			ProtocolCode: "openai", ProtocolVersion: "v1",
			ModelMappings: []gatewayAccountModelMapping{{
				SourceModel: "gpt-4o", SourceEndpointFamily: EndpointFamilyChatCompletions,
				UpstreamModel: "up_m", UpstreamEndpointFamily: EndpointFamilyChatCompletions,
			}},
			SupportedModels: []string{"up_m"},
		},
	}
	cache.groupAccess["grp_b"] = routeTestAccess()
	cache.accounts["grp_b"] = []UpstreamAccount{
		{ID: "acc_shared", ProviderCode: "openai", SupportedModels: []string{"gpt-4o"}},
		{ID: "acc_direct_only", ProviderCode: "openai", SupportedModels: []string{"gpt-4o"}},
	}
	cache.providerRoute = func(input ProviderModelRouteInput) (ProviderModelRouteResolution, error) {
		return ProviderModelRouteResolution{Outcome: ProviderModelRouteMissing, ModelKey: input.Model}, nil
	}
	apiKey := routeTestAPIKey(RouteStrategyModeNormal,
		superGroupBinding("b1", "grp_super", []string{"grp_a", "grp_b"}, 1),
	)

	result := runRouteTest(t, cache, nil, apiKey, routeTestRequest("gpt-4o"))

	if result.Outcome != NormalRouteOutcomeSelected {
		t.Fatalf("outcome = %q, want selected: %+v", result.Outcome, result)
	}
	if result.GroupID != "grp_super" {
		t.Fatalf("route group = %q, want grp_super", result.GroupID)
	}
	if result.APIKeyRecord == nil || result.APIKeyRecord.SelectedGroupID != "grp_super" {
		t.Fatalf("selected group id must stay the super group: %+v", result.APIKeyRecord)
	}
	if result.RouteSource != RouteSourceCatalogProvider {
		t.Fatalf("route source = %q, want catalog_provider (direct wins)", result.RouteSource)
	}
	if got := accountIDs(result.Accounts); !equalStrings(got, []string{"acc_direct_only", "acc_shared"}) {
		t.Fatalf("accounts = %v, want [acc_direct_only acc_shared] (dedup keeps direct)", got)
	}
	if got := segmentSummary(result.GroupSegments); !equalStrings(got, []string{"grp_b:acc_direct_only,acc_shared"}) {
		t.Fatalf("segments = %v, want [grp_b:acc_direct_only,acc_shared]", got)
	}
	for _, call := range cache.callsAccounts {
		if call.groupID == "grp_super" {
			t.Fatal("超级分组自身不应被当作账号组读取")
		}
	}
}

// 超级分组全部成员无候选时该目标不可用，外层模式（failover）继续下一目标。
func TestSuperGroupRouteEmptyTargetFallsThroughOuterBindings(t *testing.T) {
	cache := newFakeRuntimeCache()
	cache.groupAccess["grp_super"] = routeTestAccess()
	cache.groupAccess["grp_m1"] = routeTestAccess()
	cache.accounts["grp_m1"] = []UpstreamAccount{
		{ID: "acc_none", ProviderCode: "openai", SupportedModels: []string{"other-model"}},
	}
	cache.groupAccess["grp_g2"] = routeTestAccess()
	cache.accounts["grp_g2"] = []UpstreamAccount{
		{ID: "acc_g2", ProviderCode: "openai", SupportedModels: []string{"gpt-4o"}},
	}
	cache.providerRoute = func(input ProviderModelRouteInput) (ProviderModelRouteResolution, error) {
		return ProviderModelRouteResolution{Outcome: ProviderModelRouteMissing, ModelKey: input.Model}, nil
	}
	apiKey := routeTestAPIKey(RouteStrategyModeFailover,
		superGroupBinding("b1", "grp_super", []string{"grp_m1"}, 1),
		routeTestBinding("b2", "grp_g2", 2),
	)

	result := runRouteTest(t, cache, nil, apiKey, routeTestRequest("gpt-4o"))

	if result.Outcome != NormalRouteOutcomeSelected || result.GroupID != "grp_g2" {
		t.Fatalf("outer target = %q/%q, want selected/grp_g2: %+v", result.Outcome, result.GroupID, result)
	}
	if len(result.GroupSegments) != 0 {
		t.Fatalf("standard target must not carry segments: %+v", result.GroupSegments)
	}
}

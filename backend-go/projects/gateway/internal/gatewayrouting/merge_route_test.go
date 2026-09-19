package gatewayrouting

import (
	"context"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Merge-mode fixtures (docs/functions/合并路由设计.md 3.1/3.2, B9-B13)
// ---------------------------------------------------------------------------

func mergeTestBinding(id, groupID, providerCode string, priority int64) GroupBindingRow {
	return GroupBindingRow{
		ID: id, APIKeyID: "key1", SystemAccountID: "owner1",
		GroupID: groupID, Priority: priority, Status: RowStatusActive,
		ProviderCode: providerCode, GroupEnabled: 1,
	}
}

func mergeTestAPIKey(mode string, bindings ...GroupBindingRow) *APIKeyRow {
	return &APIKeyRow{
		ID: "key1", SystemAccountID: "owner1",
		RouteStrategyMode: mode,
		Status:            RowStatusActive,
		GroupBindings:     bindings,
	}
}

func mergeTestAccess(providerCode string) GroupUsageAccessMetadata {
	return GroupUsageAccessMetadata{ProviderCode: providerCode, GroupAccessType: GroupAccessTypeOwner}
}

func mergeTestRequest(model string) RequestView {
	return RequestView{Method: "POST", OriginalURL: "/v1/chat/completions", BodyModel: model}
}

// runMergeRouteTest resolves the route with the shared fake runtime cache and
// the pass-through capability probe unless a scripted one is given.
func runMergeRouteTest(t *testing.T, cache *fakeRuntimeCache, capability AccountCapabilityFilter, apiKey *APIKeyRow, request RequestView) NormalGatewayModelRouteResult {
	t.Helper()
	if capability == nil {
		capability = PassthroughCapabilityFilter{}
	}
	service := NewNormalModelRouteService(cache, capability)
	result, err := service.ResolveNormalGatewayModelRoute(context.Background(), ResolveNormalGatewayModelRouteInput{
		Request:      request,
		APIKeyRecord: apiKey,
	})
	if err != nil {
		t.Fatalf("ResolveNormalGatewayModelRoute returned error: %v", err)
	}
	return result
}

// segmentSummary renders segments as "group:acc1,acc2" for order assertions.
func segmentSummary(segments []NormalRouteGroupSegment) []string {
	parts := make([]string, 0, len(segments))
	for _, segment := range segments {
		parts = append(parts, segment.GroupID+":"+strings.Join(accountIDs(segment.Accounts), ","))
	}
	return parts
}

// Two provider-distinct groups each holding servable accounts merge into two
// segments; the flat pool follows binding order then intra-group order; the
// window group is the first non-empty segment's group; RouteSource is merged;
// the catalog route being missing must not gate the selection (B13). A
// GroupEnabled=0 binding is dropped defensively and never consulted.
func TestMergeRouteCollectsAllGroupSegments(t *testing.T) {
	cache := newFakeRuntimeCache()
	cache.groupAccess["grp_a"] = mergeTestAccess("openai")
	cache.accounts["grp_a"] = []UpstreamAccount{
		{ID: "acc_a1", ProviderCode: "openai", SupportedModels: []string{"gpt-4o"}},
		{ID: "acc_a2", ProviderCode: "openai", SupportedModels: []string{"gpt-4o"}},
	}
	cache.groupAccess["grp_b"] = mergeTestAccess("anthropic")
	cache.accounts["grp_b"] = []UpstreamAccount{
		{ID: "acc_b1", ProviderCode: "anthropic", SupportedModels: []string{"gpt-4o"}},
	}
	cache.groupAccess["grp_c"] = mergeTestAccess("openai")
	cache.accounts["grp_c"] = []UpstreamAccount{
		{ID: "acc_c1", ProviderCode: "openai", SupportedModels: []string{"gpt-4o"}},
	}
	// Catalog says missing: merge selection must not depend on it (B13).
	cache.providerRoute = func(input ProviderModelRouteInput) (ProviderModelRouteResolution, error) {
		return ProviderModelRouteResolution{Outcome: ProviderModelRouteMissing, ModelKey: input.Model, MatchedProviderCodes: []string{}}, nil
	}

	disabledBinding := mergeTestBinding("b3", "grp_c", "openai", 3)
	disabledBinding.GroupEnabled = 0
	apiKey := mergeTestAPIKey(RouteStrategyModeMerge,
		mergeTestBinding("b1", "grp_a", "openai", 1),
		mergeTestBinding("b2", "grp_b", "anthropic", 2),
		disabledBinding,
	)

	result := runMergeRouteTest(t, cache, nil, apiKey, mergeTestRequest("gpt-4o"))

	if result.Outcome != NormalRouteOutcomeSelected {
		t.Fatalf("outcome = %q, want selected (result: %+v)", result.Outcome, result)
	}
	if result.RouteSource != RouteSourceMerged {
		t.Fatalf("route source = %q, want %q", result.RouteSource, RouteSourceMerged)
	}
	if result.GroupID != "grp_a" {
		t.Fatalf("window group = %q, want grp_a", result.GroupID)
	}
	if result.MatchedProviderCode != "" {
		t.Fatalf("matched provider code = %q, want empty (B11)", result.MatchedProviderCode)
	}
	if result.RequestedModel != "gpt-4o" {
		t.Fatalf("requested model = %q, want gpt-4o", result.RequestedModel)
	}
	if got := segmentSummary(result.GroupSegments); !equalStrings(got, []string{"grp_a:acc_a1,acc_a2", "grp_b:acc_b1"}) {
		t.Fatalf("segments = %v, want [grp_a:acc_a1,acc_a2 grp_b:acc_b1]", got)
	}
	if got := accountIDs(result.Accounts); !equalStrings(got, []string{"acc_a1", "acc_a2", "acc_b1"}) {
		t.Fatalf("flat accounts = %v, want [acc_a1 acc_a2 acc_b1] (binding order + intra-group order)", got)
	}
	if result.GroupAccess.ProviderCode != "openai" || result.GroupAccess.GroupAccessType != GroupAccessTypeOwner {
		t.Fatalf("top-level group access = %+v, want grp_a access", result.GroupAccess)
	}
	if result.GroupSegments[0].GroupAccess.ProviderCode != "openai" || result.GroupSegments[1].GroupAccess.ProviderCode != "anthropic" {
		t.Fatalf("segment group access = [%+v %+v], want per-group access", result.GroupSegments[0].GroupAccess, result.GroupSegments[1].GroupAccess)
	}
	// B11: GroupBindings stay full (no single-provider narrowing); SelectedGroupID = window group.
	if got := bindingIDs(result.APIKeyRecord.GroupBindings); !equalStrings(got, []string{"b1", "b2", "b3"}) {
		t.Fatalf("group bindings = %v, want full [b1 b2 b3]", got)
	}
	if result.APIKeyRecord.SelectedGroupID != "grp_a" {
		t.Fatalf("selected group id on record = %q, want grp_a", result.APIKeyRecord.SelectedGroupID)
	}
	if len(result.ResponseInspectionPolicies) != 0 {
		t.Fatalf("response inspection policies = %v, want empty", result.ResponseInspectionPolicies)
	}
	// The disabled binding must not be consulted at all (defensive filter).
	for _, groupID := range cache.callsGroupAccess {
		if groupID == "grp_c" {
			t.Fatalf("GroupEnabled=0 binding was consulted (calls: %v)", cache.callsGroupAccess)
		}
	}
	// Each consulted group gets the requested model + endpoint family.
	if len(cache.callsAccounts) != 2 {
		t.Fatalf("accounts calls = %d, want 2", len(cache.callsAccounts))
	}
	for _, call := range cache.callsAccounts {
		if call.options.RequestedModel != "gpt-4o" || call.options.RequestedEndpointFamily != EndpointFamilyChatCompletions {
			t.Fatalf("accounts call options = %+v, want model gpt-4o family %s", call.options, EndpointFamilyChatCompletions)
		}
	}
}

// B9: two bindings on the same provider must still merge instead of hitting
// the single-provider skip.
func TestMergeRouteSingleProviderDoesNotSkip(t *testing.T) {
	cache := newFakeRuntimeCache()
	cache.groupAccess["grp_a"] = mergeTestAccess("openai")
	cache.accounts["grp_a"] = []UpstreamAccount{
		{ID: "acc_a1", ProviderCode: "openai", SupportedModels: []string{"gpt-4o"}},
	}
	cache.groupAccess["grp_b"] = mergeTestAccess("openai")
	cache.accounts["grp_b"] = []UpstreamAccount{
		{ID: "acc_b1", ProviderCode: "openai", SupportedModels: []string{"gpt-4o"}},
	}
	cache.providerRoute = func(input ProviderModelRouteInput) (ProviderModelRouteResolution, error) {
		return ProviderModelRouteResolution{Outcome: ProviderModelRouteMissing, ModelKey: input.Model, MatchedProviderCodes: []string{}}, nil
	}
	apiKey := mergeTestAPIKey(RouteStrategyModeMerge,
		mergeTestBinding("b1", "grp_a", "openai", 1),
		mergeTestBinding("b2", "grp_b", "openai", 2),
	)

	result := runMergeRouteTest(t, cache, nil, apiKey, mergeTestRequest("gpt-4o"))

	if result.Outcome != NormalRouteOutcomeSelected {
		t.Fatalf("outcome = %q, want selected (single-provider skip must not apply, B9)", result.Outcome)
	}
	if result.RouteSource != RouteSourceMerged {
		t.Fatalf("route source = %q, want %q", result.RouteSource, RouteSourceMerged)
	}
	if got := segmentSummary(result.GroupSegments); !equalStrings(got, []string{"grp_a:acc_a1", "grp_b:acc_b1"}) {
		t.Fatalf("segments = %v, want [grp_a:acc_a1 grp_b:acc_b1]", got)
	}
	if got := accountIDs(result.Accounts); !equalStrings(got, []string{"acc_a1", "acc_b1"}) {
		t.Fatalf("flat accounts = %v, want [acc_a1 acc_b1]", got)
	}
}

// 3.2: an account reachable only through its model mapping joins the merged
// pool even though the catalog has no route for the requested model; the
// direct-only first group contributes an empty fragment and is skipped, so
// the window group becomes the second group.
func TestMergeRouteMappingAccountEntersSecondSegment(t *testing.T) {
	cache := newFakeRuntimeCache()
	cache.groupAccess["grp_a"] = mergeTestAccess("openai")
	cache.accounts["grp_a"] = []UpstreamAccount{
		{ID: "acc_direct", ProviderCode: "openai", SupportedModels: []string{"other-model"}},
	}
	cache.groupAccess["grp_b"] = mergeTestAccess("anthropic")
	cache.accounts["grp_b"] = []UpstreamAccount{
		{
			ID: "acc_map", ProviderCode: "anthropic",
			ProtocolCode: "openai", ProtocolVersion: "v1",
			ModelMappings: []gatewayAccountModelMapping{{
				SourceModel: "my-model", SourceEndpointFamily: EndpointFamilyChatCompletions,
				UpstreamModel: "gpt-4o", UpstreamEndpointFamily: EndpointFamilyChatCompletions,
			}},
			SupportedModels: []string{"gpt-4o"},
		},
	}
	cache.providerRoute = func(input ProviderModelRouteInput) (ProviderModelRouteResolution, error) {
		return ProviderModelRouteResolution{Outcome: ProviderModelRouteMissing, ModelKey: input.Model, MatchedProviderCodes: []string{}}, nil
	}
	apiKey := mergeTestAPIKey(RouteStrategyModeMerge,
		mergeTestBinding("b1", "grp_a", "openai", 1),
		mergeTestBinding("b2", "grp_b", "anthropic", 2),
	)

	result := runMergeRouteTest(t, cache, nil, apiKey, mergeTestRequest("my-model"))

	if result.Outcome != NormalRouteOutcomeSelected {
		t.Fatalf("outcome = %q, want selected (result: %+v)", result.Outcome, result)
	}
	if got := segmentSummary(result.GroupSegments); !equalStrings(got, []string{"grp_b:acc_map"}) {
		t.Fatalf("segments = %v, want [grp_b:acc_map] (empty first fragment skipped)", got)
	}
	if result.GroupID != "grp_b" {
		t.Fatalf("window group = %q, want grp_b (first non-empty segment)", result.GroupID)
	}
	if result.APIKeyRecord.SelectedGroupID != "grp_b" {
		t.Fatalf("selected group id on record = %q, want grp_b", result.APIKeyRecord.SelectedGroupID)
	}
	if got := accountIDs(result.Accounts); !equalStrings(got, []string{"acc_map"}) {
		t.Fatalf("flat accounts = %v, want [acc_map]", got)
	}
}

// Inaccessible groups (access metadata not found) and groups whose accounts
// all fail the model filter are skipped; surviving groups still merge.
func TestMergeRouteSkipsInaccessibleAndEmptyGroups(t *testing.T) {
	cache := newFakeRuntimeCache()
	cache.missingAccess["grp_a"] = true
	cache.groupAccess["grp_b"] = mergeTestAccess("anthropic")
	cache.accounts["grp_b"] = []UpstreamAccount{
		{ID: "acc_b1", ProviderCode: "anthropic", SupportedModels: []string{"gpt-4o"}},
	}
	cache.providerRoute = func(input ProviderModelRouteInput) (ProviderModelRouteResolution, error) {
		return ProviderModelRouteResolution{Outcome: ProviderModelRouteMissing, ModelKey: input.Model, MatchedProviderCodes: []string{}}, nil
	}
	apiKey := mergeTestAPIKey(RouteStrategyModeMerge,
		mergeTestBinding("b1", "grp_a", "openai", 1),
		mergeTestBinding("b2", "grp_b", "anthropic", 2),
	)

	result := runMergeRouteTest(t, cache, nil, apiKey, mergeTestRequest("gpt-4o"))

	if result.Outcome != NormalRouteOutcomeSelected {
		t.Fatalf("outcome = %q, want selected (result: %+v)", result.Outcome, result)
	}
	if got := segmentSummary(result.GroupSegments); !equalStrings(got, []string{"grp_b:acc_b1"}) {
		t.Fatalf("segments = %v, want [grp_b:acc_b1]", got)
	}
	if result.GroupID != "grp_b" {
		t.Fatalf("window group = %q, want grp_b", result.GroupID)
	}
}

// When every fragment is empty the failure is classified through the catalog
// route: matched-and-bound provider → 503 model_target_group_unavailable;
// catalog missing → 400 model_target_group_not_bound; catalog ambiguous must
// NOT produce model_route_ambiguous (B11/B13).
func TestMergeRouteAllEmptyFailureClassification(t *testing.T) {
	bindings := func() *APIKeyRow {
		return mergeTestAPIKey(RouteStrategyModeMerge,
			mergeTestBinding("b1", "grp_a", "openai", 1),
			mergeTestBinding("b2", "grp_b", "anthropic", 2),
		)
	}
	newCache := func() *fakeRuntimeCache {
		cache := newFakeRuntimeCache()
		// grp_a access exists but its account cannot serve the model
		// (empty fragment); grp_b access missing entirely.
		cache.groupAccess["grp_a"] = mergeTestAccess("openai")
		cache.accounts["grp_a"] = []UpstreamAccount{
			{ID: "acc_a1", ProviderCode: "openai", SupportedModels: []string{"other-model"}},
		}
		cache.missingAccess["grp_b"] = true
		return cache
	}

	tests := []struct {
		name             string
		providerRoute    ProviderModelRouteResolution
		wantStatusCode   int
		wantType         string
		wantCode         string
		wantMessage      string
		wantMatchedCodes []string
	}{
		{
			name: "catalog matched and bound fails model_target_group_unavailable",
			providerRoute: ProviderModelRouteResolution{
				Outcome: ProviderModelRouteMatched, ModelKey: "gpt-4o",
				ProviderCode: "openai", MatchedProviderCodes: []string{"openai"},
			},
			wantStatusCode:   503,
			wantType:         "service_unavailable",
			wantCode:         FailCodeModelTargetGroupUnavailable,
			wantMessage:      "请求模型对应的供应商分组当前没有可用账号：gpt-4o",
			wantMatchedCodes: []string{"openai"},
		},
		{
			name: "catalog missing fails model_target_group_not_bound",
			providerRoute: ProviderModelRouteResolution{
				Outcome: ProviderModelRouteMissing, ModelKey: "gpt-4o",
				MatchedProviderCodes: []string{},
			},
			wantStatusCode:   400,
			wantType:         "invalid_request_error",
			wantCode:         FailCodeModelTargetGroupNotBound,
			wantMessage:      "当前 API Key 未绑定请求模型对应的供应商分组：gpt-4o",
			wantMatchedCodes: []string{},
		},
		{
			name: "catalog ambiguous never yields model_route_ambiguous (B11)",
			providerRoute: ProviderModelRouteResolution{
				Outcome: ProviderModelRouteAmbiguous, ModelKey: "gpt-4o",
				MatchedProviderCodes: []string{"openai", "anthropic"},
			},
			wantStatusCode:   400,
			wantType:         "invalid_request_error",
			wantCode:         FailCodeModelTargetGroupNotBound,
			wantMessage:      "当前 API Key 未绑定请求模型对应的供应商分组：gpt-4o",
			wantMatchedCodes: []string{"openai", "anthropic"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache := newCache()
			cache.providerRoute = func(input ProviderModelRouteInput) (ProviderModelRouteResolution, error) {
				return tt.providerRoute, nil
			}

			result := runMergeRouteTest(t, cache, nil, bindings(), mergeTestRequest("gpt-4o"))

			if result.Outcome != NormalRouteOutcomeFailed {
				t.Fatalf("outcome = %q, want failed (result: %+v)", result.Outcome, result)
			}
			if result.StatusCode != tt.wantStatusCode || result.Type != tt.wantType || result.Code != tt.wantCode || result.Message != tt.wantMessage {
				t.Fatalf("failure = [%d %s %s %s], want [%d %s %s %s]",
					result.StatusCode, result.Type, result.Code, result.Message,
					tt.wantStatusCode, tt.wantType, tt.wantCode, tt.wantMessage)
			}
			if !equalStrings(result.MatchedProviderCodes, tt.wantMatchedCodes) {
				t.Fatalf("matched provider codes = %v, want %v", result.MatchedProviderCodes, tt.wantMatchedCodes)
			}
			if result.RequestedModel != "gpt-4o" {
				t.Fatalf("requested model = %q, want gpt-4o", result.RequestedModel)
			}
			if len(result.GroupSegments) != 0 {
				t.Fatalf("group segments = %v, want empty on failure", result.GroupSegments)
			}
		})
	}
}

// B12: an account bound to several groups joins the pool once, through its
// highest-priority (first-seen) group; the later group's segment keeps only
// its own accounts.
func TestMergeRouteDedupesCrossGroupAccounts(t *testing.T) {
	cache := newFakeRuntimeCache()
	shared := UpstreamAccount{ID: "acc_shared", ProviderCode: "openai", SupportedModels: []string{"gpt-4o"}}
	cache.groupAccess["grp_a"] = mergeTestAccess("openai")
	cache.accounts["grp_a"] = []UpstreamAccount{
		shared,
		{ID: "acc_a1", ProviderCode: "openai", SupportedModels: []string{"gpt-4o"}},
	}
	cache.groupAccess["grp_b"] = mergeTestAccess("openai")
	cache.accounts["grp_b"] = []UpstreamAccount{
		shared,
		{ID: "acc_b1", ProviderCode: "openai", SupportedModels: []string{"gpt-4o"}},
	}
	cache.providerRoute = func(input ProviderModelRouteInput) (ProviderModelRouteResolution, error) {
		return ProviderModelRouteResolution{Outcome: ProviderModelRouteMissing, ModelKey: input.Model, MatchedProviderCodes: []string{}}, nil
	}
	apiKey := mergeTestAPIKey(RouteStrategyModeMerge,
		mergeTestBinding("b1", "grp_a", "openai", 1),
		mergeTestBinding("b2", "grp_b", "openai", 2),
	)

	result := runMergeRouteTest(t, cache, nil, apiKey, mergeTestRequest("gpt-4o"))

	if result.Outcome != NormalRouteOutcomeSelected {
		t.Fatalf("outcome = %q, want selected (result: %+v)", result.Outcome, result)
	}
	if got := segmentSummary(result.GroupSegments); !equalStrings(got, []string{"grp_a:acc_shared,acc_a1", "grp_b:acc_b1"}) {
		t.Fatalf("segments = %v, want [grp_a:acc_shared,acc_a1 grp_b:acc_b1] (first group wins acc_shared)", got)
	}
	if got := accountIDs(result.Accounts); !equalStrings(got, []string{"acc_shared", "acc_a1", "acc_b1"}) {
		t.Fatalf("flat accounts = %v, want [acc_shared acc_a1 acc_b1]", got)
	}
	if result.GroupID != "grp_a" {
		t.Fatalf("window group = %q, want grp_a", result.GroupID)
	}
}

// The capability probe keeps its position inside each group's walk: blocked
// accounts drop out of their group's fragment before the model filter.
func TestMergeRouteCapabilityFilterAppliesPerGroup(t *testing.T) {
	cache := newFakeRuntimeCache()
	cache.groupAccess["grp_a"] = mergeTestAccess("openai")
	cache.accounts["grp_a"] = []UpstreamAccount{
		{ID: "acc_a1", ProviderCode: "openai", SupportedModels: []string{"gpt-4o"}},
		{ID: "acc_a2", ProviderCode: "openai", SupportedModels: []string{"gpt-4o"}},
	}
	cache.groupAccess["grp_b"] = mergeTestAccess("anthropic")
	cache.accounts["grp_b"] = []UpstreamAccount{
		{ID: "acc_b1", ProviderCode: "anthropic", SupportedModels: []string{"gpt-4o"}},
	}
	cache.providerRoute = func(input ProviderModelRouteInput) (ProviderModelRouteResolution, error) {
		return ProviderModelRouteResolution{Outcome: ProviderModelRouteMissing, ModelKey: input.Model, MatchedProviderCodes: []string{}}, nil
	}
	apiKey := mergeTestAPIKey(RouteStrategyModeMerge,
		mergeTestBinding("b1", "grp_a", "openai", 1),
		mergeTestBinding("b2", "grp_b", "anthropic", 2),
	)
	capability := &scriptedCapabilityFilter{blocked: map[string]bool{"acc_a1": true}}

	result := runMergeRouteTest(t, cache, capability, apiKey, mergeTestRequest("gpt-4o"))

	if result.Outcome != NormalRouteOutcomeSelected {
		t.Fatalf("outcome = %q, want selected (result: %+v)", result.Outcome, result)
	}
	if got := segmentSummary(result.GroupSegments); !equalStrings(got, []string{"grp_a:acc_a2", "grp_b:acc_b1"}) {
		t.Fatalf("segments = %v, want [grp_a:acc_a2 grp_b:acc_b1]", got)
	}
	if capability.seenModel != "gpt-4o" {
		t.Fatalf("capability probe model = %q, want gpt-4o", capability.seenModel)
	}
}

// Defensive: merge bindings exist but none is enabled → empty_binding skip.
func TestMergeRouteAllBindingsDisabledSkips(t *testing.T) {
	cache := newFakeRuntimeCache()
	disabled := mergeTestBinding("b1", "grp_a", "openai", 1)
	disabled.GroupEnabled = 0
	apiKey := mergeTestAPIKey(RouteStrategyModeMerge, disabled)

	result := runMergeRouteTest(t, cache, nil, apiKey, mergeTestRequest("gpt-4o"))

	if result.Outcome != NormalRouteOutcomeSkipped {
		t.Fatalf("outcome = %q, want skipped", result.Outcome)
	}
	if result.Reason != SkipReasonEmptyBinding {
		t.Fatalf("reason = %q, want %q", result.Reason, SkipReasonEmptyBinding)
	}
	if len(cache.callsGroupAccess) != 0 {
		t.Fatalf("group access consulted for disabled bindings: %v", cache.callsGroupAccess)
	}
}

// Regression lock: every non-merge mode keeps the single-group
// contract — selected group, narrowed bindings, and a nil GroupSegments.
func TestNonMergeModesLeaveGroupSegmentsEmpty(t *testing.T) {
	for _, mode := range []string{
		RouteStrategyModeNormal,
		RouteStrategyModeWeighted,
		RouteStrategyModeFailover,
		RouteStrategyModeRoundRobin,
	} {
		t.Run(mode, func(t *testing.T) {
			cache := newFakeRuntimeCache()
			cache.groupAccess["grp_a"] = mergeTestAccess("openai")
			cache.accounts["grp_a"] = []UpstreamAccount{
				{ID: "acc_a1", ProviderCode: "openai", SupportedModels: []string{"gpt-4o"}},
			}
			cache.groupAccess["grp_b"] = mergeTestAccess("anthropic")
			cache.accounts["grp_b"] = []UpstreamAccount{
				{ID: "acc_b1", ProviderCode: "anthropic", SupportedModels: []string{"gpt-4o"}},
			}
			cache.providerRoute = func(input ProviderModelRouteInput) (ProviderModelRouteResolution, error) {
				return ProviderModelRouteResolution{
					Outcome: ProviderModelRouteMatched, ModelKey: "gpt-4o",
					ProviderCode: "openai", MatchedProviderCodes: []string{"openai"},
				}, nil
			}
			apiKey := mergeTestAPIKey(mode,
				mergeTestBinding("b1", "grp_a", "openai", 1),
				mergeTestBinding("b2", "grp_b", "anthropic", 2),
			)

			result := runMergeRouteTest(t, cache, nil, apiKey, mergeTestRequest("gpt-4o"))

			if result.Outcome != NormalRouteOutcomeSelected {
				t.Fatalf("outcome = %q, want selected (mode %s)", result.Outcome, mode)
			}
			if result.GroupID != "grp_a" {
				t.Fatalf("group id = %q, want grp_a (mode %s)", result.GroupID, mode)
			}
			if result.RouteSource != RouteSourceCatalogProvider {
				t.Fatalf("route source = %q, want %q (mode %s)", result.RouteSource, RouteSourceCatalogProvider, mode)
			}
			if len(result.GroupSegments) != 0 {
				t.Fatalf("group segments = %v, want empty (mode %s)", result.GroupSegments, mode)
			}
			if got := bindingIDs(result.APIKeyRecord.GroupBindings); !equalStrings(got, []string{"b1"}) {
				t.Fatalf("selected bindings = %v, want narrowed [b1] (mode %s)", got, mode)
			}
			if result.APIKeyRecord.SelectedGroupID != "grp_a" {
				t.Fatalf("selected group id = %q, want grp_a (mode %s)", result.APIKeyRecord.SelectedGroupID, mode)
			}
		})
	}
}

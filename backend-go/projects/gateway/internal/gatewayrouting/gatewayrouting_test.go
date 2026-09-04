package gatewayrouting

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// ---------------------------------------------------------------------------
// Test doubles (strict mock closure over the injected ports)
// ---------------------------------------------------------------------------

// fakeRuntimeCache replays scripted results per group/model and records the
// calls the routing core made.
type fakeRuntimeCache struct {
	groupAccess   map[string]GroupUsageAccessMetadata // keyed by groupID
	missingAccess map[string]bool
	accounts      map[string][]UpstreamAccount // keyed by groupID
	providerRoute func(input ProviderModelRouteInput) (ProviderModelRouteResolution, error)

	callsGroupAccess []string
	callsAccounts    []cachedAccountsCall
	callsRoute       []ProviderModelRouteInput
	routeErr         error
	accountsErr      error
	accessErr        error
}

type cachedAccountsCall struct {
	groupID         string
	systemAccountID string
	options         CachedAccountsForGroupOptions
}

func newFakeRuntimeCache() *fakeRuntimeCache {
	return &fakeRuntimeCache{
		groupAccess:   map[string]GroupUsageAccessMetadata{},
		missingAccess: map[string]bool{},
		accounts:      map[string][]UpstreamAccount{},
	}
}

func (f *fakeRuntimeCache) ResolveCachedGroupUsageAccessMetadataAsync(_ context.Context, groupID, _ string) (GroupUsageAccessMetadata, bool, error) {
	f.callsGroupAccess = append(f.callsGroupAccess, groupID)
	if f.accessErr != nil {
		return GroupUsageAccessMetadata{}, false, f.accessErr
	}
	if f.missingAccess[groupID] {
		return GroupUsageAccessMetadata{}, false, nil
	}
	access, ok := f.groupAccess[groupID]
	if !ok {
		return GroupUsageAccessMetadata{}, false, nil
	}
	return access, true, nil
}

func (f *fakeRuntimeCache) ListCachedOpenAIAccountsForGroupAsync(_ context.Context, groupID, _ string, options CachedAccountsForGroupOptions) ([]UpstreamAccount, error) {
	f.callsAccounts = append(f.callsAccounts, cachedAccountsCall{groupID: groupID, options: options})
	if f.accountsErr != nil {
		return nil, f.accountsErr
	}
	return f.accounts[groupID], nil
}

func (f *fakeRuntimeCache) ResolveCachedProviderModelRouteAsync(_ context.Context, input ProviderModelRouteInput) (ProviderModelRouteResolution, error) {
	f.callsRoute = append(f.callsRoute, input)
	if f.routeErr != nil {
		return ProviderModelRouteResolution{}, f.routeErr
	}
	if f.providerRoute == nil {
		return ProviderModelRouteResolution{Outcome: ProviderModelRouteMissing, ModelKey: input.Model, MatchedProviderCodes: []string{}}, nil
	}
	return f.providerRoute(input)
}

// scriptedCapabilityFilter filters accounts whose ID is listed as blocked.
type scriptedCapabilityFilter struct {
	blocked          map[string]bool
	reason           string
	seenModel        string
	seenCompat       string
	seenAccountCount int
}

func (s *scriptedCapabilityFilter) FilterAccountsByRequestCapability(_ context.Context, accounts []UpstreamAccount, input CapabilityFilterInput) CapabilityFilterResult {
	s.seenModel = input.RequestModel
	s.seenCompat = input.RequestClientCompatibility
	s.seenAccountCount = len(accounts)
	filtered := make([]UpstreamAccount, 0, len(accounts))
	for _, account := range accounts {
		if s.blocked[account.ID] {
			continue
		}
		filtered = append(filtered, account)
	}
	return CapabilityFilterResult{
		Accounts:     filtered,
		SkippedCount: len(accounts) - len(filtered),
		Reason:       s.reason,
	}
}

// failingCapabilityFilter reproduces a probe that never lets accounts pass.
type failingCapabilityFilter struct{ reason string }

func (s failingCapabilityFilter) FilterAccountsByRequestCapability(_ context.Context, accounts []UpstreamAccount, _ CapabilityFilterInput) CapabilityFilterResult {
	return CapabilityFilterResult{Accounts: []UpstreamAccount{}, SkippedCount: len(accounts), Reason: s.reason}
}

// scriptedRedisCounter replays counter values in order.
type scriptedRedisCounter struct {
	values []int64
	err    error
	keys   []string
	moduli []int64
}

func (s *scriptedRedisCounter) NextRouteCounterIndex(_ context.Context, key string, modulo int64) (int64, error) {
	s.keys = append(s.keys, key)
	s.moduli = append(s.moduli, modulo)
	if s.err != nil {
		return 0, s.err
	}
	if len(s.values) == 0 {
		return 0, nil
	}
	value := s.values[0]
	s.values = s.values[1:]
	return value, nil
}

// recordingObserver captures routing observation events.
type recordingObserver struct {
	kinds    []string
	outcomes []string
	nows     []int64
}

func (r *recordingObserver) ObserveRouting(kind, outcome string, nowMs int64) {
	r.kinds = append(r.kinds, kind)
	r.outcomes = append(r.outcomes, outcome)
	r.nows = append(r.nows, nowMs)
}

// fixedClock is the injected clock.
type fixedClock struct{ nowMs int64 }

func (c *fixedClock) Now() int64 { return c.nowMs }

// ---------------------------------------------------------------------------
// Builders
// ---------------------------------------------------------------------------

func int64Ptr(v int64) *int64    { return &v }
func intPtr(v int) *int          { return &v }
func weightPtr(v int64) *int64   { return &v }
func boolPtr(v bool) *bool       { return &v }

func mustDeadline(t *testing.T, input NormalRouteAttemptFirstByteDeadlineInput) NormalRouteAttemptFirstByteDeadline {
	t.Helper()
	deadline, err := ResolveNormalRouteAttemptFirstByteDeadline(input)
	if err != nil {
		t.Fatalf("ResolveNormalRouteAttemptFirstByteDeadline returned error: %v", err)
	}
	return deadline
}

// ---------------------------------------------------------------------------
// Table-driven coverage
// ---------------------------------------------------------------------------

func TestResolveNormalGatewayModelRoute(t *testing.T) {
	account := func(id, providerCode string, supportedModels ...string) UpstreamAccount {
		return UpstreamAccount{
			ID:           id,
			ProviderCode: providerCode,
			SupportedModels: supportedModels,
		}
	}
	binding := func(id, groupID, providerCode string, priority int64) GroupBindingRow {
		return GroupBindingRow{
			ID: id, APIKeyID: "key1", SystemAccountID: "owner1",
			GroupID: groupID, Priority: priority, Status: RowStatusActive,
			ProviderCode: providerCode, GroupEnabled: 1,
		}
	}
	apiKey := func(bindings ...GroupBindingRow) *APIKeyRow {
		return &APIKeyRow{
			ID: "key1", SystemAccountID: "owner1",
			RouteStrategyMode: RouteStrategyModeNormal,
			Status:            RowStatusActive,
			GroupBindings:     bindings,
		}
	}
	groupAccess := func(providerCode string) GroupUsageAccessMetadata {
		return GroupUsageAccessMetadata{ProviderCode: providerCode, GroupAccessType: GroupAccessTypeOwner}
	}

	tests := []struct {
		name string
		// inputs
		apiKey        *APIKeyRow
		request       RequestView
		cache         func(*fakeRuntimeCache)
		capability    AccountCapabilityFilter
		providerRoute ProviderModelRouteResolution
		// expectations
		wantOutcome       string
		wantReason        string
		wantStatusCode    int
		wantType          string
		wantCode          string
		wantMessage       string
		wantGroupID       string
		wantRouteSource   NormalGatewayModelRouteSource
		wantMatchedCode   string
		wantMatchedCodes  []string
		wantAccounts      []string
		wantSelectedBindings []string
	}{
		{
			name:        "hybrid_smart skips with reason",
			apiKey:      &APIKeyRow{RouteStrategyMode: RouteStrategyModeHybridSmart},
			wantOutcome: NormalRouteOutcomeSkipped,
			wantReason:  SkipReasonRouteStrategyIsHybridSmart,
		},
		{
			name:        "missing requested model skips",
			apiKey:      apiKey(),
			request:     RequestView{Method: "POST", OriginalURL: "/v1/chat/completions"},
			wantOutcome: NormalRouteOutcomeSkipped,
			wantReason:  SkipReasonMissingRequestedModel,
		},
		{
			name: "empty active bindings skip",
			apiKey: &APIKeyRow{
				RouteStrategyMode: RouteStrategyModeNormal,
				GroupBindings:     []GroupBindingRow{{ID: "b1", Status: RowStatusDisabled, ProviderCode: "openai"}},
			},
			request:     RequestView{Method: "POST", OriginalURL: "/v1/chat/completions", BodyModel: "gpt-4o"},
			wantOutcome: NormalRouteOutcomeSkipped,
			wantReason:  SkipReasonEmptyBinding,
		},
		{
			name: "single provider skips",
			apiKey: apiKey(
				binding("b1", "grp_a", "openai", 1),
				binding("b2", "grp_b", "openai", 2),
			),
			request:     RequestView{Method: "POST", OriginalURL: "/v1/chat/completions", BodyModel: "gpt-4o"},
			wantOutcome: NormalRouteOutcomeSkipped,
			wantReason:  SkipReasonSingleProvider,
		},
		{
			name: "mapping target selected over catalog match, group_access provider wins for mapping source",
			apiKey: apiKey(
				binding("b1", "grp_a", "openai", 1),
				binding("b2", "grp_b", "anthropic", 2),
			),
			request: RequestView{Method: "POST", OriginalURL: "/v1/chat/completions", BodyModel: "gpt-4o"},
			cache: func(f *fakeRuntimeCache) {
				f.groupAccess["grp_a"] = groupAccess("openai")
				f.accounts["grp_a"] = []UpstreamAccount{account("acc1", "openai", "gpt-4o")}
				f.groupAccess["grp_b"] = groupAccess("anthropic")
				f.accounts["grp_b"] = []UpstreamAccount{account("acc2", "anthropic")}
			},
			providerRoute: ProviderModelRouteResolution{
				Outcome: ProviderModelRouteMatched, ModelKey: "gpt-4o",
				ProviderCode: "openai", MatchedProviderCodes: []string{"openai"},
			},
			wantOutcome:     NormalRouteOutcomeSelected,
			wantGroupID:     "grp_a",
			wantRouteSource: RouteSourceCatalogProvider,
			wantMatchedCode: "openai",
			wantAccounts:    []string{"acc1"},
			wantSelectedBindings: []string{"b1"},
		},
		{
			name: "mapping-only account routes through account_mapping with access provider code",
			apiKey: apiKey(
				binding("b1", "grp_a", "openai", 1),
				binding("b2", "grp_b", "anthropic", 2),
			),
			request: RequestView{Method: "POST", OriginalURL: "/v1/chat/completions", BodyModel: "my-model"},
			cache: func(f *fakeRuntimeCache) {
				f.groupAccess["grp_a"] = groupAccess("openai")
				f.accounts["grp_a"] = []UpstreamAccount{{
					ID: "acc1", ProviderCode: "openai",
					ProviderProtocolProfileID: "",
					ProtocolCode:              "openai",
					ProtocolVersion:           "v1",
					ModelMappings: []gatewayAccountModelMapping{{
						SourceModel: "my-model", SourceEndpointFamily: EndpointFamilyChatCompletions,
						UpstreamModel: "gpt-4o", UpstreamEndpointFamily: EndpointFamilyChatCompletions,
					}},
					SupportedModels: []string{"gpt-4o"},
				}}
			},
			providerRoute: ProviderModelRouteResolution{
				Outcome: ProviderModelRouteMissing, ModelKey: "my-model",
				MatchedProviderCodes: []string{},
			},
			wantOutcome:     NormalRouteOutcomeFailed,
			wantStatusCode:  400,
			wantType:        "invalid_request_error",
			wantCode:        FailCodeModelNotRoutableForAPIKey,
			wantMessage:     "当前 API Key 绑定的供应商中没有可路由模型：my-model",
			wantMatchedCodes: []string{},
		},
		{
			name: "catalog missing fails model_not_routable_for_api_key",
			apiKey: apiKey(
				binding("b1", "grp_a", "openai", 1),
				binding("b2", "grp_b", "anthropic", 2),
			),
			request: RequestView{Method: "POST", OriginalURL: "/v1/chat/completions", BodyModel: "gpt-4o"},
			providerRoute: ProviderModelRouteResolution{
				Outcome: ProviderModelRouteMissing, ModelKey: "gpt-4o",
				MatchedProviderCodes: []string{},
			},
			wantOutcome:     NormalRouteOutcomeFailed,
			wantStatusCode:  400,
			wantType:        "invalid_request_error",
			wantCode:        FailCodeModelNotRoutableForAPIKey,
			wantMessage:     "当前 API Key 绑定的供应商中没有可路由模型：gpt-4o",
			wantMatchedCodes: []string{},
		},
		{
			name: "catalog matched but provider unbound downgrades to missing",
			apiKey: apiKey(
				binding("b1", "grp_a", "openai", 1),
				binding("b2", "grp_b", "anthropic", 2),
			),
			request: RequestView{Method: "POST", OriginalURL: "/v1/chat/completions", BodyModel: "gpt-4o"},
			providerRoute: ProviderModelRouteResolution{
				Outcome: ProviderModelRouteMatched, ModelKey: "gpt-4o",
				ProviderCode: "gemini", MatchedProviderCodes: []string{"gemini"},
			},
			wantOutcome:     NormalRouteOutcomeFailed,
			wantStatusCode:  400,
			wantType:        "invalid_request_error",
			wantCode:        FailCodeModelNotRoutableForAPIKey,
			wantMessage:     "当前 API Key 绑定的供应商中没有可路由模型：gpt-4o",
			wantMatchedCodes: []string{"gemini"},
		},
		{
			name: "ambiguous catalog with no mapping target fails model_route_ambiguous",
			apiKey: apiKey(
				binding("b1", "grp_a", "openai", 1),
				binding("b2", "grp_b", "anthropic", 2),
			),
			request: RequestView{Method: "POST", OriginalURL: "/v1/chat/completions", BodyModel: "gpt-x"},
			cache: func(f *fakeRuntimeCache) {
				// groups exist but no account survives the model filter
				f.groupAccess["grp_a"] = groupAccess("openai")
				f.accounts["grp_a"] = []UpstreamAccount{account("acc1", "openai", "gpt-4o")}
				f.groupAccess["grp_b"] = groupAccess("anthropic")
				f.accounts["grp_b"] = []UpstreamAccount{account("acc2", "anthropic", "claude")}
			},
			providerRoute: ProviderModelRouteResolution{
				Outcome: ProviderModelRouteAmbiguous, ModelKey: "gpt-x",
				MatchedProviderCodes: []string{"openai", "anthropic"},
			},
			wantOutcome:     NormalRouteOutcomeFailed,
			wantStatusCode:  400,
			wantType:        "invalid_request_error",
			wantCode:        FailCodeModelRouteAmbiguous,
			wantMessage:     "请求模型在多个供应商中同时存在，无法确定目标号池：gpt-x",
			wantMatchedCodes: []string{"openai", "anthropic"},
		},
		{
			name: "missing catalog route short-circuits before target selection",
			apiKey: apiKey(
				binding("b1", "grp_a", "openai", 1),
				binding("b2", "grp_b", "anthropic", 2),
			),
			request: RequestView{Method: "POST", OriginalURL: "/v1/chat/completions", BodyModel: "gpt-x"},
			cache: func(f *fakeRuntimeCache) {
				f.missingAccess["grp_a"] = true
				f.missingAccess["grp_b"] = true
			},
			providerRoute: ProviderModelRouteResolution{
				Outcome: ProviderModelRouteMissing, ModelKey: "gpt-x",
				MatchedProviderCodes: []string{},
			},
			wantOutcome:     NormalRouteOutcomeFailed,
			wantStatusCode:  400,
			wantType:        "invalid_request_error",
			wantCode:        FailCodeModelNotRoutableForAPIKey,
			wantMessage:     "当前 API Key 绑定的供应商中没有可路由模型：gpt-x",
			wantMatchedCodes: []string{},
		},
		{
			name: "matched provider bound but no usable account fails model_target_group_unavailable",
			apiKey: apiKey(
				binding("b1", "grp_a", "openai", 1),
				binding("b2", "grp_b", "anthropic", 2),
			),
			request: RequestView{Method: "POST", OriginalURL: "/v1/chat/completions", BodyModel: "gpt-4o"},
			cache: func(f *fakeRuntimeCache) {
				f.groupAccess["grp_a"] = groupAccess("openai")
				f.accounts["grp_a"] = []UpstreamAccount{account("acc1", "openai", "gpt-4o")}
				f.groupAccess["grp_b"] = groupAccess("anthropic")
				f.accounts["grp_b"] = []UpstreamAccount{account("acc2", "anthropic", "claude")}
			},
			capability: failingCapabilityFilter{reason: "endpoint_mode_mismatch"},
			providerRoute: ProviderModelRouteResolution{
				Outcome: ProviderModelRouteMatched, ModelKey: "gpt-4o",
				ProviderCode: "openai", MatchedProviderCodes: []string{"openai"},
			},
			wantOutcome:     NormalRouteOutcomeFailed,
			wantStatusCode:  503,
			wantType:        "service_unavailable",
			wantCode:        FailCodeModelTargetGroupUnavailable,
			wantMessage:     "请求模型对应的供应商分组当前没有可用账号：gpt-4o",
			wantMatchedCodes: []string{"openai"},
		},
		{
			name: "capability probe filtering is applied before the model filter",
			apiKey: apiKey(
				binding("b1", "grp_a", "openai", 1),
				binding("b2", "grp_b", "anthropic", 2),
			),
			request: RequestView{Method: "POST", OriginalURL: "/v1/chat/completions", BodyModel: "gpt-4o"},
			cache: func(f *fakeRuntimeCache) {
				f.groupAccess["grp_a"] = groupAccess("openai")
				f.accounts["grp_a"] = []UpstreamAccount{account("acc1", "openai", "gpt-4o")}
				f.groupAccess["grp_b"] = groupAccess("anthropic")
				f.accounts["grp_b"] = []UpstreamAccount{account("acc2", "anthropic", "gpt-4o")}
			},
			capability: &scriptedCapabilityFilter{blocked: map[string]bool{"acc1": true}},
			providerRoute: ProviderModelRouteResolution{
				Outcome: ProviderModelRouteMatched, ModelKey: "gpt-4o",
				ProviderCode: "openai", MatchedProviderCodes: []string{"openai"},
			},
			wantOutcome:     NormalRouteOutcomeSelected,
			wantGroupID:     "grp_b",
			wantRouteSource: RouteSourceCatalogProvider,
			wantMatchedCode: "openai",
			wantAccounts:    []string{"acc2"},
			wantSelectedBindings: []string{"b2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache := newFakeRuntimeCache()
			if tt.cache != nil {
				tt.cache(cache)
			}
			cache.providerRoute = func(input ProviderModelRouteInput) (ProviderModelRouteResolution, error) {
				return tt.providerRoute, nil
			}
			capability := tt.capability
			if capability == nil {
				capability = PassthroughCapabilityFilter{}
			}
			service := NewNormalModelRouteService(cache, capability)

			result, err := service.ResolveNormalGatewayModelRoute(context.Background(), ResolveNormalGatewayModelRouteInput{
				Request:      tt.request,
				APIKeyRecord: tt.apiKey,
			})
			if err != nil {
				t.Fatalf("ResolveNormalGatewayModelRoute returned error: %v", err)
			}
			if result.Outcome != tt.wantOutcome {
				t.Fatalf("outcome = %q, want %q (result: %+v)", result.Outcome, tt.wantOutcome, result)
			}
			if tt.wantOutcome == NormalRouteOutcomeSelected {
				if result.GroupID != tt.wantGroupID {
					t.Fatalf("group id = %q, want %q", result.GroupID, tt.wantGroupID)
				}
				if result.RouteSource != tt.wantRouteSource {
					t.Fatalf("route source = %q, want %q", result.RouteSource, tt.wantRouteSource)
				}
				if result.MatchedProviderCode != tt.wantMatchedCode {
					t.Fatalf("matched provider code = %q, want %q", result.MatchedProviderCode, tt.wantMatchedCode)
				}
				if got := accountIDs(result.Accounts); !equalStrings(got, tt.wantAccounts) {
					t.Fatalf("accounts = %v, want %v", got, tt.wantAccounts)
				}
				if got := bindingIDs(result.APIKeyRecord.GroupBindings); !equalStrings(got, tt.wantSelectedBindings) {
					t.Fatalf("selected bindings = %v, want %v", got, tt.wantSelectedBindings)
				}
				if result.APIKeyRecord.SelectedGroupID != tt.wantGroupID {
					t.Fatalf("selected group id on record = %q, want %q", result.APIKeyRecord.SelectedGroupID, tt.wantGroupID)
				}
				if len(result.ResponseInspectionPolicies) != 0 {
					t.Fatalf("response inspection policies = %v, want empty", result.ResponseInspectionPolicies)
				}
				return
			}
			if result.Reason != tt.wantReason {
				t.Fatalf("reason = %q, want %q", result.Reason, tt.wantReason)
			}
			if tt.wantOutcome == NormalRouteOutcomeFailed {
				if result.StatusCode != tt.wantStatusCode {
					t.Fatalf("status code = %d, want %d", result.StatusCode, tt.wantStatusCode)
				}
				if result.Type != tt.wantType {
					t.Fatalf("type = %q, want %q", result.Type, tt.wantType)
				}
				if result.Code != tt.wantCode {
					t.Fatalf("code = %q, want %q", result.Code, tt.wantCode)
				}
				if result.Message != tt.wantMessage {
					t.Fatalf("message = %q, want %q", result.Message, tt.wantMessage)
				}
				if !equalStrings(result.MatchedProviderCodes, tt.wantMatchedCodes) {
					t.Fatalf("matched provider codes = %v, want %v", result.MatchedProviderCodes, tt.wantMatchedCodes)
				}
			}
		})
	}
}

// The mapping-hit branch needs at least two provider codes, a mapping-only
// account, and a catalog provider match on a different binding to exercise
// the full selected/account_mapping contract.
func TestResolveNormalGatewayModelRouteAccountMappingSelection(t *testing.T) {
	cache := newFakeRuntimeCache()
	mappingAccount := UpstreamAccount{
		ID: "acc1", ProviderCode: "openai",
		ProtocolCode: "openai", ProtocolVersion: "v1",
		ModelMappings: []gatewayAccountModelMapping{{
			SourceModel: "my-model", SourceEndpointFamily: EndpointFamilyChatCompletions,
			UpstreamModel: "gpt-4o", UpstreamEndpointFamily: EndpointFamilyChatCompletions,
		}},
		SupportedModels: []string{"gpt-4o"},
	}
	directAccount := UpstreamAccount{ID: "acc2", ProviderCode: "anthropic", SupportedModels: []string{"other"}}
	cache.groupAccess["grp_a"] = GroupUsageAccessMetadata{ProviderCode: "openai", GroupAccessType: GroupAccessTypeOwner}
	cache.accounts["grp_a"] = []UpstreamAccount{mappingAccount}
	cache.groupAccess["grp_b"] = GroupUsageAccessMetadata{ProviderCode: "anthropic", GroupAccessType: GroupAccessTypeOwner}
	cache.accounts["grp_b"] = []UpstreamAccount{directAccount}
	cache.providerRoute = func(input ProviderModelRouteInput) (ProviderModelRouteResolution, error) {
		return ProviderModelRouteResolution{
			Outcome: ProviderModelRouteMatched, ModelKey: "my-model",
			ProviderCode: "openai", MatchedProviderCodes: []string{"openai"},
		}, nil
	}

	service := NewNormalModelRouteService(cache, PassthroughCapabilityFilter{})
	apiKey := &APIKeyRow{
		ID: "key1", SystemAccountID: "owner1",
		RouteStrategyMode: RouteStrategyModeNormal,
		GroupBindings: []GroupBindingRow{
			{ID: "b1", GroupID: "grp_a", ProviderCode: "openai", Status: RowStatusActive, GroupEnabled: 1},
			{ID: "b2", GroupID: "grp_b", ProviderCode: "anthropic", Status: RowStatusActive, GroupEnabled: 1},
		},
	}

	result, err := service.ResolveNormalGatewayModelRoute(context.Background(), ResolveNormalGatewayModelRouteInput{
		Request:  RequestView{Method: "POST", OriginalURL: "/v1/chat/completions", BodyModel: "my-model"},
		APIKeyRecord: apiKey,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Outcome != NormalRouteOutcomeSelected {
		t.Fatalf("outcome = %q, want selected (result %+v)", result.Outcome, result)
	}
	if result.RouteSource != RouteSourceAccountMapping {
		t.Fatalf("route source = %q, want %q", result.RouteSource, RouteSourceAccountMapping)
	}
	if result.GroupID != "grp_a" {
		t.Fatalf("group id = %q, want grp_a", result.GroupID)
	}
	if result.MatchedProviderCode != "openai" {
		t.Fatalf("matched provider code = %q, want openai", result.MatchedProviderCode)
	}
	if got := accountIDs(result.Accounts); !equalStrings(got, []string{"acc1"}) {
		t.Fatalf("accounts = %v, want [acc1]", got)
	}
	// The cache options carry the requested model and endpoint family.
	if len(cache.callsAccounts) == 0 || cache.callsAccounts[0].options.RequestedModel != "my-model" ||
		cache.callsAccounts[0].options.RequestedEndpointFamily != EndpointFamilyChatCompletions {
		t.Fatalf("accounts cache options = %+v", cache.callsAccounts)
	}
	// Provider route lookup gets unique provider codes and includeUnpriced.
	if len(cache.callsRoute) != 1 {
		t.Fatalf("provider route calls = %d, want 1", len(cache.callsRoute))
	}
	if !equalStrings(cache.callsRoute[0].ProviderCodes, []string{"openai", "anthropic"}) {
		t.Fatalf("provider codes = %v", cache.callsRoute[0].ProviderCodes)
	}
	if !cache.callsRoute[0].IncludeUnpriced {
		t.Fatal("provider route call must set IncludeUnpriced")
	}
}

func TestNormalGatewayModelTargetPriority(t *testing.T) {
	tests := []struct {
		name                  string
		directMatchedCount    int
		mappingMatchedCount   int
		catalogProviderMatched bool
		want                  float64
	}{
		{"direct wins", 1, 0, false, 2},
		{"mapping second", 0, 1, false, 1},
		{"catalog third", 0, 0, true, 0},
		{"otherwise negative infinity", 0, 0, false, negInf()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalGatewayModelTargetPriority(GatewayModelAccountFilterResult{
				DirectMatchedCount:  tt.directMatchedCount,
				MappingMatchedCount: tt.mappingMatchedCount,
			}, tt.catalogProviderMatched)
			if got != tt.want {
				t.Fatalf("priority = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFilterAccountsByRequestedModel(t *testing.T) {
	direct := UpstreamAccount{ID: "direct", SupportedModels: []string{"gpt-4o", "gpt-4o-mini"}}
	mapping := UpstreamAccount{
		ID: "mapping", ProtocolCode: "openai", ProtocolVersion: "v1",
		SupportedModels: []string{"gpt-4o"},
		ModelMappings: []gatewayAccountModelMapping{{
			SourceModel: "alias", SourceEndpointFamily: EndpointFamilyChatCompletions,
			UpstreamModel: "gpt-4o", UpstreamEndpointFamily: EndpointFamilyChatCompletions,
		}},
	}
	mappingToUnsupported := UpstreamAccount{
		ID: "mapping-unsupported", ProtocolCode: "openai", ProtocolVersion: "v1",
		SupportedModels: []string{"other"},
		ModelMappings: []gatewayAccountModelMapping{{
			SourceModel: "alias", SourceEndpointFamily: EndpointFamilyChatCompletions,
			UpstreamModel: "gpt-4o", UpstreamEndpointFamily: EndpointFamilyChatCompletions,
		}},
	}
	disabledMapping := UpstreamAccount{
		ID: "disabled-mapping", ProtocolCode: "openai", ProtocolVersion: "v1",
		SupportedModels: []string{"gpt-4o"},
		ModelMappings: []gatewayAccountModelMapping{{
			SourceModel: "alias", SourceEndpointFamily: EndpointFamilyChatCompletions,
			UpstreamModel: "gpt-4o", UpstreamEndpointFamily: EndpointFamilyChatCompletions,
			Enabled: boolPtr(false),
		}},
	}
	noConstraint := UpstreamAccount{ID: "no-constraint"}

	tests := []struct {
		name                          string
		accounts                      []UpstreamAccount
		requestedModel                string
		wantAccounts                  []string
		wantSkipped                   int
		wantLimited                   int
		wantInvalidModelConstraint    int
		wantDirect                    int
		wantMapping                   int
		wantReason                    string
	}{
		{
			name:         "direct matches are ordered before mapping matches",
			accounts:     []UpstreamAccount{mapping, direct},
			requestedModel: "alias",
			wantAccounts: []string{"mapping"},
			wantMapping:  1,
			wantLimited:  2,
			wantSkipped:  1,
		},
		{
			name:         "mapping whose upstream is unsupported is skipped",
			accounts:     []UpstreamAccount{mappingToUnsupported},
			requestedModel: "alias",
			wantAccounts: []string{},
			wantSkipped:  1,
			wantLimited:  1,
			wantReason:   ModelFilterReasonUnsupportedModel,
		},
		{
			name:         "accounts without model constraints are invalid constraints",
			accounts:     []UpstreamAccount{noConstraint},
			requestedModel: "gpt-4o",
			wantAccounts: []string{},
			wantSkipped:  1,
			wantInvalidModelConstraint: 1,
			wantReason:   ModelFilterReasonUnsupportedModel,
		},
		{
			name:         "missing model without constraints reports missing_model",
			accounts:     []UpstreamAccount{noConstraint},
			requestedModel: "",
			wantAccounts: []string{},
			wantSkipped:  1,
			wantInvalidModelConstraint: 1,
			wantReason:   ModelFilterReasonMissingModel,
		},
		{
			name:         "disabled mapping falls through to direct match",
			accounts:     []UpstreamAccount{disabledMapping},
			requestedModel: "alias",
			wantAccounts: []string{},
			wantSkipped:  1,
			wantLimited:  1,
			wantReason:   ModelFilterReasonUnsupportedModel,
		},
		{
			name:         "direct model hit",
			accounts:     []UpstreamAccount{direct},
			requestedModel: "gpt-4o-mini",
			wantAccounts: []string{"direct"},
			wantDirect:   1,
			wantLimited:  1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FilterAccountsByRequestedModel(tt.accounts, tt.requestedModel, EndpointFamilyChatCompletions)
			if got := accountIDs(result.Accounts); !equalStrings(got, tt.wantAccounts) {
				t.Fatalf("accounts = %v, want %v", got, tt.wantAccounts)
			}
			if result.SkippedCount != tt.wantSkipped {
				t.Fatalf("skipped = %d, want %d", result.SkippedCount, tt.wantSkipped)
			}
			if result.LimitedAccountCount != tt.wantLimited {
				t.Fatalf("limited = %d, want %d", result.LimitedAccountCount, tt.wantLimited)
			}
			if result.InvalidModelConstraintCount != tt.wantInvalidModelConstraint {
				t.Fatalf("invalid constraints = %d, want %d", result.InvalidModelConstraintCount, tt.wantInvalidModelConstraint)
			}
			if result.DirectMatchedCount != tt.wantDirect {
				t.Fatalf("direct = %d, want %d", result.DirectMatchedCount, tt.wantDirect)
			}
			if result.MappingMatchedCount != tt.wantMapping {
				t.Fatalf("mapping = %d, want %d", result.MappingMatchedCount, tt.wantMapping)
			}
			if result.Reason != tt.wantReason {
				t.Fatalf("reason = %q, want %q", result.Reason, tt.wantReason)
			}
		})
	}
}

func TestGatewayModelFilterFailureMessage(t *testing.T) {
	tests := []struct {
		name    string
		result  GatewayModelAccountFilterResult
		want    string
	}{
		{
			name:   "missing model",
			result: GatewayModelAccountFilterResult{Reason: ModelFilterReasonMissingModel},
			want:   "请求缺少 model，当前分组内账户均需要按支持模型匹配，无法调度",
		},
		{
			name:   "unsupported model",
			result: GatewayModelAccountFilterResult{Reason: ModelFilterReasonUnsupportedModel, RequestedModel: "gpt-x"},
			want:   "当前分组无账户支持请求模型：gpt-x",
		},
		{
			name:   "unknown model fallback",
			result: GatewayModelAccountFilterResult{Reason: ModelFilterReasonUnsupportedModel},
			want:   "当前分组无账户支持请求模型：未知模型",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GatewayModelFilterFailureMessage(tt.result); got != tt.want {
				t.Fatalf("message = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSelectGatewayModelTargetGroup(t *testing.T) {
	baseBindings := []GroupBindingRow{
		{ID: "b1", GroupID: "grp_a", ProviderCode: "openai", Status: RowStatusActive, GroupEnabled: 1},
		{ID: "b2", GroupID: "grp_b", ProviderCode: "anthropic", Status: RowStatusActive, GroupEnabled: 1},
	}
	apiKey := &APIKeyRow{ID: "key1", SystemAccountID: "owner1"}

	t.Run("first viable candidate wins without priority callback", func(t *testing.T) {
		cache := newFakeRuntimeCache()
		cache.groupAccess["grp_a"] = GroupUsageAccessMetadata{ProviderCode: "openai"}
		cache.accounts["grp_a"] = []UpstreamAccount{{ID: "acc1", SupportedModels: []string{"gpt-4o"}}}
		selector := &TargetGroupSelector{RuntimeCache: cache, CapabilityFilter: PassthroughCapabilityFilter{}}

		selection, err := selector.SelectGatewayModelTargetGroup(context.Background(), ModelTargetGroupInput{
			Request:      RequestView{Method: "POST", OriginalURL: "/v1/chat/completions"},
			APIKeyRecord: apiKey,
			Bindings:     baseBindings,
			TargetModel:  "gpt-4o",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if selection == nil || selection.GroupID != "grp_a" {
			t.Fatalf("selection = %+v, want grp_a", selection)
		}
	})

	t.Run("duplicate group bindings are visited once", func(t *testing.T) {
		cache := newFakeRuntimeCache()
		cache.groupAccess["grp_a"] = GroupUsageAccessMetadata{ProviderCode: "openai"}
		cache.accounts["grp_a"] = []UpstreamAccount{{ID: "acc1", SupportedModels: []string{"gpt-4o"}}}
		selector := &TargetGroupSelector{RuntimeCache: cache, CapabilityFilter: PassthroughCapabilityFilter{}}

		_, err := selector.SelectGatewayModelTargetGroup(context.Background(), ModelTargetGroupInput{
			Request:      RequestView{Method: "POST", OriginalURL: "/v1/chat/completions"},
			APIKeyRecord: apiKey,
			Bindings: []GroupBindingRow{
				baseBindings[0],
				{ID: "b1-dup", GroupID: "grp_a", ProviderCode: "openai", Status: RowStatusActive, GroupEnabled: 1},
				baseBindings[1],
			},
			TargetModel: "gpt-4o",
			CandidatePriority: func(ModelTargetGroupCandidate) float64 { return 0 },
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(cache.callsGroupAccess) != 2 || !equalStrings(cache.callsGroupAccess, []string{"grp_a", "grp_b"}) {
			t.Fatalf("group access calls = %v, want [grp_a grp_b]", cache.callsGroupAccess)
		}
	})

	t.Run("priority callback keeps the strictly greatest candidate", func(t *testing.T) {
		cache := newFakeRuntimeCache()
		cache.groupAccess["grp_a"] = GroupUsageAccessMetadata{ProviderCode: "openai"}
		cache.accounts["grp_a"] = []UpstreamAccount{{ID: "acc1", SupportedModels: []string{"gpt-4o"}}}
		cache.groupAccess["grp_b"] = GroupUsageAccessMetadata{ProviderCode: "anthropic"}
		cache.accounts["grp_b"] = []UpstreamAccount{{ID: "acc2", SupportedModels: []string{"gpt-4o"}}}
		selector := &TargetGroupSelector{RuntimeCache: cache, CapabilityFilter: PassthroughCapabilityFilter{}}

		selection, err := selector.SelectGatewayModelTargetGroup(context.Background(), ModelTargetGroupInput{
			Request:      RequestView{Method: "POST", OriginalURL: "/v1/chat/completions"},
			APIKeyRecord: apiKey,
			Bindings:     baseBindings,
			TargetModel:  "gpt-4o",
			CandidatePriority: func(candidate ModelTargetGroupCandidate) float64 {
				if candidate.Binding.GroupID == "grp_b" {
					return 1
				}
				return 2
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if selection == nil || selection.GroupID != "grp_a" {
			t.Fatalf("selection = %+v, want grp_a (priority 2 beats 1)", selection)
		}
	})

	t.Run("empty candidate pipeline scenarios", func(t *testing.T) {
		tests := []struct {
			name   string
			setup  func(*fakeRuntimeCache)
			filter AccountCapabilityFilter
		}{
			{
				name:  "missing group access",
				setup: func(f *fakeRuntimeCache) { f.missingAccess["grp_a"] = true; f.missingAccess["grp_b"] = true },
			},
			{
				name: "empty cached accounts",
				setup: func(f *fakeRuntimeCache) {
					f.groupAccess["grp_a"] = GroupUsageAccessMetadata{ProviderCode: "openai"}
					f.accounts["grp_a"] = nil
					f.groupAccess["grp_b"] = GroupUsageAccessMetadata{ProviderCode: "anthropic"}
					f.accounts["grp_b"] = nil
				},
			},
			{
				name: "capability probe rejects everything",
				setup: func(f *fakeRuntimeCache) {
					f.groupAccess["grp_a"] = GroupUsageAccessMetadata{ProviderCode: "openai"}
					f.accounts["grp_a"] = []UpstreamAccount{{ID: "acc1", SupportedModels: []string{"gpt-4o"}}}
				},
				filter: failingCapabilityFilter{reason: "endpoint_mode_mismatch"},
			},
			{
				name: "model filter rejects everything",
				setup: func(f *fakeRuntimeCache) {
					f.groupAccess["grp_a"] = GroupUsageAccessMetadata{ProviderCode: "openai"}
					f.accounts["grp_a"] = []UpstreamAccount{{ID: "acc1", SupportedModels: []string{"other"}}}
				},
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				cache := newFakeRuntimeCache()
				tt.setup(cache)
				filter := tt.filter
				if filter == nil {
					filter = PassthroughCapabilityFilter{}
				}
				selector := &TargetGroupSelector{RuntimeCache: cache, CapabilityFilter: filter}
				selection, err := selector.SelectGatewayModelTargetGroup(context.Background(), ModelTargetGroupInput{
					Request:      RequestView{Method: "POST", OriginalURL: "/v1/chat/completions"},
					APIKeyRecord: apiKey,
					Bindings:     baseBindings,
					TargetModel:  "gpt-4o",
				})
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if selection != nil {
					t.Fatalf("selection = %+v, want nil", selection)
				}
			})
		}
	})

	t.Run("acceptCandidate gate and cache errors propagate", func(t *testing.T) {
		cache := newFakeRuntimeCache()
		cache.groupAccess["grp_a"] = GroupUsageAccessMetadata{ProviderCode: "openai"}
		cache.accounts["grp_a"] = []UpstreamAccount{{ID: "acc1", SupportedModels: []string{"gpt-4o"}}}
		selector := &TargetGroupSelector{RuntimeCache: cache, CapabilityFilter: PassthroughCapabilityFilter{}}
		selection, err := selector.SelectGatewayModelTargetGroup(context.Background(), ModelTargetGroupInput{
			Request:      RequestView{Method: "POST", OriginalURL: "/v1/chat/completions"},
			APIKeyRecord: apiKey,
			Bindings:     baseBindings[:1],
			TargetModel:  "gpt-4o",
			AcceptCandidate: func(ModelTargetGroupCandidate) bool { return false },
		})
		if err != nil || selection != nil {
			t.Fatalf("selection = %+v err = %v, want nil/nil", selection, err)
		}

		failing := newFakeRuntimeCache()
		failing.accessErr = errors.New("access lookup failed")
		failingSelector := &TargetGroupSelector{RuntimeCache: failing, CapabilityFilter: PassthroughCapabilityFilter{}}
		if _, err := failingSelector.SelectGatewayModelTargetGroup(context.Background(), ModelTargetGroupInput{
			Request:      RequestView{Method: "POST", OriginalURL: "/v1/chat/completions"},
			APIKeyRecord: apiKey,
			Bindings:     baseBindings[:1],
			TargetModel:  "gpt-4o",
		}); err == nil {
			t.Fatal("expected access error to propagate")
		}
	})
}

// ---------------------------------------------------------------------------
// Dispatch ordering (round-robin / weighted)
// ---------------------------------------------------------------------------

func TestOrderAPIKeyGroupBindingsForDispatch(t *testing.T) {
	weight := weightPtr
	binding := func(id, groupID string, priority int64, weight *int64) GroupBindingRow {
		return GroupBindingRow{
			ID: id, APIKeyID: "key1", GroupID: groupID,
			Priority: priority, Weight: weight, Status: RowStatusActive,
			ProviderCode: "openai", GroupEnabled: 1,
		}
	}

	t.Run("filters disabled bindings and disabled groups then sorts by priority", func(t *testing.T) {
		selector := NewAPIKeyGroupRouteSelector("memory", nil, "")
		apiKey := &APIKeyRow{
			ID: "key1", RouteStrategyMode: RouteStrategyModeNormal,
			GroupBindings: []GroupBindingRow{
				{ID: "disabled", GroupID: "grp_x", Status: RowStatusDisabled, GroupEnabled: 1, Priority: 0},
				{ID: "group-off", GroupID: "grp_y", Status: RowStatusActive, GroupEnabled: 0, Priority: 1},
				{ID: "b2", GroupID: "grp_b", Status: RowStatusActive, GroupEnabled: 1, Priority: 5, Weight: weight(3)},
				{ID: "b1", GroupID: "grp_a", Status: RowStatusActive, GroupEnabled: 1, Priority: 5, Weight: weight(2)},
			},
		}
		ordered, err := selector.OrderAPIKeyGroupBindingsForDispatch(apiKey)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := bindingIDs(ordered); !equalStrings(got, []string{"b1", "b2"}) {
			t.Fatalf("ordered = %v, want [b1 b2] (priority then group id)", got)
		}
		if *ordered[0].Weight != 2 {
			t.Fatalf("weight normalization lost: %v", ordered[0].Weight)
		}
	})

	t.Run("nil weight defaults to 1; out-of-range weight fails with the Node message", func(t *testing.T) {
		selector := NewAPIKeyGroupRouteSelector("memory", nil, "")
		apiKey := &APIKeyRow{
			ID: "key1", RouteStrategyMode: RouteStrategyModeNormal,
			GroupBindings: []GroupBindingRow{
				{ID: "b1", GroupID: "grp_a", Status: RowStatusActive, GroupEnabled: 1},
				{ID: "b2", GroupID: "grp_b", Status: RowStatusActive, GroupEnabled: 1, Weight: weight(101)},
			},
		}
		_, err := selector.OrderAPIKeyGroupBindingsForDispatch(apiKey)
		if err == nil || err.Error() != "策略路由分组权重必须是 1-100 之间的整数" {
			t.Fatalf("err = %v, want 策略路由分组权重必须是 1-100 之间的整数", err)
		}
	})

	t.Run("round robin rotates through bindings", func(t *testing.T) {
		selector := NewAPIKeyGroupRouteSelector("memory", nil, "")
		apiKey := &APIKeyRow{
			ID: "key1", RouteStrategyID: "strategy1",
			RouteStrategyMode: RouteStrategyModeRoundRobin,
			GroupBindings: []GroupBindingRow{
				binding("b1", "grp_a", 1, weight(1)),
				binding("b2", "grp_b", 2, weight(1)),
				binding("b3", "grp_c", 3, weight(1)),
			},
		}
		first, err := selector.OrderAPIKeyGroupBindingsForDispatch(apiKey)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		second, err := selector.OrderAPIKeyGroupBindingsForDispatch(apiKey)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		third, err := selector.OrderAPIKeyGroupBindingsForDispatch(apiKey)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		fourth, err := selector.OrderAPIKeyGroupBindingsForDispatch(apiKey)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want1 := []string{"b1", "b2", "b3"}
		if !equalStrings(bindingIDs(first), want1) {
			t.Fatalf("first = %v", bindingIDs(first))
		}
		if !equalStrings(bindingIDs(second), []string{"b2", "b3", "b1"}) {
			t.Fatalf("second = %v", bindingIDs(second))
		}
		if !equalStrings(bindingIDs(third), []string{"b3", "b1", "b2"}) {
			t.Fatalf("third = %v", bindingIDs(third))
		}
		if !equalStrings(bindingIDs(fourth), want1) {
			t.Fatalf("fourth = %v (wrap-around)", bindingIDs(fourth))
		}
	})

	t.Run("round robin shares state across keys of one strategy", func(t *testing.T) {
		selector := NewAPIKeyGroupRouteSelector("memory", nil, "")
		apiKeyA := &APIKeyRow{
			ID: "keyA", RouteStrategyID: "strategy1",
			RouteStrategyMode: RouteStrategyModeRoundRobin,
			GroupBindings: []GroupBindingRow{
				binding("b1", "grp_a", 1, weight(1)),
				binding("b2", "grp_b", 2, weight(1)),
			},
		}
		apiKeyB := &APIKeyRow{ID: "keyB", RouteStrategyID: "strategy1",
			RouteStrategyMode: RouteStrategyModeRoundRobin,
			GroupBindings:     apiKeyA.GroupBindings,
		}
		if _, err := selector.OrderAPIKeyGroupBindingsForDispatch(apiKeyA); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		second, err := selector.OrderAPIKeyGroupBindingsForDispatch(apiKeyB)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !equalStrings(bindingIDs(second), []string{"b2", "b1"}) {
			t.Fatalf("second key rotation = %v, want [b2 b1] (shared strategy state)", bindingIDs(second))
		}
	})

	t.Run("weighted smooth ordering distributes by debt", func(t *testing.T) {
		selector := NewAPIKeyGroupRouteSelector("memory", nil, "")
		apiKey := &APIKeyRow{
			ID: "key1",
			RouteStrategyMode: RouteStrategyModeWeighted,
			GroupBindings: []GroupBindingRow{
				binding("heavy", "grp_a", 1, weight(3)),
				binding("light", "grp_b", 2, weight(1)),
			},
		}
		first, err := selector.OrderAPIKeyGroupBindingsForDispatch(apiKey)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !equalStrings(bindingIDs(first), []string{"heavy", "light"}) {
			t.Fatalf("first = %v, want [heavy light]", bindingIDs(first))
		}
		second, err := selector.OrderAPIKeyGroupBindingsForDispatch(apiKey)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// After round 1: heavy current 3-4=-1, light 1. Round 2: heavy -1+3=2, light 1+1=2 →
		// tie broken by binding order (heavy first) → heavy wins again.
		if !equalStrings(bindingIDs(second), []string{"heavy", "light"}) {
			t.Fatalf("second = %v, want [heavy light]", bindingIDs(second))
		}
		third, err := selector.OrderAPIKeyGroupBindingsForDispatch(apiKey)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// After round 2: heavy 2-4=-2, light 2. Round 3: light 2+1=3, heavy -2+3=1 → light wins.
		if !equalStrings(bindingIDs(third), []string{"light", "heavy"}) {
			t.Fatalf("third = %v, want [light heavy]", bindingIDs(third))
		}
	})

	t.Run("redis driver forbids sync dynamic ordering", func(t *testing.T) {
		selector := NewAPIKeyGroupRouteSelector("redis", &scriptedRedisCounter{}, "redis://example:6379/0")
		apiKey := &APIKeyRow{
			ID: "key1", RouteStrategyMode: RouteStrategyModeRoundRobin,
			GroupBindings: []GroupBindingRow{
				binding("b1", "grp_a", 1, weight(1)),
				binding("b2", "grp_b", 2, weight(1)),
			},
		}
		_, err := selector.OrderAPIKeyGroupBindingsForDispatch(apiKey)
		if err == nil || err.Error() != "高性能模式动态路由禁止使用本机同步状态，请调用 orderGatewayApiKeyGroupBindingsForDispatchAsync" {
			t.Fatalf("err = %v", err)
		}
		// Non-dynamic modes stay allowed.
		apiKey.RouteStrategyMode = RouteStrategyModeFailover
		if _, err := selector.OrderAPIKeyGroupBindingsForDispatch(apiKey); err != nil {
			t.Fatalf("unexpected error for failover mode: %v", err)
		}
	})

	t.Run("async redis round robin uses the shared counter", func(t *testing.T) {
		counter := &scriptedRedisCounter{values: []int64{1, 0}}
		selector := NewAPIKeyGroupRouteSelector("redis", counter, "redis://example:6379/0")
		apiKey := &APIKeyRow{
			ID: "key1", RouteStrategyID: "strategy1",
			RouteStrategyMode: RouteStrategyModeRoundRobin,
			GroupBindings: []GroupBindingRow{
				binding("b1", "grp_a", 1, weight(1)),
				binding("b2", "grp_b", 2, weight(1)),
				binding("b3", "grp_c", 3, weight(1)),
			},
		}
		first, err := selector.OrderAPIKeyGroupBindingsForDispatchAsync(context.Background(), apiKey)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !equalStrings(bindingIDs(first), []string{"b2", "b3", "b1"}) {
			t.Fatalf("first = %v, want rotation by counter 1", bindingIDs(first))
		}
		if len(counter.keys) != 1 {
			t.Fatalf("counter calls = %d, want 1", len(counter.keys))
		}
		wantKey := "juhe-ai:route-state:api-key-group:round-robin:" + base64URL("strategy1")
		if counter.keys[0] != wantKey {
			t.Fatalf("counter key = %q, want %q", counter.keys[0], wantKey)
		}
		if counter.moduli[0] != 3 {
			t.Fatalf("counter modulo = %d, want 3", counter.moduli[0])
		}
	})

	t.Run("async redis weighted picks by cumulative weight", func(t *testing.T) {
		counter := &scriptedRedisCounter{values: []int64{3}}
		selector := NewAPIKeyGroupRouteSelector("redis", counter, "redis://example:6379/0")
		apiKey := &APIKeyRow{
			ID: "key1",
			RouteStrategyMode: RouteStrategyModeWeighted,
			GroupBindings: []GroupBindingRow{
				binding("w3", "grp_a", 1, weight(3)),
				binding("w1", "grp_b", 2, weight(1)),
				binding("w2", "grp_c", 3, weight(2)),
			},
		}
		ordered, err := selector.OrderAPIKeyGroupBindingsForDispatchAsync(context.Background(), apiKey)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Cursor 3: w3 covers 0..2, w1 covers 3 → selected w1; remainder by weight desc.
		if !equalStrings(bindingIDs(ordered), []string{"w1", "w3", "w2"}) {
			t.Fatalf("ordered = %v, want [w1 w3 w2]", bindingIDs(ordered))
		}
		wantKey := "juhe-ai:route-state:api-key-group:weighted:" + base64URL("key1")
		if counter.keys[0] != wantKey {
			t.Fatalf("counter key = %q, want %q", counter.keys[0], wantKey)
		}
		if counter.moduli[0] != 6 {
			t.Fatalf("counter modulo = %d, want 6", counter.moduli[0])
		}
	})

	t.Run("async without state URL fails with the Node message", func(t *testing.T) {
		selector := NewAPIKeyGroupRouteSelector("redis", &scriptedRedisCounter{}, "")
		apiKey := &APIKeyRow{
			ID: "key1", RouteStrategyMode: RouteStrategyModeRoundRobin,
			GroupBindings: []GroupBindingRow{
				binding("b1", "grp_a", 1, weight(1)),
				binding("b2", "grp_b", 2, weight(1)),
			},
		}
		_, err := selector.OrderAPIKeyGroupBindingsForDispatchAsync(context.Background(), apiKey)
		if err == nil || err.Error() != "高性能模式动态路由需要 JUHE_AI_REDIS_STATE_URL" {
			t.Fatalf("err = %v", err)
		}
		// modulo <= 0 short-circuits before the URL check — exercised through
		// the weighted path with zero total weight.
		zeroWeight := &APIKeyRow{
			ID: "key1", RouteStrategyMode: RouteStrategyModeWeighted,
			GroupBindings: []GroupBindingRow{
				{ID: "b1", GroupID: "grp_a", Status: RowStatusActive, GroupEnabled: 1, Weight: nil},
				{ID: "b2", GroupID: "grp_b", Status: RowStatusActive, GroupEnabled: 1, Weight: nil},
			},
		}
		// Weights normalize to 1 each, so total is 2 — use the counter error
		// path instead for the invalid result contract.
		selector.Redis = &scriptedRedisCounter{err: errors.New("Redis 动态路由计数器返回值无效")}
		if _, err := selector.OrderAPIKeyGroupBindingsForDispatchAsync(context.Background(), zeroWeight); err == nil {
			t.Fatal("expected counter error to propagate")
		}
	})

	t.Run("single binding returns before dynamic-state assertions", func(t *testing.T) {
		selector := NewAPIKeyGroupRouteSelector("redis", nil, "")
		apiKey := &APIKeyRow{
			ID: "key1", RouteStrategyMode: RouteStrategyModeRoundRobin,
			GroupBindings: []GroupBindingRow{binding("b1", "grp_a", 1, weight(1))},
		}
		ordered, err := selector.OrderAPIKeyGroupBindingsForDispatch(apiKey)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !equalStrings(bindingIDs(ordered), []string{"b1"}) {
			t.Fatalf("ordered = %v", bindingIDs(ordered))
		}
	})
}

// ---------------------------------------------------------------------------
// Request view: model + endpoint family extraction
// ---------------------------------------------------------------------------

func TestRequestEndpointFamilyAndModel(t *testing.T) {
	tests := []struct {
		name   string
		view   RequestView
		wantModel string
		wantFamily string
	}{
		{
			name:       "openai chat path",
			view:       RequestView{Method: "POST", OriginalURL: "/v1/chat/completions?x=1", BodyModel: "gpt-4o"},
			wantModel:  "gpt-4o",
			wantFamily: EndpointFamilyChatCompletions,
		},
		{
			name:       "openai responses path",
			view:       RequestView{Method: "POST", OriginalURL: "/v1/responses", BodyModel: "gpt-4o"},
			wantModel:  "gpt-4o",
			wantFamily: EndpointFamilyResponses,
		},
		{
			name:       "anthropic messages path strips /v1",
			view:       RequestView{Method: "POST", OriginalURL: "/v1/messages", BodyModel: "claude"},
			wantModel:  "claude",
			wantFamily: EndpointFamilyMessages,
		},
		{
			name:       "anthropic messages path requires POST",
			view:       RequestView{Method: "GET", OriginalURL: "/v1/messages"},
			wantFamily: "",
		},
		{
			name:       "gemini generateContent path",
			view:       RequestView{Method: "POST", OriginalURL: "/v1beta/models/gemini-2.0:generateContent", BodyModel: "gemini"},
			wantModel:  "gemini-2.0",
			wantFamily: EndpointFamilyGenerateContent,
		},
		{
			name:       "gemini streamGenerateContent path",
			view:       RequestView{Method: "POST", OriginalURL: "/models/gemini-2.0:streamGenerateContent?alt=sse"},
			wantModel:  "gemini-2.0",
			wantFamily: EndpointFamilyStreamGenerate,
		},
		{
			name:       "gemini countTokens path",
			view:       RequestView{Method: "POST", OriginalURL: "/v1beta/models/gemini-2.0:countTokens"},
			wantModel:  "gemini-2.0",
			wantFamily: EndpointFamilyCountTokens,
		},
		{
			name:       "gemini embedContent path",
			view:       RequestView{Method: "POST", OriginalURL: "/models/gemini-2.0:embedContent"},
			wantModel:  "gemini-2.0",
			wantFamily: EndpointFamilyEmbedContent,
		},
		{
			name:       "gemini interactions path",
			view:       RequestView{Method: "POST", OriginalURL: "/v1beta/interactions/abc"},
			wantFamily: EndpointFamilyInteractions,
		},
		{
			name:       "gemini plain models path is rejected",
			view:       RequestView{Method: "POST", OriginalURL: "/v1beta/models"},
			wantFamily: "",
		},
		{
			name:       "override wins over path families",
			view:       RequestView{Method: "POST", OriginalURL: "/v1/chat/completions", EndpointFamilyOverride: EndpointFamilyMessages},
			wantFamily: EndpointFamilyMessages,
		},
		{
			name:       "unknown path has no family",
			view:       RequestView{Method: "POST", OriginalURL: "/v1/unknown"},
			wantFamily: "",
		},
		{
			name:       "percent-encoded gemini path model decodes",
			view:       RequestView{Method: "POST", OriginalURL: "/models/my%20model:generateContent"},
			wantModel:  "my model",
			wantFamily: EndpointFamilyGenerateContent,
		},
		{
			name:       "gemini path model wins over body model",
			view:       RequestView{Method: "POST", OriginalURL: "/models/gemini-2.0:generateContent", BodyModel: "body-model"},
			wantModel:  "gemini-2.0",
			wantFamily: EndpointFamilyGenerateContent,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.view.requestModel(); got != tt.wantModel {
				t.Fatalf("model = %q, want %q", got, tt.wantModel)
			}
			if got := tt.view.requestEndpointFamily(); got != tt.wantFamily {
				t.Fatalf("family = %q, want %q", got, tt.wantFamily)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// First-byte deadline + speed-first lane gating
// ---------------------------------------------------------------------------

func TestNormalRouteAttemptFirstByteDeadline(t *testing.T) {
	newBudget := func(t *testing.T, acceptedAt int64, budgetMs int64) *GatewayRequestWallBudget {
		t.Helper()
		budget, err := NewGatewayRequestWallBudget(GatewayRequestWallBudgetOptions{
			RequestAcceptedAtMs: acceptedAt,
			BudgetMs:            &budgetMs,
			Now:                 func() int64 { return acceptedAt },
		}, nil)
		if err != nil {
			t.Fatalf("NewGatewayRequestWallBudget: %v", err)
		}
		return budget
	}

	tests := []struct {
		name string
		budget *GatewayRequestWallBudget
		input NormalRouteAttemptFirstByteDeadlineInput
		want NormalRouteAttemptFirstByteDeadline
	}{
		{
			name:   "configured deadline is the limiting factor",
			budget: newBudget(t, 1_000, 270_000),
			input: NormalRouteAttemptFirstByteDeadlineInput{
				Config:                          NormalRouteFirstByteRuntimeConfig{SchedulingPreference: SchedulingPreferenceSpeedFirst, FirstByteDeadlineMs: 5_000},
				AttemptStartedAtMs:              1_000,
				LaneFirstByteTimeoutMs:          120_000,
				UncommittedAttemptMaxLifetimeMs: 300_000,
			},
			want: NormalRouteAttemptFirstByteDeadline{
				ConfiguredDeadlineMs: 5_000, EffectiveDeadlineMs: 5_000, DeadlineAtMs: 6_000,
				SchedulingPreference: SchedulingPreferenceSpeedFirst, Clipped: false,
				LimitingFactor: FirstByteLimitingFactorConfigured,
			},
		},
		{
			name:   "wall precommit reserve clips the deadline",
			budget: newBudget(t, 1_000, 10_000),
			input: NormalRouteAttemptFirstByteDeadlineInput{
				Config:                          NormalRouteFirstByteRuntimeConfig{SchedulingPreference: SchedulingPreferenceSpeedFirst, FirstByteDeadlineMs: 5_000},
				AttemptStartedAtMs:              8_000,
				LaneFirstByteTimeoutMs:          120_000,
				UncommittedAttemptMaxLifetimeMs: 300_000,
			},
			// precommitRemaining = min(11_000, 11_000) - 8_000 - 2_000 = 1_000
			want: NormalRouteAttemptFirstByteDeadline{
				ConfiguredDeadlineMs: 5_000, EffectiveDeadlineMs: 1_000, DeadlineAtMs: 9_000,
				SchedulingPreference: SchedulingPreferenceSpeedFirst, Clipped: true,
				LimitingFactor: FirstByteLimitingFactorWallPrecommit,
			},
		},
		{
			name:   "uncommitted attempt lifetime clips the deadline",
			budget: newBudget(t, 1_000, 270_000),
			input: NormalRouteAttemptFirstByteDeadlineInput{
				Config:                          NormalRouteFirstByteRuntimeConfig{SchedulingPreference: SchedulingPreferenceSpeedFirst, FirstByteDeadlineMs: 5_000},
				AttemptStartedAtMs:              1_000,
				LaneFirstByteTimeoutMs:          120_000,
				UncommittedAttemptMaxLifetimeMs: 2_000,
			},
			want: NormalRouteAttemptFirstByteDeadline{
				ConfiguredDeadlineMs: 5_000, EffectiveDeadlineMs: 2_000, DeadlineAtMs: 3_000,
				SchedulingPreference: SchedulingPreferenceSpeedFirst, Clipped: true,
				LimitingFactor: FirstByteLimitingFactorUncommittedAttempt,
			},
		},
		{
			name:   "lane timeout clips the deadline",
			budget: newBudget(t, 1_000, 270_000),
			input: NormalRouteAttemptFirstByteDeadlineInput{
				Config:                          NormalRouteFirstByteRuntimeConfig{SchedulingPreference: SchedulingPreferenceSpeedFirst, FirstByteDeadlineMs: 5_000},
				AttemptStartedAtMs:              1_000,
				LaneFirstByteTimeoutMs:          3_000,
				UncommittedAttemptMaxLifetimeMs: 300_000,
			},
			want: NormalRouteAttemptFirstByteDeadline{
				ConfiguredDeadlineMs: 5_000, EffectiveDeadlineMs: 3_000, DeadlineAtMs: 4_000,
				SchedulingPreference: SchedulingPreferenceSpeedFirst, Clipped: true,
				LimitingFactor: FirstByteLimitingFactorLaneTimeout,
			},
		},
		{
			name:   "deadline exactly on the boundary is not clipped",
			budget: newBudget(t, 0, 100_000),
			input: NormalRouteAttemptFirstByteDeadlineInput{
				Config:                          NormalRouteFirstByteRuntimeConfig{SchedulingPreference: SchedulingPreferenceSpeedFirst, FirstByteDeadlineMs: 4_000},
				AttemptStartedAtMs:              1_000,
				LaneFirstByteTimeoutMs:          4_000,
				UncommittedAttemptMaxLifetimeMs: 4_000,
			},
			want: NormalRouteAttemptFirstByteDeadline{
				ConfiguredDeadlineMs: 4_000, EffectiveDeadlineMs: 4_000, DeadlineAtMs: 5_000,
				SchedulingPreference: SchedulingPreferenceSpeedFirst, Clipped: false,
				LimitingFactor: FirstByteLimitingFactorConfigured,
			},
		},
		{
			name:   "non-positive configured deadline clamps to 1",
			budget: newBudget(t, 0, 270_000),
			input: NormalRouteAttemptFirstByteDeadlineInput{
				Config:                          NormalRouteFirstByteRuntimeConfig{SchedulingPreference: SchedulingPreferenceSpeedFirst, FirstByteDeadlineMs: 0},
				AttemptStartedAtMs:              0,
				LaneFirstByteTimeoutMs:          120_000,
				UncommittedAttemptMaxLifetimeMs: 300_000,
			},
			want: NormalRouteAttemptFirstByteDeadline{
				ConfiguredDeadlineMs: 1, EffectiveDeadlineMs: 1, DeadlineAtMs: 1,
				SchedulingPreference: SchedulingPreferenceSpeedFirst, Clipped: false,
				LimitingFactor: FirstByteLimitingFactorConfigured,
			},
		},
		{
			name:   "negative attempt start clamps to zero",
			budget: newBudget(t, 0, 270_000),
			input: NormalRouteAttemptFirstByteDeadlineInput{
				Config:                          NormalRouteFirstByteRuntimeConfig{SchedulingPreference: SchedulingPreferenceSpeedFirst, FirstByteDeadlineMs: 2_000},
				AttemptStartedAtMs:              -5,
				LaneFirstByteTimeoutMs:          120_000,
				UncommittedAttemptMaxLifetimeMs: 300_000,
			},
			want: NormalRouteAttemptFirstByteDeadline{
				ConfiguredDeadlineMs: 2_000, EffectiveDeadlineMs: 2_000, DeadlineAtMs: 2_000,
				SchedulingPreference: SchedulingPreferenceSpeedFirst, Clipped: false,
				LimitingFactor: FirstByteLimitingFactorConfigured,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.input.GatewayRequestWallBudget = tt.budget
			got := mustDeadline(t, tt.input)
			if got != tt.want {
				t.Fatalf("deadline = %+v, want %+v", got, tt.want)
			}
		})
	}

	t.Run("negative final response reserve fails like the Node RangeError", func(t *testing.T) {
		budget := newBudget(t, 0, 270_000)
		reserve := int64(-1)
		_, err := ResolveNormalRouteAttemptFirstByteDeadline(NormalRouteAttemptFirstByteDeadlineInput{
			Config:                 NormalRouteFirstByteRuntimeConfig{SchedulingPreference: SchedulingPreferenceSpeedFirst, FirstByteDeadlineMs: 1_000},
			GatewayRequestWallBudget: budget,
			AttemptStartedAtMs:     0,
			LaneFirstByteTimeoutMs: 1_000,
			UncommittedAttemptMaxLifetimeMs: 1_000,
			FinalResponseReserveMs: &reserve,
		})
		var rangeErr *RangeError
		if !errors.As(err, &rangeErr) || rangeErr.Message != "route coordination duration must be a non-negative finite number" {
			t.Fatalf("err = %v, want non-negative duration RangeError", err)
		}
	})
}

func TestNormalRouteSpeedFirstAppliesToLane(t *testing.T) {
	tests := []struct {
		lane             gatewayproto.RequestLane
		wantDeadlineGate bool
	}{
		{gatewayproto.LaneText, true},
		{gatewayproto.LaneImage, false},
	}
	for _, tt := range tests {
		t.Run(string(tt.lane), func(t *testing.T) {
			if got := NormalRouteFirstByteDeadlineAppliesToLane(tt.lane); got != tt.wantDeadlineGate {
				t.Fatalf("deadline gate = %v, want %v", got, tt.wantDeadlineGate)
			}
			if got := NormalRouteSpeedFirstAppliesToLane(tt.lane); got != tt.wantDeadlineGate {
				t.Fatalf("speed-first gate = %v, want %v", got, tt.wantDeadlineGate)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Timeout profile
// ---------------------------------------------------------------------------

func TestGatewayTimeoutProfileForLane(t *testing.T) {
	settings := GatewayTimeoutSettings{
		TextFirstResponseTimeoutSeconds:           120,
		TextStreamIdleTimeoutSeconds:              30,
		TextUncommittedAttemptMaxLifetimeSeconds:  240,
		ImageFirstResponseTimeoutSeconds:          600,
		ImageStreamIdleTimeoutSeconds:             60,
		ImageUncommittedAttemptMaxLifetimeSeconds: 900,
		NoAvailableAccountWaitTimeoutSeconds:      10,
	}
	tests := []struct {
		name            string
		lane            gatewayproto.RequestLane
		disableTimeouts bool
		want            GatewayTimeoutProfile
	}{
		{
			name: "text lane reads text settings",
			lane: gatewayproto.LaneText,
			want: GatewayTimeoutProfile{
				FirstResponseTimeoutMs: 120_000, FirstByteTimeoutMs: 120_000, IdleTimeoutMs: 30_000,
				UncommittedAttemptMaxLifetimeMs: 240_000, NoAvailableAccountWaitMs: 10_000,
			},
		},
		{
			name: "image lane reads image settings",
			lane: gatewayproto.LaneImage,
			want: GatewayTimeoutProfile{
				FirstResponseTimeoutMs: 600_000, FirstByteTimeoutMs: 600_000, IdleTimeoutMs: 60_000,
				UncommittedAttemptMaxLifetimeMs: 900_000, NoAvailableAccountWaitMs: 10_000,
			},
		},
		{
			name:            "disabled timeouts flag mirrors timeoutsDisabled: true",
			lane:            gatewayproto.LaneText,
			disableTimeouts: true,
			want: GatewayTimeoutProfile{
				TimeoutsDisabled: true,
				FirstResponseTimeoutMs: 120_000, FirstByteTimeoutMs: 120_000, IdleTimeoutMs: 30_000,
				UncommittedAttemptMaxLifetimeMs: 240_000, NoAvailableAccountWaitMs: 10_000,
			},
		},
		{
			name: "sub-second settings clamp to one second",
			lane: gatewayproto.LaneText,
			want: GatewayTimeoutProfile{
				FirstResponseTimeoutMs: 1_000, FirstByteTimeoutMs: 1_000, IdleTimeoutMs: 1_000,
				UncommittedAttemptMaxLifetimeMs: 1_000, NoAvailableAccountWaitMs: 1_000,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			current := settings
			if tt.name == "sub-second settings clamp to one second" {
				current = GatewayTimeoutSettings{}
			}
			got := GatewayTimeoutProfileForLane(current, tt.lane, tt.disableTimeouts)
			if got != tt.want {
				t.Fatalf("profile = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Route coordination: wall budget
// ---------------------------------------------------------------------------

func TestGatewayRequestWallBudget(t *testing.T) {
	t.Run("default budget is 270s and deadline math is exact", func(t *testing.T) {
		budget, err := NewGatewayRequestWallBudget(GatewayRequestWallBudgetOptions{RequestAcceptedAtMs: 1_000}, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if budget.BudgetMs != DefaultGatewayRequestWallBudgetMs {
			t.Fatalf("budget = %d, want %d", budget.BudgetMs, DefaultGatewayRequestWallBudgetMs)
		}
		if budget.DeadlineAtMs != 271_000 {
			t.Fatalf("deadline = %d, want 271000", budget.DeadlineAtMs)
		}
		if got := budget.ElapsedMs(3_000); got != 2_000 {
			t.Fatalf("elapsed = %d, want 2000", got)
		}
		if got := budget.ElapsedMs(500); got != 0 {
			t.Fatalf("elapsed before start = %d, want 0", got)
		}
	})

	t.Run("invalid budget fails with the Node RangeError message", func(t *testing.T) {
		_, err := NewGatewayRequestWallBudget(GatewayRequestWallBudgetOptions{RequestAcceptedAtMs: 0, BudgetMs: int64Ptr(0)}, nil)
		var rangeErr *RangeError
		if !errors.As(err, &rangeErr) || rangeErr.Message != "route coordination duration must be a positive finite number" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("withMinimumBudgetMs only raises the budget", func(t *testing.T) {
		base := int64(10_000)
		budget, _ := NewGatewayRequestWallBudget(GatewayRequestWallBudgetOptions{RequestAcceptedAtMs: 0, BudgetMs: &base}, nil)
		lowered, err := budget.WithMinimumBudgetMs(5_000)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if lowered != budget {
			t.Fatal("lower minimum must return the same budget")
		}
		raised, err := budget.WithMinimumBudgetMs(20_000)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if raised == budget || raised.BudgetMs != 20_000 {
			t.Fatalf("raised budget = %+v", raised)
		}
		// Node's normalizedPositiveMs throws for non-positive values (the
		// fallback only applies to undefined).
		_, err = budget.WithMinimumBudgetMs(0)
		var rangeErr *RangeError
		if !errors.As(err, &rangeErr) || rangeErr.Message != "route coordination duration must be a positive finite number" {
			t.Fatalf("non-positive minimum err = %v, want positive-duration RangeError", err)
		}
	})

	t.Run("withoutLimit removes the ceiling", func(t *testing.T) {
		base := int64(10_000)
		budget, _ := NewGatewayRequestWallBudget(GatewayRequestWallBudgetOptions{RequestAcceptedAtMs: 1_000, BudgetMs: &base}, nil)
		unbounded := budget.WithoutLimit()
		if unbounded == budget || !unbounded.Unbounded {
			t.Fatal("expected a new unbounded budget")
		}
		if again := unbounded.WithoutLimit(); again != unbounded {
			t.Fatal("withoutLimit is idempotent")
		}
		if got := unbounded.RemainingMs(5_000); got != mathMaxInt64() {
			t.Fatalf("unbounded remaining = %d, want MaxInt64 sentinel", got)
		}
		handoff, _ := unbounded.HandoffRequired(GatewayRequestWallBudgetDecision{NowMs: int64Ptr(5_000), MinimumMeaningfulAttemptMs: int64Ptr(1_000)})
		if handoff {
			t.Fatal("unbounded budgets never require handoff")
		}
	})

	t.Run("precommit remaining honours precommit deadline and reserve", func(t *testing.T) {
		base := int64(100_000)
		budget, _ := NewGatewayRequestWallBudget(GatewayRequestWallBudgetOptions{RequestAcceptedAtMs: 0, BudgetMs: &base}, nil)
		remaining, err := budget.PrecommitRemainingMs(PrecommitBudgetInput{NowMs: int64Ptr(10_000)})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// 100_000 - 10_000 - 2_000 (default reserve) = 88_000
		if remaining != 88_000 {
			t.Fatalf("remaining = %d, want 88000", remaining)
		}
		remaining, err = budget.PrecommitRemainingMs(PrecommitBudgetInput{
			NowMs: int64Ptr(10_000), RequestPrecommitDeadlineAtMs: int64Ptr(20_000), FinalResponseReserveMs: int64Ptr(5_000),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// min(100_000, 20_000) - 10_000 - 5_000 = 5_000
		if remaining != 5_000 {
			t.Fatalf("remaining = %d, want 5000", remaining)
		}
		if remaining, _ := budget.PrecommitRemainingMs(PrecommitBudgetInput{NowMs: int64Ptr(200_000)}); remaining != 0 {
			t.Fatalf("past-deadline remaining = %d, want 0", remaining)
		}
	})

	t.Run("clip uses the smallest candidate and reports clipping to the observer", func(t *testing.T) {
		observer := &recordingObserver{}
		base := int64(100_000)
		budget, _ := NewGatewayRequestWallBudget(GatewayRequestWallBudgetOptions{RequestAcceptedAtMs: 0, BudgetMs: &base, Now: func() int64 { return 0 }}, observer)
		clipped, err := budget.ClipFirstByteDeadlineMs(FirstByteDeadlineClipInput{
			NowMs:                          int64Ptr(0),
			FirstByteDeadlineMs:            50_000,
			UncommittedAttemptDeadlineAtMs: int64Ptr(30_000),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// candidates: 50_000 configured, 98_000 precommit, 30_000 uncommitted → 30_000
		if clipped != 30_000 {
			t.Fatalf("clipped = %d, want 30000", clipped)
		}
		if len(observer.kinds) != 1 || observer.kinds[0] != "budget" || observer.outcomes[0] != "precommit_clipped" || observer.nows[0] != 0 {
			t.Fatalf("observer = %+v, want one budget/precommit_clipped event at now 0", observer)
		}
		unchanged, err := budget.ClipFirstByteDeadlineMs(FirstByteDeadlineClipInput{NowMs: int64Ptr(0), FirstByteDeadlineMs: 50_000})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if unchanged != 50_000 {
			t.Fatalf("unchanged clip = %d, want 50000", unchanged)
		}
		if len(observer.kinds) != 1 {
			t.Fatalf("observer must not fire when nothing was clipped: %+v", observer)
		}
	})

	t.Run("unbounded clip returns the configured deadline untouched", func(t *testing.T) {
		budget, _ := NewGatewayRequestWallBudget(GatewayRequestWallBudgetOptions{RequestAcceptedAtMs: 0, Unbounded: true}, nil)
		clipped, err := budget.ClipFirstByteDeadlineMs(FirstByteDeadlineClipInput{FirstByteDeadlineMs: 5_000})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if clipped != 5_000 {
			t.Fatalf("clipped = %d, want 5000", clipped)
		}
	})

	t.Run("handoff required crosses the meaningful-attempt boundary exactly", func(t *testing.T) {
		base := int64(3_000)
		budget, _ := NewGatewayRequestWallBudget(GatewayRequestWallBudgetOptions{RequestAcceptedAtMs: 0, BudgetMs: &base}, nil)
		decision := GatewayRequestWallBudgetDecision{NowMs: int64Ptr(0), MinimumMeaningfulAttemptMs: int64Ptr(1_000)}
		required, err := budget.HandoffRequired(decision)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// available = 3_000 - 2_000 reserve = 1_000 <= 1_000 → handoff
		if !required {
			t.Fatal("expected handoff at the exact boundary")
		}
		base2 := int64(3_001)
		budget2, _ := NewGatewayRequestWallBudget(GatewayRequestWallBudgetOptions{RequestAcceptedAtMs: 0, BudgetMs: &base2}, nil)
		required, err = budget2.HandoffRequired(decision)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if required {
			t.Fatal("expected no handoff just above the boundary")
		}
	})
}

// ---------------------------------------------------------------------------
// Route coordination: coordination budget
// ---------------------------------------------------------------------------

func TestRouteCoordinationBudget(t *testing.T) {
	newBudget := func(t *testing.T, now func() int64) *RouteCoordinationBudget {
		t.Helper()
		budget, err := NewRouteCoordinationBudget(RouteCoordinationBudgetOptions{RequestID: "req1", Now: now})
		if err != nil {
			t.Fatalf("NewRouteCoordinationBudget: %v", err)
		}
		return budget
	}

	t.Run("default budget is 3s and drains while waiting", func(t *testing.T) {
		tick := int64(1_000)
		budget := newBudget(t, func() int64 { return tick })
		if budget.BudgetMs != DefaultRouteCoordinationBudgetMs {
			t.Fatalf("budget = %d, want %d", budget.BudgetMs, DefaultRouteCoordinationBudgetMs)
		}
		result, err := budget.BeginWait(RouteCoordinationBudgetTransitionInput{WaitToken: "w1", ExpectedVersion: 0, NowMs: int64Ptr(1_000)})
		if err != nil || result.Outcome != BudgetTransitionApplied {
			t.Fatalf("beginWait = %+v err %v", result, err)
		}
		tick = 4_000
		if got := budget.RemainingMs(tick); got != 0 {
			t.Fatalf("remaining after full drain = %d, want 0", got)
		}
		if !budget.Exhausted(tick) {
			t.Fatal("budget must be exhausted after 3s of waiting")
		}
		tick = 2_500
		if got := budget.RemainingMs(tick); got != 1_500 {
			t.Fatalf("remaining mid-wait = %d, want 1500", got)
		}
	})

	t.Run("begin and pause transitions with version conflicts and replays", func(t *testing.T) {
		tick := int64(0)
		budget := newBudget(t, func() int64 { return tick })

		applied, err := budget.BeginWait(RouteCoordinationBudgetTransitionInput{WaitToken: "w1", ExpectedVersion: 0, NowMs: int64Ptr(0)})
		if err != nil || applied.Outcome != BudgetTransitionApplied || applied.Snapshot.Version != 1 {
			t.Fatalf("begin applied = %+v err %v", applied, err)
		}
		replay, err := budget.BeginWait(RouteCoordinationBudgetTransitionInput{WaitToken: "w1", ExpectedVersion: 1, NowMs: int64Ptr(100)})
		if err != nil || replay.Outcome != BudgetTransitionIdempotentReplay {
			t.Fatalf("begin replay = %+v err %v", replay, err)
		}
		conflict, err := budget.BeginWait(RouteCoordinationBudgetTransitionInput{WaitToken: "w2", ExpectedVersion: 0, NowMs: int64Ptr(100)})
		if err != nil || conflict.Outcome != BudgetTransitionVersionConflict {
			t.Fatalf("begin conflict = %+v err %v", conflict, err)
		}
		invalid, err := budget.BeginWait(RouteCoordinationBudgetTransitionInput{WaitToken: "w2", ExpectedVersion: 1, NowMs: int64Ptr(100)})
		if err != nil || invalid.Outcome != BudgetTransitionInvalid {
			t.Fatalf("begin while active = %+v err %v", invalid, err)
		}

		paused, err := budget.PauseWait(RouteCoordinationBudgetTransitionInput{WaitToken: "w1", ExpectedVersion: 1, NowMs: int64Ptr(500)})
		if err != nil || paused.Outcome != BudgetTransitionApplied || paused.Snapshot.Version != 2 {
			t.Fatalf("pause applied = %+v err %v", paused, err)
		}
		if paused.Snapshot.RemainingMs != DefaultRouteCoordinationBudgetMs-500 {
			t.Fatalf("paused remaining = %d, want %d", paused.Snapshot.RemainingMs, DefaultRouteCoordinationBudgetMs-500)
		}
		pauseReplay, err := budget.PauseWait(RouteCoordinationBudgetTransitionInput{WaitToken: "w1", ExpectedVersion: 2, NowMs: int64Ptr(500)})
		if err != nil || pauseReplay.Outcome != BudgetTransitionIdempotentReplay {
			t.Fatalf("pause replay = %+v err %v", pauseReplay, err)
		}
		invalidPause, err := budget.PauseWait(RouteCoordinationBudgetTransitionInput{WaitToken: "w2", ExpectedVersion: 2, NowMs: int64Ptr(500)})
		if err != nil || invalidPause.Outcome != BudgetTransitionInvalid {
			t.Fatalf("pause without active wait = %+v err %v", invalidPause, err)
		}

		// After pausing, the stored remaining is used while inactive.
		tick = 10_000
		if got := budget.RemainingMs(tick); got != DefaultRouteCoordinationBudgetMs-500 {
			t.Fatalf("inactive remaining = %d, want stored %d", got, DefaultRouteCoordinationBudgetMs-500)
		}
		// And the budget resumes from the stored value.
		resumed, err := budget.BeginWait(RouteCoordinationBudgetTransitionInput{WaitToken: "w2", ExpectedVersion: 2, NowMs: int64Ptr(10_000)})
		if err != nil || resumed.Outcome != BudgetTransitionApplied {
			t.Fatalf("resume = %+v err %v", resumed, err)
		}
	})

	t.Run("negative versions fail like the Node RangeError", func(t *testing.T) {
		budget := newBudget(t, func() int64 { return 0 })
		_, err := budget.BeginWait(RouteCoordinationBudgetTransitionInput{WaitToken: "w1", ExpectedVersion: -1})
		var rangeErr *RangeError
		if !errors.As(err, &rangeErr) || rangeErr.Message != "route coordination version must be a non-negative integer" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("empty tokens and request ids fail like the Node TypeError", func(t *testing.T) {
		if _, err := NewRouteCoordinationBudget(RouteCoordinationBudgetOptions{RequestID: "  "}); err == nil || err.Error() != "route coordination key must not be empty" {
			t.Fatalf("request id err = %v", err)
		}
		budget := newBudget(t, func() int64 { return 0 })
		if _, err := budget.BeginWait(RouteCoordinationBudgetTransitionInput{WaitToken: " "}); err == nil || err.Error() != "route coordination key must not be empty" {
			t.Fatalf("wait token err = %v", err)
		}
	})

	t.Run("custom budget id and budget size", func(t *testing.T) {
		budgetMs := int64(1_500)
		budget, err := NewRouteCoordinationBudget(RouteCoordinationBudgetOptions{
			RequestID: "req1", BudgetID: " custom-budget ", BudgetMs: &budgetMs,
			Now: func() int64 { return 0 },
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if budget.BudgetID != "custom-budget" || budget.BudgetMs != 1_500 {
			t.Fatalf("budget = %+v", budget)
		}
	})
}

// ---------------------------------------------------------------------------
// Route coordination: attempt tracker
// ---------------------------------------------------------------------------

func TestGatewayRequestAttemptTracker(t *testing.T) {
	identity := func() GatewayDispatchAttemptIdentity {
		return GatewayDispatchAttemptIdentity{
			ProtocolModelKey:      `[\"acc1\",\"openai\",\"v1\",\"gpt-4o\"]`,
			AccountRuntimeKey:     "acc1",
			PhysicalCredentialKey: "cred1",
		}
	}
	// Fix the protocol model key to the JSON the Go helper builds.
	protoKey, err := GatewayAttemptProtocolModelKey("acc1", "openai", "v1", "gpt-4o")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	t.Run("first attempt allowed, repeats rejected", func(t *testing.T) {
		tracker, err := NewGatewayRequestAttemptTracker(nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		id := identity()
		id.ProtocolModelKey = protoKey
		registration, err := tracker.TryRecordDispatchAttempt(GatewayDispatchAttemptRecordInput{GatewayDispatchAttemptIdentity: id})
		if err != nil || !registration.Allowed {
			t.Fatalf("first attempt = %+v err %v", registration, err)
		}
		registration, err = tracker.TryRecordDispatchAttempt(GatewayDispatchAttemptRecordInput{GatewayDispatchAttemptIdentity: id})
		if err != nil || registration.Allowed || registration.Reason != RejectPhysicalCredentialAlreadyTried {
			t.Fatalf("repeat attempt = %+v err %v (Node checks the physical credential first)", registration, err)
		}
		// A different runtime key on a fresh credential hits the runtime guard.
		otherKey, _ := GatewayAttemptProtocolModelKey("acc9", "openai", "v1", "gpt-4o-mini")
		otherRuntime := GatewayDispatchAttemptIdentity{
			ProtocolModelKey:      otherKey,
			AccountRuntimeKey:     "acc9",
			PhysicalCredentialKey: "cred9",
		}
		if _, err := tracker.TryRecordDispatchAttempt(GatewayDispatchAttemptRecordInput{GatewayDispatchAttemptIdentity: otherRuntime}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		repeatOther := otherRuntime
		repeatOther.PhysicalCredentialKey = "cred10"
		registration, err = tracker.TryRecordDispatchAttempt(GatewayDispatchAttemptRecordInput{GatewayDispatchAttemptIdentity: repeatOther})
		if err != nil || registration.Allowed || registration.Reason != RejectAccountRuntimeAlreadyAttempted {
			t.Fatalf("repeat runtime = %+v err %v", registration, err)
		}
	})

	t.Run("different accounts sharing one credential are rejected", func(t *testing.T) {
		tracker, _ := NewGatewayRequestAttemptTracker(nil)
		first := identity()
		first.ProtocolModelKey = protoKey
		if _, err := tracker.TryRecordDispatchAttempt(GatewayDispatchAttemptRecordInput{GatewayDispatchAttemptIdentity: first}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		second := GatewayDispatchAttemptIdentity{
			ProtocolModelKey:      protoKey,
			AccountRuntimeKey:     "acc2",
			PhysicalCredentialKey: "cred1",
		}
		registration, err := tracker.TryRecordDispatchAttempt(GatewayDispatchAttemptRecordInput{GatewayDispatchAttemptIdentity: second})
		if err != nil || registration.Allowed || registration.Reason != RejectPhysicalCredentialAlreadyTried {
			t.Fatalf("shared credential = %+v err %v", registration, err)
		}
		// Same runtime account may rotate its API key fingerprint (key rotation).
		rotation := GatewayDispatchAttemptIdentity{
			ProtocolModelKey:      protoKey,
			AccountRuntimeKey:     "acc1",
			PhysicalCredentialKey: "cred1",
			KeyFingerprint:        "fp2",
		}
		if _, err := tracker.RecordKeyFingerprint("fp1"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		registration, err = tracker.TryRecordDispatchAttempt(GatewayDispatchAttemptRecordInput{
			GatewayDispatchAttemptIdentity: rotation,
			AllowKeyRotation:               true,
		})
		if err != nil || !registration.Allowed {
			t.Fatalf("key rotation = %+v err %v", registration, err)
		}
	})

	t.Run("protocol model and fingerprint dedupe for plain attempts", func(t *testing.T) {
		tracker, _ := NewGatewayRequestAttemptTracker(nil)
		first := GatewayDispatchAttemptIdentity{
			ProtocolModelKey:      protoKey,
			AccountRuntimeKey:     "acc1",
			PhysicalCredentialKey: "cred1",
			KeyFingerprint:        "fp1",
		}
		if _, err := tracker.TryRecordDispatchAttempt(GatewayDispatchAttemptRecordInput{GatewayDispatchAttemptIdentity: first}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		sameModelOtherAccount := GatewayDispatchAttemptIdentity{
			ProtocolModelKey:      protoKey,
			AccountRuntimeKey:     "acc2",
			PhysicalCredentialKey: "cred2",
		}
		registration, err := tracker.TryRecordDispatchAttempt(GatewayDispatchAttemptRecordInput{GatewayDispatchAttemptIdentity: sameModelOtherAccount})
		if err != nil || registration.Allowed || registration.Reason != RejectProtocolModelAlreadyAttempted {
			t.Fatalf("same protocol model = %+v err %v", registration, err)
		}
		otherModel := GatewayDispatchAttemptIdentity{
			ProtocolModelKey:      `[\"acc3\",\"openai\",\"v1\",\"gpt-4o-mini\"]`,
			AccountRuntimeKey:     "acc3",
			PhysicalCredentialKey: "cred3",
			KeyFingerprint:        "fp1",
		}
		otherKey, err := GatewayAttemptProtocolModelKey("acc3", "openai", "v1", "gpt-4o-mini")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		otherModel.ProtocolModelKey = otherKey
		registration, err = tracker.TryRecordDispatchAttempt(GatewayDispatchAttemptRecordInput{GatewayDispatchAttemptIdentity: otherModel})
		if err != nil || registration.Allowed || registration.Reason != RejectKeyFingerprintAlreadyAttempted {
			t.Fatalf("same fingerprint = %+v err %v", registration, err)
		}
	})

	t.Run("confirmation attempts pin the physical credential", func(t *testing.T) {
		tracker, _ := NewGatewayRequestAttemptTracker(nil)
		key, err := GatewayAttemptProtocolModelKey("acc1", "openai", "v1", "gpt-4o")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		base := GatewayDispatchAttemptIdentity{ProtocolModelKey: key, AccountRuntimeKey: "acc1", PhysicalCredentialKey: "cred1"}
		registration, err := tracker.TryRecordDispatchAttempt(GatewayDispatchAttemptRecordInput{
			GatewayDispatchAttemptIdentity: base, MatchingConfirmation: true,
		})
		if err != nil || !registration.Allowed {
			t.Fatalf("confirmation = %+v err %v", registration, err)
		}
		registration, err = tracker.TryRecordDispatchAttempt(GatewayDispatchAttemptRecordInput{
			GatewayDispatchAttemptIdentity: base, MatchingConfirmation: true,
		})
		if err != nil || registration.Allowed || registration.Reason != RejectConfirmationAlreadyAttempted {
			t.Fatalf("repeat confirmation = %+v err %v", registration, err)
		}
		rotated := base
		rotated.KeyFingerprint = "fp2"
		registration, err = tracker.TryRecordDispatchAttempt(GatewayDispatchAttemptRecordInput{
			GatewayDispatchAttemptIdentity: rotated, MatchingConfirmation: true, AllowKeyRotation: true,
		})
		// Node allows the rotation: the pinned credential matches, so the
		// confirmation_already_attempted decision falls through to canRotateKey
		// with a fresh fingerprint.
		if err != nil || !registration.Allowed {
			t.Fatalf("confirmation key rotation = %+v err %v", registration, err)
		}
		pinnedDifferent := base
		pinnedDifferent.PhysicalCredentialKey = "cred-other"
		registration, err = tracker.TryRecordDispatchAttempt(GatewayDispatchAttemptRecordInput{
			GatewayDispatchAttemptIdentity: pinnedDifferent, MatchingConfirmation: true,
		})
		if err != nil || registration.Allowed || registration.Reason != RejectPhysicalCredentialAlreadyTried {
			t.Fatalf("confirmation with a different pinned credential = %+v err %v", registration, err)
		}
	})

	t.Run("same-account retry reservation lifecycle", func(t *testing.T) {
		tracker, _ := NewGatewayRequestAttemptTracker(nil)
		key, err := GatewayAttemptProtocolModelKey("acc1", "openai", "v1", "gpt-4o")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		identityFull := GatewayDispatchAttemptIdentity{
			ProtocolModelKey: key, AccountRuntimeKey: "acc1", PhysicalCredentialKey: "cred1", KeyFingerprint: "fp1",
		}
		if _, err := tracker.TryRecordDispatchAttempt(GatewayDispatchAttemptRecordInput{GatewayDispatchAttemptIdentity: identityFull}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		reservation, err := tracker.TryReserveSameAccountRetry(GatewaySameAccountRetryReservationInput{
			GatewayDispatchAttemptIdentity: identityFull,
			MaxRetries:                     1,
		})
		if err != nil || !reservation.Reserved || reservation.Remaining != 0 || reservation.RetryNumber != 1 {
			t.Fatalf("reservation = %+v err %v", reservation, err)
		}
		if reservation.RetryID == "" || !stringsHasPrefix(reservation.RetryID, "same-account-retry:") {
			t.Fatalf("retry id = %q", reservation.RetryID)
		}
		exhausted, err := tracker.TryReserveSameAccountRetry(GatewaySameAccountRetryReservationInput{
			GatewayDispatchAttemptIdentity: identityFull,
			MaxRetries:                     1,
		})
		if err != nil || exhausted.Reserved || exhausted.Reason != SameAccountRetryBudgetExhausted || exhausted.Remaining != 0 {
			t.Fatalf("exhausted reservation = %+v err %v", exhausted, err)
		}

		// Consuming the reservation: identity mismatch first.
		mismatch := identityFull
		mismatch.KeyFingerprint = "fp-other"
		registration, err := tracker.TryRecordDispatchAttempt(GatewayDispatchAttemptRecordInput{
			GatewayDispatchAttemptIdentity: mismatch, SameAccountRetryID: reservation.RetryID,
		})
		if err != nil || registration.Allowed || registration.Reason != RejectSameAccountRetryIdentityMismatch {
			t.Fatalf("mismatch = %+v err %v", registration, err)
		}
		// Mode conflicts are rejected outright.
		registration, err = tracker.TryRecordDispatchAttempt(GatewayDispatchAttemptRecordInput{
			GatewayDispatchAttemptIdentity: identityFull, SameAccountRetryID: reservation.RetryID, AllowKeyRotation: true,
		})
		if err != nil || registration.Allowed || registration.Reason != RejectSameAccountRetryModeConflict {
			t.Fatalf("mode conflict = %+v err %v", registration, err)
		}
		// Unknown retry ids are not registered.
		registration, err = tracker.TryRecordDispatchAttempt(GatewayDispatchAttemptRecordInput{
			GatewayDispatchAttemptIdentity: identityFull, SameAccountRetryID: "same-account-retry:missing",
		})
		if err != nil || registration.Allowed || registration.Reason != RejectSameAccountRetryNotRegistered {
			t.Fatalf("unregistered = %+v err %v", registration, err)
		}
		// Successful consumption; replay is rejected. Same-account retries do
		// not record the attempt keys again.
		registration, err = tracker.TryRecordDispatchAttempt(GatewayDispatchAttemptRecordInput{
			GatewayDispatchAttemptIdentity: identityFull, SameAccountRetryID: reservation.RetryID,
		})
		if err != nil || !registration.Allowed {
			t.Fatalf("consume = %+v err %v", registration, err)
		}
		registration, err = tracker.TryRecordDispatchAttempt(GatewayDispatchAttemptRecordInput{
			GatewayDispatchAttemptIdentity: identityFull, SameAccountRetryID: reservation.RetryID,
		})
		if err != nil || registration.Allowed || registration.Reason != RejectSameAccountRetryAlreadyAttempted {
			t.Fatalf("replay = %+v err %v", registration, err)
		}
	})

	t.Run("maxRetries range and empty keys fail like the Node errors", func(t *testing.T) {
		tracker, _ := NewGatewayRequestAttemptTracker(nil)
		_, err := tracker.TryReserveSameAccountRetry(GatewaySameAccountRetryReservationInput{
			GatewayDispatchAttemptIdentity: GatewayDispatchAttemptIdentity{
				ProtocolModelKey: "p", AccountRuntimeKey: "a", PhysicalCredentialKey: "c",
			},
			MaxRetries: 11,
		})
		var rangeErr *RangeError
		if !errors.As(err, &rangeErr) || rangeErr.Message != "same-account retry maxRetries must be an integer between 0 and 10" {
			t.Fatalf("err = %v", err)
		}
		if _, err := tracker.RecordAccountRuntimeKey("   "); err == nil || err.Error() != "route coordination key must not be empty" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("snapshot preserves insertion order and can seed a new tracker", func(t *testing.T) {
		tracker, _ := NewGatewayRequestAttemptTracker(nil)
		if _, err := tracker.RecordAccountRuntimeKey("acc2"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, err := tracker.RecordAccountRuntimeKey("acc1"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, err := tracker.RecordProtocolModelKey("pm1"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, err := tracker.RecordPhysicalCredentialKey("cred1"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, err := tracker.RecordKeyFingerprint("fp1"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		snapshot := tracker.Snapshot()
		if !equalStrings(snapshot.AttemptedAccountRuntimeKeys, []string{"acc2", "acc1"}) {
			t.Fatalf("account keys = %v", snapshot.AttemptedAccountRuntimeKeys)
		}
		seeded, err := NewGatewayRequestAttemptTracker(&snapshot)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if has, _ := seeded.HasAccountRuntimeKey("acc1"); !has {
			t.Fatal("seeded tracker lost account keys")
		}
		if has, _ := seeded.HasProtocolModelKey("pm1"); !has {
			t.Fatal("seeded tracker lost protocol model keys")
		}
	})
}

func TestGatewayAttemptProtocolModelKey(t *testing.T) {
	tests := []struct {
		name      string
		account   string
		protocol  string
		version   string
		model     string
		want      string
		wantError bool
	}{
		{
			name: "full identity", account: "acc1", protocol: "openai", version: "v1", model: "gpt-4o",
			want: `["acc1","openai","v1","gpt-4o"]`,
		},
		{
			name: "missing parts fall back to unknown markers", account: "acc1",
			want: `["acc1","unknown_protocol","unknown_version","unknown_model"]`,
		},
		{
			name: "values are trimmed", account: " acc1 ", protocol: " openai ", version: " v1 ", model: " gpt-4o ",
			want: `["acc1","openai","v1","gpt-4o"]`,
		},
		{
			name:      "empty account fails", account: "  ",
			wantError: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := GatewayAttemptProtocolModelKey(tt.account, tt.protocol, tt.version, tt.model)
			if tt.wantError {
				if err == nil || err.Error() != "route coordination key must not be empty" {
					t.Fatalf("err = %v, want empty-key TypeError", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("key = %q, want %q", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Route plan snapshots
// ---------------------------------------------------------------------------

func TestCreateGatewayRoutePlanSnapshot(t *testing.T) {
	t.Run("defaults and clamping", func(t *testing.T) {
		snapshot, err := CreateGatewayRoutePlanSnapshot(CreateGatewayRoutePlanSnapshotInput[string]{
			RoutePlanID:         " plan-1 ",
			Mode:                RouteStrategyModeFailover,
			RequestAcceptedAtMs: 1_000,
			OrderedAllowedTargets: []string{"a", "b", "c"},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if snapshot.RoutePlanID != "plan-1" {
			t.Fatalf("route plan id = %q", snapshot.RoutePlanID)
		}
		if snapshot.GatewayRequestWallBudgetMs != DefaultGatewayRequestWallBudgetMs {
			t.Fatalf("budget = %d, want default", snapshot.GatewayRequestWallBudgetMs)
		}
		if snapshot.GatewayRequestWallDeadlineAtMs != 1_000+DefaultGatewayRequestWallBudgetMs {
			t.Fatalf("deadline = %d", snapshot.GatewayRequestWallDeadlineAtMs)
		}
		if snapshot.RequestPrecommitDeadlineAtMs != snapshot.GatewayRequestWallDeadlineAtMs {
			t.Fatalf("precommit = %d, want wall deadline", snapshot.RequestPrecommitDeadlineAtMs)
		}
		if snapshot.FinalResponseReserveMs != DefaultGatewayFinalResponseReserveMs {
			t.Fatalf("reserve = %d, want default", snapshot.FinalResponseReserveMs)
		}
		if snapshot.Cursor != 0 {
			t.Fatalf("cursor = %d, want 0", snapshot.Cursor)
		}
	})

	t.Run("precommit deadline clamps to the wall deadline", func(t *testing.T) {
		snapshot, err := CreateGatewayRoutePlanSnapshot(CreateGatewayRoutePlanSnapshotInput[string]{
			RoutePlanID:                  "plan-1",
			Mode:                         RouteStrategyModeFailover,
			RequestAcceptedAtMs:          1_000,
			RequestPrecommitDeadlineAtMs: int64Ptr(9_999_999),
			OrderedAllowedTargets:        []string{"a"},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if snapshot.RequestPrecommitDeadlineAtMs != snapshot.GatewayRequestWallDeadlineAtMs {
			t.Fatalf("precommit = %d, want clamped wall deadline", snapshot.RequestPrecommitDeadlineAtMs)
		}
	})

	t.Run("empty targets and bad cursors fail like the Node RangeErrors", func(t *testing.T) {
		_, err := CreateGatewayRoutePlanSnapshot(CreateGatewayRoutePlanSnapshotInput[string]{
			RoutePlanID: "plan-1", Mode: RouteStrategyModeFailover,
			OrderedAllowedTargets: []string{},
		})
		if err == nil || err.Error() != "route plan orderedAllowedTargets must not be empty" {
			t.Fatalf("err = %v", err)
		}
		cursor := 3
		_, err = CreateGatewayRoutePlanSnapshot(CreateGatewayRoutePlanSnapshotInput[string]{
			RoutePlanID: "plan-1", Mode: RouteStrategyModeFailover,
			OrderedAllowedTargets: []string{"a"}, Cursor: &cursor,
		})
		if err == nil || err.Error() != "route plan cursor 3 is outside ordered target range" {
			t.Fatalf("err = %v", err)
		}
		negative := -1
		_, err = CreateGatewayRoutePlanSnapshot(CreateGatewayRoutePlanSnapshotInput[string]{
			RoutePlanID: "plan-1", Mode: RouteStrategyModeFailover,
			OrderedAllowedTargets: []string{"a"}, Cursor: &negative,
		})
		if err == nil || err.Error() != "route plan cursor -1 is outside ordered target range" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("advance moves and validates the cursor", func(t *testing.T) {
		snapshot, err := CreateGatewayRoutePlanSnapshot(CreateGatewayRoutePlanSnapshotInput[string]{
			RoutePlanID: "plan-1", Mode: RouteStrategyModeFailover,
			OrderedAllowedTargets: []string{"a", "b"},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		advanced, err := AdvanceGatewayRoutePlanCursor(snapshot, snapshot.Cursor+1)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if advanced.Cursor != 1 {
			t.Fatalf("cursor = %d, want 1", advanced.Cursor)
		}
		if _, err := AdvanceGatewayRoutePlanCursor(advanced, advanced.Cursor+1); err == nil {
			t.Fatal("expected out-of-range cursor error")
		}
	})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func accountIDs(accounts []UpstreamAccount) []string {
	ids := make([]string, 0, len(accounts))
	for _, account := range accounts {
		ids = append(ids, account.ID)
	}
	return ids
}

func bindingIDs(bindings []GroupBindingRow) []string {
	ids := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		ids = append(ids, binding.ID)
	}
	return ids
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func stringsHasPrefix(value, prefix string) bool {
	return len(value) >= len(prefix) && value[:len(prefix)] == prefix
}

func mathMaxInt64() int64 { return int64(^uint64(0) >> 1) }

func base64URL(value string) string { return base64.RawURLEncoding.EncodeToString([]byte(value)) }

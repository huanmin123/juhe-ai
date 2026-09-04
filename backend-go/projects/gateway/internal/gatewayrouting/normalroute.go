package gatewayrouting

import (
	"context"
	"fmt"
)

// Normal gateway model route outcomes (normal-model-route.service.ts).
const (
	NormalRouteOutcomeSelected = "selected"
	NormalRouteOutcomeSkipped  = "skipped"
	NormalRouteOutcomeFailed   = "failed"
)

// Skip reasons emitted by resolveNormalGatewayModelRoute.
const (
	SkipReasonRouteStrategyIsHybridSmart = "route_strategy_is_hybrid_smart"
	SkipReasonMissingRequestedModel      = "missing_requested_model"
	SkipReasonEmptyBinding               = "empty_binding"
	SkipReasonSingleProvider             = "single_provider"
)

// Failure codes emitted by resolveNormalGatewayModelRoute.
const (
	FailCodeModelNotRoutableForAPIKey = "model_not_routable_for_api_key"
	FailCodeModelRouteAmbiguous       = "model_route_ambiguous"
	FailCodeModelRouteUnavailable     = "model_route_unavailable"
	FailCodeModelTargetGroupNotBound  = "model_target_group_not_bound"
	FailCodeModelTargetGroupUnavailable = "model_target_group_unavailable"
)

// NormalGatewayModelRouteResult mirrors the Node
// NormalGatewayModelRouteResult union: Outcome picks the active variant and
// only the matching fields carry data.
type NormalGatewayModelRouteResult struct {
	Outcome string

	// skipped
	Reason string

	// requestedModel: present for selected/failed and every skipped reason
	// except missing_requested_model (Node omits it there).
	RequestedModel string

	// failed
	StatusCode           int
	Type                 string
	Code                 string
	Message              string
	MatchedProviderCodes []string

	// selected
	APIKeyRecord                *APIKeyRow
	GroupID                     string
	GroupAccess                 GroupUsageAccessMetadata
	Accounts                    []UpstreamAccount
	ResponseInspectionPolicies  []ResponseInspectionPolicySummary
	RouteSource                 NormalGatewayModelRouteSource
	MatchedProviderCode         string
}

// ResolveNormalGatewayModelRouteInput mirrors
// ResolveNormalGatewayModelRouteInput.
type ResolveNormalGatewayModelRouteInput struct {
	Request                     RequestView
	APIKeyRecord                *APIKeyRow
	RequestClientCompatibility  string
}

// NormalModelRouteService mirrors normal-model-route.service.ts
// resolveNormalGatewayModelRoute: resolve the target group for a non-hybrid
// strategy API key, or produce the exact skip/failure contract.
type NormalModelRouteService struct {
	TargetGroups *TargetGroupSelector
}

// NewNormalModelRouteService wires the service onto a target group selector.
func NewNormalModelRouteService(runtimeCache RuntimeCacheReader, capabilityFilter AccountCapabilityFilter) *NormalModelRouteService {
	return &NormalModelRouteService{
		TargetGroups: &TargetGroupSelector{
			RuntimeCache:     runtimeCache,
			CapabilityFilter: capabilityFilter,
		},
	}
}

// catalogProviderRoute mirrors the Node CatalogProviderRoute.
type catalogProviderRoute struct {
	providerCode         string
	matchedProviderCodes []string
}

// catalogRouteResult mirrors the Node resolveCatalogProviderRoute result
// union.
type catalogRouteResult struct {
	outcome              string // ProviderModelRouteMatched | ProviderModelRouteMissing | ProviderModelRouteAmbiguous
	route                catalogProviderRoute
	matchedProviderCodes []string
}

// ResolveNormalGatewayModelRoute mirrors resolveNormalGatewayModelRoute.
func (s *NormalModelRouteService) ResolveNormalGatewayModelRoute(ctx context.Context, input ResolveNormalGatewayModelRouteInput) (NormalGatewayModelRouteResult, error) {
	apiKeyRecord := input.APIKeyRecord
	if apiKeyRecord.RouteStrategyMode == RouteStrategyModeHybridSmart {
		return NormalGatewayModelRouteResult{Outcome: NormalRouteOutcomeSkipped, Reason: SkipReasonRouteStrategyIsHybridSmart}, nil
	}

	requestedModel := trimSpace(input.Request.requestModel())
	if requestedModel == "" {
		return NormalGatewayModelRouteResult{Outcome: NormalRouteOutcomeSkipped, Reason: SkipReasonMissingRequestedModel}, nil
	}

	bindings := activeGatewayAPIKeyGroupBindings(apiKeyRecord)
	if len(bindings) == 0 {
		return NormalGatewayModelRouteResult{Outcome: NormalRouteOutcomeSkipped, Reason: SkipReasonEmptyBinding, RequestedModel: requestedModel}, nil
	}

	activeProviderCodes := make(map[string]struct{}, len(bindings))
	for _, binding := range bindings {
		activeProviderCodes[binding.ProviderCode] = struct{}{}
	}
	if len(activeProviderCodes) == 1 {
		return NormalGatewayModelRouteResult{Outcome: NormalRouteOutcomeSkipped, Reason: SkipReasonSingleProvider, RequestedModel: requestedModel}, nil
	}

	catalogRoute, err := s.resolveCatalogProviderRoute(ctx, bindings, requestedModel, apiKeyRecord.SystemAccountID)
	if err != nil {
		return NormalGatewayModelRouteResult{}, err
	}
	if catalogRoute.outcome == ProviderModelRouteMissing {
		return NormalGatewayModelRouteResult{
			Outcome:              NormalRouteOutcomeFailed,
			StatusCode:           400,
			Type:                 "invalid_request_error",
			Code:                 FailCodeModelNotRoutableForAPIKey,
			Message:              fmt.Sprintf("当前 API Key 绑定的供应商中没有可路由模型：%s", requestedModel),
			RequestedModel:       requestedModel,
			MatchedProviderCodes: catalogRoute.matchedProviderCodes,
		}, nil
	}

	mappingTarget, err := s.TargetGroups.SelectGatewayModelTargetGroup(ctx, ModelTargetGroupInput{
		Request:                     input.Request,
		APIKeyRecord:                apiKeyRecord,
		Bindings:                    bindings,
		TargetModel:                 requestedModel,
		RequestClientCompatibility:  input.RequestClientCompatibility,
		CandidatePriority: func(candidate ModelTargetGroupCandidate) float64 {
			catalogProviderMatched := catalogRoute.outcome == ProviderModelRouteMatched &&
				candidate.Binding.ProviderCode == catalogRoute.route.providerCode
			return NormalGatewayModelTargetPriority(candidate.ModelFilter, catalogProviderMatched)
		},
	})
	if err != nil {
		return NormalGatewayModelRouteResult{}, err
	}
	if mappingTarget != nil {
		routeSource := NormalGatewayModelRouteSourceOf(mappingTarget.ModelFilter)
		selectedProviderBindings := make([]GroupBindingRow, 0)
		for _, candidate := range bindings {
			if candidate.ProviderCode == mappingTarget.Binding.ProviderCode {
				selectedProviderBindings = append(selectedProviderBindings, candidate)
			}
		}
		updatedRecord := *apiKeyRecord
		updatedRecord.SelectedGroupID = mappingTarget.GroupID
		updatedRecord.GroupBindings = selectedProviderBindings
		matchedProviderCode := ""
		switch {
		case routeSource == RouteSourceAccountMapping:
			matchedProviderCode = mappingTarget.GroupAccess.ProviderCode
		case catalogRoute.outcome == ProviderModelRouteMatched:
			matchedProviderCode = catalogRoute.route.providerCode
		}
		return NormalGatewayModelRouteResult{
			Outcome:                    NormalRouteOutcomeSelected,
			APIKeyRecord:               &updatedRecord,
			GroupID:                    mappingTarget.GroupID,
			GroupAccess:                mappingTarget.GroupAccess,
			Accounts:                   mappingTarget.Accounts,
			ResponseInspectionPolicies: mappingTarget.ResponseInspectionPolicies,
			RequestedModel:             requestedModel,
			RouteSource:                routeSource,
			MatchedProviderCode:        matchedProviderCode,
		}, nil
	}

	if catalogRoute.outcome == ProviderModelRouteAmbiguous {
		return NormalGatewayModelRouteResult{
			Outcome:              NormalRouteOutcomeFailed,
			StatusCode:           400,
			Type:                 "invalid_request_error",
			Code:                 FailCodeModelRouteAmbiguous,
			Message:              fmt.Sprintf("请求模型在多个供应商中同时存在，无法确定目标号池：%s", requestedModel),
			RequestedModel:       requestedModel,
			MatchedProviderCodes: catalogRoute.matchedProviderCodes,
		}, nil
	}
	if catalogRoute.outcome != ProviderModelRouteMatched {
		return NormalGatewayModelRouteResult{
			Outcome:        NormalRouteOutcomeFailed,
			StatusCode:     400,
			Type:           "invalid_request_error",
			Code:           FailCodeModelRouteUnavailable,
			Message:        fmt.Sprintf("请求模型无法确定目标号池：%s", requestedModel),
			RequestedModel: requestedModel,
		}, nil
	}
	matchedRoute := catalogRoute.route
	candidateBindings := make([]GroupBindingRow, 0)
	for _, binding := range bindings {
		if binding.ProviderCode == matchedRoute.providerCode {
			candidateBindings = append(candidateBindings, binding)
		}
	}

	if len(candidateBindings) == 0 {
		return NormalGatewayModelRouteResult{
			Outcome:              NormalRouteOutcomeFailed,
			StatusCode:           400,
			Type:                 "invalid_request_error",
			Code:                 FailCodeModelTargetGroupNotBound,
			Message:              fmt.Sprintf("当前 API Key 未绑定请求模型对应的供应商分组：%s", requestedModel),
			RequestedModel:       requestedModel,
			MatchedProviderCodes: matchedRoute.matchedProviderCodes,
		}, nil
	}

	return NormalGatewayModelRouteResult{
		Outcome:              NormalRouteOutcomeFailed,
		StatusCode:           503,
		Type:                 "service_unavailable",
		Code:                 FailCodeModelTargetGroupUnavailable,
		Message:              fmt.Sprintf("请求模型对应的供应商分组当前没有可用账号：%s", requestedModel),
		RequestedModel:       requestedModel,
		MatchedProviderCodes: matchedRoute.matchedProviderCodes,
	}, nil
}

// NormalGatewayModelTargetPriority mirrors normalGatewayModelTargetPriority:
// direct model matches outrank mapping matches, which outrank a bare catalog
// provider match; anything else is -Infinity.
func NormalGatewayModelTargetPriority(modelFilter GatewayModelAccountFilterResult, catalogProviderMatched bool) float64 {
	if modelFilter.DirectMatchedCount > 0 {
		return 2
	}
	if modelFilter.MappingMatchedCount > 0 {
		return 1
	}
	if catalogProviderMatched {
		return 0
	}
	return negInf()
}

// NormalGatewayModelRouteSourceOf mirrors normalGatewayModelRouteSource.
func NormalGatewayModelRouteSourceOf(modelFilter GatewayModelAccountFilterResult) NormalGatewayModelRouteSource {
	if modelFilter.DirectMatchedCount > 0 {
		return RouteSourceCatalogProvider
	}
	return RouteSourceAccountMapping
}

// activeGatewayAPIKeyGroupBindings mirrors activeGatewayApiKeyGroupBindings:
// status === 'active' only (group_enabled is NOT consulted here — that
// filter belongs to the dispatch ordering path).
func activeGatewayAPIKeyGroupBindings(apiKeyRecord *APIKeyRow) []GroupBindingRow {
	bindings := make([]GroupBindingRow, 0, len(apiKeyRecord.GroupBindings))
	for _, binding := range apiKeyRecord.GroupBindings {
		if binding.Status != RowStatusActive {
			continue
		}
		bindings = append(bindings, binding)
	}
	return bindings
}

// resolveCatalogProviderRoute mirrors resolveCatalogProviderRoute: a matched
// provider outside the bound provider codes downgrades to missing.
func (s *NormalModelRouteService) resolveCatalogProviderRoute(ctx context.Context, bindings []GroupBindingRow, requestedModel, systemAccountID string) (catalogRouteResult, error) {
	providerCodeSet := make(map[string]struct{}, len(bindings))
	providerCodes := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		if _, ok := providerCodeSet[binding.ProviderCode]; ok {
			continue
		}
		providerCodeSet[binding.ProviderCode] = struct{}{}
		providerCodes = append(providerCodes, binding.ProviderCode)
	}
	route, err := s.TargetGroups.RuntimeCache.ResolveCachedProviderModelRouteAsync(ctx, ProviderModelRouteInput{
		Model:           requestedModel,
		ProviderCodes:   providerCodes,
		SystemAccountID: systemAccountID,
		IncludeUnpriced: true,
	})
	if err != nil {
		return catalogRouteResult{}, err
	}
	if route.Outcome != ProviderModelRouteMatched {
		return catalogRouteResult{
			outcome:              route.Outcome,
			matchedProviderCodes: route.MatchedProviderCodes,
		}, nil
	}

	providerCode := route.ProviderCode
	bound := false
	for _, binding := range bindings {
		if binding.ProviderCode == providerCode {
			bound = true
			break
		}
	}
	if !bound {
		return catalogRouteResult{
			outcome:              ProviderModelRouteMissing,
			matchedProviderCodes: route.MatchedProviderCodes,
		}, nil
	}

	return catalogRouteResult{
		outcome: ProviderModelRouteMatched,
		route: catalogProviderRoute{
			providerCode:         providerCode,
			matchedProviderCodes: route.MatchedProviderCodes,
		},
	}, nil
}

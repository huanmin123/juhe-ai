package gatewaydispatch

import (
	"context"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
)

// filterOpenAIGatewayRequestCandidateAccounts, migrated from
// dispatch/candidate-filter.ts.

// CandidateFilterOutput mirrors RequestCandidateFilterResult.
type CandidateFilterOutput struct {
	// Outcome is 'accounts' | 'fallback' | 'completed'.
	Outcome       string
	Accounts      []AccountCandidate
	ModelPriority *gatewayrouting.GatewayAccountModelPriority
	// Fallback variant
	Reason  string
	Context any
}

// CandidateFilterArgs mirrors the Node input object.
type CandidateFilterArgs struct {
	Req                        *gatewaypreauth.GatewayRequest
	AuditCapture               AuditCapture
	UsageContext               gatewaypreauth.GatewayFailureUsageContext
	StartedAt                  int64
	RawCandidateAccounts       []AccountCandidate
	ClientStrategy             gatewaypreauth.ClientStrategyContext
	SystemAccountID            string
	APIKeyID                   string
	GroupID                    string
	ClientIP                   string
	Endpoint                   string
	BypassModelFilter          bool
	RequestModelOverride       string
	RouteCoordinator           gatewayrouting.GatewayRouteCoordinatorOwner
	RecoverUnavailableCandidateAccounts func(ctx context.Context) ([]AccountCandidate, error)
	LoadModelAwareCandidateAccounts     func(ctx context.Context, requestedModel, sourceEndpointFamily string) ([]AccountCandidate, error)
}

// requestCapabilityMismatchMessage mirrors requestCapabilityMismatchMessage.
func requestCapabilityMismatchMessage(reason string) string {
	if reason == "anthropic_native_group_openai_compatible_request" {
		return "当前 API Key 绑定的是 Anthropic 原生分组，不兼容 Codex / OpenAI 请求路径；请改用 Anthropic /v1/messages 客户端，或绑定支持 OpenAI Responses / Chat Completions 的分组"
	}
	return "当前分组无账户支持请求路径或客户端协议"
}

// shouldReloadModelAwareCandidates mirrors shouldReloadModelAwareCandidates.
func shouldReloadModelAwareCandidates(requestedModel string, filter ModelFilterResult, loader func(ctx context.Context, requestedModel, sourceEndpointFamily string) ([]AccountCandidate, error)) bool {
	return requestedModel != "" && loader != nil &&
		filter.DirectMatchedCount == 0 && filter.MappingMatchedCount == 0
}

// FilterOpenAIGatewayRequestCandidateAccounts mirrors
// filterOpenAIGatewayRequestCandidateAccounts.
func (p *CandidatePipeline) FilterOpenAIGatewayRequestCandidateAccounts(ctx context.Context, input CandidateFilterArgs) (CandidateFilterOutput, error) {
	requestedModel := trimString(input.RequestModelOverride)
	if requestedModel == "" {
		if model, ok := gatewaypreauth.RequestModel(input.Req); ok {
			requestedModel = model
		}
	}
	sourceEndpointFamily := GatewayRequestEndpointFamily(input.Req)
	rawCandidateAccounts := input.RawCandidateAccounts
	if len(rawCandidateAccounts) == 0 && requestedModel != "" && input.LoadModelAwareCandidateAccounts != nil {
		loaded, err := input.LoadModelAwareCandidateAccounts(ctx, requestedModel, sourceEndpointFamily)
		if err != nil {
			return CandidateFilterOutput{}, err
		}
		if loaded != nil {
			rawCandidateAccounts = loaded
		}
	}
	if len(rawCandidateAccounts) == 0 {
		fallback, err := input.RouteCoordinator.RequestFallback(ctx, "no_candidate_accounts")
		if err != nil {
			return CandidateFilterOutput{}, err
		}
		if fallback.Attempted {
			return CandidateFilterOutput{Outcome: gatewaypreauth.CandidateOutcomeFallback, Reason: "no_candidate_accounts", Context: fallback.Context}, nil
		}
		if input.RecoverUnavailableCandidateAccounts != nil {
			recovered, err := input.RecoverUnavailableCandidateAccounts(ctx)
			if err != nil {
				return CandidateFilterOutput{}, err
			}
			if recovered != nil {
				rawCandidateAccounts = recovered
			}
		}
	}

	capabilityFilter := FilterGatewayAccountsByRequestCapability(
		input.Req, rawCandidateAccounts, p.engine.Driver,
		input.ClientStrategy.RequestClientCompatibility, "",
	)
	if capabilityFilter.SkippedCount > 0 {
		input.AuditCapture.AddGatewayMetadata("account_request_capability_filter", map[string]any{
			"skippedCount":               capabilityFilter.SkippedCount,
			"remainingCount":             len(capabilityFilter.Accounts),
			"reason":                     capabilityFilter.Reason,
			"requestClientCompatibility": input.ClientStrategy.RequestClientCompatibility,
		})
	}
	if len(rawCandidateAccounts) > 0 && len(capabilityFilter.Accounts) == 0 {
		reason := capabilityFilter.Reason
		if reason == "" {
			reason = "request_capability_mismatch"
		}
		fallback, err := input.RouteCoordinator.RequestFallback(ctx, reason)
		if err != nil {
			return CandidateFilterOutput{}, err
		}
		if fallback.Attempted {
			return CandidateFilterOutput{Outcome: gatewaypreauth.CandidateOutcomeFallback, Reason: reason, Context: fallback.Context}, nil
		}
		message := requestCapabilityMismatchMessage(reason)
		if err := input.RouteCoordinator.CompleteFailure(ctx, gatewayrouting.GatewayRouteFinalFailure{
			StatusCode: 503,
			Message:    message,
			ErrorType:  "service_unavailable",
			ErrorCode:  reason,
			ErrorPhase: "dispatch",
		}); err != nil {
			return CandidateFilterOutput{}, err
		}
		return CandidateFilterOutput{Outcome: gatewaypreauth.CandidateOutcomeCompleted}, nil
	}

	var modelFilter ModelFilterResult
	if input.BypassModelFilter {
		modelFilter = BypassGatewayModelFilter(capabilityFilter.Accounts, sourceEndpointFamily)
	} else {
		modelFilter = FilterGatewayAccountsByRequestedModel(capabilityFilter.Accounts, requestedModel, sourceEndpointFamily)
	}
	if shouldReloadModelAwareCandidates(requestedModel, modelFilter, input.LoadModelAwareCandidateAccounts) {
		modelAwareRawAccounts, err := input.LoadModelAwareCandidateAccounts(ctx, requestedModel, sourceEndpointFamily)
		if err != nil {
			return CandidateFilterOutput{}, err
		}
		if len(modelAwareRawAccounts) > 0 {
			modelAwareCapabilityFilter := FilterGatewayAccountsByRequestCapability(
				input.Req, modelAwareRawAccounts, p.engine.Driver,
				input.ClientStrategy.RequestClientCompatibility, "",
			)
			modelAwareModelFilter := FilterGatewayAccountsByRequestedModel(modelAwareCapabilityFilter.Accounts, requestedModel, sourceEndpointFamily)
			if modelAwareModelFilter.DirectMatchedCount > 0 ||
				modelAwareModelFilter.MappingMatchedCount > 0 ||
				(len(modelFilter.Accounts) == 0 && len(modelAwareModelFilter.Accounts) > 0) {
				capabilityFilter = modelAwareCapabilityFilter
				modelFilter = modelAwareModelFilter
				requestedModelAttribute := any(modelAwareModelFilter.RequestedModel)
				if requestedModelAttribute == "" {
					requestedModelAttribute = nil
				}
				familyAttribute := any(modelAwareModelFilter.SourceEndpointFamily)
				if familyAttribute == "" {
					familyAttribute = nil
				}
				reasonAttribute := any(modelAwareModelFilter.Reason)
				if reasonAttribute == "" {
					reasonAttribute = nil
				}
				input.AuditCapture.AddGatewayMetadata("account_model_candidate_window", map[string]any{
					"requestedModel":             requestedModelAttribute,
					"sourceEndpointFamily":       familyAttribute,
					"directMatchedCount":         modelAwareModelFilter.DirectMatchedCount,
					"mappingMatchedCount":        modelAwareModelFilter.MappingMatchedCount,
					"invalidModelConstraintCount": modelAwareModelFilter.InvalidModelConstraintCount,
					"remainingCount":             len(modelAwareModelFilter.Accounts),
				})
			}
		}
	}
	if modelFilter.SkippedCount > 0 || modelFilter.MappingMatchedCount > 0 {
		requestedModelAttribute := any(modelFilter.RequestedModel)
		if requestedModelAttribute == "" {
			requestedModelAttribute = nil
		}
		familyAttribute := any(modelFilter.SourceEndpointFamily)
		if familyAttribute == "" {
			familyAttribute = nil
		}
		reasonAttribute := any(modelFilter.Reason)
		if reasonAttribute == "" {
			reasonAttribute = nil
		}
		input.AuditCapture.AddGatewayMetadata("account_model_filter", map[string]any{
			"requestedModel":             requestedModelAttribute,
			"sourceEndpointFamily":       familyAttribute,
			"skippedCount":               modelFilter.SkippedCount,
			"limitedAccountCount":        modelFilter.LimitedAccountCount,
			"invalidModelConstraintCount": modelFilter.InvalidModelConstraintCount,
			"directMatchedCount":         modelFilter.DirectMatchedCount,
			"mappingMatchedCount":        modelFilter.MappingMatchedCount,
			"remainingCount":             len(modelFilter.Accounts),
			"reason":                     reasonAttribute,
		})
	}
	if len(capabilityFilter.Accounts) > 0 && len(modelFilter.Accounts) == 0 {
		reason := modelFilter.Reason
		if reason == "" {
			reason = "unsupported_model"
		}
		fallback, err := input.RouteCoordinator.RequestFallback(ctx, reason)
		if err != nil {
			return CandidateFilterOutput{}, err
		}
		if fallback.Attempted {
			return CandidateFilterOutput{Outcome: gatewaypreauth.CandidateOutcomeFallback, Reason: reason, Context: fallback.Context}, nil
		}
		message := GatewayModelFilterFailureMessage(modelFilter)
		if err := input.RouteCoordinator.CompleteFailure(ctx, gatewayrouting.GatewayRouteFinalFailure{
			StatusCode: 503,
			Message:    message,
			ErrorType:  "service_unavailable",
			ErrorCode:  reason,
			ErrorPhase: "dispatch",
		}); err != nil {
			return CandidateFilterOutput{}, err
		}
		return CandidateFilterOutput{Outcome: gatewaypreauth.CandidateOutcomeCompleted}, nil
	}

	return CandidateFilterOutput{
		Outcome:       gatewaypreauth.CandidateOutcomeAccounts,
		Accounts:      modelFilter.Accounts,
		ModelPriority: modelFilter.ModelPriority,
	}, nil
}

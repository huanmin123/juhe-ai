package gatewaydispatch

import (
	"context"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// CandidatePipeline is the G15 pipeline facade: the Go assembly of
// dispatch/candidate-filter.ts + dispatch/preparation.ts +
// dispatch/api-key-group-fallback-candidate.ts. It satisfies the frozen
// gatewaypreauth.CandidatePipeline port (compile-time asserted in ports.go)
// for G20 wiring.

// CandidatePipeline consumes the shared engine.
type CandidatePipeline struct {
	engine *Engine
}

// NewCandidatePipeline mirrors constructing the pipeline around the shared
// dispatch engine.
func NewCandidatePipeline(engine *Engine) *CandidatePipeline {
	return &CandidatePipeline{engine: engine}
}

// FilterCandidates implements gatewaypreauth.CandidatePipeline.
func (p *CandidatePipeline) FilterCandidates(ctx context.Context, input gatewaypreauth.CandidateFilterInput) (gatewaypreauth.CandidateFilterResult, error) {
	output, err := p.FilterOpenAIGatewayRequestCandidateAccounts(ctx, CandidateFilterArgs{
		Req:                      input.Req,
		AuditCapture:             p.engine.auditCaptureOf(input.AuditCapture),
		UsageContext:             input.UsageContext,
		StartedAt:                input.StartedAt,
		RawCandidateAccounts:     input.RawCandidates,
		ClientStrategy:           input.ClientStrategy,
		SystemAccountID:          input.SystemAccountID,
		APIKeyID:                 input.APIKeyID,
		GroupID:                  input.GroupID,
		ClientIP:                 input.ClientIP,
		Endpoint:                 input.Endpoint,
		BypassModelFilter:        input.BypassModelFilter,
		RequestModelOverride:     input.RequestModelOverride,
		RouteCoordinator:         input.RouteCoordinator,
		RecoverUnavailableCandidateAccounts: func(ctx context.Context) ([]AccountCandidate, error) {
			if input.RecoverUnavailableCandidateAccounts == nil {
				return nil, nil
			}
			return input.RecoverUnavailableCandidateAccounts()
		},
		LoadModelAwareCandidateAccounts: func(ctx context.Context, model, sourceEndpointFamily string) ([]AccountCandidate, error) {
			if input.LoadModelAwareCandidateAccounts == nil {
				return nil, nil
			}
			return input.LoadModelAwareCandidateAccounts(model, sourceEndpointFamily)
		},
	})
	if err != nil {
		return gatewaypreauth.CandidateFilterResult{}, err
	}
	result := gatewaypreauth.CandidateFilterResult{Outcome: output.Outcome}
	switch output.Outcome {
	case gatewaypreauth.CandidateOutcomeAccounts:
		result.Accounts = output.Accounts
		result.ModelPriority = output.ModelPriority
	case gatewaypreauth.CandidateOutcomeFallback:
		result.Reason = output.Reason
	}
	return result, nil
}

// PrepareDispatchAccounts implements gatewaypreauth.CandidatePipeline.
func (p *CandidatePipeline) PrepareDispatchAccounts(ctx context.Context, input gatewaypreauth.DispatchPreparationInput) (gatewaypreauth.DispatchPreparationResult, error) {
	output, err := p.PrepareOpenAIGatewayDispatchAccounts(ctx, input)
	if err != nil {
		return gatewaypreauth.DispatchPreparationResult{}, err
	}
	result := gatewaypreauth.DispatchPreparationResult{}
	switch output.Outcome {
	case PreparationOutcomeReady:
		result.Outcome = gatewaypreauth.CandidateOutcomeAccounts
	case PreparationOutcomeFallback:
		result.Outcome = gatewaypreauth.CandidateOutcomeFallback
	default:
		result.Outcome = gatewaypreauth.CandidateOutcomeCompleted
	}
	switch output.Outcome {
	case PreparationOutcomeReady:
		result.Accounts = output.Accounts
		result.HotQualityExplorationReservation = output.HotQualityExplorationReservation
		if output.SettleHotQualityExplorationAfterDispatch != nil {
			settle := output.SettleHotQualityExplorationAfterDispatch
			result.SettleHotQualityExplorationAfterDispatch = func(outcome string) error {
				return settle(ctx, outcome)
			}
		}
		result.ReleaseClientIPConcurrency = output.ReleaseClientIPConcurrency
		result.NormalRouteLatencyDegradationApplied = output.NormalRouteLatencyDegradationApplied
		result.CodexTurnAccountAvoidanceApplied = output.CodexTurnAccountAvoidanceApplied
		result.CodexTurnAvoidedAccountIDs = output.CodexTurnAvoidedAccountIDs
		result.PrecheckHalfOpenEligible = output.PrecheckHalfOpenEligible
	case gatewaypreauth.CandidateOutcomeFallback:
		result.Reason = output.Reason
	}
	return result, nil
}

// ResolveNextGroupFallbackCandidateResult adapts the two-value resolution.
type ResolveNextGroupFallbackCandidateResult struct {
	Found    bool
	Candidate GroupFallbackCandidateOutput
}

// ResolveNextGroupFallbackCandidate implements
// gatewaypreauth.CandidatePipeline.
func (p *CandidatePipeline) ResolveNextGroupFallbackCandidate(ctx context.Context, input gatewaypreauth.GroupFallbackCandidateInput) (gatewaypreauth.GroupFallbackCandidate, bool, error) {
	output, found, err := p.ResolveNextGroupFallbackCandidateForArgs(ctx, GroupFallbackArgs{
		Req:                        input.Req,
		Reason:                     input.Reason,
		APIKeyRecord:               input.APIKeyRecord,
		SystemAccountID:            input.SystemAccountID,
		GroupID:                    input.GroupID,
		RequestLane:                input.RequestLane,
		RequestClientCompatibility: input.RequestClientCompatibility,
		RoutePlanSnapshot:          &input.RoutePlanSnapshot,
	})
	if err != nil || !found {
		return gatewaypreauth.GroupFallbackCandidate{}, found, err
	}
	return gatewaypreauth.GroupFallbackCandidate{
		GroupID:                    output.GroupID,
		Accounts:                   output.Accounts,
		ResponseInspectionPolicies: output.ResponseInspectionPolicies,
		RoutePlanSnapshot:          output.RoutePlanSnapshot,
	}, true, nil
}

// gatewayprotoLane converts the string lane into the typed lane.
func gatewayprotoLane(lane string) gatewayproto.RequestLane {
	if lane == string(gatewayproto.LaneImage) {
		return gatewayproto.LaneImage
	}
	return gatewayproto.LaneText
}

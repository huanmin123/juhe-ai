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
		Req:                  input.Req,
		AuditCapture:         p.engine.auditCaptureOf(input.AuditCapture),
		UsageContext:         input.UsageContext,
		StartedAt:            input.StartedAt,
		RawCandidateAccounts: input.RawCandidates,
		ClientStrategy:       input.ClientStrategy,
		SystemAccountID:      input.SystemAccountID,
		APIKeyID:             input.APIKeyID,
		GroupID:              input.GroupID,
		ClientIP:             input.ClientIP,
		Endpoint:             input.Endpoint,
		BypassModelFilter:    input.BypassModelFilter,
		RequestModelOverride: input.RequestModelOverride,
		RouteCoordinator:     input.RouteCoordinator,
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
		// W1b 续：预过滤跳过明细跨端口投影（gatewaypreauth.AccountSkipDetail
		// 与引擎侧 AccountSkip 同形 id/reason JSON 键）。
		if len(output.PreFilterSkipped) > 0 {
			result.PreFilterSkipped = make([]gatewaypreauth.AccountSkipDetail, 0, len(output.PreFilterSkipped))
			for _, skip := range output.PreFilterSkipped {
				result.PreFilterSkipped = append(result.PreFilterSkipped, gatewaypreauth.AccountSkipDetail{AccountID: skip.AccountID, Reason: skip.Reason})
			}
		}
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
		result.SchedulingExclusions = projectSchedulingExclusionsToPreauth(output.SchedulingExclusions)
		result.DispatchSegments = projectDispatchSegmentsToPreauth(output.DispatchSegments)
		result.PrecheckHalfOpenEligible = output.PrecheckHalfOpenEligible
	case gatewaypreauth.CandidateOutcomeFallback:
		result.Reason = output.Reason
	}
	return result, nil
}

// ResolveNextGroupFallbackCandidateResult adapts the two-value resolution.
type ResolveNextGroupFallbackCandidateResult struct {
	Found     bool
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
		ExcludedAccountIDs:         input.ExcludedAccountIDs,
		RoutePlanSnapshot:          &input.RoutePlanSnapshot,
		AuditCapture:               input.AuditCapture,
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

// projectSchedulingExclusionsToPreauth 把引擎侧排除集投影为 preauth 端口镜像
// 形状（gatewaypreauth 不反向依赖引擎包，跨端口投影逐字段复制；
// AccountSkipDetail 同模式）。nil 保持 nil。
func projectSchedulingExclusionsToPreauth(exclusions *SchedulingExclusions) *gatewaypreauth.SchedulingExclusions {
	if exclusions == nil {
		return nil
	}
	return &gatewaypreauth.SchedulingExclusions{
		ExcludedAccountIDs: append([]string(nil), exclusions.ExcludedAccountIDs...),
	}
}

// projectDispatchSegmentsToPreauth 把引擎侧分派段投影为 preauth 端口镜像形状
// （逐字段复制；AccountCandidate 两侧同为 gatewayruntimecache.OpenAIAccountSecret
// 别名，切片直接复用，准备结果在本投影链上只读）。
func projectDispatchSegmentsToPreauth(segments []DispatchSegment) []gatewaypreauth.DispatchSegment {
	if len(segments) == 0 {
		return nil
	}
	out := make([]gatewaypreauth.DispatchSegment, 0, len(segments))
	for _, segment := range segments {
		out = append(out, gatewaypreauth.DispatchSegment{
			OpaqueSegmentID: segment.OpaqueSegmentID,
			Tier: gatewaypreauth.DispatchPriorityTier{
				ModelRank:    segment.Tier.ModelRank,
				FallbackRank: segment.Tier.FallbackRank,
				SuperRank:    segment.Tier.SuperRank,
				Priority:     segment.Tier.Priority,
			},
			Accounts: segment.Accounts,
		})
	}
	return out
}

// gatewayprotoLane converts the string lane into the typed lane.
func gatewayprotoLane(lane string) gatewayproto.RequestLane {
	if lane == string(gatewayproto.LaneImage) {
		return gatewayproto.LaneImage
	}
	return gatewayproto.LaneText
}

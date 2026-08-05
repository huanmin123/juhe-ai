// Package gatewayfallbackpolicy implements the Node-ordered eligibility gates
// for a freshly hydrated cross-group fallback target. It has no route cursor,
// lease, HTTP, or upstream-dispatch ownership.
package gatewayfallbackpolicy

import (
	"context"
	"fmt"
	"maps"
	"strings"

	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
	"juhe-ai/backend-go/internal/modules/gatewaycapacityrouting"
	"juhe-ai/backend-go/internal/modules/gatewayfallbackreason"
	"juhe-ai/backend-go/internal/modules/gatewayingress"
	"juhe-ai/backend-go/internal/modules/gatewayrouteplan"
	protocolgateway "juhe-ai/backend-go/internal/protocols/gateway"
)

const (
	ReasonRuntimeDegraded          = string(gatewayfallbackreason.RuntimeDegraded)
	ReasonHighConcurrencyGroupBusy = string(gatewayfallbackreason.HighConcurrencyGroupBusy)
	ReasonGroupCapacityBusy        = string(gatewayfallbackreason.GroupCapacityBusy)
)

// CapabilityFilter must decide every supplied account from complete provider
// request facts. A missing runtime fact is an error, not an allowed account.
type CapabilityFilter interface {
	FilterFallbackCapability(context.Context, CapabilityInput) (AccountSelection, error)
}

type CapabilityInput struct {
	Target                     gatewayrouteplan.FallbackTarget
	Window                     gatewaycandidatewindow.Window
	Candidates                 []gatewaycandidatewindow.Candidate
	Intent                     gatewayingress.RequestIntent
	IngressFinalization        gatewayingress.FinalResult
	RequestShape               protocolgateway.RequestShape
	Protocol                   protocolgateway.ProtocolCode
	FinalLane                  gatewayingress.Lane
	RequestedModel             string
	EndpointFamily             string
	RequestClientCompatibility string
}

// AuthorizationQuotaChecker evaluates the target group's group/account/team
// authorization quotas after model filtering. Complete distinguishes a
// deliberately absent quota limit (allowed map entry omitted) from an
// unavailable or incomplete checker result.
type AuthorizationQuotaChecker interface {
	CheckFallbackAuthorizationQuota(context.Context, AuthorizationQuotaInput) (AuthorizationQuotaResult, error)
}

type AuthorizationQuotaInput struct {
	Target     gatewayrouteplan.FallbackTarget
	Window     gatewaycandidatewindow.Window
	Candidates []gatewaycandidatewindow.Candidate
}

type AuthorizationQuotaResult struct {
	Complete           bool
	AllowedByAccountID map[string]bool
}

// RuntimeDegradationOrderer receives the complete quota-allowed list and
// must return a permutation of that list. It cannot inject a candidate or
// silently drop one; bypassedAllDegraded preserves Node's special fallback
// rule for a runtime_degraded source reason.
type RuntimeDegradationOrderer interface {
	OrderFallbackRuntimeDegradation(context.Context, RuntimeDegradationInput) (RuntimeDegradationResult, error)
}

type RuntimeDegradationInput struct {
	Target               gatewayrouteplan.FallbackTarget
	Window               gatewaycandidatewindow.Window
	Candidates           []gatewaycandidatewindow.Candidate
	ModelRankByAccountID map[string]int
}

type RuntimeDegradationResult struct {
	CandidateAccountIDs []string
	BypassedAllDegraded bool
}

// CapacityEvaluator is satisfied by gatewaycapacityrouting.Service. It is
// used only for the two Node fallback reasons whose target selection skips an
// already all-busy group; it never acquires a lease.
type CapacityEvaluator interface {
	Evaluate(context.Context, gatewaycandidatewindow.Window, gatewayingress.Lane) (gatewaycapacityrouting.Result, error)
}

// AccountSelection contains an ordered subset of the input candidates. The
// service verifies the subset against the source window before continuing.
type AccountSelection struct {
	CandidateAccountIDs []string
}

type Options struct {
	Capability  CapabilityFilter
	Quota       AuthorizationQuotaChecker
	Degradation RuntimeDegradationOrderer
	Capacity    CapacityEvaluator
}

type Service struct {
	capability  CapabilityFilter
	quota       AuthorizationQuotaChecker
	degradation RuntimeDegradationOrderer
	capacity    CapacityEvaluator
}

func NewService(options Options) (*Service, error) {
	if options.Capability == nil || options.Quota == nil || options.Degradation == nil || options.Capacity == nil {
		return nil, fmt.Errorf("gateway fallback policy requires capability, authorization quota, runtime degradation, and capacity dependencies")
	}
	return &Service{
		capability: options.Capability, quota: options.Quota,
		degradation: options.Degradation, capacity: options.Capacity,
	}, nil
}

// SelectFallbackCandidates follows the Node candidate order exactly after the
// fresh window is loaded: excluded accounts, capability, model revalidation,
// authorization quota, runtime degradation, then reason-specific capacity.
// Every dependency is mandatory and every returned account list is checked as
// a subset/permutation, so unknown state cannot become implicit permission.
func (s *Service) SelectFallbackCandidates(ctx context.Context, input gatewayrouteplan.FallbackCandidatePolicyInput) (gatewayrouteplan.FallbackCandidatePolicyResult, error) {
	if s == nil || s.capability == nil || s.quota == nil || s.degradation == nil || s.capacity == nil {
		return gatewayrouteplan.FallbackCandidatePolicyResult{}, fmt.Errorf("gateway fallback policy is not configured")
	}
	if ctx == nil {
		return gatewayrouteplan.FallbackCandidatePolicyResult{}, fmt.Errorf("gateway fallback policy context is required")
	}
	if !input.Intent.Parsed() || input.Intent.Model() != input.RequestedModel || input.Intent.Stream() != input.RequestShape.Stream ||
		input.IngressFinalization.FinalLane() != input.FinalLane || input.IngressFinalization.SnapshotRevision() == "" || input.IngressFinalization.CandidateCapacity() < 1 ||
		input.Protocol == "" || !validFinalLane(input.FinalLane) || strings.TrimSpace(input.RequestShape.Model) == "" ||
		strings.TrimSpace(input.RequestedModel) == "" || input.RequestShape.Model != input.RequestedModel || strings.TrimSpace(input.EndpointFamily) == "" ||
		strings.TrimSpace(input.RequestClientCompatibility) == "" || !gatewayfallbackreason.Valid(input.Reason) {
		return gatewayrouteplan.FallbackCandidatePolicyResult{}, fmt.Errorf("gateway fallback policy request facts are required")
	}
	lane, err := parseLane(input.RequestLane)
	if err != nil {
		return gatewayrouteplan.FallbackCandidatePolicyResult{}, err
	}

	base, err := uniqueCandidates(input.Window.Candidates)
	if err != nil {
		return gatewayrouteplan.FallbackCandidatePolicyResult{}, err
	}
	excluded, err := uniqueIDs(input.ExcludedAccountIDs, "excluded fallback account")
	if err != nil {
		return gatewayrouteplan.FallbackCandidatePolicyResult{}, err
	}
	eligible := withoutExcluded(base, excluded)
	if len(eligible) == 0 {
		return gatewayrouteplan.FallbackCandidatePolicyResult{}, nil
	}

	capability, err := s.capability.FilterFallbackCapability(ctx, CapabilityInput{
		Target: input.Target, Window: input.Window, Candidates: copyCandidates(eligible),
		Intent: input.Intent, IngressFinalization: input.IngressFinalization, RequestShape: cloneRequestShape(input.RequestShape), Protocol: input.Protocol, FinalLane: input.FinalLane,
		RequestedModel: input.RequestedModel, EndpointFamily: input.EndpointFamily,
		RequestClientCompatibility: input.RequestClientCompatibility,
	})
	if err != nil {
		return gatewayrouteplan.FallbackCandidatePolicyResult{}, fmt.Errorf("filter fallback request capability: %w", err)
	}
	eligible, err = selectStableSubset(eligible, capability.CandidateAccountIDs, "capability")
	if err != nil {
		return gatewayrouteplan.FallbackCandidatePolicyResult{}, err
	}
	if len(eligible) == 0 {
		return gatewayrouteplan.FallbackCandidatePolicyResult{}, nil
	}

	eligible, modelRanks := filterModel(eligible, input.RequestedModel, input.EndpointFamily)
	if len(eligible) == 0 {
		return gatewayrouteplan.FallbackCandidatePolicyResult{}, nil
	}

	quota, err := s.quota.CheckFallbackAuthorizationQuota(ctx, AuthorizationQuotaInput{
		Target: input.Target, Window: input.Window, Candidates: copyCandidates(eligible),
	})
	if err != nil {
		return gatewayrouteplan.FallbackCandidatePolicyResult{}, fmt.Errorf("check fallback authorization quota: %w", err)
	}
	if !quota.Complete {
		return gatewayrouteplan.FallbackCandidatePolicyResult{}, fmt.Errorf("fallback authorization quota result is incomplete")
	}
	if err := validateQuotaDecisionKeys(eligible, quota.AllowedByAccountID); err != nil {
		return gatewayrouteplan.FallbackCandidatePolicyResult{}, err
	}
	eligible = filterQuota(eligible, quota.AllowedByAccountID)
	if len(eligible) == 0 {
		return gatewayrouteplan.FallbackCandidatePolicyResult{}, nil
	}

	degradation, err := s.degradation.OrderFallbackRuntimeDegradation(ctx, RuntimeDegradationInput{
		Target: input.Target, Window: input.Window, Candidates: copyCandidates(eligible), ModelRankByAccountID: maps.Clone(modelRanks),
	})
	if err != nil {
		return gatewayrouteplan.FallbackCandidatePolicyResult{}, fmt.Errorf("order fallback runtime degradation: %w", err)
	}
	baseEligible := eligible
	eligible, err = selectPermutation(baseEligible, degradation.CandidateAccountIDs, "runtime degradation")
	if err != nil {
		return gatewayrouteplan.FallbackCandidatePolicyResult{}, err
	}
	eligible = preserveDispatchPriorityTiers(baseEligible, eligible, modelRanks)
	if input.Reason == ReasonRuntimeDegraded && degradation.BypassedAllDegraded {
		return gatewayrouteplan.FallbackCandidatePolicyResult{}, nil
	}

	if input.Reason == ReasonHighConcurrencyGroupBusy || input.Reason == ReasonGroupCapacityBusy {
		window := input.Window
		window.Candidates = copyCandidates(eligible)
		capacity, capacityErr := s.capacity.Evaluate(ctx, window, lane)
		if capacityErr != nil {
			return gatewayrouteplan.FallbackCandidatePolicyResult{}, fmt.Errorf("observe fallback target capacity: %w", capacityErr)
		}
		if capacity.Observation.AllBusy {
			return gatewayrouteplan.FallbackCandidatePolicyResult{}, nil
		}
	}
	return gatewayrouteplan.FallbackCandidatePolicyResult{CandidateAccountIDs: candidateIDs(eligible)}, nil
}

func parseLane(value string) (gatewayingress.Lane, error) {
	switch gatewayingress.Lane(strings.TrimSpace(value)) {
	case gatewayingress.LaneText:
		return gatewayingress.LaneText, nil
	case gatewayingress.LaneImage:
		return gatewayingress.LaneImage, nil
	default:
		return "", fmt.Errorf("gateway fallback policy request lane is invalid")
	}
}

func uniqueCandidates(candidates []gatewaycandidatewindow.Candidate) ([]gatewaycandidatewindow.Candidate, error) {
	seen := make(map[string]struct{}, len(candidates))
	result := make([]gatewaycandidatewindow.Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		accountID := strings.TrimSpace(candidate.Projection.AccountID)
		if accountID == "" {
			return nil, fmt.Errorf("fallback policy candidate has no account id")
		}
		if _, exists := seen[accountID]; exists {
			return nil, fmt.Errorf("fallback policy candidate account id is duplicated: %q", accountID)
		}
		seen[accountID] = struct{}{}
		result = append(result, candidate)
	}
	return result, nil
}

func uniqueIDs(values []string, label string) (map[string]struct{}, error) {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, fmt.Errorf("%s is empty", label)
		}
		if _, exists := result[value]; exists {
			return nil, fmt.Errorf("%s is duplicated: %q", label, value)
		}
		result[value] = struct{}{}
	}
	return result, nil
}

func withoutExcluded(candidates []gatewaycandidatewindow.Candidate, excluded map[string]struct{}) []gatewaycandidatewindow.Candidate {
	result := make([]gatewaycandidatewindow.Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		if _, blocked := excluded[candidate.Projection.AccountID]; !blocked {
			result = append(result, candidate)
		}
	}
	return result
}

func filterModel(candidates []gatewaycandidatewindow.Candidate, requestedModel, endpointFamily string) ([]gatewaycandidatewindow.Candidate, map[string]int) {
	direct := make([]gatewaycandidatewindow.Candidate, 0, len(candidates))
	mapped := make([]gatewaycandidatewindow.Candidate, 0, len(candidates))
	ranks := make(map[string]int, len(candidates))
	for _, candidate := range candidates {
		resolution, supported := gatewaycandidatewindow.ResolveEffectiveModel(candidate, requestedModel, endpointFamily)
		if !supported {
			continue
		}
		if resolution.MappingApplied {
			ranks[candidate.Projection.AccountID] = 1
			mapped = append(mapped, candidate)
		} else {
			ranks[candidate.Projection.AccountID] = 0
			direct = append(direct, candidate)
		}
	}
	return append(direct, mapped...), ranks
}

func preserveDispatchPriorityTiers(base, reordered []gatewaycandidatewindow.Candidate, modelRanks map[string]int) []gatewaycandidatewindow.Candidate {
	if len(base) < 2 || len(reordered) < 2 {
		return reordered
	}
	tierOrder := make([]string, 0, len(base))
	known := make(map[string]struct{}, len(base))
	for _, candidate := range base {
		tier := dispatchPriorityTier(candidate, modelRanks)
		if _, exists := known[tier]; exists {
			continue
		}
		known[tier] = struct{}{}
		tierOrder = append(tierOrder, tier)
	}
	byTier := make(map[string][]gatewaycandidatewindow.Candidate, len(tierOrder))
	unknown := make([]gatewaycandidatewindow.Candidate, 0)
	for _, candidate := range reordered {
		tier := dispatchPriorityTier(candidate, modelRanks)
		if _, exists := known[tier]; !exists {
			unknown = append(unknown, candidate)
			continue
		}
		byTier[tier] = append(byTier[tier], candidate)
	}
	result := make([]gatewaycandidatewindow.Candidate, 0, len(reordered))
	for _, tier := range tierOrder {
		result = append(result, byTier[tier]...)
	}
	return append(result, unknown...)
}

func dispatchPriorityTier(candidate gatewaycandidatewindow.Candidate, modelRanks map[string]int) string {
	rank, found := modelRanks[candidate.Projection.AccountID]
	if !found {
		rank = 3
	}
	fallbackRank := 0
	if candidate.Projection.LocalFallbackEnabled {
		fallbackRank = 1
	}
	superRank := 1
	if candidate.Projection.LocalSuperPriorityEnabled {
		superRank = 0
	}
	return fmt.Sprintf("%d:%d:%d:%d", rank, fallbackRank, superRank, candidate.Projection.LocalPriority)
}

func validateQuotaDecisionKeys(candidates []gatewaycandidatewindow.Candidate, decisions map[string]bool) error {
	for accountID := range decisions {
		found := false
		for _, candidate := range candidates {
			if candidate.Projection.AccountID == accountID {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("fallback authorization quota returned an unknown account id: %q", accountID)
		}
	}
	return nil
}

func filterQuota(candidates []gatewaycandidatewindow.Candidate, decisions map[string]bool) []gatewaycandidatewindow.Candidate {
	result := make([]gatewaycandidatewindow.Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		if allowed, configured := decisions[candidate.Projection.AccountID]; !configured || allowed {
			result = append(result, candidate)
		}
	}
	return result
}

func selectSubset(candidates []gatewaycandidatewindow.Candidate, accountIDs []string, label string) ([]gatewaycandidatewindow.Candidate, error) {
	byID := candidateMap(candidates)
	result := make([]gatewaycandidatewindow.Candidate, 0, len(accountIDs))
	seen := make(map[string]struct{}, len(accountIDs))
	for _, accountID := range accountIDs {
		accountID = strings.TrimSpace(accountID)
		if accountID == "" {
			return nil, fmt.Errorf("fallback %s filter returned an empty account id", label)
		}
		if _, exists := seen[accountID]; exists {
			return nil, fmt.Errorf("fallback %s filter returned a duplicate account id: %q", label, accountID)
		}
		candidate, exists := byID[accountID]
		if !exists {
			return nil, fmt.Errorf("fallback %s filter returned an account outside its input: %q", label, accountID)
		}
		seen[accountID] = struct{}{}
		result = append(result, candidate)
	}
	return result, nil
}

func selectStableSubset(candidates []gatewaycandidatewindow.Candidate, accountIDs []string, label string) ([]gatewaycandidatewindow.Candidate, error) {
	selected, err := selectSubset(candidates, accountIDs, label)
	if err != nil {
		return nil, err
	}
	set := make(map[string]struct{}, len(selected))
	for _, candidate := range selected {
		set[candidate.Projection.AccountID] = struct{}{}
	}
	result := make([]gatewaycandidatewindow.Candidate, 0, len(selected))
	for _, candidate := range candidates {
		if _, found := set[candidate.Projection.AccountID]; found {
			result = append(result, candidate)
		}
	}
	return result, nil
}

func selectPermutation(candidates []gatewaycandidatewindow.Candidate, accountIDs []string, label string) ([]gatewaycandidatewindow.Candidate, error) {
	if len(candidates) != len(accountIDs) {
		return nil, fmt.Errorf("fallback %s order is not a complete candidate permutation", label)
	}
	result, err := selectSubset(candidates, accountIDs, label)
	if err != nil {
		return nil, err
	}
	if len(result) != len(candidates) {
		return nil, fmt.Errorf("fallback %s order is incomplete", label)
	}
	return result, nil
}

func candidateMap(candidates []gatewaycandidatewindow.Candidate) map[string]gatewaycandidatewindow.Candidate {
	result := make(map[string]gatewaycandidatewindow.Candidate, len(candidates))
	for _, candidate := range candidates {
		result[candidate.Projection.AccountID] = candidate
	}
	return result
}

func candidateIDs(candidates []gatewaycandidatewindow.Candidate) []string {
	result := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		result = append(result, candidate.Projection.AccountID)
	}
	return result
}

func copyCandidates(candidates []gatewaycandidatewindow.Candidate) []gatewaycandidatewindow.Candidate {
	return append([]gatewaycandidatewindow.Candidate(nil), candidates...)
}

func cloneRequestShape(input protocolgateway.RequestShape) protocolgateway.RequestShape {
	result := input
	result.Headers = maps.Clone(input.Headers)
	return result
}

func validFinalLane(value gatewayingress.Lane) bool {
	return value == gatewayingress.LaneText || value == gatewayingress.LaneImage
}

var _ gatewayrouteplan.FallbackCandidatePolicy = (*Service)(nil)

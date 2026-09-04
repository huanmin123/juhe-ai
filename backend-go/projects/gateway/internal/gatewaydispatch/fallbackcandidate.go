package gatewaydispatch

import (
	"context"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// resolveNextApiKeyGroupFallbackCandidate, migrated from
// dispatch/api-key-group-fallback-candidate.ts.

// GroupFallbackCandidateOutput mirrors ApiKeyGroupFallbackCandidate.
type GroupFallbackCandidateOutput struct {
	GroupID                    string
	Accounts                   []AccountCandidate
	ResponseInspectionPolicies []gatewayruntimecache.ResponseInspectionPolicySummary
	RoutePlanSnapshot          *gatewayrouting.RoutePlanSnapshot[string]
}

// GroupFallbackArgs mirrors ApiKeyGroupFallbackCandidateInput.
type GroupFallbackArgs struct {
	Req                        *gatewaypreauth.GatewayRequest
	Reason                     string
	APIKeyRecord               *gatewayruntimecache.GatewayAPIKeyRow
	SystemAccountID            string
	GroupID                    string
	RequestLane                string
	RequestClientCompatibility string
	ExcludedAccountIDs         map[string]struct{}
	RoutePlanSnapshot          *gatewayrouting.RoutePlanSnapshot[string]
}

// CanAttemptApiKeyGroupFallback mirrors canAttemptApiKeyGroupFallback.
func CanAttemptApiKeyGroupFallback(
	apiKeyRecord *gatewayruntimecache.GatewayAPIKeyRow,
	groupID string,
	routePlanSnapshot *gatewayrouting.RoutePlanSnapshot[string],
) bool {
	if routePlanSnapshot != nil {
		return routePlanSnapshot.Cursor < len(routePlanSnapshot.OrderedAllowedTargets)-1
	}
	if apiKeyRecord == nil {
		return false
	}
	bindings := apiKeyRecord.GroupBindings
	if len(bindings) <= 1 {
		return false
	}
	currentIndex := -1
	for index, binding := range bindings {
		if binding.GroupID == groupID {
			currentIndex = index
			break
		}
	}
	return currentIndex >= 0 && currentIndex < len(bindings)-1
}

// ResolveNextGroupFallbackCandidate mirrors
// resolveNextApiKeyGroupFallbackCandidate. found=false mirrors undefined.
func (p *CandidatePipeline) ResolveNextGroupFallbackCandidateForArgs(ctx context.Context, input GroupFallbackArgs) (GroupFallbackCandidateOutput, bool, error) {
	var bindings []gatewayruntimecache.GatewayAPIKeyGroupBindingRow
	if input.APIKeyRecord != nil {
		bindings = input.APIKeyRecord.GroupBindings
	}
	routePlanSnapshot := input.RoutePlanSnapshot
	currentIndex := -1
	for index, binding := range bindings {
		if binding.GroupID == input.GroupID {
			currentIndex = index
			break
		}
	}
	allowedBindingIDs := make(map[string]struct{}, len(bindings))
	for _, binding := range bindings {
		if binding.Status == "active" && binding.GroupEnabled != 0 {
			allowedBindingIDs[binding.GroupID] = struct{}{}
		}
	}
	var candidateGroupIDs []string
	if routePlanSnapshot != nil {
		for _, groupID := range routePlanSnapshot.OrderedAllowedTargets[routePlanSnapshot.Cursor+1:] {
			if _, allowed := allowedBindingIDs[groupID]; allowed {
				candidateGroupIDs = append(candidateGroupIDs, groupID)
			}
		}
	} else if currentIndex >= 0 {
		for _, binding := range bindings[currentIndex+1:] {
			candidateGroupIDs = append(candidateGroupIDs, binding.GroupID)
		}
	} else {
		for _, binding := range bindings {
			if binding.GroupID != input.GroupID {
				candidateGroupIDs = append(candidateGroupIDs, binding.GroupID)
			}
		}
	}
	requestedModel, _ := gatewaypreauth.RequestModel(input.Req)
	sourceEndpointFamily := GatewayRequestEndpointFamily(input.Req)
	excludedAccountIDs := input.ExcludedAccountIDs
	if excludedAccountIDs == nil {
		excludedAccountIDs = map[string]struct{}{}
	}
	seenGroupIDs := make(map[string]struct{}, len(candidateGroupIDs))
	for _, candidateGroupID := range candidateGroupIDs {
		if candidateGroupID == "" {
			continue
		}
		if _, dup := seenGroupIDs[candidateGroupID]; dup {
			continue
		}
		seenGroupIDs[candidateGroupID] = struct{}{}
		groupAccess, found, err := p.engine.Cache.ResolveCachedGroupUsageAccessMetadataAsync(ctx, candidateGroupID, input.SystemAccountID)
		if err != nil {
			return GroupFallbackCandidateOutput{}, false, err
		}
		if !found {
			continue
		}
		accounts, err := p.engine.Cache.ListCachedOpenAIAccountsForGroupAsync(ctx, candidateGroupID, input.SystemAccountID, CachedAccountsOptions{
			RequestedModel:          requestedModel,
			RequestedEndpointFamily: sourceEndpointFamily,
		})
		if err != nil {
			return GroupFallbackCandidateOutput{}, false, err
		}
		filtered := make([]AccountCandidate, 0, len(accounts))
		for _, account := range accounts {
			if _, excluded := excludedAccountIDs[account.ID]; excluded {
				continue
			}
			filtered = append(filtered, account)
		}
		accounts = filtered
		if len(accounts) == 0 {
			continue
		}
		capabilityFilter := FilterGatewayAccountsByRequestCapability(input.Req, accounts, p.engine.Driver, input.RequestClientCompatibility, "")
		if len(capabilityFilter.Accounts) == 0 {
			continue
		}
		modelFilter := FilterGatewayAccountsByRequestedModel(capabilityFilter.Accounts, requestedModel, sourceEndpointFamily)
		if len(modelFilter.Accounts) == 0 {
			continue
		}
		quotaDecisions, err := p.engine.Quota.CheckBatchAsync(ctx, groupAccess, modelFilter.Accounts)
		if err != nil {
			return GroupFallbackCandidateOutput{}, false, err
		}
		quotaAllowed := make([]AccountCandidate, 0, len(modelFilter.Accounts))
		for _, account := range modelFilter.Accounts {
			decision, ok := quotaDecisions[account.ID]
			if !ok || decision.Allowed {
				quotaAllowed = append(quotaAllowed, account)
			}
		}
		if len(quotaAllowed) == 0 {
			continue
		}
		degradationOrder := p.engine.Degradation.OrderGatewayAccountsByRuntimeDegradation(quotaAllowed, modelFilter.ModelPriority.RankByAccountID)
		if input.Reason == "runtime_degraded" && degradationOrder.BypassedAllDegraded {
			continue
		}
		orderedQuotaAllowedAccounts := degradationOrder.Accounts
		if (input.Reason == "high_concurrency_group_busy" || input.Reason == "group_capacity_busy") && p.engine.Concurrency != nil {
			busy, err := AreGatewayAccountsCapacityBusyForLaneAsync(ctx, p.engine.Concurrency, orderedQuotaAllowedAccounts, gatewayprotoLane(input.RequestLane), groupAccess.SchedulingPolicy)
			if err != nil {
				return GroupFallbackCandidateOutput{}, false, err
			}
			if busy {
				continue
			}
		}
		output := GroupFallbackCandidateOutput{
			GroupID:                    candidateGroupID,
			Accounts:                   orderedQuotaAllowedAccounts,
			ResponseInspectionPolicies: []gatewayruntimecache.ResponseInspectionPolicySummary{},
			RoutePlanSnapshot:          nil,
		}
		if routePlanSnapshot != nil {
			nextCursor := routePlanSnapshot.Cursor + 1
			for index, target := range routePlanSnapshot.OrderedAllowedTargets {
				if target == candidateGroupID {
					nextCursor = index
					break
				}
			}
			advanced, err := gatewayrouting.AdvanceGatewayRoutePlanCursor(*routePlanSnapshot, nextCursor)
			if err != nil {
				return GroupFallbackCandidateOutput{}, false, err
			}
			output.RoutePlanSnapshot = &advanced
		}
		return output, true, nil
	}
	return GroupFallbackCandidateOutput{}, false, nil
}

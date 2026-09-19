package gatewayrouting

import "context"

// ModelTargetGroupCandidate mirrors GatewayModelTargetGroupCandidate.
type ModelTargetGroupCandidate struct {
	Binding     GroupBindingRow
	GroupAccess GroupUsageAccessMetadata
	Accounts    []UpstreamAccount
	ModelFilter GatewayModelAccountFilterResult
}

// ModelTargetGroupSelection mirrors GatewayModelTargetGroupSelection.
type ModelTargetGroupSelection struct {
	ModelTargetGroupCandidate

	GroupID                    string
	ResponseInspectionPolicies []ResponseInspectionPolicySummary
}

// ModelTargetGroupInput mirrors selectGatewayModelTargetGroup's input.
type ModelTargetGroupInput struct {
	Request                    RequestView
	APIKeyRecord               *APIKeyRow
	Bindings                   []GroupBindingRow
	TargetModel                string
	RequestClientCompatibility string
	// AcceptCandidate mirrors input.acceptCandidate; nil accepts everything.
	AcceptCandidate func(candidate ModelTargetGroupCandidate) bool
	// CandidatePriority mirrors input.candidatePriority; when nil the first
	// viable candidate is returned immediately (Node `if
	// (!input.candidatePriority) return selection`).
	CandidatePriority func(candidate ModelTargetGroupCandidate) float64
}

// TargetGroupSelector mirrors selectGatewayModelTargetGroup: walk the
// (already dispatch-ordered) bindings, keep the first/priority-best group
// whose cached accounts survive the capability and model filters.
type TargetGroupSelector struct {
	RuntimeCache     RuntimeCacheReader
	CapabilityFilter AccountCapabilityFilter
}

// SelectGatewayModelTargetGroup mirrors selectGatewayModelTargetGroup.
func (s *TargetGroupSelector) SelectGatewayModelTargetGroup(ctx context.Context, input ModelTargetGroupInput) (*ModelTargetGroupSelection, error) {
	sourceEndpointFamily := input.Request.requestEndpointFamily()
	var selected *ModelTargetGroupSelection
	selectedPriority := negInf()
	for _, binding := range uniqueGatewayGroupBindings(input.Bindings) {
		candidate, ok, err := s.resolveModelTargetGroupCandidate(ctx, input, binding, sourceEndpointFamily)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if input.AcceptCandidate != nil && !input.AcceptCandidate(candidate) {
			continue
		}
		selection := &ModelTargetGroupSelection{
			ModelTargetGroupCandidate:  candidate,
			GroupID:                    binding.GroupID,
			ResponseInspectionPolicies: []ResponseInspectionPolicySummary{},
		}
		if input.CandidatePriority == nil {
			return selection, nil
		}
		priority := input.CandidatePriority(candidate)
		if priority > selectedPriority {
			selected = selection
			selectedPriority = priority
		}
	}
	return selected, nil
}

// CollectGatewayModelGroupSegments walks the same per-group skeleton as
// SelectGatewayModelTargetGroup but keeps every group's surviving candidate
// instead of selecting one group (merge mode, 合并路由设计 3.1). Order
// follows the (already dispatch-ordered) bindings; groups without access
// metadata or without an account surviving the capability and model filters
// are omitted.
func (s *TargetGroupSelector) CollectGatewayModelGroupSegments(ctx context.Context, input ModelTargetGroupInput) ([]ModelTargetGroupCandidate, error) {
	sourceEndpointFamily := input.Request.requestEndpointFamily()
	candidates := make([]ModelTargetGroupCandidate, 0, len(input.Bindings))
	for _, binding := range uniqueGatewayGroupBindings(input.Bindings) {
		candidate, ok, err := s.resolveModelTargetGroupCandidate(ctx, input, binding, sourceEndpointFamily)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		candidates = append(candidates, candidate)
	}
	return candidates, nil
}

// resolveModelTargetGroupCandidate resolves one binding group into a
// ModelTargetGroupCandidate through the shared walk: access metadata →
// cached accounts → capability filter → model filter. ok=false mirrors the
// selector's per-group `continue` branches.
func (s *TargetGroupSelector) resolveModelTargetGroupCandidate(ctx context.Context, input ModelTargetGroupInput, binding GroupBindingRow, sourceEndpointFamily string) (ModelTargetGroupCandidate, bool, error) {
	groupAccess, found, err := s.RuntimeCache.ResolveCachedGroupUsageAccessMetadataAsync(ctx, binding.GroupID, input.APIKeyRecord.SystemAccountID)
	if err != nil {
		return ModelTargetGroupCandidate{}, false, err
	}
	if !found {
		return ModelTargetGroupCandidate{}, false, nil
	}
	accounts, err := s.RuntimeCache.ListCachedOpenAIAccountsForGroupAsync(ctx, binding.GroupID, input.APIKeyRecord.SystemAccountID, CachedAccountsForGroupOptions{
		RequestedModel:          input.TargetModel,
		RequestedEndpointFamily: sourceEndpointFamily,
	})
	if err != nil {
		return ModelTargetGroupCandidate{}, false, err
	}
	if len(accounts) == 0 {
		return ModelTargetGroupCandidate{}, false, nil
	}
	capabilityFilter := s.CapabilityFilter.FilterAccountsByRequestCapability(ctx, accounts, CapabilityFilterInput{
		RequestModel:               input.TargetModel,
		RequestClientCompatibility: input.RequestClientCompatibility,
	})
	if len(capabilityFilter.Accounts) == 0 {
		return ModelTargetGroupCandidate{}, false, nil
	}
	modelFilter := FilterAccountsByRequestedModel(capabilityFilter.Accounts, input.TargetModel, sourceEndpointFamily)
	if len(modelFilter.Accounts) == 0 {
		return ModelTargetGroupCandidate{}, false, nil
	}
	return ModelTargetGroupCandidate{
		Binding:     binding,
		GroupAccess: groupAccess,
		Accounts:    modelFilter.Accounts,
		ModelFilter: modelFilter,
	}, true, nil
}

// uniqueGatewayGroupBindings mirrors uniqueGatewayGroupBindings: drop empty
// group ids and duplicates, preserving first-seen order.
func uniqueGatewayGroupBindings(bindings []GroupBindingRow) []GroupBindingRow {
	seen := make(map[string]struct{}, len(bindings))
	unique := make([]GroupBindingRow, 0, len(bindings))
	for _, binding := range bindings {
		if binding.GroupID == "" {
			continue
		}
		if _, ok := seen[binding.GroupID]; ok {
			continue
		}
		seen[binding.GroupID] = struct{}{}
		unique = append(unique, binding)
	}
	return unique
}

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
	Request                     RequestView
	APIKeyRecord                *APIKeyRow
	Bindings                    []GroupBindingRow
	TargetModel                 string
	RequestClientCompatibility  string
	// AcceptCandidate mirrors input.acceptCandidate; nil accepts everything.
	AcceptCandidate             func(candidate ModelTargetGroupCandidate) bool
	// CandidatePriority mirrors input.candidatePriority; when nil the first
	// viable candidate is returned immediately (Node `if
	// (!input.candidatePriority) return selection`).
	CandidatePriority           func(candidate ModelTargetGroupCandidate) float64
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
		groupAccess, found, err := s.RuntimeCache.ResolveCachedGroupUsageAccessMetadataAsync(ctx, binding.GroupID, input.APIKeyRecord.SystemAccountID)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		accounts, err := s.RuntimeCache.ListCachedOpenAIAccountsForGroupAsync(ctx, binding.GroupID, input.APIKeyRecord.SystemAccountID, CachedAccountsForGroupOptions{
			RequestedModel:          input.TargetModel,
			RequestedEndpointFamily: sourceEndpointFamily,
		})
		if err != nil {
			return nil, err
		}
		if len(accounts) == 0 {
			continue
		}
		capabilityFilter := s.CapabilityFilter.FilterAccountsByRequestCapability(ctx, accounts, CapabilityFilterInput{
			RequestModel:               input.TargetModel,
			RequestClientCompatibility: input.RequestClientCompatibility,
		})
		if len(capabilityFilter.Accounts) == 0 {
			continue
		}
		modelFilter := FilterAccountsByRequestedModel(capabilityFilter.Accounts, input.TargetModel, sourceEndpointFamily)
		if len(modelFilter.Accounts) == 0 {
			continue
		}
		candidate := ModelTargetGroupCandidate{
			Binding:     binding,
			GroupAccess: groupAccess,
			Accounts:    modelFilter.Accounts,
			ModelFilter: modelFilter,
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

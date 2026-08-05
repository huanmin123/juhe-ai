package gatewayrouteplan

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
	"juhe-ai/backend-go/internal/modules/gatewayfallbackreason"
	"juhe-ai/backend-go/internal/modules/gatewayingress"
	"juhe-ai/backend-go/internal/modules/gatewaypreflight"
	protocolgateway "juhe-ai/backend-go/internal/protocols/gateway"
)

// FallbackCursor proves one entered binding belongs to one immutable route
// plan. Its fields remain opaque so a caller cannot retarget a later group by
// changing only an index or group ID.
type FallbackCursor struct {
	revision           string
	dispatchGeneration int64
	position           int
	bindingID          string
	groupID            string
}

func (c FallbackCursor) BindingID() string { return c.bindingID }
func (c FallbackCursor) GroupID() string   { return c.groupID }

// FallbackTarget is only a route-level handoff. It does not contain a
// candidate window, capacity decision, lease, or runnable execution. A later
// owner must use Binding with a fresh target-group preparation before running
// any attempt.
type FallbackTarget struct {
	binding gatewaypreflight.Binding
	cursor  FallbackCursor
}

func (t FallbackTarget) Binding() gatewaypreflight.Binding { return t.binding }
func (t FallbackTarget) Cursor() FallbackCursor            { return t.cursor }

// FallbackPreparedInput carries only the immutable route handoff and the
// canonical request model facts needed to refresh a target candidate window.
// It deliberately has no source window, client-IP lease, attempt lifecycle, or
// response state: none of those may cross a route-group fallback boundary.
type FallbackPreparedInput struct {
	Route           RouteOnlyResult
	Current         FallbackCursor
	EnteredGroupIDs []string
	RequestedModel  string
	EndpointFamily  string
}

// FallbackCandidatePolicy receives one freshly hydrated target window and
// returns the account IDs that remain dispatchable for this request. A policy
// is mandatory for the dispatch-ready path: fresh hydration alone cannot
// prove request capability, authorization quota, runtime degradation, or
// reason-specific capacity semantics.
type FallbackCandidatePolicy interface {
	SelectFallbackCandidates(context.Context, FallbackCandidatePolicyInput) (FallbackCandidatePolicyResult, error)
}

// FallbackCandidatePolicyInput is a detached request-local target view. The
// route planner owns target/window construction; a policy can only select
// account IDs from this exact fresh window, never introduce a new candidate.
type FallbackCandidatePolicyInput struct {
	Target                     FallbackTarget
	Window                     gatewaycandidatewindow.Window
	Intent                     gatewayingress.RequestIntent
	IngressFinalization        gatewayingress.FinalResult
	RequestShape               protocolgateway.RequestShape
	Protocol                   protocolgateway.ProtocolCode
	FinalLane                  gatewayingress.Lane
	RequestedModel             string
	EndpointFamily             string
	Reason                     string
	RequestClientCompatibility string
	RequestLane                string
	ExcludedAccountIDs         []string
}

// FallbackCandidatePolicyResult preserves policy ordering by account ID. An
// empty list means this group is ineligible and the route planner must scan a
// later group. It is not a successful empty execution batch.
type FallbackCandidatePolicyResult struct {
	CandidateAccountIDs []string
}

// FallbackDispatchPreparedInput adds the request facts which Node applies
// after fresh target hydration. Policy is deliberately required so callers
// cannot accidentally dispatch a target that skipped those gates.
type FallbackDispatchPreparedInput struct {
	FallbackPreparedInput
	Intent                     gatewayingress.RequestIntent
	IngressFinalization        gatewayingress.FinalResult
	RequestShape               protocolgateway.RequestShape
	Protocol                   protocolgateway.ProtocolCode
	FinalLane                  gatewayingress.Lane
	Reason                     string
	RequestClientCompatibility string
	RequestLane                string
	ExcludedAccountIDs         []string
	Policy                     FallbackCandidatePolicy
}

// FallbackDispatchPreparedTarget is a distinct, opaque handoff that proves a
// fresh target completed the dispatch-time policy gates for one immutable
// request. It intentionally cannot be passed to the legacy fresh-only
// extraction path.
type FallbackDispatchPreparedTarget struct {
	prepared FallbackPreparedTarget
	proof    fallbackDispatchProof
}

type fallbackDispatchProof struct {
	current                    FallbackCursor
	enteredGroupIDs            []string
	intent                     gatewayingress.RequestIntent
	ingressFinalization        gatewayingress.FinalResult
	requestShape               protocolgateway.RequestShape
	protocol                   protocolgateway.ProtocolCode
	finalLane                  gatewayingress.Lane
	requestedModel             string
	endpointFamily             string
	requestClientCompatibility string
	reason                     string
	excludedAccountIDs         []string
}

func (t FallbackDispatchPreparedTarget) Found() bool { return t.prepared.Found() }

func (t FallbackDispatchPreparedTarget) SkippedGroupIDs() []string {
	return t.prepared.SkippedGroupIDs()
}

// FallbackPreparedTarget is the first later target whose candidate window was
// freshly loaded for this request. SkippedGroupIDs records only route groups
// examined during this preparation; they were not entered and must not be
// presented as entered groups on a later fallback.
type FallbackPreparedTarget struct {
	fence   RouteFence
	target  FallbackTarget
	window  gatewaycandidatewindow.Window
	found   bool
	skipped []string
}

func (t FallbackPreparedTarget) Found() bool { return t.found }

func (t FallbackPreparedTarget) SkippedGroupIDs() []string {
	return append([]string(nil), t.skipped...)
}

// RouteFence freezes the route-plan facts that must remain identical across a
// request's cross-group handoffs. Its fields are intentionally opaque: only
// this package can mint a fence from a validated route result.
type RouteFence struct {
	revision           string
	dispatchGeneration int64
	systemAccountID    string
	routeStrategyID    string
	mode               string
	orderedBindingIDs  []string
}

// RouteOnlyFromResult reconstructs the candidate-free form of an already
// validated route result without allowing callers to change its binding order.
func RouteOnlyFromResult(result Result) (RouteOnlyResult, error) {
	route := RouteOnlyResult{Preflight: result.Preflight, Plan: result.Plan, OrderedBindings: make([]gatewaypreflight.Binding, len(result.Groups))}
	for index, group := range result.Groups {
		route.OrderedBindings[index] = group.Binding
	}
	if err := validateRouteOnlyResult(route); err != nil {
		return RouteOnlyResult{}, err
	}
	return route, nil
}

// NewRouteFence records the complete immutable identity of one route plan.
func NewRouteFence(route RouteOnlyResult) (RouteFence, error) {
	if err := validateRouteOnlyResult(route); err != nil {
		return RouteFence{}, err
	}
	if strings.TrimSpace(route.Plan.Revision) == "" {
		return RouteFence{}, fmt.Errorf("route fence revision is required")
	}
	fence := RouteFence{
		revision: route.Plan.Revision, dispatchGeneration: route.Plan.DispatchGeneration,
		systemAccountID: route.Plan.Scope.SystemAccountID, routeStrategyID: route.Plan.Scope.RouteStrategyID,
		mode: string(route.Plan.Mode), orderedBindingIDs: make([]string, len(route.OrderedBindings)),
	}
	for index, binding := range route.OrderedBindings {
		fence.orderedBindingIDs[index] = binding.ID()
	}
	return fence, nil
}

// ValidateRouteFence rejects a route that is not the exact plan frozen into a
// source execution or freshly prepared target handoff.
func ValidateRouteFence(route RouteOnlyResult, fence RouteFence) error {
	actual, err := NewRouteFence(route)
	if err != nil {
		return err
	}
	if actual.revision != fence.revision || actual.dispatchGeneration != fence.dispatchGeneration ||
		actual.systemAccountID != fence.systemAccountID || actual.routeStrategyID != fence.routeStrategyID ||
		actual.mode != fence.mode || len(actual.orderedBindingIDs) != len(fence.orderedBindingIDs) {
		return fmt.Errorf("route fence does not match route plan")
	}
	for index, bindingID := range actual.orderedBindingIDs {
		if bindingID != fence.orderedBindingIDs[index] {
			return fmt.Errorf("route fence binding order does not match route plan")
		}
	}
	return nil
}

// InitialFallbackCursor binds the actually entered route binding to its
// immutable coordinator plan. The caller must keep this cursor rather than
// infer a fallback from a group ID, because repeated aliases may refer to the
// same group at different route positions.
func InitialFallbackCursor(route RouteOnlyResult, bindingID string) (FallbackCursor, error) {
	if err := validateRouteOnlyResult(route); err != nil {
		return FallbackCursor{}, fmt.Errorf("validate fallback route: %w", err)
	}
	if strings.TrimSpace(route.Plan.Revision) == "" {
		return FallbackCursor{}, fmt.Errorf("fallback route plan revision is required")
	}
	bindingID = strings.TrimSpace(bindingID)
	if bindingID == "" {
		return FallbackCursor{}, fmt.Errorf("fallback current binding is required")
	}
	for position, binding := range route.OrderedBindings {
		if binding.ID() == bindingID {
			return fallbackCursor(route, position), nil
		}
	}
	return FallbackCursor{}, fmt.Errorf("fallback current binding is not in route plan")
}

// NextFallbackTarget returns the first later, not-yet-entered group in the
// same ordered plan. It never wraps, reorders, or reuses an earlier group:
// those would diverge from Node's route-plan cursor semantics. A false result
// means this route plan has no legal next target; it is not permission to
// reuse a later frozen execution batch.
func NextFallbackTarget(route RouteOnlyResult, current FallbackCursor, enteredGroupIDs []string) (FallbackTarget, bool, error) {
	if err := validateFallbackCursor(route, current); err != nil {
		return FallbackTarget{}, false, err
	}
	entered, err := enteredGroups(enteredGroupIDs, current, route.OrderedBindings)
	if err != nil {
		return FallbackTarget{}, false, err
	}
	for position := current.position + 1; position < len(route.OrderedBindings); position++ {
		binding := route.OrderedBindings[position]
		if _, alreadyEntered := entered[binding.GroupID()]; alreadyEntered {
			continue
		}
		return FallbackTarget{binding: binding, cursor: fallbackCursor(route, position)}, true, nil
	}
	return FallbackTarget{}, false, nil
}

// PrepareFallbackTarget mirrors Node's forward candidate selection boundary:
// it walks only later route groups, refreshes each candidate window using the
// original model/endpoint facts, and returns the first non-empty target. It
// never advances route coordination, reuses an earlier window, performs a
// capacity decision, acquires a lease, or dispatches an attempt.
func (s *Service) PrepareFallbackTarget(ctx context.Context, input FallbackPreparedInput) (FallbackPreparedTarget, error) {
	if s == nil || s.candidates == nil {
		return FallbackPreparedTarget{}, fmt.Errorf("gateway fallback target candidate loader is not configured")
	}
	if ctx == nil {
		return FallbackPreparedTarget{}, fmt.Errorf("gateway fallback target context is required")
	}
	requestedModel := strings.TrimSpace(input.RequestedModel)
	endpointFamily := strings.TrimSpace(input.EndpointFamily)
	if requestedModel == "" || endpointFamily == "" {
		return FallbackPreparedTarget{}, fmt.Errorf("gateway fallback target model and endpoint family are required")
	}
	if err := validateFallbackCursor(input.Route, input.Current); err != nil {
		return FallbackPreparedTarget{}, err
	}
	fence, err := NewRouteFence(input.Route)
	if err != nil {
		return FallbackPreparedTarget{}, err
	}
	apiKey, ok := input.Route.Preflight.APIKey()
	if !ok {
		return FallbackPreparedTarget{}, fmt.Errorf("fallback route has no API key")
	}

	current := input.Current
	scanEntered := append([]string(nil), input.EnteredGroupIDs...)
	skipped := make([]string, 0, len(input.Route.OrderedBindings))
	for {
		target, found, err := NextFallbackTarget(input.Route, current, scanEntered)
		if err != nil {
			return FallbackPreparedTarget{}, err
		}
		if !found {
			return FallbackPreparedTarget{fence: fence, skipped: skipped}, nil
		}
		window, windowFound, loadErr := s.candidates.Load(ctx, gatewaycandidatewindow.LoadInput{
			GroupID: target.Binding().GroupID(), SystemAccountID: apiKey.SystemAccountID(),
			RequestedModel: requestedModel, EndpointFamily: endpointFamily,
		})
		if loadErr != nil {
			return FallbackPreparedTarget{}, fmt.Errorf("load fallback candidates for route binding %q: %w", target.Binding().ID(), loadErr)
		}
		if !windowFound || len(window.Candidates) == 0 {
			skipped = append(skipped, target.Binding().GroupID())
			scanEntered = append(scanEntered, target.Binding().GroupID())
			current = target.Cursor()
			continue
		}
		if err := validateFallbackWindow(target, apiKey, window); err != nil {
			return FallbackPreparedTarget{}, err
		}
		return FallbackPreparedTarget{fence: fence, target: target, window: window, found: true, skipped: skipped}, nil
	}
}

// PrepareDispatchFallbackTarget reproduces Node's target-group scan shape:
// every later group is freshly hydrated, then passed through the caller's
// complete request policy. A policy-rejected group advances the scan without
// becoming entered; a policy error fails the whole preparation closed.
func (s *Service) PrepareDispatchFallbackTarget(ctx context.Context, input FallbackDispatchPreparedInput) (FallbackDispatchPreparedTarget, error) {
	if input.Policy == nil {
		return FallbackDispatchPreparedTarget{}, fmt.Errorf("gateway fallback dispatch policy is required")
	}
	if s == nil || s.candidates == nil {
		return FallbackDispatchPreparedTarget{}, fmt.Errorf("gateway fallback target candidate loader is not configured")
	}
	if ctx == nil {
		return FallbackDispatchPreparedTarget{}, fmt.Errorf("gateway fallback target context is required")
	}
	requestedModel := strings.TrimSpace(input.RequestedModel)
	endpointFamily := strings.TrimSpace(input.EndpointFamily)
	if requestedModel == "" || endpointFamily == "" {
		return FallbackDispatchPreparedTarget{}, fmt.Errorf("gateway fallback target model and endpoint family are required")
	}
	proof, proofErr := newFallbackDispatchProof(input, requestedModel, endpointFamily)
	if proofErr != nil {
		return FallbackDispatchPreparedTarget{}, proofErr
	}
	if err := validateFallbackCursor(input.Route, input.Current); err != nil {
		return FallbackDispatchPreparedTarget{}, err
	}
	fence, err := NewRouteFence(input.Route)
	if err != nil {
		return FallbackDispatchPreparedTarget{}, err
	}
	apiKey, ok := input.Route.Preflight.APIKey()
	if !ok {
		return FallbackDispatchPreparedTarget{}, fmt.Errorf("fallback route has no API key")
	}

	current := input.Current
	scanEntered := append([]string(nil), input.EnteredGroupIDs...)
	skipped := make([]string, 0, len(input.Route.OrderedBindings))
	for {
		target, found, nextErr := NextFallbackTarget(input.Route, current, scanEntered)
		if nextErr != nil {
			return FallbackDispatchPreparedTarget{}, nextErr
		}
		if !found {
			return FallbackDispatchPreparedTarget{prepared: FallbackPreparedTarget{fence: fence, skipped: skipped}, proof: proof}, nil
		}
		window, windowFound, loadErr := s.candidates.Load(ctx, gatewaycandidatewindow.LoadInput{
			GroupID: target.Binding().GroupID(), SystemAccountID: apiKey.SystemAccountID(),
			RequestedModel: requestedModel, EndpointFamily: endpointFamily,
		})
		if loadErr != nil {
			return FallbackDispatchPreparedTarget{}, fmt.Errorf("load fallback candidates for route binding %q: %w", target.Binding().ID(), loadErr)
		}
		if !windowFound || len(window.Candidates) == 0 {
			skipped = append(skipped, target.Binding().GroupID())
			scanEntered = append(scanEntered, target.Binding().GroupID())
			current = target.Cursor()
			continue
		}
		if err := validateFallbackWindow(target, apiKey, window); err != nil {
			return FallbackDispatchPreparedTarget{}, err
		}
		policyWindow := gatewaycandidatewindow.PolicyWindow(window)
		selection, policyErr := input.Policy.SelectFallbackCandidates(ctx, FallbackCandidatePolicyInput{
			Target: target, Window: policyWindow, Intent: proof.intent, IngressFinalization: proof.ingressFinalization, RequestShape: cloneFallbackRequestShape(proof.requestShape), Protocol: proof.protocol, FinalLane: proof.finalLane,
			RequestedModel: requestedModel, EndpointFamily: endpointFamily,
			Reason: strings.TrimSpace(input.Reason), RequestClientCompatibility: strings.TrimSpace(input.RequestClientCompatibility),
			RequestLane: strings.TrimSpace(input.RequestLane), ExcludedAccountIDs: append([]string(nil), input.ExcludedAccountIDs...),
		})
		if policyErr != nil {
			return FallbackDispatchPreparedTarget{}, fmt.Errorf("select fallback candidates for route binding %q: %w", target.Binding().ID(), policyErr)
		}
		selected, selectErr := selectFallbackCandidates(window, selection.CandidateAccountIDs)
		if selectErr != nil {
			return FallbackDispatchPreparedTarget{}, selectErr
		}
		if len(selected) == 0 {
			skipped = append(skipped, target.Binding().GroupID())
			scanEntered = append(scanEntered, target.Binding().GroupID())
			current = target.Cursor()
			continue
		}
		window.Candidates = selected
		return FallbackDispatchPreparedTarget{prepared: FallbackPreparedTarget{fence: fence, target: target, window: window, found: true, skipped: skipped}, proof: proof}, nil
	}
}

func selectFallbackCandidates(window gatewaycandidatewindow.Window, accountIDs []string) ([]gatewaycandidatewindow.Candidate, error) {
	byID := make(map[string]gatewaycandidatewindow.Candidate, len(window.Candidates))
	for _, candidate := range window.Candidates {
		accountID := strings.TrimSpace(candidate.Projection.AccountID)
		if accountID == "" {
			return nil, fmt.Errorf("fallback target candidate has no account id")
		}
		if _, exists := byID[accountID]; exists {
			return nil, fmt.Errorf("fallback target candidate account id is duplicated: %q", accountID)
		}
		byID[accountID] = candidate
	}
	selected := make([]gatewaycandidatewindow.Candidate, 0, len(accountIDs))
	seen := make(map[string]struct{}, len(accountIDs))
	for _, accountID := range accountIDs {
		accountID = strings.TrimSpace(accountID)
		if accountID == "" {
			return nil, fmt.Errorf("fallback policy selected an empty account id")
		}
		if _, duplicate := seen[accountID]; duplicate {
			return nil, fmt.Errorf("fallback policy selected a duplicate account id: %q", accountID)
		}
		candidate, exists := byID[accountID]
		if !exists {
			return nil, fmt.Errorf("fallback policy selected an account outside the fresh window: %q", accountID)
		}
		seen[accountID] = struct{}{}
		selected = append(selected, candidate)
	}
	return selected, nil
}

func newFallbackDispatchProof(input FallbackDispatchPreparedInput, requestedModel, endpointFamily string) (fallbackDispatchProof, error) {
	if !gatewayfallbackreason.Valid(input.Reason) || strings.TrimSpace(input.RequestClientCompatibility) == "" || strings.TrimSpace(input.RequestLane) == "" {
		return fallbackDispatchProof{}, fmt.Errorf("gateway fallback dispatch request facts are required")
	}
	if !input.Intent.Parsed() || input.Intent.Model() != requestedModel || input.Intent.Stream() != input.RequestShape.Stream ||
		input.IngressFinalization.FinalLane() != input.FinalLane || input.IngressFinalization.SnapshotRevision() == "" || input.IngressFinalization.CandidateCapacity() < 1 ||
		input.Protocol == "" || !validFallbackLane(input.FinalLane) ||
		strings.TrimSpace(input.RequestShape.Model) == "" || input.RequestShape.Model != requestedModel {
		return fallbackDispatchProof{}, fmt.Errorf("gateway fallback dispatch request shape is invalid")
	}
	if actual := protocolgateway.EndpointFamilyFromPath(input.Protocol, input.RequestShape.Path); string(actual) != endpointFamily {
		return fallbackDispatchProof{}, fmt.Errorf("gateway fallback dispatch endpoint family does not match request shape")
	}
	if input.RequestLane != string(input.FinalLane) {
		return fallbackDispatchProof{}, fmt.Errorf("gateway fallback dispatch request lane does not match final lane")
	}
	entered, err := canonicalFallbackGroupIDs(input.EnteredGroupIDs)
	if err != nil {
		return fallbackDispatchProof{}, err
	}
	excluded, err := canonicalFallbackAccountIDs(input.ExcludedAccountIDs)
	if err != nil {
		return fallbackDispatchProof{}, err
	}
	return fallbackDispatchProof{
		current: input.Current, enteredGroupIDs: entered, intent: input.Intent, ingressFinalization: input.IngressFinalization, requestShape: cloneFallbackRequestShape(input.RequestShape), protocol: input.Protocol,
		finalLane: input.FinalLane, requestedModel: requestedModel, endpointFamily: endpointFamily,
		requestClientCompatibility: strings.TrimSpace(input.RequestClientCompatibility), reason: strings.TrimSpace(input.Reason),
		excludedAccountIDs: excluded,
	}, nil
}

// ValidateFallbackDispatchPreparedTarget extracts a target only when it was
// prepared for this exact finalized request. Unlike the legacy fresh-only
// handoff, it binds target selection to protocol shape, final lane,
// compatibility, reason, excluded accounts and source cursor.
func ValidateFallbackDispatchPreparedTarget(route RouteOnlyResult, input FallbackDispatchPreparedInput, prepared FallbackDispatchPreparedTarget) (FallbackTarget, gatewaycandidatewindow.Window, bool, error) {
	requestedModel := strings.TrimSpace(input.RequestedModel)
	endpointFamily := strings.TrimSpace(input.EndpointFamily)
	proof, err := newFallbackDispatchProof(input, requestedModel, endpointFamily)
	if err != nil {
		return FallbackTarget{}, gatewaycandidatewindow.Window{}, false, err
	}
	if !sameFallbackDispatchProof(proof, prepared.proof) {
		return FallbackTarget{}, gatewaycandidatewindow.Window{}, false, fmt.Errorf("fallback dispatch prepared target does not match request proof")
	}
	return ValidateFallbackPreparedTarget(route, input.Current, prepared.prepared)
}

func sameFallbackDispatchProof(left, right fallbackDispatchProof) bool {
	return left.current == right.current && left.protocol == right.protocol && left.finalLane == right.finalLane &&
		left.requestedModel == right.requestedModel && left.endpointFamily == right.endpointFamily &&
		left.requestClientCompatibility == right.requestClientCompatibility && left.reason == right.reason &&
		reflect.DeepEqual(left.intent, right.intent) && reflect.DeepEqual(left.ingressFinalization, right.ingressFinalization) && reflect.DeepEqual(left.requestShape, right.requestShape) && slices.Equal(left.enteredGroupIDs, right.enteredGroupIDs) &&
		slices.Equal(left.excludedAccountIDs, right.excludedAccountIDs)
}

func canonicalFallbackGroupIDs(values []string) ([]string, error) {
	return canonicalFallbackIDs(values, "entered fallback group")
}

func canonicalFallbackAccountIDs(values []string) ([]string, error) {
	return canonicalFallbackIDs(values, "excluded fallback account")
}

func canonicalFallbackIDs(values []string, label string) ([]string, error) {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, fmt.Errorf("%s is empty", label)
		}
		if _, exists := seen[value]; exists {
			return nil, fmt.Errorf("%s is duplicated: %q", label, value)
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	slices.Sort(result)
	return result, nil
}

func cloneFallbackRequestShape(input protocolgateway.RequestShape) protocolgateway.RequestShape {
	result := input
	result.Headers = maps.Clone(input.Headers)
	return result
}

func validFallbackLane(value gatewayingress.Lane) bool {
	return value == gatewayingress.LaneText || value == gatewayingress.LaneImage
}

// ValidateFallbackPreparedTarget is the only extraction boundary for a fresh
// handoff. Because FallbackPreparedTarget's payload is private, callers cannot
// wrap a source batch as a target window or splice a result from another plan.
func ValidateFallbackPreparedTarget(route RouteOnlyResult, current FallbackCursor, prepared FallbackPreparedTarget) (FallbackTarget, gatewaycandidatewindow.Window, bool, error) {
	if err := ValidateRouteFence(route, prepared.fence); err != nil {
		return FallbackTarget{}, gatewaycandidatewindow.Window{}, false, err
	}
	if err := ValidateFallbackCursor(route, current); err != nil {
		return FallbackTarget{}, gatewaycandidatewindow.Window{}, false, err
	}
	if !prepared.found {
		return FallbackTarget{}, gatewaycandidatewindow.Window{}, false, nil
	}
	if err := ValidateFallbackTransition(route, current, prepared.target); err != nil {
		return FallbackTarget{}, gatewaycandidatewindow.Window{}, false, err
	}
	apiKey, ok := route.Preflight.APIKey()
	if !ok {
		return FallbackTarget{}, gatewaycandidatewindow.Window{}, false, fmt.Errorf("fallback route has no API key")
	}
	if len(prepared.window.Candidates) == 0 || validateFallbackWindow(prepared.target, apiKey, prepared.window) != nil {
		return FallbackTarget{}, gatewaycandidatewindow.Window{}, false, fmt.Errorf("fallback prepared target window is invalid")
	}
	return prepared.target, prepared.window, true, nil
}

// ValidateFallbackTarget lets a target preparation owner verify the opaque
// handoff again before it performs I/O. It rejects a route revision, dispatch
// generation, binding, or position from another request plan.
func ValidateFallbackTarget(route RouteOnlyResult, target FallbackTarget) error {
	if err := validateFallbackCursor(route, target.cursor); err != nil {
		return err
	}
	binding := route.OrderedBindings[target.cursor.position]
	if !sameBinding(target.binding, binding) {
		return fmt.Errorf("fallback target binding does not match route plan")
	}
	return nil
}

// ValidateFallbackCursor checks that a current cursor still belongs to the
// immutable route plan before an owner interprets an explicit no-target result.
func ValidateFallbackCursor(route RouteOnlyResult, current FallbackCursor) error {
	return validateFallbackCursor(route, current)
}

// ValidateFallbackTransition proves that target advances from current within
// the same immutable route plan. It permits skipped groups between the two
// cursors, but never an equal, earlier, or cross-plan target.
func ValidateFallbackTransition(route RouteOnlyResult, current FallbackCursor, target FallbackTarget) error {
	if err := validateFallbackCursor(route, current); err != nil {
		return err
	}
	if err := ValidateFallbackTarget(route, target); err != nil {
		return err
	}
	if target.cursor.position <= current.position {
		return fmt.Errorf("fallback target does not advance route cursor")
	}
	return nil
}

func validateFallbackWindow(target FallbackTarget, apiKey gatewaypreflight.APIKey, window gatewaycandidatewindow.Window) error {
	binding := target.Binding()
	if strings.TrimSpace(window.Access.GroupID) != binding.GroupID() ||
		strings.TrimSpace(window.Access.CallerSystemAccountID) != apiKey.SystemAccountID() ||
		strings.TrimSpace(window.Access.GroupType) == "" ||
		(binding.ProviderCode() != "" && window.Access.ProviderCode != binding.ProviderCode()) {
		return fmt.Errorf("fallback target candidate window does not match route scope")
	}
	return nil
}

func fallbackCursor(route RouteOnlyResult, position int) FallbackCursor {
	binding := route.OrderedBindings[position]
	return FallbackCursor{
		revision: route.Plan.Revision, dispatchGeneration: route.Plan.DispatchGeneration,
		position: position, bindingID: binding.ID(), groupID: binding.GroupID(),
	}
}

func validateFallbackCursor(route RouteOnlyResult, current FallbackCursor) error {
	if err := validateRouteOnlyResult(route); err != nil {
		return fmt.Errorf("validate fallback route: %w", err)
	}
	if current.position < 0 || current.position >= len(route.OrderedBindings) || current.revision == "" ||
		current.revision != route.Plan.Revision || current.dispatchGeneration != route.Plan.DispatchGeneration {
		return fmt.Errorf("fallback cursor does not match route plan")
	}
	binding := route.OrderedBindings[current.position]
	if current.bindingID != binding.ID() || current.groupID != binding.GroupID() {
		return fmt.Errorf("fallback cursor binding does not match route plan")
	}
	return nil
}

func enteredGroups(values []string, current FallbackCursor, bindings []gatewaypreflight.Binding) (map[string]struct{}, error) {
	if strings.TrimSpace(current.groupID) == "" || current.position < 0 || len(bindings) == 0 || len(values) == 0 || len(values) > len(bindings) {
		return nil, fmt.Errorf("fallback entered groups are invalid")
	}
	legal := make(map[string]struct{}, current.position+1)
	for position := 0; position <= current.position; position++ {
		legal[bindings[position].GroupID()] = struct{}{}
	}
	entered := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, fmt.Errorf("fallback entered group is required")
		}
		if _, duplicate := entered[value]; duplicate {
			return nil, fmt.Errorf("fallback entered group is duplicated")
		}
		if _, valid := legal[value]; !valid {
			return nil, fmt.Errorf("fallback entered group is not before current cursor")
		}
		entered[value] = struct{}{}
	}
	if _, currentEntered := entered[current.groupID]; !currentEntered {
		return nil, fmt.Errorf("fallback entered groups do not include current group")
	}
	return entered, nil
}

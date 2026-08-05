// Package gatewayrequestexecution joins canonical request preparation with an
// already-authenticated route plan into a read-only execution plan. It has no
// HTTP, body, credential, lease, slot, circuit, audit, usage, or owner side
// effects; a future gateway owner must revalidate a candidate at its final
// claim boundary before it sends credentials upstream.
package gatewayrequestexecution

import (
	"reflect"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
	"juhe-ai/backend-go/internal/modules/gatewayingress"
	"juhe-ai/backend-go/internal/modules/gatewayrequestorchestration"
	"juhe-ai/backend-go/internal/modules/gatewayrequestprep"
	"juhe-ai/backend-go/internal/modules/gatewayroutecoordination"
	"juhe-ai/backend-go/internal/modules/gatewayrouteplan"
	"juhe-ai/backend-go/internal/modules/gatewayrouting"
	"juhe-ai/backend-go/internal/modules/gatewaystreamrelay"
	protocolgateway "juhe-ai/backend-go/internal/protocols/gateway"
)

const (
	// MaxIdentityLength bounds opaque tracing and idempotency identifiers. The
	// values are never interpreted, persisted, or used as credentials here.
	MaxIdentityLength  = 256
	maxExecutionGroups = gatewayroutecoordination.MaxBindings
)

type Outcome string

const (
	OutcomeReject      Outcome = "reject"
	OutcomeNoCandidate Outcome = "no_candidate"
	OutcomeExecute     Outcome = "execute"
)

type RejectReason string

const (
	RejectUnknownRequest           RejectReason = "unknown_request"
	RejectRequestNotAllowed        RejectReason = "request_not_allowed"
	RejectRoutePlanMissing         RejectReason = "route_plan_missing"
	RejectRoutePlanInvalid         RejectReason = "route_plan_invalid"
	RejectIdentityInvalid          RejectReason = "identity_invalid"
	RejectInitialCommitInvalid     RejectReason = "initial_commit_invalid"
	RejectCandidateSwitchCommitted RejectReason = "candidate_switch_committed"
	RejectOrchestrationIncomplete  RejectReason = "orchestration_incomplete"
)

// Identity is supplied by an outer authenticated request owner. Both fields
// are opaque bounded identifiers; they are not credentials and are not used
// to mutate any runtime state in this package.
type Identity struct {
	TraceID    string
	MutationID string
}

// Input contains the only three prerequisites for an execution decision. The
// route result must have been produced after authentication by gatewayrouteplan.
type Input struct {
	Request       gatewayrequestprep.Result
	Route         gatewayrouteplan.Result
	Identity      Identity
	InitialCommit gatewaystreamrelay.SinkState
}

// OrchestratedInput is the only execution handoff for a fully finalized W10
// request. The orchestration result supplies the same authenticated route and
// final lane that completed mapping/catalog/image-permission finalization;
// callers cannot substitute a raw path hint for either fact.
//
// It remains HTTP-independent. A future listener still owns candidate claim,
// capacity leases, response commit, audit/usage, and cross-group fallback.
type OrchestratedInput struct {
	Request       gatewayrequestprep.Result
	Intent        gatewayingress.RequestIntent
	Orchestration gatewayrequestorchestration.Result
	Identity      Identity
	InitialCommit gatewaystreamrelay.SinkState
}

// FallbackTargetInput creates a new one-group execution only after a later
// target completed dispatch-time policy preparation. Source carries the
// frozen request facts; Route/Current/Prepared prove the target belongs to a
// later route group and the same finalized request rather than a retained
// source batch or legacy fresh-only handoff.
type FallbackTargetInput struct {
	Source             Execution
	Route              gatewayrouteplan.RouteOnlyResult
	Current            gatewayrouteplan.FallbackCursor
	Prepared           gatewayrouteplan.FallbackDispatchPreparedTarget
	Reason             string
	EnteredGroupIDs    []string
	ExcludedAccountIDs []string
}

// Result is an immutable decision surface. Execution data is exposed through
// copy-returning accessors so callers cannot alter candidate or group order.
type Result struct {
	outcome Outcome
	reason  RejectReason
	execute *Execution
}

func (r Result) Outcome() Outcome           { return r.outcome }
func (r Result) RejectReason() RejectReason { return r.reason }

func (r Result) Execution() (Execution, bool) {
	if r.execute == nil {
		return Execution{}, false
	}
	return r.execute.clone(), true
}

// Capabilities is copied exclusively from gatewayrequestprep.Result. It
// deliberately has no public fields, preventing a listener from manufacturing
// permission for a controlled post-commit failure event.
type Capabilities struct {
	protocol              gatewayrequestprep.Protocol
	downstream            gatewayrequestprep.DownstreamProtocol
	clientProfile         gatewayrequestprep.ClientProfile
	compatibility         gatewayrequestprep.RequestClientCompatibility
	upstreamAdapter       gatewayrequestprep.UpstreamAdapter
	preCommitSignal       gatewayrequestprep.PreCommitFailureSignal
	committedSignal       gatewaystreamrelay.CommittedFailureSignal
	controlledFailureType gatewaystreamrelay.ControlledFailureProtocol
}

func (c Capabilities) Protocol() gatewayrequestprep.Protocol { return c.protocol }
func (c Capabilities) DownstreamProtocol() gatewayrequestprep.DownstreamProtocol {
	return c.downstream
}
func (c Capabilities) ClientProfile() gatewayrequestprep.ClientProfile { return c.clientProfile }
func (c Capabilities) RequestClientCompatibility() gatewayrequestprep.RequestClientCompatibility {
	return c.compatibility
}
func (c Capabilities) UpstreamAdapter() gatewayrequestprep.UpstreamAdapter { return c.upstreamAdapter }
func (c Capabilities) PreCommitFailureSignal() gatewayrequestprep.PreCommitFailureSignal {
	return c.preCommitSignal
}
func (c Capabilities) CommittedFailureSignal() gatewaystreamrelay.CommittedFailureSignal {
	return c.committedSignal
}
func (c Capabilities) ControlledFailureProtocol() (gatewaystreamrelay.ControlledFailureProtocol, bool) {
	return c.controlledFailureType, c.controlledFailureType != ""
}

// Execution contains immutable ordered batches. A batch maps exactly one
// verified route binding to candidates from that binding's group; candidates
// are never flattened or sorted across groups here.
type Execution struct {
	identity      Identity
	apiKeyID      string
	initialCommit gatewaystreamrelay.SinkState
	capabilities  Capabilities
	batches       []Batch
	intent        gatewayingress.RequestIntent
	finalization  gatewayingress.FinalResult
	request       protocolgateway.RequestShape
	finalLane     gatewayingress.Lane
	hasFinalLane  bool
	routeFence    gatewayrouteplan.RouteFence
	hasRouteFence bool
}

// RequestLineage is an opaque proof that two finalized executions originate
// from the same authenticated request. It deliberately omits batches because
// a later fallback target must replace them; all remaining immutable request,
// identity, finalization and route-fence facts stay bound.
type RequestLineage struct {
	execution Execution
	valid     bool
}

func (e Execution) Identity() Identity { return e.identity }

// APIKeyID is the authenticated API key identity paired with every batch.
// A future high-concurrency owner needs this exact value for its client-IP
// and queue scope; it must not recover it from a raw credential or candidate.
func (e Execution) APIKeyID() string                            { return e.apiKeyID }
func (e Execution) InitialCommit() gatewaystreamrelay.SinkState { return e.initialCommit }
func (e Execution) Capabilities() Capabilities                  { return e.capabilities }
func (e Execution) Batches() []Batch                            { return cloneBatches(e.batches) }

// RequestShape returns the canonical ingress facts for an orchestrated
// execution. The bool is false for the legacy pure Build path, which has no
// ingress-finalization proof and is not a listener handoff.
func (e Execution) RequestShape() (protocolgateway.RequestShape, bool) {
	if !e.hasFinalLane {
		return protocolgateway.RequestShape{}, false
	}
	return cloneRequestShape(e.request), true
}

// RequestIntent returns the parsed raw-body facts paired with RequestShape by
// BuildFromOrchestration. Its private fields make the parsing conclusions
// immutable to the dispatch and fallback layers.
func (e Execution) RequestIntent() (gatewayingress.RequestIntent, bool) {
	if !e.hasFinalLane || !e.intent.Parsed() {
		return gatewayingress.RequestIntent{}, false
	}
	return e.intent, true
}

// IngressFinalization returns the frozen mapping/catalog and image-permission
// decision paired with this finalized request. The value has opaque fields,
// so a later fallback cannot manufacture a different snapshot revision while
// retaining the same public lane.
func (e Execution) IngressFinalization() (gatewayingress.FinalResult, bool) {
	if !e.hasFinalLane || e.finalization.SnapshotRevision() == "" {
		return gatewayingress.FinalResult{}, false
	}
	return e.finalization, true
}

// FinalLane returns the final mapping/catalog/image-permission lane paired
// with RequestShape by BuildFromOrchestration.
func (e Execution) FinalLane() (gatewayingress.Lane, bool) {
	return e.finalLane, e.hasFinalLane
}

func (e Execution) clone() Execution {
	e.batches = cloneBatches(e.batches)
	e.request = cloneRequestShape(e.request)
	return e
}

func (e Execution) RequestLineage() RequestLineage {
	lineage := e.clone()
	lineage.batches = nil
	return RequestLineage{
		execution: lineage,
		valid: lineage.hasFinalLane && lineage.hasRouteFence && lineage.intent.Parsed() &&
			lineage.finalization.SnapshotRevision() != "" && strings.TrimSpace(lineage.identity.TraceID) != "" &&
			strings.TrimSpace(lineage.identity.MutationID) != "" && strings.TrimSpace(lineage.apiKeyID) != "",
	}
}

// Matches reports whether execution carries the exact immutable request
// lineage. It cannot be constructed outside this package, so a caller cannot
// substitute another authenticated execution by supplying matching strings.
func (l RequestLineage) Matches(execution Execution) bool {
	other := execution.RequestLineage()
	return l.valid && other.valid && reflect.DeepEqual(l.execution, other.execution)
}

type Batch struct {
	bindingID string
	groupID   string
	window    gatewaycandidatewindow.Window
}

func (b Batch) BindingID() string { return b.bindingID }
func (b Batch) GroupID() string   { return b.groupID }
func (b Batch) Candidates() []gatewaycandidatewindow.Candidate {
	return cloneCandidates(b.window.Candidates)
}

// RuntimeWindow returns the verified runtime facts that accompanied this
// candidate batch: group type, scheduling policy, authorization scope and
// candidates. It is a detached copy. Only the current group may be executed
// from this snapshot; a Node-compatible cross-group fallback must still run
// complete target-group preparation and acquire its own client-IP lease.
func (b Batch) RuntimeWindow() gatewaycandidatewindow.Window { return cloneWindow(b.window) }

// Build validates that the route plan has not been reordered or spliced after
// preflight, then preserves its ordered group boundaries. It makes no attempt
// to select, dispatch, or mutate a candidate. A transport or semantic commit
// is a hard fence: a caller must not obtain a new candidate-switch plan after
// downstream output may have become observable.
func Build(input Input) Result {
	if reason := validateRequest(input.Request); reason != "" {
		return reject(reason)
	}
	if !validIdentity(input.Identity) {
		return reject(RejectIdentityInvalid)
	}
	if !validCommit(input.InitialCommit) {
		return reject(RejectInitialCommitInvalid)
	}
	if input.InitialCommit.TransportCommitted || input.InitialCommit.SemanticCommitted {
		return reject(RejectCandidateSwitchCommitted)
	}
	if reason := validateRoute(input.Route); reason != "" {
		return reject(reason)
	}
	apiKey, ok := input.Route.Preflight.APIKey()
	if !ok {
		return reject(RejectRoutePlanInvalid)
	}

	batches := make([]Batch, 0, len(input.Route.Groups))
	for _, group := range input.Route.Groups {
		if !group.Found || len(group.Window.Candidates) == 0 {
			continue
		}
		if !validWindow(group.Window, group.Binding.GroupID(), input.Route.Plan.Scope.SystemAccountID) ||
			!validCandidates(group.Window.Candidates, group.Binding.GroupID(), input.Route.Plan.Scope.SystemAccountID) {
			return reject(RejectRoutePlanInvalid)
		}
		batches = append(batches, Batch{
			bindingID: group.Binding.ID(), groupID: group.Binding.GroupID(),
			window: cloneWindow(group.Window),
		})
	}
	if len(batches) == 0 {
		return Result{outcome: OutcomeNoCandidate}
	}
	capabilities := capabilitiesFrom(input.Request)
	return Result{outcome: OutcomeExecute, execute: &Execution{
		identity: input.Identity, apiKeyID: apiKey.ID(), initialCommit: input.InitialCommit,
		capabilities: capabilities, batches: batches,
	}}
}

// BuildFromOrchestration turns an allowed, complete orchestration result into
// a read-only execution plan. It rejects a partial result rather than asking a
// caller to reconstruct final-lane state from a request body or candidate.
func BuildFromOrchestration(input OrchestratedInput) Result {
	route, finalization, reason := validatedOrchestration(input.Request, input.Intent, input.Orchestration)
	if reason != "" {
		return reject(reason)
	}
	result := Build(Input{
		Request: input.Request, Route: route, Identity: input.Identity, InitialCommit: input.InitialCommit,
	})
	if result.outcome != OutcomeExecute || result.execute == nil {
		return result
	}
	execution := result.execute.clone()
	execution.intent = input.Intent
	execution.finalization = finalization
	execution.request = input.Request.RequestShape()
	execution.finalLane = finalization.FinalLane()
	execution.hasFinalLane = true
	routeOnly, err := gatewayrouteplan.RouteOnlyFromResult(route)
	if err != nil {
		return reject(RejectRoutePlanInvalid)
	}
	fence, err := gatewayrouteplan.NewRouteFence(routeOnly)
	if err != nil {
		return reject(RejectRoutePlanInvalid)
	}
	execution.routeFence = fence
	execution.hasRouteFence = true
	return Result{outcome: OutcomeExecute, execute: &execution}
}

// BuildFallbackTarget preserves one finalized request's identity, request
// shape, final lane, capabilities and downstream commit fence while replacing
// the runnable batch with exactly one freshly prepared later route target. It
// is not a route owner: it neither chooses a fallback, updates request state,
// acquires a lease, nor dispatches an attempt.
func BuildFallbackTarget(input FallbackTargetInput) Result {
	request, hasRequest := input.Source.RequestShape()
	intent, hasIntent := input.Source.RequestIntent()
	finalization, hasFinalization := input.Source.IngressFinalization()
	lane, hasLane := input.Source.FinalLane()
	if !hasRequest || !hasIntent || !hasFinalization || !hasLane || !validFinalLane(lane) || !validRequestShape(request) ||
		intent.Model() != request.Model || intent.Stream() != request.Stream {
		return reject(RejectOrchestrationIncomplete)
	}
	if !validIdentity(input.Source.identity) || !validCommit(input.Source.initialCommit) {
		return reject(RejectIdentityInvalid)
	}
	if input.Source.initialCommit.TransportCommitted || input.Source.initialCommit.SemanticCommitted {
		return reject(RejectCandidateSwitchCommitted)
	}
	if !input.Source.hasRouteFence || gatewayrouteplan.ValidateRouteFence(input.Route, input.Source.routeFence) != nil {
		return reject(RejectRoutePlanInvalid)
	}
	apiKey, ok := input.Route.Preflight.APIKey()
	if !ok || apiKey.ID() != input.Source.apiKeyID {
		return reject(RejectRoutePlanInvalid)
	}
	sourceBatches := input.Source.Batches()
	if len(sourceBatches) == 0 || sourceBatches[0].BindingID() != input.Current.BindingID() || sourceBatches[0].GroupID() != input.Current.GroupID() {
		return reject(RejectRoutePlanInvalid)
	}
	protocol := protocolgateway.ProtocolCode(input.Source.capabilities.Protocol())
	endpointFamily := protocolgateway.EndpointFamilyFromPath(protocol, request.Path)
	if protocol == "" || endpointFamily == protocolgateway.EndpointUnknown {
		return reject(RejectOrchestrationIncomplete)
	}
	target, window, found, err := gatewayrouteplan.ValidateFallbackDispatchPreparedTarget(input.Route, gatewayrouteplan.FallbackDispatchPreparedInput{
		FallbackPreparedInput: gatewayrouteplan.FallbackPreparedInput{
			Route: input.Route, Current: input.Current, EnteredGroupIDs: append([]string(nil), input.EnteredGroupIDs...),
			RequestedModel: request.Model, EndpointFamily: string(endpointFamily),
		},
		Intent: intent, IngressFinalization: finalization, RequestShape: request, Protocol: protocol, FinalLane: lane, Reason: input.Reason,
		RequestClientCompatibility: string(input.Source.capabilities.RequestClientCompatibility()), RequestLane: string(lane),
		ExcludedAccountIDs: append([]string(nil), input.ExcludedAccountIDs...),
	}, input.Prepared)
	if err != nil {
		return reject(RejectRoutePlanInvalid)
	}
	if !found {
		return Result{outcome: OutcomeNoCandidate}
	}
	if len(window.Candidates) == 0 || !validWindow(window, target.Binding().GroupID(), apiKey.SystemAccountID()) ||
		!validCandidates(window.Candidates, target.Binding().GroupID(), apiKey.SystemAccountID()) {
		return reject(RejectRoutePlanInvalid)
	}
	return Result{outcome: OutcomeExecute, execute: &Execution{
		identity: input.Source.identity, apiKeyID: input.Source.apiKeyID, initialCommit: input.Source.initialCommit,
		capabilities: input.Source.capabilities, batches: []Batch{{bindingID: target.Binding().ID(), groupID: target.Binding().GroupID(), window: cloneWindow(window)}},
		intent: intent, finalization: finalization, request: cloneRequestShape(request), finalLane: lane, hasFinalLane: true, routeFence: input.Source.routeFence, hasRouteFence: true,
	}}
}

func reject(reason RejectReason) Result { return Result{outcome: OutcomeReject, reason: reason} }

func validateRequest(request gatewayrequestprep.Result) RejectReason {
	if request.Protocol() == gatewayrequestprep.ProtocolUnknown ||
		request.DownstreamProtocol() == gatewayrequestprep.DownstreamUnknownStream ||
		request.UpstreamAdapter() == gatewayrequestprep.UpstreamAdapterUnknown {
		return RejectUnknownRequest
	}
	switch request.Protocol() {
	case gatewayrequestprep.ProtocolOpenAI, gatewayrequestprep.ProtocolAnthropic, gatewayrequestprep.ProtocolGemini:
	default:
		return RejectRequestNotAllowed
	}
	return ""
}

func validRequestShape(shape protocolgateway.RequestShape) bool {
	return strings.TrimSpace(shape.Model) != ""
}

func validIdentity(value Identity) bool {
	return validOpaqueID(value.TraceID) && validOpaqueID(value.MutationID)
}

func validOpaqueID(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= MaxIdentityLength && !strings.ContainsAny(value, "\r\n\x00")
}

func validCommit(value gatewaystreamrelay.SinkState) bool {
	if value.DownstreamBytes < 0 {
		return false
	}
	return value.TransportCommitted || (!value.SemanticCommitted && value.DownstreamBytes == 0)
}

func validateRoute(route gatewayrouteplan.Result) RejectReason {
	if !route.Preflight.Decision().Allowed() {
		return RejectRequestNotAllowed
	}
	if route.Plan == nil {
		return RejectRoutePlanMissing
	}
	plan := route.Plan
	if len(plan.Ordered) == 0 || len(plan.Ordered) > maxExecutionGroups || len(route.Groups) != len(plan.Ordered) ||
		!validOpaqueID(plan.Scope.SystemAccountID) || !validOpaqueID(plan.Scope.RouteStrategyID) ||
		!validOpaqueID(plan.Revision) || plan.DispatchGeneration < 0 {
		return RejectRoutePlanInvalid
	}
	key, ok := route.Preflight.APIKey()
	if !ok || key.SystemAccountID() != plan.Scope.SystemAccountID || key.RouteStrategyID() != plan.Scope.RouteStrategyID ||
		key.RouteDispatchGeneration() != plan.DispatchGeneration || key.RouteStrategyMode() != string(plan.Mode) {
		return RejectRoutePlanInvalid
	}
	seenBinding := make(map[string]struct{}, len(plan.Ordered))
	seenGroup := make(map[string]struct{}, len(plan.Ordered))
	snapshotBindings := make([]gatewayrouting.Binding, 0, len(plan.Ordered))
	for index, planned := range plan.Ordered {
		group := route.Groups[index]
		binding := group.Binding
		if !validOpaqueID(planned.ID) || !validOpaqueID(planned.GroupID) || planned.Weight < 1 || planned.Weight > 100 ||
			!planned.Active || !planned.GroupEnabled || binding.ID() != planned.ID || binding.GroupID() != planned.GroupID ||
			binding.Priority() != planned.Priority || binding.Weight() != planned.Weight || !strings.EqualFold(binding.Status(), "active") ||
			!binding.GroupEnabled() || binding.SystemAccountID() != key.SystemAccountID() || binding.APIKeyID() != key.ID() {
			return RejectRoutePlanInvalid
		}
		if _, exists := seenBinding[planned.ID]; exists {
			return RejectRoutePlanInvalid
		}
		if _, exists := seenGroup[planned.GroupID]; exists {
			return RejectRoutePlanInvalid
		}
		seenBinding[planned.ID] = struct{}{}
		seenGroup[planned.GroupID] = struct{}{}
		snapshotBindings = append(snapshotBindings, planned)
	}
	revision, err := gatewayroutecoordination.Revision(gatewayroutecoordination.Snapshot{
		Scope: plan.Scope, DispatchGeneration: plan.DispatchGeneration, Mode: plan.Mode, Bindings: snapshotBindings,
	})
	if err != nil || revision != plan.Revision {
		return RejectRoutePlanInvalid
	}
	return ""
}

func validatedOrchestration(request gatewayrequestprep.Result, intent gatewayingress.RequestIntent, orchestration gatewayrequestorchestration.Result) (gatewayrouteplan.Result, gatewayingress.FinalResult, RejectReason) {
	if !orchestration.Preflight.Decision().Allowed() || orchestration.Route == nil || orchestration.Ingress == nil {
		return gatewayrouteplan.Result{}, gatewayingress.FinalResult{}, RejectOrchestrationIncomplete
	}
	route := *orchestration.Route
	ingress := orchestration.Ingress
	shape := request.RequestShape()
	if !intent.Parsed() || !orchestration.Intent.Parsed() || intent.Model() != orchestration.Intent.Model() || intent.Stream() != orchestration.Intent.Stream() ||
		shape.Model != intent.Model() || shape.Stream != intent.Stream() ||
		!reflect.DeepEqual(route.Preflight, orchestration.Preflight) || !reflect.DeepEqual(ingress.Preflight, orchestration.Preflight) ||
		ingress.Finalization == nil || ingress.Admission == nil {
		return gatewayrouteplan.Result{}, gatewayingress.FinalResult{}, RejectOrchestrationIncomplete
	}
	finalLane := ingress.Finalization.FinalLane()
	if !validFinalLane(finalLane) || ingress.Admission.FinalLane() != finalLane ||
		ingress.Finalization.CandidateCapacity() < 1 || ingress.Admission.CandidateCapacity() < 1 ||
		ingress.Finalization.CandidateCapacity() != ingress.Admission.CandidateCapacity() ||
		ingress.Finalization.SnapshotRevision() == "" || ingress.Finalization.SnapshotRevision() != ingress.Admission.SnapshotRevision() {
		return gatewayrouteplan.Result{}, gatewayingress.FinalResult{}, RejectOrchestrationIncomplete
	}
	return route, *ingress.Finalization, ""
}

func validFinalLane(lane gatewayingress.Lane) bool {
	return lane == gatewayingress.LaneText || lane == gatewayingress.LaneImage
}

func capabilitiesFrom(request gatewayrequestprep.Result) Capabilities {
	controlled, _ := request.ControlledFailureProtocol()
	return Capabilities{
		protocol: request.Protocol(), downstream: request.DownstreamProtocol(), clientProfile: request.ClientProfile(),
		compatibility: request.RequestClientCompatibility(), upstreamAdapter: request.UpstreamAdapter(),
		preCommitSignal: request.PreCommitFailureSignal(), committedSignal: request.CommittedFailureSignal(),
		controlledFailureType: controlled,
	}
}

func validCandidates(candidates []gatewaycandidatewindow.Candidate, groupID, systemAccountID string) bool {
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		projection := candidate.Projection
		if !validOpaqueID(projection.AccountID) || projection.GroupID != groupID || projection.SystemAccountID != systemAccountID {
			return false
		}
		if _, exists := seen[projection.AccountID]; exists {
			return false
		}
		seen[projection.AccountID] = struct{}{}
	}
	return true
}

func validWindow(window gatewaycandidatewindow.Window, groupID, systemAccountID string) bool {
	access := window.Access
	return access.GroupID == groupID && access.CallerSystemAccountID == systemAccountID && strings.TrimSpace(access.GroupType) != ""
}

func cloneBatches(input []Batch) []Batch {
	result := make([]Batch, len(input))
	for index, batch := range input {
		result[index] = Batch{bindingID: batch.bindingID, groupID: batch.groupID, window: cloneWindow(batch.window)}
	}
	return result
}

func cloneWindow(input gatewaycandidatewindow.Window) gatewaycandidatewindow.Window {
	result := input
	result.Candidates = cloneCandidates(input.Candidates)
	result.Access.GroupAuthorizationExpiresAt = cloneTime(input.Access.GroupAuthorizationExpiresAt)
	return result
}

func cloneCandidates(input []gatewaycandidatewindow.Candidate) []gatewaycandidatewindow.Candidate {
	result := make([]gatewaycandidatewindow.Candidate, len(input))
	for index, candidate := range input {
		result[index] = candidate
		result[index].SupportedModels = append([]string(nil), candidate.SupportedModels...)
		result[index].ModelMappings = append([]gatewaycandidatewindow.ModelMapping(nil), candidate.ModelMappings...)
		result[index].APIKeyRuntime = append([]gatewaycandidatewindow.APIKeyRuntime(nil), candidate.APIKeyRuntime...)
		if candidate.Proxy != nil {
			proxy := *candidate.Proxy
			result[index].Proxy = &proxy
		}
		if candidate.QualityScore != nil {
			value := *candidate.QualityScore
			result[index].QualityScore = &value
		}
		if candidate.QualityEWMAFirstTokenMS != nil {
			value := *candidate.QualityEWMAFirstTokenMS
			result[index].QualityEWMAFirstTokenMS = &value
		}
		result[index].Projection.CooldownUntil = cloneTime(candidate.Projection.CooldownUntil)
		result[index].Projection.AccountExpiresAt = cloneTime(candidate.Projection.AccountExpiresAt)
		result[index].Projection.AuthorizationExpiresAt = cloneTime(candidate.Projection.AuthorizationExpiresAt)
		result[index].Projection.ResourceCooldownUntil = cloneTime(candidate.Projection.ResourceCooldownUntil)
		result[index].Projection.ResourceAccountExpiresAt = cloneTime(candidate.Projection.ResourceAccountExpiresAt)
	}
	return result
}

func cloneRequestShape(input protocolgateway.RequestShape) protocolgateway.RequestShape {
	result := input
	result.Headers = make(map[string]string, len(input.Headers))
	for key, value := range input.Headers {
		result.Headers[key] = value
	}
	return result
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

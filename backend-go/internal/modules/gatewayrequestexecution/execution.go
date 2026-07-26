// Package gatewayrequestexecution joins canonical request preparation with an
// already-authenticated route plan into a read-only execution plan. It has no
// HTTP, body, credential, lease, slot, circuit, audit, usage, or owner side
// effects; a future gateway owner must revalidate a candidate at its final
// claim boundary before it sends credentials upstream.
package gatewayrequestexecution

import (
	"strings"
	"time"

	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
	"juhe-ai/backend-go/internal/modules/gatewayrequestprep"
	"juhe-ai/backend-go/internal/modules/gatewayroutecoordination"
	"juhe-ai/backend-go/internal/modules/gatewayrouteplan"
	"juhe-ai/backend-go/internal/modules/gatewayrouting"
	"juhe-ai/backend-go/internal/modules/gatewaystreamrelay"
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
	initialCommit gatewaystreamrelay.SinkState
	capabilities  Capabilities
	batches       []Batch
}

func (e Execution) Identity() Identity                          { return e.identity }
func (e Execution) InitialCommit() gatewaystreamrelay.SinkState { return e.initialCommit }
func (e Execution) Capabilities() Capabilities                  { return e.capabilities }
func (e Execution) Batches() []Batch                            { return cloneBatches(e.batches) }

func (e Execution) clone() Execution {
	e.batches = cloneBatches(e.batches)
	return e
}

type Batch struct {
	bindingID  string
	groupID    string
	candidates []gatewaycandidatewindow.Candidate
}

func (b Batch) BindingID() string { return b.bindingID }
func (b Batch) GroupID() string   { return b.groupID }
func (b Batch) Candidates() []gatewaycandidatewindow.Candidate {
	return cloneCandidates(b.candidates)
}

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

	batches := make([]Batch, 0, len(input.Route.Groups))
	for _, group := range input.Route.Groups {
		if !group.Found || len(group.Window.Candidates) == 0 {
			continue
		}
		if !validCandidates(group.Window.Candidates, group.Binding.GroupID(), input.Route.Plan.Scope.SystemAccountID) {
			return reject(RejectRoutePlanInvalid)
		}
		batches = append(batches, Batch{
			bindingID: group.Binding.ID(), groupID: group.Binding.GroupID(),
			candidates: cloneCandidates(group.Window.Candidates),
		})
	}
	if len(batches) == 0 {
		return Result{outcome: OutcomeNoCandidate}
	}
	capabilities := capabilitiesFrom(input.Request)
	return Result{outcome: OutcomeExecute, execute: &Execution{
		identity: input.Identity, initialCommit: input.InitialCommit,
		capabilities: capabilities, batches: batches,
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

func cloneBatches(input []Batch) []Batch {
	result := make([]Batch, len(input))
	for index, batch := range input {
		result[index] = Batch{bindingID: batch.bindingID, groupID: batch.groupID, candidates: cloneCandidates(batch.candidates)}
	}
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

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

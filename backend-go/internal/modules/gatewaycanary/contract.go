// Package gatewaycanary defines a pure, fail-closed operator-canary gate for
// the future Go gateway owner. It deliberately has no configuration, router,
// listener, deployment, storage, or proxy side effects.
package gatewaycanary

import "strings"

// Decision is the only action classification emitted by Evaluate.
type Decision string

const (
	DecisionNotEligible               Decision = "not_eligible"
	DecisionEligibleForOperatorCanary Decision = "eligible_for_operator_canary"
)

// ReasonCode is stable, machine-readable evidence for a denied decision.
// ReasonReady is returned only with DecisionEligibleForOperatorCanary.
type ReasonCode string

const (
	ReasonReady                          ReasonCode = "ready"
	ReasonModelsOnlySurface              ReasonCode = "models_only_surface"
	ReasonEpochMissing                   ReasonCode = "gateway_epoch_missing"
	ReasonEpochInvalid                   ReasonCode = "gateway_epoch_invalid"
	ReasonEpochMismatch                  ReasonCode = "gateway_epoch_mismatch"
	ReasonSchemaVersionMissing           ReasonCode = "schema_version_missing"
	ReasonSchemaVersionMismatch          ReasonCode = "schema_version_mismatch"
	ReasonRouteIdentifierMissing         ReasonCode = "route_identifier_missing"
	ReasonRouteIdentifierInvalid         ReasonCode = "route_identifier_invalid"
	ReasonRollbackRouteMismatch          ReasonCode = "rollback_route_mismatch"
	ReasonGoRouteOwnerMissing            ReasonCode = "go_route_owner_missing"
	ReasonNodeRollbackOwnerMissing       ReasonCode = "node_rollback_owner_missing"
	ReasonRouteBindingUnverified         ReasonCode = "route_binding_unverified"
	ReasonRollbackRouteUnverified        ReasonCode = "rollback_route_unverified"
	ReasonGatewayTrafficEvidenceMissing  ReasonCode = "gateway_traffic_evidence_missing"
	ReasonGoListenerEvidenceMissing      ReasonCode = "go_listener_evidence_missing"
	ReasonBodyAdmissionEvidenceMissing   ReasonCode = "body_admission_evidence_missing"
	ReasonPreflightEvidenceMissing       ReasonCode = "preflight_evidence_missing"
	ReasonRoutePlanEvidenceMissing       ReasonCode = "route_plan_evidence_missing"
	ReasonAttemptTerminalEvidenceMissing ReasonCode = "attempt_terminal_evidence_missing"
	ReasonSingleWriterEvidenceMissing    ReasonCode = "single_writer_evidence_missing"
	ReasonNodeDrainEvidenceMissing       ReasonCode = "node_drain_evidence_missing"
	ReasonProxyDispatchEvidenceMissing   ReasonCode = "proxy_dispatch_evidence_missing"
	ReasonUpstreamSmokeEvidenceMissing   ReasonCode = "upstream_smoke_evidence_missing"
	ReasonRollbackEvidenceMissing        ReasonCode = "rollback_evidence_missing"
)

// Owner identifies the intended owner for one exact gateway route. It is not
// a deployment instruction; it only records evidence supplied by an operator
// workflow.
type Owner string

const (
	OwnerNode Owner = "node"
	OwnerGo   Owner = "go"
)

// VersionEvidence fences an operator result to one observed gateway/runtime
// epoch and one schema version. Epochs are bounded deployment identifiers,
// not numeric counters: both expected and observed values must be present,
// well-formed, and byte-for-byte equal. Stale evidence is never sufficient.
type VersionEvidence struct {
	ExpectedEpoch         string
	ObservedEpoch         string
	ExpectedSchemaVersion uint64
	ObservedSchemaVersion uint64
}

// RouteOwnerRollbackEvidence proves that one exact route can be operated by
// Go and restored to Node. A route identifier is canonical "METHOD /path";
// identifiers are compared byte-for-byte so rollback cannot silently target a
// broader or neighboring route.
type RouteOwnerRollbackEvidence struct {
	RouteID         string
	RollbackRouteID string
	CurrentOwner    Owner
	RollbackOwner   Owner

	RouteBindingVerified  bool
	RollbackRouteVerified bool
}

// GatewayPathEvidence records the actual gateway path, rather than a catalog
// or /models-only result. Every flag represents evidence already collected by
// an external workflow; this package neither generates nor persists it.
type GatewayPathEvidence struct {
	NonModelsGatewayTrafficObserved bool
	GoListenerObserved              bool
	BodyAdmissionObserved           bool
	PreflightObserved               bool
	RoutePlanObserved               bool
	AttemptTerminalObserved         bool
}

// HandoverEvidence records the cross-owner and upstream facts that make the
// canary reversible without retaining a second writer.
type HandoverEvidence struct {
	SingleGatewayWriterVerified bool
	NodeDrainVerified           bool
	ProxyDispatchVerified       bool
	UpstreamSmokeVerified       bool
	RollbackEvidenceVerified    bool
}

// Input is an immutable-by-value evidence snapshot. It has no maps, slices,
// pointers, callbacks, credentials, or time-dependent state; Evaluate never
// mutates it and therefore returns the same decision for the same snapshot.
type Input struct {
	ModelsOnlySurface bool
	Version           VersionEvidence
	Route             RouteOwnerRollbackEvidence
	Gateway           GatewayPathEvidence
	Handover          HandoverEvidence
}

// Result is immutable outside this package. It intentionally exposes only
// the final action and its deterministic reason, not a partial enablement
// plan that a caller could treat as a routing instruction.
type Result struct {
	decision Decision
	reason   ReasonCode
}

func (r Result) Decision() Decision     { return r.decision }
func (r Result) ReasonCode() ReasonCode { return r.reason }

// Evaluate is a pure, fail-closed gate. It does not attempt to infer missing
// proof: a /models-only surface, absent real gateway evidence, or any absent
// execution/handover proof always returns DecisionNotEligible. Reason priority
// is deliberately fixed to make audits deterministic.
func Evaluate(input Input) Result {
	for _, check := range orderedChecks {
		if !check.satisfied(input) {
			return Result{decision: DecisionNotEligible, reason: check.reason}
		}
	}
	return Result{decision: DecisionEligibleForOperatorCanary, reason: ReasonReady}
}

type check struct {
	reason    ReasonCode
	satisfied func(Input) bool
}

var orderedChecks = [...]check{
	{ReasonModelsOnlySurface, func(input Input) bool {
		return !input.ModelsOnlySurface && !modelsOnlyRoute(input.Route.RouteID)
	}},
	{ReasonEpochMissing, func(input Input) bool {
		return strings.TrimSpace(input.Version.ExpectedEpoch) != "" && strings.TrimSpace(input.Version.ObservedEpoch) != ""
	}},
	{ReasonEpochInvalid, func(input Input) bool {
		return validEpoch(input.Version.ExpectedEpoch) && validEpoch(input.Version.ObservedEpoch)
	}},
	{ReasonEpochMismatch, func(input Input) bool { return input.Version.ExpectedEpoch == input.Version.ObservedEpoch }},
	{ReasonSchemaVersionMissing, func(input Input) bool {
		return input.Version.ExpectedSchemaVersion != 0 && input.Version.ObservedSchemaVersion != 0
	}},
	{ReasonSchemaVersionMismatch, func(input Input) bool {
		return input.Version.ExpectedSchemaVersion == input.Version.ObservedSchemaVersion
	}},
	{ReasonRouteIdentifierMissing, func(input Input) bool {
		return strings.TrimSpace(input.Route.RouteID) != "" && strings.TrimSpace(input.Route.RollbackRouteID) != ""
	}},
	{ReasonRouteIdentifierInvalid, func(input Input) bool {
		return validRouteIdentifier(input.Route.RouteID) && validRouteIdentifier(input.Route.RollbackRouteID)
	}},
	{ReasonRollbackRouteMismatch, func(input Input) bool { return input.Route.RouteID == input.Route.RollbackRouteID }},
	{ReasonGoRouteOwnerMissing, func(input Input) bool { return input.Route.CurrentOwner == OwnerGo }},
	{ReasonNodeRollbackOwnerMissing, func(input Input) bool { return input.Route.RollbackOwner == OwnerNode }},
	{ReasonRouteBindingUnverified, func(input Input) bool { return input.Route.RouteBindingVerified }},
	{ReasonRollbackRouteUnverified, func(input Input) bool { return input.Route.RollbackRouteVerified }},
	{ReasonGatewayTrafficEvidenceMissing, func(input Input) bool { return input.Gateway.NonModelsGatewayTrafficObserved }},
	{ReasonGoListenerEvidenceMissing, func(input Input) bool { return input.Gateway.GoListenerObserved }},
	{ReasonBodyAdmissionEvidenceMissing, func(input Input) bool { return input.Gateway.BodyAdmissionObserved }},
	{ReasonPreflightEvidenceMissing, func(input Input) bool { return input.Gateway.PreflightObserved }},
	{ReasonRoutePlanEvidenceMissing, func(input Input) bool { return input.Gateway.RoutePlanObserved }},
	{ReasonAttemptTerminalEvidenceMissing, func(input Input) bool { return input.Gateway.AttemptTerminalObserved }},
	{ReasonSingleWriterEvidenceMissing, func(input Input) bool { return input.Handover.SingleGatewayWriterVerified }},
	{ReasonNodeDrainEvidenceMissing, func(input Input) bool { return input.Handover.NodeDrainVerified }},
	{ReasonProxyDispatchEvidenceMissing, func(input Input) bool { return input.Handover.ProxyDispatchVerified }},
	{ReasonUpstreamSmokeEvidenceMissing, func(input Input) bool { return input.Handover.UpstreamSmokeVerified }},
	{ReasonRollbackEvidenceMissing, func(input Input) bool { return input.Handover.RollbackEvidenceVerified }},
}

func validRouteIdentifier(value string) bool {
	method, path, found := strings.Cut(value, " ")
	if !found || method == "" || path == "" || strings.Count(value, " ") != 1 || !validHTTPMethod(method) {
		return false
	}
	if !strings.HasPrefix(path, "/") || path == "/" || strings.HasSuffix(path, "/") || strings.Contains(path, "//") || strings.ContainsAny(path, "*? #%\\\r\n\x00{}:") {
		return false
	}
	for _, prefix := range [...]string{"/__aisys__", "/__aipublic__", "/__aiinternal__"} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return false
		}
	}
	for _, segment := range strings.Split(path, "/")[1:] {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func validHTTPMethod(value string) bool {
	switch value {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
		return true
	default:
		return false
	}
}

func modelsOnlyRoute(value string) bool {
	_, path, found := strings.Cut(value, " ")
	return found && (path == "/v1/models" || strings.HasPrefix(path, "/v1/models/"))
}

func validEpoch(value string) bool {
	if value == "" || len(value) > 128 || value != strings.TrimSpace(value) {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z') && !(char >= 'A' && char <= 'Z') && !(char >= '0' && char <= '9') && char != '.' && char != '_' && char != '-' {
			return false
		}
	}
	return true
}

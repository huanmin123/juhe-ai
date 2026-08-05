package gatewayrequestexecution

import (
	"context"
	"reflect"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
	"juhe-ai/backend-go/internal/modules/gatewayingress"
	"juhe-ai/backend-go/internal/modules/gatewayingressplan"
	"juhe-ai/backend-go/internal/modules/gatewaypreflight"
	"juhe-ai/backend-go/internal/modules/gatewayrequestorchestration"
	"juhe-ai/backend-go/internal/modules/gatewayrequestprep"
	"juhe-ai/backend-go/internal/modules/gatewayroutecoordination"
	"juhe-ai/backend-go/internal/modules/gatewayrouteplan"
	"juhe-ai/backend-go/internal/modules/gatewaystreamrelay"
	protocolgateway "juhe-ai/backend-go/internal/protocols/gateway"
	"juhe-ai/backend-go/internal/store/port"
)

func TestBuildPreservesAuthenticatedBindingAndCandidateOrder(t *testing.T) {
	t.Parallel()
	route := testRoute(t, "failover", []testRouteGroup{
		{bindingID: "binding-one", groupID: "group-one", priority: 1, candidates: []gatewaycandidatewindow.Candidate{candidate("account-a", "group-one"), candidate("account-b", "group-one")}},
		{bindingID: "binding-two", groupID: "group-two", priority: 2, candidates: []gatewaycandidatewindow.Candidate{candidate("account-c", "group-two")}},
	})
	result := Build(Input{Request: openAIRequest(), Route: route, Identity: Identity{TraceID: "trace-1", MutationID: "mutation-1"}})
	if result.Outcome() != OutcomeExecute || result.RejectReason() != "" {
		t.Fatalf("result = %#v", result)
	}
	execution, ok := result.Execution()
	if !ok || execution.Capabilities().Protocol() != gatewayrequestprep.ProtocolOpenAI || execution.InitialCommit().TransportCommitted {
		t.Fatalf("execution = %#v ok=%v", execution, ok)
	}
	batches := execution.Batches()
	if got, want := batchIDs(batches), []string{"binding-one/group-one", "binding-two/group-two"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("batch order = %v, want %v", got, want)
	}
	if got, want := candidateIDs(batches[0].Candidates()), []string{"account-a", "account-b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("candidate order = %v, want %v", got, want)
	}

	// Copy-returning accessors make later caller mutation unable to alter the
	// plan used by an execution owner.
	batches[0].window.Candidates[0].Projection.Name = "forged"
	batches[0].window.Candidates[0].SupportedModels = []string{"forged"}
	again, ok := result.Execution()
	if !ok || again.Batches()[0].Candidates()[0].Projection.Name == "forged" || len(again.Batches()[0].Candidates()[0].SupportedModels) != 1 {
		t.Fatalf("execution leaked mutable batch data: %#v", again.Batches()[0].Candidates()[0])
	}
}

func TestBuildRejectsUnknownDeniedOrForgedRouteInputs(t *testing.T) {
	t.Parallel()
	route := testRoute(t, "failover", []testRouteGroup{
		{bindingID: "binding-one", groupID: "group-one", priority: 1, candidates: []gatewaycandidatewindow.Candidate{candidate("account-a", "group-one")}},
		{bindingID: "binding-two", groupID: "group-two", priority: 2, candidates: []gatewaycandidatewindow.Candidate{candidate("account-b", "group-two")}},
	})
	base := Input{Request: openAIRequest(), Route: route, Identity: Identity{TraceID: "trace-2", MutationID: "mutation-2"}}
	if got := Build(Input{Route: route, Identity: base.Identity}); got.RejectReason() != RejectUnknownRequest {
		t.Fatalf("unknown request = %q", got.RejectReason())
	}
	forged := base
	forged.Route.Groups[1].Binding = forged.Route.Groups[0].Binding
	if got := Build(forged); got.RejectReason() != RejectRoutePlanInvalid {
		t.Fatalf("cross-group reorder = %q", got.RejectReason())
	}
	duplicate := base
	duplicate.Route.Plan.Ordered[1].ID = duplicate.Route.Plan.Ordered[0].ID
	if got := Build(duplicate); got.RejectReason() != RejectRoutePlanInvalid {
		t.Fatalf("duplicate binding = %q", got.RejectReason())
	}
	denied := base
	denied.Route = gatewayrouteplan.Result{}
	if got := Build(denied); got.RejectReason() != RejectRequestNotAllowed {
		t.Fatalf("denied route = %q", got.RejectReason())
	}
}

func TestBuildRejectsCandidateSwitchAfterCommit(t *testing.T) {
	t.Parallel()
	route := testRoute(t, "normal", []testRouteGroup{{bindingID: "binding-one", groupID: "group-one", priority: 1, candidates: []gatewaycandidatewindow.Candidate{candidate("account-a", "group-one")}}})
	for _, commit := range []gatewaystreamrelay.SinkState{{TransportCommitted: true}, {TransportCommitted: true, SemanticCommitted: true, DownstreamBytes: 1}} {
		result := Build(Input{Request: openAIRequest(), Route: route, Identity: Identity{TraceID: "trace-3", MutationID: "mutation-3"}, InitialCommit: commit})
		if result.Outcome() != OutcomeReject || result.RejectReason() != RejectCandidateSwitchCommitted {
			t.Fatalf("commit %#v result=%#v", commit, result)
		}
	}
	invalid := Build(Input{Request: openAIRequest(), Route: route, Identity: Identity{TraceID: "trace-3", MutationID: "mutation-3"}, InitialCommit: gatewaystreamrelay.SinkState{DownstreamBytes: 1}})
	if invalid.RejectReason() != RejectInitialCommitInvalid {
		t.Fatalf("invalid commit = %q", invalid.RejectReason())
	}
}

func TestBuildNoCandidateAndCopiesOnlyPrepFailureCapability(t *testing.T) {
	t.Parallel()
	empty := testRoute(t, "normal", []testRouteGroup{{bindingID: "binding-one", groupID: "group-one", priority: 1}})
	if result := Build(Input{Request: openAIRequest(), Route: empty, Identity: Identity{TraceID: "trace-4", MutationID: "mutation-4"}}); result.Outcome() != OutcomeNoCandidate {
		t.Fatalf("no candidate result = %#v", result)
	}
	route := testRoute(t, "normal", []testRouteGroup{{bindingID: "binding-one", groupID: "group-one", priority: 1, candidates: []gatewaycandidatewindow.Candidate{candidate("account-a", "group-one")}}})
	codex := gatewayrequestprep.Prepare(gatewayrequestprep.Input{Method: "POST", Path: "/v1/responses", StreamRequested: true, CodexTurnMetadataValid: true})
	result := Build(Input{Request: codex, Route: route, Identity: Identity{TraceID: "trace-4", MutationID: "mutation-4"}})
	execution, ok := result.Execution()
	if !ok {
		t.Fatalf("result = %#v", result)
	}
	if protocol, allowed := execution.Capabilities().ControlledFailureProtocol(); !allowed || protocol != gatewaystreamrelay.ControlledFailureProtocolResponses || execution.Capabilities().PreCommitFailureSignal() != gatewayrequestprep.PreCommitFailureSignalProtocolEvent {
		t.Fatalf("capabilities = %#v protocol=%q allowed=%v", execution.Capabilities(), protocol, allowed)
	}
}

func TestBuildFromOrchestrationCarriesFrozenShapeAndFinalLane(t *testing.T) {
	t.Parallel()
	route := testRoute(t, "normal", []testRouteGroup{{
		bindingID: "binding-one", groupID: "group-one", priority: 1,
		candidates: []gatewaycandidatewindow.Candidate{candidate("account-a", "group-one")},
	}})
	request := gatewayrequestprep.Prepare(gatewayrequestprep.Input{
		Method: "POST", Path: "/v1/responses", RequestedModel: "gpt", StreamRequested: true,
	})
	orchestration := completeOrchestration(t, route, gatewayingress.LaneImage)
	decision := BuildFromOrchestration(OrchestratedInput{
		Request: request, Intent: orchestration.Intent, Orchestration: orchestration,
		Identity: Identity{TraceID: "trace-handoff", MutationID: "mutation-handoff"},
	})
	execution, ok := decision.Execution()
	if !ok || decision.Outcome() != OutcomeExecute {
		t.Fatalf("decision = %#v", decision)
	}
	if lane, ok := execution.FinalLane(); !ok || lane != gatewayingress.LaneImage {
		t.Fatalf("final lane = %q, present=%v", lane, ok)
	}
	shape, ok := execution.RequestShape()
	if !ok || shape.Model != "gpt" || !shape.Stream {
		t.Fatalf("request shape = %#v, present=%v", shape, ok)
	}
	shape.Headers["X-Juhe-Client-Profile"] = "forged"
	again, ok := decision.Execution()
	if !ok {
		t.Fatal("execution disappeared")
	}
	againShape, ok := again.RequestShape()
	if !ok || againShape.Header("X-Juhe-Client-Profile") == "forged" {
		t.Fatalf("execution leaked mutable request shape: %#v", againShape)
	}
}

func TestBuildFallbackTargetReplacesFrozenBatchWithFreshLaterWindow(t *testing.T) {
	t.Parallel()
	route := testRoute(t, "failover", []testRouteGroup{
		{bindingID: "binding-one", groupID: "group-one", priority: 1, candidates: []gatewaycandidatewindow.Candidate{candidate("account-one", "group-one")}},
		{bindingID: "binding-two", groupID: "group-two", priority: 2, candidates: []gatewaycandidatewindow.Candidate{candidate("account-two", "group-two")}},
	})
	request := gatewayrequestprep.Prepare(gatewayrequestprep.Input{Method: "POST", Path: "/v1/responses", RequestedModel: "gpt", StreamRequested: true})
	orchestration := completeOrchestration(t, route, gatewayingress.LaneImage)
	sourceDecision := BuildFromOrchestration(OrchestratedInput{
		Request: request, Intent: orchestration.Intent, Orchestration: orchestration,
		Identity: Identity{TraceID: "trace-fallback", MutationID: "mutation-fallback"},
	})
	source, ok := sourceDecision.Execution()
	if !ok || len(source.Batches()) != 2 {
		t.Fatalf("source=%#v decision=%#v", source, sourceDecision)
	}
	routeOnly := routeOnlyFromResult(route)
	current, err := gatewayrouteplan.InitialFallbackCursor(routeOnly, "binding-one")
	if err != nil {
		t.Fatal(err)
	}
	prepared := prepareDispatchFallbackTarget(t, routeOnly, current, map[string][]gatewaycandidatewindow.Candidate{
		"group-two": {candidate("account-fresh", "group-two")},
	}, gatewayingress.LaneImage)
	decision := BuildFallbackTarget(FallbackTargetInput{
		Source: source, Route: routeOnly, Current: current,
		Prepared: prepared, Reason: "runtime_degraded", EnteredGroupIDs: []string{"group-one"},
	})
	execution, ok := decision.Execution()
	if !ok || decision.Outcome() != OutcomeExecute || !reflect.DeepEqual(batchIDs(execution.Batches()), []string{"binding-two/group-two"}) {
		t.Fatalf("decision=%#v execution=%#v", decision, execution)
	}
	shape, hasShape := execution.RequestShape()
	lane, hasLane := execution.FinalLane()
	if !hasShape || !hasLane || shape.Model != "gpt" || lane != gatewayingress.LaneImage || execution.Identity() != source.Identity() || execution.APIKeyID() != source.APIKeyID() {
		t.Fatalf("fallback execution=%#v shape=%#v lane=%q", execution, shape, lane)
	}
	if got := candidateIDs(execution.Batches()[0].Candidates()); !reflect.DeepEqual(got, []string{"account-fresh"}) {
		t.Fatalf("target reused source candidate window: %v", got)
	}
}

func TestBuildFallbackTargetFailsClosedForMissingTargetSourceMismatchAndCommit(t *testing.T) {
	t.Parallel()
	route := testRoute(t, "failover", []testRouteGroup{
		{bindingID: "binding-one", groupID: "group-one", priority: 1, candidates: []gatewaycandidatewindow.Candidate{candidate("account-one", "group-one")}},
		{bindingID: "binding-two", groupID: "group-two", priority: 2, candidates: []gatewaycandidatewindow.Candidate{candidate("account-two", "group-two")}},
	})
	request := gatewayrequestprep.Prepare(gatewayrequestprep.Input{Method: "POST", Path: "/v1/responses", RequestedModel: "gpt", StreamRequested: true})
	orchestration := completeOrchestration(t, route, gatewayingress.LaneText)
	source, ok := BuildFromOrchestration(OrchestratedInput{Request: request, Intent: orchestration.Intent, Orchestration: orchestration, Identity: Identity{TraceID: "trace-fallback-fail", MutationID: "mutation-fallback-fail"}}).Execution()
	if !ok {
		t.Fatal("source execution missing")
	}
	routeOnly := routeOnlyFromResult(route)
	current, err := gatewayrouteplan.InitialFallbackCursor(routeOnly, "binding-one")
	if err != nil {
		t.Fatal(err)
	}
	base := FallbackTargetInput{Source: source, Route: routeOnly, Current: current, Prepared: prepareDispatchFallbackTarget(t, routeOnly, current, map[string][]gatewaycandidatewindow.Candidate{
		"group-two": {candidate("account-fresh", "group-two")},
	}, gatewayingress.LaneText), Reason: "runtime_degraded", EnteredGroupIDs: []string{"group-one"}}
	missing := base
	missing.Prepared = prepareDispatchFallbackTarget(t, routeOnly, current, nil, gatewayingress.LaneText)
	if got := BuildFallbackTarget(missing); got.Outcome() != OutcomeNoCandidate {
		t.Fatalf("missing target=%#v", got)
	}
	mismatch := base
	mismatch.Current, err = gatewayrouteplan.InitialFallbackCursor(routeOnly, "binding-two")
	if err != nil {
		t.Fatal(err)
	}
	if got := BuildFallbackTarget(mismatch); got.RejectReason() != RejectRoutePlanInvalid {
		t.Fatalf("mismatched source=%#v", got)
	}
	committed := base
	committed.Source.initialCommit = gatewaystreamrelay.SinkState{TransportCommitted: true}
	if got := BuildFallbackTarget(committed); got.RejectReason() != RejectCandidateSwitchCommitted {
		t.Fatalf("committed source=%#v", got)
	}
	foreignResult := testRoute(t, "failover", []testRouteGroup{
		{bindingID: "binding-one", groupID: "group-one", priority: 1, candidates: []gatewaycandidatewindow.Candidate{candidate("account-one", "group-one")}},
		{bindingID: "binding-two", groupID: "group-two", priority: 99, candidates: []gatewaycandidatewindow.Candidate{candidate("account-foreign", "group-two")}},
	})
	foreign := routeOnlyFromResult(foreignResult)
	foreignCurrent, err := gatewayrouteplan.InitialFallbackCursor(foreign, "binding-one")
	if err != nil {
		t.Fatal(err)
	}
	foreignPrepared := prepareDispatchFallbackTarget(t, foreign, foreignCurrent, map[string][]gatewaycandidatewindow.Candidate{
		"group-two": {candidate("account-foreign", "group-two")},
	}, gatewayingress.LaneText)
	if got := BuildFallbackTarget(FallbackTargetInput{Source: source, Route: foreign, Current: foreignCurrent, Prepared: foreignPrepared, Reason: "runtime_degraded", EnteredGroupIDs: []string{"group-one"}}); got.RejectReason() != RejectRoutePlanInvalid {
		t.Fatalf("foreign route plan=%#v", got)
	}
	wrongReason := base
	wrongReason.Reason = "group_capacity_busy"
	if got := BuildFallbackTarget(wrongReason); got.RejectReason() != RejectRoutePlanInvalid {
		t.Fatalf("different fallback reason=%#v", got)
	}
	wrongExcluded := base
	wrongExcluded.ExcludedAccountIDs = []string{"account-fresh"}
	if got := BuildFallbackTarget(wrongExcluded); got.RejectReason() != RejectRoutePlanInvalid {
		t.Fatalf("different excluded accounts=%#v", got)
	}
	wrongCompatibility := base
	wrongCompatibility.Source.capabilities.compatibility = "foreign_client"
	if got := BuildFallbackTarget(wrongCompatibility); got.RejectReason() != RejectRoutePlanInvalid {
		t.Fatalf("different compatibility=%#v", got)
	}
	wrongLane := base
	wrongLane.Source.finalLane = gatewayingress.LaneImage
	if got := BuildFallbackTarget(wrongLane); got.RejectReason() != RejectRoutePlanInvalid {
		t.Fatalf("different final lane=%#v", got)
	}
}

func TestBuildPreservesRuntimeWindowAndAPIKeyID(t *testing.T) {
	t.Parallel()
	expiresAt := time.Date(2026, time.August, 4, 8, 0, 0, 0, time.UTC)
	route := testRoute(t, "normal", []testRouteGroup{{
		bindingID: "binding-runtime", groupID: "group-runtime", priority: 1,
		candidates: []gatewaycandidatewindow.Candidate{candidate("account-runtime", "group-runtime")},
	}})
	route.Groups[0].Window.Access.GroupAuthorizationExpiresAt = &expiresAt
	decision := Build(Input{
		Request: openAIRequest(), Route: route,
		Identity: Identity{TraceID: "trace-runtime", MutationID: "mutation-runtime"},
	})
	execution, ok := decision.Execution()
	if !ok || execution.APIKeyID() != "key" {
		t.Fatalf("execution = %#v api key=%q", decision, execution.APIKeyID())
	}
	batches := execution.Batches()
	if len(batches) != 1 {
		t.Fatalf("batches = %#v", batches)
	}
	window := batches[0].RuntimeWindow()
	if window.Access.GroupID != "group-runtime" || window.Access.CallerSystemAccountID != "system" || window.Access.GroupType != "normal" {
		t.Fatalf("runtime window access = %#v", window.Access)
	}
	window.Access.GroupType = "forged"
	window.Candidates[0].SupportedModels[0] = "forged"
	*window.Access.GroupAuthorizationExpiresAt = expiresAt.Add(time.Hour)
	again := execution.Batches()[0].RuntimeWindow()
	if again.Access.GroupType == "forged" ||
		again.Candidates[0].SupportedModels[0] == "forged" ||
		again.Access.GroupAuthorizationExpiresAt == nil ||
		!again.Access.GroupAuthorizationExpiresAt.Equal(expiresAt) {
		t.Fatalf("runtime window leaked mutable state: %#v", again)
	}
}

func TestBuildRejectsMismatchedRuntimeWindowAccess(t *testing.T) {
	t.Parallel()
	route := testRoute(t, "normal", []testRouteGroup{{
		bindingID: "binding-window", groupID: "group-window", priority: 1,
		candidates: []gatewaycandidatewindow.Candidate{candidate("account-window", "group-window")},
	}})
	route.Groups[0].Window.Access.GroupID = "forged-group"
	result := Build(Input{
		Request: openAIRequest(), Route: route,
		Identity: Identity{TraceID: "trace-window", MutationID: "mutation-window"},
	})
	if result.Outcome() != OutcomeReject || result.RejectReason() != RejectRoutePlanInvalid {
		t.Fatalf("result = %#v", result)
	}
}

func TestBuildFromOrchestrationFailsClosedForIncompleteStages(t *testing.T) {
	t.Parallel()
	route := testRoute(t, "normal", []testRouteGroup{{
		bindingID: "binding-one", groupID: "group-one", priority: 1,
		candidates: []gatewaycandidatewindow.Candidate{candidate("account-a", "group-one")},
	}})
	base := OrchestratedInput{
		Request: openAIRequest(), Identity: Identity{TraceID: "trace-incomplete", MutationID: "mutation-incomplete"},
	}
	if got := BuildFromOrchestration(base); got.RejectReason() != RejectOrchestrationIncomplete {
		t.Fatalf("missing orchestration = %#v", got)
	}
	base.Orchestration = gatewayrequestorchestration.Result{Preflight: route.Preflight, Route: &route}
	if got := BuildFromOrchestration(base); got.RejectReason() != RejectOrchestrationIncomplete {
		t.Fatalf("missing ingress = %#v", got)
	}
	complete := completeOrchestration(t, route, gatewayingress.LaneText)
	base.Intent = complete.Intent
	base.Request = gatewayrequestprep.Prepare(gatewayrequestprep.Input{Method: "POST", Path: "/v1/responses", RequestedModel: "other-model"})
	base.Orchestration = complete
	if got := BuildFromOrchestration(base); got.RejectReason() != RejectOrchestrationIncomplete {
		t.Fatalf("mismatched request model = %#v", got)
	}
	base.Request = openAIRequest()
	base.Intent = gatewayingress.RequestIntent{}
	base.Orchestration = complete
	if got := BuildFromOrchestration(base); got.RejectReason() != RejectOrchestrationIncomplete {
		t.Fatalf("missing parsed boundary intent = %#v", got)
	}
	base.Intent = complete.Intent
	complete.Ingress.Preflight = gatewaypreflight.Result{}
	base.Orchestration = complete
	if got := BuildFromOrchestration(base); got.RejectReason() != RejectOrchestrationIncomplete {
		t.Fatalf("forged ingress preflight = %#v", got)
	}
}

func TestBuildLegacyPathDoesNotClaimIngressHandoff(t *testing.T) {
	t.Parallel()
	route := testRoute(t, "normal", []testRouteGroup{{
		bindingID: "binding-one", groupID: "group-one", priority: 1,
		candidates: []gatewaycandidatewindow.Candidate{candidate("account-a", "group-one")},
	}})
	execution, ok := Build(Input{Request: openAIRequest(), Route: route, Identity: Identity{TraceID: "trace-legacy", MutationID: "mutation-legacy"}}).Execution()
	if !ok {
		t.Fatal("legacy execution was rejected")
	}
	if _, ok := execution.RequestShape(); ok {
		t.Fatal("legacy execution claimed a request-shape handoff")
	}
	if _, ok := execution.FinalLane(); ok {
		t.Fatal("legacy execution claimed a final-lane handoff")
	}
}

func completeOrchestration(t *testing.T, route gatewayrouteplan.Result, lane gatewayingress.Lane) gatewayrequestorchestration.Result {
	t.Helper()
	intent, err := gatewayingress.Parse(gatewayingress.ParseInput{RawBody: []byte(`{"model":"gpt","stream":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := gatewayingress.NewSnapshot(gatewayingress.SnapshotInput{
		Revision: "snapshot-execution", Model: "gpt", CandidateCapacity: 1,
		ToolCatalog: map[string]struct{}{}, ToolCatalogComplete: true, MappingLane: lane,
	})
	if err != nil {
		t.Fatal(err)
	}
	finalization, err := gatewayingress.Finalize(intent, snapshot, true)
	if err != nil {
		t.Fatal(err)
	}
	admission, err := gatewayingress.Admit(finalization)
	if err != nil {
		t.Fatal(err)
	}
	ingress := gatewayingressplan.Result{Preflight: route.Preflight, Finalization: &finalization, Admission: &admission}
	return gatewayrequestorchestration.Result{Preflight: route.Preflight, Intent: intent, Route: &route, Ingress: &ingress}
}

type testRouteGroup struct {
	bindingID  string
	groupID    string
	priority   int
	candidates []gatewaycandidatewindow.Candidate
}

func routeOnlyFromResult(route gatewayrouteplan.Result) gatewayrouteplan.RouteOnlyResult {
	result, err := gatewayrouteplan.RouteOnlyFromResult(route)
	if err != nil {
		panic(err)
	}
	return result
}

func prepareDispatchFallbackTarget(t *testing.T, route gatewayrouteplan.RouteOnlyResult, current gatewayrouteplan.FallbackCursor, candidates map[string][]gatewaycandidatewindow.Candidate, lane gatewayingress.Lane) gatewayrouteplan.FallbackDispatchPreparedTarget {
	t.Helper()
	service, err := gatewayrouteplan.NewService(gatewayrouteplan.Options{
		Preflight: testFallbackPreflightResolver{}, Coordinator: gatewayroutecoordination.NewMemoryStore(), Candidates: testCandidateLoader{byGroup: candidates},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := gatewayrequestprep.Prepare(gatewayrequestprep.Input{Method: "POST", Path: "/v1/responses", RequestedModel: "gpt", StreamRequested: true})
	prepared, err := service.PrepareDispatchFallbackTarget(context.Background(), gatewayrouteplan.FallbackDispatchPreparedInput{
		FallbackPreparedInput: gatewayrouteplan.FallbackPreparedInput{Route: route, Current: current, EnteredGroupIDs: []string{current.GroupID()}, RequestedModel: "gpt", EndpointFamily: "responses"},
		Intent:                fallbackIntent(t, "gpt", true), IngressFinalization: fallbackFinalization(t, lane), RequestShape: request.RequestShape(), Protocol: protocolgateway.ProtocolOpenAI, FinalLane: lane,
		Reason: "runtime_degraded", RequestClientCompatibility: "openai_standard", RequestLane: string(lane), Policy: executionFallbackPolicy{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}

func fallbackIntent(t *testing.T, model string, stream bool) gatewayingress.RequestIntent {
	t.Helper()
	raw := []byte(`{"model":"` + model + `","stream":true}`)
	if !stream {
		raw = []byte(`{"model":"` + model + `","stream":false}`)
	}
	intent, err := gatewayingress.Parse(gatewayingress.ParseInput{RawBody: raw})
	if err != nil {
		t.Fatal(err)
	}
	return intent
}

func fallbackFinalization(t *testing.T, lane gatewayingress.Lane) gatewayingress.FinalResult {
	t.Helper()
	snapshot, err := gatewayingress.NewSnapshot(gatewayingress.SnapshotInput{
		Revision: "snapshot-execution", Model: "gpt", CandidateCapacity: 1,
		ToolCatalog: map[string]struct{}{}, ToolCatalogComplete: true, MappingLane: lane,
	})
	if err != nil {
		t.Fatal(err)
	}
	finalization, err := gatewayingress.Finalize(fallbackIntent(t, "gpt", true), snapshot, true)
	if err != nil {
		t.Fatal(err)
	}
	return finalization
}

type executionFallbackPolicy struct{}

func (executionFallbackPolicy) SelectFallbackCandidates(_ context.Context, input gatewayrouteplan.FallbackCandidatePolicyInput) (gatewayrouteplan.FallbackCandidatePolicyResult, error) {
	ids := make([]string, 0, len(input.Window.Candidates))
	for _, candidate := range input.Window.Candidates {
		ids = append(ids, candidate.Projection.AccountID)
	}
	return gatewayrouteplan.FallbackCandidatePolicyResult{CandidateAccountIDs: ids}, nil
}

func testRoute(t *testing.T, mode string, groups []testRouteGroup) gatewayrouteplan.Result {
	t.Helper()
	bindings := make([]port.GatewayPreflightBindingRecord, 0, len(groups))
	candidates := make(map[string][]gatewaycandidatewindow.Candidate, len(groups))
	for _, group := range groups {
		bindings = append(bindings, port.GatewayPreflightBindingRecord{ID: group.bindingID, APIKeyID: "key", SystemAccountID: "system", GroupID: group.groupID, Priority: group.priority, Weight: 1, Status: "active", GroupEnabled: true})
		candidates[group.groupID] = append([]gatewaycandidatewindow.Candidate(nil), group.candidates...)
	}
	store := &testPreflightStore{key: port.GatewayPreflightAPIKeyRecord{ID: "key", SystemAccountID: "system", APIKeyStatus: "active", SystemAccountStatus: "active", RouteStrategyID: "route", RouteStrategyStatus: "active", RouteStrategyMode: mode, RouteDispatchGeneration: 7}, bindings: bindings}
	preflight := gatewaypreflight.NewService(gatewaypreflight.ServiceOptions{Store: store, Now: func() time.Time { return time.Unix(0, 0) }})
	service, err := gatewayrouteplan.NewService(gatewayrouteplan.Options{Preflight: preflight, Coordinator: gatewayroutecoordination.NewMemoryStore(), Candidates: testCandidateLoader{byGroup: candidates}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Build(context.Background(), gatewayrouteplan.Input{RawAPIKey: "sk-execution"})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

type testPreflightStore struct {
	key      port.GatewayPreflightAPIKeyRecord
	bindings []port.GatewayPreflightBindingRecord
}

type testFallbackPreflightResolver struct{}

func (testFallbackPreflightResolver) Resolve(context.Context, string) (gatewaypreflight.Result, error) {
	return gatewaypreflight.Result{}, nil
}

func (s *testPreflightStore) LoadGatewayPreflightAPIKey(context.Context, string) (port.GatewayPreflightAPIKeyRecord, bool, error) {
	return s.key, true, nil
}
func (s *testPreflightStore) ListGatewayPreflightBindings(context.Context, string, string, string, time.Time, int) ([]port.GatewayPreflightBindingRecord, error) {
	return append([]port.GatewayPreflightBindingRecord(nil), s.bindings...), nil
}
func (s *testPreflightStore) LoadGatewayPreflightSettings(context.Context) (port.GatewayPreflightSettingsRecord, error) {
	return port.GatewayPreflightSettingsRecord{}, nil
}

type testCandidateLoader struct {
	byGroup map[string][]gatewaycandidatewindow.Candidate
}

func (l testCandidateLoader) Load(_ context.Context, input gatewaycandidatewindow.LoadInput) (gatewaycandidatewindow.Window, bool, error) {
	values, exists := l.byGroup[input.GroupID]
	return gatewaycandidatewindow.Window{
		Access:     port.GatewayGroupAccess{GroupID: input.GroupID, CallerSystemAccountID: input.SystemAccountID, GroupType: "normal"},
		Candidates: append([]gatewaycandidatewindow.Candidate(nil), values...),
	}, exists, nil
}

func candidate(id, groupID string) gatewaycandidatewindow.Candidate {
	return gatewaycandidatewindow.Candidate{Projection: port.GatewayAccountCandidate{AccountID: id, SystemAccountID: "system", GroupID: groupID, Name: id}, SupportedModels: []string{"gpt"}}
}

func openAIRequest() gatewayrequestprep.Result {
	return gatewayrequestprep.Prepare(gatewayrequestprep.Input{Method: "POST", Path: "/v1/responses"})
}

func batchIDs(batches []Batch) []string {
	result := make([]string, 0, len(batches))
	for _, batch := range batches {
		result = append(result, batch.BindingID()+"/"+batch.GroupID())
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

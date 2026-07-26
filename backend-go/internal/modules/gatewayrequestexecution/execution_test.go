package gatewayrequestexecution

import (
	"context"
	"reflect"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
	"juhe-ai/backend-go/internal/modules/gatewaypreflight"
	"juhe-ai/backend-go/internal/modules/gatewayrequestprep"
	"juhe-ai/backend-go/internal/modules/gatewayroutecoordination"
	"juhe-ai/backend-go/internal/modules/gatewayrouteplan"
	"juhe-ai/backend-go/internal/modules/gatewaystreamrelay"
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
	batches[0].candidates[0].Projection.Name = "forged"
	batches[0].candidates[0].SupportedModels = []string{"forged"}
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

type testRouteGroup struct {
	bindingID  string
	groupID    string
	priority   int
	candidates []gatewaycandidatewindow.Candidate
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
	return gatewaycandidatewindow.Window{Candidates: append([]gatewaycandidatewindow.Candidate(nil), values...)}, exists, nil
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

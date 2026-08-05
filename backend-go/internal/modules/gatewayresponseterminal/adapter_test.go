package gatewayresponseterminal

import (
	"context"
	"errors"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
	"juhe-ai/backend-go/internal/modules/gatewayhttpcompletion"
	"juhe-ai/backend-go/internal/modules/gatewaypreflight"
	"juhe-ai/backend-go/internal/modules/gatewayrequestexecution"
	"juhe-ai/backend-go/internal/modules/gatewayrequestlifecycle"
	"juhe-ai/backend-go/internal/modules/gatewayrequestprep"
	"juhe-ai/backend-go/internal/modules/gatewayresponse"
	"juhe-ai/backend-go/internal/modules/gatewayroutecoordination"
	"juhe-ai/backend-go/internal/modules/gatewayrouteplan"
	"juhe-ai/backend-go/internal/modules/gatewaystreamrelay"
	"juhe-ai/backend-go/internal/store/port"
)

func TestRecordProtocolValidatedSuccessCompletesOnlyThroughTerminalObserver(t *testing.T) {
	lifecycle, attempt := activeLifecycle(t)
	terminal := gatewayhttpcompletion.New(nil)
	adapter, err := New(Input{Terminal: terminal, Attempt: attempt})
	if err != nil {
		t.Fatal(err)
	}

	if err := adapter.Record(successObservation()); err != nil {
		t.Fatal(err)
	}
	if _, completed := terminal.Terminal(); completed {
		t.Fatal("recorded success completed before response owner reported completion")
	}
	if snapshot := lifecycle.Snapshot(); snapshot.State != gatewayrequestlifecycle.StateSemanticCommitted || snapshot.IsTerminal {
		t.Fatalf("recorded lifecycle=%+v", snapshot)
	}
	if err := adapter.CompleteResponse(); err != nil {
		t.Fatal(err)
	}
	completed, ok := terminal.Terminal()
	if !ok || completed.Reason != gatewayhttpcompletion.TerminalResponseFinished {
		t.Fatalf("terminal=%+v completed=%v", completed, ok)
	}
	if snapshot := lifecycle.Snapshot(); snapshot.State != gatewayrequestlifecycle.StateSucceeded || !snapshot.IsTerminal {
		t.Fatalf("lifecycle=%+v", snapshot)
	}
}

func TestDeferredHandoffRecordsFactsBeforeResponseCompletion(t *testing.T) {
	lifecycle, attempt := activeLifecycle(t)
	terminal := gatewayhttpcompletion.New(nil)
	observation := successObservation()
	handoff, err := NewHandoff(attempt, observation.Response, observation.Downstream)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := NewFromHandoff(terminal, handoff)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.RecordHandoff(DispositionProtocolValidatedSuccess, WriterActionProtocolSuccess, true); err != nil {
		t.Fatal(err)
	}
	if _, completed := terminal.Terminal(); completed {
		t.Fatal("handoff record completed response before owner completion")
	}
	if snapshot := lifecycle.Snapshot(); snapshot.State != gatewayrequestlifecycle.StateSemanticCommitted || snapshot.IsTerminal {
		t.Fatalf("handoff record lifecycle=%+v", snapshot)
	}
	if err := adapter.CompleteResponse(); err != nil {
		t.Fatal(err)
	}
	if snapshot := lifecycle.Snapshot(); snapshot.State != gatewayrequestlifecycle.StateSucceeded || !snapshot.IsTerminal {
		t.Fatalf("handoff complete lifecycle=%+v", snapshot)
	}
}

func TestRecordRejectsSuccessWithoutExplicitProtocolValidation(t *testing.T) {
	lifecycle, attempt := activeLifecycle(t)
	terminal := gatewayhttpcompletion.New(nil)
	adapter, err := New(Input{Terminal: terminal, Attempt: attempt})
	if err != nil {
		t.Fatal(err)
	}
	observation := successObservation()
	observation.ProtocolValidatedSuccess = false
	if err := adapter.Record(observation); !errors.Is(err, ErrProtocolValidationRequired) {
		t.Fatalf("settle error=%v", err)
	}
	if _, completed := terminal.Terminal(); completed {
		t.Fatal("missing protocol validation completed terminal")
	}
	if snapshot := lifecycle.Snapshot(); snapshot.State != gatewayrequestlifecycle.StateAttempting || snapshot.IsTerminal {
		t.Fatalf("lifecycle=%+v", snapshot)
	}
}

func TestRecordPreCommitRetryDoesNotCompleteRequest(t *testing.T) {
	lifecycle, attempt := activeLifecycle(t)
	terminal := gatewayhttpcompletion.New(nil)
	adapter, err := New(Input{Terminal: terminal, Attempt: attempt})
	if err != nil {
		t.Fatal(err)
	}
	observation := Observation{
		Response:     gatewayresponse.Result{State: gatewayresponse.StateFailedBeforeCommit, RetryAllowed: true},
		Disposition:  DispositionRetryPreCommit,
		WriterAction: WriterActionNone,
	}
	if err := adapter.Record(observation); err != nil {
		t.Fatal(err)
	}
	if _, completed := terminal.Terminal(); completed {
		t.Fatal("pre-commit retry completed terminal")
	}
	if snapshot := lifecycle.Snapshot(); snapshot.State != gatewayrequestlifecycle.StateReady || snapshot.IsTerminal || snapshot.Attempts != 1 {
		t.Fatalf("lifecycle=%+v", snapshot)
	}
}

func TestRecordForwardedUpstreamFailureKeepsClientCancellationDistinct(t *testing.T) {
	lifecycle, attempt := activeLifecycle(t)
	terminal := gatewayhttpcompletion.New(nil)
	adapter, err := New(Input{Terminal: terminal, Attempt: attempt})
	if err != nil {
		t.Fatal(err)
	}
	observation := Observation{
		Response: gatewayresponse.Result{
			State:              gatewayresponse.StateUpstreamFailureForwarded,
			BytesWritten:       18,
			TransportCommitted: true,
			SemanticCommitted:  true,
		},
		Downstream:   gatewaystreamrelay.SinkState{TransportCommitted: true, SemanticCommitted: true, DownstreamBytes: 18},
		Disposition:  DispositionUpstreamFailure,
		WriterAction: WriterActionForwardedUpstreamFailure,
	}
	if err := adapter.Record(observation); err != nil {
		t.Fatal(err)
	}
	if err := adapter.CompleteResponse(); err != nil {
		t.Fatal(err)
	}
	if snapshot := lifecycle.Snapshot(); snapshot.State != gatewayrequestlifecycle.StateFailed || snapshot.Failure != gatewayrequestlifecycle.FailureUpstream {
		t.Fatalf("lifecycle=%+v", snapshot)
	}
}

func TestClientCancellationWinsBeforeSettlementAndDoesNotBecomeGatewayFailure(t *testing.T) {
	lifecycle, attempt := activeLifecycle(t)
	terminal := gatewayhttpcompletion.New(nil)
	adapter, err := New(Input{Terminal: terminal, Attempt: attempt})
	if err != nil {
		t.Fatal(err)
	}
	terminal.CompleteClientCanceled()
	if err := adapter.Record(successObservation()); !errors.Is(err, ErrClientCanceled) {
		t.Fatalf("record error=%v", err)
	}
	if snapshot := lifecycle.Snapshot(); snapshot.State != gatewayrequestlifecycle.StateFailed || snapshot.Failure != gatewayrequestlifecycle.FailureClientCanceled {
		t.Fatalf("lifecycle=%+v", snapshot)
	}
}

func TestCompleteResponseReportsContextCancellationThatWins(t *testing.T) {
	contextValue, cancel := context.WithCancel(context.Background())
	lifecycle, attempt := activeLifecycle(t)
	terminal := gatewayhttpcompletion.New(contextValue)
	adapter, err := New(Input{Terminal: terminal, Attempt: attempt})
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Record(successObservation()); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := adapter.CompleteResponse(); !errors.Is(err, ErrClientCanceled) {
		t.Fatalf("complete error=%v", err)
	}
	if snapshot := lifecycle.Snapshot(); snapshot.State != gatewayrequestlifecycle.StateFailed || snapshot.Failure != gatewayrequestlifecycle.FailureClientCanceled {
		t.Fatalf("lifecycle=%+v", snapshot)
	}
}

func TestResponseFinishWithoutSettlementFailsClosed(t *testing.T) {
	lifecycle, attempt := activeLifecycle(t)
	terminal := gatewayhttpcompletion.New(nil)
	adapter, err := New(Input{Terminal: terminal, Attempt: attempt})
	if err != nil {
		t.Fatal(err)
	}
	terminal.Complete()
	if err := adapter.Err(); !errors.Is(err, ErrResponseFinishedWithoutSettlement) {
		t.Fatalf("adapter error=%v", err)
	}
	if snapshot := lifecycle.Snapshot(); snapshot.IsTerminal {
		t.Fatalf("unproven response finish terminalized lifecycle=%+v", snapshot)
	}
}

func TestCompleteResponseWithoutRecordDoesNotCompleteObserver(t *testing.T) {
	lifecycle, attempt := activeLifecycle(t)
	terminal := gatewayhttpcompletion.New(nil)
	adapter, err := New(Input{Terminal: terminal, Attempt: attempt})
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.CompleteResponse(); !errors.Is(err, ErrResponseFinishedWithoutSettlement) {
		t.Fatalf("complete error=%v", err)
	}
	if _, completed := terminal.Terminal(); completed {
		t.Fatal("completion without record completed terminal")
	}
	if snapshot := lifecycle.Snapshot(); snapshot.IsTerminal {
		t.Fatalf("completion without record terminalized lifecycle=%+v", snapshot)
	}
}

func TestRecordGatewayFailureRequiresActualControlledErrorCommit(t *testing.T) {
	lifecycle, attempt := activeLifecycle(t)
	terminal := gatewayhttpcompletion.New(nil)
	adapter, err := New(Input{Terminal: terminal, Attempt: attempt})
	if err != nil {
		t.Fatal(err)
	}
	observation := Observation{
		Response:     gatewayresponse.Result{State: gatewayresponse.StateFailedBeforeCommit},
		Downstream:   gatewaystreamrelay.SinkState{TransportCommitted: true},
		Disposition:  DispositionGatewayFailure,
		WriterAction: WriterActionControlledError,
	}
	if err := adapter.Record(observation); err != nil {
		t.Fatal(err)
	}
	if err := adapter.CompleteResponse(); err != nil {
		t.Fatal(err)
	}
	if snapshot := lifecycle.Snapshot(); snapshot.State != gatewayrequestlifecycle.StateFailed || snapshot.Failure != gatewayrequestlifecycle.FailureGateway {
		t.Fatalf("lifecycle=%+v", snapshot)
	}
}

func TestRecordRejectsRetryAfterDownstreamCommit(t *testing.T) {
	_, attempt := activeLifecycle(t)
	adapter, err := New(Input{Terminal: gatewayhttpcompletion.New(nil), Attempt: attempt})
	if err != nil {
		t.Fatal(err)
	}
	observation := Observation{
		Response:     gatewayresponse.Result{State: gatewayresponse.StateFailedBeforeCommit, RetryAllowed: true},
		Downstream:   gatewaystreamrelay.SinkState{TransportCommitted: true},
		Disposition:  DispositionRetryPreCommit,
		WriterAction: WriterActionNone,
	}
	if err := adapter.Record(observation); !errors.Is(err, ErrRetryDispositionCommitted) {
		t.Fatalf("record error=%v", err)
	}
}

func activeLifecycle(t *testing.T) (*gatewayrequestlifecycle.Lifecycle, *gatewayrequestlifecycle.AttemptLoopAdapter) {
	t.Helper()
	lifecycle := newLifecycle(t)
	attempt, err := gatewayrequestlifecycle.NewAttemptLoopAdapter(lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	if err := attempt.Start(); err != nil {
		t.Fatal(err)
	}
	return lifecycle, attempt
}

func successObservation() Observation {
	return Observation{
		Response: gatewayresponse.Result{
			State:              gatewayresponse.StateSucceeded,
			TransportCommitted: true,
			SemanticCommitted:  true,
		},
		Downstream:               gatewaystreamrelay.SinkState{TransportCommitted: true, SemanticCommitted: true},
		Disposition:              DispositionProtocolValidatedSuccess,
		WriterAction:             WriterActionProtocolSuccess,
		ProtocolValidatedSuccess: true,
	}
}

func newLifecycle(t *testing.T) *gatewayrequestlifecycle.Lifecycle {
	t.Helper()
	bindings := []port.GatewayPreflightBindingRecord{{ID: "binding", APIKeyID: "key", SystemAccountID: "system", GroupID: "group", Priority: 1, Weight: 1, Status: "active", GroupEnabled: true}}
	store := &preflightStore{key: port.GatewayPreflightAPIKeyRecord{ID: "key", SystemAccountID: "system", APIKeyStatus: "active", SystemAccountStatus: "active", RouteStrategyID: "route", RouteStrategyStatus: "active", RouteStrategyMode: "failover", RouteDispatchGeneration: 1}, bindings: bindings}
	preflight := gatewaypreflight.NewService(gatewaypreflight.ServiceOptions{Store: store, Now: func() time.Time { return time.Unix(0, 0) }})
	candidates := candidateLoader{values: map[string][]gatewaycandidatewindow.Candidate{"group": {{Projection: port.GatewayAccountCandidate{AccountID: "account", SystemAccountID: "system", GroupID: "group", Name: "account"}, SupportedModels: []string{"gpt"}}}}}
	routes, err := gatewayrouteplan.NewService(gatewayrouteplan.Options{Preflight: preflight, Coordinator: gatewayroutecoordination.NewMemoryStore(), Candidates: candidates})
	if err != nil {
		t.Fatal(err)
	}
	route, err := routes.Build(context.Background(), gatewayrouteplan.Input{RawAPIKey: "sk-terminal"})
	if err != nil {
		t.Fatal(err)
	}
	prepared := gatewayrequestprep.Prepare(gatewayrequestprep.Input{Method: "POST", Path: "/v1/responses"})
	decision := gatewayrequestexecution.Build(gatewayrequestexecution.Input{Request: prepared, Route: route, Identity: gatewayrequestexecution.Identity{TraceID: "trace", MutationID: "mutation"}})
	execution, ok := decision.Execution()
	if !ok {
		t.Fatalf("execution=%#v", decision)
	}
	lifecycle, err := gatewayrequestlifecycle.New(execution)
	if err != nil {
		t.Fatal(err)
	}
	return lifecycle
}

type preflightStore struct {
	key      port.GatewayPreflightAPIKeyRecord
	bindings []port.GatewayPreflightBindingRecord
}

func (s *preflightStore) LoadGatewayPreflightAPIKey(context.Context, string) (port.GatewayPreflightAPIKeyRecord, bool, error) {
	return s.key, true, nil
}

func (s *preflightStore) ListGatewayPreflightBindings(context.Context, string, string, string, time.Time, int) ([]port.GatewayPreflightBindingRecord, error) {
	return append([]port.GatewayPreflightBindingRecord(nil), s.bindings...), nil
}

func (s *preflightStore) LoadGatewayPreflightSettings(context.Context) (port.GatewayPreflightSettingsRecord, error) {
	return port.GatewayPreflightSettingsRecord{}, nil
}

type candidateLoader struct {
	values map[string][]gatewaycandidatewindow.Candidate
}

func (l candidateLoader) Load(_ context.Context, input gatewaycandidatewindow.LoadInput) (gatewaycandidatewindow.Window, bool, error) {
	values, found := l.values[input.GroupID]
	return gatewaycandidatewindow.Window{
		Access:     port.GatewayGroupAccess{GroupID: input.GroupID, CallerSystemAccountID: input.SystemAccountID, GroupType: "normal"},
		Candidates: append([]gatewaycandidatewindow.Candidate(nil), values...),
	}, found, nil
}

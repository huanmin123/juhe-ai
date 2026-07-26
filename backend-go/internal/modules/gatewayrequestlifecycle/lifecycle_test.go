package gatewayrequestlifecycle

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
	"juhe-ai/backend-go/internal/modules/gatewaypreflight"
	"juhe-ai/backend-go/internal/modules/gatewayrequestexecution"
	"juhe-ai/backend-go/internal/modules/gatewayrequestprep"
	"juhe-ai/backend-go/internal/modules/gatewayroutecoordination"
	"juhe-ai/backend-go/internal/modules/gatewayrouteplan"
	"juhe-ai/backend-go/internal/modules/gatewaystreamrelay"
	"juhe-ai/backend-go/internal/store/port"
)

func TestLifecycleStartAndPreCommitRetryUseFreshOpaqueAttempt(t *testing.T) {
	lifecycle := mustLifecycle(t)
	first, err := lifecycle.Start()
	if err != nil || first.Number() != 1 {
		t.Fatalf("first start=%#v error=%v", first, err)
	}
	retried, err := lifecycle.RetryPreCommit(first)
	if err != nil || retried.State != StateReady || retried.Attempts != 1 || retried.IsTerminal {
		t.Fatalf("retry snapshot=%#v error=%v", retried, err)
	}
	second, err := lifecycle.Start()
	if err != nil || second.Number() != 2 {
		t.Fatalf("second start=%#v error=%v", second, err)
	}
	if _, err := lifecycle.FinishFailure(first, FailureUpstream); !errors.Is(err, ErrStaleAttempt) {
		t.Fatalf("late first-attempt failure error = %v", err)
	}
	if _, err := lifecycle.FinishFailure(second, FailureUpstream); err != nil {
		t.Fatalf("second-attempt failure: %v", err)
	}
}

func TestLifecycleSinkCommitIsMonotonicAndBlocksRetry(t *testing.T) {
	lifecycle := mustLifecycle(t)
	attempt := mustStart(t, lifecycle)
	transport, err := lifecycle.ObserveSink(attempt, gatewaystreamrelay.SinkState{TransportCommitted: true})
	if err != nil || transport.State != StateTransportCommitted || transport.Sink.DownstreamBytes != 0 {
		t.Fatalf("transport snapshot=%#v error=%v", transport, err)
	}
	if _, err := lifecycle.RetryPreCommit(attempt); !errors.Is(err, ErrRetryAfterCommit) {
		t.Fatalf("retry after transport commit error = %v", err)
	}
	semantic, err := lifecycle.ObserveSink(attempt, gatewaystreamrelay.SinkState{TransportCommitted: true, SemanticCommitted: true, DownstreamBytes: 7})
	if err != nil || semantic.State != StateSemanticCommitted {
		t.Fatalf("semantic snapshot=%#v error=%v", semantic, err)
	}
	if _, err := lifecycle.ObserveSink(attempt, gatewaystreamrelay.SinkState{TransportCommitted: true}); !errors.Is(err, ErrSinkStateRegression) {
		t.Fatalf("semantic regression error = %v", err)
	}
	if _, err := lifecycle.ObserveSink(attempt, gatewaystreamrelay.SinkState{SemanticCommitted: true}); !errors.Is(err, ErrInvalidSinkState) {
		t.Fatalf("semantic without transport error = %v", err)
	}
}

func TestLifecycleTerminalFirstWinsAndCannotRecover(t *testing.T) {
	lifecycle := mustLifecycle(t)
	attempt := mustStart(t, lifecycle)
	if _, err := lifecycle.FinishSuccess(attempt); !errors.Is(err, ErrSuccessBeforeCommit) {
		t.Fatalf("success before commit error = %v", err)
	}
	if _, err := lifecycle.ObserveSink(attempt, gatewaystreamrelay.SinkState{TransportCommitted: true}); err != nil {
		t.Fatal(err)
	}
	completed, err := lifecycle.FinishSuccess(attempt)
	if err != nil || completed.State != StateSucceeded || !completed.IsTerminal {
		t.Fatalf("success snapshot=%#v error=%v", completed, err)
	}
	lost, err := lifecycle.FinishFailure(attempt, FailureGateway)
	if !errors.Is(err, ErrTerminal) || lost.State != StateSucceeded || lost.Failure != "" {
		t.Fatalf("late failure snapshot=%#v error=%v", lost, err)
	}
	if _, err := lifecycle.Start(); !errors.Is(err, ErrTerminal) {
		t.Fatalf("restart after terminal error = %v", err)
	}
}

func TestLifecycleCancelClientCanWinBeforeStartAndAfterCommit(t *testing.T) {
	t.Run("ready", func(t *testing.T) {
		lifecycle := mustLifecycle(t)
		canceled, err := lifecycle.CancelClient()
		if err != nil || canceled.State != StateFailed || canceled.Failure != FailureClientCanceled || !canceled.IsTerminal {
			t.Fatalf("ready cancellation snapshot=%#v error=%v", canceled, err)
		}
		if _, err := lifecycle.Start(); !errors.Is(err, ErrTerminal) {
			t.Fatalf("start after ready cancellation error = %v", err)
		}
		if _, err := lifecycle.FinishClientCanceled(); !errors.Is(err, ErrTerminal) {
			t.Fatalf("second cancellation error = %v", err)
		}
	})
	t.Run("committed", func(t *testing.T) {
		lifecycle := mustLifecycle(t)
		attempt := mustStart(t, lifecycle)
		if _, err := lifecycle.ObserveSink(attempt, gatewaystreamrelay.SinkState{TransportCommitted: true, SemanticCommitted: true, DownstreamBytes: 3}); err != nil {
			t.Fatal(err)
		}
		canceled, err := lifecycle.FinishClientCanceled()
		if err != nil || canceled.State != StateFailed || canceled.Failure != FailureClientCanceled {
			t.Fatalf("committed cancellation snapshot=%#v error=%v", canceled, err)
		}
		if _, err := lifecycle.FinishSuccess(attempt); !errors.Is(err, ErrTerminal) {
			t.Fatalf("success after cancellation error = %v", err)
		}
	})
}

func TestLifecycleConcurrentTerminalFirstWins(t *testing.T) {
	lifecycle := mustLifecycle(t)
	attempt := mustStart(t, lifecycle)
	if _, err := lifecycle.ObserveSink(attempt, gatewaystreamrelay.SinkState{TransportCommitted: true}); err != nil {
		t.Fatal(err)
	}

	const callers = 40
	var group sync.WaitGroup
	results := make(chan error, callers)
	for index := 0; index < callers; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			if index%2 == 0 {
				_, err := lifecycle.FinishSuccess(attempt)
				results <- err
				return
			}
			_, err := lifecycle.FinishFailure(attempt, FailureUpstream)
			results <- err
		}(index)
	}
	group.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
			continue
		}
		if !errors.Is(err, ErrTerminal) {
			t.Fatalf("terminal competitor error = %v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("terminal winners = %d, want 1", winners)
	}
}

func TestLifecycleConcurrentClientCancelSuccessAndFailureFirstTerminalWins(t *testing.T) {
	lifecycle := mustLifecycle(t)
	attempt := mustStart(t, lifecycle)
	if _, err := lifecycle.ObserveSink(attempt, gatewaystreamrelay.SinkState{TransportCommitted: true}); err != nil {
		t.Fatal(err)
	}

	var group sync.WaitGroup
	results := make(chan error, 3)
	group.Add(3)
	go func() { defer group.Done(); _, err := lifecycle.CancelClient(); results <- err }()
	go func() { defer group.Done(); _, err := lifecycle.FinishSuccess(attempt); results <- err }()
	go func() {
		defer group.Done()
		_, err := lifecycle.FinishFailure(attempt, FailureUpstream)
		results <- err
	}()
	group.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
			continue
		}
		if !errors.Is(err, ErrTerminal) {
			t.Fatalf("terminal competitor error = %v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("terminal winners = %d, want 1", winners)
	}
	if snapshot := lifecycle.Snapshot(); !snapshot.IsTerminal {
		t.Fatalf("final snapshot = %#v", snapshot)
	}
}

func TestLifecycleConcurrentRetryAndTerminalCannotCrossAttemptGeneration(t *testing.T) {
	lifecycle := mustLifecycle(t)
	first := mustStart(t, lifecycle)
	var group sync.WaitGroup
	group.Add(2)
	retry := make(chan error, 1)
	terminal := make(chan error, 1)
	go func() { defer group.Done(); _, err := lifecycle.RetryPreCommit(first); retry <- err }()
	go func() { defer group.Done(); _, err := lifecycle.FinishFailure(first, FailureGateway); terminal <- err }()
	group.Wait()
	retryErr, terminalErr := <-retry, <-terminal
	if retryErr == nil {
		if !errors.Is(terminalErr, ErrNoActiveAttempt) {
			t.Fatalf("retry won but terminal error = %v", terminalErr)
		}
		second := mustStart(t, lifecycle)
		if second.Number() != 2 {
			t.Fatalf("second attempt number = %d", second.Number())
		}
		if _, err := lifecycle.FinishFailure(first, FailureUpstream); !errors.Is(err, ErrStaleAttempt) {
			t.Fatalf("retired attempt error = %v", err)
		}
		return
	}
	if terminalErr == nil {
		if !errors.Is(retryErr, ErrTerminal) {
			t.Fatalf("terminal won but retry error = %v", retryErr)
		}
		return
	}
	t.Fatalf("retry=%v terminal=%v; one operation must win", retryErr, terminalErr)
}

func TestLifecycleRejectsEmptyExecutionAndInvalidFailure(t *testing.T) {
	if _, err := New(gatewayrequestexecution.Execution{}); !errors.Is(err, ErrExecutionEmpty) {
		t.Fatalf("empty execution error = %v", err)
	}
	lifecycle := mustLifecycle(t)
	attempt := mustStart(t, lifecycle)
	if _, err := lifecycle.FinishFailure(attempt, FailureKind("forged")); !errors.Is(err, ErrInvalidFailure) {
		t.Fatalf("forged failure error = %v", err)
	}
}

func mustStart(t *testing.T, lifecycle *Lifecycle) Attempt {
	t.Helper()
	attempt, err := lifecycle.Start()
	if err != nil {
		t.Fatal(err)
	}
	return attempt
}

func mustLifecycle(t *testing.T) *Lifecycle {
	t.Helper()
	bindings := []port.GatewayPreflightBindingRecord{{ID: "binding", APIKeyID: "key", SystemAccountID: "system", GroupID: "group", Priority: 1, Weight: 1, Status: "active", GroupEnabled: true}}
	store := &preflightStore{key: port.GatewayPreflightAPIKeyRecord{ID: "key", SystemAccountID: "system", APIKeyStatus: "active", SystemAccountStatus: "active", RouteStrategyID: "route", RouteStrategyStatus: "active", RouteStrategyMode: "failover", RouteDispatchGeneration: 1}, bindings: bindings}
	preflight := gatewaypreflight.NewService(gatewaypreflight.ServiceOptions{Store: store, Now: func() time.Time { return time.Unix(0, 0) }})
	candidates := candidateLoader{values: map[string][]gatewaycandidatewindow.Candidate{"group": {{Projection: port.GatewayAccountCandidate{AccountID: "account", SystemAccountID: "system", GroupID: "group", Name: "account"}, SupportedModels: []string{"gpt"}}}}}
	routes, err := gatewayrouteplan.NewService(gatewayrouteplan.Options{Preflight: preflight, Coordinator: gatewayroutecoordination.NewMemoryStore(), Candidates: candidates})
	if err != nil {
		t.Fatal(err)
	}
	route, err := routes.Build(context.Background(), gatewayrouteplan.Input{RawAPIKey: "sk-lifecycle"})
	if err != nil {
		t.Fatal(err)
	}
	prepared := gatewayrequestprep.Prepare(gatewayrequestprep.Input{Method: "POST", Path: "/v1/responses"})
	decision := gatewayrequestexecution.Build(gatewayrequestexecution.Input{Request: prepared, Route: route, Identity: gatewayrequestexecution.Identity{TraceID: "trace", MutationID: "mutation"}})
	execution, ok := decision.Execution()
	if !ok {
		t.Fatalf("execution decision = %#v", decision)
	}
	lifecycle, err := New(execution)
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
	return gatewaycandidatewindow.Window{Candidates: append([]gatewaycandidatewindow.Candidate(nil), values...)}, found, nil
}

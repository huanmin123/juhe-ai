package gatewaycurrentgroupexecution

import (
	"context"
	"errors"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/modules/gatewayattemptloop"
	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
	"juhe-ai/backend-go/internal/modules/gatewayclientipconcurrency"
	"juhe-ai/backend-go/internal/modules/gatewayhighconcurrencyexecution"
	"juhe-ai/backend-go/internal/modules/gatewayhighconcurrencyorchestration"
	"juhe-ai/backend-go/internal/modules/gatewayingress"
	"juhe-ai/backend-go/internal/modules/gatewayingressplan"
	"juhe-ai/backend-go/internal/modules/gatewaypreflight"
	"juhe-ai/backend-go/internal/modules/gatewayrequestexecution"
	"juhe-ai/backend-go/internal/modules/gatewayrequestlifecycle"
	"juhe-ai/backend-go/internal/modules/gatewayrequestorchestration"
	"juhe-ai/backend-go/internal/modules/gatewayrequestprep"
	"juhe-ai/backend-go/internal/modules/gatewayroutecoordination"
	"juhe-ai/backend-go/internal/modules/gatewayrouteplan"
	"juhe-ai/backend-go/internal/modules/gatewaystreamrelay"
	protocolgateway "juhe-ai/backend-go/internal/protocols/gateway"
	"juhe-ai/backend-go/internal/store/port"
)

func TestRunUsesOnlyFirstNormalRuntimeWindow(t *testing.T) {
	normal := &normalRunnerStub{result: gatewayattemptloop.Result{Outcome: gatewayattemptloop.OutcomeSucceeded}}
	service, err := NewService(Options{Normal: normal})
	if err != nil {
		t.Fatal(err)
	}
	facts := finalizedFacts("normal", gatewayingress.LaneImage)
	facts.batch.window.Candidates = append(facts.batch.window.Candidates, candidate("later-account", facts.batch.groupID))
	result, err := service.run(Input{Context: t.Context(), DeferResponseTerminal: true, PreserveLifecycleOnCandidatesExhausted: true}, facts)
	if err != nil || result.Normal == nil || result.High != nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if normal.calls != 1 || normal.input.MutationID != "mutation" || normal.input.TraceID != "trace" ||
		normal.input.FinalLane != gatewayingress.LaneImage || normal.input.Request.Model != "gpt" || len(normal.input.Candidates) != 2 || !normal.input.DeferResponseTerminal || !normal.input.PreserveLifecycleOnCandidatesExhausted {
		t.Fatalf("normal calls=%d input=%+v", normal.calls, normal.input)
	}
}

func TestRunUsesHighConcurrencyWindowAndAuthenticatedAPIKey(t *testing.T) {
	normal := &normalRunnerStub{}
	high := &highRunnerStub{result: gatewayhighconcurrencyexecution.Result{Orchestration: gatewayhighconcurrencyorchestration.Result{Outcome: gatewayhighconcurrencyorchestration.OutcomeFallback}}}
	service, err := NewService(Options{Normal: normal, HighConcurrency: high})
	if err != nil {
		t.Fatal(err)
	}
	facts := finalizedFacts("high_concurrency", gatewayingress.LaneImage)
	fallback := &fallbackCallbackStub{}
	postSource := &postSourceLeaseFallbackStub{}
	preAcquired := gatewayclientipconcurrency.Decision{Acquired: true}
	result, err := service.run(Input{Context: t.Context(), ClientIP: "203.0.113.8", DeferResponseTerminal: true, Fallback: fallback, PostSourceLeaseFallback: postSource, PreserveLifecycleOnCandidatesExhausted: true, PreAcquiredClientIP: &preAcquired, RetainPreAcquiredClientIPLease: true}, facts)
	if err != nil || result.High == nil || result.Normal != nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if normal.calls != 0 || high.calls != 1 || high.input.Orchestration.APIKeyID != "key" ||
		high.input.Orchestration.Lane != gatewayingress.LaneImage || high.input.FinalLane != gatewayingress.LaneImage ||
		high.input.ClientIP != "203.0.113.8" || high.input.PreAcquiredClientIP != &preAcquired || !high.input.RetainPreAcquiredClientIPLease || high.input.Fallback != fallback || high.input.PostSourceLeaseFallback != postSource || high.input.Orchestration.Fallback != fallback || !high.input.DeferResponseTerminal || !high.input.PreserveLifecycleOnCandidatesExhausted || high.input.Orchestration.Window.Access.GroupID != facts.batch.groupID {
		t.Fatalf("normal=%d high=%d input=%+v", normal.calls, high.calls, high.input)
	}
}

func TestRunRejectsPreAcquiredClientIPForNormalGroup(t *testing.T) {
	normal := &normalRunnerStub{}
	service, err := NewService(Options{Normal: normal})
	if err != nil {
		t.Fatal(err)
	}
	prepared := gatewayclientipconcurrency.Decision{Acquired: true}
	if _, err := service.run(Input{Context: t.Context(), PreAcquiredClientIP: &prepared}, finalizedFacts("normal", gatewayingress.LaneText)); err == nil || normal.calls != 0 {
		t.Fatalf("normal group accepted pre-acquired client IP err=%v calls=%d", err, normal.calls)
	}
	if _, err := service.run(Input{Context: t.Context(), RetainPreAcquiredClientIPLease: true}, finalizedFacts("normal", gatewayingress.LaneText)); err == nil || normal.calls != 0 {
		t.Fatalf("normal group accepted retained client IP err=%v calls=%d", err, normal.calls)
	}
	if _, err := service.run(Input{Context: t.Context(), PostSourceLeaseFallback: &postSourceLeaseFallbackStub{}}, finalizedFacts("normal", gatewayingress.LaneText)); err == nil || normal.calls != 0 {
		t.Fatalf("normal group accepted post-source fallback handoff err=%v calls=%d", err, normal.calls)
	}
}

type fallbackCallbackStub struct{}

func (*fallbackCallbackStub) RequestFallback(context.Context, string) (gatewayhighconcurrencyorchestration.FallbackResult, error) {
	return gatewayhighconcurrencyorchestration.FallbackResult{}, nil
}

type postSourceLeaseFallbackStub struct{}

func (*postSourceLeaseFallbackStub) PrepareFallbackTarget(context.Context, string, gatewayclientipconcurrency.TargetPreparationHandoff) (gatewayhighconcurrencyorchestration.FallbackResult, error) {
	return gatewayhighconcurrencyorchestration.FallbackResult{}, nil
}

func TestRunFailsClosedBeforeSelectingRunner(t *testing.T) {
	normal := &normalRunnerStub{}
	service, err := NewService(Options{Normal: normal})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Run(Input{Context: t.Context()}); !errors.Is(err, ErrExecutionNotFinalized) || normal.calls != 0 {
		t.Fatalf("empty execution err=%v calls=%d", err, normal.calls)
	}
	if _, err := service.run(Input{Context: t.Context()}, finalizedFacts("high_concurrency", gatewayingress.LaneText)); !errors.Is(err, ErrHighRunnerMissing) || normal.calls != 0 {
		t.Fatalf("missing high runner err=%v calls=%d", err, normal.calls)
	}
}

func TestRunWithExistingRequestLifecycleCreatesFreshAdapterWithoutHook(t *testing.T) {
	normal := &normalRunnerStub{result: gatewayattemptloop.Result{Outcome: gatewayattemptloop.OutcomeCandidatesExhausted}}
	service, err := NewService(Options{Normal: normal})
	if err != nil {
		t.Fatal(err)
	}
	execution := actualFinalizedExecution(t)
	lifecycle, err := gatewayrequestlifecycle.New(execution)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.RunWithExistingRequestLifecycle(Input{Context: t.Context(), Execution: execution, PreserveLifecycleOnCandidatesExhausted: true}, lifecycle)
	if err != nil || result.Normal == nil || normal.input.Lifecycle == nil {
		t.Fatalf("result=%+v err=%v input=%+v", result, err, normal.input)
	}
	if snapshot := lifecycle.Snapshot(); snapshot.State != gatewayrequestlifecycle.StateReady || snapshot.Attempts != 0 {
		t.Fatalf("lifecycle snapshot=%+v", snapshot)
	}
	if _, err := service.RunWithExistingRequestLifecycle(Input{Context: t.Context(), Execution: execution, OnRequestLifecycleReady: func(*gatewayrequestlifecycle.Lifecycle) {}}, lifecycle); err == nil {
		t.Fatal("existing lifecycle accepted hook")
	}
	different := actualFinalizedExecutionForBindings(t, "normal", []port.GatewayPreflightBindingRecord{{ID: "other-binding", APIKeyID: "key", SystemAccountID: "system", GroupID: "other-group", Priority: 1, Weight: 1, Status: "active", GroupEnabled: true}})
	if _, err := service.RunWithExistingRequestLifecycle(Input{Context: t.Context(), Execution: different}, lifecycle); !errors.Is(err, gatewayrequestlifecycle.ErrExecutionMismatch) {
		t.Fatalf("different execution lifecycle err=%v", err)
	}
	activeLifecycle, err := gatewayrequestlifecycle.New(execution)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := activeLifecycle.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RunWithExistingRequestLifecycle(Input{Context: t.Context(), Execution: execution}, activeLifecycle); !errors.Is(err, gatewayrequestlifecycle.ErrContinuationState) {
		t.Fatalf("active lifecycle err=%v", err)
	}
}

func TestRunRejectsIncompleteFrozenRuntimeFacts(t *testing.T) {
	facts := finalizedFacts("normal", gatewayingress.LaneText)
	facts.apiKeyID = " "
	normal := &normalRunnerStub{}
	if _, err := (&Service{normal: normal}).run(Input{Context: t.Context()}, facts); !errors.Is(err, ErrExecutionEmpty) || normal.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, normal.calls)
	}
}

func TestFactsFromExecutionRejectsLegacyExecution(t *testing.T) {
	if _, err := factsFromExecution(gatewayrequestexecution.Execution{}); !errors.Is(err, ErrExecutionNotFinalized) {
		t.Fatalf("err=%v", err)
	}
}

func TestRunConsumesActualFinalizedExecution(t *testing.T) {
	normal := &normalRunnerStub{result: gatewayattemptloop.Result{Outcome: gatewayattemptloop.OutcomeSucceeded}}
	service, err := NewService(Options{Normal: normal})
	if err != nil {
		t.Fatal(err)
	}
	execution := actualFinalizedExecution(t)
	result, err := service.Run(Input{Context: t.Context(), Execution: execution})
	if err != nil || result.Normal == nil || result.GroupID != "group" || normal.calls != 1 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, normal.calls)
	}
	if normal.input.FinalLane != gatewayingress.LaneImage || normal.input.Request.Model != "gpt" || normal.input.MutationID != "mutation" || normal.input.TraceID != "trace" {
		t.Fatalf("normal input=%+v", normal.input)
	}
}

func TestRunWithRequestLifecycleCreatesAdapterFromFinalizedExecution(t *testing.T) {
	normal := &normalRunnerStub{result: gatewayattemptloop.Result{Outcome: gatewayattemptloop.OutcomeSucceeded}}
	service, err := NewService(Options{Normal: normal})
	if err != nil {
		t.Fatal(err)
	}
	result, lifecycle, err := service.RunWithRequestLifecycle(Input{Context: t.Context(), Execution: actualFinalizedExecution(t)})
	if err != nil || result.Normal == nil || lifecycle == nil || normal.input.Lifecycle == nil {
		t.Fatalf("result=%+v lifecycle=%v err=%v normal=%+v", result, lifecycle, err, normal.input)
	}
	if snapshot := lifecycle.Snapshot(); snapshot.State != gatewayrequestlifecycle.StateReady || snapshot.IsTerminal {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}

func TestRunWithRequestLifecycleNotifiesOwnerBeforeRunner(t *testing.T) {
	normal := &normalRunnerStub{result: gatewayattemptloop.Result{Outcome: gatewayattemptloop.OutcomeSucceeded}}
	service, err := NewService(Options{Normal: normal})
	if err != nil {
		t.Fatal(err)
	}
	var notified *gatewayrequestlifecycle.Lifecycle
	result, lifecycle, err := service.RunWithRequestLifecycle(Input{
		Context: t.Context(), Execution: actualFinalizedExecution(t),
		OnRequestLifecycleReady: func(value *gatewayrequestlifecycle.Lifecycle) {
			notified = value
			if normal.calls != 0 {
				t.Errorf("runner calls during lifecycle callback = %d", normal.calls)
			}
		},
	})
	if err != nil || result.Normal == nil || lifecycle == nil || lifecycle != notified || normal.calls != 1 {
		t.Fatalf("result=%+v lifecycle=%v notified=%v err=%v calls=%d", result, lifecycle, notified, err, normal.calls)
	}
}

func TestRunWithRequestLifecycleRejectsInjectedLifecycle(t *testing.T) {
	normal := &normalRunnerStub{}
	service, err := NewService(Options{Normal: normal})
	if err != nil {
		t.Fatal(err)
	}
	result, lifecycle, err := service.RunWithRequestLifecycle(Input{
		Context: t.Context(), Execution: actualFinalizedExecution(t), Lifecycle: lifecycleSentinel{},
	})
	if !errors.Is(err, ErrLifecycleProvided) || lifecycle != nil || result.Normal != nil || normal.calls != 0 {
		t.Fatalf("result=%+v lifecycle=%v err=%v calls=%d", result, lifecycle, err, normal.calls)
	}
}

func TestRunWithRequestLifecycleRejectsNonFinalizedExecution(t *testing.T) {
	normal := &normalRunnerStub{}
	service, err := NewService(Options{Normal: normal})
	if err != nil {
		t.Fatal(err)
	}
	result, lifecycle, err := service.RunWithRequestLifecycle(Input{Context: t.Context()})
	if !errors.Is(err, ErrExecutionNotFinalized) || lifecycle != nil || result.Normal != nil || normal.calls != 0 {
		t.Fatalf("result=%+v lifecycle=%v err=%v calls=%d", result, lifecycle, err, normal.calls)
	}
}

func TestRunWithRequestLifecyclePrioritizesNonFinalizedExecutionOverInjectedLifecycle(t *testing.T) {
	normal := &normalRunnerStub{}
	service, err := NewService(Options{Normal: normal})
	if err != nil {
		t.Fatal(err)
	}
	result, lifecycle, err := service.RunWithRequestLifecycle(Input{
		Context: t.Context(), Lifecycle: lifecycleSentinel{},
	})
	if !errors.Is(err, ErrExecutionNotFinalized) || errors.Is(err, ErrLifecycleProvided) || lifecycle != nil || result.Normal != nil || normal.calls != 0 {
		t.Fatalf("result=%+v lifecycle=%v err=%v calls=%d", result, lifecycle, err, normal.calls)
	}
}

func TestRunWithRequestLifecycleDoesNotNotifyOwnerForRejectedInput(t *testing.T) {
	service, err := NewService(Options{Normal: &normalRunnerStub{}})
	if err != nil {
		t.Fatal(err)
	}
	called := false
	_, lifecycle, err := service.RunWithRequestLifecycle(Input{
		Context: t.Context(), Lifecycle: lifecycleSentinel{},
		OnRequestLifecycleReady: func(*gatewayrequestlifecycle.Lifecycle) { called = true },
	})
	if !errors.Is(err, ErrExecutionNotFinalized) || lifecycle != nil || called {
		t.Fatalf("lifecycle=%v called=%v err=%v", lifecycle, called, err)
	}
}

func TestRunWithRequestLifecycleRetainsLifecycleAfterRunnerError(t *testing.T) {
	normal := &normalRunnerStub{err: errors.New("attempt infrastructure failed")}
	service, err := NewService(Options{Normal: normal})
	if err != nil {
		t.Fatal(err)
	}
	result, lifecycle, err := service.RunWithRequestLifecycle(Input{Context: t.Context(), Execution: actualFinalizedExecution(t)})
	if err == nil || lifecycle == nil || result.Normal != nil || normal.input.Lifecycle == nil {
		t.Fatalf("result=%+v lifecycle=%v err=%v normal=%+v", result, lifecycle, err, normal.input)
	}
}

func TestRunConsumesOnlyFirstBatchFromActualMultiGroupExecution(t *testing.T) {
	normal := &normalRunnerStub{result: gatewayattemptloop.Result{Outcome: gatewayattemptloop.OutcomeSucceeded}}
	service, err := NewService(Options{Normal: normal})
	if err != nil {
		t.Fatal(err)
	}
	execution := actualFinalizedExecutionForBindings(t, "failover", []port.GatewayPreflightBindingRecord{
		{ID: "first-binding", APIKeyID: "key", SystemAccountID: "system", GroupID: "first-group", Priority: 1, Weight: 1, ProviderCode: "gpt", Status: "active", GroupEnabled: true},
		{ID: "second-binding", APIKeyID: "key", SystemAccountID: "system", GroupID: "second-group", Priority: 2, Weight: 1, ProviderCode: "gpt", Status: "active", GroupEnabled: true},
	})
	result, err := service.Run(Input{Context: t.Context(), Execution: execution})
	if err != nil || result.BindingID != "first-binding" || result.GroupID != "first-group" || normal.calls != 1 || len(normal.input.Candidates) != 1 || normal.input.Candidates[0].Projection.AccountID != "account-first-group" {
		t.Fatalf("result=%+v err=%v normal=%+v calls=%d", result, err, normal.input, normal.calls)
	}
}

func finalizedFacts(groupType string, lane gatewayingress.Lane) executionFacts {
	return executionFacts{
		identity: gatewayrequestexecution.Identity{TraceID: "trace", MutationID: "mutation"},
		apiKeyID: "key", request: protocolgateway.RequestShape{Method: "POST", Path: "/v1/responses", Model: "gpt", Stream: true}, lane: lane,
		batch: batchFacts{bindingID: "binding", groupID: "group", window: gatewaycandidatewindow.Window{
			Access:     port.GatewayGroupAccess{GroupID: "group", CallerSystemAccountID: "system", GroupType: groupType},
			Candidates: []gatewaycandidatewindow.Candidate{candidate("account", "group")},
		}},
	}
}

func candidate(accountID, groupID string) gatewaycandidatewindow.Candidate {
	return gatewaycandidatewindow.Candidate{Projection: port.GatewayAccountCandidate{AccountID: accountID, GroupID: groupID, SystemAccountID: "system"}, SupportedModels: []string{"gpt"}}
}

func actualFinalizedExecution(t *testing.T) gatewayrequestexecution.Execution {
	t.Helper()
	return actualFinalizedExecutionForBindings(t, "normal", nil)
}

func actualFinalizedExecutionForBindings(t *testing.T, mode string, bindings []port.GatewayPreflightBindingRecord) gatewayrequestexecution.Execution {
	t.Helper()
	preflightStore := &executionPreflightStore{mode: mode, bindings: bindings}
	preflight, err := gatewaypreflight.NewService(gatewaypreflight.ServiceOptions{Store: preflightStore, Now: func() time.Time { return time.Date(2026, time.August, 4, 0, 0, 0, 0, time.UTC) }}).Resolve(t.Context(), "sk-current-group-owner")
	if err != nil || !preflight.Decision().Allowed() {
		t.Fatalf("preflight=%#v err=%v", preflight, err)
	}
	routeService, err := gatewayrouteplan.NewService(gatewayrouteplan.Options{
		Preflight: preflightResolverStub{}, Coordinator: gatewayroutecoordination.NewMemoryStore(), Candidates: executionCandidateLoader{},
	})
	if err != nil {
		t.Fatal(err)
	}
	route, err := routeService.BuildFromPreflight(t.Context(), gatewayrouteplan.PreparedInput{Preflight: preflight, RequestedModel: "gpt", EndpointFamily: "responses"})
	if err != nil {
		t.Fatal(err)
	}
	intent, err := gatewayingress.Parse(gatewayingress.ParseInput{RawBody: []byte(`{"model":"gpt","stream":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := gatewayingress.NewSnapshot(gatewayingress.SnapshotInput{
		Revision: "owner-test", Model: "gpt", CandidateCapacity: 1, ToolCatalog: map[string]struct{}{}, ToolCatalogComplete: true, MappingLane: gatewayingress.LaneImage,
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
	orchestration := gatewayrequestorchestration.Result{
		Preflight: preflight, Intent: intent, Route: &route,
		Ingress: &gatewayingressplan.Result{Preflight: preflight, Finalization: &finalization, Admission: &admission},
	}
	decision := gatewayrequestexecution.BuildFromOrchestration(gatewayrequestexecution.OrchestratedInput{
		Request: gatewayrequestprep.Prepare(gatewayrequestprep.Input{Method: "POST", Path: "/v1/responses", RequestedModel: "gpt", StreamRequested: true}),
		Intent:  intent, Orchestration: orchestration, Identity: gatewayrequestexecution.Identity{TraceID: "trace", MutationID: "mutation"},
	})
	execution, ok := decision.Execution()
	if !ok {
		t.Fatalf("execution decision=%#v", decision)
	}
	return execution
}

type executionPreflightStore struct {
	mode     string
	bindings []port.GatewayPreflightBindingRecord
}

func (store executionPreflightStore) LoadGatewayPreflightAPIKey(context.Context, string) (port.GatewayPreflightAPIKeyRecord, bool, error) {
	config := `{}`
	mode := store.mode
	if mode == "" {
		mode = "normal"
	}
	return port.GatewayPreflightAPIKeyRecord{
		ID: "key", SystemAccountID: "system", APIKeyStatus: "active", SystemAccountStatus: "active", SystemAccountImageGenerationEnabled: true,
		RouteStrategyID: "route", RouteStrategyStatus: "active", RouteStrategyMode: mode, RouteStrategyConfigJSON: &config,
	}, true, nil
}

func (store executionPreflightStore) ListGatewayPreflightBindings(context.Context, string, string, string, time.Time, int) ([]port.GatewayPreflightBindingRecord, error) {
	if len(store.bindings) != 0 {
		return append([]port.GatewayPreflightBindingRecord(nil), store.bindings...), nil
	}
	return []port.GatewayPreflightBindingRecord{{
		ID: "binding", APIKeyID: "key", SystemAccountID: "system", GroupID: "group", Priority: 1, Weight: 1,
		ProviderCode: "gpt", Status: "active", GroupEnabled: true,
	}}, nil
}

func (executionPreflightStore) LoadGatewayPreflightSettings(context.Context) (port.GatewayPreflightSettingsRecord, error) {
	return port.GatewayPreflightSettingsRecord{
		GatewayTextRawBodyLimitMegabytes: 16, DefaultTemporaryUnschedulableMinutes: 1,
		TemporaryUnschedulableRetryIntervalSeconds: 1, TemporaryUnschedulableRetryAttempts: 1,
		TextFirstResponseTimeoutSeconds: 1, TextStreamIdleTimeoutSeconds: 1, TextUncommittedAttemptMaxLifetimeSeconds: 1,
		ImageFirstResponseTimeoutSeconds: 1, ImageStreamIdleTimeoutSeconds: 1, ImageUncommittedAttemptMaxLifetimeSeconds: 1,
		ImageRequestWallTimeoutSeconds: 1, NoAvailableAccountWaitTimeoutSeconds: 1, StreamFailureThresholdCount: 1, StreamFailureThresholdWindowMinutes: 1,
	}, nil
}

type preflightResolverStub struct{}

func (preflightResolverStub) Resolve(context.Context, string) (gatewaypreflight.Result, error) {
	return gatewaypreflight.Result{}, errors.New("preflight resolver must not be called")
}

type executionCandidateLoader struct{}

func (executionCandidateLoader) Load(_ context.Context, input gatewaycandidatewindow.LoadInput) (gatewaycandidatewindow.Window, bool, error) {
	return gatewaycandidatewindow.Window{
		Access:     port.GatewayGroupAccess{GroupID: input.GroupID, CallerSystemAccountID: input.SystemAccountID, GroupType: "normal"},
		Candidates: []gatewaycandidatewindow.Candidate{candidate("account-"+input.GroupID, input.GroupID)},
	}, true, nil
}

type normalRunnerStub struct {
	input  gatewayattemptloop.Input
	result gatewayattemptloop.Result
	err    error
	calls  int
}

func (s *normalRunnerStub) Run(input gatewayattemptloop.Input) (gatewayattemptloop.Result, error) {
	s.calls++
	s.input = input
	return s.result, s.err
}

type highRunnerStub struct {
	input  gatewayhighconcurrencyexecution.Input
	result gatewayhighconcurrencyexecution.Result
	err    error
	calls  int
}

type lifecycleSentinel struct{}

func (lifecycleSentinel) Start() error                                   { return nil }
func (lifecycleSentinel) ObserveSink(gatewaystreamrelay.SinkState) error { return nil }
func (lifecycleSentinel) RetryPreCommit() error                          { return nil }
func (lifecycleSentinel) FinishSuccess() error                           { return nil }
func (lifecycleSentinel) FinishFailure(string) error                     { return nil }
func (lifecycleSentinel) CancelClient() error                            { return nil }

func (s *highRunnerStub) Run(_ context.Context, input gatewayhighconcurrencyexecution.Input) (gatewayhighconcurrencyexecution.Result, error) {
	s.calls++
	s.input = input
	return s.result, s.err
}

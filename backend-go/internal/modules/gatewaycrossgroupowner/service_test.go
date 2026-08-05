package gatewaycrossgroupowner

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/domain/groupscheduling"
	"juhe-ai/backend-go/internal/modules/gatewayattemptloop"
	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
	"juhe-ai/backend-go/internal/modules/gatewayclientipconcurrency"
	"juhe-ai/backend-go/internal/modules/gatewaycurrentgroupexecution"
	"juhe-ai/backend-go/internal/modules/gatewayhighconcurrencyexecution"
	"juhe-ai/backend-go/internal/modules/gatewayhighconcurrencyorchestration"
	"juhe-ai/backend-go/internal/modules/gatewayhttpcompletion"
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
	"juhe-ai/backend-go/internal/store/port"
)

func TestRunContinuesOnlyToFreshDispatchPreparedTarget(t *testing.T) {
	execution, route, planner := ownerFixture(t)
	runner := &ownerRunner{}
	service, err := NewService(Options{CurrentGroup: runner, Targets: planner, ClientIP: ownerClientIP{}})
	if err != nil {
		t.Fatal(err)
	}
	terminal := gatewayhttpcompletion.New(nil)
	result, err := service.Run(Input{
		Current: gatewaycurrentgroupexecution.Input{Context: t.Context(), Execution: execution, ClientIP: "203.0.113.8"},
		Route:   route, Policy: ownerAllowAllPolicy{}, Terminal: terminal, PendingFailureTransfer: ownerTransfer{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Groups) != 2 || runner.firstCalls != 1 || runner.existingCalls != 1 {
		t.Fatalf("groups=%+v first=%d existing=%d", result.Groups, runner.firstCalls, runner.existingCalls)
	}
	if runner.existingExecution.Batches()[0].BindingID() != "second-binding" || runner.existingExecution.Batches()[0].GroupID() != "second-group" {
		t.Fatalf("target execution batches=%+v", runner.existingExecution.Batches())
	}
	if result.Lifecycle == nil || result.Lifecycle.Snapshot().State != gatewayrequestlifecycle.StateReady {
		t.Fatalf("lifecycle=%+v", result.Lifecycle)
	}
	if len(result.EnteredGroupIDs) != 2 || result.EnteredGroupIDs[0] != "first-group" || result.EnteredGroupIDs[1] != "second-group" {
		t.Fatalf("entered=%v", result.EnteredGroupIDs)
	}
	if len(result.ExcludedAccountIDs) != 1 || result.ExcludedAccountIDs[0] != "source-account" {
		t.Fatalf("excluded=%v", result.ExcludedAccountIDs)
	}
}

func TestRunRequiresPendingFailureTransferBeforeCurrentExecution(t *testing.T) {
	execution, route, planner := ownerFixture(t)
	runner := &ownerRunner{}
	service, err := NewService(Options{CurrentGroup: runner, Targets: planner, ClientIP: ownerClientIP{}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Run(Input{Current: gatewaycurrentgroupexecution.Input{Context: t.Context(), Execution: execution}, Route: route, Policy: ownerAllowAllPolicy{}, Terminal: gatewayhttpcompletion.New(nil)})
	if !errors.Is(err, ErrPendingFailureTransferMissing) {
		t.Fatalf("err=%v", err)
	}
	if runner.firstCalls != 0 {
		t.Fatalf("current group executed despite missing transfer: %d", runner.firstCalls)
	}
}

func TestRunTransfersNormalizedScopesBeforeTargetRunner(t *testing.T) {
	execution, route, planner := ownerFixture(t)
	events := []string{}
	runner := &ownerRunner{events: &events}
	var sourceScope, targetScope gatewayclientipconcurrency.Scope
	service, err := NewService(Options{CurrentGroup: runner, Targets: planner, ClientIP: ownerClientIP{}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Run(Input{
		Current: gatewaycurrentgroupexecution.Input{Context: t.Context(), Execution: execution, ClientIP: " 203.0.113.8 "},
		Route:   route, Policy: ownerAllowAllPolicy{}, Terminal: gatewayhttpcompletion.New(nil),
		PendingFailureTransfer: PendingFailureTransferFunc(func(_ context.Context, source, target gatewayclientipconcurrency.Scope) (gatewayclientipconcurrency.TransferResult, error) {
			events = append(events, "transfer")
			sourceScope, targetScope = source, target
			return gatewayclientipconcurrency.TransferResult{NoOp: true, SourceEmpty: true}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Groups) != 2 || !sameStrings(events, []string{"source", "transfer", "target"}) {
		t.Fatalf("groups=%d events=%v", len(result.Groups), events)
	}
	for name, scope := range map[string]gatewayclientipconcurrency.Scope{"source": sourceScope, "target": targetScope} {
		if scope.SystemAccountID != "system" || scope.APIKeyID != "key" || scope.ClientIP != "203.0.113.8" {
			t.Fatalf("%s scope=%+v", name, scope)
		}
	}
}

func TestTransferScopeDefaultsInternalAPIKeyAndPreservesSystemAccount(t *testing.T) {
	execution, _, _ := ownerFixture(t)
	batch, err := firstBatch(execution)
	if err != nil {
		t.Fatal(err)
	}
	scope := transferScopeForBatch(batch, "  ", " 203.0.113.8 ")
	if scope.SystemAccountID != batch.RuntimeWindow().Access.CallerSystemAccountID || scope.SystemAccountID != "system" {
		t.Fatalf("system account was not preserved: %+v", scope)
	}
	if scope.APIKeyID != gatewayclientipconcurrency.InternalAPIKeyID || scope.ClientIP != "203.0.113.8" {
		t.Fatalf("scope normalization=%+v", scope)
	}
}

func TestRunRejectsMalformedTransferAndReleasesTargetLease(t *testing.T) {
	execution, route, planner := ownerFixtureForTargetType(t, "high_concurrency")
	clientIP := gatewayclientipconcurrency.NewService(nil)
	runner := &ownerRunner{}
	service, err := NewService(Options{CurrentGroup: runner, Targets: planner, ClientIP: clientIP})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Run(Input{
		Current: gatewaycurrentgroupexecution.Input{Context: t.Context(), Execution: execution, ClientIP: "203.0.113.8"},
		Route:   route, Policy: ownerAllowAllPolicy{}, Terminal: gatewayhttpcompletion.New(nil),
		PendingFailureTransfer: PendingFailureTransferFunc(func(context.Context, gatewayclientipconcurrency.Scope, gatewayclientipconcurrency.Scope) (gatewayclientipconcurrency.TransferResult, error) {
			return gatewayclientipconcurrency.TransferResult{}, nil
		}),
	})
	if err == nil {
		t.Fatal("malformed transfer result was accepted")
	}
	if snapshots := clientIP.Snapshot(); len(snapshots) != 0 {
		t.Fatalf("target lease leaked after malformed transfer: %+v", snapshots)
	}
}

func TestPendingFailureTransferFuncNilIsObservable(t *testing.T) {
	_, err := PendingFailureTransferFunc(nil).Transfer(t.Context(), gatewayclientipconcurrency.Scope{}, gatewayclientipconcurrency.Scope{})
	if err == nil {
		t.Fatal("nil transfer function did not fail")
	}
}

func TestValidateTransferResultRejectsNegativeAndOverflowCounts(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	tests := []gatewayclientipconcurrency.TransferResult{
		{Attempted: 1, Inserted: 2, Replaced: -1, SourceCleared: true},
		{Attempted: 1, Inserted: 0, CapacityDropped: 1, Dropped: -1, SourceCleared: true},
		{Attempted: 1, Inserted: 0, CapacityDropped: -1, Dropped: 0, SourceCleared: true},
		{Attempted: maxInt, Inserted: maxInt, Replaced: 1, SourceCleared: true},
	}
	for index, result := range tests {
		if err := validateTransferResult(result); err == nil {
			t.Fatalf("case %d malformed result was accepted: %+v", index, result)
		}
	}
	if err := validateTransferResult(gatewayclientipconcurrency.TransferResult{NoOp: true, SourceEmpty: true, Attempted: -1}); err == nil {
		t.Fatal("negative no-op count was accepted")
	}
}

func TestPrepareTargetFailuresDoNotInvokePendingFailureTransfer(t *testing.T) {
	tests := map[string]func(*testing.T){
		"prepare": func(t *testing.T) {
			execution, route, _ := ownerFixture(t)
			runner := &ownerRunner{}
			calls := 0
			service, err := NewService(Options{CurrentGroup: runner, Targets: failingTargetPreparer{}, ClientIP: ownerClientIP{}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = service.Run(Input{Current: gatewaycurrentgroupexecution.Input{Context: t.Context(), Execution: execution}, Route: route, Policy: ownerAllowAllPolicy{}, Terminal: gatewayhttpcompletion.New(nil), PendingFailureTransfer: PendingFailureTransferFunc(func(context.Context, gatewayclientipconcurrency.Scope, gatewayclientipconcurrency.Scope) (gatewayclientipconcurrency.TransferResult, error) {
				calls++
				return gatewayclientipconcurrency.TransferResult{NoOp: true, SourceEmpty: true}, nil
			})})
			if err == nil || calls != 0 {
				t.Fatalf("err=%v transfer calls=%d", err, calls)
			}
		},
		"acquire": func(t *testing.T) {
			execution, route, planner := ownerFixtureForTargetType(t, "high_concurrency")
			calls := 0
			service, err := NewService(Options{CurrentGroup: &ownerRunner{}, Targets: planner, ClientIP: malformedClientIPAcquirer{err: errors.New("acquire failed")}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = service.Run(Input{Current: gatewaycurrentgroupexecution.Input{Context: t.Context(), Execution: execution, ClientIP: "203.0.113.8"}, Route: route, Policy: ownerAllowAllPolicy{}, Terminal: gatewayhttpcompletion.New(nil), PendingFailureTransfer: PendingFailureTransferFunc(func(context.Context, gatewayclientipconcurrency.Scope, gatewayclientipconcurrency.Scope) (gatewayclientipconcurrency.TransferResult, error) {
				calls++
				return gatewayclientipconcurrency.TransferResult{NoOp: true, SourceEmpty: true}, nil
			})})
			if err == nil || calls != 0 {
				t.Fatalf("err=%v transfer calls=%d", err, calls)
			}
		},
		"validation": func(t *testing.T) {
			execution, route, planner := ownerFixtureForTargetType(t, "high_concurrency")
			calls := 0
			service, err := NewService(Options{CurrentGroup: &ownerRunner{}, Targets: planner, ClientIP: malformedClientIPAcquirer{decision: gatewayclientipconcurrency.Decision{Acquired: true}}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = service.Run(Input{Current: gatewaycurrentgroupexecution.Input{Context: t.Context(), Execution: execution, ClientIP: "203.0.113.8"}, Route: route, Policy: ownerAllowAllPolicy{}, Terminal: gatewayhttpcompletion.New(nil), PendingFailureTransfer: PendingFailureTransferFunc(func(context.Context, gatewayclientipconcurrency.Scope, gatewayclientipconcurrency.Scope) (gatewayclientipconcurrency.TransferResult, error) {
				calls++
				return gatewayclientipconcurrency.TransferResult{NoOp: true, SourceEmpty: true}, nil
			})})
			if err == nil || calls != 0 {
				t.Fatalf("err=%v transfer calls=%d", err, calls)
			}
		},
	}
	for name, test := range tests {
		t.Run(name, test)
	}
}

func TestHighSourceHandoffTransfersBeforeSourceReleaseAndRetainsTarget(t *testing.T) {
	execution, route, planner := ownerFixtureForTargetType(t, "high_concurrency")
	provider := gatewayclientipconcurrency.NewService(nil)
	policy := groupscheduling.DefaultHighConcurrencyPolicy()
	policy.ClientIPConcurrencyLimit = 1
	sourceInput := gatewayclientipconcurrency.Input{SystemAccountID: "system", GroupID: "first-group", APIKeyID: "key", ClientIP: "203.0.113.8", Policy: &policy}
	sourceDecision, err := provider.Acquire(t.Context(), sourceInput)
	if err != nil || sourceDecision.Lease == nil {
		t.Fatalf("source acquire=%+v err=%v", sourceDecision, err)
	}
	defer sourceDecision.Lease.Release()
	handoff, err := gatewayclientipconcurrency.NewLeaseHandoff(sourceInput, sourceDecision)
	if err != nil {
		t.Fatal(err)
	}
	callbackObservedSource := false
	service, err := NewService(Options{CurrentGroup: &ownerRunner{}, Targets: planner, ClientIP: provider})
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := gatewayrouteplan.InitialFallbackCursor(route, "first-binding")
	if err != nil {
		t.Fatal(err)
	}
	state := &requestState{
		service: service,
		input: Input{
			Current: gatewaycurrentgroupexecution.Input{Context: t.Context(), Execution: execution, ClientIP: "203.0.113.8"},
			Route:   route, Policy: ownerAllowAllPolicy{},
			PendingFailureTransfer: PendingFailureTransferFunc(func(context.Context, gatewayclientipconcurrency.Scope, gatewayclientipconcurrency.Scope) (gatewayclientipconcurrency.TransferResult, error) {
				for _, snapshot := range provider.Snapshot() {
					if strings.Contains(snapshot.Key, ":first-group:") && snapshot.Current == 1 {
						callbackObservedSource = true
					}
				}
				return gatewayclientipconcurrency.TransferResult{NoOp: true, SourceEmpty: true}, nil
			}),
		},
		source: execution, cursor: cursor, entered: []string{"first-group"},
		sourceScope: gatewayclientipconcurrency.Scope{SystemAccountID: "system", APIKeyID: "key", ClientIP: "203.0.113.8"},
	}
	terminal := gatewayhttpcompletion.New(nil)
	if err := state.prepareAndStore(t.Context(), "upstream_accounts_exhausted", nil, handoff.TargetPreparation()); err != nil {
		t.Fatal(err)
	}
	if !callbackObservedSource {
		t.Fatal("callback did not observe source lease")
	}
	if snapshots := provider.Snapshot(); len(snapshots) != 1 || snapshots[0].Current != 1 {
		t.Fatalf("source/target handoff state=%+v", snapshots)
	}
	target := state.pending
	terminal.OnTerminal(func(gatewayhttpcompletion.Terminal) { target.clientIP.Lease.Release() })
	if snapshots := provider.Snapshot(); len(snapshots) != 1 || snapshots[0].Current != 1 {
		t.Fatalf("target lease not retained before terminal=%+v", snapshots)
	}
	terminal.Complete()
	if snapshots := provider.Snapshot(); len(snapshots) != 0 {
		t.Fatalf("target lease remained after terminal=%+v", snapshots)
	}
}

func TestHighSourceTransferErrorLeavesSourceLeaseOwned(t *testing.T) {
	execution, route, planner := ownerFixtureForTargetType(t, "high_concurrency")
	provider := gatewayclientipconcurrency.NewService(nil)
	policy := groupscheduling.DefaultHighConcurrencyPolicy()
	policy.ClientIPConcurrencyLimit = 1
	sourceInput := gatewayclientipconcurrency.Input{SystemAccountID: "system", GroupID: "first-group", APIKeyID: "key", ClientIP: "203.0.113.8", Policy: &policy}
	sourceDecision, err := provider.Acquire(t.Context(), sourceInput)
	if err != nil || sourceDecision.Lease == nil {
		t.Fatalf("source acquire=%+v err=%v", sourceDecision, err)
	}
	defer sourceDecision.Lease.Release()
	handoff, err := gatewayclientipconcurrency.NewLeaseHandoff(sourceInput, sourceDecision)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Options{CurrentGroup: &ownerRunner{}, Targets: planner, ClientIP: provider})
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := gatewayrouteplan.InitialFallbackCursor(route, "first-binding")
	if err != nil {
		t.Fatal(err)
	}
	state := &requestState{
		service: service,
		input: Input{
			Current: gatewaycurrentgroupexecution.Input{Context: t.Context(), Execution: execution, ClientIP: "203.0.113.8"},
			Route:   route, Policy: ownerAllowAllPolicy{},
			PendingFailureTransfer: PendingFailureTransferFunc(func(context.Context, gatewayclientipconcurrency.Scope, gatewayclientipconcurrency.Scope) (gatewayclientipconcurrency.TransferResult, error) {
				return gatewayclientipconcurrency.TransferResult{}, errors.New("transfer failed")
			}),
		},
		source: execution, cursor: cursor, entered: []string{"first-group"},
		sourceScope: gatewayclientipconcurrency.Scope{SystemAccountID: "system", APIKeyID: "key", ClientIP: "203.0.113.8"},
	}
	if err := state.prepareAndStore(t.Context(), "upstream_accounts_exhausted", nil, handoff.TargetPreparation()); err == nil {
		t.Fatal("transfer error was accepted")
	}
	if snapshots := provider.Snapshot(); len(snapshots) != 1 || snapshots[0].Current != 1 || !strings.Contains(snapshots[0].Key, ":first-group:") {
		t.Fatalf("source lease was released or target leaked=%+v", snapshots)
	}
}

func TestHighSourceMalformedTransferLeavesSourceLeaseOwned(t *testing.T) {
	execution, route, planner := ownerFixtureForTargetType(t, "high_concurrency")
	provider := gatewayclientipconcurrency.NewService(nil)
	policy := groupscheduling.DefaultHighConcurrencyPolicy()
	policy.ClientIPConcurrencyLimit = 1
	sourceInput := gatewayclientipconcurrency.Input{SystemAccountID: "system", GroupID: "first-group", APIKeyID: "key", ClientIP: "203.0.113.8", Policy: &policy}
	sourceDecision, err := provider.Acquire(t.Context(), sourceInput)
	if err != nil || sourceDecision.Lease == nil {
		t.Fatalf("source acquire=%+v err=%v", sourceDecision, err)
	}
	defer sourceDecision.Lease.Release()
	handoff, err := gatewayclientipconcurrency.NewLeaseHandoff(sourceInput, sourceDecision)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Options{CurrentGroup: &ownerRunner{}, Targets: planner, ClientIP: provider})
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := gatewayrouteplan.InitialFallbackCursor(route, "first-binding")
	if err != nil {
		t.Fatal(err)
	}
	state := &requestState{
		service: service,
		input: Input{
			Current: gatewaycurrentgroupexecution.Input{Context: t.Context(), Execution: execution, ClientIP: "203.0.113.8"},
			Route:   route, Policy: ownerAllowAllPolicy{},
			PendingFailureTransfer: PendingFailureTransferFunc(func(context.Context, gatewayclientipconcurrency.Scope, gatewayclientipconcurrency.Scope) (gatewayclientipconcurrency.TransferResult, error) {
				return gatewayclientipconcurrency.TransferResult{Attempted: 1, Inserted: 2, Replaced: -1, SourceCleared: true}, nil
			}),
		},
		source: execution, cursor: cursor, entered: []string{"first-group"},
		sourceScope: gatewayclientipconcurrency.Scope{SystemAccountID: "system", APIKeyID: "key", ClientIP: "203.0.113.8"},
	}
	if err := state.prepareAndStore(t.Context(), "upstream_accounts_exhausted", nil, handoff.TargetPreparation()); err == nil {
		t.Fatal("malformed transfer result was accepted")
	}
	if handoff.TargetPrepared() {
		t.Fatal("handoff completed despite malformed transfer")
	}
	if snapshots := provider.Snapshot(); len(snapshots) != 1 || snapshots[0].Current != 1 || !strings.Contains(snapshots[0].Key, ":first-group:") {
		t.Fatalf("source lease was released or target leaked=%+v", snapshots)
	}
}

func TestRunRetainsHighTargetClientIPLeaseUntilTerminal(t *testing.T) {
	execution, route, planner := ownerFixtureForTargetType(t, "high_concurrency")
	runner := &ownerRunner{}
	clientIP := gatewayclientipconcurrency.NewService(nil)
	service, err := NewService(Options{CurrentGroup: runner, Targets: planner, ClientIP: clientIP})
	if err != nil {
		t.Fatal(err)
	}
	terminal := gatewayhttpcompletion.New(nil)
	result, err := service.Run(Input{
		Current: gatewaycurrentgroupexecution.Input{Context: t.Context(), Execution: execution, ClientIP: "203.0.113.8"},
		Route:   route, Policy: ownerAllowAllPolicy{}, Terminal: terminal, PendingFailureTransfer: ownerTransfer{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Groups) != 2 || runner.highTargetInput.PreAcquiredClientIP == nil || !runner.highTargetInput.RetainPreAcquiredClientIPLease {
		t.Fatalf("groups=%+v target-input=%+v", result.Groups, runner.highTargetInput)
	}
	if snapshots := clientIP.Snapshot(); len(snapshots) != 1 || snapshots[0].Current != 1 {
		t.Fatalf("target lease not retained until terminal: %+v", snapshots)
	}
	terminal.Complete()
	if snapshots := clientIP.Snapshot(); len(snapshots) != 0 {
		t.Fatalf("target lease remained after terminal: %+v", snapshots)
	}
}

func TestRunAccumulatesExcludedAccountsAcrossGroups(t *testing.T) {
	execution, route, planner := ownerFixture(t)
	runner := &ownerRunner{secondExhausted: true}
	policy := &ownerRecordingPolicy{}
	service, err := NewService(Options{CurrentGroup: runner, Targets: planner, ClientIP: ownerClientIP{}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Run(Input{
		Current: gatewaycurrentgroupexecution.Input{Context: t.Context(), Execution: execution, ClientIP: "203.0.113.8"},
		Route:   route, Policy: policy, Terminal: gatewayhttpcompletion.New(nil), PendingFailureTransfer: ownerTransfer{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Groups) != 3 || runner.existingCalls != 2 {
		t.Fatalf("groups=%+v existing=%d", result.Groups, runner.existingCalls)
	}
	if got, want := result.ExcludedAccountIDs, []string{"source-account", "second-account"}; !sameStrings(got, want) {
		t.Fatalf("excluded=%v want=%v", got, want)
	}
	if len(policy.excluded) != 2 || !sameStrings(policy.excluded[1], []string{"source-account", "second-account"}) {
		t.Fatalf("policy excluded history=%v", policy.excluded)
	}
}

func TestRunClassifiesHighCapacityTerminalAsGatewayFailure(t *testing.T) {
	for name, first := range map[string]gatewaycurrentgroupexecution.Result{
		"busy":               {BindingID: "first-binding", GroupID: "first-group", GroupType: "high_concurrency", High: &gatewayhighconcurrencyexecution.Result{Orchestration: gatewayhighconcurrencyorchestration.Result{Outcome: gatewayhighconcurrencyorchestration.OutcomeBusy}}},
		"client-ip-rejected": {BindingID: "first-binding", GroupID: "first-group", GroupType: "high_concurrency", High: &gatewayhighconcurrencyexecution.Result{Orchestration: gatewayhighconcurrencyorchestration.Result{Outcome: gatewayhighconcurrencyorchestration.OutcomeReady}, ClientIP: &gatewayclientipconcurrency.Decision{Enabled: true}}},
	} {
		t.Run(name, func(t *testing.T) {
			execution, route, planner := ownerFixture(t)
			service, err := NewService(Options{CurrentGroup: &ownerRunner{firstResult: &first}, Targets: planner, ClientIP: ownerClientIP{}})
			if err != nil {
				t.Fatal(err)
			}
			result, err := service.Run(Input{Current: gatewaycurrentgroupexecution.Input{Context: t.Context(), Execution: execution, ClientIP: "203.0.113.8"}, Route: route, Policy: ownerAllowAllPolicy{}, Terminal: gatewayhttpcompletion.New(nil), PendingFailureTransfer: ownerTransfer{}})
			if err != nil {
				t.Fatal(err)
			}
			if result.Lifecycle == nil || result.Lifecycle.Snapshot().State != gatewayrequestlifecycle.StateFailed || result.Lifecycle.Snapshot().Failure != gatewayrequestlifecycle.FailureGateway {
				t.Fatalf("lifecycle=%+v", result.Lifecycle)
			}
		})
	}
}

func TestRunDoesNotClassifyResponseFinishedAsClientCanceled(t *testing.T) {
	execution, route, planner := ownerFixture(t)
	terminal := gatewayhttpcompletion.New(nil)
	runner := &ownerRunner{completeAfterLifecycle: terminal.Complete}
	service, err := NewService(Options{CurrentGroup: runner, Targets: planner, ClientIP: ownerClientIP{}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Run(Input{Current: gatewaycurrentgroupexecution.Input{Context: t.Context(), Execution: execution, ClientIP: "203.0.113.8"}, Route: route, Policy: ownerAllowAllPolicy{}, Terminal: terminal, PendingFailureTransfer: ownerTransfer{}})
	if err == nil {
		t.Fatal("completed response was allowed to start a target group")
	}
	if result.Lifecycle == nil || result.Lifecycle.Snapshot().State != gatewayrequestlifecycle.StateReady || result.Lifecycle.Snapshot().Failure != "" {
		t.Fatalf("lifecycle=%+v", result.Lifecycle)
	}
}

func TestRunReleasesMalformedTargetClientIPLease(t *testing.T) {
	execution, route, planner := ownerFixtureForTargetType(t, "high_concurrency")
	provider := gatewayclientipconcurrency.NewService(nil)
	policy := groupscheduling.DefaultHighConcurrencyPolicy()
	policy.ClientIPConcurrencyLimit = 1
	valid, err := provider.Acquire(t.Context(), gatewayclientipconcurrency.Input{SystemAccountID: "system", GroupID: "second-group", APIKeyID: "key", ClientIP: "203.0.113.8", Policy: &policy})
	if err != nil || valid.Lease == nil {
		t.Fatalf("seed target lease=%+v err=%v", valid, err)
	}
	malformed := valid
	malformed.Enabled = false
	service, err := NewService(Options{CurrentGroup: &ownerRunner{}, Targets: planner, ClientIP: malformedClientIPAcquirer{decision: malformed}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Run(Input{Current: gatewaycurrentgroupexecution.Input{Context: t.Context(), Execution: execution, ClientIP: "203.0.113.8"}, Route: route, Policy: ownerAllowAllPolicy{}, Terminal: gatewayhttpcompletion.New(nil), PendingFailureTransfer: ownerTransfer{}})
	if err == nil {
		t.Fatal("malformed target lease decision was accepted")
	}
	if snapshots := provider.Snapshot(); len(snapshots) != 0 {
		t.Fatalf("malformed target lease leaked: %+v", snapshots)
	}
}

func TestRunReleasesTargetClientIPLeaseWhenAcquireAlsoErrors(t *testing.T) {
	execution, route, planner := ownerFixtureForTargetType(t, "high_concurrency")
	provider := gatewayclientipconcurrency.NewService(nil)
	policy := groupscheduling.DefaultHighConcurrencyPolicy()
	policy.ClientIPConcurrencyLimit = 1
	valid, err := provider.Acquire(t.Context(), gatewayclientipconcurrency.Input{SystemAccountID: "system", GroupID: "second-group", APIKeyID: "key", ClientIP: "203.0.113.8", Policy: &policy})
	if err != nil || valid.Lease == nil {
		t.Fatalf("seed target lease=%+v err=%v", valid, err)
	}
	service, err := NewService(Options{CurrentGroup: &ownerRunner{}, Targets: planner, ClientIP: malformedClientIPAcquirer{decision: valid, err: errors.New("provider error")}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Run(Input{Current: gatewaycurrentgroupexecution.Input{Context: t.Context(), Execution: execution, ClientIP: "203.0.113.8"}, Route: route, Policy: ownerAllowAllPolicy{}, Terminal: gatewayhttpcompletion.New(nil), PendingFailureTransfer: ownerTransfer{}})
	if err == nil {
		t.Fatal("target client-IP provider error was accepted")
	}
	if snapshots := provider.Snapshot(); len(snapshots) != 0 {
		t.Fatalf("target lease leaked after provider error: %+v", snapshots)
	}
}

type ownerRunner struct {
	firstCalls             int
	existingCalls          int
	existingExecution      gatewayrequestexecution.Execution
	highTargetInput        gatewaycurrentgroupexecution.Input
	firstResult            *gatewaycurrentgroupexecution.Result
	secondExhausted        bool
	completeAfterLifecycle func()
	events                 *[]string
}

func (r *ownerRunner) RunWithRequestLifecycle(input gatewaycurrentgroupexecution.Input) (gatewaycurrentgroupexecution.Result, *gatewayrequestlifecycle.Lifecycle, error) {
	r.firstCalls++
	if r.events != nil {
		*r.events = append(*r.events, "source")
	}
	lifecycle, err := gatewayrequestlifecycle.New(input.Execution)
	if err != nil {
		return gatewaycurrentgroupexecution.Result{}, nil, err
	}
	if input.OnRequestLifecycleReady != nil {
		input.OnRequestLifecycleReady(lifecycle)
	}
	if r.completeAfterLifecycle != nil {
		r.completeAfterLifecycle()
	}
	if r.firstResult != nil {
		return *r.firstResult, lifecycle, nil
	}
	batch := input.Execution.Batches()[0]
	return gatewaycurrentgroupexecution.Result{
		BindingID: batch.BindingID(), GroupID: batch.GroupID(), GroupType: "normal",
		Normal: &gatewayattemptloop.Result{Outcome: gatewayattemptloop.OutcomeCandidatesExhausted, FallbackAccounts: gatewayattemptloop.FallbackAccountFacts{Complete: true, ExcludedAccountIDs: []string{"source-account"}}, FallbackReason: "upstream_accounts_exhausted"},
	}, lifecycle, nil
}

func (r *ownerRunner) RunWithExistingRequestLifecycle(input gatewaycurrentgroupexecution.Input, lifecycle *gatewayrequestlifecycle.Lifecycle) (gatewaycurrentgroupexecution.Result, error) {
	r.existingCalls++
	if r.events != nil {
		*r.events = append(*r.events, "target")
	}
	if err := lifecycle.ValidateContinuation(input.Execution); err != nil {
		return gatewaycurrentgroupexecution.Result{}, err
	}
	r.existingExecution = input.Execution
	batch := input.Execution.Batches()[0]
	if batch.RuntimeWindow().Access.GroupType == "high_concurrency" {
		r.highTargetInput = input
		return gatewaycurrentgroupexecution.Result{BindingID: batch.BindingID(), GroupID: batch.GroupID(), GroupType: "high_concurrency", High: &gatewayhighconcurrencyexecution.Result{Attempts: &gatewayattemptloop.Result{Outcome: gatewayattemptloop.OutcomeSucceeded}}}, nil
	}
	if batch.GroupID() == "second-group" && r.secondExhausted {
		return gatewaycurrentgroupexecution.Result{BindingID: batch.BindingID(), GroupID: batch.GroupID(), GroupType: "normal", Normal: exhaustedResult("second-account")}, nil
	}
	return gatewaycurrentgroupexecution.Result{BindingID: batch.BindingID(), GroupID: batch.GroupID(), GroupType: "normal", Normal: &gatewayattemptloop.Result{Outcome: gatewayattemptloop.OutcomeSucceeded}}, nil
}

type ownerAllowAllPolicy struct{}

type failingTargetPreparer struct{}

func (failingTargetPreparer) PrepareDispatchFallbackTarget(context.Context, gatewayrouteplan.FallbackDispatchPreparedInput) (gatewayrouteplan.FallbackDispatchPreparedTarget, error) {
	return gatewayrouteplan.FallbackDispatchPreparedTarget{}, errors.New("target preparation failed")
}

func (ownerAllowAllPolicy) SelectFallbackCandidates(_ context.Context, input gatewayrouteplan.FallbackCandidatePolicyInput) (gatewayrouteplan.FallbackCandidatePolicyResult, error) {
	ids := make([]string, 0, len(input.Window.Candidates))
	for _, candidate := range input.Window.Candidates {
		ids = append(ids, candidate.Projection.AccountID)
	}
	return gatewayrouteplan.FallbackCandidatePolicyResult{CandidateAccountIDs: ids}, nil
}

type ownerRecordingPolicy struct {
	excluded [][]string
}

func (p *ownerRecordingPolicy) SelectFallbackCandidates(ctx context.Context, input gatewayrouteplan.FallbackCandidatePolicyInput) (gatewayrouteplan.FallbackCandidatePolicyResult, error) {
	p.excluded = append(p.excluded, append([]string(nil), input.ExcludedAccountIDs...))
	return ownerAllowAllPolicy{}.SelectFallbackCandidates(ctx, input)
}

func exhaustedResult(accountID string) *gatewayattemptloop.Result {
	return &gatewayattemptloop.Result{Outcome: gatewayattemptloop.OutcomeCandidatesExhausted, FallbackAccounts: gatewayattemptloop.FallbackAccountFacts{Complete: true, ExcludedAccountIDs: []string{accountID}}, FallbackReason: "upstream_accounts_exhausted"}
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

type ownerClientIP struct{}

type ownerTransfer struct{}

func (ownerTransfer) Transfer(context.Context, gatewayclientipconcurrency.Scope, gatewayclientipconcurrency.Scope) (gatewayclientipconcurrency.TransferResult, error) {
	return gatewayclientipconcurrency.TransferResult{NoOp: true, SourceEmpty: true}, nil
}

func (ownerClientIP) Acquire(context.Context, gatewayclientipconcurrency.Input) (gatewayclientipconcurrency.Decision, error) {
	return gatewayclientipconcurrency.Decision{}, errors.New("normal target must not acquire client-IP lease")
}

type malformedClientIPAcquirer struct {
	decision gatewayclientipconcurrency.Decision
	err      error
}

func (a malformedClientIPAcquirer) Acquire(context.Context, gatewayclientipconcurrency.Input) (gatewayclientipconcurrency.Decision, error) {
	return a.decision, a.err
}

func ownerFixture(t *testing.T) (gatewayrequestexecution.Execution, gatewayrouteplan.RouteOnlyResult, *gatewayrouteplan.Service) {
	return ownerFixtureForTargetType(t, "normal")
}

func ownerFixtureForTargetType(t *testing.T, targetType string) (gatewayrequestexecution.Execution, gatewayrouteplan.RouteOnlyResult, *gatewayrouteplan.Service) {
	t.Helper()
	store := ownerPreflightStore{}
	preflight, err := gatewaypreflight.NewService(gatewaypreflight.ServiceOptions{Store: store, Now: func() time.Time { return time.Date(2026, time.August, 4, 0, 0, 0, 0, time.UTC) }}).Resolve(t.Context(), "sk-owner")
	if err != nil || !preflight.Decision().Allowed() {
		t.Fatalf("preflight=%#v err=%v", preflight, err)
	}
	planner, err := gatewayrouteplan.NewService(gatewayrouteplan.Options{Preflight: ownerPreflightResolver{}, Coordinator: gatewayroutecoordination.NewMemoryStore(), Candidates: ownerCandidateLoader{targetType: targetType}})
	if err != nil {
		t.Fatal(err)
	}
	route, err := planner.BuildFromPreflight(t.Context(), gatewayrouteplan.PreparedInput{Preflight: preflight, RequestedModel: "gpt", EndpointFamily: "responses"})
	if err != nil {
		t.Fatal(err)
	}
	routeOnly, err := gatewayrouteplan.RouteOnlyFromResult(route)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := gatewayingress.Parse(gatewayingress.ParseInput{RawBody: []byte(`{"model":"gpt","stream":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := gatewayingress.NewSnapshot(gatewayingress.SnapshotInput{Revision: "owner-test", Model: "gpt", CandidateCapacity: 1, ToolCatalog: map[string]struct{}{}, ToolCatalogComplete: true, MappingLane: gatewayingress.LaneText})
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
	orchestration := gatewayrequestorchestration.Result{Preflight: preflight, Intent: intent, Route: &route, Ingress: &gatewayingressplan.Result{Preflight: preflight, Finalization: &finalization, Admission: &admission}}
	decision := gatewayrequestexecution.BuildFromOrchestration(gatewayrequestexecution.OrchestratedInput{
		Request: gatewayrequestprep.Prepare(gatewayrequestprep.Input{Method: "POST", Path: "/v1/responses", RequestedModel: "gpt", StreamRequested: true}), Intent: intent,
		Orchestration: orchestration, Identity: gatewayrequestexecution.Identity{TraceID: "trace", MutationID: "mutation"}, InitialCommit: gatewaystreamrelay.SinkState{},
	})
	execution, ok := decision.Execution()
	if !ok {
		t.Fatalf("execution decision=%#v", decision)
	}
	return execution, routeOnly, planner
}

type ownerPreflightStore struct{}

func (ownerPreflightStore) LoadGatewayPreflightAPIKey(context.Context, string) (port.GatewayPreflightAPIKeyRecord, bool, error) {
	config := `{}`
	return port.GatewayPreflightAPIKeyRecord{ID: "key", SystemAccountID: "system", APIKeyStatus: "active", SystemAccountStatus: "active", SystemAccountImageGenerationEnabled: true, RouteStrategyID: "route", RouteStrategyStatus: "active", RouteStrategyMode: "failover", RouteStrategyConfigJSON: &config}, true, nil
}

func (ownerPreflightStore) ListGatewayPreflightBindings(context.Context, string, string, string, time.Time, int) ([]port.GatewayPreflightBindingRecord, error) {
	return []port.GatewayPreflightBindingRecord{
		{ID: "first-binding", APIKeyID: "key", SystemAccountID: "system", GroupID: "first-group", Priority: 1, Weight: 1, ProviderCode: "gpt", Status: "active", GroupEnabled: true},
		{ID: "second-binding", APIKeyID: "key", SystemAccountID: "system", GroupID: "second-group", Priority: 2, Weight: 1, ProviderCode: "gpt", Status: "active", GroupEnabled: true},
		{ID: "third-binding", APIKeyID: "key", SystemAccountID: "system", GroupID: "third-group", Priority: 3, Weight: 1, ProviderCode: "gpt", Status: "active", GroupEnabled: true},
	}, nil
}

func (ownerPreflightStore) LoadGatewayPreflightSettings(context.Context) (port.GatewayPreflightSettingsRecord, error) {
	return port.GatewayPreflightSettingsRecord{GatewayTextRawBodyLimitMegabytes: 16, DefaultTemporaryUnschedulableMinutes: 1, TemporaryUnschedulableRetryIntervalSeconds: 1, TemporaryUnschedulableRetryAttempts: 1, TextFirstResponseTimeoutSeconds: 1, TextStreamIdleTimeoutSeconds: 1, TextUncommittedAttemptMaxLifetimeSeconds: 1, ImageFirstResponseTimeoutSeconds: 1, ImageStreamIdleTimeoutSeconds: 1, ImageUncommittedAttemptMaxLifetimeSeconds: 1, ImageRequestWallTimeoutSeconds: 1, NoAvailableAccountWaitTimeoutSeconds: 1, StreamFailureThresholdCount: 1, StreamFailureThresholdWindowMinutes: 1}, nil
}

type ownerPreflightResolver struct{}

func (ownerPreflightResolver) Resolve(context.Context, string) (gatewaypreflight.Result, error) {
	return gatewaypreflight.Result{}, errors.New("owner fixture must not resolve preflight")
}

type ownerCandidateLoader struct {
	targetType string
}

func (loader ownerCandidateLoader) Load(_ context.Context, input gatewaycandidatewindow.LoadInput) (gatewaycandidatewindow.Window, bool, error) {
	groupType := "normal"
	policyJSON := ""
	if input.GroupID == "second-group" && strings.TrimSpace(loader.targetType) != "" {
		groupType = loader.targetType
	}
	if groupType == "high_concurrency" {
		policy := groupscheduling.DefaultHighConcurrencyPolicy()
		policy.ClientIPConcurrencyLimit = 1
		encoded, err := json.Marshal(policy)
		if err != nil {
			return gatewaycandidatewindow.Window{}, false, err
		}
		policyJSON = string(encoded)
	}
	return gatewaycandidatewindow.Window{Access: port.GatewayGroupAccess{GroupID: input.GroupID, CallerSystemAccountID: input.SystemAccountID, GroupType: groupType, ProviderCode: "gpt", SchedulingPolicyJSON: policyJSON}, Candidates: []gatewaycandidatewindow.Candidate{{Projection: port.GatewayAccountCandidate{AccountID: input.GroupID + "-account", GroupID: input.GroupID, SystemAccountID: input.SystemAccountID}, SupportedModels: []string{"gpt"}}}}, true, nil
}

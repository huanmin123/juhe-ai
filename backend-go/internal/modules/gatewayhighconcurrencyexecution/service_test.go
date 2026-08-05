package gatewayhighconcurrencyexecution

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/domain/groupscheduling"
	"juhe-ai/backend-go/internal/modules/gatewayattemptloop"
	"juhe-ai/backend-go/internal/modules/gatewaycandidateclaim"
	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
	"juhe-ai/backend-go/internal/modules/gatewayclientipconcurrency"
	"juhe-ai/backend-go/internal/modules/gatewayhighconcurrencyorchestration"
	"juhe-ai/backend-go/internal/modules/gatewayingress"
	"juhe-ai/backend-go/internal/modules/gatewayresponse"
	"juhe-ai/backend-go/internal/modules/gatewaystreamrelay"
	platformredis "juhe-ai/backend-go/internal/platform/redis"
	protocolgateway "juhe-ai/backend-go/internal/protocols/gateway"
	"juhe-ai/backend-go/internal/store/port"
)

func TestRunUsesClaimedAttemptLoopForReadyWindow(t *testing.T) {
	slots := &slotStub{acquireResult: platformredis.AccountSlotAcquireResult{Acquired: true, Lease: platformredis.AccountSlotLease{AccountID: "resource", Lane: platformredis.AccountSlotLaneText, Token: "token"}}}
	innerCalls := 0
	service := newService(t, &orchestratorStub{result: readyResult()}, newClaimingExecutor(t, slots, attemptExecutorFunc(func(context.Context, gatewayattemptloop.Attempt) (gatewayattemptloop.AttemptResult, error) {
		innerCalls++
		return gatewayattemptloop.AttemptResult{Success: true, Committed: true}, nil
	})))
	result, err := service.Run(t.Context(), executionInput())
	if err != nil || result.Attempts == nil || result.Attempts.Outcome != gatewayattemptloop.OutcomeSucceeded || innerCalls != 1 {
		t.Fatalf("result=%+v err=%v inner calls=%d", result, err, innerCalls)
	}
	if slots.acquireCalls != 1 || slots.releaseCalls != 1 || slots.acquire.Lane != platformredis.AccountSlotLaneText || slots.acquire.AccountID != "resource" {
		t.Fatalf("slot calls=%d/%d input=%+v", slots.acquireCalls, slots.releaseCalls, slots.acquire)
	}
}

func TestRunDoesNotDispatchForNonReadyOrchestrationOutcome(t *testing.T) {
	slots := &slotStub{}
	service := newService(t, &orchestratorStub{result: gatewayhighconcurrencyorchestration.Result{Outcome: gatewayhighconcurrencyorchestration.OutcomeFallback}}, newClaimingExecutor(t, slots, attemptExecutorFunc(func(context.Context, gatewayattemptloop.Attempt) (gatewayattemptloop.AttemptResult, error) {
		t.Fatal("inner attempt must not run")
		return gatewayattemptloop.AttemptResult{}, nil
	})))
	result, err := service.Run(t.Context(), executionInput())
	if err != nil || result.Attempts != nil || result.Orchestration.Outcome != gatewayhighconcurrencyorchestration.OutcomeFallback || slots.acquireCalls != 0 {
		t.Fatalf("result=%+v err=%v slot calls=%d", result, err, slots.acquireCalls)
	}
}

func TestRunUsesFrozenImageLaneForRawTextRequest(t *testing.T) {
	slots := &slotStub{acquireResult: platformredis.AccountSlotAcquireResult{Acquired: true, Lease: platformredis.AccountSlotLease{AccountID: "resource", Lane: platformredis.AccountSlotLaneImage, Token: "token"}}}
	var received gatewayattemptloop.Attempt
	service := newService(t, &orchestratorStub{result: readyResult()}, newClaimingExecutor(t, slots, attemptExecutorFunc(func(_ context.Context, attempt gatewayattemptloop.Attempt) (gatewayattemptloop.AttemptResult, error) {
		received = attempt
		return gatewayattemptloop.AttemptResult{Success: true, Committed: true}, nil
	})))
	input := executionInput()
	input.Orchestration.Lane = gatewayingress.LaneImage
	input.FinalLane = gatewayingress.LaneImage
	result, err := service.Run(t.Context(), input)
	if err != nil || result.Attempts == nil || result.Attempts.Outcome != gatewayattemptloop.OutcomeSucceeded {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if received.Lane != string(gatewayingress.LaneImage) || slots.acquire.Lane != platformredis.AccountSlotLaneImage {
		t.Fatalf("attempt=%+v slot=%+v", received, slots.acquire)
	}
}

func TestRunFailsClosedForLaneMismatchAndEmptyReadyWindow(t *testing.T) {
	slots := &slotStub{}
	stub := &orchestratorStub{result: readyResult()}
	service := newService(t, stub, newClaimingExecutor(t, slots, attemptExecutorFunc(func(context.Context, gatewayattemptloop.Attempt) (gatewayattemptloop.AttemptResult, error) {
		return gatewayattemptloop.AttemptResult{}, nil
	})))
	wrongLane := executionInput()
	wrongLane.Orchestration.Lane = gatewayingress.LaneImage
	if _, err := service.Run(t.Context(), wrongLane); err == nil || stub.prepareCalls != 0 || stub.afterCalls != 0 {
		t.Fatalf("lane mismatch err=%v calls=%d/%d", err, stub.prepareCalls, stub.afterCalls)
	}
	missingLane := executionInput()
	missingLane.FinalLane = ""
	if _, err := service.Run(t.Context(), missingLane); err == nil || stub.prepareCalls != 0 || stub.afterCalls != 0 {
		t.Fatalf("missing final lane err=%v calls=%d/%d", err, stub.prepareCalls, stub.afterCalls)
	}
	empty := readyResult()
	empty.Window.Candidates = nil
	stub.result = empty
	if _, err := service.Run(t.Context(), executionInput()); err == nil || slots.acquireCalls != 0 {
		t.Fatalf("empty ready window err=%v slot calls=%d", err, slots.acquireCalls)
	}
}

func TestRunReturnsInitialFallbackBeforeClientIPAcquire(t *testing.T) {
	slots := &slotStub{}
	clientIP := &clientIPStub{decision: gatewayclientipconcurrency.Decision{Enabled: true, Acquired: false, Reason: gatewayclientipconcurrency.RejectLimitReached}}
	orchestrator := &orchestratorStub{preLease: gatewayhighconcurrencyorchestration.PreLeaseResult{Result: gatewayhighconcurrencyorchestration.Result{Outcome: gatewayhighconcurrencyorchestration.OutcomeFallback}}}
	service := newServiceWithClientIP(t, orchestrator, clientIP, newClaimingExecutor(t, slots, attemptExecutorFunc(func(context.Context, gatewayattemptloop.Attempt) (gatewayattemptloop.AttemptResult, error) {
		t.Fatal("inner attempt must not run after initial fallback")
		return gatewayattemptloop.AttemptResult{}, nil
	})))
	result, err := service.Run(t.Context(), executionInput())
	if err != nil || result.Orchestration.Outcome != gatewayhighconcurrencyorchestration.OutcomeFallback || result.ClientIP != nil || orchestrator.prepareCalls != 1 || orchestrator.afterCalls != 0 || clientIP.calls != 0 || slots.acquireCalls != 0 {
		t.Fatalf("result=%+v err=%v orchestration=%d/%d client-ip=%d slots=%d", result, err, orchestrator.prepareCalls, orchestrator.afterCalls, clientIP.calls, slots.acquireCalls)
	}
}

func TestRunInitialFallbackDoesNotInvokePostSourceLeasePreparer(t *testing.T) {
	legacy := &fallbackRequesterStub{result: gatewayhighconcurrencyorchestration.FallbackResult{Attempted: true}}
	strict := &postSourceLeaseFallbackStub{}
	orchestrator := &orchestratorStub{preFallbackReason: "initial-busy"}
	service := newService(t, orchestrator, newClaimingExecutor(t, &slotStub{}, attemptExecutorFunc(func(context.Context, gatewayattemptloop.Attempt) (gatewayattemptloop.AttemptResult, error) {
		t.Fatal("inner attempt must not run after initial fallback")
		return gatewayattemptloop.AttemptResult{}, nil
	})))
	input := executionInput()
	input.Fallback = legacy
	input.PostSourceLeaseFallback = strict
	result, err := service.Run(t.Context(), input)
	if err != nil || result.Orchestration.Outcome != gatewayhighconcurrencyorchestration.OutcomeFallback || legacy.calls != 1 || strict.calls != 0 || orchestrator.afterCalls != 0 {
		t.Fatalf("result=%+v err=%v legacy=%d strict=%d after=%d", result, err, legacy.calls, strict.calls, orchestrator.afterCalls)
	}
}

func TestRunPostSourceLeaseStrictFallbackTransfersOnlySourceLease(t *testing.T) {
	clientIP := &clientIPRecorder{service: gatewayclientipconcurrency.NewService(nil)}
	var target gatewayclientipconcurrency.Decision
	strict := &postSourceLeaseFallbackStub{prepare: func(ctx context.Context, _ string, handoff gatewayclientipconcurrency.TargetPreparationHandoff) (gatewayhighconcurrencyorchestration.FallbackResult, error) {
		targetInput := fallbackTargetClientIPInput(clientIP.input)
		decision, err := clientIP.service.Acquire(ctx, targetInput)
		if err != nil {
			return gatewayhighconcurrencyorchestration.FallbackResult{}, err
		}
		target = decision
		if err := handoff.CompleteTargetPreparation(targetInput, target); err != nil {
			return gatewayhighconcurrencyorchestration.FallbackResult{}, err
		}
		return gatewayhighconcurrencyorchestration.FallbackResult{Attempted: true}, nil
	}}
	orchestrator := &orchestratorStub{result: readyResult(), afterFallbackReason: "post-lease-busy"}
	service := newServiceWithClientIP(t, orchestrator, clientIP, newClaimingExecutor(t, &slotStub{}, attemptExecutorFunc(func(context.Context, gatewayattemptloop.Attempt) (gatewayattemptloop.AttemptResult, error) {
		t.Fatal("inner attempt must not run after fallback")
		return gatewayattemptloop.AttemptResult{}, nil
	})))
	input := executionInputWithClientIPLimit(2)
	input.PostSourceLeaseFallback = strict
	result, err := service.Run(t.Context(), input)
	if err != nil || result.Orchestration.Outcome != gatewayhighconcurrencyorchestration.OutcomeFallback || strict.calls != 1 || target.Lease == nil {
		t.Fatalf("result=%+v err=%v strict=%d target=%+v", result, err, strict.calls, target)
	}
	if snapshots := clientIP.service.Snapshot(); len(snapshots) != 1 || snapshots[0].Current != 1 {
		t.Fatalf("source release or target ownership is wrong: %+v", snapshots)
	}
	target.Lease.Release()
	if snapshots := clientIP.service.Snapshot(); len(snapshots) != 0 {
		t.Fatalf("target terminal release snapshots=%+v", snapshots)
	}
}

func TestRunPostSourceLeaseStrictFallbackAllowsDisabledPreparedTarget(t *testing.T) {
	clientIP := &clientIPRecorder{service: gatewayclientipconcurrency.NewService(nil)}
	strict := &postSourceLeaseFallbackStub{prepare: func(_ context.Context, _ string, handoff gatewayclientipconcurrency.TargetPreparationHandoff) (gatewayhighconcurrencyorchestration.FallbackResult, error) {
		targetInput := fallbackTargetClientIPInput(clientIP.input)
		targetInput.ClientIP = ""
		if err := handoff.CompleteTargetPreparation(targetInput, gatewayclientipconcurrency.Decision{Enabled: false, Acquired: true}); err != nil {
			return gatewayhighconcurrencyorchestration.FallbackResult{}, err
		}
		return gatewayhighconcurrencyorchestration.FallbackResult{Attempted: true}, nil
	}}
	service := newServiceWithClientIP(t, &orchestratorStub{result: readyResult(), afterFallbackReason: "post-lease-busy"}, clientIP, newClaimingExecutor(t, &slotStub{}, attemptExecutorFunc(func(context.Context, gatewayattemptloop.Attempt) (gatewayattemptloop.AttemptResult, error) {
		t.Fatal("inner attempt must not run after fallback")
		return gatewayattemptloop.AttemptResult{}, nil
	})))
	input := executionInputWithClientIPLimit(1)
	input.PostSourceLeaseFallback = strict
	result, err := service.Run(t.Context(), input)
	if err != nil || result.Orchestration.Outcome != gatewayhighconcurrencyorchestration.OutcomeFallback || strict.calls != 1 {
		t.Fatalf("result=%+v err=%v strict=%d", result, err, strict.calls)
	}
	if snapshots := clientIP.service.Snapshot(); len(snapshots) != 0 {
		t.Fatalf("source lease remained after disabled target preparation: %+v", snapshots)
	}
}

func TestRunPostSourceLeaseStrictFallbackFailsClosedWithoutCompletedTarget(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(context.Context, *clientIPRecorder, gatewayclientipconcurrency.TargetPreparationHandoff) (gatewayhighconcurrencyorchestration.FallbackResult, error)
	}{
		{
			name: "reported without handoff",
			prepare: func(_ context.Context, _ *clientIPRecorder, _ gatewayclientipconcurrency.TargetPreparationHandoff) (gatewayhighconcurrencyorchestration.FallbackResult, error) {
				return gatewayhighconcurrencyorchestration.FallbackResult{Attempted: true}, nil
			},
		},
		{
			name: "target rejected",
			prepare: func(ctx context.Context, clientIP *clientIPRecorder, _ gatewayclientipconcurrency.TargetPreparationHandoff) (gatewayhighconcurrencyorchestration.FallbackResult, error) {
				target, err := clientIP.service.Acquire(ctx, clientIP.input)
				if err != nil {
					return gatewayhighconcurrencyorchestration.FallbackResult{}, err
				}
				if target.Acquired {
					return gatewayhighconcurrencyorchestration.FallbackResult{}, context.Canceled
				}
				return gatewayhighconcurrencyorchestration.FallbackResult{Attempted: true}, nil
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			clientIP := &clientIPRecorder{service: gatewayclientipconcurrency.NewService(nil)}
			strict := &postSourceLeaseFallbackStub{prepare: func(ctx context.Context, _ string, handoff gatewayclientipconcurrency.TargetPreparationHandoff) (gatewayhighconcurrencyorchestration.FallbackResult, error) {
				return test.prepare(ctx, clientIP, handoff)
			}}
			service := newServiceWithClientIP(t, &orchestratorStub{result: readyResult(), afterFallbackReason: "post-lease-busy"}, clientIP, newClaimingExecutor(t, &slotStub{}, attemptExecutorFunc(func(context.Context, gatewayattemptloop.Attempt) (gatewayattemptloop.AttemptResult, error) {
				t.Fatal("inner attempt must not run after failed strict fallback")
				return gatewayattemptloop.AttemptResult{}, nil
			})))
			input := executionInputWithClientIPLimit(1)
			input.PostSourceLeaseFallback = strict
			if _, err := service.Run(t.Context(), input); err == nil || strict.calls != 1 {
				t.Fatalf("err=%v strict=%d", err, strict.calls)
			}
			if snapshots := clientIP.service.Snapshot(); len(snapshots) != 0 {
				t.Fatalf("source lease was not released after strict fallback failure: %+v", snapshots)
			}
		})
	}
}

func TestRunPostSourceLeaseFallbackErrorAndNormalTerminalReleaseSource(t *testing.T) {
	t.Run("non-attempt fallback", func(t *testing.T) {
		clientIP := &clientIPRecorder{service: gatewayclientipconcurrency.NewService(nil)}
		strict := &postSourceLeaseFallbackStub{}
		service := newServiceWithClientIP(t, &orchestratorStub{result: readyResult(), afterFallbackReason: "post-lease-busy"}, clientIP, newClaimingExecutor(t, &slotStub{}, attemptExecutorFunc(func(context.Context, gatewayattemptloop.Attempt) (gatewayattemptloop.AttemptResult, error) {
			t.Fatal("inner attempt must not run after busy fallback")
			return gatewayattemptloop.AttemptResult{}, nil
		})))
		input := executionInputWithClientIPLimit(1)
		input.PostSourceLeaseFallback = strict
		result, err := service.Run(t.Context(), input)
		if err != nil || result.Orchestration.Outcome != gatewayhighconcurrencyorchestration.OutcomeBusy || strict.calls != 1 {
			t.Fatalf("result=%+v err=%v strict=%d", result, err, strict.calls)
		}
		if snapshots := clientIP.service.Snapshot(); len(snapshots) != 0 {
			t.Fatalf("source lease was not released after busy fallback: %+v", snapshots)
		}
	})
	t.Run("callback error", func(t *testing.T) {
		clientIP := &clientIPRecorder{service: gatewayclientipconcurrency.NewService(nil)}
		strict := &postSourceLeaseFallbackStub{prepare: func(context.Context, string, gatewayclientipconcurrency.TargetPreparationHandoff) (gatewayhighconcurrencyorchestration.FallbackResult, error) {
			return gatewayhighconcurrencyorchestration.FallbackResult{}, context.Canceled
		}}
		service := newServiceWithClientIP(t, &orchestratorStub{result: readyResult(), afterFallbackReason: "post-lease-busy"}, clientIP, newClaimingExecutor(t, &slotStub{}, attemptExecutorFunc(func(context.Context, gatewayattemptloop.Attempt) (gatewayattemptloop.AttemptResult, error) {
			t.Fatal("inner attempt must not run after callback error")
			return gatewayattemptloop.AttemptResult{}, nil
		})))
		input := executionInputWithClientIPLimit(1)
		input.PostSourceLeaseFallback = strict
		if _, err := service.Run(t.Context(), input); err == nil {
			t.Fatal("callback error was accepted")
		}
		if snapshots := clientIP.service.Snapshot(); len(snapshots) != 0 {
			t.Fatalf("source lease was not released after callback error: %+v", snapshots)
		}
	})
	t.Run("normal terminal", func(t *testing.T) {
		clientIP := &clientIPRecorder{service: gatewayclientipconcurrency.NewService(nil)}
		service := newServiceWithClientIP(t, &orchestratorStub{result: readyResult()}, clientIP, newClaimingExecutor(t, &slotStub{acquireResult: platformredis.AccountSlotAcquireResult{Acquired: true, Lease: platformredis.AccountSlotLease{AccountID: "resource", Lane: platformredis.AccountSlotLaneText, Token: "token"}}}, attemptExecutorFunc(func(context.Context, gatewayattemptloop.Attempt) (gatewayattemptloop.AttemptResult, error) {
			return gatewayattemptloop.AttemptResult{Success: true, Committed: true}, nil
		})))
		if _, err := service.Run(t.Context(), executionInputWithClientIPLimit(1)); err != nil {
			t.Fatal(err)
		}
		if snapshots := clientIP.service.Snapshot(); len(snapshots) != 0 {
			t.Fatalf("source lease was not released after normal terminal: %+v", snapshots)
		}
	})
}

func TestRunRetainedPreAcquiredLeaseSurvivesStrictCallbackAndInitialFallback(t *testing.T) {
	t.Run("ready without fallback", func(t *testing.T) {
		clientIP := &clientIPRecorder{service: gatewayclientipconcurrency.NewService(nil)}
		service := newServiceWithClientIP(t, &orchestratorStub{result: readyResult()}, clientIP, newClaimingExecutor(t, &slotStub{acquireResult: platformredis.AccountSlotAcquireResult{Acquired: true, Lease: platformredis.AccountSlotLease{AccountID: "resource", Lane: platformredis.AccountSlotLaneText, Token: "token"}}}, attemptExecutorFunc(func(context.Context, gatewayattemptloop.Attempt) (gatewayattemptloop.AttemptResult, error) {
			return gatewayattemptloop.AttemptResult{Success: true, Committed: true}, nil
		})))
		input := executionInputWithClientIPLimit(1)
		preAcquired, err := clientIP.service.Acquire(t.Context(), clientIPInputForTest(input))
		if err != nil || preAcquired.Lease == nil {
			t.Fatalf("pre-acquired=%+v err=%v", preAcquired, err)
		}
		input.PreAcquiredClientIP = &preAcquired
		input.RetainPreAcquiredClientIPLease = true
		input.PostSourceLeaseFallback = &postSourceLeaseFallbackStub{}
		if _, err := service.Run(t.Context(), input); err != nil {
			t.Fatal(err)
		}
		if snapshots := clientIP.service.Snapshot(); len(snapshots) != 1 || snapshots[0].Current != 1 {
			t.Fatalf("retained pre-acquired lease was released: %+v", snapshots)
		}
		preAcquired.Lease.Release()
	})

	t.Run("initial fallback validates and releases non-retained", func(t *testing.T) {
		clientIP := &clientIPRecorder{service: gatewayclientipconcurrency.NewService(nil)}
		service := newServiceWithClientIP(t, &orchestratorStub{preLease: gatewayhighconcurrencyorchestration.PreLeaseResult{Result: gatewayhighconcurrencyorchestration.Result{Outcome: gatewayhighconcurrencyorchestration.OutcomeFallback}}}, clientIP, newClaimingExecutor(t, &slotStub{}, attemptExecutorFunc(func(context.Context, gatewayattemptloop.Attempt) (gatewayattemptloop.AttemptResult, error) {
			t.Fatal("attempt must not run after initial fallback")
			return gatewayattemptloop.AttemptResult{}, nil
		})))
		input := executionInputWithClientIPLimit(1)
		preAcquired, err := clientIP.service.Acquire(t.Context(), clientIPInputForTest(input))
		if err != nil || preAcquired.Lease == nil {
			t.Fatalf("pre-acquired=%+v err=%v", preAcquired, err)
		}
		input.PreAcquiredClientIP = &preAcquired
		result, err := service.Run(t.Context(), input)
		if err != nil || result.Orchestration.Outcome != gatewayhighconcurrencyorchestration.OutcomeFallback {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		if snapshots := clientIP.service.Snapshot(); len(snapshots) != 0 {
			t.Fatalf("non-retained pre-acquired lease leaked after initial fallback: %+v", snapshots)
		}
	})
}

func TestRunRetainedPreAcquiredLeaseUsesStrictHandoffDuringInitialFallback(t *testing.T) {
	clientIP := &clientIPRecorder{service: gatewayclientipconcurrency.NewService(nil)}
	var target gatewayclientipconcurrency.Decision
	strict := &postSourceLeaseFallbackStub{prepare: func(ctx context.Context, _ string, handoff gatewayclientipconcurrency.TargetPreparationHandoff) (gatewayhighconcurrencyorchestration.FallbackResult, error) {
		targetInput := fallbackTargetClientIPInput(clientIP.input)
		decision, err := clientIP.service.Acquire(ctx, targetInput)
		if err != nil {
			return gatewayhighconcurrencyorchestration.FallbackResult{}, err
		}
		target = decision
		if err := handoff.CompleteTargetPreparation(targetInput, target); err != nil {
			return gatewayhighconcurrencyorchestration.FallbackResult{}, err
		}
		return gatewayhighconcurrencyorchestration.FallbackResult{Attempted: true}, nil
	}}
	orchestrator := &orchestratorStub{preFallbackReason: "initial-busy"}
	service := newServiceWithClientIP(t, orchestrator, clientIP, newClaimingExecutor(t, &slotStub{}, attemptExecutorFunc(func(context.Context, gatewayattemptloop.Attempt) (gatewayattemptloop.AttemptResult, error) {
		t.Fatal("attempt must not run after initial fallback")
		return gatewayattemptloop.AttemptResult{}, nil
	})))
	input := executionInputWithClientIPLimit(2)
	clientIP.input = clientIPInputForTest(input)
	preAcquired, err := clientIP.service.Acquire(t.Context(), clientIP.input)
	if err != nil || preAcquired.Lease == nil {
		t.Fatalf("pre-acquired=%+v err=%v", preAcquired, err)
	}
	input.PreAcquiredClientIP = &preAcquired
	input.RetainPreAcquiredClientIPLease = true
	input.PostSourceLeaseFallback = strict
	result, err := service.Run(t.Context(), input)
	if err != nil || result.Orchestration.Outcome != gatewayhighconcurrencyorchestration.OutcomeFallback || strict.calls != 1 || target.Lease == nil {
		t.Fatalf("result=%+v err=%v strict=%d target=%+v", result, err, strict.calls, target)
	}
	if snapshots := clientIP.service.Snapshot(); len(snapshots) != 1 || snapshots[0].Current != 1 {
		t.Fatalf("source did not transfer to target during initial fallback: %+v", snapshots)
	}
	target.Lease.Release()
}

func TestRunKeepsLegacyPostSourceLeaseFallback(t *testing.T) {
	clientIP := &clientIPRecorder{service: gatewayclientipconcurrency.NewService(nil)}
	legacy := &fallbackRequesterStub{result: gatewayhighconcurrencyorchestration.FallbackResult{Attempted: true}}
	service := newServiceWithClientIP(t, &orchestratorStub{result: readyResult(), afterFallbackReason: "post-lease-busy"}, clientIP, newClaimingExecutor(t, &slotStub{}, attemptExecutorFunc(func(context.Context, gatewayattemptloop.Attempt) (gatewayattemptloop.AttemptResult, error) {
		t.Fatal("inner attempt must not run after fallback")
		return gatewayattemptloop.AttemptResult{}, nil
	})))
	input := executionInputWithClientIPLimit(1)
	input.Fallback = legacy
	result, err := service.Run(t.Context(), input)
	if err != nil || result.Orchestration.Outcome != gatewayhighconcurrencyorchestration.OutcomeFallback || legacy.calls != 1 {
		t.Fatalf("result=%+v err=%v legacy=%d", result, err, legacy.calls)
	}
	if snapshots := clientIP.service.Snapshot(); len(snapshots) != 0 {
		t.Fatalf("legacy fallback left source lease: %+v", snapshots)
	}
}

func TestRunForwardsRequestScopedFallbackOverrideToOrchestration(t *testing.T) {
	override := &fallbackOverrideStub{}
	orchestrator := &orchestratorStub{preLease: gatewayhighconcurrencyorchestration.PreLeaseResult{Result: gatewayhighconcurrencyorchestration.Result{Outcome: gatewayhighconcurrencyorchestration.OutcomeFallback}}}
	orchestrator.onPrepareInput = func(input gatewayhighconcurrencyorchestration.Input) {
		if input.Fallback != override {
			t.Fatalf("fallback override=%T want=%T", input.Fallback, override)
		}
	}
	service := newService(t, orchestrator, newClaimingExecutor(t, &slotStub{}, attemptExecutorFunc(func(context.Context, gatewayattemptloop.Attempt) (gatewayattemptloop.AttemptResult, error) {
		t.Fatal("inner attempt must not run")
		return gatewayattemptloop.AttemptResult{}, nil
	})))
	input := executionInput()
	input.Fallback = override
	result, err := service.Run(t.Context(), input)
	if err != nil || result.Orchestration.Outcome != gatewayhighconcurrencyorchestration.OutcomeFallback || orchestrator.prepareCalls != 1 {
		t.Fatalf("result=%+v err=%v prepare=%d", result, err, orchestrator.prepareCalls)
	}
}

func TestRunQueuesOnlyAfterClientIPAcquireWhenInitialFallbackMisses(t *testing.T) {
	steps := make([]string, 0, 3)
	slots := &slotStub{acquireResult: platformredis.AccountSlotAcquireResult{Acquired: true, Lease: platformredis.AccountSlotLease{AccountID: "resource", Lane: platformredis.AccountSlotLaneText, Token: "token"}}}
	initial := readyResult()
	orchestrator := &orchestratorStub{
		preLease: gatewayhighconcurrencyorchestration.PreLeaseResult{
			Result:        gatewayhighconcurrencyorchestration.Result{Outcome: gatewayhighconcurrencyorchestration.OutcomeQueue, Window: initial.Window},
			RequiresQueue: true,
		},
		result:    initial,
		onPrepare: func() { steps = append(steps, "prepare") },
		onAfter:   func() { steps = append(steps, "after") },
	}
	clientIP := &clientIPStub{
		decision:  gatewayclientipconcurrency.Decision{Enabled: false, Acquired: true},
		onAcquire: func() { steps = append(steps, "client-ip") },
	}
	service := newServiceWithClientIP(t, orchestrator, clientIP, newClaimingExecutor(t, slots, attemptExecutorFunc(func(context.Context, gatewayattemptloop.Attempt) (gatewayattemptloop.AttemptResult, error) {
		return gatewayattemptloop.AttemptResult{Success: true, Committed: true}, nil
	})))
	result, err := service.Run(t.Context(), executionInput())
	if err != nil || result.Attempts == nil || strings.Join(steps, ",") != "prepare,client-ip,after" {
		t.Fatalf("result=%+v err=%v steps=%v", result, err, steps)
	}
}

func TestRunFailsClosedForMalformedPreLeaseBeforeClientIPAcquire(t *testing.T) {
	for _, preLease := range []gatewayhighconcurrencyorchestration.PreLeaseResult{
		{Result: gatewayhighconcurrencyorchestration.Result{Outcome: gatewayhighconcurrencyorchestration.OutcomeQueue}},
		{Result: gatewayhighconcurrencyorchestration.Result{Outcome: gatewayhighconcurrencyorchestration.OutcomeReady}, RequiresQueue: true},
		{Result: gatewayhighconcurrencyorchestration.Result{Outcome: gatewayhighconcurrencyorchestration.OutcomeFallback}, RequiresQueue: true},
		{Result: gatewayhighconcurrencyorchestration.Result{Outcome: gatewayhighconcurrencyorchestration.OutcomeBusy}},
	} {
		t.Run(string(preLease.Result.Outcome), func(t *testing.T) {
			slots := &slotStub{}
			clientIP := &clientIPStub{decision: gatewayclientipconcurrency.Decision{Enabled: false, Acquired: true}}
			orchestrator := &orchestratorStub{preLease: preLease}
			service := newServiceWithClientIP(t, orchestrator, clientIP, newClaimingExecutor(t, slots, attemptExecutorFunc(func(context.Context, gatewayattemptloop.Attempt) (gatewayattemptloop.AttemptResult, error) {
				t.Fatal("inner attempt must not run after malformed pre-lease")
				return gatewayattemptloop.AttemptResult{}, nil
			})))
			if _, err := service.Run(t.Context(), executionInput()); err == nil || orchestrator.prepareCalls != 1 || orchestrator.afterCalls != 0 || clientIP.calls != 0 || slots.acquireCalls != 0 {
				t.Fatalf("err=%v prepare=%d after=%d client-ip=%d slots=%d", err, orchestrator.prepareCalls, orchestrator.afterCalls, clientIP.calls, slots.acquireCalls)
			}
		})
	}
}

func TestRunReturnsRejectedClientIPAfterInitialReadyCheckWithoutClaim(t *testing.T) {
	slots := &slotStub{}
	clientIP := &clientIPStub{decision: gatewayclientipconcurrency.Decision{
		Enabled: true, Acquired: false, Reason: gatewayclientipconcurrency.RejectLimitReached, Current: 8, Limit: 8,
	}}
	orchestrator := &orchestratorStub{result: readyResult()}
	service := newServiceWithClientIP(t, orchestrator, clientIP, newClaimingExecutor(t, slots, attemptExecutorFunc(func(context.Context, gatewayattemptloop.Attempt) (gatewayattemptloop.AttemptResult, error) {
		t.Fatal("inner attempt must not run after rejected client IP slot")
		return gatewayattemptloop.AttemptResult{}, nil
	})))
	input := executionInput()
	input.ClientIP = "203.0.113.4"
	result, err := service.Run(t.Context(), input)
	if err != nil || result.ClientIP == nil || result.ClientIP.Acquired || result.ClientIP.Reason != gatewayclientipconcurrency.RejectLimitReached || orchestrator.prepareCalls != 1 || orchestrator.afterCalls != 0 || slots.acquireCalls != 0 {
		t.Fatalf("result=%+v err=%v orchestration=%d/%d slots=%d", result, err, orchestrator.prepareCalls, orchestrator.afterCalls, slots.acquireCalls)
	}
	if clientIP.calls != 1 || clientIP.input.ClientIP != input.ClientIP || clientIP.input.GroupID != "group" || clientIP.input.SystemAccountID != "sys" || clientIP.input.APIKeyID != "key" {
		t.Fatalf("client IP calls=%d input=%+v", clientIP.calls, clientIP.input)
	}
}

func TestRunFailsClosedForMalformedClientIPLeaseDecision(t *testing.T) {
	for _, decision := range []gatewayclientipconcurrency.Decision{
		{Enabled: true, Acquired: true},
		{Enabled: true, Acquired: false, Lease: &gatewayclientipconcurrency.Lease{}},
	} {
		slots := &slotStub{}
		orchestrator := &orchestratorStub{result: readyResult()}
		service := newServiceWithClientIP(t, orchestrator, &clientIPStub{decision: decision}, newClaimingExecutor(t, slots, attemptExecutorFunc(func(context.Context, gatewayattemptloop.Attempt) (gatewayattemptloop.AttemptResult, error) {
			t.Fatal("inner attempt must not run after malformed client IP decision")
			return gatewayattemptloop.AttemptResult{}, nil
		})))
		if _, err := service.Run(t.Context(), executionInput()); err == nil || orchestrator.prepareCalls != 1 || orchestrator.afterCalls != 0 || slots.acquireCalls != 0 {
			t.Fatalf("decision=%+v err=%v orchestration=%d/%d slots=%d", decision, err, orchestrator.prepareCalls, orchestrator.afterCalls, slots.acquireCalls)
		}
	}
}

func TestRunReleasesClientIPLeaseAfterAttemptCompletion(t *testing.T) {
	slots := &slotStub{acquireResult: platformredis.AccountSlotAcquireResult{Acquired: true, Lease: platformredis.AccountSlotLease{AccountID: "resource", Lane: platformredis.AccountSlotLaneText, Token: "token"}}}
	clientIP := &clientIPRecorder{service: gatewayclientipconcurrency.NewService(nil)}
	service := newServiceWithClientIP(t, &orchestratorStub{result: readyResult()}, clientIP, newClaimingExecutor(t, slots, attemptExecutorFunc(func(context.Context, gatewayattemptloop.Attempt) (gatewayattemptloop.AttemptResult, error) {
		return gatewayattemptloop.AttemptResult{Success: true, Committed: true}, nil
	})))
	input := executionInput()
	input.ClientIP = "203.0.113.5"
	result, err := service.Run(t.Context(), input)
	if err != nil || result.ClientIP == nil || !result.ClientIP.Acquired || clientIP.calls != 1 || len(clientIP.service.Snapshot()) != 0 {
		t.Fatalf("result=%+v err=%v calls=%d snapshot=%+v", result, err, clientIP.calls, clientIP.service.Snapshot())
	}
}

func TestRunUsesMatchingPreAcquiredClientIPLeaseAndRetainsItForTerminalOwner(t *testing.T) {
	clientIP := &clientIPRecorder{service: gatewayclientipconcurrency.NewService(nil)}
	input := executionInputWithClientIPLimit(1)
	clientInput, err := clientIPInputFor(input)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := clientIP.Acquire(t.Context(), clientInput)
	if err != nil || prepared.Lease == nil {
		t.Fatalf("prepared=%+v err=%v", prepared, err)
	}
	service := newServiceWithClientIP(t, &orchestratorStub{result: readyResult()}, clientIP, newClaimingExecutor(t, &slotStub{acquireResult: platformredis.AccountSlotAcquireResult{Acquired: true, Lease: platformredis.AccountSlotLease{AccountID: "resource", Lane: platformredis.AccountSlotLaneText, Token: "token"}}}, attemptExecutorFunc(func(context.Context, gatewayattemptloop.Attempt) (gatewayattemptloop.AttemptResult, error) {
		return gatewayattemptloop.AttemptResult{Success: true, Committed: true}, nil
	})))
	input.PreAcquiredClientIP = &prepared
	input.RetainPreAcquiredClientIPLease = true
	result, err := service.Run(t.Context(), input)
	if err != nil || result.Attempts == nil || clientIP.calls != 1 {
		t.Fatalf("result=%+v err=%v client-ip=%d", result, err, clientIP.calls)
	}
	if snapshots := clientIP.service.Snapshot(); len(snapshots) != 1 || snapshots[0].Current != 1 {
		t.Fatalf("pre-acquired target lease was not retained: %+v", snapshots)
	}
	prepared.Lease.Release()
	if snapshots := clientIP.service.Snapshot(); len(snapshots) != 0 {
		t.Fatalf("terminal owner release snapshots=%+v", snapshots)
	}
}

func TestRunRejectsMismatchedOrRetainWithoutPreAcquiredClientIPLease(t *testing.T) {
	clientIP := &clientIPRecorder{service: gatewayclientipconcurrency.NewService(nil)}
	input := executionInputWithClientIPLimit(1)
	preparedInput, err := clientIPInputFor(input)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := clientIP.Acquire(t.Context(), preparedInput)
	if err != nil {
		t.Fatal(err)
	}
	service := newServiceWithClientIP(t, &orchestratorStub{result: readyResult()}, clientIP, newClaimingExecutor(t, &slotStub{}, attemptExecutorFunc(func(context.Context, gatewayattemptloop.Attempt) (gatewayattemptloop.AttemptResult, error) {
		t.Fatal("inner attempt must not run")
		return gatewayattemptloop.AttemptResult{}, nil
	})))
	mismatched := input
	mismatched.ClientIP = "203.0.113.99"
	mismatched.PreAcquiredClientIP = &prepared
	if _, err := service.Run(t.Context(), mismatched); err == nil || clientIP.calls != 1 {
		t.Fatalf("mismatched pre-acquired decision err=%v calls=%d", err, clientIP.calls)
	}
	if _, err := service.Run(t.Context(), Input{Orchestration: input.Orchestration, FinalLane: input.FinalLane, RetainPreAcquiredClientIPLease: true}); err == nil {
		t.Fatal("retain without pre-acquired decision accepted")
	}
	prepared.Lease.Release()
}

func TestRunPassesRequestLifecycleToClaimedAttempts(t *testing.T) {
	slots := &slotStub{acquireResult: platformredis.AccountSlotAcquireResult{Acquired: true, Lease: platformredis.AccountSlotLease{AccountID: "resource", Lane: platformredis.AccountSlotLaneText, Token: "token"}}}
	sink := gatewaystreamrelay.SinkState{TransportCommitted: true, SemanticCommitted: true, DownstreamBytes: 3}
	lifecycle := &highExecutionLifecycleStub{}
	service := newService(t, &orchestratorStub{result: readyResult()}, newClaimingExecutor(t, slots, attemptExecutorFunc(func(context.Context, gatewayattemptloop.Attempt) (gatewayattemptloop.AttemptResult, error) {
		return gatewayattemptloop.AttemptResult{Success: true, Committed: true, Sink: &sink}, nil
	})))
	input := executionInput()
	input.Lifecycle = lifecycle
	result, err := service.Run(t.Context(), input)
	if err != nil || result.Attempts == nil || result.Attempts.Outcome != gatewayattemptloop.OutcomeSucceeded || strings.Join(lifecycle.operations, ",") != "start,observe,success" || lifecycle.sink != sink {
		t.Fatalf("result=%+v err=%v operations=%v sink=%+v", result, err, lifecycle.operations, lifecycle.sink)
	}
}

func TestRunDefersResponseTerminalForClaimedAttempts(t *testing.T) {
	slots := &slotStub{acquireResult: platformredis.AccountSlotAcquireResult{Acquired: true, Lease: platformredis.AccountSlotLease{AccountID: "resource", Lane: platformredis.AccountSlotLaneText, Token: "token"}}}
	sink := gatewaystreamrelay.SinkState{TransportCommitted: true, SemanticCommitted: true, DownstreamBytes: 3}
	lifecycle := &highExecutionLifecycleStub{}
	service := newService(t, &orchestratorStub{result: readyResult()}, newClaimingExecutor(t, slots, attemptExecutorFunc(func(context.Context, gatewayattemptloop.Attempt) (gatewayattemptloop.AttemptResult, error) {
		return gatewayattemptloop.AttemptResult{Success: true, Committed: true, Sink: &sink, Response: &gatewayresponse.Result{State: gatewayresponse.StateSucceeded, TransportCommitted: true, SemanticCommitted: true}}, nil
	})))
	input := executionInput()
	input.Lifecycle = lifecycle
	input.DeferResponseTerminal = true
	result, err := service.Run(t.Context(), input)
	if err != nil || result.Attempts == nil || result.Attempts.PendingResponseTerminal == nil || strings.Join(lifecycle.operations, ",") != "start" {
		t.Fatalf("result=%+v err=%v operations=%v", result, err, lifecycle.operations)
	}
}

func TestRunPreservesLifecycleOnCandidatesExhaustedWhenOuterOwnerAuthorizedIt(t *testing.T) {
	slots := &slotStub{acquireResult: platformredis.AccountSlotAcquireResult{Acquired: true, Lease: platformredis.AccountSlotLease{AccountID: "resource", Lane: platformredis.AccountSlotLaneText, Token: "token"}}}
	lifecycle := &highExecutionLifecycleStub{}
	service, err := NewService(Options{
		Orchestrator: &orchestratorStub{result: readyResult()},
		ClientIP:     gatewayclientipconcurrency.NewService(nil),
		ClaimingExecutor: newClaimingExecutor(t, slots, attemptExecutorFunc(func(context.Context, gatewayattemptloop.Attempt) (gatewayattemptloop.AttemptResult, error) {
			return gatewayattemptloop.AttemptResult{RetryAllowed: true}, nil
		})),
		AttemptConfig: gatewayattemptloop.Config{MaxAttempts: 2, DisableWallTimeout: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	input := executionInput()
	input.Lifecycle = lifecycle
	input.PreserveLifecycleOnCandidatesExhausted = true
	result, err := service.Run(t.Context(), input)
	if err != nil || result.Attempts == nil || result.Attempts.Outcome != gatewayattemptloop.OutcomeCandidatesExhausted || strings.Join(lifecycle.operations, ",") != "start,retry" {
		t.Fatalf("result=%+v err=%v operations=%v", result, err, lifecycle.operations)
	}
}

func TestNewServiceRequiresClaimingExecutor(t *testing.T) {
	if _, err := NewService(Options{Orchestrator: &orchestratorStub{}, ClientIP: gatewayclientipconcurrency.NewService(nil), AttemptConfig: gatewayattemptloop.Config{DisableWallTimeout: true}}); err == nil {
		t.Fatal("missing claiming executor accepted")
	}
	if _, err := NewService(Options{Orchestrator: &orchestratorStub{}, AttemptConfig: gatewayattemptloop.Config{DisableWallTimeout: true}}); err == nil {
		t.Fatal("missing client IP acquirer accepted")
	}
}

func newService(t *testing.T, orchestrator Orchestrator, executor *gatewaycandidateclaim.ClaimingExecutor) *Service {
	return newServiceWithClientIP(t, orchestrator, gatewayclientipconcurrency.NewService(nil), executor)
}

func newServiceWithClientIP(t *testing.T, orchestrator Orchestrator, clientIP ClientIPAcquirer, executor *gatewaycandidateclaim.ClaimingExecutor) *Service {
	t.Helper()
	service, err := NewService(Options{Orchestrator: orchestrator, ClientIP: clientIP, ClaimingExecutor: executor, AttemptConfig: gatewayattemptloop.Config{MaxAttempts: 1, DisableWallTimeout: true}})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func executionInput() Input {
	return Input{
		Orchestration: gatewayhighconcurrencyorchestration.Input{Window: readyResult().Window, Lane: gatewayingress.LaneText, APIKeyID: "key"},
		MutationID:    "mutation",
		TraceID:       "trace",
		Request:       protocolgateway.RequestShape{Method: "POST", Path: "/v1/responses", Model: "gpt-test"},
		FinalLane:     gatewayingress.LaneText,
	}
}

func executionInputWithClientIPLimit(limit int) Input {
	input := executionInput()
	policy := groupscheduling.DefaultHighConcurrencyPolicy()
	policy.ClientIPConcurrencyLimit = limit
	input.Orchestration.Window.Access.SchedulingPolicyJSON = mustPolicyJSON(policy)
	input.ClientIP = "203.0.113.5"
	return input
}

func fallbackTargetClientIPInput(input gatewayclientipconcurrency.Input) gatewayclientipconcurrency.Input {
	input.GroupID = "target-group"
	return input
}

func clientIPInputForTest(input Input) gatewayclientipconcurrency.Input {
	result, err := clientIPInputFor(input)
	if err != nil {
		panic(err)
	}
	return result
}

type clientIPStub struct {
	decision  gatewayclientipconcurrency.Decision
	err       error
	input     gatewayclientipconcurrency.Input
	calls     int
	onAcquire func()
}

type fallbackRequesterStub struct {
	result gatewayhighconcurrencyorchestration.FallbackResult
	err    error
	calls  int
}

func (s *fallbackRequesterStub) RequestFallback(context.Context, string) (gatewayhighconcurrencyorchestration.FallbackResult, error) {
	s.calls++
	return s.result, s.err
}

type postSourceLeaseFallbackStub struct {
	prepare func(context.Context, string, gatewayclientipconcurrency.TargetPreparationHandoff) (gatewayhighconcurrencyorchestration.FallbackResult, error)
	calls   int
}

func (s *postSourceLeaseFallbackStub) PrepareFallbackTarget(ctx context.Context, reason string, handoff gatewayclientipconcurrency.TargetPreparationHandoff) (gatewayhighconcurrencyorchestration.FallbackResult, error) {
	s.calls++
	if s.prepare == nil {
		return gatewayhighconcurrencyorchestration.FallbackResult{}, nil
	}
	return s.prepare(ctx, reason, handoff)
}

func (s *clientIPStub) Acquire(_ context.Context, input gatewayclientipconcurrency.Input) (gatewayclientipconcurrency.Decision, error) {
	s.calls++
	s.input = input
	if s.onAcquire != nil {
		s.onAcquire()
	}
	return s.decision, s.err
}

type clientIPRecorder struct {
	service *gatewayclientipconcurrency.Service
	calls   int
	input   gatewayclientipconcurrency.Input
}

type highExecutionLifecycleStub struct {
	operations []string
	sink       gatewaystreamrelay.SinkState
}

func (s *highExecutionLifecycleStub) Start() error {
	s.operations = append(s.operations, "start")
	return nil
}

func (s *highExecutionLifecycleStub) ObserveSink(value gatewaystreamrelay.SinkState) error {
	s.operations = append(s.operations, "observe")
	s.sink = value
	return nil
}

func (s *highExecutionLifecycleStub) RetryPreCommit() error {
	s.operations = append(s.operations, "retry")
	return nil
}

func (s *highExecutionLifecycleStub) FinishSuccess() error {
	s.operations = append(s.operations, "success")
	return nil
}

func (s *highExecutionLifecycleStub) FinishFailure(kind string) error {
	s.operations = append(s.operations, "failure:"+kind)
	return nil
}

func (s *highExecutionLifecycleStub) CancelClient() error {
	s.operations = append(s.operations, "cancel")
	return nil
}

func (s *clientIPRecorder) Acquire(ctx context.Context, input gatewayclientipconcurrency.Input) (gatewayclientipconcurrency.Decision, error) {
	s.calls++
	s.input = input
	return s.service.Acquire(ctx, input)
}

func readyResult() gatewayhighconcurrencyorchestration.Result {
	policy := groupscheduling.DefaultHighConcurrencyPolicy()
	row := candidateRow()
	return gatewayhighconcurrencyorchestration.Result{
		Outcome: gatewayhighconcurrencyorchestration.OutcomeReady,
		Window: gatewaycandidatewindow.Window{
			Access:     port.GatewayGroupAccess{GroupID: row.GroupID, CallerSystemAccountID: row.SystemAccountID, GroupType: "high_concurrency", SchedulingPolicyJSON: mustPolicyJSON(policy)},
			Candidates: []gatewaycandidatewindow.Candidate{candidate(row)},
		},
	}
}

type orchestratorStub struct {
	preLease            gatewayhighconcurrencyorchestration.PreLeaseResult
	result              gatewayhighconcurrencyorchestration.Result
	prepareCalls        int
	afterCalls          int
	onPrepare           func()
	onPrepareInput      func(gatewayhighconcurrencyorchestration.Input)
	onAfter             func()
	preFallbackReason   string
	afterFallbackReason string
}

func (s *orchestratorStub) PrepareBeforeClientIP(_ context.Context, input gatewayhighconcurrencyorchestration.Input) (gatewayhighconcurrencyorchestration.PreLeaseResult, error) {
	s.prepareCalls++
	if s.onPrepareInput != nil {
		s.onPrepareInput(input)
	}
	if s.onPrepare != nil {
		s.onPrepare()
	}
	if s.preFallbackReason != "" {
		if input.Fallback == nil {
			return gatewayhighconcurrencyorchestration.PreLeaseResult{}, context.Canceled
		}
		fallback, err := input.Fallback.RequestFallback(context.Background(), s.preFallbackReason)
		if err != nil {
			return gatewayhighconcurrencyorchestration.PreLeaseResult{}, err
		}
		outcome := gatewayhighconcurrencyorchestration.OutcomeQueue
		if fallback.Attempted {
			outcome = gatewayhighconcurrencyorchestration.OutcomeFallback
		}
		return gatewayhighconcurrencyorchestration.PreLeaseResult{Result: gatewayhighconcurrencyorchestration.Result{Outcome: outcome}, RequiresQueue: !fallback.Attempted}, nil
	}
	if s.preLease.Result.Outcome != "" || s.preLease.RequiresQueue {
		return s.preLease, nil
	}
	return gatewayhighconcurrencyorchestration.PreLeaseResult{Result: s.result}, nil
}

type fallbackOverrideStub struct{}

func (*fallbackOverrideStub) RequestFallback(context.Context, string) (gatewayhighconcurrencyorchestration.FallbackResult, error) {
	return gatewayhighconcurrencyorchestration.FallbackResult{}, nil
}

func (s *orchestratorStub) RunAfterClientIP(ctx context.Context, input gatewayhighconcurrencyorchestration.Input, preLease gatewayhighconcurrencyorchestration.PreLeaseResult) (gatewayhighconcurrencyorchestration.Result, error) {
	s.afterCalls++
	if s.onAfter != nil {
		s.onAfter()
	}
	if s.afterFallbackReason != "" {
		if input.Fallback == nil {
			return gatewayhighconcurrencyorchestration.Result{}, context.Canceled
		}
		fallback, err := input.Fallback.RequestFallback(ctx, s.afterFallbackReason)
		if err != nil {
			return gatewayhighconcurrencyorchestration.Result{}, err
		}
		outcome := gatewayhighconcurrencyorchestration.OutcomeBusy
		if fallback.Attempted {
			outcome = gatewayhighconcurrencyorchestration.OutcomeFallback
		}
		return gatewayhighconcurrencyorchestration.Result{Outcome: outcome}, nil
	}
	if preLease.RequiresQueue {
		return s.result, nil
	}
	return preLease.Result, nil
}

type attemptExecutorFunc func(context.Context, gatewayattemptloop.Attempt) (gatewayattemptloop.AttemptResult, error)

func (f attemptExecutorFunc) Execute(ctx context.Context, attempt gatewayattemptloop.Attempt) (gatewayattemptloop.AttemptResult, error) {
	return f(ctx, attempt)
}

func newClaimingExecutor(t *testing.T, slots *slotStub, inner gatewayattemptloop.AttemptExecutor) *gatewaycandidateclaim.ClaimingExecutor {
	t.Helper()
	row := candidateRow()
	claimService, err := gatewaycandidateclaim.NewService(gatewaycandidateclaim.Options{
		Reader: &claimReader{row: row}, Hydrator: &hydratorStub{candidate: candidate(row)}, Slots: slots,
		Now: func() time.Time { return time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	executor, err := gatewaycandidateclaim.NewClaimingExecutor(gatewaycandidateclaim.ClaimingExecutorOptions{
		Service: claimService, Inner: inner, OnReleaseError: func(error) {}, RefreshInterval: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	return executor
}

type claimReader struct{ row port.GatewayAccountCandidate }

func (r *claimReader) ResolveGatewayGroupAccess(context.Context, port.GatewayGroupAccessInput) (port.GatewayGroupAccess, bool, error) {
	return port.GatewayGroupAccess{GroupID: r.row.GroupID, CallerSystemAccountID: r.row.SystemAccountID}, true, nil
}

func (r *claimReader) ListGatewayAccountCandidates(context.Context, port.GatewayAccountCandidateListInput) ([]port.GatewayAccountCandidate, error) {
	return []port.GatewayAccountCandidate{r.row}, nil
}

type hydratorStub struct {
	candidate gatewaycandidatewindow.Candidate
}

func (h *hydratorStub) Hydrate(context.Context, gatewaycandidatewindow.HydrateInput) ([]gatewaycandidatewindow.HydrationResult, error) {
	return []gatewaycandidatewindow.HydrationResult{{AccountID: h.candidate.Projection.AccountID, Candidate: h.candidate}}, nil
}

type slotStub struct {
	acquireResult platformredis.AccountSlotAcquireResult
	acquire       platformredis.AccountSlotAcquireInput
	acquireCalls  int
	releaseCalls  int
}

func (s *slotStub) Acquire(_ context.Context, input platformredis.AccountSlotAcquireInput) (platformredis.AccountSlotAcquireResult, error) {
	s.acquire = input
	s.acquireCalls++
	return s.acquireResult, nil
}

func (*slotStub) Refresh(context.Context, platformredis.AccountSlotLease, time.Duration) (platformredis.AccountSlotRefreshResult, error) {
	return platformredis.AccountSlotRefreshResult{}, nil
}

func (s *slotStub) Release(context.Context, platformredis.AccountSlotLease) (bool, error) {
	s.releaseCalls++
	return true, nil
}

func candidateRow() port.GatewayAccountCandidate {
	return port.GatewayAccountCandidate{AccountID: "view", SystemAccountID: "sys", GroupID: "group", AccountAuthorizationID: "auth", ConfigRevision: 2, DispatchRevision: 3, ProviderCode: "openai", ProtocolCode: "openai", ProtocolVersion: "v1", Type: "api_key", ConcurrencyLimit: 1, ResourceAccountID: "resource", ResourceConfigRevision: 4, ResourceDispatchRevision: 5, ResourceProviderCode: "openai", ResourceProtocolCode: "openai", ResourceProtocolVersion: "v1", ResourceType: "api_key", ResourceConcurrencyLimit: 7}
}

func candidate(row port.GatewayAccountCandidate) gatewaycandidatewindow.Candidate {
	return gatewaycandidatewindow.Candidate{Projection: row, SupportedModels: []string{"gpt-test"}, APIKeyRuntime: []gatewaycandidatewindow.APIKeyRuntime{{KeyIndex: 2, KeyFingerprint: "fp", Status: "active"}}}
}

func mustPolicyJSON(policy groupscheduling.Policy) string {
	encoded, err := json.Marshal(policy)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

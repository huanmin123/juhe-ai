package gatewayhighconcurrencyorchestration

import (
	"context"
	"encoding/json"
	"testing"

	"juhe-ai/backend-go/internal/domain/groupscheduling"
	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
	"juhe-ai/backend-go/internal/modules/gatewaycapacityrouting"
	"juhe-ai/backend-go/internal/modules/gatewayhighconcurrencyqueue"
	"juhe-ai/backend-go/internal/modules/gatewayingress"
	"juhe-ai/backend-go/internal/modules/gatewaylanecapacity"
	"juhe-ai/backend-go/internal/store/port"
)

func TestRunUsesInitialFallbackBeforeQueue(t *testing.T) {
	capacity := &capacityStub{results: []gatewaycapacityrouting.Result{busyDecision()}}
	fallback := &fallbackStub{results: []FallbackResult{{Attempted: true}}}
	queue := &queueStub{}
	service := newService(t, capacity, fallback, &refresherStub{}, queue)
	result, err := service.Run(t.Context(), Input{Window: testWindow(), Lane: gatewayingress.LaneText})
	if err != nil || result.Outcome != OutcomeFallback || queue.calls != 0 || capacity.calls != 1 || fallback.calls != 1 {
		t.Fatalf("result=%+v err=%v calls=%d/%d/%d", result, err, capacity.calls, fallback.calls, queue.calls)
	}
}

func TestRunUsesRequestScopedFallbackOverrideBeforeClientIPQueue(t *testing.T) {
	capacity := &capacityStub{results: []gatewaycapacityrouting.Result{busyDecision()}}
	defaultFallback := &fallbackStub{results: []FallbackResult{{Attempted: false}}}
	override := &fallbackStub{results: []FallbackResult{{Attempted: true}}}
	queue := &queueStub{}
	service := newService(t, capacity, defaultFallback, &refresherStub{}, queue)

	result, err := service.Run(t.Context(), Input{Window: testWindow(), Lane: gatewayingress.LaneText, Fallback: override})
	if err != nil || result.Outcome != OutcomeFallback || capacity.calls != 1 || defaultFallback.calls != 0 || override.calls != 1 || queue.calls != 0 {
		t.Fatalf("result=%+v err=%v capacity=%d default=%d override=%d queue=%d", result, err, capacity.calls, defaultFallback.calls, override.calls, queue.calls)
	}
}

func TestRunReturnsInitialReadyWindowWithoutFallbackOrQueue(t *testing.T) {
	capacity := &capacityStub{results: []gatewaycapacityrouting.Result{readyDecision()}}
	fallback := &fallbackStub{}
	queue := &queueStub{}
	service := newService(t, capacity, fallback, &refresherStub{}, queue)
	result, err := service.Run(t.Context(), Input{Window: testWindow(), Lane: gatewayingress.LaneText})
	if err != nil || result.Outcome != OutcomeReady || result.Queue != nil || fallback.calls != 0 || queue.calls != 0 || capacity.calls != 1 {
		t.Fatalf("result=%+v err=%v calls=%d/%d/%d", result, err, capacity.calls, fallback.calls, queue.calls)
	}
}

func TestRunAfterClientIPFailsClosedForMalformedPreLease(t *testing.T) {
	capacity := &capacityStub{results: []gatewaycapacityrouting.Result{readyDecision()}}
	queue := &queueStub{}
	service := newService(t, capacity, &fallbackStub{}, &refresherStub{}, queue)
	window := testWindow()
	for _, preLease := range []PreLeaseResult{
		{Result: Result{Outcome: OutcomeFallback, Window: window}},
		{Result: Result{Outcome: OutcomeQueue, Window: window}, RequiresQueue: true},
		{Result: Result{Outcome: OutcomeReady, Window: window}, RequiresQueue: true},
	} {
		if _, err := service.RunAfterClientIP(t.Context(), Input{Window: window, Lane: gatewayingress.LaneText}, preLease); err == nil {
			t.Fatalf("pre-lease=%+v was accepted", preLease)
		}
	}
	if capacity.calls != 0 || queue.calls != 0 {
		t.Fatalf("malformed pre-lease must not observe or queue: capacity=%d queue=%d", capacity.calls, queue.calls)
	}
}

func TestRunAfterClientIPQueuesWhenInitiallyReadyGroupBecomesBusy(t *testing.T) {
	initial := readyDecision()
	postLease := busyDecision()
	final := readyDecision()
	capacity := &capacityStub{results: []gatewaycapacityrouting.Result{postLease, final}}
	fallback := &fallbackStub{}
	refreshed := testWindow()
	refreshed.Candidates[0].Projection.AccountID = "fresh-after-client-ip"
	queue := &queueStub{result: gatewayhighconcurrencyqueue.Result{Ready: true}}
	service := newService(t, capacity, fallback, &refresherStub{window: refreshed}, queue)
	window := testWindow()
	result, err := service.RunAfterClientIP(t.Context(), Input{Window: window, Lane: gatewayingress.LaneText, APIKeyID: "key"}, PreLeaseResult{Result: Result{Outcome: OutcomeReady, Window: window, Initial: initial, Final: initial}})
	if err != nil || result.Outcome != OutcomeReady || result.Window.Candidates[0].Projection.AccountID != "fresh-after-client-ip" || queue.calls != 1 || fallback.calls != 0 || capacity.calls != 2 {
		t.Fatalf("result=%+v err=%v capacity=%d fallback=%d queue=%d", result, err, capacity.calls, fallback.calls, queue.calls)
	}
}

func TestRunQueuesThenReturnsFreshReadyWindow(t *testing.T) {
	initial := busyDecision()
	final := readyDecision()
	capacity := &capacityStub{results: []gatewaycapacityrouting.Result{initial, final}}
	fallback := &fallbackStub{results: []FallbackResult{{Attempted: false}}}
	refreshed := testWindow()
	refreshed.Candidates[0].Projection.AccountID = "fresh"
	queue := &queueStub{result: gatewayhighconcurrencyqueue.Result{Ready: true}}
	service := newService(t, capacity, fallback, &refresherStub{window: refreshed}, queue)
	maxWait := 9
	result, err := service.Run(t.Context(), Input{Window: testWindow(), Lane: gatewayingress.LaneImage, APIKeyID: "key", MaxQueueWaitMS: &maxWait})
	if err != nil || result.Outcome != OutcomeReady || result.Window.Candidates[0].Projection.AccountID != "fresh" || result.Queue == nil || !result.Queue.Ready {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if fallback.calls != 1 || capacity.calls != 2 || queue.calls != 1 || queue.input.SystemAccountID != "caller" || queue.input.GroupID != "group" || queue.input.Lane != gatewayhighconcurrencyqueue.LaneImage || queue.input.AccountConcurrencyLimits["resource"] != 3 || queue.input.MaxWaitMS != &maxWait {
		t.Fatalf("calls=%d/%d/%d input=%+v", capacity.calls, fallback.calls, queue.calls, queue.input)
	}
}

func TestRunRechecksAndFallsBackAfterQueue(t *testing.T) {
	capacity := &capacityStub{results: []gatewaycapacityrouting.Result{busyDecision(), busyDecision()}}
	fallback := &fallbackStub{results: []FallbackResult{{Attempted: false}, {Attempted: true}}}
	queue := &queueStub{result: gatewayhighconcurrencyqueue.Result{Reason: gatewayhighconcurrencyqueue.RejectTimeout}}
	service := newService(t, capacity, fallback, &refresherStub{window: testWindow()}, queue)
	result, err := service.Run(t.Context(), Input{Window: testWindow(), Lane: gatewayingress.LaneText})
	if err != nil || result.Outcome != OutcomeFallback || result.Queue == nil || result.Queue.Reason != gatewayhighconcurrencyqueue.RejectTimeout || fallback.calls != 2 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, fallback.calls)
	}
}

func TestRunUsesRequestScopedFallbackOverrideAfterQueue(t *testing.T) {
	capacity := &capacityStub{results: []gatewaycapacityrouting.Result{busyDecision(), busyDecision()}}
	defaultFallback := &fallbackStub{results: []FallbackResult{{Attempted: true}}}
	override := &fallbackStub{results: []FallbackResult{{Attempted: false}, {Attempted: true}}}
	queue := &queueStub{result: gatewayhighconcurrencyqueue.Result{Reason: gatewayhighconcurrencyqueue.RejectTimeout}}
	service := newService(t, capacity, defaultFallback, &refresherStub{window: testWindow()}, queue)

	result, err := service.Run(t.Context(), Input{Window: testWindow(), Lane: gatewayingress.LaneText, Fallback: override})
	if err != nil || result.Outcome != OutcomeFallback || defaultFallback.calls != 0 || override.calls != 2 || queue.calls != 1 {
		t.Fatalf("result=%+v err=%v default=%d override=%d queue=%d", result, err, defaultFallback.calls, override.calls, queue.calls)
	}
}

func TestRunReturnsBusyOnlyAfterFinalFallbackIsUnavailable(t *testing.T) {
	capacity := &capacityStub{results: []gatewaycapacityrouting.Result{busyDecision(), busyDecision()}}
	fallback := &fallbackStub{results: []FallbackResult{{Attempted: false}, {Attempted: false}}}
	service := newService(t, capacity, fallback, &refresherStub{window: testWindow()}, &queueStub{})
	result, err := service.Run(t.Context(), Input{Window: testWindow(), Lane: gatewayingress.LaneText})
	if err != nil || result.Outcome != OutcomeBusy || result.FallbackReason != gatewaycapacityrouting.FallbackHighConcurrencyGroupBusy {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestRunDoesNotRefreshOrFallbackAfterQueueAbort(t *testing.T) {
	capacity := &capacityStub{results: []gatewaycapacityrouting.Result{busyDecision()}}
	fallback := &fallbackStub{results: []FallbackResult{{Attempted: false}}}
	queue := &queueStub{result: gatewayhighconcurrencyqueue.Result{Reason: gatewayhighconcurrencyqueue.RejectAborted}}
	refresher := &refresherStub{window: testWindow()}
	service := newService(t, capacity, fallback, refresher, queue)
	result, err := service.Run(t.Context(), Input{Window: testWindow(), Lane: gatewayingress.LaneText})
	if err != nil || result.Outcome != OutcomeAborted || capacity.calls != 1 || fallback.calls != 1 || refresher.calls != 0 {
		t.Fatalf("result=%+v err=%v calls=%d/%d/%d", result, err, capacity.calls, fallback.calls, refresher.calls)
	}
}

func TestQueueInputUsesMinimumLimitForSharedResource(t *testing.T) {
	window := testWindow()
	window.Candidates = append(window.Candidates, gatewaycandidatewindow.Candidate{Projection: port.GatewayAccountCandidate{AccountID: "view-two", SystemAccountID: "caller", GroupID: "group", ConcurrencyLimit: 9, ResourceAccountID: "resource", ResourceConcurrencyLimit: 2}})
	policy := groupscheduling.DefaultHighConcurrencyPolicy()
	input, err := queueInputForWindow(window, gatewayingress.LaneText, "", &policy, nil)
	if err != nil || len(input.AccountIDs) != 1 || input.AccountIDs[0] != "resource" || input.AccountConcurrencyLimits["resource"] != 2 {
		t.Fatalf("input=%+v err=%v", input, err)
	}
}

func TestRunFailsClosedForDriftAndIncompleteDecision(t *testing.T) {
	capacity := &capacityStub{results: []gatewaycapacityrouting.Result{busyDecision()}}
	service := newService(t, capacity, &fallbackStub{results: []FallbackResult{{}}}, &refresherStub{}, &queueStub{})
	if _, err := service.Run(t.Context(), Input{Window: testWindow(), Lane: gatewayingress.LaneText}); err == nil {
		t.Fatal("incomplete refresher output accepted")
	}
	bad := busyDecision()
	bad.SchedulingPolicy = nil
	service = newService(t, &capacityStub{results: []gatewaycapacityrouting.Result{bad}}, &fallbackStub{}, &refresherStub{}, &queueStub{})
	if _, err := service.Run(t.Context(), Input{Window: testWindow(), Lane: gatewayingress.LaneText}); err == nil {
		t.Fatal("incomplete capacity decision accepted")
	}
}

func TestRunFailsClosedForMissingCandidates(t *testing.T) {
	window := testWindow()
	window.Candidates = nil
	capacity := &capacityStub{results: []gatewaycapacityrouting.Result{readyDecision()}}
	service := newService(t, capacity, &fallbackStub{}, &refresherStub{}, &queueStub{})
	if _, err := service.Run(t.Context(), Input{Window: window, Lane: gatewayingress.LaneText}); err == nil || capacity.calls != 0 {
		t.Fatalf("err=%v capacity calls=%d", err, capacity.calls)
	}
}

func TestRunFailsClosedWhenRefreshedCandidatesAreMissing(t *testing.T) {
	capacity := &capacityStub{results: []gatewaycapacityrouting.Result{busyDecision()}}
	fallback := &fallbackStub{results: []FallbackResult{{Attempted: false}}}
	empty := testWindow()
	empty.Candidates = nil
	service := newService(t, capacity, fallback, &refresherStub{window: empty}, &queueStub{result: gatewayhighconcurrencyqueue.Result{Ready: true}})
	if _, err := service.Run(t.Context(), Input{Window: testWindow(), Lane: gatewayingress.LaneText}); err == nil || capacity.calls != 1 || fallback.calls != 1 {
		t.Fatalf("err=%v calls=%d/%d", err, capacity.calls, fallback.calls)
	}
}

func newService(t *testing.T, capacity CapacityEvaluator, fallback FallbackRequester, refresher CandidateRefresher, queue QueueWaiter) *Service {
	t.Helper()
	service, err := NewService(Options{Capacity: capacity, Fallback: fallback, Refresher: refresher, Queue: queue})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func testWindow() gatewaycandidatewindow.Window {
	policy := groupscheduling.DefaultHighConcurrencyPolicy()
	return gatewaycandidatewindow.Window{Access: port.GatewayGroupAccess{GroupID: "group", CallerSystemAccountID: "caller", GroupType: "high_concurrency", SchedulingPolicyJSON: mustPolicyJSON(policy)}, Candidates: []gatewaycandidatewindow.Candidate{{Projection: port.GatewayAccountCandidate{AccountID: "view", SystemAccountID: "caller", GroupID: "group", ConcurrencyLimit: 7, ResourceAccountID: "resource", ResourceConcurrencyLimit: 3}}}}
}

func busyDecision() gatewaycapacityrouting.Result {
	policy := groupscheduling.DefaultHighConcurrencyPolicy()
	return gatewaycapacityrouting.Result{GroupType: "high_concurrency", SchedulingPolicy: &policy, Observation: gatewaylanecapacity.Result{AllBusy: true}, FallbackReason: gatewaycapacityrouting.FallbackHighConcurrencyGroupBusy}
}

func readyDecision() gatewaycapacityrouting.Result {
	return gatewaycapacityrouting.Result{GroupType: "high_concurrency", Observation: gatewaylanecapacity.Result{AllBusy: false}}
}

func mustPolicyJSON(policy groupscheduling.Policy) string {
	value, err := json.Marshal(policy)
	if err != nil {
		panic(err)
	}
	return string(value)
}

type capacityStub struct {
	results []gatewaycapacityrouting.Result
	calls   int
}

func (s *capacityStub) Evaluate(context.Context, gatewaycandidatewindow.Window, gatewayingress.Lane) (gatewaycapacityrouting.Result, error) {
	result := s.results[s.calls]
	s.calls++
	return result, nil
}

type fallbackStub struct {
	results []FallbackResult
	calls   int
}

func (s *fallbackStub) RequestFallback(context.Context, string) (FallbackResult, error) {
	result := s.results[s.calls]
	s.calls++
	return result, nil
}

type refresherStub struct {
	window gatewaycandidatewindow.Window
	calls  int
}

func (s *refresherStub) RefreshHighConcurrencyCandidates(context.Context, gatewaycandidatewindow.Window) (gatewaycandidatewindow.Window, error) {
	s.calls++
	return s.window, nil
}

type queueStub struct {
	result gatewayhighconcurrencyqueue.Result
	calls  int
	input  gatewayhighconcurrencyqueue.Input
}

func (s *queueStub) Wait(_ context.Context, input gatewayhighconcurrencyqueue.Input) (gatewayhighconcurrencyqueue.Result, error) {
	s.calls++
	s.input = input
	return s.result, nil
}

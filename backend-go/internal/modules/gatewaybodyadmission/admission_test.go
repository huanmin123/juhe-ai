package gatewaybodyadmission

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestShouldAdmitRequiresEveryPreBodyFact(t *testing.T) {
	base := Eligibility{
		RouteStrategyMode:    RouteStrategyModeNormal,
		SchedulingPreference: SchedulingPreferenceSpeedFirst,
		GroupType:            GroupTypeHighConcurrency,
		HasCandidate:         true,
		RequestLane:          RequestLaneText,
	}
	if !ShouldAdmit(base) {
		t.Fatal("complete speed-first text eligibility must admit")
	}
	cases := []struct {
		name   string
		mutate func(*Eligibility)
	}{
		{"non-normal route", func(value *Eligibility) { value.RouteStrategyMode = "failover" }},
		{"non-speed preference", func(value *Eligibility) { value.SchedulingPreference = "cost_first" }},
		{"non-high concurrency group", func(value *Eligibility) { value.GroupType = "personal" }},
		{"no candidates", func(value *Eligibility) { value.HasCandidate = false }},
		{"image lane", func(value *Eligibility) { value.RequestLane = RequestLaneImage }},
		{"unknown lane", func(value *Eligibility) { value.RequestLane = "" }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			value := base
			test.mutate(&value)
			if ShouldAdmit(value) {
				t.Fatalf("ShouldAdmit(%+v) must skip", value)
			}
		})
	}
}

func TestEffectiveCapacityDeduplicatesPhysicalSourcesAndSaturates(t *testing.T) {
	candidates := []CapacityCandidate{
		{ResourceID: "resource-one", ConcurrencyLimit: 8},
		{ResourceID: "authorized-instance", CredentialSourceID: "resource-one", ConcurrencyLimit: 3},
		{ResourceID: "resource-two", ConcurrencyLimit: 4},
		{ResourceID: "resource-two", ConcurrencyLimit: 9},
		{ResourceID: "ignored-zero", ConcurrencyLimit: 0},
		{CredentialSourceID: "", ConcurrencyLimit: 5},
	}
	if got := EffectiveCapacity(candidates); got != 7 {
		t.Fatalf("EffectiveCapacity() = %d, want 7", got)
	}
	if got := EffectiveCapacity([]CapacityCandidate{{ResourceID: "one", ConcurrencyLimit: maxInt()}, {ResourceID: "two", ConcurrencyLimit: 1}}); got != maxInt() {
		t.Fatalf("overflow-safe capacity = %d, want %d", got, maxInt())
	}
}

func TestControllerFIFOAndIdempotentRelease(t *testing.T) {
	controller := NewController()
	first := mustAcquire(t, controller, acquireInput("key-one", 1, time.Second, 4, 2))
	secondResult := make(chan Decision, 1)
	thirdResult := make(chan Decision, 1)
	go func() {
		secondResult <- mustAcquireDecision(t, controller, acquireInput("key-two", 1, time.Second, 4, 2))
	}()
	waitFor(t, time.Second, func() bool { return queued(controller, testScope()) == 1 })
	go func() {
		thirdResult <- mustAcquireDecision(t, controller, acquireInput("key-three", 1, time.Second, 4, 2))
	}()
	waitFor(t, time.Second, func() bool { return queued(controller, testScope()) == 2 })

	first.Release()
	second := awaitDecision(t, secondResult)
	if !second.Acquired() {
		t.Fatalf("second decision = %+v", second)
	}
	select {
	case third := <-thirdResult:
		t.Fatalf("FIFO violated: third acquired before second release: %+v", third)
	case <-time.After(20 * time.Millisecond):
	}
	second.Lease.Release()
	third := awaitDecision(t, thirdResult)
	if !third.Acquired() {
		t.Fatalf("third decision = %+v", third)
	}
	third.Lease.Release()
	third.Lease.Release()
	waitFor(t, time.Second, func() bool { return scopeCount(controller) == 0 })
}

func TestControllerRejectsQueueAndPerKeyOverflow(t *testing.T) {
	controller := NewController()
	first := mustAcquire(t, controller, acquireInput("holder", 1, time.Second, 2, 1))
	queuedResult := make(chan Decision, 1)
	go func() {
		queuedResult <- mustAcquireDecision(t, controller, acquireInput("key-two", 1, time.Second, 2, 1))
	}()
	waitFor(t, time.Second, func() bool { return queued(controller, testScope()) == 1 })

	perKey := mustAcquireDecision(t, controller, acquireInput("key-two", 1, time.Second, 2, 1))
	if perKey.Reason != RejectAPIKeyQueueFull {
		t.Fatalf("per-key rejection = %+v", perKey)
	}
	secondQueuedResult := make(chan Decision, 1)
	go func() {
		secondQueuedResult <- mustAcquireDecision(t, controller, acquireInput("key-three", 1, time.Second, 2, 1))
	}()
	waitFor(t, time.Second, func() bool { return queued(controller, testScope()) == 2 })
	full := mustAcquireDecision(t, controller, acquireInput("key-four", 1, time.Second, 2, 1))
	if full.Reason != RejectQueueFull {
		t.Fatalf("queue-full rejection = %+v", full)
	}
	first.Release()
	queued := awaitDecision(t, queuedResult)
	queued.Lease.Release()
	secondQueued := awaitDecision(t, secondQueuedResult)
	secondQueued.Lease.Release()

	first = mustAcquire(t, controller, acquireInput("holder", 1, time.Second, 1, 1))
	disabled := mustAcquireDecision(t, controller, acquireInput("disabled", 1, 0, 1, 1))
	if disabled.Reason != RejectQueueDisabled {
		t.Fatalf("queue-disabled rejection = %+v", disabled)
	}
	first.Release()
}

func TestControllerCancellationAndTimeoutDoNotBlockSuccessors(t *testing.T) {
	controller := NewController()
	holder := mustAcquire(t, controller, acquireInput("holder", 1, time.Second, 3, 3))

	canceledContext, cancel := context.WithCancel(context.Background())
	canceledResult := make(chan Decision, 1)
	go func() {
		decision, err := controller.Acquire(canceledContext, acquireInput("canceled", 1, time.Second, 3, 3))
		if err != nil {
			t.Errorf("canceled acquire: %v", err)
			return
		}
		canceledResult <- decision
	}()
	waitFor(t, time.Second, func() bool { return queued(controller, testScope()) == 1 })
	cancel()
	if decision := awaitDecision(t, canceledResult); decision.Reason != RejectCanceled {
		t.Fatalf("cancel decision = %+v", decision)
	}

	timeoutResult := make(chan Decision, 1)
	go func() {
		timeoutResult <- mustAcquireDecision(t, controller, acquireInput("timeout", 1, 15*time.Millisecond, 3, 3))
	}()
	if decision := awaitDecision(t, timeoutResult); decision.Reason != RejectTimeout {
		t.Fatalf("timeout decision = %+v", decision)
	}

	successorResult := make(chan Decision, 1)
	go func() {
		successorResult <- mustAcquireDecision(t, controller, acquireInput("successor", 1, time.Second, 3, 3))
	}()
	waitFor(t, time.Second, func() bool { return queued(controller, testScope()) == 1 })
	holder.Release()
	successor := awaitDecision(t, successorResult)
	if !successor.Acquired() {
		t.Fatalf("successor must acquire after canceled and timed-out waiters: %+v", successor)
	}
	successor.Lease.Release()
}

func TestControllerCapacityIncreaseWakesExistingFIFO(t *testing.T) {
	controller := NewController()
	holder := mustAcquire(t, controller, acquireInput("holder", 1, time.Second, 4, 4))
	waiterResult := make(chan Decision, 1)
	go func() {
		waiterResult <- mustAcquireDecision(t, controller, acquireInput("waiter", 1, time.Second, 4, 4))
	}()
	waitFor(t, time.Second, func() bool { return queued(controller, testScope()) == 1 })

	newArrivalResult := make(chan Decision, 1)
	go func() {
		newArrivalResult <- mustAcquireDecision(t, controller, acquireInput("new-arrival", 2, time.Second, 4, 4))
	}()
	waiter := awaitDecision(t, waiterResult)
	if !waiter.Acquired() {
		t.Fatalf("existing waiter was not woken by increased capacity: %+v", waiter)
	}
	select {
	case newArrival := <-newArrivalResult:
		t.Fatalf("new arrival bypassed existing FIFO waiter: %+v", newArrival)
	case <-time.After(20 * time.Millisecond):
	}
	holder.Release()
	newArrival := awaitDecision(t, newArrivalResult)
	if !newArrival.Acquired() {
		t.Fatalf("new arrival after holder release = %+v", newArrival)
	}
	waiter.Lease.Release()
	newArrival.Lease.Release()
}

func TestControllerCanonicalizesScopeAndAPIKeyIdentity(t *testing.T) {
	controller := NewController()
	holderInput := acquireInput(" key ", 1, time.Second, 2, 1)
	holderInput.Scope = Scope{SystemAccountID: " system ", RouteStrategyID: " route ", GroupID: " group "}
	holder := mustAcquire(t, controller, holderInput)
	queuedResult := make(chan Decision, 1)
	go func() {
		queuedResult <- mustAcquireDecision(t, controller, acquireInput("key", 1, time.Second, 2, 1))
	}()
	waitFor(t, time.Second, func() bool { return queued(controller, testScope()) == 1 })
	if decision := mustAcquireDecision(t, controller, acquireInput(" key ", 1, time.Second, 2, 1)); decision.Reason != RejectAPIKeyQueueFull {
		t.Fatalf("canonical API-key queue limit = %+v", decision)
	}
	holder.Release()
	queued := awaitDecision(t, queuedResult)
	queued.Lease.Release()
}

func TestControllerConcurrentAcquireRelease(t *testing.T) {
	controller := NewController()
	const workers = 32
	var group sync.WaitGroup
	for index := 0; index < workers; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			decision, err := controller.Acquire(context.Background(), acquireInput("key-"+itoa(index), 4, time.Second, workers, workers))
			if err != nil {
				t.Errorf("Acquire(%d): %v", index, err)
				return
			}
			if !decision.Acquired() {
				t.Errorf("Acquire(%d) rejected: %+v", index, decision)
				return
			}
			decision.Lease.Release()
		}(index)
	}
	group.Wait()
	if got := scopeCount(controller); got != 0 {
		t.Fatalf("controller retained %d idle scopes", got)
	}
}

func mustAcquire(t *testing.T, controller *Controller, input AcquireInput) *Lease {
	t.Helper()
	decision := mustAcquireDecision(t, controller, input)
	if !decision.Acquired() {
		t.Fatalf("Acquire() rejected: %+v", decision)
	}
	return decision.Lease
}

func mustAcquireDecision(t *testing.T, controller *Controller, input AcquireInput) Decision {
	t.Helper()
	decision, err := controller.Acquire(context.Background(), input)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	return decision
}

func awaitDecision(t *testing.T, values <-chan Decision) Decision {
	t.Helper()
	select {
	case decision := <-values:
		return decision
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for admission decision")
		return Decision{}
	}
}

func waitFor(t *testing.T, wait time.Duration, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for condition")
}

func acquireInput(apiKey string, capacity int, maxWait time.Duration, queueSize int, perKey int) AcquireInput {
	return AcquireInput{Scope: testScope(), APIKeyID: apiKey, Capacity: capacity, MaxQueueWait: maxWait, MaxQueueSize: queueSize, PerAPIKeyQueueLimit: perKey}
}

func testScope() Scope {
	return Scope{SystemAccountID: "system", RouteStrategyID: "route", GroupID: "group"}
}

func queued(controller *Controller, scope Scope) int {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return len(controller.states[scope].queue)
}

func scopeCount(controller *Controller) int {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return len(controller.states)
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var encoded [20]byte
	index := len(encoded)
	for value > 0 {
		index--
		encoded[index] = byte('0' + value%10)
		value /= 10
	}
	return string(encoded[index:])
}

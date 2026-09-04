package gatewayhotquality

import (
	"context"
	"sync"
	"testing"
	"time"
)

func waitForQueued(t *testing.T, registry *SpeedFirstBodyAdmissionRegistry, queued int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		entries := registry.Snapshot()
		if len(entries) == 1 && entries[0].Queued >= queued {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("queued items = %+v", entries)
		}
		time.Sleep(time.Millisecond)
	}
}

func bodyAdmissionInput(mutate func(*SpeedFirstBodyAdmissionInput)) SpeedFirstBodyAdmissionInput {
	input := SpeedFirstBodyAdmissionInput{
		SystemAccountID:     "sys",
		RouteStrategyID:     "rs",
		GroupID:             "g1",
		APIKeyID:            "key-1",
		Capacity:            1,
		MaxQueueWaitMs:      5_000,
		MaxQueueSize:        4,
		PerAPIKeyQueueLimit: 2,
	}
	if mutate != nil {
		mutate(&input)
	}
	return input
}

func TestSpeedFirstBodyAdmissionImmediateAcquireAndRelease(t *testing.T) {
	registry := NewSpeedFirstBodyAdmissionRegistry()
	ctx := context.Background()
	decision := registry.Acquire(ctx, bodyAdmissionInput(nil))
	if !decision.Acquired || decision.WaitedMs != 0 || decision.Release == nil {
		t.Fatalf("decision = %+v", decision)
	}
	// second acquire with capacity 1 must queue (waiters block); verify state snapshot
	queued := make(chan SpeedFirstBodyAdmissionDecision, 1)
	go func() {
		queued <- registry.Acquire(ctx, bodyAdmissionInput(func(input *SpeedFirstBodyAdmissionInput) { input.APIKeyID = "key-2" }))
	}()
	deadline := time.Now().Add(time.Second)
	for len(registry.Snapshot()) == 0 || registry.Snapshot()[0].Queued != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("queued item not registered: %+v", registry.Snapshot())
		}
		time.Sleep(time.Millisecond)
	}
	entries := registry.Snapshot()
	if entries[0].Key != "sys:rs:g1" || entries[0].Active != 1 || entries[0].Capacity != 1 {
		t.Fatalf("snapshot = %+v", entries)
	}
	// release wakes the queued item
	decision.Release()
	woken := <-queued
	if !woken.Acquired || woken.WaitedMs <= 0 {
		t.Fatalf("woken decision = %+v", woken)
	}
	woken.Release()
	// empty state is cleaned up
	if remaining := registry.Snapshot(); len(remaining) != 0 {
		t.Fatalf("state leaked: %+v", remaining)
	}
}

func TestSpeedFirstBodyAdmissionQueuePolicyRejections(t *testing.T) {
	newOccupiedRegistry := func(t *testing.T) (*SpeedFirstBodyAdmissionRegistry, SpeedFirstBodyAdmissionDecision) {
		registry := NewSpeedFirstBodyAdmissionRegistry()
		held := registry.Acquire(context.Background(), bodyAdmissionInput(nil))
		if !held.Acquired {
			t.Fatalf("initial acquire failed")
		}
		return registry, held
	}

	t.Run("queue disabled while active slot is held", func(t *testing.T) {
		registry, held := newOccupiedRegistry(t)
		defer held.Release()
		decision := registry.Acquire(context.Background(), bodyAdmissionInput(func(input *SpeedFirstBodyAdmissionInput) {
			input.MaxQueueWaitMs = 0
		}))
		if decision.Acquired || decision.Reason != BodyAdmissionRejectQueueDisabled || decision.WaitedMs != 0 {
			t.Fatalf("decision = %+v", decision)
		}
	})

	t.Run("queue full", func(t *testing.T) {
		registry, held := newOccupiedRegistry(t)
		defer held.Release()
		ctx := context.Background()
		// fill the queue with another api key (MaxQueueSize 4)
		for i := 0; i < 3; i++ {
			go func() {
				registry.Acquire(ctx, bodyAdmissionInput(func(input *SpeedFirstBodyAdmissionInput) {
					input.APIKeyID = "other"
					input.MaxQueueWaitMs = 10_000
				}))
			}()
		}
		waitForQueued(t, registry, 1)
		// the incoming request shrinks the queue bound; the standing queue
		// exceeds it → queue_full without waiting
		decision := registry.Acquire(ctx, bodyAdmissionInput(func(input *SpeedFirstBodyAdmissionInput) {
			input.APIKeyID = "fresh"
			input.MaxQueueSize = 1
			input.MaxQueueWaitMs = 10_000
		}))
		if decision.Acquired || decision.Reason != BodyAdmissionRejectQueueFull {
			t.Fatalf("decision = %+v", decision)
		}
	})

	t.Run("api key queue full", func(t *testing.T) {
		registry, held := newOccupiedRegistry(t)
		defer held.Release()
		ctx := context.Background()
		// enqueue one item for key-1 (limit clamps to >= 1)
		go func() {
			registry.Acquire(ctx, bodyAdmissionInput(func(input *SpeedFirstBodyAdmissionInput) {
				input.PerAPIKeyQueueLimit = 5
				input.MaxQueueWaitMs = 10_000
			}))
		}()
		waitForQueued(t, registry, 1)
		decision := registry.Acquire(ctx, bodyAdmissionInput(func(input *SpeedFirstBodyAdmissionInput) {
			input.PerAPIKeyQueueLimit = 1
			input.MaxQueueWaitMs = 10_000
		}))
		if decision.Acquired || decision.Reason != BodyAdmissionRejectAPIKeyQueueFull {
			t.Fatalf("decision = %+v", decision)
		}
	})

	t.Run("aborted before enqueue", func(t *testing.T) {
		registry, held := newOccupiedRegistry(t)
		defer held.Release()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		decision := registry.Acquire(ctx, bodyAdmissionInput(nil))
		if decision.Acquired || decision.Reason != BodyAdmissionRejectAborted || decision.WaitedMs != 0 {
			t.Fatalf("decision = %+v", decision)
		}
	})

	t.Run("timeout while queued", func(t *testing.T) {
		registry, held := newOccupiedRegistry(t)
		defer held.Release()
		start := time.Now()
		decision := registry.Acquire(context.Background(), bodyAdmissionInput(func(input *SpeedFirstBodyAdmissionInput) {
			input.MaxQueueWaitMs = 30
		}))
		if decision.Acquired || decision.Reason != BodyAdmissionRejectTimeout || decision.WaitedMs < 20 {
			t.Fatalf("decision = %+v (elapsed %v)", decision, time.Since(start))
		}
	})

	t.Run("abort while queued", func(t *testing.T) {
		registry, held := newOccupiedRegistry(t)
		defer held.Release()
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(20 * time.Millisecond)
			cancel()
		}()
		decision := registry.Acquire(ctx, bodyAdmissionInput(func(input *SpeedFirstBodyAdmissionInput) {
			input.MaxQueueWaitMs = 10_000
		}))
		if decision.Acquired || decision.Reason != BodyAdmissionRejectAborted {
			t.Fatalf("decision = %+v", decision)
		}
	})

	t.Run("release is idempotent", func(t *testing.T) {
		registry := NewSpeedFirstBodyAdmissionRegistry()
		decision := registry.Acquire(context.Background(), bodyAdmissionInput(nil))
		decision.Release()
		decision.Release()
		if entries := registry.Snapshot(); len(entries) != 0 || (len(entries) == 1 && entries[0].Active != 0) {
			t.Fatalf("snapshot = %+v", entries)
		}
	})
}

func TestSpeedFirstBodyAdmissionDynamicCapacityUpdate(t *testing.T) {
	registry := NewSpeedFirstBodyAdmissionRegistry()
	// capacity 2 first
	decisionA := registry.Acquire(context.Background(), bodyAdmissionInput(func(input *SpeedFirstBodyAdmissionInput) {
		input.Capacity = 2
		input.APIKeyID = "a"
	}))
	if !decisionA.Acquired {
		t.Fatalf("decision = %+v", decisionA)
	}
	decisionB := registry.Acquire(context.Background(), bodyAdmissionInput(func(input *SpeedFirstBodyAdmissionInput) {
		input.Capacity = 2
		input.APIKeyID = "b"
	}))
	if !decisionB.Acquired {
		t.Fatalf("second acquire must fit capacity 2: %+v", decisionB)
	}
	decisionA.Release()
	decisionB.Release()
}

func TestSpeedFirstBodyAdmissionConcurrentFairness(t *testing.T) {
	registry := NewSpeedFirstBodyAdmissionRegistry()
	const workers = 24
	var wg sync.WaitGroup
	acquired := make(chan struct{}, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			decision := registry.Acquire(context.Background(), bodyAdmissionInput(func(input *SpeedFirstBodyAdmissionInput) {
				input.Capacity = 4
				input.MaxQueueWaitMs = 2_000
				input.MaxQueueSize = 64
				input.PerAPIKeyQueueLimit = 64
				input.APIKeyID = "key"
			}))
			if decision.Acquired {
				acquired <- struct{}{}
				time.Sleep(time.Millisecond)
				decision.Release()
			}
		}()
	}
	wg.Wait()
	close(acquired)
	successes := 0
	for range acquired {
		successes++
	}
	if successes == 0 {
		t.Fatalf("no worker acquired a slot")
	}
	if entries := registry.Snapshot(); len(entries) != 0 {
		t.Fatalf("states leaked: %+v", entries)
	}
}

func TestSpeedFirstBodyAdmissionDefaultRegistryFunctions(t *testing.T) {
	ClearSpeedFirstBodyAdmissionsForTest()
	decision := AcquireSpeedFirstBodyAdmission(context.Background(), bodyAdmissionInput(nil))
	if !decision.Acquired {
		t.Fatalf("decision = %+v", decision)
	}
	if entries := SpeedFirstBodyAdmissionSnapshot(); len(entries) != 1 {
		t.Fatalf("snapshot = %+v", entries)
	}
	decision.Release()
	ClearSpeedFirstBodyAdmissionsForTest()
}

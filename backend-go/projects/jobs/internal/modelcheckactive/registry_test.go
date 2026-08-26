package modelcheckactive

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestRegistryEnforcesPerKeyExclusionAndMatchingFinish(t *testing.T) {
	r := NewRegistry()
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	first, acquired, _ := r.TryStart(context.Background(), "system-account:1", Summary{TargetID: "target-1", StartedAt: now})
	if !acquired {
		t.Fatal("first active run was rejected")
	}
	_, acquired, current := r.TryStart(context.Background(), "system-account:1", Summary{TargetID: "target-2", StartedAt: now})
	if acquired || current.TargetID != "target-1" {
		t.Fatalf("second active run acquired=%v current=%#v", acquired, current)
	}
	first.Finish()
	if _, ok := r.Get("system-account:1"); ok {
		t.Fatal("finished active run remained registered")
	}
	third, acquired, _ := r.TryStart(context.Background(), "system-account:1", Summary{TargetID: "target-3", StartedAt: now})
	if !acquired {
		t.Fatal("key was not reusable after finish")
	}
	third.Finish()
}

func TestRegistryStopCancelsOnlyMatchingKeyAndPublishesStopRequested(t *testing.T) {
	r := NewRegistry()
	first, acquired, _ := r.TryStart(context.Background(), "system-account:1", Summary{RunID: "run-1"})
	if !acquired {
		t.Fatal("first active run was rejected")
	}
	_, otherAcquired, _ := r.TryStart(context.Background(), "system-account:2", Summary{RunID: "run-2"})
	if !otherAcquired {
		t.Fatal("different key should be independent")
	}
	stopped, ok := r.Stop("system-account:1")
	if !ok || !stopped.StopRequest {
		t.Fatalf("stop result=%#v ok=%v", stopped, ok)
	}
	select {
	case <-first.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("stop did not cancel active context")
	}
	if _, ok := r.Get("system-account:2"); !ok {
		t.Fatal("stop leaked into another key")
	}
	first.Finish()
}

func TestRegistryConcurrentStartsAllowOneHandlePerKey(t *testing.T) {
	r := NewRegistry()
	ctx := context.Background()
	var wg sync.WaitGroup
	var mu sync.Mutex
	acquired := 0
	var handle Handle
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			candidate, ok, _ := r.TryStart(ctx, "system-account:concurrent", Summary{})
			if !ok {
				return
			}
			mu.Lock()
			acquired++
			handle = candidate
			mu.Unlock()
		}()
	}
	wg.Wait()
	if acquired != 1 {
		t.Fatalf("concurrent acquired=%d", acquired)
	}
	handle.Finish()
}

func TestHandleUpdateCannotModifyReplacementRun(t *testing.T) {
	r := NewRegistry()
	first, acquired, _ := r.TryStart(context.Background(), "system-account:1", Summary{RunID: "first"})
	if !acquired {
		t.Fatal("first start failed")
	}
	first.Finish()
	second, acquired, _ := r.TryStart(context.Background(), "system-account:1", Summary{RunID: "second"})
	if !acquired {
		t.Fatal("second start failed")
	}
	if first.Update(Summary{RunID: "stale"}) {
		t.Fatal("stale handle updated replacement run")
	}
	current, ok := r.Get("system-account:1")
	if !ok || current.RunID != "second" {
		t.Fatalf("current=%#v ok=%v", current, ok)
	}
	second.Finish()
}

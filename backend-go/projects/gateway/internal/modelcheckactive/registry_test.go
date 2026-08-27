package modelcheckactive

import (
	"context"
	"testing"
)

func TestRegistryPreventsDuplicateAndStopsOwner(t *testing.T) {
	r := NewRegistry()
	first, ok, _ := r.TryStart(context.Background(), "sys:1", Summary{RunID: "run-1"})
	if !ok {
		t.Fatal("first run not acquired")
	}
	if _, ok, current := r.TryStart(context.Background(), "sys:1", Summary{RunID: "run-2"}); ok || current.RunID != "run-1" {
		t.Fatalf("duplicate acquired=%v current=%+v", ok, current)
	}
	stopped, ok := r.Stop("sys:1")
	if !ok || !stopped.StopRequest {
		t.Fatalf("stop=%+v ok=%v", stopped, ok)
	}
	select {
	case <-first.Context().Done():
	default:
		t.Fatal("stop did not cancel run context")
	}
	first.Finish()
	if _, ok, _ := r.TryStart(context.Background(), "sys:1", Summary{RunID: "run-3"}); !ok {
		t.Fatal("finished run still blocks new owner")
	}
}

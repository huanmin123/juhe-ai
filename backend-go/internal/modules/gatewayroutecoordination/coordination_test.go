package gatewayroutecoordination

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"juhe-ai/backend-go/internal/modules/gatewayrouting"
)

func TestMemoryStoreRoundRobinIsSharedAndReturnsCompleteFallbacks(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()
	snapshot := testSnapshot(gatewayrouting.ModeRoundRobin, 1, 1)
	first, err := store.Plan(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Plan(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := bindingIDs(first.Ordered), []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("first = %v, want %v", got, want)
	}
	if got, want := bindingIDs(second.Ordered), []string{"b", "a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("second = %v, want %v", got, want)
	}
	if !first.StateAdvanced || !second.StateAdvanced {
		t.Fatal("round robin did not mark state advanced")
	}
}

func TestMemoryStoreWeightedIsConcurrencySafe(t *testing.T) {
	store := NewMemoryStore()
	snapshot := testSnapshot(gatewayrouting.ModeWeighted, 3, 1)
	const requests = 40
	ids := make(chan string, requests)
	var group sync.WaitGroup
	for range requests {
		group.Add(1)
		go func() {
			defer group.Done()
			plan, err := store.Plan(context.Background(), snapshot)
			if err != nil {
				t.Errorf("Plan() error = %v", err)
				return
			}
			ids <- plan.Ordered[0].ID
		}()
	}
	group.Wait()
	close(ids)
	counts := map[string]int{}
	for id := range ids {
		counts[id]++
	}
	if got, want := counts, map[string]int{"a": 30, "b": 10}; !reflect.DeepEqual(got, want) {
		t.Fatalf("weighted counts = %#v, want %#v", got, want)
	}
}

func TestRevisionIsOrderStableButChangesWithSemantics(t *testing.T) {
	t.Parallel()
	base := testSnapshot(gatewayrouting.ModeWeighted, 2, 1)
	baseRevision, err := Revision(base)
	if err != nil {
		t.Fatal(err)
	}
	reordered := base
	reordered.Bindings = []gatewayrouting.Binding{base.Bindings[1], base.Bindings[0]}
	got, err := Revision(reordered)
	if err != nil {
		t.Fatal(err)
	}
	if got != baseRevision {
		t.Fatalf("reordered revision = %s, want %s", got, baseRevision)
	}
	changed := base
	changed.Bindings[0].Active = false
	got, err = Revision(changed)
	if err != nil {
		t.Fatal(err)
	}
	if got == baseRevision {
		t.Fatal("availability change reused revision")
	}
}

func TestSnapshotRejectsIncompleteOrUnsafeState(t *testing.T) {
	t.Parallel()
	bad := testSnapshot(gatewayrouting.ModeNormal, 1, 1)
	bad.Scope.RouteStrategyID = ""
	if _, err := Revision(bad); err == nil {
		t.Fatal("Revision() accepted empty scope")
	}
	bad = testSnapshot(gatewayrouting.ModeNormal, 1, 1)
	bad.Bindings = append(bad.Bindings, bad.Bindings[0])
	if _, err := Revision(bad); err == nil {
		t.Fatal("Revision() accepted duplicate binding")
	}
	bad = testSnapshot(gatewayrouting.ModeNormal, 1, 1)
	for index := range bad.Bindings {
		bad.Bindings[index].Active = false
	}
	if _, err := Revision(bad); err == nil {
		t.Fatal("Revision() accepted no eligible binding")
	}
}

func testSnapshot(mode gatewayrouting.Mode, firstWeight, secondWeight int) Snapshot {
	return Snapshot{Scope: Scope{SystemAccountID: "system", RouteStrategyID: "route"}, Mode: mode, Bindings: []gatewayrouting.Binding{
		{ID: "a", GroupID: "group-a", Priority: 1, Weight: firstWeight, Active: true, GroupEnabled: true},
		{ID: "b", GroupID: "group-b", Priority: 2, Weight: secondWeight, Active: true, GroupEnabled: true},
	}}
}

func bindingIDs(bindings []gatewayrouting.Binding) []string {
	result := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		result = append(result, binding.ID)
	}
	return result
}

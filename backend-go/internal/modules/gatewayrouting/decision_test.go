package gatewayrouting

import (
	"reflect"
	"testing"
)

func TestOrderBindingsFiltersAndSortsEligibleBindings(t *testing.T) {
	t.Parallel()

	result, err := OrderBindings(OrderInput{
		Mode: ModeSingle,
		Bindings: []Binding{
			{ID: "disabled", GroupID: "group-disabled", Priority: 1, Weight: 1, Active: true, GroupEnabled: false},
			{ID: "inactive", GroupID: "group-inactive", Priority: 1, Weight: 1, Active: false, GroupEnabled: true},
			{ID: "second", GroupID: "group-b", Priority: 2, Weight: 1, Active: true, GroupEnabled: true},
			{ID: "first-b", GroupID: "group-b", Priority: 1, Weight: 1, Active: true, GroupEnabled: true},
			{ID: "first-a", GroupID: "group-a", Priority: 1, Weight: 1, Active: true, GroupEnabled: true},
		},
	})
	if err != nil {
		t.Fatalf("OrderBindings() error = %v", err)
	}

	if got, want := bindingIDs(result.Bindings), []string{"first-a", "first-b", "second"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ordered IDs = %v, want %v", got, want)
	}
}

func TestOrderBindingsRoundRobinUsesProvidedSequenceWithoutState(t *testing.T) {
	t.Parallel()

	input := OrderInput{
		Mode:     ModeRoundRobin,
		Sequence: 4,
		Bindings: activeBindings("a", "b", "c"),
	}
	result, err := OrderBindings(input)
	if err != nil {
		t.Fatalf("OrderBindings() error = %v", err)
	}
	if got, want := bindingIDs(result.Bindings), []string{"b", "c", "a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("round-robin IDs = %v, want %v", got, want)
	}
	if result.NextWeightedState != nil {
		t.Fatalf("round-robin NextWeightedState = %#v, want nil", result.NextWeightedState)
	}
}

func TestOrderBindingsPassThroughModesUseConfiguredOrder(t *testing.T) {
	t.Parallel()

	for _, mode := range []Mode{ModeFailover, ModeHybridSmart} {
		result, err := OrderBindings(OrderInput{
			Mode: mode,
			Bindings: []Binding{
				{ID: "later", GroupID: "later", Priority: 2, Weight: 1, Active: true, GroupEnabled: true},
				{ID: "first", GroupID: "first", Priority: 1, Weight: 1, Active: true, GroupEnabled: true},
			},
		})
		if err != nil {
			t.Fatalf("OrderBindings() mode %q error = %v", mode, err)
		}
		if got, want := bindingIDs(result.Bindings), []string{"first", "later"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("mode %q IDs = %v, want %v", mode, got, want)
		}
	}
}

func TestOrderBindingsWeightedProducesSmoothWeightedOrderAndNextState(t *testing.T) {
	t.Parallel()

	bindings := []Binding{
		{ID: "high", GroupID: "group-high", Priority: 1, Weight: 3, Active: true, GroupEnabled: true},
		{ID: "low", GroupID: "group-low", Priority: 2, Weight: 1, Active: true, GroupEnabled: true},
	}

	first, err := OrderBindings(OrderInput{Mode: ModeWeighted, Bindings: bindings})
	if err != nil {
		t.Fatalf("first OrderBindings() error = %v", err)
	}
	if got, want := bindingIDs(first.Bindings), []string{"high", "low"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("first weighted IDs = %v, want %v", got, want)
	}
	if got, want := first.NextWeightedState, map[string]int{"high": -1, "low": 1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("first next state = %#v, want %#v", got, want)
	}

	second, err := OrderBindings(OrderInput{Mode: ModeWeighted, Bindings: bindings, WeightedState: first.NextWeightedState})
	if err != nil {
		t.Fatalf("second OrderBindings() error = %v", err)
	}
	if got, want := bindingIDs(second.Bindings), []string{"high", "low"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("second weighted IDs = %v, want %v", got, want)
	}

	third, err := OrderBindings(OrderInput{Mode: ModeWeighted, Bindings: bindings, WeightedState: second.NextWeightedState})
	if err != nil {
		t.Fatalf("third OrderBindings() error = %v", err)
	}
	if got, want := bindingIDs(third.Bindings), []string{"low", "high"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("third weighted IDs = %v, want %v", got, want)
	}
}

func TestOrderBindingsDoesNotMutateInputsAndDropsRemovedWeightedState(t *testing.T) {
	t.Parallel()

	bindings := []Binding{
		{ID: "kept", GroupID: "group-kept", Priority: 1, Weight: 2, Active: true, GroupEnabled: true},
		{ID: "removed", GroupID: "group-removed", Priority: 2, Weight: 1, Active: false, GroupEnabled: true},
	}
	state := map[string]int{"kept": 0, "removed": 99}
	result, err := OrderBindings(OrderInput{Mode: ModeWeighted, Bindings: bindings, WeightedState: state})
	if err != nil {
		t.Fatalf("OrderBindings() error = %v", err)
	}
	if got, want := state, map[string]int{"kept": 0, "removed": 99}; !reflect.DeepEqual(got, want) {
		t.Fatalf("input state mutated = %#v, want %#v", got, want)
	}
	if got, want := bindingIDs(bindings), []string{"kept", "removed"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("input bindings mutated = %v, want %v", got, want)
	}
	if got, want := result.NextWeightedState, map[string]int{"kept": 0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("next state = %#v, want %#v", got, want)
	}
}

func TestOrderBindingsRejectsUnsupportedModeAndInvalidWeight(t *testing.T) {
	t.Parallel()

	if _, err := OrderBindings(OrderInput{Mode: Mode("bogus"), Bindings: activeBindings("a")}); err == nil {
		t.Fatal("OrderBindings() error = nil, want unsupported mode error")
	}
	invalidBinding := []Binding{{ID: "a", GroupID: "a", Weight: 0, Active: true, GroupEnabled: true}}
	for _, mode := range []Mode{ModeSingle, ModeRoundRobin, ModeWeighted} {
		if _, err := OrderBindings(OrderInput{Mode: mode, Bindings: invalidBinding}); err == nil {
			t.Fatalf("OrderBindings() mode %q error = nil, want invalid weight error", mode)
		}
	}
}

func activeBindings(ids ...string) []Binding {
	bindings := make([]Binding, 0, len(ids))
	for index, id := range ids {
		bindings = append(bindings, Binding{ID: id, GroupID: id, Priority: index + 1, Weight: 1, Active: true, GroupEnabled: true})
	}
	return bindings
}

func bindingIDs(bindings []Binding) []string {
	ids := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		ids = append(ids, binding.ID)
	}
	return ids
}

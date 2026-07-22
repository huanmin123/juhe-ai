// Package gatewayrouting contains side-effect-free group binding decisions for
// the gateway. The runtime store owns counters and persists returned state.
package gatewayrouting

import (
	"fmt"
	"sort"
)

// Mode determines how eligible group bindings are ordered for an upstream
// attempt. ModeNormal (also exposed as ModeSingle) preserves configured order.
type Mode string

const (
	ModeNormal      Mode = "normal"
	ModeSingle           = ModeNormal
	ModeRoundRobin  Mode = "round_robin"
	ModeWeighted    Mode = "weighted"
	ModeFailover    Mode = "failover"
	ModeHybridSmart Mode = "hybrid_smart"
)

// Binding is the routing data needed by this decision layer. Active and
// GroupEnabled are explicit because callers may use a cached binding snapshot.
type Binding struct {
	ID           string
	GroupID      string
	Priority     int
	Weight       int
	Active       bool
	GroupEnabled bool
}

// OrderInput contains all state required to make a deterministic decision.
// Sequence is an externally-owned, monotonic counter for round-robin routing.
// WeightedState is keyed by binding ID and is never mutated by OrderBindings.
type OrderInput struct {
	Mode          Mode
	Bindings      []Binding
	Sequence      int64
	WeightedState map[string]int
}

// OrderResult includes the ordered fallbacks and, for weighted mode, the state
// that the runtime store should use for the next decision.
type OrderResult struct {
	Bindings          []Binding
	NextWeightedState map[string]int
}

// OrderBindings filters inactive bindings, applies the stable configured order,
// then applies the selected dispatch policy. It has no I/O and no shared state.
func OrderBindings(input OrderInput) (OrderResult, error) {
	bindings, err := eligibleAndSorted(input.Bindings)
	if err != nil {
		return OrderResult{}, err
	}

	switch input.Mode {
	case ModeNormal, ModeFailover, ModeHybridSmart:
		return OrderResult{Bindings: bindings}, nil
	case ModeRoundRobin:
		return OrderResult{Bindings: rotate(bindings, input.Sequence)}, nil
	case ModeWeighted:
		return orderWeighted(bindings, input.WeightedState)
	default:
		return OrderResult{}, fmt.Errorf("unsupported gateway route mode %q", input.Mode)
	}
}

func eligibleAndSorted(bindings []Binding) ([]Binding, error) {
	eligible := make([]Binding, 0, len(bindings))
	for _, binding := range bindings {
		if !binding.Active || !binding.GroupEnabled {
			continue
		}
		if binding.Weight < 1 || binding.Weight > 100 {
			return nil, fmt.Errorf("binding %q has invalid weight %d: must be within 1..100", binding.ID, binding.Weight)
		}
		eligible = append(eligible, binding)
	}
	sort.Slice(eligible, func(leftIndex, rightIndex int) bool {
		left := eligible[leftIndex]
		right := eligible[rightIndex]
		if left.Priority != right.Priority {
			return left.Priority < right.Priority
		}
		if left.GroupID != right.GroupID {
			return left.GroupID < right.GroupID
		}
		return left.ID < right.ID
	})
	return eligible, nil
}

func rotate(bindings []Binding, sequence int64) []Binding {
	if len(bindings) <= 1 {
		return bindings
	}
	length := int64(len(bindings))
	start := sequence % length
	if start < 0 {
		start += length
	}
	ordered := make([]Binding, 0, len(bindings))
	ordered = append(ordered, bindings[start:]...)
	ordered = append(ordered, bindings[:start]...)
	return ordered
}

func orderWeighted(bindings []Binding, currentState map[string]int) (OrderResult, error) {
	nextState := make(map[string]int, len(bindings))
	if len(bindings) == 0 {
		return OrderResult{Bindings: bindings, NextWeightedState: nextState}, nil
	}

	totalWeight := 0
	selectedIndex := -1
	selectedCurrentWeight := 0
	for index, binding := range bindings {
		totalWeight += binding.Weight
		current := currentState[binding.ID] + binding.Weight
		nextState[binding.ID] = current
		if selectedIndex == -1 || current > selectedCurrentWeight {
			selectedIndex = index
			selectedCurrentWeight = current
		}
	}

	selected := bindings[selectedIndex]
	nextState[selected.ID] -= totalWeight
	ordered := make([]Binding, 0, len(bindings))
	ordered = append(ordered, selected)
	remaining := append([]Binding(nil), bindings[:selectedIndex]...)
	remaining = append(remaining, bindings[selectedIndex+1:]...)
	sort.Slice(remaining, func(leftIndex, rightIndex int) bool {
		left := remaining[leftIndex]
		right := remaining[rightIndex]
		if nextState[left.ID] != nextState[right.ID] {
			return nextState[left.ID] > nextState[right.ID]
		}
		if left.Weight != right.Weight {
			return left.Weight > right.Weight
		}
		return bindingOrder(left, right)
	})
	ordered = append(ordered, remaining...)
	return OrderResult{Bindings: ordered, NextWeightedState: nextState}, nil
}

func bindingOrder(left, right Binding) bool {
	if left.Priority != right.Priority {
		return left.Priority < right.Priority
	}
	if left.GroupID != right.GroupID {
		return left.GroupID < right.GroupID
	}
	return left.ID < right.ID
}

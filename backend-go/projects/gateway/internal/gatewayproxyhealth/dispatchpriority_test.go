package gatewayproxyhealth

import (
	"testing"
)

// Ports runtime/account-dispatch-priority-order.ts behavior.

func TestGatewayAccountDispatchPriorityTier(t *testing.T) {
	priority := 5.0
	superTrue := true
	fallbackTrue := true
	tests := []struct {
		name     string
		view     DispatchPriorityAccountView
		ranks    map[string]int
		expected string
	}{
		{
			name:     "no options map keeps rank 0",
			view:     DispatchPriorityAccountView{ID: "a", Priority: &priority},
			expected: "0:0:1:5",
		},
		{
			name:     "unknown account in map falls back to rank 3",
			view:     DispatchPriorityAccountView{ID: "a"},
			ranks:    map[string]int{"other": 1},
			expected: "3:0:1:0",
		},
		{
			name:     "negative rank clamps to 0",
			view:     DispatchPriorityAccountView{ID: "a"},
			ranks:    map[string]int{"a": -4},
			expected: "0:0:1:0",
		},
		{
			name:     "super and fallback flip ranks",
			view:     DispatchPriorityAccountView{ID: "a", SuperPriorityEnabled: &superTrue, FallbackEnabled: &fallbackTrue},
			ranks:    map[string]int{"a": 2},
			expected: "2:1:0:0",
		},
		{
			name:     "priority truncates toward zero",
			view:     DispatchPriorityAccountView{ID: "a", Priority: float64Ptr(-2.9)},
			expected: "0:0:1:-2",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GatewayAccountDispatchPriorityTier(tt.view, DispatchPriorityOrderOptions{ModelRankByAccountID: tt.ranks})
			if got != tt.expected {
				t.Fatalf("tier = %q, want %q", got, tt.expected)
			}
		})
	}
}

func float64Ptr(v float64) *float64 { return &v }

func TestPreserveGatewayAccountDispatchPriorityTiers(t *testing.T) {
	makeView := func(a accountFixture) DispatchPriorityAccountView {
		priority := float64(a.priority)
		super := a.superPriority
		fallback := a.fallbackEnabled
		return DispatchPriorityAccountView{ID: a.id, Priority: &priority, SuperPriorityEnabled: &super, FallbackEnabled: &fallback}
	}
	base := []accountFixture{
		{id: "a", priority: 10},
		{id: "b", priority: 5},
		{id: "c", priority: 10, superPriority: true},
	}
	reordered := []accountFixture{
		{id: "c", priority: 10, superPriority: true},
		{id: "b", priority: 5},
		{id: "a", priority: 10},
	}
	accounts := make([]accountFixture, 0, len(base))
	accounts = append(accounts, base...)
	reorderedCopy := make([]accountFixture, 0, len(reordered))
	reorderedCopy = append(reorderedCopy, reordered...)

	got := PreserveGatewayAccountDispatchPriorityTiers(accounts, reorderedCopy, makeView, DispatchPriorityOrderOptions{})
	wantOrder := []string{"a", "b", "c"}
	for i, id := range wantOrder {
		if got[i].id != id {
			t.Fatalf("order[%d] = %s, want %s (full: %v)", i, got[i].id, id, ids(got))
		}
	}

	// Fewer than two base accounts returns a copy of the reordered input.
	single := []accountFixture{{id: "a"}}
	singleReordered := []accountFixture{{id: "b"}, {id: "a"}}
	out := PreserveGatewayAccountDispatchPriorityTiers(single, singleReordered, makeView, DispatchPriorityOrderOptions{})
	if len(out) != 2 || out[0].id != "b" || out[1].id != "a" {
		t.Fatalf("short-circuit order = %v", ids(out))
	}
}

func ids(accounts []accountFixture) []string {
	output := make([]string, 0, len(accounts))
	for _, account := range accounts {
		output = append(output, account.id)
	}
	return output
}

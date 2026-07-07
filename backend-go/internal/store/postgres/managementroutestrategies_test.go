package postgres

import "testing"

func TestManagementRouteStrategyOptionLimit(t *testing.T) {
	tests := []struct {
		input int
		want  int
	}{
		{input: 0, want: 50},
		{input: -1, want: 50},
		{input: 1, want: 1},
		{input: 100, want: 100},
		{input: 101, want: 100},
	}
	for _, tt := range tests {
		if got := managementRouteStrategyOptionLimit(tt.input); got != tt.want {
			t.Fatalf("managementRouteStrategyOptionLimit(%d) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestUniqueStrings(t *testing.T) {
	values := uniqueStrings([]string{" a ", "a", "", "b", "c"}, 2)
	if len(values) != 2 || values[0] != "a" || values[1] != "b" {
		t.Fatalf("uniqueStrings() = %#v", values)
	}
}

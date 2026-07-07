package postgres

import "testing"

func TestManagementAccountOptionLimit(t *testing.T) {
	tests := []struct {
		input int
		want  int
	}{
		{input: 0, want: 50},
		{input: -1, want: 50},
		{input: 1, want: 1},
		{input: 50, want: 50},
		{input: 51, want: 50},
	}
	for _, tt := range tests {
		if got := managementAccountOptionLimit(tt.input); got != tt.want {
			t.Fatalf("managementAccountOptionLimit(%d) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

package statsagg

import (
	"testing"
)

func assertStringList(t *testing.T, label string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %v want %v", label, got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("%s: got %v want %v", label, got, want)
		}
	}
}

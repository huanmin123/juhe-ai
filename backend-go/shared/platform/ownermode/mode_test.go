package ownermode

import "testing"

func TestLoadDefaultsToActive(t *testing.T) {
	mode, err := Load(func(string) string { return "" })
	if err != nil || mode != Active || !mode.OwnsWork() {
		t.Fatalf("default mode = %q, %v", mode, err)
	}
}

func TestLoadAcceptsPassiveStates(t *testing.T) {
	for _, expected := range []Mode{Standby, Drain} {
		mode, err := Load(func(string) string { return string(expected) })
		if err != nil || mode != expected || mode.OwnsWork() {
			t.Fatalf("mode %q = %q, %v", expected, mode, err)
		}
	}
}

func TestLoadRejectsUnknownMode(t *testing.T) {
	if _, err := Load(func(string) string { return "shadow" }); err == nil {
		t.Fatal("unknown mode must fail closed")
	}
}

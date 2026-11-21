package main

import (
	"os"
	"strings"
	"testing"
)

// The TTL and source-revision sweeps live in the gateway process (jobs cannot
// import internal/authz); main must wire the component next to the legacy
// expiry-runtime-sync compensator.
func TestMainWiresAuthzGovernanceSweepComponent(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), "components = append(components, newAuthzGovernanceSweepComponent(composed.AuthzStore))") {
		t.Fatal("main must append the authz governance sweep component")
	}
	componentSource, err := os.ReadFile("compose_authz_governance_sweep.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(componentSource)
	for _, needle := range []string{"ExpireDueOffers", "ReconcileSourceRevisions"} {
		if !strings.Contains(text, needle) {
			t.Fatalf("governance sweep must call %s", needle)
		}
	}
}

package main

import (
	"os"
	"strings"
	"testing"
)

// The TTL sweep lives in the gateway process (jobs cannot import
// internal/authz); main must wire the component next to the legacy
// expiry-runtime-sync compensator.（ReconcileSourceRevisions needle 随
// source-revision 校对域退役摘除：发送-领取模型下 sweep 只收敛 expires_at。）
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
	for _, needle := range []string{"ExpireDueOffers"} {
		if !strings.Contains(text, needle) {
			t.Fatalf("governance sweep must call %s", needle)
		}
	}
}

package circuitruntime

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAccountCircuitRuntimeLuaUsesReadyIndexAndDoesNotScanGlobalState(t *testing.T) {
	for name, script := range map[string]string{"mutation": accountCircuitRuntimeMutationLua, "escalation": accountCircuitRuntimeEscalationLua, "due": accountCircuitRuntimeListDueLua, "account revision": accountCircuitRuntimeReplaceAccountRevisionLua} {
		if strings.Contains(script, "HGETALL") {
			t.Fatalf("%s runtime script must use reverse indexes, not HGETALL", name)
		}
		for _, fragment := range []string{"status') ~= 'ready", "ownerMode') ~= 'go-runtime-state-v1"} {
			if !strings.Contains(script, fragment) {
				t.Fatalf("%s runtime script must require %q", name, fragment)
			}
		}
	}
	if !strings.Contains(accountCircuitRuntimeMutationLua, "dispatch_tombstone") || !strings.Contains(accountCircuitRuntimeMutationLua, "state['phase'] = 'SUSPECT'") {
		t.Fatal("runtime mutation must fence durable revision and handle SUSPECT explicitly")
	}
	if !strings.Contains(accountCircuitRuntimeListDueLua, "state['phase'] == 'OPEN' or state['phase'] == 'RECOVERING'") {
		t.Fatal("listDue must return canary-eligible phases only")
	}
}

func TestAccountCircuitRuntimeIndexBackfillRequiresLockEpochAndAudit(t *testing.T) {
	for _, fragment := range []string{"buildEpoch", "source changed during backfill", "status', 'ready", "ownerMode', 'go-runtime-state-v1"} {
		if !strings.Contains(beginAccountCircuitRuntimeIndexLua+applyAccountCircuitRuntimeIndexPageLua+finalizeAccountCircuitRuntimeIndexLua, fragment) {
			t.Fatalf("runtime index script missing %q", fragment)
		}
	}
	if !strings.Contains(accountCircuitRuntimeIndexOwnerMode, "go-runtime-state-v1") {
		t.Fatal("runtime index owner mode must be explicit")
	}
}

func TestAccountCircuitRuntimeLegacyRestoreCannotWriteReadyIndex(t *testing.T) {
	if !strings.Contains(restoreAccountCircuitIncidentLua, "legacy restore") || !strings.Contains(restoreAccountCircuitIncidentLua, "index_status") {
		t.Fatal("legacy incident restore must be fenced away from ready runtime owner")
	}
}

func TestKeyModelScopeRoundTripsWithCompleteIdentity(t *testing.T) {
	scope := GatewayAccountCircuitScope{
		Kind: GatewayAccountCircuitScopeKeyModel, AccountRuntimeKey: "acct",
		KeyFingerprint: "key-1", ClientModel: "gpt-4.1", CapabilityHash: "hash-1",
		CredentialSourceAccountID: "credential-acct", ClientEndpointFamily: "chat_completions",
		FinalUpstreamModel: "gpt-4.1-mini", UpstreamEndpointMode: "chat_sse",
	}
	key, err := GatewayAccountCircuitScopeKey(scope)
	if err != nil || key == "" {
		t.Fatalf("key-model scope key failed: %v %q", err, key)
	}
	if err := ValidateGatewayAccountCircuitScope(scope); err != nil {
		t.Fatalf("key-model scope rejected: %v", err)
	}
	for field, value := range map[string]string{
		"ClientModel": scope.ClientModel, "CapabilityHash": scope.CapabilityHash,
		"CredentialSourceAccountID": scope.CredentialSourceAccountID,
		"ClientEndpointFamily":      scope.ClientEndpointFamily, "FinalUpstreamModel": scope.FinalUpstreamModel,
		"UpstreamEndpointMode": scope.UpstreamEndpointMode,
	} {
		if !strings.Contains(key, value) {
			t.Fatalf("scope key omitted %s: %q", field, key)
		}
	}
	invalid := scope
	invalid.UpstreamEndpointMode = ""
	if _, err := GatewayAccountCircuitScopeKey(invalid); err == nil {
		t.Fatal("key-model scope accepted incomplete identity")
	}
}

func TestKeyModelScopeWirePreservesIdentity(t *testing.T) {
	scope := GatewayAccountCircuitScope{Kind: GatewayAccountCircuitScopeKeyModel, AccountRuntimeKey: "acct", KeyFingerprint: "key-1", ClientModel: "gpt-4.1", CapabilityHash: "capability-hash", CredentialSourceAccountID: "credential-acct", ClientEndpointFamily: "chat_completions", FinalUpstreamModel: "gpt-4.1-mini", UpstreamEndpointMode: "chat_sse"}
	state, err := GatewayAccountCircuitClosedState(scope, 3, 0, "transition", time.UnixMilli(100))
	if err != nil {
		t.Fatal(err)
	}
	wireValue, err := runtimeStateToWire(state)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(wireValue)
	if err != nil {
		t.Fatal(err)
	}
	var wire accountCircuitRuntimeStateWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	got, err := runtimeStateFromWire(wire)
	if err != nil {
		t.Fatal(err)
	}
	if got.Scope != scope {
		t.Fatalf("key-model wire identity drifted: got=%+v want=%+v", got.Scope, scope)
	}
}

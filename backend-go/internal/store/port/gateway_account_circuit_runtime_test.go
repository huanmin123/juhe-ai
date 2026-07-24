package port

import (
	"testing"
	"time"
)

func TestGatewayAccountCircuitScopeKeyMatchesNodeUTF8Encoding(t *testing.T) {
	scope := GatewayAccountCircuitScope{
		Kind: GatewayAccountCircuitScopeProtocolModel, AccountRuntimeKey: "账号-1:authorized:user:group:grant",
		ProtocolProfile: "openai", RequestLane: "text", ModelBucket: "gpt-测试",
	}
	key, err := GatewayAccountCircuitScopeKey(scope)
	if err != nil {
		t.Fatal(err)
	}
	want := "14:protocol_model|36:账号-1:authorized:user:group:grant|6:openai|4:text|10:gpt-测试"
	if key != want {
		t.Fatalf("scope key=%q, want %q", key, want)
	}
}

func TestGatewayAccountCircuitScopeRejectsAmbiguousFields(t *testing.T) {
	for _, scope := range []GatewayAccountCircuitScope{
		{Kind: GatewayAccountCircuitScopeAccount, AccountRuntimeKey: "account-1", KeyFingerprint: "leak"},
		{Kind: GatewayAccountCircuitScopeAPIKey, AccountRuntimeKey: "account-1", KeyFingerprint: "fingerprint", ModelBucket: "leak"},
		{Kind: GatewayAccountCircuitScopeProtocolModel, AccountRuntimeKey: " account-1", ProtocolProfile: "openai", RequestLane: "text", ModelBucket: "gpt"},
		{Kind: GatewayAccountCircuitScopeProtocolModel, AccountRuntimeKey: "account-1", ProtocolProfile: "openai", RequestLane: "audio", ModelBucket: "gpt"},
	} {
		if err := ValidateGatewayAccountCircuitScope(scope); err == nil {
			t.Fatalf("scope %+v unexpectedly accepted", scope)
		}
	}
}

func TestGatewayAccountCircuitRuntimeFamilyMatchingIsExact(t *testing.T) {
	if !GatewayAccountCircuitRuntimeKeyMatchesFamily("account-1", "account-1:authorized:user:group:grant") {
		t.Fatal("raw account must match its authorized runtime family")
	}
	if GatewayAccountCircuitRuntimeKeyMatchesFamily("account-1", "account-10:authorized:user:group:grant") || GatewayAccountCircuitRuntimeKeyMatchesFamily("account-1:authorized:user:group:grant", "account-1:authorized:other") {
		t.Fatal("runtime family match must not use a loose prefix")
	}
}

func TestGatewayAccountCircuitStateRequiresCanonicalRelationLists(t *testing.T) {
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	scope := GatewayAccountCircuitScope{Kind: GatewayAccountCircuitScopeAccount, AccountRuntimeKey: "account-1"}
	state, err := GatewayAccountCircuitClosedState(scope, 2, 1, "transition-1", now)
	if err != nil {
		t.Fatal(err)
	}
	state.ChildScopeKeys = []string{"z", "a"}
	if err := ValidateGatewayAccountCircuitState(state); err == nil {
		t.Fatal("unordered state relation list must fail closed")
	}
}

package circuitruntime

// w11c: contract validation matrix and Redis payload corruption branches.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func mustW11CJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func TestW11CContractScopeValidation(t *testing.T) {
	validRuntime := "w11c-acc"
	// Account scope rejects unrelated fields.
	if err := ValidateGatewayAccountCircuitScope(GatewayAccountCircuitScope{Kind: GatewayAccountCircuitScopeAccount, AccountRuntimeKey: validRuntime, KeyFingerprint: "x"}); err == nil {
		t.Fatalf("account scope with fingerprint must fail")
	}
	// Blank runtime key fails.
	if err := ValidateGatewayAccountCircuitScope(GatewayAccountCircuitScope{Kind: GatewayAccountCircuitScopeAccount}); err == nil {
		t.Fatalf("blank runtime key must fail")
	}
	// API key scope requires a valid fingerprint.
	if err := ValidateGatewayAccountCircuitScope(GatewayAccountCircuitScope{Kind: GatewayAccountCircuitScopeAPIKey, AccountRuntimeKey: validRuntime}); err == nil {
		t.Fatalf("missing fingerprint must fail")
	}
	if err := ValidateGatewayAccountCircuitScope(GatewayAccountCircuitScope{Kind: GatewayAccountCircuitScopeAPIKey, AccountRuntimeKey: validRuntime, KeyFingerprint: strings.Repeat("k", 600)}); err == nil {
		t.Fatalf("oversized fingerprint must fail")
	}
	if err := ValidateGatewayAccountCircuitScope(GatewayAccountCircuitScope{Kind: GatewayAccountCircuitScopeAPIKey, AccountRuntimeKey: validRuntime, KeyFingerprint: "fp", ProtocolProfile: "x"}); err == nil {
		t.Fatalf("key scope with protocol profile must fail")
	}
	// Protocol-model scope validation.
	if err := ValidateGatewayAccountCircuitScope(GatewayAccountCircuitScope{Kind: GatewayAccountCircuitScopeProtocolModel, AccountRuntimeKey: validRuntime, ProtocolProfile: strings.Repeat("p", 300)}); err == nil {
		t.Fatalf("oversized protocol profile must fail")
	}
	if err := ValidateGatewayAccountCircuitScope(GatewayAccountCircuitScope{Kind: GatewayAccountCircuitScopeProtocolModel, AccountRuntimeKey: validRuntime, ProtocolProfile: "openai", RequestLane: "bogus"}); err == nil {
		t.Fatalf("invalid lane must fail")
	}
	if err := ValidateGatewayAccountCircuitScope(GatewayAccountCircuitScope{Kind: GatewayAccountCircuitScopeProtocolModel, AccountRuntimeKey: validRuntime, ProtocolProfile: "openai", RequestLane: "text", ModelBucket: strings.Repeat("m", 600)}); err == nil {
		t.Fatalf("oversized model bucket must fail")
	}
	if err := ValidateGatewayAccountCircuitScope(GatewayAccountCircuitScope{Kind: GatewayAccountCircuitScopeProtocolModel, AccountRuntimeKey: validRuntime, ProtocolProfile: "openai", RequestLane: "text", KeyFingerprint: "x"}); err == nil {
		t.Fatalf("protocol scope with fingerprint must fail")
	}
	// Key-model scope validation.
	keyModel := GatewayAccountCircuitScope{Kind: GatewayAccountCircuitScopeKeyModel, AccountRuntimeKey: validRuntime, KeyFingerprint: strings.Repeat("f", 300)}
	if err := ValidateGatewayAccountCircuitScope(keyModel); err == nil {
		t.Fatalf("oversized key-model fingerprint must fail")
	}
	keyModel.KeyFingerprint = "fp"
	keyModel.ClientModel = strings.Repeat("c", 300)
	if err := ValidateGatewayAccountCircuitScope(keyModel); err == nil {
		t.Fatalf("oversized client model must fail")
	}
	keyModel.ClientModel = strings.Repeat("c", 300) + "-too-long"
	keyModel.CapabilityHash = "h"
	keyModel.ClientModel = "m"
	keyModel.CapabilityHash = "cap"
	keyModel.CredentialSourceAccountID = "src"
	keyModel.ClientEndpointFamily = "family"
	keyModel.FinalUpstreamModel = "final"
	keyModel.UpstreamEndpointMode = "mode"
	if err := ValidateGatewayAccountCircuitScope(keyModel); err != nil {
		t.Fatalf("valid key-model scope rejected: %v", err)
	}
	// Unknown kind.
	if err := ValidateGatewayAccountCircuitScope(GatewayAccountCircuitScope{Kind: "bogus", AccountRuntimeKey: validRuntime}); err == nil {
		t.Fatalf("unknown kind must fail")
	}
}

func w11cValidSuspectState(scope GatewayAccountCircuitScope) GatewayAccountCircuitState {
	return GatewayAccountCircuitState{
		Scope: scope, ScopeKey: mustGatewayAccountCircuitScopeKey(scope),
		Phase: GatewayAccountCircuitPhaseSuspect, Generation: 1, DispatchRevision: 1,
		TransitionID: "w11c-t", UpdatedAt: time.Now(),
	}
}

func TestW11CContractStateValidation(t *testing.T) {
	scope := w7bAccountScope("w11c-acc")
	base := w11cValidSuspectState(scope)
	if err := ValidateGatewayAccountCircuitState(base); err != nil {
		t.Fatalf("valid state rejected: %v", err)
	}
	// Scope key mismatch.
	mismatch := base
	mismatch.ScopeKey = "bogus"
	if err := ValidateGatewayAccountCircuitState(mismatch); err == nil {
		t.Fatalf("scope key mismatch must fail")
	}
	// Missing transition id on a non-closed phase.
	noTransition := base
	noTransition.TransitionID = ""
	if err := ValidateGatewayAccountCircuitState(noTransition); err == nil {
		t.Fatalf("missing transition must fail")
	}
	// Oversized transition id.
	big := base
	big.TransitionID = strings.Repeat("t", 300)
	if err := ValidateGatewayAccountCircuitState(big); err == nil {
		t.Fatalf("oversized transition must fail")
	}
	// Invalid half-open origin.
	badOrigin := base
	badOrigin.HalfOpenOrigin = "bogus"
	if err := ValidateGatewayAccountCircuitState(badOrigin); err == nil {
		t.Fatalf("invalid half-open origin must fail")
	}
	// Invalid lease id.
	withLease := base
	withLease.Lease = &GatewayAccountCircuitLease{ID: strings.Repeat("l", 300), Until: time.Now().Add(time.Minute)}
	if err := ValidateGatewayAccountCircuitState(withLease); err == nil {
		t.Fatalf("oversized lease id must fail")
	}
	withLease.Lease = &GatewayAccountCircuitLease{ID: "lease"}
	if err := ValidateGatewayAccountCircuitState(withLease); err == nil {
		t.Fatalf("zero lease until must fail")
	}
	// Unknown phase.
	badPhase := base
	badPhase.Phase = "bogus"
	if err := ValidateGatewayAccountCircuitState(badPhase); err == nil {
		t.Fatalf("unknown phase must fail")
	}
}

func TestW11CCorruptIndexSources(t *testing.T) {
	clock := newW7BClock()
	store, rt := w7bReadyStore(t, clock, 0, time.Minute)
	_ = rt
	ctx := context.Background()
	backfiller, err := NewAccountCircuitRuntimeIndexBackfiller(store.client)
	if err != nil {
		t.Fatal(err)
	}
	reader := DispatchRevisionReaderFunc(func(_ context.Context, _ GatewayAccountCircuitDispatchRevisionPageInput) (GatewayAccountCircuitDispatchRevisionPage, error) {
		return GatewayAccountCircuitDispatchRevisionPage{}, nil
	})
	// A corrupt runtimeScopes array entry fails the audit decode.
	scopeKey := mustGatewayAccountCircuitScopeKey(w7bAccountScope("w11c-corrupt"))
	entry := map[string]any{
		"state": map[string]any{
			"scopeKey": scopeKey,
			"scope":    map[string]any{"kind": "account", "accountRuntimeKey": "w11c-corrupt"},
			"phase":    "SUSPECT", "generation": 1, "dispatchRevision": 1, "transitionId": "w11c-t",
			"updatedAt": clock.Now().UnixMilli(),
		},
	}
	raw := mustW11CJSON(entry)
	if err := store.client.client.HSet(ctx, backfiller.keys.states, scopeKey, raw).Err(); err != nil {
		t.Fatal(err)
	}
	if err := store.client.client.HSet(ctx, backfiller.keys.runtimeScopes, "w11c-corrupt", `["dup","dup"]`).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := backfiller.WithDispatchRevisionReader(reader).BackfillGatewayAccountCircuitRuntimeIndex(ctx, GatewayAccountCircuitRuntimeIndexBackfillInput{OwnerID: "w11c-owner"}); err == nil {
		t.Fatalf("corrupt runtime scopes must fail the backfill")
	}
}

func TestW11CCorruptEscalationSource(t *testing.T) {
	clock := newW7BClock()
	store, rt := w7bReadyStore(t, clock, 0, time.Minute)
	_ = rt
	ctx := context.Background()
	backfiller, err := NewAccountCircuitRuntimeIndexBackfiller(store.client)
	if err != nil {
		t.Fatal(err)
	}
	reader := DispatchRevisionReaderFunc(func(_ context.Context, _ GatewayAccountCircuitDispatchRevisionPageInput) (GatewayAccountCircuitDispatchRevisionPage, error) {
		return GatewayAccountCircuitDispatchRevisionPage{}, nil
	})
	// A corrupt escalation entry fails the item parse.
	if err := store.client.client.HSet(ctx, backfiller.keys.escalation, "w11c-bad-escalation", "{nope").Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := backfiller.WithDispatchRevisionReader(reader).BackfillGatewayAccountCircuitRuntimeIndex(ctx, GatewayAccountCircuitRuntimeIndexBackfillInput{OwnerID: "w11c-owner"}); err == nil {
		t.Fatalf("corrupt escalation must fail the backfill")
	}
}

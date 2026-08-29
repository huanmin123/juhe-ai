package circuitprojector

import (
	"context"
	"testing"
	"time"

	control "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/circuit_control_plane"
	runtime "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/circuit_runtime"
)

func TestConvertIncidentPreservesRuntimeDeadlines(t *testing.T) {
	open := int64(100)
	retry := int64(200)
	lease := int64(300)
	retained := int64(400)
	id := "incident-1"
	leaseID := "lease-1"
	purpose := "half_open"
	v := control.Incident{CircuitScopeKey: "scope", AccountID: "a1", AccountRuntimeKey: "a1", ScopeKind: "account", IncidentID: &id, State: "OPEN", Generation: 1, DispatchRevision: 2, LedgerRevision: 3, TransitionID: "transition", OpenUntilMS: &open, NextTransitionAtMS: &retry, LeaseID: &leaseID, LeasePurpose: &purpose, LeaseUntilMS: &lease, RetainedUntilMS: &retained, UpdatedAtMS: 500}
	got, err := convertIncident(v)
	if err != nil {
		t.Fatal(err)
	}
	if got.OpenUntil == nil || !got.OpenUntil.Equal(time.UnixMilli(open).UTC()) || got.LeaseUntil == nil || got.RetainedUntil == nil || got.LeaseID != leaseID || got.LeasePurpose != purpose {
		t.Fatalf("converted incident lost deadlines: %+v", got)
	}
}

func TestConvertIncidentPreservesKeyModelIdentity(t *testing.T) {
	id := "incident-key-model"
	clientModel := "gpt-4.1"
	capabilityHash := "capability-hash"
	credentialSource := "credential-acct"
	clientFamily := "chat_completions"
	finalModel := "gpt-4.1-mini"
	endpointMode := "chat_sse"
	v := control.Incident{CircuitScopeKey: "scope", AccountID: "a1", AccountRuntimeKey: "a1", ScopeKind: "key_model", KeyFingerprint: strptr("key-1"), ClientModel: &clientModel, CapabilityHash: &capabilityHash, CredentialSourceAccountID: &credentialSource, ClientEndpointFamily: &clientFamily, FinalUpstreamModel: &finalModel, UpstreamEndpointMode: &endpointMode, IncidentID: &id, State: "OPEN", Generation: 1, DispatchRevision: 2, LedgerRevision: 3, TransitionID: "transition", UpdatedAtMS: 500}
	got, err := convertIncident(v)
	if err != nil {
		t.Fatal(err)
	}
	if got.ScopeKind != "key_model" || got.KeyFingerprint != "key-1" || got.ClientModel != clientModel || got.CapabilityHash != capabilityHash || got.CredentialSourceAccountID != credentialSource || got.ClientEndpointFamily != clientFamily || got.FinalUpstreamModel != finalModel || got.UpstreamEndpointMode != endpointMode {
		t.Fatalf("converted incident lost key-model identity: %+v", got)
	}
}

func strptr(value string) *string { return &value }

func TestDispatchRevisionReaderRequiresStore(t *testing.T) {
	_, err := (DispatchRevisionReader{}).ListGatewayAccountCircuitDispatchRevisions(context.Background(), runtime.GatewayAccountCircuitDispatchRevisionPageInput{Limit: 1})
	if err == nil {
		t.Fatal("nil dispatch revision reader store unexpectedly succeeded")
	}
}

package keymodelruntime

import (
	"testing"
	"time"
)

func testCapability() Capability {
	return Capability{CredentialSourceAccountID: "source-1", KeyFingerprint: "key-1", ClientModel: "model-a", ClientEndpointFamily: "chat_completions", FinalUpstreamModel: "model-up", UpstreamEndpointMode: "chat_json", DispatchRevision: 7}
}

func TestRecoveryMatchesNodeSemantics(t *testing.T) {
	hash, err := HashCapability(testCapability())
	if err != nil || hash != "27d95965a9e5eb856fab29cdf92e308ad4326fb5a73dcf52314b1ce9bb55aa97" {
		t.Fatalf("capability hash drifted from Node canonical contract: %q %v", hash, err)
	}
	now := time.UnixMilli(1000).UTC()
	state, err := Open(testCapability(), now)
	if err != nil {
		t.Fatal(err)
	}
	if state.RetryAt.Sub(now) != 5*time.Second || state.Phase != PhaseOpen {
		t.Fatalf("unexpected open state: %+v", state)
	}
	status, state := AcquireRecoveryLease(state, 1, 7, "lease-1", state.RetryAt)
	if status != StatusApplied {
		t.Fatalf("acquire status=%s", status)
	}
	status, state = SettleRecovery(state, 1, 7, "lease-1", OutcomeCompleteSuccess, state.ProbeLease.Until.Add(-time.Second))
	if status != StatusApplied || state.Phase != PhaseRecovering || state.RecoverySuccessCount != 1 {
		t.Fatalf("unexpected first recovery: %s %+v", status, state)
	}
	status, _ = SettleRecovery(state, 1, 7, "wrong", OutcomeUnknown, now)
	if status != StatusLeaseMismatch {
		t.Fatalf("wrong lease status=%s", status)
	}
}

func TestMemoryForegroundAdmissionIsAtomic(t *testing.T) {
	store := NewMemoryStore()
	capability := testCapability()
	now := time.UnixMilli(1000).UTC()
	decisions := make(chan ForegroundDecision, 3)
	for i := 0; i < 3; i++ {
		go func(index int) {
			decision, _, _, err := store.AdmitForeground(capability, string(rune('a'+index)), now)
			if err != nil {
				t.Errorf("admit: %v", err)
				return
			}
			decisions <- decision
		}(i)
	}
	count := 0
	for i := 0; i < 3; i++ {
		if <-decisions == ForegroundAdmitted {
			count++
		}
	}
	if count != 3 {
		t.Fatalf("admitted=%d want all attempts admitted", count)
	}
}

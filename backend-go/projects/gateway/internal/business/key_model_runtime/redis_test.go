package keymodelruntime

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
)

func TestRedisStoreAdmissionAndFailureAreAtomic(t *testing.T) {
	server := miniredis.RunT(t)
	store, err := NewRedisStore("redis://"+server.Addr(), "juhe-ai:test", OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	capability := testCapability()
	now := time.UnixMilli(1_000).UTC()
	decision, permit, _, err := store.AdmitForeground(ctx, capability, "attempt-1")
	if err != nil || decision != ForegroundAdmitted {
		t.Fatalf("admit: %s %v", decision, err)
	}
	released, err := store.ReleaseForeground(ctx, permit)
	if err != nil || !released {
		t.Fatal("release did not remove permit")
	}
	status, state, err := store.RecordFailure(ctx, capability, now, "receipt-1")
	if err != nil || status != StatusApplied || state.Phase != PhaseOpen {
		t.Fatalf("record failure: %s %+v %v", status, state, err)
	}
	decision, _, _, err = store.AdmitForeground(ctx, capability, "attempt-2")
	if err != nil || decision != ForegroundBlocked {
		t.Fatalf("blocked admission: %s %v", decision, err)
	}
}

func TestRedisStoreUsesNodeNamespaceAndReleasesMainProbePermit(t *testing.T) {
	server := miniredis.RunT(t)
	store, err := NewRedisStore("redis://"+server.Addr(), "interop-test", OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if got, want := store.prefix, "juhe-ai:interop-test:gateway-account-circuit-key-model"; got != want {
		t.Fatalf("Redis prefix = %q, want %q", got, want)
	}
	ctx := context.Background()
	capability := testCapability()
	decision, permit, _, err := store.AdmitForeground(ctx, capability, "main-probe-attempt")
	if err != nil || decision != ForegroundAdmitted {
		t.Fatalf("admit main probe: %s %v", decision, err)
	}
	if err := store.RecordMainProbeFence(ctx, capability, permit.AttemptID, time.Minute); err != nil {
		t.Fatalf("record main probe fence: %v", err)
	}
	if exists, err := store.client.Exists(ctx, store.admissionLeaseKey(permit.CapabilityHash, permit.AttemptID)).Result(); err != nil || exists != 0 {
		t.Fatalf("main probe permit was not released: exists=%d err=%v", exists, err)
	}
	if exists, err := store.client.Exists(ctx, store.key("mainProbeFence", permit.CapabilityHash)).Result(); err != nil || exists != 1 {
		t.Fatalf("main probe fence was not written: exists=%d err=%v", exists, err)
	}
}

func TestRedisStoreFailureIntentReleasesActualPermit(t *testing.T) {
	server := miniredis.RunT(t)
	store, err := NewRedisStore("redis://"+server.Addr(), "interop-test", OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	capability := testCapability()
	decision, permit, _, err := store.AdmitForeground(ctx, capability, "actual-attempt")
	if err != nil || decision != ForegroundAdmitted {
		t.Fatalf("admit: %s %v", decision, err)
	}
	status, _, err := store.RecordFailureIntent(ctx, FailureIntent{
		IntentID: "failure-receipt", RequestID: "request-1", AttemptID: "actual-attempt",
		Capability: capability, ObservedAt: time.Now().UTC(), Permit: &permit,
	})
	if err != nil || status != StatusApplied {
		t.Fatalf("record failure intent: %s %v", status, err)
	}
	if exists, err := store.client.Exists(ctx, store.admissionLeaseKey(permit.CapabilityHash, permit.AttemptID)).Result(); err != nil || exists != 0 {
		t.Fatalf("failure intent left permit lease: exists=%d err=%v", exists, err)
	}
	if wake, err := store.client.Get(ctx, store.wakeKey(permit.CapabilityHash)).Int64(); err != nil || wake != 1 {
		t.Fatalf("failure intent did not publish wake sequence: wake=%d err=%v", wake, err)
	}
}

func TestRedisStoreFailsClosedBeforeOwnerHandoff(t *testing.T) {
	server := miniredis.RunT(t)
	store, err := NewRedisStore("redis://"+server.Addr(), "juhe-ai:test", OwnerGate{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Ping(context.Background()); err == nil {
		t.Fatal("key-model Redis owner ping unexpectedly succeeded before handoff")
	}
}

func TestRedisStoreRecoveryLeaseAndCommit(t *testing.T) {
	server := miniredis.RunT(t)
	store, err := NewRedisStore("redis://"+server.Addr(), "juhe-ai:test", OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	capability := testCapability()
	status, state, err := store.RecordFailure(ctx, capability, time.Now().UTC(), "receipt-recovery")
	if err != nil || status != StatusApplied {
		t.Fatalf("record failure: %s %v", status, err)
	}
	state.RetryAt = time.UnixMilli(1).UTC()
	encoded, err := encodeRedisState(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.client.Set(ctx, store.stateKey(state.CapabilityHash), encoded, 0).Err(); err != nil {
		t.Fatal(err)
	}
	if err := store.client.ZAdd(ctx, store.key("due"), redis.Z{Score: 1, Member: state.CapabilityHash}).Err(); err != nil {
		t.Fatal(err)
	}
	state, status, err = store.AcquireRecovery(ctx, state, "lease-recovery", false, false)
	if err != nil || status != StatusApplied || state.Phase != PhaseHalfOpen {
		t.Fatalf("acquire recovery: %s %+v %v", status, state, err)
	}
	now := time.Now().UTC()
	status, next := SettleRecovery(state, state.Generation, state.DispatchRevision, "lease-recovery", OutcomeCompleteSuccess, now)
	if status != StatusApplied {
		t.Fatalf("settle recovery: %s", status)
	}
	if committed, err := store.CommitRecovery(ctx, state, next, "lease-recovery"); err != nil || committed != StatusApplied {
		t.Fatalf("commit recovery: %s %v", committed, err)
	}
}

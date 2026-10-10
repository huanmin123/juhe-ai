// Coverage-table behavior test (网关模型列表账户并集设计 6.3 "写路径 → 失效覆盖
// 依据"): every committed account-cleanup write that actually changed
// union-cache dependency rows must clear the gateway runtime cache through the
// K5 bus port after its transaction commits — a changed arm publishes exactly
// the runtime + authorization-quota pair with account_cleanup_applied (the
// business/authorization ExpireDue two-topic shape; both arms touch
// grant/authorization rows), a no-change pass and a rolled-back (CAS-stale)
// candidate publish nothing, and an unwired (nil) port keeps the cleanup
// behavior unchanged.
package accountcleanup

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/inval"
)

// recordingInvalidator captures the bus publishes as "topic reason" pairs.
type recordingInvalidator struct {
	calls []string
}

func (r *recordingInvalidator) Invalidate(topic, reason string) {
	r.calls = append(r.calls, topic+" "+reason)
}

func TestCleanupFiresRuntimeInvalidationAfterCommit(t *testing.T) {
	store, db := cleanupTestStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	bus := &recordingInvalidator{}
	store.AttachWriteInvalidator(bus)

	// A no-change pass (empty business tables) commits no state change and
	// publishes nothing, so a periodic driver never clears caches on an empty
	// tick.
	if _, err := store.Cleanup(context.Background(), CleanupInput{CutoffDeletedAt: "2026-02-01T00:00:00.000Z"}); err != nil {
		t.Fatal(err)
	}
	if len(bus.calls) != 0 {
		t.Fatalf("no-change pass published %v", bus.calls)
	}

	// Orphan-instance tombstone arm (the same seed as
	// TestCleanupSoftDeletesOrphanWithCAS): one committed soft delete, one
	// runtime + quota publish pair.
	if _, err := db.Exec(`INSERT INTO accounts
		(id,system_account_id,authorization_instance_authorization_id,authorization_instance_source_account_id,updated_at,created_at,status,schedulable)
		VALUES ('orphan','sys','missing-auth',NULL,'2026-08-01T00:00:00.000Z','2026-08-01T00:00:00.000Z','active',1)`); err != nil {
		t.Fatal(err)
	}
	result, err := store.Cleanup(context.Background(), CleanupInput{CutoffDeletedAt: "2026-02-01T00:00:00.000Z"})
	if err != nil || result.OrphanedAuthorizationInstances != 1 || result.Attempted != 0 {
		t.Fatalf("unexpected orphan result=%+v error=%v", result, err)
	}
	want := []string{
		inval.TopicGatewayRuntime + " " + invalidationReasonCleanupApplied,
		inval.TopicAuthorizationQuota + " " + invalidationReasonCleanupApplied,
	}
	if len(bus.calls) != len(want) {
		t.Fatalf("bus calls = %v, want %v", bus.calls, want)
	}
	for i := range want {
		if bus.calls[i] != want[i] {
			t.Fatalf("bus call[%d] = %q, want %q", i, bus.calls[i], want[i])
		}
	}
}

func TestPhysicalDeleteFiresRuntimeInvalidationAfterCommit(t *testing.T) {
	store, db := cleanupTestStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	seedDeletedAccountTree(t, db)
	bus := &recordingInvalidator{}
	store.AttachWriteInvalidator(bus)
	cleared := RecordFenceReaderFunc(func(context.Context, CleanupTarget) (RecordFence, error) {
		return RecordFence{Status: RecordFenceCleared, Token: "fence-1"}, nil
	})

	// Physical-delete arm (the same seed as
	// TestCleanupDeletesBusinessRowsAfterClearedFenceAndReplays): one
	// committed business delete, one runtime + quota publish pair.
	result, err := store.Cleanup(context.Background(), CleanupInput{CutoffDeletedAt: "2026-02-01T00:00:00.000Z", RecordFence: cleared})
	if err != nil || result.Completed != 1 || result.PhysicallyDeletedAccounts != 2 {
		t.Fatalf("unexpected delete result=%+v error=%v", result, err)
	}
	want := []string{
		inval.TopicGatewayRuntime + " " + invalidationReasonCleanupApplied,
		inval.TopicAuthorizationQuota + " " + invalidationReasonCleanupApplied,
	}
	if len(bus.calls) != len(want) {
		t.Fatalf("bus calls = %v, want %v", bus.calls, want)
	}
	for i := range want {
		if bus.calls[i] != want[i] {
			t.Fatalf("bus call[%d] = %q, want %q", i, bus.calls[i], want[i])
		}
	}

	// A CAS-stale candidate rolls back before the commit and publishes
	// nothing (the same seed trick as TestCleanupRejectsStaleBusinessCAS).
	if _, err := db.Exec(`INSERT INTO accounts
		(id,system_account_id,authorization_instance_authorization_id,authorization_instance_source_account_id,deleted_at,updated_at,created_at,status,schedulable)
		VALUES ('late','sys',NULL,NULL,'2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z','disabled',0)`); err != nil {
		t.Fatal(err)
	}
	staleFence := RecordFenceReaderFunc(func(_ context.Context, target CleanupTarget) (RecordFence, error) {
		if target.AccountID != "late" {
			t.Fatalf("fence received wrong target=%+v", target)
		}
		if _, updateErr := db.Exec(`UPDATE accounts SET updated_at='2026-01-02T00:00:00.000Z' WHERE id='late'`); updateErr != nil {
			t.Fatalf("advance account version: %v", updateErr)
		}
		return RecordFence{Status: RecordFenceCleared, Token: "fence-2"}, nil
	})
	result, err = store.Cleanup(context.Background(), CleanupInput{CutoffDeletedAt: "2026-02-01T00:00:00.000Z", RecordFence: staleFence})
	if err != nil || result.Failed != 1 || len(result.Failures) != 1 {
		t.Fatalf("unexpected stale CAS result=%+v error=%v", result, err)
	}
	if len(bus.calls) != len(want) {
		t.Fatalf("rolled-back cleanup published %v", bus.calls)
	}
}

func TestCleanupWithoutInvalidatorKeepsCleanupBehavior(t *testing.T) {
	store, db := cleanupTestStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	seedDeletedAccountTree(t, db)
	// Nil port: the committed cleanup must run unchanged (invalidation off).
	fence := RecordFenceReaderFunc(func(context.Context, CleanupTarget) (RecordFence, error) {
		return RecordFence{Status: RecordFenceCleared, Token: "fence-1"}, nil
	})
	result, err := store.Cleanup(context.Background(), CleanupInput{CutoffDeletedAt: "2026-02-01T00:00:00.000Z", RecordFence: fence})
	if err != nil || result.Completed != 1 || result.PhysicallyDeletedAccounts != 2 {
		t.Fatalf("unexpected delete result=%+v error=%v", result, err)
	}
}

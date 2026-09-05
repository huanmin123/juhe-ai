package accounts

import (
	"context"
	"sync"
	"testing"
)

// recordingInvalidator captures the post-commit invalidation channels so the
// write-path tests can assert topic + reason parity with the Node archive.
type recordingInvalidator struct {
	mu             sync.Mutex
	lookups        []string
	runtimeReasons []string
}

func (r *recordingInvalidator) InvalidateAccountLookup(accountID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lookups = append(r.lookups, accountID)
	return nil
}

func (r *recordingInvalidator) InvalidateGatewayRuntime(reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runtimeReasons = append(r.runtimeReasons, reason)
	return nil
}

func (r *recordingInvalidator) snapshot() (lookups []string, reasons []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.lookups...), append([]string{}, r.runtimeReasons...)
}

func stringPtr(value string) *string { return &value }

// TestPatchInvalidationChannels mirrors the Node patch post-commit condition
// (account-management-patch.repository.ts:1877-1896): gateway-affecting field
// changes notify the runtime topic with 'account_management_patch', name /
// expiry / tags changes flush the per-account lookup, no-change patches stay
// silent.
func TestPatchInvalidationChannels(t *testing.T) {
	env := newTestEnv(t)
	invalidator := &recordingInvalidator{}
	env.store.SetCacheInvalidator(invalidator)
	env.seedAccount(t, "acc-inv-1", "owner-1", "alpha", "active")
	ctx := context.Background()
	scope := AccessScope{ViewerID: "owner-1"}

	// Status-only change: runtime channel only (status is a gatewayField,
	// not a lookup field).
	if _, err := env.store.Patch(ctx, "acc-inv-1", PatchInput{
		ExpectedConfigRevision: 1,
		Status:                 stringPtr("disabled"),
	}, scope); err != nil {
		t.Fatal(err)
	}
	lookups, reasons := invalidator.snapshot()
	if len(lookups) != 0 {
		t.Fatalf("status change must not flush the lookup channel: %v", lookups)
	}
	if len(reasons) != 1 || reasons[0] != "account_management_patch" {
		t.Fatalf("runtime reasons = %v, want [account_management_patch]", reasons)
	}

	// Name-only change: lookup channel only (Node accountLookupAffected).
	if _, err := env.store.Patch(ctx, "acc-inv-1", PatchInput{
		ExpectedConfigRevision: 2,
		Name:                   stringPtr("alpha-renamed"),
	}, scope); err != nil {
		t.Fatal(err)
	}
	lookups, reasons = invalidator.snapshot()
	if len(lookups) != 1 || lookups[0] != "acc-inv-1" {
		t.Fatalf("lookup flushes = %v, want [acc-inv-1]", lookups)
	}
	if len(reasons) != 1 {
		t.Fatalf("name change must not notify the runtime topic: %v", reasons)
	}

	// No-op patch (no field change): cache silent.
	if _, err := env.store.Patch(ctx, "acc-inv-1", PatchInput{
		ExpectedConfigRevision: 3,
		Name:                   stringPtr("alpha-renamed"),
	}, scope); err != nil {
		t.Fatal(err)
	}
	lookups, reasons = invalidator.snapshot()
	if len(lookups) != 1 || len(reasons) != 1 {
		t.Fatalf("no-change patch leaked invalidations: lookups=%v reasons=%v", lookups, reasons)
	}
}

// TestDeleteInvalidationChannels mirrors the Node owner-mode delete tail
// (account-delete-cleanup.repository.ts:197-201): one lookup flush per soft
// deleted account (parent + authorization instances) plus one whole-surface
// runtime invalidation with the 'account_deleted' reason.
func TestDeleteInvalidationChannels(t *testing.T) {
	env := newTestEnv(t)
	invalidator := &recordingInvalidator{}
	env.store.SetCacheInvalidator(invalidator)
	env.seedAccount(t, "acc-inv-2", "owner-1", "parent", "active")
	env.seedAccount(t, "acc-inv-3", "owner-2", "instance", "active")
	env.exec(t, `UPDATE accounts SET authorization_instance_source_account_id = 'acc-inv-2' WHERE id = 'acc-inv-3'`)

	deleted, err := env.store.Delete(context.Background(), "acc-inv-2", AccessScope{ViewerID: "owner-1", IsAdmin: true})
	if err != nil || !deleted {
		t.Fatalf("delete: %v %v", deleted, err)
	}
	lookups, reasons := invalidator.snapshot()
	if len(lookups) != 2 || lookups[0] != "acc-inv-2" || lookups[1] != "acc-inv-3" {
		t.Fatalf("lookup flushes = %v, want parent + instance", lookups)
	}
	if len(reasons) != 1 || reasons[0] != "account_deleted" {
		t.Fatalf("runtime reasons = %v, want [account_deleted]", reasons)
	}
}

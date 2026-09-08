// Post-commit invalidation fan-out tests (BUG-0175 D-56/D-65/D-115): mock
// ports assert that every committed business write fires exactly the archived
// whole-surface set — refreshAfterResourceAuthorizationBusinessWrite
// (write.repository.ts:2706-2731) for create/patch/revoke with reasons
// resource_authorization_created/updated/revoked, and
// refreshAfterResourceAuthorizationReturnedWrite
// (return.repository.ts:791-800) with resource_authorization_returned for the
// direct-grant return and the group return. The third archived return path
// (the account instance return, return.repository.ts:242-291) delegates its
// terminal write to Store.Return through the accounts
// AuthorizationGrantReturner port, so the Store.Return assertions here carry
// that route too.
//
// The mocked set per committed write (Node six-piece → Go channels, see
// invalidation.go): one group-stats MarkAll with the reason, then the bus
// publishes the reason on the API-key validation, gateway runtime (which
// folds invalidateGroupAccountIdsCache) and authorization quota topics. The
// no-write outcomes (idempotent re-create, unchanged, not_found, conflict)
// fire nothing — the archive returns before its refresh tail. A failing
// stats marker never fails the committed write and the remaining bus arms
// still run.
package authz

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/inval"
)

// recordingStatsDirty captures the group-stats dirty arm (piece 1).
type recordingStatsDirty struct {
	reasons []string
	err     error
}

func (r *recordingStatsDirty) MarkAllGroupAccountStatsDirty(_ context.Context, reason string) error {
	r.reasons = append(r.reasons, reason)
	return r.err
}

// recordingBus captures the bus publishes (pieces 2/3+5/6) as
// "topic reason" pairs.
type recordingBus struct {
	calls []string
}

func (r *recordingBus) Invalidate(topic, reason string) {
	r.calls = append(r.calls, topic+" "+reason)
}

func busSet(reason string) []string {
	return []string{
		inval.TopicGatewayAPIKeyValidation + " " + reason,
		inval.TopicGatewayRuntime + " " + reason,
		inval.TopicAuthorizationQuota + " " + reason,
	}
}

// assertOneFanout asserts that exactly one six-piece set with the given
// reason was appended since the baseline counts.
func assertOneFanout(t *testing.T, stats *recordingStatsDirty, bus *recordingBus,
	statsBaseline, busBaseline int, reason string) {
	t.Helper()
	if got := len(stats.reasons); got != statsBaseline+1 {
		t.Fatalf("stats dirty calls = %d, want %d (reasons so far: %v)", got, statsBaseline+1, stats.reasons)
	}
	if got := stats.reasons[len(stats.reasons)-1]; got != reason {
		t.Fatalf("stats dirty reason = %q, want %q", got, reason)
	}
	assertBusFanout(t, bus, busBaseline, reason)
}

func assertBusFanout(t *testing.T, bus *recordingBus, busBaseline int, reason string) {
	t.Helper()
	want := busSet(reason)
	if got := len(bus.calls); got != busBaseline+len(want) {
		t.Fatalf("bus calls = %d, want %d (calls so far: %v)", got, busBaseline+len(want), bus.calls)
	}
	for i, expected := range want {
		if got := bus.calls[busBaseline+i]; got != expected {
			t.Fatalf("bus call[%d] = %q, want %q", busBaseline+i, got, expected)
		}
	}
}

func assertNoFanout(t *testing.T, stats *recordingStatsDirty, bus *recordingBus, statsBaseline, busBaseline int) {
	t.Helper()
	if len(stats.reasons) != statsBaseline || len(bus.calls) != busBaseline {
		t.Fatalf("unexpected fan-out: stats %v bus %v", stats.reasons, bus.calls)
	}
}

// TestWritePathsFireInvalidationFanout walks grant A through create →
// idempotent re-create → patch → no-op patch → not_found patch → conflict
// revoke → revoke → unchanged revoke, and grant B through create → return →
// unchanged return, pinning the fan-out set of each committed write against
// the archive reasons.
func TestWritePathsFireInvalidationFanout(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedAccount(t, "grantee2", "active")
	f.seedGroup(t, "grp_1", "owner")
	stats := &recordingStatsDirty{}
	bus := &recordingBus{}
	f.store.AttachWriteInvalidator(stats, bus)

	// Grant A: create fires resource_authorization_created.
	created, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_1",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	assertOneFanout(t, stats, bus, 0, 0, invalidationReasonCreated)

	// The identical re-create is idempotent (created=false) and the archive
	// skips its refresh (write.repository.ts:343-345 condition).
	if _, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_1",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner"); err != nil {
		t.Fatal(err)
	}
	assertNoFanout(t, stats, bus, 1, 3)

	// Patch with a real change fires resource_authorization_updated.
	paused := StatusPaused
	patched, err := f.store.Patch(context.Background(), created.Item.ID, PatchInput{Status: &paused},
		created.Item.UpdatedAt, "owner")
	if err != nil || patched.Status != "updated" {
		t.Fatalf("patch: %+v %v", patched, err)
	}
	assertOneFanout(t, stats, bus, 1, 3, invalidationReasonUpdated)

	// The no-op patch lands unchanged and never refreshes (Node :801).
	if _, err := f.store.Patch(context.Background(), created.Item.ID, PatchInput{Status: &paused},
		patched.Result.UpdatedAt, "owner"); err != nil {
		t.Fatal(err)
	}
	assertNoFanout(t, stats, bus, 2, 6)

	// not_found and conflict never refresh.
	if outcome, err := f.store.Patch(context.Background(), "rauthgrant_missing", PatchInput{Status: &paused},
		patched.Result.UpdatedAt, "owner"); err != nil || outcome.Status != "not_found" {
		t.Fatalf("missing patch: %+v %v", outcome, err)
	}
	if outcome, err := f.store.Revoke(context.Background(), created.Item.ID, "bogus-version", "owner"); err != nil ||
		outcome.Status != "conflict" {
		t.Fatalf("conflict revoke: %+v %v", outcome, err)
	}
	assertNoFanout(t, stats, bus, 2, 6)

	// Revoke fires resource_authorization_revoked; the second revoke is
	// unchanged and stays silent.
	if _, err := f.store.Revoke(context.Background(), created.Item.ID, patched.Result.UpdatedAt, "owner"); err != nil {
		t.Fatal(err)
	}
	assertOneFanout(t, stats, bus, 2, 6, invalidationReasonRevoked)
	if _, err := f.store.Revoke(context.Background(), created.Item.ID, patched.Result.UpdatedAt, "owner"); err != nil {
		t.Fatal(err)
	}
	assertNoFanout(t, stats, bus, 3, 9)

	// Grant B: return fires resource_authorization_returned (this write point
	// also carries the accounts instance-return route through its port); the
	// second return is unchanged and stays silent.
	grantB, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_1",
		GranteeType: "system_account", GranteeID: "grantee2",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	assertOneFanout(t, stats, bus, 3, 9, invalidationReasonCreated)

	returned, err := f.store.Return(context.Background(), grantB.Item.ID, grantB.Item.UpdatedAt, "grantee2")
	if err != nil || returned.Status != "updated" {
		t.Fatalf("return: %+v %v", returned, err)
	}
	assertOneFanout(t, stats, bus, 4, 12, invalidationReasonReturned)
	if outcome, err := f.store.Return(context.Background(), grantB.Item.ID, returned.Result.UpdatedAt, "grantee2"); err != nil ||
		outcome.Status != "unchanged" {
		t.Fatalf("unchanged return: %+v %v", outcome, err)
	}
	assertNoFanout(t, stats, bus, 5, 15)
}

// TestReturnGroupFiresReturnedFanout pins the group return write point the
// groups return-authorization route mounts through
// (groups/routes.go mountGuardedReturnAuthorization → ReturnGroupForGrantee).
func TestReturnGroupFiresReturnedFanout(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedGroup(t, "grp_1", "owner")
	stats := &recordingStatsDirty{}
	bus := &recordingBus{}
	f.store.AttachWriteInvalidator(stats, bus)

	if _, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_1",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner"); err != nil {
		t.Fatal(err)
	}
	assertOneFanout(t, stats, bus, 0, 0, invalidationReasonCreated)

	// The unreturnable probes (owner self, unknown group) never refresh.
	if _, err := f.store.ReturnGroupForGrantee(context.Background(), "grp_1", "owner", "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ReturnGroupForGrantee(context.Background(), "grp_missing", "grantee", "grantee"); err != nil {
		t.Fatal(err)
	}
	assertNoFanout(t, stats, bus, 1, 3)

	if _, err := f.store.ReturnGroupForGrantee(context.Background(), "grp_1", "grantee", "actor_admin"); err != nil {
		t.Fatal(err)
	}
	assertOneFanout(t, stats, bus, 1, 3, invalidationReasonReturned)

	// The second return resolves no returnable grant and stays silent.
	if _, err := f.store.ReturnGroupForGrantee(context.Background(), "grp_1", "grantee", "actor_admin"); err != nil {
		t.Fatal(err)
	}
	assertNoFanout(t, stats, bus, 2, 6)
}

// TestUnwiredInvalidationStaysSilent pins the nil-tolerance contract: stores
// built through plain NewStore (every pre-existing fixture and partial
// composition) keep their behavior with no fan-out wiring.
func TestUnwiredInvalidationStaysSilent(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedGroup(t, "grp_1", "owner")

	created, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_1",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Revoke(context.Background(), created.Item.ID, created.Item.UpdatedAt, "owner"); err != nil {
		t.Fatal(err)
	}
}

// TestInvalidationPartialFailureDoesNotFailWrite pins the best-effort
// contract: a failing group-stats marker neither fails the committed write
// nor blocks the bus arms (the accounts finishXxxSideEffects precedent —
// partial failure never rolls the main write back).
func TestInvalidationPartialFailureDoesNotFailWrite(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedGroup(t, "grp_1", "owner")
	stats := &recordingStatsDirty{err: context.DeadlineExceeded}
	bus := &recordingBus{}
	f.store.AttachWriteInvalidator(stats, bus)

	if _, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_1",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner"); err != nil {
		t.Fatalf("committed write must not fail on the stats marker: %v", err)
	}
	if len(stats.reasons) != 1 {
		t.Fatalf("stats attempts = %d, want 1", len(stats.reasons))
	}
	assertBusFanout(t, bus, 0, invalidationReasonCreated)
}

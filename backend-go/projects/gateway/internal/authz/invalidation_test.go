// Post-commit invalidation fan-out tests (BUG-0175 D-56/D-65/D-115): mock
// ports assert that every committed business write fires exactly the archived
// whole-surface set keyed by reason (create/claim/pause/extend/expire/revoke/
// decline/return). The authorization-quota bus topic retired with the quota
// domain (revision 31): the served set per committed write is the group-stats
// MarkAll plus the API-key validation and gateway runtime topics.
//
// The mocked set per committed write (Node six-piece → Go channels, see
// invalidation.go): one group-stats MarkAll with the reason, then the bus
// publishes the reason on the API-key validation and gateway runtime topics
// (the group-account-ids cache folds into the runtime topic; piece 4 is the
// documented no-op; piece 6 retired). The no-write outcomes (idempotent
// re-offer, unchanged, not_found, conflict) fire nothing — the store returns
// before its refresh tail. A failing stats marker never fails the committed
// write and the remaining bus arms still run.
package authz

import (
	"context"
	"testing"
	"time"

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

// recordingBus captures the bus publishes as "topic reason" pairs.
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
	}
}

// assertOneFanout asserts that exactly one set with the given reason was
// appended since the baseline counts.
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

// TestWritePathsFireInvalidationFanout walks grant A through offer →
// idempotent re-offer → claim → pause → resume → revoke → idempotent revoke,
// grant B through offer → decline, and grant C through offer → claim →
// return, pinning the fan-out set of each committed write.
func TestWritePathsFireInvalidationFanout(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedAccount(t, "grantee2", "active")
	f.seedGroup(t, "grp_1", "owner")
	stats := &recordingStatsDirty{}
	bus := &recordingBus{}
	f.store.AttachWriteInvalidator(stats, bus)
	nowText := f.now.UTC().Format(time.RFC3339Nano)

	// Grant A: the offer fires resource_authorization_created.
	created, err := f.store.CreateOffer(context.Background(), OfferInput{
		ResourceType: "group", ResourceID: "grp_1",
		GranteeType: "system_account", GranteeUsername: "grantee",
	}, "owner", false, "owner")
	if err != nil {
		t.Fatal(err)
	}
	assertOneFanout(t, stats, bus, 0, 0, invalidationReasonCreated)

	// The identical re-offer resolves the open offer (created=false) and the
	// store skips its refresh tail.
	if _, err := f.store.CreateOffer(context.Background(), OfferInput{
		ResourceType: "group", ResourceID: "grp_1",
		GranteeType: "system_account", GranteeUsername: "grantee",
	}, "owner", false, "owner"); err != nil {
		t.Fatal(err)
	}
	assertNoFanout(t, stats, bus, 1, 2)

	// The claim fires resource_authorization_updated.
	if _, err := f.store.AcceptSource(context.Background(), "grantee", created.SourceID, AcceptInput{}, nowText); err != nil {
		t.Fatal(err)
	}
	assertOneFanout(t, stats, bus, 1, 2, invalidationReasonUpdated)

	// Pause fires resource_authorization_updated.
	if err := f.store.SetOfferPaused(context.Background(), created.GrantID, "owner", true, false, nowText); err != nil {
		t.Fatal(err)
	}
	assertOneFanout(t, stats, bus, 2, 4, invalidationReasonUpdated)

	// Resume fires resource_authorization_updated.
	if err := f.store.SetOfferPaused(context.Background(), created.GrantID, "owner", false, false, nowText); err != nil {
		t.Fatal(err)
	}
	assertOneFanout(t, stats, bus, 3, 6, invalidationReasonUpdated)

	// Revoke fires resource_authorization_revoked; the second revoke hits the
	// already-revoked short-circuit and stays silent.
	if err := f.store.RevokeOffer(context.Background(), created.GrantID, "owner", false, nowText); err != nil {
		t.Fatal(err)
	}
	assertOneFanout(t, stats, bus, 4, 8, invalidationReasonRevoked)
	if err := f.store.RevokeOffer(context.Background(), created.GrantID, "owner", false, nowText); err != nil {
		t.Fatal(err)
	}
	assertNoFanout(t, stats, bus, 5, 10)

	// Grant B: the decline fires resource_authorization_updated.
	declined, err := f.store.CreateOffer(context.Background(), OfferInput{
		ResourceType: "group", ResourceID: "grp_1",
		GranteeType: "system_account", GranteeUsername: "grantee2",
	}, "owner", false, "owner")
	if err != nil {
		t.Fatal(err)
	}
	assertOneFanout(t, stats, bus, 5, 10, invalidationReasonCreated)
	if err := f.store.DeclineSource(context.Background(), "grantee2", declined.SourceID, nowText); err != nil {
		t.Fatal(err)
	}
	assertOneFanout(t, stats, bus, 6, 12, invalidationReasonUpdated)

	// Grant C: the receiver-side return fires resource_authorization_updated.
	returned, err := f.store.CreateOffer(context.Background(), OfferInput{
		ResourceType: "group", ResourceID: "grp_1",
		GranteeType: "system_account", GranteeUsername: "grantee2",
	}, "owner", false, "owner")
	if err != nil {
		t.Fatal(err)
	}
	assertOneFanout(t, stats, bus, 7, 14, invalidationReasonCreated)
	if err := f.store.ReturnSource(context.Background(), "grantee2", returned.SourceID, nowText); err != nil {
		t.Fatal(err)
	}
	assertOneFanout(t, stats, bus, 8, 16, invalidationReasonUpdated)
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

	f.createAcceptedDirectOffer(t, "group", "grp_1", "grantee")
	statsBaseline, busBaseline := len(stats.reasons), len(bus.calls)

	// The unreturnable probes (owner self, unknown group) never refresh.
	if _, err := f.store.ReturnGroupForGrantee(context.Background(), "grp_1", "owner", "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ReturnGroupForGrantee(context.Background(), "grp_missing", "grantee", "grantee"); err != nil {
		t.Fatal(err)
	}
	assertNoFanout(t, stats, bus, statsBaseline, busBaseline)

	if _, err := f.store.ReturnGroupForGrantee(context.Background(), "grp_1", "grantee", "grantee"); err != nil {
		t.Fatal(err)
	}
	// The source-level return commits its own updated fan-out; the
	// compatibility group wrapper then emits the returned fan-out.
	if got := len(stats.reasons); got != statsBaseline+2 ||
		stats.reasons[got-1] != invalidationReasonReturned {
		t.Fatalf("stats dirty reasons = %v, want updated+returned", stats.reasons)
	}
	if got := len(bus.calls); got != busBaseline+4 {
		t.Fatalf("bus calls = %d, want %d (%v)", got, busBaseline+4, bus.calls)
	}

	// The second return resolves no returnable grant and stays silent.
	if _, err := f.store.ReturnGroupForGrantee(context.Background(), "grp_1", "grantee", "grantee"); err != nil {
		t.Fatal(err)
	}
	assertNoFanout(t, stats, bus, statsBaseline+2, busBaseline+4)
}

// TestUnwiredInvalidationStaysSilent pins the nil-tolerance contract: stores
// built through plain NewStore (every pre-existing fixture and partial
// composition) keep their behavior with no fan-out wiring.
func TestUnwiredInvalidationStaysSilent(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedGroup(t, "grp_1", "owner")

	created, err := f.store.CreateOffer(context.Background(), OfferInput{
		ResourceType: "group", ResourceID: "grp_1",
		GranteeType: "system_account", GranteeUsername: "grantee",
	}, "owner", false, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.AcceptSource(context.Background(), "grantee", created.SourceID, AcceptInput{},
		f.now.UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := f.store.RevokeOffer(context.Background(), created.GrantID, "owner", false,
		f.now.UTC().Format(time.RFC3339Nano)); err != nil {
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

	if _, err := f.store.CreateOffer(context.Background(), OfferInput{
		ResourceType: "group", ResourceID: "grp_1",
		GranteeType: "system_account", GranteeUsername: "grantee",
	}, "owner", false, "owner"); err != nil {
		t.Fatalf("committed write must not fail on the stats marker: %v", err)
	}
	if len(stats.reasons) != 1 {
		t.Fatalf("stats attempts = %d, want 1", len(stats.reasons))
	}
	assertBusFanout(t, bus, 0, invalidationReasonCreated)
}

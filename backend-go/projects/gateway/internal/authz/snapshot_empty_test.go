package authz

import (
	"context"
	"testing"
	"time"
)

// A zero-account group snapshot is a legitimate canonical empty-set snapshot,
// not a missing one. Accepting a second source on the same runtime must work,
// and scope-diff must not tell the receiver to rebuild a snapshot that exists.
func TestZeroAccountGroupSnapshotAcceptsSecondSource(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedAccount(t, "approver", "active")
	f.seedGroup(t, "grp-zero", "owner")
	f.seedTeamWithMember(t, "team_zero", "grantee")
	if _, err := f.db.Exec(fixtureBind(`UPDATE system_teams SET created_by = 'approver' WHERE id = 'team_zero'`)); err != nil {
		t.Fatalf("set team approver: %v", err)
	}

	first, err := f.store.CreateOffer(context.Background(), OfferInput{
		ResourceType: "group", ResourceID: "grp-zero",
		GranteeType: "system_account", GranteeUsername: "grantee",
	}, "owner", false, "owner")
	if err != nil {
		t.Fatalf("create first offer: %v", err)
	}
	if _, err := f.acceptSourceForTest(t, "grantee", first.SourceID); err != nil {
		t.Fatalf("accept first source: %v", err)
	}
	scope, err := f.store.ScopeDiff(context.Background(), "grantee", first.SourceID)
	if err != nil {
		t.Fatalf("scope diff after empty snapshot: %v", err)
	}
	if scope.SnapshotMissing {
		t.Fatal("canonical empty-set snapshot must not be reported missing")
	}
	if scope.SnapshotCount != 0 || scope.CurrentCount != 0 {
		t.Fatalf("unexpected empty scope counts: %+v", scope)
	}

	teamOffer, err := f.store.CreateOffer(context.Background(), OfferInput{
		ResourceType: "group", ResourceID: "grp-zero",
		GranteeType: "team", TeamID: "team_zero",
	}, "owner", false, "owner")
	if err != nil {
		t.Fatalf("create team offer: %v", err)
	}
	now := f.now.UTC().Format(time.RFC3339Nano)
	if err := f.store.TeamApprove(context.Background(), "team_zero", teamOffer.GrantID, "approver", false, now); err != nil {
		t.Fatalf("team approve: %v", err)
	}
	var teamSourceID string
	if err := f.db.QueryRow(fixtureBind(`SELECT id FROM resource_authorization_sources
		WHERE grant_id = ? AND source_type = 'team' AND status = 'pending'`), teamOffer.GrantID).Scan(&teamSourceID); err != nil {
		t.Fatalf("load team source: %v", err)
	}
	if _, err := f.acceptSourceForTest(t, "grantee", teamSourceID); err != nil {
		t.Fatalf("accept second source on empty snapshot: %v", err)
	}
	var status string
	if err := f.db.QueryRow(fixtureBind(`SELECT status FROM resource_authorization_sources WHERE id = ?`), teamSourceID).
		Scan(&status); err != nil {
		t.Fatalf("read team source: %v", err)
	}
	if status != StatusActive {
		t.Fatalf("second source status = %q, want active", status)
	}
}

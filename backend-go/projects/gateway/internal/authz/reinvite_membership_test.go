package authz

// Team reinvites must stay inside the team: the actor can only re-offer a
// grant to an active member of the grant's team, never to an arbitrary account.

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestReinviteRejectsNonTeamMember(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "member", "active")
	f.seedAccount(t, "approver", "active")
	f.seedAccount(t, "outsider", "active")
	f.seedGroup(t, "grp-reinvite", "owner")
	f.seedTeamWithMember(t, "team_reinvite", "member")
	if _, err := f.db.Exec(fixtureBind(`UPDATE system_teams SET created_by = 'approver' WHERE id = 'team_reinvite'`)); err != nil {
		t.Fatalf("set team approver: %v", err)
	}
	offer, err := f.store.CreateOffer(context.Background(), OfferInput{
		ResourceType: "group", ResourceID: "grp-reinvite",
		GranteeType: "team", TeamID: "team_reinvite",
	}, "owner", false, "owner")
	if err != nil {
		t.Fatalf("create team offer: %v", err)
	}
	now := f.now.UTC().Format(time.RFC3339Nano)
	if err := f.store.TeamApprove(context.Background(), "team_reinvite", offer.GrantID, "approver", false, now); err != nil {
		t.Fatalf("team approve: %v", err)
	}

	_, err = f.store.Reinvite(context.Background(), offer.GrantID, "outsider", "owner", now)
	var fail *Fail
	if !errors.As(err, &fail) || fail.Code != "grantee_not_found" {
		t.Fatalf("non-member reinvite error = %v, want grantee_not_found", err)
	}
}

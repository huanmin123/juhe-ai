package authz

import (
	"context"
	"testing"
	"time"
)

// A pending team source can coexist with an already-active manual source on
// the same runtime. The receiver must still see the accept action: the store
// gate checks the source/grant, not the runtime projection.
func TestListInboundAllowsAcceptOfPendingTeamSourceOnActiveRuntime(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedTeamWithMember(t, "team-list", "grantee")
	f.seedGroup(t, "grp-list", "owner")
	ctx := context.Background()
	_, _ = f.createAcceptedDirectOffer(t, "group", "grp-list", "grantee")

	var runtimeID string
	if err := f.db.QueryRow(fixtureBind(`SELECT id FROM resource_authorizations
		WHERE resource_type = 'group' AND resource_id = 'grp-list' AND grantee_system_account_id = 'grantee'`)).
		Scan(&runtimeID); err != nil {
		t.Fatal(err)
	}
	now := f.now.UTC().Format(time.RFC3339Nano)
	if _, err := f.db.Exec(fixtureBind(`INSERT INTO resource_authorization_grants
		(id, resource_type, resource_id, resource_owner_system_account_id, grantee_type,
		 grantee_team_id, status, created_by, created_at, updated_at)
		VALUES ('grant-team-list', 'group', 'grp-list', 'owner', 'team', 'team-list', 'active', 'owner', ?, ?)`),
		now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(fixtureBind(`INSERT INTO resource_authorization_sources
		(id, authorization_id, grant_id, source_type, source_team_id, status,
		 created_by, created_at, updated_at)
		VALUES ('src-team-list', ?, 'grant-team-list', 'team', 'team-list', 'pending', 'owner', ?, ?)`),
		runtimeID, now, now); err != nil {
		t.Fatal(err)
	}

	items, err := f.store.ListInboundSources(ctx, "grantee")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.SourceID != "src-team-list" {
			continue
		}
		if !item.CanAccept {
			t.Fatalf("pending team source on active runtime must remain acceptable: %+v", item)
		}
		if item.RuntimeStatus != StatusActive || item.Status != StatusPending {
			t.Fatalf("unexpected row projection: %+v", item)
		}
		return
	}
	t.Fatal("pending team source missing from inbound list")
}

package authz

import (
	"context"
	"testing"
)

// TestReturnGroupForGrantee locks in the exported return domain the groups
// return-authorization route mounts through (Node
// returnGroupAuthorizationForGranteeAsync): the runtime receipt resolves by
// (group, grantee), the direct grant lands in returned, and a second return
// finds nothing.
func TestReturnGroupForGrantee(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedGroup(t, "grp_1", "owner")

	if _, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_1",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner"); err != nil {
		t.Fatal(err)
	}

	// Owner-self returns never resolve (Node :381).
	receipt, err := f.store.ReturnGroupForGrantee(context.Background(), "grp_1", "owner", "owner")
	if err != nil || receipt != nil {
		t.Fatalf("owner self return: %+v %v", receipt, err)
	}

	// Unknown group never resolves.
	receipt, err = f.store.ReturnGroupForGrantee(context.Background(), "grp_missing", "grantee", "grantee")
	if err != nil || receipt != nil {
		t.Fatalf("unknown group: %+v %v", receipt, err)
	}

	// The grantee return resolves the runtime receipt (resource_name from the
	// groups join) and flips the direct grant to returned.
	receipt, err = f.store.ReturnGroupForGrantee(context.Background(), "grp_1", "grantee", "actor_admin")
	if err != nil {
		t.Fatal(err)
	}
	if receipt == nil {
		t.Fatal("grantee return must resolve the receipt")
	}
	if receipt.ResourceType != "group" || receipt.ResourceID != "grp_1" ||
		receipt.ResourceOwnerSystemAccountID != "owner" || receipt.GranteeSystemAccountID != "grantee" {
		t.Fatalf("receipt identity drift: %+v", receipt)
	}
	if receipt.ResourceName != "Group grp_1" {
		t.Fatalf("receipt resource name drift: %q", receipt.ResourceName)
	}
	var grantStatus, revokedBy string
	if err := f.db.QueryRow(`SELECT status, COALESCE(revoked_by,'') FROM resource_authorization_grants
		WHERE resource_type = 'group' AND resource_id = 'grp_1' AND grantee_system_account_id = 'grantee'`).
		Scan(&grantStatus, &revokedBy); err != nil {
		t.Fatal(err)
	}
	if grantStatus != StatusReturned || revokedBy != "actor_admin" {
		t.Fatalf("grant stamps: status=%s revokedBy=%s", grantStatus, revokedBy)
	}
	var runtimeStatus string
	if err := f.db.QueryRow(`SELECT status FROM resource_authorizations
		WHERE resource_type = 'group' AND resource_id = 'grp_1' AND grantee_system_account_id = 'grantee'`).
		Scan(&runtimeStatus); err != nil {
		t.Fatal(err)
	}
	if runtimeStatus != StatusReturned {
		t.Fatalf("runtime status after return: %s", runtimeStatus)
	}

	// A second return finds no returnable direct grant.
	receipt, err = f.store.ReturnGroupForGrantee(context.Background(), "grp_1", "grantee", "actor_admin")
	if err != nil || receipt != nil {
		t.Fatalf("second return: %+v %v", receipt, err)
	}
}

// TestReturnGroupForGranteeRequiresManualSource pins the active-manual-source
// arm: a group whose runtime authorization carries no active manual source is
// not returnable (Node hasActiveManualRuntimeAuthorizationSource).
func TestReturnGroupForGranteeRequiresManualSource(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedGroup(t, "grp_1", "owner")

	if _, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_1",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner"); err != nil {
		t.Fatal(err)
	}
	// Drop the manual source directly to simulate a team-only runtime (the
	// direct grant itself stays active, so the returnable-grant arm is not
	// what short-circuits here).
	if _, err := f.db.Exec(`UPDATE resource_authorization_sources SET status = 'revoked'
		WHERE authorization_id IN (SELECT id FROM resource_authorizations
			WHERE resource_type = 'group' AND resource_id = 'grp_1' AND grantee_system_account_id = 'grantee')`); err != nil {
		t.Fatal(err)
	}
	receipt, err := f.store.ReturnGroupForGrantee(context.Background(), "grp_1", "grantee", "actor")
	if err != nil || receipt != nil {
		t.Fatalf("return without manual source: %+v %v", receipt, err)
	}
}

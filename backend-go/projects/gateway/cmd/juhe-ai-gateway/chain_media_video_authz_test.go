package main

import (
	"context"
	"testing"
)

// groupAuthorizationAllowed must compare group_authorization_settings'
// system_account_id with the receiver (runtime grantee), not with the group
// owner. The old comparison made every group-authorized media task fail at
// poll time because the receiver and group owner are different accounts by
// construction.（revision 31 授权大简化后 settings 行即领取行：无
// enabled/snapshot_revision 列，行在场且 receiver/group 匹配即放行。）
func TestMediaGroupAuthorizationAllowedUsesReceiverSettingsOwner(t *testing.T) {
	db := newChainFixturePGDatabase(t)
	seedChainBusinessSchema(t, db)
	ctx := context.Background()
	secret := "media-group-authz-test-secret"

	seed := func() {
		t.Helper()
		exec := func(query string, args ...any) {
			t.Helper()
			if _, err := db.Exec(fixtureBind(query), args...); err != nil {
				t.Fatalf("seed fixture: %v\n%s", err, query)
			}
		}
		exec(`INSERT INTO resource_authorizations
			(id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id,
			 status, expires_at, effective_source_type)
			VALUES ('authz-media', 'group', 'grp-media', 'owner-media', 'grantee-media', 'active', '', 'manual')`)
		exec(`INSERT INTO group_authorization_settings
			(authorization_id, system_account_id, group_id)
			VALUES ('authz-media', 'grantee-media', 'grp-media')`)
		exec(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at)
			VALUES ('grp-media', 'owner-media', 'source-account', 1, '2026-01-01T00:00:00Z')`)
	}
	seed()

	runtime := &mediaJobsRuntime{db: db, secret: secret}
	allowed, err := runtime.groupAuthorizationAllowed(ctx, "authz-media", "grp-media",
		"owner-media", "grantee-media", "source-account")
	if err != nil {
		t.Fatalf("groupAuthorizationAllowed: %v", err)
	}
	if !allowed {
		t.Fatal("group-authorized media task must pass with receiver settings owner")
	}

	// Negative control: storing the group owner in settings reproduces the old
	// broken shape and must fail.
	if _, err := db.Exec(fixtureBind(`UPDATE group_authorization_settings
		SET system_account_id = 'owner-media' WHERE authorization_id = 'authz-media'`)); err != nil {
		t.Fatalf("update settings owner: %v", err)
	}
	allowed, err = runtime.groupAuthorizationAllowed(ctx, "authz-media", "grp-media",
		"owner-media", "grantee-media", "source-account")
	if err != nil {
		t.Fatalf("groupAuthorizationAllowed negative: %v", err)
	}
	if allowed {
		t.Fatal("settings owner must be the receiver, not the group owner")
	}
}

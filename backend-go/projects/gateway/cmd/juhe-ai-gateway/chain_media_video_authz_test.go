package main

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/authzrevision"
)

// groupAuthorizationAllowed must compare group_authorization_settings'
// system_account_id with the receiver (runtime grantee), not with the group
// owner. The old comparison made every group-authorized media task fail at
// poll time because the receiver and group owner are different accounts by
// construction.
func TestMediaGroupAuthorizationAllowedUsesReceiverSettingsOwner(t *testing.T) {
	db := newChainFixturePGDatabase(t)
	seedChainBusinessSchema(t, db)
	ctx := context.Background()
	secret := "media-group-authz-test-secret"

	for _, statement := range []string{
		`ALTER TABLE group_authorization_settings ADD COLUMN snapshot_revision TEXT`,
		`CREATE TABLE resource_authorization_sources (
			authorization_id TEXT NOT NULL, source_type TEXT NOT NULL, source_team_id TEXT,
			status TEXT NOT NULL, accepted_source_revision TEXT,
			accepted_source_revision_version INTEGER, revision_version INTEGER NOT NULL DEFAULT 0,
			grant_id TEXT)`,
		`CREATE TABLE resource_authorization_grants (id TEXT PRIMARY KEY, status TEXT NOT NULL, expires_at TEXT)`,
		`CREATE TABLE resource_authorization_group_accounts (
			authorization_id TEXT NOT NULL, account_id TEXT NOT NULL)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed authz schema: %v\n%s", err, statement)
		}
	}

	grantRevision := authzrevision.Group(secret, "grp-media", "owner-media")
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
			(authorization_id, system_account_id, group_id, enabled, snapshot_revision)
			VALUES ('authz-media', 'grantee-media', 'grp-media', 1, 'snapshot-1')`)
		exec(`INSERT INTO resource_authorization_group_accounts (authorization_id, account_id)
			VALUES ('authz-media', 'source-account')`)
		exec(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at)
			VALUES ('grp-media', 'owner-media', 'source-account', 1, '2026-01-01T00:00:00Z')`)
		exec(`INSERT INTO resource_authorization_grants (id, status, expires_at)
			VALUES ('grant-media', 'active', '')`)
		exec(`INSERT INTO resource_authorization_sources
			(authorization_id, source_type, source_team_id, status, accepted_source_revision,
			 accepted_source_revision_version, revision_version, grant_id)
			VALUES ('authz-media', 'manual', NULL, 'active', ?, ?, ?, 'grant-media')`,
			grantRevision, authzrevision.Version, authzrevision.Version)
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

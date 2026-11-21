package accounts

// Recipient-consent boundary: binding an authorized instance to a group is an
// explicit receiver action. The management surface must reject it even for an
// administrator acting inside the receiver's own scope; the receiver's own
// personal PATCH stays usable.

import (
	"context"
	"errors"
	"testing"
)

func TestPatchAdminCannotBindAuthorizedInstance(t *testing.T) {
	env := newTestEnv(t)
	const accountID = "acc-admin-authz"
	env.seedAccount(t, accountID, "receiver", "授权实例", "active")
	env.seedProviderAndDefaultGroup(t, "receiver")
	env.exec(t, `UPDATE accounts SET authorization_instance_authorization_id = 'ra-admin-authz'
		WHERE id = ?`, accountID)
	env.exec(t, `INSERT INTO resource_authorizations
		(id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id,
		 status, created_by, created_at, updated_at)
		VALUES ('ra-admin-authz', 'account', 'acc-source-admin-authz', 'owner-source', 'receiver',
		 'active', 'owner-source', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`)
	ctx := context.Background()
	bind := PatchInput{
		ExpectedConfigRevision: 1,
		GroupID:                stringPtr("grp-default-receiver"),
		GroupIDPresent:         true,
	}

	_, err := env.store.Patch(ctx, accountID, bind, AccessScope{ViewerID: "receiver", IsAdmin: true})
	var validation *ValidationError
	if !errors.As(err, &validation) || validation.Code != "authorization_binding_forbidden" {
		t.Fatalf("admin authorized binding error = %v, want authorization_binding_forbidden", err)
	}
	if got := env.count(t, `SELECT COUNT(*) FROM group_accounts WHERE account_id = ?`, accountID); got != 0 {
		t.Fatalf("rejected admin binding must not write group_accounts, got %d rows", got)
	}

	if _, err := env.store.Patch(ctx, accountID, bind, AccessScope{ViewerID: "receiver"}); err != nil {
		t.Fatalf("receiver self binding must stay usable: %v", err)
	}
	if got := env.count(t, `SELECT COUNT(*) FROM group_accounts WHERE account_id = ?`, accountID); got != 1 {
		t.Fatalf("receiver binding rows = %d, want 1", got)
	}
}

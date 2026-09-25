package groups

import (
	"context"
	"testing"
)

// Delete must cascade the group's resource_authorizations rows
// (resource_type='group') and their resource_authorization_sources children
// inside the delete transaction; unrelated resource types and other groups'
// authorizations stay.
func TestW11FDeleteCascadesResourceAuthorizations(t *testing.T) {
	env := newW11FEnv(t)
	store := env.store
	ctx := context.Background()
	env.w11fSeedGroup(t, "w11f-cg", "w11f-cowner", "w11f-级联组", "openai", 1, 0, "personal")
	env.w11fSeedGroup(t, "w11f-ck", "w11f-cowner", "w11f-保留组", "openai", 1, 0, "personal")
	const seedAuthorization = `INSERT INTO resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id, status, created_by, created_at, updated_at)
		VALUES (?, ?, ?, 'w11f-cowner', 'w11f-cgrantee', 'active', 'w11f-cowner', '2024-01-01T00:00:00.000Z', '2024-01-01T00:00:00.000Z')`
	env.exec(t, seedAuthorization, "w11f-ca", "group", "w11f-cg")
	env.exec(t, seedAuthorization, "w11f-cb", "group", "w11f-ck")
	env.exec(t, seedAuthorization, "w11f-cc", "account", "w11f-unrelated")
	env.exec(t, `INSERT INTO resource_authorization_sources (id, authorization_id, source_type, source_team_id, status, activated_at, ended_at, ended_reason, created_by, created_at, updated_at) VALUES
		('w11f-csrc1', 'w11f-ca', 'manual', NULL, 'active', '2024-01-01T00:00:00.000Z', NULL, NULL, 'w11f-cowner', '2024-01-01T00:00:00.000Z', '2024-01-01T00:00:00.000Z'),
		('w11f-csrc2', 'w11f-cb', 'manual', NULL, 'active', '2024-01-01T00:00:00.000Z', NULL, NULL, 'w11f-cowner', '2024-01-01T00:00:00.000Z', '2024-01-01T00:00:00.000Z'),
		('w11f-csrc3', 'w11f-cc', 'manual', NULL, 'active', '2024-01-01T00:00:00.000Z', NULL, NULL, 'w11f-cowner', '2024-01-01T00:00:00.000Z', '2024-01-01T00:00:00.000Z')`)

	result, err := store.Delete(ctx, "w11f-cg", AccessScope{ViewerID: "w11f-cowner"})
	if err != nil || !result.Deleted {
		t.Fatalf("delete = %+v/%v", result, err)
	}
	if remaining := env.queryString(t, `SELECT COUNT(*) FROM resource_authorizations WHERE id IN ('w11f-ca','w11f-cb','w11f-cc')`); remaining != "2" {
		t.Fatalf("authorizations remaining = %s (w11f-ca must be gone, others kept)", remaining)
	}
	if remaining := env.queryString(t, `SELECT COUNT(*) FROM resource_authorization_sources WHERE id IN ('w11f-csrc1','w11f-csrc2','w11f-csrc3')`); remaining != "2" {
		t.Fatalf("sources remaining = %s (w11f-csrc1 must be gone, others kept)", remaining)
	}
}

package authz

import (
	"context"
	"strings"
	"testing"
	"time"
)

// accountsFixtureDDL adds the accounts table the authorized-read projection
// joins (and resolveResourceOwner reads). The base fixture owns the
// authorization state-machine tables; the projection only needs the owner and
// instance correlation columns.
const accountsFixtureDDL = `CREATE TABLE IF NOT EXISTS accounts (
	id TEXT PRIMARY KEY,
	system_account_id TEXT NOT NULL,
	name TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'active',
	resource_owner_system_account_id TEXT NOT NULL DEFAULT '',
	deleted_at TEXT,
	account_expires_at TEXT,
	authorization_instance_authorization_id TEXT,
	authorization_instance_source_account_id TEXT)`

func (f *fixture) seedSourceAccount(t *testing.T, id, ownerID string) {
	t.Helper()
	if _, err := f.db.Exec(`INSERT INTO accounts (id, system_account_id, name, resource_owner_system_account_id)
		VALUES (?, ?, ?, ?)`, id, ownerID, "源账户 "+id, ownerID); err != nil {
		t.Fatal(err)
	}
}

// seedInstanceAccount mirrors the authorization-quota join shape: canonical
// rows carry the runtime authorization id, legacy rows only the grantee
// namespace plus the source account id.
func (f *fixture) seedInstanceAccount(t *testing.T, id, namespaceID, authorizationID, sourceID string) {
	t.Helper()
	if _, err := f.db.Exec(`INSERT INTO accounts (id, system_account_id, name,
		authorization_instance_authorization_id, authorization_instance_source_account_id)
		VALUES (?, ?, ?, ?, ?)`, id, namespaceID, "授权实例 "+id, nullableText(authorizationID), sourceID); err != nil {
		t.Fatal(err)
	}
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func runtimeIDFor(t *testing.T, f *fixture, grantee, resourceID string) string {
	t.Helper()
	var id string
	if err := f.db.QueryRow(`SELECT id FROM resource_authorizations
		WHERE grantee_system_account_id = ? AND resource_id = ?`, grantee, resourceID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func readableKeys(t *testing.T, f *fixture, viewer string) map[string]bool {
	t.Helper()
	ids, err := f.store.AuthorizedReadableAccountIDs(context.Background(), viewer)
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

func TestAuthorizedReadableAccountIDsTeamAndDirect(t *testing.T) {
	f := newFixture(t)
	if _, err := f.db.Exec(accountsFixtureDDL); err != nil {
		t.Fatal(err)
	}
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "member1", "active")
	f.seedAccount(t, "member2", "active")
	f.seedAccount(t, "outsider", "active")
	f.seedTeamWithMember(t, "team_1", "member1")
	f.seedSourceAccount(t, "acc-src-team", "owner")
	f.seedSourceAccount(t, "acc-src-direct", "owner")

	// Team grant → one runtime row per active member.
	if _, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "account", ResourceID: "acc-src-team",
		GranteeType: "team", GranteeID: "team_1",
	}, "owner"); err != nil {
		t.Fatal(err)
	}
	// Direct grant to member2.
	if _, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "account", ResourceID: "acc-src-direct",
		GranteeType: "system_account", GranteeID: "member2",
	}, "owner"); err != nil {
		t.Fatal(err)
	}

	teamRuntime := runtimeIDFor(t, f, "member1", "acc-src-team")
	f.seedInstanceAccount(t, "acc-inst-team", "member1", teamRuntime, "acc-src-team")
	// Each member's instance is stamped with his own runtime row inside his
	// own namespace (Node resource-authorization-write.repository.ts:1880-1915,
	// grantee namespace at :1893).
	directRuntime := runtimeIDFor(t, f, "member2", "acc-src-direct")
	f.seedInstanceAccount(t, "acc-inst-direct", "member2", directRuntime, "acc-src-direct")
	f.seedInstanceAccount(t, "acc-inst-direct-legacy", "member2", "", "acc-src-direct")

	teamIDs := readableKeys(t, f, "member1")
	if !teamIDs["acc-inst-team"] || len(teamIDs) != 1 {
		t.Fatalf("team member readable ids: %v", teamIDs)
	}
	// Cross-member isolation: the team source must never surface another
	// member's runtime row or instance (Node visibility is own-row only; no
	// team-member branch on the read path).
	if teamIDs["acc-inst-direct"] || teamIDs["acc-inst-direct-legacy"] {
		t.Fatalf("team member must not read member2 instances: %v", teamIDs)
	}
	directIDs := readableKeys(t, f, "member2")
	if !directIDs["acc-inst-direct"] || !directIDs["acc-inst-direct-legacy"] || len(directIDs) != 2 {
		t.Fatalf("direct grantee readable ids: %v", directIDs)
	}
	if directIDs["acc-inst-team"] {
		t.Fatalf("member2 must not read the member1 team instance: %v", directIDs)
	}
	if ids := readableKeys(t, f, "outsider"); len(ids) != 0 {
		t.Fatalf("outsider must see nothing: %v", ids)
	}
	if ids := readableKeys(t, f, "owner"); len(ids) != 0 {
		t.Fatalf("owner is not a grantee: %v", ids)
	}
	// The source account itself is never the readable row.
	for viewer := range map[string]bool{"member1": true, "member2": true} {
		if ids := readableKeys(t, f, viewer); ids["acc-src-team"] || ids["acc-src-direct"] {
			t.Fatalf("source account leaked for %s: %v", viewer, ids)
		}
	}
}

func TestAuthorizedReadableAccountIDsRevokedPausedAndExpired(t *testing.T) {
	f := newFixture(t)
	if _, err := f.db.Exec(accountsFixtureDDL); err != nil {
		t.Fatal(err)
	}
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "member1", "active")
	f.seedTeamWithMember(t, "team_1", "member1")
	f.seedSourceAccount(t, "acc-src", "owner")

	result, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "account", ResourceID: "acc-src",
		GranteeType: "team", GranteeID: "team_1",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	f.seedInstanceAccount(t, "acc-inst", "member1",
		runtimeIDFor(t, f, "member1", "acc-src"), "acc-src")
	if ids := readableKeys(t, f, "member1"); !ids["acc-inst"] {
		t.Fatalf("active grant must expose the instance: %v", ids)
	}

	// Paused team grant → runtime paused → still readable: the Node list guard
	// keeps instances visible for status IN ('active','paused','expired')
	// (account-management-list.repository.ts:331-334).
	paused := StatusPaused
	if outcome, err := f.store.Patch(context.Background(), result.Item.ID, PatchInput{Status: &paused},
		result.Item.UpdatedAt, "owner"); err != nil || outcome.Status != "updated" {
		t.Fatalf("pause patch: %+v err=%v", outcome, err)
	}
	if ids := readableKeys(t, f, "member1"); !ids["acc-inst"] {
		t.Fatalf("paused grant must keep the instance readable: %v", ids)
	}

	// Patching the team grant back to active runs the Node grant→runtime sync
	// (patchResourceAuthorizationAsync :809 → syncTeamGrantMemberAuthorizations
	// → upsertResourceAuthorizationForUser :863-867), so the runtime row
	// returns to active and the correlated instance becomes visible again.
	active := StatusActive
	reactivated, err := f.store.Patch(context.Background(), result.Item.ID, PatchInput{Status: &active},
		outcomeUpdatedAt(t, f, result.Item.ID), "owner")
	if err != nil {
		t.Fatal(err)
	}
	if reactivated.Status != "updated" {
		t.Fatalf("reactivate patch: %+v", reactivated)
	}
	var runtimeStatus string
	if err := f.db.QueryRow(`SELECT status FROM resource_authorizations
		WHERE grantee_system_account_id = 'member1' AND resource_id = 'acc-src'`).Scan(&runtimeStatus); err != nil {
		t.Fatal(err)
	}
	if runtimeStatus != StatusActive {
		t.Fatalf("runtime after grant reactivate = %s, want active (Node sync write-back)", runtimeStatus)
	}
	if ids := readableKeys(t, f, "member1"); !ids["acc-inst"] {
		t.Fatalf("resumed grant must expose the instance again: %v", ids)
	}

	// A fresh manual grant on the same resource records a superseded manual
	// source while the live team source keeps the runtime active; visibility is
	// unchanged.
	if _, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "account", ResourceID: "acc-src",
		GranteeType: "system_account", GranteeID: "member1",
	}, "owner"); err != nil {
		t.Fatal(err)
	}
	if ids := readableKeys(t, f, "member1"); !ids["acc-inst"] {
		t.Fatalf("manual grant over active team source must stay visible: %v", ids)
	}

	// Owner-side revoke of the manual grant only revokes its manual sources
	// (syncUserGrantRuntime :1315-1326 filters source_type='manual'); the
	// active team source keeps the runtime authorization alive, so the instance
	// stays readable.
	revoked, err := f.store.Revoke(context.Background(), outcomeUpdatedAtGrantID(t, f, "acc-src", "system_account", "member1"),
		outcomeUpdatedAt(t, f, outcomeUpdatedAtGrantID(t, f, "acc-src", "system_account", "member1")), "owner")
	if err != nil || revoked.Status != "updated" {
		t.Fatalf("revoke: %+v err=%v", revoked, err)
	}
	if ids := readableKeys(t, f, "member1"); !ids["acc-inst"] {
		t.Fatalf("revoked manual grant must not hide the team-carried runtime: %v", ids)
	}

	// Revoking the team grant revokes the team sources of the resource
	// (syncTeamGrantMemberAuthorizations → revokeTeamGrantSources) and the
	// instance becomes invisible.
	teamGrantID := outcomeUpdatedAtGrantID(t, f, "acc-src", "team", "team_1")
	teamRevoked, err := f.store.Revoke(context.Background(), teamGrantID, outcomeUpdatedAt(t, f, teamGrantID), "owner")
	if err != nil || teamRevoked.Status != "updated" {
		t.Fatalf("team revoke: %+v err=%v", teamRevoked, err)
	}
	if ids := readableKeys(t, f, "member1"); len(ids) != 0 {
		t.Fatalf("revoked team grant must hide the instance: %v", ids)
	}

	// Model an existing expired row directly: Node rejects an attempt to create
	// a newly expired authorization, and expiry is materialized as the status
	// flip by the sweep (expireDueResourceAuthorizationsAsync :930-969). The
	// Node list guard keeps 'expired' instances readable
	// (account-management-list.repository.ts:331-334).
	futureExpiry := f.now.Add(time.Hour).UTC().Format(time.RFC3339Nano)
	created, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "account", ResourceID: "acc-src",
		GranteeType: "system_account", GranteeID: "member1",
		ExpiresAt: &futureExpiry,
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	expiredAt := f.now.Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	if _, err := f.db.Exec(`UPDATE resource_authorization_grants SET status = ?, expires_at = ? WHERE id = ?`,
		StatusExpired, expiredAt, created.Item.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE resource_authorizations SET status = ?, expires_at = ? WHERE resource_type = 'account' AND resource_id = 'acc-src' AND grantee_system_account_id = 'member1'`,
		StatusExpired, expiredAt); err != nil {
		t.Fatal(err)
	}
	if ids := readableKeys(t, f, "member1"); !ids["acc-inst"] {
		t.Fatalf("expired grant must keep the instance readable: %v", ids)
	}
}

// outcomeUpdatedAtGrantID locates the grant row for a resource+grantee pair.
func outcomeUpdatedAtGrantID(t *testing.T, f *fixture, resourceID, granteeType, granteeID string) string {
	t.Helper()
	column := "grantee_system_account_id"
	if granteeType == "team" {
		column = "grantee_team_id"
	}
	var id string
	if err := f.db.QueryRow(`SELECT id FROM resource_authorization_grants
		WHERE resource_id = ? AND `+column+` = ? ORDER BY created_at DESC, id DESC LIMIT 1`, resourceID, granteeID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// outcomeUpdatedAt reads the current grant version between mutations.
func outcomeUpdatedAt(t *testing.T, f *fixture, grantID string) string {
	t.Helper()
	var updatedAt string
	if err := f.db.QueryRow(`SELECT updated_at FROM resource_authorization_grants WHERE id = ?`, grantID).Scan(&updatedAt); err != nil {
		t.Fatal(err)
	}
	return updatedAt
}

func TestAuthorizedReadableAccountIDsEmptyViewer(t *testing.T) {
	f := newFixture(t)
	if _, err := f.db.Exec(accountsFixtureDDL); err != nil {
		t.Fatal(err)
	}
	ids, err := f.store.AuthorizedReadableAccountIDs(context.Background(), "  ")
	if err != nil || len(ids) != 0 {
		t.Fatalf("blank viewer: %v %v", ids, err)
	}
	if _, err := f.store.AuthorizedReadableAccountIDs(context.Background(), strings.Repeat("x", 64)); err != nil {
		t.Fatalf("unknown viewer must not error: %v", err)
	}
}

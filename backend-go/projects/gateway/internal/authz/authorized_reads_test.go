package authz

import (
	"context"
	"strings"
	"testing"
	"time"
)

// accountsFixtureDDL adds the accounts table the authorized-read projection
// joins (and resolveResource reads). The base fixture owns the
// authorization state-machine tables; the projection only needs the owner and
// instance correlation columns.
const accountsFixtureDDL = `CREATE TABLE IF NOT EXISTS accounts (
	id TEXT PRIMARY KEY,
	system_account_id TEXT NOT NULL,
	name TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'active',
	deleted_at TEXT,
	account_expires_at TEXT,
	authorization_instance_authorization_id TEXT,
	authorization_instance_source_account_id TEXT)`

func (f *fixture) seedSourceAccount(t *testing.T, id, ownerID string) {
	t.Helper()
	//（accounts.credentials_encrypted 进入 material revision 的解密面
	// （authzrevision.Compute fail-closed）：fixture 账户必须种有效 v1
	// envelope，空值会让 CreateOffer 直接报解密失败。）
	credentials := sealedTestCredentials(t, map[string]any{"api_key": "sk-" + id})
	if _, err := f.db.Exec(fixtureBind(`INSERT INTO accounts (id, system_account_id, name, credentials_encrypted)
		VALUES (?, ?, ?, ?)`), id, ownerID, "源账户 "+id, credentials); err != nil {
		t.Fatal(err)
	}
}

// seedInstanceAccount mirrors the authorized-instance join shape: canonical
// rows carry the runtime authorization id, fallback rows only the grantee
// namespace plus the source account id.
func (f *fixture) seedInstanceAccount(t *testing.T, id, namespaceID, authorizationID, sourceID string) {
	t.Helper()
	if _, err := f.db.Exec(fixtureBind(`INSERT INTO accounts (id, system_account_id, name,
		authorization_instance_authorization_id, authorization_instance_source_account_id)
		VALUES (?, ?, ?, ?, ?)`), id, namespaceID, "授权实例 "+id, nullableText(authorizationID), sourceID); err != nil {
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
	if err := f.db.QueryRow(fixtureBind(`SELECT id FROM resource_authorizations
		WHERE grantee_system_account_id = ? AND resource_id = ?`), grantee, resourceID).Scan(&id); err != nil {
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

// pendingTeamSourceFor resolves the member's pending team source of a grant.
func pendingTeamSourceFor(t *testing.T, f *fixture, grantID, teamID, memberID string) string {
	t.Helper()
	var sourceID string
	if err := f.db.QueryRow(fixtureBind(`SELECT s.id FROM resource_authorization_sources s
		JOIN resource_authorizations r ON r.id = s.authorization_id
		WHERE s.grant_id = ? AND s.source_type = 'team' AND s.source_team_id = ?
		  AND s.status = 'pending' AND r.grantee_system_account_id = ?
		LIMIT 1`), grantID, teamID, memberID).Scan(&sourceID); err != nil {
		t.Fatalf("pending team source for %s: %v", memberID, err)
	}
	return sourceID
}

// TestAuthorizedReadableAccountIDsTeamAndDirect walks the authorized-instance
// read projection: a viewer reads instances only through his own runtime row
// (canonical stamped rows plus the own-namespace un-stamped fallback), never
// another member's rows, and never the source account itself.
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
	seedGranteeDefaultGroup(t, f, "member1", "gpt")
	seedGranteeDefaultGroup(t, f, "member2", "gpt")

	// Team offer → the member claims his pending source (one-step claim
	// provisions and binds a real instance stamped with his runtime row).
	teamOffer, err := f.store.CreateOffer(context.Background(), OfferInput{
		ResourceType: "account", ResourceID: "acc-src-team",
		GranteeType: "team", TeamID: "team_1",
	}, "owner", false, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.acceptSourceForTest(t, "member1", pendingTeamSourceFor(t, f, teamOffer.GrantID, "team_1", "member1")); err != nil {
		t.Fatal(err)
	}
	// Direct offer → member2 claims.
	directOffer, err := f.store.CreateOffer(context.Background(), OfferInput{
		ResourceType: "account", ResourceID: "acc-src-direct",
		GranteeType: "system_account", GranteeUsername: "member2",
	}, "owner", false, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.acceptSourceForTest(t, "member2", directOffer.SourceID); err != nil {
		t.Fatal(err)
	}

	teamRuntime := runtimeIDFor(t, f, "member1", "acc-src-team")
	f.seedInstanceAccount(t, "acc-inst-team", "member1", teamRuntime, "acc-src-team")
	// Each member's instance is stamped with his own runtime row inside his
	// own namespace; the un-stamped fallback row stays scoped to the viewer's
	// own namespace.
	directRuntime := runtimeIDFor(t, f, "member2", "acc-src-direct")
	f.seedInstanceAccount(t, "acc-inst-direct", "member2", directRuntime, "acc-src-direct")
	f.seedInstanceAccount(t, "acc-inst-direct-fallback", "member2", "", "acc-src-direct")

	teamIDs := readableKeys(t, f, "member1")
	if !teamIDs["acc-inst-team"] {
		t.Fatalf("team member must read his stamped instance: %v", teamIDs)
	}
	// Cross-member isolation: the team source must never surface another
	// member's runtime row or instance (own-row only; no team-member branch on
	// the read path).
	if teamIDs["acc-inst-direct"] || teamIDs["acc-inst-direct-fallback"] {
		t.Fatalf("team member must not read member2 instances: %v", teamIDs)
	}
	directIDs := readableKeys(t, f, "member2")
	// The one-step claim adds one real instance for the direct grant on top of
	// the two seeded rows (stamped + namespace fallback).
	if !directIDs["acc-inst-direct"] || !directIDs["acc-inst-direct-fallback"] {
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

// TestAuthorizedReadableAccountIDsStatusMatrix pins the status guard
// (active/paused/expired readable; revoked and returned hide the instance)
// and the terminal side effect: a revoked runtime disables its provisioned
// instance, which additionally drops it from the projection.
func TestAuthorizedReadableAccountIDsStatusMatrix(t *testing.T) {
	f := newFixture(t)
	if _, err := f.db.Exec(accountsFixtureDDL); err != nil {
		t.Fatal(err)
	}
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedSourceAccount(t, "acc-src", "owner")
	seedGranteeDefaultGroup(t, f, "grantee", "gpt")

	grantID := f.claimAccountOffer(t, "acc-src", "grantee", "")
	if ids := readableKeys(t, f, "grantee"); len(ids) < 1 {
		t.Fatalf("active grant must expose the provisioned instance: %v", ids)
	}
	// 空 targetGroupId 走 grantee 默认组兜底：绑定必须落在 seedGranteeDefaultGroup
	// 建的 is_default 组上（一步创建 + 绑定，无独立 bind 调用）。
	var boundGroupID string
	if err := f.db.QueryRow(fixtureBind(`SELECT ga.group_id FROM group_accounts ga
		JOIN accounts a ON a.id = ga.account_id
		WHERE a.authorization_instance_source_account_id = 'acc-src'
		AND a.system_account_id = 'grantee'`)).Scan(&boundGroupID); err != nil {
		t.Fatal(err)
	}
	if boundGroupID != "grp-default-grantee" {
		t.Fatalf("default-group fallback binding = %q, want grp-default-grantee", boundGroupID)
	}

	// Paused offer → runtime paused → still readable (the management-list
	// guard keeps active/paused/expired visible).
	if err := f.store.SetOfferPaused(context.Background(), grantID, "owner", true, false,
		f.now.UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("pause: %v", err)
	}
	var runtimeStatus string
	if err := f.db.QueryRow(fixtureBind(`SELECT status FROM resource_authorizations
		WHERE grantee_system_account_id = 'grantee' AND resource_id = 'acc-src'`)).Scan(&runtimeStatus); err != nil {
		t.Fatal(err)
	}
	if runtimeStatus != StatusPaused {
		t.Fatalf("runtime after pause = %s, want paused", runtimeStatus)
	}
	if ids := readableKeys(t, f, "grantee"); len(ids) < 1 {
		t.Fatalf("paused grant must keep the instance readable: %v", ids)
	}

	// Resume → active → readable again.
	if err := f.store.SetOfferPaused(context.Background(), grantID, "owner", false, false,
		f.now.UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if ids := readableKeys(t, f, "grantee"); len(ids) < 1 {
		t.Fatalf("resumed grant must expose the instance again: %v", ids)
	}

	// Owner revoke → terminal runtime → the instance is disabled and invisible.
	if err := f.store.RevokeOffer(context.Background(), grantID, "owner", false,
		f.now.UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if ids := readableKeys(t, f, "grantee"); len(ids) != 0 {
		t.Fatalf("revoked grant must hide the instance: %v", ids)
	}

	// Model an existing expired runtime directly: expiry is materialized as the
	// status flip by the sweep; the list guard keeps expired readable.
	if _, err := f.db.Exec(fixtureBind(`UPDATE resource_authorizations SET status = ? WHERE resource_type = 'account' AND resource_id = 'acc-src' AND grantee_system_account_id = 'grantee'`), StatusExpired); err != nil {
		t.Fatal(err)
	}
	if ids := readableKeys(t, f, "grantee"); len(ids) < 1 {
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
	if err := f.db.QueryRow(fixtureBind(`SELECT id FROM resource_authorization_grants
		WHERE resource_id = ? AND `+column+` = ? ORDER BY created_at DESC, id DESC LIMIT 1`), resourceID, granteeID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
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

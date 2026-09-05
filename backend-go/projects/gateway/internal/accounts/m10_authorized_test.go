package accounts

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authz"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// m10AuthzDDL adds the authorization state-machine tables the authz slice
// owns; the accounts schema above already carries the accounts instance
// correlation columns (authorization_instance_*).
var m10AuthzDDL = []string{
	`CREATE TABLE IF NOT EXISTS system_teams (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, status TEXT NOT NULL DEFAULT 'active', created_by TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS system_team_members (id TEXT PRIMARY KEY, team_id TEXT NOT NULL, system_account_id TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'active', joined_at TEXT NOT NULL, removed_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS resource_authorizations (id TEXT PRIMARY KEY, resource_type TEXT NOT NULL, resource_id TEXT NOT NULL, resource_owner_system_account_id TEXT NOT NULL, grantee_system_account_id TEXT NOT NULL, scope TEXT NOT NULL DEFAULT 'use', status TEXT NOT NULL DEFAULT 'active', effective_source_type TEXT, effective_source_team_id TEXT, activated_at TEXT, last_source_changed_at TEXT, remark TEXT, expires_at TEXT, limits_json TEXT, created_by TEXT NOT NULL, created_at TEXT NOT NULL, revoked_by TEXT, revoked_at TEXT, revoked_reason TEXT, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS resource_authorization_sources (id TEXT PRIMARY KEY, authorization_id TEXT NOT NULL, source_type TEXT NOT NULL, source_team_id TEXT, status TEXT NOT NULL DEFAULT 'active', activated_at TEXT, ended_at TEXT, ended_reason TEXT, created_by TEXT NOT NULL, created_at TEXT NOT NULL, revoked_by TEXT, revoked_at TEXT, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS resource_authorization_grants (id TEXT PRIMARY KEY, resource_type TEXT NOT NULL, resource_id TEXT NOT NULL, resource_owner_system_account_id TEXT NOT NULL, grantee_type TEXT NOT NULL, grantee_system_account_id TEXT, grantee_team_id TEXT, scope TEXT NOT NULL DEFAULT 'use', status TEXT NOT NULL DEFAULT 'active', remark TEXT, expires_at TEXT, limits_json TEXT, created_by TEXT NOT NULL, created_at TEXT NOT NULL, revoked_by TEXT, revoked_at TEXT, updated_at TEXT NOT NULL)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_m10_resource_authorizations_user_unique ON resource_authorizations(resource_type, resource_id, grantee_system_account_id)`,
}

// newAuthorizedTestEnv reuses the accounts fixture database but serves a
// second kernel whose accounts Deps carries the authz authorized-instance
// reader (the composition-root wiring the M10 slice prescribes).
func newAuthorizedTestEnv(t *testing.T) (*testEnv, *authz.Store) {
	t.Helper()
	base := newTestEnv(t)
	for _, statement := range m10AuthzDDL {
		if _, err := base.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	authzStore, err := authz.NewStore(base.db, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(base.db, false, testSecret, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	k := kernel.New(kernel.Options{CompressionDisabled: true})
	base.deps.MountAuth(k, "lax", false)
	(&Deps{Store: store, Auth: base.deps, Authorized: authzStore}).Mount(k)
	server := httptest.NewServer(k.Handler())
	t.Cleanup(server.Close)
	wired := &testEnv{deps: base.deps, k: k, server: server, jar: map[string]string{}, db: base.db}
	return wired, authzStore
}

// seedAuthorizationInstance inserts the instance account in the given
// namespace. Node provisions instances in the grantee's namespace
// (resource-authorization-write.repository.ts:1880-1915, system_account_id =
// grantee_system_account_id at :1893); tests must seed the same shape so the
// authorized-view pass-through is exercised against production semantics.
func (e *testEnv) seedAuthorizationInstance(t *testing.T, id, namespaceID, runtimeID, sourceID string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	e.exec(t, `INSERT INTO accounts (id, system_account_id, provider_code, provider_protocol_profile_id,
		protocol_code, protocol_version, name, type, status, credentials_encrypted, credential_mask,
		health_check_model, authorization_instance_authorization_id, authorization_instance_source_account_id,
		created_at, updated_at)
		VALUES (?, ?, 'gpt', 'prof-gpt', 'openai', 'v1', ?, 'api_key', 'active',
		'sealed', 'masked', 'gpt-4o-mini', ?, ?, ?, ?)`, id, namespaceID, "授权实例-"+id, runtimeID, sourceID, now, now)
}

func (e *testEnv) seedTeamMember(t *testing.T, teamID, creatorID, memberID string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	e.exec(t, `INSERT INTO system_teams (id, name, status, created_by, created_at, updated_at)
		VALUES (?, ?, 'active', ?, ?, ?)`, teamID, "团队 "+teamID, creatorID, now, now)
	e.exec(t, `INSERT INTO system_team_members (id, team_id, system_account_id, status, joined_at, created_at, updated_at)
		VALUES (?, ?, ?, 'active', ?, ?, ?)`, "teammem-"+memberID, teamID, memberID, now, now, now)
}

// seedTeamMemberRow adds one more active member to an existing team.
func (e *testEnv) seedTeamMemberRow(t *testing.T, teamID, memberID string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	e.exec(t, `INSERT INTO system_team_members (id, team_id, system_account_id, status, joined_at, created_at, updated_at)
		VALUES (?, ?, ?, 'active', ?, ?, ?)`, "teammem-"+memberID, teamID, memberID, now, now, now)
}

func listItems(t *testing.T, payload map[string]any) map[string]map[string]any {
	t.Helper()
	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("missing list data: %v", payload)
	}
	rawItems, ok := data["items"].([]any)
	if !ok {
		t.Fatalf("missing list items: %v", data)
	}
	items := map[string]map[string]any{}
	for _, raw := range rawItems {
		item, _ := raw.(map[string]any)
		if id, ok := item["id"].(string); ok {
			items[id] = item
		}
	}
	return items
}

func TestMyAccountsAuthorizedInstanceVisibility(t *testing.T) {
	env, authzStore := newAuthorizedTestEnv(t)
	ownerID := env.login(t, "owner1", "owner-pass", "user")
	memberID := env.login(t, "member1", "member-pass", "user")
	env.login(t, "outsider1", "outsider-pass", "user")
	env.seedAccount(t, "acc-src", ownerID, "源账户", "active")
	env.seedTeamMember(t, "team-m10", ownerID, memberID)

	grant, err := authzStore.Create(context.Background(), authz.CreateInput{
		ResourceType: "account", ResourceID: "acc-src",
		GranteeType: "team", GranteeID: "team-m10",
	}, ownerID)
	if err != nil {
		t.Fatal(err)
	}
	runtimeID := env.queryCell(t, `SELECT id FROM resource_authorizations
		WHERE grantee_system_account_id = ? AND resource_id = 'acc-src'`, memberID)
	if runtimeID == "" {
		t.Fatal("team runtime row missing")
	}
	// The instance account is provisioned in the grantee's namespace with the
	// runtime row stamp (Node resource-authorization-write.repository.ts:1893).
	env.seedAuthorizationInstance(t, "acc-inst", memberID, runtimeID, "acc-src")

	// Member list: the instance shows with the authorized access type and the
	// restricted permission set; the source account itself stays hidden.
	env.login(t, "member1", "member-pass", "user")
	code, listed := env.do(t, http.MethodGet, "/__aisys__/api/my-accounts", "")
	if code != http.StatusOK {
		t.Fatalf("member list: %d %v", code, listed)
	}
	items := listItems(t, listed)
	instance, ok := items["acc-inst"]
	if !ok {
		t.Fatalf("authorized instance missing from member my-accounts: %v", items)
	}
	if instance["accessType"] != "authorized" {
		t.Fatalf("instance accessType: %v", instance["accessType"])
	}
	permissions := instance["permissions"].(map[string]any)
	if permissions["canUse"] != true || permissions["canEdit"] != false ||
		permissions["canViewCredentials"] != false || permissions["canDelete"] != false ||
		permissions["canLock"] != true {
		t.Fatalf("instance permissions: %v", permissions)
	}
	if _, leaked := items["acc-src"]; leaked {
		t.Fatal("source account must stay invisible to the member")
	}

	// Member detail: the instance resolves into the reserved 403 credentials
	// branch; the source account stays 404.
	code, detail := env.do(t, http.MethodGet, "/__aisys__/api/my-accounts/acc-inst", "")
	if code != http.StatusForbidden || detail["message"] != "无权查看账户凭据" {
		t.Fatalf("instance detail: %d %v", code, detail)
	}
	code, sourceDetail := env.do(t, http.MethodGet, "/__aisys__/api/my-accounts/acc-src", "")
	if code != http.StatusNotFound || sourceDetail["message"] != "账户不存在" {
		t.Fatalf("source detail: %d %v", code, sourceDetail)
	}

	// Outsider: neither the instance nor the source account is visible.
	env.login(t, "outsider1", "outsider-pass", "user")
	code, outsiderList := env.do(t, http.MethodGet, "/__aisys__/api/my-accounts", "")
	if code != http.StatusOK {
		t.Fatalf("outsider list: %d %v", code, outsiderList)
	}
	items = listItems(t, outsiderList)
	if _, visible := items["acc-inst"]; visible {
		t.Fatalf("unauthorized user must not see the instance: %v", items)
	}
	if _, visible := items["acc-src"]; visible {
		t.Fatalf("unauthorized user must not see the source account: %v", items)
	}

	// Owner view unchanged: the source account renders with owner semantics.
	env.login(t, "owner1", "owner-pass", "user")
	code, ownerList := env.do(t, http.MethodGet, "/__aisys__/api/my-accounts", "")
	if code != http.StatusOK {
		t.Fatalf("owner list: %d %v", code, ownerList)
	}
	items = listItems(t, ownerList)
	source, ok := items["acc-src"]
	if !ok || source["accessType"] != "owner" {
		t.Fatalf("owner source account: %v", source)
	}
	// The instance lives in the grantee namespace (Node
	// resource-authorization-write.repository.ts:1893), so the owner list
	// never contains it.
	if _, leaked := items["acc-inst"]; leaked {
		t.Fatalf("grantee-namespace instance must stay out of the owner list: %v", items)
	}

	// After the grant is revoked the member loses the instance.
	if _, err := authzStore.Revoke(context.Background(), grant.Item.ID, grant.Item.UpdatedAt, ownerID); err != nil {
		t.Fatal(err)
	}
	env.login(t, "member1", "member-pass", "user")
	code, memberList := env.do(t, http.MethodGet, "/__aisys__/api/my-accounts", "")
	if code != http.StatusOK {
		t.Fatalf("member list after revoke: %d %v", code, memberList)
	}
	items = listItems(t, memberList)
	if _, visible := items["acc-inst"]; visible {
		t.Fatalf("revoked authorization must hide the instance: %v", items)
	}
}

func TestAccountsAdminSurfaceIgnoresAuthorizedProjection(t *testing.T) {
	env, authzStore := newAuthorizedTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	memberID := env.login(t, "member2", "member-pass", "user")
	env.seedAccount(t, "acc-admin-src", adminID, "管理员账户", "active")
	env.seedTeamMember(t, "team-admin", adminID, memberID)

	if _, err := authzStore.Create(context.Background(), authz.CreateInput{
		ResourceType: "account", ResourceID: "acc-admin-src",
		GranteeType: "team", GranteeID: "team-admin",
	}, adminID); err != nil {
		t.Fatal(err)
	}
	runtimeID := env.queryCell(t, `SELECT id FROM resource_authorizations
		WHERE grantee_system_account_id = ? AND resource_id = 'acc-admin-src'`, memberID)
	// Instance namespace follows the Node production write: grantee namespace
	// (resource-authorization-write.repository.ts:1893).
	env.seedAuthorizationInstance(t, "acc-admin-inst", memberID, runtimeID, "acc-admin-src")

	// Admin surface (unscoped) sees every row; the authorized projection is a
	// self-surface concern and never rewrites admin rendering.
	env.login(t, "root", "root-pass", "super_admin")
	code, listed := env.do(t, http.MethodGet, "/__aisys__/api/accounts", "")
	if code != http.StatusOK {
		t.Fatalf("admin list: %d %v", code, listed)
	}
	items := listItems(t, listed)
	if _, ok := items["acc-admin-inst"]; !ok {
		t.Fatalf("admin must see the instance row: %v", items)
	}

	// The member still reads the instance through the authorized view.
	env.login(t, "member2", "member-pass", "user")
	code, memberList := env.do(t, http.MethodGet, "/__aisys__/api/my-accounts", "")
	if code != http.StatusOK {
		t.Fatalf("member list: %d %v", code, memberList)
	}
	items = listItems(t, memberList)
	item, ok := items["acc-admin-inst"]
	if !ok || item["accessType"] != "authorized" {
		t.Fatalf("member authorized instance: %v", item)
	}
}

// TestMyAccountsTeamMembersSeeOnlyOwnInstances pins the Node per-user fanout
// semantics: a team grant writes one runtime authorization row per member
// (resource-authorization-write.repository.ts:202-218) and each member's
// instance is stamped with that row inside the member's own namespace
// (:1880-1915), so a member's my-accounts list only ever contains his own
// instance. Revoking a member's runtime row (Node revoke only flips the
// authorization row status, :986-992) hides his stamped instance through the
// list status guard (account-management-list.repository.ts:331-334) without
// touching the other member.
func TestMyAccountsTeamMembersSeeOnlyOwnInstances(t *testing.T) {
	env, authzStore := newAuthorizedTestEnv(t)
	ownerID := env.login(t, "owner2", "owner-pass", "user")
	memberAID := env.login(t, "memberA", "member-pass", "user")
	memberBID := env.login(t, "memberB", "member-pass", "user")
	env.seedAccount(t, "acc-team-src", ownerID, "团队源账户", "active")
	env.seedTeamMember(t, "team-pair", ownerID, memberAID)
	env.seedTeamMemberRow(t, "team-pair", memberBID)

	if _, err := authzStore.Create(context.Background(), authz.CreateInput{
		ResourceType: "account", ResourceID: "acc-team-src",
		GranteeType: "team", GranteeID: "team-pair",
	}, ownerID); err != nil {
		t.Fatal(err)
	}
	runtimeA := env.queryCell(t, `SELECT id FROM resource_authorizations
		WHERE grantee_system_account_id = ? AND resource_id = 'acc-team-src'`, memberAID)
	runtimeB := env.queryCell(t, `SELECT id FROM resource_authorizations
		WHERE grantee_system_account_id = ? AND resource_id = 'acc-team-src'`, memberBID)
	if runtimeA == "" || runtimeB == "" {
		t.Fatalf("team fanout runtime rows missing: %q %q", runtimeA, runtimeB)
	}
	env.seedAuthorizationInstance(t, "acc-inst-a", memberAID, runtimeA, "acc-team-src")
	env.seedAuthorizationInstance(t, "acc-inst-b", memberBID, runtimeB, "acc-team-src")

	// Member A reads only his own instance; member B's instance and the source
	// account stay invisible.
	env.login(t, "memberA", "member-pass", "user")
	code, listed := env.do(t, http.MethodGet, "/__aisys__/api/my-accounts", "")
	if code != http.StatusOK {
		t.Fatalf("member A list: %d %v", code, listed)
	}
	items := listItems(t, listed)
	if _, ok := items["acc-inst-a"]; !ok {
		t.Fatalf("member A instance missing: %v", items)
	}
	if _, leaked := items["acc-inst-b"]; leaked {
		t.Fatalf("member A must not read member B's instance: %v", items)
	}
	if _, leaked := items["acc-team-src"]; leaked {
		t.Fatalf("source account must stay invisible to member A: %v", items)
	}

	// Member B is symmetric.
	env.login(t, "memberB", "member-pass", "user")
	code, listed = env.do(t, http.MethodGet, "/__aisys__/api/my-accounts", "")
	if code != http.StatusOK {
		t.Fatalf("member B list: %d %v", code, listed)
	}
	items = listItems(t, listed)
	if _, ok := items["acc-inst-b"]; !ok {
		t.Fatalf("member B instance missing: %v", items)
	}
	if _, leaked := items["acc-inst-a"]; leaked {
		t.Fatalf("member B must not read member A's instance: %v", items)
	}

	// Revoke member A's runtime row: A's stamped instance disappears from his
	// list while B keeps reading his own.
	env.exec(t, `UPDATE resource_authorizations SET status = 'revoked' WHERE id = ?`, runtimeA)
	env.login(t, "memberA", "member-pass", "user")
	code, listed = env.do(t, http.MethodGet, "/__aisys__/api/my-accounts", "")
	if code != http.StatusOK {
		t.Fatalf("member A list after revoke: %d %v", code, listed)
	}
	if _, visible := listItems(t, listed)["acc-inst-a"]; visible {
		t.Fatalf("revoked authorization must hide member A's instance: %v", listItems(t, listed))
	}
	env.login(t, "memberB", "member-pass", "user")
	code, listed = env.do(t, http.MethodGet, "/__aisys__/api/my-accounts", "")
	if code != http.StatusOK {
		t.Fatalf("member B list after A revoke: %d %v", code, listed)
	}
	if _, ok := listItems(t, listed)["acc-inst-b"]; !ok {
		t.Fatalf("member B instance must survive member A's revoke: %v", listItems(t, listed))
	}
}

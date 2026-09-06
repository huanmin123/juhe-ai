package groups

import (
	"net/http"
	"testing"
	"time"
)

// TestGroupAuthorizedOptionsAndEditBasicLocksIn mirrors the M05 deferral ①⑤
// authorized read branch: GET /options and GET /:id/edit-basic render the
// access_type='authorized' UNION arm of queryGroupRowsForAccess /
// findGroupEditRowForAccess — grantee-scoped resource_authorizations rows
// (status IN ('active','paused','expired'), owner rows excluded) joined to
// group_authorization_settings, with the authorizedGroupRowSelectColumns /
// groupEditAuthorizedSelectColumns overrides and the
// authorizedGroupPermissions(canBindAuthorizedGroupRowToApiKey(row))
// projection (isDefault forced false, runtime authorization columns, parsed
// limits document).
func TestGroupAuthorizedOptionsAndEditBasicLocksIn(t *testing.T) {
	env := newTestEnv(t)
	ownerID := env.login(t, "root", "root-pass", "super_admin")
	otherID := env.login(t, "carol", "carol-pass", "user")
	// alice last: the shared cookie jar must hold the grantee session for the
	// assertions below.
	granteeID := env.login(t, "alice", "alice-pass", "user")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	later := time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
	old := "2020-01-01T00:00:00.000Z"
	far := "2999-01-01T00:00:00.000Z"

	// The grantee's own group (owner arm of the union).
	env.exec(t, `INSERT INTO groups (id, system_account_id, name, provider_code, enabled, is_default, group_type, created_at, updated_at)
		VALUES ('grp-own', ?, 'Alice 自有组', 'openai', 1, 1, 'personal', ?, ?)`, granteeID, now, now)
	// The authorized group: owned by root, granted to alice, with a
	// per-grantee settings row disabling it (settings.enabled=0) and
	// overriding group_type back to personal.
	env.exec(t, `INSERT INTO groups (id, system_account_id, name, provider_code, enabled, is_default, group_type, scheduling_policy_json, created_at, updated_at)
		VALUES ('grp-auth', ?, 'Auth 授权组', 'anthropic', 1, 1, 'high_concurrency', ?, ?, ?)`, ownerID, storedPolicyJSON(t, map[string]any{"imageLaneMaxConcurrency": 7}), now, now)
	// An enabled grant with a past expiry: still listed (the status guard is
	// the only read filter) but cannot bind to an API key.
	env.exec(t, `INSERT INTO groups (id, system_account_id, name, provider_code, enabled, is_default, group_type, created_at, updated_at)
		VALUES ('grp-exp', ?, 'Expired 授权组', 'openai', 1, 0, 'personal', ?, ?)`, ownerID, now, now)

	env.exec(t, `INSERT INTO resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id, scope, status, effective_source_type, effective_source_team_id, expires_at, limits_json, created_by, created_at, updated_at)
		VALUES ('authz-1', 'group', 'grp-auth', ?, ?, 'use', 'active', 'team', 'team-1', ?, '{"daily":{"enabled":true,"limit":100}}', ?, ?, ?)`,
		ownerID, granteeID, far, ownerID, now, now)
	env.exec(t, `INSERT INTO resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id, scope, status, effective_source_type, expires_at, limits_json, created_by, created_at, updated_at)
		VALUES ('authz-2', 'group', 'grp-auth', ?, ?, 'use', 'revoked', 'manual', ?, NULL, ?, ?, ?)`,
		ownerID, otherID, far, ownerID, now, now)
	env.exec(t, `INSERT INTO resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id, scope, status, effective_source_type, expires_at, limits_json, created_by, created_at, updated_at)
		VALUES ('authz-3', 'group', 'grp-exp', ?, ?, 'use', 'active', 'manual', ?, NULL, ?, ?, ?)`,
		ownerID, granteeID, old, ownerID, now, now)
	env.exec(t, `INSERT INTO group_authorization_settings (authorization_id, system_account_id, group_id, enabled, group_type, scheduling_policy_json, created_at, updated_at)
		VALUES ('authz-1', ?, 'grp-auth', 0, 'personal', NULL, ?, ?)`, granteeID, now, now)

	// purpose=account renders the full union: own rows plus the authorized
	// projection; the revoked grant (authz-2) stays invisible to its grantee.
	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/my-groups/options?purpose=account", "")
	if code != http.StatusOK {
		t.Fatalf("grantee options: %d %v", code, payload)
	}
	items := payload["data"].([]any)
	rows := map[string]map[string]any{}
	for _, item := range items {
		row := item.(map[string]any)
		rows[row["id"].(string)] = row
	}
	if len(rows) != 3 {
		t.Fatalf("expected grp-own + 2 authorized rows, got %v", rows)
	}
	own := rows["grp-own"]
	if own["accessType"] != "owner" || own["isDefault"] != true {
		t.Fatalf("own row projection mismatch: %v", own)
	}
	if own["groupAuthorizationId"] != nil || own["authorizationStatus"] != nil {
		t.Fatalf("own row must not carry authorization columns: %v", own)
	}
	if limits := own["authorizationLimits"]; limits == nil || len(limits.(map[string]any)) != 0 {
		t.Fatalf("owner row limits must render the empty document: %v", own)
	}
	auth := rows["grp-auth"]
	if auth["accessType"] != "authorized" || auth["isDefault"] != false {
		t.Fatalf("authorized access/isDefault mismatch: %v", auth)
	}
	// Settings override: group enabled=1 but settings.enabled=0 → projected
	// disabled; group_type override drops the policy (personal → null).
	if auth["enabled"] != false || auth["groupType"] != "personal" || auth["schedulingPolicy"] != nil {
		t.Fatalf("authorized settings override mismatch: %v", auth)
	}
	if auth["ownerSystemAccountId"] != ownerID {
		t.Fatalf("authorized owner hydration mismatch: %v", auth)
	}
	if auth["groupAuthorizationId"] != "authz-1" || auth["authorizationStatus"] != "active" || auth["authorizationExpiresAt"] != far {
		t.Fatalf("authorized runtime columns mismatch: %v", auth)
	}
	limits := auth["authorizationLimits"].(map[string]any)
	daily := limits["daily"].(map[string]any)
	if daily["enabled"] != true || daily["limit"].(float64) != 100 {
		t.Fatalf("authorized limits document mismatch: %v", limits)
	}
	// canBindAuthorizedGroupRowToApiKey: projected enabled=0 → cannot bind.
	permissions := auth["permissions"].(map[string]any)
	if permissions["canUse"] != true || permissions["canEdit"] != true || permissions["canDelete"] != false ||
		permissions["canAuthorize"] != false || permissions["canViewCredentials"] != false ||
		permissions["canManageAccounts"] != false || permissions["canBindToApiKey"] != false {
		t.Fatalf("authorized permissions mismatch: %v", permissions)
	}
	exp := rows["grp-exp"]
	if exp["accessType"] != "authorized" || exp["enabled"] != true {
		t.Fatalf("expired-grant row projection mismatch: %v", exp)
	}
	if exp["permissions"].(map[string]any)["canBindToApiKey"] != false {
		t.Fatalf("expired grant must not bind: %v", exp)
	}

	// purpose=select keeps the {id,name} projection and includes authorized
	// rows.
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/my-groups/options?purpose=select", "")
	if code != http.StatusOK {
		t.Fatalf("grantee select options: %d %v", code, payload)
	}
	items = payload["data"].([]any)
	if len(items) != 3 {
		t.Fatalf("select union mismatch: %v", items)
	}

	// edit-basic renders the groupEditAuthorizedSelectColumns overrides.
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/my-groups/grp-auth/edit-basic", "")
	if code != http.StatusOK {
		t.Fatalf("authorized edit-basic: %d %v", code, payload)
	}
	edit := payload["data"].(map[string]any)
	if edit["enabled"] != false || edit["groupType"] != "personal" || edit["schedulingPolicy"] != nil {
		t.Fatalf("authorized edit overrides mismatch: %v", edit)
	}
	if edit["updatedAt"] != now {
		t.Fatalf("authorized edit updated_at must COALESCE settings.updated_at: %v", edit)
	}
	// The settings row carries a newer updated_at → wins the COALESCE.
	env.exec(t, `UPDATE group_authorization_settings SET enabled = 1, group_type = 'high_concurrency',
		scheduling_policy_json = ?, updated_at = ? WHERE authorization_id = 'authz-1'`,
		storedPolicyJSON(t, map[string]any{"maxQueueWaitMs": 90_000}), later)
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/my-groups/grp-auth/edit-basic", "")
	if code != http.StatusOK {
		t.Fatalf("authorized edit-basic after override: %d %v", code, payload)
	}
	edit = payload["data"].(map[string]any)
	if edit["enabled"] != true || edit["groupType"] != "high_concurrency" || edit["updatedAt"] != later {
		t.Fatalf("authorized edit override restore mismatch: %v", edit)
	}
	policy := edit["schedulingPolicy"].(map[string]any)
	if policy["maxQueueWaitMs"].(float64) != 90_000 {
		t.Fatalf("authorized edit policy override mismatch: %v", policy)
	}
	// A grantee-untouched field falls back to the group column: name.
	if edit["name"] != "Auth 授权组" || edit["providerCode"] != "anthropic" {
		t.Fatalf("authorized edit passthrough mismatch: %v", edit)
	}
	// With settings enabled again the bound permissions flip canBindToApiKey.
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/my-groups/options?purpose=account", "")
	if code != http.StatusOK {
		t.Fatalf("grantee options refresh: %d %v", code, payload)
	}
	for _, item := range payload["data"].([]any) {
		row := item.(map[string]any)
		if row["id"] == "grp-auth" && row["permissions"].(map[string]any)["canBindToApiKey"] != true {
			t.Fatalf("re-enabled grant must bind: %v", row)
		}
	}

	// Admin surface with a systemAccountId filter reads the same union for
	// the filtered account (grants TO alice, excluding alice's own rows).
	// Switch the shared jar back to root first (admin surface).
	env.do(t, http.MethodPost, "/__aisys__/api/auth/login", `{"username":"root","password":"root-pass"}`)
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/groups/options?purpose=account&systemAccountId="+granteeID, "")
	if code != http.StatusOK {
		t.Fatalf("admin filtered options: %d %v", code, payload)
	}
	items = payload["data"].([]any)
	sawAuthorized := false
	for _, item := range items {
		row := item.(map[string]any)
		if row["accessType"] == "authorized" {
			sawAuthorized = true
			if row["groupAuthorizationId"] != "authz-1" && row["groupAuthorizationId"] != "authz-3" {
				t.Fatalf("unexpected authorized grant under admin filter: %v", row)
			}
		}
	}
	if !sawAuthorized {
		t.Fatalf("admin filtered view must include authorized rows: %v", items)
	}

	// manageableOnly stays owner-arm-only (no authorized rows).
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/groups/options?purpose=account&systemAccountId="+granteeID+"&manageableOnly=1", "")
	if code != http.StatusOK {
		t.Fatalf("manageableOnly options: %d %v", code, payload)
	}
	for _, item := range payload["data"].([]any) {
		row := item.(map[string]any)
		if row["accessType"] != "owner" {
			t.Fatalf("manageableOnly must skip the authorized arm: %v", row)
		}
	}

	// A revoked grant never reaches its grantee's edit-basic either: switch
	// the shared jar to carol (authz-2's grantee).
	env.do(t, http.MethodPost, "/__aisys__/api/auth/login", `{"username":"carol","password":"carol-pass"}`)
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/my-groups/grp-auth/edit-basic", "")
	if code != http.StatusNotFound {
		t.Fatalf("revoked grantee edit-basic must 404: %d", code)
	}
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/my-groups/options?purpose=account", "")
	if code != http.StatusOK {
		t.Fatalf("carol options: %d %v", code, payload)
	}
	for _, item := range payload["data"].([]any) {
		row := item.(map[string]any)
		if row["accessType"] == "authorized" {
			t.Fatalf("revoked grant must stay invisible: %v", row)
		}
	}
}

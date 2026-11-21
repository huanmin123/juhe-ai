package groups

import (
	"net/http"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authz"
)

// findSinkEntry returns the recorded operation-log entry with the given
// module/action pair (the sink records synchronously at the route boundary).
func findSinkEntry(env *testEnv, module, action string) *authsys.OperationLogEntry {
	env.sink.mu.Lock()
	defer env.sink.mu.Unlock()
	for i := range env.sink.entries {
		entry := &env.sink.entries[i]
		if entry.Module == module && entry.Action == action {
			return entry
		}
	}
	return nil
}

// seedGroupAuthorization seeds the post-consent runtime/grant/source rows the
// return route consumes. The groups fixture intentionally keeps only the authz
// tables that package reads, so the offer/accept flow itself is covered in the
// authz package; here the rows already represent an accepted manual source.
func seedGroupAuthorization(t *testing.T, env *testEnv, groupID, ownerID, granteeID string) string {
	t.Helper()
	grantID := "grant-return-" + groupID
	runtimeID := "ra-return-" + groupID
	now := time.Now().UTC().Format(time.RFC3339Nano)
	env.exec(t, `INSERT INTO resource_authorizations
		(id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id,
		 status, effective_source_type, acceptance_mode, created_by, created_at, updated_at)
		VALUES (?, 'group', ?, ?, ?, 'active', 'manual', 'recipient', ?, ?, ?)`,
		runtimeID, groupID, ownerID, granteeID, ownerID, now, now)
	env.exec(t, `INSERT INTO resource_authorization_grants
		(id, resource_type, resource_id, resource_owner_system_account_id, grantee_type,
		 grantee_system_account_id, status, acceptance_mode, accepted_at, accepted_by, created_by, created_at, updated_at)
		VALUES (?, 'group', ?, ?, 'system_account', ?, 'active', 'recipient', ?, ?, ?, ?, ?)`,
		grantID, groupID, ownerID, granteeID, now, granteeID, ownerID, now, now)
	env.exec(t, `INSERT INTO resource_authorization_sources
		(id, authorization_id, grant_id, source_type, status, acceptance_mode, accepted_at, accepted_by,
		 created_by, created_at, updated_at)
		VALUES (?, ?, ?, 'manual', 'active', 'recipient', ?, ?, ?, ?, ?)`,
		"src-return-"+groupID, runtimeID, grantID, now, granteeID, ownerID, now, now)
	return grantID
}

// TestGroupsDeleteAuditTargetsLockedIn closes the M05 handover: the delete
// audit record carries the affected route-strategy sample targets next to the
// count metadata (Node groups.routes.ts delete log).
func TestGroupsDeleteAuditTargetsLockedIn(t *testing.T) {
	env := newTestEnv(t)
	ownerID := env.login(t, "owner", "owner-pass", "user")

	groupID, _ := env.createGroup(t, "/__aisys__/api/my-groups", "audited")
	// The BUG-0163 delete guard keeps the last enabled group of a bound
	// strategy alive: bind an alternative enabled group so both strategies
	// survive the delete as affected (not blocking).
	alternativeID, _ := env.createGroup(t, "/__aisys__/api/my-groups", "alternative")
	for _, strategyID := range []string{"rs-a", "rs-b"} {
		env.exec(t, `INSERT INTO route_strategies (id, system_account_id, name, status) VALUES (?, ?, ?, 'active')`,
			strategyID, ownerID, "策略 "+strategyID)
		env.bindRouteStrategy(t, strategyID, ownerID, groupID, "active")
		env.bindRouteStrategy(t, strategyID, ownerID, alternativeID, "active")
	}

	code, payload := env.do(t, http.MethodDelete, "/__aisys__/api/my-groups/"+groupID, "")
	if code != http.StatusNoContent {
		t.Fatalf("delete: %d %v", code, payload)
	}
	entry := findSinkEntry(env, "groups", "delete")
	if entry == nil {
		t.Fatal("delete operation log missing")
	}
	if len(entry.Targets) != 2 {
		t.Fatalf("delete targets must carry the affected route strategies: %+v", entry.Targets)
	}
	for index, target := range entry.Targets {
		wantID := []string{"rs-a", "rs-b"}[index]
		if target.TargetType != "route_strategy" || target.TargetID != wantID ||
			target.TargetName != "策略 "+wantID || target.Relation != "affected" ||
			target.TargetOwnerSystemAccountID != ownerID {
			t.Fatalf("target %d drift: %+v", index, target)
		}
	}
	for _, change := range entry.Changes {
		if change.Field != "deleted" {
			continue
		}
		// safeChange('deleted', '删除状态', false, true): booleans stay native.
		if change.BeforeValue != false || change.AfterValue != true {
			t.Fatalf("deleted change must carry native booleans: %+v", change)
		}
		if change.Before != "" || change.After != "" {
			t.Fatalf("deleted change must not fall back to strings: %+v", change)
		}
	}
}

// TestMyGroupsReturnAuthorization closes the M05 handover ③: the
// return-authorization route mounts through the authz return domain with the
// Node contract — 204 on the returned grant and the verbatim authorizations
// return audit record (targets + viewers).
func TestMyGroupsReturnAuthorization(t *testing.T) {
	env := newTestEnv(t)
	ownerID := env.login(t, "owner", "owner-pass", "user")
	groupID, group := env.createGroup(t, "/__aisys__/api/my-groups", "returnable")
	granteeID := env.login(t, "grantee", "grantee-pass", "user")
	grantID := seedGroupAuthorization(t, env, groupID, ownerID, granteeID)
	// The audit record carries the RUNTIME authorization id (Node
	// authorization.id = resource_authorizations.id), not the grant id.
	var runtimeAuthID string
	if err := env.queryRow(`SELECT id FROM resource_authorizations
		WHERE resource_type = 'group' AND resource_id = ? AND grantee_system_account_id = ?`,
		groupID, granteeID).Scan(&runtimeAuthID); err != nil {
		t.Fatal(err)
	}

	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/my-groups/"+groupID+"/return-authorization", "")
	if code != http.StatusNoContent {
		t.Fatalf("return authorization: %d %v", code, payload)
	}
	if len(payload) != 0 {
		t.Fatalf("204 must carry no body: %v", payload)
	}
	var grantStatus string
	if err := env.queryRow(`SELECT status FROM resource_authorization_grants WHERE id = ?`, grantID).Scan(&grantStatus); err != nil {
		t.Fatal(err)
	}
	if grantStatus != authz.StatusReturned {
		t.Fatalf("grant status after return: %s", grantStatus)
	}

	entry := findSinkEntry(env, "authorizations", "return")
	if entry == nil {
		t.Fatal("return operation log missing")
	}
	if entry.OperationKey != "groups.return_authorization" || entry.ResourceType != "authorization" {
		t.Fatalf("return log identity drift: %+v", entry)
	}
	if entry.ResourceID != runtimeAuthID || entry.ResourceName != group["name"].(string) {
		t.Fatalf("return log resource drift: %+v", entry)
	}
	if entry.Summary != "归还授权分组："+group["name"].(string) {
		t.Fatalf("return log summary drift: %q", entry.Summary)
	}
	if entry.OperationScopeSystemAccountID != granteeID || entry.Mode != "self" {
		t.Fatalf("return log scope drift: %+v", entry)
	}
	if len(entry.Changes) != 1 || entry.Changes[0].Field != "returned" ||
		entry.Changes[0].Label != "归还授权分组" || entry.Changes[0].BeforeValue != false || entry.Changes[0].AfterValue != true {
		t.Fatalf("return log change drift: %+v", entry.Changes)
	}
	if len(entry.Targets) != 2 {
		t.Fatalf("return log targets drift: %+v", entry.Targets)
	}
	ownerTarget, granteeTarget := entry.Targets[0], entry.Targets[1]
	if ownerTarget.TargetType != "group" || ownerTarget.TargetID != groupID ||
		ownerTarget.TargetName != group["name"].(string) ||
		ownerTarget.TargetOwnerSystemAccountID != ownerID || ownerTarget.Relation != "owner" {
		t.Fatalf("owner target drift: %+v", ownerTarget)
	}
	if granteeTarget.TargetType != "system_account" || granteeTarget.TargetID != granteeID ||
		granteeTarget.TargetOwnerSystemAccountID != granteeID || granteeTarget.Relation != "grantee" {
		t.Fatalf("grantee target drift: %+v", granteeTarget)
	}
	if len(entry.Viewers) != 2 ||
		entry.Viewers[0].SystemAccountID != ownerID || entry.Viewers[0].Reason != "authorization_owner" ||
		entry.Viewers[1].SystemAccountID != granteeID || entry.Viewers[1].Reason != "authorization_grantee" {
		t.Fatalf("return log viewers drift: %+v", entry.Viewers)
	}
}

// switchSession logs an existing account back in (env.login also creates the
// account, which fails on the second call for the same user).
func (e *testEnv) switchSession(t *testing.T, username, password string) {
	t.Helper()
	code, payload := e.do(t, http.MethodPost, "/__aisys__/api/auth/login",
		`{"username":"`+username+`","password":"`+password+`"}`)
	if code != http.StatusOK {
		t.Fatalf("switch session %s: %d %v", username, code, payload)
	}
}

// TestGroupsReturnAuthorizationContracts pins the remaining route contract:
// unknown groups and owner-self returns 404 verbatim, and the management
// return route is removed (404) so an admin can never return on behalf of the
// receiver. Each sub-case uses its own group so the mutation-guard failure
// window (Node mutationGuard semantics, 10s) cannot collapse them into 409s.
func TestGroupsReturnAuthorizationContracts(t *testing.T) {
	env := newTestEnv(t)
	ownerID := env.login(t, "owner", "owner-pass", "user")
	// All groups are created under the owner account while the owner session
	// is active; the later sub-cases switch sessions deliberately.
	ownGroup, _ := env.createGroup(t, "/__aisys__/api/my-groups", "owned")
	blankGroup, _ := env.createGroup(t, "/__aisys__/api/my-groups", "blank-scope")
	groupID, _ := env.createGroup(t, "/__aisys__/api/my-groups", "admin-returnable")
	granteeID := env.login(t, "grantee", "grantee-pass", "user")
	grantID := seedGroupAuthorization(t, env, groupID, ownerID, granteeID)
	env.login(t, "root", "root-pass", "super_admin")

	// Unknown group on the self surface (root scope) → 404 verbatim.
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/my-groups/grp_missing/return-authorization", "")
	if code != http.StatusNotFound || payload["message"] != "授权分组不存在或不可归还" {
		t.Fatalf("unknown group: %d %v", code, payload)
	}

	// The owner cannot "return" their own group (owner == grantee arm).
	env.switchSession(t, "owner", "owner-pass")
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/my-groups/"+ownGroup+"/return-authorization", "")
	if code != http.StatusNotFound || payload["message"] != "授权分组不存在或不可归还" {
		t.Fatalf("owner self return: %d %v", code, payload)
	}

	// Anonymous callers stay unauthorized (auth runs ahead of the guard).
	env.mu.Lock()
	env.jar = map[string]string{}
	env.mu.Unlock()
	code, _ = env.do(t, http.MethodPost, "/__aisys__/api/my-groups/"+ownGroup+"-anon/return-authorization", "")
	if code != http.StatusUnauthorized {
		t.Fatalf("anonymous return: %d", code)
	}
	// The management return route is removed: both a normal user and an admin
	// get 404, and nothing is written.
	env.switchSession(t, "grantee", "grantee-pass")
	code, _ = env.do(t, http.MethodPost, "/__aisys__/api/groups/"+blankGroup+"/return-authorization", "")
	if code != http.StatusNotFound {
		t.Fatalf("user on removed admin route: %d", code)
	}
	env.switchSession(t, "root", "root-pass")
	code, _ = env.do(t, http.MethodPost, "/__aisys__/api/groups/"+groupID+"/return-authorization?systemAccountId="+granteeID, "")
	if code != http.StatusNotFound {
		t.Fatalf("admin on removed admin route: %d", code)
	}
	var grantStatus string
	if err := env.queryRow(`SELECT status FROM resource_authorization_grants WHERE id = ?`, grantID).Scan(&grantStatus); err != nil {
		t.Fatal(err)
	}
	if grantStatus != authz.StatusActive {
		t.Fatalf("removed admin route must not mutate the grant: %s", grantStatus)
	}
	if entry := findSinkEntry(env, "authorizations", "return"); entry != nil {
		t.Fatalf("removed admin route must not write an audit record: %+v", entry)
	}
}

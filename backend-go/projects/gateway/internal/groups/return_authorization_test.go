package groups

import (
	"context"
	"net/http"
	"testing"

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

// seedGroupAuthorization creates a direct manual group grant through the same
// authz store the composition wires into groups.Deps.Authz.
func seedGroupAuthorization(t *testing.T, env *testEnv, groupID, ownerID, granteeID string) string {
	t.Helper()
	result, err := env.authz.Create(context.Background(), authz.CreateInput{
		ResourceType: "group", ResourceID: groupID,
		GranteeType: "system_account", GranteeID: granteeID,
	}, ownerID)
	if err != nil {
		t.Fatalf("authorize group: %v", err)
	}
	if !result.Created {
		t.Fatalf("authorize group must create the grant: %+v", result)
	}
	return result.Item.ID
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
	if err := env.db.QueryRow(`SELECT id FROM resource_authorizations
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
	if err := env.db.QueryRow(`SELECT status FROM resource_authorization_grants WHERE id = ?`, grantID).Scan(&grantStatus); err != nil {
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
// unknown groups and owner-self returns 404 verbatim, the admin surface keeps
// the scope-query gate and the admin filter acts as the grantee. Each
// sub-case uses its own group so the mutation-guard failure window (Node
// mutationGuard semantics, 10s) cannot collapse them into 409s.
func TestGroupsReturnAuthorizationContracts(t *testing.T) {
	env := newTestEnv(t)
	ownerID := env.login(t, "owner", "owner-pass", "user")
	// All groups are created under the owner account while the owner session
	// is active; the later sub-cases switch sessions deliberately.
	ownGroup, _ := env.createGroup(t, "/__aisys__/api/my-groups", "owned")
	blankGroup, _ := env.createGroup(t, "/__aisys__/api/my-groups", "blank-scope")
	groupID, group := env.createGroup(t, "/__aisys__/api/my-groups", "admin-returnable")
	granteeID := env.login(t, "grantee", "grantee-pass", "user")
	grantID := seedGroupAuthorization(t, env, groupID, ownerID, granteeID)
	rootID := env.login(t, "root", "root-pass", "super_admin")

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
	// The admin surface additionally requires the admin role (grantee session).
	env.switchSession(t, "grantee", "grantee-pass")
	code, _ = env.do(t, http.MethodPost, "/__aisys__/api/groups/"+ownGroup+"-admin-surface/return-authorization", "")
	if code != http.StatusForbidden && code != http.StatusUnauthorized {
		t.Fatalf("user on admin surface: %d", code)
	}
	// Restore the admin session for the remaining admin-surface cases.
	env.switchSession(t, "root", "root-pass")

	// Admin surface scope-query gate on a fresh group (blank explicit
	// systemAccountId is a 400 with the parseRequestScopeQuery text).
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/groups/"+blankGroup+"/return-authorization?systemAccountId=", "")
	if code != http.StatusBadRequest || payload["message"] != "系统账号 ID 不能为空" {
		t.Fatalf("blank scope query: %d %v", code, payload)
	}

	// Admin returning on behalf of the grantee (scope filter = grantee).
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/groups/"+groupID+"/return-authorization?systemAccountId="+granteeID, "")
	if code != http.StatusNoContent {
		t.Fatalf("admin return: %d %v", code, payload)
	}
	var grantStatus, revokedBy string
	if err := env.db.QueryRow(`SELECT status, COALESCE(revoked_by,'') FROM resource_authorization_grants WHERE id = ?`, grantID).Scan(&grantStatus, &revokedBy); err != nil {
		t.Fatal(err)
	}
	if grantStatus != authz.StatusReturned || revokedBy != rootID {
		t.Fatalf("admin return stamps: status=%s revokedBy=%s", grantStatus, revokedBy)
	}
	entry := findSinkEntry(env, "authorizations", "return")
	if entry == nil || entry.Mode != "admin" {
		t.Fatalf("admin return log mode drift: %+v", entry)
	}
	if entry.Summary != "归还授权分组："+group["name"].(string) {
		t.Fatalf("admin return summary drift: %q", entry.Summary)
	}
	if entry.OperationScopeSystemAccountID != granteeID {
		t.Fatalf("admin return scope drift: %+v", entry)
	}
}

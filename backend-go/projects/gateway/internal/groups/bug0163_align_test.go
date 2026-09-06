package groups

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// BUG-0163 alignment tests: each case pins one reviewed deviation between the
// archived Node groups surface and this package (pagination normalization,
// null rejection, scheduling-policy global max, stored-policy integrity,
// group-type integrity, PATCH deep-equality no-op, authorized binding counts,
// member predicates, authorized list/detail, option endpoints, delete audit
// metadata, postgres background dirty marking).

func TestGroupListPageSizeNormalization(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")
	for _, name := range []string{"p1", "p2", "p3"} {
		env.createGroup(t, "/__aisys__/api/groups", name)
	}

	// pageSize=0 clamps to 1 (Node Math.max(1, 0)) — the legacy Go fallback
	// rendered 20.
	_, payload := env.do(t, http.MethodGet, "/__aisys__/api/groups?pageSize=0", "")
	page := dataMap(t, payload)
	if page["pageSize"] != float64(1) || len(page["items"].([]any)) != 1 {
		t.Fatalf("pageSize=0: %v", page)
	}
	// pageSize=501 clamps to 500 — the legacy cap rendered 100.
	_, payload = env.do(t, http.MethodGet, "/__aisys__/api/groups?pageSize=501", "")
	page = dataMap(t, payload)
	if page["pageSize"] != float64(500) {
		t.Fatalf("pageSize=501: %v", page)
	}
	// A non-integer pageSize falls back to the Node default 50 — the legacy
	// fallback rendered 20.
	_, payload = env.do(t, http.MethodGet, "/__aisys__/api/groups?pageSize=abc", "")
	page = dataMap(t, payload)
	if page["pageSize"] != float64(50) {
		t.Fatalf("pageSize=abc: %v", page)
	}
	// An absent pageSize defaults to 50 and the page window clamps to
	// floor(1000/pageSize) = 20 (normalizeListPage).
	_, payload = env.do(t, http.MethodGet, "/__aisys__/api/groups?page=99999", "")
	page = dataMap(t, payload)
	if page["page"] != float64(20) || len(page["items"].([]any)) != 0 {
		t.Fatalf("page window clamp: %v", page)
	}
	// The hydrated list page always stamps generatedAt.
	if _, ok := page["generatedAt"].(string); !ok {
		t.Fatalf("generatedAt missing: %v", page)
	}
}

func TestGroupDefaultPolicyUsesInjectedGlobalMax(t *testing.T) {
	env := newTestEnv(t)
	store, err := NewStore(env.db, false, nil, nil, nil, WithGlobalConcurrencyMax(1234))
	if err != nil {
		t.Fatal(err)
	}
	item, err := store.Create(context.Background(), MutationInput{
		Name: ptrString("hc-global"), ProviderCode: ptrString("openai"),
		GroupType: ptrString(GroupTypeHighConcurrency),
	}, AccessScope{ViewerID: "owner-1"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	detail, err := store.FindDetail(context.Background(), item.ID, AccessScope{ViewerID: "owner-1"})
	if err != nil {
		t.Fatal(err)
	}
	policy := detail.SchedulingPolicy.(map[string]any)
	for _, key := range []string{"defaultSoftConcurrency", "maxQueueSize", "perApiKeyQueueLimit"} {
		if policy[key] != 1234 {
			t.Fatalf("policy %s = %v, want the injected global max 1234", key, policy[key])
		}
	}
	// Stores built without the option keep the 5000 default.
	legacy, err := NewStore(env.db, false, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if legacy.globalConcurrencyMax() != 5000 {
		t.Fatalf("legacy default: %d", legacy.globalConcurrencyMax())
	}
}

func TestGroupCorruptedStoredPolicyAndTypeAreReadErrors(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	// high_concurrency with a broken policy document (missing keys).
	env.exec(t, `INSERT INTO groups (id, system_account_id, name, provider_code, enabled, is_default, group_type, scheduling_policy_json, created_at, updated_at)
		VALUES ('grp-broken-policy', ?, '坏策略', 'openai', 1, 0, 'high_concurrency', '{"mode":"balanced_fast"}', ?, ?)`, adminID, now, now)
	// high_concurrency with a NULL policy column.
	env.exec(t, `INSERT INTO groups (id, system_account_id, name, provider_code, enabled, is_default, group_type, scheduling_policy_json, created_at, updated_at)
		VALUES ('grp-null-policy', ?, '空策略', 'openai', 1, 0, 'high_concurrency', NULL, ?, ?)`, adminID, now, now)
	// An unknown stored group_type (Node normalizeGroupType throws).
	env.exec(t, `INSERT INTO groups (id, system_account_id, name, provider_code, enabled, is_default, group_type, created_at, updated_at)
		VALUES ('grp-weird-type', ?, '怪类型', 'openai', 1, 0, 'weird_type', ?, ?)`, adminID, now, now)
	// A personal group without a policy stays readable.
	env.exec(t, `INSERT INTO groups (id, system_account_id, name, provider_code, enabled, is_default, group_type, created_at, updated_at)
		VALUES ('grp-ok', ?, '正常组', 'openai', 1, 0, 'personal', ?, ?)`, adminID, now, now)

	for _, id := range []string{"grp-broken-policy", "grp-null-policy"} {
		if code, payload := env.do(t, http.MethodGet, "/__aisys__/api/groups/"+id, ""); code != http.StatusInternalServerError {
			t.Fatalf("%s detail must 500 on the corrupted policy: %d %v", id, code, payload)
		}
		if code, payload := env.do(t, http.MethodGet, "/__aisys__/api/groups/"+id+"/edit-basic", ""); code != http.StatusInternalServerError {
			t.Fatalf("%s edit-basic must 500 on the corrupted policy: %d %v", id, code, payload)
		}
	}
	if code, payload := env.do(t, http.MethodGet, "/__aisys__/api/groups", ""); code != http.StatusInternalServerError {
		t.Fatalf("list must 500 while a stored policy is corrupted: %d %v", code, payload)
	}
	if code, payload := env.do(t, http.MethodGet, "/__aisys__/api/groups/grp-ok", ""); code != http.StatusOK {
		t.Fatalf("personal group must stay readable: %d %v", code, payload)
	}

	// PATCH renders the corrupted type through the 400 branch (分组类型无效);
	// like Node, the stored type is only normalized when the patch touches
	// groupType/schedulingPolicy.
	code, payload := env.do(t, http.MethodPatch, "/__aisys__/api/groups/grp-weird-type",
		`{"expectedUpdatedAt":"`+now+`","groupType":"personal"}`)
	if code != http.StatusBadRequest || payload["message"] != "分组类型无效" {
		t.Fatalf("corrupted group type patch: %d %v", code, payload)
	}
}

func TestGroupPatchPolicyDeepEqualityNoop(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/groups",
		`{"name":"deep-eq","providerCode":"openai","groupType":"high_concurrency","schedulingPolicy":{"defaultSoftConcurrency":100}}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, created)
	}
	groupID := dataMap(t, created)["id"].(string)

	// Rewrite the stored document with a different key order, extra
	// whitespace and a textual exponent — the same semantic policy (Node
	// isDeepStrictEqual → no-op; the legacy Go compare saw different bytes).
	reordered := `{"imageLaneMaxConcurrency":0,  "clientIpConcurrencyOverflowMode":"reject",
		"clientIpConcurrencyLimit":0, "perApiKeyQueueLimit":5000, "maxQueueSize":5000,
		"maxQueueWaitMs":60000, "recentTimeoutPenaltyThreshold":2, "recentTimeoutWindowSeconds":120,
		"firstOutputSlowThresholdMs":15000, "slowRequestThresholdMs":30000,
		"breakAffinityOnQueueWaitMs":0, "breakAffinityOnSoftLimit":true, "fallbackOnQueueEnabled":true,
		"fastFirstEnabled":true, "mode":"balanced_fast", "defaultSoftConcurrency":100}`
	env.exec(t, `UPDATE groups SET scheduling_policy_json = ? WHERE id = ?`, reordered, groupID)

	detailCode, detail := env.do(t, http.MethodGet, "/__aisys__/api/groups/"+groupID, "")
	if detailCode != http.StatusOK {
		t.Fatalf("detail: %d %v", detailCode, detail)
	}
	updatedAt := dataMap(t, detail)["updatedAt"].(string)

	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/groups/"+groupID,
		`{"expectedUpdatedAt":"`+updatedAt+`","schedulingPolicy":{"defaultSoftConcurrency":100,"maxQueueWaitMs":60000}}`)
	if code != http.StatusOK {
		t.Fatalf("noop patch: %d %v", code, patched)
	}
	patchData := dataMap(t, patched)
	if fields := patchData["changedFields"].([]any); len(fields) != 0 {
		t.Fatalf("key-order-only policy input must be a no-op: %v", patchData["changedFields"])
	}
	if patchData["updatedAt"].(string) != updatedAt {
		t.Fatalf("noop patch must not bump updatedAt: %v", patchData["updatedAt"])
	}
	// The stored text stays untouched on a no-op.
	var stored string
	if err := env.db.QueryRow(`SELECT scheduling_policy_json FROM groups WHERE id = ?`, groupID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != reordered {
		t.Fatalf("noop patch must not rewrite the stored document")
	}
	// A real change still lands.
	code, changed := env.do(t, http.MethodPatch, "/__aisys__/api/groups/"+groupID,
		`{"expectedUpdatedAt":"`+updatedAt+`","schedulingPolicy":{"defaultSoftConcurrency":200}}`)
	if code != http.StatusOK {
		t.Fatalf("real patch: %d %v", code, changed)
	}
	fields := dataMap(t, changed)["changedFields"].([]any)
	if len(fields) != 1 || fields[0] != "schedulingPolicy" {
		t.Fatalf("real patch changedFields: %v", fields)
	}
}

func TestGroupAvailabilityGuardCountsAuthorizedGroups(t *testing.T) {
	env := newTestEnv(t)
	ownerID := env.login(t, "root", "root-pass", "super_admin")
	userAID := env.login(t, "alice", "alice-pass", "user")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	later := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339Nano)
	far := "2999-01-01T00:00:00.000Z"

	// alice owns the strategy rs-auth and her only own enabled group;
	// root's group is bound to the same strategy and granted to alice.
	env.exec(t, `INSERT INTO groups (id, system_account_id, name, provider_code, enabled, is_default, group_type, created_at, updated_at)
		VALUES ('grp-alice', ?, 'Alice 唯一组', 'openai', 1, 0, 'personal', ?, ?)`, userAID, now, now)
	env.exec(t, `INSERT INTO groups (id, system_account_id, name, provider_code, enabled, is_default, group_type, created_at, updated_at)
		VALUES ('grp-granted', ?, '授权组', 'openai', 1, 0, 'personal', ?, ?)`, ownerID, now, now)
	env.exec(t, `INSERT INTO route_strategies (id, system_account_id, name, status) VALUES ('rs-auth', ?, '授权策略', 'active')`, userAID)
	env.bindRouteStrategy(t, "rs-auth", userAID, "grp-alice", "active")
	env.bindRouteStrategy(t, "rs-auth", userAID, "grp-granted", "active")
	env.exec(t, `INSERT INTO resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id, scope, status, expires_at, created_by, created_at, updated_at)
		VALUES ('authz-guard', 'group', 'grp-granted', ?, ?, 'use', 'active', ?, ?, ?, ?)`,
		ownerID, userAID, far, ownerID, now, now)

	// The authorized group is the strategy's remaining usable binding, so
	// disabling alice's own group must succeed (the legacy owner-only count
	// refused it as the "only" group).
	code, payload := env.do(t, http.MethodPatch, "/__aisys__/api/my-groups/grp-alice",
		`{"expectedUpdatedAt":"`+now+`","enabled":false}`)
	if code != http.StatusOK {
		t.Fatalf("authorized remainder must unblock the disable: %d %v", code, payload)
	}

	// Re-enable, then break the authorized remainder: a disabled settings row
	// drops the count back to zero and the guard fires again.
	env.exec(t, `UPDATE groups SET enabled = 1 WHERE id = 'grp-alice'`)
	env.exec(t, `UPDATE groups SET updated_at = ? WHERE id = 'grp-alice'`, later)
	env.exec(t, `INSERT INTO group_authorization_settings (authorization_id, system_account_id, group_id, enabled, group_type, created_at, updated_at)
		VALUES ('authz-guard', ?, 'grp-granted', 0, 'personal', ?, ?)`, userAID, now, now)
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/my-groups/grp-alice",
		`{"expectedUpdatedAt":"`+later+`","enabled":false}`)
	if code != http.StatusBadRequest || !strings.Contains(payload["message"].(string), "唯一可用启用分组") {
		t.Fatalf("disabled authorized settings must restore the guard: %d %v", code, payload)
	}
	// An expired grant counts for nothing either.
	env.exec(t, `DELETE FROM group_authorization_settings WHERE authorization_id = 'authz-guard'`)
	env.exec(t, `UPDATE resource_authorizations SET expires_at = '2020-01-01T00:00:00.000Z' WHERE id = 'authz-guard'`)
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/my-groups/grp-alice",
		`{"expectedUpdatedAt":"`+later+`","enabled":false}`)
	if code != http.StatusBadRequest || !strings.Contains(payload["message"].(string), "唯一可用启用分组") {
		t.Fatalf("expired grant must restore the guard: %d %v", code, payload)
	}
}

func TestGroupMemberPredicateHonorsAuthorizationRows(t *testing.T) {
	env := newTestEnv(t)
	ownerID := env.login(t, "root", "root-pass", "super_admin")
	granteeID := env.login(t, "alice", "alice-pass", "user")
	env.do(t, http.MethodPost, "/__aisys__/api/auth/login", `{"username":"root","password":"root-pass"}`)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	far := "2999-01-01T00:00:00.000Z"

	env.exec(t, `INSERT INTO groups (id, system_account_id, name, provider_code, enabled, is_default, group_type, created_at, updated_at)
		VALUES ('grp-members', ?, '成员组', 'openai', 1, 0, 'personal', ?, ?)`, ownerID, now, now)
	// Own account: always counts.
	seedAccountWithBinding(t, env, ownerID, "grp-members", "acc-own", "", now)
	// Cross-owner account with an active grant binding: counts.
	env.exec(t, `INSERT INTO resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id, scope, status, expires_at, created_by, created_at, updated_at)
		VALUES ('authz-m-active', 'group', 'grp-members', ?, ?, 'use', 'active', ?, ?, ?, ?)`,
		ownerID, granteeID, far, ownerID, now, now)
	seedAccountWithBinding(t, env, granteeID, "grp-members", "acc-granted", "authz-m-active", now)
	// Cross-owner account bound through a revoked grant: must not count.
	seedAccountWithBinding(t, env, granteeID, "grp-members", "acc-revoked", "authz-m-revoked", now)
	// Cross-owner account without any grant binding: must not count.
	seedAccountWithBinding(t, env, granteeID, "grp-members", "acc-orphan", "", now)

	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/groups/grp-members", "")
	if code != http.StatusOK {
		t.Fatalf("detail: %d %v", code, payload)
	}
	accountIDs := dataMap(t, payload)["accountIds"].([]any)
	// Same-timestamp bindings fall back to the account_id ASC tiebreak
	// (Node ORDER BY created_at ASC, account_id ASC); revoked and un-granted
	// cross-owner bindings never count.
	if len(accountIDs) != 2 || accountIDs[0] != "acc-granted" || accountIDs[1] != "acc-own" {
		t.Fatalf("member predicate mismatch: %v", accountIDs)
	}
}

func seedAccountWithBinding(t *testing.T, env *testEnv, ownerID, groupID, accountID, authorizationID, now string) {
	t.Helper()
	env.exec(t, `INSERT INTO accounts (id, system_account_id, deleted_at, created_at) VALUES (?, ?, NULL, ?)`, accountID, ownerID, now)
	if authorizationID == "" {
		env.exec(t, `INSERT INTO group_accounts (system_account_id, group_id, account_id, account_authorization_id, enabled, created_at, updated_at)
			VALUES (?, ?, ?, NULL, 1, ?, ?)`, ownerID, groupID, accountID, now, now)
		return
	}
	env.exec(t, `INSERT INTO group_accounts (system_account_id, group_id, account_id, account_authorization_id, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, 1, ?, ?)`, ownerID, groupID, accountID, authorizationID, now, now)
}

func TestGroupAuthorizedListAndDetailAndOptionEndpoints(t *testing.T) {
	env := newTestEnv(t)
	ownerID := env.login(t, "root", "root-pass", "super_admin")
	granteeID := env.login(t, "alice", "alice-pass", "user")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	far := "2999-01-01T00:00:00.000Z"

	env.exec(t, `INSERT INTO groups (id, system_account_id, name, provider_code, enabled, is_default, group_type, created_at, updated_at)
		VALUES ('grp-visible', ?, '授权可见组', 'openai', 1, 1, 'personal', ?, ?)`, ownerID, now, now)
	env.exec(t, `INSERT INTO resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id, scope, status, expires_at, limits_json, created_by, created_at, updated_at)
		VALUES ('authz-view', 'group', 'grp-visible', ?, ?, 'use', 'active', ?, '{"total":{"enabled":true,"limit":5}}', ?, ?, ?)`,
		ownerID, granteeID, far, ownerID, now, now)
	env.exec(t, `INSERT INTO resource_authorization_sources (id, authorization_id, source_type, source_team_id, status, created_by, created_at, updated_at)
		VALUES ('src-1', 'authz-view', 'manual', NULL, 'active', ?, ?, ?)`, ownerID, now, now)
	seedAccountWithBinding(t, env, ownerID, "grp-visible", "acc-hidden", "", now)

	// List: the grantee sees the authorized row (legacy owner-only list hid
	// it entirely).
	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/my-groups", "")
	if code != http.StatusOK {
		t.Fatalf("grantee list: %d %v", code, payload)
	}
	items := dataMap(t, payload)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("grantee list must contain the authorized group: %v", payload)
	}
	item := items[0].(map[string]any)
	if item["accessType"] != "authorized" || item["isDefault"] != false || item["canDelete"] != false ||
		item["groupAuthorizationId"] != "authz-view" || item["authorizationStatus"] != "active" {
		t.Fatalf("authorized list row mismatch: %v", item)
	}
	summary := item["authorizationSourceSummary"].(map[string]any)
	if summary["activeSourceCount"] != float64(1) || summary["hasManual"] != true {
		t.Fatalf("authorization source summary mismatch: %v", summary)
	}

	// Detail: previously a 404, now the authorized GroupSummary projection.
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/my-groups/grp-visible", "")
	if code != http.StatusOK {
		t.Fatalf("authorized detail: %d %v", code, payload)
	}
	detail := dataMap(t, payload)
	if detail["accessType"] != "authorized" || detail["accountIds"] == nil || len(detail["accountIds"].([]any)) != 0 {
		t.Fatalf("authorized detail projection mismatch: %v", detail)
	}
	if detail["permissions"].(map[string]any)["canDelete"] != false {
		t.Fatalf("authorized permissions mismatch: %v", detail["permissions"])
	}
	// The authorized viewer receives the sanitized sources (createdBy blank).
	sources := detail["authorizationSources"].([]any)
	if len(sources) != 1 || sources[0].(map[string]any)["createdBy"] != "" {
		t.Fatalf("sanitized sources mismatch: %v", sources)
	}
	limits := detail["authorizationLimits"].(map[string]any)
	if limits["total"].(map[string]any)["limit"].(float64) != 5 {
		t.Fatalf("authorization limits mismatch: %v", limits)
	}

	// authorization-options projects {id,name,canAuthorize}.
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/my-groups/authorization-options", "")
	if code != http.StatusOK {
		t.Fatalf("authorization-options: %d %v", code, payload)
	}
	options := payload["data"].([]any)
	if len(options) != 1 || options[0].(map[string]any)["id"] != "grp-visible" ||
		options[0].(map[string]any)["canAuthorize"] != false {
		t.Fatalf("authorization-options mismatch: %v", options)
	}

	// account-options hides the owner's account ids on the authorized view.
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/my-groups/account-options", "")
	if code != http.StatusOK {
		t.Fatalf("account-options: %d %v", code, payload)
	}
	optionRows := payload["data"].([]any)
	if len(optionRows) != 1 {
		t.Fatalf("account-options rows: %v", payload)
	}
	if accountIDs := optionRows[0].(map[string]any)["accountIds"].([]any); len(accountIDs) != 0 {
		t.Fatalf("authorized account-options must hide accountIds: %v", optionRows[0])
	}
	// The owner view lists them.
	env.do(t, http.MethodPost, "/__aisys__/api/auth/login", `{"username":"root","password":"root-pass"}`)
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/groups/account-options?ids=grp-visible", "")
	if code != http.StatusOK {
		t.Fatalf("owner account-options: %d %v", code, payload)
	}
	ownerRows := payload["data"].([]any)
	accountIDs := ownerRows[0].(map[string]any)["accountIds"].([]any)
	if len(accountIDs) != 1 || accountIDs[0] != "acc-hidden" {
		t.Fatalf("owner account-options must list member accounts: %v", ownerRows[0])
	}

	// route-strategy-options reflects the settings override of enabled.
	// Switch the shared jar back to the grantee first.
	env.do(t, http.MethodPost, "/__aisys__/api/auth/login", `{"username":"alice","password":"alice-pass"}`)
	env.exec(t, `INSERT INTO group_authorization_settings (authorization_id, system_account_id, group_id, enabled, group_type, created_at, updated_at)
		VALUES ('authz-view', ?, 'grp-visible', 0, 'personal', ?, ?)`, granteeID, now, now)
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/my-groups/route-strategy-options", "")
	if code != http.StatusOK {
		t.Fatalf("route-strategy-options: %d %v", code, payload)
	}
	strategyRows := payload["data"].([]any)
	if len(strategyRows) != 1 {
		t.Fatalf("route-strategy-options rows: %v", payload)
	}
	row := strategyRows[0].(map[string]any)
	if row["id"] != "grp-visible" || row["providerCode"] != "openai" || row["enabled"] != false {
		t.Fatalf("route-strategy-options override mismatch: %v", row)
	}
}

func TestGroupDeleteAuditMetadata(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	groupID, _ := env.createGroup(t, "/__aisys__/api/groups", "audited")
	survivorID, _ := env.createGroup(t, "/__aisys__/api/groups", "audited-survivor")
	env.exec(t, `INSERT INTO route_strategies (id, system_account_id, name, status) VALUES ('rs-audit', ?, '审计策略', 'active')`, adminID)
	env.bindRouteStrategy(t, "rs-audit", adminID, groupID, "active")
	env.bindRouteStrategy(t, "rs-audit", adminID, survivorID, "active")

	code, _ := env.do(t, http.MethodDelete, "/__aisys__/api/groups/"+groupID, "")
	if code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	env.sink.mu.Lock()
	defer env.sink.mu.Unlock()
	var metadata json.RawMessage
	for _, entry := range env.sink.entries {
		if entry.Module == "groups" && entry.Action == "delete" && entry.ResourceID == groupID {
			metadata = entry.Metadata
		}
	}
	if metadata == nil {
		t.Fatal("delete audit entry must carry the affected-route-strategy metadata")
	}
	document := map[string]any{}
	if err := json.Unmarshal(metadata, &document); err != nil {
		t.Fatalf("metadata must be a JSON document: %v", err)
	}
	if document["affectedRouteStrategyCount"] != float64(1) {
		t.Fatalf("metadata count mismatch: %v", document)
	}
	samples := document["affectedRouteStrategies"].([]any)
	if len(samples) != 1 || samples[0].(map[string]any)["routeStrategyId"] != "rs-audit" {
		t.Fatalf("metadata samples mismatch: %v", samples)
	}
}

func TestGroupRefreshStatsAfterWriteNeverFailsTheRequestOnPostgres(t *testing.T) {
	// A closed handle fails every statement; the SQLite path surfaces the
	// error while the PostgreSQL path hands the write to the background task
	// (postgres_group_account_stats_dirty_mark_failed log only).
	db, err := sql.Open("sqlite", "file:closed-groups?mode=memory")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	pgStore, err := NewStore(db, true, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := pgStore.refreshStatsAfterWrite(context.Background(), []string{"grp-x"}, "group_deleted"); err != nil {
		t.Fatalf("postgres refresh must degrade to the background log: %v", err)
	}
	sqliteStore, err := NewStore(db, false, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := sqliteStore.refreshStatsAfterWrite(context.Background(), []string{"grp-x"}, "group_deleted"); err == nil {
		t.Fatal("sqlite refresh must surface the failure synchronously")
	}
}

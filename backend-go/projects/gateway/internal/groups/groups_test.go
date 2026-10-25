package groups

// M05 主链路测试（admin/my-groups 路由族全量 HTTP 契约）。夹具
// newTestEnv 在 pgkit_test.go：pgtestkit 独立 PG 测试库 + 生产 PG 臂装配
// （PLAN-20261024T051925945Z 测试迁移切片），测试体断言逐字节保持。

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestGroupAdminLifecycle(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")

	// Create (201) with contract fields.
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/groups",
		`{"name":"alpha","providerCode":"openai","description":"首个分组"}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, payload)
	}
	created := dataMap(t, payload)
	alphaID := created["id"].(string)
	// providerCode 为兼容入参：响应键保留但恒空串（供应商解耦设计 §8）。
	if created["name"] != "alpha" || created["providerCode"] != "" ||
		created["isDefault"] != false || created["canEdit"] != true || created["canDelete"] != true ||
		created["groupType"] != "personal" || created["accessType"] != "owner" ||
		created["ownerSystemAccountId"] != adminID || created["canReturn"] != false {
		t.Fatalf("create payload: %v", created)
	}
	// The create list-item projection carries the eight counter accountStats
	// only (no todayUsage/usage keys) and no memberCount/accountIds.
	createStats := created["accountStats"].(map[string]any)
	if len(createStats) != 8 || createStats["currentConcurrency"] != float64(0) {
		t.Fatalf("create accountStats: %v", createStats)
	}
	if _, present := created["memberCount"]; present {
		t.Fatalf("create payload must not carry memberCount: %v", created)
	}
	if _, present := created["schedulingPolicy"]; present {
		t.Fatalf("personal group list item must omit schedulingPolicy: %v", created["schedulingPolicy"])
	}

	// Idempotent create → 409 (dedup guard).
	code, duplicate := env.do(t, http.MethodPost, "/__aisys__/api/groups",
		`{"name":"alpha","providerCode":"openai","description":"首个分组"}`)
	if code != http.StatusConflict {
		t.Fatalf("idempotent create: %d %v", code, duplicate)
	}

	// Second group + rename onto an existing name → store-level 409.
	bravoID, _ := env.createGroup(t, "/__aisys__/api/groups", "bravo")
	bravoDetailCode, bravoDetail := env.do(t, http.MethodGet, "/__aisys__/api/groups/"+bravoID, "")
	if bravoDetailCode != 200 {
		t.Fatalf("bravo detail: %d %v", bravoDetailCode, bravoDetail)
	}
	bravoUpdatedAt := dataMap(t, bravoDetail)["updatedAt"].(string)
	code, renamed := env.do(t, http.MethodPatch, "/__aisys__/api/groups/"+bravoID,
		`{"expectedUpdatedAt":"`+bravoUpdatedAt+`","name":"alpha"}`)
	if code != http.StatusConflict || !strings.Contains(renamed["message"].(string), "同名分组已存在") {
		t.Fatalf("rename onto existing name: %d %v", code, renamed)
	}

	// providerCode 兼容入参（接受但忽略，供应商解耦设计 §8）：未知/停用
	// provider 不再校验，create 正常成功且响应 providerCode 恒空串。
	code, ignoredProvider := env.do(t, http.MethodPost, "/__aisys__/api/groups",
		`{"name":"gamma","providerCode":"missing"}`)
	if code != http.StatusCreated || dataMap(t, ignoredProvider)["providerCode"] != "" {
		t.Fatalf("ignored provider create: %d %v", code, ignoredProvider)
	}

	// High-concurrency group carries a defaulted policy.
	code, hc := env.do(t, http.MethodPost, "/__aisys__/api/groups",
		`{"name":"hc","providerCode":"openai","groupType":"high_concurrency","schedulingPolicy":{"defaultSoftConcurrency":100}}`)
	if code != http.StatusCreated {
		t.Fatalf("hc create: %d %v", code, hc)
	}
	hcID := dataMap(t, hc)["id"].(string)
	code, hcDetail := env.do(t, http.MethodGet, "/__aisys__/api/groups/"+hcID, "")
	policy := dataMap(t, hcDetail)["schedulingPolicy"].(map[string]any)
	if policy["defaultSoftConcurrency"] != float64(100) || policy["mode"] != "balanced_fast" || policy["maxQueueWaitMs"] != float64(60_000) {
		t.Fatalf("hc policy: %v", policy)
	}

	// Pagination + keyword prefix (alpha, bravo, gamma, hc).
	code, page1 := env.do(t, http.MethodGet, "/__aisys__/api/groups?page=1&pageSize=2", "")
	page1Data := dataMap(t, page1)
	if code != 200 || len(page1Data["items"].([]any)) != 2 || page1Data["hasMore"] != true || page1Data["total"] != float64(3) {
		t.Fatalf("page 1: %d %v", code, page1)
	}
	code, page2 := env.do(t, http.MethodGet, "/__aisys__/api/groups?page=2&pageSize=2", "")
	page2Data := dataMap(t, page2)
	if code != 200 || len(page2Data["items"].([]any)) != 2 || page2Data["hasMore"] != false || page2Data["total"] != float64(4) {
		t.Fatalf("page 2: %d %v", code, page2)
	}
	code, filtered := env.do(t, http.MethodGet, "/__aisys__/api/groups?keyword=al", "")
	filteredItems := dataMap(t, filtered)["items"].([]any)
	if code != 200 || len(filteredItems) != 1 || filteredItems[0].(map[string]any)["name"] != "alpha" {
		t.Fatalf("keyword filter: %d %v", code, filtered)
	}

	// Detail + members.
	env.bindAccount(t, adminID, alphaID, "acc-1", "active")
	env.bindAccount(t, adminID, alphaID, "acc-2", "active")
	code, detail := env.do(t, http.MethodGet, "/__aisys__/api/groups/"+alphaID, "")
	detailData := dataMap(t, detail)
	if code != 200 {
		t.Fatalf("detail: %d %v", code, detail)
	}
	accountIDs := detailData["accountIds"].([]any)
	if len(accountIDs) != 2 || accountIDs[0] != "acc-1" {
		t.Fatalf("accountIds: %v", accountIDs)
	}
	// The owner summary overrides accountStats.total with the member count
	// (Node buildGroupSummaries includeAccountIds branch).
	if detailData["accountStats"].(map[string]any)["total"] != float64(2) {
		t.Fatalf("detail accountStats.total: %v", detailData["accountStats"])
	}
	if _, present := detailData["memberCount"]; present {
		t.Fatalf("detail payload must not carry memberCount: %v", detailData)
	}
	if detailData["permissions"].(map[string]any)["canDelete"] != true {
		t.Fatalf("owner permissions: %v", detailData["permissions"])
	}

	// Optimistic locking.
	updatedAt := detailData["updatedAt"].(string)
	code, stale := env.do(t, http.MethodPatch, "/__aisys__/api/groups/"+alphaID,
		`{"expectedUpdatedAt":"2000-01-01T00:00:00.000Z","name":"alpha-renamed"}`)
	if code != http.StatusConflict || stale["message"] != "分组已被其他操作更新，请刷新后重试" {
		t.Fatalf("stale patch: %d %v", code, stale)
	}
	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/groups/"+alphaID,
		`{"expectedUpdatedAt":"`+updatedAt+`","name":"alpha-renamed","description":"更新说明","groupType":"high_concurrency"}`)
	if code != 200 {
		t.Fatalf("fresh patch: %d %v", code, patched)
	}
	patchData := dataMap(t, patched)
	changed := map[string]bool{}
	for _, field := range patchData["changedFields"].([]any) {
		changed[field.(string)] = true
	}
	if !changed["name"] || !changed["description"] || !changed["groupType"] || len(changed) != 3 {
		t.Fatalf("changedFields: %v", patchData["changedFields"])
	}
	nextUpdatedAt := patchData["updatedAt"].(string)
	if nextUpdatedAt == updatedAt {
		t.Fatal("patch must bump updatedAt")
	}

	// No-change patch returns the current version with no fields.
	code, noop := env.do(t, http.MethodPatch, "/__aisys__/api/groups/"+alphaID,
		`{"expectedUpdatedAt":"`+nextUpdatedAt+`"}`)
	if code != 400 || noop["message"] != "请提供要修改的分组内容" {
		t.Fatalf("empty patch: %d %v", code, noop)
	}

	// providerCode patch 兼容入参：接受但忽略（供应商解耦设计 §8）——仅含
	// 忽略字段的 patch 无可变更内容，路由层按空输入拒绝（分组参数无效）。
	code, providerIgnored := env.do(t, http.MethodPatch, "/__aisys__/api/groups/"+alphaID,
		`{"expectedUpdatedAt":"`+nextUpdatedAt+`","providerCode":"anthropic"}`)
	if code != http.StatusBadRequest || providerIgnored["message"] != "分组参数无效" {
		t.Fatalf("ignored provider patch: %d %v", code, providerIgnored)
	}

	// Route-strategy binding guard on disable.
	env.exec(t, `INSERT INTO route_strategies (id, system_account_id, name, status) VALUES ('rs-1', ?, '策略一', 'active')`, adminID)
	env.bindRouteStrategy(t, "rs-1", adminID, alphaID, "active")
	code, disableBlocked := env.do(t, http.MethodPatch, "/__aisys__/api/groups/"+alphaID,
		`{"expectedUpdatedAt":"`+nextUpdatedAt+`","enabled":false}`)
	if code != http.StatusBadRequest || !strings.Contains(disableBlocked["message"].(string), "唯一可用启用分组") {
		t.Fatalf("disable blocked: %d %v", code, disableBlocked)
	}

	// Default-group patch guard.
	defaultID := "grp-default"
	now := time.Now().UTC().Format(time.RFC3339Nano)
	env.exec(t, `INSERT INTO groups (id, system_account_id, name, enabled, is_default, group_type, created_at, updated_at)
		VALUES (?, ?, '默认分组', 1, 1, 'personal', ?, ?)`, defaultID, adminID, now, now)
	code, defaultBlocked := env.do(t, http.MethodPatch, "/__aisys__/api/groups/"+defaultID,
		`{"expectedUpdatedAt":"`+now+`","name":"renamed-default"}`)
	if code != http.StatusBadRequest || defaultBlocked["message"] != "默认分组不允许修改" {
		t.Fatalf("default patch: %d %v", code, defaultBlocked)
	}

	// Operation log entries for create.
	seen := map[string]bool{}
	for _, action := range env.sink.actions() {
		seen[action] = true
	}
	if !seen["groups.create"] || !seen["groups.update"] {
		t.Fatalf("operation log actions: %v", env.sink.actions())
	}
	if !env.inval.has("group_created") || !env.inval.has("group_updated") {
		t.Fatalf("invalidation reasons: %v", env.inval.reasons)
	}
}

func TestGroupDeleteCascadeAndGuards(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	groupID, _ := env.createGroup(t, "/__aisys__/api/groups", "doomed")

	// Default-group delete guard.
	code, defaultBlocked := env.do(t, http.MethodDelete, "/__aisys__/api/groups/grp-default", "")
	if code != http.StatusNotFound {
		t.Fatalf("missing group delete must 404: %d %v", code, defaultBlocked)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	env.exec(t, `INSERT INTO groups (id, system_account_id, name, enabled, is_default, group_type, created_at, updated_at)
		VALUES ('grp-default', ?, '默认分组', 1, 1, 'personal', ?, ?)`, adminID, now, now)
	code, defaultGuard := env.do(t, http.MethodDelete, "/__aisys__/api/groups/grp-default", "")
	if code != http.StatusBadRequest || defaultGuard["message"] != "默认分组不能删除" {
		t.Fatalf("default delete guard: %d %v", code, defaultGuard)
	}

	// Sole active binding of an active strategy blocks the delete.
	env.exec(t, `INSERT INTO route_strategies (id, system_account_id, name, status) VALUES ('rs-del', ?, '删除策略', 'active')`, adminID)
	env.bindRouteStrategy(t, "rs-del", adminID, groupID, "active")
	code, deleteBlocked := env.do(t, http.MethodDelete, "/__aisys__/api/groups/"+groupID, "")
	if code != http.StatusBadRequest || !strings.Contains(deleteBlocked["message"].(string), "唯一可用启用分组") {
		t.Fatalf("delete blocked: %d %v", code, deleteBlocked)
	}

	// Adding a second active enabled binding unblocks the delete.
	env.createGroup(t, "/__aisys__/api/groups", "survivor")
	survivorDetailCode, survivorDetail := env.do(t, http.MethodGet, "/__aisys__/api/groups", "")
	if survivorDetailCode != 200 {
		t.Fatalf("list: %d %v", survivorDetailCode, survivorDetail)
	}
	var survivorID string
	for _, item := range dataMap(t, survivorDetail)["items"].([]any) {
		itemMap := item.(map[string]any)
		if itemMap["name"] == "survivor" {
			survivorID = itemMap["id"].(string)
		}
	}
	if survivorID == "" {
		t.Fatal("survivor group missing")
	}
	env.bindRouteStrategy(t, "rs-del", adminID, survivorID, "active")

	// Cascade payloads.
	env.bindAccount(t, adminID, groupID, "acc-del", "active")
	env.exec(t, `INSERT INTO group_authorization_settings (authorization_id, system_account_id, group_id, enabled, group_type, created_at, updated_at)
		VALUES ('authz-1', ?, ?, 1, 'personal', ?, ?)`, adminID, groupID, now, now)

	code, deletePayload := env.do(t, http.MethodDelete, "/__aisys__/api/groups/"+groupID, "")
	if code != http.StatusNoContent {
		t.Fatalf("delete: %d %v", code, deletePayload)
	}
	code, gone := env.do(t, http.MethodGet, "/__aisys__/api/groups/"+groupID, "")
	if code != http.StatusNotFound || gone["message"] != "分组不存在" {
		t.Fatalf("after delete: %d %v", code, gone)
	}
	if env.count(t, `SELECT COUNT(*) FROM group_accounts WHERE group_id = ?`, groupID) != 0 {
		t.Fatal("group_accounts must cascade")
	}
	if env.count(t, `SELECT COUNT(*) FROM group_authorization_settings WHERE group_id = ?`, groupID) != 0 {
		t.Fatal("group_authorization_settings must cascade")
	}
	if env.count(t, `SELECT COUNT(*) FROM route_strategy_groups WHERE group_id = ?`, groupID) != 0 {
		t.Fatal("route_strategy_groups must cascade")
	}
	if env.count(t, `SELECT COUNT(*) FROM group_account_stats_dirty WHERE group_id = ? AND reason = 'group_deleted'`, groupID) != 1 {
		t.Fatal("stats dirty row must be marked")
	}
	if !env.inval.has("group_deleted") {
		t.Fatalf("invalidation reasons: %v", env.inval.reasons)
	}
	seen := map[string]bool{}
	for _, action := range env.sink.actions() {
		seen[action] = true
	}
	if !seen["groups.delete"] {
		t.Fatalf("operation log actions: %v", env.sink.actions())
	}

	// Delete again → 404.
	code, again := env.do(t, http.MethodDelete, "/__aisys__/api/groups/"+groupID, "")
	if code != http.StatusNotFound {
		t.Fatalf("double delete: %d %v", code, again)
	}
}

func TestGroupMyGroupsSelfScope(t *testing.T) {
	env := newTestEnv(t)
	userAID := env.login(t, "alice", "alice-pass", "user")
	aliceGroupID, created := env.createGroup(t, "/__aisys__/api/my-groups", "alice-group")
	if created["ownerSystemAccountId"] != userAID {
		t.Fatalf("my-groups create owner: %v", created)
	}

	// userB cannot see or mutate alice's group via my-*.
	env.login(t, "bob", "bob-pass", "user")
	code, forbidden := env.do(t, http.MethodGet, "/__aisys__/api/my-groups/"+aliceGroupID, "")
	if code != http.StatusNotFound || forbidden["message"] != "分组不存在" {
		t.Fatalf("bob read alice group: %d %v", code, forbidden)
	}
	code, _ = env.do(t, http.MethodPatch, "/__aisys__/api/my-groups/"+aliceGroupID,
		`{"expectedUpdatedAt":"2020-01-01T00:00:00.000Z","name":"hijacked"}`)
	if code != http.StatusNotFound {
		t.Fatalf("bob patch alice group: %d", code)
	}
	code, _ = env.do(t, http.MethodDelete, "/__aisys__/api/my-groups/"+aliceGroupID, "")
	if code != http.StatusNotFound {
		t.Fatalf("bob delete alice group: %d", code)
	}

	// Bob's list only shows bob's own groups.
	bobGroupID, _ := env.createGroup(t, "/__aisys__/api/my-groups", "bob-group")
	code, bobList := env.do(t, http.MethodGet, "/__aisys__/api/my-groups", "")
	items := dataMap(t, bobList)["items"].([]any)
	if code != 200 || len(items) != 1 || items[0].(map[string]any)["id"] != bobGroupID {
		t.Fatalf("bob list: %d %v", code, bobList)
	}

	// Bob (user role) cannot reach the admin surface.
	code, adminDenied := env.do(t, http.MethodGet, "/__aisys__/api/groups", "")
	if code != http.StatusForbidden || adminDenied["message"] != "需要管理员权限" {
		t.Fatalf("admin list as user: %d %v", code, adminDenied)
	}
	code, _ = env.do(t, http.MethodPost, "/__aisys__/api/groups", `{"name":"x","providerCode":"openai"}`)
	if code != http.StatusForbidden {
		t.Fatalf("admin create as user: %d", code)
	}
	code, _ = env.do(t, http.MethodPatch, "/__aisys__/api/groups/"+bobGroupID, `{"expectedUpdatedAt":"2020-01-01T00:00:00.000Z","name":"y"}`)
	if code != http.StatusForbidden {
		t.Fatalf("admin patch as user: %d", code)
	}
	code, _ = env.do(t, http.MethodDelete, "/__aisys__/api/groups/"+bobGroupID, "")
	if code != http.StatusForbidden {
		t.Fatalf("admin delete as user: %d", code)
	}

	// Admin /my-groups is forced to self scope too (admin-owned rows only).
	env.login(t, "root", "root-pass", "super_admin")
	code, adminSelf := env.do(t, http.MethodGet, "/__aisys__/api/my-groups", "")
	if code != 200 || len(dataMap(t, adminSelf)["items"].([]any)) != 0 {
		t.Fatalf("admin my-groups must be self-scoped: %d %v", code, adminSelf)
	}
	// Admin surface sees everything (systemAccountId filter supported).
	code, adminAll := env.do(t, http.MethodGet, "/__aisys__/api/groups", "")
	if code != 200 || len(dataMap(t, adminAll)["items"].([]any)) != 2 {
		t.Fatalf("admin list: %d %v", code, adminAll)
	}
}

func TestGroupPermissionMatrixAnonymousAndValidation(t *testing.T) {
	env := newTestEnv(t)

	// Anonymous access is refused on both surfaces.
	code, anonymous := env.do(t, http.MethodGet, "/__aisys__/api/my-groups", "")
	if code != http.StatusUnauthorized || anonymous["message"] != "请先登录" {
		t.Fatalf("anonymous my-groups: %d %v", code, anonymous)
	}
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/groups", "")
	if code != http.StatusUnauthorized {
		t.Fatalf("anonymous groups: %d", code)
	}

	env.login(t, "root", "root-pass", "super_admin")
	code, invalid := env.do(t, http.MethodPost, "/__aisys__/api/groups", `{"providerCode":"openai"}`)
	if code != http.StatusBadRequest || invalid["message"] != "分组参数无效" {
		t.Fatalf("missing name: %d %v", code, invalid)
	}
	code, unknownField := env.do(t, http.MethodPost, "/__aisys__/api/groups",
		`{"name":"ok","providerCode":"openai","bogusField":1}`)
	if code != http.StatusBadRequest || unknownField["message"] != "分组参数无效" {
		t.Fatalf("unknown field: %d %v", code, unknownField)
	}
	createdID, _ := env.createGroup(t, "/__aisys__/api/groups", "patched-target")
	code, missingVersion := env.do(t, http.MethodPatch, "/__aisys__/api/groups/"+createdID, `{"name":"new-name"}`)
	if code != http.StatusBadRequest || missingVersion["message"] != "分组参数无效" {
		t.Fatalf("missing expectedUpdatedAt: %d %v", code, missingVersion)
	}
	code, badVersion := env.do(t, http.MethodPatch, "/__aisys__/api/groups/"+createdID,
		`{"expectedUpdatedAt":"not-a-time","name":"new-name"}`)
	if code != http.StatusBadRequest || badVersion["message"] != "分组参数无效" {
		t.Fatalf("bad expectedUpdatedAt: %d %v", code, badVersion)
	}
	code, badPolicy := env.do(t, http.MethodPost, "/__aisys__/api/groups",
		`{"name":"policy-broken","providerCode":"openai","groupType":"high_concurrency","schedulingPolicy":{"defaultSoftConcurrency":0}}`)
	if code != http.StatusBadRequest || badPolicy["message"] != "分组参数无效" {
		t.Fatalf("bad policy: %d %v", code, badPolicy)
	}
	// Route-level schema rejects null optional fields and null/float/unknown
	// schedulingPolicy sub-keys exactly like the Node zod schema (BUG-0163
	// null/子字段主张)。
	code, nullDescription := env.do(t, http.MethodPost, "/__aisys__/api/groups",
		`{"name":"null-desc","providerCode":"openai","description":null}`)
	if code != http.StatusBadRequest || nullDescription["message"] != "分组参数无效" {
		t.Fatalf("null description: %d %v", code, nullDescription)
	}
	code, nullSubKey := env.do(t, http.MethodPost, "/__aisys__/api/groups",
		`{"name":"null-policy","providerCode":"openai","groupType":"high_concurrency","schedulingPolicy":{"defaultSoftConcurrency":null}}`)
	if code != http.StatusBadRequest || nullSubKey["message"] != "分组参数无效" {
		t.Fatalf("null policy sub-key: %d %v", code, nullSubKey)
	}
	unknownSubKey := `{"name":"unknown-policy","providerCode":"openai","groupType":"high_concurrency","schedulingPolicy":{"bogusKey":1}}`
	code, unknownPolicy := env.do(t, http.MethodPost, "/__aisys__/api/groups", unknownSubKey)
	if code != http.StatusBadRequest || unknownPolicy["message"] != "分组参数无效" {
		t.Fatalf("unknown policy sub-key: %d %v", code, unknownPolicy)
	}
	code, nullEnabledPatch := env.do(t, http.MethodPatch, "/__aisys__/api/groups/"+createdID,
		`{"expectedUpdatedAt":"2020-01-01T00:00:00.000Z","enabled":null}`)
	if code != http.StatusBadRequest || nullEnabledPatch["message"] != "分组参数无效" {
		t.Fatalf("null enabled patch: %d %v", code, nullEnabledPatch)
	}

	// Detail 404 for unknown ids on both surfaces.
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/groups/does-not-exist", "")
	if code != http.StatusNotFound {
		t.Fatalf("admin detail 404: %d", code)
	}
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/my-groups/does-not-exist", "")
	if code != http.StatusNotFound {
		t.Fatalf("my detail 404: %d", code)
	}
}

// TestGroupStoreLevelDuplicateName covers the storage-side unique-name
// conflict mapping (the HTTP dedup guard shadows it for identical payloads).
func TestGroupStoreLevelDuplicateName(t *testing.T) {
	env := newTestEnv(t)
	store, err := NewStore(env.db, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	access := AccessScope{ViewerID: "owner-1"}
	first, err := store.Create(context.Background(), MutationInput{
		Name: ptrString("dupe"), ProviderCode: ptrString("openai"),
	}, access)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err = store.Create(context.Background(), MutationInput{
		Name: ptrString("dupe"), ProviderCode: ptrString("openai"),
	}, access)
	var conflict *ConflictError
	if !errors.As(err, &conflict) || !strings.Contains(conflict.Message, "同名分组已存在") {
		t.Fatalf("duplicate create: %v", err)
	}
	// 供应商解耦后 owner 内名称唯一不再有 provider 维度（设计 §8）：跨 provider
	// 同名同样冲突。
	_, err = store.Create(context.Background(), MutationInput{
		Name: ptrString("dupe"), ProviderCode: ptrString("anthropic"),
	}, access)
	if !errors.As(err, &conflict) {
		t.Fatalf("same name other provider must conflict: %v", err)
	}
	detail, err := store.FindDetail(context.Background(), first.ID, access)
	if err != nil || detail == nil || detail.Name != "dupe" {
		t.Fatalf("detail: %v %v", detail, err)
	}
}

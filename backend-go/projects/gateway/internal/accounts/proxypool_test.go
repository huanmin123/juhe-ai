package accounts

// proxypool_test.go 是账户主写入/读取面的代理池支持测试（代理池设计
// §6.1/§8.3/§8.4/§8.5，P0-4 子任务）：创建互斥、绑池租约 reason/generation、
// 健康全 dead 无租约创建、PATCH 改绑/解绑租约替换与删除、列表池字段契约、
// 高级详情配置绑定回填、克隆池名、批量逐账户独立租约、导入/导出池引用。
// PG 集成走 pgkit_test 夹具（proxy_pools/proxy_pool_members/account_proxy_leases
// + accounts.proxy_pool_id + accounts_proxy_target_single_check）。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// seedProxyPoolFixture 落一个代理档案 + 池 + 成员的最小事实集（enabled=false
// 的成员行可显式关闭；health 传空串 = NULL → unknown）。
func seedProxyPoolFixture(t *testing.T, env *testEnv, ownerID, profileID, profileName string,
	profileEnabled bool, health string, poolID, poolName, strategy string, poolEnabled bool,
	memberEnabled bool, priority, weight int) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var healthArg any
	if health == "" {
		healthArg = nil
	} else {
		healthArg = health
	}
	env.exec(t, `INSERT INTO proxy_profiles (id, system_account_id, name, type, host, port, enabled,
		test_status, pool_health_status, pool_health_failure_streak, created_at, updated_at)
		VALUES (?, ?, ?, 'socks5', '127.0.0.1', 1080, ?, 'unknown', ?, 0, ?, ?)
		ON CONFLICT (id) DO UPDATE SET enabled = EXCLUDED.enabled, pool_health_status = EXCLUDED.pool_health_status`,
		profileID, ownerID, profileName, profileEnabled, healthArg, now, now)
	env.exec(t, `INSERT INTO proxy_pools (id, system_account_id, name, strategy, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET enabled = EXCLUDED.enabled, strategy = EXCLUDED.strategy`,
		poolID, ownerID, poolName, strategy, poolEnabled, now, now)
	env.exec(t, `INSERT INTO proxy_pool_members (pool_id, proxy_profile_id, priority, weight, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (pool_id, proxy_profile_id) DO UPDATE SET enabled = EXCLUDED.enabled,
			priority = EXCLUDED.priority, weight = EXCLUDED.weight`,
		poolID, profileID, priority, weight, memberEnabled, now, now)
}

// poolCreatePayload 基于 createPayload 在顶层注入 proxyPoolId（代理池设计
// §8.3）。
func poolCreatePayload(name, poolID string) string {
	// createPayload 以 "}]}" 收尾（connections 数组 + 顶层对象），直接在末个
	// `}` 前注入 proxyPoolId。
	body := createPayload(name)
	idx := strings.LastIndex(body, "}")
	return body[:idx] + `,"proxyPoolId":"` + poolID + `"}` + body[idx+1:]
}

func assertPoolLease(t *testing.T, env *testEnv, accountID, poolID, memberProfileID, reason string) {
	t.Helper()
	if got := env.count(t, `SELECT COUNT(*) FROM account_proxy_leases
		WHERE account_id = ? AND pool_id = ? AND proxy_profile_id = ? AND assignment_reason = ?
		AND generation > 0 AND assigned_at IS NOT NULL AND updated_at IS NOT NULL`,
		accountID, poolID, memberProfileID, reason); got != 1 {
		t.Fatalf("租约行契约不满足（%s/%s/%s/%s）：%d", accountID, poolID, memberProfileID, reason, got)
	}
}

func poolListItems(t *testing.T, env *testEnv) []map[string]any {
	t.Helper()
	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts?page=1&pageSize=50", "")
	if code != http.StatusOK {
		t.Fatalf("list: %d %v", code, payload)
	}
	items, ok := dataMap(t, payload)["items"].([]any)
	if !ok {
		t.Fatalf("list items missing: %v", payload)
	}
	out := []map[string]any{}
	for _, item := range items {
		out = append(out, item.(map[string]any))
	}
	return out
}

func findListItem(items []map[string]any, id string) map[string]any {
	for _, item := range items {
		if item["id"] == id {
			return item
		}
	}
	return nil
}

// TestProxyPoolCreateConflictAndDisabled 覆盖 §8.3 互斥与池状态校验（400 错误码）。
func TestProxyPoolCreateConflictAndDisabled(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	seedProxyPoolFixture(t, env, adminID, "prof-a", "代理A", true, "", "pool-1", "池一", "round_robin", true, true, 0, 1)
	disabledPool := "pool-off"
	seedProxyPoolFixture(t, env, adminID, "prof-b", "代理B", true, "", disabledPool, "池停用", "round_robin", false, true, 0, 1)
	emptyPool := "pool-empty"
	env.exec(t, `INSERT INTO proxy_pools (id, system_account_id, name, strategy, enabled, created_at, updated_at)
		VALUES (?, ?, '空池', 'round_robin', true, NOW(), NOW())`, emptyPool, adminID)

	// 同时提交 proxyProfileId + proxyPoolId → 400 proxy_target_conflict。
	body := strings.Replace(poolCreatePayload("conflict-account", "pool-1"),
		`"supportedModels"`, `"proxyProfileId":"prof-a","supportedModels"`, 1)
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", body)
	if code != http.StatusBadRequest {
		t.Fatalf("conflict: %d %v", code, payload)
	}
	if payload["code"] != "proxy_target_conflict" {
		t.Fatalf("conflict code: %v", payload)
	}

	// 池停用 → 400 proxy_pool_disabled。
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts", poolCreatePayload("disabled-pool-account", disabledPool))
	if code != http.StatusBadRequest || payload["code"] != "proxy_pool_disabled" {
		t.Fatalf("disabled pool: %d %v", code, payload)
	}

	// 池无 enabled 成员 → 400 proxy_pool_empty。
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts", poolCreatePayload("empty-pool-account", emptyPool))
	if code != http.StatusBadRequest || payload["code"] != "proxy_pool_empty" {
		t.Fatalf("empty pool: %d %v", code, payload)
	}

	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE deleted_at IS NULL`) != 0 {
		t.Fatal("失败的创建不得残留账户行")
	}
}

// TestProxyPoolCreateWritesInitialLeaseAndListFields 覆盖 §6.1 创建租约（reason=
// initial、generation 取序列）与 §8.5 列表池字段契约。
func TestProxyPoolCreateWritesInitialLeaseAndListFields(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	seedProxyPoolFixture(t, env, adminID, "prof-a", "代理A", true, "", "pool-1", "池一", "round_robin", true, true, 0, 1)

	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", poolCreatePayload("pool-account", "pool-1"))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, payload)
	}
	id := dataMap(t, payload)["id"].(string)
	if got := env.queryCell(t, `SELECT proxy_pool_id FROM accounts WHERE id = ?`, id); got != "pool-1" {
		t.Fatalf("proxy_pool_id: %v", got)
	}
	assertPoolLease(t, env, id, "pool-1", "prof-a", "initial")

	// 列表：proxyTargetType=pool + 池/成员/可分配成员数字段；proxyProfileId 恒 null。
	items := poolListItems(t, env)
	item := findListItem(items, id)
	if item == nil {
		t.Fatal("list missing created account")
	}
	if item["proxyTargetType"] != "pool" {
		t.Fatalf("proxyTargetType: %v", item["proxyTargetType"])
	}
	if item["proxyPoolId"] != "pool-1" || item["proxyPoolName"] != "池一" {
		t.Fatalf("pool fields: %v %v", item["proxyPoolId"], item["proxyPoolName"])
	}
	if enabled, ok := item["proxyPoolEnabled"].(bool); !ok || !enabled {
		t.Fatalf("proxyPoolEnabled: %v", item["proxyPoolEnabled"])
	}
	if item["proxyMemberProfileId"] != "prof-a" || item["proxyMemberProfileName"] != "代理A" {
		t.Fatalf("member fields: %v %v", item["proxyMemberProfileId"], item["proxyMemberProfileName"])
	}
	if count, ok := item["proxyPoolAssignableMemberCount"].(float64); !ok || count != 1 {
		t.Fatalf("assignable count: %v", item["proxyPoolAssignableMemberCount"])
	}
	if _, exists := item["proxyProfileId"]; exists {
		t.Fatalf("pool row must keep proxyProfileId null: %v", item["proxyProfileId"])
	}
	if unavailable, ok := item["proxyProfileUnavailable"].(bool); ok && unavailable {
		t.Fatal("可用池 + 可用成员不得标红")
	}

	// 高级详情：配置绑定 + 池摘要 + 有效成员（编辑回填用配置绑定）。
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/accounts/"+id+"/advanced", "")
	if code != http.StatusOK {
		t.Fatalf("advanced: %d %v", code, payload)
	}
	advanced := dataMap(t, payload)
	if advanced["proxyPoolId"] != "pool-1" || advanced["proxyPoolName"] != "池一" || advanced["proxyPoolStrategy"] != "round_robin" {
		t.Fatalf("advanced pool summary: %v %v %v", advanced["proxyPoolId"], advanced["proxyPoolName"], advanced["proxyPoolStrategy"])
	}
	if memberID, ok := advanced["proxyMemberProfileId"].(string); !ok || memberID != "prof-a" {
		t.Fatalf("advanced member: %v", advanced["proxyMemberProfileId"])
	}

	// 克隆上下文：池绑定 + 池名（C9 A：克隆不复制租约，新账户由创建事务分配）。
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/accounts/"+id+"/clone-context", "")
	if code != http.StatusOK {
		t.Fatalf("clone-context: %d %v", code, payload)
	}
	clone := dataMap(t, payload)
	if clone["proxyPoolId"] != "pool-1" || clone["proxyPoolName"] != "池一" {
		t.Fatalf("clone pool fields: %v %v", clone["proxyPoolId"], clone["proxyPoolName"])
	}
}

// TestProxyPoolCreateWithDeadHealthCreatesWithoutLease 覆盖 §6.1：健康全 dead
// 允许创建但无租约（列表「待分配」不标红）。
func TestProxyPoolCreateWithDeadHealthCreatesWithoutLease(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	seedProxyPoolFixture(t, env, adminID, "prof-dead", "死代理", true, "dead", "pool-dead", "死池", "round_robin", true, true, 0, 1)

	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", poolCreatePayload("dead-health-account", "pool-dead"))
	if code != http.StatusCreated {
		t.Fatalf("create with dead health: %d %v", code, payload)
	}
	id := dataMap(t, payload)["id"].(string)
	if env.count(t, `SELECT COUNT(*) FROM account_proxy_leases WHERE account_id = ?`, id) != 0 {
		t.Fatal("健康全 dead 时不得写租约")
	}
	items := poolListItems(t, env)
	item := findListItem(items, id)
	if item == nil || item["proxyTargetType"] != "pool" {
		t.Fatalf("list item: %v", item)
	}
	// 成员 dead → 可分配计数 0 → 池级不可用（§8.5「无可用成员」）。
	if count, ok := item["proxyPoolAssignableMemberCount"].(float64); !ok || count != 0 {
		t.Fatalf("dead member count: %v", item["proxyPoolAssignableMemberCount"])
	}
	if unavailable, ok := item["proxyProfileUnavailable"].(bool); !ok || !unavailable {
		t.Fatal("池无可分配成员必须标红")
	}
	if message, ok := item["proxyProfileErrorMessage"].(string); !ok || !strings.Contains(message, "代理池没有可用成员") {
		t.Fatalf("pool-level message: %v", item["proxyProfileErrorMessage"])
	}
}

// TestProxyPoolPatchRebindAndUnbindLeaseContract 覆盖 §6.1 PATCH 改绑（reason=
// pool_reassigned、旧租约替换）与解绑（租约删除）、改走直接代理（不残留租约）。
func TestProxyPoolPatchRebindAndUnbindLeaseContract(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	seedProxyPoolFixture(t, env, adminID, "prof-a", "代理A", true, "", "pool-1", "池一", "round_robin", true, true, 0, 1)
	seedProxyPoolFixture(t, env, adminID, "prof-b", "代理B", true, "", "pool-2", "池二", "round_robin", true, true, 0, 1)

	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", poolCreatePayload("patch-pool-account", "pool-1"))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, payload)
	}
	id := dataMap(t, payload)["id"].(string)
	assertPoolLease(t, env, id, "pool-1", "prof-a", "initial")
	firstGeneration := env.queryCell(t, `SELECT generation FROM account_proxy_leases WHERE account_id = ?`, id)

	// PATCH 改绑池二 → pool_reassigned，generation 取新序列值。
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":1,"proxyPoolId":"pool-2"}`)
	if code != http.StatusOK {
		t.Fatalf("patch rebind: %d %v", code, payload)
	}
	if env.count(t, `SELECT COUNT(*) FROM account_proxy_leases WHERE account_id = ? AND pool_id = 'pool-1'`, id) != 0 {
		t.Fatal("改绑必须替换旧租约")
	}
	assertPoolLease(t, env, id, "pool-2", "prof-b", "pool_reassigned")
	if got := env.queryCell(t, `SELECT generation FROM account_proxy_leases WHERE account_id = ?`, id); got == firstGeneration {
		t.Fatalf("generation 必须取共享序列新值：%s", got)
	}

	// PATCH 同时提交 profile + pool → 400 proxy_target_conflict，绑定不变。
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":2,"proxyProfileId":"prof-a","proxyPoolId":"pool-1"}`)
	if code != http.StatusBadRequest || payload["code"] != "proxy_target_conflict" {
		t.Fatalf("patch conflict: %d %v", code, payload)
	}
	assertPoolLease(t, env, id, "pool-2", "prof-b", "pool_reassigned")

	// PATCH 解绑（proxyPoolId:null）→ 租约删除、直连。
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":2,"proxyPoolId":null}`)
	if code != http.StatusOK {
		t.Fatalf("patch unbind: %d %v", code, payload)
	}
	if env.count(t, `SELECT COUNT(*) FROM account_proxy_leases WHERE account_id = ?`, id) != 0 {
		t.Fatal("解绑必须删除租约")
	}
	if got := env.queryCell(t, `SELECT proxy_pool_id FROM accounts WHERE id = ?`, id); got != "" {
		t.Fatalf("unbind pool column: %v", got)
	}

	// PATCH 改走直接代理：直接代理绑定不得残留租约。
	env.exec(t, `UPDATE accounts SET config_revision = 3 WHERE id = ?`, id)
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":3,"proxyProfileId":"prof-a"}`)
	if code != http.StatusOK {
		t.Fatalf("patch profile: %d %v", code, payload)
	}
	if env.count(t, `SELECT COUNT(*) FROM account_proxy_leases WHERE account_id = ?`, id) != 0 {
		t.Fatal("直接代理绑定不得残留租约")
	}
}

// TestProxyPoolBatchUpdateIndependentLeases 覆盖 §8.4：批量绑池逐账户独立
// 租约（round_robin 按租约数最小分配）、互斥整批拒绝。
func TestProxyPoolBatchUpdateIndependentLeases(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	seedProxyPoolFixture(t, env, adminID, "prof-a", "代理A", true, "", "pool-1", "池一", "round_robin", true, true, 0, 1)
	seedProxyPoolFixture(t, env, adminID, "prof-b", "代理B", true, "", "pool-1", "池一", "round_robin", true, true, 1, 1)

	ids := []string{}
	for _, name := range []string{"batch-pool-alpha", "batch-pool-bravo"} {
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload(name))
		if code != http.StatusCreated {
			t.Fatalf("create %s: %d %v", name, code, payload)
		}
		ids = append(ids, dataMap(t, payload)["id"].(string))
	}

	// 互斥：同时勾选 proxyProfileId 与 proxyPoolId → 400。
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/batch-update",
		`{"targets":[{"accountId":"`+ids[0]+`","configRevision":1},{"accountId":"`+ids[1]+`","configRevision":1}],
		"updates":{"proxyProfileId":{"enabled":true,"value":"prof-a"},"proxyPoolId":{"enabled":true,"value":"pool-1"}}}`)
	if code != http.StatusBadRequest {
		t.Fatalf("batch conflict: %d %v", code, payload)
	}
	if payload["code"] != "proxy_target_conflict" {
		t.Fatalf("batch conflict code: %v", payload)
	}

	// 绑池：两账户独立租约（成员 A/B 各一）。
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts/batch-update",
		`{"targets":[{"accountId":"`+ids[0]+`","configRevision":1},{"accountId":"`+ids[1]+`","configRevision":1}],
		"updates":{"proxyPoolId":{"enabled":true,"value":"pool-1"}}}`)
	if code != http.StatusOK {
		t.Fatalf("batch bind: %d %v", code, payload)
	}
	assertPoolLease(t, env, ids[0], "pool-1", "prof-a", "pool_reassigned")
	assertPoolLease(t, env, ids[1], "pool-1", "prof-b", "pool_reassigned")
	if got := env.count(t, `SELECT COUNT(*) FROM account_proxy_leases WHERE pool_id = 'pool-1'`); got != 2 {
		t.Fatalf("independent leases: %d", got)
	}

	// 解绑：清池后租约删除。
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts/batch-update",
		`{"targets":[{"accountId":"`+ids[0]+`","configRevision":2},{"accountId":"`+ids[1]+`","configRevision":2}],
		"updates":{"proxyPoolId":{"enabled":true,"value":null}}}`)
	if code != http.StatusOK {
		t.Fatalf("batch unbind: %d %v", code, payload)
	}
	if got := env.count(t, `SELECT COUNT(*) FROM account_proxy_leases WHERE pool_id = 'pool-1'`); got != 0 {
		t.Fatalf("unbind leases left: %d", got)
	}
}

// TestProxyPoolExportImportRoundTrip 覆盖 §8.4：导出 proxyPoolRef + 顶层
// proxyPools 段（成员 proxyProfileId，不含密码）；导入引用已存在池成功、
// 缺失池明确失败。
func TestProxyPoolExportImportRoundTrip(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	seedProxyPoolFixture(t, env, adminID, "prof-a", "代理A", true, "", "pool-1", "池一", "weighted", true, true, 0, 2)

	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", poolCreatePayload("roundtrip-account", "pool-1"))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, payload)
	}
	id := dataMap(t, payload)["id"].(string)

	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts/export", `{"accountIds":["`+id+`"]}`)
	if code != http.StatusOK {
		t.Fatalf("export: %d %v", code, payload)
	}
	document := dataMap(t, payload)["document"].(map[string]any)
	exported := document["accounts"].([]any)[0].(map[string]any)
	if exported["proxyPoolRef"] != "proxy-pool-pool-1" {
		t.Fatalf("proxyPoolRef: %v", exported["proxyPoolRef"])
	}
	if _, exists := exported["proxyRef"]; exists {
		t.Fatalf("绑池账户不得导出 proxyRef：%v", exported["proxyRef"])
	}
	pools, ok := document["proxyPools"].([]any)
	if !ok || len(pools) != 1 {
		t.Fatalf("proxyPools section: %v", document["proxyPools"])
	}
	pool := pools[0].(map[string]any)
	if pool["ref"] != "proxy-pool-pool-1" || pool["name"] != "池一" || pool["strategy"] != "weighted" {
		t.Fatalf("pool entry: %v", pool)
	}
	members := pool["members"].([]any)
	if len(members) != 1 || members[0].(map[string]any)["proxyProfileId"] != "prof-a" {
		t.Fatalf("pool members: %v", members)
	}
	// 成员引用不携带代理密码（Proxies 段无该池成员的密码行）。
	for _, proxy := range func() []any {
		if list, ok := document["proxies"].([]any); ok {
			return list
		}
		return nil
	}() {
		if proxy.(map[string]any)["ref"] == "proxy-prof-a" {
			t.Fatal("池成员代理不应经 Proxies 段重复导出")
		}
	}

	// 导入：顶层 proxyPools 只读引用段 + proxyPoolRef 命中已存在池 → 创建并分配租约。
	importDoc := map[string]any{
		"type": accountImportProtocolType, "version": float64(accountImportProtocolVersion),
		"proxyPools": []any{pool},
		"accounts": []any{map[string]any{
			"name": "imported-pool-account", "providerCode": "gpt",
			"providerProtocolProfileId": "prof-gpt", "type": "api_key", "status": "active",
			"credentials": map[string]any{"api_key": "sk-import-pool-1234567890",
				"base_url":                 "https://api.openai.com/v1",
				"supported_endpoint_modes": []any{"chat_json", "chat_sse"}},
			"groupName":                       "默认分组",
			"supportedModels":                 []any{"gpt-4o-mini", "gpt-4.1"},
			"connections":                     json.RawMessage(`[{"connectionPresetCode":"gpt_openai_v1","baseUrl":"https://api.openai.com/v1","endpointModes":["chat_json","chat_sse"],"enabled":true}]`),
			"healthCheckConnectionPresetCode": "gpt_openai_v1",
			"healthCheckEndpointMode":         "chat_json",
			"healthCheckModel":                "gpt-4o-mini",
			"proxyPoolRef":                    "proxy-pool-pool-1",
		}},
	}
	raw, err := json.Marshal(importDoc)
	if err != nil {
		t.Fatal(err)
	}
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts/import/confirm",
		`{"data":`+string(raw)+`,"options":{"createMissingGroups":true,"createMissingProxies":false,"skipDuplicates":false}}`)
	if code != http.StatusOK {
		t.Fatalf("import: %d %v", code, payload)
	}
	imported := dataMap(t, payload)
	item := imported["accounts"].([]any)[0].(map[string]any)
	if item["action"] != importActionCreate {
		t.Fatalf("import action: %v %v", item["action"], item["messages"])
	}
	importedID := item["accountId"].(string)
	assertPoolLease(t, env, importedID, "pool-1", "prof-a", "initial")

	// 导入缺失池 → 明确失败（不静默丢绑）。
	missingDoc := map[string]any{
		"type": accountImportProtocolType, "version": float64(accountImportProtocolVersion),
		"accounts": []any{map[string]any{
			"name": "imported-missing-pool", "providerCode": "gpt",
			"providerProtocolProfileId": "prof-gpt", "type": "api_key", "status": "active",
			"credentials": map[string]any{"api_key": "sk-import-pool-2", "base_url": "https://api.openai.com/v1",
				"supported_endpoint_modes": []any{"chat_json", "chat_sse"}},
			"groupName":                       "默认分组",
			"supportedModels":                 []any{"gpt-4o-mini", "gpt-4.1"},
			"connections":                     json.RawMessage(`[{"connectionPresetCode":"gpt_openai_v1","baseUrl":"https://api.openai.com/v1","endpointModes":["chat_json","chat_sse"],"enabled":true}]`),
			"healthCheckConnectionPresetCode": "gpt_openai_v1",
			"healthCheckEndpointMode":         "chat_json",
			"healthCheckModel":                "gpt-4o-mini",
			"proxyPoolId":                     "pool-missing",
		}},
	}
	raw, err = json.Marshal(missingDoc)
	if err != nil {
		t.Fatal(err)
	}
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts/import/confirm",
		`{"data":`+string(raw)+`,"options":{"createMissingGroups":true,"createMissingProxies":false,"skipDuplicates":false}}`)
	if code != http.StatusOK {
		t.Fatalf("import missing pool: %d %v", code, payload)
	}
	imported = dataMap(t, payload)
	item = imported["accounts"].([]any)[0].(map[string]any)
	if item["action"] != importActionFailed {
		t.Fatalf("missing pool must fail: %v", item)
	}
	joined := ""
	for _, message := range item["messages"].([]any) {
		joined += message.(string)
	}
	if !strings.Contains(joined, "代理池不存在") {
		t.Fatalf("missing pool message: %v", item["messages"])
	}
}

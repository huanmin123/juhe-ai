package accounts

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// alwaysAllowSchedule covers every UTC minute (regular windows plus the
// cross-midnight remainder) so the schedule-driven status mutation is
// deterministic regardless of the wall clock.
const alwaysAllowSchedule = `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[
	{"daysOfWeek":[1,2,3,4,5,6,7],"start":"00:00","end":"23:59"},
	{"daysOfWeek":[1,2,3,4,5,6,7],"start":"23:59","end":"00:00"}]}`

func seedImportProxy(t *testing.T, env *testEnv, id, ownerID, name string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	env.exec(t, `INSERT INTO proxy_profiles (id, system_account_id, name, type, host, port, enabled, test_status, created_at, updated_at)
		VALUES (?, ?, ?, 'socks5', '127.0.0.1', 1080, 1, 'unknown', ?, ?)`, id, ownerID, name, now, now)
}

// seedOpenAICompatibleProvider mirrors the adapter constants: the openai
// provider plus profile_openai_openai_v1 with default supported models.
func seedOpenAICompatibleProvider(t *testing.T, env *testEnv) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	env.exec(t, `INSERT INTO providers (id, code, name, enabled, default_supported_models_json, created_at, updated_at)
		VALUES ('prov-openai-compat', 'openai', 'OpenAI 兼容', 1, '["gpt-4o-mini"]', ?, ?)`, now, now)
	env.exec(t, `INSERT INTO provider_protocol_profiles (id, provider_code, name, enabled, protocol_code,
		protocol_version, base_url, default_health_check_model, account_types_json, capabilities_json, created_at, updated_at)
		VALUES ('profile_openai_openai_v1', 'openai', 'OpenAI 兼容协议', 1, 'openai', 'v1',
		'https://api.openai.com/v1', 'gpt-4o-mini', '["api_key","oauth"]', '[]', ?, ?)`, now, now)
}

func changedFieldSet(t *testing.T, payload map[string]any) map[string]bool {
	t.Helper()
	data := dataMap(t, payload)
	set := map[string]bool{}
	for _, field := range data["changedFields"].([]any) {
		set[field.(string)] = true
	}
	return set
}

func itemChangedFieldSet(t *testing.T, payload map[string]any, index int) map[string]bool {
	t.Helper()
	data := dataMap(t, payload)
	item := data["items"].([]any)[index].(map[string]any)
	set := map[string]bool{}
	for _, field := range item["changedFields"].([]any) {
		set[field.(string)] = true
	}
	return set
}

func TestAccountBatchEditContextAndBatchUpdate(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	ids := []string{}
	for _, name := range []string{"alpha", "bravo"} {
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload(name))
		if code != http.StatusCreated {
			t.Fatalf("create %s: %d %v", name, code, payload)
		}
		ids = append(ids, dataMap(t, payload)["id"].(string))
	}
	seedImportProxy(t, env, "pp-1", adminID, "批量代理")

	// batch-edit-context: field payload per account, owner fields stripped.
	code, context := env.do(t, http.MethodPost, "/__aisys__/api/accounts/batch-edit-context",
		`{"accountIds":["`+ids[0]+`","`+ids[1]+`"],"fields":["supportedModels","modelMappings","supportedEndpointModes"]}`)
	if code != http.StatusOK {
		t.Fatalf("batch-edit-context: %d %v", code, context)
	}
	items := dataArray(t, context)
	if len(items) != 2 {
		t.Fatalf("context items: %v", context)
	}
	first := items[0].(map[string]any)
	if first["id"] != ids[0] || first["configRevision"] != float64(1) ||
		first["providerCode"] != "gpt" || first["providerProtocolProfileId"] != "prof-gpt" ||
		first["protocolCode"] != "openai" || first["protocolVersion"] != "v1" || first["type"] != "api_key" {
		t.Fatalf("context item contract: %v", first)
	}
	models := first["supportedModels"].([]any)
	if len(models) != 2 || models[0].(string) != "gpt-4.1" || models[1].(string) != "gpt-4o-mini" {
		t.Fatalf("context supportedModels: %v", models)
	}
	if mappings := first["modelMappings"].([]any); len(mappings) != 0 {
		t.Fatalf("context modelMappings: %v", mappings)
	}
	if modes := first["supportedEndpointModes"].([]any); len(modes) != 0 {
		t.Fatalf("context supportedEndpointModes: %v", modes)
	}
	if _, leaked := first["ownerSystemAccountId"]; leaked {
		t.Fatal("context items must not leak the owner field")
	}

	// Context validation surface.
	contextCases := []struct {
		name    string
		body    string
		message string
	}{
		{"single account", `{"accountIds":["` + ids[0] + `"],"fields":[]}`, "批量编辑账户不能重复"},
		{"duplicate accounts", `{"accountIds":["` + ids[0] + `","` + ids[0] + `"],"fields":[]}`, "批量编辑账户不能重复"},
		{"unknown body key", `{"accountIds":["` + ids[0] + `","` + ids[1] + `"],"fields":[],"bogus":1}`, "批量编辑上下文参数无效"},
		{"missing fields", `{"accountIds":["` + ids[0] + `","` + ids[1] + `"]}`, "批量编辑上下文参数无效"},
		{"unknown field", `{"accountIds":["` + ids[0] + `","` + ids[1] + `"],"fields":["bogus"]}`, "批量编辑上下文参数无效"},
		{"too many fields", `{"accountIds":["` + ids[0] + `","` + ids[1] + `"],"fields":["supportedModels","modelMappings","supportedEndpointModes","supportedModels"]}`, "批量编辑上下文参数无效"},
	}
	for _, testCase := range contextCases {
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/batch-edit-context", testCase.body)
		if code != http.StatusBadRequest || payload["message"] != testCase.message {
			t.Fatalf("%s: %d %v (want 400 %s)", testCase.name, code, payload, testCase.message)
		}
	}

	// Missing account → 404 with the Node copy.
	code, missing := env.do(t, http.MethodPost, "/__aisys__/api/accounts/batch-edit-context",
		`{"accountIds":["`+ids[0]+`","acc-missing"],"fields":[]}`)
	if code != http.StatusNotFound || missing["message"] != "批量编辑账户不存在、不可编辑或不属于同一作用域" {
		t.Fatalf("missing context account: %d %v", code, missing)
	}

	// Cross-owner batch is refused with the same-scope copy.
	aliceID := env.login(t, "alice", "alice-pass", "user")
	env.exec(t, `INSERT INTO groups (id, system_account_id, name, provider_code, enabled, is_default, group_type, created_at, updated_at)
		VALUES ('grp-alice', ?, '默认分组', 'gpt', 1, 1, 'personal', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`, aliceID)
	code, aliceCreated := env.do(t, http.MethodPost, "/__aisys__/api/my-accounts", createPayload("alice-batch"))
	if code != http.StatusCreated {
		t.Fatalf("alice create: %d %v", code, aliceCreated)
	}
	aliceAccountID := dataMap(t, aliceCreated)["id"].(string)
	env.login(t, "root", "root-pass", "super_admin")
	code, mixed := env.do(t, http.MethodPost, "/__aisys__/api/accounts/batch-edit-context",
		`{"accountIds":["`+ids[0]+`","`+aliceAccountID+`"],"fields":[]}`)
	if code != http.StatusBadRequest || mixed["message"] != "批量编辑账户必须属于同一系统账户作用域" {
		t.Fatalf("mixed owner context: %d %v", code, mixed)
	}

	// batch-update: the full supported field union in one request
	// (serviceTierOverride stays enabled:false to prove disabled fields are
	// skipped without validation).
	batchBody := `{"targets":[
			{"accountId":"` + ids[0] + `","configRevision":1},
			{"accountId":"` + ids[1] + `","configRevision":1}],
		"updates":{
			"tags":{"enabled":true,"value":["批量","导入"]},
			"proxyProfileId":{"enabled":true,"value":"pp-1"},
			"concurrencyLimit":{"enabled":true,"value":777},
			"priority":{"enabled":true,"value":42},
			"superPriorityEnabled":{"enabled":true,"value":true},
			"fallbackEnabled":{"enabled":true,"value":false},
			"accountExpiresAt":{"enabled":true,"value":"2030-01-01T00:00:00Z"},
			"availabilitySchedule":{"enabled":true,"value":` + alwaysAllowSchedule + `},
			"notes":{"enabled":true,"value":"批量备注"},
			"supportedModels":{"enabled":true,"value":["gpt-4o-mini","gpt-4.1","o3-mini"]},
			"healthCheckModel":{"enabled":true,"value":"gpt-4o-mini"},
			"healthCheckEndpointMode":{"enabled":true,"value":"chat_sse"},
			"modelMappings":{"enabled":true,"value":[{"sourceModel":"o3-mini","sourceEndpointFamily":"responses","upstreamModel":"gpt-4o-mini","upstreamEndpointFamily":"chat_completions","enabled":true}]},
			"serviceTierOverride":{"enabled":false}}}`
	code, updated := env.do(t, http.MethodPost, "/__aisys__/api/accounts/batch-update", batchBody)
	if code != http.StatusOK {
		t.Fatalf("batch-update: %d %v", code, updated)
	}
	result := dataMap(t, updated)
	if batchID, ok := result["batchId"].(string); !ok || !strings.HasPrefix(batchID, "account_batch_") {
		t.Fatalf("batchId: %v", result["batchId"])
	}
	wantChanged := map[string]bool{
		"accountExpiresAt": true, "availabilitySchedule": true, "concurrencyLimit": true,
		"healthCheckEndpointMode": true, "modelMappings": true, "notes": true, "priority": true,
		"proxyProfileId": true, "superPriorityEnabled": true, "supportedModels": true, "tags": true,
	}
	changed := changedFieldSet(t, updated)
	if len(changed) != len(wantChanged) {
		t.Fatalf("changedFields mismatch: %v", result["changedFields"])
	}
	for field := range wantChanged {
		if !changed[field] {
			t.Fatalf("changedFields missing %s: %v", field, result["changedFields"])
		}
	}
	batchItems := result["items"].([]any)
	if len(batchItems) != 2 {
		t.Fatalf("batch items: %v", batchItems)
	}
	// Items follow the locked-account load order (id ASC), like Node.
	if batchItems[0].(map[string]any)["id"].(string) > batchItems[1].(map[string]any)["id"].(string) {
		t.Fatalf("batch items must be id-ordered: %v", batchItems)
	}
	for index := range ids {
		item := batchItems[index].(map[string]any)
		if item["configRevision"] != float64(2) {
			t.Fatalf("batch item %d: %v", index, item)
		}
		if itemChanged := itemChangedFieldSet(t, updated, index); len(itemChanged) != len(wantChanged) {
			t.Fatalf("item changedFields: %v", item["changedFields"])
		}
	}

	// Row contract: the physical columns match the Node repository writes.
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ? AND config_revision = 2 AND priority = 42
		AND concurrency_limit = 777 AND super_priority_enabled = 1 AND fallback_enabled = 0
		AND notes = '批量备注' AND account_expires_at = '2030-01-01T00:00:00.000Z'
		AND health_check_endpoint_mode = 'chat_sse' AND health_check_model = 'gpt-4o-mini'
		AND proxy_profile_id = 'pp-1' AND availability_schedule_json IS NOT NULL
		AND availability_schedule_next_check_at IS NOT NULL AND status = 'active'`, ids[0]) != 1 {
		t.Fatal("batch row contract violated")
	}
	if env.count(t, `SELECT COUNT(*) FROM account_supported_models WHERE account_id = ?`, ids[0]) != 3 {
		t.Fatal("batch supported models replace missing")
	}
	if env.count(t, `SELECT COUNT(*) FROM account_model_mappings WHERE account_id = ? AND source_model = 'o3-mini'`, ids[0]) != 1 {
		t.Fatal("batch model mappings replace missing")
	}
	if env.count(t, `SELECT COUNT(*) FROM account_tag_bindings WHERE account_id = ?`, ids[0]) != 2 {
		t.Fatal("batch tag replace missing")
	}
	if localPriority := env.queryCell(t, `SELECT local_priority FROM group_accounts WHERE account_id = ?`, ids[0]); localPriority != "42" {
		t.Fatalf("dispatch binding local_priority: %v", localPriority)
	}
	if super := env.queryCell(t, `SELECT local_super_priority_enabled FROM group_accounts WHERE account_id = ?`, ids[0]); super != "1" {
		t.Fatalf("dispatch binding local_super_priority_enabled: %v", super)
	}

	// Operation log for the batch.
	seen := map[string]bool{}
	for _, action := range env.sink.actions() {
		seen[action] = true
	}
	if !seen["accounts.batch_update"] {
		t.Fatalf("operation log actions: %v", env.sink.actions())
	}

	// Stale revision on any account → 409 with the per-account copy.
	code, conflict := env.do(t, http.MethodPost, "/__aisys__/api/accounts/batch-update", `{"targets":[
			{"accountId":"`+ids[0]+`","configRevision":1},
			{"accountId":"`+ids[1]+`","configRevision":1}],
		"updates":{"notes":{"enabled":true,"value":"太晚"}}}`)
	if code != http.StatusConflict {
		t.Fatalf("stale batch: %d %v", code, conflict)
	}
	if message, _ := conflict["message"].(string); !strings.HasPrefix(message, "账户配置已发生变化，请刷新后重试：") {
		t.Fatalf("stale batch message: %v", conflict["message"])
	}
	// Nothing was written by the aborted batch.
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE notes = '太晚'`) != 0 {
		t.Fatal("aborted batch must not write")
	}

	// No-op rerun with fresh revisions: changedFields empty, revision kept.
	code, noop := env.do(t, http.MethodPost, "/__aisys__/api/accounts/batch-update", `{"targets":[
			{"accountId":"`+ids[0]+`","configRevision":2},
			{"accountId":"`+ids[1]+`","configRevision":2}],
		"updates":{"concurrencyLimit":{"enabled":true,"value":777}}}`)
	if code != http.StatusOK {
		t.Fatalf("noop batch: %d %v", code, noop)
	}
	if fields := changedFieldSet(t, noop); len(fields) != 0 {
		t.Fatalf("noop changedFields: %v", dataMap(t, noop)["changedFields"])
	}
	for index, item := range dataMap(t, noop)["items"].([]any) {
		if item.(map[string]any)["configRevision"] != float64(2) {
			t.Fatalf("noop keeps the revision: item %d %v", index, item)
		}
	}

	// Validation surface.
	updateCases := []struct {
		name    string
		body    string
		message string
	}{
		{"single target", `{"targets":[{"accountId":"` + ids[0] + `","configRevision":2}],"updates":{"notes":{"enabled":true,"value":"x"}}}`, "批量编辑账户不能重复"},
		{"unknown update field", `{"targets":[{"accountId":"` + ids[0] + `","configRevision":2},{"accountId":"` + ids[1] + `","configRevision":2}],"updates":{"bogus":{"enabled":true,"value":1}}}`, "批量编辑参数无效"},
		{"nothing enabled", `{"targets":[{"accountId":"` + ids[0] + `","configRevision":2},{"accountId":"` + ids[1] + `","configRevision":2}],"updates":{"notes":{"enabled":false}}}`, "请至少选择一项需要覆盖的配置"},
		{"bad revision", `{"targets":[{"accountId":"` + ids[0] + `","configRevision":0},{"accountId":"` + ids[1] + `","configRevision":2}],"updates":{"notes":{"enabled":true,"value":"x"}}}`, "批量编辑参数无效"},
		{"credential rules deferred", `{"targets":[{"accountId":"` + ids[0] + `","configRevision":2},{"accountId":"` + ids[1] + `","configRevision":2}],"updates":{"errorHandlingRules":{"enabled":true,"value":[]}}}`, "批量编辑暂不支持覆盖 errorHandlingRules，请等待凭据配置切片迁移"},
		{"inspection rules deferred", `{"targets":[{"accountId":"` + ids[0] + `","configRevision":2},{"accountId":"` + ids[1] + `","configRevision":2}],"updates":{"responseInspectionRules":{"enabled":true,"value":[]}}}`, "批量编辑暂不支持覆盖 responseInspectionRules，请等待凭据配置切片迁移"},
	}
	for _, testCase := range updateCases {
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/batch-update", testCase.body)
		if code != http.StatusBadRequest || payload["message"] != testCase.message {
			t.Fatalf("%s: %d %v (want 400 %s)", testCase.name, code, payload, testCase.message)
		}
	}

	// Super priority + fallback together is refused.
	code, superFallback := env.do(t, http.MethodPost, "/__aisys__/api/accounts/batch-update", `{"targets":[
			{"accountId":"`+ids[0]+`","configRevision":2},
			{"accountId":"`+ids[1]+`","configRevision":2}],
		"updates":{"superPriorityEnabled":{"enabled":true,"value":true},"fallbackEnabled":{"enabled":true,"value":true}}}`)
	if code != http.StatusBadRequest || superFallback["message"] != "超级优先和降级备用不能同时开启" {
		t.Fatalf("super+fallback: %d %v", code, superFallback)
	}

	// Unknown proxy is refused before any write.
	code, badProxy := env.do(t, http.MethodPost, "/__aisys__/api/accounts/batch-update", `{"targets":[
			{"accountId":"`+ids[0]+`","configRevision":2},
			{"accountId":"`+ids[1]+`","configRevision":2}],
		"updates":{"proxyProfileId":{"enabled":true,"value":"pp-missing"}}}`)
	if code != http.StatusBadRequest || badProxy["message"] != "代理不存在或已停用，请选择一个已启用的代理" {
		t.Fatalf("bad proxy: %d %v", code, badProxy)
	}

	// User role cannot reach the admin batch surface.
	env.login(t, "alice", "alice-pass", "user")
	code, denied := env.do(t, http.MethodPost, "/__aisys__/api/accounts/batch-update", batchBody)
	if code != http.StatusForbidden {
		t.Fatalf("user batch-update: %d %v", code, denied)
	}
	code, _ = env.do(t, http.MethodPost, "/__aisys__/api/accounts/batch-edit-context", `{"accountIds":["a","b"],"fields":[]}`)
	if code != http.StatusForbidden {
		t.Fatalf("user batch-edit-context: %d", code)
	}
}

func importNativeDocument() string {
	return `{
		"type":"juhe-ai-account-import","version":1,
		"proxies":[{"ref":"proxy-1","name":"导入代理","type":"socks5","host":"127.0.0.1","port":1080,"username":"u1","password":"proxy-secret-1"}],
		"accounts":[
			{"ref":"a1","name":"导入一号","providerCode":"gpt","providerProtocolProfileId":"prof-gpt","type":"api_key","status":"active",
			 "credentials":{"api_key":"sk-import-secret-1","base_url":"https://api.openai.com/v1"},
			 "groupName":"导入分组","tags":["导入"],"supportedModels":["gpt-4o-mini"],"notes":"第一条","proxyRef":"proxy-1"},
			{"ref":"a2","name":"导入二号","providerCode":"gpt","providerProtocolProfileId":"prof-gpt","type":"api_key","status":"disabled",
			 "credentials":{"api_key":"sk-import-secret-2","base_url":"https://api.openai.com/v1"},
			 "groupName":"导入分组","supportedModels":["gpt-4o-mini"]},
			{"ref":"a3","name":"导入一号","providerCode":"gpt","providerProtocolProfileId":"prof-gpt","type":"api_key","status":"active",
			 "credentials":{"api_key":"sk-import-secret-3","base_url":"https://api.openai.com/v1"},
			 "groupName":"导入分组","supportedModels":["gpt-4o-mini"]}
		]
	}`
}

func TestAccountImportPreviewAndConfirm(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	seedOpenAICompatibleProvider(t, env)

	baseAccounts := env.count(t, `SELECT COUNT(*) FROM accounts`)
	baseGroups := env.count(t, `SELECT COUNT(*) FROM groups`)
	baseProxies := env.count(t, `SELECT COUNT(*) FROM proxy_profiles`)
	body := `{"data":` + importNativeDocument() + `}`

	// Preview: full plan, zero writes.
	code, preview := env.do(t, http.MethodPost, "/__aisys__/api/accounts/import/preview", body)
	if code != http.StatusOK {
		t.Fatalf("preview: %d %v", code, preview)
	}
	plan := dataMap(t, preview)
	if plan["type"] != "juhe-ai-account-import" || plan["version"] != float64(1) ||
		plan["mode"] != "preview" || plan["canImport"] != true || plan["imported"] != false {
		t.Fatalf("preview envelope: %v", plan)
	}
	summary := plan["summary"].(map[string]any)
	accountsSummary := summary["accounts"].(map[string]any)
	if accountsSummary["total"] != float64(3) || accountsSummary["create"] != float64(2) ||
		accountsSummary["skip"] != float64(1) || accountsSummary["failed"] != float64(0) {
		t.Fatalf("preview account summary: %v", accountsSummary)
	}
	proxiesSummary := summary["proxies"].(map[string]any)
	if proxiesSummary["total"] != float64(1) || proxiesSummary["create"] != float64(1) {
		t.Fatalf("preview proxy summary: %v", proxiesSummary)
	}
	groupsSummary := summary["groups"].(map[string]any)
	if groupsSummary["create"] != float64(1) || groupsSummary["reuse"] != float64(0) {
		t.Fatalf("preview group summary: %v", groupsSummary)
	}
	source := plan["source"].(map[string]any)
	if source["mode"] != "native" {
		t.Fatalf("preview source: %v", source)
	}
	planItems := plan["accounts"].([]any)
	first := planItems[0].(map[string]any)
	if first["action"] != "create" || first["ref"] != "a1" || first["name"] != "导入一号" ||
		first["providerProtocolProfileId"] != "prof-gpt" || first["accountType"] != "api_key" {
		t.Fatalf("preview item a1: %v", first)
	}
	if planItems[2].(map[string]any)["action"] != "skip" {
		t.Fatalf("preview item a3: %v", planItems[2])
	}
	found := false
	for _, message := range planItems[2].(map[string]any)["messages"].([]any) {
		if message.(string) == "与第 1 条账户名称重复" {
			found = true
		}
	}
	if !found {
		t.Fatalf("duplicate message missing: %v", planItems[2])
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts`) != baseAccounts ||
		env.count(t, `SELECT COUNT(*) FROM groups`) != baseGroups ||
		env.count(t, `SELECT COUNT(*) FROM proxy_profiles`) != baseProxies {
		t.Fatal("preview must not write")
	}

	// Invalid bodies.
	code, invalid := env.do(t, http.MethodPost, "/__aisys__/api/accounts/import/preview", `{"bogus":1}`)
	if code != http.StatusBadRequest || invalid["message"] != "账户导入参数无效" {
		t.Fatalf("invalid import body: %d %v", code, invalid)
	}
	code, badSource := env.do(t, http.MethodPost, "/__aisys__/api/accounts/import/preview", `{"data":{},"sourceMode":"unknown"}`)
	if code != http.StatusBadRequest || badSource["message"] != "账户导入参数无效" {
		t.Fatalf("unknown sourceMode: %d %v", code, badSource)
	}

	// Root validation failures surface as messages with canImport=false.
	code, badRoot := env.do(t, http.MethodPost, "/__aisys__/api/accounts/import/preview",
		`{"data":{"type":"wrong","version":1,"accounts":[]}}`)
	if code != http.StatusOK {
		t.Fatalf("bad root: %d %v", code, badRoot)
	}
	if dataMap(t, badRoot)["canImport"] != false {
		t.Fatalf("bad root canImport: %v", badRoot)
	}
	messages := dataMap(t, badRoot)["messages"].([]any)
	if len(messages) == 0 || messages[0].(string) != "type 必须是 juhe-ai-account-import" {
		t.Fatalf("bad root messages: %v", messages)
	}

	// Confirm: executes the plan.
	code, confirmed := env.do(t, http.MethodPost, "/__aisys__/api/accounts/import/confirm", body)
	if code != http.StatusOK {
		t.Fatalf("confirm: %d %v", code, confirmed)
	}
	result := dataMap(t, confirmed)
	if result["mode"] != "import" || result["imported"] != true || result["canImport"] != false {
		t.Fatalf("confirm envelope: %v", result)
	}
	confirmItems := result["accounts"].([]any)
	a1 := confirmItems[0].(map[string]any)
	a2 := confirmItems[1].(map[string]any)
	if a1["accountId"] == nil || a1["messages"].([]any)[0].(string) != "已创建账户，等待后台健康检查通过后参与调度" {
		t.Fatalf("confirm a1: %v", a1)
	}
	if a2["accountId"] == nil || a2["messages"].([]any)[0].(string) != "已创建账户" {
		t.Fatalf("confirm a2: %v", a2)
	}
	if confirmItems[2].(map[string]any)["accountId"] != nil {
		t.Fatalf("confirm a3 must stay skipped: %v", confirmItems[2])
	}
	proxyItems := result["proxies"].([]any)
	if len(proxyItems) != 1 || proxyItems[0].(map[string]any)["proxyProfileId"] == nil {
		t.Fatalf("confirm proxies: %v", proxyItems)
	}

	// Rows: accounts created pending_test/disabled with sealed credentials,
	// group + proxy materialized, bindings in place.
	if env.count(t, `SELECT COUNT(*) FROM accounts`) != baseAccounts+2 {
		t.Fatal("confirm account count")
	}
	createdID := a1["accountId"].(string)
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ? AND status = 'pending_test' AND schedulable = 0
		AND system_account_id = ? AND provider_code = 'gpt' AND notes = '第一条'`, createdID, adminID) != 1 {
		t.Fatal("created account row contract violated")
	}
	sealed := env.queryCell(t, `SELECT credentials_encrypted FROM accounts WHERE id = ?`, createdID)
	if strings.Contains(sealed, "sk-import-secret-1") {
		t.Fatal("imported credentials must be sealed")
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ? AND status = 'disabled'`, a2["accountId"].(string)) != 1 {
		t.Fatal("disabled import must keep the status")
	}
	if env.count(t, `SELECT COUNT(*) FROM groups WHERE name = '导入分组' AND system_account_id = ? AND provider_code = 'gpt'
		AND is_default = 0 AND group_type = 'personal'`, adminID) != 1 {
		t.Fatal("imported group missing")
	}
	if env.count(t, `SELECT COUNT(*) FROM group_accounts WHERE account_id IN (?, ?)`, createdID, a2["accountId"].(string)) != 2 {
		t.Fatal("imported group bindings missing")
	}
	if env.count(t, `SELECT COUNT(*) FROM account_tag_bindings WHERE account_id = ?`, createdID) != 1 {
		t.Fatal("imported tags missing")
	}
	proxyID := proxyItems[0].(map[string]any)["proxyProfileId"].(string)
	proxyPassword := env.queryCell(t, `SELECT password_encrypted FROM proxy_profiles WHERE id = ?`, proxyID)
	if strings.Contains(proxyPassword, "proxy-secret-1") {
		t.Fatal("imported proxy password must be sealed")
	}
	if bound := env.queryCell(t, `SELECT proxy_profile_id FROM accounts WHERE id = ?`, createdID); bound != proxyID {
		t.Fatalf("proxy binding: %v", bound)
	}
	if env.count(t, `SELECT COUNT(*) FROM account_supported_models WHERE account_id = ? AND model = 'gpt-4o-mini'`, createdID) != 1 {
		t.Fatal("imported supported models missing")
	}

	seen := map[string]bool{}
	for _, action := range env.sink.actions() {
		seen[action] = true
	}
	if !seen["accounts.import"] {
		t.Fatalf("operation log actions: %v", env.sink.actions())
	}

	// The identical confirm body is deduplicated by the mutation guard.
	code, duplicate := env.do(t, http.MethodPost, "/__aisys__/api/accounts/import/confirm", body)
	if code != http.StatusConflict {
		t.Fatalf("duplicate confirm: %d %v", code, duplicate)
	}

	// skipDuplicates=false turns duplicate names into plan failures.
	code, strictPreview := env.do(t, http.MethodPost, "/__aisys__/api/accounts/import/preview",
		`{"data":`+importNativeDocument()+`,"options":{"skipDuplicates":false}}`)
	if code != http.StatusOK || dataMap(t, strictPreview)["canImport"] != false {
		t.Fatalf("strict preview: %d %v", code, strictPreview)
	}
	strictSummary := dataMap(t, strictPreview)["summary"].(map[string]any)["accounts"].(map[string]any)
	if strictSummary["failed"] != float64(1) || strictSummary["create"] != float64(2) {
		t.Fatalf("strict preview summary: %v", strictSummary)
	}

	// A second import reuses the existing group by name (no create).
	code, reuse := env.do(t, http.MethodPost, "/__aisys__/api/accounts/import/confirm", `{"data":{
		"type":"juhe-ai-account-import","version":1,"proxies":[],
		"accounts":[{"name":"导入三号","providerCode":"gpt","providerProtocolProfileId":"prof-gpt","type":"api_key",
			"status":"active","credentials":{"api_key":"sk-import-secret-4","base_url":"https://api.openai.com/v1"},
			"groupName":"导入分组","supportedModels":["gpt-4o-mini"]}]}}`)
	if code != http.StatusOK {
		t.Fatalf("reuse confirm: %d %v", code, reuse)
	}
	reuseResult := dataMap(t, reuse)
	reuseSummary := reuseResult["summary"].(map[string]any)["groups"].(map[string]any)
	if reuseSummary["create"] != float64(0) || reuseSummary["reuse"] != float64(1) {
		t.Fatalf("reuse summary: %v", reuseSummary)
	}
	if env.count(t, `SELECT COUNT(*) FROM groups WHERE name = '导入分组'`) != 1 {
		t.Fatal("group must be reused, not recreated")
	}

	// createMissingGroups=false fails the plan with the group message.
	code, noGroup := env.do(t, http.MethodPost, "/__aisys__/api/accounts/import/preview", `{"data":{
		"type":"juhe-ai-account-import","version":1,
		"accounts":[{"name":"导入四号","providerCode":"gpt","providerProtocolProfileId":"prof-gpt","type":"api_key",
			"status":"active","credentials":{"api_key":"sk-import-secret-5"},"groupName":"缺失分组","supportedModels":["gpt-4o-mini"]}]},
		"options":{"createMissingGroups":false}}`)
	if code != http.StatusOK || dataMap(t, noGroup)["canImport"] != false {
		t.Fatalf("no-group preview: %d %v", code, noGroup)
	}
	item := dataMap(t, noGroup)["accounts"].([]any)[0].(map[string]any)
	if item["action"] != "failed" || item["messages"].([]any)[0].(string) != "分组不存在：缺失分组" {
		t.Fatalf("no-group item: %v", item)
	}
}

func TestAccountImportSourceModesAndConfirm(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	seedOpenAICompatibleProvider(t, env)

	// sub2api: the source document is rewritten into the native plan.
	code, sub2api := env.do(t, http.MethodPost, "/__aisys__/api/accounts/import/preview", `{
		"data":{"data":{"accounts":[{"name":"s2-一号","platform":"openai","type":"api_key",
			"credentials":{"api_key":"sk-s2-secret","base_url":"https://api.openai.com/v1"},
			"concurrency":5,"priority":2,"notes":"来自Sub2API"}]},"proxies":[]},
		"sourceMode":"sub2api"}`)
	if code != http.StatusOK {
		t.Fatalf("sub2api preview: %d %v", code, sub2api)
	}
	plan := dataMap(t, sub2api)
	source := plan["source"].(map[string]any)
	if source["mode"] != "sub2api" || source["records"] != float64(1) || source["accepted"] != float64(1) {
		t.Fatalf("sub2api source summary: %v", source)
	}
	item := plan["accounts"].([]any)[0].(map[string]any)
	if item["action"] != "create" || item["name"] != "s2-一号" ||
		item["providerCode"] != "openai" || item["providerProtocolProfileId"] != "profile_openai_openai_v1" {
		t.Fatalf("sub2api planned item: %v", item)
	}
	if plan["summary"].(map[string]any)["accounts"].(map[string]any)["create"] != float64(1) {
		t.Fatalf("sub2api summary: %v", plan["summary"])
	}

	// The sub2api plan confirms into real rows (default models applied).
	code, confirmed := env.do(t, http.MethodPost, "/__aisys__/api/accounts/import/confirm", `{
		"data":{"data":{"accounts":[{"name":"s2-二号","platform":"openai","type":"api_key",
			"credentials":{"api_key":"sk-s2-secret-2","base_url":"https://api.openai.com/v1"},
			"concurrency":6,"priority":3,"notes":"来自Sub2API"}]},"proxies":[]},
		"sourceMode":"sub2api"}`)
	if code != http.StatusOK {
		t.Fatalf("sub2api confirm: %d %v", code, confirmed)
	}
	confirmItem := dataMap(t, confirmed)["accounts"].([]any)[0].(map[string]any)
	if confirmItem["accountId"] == nil {
		t.Fatalf("sub2api confirm item: %v", confirmItem)
	}
	createdID := confirmItem["accountId"].(string)
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ? AND concurrency_limit = 6 AND priority = 3
		AND provider_protocol_profile_id = 'profile_openai_openai_v1' AND notes = '来自Sub2API'`, createdID) != 1 {
		t.Fatal("sub2api row contract violated")
	}
	if env.count(t, `SELECT COUNT(*) FROM account_supported_models WHERE account_id = ? AND model = 'gpt-4o-mini'`, createdID) != 1 {
		t.Fatal("sub2api default supported models missing")
	}

	// cpa: openai-compatibility providers become api_key accounts.
	code, cpa := env.do(t, http.MethodPost, "/__aisys__/api/accounts/import/preview", `{
		"data":{"openai-compatibility":[{"name":"cpa-provider","base-url":"https://cpa.example.com/v1",
			"api-key-entries":[{"api-key":"sk-cpa-secret"}]}]},
		"sourceMode":"cpa"}`)
	if code != http.StatusOK {
		t.Fatalf("cpa preview: %d %v", code, cpa)
	}
	cpaPlan := dataMap(t, cpa)
	cpaSource := cpaPlan["source"].(map[string]any)
	if cpaSource["mode"] != "cpa" || cpaSource["accepted"] != float64(1) {
		t.Fatalf("cpa source summary: %v", cpaSource)
	}
	cpaItem := cpaPlan["accounts"].([]any)[0].(map[string]any)
	if cpaItem["action"] != "create" || cpaItem["name"] != "cpa-provider 1" {
		t.Fatalf("cpa planned item: %v", cpaItem)
	}

	// newapi: channel records become failover api_key accounts; disabled or
	// non-openai channels are skipped at the source layer.
	code, newapi := env.do(t, http.MethodPost, "/__aisys__/api/accounts/import/preview", `{
		"data":[{"type":1,"key":"sk-newapi-secret","name":"渠道一","status":1,"group":"渠道分组"},
			{"type":15,"key":"sk-other","name":"渠道二"}],
		"sourceMode":"newapi"}`)
	if code != http.StatusOK {
		t.Fatalf("newapi preview: %d %v", code, newapi)
	}
	newapiPlan := dataMap(t, newapi)
	newapiSource := newapiPlan["source"].(map[string]any)
	if newapiSource["records"] != float64(2) || newapiSource["accepted"] != float64(1) || newapiSource["skipped"] != float64(1) {
		t.Fatalf("newapi source summary: %v", newapiSource)
	}

	// oneapi with a multi-key channel creates the failover strategy.
	code, oneapi := env.do(t, http.MethodPost, "/__aisys__/api/accounts/import/preview", `{
		"data":{"channels":[{"type":"openai","key":"sk-one-1\nsk-one-2","name":"oneapi-渠道"}]},
		"sourceMode":"oneapi"}`)
	if code != http.StatusOK {
		t.Fatalf("oneapi preview: %d %v", code, oneapi)
	}
	oneapiPlan := dataMap(t, oneapi)
	if oneapiPlan["source"].(map[string]any)["accepted"] != float64(1) {
		t.Fatalf("oneapi source summary: %v", oneapiPlan["source"])
	}
	if oneapiPlan["canImport"] != true {
		t.Fatalf("oneapi plan: %v", oneapiPlan)
	}
}

func TestAccountExportByIdsAndFilters(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	code, alpha := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("alpha"))
	if code != http.StatusCreated {
		t.Fatalf("create alpha: %d %v", code, alpha)
	}
	alphaID := dataMap(t, alpha)["id"].(string)
	code, bravo := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("bravo"))
	if code != http.StatusCreated {
		t.Fatalf("create bravo: %d %v", code, bravo)
	}
	bravoID := dataMap(t, bravo)["id"].(string)
	env.exec(t, `UPDATE accounts SET status = 'disabled' WHERE id = ?`, bravoID)
	code, pending := env.do(t, http.MethodPost, "/__aisys__/api/accounts",
		`{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"charlie","type":"api_key",
		"credentials":{"api_key":"sk-pending-secret","base_url":"https://api.openai.com/v1"},
		"supportedModels":["gpt-4o-mini"]}`)
	if code != http.StatusCreated {
		t.Fatalf("create charlie: %d %v", code, pending)
	}
	pendingID := dataMap(t, pending)["id"].(string)

	seedImportProxy(t, env, "pp-exp", adminID, "导出代理")
	env.exec(t, `UPDATE accounts SET proxy_profile_id = 'pp-exp' WHERE id = ?`, alphaID)
	// An authorization instance never exports (skipped like a missing row).
	now := time.Now().UTC().Format(time.RFC3339Nano)
	env.exec(t, `INSERT INTO accounts (id, system_account_id, provider_code, provider_protocol_profile_id,
		protocol_code, protocol_version, name, type, status, credentials_encrypted, credential_mask,
		health_check_model, authorization_instance_source_account_id, created_at, updated_at)
		VALUES ('acc-authz', ?, 'gpt', 'prof-gpt', 'openai', 'v1', '授权实例', 'api_key', 'active',
		'sealed', 'masked', 'gpt-4o-mini', 'acc-owner', ?, ?)`, adminID, now, now)

	byIDs := `{"accountIds":["` + alphaID + `","` + bravoID + `","` + pendingID + `","acc-authz","acc-missing"]}`
	code, exported := env.do(t, http.MethodPost, "/__aisys__/api/accounts/export", byIDs)
	if code != http.StatusOK {
		t.Fatalf("export: %d %v", code, exported)
	}
	result := dataMap(t, exported)
	document := result["document"].(map[string]any)
	if document["type"] != "juhe-ai-account-import" || document["version"] != float64(1) {
		t.Fatalf("export document envelope: %v", document)
	}
	accounts := document["accounts"].([]any)
	if len(accounts) != 3 {
		t.Fatalf("export accounts: %v", accounts)
	}
	summary := result["summary"].(map[string]any)
	if summary["accounts"] != float64(3) || summary["skippedAccounts"] != float64(2) {
		t.Fatalf("export summary: %v", summary)
	}
	// Request order is preserved.
	if accounts[0].(map[string]any)["ref"] != alphaID || accounts[1].(map[string]any)["ref"] != bravoID {
		t.Fatalf("export order: %v", accounts)
	}

	alphaDoc := accounts[0].(map[string]any)
	if alphaDoc["name"] != "alpha" || alphaDoc["providerCode"] != "gpt" || alphaDoc["type"] != "api_key" ||
		alphaDoc["status"] != "active" || alphaDoc["healthCheckEndpointMode"] != "chat_json" {
		t.Fatalf("alpha export: %v", alphaDoc)
	}
	credentials := alphaDoc["credentials"].(map[string]any)
	if credentials["api_key"] != "sk-live-secret-1234567890" || credentials["base_url"] != "https://api.openai.com/v1" {
		t.Fatalf("export credentials whitelist: %v", credentials)
	}
	if _, hasStrategy := credentials["api_key_strategy"]; hasStrategy {
		t.Fatalf("single key must not carry strategy: %v", credentials)
	}
	models := alphaDoc["supportedModels"].([]any)
	if len(models) != 2 || models[0].(string) != "gpt-4.1" {
		t.Fatalf("export supportedModels: %v", models)
	}
	tags := alphaDoc["tags"].([]any)
	if len(tags) != 2 || tags[0].(string) != "主力" || tags[1].(string) != "生产" {
		t.Fatalf("export tags: %v", tags)
	}
	if alphaDoc["groupName"] != "默认分组" || alphaDoc["proxyRef"] != "proxy-pp-exp" {
		t.Fatalf("alpha group/proxy: %v", alphaDoc)
	}
	if alphaDoc["concurrencyLimit"] != float64(5000) || alphaDoc["priority"] != float64(0) {
		t.Fatalf("alpha dispatch fields: %v", alphaDoc)
	}
	// Empty notes stay omitted (Node only assigns truthy notes).
	if _, hasNotes := alphaDoc["notes"]; hasNotes {
		t.Fatalf("empty notes must stay omitted: %v", alphaDoc)
	}
	proxies := document["proxies"].([]any)
	if len(proxies) != 1 {
		t.Fatalf("export proxies: %v", proxies)
	}
	proxy := proxies[0].(map[string]any)
	if proxy["ref"] != "proxy-pp-exp" || proxy["name"] != "导出代理" || proxy["type"] != "socks5" ||
		proxy["host"] != "127.0.0.1" || proxy["port"] != float64(1080) || proxy["enabled"] != true {
		t.Fatalf("export proxy entry: %v", proxy)
	}

	bravoDoc := accounts[1].(map[string]any)
	if bravoDoc["status"] != "disabled" {
		t.Fatalf("bravo export status: %v", bravoDoc)
	}
	if _, hasSuper := bravoDoc["superPriorityEnabled"]; hasSuper {
		t.Fatalf("disabled account must not carry super priority: %v", bravoDoc)
	}
	pendingDoc := accounts[2].(map[string]any)
	if pendingDoc["status"] != "pending_test" {
		t.Fatalf("pending export status: %v", pendingDoc)
	}
	if _, hasProbe := pendingDoc["temporaryUnavailableContinuousProbeEnabled"]; hasProbe {
		t.Fatalf("default probe flag stays omitted: %v", pendingDoc)
	}

	// Export by filters: matched count rides along.
	code, filtered := env.do(t, http.MethodPost, "/__aisys__/api/accounts/export", `{"filters":{"keyword":"alpha"}}`)
	if code != http.StatusOK {
		t.Fatalf("filtered export: %d %v", code, filtered)
	}
	filteredResult := dataMap(t, filtered)
	if len(filteredResult["document"].(map[string]any)["accounts"].([]any)) != 1 {
		t.Fatalf("filtered export accounts: %v", filteredResult)
	}
	if filteredResult["summary"].(map[string]any)["matchedAccounts"] != float64(1) {
		t.Fatalf("matchedAccounts: %v", filteredResult["summary"])
	}

	// Empty filter match → 400 with the Node copy.
	code, empty := env.do(t, http.MethodPost, "/__aisys__/api/accounts/export", `{"filters":{"keyword":"zzz-none"}}`)
	if code != http.StatusBadRequest || empty["message"] != "当前筛选条件下没有匹配的 AI 账户" {
		t.Fatalf("empty filter export: %d %v", code, empty)
	}
	// Invalid body → 400 with the ceiling copy.
	code, invalid := env.do(t, http.MethodPost, "/__aisys__/api/accounts/export", `{"bogus":1}`)
	if code != http.StatusBadRequest || invalid["message"] != "账户导出参数无效，单次最多导出 500 个账户" {
		t.Fatalf("invalid export body: %d %v", code, invalid)
	}

	seen := map[string]bool{}
	for _, action := range env.sink.actions() {
		seen[action] = true
	}
	if !seen["accounts.export"] {
		t.Fatalf("operation log actions: %v", env.sink.actions())
	}

	// The self surface exports the caller's own accounts.
	aliceID := env.login(t, "alice", "alice-pass", "user")
	env.exec(t, `INSERT INTO groups (id, system_account_id, name, provider_code, enabled, is_default, group_type, created_at, updated_at)
		VALUES ('grp-alice-exp', ?, '默认分组', 'gpt', 1, 1, 'personal', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`, aliceID)
	code, aliceAccount := env.do(t, http.MethodPost, "/__aisys__/api/my-accounts", createPayload("alice-export"))
	if code != http.StatusCreated {
		t.Fatalf("alice create: %d %v", code, aliceAccount)
	}
	aliceAccountID := dataMap(t, aliceAccount)["id"].(string)
	code, selfExport := env.do(t, http.MethodPost, "/__aisys__/api/my-accounts/export",
		`{"accountIds":["`+aliceAccountID+`","`+alphaID+`"]}`)
	if code != http.StatusOK {
		t.Fatalf("self export: %d %v", code, selfExport)
	}
	selfAccounts := dataMap(t, selfExport)["document"].(map[string]any)["accounts"].([]any)
	if len(selfAccounts) != 1 || selfAccounts[0].(map[string]any)["ref"] != aliceAccountID {
		t.Fatalf("self export must pin the caller scope: %v", selfAccounts)
	}
	// User exports record the owner viewer.
	viewed := false
	for _, entry := range env.sink.entries {
		if entry.Module == "accounts" && entry.Action == "export" {
			for _, viewer := range entry.Viewers {
				if viewer.SystemAccountID == aliceID {
					viewed = true
				}
			}
		}
	}
	if !viewed {
		t.Fatal("user export must record the resource owner viewer")
	}

	// The my-* surface is force-self-scoped even for admins: alice's account
	// is invisible through the admin's my-* export.
	env.login(t, "root", "root-pass", "super_admin")
	code, adminSelf := env.do(t, http.MethodPost, "/__aisys__/api/my-accounts/export",
		`{"accountIds":["`+aliceAccountID+`"]}`)
	if code != http.StatusBadRequest || adminSelf["message"] != "没有可导出的自有 AI 账户" {
		t.Fatalf("admin my export must be self-scoped: %d %v", code, adminSelf)
	}
}

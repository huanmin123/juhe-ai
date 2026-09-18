// w14d_lifecycle_variants_test.go drives the handler paths that the base
// lifecycles leave open: auto-created targets, the existing-resource short
// circuit, owned-resource resolution without an explicit username, the
// strategy config arms, api-key optional patches and the provider state
// diagnostics.
package aipublic

import (
	"net/http"
	"strings"
	"testing"
)

func TestW14dAccountAddAutoCreatesTargetAndGroup(t *testing.T) {
	env := newAIPublicEnv(t)
	env.seedProvider("gpt", true)
	if _, err := env.db.Exec(`INSERT INTO provider_protocol_profiles
		(id, provider_code, name, enabled, protocol_code, protocol_version, base_url, default_health_check_model, account_types_json, capabilities_json, created_at, updated_at)
		VALUES ('profile_gpt_openai_v1', 'gpt', 'OpenAI v1', 1, 'openai', 'v1', 'https://api.openai.com', 'gpt-4o-mini', '["api_key"]', '{}', datetime('now'), datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	scopes := []string{"juhe_ai_public:account_list:read", "juhe_ai_public:account_add:write",
		"juhe_ai_public:account_update:write", "juhe_ai_public:account_delete:write"}
	token := env.seedSource("w14d_ext", "w14d_tok", "juis_token_aut0creat3d1111",
		"active", "active", scopes, "[]", "", "")

	body := `{"targetUsername":"w14dbrandnew","targetDisplayName":"新目标","targetGroupName":"自动组",
		"name":"acc1","providerCode":"gpt","providerProtocolProfileId":"profile_gpt_openai_v1",
		"type":"api_key","baseUrl":"https://a.example","apiKey":"sk-x",
		"supportedModels":["gpt-4o-mini"],"status":"active","concurrencyLimit":5,"priority":1}`
	status, payload, _ := env.doAuth(http.MethodPost, "/__aipublic__/account/add", body, token)
	if status != http.StatusCreated {
		t.Fatalf("auto-created add: %d %v", status, payload)
	}
	data := payload["data"].(map[string]any)
	target := data["target"].(map[string]any)
	if target["created"] != true {
		t.Fatalf("target created flag: %v", target)
	}
	// The group info rides the target block (groupName/groupCreated).
	if target["groupCreated"] != true || target["groupName"] != "自动组" {
		t.Fatalf("target group info: %v", target)
	}

	// A second identical push renders the 409 duplicate-account contract.
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/account/add", body, token)
	if status != http.StatusConflict || !strings.Contains(payload["message"].(string), "账号已存在") {
		t.Fatalf("duplicate add: %d %v", status, payload)
	}

	// The disabled provider renders the verbatim diagnostic.
	env.seedProvider("off", false)
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/account/add",
		`{"targetUsername":"w14dbrandnew","targetGroupName":"自动组","name":"acc2","type":"api_key","baseUrl":"https://a.example","providerCode":"off","providerProtocolProfileId":"profile_gpt_openai_v1","apiKey":"sk-x","supportedModels":["m"]}`, token)
	if status != http.StatusBadRequest || payload["message"] != "供应商已停用：off" {
		t.Fatalf("disabled provider: %d %v", status, payload)
	}
	// An unknown provider renders the unsupported diagnostic.
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/account/add",
		`{"targetUsername":"w14dbrandnew","targetGroupName":"自动组","name":"acc3","type":"api_key","baseUrl":"https://a.example","providerCode":"mystery","providerProtocolProfileId":"profile_gpt_openai_v1","apiKey":"sk-x","supportedModels":["m"]}`, token)
	if status != http.StatusBadRequest || payload["message"] != "不支持的供应商：mystery" {
		t.Fatalf("unknown provider: %d %v", status, payload)
	}
}

func TestW14dApiKeyOptionalPatches(t *testing.T) {
	env := newAIPublicEnv(t)
	env.seedProvider("gpt", true)
	env.seedTargetUser("w14d_keyer", "w14dkeyer", "active")
	scopes := []string{"juhe_ai_public:group_add:write", "juhe_ai_public:group_list:read",
		"juhe_ai_public:route_strategy_add:write", "juhe_ai_public:route_strategy_list:read",
		"juhe_ai_public:api_key_list:read", "juhe_ai_public:api_key_add:write",
		"juhe_ai_public:api_key_update:write", "juhe_ai_public:api_key_delete:write"}
	token := env.seedSource("w14d_ext", "w14d_tok2", "juis_token_opt10pt10pt1",
		"active", "active", scopes, "[]", "", "")

	// Group + strategy through the stores via the HTTP add paths.
	env.doAuth(http.MethodPost, "/__aipublic__/group/add",
		`{"targetUsername":"w14dkeyer","name":"Key组","providerCode":"gpt"}`, token)
	if status, _, _ := env.doAuth(http.MethodGet, "/__aipublic__/group/list?targetUsername=w14dkeyer", "", token); status != http.StatusOK {
		t.Fatalf("group list: %d", status)
	}
	status, payload, _ := env.doAuth(http.MethodPost, "/__aipublic__/route-strategy/add",
		`{"targetUsername":"w14dkeyer","name":"Key策略","mode":"normal","groupBindings":[{"groupId":null}]}`, token)
	if status == http.StatusCreated {
		t.Fatalf("null binding must fail: %d %v", status, payload)
	}

	var groupID, strategyID string
	if err := env.db.QueryRow(`SELECT id FROM groups LIMIT 1`).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.db.Exec(`INSERT INTO route_strategies (id, system_account_id, name, mode, status, is_default, created_at, updated_at)
		VALUES ('w14d_rs', (SELECT id FROM system_accounts WHERE username='w14dkeyer'), 'Key策略', 'normal', 'active', 0, datetime('now'), datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := env.db.Exec(`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at, updated_at)
		VALUES ('w14d_rsg', 'w14d_rs', (SELECT id FROM system_accounts WHERE username='w14dkeyer'), ?, 1, 100, 'active', datetime('now'), datetime('now'))`, groupID); err != nil {
		t.Fatal(err)
	}
	_ = strategyID

	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/api-key/add",
		`{"targetUsername":"w14dkeyer","name":"Key","routeStrategyId":"w14d_rs","description":"d","expiresAt":"2030-01-01T00:00:00Z","quotaLimits":{}}`, token)
	if status != http.StatusCreated {
		t.Fatalf("api key add: %d %v", status, payload)
	}
	apiKeyID := payload["data"].(map[string]any)["apiKey"].(map[string]any)["id"].(string)

	// The optional patch arms: rename and flip the status.
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/api-key/update",
		`{"targetUsername":"w14dkeyer","apiKeyId":"`+apiKeyID+`","name":"Key2","status":"disabled"}`, token)
	if status != http.StatusOK {
		t.Fatalf("api key update patch: %d %v", status, payload)
	}

	// An unknown api key with an explicit username renders the not-found
	// envelope while exercising the owned-target resolution.
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/api-key/update",
		`{"targetUsername":"w14dkeyer","apiKeyId":"ak-missing","name":"x"}`, token)
	if status == http.StatusOK {
		t.Fatalf("missing api key must not succeed: %v", payload)
	}
}

func TestW14dStrategyAddWithConfigs(t *testing.T) {
	env := newAIPublicEnv(t)
	env.seedProvider("gpt", true)
	env.seedTargetUser("w14d_strat", "w14dstrat", "active")
	scopes := []string{"juhe_ai_public:group_add:write", "juhe_ai_public:route_strategy_add:write",
		"juhe_ai_public:route_strategy_update:write", "juhe_ai_public:route_strategy_delete:write"}
	token := env.seedSource("w14d_ext", "w14d_tok3", "juis_token_str4tegy111",
		"active", "active", scopes, "[]", "", "")

	if status, payload, _ := env.doAuth(http.MethodPost, "/__aipublic__/group/add",
		`{"targetUsername":"w14dstrat","name":"策略组","providerCode":"gpt"}`, token); status != http.StatusCreated {
		t.Fatalf("group add for strategy: %d %v", status, payload)
	}
	var groupID string
	if err := env.db.QueryRow(`SELECT id FROM groups LIMIT 1`).Scan(&groupID); err != nil {
		t.Fatal(err)
	}

	body := `{"targetUsername":"w14dstrat","name":"混合策略","mode":"normal",
		"description":"d","status":"active",
		"groupBindings":[{"groupId":"` + groupID + `","priority":1,"weight":100,"status":"active"}],
		"normalRoutingConfig":{"keys":["a"]}}`
	status, payload, _ := env.doAuth(http.MethodPost, "/__aipublic__/route-strategy/add", body, token)
	if status != http.StatusCreated {
		t.Fatalf("strategy add: %d %v", status, payload)
	}
	created := payload["data"].(map[string]any)
	createdStrategy, ok := created["routeStrategy"].(map[string]any)
	if !ok {
		t.Fatalf("strategy add envelope: %v", created)
	}
	strategyID := createdStrategy["id"].(string)

	// The update path rides the expected-version from the row.
	update := `{"targetUsername":"w14dstrat","routeStrategyId":"` + strategyID + `","name":"混合策略2","status":"disabled"}`
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/route-strategy/update", update, token)
	if status != http.StatusOK {
		t.Fatalf("strategy update: %d %v", status, payload)
	}

	// Delete removes it.
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/route-strategy/del",
		`{"targetUsername":"w14dstrat","routeStrategyId":"`+strategyID+`"}`, token)
	if status != http.StatusOK {
		t.Fatalf("strategy delete: %d %v", status, payload)
	}
}

func TestW14dGroupUpdateAndDeletePaths(t *testing.T) {
	env := newAIPublicEnv(t)
	env.seedProvider("gpt", true)
	env.seedTargetUser("w14d_grpr", "w14dgrpr", "active")
	scopes := []string{"juhe_ai_public:group_list:read", "juhe_ai_public:group_add:write",
		"juhe_ai_public:group_update:write", "juhe_ai_public:group_delete:write"}
	token := env.seedSource("w14d_ext", "w14d_tok4", "juis_token_gr0up12345678",
		"active", "active", scopes, "[]", "", "")

	status, payload, _ := env.doAuth(http.MethodPost, "/__aipublic__/group/add",
		`{"targetUsername":"w14dgrpr","name":"组A","providerCode":"gpt","description":"first"}`, token)
	if status != http.StatusCreated {
		t.Fatalf("group add: %d %v", status, payload)
	}
	groupID := payload["data"].(map[string]any)["group"].(map[string]any)["id"].(string)

	// Re-adding the same name short-circuits through the existing arm.
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/group/add",
		`{"targetUsername":"w14dgrpr","name":"组A","providerCode":"gpt"}`, token)
	if status != http.StatusOK || payload["data"].(map[string]any)["action"] != "existing" {
		t.Fatalf("group existing: %d %v", status, payload)
	}

	// The update patch applies the optional arms.
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/group/update",
		`{"targetUsername":"w14dgrpr","groupId":"`+groupID+`","name":"组B","providerCode":"gpt","description":null,"enabled":false,"groupType":"high_concurrency"}`, token)
	if status != http.StatusOK {
		t.Fatalf("group update: %d %v", status, payload)
	}

	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/group/del",
		`{"targetUsername":"w14dgrpr","groupId":"`+groupID+`"}`, token)
	if status != http.StatusOK {
		t.Fatalf("group delete: %d %v", status, payload)
	}
	// Double delete renders the not-found envelope.
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/group/del",
		`{"targetUsername":"w14dgrpr","groupId":"`+groupID+`"}`, token)
	if status != http.StatusNotFound {
		t.Fatalf("group re-delete: %d %v", status, payload)
	}
}

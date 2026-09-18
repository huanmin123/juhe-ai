// w14d_dropped_tables_test.go covers the handler error branches that only
// trigger on storage failures: dropping the domain tables turns the read,
// mutation and delete routes into their 500 arms while the auth tables stay
// intact so the guard still passes.
package aipublic

import (
	"net/http"
	"testing"
)

// w14dSeedAllScopes provisions a provider, an active target user and a source
// token carrying every public scope.
func w14dSeedAllScopes(t *testing.T, env *aipublicEnv, sourceID, tokenID, token string) string {
	t.Helper()
	env.seedProvider("gpt", true)
	env.seedTargetUser("user_w14downer", "w14downer", "active")
	scopes := []string{
		"juhe_ai_public:group_list:read", "juhe_ai_public:group_add:write",
		"juhe_ai_public:group_update:write", "juhe_ai_public:group_delete:write",
		"juhe_ai_public:route_strategy_list:read", "juhe_ai_public:route_strategy_add:write",
		"juhe_ai_public:route_strategy_update:write", "juhe_ai_public:route_strategy_delete:write",
		"juhe_ai_public:api_key_list:read", "juhe_ai_public:api_key_add:write",
		"juhe_ai_public:api_key_update:write", "juhe_ai_public:api_key_delete:write",
		"juhe_ai_public:account_list:read", "juhe_ai_public:account_add:write",
		"juhe_ai_public:account_update:write", "juhe_ai_public:account_delete:write",
	}
	return env.seedSource(sourceID, tokenID, token, "active", "active", scopes, "[]", "", "")
}

func w14dDropTable(t *testing.T, env *aipublicEnv, table string) {
	t.Helper()
	if _, err := env.db.Exec(`DROP TABLE ` + table); err != nil {
		t.Fatalf("drop %s: %v", table, err)
	}
}

func TestW14dDroppedGroupTables(t *testing.T) {
	env := newAIPublicEnv(t)
	token := w14dSeedAllScopes(t, env, "extsrc_dg", "exttok_dg", "juis_token_dg1dg1dg1dg1dg1x")

	// Every group route hits its storage-error arm once the groups table is
	// gone while the auth tables stay queryable.
	w14dDropTable(t, env, "groups")
	for _, route := range []struct {
		method, path, body string
	}{
		{http.MethodGet, "/__aipublic__/group/list?targetUsername=w14downer", ""},
		{http.MethodPost, "/__aipublic__/group/add", `{"targetUsername":"w14downer","name":"g","providerCode":"gpt"}`},
		{http.MethodPost, "/__aipublic__/group/update", `{"targetUsername":"w14downer","groupId":"g1","name":"n"}`},
		{http.MethodPost, "/__aipublic__/group/del", `{"targetUsername":"w14downer","groupId":"g1"}`},
	} {
		status, payload, _ := env.doAuth(route.method, route.path, route.body, token)
		if status != http.StatusInternalServerError {
			t.Fatalf("%s %s: %d %v", route.method, route.path, status, payload)
		}
	}
}

func TestW14dDroppedStrategyTables(t *testing.T) {
	env := newAIPublicEnv(t)
	token := w14dSeedAllScopes(t, env, "extsrc_ds", "exttok_ds", "juis_token_ds1ds1ds1ds1ds1x")

	// A real binding group keeps the binding validation green so Create fails
	// on the dropped strategy table instead.
	status, payload, _ := env.doAuth(http.MethodPost, "/__aipublic__/group/add",
		`{"targetUsername":"w14downer","name":"绑定组","providerCode":"gpt"}`, token)
	if status != 201 {
		t.Fatalf("seed group: %d %v", status, payload)
	}
	groupID := payload["data"].(map[string]any)["group"].(map[string]any)["id"].(string)

	w14dDropTable(t, env, "route_strategies")
	// Read/update/delete fail in the owner-lookup arm (explicit 500); the add
	// store error carries no CJK so the localize layer renders 请求参数无效.
	bindingBody := `{"targetUsername":"w14downer","name":"s","groupBindings":[{"groupId":"` + groupID + `"}]}`
	for _, route := range []struct {
		method, path, body, message string
		status                      int
	}{
		{http.MethodGet, "/__aipublic__/route-strategy/list?targetUsername=w14downer", "", "服务器内部错误", http.StatusInternalServerError},
		{http.MethodPost, "/__aipublic__/route-strategy/add", bindingBody, "请求参数无效", http.StatusBadRequest},
		{http.MethodPost, "/__aipublic__/route-strategy/update", `{"targetUsername":"w14downer","routeStrategyId":"rs1","name":"n"}`, "服务器内部错误", http.StatusInternalServerError},
		{http.MethodPost, "/__aipublic__/route-strategy/del", `{"targetUsername":"w14downer","routeStrategyId":"rs1"}`, "服务器内部错误", http.StatusInternalServerError},
	} {
		status, payload, _ := env.doAuth(route.method, route.path, route.body, token)
		if status != route.status || payload["message"] != route.message {
			t.Fatalf("%s %s: %d %v", route.method, route.path, status, payload)
		}
	}
}

func TestW14dDroppedApiKeyTables(t *testing.T) {
	env := newAIPublicEnv(t)
	token := w14dSeedAllScopes(t, env, "extsrc_dk", "exttok_dk", "juis_token_dk1dk1dk1dk1dk1x")

	// The api-key read fails once the api_keys table is gone.
	w14dDropTable(t, env, "api_keys")
	for _, route := range []struct {
		method, path, body string
	}{
		{http.MethodGet, "/__aipublic__/api-key/list?targetUsername=w14downer", ""},
		{http.MethodPost, "/__aipublic__/api-key/update", `{"targetUsername":"w14downer","apiKeyId":"ak1","name":"n"}`},
		{http.MethodPost, "/__aipublic__/api-key/del", `{"targetUsername":"w14downer","apiKeyId":"ak1"}`},
	} {
		status, payload, _ := env.doAuth(route.method, route.path, route.body, token)
		if status != http.StatusInternalServerError {
			t.Fatalf("%s %s: %d %v", route.method, route.path, status, payload)
		}
	}
}

func TestW14dDroppedAccountTables(t *testing.T) {
	env := newAIPublicEnv(t)
	token := w14dSeedAllScopes(t, env, "extsrc_da", "exttok_da", "juis_token_da1da1da1da1da1x")

	// The account read fails once the accounts table is gone.
	w14dDropTable(t, env, "accounts")
	for _, route := range []struct {
		method, path, body string
	}{
		{http.MethodGet, "/__aipublic__/account/list?targetUsername=w14downer", ""},
		{http.MethodPost, "/__aipublic__/account/update", `{"accountId":"acc1","name":"n"}`},
		{http.MethodPost, "/__aipublic__/account/del", `{"accountId":"acc1"}`},
	} {
		status, payload, _ := env.doAuth(route.method, route.path, route.body, token)
		if status != http.StatusInternalServerError {
			t.Fatalf("%s %s: %d %v", route.method, route.path, status, payload)
		}
	}
}

func TestW14dDroppedBindingTables(t *testing.T) {
	env := newAIPublicEnv(t)
	token := w14dSeedAllScopes(t, env, "extsrc_db", "exttok_db", "juis_token_db1db1db1db1db1x")

	// Seed a real strategy so the list hydration actually runs, then drop the
	// binding table to break it.
	status, payload, _ := env.doAuth(http.MethodPost, "/__aipublic__/group/add",
		`{"targetUsername":"w14downer","name":"绑定组","providerCode":"gpt"}`, token)
	if status != 201 {
		t.Fatalf("seed group: %d %v", status, payload)
	}
	groupID := payload["data"].(map[string]any)["group"].(map[string]any)["id"].(string)
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/route-strategy/add",
		`{"targetUsername":"w14downer","name":"公开策略","groupBindings":[{"groupId":"`+groupID+`"}]}`, token)
	if status != 201 {
		t.Fatalf("seed strategy: %d %v", status, payload)
	}

	w14dDropTable(t, env, "route_strategy_groups")
	status, payload, _ = env.doAuth(http.MethodGet, "/__aipublic__/route-strategy/list?targetUsername=w14downer", "", token)
	if status != http.StatusInternalServerError || payload["message"] != "服务器内部错误" {
		t.Fatalf("binding hydration: %d %v", status, payload)
	}
}

func TestW14dDroppedProviderTable(t *testing.T) {
	env := newAIPublicEnv(t)
	token := w14dSeedAllScopes(t, env, "extsrc_dp", "exttok_dp", "juis_token_dp1dp1dp1dp1dp1x")

	// A real group keeps the ownership lookup green so the update reaches the
	// provider precheck.
	status, payload, _ := env.doAuth(http.MethodPost, "/__aipublic__/group/add",
		`{"targetUsername":"w14downer","name":"g","providerCode":"gpt"}`, token)
	if status != 201 {
		t.Fatalf("seed group: %d %v", status, payload)
	}
	groupID := payload["data"].(map[string]any)["group"].(map[string]any)["id"].(string)

	// The provider precheck surfaces the raw driver error whose non-CJK text
	// the kernel localize layer renders as 请求参数无效.
	w14dDropTable(t, env, "providers")
	for _, route := range []struct {
		method, path, body string
	}{
		{http.MethodPost, "/__aipublic__/group/add", `{"targetUsername":"w14downer","name":"g2","providerCode":"gpt"}`},
		{http.MethodPost, "/__aipublic__/group/update", `{"targetUsername":"w14downer","groupId":"` + groupID + `","name":"n2","providerCode":"gpt"}`},
	} {
		status, payload, _ := env.doAuth(route.method, route.path, route.body, token)
		if status != http.StatusBadRequest || payload["message"] != "请求参数无效" {
			t.Fatalf("%s %s: %d %v", route.method, route.path, status, payload)
		}
	}
}

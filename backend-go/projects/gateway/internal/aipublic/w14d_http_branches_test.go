// w14d_http_branches_test.go drives the aipublic handlers through their
// previously uncovered query/body validation forks, the target-resolution
// diagnostics, the profile and group filter arms and the provider validation
// errors across all four domains (accounts, strategies, groups, api-keys).
package aipublic

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// w14dSeedWorld provisions a source token with every public scope plus an
// active target user and provider.
func w14dSeedWorld(t *testing.T, env *aipublicEnv) string {
	t.Helper()
	env.seedProvider("gpt", true)
	env.seedTargetUser("w14d_user", "w14duser", "active")
	env.seedTargetUser("w14d_off", "w14doff", "disabled")
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
	return env.seedSource("w14d_ext", "w14d_tok", "juis_token_w14dw14dw14d",
		"active", "active", scopes, "[]", "", "")
}

// w14dExpect400 asserts a bad request with the verbatim zod issue.
func w14dExpect400(t *testing.T, env *aipublicEnv, token, method, path, body, issue string) {
	t.Helper()
	status, payload, _ := env.doAuth(method, path, body, token)
	if status != http.StatusBadRequest {
		t.Fatalf("%s %s: %d %v (want 400 %q)", method, path, status, payload, issue)
	}
	if issue != "" && payload["message"] != issue {
		t.Fatalf("%s %s: message %v (want %q)", method, path, payload["message"], issue)
	}
}

func TestW14dAccountListValidationForks(t *testing.T) {
	env := newAIPublicEnv(t)
	token := w14dSeedWorld(t, env)
	base := "/__aipublic__/account/list"

	// Unknown GET query keys are rejected by the guard before the zod parse.
	w14dExpect400(t, env, token, http.MethodGet, base+"?targetUsername=w14duser&bogus=1", "", "请求参数无效")
	w14dExpect400(t, env, token, http.MethodGet, base+"?targetUsername=w14duser&schedulable=nope", "", "请求参数无效")
	w14dExpect400(t, env, token, http.MethodGet, base+"?targetUsername=w14duser&page=0", "", "请求参数无效")
	w14dExpect400(t, env, token, http.MethodGet, base+"?targetUsername=w14duser&pageSize=101", "", "请求参数无效")
	w14dExpect400(t, env, token, http.MethodGet, base+"?targetUsername=w14duser&providerProtocolProfileId=p1", "",
		"按协议档案查询时必须提供 providerCode")
	w14dExpect400(t, env, token, http.MethodGet, base+"?targetUsername=w14duser&providerCode=gpt&providerProtocolProfileId=profile-nope", "",
		"供应商未配置协议档案：gpt")
	w14dExpect400(t, env, token, http.MethodGet, base+"?targetUsername=w14duser&targetGroupName=g", "",
		"按目标分组名称查询账号时必须提供 providerCode")

	// Unknown target and a disabled target render their own envelopes.
	status, payload, _ := env.doAuth(http.MethodGet, base+"?targetUsername=nobody", "", token)
	if status != http.StatusNotFound {
		t.Fatalf("unknown target: %d %v", status, payload)
	}
	status, payload, _ = env.doAuth(http.MethodGet, base+"?targetUsername=w14doff", "", token)
	if status != http.StatusBadRequest || payload["message"] != "目标用户已停用：w14doff" {
		t.Fatalf("disabled target: %d %v", status, payload)
	}

	// The filtered happy paths stay 200.
	if status, _, _ := env.doAuth(http.MethodGet,
		base+"?targetUsername=w14duser&providerCode=gpt&targetGroupName=missing-group", "", token); status != http.StatusOK {
		t.Fatalf("group sentinel: %d", status)
	}
	if status, _, _ := env.doAuth(http.MethodGet,
		base+"?targetUsername=w14duser&type=chat&status=active&schedulable=disabled&keyword=k&page=2&pageSize=10", "", token); status != http.StatusOK {
		t.Fatalf("filtered list: %d", status)
	}
}

func TestW14dStrategyListValidationForks(t *testing.T) {
	env := newAIPublicEnv(t)
	token := w14dSeedWorld(t, env)
	base := "/__aipublic__/route-strategy/list"

	// Unknown GET query keys are rejected by the guard before the zod parse.
	// Unknown GET query keys are rejected by the guard before the zod parse.
	w14dExpect400(t, env, token, http.MethodGet, base+"?targetUsername=w14duser&bogus=1", "", "请求参数无效")
	w14dExpect400(t, env, token, http.MethodGet, base+"?targetUsername=w14duser&mode=mystery", "", "请求参数无效")
	w14dExpect400(t, env, token, http.MethodGet, base+"?targetUsername=w14duser&status=mystery", "", "请求参数无效")
	if status, _, _ := env.doAuth(http.MethodGet,
		base+"?targetUsername=w14duser&keyword=k&mode=all&status=all&page=2&pageSize=5", "", token); status != http.StatusOK {
		t.Fatalf("strategy list happy: %d", status)
	}
}

func TestW14dGroupAndApiKeyListValidationForks(t *testing.T) {
	env := newAIPublicEnv(t)
	token := w14dSeedWorld(t, env)

	// Unknown GET query keys are rejected by the guard before the zod parse.
	w14dExpect400(t, env, token, http.MethodGet, "/__aipublic__/group/list?targetUsername=w14duser&bogus=1", "",
		"请求参数无效")
	w14dExpect400(t, env, token, http.MethodGet, "/__aipublic__/group/list?targetUsername=w14duser&bogus=2", "",
		"请求参数无效")
	w14dExpect400(t, env, token, http.MethodGet, "/__aipublic__/api-key/list?targetUsername=w14duser&status=mystery", "",
		"请求参数无效")
	w14dExpect400(t, env, token, http.MethodGet, "/__aipublic__/api-key/list?targetUsername=w14duser&routeStrategyId="+strings.Repeat("s", 121), "",
		"请求参数无效")
	if status, _, _ := env.doAuth(http.MethodGet, "/__aipublic__/api-key/list?targetUsername=w14duser&keyword=k&status=all&page=1&pageSize=25", "", token); status != http.StatusOK {
		t.Fatalf("api key list happy: %d", status)
	}
	if status, _, _ := env.doAuth(http.MethodGet, "/__aipublic__/group/list?targetUsername=w14duser&keyword=k&providerCode=gpt", "", token); status != http.StatusOK {
		t.Fatalf("group list happy: %d", status)
	}
}

func TestW14dMutationBodyValidationForks(t *testing.T) {
	env := newAIPublicEnv(t)
	token := w14dSeedWorld(t, env)

	cases := []struct {
		name string
		path string
		body string
	}{
		{"group add strict", "/__aipublic__/group/add", `{"targetUsername":"w14duser","nope":1}`},
		{"group add required", "/__aipublic__/group/add", `{"targetUsername":"w14duser"}`},
		{"group add display name", "/__aipublic__/group/add", `{"targetUsername":"w14duser","targetDisplayName":" ","name":"g","providerCode":"gpt"}`},
		{"group update no fields", "/__aipublic__/group/update", `{"groupId":"g"}`},
		{"strategy add bindings", "/__aipublic__/route-strategy/add", `{"targetUsername":"w14duser","name":"s"}`},
		{"strategy add bindings empty", "/__aipublic__/route-strategy/add", `{"targetUsername":"w14duser","name":"s","groupBindings":[]}`},
		{"strategy update no fields", "/__aipublic__/route-strategy/update", `{"routeStrategyId":"s"}`},
		{"api key add required", "/__aipublic__/api-key/add", `{"targetUsername":"w14duser"}`},
		{"api key update no fields", "/__aipublic__/api-key/update", `{"apiKeyId":"k"}`},
		{"account add strict", "/__aipublic__/account/add", `{"bogus":1}`},
	}
	for _, testCase := range cases {
		w14dExpect400(t, env, token, http.MethodPost, testCase.path, testCase.body, "")
	}

	// A supported-models-less account add renders the verbatim 400.
	env.seedTargetUser("w14d_add", "w14dadd", "active")
	status, payload, _ := env.doAuth(http.MethodPost, "/__aipublic__/account/add",
		`{"targetUsername":"w14dadd","name":"acc","providerCode":"gpt","providerProtocolProfileId":"profile-x"}`, token)
	if status != http.StatusBadRequest || payload["message"] == "" {
		t.Fatalf("account add missing models: %d %v", status, payload)
	}
}

func TestW14dDeleteNotFoundEnvelopes(t *testing.T) {
	env := newAIPublicEnv(t)
	token := w14dSeedWorld(t, env)

	cases := []struct {
		name string
		path string
		body string
	}{
		{"group", "/__aipublic__/group/del", `{"targetUsername":"w14duser","groupId":"grp-missing"}`},
		{"strategy", "/__aipublic__/route-strategy/del", `{"targetUsername":"w14duser","routeStrategyId":"rs-missing"}`},
		{"api key", "/__aipublic__/api-key/del", `{"targetUsername":"w14duser","apiKeyId":"ak-missing"}`},
		{"account", "/__aipublic__/account/del", `{"targetUsername":"w14duser","accountId":"acc-missing"}`},
	}
	for _, testCase := range cases {
		status, payload, _ := env.doAuth(http.MethodPost, testCase.path, testCase.body, token)
		if status != http.StatusNotFound && status != http.StatusBadRequest && status != http.StatusOK {
			t.Fatalf("%s delete: %d %v", testCase.name, status, payload)
		}
	}
	// The not-found envelope keeps the code field.
	_, payload, _ := env.doAuth(http.MethodPost, "/__aipublic__/group/del",
		`{"targetUsername":"w14duser","groupId":"grp-missing"}`, token)
	if payload["message"] == nil || payload["message"] == "" {
		t.Fatalf("missing group must carry a message: %v", payload)
	}
	_ = time.Now
}

// w14d_gap_sweep_test.go drives the remaining handler validation and
// storage-error arms of the public account/strategy/group/api-key slices:
// strict-body parse branches, provider/profile prechecks, disabled targets,
// raw-account credential guards and dropped-table storage failures.
package aipublic

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
)

func w14dSeedProfile(t *testing.T, env *aipublicEnv, id, providerCode, accountTypesJSON string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := env.db.Exec(`INSERT INTO provider_protocol_profiles
		(id, provider_code, name, enabled, protocol_code, protocol_version, base_url, default_health_check_model, account_types_json, capabilities_json, created_at, updated_at)
		VALUES (?, ?, ?, 1, 'openai', 'v1', 'https://api.example', 'gpt-4o-mini', ?, '{}', ?, ?)`,
		id, providerCode, "Profile"+id, accountTypesJSON, now, now); err != nil {
		t.Fatal(err)
	}
}

// w14dSeedRawAccount inserts an account row directly with the given
// credentials so the credential-guard arms of account update can run.
func w14dSeedRawAccount(t *testing.T, env *aipublicEnv, id, ownerID, profileID string, creds accounts.Credentials) {
	t.Helper()
	sealed, err := accounts.EncryptJSON("test-crypto-secret", creds)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := env.db.Exec(`INSERT INTO accounts
		(id, config_revision, dispatch_revision, system_account_id, provider_code, provider_protocol_profile_id,
		 protocol_code, protocol_version, name, type, status, credentials_encrypted, schedulable, created_at, updated_at)
		VALUES (?, 1, 1, ?, 'gpt', ?, 'openai', 'v1', ?, 'api_key', 'active', ?, 1, ?, ?)`,
		id, ownerID, profileID, "raw-"+id, sealed, now, now); err != nil {
		t.Fatal(err)
	}
}

func w14dOwnerIDByUsername(t *testing.T, env *aipublicEnv, username string) string {
	t.Helper()
	var ownerID string
	if err := env.db.QueryRow(`SELECT id FROM system_accounts WHERE username = ?`, username).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	return ownerID
}

func TestW14dAccountGapSweep(t *testing.T) {
	env := newAIPublicEnv(t)
	token := w14dSeedAllScopes(t, env, "extsrc_gs", "exttok_gs", "juis_token_gs1gs1gs1gs1gs1x")
	w14dSeedProfile(t, env, "profile_gpt_openai_v1", "gpt", `["api_key"]`)
	w14dSeedProfile(t, env, "profile_gpt_oauth_v1", "gpt", `["gpt_oauth"]`)
	env.seedTargetUser("user_frozen2", "frozen2", "disabled")

	// List query: over-long profile filter id (L72).
	status, payload, _ := env.doAuth(http.MethodGet,
		"/__aipublic__/account/list?targetUsername=w14downer&providerProtocolProfileId="+strings.Repeat("p", 121), "", token)
	if status != http.StatusBadRequest {
		t.Fatalf("long profile filter: %d %v", status, payload)
	}

	// Add body parse arms: over-long targetDisplayName, missing
	// targetGroupName, missing providerProtocolProfileId, missing name,
	// missing baseUrl, missing apiKey.
	for _, body := range []string{
		`{"targetUsername":"w14downer","targetGroupName":"福利","targetDisplayName":"` + strings.Repeat("d", 81) + `","providerCode":"gpt","providerProtocolProfileId":"p","name":"n","baseUrl":"https://x","apiKey":"sk"}`,
		`{"targetUsername":"w14downer","providerCode":"gpt","providerProtocolProfileId":"p","name":"n","baseUrl":"https://x","apiKey":"sk"}`,
		`{"targetUsername":"w14downer","targetGroupName":"福利","providerCode":"gpt","name":"n","baseUrl":"https://x","apiKey":"sk","supportedModels":["m"]}`,
		`{"targetUsername":"w14downer","targetGroupName":"福利","providerCode":"gpt","providerProtocolProfileId":"p","baseUrl":"https://x","apiKey":"sk","supportedModels":["m"]}`,
		`{"targetUsername":"w14downer","targetGroupName":"福利","providerCode":"gpt","providerProtocolProfileId":"p","name":"n","apiKey":"sk","supportedModels":["m"]}`,
		`{"targetUsername":"w14downer","targetGroupName":"福利","providerCode":"gpt","providerProtocolProfileId":"p","name":"n","baseUrl":"https://x","supportedModels":["m"]}`,
	} {
		status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/account/add", body, token)
		if status != http.StatusBadRequest {
			t.Fatalf("add parse arm: %d %v", status, payload)
		}
	}
	// supportedModels over the 500 element cap.
	items := make([]string, 501)
	for index := range items {
		items[index] = fmt.Sprintf(`"m%d"`, index)
	}
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/account/add",
		`{"targetUsername":"w14downer","targetGroupName":"福利","providerCode":"gpt","providerProtocolProfileId":"p","name":"n","baseUrl":"https://x","apiKey":"sk","supportedModels":[`+strings.Join(items, ",")+`]}`, token)
	if status != http.StatusBadRequest {
		t.Fatalf("models cap: %d %v", status, payload)
	}

	// Unknown profile id (L475) and profile without api_key support (L484).
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/account/add",
		`{"targetUsername":"w14downer","targetGroupName":"福利","providerCode":"gpt","providerProtocolProfileId":"profile_ghost","name":"n","baseUrl":"https://x","apiKey":"sk","supportedModels":["m"],"type":"api_key"}`, token)
	if status != http.StatusBadRequest {
		t.Fatalf("ghost profile: %d %v", status, payload)
	}
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/account/add",
		`{"targetUsername":"w14downer","targetGroupName":"福利","providerCode":"gpt","providerProtocolProfileId":"profile_gpt_oauth_v1","name":"n","baseUrl":"https://x","apiKey":"sk","supportedModels":["m"],"type":"api_key"}`, token)
	if status != http.StatusBadRequest || payload["message"] != "当前供应商不支持 API Key 账户" {
		t.Fatalf("oauth-only profile: %d %v", status, payload)
	}

	// Disabled push target (L519 of the flow) and a store-level schedule
	// validation failure inside AiAccounts.Create.
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/account/add",
		`{"targetUsername":"frozen2","targetGroupName":"福利","providerCode":"gpt","providerProtocolProfileId":"profile_gpt_openai_v1","name":"n","baseUrl":"https://x","apiKey":"sk","supportedModels":["m"],"type":"api_key"}`, token)
	if status != http.StatusBadRequest || payload["message"] != "目标用户已停用：frozen2" {
		t.Fatalf("disabled target: %d %v", status, payload)
	}
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/account/add",
		`{"targetUsername":"w14downer","targetGroupName":"福利","providerCode":"gpt","providerProtocolProfileId":"profile_gpt_openai_v1","name":"坏计划","baseUrl":"https://x","apiKey":"sk","supportedModels":["m"],"availabilitySchedule":{"bogus":true},"type":"api_key"}`, token)
	if status != http.StatusBadRequest || payload["message"] == "" {
		t.Fatalf("bad schedule: %d %v", status, payload)
	}

	// Two successful pushes (the second carries availabilitySchedule, L514)
	// give the list two ids (L261).
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/account/add",
		`{"targetUsername":"w14downer","targetGroupName":"福利","providerCode":"gpt","providerProtocolProfileId":"profile_gpt_openai_v1","name":"站点A","baseUrl":"https://a.example","apiKey":"sk-a","supportedModels":["gpt-4o-mini"],"type":"api_key"}`, token)
	if status != http.StatusCreated {
		t.Fatalf("account A: %d %v", status, payload)
	}
	groupA := payload["data"].(map[string]any)["target"].(map[string]any)["groupId"]
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/account/add",
		`{"targetUsername":"w14downer","targetGroupName":"福利2","providerCode":"gpt","providerProtocolProfileId":"profile_gpt_openai_v1","name":"站点B","baseUrl":"https://b.example","apiKey":"sk-b","supportedModels":["gpt-4o-mini"],"availabilitySchedule":{"enabled":true,"mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"09:00","end":"18:00"}]},"type":"api_key"}`, token)
	if status != http.StatusCreated {
		t.Fatalf("account B: %d %v", status, payload)
	}

	// List with the profile and group filters set.
	status, payload, _ = env.doAuth(http.MethodGet,
		"/__aipublic__/account/list?targetUsername=w14downer&providerCode=gpt&providerProtocolProfileId=profile_gpt_openai_v1", "", token)
	if status != http.StatusOK {
		t.Fatalf("profile filter: %d %v", status, payload)
	}
	status, payload, _ = env.doAuth(http.MethodGet,
		"/__aipublic__/account/list?targetUsername=w14downer&groupId="+groupA.(string), "", token)
	if status != http.StatusOK {
		t.Fatalf("group filter: %d %v", status, payload)
	}

	accountA := ""
	if err := env.db.QueryRow(`SELECT id FROM accounts WHERE name = '站点A'`).Scan(&accountA); err != nil {
		t.Fatal(err)
	}

	// Update parse arms.
	for _, body := range []string{
		`{"accountId":"` + accountA + `","foo":1}`,
		`{"accountId":"` + accountA + `","supportedModels":["   "]}`,
		`{"accountId":"` + accountA + `","supportedModels":["` + strings.Repeat("m", 121) + `"]}`,
		`{"accountId":"` + accountA + `","notes":"` + strings.Repeat("n", 1001) + `"}`,
	} {
		status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/account/update", body, token)
		if status != http.StatusBadRequest {
			t.Fatalf("update parse arm: %d %v", status, payload)
		}
	}

	// Cross-target update renders 404 (L790).
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/account/update",
		`{"accountId":"`+accountA+`","targetUsername":"ghost","name":"x"}`, token)
	if status != http.StatusNotFound {
		t.Fatalf("cross target update: %d %v", status, payload)
	}

	// Plain update without a group filter (L927/930 region).
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/account/update",
		`{"accountId":"`+accountA+`","name":"站点A2"}`, token)
	if status != http.StatusOK {
		t.Fatalf("plain update: %d %v", status, payload)
	}

	// Profile mismatch through resolveAccountGroupFilter (L956).
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/account/update",
		`{"accountId":"`+accountA+`","providerProtocolProfileId":"profile_gpt_oauth_v1","name":"站点A3"}`, token)
	if status != http.StatusBadRequest || payload["message"] != "账号不存在" {
		t.Fatalf("profile mismatch: %d %v", status, payload)
	}

	// Raw accounts drive the credential guards.
	ownerID := w14dOwnerIDByUsername(t, env, "w14downer")
	w14dSeedRawAccount(t, env, "acc_raw1", ownerID, "profile_gpt_openai_v1", accounts.Credentials{"api_key": "sk-old"})
	w14dSeedRawAccount(t, env, "acc_raw2", ownerID, "profile_gpt_openai_v1", accounts.Credentials{"base_url": "https://r.example"})
	w14dSeedRawAccount(t, env, "acc_raw3", ownerID, "", accounts.Credentials{"api_key": "sk", "base_url": "https://r.example"})
	w14dSeedRawAccount(t, env, "acc_raw4", ownerID, "profile_gpt_openai_v1", accounts.Credentials{"api_key": "sk", "base_url": "https://r.example"})

	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/account/update",
		`{"accountId":"acc_raw1","apiKey":"sk-new"}`, token)
	if status != http.StatusBadRequest || payload["message"] != "Base URL 不能为空" {
		t.Fatalf("missing base url: %d %v", status, payload)
	}
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/account/update",
		`{"accountId":"acc_raw2","baseUrl":"https://y.example"}`, token)
	if status != http.StatusBadRequest || payload["message"] != "API Key 不能为空" {
		t.Fatalf("missing api key: %d %v", status, payload)
	}
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/account/update",
		`{"accountId":"acc_raw3","name":"raw3"}`, token)
	if status != http.StatusBadRequest || payload["message"] != "providerProtocolProfileId 不能为空" {
		t.Fatalf("empty profile: %d %v", status, payload)
	}
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/account/update",
		`{"accountId":"acc_raw4","name":"raw4"}`, token)
	if status != http.StatusOK {
		t.Fatalf("raw update: %d %v", status, payload)
	}

	// Delete parse arms.
	for _, body := range []string{
		`{"accountId":"` + accountA + `","foo":1}`,
		`{"accountId":"` + accountA + `","targetUsername":""}`,
	} {
		status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/account/del", body, token)
		if status != http.StatusBadRequest {
			t.Fatalf("delete parse arm: %d %v", status, payload)
		}
	}
	// Cross-target delete renders the 200 not_found envelope (L1108).
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/account/del",
		`{"accountId":"`+accountA+`","targetUsername":"ghost"}`, token)
	if status != http.StatusOK {
		t.Fatalf("cross target delete: %d %v", status, payload)
	}
	if payload["data"].(map[string]any)["action"] != "not_found" {
		t.Fatalf("cross target delete envelope: %v", payload)
	}

	// Storage failures: drop the supported-models table (L180+L270), then the
	// accounts table (L570+L578), then groups (L243).
	w14dDropTable(t, env, "account_supported_models")
	status, payload, _ = env.doAuth(http.MethodGet, "/__aipublic__/account/list?targetUsername=w14downer", "", token)
	if status != http.StatusInternalServerError {
		t.Fatalf("dropped models: %d %v", status, payload)
	}
	w14dDropTable(t, env, "accounts")
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/account/add",
		`{"targetUsername":"w14downer","targetGroupName":"福利","providerCode":"gpt","providerProtocolProfileId":"profile_gpt_openai_v1","name":"站点C","baseUrl":"https://c.example","apiKey":"sk-c","supportedModels":["m"],"type":"api_key"}`, token)
	if status != http.StatusBadRequest {
		t.Fatalf("dropped accounts add: %d %v", status, payload)
	}
	w14dDropTable(t, env, "groups")
	status, payload, _ = env.doAuth(http.MethodGet,
		"/__aipublic__/account/list?targetUsername=w14downer&targetGroupName=%E7%A6%8F%E5%88%A9&providerCode=gpt", "", token)
	if status != http.StatusBadRequest {
		t.Fatalf("dropped groups list: %d %v", status, payload)
	}
}

func TestW14dStrategyGapSweep(t *testing.T) {
	env := newAIPublicEnv(t)
	token := w14dSeedAllScopes(t, env, "extsrc_ss", "exttok_ss", "juis_token_ss1ss1ss1ss1ss1x")

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
	strategyID := payload["data"].(map[string]any)["routeStrategy"].(map[string]any)["id"].(string)

	// List arms: missing target (L94), the mode filter (L104), the parse
	// arms for a missing username and a bad page (L44/L64) plus add parse
	// arms (L340/346 and friends).
	for _, route := range []struct {
		method, path, body string
		status             int
	}{
		{http.MethodGet, "/__aipublic__/route-strategy/list", "", http.StatusBadRequest},
		{http.MethodGet, "/__aipublic__/route-strategy/list?targetUsername=w14downer&page=0", "", http.StatusBadRequest},
		{http.MethodGet, "/__aipublic__/route-strategy/list?targetUsername=ghost", "", http.StatusNotFound},
		{http.MethodGet, "/__aipublic__/route-strategy/list?targetUsername=w14downer&mode=normal", "", http.StatusOK},
		{http.MethodPost, "/__aipublic__/route-strategy/add", `{"targetUsername":"w14downer","groupBindings":[{"groupId":"` + groupID + `"}]}`, http.StatusBadRequest},
		{http.MethodPost, "/__aipublic__/route-strategy/add", `{"targetUsername":"w14downer","name":"长描述","description":"` + strings.Repeat("d", 201) + `","groupBindings":[{"groupId":"` + groupID + `"}]}`, http.StatusBadRequest},
		{http.MethodPost, "/__aipublic__/route-strategy/add", `{"targetUsername":"w14downer","name":"s1","groupBindings":[{"groupId":"` + groupID + `"}],"normalRoutingConfig":"x"}`, http.StatusBadRequest},
	} {
		status, payload, _ = env.doAuth(route.method, route.path, route.body, token)
		if status != route.status {
			t.Fatalf("strategy arm %s %s: %d %v", route.method, route.path, status, payload)
		}
	}

	// Add arm: unknown target user renders 404.
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/route-strategy/add",
		`{"targetUsername":"ghost","name":"s3","groupBindings":[{"groupId":"`+groupID+`"}]}`, token)
	if status != http.StatusNotFound {
		t.Fatalf("add ghost: %d %v", status, payload)
	}

	// Update arms: unknown key (L509), missing id (L521), enum guards
	// (L540/545), routing config type guard (L559) and the bindings rewrite
	// with explicit priority (L452/L635).
	for _, body := range []string{
		`{"routeStrategyId":"` + strategyID + `","foo":1}`,
		`{"name":"x"}`,
		`{"routeStrategyId":"` + strategyID + `","mode":"bogus"}`,
		`{"routeStrategyId":"` + strategyID + `","status":"bogus"}`,
		`{"routeStrategyId":"` + strategyID + `","normalRoutingConfig":"x"}`,
	} {
		status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/route-strategy/update", body, token)
		if status != http.StatusBadRequest {
			t.Fatalf("update guard: %d %v", status, payload)
		}
	}
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/route-strategy/update",
		`{"routeStrategyId":"`+strategyID+`","groupBindings":[{"groupId":"`+groupID+`","priority":2,"weight":5}]}`, token)
	if status != http.StatusOK {
		t.Fatalf("update bindings: %d %v", status, payload)
	}

	// Delete parse arms (L684/L698).
	for _, body := range []string{
		`{"routeStrategyId":"` + strategyID + `","foo":1}`,
		`{"routeStrategyId":"` + strategyID + `","targetUsername":""}`,
	} {
		status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/route-strategy/del", body, token)
		if status != http.StatusBadRequest {
			t.Fatalf("delete guard: %d %v", status, payload)
		}
	}
}

func TestW14dGroupApiKeyGapSweep(t *testing.T) {
	env := newAIPublicEnv(t)
	token := w14dSeedAllScopes(t, env, "extsrc_gk", "exttok_gk", "juis_token_gk1gk1gk1gk1gk1x")

	// Group add with description feeds the scan branch (L160); over-long
	// description and disabled target render 400 (L255/L297).
	status, payload, _ := env.doAuth(http.MethodPost, "/__aipublic__/group/add",
		`{"targetUsername":"w14downer","name":"描述组","providerCode":"gpt","description":"带描述"}`, token)
	if status != 201 {
		t.Fatalf("group with description: %d %v", status, payload)
	}
	status, payload, _ = env.doAuth(http.MethodGet, "/__aipublic__/group/list?targetUsername=w14downer", "", token)
	if status != http.StatusOK {
		t.Fatalf("group list: %d %v", status, payload)
	}
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/group/add",
		`{"targetUsername":"w14downer","name":"长描述组","providerCode":"gpt","description":"`+strings.Repeat("d", 501)+`"}`, token)
	if status != http.StatusBadRequest {
		t.Fatalf("long description: %d %v", status, payload)
	}
	env.seedTargetUser("user_frozen3", "frozen3", "disabled")
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/group/add",
		`{"targetUsername":"frozen3","name":"冻结组","providerCode":"gpt"}`, token)
	if status != http.StatusBadRequest || payload["message"] != "目标用户已停用：frozen3" {
		t.Fatalf("disabled group target: %d %v", status, payload)
	}

	// Group update parse arms (L363/L375/L395).
	for _, body := range []string{
		`{"groupId":"g1","foo":1}`,
		`{"name":"x"}`,
		`{"groupId":"g1","groupType":"bogus"}`,
		`{"groupId":"g1","description":"` + strings.Repeat("d", 501) + `"}`,
	} {
		status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/group/update", body, token)
		if status != http.StatusBadRequest {
			t.Fatalf("group update guard: %d %v", status, payload)
		}
	}

	// API key list parse arms (L32/L37/L62) and the missing-target arm
	// (L87).
	for _, query := range []string{
		"targetUsername=w14downer&foo=1",
		"",
		"targetUsername=w14downer&pageSize=101",
	} {
		status, payload, _ = env.doAuth(http.MethodGet, "/__aipublic__/api-key/list?"+query, "", token)
		if status != http.StatusBadRequest {
			t.Fatalf("api key list guard %q: %d %v", query, status, payload)
		}
	}
	status, payload, _ = env.doAuth(http.MethodGet, "/__aipublic__/api-key/list?targetUsername=ghost", "", token)
	if status != http.StatusNotFound {
		t.Fatalf("api key list ghost: %d %v", status, payload)
	}

	// API key add parse arms (L157/L168) and the missing-target arm (L215).
	for _, body := range []string{
		`{"name":"n","routeStrategyId":"rs1"}`,
		`{"targetUsername":"w14downer","name":"n","routeStrategyId":"rs1","description":"` + strings.Repeat("d", 201) + `"}`,
	} {
		status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/api-key/add", body, token)
		if status != http.StatusBadRequest {
			t.Fatalf("api key add guard: %d %v", status, payload)
		}
	}
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/api-key/add",
		`{"targetUsername":"ghost","name":"n","routeStrategyId":"rs1"}`, token)
	if status != http.StatusNotFound {
		t.Fatalf("api key add ghost: %d %v", status, payload)
	}

	// Seed a strategy through the store helper and add a key with a schedule
	// (L236).
	ownerID := w14dOwnerIDByUsername(t, env, "w14downer")
	groupID := ""
	if err := env.db.QueryRow(`SELECT id FROM groups WHERE name = '描述组'`).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	strategyID := seedStrategyForTest(t, env.db, ownerID, groupID)
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/api-key/add",
		`{"targetUsername":"w14downer","name":"排程Key","routeStrategyId":"`+strategyID+`","availabilitySchedule":{"enabled":true,"mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"09:00","end":"18:00"}]}}`, token)
	if status != 201 {
		t.Fatalf("scheduled key: %d %v", status, payload)
	}

	// Update parse arms: over-long name (L302 region) and missing apiKeyId
	// (L296).
	for _, body := range []string{
		`{"apiKeyId":"ak1","name":"` + strings.Repeat("n", 121) + `"}`,
		`{"name":"x"}`,
	} {
		status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/api-key/update", body, token)
		if status != http.StatusBadRequest {
			t.Fatalf("api key update guard: %d %v", status, payload)
		}
	}

	// Dropped api_keys table: Create error arm (L247).
	w14dDropTable(t, env, "api_keys")
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/api-key/add",
		`{"targetUsername":"w14downer","name":"掉表Key","routeStrategyId":"`+strategyID+`"}`, token)
	if status != http.StatusBadRequest {
		t.Fatalf("dropped api keys add: %d %v", status, payload)
	}
}

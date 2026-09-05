// Tests for the /__aipublic__ slice: the auth contract (missing/invalid
// token, disabled/expired source, unavailable token, missing scope), the
// penalty-window rate limiter (429 + Retry-After), the built-in test-token
// mock paths, the 404 mapping and the group lifecycle over the real stores.
// Every request path goes through the mounted kernel exactly like the Node
// regression script (external-source-auth-regression.ts).
package aipublic

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/apikeys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/businessauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/groups"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/routestrategies"
	_ "modernc.org/sqlite"
)

type aipublicEnv struct {
	t      *testing.T
	db     *sql.DB
	server *httptest.Server
	now    *time.Time
}

func newAIPublicEnv(t *testing.T) *aipublicEnv {
	t.Helper()
	db, err := sql.Open("sqlite", "file:aipublic-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, statement := range aipublicSchema {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	service, err := businessauth.New(db, modelcheckauth.SQLite, time.Now, businessauth.OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	systemAccounts, err := authsys.NewAccountStore(db, modelcheckauth.SQLite, nil)
	if err != nil {
		t.Fatal(err)
	}
	groupsStore, err := groups.NewStore(db, false, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	strategyStore, err := routestrategies.NewStore(db, false, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	apiKeyStore, err := apikeys.NewStore(db, false, "test-crypto-secret", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	accountStore, err := accounts.NewStore(db, false, "test-crypto-secret", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = service
	k := kernelForTest()
	aipublicDeps := &Deps{
		DB: db, PGDialect: false, Now: time.Now,
		SystemAccounts: systemAccounts,
		Groups:         groupsStore, Strategies: strategyStore,
		ApiKeys: apiKeyStore, AiAccounts: accountStore,
		Sink: &recordingAIPublicSink{},
	}
	aipublicDeps.Mount(k)
	env := &aipublicEnv{t: t, db: db, server: httptest.NewServer(k.Handler())}
	t.Cleanup(env.server.Close)
	return env
}

// doAuth issues a request with the given bearer token ("" omits the header).
func (e *aipublicEnv) doAuth(method, path, body, token string) (int, map[string]any, http.Header) {
	e.t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, e.server.URL+path, reader)
	if err != nil {
		e.t.Fatal(err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		e.t.Fatal(err)
	}
	raw, _ := io.ReadAll(response.Body)
	response.Body.Close()
	var payload map[string]any
	_ = json.Unmarshal(raw, &payload)
	if payload == nil {
		payload = map[string]any{}
	}
	return response.StatusCode, payload, response.Header
}

// seedSource inserts an external source + one token row and returns the token.
func (e *aipublicEnv) seedSource(sourceID, tokenID, token, sourceStatus, tokenStatus string, scopes []string, rateLimitsJSON, sourceExpiresAt, tokenExpiresAt string) string {
	e.t.Helper()
	if rateLimitsJSON == "" {
		rateLimitsJSON = "[]"
	}
	scopesJSON, _ := json.Marshal(scopes)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := e.db.Exec(`INSERT INTO external_integration_sources
		(id, name, status, scopes_json, rate_limits_json, expires_at, notes, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, NULL, ?, ?)`,
		sourceID, "来源"+sourceID, sourceStatus, string(scopesJSON), rateLimitsJSON, nullText(sourceExpiresAt), now, now); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.db.Exec(`INSERT INTO external_integration_source_tokens
		(id, source_ref_id, name, token_hash, token_secret_encrypted, token_prefix, token_suffix, status, scopes_json, expires_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, NULL, ?, ?, ?, ?, ?, ?, ?)`,
		tokenID, sourceID, "Token"+tokenID,
		apikeys.HashSecret("external-integration-source-token:"+token),
		token[:8], token[len(token)-8:], tokenStatus, string(scopesJSON), nullText(tokenExpiresAt), now, now); err != nil {
		e.t.Fatal(err)
	}
	return token
}

func nullText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// seedTargetUser inserts a system account directly (active by default).
func (e *aipublicEnv) seedTargetUser(id, username, status string) {
	e.t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := e.db.Exec(`INSERT INTO system_accounts
		(id, username, display_name, role, status, password_hash, created_at, updated_at)
		VALUES (?, ?, ?, 'user', ?, 'x', ?, ?)`, id, username, username, status, now, now); err != nil {
		e.t.Fatal(err)
	}
}

func (e *aipublicEnv) seedProvider(code string, enabled bool) {
	e.t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	enabledInt := 0
	if enabled {
		enabledInt = 1
	}
	if _, err := e.db.Exec(`INSERT INTO providers (id, code, name, enabled, default_supported_models_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, "prov_"+code, code, code, enabledInt,
		`["`+code+`-4o-mini","`+code+`-4o"]`, now, now); err != nil {
		e.t.Fatal(err)
	}
}

// TestAIPublicAuthMatrix mirrors the Node regression auth branches.
func TestAIPublicAuthMatrix(t *testing.T) {
	env := newAIPublicEnv(t)
	env.seedTargetUser("user_huanmin", "huanmin", "active")
	goodToken := env.seedSource("extsrc_a", "exttok_a", "juis_token_aaaaaaaaaaaaaaaa",
		"active", "active", []string{"juhe_ai_public:group_list:read"}, "[]", "", "")
	// Disabled source.
	env.seedSource("extsrc_disabled", "exttok_d", "juis_token_dddddddddddddddd",
		"disabled", "active", []string{"juhe_ai_public:group_list:read"}, "[]", "", "")
	// Expired source.
	env.seedSource("extsrc_expired", "exttok_e", "juis_token_eeeeeeeeeeeeeeee",
		"active", "active", []string{"juhe_ai_public:group_list:read"}, "[]", "2020-01-01T00:00:00Z", "")
	// Disabled token.
	env.seedSource("extsrc_tokoff", "exttok_o", "juis_token_oooooooooooooooo",
		"active", "disabled", []string{"juhe_ai_public:group_list:read"}, "[]", "", "")
	// Expired token.
	env.seedSource("extsrc_tokexp", "exttok_x", "juis_token_xxxxxxxxxxxxxxxx",
		"active", "active", []string{"juhe_ai_public:group_list:read"}, "[]", "", "2020-01-01T00:00:00Z")
	// Missing scope for writes.
	env.seedSource("extsrc_w", "exttok_w", "juis_token_wwwwwwwwwwwwwwww",
		"active", "active", []string{"juhe_ai_public:group_list:read"}, "[]", "", "")

	target := "targetUsername=huanmin"
	cases := []struct {
		name   string
		method string
		path   string
		token  string
		status int
		code   string
	}{
		{"missing token", http.MethodGet, "/__aipublic__/group/list?" + target, "", 401, "external_source_token_missing"},
		{"unknown token", http.MethodGet, "/__aipublic__/group/list?" + target, "juis_unknown", 401, "external_source_unauthorized"},
		{"disabled source", http.MethodGet, "/__aipublic__/group/list?" + target, "juis_token_dddddddddddddddd", 403, "external_source_disabled"},
		{"expired source", http.MethodGet, "/__aipublic__/group/list?" + target, "juis_token_eeeeeeeeeeeeeeee", 403, "external_source_expired"},
		{"disabled token", http.MethodGet, "/__aipublic__/group/list?" + target, "juis_token_oooooooooooooooo", 401, "external_source_token_unavailable"},
		{"expired token", http.MethodGet, "/__aipublic__/group/list?" + target, "juis_token_xxxxxxxxxxxxxxxx", 401, "external_source_token_unavailable"},
		{"scope forbidden", http.MethodPost, "/__aipublic__/group/add", "juis_token_wwwwwwwwwwwwwwww", 403, "external_source_scope_forbidden"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			status, payload, _ := env.doAuth(testCase.method, testCase.path, "", testCase.token)
			if status != testCase.status {
				t.Fatalf("status: %d payload %v", status, payload)
			}
			if payload["code"] != testCase.code {
				t.Fatalf("code: %v", payload["code"])
			}
			if payload["message"] == nil || payload["message"] == "" {
				t.Fatalf("message missing: %v", payload)
			}
		})
	}
	// The happy path renders the ok envelope (no code field).
	status, payload, _ := env.doAuth(http.MethodGet, "/__aipublic__/group/list?"+target, "", goodToken)
	if status != 200 || payload["code"] != nil || payload["data"] == nil {
		t.Fatalf("happy path: %d %v", status, payload)
	}
	data := payload["data"].(map[string]any)
	if data["source"] != "stats" || data["target"] == nil {
		t.Fatalf("envelope: %v", data)
	}
}

// TestAIPublicRateLimit exercises the penalty window: the third call inside a
// 2-per-60s window renders 429 with Retry-After and the details block.
func TestAIPublicRateLimit(t *testing.T) {
	env := newAIPublicEnv(t)
	env.seedTargetUser("user_huanmin", "huanmin", "active")
	token := env.seedSource("extsrc_rl", "exttok_rl", "juis_token_rrrrrrrrrrrrrrrr",
		"active", "active", []string{"juhe_ai_public:group_list:read"},
		`[{"windowSeconds":60,"maxRequests":2}]`, "", "")
	path := "/__aipublic__/group/list?targetUsername=huanmin"
	for attempt := 0; attempt < 2; attempt++ {
		status, payload, _ := env.doAuth(http.MethodGet, path, "", token)
		if status != 200 {
			t.Fatalf("attempt %d: %d %v", attempt, status, payload)
		}
	}
	status, payload, headers := env.doAuth(http.MethodGet, path, "", token)
	if status != 429 {
		t.Fatalf("third attempt: %d %v", status, payload)
	}
	if payload["code"] != "external_source_rate_limited" {
		t.Fatalf("code: %v", payload["code"])
	}
	if headers.Get("Retry-After") == "" {
		t.Fatalf("missing Retry-After: %v", headers)
	}
	details := payload["details"].(map[string]any)
	if details["windowSeconds"] != float64(60) || details["maxRequests"] != float64(2) {
		t.Fatalf("details: %v", details)
	}
	if retryAfter, ok := details["retryAfterSeconds"].(float64); !ok || retryAfter < 1 || retryAfter > 60 {
		t.Fatalf("retryAfterSeconds: %v", details["retryAfterSeconds"])
	}
}

// TestAIPublicMockToken covers the built-in test token: mock envelopes never
// touch the resource tables and the response carries source=mock.
func TestAIPublicMockToken(t *testing.T) {
	env := newAIPublicEnv(t)
	allScopes := []string{
		"juhe_ai_public:group_list:read", "juhe_ai_public:route_strategy_list:read",
		"juhe_ai_public:api_key_list:read", "juhe_ai_public:account_list:read",
		"juhe_ai_public:group_add:write", "juhe_ai_public:account_add:write",
	}
	token := env.seedSource(builtInTestSourceID, builtInTestTokenID, "juis_token_mockmockmock01",
		"active", "active", allScopes, "[]", "", "")
	accounts, err := env.db.Query(`SELECT COUNT(*) FROM groups`)
	if err != nil {
		t.Fatal(err)
	}
	accounts.Close()

	status, payload, _ := env.doAuth(http.MethodGet, "/__aipublic__/group/list?targetUsername=huanmin", "", token)
	if status != 200 {
		t.Fatalf("mock list: %d %v", status, payload)
	}
	data := payload["data"].(map[string]any)
	if data["source"] != "mock" {
		t.Fatalf("mock source: %v", data)
	}

	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/account/add",
		`{"targetUsername":"someone","targetGroupName":"福利","providerCode":"gpt","providerProtocolProfileId":"p1","name":"n1","type":"api_key","baseUrl":"https://x","apiKey":"sk"}`, token)
	if status != 200 {
		t.Fatalf("mock account add: %d %v", status, payload)
	}
	data = payload["data"].(map[string]any)
	if data["source"] != "mock" || data["action"] != "mock" {
		t.Fatalf("mock account envelope: %v", data)
	}
	var rows int
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM groups`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("mock path must not write groups: %d", rows)
	}
}

// TestAIPublicNotFound covers the 404 mapping for missing targets/resources.
func TestAIPublicNotFound(t *testing.T) {
	env := newAIPublicEnv(t)
	env.seedTargetUser("user_huanmin", "huanmin", "active")
	scopes := []string{
		"juhe_ai_public:group_list:read", "juhe_ai_public:group_update:write",
		"juhe_ai_public:group_delete:write", "juhe_ai_public:api_key_update:write",
		"juhe_ai_public:api_key_delete:write",
	}
	token := env.seedSource("extsrc_404", "exttok_404", "juis_token_4444444444444444",
		"active", "active", scopes, "[]", "", "")

	status, payload, _ := env.doAuth(http.MethodGet, "/__aipublic__/group/list?targetUsername=ghost", "", token)
	if status != 404 || payload["message"] != "目标用户不存在：ghost" {
		t.Fatalf("missing target: %d %v", status, payload)
	}

	body := `{"groupId":"grp_missing","name":"x"}`
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/group/update", body, token)
	if status != 404 || payload["message"] != "分组不存在" {
		t.Fatalf("missing group update: %d %v", status, payload)
	}
	body = `{"groupId":"grp_missing"}`
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/group/del", body, token)
	if status != 404 || payload["message"] != "分组不存在" {
		t.Fatalf("missing group delete: %d %v", status, payload)
	}
	body = `{"apiKeyId":"key_missing","name":"x"}`
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/api-key/update", body, token)
	if status != 404 || payload["message"] != "API Key 不存在" {
		t.Fatalf("missing api key update: %d %v", status, payload)
	}
	// api-key/del renders 200 with action not_found (Node parity).
	body = `{"apiKeyId":"key_missing"}`
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/api-key/del", body, token)
	if status != 200 {
		t.Fatalf("missing api key delete: %d %v", status, payload)
	}
	data := payload["data"].(map[string]any)
	if data["action"] != "not_found" {
		t.Fatalf("delete action: %v", data)
	}
}

// TestAIPublicGroupLifecycle drives the real group family: add (auto-create
// user + group), duplicate add (existing), list, update, delete.
func TestAIPublicGroupLifecycle(t *testing.T) {
	env := newAIPublicEnv(t)
	env.seedProvider("gpt", true)
	scopes := []string{
		"juhe_ai_public:group_list:read", "juhe_ai_public:group_add:write",
		"juhe_ai_public:group_update:write", "juhe_ai_public:group_delete:write",
	}
	token := env.seedSource("extsrc_g", "exttok_g", "juis_token_gggggggggggggggg",
		"active", "active", scopes, "[]", "", "")

	addBody := `{"targetUsername":"pusher","name":"福利站","providerCode":"gpt"}`
	status, payload, _ := env.doAuth(http.MethodPost, "/__aipublic__/group/add", addBody, token)
	if status != 201 {
		t.Fatalf("group add: %d %v", status, payload)
	}
	created := payload["data"].(map[string]any)
	if created["action"] != "created" || created["source"] != "stats" {
		t.Fatalf("group add envelope: %v", created)
	}
	group := created["group"].(map[string]any)
	groupID := group["id"].(string)
	target := created["target"].(map[string]any)
	if target["username"] != "pusher" || target["created"] != true {
		t.Fatalf("group add target: %v", target)
	}
	// The target user was auto-created and is active.
	var accountStatus string
	if err := env.db.QueryRow(`SELECT status FROM system_accounts WHERE username = 'pusher'`).Scan(&accountStatus); err != nil {
		t.Fatal(err)
	}
	if accountStatus != "active" {
		t.Fatalf("target user status: %s", accountStatus)
	}

	// The second push resolves the same group (action existing).
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/group/add", addBody, token)
	if status != 200 {
		t.Fatalf("group re-add: %d %v", status, payload)
	}
	if payload["data"].(map[string]any)["action"] != "existing" {
		t.Fatalf("re-add action: %v", payload)
	}

	// List renders the group.
	status, payload, _ = env.doAuth(http.MethodGet, "/__aipublic__/group/list?targetUsername=pusher&providerCode=gpt", "", token)
	if status != 200 {
		t.Fatalf("group list: %d %v", status, payload)
	}
	list := payload["data"].(map[string]any)
	items := list["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != groupID {
		t.Fatalf("group list items: %v", list)
	}

	// Update renames the group.
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/group/update",
		`{"targetUsername":"pusher","groupId":"`+groupID+`","name":"福利站2"}`, token)
	if status != 200 {
		t.Fatalf("group update: %d %v", status, payload)
	}
	if payload["data"].(map[string]any)["action"] != "updated" {
		t.Fatalf("update action: %v", payload)
	}

	// Delete removes it.
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/group/del",
		`{"targetUsername":"pusher","groupId":"`+groupID+`"}`, token)
	if status != 200 {
		t.Fatalf("group delete: %d %v", status, payload)
	}
	if payload["data"].(map[string]any)["action"] != "deleted" {
		t.Fatalf("delete action: %v", payload)
	}
	// Disabled target user renders 400.
	env.seedTargetUser("user_frozen", "frozen", "disabled")
	status, payload, _ = env.doAuth(http.MethodGet, "/__aipublic__/group/list?targetUsername=frozen", "", token)
	if status != 400 || payload["message"] != "目标用户已停用：frozen" {
		t.Fatalf("disabled target: %d %v", status, payload)
	}
}

// TestAIPublicProviderValidation mirrors the provider prechecks.
func TestAIPublicProviderValidation(t *testing.T) {
	env := newAIPublicEnv(t)
	env.seedProvider("gpt", false)
	scopes := []string{"juhe_ai_public:group_add:write"}
	token := env.seedSource("extsrc_p", "exttok_p", "juis_token_pppppppppppppppp",
		"active", "active", scopes, "[]", "", "")
	status, payload, _ := env.doAuth(http.MethodPost, "/__aipublic__/group/add",
		`{"targetUsername":"pusher","name":"x","providerCode":"gpt"}`, token)
	if status != 400 || payload["message"] != "供应商已停用：gpt" {
		t.Fatalf("disabled provider: %d %v", status, payload)
	}
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/group/add",
		`{"targetUsername":"pusher","name":"x","providerCode":"claude"}`, token)
	if status != 400 || payload["message"] != "不支持的供应商：claude" {
		t.Fatalf("unknown provider: %d %v", status, payload)
	}
}

// TestAIPublicStrategyLifecycle drives the route-strategy family over the
// real store: add (binding a seeded group), list (binding hydration),
// update, delete.
func TestAIPublicStrategyLifecycle(t *testing.T) {
	env := newAIPublicEnv(t)
	env.seedProvider("gpt", true)
	env.seedTargetUser("user_pusher", "pusher", "active")
	scopes := []string{
		"juhe_ai_public:group_add:write", "juhe_ai_public:route_strategy_list:read",
		"juhe_ai_public:route_strategy_add:write", "juhe_ai_public:route_strategy_update:write",
		"juhe_ai_public:route_strategy_delete:write",
	}
	token := env.seedSource("extsrc_s", "exttok_s", "juis_token_ssssssssssssssss",
		"active", "active", scopes, "[]", "", "")

	// Seed a group to bind.
	status, payload, _ := env.doAuth(http.MethodPost, "/__aipublic__/group/add",
		`{"targetUsername":"pusher","name":"绑定组","providerCode":"gpt"}`, token)
	if status != 201 {
		t.Fatalf("seed group: %d %v", status, payload)
	}
	groupID := payload["data"].(map[string]any)["group"].(map[string]any)["id"].(string)

	addBody := `{"targetUsername":"pusher","name":"公开策略","groupBindings":[{"groupId":"` + groupID + `","weight":100}]}`
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/route-strategy/add", addBody, token)
	if status != 201 {
		t.Fatalf("strategy add: %d %v", status, payload)
	}
	created := payload["data"].(map[string]any)
	strategy := created["routeStrategy"].(map[string]any)
	strategyID := strategy["id"].(string)
	bindings := strategy["groupBindings"].([]any)
	if len(bindings) != 1 {
		t.Fatalf("bindings: %v", strategy)
	}
	binding := bindings[0].(map[string]any)
	if binding["groupId"] != groupID || binding["weight"] != float64(100) || binding["groupEnabled"] != true {
		t.Fatalf("binding: %v", binding)
	}

	status, payload, _ = env.doAuth(http.MethodGet, "/__aipublic__/route-strategy/list?targetUsername=pusher", "", token)
	if status != 200 {
		t.Fatalf("strategy list: %d %v", status, payload)
	}
	list := payload["data"].(map[string]any)
	items := list["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("list items: %v", list)
	}
	item := items[0].(map[string]any)
	if item["id"] != strategyID || item["mode"] != "normal" {
		t.Fatalf("list item: %v", item)
	}
	if len(item["groupBindings"].([]any)) != 1 {
		t.Fatalf("list bindings: %v", item)
	}

	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/route-strategy/update",
		`{"targetUsername":"pusher","routeStrategyId":"`+strategyID+`","status":"disabled"}`, token)
	if status != 200 {
		t.Fatalf("strategy update: %d %v", status, payload)
	}
	if payload["data"].(map[string]any)["routeStrategy"].(map[string]any)["status"] != "disabled" {
		t.Fatalf("update status: %v", payload)
	}

	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/route-strategy/del",
		`{"targetUsername":"pusher","routeStrategyId":"`+strategyID+`"}`, token)
	if status != 200 {
		t.Fatalf("strategy delete: %d %v", status, payload)
	}
	if payload["data"].(map[string]any)["action"] != "deleted" {
		t.Fatalf("delete action: %v", payload)
	}
}

// TestAIPublicApiKeyLifecycle drives the api-key family over the real store:
// add returns the plaintext key once, list/update/del follow.
func TestAIPublicApiKeyLifecycle(t *testing.T) {
	env := newAIPublicEnv(t)
	env.seedProvider("gpt", true)
	env.seedTargetUser("user_pusher", "pusher", "active")
	scopes := []string{
		"juhe_ai_public:group_add:write", "juhe_ai_public:route_strategy_add:write",
		"juhe_ai_public:api_key_list:read", "juhe_ai_public:api_key_add:write",
		"juhe_ai_public:api_key_update:write", "juhe_ai_public:api_key_delete:write",
	}
	token := env.seedSource("extsrc_k", "exttok_k", "juis_token_kkkkkkkkkkkkkkkk",
		"active", "active", scopes, "[]", "", "")

	// Seed group + strategy.
	env.doAuth(http.MethodPost, "/__aipublic__/group/add",
		`{"targetUsername":"pusher","name":"Key组","providerCode":"gpt"}`, token)
	status, payload, _ := env.doAuth(http.MethodPost, "/__aipublic__/route-strategy/add",
		`{"targetUsername":"pusher","name":"Key策略","groupBindings":[{"groupId":null}]}`, token)
	_ = payload
	if status == 201 {
		t.Fatalf("invalid binding must fail")
	}

	// Build the strategy through the direct store to keep this test focused.
	groupID := ""
	if err := env.db.QueryRow(`SELECT id FROM groups LIMIT 1`).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	seededStrategy := seedStrategyForTest(t, env.db, "user_pusher", groupID)

	addBody := `{"targetUsername":"pusher","name":"公开Key","routeStrategyId":"` + seededStrategy + `"}`
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/api-key/add", addBody, token)
	if status != 201 {
		t.Fatalf("api key add: %d %v", status, payload)
	}
	created := payload["data"].(map[string]any)
	apiKey := created["apiKey"].(map[string]any)
	if apiKey["key"] == nil || apiKey["keyPrefix"] == "" {
		t.Fatalf("api key secret: %v", apiKey)
	}
	apiKeyID := apiKey["id"].(string)

	status, payload, _ = env.doAuth(http.MethodGet, "/__aipublic__/api-key/list?targetUsername=pusher", "", token)
	if status != 200 {
		t.Fatalf("api key list: %d %v", status, payload)
	}
	items := payload["data"].(map[string]any)["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != apiKeyID {
		t.Fatalf("list items: %v", payload)
	}
	if items[0].(map[string]any)["key"] != nil {
		t.Fatalf("list must omit the secret: %v", items[0])
	}

	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/api-key/update",
		`{"targetUsername":"pusher","apiKeyId":"`+apiKeyID+`","status":"disabled"}`, token)
	if status != 200 {
		t.Fatalf("api key update: %d %v", status, payload)
	}
	if payload["data"].(map[string]any)["apiKey"].(map[string]any)["status"] != "disabled" {
		t.Fatalf("update status: %v", payload)
	}

	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/api-key/del",
		`{"targetUsername":"pusher","apiKeyId":"`+apiKeyID+`"}`, token)
	if status != 200 {
		t.Fatalf("api key delete: %d %v", status, payload)
	}
	if payload["data"].(map[string]any)["action"] != "deleted" {
		t.Fatalf("delete action: %v", payload)
	}
}

func seedStrategyForTest(t *testing.T, db *sql.DB, ownerSystemAccountID, groupID string) string {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	strategyID := "rs_test_" + now[len(now)-6:]
	if _, err := db.Exec(`INSERT INTO route_strategies (id, system_account_id, name, mode, status, is_default, created_at, updated_at)
		VALUES (?, ?, 'Key策略', 'normal', 'active', 0, ?, ?)`, strategyID, ownerSystemAccountID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, 1, 100, 'active', ?, ?)`, "rsg_"+strategyID, strategyID, ownerSystemAccountID, groupID, now, now); err != nil {
		t.Fatal(err)
	}
	return strategyID
}

// TestAIPublicAccountLifecycle drives the welfare-account family over the
// real stores: add auto-creates the user + group, list exposes the
// concurrency/priority projection, update patches credentials with the
// configRevision CAS, delete removes the row.
func TestAIPublicAccountLifecycle(t *testing.T) {
	env := newAIPublicEnv(t)
	env.seedProvider("gpt", true)
	if _, err := env.db.Exec(`INSERT INTO provider_protocol_profiles
		(id, provider_code, name, enabled, protocol_code, protocol_version, base_url, default_health_check_model, account_types_json, capabilities_json, created_at, updated_at)
		VALUES ('profile_gpt_openai_v1', 'gpt', 'OpenAI v1', 1, 'openai', 'v1', 'https://api.openai.com', 'gpt-4o-mini', '["api_key"]', '{}', ?, ?)`,
		time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	scopes := []string{
		"juhe_ai_public:account_list:read", "juhe_ai_public:account_add:write",
		"juhe_ai_public:account_update:write", "juhe_ai_public:account_delete:write",
	}
	token := env.seedSource("extsrc_a", "exttok_a2", "juis_token_a2a2a2a2a2a2a2a2",
		"active", "active", scopes, "[]", "", "")

	// Node parity: without supportedModels the create path renders 400
	// 账户支持模型不能为空.
	status, payload, _ := env.doAuth(http.MethodPost, "/__aipublic__/account/add",
		`{"targetUsername":"welfare","targetGroupName":"福利","providerCode":"gpt",`+
			`"providerProtocolProfileId":"profile_gpt_openai_v1","name":"站点A","type":"api_key",`+
			`"baseUrl":"https://a.example","apiKey":"sk-a"}`, token)
	if status != 400 || payload["message"] != "账户支持模型不能为空，请至少选择一个该 Base URL 支持的模型" {
		t.Fatalf("missing models: %d %v", status, payload)
	}
	addBody := `{"targetUsername":"welfare","targetGroupName":"福利","providerCode":"gpt",` +
		`"providerProtocolProfileId":"profile_gpt_openai_v1","name":"站点A","type":"api_key",` +
		`"baseUrl":"https://a.example","apiKey":"sk-a","supportedModels":["gpt-4o-mini"],` +
		`"concurrencyLimit":10,"priority":3}`
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/account/add", addBody, token)
	if status != 201 {
		t.Fatalf("account add: %d %v", status, payload)
	}
	created := payload["data"].(map[string]any)
	if created["action"] != "created" {
		t.Fatalf("account envelope: %v", created)
	}
	account := created["account"].(map[string]any)
	if account["status"] != "pending_test" || account["schedulable"] != false {
		t.Fatalf("created account projection: %v", account)
	}
	targetInfo := created["target"].(map[string]any)
	if targetInfo["groupCreated"] != true || targetInfo["groupName"] != "福利" {
		t.Fatalf("created target: %v", targetInfo)
	}
	accountID := account["id"].(string)

	// Duplicate push renders 409 账号已存在.
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/account/add", addBody, token)
	if status != 409 {
		t.Fatalf("duplicate add: %d %v", status, payload)
	}

	status, payload, _ = env.doAuth(http.MethodGet, "/__aipublic__/account/list?targetUsername=welfare", "", token)
	if status != 200 {
		t.Fatalf("account list: %d %v", status, payload)
	}
	list := payload["data"].(map[string]any)
	items := list["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("list items: %v", list)
	}
	item := items[0].(map[string]any)
	if item["concurrencyLimit"] != float64(10) || item["priority"] != float64(3) {
		t.Fatalf("list projection: %v", item)
	}

	// Update rotates the API key and activates the account.
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/account/update",
		`{"accountId":"`+accountID+`","apiKey":"sk-b","status":"active","supportedModels":["gpt-4o"," gpt-4o "]}`, token)
	if status != 200 {
		t.Fatalf("account update: %d %v", status, payload)
	}
	updated := payload["data"].(map[string]any)
	if updated["action"] != "updated" {
		t.Fatalf("update envelope: %v", updated)
	}
	updatedAccount := updated["account"].(map[string]any)
	if updatedAccount["status"] != "active" || updatedAccount["schedulable"] != true {
		t.Fatalf("updated account: %v", updatedAccount)
	}
	models := updatedAccount["supportedModels"].([]any)
	if len(models) != 1 || models[0] != "gpt-4o" {
		t.Fatalf("supported models dedupe: %v", models)
	}

	// Delete removes the account.
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/account/del",
		`{"accountId":"`+accountID+`"}`, token)
	if status != 200 {
		t.Fatalf("account delete: %d %v", status, payload)
	}
	if payload["data"].(map[string]any)["action"] != "deleted" {
		t.Fatalf("delete action: %v", payload)
	}
}

// TestAIPublicTouchLastUsed verifies the 60s-throttled last_used touch.
func TestAIPublicTouchLastUsed(t *testing.T) {
	env := newAIPublicEnv(t)
	env.seedTargetUser("user_huanmin", "huanmin", "active")
	token := env.seedSource("extsrc_t", "exttok_t", "juis_token_tttttttttttttttt",
		"active", "active", []string{"juhe_ai_public:group_list:read"}, "[]", "", "")
	status, _, _ := env.doAuth(http.MethodGet, "/__aipublic__/group/list?targetUsername=huanmin", "", token)
	if status != 200 {
		t.Fatalf("list: %d", status)
	}
	var tokenUsed, sourceUsed sql.NullString
	if err := env.db.QueryRow(`SELECT last_used_at FROM external_integration_source_tokens WHERE id = 'exttok_t'`).Scan(&tokenUsed); err != nil {
		t.Fatal(err)
	}
	if err := env.db.QueryRow(`SELECT last_used_at FROM external_integration_sources WHERE id = 'extsrc_t'`).Scan(&sourceUsed); err != nil {
		t.Fatal(err)
	}
	if !tokenUsed.Valid || !sourceUsed.Valid {
		t.Fatalf("last_used_at not touched: %v %v", tokenUsed, sourceUsed)
	}
}

var _ = context.Background

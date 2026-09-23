// w14d_http_validation_test.go pins the previously uncovered HTTP branches of
// the oauthmgmt routes: anonymous 401, the scope-query 400, strict-body and
// managed-field 400s, the rotation 409/404 arms, the Grok SSO import token
// limits and the per-plan required-field ValidationErrors (unit-called on the
// plan closures: the routes fold them into the 502 fallback copy).
package oauthmgmt

import (
	"context"
	"net/http"
	"sync"
	"testing"
)

func TestW14dAnonymousAndScopeGuards(t *testing.T) {
	env := newTestEnv(t)

	// Anonymous requests stay 401 on the session-gated self surface.
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/my-openai-oauth/auth-url", `{}`)
	if code != http.StatusUnauthorized {
		t.Fatalf("anonymous auth-url: %d %v", code, payload)
	}

	// A blank systemAccountId query is rejected by the scope guard.
	env.w14dLoginAdmin(t)
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/my-openai-oauth/auth-url?systemAccountId=", `{}`)
	if code != http.StatusBadRequest || payload["message"] != "系统账号 ID 不能为空" {
		t.Fatalf("blank scope: %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/my-gemini-oauth/accounts/x/refresh-token?systemAccountId=%20", `{}`)
	if code != http.StatusBadRequest || payload["message"] != "系统账号 ID 不能为空" {
		t.Fatalf("blank scope rotation: %d %v", code, payload)
	}
}

func TestW14dAuthURLStrictBodyAndValidation(t *testing.T) {
	env := newTestEnv(t)
	env.w14dLoginAdmin(t)

	// Unknown keys on the strict gemini auth-url schema.
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/gemini-oauth/auth-url", `{"nope":1}`)
	if code != http.StatusBadRequest || payload["message"] != "Gemini 授权链接参数无效" {
		t.Fatalf("gemini strict auth-url: %d %v", code, payload)
	}
	// An invalid gemini oauthType surfaces the field validation verbatim.
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/gemini-oauth/auth-url",
		`{"oauthType":"mystery"}`)
	if code != http.StatusBadRequest || payload["message"] != "oauthType 无效" {
		t.Fatalf("gemini oauthType: %d %v", code, payload)
	}
	// A malformed gemini baseUrl.
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/gemini-oauth/auth-url",
		`{"baseUrl":"not-a-url"}`)
	if code != http.StatusBadRequest || payload["message"] != "baseUrl 必须是有效 URL" {
		t.Fatalf("gemini baseUrl: %d %v", code, payload)
	}
	// OpenAI auth-url with an unknown key.
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/auth-url", `{"extra":true}`)
	if code != http.StatusBadRequest || payload["message"] != "OpenAI 授权链接参数无效" {
		t.Fatalf("openai strict auth-url: %d %v", code, payload)
	}
	// Anthropic auth-url unknown key.
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/anthropic-oauth/auth-url", `{"extra":1}`)
	if code != http.StatusBadRequest || payload["message"] != "Anthropic 授权链接参数无效" {
		t.Fatalf("anthropic strict auth-url: %d %v", code, payload)
	}
}

func TestW14dCreateFromCodeValidationMatrix(t *testing.T) {
	env := newTestEnv(t)
	env.w14dLoginAdmin(t)

	// Unknown keys reject the whole create payload (strict schema).
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/create-from-code",
		`{"sessionId":"s","callbackUrl":"https://cb","bogus":1}`)
	if code != http.StatusBadRequest || payload["message"] != "OpenAI 授权码参数无效" {
		t.Fatalf("strict create body: %d %v", code, payload)
	}

	// The managed fields must type-check (并发限制非数字).
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/create-from-code",
		`{"sessionId":"s2","callbackUrl":"https://cb2","concurrencyLimit":"x","providerProtocolProfileId":"profile_gpt_openai_v1"}`)
	if code != http.StatusBadRequest || payload["message"] != "OpenAI 授权码参数无效" {
		t.Fatalf("managed type check: %d %v", code, payload)
	}

	// An unknown group id renders the group validation 400.
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/create-from-code",
		`{"sessionId":"s3","callbackUrl":"https://cb3","groupId":"grp-missing","providerProtocolProfileId":"profile_gpt_openai_v1"}`)
	if code != http.StatusBadRequest || payload["message"] != "账户分组无效" {
		t.Fatalf("unknown group: %d %v", code, payload)
	}

	// A malformed providerProtocolProfileId renders the profile 400.
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/gemini-oauth/create-from-refresh-token",
		`{"refreshToken":"rt","providerProtocolProfileId":"profile-nope"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("unknown profile: %d %v", code, payload)
	}
}

func TestW14dCreateFromRefreshValidationMatrix(t *testing.T) {
	env := newTestEnv(t)
	env.w14dLoginAdmin(t)

	// Strict schema on the refresh create (unknown key).
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/create-from-refresh-token",
		`{"refreshToken":"rt","extra":1}`)
	if code != http.StatusBadRequest || payload["message"] != "Grok 刷新令牌参数无效" {
		t.Fatalf("strict refresh create: %d %v", code, payload)
	}

	// The managed fields must type-check (并发限制非数字).
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/create-from-refresh-token",
		`{"refreshToken":"rt","priority":"x","providerProtocolProfileId":"profile_xai_openai_v1"}`)
	if code != http.StatusBadRequest || payload["message"] != "Grok 刷新令牌参数无效" {
		t.Fatalf("managed type check: %d %v", code, payload)
	}
}

// TestW14dPlansRequiredFieldValidation calls the plan closures directly: the
// routes fold their ValidationErrors into the 502 fallback, so the verbatim
// messages are only observable at this seam.
func TestW14dPlansRequiredFieldValidation(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	store := env.store

	for _, plan := range providerPlans() {
		// exchangeCode: missing sessionId / callbackUrl / credentialsPatch.
		// (gemini resolves the code+state from the callback URL instead, so
		// its closure carries no sessionId/callbackUrl required checks.)
		if plan.slug != "gemini" {
			if _, err := plan.exchangeCode(ctx, store, map[string]any{"callbackUrl": "https://cb"}, "owner", ""); err == nil ||
				err.Error() != "sessionId 不能为空" {
				t.Fatalf("%s exchangeCode sessionId: %v", plan.slug, err)
			}
			if _, err := plan.exchangeCode(ctx, store, map[string]any{"sessionId": "s"}, "owner", ""); err == nil ||
				err.Error() != "callbackUrl 不能为空" {
				t.Fatalf("%s exchangeCode callbackUrl: %v", plan.slug, err)
			}
		}
		if _, err := plan.exchangeCode(ctx, store, map[string]any{
			"sessionId": "s", "callbackUrl": "https://cb", "credentialsPatch": "x",
		}, "owner", ""); err == nil || err.Error() != "credentialsPatch 无效" {
			t.Fatalf("%s exchangeCode patch: %v", plan.slug, err)
		}
		// exchangeRefresh: missing refreshToken / bad patch.
		if _, err := plan.exchangeRefresh(ctx, store, map[string]any{}, ""); err == nil ||
			err.Error() != "refreshToken 不能为空" {
			t.Fatalf("%s exchangeRefresh refreshToken: %v", plan.slug, err)
		}
		if _, err := plan.exchangeRefresh(ctx, store, map[string]any{
			"refreshToken": "rt", "credentialsPatch": 7,
		}, ""); err == nil || err.Error() != "credentialsPatch 无效" {
			t.Fatalf("%s exchangeRefresh patch: %v", plan.slug, err)
		}
		// refreshInput: missing refreshToken.
		if _, err := plan.refreshInput(ctx, store, map[string]any{}, &rotationAccount{Credentials: map[string]any{}}, ""); err == nil ||
			err.Error() != "refreshToken 不能为空" {
			t.Fatalf("%s refreshInput refreshToken: %v", plan.slug, err)
		}
	}

	// Gemini-specific field validation rides the auth/exchange closures.
	gemini := geminiPlan()
	if err := validateGeminiBodyFields(map[string]any{"oauthType": "nope"}); err == nil || err.Error() != "oauthType 无效" {
		t.Fatalf("gemini oauthType: %v", err)
	}
	if err := validateGeminiBodyFields(map[string]any{"baseUrl": "nope"}); err == nil || err.Error() != "baseUrl 必须是有效 URL" {
		t.Fatalf("gemini baseUrl: %v", err)
	}
	if _, err := gemini.authURL(ctx, store, map[string]any{"oauthType": "nope"}, "owner"); err == nil ||
		err.Error() != "oauthType 无效" {
		t.Fatalf("gemini authURL validation: %v", err)
	}
	if _, err := gemini.exchangeCode(ctx, store, map[string]any{"oauthType": "nope"}, "owner", ""); err == nil ||
		err.Error() != "oauthType 无效" {
		t.Fatalf("gemini exchangeCode validation: %v", err)
	}
}

// w14dSeedRotatableAccount seeds an OAuth account row owned by the admin with
// a properly encrypted credentials envelope.
func (env *testEnv) w14dSeedRotatableAccount(t *testing.T, id, provider, profileID, protocol, version string, refreshToken bool) string {
	t.Helper()
	return env.w14dSeedRotatableAccountWithCreds(t, id, provider, profileID, protocol, version,
		map[string]any{"access_token": "a", "refresh_token": "r"}, refreshToken)
}

// w14dSeedRotatableAccountWithCreds seeds the row with explicit credentials.
func (env *testEnv) w14dSeedRotatableAccountWithCreds(t *testing.T, id, provider, profileID, protocol, version string, creds map[string]any, refreshToken bool) string {
	t.Helper()
	adminID := env.w14dLoginAdmin(t)
	refreshFlag := 0
	if refreshToken {
		refreshFlag = 1
	}
	sealed, err := encryptJSON(testSecret, creds)
	if err != nil {
		t.Fatal(err)
	}
	env.exec(t, `INSERT INTO accounts (id, system_account_id, provider_code, provider_protocol_profile_id,
		protocol_code, protocol_version, name, type, status, credentials_encrypted, oauth_refresh_token_present,
		config_revision, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'oauth', 'active', ?, ?, 1, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`,
		id, adminID, provider, profileID, protocol, version, "w14d-acc-"+id, sealed, refreshFlag)
	return adminID
}

func TestW14dRotationValidationAndConflicts(t *testing.T) {
	env := newTestEnv(t)
	env.w14dSeedRotatableAccount(t, "w14d-rot-1", "gpt", "profile_gpt_openai_v1", "openai", "v1", true)

	// Strict body: missing expectedConfigRevision.
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/accounts/w14d-rot-1/refresh-token", `{}`)
	if code != http.StatusBadRequest || payload["message"] != "OpenAI 访问令牌刷新参数无效" {
		t.Fatalf("strict refresh body: %d %v", code, payload)
	}
	// Non-numeric revision.
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/accounts/w14d-rot-1/refresh-token",
		`{"expectedConfigRevision":"x"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("bad revision: %d %v", code, payload)
	}
	// Unknown account id.
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/accounts/w14d-nope/refresh-token",
		`{"expectedConfigRevision":1}`)
	if code != http.StatusNotFound {
		t.Fatalf("missing account: %d %v", code, payload)
	}
	// Stale revision conflicts.
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/accounts/w14d-rot-1/refresh-token",
		`{"expectedConfigRevision":99}`)
	if code != http.StatusConflict || payload["message"] != "OpenAI OAuth 账户已被其他操作更新，请刷新页面后重试" {
		t.Fatalf("stale refresh: %d %v", code, payload)
	}

	// Reauthorize-from-code validation arms.
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/accounts/w14d-rot-1/reauthorize-from-code", `{}`)
	if code != http.StatusBadRequest || payload["message"] != "OpenAI 重新授权参数无效" {
		t.Fatalf("strict reauth body: %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/accounts/w14d-rot-1/reauthorize-from-code",
		`{"sessionId":"s","callbackUrl":"https://cb","expectedConfigRevision":99}`)
	if code != http.StatusConflict {
		t.Fatalf("stale reauth: %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/accounts/w14d-nope/reauthorize-from-code",
		`{"sessionId":"s","callbackUrl":"https://cb","expectedConfigRevision":1}`)
	if code != http.StatusNotFound {
		t.Fatalf("missing reauth account: %d %v", code, payload)
	}

	// Reauthorize-from-refresh-token validation arms.
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/accounts/w14d-rot-1/reauthorize-from-refresh-token", `{}`)
	if code != http.StatusBadRequest || payload["message"] != "OpenAI 刷新令牌参数无效" {
		t.Fatalf("strict reauth refresh body: %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/accounts/w14d-rot-1/reauthorize-from-refresh-token",
		`{"refreshToken":"rt","expectedConfigRevision":99}`)
	if code != http.StatusConflict {
		t.Fatalf("stale reauth refresh: %d %v", code, payload)
	}
}

func TestW14dRefreshMissingRefreshToken(t *testing.T) {
	env := newTestEnv(t)
	env.w14dSeedRotatableAccountWithCreds(t, "w14d-rot-2", "anthropic", "profile_anthropic_anthropic_v1",
		"anthropic", "v1", map[string]any{"access_token": "a"}, false)

	// The rotation refresh requires a stored refresh token (non-openai plans
	// reject it before the upstream call).
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/anthropic-oauth/accounts/w14d-rot-2/refresh-token",
		`{"expectedConfigRevision":1}`)
	if code != http.StatusBadRequest || payload["message"] != "Anthropic OAuth 账户缺少 Refresh Token" {
		t.Fatalf("missing stored refresh: %d %v", code, payload)
	}
}

func TestW14dSSOImportValidation(t *testing.T) {
	env := newTestEnv(t)
	env.w14dLoginAdmin(t)

	// Unknown keys reject the import payload.
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/sso-to-oauth", `{"nope":1}`)
	if code != http.StatusBadRequest || payload["message"] != "Grok SSO 导入参数无效" {
		t.Fatalf("strict sso body: %d %v", code, payload)
	}

	// An empty token list is rejected (distinct fingerprint via the name field).
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/sso-to-oauth",
		`{"ssoToken":"  ","name":"w14d-empty"}`)
	if code != http.StatusBadRequest || payload["message"] != "Grok SSO Cookie 不能为空" {
		t.Fatalf("empty sso: %d %v", code, payload)
	}

	// More than three tokens are rejected.
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/sso-to-oauth",
		`{"ssoTokens":["t1","t2","t3","t4"],"name":"w14d-cap"}`)
	if code != http.StatusBadRequest || payload["message"] != "Grok SSO Cookie 单次最多导入 3 个" {
		t.Fatalf("token cap: %d %v", code, payload)
	}

	// Managed field type errors surface the generic invalid message.
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/sso-to-oauth",
		`{"ssoToken":"tok","concurrencyLimit":"x","name":"w14d-managed"}`)
	if code != http.StatusBadRequest || payload["message"] != "Grok SSO 导入参数无效" {
		t.Fatalf("managed types: %d %v", code, payload)
	}

	// A failed exchange lands in the failed list and keeps the response 200.
	env.exchanger.respond = func(_ int, call exchangeCall) (int, string) {
		if call.URL != GrokOAuthTokenURL {
			return http.StatusNotFound, `{}`
		}
		return http.StatusInternalServerError, `{"error":"upstream"}`
	}
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/sso-to-oauth",
		`{"ssoTokens":["tok-bad"],"name":"w14d-failed","providerProtocolProfileId":"profile_xai_openai_v1"}`)
	if code != http.StatusOK {
		t.Fatalf("failed exchange import: %d %v", code, payload)
	}
	data := dataMap(t, payload)
	if data["createdCount"] != float64(0) {
		t.Fatalf("created count: %v", data)
	}
}

func TestW14dCredentialsPatchParsers(t *testing.T) {
	// Unknown keys fail every dialect.
	for _, parse := range []func(map[string]any) (map[string]any, bool){
		parseOpenAICredentialsPatch, parseAnthropicCredentialsPatch,
		parseGeminiCredentialsPatch, parseGrokCredentialsPatch,
	} {
		if _, ok := parse(map[string]any{"mystery": 1}); ok {
			t.Fatal("unknown patch key must fail")
		}
	}
	// Endpoint modes: wrong element type / blank entries / empty list.
	for _, modes := range []any{[]any{7}, []any{" "}, []any{}, "chat"} {
		if _, ok := parseGrokCredentialsPatch(map[string]any{"supported_endpoint_modes": modes}); ok {
			t.Fatalf("invalid endpoint modes must fail: %v", modes)
		}
	}
	patch, ok := parseGrokCredentialsPatch(map[string]any{"supported_endpoint_modes": []any{"chat", " responses "}})
	if !ok {
		t.Fatal("valid endpoint modes must pass")
	}
	modes, isList := patch["supported_endpoint_modes"].([]any)
	if !isList || len(modes) != 2 || modes[1] != "responses" {
		t.Fatalf("parsed endpoint modes: %#v", patch["supported_endpoint_modes"])
	}
	// Service tier overrides accept only known tiers.
	if _, ok := parseOpenAICredentialsPatch(map[string]any{"service_tier_override": "mystery"}); ok {
		t.Fatal("unknown service tier must fail")
	}
	if _, ok := parseOpenAICredentialsPatch(map[string]any{"reasoning_effort_override": "mystery"}); ok {
		t.Fatal("unknown reasoning effort must fail")
	}
	if _, ok := parseOpenAICredentialsPatch(map[string]any{"service_tier_override": "priority"}); !ok {
		t.Fatal("known service tier must pass")
	}
	if _, ok := parseOpenAICredentialsPatch(map[string]any{"reasoning_effort_override": "high"}); !ok {
		t.Fatal("known reasoning effort must pass")
	}
	// Blank override strings are rejected on every dialect.
	for _, parse := range []func(map[string]any) (map[string]any, bool){
		parseOpenAICredentialsPatch, parseGeminiCredentialsPatch, parseGrokCredentialsPatch,
	} {
		if _, ok := parse(map[string]any{"service_tier_override": " "}); ok {
			t.Fatal("blank service tier must fail")
		}
		if _, ok := parse(map[string]any{"reasoning_effort_override": " "}); ok {
			t.Fatal("blank reasoning effort must fail")
		}
	}
	// Anthropic accepts a plain base_url (trim-only).
	if patch, ok := parseAnthropicCredentialsPatch(map[string]any{"base_url": " https://a.example "}); !ok ||
		patch["base_url"] != "https://a.example" {
		t.Fatalf("anthropic base_url: %v", patch)
	}
	// Gemini base_url must be a URL.
	if _, ok := parseGeminiCredentialsPatch(map[string]any{"base_url": "not-url"}); ok {
		t.Fatal("gemini non-url base_url must fail")
	}
	if patch, ok := parseGeminiCredentialsPatch(map[string]any{"base_url": "https://g.example"}); !ok ||
		patch["base_url"] != "https://g.example" {
		t.Fatalf("gemini base_url: %v", patch)
	}
	// Grok accepts a plain base_url.
	if patch, ok := parseGrokCredentialsPatch(map[string]any{"base_url": " https://k.example "}); !ok ||
		patch["base_url"] != "https://k.example" {
		t.Fatalf("grok base_url: %v", patch)
	}
	// Policy keys pass through untouched.
	if patch, ok := parseAnthropicCredentialsPatch(map[string]any{"error_handling_rules": []any{"rule"}}); !ok {
		t.Fatal("policy key must pass")
	} else if patch["error_handling_rules"] == nil {
		t.Fatalf("policy passthrough: %v", patch)
	}
	// Non-string base_url values fail.
	for _, parse := range []func(map[string]any) (map[string]any, bool){
		parseAnthropicCredentialsPatch, parseGrokCredentialsPatch,
	} {
		if _, ok := parse(map[string]any{"base_url": 7}); ok {
			t.Fatal("non-string base_url must fail")
		}
	}
}

// w14dAdminIDs memoizes the super_admin account id per env so repeated
// logins within one test do not hit the unique-username create guard.
var w14dAdminIDs sync.Map

// w14dLoginAdmin logs the super_admin in (once per env).
func (env *testEnv) w14dLoginAdmin(t *testing.T) string {
	t.Helper()
	if cached, ok := w14dAdminIDs.Load(env); ok {
		return cached.(string)
	}
	id := env.login(t, "root", "root-pass", "super_admin")
	w14dAdminIDs.Store(env, id)
	return id
}

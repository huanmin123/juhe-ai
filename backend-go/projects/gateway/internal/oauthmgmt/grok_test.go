package oauthmgmt

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func grokTokenPayload(accessToken string) string {
	claims := map[string]any{
		"email":              "grok@example.com",
		"sub":                "grok-sub-1",
		"team_id":            "team-1",
		"subscription_tier":  "superhero",
		"entitlement_status": "active",
	}
	return fmt.Sprintf(`{"access_token":%q,"id_token":%q,"refresh_token":"grok-refresh-1","token_type":"Bearer","expires_in":21600,"scope":%q}`,
		accessToken, fakeJWT(claims), GrokOAuthScope)
}

func ssoStep(status int, headers map[string]string, body string) SSODeviceResponse {
	return SSODeviceResponse{StatusCode: status, Headers: headers, Body: body}
}

func TestGrokSSOToDeviceFlow(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")

	// auth-url shape.
	code, authURLPayload := env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/auth-url", `{}`)
	if code != http.StatusOK {
		t.Fatalf("grok auth-url: %d %v", code, authURLPayload)
	}
	authData := dataMap(t, authURLPayload)
	parsed, err := url.Parse(authData["authUrl"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Scheme != "https" || parsed.Host != "auth.x.ai" || parsed.Path != "/oauth2/authorize" {
		t.Fatalf("grok authorize url: %v", parsed)
	}
	query := parsed.Query()
	if query.Get("client_id") != GrokOAuthClientID ||
		query.Get("redirect_uri") != GrokOAuthRedirectURI ||
		query.Get("scope") != GrokOAuthScope ||
		query.Get("plan") != "generic" ||
		query.Get("referrer") != "sub2api" ||
		query.Get("nonce") == "" || query.Get("state") == "" ||
		query.Get("code_challenge_method") != "S256" {
		t.Fatalf("grok authorize params: %v", query)
	}

	// Device flow script: accounts check → device code → verification page →
	// consent redirect → approve → done → token.
	tokenBody := grokTokenPayload("grok-access-1")
	env.sso.steps = []SSODeviceResponse{
		ssoStep(http.StatusOK, nil, "<html>accounts</html>"),
		ssoStep(http.StatusOK, nil, `{"device_code":"dc-1","user_code":"UC-1","verification_uri_complete":"https://auth.x.ai/device?user_code=UC-1","interval":1,"expires_in":600}`),
		ssoStep(http.StatusOK, nil, "<html>device page</html>"),
		ssoStep(http.StatusFound, map[string]string{"location": "https://auth.x.ai/oauth2/device/consent"}, ""),
		ssoStep(http.StatusOK, nil, "<html>consent</html>"),
		ssoStep(http.StatusFound, map[string]string{"location": "https://auth.x.ai/oauth2/device/done"}, ""),
		ssoStep(http.StatusOK, nil, "<html>done</html>"),
		ssoStep(http.StatusOK, nil, tokenBody),
	}
	env.exchanger.respond = staticToken(`{}`)

	code, imported := env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/sso-to-oauth",
		`{"ssoTokens":["sso=abc; other=1"],"providerProtocolProfileId":"profile_xai_openai_v1"}`)
	if code != http.StatusOK {
		t.Fatalf("sso-to-oauth: %d %v", code, imported)
	}
	result := dataMap(t, imported)
	if result["createdCount"] != float64(1) {
		t.Fatalf("sso result: %v", result)
	}
	createdIDs := result["createdIds"].([]any)
	if len(createdIDs) != 1 {
		t.Fatalf("createdIds: %v", createdIDs)
	}
	accountID := createdIDs[0].(string)
	if failed := result["failed"].([]any); len(failed) != 0 {
		t.Fatalf("failed items: %v", failed)
	}

	// Device flow request assertions: SSO cookie header, device form, token
	// poll grant.
	calls := env.sso.recorded()
	if len(calls) != 8 {
		t.Fatalf("device flow calls: %d", len(calls))
	}
	if calls[0].Method != "GET" || calls[0].URL != GrokSSOAccountsURL {
		t.Fatalf("call 0: %v", calls[0])
	}
	if cookie := calls[0].Headers["cookie"]; !strings.Contains(cookie, "sso=abc") || !strings.Contains(cookie, "sso-rw=abc") {
		t.Fatalf("sso cookie header: %q", cookie)
	}
	if calls[1].Method != "POST" || calls[1].URL != GrokSSODeviceURL {
		t.Fatalf("call 1: %v", calls[1])
	}
	deviceForm := mustForm(t, calls[1].Body)
	if deviceForm.Get("client_id") != GrokSSOClientID || deviceForm.Get("scope") != GrokSSOBuildScope {
		t.Fatalf("device form: %v", deviceForm)
	}
	tokenCall := calls[7]
	if tokenCall.URL != GrokSSOTokenURL {
		t.Fatalf("token call: %v", tokenCall)
	}
	tokenForm := mustForm(t, tokenCall.Body)
	if tokenForm.Get("grant_type") != "urn:ietf:params:oauth:grant-type:device_code" ||
		tokenForm.Get("device_code") != "dc-1" || tokenForm.Get("client_id") != GrokSSOClientID {
		t.Fatalf("token poll form: %v", tokenForm)
	}

	// Account assertions: profile pin, credential shape, claim-derived fields.
	var providerCode, profileID, accountType, name string
	if err := env.db.QueryRow(`SELECT provider_code, provider_protocol_profile_id, type, name
		FROM accounts WHERE id = ?`, accountID).Scan(&providerCode, &profileID, &accountType, &name); err != nil {
		t.Fatal(err)
	}
	if providerCode != "xai" || profileID != "profile_xai_openai_v1" || accountType != "oauth" || name != "grok@example.com" {
		t.Fatalf("grok account row: %s %s %s %s", providerCode, profileID, accountType, name)
	}
	credentials := env.accountCredentials(t, accountID)
	if credentials["access_token"] != "grok-access-1" ||
		credentials["refresh_token"] != "grok-refresh-1" ||
		credentials["base_url"] != GrokOAuthBaseURL ||
		credentials["client_id"] != GrokOAuthClientID ||
		credentials["token_type"] != "Bearer" ||
		credentials["sub"] != "grok-sub-1" ||
		credentials["team_id"] != "team-1" ||
		credentials["subscription_tier"] != "superhero" ||
		credentials["entitlement_status"] != "active" {
		t.Fatalf("grok credentials: %v", credentials)
	}
	if !env.sink.has("grok_oauth.sso_to_oauth") {
		t.Fatalf("sso log: %v", env.sink.actions())
	}

	// Input guards: empty → 400, >3 tokens → 400.
	code, empty := env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/sso-to-oauth",
		`{"providerProtocolProfileId":"profile_xai_openai_v1"}`)
	if code != http.StatusBadRequest || empty["message"] != "Grok SSO Cookie 不能为空" {
		t.Fatalf("empty sso: %d %v", code, empty)
	}
	code, tooMany := env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/sso-to-oauth",
		`{"ssoTokens":["a","b","c","d"],"providerProtocolProfileId":"profile_xai_openai_v1"}`)
	if code != http.StatusBadRequest || tooMany["message"] != "Grok SSO Cookie 单次最多导入 3 个" {
		t.Fatalf("too many sso: %d %v", code, tooMany)
	}

	// SSO failure path: unauthorized accounts page renders a per-item failure.
	env.sso.steps = []SSODeviceResponse{
		ssoStep(http.StatusUnauthorized, nil, `<html>sign-in</html>`),
	}
	code, failed := env.do(t, http.MethodPost, "/__aisys__/api/my-grok-oauth/sso-to-oauth",
		`{"ssoToken":"cookie: sso=bad-token","providerProtocolProfileId":"profile_xai_openai_v1"}`)
	if code != http.StatusOK {
		t.Fatalf("failed sso import: %d %v", code, failed)
	}
	failedResult := dataMap(t, failed)
	if failedResult["createdCount"] != float64(0) {
		t.Fatalf("failed sso result: %v", failedResult)
	}
	failedItems := failedResult["failed"].([]any)
	if len(failedItems) != 1 {
		t.Fatalf("failed items: %v", failedItems)
	}
	item := failedItems[0].(map[string]any)
	if item["index"] != float64(1) || item["error"] != "Grok SSO Cookie 转换失败" {
		t.Fatalf("failed item: %v", item)
	}
}

func TestGrokCodeAndRefreshFamily(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")
	env.exchanger.respond = staticToken(grokTokenPayload("grok-access-create"))

	// Bare-code callback (xAI CLI form): no state required.
	code, authURLPayload := env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/auth-url", `{}`)
	sessionID := dataMap(t, authURLPayload)["sessionId"].(string)
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/create-from-code",
		fmt.Sprintf(`{"sessionId":%q,"callbackUrl":"bare-auth-code","providerProtocolProfileId":"profile_xai_openai_v1"}`, sessionID))
	if code != http.StatusCreated {
		t.Fatalf("grok create-from-code: %d %v", code, created)
	}
	accountID := dataMap(t, created)["id"].(string)
	credentials := env.accountCredentials(t, accountID)
	if credentials["access_token"] != "grok-access-create" || credentials["refresh_token"] != "grok-refresh-1" {
		t.Fatalf("grok create credentials: %v", credentials)
	}

	// Wrong state on the URL form → 400 (grokOAuthError renders the fallback).
	code, fresh := env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/auth-url", `{}`)
	freshSession := dataMap(t, fresh)["sessionId"].(string)
	code, badState := env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/create-from-code",
		fmt.Sprintf(`{"sessionId":%q,"callbackUrl":"http://127.0.0.1:56121/callback?code=c1&state=nope","providerProtocolProfileId":"profile_xai_openai_v1"}`, freshSession))
	if code != http.StatusBadRequest || badState["message"] != "Grok 授权码交换失败" {
		t.Fatalf("grok wrong state: %d %v", code, badState)
	}

	// create-from-refresh-token + manual refresh + reauthorize.
	code, fromRefresh := env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/create-from-refresh-token",
		`{"refreshToken":"grok-refresh-A","name":"Grok Refresh Account","providerProtocolProfileId":"profile_xai_openai_v1"}`)
	if code != http.StatusCreated {
		t.Fatalf("grok create-from-refresh-token: %d %v", code, fromRefresh)
	}
	refreshAccountID := dataMap(t, fromRefresh)["id"].(string)

	env.exchanger.respond = staticToken(`{"access_token":"grok-access-rotated","refresh_token":"grok-refresh-2","token_type":"Bearer","expires_in":21600}`)
	code, refreshed := env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/accounts/"+refreshAccountID+"/refresh-token", `{"expectedConfigRevision":1}`)
	if code != http.StatusOK || dataMap(t, refreshed)["configRevision"] != float64(2) {
		t.Fatalf("grok refresh: %d %v", code, refreshed)
	}
	credentials = env.accountCredentials(t, refreshAccountID)
	if credentials["access_token"] != "grok-access-rotated" || credentials["refresh_token"] != "grok-refresh-2" {
		t.Fatalf("grok rotated credentials: %v", credentials)
	}

	// Missing stored refresh token → 400 pre-check.
	env.exchanger.respond = staticToken(`{"access_token":"x","token_type":"Bearer","expires_in":21600}`)
	code, noRefresh := env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/create-from-refresh-token",
		`{"refreshToken":"grok-refresh-E","name":"Grok Stripped Account","providerProtocolProfileId":"profile_xai_openai_v1"}`)
	if code != http.StatusCreated {
		t.Fatalf("grok create for refresh-token removal: %d %v", code, noRefresh)
	}
	strippedID := dataMap(t, noRefresh)["id"].(string)
	credentials = env.accountCredentials(t, strippedID)
	delete(credentials, "refresh_token")
	sealed, err := encryptJSON(testSecret, credentials)
	if err != nil {
		t.Fatal(err)
	}
	env.exec(t, `UPDATE accounts SET credentials_encrypted = ? WHERE id = ?`, sealed, strippedID)
	code, missing := env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/accounts/"+strippedID+"/refresh-token", `{"expectedConfigRevision":1}`)
	if code != http.StatusBadRequest || missing["message"] != "Grok OAuth 账户缺少 Refresh Token" {
		t.Fatalf("grok missing refresh token: %d %v", code, missing)
	}

	// Profile pin: another openai-protocol profile is not rotatable via grok.
	env.exec(t, `UPDATE accounts SET provider_protocol_profile_id = 'profile_other' WHERE id = ?`, strippedID)
	code, unrotatable := env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/accounts/"+strippedID+"/refresh-token", `{"expectedConfigRevision":1}`)
	if code != http.StatusNotFound || unrotatable["message"] != "Grok OAuth 账户不存在或无权操作" {
		t.Fatalf("grok unrotatable profile: %d %v", code, unrotatable)
	}
}

func TestOAuthPermissionMatrix(t *testing.T) {
	env := newTestEnv(t)

	// Anonymous access refused on both surfaces (no login yet, empty jar).
	for _, path := range []string{
		"/__aisys__/api/openai-oauth/auth-url",
		"/__aisys__/api/my-openai-oauth/auth-url",
		"/__aisys__/api/my-grok-oauth/sso-to-oauth",
		"/__aisys__/api/anthropic-oauth/accounts/acc-1/refresh-token",
	} {
		code, payload := env.do(t, http.MethodPost, path, `{}`)
		if code != http.StatusUnauthorized || payload["message"] != "请先登录" {
			t.Fatalf("anonymous %s: %d %v", path, code, payload)
		}
	}
	code, _ := env.do(t, http.MethodGet, "/__aisys__/api/gemini-oauth/capabilities", "")
	if code != http.StatusUnauthorized {
		t.Fatalf("anonymous capabilities: %d", code)
	}

	_ = env.login(t, "alice", "alice-pass", "user")
	bobID := env.login(t, "bob", "bob-pass", "user")

	// Bob (user role) cannot reach admin surfaces.
	for _, item := range []struct {
		method, path, body string
	}{
		{"POST", "/__aisys__/api/openai-oauth/auth-url", `{}`},
		{"POST", "/__aisys__/api/openai-oauth/create-from-refresh-token", `{"refreshToken":"x","providerProtocolProfileId":"profile_gpt_openai_v1"}`},
		{"GET", "/__aisys__/api/gemini-oauth/capabilities", ""},
	} {
		code, payload := env.do(t, item.method, item.path, item.body)
		if code != http.StatusForbidden || payload["message"] != "需要管理员权限" {
			t.Fatalf("user on admin surface %s: %d %v", item.path, code, payload)
		}
	}

	// Bob creates an account via my-*: owner is bob.
	env.exchanger.respond = staticToken(openAITokenPayload("bob-access"))
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/my-openai-oauth/create-from-refresh-token",
		`{"refreshToken":"bob-refresh-1","providerProtocolProfileId":"profile_gpt_openai_v1"}`)
	if code != http.StatusCreated {
		t.Fatalf("bob my create: %d %v", code, created)
	}
	bobAccountID := dataMap(t, created)["id"].(string)
	var ownerID string
	if err := env.db.QueryRow(`SELECT system_account_id FROM accounts WHERE id = ?`, bobAccountID).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	if ownerID != bobID {
		t.Fatalf("bob account owner: %s (bob %s)", ownerID, bobID)
	}

	// Admins are unscoped without a filter (manageableSystemAccountId):
	// bob's account is rotatable from the admin surface.
	env.login(t, "root", "root-pass", "super_admin")
	env.exchanger.respond = staticToken(openAITokenPayload("bob-access-2"))
	code, adminNoFilter := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/accounts/"+bobAccountID+"/refresh-token", `{"expectedConfigRevision":1}`)
	if code != http.StatusOK || dataMap(t, adminNoFilter)["configRevision"] != float64(2) {
		t.Fatalf("admin without filter: %d %v", code, adminNoFilter)
	}
	env.exchanger.respond = staticToken(openAITokenPayload("bob-access-3"))
	code, adminFiltered := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/accounts/"+bobAccountID+"/refresh-token?systemAccountId="+url.QueryEscape(bobID), `{"expectedConfigRevision":2}`)
	if code != http.StatusOK || dataMap(t, adminFiltered)["configRevision"] != float64(3) {
		t.Fatalf("admin with filter: %d %v", code, adminFiltered)
	}

	// Admin on my-* is forced to self scope → bob's account is invisible.
	code, adminSelf := env.do(t, http.MethodPost, "/__aisys__/api/my-openai-oauth/accounts/"+bobAccountID+"/refresh-token", `{"expectedConfigRevision":2}`)
	if code != http.StatusNotFound {
		t.Fatalf("admin my-*: %d %v", code, adminSelf)
	}

	// Provider mismatch: the openai account is not rotatable via anthropic.
	code, mismatch := env.do(t, http.MethodPost, "/__aisys__/api/anthropic-oauth/accounts/"+bobAccountID+"/refresh-token", `{"expectedConfigRevision":2}`)
	if code != http.StatusNotFound || mismatch["message"] != "Anthropic OAuth 账户不存在或无权操作" {
		t.Fatalf("provider mismatch: %d %v", code, mismatch)
	}
}

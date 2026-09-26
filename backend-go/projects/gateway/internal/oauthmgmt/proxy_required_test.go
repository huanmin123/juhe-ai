// proxy_required_test.go covers the requiresProxy enforcement: the four
// overseas OAuth providers (openai/anthropic/gemini/grok) must bind an enabled
// proxy profile on create/refresh/reauthorize/SSO-import, while disabled
// enforcement keeps the legacy no-proxy fixtures working.
package oauthmgmt

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// newProxyRequiredEnv builds an env with the enforcement ON and one enabled
// proxy profile seeded for the happy paths.
func newProxyRequiredEnv(t *testing.T) *testEnv {
	t.Helper()
	env := newTestEnv(t)
	// 重新打开拦截：newTestEnv 默认 WithProxyRequired(false)。Store 不可变，
	// 直接翻转字段（同包测试可见）。
	env.store.proxyRequired = true
	if _, err := env.db.Exec(`INSERT INTO proxy_profiles (id, system_account_id, name, type, host, port, enabled, test_status, created_at, updated_at)
		VALUES ('proxy-oauth-test', 'system', '测试出海代理', 'socks5', '127.0.0.1', 7890, 1, 'unknown', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	return env
}

func TestProxyRequiredBlocksCreateWithoutProxy(t *testing.T) {
	env := newProxyRequiredEnv(t)
	env.w14dLoginAdmin(t)

	for _, route := range []string{"grok-oauth", "openai-oauth", "anthropic-oauth", "gemini-oauth"} {
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/"+route+"/create-from-code",
			`{"sessionId":"missing","callbackUrl":"https://cb?code=c&state=s","providerProtocolProfileId":"ignored"}`)
		if code != http.StatusBadRequest {
			t.Fatalf("%s create without proxy: %d %v", route, code, payload)
		}
		if message, _ := payload["message"].(string); !strings.Contains(message, "必须绑定代理") {
			t.Fatalf("%s create without proxy message: %v", route, payload)
		}
	}
}

func TestProxyRequiredAllowsCreateWithProxy(t *testing.T) {
	env := newProxyRequiredEnv(t)
	env.w14dLoginAdmin(t)
	env.exchanger.respond = staticToken(grokTokenPayload("grok-proxy-required"))

	code, authPayload := env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/auth-url", `{}`)
	if code != http.StatusOK {
		t.Fatalf("auth-url: %d %v", code, authPayload)
	}
	authData := dataMap(t, authPayload)
	sessionID := authData["sessionId"].(string)
	state := authData["state"].(string)
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/create-from-code",
		fmt.Sprintf(`{"sessionId":%q,"callbackUrl":"http://127.0.0.1:56121/callback?code=c1&state=%s","providerProtocolProfileId":"profile_xai_openai_v1","proxyProfileId":"proxy-oauth-test"}`, sessionID, state))
	if code != http.StatusCreated {
		t.Fatalf("grok create with proxy: %d %v", code, created)
	}
}

func TestProxyRequiredAllowsLegacyEnvironments(t *testing.T) {
	env := newTestEnv(t)
	env.w14dLoginAdmin(t)
	env.exchanger.respond = staticToken(grokTokenPayload("grok-legacy"))

	code, authPayload := env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/auth-url", `{}`)
	if code != http.StatusOK {
		t.Fatalf("auth-url: %d %v", code, authPayload)
	}
	sessionID := dataMap(t, authPayload)["sessionId"].(string)
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/create-from-code",
		fmt.Sprintf(`{"sessionId":%q,"callbackUrl":"bare-code","providerProtocolProfileId":"profile_xai_openai_v1"}`, sessionID))
	if code != http.StatusCreated {
		t.Fatalf("legacy no-proxy create: %d %v", code, created)
	}
}

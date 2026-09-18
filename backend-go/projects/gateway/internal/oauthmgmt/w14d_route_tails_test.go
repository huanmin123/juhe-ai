// w14d_route_tails_test.go closes the remaining route-tail branches: the
// guarded scope 400, the openai stored-refresh 502 fallback, the reauthorize
// exchange failure copy, the SSO import profile/group/expires arms and the
// anthropic upstream refresh fallback.
package oauthmgmt

import (
	"net/http"
	"strings"
	"testing"
)

func TestW14dGuardedScopeQueryFork(t *testing.T) {
	env := newTestEnv(t)
	env.w14dLoginAdmin(t)

	// The mutation guard rejects an explicit blank scope before the handler.
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/create-from-code?systemAccountId=", `{}`)
	if code != http.StatusBadRequest {
		t.Fatalf("guarded scope 400: %d %v", code, payload)
	}
}

func TestW14dOpenAIStoredRefreshMissingFallsBack(t *testing.T) {
	env := newTestEnv(t)
	env.w14dSeedRotatableAccountWithCreds(t, "w14d-rot-nr", "gpt", "profile_gpt_openai_v1", "openai", "v1",
		map[string]any{"access_token": "a"}, false)
	env.w14dLoginAdmin(t)

	// The openai stored refresh wraps its missing-token validation into the
	// route fallback at 502.
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/accounts/w14d-rot-nr/refresh-token",
		`{"expectedConfigRevision":1}`)
	if code != http.StatusBadGateway || payload["message"] != "OpenAI 访问令牌刷新失败" {
		t.Fatalf("openai missing stored refresh: %d %v", code, payload)
	}
}

func TestW14dReauthorizeExchangeFailureCopy(t *testing.T) {
	env := newTestEnv(t)
	env.w14dSeedRotatableAccount(t, "w14d-reauth-g", "xai", "profile_xai_openai_v1", "openai", "v1", true)
	env.w14dLoginAdmin(t)

	// The grok code exchange fails (the scripted SSO device answers 500): the
	// route renders the reauthorize fallback copy at 502.
	env.sso.steps = []SSODeviceResponse{ssoStep(http.StatusInternalServerError, nil, "boom")}
	_, authPayload := env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/auth-url", `{}`)
	authData := dataMap(t, authPayload)
	sessionID := authData["sessionId"].(string)
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/accounts/w14d-reauth-g/reauthorize-from-code",
		`{"sessionId":"`+sessionID+`","callbackUrl":"https://cb?code=c","expectedConfigRevision":1}`)
	// The grok error renders its 400 status with the route fallback copy.
	if code != http.StatusBadRequest || payload["message"] != "Grok OAuth 重新授权失败" {
		t.Fatalf("grok reauth exchange failure: %d %v", code, payload)
	}
}

func TestW14dAnthropicRefreshUpstreamFallback(t *testing.T) {
	env := newTestEnv(t)
	env.w14dSeedRotatableAccount(t, "w14d-rot-an", "anthropic", "profile_anthropic_anthropic_v1", "anthropic", "v1", true)
	env.w14dLoginAdmin(t)

	// The anthropic upstream refresh fails at 500: the route renders the
	// fallback copy at 502.
	env.exchanger.respond = func(_ int, call exchangeCall) (int, string) {
		if call.URL != AnthropicOAuthTokenURL {
			return http.StatusNotFound, `{}`
		}
		return http.StatusInternalServerError, `{"error":"down"}`
	}
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/anthropic-oauth/accounts/w14d-rot-an/refresh-token",
		`{"expectedConfigRevision":1}`)
	// Upstream failures surface verbatim (the UpstreamError arm).
	if code != http.StatusBadGateway || !strings.Contains(payload["message"].(string), "HTTP 500") {
		t.Fatalf("anthropic refresh fallback: %d %v", code, payload)
	}
}

func TestW14dSSOImportProfileAndGroupArms(t *testing.T) {
	env := newTestEnv(t)
	env.w14dLoginAdmin(t)

	// Dropping the profile table turns the SSO profile resolve into a 500.
	env.exec(t, `DROP TABLE provider_protocol_profiles`)
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/sso-to-oauth",
		`{"ssoToken":"tok","providerProtocolProfileId":"profile_xai_openai_v1","name":"w14d-prof"}`)
	if code != http.StatusInternalServerError || payload["message"] != "服务器内部错误" {
		t.Fatalf("sso profile 500: %d %v", code, payload)
	}
}

func TestW14dSSOImportUnknownGroup(t *testing.T) {
	env := newTestEnv(t)
	env.w14dLoginAdmin(t)

	// An unknown group id fails before any token conversion.
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/sso-to-oauth",
		`{"ssoToken":"tok","groupId":"grp-missing","name":"w14d-grp","providerProtocolProfileId":"profile_xai_openai_v1"}`)
	if code != http.StatusBadRequest || payload["message"] != "账户分组无效" {
		t.Fatalf("sso group 400: %d %v", code, payload)
	}
	// The duplicate group id variant rides the same arm.
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/sso-to-oauth",
		`{"ssoToken":"tok","groupId":"grp-missing","groupId2":"x","name":"w14d-grp2","providerProtocolProfileId":"profile_xai_openai_v1"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("unknown key guard: %d %v", code, payload)
	}
	if strings.Contains(payload["message"].(string), "账户分组") {
		t.Fatalf("unknown keys must fail first: %v", payload)
	}
}

func TestW14dSSOImportExpiresValidation(t *testing.T) {
	env := newTestEnv(t)
	env.w14dLoginAdmin(t)

	// A malformed accountExpiresAt lands the token in the failed list while
	// the overall import stays 200.
	successSteps := wcDeviceSuccessSteps(`{"access_token":"at","refresh_token":"rt","expires_in":3600,"token_type":"Bearer"}`)
	env.sso.steps = append([]SSODeviceResponse{}, successSteps...)
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/sso-to-oauth",
		`{"ssoToken":"tok","accountExpiresAt":"not-a-date","name":"w14d-exp","providerProtocolProfileId":"profile_xai_openai_v1"}`)
	if code != http.StatusOK {
		t.Fatalf("sso expires import: %d %v", code, payload)
	}
	data := dataMap(t, payload)
	if data["createdCount"] != float64(0) {
		t.Fatalf("created: %v", data)
	}
}

// w14d_rotation_flows_test.go pins the reauthorize/refresh success and error
// tails: full reauthorize-from-code and reauthorize-from-refresh flows with
// scripted upstreams, the 502 fallback arms, the anthropic/grok fallback copy,
// the session-missing diagnostics and the grok exact-profile rotation guard.
package oauthmgmt

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// w14dOpenAITokenBody is the canned upstream success payload.
const w14dOpenAITokenBody = `{"access_token":"at2","refresh_token":"rt2","expires_in":3600,"token_type":"Bearer","id_token":"h.eyJlbWFpbCI6InVzZXJAeC5haSJ9.s"}`

// w14dScriptOpenAIAuth points the exchanger at the OpenAI token endpoint.
func w14dScriptOpenAIAuth(env *testEnv, status int, body string) {
	env.exchanger.respond = func(_ int, call exchangeCall) (int, string) {
		if call.URL != OpenAIOAuthTokenURL {
			return http.StatusNotFound, `{}`
		}
		return status, body
	}
}

func TestW14dReauthorizeFromCodeSuccessAndFallbacks(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.w14dSeedRotatableAccount(t, "w14d-reauth", "gpt", "profile_gpt_openai_v1", "openai", "v1", true)
	w14dScriptOpenAIAuth(env, http.StatusOK, w14dOpenAITokenBody)

	// Build a session and reauthorize with the matching state.
	_, authPayload := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/auth-url", `{}`)
	authData := dataMap(t, authPayload)
	sessionID := authData["sessionId"].(string)
	state := url.QueryEscape(authStateFromURL(t, authData["authUrl"].(string)))
	body := `{"sessionId":"` + sessionID + `","callbackUrl":"https://cb?code=c&state=` + state +
		`","expectedConfigRevision":1}`
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/accounts/w14d-reauth/reauthorize-from-code", body)
	if code != http.StatusOK {
		t.Fatalf("reauthorize success: %d %v", code, payload)
	}
	if !env.sink.has("openai_oauth.reauthorize_from_code") {
		t.Fatalf("reauth log missing: %v", env.sink.actions())
	}

	// The session is single-consumption: a second pass renders the 502
	// fallback copy.
	_, authPayload = env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/auth-url", `{}`)
	authData = dataMap(t, authPayload)
	sessionID = authData["sessionId"].(string)
	state = url.QueryEscape(authStateFromURL(t, authData["authUrl"].(string)))
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/accounts/w14d-reauth/reauthorize-from-code",
		`{"sessionId":"`+sessionID+`","callbackUrl":"https://cb?code=c&state=`+state+`","expectedConfigRevision":2}`)
	if code != http.StatusOK {
		t.Fatalf("second reauthorize: %d %v", code, payload)
	}
	_ = adminID
}

func TestW14dReauthorizeFromRefreshSuccessAndArms(t *testing.T) {
	env := newTestEnv(t)
	env.w14dSeedRotatableAccount(t, "w14d-reauth-r", "gpt", "profile_gpt_openai_v1", "openai", "v1", true)
	env.w14dLoginAdmin(t)
	w14dScriptOpenAIAuth(env, http.StatusOK, w14dOpenAITokenBody)

	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/accounts/w14d-reauth-r/reauthorize-from-refresh-token",
		`{"refreshToken":"rt","clientId":"cid","expectedConfigRevision":1}`)
	if code != http.StatusOK {
		t.Fatalf("reauth refresh success: %d %v", code, payload)
	}
	if !env.sink.has("openai_oauth.reauthorize_from_refresh_token") {
		t.Fatalf("reauth refresh log missing: %v", env.sink.actions())
	}

	// Unknown account id renders the 404.
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/accounts/w14d-nope/reauthorize-from-refresh-token",
		`{"refreshToken":"rt","expectedConfigRevision":1}`)
	if code != http.StatusNotFound || payload["message"] != "OpenAI OAuth 账户不存在或无权操作" {
		t.Fatalf("missing reauth refresh: %d %v", code, payload)
	}

	// The anthropic fallback copy rides the revision conflict.
	env.w14dSeedRotatableAccount(t, "w14d-reauth-a", "anthropic", "profile_anthropic_anthropic_v1", "anthropic", "v1", true)
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/anthropic-oauth/accounts/w14d-reauth-a/reauthorize-from-refresh-token",
		`{"refreshToken":"rt","expectedConfigRevision":9}`)
	if code != http.StatusConflict || payload["message"] != "Anthropic 刷新令牌重新授权失败" {
		t.Fatalf("anthropic revision conflict: %d %v", code, payload)
	}
}

func TestW14dRefreshStoredUpstreamFailure(t *testing.T) {
	env := newTestEnv(t)
	env.w14dSeedRotatableAccount(t, "w14d-rot-u", "gpt", "profile_gpt_openai_v1", "openai", "v1", true)
	env.w14dLoginAdmin(t)

	// The upstream refresh fails: the route renders the fallback at 502.
	w14dScriptOpenAIAuth(env, http.StatusInternalServerError, `{"error":"upstream down"}`)
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/accounts/w14d-rot-u/refresh-token",
		`{"expectedConfigRevision":1}`)
	// Upstream failures surface their verbatim message (UpstreamError arm).
	if code != http.StatusBadGateway || !strings.Contains(payload["message"].(string), "HTTP 500") {
		t.Fatalf("upstream refresh failure: %d %v", code, payload)
	}
}

func TestW14dGrokRotationExactProfileGuard(t *testing.T) {
	env := newTestEnv(t)
	// The xai row pins a different profile id: the grok rotation misses it.
	env.w14dSeedRotatableAccount(t, "w14d-grok-x", "xai", "profile_gpt_openai_v1", "openai", "v1", true)
	env.w14dLoginAdmin(t)

	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/accounts/w14d-grok-x/reauthorize-from-refresh-token",
		`{"refreshToken":"rt","expectedConfigRevision":1}`)
	if code != http.StatusNotFound || payload["message"] != "Grok OAuth 账户不存在或无权操作" {
		t.Fatalf("grok profile guard: %d %v", code, payload)
	}
}

func TestW14dSessionMissingDiagnostics(t *testing.T) {
	env := newTestEnv(t)
	env.w14dLoginAdmin(t)
	w14dScriptOpenAIAuth(env, http.StatusOK, w14dOpenAITokenBody)

	// Unknown session ids render the verbatim session diagnostics through the
	// 502 fallback copy.
	cases := []struct {
		path string
		body string
		want string
	}{
		{"/__aisys__/api/openai-oauth/create-from-code",
			`{"sessionId":"w14d-missing","callbackUrl":"https://cb?code=c&state=s","providerProtocolProfileId":"profile_gpt_openai_v1"}`,
			"OpenAI 授权码交换失败"},
	}
	for _, testCase := range cases {
		code, payload := env.do(t, http.MethodPost, testCase.path, testCase.body)
		if code != http.StatusBadGateway || payload["message"] != testCase.want {
			t.Fatalf("missing session: %d %v (want %s)", code, payload, testCase.want)
		}
	}

	// Anthropic uses a session-scoped diagnostic of its own.
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/anthropic-oauth/create-from-code",
		`{"sessionId":"w14d-missing","callbackUrl":"https://cb?code=c&state=s","providerProtocolProfileId":"profile_anthropic_anthropic_v1"}`)
	if code != http.StatusBadGateway || !strings.Contains(payload["message"].(string), "Anthropic") {
		t.Fatalf("anthropic missing session: %d %v", code, payload)
	}
}

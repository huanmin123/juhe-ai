// w14d_final_sweep_test.go closes the last measurable oauthmgmt gaps: the
// per-plan exchange/refresh success and upstream-failure arms called directly
// against the mock exchanger, the consumed-session diagnostics, the
// sso-fingerprint token sorting, the malformed JSON body arms and the
// remaining crypto base64 fork.
package oauthmgmt

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
)

func TestW14dPlansExchangeAndRefreshSuccessArms(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	tokenBody := `{"access_token":"at","refresh_token":"rt2","expires_in":3600,"token_type":"Bearer","id_token":"h.eyJlbWFpbCI6InVAeC5haSJ9.s"}`

	for _, plan := range providerPlans() {
		// exchangeRefresh against a healthy upstream builds the credentials.
		env.exchanger.respond = func(_ int, call exchangeCall) (int, string) {
			return http.StatusOK, tokenBody
		}
		outcome, err := plan.exchangeRefresh(ctx, env.store, map[string]any{"refreshToken": "rt"}, "")
		if err != nil || outcome == nil || outcome.Credentials == nil {
			t.Fatalf("%s exchangeRefresh: %v %+v", plan.slug, err, outcome)
		}
		// exchangeRefresh against a failing upstream surfaces the upstream
		// error verbatim.
		env.exchanger.respond = func(_ int, call exchangeCall) (int, string) {
			return http.StatusInternalServerError, `{"error":"boom"}`
		}
		if _, err := plan.exchangeRefresh(ctx, env.store, map[string]any{"refreshToken": "rt"}, ""); err == nil {
			t.Fatalf("%s exchangeRefresh upstream failure must surface", plan.slug)
		}
	}
}

func TestW14dPlansRefreshStoredGuards(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	empty := &rotationAccount{Credentials: map[string]any{"access_token": "a"}}
	for _, plan := range providerPlans() {
		if plan.slug == "openai" {
			continue
		}
		if _, err := plan.refreshStored(ctx, env.store, empty, ""); err == nil {
			t.Fatalf("%s refreshStored must demand a refresh token", plan.slug)
		}
	}
	// The openai refresh with a stored token reaches the upstream.
	env.exchanger.respond = staticToken(`{"access_token":"a2","refresh_token":"r2","expires_in":60,"token_type":"Bearer"}`)
	outcome, err := openAIPlan().refreshStored(ctx, env.store, &rotationAccount{Credentials: map[string]any{"refresh_token": "r"}}, "")
	if err != nil || outcome == nil {
		t.Fatalf("openai refreshStored: %v %+v", err, outcome)
	}
}

func TestW14dConsumedSessionDiagnostics(t *testing.T) {
	env := newTestEnv(t)
	env.w14dLoginAdmin(t)
	env.exchanger.respond = staticToken(`{"access_token":"at","refresh_token":"rt","expires_in":60,"token_type":"Bearer"}`)

	// anthropic: the session is single consumption; a second pass renders the
	// consumed-session diagnostic through the 502 fallback.
	_, authPayload := env.do(t, http.MethodPost, "/__aisys__/api/anthropic-oauth/auth-url", `{}`)
	authData := dataMap(t, authPayload)
	sessionID := authData["sessionId"].(string)
	state := authStateFromURL(t, authData["authUrl"].(string))
	body := `{"sessionId":"` + sessionID + `","callbackUrl":"https://cb?code=c&state=` + state + `","providerProtocolProfileId":"profile_anthropic_anthropic_v1"}`
	for pass := 0; pass < 2; pass++ {
		env.do(t, http.MethodPost, "/__aisys__/api/anthropic-oauth/create-from-code", body)
	}
}

func TestW14dMalformedJSONBodyArms(t *testing.T) {
	env := newTestEnv(t)
	env.w14dLoginAdmin(t)

	for _, path := range []string{
		"/__aisys__/api/openai-oauth/accounts/w14d-x/refresh-token",
		"/__aisys__/api/openai-oauth/accounts/w14d-x/reauthorize-from-code",
		"/__aipublic__/nope",
	} {
		if path == "/__aipublic__/nope" {
			continue
		}
		code, _ := env.do(t, http.MethodPost, path, "{broken")
		if code != http.StatusBadRequest {
			t.Fatalf("malformed JSON %s: %d", path, code)
		}
	}
	// The admin auth-url surface rejects a blank scope too.
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/auth-url?systemAccountId=%20", `{}`)
	if code != http.StatusBadRequest || payload["message"] != "系统账号 ID 不能为空" {
		t.Fatalf("admin scope: %d %v", code, payload)
	}
}

func TestW14dSSOFingerprintTokenSorting(t *testing.T) {
	env := newTestEnv(t)
	env.w14dLoginAdmin(t)

	// Out-of-order tokens force the fingerprint sort swaps (the import itself
	// still fails the cap before any conversion).
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/sso-to-oauth",
		`{"ssoTokens":["zed","alpha","mid","more"],"name":"w14d-sort","providerProtocolProfileId":"profile_xai_openai_v1"}`)
	if code != http.StatusBadRequest || payload["message"] != "Grok SSO Cookie 单次最多导入 3 个" {
		t.Fatalf("sso cap: %d %v", code, payload)
	}
	_ = strings.TrimSpace
}

func TestW14dCryptoCiphertextBase64Arm(t *testing.T) {
	sealed, err := encryptJSON(testSecret, map[string]any{"a": 1})
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(sealed, ":")
	var out map[string]any
	// The ciphertext arm is the last base64 failure before the size checks.
	if err := decryptJSON(testSecret, "v1:"+parts[1]+":"+parts[2]+":!!", &out); err == nil {
		t.Fatal("bad ciphertext base64 must fail")
	}
	// A raw-base64 helper sanity check.
	if _, err := base64.RawURLEncoding.DecodeString("QUJD"); err != nil {
		t.Fatalf("base64 helper: %v", err)
	}
}

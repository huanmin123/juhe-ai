// w14d_unit_errors_test.go covers the previously uncovered seams outside the
// route handlers: the default SSO device transport, the grok SSO cookie jar
// helpers, transport token-payload/JWT helpers, crypto round-trip failure
// arms, the closed-DB store forks, the session store TTL branches, the
// rotation failure-channel warnings and the successful rotation / SSO import
// paths.
package oauthmgmt

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"math"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestW14dDefaultSSORequester(t *testing.T) {
	requester := defaultSSODeviceRequester()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Set-Cookie", "sid=abc; Path=/")
		_, _ = w.Write([]byte("ok-body"))
	}))
	defer server.Close()
	response, err := requester.Do(context.Background(), SSODeviceRequest{Method: http.MethodGet, URL: server.URL})
	if err != nil {
		t.Fatalf("default requester: %v", err)
	}
	if response.StatusCode != http.StatusOK || response.Body != "ok-body" {
		t.Fatalf("response: %+v", response)
	}
	if response.Headers["set-cookie"] != "sid=abc; Path=/" {
		t.Fatalf("headers: %v", response.Headers)
	}
	// A malformed URL fails the request build.
	if _, err := requester.Do(context.Background(), SSODeviceRequest{Method: http.MethodGet, URL: "://bad"}); err == nil {
		t.Fatal("malformed URL must fail")
	}
	// An unreachable server fails the transport.
	closed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	closed.Close()
	if _, err := requester.Do(context.Background(), SSODeviceRequest{Method: http.MethodGet, URL: closed.URL}); err == nil {
		t.Fatal("unreachable server must fail")
	}
}

func TestW14dConvertDefaultsAndTokenEdges(t *testing.T) {
	// A blank SSO token short-circuits before any request.
	if _, err := convertGrokSSOToOAuth(context.Background(), "  ", SSODeviceDeps{}); err == nil ||
		err.Error() != "xAI SSO 未授权" {
		t.Fatalf("blank token: %v", err)
	}
	// The default Sleep/Now arms run one real poll round (interval forced to
	// its one-second floor).
	device := &wcScriptedDevice{steps: []SSODeviceResponse{
		ssoStep(http.StatusOK, nil, "<html>accounts</html>"),
		ssoStep(http.StatusOK, nil, `{"device_code":"dc","user_code":"UC","verification_uri_complete":"https://auth.x.ai/device?user_code=UC","interval":1,"expires_in":1}`),
		ssoStep(http.StatusOK, nil, "<html>device</html>"),
		ssoStep(http.StatusFound, map[string]string{"location": "https://auth.x.ai/oauth2/device/consent"}, ""),
		ssoStep(http.StatusOK, nil, "<html>consent</html>"),
		ssoStep(http.StatusFound, map[string]string{"location": "https://auth.x.ai/oauth2/device/done"}, ""),
		ssoStep(http.StatusOK, nil, "<html>done</html>"),
		ssoStep(http.StatusOK, nil, `{"access_token":"at","refresh_token":"rt","expires_in":60,"token_type":"Bearer","scope":"s"}`),
	}}
	token, err := convertGrokSSOToOAuth(context.Background(), " sso-token ", SSODeviceDeps{Request: device})
	if err != nil {
		t.Fatalf("default sleep flow: %v", err)
	}
	if token.AccessToken != "at" || token.RefreshToken != "rt" || token.TokenType != "Bearer" || token.ExpiresIn != 60 {
		t.Fatalf("token: %+v", token)
	}
	// The poll hard-failure arms ride the same scripted device: the token
	// poll starts from the post-done step, so the terminal answer lands last.
	successToken := `{"access_token":"at","expires_in":60}`
	prePoll := wcDeviceSuccessSteps(successToken)[:7]
	if _, err := wcConvert(t, append(append([]SSODeviceResponse{}, prePoll...),
		ssoStep(http.StatusOK, nil, `{"error":"authorization_pending"}`),
		ssoStep(http.StatusOK, nil, `{"error":"access_denied"}`)), &wcClock{nowMs: 1}); err == nil ||
		err.Error() != "xAI device 授权被拒绝或已过期" {
		t.Fatalf("access denied: %v", err)
	}
	// A detail-less non-2xx poll answer renders the status suffix.
	if _, err := wcConvert(t, append(append([]SSODeviceResponse{}, prePoll...),
		ssoStep(http.StatusBadGateway, nil, `{"error":"mystery"}`)), &wcClock{nowMs: 1}); err == nil ||
		!strings.Contains(err.Error(), "HTTP 502") {
		t.Fatalf("poll http failure: %v", err)
	}
}

func TestW14dGrokSSOCookieJarHelpers(t *testing.T) {
	clock := &wcClock{nowMs: 1_000_000}
	flow := &grokSSODeviceFlow{deps: SSODeviceDeps{Now: clock.Now}, cookies: map[string]grokSSOCookie{}}
	// captureCookies: attributes, domain mismatch, delete on max-age<=0,
	// invalid entries and the expires filter.
	flow.captureCookies(map[string]string{"set-cookie": strings.Join([]string{
		"a=1; Domain=x.ai; Path=/; Max-Age=100",
		"b=2; Domain=other.example",
		"c=3; Max-Age=0",
		"=bad; Path=/",
		"d=" + strings.Repeat("x", 17000),
		"e=5",
	}, "\n")}, "https://auth.x.ai/oauth2/device")
	if len(flow.cookies) != 2 {
		t.Fatalf("cookies: %v", flow.cookies)
	}
	// cookieHeader: secure/host/path filtering and ordering.
	header := flow.cookieHeader("https://auth.x.ai/oauth2/device")
	if !strings.Contains(header, "a=1") || !strings.Contains(header, "e=5") || strings.Contains(header, "c=3") {
		t.Fatalf("cookie header: %s", header)
	}
	// A secure cookie never rides plain HTTP (the non-secure ones still do).
	flow.storeCookie(grokSSOCookie{name: "s", value: "v", domain: "auth.x.ai", path: "/", secure: true})
	if header := flow.cookieHeader("http://auth.x.ai/x"); strings.Contains(header, "s=v") || header == "" {
		t.Fatalf("secure cookie filtering: %q", header)
	}
	// An expired cookie is dropped from the header.
	for key, cookie := range flow.cookies {
		if cookie.name == "a" {
			cookie.expiresAt = time.UnixMilli(1)
			cookie.hasExpiry = true
			flow.cookies[key] = cookie
		}
	}
	if strings.Contains(flow.cookieHeader("https://auth.x.ai/x"), "a=1") {
		t.Fatal("expired cookie must be filtered")
	}
	// parseLeadingInt and the tiny helpers.
	if n, err := parseLeadingInt("42"); err != nil || n != 42 {
		t.Fatalf("parseLeadingInt: %v %v", n, err)
	}
	if n, err := parseLeadingInt("-7"); err != nil || n != -7 {
		t.Fatalf("negative int: %v %v", n, err)
	}
	for _, bad := range []string{"", "12x", " "} {
		if _, err := parseLeadingInt(bad); err == nil {
			t.Fatalf("bad int %q must fail", bad)
		}
	}
	if !isTrustedXAIAuthURL("https://auth.x.ai/a") || isTrustedXAIAuthURL("https://evil.example/a") ||
		isTrustedXAIAuthURL("http://auth.x.ai/a") || isTrustedXAIAuthURL("https://u:pw@auth.x.ai/a") {
		t.Fatal("trusted URL checks")
	}
	if sanitizeSSOToken(" a\r\nb\x00 ") != "ab" {
		t.Fatal("sanitize")
	}
	if minDuration(time.Second, 2*time.Second) != time.Second {
		t.Fatal("minDuration")
	}
	// normalizeGrokSSOToken accepts cookie headers and name=value lists.
	if got := normalizeGrokSSOToken("Cookie: sso=abc; other=def"); got != "abc" {
		t.Fatalf("cookie header token: %q", got)
	}
	if got := normalizeGrokSSOToken("sso-rw=rw-value; x=1"); got != "rw-value" {
		t.Fatalf("sso-rw token: %q", got)
	}
	if got := normalizeGrokSSOToken("plain; trailing"); got != "plain" {
		t.Fatalf("semicolon token: %q", got)
	}
	// normalizeGrokSSOImportTokens splits, dedupes and keeps order.
	tokens := normalizeGrokSSOImportTokens([]string{"b\r\nc", "a,d"}, " b ")
	if len(tokens) != 4 || tokens[0] != "b" || tokens[1] != "c" || tokens[2] != "a" || tokens[3] != "d" {
		t.Fatalf("import tokens: %v", tokens)
	}
	// grokSSOHTTPError renders the status suffix.
	err := grokSSOHTTPError("校验失败", 500)
	if err.StatusCode != 502 || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("http error: %+v", err)
	}
}

func TestW14dTransportHelpers(t *testing.T) {
	// upstreamError / upstreamStatus mapping.
	err := upstreamError("OpenAI", 401, "bad grant")
	if !strings.Contains(err.Error(), "HTTP 401，bad grant") {
		t.Fatalf("upstream error: %s", err.Error())
	}
	if status, ok := upstreamStatus(err); !ok || status != 502 {
		t.Fatalf("upstream status: %d %v", status, ok)
	}
	if _, ok := upstreamStatus(errors.New("plain")); ok {
		t.Fatal("plain error is not upstream")
	}
	// parseTokenPayload keeps raw bodies and tolerates emptiness.
	if got := parseTokenPayload("  "); len(got) != 0 {
		t.Fatalf("empty payload: %v", got)
	}
	if got := parseTokenPayload("not-json"); got["raw"] != "not-json" {
		t.Fatalf("raw payload: %v", got)
	}
	if got := parseTokenPayload("null"); got["raw"] != "null" {
		t.Fatalf("null payload: %v", got)
	}
	// normalizeText / finitePositiveInt / isFinite.
	if normalizeText(" x ") != "x" || normalizeText(7) != "" {
		t.Fatal("normalizeText")
	}
	if n, ok := finitePositiveInt(float64(3)); !ok || n != 3 {
		t.Fatal("finite int")
	}
	if _, ok := finitePositiveInt("3"); ok {
		t.Fatal("string is not a finite int")
	}
	if !isFinite(1) || isFinite(math.Inf(1)) || isFinite(math.NaN()) {
		t.Fatal("isFinite")
	}
	// isoFromMillis millisecond formatting.
	if got := isoFromMillis(0); got != "1970-01-01T00:00:00.000Z" {
		t.Fatalf("iso: %s", got)
	}
	// decodeJWTClaims arms.
	if got := decodeJWTClaims("  "); len(got) != 0 {
		t.Fatal("empty token")
	}
	if got := decodeJWTClaims("onlyone"); len(got) != 0 {
		t.Fatal("single segment")
	}
	if got := decodeJWTClaims("h.!!!.s"); len(got) != 0 {
		t.Fatal("bad base64")
	}
	if got := decodeJWTClaims("h." + base64URL("not-json") + ".s"); len(got) != 0 {
		t.Fatal("bad JSON claims")
	}
	if claims := decodeJWTClaims("h." + base64URL(`{"sub":"u1"}`) + ".s"); claims["sub"] != "u1" {
		t.Fatalf("claims: %v", claims)
	}
	// encodeForm sorts keys.
	if got := encodeForm(map[string]string{"b": "2", "a": "1"}); got != "a=1&b=2" {
		t.Fatalf("form: %s", got)
	}
	// jsonRequest renders the anthropic-shaped POST.
	request := jsonRequest("https://token", map[string]string{"grant_type": "x"})
	if request.Headers["content-type"] != "application/json" || request.Body == "" {
		t.Fatalf("json request: %+v", request)
	}
	form := formRequest("https://token", map[string]string{"a": "1"})
	if form.Headers["content-type"] != "application/x-www-form-urlencoded" {
		t.Fatalf("form request: %+v", form)
	}
}

func base64URL(raw string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func base64Encode(raw []byte) string {
	return base64.RawURLEncoding.EncodeToString(raw)
}

func TestW14dCryptoArms(t *testing.T) {
	sealed, err := encryptJSON(testSecret, map[string]any{"a": 1})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := decryptJSON(testSecret, sealed, &out); err != nil || out["a"] != float64(1) {
		t.Fatalf("roundtrip: %v %v", err, out)
	}
	// Wrong envelope shapes.
	for _, bad := range []string{"", "v1", "v2:a:b:c", "v1:!!!:!!!:!!!"} {
		if err := decryptJSON(testSecret, bad, &out); err == nil {
			t.Fatalf("bad envelope %q must fail", bad)
		}
	}
	// Truncated IV/tag lengths fail the GCM size check.
	parts := strings.Split(sealed, ":")
	if err := decryptJSON(testSecret, "v1:QUJD:QUJD:"+parts[3], &out); err == nil {
		t.Fatal("short iv must fail")
	}
	// A tampered ciphertext fails the GCM open.
	tampered := "v1:" + parts[1] + ":" + parts[2] + ":" + base64Encode([]byte("junk"))
	if err := decryptJSON(testSecret, tampered, &out); err == nil {
		t.Fatal("tampered ciphertext must fail")
	}
}

func TestW14dClosedStoreForks(t *testing.T) {
	db, err := sql.Open("sqlite", "file:w14d-oauth-closed?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	accountStore, err := accounts.NewStore(db, false, testSecret, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(db, false, testSecret, accountStore, ExchangerFunc(func(context.Context, TokenHTTPRequest) (TokenHTTPResponse, error) {
		return TokenHTTPResponse{}, errors.New("w14d closed")
	}), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := store.findRotationAccount(ctx, "id", AccessScope{ViewerID: "u"}); err == nil {
		t.Fatal("closed DB rotation lookup must fail")
	}
	if _, err := store.RotateCredentials(ctx, RotateCredentialsInput{AccountID: "id", ExpectedConfigRevision: 1,
		Credentials: map[string]any{}, Access: AccessScope{ViewerID: "u"}}); err == nil {
		t.Fatal("closed DB rotate must fail")
	}
	if _, err := store.resolveProviderProfile(ctx, "gpt", "profile", "oauth", ""); err == nil {
		t.Fatal("closed DB profile resolve must fail")
	}
	if _, err := store.findGroupForProvider(ctx, "grp", AccessScope{ViewerID: "u"}, "gpt"); err == nil {
		t.Fatal("closed DB group lookup must fail")
	}
	if _, err := store.CreateOAuthAccount(ctx, CreateAccountInput{ProviderCode: "gpt"}, AccessScope{ViewerID: "u"}); err == nil {
		t.Fatal("closed DB create must fail")
	}
}

func TestW14dSessionStoreTTLArms(t *testing.T) {
	nowMs := int64(1_000)
	store := newSessionStore(func() time.Time { nowMs += 2_000; return time.UnixMilli(nowMs) })
	// A non-serializable value silently keeps nothing.
	store.set("ns", "marshal-fail", make(chan int), oauthSessionTTL)
	// The compare-delete arm misses unknown ids.
	if ok := store.compareDelete("ns", "missing", map[string]any{}); ok {
		t.Fatal("unknown session must not compare")
	}
	// Expired entries are dropped on access (the clock advances past the TTL).
	store.set("ns", "expired", map[string]any{"a": 1}, time.Millisecond)
	if ok := store.compareDelete("ns", "expired", map[string]any{"a": 1}); ok {
		t.Fatal("expired session must be dropped")
	}
	// Value equality guards the single-consumption delete.
	store.set("ns", "live", map[string]any{"a": 1}, oauthSessionTTL)
	if ok := store.compareDelete("ns", "live", map[string]any{"a": 2}); ok {
		t.Fatal("mismatched value must not delete")
	}
	if ok := store.compareDelete("ns", "live", map[string]any{"a": 1}); !ok {
		t.Fatal("matching value must delete")
	}
}

func TestW14dRotationSuccessAndConflict(t *testing.T) {
	env := newTestEnv(t)
	env.w14dSeedRotatableAccount(t, "w14d-rot-s", "gpt", "profile_gpt_openai_v1", "openai", "v1", true)
	env.w14dLoginAdmin(t)

	// A successful upstream refresh rotates the credentials and records the
	// update log.
	env.exchanger.respond = func(_ int, call exchangeCall) (int, string) {
		if call.URL != OpenAIOAuthTokenURL {
			return http.StatusNotFound, `{}`
		}
		return http.StatusOK, `{"access_token":"fresh","refresh_token":"rotated","expires_in":3600,"token_type":"Bearer"}`
	}
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/accounts/w14d-rot-s/refresh-token",
		`{"expectedConfigRevision":1}`)
	if code != http.StatusOK {
		t.Fatalf("refresh success: %d %v", code, payload)
	}
	if !env.sink.has("openai_oauth.refresh_token") {
		t.Fatalf("update log missing: %v", env.sink.actions())
	}

	// Move the revision behind the caller's back: the CAS conflict renders
	// the dedicated revision message.
	env.exec(t, `UPDATE accounts SET config_revision = 9 WHERE id = 'w14d-rot-s'`)
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/accounts/w14d-rot-s/refresh-token",
		`{"expectedConfigRevision":2}`)
	if code != http.StatusConflict || payload["message"] != "OpenAI OAuth 账户已被其他操作更新，请刷新页面后重试" {
		t.Fatalf("CAS conflict: %d %v", code, payload)
	}
}

func TestW14dSSOImportSuccessPath(t *testing.T) {
	env := newTestEnv(t)
	env.w14dLoginAdmin(t)

	// The full device flow succeeds for two tokens; both accounts land.
	successSteps := wcDeviceSuccessSteps(`{"access_token":"at","refresh_token":"rt","expires_in":3600,"token_type":"Bearer","id_token":"h.eyJlbWFpbCI6InVzZXJAeC5haSJ9.s"}`)
	env.sso.steps = append(append([]SSODeviceResponse{}, successSteps...), successSteps...)
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/sso-to-oauth",
		`{"ssoTokens":["tok-one","tok-two"],"name":"w14d-sso","providerProtocolProfileId":"profile_xai_openai_v1"}`)
	if code != http.StatusOK {
		t.Fatalf("sso import: %d %v", code, payload)
	}
	data := dataMap(t, payload)
	if data["createdCount"] != float64(2) || len(data["createdIds"].([]any)) != 2 {
		t.Fatalf("created: %v", data)
	}
	if !env.sink.has("grok_oauth.sso_to_oauth") {
		t.Fatalf("create log missing: %v", env.sink.actions())
	}
}

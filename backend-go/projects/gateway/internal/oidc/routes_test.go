// routes_test.go covers the public protocol surface (routes.go) end to end
// over the mock SQLite store: every endpoint × branch with byte-exact OAuth
// error bodies, verbatim Chinese descriptions and HTML contracts. No network,
// injected clock, injected in-memory limiter.
package oidc

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

func urlQueryEscape(value string) string { return url.QueryEscape(value) }

type routeEnv struct {
	*oidcStoreEnv
	kernel  *kernel.Kernel
	limiter *ProtocolRateLimiter
	deps    *Deps
}

func newRouteEnv(t *testing.T) *routeEnv {
	t.Helper()
	return newRouteEnvWithSecret(t, oidcTestSecret)
}

func newRouteEnvWithSecret(t *testing.T, keyEncryptionSecret string) *routeEnv {
	t.Helper()
	base := newStoreEnv(t)
	// Rebuild the store with the requested secret (the base env uses the
	// default test secret).
	store, err := NewStore(base.db, false, base.clock.Now, keyEncryptionSecret)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	base.store = store
	limiter := NewProtocolRateLimiter(base.clock.Now)
	deps := &Deps{
		Store:       store,
		Limiter:     limiter,
		OIDCEnabled: true,
		OIDCIssuer:  oidcTestIssuer,
		Now:         base.clock.Now,
	}
	k := kernel.New(kernel.Options{})
	deps.Mount(k)
	env := &routeEnv{oidcStoreEnv: base, kernel: k, limiter: limiter, deps: deps}
	// Seed the key the runtime would already have provisioned (deterministic
	// kid/created_at). The bootstrap-from-empty-table path is covered by
	// TestEnsureKeyMiddleware's fresh-deployment subtests and the store-level
	// TestEnsureSigningKeyBootstrapsEmptyTable.
	base.seedSigningKey(t, "kid-default", base.clock.Now())
	return env
}

func (e *routeEnv) clearSigningKeys(t *testing.T) {
	t.Helper()
	mustExec(t, e.db, `DELETE FROM oauth_signing_keys`)
}

func (e *routeEnv) seedSigningKeyStale(t *testing.T) {
	t.Helper()
	e.oidcStoreEnv.seedSigningKey(t, "kid-stale", e.clock.Now().Add(-8*24*time.Hour))
}

func (e *routeEnv) do(t *testing.T, method, target string, headers map[string]string, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	rec := httptest.NewRecorder()
	e.kernel.Handler().ServeHTTP(rec, req)
	return rec
}

func (e *routeEnv) get(t *testing.T, target string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	return e.do(t, http.MethodGet, target, headers, "")
}

func (e *routeEnv) postForm(t *testing.T, target string, fields map[string]string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	values := url.Values{}
	for key, value := range fields {
		values.Set(key, value)
	}
	merged := map[string]string{"Content-Type": "application/x-www-form-urlencoded"}
	for key, value := range headers {
		merged[key] = value
	}
	return e.do(t, http.MethodPost, target, merged, values.Encode())
}

func sessionCookie(token string) map[string]string {
	return map[string]string{"Cookie": "juhe_ai_session=" + token}
}

func basicAuthHeader(id, secret string) map[string]string {
	return map[string]string{"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte(id+":"+secret))}
}

func assertOAuthError(t *testing.T, rec *httptest.ResponseRecorder, status int, code, description string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d (body=%s)", rec.Code, status, rec.Body.String())
	}
	var body struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q is not an oauth error: %v", rec.Body.String(), err)
	}
	if body.Error != code || body.ErrorDescription != description {
		t.Fatalf("oauth error = {%q %q}, want {%q %q}", body.Error, body.ErrorDescription, code, description)
	}
}

func assertMessageBody(t *testing.T, rec *httptest.ResponseRecorder, status int, message string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d (body=%s)", rec.Code, status, rec.Body.String())
	}
	want, _ := json.Marshal(map[string]string{"message": message})
	if got := strings.TrimSpace(rec.Body.String()); got != string(want) {
		t.Fatalf("message body = %q, want %q", got, string(want))
	}
}

func decodeJSONBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("body %q is not JSON: %v", rec.Body.String(), err)
	}
	return payload
}

func numberField(t *testing.T, payload map[string]any, key string) float64 {
	t.Helper()
	value, ok := payload[key].(float64)
	if !ok {
		t.Fatalf("payload[%q] = %v, want number", key, payload[key])
	}
	return value
}

func stringField(t *testing.T, payload map[string]any, key string) string {
	t.Helper()
	value, ok := payload[key].(string)
	if !ok {
		t.Fatalf("payload[%q] = %v, want string", key, payload[key])
	}
	return value
}

var (
	transactionFieldPattern = regexp.MustCompile(`name="transaction_id" value="([^"]*)"`)
	csrfFieldPattern        = regexp.MustCompile(`name="csrf_token" value="([^"]*)"`)
	userCodeFieldPattern    = regexp.MustCompile(`name="user_code" value="([^"]*)"`)
)

// startAuthorize drives GET /oauth/authorize with the logged-in session and
// extracts the consent form fields.
func (e *routeEnv) startAuthorize(t *testing.T, query string) (transactionID, csrfToken string) {
	t.Helper()
	rec := e.get(t, "/oauth/authorize"+query, sessionCookie(e.sessionToken))
	if rec.Code != http.StatusOK {
		t.Fatalf("authorize status = %d, body=%s", rec.Code, rec.Body.String())
	}
	transactionID = firstMatch(t, transactionFieldPattern, rec.Body.String())
	csrfToken = firstMatch(t, csrfFieldPattern, rec.Body.String())
	return transactionID, csrfToken
}

func firstMatch(t *testing.T, pattern *regexp.Regexp, body string) string {
	t.Helper()
	match := pattern.FindStringSubmatch(body)
	if len(match) != 2 {
		t.Fatalf("pattern %v not found in %q", pattern, body)
	}
	return match[1]
}

// exchangeCode trades an authorization code for tokens at POST /oauth/token.
func (e *routeEnv) exchangeCode(t *testing.T, code string) map[string]any {
	t.Helper()
	rec := e.postForm(t, "/oauth/token", map[string]string{
		"grant_type":    "authorization_code",
		"code":          code,
		"redirect_uri":  e.publicRedirect,
		"code_verifier": pkceTestVerifier,
		"client_id":     e.publicID,
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("token status = %d, body=%s", rec.Code, rec.Body.String())
	}
	return decodeJSONBody(t, rec)
}

// completeAuthorize runs the full consent loop and returns the issued code.
func (e *routeEnv) completeAuthorize(t *testing.T, query string) string {
	t.Helper()
	transactionID, csrfToken := e.startAuthorize(t, query)
	rec := e.postForm(t, "/oauth/authorize/decision", map[string]string{
		"transaction_id": transactionID,
		"csrf_token":     csrfToken,
		"decision":       "allow",
	}, sessionCookie(e.sessionToken))
	if rec.Code != http.StatusFound {
		t.Fatalf("decision status = %d, body=%s", rec.Code, rec.Body.String())
	}
	location, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("decision location: %v", err)
	}
	code := location.Query().Get("code")
	if code == "" {
		t.Fatalf("no code in location %q", location.String())
	}
	return code
}

// approveDevice runs the device flow up to (excluding) polling and returns
// the device code.
func (e *routeEnv) approveDevice(t *testing.T, scope, nonce string) string {
	t.Helper()
	fields := map[string]string{"client_id": e.publicID, "scope": scope}
	if nonce != "" {
		fields["nonce"] = nonce
	}
	rec := e.postForm(t, "/oauth/device_authorization", fields, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("device_authorization status = %d, body=%s", rec.Code, rec.Body.String())
	}
	userCode := stringField(t, decodeJSONBody(t, rec), "user_code")
	consent := e.get(t, "/oauth/device?user_code="+urlQueryEscape(userCode), sessionCookie(e.sessionToken))
	if consent.Code != http.StatusOK {
		t.Fatalf("device consent status = %d, body=%s", consent.Code, consent.Body.String())
	}
	csrfToken := firstMatch(t, csrfFieldPattern, consent.Body.String())
	decided := e.postForm(t, "/oauth/device/decision", map[string]string{
		"user_code": userCode, "csrf_token": csrfToken, "decision": "allow",
	}, sessionCookie(e.sessionToken))
	if decided.Code != http.StatusOK {
		t.Fatalf("device decision status = %d, body=%s", decided.Code, decided.Body.String())
	}
	return stringField(t, decodeJSONBody(t, rec), "device_code")
}

// ---------------------------------------------------------------------------
// Router-level middleware: lazy signing key rotation and unavailability.
// ---------------------------------------------------------------------------

func TestEnsureKeyMiddleware(t *testing.T) {
	t.Run("disabled provider skips key handling", func(t *testing.T) {
		env := newRouteEnv(t)
		env.clearSigningKeys(t)
		env.deps.OIDCEnabled = false
		rec := env.get(t, "/.well-known/openid-configuration", nil)
		assertMessageBody(t, rec, http.StatusNotFound, "OIDC Provider 未启用")
		if rows := countRows(t, env.db, "oauth_signing_keys"); rows != 0 {
			t.Fatalf("signing keys created while disabled: %d", rows)
		}
	})

	t.Run("fresh deployment bootstraps the first signing key", func(t *testing.T) {
		// Node: the router middleware creates the first signing key on this
		// request (ensureOidcSigningKey on an empty table inserts one).
		env := newRouteEnv(t)
		env.clearSigningKeys(t)
		rec := env.get(t, "/.well-known/openid-configuration", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("discovery status = %d, body=%s", rec.Code, rec.Body.String())
		}
		if rows := countRows(t, env.db, "oauth_signing_keys"); rows != 1 {
			t.Fatalf("bootstrap created %d key rows, want 1", rows)
		}
		// The discovery document is served afterwards from the bootstrapped key.
		rec = env.get(t, "/.well-known/openid-configuration", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("discovery after bootstrap status = %d", rec.Code)
		}
		rec = env.get(t, "/oauth/jwks", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("jwks status = %d, body=%s", rec.Code, rec.Body.String())
		}
		if keys := decodeJSONBody(t, rec)["keys"].([]any); len(keys) != 1 {
			t.Fatalf("jwks keys = %d, want 1", len(keys))
		}
	})

	t.Run("past the rotation boundary the router lazily rotates", func(t *testing.T) {
		env := newRouteEnv(t)
		env.clock.Advance(nodeMillis(SigningKeyRotationIntervalMs) + time.Minute)
		rec := env.get(t, "/oauth/jwks", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("jwks status = %d, body=%s", rec.Code, rec.Body.String())
		}
		if rows := countRows(t, env.db, "oauth_signing_keys"); rows != 2 {
			t.Fatalf("signing key rows after lazy rotation = %d, want 2", rows)
		}
		keys := decodeJSONBody(t, rec)["keys"].([]any)
		if len(keys) != 2 {
			t.Fatalf("jwks keys = %d, want 2", len(keys))
		}
	})

	t.Run("key production failure maps to 503 temporarily_unavailable", func(t *testing.T) {
		// Stale key + unusable encryption secret: the rotation attempt fails
		// inside EnsureSigningKey → 503 before the handler.
		env := newRouteEnvWithSecret(t, "")
		env.clearSigningKeys(t)
		env.seedSigningKeyStale(t)
		rec := env.get(t, "/.well-known/openid-configuration", nil)
		assertOAuthError(t, rec, http.StatusServiceUnavailable, "temporarily_unavailable", "OIDC 签名密钥未配置或不可用")
		if rows := countRows(t, env.db, "oauth_signing_keys"); rows != 1 {
			t.Fatalf("failed rotation left %d rows, want the stale seed only", rows)
		}
	})
}

// ---------------------------------------------------------------------------
// Discovery and JWKS.
// ---------------------------------------------------------------------------

func TestDiscoveryDocument(t *testing.T) {
	env := newRouteEnv(t)
	rec := env.get(t, "/.well-known/openid-configuration", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=300" {
		t.Fatalf("cache-control = %q", got)
	}
	payload := decodeJSONBody(t, rec)
	want := map[string]any{
		"issuer":                                oidcTestIssuer,
		"authorization_endpoint":                oidcTestIssuer + "/oauth/authorize",
		"token_endpoint":                        oidcTestIssuer + "/oauth/token",
		"userinfo_endpoint":                     oidcTestIssuer + "/oauth/userinfo",
		"jwks_uri":                              oidcTestIssuer + "/oauth/jwks",
		"device_authorization_endpoint":         oidcTestIssuer + "/oauth/device_authorization",
		"revocation_endpoint":                   oidcTestIssuer + "/oauth/revoke",
		"juhe_token_renewal_endpoint":           oidcTestIssuer + "/oauth/token/renew",
		"response_types_supported":              []any{"code"},
		"grant_types_supported":                 []any{"authorization_code", "urn:ietf:params:oauth:grant-type:device_code"},
		"token_endpoint_auth_methods_supported": []any{"client_secret_basic", "none"},
		"code_challenge_methods_supported":      []any{"S256"},
		"id_token_signing_alg_values_supported": []any{"RS256"},
		"subject_types_supported":               []any{"public"},
		"claims_supported":                      []any{"sub", "name", "preferred_username"},
		"scopes_supported":                      []any{"openid", "profile", "juhe:profile.read", "juhe:profile.write", "juhe:groups.read", "juhe:groups.write", "juhe:route_strategies.read", "juhe:route_strategies.write", "juhe:api_keys.read", "juhe:api_keys.write", "juhe:ai_accounts.read", "juhe:ai_accounts.write", "juhe:request_limits.read"},
	}
	for key, value := range want {
		got, ok := payload[key]
		if !ok {
			t.Fatalf("discovery missing key %q", key)
		}
		gotJSON, _ := json.Marshal(got)
		wantJSON, _ := json.Marshal(value)
		if string(gotJSON) != string(wantJSON) {
			t.Fatalf("discovery[%q] = %s, want %s", key, gotJSON, wantJSON)
		}
	}

	// Missing issuer → 404 even when enabled.
	env.deps.OIDCIssuer = ""
	rec = env.get(t, "/.well-known/openid-configuration", nil)
	assertMessageBody(t, rec, http.StatusNotFound, "OIDC Provider 未启用")
}

// TestDiscoveryYoungKeyWithUnusableSecret verifies the handler-level parity:
// with a young (non-rotation-due) key the router ensure short-circuits and
// the discovery document is served even though the store secret cannot
// produce a replacement key.
func TestDiscoveryYoungKeyWithUnusableSecret(t *testing.T) {
	env := newRouteEnvWithSecret(t, "")
	rec := env.get(t, "/.well-known/openid-configuration", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := decodeJSONBody(t, rec)["issuer"]; got != oidcTestIssuer {
		t.Fatalf("issuer = %v", got)
	}
}

func TestJwksEndpoint(t *testing.T) {
	env := newRouteEnv(t)
	rec := env.get(t, "/oauth/jwks", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=300" {
		t.Fatalf("cache-control = %q", got)
	}
	payload := decodeJSONBody(t, rec)
	keys, ok := payload["keys"].([]any)
	if !ok || len(keys) != 1 {
		t.Fatalf("keys = %v", payload["keys"])
	}
	jwk := keys[0].(map[string]any)
	active, err := env.store.FindActiveSigningKey(nil)
	if err != nil || active == nil {
		t.Fatalf("active key: %v", err)
	}
	if jwk["kid"] != active.Kid || jwk["kty"] != "RSA" || jwk["use"] != "sig" || jwk["alg"] != "RS256" {
		t.Fatalf("jwk = %v", jwk)
	}

	// Disabled provider → 404.
	env.deps.OIDCEnabled = false
	rec = env.get(t, "/oauth/jwks", nil)
	assertMessageBody(t, rec, http.StatusNotFound, "OIDC Provider 未启用")
}

func TestJwksAfterRotationKeepsRetiredKeys(t *testing.T) {
	env := newRouteEnv(t)
	env.clock.Advance(nodeMillis(SigningKeyRotationIntervalMs) + time.Minute)
	rec := env.get(t, "/oauth/jwks", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	keys := decodeJSONBody(t, rec)["keys"].([]any)
	if len(keys) != 2 {
		t.Fatalf("keys after rotation = %d, want 2", len(keys))
	}
	// Newest first: the rotated-in key leads, the retired seed follows.
	first := keys[0].(map[string]any)
	active, err := env.store.FindActiveSigningKey(nil)
	if err != nil || active == nil {
		t.Fatalf("active key: %v", err)
	}
	if first["kid"] != active.Kid {
		t.Fatalf("first jwk kid = %v, want %v", first["kid"], active.Kid)
	}
}

// ---------------------------------------------------------------------------
// GET /oauth/authorize.
// ---------------------------------------------------------------------------

func TestAuthorizeParameterValidation(t *testing.T) {
	env := newRouteEnv(t)
	// Seed a key so the lazy ensure is a no-op and the handler is reached.
	env.get(t, "/oauth/jwks", nil)
	cases := []struct {
		name      string
		overrides map[string]string
	}{
		{"wrong response_type", map[string]string{"response_type": "token"}},
		{"missing client_id", map[string]string{"client_id": ""}},
		{"invalid redirect_uri", map[string]string{"redirect_uri": "not-a-url"}},
		{"missing scope", map[string]string{"scope": ""}},
		{"missing state", map[string]string{"state": ""}},
		{"oversized state", map[string]string{"state": strings.Repeat("s", 1025)}},
		{"short code_challenge", map[string]string{"code_challenge": strings.Repeat("a", 42)}},
		{"invalid code_challenge charset", map[string]string{"code_challenge": strings.Repeat("a", 42) + "!"}},
		{"wrong challenge method", map[string]string{"code_challenge_method": "plain"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := env.get(t, "/oauth/authorize"+env.authorizeQuery(tc.overrides), sessionCookie(env.sessionToken))
			assertOAuthError(t, rec, http.StatusBadRequest, "invalid_request", "授权请求参数无效")
		})
	}
}

func TestAuthorizeClientValidation(t *testing.T) {
	env := newRouteEnv(t)
	env.get(t, "/oauth/jwks", nil)
	// Unknown client.
	rec := env.get(t, "/oauth/authorize"+env.authorizeQuery(map[string]string{"client_id": "juhe_ghost"}), sessionCookie(env.sessionToken))
	assertOAuthError(t, rec, http.StatusBadRequest, "invalid_request", "Client 或回调地址无效")
	// Disabled client.
	mustExec(t, env.db, `UPDATE oauth_clients SET status = 'disabled' WHERE client_id = ?`, env.publicID)
	rec = env.get(t, "/oauth/authorize"+env.authorizeQuery(nil), sessionCookie(env.sessionToken))
	assertOAuthError(t, rec, http.StatusBadRequest, "invalid_request", "Client 或回调地址无效")
	mustExec(t, env.db, `UPDATE oauth_clients SET status = 'active' WHERE client_id = ?`, env.publicID)
	// Unregistered redirect.
	rec = env.get(t, "/oauth/authorize"+env.authorizeQuery(map[string]string{"redirect_uri": "https://evil.example.com/callback"}), sessionCookie(env.sessionToken))
	assertOAuthError(t, rec, http.StatusBadRequest, "invalid_request", "Client 或回调地址无效")
}

func TestAuthorizeScopeValidation(t *testing.T) {
	env := newRouteEnv(t)
	env.get(t, "/oauth/jwks", nil)
	cases := []struct {
		name  string
		scope string
		code  string
	}{
		{"scope not registered for the client", "juhe:groups.read", "invalid_scope"},
		{"profile without openid", "profile", "invalid_scope"},
		{"write without read", "juhe:profile.write", "invalid_scope"},
		{"totally unknown scope", "juhe:nothing", "invalid_scope"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := env.get(t, "/oauth/authorize"+env.authorizeQuery(map[string]string{"scope": tc.scope}), sessionCookie(env.sessionToken))
			assertOAuthError(t, rec, http.StatusBadRequest, tc.code, "请求的 scope 未登记")
		})
	}
	// openid without nonce.
	rec := env.get(t, "/oauth/authorize"+env.authorizeQuery(map[string]string{"nonce": ""}), sessionCookie(env.sessionToken))
	assertOAuthError(t, rec, http.StatusBadRequest, "invalid_request", "请求 openid scope 时必须提供 nonce")
}

func TestAuthorizeRendersConsentHTML(t *testing.T) {
	env := newRouteEnv(t)
	rec := env.get(t, "/oauth/authorize"+env.authorizeQuery(nil), sessionCookie(env.sessionToken))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("content-type = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("cache-control = %q", got)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"<title>授权确认</title>",
		"应用 <strong>Public App</strong> 请求访问你的 juhe-ai 个人资源。",
		"<li>openid</li>",
		"<li>profile</li>",
		`action="/oauth/authorize/decision"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("consent html missing %q", want)
		}
	}
	if transactionFieldPattern.FindStringSubmatch(body) == nil || csrfFieldPattern.FindStringSubmatch(body) == nil {
		t.Fatal("consent html missing hidden form fields")
	}

	// Without a session the caller is bounced to the login page. Resume the
	// same transaction so the redirect target is deterministic.
	transactionID := firstMatch(t, transactionFieldPattern, body)
	anonymous := env.get(t, "/oauth/authorize?transaction_id="+urlQueryEscape(transactionID), nil)
	if anonymous.Code != http.StatusFound {
		t.Fatalf("anonymous status = %d", anonymous.Code)
	}
	wantLocation := "/__aisys__/login?redirect=" + urlQueryEscape("/oauth/authorize?transaction_id="+transactionID)
	if got := anonymous.Header().Get("Location"); got != wantLocation {
		t.Fatalf("login location = %q, want %q", got, wantLocation)
	}
}

func TestAuthorizeEscapesHTMLInClientDisplayName(t *testing.T) {
	env := newRouteEnv(t)
	now := iso(env.clock.Now())
	mustExec(t, env.db, `INSERT INTO oauth_clients (id, client_id, display_name, client_type, client_secret_hash,
		client_secret_ciphertext, redirect_uris_json, allowed_scopes_json, status, created_at, updated_at)
		VALUES ('cl-escape', 'juhe_escape_client', ?, 'public', NULL, NULL, ?, ?, 'active', ?, ?)`,
		`<Scr>"'&`,
		`["`+env.publicRedirect+`"]`,
		`["openid"]`, now, now)
	query := env.authorizeQuery(map[string]string{"client_id": "juhe_escape_client", "scope": "openid"})
	rec := env.get(t, "/oauth/authorize"+query, sessionCookie(env.sessionToken))
	body := rec.Body.String()
	if !strings.Contains(body, `&lt;Scr&gt;&quot;&#39;&amp;`) {
		t.Fatalf("display name not escaped: %s", body)
	}
	if strings.Contains(body, "<Scr>") {
		t.Fatal("raw html leaked into consent page")
	}
}

func TestAuthorizeResumeWithTransactionID(t *testing.T) {
	env := newRouteEnv(t)
	env.get(t, "/oauth/jwks", nil)
	transactionID, csrf := env.startAuthorize(t, env.authorizeQuery(nil))
	// Resuming renders the same consent page for the stored transaction.
	rec := env.get(t, "/oauth/authorize?transaction_id="+urlQueryEscape(transactionID), sessionCookie(env.sessionToken))
	if rec.Code != http.StatusOK {
		t.Fatalf("resume status = %d", rec.Code)
	}
	if got := firstMatch(t, transactionFieldPattern, rec.Body.String()); got != transactionID {
		t.Fatalf("resume transaction = %q, want %q", got, transactionID)
	}
	if got := firstMatch(t, csrfFieldPattern, rec.Body.String()); got != csrf {
		t.Fatalf("resume csrf = %q, want %q", got, csrf)
	}
	// Unknown / consumed / expired transaction ids answer the Node contract:
	// 400 invalid_request "授权请求不存在或已过期" (FindAuthorizationTransaction
	// returns undefined for them).
	rec = env.get(t, "/oauth/authorize?transaction_id="+urlQueryEscape("00000000-0000-4000-8000-000000000000"), sessionCookie(env.sessionToken))
	assertOAuthError(t, rec, http.StatusBadRequest, "invalid_request", "授权请求不存在或已过期")
	// Expired: the transaction from startAuthorize lives 10 minutes.
	env.clock.Advance(10 * time.Minute)
	rec = env.get(t, "/oauth/authorize?transaction_id="+urlQueryEscape(transactionID), sessionCookie(env.sessionToken))
	assertOAuthError(t, rec, http.StatusBadRequest, "invalid_request", "授权请求不存在或已过期")
}

// ---------------------------------------------------------------------------
// POST /oauth/authorize/decision.
// ---------------------------------------------------------------------------

func TestAuthorizeDecisionValidation(t *testing.T) {
	env := newRouteEnv(t)
	env.get(t, "/oauth/jwks", nil)
	transactionID, csrfToken := env.startAuthorize(t, env.authorizeQuery(nil))
	cases := []struct {
		name    string
		fields  map[string]string
		headers map[string]string
	}{
		{"non-uuid transaction", map[string]string{"transaction_id": "not-a-uuid", "csrf_token": csrfToken, "decision": "allow"}, sessionCookie(env.sessionToken)},
		{"missing csrf", map[string]string{"transaction_id": transactionID, "decision": "allow"}, sessionCookie(env.sessionToken)},
		{"invalid decision", map[string]string{"transaction_id": transactionID, "csrf_token": csrfToken, "decision": "maybe"}, sessionCookie(env.sessionToken)},
		{"missing session", map[string]string{"transaction_id": transactionID, "csrf_token": csrfToken, "decision": "allow"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := env.postForm(t, "/oauth/authorize/decision", tc.fields, tc.headers)
			assertOAuthError(t, rec, http.StatusBadRequest, "invalid_request", "授权确认请求无效")
		})
	}
	// Unknown transaction with a valid shape and session.
	rec := env.postForm(t, "/oauth/authorize/decision", map[string]string{
		"transaction_id": "00000000-0000-4000-8000-000000000001", "csrf_token": csrfToken, "decision": "allow",
	}, sessionCookie(env.sessionToken))
	assertOAuthError(t, rec, http.StatusBadRequest, "invalid_request", "授权事务无效或已处理")
	// Wrong CSRF on a live transaction.
	rec = env.postForm(t, "/oauth/authorize/decision", map[string]string{
		"transaction_id": transactionID, "csrf_token": "wrong-csrf", "decision": "allow",
	}, sessionCookie(env.sessionToken))
	assertOAuthError(t, rec, http.StatusBadRequest, "invalid_request", "授权事务无效或已处理")
}

func TestAuthorizeDecisionDenyRedirectsWithAccessDenied(t *testing.T) {
	env := newRouteEnv(t)
	env.get(t, "/oauth/jwks", nil)
	transactionID, csrfToken := env.startAuthorize(t, env.authorizeQuery(nil))
	rec := env.postForm(t, "/oauth/authorize/decision", map[string]string{
		"transaction_id": transactionID, "csrf_token": csrfToken, "decision": "deny",
	}, sessionCookie(env.sessionToken))
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d", rec.Code)
	}
	want := env.publicRedirect + "?error=access_denied&state=st-123"
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("location = %q, want %q", got, want)
	}
	// The transaction is consumed: a replay finds nothing.
	rec = env.postForm(t, "/oauth/authorize/decision", map[string]string{
		"transaction_id": transactionID, "csrf_token": csrfToken, "decision": "deny",
	}, sessionCookie(env.sessionToken))
	assertOAuthError(t, rec, http.StatusBadRequest, "invalid_request", "授权事务无效或已处理")
}

func TestAuthorizeDecisionAllowIssuesCode(t *testing.T) {
	env := newRouteEnv(t)
	env.get(t, "/oauth/jwks", nil)
	transactionID, csrfToken := env.startAuthorize(t, env.authorizeQuery(nil))
	rec := env.postForm(t, "/oauth/authorize/decision", map[string]string{
		"transaction_id": transactionID, "csrf_token": csrfToken, "decision": "allow",
	}, sessionCookie(env.sessionToken))
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	location, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("location: %v", err)
	}
	if location.String() != env.publicRedirect+"?code="+location.Query().Get("code")+"&state=st-123" {
		t.Fatalf("location = %q", location.String())
	}
	code := location.Query().Get("code")
	if code == "" {
		t.Fatal("no code issued")
	}
	// The issued code is bound to the transaction's PKCE challenge and
	// client. The wrong verifier cannot redeem it (and must not consume it);
	// the right one can, exactly once (Node lifetimes: code 120s, grant 7d).
	rec = env.postForm(t, "/oauth/token", map[string]string{
		"grant_type": "authorization_code", "code": code, "redirect_uri": env.publicRedirect,
		"code_verifier": strings.Repeat("a", 43), "client_id": env.publicID,
	}, nil)
	assertOAuthError(t, rec, http.StatusBadRequest, "invalid_grant", "授权码无效、已过期或已使用")
	tokens := env.exchangeCode(t, code)
	if tokens["access_token"] == "" {
		t.Fatal("no access token issued")
	}
	// One-time: the replay is rejected.
	assertOAuthError(t, env.postForm(t, "/oauth/token", map[string]string{
		"grant_type": "authorization_code", "code": code, "redirect_uri": env.publicRedirect,
		"code_verifier": pkceTestVerifier, "client_id": env.publicID,
	}, nil), http.StatusBadRequest, "invalid_grant", "授权码无效、已过期或已使用")
}

// ---------------------------------------------------------------------------
// POST /oauth/device_authorization.
// ---------------------------------------------------------------------------

func TestDeviceAuthorizationEndpoint(t *testing.T) {
	env := newRouteEnv(t)
	env.get(t, "/oauth/jwks", nil)

	t.Run("public client without authorization header", func(t *testing.T) {
		rec := env.postForm(t, "/oauth/device_authorization", map[string]string{
			"client_id": env.publicID, "scope": "openid profile", "nonce": "n-dev",
		}, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
		}
		payload := decodeJSONBody(t, rec)
		if stringField(t, payload, "verification_uri") != oidcTestIssuer+"/oauth/device" {
			t.Fatalf("verification_uri = %v", payload["verification_uri"])
		}
		userCode := stringField(t, payload, "user_code")
		if stringField(t, payload, "verification_uri_complete") != oidcTestIssuer+"/oauth/device?user_code="+userCode {
			t.Fatalf("verification_uri_complete = %v", payload["verification_uri_complete"])
		}
		if got := numberField(t, payload, "expires_in"); got != 600 {
			t.Fatalf("expires_in = %v", got)
		}
		if got := numberField(t, payload, "interval"); got != 5 {
			t.Fatalf("interval = %v", got)
		}
		if stringField(t, payload, "device_code") == "" {
			t.Fatal("missing device_code")
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("cache-control = %q", got)
		}
	})

	t.Run("confidential client with basic auth", func(t *testing.T) {
		rec := env.postForm(t, "/oauth/device_authorization", map[string]string{
			"scope": "openid", "nonce": "n",
		}, basicAuthHeader(env.confID, env.confSecret))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("confidential client with form client_id and wrong secret", func(t *testing.T) {
		rec := env.postForm(t, "/oauth/device_authorization", map[string]string{
			"client_id": env.confID, "scope": "openid", "nonce": "n",
		}, basicAuthHeader(env.confID, "wrong-secret"))
		assertOAuthError(t, rec, http.StatusUnauthorized, "invalid_client", "Client 认证失败")
	})

	authCases := []struct {
		name    string
		fields  map[string]string
		headers map[string]string
	}{
		{"unknown client", map[string]string{"client_id": "juhe_ghost", "scope": "openid"}, nil},
		{"missing client id", map[string]string{"scope": "openid"}, nil},
		{"public client with basic auth", map[string]string{"client_id": env.publicID, "scope": "openid"}, basicAuthHeader(env.publicID, "")},
		{"confidential without basic auth", map[string]string{"client_id": env.confID, "scope": "openid"}, nil},
	}
	for _, tc := range authCases {
		t.Run("client auth: "+tc.name, func(t *testing.T) {
			rec := env.postForm(t, "/oauth/device_authorization", tc.fields, tc.headers)
			assertOAuthError(t, rec, http.StatusUnauthorized, "invalid_client", "Client 认证失败")
		})
	}

	scopeCases := []struct {
		name  string
		scope string
		code  string
	}{
		{"empty scope", "", "invalid_scope"},
		{"unregistered scope", "juhe:groups.read", "invalid_scope"},
		{"profile without openid", "profile", "invalid_scope"},
		{"write without read", "juhe:profile.write", "invalid_scope"},
	}
	for _, tc := range scopeCases {
		t.Run("scope: "+tc.name, func(t *testing.T) {
			rec := env.postForm(t, "/oauth/device_authorization", map[string]string{
				"client_id": env.publicID, "scope": tc.scope,
			}, nil)
			assertOAuthError(t, rec, http.StatusBadRequest, tc.code, "请求的 scope 未登记")
		})
	}

	t.Run("openid requires nonce", func(t *testing.T) {
		rec := env.postForm(t, "/oauth/device_authorization", map[string]string{
			"client_id": env.publicID, "scope": "openid",
		}, nil)
		assertOAuthError(t, rec, http.StatusBadRequest, "invalid_request", "请求 openid scope 时必须提供 nonce")
	})
	t.Run("oversized nonce rejected", func(t *testing.T) {
		rec := env.postForm(t, "/oauth/device_authorization", map[string]string{
			"client_id": env.publicID, "scope": "openid", "nonce": strings.Repeat("n", 1025),
		}, nil)
		assertOAuthError(t, rec, http.StatusBadRequest, "invalid_request", "请求 openid scope 时必须提供 nonce")
	})
	t.Run("whitespace nonce counts as missing", func(t *testing.T) {
		rec := env.postForm(t, "/oauth/device_authorization", map[string]string{
			"client_id": env.publicID, "scope": "openid", "nonce": "   ",
		}, nil)
		assertOAuthError(t, rec, http.StatusBadRequest, "invalid_request", "请求 openid scope 时必须提供 nonce")
	})
}

// ---------------------------------------------------------------------------
// GET /oauth/device.
// ---------------------------------------------------------------------------

func TestDevicePageFlow(t *testing.T) {
	env := newRouteEnv(t)
	env.get(t, "/oauth/jwks", nil)

	t.Run("entry page without user_code", func(t *testing.T) {
		rec := env.get(t, "/oauth/device", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		for _, want := range []string{"<title>设备授权</title>", `action="/oauth/device"`, `name="user_code"`} {
			if !strings.Contains(rec.Body.String(), want) {
				t.Fatalf("entry html missing %q", want)
			}
		}
	})

	t.Run("user code redirects anonymous visitor to login", func(t *testing.T) {
		rec := env.get(t, "/oauth/device?user_code=ABCD2345", nil)
		if rec.Code != http.StatusFound {
			t.Fatalf("status = %d", rec.Code)
		}
		want := "/__aisys__/login?redirect=" + urlQueryEscape("/oauth/device?user_code=ABCD2345")
		if got := rec.Header().Get("Location"); got != want {
			t.Fatalf("location = %q, want %q", got, want)
		}
	})

	t.Run("invalid user code renders the error page", func(t *testing.T) {
		rec := env.get(t, "/oauth/device?user_code=ZZZZ9999", sessionCookie(env.sessionToken))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "设备授权码无效、已过期或已处理") {
			t.Fatalf("error html missing message: %s", rec.Body.String())
		}
	})

	t.Run("valid user code renders the consent page", func(t *testing.T) {
		authorization, _, err := env.store.CreateDeviceAuthorization(nil, struct {
			ClientID        string
			Scopes          []string
			Nonce           string
			VerificationURI string
		}{ClientID: env.publicID, Scopes: []string{"openid", "profile"}, Nonce: "", VerificationURI: "u"})
		if err != nil {
			t.Fatalf("create device: %v", err)
		}
		// Lowercase input must still match.
		rec := env.get(t, "/oauth/device?user_code="+urlQueryEscape(strings.ToLower(authorization.UserCode)), sessionCookie(env.sessionToken))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		for _, want := range []string{
			"<title>设备授权确认</title>",
			"设备码 <strong>" + authorization.UserCode + "</strong>",
			"<li>openid</li>",
			`action="/oauth/device/decision"`,
		} {
			if !strings.Contains(body, want) {
				t.Fatalf("consent html missing %q", want)
			}
		}
		if userCodeFieldPattern.FindStringSubmatch(body) == nil {
			t.Fatal("consent html missing user_code field")
		}
	})
}

// ---------------------------------------------------------------------------
// POST /oauth/device/decision.
// ---------------------------------------------------------------------------

func TestDeviceDecisionEndpoint(t *testing.T) {
	env := newRouteEnv(t)
	env.get(t, "/oauth/jwks", nil)
	mkPending := func() (userCode, csrf string) {
		authorization, _, err := env.store.CreateDeviceAuthorization(nil, struct {
			ClientID        string
			Scopes          []string
			Nonce           string
			VerificationURI string
		}{ClientID: env.publicID, Scopes: []string{"openid"}, Nonce: "", VerificationURI: "u"})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		_, token, err := env.store.PrepareDeviceAuthorization(nil, authorization.UserCode)
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		return authorization.UserCode, token
	}

	cases := []struct {
		name    string
		fields  map[string]string
		headers map[string]string
	}{
		{"empty user code", map[string]string{"user_code": "", "csrf_token": "c", "decision": "allow"}, sessionCookie(env.sessionToken)},
		{"oversized user code", map[string]string{"user_code": strings.Repeat("A", 65), "csrf_token": "c", "decision": "allow"}, sessionCookie(env.sessionToken)},
		{"missing csrf", map[string]string{"user_code": "ABCD2345", "decision": "allow"}, sessionCookie(env.sessionToken)},
		{"invalid decision", map[string]string{"user_code": "ABCD2345", "csrf_token": "c", "decision": "perhaps"}, sessionCookie(env.sessionToken)},
		{"missing session", map[string]string{"user_code": "ABCD2345", "csrf_token": "c", "decision": "allow"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := env.postForm(t, "/oauth/device/decision", tc.fields, tc.headers)
			assertOAuthError(t, rec, http.StatusBadRequest, "invalid_request", "设备授权确认请求无效")
		})
	}

	t.Run("wrong csrf rejected", func(t *testing.T) {
		userCode, _ := mkPending()
		rec := env.postForm(t, "/oauth/device/decision", map[string]string{
			"user_code": userCode, "csrf_token": "wrong", "decision": "allow",
		}, sessionCookie(env.sessionToken))
		assertOAuthError(t, rec, http.StatusBadRequest, "invalid_request", "设备授权码无效、已过期或已处理")
	})

	t.Run("allow completes the flow", func(t *testing.T) {
		userCode, csrf := mkPending()
		rec := env.postForm(t, "/oauth/device/decision", map[string]string{
			"user_code": userCode, "csrf_token": csrf, "decision": "allow",
		}, sessionCookie(env.sessionToken))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "设备已获授权，你可以回到设备继续。") {
			t.Fatalf("allow html wrong: %s", rec.Body.String())
		}
		// Replay after consumption is rejected.
		rec = env.postForm(t, "/oauth/device/decision", map[string]string{
			"user_code": userCode, "csrf_token": csrf, "decision": "allow",
		}, sessionCookie(env.sessionToken))
		assertOAuthError(t, rec, http.StatusBadRequest, "invalid_request", "设备授权码无效、已过期或已处理")
	})

	t.Run("deny completes the flow", func(t *testing.T) {
		userCode, csrf := mkPending()
		rec := env.postForm(t, "/oauth/device/decision", map[string]string{
			"user_code": userCode, "csrf_token": csrf, "decision": "deny",
		}, sessionCookie(env.sessionToken))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "设备授权已拒绝。") {
			t.Fatalf("deny html wrong: %s", rec.Body.String())
		}
	})
}

// ---------------------------------------------------------------------------
// POST /oauth/token.
// ---------------------------------------------------------------------------

func TestTokenEndpointAvailability(t *testing.T) {
	t.Run("disabled provider", func(t *testing.T) {
		env := newRouteEnv(t)
		env.deps.OIDCEnabled = false
		rec := env.postForm(t, "/oauth/token", map[string]string{"grant_type": "authorization_code"}, nil)
		assertMessageBody(t, rec, http.StatusNotFound, "OIDC Provider 未启用")
	})
	t.Run("no active signing key", func(t *testing.T) {
		env := newRouteEnvWithSecret(t, "")
		env.clearSigningKeys(t)
		rec := env.postForm(t, "/oauth/token", map[string]string{"grant_type": "authorization_code", "client_id": env.publicID}, nil)
		assertOAuthError(t, rec, http.StatusServiceUnavailable, "temporarily_unavailable", "OIDC 签名密钥未配置或不可用")
	})
}

func TestTokenEndpointClientAuthentication(t *testing.T) {
	env := newRouteEnv(t)
	env.get(t, "/oauth/jwks", nil)
	cases := []struct {
		name    string
		fields  map[string]string
		headers map[string]string
	}{
		{"no client id", map[string]string{"grant_type": "authorization_code"}, nil},
		{"unknown client", map[string]string{"client_id": "juhe_ghost", "grant_type": "authorization_code"}, nil},
		{"public client with basic header", map[string]string{"client_id": env.publicID, "grant_type": "authorization_code"}, basicAuthHeader(env.publicID, "")},
		{"confidential client without basic header", map[string]string{"client_id": env.confID, "grant_type": "authorization_code"}, nil},
		{"confidential client wrong secret", map[string]string{"client_id": env.confID, "grant_type": "authorization_code"}, basicAuthHeader(env.confID, "nope")},
		{"basic id mismatch", map[string]string{"grant_type": "authorization_code"}, basicAuthHeader("someone-else", env.confSecret)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := env.postForm(t, "/oauth/token", tc.fields, tc.headers)
			assertOAuthError(t, rec, http.StatusUnauthorized, "invalid_client", "Client 认证失败")
		})
	}
	// Disabled client.
	mustExec(t, env.db, `UPDATE oauth_clients SET status = 'disabled' WHERE client_id = ?`, env.publicID)
	rec := env.postForm(t, "/oauth/token", map[string]string{"client_id": env.publicID, "grant_type": "authorization_code"}, nil)
	assertOAuthError(t, rec, http.StatusUnauthorized, "invalid_client", "Client 认证失败")
	mustExec(t, env.db, `UPDATE oauth_clients SET status = 'active' WHERE client_id = ?`, env.publicID)
}

func TestTokenAuthorizationCodeGrantValidation(t *testing.T) {
	env := newRouteEnv(t)
	env.get(t, "/oauth/jwks", nil)
	base := map[string]string{"client_id": env.publicID, "grant_type": "authorization_code",
		"code": "c", "redirect_uri": env.publicRedirect, "code_verifier": pkceTestVerifier}
	cases := []struct {
		name string
		drop string
	}{
		{"missing grant_type", "grant_type"},
		{"missing code", "code"},
		{"missing redirect_uri", "redirect_uri"},
		{"missing code_verifier", "code_verifier"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fields := map[string]string{}
			for key, value := range base {
				fields[key] = value
			}
			delete(fields, tc.drop)
			rec := env.postForm(t, "/oauth/token", fields, nil)
			assertOAuthError(t, rec, http.StatusBadRequest, "invalid_grant", "授权码参数无效")
		})
	}
	t.Run("unsupported grant type", func(t *testing.T) {
		fields := map[string]string{}
		for key, value := range base {
			fields[key] = value
		}
		fields["grant_type"] = "client_credentials"
		rec := env.postForm(t, "/oauth/token", fields, nil)
		assertOAuthError(t, rec, http.StatusBadRequest, "invalid_grant", "授权码参数无效")
	})

	// Unknown or unredeemable codes never leak which part failed. Seed a
	// redeemable code per case (seedAuthorizationCode pins the exact
	// bindings without rerunning the consent loop).
	failureCases := []struct {
		name   string
		mutate func(fields map[string]string)
	}{
		{"unknown code", func(fields map[string]string) { fields["code"] = "no-such-code" }},
		{"wrong redirect", func(fields map[string]string) { fields["redirect_uri"] = "https://evil.example.com/cb" }},
		{"wrong verifier", func(fields map[string]string) { fields["code_verifier"] = strings.Repeat("x", 43) }},
	}
	for _, tc := range failureCases {
		t.Run(tc.name, func(t *testing.T) {
			code := env.seedAuthorizationCode(t, env.publicID, env.accountID,
				[]string{"openid", "profile"}, env.publicRedirect, pkceChallengeOf(pkceTestVerifier), "",
				nodeMillis(grantLifetimeMs), nodeMillis(authorizationCodeLifetimeMs))
			fields := map[string]string{"client_id": env.publicID, "grant_type": "authorization_code",
				"code": code, "redirect_uri": env.publicRedirect, "code_verifier": pkceTestVerifier}
			tc.mutate(fields)
			rec := env.postForm(t, "/oauth/token", fields, nil)
			assertOAuthError(t, rec, http.StatusBadRequest, "invalid_grant", "授权码无效、已过期或已使用")
		})
	}
	t.Run("replayed code", func(t *testing.T) {
		code := env.seedAuthorizationCode(t, env.publicID, env.accountID,
			[]string{"openid", "profile"}, env.publicRedirect, pkceChallengeOf(pkceTestVerifier), "",
			nodeMillis(grantLifetimeMs), nodeMillis(authorizationCodeLifetimeMs))
		if rec := env.postForm(t, "/oauth/token", map[string]string{
			"client_id": env.publicID, "grant_type": "authorization_code", "code": code,
			"redirect_uri": env.publicRedirect, "code_verifier": pkceTestVerifier,
		}, nil); rec.Code != http.StatusOK {
			t.Fatalf("first exchange status = %d, body=%s", rec.Code, rec.Body.String())
		}
		rec := env.postForm(t, "/oauth/token", map[string]string{
			"client_id": env.publicID, "grant_type": "authorization_code", "code": code,
			"redirect_uri": env.publicRedirect, "code_verifier": pkceTestVerifier,
		}, nil)
		assertOAuthError(t, rec, http.StatusBadRequest, "invalid_grant", "授权码无效、已过期或已使用")
	})
	t.Run("expired code", func(t *testing.T) {
		code := env.seedAuthorizationCode(t, env.publicID, env.accountID,
			[]string{"openid", "profile"}, env.publicRedirect, pkceChallengeOf(pkceTestVerifier), "",
			nodeMillis(grantLifetimeMs), nodeMillis(authorizationCodeLifetimeMs))
		env.clock.Advance(nodeMillis(authorizationCodeLifetimeMs) + time.Second)
		rec := env.postForm(t, "/oauth/token", map[string]string{
			"client_id": env.publicID, "grant_type": "authorization_code", "code": code,
			"redirect_uri": env.publicRedirect, "code_verifier": pkceTestVerifier,
		}, nil)
		assertOAuthError(t, rec, http.StatusBadRequest, "invalid_grant", "授权码无效、已过期或已使用")
	})
}

func TestTokenAuthorizationCodeGrantSuccess(t *testing.T) {
	env := newRouteEnv(t)
	env.get(t, "/oauth/jwks", nil)
	// Plain scope (no openid) → no id_token field.
	code := env.seedAuthorizationCode(t, env.publicID, env.accountID,
		[]string{"juhe:profile.read"}, env.publicRedirect, pkceChallengeOf(pkceTestVerifier), "",
		nodeMillis(grantLifetimeMs), nodeMillis(authorizationCodeLifetimeMs))
	rec := env.postForm(t, "/oauth/token", map[string]string{
		"client_id": env.publicID, "grant_type": "authorization_code", "code": code,
		"redirect_uri": env.publicRedirect, "code_verifier": pkceTestVerifier,
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("cache-control = %q", got)
	}
	payload := decodeJSONBody(t, rec)
	if stringField(t, payload, "access_token") == "" || stringField(t, payload, "token_type") != "Bearer" {
		t.Fatalf("payload = %v", payload)
	}
	if got := numberField(t, payload, "expires_in"); got != 604_800 { // 168h in seconds
		t.Fatalf("expires_in = %v", got)
	}
	if stringField(t, payload, "scope") != "juhe:profile.read" {
		t.Fatalf("scope = %v", payload["scope"])
	}
	if _, present := payload["id_token"]; present {
		t.Fatal("id_token must be absent without the openid scope")
	}
}

func TestTokenIssuesVerifiableIDToken(t *testing.T) {
	env := newRouteEnv(t)
	env.get(t, "/oauth/jwks", nil)
	code := env.seedAuthorizationCode(t, env.publicID, env.accountID,
		[]string{"openid", "profile"}, env.publicRedirect, pkceChallengeOf(pkceTestVerifier), "n-abc",
		nodeMillis(grantLifetimeMs), nodeMillis(authorizationCodeLifetimeMs))
	issuedAt := env.clock.Now()
	payload := env.exchangeCode(t, code)
	idToken := stringField(t, payload, "id_token")
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		t.Fatalf("id_token has %d parts", len(parts))
	}
	decodePart := func(part string) map[string]any {
		raw, err := base64.RawURLEncoding.DecodeString(part)
		if err != nil {
			t.Fatalf("decode id_token part: %v", err)
		}
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("unmarshal id_token part: %v", err)
		}
		return out
	}
	header := decodePart(parts[0])
	claims := decodePart(parts[1])
	active, err := env.store.FindActiveSigningKey(nil)
	if err != nil || active == nil {
		t.Fatalf("active key: %v", err)
	}
	if header["alg"] != "RS256" || header["kid"] != active.Kid || header["typ"] != "JWT" {
		t.Fatalf("id_token header = %v", header)
	}
	if claims["iss"] != oidcTestIssuer || claims["aud"] != env.publicID || claims["nonce"] != "n-abc" {
		t.Fatalf("id_token claims = %v", claims)
	}
	// sub is the HMAC-derived pseudonymous subject.
	wantSub, err := OidcSubjectForSystemAccount(oidcTestSecret, oidcTestIssuer, env.accountID)
	if err != nil {
		t.Fatalf("subject: %v", err)
	}
	if claims["sub"] != wantSub {
		t.Fatalf("sub = %v, want %v", claims["sub"], wantSub)
	}
	// The grant lives 168h, so the id token exp is clamped to now+5m.
	if claims["exp"] != float64(issuedAt.Add(5*time.Minute).Unix()) {
		t.Fatalf("exp = %v, want %v", claims["exp"], float64(issuedAt.Add(5*time.Minute).Unix()))
	}
	if claims["iat"] != float64(issuedAt.Unix()) {
		t.Fatalf("iat = %v", claims["iat"])
	}
	// Verify the RS256 signature against the published JWKS.
	jwks := decodeJSONBody(t, env.get(t, "/oauth/jwks", nil))
	keys := jwks["keys"].([]any)
	jwk := keys[0].(map[string]any)
	n, err := base64.RawURLEncoding.DecodeString(jwk["n"].(string))
	if err != nil {
		t.Fatalf("decode n: %v", err)
	}
	e, err := base64.RawURLEncoding.DecodeString(jwk["e"].(string))
	if err != nil {
		t.Fatalf("decode e: %v", err)
	}
	public := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(public, crypto.SHA256, digest[:], signature); err != nil {
		t.Fatalf("id_token signature: %v", err)
	}
}

func TestTokenGrantExpiredMapsToServerError(t *testing.T) {
	env := newRouteEnv(t)
	env.get(t, "/oauth/jwks", nil)
	code := env.seedAuthorizationCode(t, env.publicID, env.accountID,
		[]string{"openid"}, env.publicRedirect, pkceChallengeOf(pkceTestVerifier), "",
		nodeMillis(grantLifetimeMs), nodeMillis(authorizationCodeLifetimeMs))
	// Corrupt the grant client binding: the code JOIN matches but
	// issueAccessTokenInTransaction refuses → 500 server_error.
	grantID := mustQueryString(t, env.db, `SELECT grant_id FROM oauth_authorization_codes WHERE code_hash = ?`, hashSecret(code))
	mustExec(t, env.db, `UPDATE oauth_grants SET client_id = ? WHERE id = ?`, env.confID, grantID)
	rec := env.postForm(t, "/oauth/token", map[string]string{
		"client_id": env.publicID, "grant_type": "authorization_code", "code": code,
		"redirect_uri": env.publicRedirect, "code_verifier": pkceTestVerifier,
	}, nil)
	assertOAuthError(t, rec, http.StatusInternalServerError, "server_error", "OAuth 服务暂时不可用")
}

func TestTokenDeviceGrantKinds(t *testing.T) {
	env := newRouteEnv(t)
	env.get(t, "/oauth/jwks", nil)

	poll := func(deviceCode string) *httptest.ResponseRecorder {
		return env.postForm(t, "/oauth/token", map[string]string{
			"client_id": env.publicID, "grant_type": "urn:ietf:params:oauth:grant-type:device_code",
			"device_code": deviceCode,
		}, nil)
	}

	t.Run("missing device_code", func(t *testing.T) {
		rec := env.postForm(t, "/oauth/token", map[string]string{
			"client_id": env.publicID, "grant_type": "urn:ietf:params:oauth:grant-type:device_code",
		}, nil)
		assertOAuthError(t, rec, http.StatusBadRequest, "invalid_request", "device_code 参数无效")
	})
	t.Run("empty device_code goes to the poll path", func(t *testing.T) {
		// Node: `typeof body.device_code !== 'string'` is the only
		// invalid_request branch — device_code= is a string and reaches the
		// poll, whose unknown-row answer is invalid_grant.
		rec := env.postForm(t, "/oauth/token", map[string]string{
			"client_id": env.publicID, "grant_type": "urn:ietf:params:oauth:grant-type:device_code",
			"device_code": "",
		}, nil)
		assertOAuthError(t, rec, http.StatusBadRequest, "invalid_grant", "设备码无效或已使用")
	})
	t.Run("unknown device_code", func(t *testing.T) {
		assertOAuthError(t, poll("unknown-device-code"), http.StatusBadRequest, "invalid_grant", "设备码无效或已使用")
	})
	t.Run("authorization_pending", func(t *testing.T) {
		deviceCode := env.approveDevicePrep(t, false)
		assertOAuthError(t, poll(deviceCode), http.StatusBadRequest, "authorization_pending", "用户尚未完成设备授权确认")
	})
	t.Run("slow_down", func(t *testing.T) {
		deviceCode := env.approveDevicePrep(t, false)
		_ = poll(deviceCode) // sets last_polled_at
		assertOAuthError(t, poll(deviceCode), http.StatusBadRequest, "slow_down", "设备轮询过于频繁，请降低频率")
	})
	t.Run("expired", func(t *testing.T) {
		deviceCode := env.approveDevicePrep(t, false)
		env.clock.Advance(11 * time.Minute)
		assertOAuthError(t, poll(deviceCode), http.StatusBadRequest, "expired_token", "设备码已过期")
	})
	t.Run("access_denied", func(t *testing.T) {
		deviceCode := env.approveDeviceDenied(t)
		assertOAuthError(t, poll(deviceCode), http.StatusBadRequest, "access_denied", "用户拒绝了设备授权")
	})
	t.Run("approved issues tokens and consumes one time", func(t *testing.T) {
		deviceCode := env.approveDevice(t, "openid profile", "n-dev")
		rec := poll(deviceCode)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
		}
		payload := decodeJSONBody(t, rec)
		if stringField(t, payload, "access_token") == "" || stringField(t, payload, "token_type") != "Bearer" {
			t.Fatalf("payload = %v", payload)
		}
		if _, present := payload["id_token"]; !present {
			t.Fatal("openid device flow must issue an id_token")
		}
		if stringField(t, payload, "scope") != "openid profile" {
			t.Fatalf("scope = %v", payload["scope"])
		}
		assertOAuthError(t, poll(deviceCode), http.StatusBadRequest, "invalid_grant", "设备码无效或已使用")
	})
	t.Run("device code of another client is invalid", func(t *testing.T) {
		deviceCode := env.approveDevice(t, "openid", "n")
		rec := env.postForm(t, "/oauth/token", map[string]string{
			"client_id": env.confID, "grant_type": "urn:ietf:params:oauth:grant-type:device_code",
			"device_code": deviceCode,
		}, basicAuthHeader(env.confID, env.confSecret))
		assertOAuthError(t, rec, http.StatusBadRequest, "invalid_grant", "设备码无效或已使用")
	})
}

// approveDevicePrep creates a pending device authorization (decision pending
// when decided=false, decided when true) and returns the device code.
func (e *routeEnv) approveDevicePrep(t *testing.T, decided bool) string {
	if decided {
		return e.approveDevice(t, "openid", "n")
	}
	rec := e.postForm(t, "/oauth/device_authorization", map[string]string{
		"client_id": e.publicID, "scope": "openid", "nonce": "n",
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("device_authorization status = %d", rec.Code)
	}
	return stringField(t, decodeJSONBody(t, rec), "device_code")
}

func (e *routeEnv) approveDeviceDenied(t *testing.T) string {
	rec := e.postForm(t, "/oauth/device_authorization", map[string]string{
		"client_id": e.publicID, "scope": "openid", "nonce": "n",
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("device_authorization status = %d", rec.Code)
	}
	payload := decodeJSONBody(t, rec)
	userCode := stringField(t, payload, "user_code")
	consent := e.get(t, "/oauth/device?user_code="+urlQueryEscape(userCode), sessionCookie(e.sessionToken))
	csrfToken := firstMatch(t, csrfFieldPattern, consent.Body.String())
	decided := e.postForm(t, "/oauth/device/decision", map[string]string{
		"user_code": userCode, "csrf_token": csrfToken, "decision": "deny",
	}, sessionCookie(e.sessionToken))
	if decided.Code != http.StatusOK {
		t.Fatalf("device decision status = %d", decided.Code)
	}
	return stringField(t, payload, "device_code")
}

// ---------------------------------------------------------------------------
// POST /oauth/token/renew.
// ---------------------------------------------------------------------------

func TestTokenRenewEndpoint(t *testing.T) {
	env := newRouteEnv(t)
	env.get(t, "/oauth/jwks", nil)
	seedToken := func(grantID, token string) {
		mustExec(t, env.db, `INSERT INTO oauth_grants (id, client_id, system_account_id, scopes_json, expires_at, revoked_at, created_at)
			VALUES (?, ?, ?, '["juhe:profile.read"]', ?, NULL, ?)`,
			grantID, env.publicID, env.accountID, iso(env.clock.Now().Add(96*time.Hour)), iso(env.clock.Now()))
		mustExec(t, env.db, `INSERT INTO oauth_access_tokens (id, token_hash, client_id, grant_id, issued_at, expires_at, revoked_at, replaced_at, successor_token_id, created_at)
			VALUES (?, ?, ?, ?, ?, ?, NULL, NULL, NULL, ?)`,
			"tok-"+token, hashSecret(token), env.publicID, grantID, iso(env.clock.Now()), iso(env.clock.Now().Add(96*time.Hour)), iso(env.clock.Now()))
	}
	renew := func(token string, headers map[string]string) *httptest.ResponseRecorder {
		return env.postForm(t, "/oauth/token/renew", map[string]string{
			"client_id": env.publicID, "current_access_token": token,
		}, headers)
	}

	t.Run("missing current token", func(t *testing.T) {
		rec := env.postForm(t, "/oauth/token/renew", map[string]string{"client_id": env.publicID}, nil)
		assertOAuthError(t, rec, http.StatusUnauthorized, "invalid_client", "Client 认证失败")
	})
	t.Run("unknown client", func(t *testing.T) {
		rec := env.postForm(t, "/oauth/token/renew", map[string]string{"client_id": "juhe_ghost", "current_access_token": "t"}, nil)
		assertOAuthError(t, rec, http.StatusUnauthorized, "invalid_client", "Client 认证失败")
	})
	t.Run("not eligible inside 72h", func(t *testing.T) {
		seedToken("grant-renew-fresh", "tok-fresh")
		rec := renew("tok-fresh", nil)
		assertOAuthError(t, rec, http.StatusBadRequest, "token_renewal_not_eligible", "当前令牌签发未满 72 小时")
	})
	t.Run("invalid token", func(t *testing.T) {
		rec := renew("tok-ghost", nil)
		assertOAuthError(t, rec, http.StatusBadRequest, "invalid_token", "令牌无效或授权已到期")
	})
	t.Run("rotates after 72h and retires the old token", func(t *testing.T) {
		seedToken("grant-renew-aged", "tok-aged")
		env.clock.Advance(73 * time.Hour)
		rec := renew("tok-aged", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
		}
		payload := decodeJSONBody(t, rec)
		newToken := stringField(t, payload, "access_token")
		if newToken == "" || newToken == "tok-aged" {
			t.Fatalf("renewed token = %q", newToken)
		}
		if stringField(t, payload, "token_type") != "Bearer" || stringField(t, payload, "scope") != "juhe:profile.read" {
			t.Fatalf("payload = %v", payload)
		}
		// The old token is replaced and unusable; the new one resolves.
		if context, err := env.store.FindAccessTokenContext(nil, "tok-aged"); err != nil || context != nil {
			t.Fatalf("old token still valid = %+v", context)
		}
		if context, err := env.store.FindAccessTokenContext(nil, newToken); err != nil || context == nil {
			t.Fatalf("new token unresolvable = %+v, err=%v", context, err)
		}
	})
	t.Run("confidential client via basic auth", func(t *testing.T) {
		// The confidential client has no seeded grant → invalid_token after auth passes.
		rec := env.postForm(t, "/oauth/token/renew", map[string]string{"current_access_token": "whatever"}, basicAuthHeader(env.confID, env.confSecret))
		assertOAuthError(t, rec, http.StatusBadRequest, "invalid_token", "令牌无效或授权已到期")
	})
}

// ---------------------------------------------------------------------------
// POST /oauth/revoke.
// ---------------------------------------------------------------------------

func TestRevokeEndpoint(t *testing.T) {
	env := newRouteEnv(t)
	env.get(t, "/oauth/jwks", nil)
	mustExec(t, env.db, `INSERT INTO oauth_grants (id, client_id, system_account_id, scopes_json, expires_at, revoked_at, created_at)
		VALUES ('grant-revoke', ?, ?, '["juhe:profile.read"]', ?, NULL, ?)`,
		env.publicID, env.accountID, iso(env.clock.Now().Add(24*time.Hour)), iso(env.clock.Now()))
	mustExec(t, env.db, `INSERT INTO oauth_access_tokens (id, token_hash, client_id, grant_id, issued_at, expires_at, revoked_at, replaced_at, successor_token_id, created_at)
		VALUES ('tok-rv', ?, ?, 'grant-revoke', ?, ?, NULL, NULL, NULL, ?)`,
		hashSecret("tok-rv-plain"), env.publicID, iso(env.clock.Now()), iso(env.clock.Now().Add(time.Hour)), iso(env.clock.Now()))

	t.Run("invalid client", func(t *testing.T) {
		rec := env.postForm(t, "/oauth/revoke", map[string]string{"token": "tok-rv-plain"}, nil)
		assertOAuthError(t, rec, http.StatusUnauthorized, "invalid_client", "Client 认证失败")
	})
	t.Run("missing token", func(t *testing.T) {
		rec := env.postForm(t, "/oauth/revoke", map[string]string{"client_id": env.publicID}, nil)
		assertOAuthError(t, rec, http.StatusUnauthorized, "invalid_client", "Client 认证失败")
	})
	t.Run("revokes and answers 200 with empty body", func(t *testing.T) {
		rec := env.postForm(t, "/oauth/revoke", map[string]string{"client_id": env.publicID, "token": "tok-rv-plain"}, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
		}
		if rec.Body.Len() != 0 {
			t.Fatalf("body = %q, want empty", rec.Body.String())
		}
		if context, err := env.store.FindAccessTokenContext(nil, "tok-rv-plain"); err != nil || context != nil {
			t.Fatalf("revoked token still resolvable = %+v", context)
		}
	})
	t.Run("revoking again stays 200 (idempotent)", func(t *testing.T) {
		rec := env.postForm(t, "/oauth/revoke", map[string]string{"client_id": env.publicID, "token": "tok-rv-plain"}, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
	})
	t.Run("other client token untouched", func(t *testing.T) {
		mustExec(t, env.db, `INSERT INTO oauth_access_tokens (id, token_hash, client_id, grant_id, issued_at, expires_at, revoked_at, replaced_at, successor_token_id, created_at)
			VALUES ('tok-rv2', ?, ?, 'grant-revoke', ?, ?, NULL, NULL, NULL, ?)`,
			hashSecret("tok-rv2-plain"), env.confID, iso(env.clock.Now()), iso(env.clock.Now().Add(time.Hour)), iso(env.clock.Now()))
		rec := env.postForm(t, "/oauth/revoke", map[string]string{"client_id": env.publicID, "token": "tok-rv2-plain"}, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		if rows := countRows(t, env.db, "oauth_access_tokens WHERE id = 'tok-rv2' AND revoked_at IS NULL"); rows != 1 {
			t.Fatal("foreign token was revoked")
		}
	})
}

// ---------------------------------------------------------------------------
// GET /oauth/userinfo.
// ---------------------------------------------------------------------------

func TestUserinfoEndpoint(t *testing.T) {
	env := newRouteEnv(t)
	env.get(t, "/oauth/jwks", nil)
	seedToken := func(token, scopes string) {
		grantID := "grant-ui-" + token
		mustExec(t, env.db, `INSERT INTO oauth_grants (id, client_id, system_account_id, scopes_json, expires_at, revoked_at, created_at)
			VALUES (?, ?, ?, ?, ?, NULL, ?)`,
			grantID, env.publicID, env.accountID, scopes, iso(env.clock.Now().Add(24*time.Hour)), iso(env.clock.Now()))
		mustExec(t, env.db, `INSERT INTO oauth_access_tokens (id, token_hash, client_id, grant_id, issued_at, expires_at, revoked_at, replaced_at, successor_token_id, created_at)
			VALUES (?, ?, ?, ?, ?, ?, NULL, NULL, NULL, ?)`,
			"tok-"+token, hashSecret(token), env.publicID, grantID, iso(env.clock.Now()), iso(env.clock.Now().Add(time.Hour)), iso(env.clock.Now()))
	}
	userinfo := func(token string) *httptest.ResponseRecorder {
		headers := map[string]string{}
		if token != "" {
			headers["Authorization"] = "Bearer " + token
		}
		return env.get(t, "/oauth/userinfo", headers)
	}

	t.Run("missing bearer token", func(t *testing.T) {
		assertOAuthError(t, userinfo(""), http.StatusUnauthorized, "invalid_token", "访问令牌无效")
	})
	t.Run("unknown bearer token", func(t *testing.T) {
		assertOAuthError(t, userinfo("tok-ghost"), http.StatusUnauthorized, "invalid_token", "访问令牌无效")
	})
	t.Run("insufficient scope without openid", func(t *testing.T) {
		seedToken("noid", `["juhe:profile.read"]`)
		assertOAuthError(t, userinfo("noid"), http.StatusForbidden, "insufficient_scope", "访问令牌未包含 openid scope")
	})
	t.Run("openid-only claims", func(t *testing.T) {
		seedToken("oid", `["openid"]`)
		rec := userinfo("oid")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
		}
		wantSub, _ := OidcSubjectForSystemAccount(oidcTestSecret, oidcTestIssuer, env.accountID)
		want, _ := json.Marshal(map[string]string{"sub": wantSub})
		if got := strings.TrimSpace(rec.Body.String()); got != string(want) {
			t.Fatalf("claims = %s, want %s", got, string(want))
		}
	})
	t.Run("openid+profile claims", func(t *testing.T) {
		seedToken("prof", `["openid","profile"]`)
		rec := userinfo("prof")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		wantSub, _ := OidcSubjectForSystemAccount(oidcTestSecret, oidcTestIssuer, env.accountID)
		payload := decodeJSONBody(t, rec)
		if stringField(t, payload, "sub") != wantSub || stringField(t, payload, "name") != "Alice" || stringField(t, payload, "preferred_username") != "alice" {
			t.Fatalf("claims = %v", payload)
		}
	})
	t.Run("inactive account is filtered at token lookup", func(t *testing.T) {
		// The token lookup INNER JOIN already requires accounts.status =
		// 'active', so a dead account yields the generic invalid_token
		// contract (Node behaves identically). The narrower "访问令牌对应用户
		// 无效" branch is only reachable if the account flips between the
		// lookup and the profile read — pinned as unreachable here.
		mustExec(t, env.db, `INSERT INTO system_accounts (id, username, display_name, role, status, password_hash, created_at, updated_at)
			VALUES ('acc-ghost', 'ghost', 'Ghost', 'user', 'disabled', 'hash', ?, ?)`,
			iso(env.clock.Now()), iso(env.clock.Now()))
		mustExec(t, env.db, `INSERT INTO oauth_grants (id, client_id, system_account_id, scopes_json, expires_at, revoked_at, created_at)
			VALUES ('grant-ui-ghost', ?, 'acc-ghost', '["openid"]', ?, NULL, ?)`,
			env.publicID, iso(env.clock.Now().Add(24*time.Hour)), iso(env.clock.Now()))
		mustExec(t, env.db, `INSERT INTO oauth_access_tokens (id, token_hash, client_id, grant_id, issued_at, expires_at, revoked_at, replaced_at, successor_token_id, created_at)
			VALUES ('tok-ghostrow', ?, ?, 'grant-ui-ghost', ?, ?, NULL, NULL, NULL, ?)`,
			hashSecret("tok-ghostrow"), env.publicID, iso(env.clock.Now()), iso(env.clock.Now().Add(time.Hour)), iso(env.clock.Now()))
		assertOAuthError(t, userinfo("tok-ghostrow"), http.StatusUnauthorized, "invalid_token", "访问令牌无效")
	})
}

// ---------------------------------------------------------------------------
// Rate limiting across the mounted router.
// ---------------------------------------------------------------------------

func TestRouterRateLimitContract(t *testing.T) {
	env := newRouteEnv(t)
	env.get(t, "/oauth/jwks", nil)
	// The token class allows 30 requests per 60s; the 31st is a 429.
	var rec *httptest.ResponseRecorder
	for i := 0; i < 31; i++ {
		rec = env.postForm(t, "/oauth/token", map[string]string{
			"client_id": env.publicID, "grant_type": "authorization_code", "code": "x",
			"redirect_uri": env.publicRedirect, "code_verifier": pkceTestVerifier,
		}, nil)
	}
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Retry-After"); got == "" {
		t.Fatal("Retry-After missing")
	}
	assertOAuthError(t, rec, http.StatusTooManyRequests, "slow_down", "OAuth 请求过于频繁，请稍后重试")

	// After the window the endpoint recovers (the unknown code then yields
	// the exchange-miss contract).
	env.clock.Advance(61 * time.Second)
	rec = env.postForm(t, "/oauth/token", map[string]string{
		"client_id": env.publicID, "grant_type": "authorization_code", "code": "x",
		"redirect_uri": env.publicRedirect, "code_verifier": pkceTestVerifier,
	}, nil)
	assertOAuthError(t, rec, http.StatusBadRequest, "invalid_grant", "授权码无效、已过期或已使用")
}

func TestRouterServesFullConsentLoop(t *testing.T) {
	env := newRouteEnv(t)
	env.get(t, "/oauth/jwks", nil)
	// End-to-end: authorize → decision issues a code bound to the consent and
	// the code itself is redeemable (Node lifetimes: code 120s, grant 7d).
	code := env.completeAuthorize(t, env.authorizeQuery(nil))
	tokens := env.exchangeCode(t, code)
	accessToken := stringField(t, tokens, "access_token")
	rec := env.get(t, "/oauth/userinfo", map[string]string{"Authorization": "Bearer " + accessToken})
	if rec.Code != http.StatusOK {
		t.Fatalf("userinfo status = %d, body=%s", rec.Code, rec.Body.String())
	}
	payload := decodeJSONBody(t, rec)
	if stringField(t, payload, "name") != "Alice" {
		t.Fatalf("userinfo claims = %v", payload)
	}
	// Form-body edge: an oversized body (>32kb) fails Node's express.urlencoded
	// with a 413 error that the router error middleware maps to 500
	// server_error "OAuth 服务暂时不可用" (Go mirrors that mapping in
	// formBody/writeRouteError).
	huge := strings.Repeat("a", 33*1024)
	rec = env.do(t, http.MethodPost, "/oauth/token", map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, "client_id="+huge)
	assertOAuthError(t, rec, http.StatusInternalServerError, "server_error", "OAuth 服务暂时不可用")
}

// TestFreshDeploymentServesFullChain pins the fix 2 acceptance run: a fresh
// empty key table must bootstrap the first signing key on the first protocol
// request and then serve discovery → jwks → authorize → decision → token →
// userinfo end to end (Node behavior; the former ErrNoRows leak answered 503
// on every leg).
func TestFreshDeploymentServesFullChain(t *testing.T) {
	env := newRouteEnv(t)
	env.clearSigningKeys(t)

	rec := env.get(t, "/.well-known/openid-configuration", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("discovery status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if rows := countRows(t, env.db, "oauth_signing_keys"); rows != 1 {
		t.Fatalf("discovery bootstrap key rows = %d, want 1", rows)
	}

	rec = env.get(t, "/oauth/jwks", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("jwks status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if keys := decodeJSONBody(t, rec)["keys"].([]any); len(keys) != 1 {
		t.Fatalf("jwks keys = %d, want 1", len(keys))
	}

	code := env.completeAuthorize(t, env.authorizeQuery(nil))
	tokens := env.exchangeCode(t, code)
	accessToken := stringField(t, tokens, "access_token")
	if accessToken == "" {
		t.Fatal("no access token issued")
	}
	if stringField(t, tokens, "token_type") != "Bearer" {
		t.Fatalf("token_type = %v", tokens["token_type"])
	}

	rec = env.get(t, "/oauth/userinfo", map[string]string{"Authorization": "Bearer " + accessToken})
	if rec.Code != http.StatusOK {
		t.Fatalf("userinfo status = %d, body=%s", rec.Code, rec.Body.String())
	}
	payload := decodeJSONBody(t, rec)
	if stringField(t, payload, "sub") == "" || stringField(t, payload, "preferred_username") != "alice" {
		t.Fatalf("userinfo claims = %v", payload)
	}
}

// TestAuthorizeDecisionLoginRequiredAfterConsume pins the Node second session
// check of POST /oauth/authorize/decision: after consuming the transaction,
// a vanished browser session answers 401 login_required "请先登录" (the first
// check still passed). The SQLite AFTER UPDATE trigger mocks the session
// disappearing mid-request, between the two lookups.
func TestAuthorizeDecisionLoginRequiredAfterConsume(t *testing.T) {
	env := newRouteEnv(t)
	env.get(t, "/oauth/jwks", nil)
	transactionID, csrfToken := env.startAuthorize(t, env.authorizeQuery(nil))
	mustExec(t, env.db, `CREATE TRIGGER drop_sessions_after_consume
		AFTER UPDATE ON oauth_authorization_transactions
		BEGIN
			DELETE FROM system_sessions;
		END`)
	rec := env.postForm(t, "/oauth/authorize/decision", map[string]string{
		"transaction_id": transactionID, "csrf_token": csrfToken, "decision": "allow",
	}, sessionCookie(env.sessionToken))
	assertOAuthError(t, rec, http.StatusUnauthorized, "login_required", "请先登录")
	// The session was dropped by the trigger; no code may exist.
	if rows := countRows(t, env.db, "oauth_authorization_codes"); rows != 0 {
		t.Fatalf("authorization codes issued despite login_required: %d", rows)
	}
}

// routes.go owns the public protocol route family ported from
// oidc-provider.routes.ts oauthPublicRouter: discovery, jwks, authorize
// (browser consent), device flow, token, renewal, revocation and userinfo.
// Error/render contracts (OAuth error JSON, HTML consent pages, Chinese
// descriptions) are verbatim.
package oidc

import (
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// resourceScopes mirror the juhe: scope list of oidc-provider.routes.ts.
var resourceScopes = []string{
	"juhe:profile.read", "juhe:profile.write",
	"juhe:groups.read", "juhe:groups.write",
	"juhe:route_strategies.read", "juhe:route_strategies.write",
	"juhe:api_keys.read", "juhe:api_keys.write",
	"juhe:ai_accounts.read", "juhe:ai_accounts.write",
	"juhe:request_limits.read",
}

// SupportedScopes = oidcScopes + resourceScopes.
var SupportedScopes = append([]string{"openid", "profile"}, resourceScopes...)

var requiredReadScopeByWriteScope = [][2]string{
	{"juhe:profile.write", "juhe:profile.read"},
	{"juhe:groups.write", "juhe:groups.read"},
	{"juhe:route_strategies.write", "juhe:route_strategies.read"},
	{"juhe:api_keys.write", "juhe:api_keys.read"},
	{"juhe:ai_accounts.write", "juhe:ai_accounts.read"},
}

const deviceCodeGrantType = "urn:ietf:params:oauth:grant-type:device_code"

// Deps bundles the P04 collaborators plus the runtimeConfig.oidc flags.
type Deps struct {
	Store *Store
	// Limiter is the shared protocol limiter; nil disables rate limiting.
	Limiter *ProtocolRateLimiter
	// OIDCEnabled mirrors runtimeConfig.oidc.enabled.
	OIDCEnabled bool
	// OIDCIssuer mirrors runtimeConfig.oidc.issuer.
	OIDCIssuer string
	Now        func() time.Time
}

// Mount wires the public protocol surface (root-level paths, exactly like
// Node's app.use(oauthPublicRouter)).
func (d *Deps) Mount(k *kernel.Kernel) {
	limiter := d.Limiter
	if limiter == nil {
		limiter = NewProtocolRateLimiter(d.Now)
	}
	guard := func(pattern string, class string, handler http.HandlerFunc) {
		wrapped := limiter.Middleware(func(r *http.Request) string {
			if class != "" {
				return class
			}
			return OAuthEndpointClass(pathOnly(r.URL.Path))
		})(d.ensureKey(handler))
		k.Register(pattern, wrapped)
	}
	guard("GET /.well-known/openid-configuration", "", d.getDiscovery)
	guard("GET /oauth/jwks", "", d.getJwks)
	guard("GET /oauth/authorize", "authorize", d.getAuthorize)
	guard("POST /oauth/authorize/decision", "decision", d.postAuthorizeDecision)
	guard("POST /oauth/device_authorization", "authorize", d.postDeviceAuthorization)
	guard("GET /oauth/device", "authorize", d.getDevice)
	guard("POST /oauth/device/decision", "decision", d.postDeviceDecision)
	guard("POST /oauth/token", "token", d.postToken)
	guard("POST /oauth/token/renew", "token", d.postTokenRenew)
	guard("POST /oauth/revoke", "token", d.postRevoke)
	guard("GET /oauth/userinfo", "userinfo", d.getUserinfo)
}

// ensureKey mirrors the router-level middleware: lazily rotate the signing
// key on every OIDC protocol request (503 when the key cannot be produced).
func (d *Deps) ensureKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isOidcProtocolRequest(pathOnly(r.URL.Path)) || !d.OIDCEnabled || d.OIDCIssuer == "" {
			next.ServeHTTP(w, r)
			return
		}
		if _, err := d.Store.EnsureSigningKey(r.Context()); err != nil {
			d.oidcUnavailable(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func pathOnly(path string) string {
	if index := strings.IndexByte(path, '?'); index >= 0 {
		return path[:index]
	}
	return path
}

func isOidcProtocolRequest(path string) bool {
	return path == "/.well-known/openid-configuration" || path == "/oauth" || strings.HasPrefix(path, "/oauth/")
}

// ---------------------------------------------------------------------------
// Shared response helpers.
// ---------------------------------------------------------------------------

type oauthErrorBody struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func oauthError(errorCode, description string) oauthErrorBody {
	return oauthErrorBody{Error: errorCode, ErrorDescription: description}
}

func (d *Deps) oidcUnavailable(w http.ResponseWriter) {
	kernel.WriteJSON(w, http.StatusServiceUnavailable, oauthError("temporarily_unavailable", "OIDC 签名密钥未配置或不可用"))
}

func (d *Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// OAuthRouteError mirrors the Node class: an OAuth JSON error with status.
type OAuthRouteError struct {
	Code       string
	Message    string
	StatusCode int
}

func (e *OAuthRouteError) Error() string { return e.Message }

// OidcUnavailableError mirrors the Node class (503 through the error mapper).
type OidcUnavailableError struct{}

func (e *OidcUnavailableError) Error() string { return "OIDC 签名密钥未配置或不可用" }

// writeRouteError mirrors the end-of-router error middleware.
func (d *Deps) writeRouteError(w http.ResponseWriter, err error) {
	var unavailable *OidcUnavailableError
	if errors.As(err, &unavailable) {
		d.oidcUnavailable(w)
		return
	}
	var route *OAuthRouteError
	if errors.As(err, &route) {
		kernel.WriteJSON(w, route.StatusCode, oauthError(route.Code, route.Message))
		return
	}
	kernel.WriteJSON(w, http.StatusInternalServerError, oauthError("server_error", "OAuth 服务暂时不可用"))
}

func secondsUntil(expiresAt string, now time.Time) (int, error) {
	expiresAtMs, err := requiredTimestampMS(expiresAt)
	if err != nil {
		return 0, err
	}
	remaining := (expiresAtMs - now.UnixMilli()) / 1_000
	if remaining < 0 {
		return 0, nil
	}
	return int(remaining), nil
}

// ---------------------------------------------------------------------------
// Discovery + JWKS.
// ---------------------------------------------------------------------------

func (d *Deps) getDiscovery(w http.ResponseWriter, r *http.Request) {
	if !d.OIDCEnabled || d.OIDCIssuer == "" {
		kernel.WriteError(w, http.StatusNotFound, "OIDC Provider 未启用")
		return
	}
	key, err := d.Store.FindActiveSigningKey(r.Context())
	if err != nil || key == nil {
		d.oidcUnavailable(w)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	issuer := d.OIDCIssuer
	kernel.WriteJSON(w, http.StatusOK, map[string]any{
		"issuer":                                issuer,
		"authorization_endpoint":                issuer + "/oauth/authorize",
		"token_endpoint":                        issuer + "/oauth/token",
		"userinfo_endpoint":                     issuer + "/oauth/userinfo",
		"jwks_uri":                              issuer + "/oauth/jwks",
		"device_authorization_endpoint":         issuer + "/oauth/device_authorization",
		"revocation_endpoint":                   issuer + "/oauth/revoke",
		"juhe_token_renewal_endpoint":           issuer + "/oauth/token/renew",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", deviceCodeGrantType},
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "none"},
		"code_challenge_methods_supported":      []string{"S256"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"subject_types_supported":               []string{"public"},
		"claims_supported":                      []string{"sub", "name", "preferred_username"},
		"scopes_supported":                      SupportedScopes,
	})
}

func (d *Deps) getJwks(w http.ResponseWriter, r *http.Request) {
	if !d.OIDCEnabled {
		kernel.WriteError(w, http.StatusNotFound, "OIDC Provider 未启用")
		return
	}
	key, err := d.Store.FindActiveSigningKey(r.Context())
	if err != nil || key == nil {
		d.oidcUnavailable(w)
		return
	}
	keys, err := d.Store.ListSigningJwks(r.Context())
	if err != nil {
		d.writeRouteError(w, err)
		return
	}
	if len(keys) == 0 {
		d.oidcUnavailable(w)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	kernel.WriteJSON(w, http.StatusOK, map[string]any{"keys": keys})
}

// ---------------------------------------------------------------------------
// Client authentication helpers.
// ---------------------------------------------------------------------------

// basicAuthParts decodes the Basic Authorization header; ok requires a
// non-empty id before the first colon (Node separator < 1 → false).
func basicAuthParts(r *http.Request) (string, string, bool) {
	authorization := r.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, "Basic ") {
		return "", "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(authorization[len("Basic "):]))
	if err != nil {
		return "", "", false
	}
	text := string(decoded)
	separator := strings.IndexByte(text, ':')
	if separator < 1 {
		return "", "", false
	}
	return text[:separator], text[separator+1:], true
}

// clientIDFromTokenRequest mirrors clientIdFromTokenRequest.
func clientIDFromTokenRequest(r *http.Request, body url.Values) string {
	if strings.HasPrefix(r.Header.Get("Authorization"), "Basic ") {
		id, _, ok := basicAuthParts(r)
		if !ok {
			return ""
		}
		return id
	}
	return body.Get("client_id")
}

// authenticateClient mirrors authenticateClient.
func (d *Deps) authenticateClient(r *http.Request, client *Client) bool {
	if client == nil || client.Status != "active" {
		return false
	}
	if client.ClientType == "public" {
		return r.Header.Get("Authorization") == ""
	}
	authorization := r.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, "Basic ") || client.ClientSecretHash == nil {
		return false
	}
	id, secret, ok := basicAuthParts(r)
	if !ok || id != client.ClientID {
		return false
	}
	expected := *client.ClientSecretHash
	actual := hashSecret(secret)
	return subtle.ConstantTimeCompare([]byte(actual), []byte(expected)) == 1
}

// normalizeScopes mirrors normalizeScopes (whitespace split, dedupe).
func normalizeScopes(value string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, scope := range strings.FieldsFunc(value, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\r' }) {
		scope = strings.TrimSpace(scope)
		if scope == "" || seen[scope] {
			continue
		}
		seen[scope] = true
		out = append(out, scope)
	}
	return out
}

// hasRequiredReadScopes mirrors hasRequiredReadScopes.
func hasRequiredReadScopes(scopes []string) bool {
	granted := map[string]bool{}
	for _, scope := range scopes {
		granted[scope] = true
	}
	for _, pair := range requiredReadScopeByWriteScope {
		if granted[pair[0]] && !granted[pair[1]] {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Authorize (browser consent).
// ---------------------------------------------------------------------------

var codeChallengePattern = regexp.MustCompile(`^[A-Za-z0-9\-_]{43,128}$`)
var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func stringQuery(values url.Values, name string) string { return values.Get(name) }

func (d *Deps) createAuthorizationRequest(r *http.Request, query url.Values) (*AuthorizationTransaction, error) {
	if query.Get("response_type") != "code" ||
		query.Get("client_id") == "" ||
		!isValidURLString(query.Get("redirect_uri")) ||
		query.Get("scope") == "" ||
		query.Get("state") == "" || len(query.Get("state")) > 1024 ||
		!codeChallengePattern.MatchString(query.Get("code_challenge")) ||
		query.Get("code_challenge_method") != "S256" ||
		(len(query.Get("nonce")) > 0 && (len(query.Get("nonce")) < 1 || len(query.Get("nonce")) > 1024)) {
		return nil, &OAuthRouteError{Code: "invalid_request", Message: "授权请求参数无效", StatusCode: http.StatusBadRequest}
	}
	client, err := d.Store.FindClient(r.Context(), query.Get("client_id"))
	if err != nil {
		return nil, err
	}
	if client == nil || client.Status != "active" || !matchesRegisteredRedirectUri(client.RedirectUris, query.Get("redirect_uri")) {
		return nil, &OAuthRouteError{Code: "invalid_request", Message: "Client 或回调地址无效", StatusCode: http.StatusBadRequest}
	}
	scopes := normalizeScopes(query.Get("scope"))
	for _, scope := range scopes {
		if !containsScope(client.AllowedScopes, scope) {
			return nil, &OAuthRouteError{Code: "invalid_scope", Message: "请求的 scope 未登记", StatusCode: http.StatusBadRequest}
		}
	}
	if containsScope(scopes, "profile") && !containsScope(scopes, "openid") {
		return nil, &OAuthRouteError{Code: "invalid_scope", Message: "请求的 scope 未登记", StatusCode: http.StatusBadRequest}
	}
	if !hasRequiredReadScopes(scopes) {
		return nil, &OAuthRouteError{Code: "invalid_scope", Message: "请求的 scope 未登记", StatusCode: http.StatusBadRequest}
	}
	if containsScope(scopes, "openid") && query.Get("nonce") == "" {
		return nil, &OAuthRouteError{Code: "invalid_request", Message: "请求 openid scope 时必须提供 nonce", StatusCode: http.StatusBadRequest}
	}
	return d.Store.CreateAuthorizationTransaction(r.Context(), struct {
		ClientID      string
		RedirectURI   string
		Scopes        []string
		State         string
		CodeChallenge string
		Nonce         string
	}{
		ClientID: client.ClientID, RedirectURI: query.Get("redirect_uri"), Scopes: scopes,
		State: query.Get("state"), CodeChallenge: query.Get("code_challenge"), Nonce: query.Get("nonce"),
	})
}

func (d *Deps) getAuthorize(w http.ResponseWriter, r *http.Request) {
	if !d.OIDCEnabled {
		kernel.WriteError(w, http.StatusNotFound, "OIDC Provider 未启用")
		return
	}
	key, err := d.Store.FindActiveSigningKey(r.Context())
	if err != nil || key == nil {
		d.oidcUnavailable(w)
		return
	}
	query := r.URL.Query()
	var transaction *AuthorizationTransaction
	transactionID := stringQuery(query, "transaction_id")
	if transactionID != "" {
		transaction, err = d.Store.FindAuthorizationTransaction(r.Context(), transactionID)
		if err != nil {
			d.writeRouteError(w, err)
			return
		}
	} else {
		transaction, err = d.createAuthorizationRequest(r, query)
		if err != nil {
			d.writeRouteError(w, err)
			return
		}
	}
	if transaction == nil {
		kernel.WriteJSON(w, http.StatusBadRequest, oauthError("invalid_request", "授权请求不存在或已过期"))
		return
	}
	client, err := d.Store.FindClient(r.Context(), transaction.ClientID)
	if err != nil {
		d.writeRouteError(w, err)
		return
	}
	if client == nil || client.Status != "active" || !matchesRegisteredRedirectUri(client.RedirectUris, transaction.RedirectURI) {
		kernel.WriteJSON(w, http.StatusBadRequest, oauthError("invalid_request", "Client 或回调地址无效"))
		return
	}
	session, err := d.browserSession(r)
	if err != nil {
		d.writeRouteError(w, err)
		return
	}
	if session == nil {
		http.Redirect(w, r, "/__aisys__/login?redirect="+url.QueryEscape("/oauth/authorize?transaction_id="+transaction.ID), http.StatusFound)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, consentHTML(client.DisplayName, transaction.ID, transaction.CSRFToken, transaction.Scopes))
}

func (d *Deps) postAuthorizeDecision(w http.ResponseWriter, r *http.Request) {
	if !d.OIDCEnabled {
		kernel.WriteError(w, http.StatusNotFound, "OIDC Provider 未启用")
		return
	}
	key, err := d.Store.FindActiveSigningKey(r.Context())
	if err != nil || key == nil {
		d.oidcUnavailable(w)
		return
	}
	body := formBody(r)
	transactionID := body.Get("transaction_id")
	csrfToken := body.Get("csrf_token")
	decision := body.Get("decision")
	if transactionID == "" || !uuidPattern.MatchString(transactionID) ||
		csrfToken == "" || (decision != "allow" && decision != "deny") {
		kernel.WriteJSON(w, http.StatusBadRequest, oauthError("invalid_request", "授权确认请求无效"))
		return
	}
	session, err := d.browserSession(r)
	if err != nil {
		d.writeRouteError(w, err)
		return
	}
	if session == nil {
		kernel.WriteJSON(w, http.StatusBadRequest, oauthError("invalid_request", "授权确认请求无效"))
		return
	}
	transaction, err := d.Store.ConsumeAuthorizationTransaction(r.Context(), transactionID, csrfToken)
	if err != nil {
		d.writeRouteError(w, err)
		return
	}
	if transaction == nil {
		kernel.WriteJSON(w, http.StatusBadRequest, oauthError("invalid_request", "授权事务无效或已处理"))
		return
	}
	if decision == "deny" {
		d.redirectWithError(w, r, transaction.RedirectURI, transaction.State, "access_denied")
		return
	}
	code, err := d.Store.CreateAuthorizationCode(r.Context(), struct {
		ClientID        string
		SystemAccountID string
		Scopes          []string
		RedirectURI     string
		CodeChallenge   string
		Nonce           string
	}{
		ClientID: transaction.ClientID, SystemAccountID: session.AccountID, Scopes: transaction.Scopes,
		RedirectURI: transaction.RedirectURI, CodeChallenge: transaction.CodeChallenge, Nonce: transaction.Nonce,
	})
	if err != nil {
		d.writeRouteError(w, err)
		return
	}
	target, err := url.Parse(transaction.RedirectURI)
	if err != nil {
		d.writeRouteError(w, err)
		return
	}
	callback := target.Query()
	callback.Set("code", code)
	callback.Set("state", transaction.State)
	target.RawQuery = callback.Encode()
	http.Redirect(w, r, target.String(), http.StatusFound)
}

// browserSession mirrors browserSession(req) — cookie → session lookup.
func (d *Deps) browserSession(r *http.Request) (*BrowserSession, error) {
	cookies := parseCookieHeader(r.Header.Get("Cookie"))
	token := cookies[authsysSessionCookieName]
	if token == "" {
		return nil, nil
	}
	return d.Store.FindSessionByToken(r.Context(), token)
}

const authsysSessionCookieName = "juhe_ai_session"

func parseCookieHeader(header string) map[string]string {
	result := map[string]string{}
	for _, part := range strings.Split(header, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		pieces := strings.SplitN(part, "=", 2)
		if len(pieces) != 2 || pieces[0] == "" {
			continue
		}
		result[pieces[0]] = pieces[1]
	}
	return result
}

func (d *Deps) redirectWithError(w http.ResponseWriter, r *http.Request, redirectURI, state, errorCode string) {
	target, err := url.Parse(redirectURI)
	if err != nil {
		d.writeRouteError(w, err)
		return
	}
	query := target.Query()
	query.Set("error", errorCode)
	query.Set("state", state)
	target.RawQuery = query.Encode()
	http.Redirect(w, r, target.String(), http.StatusFound)
}

// ---------------------------------------------------------------------------
// Device flow.
// ---------------------------------------------------------------------------

func (d *Deps) postDeviceAuthorization(w http.ResponseWriter, r *http.Request) {
	if !d.OIDCEnabled || d.OIDCIssuer == "" {
		kernel.WriteError(w, http.StatusNotFound, "OIDC Provider 未启用")
		return
	}
	key, err := d.Store.FindActiveSigningKey(r.Context())
	if err != nil || key == nil {
		d.oidcUnavailable(w)
		return
	}
	body := formBody(r)
	clientID := clientIDFromTokenRequest(r, body)
	var client *Client
	if clientID != "" {
		client, err = d.Store.FindClient(r.Context(), clientID)
		if err != nil {
			d.writeRouteError(w, err)
			return
		}
	}
	if client == nil || !d.authenticateClient(r, client) {
		kernel.WriteJSON(w, http.StatusUnauthorized, oauthError("invalid_client", "Client 认证失败"))
		return
	}
	scopes := normalizeScopes(body.Get("scope"))
	nonce := strings.TrimSpace(body.Get("nonce"))
	if len(scopes) == 0 {
		kernel.WriteJSON(w, http.StatusBadRequest, oauthError("invalid_scope", "请求的 scope 未登记"))
		return
	}
	for _, scope := range scopes {
		if !containsScope(client.AllowedScopes, scope) {
			kernel.WriteJSON(w, http.StatusBadRequest, oauthError("invalid_scope", "请求的 scope 未登记"))
			return
		}
	}
	if containsScope(scopes, "profile") && !containsScope(scopes, "openid") {
		kernel.WriteJSON(w, http.StatusBadRequest, oauthError("invalid_scope", "请求的 scope 未登记"))
		return
	}
	if !hasRequiredReadScopes(scopes) {
		kernel.WriteJSON(w, http.StatusBadRequest, oauthError("invalid_scope", "请求的 scope 未登记"))
		return
	}
	if containsScope(scopes, "openid") && (nonce == "" || len(nonce) > 1024) {
		kernel.WriteJSON(w, http.StatusBadRequest, oauthError("invalid_request", "请求 openid scope 时必须提供 nonce"))
		return
	}
	verificationURI := d.OIDCIssuer + "/oauth/device"
	authorization, deviceCode, err := d.Store.CreateDeviceAuthorization(r.Context(), struct {
		ClientID        string
		Scopes          []string
		Nonce           string
		VerificationURI string
	}{ClientID: client.ClientID, Scopes: scopes, Nonce: nonce, VerificationURI: verificationURI})
	if err != nil {
		d.writeRouteError(w, err)
		return
	}
	expiresIn, err := secondsUntil(authorization.ExpiresAt, d.now())
	if err != nil {
		d.writeRouteError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	kernel.WriteJSON(w, http.StatusOK, map[string]any{
		"device_code":                deviceCode,
		"user_code":                  authorization.UserCode,
		"verification_uri":           verificationURI,
		"verification_uri_complete":  verificationURI + "?user_code=" + url.QueryEscape(authorization.UserCode),
		"expires_in":                 expiresIn,
		"interval":                   authorization.IntervalSeconds,
	})
}

func (d *Deps) getDevice(w http.ResponseWriter, r *http.Request) {
	if !d.OIDCEnabled {
		kernel.WriteError(w, http.StatusNotFound, "OIDC Provider 未启用")
		return
	}
	key, err := d.Store.FindActiveSigningKey(r.Context())
	if err != nil || key == nil {
		d.oidcUnavailable(w)
		return
	}
	userCode := stringQuery(r.URL.Query(), "user_code")
	if userCode == "" {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, deviceCodeEntryHTML)
		return
	}
	session, err := d.browserSession(r)
	if err != nil {
		d.writeRouteError(w, err)
		return
	}
	if session == nil {
		http.Redirect(w, r, "/__aisys__/login?redirect="+url.QueryEscape("/oauth/device?user_code="+url.QueryEscape(userCode)), http.StatusFound)
		return
	}
	prepared, csrfToken, err := d.Store.PrepareDeviceAuthorization(r.Context(), userCode)
	if err != nil {
		d.writeRouteError(w, err)
		return
	}
	if prepared == nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, deviceErrorHTML("设备授权码无效、已过期或已处理"))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, deviceConsentHTML(prepared, csrfToken))
}

func (d *Deps) postDeviceDecision(w http.ResponseWriter, r *http.Request) {
	if !d.OIDCEnabled {
		kernel.WriteError(w, http.StatusNotFound, "OIDC Provider 未启用")
		return
	}
	key, err := d.Store.FindActiveSigningKey(r.Context())
	if err != nil || key == nil {
		d.oidcUnavailable(w)
		return
	}
	body := formBody(r)
	userCode := strings.TrimSpace(body.Get("user_code"))
	csrfToken := body.Get("csrf_token")
	decision := body.Get("decision")
	session, err := d.browserSession(r)
	if err != nil {
		d.writeRouteError(w, err)
		return
	}
	if userCode == "" || len(userCode) > 64 || csrfToken == "" || (decision != "allow" && decision != "deny") || session == nil {
		kernel.WriteJSON(w, http.StatusBadRequest, oauthError("invalid_request", "设备授权确认请求无效"))
		return
	}
	decided, err := d.Store.DecideDeviceAuthorization(r.Context(), userCode, csrfToken, session.AccountID, decision)
	if err != nil {
		d.writeRouteError(w, err)
		return
	}
	if decided == nil {
		kernel.WriteJSON(w, http.StatusBadRequest, oauthError("invalid_request", "设备授权码无效、已过期或已处理"))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, deviceCompleteHTML(decision))
}

// ---------------------------------------------------------------------------
// Token / renewal / revocation.
// ---------------------------------------------------------------------------

func (d *Deps) postToken(w http.ResponseWriter, r *http.Request) {
	if !d.OIDCEnabled {
		kernel.WriteError(w, http.StatusNotFound, "OIDC Provider 未启用")
		return
	}
	signingKey, err := d.Store.FindActiveSigningKey(r.Context())
	if err != nil {
		d.writeRouteError(w, err)
		return
	}
	if signingKey == nil || d.OIDCIssuer == "" {
		d.oidcUnavailable(w)
		return
	}
	body := formBody(r)
	clientID := clientIDFromTokenRequest(r, body)
	var client *Client
	if clientID != "" {
		client, err = d.Store.FindClient(r.Context(), clientID)
		if err != nil {
			d.writeRouteError(w, err)
			return
		}
	}
	if client == nil || !d.authenticateClient(r, client) {
		kernel.WriteJSON(w, http.StatusUnauthorized, oauthError("invalid_client", "Client 认证失败"))
		return
	}
	grantType := body.Get("grant_type")
	if grantType == deviceCodeGrantType {
		deviceCode := body.Get("device_code")
		if deviceCode == "" {
			kernel.WriteJSON(w, http.StatusBadRequest, oauthError("invalid_request", "device_code 参数无效"))
			return
		}
		requestsIDToken, err := d.Store.DeviceAuthorizationRequestsIdToken(r.Context(), client.ClientID, deviceCode)
		if err != nil {
			d.writeRouteError(w, err)
			return
		}
		if requestsIDToken {
			if err := d.signingPreflight(r, signingKey); err != nil {
				d.oidcUnavailable(w)
				return
			}
		}
		polled, err := d.Store.PollDeviceAuthorization(r.Context(), client.ClientID, deviceCode)
		if err != nil {
			d.writeRouteError(w, err)
			return
		}
		if polled.Kind != PollApproved {
			errorCode := string(polled.Kind)
			switch polled.Kind {
			case PollInvalid, PollInvalidGrant:
				errorCode = "invalid_grant"
			case PollExpired:
				errorCode = "expired_token"
			}
			kernel.WriteJSON(w, http.StatusBadRequest, oauthError(errorCode, devicePollDescription(errorCode)))
			return
		}
		idToken, err := d.maybeIssueIDToken(r, &polled.Context, polled.Nonce)
		if err != nil {
			d.writeRouteError(w, err)
			return
		}
		d.sendTokenResponse(w, polled.AccessToken, &polled.Context, idToken)
		return
	}
	code := body.Get("code")
	redirectURI := body.Get("redirect_uri")
	codeVerifier := body.Get("code_verifier")
	if grantType != "authorization_code" || code == "" || redirectURI == "" || codeVerifier == "" {
		kernel.WriteJSON(w, http.StatusBadRequest, oauthError("invalid_grant", "授权码参数无效"))
		return
	}
	requestsIDToken, err := d.Store.AuthorizationCodeRequestsIdToken(r.Context(), client.ClientID, code)
	if err != nil {
		d.writeRouteError(w, err)
		return
	}
	if requestsIDToken {
		if err := d.signingPreflight(r, signingKey); err != nil {
			d.oidcUnavailable(w)
			return
		}
	}
	issued, err := d.Store.ExchangeAuthorizationCode(r.Context(), client.ClientID, code, redirectURI, codeVerifier)
	if err != nil {
		d.writeRouteError(w, err)
		return
	}
	if issued == nil {
		kernel.WriteJSON(w, http.StatusBadRequest, oauthError("invalid_grant", "授权码无效、已过期或已使用"))
		return
	}
	idToken, err := d.maybeIssueIDToken(r, &issued.Context, issued.Nonce)
	if err != nil {
		d.writeRouteError(w, err)
		return
	}
	d.sendTokenResponse(w, issued.AccessToken, &issued.Context, idToken)
}

func (d *Deps) signingPreflight(r *http.Request, signingKey *SigningKey) error {
	return AssertSigningKeyUsable(d.Store.KeyEncryptionSecret, signingKey.PrivateKeyCiphertext,
		signingKey.Kid, d.OIDCIssuer, d.now())
}

// maybeIssueIdToken mirrors maybeIssueIdToken.
func (d *Deps) maybeIssueIDToken(r *http.Request, context *AccessTokenContext, nonce string) (string, error) {
	if !containsScope(context.Scopes, "openid") {
		return "", nil
	}
	signingKey, err := d.Store.FindActiveSigningKey(r.Context())
	if err != nil {
		return "", err
	}
	if signingKey == nil || d.OIDCIssuer == "" {
		return "", &OidcUnavailableError{}
	}
	expiresAtMs, err := requiredTimestampMS(context.ExpiresAt)
	if err != nil {
		return "", err
	}
	if cap := d.now().UnixMilli() + 5*60*1_000; expiresAtMs > cap {
		expiresAtMs = cap
	}
	subject, err := OidcSubjectForSystemAccount(d.Store.KeyEncryptionSecret, d.OIDCIssuer, context.SystemAccountID)
	if err != nil {
		return "", err
	}
	return SignIDToken(d.Store.KeyEncryptionSecret, signingKey.PrivateKeyCiphertext, signingKey.Kid,
		d.OIDCIssuer, context.ClientID, subject, expiresAtMs/1_000, nonce, d.now())
}

func (d *Deps) sendTokenResponse(w http.ResponseWriter, accessToken string, context *AccessTokenContext, idToken string) {
	expiresIn, err := secondsUntil(context.ExpiresAt, d.now())
	if err != nil {
		d.writeRouteError(w, err)
		return
	}
	response := map[string]any{
		"access_token": accessToken,
		"token_type":   "Bearer",
		"expires_in":   expiresIn,
		"scope":        strings.Join(context.Scopes, " "),
	}
	if idToken != "" {
		response["id_token"] = idToken
	}
	w.Header().Set("Cache-Control", "no-store")
	kernel.WriteJSON(w, http.StatusOK, response)
}

func devicePollDescription(errorCode string) string {
	switch errorCode {
	case "authorization_pending":
		return "用户尚未完成设备授权确认"
	case "slow_down":
		return "设备轮询过于频繁，请降低频率"
	case "expired_token":
		return "设备码已过期"
	case "access_denied":
		return "用户拒绝了设备授权"
	default:
		return "设备码无效或已使用"
	}
}

func (d *Deps) postTokenRenew(w http.ResponseWriter, r *http.Request) {
	if !d.OIDCEnabled {
		kernel.WriteError(w, http.StatusNotFound, "OIDC Provider 未启用")
		return
	}
	key, err := d.Store.FindActiveSigningKey(r.Context())
	if err != nil || key == nil {
		d.oidcUnavailable(w)
		return
	}
	body := formBody(r)
	currentAccessToken := body.Get("current_access_token")
	clientID := clientIDFromTokenRequest(r, body)
	var client *Client
	if clientID != "" {
		client, err = d.Store.FindClient(r.Context(), clientID)
		if err != nil {
			d.writeRouteError(w, err)
			return
		}
	}
	if client == nil || currentAccessToken == "" || !d.authenticateClient(r, client) {
		kernel.WriteJSON(w, http.StatusUnauthorized, oauthError("invalid_client", "Client 认证失败"))
		return
	}
	renewed, notEligible, err := d.Store.RotateAccessToken(r.Context(), client.ClientID, currentAccessToken)
	if err != nil {
		d.writeRouteError(w, err)
		return
	}
	if notEligible {
		kernel.WriteJSON(w, http.StatusBadRequest, oauthError("token_renewal_not_eligible", "当前令牌签发未满 72 小时"))
		return
	}
	if renewed == nil {
		kernel.WriteJSON(w, http.StatusBadRequest, oauthError("invalid_token", "令牌无效或授权已到期"))
		return
	}
	expiresIn, err := secondsUntil(renewed.Context.ExpiresAt, d.now())
	if err != nil {
		d.writeRouteError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	kernel.WriteJSON(w, http.StatusOK, map[string]any{
		"access_token": renewed.AccessToken,
		"token_type":   "Bearer",
		"expires_in":   expiresIn,
		"scope":        strings.Join(renewed.Context.Scopes, " "),
	})
}

func (d *Deps) postRevoke(w http.ResponseWriter, r *http.Request) {
	if !d.OIDCEnabled {
		kernel.WriteError(w, http.StatusNotFound, "OIDC Provider 未启用")
		return
	}
	key, err := d.Store.FindActiveSigningKey(r.Context())
	if err != nil || key == nil {
		d.oidcUnavailable(w)
		return
	}
	body := formBody(r)
	token := body.Get("token")
	clientID := clientIDFromTokenRequest(r, body)
	var client *Client
	if clientID != "" {
		client, err = d.Store.FindClient(r.Context(), clientID)
		if err != nil {
			d.writeRouteError(w, err)
			return
		}
	}
	if client == nil || token == "" || !d.authenticateClient(r, client) {
		kernel.WriteJSON(w, http.StatusUnauthorized, oauthError("invalid_client", "Client 认证失败"))
		return
	}
	if err := d.Store.RevokeAccessToken(r.Context(), token, client.ClientID); err != nil {
		d.writeRouteError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// ---------------------------------------------------------------------------
// Userinfo.
// ---------------------------------------------------------------------------

func (d *Deps) getUserinfo(w http.ResponseWriter, r *http.Request) {
	if !d.OIDCEnabled {
		kernel.WriteError(w, http.StatusNotFound, "OIDC Provider 未启用")
		return
	}
	key, err := d.Store.FindActiveSigningKey(r.Context())
	if err != nil || key == nil {
		d.oidcUnavailable(w)
		return
	}
	authorization := r.Header.Get("Authorization")
	var token string
	if strings.HasPrefix(authorization, "Bearer ") {
		token = strings.TrimSpace(authorization[len("Bearer "):])
	}
	var context *AccessTokenContext
	if token != "" {
		context, err = d.Store.FindAccessTokenContext(r.Context(), token)
		if err != nil {
			d.writeRouteError(w, err)
			return
		}
	}
	if context == nil {
		kernel.WriteJSON(w, http.StatusUnauthorized, oauthError("invalid_token", "访问令牌无效"))
		return
	}
	if !containsScope(context.Scopes, "openid") {
		kernel.WriteJSON(w, http.StatusForbidden, oauthError("insufficient_scope", "访问令牌未包含 openid scope"))
		return
	}
	account, err := d.Store.FindSystemAccountProfile(r.Context(), context.SystemAccountID)
	if err != nil {
		d.writeRouteError(w, err)
		return
	}
	if account == nil {
		kernel.WriteJSON(w, http.StatusUnauthorized, oauthError("invalid_token", "访问令牌对应用户无效"))
		return
	}
	subject, err := OidcSubjectForSystemAccount(d.Store.KeyEncryptionSecret, d.OIDCIssuer, account.AccountID)
	if err != nil {
		d.writeRouteError(w, err)
		return
	}
	claims := map[string]string{"sub": subject}
	if containsScope(context.Scopes, "profile") {
		claims["name"] = account.DisplayName
		claims["preferred_username"] = account.Username
	}
	w.Header().Set("Cache-Control", "no-store")
	kernel.WriteJSON(w, http.StatusOK, claims)
}

// ---------------------------------------------------------------------------
// Form parsing (express.urlencoded extended:false, limit 32kb).
// ---------------------------------------------------------------------------

func formBody(r *http.Request) url.Values {
	contentType := strings.ToLower(r.Header.Get("Content-Type"))
	if !strings.Contains(contentType, "application/x-www-form-urlencoded") {
		return url.Values{}
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 32*1024+1))
	if err != nil || len(body) > 32*1024 {
		return url.Values{}
	}
	values, err := url.ParseQuery(string(body))
	if err != nil {
		return url.Values{}
	}
	return values
}

// isValidURLString mirrors z.string().url() for the authorize redirect_uri.
func isValidURLString(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme != "" && parsed.Host != ""
}

// matchesRegisteredRedirectUri mirrors matchesRegisteredRedirectUri: exact
// match, or a loopback http match on hostname+path+search.
func matchesRegisteredRedirectUri(registeredUris []string, requestedURI string) bool {
	for _, uri := range registeredUris {
		if uri == requestedURI {
			return true
		}
	}
	requested, err := url.Parse(requestedURI)
	if err != nil {
		return false
	}
	if requested.Scheme != "http" || !isLoopbackHostname(requested.Hostname()) ||
		requested.Fragment != "" || requested.User != nil {
		return false
	}
	for _, registeredRaw := range registeredUris {
		registered, err := url.Parse(registeredRaw)
		if err != nil {
			continue
		}
		if registered.Scheme == requested.Scheme &&
			isLoopbackHostname(registered.Hostname()) &&
			registered.Hostname() == requested.Hostname() &&
			registered.Path == requested.Path &&
			registered.RawQuery == requested.RawQuery &&
			registered.Fragment == "" &&
			registered.User == nil {
			return true
		}
	}
	return false
}

func isLoopbackHostname(hostname string) bool {
	return hostname == "127.0.0.1" || hostname == "::1" || hostname == "[::1]"
}

// ---------------------------------------------------------------------------
// HTML views (verbatim Node consent/device pages).
// ---------------------------------------------------------------------------

func escapeHTML(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;")
	return replacer.Replace(value)
}

func scopeListHTML(scopes []string) string {
	items := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		items = append(items, "<li>"+escapeHTML(scope)+"</li>")
	}
	return strings.Join(items, "")
}

func consentHTML(displayName, transactionID, csrfToken string, scopes []string) string {
	return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><title>授权确认</title><meta name="viewport" content="width=device-width,initial-scale=1"></head><body><main><h1>授权确认</h1><p>应用 <strong>` + escapeHTML(displayName) + `</strong> 请求访问你的 juhe-ai 个人资源。</p><ul>` + scopeListHTML(scopes) + `</ul><form method="post" action="/oauth/authorize/decision"><input type="hidden" name="transaction_id" value="` + escapeHTML(transactionID) + `"><input type="hidden" name="csrf_token" value="` + escapeHTML(csrfToken) + `"><button name="decision" value="allow" type="submit">允许</button><button name="decision" value="deny" type="submit">拒绝</button></form></main></body></html>`
}

const deviceCodeEntryHTML = `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><title>设备授权</title><meta name="viewport" content="width=device-width,initial-scale=1"></head><body><main><h1>设备授权</h1><form method="get" action="/oauth/device"><label>设备码 <input name="user_code" autocomplete="one-time-code" required></label><button type="submit">继续</button></form></main></body></html>`

func deviceConsentHTML(authorization *DeviceAuthorization, csrfToken string) string {
	return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><title>设备授权确认</title><meta name="viewport" content="width=device-width,initial-scale=1"></head><body><main><h1>设备授权确认</h1><p>设备码 <strong>` + escapeHTML(authorization.UserCode) + `</strong> 请求访问你的 juhe-ai 个人资源。</p><ul>` + scopeListHTML(authorization.Scopes) + `</ul><form method="post" action="/oauth/device/decision"><input type="hidden" name="user_code" value="` + escapeHTML(authorization.UserCode) + `"><input type="hidden" name="csrf_token" value="` + escapeHTML(csrfToken) + `"><button name="decision" value="allow" type="submit">允许</button><button name="decision" value="deny" type="submit">拒绝</button></form></main></body></html>`
}

func deviceCompleteHTML(decision string) string {
	message := "设备授权已拒绝。"
	if decision == "allow" {
		message = "设备已获授权，你可以回到设备继续。"
	}
	return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><title>设备授权完成</title></head><body><main><p>` + message + `</p></main></body></html>`
}

func deviceErrorHTML(message string) string {
	return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><title>设备授权</title></head><body><main><p>` + escapeHTML(message) + `</p></main></body></html>`
}

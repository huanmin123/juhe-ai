package oauthmgmt

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/url"
	"strings"
)

// Grok OAuth constants mirror grok-oauth.service.ts.
const (
	GrokOAuthAuthorizeURL = "https://auth.x.ai/oauth2/authorize"
	GrokOAuthTokenURL     = "https://auth.x.ai/oauth2/token"
	GrokOAuthClientID     = "b1a00492-073a-47ea-816f-4c329264a828"
	GrokOAuthScope        = "openid profile email offline_access grok-cli:access api:access"
	GrokOAuthRedirectURI  = "http://127.0.0.1:56121/callback"
	GrokOAuthBaseURL      = "https://cli-chat-proxy.grok.com/v1"
	grokSessionNamespace  = "grok-oauth:sessions"
	grokDefaultTokenTTL   = 6 * 60 * 60 // seconds
)

// grokOAuthSession mirrors GrokOAuthSession.
type grokOAuthSession struct {
	State                string `json:"state"`
	Nonce                string `json:"nonce"`
	CodeVerifier         string `json:"codeVerifier"`
	CodeChallenge        string `json:"codeChallenge"`
	ClientID             string `json:"clientId"`
	Scope                string `json:"scope"`
	RedirectURI          string `json:"redirectUri"`
	OwnerSystemAccountID string `json:"ownerSystemAccountId,omitempty"`
}

// grokOAuthError mirrors GrokOAuthError: a client-facing OAuth failure with an
// explicit HTTP status (400 renders badRequest, others pass through).
type grokOAuthError struct {
	Message    string
	StatusCode int
}

func (e *grokOAuthError) Error() string { return e.Message }

// generateGrokAuthURL mirrors generateGrokAuthURL. Payload shape:
// {authUrl, sessionId, state}.
func (s *Store) generateGrokAuthURL(ownerID string) (map[string]any, error) {
	state := randomHex(32)
	nonce := randomHex(16)
	codeVerifier := randomBase64URL(32)
	codeChallenge := pkceS256(codeVerifier)
	sessionID := randomHex(16)
	session := grokOAuthSession{
		State:                state,
		Nonce:                nonce,
		CodeVerifier:         codeVerifier,
		CodeChallenge:        codeChallenge,
		ClientID:             GrokOAuthClientID,
		Scope:                GrokOAuthScope,
		RedirectURI:          GrokOAuthRedirectURI,
		OwnerSystemAccountID: ownerID,
	}
	s.sessions.set(grokSessionNamespace, sessionID, session, oauthSessionTTL)
	return map[string]any{
		"authUrl":   buildGrokAuthorizeURL(state, nonce, codeChallenge),
		"sessionId": sessionID,
		"state":     state,
	}, nil
}

// buildGrokAuthorizeURL mirrors buildGrokAuthorizeUrl.
func buildGrokAuthorizeURL(state, nonce, codeChallenge string) string {
	params := map[string]string{
		"response_type":         "code",
		"client_id":             GrokOAuthClientID,
		"redirect_uri":          GrokOAuthRedirectURI,
		"scope":                 GrokOAuthScope,
		"state":                 state,
		"nonce":                 nonce,
		"code_challenge":        codeChallenge,
		"code_challenge_method": "S256",
		"plan":                  "generic",
		"referrer":              "sub2api",
	}
	return GrokOAuthAuthorizeURL + "?" + encodeForm(params)
}

// grokTokenInfo mirrors GrokOAuthTokenInfo.
type grokTokenInfo struct {
	AccessToken       string
	RefreshToken      string
	IDToken           string
	TokenType         string
	ExpiresIn         int
	ExpiresAt         string
	ClientID          string
	Scope             string
	Email             string
	Subject           string
	TeamID            string
	SubscriptionTier  string
	EntitlementStatus string
}

// grokAuthorization mirrors parseGrokAuthorizationInput output.
type grokAuthorization struct {
	code          string
	state         string
	requiresState bool
}

// parseGrokAuthorizationInput mirrors parseGrokAuthorizationInput: URL form,
// then "?..."/"a=b" query form, then the bare code accepted by the xAI CLI
// flow.
func parseGrokAuthorizationInput(raw string) (*grokAuthorization, error) {
	trimmed := normalizeText(raw)
	if trimmed == "" {
		return &grokAuthorization{}, nil
	}
	if parsed, parseErr := url.Parse(trimmed); parseErr == nil && parsed.Scheme != "" && parsed.Host != "" {
		query := parsed.Query()
		if errorCode := normalizeText(query.Get("error")); errorCode != "" {
			detail := normalizeText(query.Get("error_description"))
			if detail == "" {
				detail = errorCode
			}
			return nil, &UpstreamError{Message: detail, StatusCode: 502}
		}
		if code := normalizeText(query.Get("code")); code != "" {
			return &grokAuthorization{
				code:          code,
				state:         normalizeText(query.Get("state")),
				requiresState: true,
			}, nil
		}
	}
	queryCandidate := strings.TrimPrefix(trimmed, "?")
	if strings.Contains(queryCandidate, "=") {
		query, err := url.ParseQuery(queryCandidate)
		if err == nil {
			if errorCode := normalizeText(query.Get("error")); errorCode != "" {
				detail := normalizeText(query.Get("error_description"))
				if detail == "" {
					detail = errorCode
				}
				return nil, &UpstreamError{Message: detail, StatusCode: 502}
			}
			if code := normalizeText(query.Get("code")); code != "" {
				return &grokAuthorization{
					code:          code,
					state:         normalizeText(query.Get("state")),
					requiresState: true,
				}, nil
			}
		}
	}
	return &grokAuthorization{code: trimmed}, nil
}

// exchangeGrokAuthorizationCode mirrors exchangeGrokAuthCode.
func (s *Store) exchangeGrokAuthorizationCode(ctx context.Context, sessionID, callbackURL, ownerID string) (*grokTokenInfo, error) {
	authorization, err := parseGrokAuthorizationInput(callbackURL)
	if err != nil {
		return nil, err
	}
	if authorization.code == "" {
		return nil, &grokOAuthError{Message: "Grok OAuth 授权码不能为空", StatusCode: 400}
	}
	raw := s.sessions.get(grokSessionNamespace, sessionID)
	if raw == nil {
		return nil, &grokOAuthError{Message: "Grok OAuth 会话不存在或已过期", StatusCode: 400}
	}
	var session grokOAuthSession
	if err := unmarshalSession(raw, &session); err != nil {
		return nil, &grokOAuthError{Message: "Grok OAuth 会话不存在或已过期", StatusCode: 400}
	}
	if authorization.requiresState && authorization.state == "" {
		return nil, &grokOAuthError{Message: "Grok OAuth 回调缺少 state", StatusCode: 400}
	}
	if authorization.state != "" && !constantTimeEqual(authorization.state, session.State) {
		return nil, &grokOAuthError{Message: "Grok OAuth state 无效", StatusCode: 400}
	}
	if session.OwnerSystemAccountID != "" && normalizeText(ownerID) != session.OwnerSystemAccountID {
		return nil, &grokOAuthError{Message: "Grok OAuth session owner 归属无效", StatusCode: 400}
	}
	info, err := s.requestGrokToken(ctx, map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     session.ClientID,
		"code":          authorization.code,
		"redirect_uri":  session.RedirectURI,
		"code_verifier": session.CodeVerifier,
	}, session.ClientID)
	if err != nil {
		return nil, err
	}
	if !s.sessions.compareDelete(grokSessionNamespace, sessionID, session) {
		return nil, &grokOAuthError{Message: "Grok OAuth 会话已消费，请重新发起授权", StatusCode: 400}
	}
	return info, nil
}

// refreshGrokToken mirrors refreshGrokAuthToken: a missing rotated refresh
// token keeps the input one.
func (s *Store) refreshGrokToken(ctx context.Context, refreshToken, clientID string) (*grokTokenInfo, error) {
	refreshToken = normalizeText(refreshToken)
	if refreshToken == "" {
		return nil, &grokOAuthError{Message: "Grok Refresh Token 不能为空", StatusCode: 400}
	}
	if normalizeText(clientID) == "" {
		clientID = GrokOAuthClientID
	}
	info, err := s.requestGrokToken(ctx, map[string]string{
		"grant_type":    "refresh_token",
		"client_id":     clientID,
		"refresh_token": refreshToken,
	}, clientID)
	if err != nil {
		return nil, err
	}
	if info.RefreshToken == "" {
		info.RefreshToken = refreshToken
	}
	return info, nil
}

// requestGrokToken mirrors requestGrokToken.
func (s *Store) requestGrokToken(ctx context.Context, form map[string]string, clientID string) (*grokTokenInfo, error) {
	request := formRequest(GrokOAuthTokenURL, form)
	request.Headers["user-agent"] = "sub2api-grok-oauth/1.0"
	response, err := s.exchange(ctx, request)
	if err != nil {
		return nil, err
	}
	payload := parseTokenPayload(response.Body)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail := normalizeText(payload["error_description"])
		if detail == "" {
			detail = normalizeText(payload["error"])
		}
		if detail == "" {
			detail = response.Body
		}
		statusCode := 502
		if response.StatusCode == 403 && hasExplicitEntitlementDenial(payload, response.Body) {
			statusCode = 403
		}
		return nil, &UpstreamError{
			Message:    "Grok OAuth 令牌请求失败：HTTP " + itoa(response.StatusCode) + "，" + detail,
			StatusCode: statusCode,
		}
	}
	accessToken := normalizeText(payload["access_token"])
	if accessToken == "" {
		return nil, errors.New("Grok OAuth 令牌响应缺少 access_token")
	}
	expiresIn := grokDefaultTokenTTL
	if value, ok := finitePositiveInt(payload["expires_in"]); ok {
		expiresIn = value
	}
	tokenType := normalizeText(payload["token_type"])
	if tokenType == "" {
		tokenType = "Bearer"
	}
	return toGrokTokenInfo(grokRawToken{
		AccessToken:  accessToken,
		RefreshToken: normalizeText(payload["refresh_token"]),
		IDToken:      normalizeText(payload["id_token"]),
		TokenType:    tokenType,
		ExpiresIn:    expiresIn,
		Scope:        normalizeText(payload["scope"]),
	}, clientID, func() int64 { return s.now().UnixMilli() }), nil
}

// grokRawToken mirrors the raw payload crossing service/SSO boundaries.
type grokRawToken struct {
	AccessToken  string
	RefreshToken string
	IDToken      string
	TokenType    string
	ExpiresIn    int
	Scope        string
}

// toGrokTokenInfo mirrors toGrokOAuthTokenInfo: merged id/access JWT claims.
func toGrokTokenInfo(payload grokRawToken, clientID string, now func() int64) *grokTokenInfo {
	expiresIn := payload.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = grokDefaultTokenTTL
	}
	claims := mergeJWTClaims(payload.IDToken, payload.AccessToken)
	tokenType := payload.TokenType
	if tokenType == "" {
		tokenType = "Bearer"
	}
	if normalizeText(clientID) == "" {
		clientID = GrokOAuthClientID
	}
	claim := func(key string) string { return normalizeText(claims[key]) }
	return &grokTokenInfo{
		AccessToken:       payload.AccessToken,
		RefreshToken:      payload.RefreshToken,
		IDToken:           payload.IDToken,
		TokenType:         tokenType,
		ExpiresIn:         expiresIn,
		ExpiresAt:         isoFromMillis(now() + int64(expiresIn)*1000),
		ClientID:          clientID,
		Scope:             payload.Scope,
		Email:             claim("email"),
		Subject:           claim("sub"),
		TeamID:            claim("team_id"),
		SubscriptionTier:  claim("subscription_tier"),
		EntitlementStatus: claim("entitlement_status"),
	}
}

// hasExplicitEntitlementDenial mirrors hasExplicitEntitlementDenial: 403s only
// when the body names an entitlement/subscription denial.
func hasExplicitEntitlementDenial(payload map[string]any, body string) bool {
	denials := map[string]bool{
		"access_denied":          true,
		"entitlement_denied":     true,
		"subscription_required":  true,
		"no_active_subscription": true,
	}
	for _, key := range []string{"error", "code", "reason"} {
		if denials[strings.ToLower(normalizeText(payload[key]))] {
			return true
		}
	}
	lower := strings.ToLower(body)
	return strings.Contains(lower, "entitlement denied") ||
		strings.Contains(lower, "subscription required") ||
		strings.Contains(lower, "no active grok subscription")
}

// mergeJWTClaims mirrors mergeJwtClaims: earlier tokens win for defined values.
func mergeJWTClaims(tokens ...string) map[string]any {
	output := map[string]any{}
	for _, token := range tokens {
		for key, value := range decodeJWTClaims(token) {
			if existing, ok := output[key]; ok {
				if text := normalizeText(existing); text != "" {
					continue
				}
			}
			output[key] = value
		}
	}
	return output
}

// constantTimeEqual mirrors constantTimeEqual (timingSafeEqual over bytes,
// length-guarded).
func constantTimeEqual(actual, expected string) bool {
	if len(actual) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(actual), []byte(expected)) == 1
}

// buildGrokOAuthCredentials mirrors buildGrokOAuthCredentials.
func buildGrokOAuthCredentials(info *grokTokenInfo, fallbackRefreshToken string) map[string]any {
	credentials := map[string]any{
		"access_token": info.AccessToken,
		"expires_at":   info.ExpiresAt,
		"token_type":   info.TokenType,
		"client_id":    info.ClientID,
		"base_url":     GrokOAuthBaseURL,
	}
	refreshToken := normalizeText(info.RefreshToken)
	if refreshToken == "" {
		refreshToken = normalizeText(fallbackRefreshToken)
	}
	if refreshToken != "" {
		credentials["refresh_token"] = refreshToken
	}
	if info.IDToken != "" {
		credentials["id_token"] = info.IDToken
	}
	if info.Scope != "" {
		credentials["scope"] = info.Scope
	}
	if info.Email != "" {
		credentials["email"] = info.Email
	}
	if info.Subject != "" {
		credentials["sub"] = info.Subject
	}
	if info.TeamID != "" {
		credentials["team_id"] = info.TeamID
	}
	if info.SubscriptionTier != "" {
		credentials["subscription_tier"] = info.SubscriptionTier
	}
	if info.EntitlementStatus != "" {
		credentials["entitlement_status"] = info.EntitlementStatus
	}
	return credentials
}

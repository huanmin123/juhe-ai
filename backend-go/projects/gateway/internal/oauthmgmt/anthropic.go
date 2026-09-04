package oauthmgmt

import (
	"context"
	"errors"
	"net/url"
	"strings"
)

// Anthropic OAuth constants mirror anthropic-oauth.service.ts.
const (
	AnthropicOAuthClientID     = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	AnthropicOAuthAuthorizeURL = "https://claude.ai/oauth/authorize"
	AnthropicOAuthTokenURL     = "https://platform.claude.com/v1/oauth/token"
	AnthropicOAuthRedirectURI  = "https://platform.claude.com/oauth/code/callback"
	AnthropicOAuthBrowserScope = "org:create_api_key user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload"
	anthropicOAuthBaseURL      = "https://api.anthropic.com/v1"
	anthropicSessionNamespace  = "anthropic-oauth:sessions"
)

// anthropicOAuthSession mirrors AnthropicOAuthSession.
type anthropicOAuthSession struct {
	State                string `json:"state"`
	CodeVerifier         string `json:"codeVerifier"`
	Scope                string `json:"scope"`
	RedirectURI          string `json:"redirectUri"`
	ClientID             string `json:"clientId"`
	OwnerSystemAccountID string `json:"ownerSystemAccountId,omitempty"`
}

// generateAnthropicAuthURL mirrors generateAnthropicAuthURL. Payload shape:
// {authUrl, sessionId}.
func (s *Store) generateAnthropicAuthURL(ownerID string) (map[string]any, error) {
	state := randomHex(32)
	codeVerifier := randomBase64URL(32)
	codeChallenge := pkceS256(codeVerifier)
	sessionID := randomHex(16)
	session := anthropicOAuthSession{
		State:                state,
		CodeVerifier:         codeVerifier,
		Scope:                AnthropicOAuthBrowserScope,
		RedirectURI:          AnthropicOAuthRedirectURI,
		ClientID:             AnthropicOAuthClientID,
		OwnerSystemAccountID: ownerID,
	}
	s.sessions.set(anthropicSessionNamespace, sessionID, session, oauthSessionTTL)
	return map[string]any{
		"authUrl":   buildAnthropicAuthorizeURL(session.ClientID, session.RedirectURI, state, codeChallenge, AnthropicOAuthBrowserScope),
		"sessionId": sessionID,
	}, nil
}

// buildAnthropicAuthorizeURL mirrors buildAnthropicAuthorizeUrl (note the
// leading literal `code: 'true'` parameter).
func buildAnthropicAuthorizeURL(clientID, redirectURI, state, codeChallenge, scope string) string {
	params := map[string]string{
		"code":                  "true",
		"client_id":             clientID,
		"response_type":         "code",
		"redirect_uri":          redirectURI,
		"scope":                 scope,
		"code_challenge":        codeChallenge,
		"code_challenge_method": "S256",
		"state":                 state,
	}
	return AnthropicOAuthAuthorizeURL + "?" + encodeForm(params)
}

// anthropicTokenInfo mirrors AnthropicOAuthTokenInfo.
type anthropicTokenInfo struct {
	AccessToken    string
	RefreshToken   string
	ExpiresIn      int
	ExpiresAt      string
	Email          string
	AccountID      string
	OrganizationID string
	Scope          string
	TokenType      string
	ClientID       string
}

// anthropicAuthorization mirrors extractCodeAndState output.
type anthropicAuthorization struct {
	code          string
	state         string
	requiresState bool
}

// extractAnthropicCodeAndState mirrors extractCodeAndState: URL or query forms
// require state, a bare code string does not.
func extractAnthropicCodeAndState(callbackURL string) (*anthropicAuthorization, error) {
	value := normalizeText(callbackURL)
	if value == "" {
		return nil, errors.New("Anthropic 授权结果不能为空")
	}
	authorization := &anthropicAuthorization{}
	if parsed, parseErr := url.Parse(value); parseErr == nil && parsed.Scheme != "" && parsed.Host != "" {
		authorization.requiresState = true
		query := parsed.Query()
		if errorCode := normalizeText(query.Get("error")); errorCode != "" {
			detail := normalizeText(query.Get("error_description"))
			if detail == "" {
				detail = errorCode
			}
			return nil, &UpstreamError{Message: detail, StatusCode: 502}
		}
		authorization.code = normalizeText(query.Get("code"))
		authorization.state = normalizeText(query.Get("state"))
	}
	if authorization.code == "" || authorization.state == "" {
		if index := strings.LastIndex(value, "#"); index > 0 {
			authorization.requiresState = true
			authorization.code = normalizeText(value[:index])
			authorization.state = normalizeText(value[index+1:])
		} else if strings.Contains(value, "=") {
			authorization.requiresState = true
			query := parseQueryValues(value)
			authorization.code = normalizeText(query.Get("code"))
			authorization.state = normalizeText(query.Get("state"))
		} else {
			authorization.code = value
		}
	}
	if authorization.code == "" || (authorization.requiresState && authorization.state == "") {
		return nil, errors.New("Anthropic 授权结果必须包含 code，URL 或查询形式还必须包含 state")
	}
	return authorization, nil
}

// exchangeAnthropicAuthorizationCode mirrors exchangeAnthropicAuthCode.
func (s *Store) exchangeAnthropicAuthorizationCode(ctx context.Context, sessionID, callbackURL, ownerID string) (*anthropicTokenInfo, error) {
	authorization, err := extractAnthropicCodeAndState(callbackURL)
	if err != nil {
		return nil, err
	}
	raw := s.sessions.get(anthropicSessionNamespace, sessionID)
	if raw == nil {
		return nil, errors.New("Anthropic OAuth 会话不存在或已过期")
	}
	var session anthropicOAuthSession
	if err := unmarshalSession(raw, &session); err != nil {
		return nil, errors.New("Anthropic OAuth 会话不存在或已过期")
	}
	if authorization.requiresState && authorization.state == "" {
		return nil, errors.New("Anthropic OAuth 回调缺少 state")
	}
	if authorization.state != "" && authorization.state != session.State {
		return nil, errors.New("Anthropic OAuth state 无效")
	}
	if session.OwnerSystemAccountID != "" && normalizeText(ownerID) != session.OwnerSystemAccountID {
		return nil, errors.New("Anthropic OAuth session owner 归属无效")
	}
	info, err := s.requestAnthropicToken(ctx, map[string]string{
		"code":          authorization.code,
		"redirect_uri":  session.RedirectURI,
		"client_id":     session.ClientID,
		"grant_type":    "authorization_code",
		"code_verifier": session.CodeVerifier,
		"state":         authorization.state,
	})
	if err != nil {
		return nil, err
	}
	if authorization.state == "" {
		authorization.state = session.State
	}
	if !s.sessions.compareDelete(anthropicSessionNamespace, sessionID, session) {
		return nil, errors.New("Anthropic OAuth 会话已消费，请重新发起授权")
	}
	return info, nil
}

// refreshAnthropicToken mirrors refreshAnthropicAuthToken.
func (s *Store) refreshAnthropicToken(ctx context.Context, refreshToken, clientID string) (*anthropicTokenInfo, error) {
	refreshToken = normalizeText(refreshToken)
	if refreshToken == "" {
		return nil, errors.New("Anthropic Refresh Token 不能为空")
	}
	if normalizeText(clientID) == "" {
		clientID = AnthropicOAuthClientID
	}
	return s.requestAnthropicToken(ctx, map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
		"client_id":     clientID,
	})
}

// requestAnthropicToken mirrors requestAnthropicToken: JSON body POST with the
// axios user-agent, upstream error envelope, account/organization claim
// extraction.
func (s *Store) requestAnthropicToken(ctx context.Context, form map[string]string) (*anthropicTokenInfo, error) {
	response, err := s.exchange(ctx, jsonRequest(AnthropicOAuthTokenURL, form))
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
		return nil, upstreamError("Anthropic", response.StatusCode, detail)
	}
	accessToken := normalizeText(payload["access_token"])
	if accessToken == "" {
		return nil, errors.New("Anthropic OAuth 令牌响应缺少 access_token")
	}
	account, _ := payload["account"].(map[string]any)
	organization, _ := payload["organization"].(map[string]any)
	expiresIn := 0
	expiresAt := ""
	if value, ok := finitePositiveInt(payload["expires_in"]); ok {
		expiresIn = value
		expiresAt = isoFromMillis(s.now().UnixMilli() + int64(value)*1000)
	}
	clientID := normalizeText(form["client_id"])
	if clientID == "" {
		clientID = AnthropicOAuthClientID
	}
	return &anthropicTokenInfo{
		AccessToken:    accessToken,
		RefreshToken:   normalizeText(payload["refresh_token"]),
		ExpiresIn:      expiresIn,
		ExpiresAt:      expiresAt,
		Email:          normalizeText(account["email_address"]),
		AccountID:      normalizeText(account["uuid"]),
		OrganizationID: normalizeText(organization["uuid"]),
		Scope:          normalizeText(payload["scope"]),
		TokenType:      normalizeText(payload["token_type"]),
		ClientID:       clientID,
	}, nil
}

// buildAnthropicOAuthCredentials mirrors buildAnthropicOAuthCredentials.
func buildAnthropicOAuthCredentials(info *anthropicTokenInfo, fallbackRefreshToken string) map[string]any {
	credentials := map[string]any{
		"access_token": info.AccessToken,
		"base_url":     anthropicOAuthBaseURL,
		"client_id":    info.ClientID,
	}
	refreshToken := normalizeText(info.RefreshToken)
	if refreshToken == "" {
		refreshToken = normalizeText(fallbackRefreshToken)
	}
	if refreshToken != "" {
		credentials["refresh_token"] = refreshToken
	}
	if info.ExpiresAt != "" {
		credentials["expires_at"] = info.ExpiresAt
	}
	if info.Email != "" {
		credentials["email"] = info.Email
	}
	if info.AccountID != "" {
		credentials["account_id"] = info.AccountID
	}
	if info.OrganizationID != "" {
		credentials["organization_id"] = info.OrganizationID
	}
	if info.Scope != "" {
		credentials["scope"] = info.Scope
	}
	if info.TokenType != "" {
		credentials["token_type"] = info.TokenType
	}
	return credentials
}

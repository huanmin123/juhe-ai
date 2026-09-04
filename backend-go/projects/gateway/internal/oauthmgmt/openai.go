package oauthmgmt

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
)

// OpenAI OAuth constants mirror openai-oauth.service.ts.
const (
	OpenAIOAuthClientID         = "app_EMoamEEZ73f0CkXaXp7hrann"
	OpenAIOAuthAuthorizeURL     = "https://auth.openai.com/oauth/authorize"
	OpenAIOAuthTokenURL         = "https://auth.openai.com/oauth/token"
	OpenAIOAuthDefaultRedirect  = "http://localhost:1455/auth/callback"
	OpenAIOAuthDefaultScopes    = "openid profile email offline_access"
	OpenAIOAuthRefreshScopes    = "openid profile email"
	openAIOAuthBaseURL          = "https://api.openai.com/v1"
	openAIOAuthSessionNamespace = "openai-oauth:sessions"
)

// openAIOAuthSession mirrors OpenAIOAuthSession.
type openAIOAuthSession struct {
	State                string `json:"state"`
	CodeVerifier         string `json:"codeVerifier"`
	RedirectURI          string `json:"redirectUri"`
	ClientID             string `json:"clientId"`
	OwnerSystemAccountID string `json:"ownerSystemAccountId,omitempty"`
}

// randomHex mirrors randomBytes(n).toString('hex').
func randomHex(size int) string {
	buf := make([]byte, size)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// randomBase64URL mirrors randomBytes(n).toString('base64url').
func randomBase64URL(size int) string {
	buf := make([]byte, size)
	_, _ = rand.Read(buf)
	return base64.RawURLEncoding.EncodeToString(buf)
}

// pkceS256 mirrors createHash('sha256').update(verifier).digest('base64url').
func pkceS256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// generateOpenAIAuthURL mirrors generateOpenAIAuthURL: state/verifier/session
// material, the stored session and the authorize URL with the Codex CLI
// parameters. The payload shape is {authUrl, sessionId}.
func (s *Store) generateOpenAIAuthURL(ownerID string) (map[string]any, error) {
	state := randomHex(32)
	codeVerifier := randomHex(64)
	codeChallenge := pkceS256(codeVerifier)
	sessionID := randomHex(16)
	session := openAIOAuthSession{
		State:                state,
		CodeVerifier:         codeVerifier,
		RedirectURI:          OpenAIOAuthDefaultRedirect,
		ClientID:             OpenAIOAuthClientID,
		OwnerSystemAccountID: ownerID,
	}
	s.sessions.set(openAIOAuthSessionNamespace, sessionID, session, oauthSessionTTL)
	return map[string]any{
		"authUrl":   buildOpenAIAuthorizeURL(session.ClientID, session.RedirectURI, state, codeChallenge),
		"sessionId": sessionID,
	}, nil
}

// buildOpenAIAuthorizeURL mirrors buildOpenAIOAuthAuthorizeUrl.
func buildOpenAIAuthorizeURL(clientID, redirectURI, state, codeChallenge string) string {
	params := map[string]string{
		"response_type":              "code",
		"client_id":                  clientID,
		"redirect_uri":               redirectURI,
		"scope":                      OpenAIOAuthDefaultScopes,
		"state":                      state,
		"code_challenge":             codeChallenge,
		"code_challenge_method":      "S256",
		"id_token_add_organizations": "true",
		"codex_cli_simplified_flow":  "true",
	}
	return OpenAIOAuthAuthorizeURL + "?" + encodeForm(params)
}

// openAITokenInfo mirrors OpenAITokenInfo.
type openAITokenInfo struct {
	AccessToken   string
	RefreshToken  string
	IDToken       string
	ExpiresIn     int
	ExpiresAt     string
	ClientID      string
	Email         string
	AccountID     string
	ChatGPTUserID string
	PlanType      string
}

// exchangeOpenAIAuthorizationCode mirrors exchangeOpenAIAuthCode: session read
// (state + owner), code exchange and single-consumption session delete.
func (s *Store) exchangeOpenAIAuthorizationCode(ctx context.Context, sessionID, callbackURL, ownerID string) (*openAITokenInfo, error) {
	code, state, err := extractOpenAICodeAndState(callbackURL)
	if err != nil {
		return nil, err
	}
	raw := s.sessions.get(openAIOAuthSessionNamespace, sessionID)
	if raw == nil {
		return nil, errors.New("OAuth 会话不存在或已过期")
	}
	var session openAIOAuthSession
	if err := unmarshalSession(raw, &session); err != nil {
		return nil, errors.New("OAuth 会话不存在或已过期")
	}
	if state == "" || state != session.State {
		return nil, errors.New("OAuth state 无效")
	}
	if session.OwnerSystemAccountID != "" && normalizeText(ownerID) != session.OwnerSystemAccountID {
		return nil, errors.New("OAuth session owner 归属无效")
	}
	info, err := s.requestOpenAIToken(ctx, map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     session.ClientID,
		"code":          code,
		"redirect_uri":  session.RedirectURI,
		"code_verifier": session.CodeVerifier,
	})
	if err != nil {
		return nil, err
	}
	if !s.sessions.compareDelete(openAIOAuthSessionNamespace, sessionID, session) {
		return nil, errors.New("OAuth 会话已消费，请重新发起授权")
	}
	return info, nil
}

// refreshOpenAIToken mirrors refreshOpenAIOAuthToken.
func (s *Store) refreshOpenAIToken(ctx context.Context, refreshToken, clientID string) (*openAITokenInfo, error) {
	refreshToken = normalizeText(refreshToken)
	if refreshToken == "" {
		return nil, errors.New("刷新令牌不能为空")
	}
	if normalizeText(clientID) == "" {
		clientID = OpenAIOAuthClientID
	}
	return s.requestOpenAIToken(ctx, map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
		"client_id":     clientID,
		"scope":         OpenAIOAuthRefreshScopes,
	})
}

// requestOpenAIToken mirrors requestOpenAIToken: form POST, upstream error
// envelope, required access_token/expires_in, JWT claim enrichment.
func (s *Store) requestOpenAIToken(ctx context.Context, form map[string]string) (*openAITokenInfo, error) {
	response, err := s.exchange(ctx, formRequest(OpenAIOAuthTokenURL, form))
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
		return nil, upstreamError("OpenAI", response.StatusCode, detail)
	}
	accessToken := normalizeText(payload["access_token"])
	if accessToken == "" {
		return nil, errors.New("OpenAI OAuth 令牌响应缺少访问令牌")
	}
	expiresIn, ok := finitePositiveInt(payload["expires_in"])
	if !ok {
		return nil, errors.New("OpenAI OAuth 令牌响应的 expires_in 必须是有限正数")
	}
	idToken := normalizeText(payload["id_token"])
	refreshToken := normalizeText(payload["refresh_token"])
	clientID := normalizeText(form["client_id"])
	if clientID == "" {
		clientID = OpenAIOAuthClientID
	}
	idClaims := decodeJWTClaims(idToken)
	accessClaims := decodeJWTClaims(accessToken)
	idAuth, _ := idClaims["https://api.openai.com/auth"].(map[string]any)
	accessAuth, _ := accessClaims["https://api.openai.com/auth"].(map[string]any)
	pick := func(values ...any) string {
		for _, value := range values {
			if text := normalizeText(value); text != "" {
				return text
			}
		}
		return ""
	}
	return &openAITokenInfo{
		AccessToken:   accessToken,
		RefreshToken:  refreshToken,
		IDToken:       idToken,
		ExpiresIn:     expiresIn,
		ExpiresAt:     isoFromMillis(s.now().UnixMilli() + int64(expiresIn)*1000),
		ClientID:      clientID,
		Email:         pick(idClaims["email"], accessClaims["email"]),
		AccountID:     pick(idAuth["chatgpt_account_id"], accessAuth["chatgpt_account_id"]),
		ChatGPTUserID: pick(idAuth["chatgpt_user_id"], idAuth["user_id"], accessAuth["chatgpt_user_id"], accessAuth["user_id"]),
		PlanType:      pick(idAuth["chatgpt_plan_type"], accessAuth["chatgpt_plan_type"]),
	}, nil
}

// buildOpenAIOAuthCredentials mirrors buildOpenAIOAuthCredentials.
func buildOpenAIOAuthCredentials(info *openAITokenInfo, fallbackRefreshToken string) map[string]any {
	credentials := map[string]any{
		"access_token": info.AccessToken,
		"expires_at":   info.ExpiresAt,
		"client_id":    info.ClientID,
		"base_url":     openAIOAuthBaseURL,
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
	if info.Email != "" {
		credentials["email"] = info.Email
	}
	if info.AccountID != "" {
		credentials["account_id"] = info.AccountID
	}
	if info.ChatGPTUserID != "" {
		credentials["chatgpt_user_id"] = info.ChatGPTUserID
	}
	if info.PlanType != "" {
		credentials["plan_type"] = info.PlanType
	}
	return credentials
}

// extractOpenAICodeAndState mirrors extractCodeAndState +
// parseOAuthAuthorizationInput: query form, fragment form, bare "code#state"
// and "?code=..." forms, with upstream error forwarding.
func extractOpenAICodeAndState(callbackURL string) (code, state string, err error) {
	callbackURL = normalizeText(callbackURL)
	if callbackURL == "" {
		return "", "", errors.New("回调 URL 不能为空")
	}
	if parsed, parseErr := url.Parse(callbackURL); parseErr == nil && parsed.Scheme != "" && parsed.Host != "" {
		query := parsed.Query()
		if errorCode := normalizeText(query.Get("error")); errorCode != "" {
			detail := normalizeText(query.Get("error_description"))
			if detail == "" {
				detail = errorCode
			}
			return "", "", &UpstreamError{Message: detail, StatusCode: 502}
		}
		code := normalizeText(query.Get("code"))
		state := normalizeText(query.Get("state"))
		if code == "" && state == "" {
			fragment := parseQueryValues(parsed.Fragment)
			code = normalizeText(fragment["code"])
			state = normalizeText(fragment["state"])
		}
		if code == "" || state == "" {
			return "", "", errors.New("回调 URL 必须包含 code 和 state")
		}
		return code, state, nil
	}
	// Non-URL forms: "code#state" then raw query strings.
	if index := strings.LastIndex(callbackURL, "#"); index > 0 {
		code = normalizeText(callbackURL[:index])
		state = normalizeText(callbackURL[index+1:])
	} else {
		query := parseQueryValues(callbackURL)
		code = normalizeText(query["code"])
		state = normalizeText(query["state"])
	}
	if code == "" || state == "" {
		return "", "", errors.New("回调 URL 必须包含 code 和 state")
	}
	return code, state, nil
}

// parseQueryValues mirrors new URLSearchParams(value): tolerant "?prefix"
// parsing over a raw query string; malformed pairs are skipped.
func parseQueryValues(value string) url.Values {
	values, _ := url.ParseQuery(strings.TrimPrefix(value, "?"))
	return values
}

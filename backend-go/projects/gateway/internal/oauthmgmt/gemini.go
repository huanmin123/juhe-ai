package oauthmgmt

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
)

// Gemini OAuth constants mirror gemini-oauth.service.ts.
const (
	GeminiOAuthAuthorizeURL    = "https://accounts.google.com/o/oauth2/v2/auth"
	GeminiOAuthTokenURL        = "https://oauth2.googleapis.com/token"
	GeminiOAuthRedirectURI     = "http://localhost:1455/auth/callback"
	GeminiCLIOAuthRedirectURI  = "https://codeassist.google.com/authcode"
	GeminiCLIOAuthClientID     = "681255809395-oo8ft2oprdrnp9e3aqf6av3hmdib135j.apps.googleusercontent.com"
	GeminiCLIOAuthClientSecret = "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"
	GeminiOAuthScope           = "https://www.googleapis.com/auth/cloud-platform https://www.googleapis.com/auth/generative-language.retriever"
	GeminiCodeAssistOAuthScope = "https://www.googleapis.com/auth/cloud-platform https://www.googleapis.com/auth/userinfo.email https://www.googleapis.com/auth/userinfo.profile"
	geminiOAuthDefaultBaseURL  = "https://generativelanguage.googleapis.com"
	geminiCLIDefaultBaseURL    = "https://cloudcode-pa.googleapis.com"
	geminiSessionNamespace     = "gemini-oauth:sessions"
)

// geminiCLIEndpointModes mirrors geminiCliEndpointModes.
var geminiCLIEndpointModes = []string{"generate_content_json", "generate_content_sse"}

// geminiOAuthTypes mirrors GEMINI_OAUTH_TYPES.
var geminiOAuthTypes = []string{"code_assist", "google_one", "ai_studio"}

// normalizeGeminiOAuthType mirrors normalizeOAuthType: unknown falls back to
// code_assist.
func normalizeGeminiOAuthType(value string) string {
	value = normalizeText(value)
	for _, candidate := range geminiOAuthTypes {
		if value == candidate {
			return value
		}
	}
	return "code_assist"
}

// geminiOAuthClientConfig mirrors oauthConfigForType.
func geminiOAuthClientConfig(oauthType string) (usesBuiltInClient bool, redirectURI, scope string) {
	if oauthType == "ai_studio" {
		return false, GeminiOAuthRedirectURI, GeminiOAuthScope
	}
	scope = GeminiCodeAssistOAuthScope
	return true, GeminiCLIOAuthRedirectURI, scope
}

// resolveGeminiOAuthClient mirrors resolveGeminiOAuthClient: built-in client
// for code_assist/google_one, request/env credentials for ai_studio.
func resolveGeminiOAuthClient(oauthType, clientID, clientSecret string) (id, secret, redirectURI, scope string, err error) {
	usesBuiltIn, redirect, resolvedScope := geminiOAuthClientConfig(oauthType)
	if usesBuiltIn {
		builtinSecret := normalizeText(os.Getenv("GEMINI_CLI_OAUTH_CLIENT_SECRET"))
		if builtinSecret == "" {
			builtinSecret = GeminiCLIOAuthClientSecret
		}
		return GeminiCLIOAuthClientID, builtinSecret, redirect, resolvedScope, nil
	}
	id = normalizeText(clientID)
	if id == "" {
		id = normalizeText(os.Getenv("GEMINI_OAUTH_CLIENT_ID"))
	}
	secret = normalizeText(clientSecret)
	if secret == "" {
		secret = normalizeText(os.Getenv("GEMINI_OAUTH_CLIENT_SECRET"))
	}
	if id == "" || secret == "" {
		return "", "", "", "", errors.New("Gemini AI Studio OAuth 需要同时配置 Client ID 和 Client Secret")
	}
	return id, secret, redirect, resolvedScope, nil
}

// defaultGeminiBaseURL mirrors defaultBaseUrl.
func defaultGeminiBaseURL(oauthType string) string {
	if oauthType == "ai_studio" {
		return geminiOAuthDefaultBaseURL
	}
	return geminiCLIDefaultBaseURL
}

// canonicalGeminiTierID mirrors canonicalGeminiTierId.
func canonicalGeminiTierID(oauthType, raw string) string {
	value := strings.ToLower(normalizeText(raw))
	value = strings.ReplaceAll(value, "-", "_")
	if value == "" {
		return ""
	}
	contains := func(candidates ...string) bool {
		for _, candidate := range candidates {
			if value == candidate {
				return true
			}
		}
		return false
	}
	switch oauthType {
	case "google_one":
		if contains("ai_premium", "google_ai_pro") {
			return "google_ai_pro"
		}
		if contains("google_one_unlimited", "google_ai_ultra") {
			return "google_ai_ultra"
		}
		if value == "google_one_unknown" {
			return value
		}
		if contains("free", "google_one_basic", "google_one_standard", "google_one_free") {
			return "google_one_free"
		}
		return ""
	case "ai_studio":
		if contains("aistudio_paid", "paid") {
			return "aistudio_paid"
		}
		if contains("aistudio_free", "free") {
			return "aistudio_free"
		}
		return ""
	default:
		if contains("enterprise", "ultra", "gcp_enterprise", "ultra_tier") {
			return "gcp_enterprise"
		}
		if contains("legacy", "standard", "pro", "gcp_standard", "standard_tier", "pro_tier") {
			return "gcp_standard"
		}
		return ""
	}
}

// geminiOAuthSession mirrors GeminiOAuthSession.
type geminiOAuthSession struct {
	State                string `json:"state"`
	CodeVerifier         string `json:"codeVerifier"`
	Scope                string `json:"scope"`
	RedirectURI          string `json:"redirectUri"`
	ClientID             string `json:"clientId"`
	ClientSecret         string `json:"clientSecret"`
	OAuthType            string `json:"oauthType"`
	ProjectID            string `json:"projectId,omitempty"`
	TierID               string `json:"tierId,omitempty"`
	QuotaProjectID       string `json:"quotaProjectId,omitempty"`
	BaseURL              string `json:"baseUrl"`
	OwnerSystemAccountID string `json:"ownerSystemAccountId,omitempty"`
}

// geminiOAuthCapabilities mirrors getGeminiOAuthCapabilities.
func geminiOAuthCapabilities() map[string]any {
	capability := func(oauthType, label string, usesBuiltInClient, supportsProjectID, supportsTierID bool) map[string]any {
		_, redirectURI, scope := geminiOAuthClientConfig(oauthType)
		modes := append([]string{}, geminiCLIEndpointModes...)
		if oauthType == "ai_studio" {
			modes = []string{}
		}
		return map[string]any{
			"oauthType":                 oauthType,
			"label":                     label,
			"usesBuiltInClient":         usesBuiltInClient,
			"requiresClientCredentials": !usesBuiltInClient,
			"redirectUri":               redirectURI,
			"scope":                     scope,
			"supportsProjectId":         supportsProjectID,
			"supportsTierId":            supportsTierID,
			"supportedEndpointModes":    modes,
		}
	}
	return map[string]any{
		"defaultOAuthType": "code_assist",
		"oauthTypes": []any{
			capability("code_assist", "Gemini Code Assist", true, true, true),
			capability("google_one", "Google One", true, true, true),
			capability("ai_studio", "Google AI Studio", false, true, true),
		},
	}
}

// geminiAuthURLOptions mirrors the auth-url schema input; the same struct also
// carries the refresh-token body fields (Scope included).
type geminiAuthURLOptions struct {
	OAuthType      string
	ClientID       string
	ClientSecret   string
	ProjectID      string
	TierID         string
	QuotaProjectID string
	BaseURL        string
	Scope          string
	OwnerID        string
}

// generateGeminiAuthURL mirrors generateGeminiAuthURL. Payload shape:
// {authUrl, sessionId, state}.
func (s *Store) generateGeminiAuthURL(input geminiAuthURLOptions) (map[string]any, error) {
	oauthType := normalizeGeminiOAuthType(input.OAuthType)
	clientID, clientSecret, redirectURI, scope, err := resolveGeminiOAuthClient(oauthType, input.ClientID, input.ClientSecret)
	if err != nil {
		return nil, err
	}
	state := randomBase64URL(32)
	codeVerifier := randomBase64URL(32)
	codeChallenge := pkceS256(codeVerifier)
	sessionID := randomHex(16)
	projectID := normalizeText(input.ProjectID)
	tierID := canonicalGeminiTierID(oauthType, input.TierID)
	quotaProjectID := normalizeText(input.QuotaProjectID)
	baseURL := normalizeText(input.BaseURL)
	if baseURL == "" {
		baseURL = defaultGeminiBaseURL(oauthType)
	}
	session := geminiOAuthSession{
		State:                state,
		CodeVerifier:         codeVerifier,
		Scope:                scope,
		RedirectURI:          redirectURI,
		ClientID:             clientID,
		ClientSecret:         clientSecret,
		OAuthType:            oauthType,
		ProjectID:            projectID,
		TierID:               tierID,
		QuotaProjectID:       quotaProjectID,
		BaseURL:              baseURL,
		OwnerSystemAccountID: input.OwnerID,
	}
	s.sessions.set(geminiSessionNamespace, sessionID, session, oauthSessionTTL)
	return map[string]any{
		"authUrl":   buildGeminiAuthorizeURL(clientID, redirectURI, scope, state, codeChallenge, projectID),
		"sessionId": sessionID,
		"state":     state,
	}, nil
}

// buildGeminiAuthorizeURL mirrors buildGeminiAuthorizeUrl.
func buildGeminiAuthorizeURL(clientID, redirectURI, scope, state, codeChallenge, projectID string) string {
	params := map[string]string{
		"response_type":          "code",
		"client_id":              clientID,
		"redirect_uri":           redirectURI,
		"scope":                  scope,
		"state":                  state,
		"code_challenge":         codeChallenge,
		"code_challenge_method":  "S256",
		"access_type":            "offline",
		"prompt":                 "consent",
		"include_granted_scopes": "true",
	}
	if projectID = normalizeText(projectID); projectID != "" {
		params["project_id"] = projectID
	}
	return GeminiOAuthAuthorizeURL + "?" + encodeForm(params)
}

// geminiTokenInfo mirrors GeminiOAuthTokenInfo.
type geminiTokenInfo struct {
	AccessToken    string
	RefreshToken   string
	ExpiresIn      int
	ExpiresAt      string
	Scope          string
	TokenType      string
	ClientID       string
	ClientSecret   string
	OAuthType      string
	ProjectID      string
	TierID         string
	QuotaProjectID string
	BaseURL        string
}

// geminiExchangeOptions carries the create-from-code session-consistency
// fields (route body) into the session check.
type geminiExchangeOptions struct {
	SessionID      string
	CallbackURL    string
	OAuthType      string
	ClientID       string
	ClientSecret   string
	ProjectID      string
	TierID         string
	QuotaProjectID string
	BaseURL        string
	OwnerID        string
}

// extractGeminiCodeAndState mirrors extractCodeAndState (gemini): URL, then
// fragment, then query; both parts required.
func extractGeminiCodeAndState(callbackURL string) (code, state string, err error) {
	value := normalizeText(callbackURL)
	if value == "" {
		return "", "", errors.New("Gemini 授权结果不能为空")
	}
	if parsed, parseErr := url.Parse(value); parseErr == nil && parsed.Scheme != "" && parsed.Host != "" {
		query := parsed.Query()
		if errorCode := normalizeText(query.Get("error")); errorCode != "" {
			detail := normalizeText(query.Get("error_description"))
			if detail == "" {
				detail = errorCode
			}
			return "", "", &UpstreamError{Message: detail, StatusCode: 502}
		}
		code = normalizeText(query.Get("code"))
		state = normalizeText(query.Get("state"))
	}
	if code == "" || state == "" {
		if index := strings.LastIndex(value, "#"); index > 0 {
			code = normalizeText(value[:index])
			state = normalizeText(value[index+1:])
		} else {
			query := parseQueryValues(value)
			code = normalizeText(query.Get("code"))
			state = normalizeText(query.Get("state"))
		}
	}
	if code == "" || state == "" {
		return "", "", errors.New("Gemini 授权结果必须包含 code 和 state")
	}
	return code, state, nil
}

// assertGeminiSessionField mirrors assertSessionFieldMatches.
func assertGeminiSessionField(label, provided, sessionValue string) error {
	provided = normalizeText(provided)
	if provided != "" && provided != normalizeText(sessionValue) {
		return errors.New("Gemini OAuth " + label + " 与授权会话不一致")
	}
	return nil
}

// exchangeGeminiAuthorizationCode mirrors exchangeGeminiAuthCode minus the
// upstream code-assist/drive enrichment probes (M17 deferral: project/tier
// detection rides the probe slice; request-supplied project/tier inputs are
// carried through instead).
func (s *Store) exchangeGeminiAuthorizationCode(ctx context.Context, input geminiExchangeOptions) (*geminiTokenInfo, error) {
	code, state, err := extractGeminiCodeAndState(input.CallbackURL)
	if err != nil {
		return nil, err
	}
	raw := s.sessions.get(geminiSessionNamespace, input.SessionID)
	if raw == nil {
		return nil, errors.New("Gemini OAuth 会话不存在或已过期")
	}
	var session geminiOAuthSession
	if err := unmarshalSession(raw, &session); err != nil {
		return nil, errors.New("Gemini OAuth 会话不存在或已过期")
	}
	if state == "" || state != session.State {
		return nil, errors.New("Gemini OAuth state 无效")
	}
	if session.OwnerSystemAccountID != "" && normalizeText(input.OwnerID) != session.OwnerSystemAccountID {
		return nil, errors.New("Gemini OAuth session owner 归属无效")
	}
	if input.OAuthType != "" && normalizeGeminiOAuthType(input.OAuthType) != session.OAuthType {
		return nil, errors.New("Gemini OAuth 类型与授权会话不一致")
	}
	for _, check := range []struct {
		label, provided, sessionValue string
	}{
		{"Client ID", input.ClientID, session.ClientID},
		{"Client Secret", input.ClientSecret, session.ClientSecret},
		{"Project ID", input.ProjectID, session.ProjectID},
		{"Tier ID", canonicalGeminiTierID(session.OAuthType, input.TierID), session.TierID},
		{"Quota Project ID", input.QuotaProjectID, session.QuotaProjectID},
		{"Base URL", input.BaseURL, session.BaseURL},
	} {
		if err := assertGeminiSessionField(check.label, check.provided, check.sessionValue); err != nil {
			return nil, err
		}
	}
	info, err := s.requestGeminiToken(ctx, map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     session.ClientID,
		"client_secret": session.ClientSecret,
		"code":          code,
		"code_verifier": session.CodeVerifier,
		"redirect_uri":  session.RedirectURI,
	}, geminiRequestOptions{
		OAuthType:      session.OAuthType,
		ClientID:       session.ClientID,
		ClientSecret:   session.ClientSecret,
		ProjectID:      session.ProjectID,
		TierID:         session.TierID,
		QuotaProjectID: session.QuotaProjectID,
		BaseURL:        session.BaseURL,
		Scope:          session.Scope,
	})
	if err != nil {
		return nil, err
	}
	if !s.sessions.compareDelete(geminiSessionNamespace, input.SessionID, session) {
		return nil, errors.New("Gemini OAuth 会话已消费，请重新发起授权")
	}
	return enrichGeminiTokenInfo(info), nil
}

// geminiRequestOptions mirrors the requestGeminiToken options payload.
type geminiRequestOptions struct {
	OAuthType      string
	ClientID       string
	ClientSecret   string
	ProjectID      string
	TierID         string
	QuotaProjectID string
	BaseURL        string
	Scope          string
}

// refreshGeminiToken mirrors refreshGeminiAuthToken minus the retry/backoff and
// legacy-client fallback loops (M17 deferral); the token request runs once.
func (s *Store) refreshGeminiToken(ctx context.Context, refreshToken string, input geminiAuthURLOptions) (*geminiTokenInfo, error) {
	refreshToken = normalizeText(refreshToken)
	if refreshToken == "" {
		return nil, errors.New("Gemini Refresh Token 不能为空")
	}
	oauthType := normalizeGeminiOAuthType(input.OAuthType)
	clientID, clientSecret, _, _, err := resolveGeminiOAuthClient(oauthType, input.ClientID, input.ClientSecret)
	if err != nil {
		return nil, err
	}
	tierID := canonicalGeminiTierID(oauthType, input.TierID)
	baseURL := normalizeText(input.BaseURL)
	if baseURL == "" {
		baseURL = defaultGeminiBaseURL(oauthType)
	}
	info, err := s.requestGeminiToken(ctx, map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
		"client_id":     clientID,
		"client_secret": clientSecret,
	}, geminiRequestOptions{
		OAuthType:      oauthType,
		ClientID:       clientID,
		ClientSecret:   clientSecret,
		ProjectID:      normalizeText(input.ProjectID),
		TierID:         tierID,
		QuotaProjectID: normalizeText(input.QuotaProjectID),
		BaseURL:        baseURL,
		Scope:          normalizeText(input.Scope),
	})
	if err != nil {
		return nil, err
	}
	return enrichGeminiTokenInfo(info), nil
}

// requestGeminiToken mirrors requestGeminiToken: form POST with client secret,
// upstream error envelope, 5-minute clock skew safety on expires_at.
func (s *Store) requestGeminiToken(ctx context.Context, form map[string]string, options geminiRequestOptions) (*geminiTokenInfo, error) {
	response, err := s.exchange(ctx, formRequest(GeminiOAuthTokenURL, form))
	if err != nil {
		return nil, err
	}
	payload := parseTokenPayload(response.Body)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		errorCode := normalizeText(payload["error"])
		errorDescription := normalizeText(payload["error_description"])
		detail := ""
		if errorCode != "" {
			detail = errorCode
			if errorDescription != "" && errorDescription != errorCode {
				detail += ": " + errorDescription
			}
		} else {
			detail = errorDescription
			if detail == "" {
				detail = response.Body
			}
		}
		return nil, upstreamError("Gemini", response.StatusCode, detail)
	}
	accessToken := normalizeText(payload["access_token"])
	if accessToken == "" {
		return nil, errors.New("Gemini OAuth 令牌响应缺少 access_token")
	}
	expiresIn := 0
	expiresAt := ""
	if value, ok := finitePositiveInt(payload["expires_in"]); ok {
		expiresIn = value
		safeExpiresIn := value - 300
		if safeExpiresIn < 30 {
			safeExpiresIn = 30
		}
		expiresAt = isoFromMillis(s.now().UnixMilli() + int64(safeExpiresIn)*1000)
	}
	scope := normalizeText(payload["scope"])
	if scope == "" {
		scope = options.Scope
	}
	tokenType := normalizeText(payload["token_type"])
	return &geminiTokenInfo{
		AccessToken:    accessToken,
		RefreshToken:   normalizeText(payload["refresh_token"]),
		ExpiresIn:      expiresIn,
		ExpiresAt:      expiresAt,
		Scope:          scope,
		TokenType:      tokenType,
		ClientID:       options.ClientID,
		ClientSecret:   options.ClientSecret,
		OAuthType:      options.OAuthType,
		ProjectID:      options.ProjectID,
		TierID:         options.TierID,
		QuotaProjectID: options.QuotaProjectID,
		BaseURL:        options.BaseURL,
	}, nil
}

// enrichGeminiTokenInfo mirrors enrichGeminiTokenInfo's static tier defaults
// (the code-assist/drive upstream probes are an M17 deferral): ai_studio pins
// aistudio_free, code_assist pins gcp_standard, google_one pins
// google_one_free unless a tier was already resolved.
func enrichGeminiTokenInfo(info *geminiTokenInfo) *geminiTokenInfo {
	switch info.OAuthType {
	case "ai_studio":
		if canonical := canonicalGeminiTierID("ai_studio", info.TierID); canonical != "" {
			info.TierID = canonical
		} else {
			info.TierID = "aistudio_free"
		}
	case "google_one":
		if canonical := canonicalGeminiTierID("google_one", info.TierID); canonical != "" {
			info.TierID = canonical
		} else {
			info.TierID = "google_one_free"
		}
	default:
		if canonical := canonicalGeminiTierID("code_assist", info.TierID); canonical != "" {
			info.TierID = canonical
		} else {
			info.TierID = "gcp_standard"
		}
	}
	return info
}

// buildGeminiOAuthCredentials mirrors buildGeminiOAuthCredentials.
func buildGeminiOAuthCredentials(info *geminiTokenInfo, fallback *geminiCredentialFallback) map[string]any {
	oauthType := info.OAuthType
	if normalizeText(oauthType) == "" && fallback != nil {
		oauthType = normalizeGeminiOAuthType(fallback.OAuthType)
	}
	oauthType = normalizeGeminiOAuthType(oauthType)
	credentials := map[string]any{
		"access_token":  info.AccessToken,
		"client_id":     info.ClientID,
		"client_secret": info.ClientSecret,
		"oauth_type":    oauthType,
	}
	baseURL := normalizeText(info.BaseURL)
	if baseURL == "" && fallback != nil {
		baseURL = normalizeText(fallback.BaseURL)
	}
	if baseURL == "" {
		baseURL = defaultGeminiBaseURL(oauthType)
	}
	credentials["base_url"] = baseURL
	pick := func(primary, secondary string) string {
		if text := normalizeText(primary); text != "" {
			return text
		}
		if fallback == nil {
			return ""
		}
		return normalizeText(secondary)
	}
	refreshToken := normalizeText(info.RefreshToken)
	if refreshToken == "" && fallback != nil {
		refreshToken = normalizeText(fallback.RefreshToken)
	}
	if refreshToken != "" {
		credentials["refresh_token"] = refreshToken
	}
	if info.ExpiresAt != "" {
		credentials["expires_at"] = info.ExpiresAt
	}
	if info.TokenType != "" {
		credentials["token_type"] = info.TokenType
	}
	if scope := pick(info.Scope, fallbackScope(fallback)); scope != "" {
		credentials["scope"] = scope
	}
	if projectID := pick(info.ProjectID, fallback.ProjectID); projectID != "" {
		credentials["project_id"] = projectID
	}
	tierID := canonicalGeminiTierID(oauthType, info.TierID)
	if tierID == "" && fallback != nil {
		tierID = canonicalGeminiTierID(oauthType, fallback.TierID)
	}
	if tierID != "" {
		credentials["tier_id"] = tierID
	}
	if quotaProjectID := pick(info.QuotaProjectID, fallback.QuotaProjectID); quotaProjectID != "" {
		credentials["quota_project_id"] = quotaProjectID
	}
	if oauthType != "ai_studio" {
		credentials["supported_endpoint_modes"] = append([]string{}, geminiCLIEndpointModes...)
	}
	return credentials
}

func fallbackScope(fallback *geminiCredentialFallback) string {
	if fallback == nil {
		return ""
	}
	return fallback.Scope
}

// geminiCredentialFallback mirrors the buildGeminiOAuthCredentials fallback
// bundle.
type geminiCredentialFallback struct {
	RefreshToken   string
	OAuthType      string
	ProjectID      string
	TierID         string
	QuotaProjectID string
	BaseURL        string
	Scope          string
}

// geminiAccountOAuthType mirrors accountOAuthType: infer the stored account's
// oauth type from its credentials.
func geminiAccountOAuthType(credentials map[string]any) string {
	if value := normalizeText(credentials["oauth_type"]); value != "" {
		return normalizeGeminiOAuthType(value)
	}
	baseURL := normalizeText(credentials["base_url"])
	if strings.Contains(baseURL, "generativelanguage.googleapis.com") {
		return "ai_studio"
	}
	if normalizeText(credentials["project_id"]) != "" || strings.Contains(baseURL, "cloudcode-pa.googleapis.com") {
		return "code_assist"
	}
	if clientID := normalizeText(credentials["client_id"]); clientID != "" && clientID != GeminiCLIOAuthClientID {
		return "ai_studio"
	}
	return "code_assist"
}

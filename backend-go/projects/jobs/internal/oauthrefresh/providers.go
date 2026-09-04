package oauthrefresh

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"
)

// Provider constants mirror the four Node OAuth services
// (openai-oauth.service.ts, anthropic-oauth.service.ts, gemini-oauth.service.ts,
// grok-oauth.service.ts) and the M17 gateway port of the same protocols.
const (
	OpenAIOAuthClientID      = "app_EMoamEEZ73f0CkXaXp7hrann"
	OpenAIOAuthTokenURL      = "https://auth.openai.com/oauth/token"
	OpenAIOAuthRefreshScopes = "openid profile email"
	openAIOAuthBaseURL       = "https://api.openai.com/v1"

	AnthropicOAuthClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	AnthropicOAuthTokenURL = "https://platform.claude.com/v1/oauth/token"
	anthropicOAuthBaseURL  = "https://api.anthropic.com/v1"

	GeminiOAuthTokenURL        = "https://oauth2.googleapis.com/token"
	GeminiCLIOAuthClientID     = "681255809395-oo8ft2oprdrnp9e3aqf6av3hmdib135j.apps.googleusercontent.com"
	GeminiCLIOAuthClientSecret = "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"
	geminiOAuthDefaultBaseURL  = "https://generativelanguage.googleapis.com"
	geminiCLIDefaultBaseURL    = "https://cloudcode-pa.googleapis.com"

	GrokOAuthTokenURL   = "https://auth.x.ai/oauth2/token"
	GrokOAuthClientID   = "b1a00492-073a-47ea-816f-4c329264a828"
	GrokOAuthBaseURL    = "https://cli-chat-proxy.grok.com/v1"
	grokDefaultTokenTTL = 6 * 60 * 60 // seconds
)

// Provider codes mirror domain/provider-protocol.ts.
const (
	ProviderGPT       = "gpt"
	ProviderAnthropic = "anthropic"
	ProviderGemini    = "gemini"
	ProviderXAI       = "xai"

	ProfileGPTOpenAIV1 = "profile_gpt_openai_v1"
	ProfileXAIOpenAIV1 = "profile_xai_openai_v1"

	AccountTypeOAuth       = "oauth"
	AccountTypeGoogleOAuth = "google_oauth"
)

// ---------------------------------------------------------------------------
// OpenAI
// ---------------------------------------------------------------------------

// OpenAITokenInfo mirrors OpenAITokenInfo.
type OpenAITokenInfo struct {
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

// RefreshOpenAIToken mirrors refreshOpenAIOAuthToken: refresh grant with the
// narrowed scope and the default client id fallback.
func RefreshOpenAIToken(ctx context.Context, ex TokenExchanger, refreshToken, clientID string, now time.Time) (*OpenAITokenInfo, error) {
	refreshToken = normalizeText(refreshToken)
	if refreshToken == "" {
		return nil, errors.New("刷新令牌不能为空")
	}
	if normalizeText(clientID) == "" {
		clientID = OpenAIOAuthClientID
	}
	return requestOpenAIToken(ctx, ex, map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
		"client_id":     clientID,
		"scope":         OpenAIOAuthRefreshScopes,
	}, now)
}

// requestOpenAIToken mirrors requestOpenAIToken: form POST, upstream error
// envelope, required access_token/expires_in, JWT claim enrichment.
func requestOpenAIToken(ctx context.Context, ex TokenExchanger, form map[string]string, now time.Time) (*OpenAITokenInfo, error) {
	response, err := exchange(ctx, ex, formRequest(OpenAIOAuthTokenURL, form))
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
	return &OpenAITokenInfo{
		AccessToken:   accessToken,
		RefreshToken:  refreshToken,
		IDToken:       idToken,
		ExpiresIn:     expiresIn,
		ExpiresAt:     isoFromMillis(now.UnixMilli() + int64(expiresIn)*1000),
		ClientID:      clientID,
		Email:         pick(idClaims["email"], accessClaims["email"]),
		AccountID:     pick(idAuth["chatgpt_account_id"], accessAuth["chatgpt_account_id"]),
		ChatGPTUserID: pick(idAuth["chatgpt_user_id"], idAuth["user_id"], accessAuth["chatgpt_user_id"], accessAuth["user_id"]),
		PlanType:      pick(idAuth["chatgpt_plan_type"], accessAuth["chatgpt_plan_type"]),
	}, nil
}

// BuildOpenAIOAuthCredentials mirrors buildOpenAIOAuthCredentials.
func BuildOpenAIOAuthCredentials(info *OpenAITokenInfo, fallbackRefreshToken string) map[string]any {
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

// ---------------------------------------------------------------------------
// Anthropic
// ---------------------------------------------------------------------------

// AnthropicTokenInfo mirrors AnthropicOAuthTokenInfo.
type AnthropicTokenInfo struct {
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

// RefreshAnthropicToken mirrors refreshAnthropicAuthToken.
func RefreshAnthropicToken(ctx context.Context, ex TokenExchanger, refreshToken, clientID string, now time.Time) (*AnthropicTokenInfo, error) {
	refreshToken = normalizeText(refreshToken)
	if refreshToken == "" {
		return nil, errors.New("Anthropic Refresh Token 不能为空")
	}
	if normalizeText(clientID) == "" {
		clientID = AnthropicOAuthClientID
	}
	return requestAnthropicToken(ctx, ex, map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
		"client_id":     clientID,
	}, now)
}

// requestAnthropicToken mirrors requestAnthropicToken: JSON body POST with the
// axios user-agent, upstream error envelope, account/organization claim
// extraction.
func requestAnthropicToken(ctx context.Context, ex TokenExchanger, form map[string]string, now time.Time) (*AnthropicTokenInfo, error) {
	response, err := exchange(ctx, ex, jsonRequest(AnthropicOAuthTokenURL, form))
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
		expiresAt = isoFromMillis(now.UnixMilli() + int64(value)*1000)
	}
	clientID := normalizeText(form["client_id"])
	if clientID == "" {
		clientID = AnthropicOAuthClientID
	}
	return &AnthropicTokenInfo{
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

// BuildAnthropicOAuthCredentials mirrors buildAnthropicOAuthCredentials.
func BuildAnthropicOAuthCredentials(info *AnthropicTokenInfo, fallbackRefreshToken string) map[string]any {
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

// ---------------------------------------------------------------------------
// Gemini
// ---------------------------------------------------------------------------

// GeminiTokenInfo mirrors GeminiOAuthTokenInfo.
type GeminiTokenInfo struct {
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

// NormalizeGeminiOAuthType mirrors normalizeOAuthType: unknown falls back to
// code_assist.
func NormalizeGeminiOAuthType(value string) string {
	value = normalizeText(value)
	switch value {
	case "code_assist", "google_one", "ai_studio":
		return value
	}
	return "code_assist"
}

// resolveGeminiOAuthClient mirrors resolveGeminiOAuthClient: built-in client
// for code_assist/google_one, stored/env credentials for ai_studio.
func resolveGeminiOAuthClient(oauthType, clientID, clientSecret string) (id, secret string, err error) {
	if oauthType != "ai_studio" {
		builtinSecret := normalizeText(os.Getenv("GEMINI_CLI_OAUTH_CLIENT_SECRET"))
		if builtinSecret == "" {
			builtinSecret = GeminiCLIOAuthClientSecret
		}
		return GeminiCLIOAuthClientID, builtinSecret, nil
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
		return "", "", errors.New("Gemini AI Studio OAuth 需要同时配置 Client ID 和 Client Secret")
	}
	return id, secret, nil
}

// defaultGeminiBaseURL mirrors defaultBaseUrl.
func defaultGeminiBaseURL(oauthType string) string {
	if oauthType == "ai_studio" {
		return geminiOAuthDefaultBaseURL
	}
	return geminiCLIDefaultBaseURL
}

// CanonicalGeminiTierID mirrors canonicalGeminiTierId.
func CanonicalGeminiTierID(oauthType, raw string) string {
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

// GeminiAccountOAuthType mirrors accountOAuthType: infer the stored account's
// oauth type from its credentials.
func GeminiAccountOAuthType(credentials map[string]any) string {
	if value := normalizeText(credentials["oauth_type"]); value != "" {
		return NormalizeGeminiOAuthType(value)
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

// GeminiCredentialFallback mirrors the stored-credential bundle the Node
// dispatch preparation passes into refreshGeminiAuthToken/buildGeminiOAuth
// Credentials (client pair included; ai_studio requires both).
type GeminiCredentialFallback struct {
	RefreshToken   string
	OAuthType      string
	ClientID       string
	ClientSecret   string
	ProjectID      string
	TierID         string
	QuotaProjectID string
	BaseURL        string
	Scope          string
}

// RefreshGeminiToken mirrors refreshGeminiAuthToken minus the retry/backoff and
// legacy-client fallback loops (the M17 deferral carries over: the token
// request runs once with the resolved client).
func RefreshGeminiToken(ctx context.Context, ex TokenExchanger, refreshToken string, fallback GeminiCredentialFallback, now time.Time) (*GeminiTokenInfo, error) {
	refreshToken = normalizeText(refreshToken)
	if refreshToken == "" {
		return nil, errors.New("Gemini Refresh Token 不能为空")
	}
	oauthType := NormalizeGeminiOAuthType(fallback.OAuthType)
	clientID, clientSecret, err := resolveGeminiOAuthClient(oauthType, fallback.ClientID, fallback.ClientSecret)
	if err != nil {
		return nil, err
	}
	baseURL := normalizeText(fallback.BaseURL)
	if baseURL == "" {
		baseURL = defaultGeminiBaseURL(oauthType)
	}
	info, err := requestGeminiToken(ctx, ex, map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
		"client_id":     clientID,
		"client_secret": clientSecret,
	}, geminiRequestOptions{
		OAuthType:      oauthType,
		ClientID:       clientID,
		ClientSecret:   clientSecret,
		ProjectID:      normalizeText(fallback.ProjectID),
		TierID:         CanonicalGeminiTierID(oauthType, fallback.TierID),
		QuotaProjectID: normalizeText(fallback.QuotaProjectID),
		BaseURL:        baseURL,
		Scope:          normalizeText(fallback.Scope),
	}, now)
	if err != nil {
		return nil, err
	}
	return enrichGeminiTokenInfo(info), nil
}

// geminiRequestOptions carries the client/tier context through the token call.
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

// requestGeminiToken mirrors requestGeminiToken: form POST with client secret,
// upstream error envelope, 5-minute clock skew safety on expires_at.
func requestGeminiToken(ctx context.Context, ex TokenExchanger, form map[string]string, options geminiRequestOptions, now time.Time) (*GeminiTokenInfo, error) {
	response, err := exchange(ctx, ex, formRequest(GeminiOAuthTokenURL, form))
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
		expiresAt = isoFromMillis(now.UnixMilli() + int64(safeExpiresIn)*1000)
	}
	scope := normalizeText(payload["scope"])
	if scope == "" {
		scope = options.Scope
	}
	tokenType := normalizeText(payload["token_type"])
	return &GeminiTokenInfo{
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

// enrichGeminiTokenInfo mirrors enrichGeminiTokenInfo's static tier defaults:
// ai_studio pins aistudio_free, code_assist pins gcp_standard, google_one pins
// google_one_free unless a tier was already resolved.
func enrichGeminiTokenInfo(info *GeminiTokenInfo) *GeminiTokenInfo {
	switch info.OAuthType {
	case "ai_studio":
		if canonical := CanonicalGeminiTierID("ai_studio", info.TierID); canonical != "" {
			info.TierID = canonical
		} else {
			info.TierID = "aistudio_free"
		}
	case "google_one":
		if canonical := CanonicalGeminiTierID("google_one", info.TierID); canonical != "" {
			info.TierID = canonical
		} else {
			info.TierID = "google_one_free"
		}
	default:
		if canonical := CanonicalGeminiTierID("code_assist", info.TierID); canonical != "" {
			info.TierID = canonical
		} else {
			info.TierID = "gcp_standard"
		}
	}
	return info
}

// BuildGeminiOAuthCredentials mirrors buildGeminiOAuthCredentials. The stored
// credentials supply the fallback bundle; the token info wins.
func BuildGeminiOAuthCredentials(info *GeminiTokenInfo, fallback *GeminiCredentialFallback) map[string]any {
	oauthType := info.OAuthType
	if normalizeText(oauthType) == "" && fallback != nil {
		oauthType = NormalizeGeminiOAuthType(fallback.OAuthType)
	}
	oauthType = NormalizeGeminiOAuthType(oauthType)
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
	fallbackScope := ""
	fallbackProjectID := ""
	fallbackTierID := ""
	fallbackQuotaProjectID := ""
	if fallback != nil {
		fallbackScope = fallback.Scope
		fallbackProjectID = fallback.ProjectID
		fallbackTierID = fallback.TierID
		fallbackQuotaProjectID = fallback.QuotaProjectID
	}
	if scope := pick(info.Scope, fallbackScope); scope != "" {
		credentials["scope"] = scope
	}
	if projectID := pick(info.ProjectID, fallbackProjectID); projectID != "" {
		credentials["project_id"] = projectID
	}
	tierID := CanonicalGeminiTierID(oauthType, info.TierID)
	if tierID == "" && fallback != nil {
		tierID = CanonicalGeminiTierID(oauthType, fallbackTierID)
	}
	if tierID != "" {
		credentials["tier_id"] = tierID
	}
	if quotaProjectID := pick(info.QuotaProjectID, fallbackQuotaProjectID); quotaProjectID != "" {
		credentials["quota_project_id"] = quotaProjectID
	}
	if oauthType != "ai_studio" {
		credentials["supported_endpoint_modes"] = []string{"generate_content_json", "generate_content_sse"}
	}
	return credentials
}

// ---------------------------------------------------------------------------
// Grok (xai)
// ---------------------------------------------------------------------------

// GrokTokenInfo mirrors GrokOAuthTokenInfo.
type GrokTokenInfo struct {
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

// RefreshGrokToken mirrors refreshGrokAuthToken: a missing rotated refresh
// token keeps the input one.
func RefreshGrokToken(ctx context.Context, ex TokenExchanger, refreshToken, clientID string, now time.Time) (*GrokTokenInfo, error) {
	refreshToken = normalizeText(refreshToken)
	if refreshToken == "" {
		return nil, errors.New("Grok Refresh Token 不能为空")
	}
	if normalizeText(clientID) == "" {
		clientID = GrokOAuthClientID
	}
	info, err := requestGrokToken(ctx, ex, map[string]string{
		"grant_type":    "refresh_token",
		"client_id":     clientID,
		"refresh_token": refreshToken,
	}, clientID, now)
	if err != nil {
		return nil, err
	}
	if info.RefreshToken == "" {
		info.RefreshToken = refreshToken
	}
	return info, nil
}

// requestGrokToken mirrors requestGrokToken: form POST with the grok
// user-agent, upstream error envelope, default 6h TTL and 403 entitlement
// branch.
func requestGrokToken(ctx context.Context, ex TokenExchanger, form map[string]string, clientID string, now time.Time) (*GrokTokenInfo, error) {
	request := formRequest(GrokOAuthTokenURL, form)
	request.Headers["user-agent"] = "sub2api-grok-oauth/1.0"
	response, err := exchange(ctx, ex, request)
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
	}, clientID, now), nil
}

// grokRawToken mirrors the raw payload crossing service boundaries.
type grokRawToken struct {
	AccessToken  string
	RefreshToken string
	IDToken      string
	TokenType    string
	ExpiresIn    int
	Scope        string
}

// toGrokTokenInfo mirrors toGrokOAuthTokenInfo: merged id/access JWT claims.
func toGrokTokenInfo(payload grokRawToken, clientID string, now time.Time) *GrokTokenInfo {
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
	return &GrokTokenInfo{
		AccessToken:       payload.AccessToken,
		RefreshToken:      payload.RefreshToken,
		IDToken:           payload.IDToken,
		TokenType:         tokenType,
		ExpiresIn:         expiresIn,
		ExpiresAt:         isoFromMillis(now.UnixMilli() + int64(expiresIn)*1000),
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

// BuildGrokOAuthCredentials mirrors buildGrokOAuthCredentials.
func BuildGrokOAuthCredentials(info *GrokTokenInfo, fallbackRefreshToken string) map[string]any {
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

// isoFromMillis renders Node millisecond ISO strings from epoch millis.
func isoFromMillis(milliseconds int64) string {
	return isoMillis(time.UnixMilli(milliseconds))
}

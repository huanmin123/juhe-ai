package oauthmgmt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamidentity"
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

// exchangeGeminiAuthorizationCode mirrors exchangeGeminiAuthCode，只负责换发与
// 会话一致性（project/tier 入参沿用请求/会话透传）。M17 曾把 Code Assist
// project/tier 探测整体后移到探针切片（"rides the probe slice"，与探针实现不
// 符）；T65-② 起该探测改在建号链换发成功后执行（plans.go geminiPlan 的
// enrichOutcome 钩子 → geminiCreateOnboarding），本函数不再承载探测。
// proxyURL 是账户绑定代理的出站 URL（空 = 直连）。
func (s *Store) exchangeGeminiAuthorizationCode(ctx context.Context, input geminiExchangeOptions, proxyURL string) (*geminiTokenInfo, error) {
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
	if state == "" || !constantTimeEqual(state, session.State) {
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
	}, proxyURL)
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
func (s *Store) refreshGeminiToken(ctx context.Context, refreshToken string, input geminiAuthURLOptions, proxyURL string) (*geminiTokenInfo, error) {
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
	}, proxyURL)
	if err != nil {
		return nil, err
	}
	return enrichGeminiTokenInfo(info), nil
}

// requestGeminiToken mirrors requestGeminiToken: form POST with client secret,
// upstream error envelope, 5-minute clock skew safety on expires_at.
func (s *Store) requestGeminiToken(ctx context.Context, form map[string]string, options geminiRequestOptions, proxyURL string) (*geminiTokenInfo, error) {
	request := formRequest(GeminiOAuthTokenURL, form)
	request.ProxyURL = proxyURL
	response, err := s.exchange(ctx, request)
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

// enrichGeminiTokenInfo 落静态档位兜底：ai_studio 钉 aistudio_free、code_assist
// 钉 gcp_standard、google_one 钉 google_one_free（已有归一档位时保持原值）。
// T65-② 起建号链探测成功时由 geminiCreateOnboarding 以探测档位覆盖凭据
// tier_id；本函数的静态默认仍是刷新/轮换链与探测失败回落面的兜底（探针侧、
// keepalive 刷新回填均保持现状）。
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

// --- T65-②：建号链 Code Assist 探测 / onboarding -----------------------------

// geminiCodeAssistProbeTimeout 与包内既有上游 token 调用同一量级（token 端点
// 25s，见 transport.go defaultTokenTimeout）；响应体超限由共用 exchanger 报错
//（transport.go 的 256KiB 上限）。
const geminiCodeAssistProbeTimeout = defaultTokenTimeout

// geminiOnboardingError 标记建号链 Code Assist 探测/onboarding 的确定性失败：
// writeOAuthError 将其渲染为 400 + 行动文案，不落 502 兜底——一次性交互路径
// 上不静默产出注定不可用的账户（探针在缺 project_id 时直接报错）。
type geminiOnboardingError struct {
	Reason string
}

func (e *geminiOnboardingError) Error() string {
	return "未能自动开通 Code Assist 项目（" + e.Reason + "），请手填 projectId 后重试"
}

// geminiCodeAssistMetadata 是 cloudcode-pa v1internal 两端点共用的 CLI 仿真
// 元数据，取值与参考实现（sub2api_source geminicli 默认请求、CLIProxyAPI
// antigravity）同一形态，不另造指纹。
func geminiCodeAssistMetadata() map[string]any {
	return map[string]any{
		"ideType":    "ANTIGRAVITY",
		"platform":   "PLATFORM_UNSPECIFIED",
		"pluginType": "GEMINI",
	}
}

// geminiCreateOnboarding 是 geminiPlan.enrichOutcome 的实现：code_assist /
// google_one 且凭据未携带 projectId 时，用新换发的 access token 执行一次
// loadCodeAssist → onboardUser，把探测到的 project 与归一档位写回凭据。
// 失败回落契约：用户在建号请求 body 显式提交过 tierId（归一非空）→ 按现状
// 继续建号并落日志；否则 400「未能自动开通 Code Assist 项目（原因），请手填
// projectId 后重试」。ai_studio 与显式 projectId 全程短路，零探测调用。
// 判定口径：回落标记只认建号请求的显式字段（两条建号路由 body 均携带
// tierId）；授权链接阶段提交进会话的 tier 在换发成功后会话即被消费，不再作
// 为回落依据。
func (s *Store) geminiCreateOnboarding(ctx context.Context, body map[string]any, outcome *tokenOutcome, proxyURL string) (*tokenOutcome, error) {
	if outcome == nil || outcome.Credentials == nil {
		return outcome, nil
	}
	oauthType := normalizeGeminiOAuthType(stringCredential(outcome.Credentials, "oauth_type"))
	if oauthType == "ai_studio" {
		return outcome, nil
	}
	if stringCredential(outcome.Credentials, "project_id") != "" {
		// 用户显式提交 projectId：现状不变，零探测调用。
		return outcome, nil
	}
	explicitTier := canonicalGeminiTierID(oauthType, optionalTrimmedText(body, "tierId"))
	projectID, tierRaw, err := s.detectGeminiCodeAssistProject(ctx, oauthType,
		stringCredential(outcome.Credentials, "access_token"),
		stringCredential(outcome.Credentials, "base_url"), proxyURL)
	if err != nil {
		if explicitTier != "" {
			// 用户显式提交过档位：按现状继续建号（无 project 账户保持既有
			// 形态），失败原因落日志供排查。
			slog.Warn("Gemini Code Assist 探测失败，按用户显式档位回落",
				"event", "gemini_code_assist_onboarding_fallback",
				"oauthType", oauthType, "error", err)
			return outcome, nil
		}
		return nil, &geminiOnboardingError{Reason: err.Error()}
	}
	if projectID != "" {
		outcome.Credentials["project_id"] = projectID
	}
	if canonical := canonicalGeminiTierID(oauthType, tierRaw); canonical != "" {
		outcome.Credentials["tier_id"] = canonical
	}
	return outcome, nil
}

// detectGeminiCodeAssistProject 执行一次探测序列（对齐 Gemini CLI 系公开实现，
// 参考 CLIProxyAPI antigravity 与 sub2api_source gemini_oauth_service）：
// POST /v1internal:loadCodeAssist，响应含 cloudaicompanionProject 即命中；
// 否则 POST /v1internal:onboardUser（tierId 取 allowedTiers 归一可用档位）取回
// project。两步各自单次调用、失败不重试（建号是一次性交互路径，上游失败直接
// 给出明确错误）；tierRaw 始终返回 loadCodeAssist 报告的 paidTier/currentTier
// 原始值供归一落库。
func (s *Store) detectGeminiCodeAssistProject(ctx context.Context, oauthType, accessToken, baseURL, proxyURL string) (projectID, tierRaw string, err error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = defaultGeminiBaseURL(oauthType)
	}
	load, err := s.postGeminiCodeAssistInternal(ctx, baseURL, "loadCodeAssist", accessToken, proxyURL, map[string]any{
		"metadata": geminiCodeAssistMetadata(),
	})
	if err != nil {
		return "", "", err
	}
	tierRaw = geminiLoadTierID(load)
	if projectID := extractGeminiCompanionProject(load); projectID != "" {
		return projectID, tierRaw, nil
	}
	onboard, err := s.postGeminiCodeAssistInternal(ctx, baseURL, "onboardUser", accessToken, proxyURL, map[string]any{
		"tierId":   geminiOnboardUserTierID(oauthType, load, tierRaw),
		"metadata": geminiCodeAssistMetadata(),
	})
	if err != nil {
		return "", tierRaw, err
	}
	if done, _ := onboard["done"].(bool); !done {
		return "", tierRaw, errors.New("onboardUser 未完成（done=false）")
	}
	response, _ := onboard["response"].(map[string]any)
	if projectID := extractGeminiCompanionProject(response); projectID != "" {
		return projectID, tierRaw, nil
	}
	return "", tierRaw, errors.New("onboardUser 已完成但未返回 cloudaicompanionProject")
}

// postGeminiCodeAssistInternal 发出一次 cloudcode-pa v1internal 调用：Bearer 新
// access token + JSON 头 + 与既有 cloudcode-pa 出站请求同一口径的 Gemini CLI
// user-agent（upstreamidentity.EffectiveGeminiCLIUserAgent，与运行时
// generate_content 链同源，不新造第二套指纹），经账户绑定代理出站，走共用
// s.exchange 统一日志。非 2xx 返回带截断响应体的错误原文。
func (s *Store) postGeminiCodeAssistInternal(ctx context.Context, baseURL, action, accessToken, proxyURL string, payload map[string]any) (map[string]any, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	response, err := s.exchange(ctx, TokenHTTPRequest{
		URL: strings.TrimSuffix(baseURL, "/") + "/v1internal:" + action,
		Headers: map[string]string{
			"authorization": "Bearer " + accessToken,
			"content-type":  "application/json",
			"user-agent":    upstreamidentity.EffectiveGeminiCLIUserAgent(),
		},
		Body:     string(body),
		Timeout:  geminiCodeAssistProbeTimeout,
		ProxyURL: proxyURL,
	})
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail := strings.TrimSpace(response.Body)
		if len(detail) > 300 {
			detail = detail[:300]
		}
		return nil, fmt.Errorf("%s HTTP %d：%s", action, response.StatusCode, detail)
	}
	return parseTokenPayload(response.Body), nil
}

// geminiLoadTierID 提取 loadCodeAssist 报告的档位原始值：paidTier 优先、其次
// currentTier（与 sub2api_source GetTier 同序），两者均兼容字符串与 {id} 对象
// 两种上游形态。
func geminiLoadTierID(load map[string]any) string {
	for _, key := range []string{"paidTier", "currentTier"} {
		if id := geminiTierObjectID(load[key]); id != "" {
			return id
		}
	}
	return ""
}

// geminiTierObjectID 归一「字符串档位或 {id} 对象档位」两种上游形态。
func geminiTierObjectID(value any) string {
	switch tier := value.(type) {
	case string:
		return strings.TrimSpace(tier)
	case map[string]any:
		return normalizeText(tier["id"])
	}
	return ""
}

// extractGeminiCompanionProject 从 cloudcode-pa 响应提取项目 ID：
// cloudaicompanionProject 在参考实现里既有字符串形态也有 {id} 对象形态，顺带
// 兼容 projectId/project 键（与 CLIProxyAPI extractCloudaicompanionProject 同构）。
func extractGeminiCompanionProject(data map[string]any) string {
	if data == nil {
		return ""
	}
	for _, key := range []string{"cloudaicompanionProject", "projectId", "project"} {
		switch value := data[key].(type) {
		case string:
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		case map[string]any:
			if id := normalizeText(value["id"]); id != "" {
				return id
			}
		}
	}
	return ""
}

// geminiOnboardUserTierID 从 loadCodeAssist 响应挑选 onboardUser 的 tierId：
// 优先取 allowedTiers 中能被 canonicalGeminiTierID 归一的档位（isDefault 优先，
// 其次保持上游顺序），无可用档位时回落原始档位（isDefault 优先），再回落
// paidTier/currentTier 原始值，最后落 free-tier 兜底（两参考实现的 floor）。
func geminiOnboardUserTierID(oauthType string, load map[string]any, fallbackTier string) string {
	type tierOption struct {
		id        string
		isDefault bool
	}
	options := []tierOption{}
	if tiers, ok := load["allowedTiers"].([]any); ok {
		for _, raw := range tiers {
			tier, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			id := normalizeText(tier["id"])
			if id == "" {
				continue
			}
			isDefault, _ := tier["isDefault"].(bool)
			options = append(options, tierOption{id: id, isDefault: isDefault})
		}
	}
	for _, wantDefault := range []bool{true, false} {
		for _, option := range options {
			if option.isDefault != wantDefault {
				continue
			}
			if canonicalGeminiTierID(oauthType, option.id) != "" {
				return option.id
			}
		}
	}
	for _, wantDefault := range []bool{true, false} {
		for _, option := range options {
			if option.isDefault != wantDefault {
				continue
			}
			return option.id
		}
	}
	if fallbackTier != "" {
		return fallbackTier
	}
	return "free-tier"
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
	// 缺陷修复：plans.go 的 exchangeRefresh 以 fallback=nil 调用本函数，而
	// pick(info.ProjectID, fallback.ProjectID) 的第二参数在调用前即被求值，
	// 导致该流程必然空指针 panic（现象：gemini 刷新令牌建户 500/panic；根因：
	// 583/593 行未按同函数 568/587 行的既有模式对 fallback 判空）。
	projectFallback, quotaFallback := "", ""
	if fallback != nil {
		projectFallback = fallback.ProjectID
		quotaFallback = fallback.QuotaProjectID
	}
	if projectID := pick(info.ProjectID, projectFallback); projectID != "" {
		credentials["project_id"] = projectID
	}
	tierID := canonicalGeminiTierID(oauthType, info.TierID)
	if tierID == "" && fallback != nil {
		tierID = canonicalGeminiTierID(oauthType, fallback.TierID)
	}
	if tierID != "" {
		credentials["tier_id"] = tierID
	}
	if quotaProjectID := pick(info.QuotaProjectID, quotaFallback); quotaProjectID != "" {
		credentials["quota_project_id"] = quotaProjectID
	}
	if oauthType != "ai_studio" {
		// The accounts credentials write path consumes the decoded-JSON record
		// shape (lists as []any, mirroring a Node decryptJson product); a
		// Go-native []string here fails the 上游接口能力必须是数组 array check.
		credentials["supported_endpoint_modes"] = endpointModesJSONValue(geminiCLIEndpointModes)
	}
	return credentials
}

// endpointModesJSONValue widens the Go-native mode list onto the decoded-JSON
// []any shape (the credentials record contract the accounts write path
// validates).
func endpointModesJSONValue(modes []string) []any {
	output := make([]any, 0, len(modes))
	for _, mode := range modes {
		output = append(output, mode)
	}
	return output
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

// geminiRotationAccountOAuthType 只读判定列（#B4 ⑨，B01 收口）：refresh/
// reauthorize 读面使用 accounts.oauth_type 物理列；缺列即数据不完整，返回空串
// 交给调用方既有路径处理（refreshGeminiToken 按 normalizeGeminiOAuthType 把空
// 值归一为 code_assist 默认），不再按凭据推断。第二参数已退役，保留占位避免
// 调整调用方。
func geminiRotationAccountOAuthType(current *rotationAccount, _ map[string]any) string {
	if current != nil {
		if value := normalizeText(current.OAuthType); value != "" {
			return normalizeGeminiOAuthType(value)
		}
	}
	return ""
}

// geminiAccountOAuthType mirrors accountOAuthType: infer the stored account's
// oauth type from its credentials. B01 收口后不再出现在 rotation 读面
// （geminiRotationAccountOAuthType 只读判定列），仅保留独立启发式矩阵测试。
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

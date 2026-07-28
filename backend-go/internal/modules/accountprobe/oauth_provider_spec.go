package accountprobe

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
)

type OAuthProvider string

const (
	OAuthOpenAI    OAuthProvider = "openai"
	OAuthAnthropic OAuthProvider = "anthropic"
	OAuthGemini    OAuthProvider = "gemini"
	OAuthXAI       OAuthProvider = "xai"

	openAIClientID       = "app_EMoamEEZ73f0CkXaXp7hrann"
	anthropicClientID    = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	xaiClientID          = "b1a00492-073a-47ea-816f-4c329264a828"
	geminiCLIClientID    = "681255809395-oo8ft2oprdrnp9e3aqf6av3hmdib135j.apps.googleusercontent.com"
	geminiCLISecret      = "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"
	openAIRefreshScope   = "openid profile email"
	xaiDefaultTokenTTL   = 6 * time.Hour
	oauthResponseMaxSize = 256 << 10
)

var ErrInvalidOAuthSpec = errors.New("account probe OAuth provider spec is invalid")

type OAuthCredentials struct {
	provider OAuthProvider
	values   map[string]any
}

func ParseOAuthCredentials(provider OAuthProvider, values map[string]any) (OAuthCredentials, error) {
	if !validOAuthProvider(provider) {
		return OAuthCredentials{}, fmt.Errorf("%w: unsupported provider %q", ErrInvalidOAuthSpec, provider)
	}
	copyValues := cloneOAuthMap(values)
	if oauthString(copyValues, "access_token") == "" && oauthString(copyValues, "refresh_token") == "" {
		return OAuthCredentials{}, fmt.Errorf("%w: access_token or refresh_token is required", ErrInvalidOAuthSpec)
	}
	return OAuthCredentials{provider: provider, values: copyValues}, nil
}

func (OAuthCredentials) String() string               { return "[REDACTED]" }
func (OAuthCredentials) GoString() string             { return "[REDACTED]" }
func (OAuthCredentials) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }
func (c OAuthCredentials) AccessToken() string        { return oauthString(c.values, "access_token") }
func (c OAuthCredentials) HasRefreshToken() bool      { return oauthString(c.values, "refresh_token") != "" }
func (c OAuthCredentials) OAuthType() string          { return geminiRefreshOAuthType(c.values) }

func ShouldRefreshOAuth(c OAuthCredentials, now time.Time) bool {
	if c.provider == OAuthOpenAI && !c.HasRefreshToken() {
		return false
	}
	if c.AccessToken() == "" {
		return true
	}
	expiresAt, err := time.Parse(time.RFC3339, oauthString(c.values, "expires_at"))
	if err != nil {
		return true
	}
	lead := time.Minute
	if c.provider == OAuthXAI {
		lead = 5 * time.Minute
	}
	if c.provider == OAuthOpenAI {
		return expiresAt.Sub(now) < lead
	}
	return expiresAt.Sub(now) <= lead
}

type OAuthRefreshRequest struct {
	provider OAuthProvider
	url      string
	header   http.Header
	body     []byte
	context  map[string]string
	fallback *OAuthRefreshRequest
	timeout  time.Duration
}

func (OAuthRefreshRequest) String() string               { return "[REDACTED]" }
func (OAuthRefreshRequest) GoString() string             { return "[REDACTED]" }
func (OAuthRefreshRequest) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }
func (r OAuthRefreshRequest) Provider() OAuthProvider    { return r.provider }
func (r OAuthRefreshRequest) URL() string                { return r.url }
func (r OAuthRefreshRequest) Header() http.Header        { return r.header.Clone() }
func (r OAuthRefreshRequest) Body() []byte               { return append([]byte(nil), r.body...) }
func (r OAuthRefreshRequest) Timeout() time.Duration     { return r.timeout }
func (OAuthRefreshRequest) MaxResponseBytes() int        { return oauthResponseMaxSize }
func (r OAuthRefreshRequest) FallbackForResponse(status int, body []byte, truncated bool) (OAuthRefreshRequest, bool) {
	if truncated || len(body) > oauthResponseMaxSize || status >= 200 && status < 300 || !strings.Contains(strings.ToLower(string(body)), "unauthorized_client") {
		return OAuthRefreshRequest{}, false
	}
	if r.fallback == nil {
		return OAuthRefreshRequest{}, false
	}
	copyRequest := *r.fallback
	copyRequest.header = r.fallback.header.Clone()
	copyRequest.body = append([]byte(nil), r.fallback.body...)
	copyRequest.context = cloneOAuthStringMap(r.fallback.context)
	copyRequest.fallback = nil
	return copyRequest, true
}
func (r OAuthRefreshRequest) MaxAttempts() int {
	if r.provider == OAuthGemini {
		return 4
	}
	return 1
}
func (r OAuthRefreshRequest) RetryBackoff(attempt int) time.Duration {
	if r.provider != OAuthGemini || attempt < 1 || attempt >= r.MaxAttempts() {
		return 0
	}
	return time.Second << (attempt - 1)
}
func (r OAuthRefreshRequest) RetryableResponse(status, completedAttempts int, body []byte) bool {
	if r.provider != OAuthGemini || status >= 200 && status < 300 || completedAttempts >= r.MaxAttempts() {
		return false
	}
	lower := strings.ToLower(string(body))
	for _, code := range []string{"invalid_grant", "invalid_client", "unauthorized_client", "access_denied"} {
		if strings.Contains(lower, code) {
			return false
		}
	}
	return true
}

func BuildOAuthRefreshRequest(c OAuthCredentials) (OAuthRefreshRequest, error) {
	refreshToken := oauthString(c.values, "refresh_token")
	if refreshToken == "" {
		return OAuthRefreshRequest{}, fmt.Errorf("%w: refresh_token is required", ErrInvalidOAuthSpec)
	}
	request := OAuthRefreshRequest{provider: c.provider, header: make(http.Header), context: map[string]string{}, timeout: 25 * time.Second}
	form := make(url.Values)
	switch c.provider {
	case OAuthOpenAI:
		request.url = "https://auth.openai.com/oauth/token"
		form.Set("grant_type", "refresh_token")
		form.Set("refresh_token", refreshToken)
		form.Set("client_id", oauthStringOr(c.values, "client_id", openAIClientID))
		form.Set("scope", openAIRefreshScope)
		request.header.Set("Accept", "application/json")
		request.header.Set("Content-Type", "application/x-www-form-urlencoded")
	case OAuthAnthropic:
		request.url = "https://platform.claude.com/v1/oauth/token"
		clientID := oauthStringOr(c.values, "client_id", anthropicClientID)
		request.body, _ = json.Marshal(map[string]string{"grant_type": "refresh_token", "refresh_token": refreshToken, "client_id": clientID})
		request.context["client_id"] = clientID
		request.header.Set("Accept", "application/json, text/plain, */*")
		request.header.Set("Content-Type", "application/json")
		request.header.Set("User-Agent", "axios/1.13.6")
	case OAuthGemini:
		request.url = "https://oauth2.googleapis.com/token"
		oauthType := geminiRefreshOAuthType(c.values)
		clientID, clientSecret := oauthString(c.values, "client_id"), oauthString(c.values, "client_secret")
		if oauthType != "ai_studio" {
			legacyClientID, legacyClientSecret := clientID, clientSecret
			clientID, clientSecret = geminiCLIClientID, geminiCLIClientSecret()
			if legacyClientID != "" && legacyClientSecret != "" && (legacyClientID != clientID || legacyClientSecret != clientSecret) {
				fallbackValues := cloneOAuthMap(c.values)
				fallbackValues["oauth_type"] = "ai_studio"
				fallback, err := BuildOAuthRefreshRequest(OAuthCredentials{provider: OAuthGemini, values: fallbackValues})
				if err != nil {
					return OAuthRefreshRequest{}, err
				}
				fallback.context["oauth_type"] = oauthType
				fallback.context["base_url"] = oauthString(c.values, "base_url")
				request.fallback = &fallback
			}
		} else {
			clientID = oauthStringOrEnv(clientID, "GEMINI_OAUTH_CLIENT_ID")
			clientSecret = oauthStringOrEnv(clientSecret, "GEMINI_OAUTH_CLIENT_SECRET")
		}
		if clientID == "" || clientSecret == "" {
			return OAuthRefreshRequest{}, fmt.Errorf("%w: Gemini AI Studio requires client_id and client_secret", ErrInvalidOAuthSpec)
		}
		form.Set("grant_type", "refresh_token")
		form.Set("refresh_token", refreshToken)
		form.Set("client_id", clientID)
		form.Set("client_secret", clientSecret)
		request.context = map[string]string{
			"client_id": clientID, "client_secret": clientSecret, "oauth_type": oauthType,
			"project_id": oauthString(c.values, "project_id"), "tier_id": oauthString(c.values, "tier_id"),
			"quota_project_id": oauthString(c.values, "quota_project_id"), "base_url": oauthString(c.values, "base_url"),
			"scope": oauthString(c.values, "scope"),
		}
		request.header.Set("Accept", "application/json")
		request.header.Set("Content-Type", "application/x-www-form-urlencoded")
	case OAuthXAI:
		request.url = "https://auth.x.ai/oauth2/token"
		request.timeout = time.Minute
		clientID := oauthStringOr(c.values, "client_id", xaiClientID)
		form.Set("grant_type", "refresh_token")
		form.Set("client_id", clientID)
		form.Set("refresh_token", refreshToken)
		request.context["client_id"] = clientID
		request.header.Set("Accept", "application/json")
		request.header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.header.Set("User-Agent", "sub2api-grok-oauth/1.0")
	}
	if request.body == nil {
		request.body = []byte(form.Encode())
	}
	if c.provider != OAuthGemini {
		request.header.Set("Content-Length", strconv.Itoa(len(request.body)))
	}
	return request, nil
}

type OAuthRefreshResult struct {
	provider OAuthProvider
	values   map[string]any
}

func (OAuthRefreshResult) String() string               { return "[REDACTED]" }
func (OAuthRefreshResult) GoString() string             { return "[REDACTED]" }
func (OAuthRefreshResult) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }
func (r OAuthRefreshResult) RequiresGeminiEnrichment() bool {
	return r.provider == OAuthGemini && oauthString(r.values, "oauth_type") != "ai_studio"
}

func ParseOAuthRefreshResponse(request OAuthRefreshRequest, status int, body []byte, now time.Time) (OAuthRefreshResult, error) {
	if !validOAuthProvider(request.provider) || status < 200 || status >= 300 {
		return OAuthRefreshResult{}, fmt.Errorf("%w: OAuth token HTTP status %d", ErrInvalidOAuthSpec, status)
	}
	if len(body) == 0 || len(body) > oauthResponseMaxSize {
		return OAuthRefreshResult{}, fmt.Errorf("%w: OAuth token response size", ErrInvalidOAuthSpec)
	}
	var payload map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil || payload == nil {
		return OAuthRefreshResult{}, fmt.Errorf("%w: OAuth token response JSON", ErrInvalidOAuthSpec)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return OAuthRefreshResult{}, fmt.Errorf("%w: OAuth token response trailing JSON", ErrInvalidOAuthSpec)
	}
	accessToken := oauthString(payload, "access_token")
	if accessToken == "" {
		return OAuthRefreshResult{}, fmt.Errorf("%w: OAuth token response missing access_token", ErrInvalidOAuthSpec)
	}
	values := map[string]any{"access_token": accessToken}
	copyOAuthText(payload, values, "refresh_token", "id_token", "scope", "token_type")
	switch request.provider {
	case OAuthOpenAI:
		expires, err := positiveOAuthSeconds(payload["expires_in"], true)
		if err != nil {
			return OAuthRefreshResult{}, err
		}
		if expires == 0 {
			return OAuthRefreshResult{}, fmt.Errorf("%w: OAuth token response missing expires_in", ErrInvalidOAuthSpec)
		}
		values["expires_at"] = now.Add(time.Duration(expires) * time.Second).UTC().Format(time.RFC3339Nano)
		values["client_id"] = requestFormValue(request.body, "client_id", openAIClientID)
		values["base_url"] = "https://api.openai.com/v1"
		mergeOpenAIClaims(values, oauthString(payload, "id_token"), accessToken)
	case OAuthAnthropic:
		if expires := finitePositiveOAuthSeconds(payload["expires_in"]); expires > 0 {
			values["expires_at"] = now.Add(time.Duration(expires) * time.Second).UTC().Format(time.RFC3339Nano)
		}
		values["client_id"] = request.context["client_id"]
		values["base_url"] = "https://api.anthropic.com/v1"
		if account, ok := payload["account"].(map[string]any); ok {
			setOAuthText(values, "email", account["email_address"])
			setOAuthText(values, "account_id", account["uuid"])
		}
		if organization, ok := payload["organization"].(map[string]any); ok {
			setOAuthText(values, "organization_id", organization["uuid"])
		}
	case OAuthGemini:
		if expires := finitePositiveOAuthSeconds(payload["expires_in"]); expires > 0 {
			safe := expires - 300
			if safe < 30 {
				safe = 30
			}
			values["expires_at"] = now.Add(time.Duration(safe) * time.Second).UTC().Format(time.RFC3339Nano)
		}
		for key, value := range request.context {
			if key == "scope" && oauthString(values, "scope") != "" {
				continue
			}
			if value != "" {
				values[key] = value
			}
		}
		if request.context["oauth_type"] != "ai_studio" {
			values["supported_endpoint_modes"] = []string{string(ModeGenerateContentJSON), string(ModeGenerateContentSSE)}
		}
		if oauthString(values, "base_url") == "" {
			if request.context["oauth_type"] == "ai_studio" {
				values["base_url"] = "https://generativelanguage.googleapis.com"
			} else {
				values["base_url"] = "https://cloudcode-pa.googleapis.com"
			}
		}
	case OAuthXAI:
		expires := finitePositiveOAuthSeconds(payload["expires_in"])
		if expires == 0 {
			expires = int64(xaiDefaultTokenTTL / time.Second)
		}
		values["expires_at"] = now.Add(time.Duration(expires) * time.Second).UTC().Format(time.RFC3339Nano)
		values["client_id"] = request.context["client_id"]
		values["base_url"] = "https://cli-chat-proxy.grok.com/v1"
		if oauthString(values, "token_type") == "" {
			values["token_type"] = "Bearer"
		}
		mergeXAIClaims(values, oauthString(payload, "id_token"), accessToken)
	}
	return OAuthRefreshResult{provider: request.provider, values: values}, nil
}

type OAuthCredentialPatch struct{ values map[string]any }

func (OAuthCredentialPatch) String() string               { return "[REDACTED]" }
func (OAuthCredentialPatch) GoString() string             { return "[REDACTED]" }
func (OAuthCredentialPatch) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }
func (p OAuthCredentialPatch) Values() map[string]any     { return cloneOAuthMap(p.values) }

func MergeOAuthRefreshCredentials(current OAuthCredentials, result OAuthRefreshResult) (OAuthCredentialPatch, error) {
	if current.provider != result.provider || !validOAuthProvider(result.provider) {
		return OAuthCredentialPatch{}, fmt.Errorf("%w: provider mismatch", ErrInvalidOAuthSpec)
	}
	merged := cloneOAuthMap(current.values)
	for key, value := range result.values {
		merged[key] = value
	}
	if oauthString(result.values, "refresh_token") == "" && oauthString(current.values, "refresh_token") != "" {
		merged["refresh_token"] = oauthString(current.values, "refresh_token")
	}
	if (result.provider == OAuthAnthropic || result.provider == OAuthXAI) && oauthString(current.values, "base_url") != "" {
		merged["base_url"] = oauthString(current.values, "base_url")
	}
	return OAuthCredentialPatch{values: merged}, nil
}

type OAuthAttempt struct {
	method, url  string
	header       http.Header
	body         []byte
	evidenceMode EndpointMode
}

func (OAuthAttempt) String() string               { return "[REDACTED]" }
func (OAuthAttempt) GoString() string             { return "[REDACTED]" }
func (OAuthAttempt) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }
func (a OAuthAttempt) Method() string             { return a.method }
func (a OAuthAttempt) URL() string                { return a.url }
func (a OAuthAttempt) Header() http.Header        { return a.header.Clone() }
func (a OAuthAttempt) Body() []byte               { return append([]byte(nil), a.body...) }
func (a OAuthAttempt) EvidenceMode() EndpointMode { return a.evidenceMode }
func (a OAuthAttempt) XAIModelFallback(status int, body []byte, truncated bool) (OAuthAttempt, bool) {
	if status != http.StatusForbidden || truncated || len(body) == 0 || len(body) > 64<<10 || !strings.Contains(strings.ToLower(string(body)), "access denied") || !strings.EqualFold(a.header.Get("X-Xai-Token-Auth"), "xai-grok-cli") || !strings.HasPrefix(strings.ToLower(a.header.Get("Authorization")), "bearer ") {
		return OAuthAttempt{}, false
	}
	fallbackURL, err := url.Parse(a.url)
	if err != nil || !strings.EqualFold(fallbackURL.Hostname(), "cli-chat-proxy.grok.com") {
		return OAuthAttempt{}, false
	}
	fallbackURL.Scheme, fallbackURL.Host, fallbackURL.RawPath = "https", "api.x.ai", ""
	header := a.header.Clone()
	for _, name := range []string{"X-Xai-Token-Auth", "X-Grok-Client-Version", "X-Grok-Client-Surface", "X-Userid", "X-Email", "User-Agent"} {
		header.Del(name)
	}
	return OAuthAttempt{method: a.method, url: fallbackURL.String(), header: header, body: append([]byte(nil), a.body...), evidenceMode: a.evidenceMode}, true
}

func PrepareOAuthAttempt(candidate gatewaycandidatewindow.Candidate, prepared PreparedRequest) (OAuthAttempt, error) {
	identity := gatewaycandidatewindow.EffectiveAccountIdentity(candidate)
	provider, ok := oauthProviderForIdentity(identity.ProviderCode, identity.Type)
	if !ok {
		return OAuthAttempt{}, fmt.Errorf("%w: unsupported OAuth identity", ErrInvalidOAuthSpec)
	}
	values := make(map[string]any)
	for _, key := range []string{"access_token", "refresh_token", "client_id", "client_secret", "expires_at", "account_id", "chatgpt_account_id", "base_url", "quota_project_id", "oauth_type", "project_id"} {
		if value, exists := candidate.Credentials.Value(key); exists {
			values[key] = value
		}
	}
	credentials, err := ParseOAuthCredentials(provider, values)
	if err != nil || credentials.AccessToken() == "" {
		return OAuthAttempt{}, fmt.Errorf("%w: access_token is required", ErrInvalidOAuthSpec)
	}
	request := prepared.Request
	header := request.Header.Clone()
	header.Del("X-Juhe-Client-Profile")
	body := append([]byte(nil), request.Body...)
	evidenceMode := request.Mode
	var attemptURL string
	switch provider {
	case OAuthOpenAI:
		if request.Mode != ModeResponsesJSON && request.Mode != ModeResponsesSSE {
			return OAuthAttempt{}, fmt.Errorf("%w: OpenAI OAuth supports Responses only", ErrInvalidOAuthSpec)
		}
		path := strings.TrimPrefix(request.PathAndQuery, "/v1")
		attemptURL = "https://chatgpt.com/backend-api/codex" + path
		header.Set("Authorization", "Bearer "+credentials.AccessToken())
		header.Set("OpenAI-Beta", "responses=experimental")
		body, err = normalizeOpenAIProbeOAuthBody(header, body, request.Model)
		if err != nil {
			return OAuthAttempt{}, err
		}
		evidenceMode = ModeResponsesSSE
		if accountID := oauthStringOr(values, "account_id", oauthString(values, "chatgpt_account_id")); accountID != "" {
			header.Set("ChatGPT-Account-Id", accountID)
		}
	case OAuthAnthropic:
		if request.Mode != ModeMessagesJSON && request.Mode != ModeMessagesSSE {
			return OAuthAttempt{}, fmt.Errorf("%w: Anthropic OAuth supports Messages only", ErrInvalidOAuthSpec)
		}
		attemptURL, err = buildVersionedURL(oauthStringOr(values, "base_url", candidate.DefaultBaseURL), withQueryValue(request.PathAndQuery, "beta", "true"), "v1")
		if err != nil {
			return OAuthAttempt{}, err
		}
		header.Set("Authorization", "Bearer "+credentials.AccessToken())
		header.Set("Anthropic-Version", "2023-06-01")
		header.Set("Anthropic-Beta", "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,fine-grained-tool-streaming-2025-05-14,effort-2025-11-24")
		if request.Mode == ModeMessagesSSE {
			header.Set("Accept", "text/event-stream")
		} else {
			header.Set("Accept", "application/json")
		}
		applyAnthropicOAuthIdentity(header)
	case OAuthGemini:
		if request.Mode != ModeGenerateContentJSON && request.Mode != ModeGenerateContentSSE {
			return OAuthAttempt{}, fmt.Errorf("%w: Gemini OAuth supports GenerateContent only", ErrInvalidOAuthSpec)
		}
		if geminiRuntimeOAuthType(values) != "ai_studio" {
			projectID := oauthString(values, "project_id")
			if projectID == "" {
				return OAuthAttempt{}, fmt.Errorf("%w: Gemini Code Assist project_id is required", ErrInvalidOAuthSpec)
			}
			var inner map[string]any
			if json.Unmarshal(body, &inner) != nil {
				return OAuthAttempt{}, fmt.Errorf("%w: Gemini request body", ErrInvalidOAuthSpec)
			}
			body, _ = json.Marshal(map[string]any{"model": request.Model, "project": projectID, "request": inner})
			attemptURL = "https://cloudcode-pa.googleapis.com/v1internal:streamGenerateContent?alt=sse"
			evidenceMode = ModeGenerateContentSSE
			header = make(http.Header)
			header.Set("User-Agent", "GeminiCLI/0.1.5 (Windows; AMD64)")
		} else {
			attemptURL, err = buildGeminiURL(oauthStringOr(values, "base_url", candidate.DefaultBaseURL), request.PathAndQuery)
			if err != nil {
				return OAuthAttempt{}, err
			}
			if quota := oauthString(values, "quota_project_id"); quota != "" {
				header.Set("X-Goog-User-Project", quota)
			}
			if request.Mode == ModeGenerateContentSSE {
				header.Set("Accept", "text/event-stream")
			} else {
				header.Set("Accept", "application/json")
			}
		}
		header.Del("X-Goog-Api-Key")
		header.Set("Authorization", "Bearer "+credentials.AccessToken())
	case OAuthXAI:
		if request.Mode != ModeResponsesJSON && request.Mode != ModeResponsesSSE {
			return OAuthAttempt{}, fmt.Errorf("%w: xAI OAuth supports Responses only", ErrInvalidOAuthSpec)
		}
		baseURL := oauthStringOr(values, "base_url", "https://cli-chat-proxy.grok.com/v1")
		attemptURL, err = buildVersionedURL(baseURL, request.PathAndQuery, "v1")
		if err != nil {
			return OAuthAttempt{}, err
		}
		header.Set("Authorization", "Bearer "+credentials.AccessToken())
		header.Set("Accept", "application/json, text/event-stream")
		if parsed, parseErr := url.Parse(baseURL); parseErr == nil && strings.EqualFold(parsed.Hostname(), "cli-chat-proxy.grok.com") {
			header.Set("User-Agent", "xai-grok-workspace/0.2.93")
			header.Set("X-Xai-Token-Auth", "xai-grok-cli")
			header.Set("X-Grok-Client-Version", "0.2.93")
		} else {
			header.Del("X-Xai-Token-Auth")
			header.Del("X-Grok-Client-Version")
		}
	}
	header.Set("Content-Type", "application/json")
	return OAuthAttempt{method: request.Method, url: attemptURL, header: header, body: body, evidenceMode: evidenceMode}, nil
}

func applyAnthropicOAuthIdentity(header http.Header) {
	for key, value := range map[string]string{
		"User-Agent": "claude-cli/2.1.161 (external, cli)", "X-Stainless-Lang": "js", "X-Stainless-Package-Version": "0.94.0",
		"X-Stainless-Os": "Linux", "X-Stainless-Arch": "arm64", "X-Stainless-Runtime": "node", "X-Stainless-Runtime-Version": "v24.3.0",
		"X-Stainless-Retry-Count": "0", "X-Stainless-Timeout": "600", "X-App": "cli", "Anthropic-Dangerous-Direct-Browser-Access": "true",
	} {
		header.Set(key, value)
	}
}

func oauthProviderForIdentity(providerCode, accountType string) (OAuthProvider, bool) {
	switch strings.ToLower(strings.TrimSpace(providerCode)) {
	case "openai", "gpt":
		return OAuthOpenAI, strings.EqualFold(accountType, "oauth")
	case "anthropic":
		return OAuthAnthropic, strings.EqualFold(accountType, "oauth")
	case "gemini":
		return OAuthGemini, strings.EqualFold(accountType, "google_oauth")
	case "xai":
		return OAuthXAI, strings.EqualFold(accountType, "oauth")
	default:
		return "", false
	}
}

func validOAuthProvider(provider OAuthProvider) bool {
	return provider == OAuthOpenAI || provider == OAuthAnthropic || provider == OAuthGemini || provider == OAuthXAI
}
func cloneOAuthMap(values map[string]any) map[string]any {
	output := make(map[string]any, len(values))
	for key, value := range values {
		output[key] = cloneOAuthValue(value)
	}
	return output
}
func cloneOAuthValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneOAuthMap(typed)
	case []any:
		output := make([]any, len(typed))
		for index, item := range typed {
			output[index] = cloneOAuthValue(item)
		}
		return output
	case []string:
		return append([]string(nil), typed...)
	default:
		return value
	}
}
func cloneOAuthStringMap(values map[string]string) map[string]string {
	output := make(map[string]string, len(values))
	for key, value := range values {
		output[key] = value
	}
	return output
}
func oauthString(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return strings.TrimSpace(value)
}
func oauthStringOr(values map[string]any, key, fallback string) string {
	if value := oauthString(values, key); value != "" {
		return value
	}
	return strings.TrimSpace(fallback)
}
func setOAuthText(output map[string]any, key string, value any) {
	if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
		output[key] = strings.TrimSpace(text)
	}
}
func copyOAuthText(input, output map[string]any, keys ...string) {
	for _, key := range keys {
		setOAuthText(output, key, input[key])
	}
}
func geminiRefreshOAuthType(values map[string]any) string {
	value := strings.ToLower(oauthString(values, "oauth_type"))
	if value == "ai_studio" || value == "code_assist" || value == "google_one" {
		return value
	}
	return "code_assist"
}
func geminiRuntimeOAuthType(values map[string]any) string {
	value := strings.ToLower(oauthString(values, "oauth_type"))
	if value == "ai_studio" || value == "code_assist" || value == "google_one" {
		return value
	}
	if oauthString(values, "project_id") != "" {
		return "code_assist"
	}
	return "ai_studio"
}
func positiveOAuthSeconds(value any, allowNumericString bool) (int64, error) {
	if value == nil {
		return 0, nil
	}
	var number float64
	switch typed := value.(type) {
	case json.Number:
		var err error
		number, err = typed.Float64()
		if err != nil {
			return 0, fmt.Errorf("%w: invalid expires_in", ErrInvalidOAuthSpec)
		}
	case float64:
		number = typed
	case string:
		if !allowNumericString {
			return 0, fmt.Errorf("%w: invalid expires_in", ErrInvalidOAuthSpec)
		}
		var err error
		number, err = strconv.ParseFloat(typed, 64)
		if err != nil {
			return 0, fmt.Errorf("%w: invalid expires_in", ErrInvalidOAuthSpec)
		}
	default:
		return 0, fmt.Errorf("%w: invalid expires_in", ErrInvalidOAuthSpec)
	}
	seconds := int64(number)
	if number <= 0 || seconds <= 0 || seconds > int64((1<<63-1)/int64(time.Second)) || (!allowNumericString && float64(seconds) != number) {
		return 0, fmt.Errorf("%w: invalid expires_in", ErrInvalidOAuthSpec)
	}
	return seconds, nil
}

func finitePositiveOAuthSeconds(value any) int64 {
	var number float64
	switch typed := value.(type) {
	case json.Number:
		number, _ = typed.Float64()
	case float64:
		number = typed
	case string:
		number, _ = strconv.ParseFloat(strings.TrimSpace(typed), 64)
	case bool:
		if typed {
			number = 1
		}
	default:
		return 0
	}
	seconds := int64(number)
	if number <= 0 || seconds <= 0 || seconds > int64((1<<63-1)/int64(time.Second)) {
		return 0
	}
	return seconds
}

func normalizeOpenAIProbeOAuthBody(header http.Header, body []byte, model string) ([]byte, error) {
	var object map[string]any
	if err := json.Unmarshal(body, &object); err != nil || object == nil {
		return nil, fmt.Errorf("%w: OpenAI OAuth request body", ErrInvalidOAuthSpec)
	}
	for _, field := range []string{"background", "conversation", "context_management", "frequency_penalty", "max_completion_tokens", "max_output_tokens", "metadata", "presence_penalty", "prompt_cache_retention", "safety_identifier", "stream_options", "temperature", "top_p", "truncation", "user"} {
		delete(object, field)
	}
	object["store"] = false
	object["stream"] = true
	if header.Get("Session-Id") == "" || objectValue(object["client_metadata"]) == nil {
		normalizeCodexResponsesRequest(header, object, model)
	}
	ensureOpenAIOAuthReasoningInclude(object)
	header.Set("Accept", "text/event-stream")
	normalized, err := json.Marshal(object)
	if err != nil {
		return nil, fmt.Errorf("%w: OpenAI OAuth request body", ErrInvalidOAuthSpec)
	}
	return normalized, nil
}
func ensureOpenAIOAuthReasoningInclude(object map[string]any) {
	if len(objectValue(object["reasoning"])) == 0 {
		return
	}
	const required = "reasoning.encrypted_content"
	switch include := object["include"].(type) {
	case nil:
		object["include"] = []string{required}
	case []string:
		for _, value := range include {
			if value == required {
				return
			}
		}
		object["include"] = append(include, required)
	case []any:
		for _, value := range include {
			if value == required {
				return
			}
		}
		object["include"] = append(include, required)
	}
}
func geminiCLIClientSecret() string {
	if value := strings.TrimSpace(os.Getenv("GEMINI_CLI_OAUTH_CLIENT_SECRET")); value != "" {
		return value
	}
	return geminiCLISecret
}
func oauthStringOrEnv(value, name string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return strings.TrimSpace(os.Getenv(name))
}
func requestFormValue(body []byte, key, fallback string) string {
	values, _ := url.ParseQuery(string(body))
	if value := strings.TrimSpace(values.Get(key)); value != "" {
		return value
	}
	return fallback
}
func jwtClaims(token string) map[string]any {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) < 2 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var claims map[string]any
	if json.Unmarshal(payload, &claims) != nil {
		return nil
	}
	return claims
}
func mergeOpenAIClaims(values map[string]any, tokens ...string) {
	for _, token := range tokens {
		claims := jwtClaims(token)
		if claims == nil {
			continue
		}
		setFirstOAuth(values, "email", claims["email"])
		auth, _ := claims["https://api.openai.com/auth"].(map[string]any)
		if auth == nil {
			continue
		}
		setFirstOAuth(values, "account_id", auth["chatgpt_account_id"])
		setFirstOAuth(values, "chatgpt_user_id", auth["chatgpt_user_id"], auth["user_id"])
		setFirstOAuth(values, "plan_type", auth["chatgpt_plan_type"])
	}
}
func mergeXAIClaims(values map[string]any, tokens ...string) {
	for _, token := range tokens {
		claims := jwtClaims(token)
		setFirstOAuth(values, "email", claims["email"])
		setFirstOAuth(values, "sub", claims["sub"])
		setFirstOAuth(values, "team_id", claims["team_id"])
		setFirstOAuth(values, "subscription_tier", claims["subscription_tier"])
		setFirstOAuth(values, "entitlement_status", claims["entitlement_status"])
	}
}
func setFirstOAuth(output map[string]any, key string, values ...any) {
	if oauthString(output, key) != "" {
		return
	}
	for _, value := range values {
		if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
			output[key] = strings.TrimSpace(text)
			return
		}
	}
}

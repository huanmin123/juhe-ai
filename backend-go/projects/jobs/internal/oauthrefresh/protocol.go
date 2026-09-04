package oauthrefresh

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// TokenHTTPRequest mirrors the provider-oauth-token-transport input the Node
// services build: a fully-formed upstream token request.
type TokenHTTPRequest struct {
	URL     string
	Headers map[string]string
	Body    string
	// Timeout bounds a single upstream call. Zero falls back to
	// DefaultTokenTimeout (the 25s Node default).
	Timeout time.Duration
}

// TokenHTTPResponse is the upstream token response subset the services parse.
type TokenHTTPResponse struct {
	StatusCode int
	Body       string
}

// TokenExchanger is the injected upstream transport boundary (Node
// requestProviderOAuthToken). Tests supply stub exchangers; the package never
// performs a real network call unless the production exchanger is wired.
type TokenExchanger interface {
	Do(ctx context.Context, request TokenHTTPRequest) (TokenHTTPResponse, error)
}

// ExchangerFunc adapts a function to TokenExchanger.
type ExchangerFunc func(ctx context.Context, request TokenHTTPRequest) (TokenHTTPResponse, error)

// Do implements TokenExchanger.
func (f ExchangerFunc) Do(ctx context.Context, request TokenHTTPRequest) (TokenHTTPResponse, error) {
	return f(ctx, request)
}

// DefaultTokenTimeout mirrors openAIOAuthTokenRequestTimeoutMs (25s; grok uses
// a 60s magnitude in Node).
const DefaultTokenTimeout = 25 * time.Second

// tokenResponseMaxBytes mirrors openAIOAuthTokenResponseMaxBytes.
const tokenResponseMaxBytes = 256 * 1024

// HTTPTokenExchanger is the production transport: form/JSON POST without
// proxying. Tests never construct this type, so no test traffic leaves the
// process.
type HTTPTokenExchanger struct {
	Client *http.Client
}

// NewHTTPTokenExchanger builds the default exchanger.
func NewHTTPTokenExchanger() *HTTPTokenExchanger {
	return &HTTPTokenExchanger{Client: &http.Client{}}
}

// Do implements TokenExchanger.
func (e *HTTPTokenExchanger) Do(ctx context.Context, request TokenHTTPRequest) (TokenHTTPResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := request.Timeout
	if timeout <= 0 {
		timeout = DefaultTokenTimeout
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, request.URL, strings.NewReader(request.Body))
	if err != nil {
		return TokenHTTPResponse{}, err
	}
	for key, value := range request.Headers {
		req.Header.Set(key, value)
	}
	client := e.Client
	if client == nil {
		client = &http.Client{}
	}
	response, err := client.Do(req)
	if err != nil {
		return TokenHTTPResponse{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, tokenResponseMaxBytes+1))
	if err != nil {
		return TokenHTTPResponse{}, err
	}
	if len(body) > tokenResponseMaxBytes {
		return TokenHTTPResponse{}, errors.New("OAuth 令牌响应体过大")
	}
	return TokenHTTPResponse{StatusCode: response.StatusCode, Body: string(body)}, nil
}

// UpstreamError mirrors OAuthUpstreamResponseError: an upstream token endpoint
// failure whose message surfaces verbatim (the refresh failure record stores
// the sanitized copy verbatim).
type UpstreamError struct {
	Message    string
	StatusCode int
}

func (e *UpstreamError) Error() string { return e.Message }

// upstreamError wraps a non-2xx token response the way the Node services do:
// "X OAuth 令牌请求失败：HTTP {status}，{detail}".
func upstreamError(label string, statusCode int, detail string) *UpstreamError {
	message := fmt.Sprintf("%s OAuth 令牌请求失败：HTTP %d", label, statusCode)
	if detail != "" {
		message += "，" + detail
	}
	return &UpstreamError{Message: message, StatusCode: 502}
}

// AsUpstreamError reports whether err is an upstream token endpoint failure.
func AsUpstreamError(err error) (*UpstreamError, bool) {
	var upstream *UpstreamError
	if errors.As(err, &upstream) {
		return upstream, true
	}
	return nil, false
}

// parseTokenPayload mirrors the shared JSON fallback parse: invalid JSON keeps
// the raw body under "raw".
func parseTokenPayload(body string) map[string]any {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return map[string]any{}
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil || payload == nil {
		return map[string]any{"raw": trimmed}
	}
	return payload
}

// normalizeText mirrors normalizeString: trimmed string or "".
func normalizeText(value any) string {
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}

// finitePositiveInt mirrors finitePositiveInteger (JSON numbers are float64).
func finitePositiveInt(value any) (int, bool) {
	number, ok := value.(float64)
	if !ok || number != number || number <= 0 {
		return 0, false
	}
	return int(number), true
}

// decodeJWTClaims mirrors decodeJwtClaims: the base64url JSON payload of a
// compact JWS, or an empty object when absent/malformed.
func decodeJWTClaims(token string) map[string]any {
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		return map[string]any{}
	}
	parts := strings.Split(trimmed, ".")
	if len(parts) < 2 || parts[1] == "" {
		return map[string]any{}
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return map[string]any{}
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil || claims == nil {
		return map[string]any{}
	}
	return claims
}

// mergeJWTClaims mirrors mergeJwtClaims: earlier tokens win for defined values.
func mergeJWTClaims(tokens ...string) map[string]any {
	output := map[string]any{}
	for _, token := range tokens {
		for key, value := range decodeJWTClaims(token) {
			if existing, ok := output[key]; ok {
				if normalizeText(existing) != "" {
					continue
				}
			}
			output[key] = value
		}
	}
	return output
}

// encodeForm mirrors new URLSearchParams(form).toString(): sorted keys,
// percent-encoded.
func encodeForm(form map[string]string) string {
	values := url.Values{}
	for key, value := range form {
		values.Set(key, value)
	}
	return values.Encode()
}

// formRequest builds the shared application/x-www-form-urlencoded token
// request (openai/gemini/grok shape, headers identical to
// buildOpenAIOAuthTokenHttpRequest).
func formRequest(tokenURL string, form map[string]string) TokenHTTPRequest {
	body := encodeForm(form)
	return TokenHTTPRequest{
		URL: tokenURL,
		Headers: map[string]string{
			"accept":         "application/json",
			"content-type":   "application/x-www-form-urlencoded",
			"content-length": itoa(len(body)),
		},
		Body: body,
	}
}

// jsonRequest builds the JSON token request (anthropic shape with the axios
// user-agent).
func jsonRequest(tokenURL string, payload map[string]string) TokenHTTPRequest {
	body, _ := json.Marshal(payload)
	return TokenHTTPRequest{
		URL: tokenURL,
		Headers: map[string]string{
			"accept":         "application/json, text/plain, */*",
			"content-type":   "application/json",
			"content-length": itoa(len(body)),
			"user-agent":     "axios/1.13.6",
		},
		Body: string(body),
	}
}

// exchange performs one upstream call with the exchanger default timeout.
func exchange(ctx context.Context, ex TokenExchanger, request TokenHTTPRequest) (TokenHTTPResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return ex.Do(ctx, request)
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	digits := ""
	for v > 0 {
		digits = string(rune('0'+v%10)) + digits
		v /= 10
	}
	return digits
}

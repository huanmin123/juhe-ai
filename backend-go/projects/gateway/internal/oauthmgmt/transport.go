package oauthmgmt

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

// TokenHTTPRequest mirrors the provider-oauth-token-transport call input the
// Node services build: a fully-formed upstream token request.
type TokenHTTPRequest struct {
	URL     string
	Headers map[string]string
	Body    string
	// Timeout bounds a single upstream call (25s for openai/anthropic/gemini,
	// 60s for grok in Node). Zero falls back to the exchanger default.
	Timeout time.Duration
}

// TokenHTTPResponse is the upstream token response subset the services parse.
type TokenHTTPResponse struct {
	StatusCode int
	Body       string
}

// TokenExchanger is the injected upstream transport boundary (Node
// requestProviderOAuthToken). Tests supply httptest-backed stubs; the slice
// never performs a real network call unless a production exchanger is wired.
type TokenExchanger interface {
	Do(ctx context.Context, request TokenHTTPRequest) (TokenHTTPResponse, error)
}

// ExchangerFunc adapts a function to TokenExchanger.
type ExchangerFunc func(ctx context.Context, request TokenHTTPRequest) (TokenHTTPResponse, error)

// Do implements TokenExchanger.
func (f ExchangerFunc) Do(ctx context.Context, request TokenHTTPRequest) (TokenHTTPResponse, error) {
	return f(ctx, request)
}

// defaultTokenTimeout mirrors openAIOAuthTokenRequestTimeoutMs and its
// per-provider overrides of the same magnitude.
const defaultTokenTimeout = 25 * time.Second

// httpTokenExchanger is the production transport: form/JSON POST without
// proxying. The OAuth proxy-profile resolution rides the proxy slice (see the
// M17 deferral notes); tests never construct this type, so no test traffic
// leaves the process.
type httpTokenExchanger struct {
	client *http.Client
}

// NewHTTPTokenExchanger builds the default exchanger.
func NewHTTPTokenExchanger() TokenExchanger {
	return &httpTokenExchanger{client: &http.Client{}}
}

func (e *httpTokenExchanger) Do(ctx context.Context, request TokenHTTPRequest) (TokenHTTPResponse, error) {
	timeout := request.Timeout
	if timeout <= 0 {
		timeout = defaultTokenTimeout
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
	response, err := e.client.Do(req)
	if err != nil {
		return TokenHTTPResponse{}, err
	}
	defer response.Body.Close()
	const maxBytes = 256 * 1024
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return TokenHTTPResponse{}, err
	}
	if len(body) > maxBytes {
		return TokenHTTPResponse{}, errors.New("OAuth 令牌响应体过大")
	}
	return TokenHTTPResponse{StatusCode: response.StatusCode, Body: string(body)}, nil
}

// UpstreamError mirrors OAuthUpstreamResponseError: an upstream token endpoint
// failure whose message surfaces verbatim and whose status decides the HTTP
// mapping (502 normally, 400/403 for the grok entitlement branches).
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

// statusFromError maps an error onto the HTTP status the route family renders:
// 502 for upstream failures, 400 for grok-style client errors, everything else
// 502 as well (the route falls back to its own message for non-upstream
// errors).
func upstreamStatus(err error) (int, bool) {
	var upstream *UpstreamError
	if errors.As(err, &upstream) {
		return upstream.StatusCode, true
	}
	return 0, false
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

// finitePositiveInt mirrors finitePositiveInteger.
func finitePositiveInt(value any) (int, bool) {
	number, ok := value.(float64)
	if !ok || !isFinite(number) || number <= 0 {
		return 0, false
	}
	return int(number), true
}

func isFinite(value float64) bool {
	return value == value && value <= 1.7976931348623157e308 && value >= -1.7976931348623157e308
}

// isoMillisMillis mirrors new Date(...).toISOString() millisecond precision.
func isoFromMillis(milliseconds int64) string {
	return time.UnixMilli(milliseconds).UTC().Format("2006-01-02T15:04:05.000") + "Z"
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
// request (openai/gemini/grok shape).
func formRequest(url string, form map[string]string) TokenHTTPRequest {
	body := encodeForm(form)
	return TokenHTTPRequest{
		URL: url,
		Headers: map[string]string{
			"accept":       "application/json",
			"content-type": "application/x-www-form-urlencoded",
			"content-length": func() string {
				return itoa(len(body))
			}(),
		},
		Body: body,
	}
}

// jsonRequest builds the JSON token request (anthropic shape).
func jsonRequest(url string, payload map[string]string) TokenHTTPRequest {
	body, _ := json.Marshal(payload)
	return TokenHTTPRequest{
		URL: url,
		Headers: map[string]string{
			"accept":         "application/json, text/plain, */*",
			"content-type":   "application/json",
			"content-length": itoa(len(body)),
			"user-agent":     "axios/1.13.6",
		},
		Body: string(body),
	}
}

// ensureContext mirrors the slices' nil-context tolerance.
func ensureContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

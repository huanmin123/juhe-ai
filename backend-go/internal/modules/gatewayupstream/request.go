// Package gatewayupstream builds bounded, owned upstream HTTP requests without
// dispatching them. Network policy, proxy selection, retries, and response
// handling remain separate gateway responsibilities.
package gatewayupstream

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/http/httpguts"

	"juhe-ai/backend-go/internal/protocols/gateway"
	"juhe-ai/backend-go/internal/store/port"
)

const (
	DefaultMaxBodyBytes     int64 = 64 << 20
	maxCredentialBytes            = 8192
	defaultAnthropicVersion       = "2023-06-01"
)

var (
	ErrBodyTooLarge         = errors.New("上游请求体超过限制")
	ErrContextRequired      = errors.New("上游请求缺少 context")
	ErrInvalidBaseURL       = errors.New("上游 Base URL 无效")
	ErrInvalidCredential    = errors.New("上游凭据无效")
	ErrInvalidHeader        = errors.New("上游请求 header 无效")
	ErrInvalidRequestTarget = errors.New("上游请求目标无效")
	ErrUnsupportedProtocol  = errors.New("上游协议不受支持")
)

// AuthMode selects the protocol-specific location for an upstream credential.
// AuthAuto derives the mode from the shared protocol registry and candidate.
type AuthMode string

const (
	AuthAuto            AuthMode = ""
	AuthBearer          AuthMode = "bearer"
	AuthAnthropicAPIKey AuthMode = "anthropic_api_key"
	AuthGeminiAPIKey    AuthMode = "gemini_api_key"
)

// CredentialOptions holds non-secret upstream authentication metadata.
type CredentialOptions struct {
	AuthMode       AuthMode
	AccountID      string
	QuotaProjectID string
}

// Credential keeps the secret private so default formatting and JSON encoding
// cannot accidentally expose it.
type Credential struct {
	secret         string
	authMode       AuthMode
	accountID      string
	quotaProjectID string
}

func NewCredential(secret string, options CredentialOptions) (Credential, error) {
	secret = strings.TrimSpace(secret)
	if !validCredential(secret) {
		return Credential{}, ErrInvalidCredential
	}
	if !validAuthMode(options.AuthMode) {
		return Credential{}, ErrInvalidCredential
	}
	accountID := strings.TrimSpace(options.AccountID)
	quotaProjectID := strings.TrimSpace(options.QuotaProjectID)
	if !validOptionalHeaderValue(accountID) || !validOptionalHeaderValue(quotaProjectID) {
		return Credential{}, ErrInvalidCredential
	}
	return Credential{
		secret:         secret,
		authMode:       options.AuthMode,
		accountID:      accountID,
		quotaProjectID: quotaProjectID,
	}, nil
}

func (Credential) String() string     { return "upstream credential [redacted]" }
func (c Credential) GoString() string { return c.String() }

type Builder struct {
	MaxBodyBytes int64
}

type Input struct {
	Context    context.Context
	Request    gateway.RequestShape
	Candidate  port.GatewayAccountCandidate
	BaseURL    string
	Credential Credential
	Headers    http.Header
	Body       []byte
}

// Build returns a request whose body is a private bounded copy. The supplied
// context is attached directly, so downstream cancellation and deadlines reach
// the shared http.Client without a builder-owned goroutine.
func (b Builder) Build(input Input) (*http.Request, gateway.Definition, error) {
	if input.Context == nil {
		return nil, gateway.Definition{}, ErrContextRequired
	}
	if !validCredential(input.Credential.secret) || !validAuthMode(input.Credential.authMode) {
		return nil, gateway.Definition{}, ErrInvalidCredential
	}

	profile := effectiveCandidateProfile(input.Candidate)
	definition, ok := gateway.DefinitionForProfile(profile)
	if !ok {
		return nil, gateway.Definition{}, ErrUnsupportedProtocol
	}
	endpoint, err := buildEndpoint(input.BaseURL, input.Request, definition.Code, input.Candidate)
	if err != nil {
		return nil, gateway.Definition{}, err
	}

	method := strings.ToUpper(strings.TrimSpace(input.Request.Method))
	if method == "" {
		return nil, gateway.Definition{}, fmt.Errorf("%w：method 为空", ErrInvalidRequestTarget)
	}
	maxBodyBytes := b.MaxBodyBytes
	if maxBodyBytes <= 0 {
		maxBodyBytes = DefaultMaxBodyBytes
	}
	if int64(len(input.Body)) > maxBodyBytes {
		return nil, gateway.Definition{}, fmt.Errorf("%w：收到 %d bytes，上限 %d bytes", ErrBodyTooLarge, len(input.Body), maxBodyBytes)
	}

	var ownedBody []byte
	if method != http.MethodGet && method != http.MethodHead && len(input.Body) > 0 {
		ownedBody = bytes.Clone(input.Body)
	}
	var bodyReader *bytes.Reader
	if ownedBody != nil {
		bodyReader = bytes.NewReader(ownedBody)
	} else {
		bodyReader = bytes.NewReader(nil)
	}
	request, err := http.NewRequestWithContext(input.Context, method, endpoint.String(), bodyReader)
	if err != nil {
		return nil, gateway.Definition{}, fmt.Errorf("%w: %v", ErrInvalidRequestTarget, err)
	}

	headers, err := copySafeHeaders(input.Headers)
	if err != nil {
		return nil, gateway.Definition{}, err
	}
	request.Header = headers
	request.Host = endpoint.Host
	if ownedBody == nil {
		request.Body = http.NoBody
		request.GetBody = func() (io.ReadCloser, error) { return http.NoBody, nil }
		request.ContentLength = 0
	}
	if len(ownedBody) > 0 && request.Header.Get("Content-Type") == "" {
		request.Header.Set("Content-Type", "application/json")
	}
	applyProtocolHeaders(request.Header, definition.Code, input.Request, input.Candidate, input.Credential)
	return request, definition, nil
}

func buildEndpoint(rawBaseURL string, request gateway.RequestShape, protocol gateway.ProtocolCode, candidate port.GatewayAccountCandidate) (*url.URL, error) {
	base, err := parseBaseURL(rawBaseURL)
	if err != nil {
		return nil, err
	}
	target, err := parseRequestTarget(request.Path)
	if err != nil {
		return nil, err
	}

	version := "v1"
	if protocol == gateway.ProtocolGemini {
		version = "v1beta"
	}
	baseEscapedPath := base.EscapedPath()
	if protocol != gateway.ProtocolOpenAI || effectiveCandidateType(candidate) != "oauth" {
		baseEscapedPath = ensureVersionPath(baseEscapedPath, version)
	}
	targetEscapedPath := stripVersionPath(target.EscapedPath(), version)
	joinedEscapedPath := strings.TrimRight(baseEscapedPath, "/")
	if targetEscapedPath != "/" {
		joinedEscapedPath += targetEscapedPath
	}
	if joinedEscapedPath == "" {
		joinedEscapedPath = "/"
	}
	decodedPath, err := url.PathUnescape(joinedEscapedPath)
	if err != nil {
		return nil, fmt.Errorf("%w：path 转义无效", ErrInvalidRequestTarget)
	}
	base.Path = decodedPath
	base.RawPath = joinedEscapedPath

	query := base.Query()
	for key, values := range target.Query() {
		query[key] = append([]string(nil), values...)
	}
	if protocol == gateway.ProtocolGemini {
		deleteQueryFold(query, "key")
		family := gateway.EndpointFamilyFromPath(protocol, request.Path)
		if family == gateway.EndpointInteractions {
			deleteQueryFold(query, "alt")
		} else if family == gateway.EndpointStreamGenerateContent || request.Stream {
			if query.Get("alt") == "" {
				query.Set("alt", "sse")
			}
		}
	}
	base.RawQuery = query.Encode()
	return base, nil
}

func parseBaseURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Opaque != "" || parsed.User != nil || parsed.Fragment != "" {
		return nil, ErrInvalidBaseURL
	}
	if !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return nil, ErrInvalidBaseURL
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	return parsed, nil
}

func parseRequestTarget(raw string) (*url.URL, error) {
	parsed, err := url.ParseRequestURI(strings.TrimSpace(raw))
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.User != nil || parsed.Fragment != "" {
		return nil, ErrInvalidRequestTarget
	}
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	return parsed, nil
}

func ensureVersionPath(escapedPath, version string) string {
	path := strings.TrimRight(escapedPath, "/")
	if path == "" {
		return "/" + version
	}
	if strings.HasSuffix(strings.ToLower(path), "/"+version) {
		return path
	}
	return path + "/" + version
}

func stripVersionPath(escapedPath, version string) string {
	path := escapedPath
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	prefix := "/" + version
	if len(path) >= len(prefix) && strings.EqualFold(path[:len(prefix)], prefix) && (len(path) == len(prefix) || path[len(prefix)] == '/') {
		path = path[len(prefix):]
	}
	if path == "" {
		return "/"
	}
	if !strings.HasPrefix(path, "/") {
		return "/" + path
	}
	return path
}

func deleteQueryFold(query url.Values, name string) {
	for key := range query {
		if strings.EqualFold(key, name) {
			delete(query, key)
		}
	}
}

func copySafeHeaders(input http.Header) (http.Header, error) {
	dynamicSkipped := make(map[string]struct{})
	for name, values := range input {
		if !strings.EqualFold(name, "Connection") {
			continue
		}
		for _, value := range values {
			for _, token := range strings.Split(value, ",") {
				token = strings.ToLower(strings.TrimSpace(token))
				if token != "" {
					dynamicSkipped[token] = struct{}{}
				}
			}
		}
	}

	output := make(http.Header)
	for name, values := range input {
		lowerName := strings.ToLower(strings.TrimSpace(name))
		if shouldSkipHeader(lowerName, dynamicSkipped) {
			continue
		}
		if !httpguts.ValidHeaderFieldName(name) {
			return nil, fmt.Errorf("%w：名称无效", ErrInvalidHeader)
		}
		for _, value := range values {
			if !httpguts.ValidHeaderFieldValue(value) {
				return nil, fmt.Errorf("%w：%s", ErrInvalidHeader, name)
			}
			output.Add(name, value)
		}
	}
	return output, nil
}

func shouldSkipHeader(name string, dynamicSkipped map[string]struct{}) bool {
	if _, ok := dynamicSkipped[name]; ok {
		return true
	}
	if _, ok := skippedRequestHeaders[name]; ok {
		return true
	}
	for _, prefix := range skippedRequestHeaderPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func applyProtocolHeaders(headers http.Header, protocol gateway.ProtocolCode, request gateway.RequestShape, candidate port.GatewayAccountCandidate, credential Credential) {
	mode := credential.authMode
	if mode == AuthAuto {
		mode = defaultAuthMode(protocol, candidate)
	}
	switch mode {
	case AuthBearer:
		headers.Set("Authorization", "Bearer "+credential.secret)
	case AuthAnthropicAPIKey:
		headers.Set("X-Api-Key", credential.secret)
	case AuthGeminiAPIKey:
		headers.Set("X-Goog-Api-Key", credential.secret)
	}

	if protocol == gateway.ProtocolAnthropic && headers.Get("Anthropic-Version") == "" {
		headers.Set("Anthropic-Version", defaultAnthropicVersion)
	}
	if protocol == gateway.ProtocolGemini && effectiveCandidateType(candidate) == "google_oauth" && credential.quotaProjectID != "" {
		headers.Set("X-Goog-User-Project", credential.quotaProjectID)
	}
	if protocol == gateway.ProtocolOpenAI && credential.accountID != "" {
		headers.Set("Chatgpt-Account-Id", credential.accountID)
	}
	if headers.Get("Accept") == "" {
		if request.Stream {
			headers.Set("Accept", "text/event-stream")
		} else {
			headers.Set("Accept", "application/json")
		}
	}
}

func defaultAuthMode(protocol gateway.ProtocolCode, candidate port.GatewayAccountCandidate) AuthMode {
	switch protocol {
	case gateway.ProtocolAnthropic:
		if strings.EqualFold(effectiveCandidateProvider(candidate), "glm") &&
			strings.EqualFold(effectiveCandidateProfileID(candidate), "profile_glm_coding_anthropic_v1") {
			return AuthBearer
		}
		return AuthAnthropicAPIKey
	case gateway.ProtocolGemini:
		if effectiveCandidateType(candidate) == "google_oauth" {
			return AuthBearer
		}
		return AuthGeminiAPIKey
	default:
		return AuthBearer
	}
}

func effectiveCandidateProfile(candidate port.GatewayAccountCandidate) gateway.Profile {
	if candidate.ResourceAccountID != "" {
		return gateway.Profile{Code: candidate.ResourceProtocolCode, Version: candidate.ResourceProtocolVersion}
	}
	return gateway.Profile{Code: candidate.ProtocolCode, Version: candidate.ProtocolVersion}
}

func effectiveCandidateType(candidate port.GatewayAccountCandidate) string {
	if candidate.ResourceAccountID != "" {
		return strings.ToLower(strings.TrimSpace(candidate.ResourceType))
	}
	return strings.ToLower(strings.TrimSpace(candidate.Type))
}

func effectiveCandidateProvider(candidate port.GatewayAccountCandidate) string {
	if candidate.ResourceAccountID != "" {
		return strings.ToLower(strings.TrimSpace(candidate.ResourceProviderCode))
	}
	return strings.ToLower(strings.TrimSpace(candidate.ProviderCode))
}

func effectiveCandidateProfileID(candidate port.GatewayAccountCandidate) string {
	if candidate.ResourceAccountID != "" {
		return strings.TrimSpace(candidate.ResourceProviderProtocolProfileID)
	}
	return strings.TrimSpace(candidate.ProviderProtocolProfileID)
}

func validCredential(value string) bool {
	if value == "" || len(value) > maxCredentialBytes || !utf8.ValidString(value) {
		return false
	}
	return strings.IndexFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0
}

func validOptionalHeaderValue(value string) bool {
	return value == "" || (len(value) <= maxCredentialBytes && httpguts.ValidHeaderFieldValue(value))
}

func validAuthMode(mode AuthMode) bool {
	switch mode {
	case AuthAuto, AuthBearer, AuthAnthropicAPIKey, AuthGeminiAPIKey:
		return true
	default:
		return false
	}
}

var skippedRequestHeaders = map[string]struct{}{
	"host": {}, "authorization": {}, "content-length": {}, "connection": {}, "keep-alive": {},
	"proxy-authenticate": {}, "proxy-authorization": {}, "proxy-connection": {}, "te": {}, "trailer": {},
	"transfer-encoding": {}, "upgrade": {}, "expect": {}, "content-encoding": {}, "accept-encoding": {},
	"cookie": {}, "set-cookie": {}, "openai-api-key": {}, "x-openai-api-key": {}, "x-api-key": {},
	"anthropic-api-key": {}, "x-anthropic-api-key": {}, "x-goog-api-key": {}, "x-google-api-key": {},
	"api-key": {}, "chatgpt-account-id": {}, "x-oai-attestation": {}, "openai-organization": {},
	"openai-project": {}, "x-juhe-client-profile": {}, "x-request-id": {}, "traceparent": {}, "tracestate": {},
	"baggage": {}, "x-amzn-trace-id": {}, "x-cloud-trace-context": {}, "x-forwarded-for": {},
	"x-forwarded-host": {}, "x-forwarded-port": {}, "x-forwarded-proto": {}, "x-forwarded-server": {},
	"x-real-ip": {}, "forwarded": {}, "via": {}, "cf-connecting-ip": {},
}

var skippedRequestHeaderPrefixes = [...]string{"x-forwarded-", "x-openai-", "x-stainless-", "x-vercel-"}

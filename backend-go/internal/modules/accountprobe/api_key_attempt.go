package accountprobe

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
)

var (
	ErrUnsupportedCredential = errors.New("account probe credential type or protocol is unsupported")
	ErrCredentialUnavailable = errors.New("account probe credential is unavailable")
	ErrInvalidBaseURL        = errors.New("account probe base URL is invalid")
)

type APIKeyAttempt struct {
	method         string
	url            string
	header         http.Header
	body           []byte
	keyFingerprint string
	keyIndex       int
}

func (a APIKeyAttempt) Method() string         { return a.method }
func (a APIKeyAttempt) URL() string            { return a.url }
func (a APIKeyAttempt) Header() http.Header    { return a.header.Clone() }
func (a APIKeyAttempt) Body() []byte           { return append([]byte(nil), a.body...) }
func (a APIKeyAttempt) KeyFingerprint() string { return a.keyFingerprint }
func (a APIKeyAttempt) KeyIndex() int          { return a.keyIndex }
func (APIKeyAttempt) String() string           { return "[REDACTED]" }
func (APIKeyAttempt) GoString() string         { return "[REDACTED]" }

func PrepareAPIKeyAttempt(candidate gatewaycandidatewindow.Candidate, prepared PreparedRequest, now time.Time) (APIKeyAttempt, error) {
	identity := gatewaycandidatewindow.EffectiveAccountIdentity(candidate)
	if !strings.EqualFold(identity.Type, "api_key") {
		return APIKeyAttempt{}, fmt.Errorf("%w: account type %q", ErrUnsupportedCredential, identity.Type)
	}
	selected, ok, err := gatewaycandidatewindow.SelectProbeAPIKey(candidate, now)
	if err != nil {
		return APIKeyAttempt{}, fmt.Errorf("%w: %v", ErrCredentialUnavailable, err)
	}
	if !ok {
		return APIKeyAttempt{}, ErrCredentialUnavailable
	}
	baseURL, ok := candidate.Credentials.StringValue("base_url")
	if !ok {
		baseURL = strings.TrimSpace(candidate.DefaultBaseURL)
	}
	request := prepared.Request
	header := request.Header.Clone()
	header.Del("X-Juhe-Client-Profile")
	var attemptURL string
	switch strings.ToLower(identity.ProtocolCode) {
	case "openai":
		header.Set("Authorization", "Bearer "+selected.Secret())
		attemptURL, err = buildVersionedURL(baseURL, request.PathAndQuery, "v1")
	case "anthropic":
		if identity.ProviderCode == "glm" && identity.ProviderProtocolProfileID == "profile_glm_coding_anthropic_v1" {
			header.Set("Authorization", "Bearer "+selected.Secret())
		} else {
			header.Set("X-Api-Key", selected.Secret())
		}
		header.Set("Anthropic-Version", "2023-06-01")
		applyClaudeCodeAPIKeyHeaders(header)
		path := request.PathAndQuery
		if request.Mode == ModeMessagesJSON || request.Mode == ModeMessagesSSE {
			path = withQueryValue(path, "beta", "true")
		}
		attemptURL, err = buildVersionedURL(baseURL, path, "v1")
	case "gemini":
		header.Set("X-Goog-Api-Key", selected.Secret())
		attemptURL, err = buildGeminiURL(baseURL, request.PathAndQuery)
	default:
		return APIKeyAttempt{}, fmt.Errorf("%w: protocol %q", ErrUnsupportedCredential, identity.ProtocolCode)
	}
	if err != nil {
		return APIKeyAttempt{}, err
	}
	return APIKeyAttempt{
		method: request.Method, url: attemptURL, header: header, body: append([]byte(nil), request.Body...),
		keyFingerprint: selected.Fingerprint(), keyIndex: selected.Index(),
	}, nil
}

func applyClaudeCodeAPIKeyHeaders(header http.Header) {
	header.Set("User-Agent", "claude-cli/2.1.201 (external, sdk-cli)")
	header.Set("Anthropic-Beta", strings.Join([]string{
		"claude-code-20250219", "interleaved-thinking-2025-05-14", "effort-2025-11-24",
	}, ","))
}

func buildVersionedURL(rawBase, pathAndQuery, version string) (string, error) {
	base, err := parseProbeBaseURL(rawBase)
	if err != nil {
		return "", err
	}
	basePath := strings.TrimRight(base.Path, "/")
	versionPath := "/" + version
	if !strings.HasSuffix(strings.ToLower(basePath), versionPath) {
		basePath += versionPath
	}
	path, rawQuery, _ := strings.Cut(pathAndQuery, "?")
	path = "/" + strings.TrimLeft(path, "/")
	if len(path) >= len(versionPath) && strings.EqualFold(path[:len(versionPath)], versionPath) && (len(path) == len(versionPath) || path[len(versionPath)] == '/') {
		path = path[len(versionPath):]
	}
	if path == "/" {
		path = ""
	}
	base.Path = ""
	base.RawPath = ""
	base.RawQuery = rawQuery
	base.Path = basePath + path
	return base.String(), nil
}

func buildGeminiURL(rawBase, pathAndQuery string) (string, error) {
	base, err := parseProbeBaseURL(rawBase)
	if err != nil {
		return "", err
	}
	path, rawQuery, _ := strings.Cut(pathAndQuery, "?")
	path = "/" + strings.TrimLeft(path, "/")
	const versionPath = "/v1beta"
	if len(path) >= len(versionPath) && strings.EqualFold(path[:len(versionPath)], versionPath) && (len(path) == len(versionPath) || path[len(versionPath)] == '/') {
		path = path[len(versionPath):]
	}
	basePath := strings.TrimRight(base.Path, "/")
	if !strings.HasSuffix(strings.ToLower(basePath), versionPath) {
		basePath += versionPath
	}
	query := base.Query()
	incoming, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", fmt.Errorf("%w: request query", ErrInvalidBaseURL)
	}
	incoming.Del("key")
	for key, values := range incoming {
		query.Del(key)
		for _, value := range values {
			query.Add(key, value)
		}
	}
	base.Path = basePath + path
	base.RawPath = ""
	base.RawQuery = query.Encode()
	return base.String(), nil
}

func parseProbeBaseURL(raw string) (*url.URL, error) {
	base, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || base == nil || base.Opaque != "" || base.Hostname() == "" || base.User != nil || base.Fragment != "" || (base.Scheme != "http" && base.Scheme != "https") {
		return nil, ErrInvalidBaseURL
	}
	if base.RawQuery != "" {
		return nil, fmt.Errorf("%w: base URL query is not allowed", ErrInvalidBaseURL)
	}
	return base, nil
}

func withQueryValue(pathAndQuery, key, value string) string {
	path, rawQuery, _ := strings.Cut(pathAndQuery, "?")
	query, _ := url.ParseQuery(rawQuery)
	if query.Get(key) == "" {
		query.Set(key, value)
	}
	if encoded := query.Encode(); encoded != "" {
		return path + "?" + encoded
	}
	return path
}

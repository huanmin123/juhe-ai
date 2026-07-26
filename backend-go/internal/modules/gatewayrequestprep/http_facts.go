package gatewayrequestprep

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	protocolgateway "juhe-ai/backend-go/internal/protocols/gateway"
)

var ErrHTTPRequestRequired = errors.New("gateway preparation HTTP request is required")

// HTTPFacts contains the body-derived and caller-owned facts that this adapter
// must not derive itself. In particular, StreamRequested and
// CodexTurnMetadataValid must come from one bounded parser owned by the future
// listener; this adapter never reads r.Body.
type HTTPFacts struct {
	FallbackProtocol       Protocol
	StreamRequested        bool
	CodexTurnMetadataValid bool
}

// PrepareHTTPRequest reads only method, URL, and request metadata needed by
// Prepare. It neither authenticates nor writes a response, and it leaves the
// request, headers, URL, and body untouched.
func PrepareHTTPRequest(request *http.Request, facts HTTPFacts) (Result, error) {
	if request == nil {
		return Result{}, ErrHTTPRequestRequired
	}
	path := "/"
	query := url.Values{}
	if request.URL != nil {
		path = request.URL.EscapedPath()
		if path == "" {
			path = "/"
		}
		query = request.URL.Query()
	}
	return Prepare(Input{
		Method:                 request.Method,
		Path:                   path,
		FallbackProtocol:       facts.FallbackProtocol,
		StreamRequested:        facts.StreamRequested,
		AcceptsEventStream:     protocolgateway.AcceptsEventStream(strings.Join(request.Header.Values("Accept"), ",")),
		GeminiAltSSE:           strings.EqualFold(query.Get("alt"), "sse"),
		ExplicitClientProfile:  request.Header.Get("X-Juhe-Client-Profile"),
		UserAgent:              request.Header.Get("User-Agent"),
		HasAnthropicBeta:       headerHasNonEmptyValue(request.Header, "Anthropic-Beta"),
		HasAnthropicBetaQuery:  query.Get("beta") == "true",
		HasClaudeSessionID:     headerHasNonEmptyValue(request.Header, "X-Claude-Code-Session-Id"),
		HasClaudeAgentID:       headerHasNonEmptyValue(request.Header, "X-Claude-Code-Agent-Id"),
		HasGeminiCredential:    hasGeminiCredentialPresence(request, query),
		CodexTurnMetadataValid: facts.CodexTurnMetadataValid,
	}), nil
}

func headerHasNonEmptyValue(header http.Header, name string) bool {
	for _, value := range header.Values(name) {
		if strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func hasGeminiCredentialPresence(request *http.Request, query url.Values) bool {
	return headerHasNonEmptyValue(request.Header, "X-Goog-API-Key") ||
		headerHasNonEmptyValue(request.Header, "X-API-Key") ||
		headerHasNonEmptyValue(request.Header, "Authorization") ||
		strings.TrimSpace(query.Get("key")) != ""
}

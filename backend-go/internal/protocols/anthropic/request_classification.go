package anthropic

import (
	"mime"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Endpoint identifies an Anthropic v1 resource independently of its HTTP method.
type Endpoint string

const (
	EndpointUnknown              Endpoint = "unknown"
	EndpointMessages             Endpoint = "messages"
	EndpointMessageTokenCounting Endpoint = "message_token_counting"
	EndpointModels               Endpoint = "models"
)

// EndpointMode is the account capability required to dispatch a request.
// Models is a discovery endpoint and therefore has no account capability mode.
type EndpointMode string

const (
	EndpointModeNone                 EndpointMode = ""
	EndpointModeMessagesJSON         EndpointMode = "messages_json"
	EndpointModeMessagesSSE          EndpointMode = "messages_sse"
	EndpointModeMessageTokenCounting EndpointMode = "message_token_counting"
)

type PathClassification struct {
	Valid          bool
	NormalizedPath string
	Endpoint       Endpoint
	BetaQuery      bool
}

type RequestInput struct {
	Method string
	Target string
	Stream bool
	Accept string
}

type RequestClassification struct {
	PathClassification
	Method    string
	Mode      EndpointMode
	Stream    bool
	Supported bool
}

// ClassifyPath parses a request target without decoding escaped path separators.
// Anthropic accepts both root and /v1-prefixed gateway paths.
func ClassifyPath(rawTarget string) PathClassification {
	target := strings.TrimSpace(rawTarget)
	if target == "" {
		target = "/"
	} else if !strings.HasPrefix(target, "/") {
		target = "/" + target
	}

	parsed, err := url.ParseRequestURI(target)
	if err != nil {
		return PathClassification{Endpoint: EndpointUnknown}
	}

	path := parsed.EscapedPath()
	if path == "" {
		path = "/"
	}
	path = strings.TrimPrefix(path, "/v1")
	if path == "" {
		path = "/"
	} else if !strings.HasPrefix(path, "/") {
		// /v10 and similar paths are not /v1-prefixed paths.
		path = parsed.EscapedPath()
	}

	endpoint := EndpointUnknown
	switch path {
	case "/messages":
		endpoint = EndpointMessages
	case "/messages/count_tokens":
		endpoint = EndpointMessageTokenCounting
	case "/models":
		endpoint = EndpointModels
	}

	return PathClassification{
		Valid:          true,
		NormalizedPath: path,
		Endpoint:       endpoint,
		BetaQuery:      firstQueryValueEquals(parsed.RawQuery, "beta", "true"),
	}
}

func ClassifyRequest(input RequestInput) RequestClassification {
	path := ClassifyPath(input.Target)
	method := strings.ToUpper(strings.TrimSpace(input.Method))
	stream := input.Stream || AcceptsEventStream(input.Accept)
	result := RequestClassification{
		PathClassification: path,
		Method:             method,
		Mode:               EndpointModeNone,
		Stream:             stream,
	}
	if !path.Valid {
		return result
	}

	switch {
	case method == "POST" && path.Endpoint == EndpointMessages:
		result.Supported = true
		if stream {
			result.Mode = EndpointModeMessagesSSE
		} else {
			result.Mode = EndpointModeMessagesJSON
		}
	case method == "POST" && path.Endpoint == EndpointMessageTokenCounting:
		result.Supported = true
		result.Mode = EndpointModeMessageTokenCounting
	case method == "GET" && path.Endpoint == EndpointModels:
		result.Supported = true
	}
	return result
}

// AcceptsEventStream applies the Accept quality value instead of treating a
// rejected text/event-stream media range (q=0) as a streaming request.
func AcceptsEventStream(accept string) bool {
	for _, item := range splitHeaderList(accept) {
		mediaType, params, err := mime.ParseMediaType(strings.TrimSpace(item))
		if err != nil || !strings.EqualFold(mediaType, "text/event-stream") {
			continue
		}
		if quality, ok := params["q"]; ok {
			value, err := strconv.ParseFloat(quality, 64)
			if err != nil || !(value > 0 && value <= 1) {
				continue
			}
		}
		return true
	}
	return false
}

func firstQueryValueEquals(rawQuery string, name string, want string) bool {
	for _, field := range strings.Split(rawQuery, "&") {
		rawName, rawValue, _ := strings.Cut(field, "=")
		decodedName, err := url.QueryUnescape(rawName)
		if err != nil || decodedName != name {
			continue
		}
		decodedValue, err := url.QueryUnescape(rawValue)
		return err == nil && decodedValue == want
	}
	return false
}

func splitHeaderList(value string) []string {
	items := make([]string, 0, 4)
	start := 0
	quoted := false
	escaped := false
	for index := 0; index < len(value); index++ {
		switch current := value[index]; {
		case escaped:
			escaped = false
		case quoted && current == '\\':
			escaped = true
		case current == '"':
			quoted = !quoted
		case current == ',' && !quoted:
			items = append(items, value[start:index])
			start = index + 1
		}
	}
	return append(items, value[start:])
}

type ClientProfile string

const (
	ClientProfileGenericAnthropic ClientProfile = "generic_anthropic"
	ClientProfileClaudeCode       ClientProfile = "claude_code"
)

type ClientProfileSource string

const (
	ClientProfileSourceDefault             ClientProfileSource = "default"
	ClientProfileSourceExplicitHeader      ClientProfileSource = "explicit_header"
	ClientProfileSourceClaudeCodeSignature ClientProfileSource = "claude_code_request_signature"
)

type ClientProfileInput struct {
	Request             RequestClassification
	ExplicitProfile     string
	UserAgent           string
	AnthropicBeta       string
	ClaudeCodeSessionID string
	ClaudeCodeAgentID   string
}

type ClientProfileClassification struct {
	Profile              ClientProfile
	Source               ClientProfileSource
	SignatureSignalCount int
}

var profileSeparatorPattern = regexp.MustCompile(`[-\s]+`)

func ClassifyClientProfile(input ClientProfileInput) ClientProfileClassification {
	result := ClientProfileClassification{
		Profile: ClientProfileGenericAnthropic,
		Source:  ClientProfileSourceDefault,
	}

	// A client hint must not turn an unrelated or unsupported endpoint into a
	// Claude Code request. Profile classification is valid only for Messages.
	if !input.Request.Supported || input.Request.Endpoint != EndpointMessages {
		return result
	}

	explicit := profileSeparatorPattern.ReplaceAllString(strings.ToLower(strings.TrimSpace(input.ExplicitProfile)), "_")
	if explicit == string(ClientProfileClaudeCode) {
		result.Profile = ClientProfileClaudeCode
		result.Source = ClientProfileSourceExplicitHeader
		return result
	}

	result.SignatureSignalCount = claudeCodeSignatureSignalCount(input)
	if result.SignatureSignalCount >= 2 {
		result.Profile = ClientProfileClaudeCode
		result.Source = ClientProfileSourceClaudeCodeSignature
	}
	return result
}

func claudeCodeSignatureSignalCount(input ClientProfileInput) int {
	count := 0
	userAgent := strings.ToLower(strings.TrimSpace(input.UserAgent))
	if strings.HasPrefix(userAgent, "claude-cli/") || strings.Contains(userAgent, " claude-cli/") {
		count++
	}

	for _, item := range strings.Split(strings.ToLower(input.AnthropicBeta), ",") {
		if strings.HasPrefix(strings.TrimSpace(item), "claude-code-") {
			count++
			break
		}
	}

	if strings.TrimSpace(input.ClaudeCodeSessionID) != "" || strings.TrimSpace(input.ClaudeCodeAgentID) != "" {
		count++
	}
	if input.Request.BetaQuery {
		count++
	}
	return count
}

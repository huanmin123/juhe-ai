package gatewayrouting

import (
	"net/url"
	"regexp"
	"strings"
)

// RequestView is the minimal request projection the routing layer consumes.
// It replaces direct express req reads: Method/OriginalURL/Path drive the
// endpoint-family and Gemini path-model extraction (request/metadata.ts,
// protocols/openai-v1/model-mapping.ts gatewayRequestEndpointFamily),
// BodyModel carries the parsed request body model (gateway request body
// state model ?? req.body.model), and EndpointFamilyOverride mirrors
// setGatewayModelMappingSourceEndpointFamilyOverride.
type RequestView struct {
	Method                  string
	OriginalURL             string
	Path                    string
	BodyModel               string
	EndpointFamilyOverride  string
}

// requestModel mirrors requestModel(req): the Gemini path model wins over
// the parsed body model. The caller trims where the Node call sites do.
func (v RequestView) requestModel() string {
	if model := requestModelFromGeminiPath(v.OriginalURL, v.Path); model != "" {
		return model
	}
	return v.BodyModel
}

// requestEndpointPath mirrors `(req.originalUrl || req.path || '').split('?', 1)[0]`.
func (v RequestView) requestEndpointPath() string {
	source := v.OriginalURL
	if source == "" {
		source = v.Path
	}
	if index := strings.Index(source, "?"); index >= 0 {
		return source[:index]
	}
	return source
}

// geminiModelPathPattern mirrors requestModelFromGeminiPath's regex
// (case-insensitive, anchored at the query-free path end).
var geminiModelPathPattern = regexp.MustCompile(`(?i)/models/([^/:?#]+):(?:generateContent|streamGenerateContent|countTokens|embedContent)$`)

// requestModelFromGeminiPath mirrors requestModelFromGeminiPath.
func requestModelFromGeminiPath(originalURL, path string) string {
	source := originalURL
	if index := strings.Index(source, "?"); index >= 0 {
		source = source[:index]
	}
	if source == "" {
		source = path
	}
	match := geminiModelPathPattern.FindStringSubmatch(source)
	if len(match) < 2 || match[1] == "" {
		return ""
	}
	if decoded, err := url.PathUnescape(match[1]); err == nil {
		return decoded
	}
	return match[1]
}

// requestEndpointFamily mirrors gatewayRequestEndpointFamily: the explicit
// override wins, then the OpenAI path families, then the Anthropic messages
// family, then the Gemini families.
func (v RequestView) requestEndpointFamily() string {
	if v.EndpointFamilyOverride != "" {
		return v.EndpointFamilyOverride
	}
	if family := openAIRequestEndpointFamily(v.requestEndpointPath()); family != "" {
		return family
	}
	if family := anthropicMessagesRequestEndpointFamily(v.Method, v.requestEndpointPath()); family != "" {
		return family
	}
	return geminiRequestEndpointFamily(v.Method, v.requestEndpointPath())
}

// openAIRequestEndpointFamily mirrors openAIEndpointFamilyFromPath +
// openAIRequestEndpointFamily.
func openAIRequestEndpointFamily(endpoint string) string {
	path := strings.ToLower(trimSpace(endpoint))
	if path == "" {
		return ""
	}
	if strings.Contains(path, "/chat/completions") {
		return EndpointFamilyChatCompletions
	}
	if strings.Contains(path, "/responses") {
		return EndpointFamilyResponses
	}
	return ""
}

// anthropicMessagesRequestEndpointFamily mirrors
// anthropicMessagesRequestEndpointFamily.
func anthropicMessagesRequestEndpointFamily(method, endpoint string) string {
	if strings.ToUpper(method) != "POST" {
		return ""
	}
	normalizedPath := endpoint
	if !strings.HasPrefix(normalizedPath, "/") {
		normalizedPath = "/" + normalizedPath
	}
	normalizedPath = replaceV1Prefix(normalizedPath)
	if normalizedPath == "" {
		normalizedPath = "/"
	}
	if normalizedPath == "/messages" {
		return EndpointFamilyMessages
	}
	return ""
}

// geminiRequestEndpointFamily mirrors geminiRequestEndpointFamily: the
// plain '/models' family is rejected, every other Gemini family passes.
func geminiRequestEndpointFamily(method, endpoint string) string {
	if strings.ToUpper(method) != "POST" {
		return ""
	}
	family := geminiEndpointFamilyFromPath(endpoint)
	if family == EndpointFamilyGeminiModelsPath {
		return ""
	}
	return family
}

// replaceV1Prefix mirrors .replace(/^\/v1(?=\/|$)/, '').
func replaceV1Prefix(path string) string {
	if strings.HasPrefix(path, "/v1") {
		rest := path[3:]
		if rest == "" || rest[0] == '/' {
			return rest
		}
	}
	return path
}

var (
	geminiInteractionsPattern  = regexp.MustCompile(`^/interactions(?:/[^/]+(?:/cancel)?)?$`)
	geminiModelActionPattern   = regexp.MustCompile(`^/models/[^/]+:(generatecontent|streamgeneratecontent|counttokens|embedcontent)$`)
)

// geminiEndpointFamilyFromPath mirrors geminiEndpointFamilyFromPath
// (domain/gemini-endpoint-modes.ts).
func geminiEndpointFamilyFromPath(value string) string {
	path := strings.ToLower(trimSpace(value))
	if path == "" {
		return ""
	}
	normalizedPath := normalizedGeminiPath(path)
	if normalizedPath == "/models" {
		return EndpointFamilyGeminiModelsPath
	}
	if geminiInteractionsPattern.MatchString(normalizedPath) {
		return EndpointFamilyInteractions
	}
	match := geminiModelActionPattern.FindStringSubmatch(normalizedPath)
	if match == nil {
		return ""
	}
	switch match[1] {
	case "generatecontent":
		return EndpointFamilyGenerateContent
	case "streamgeneratecontent":
		return EndpointFamilyStreamGenerate
	case "counttokens":
		return EndpointFamilyCountTokens
	case "embedcontent":
		return EndpointFamilyEmbedContent
	}
	return ""
}

// normalizedGeminiPath mirrors normalizedGeminiPath.
func normalizedGeminiPath(pathAndQuery string) string {
	path := pathAndQuery
	if index := strings.Index(pathAndQuery, "?"); index >= 0 {
		path = pathAndQuery[:index]
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	normalized := replaceV1BetaPrefix(path)
	if normalized == "" {
		return "/"
	}
	return normalized
}

// replaceV1BetaPrefix mirrors .replace(/^\/v1beta(?=\/|$)/, '').
func replaceV1BetaPrefix(path string) string {
	if strings.HasPrefix(path, "/v1beta") {
		rest := path[len("/v1beta"):]
		if rest == "" || rest[0] == '/' {
			return rest
		}
	}
	return path
}

// trimSpace is the shared strings.TrimSpace alias for call sites mirroring
// JS String#trim.
func trimSpace(value string) string {
	return strings.TrimSpace(value)
}

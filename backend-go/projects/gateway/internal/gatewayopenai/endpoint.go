package gatewayopenai

import (
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// endpointFamilyFromPath mirrors openAIEndpointFamilyFromPath
// (domain/openai-endpoint-modes.ts): lowercase containment match.
func endpointFamilyFromPath(value string) (string, bool) {
	path := strings.ToLower(strings.TrimSpace(value))
	if path == "" {
		return "", false
	}
	if strings.Contains(path, "/chat/completions") {
		return FamilyChatCompletions, true
	}
	if strings.Contains(path, "/responses") {
		return FamilyResponses, true
	}
	return "", false
}

// responseEndpointFamilyFromPath mirrors openAIResponseEndpointFamilyFromRequest.
func responseEndpointFamilyFromPath(path string) gatewayproto.ResponseEndpointFamily {
	if strings.Contains(path, "/chat/completions") {
		return gatewayproto.EndpointFamilyChatCompletions
	}
	if strings.Contains(path, "/responses") {
		return gatewayproto.EndpointFamilyResponses
	}
	return gatewayproto.EndpointFamilyUnknown
}

// normalizedPathWithoutVersion strips a leading "/v1" path segment
// (mirrors the Node ^\/v1(?=\/|$) replacement).
func normalizedPathWithoutVersion(path string) string {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if path == "/v1" {
		return "/"
	}
	if strings.HasPrefix(path, "/v1/") {
		return path[len("/v1"):]
	}
	return path
}

// SplitPathAndQuery mirrors splitPathAndQuery.
func SplitPathAndQuery(pathAndQuery string) (path, query string) {
	index := strings.Index(pathAndQuery, "?")
	if index < 0 {
		return pathAndQuery, ""
	}
	return pathAndQuery[:index], pathAndQuery[index:]
}

// IsProtocolRequestPath mirrors isOpenAIProtocolRequestPath: the request
// path belongs to the OpenAI protocol surface.
func IsProtocolRequestPath(originalPathAndQuery string) bool {
	if _, ok := endpointFamilyFromPath(originalPathAndQuery); ok {
		return true
	}
	path, _ := SplitPathAndQuery(originalPathAndQuery)
	path = strings.ToLower(strings.TrimSpace(path))
	normalized := normalizedPathWithoutVersion(path)
	switch {
	case normalized == "/models":
		return true
	case normalized == "/images", strings.HasPrefix(normalized, "/images/"):
		return true
	case normalized == "/embeddings":
		return true
	case normalized == "/audio", strings.HasPrefix(normalized, "/audio/"):
		return true
	}
	return false
}

// IsModelsRequest mirrors isOpenAIModelsRequest: GET /v1/models.
func IsModelsRequest(method, originalPathAndQuery string) bool {
	if !strings.EqualFold(method, "GET") {
		return false
	}
	path, _ := SplitPathAndQuery(originalPathAndQuery)
	return normalizedPathWithoutVersion(path) == "/models"
}

// BuildUpstreamURL mirrors buildUpstreamUrl: normalize the account base URL
// (always /v1-suffixed) and append the version-stripped path + query.
func BuildUpstreamURL(baseURL, pathAndQuery string) string {
	normalizedBase := strings.TrimSpace(baseURL)
	normalizedBase = strings.TrimRight(normalizedBase, "/")
	if !strings.HasSuffix(normalizedBase, "/v1") {
		normalizedBase += "/v1"
	}
	return normalizedBase + upstreamPathSuffix(pathAndQuery)
}

func upstreamPathSuffix(pathAndQuery string) string {
	path, query := SplitPathAndQuery(pathAndQuery)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	pathWithoutVersion := normalizedPathWithoutVersion(path)
	if pathWithoutVersion == "/" {
		pathWithoutVersion = ""
	}
	return pathWithoutVersion + query
}

// endpointModeForShape mirrors openAIEndpointModeForRequestShape.
func endpointModeForShape(path string, stream bool) (gatewayproto.EndpointMode, bool) {
	family, ok := endpointFamilyFromPath(path)
	if !ok {
		return "", false
	}
	if family == FamilyChatCompletions {
		if stream {
			return gatewayproto.EndpointModeChatSSE, true
		}
		return gatewayproto.EndpointModeChatJSON, true
	}
	if stream {
		return gatewayproto.EndpointModeResponsesSSE, true
	}
	return gatewayproto.EndpointModeResponsesJSON, true
}

package gatewaypreauth

import (
	"strings"
)

// SupportedEndpointModes hunk (BUG-0175 D-155). Ports the request-side half of
// domain/openai-endpoint-modes.ts + anthropic/gemini endpoint-mode families:
// the AccountSupportedEndpointMode vocabulary token the current request shape
// requires. Dispatch candidate filtering consumes it against
// account.SupportedEndpointModes (zero consumption before this hunk).

// Account endpoint mode vocabulary (domain/types.ts AccountSupportedEndpointMode).
const (
	EndpointModeImagesJSON           = "images_json"
	EndpointModeChatJSON             = "chat_json"
	EndpointModeChatSSE              = "chat_sse"
	EndpointModeResponsesJSON        = "responses_json"
	EndpointModeResponsesSSE         = "responses_sse"
	EndpointModeMessagesJSON         = "messages_json"
	EndpointModeMessagesSSE          = "messages_sse"
	EndpointModeMessageTokenCounting = "message_token_counting"
	EndpointModeGenerateContentJSON  = "generate_content_json"
	EndpointModeGenerateContentSSE   = "generate_content_sse"
	EndpointModeCountTokens          = "count_tokens"
	EndpointModeEmbedContent         = "embed_content"
	EndpointModeInteractionsJSON     = "interactions_json"
	EndpointModeInteractionsSSE      = "interactions_sse"
)

// RequestSupportedEndpointMode mirrors openAIEndpointModeForGatewayRequest +
// the per-protocol request-shape mappers: the endpoint-mode token the request
// would exercise on a native account of its protocol family. An empty result
// means the request shape carries no gated mode (the caller must not filter).
func RequestSupportedEndpointMode(req *GatewayRequest) string {
	if req == nil {
		return ""
	}
	method := req.MethodUpper()
	path := RequestPathWithoutQuery(req)
	stream := RequestStream(req)
	switch {
	case method == "POST" && strings.Contains(path, "/chat/completions"):
		if stream {
			return EndpointModeChatSSE
		}
		return EndpointModeChatJSON
	case method == "POST" && normalizedV1StrippedPath(path) == "/responses":
		if stream {
			return EndpointModeResponsesSSE
		}
		return EndpointModeResponsesJSON
	case method == "POST" && (normalizedV1StrippedPath(path) == "/images" || strings.HasPrefix(normalizedV1StrippedPath(path), "/images/")):
		return EndpointModeImagesJSON
	case method == "POST" && normalizedV1StrippedPath(path) == "/messages":
		if stream {
			return EndpointModeMessagesSSE
		}
		return EndpointModeMessagesJSON
	case method == "POST" && strings.HasSuffix(path, "/messages/count_tokens"):
		return EndpointModeMessageTokenCounting
	case method == "POST" && strings.Contains(path, ":generateContent"):
		if stream {
			return EndpointModeGenerateContentSSE
		}
		return EndpointModeGenerateContentJSON
	case method == "POST" && strings.Contains(path, ":streamGenerateContent"):
		return EndpointModeGenerateContentSSE
	case strings.Contains(path, ":countTokens"):
		return EndpointModeCountTokens
	case method == "POST" && strings.Contains(path, ":embedContent"):
		return EndpointModeEmbedContent
	case method == "POST" && strings.Contains(path, "/interactions"):
		if stream {
			return EndpointModeInteractionsSSE
		}
		return EndpointModeInteractionsJSON
	default:
		return ""
	}
}

// RequestPathWithoutQuery returns the request path with the query string and
// any /v1-style protocol prefix preserved for suffix matching.
func RequestPathWithoutQuery(req *GatewayRequest) string {
	pathAndQuery := req.PathAndQuery()
	if pathAndQuery == "" {
		pathAndQuery = req.Path()
	}
	path := strings.SplitN(pathAndQuery, "?", 2)[0]
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return path
}

// normalizedV1StrippedPath strips a leading "/v1" or "/v1beta" protocol
// version segment (mirrors the Node ^\/v1(?=\/|$) replacement used by the
// endpoint family helpers).
func normalizedV1StrippedPath(path string) string {
	for _, prefix := range []string{"/v1/", "/v1beta/"} {
		if strings.HasPrefix(path, prefix) {
			return path[len(prefix)-1:]
		}
	}
	if path == "/v1" || path == "/v1beta" {
		return "/"
	}
	return path
}

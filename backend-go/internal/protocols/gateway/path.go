package gateway

import (
	"net/url"
	"regexp"
	"strings"
)

var geminiModelActionPattern = regexp.MustCompile(`^/models/[^/]+:(generatecontent|streamgeneratecontent|counttokens|embedcontent)$`)
var geminiInteractionPattern = regexp.MustCompile(`^/interactions(?:/[^/]+(?:/cancel)?)?$`)

func EndpointFamilyFromPath(protocol ProtocolCode, pathAndQuery string) EndpointFamily {
	switch protocol {
	case ProtocolOpenAI:
		return openAIEndpointFamily(pathAndQuery)
	case ProtocolAnthropic:
		return anthropicEndpointFamily(pathAndQuery)
	case ProtocolGemini:
		return geminiEndpointFamily(pathAndQuery)
	default:
		return EndpointUnknown
	}
}

func NativeProtocolForRequest(request RequestShape) (ProtocolCode, bool) {
	// Preserve the coexistence contract: shared OpenAI-shaped paths, including
	// /models, take precedence over an account profile or another native driver.
	if openAIEndpointFamily(request.Path) != EndpointUnknown {
		return ProtocolOpenAI, true
	}

	method := strings.ToUpper(strings.TrimSpace(request.Method))
	switch family := anthropicEndpointFamily(request.Path); family {
	case EndpointMessages, EndpointMessageTokenCounting:
		if method == "POST" {
			return ProtocolAnthropic, true
		}
	case EndpointModels:
		if method == "GET" {
			return ProtocolAnthropic, true
		}
	}

	switch family := geminiEndpointFamily(request.Path); family {
	case EndpointModels:
		if method == "GET" {
			return ProtocolGemini, true
		}
	case EndpointInteractions:
		if GeminiInteractionActionForRequest(method, request.Path) != GeminiInteractionNone {
			return ProtocolGemini, true
		}
	case EndpointGenerateContent, EndpointStreamGenerateContent, EndpointCountTokens, EndpointEmbedContent:
		if method == "POST" {
			return ProtocolGemini, true
		}
	}
	return "", false
}

func GeminiInteractionActionForRequest(method, pathAndQuery string) GeminiInteractionAction {
	method = strings.ToUpper(strings.TrimSpace(method))
	path := normalizedPath(pathAndQuery, "/v1beta")
	switch {
	case path == "/interactions":
		if method == "POST" {
			return GeminiInteractionCreate
		}
	case isGeminiInteractionResourcePath(path):
		if method == "GET" {
			return GeminiInteractionGet
		}
		if method == "DELETE" {
			return GeminiInteractionDelete
		}
	case strings.HasSuffix(path, "/cancel"):
		if method == "POST" && isGeminiInteractionResourcePath(strings.TrimSuffix(path, "/cancel")) {
			return GeminiInteractionCancel
		}
	}
	return GeminiInteractionNone
}

func ResolveDownstreamProtocol(protocol ProtocolCode, request RequestShape) DownstreamProtocol {
	method := strings.ToUpper(strings.TrimSpace(request.Method))
	family := EndpointFamilyFromPath(protocol, request.Path)
	acceptsSSE := acceptsEventStream(request.Header("accept"))
	stream := request.Stream || acceptsSSE

	switch protocol {
	case ProtocolOpenAI:
		path := normalizedPath(request.Path, "/v1")
		if method == "POST" && path == "/responses" && stream {
			return DownstreamResponsesSSE
		}
		if method == "POST" && family == EndpointChatCompletions && stream {
			return DownstreamChatCompletionsSSE
		}
	case ProtocolAnthropic:
		if method == "POST" && family == EndpointMessages && stream {
			return DownstreamMessagesSSE
		}
	case ProtocolGemini:
		query := queryValues(request.Path)
		geminiPath := normalizedPath(request.Path, "/v1beta")
		interactionQueryStream := method == "GET" && isGeminiInteractionResourcePath(geminiPath) && strings.EqualFold(query.Get("stream"), "true")
		stream = stream || interactionQueryStream
		geminiStream := stream || strings.EqualFold(query.Get("alt"), "sse")
		if method == "POST" && (family == EndpointStreamGenerateContent || (family == EndpointGenerateContent && geminiStream)) {
			return DownstreamGeminiGenerateContentSSE
		}
		interactionRequest := (method == "POST" && geminiPath == "/interactions") ||
			(method == "GET" && isGeminiInteractionResourcePath(geminiPath))
		if interactionRequest && stream {
			return DownstreamGeminiInteractionsSSE
		}
	}
	if stream {
		return DownstreamUnknownStream
	}
	return DownstreamJSON
}

func isGeminiInteractionResourcePath(path string) bool {
	if !strings.HasPrefix(path, "/interactions/") {
		return false
	}
	suffix := strings.TrimPrefix(path, "/interactions/")
	return suffix != "" && !strings.Contains(suffix, "/")
}

func openAIEndpointFamily(pathAndQuery string) EndpointFamily {
	path := normalizedPath(pathAndQuery, "/v1")
	switch {
	case path == "/chat/completions":
		return EndpointChatCompletions
	case path == "/responses" || strings.HasPrefix(path, "/responses/"):
		return EndpointResponses
	case path == "/models":
		return EndpointModels
	case path == "/images" || strings.HasPrefix(path, "/images/"):
		return EndpointImages
	case path == "/embeddings":
		return EndpointEmbeddings
	case path == "/audio" || strings.HasPrefix(path, "/audio/"):
		return EndpointAudio
	default:
		return EndpointUnknown
	}
}

func anthropicEndpointFamily(pathAndQuery string) EndpointFamily {
	path := normalizedPath(pathAndQuery, "/v1")
	switch path {
	case "/messages":
		return EndpointMessages
	case "/messages/count_tokens":
		return EndpointMessageTokenCounting
	case "/models":
		return EndpointModels
	default:
		return EndpointUnknown
	}
}

func geminiEndpointFamily(pathAndQuery string) EndpointFamily {
	path := normalizedPath(pathAndQuery, "/v1beta")
	if path == "/models" {
		return EndpointModels
	}
	if geminiInteractionPattern.MatchString(path) {
		return EndpointInteractions
	}
	match := geminiModelActionPattern.FindStringSubmatch(path)
	if len(match) != 2 {
		return EndpointUnknown
	}
	switch match[1] {
	case "generatecontent":
		return EndpointGenerateContent
	case "streamgeneratecontent":
		return EndpointStreamGenerateContent
	case "counttokens":
		return EndpointCountTokens
	case "embedcontent":
		return EndpointEmbedContent
	default:
		return EndpointUnknown
	}
}

func normalizedPath(pathAndQuery, versionPrefix string) string {
	rawPath := strings.TrimSpace(strings.SplitN(pathAndQuery, "?", 2)[0])
	if rawPath == "" {
		rawPath = "/"
	}
	if !strings.HasPrefix(rawPath, "/") {
		rawPath = "/" + rawPath
	}
	path := strings.ToLower(rawPath)
	if path == versionPrefix {
		return "/"
	}
	if strings.HasPrefix(path, versionPrefix+"/") {
		path = strings.TrimPrefix(path, versionPrefix)
	}
	return path
}

func queryValues(pathAndQuery string) url.Values {
	queryIndex := strings.IndexByte(pathAndQuery, '?')
	if queryIndex < 0 {
		return url.Values{}
	}
	values, err := url.ParseQuery(pathAndQuery[queryIndex+1:])
	if err != nil {
		return url.Values{}
	}
	return values
}

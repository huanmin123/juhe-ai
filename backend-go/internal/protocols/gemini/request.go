// Package gemini contains side-effect-free Gemini protocol classification and
// response parsing. HTTP transport and upstream ownership live elsewhere.
package gemini

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

var (
	ErrInvalidRequestTarget = errors.New("gemini: invalid request target")
	ErrInvalidQuery         = errors.New("gemini: invalid query")
	ErrAmbiguousQuery       = errors.New("gemini: ambiguous query")
	ErrInvalidIdentifier    = errors.New("gemini: invalid identifier")
)

type EndpointFamily string

const (
	EndpointUnknown               EndpointFamily = ""
	EndpointModels                EndpointFamily = "models"
	EndpointGenerateContent       EndpointFamily = "generate_content"
	EndpointStreamGenerateContent EndpointFamily = "stream_generate_content"
	EndpointCountTokens           EndpointFamily = "count_tokens"
	EndpointEmbedContent          EndpointFamily = "embed_content"
	EndpointInteractions          EndpointFamily = "interactions"
)

type EndpointMode string

const (
	ModeUnknown             EndpointMode = ""
	ModeModels              EndpointMode = "models"
	ModeGenerateContentJSON EndpointMode = "generate_content_json"
	ModeGenerateContentSSE  EndpointMode = "generate_content_sse"
	ModeCountTokens         EndpointMode = "count_tokens"
	ModeEmbedContent        EndpointMode = "embed_content"
	ModeInteractionsJSON    EndpointMode = "interactions_json"
	ModeInteractionsSSE     EndpointMode = "interactions_sse"
)

type InteractionAction string

const (
	InteractionNone   InteractionAction = ""
	InteractionCreate InteractionAction = "create"
	InteractionGet    InteractionAction = "get"
	InteractionDelete InteractionAction = "delete"
	InteractionCancel InteractionAction = "cancel"
)

type RequestInput struct {
	Method       string
	PathAndQuery string
	Accept       string
	BodyStream   bool
}

// QueryFacts deliberately records only routing facts. In particular, it never
// carries the Gemini API key value.
type QueryFacts struct {
	StreamRequested bool
	AltSSE          bool
	HasAPIKey       bool
}

type RequestClassification struct {
	Family            EndpointFamily
	Mode              EndpointMode
	InteractionAction InteractionAction
	Model             string
	InteractionID     string
	Stream            bool
	Native            bool
	Query             QueryFacts
}

var (
	modelActionPattern = regexp.MustCompile(`(?i)^/models/([^/]+):(generatecontent|streamgeneratecontent|counttokens|embedcontent)$`)
	interactionPattern = regexp.MustCompile(`(?i)^/interactions(?:/([^/]+)(/cancel)?)?$`)
)

// ClassifyRequest validates a raw request target and classifies only supported
// Gemini-native method/path combinations. Invalid method combinations retain
// their path family for diagnostics but are not marked Native.
func ClassifyRequest(input RequestInput) (RequestClassification, error) {
	path, query, err := splitRequestTarget(input.PathAndQuery)
	if err != nil {
		return RequestClassification{}, err
	}
	queryFacts, err := classifyQuery(query)
	if err != nil {
		return RequestClassification{}, err
	}
	result := RequestClassification{Query: queryFacts}
	path = stripVersion(path)
	method := strings.ToUpper(strings.TrimSpace(input.Method))

	if strings.EqualFold(path, "/models") {
		result.Family = EndpointModels
		if method == "GET" {
			result.Native = true
			result.Mode = ModeModels
		}
		return result, nil
	}

	if match := modelActionPattern.FindStringSubmatch(path); match != nil {
		model, err := decodeIdentifier(match[1], true)
		if err != nil {
			return RequestClassification{}, err
		}
		result.Model = model
		switch strings.ToLower(match[2]) {
		case "generatecontent":
			result.Family = EndpointGenerateContent
			if method == "POST" {
				result.Native = true
				result.Mode = ModeGenerateContentJSON
			}
		case "streamgeneratecontent":
			result.Family = EndpointStreamGenerateContent
			if method == "POST" {
				result.Native = true
				result.Stream = true
				result.Mode = ModeGenerateContentSSE
			}
		case "counttokens":
			result.Family = EndpointCountTokens
			if method == "POST" {
				result.Native = true
				result.Mode = ModeCountTokens
			}
		case "embedcontent":
			result.Family = EndpointEmbedContent
			if method == "POST" {
				result.Native = true
				result.Mode = ModeEmbedContent
			}
		}
		return result, nil
	}

	match := interactionPattern.FindStringSubmatch(path)
	if match == nil {
		return result, nil
	}
	result.Family = EndpointInteractions
	if match[1] != "" {
		interactionID, err := decodeIdentifier(match[1], false)
		if err != nil {
			return RequestClassification{}, err
		}
		result.InteractionID = interactionID
	}

	switch {
	case result.InteractionID == "" && match[2] == "" && method == "POST":
		result.InteractionAction = InteractionCreate
	case result.InteractionID != "" && match[2] == "" && method == "GET":
		result.InteractionAction = InteractionGet
	case result.InteractionID != "" && match[2] == "" && method == "DELETE":
		result.InteractionAction = InteractionDelete
	case result.InteractionID != "" && match[2] != "" && method == "POST":
		result.InteractionAction = InteractionCancel
	default:
		return result, nil
	}

	result.Native = true
	streamCapable := result.InteractionAction == InteractionCreate || result.InteractionAction == InteractionGet
	result.Stream = streamCapable && (queryFacts.StreamRequested || input.BodyStream || acceptsEventStream(input.Accept))
	if result.Stream {
		result.Mode = ModeInteractionsSSE
	} else {
		result.Mode = ModeInteractionsJSON
	}
	return result, nil
}

func splitRequestTarget(target string) (string, string, error) {
	if target == "" {
		return "", "", nil
	}
	if !strings.HasPrefix(target, "/") || strings.Contains(target, "#") || strings.ContainsAny(target, "\r\n\x00\\") {
		return "", "", ErrInvalidRequestTarget
	}
	path, query, hasQuery := strings.Cut(target, "?")
	if !hasQuery {
		query = ""
	}
	return path, query, nil
}

func stripVersion(path string) string {
	if len(path) >= len("/v1beta") && strings.EqualFold(path[:len("/v1beta")], "/v1beta") {
		rest := path[len("/v1beta"):]
		if rest == "" {
			return "/"
		}
		if strings.HasPrefix(rest, "/") {
			return rest
		}
	}
	return path
}

func classifyQuery(raw string) (QueryFacts, error) {
	values, err := url.ParseQuery(raw)
	if err != nil {
		return QueryFacts{}, ErrInvalidQuery
	}
	result := QueryFacts{
		HasAPIKey: len(values["key"]) > 0,
	}
	for _, value := range values["alt"] {
		if strings.EqualFold(value, "sse") {
			result.AltSSE = true
		}
	}
	streamValues := values["stream"]
	if len(streamValues) == 0 {
		return result, nil
	}
	first := strings.ToLower(streamValues[0])
	for _, value := range streamValues[1:] {
		if strings.ToLower(value) != first {
			return QueryFacts{}, ErrAmbiguousQuery
		}
	}
	result.StreamRequested = first == "true"
	return result, nil
}

func decodeIdentifier(encoded string, model bool) (string, error) {
	value, err := url.PathUnescape(encoded)
	if err != nil || !identifierAllowed(value, model) {
		return "", ErrInvalidIdentifier
	}
	return value, nil
}

func identifierAllowed(value string, model bool) bool {
	if value == "" || value == "." || value == ".." || len(value) > 512 {
		return false
	}
	for _, char := range value {
		if char <= 0x1f || char == 0x7f || char == '/' || char == '\\' || char == '?' || char == '#' || model && char == ':' {
			return false
		}
	}
	return true
}

func acceptsEventStream(accept string) bool {
	for _, item := range strings.Split(accept, ",") {
		mediaType := strings.TrimSpace(strings.SplitN(item, ";", 2)[0])
		if strings.EqualFold(mediaType, "text/event-stream") {
			return true
		}
	}
	return false
}

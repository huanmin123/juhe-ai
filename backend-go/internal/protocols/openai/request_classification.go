package openai

import (
	"mime"
	"strings"
)

// HybridEndpointFamily is the small set of OpenAI generation endpoints that
// may use the smart hybrid router. Other OpenAI-compatible POST endpoints are
// deliberately excluded.
type HybridEndpointFamily string

const (
	HybridEndpointUnknown         HybridEndpointFamily = ""
	HybridEndpointChatCompletions HybridEndpointFamily = "chat_completions"
	HybridEndpointResponses       HybridEndpointFamily = "responses"
)

// ModelKind describes only the model information needed before dispatch.
// A non-image model remains "other" because catalog ownership belongs to a
// later gateway stage.
type ModelKind string

const (
	ModelKindMissing         ModelKind = "missing"
	ModelKindOther           ModelKind = "other"
	ModelKindImageGeneration ModelKind = "image_generation"
)

// ModelHint is a normalized, side-effect-free view of the request model.
type ModelHint struct {
	Original   string
	Normalized string
	Kind       ModelKind
}

// HybridEligibilityReason is stable diagnostic output for preflight and
// tests. Ineligible requests should continue through their normal route.
type HybridEligibilityReason string

const (
	HybridEligible               HybridEligibilityReason = "eligible"
	HybridMethodNotAllowed       HybridEligibilityReason = "method_not_allowed"
	HybridUnsupportedEndpoint    HybridEligibilityReason = "unsupported_endpoint"
	HybridUnsupportedMediaType   HybridEligibilityReason = "unsupported_media_type"
	HybridEmptyBody              HybridEligibilityReason = "empty_body"
	HybridImageGenerationRequest HybridEligibilityReason = "image_generation_request"
)

// HybridRequestInput contains already-bounded request metadata. It does not
// own HTTP body reads or JSON parsing.
type HybridRequestInput struct {
	Method              string
	PathAndQuery        string
	ContentType         string
	BodyPresent         bool
	Model               string
	ImageGenerationHint bool
}

// HybridRequestClassification determines whether a request may enter smart
// hybrid scoring without performing I/O or mutating the request.
type HybridRequestClassification struct {
	Eligible       bool
	Reason         HybridEligibilityReason
	EndpointFamily HybridEndpointFamily
	Model          ModelHint
}

// ClassifyHybridRequest limits smart hybrid routing to JSON generation calls.
// In particular, generic JSON POST endpoints and image-generation calls must
// not be scored or have their model rewritten by the text hybrid router.
func ClassifyHybridRequest(input HybridRequestInput) HybridRequestClassification {
	result := HybridRequestClassification{
		EndpointFamily: HybridEndpointFamilyFromPath(input.PathAndQuery),
		Model:          ClassifyModelHint(input.Model),
	}

	switch {
	case !strings.EqualFold(strings.TrimSpace(input.Method), "POST"):
		result.Reason = HybridMethodNotAllowed
	case result.EndpointFamily == HybridEndpointUnknown:
		result.Reason = HybridUnsupportedEndpoint
	case !isApplicationJSON(input.ContentType):
		result.Reason = HybridUnsupportedMediaType
	case !input.BodyPresent:
		result.Reason = HybridEmptyBody
	case input.ImageGenerationHint || result.Model.Kind == ModelKindImageGeneration:
		result.Reason = HybridImageGenerationRequest
	default:
		result.Eligible = true
		result.Reason = HybridEligible
	}

	return result
}

// HybridEndpointFamilyFromPath classifies exact OpenAI generation paths. It
// intentionally does not decode or clean paths, which prevents encoded or
// parent-path variants from being promoted into the hybrid route.
func HybridEndpointFamilyFromPath(pathAndQuery string) HybridEndpointFamily {
	requestPath := strings.TrimSpace(pathAndQuery)
	if queryIndex := strings.IndexByte(requestPath, '?'); queryIndex >= 0 {
		requestPath = requestPath[:queryIndex]
	}
	requestPath = strings.TrimRight(requestPath, "/")

	switch requestPath {
	case "/chat/completions", "/v1/chat/completions":
		return HybridEndpointChatCompletions
	case "/responses", "/v1/responses":
		return HybridEndpointResponses
	default:
		return HybridEndpointUnknown
	}
}

// ClassifyModelHint recognizes only OpenAI image-generation model families.
// Family boundaries prevent unrelated names from acquiring the image lane.
func ClassifyModelHint(model string) ModelHint {
	original := strings.TrimSpace(model)
	normalized := strings.ToLower(original)
	kind := ModelKindOther
	if normalized == "" {
		kind = ModelKindMissing
	} else if isModelFamily(normalized, "gpt-image") || isModelFamily(normalized, "dall-e") {
		kind = ModelKindImageGeneration
	}
	return ModelHint{
		Original:   original,
		Normalized: normalized,
		Kind:       kind,
	}
}

func isApplicationJSON(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	mediaType = strings.ToLower(mediaType)
	return mediaType == "application/json" ||
		(strings.HasPrefix(mediaType, "application/") && strings.HasSuffix(mediaType, "+json"))
}

func isModelFamily(model, family string) bool {
	return model == family || strings.HasPrefix(model, family+"-")
}
